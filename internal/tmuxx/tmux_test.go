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

	runner := NewFakeRunner(Response{Stdout: []byte("$4\t@7\t%9\t4321\n")})
	got, err := New(runner).NewSession(context.Background(), "epic123", "planner", "/repo path", "exec amq coop exec", nil)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	want := CreatedSession{SessionID: "$4", WindowID: "@7", PaneID: "%9", PanePID: 4321}
	if got != want {
		t.Fatalf("NewSession() = %#v, want %#v", got, want)
	}
	assertCalls(t, runner, Call{Executable: "tmux", Args: []string{
		"new-session", "-d", "-s", "epic123", "-n", "planner", "-c", "/repo path",
		"-P", "-F", "#{session_id}\t#{window_id}\t#{pane_id}\t#{pane_pid}", "--", "exec amq coop exec",
	}})
}

func TestNewSessionPassesEachEnvironmentVariableAsSeparateArgvPair(t *testing.T) {
	t.Parallel()

	runner := NewFakeRunner(Response{Stdout: []byte("$4\t@7\t%9\t4321\n")})
	environment := []EnvVar{
		{Name: "AGENTCTL_SESSION", Value: "epic123"},
		{Name: "AGENTCTL_ROLE", Value: "planner"},
		{Name: "AGENTCTL_MANAGED", Value: "1"},
	}
	if _, err := New(runner).NewSession(context.Background(), "epic123", "planner", "/repo path", "exec amq coop exec", environment); err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	assertCalls(t, runner, Call{Executable: "tmux", Args: []string{
		"new-session", "-d", "-s", "epic123", "-n", "planner", "-c", "/repo path",
		"-e", "AGENTCTL_SESSION=epic123", "-e", "AGENTCTL_ROLE=planner", "-e", "AGENTCTL_MANAGED=1",
		"-P", "-F", "#{session_id}\t#{window_id}\t#{pane_id}\t#{pane_pid}", "--", "exec amq coop exec",
	}})
}

func TestNewWindowUsesResolvedSessionIDAndParsesCreatedIDs(t *testing.T) {
	t.Parallel()

	runner := NewFakeRunner(Response{Stdout: []byte("@8\t%10\t5432\n")})
	got, err := New(runner).NewWindow(context.Background(), "$4", "codex1", "/repo", "exec amq coop exec --session epic123", nil)
	if err != nil {
		t.Fatalf("NewWindow() error = %v", err)
	}
	want := CreatedWindow{WindowID: "@8", PaneID: "%10", PanePID: 5432}
	if got != want {
		t.Fatalf("NewWindow() = %#v, want %#v", got, want)
	}
	assertCalls(t, runner, Call{Executable: "tmux", Args: []string{
		"new-window", "-d", "-t", "$4", "-n", "codex1", "-c", "/repo",
		"-P", "-F", "#{window_id}\t#{pane_id}\t#{pane_pid}", "--", "exec amq coop exec --session epic123",
	}})
}

func TestNewWindowPassesEachEnvironmentVariableAsSeparateArgvPair(t *testing.T) {
	t.Parallel()

	runner := NewFakeRunner(Response{Stdout: []byte("@8\t%10\t5432\n")})
	environment := []EnvVar{
		{Name: "AGENTCTL_SESSION", Value: "epic123"},
		{Name: "AGENTCTL_ROLE", Value: "codex1"},
		{Name: "AGENTCTL_MANAGED", Value: "1"},
	}
	if _, err := New(runner).NewWindow(context.Background(), "$4", "codex1", "/repo", "exec amq coop exec", environment); err != nil {
		t.Fatalf("NewWindow() error = %v", err)
	}
	assertCalls(t, runner, Call{Executable: "tmux", Args: []string{
		"new-window", "-d", "-t", "$4", "-n", "codex1", "-c", "/repo",
		"-e", "AGENTCTL_SESSION=epic123", "-e", "AGENTCTL_ROLE=codex1", "-e", "AGENTCTL_MANAGED=1",
		"-P", "-F", "#{window_id}\t#{pane_id}\t#{pane_pid}", "--", "exec amq coop exec",
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
		{name: "session two records", newSession: true, stdout: "$1\t@2\t%3\t10\n$4\t@5\t%6\t11\n"},
		{name: "session missing pane pid", newSession: true, stdout: "$1\t@2\t%3\n"},
		{name: "session too many fields", newSession: true, stdout: "$1\t@2\t%3\t10\textra\n"},
		{name: "session wrong session prefix", newSession: true, stdout: "@1\t@2\t%3\t10\n"},
		{name: "session wrong window prefix", newSession: true, stdout: "$1\t$2\t%3\t10\n"},
		{name: "session wrong pane prefix", newSession: true, stdout: "$1\t@2\t@3\t10\n"},
		{name: "session nonnumeric pane pid", newSession: true, stdout: "$1\t@2\t%3\tpid\n"},
		{name: "session signed pane pid", newSession: true, stdout: "$1\t@2\t%3\t+10\n"},
		{name: "session zero pane pid", newSession: true, stdout: "$1\t@2\t%3\t0\n"},
		{name: "window empty"},
		{name: "window two records", stdout: "@2\t%3\t10\n@5\t%6\t11\n"},
		{name: "window missing pane pid", stdout: "@2\t%3\n"},
		{name: "window too many fields", stdout: "@2\t%3\t10\textra\n"},
		{name: "window empty id", stdout: "\t%3\t10\n"},
		{name: "window wrong pane prefix", stdout: "@2\t@3\t10\n"},
		{name: "window nonnumeric pane pid", stdout: "@2\t%3\tpid\n"},
		{name: "window signed pane pid", stdout: "@2\t%3\t+10\n"},
		{name: "window zero pane pid", stdout: "@2\t%3\t0\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := NewFakeRunner(Response{Stdout: []byte(tt.stdout)})
			client := New(runner)
			var err error
			if tt.newSession {
				_, err = client.NewSession(context.Background(), "epic123", "planner", "/repo", "exec agent", nil)
			} else {
				_, err = client.NewWindow(context.Background(), "$1", "worker", "/repo", "exec agent", nil)
			}
			if err == nil {
				t.Fatal("creation error = nil, want malformed-output error")
			}
			if !errors.Is(err, ErrCreationOutput) {
				t.Fatalf("creation error = %v, want ErrCreationOutput", err)
			}
		})
	}
}

func TestCreationRunnerErrorsAreNotCreationOutputErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(Client) error
	}{
		{name: "session", run: func(client Client) error {
			_, err := client.NewSession(context.Background(), "epic123", "planner", "/repo", "exec agent", nil)
			return err
		}},
		{name: "window", run: func(client Client) error {
			_, err := client.NewWindow(context.Background(), "$1", "worker", "/repo", "exec agent", nil)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			wantErr := errors.New("tmux unavailable")
			err := tt.run(New(NewFakeRunner(Response{Err: wantErr})))
			if !errors.Is(err, wantErr) {
				t.Fatalf("creation error = %v, want runner error %v", err, wantErr)
			}
			if errors.Is(err, ErrCreationOutput) {
				t.Fatalf("creation error = %v, must not wrap ErrCreationOutput", err)
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
			_, err := client.NewWindow(context.Background(), "epic123", "worker", "/repo", "exec agent", nil)
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

func TestAttachSessionUsesInteractiveRunnerOperation(t *testing.T) {
	t.Parallel()

	runner := &operationRunner{}
	if err := New(runner).AttachSession(context.Background(), "$4"); err != nil {
		t.Fatalf("AttachSession() error = %v", err)
	}
	if runner.outputCalled {
		t.Fatal("AttachSession() used output-capturing operation")
	}
	if !runner.interactiveCalled {
		t.Fatal("AttachSession() did not use interactive operation")
	}
}

type operationRunner struct {
	outputCalled      bool
	interactiveCalled bool
}

func (r *operationRunner) Output(context.Context, string, ...string) ([]byte, error) {
	r.outputCalled = true
	return nil, nil
}

func (r *operationRunner) RunInteractive(context.Context, string, ...string) error {
	r.interactiveCalled = true
	return nil
}

func TestListWindowsParsesMetadataAndPreservesProcessResidue(t *testing.T) {
	t.Parallel()

	runner := NewFakeRunner(Response{Stdout: []byte(
		"@7\tplanner\t1\t1\tplanner\tclaude\tfable\tweird name\twith tab\n" +
			"@8\tcodex1\t1\t1\tcodex1\tcodex\t\tcodex\n",
	)})
	got, err := New(runner).ListWindows(context.Background(), "$4")
	if err != nil {
		t.Fatalf("ListWindows() error = %v", err)
	}
	want := []Window{
		{ID: "@7", Name: "planner", Managed: "1", Version: "1", Role: "planner", Harness: "claude", Model: "fable", Process: "weird name\twith tab"},
		{ID: "@8", Name: "codex1", Managed: "1", Version: "1", Role: "codex1", Harness: "codex", Model: "", Process: "codex"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListWindows() = %#v, want %#v", got, want)
	}
	assertCalls(t, runner, Call{Executable: "tmux", Args: []string{
		"list-windows", "-t", "$4", "-F",
		"#{window_id}\t#{window_name}\t#{@agentctl_managed}\t#{@agentctl_version}\t#{@agentctl_role}\t#{@agentctl_harness}\t#{@agentctl_model}\t#{@agentctl_process}",
	}})
}

func TestListWindowsAcceptsEmptyOutput(t *testing.T) {
	t.Parallel()

	runner := NewFakeRunner(Response{})
	got, err := New(runner).ListWindows(context.Background(), "$4")
	if err != nil {
		t.Fatalf("ListWindows() error = %v", err)
	}
	if got != nil {
		t.Fatalf("ListWindows() = %#v, want nil", got)
	}
}

func TestListWindowsRejectsMalformedOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stdout string
	}{
		{name: "too few fields", stdout: "@7\tplanner\t1\t1\tplanner\tclaude\tfable\n"},
		{name: "wrong id prefix", stdout: "$7\tplanner\t1\t1\tplanner\tclaude\tfable\tclaude\n"},
		{name: "empty name", stdout: "@7\t\t1\t1\tplanner\tclaude\tfable\tclaude\n"},
		{name: "blank trailing record", stdout: "@7\tplanner\t1\t1\tplanner\tclaude\tfable\tclaude\n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := NewFakeRunner(Response{Stdout: []byte(tt.stdout)})
			if _, err := New(runner).ListWindows(context.Background(), "$4"); err == nil {
				t.Fatal("ListWindows() error = nil, want malformed-output error")
			}
		})
	}
}

func TestListPanesParsesTypedState(t *testing.T) {
	t.Parallel()

	runner := NewFakeRunner(Response{Stdout: []byte("%9\t1234\t0\t1\n%10\t999\t1\t2\n")})
	got, err := New(runner).ListPanes(context.Background(), "@7")
	if err != nil {
		t.Fatalf("ListPanes() error = %v", err)
	}
	want := []Pane{
		{ID: "%9", PID: 1234, Dead: false, WindowPanes: 1},
		{ID: "%10", PID: 999, Dead: true, WindowPanes: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListPanes() = %#v, want %#v", got, want)
	}
	assertCalls(t, runner, Call{Executable: "tmux", Args: []string{
		"list-panes", "-t", "@7", "-F", "#{pane_id}\t#{pane_pid}\t#{pane_dead}\t#{window_panes}",
	}})
}

func TestListPanesAcceptsEmptyOutput(t *testing.T) {
	t.Parallel()

	runner := NewFakeRunner(Response{})
	got, err := New(runner).ListPanes(context.Background(), "@7")
	if err != nil {
		t.Fatalf("ListPanes() error = %v", err)
	}
	if got != nil {
		t.Fatalf("ListPanes() = %#v, want nil", got)
	}
}

func TestListPanesRejectsMalformedOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stdout string
	}{
		{name: "too few fields", stdout: "%9\t1234\t0\n"},
		{name: "too many fields", stdout: "%9\t1234\t0\t1\textra\n"},
		{name: "wrong id prefix", stdout: "@9\t1234\t0\t1\n"},
		{name: "pid nonnumeric", stdout: "%9\tpid\t0\t1\n"},
		{name: "pid zero", stdout: "%9\t0\t0\t1\n"},
		{name: "dead invalid", stdout: "%9\t1234\tfalse\t1\n"},
		{name: "count nonnumeric", stdout: "%9\t1234\t0\tpanes\n"},
		{name: "count zero", stdout: "%9\t1234\t0\t0\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := NewFakeRunner(Response{Stdout: []byte(tt.stdout)})
			if _, err := New(runner).ListPanes(context.Background(), "@7"); err == nil {
				t.Fatal("ListPanes() error = nil, want malformed-output error")
			}
		})
	}
}

func TestCollectionWrappersRejectNameTargetsBeforeRunningTmux(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(Client) error
	}{
		{name: "windows", run: func(client Client) error {
			_, err := client.ListWindows(context.Background(), "epic123")
			return err
		}},
		{name: "panes", run: func(client Client) error {
			_, err := client.ListPanes(context.Background(), "planner")
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := NewFakeRunner()
			if err := tt.run(New(runner)); err == nil {
				t.Fatal("collection error = nil, want invalid typed ID error")
			}
			if len(runner.Calls) != 0 {
				t.Fatalf("Calls = %#v, want no external command", runner.Calls)
			}
		})
	}
}

func assertCalls(t *testing.T, runner *FakeRunner, want ...Call) {
	t.Helper()
	if !reflect.DeepEqual(runner.Calls, want) {
		t.Fatalf("Calls = %#v, want %#v", runner.Calls, want)
	}
}
