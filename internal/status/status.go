// Package status collects and renders objective fleet status.
package status

// State is one objective tmux or process state for an agent role.
type State string

const (
	StateRunning           State = "running"
	StateDead              State = "dead"
	StateMissing           State = "missing"
	StateUnexpectedProcess State = "unexpected-process"
	StateUnmanaged         State = "unmanaged"
	StateAmbiguous         State = "ambiguous"
)

// Report is the versioned status document for one resolved session.
type Report struct {
	Schema  int     `json:"schema"`
	Session string  `json:"session"`
	Managed bool    `json:"managed"`
	Agents  []Agent `json:"agents"`
}

// Agent is one role row in a status report.
type Agent struct {
	Role    string `json:"role"`
	Harness string `json:"harness"`
	Model   string `json:"model"`
	Window  string `json:"window"`
	PaneID  string `json:"pane_id"`
	Process string `json:"process"`
	State   State  `json:"state"`
}
