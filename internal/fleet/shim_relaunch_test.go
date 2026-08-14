//go:build darwin

package fleet

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mnbf9rca/agentctl/internal/ptyx"
	"github.com/mnbf9rca/agentctl/internal/shim"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestShimRelaunchRefusesEveryNonAbsentRuntimeStateBeforeMutation(t *testing.T) {
	t.Parallel()

	refusals := []shim.Outcome{
		shim.OutcomeInvalidRecord,
		shim.OutcomeRunning,
		shim.OutcomeStarting,
		shim.OutcomeIndeterminateChildStarting,
		shim.OutcomeOrphan,
		shim.OutcomePresentTokenDisagreement,
		shim.OutcomePresentNotOurs,
		shim.OutcomeCouldNotObserve,
		shim.OutcomeStateRootDisagreement,
		shim.OutcomeAnswererDisagreement,
		shim.OutcomeProtocolSkew,
		shim.OutcomeCleanupFailed,
		shim.OutcomeConcurrentContender,
	}
	for _, outcome := range refusals {
		outcome := outcome
		t.Run(string(outcome), func(t *testing.T) {
			events := &shimEventLog{}
			records := &fakeShimFleetRecords{events: events, record: mustShimFleetRecord(t, "fleet", "/repo")}
			presentation := &fakeShimPresentation{events: events}
			lifecycle := &fakeShimLifecycle{events: events}
			inspector := &fakeShimRoleInspector{events: events, observation: ShimRoleObservation{Outcome: outcome}}
			relauncher := NewShimRelauncher(presentation, lifecycle, records, inspector, shimLaunchTestDependencies(events))

			_, err := relauncher.Relaunch(context.Background(), "fleet", RelaunchRequest{Role: "planner"})
			var refusal *ShimRelaunchRefusalError
			if !errors.As(err, &refusal) {
				t.Fatalf("Relaunch() error = %T %v, want *ShimRelaunchRefusalError", err, err)
			}
			if refusal.Outcome != outcome {
				t.Fatalf("refusal outcome = %q, want %q", refusal.Outcome, outcome)
			}
			want := []string{"record-read:fleet", "inspect:planner"}
			if got := events.snapshot(); !reflect.DeepEqual(got, want) {
				t.Fatalf("refusal events = %#v, want read-only %#v", got, want)
			}
		})
	}
}

// This catches a relaunch implementation that infers presentation from tmux
// availability instead of recreating a durable detached role directly.
func TestShimRelaunchRecreatesDetachedFleetWithoutConsultingTmux(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	record := mustShimFleetRecord(t, "fleet", "/repo")
	record.Presentation = PresentationDetached
	records := &fakeShimFleetRecords{events: events, record: record}
	starter := &fakeDetachedShimStarter{events: events, processes: []*fakeDetachedShimProcess{{pid: 4321, wait: make(chan error)}}}
	lifecycle := &fakeShimLifecycle{events: events, observe: []shim.Response{runningShimResponse(4321, 7001)}}
	dependencies := shimLaunchTestDependencies(events)
	dependencies.DetachedStarter = starter
	relauncher := NewShimRelauncher(nil, lifecycle, records, &fakeShimRoleInspector{events: events, observation: ShimRoleObservation{Outcome: shim.OutcomeMissing}}, dependencies)

	result, err := relauncher.Relaunch(context.Background(), "fleet", RelaunchRequest{Role: "planner"})
	if err != nil {
		t.Fatalf("Relaunch() error = %v", err)
	}
	if result.WindowID != "" || result.PaneID != "" || result.PresentationSessionID != "" {
		t.Fatalf("Relaunch() = %#v, want no tmux presentation IDs", result)
	}
	if got, want := events.snapshot(), []string{"record-read:fleet", "inspect:planner", "self", "look:amq", "look:claude", "detached-start:planner", "observe:planner"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("detached relaunch events = %#v, want direct no-tmux recreation %#v", got, want)
	}
}

// This catches applying a detached relaunch override by rebuilding the fleet
// record with the default tmux presentation instead of preserving its durable
// detached presentation fact.
func TestShimRelaunchDetachedOverridePreservesPresentation(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	record := mustShimFleetRecord(t, "fleet", "/repo")
	record.Presentation = PresentationDetached
	records := &fakeShimFleetRecords{events: events, record: record}
	starter := &fakeDetachedShimStarter{events: events, processes: []*fakeDetachedShimProcess{{pid: 4321, wait: make(chan error)}}}
	dependencies := shimLaunchTestDependencies(events)
	dependencies.DetachedStarter = starter
	relauncher := NewShimRelauncher(nil, &fakeShimLifecycle{events: events, observe: []shim.Response{runningShimResponse(4321, 7001)}}, records, &fakeShimRoleInspector{events: events, observation: ShimRoleObservation{Outcome: shim.OutcomeMissing}}, dependencies)
	model := "gpt-5.6-sol"

	_, err := relauncher.Relaunch(context.Background(), "fleet", RelaunchRequest{Role: "planner", Model: &model})
	if err != nil {
		t.Fatalf("Relaunch() error = %v", err)
	}
	if records.replaced == nil || records.replaced.Presentation != PresentationDetached {
		t.Fatalf("replacement = %#v, want detached presentation preserved", records.replaced)
	}
	if got, want := records.replaced.Roles["planner"].Model, model; got != want {
		t.Fatalf("replacement planner model = %q, want %q", got, want)
	}
}

// This catches detached relaunch returning a raw readiness failure after Start
// instead of retaining the pre-existing fleet record and avoiding peer stop.
func TestShimRelaunchDetachedReadinessFailureRetainsRecordWithoutStoppingRole(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	record := mustShimFleetRecord(t, "fleet", "/repo")
	record.Presentation = PresentationDetached
	records := &fakeShimFleetRecords{events: events, record: record}
	starter := &fakeDetachedShimStarter{events: events, processes: []*fakeDetachedShimProcess{{pid: 4321, wait: make(chan error)}}}
	lifecycle := &fakeShimLifecycle{events: events, observe: []shim.Response{{Version: shim.ShimProtocolVersion, Outcome: shim.OutcomeOrphan}}}
	dependencies := shimLaunchTestDependencies(events)
	dependencies.DetachedStarter = starter
	relauncher := NewShimRelauncher(nil, lifecycle, records, &fakeShimRoleInspector{events: events, observation: ShimRoleObservation{Outcome: shim.OutcomeMissing}}, dependencies)

	_, err := relauncher.Relaunch(context.Background(), "fleet", RelaunchRequest{Role: "planner"})
	var retained *ShimDetachedStartRetainedError
	if !errors.As(err, &retained) || retained.CreatedPID != 4321 {
		t.Fatalf("Relaunch() error = %T %v, want retained detached start for PID 4321", err, err)
	}
	if got, want := retained.Remaining, "unreconciled detached shim PID 4321 runtime state and durable fleet record"; got != want {
		t.Fatalf("retained remaining = %q, want %q", got, want)
	}
	for _, event := range events.snapshot() {
		if event == "stop:planner" || event == "record-remove" || event == "record-replace:fleet" {
			t.Fatalf("detached relaunch failure mutated peer or pre-existing record: events=%#v", events.snapshot())
		}
	}
}

// This catches detached relaunch collapsing observed child exit and caller
// cancellation into generic errors that imply neither retention nor PID facts.
func TestShimRelaunchDetachedExitAndCancellationRetainRecordedFleet(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name          string
		context       func() context.Context
		wait          chan error
		wantUncertain bool
	}{
		{name: "waiter exit", context: context.Background, wait: func() chan error { done := make(chan error, 1); done <- errors.New("exited"); return done }()},
		{name: "cancellation", context: func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }, wait: make(chan error), wantUncertain: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := &shimEventLog{}
			record := mustShimFleetRecord(t, "fleet", "/repo")
			record.Presentation = PresentationDetached
			starter := &fakeDetachedShimStarter{events: events, processes: []*fakeDetachedShimProcess{{pid: 4321, wait: test.wait}}}
			dependencies := shimLaunchTestDependencies(events)
			dependencies.DetachedStarter = starter
			relauncher := NewShimRelauncher(nil, &fakeShimLifecycle{events: events}, &fakeShimFleetRecords{events: events, record: record}, &fakeShimRoleInspector{events: events, observation: ShimRoleObservation{Outcome: shim.OutcomeMissing}}, dependencies)

			_, err := relauncher.Relaunch(test.context(), "fleet", RelaunchRequest{Role: "planner"})
			if test.wantUncertain {
				var uncertain *ShimDetachedStartUncertainError
				if !errors.As(err, &uncertain) || uncertain.CreatedPID != 4321 {
					t.Fatalf("Relaunch() error = %T %v, want uncertain PID 4321", err, err)
				}
			} else {
				var rolledBack *ShimDetachedStartRolledBackError
				if !errors.As(err, &rolledBack) || rolledBack.CreatedPID != 4321 {
					t.Fatalf("Relaunch() error = %T %v, want rolled-back PID 4321", err, err)
				}
			}
			for _, event := range events.snapshot() {
				if strings.HasPrefix(event, "stop:") || event == "record-remove" || event == "record-replace:fleet" {
					t.Fatalf("failure mutated pre-existing fleet: events=%#v", events.snapshot())
				}
			}
		})
	}
}

// This catches detached relaunch post-exit reconciliation depending on a fake
// OutcomeMissing response that production shim.Client cannot return after the
// lock/socket cleanup. Complete typed artifact absence is the rollback fact.
func TestShimRelaunchDetachedImmediateExitUsesProductionArtifactAbsence(t *testing.T) {
	events := &shimEventLog{}
	inspector, lifecycle := emptyRuntimeArtifactInspector(t, events)
	record := mustShimFleetRecord(t, "fleet", "/repo")
	record.Presentation = PresentationDetached
	wait := make(chan error, 1)
	wait <- errors.New("exit status 1")
	dependencies := shimLaunchTestDependencies(events)
	dependencies.DetachedStarter = &fakeDetachedShimStarter{events: events, processes: []*fakeDetachedShimProcess{{pid: 4321, wait: wait}}}
	dependencies.ArtifactInspector = inspector
	start := time.Unix(1000, 0)
	nowCalls := 0
	dependencies.Now = func() time.Time {
		nowCalls++
		if nowCalls <= 2 {
			return start
		}
		return start.Add(ptyx.ReadinessTimeout)
	}
	relauncher := NewShimRelauncher(nil, lifecycle, &fakeShimFleetRecords{events: events, record: record}, inspector, dependencies)

	_, err := relauncher.Relaunch(context.Background(), "fleet", RelaunchRequest{Role: "planner"})
	rolledBack, ok := err.(*ShimDetachedStartRolledBackError)
	if !ok || rolledBack.CreatedPID != 4321 {
		t.Fatalf("Relaunch() error = %T %v, want rolled back after production artifact absence", err, err)
	}
	want := []string{"record-read:fleet", "self", "look:amq", "look:claude", "detached-start:planner", "artifacts:planner"}
	if got := events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("production-equivalent relaunch events = %#v, want artifact proof with fleet retained %#v", got, want)
	}
}

func TestShimRelaunchRemovesStaleRecordOnlyAfterFreshESRCHThenStartsOneShim(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	records := &fakeShimFleetRecords{events: events, record: mustShimFleetRecord(t, "fleet", "/repo")}
	presentation := &fakeShimPresentation{
		events:  events,
		found:   &tmuxx.Session{ID: "$4", Name: "fleet"},
		windows: []tmuxx.CreatedWindow{{WindowID: "@8", PaneID: "%10", PanePID: 5432}},
	}
	lifecycle := &fakeShimLifecycle{events: events, observe: []shim.Response{runningShimResponse(5432, 7002)}}
	inspector := &fakeShimRoleInspector{
		events:            events,
		observation:       ShimRoleObservation{Outcome: shim.OutcomeStaleRecord, ChildPID: 7001},
		removeObservation: shim.ProcessResult{Observation: shim.ProcessAbsent},
	}
	relauncher := NewShimRelauncher(presentation, lifecycle, records, inspector, shimLaunchTestDependencies(events))

	result, err := relauncher.Relaunch(context.Background(), "fleet", RelaunchRequest{Role: "planner"})
	if err != nil {
		t.Fatalf("Relaunch() error = %v", err)
	}
	if result.Role != "planner" || result.Session != "fleet" || result.WindowID != "@8" {
		t.Fatalf("Relaunch() = %#v, want planner/fleet/@8", result)
	}
	want := []string{
		"record-read:fleet", "inspect:planner",
		"self", "look:tmux", "look:amq", "look:claude",
		"remove-stale:planner",
		"presentation-find:fleet",
		"presentation-window:planner:exec '/current agentctl' '__shim' '--session' 'fleet' '--role' 'planner' '--harness' 'claude' '--effort' 'max'",
		"observe:planner",
	}
	if got := events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("relaunch events = %#v, want %#v", got, want)
	}
}

func TestShimRelaunchFreshAbsenceDisagreementRefusesBeforePresentation(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	records := &fakeShimFleetRecords{events: events, record: mustShimFleetRecord(t, "fleet", "/repo")}
	presentation := &fakeShimPresentation{events: events}
	lifecycle := &fakeShimLifecycle{events: events}
	inspector := &fakeShimRoleInspector{
		events:            events,
		observation:       ShimRoleObservation{Outcome: shim.OutcomeStaleRecord, ChildPID: 7001},
		removeObservation: shim.ProcessResult{Observation: shim.ProcessPresentTokenDisagreement},
	}
	relauncher := NewShimRelauncher(presentation, lifecycle, records, inspector, shimLaunchTestDependencies(events))

	_, err := relauncher.Relaunch(context.Background(), "fleet", RelaunchRequest{Role: "planner"})
	var refusal *ShimRelaunchRefusalError
	if !errors.As(err, &refusal) || refusal.Outcome != shim.OutcomePresentTokenDisagreement {
		t.Fatalf("Relaunch() error = %T %v, want fresh token-disagreement refusal", err, err)
	}
	want := []string{
		"record-read:fleet", "inspect:planner",
		"self", "look:tmux", "look:amq", "look:claude",
		"remove-stale:planner",
	}
	if got := events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("relaunch events = %#v, want no presentation after fresh disagreement %#v", got, want)
	}
}

func TestShimRelaunchCreatesNewPresentationSessionWhenRuntimeFleetHasNoTmuxPresentation(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	records := &fakeShimFleetRecords{events: events, record: mustShimFleetRecord(t, "fleet", "/repo")}
	presentation := &fakeShimPresentation{
		events:  events,
		session: tmuxx.CreatedSession{SessionID: "$9", WindowID: "@12", PaneID: "%14", PanePID: 6543},
	}
	lifecycle := &fakeShimLifecycle{events: events, observe: []shim.Response{runningShimResponse(6543, 8001)}}
	inspector := &fakeShimRoleInspector{events: events, observation: ShimRoleObservation{Outcome: shim.OutcomeMissing}}
	relauncher := NewShimRelauncher(presentation, lifecycle, records, inspector, shimLaunchTestDependencies(events))

	result, err := relauncher.Relaunch(context.Background(), "fleet", RelaunchRequest{Role: "planner"})
	if err != nil {
		t.Fatalf("Relaunch() error = %v", err)
	}
	if result.PresentationSessionID != "$9" || result.WindowID != "@12" {
		t.Fatalf("Relaunch() = %#v, want new optional presentation IDs", result)
	}
	got := events.snapshot()
	if !containsShimEvent(got, "presentation-find:fleet") || !containsShimEventPrefix(got, "presentation-session:planner:") {
		t.Fatalf("presentation-gone relaunch events = %#v, want new presentation session", got)
	}
}

func TestShimRelaunchConcurrentClaimLoserRemovesOnlyItsCreatedPresentationWindow(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	records := &fakeShimFleetRecords{events: events, record: mustShimFleetRecord(t, "fleet", "/repo")}
	presentation := &fakeShimPresentation{
		events: events,
		found:  &tmuxx.Session{ID: "$4", Name: "fleet"},
		windows: []tmuxx.CreatedWindow{{
			WindowID: "@8", PaneID: "%10", PanePID: 5432,
		}},
	}
	lifecycle := &fakeShimLifecycle{events: events, observe: []shim.Response{runningShimResponse(9001, 7002)}}
	inspector := &fakeShimRoleInspector{events: events, observation: ShimRoleObservation{Outcome: shim.OutcomeMissing}}
	relauncher := NewShimRelauncher(presentation, lifecycle, records, inspector, shimLaunchTestDependencies(events))

	_, err := relauncher.Relaunch(context.Background(), "fleet", RelaunchRequest{Role: "planner"})
	var refusal *ShimRelaunchRefusalError
	if !errors.As(err, &refusal) || refusal.Outcome != shim.OutcomeConcurrentContender {
		t.Fatalf("Relaunch() error = %T %v, want concurrent-contender refusal", err, err)
	}
	wantTail := []string{"observe:planner", "presentation-window-remove:@8"}
	got := events.snapshot()
	if len(got) < len(wantTail) || !reflect.DeepEqual(got[len(got)-len(wantTail):], wantTail) {
		t.Fatalf("concurrent loser event tail = %#v, want exact owned-window cleanup %#v", got, wantTail)
	}
	for _, event := range got {
		if strings.HasPrefix(event, "stop:") || strings.HasPrefix(event, "presentation-session-remove:") || event == "record-remove" {
			t.Fatalf("concurrent loser removed peer-owned runtime/session/record: events=%#v", got)
		}
	}
}

func TestShimRelaunchPersistsOverridesOnlyAfterOwnedShimIsObservedReady(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	records := &fakeShimFleetRecords{events: events, record: mustShimFleetRecord(t, "fleet", "/repo")}
	presentation := &fakeShimPresentation{
		events: events, found: &tmuxx.Session{ID: "$4", Name: "fleet"},
		windows: []tmuxx.CreatedWindow{{WindowID: "@8", PaneID: "%10", PanePID: 5432}},
	}
	lifecycle := &fakeShimLifecycle{events: events, observe: []shim.Response{runningShimResponse(5432, 7002)}}
	inspector := &fakeShimRoleInspector{events: events, observation: ShimRoleObservation{Outcome: shim.OutcomeMissing}}
	dependencies := shimLaunchTestDependencies(events)
	dependencies.Stat = func(path string) (os.FileInfo, error) {
		if path != "/override" {
			t.Fatalf("Stat(%q), want override directory", path)
		}
		return testFileInfo{mode: os.ModeDir | 0o755}, nil
	}
	relauncher := NewShimRelauncher(presentation, lifecycle, records, inspector, dependencies)
	harness, model, effort, directory := "codex", "gpt-5.6-sol", "high", "/override"

	_, err := relauncher.Relaunch(context.Background(), "fleet", RelaunchRequest{
		Role: "planner", Harness: &harness, Model: &model, Effort: &effort, Directory: &directory,
	})
	if err != nil {
		t.Fatalf("Relaunch() error = %v", err)
	}
	got := events.snapshot()
	if len(got) < 2 || !reflect.DeepEqual(got[len(got)-2:], []string{"observe:planner", "record-replace:fleet"}) {
		t.Fatalf("override event tail = %#v, want readiness before durable replacement", got)
	}
	if records.replaced == nil || records.replaced.Directory != "/override" || records.replaced.Roles["planner"] != (ShimFleetRoleRecord{
		Harness: "codex", Model: "gpt-5.6-sol", Effort: "high",
	}) {
		t.Fatalf("replacement fleet record = %#v, want persisted override", records.replaced)
	}
}

func TestShimRelaunchResolvesRelativeDirectoryOverrideBeforePresentationMutation(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		directory string
		want      string
	}{
		{directory: ".", want: "/workspace/current"},
		{directory: "..", want: "/workspace"},
		{directory: "../override", want: "/workspace/override"},
	} {
		t.Run(test.directory, func(t *testing.T) {
			events := &shimEventLog{}
			records := &fakeShimFleetRecords{events: events, record: mustShimFleetRecord(t, "fleet", "/repo")}
			presentation := &fakeShimPresentation{
				events: events, found: &tmuxx.Session{ID: "$4", Name: "fleet"},
				windows: []tmuxx.CreatedWindow{{WindowID: "@8", PaneID: "%10", PanePID: 5432}},
			}
			lifecycle := &fakeShimLifecycle{events: events, observe: []shim.Response{runningShimResponse(5432, 7002)}}
			inspector := &fakeShimRoleInspector{events: events, observation: ShimRoleObservation{Outcome: shim.OutcomeMissing}}
			dependencies := shimLaunchTestDependencies(events)
			dependencies.Getwd = func() (string, error) { return "/workspace/current", nil }
			dependencies.Stat = func(path string) (os.FileInfo, error) {
				if path != test.want {
					t.Fatalf("Stat(%q), want absolute override %q before presentation mutation", path, test.want)
				}
				return testFileInfo{mode: os.ModeDir | 0o755}, nil
			}
			relauncher := NewShimRelauncher(presentation, lifecycle, records, inspector, dependencies)

			_, err := relauncher.Relaunch(context.Background(), "fleet", RelaunchRequest{Role: "planner", Directory: &test.directory})
			if err != nil {
				t.Fatalf("Relaunch() error = %v", err)
			}
			if records.replaced == nil || records.replaced.Directory != test.want {
				t.Fatalf("replacement directory = %#v, want absolute override %q", records.replaced, test.want)
			}
			if !reflect.DeepEqual(presentation.directories, []string{test.want}) {
				t.Fatalf("presentation directories = %#v, want exact tmux -c input %q", presentation.directories, test.want)
			}
		})
	}
}

func TestShimRelaunchDefiniteFleetUpdateFailureStopsOwnedChildBeforeRemovingOwnedPresentation(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	records := &fakeShimFleetRecords{
		events: events, record: mustShimFleetRecord(t, "fleet", "/repo"),
		replaceErr: errors.New("injected definite replacement failure"),
	}
	presentation := &fakeShimPresentation{
		events: events, found: &tmuxx.Session{ID: "$4", Name: "fleet"},
		windows: []tmuxx.CreatedWindow{{WindowID: "@8", PaneID: "%10", PanePID: 5432}},
	}
	lifecycle := &fakeShimLifecycle{
		events: events, observe: []shim.Response{runningShimResponse(5432, 7002)},
		stop: []shim.Response{stoppedShimResponse(7002)},
	}
	inspector := &fakeShimRoleInspector{events: events, observation: ShimRoleObservation{Outcome: shim.OutcomeMissing}}
	relauncher := NewShimRelauncher(presentation, lifecycle, records, inspector, shimLaunchTestDependencies(events))
	model := "override"

	_, err := relauncher.Relaunch(context.Background(), "fleet", RelaunchRequest{Role: "planner", Model: &model})
	var rollback *ShimRelaunchRollbackError
	if !errors.As(err, &rollback) {
		t.Fatalf("Relaunch() error = %T %v, want *ShimRelaunchRollbackError", err, err)
	}
	wantTail := []string{"record-replace:fleet", "stop:planner", "presentation-window-remove:@8"}
	got := events.snapshot()
	if len(got) < len(wantTail) || !reflect.DeepEqual(got[len(got)-len(wantTail):], wantTail) {
		t.Fatalf("definite update failure event tail = %#v, want child-before-presentation cleanup %#v", got, wantTail)
	}
}

func TestShimRelaunchUncertainFleetUpdateRetainsRunningRoleWithoutRetryOrCleanup(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	records := &fakeShimFleetRecords{
		events: events, record: mustShimFleetRecord(t, "fleet", "/repo"),
		replaceErr: &shim.RecordCommitUncertainError{Err: errors.New("directory sync failed")},
	}
	presentation := &fakeShimPresentation{
		events: events, found: &tmuxx.Session{ID: "$4", Name: "fleet"},
		windows: []tmuxx.CreatedWindow{{WindowID: "@8", PaneID: "%10", PanePID: 5432}},
	}
	lifecycle := &fakeShimLifecycle{events: events, observe: []shim.Response{runningShimResponse(5432, 7002)}}
	inspector := &fakeShimRoleInspector{events: events, observation: ShimRoleObservation{Outcome: shim.OutcomeMissing}}
	relauncher := NewShimRelauncher(presentation, lifecycle, records, inspector, shimLaunchTestDependencies(events))
	model := "override"

	_, err := relauncher.Relaunch(context.Background(), "fleet", RelaunchRequest{Role: "planner", Model: &model})
	var uncertain *shim.RecordCommitUncertainError
	if !errors.As(err, &uncertain) {
		t.Fatalf("Relaunch() error = %T %v, want uncertain fleet update", err, err)
	}
	got := events.snapshot()
	if got[len(got)-1] != "record-replace:fleet" {
		t.Fatalf("uncertain update events = %#v, want no retry or cleanup after replacement", got)
	}
}

func TestShimRelaunchRejectsStoredNonDirectoryBeforePresentationMutation(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	records := &fakeShimFleetRecords{events: events, record: mustShimFleetRecord(t, "fleet", "/not-a-directory")}
	presentation := &fakeShimPresentation{events: events}
	lifecycle := &fakeShimLifecycle{events: events}
	inspector := &fakeShimRoleInspector{events: events, observation: ShimRoleObservation{Outcome: shim.OutcomeMissing}}
	dependencies := shimLaunchTestDependencies(events)
	dependencies.Stat = func(string) (os.FileInfo, error) { return testFileInfo{mode: 0o600}, nil }
	relauncher := NewShimRelauncher(presentation, lifecycle, records, inspector, dependencies)

	_, err := relauncher.Relaunch(context.Background(), "fleet", RelaunchRequest{Role: "planner"})
	var stored *StoredDirectoryError
	if !errors.As(err, &stored) {
		t.Fatalf("Relaunch() error = %T %v, want *StoredDirectoryError", err, err)
	}
	for _, event := range events.snapshot() {
		if strings.HasPrefix(event, "presentation-") || strings.HasPrefix(event, "remove-stale:") {
			t.Fatalf("invalid stored directory allowed mutation: events=%#v", events.snapshot())
		}
	}
}

func TestRuntimeShimRoleInspectorMapsProcessOracleWithoutMutatingDurableRecord(t *testing.T) {
	processCases := []struct {
		name   string
		result shim.ProcessResult
		want   shim.Outcome
	}{
		{name: "ESRCH absent", result: shim.ProcessResult{Observation: shim.ProcessAbsent}, want: shim.OutcomeStaleRecord},
		{name: "present matching orphan", result: shim.ProcessResult{Observation: shim.ProcessPresentMatch}, want: shim.OutcomeOrphan},
		{name: "present token disagreement", result: shim.ProcessResult{Observation: shim.ProcessPresentTokenDisagreement}, want: shim.OutcomePresentTokenDisagreement},
		{name: "EPERM present not ours", result: shim.ProcessResult{Observation: shim.ProcessPresentNotOurs, Err: errors.New("operation not permitted")}, want: shim.OutcomePresentNotOurs},
		{name: "token or syscall observation error", result: shim.ProcessResult{Observation: shim.ProcessCouldNotObserve, Err: errors.New("sysctl failed")}, want: shim.OutcomeCouldNotObserve},
	}
	for _, tt := range processCases {
		t.Run(tt.name, func(t *testing.T) {
			namespace, path := runtimeInspectorFixture(t, shim.RecordStateChildRecorded)
			lifecycle := inspectorLifecycle{err: &net.OpError{Op: "dial", Net: "unix", Err: syscall.ECONNREFUSED}}
			inspector := NewRuntimeShimRoleInspector(namespace, lifecycle)
			inspector.observeProcess = func(pid int, token shim.StartToken) shim.ProcessResult {
				if pid != 7001 || token != (shim.StartToken{Sec: 11, Usec: 22}) {
					t.Fatalf("observeProcess(%d, %#v), want recorded child identity", pid, token)
				}
				return tt.result
			}

			got, err := inspector.Inspect(context.Background(), "fleet", "planner")
			if err != nil {
				t.Fatalf("Inspect() error = %v", err)
			}
			if got.Outcome != tt.want || got.ChildPID != 7001 {
				t.Fatalf("Inspect() = %#v, want %s for child 7001", got, tt.want)
			}
			if _, err := shim.ReadRecord(path); err != nil {
				t.Fatalf("Inspect() mutated durable record: %v", err)
			}
		})
	}
}

func TestRuntimeShimRoleInspectorNeverAutoDeletesDeadChildStarting(t *testing.T) {
	namespace, path := runtimeInspectorFixture(t, shim.RecordStateChildStarting)
	inspector := NewRuntimeShimRoleInspector(namespace, inspectorLifecycle{err: &net.OpError{Op: "dial", Net: "unix", Err: syscall.ECONNREFUSED}})
	inspector.observeProcess = func(int, shim.StartToken) shim.ProcessResult {
		t.Fatal("child-starting must not invoke the child process oracle")
		return shim.ProcessResult{}
	}

	got, err := inspector.Inspect(context.Background(), "fleet", "planner")
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if got.Outcome != shim.OutcomeIndeterminateChildStarting {
		t.Fatalf("Inspect() outcome = %q, want indeterminate-child-starting", got.Outcome)
	}
	if _, err := shim.ReadRecord(path); err != nil {
		t.Fatalf("Inspect() deleted child-starting record: %v", err)
	}
}

func TestRuntimeShimRoleInspectorRefusesMalformedProtocolWithoutUsingChildAbsenceOracle(t *testing.T) {
	namespace, path := runtimeInspectorFixture(t, shim.RecordStateChildRecorded)
	inspector := NewRuntimeShimRoleInspector(namespace, inspectorLifecycle{err: &shim.ProtocolSchemaError{
		Kind: shim.ProtocolSchemaUnknownField, Field: "future",
	}})
	inspector.observeProcess = func(int, shim.StartToken) shim.ProcessResult {
		t.Fatal("malformed connected protocol must not invoke dead-shim child fallback")
		return shim.ProcessResult{}
	}

	got, err := inspector.Inspect(context.Background(), "fleet", "planner")
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if got.Outcome != shim.OutcomeCouldNotObserve {
		t.Fatalf("Inspect() outcome = %q, want could-not-observe protocol refusal", got.Outcome)
	}
	if _, err := shim.ReadRecord(path); err != nil {
		t.Fatalf("Inspect() mutated durable record: %v", err)
	}
}

func TestRuntimeShimRoleInspectorDoesNotReportMissingFromAdvisoryWithoutRoleRecord(t *testing.T) {
	namespace, path := runtimeInspectorFixture(t, shim.RecordStateChildStarting)
	if err := shim.RemoveRecord(path); err != nil {
		t.Fatalf("RemoveRecord(fixture): %v", err)
	}
	inspector := NewRuntimeShimRoleInspector(namespace, inspectorLifecycle{err: errors.New("observe must not be called")})

	got, err := inspector.Inspect(context.Background(), "fleet", "planner")
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if got.Outcome != shim.OutcomeInvalidRecord {
		t.Fatalf("Inspect() outcome = %q, want invalid-record for advisory without durable record", got.Outcome)
	}
}

func TestRuntimeShimRoleInspectorReportsMissingOnlyWhenAdvisoryAndRecordAreBothAbsent(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "a5-")
	if err != nil {
		t.Fatalf("MkdirTemp(short root): %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	t.Setenv("AGENTCTL_RUNTIME_ROOT", filepath.Join(base, "runtime"))
	t.Setenv("AGENTCTL_STATE_ROOT", filepath.Join(base, "state"))
	namespace, err := shim.OpenNamespace()
	if err != nil {
		t.Fatalf("OpenNamespace() error = %v", err)
	}
	t.Cleanup(func() { _ = namespace.Close() })
	path, err := namespace.RolePath("fleet", "planner")
	if err != nil {
		t.Fatalf("RolePath() error = %v", err)
	}
	if err := path.Close(); err != nil {
		t.Fatalf("RolePath.Close() error = %v", err)
	}

	inspector := NewRuntimeShimRoleInspector(namespace, inspectorLifecycle{err: errors.New("observe must not be called")})
	got, err := inspector.Inspect(context.Background(), "fleet", "planner")
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if got.Outcome != shim.OutcomeMissing {
		t.Fatalf("Inspect() outcome = %q, want missing when advisory and record are absent", got.Outcome)
	}
}

func runtimeInspectorFixture(t *testing.T, state shim.RecordState) (*shim.Namespace, *shim.RolePath) {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "a5-")
	if err != nil {
		t.Fatalf("MkdirTemp(short root): %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	runtimeRoot := filepath.Join(base, "runtime")
	stateRoot := filepath.Join(base, "state")
	t.Setenv("AGENTCTL_RUNTIME_ROOT", runtimeRoot)
	t.Setenv("AGENTCTL_STATE_ROOT", stateRoot)
	namespace, err := shim.OpenNamespace()
	if err != nil {
		t.Fatalf("OpenNamespace() error = %v", err)
	}
	t.Cleanup(func() { _ = namespace.Close() })
	path, err := namespace.RolePath("fleet", "planner")
	if err != nil {
		t.Fatalf("RolePath() error = %v", err)
	}
	t.Cleanup(func() { _ = path.Close() })
	claim, err := shim.AcquireClaim(path, shim.Advisory{
		Version: shim.ShimProtocolVersion, ShimPID: os.Getpid(), Nonce: "fixture", StateRoot: namespace.StateRoot,
	})
	if err != nil {
		t.Fatalf("AcquireClaim() error = %v", err)
	}
	reservation := shim.NewChildStartingRecord("fleet", "planner", os.Getpid(), "fixture")
	record := reservation
	if state == shim.RecordStateChildRecorded {
		record, err = reservation.WithChild(7001, shim.StartToken{Sec: 11, Usec: 22})
		if err != nil {
			t.Fatalf("WithChild() error = %v", err)
		}
	}
	if err := shim.WriteRecord(path, record); err != nil {
		t.Fatalf("WriteRecord() error = %v", err)
	}
	if err := claim.Close(); err != nil {
		t.Fatalf("Claim.Close() error = %v", err)
	}
	return namespace, path
}

func emptyRuntimeArtifactInspector(t *testing.T, events *shimEventLog) (*recordingRuntimeArtifactInspector, ShimLifecycle) {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "a5-")
	if err != nil {
		t.Fatalf("MkdirTemp(short root): %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	t.Setenv("AGENTCTL_RUNTIME_ROOT", filepath.Join(base, "runtime"))
	t.Setenv("AGENTCTL_STATE_ROOT", filepath.Join(base, "state"))
	namespace, err := shim.OpenNamespace()
	if err != nil {
		t.Fatalf("OpenNamespace() error = %v", err)
	}
	t.Cleanup(func() { _ = namespace.Close() })
	path, err := namespace.RolePath("fleet", "planner")
	if err != nil {
		t.Fatalf("RolePath() error = %v", err)
	}
	if err := path.Close(); err != nil {
		t.Fatalf("RolePath.Close() error = %v", err)
	}
	lifecycle := shim.NewClient(namespace)
	return &recordingRuntimeArtifactInspector{RuntimeShimRoleInspector: NewRuntimeShimRoleInspector(namespace, lifecycle), events: events}, lifecycle
}

type recordingRuntimeArtifactInspector struct {
	*RuntimeShimRoleInspector
	events *shimEventLog
}

func (i *recordingRuntimeArtifactInspector) InspectArtifacts(ctx context.Context, session, role string) (shim.RoleArtifacts, error) {
	if i.events != nil {
		i.events.add("artifacts:" + role)
	}
	return i.RuntimeShimRoleInspector.InspectArtifacts(ctx, session, role)
}

type inspectorLifecycle struct {
	response shim.Response
	err      error
}

func (l inspectorLifecycle) Observe(context.Context, string, string) (shim.Response, error) {
	return l.response, l.err
}

func (inspectorLifecycle) Stop(context.Context, string, string) (shim.Response, error) {
	return shim.Response{}, errors.New("unexpected stop")
}

func mustShimFleetRecord(t *testing.T, session, directory string) ShimFleetRecord {
	t.Helper()
	record, err := NewShimFleetRecord(session, directory, PresentationTmux, shimTestFleet())
	if err != nil {
		t.Fatalf("NewShimFleetRecord() error = %v", err)
	}
	return record
}

type fakeShimRoleInspector struct {
	events            *shimEventLog
	observation       ShimRoleObservation
	removeObservation shim.ProcessResult
}

func (f *fakeShimRoleInspector) Inspect(_ context.Context, _, role string) (ShimRoleObservation, error) {
	f.events.add("inspect:" + role)
	return f.observation, nil
}

func (f *fakeShimRoleInspector) RemoveStale(_ context.Context, _, role string, _ int) (shim.ProcessResult, error) {
	f.events.add("remove-stale:" + role)
	return f.removeObservation, nil
}

func containsShimEvent(events []string, want string) bool {
	for _, event := range events {
		if event == want {
			return true
		}
	}
	return false
}

func containsShimEventPrefix(events []string, want string) bool {
	for _, event := range events {
		if len(event) >= len(want) && event[:len(want)] == want {
			return true
		}
	}
	return false
}
