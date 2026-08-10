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
