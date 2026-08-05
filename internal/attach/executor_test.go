package attach

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/buildinfo"
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
	restoreBuildStamp(t, "0.2.0")

	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte(
			"@7\tplanner\t1\t1\tplanner\tclaude\t\t\tclaude\n" +
				"@8\tcoder\t1\t1\tcoder\tcodex\t\t\tcodex\n" +
				"@9\treviewer\t1\t1\treviewer\tclaude\t\t\tclaude\n",
		)},
		tmuxx.Response{},
	)
	var notice bytes.Buffer

	err := New(tmuxx.New(runner), nil).Execute(context.Background(), tmuxx.Session{ID: "$4", Name: "fleet"}, &notice)

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	want := "agentctl 0.2.0\n" +
		"Attaching session \"fleet\" (3 windows) in iTerm2.\n\n" +
		"iTerm2 will now show its Command Menu. That menu is iTerm2's, not agentctl's:\n\n" +
		"  esc   detach cleanly — the tabs close and the fleet keeps running\n" +
		"  X     (uppercase) force-quit — the fleet keeps running, but the tmux client\n" +
		"        does not exit, so this terminal stays busy and agentctl cannot report.\n" +
		"        Prefer esc.\n\n" +
		"Detaching never stops the fleet. To stop it: agentctl kill --session fleet\n"
	if notice.String() != want {
		t.Fatalf("notice = %q, want %q", notice.String(), want)
	}
	assertCalls(t, runner,
		tmuxx.Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
		tmuxx.Call{Executable: "tmux", Args: []string{
			"list-windows", "-t", "$4", "-F",
			"#{window_id}\t#{window_name}\t#{@agentctl_managed}\t#{@agentctl_version}\t#{@agentctl_role}\t#{@agentctl_harness}\t#{@agentctl_model}\t#{@agentctl_effort}\t#{@agentctl_process}",
		}},
		tmuxx.Call{Executable: "tmux", Args: []string{"-CC", "attach-session", "-t", "$4"}},
	)
}

func TestExecuteOmitsWindowCountWhenTheAdvisoryReadFails(t *testing.T) {
	restoreBuildStamp(t, "0.2.0")

	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Err: errors.New("count failed")},
		tmuxx.Response{},
	)
	var narration bytes.Buffer

	err := New(tmuxx.New(runner), nil).Execute(context.Background(), tmuxx.Session{ID: "$4", Name: "fleet"}, &narration)

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	want := "agentctl 0.2.0\n" +
		"Attaching session \"fleet\" in iTerm2.\n\n" +
		"iTerm2 will now show its Command Menu. That menu is iTerm2's, not agentctl's:\n\n" +
		"  esc   detach cleanly — the tabs close and the fleet keeps running\n" +
		"  X     (uppercase) force-quit — the fleet keeps running, but the tmux client\n" +
		"        does not exit, so this terminal stays busy and agentctl cannot report.\n" +
		"        Prefer esc.\n\n" +
		"Detaching never stops the fleet. To stop it: agentctl kill --session fleet\n"
	if narration.String() != want {
		t.Fatalf("narration = %q, want %q", narration.String(), want)
	}
	assertCalls(t, runner,
		tmuxx.Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
		tmuxx.Call{Executable: "tmux", Args: []string{
			"list-windows", "-t", "$4", "-F",
			"#{window_id}\t#{window_name}\t#{@agentctl_managed}\t#{@agentctl_version}\t#{@agentctl_role}\t#{@agentctl_harness}\t#{@agentctl_model}\t#{@agentctl_effort}\t#{@agentctl_process}",
		}},
		tmuxx.Call{Executable: "tmux", Args: []string{"-CC", "attach-session", "-t", "$4"}},
	)
}

func TestExecuteNarratesASingleObservedWindowGrammatically(t *testing.T) {
	restoreBuildStamp(t, "0.2.0")

	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("@7\tplanner\t1\t1\tplanner\tclaude\t\t\tclaude\n")},
		tmuxx.Response{},
	)
	var narration bytes.Buffer

	err := New(tmuxx.New(runner), nil).Execute(context.Background(), tmuxx.Session{ID: "$4", Name: "fleet"}, &narration)

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	lines := strings.Split(narration.String(), "\n")
	if got := lines[1]; got != "Attaching session \"fleet\" (1 window) in iTerm2." {
		t.Fatalf("attach narration line = %q, want singular window", got)
	}
}

func restoreBuildStamp(t *testing.T, stamp string) {
	t.Helper()
	previous := buildinfo.Stamp
	buildinfo.Stamp = stamp
	t.Cleanup(func() {
		buildinfo.Stamp = previous
	})
}

func TestExecuteWritesNoNoticeWhenTheOwnershipGateRefuses(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(tmuxx.Response{Stdout: []byte("0\n")})
	var notice bytes.Buffer

	err := New(tmuxx.New(runner), nil).Execute(context.Background(), tmuxx.Session{ID: "$4", Name: "fleet"}, &notice)

	var refusal *RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("Execute() error = %T %v, want *RefusalError", err, err)
	}
	if notice.Len() != 0 {
		t.Fatalf("notice = %q, want empty; a refused attach must not announce one", notice.String())
	}
}

func TestStillRunningReportsPresenceOfTheAttachedSessionID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		sessions    string
		wantPresent bool
	}{
		{name: "present", sessions: "$4\tfleet\n$7\tother\n", wantPresent: true},
		{name: "absent", sessions: "$7\tother\n"},
		{name: "same name different id", sessions: "$9\tfleet\n"},
		{name: "no sessions", sessions: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := tmuxx.NewFakeRunner(tmuxx.Response{Stdout: []byte(tt.sessions)})

			present, err := New(tmuxx.New(runner), nil).StillRunning(context.Background(), tmuxx.Session{ID: "$4", Name: "fleet"})

			if err != nil {
				t.Fatalf("StillRunning() error = %v", err)
			}
			if present != tt.wantPresent {
				t.Fatalf("StillRunning() = %v, want %v", present, tt.wantPresent)
			}
			assertCalls(t, runner, tmuxx.Call{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}})
		})
	}
}

func TestStillRunningReportsItsOwnFailureInsteadOfAnAbsence(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("tmux failed")
	runner := tmuxx.NewFakeRunner(tmuxx.Response{Err: wantErr})

	present, err := New(tmuxx.New(runner), nil).StillRunning(context.Background(), tmuxx.Session{ID: "$4", Name: "fleet"})

	if present {
		t.Fatalf("StillRunning() = %v, want false", present)
	}
	var tmuxFailure *tmuxx.TmuxError
	if !errors.As(err, &tmuxFailure) {
		t.Fatalf("StillRunning() error = %T %v, want *tmuxx.TmuxError", err, err)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("StillRunning() error = %v, want wrapped runner error", err)
	}
}

func TestStillRunningPreservesContextErrors(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(tmuxx.Response{Err: context.Canceled})

	if _, err := New(tmuxx.New(runner), nil).StillRunning(context.Background(), tmuxx.Session{ID: "$4", Name: "fleet"}); err != context.Canceled {
		t.Fatalf("StillRunning() error = %T %v, want context.Canceled", err, err)
	}
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

			err := New(tmuxx.New(runner), nil).Execute(context.Background(), tmuxx.Session{ID: "$4", Name: "fleet"}, io.Discard)

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
			responses: []tmuxx.Response{{Stdout: []byte("1\n")}, {Stdout: []byte("1\n")}, {}, {Err: wantErr}},
			wantCalls: []tmuxx.Call{
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
				{Executable: "tmux", Args: []string{
					"list-windows", "-t", "$4", "-F",
					"#{window_id}\t#{window_name}\t#{@agentctl_managed}\t#{@agentctl_version}\t#{@agentctl_role}\t#{@agentctl_harness}\t#{@agentctl_model}\t#{@agentctl_effort}\t#{@agentctl_process}",
				}},
				{Executable: "tmux", Args: []string{"-CC", "attach-session", "-t", "$4"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := tmuxx.NewFakeRunner(tt.responses...)

			err := New(tmuxx.New(runner), nil).Execute(context.Background(), tmuxx.Session{ID: "$4", Name: "fleet"}, io.Discard)

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
	err := New(tmuxx.New(runner), nil).Execute(context.Background(), tmuxx.Session{ID: "$4", Name: "fleet"}, io.Discard)
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
