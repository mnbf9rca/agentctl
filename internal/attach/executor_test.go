package attach

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestCheckEnvironmentAcceptsITermOutsideTmuxWithoutRunningCommands(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner()
	err := New(tmuxx.New(runner), mapLookup(map[string]string{
		"TERM_PROGRAM": "iTerm.app",
	})).CheckEnvironment()

	if err != nil {
		t.Fatalf("CheckEnvironment() error = %v", err)
	}
	assertNoSessionCreation(t, runner.Calls)
	if len(runner.Calls) != 0 {
		t.Fatalf("Calls = %#v, want no commands", runner.Calls)
	}
}

func TestCheckEnvironmentRefusesBeforeRunningCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		environment     map[string]string
		wantTermProgram string
		wantTermSet     bool
		wantInsideTmux  bool
		wantPane        string
		wantText        string
	}{
		{
			name:     "TERM_PROGRAM absent",
			wantText: "TERM_PROGRAM is unset",
		},
		{
			name:        "TERM_PROGRAM set empty",
			environment: map[string]string{"TERM_PROGRAM": ""},
			wantTermSet: true,
			wantText:    `TERM_PROGRAM=""`,
		},
		{
			name:            "another terminal",
			environment:     map[string]string{"TERM_PROGRAM": "Apple_Terminal"},
			wantTermProgram: "Apple_Terminal",
			wantTermSet:     true,
			wantText:        `TERM_PROGRAM="Apple_Terminal"`,
		},
		{
			name:            "already inside tmux",
			environment:     map[string]string{"TERM_PROGRAM": "iTerm.app", "TMUX_PANE": "%9"},
			wantTermProgram: "iTerm.app",
			wantTermSet:     true,
			wantInsideTmux:  true,
			wantPane:        "%9",
			wantText:        `TMUX_PANE="%9"`,
		},
		{
			name:            "set empty pane remains inside signal",
			environment:     map[string]string{"TERM_PROGRAM": "iTerm.app", "TMUX_PANE": ""},
			wantTermProgram: "iTerm.app",
			wantTermSet:     true,
			wantInsideTmux:  true,
			wantText:        `TMUX_PANE=""`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := tmuxx.NewFakeRunner()

			err := New(tmuxx.New(runner), mapLookup(tt.environment)).CheckEnvironment()

			var environmentError *EnvironmentError
			if !errors.As(err, &environmentError) {
				t.Fatalf("CheckEnvironment() error = %T %v, want *EnvironmentError", err, err)
			}
			if environmentError.TermProgram != tt.wantTermProgram || environmentError.TermProgramSet != tt.wantTermSet || environmentError.InsideTmux != tt.wantInsideTmux || environmentError.Pane != tt.wantPane {
				t.Fatalf("EnvironmentError = %#v, want term=%q set=%v inside=%v pane=%q", environmentError, tt.wantTermProgram, tt.wantTermSet, tt.wantInsideTmux, tt.wantPane)
			}
			if !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("error = %q, want substring %q", err, tt.wantText)
			}
			assertNoSessionCreation(t, runner.Calls)
			if len(runner.Calls) != 0 {
				t.Fatalf("Calls = %#v, want no commands", runner.Calls)
			}
		})
	}
}

func TestExecuteAttachesOnlyAfterManagedVersionGateByResolvedID(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{},
	)

	err := New(tmuxx.New(runner), nil).Execute(context.Background(), tmuxx.Session{ID: "$4", Name: "fleet"})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	assertCalls(t, runner,
		tmuxx.Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"-CC", "attach-session", "-t", "$4"}},
	)
}

func TestExecuteRefusesEveryFailedOwnershipGateWithoutAttaching(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		responses   []tmuxx.Response
		wantCalls   []tmuxx.Call
		wantMessage string
	}{
		{
			name:        "managed option missing",
			responses:   []tmuxx.Response{{Stdout: nil}},
			wantCalls:   []tmuxx.Call{{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}}},
			wantMessage: `session "fleet" is not managed by agentctl`,
		},
		{
			name:        "managed option wrong",
			responses:   []tmuxx.Response{{Stdout: []byte("0\n")}},
			wantCalls:   []tmuxx.Call{{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}}},
			wantMessage: `session "fleet" is not managed by agentctl`,
		},
		{
			name:      "version option missing",
			responses: []tmuxx.Response{{Stdout: []byte("1\n")}, {Stdout: nil}},
			wantCalls: []tmuxx.Call{
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
			},
			wantMessage: "managed session carries no @agentctl_version marker",
		},
		{
			name:      "version option wrong",
			responses: []tmuxx.Response{{Stdout: []byte("1\n")}, {Stdout: []byte("2\n")}},
			wantCalls: []tmuxx.Call{
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
			},
			wantMessage: `session "fleet" has @agentctl_version="2"; expected "1"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := tmuxx.NewFakeRunner(tt.responses...)

			err := New(tmuxx.New(runner), nil).Execute(context.Background(), tmuxx.Session{ID: "$4", Name: "fleet"})

			var refusal *RefusalError
			if !errors.As(err, &refusal) {
				t.Fatalf("Execute() error = %T %v, want *RefusalError", err, err)
			}
			if refusal.Session.Name != "fleet" {
				t.Fatalf("RefusalError.Session.Name = %q, want fleet", refusal.Session.Name)
			}
			if err.Error() != tt.wantMessage {
				t.Fatalf("Execute() error = %q, want %q", err, tt.wantMessage)
			}
			assertCalls(t, runner, tt.wantCalls...)
		})
	}
}

func TestExecuteClassifiesTmuxFailuresAndStopsAtFailedOperation(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("tmux failed")
	tests := []struct {
		name      string
		responses []tmuxx.Response
		wantCalls []tmuxx.Call
	}{
		{
			name:      "managed read",
			responses: []tmuxx.Response{{Err: wantErr}},
			wantCalls: []tmuxx.Call{{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}}},
		},
		{
			name:      "version read",
			responses: []tmuxx.Response{{Stdout: []byte("1\n")}, {Err: wantErr}},
			wantCalls: []tmuxx.Call{
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
			},
		},
		{
			name:      "control-mode attach",
			responses: []tmuxx.Response{{Stdout: []byte("1\n")}, {Stdout: []byte("1\n")}, {Err: wantErr}},
			wantCalls: []tmuxx.Call{
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
				{Executable: "tmux", Args: []string{"-CC", "attach-session", "-t", "$4"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := tmuxx.NewFakeRunner(tt.responses...)

			err := New(tmuxx.New(runner), nil).Execute(context.Background(), tmuxx.Session{ID: "$4", Name: "fleet"})

			var tmuxFailure *tmuxx.TmuxError
			if !errors.As(err, &tmuxFailure) {
				t.Fatalf("Execute() error = %T %v, want *tmuxx.TmuxError", err, err)
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("Execute() error = %v, want wrapped runner error", err)
			}
			assertCalls(t, runner, tt.wantCalls...)
		})
	}
}

func TestExecutePreservesContextErrors(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(tmuxx.Response{Err: context.Canceled})
	err := New(tmuxx.New(runner), nil).Execute(context.Background(), tmuxx.Session{ID: "$4", Name: "fleet"})
	if err != context.Canceled {
		t.Fatalf("Execute() error = %T %v, want context.Canceled", err, err)
	}
	assertCalls(t, runner, tmuxx.Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}})
}

func mapLookup(values map[string]string) LookupEnv {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func assertCalls(t *testing.T, runner *tmuxx.FakeRunner, want ...tmuxx.Call) {
	t.Helper()
	assertNoSessionCreation(t, runner.Calls)
	if !reflect.DeepEqual(runner.Calls, want) {
		t.Fatalf("Calls = %#v, want %#v", runner.Calls, want)
	}
}

func assertNoSessionCreation(t *testing.T, calls []tmuxx.Call) {
	t.Helper()
	for _, call := range calls {
		for _, argument := range call.Args {
			if argument == "new-session" {
				t.Fatalf("Calls = %#v, attach must never create a session", calls)
			}
		}
	}
}
