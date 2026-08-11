//go:build darwin

package kill

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/fleet"
	"github.com/mnbf9rca/agentctl/internal/shim"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestShimExecutorInspectsWholeRosterThenObservesEachStopBeforeCleanup(t *testing.T) {
	t.Parallel()

	events := &killEventLog{}
	record := killFleetRecord(t, twoKillRoles())
	records := &killRecords{events: events, record: record}
	inspector := &killInspector{events: events, observations: map[string]fleet.ShimRoleObservation{
		"planner": {Outcome: shim.OutcomeRunning, ChildPID: 7001},
		"coder":   {Outcome: shim.OutcomeStarting, ChildPID: 7002},
	}}
	lifecycle := &killLifecycle{events: events, responses: map[string]shim.Response{
		"planner": killStoppedResponse(7001),
		"coder":   killStoppedResponse(7002),
	}}
	presentation := &killPresentation{events: events, found: &tmuxx.Session{ID: "$4", Name: "fleet"}}
	executor := NewShimExecutor(lifecycle, records, inspector, presentation)

	result, err := executor.Execute(context.Background(), "fleet")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.StoppedRoles != 2 || !result.PresentationRemoved {
		t.Fatalf("Execute() = %#v, want two observed exits and removed presentation", result)
	}
	want := []string{
		"record-read:fleet", "presentation-find:fleet",
		"inspect:planner", "inspect:coder",
		"stop:planner", "inspect:planner", "stop:coder", "inspect:coder",
		"presentation-remove:$4", "record-remove:fleet",
	}
	if got := events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("kill events = %#v, want %#v", got, want)
	}
}

func TestShimExecutorRefusesIndeterminateStatesWithoutStoppingPeers(t *testing.T) {
	t.Parallel()

	refusals := []shim.Outcome{
		shim.OutcomeInvalidRecord,
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
			events := &killEventLog{}
			record := killFleetRecord(t, twoKillRoles())
			records := &killRecords{events: events, record: record}
			inspector := &killInspector{events: events, observations: map[string]fleet.ShimRoleObservation{
				"planner": {Outcome: shim.OutcomeRunning, ChildPID: 7001},
				"coder":   {Outcome: outcome, ChildPID: 7002},
			}}
			lifecycle := &killLifecycle{events: events}
			presentation := &killPresentation{events: events, found: &tmuxx.Session{ID: "$4", Name: "fleet"}}
			executor := NewShimExecutor(lifecycle, records, inspector, presentation)

			_, err := executor.Execute(context.Background(), "fleet")
			var refusal *ShimKillRefusalError
			if !errors.As(err, &refusal) || refusal.Role != "coder" || refusal.Outcome != outcome {
				t.Fatalf("Execute() error = %T %v, want coder/%s refusal", err, err, outcome)
			}
			want := []string{"record-read:fleet", "presentation-find:fleet", "inspect:planner", "inspect:coder"}
			if got := events.snapshot(); !reflect.DeepEqual(got, want) {
				t.Fatalf("refusal events = %#v, want read-only %#v", got, want)
			}
		})
	}
}

func TestShimExecutorRetainsPresentationAndFleetRecordWhenStopDoesNotObserveExit(t *testing.T) {
	t.Parallel()

	events := &killEventLog{}
	record := killFleetRecord(t, oneKillRole())
	records := &killRecords{events: events, record: record}
	inspector := &killInspector{events: events, observations: map[string]fleet.ShimRoleObservation{
		"planner": {Outcome: shim.OutcomeRunning, ChildPID: 7001},
	}}
	lifecycle := &killLifecycle{events: events, responses: map[string]shim.Response{
		"planner": killRetainedResponse(7001),
	}}
	presentation := &killPresentation{events: events, found: &tmuxx.Session{ID: "$4", Name: "fleet"}}
	executor := NewShimExecutor(lifecycle, records, inspector, presentation)

	_, err := executor.Execute(context.Background(), "fleet")
	var retained *ShimKillRetainedError
	if !errors.As(err, &retained) || retained.Role != "planner" || retained.Observation != shim.ProcessPresentMatch {
		t.Fatalf("Execute() error = %T %v, want planner present-match retained", err, err)
	}
	want := []string{"record-read:fleet", "presentation-find:fleet", "inspect:planner", "stop:planner"}
	if got := events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("retained kill events = %#v, want no cleanup %#v", got, want)
	}
}

func TestShimExecutorReportsAlreadyStoppingWithoutClaimingASecondSignal(t *testing.T) {
	t.Parallel()

	events := &killEventLog{}
	record := killFleetRecord(t, oneKillRole())
	recordState := "stopping"
	shimPID, childPID := 6001, 7001
	attempted := false
	executor := NewShimExecutor(
		&killLifecycle{events: events, responses: map[string]shim.Response{"planner": {
			Version: shim.ShimProtocolVersion, Outcome: shim.OutcomeStopAlreadyStopping,
			State: &recordState, ShimPID: &shimPID, ChildPID: &childPID, SignalAttempted: &attempted,
		}}},
		&killRecords{events: events, record: record},
		&killInspector{events: events, observations: map[string]fleet.ShimRoleObservation{
			"planner": {Outcome: shim.OutcomeRunning, ShimPID: shimPID, ChildPID: childPID},
		}},
		&killPresentation{events: events},
	)

	_, err := executor.Execute(context.Background(), "fleet")
	var refusal *ShimKillRefusalError
	if !errors.As(err, &refusal) || refusal.Outcome != shim.OutcomeStopAlreadyStopping || refusal.Observation.State != "stopping" || refusal.Observation.ShimPID != shimPID || refusal.Observation.ChildPID != childPID {
		t.Fatalf("Execute() error = %T %#v, want exact stop-already-stopping refusal", err, err)
	}
	want := []string{"record-read:fleet", "presentation-find:fleet", "inspect:planner", "stop:planner"}
	if got := events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events=%#v, want retained ownership %#v", got, want)
	}
}

func TestShimExecutorSucceedsWithoutTmuxPresentation(t *testing.T) {
	t.Parallel()

	events := &killEventLog{}
	record := killFleetRecord(t, oneKillRole())
	records := &killRecords{events: events, record: record}
	inspector := &killInspector{events: events, observations: map[string]fleet.ShimRoleObservation{
		"planner": {Outcome: shim.OutcomeStaleRecord, ChildPID: 7001},
	}}
	lifecycle := &killLifecycle{events: events}
	presentation := &killPresentation{events: events}
	executor := NewShimExecutor(lifecycle, records, inspector, presentation)

	result, err := executor.Execute(context.Background(), "fleet")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.PresentationRemoved || result.StoppedRoles != 0 {
		t.Fatalf("Execute() = %#v, want no presentation and already-absent role", result)
	}
	want := []string{"record-read:fleet", "presentation-find:fleet", "inspect:planner", "remove-stale:planner", "record-remove:fleet"}
	if got := events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("presentation-gone events = %#v, want %#v", got, want)
	}
}

func TestShimExecutorWaitsForStoppedRoleArtifactsToDisappearBeforeFleetRemoval(t *testing.T) {
	t.Parallel()

	events := &killEventLog{}
	record := killFleetRecord(t, oneKillRole())
	inspector := &killInspector{events: events, sequences: map[string][]fleet.ShimRoleObservation{
		"planner": {
			{Outcome: shim.OutcomeRunning, ChildPID: 7001},
			{Outcome: shim.OutcomeInvalidRecord, ChildPID: 7001},
			{Outcome: shim.OutcomeStopped, ChildPID: 7001},
			{Outcome: shim.OutcomeMissing},
		},
	}}
	executor := NewShimExecutor(
		&killLifecycle{events: events, responses: map[string]shim.Response{"planner": killStoppedResponse(7001)}},
		&killRecords{events: events, record: record}, inspector, &killPresentation{events: events},
	)

	result, err := executor.Execute(context.Background(), "fleet")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.StoppedRoles != 1 {
		t.Fatalf("Execute() = %#v, want one stopped role", result)
	}
	want := []string{
		"record-read:fleet", "presentation-find:fleet", "inspect:planner", "stop:planner",
		"inspect:planner", "inspect:planner", "inspect:planner", "record-remove:fleet",
	}
	if got := events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("cleanup-drain events = %#v, want %#v", got, want)
	}
}

func TestShimExecutorPostExitCleanupTimeoutRetainsPresentationAndFleetTruthfully(t *testing.T) {
	t.Parallel()

	events := &killEventLog{}
	record := killFleetRecord(t, oneKillRole())
	inspector := &killInspector{events: events, sequences: map[string][]fleet.ShimRoleObservation{
		"planner": {
			{Outcome: shim.OutcomeRunning, ChildPID: 7001},
			{Outcome: shim.OutcomeInvalidRecord, ChildPID: 7001},
		},
	}}
	executor := NewShimExecutor(
		&killLifecycle{events: events, responses: map[string]shim.Response{"planner": killStoppedResponse(7001)}},
		&killRecords{events: events, record: record}, inspector,
		&killPresentation{events: events, found: &tmuxx.Session{ID: "$4", Name: "fleet"}},
	)
	executor.cleanupTimeout = 0

	result, err := executor.Execute(context.Background(), "fleet")
	var retained *ShimKillCleanupRetainedError
	if !errors.As(err, &retained) || retained.ChildPID != 7001 || retained.LastOutcome != shim.OutcomeInvalidRecord || retained.Cause == nil {
		t.Fatalf("Execute() error = %T %#v, want truthful post-exit cleanup retention", err, err)
	}
	if result.StoppedRoles != 1 || result.PresentationRemoved || result.PresentationGone {
		t.Fatalf("Execute() = %#v, want child-exit fact with presentation/fleet retained", result)
	}
	want := []string{"record-read:fleet", "presentation-find:fleet", "inspect:planner", "stop:planner", "inspect:planner"}
	if got := events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("post-exit retention events = %#v, want no presentation/fleet cleanup %#v", got, want)
	}
}

func TestShimExecutorObservesPresentationGoneAfterExactIDRemovalRacesAutoDestruction(t *testing.T) {
	t.Parallel()

	events := &killEventLog{}
	record := killFleetRecord(t, oneKillRole())
	records := &killRecords{events: events, record: record}
	inspector := &killInspector{events: events, observations: map[string]fleet.ShimRoleObservation{
		"planner": {Outcome: shim.OutcomeRunning, ChildPID: 7001},
	}}
	lifecycle := &killLifecycle{events: events, responses: map[string]shim.Response{
		"planner": killStoppedResponse(7001),
	}}
	presentation := &killPresentation{
		events: events,
		findResults: []killPresentationFindResult{
			{session: tmuxx.Session{ID: "$4", Name: "fleet"}, present: true},
			{},
		},
		removeErr: errors.New("tmux kill session: exit status 1"),
	}
	executor := NewShimExecutor(lifecycle, records, inspector, presentation)

	result, err := executor.Execute(context.Background(), "fleet")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.StoppedRoles != 1 || result.PresentationRemoved || !result.PresentationGone {
		t.Fatalf("Execute() = %#v, want one observed exit and post-failure presentation-gone fact", result)
	}
	want := []string{
		"record-read:fleet", "presentation-find:fleet", "inspect:planner", "stop:planner",
		"inspect:planner", "presentation-remove:$4", "presentation-find:fleet", "record-remove:fleet",
	}
	if got := events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("auto-destruction events = %#v, want %#v", got, want)
	}
}

func TestShimExecutorRetainsFleetRecordWhenFailedExactIDRemovalCannotProvePresentationGone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		second      killPresentationFindResult
		wantMessage string
	}{
		{
			name:   "presentation remains present",
			second: killPresentationFindResult{session: tmuxx.Session{ID: "$5", Name: "fleet"}, present: true},
			wantMessage: "shim kill retained fleet record for session \"fleet\": exact-ID presentation removal of \"$4\" failed: " +
				"\"tmux kill session: exit status 1\"; post-removal presentation \"$5\" remained present",
		},
		{
			name:   "presentation observation unavailable",
			second: killPresentationFindResult{err: errors.New("tmux list sessions: permission denied")},
			wantMessage: "shim kill retained fleet record for session \"fleet\": exact-ID presentation removal of \"$4\" failed: " +
				"\"tmux kill session: exit status 1\"; post-removal presentation observation failed: \"tmux list sessions: permission denied\"",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			events := &killEventLog{}
			record := killFleetRecord(t, oneKillRole())
			records := &killRecords{events: events, record: record}
			inspector := &killInspector{events: events, observations: map[string]fleet.ShimRoleObservation{
				"planner": {Outcome: shim.OutcomeRunning, ChildPID: 7001},
			}}
			lifecycle := &killLifecycle{events: events, responses: map[string]shim.Response{
				"planner": killStoppedResponse(7001),
			}}
			presentation := &killPresentation{
				events: events,
				findResults: []killPresentationFindResult{
					{session: tmuxx.Session{ID: "$4", Name: "fleet"}, present: true},
					tt.second,
				},
				removeErr: errors.New("tmux kill session: exit status 1"),
			}
			executor := NewShimExecutor(lifecycle, records, inspector, presentation)

			result, err := executor.Execute(context.Background(), "fleet")
			var retained *ShimKillPresentationRetainedError
			if !errors.As(err, &retained) || err.Error() != tt.wantMessage {
				t.Fatalf("Execute() error = %T %v, want typed retained error %q", err, err, tt.wantMessage)
			}
			if result.StoppedRoles != 1 || result.PresentationRemoved || result.PresentationGone {
				t.Fatalf("Execute() = %#v, want observed child exit with presentation outcome unproved", result)
			}
			want := []string{
				"record-read:fleet", "presentation-find:fleet", "inspect:planner", "stop:planner",
				"inspect:planner", "presentation-remove:$4", "presentation-find:fleet",
			}
			if got := events.snapshot(); !reflect.DeepEqual(got, want) {
				t.Fatalf("retained presentation events = %#v, want no fleet-record removal %#v", got, want)
			}
		})
	}
}

func killFleetRecord(t *testing.T, roles []config.RoleConfig) fleet.ShimFleetRecord {
	t.Helper()
	record, err := fleet.NewShimFleetRecord("fleet", "/repo", config.FleetConfig{Roles: roles})
	if err != nil {
		t.Fatalf("NewShimFleetRecord() error = %v", err)
	}
	return record
}

func oneKillRole() []config.RoleConfig {
	return []config.RoleConfig{{Name: "planner", Harness: config.HarnessClaude}}
}

func twoKillRoles() []config.RoleConfig {
	return []config.RoleConfig{
		{Name: "planner", Harness: config.HarnessClaude},
		{Name: "coder", Harness: config.HarnessCodex},
	}
}

func killStoppedResponse(childPID int) shim.Response {
	attempted := true
	signal := "SIGHUP"
	exited := true
	return shim.Response{
		Version: shim.ShimProtocolVersion, Outcome: shim.OutcomeStopChildExited,
		ChildPID: &childPID, SignalAttempted: &attempted, Signal: &signal, ChildExitObserved: &exited,
	}
}

func killRetainedResponse(childPID int) shim.Response {
	attempted := true
	signal := "SIGHUP"
	exited := false
	state := string(shim.ProcessPresentMatch)
	return shim.Response{
		Version: shim.ShimProtocolVersion, Outcome: shim.OutcomeStopChildRetained,
		ChildPID: &childPID, SignalAttempted: &attempted, Signal: &signal,
		ChildExitObserved: &exited, State: &state,
	}
}

type killEventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *killEventLog) add(value string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, value)
}

func (l *killEventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

type killRecords struct {
	events *killEventLog
	record fleet.ShimFleetRecord
}

func (r *killRecords) Create(fleet.ShimFleetRecord) error { return errors.New("unexpected create") }
func (r *killRecords) Read(session string) (fleet.ShimFleetRecord, error) {
	r.events.add("record-read:" + session)
	return r.record, nil
}
func (r *killRecords) ReplaceOwned(fleet.ShimFleetRecord, fleet.ShimFleetRecord) error {
	return errors.New("unexpected replace")
}
func (r *killRecords) RemoveOwned(record fleet.ShimFleetRecord) error {
	r.events.add("record-remove:" + record.Session)
	return nil
}

type killInspector struct {
	events       *killEventLog
	observations map[string]fleet.ShimRoleObservation
	sequences    map[string][]fleet.ShimRoleObservation
	calls        map[string]int
}

func (i *killInspector) Inspect(_ context.Context, _, role string) (fleet.ShimRoleObservation, error) {
	i.events.add("inspect:" + role)
	if i.calls == nil {
		i.calls = make(map[string]int)
	}
	i.calls[role]++
	if len(i.sequences[role]) > 0 {
		observation := i.sequences[role][0]
		i.sequences[role] = i.sequences[role][1:]
		return observation, nil
	}
	if i.calls[role] > 1 {
		if initial := i.observations[role]; initial.Outcome == shim.OutcomeRunning || initial.Outcome == shim.OutcomeStarting {
			return fleet.ShimRoleObservation{Outcome: shim.OutcomeMissing}, nil
		}
	}
	return i.observations[role], nil
}
func (i *killInspector) RemoveStale(_ context.Context, _, role string, _ int) (shim.ProcessResult, error) {
	i.events.add("remove-stale:" + role)
	return shim.ProcessResult{Observation: shim.ProcessAbsent}, nil
}

type killLifecycle struct {
	events    *killEventLog
	responses map[string]shim.Response
}

func (*killLifecycle) Observe(context.Context, string, string) (shim.Response, error) {
	return shim.Response{}, errors.New("unexpected observe")
}
func (l *killLifecycle) Stop(_ context.Context, _, role string) (shim.Response, error) {
	l.events.add("stop:" + role)
	return l.responses[role], nil
}

type killPresentation struct {
	events      *killEventLog
	found       *tmuxx.Session
	findResults []killPresentationFindResult
	findIndex   int
	removeErr   error
}

type killPresentationFindResult struct {
	session tmuxx.Session
	present bool
	err     error
}

func (*killPresentation) CreatePresentationSession(context.Context, string, string, string, string) (tmuxx.CreatedSession, error) {
	return tmuxx.CreatedSession{}, errors.New("unexpected presentation creation")
}
func (*killPresentation) CreatePresentationWindow(context.Context, tmuxx.SessionID, string, string, string) (tmuxx.CreatedWindow, error) {
	return tmuxx.CreatedWindow{}, errors.New("unexpected presentation creation")
}
func (*killPresentation) RemovePresentationWindow(context.Context, tmuxx.WindowID) error {
	return errors.New("unexpected window removal")
}
func (p *killPresentation) RemovePresentationSession(_ context.Context, id tmuxx.SessionID) error {
	p.events.add("presentation-remove:" + string(id))
	return p.removeErr
}
func (p *killPresentation) FindPresentationSession(_ context.Context, name string) (tmuxx.Session, bool, error) {
	p.events.add("presentation-find:" + name)
	if p.findIndex < len(p.findResults) {
		result := p.findResults[p.findIndex]
		p.findIndex++
		return result.session, result.present, result.err
	}
	if p.found == nil {
		return tmuxx.Session{}, false, nil
	}
	return *p.found, true, nil
}

var _ fleet.ShimFleetRecords = (*killRecords)(nil)
var _ fleet.ShimRoleInspector = (*killInspector)(nil)
var _ fleet.ShimLifecycle = (*killLifecycle)(nil)
var _ fleet.ShimPresentation = (*killPresentation)(nil)
