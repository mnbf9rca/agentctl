package main

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestRunAttachRefusesInvalidEnvironmentBeforeSessionResolution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		lookup   map[string]string
		wantText string
	}{
		{name: "TERM_PROGRAM absent", wantText: "TERM_PROGRAM is unset"},
		{name: "another terminal", lookup: map[string]string{"TERM_PROGRAM": "Apple_Terminal"}, wantText: `TERM_PROGRAM="Apple_Terminal"`},
		{name: "inside tmux", lookup: map[string]string{"TERM_PROGRAM": "iTerm.app", "TMUX_PANE": "%9"}, wantText: `TMUX_PANE="%9"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := tmuxx.NewFakeRunner(tmuxx.Response{Stdout: []byte("$4\tfleet\n")})
			var stdout, stderr bytes.Buffer

			code := runWithRunner(context.Background(), []string{"attach", "--session", "fleet"}, &stdout, &stderr, runner, lookupValues(tt.lookup))

			if code != exitNotImplemented {
				t.Fatalf("runWithRunner() = %d, want %d; stderr = %q", code, exitNotImplemented, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantText) {
				t.Fatalf("stderr = %q, want substring %q", stderr.String(), tt.wantText)
			}
			if strings.Contains(stderr.String(), "tmux operation failed") {
				t.Fatalf("stderr = %q, must not claim a tmux failure", stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			assertAttachCalls(t, runner)
		})
	}
}

func TestRunAttachParsesUsageAndHelpBeforeEnvironmentPreflight(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		arguments  []string
		wantCode   int
		wantOutput string
	}{
		{name: "invalid arguments", arguments: []string{"attach", "extra"}, wantCode: exitUsage, wantOutput: "accepts no positional arguments"},
		{name: "help", arguments: []string{"attach", "--help"}, wantCode: exitOK, wantOutput: "Usage: agentctl attach"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := tmuxx.NewFakeRunner()
			var stdout, stderr bytes.Buffer

			code := runWithRunner(context.Background(), tt.arguments, &stdout, &stderr, runner, lookupValues(nil))

			if code != tt.wantCode {
				t.Fatalf("runWithRunner() = %d, want %d", code, tt.wantCode)
			}
			if !strings.Contains(stdout.String()+stderr.String(), tt.wantOutput) {
				t.Fatalf("stdout = %q, stderr = %q; want substring %q", stdout.String(), stderr.String(), tt.wantOutput)
			}
			assertAttachCalls(t, runner)
		})
	}
}

func TestRunAttachUsesResolverAndStopsOnMissingOrAmbiguousSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		sessions string
		wantText string
	}{
		{name: "missing", sessions: "$1\tother\n", wantText: "not found"},
		{name: "ambiguous", sessions: "$4\tfleet\n$5\tfleet\n", wantText: "ambiguous"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := tmuxx.NewFakeRunner(tmuxx.Response{Stdout: []byte(tt.sessions)})
			var stdout, stderr bytes.Buffer

			code := runWithRunner(context.Background(), []string{"attach", "--session", "fleet"}, &stdout, &stderr, runner, lookupValues(map[string]string{"TERM_PROGRAM": "iTerm.app"}))

			if code != exitSession {
				t.Fatalf("runWithRunner() = %d, want %d; stderr = %q", code, exitSession, stderr.String())
			}
			if !strings.Contains(stderr.String(), "fleet") || !strings.Contains(stderr.String(), tt.wantText) {
				t.Fatalf("stderr = %q, want fleet and %q", stderr.String(), tt.wantText)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			assertAttachCalls(t, runner,
				tmuxx.Call{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}},
			)
		})
	}
}

func TestRunAttachRefusesEveryFailedOwnershipGateWithEscapeHatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		responses   []tmuxx.Response
		wantCalls   []tmuxx.Call
		wantMessage string
	}{
		{
			name: "unmanaged",
			responses: []tmuxx.Response{
				{Stdout: []byte("$4\tfleet\n")},
				{Stdout: nil},
			},
			wantCalls: []tmuxx.Call{
				{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
			},
			wantMessage: "agentctl: refusing to attach; session \"fleet\" is not managed by agentctl; to attach anyway, run: tmux -CC attach-session -t '=fleet'\n",
		},
		{
			name: "different version",
			responses: []tmuxx.Response{
				{Stdout: []byte("$4\tfleet\n")},
				{Stdout: []byte("1\n")},
				{Stdout: []byte("2\n")},
			},
			wantCalls: []tmuxx.Call{
				{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
			},
			wantMessage: "agentctl: refusing to attach; session \"fleet\" was created by a different agentctl version; to attach anyway, run: tmux -CC attach-session -t '=fleet'\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := tmuxx.NewFakeRunner(tt.responses...)
			var stdout, stderr bytes.Buffer

			code := runWithRunner(context.Background(), []string{"attach", "--session", "fleet"}, &stdout, &stderr, runner, lookupValues(map[string]string{"TERM_PROGRAM": "iTerm.app"}))

			if code != exitSession {
				t.Fatalf("runWithRunner() = %d, want %d; stderr = %q", code, exitSession, stderr.String())
			}
			if stderr.String() != tt.wantMessage {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tt.wantMessage)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			assertAttachCalls(t, runner, tt.wantCalls...)
		})
	}
}

func TestRunAttachAttemptsControlModeByResolvedID(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("$4\tfleet\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{},
	)
	var stdout, stderr bytes.Buffer

	code := runWithRunner(context.Background(), []string{"attach", "--session", "fleet"}, &stdout, &stderr, runner, lookupValues(map[string]string{"TERM_PROGRAM": "iTerm.app"}))

	if code != exitOK {
		t.Fatalf("runWithRunner() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	if want := "agentctl: attempted iTerm2 control-mode attachment to session \"fleet\"\n"; stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertAttachCalls(t, runner,
		tmuxx.Call{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"-CC", "attach-session", "-t", "$4"}},
	)
}

func TestRunAttachMapsEveryTmuxFailureToTmuxExit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		responses []tmuxx.Response
	}{
		{
			name: "resolver",
			responses: []tmuxx.Response{
				{Err: assertError("list failed")},
			},
		},
		{
			name: "managed read",
			responses: []tmuxx.Response{
				{Stdout: []byte("$4\tfleet\n")},
				{Err: assertError("managed read failed")},
			},
		},
		{
			name: "version read",
			responses: []tmuxx.Response{
				{Stdout: []byte("$4\tfleet\n")},
				{Stdout: []byte("1\n")},
				{Err: assertError("version read failed")},
			},
		},
		{
			name: "control mode",
			responses: []tmuxx.Response{
				{Stdout: []byte("$4\tfleet\n")},
				{Stdout: []byte("1\n")},
				{Stdout: []byte("1\n")},
				{Err: assertError("control mode failed")},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := tmuxx.NewFakeRunner(tt.responses...)
			var stdout, stderr bytes.Buffer

			code := runWithRunner(context.Background(), []string{"attach", "--session", "fleet"}, &stdout, &stderr, runner, lookupValues(map[string]string{"TERM_PROGRAM": "iTerm.app"}))

			if code != exitTmux {
				t.Fatalf("runWithRunner() = %d, want %d; stderr = %q", code, exitTmux, stderr.String())
			}
			if !strings.Contains(stderr.String(), "failed") {
				t.Fatalf("stderr = %q, want tmux cause", stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			assertAttachNeverCreates(t, runner.Calls)
		})
	}
}

type assertError string

func (e assertError) Error() string { return string(e) }

func assertAttachCalls(t *testing.T, runner *tmuxx.FakeRunner, want ...tmuxx.Call) {
	t.Helper()
	assertAttachNeverCreates(t, runner.Calls)
	if !reflect.DeepEqual(runner.Calls, want) {
		t.Fatalf("Calls = %#v, want %#v", runner.Calls, want)
	}
}

func assertAttachNeverCreates(t *testing.T, calls []tmuxx.Call) {
	t.Helper()
	for _, call := range calls {
		for _, argument := range call.Args {
			if argument == "new-session" {
				t.Fatalf("Calls = %#v, attach must never create a session", calls)
			}
		}
	}
}
