package main

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/buildinfo"
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
		{name: "TERM_PROGRAM set empty", lookup: map[string]string{"TERM_PROGRAM": ""}, wantText: `TERM_PROGRAM=""`},
		{name: "another terminal", lookup: map[string]string{"TERM_PROGRAM": "Apple_Terminal"}, wantText: `TERM_PROGRAM="Apple_Terminal"`},
		{name: "inside tmux", lookup: map[string]string{"TERM_PROGRAM": "iTerm.app", "TMUX_PANE": "%9"}, wantText: `TMUX_PANE="%9"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := tmuxx.NewFakeRunner(tmuxx.Response{Stdout: []byte("$4\tfleet\n")})
			var stdout, stderr bytes.Buffer

			code := runWithRunner(context.Background(), []string{"attach", "--session", "fleet"}, &stdout, &stderr, runner, lookupValues(tt.lookup))

			if code != exitUnclassified {
				t.Fatalf("runWithRunner() = %d, want %d; stderr = %q", code, exitUnclassified, stderr.String())
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
			name: "version marker absent",
			responses: []tmuxx.Response{
				{Stdout: []byte("$4\tfleet\n")},
				{Stdout: []byte("1\n")},
				{Stdout: nil},
			},
			wantCalls: []tmuxx.Call{
				{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
			},
			wantMessage: "agentctl: refusing to attach; managed session carries no @agentctl_version marker; to attach anyway, run: tmux -CC attach-session -t '=fleet'\n",
		},
		{
			name: "version marker observed wrong",
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
			wantMessage: "agentctl: refusing to attach; session \"fleet\" has @agentctl_version=\"2\"; expected \"1\"; to attach anyway, run: tmux -CC attach-session -t '=fleet'\n",
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

var attachNotice = "agentctl " + buildinfo.Current() + "\n" +
	"Attaching session \"fleet\" (3 windows) in iTerm2.\n\n" +
	"iTerm2 will now show its Command Menu. That menu is iTerm2's, not agentctl's:\n\n" +
	"  esc   detach cleanly — the tabs close and the fleet keeps running\n" +
	"  X     (uppercase) force-quit — the fleet keeps running, but the tmux client\n" +
	"        does not exit, so this terminal stays busy and agentctl cannot report.\n" +
	"        Prefer esc.\n\n" +
	"Detaching never stops the fleet. To stop it: agentctl kill --session fleet\n"

var attachNoticeWithoutCount = "agentctl " + buildinfo.Current() + "\n" +
	"Attaching session \"fleet\" in iTerm2.\n\n" +
	"iTerm2 will now show its Command Menu. That menu is iTerm2's, not agentctl's:\n\n" +
	"  esc   detach cleanly — the tabs close and the fleet keeps running\n" +
	"  X     (uppercase) force-quit — the fleet keeps running, but the tmux client\n" +
	"        does not exit, so this terminal stays busy and agentctl cannot report.\n" +
	"        Prefer esc.\n\n" +
	"Detaching never stops the fleet. To stop it: agentctl kill --session fleet\n"

const attachWindows = "@7\tplanner\t1\t1\tplanner\tclaude\t\t\tclaude\n" +
	"@8\tcoder\t1\t1\tcoder\tcodex\t\t\tcodex\n" +
	"@9\treviewer\t1\t1\treviewer\tclaude\t\t\tclaude\n"

var attachListWindowsCall = tmuxx.Call{Executable: "tmux", Args: []string{
	"list-windows", "-t", "$4", "-F",
	"#{window_id}\t#{window_name}\t#{@agentctl_managed}\t#{@agentctl_version}\t#{@agentctl_role}\t#{@agentctl_harness}\t#{@agentctl_model}\t#{@agentctl_effort}\t#{@agentctl_process}",
}}

func TestRunAttachAttemptsControlModeByResolvedIDAndReportsTheSessionStateItObserved(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		afterexit  tmuxx.Response
		wantReport string
	}{
		{
			name:      "session still present",
			afterexit: tmuxx.Response{Stdout: []byte("$4\tfleet\n")},
			wantReport: "Attachment to session \"fleet\" ended (tmux exit 0). Session $4 is still running.\n\n" +
				"  re-attach:     agentctl attach --session fleet\n" +
				"  check status:  agentctl status --session fleet\n" +
				"  stop it:       agentctl kill --session fleet\n",
		},
		{
			name:       "session gone",
			afterexit:  tmuxx.Response{Stdout: []byte("$7\tother\n")},
			wantReport: "Attachment to session \"fleet\" ended (tmux exit 0). Session $4 is no longer present.\n",
		},
		{
			name:      "state unverifiable",
			afterexit: tmuxx.Response{Err: assertError("list failed")},
			wantReport: "Attachment to session \"fleet\" ended (tmux exit 0). Could not verify whether session $4 is still running: tmux list sessions: list failed\n\n" +
				"  check status:  agentctl status --session fleet\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := tmuxx.NewFakeRunner(
				tmuxx.Response{Stdout: []byte("$4\tfleet\n")},
				tmuxx.Response{Stdout: []byte("1\n")},
				tmuxx.Response{Stdout: []byte("1\n")},
				tmuxx.Response{Stdout: []byte(attachWindows)},
				tmuxx.Response{},
				tt.afterexit,
			)
			var stdout, stderr bytes.Buffer

			code := runWithRunner(context.Background(), []string{"attach", "--session", "fleet"}, &stdout, &stderr, runner, lookupValues(map[string]string{"TERM_PROGRAM": "iTerm.app"}))

			if code != exitOK {
				t.Fatalf("runWithRunner() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
			}
			if want := attachNotice + tt.wantReport; stdout.String() != want {
				t.Fatalf("stdout = %q, want %q", stdout.String(), want)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
			assertAttachCalls(t, runner,
				tmuxx.Call{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}},
				tmuxx.Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
				tmuxx.Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
				attachListWindowsCall,
				tmuxx.Call{Executable: "tmux", Args: []string{"-CC", "attach-session", "-t", "$4"}},
				tmuxx.Call{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}},
			)
		})
	}
}

func TestRunAttachOmitsWindowCountWhenItsReadFails(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("$4\tfleet\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Err: assertError("count failed")},
		tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("$4\tfleet\n")},
	)
	var stdout, stderr bytes.Buffer

	code := runWithRunner(context.Background(), []string{"attach", "--session", "fleet"}, &stdout, &stderr, runner, lookupValues(map[string]string{"TERM_PROGRAM": "iTerm.app"}))

	if code != exitOK {
		t.Fatalf("runWithRunner() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	want := attachNoticeWithoutCount +
		"Attachment to session \"fleet\" ended (tmux exit 0). Session $4 is still running.\n\n" +
		"  re-attach:     agentctl attach --session fleet\n" +
		"  check status:  agentctl status --session fleet\n" +
		"  stop it:       agentctl kill --session fleet\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertAttachCalls(t, runner,
		tmuxx.Call{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
		attachListWindowsCall,
		tmuxx.Call{Executable: "tmux", Args: []string{"-CC", "attach-session", "-t", "$4"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}},
	)
}

func TestRunAttachMapsEveryTmuxFailureToTmuxExit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		responses  []tmuxx.Response
		wantStdout string
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
				{Stdout: []byte(attachWindows)},
				{Err: assertError("control mode failed")},
			},
			wantStdout: attachNotice,
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
			if stdout.String() != tt.wantStdout {
				t.Fatalf("stdout = %q, want %q", stdout.String(), tt.wantStdout)
			}
			if strings.Contains(stdout.String(), "ended (tmux exit 0)") {
				t.Fatalf("stdout = %q, must not report a completed attachment", stdout.String())
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
