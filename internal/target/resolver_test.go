package target

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

const targetWindowFormat = "#{window_id}\t#{window_name}\t#{@agentctl_managed}\t#{@agentctl_version}\t#{@agentctl_role}\t#{@agentctl_harness}\t#{@agentctl_model}\t#{@agentctl_process}"
const targetPaneFormat = "#{pane_id}\t#{pane_pid}\t#{pane_dead}\t#{window_panes}"

func TestResolverRejectsMalformedRoleBeforeClientCall(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner()
	resolver := New(tmuxx.New(runner), func(string) (string, bool) { return "", false })

	_, err := resolver.Resolve(context.Background(), tmuxx.Session{ID: "$4", Name: "epic123"}, "bad/role")
	var validation *config.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("Resolve() error = %T %v, want *config.ValidationError", err, err)
	}
	if got := len(runner.Calls); got != 0 {
		t.Fatalf("Resolve() recorded %d calls, want zero: %#v", got, runner.Calls)
	}
}

func TestResolverRejectsInvalidSessionMetadataAtFirstFailedGate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		responses  []tmuxx.Response
		wantOption string
		wantValue  string
		wantCalls  []tmuxx.Call
	}{
		{
			name:       "managed unset",
			responses:  []tmuxx.Response{{}},
			wantOption: "@agentctl_managed",
			wantCalls: []tmuxx.Call{{Executable: "tmux", Args: []string{
				"show-options", "-qv", "-t", "$4", "@agentctl_managed",
			}}},
		},
		{
			name:       "managed wrong",
			responses:  []tmuxx.Response{{Stdout: []byte("0\n")}},
			wantOption: "@agentctl_managed",
			wantValue:  "0",
			wantCalls: []tmuxx.Call{{Executable: "tmux", Args: []string{
				"show-options", "-qv", "-t", "$4", "@agentctl_managed",
			}}},
		},
		{
			name:       "version unset",
			responses:  []tmuxx.Response{{Stdout: []byte("1\n")}, {}},
			wantOption: "@agentctl_version",
			wantCalls: []tmuxx.Call{
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
			},
		},
		{
			name:       "version wrong",
			responses:  []tmuxx.Response{{Stdout: []byte("1\n")}, {Stdout: []byte("2\n")}},
			wantOption: "@agentctl_version",
			wantValue:  "2",
			wantCalls: []tmuxx.Call{
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			runner := tmuxx.NewFakeRunner(test.responses...)
			resolver := New(tmuxx.New(runner), nil)
			session := tmuxx.Session{ID: "$4", Name: "epic123"}
			_, err := resolver.Resolve(context.Background(), session, "codex2")

			var metadata *SessionMetadataError
			if !errors.As(err, &metadata) {
				t.Fatalf("Resolve() error = %T %v, want *SessionMetadataError", err, err)
			}
			if metadata.Session != session || metadata.Option != test.wantOption || metadata.Value != test.wantValue {
				t.Fatalf("SessionMetadataError = %#v, want session=%#v option=%q value=%q", metadata, session, test.wantOption, test.wantValue)
			}
			if !reflect.DeepEqual(runner.Calls, test.wantCalls) {
				t.Fatalf("recorded calls = %#v, want %#v", runner.Calls, test.wantCalls)
			}
		})
	}
}

func TestResolverClassifiesSessionProbeFailure(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(tmuxx.Response{Err: errors.New("tmux probe failed")})
	resolver := New(tmuxx.New(runner), nil)
	_, err := resolver.Resolve(context.Background(), tmuxx.Session{ID: "$4", Name: "epic123"}, "codex2")

	var tmuxFailure *tmuxx.TmuxError
	if !errors.As(err, &tmuxFailure) {
		t.Fatalf("Resolve() error = %T %v, want *tmuxx.TmuxError", err, err)
	}
}

func TestResolverPreservesCanceledSessionProbe(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(tmuxx.Response{Err: context.Canceled})
	resolver := New(tmuxx.New(runner), nil)
	_, err := resolver.Resolve(context.Background(), tmuxx.Session{ID: "$4", Name: "epic123"}, "codex2")
	if err != context.Canceled {
		t.Fatalf("Resolve() error = %T %v, want exact context.Canceled", err, err)
	}
}

func TestResolverRequiresExactlyOneExactWindowName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		windows string
		wantIDs []tmuxx.WindowID
	}{
		{
			name:    "no exact match despite prefix and suffix decoys",
			windows: "@3\tcodex21\t1\t1\tcodex21\tcodex\t\tcodex\n@5\txcodex2\t1\t1\txcodex2\tcodex\t\tcodex\n",
			wantIDs: []tmuxx.WindowID{},
		},
		{
			name:    "multiple exact matches",
			windows: "@3\tcodex21\t1\t1\tcodex21\tcodex\t\tcodex\n@4\tcodex2\t1\t1\tcodex2\tcodex\t\tcodex\n@7\tcodex2\t1\t1\tcodex2\tcodex\t\tcodex\n",
			wantIDs: []tmuxx.WindowID{"@4", "@7"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			runner := tmuxx.NewFakeRunner(
				tmuxx.Response{Stdout: []byte("1\n")},
				tmuxx.Response{Stdout: []byte("1\n")},
				tmuxx.Response{Stdout: []byte(test.windows)},
			)
			resolver := New(tmuxx.New(runner), nil)
			session := tmuxx.Session{ID: "$4", Name: "epic123"}
			_, err := resolver.Resolve(context.Background(), session, "codex2")

			var resolution *RoleResolutionError
			if !errors.As(err, &resolution) {
				t.Fatalf("Resolve() error = %T %v, want *RoleResolutionError", err, err)
			}
			if resolution.Session != session || resolution.Role != "codex2" || !reflect.DeepEqual(resolution.WindowIDs, test.wantIDs) {
				t.Fatalf("RoleResolutionError = %#v, want session=%#v role=codex2 ids=%#v", resolution, session, test.wantIDs)
			}

			wantCalls := []tmuxx.Call{
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
				{Executable: "tmux", Args: []string{"list-windows", "-t", "$4", "-F", targetWindowFormat}},
			}
			if !reflect.DeepEqual(runner.Calls, wantCalls) {
				t.Fatalf("recorded calls = %#v, want ID-only calls %#v", runner.Calls, wantCalls)
			}
		})
	}
}

func TestResolverRejectsFirstInvalidWindowMetadataField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		windowLine string
		wantWindow tmuxx.Window
	}{
		{
			name:       "window unmanaged",
			windowLine: "@4\tcodex2\t0\t1\tcodex2\tcodex\t\tcodex\n",
			wantWindow: tmuxx.Window{ID: "@4", Name: "codex2", Managed: "0", Version: "1", Role: "codex2", Harness: "codex", Process: "codex"},
		},
		{
			name:       "stored role mismatch",
			windowLine: "@4\tcodex2\t1\t1\tplanner\tcodex\t\tcodex\n",
			wantWindow: tmuxx.Window{ID: "@4", Name: "codex2", Managed: "1", Version: "1", Role: "planner", Harness: "codex", Process: "codex"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			runner := tmuxx.NewFakeRunner(
				tmuxx.Response{Stdout: []byte("1\n")},
				tmuxx.Response{Stdout: []byte("1\n")},
				tmuxx.Response{Stdout: []byte(test.windowLine)},
			)
			resolver := New(tmuxx.New(runner), nil)
			session := tmuxx.Session{ID: "$4", Name: "epic123"}
			_, err := resolver.Resolve(context.Background(), session, "codex2")

			var metadata *WindowMetadataError
			if !errors.As(err, &metadata) {
				t.Fatalf("Resolve() error = %T %v, want *WindowMetadataError", err, err)
			}
			if metadata.Session != session || metadata.Role != "codex2" || !reflect.DeepEqual(metadata.Window, test.wantWindow) {
				t.Fatalf("WindowMetadataError = %#v, want session=%#v role=codex2 window=%#v", metadata, session, test.wantWindow)
			}
			if got := len(runner.Calls); got != 3 {
				t.Fatalf("recorded %d calls, want metadata failure before pane probe: %#v", got, runner.Calls)
			}
		})
	}
}

func TestResolverRejectsUnsafePaneStateBeforeProcessProbe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		paneRows  string
		wantPanes []tmuxx.Pane
	}{
		{name: "zero panes", paneRows: "", wantPanes: nil},
		{
			name:     "multiple panes",
			paneRows: "%8\t101\t0\t2\n%9\t102\t0\t2\n",
			wantPanes: []tmuxx.Pane{
				{ID: "%8", PID: 101, WindowPanes: 2},
				{ID: "%9", PID: 102, WindowPanes: 2},
			},
		},
		{
			name:      "sole record reports multiple window panes",
			paneRows:  "%8\t101\t0\t2\n",
			wantPanes: []tmuxx.Pane{{ID: "%8", PID: 101, WindowPanes: 2}},
		},
		{
			name:      "sole pane dead",
			paneRows:  "%8\t101\t1\t1\n",
			wantPanes: []tmuxx.Pane{{ID: "%8", PID: 101, Dead: true, WindowPanes: 1}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			runner := tmuxx.NewFakeRunner(
				tmuxx.Response{Stdout: []byte("1\n")},
				tmuxx.Response{Stdout: []byte("1\n")},
				tmuxx.Response{Stdout: []byte("@4\tcodex2\t1\t1\tcodex2\tcodex\t\tcodex\n")},
				tmuxx.Response{Stdout: []byte(test.paneRows)},
			)
			resolver := New(tmuxx.New(runner), nil)
			session := tmuxx.Session{ID: "$4", Name: "epic123"}
			_, err := resolver.Resolve(context.Background(), session, "codex2")

			var paneState *PaneStateError
			if !errors.As(err, &paneState) {
				t.Fatalf("Resolve() error = %T %v, want *PaneStateError", err, err)
			}
			if paneState.Session != session || paneState.Role != "codex2" || paneState.Window.ID != "@4" || !reflect.DeepEqual(paneState.Panes, test.wantPanes) {
				t.Fatalf("PaneStateError = %#v, want session=%#v role=codex2 window=@4 panes=%#v", paneState, session, test.wantPanes)
			}

			wantCalls := []tmuxx.Call{
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
				{Executable: "tmux", Args: []string{"list-windows", "-t", "$4", "-F", targetWindowFormat}},
				{Executable: "tmux", Args: []string{"list-panes", "-t", "@4", "-F", targetPaneFormat}},
			}
			if !reflect.DeepEqual(runner.Calls, wantCalls) {
				t.Fatalf("recorded calls = %#v, want no process or delivery after pane failure: %#v", runner.Calls, wantCalls)
			}
		})
	}
}

func TestResolverRejectsEmptyProcessBaselineBeforeProcessProbe(t *testing.T) {
	t.Parallel()

	runner := validTargetRunner("")
	resolver := New(tmuxx.New(runner), func(string) (string, bool) {
		t.Fatal("lookupEnv called before process identity succeeded")
		return "", false
	})
	session := tmuxx.Session{ID: "$4", Name: "epic123"}
	_, err := resolver.Resolve(context.Background(), session, "codex2")

	var identity *ProcessIdentityError
	if !errors.As(err, &identity) {
		t.Fatalf("Resolve() error = %T %v, want *ProcessIdentityError", err, err)
	}
	if identity.Session != session || identity.Role != "codex2" || identity.Window.ID != "@4" || identity.Window.Process != "" || identity.Pane.ID != "%8" || identity.ActualProcess != "" || identity.Err != nil {
		t.Fatalf("ProcessIdentityError = %#v, want empty recorded baseline facts", identity)
	}
	if got := len(runner.Calls); got != 4 {
		t.Fatalf("recorded %d calls, want no ps or delivery for empty baseline: %#v", got, runner.Calls)
	}
}

func TestResolverRejectsUnavailableProcessAsUnsafeTarget(t *testing.T) {
	t.Parallel()

	runner := validTargetRunner("codex", tmuxx.Response{Err: targetExitError(7)})
	resolver := New(tmuxx.New(runner), func(string) (string, bool) {
		t.Fatal("lookupEnv called after unavailable process identity")
		return "", false
	})
	_, err := resolver.Resolve(context.Background(), tmuxx.Session{ID: "$4", Name: "epic123"}, "codex2")

	var identity *ProcessIdentityError
	if !errors.As(err, &identity) {
		t.Fatalf("Resolve() error = %T %v, want *ProcessIdentityError", err, err)
	}
	if !errors.Is(err, tmuxx.ErrProcessUnavailable) || identity.ActualProcess != "" || identity.Err == nil {
		t.Fatalf("ProcessIdentityError = %#v, want wrapped ErrProcessUnavailable and empty actual process", identity)
	}
	if got := len(runner.Calls); got != 5 {
		t.Fatalf("recorded %d calls, want identity probe then refusal: %#v", got, runner.Calls)
	}
}

func TestResolverClassifiesProcessStartupFailure(t *testing.T) {
	t.Parallel()

	runner := validTargetRunner("codex", tmuxx.Response{Err: errors.New("ps could not start")})
	resolver := New(tmuxx.New(runner), nil)
	_, err := resolver.Resolve(context.Background(), tmuxx.Session{ID: "$4", Name: "epic123"}, "codex2")

	var tmuxFailure *tmuxx.TmuxError
	if !errors.As(err, &tmuxFailure) {
		t.Fatalf("Resolve() error = %T %v, want *tmuxx.TmuxError", err, err)
	}
	var identity *ProcessIdentityError
	if errors.As(err, &identity) {
		t.Fatalf("Resolve() error = %T %v, must not adapt startup failure to ProcessIdentityError", err, err)
	}
}

func TestResolverRejectsProcessIdentityMismatchBeforeSelfTargetCheck(t *testing.T) {
	t.Parallel()

	runner := validTargetRunner("codex", tmuxx.Response{Stdout: []byte("zsh\n")})
	resolver := New(tmuxx.New(runner), func(string) (string, bool) {
		t.Fatal("lookupEnv called after process identity mismatch")
		return "", false
	})
	session := tmuxx.Session{ID: "$4", Name: "epic123"}
	_, err := resolver.Resolve(context.Background(), session, "codex2")

	var identity *ProcessIdentityError
	if !errors.As(err, &identity) {
		t.Fatalf("Resolve() error = %T %v, want *ProcessIdentityError", err, err)
	}
	if identity.Session != session || identity.Window.Process != "codex" || identity.ActualProcess != "zsh" || identity.Err != nil {
		t.Fatalf("ProcessIdentityError = %#v, want expected codex and actual zsh", identity)
	}
	if got := len(runner.Calls); got != 5 {
		t.Fatalf("recorded %d calls, want identity probe then refusal: %#v", got, runner.Calls)
	}
}

func TestResolverReturnsPaneWhenCallerIsUnsetOrDifferent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		isSet bool
	}{
		{name: "unset"},
		{name: "different", value: "%99", isSet: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			runner := validTargetRunner("codex", tmuxx.Response{Stdout: []byte("codex\n")})
			resolver := New(tmuxx.New(runner), func(name string) (string, bool) {
				if name != "TMUX_PANE" {
					t.Fatalf("lookupEnv(%q), want TMUX_PANE", name)
				}
				return test.value, test.isSet
			})
			paneID, err := resolver.Resolve(context.Background(), tmuxx.Session{ID: "$4", Name: "epic123"}, "codex2")
			if err != nil {
				t.Fatalf("Resolve() error = %v, want nil", err)
			}
			if paneID != "%8" {
				t.Fatalf("Resolve() pane = %q, want %%8", paneID)
			}
			if got := len(runner.Calls); got != 5 {
				t.Fatalf("recorded %d calls, want validation only and no delivery: %#v", got, runner.Calls)
			}
		})
	}
}

func TestResolverRejectsCallingPaneAfterIdentitySucceeds(t *testing.T) {
	t.Parallel()

	runner := validTargetRunner("codex", tmuxx.Response{Stdout: []byte("codex\n")})
	resolver := New(tmuxx.New(runner), func(name string) (string, bool) {
		if name != "TMUX_PANE" {
			t.Fatalf("lookupEnv(%q), want TMUX_PANE", name)
		}
		return "%8", true
	})
	session := tmuxx.Session{ID: "$4", Name: "epic123"}
	_, err := resolver.Resolve(context.Background(), session, "codex2")

	var selfTarget *SelfTargetError
	if !errors.As(err, &selfTarget) {
		t.Fatalf("Resolve() error = %T %v, want *SelfTargetError", err, err)
	}
	if selfTarget.Session != session || selfTarget.Role != "codex2" || selfTarget.Window.ID != "@4" || selfTarget.Pane.ID != "%8" || selfTarget.CallerPane != "%8" {
		t.Fatalf("SelfTargetError = %#v, want target and caller pane %%8", selfTarget)
	}
	if got := len(runner.Calls); got != 5 {
		t.Fatalf("recorded %d calls, want identity check before self-target refusal and no delivery: %#v", got, runner.Calls)
	}
}

func validTargetRunner(baseline string, processResponse ...tmuxx.Response) *tmuxx.FakeRunner {
	responses := []tmuxx.Response{
		{Stdout: []byte("1\n")},
		{Stdout: []byte("1\n")},
		{Stdout: []byte("@4\tcodex2\t1\t1\tcodex2\tcodex\t\t" + baseline + "\n")},
		{Stdout: []byte("%8\t101\t0\t1\n")},
	}
	responses = append(responses, processResponse...)
	return tmuxx.NewFakeRunner(responses...)
}

type targetExitError int

func (e targetExitError) Error() string { return "process exited" }
func (e targetExitError) ExitCode() int { return int(e) }
