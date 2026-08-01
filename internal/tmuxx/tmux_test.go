package tmuxx

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestListSessionsParsesTypedPairs(t *testing.T) {
	t.Parallel()

	runner := NewFakeRunner(Response{Stdout: []byte("$1\tepic123\n$2\tother\n")})
	got, err := New(runner).ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	want := []Session{{ID: "$1", Name: "epic123"}, {ID: "$2", Name: "other"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListSessions() = %#v, want %#v", got, want)
	}
	assertCalls(t, runner, Call{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}})
}

func TestListSessionsAcceptsEmptyOutput(t *testing.T) {
	t.Parallel()

	runner := NewFakeRunner(Response{})
	got, err := New(runner).ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if got != nil {
		t.Fatalf("ListSessions() = %#v, want nil", got)
	}
}

func TestListSessionsRejectsMalformedOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stdout string
	}{
		{name: "missing name field", stdout: "$1\n"},
		{name: "empty id", stdout: "\tepic123\n"},
		{name: "wrong id prefix", stdout: "@1\tepic123\n"},
		{name: "empty name", stdout: "$1\t\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := NewFakeRunner(Response{Stdout: []byte(tt.stdout)})
			if _, err := New(runner).ListSessions(context.Background()); err == nil {
				t.Fatal("ListSessions() error = nil, want malformed-output error")
			}
		})
	}
}

func TestListSessionsPropagatesRunnerError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("tmux unavailable")
	runner := NewFakeRunner(Response{Err: wantErr})
	_, err := New(runner).ListSessions(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("ListSessions() error = %v, want %v", err, wantErr)
	}
}

func TestNewSessionUsesCanonicalArgvAndParsesCreatedIDs(t *testing.T) {
	t.Parallel()

	runner := NewFakeRunner(Response{Stdout: []byte("$4\t@7\t%9\n")})
	got, err := New(runner).NewSession(context.Background(), "epic123", "planner", "/repo path", "exec amq coop exec")
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	want := CreatedSession{SessionID: "$4", WindowID: "@7", PaneID: "%9"}
	if got != want {
		t.Fatalf("NewSession() = %#v, want %#v", got, want)
	}
	assertCalls(t, runner, Call{Executable: "tmux", Args: []string{
		"new-session", "-d", "-s", "epic123", "-n", "planner", "-c", "/repo path",
		"-P", "-F", "#{session_id}\t#{window_id}\t#{pane_id}", "--", "exec amq coop exec",
	}})
}

func TestNewWindowUsesResolvedSessionIDAndParsesCreatedIDs(t *testing.T) {
	t.Parallel()

	runner := NewFakeRunner(Response{Stdout: []byte("@8\t%10\n")})
	got, err := New(runner).NewWindow(context.Background(), "$4", "codex1", "/repo", "exec amq coop exec --session epic123")
	if err != nil {
		t.Fatalf("NewWindow() error = %v", err)
	}
	want := CreatedWindow{WindowID: "@8", PaneID: "%10"}
	if got != want {
		t.Fatalf("NewWindow() = %#v, want %#v", got, want)
	}
	assertCalls(t, runner, Call{Executable: "tmux", Args: []string{
		"new-window", "-d", "-t", "$4", "-n", "codex1", "-c", "/repo",
		"-P", "-F", "#{window_id}\t#{pane_id}", "--", "exec amq coop exec --session epic123",
	}})
}

func TestCreationRejectsMalformedOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		newSession bool
		stdout     string
	}{
		{name: "session empty", newSession: true},
		{name: "session two records", newSession: true, stdout: "$1\t@2\t%3\n$4\t@5\t%6\n"},
		{name: "session too few fields", newSession: true, stdout: "$1\t@2\n"},
		{name: "session too many fields", newSession: true, stdout: "$1\t@2\t%3\textra\n"},
		{name: "session wrong session prefix", newSession: true, stdout: "@1\t@2\t%3\n"},
		{name: "session wrong window prefix", newSession: true, stdout: "$1\t$2\t%3\n"},
		{name: "session wrong pane prefix", newSession: true, stdout: "$1\t@2\t@3\n"},
		{name: "window empty"},
		{name: "window two records", stdout: "@2\t%3\n@5\t%6\n"},
		{name: "window too few fields", stdout: "@2\n"},
		{name: "window too many fields", stdout: "@2\t%3\textra\n"},
		{name: "window empty id", stdout: "\t%3\n"},
		{name: "window wrong pane prefix", stdout: "@2\t@3\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := NewFakeRunner(Response{Stdout: []byte(tt.stdout)})
			client := New(runner)
			var err error
			if tt.newSession {
				_, err = client.NewSession(context.Background(), "epic123", "planner", "/repo", "exec agent")
			} else {
				_, err = client.NewWindow(context.Background(), "$1", "worker", "/repo", "exec agent")
			}
			if err == nil {
				t.Fatal("creation error = nil, want malformed-output error")
			}
		})
	}
}

func TestSessionOptionWrappersOwnScopeAndPreserveValues(t *testing.T) {
	t.Parallel()

	runner := NewFakeRunner(
		Response{},
		Response{Stdout: []byte("two words\n")},
		Response{Stdout: []byte("two lines\n\n")},
	)
	client := New(runner)
	if err := client.SetSessionOption(context.Background(), "$4", "@agentctl_managed", "1"); err != nil {
		t.Fatalf("SetSessionOption() error = %v", err)
	}
	got, err := client.ShowSessionOption(context.Background(), "$4", "@agentctl_spacey")
	if err != nil {
		t.Fatalf("ShowSessionOption() error = %v", err)
	}
	if want := "two words"; got != want {
		t.Fatalf("ShowSessionOption() = %q, want %q", got, want)
	}
	got, err = client.ShowSessionOption(context.Background(), "$4", "@agentctl_multiline")
	if err != nil {
		t.Fatalf("ShowSessionOption() second error = %v", err)
	}
	if want := "two lines\n"; got != want {
		t.Fatalf("ShowSessionOption() second = %q, want %q", got, want)
	}
	assertCalls(t, runner,
		Call{Executable: "tmux", Args: []string{"set-option", "-t", "$4", "@agentctl_managed", "1"}},
		Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_spacey"}},
		Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_multiline"}},
	)
}

func TestWindowOptionWrappersOwnScopeAndAllowEmptyValue(t *testing.T) {
	t.Parallel()

	runner := NewFakeRunner(Response{}, Response{})
	client := New(runner)
	if err := client.SetWindowOption(context.Background(), "@7", "@agentctl_model", ""); err != nil {
		t.Fatalf("SetWindowOption() error = %v", err)
	}
	got, err := client.ShowWindowOption(context.Background(), "@7", "@agentctl_model")
	if err != nil {
		t.Fatalf("ShowWindowOption() error = %v", err)
	}
	if got != "" {
		t.Fatalf("ShowWindowOption() = %q, want empty", got)
	}
	assertCalls(t, runner,
		Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@7", "@agentctl_model", ""}},
		Call{Executable: "tmux", Args: []string{"show-options", "-wqv", "-t", "@7", "@agentctl_model"}},
	)
}

func TestTypedTargetsRejectNamesBeforeRunningTmux(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(Client) error
	}{
		{name: "new window session", run: func(client Client) error {
			_, err := client.NewWindow(context.Background(), "epic123", "worker", "/repo", "exec agent")
			return err
		}},
		{name: "session option", run: func(client Client) error {
			return client.SetSessionOption(context.Background(), "epic123", "@k", "v")
		}},
		{name: "window option", run: func(client Client) error {
			return client.SetWindowOption(context.Background(), "worker", "@k", "v")
		}},
		{name: "kill", run: func(client Client) error {
			return client.KillSession(context.Background(), "epic123")
		}},
		{name: "display", run: func(client Client) error {
			_, err := client.DisplayMessage(context.Background(), "pane")
			return err
		}},
		{name: "attach", run: func(client Client) error {
			return client.AttachSession(context.Background(), "epic123")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := NewFakeRunner()
			if err := tt.run(New(runner)); err == nil {
				t.Fatal("wrapper error = nil, want invalid typed ID error")
			}
			if len(runner.Calls) != 0 {
				t.Fatalf("Calls = %#v, want no external command", runner.Calls)
			}
		})
	}
}

func TestKillSessionUsesResolvedID(t *testing.T) {
	t.Parallel()

	runner := NewFakeRunner(Response{})
	if err := New(runner).KillSession(context.Background(), "$4"); err != nil {
		t.Fatalf("KillSession() error = %v", err)
	}
	assertCalls(t, runner, Call{Executable: "tmux", Args: []string{"kill-session", "-t", "$4"}})
}

func TestDisplayMessageTargetsCurrentPaneID(t *testing.T) {
	t.Parallel()

	runner := NewFakeRunner(Response{Stdout: []byte("epic123\n")})
	got, err := New(runner).DisplayMessage(context.Background(), "%9")
	if err != nil {
		t.Fatalf("DisplayMessage() error = %v", err)
	}
	if want := "epic123"; got != want {
		t.Fatalf("DisplayMessage() = %q, want %q", got, want)
	}
	assertCalls(t, runner, Call{Executable: "tmux", Args: []string{"display-message", "-p", "-t", "%9", "#{session_name}"}})
}

func TestAttachSessionPlacesGlobalControlModeOptionFirst(t *testing.T) {
	t.Parallel()

	runner := NewFakeRunner(Response{})
	if err := New(runner).AttachSession(context.Background(), "$4"); err != nil {
		t.Fatalf("AttachSession() error = %v", err)
	}
	assertCalls(t, runner, Call{Executable: "tmux", Args: []string{"-CC", "attach-session", "-t", "$4"}})
}

func assertCalls(t *testing.T, runner *FakeRunner, want ...Call) {
	t.Helper()
	if !reflect.DeepEqual(runner.Calls, want) {
		t.Fatalf("Calls = %#v, want %#v", runner.Calls, want)
	}
}
