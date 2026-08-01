package kill

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestExecuteKillsOnlyAfterManagedVersionGate(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{},
	)
	err := New(tmuxx.New(runner)).Execute(context.Background(), tmuxx.Session{ID: "$4", Name: "fleet"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	assertCalls(t, runner,
		tmuxx.Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"kill-session", "-t", "$4"}},
	)
}

func TestExecuteRefusesEveryFailedOwnershipGateWithoutKilling(t *testing.T) {
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
			wantMessage: "not managed",
		},
		{
			name:        "managed option wrong",
			responses:   []tmuxx.Response{{Stdout: []byte("0\n")}},
			wantCalls:   []tmuxx.Call{{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}}},
			wantMessage: "not managed",
		},
		{
			name:      "version option missing",
			responses: []tmuxx.Response{{Stdout: []byte("1\n")}, {Stdout: nil}},
			wantCalls: []tmuxx.Call{
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
			},
			wantMessage: "different agentctl version",
		},
		{
			name:      "version option wrong",
			responses: []tmuxx.Response{{Stdout: []byte("1\n")}, {Stdout: []byte("2\n")}},
			wantCalls: []tmuxx.Call{
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
			},
			wantMessage: "different agentctl version",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := tmuxx.NewFakeRunner(tt.responses...)
			err := New(tmuxx.New(runner)).Execute(context.Background(), tmuxx.Session{ID: "$4", Name: "fleet"})
			var refusal *RefusalError
			if !errors.As(err, &refusal) {
				t.Fatalf("Execute() error = %T %v, want *RefusalError", err, err)
			}
			if !strings.Contains(err.Error(), "fleet") || !strings.Contains(err.Error(), tt.wantMessage) {
				t.Fatalf("Execute() error = %q, want session name and %q", err, tt.wantMessage)
			}
			assertCalls(t, runner, tt.wantCalls...)
		})
	}
}

func TestExecuteClassifiesTmuxFailuresAndStopsAtTheFailedOperation(t *testing.T) {
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
			name:      "kill",
			responses: []tmuxx.Response{{Stdout: []byte("1\n")}, {Stdout: []byte("1\n")}, {Err: wantErr}},
			wantCalls: []tmuxx.Call{
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
				{Executable: "tmux", Args: []string{"kill-session", "-t", "$4"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := tmuxx.NewFakeRunner(tt.responses...)
			err := New(tmuxx.New(runner)).Execute(context.Background(), tmuxx.Session{ID: "$4", Name: "fleet"})
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
	err := New(tmuxx.New(runner)).Execute(context.Background(), tmuxx.Session{ID: "$4", Name: "fleet"})
	if err != context.Canceled {
		t.Fatalf("Execute() error = %T %v, want context.Canceled", err, err)
	}
	assertCalls(t, runner, tmuxx.Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}})
}

func assertCalls(t *testing.T, runner *tmuxx.FakeRunner, want ...tmuxx.Call) {
	t.Helper()
	if !reflect.DeepEqual(runner.Calls, want) {
		t.Fatalf("Calls = %#v, want %#v", runner.Calls, want)
	}
}
