package status

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

const statusWindowFormat = "#{window_id}\t#{window_name}\t#{@agentctl_role}\t#{@agentctl_harness}\t#{@agentctl_model}\t#{@agentctl_effort}\t#{@agentctl_unproven}\t#{@agentctl_process}"

func TestCollectorReportsHealthyFleetInRosterOrder(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("planner,codex1\n")},
		tmuxx.Response{Stdout: []byte("@8\tcodex1\tcodex1\tcodex\t\t\t\tweird name\n@9\textra\textra\tclaude\t\t\t\t\n@7\tplanner\tplanner\tclaude\tfable\tmax\t1\tclaude\n")},
		tmuxx.Response{Stdout: []byte("%12\t111\t0\t1\n")},
		tmuxx.Response{Stdout: []byte("claude\n")},
		tmuxx.Response{Stdout: []byte("%13\t222\t0\t1\n")},
		tmuxx.Response{Stdout: []byte("weird name\n")},
	)

	got, err := NewCollector(tmuxx.New(runner)).Collect(context.Background(), "epic123", "$4")
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	want := Report{
		Schema:  1,
		Session: "epic123",
		Managed: true,
		Agents: []Agent{
			{Role: "planner", Harness: "claude", Model: "fable", Effort: "max", Window: "planner", PaneID: "%12", Process: "claude", State: StateRunning},
			{Role: "codex1", Harness: "codex", Model: "", Window: "codex1", PaneID: "%13", Process: "weird name", State: StateRunning},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Collect() = %#v, want %#v", got, want)
	}

	wantCalls := []tmuxx.Call{
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_roles"}},
		{Executable: "tmux", Args: []string{"list-windows", "-t", "$4", "-F", statusWindowFormat}},
		{Executable: "tmux", Args: []string{"list-panes", "-t", "@7", "-F", "#{pane_id}\t#{pane_pid}\t#{pane_dead}\t#{window_panes}"}},
		{Executable: "ps", Args: []string{"-o", "comm=", "-p", "111"}},
		{Executable: "tmux", Args: []string{"list-panes", "-t", "@8", "-F", "#{pane_id}\t#{pane_pid}\t#{pane_dead}\t#{window_panes}"}},
		{Executable: "ps", Args: []string{"-o", "comm=", "-p", "222"}},
	}
	if !reflect.DeepEqual(runner.Calls, wantCalls) {
		t.Fatalf("recorded calls = %#v, want %#v", runner.Calls, wantCalls)
	}
}

func TestCollectorMergedLayoutAggregateNoteThreshold(t *testing.T) {
	t.Parallel()

	const note = `all 4 roster roles are missing; unmanaged window "joined" has 4 panes`
	for _, test := range []struct {
		name      string
		paneCount int
		wantNote  string
	}{
		{name: "below", paneCount: 3},
		{name: "equal", paneCount: 4, wantNote: note},
		{name: "above", paneCount: 5},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var paneRows strings.Builder
			for index := 0; index < test.paneCount; index++ {
				fmt.Fprintf(&paneRows, "%%%d\t%d\t0\t%d\n", index+20, index+200, test.paneCount)
			}
			runner := tmuxx.NewFakeRunner(
				tmuxx.Response{Stdout: []byte("1\n")},
				tmuxx.Response{Stdout: []byte("1\n")},
				tmuxx.Response{Stdout: []byte("planner,build1,build2,review\n")},
				tmuxx.Response{Stdout: []byte("@12\tjoined\t\t\t\t\t\t\n")},
				tmuxx.Response{Stdout: []byte(paneRows.String())},
			)

			got, err := NewCollector(tmuxx.New(runner)).Collect(context.Background(), "fleet", "$4")
			if err != nil {
				t.Fatalf("Collect() error = %v", err)
			}
			if got.Note != test.wantNote {
				t.Fatalf("Collect() note = %q, want %q", got.Note, test.wantNote)
			}
			wantLastCall := tmuxx.Call{
				Executable: "tmux",
				Args:       []string{"list-panes", "-t", "@12", "-F", "#{pane_id}\t#{pane_pid}\t#{pane_dead}\t#{window_panes}"},
			}
			if lastCall := runner.Calls[len(runner.Calls)-1]; !reflect.DeepEqual(lastCall, wantLastCall) {
				t.Fatalf("last call = %#v, want exact aggregate pane observation %#v", lastCall, wantLastCall)
			}
			for _, causal := range []string{"joined panes", "merged panes", "caused"} {
				if strings.Contains(strings.ToLower(got.Note), causal) {
					t.Fatalf("Collect() note = %q, want observation without causal language %q", got.Note, causal)
				}
			}
		})
	}
}

func TestCollectorMergedLayoutAggregateNoteRejectsNearMisses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		windows   string
		paneRows  string
		wantCalls int
	}{
		{
			name:      "one roster role still has a window",
			windows:   "@7\tplanner\tplanner\tclaude\t\t\t\tbaseline\n@12\tjoined\t\t\t\t\t\t\n",
			paneRows:  "%7\t111\t0\t1\n",
			wantCalls: 6,
		},
		{
			name:      "two role-less windows",
			windows:   "@12\tjoined-a\t\t\t\t\t\t\n@13\tjoined-b\t\t\t\t\t\t\n",
			wantCalls: 4,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			responses := []tmuxx.Response{
				{Stdout: []byte("1\n")},
				{Stdout: []byte("1\n")},
				{Stdout: []byte("planner,build1,build2,review\n")},
				{Stdout: []byte(test.windows)},
			}
			if test.paneRows != "" {
				responses = append(responses,
					tmuxx.Response{Stdout: []byte(test.paneRows)},
					tmuxx.Response{Stdout: []byte("claude\n")},
				)
			}
			runner := tmuxx.NewFakeRunner(responses...)
			got, err := NewCollector(tmuxx.New(runner)).Collect(context.Background(), "fleet", "$4")
			if err != nil {
				t.Fatalf("Collect() error = %v", err)
			}
			if got.Note != "" {
				t.Fatalf("Collect() note = %q, want empty", got.Note)
			}
			if len(runner.Calls) != test.wantCalls {
				t.Fatalf("recorded calls = %d, want %d: %#v", len(runner.Calls), test.wantCalls, runner.Calls)
			}
		})
	}
}

func TestCollectorRendersUnmanagedSessionAfterCheckingAbsentVersion(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("0\n")},
		tmuxx.Response{},
	)
	got, err := NewCollector(tmuxx.New(runner)).Collect(context.Background(), "other", "$9")
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	want := Report{Schema: 1, Session: "other", Managed: false, Agents: []Agent{}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Collect() = %#v, want %#v", got, want)
	}
	wantCalls := []tmuxx.Call{
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$9", "@agentctl_managed"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$9", "@agentctl_version"}},
	}
	if !reflect.DeepEqual(runner.Calls, wantCalls) {
		t.Fatalf("recorded calls = %#v, want %#v", runner.Calls, wantCalls)
	}
}

func TestCollectorRejectsDifferentVersionForUnmanagedSession(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("0\n")},
		tmuxx.Response{Stdout: []byte("2\n")},
	)
	_, err := NewCollector(tmuxx.New(runner)).Collect(context.Background(), "future", "$9")
	var versionError *VersionError
	if !errors.As(err, &versionError) {
		t.Fatalf("Collect() error = %T %v, want *VersionError", err, err)
	}
	if versionError.Session != "future" || versionError.Version != "2" {
		t.Fatalf("VersionError = %#v, want session future and version 2", versionError)
	}
	wantCalls := []tmuxx.Call{
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$9", "@agentctl_managed"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$9", "@agentctl_version"}},
	}
	if !reflect.DeepEqual(runner.Calls, wantCalls) {
		t.Fatalf("recorded calls = %#v, want %#v", runner.Calls, wantCalls)
	}
}

func TestCollectorRejectsDifferentSessionVersionBeforeReadingRoster(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("2\n")},
	)
	_, err := NewCollector(tmuxx.New(runner)).Collect(context.Background(), "future", "$10")
	var versionError *VersionError
	if !errors.As(err, &versionError) {
		t.Fatalf("Collect() error = %T %v, want *VersionError", err, err)
	}
	if versionError.Session != "future" || versionError.Version != "2" {
		t.Fatalf("VersionError = %#v, want session future and version 2", versionError)
	}
	wantCalls := []tmuxx.Call{
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$10", "@agentctl_managed"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$10", "@agentctl_version"}},
	}
	if !reflect.DeepEqual(runner.Calls, wantCalls) {
		t.Fatalf("recorded calls = %#v, want %#v", runner.Calls, wantCalls)
	}
}

func TestCollectorRejectsEmptyRosterBeforeListingWindows(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{},
	)
	_, err := NewCollector(tmuxx.New(runner)).Collect(context.Background(), "epic123", "$4")
	if err == nil {
		t.Fatal("Collect() error = nil, want empty-roster error")
	}
	if len(runner.Calls) != 3 {
		t.Fatalf("recorded %d calls, want three option reads and no window listing: %#v", len(runner.Calls), runner.Calls)
	}
}

func TestCollectorRejectsEmptyRosterEntriesBeforeListingWindows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		roster string
	}{
		{name: "leading comma", roster: ",planner"},
		{name: "trailing comma", roster: "planner,"},
		{name: "double comma", roster: "planner,,codex1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runner := tmuxx.NewFakeRunner(
				tmuxx.Response{Stdout: []byte("1\n")},
				tmuxx.Response{Stdout: []byte("1\n")},
				tmuxx.Response{Stdout: []byte(tt.roster + "\n")},
			)
			_, err := NewCollector(tmuxx.New(runner)).Collect(context.Background(), "epic123", "$4")
			var rosterError *RosterError
			if !errors.As(err, &rosterError) {
				t.Fatalf("Collect() error = %T %v, want *RosterError", err, err)
			}
			wantText := fmt.Sprintf("managed session has invalid @agentctl_roles roster %q", tt.roster)
			if err.Error() != wantText {
				t.Fatalf("Collect() error = %q, want %q", err, wantText)
			}
			if len(runner.Calls) != 3 {
				t.Fatalf("recorded %d calls, want three option reads and no window listing: %#v", len(runner.Calls), runner.Calls)
			}
		})
	}
}

func TestCollectorAppliesStatePrecedenceAndSkipsUnneededProbes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		windows          string
		afterWindows     []tmuxx.Response
		wantAgents       []Agent
		wantCallCount    int
		wantProcessCalls int
	}{
		{
			name:          "missing roster window",
			wantAgents:    []Agent{{Role: "planner", State: StateMissing}},
			wantCallCount: 4,
		},
		{
			name: "ambiguous precedes unmanaged and process state",
			windows: "@7\tplanner\twrong\tclaude\tfable\t\t\t\n" +
				"@8\tplanner\tplanner\tcodex\t\t\t\tbaseline\n",
			wantAgents: []Agent{
				{Role: "planner", Harness: "claude", Model: "fable", Window: "planner", State: StateAmbiguous},
				{Role: "planner", Harness: "codex", Window: "planner", State: StateAmbiguous},
			},
			wantCallCount: 4,
		},
		{
			name:          "unmanaged metadata precedes missing pane",
			windows:       "@7\tplanner\twrong\tclaude\tfable\tmax\t\tbaseline\n",
			wantAgents:    []Agent{{Role: "planner", Harness: "claude", Model: "fable", Effort: "max", Window: "planner", State: StateUnmanaged}},
			wantCallCount: 4,
		},
		{
			name:          "missing stored role precedes pane probe",
			windows:       "@7\tplanner\t\tclaude\tfable\tmax\t\tbaseline\n",
			wantAgents:    []Agent{{Role: "planner", Harness: "claude", Model: "fable", Effort: "max", Window: "planner", State: StateUnmanaged}},
			wantCallCount: 4,
		},
		{
			name:          "zero panes is missing",
			windows:       "@7\tplanner\tplanner\tclaude\tfable\tmax\t\tbaseline\n",
			afterWindows:  []tmuxx.Response{{}},
			wantAgents:    []Agent{{Role: "planner", Harness: "claude", Model: "fable", Effort: "max", Window: "planner", State: StateMissing}},
			wantCallCount: 5,
		},
		{
			name:          "reported multiple pane count is unmanaged",
			windows:       "@7\tplanner\tplanner\tclaude\tfable\tmax\t\tbaseline\n",
			afterWindows:  []tmuxx.Response{{Stdout: []byte("%12\t111\t0\t2\n")}},
			wantAgents:    []Agent{{Role: "planner", Harness: "claude", Model: "fable", Effort: "max", Window: "planner", State: StateUnmanaged}},
			wantCallCount: 5,
		},
		{
			name:          "multiple panes is unmanaged",
			windows:       "@7\tplanner\tplanner\tclaude\tfable\tmax\t\tbaseline\n",
			afterWindows:  []tmuxx.Response{{Stdout: []byte("%12\t111\t0\t2\n%13\t222\t1\t2\n")}},
			wantAgents:    []Agent{{Role: "planner", Harness: "claude", Model: "fable", Effort: "max", Window: "planner", State: StateUnmanaged}},
			wantCallCount: 5,
		},
		{
			name:          "dead precedes empty baseline",
			windows:       "@7\tplanner\tplanner\tclaude\tfable\tmax\t\t\n",
			afterWindows:  []tmuxx.Response{{Stdout: []byte("%12\t111\t1\t1\n")}},
			wantAgents:    []Agent{{Role: "planner", Harness: "claude", Model: "fable", Effort: "max", Window: "planner", PaneID: "%12", State: StateDead}},
			wantCallCount: 5,
		},
		{
			name:          "empty baseline is no-baseline without process probe",
			windows:       "@7\tplanner\tplanner\tclaude\tfable\tmax\t\t\n",
			afterWindows:  []tmuxx.Response{{Stdout: []byte("%12\t111\t0\t1\n")}},
			wantAgents:    []Agent{{Role: "planner", Harness: "claude", Model: "fable", Effort: "max", Window: "planner", PaneID: "%12", State: StateNoBaseline}},
			wantCallCount: 5,
		},
		{
			name:    "unavailable identity is unexpected",
			windows: "@7\tplanner\tplanner\tclaude\tfable\tmax\t\tbaseline\n",
			afterWindows: []tmuxx.Response{
				{Stdout: []byte("%12\t111\t0\t1\n")},
				{Err: processExitError(1)},
			},
			wantAgents:       []Agent{{Role: "planner", Harness: "claude", Model: "fable", Effort: "max", Window: "planner", PaneID: "%12", State: StateUnexpectedProcess}},
			wantCallCount:    6,
			wantProcessCalls: 1,
		},
		{
			name:    "mismatch renders current process",
			windows: "@7\tplanner\tplanner\tclaude\tfable\tmax\t\tclaude\n",
			afterWindows: []tmuxx.Response{
				{Stdout: []byte("%12\t111\t0\t1\n")},
				{Stdout: []byte("/bin/zsh\n")},
			},
			wantAgents:       []Agent{{Role: "planner", Harness: "claude", Model: "fable", Effort: "max", Window: "planner", PaneID: "%12", Process: "/bin/zsh", State: StateUnexpectedProcess}},
			wantCallCount:    6,
			wantProcessCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			responses := []tmuxx.Response{
				{Stdout: []byte("1\n")},
				{Stdout: []byte("1\n")},
				{Stdout: []byte("planner\n")},
				{Stdout: []byte(tt.windows)},
			}
			responses = append(responses, tt.afterWindows...)
			runner := tmuxx.NewFakeRunner(responses...)

			got, err := NewCollector(tmuxx.New(runner)).Collect(context.Background(), "epic123", "$4")
			if err != nil {
				t.Fatalf("Collect() error = %v", err)
			}
			want := Report{Schema: 1, Session: "epic123", Managed: true, Agents: tt.wantAgents}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Collect() = %#v, want %#v", got, want)
			}
			if len(runner.Calls) != tt.wantCallCount {
				t.Fatalf("recorded %d calls, want %d: %#v", len(runner.Calls), tt.wantCallCount, runner.Calls)
			}
			processCalls := assertStatusCallAllowlist(t, runner.Calls)
			if processCalls != tt.wantProcessCalls {
				t.Fatalf("recorded %d process calls, want %d", processCalls, tt.wantProcessCalls)
			}
		})
	}
}

func TestCollectorRechecksWindowSnapshotBeforeClassifyingPaneErrorAsMissing(t *testing.T) {
	t.Parallel()

	windowRecord := "@7\tplanner\tplanner\tclaude\tfable\tmax\t\tbaseline\n"
	windowGone := errors.New("window disappeared")
	tests := []struct {
		name          string
		secondWindows string
		wantMissing   bool
	}{
		{name: "window is gone", wantMissing: true},
		{name: "window still exists", secondWindows: windowRecord},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := tmuxx.NewFakeRunner(
				tmuxx.Response{Stdout: []byte("1\n")},
				tmuxx.Response{Stdout: []byte("1\n")},
				tmuxx.Response{Stdout: []byte("planner\n")},
				tmuxx.Response{Stdout: []byte(windowRecord)},
				tmuxx.Response{Err: windowGone},
				tmuxx.Response{Stdout: []byte(tt.secondWindows)},
			)

			got, err := NewCollector(tmuxx.New(runner)).Collect(context.Background(), "epic123", "$4")
			if tt.wantMissing {
				if err != nil {
					t.Fatalf("Collect() error = %v", err)
				}
				want := Report{
					Schema:  1,
					Session: "epic123",
					Managed: true,
					Agents:  []Agent{{Role: "planner", Harness: "claude", Model: "fable", Effort: "max", Window: "planner", State: StateMissing}},
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("Collect() = %#v, want %#v", got, want)
				}
			} else if !errors.Is(err, windowGone) {
				t.Fatalf("Collect() error = %v, want original pane error %v", err, windowGone)
			}
			if len(runner.Calls) != 6 {
				t.Fatalf("recorded %d calls, want 6: %#v", len(runner.Calls), runner.Calls)
			}
			if call := runner.Calls[5]; call.Executable != "tmux" || len(call.Args) == 0 || call.Args[0] != "list-windows" {
				t.Fatalf("race confirmation call = %#v, want list-windows", call)
			}
			assertStatusCallAllowlist(t, runner.Calls)
		})
	}
}

func TestCollectorPropagatesProcessRunnerFailure(t *testing.T) {
	t.Parallel()

	processFailure := errors.New("cannot start ps")
	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("planner\n")},
		tmuxx.Response{Stdout: []byte("@7\tplanner\tplanner\tclaude\tfable\tmax\t\tbaseline\n")},
		tmuxx.Response{Stdout: []byte("%12\t111\t0\t1\n")},
		tmuxx.Response{Err: processFailure},
	)
	_, err := NewCollector(tmuxx.New(runner)).Collect(context.Background(), "epic123", "$4")
	if !errors.Is(err, processFailure) {
		t.Fatalf("Collect() error = %v, want process failure %v", err, processFailure)
	}
	var tmuxError *tmuxx.TmuxError
	if !errors.As(err, &tmuxError) {
		t.Fatalf("Collect() error = %T %v, want *tmuxx.TmuxError", err, err)
	}
	if processCalls := assertStatusCallAllowlist(t, runner.Calls); processCalls != 1 {
		t.Fatalf("recorded %d process calls, want 1", processCalls)
	}
}

func TestCollectorCollectAllListsEverySessionInServerOrder(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("$4\tfleet\n$5\tshell\n$6\tepic123\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("planner\n")},
		tmuxx.Response{Stdout: []byte("@7\tplanner\tplanner\tclaude\t\t\t\tclaude\n")},
		tmuxx.Response{Stdout: []byte("%12\t111\t0\t1\n")},
		tmuxx.Response{Stdout: []byte("claude\n")},
		tmuxx.Response{Stdout: []byte("0\n")},
		tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("codex1\n")},
		tmuxx.Response{Stdout: []byte("@9\tcodex1\tcodex1\tcodex\tgpt\t\t\tcodex\n")},
		tmuxx.Response{Stdout: []byte("%20\t222\t0\t1\n")},
		tmuxx.Response{Stdout: []byte("codex\n")},
	)

	got, err := NewCollector(tmuxx.New(runner)).CollectAll(context.Background())
	if err != nil {
		t.Fatalf("CollectAll() error = %v", err)
	}
	want := SessionsReport{
		Schema: 1,
		Sessions: []Report{
			{Schema: 1, Session: "fleet", Managed: true, Agents: []Agent{
				{Role: "planner", Harness: "claude", Model: "", Window: "planner", PaneID: "%12", Process: "claude", State: StateRunning},
			}},
			{Schema: 1, Session: "shell", Managed: false, Agents: []Agent{}},
			{Schema: 1, Session: "epic123", Managed: true, Agents: []Agent{
				{Role: "codex1", Harness: "codex", Model: "gpt", Window: "codex1", PaneID: "%20", Process: "codex", State: StateRunning},
			}},
		},
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("CollectAll() = %#v, want %#v", got, want)
	}

	wantCalls := []tmuxx.Call{
		{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_roles"}},
		{Executable: "tmux", Args: []string{"list-windows", "-t", "$4", "-F", statusWindowFormat}},
		{Executable: "tmux", Args: []string{"list-panes", "-t", "@7", "-F", "#{pane_id}\t#{pane_pid}\t#{pane_dead}\t#{window_panes}"}},
		{Executable: "ps", Args: []string{"-o", "comm=", "-p", "111"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$5", "@agentctl_managed"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$5", "@agentctl_version"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$6", "@agentctl_managed"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$6", "@agentctl_version"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$6", "@agentctl_roles"}},
		{Executable: "tmux", Args: []string{"list-windows", "-t", "$6", "-F", statusWindowFormat}},
		{Executable: "tmux", Args: []string{"list-panes", "-t", "@9", "-F", "#{pane_id}\t#{pane_pid}\t#{pane_dead}\t#{window_panes}"}},
		{Executable: "ps", Args: []string{"-o", "comm=", "-p", "222"}},
	}
	if !reflect.DeepEqual(runner.Calls, wantCalls) {
		t.Fatalf("recorded calls = %#v, want %#v", runner.Calls, wantCalls)
	}
}

func TestCollectorCollectAllMarksOnlyTheSessionContainingTheCallerPane(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("$4\tfleet\n$5\tshell\n")},
		tmuxx.Response{Stdout: []byte("fleet\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("planner\n")},
		tmuxx.Response{Stdout: []byte("@7\tplanner\tplanner\tclaude\t\t\t\tclaude\n")},
		tmuxx.Response{Stdout: []byte("%12\t111\t0\t1\n")},
		tmuxx.Response{Stdout: []byte("claude\n")},
		tmuxx.Response{Stdout: []byte("0\n")},
		tmuxx.Response{},
	)

	collector := NewCollector(tmuxx.New(runner)).WithLookupEnv(statusLookup(map[string]string{
		"AGENTCTL_SESSION": "shell",
		"TMUX_PANE":        "%9",
	}))
	got, err := collector.CollectAll(context.Background())
	if err != nil {
		t.Fatalf("CollectAll() error = %v", err)
	}
	if !got.Sessions[0].Current || got.Sessions[1].Current {
		t.Fatalf("CollectAll() sessions = %#v, want only fleet current", got.Sessions)
	}
	wantCall := tmuxx.Call{Executable: "tmux", Args: []string{"display-message", "-p", "-t", "%9", "#{session_name}"}}
	if !reflect.DeepEqual(runner.Calls[1], wantCall) {
		t.Fatalf("current-session call = %#v, want %#v", runner.Calls[1], wantCall)
	}
}

func TestCollectorCollectAllSilentlyOmitsMarkerWhenCurrentSessionReadFails(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("$5\tshell\n")},
		tmuxx.Response{Err: errors.New("lost caller pane")},
		tmuxx.Response{Stdout: []byte("0\n")},
		tmuxx.Response{},
	)

	collector := NewCollector(tmuxx.New(runner)).WithLookupEnv(statusLookup(map[string]string{"TMUX_PANE": "%9"}))
	got, err := collector.CollectAll(context.Background())
	if err != nil {
		t.Fatalf("CollectAll() error = %v, want nil", err)
	}
	if got.Sessions[0].Current {
		t.Fatalf("CollectAll() sessions = %#v, want no current marker", got.Sessions)
	}
}

func TestCollectorCollectAllReportsUnmanagedSessions(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("$5\tshell\n")},
		tmuxx.Response{Stdout: []byte("0\n")},
		tmuxx.Response{},
	)

	got, err := NewCollector(tmuxx.New(runner)).CollectAll(context.Background())
	if err != nil {
		t.Fatalf("CollectAll() error = %v", err)
	}
	want := SessionsReport{Schema: 1, Sessions: []Report{{Schema: 1, Session: "shell", Managed: false, Agents: []Agent{}}}}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("CollectAll() = %#v, want %#v", got, want)
	}
}

func TestCollectorCollectAllContinuesAfterUnreadableSessionMetadata(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("$4\tfleet\n$6\tfuture\n$7\tshell\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("planner\n")},
		tmuxx.Response{Stdout: []byte("@7\tplanner\tplanner\tclaude\t\t\t\tclaude\n")},
		tmuxx.Response{Stdout: []byte("%12\t111\t0\t1\n")},
		tmuxx.Response{Stdout: []byte("claude\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("2\n")},
		tmuxx.Response{Stdout: []byte("0\n")},
		tmuxx.Response{},
	)

	got, err := NewCollector(tmuxx.New(runner)).CollectAll(context.Background())
	var versionError *VersionError
	if !errors.As(err, &versionError) {
		t.Fatalf("CollectAll() error = %T %v, want *VersionError", err, err)
	}
	if versionError.Session != "future" || versionError.Version != "2" {
		t.Fatalf("VersionError = %#v, want session future and version 2", versionError)
	}
	want := SessionsReport{Schema: 1, Sessions: []Report{
		{Schema: 1, Session: "fleet", Managed: true, Agents: []Agent{{Role: "planner", Harness: "claude", Window: "planner", PaneID: "%12", Process: "claude", State: StateRunning}}},
		{Schema: 1, Session: "future", Managed: true, Agents: []Agent{}, Defect: "session \"future\" was created by a different agentctl version \"2\""},
		{Schema: 1, Session: "shell", Managed: false, Agents: []Agent{}},
	}}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("CollectAll() = %#v, want %#v", got, want)
	}
}

func TestCollectorCollectAllNamesRosterDefectAndContinues(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("$4\tbroken\n$5\tshell\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("0\n")},
		tmuxx.Response{},
	)

	got, err := NewCollector(tmuxx.New(runner)).CollectAll(context.Background())
	var rosterError *RosterError
	if !errors.As(err, &rosterError) {
		t.Fatalf("CollectAll() error = %T %v, want *RosterError", err, err)
	}
	if !strings.Contains(err.Error(), `session "broken"`) {
		t.Fatalf("CollectAll() error = %q, want broken session name", err)
	}
	want := SessionsReport{Schema: 1, Sessions: []Report{
		{Schema: 1, Session: "broken", Managed: true, Agents: []Agent{}, Defect: `session "broken": managed session has no @agentctl_roles roster`},
		{Schema: 1, Session: "shell", Managed: false, Agents: []Agent{}},
	}}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("CollectAll() = %#v, want %#v", got, want)
	}
}

func TestCollectorCollectAllReturnsRowOneFailureWithoutReport(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(tmuxx.Response{Err: errors.New("no server running")})
	got, err := NewCollector(tmuxx.New(runner)).CollectAll(context.Background())
	var tmuxError *tmuxx.TmuxError
	if !errors.As(err, &tmuxError) {
		t.Fatalf("CollectAll() error = %T %v, want *tmuxx.TmuxError", err, err)
	}
	if got != nil {
		t.Fatalf("CollectAll() = %#v, want nil report", got)
	}
}

func TestCollectorCollectAllPreservesContextErrors(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(tmuxx.Response{Err: context.Canceled})
	if _, err := NewCollector(tmuxx.New(runner)).CollectAll(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("CollectAll() error = %T %v, want preserved %v", err, err, context.Canceled)
	}
}

type processExitError int

func (e processExitError) Error() string { return "process exited" }
func (e processExitError) ExitCode() int { return int(e) }

func statusLookup(values map[string]string) LookupEnv {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func assertStatusCallAllowlist(t *testing.T, calls []tmuxx.Call) int {
	t.Helper()
	processCalls := 0
	for _, call := range calls {
		if call.Executable == "ps" {
			processCalls++
			if !reflect.DeepEqual(call.Args, []string{"-o", "comm=", "-p", "111"}) {
				t.Fatalf("unexpected ps argv: %#v", call.Args)
			}
			continue
		}
		if call.Executable != "tmux" || len(call.Args) == 0 {
			t.Fatalf("unexpected status call: %#v", call)
		}
		switch call.Args[0] {
		case "show-options":
			if len(call.Args) < 2 || call.Args[1] != "-qv" {
				t.Fatalf("status used forbidden window option read: %#v", call.Args)
			}
		case "list-windows", "list-panes":
		default:
			t.Fatalf("status used forbidden tmux verb: %#v", call.Args)
		}
	}
	return processCalls
}
