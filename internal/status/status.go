// Package status collects and renders objective fleet status.
package status

// State is one objective tmux or process state for an agent role.
type State string

const (
	StateRunning           State = "running"
	StateDead              State = "dead"
	StateMissing           State = "missing"
	StateNoBaseline        State = "no-baseline"
	StateUnexpectedProcess State = "unexpected-process"
	StateUnmanaged         State = "unmanaged"
	StateAmbiguous         State = "ambiguous"
)

var states = [...]State{
	StateAmbiguous,
	StateUnmanaged,
	StateMissing,
	StateDead,
	StateNoBaseline,
	StateUnexpectedProcess,
	StateRunning,
}

// States returns the complete set of State values the status package can emit.
// The returned slice has independent backing storage.
func States() []State {
	return append([]State(nil), states[:]...)
}

// Report is the versioned status document for one resolved session.
type Report struct {
	Schema  int     `json:"schema"`
	Session string  `json:"session"`
	Managed bool    `json:"managed"`
	Agents  []Agent `json:"agents"`
	Current bool    `json:"current,omitempty"`
	Defect  string  `json:"defect,omitempty"`
	Note    string  `json:"note,omitempty"`
}

// SessionsReport is the versioned status document for every session on the
// tmux server. Each element is itself a complete schema-1 session report.
type SessionsReport struct {
	Schema   int      `json:"schema"`
	Sessions []Report `json:"sessions"`
}

// Agent is one role row in a status report.
type Agent struct {
	Role    string `json:"role"`
	Harness string `json:"harness"`
	Model   string `json:"model"`
	Effort  string `json:"effort"`
	Window  string `json:"window"`
	PaneID  string `json:"pane_id"`
	Process string `json:"process"`
	State   State  `json:"state"`
}

// RuntimeState is the shim-plane factual vocabulary in §15.6. It remains
// beside the legacy tmux State until PR 7 performs the atomic CLI cutover.
type RuntimeState string

const (
	RuntimeStateInvalidRecord              RuntimeState = "invalid-record"
	RuntimeStateRootDisagreement           RuntimeState = "state-root-disagreement"
	RuntimeStateProtocolSkew               RuntimeState = "protocol-skew"
	RuntimeStateAnswererDisagreement       RuntimeState = "answerer-disagreement"
	RuntimeStateCleanupFailed              RuntimeState = "cleanup-failed"
	RuntimeStateConcurrentContender        RuntimeState = "concurrent-contender"
	RuntimeStateStarting                   RuntimeState = "starting"
	RuntimeStateStopping                   RuntimeState = "stopping"
	RuntimeStateStopped                    RuntimeState = "stopped"
	RuntimeStateIndeterminateChildStarting RuntimeState = "indeterminate-child-starting"
	RuntimeStateRunning                    RuntimeState = "running"
	RuntimeStateOrphan                     RuntimeState = "orphan"
	RuntimeStatePresentTokenDisagreement   RuntimeState = "present-token-disagreement"
	RuntimeStatePresentNotOurs             RuntimeState = "present-not-ours"
	RuntimeStateCouldNotObserve            RuntimeState = "could-not-observe"
	RuntimeStateStaleRecord                RuntimeState = "stale-record"
	RuntimeStateMissing                    RuntimeState = "missing"
)

var runtimeStates = [...]RuntimeState{
	RuntimeStateInvalidRecord,
	RuntimeStateRootDisagreement,
	RuntimeStateProtocolSkew,
	RuntimeStateAnswererDisagreement,
	RuntimeStateCleanupFailed,
	RuntimeStateConcurrentContender,
	RuntimeStateStarting,
	RuntimeStateStopping,
	RuntimeStateStopped,
	RuntimeStateIndeterminateChildStarting,
	RuntimeStateRunning,
	RuntimeStateOrphan,
	RuntimeStatePresentTokenDisagreement,
	RuntimeStatePresentNotOurs,
	RuntimeStateCouldNotObserve,
	RuntimeStateStaleRecord,
	RuntimeStateMissing,
}

// RuntimeStates returns the shim-plane states in first-match precedence order.
func RuntimeStates() []RuntimeState {
	return append([]RuntimeState(nil), runtimeStates[:]...)
}

// Confidence describes whether a volatile lockfile anchored the durable join.
type Confidence string

const (
	ConfidenceAnchored   Confidence = "anchored"
	ConfidenceUnanchored Confidence = "unanchored"
)

// PresentationState is an additive tmux observation that never changes a
// runtime role state.
type PresentationState string

const (
	PresentationPresent     PresentationState = "present"
	PresentationGone        PresentationState = "gone"
	PresentationUnavailable PresentationState = "unavailable"
)

// ShimFleetRole is operator-claim provenance from the durable fleet roster.
type ShimFleetRole struct {
	Harness string
	Model   string
	Effort  string
}

// ShimFleetRecord is the status package's cycle-free view of the durable
// roster. PR 7 adapts the fleet store to this narrow compatibility boundary.
type ShimFleetRecord struct {
	Version   int
	Session   string
	Directory string
	Roster    []string
	Roles     map[string]ShimFleetRole
}

// ShimCleanup carries exact durable cleanup-failed facts without making this
// cross-platform status schema depend on Darwin-only shim types.
type ShimCleanup struct {
	Cause       string   `json:"cause"`
	Observation string   `json:"observation"`
	Remaining   []string `json:"remaining"`
}

// ShimAgent joins operator-selected fleet fields with shim-runtime facts.
type ShimAgent struct {
	Role          string       `json:"role"`
	Harness       string       `json:"harness"`
	Model         string       `json:"model"`
	Effort        string       `json:"effort"`
	Directory     string       `json:"directory"`
	Confidence    Confidence   `json:"confidence"`
	ShimPID       int          `json:"shim_pid,omitempty"`
	AnswererPID   int          `json:"answerer_pid,omitempty"`
	RecordShimPID int          `json:"record_shim_pid,omitempty"`
	AdvisoryNonce string       `json:"advisory_nonce,omitempty"`
	RecordNonce   string       `json:"record_nonce,omitempty"`
	LocalRoot     string       `json:"local_root,omitempty"`
	RecordedRoot  string       `json:"recorded_root,omitempty"`
	ChildPID      int          `json:"child_pid,omitempty"`
	Cleanup       *ShimCleanup `json:"cleanup,omitempty"`
	State         RuntimeState `json:"state"`
}

// ShimReport is the runtime-authoritative status document kept beside Report
// until the PR 7 flag day.
type ShimReport struct {
	Schema       int               `json:"schema"`
	Session      string            `json:"session"`
	Presentation PresentationState `json:"presentation"`
	Agents       []ShimAgent       `json:"agents"`
	Note         string            `json:"note,omitempty"`
}
