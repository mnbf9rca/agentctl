//go:build darwin

package status

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/shim"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestShimCollectorUsesFleetRosterAndApprovedRuntimePrecedence(t *testing.T) {
	t.Parallel()

	states := RuntimeStates()
	roster := make([]string, len(states))
	roles := make(map[string]ShimFleetRole, len(states))
	observations := make(map[string]ShimRoleObservation, len(states))
	for index, state := range states {
		role := "role" + string(rune('a'+index))
		roster[index] = role
		roles[role] = ShimFleetRole{Harness: "codex", Model: "model", Effort: "high"}
		observations[role] = ShimRoleObservation{
			Candidates: []RuntimeState{RuntimeStateMissing, state},
			Confidence: ConfidenceAnchored,
			ShimPID:    100 + index,
			ChildPID:   200 + index,
		}
	}
	source := &fakeShimRoleSource{observations: observations}
	collector := NewShimCollector(
		fakeShimFleetReader{record: ShimFleetRecord{
			Version: 1, Session: "fleet", Directory: "/work", Roster: roster, Roles: roles,
		}},
		source,
		nil,
	)

	got, err := collector.Collect(context.Background(), "fleet")
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if got.Presentation != PresentationGone {
		t.Fatalf("Collect() presentation = %q, want no-tmux gone", got.Presentation)
	}
	if len(got.Agents) != len(states) {
		t.Fatalf("Collect() rows = %d, want %d", len(got.Agents), len(states))
	}
	for index, agent := range got.Agents {
		if agent.Role != roster[index] || agent.State != states[index] {
			t.Fatalf("row %d = %#v, want role %q state %q", index, agent, roster[index], states[index])
		}
		if agent.Confidence != ConfidenceAnchored || agent.Harness != "codex" || agent.Directory != "/work" {
			t.Fatalf("row %d provenance = %#v, want anchored fleet/runtime join", index, agent)
		}
	}
	if !reflect.DeepEqual(source.roles, roster) {
		t.Fatalf("observed role order = %#v, want fleet roster %#v", source.roles, roster)
	}
}

func TestShimCollectorSelectsFirstStateFromCompletePrecedenceCollision(t *testing.T) {
	t.Parallel()

	candidates := RuntimeStates()
	for left, right := 0, len(candidates)-1; left < right; left, right = left+1, right-1 {
		candidates[left], candidates[right] = candidates[right], candidates[left]
	}
	record := ShimFleetRecord{
		Version: 1, Session: "fleet", Directory: "/work", Roster: []string{"planner"},
		Roles: map[string]ShimFleetRole{"planner": {Harness: "claude"}},
	}
	source := &fakeShimRoleSource{observations: map[string]ShimRoleObservation{
		"planner": {Candidates: candidates, Confidence: ConfidenceAnchored},
	}}
	got, err := NewShimCollector(fakeShimFleetReader{record: record}, source, nil).Collect(context.Background(), "fleet")
	if err != nil {
		t.Fatal(err)
	}
	if got.Agents[0].State != RuntimeStateInvalidRecord {
		t.Fatalf("complete precedence collision selected %q, want first-match %q", got.Agents[0].State, RuntimeStateInvalidRecord)
	}
}

func TestShimCollectorPreservesUnanchoredRowsAndDerivedMissing(t *testing.T) {
	t.Parallel()

	fleetRecord := ShimFleetRecord{
		Version: 1, Session: "fleet", Directory: "/work", Roster: []string{"durable", "absent"},
		Roles: map[string]ShimFleetRole{
			"durable": {Harness: "claude"},
			"absent":  {Harness: "codex"},
		},
	}
	source := &fakeShimRoleSource{observations: map[string]ShimRoleObservation{
		"durable": {Candidates: []RuntimeState{RuntimeStateOrphan}, Confidence: ConfidenceUnanchored, ChildPID: 22},
		"absent":  {Candidates: []RuntimeState{RuntimeStateMissing}, Confidence: ConfidenceUnanchored},
	}}

	got, err := NewShimCollector(fakeShimFleetReader{record: fleetRecord}, source, nil).Collect(context.Background(), "fleet")
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	for _, agent := range got.Agents {
		if agent.Confidence != ConfidenceUnanchored {
			t.Fatalf("row %#v silently gained anchored confidence", agent)
		}
	}
}

func TestShimCollectorPresentationIsAdditiveAndKeepsObservedNote(t *testing.T) {
	t.Parallel()

	roleSource := &fakeShimRoleSource{observations: map[string]ShimRoleObservation{
		"planner": {Candidates: []RuntimeState{RuntimeStateRunning}, Confidence: ConfidenceAnchored},
	}}
	presentation := &fakeShimPresentationSource{observation: ShimPresentationObservation{
		State: PresentationPresent,
		Note:  `all 1 roster roles are missing; unmanaged window "joined" has 1 panes`,
	}}
	record := ShimFleetRecord{
		Version: 1, Session: "fleet", Directory: "/work", Roster: []string{"planner"},
		Roles: map[string]ShimFleetRole{"planner": {Harness: "claude"}},
	}
	got, err := NewShimCollector(fakeShimFleetReader{record: record}, roleSource, presentation).Collect(context.Background(), "fleet")
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if got.Agents[0].State != RuntimeStateRunning || got.Presentation != PresentationPresent || got.Note != presentation.observation.Note {
		t.Fatalf("Collect() = %#v, want runtime running plus additive presentation/note", got)
	}

	presentation.err = errors.New("tmux unavailable")
	got, err = NewShimCollector(fakeShimFleetReader{record: record}, roleSource, presentation).Collect(context.Background(), "fleet")
	if err != nil {
		t.Fatalf("Collect() with unavailable tmux error = %v", err)
	}
	if got.Agents[0].State != RuntimeStateRunning || got.Presentation != PresentationUnavailable || got.Note != "" {
		t.Fatalf("Collect() = %#v, presentation failure changed runtime or invented note", got)
	}
}

func TestRuntimeShimRoleSourceChecksAnchorRootBeforeDurableEnumeration(t *testing.T) {
	t.Parallel()

	access := &fakeShimRuntimeAccess{
		localRoot: "/local",
		advisory:  shim.Advisory{Version: 1, ShimPID: 10, Nonce: "nonce", StateRoot: "/recorded"},
		recordErr: errors.New("durable tree must not be read"),
	}
	got, err := newRuntimeShimRoleSource(access, shim.ObserveProcess).ObserveRole(context.Background(), "fleet", "planner")
	if err != nil {
		t.Fatalf("ObserveRole() error = %v", err)
	}
	if !reflect.DeepEqual(got.Candidates, []RuntimeState{RuntimeStateRootDisagreement}) || got.Confidence != ConfidenceAnchored {
		t.Fatalf("ObserveRole() = %#v, want anchored root disagreement", got)
	}
	if !reflect.DeepEqual(access.events, []string{"advisory"}) {
		t.Fatalf("events = %#v, want root comparison before and instead of durable enumeration", access.events)
	}
}

func TestRuntimeShimRoleSourceLabelsNoAnchorDurableAndMissingRowsUnanchored(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		record      shim.Record
		recordErr   error
		process     shim.ProcessResult
		want        RuntimeState
		wantProcess bool
	}{
		{name: "actual durable absence", recordErr: os.ErrNotExist, want: RuntimeStateMissing},
		{
			name: "durable orphan", want: RuntimeStateOrphan, wantProcess: true,
			record:  shim.Record{Version: 1, State: shim.RecordStateChildRecorded, Session: "fleet", Role: "planner", ShimPID: 10, Nonce: "nonce", ChildPID: 20, ChildStartToken: &shim.StartToken{Sec: 1}},
			process: shim.ProcessResult{Observation: shim.ProcessPresentMatch},
		},
		{
			name: "durable stale record", want: RuntimeStateStaleRecord, wantProcess: true,
			record:  shim.Record{Version: 1, State: shim.RecordStateChildRecorded, Session: "fleet", Role: "planner", ShimPID: 10, Nonce: "nonce", ChildPID: 20, ChildStartToken: &shim.StartToken{Sec: 1}},
			process: shim.ProcessResult{Observation: shim.ProcessAbsent},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			access := &fakeShimRuntimeAccess{localRoot: "/local", advisoryErr: os.ErrNotExist, record: test.record, recordErr: test.recordErr}
			processCalls := 0
			source := newRuntimeShimRoleSource(access, func(int, shim.StartToken) shim.ProcessResult {
				processCalls++
				return test.process
			})
			got, err := source.ObserveRole(context.Background(), "fleet", "planner")
			if err != nil {
				t.Fatalf("ObserveRole() error = %v", err)
			}
			if !reflect.DeepEqual(got.Candidates, []RuntimeState{test.want}) || got.Confidence != ConfidenceUnanchored {
				t.Fatalf("ObserveRole() = %#v, want unanchored %q", got, test.want)
			}
			if (processCalls == 1) != test.wantProcess {
				t.Fatalf("process calls = %d, want process=%v", processCalls, test.wantProcess)
			}
		})
	}
}

func TestRuntimeShimRoleSourceClassifiesSocketWithoutLockAsTopologyDisagreement(t *testing.T) {
	t.Parallel()

	access := &fakeShimRuntimeAccess{localRoot: "/local", advisoryErr: os.ErrNotExist, topology: runtimeTopology{SocketPresent: true}}
	got, err := newRuntimeShimRoleSource(access, shim.ObserveProcess).ObserveRole(context.Background(), "fleet", "planner")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Candidates, []RuntimeState{RuntimeStateAnswererDisagreement}) || got.Confidence != ConfidenceUnanchored {
		t.Fatalf("ObserveRole() = %#v, want unanchored socket/claim topology disagreement", got)
	}
	if !reflect.DeepEqual(access.events, []string{"advisory", "topology"}) {
		t.Fatalf("events = %#v, want no durable inference through conflicting socket", access.events)
	}
}

func TestRuntimeShimRoleSourceMapsLiveResponsesAndAnswererFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		response   shim.Response
		observeErr error
		want       RuntimeState
	}{
		{name: "running", response: shim.Response{Version: 1, Outcome: shim.OutcomeRunning}, want: RuntimeStateRunning},
		{name: "starting", response: shim.Response{Version: 1, Outcome: shim.OutcomeStarting}, want: RuntimeStateStarting},
		{name: "stopping", response: shim.Response{Version: 1, Outcome: shim.OutcomeStopping}, want: RuntimeStateStopping},
		{name: "stopped", response: shim.Response{Version: 1, Outcome: shim.OutcomeStopped}, want: RuntimeStateStopped},
		{name: "cleanup", response: shim.Response{Version: 1, Outcome: shim.OutcomeCleanupFailed}, want: RuntimeStateCleanupFailed},
		{name: "contender", response: shim.Response{Version: 1, Outcome: shim.OutcomeConcurrentContender}, want: RuntimeStateConcurrentContender},
		{name: "protocol skew", observeErr: &shim.ProtocolSkewError{Kind: shim.ProtocolSkewForeign, Token: "2"}, want: RuntimeStateProtocolSkew},
		{name: "answerer disagreement", observeErr: &shim.AnswererDisagreementError{RecordedPID: 10, AnswererPID: 11}, want: RuntimeStateAnswererDisagreement},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			access := &fakeShimRuntimeAccess{
				localRoot: "/local", advisory: shim.Advisory{Version: 1, ShimPID: 10, Nonce: "nonce", StateRoot: "/local"},
				topology: runtimeTopology{SocketPresent: true, Claim: shim.ClaimObservation{Held: true, ConflictPID: -1}},
				response: test.response, observeErr: test.observeErr,
			}
			got, err := newRuntimeShimRoleSource(access, shim.ObserveProcess).ObserveRole(context.Background(), "fleet", "planner")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Candidates, []RuntimeState{test.want}) || got.Confidence != ConfidenceAnchored {
				t.Fatalf("ObserveRole() = %#v, want anchored %q", got, test.want)
			}
			if !reflect.DeepEqual(access.events, []string{"advisory", "topology", "observe"}) {
				t.Fatalf("events = %#v, want runtime observation before durable fallback", access.events)
			}
		})
	}
}

func TestRuntimeShimRoleSourceUsesDurableChildOnlyAfterUnheldRuntimeTopology(t *testing.T) {
	t.Parallel()

	token := shim.StartToken{Sec: 1}
	for _, test := range []struct {
		name    string
		record  shim.Record
		process shim.ProcessResult
		want    RuntimeState
	}{
		{name: "child starting", record: shim.Record{Version: 1, State: shim.RecordStateChildStarting, Session: "fleet", Role: "planner", ShimPID: 10, Nonce: "nonce"}, want: RuntimeStateIndeterminateChildStarting},
		{name: "orphan", record: shim.Record{Version: 1, State: shim.RecordStateChildRecorded, Session: "fleet", Role: "planner", ShimPID: 10, Nonce: "nonce", ChildPID: 20, ChildStartToken: &token}, process: shim.ProcessResult{Observation: shim.ProcessPresentMatch}, want: RuntimeStateOrphan},
		{name: "token disagreement", record: shim.Record{Version: 1, State: shim.RecordStateChildRecorded, Session: "fleet", Role: "planner", ShimPID: 10, Nonce: "nonce", ChildPID: 20, ChildStartToken: &token}, process: shim.ProcessResult{Observation: shim.ProcessPresentTokenDisagreement}, want: RuntimeStatePresentTokenDisagreement},
		{name: "not ours", record: shim.Record{Version: 1, State: shim.RecordStateChildRecorded, Session: "fleet", Role: "planner", ShimPID: 10, Nonce: "nonce", ChildPID: 20, ChildStartToken: &token}, process: shim.ProcessResult{Observation: shim.ProcessPresentNotOurs}, want: RuntimeStatePresentNotOurs},
		{name: "could not observe", record: shim.Record{Version: 1, State: shim.RecordStateChildRecorded, Session: "fleet", Role: "planner", ShimPID: 10, Nonce: "nonce", ChildPID: 20, ChildStartToken: &token}, process: shim.ProcessResult{Observation: shim.ProcessCouldNotObserve}, want: RuntimeStateCouldNotObserve},
		{name: "stale", record: shim.Record{Version: 1, State: shim.RecordStateChildRecorded, Session: "fleet", Role: "planner", ShimPID: 10, Nonce: "nonce", ChildPID: 20, ChildStartToken: &token}, process: shim.ProcessResult{Observation: shim.ProcessAbsent}, want: RuntimeStateStaleRecord},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			access := &fakeShimRuntimeAccess{
				localRoot: "/local", advisory: shim.Advisory{Version: 1, ShimPID: 10, Nonce: "nonce", StateRoot: "/local"},
				record: test.record,
			}
			source := newRuntimeShimRoleSource(access, func(int, shim.StartToken) shim.ProcessResult { return test.process })
			got, err := source.ObserveRole(context.Background(), "fleet", "planner")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Candidates, []RuntimeState{test.want}) || got.Confidence != ConfidenceAnchored {
				t.Fatalf("ObserveRole() = %#v, want anchored %q", got, test.want)
			}
			if !reflect.DeepEqual(access.events, []string{"advisory", "topology", "record"}) {
				t.Fatalf("events = %#v, want runtime before durable", access.events)
			}
		})
	}
}

func TestRuntimeShimRoleSourceReportsLiveHeldClaimWithoutSocketAsStarting(t *testing.T) {
	t.Parallel()

	access := &fakeShimRuntimeAccess{
		localRoot: "/local",
		advisory:  shim.Advisory{Version: 1, ShimPID: 10, Nonce: "nonce", StateRoot: "/local"},
		topology:  runtimeTopology{Claim: shim.ClaimObservation{Held: true, ConflictPID: -1}},
		recordErr: os.ErrNotExist,
	}
	got, err := newRuntimeShimRoleSource(access, shim.ObserveProcess).ObserveRole(context.Background(), "fleet", "planner")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Candidates, []RuntimeState{RuntimeStateStarting}) || got.Confidence != ConfidenceAnchored {
		t.Fatalf("ObserveRole() = %#v, want held runtime claim reported as anchored starting", got)
	}
	if !reflect.DeepEqual(access.events, []string{"advisory", "topology", "record"}) {
		t.Fatalf("events = %#v, want claim topology before durable startup evidence", access.events)
	}
}

func TestRuntimeShimRoleSourceDistinguishesMalformedEvidenceFromObservationFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		access *fakeShimRuntimeAccess
		want   RuntimeState
	}{
		{name: "malformed advisory", access: &fakeShimRuntimeAccess{advisoryErr: &shim.AdvisoryParseError{Cause: "duplicate field"}}, want: RuntimeStateInvalidRecord},
		{name: "inaccessible advisory", access: &fakeShimRuntimeAccess{advisoryErr: &shim.FilesystemObservationError{Kind: "lock", Operation: "read", Err: os.ErrPermission}}, want: RuntimeStateCouldNotObserve},
		{name: "substituted runtime", access: &fakeShimRuntimeAccess{advisoryErr: &shim.RootSubstitutedError{Kind: "runtime", Path: "/runtime"}}, want: RuntimeStateCouldNotObserve},
		{
			name: "non-socket artifact", want: RuntimeStateAnswererDisagreement,
			access: &fakeShimRuntimeAccess{localRoot: "/local", advisory: shim.Advisory{Version: 1, ShimPID: 10, Nonce: "nonce", StateRoot: "/local"}, topologyErr: &shim.SocketTopologyError{Path: "/runtime/fleet/planner.sock", Reason: "non-socket"}},
		},
		{
			name: "durable filesystem failure", want: RuntimeStateCouldNotObserve,
			access: &fakeShimRuntimeAccess{localRoot: "/local", advisory: shim.Advisory{Version: 1, ShimPID: 10, Nonce: "nonce", StateRoot: "/local"}, recordErr: os.ErrPermission},
		},
		{
			name: "malformed durable record", want: RuntimeStateInvalidRecord,
			access: &fakeShimRuntimeAccess{localRoot: "/local", advisory: shim.Advisory{Version: 1, ShimPID: 10, Nonce: "nonce", StateRoot: "/local"}, recordErr: &shim.RecordParseError{Cause: "unknown state"}},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := newRuntimeShimRoleSource(test.access, shim.ObserveProcess).ObserveRole(context.Background(), "fleet", "planner")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Candidates, []RuntimeState{test.want}) {
				t.Fatalf("ObserveRole() = %#v, want %q", got, test.want)
			}
		})
	}
}

func TestRuntimeShimRoleSourceReportsDurableCleanupFailureAndDisagreementFacts(t *testing.T) {
	t.Parallel()

	cleanup, err := newShimCleanupTestRecord()
	if err != nil {
		t.Fatal(err)
	}
	access := &fakeShimRuntimeAccess{localRoot: "/local", advisory: shim.Advisory{Version: 1, ShimPID: 10, Nonce: "nonce", StateRoot: "/local"}, record: cleanup}
	got, err := newRuntimeShimRoleSource(access, shim.ObserveProcess).ObserveRole(context.Background(), "fleet", "planner")
	if err != nil {
		t.Fatal(err)
	}
	wantCleanup := &ShimCleanup{Cause: "cleanup retained child", Observation: "present-match", Remaining: []string{"child"}}
	if !reflect.DeepEqual(got.Candidates, []RuntimeState{RuntimeStateCleanupFailed}) || got.ChildPID != 20 || !reflect.DeepEqual(got.Cleanup, wantCleanup) {
		t.Fatalf("cleanup ObserveRole() = %#v", got)
	}

	access = &fakeShimRuntimeAccess{localRoot: "/local", advisory: shim.Advisory{Version: 1, ShimPID: 10, Nonce: "nonce", StateRoot: "/local"}, record: shim.Record{Version: 1, State: shim.RecordStateChildStarting, Session: "fleet", Role: "planner", ShimPID: 11, Nonce: "other"}}
	got, err = newRuntimeShimRoleSource(access, shim.ObserveProcess).ObserveRole(context.Background(), "fleet", "planner")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Candidates, []RuntimeState{RuntimeStateAnswererDisagreement}) || got.ShimPID != 10 || got.RecordShimPID != 11 || got.AdvisoryNonce != "nonce" || got.RecordNonce != "other" {
		t.Fatalf("disagreement ObserveRole() = %#v, want both advisory and record shim PIDs", got)
	}
}

func newShimCleanupTestRecord() (shim.Record, error) {
	base := shim.NewChildStartingRecord("fleet", "planner", 10, "nonce")
	return base.WithCleanupFailure(20, nil, shim.CleanupFailure{Cause: "cleanup retained child", Observation: shim.CleanupObservationPresentMatch, Remaining: []string{"child"}})
}

func TestRuntimePresentationSourceKeepsTmuxAdditiveAndEmitsOnlyExactAggregateNote(t *testing.T) {
	t.Parallel()

	client := &fakeRuntimePresentationClient{
		session: tmuxx.Session{ID: "$4", Name: "fleet"}, present: true,
		windows: []tmuxx.Window{{ID: "@12", Name: "joined", Role: ""}},
		panes:   []tmuxx.Pane{{ID: "%1"}, {ID: "%2"}},
	}
	agents := []ShimAgent{
		{Role: "planner", State: RuntimeStateMissing},
		{Role: "worker", State: RuntimeStateMissing},
	}
	got, err := NewRuntimePresentationSource(client).ObservePresentation(context.Background(), "fleet", []string{"planner", "worker"}, agents)
	if err != nil {
		t.Fatalf("ObservePresentation() error = %v", err)
	}
	wantNote := `all 2 roster roles are missing; unmanaged window "joined" has 2 panes`
	if got.State != PresentationPresent || got.Note != wantNote {
		t.Fatalf("ObservePresentation() = %#v, want present and exact objective note %q", got, wantNote)
	}

	client.panes = client.panes[:1]
	got, err = NewRuntimePresentationSource(client).ObservePresentation(context.Background(), "fleet", []string{"planner", "worker"}, agents)
	if err != nil || got.Note != "" || got.State != PresentationPresent {
		t.Fatalf("below-threshold ObservePresentation() = %#v, %v, want present without note", got, err)
	}

	client.present = false
	got, err = NewRuntimePresentationSource(client).ObservePresentation(context.Background(), "fleet", []string{"planner", "worker"}, agents)
	if err != nil || got.State != PresentationGone || got.Note != "" {
		t.Fatalf("gone ObservePresentation() = %#v, %v, want presentation gone only", got, err)
	}
}

func TestRuntimePresentationSourceSuppressesOnlyAWindowActuallyMatchedByRosterName(t *testing.T) {
	t.Parallel()

	agents := []ShimAgent{{Role: "planner", State: RuntimeStateMissing}}
	client := &fakeRuntimePresentationClient{
		session: tmuxx.Session{ID: "$4", Name: "fleet"}, present: true,
		windows: []tmuxx.Window{{ID: "@12", Name: "planner"}},
		panes:   []tmuxx.Pane{{ID: "%1"}},
	}
	got, err := NewRuntimePresentationSource(client).ObservePresentation(context.Background(), "fleet", []string{"planner"}, agents)
	if err != nil || got.Note != "" {
		t.Fatalf("roster-named window = %#v, %v, want aggregate note suppressed", got, err)
	}

	client.windows = []tmuxx.Window{
		{ID: "@11", Name: "stale", Role: "planner"},
		{ID: "@12", Name: "joined"},
	}
	got, err = NewRuntimePresentationSource(client).ObservePresentation(context.Background(), "fleet", []string{"planner"}, agents)
	if err != nil {
		t.Fatal(err)
	}
	want := `all 1 roster roles are missing; unmanaged window "joined" has 1 panes`
	if got.Note != want {
		t.Fatalf("stale role metadata note = %q, want factual name-based note %q", got.Note, want)
	}
}

type fakeShimFleetReader struct {
	record ShimFleetRecord
	err    error
}

func (f fakeShimFleetReader) Read(string) (ShimFleetRecord, error) { return f.record, f.err }

type fakeShimRoleSource struct {
	observations map[string]ShimRoleObservation
	roles        []string
}

func (f *fakeShimRoleSource) ObserveRole(_ context.Context, _, role string) (ShimRoleObservation, error) {
	f.roles = append(f.roles, role)
	return f.observations[role], nil
}

type fakeShimPresentationSource struct {
	observation ShimPresentationObservation
	err         error
}

func (f *fakeShimPresentationSource) ObservePresentation(context.Context, string, []string, []ShimAgent) (ShimPresentationObservation, error) {
	return f.observation, f.err
}

type fakeShimRuntimeAccess struct {
	localRoot   string
	advisory    shim.Advisory
	advisoryErr error
	topology    runtimeTopology
	topologyErr error
	record      shim.Record
	recordErr   error
	response    shim.Response
	observeErr  error
	events      []string
}

func (f *fakeShimRuntimeAccess) LocalStateRoot() string { return f.localRoot }

func (f *fakeShimRuntimeAccess) ReadAdvisory(string, string) (shim.Advisory, error) {
	f.events = append(f.events, "advisory")
	return f.advisory, f.advisoryErr
}

func (f *fakeShimRuntimeAccess) ObserveTopology(string, string) (runtimeTopology, error) {
	f.events = append(f.events, "topology")
	return f.topology, f.topologyErr
}

func (f *fakeShimRuntimeAccess) ReadRecord(string, string) (shim.Record, error) {
	f.events = append(f.events, "record")
	return f.record, f.recordErr
}

func (f *fakeShimRuntimeAccess) Observe(context.Context, string, string) (shim.Response, error) {
	f.events = append(f.events, "observe")
	return f.response, f.observeErr
}

type fakeRuntimePresentationClient struct {
	session   tmuxx.Session
	present   bool
	findErr   error
	windows   []tmuxx.Window
	windowErr error
	panes     []tmuxx.Pane
	paneErr   error
}

func (f *fakeRuntimePresentationClient) FindPresentationSession(context.Context, string) (tmuxx.Session, bool, error) {
	return f.session, f.present, f.findErr
}

func (f *fakeRuntimePresentationClient) ListWindows(context.Context, tmuxx.SessionID) ([]tmuxx.Window, error) {
	return append([]tmuxx.Window(nil), f.windows...), f.windowErr
}

func (f *fakeRuntimePresentationClient) ListPanes(context.Context, tmuxx.WindowID) ([]tmuxx.Pane, error) {
	return append([]tmuxx.Pane(nil), f.panes...), f.paneErr
}
