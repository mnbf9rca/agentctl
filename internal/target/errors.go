package target

import (
	"fmt"
	"strings"

	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

// SessionMetadataError reports the first session ownership marker that was
// observed with a value other than the required invariant "1".
type SessionMetadataError struct {
	Session tmuxx.Session
	Option  string
	Value   string
}

func (e *SessionMetadataError) Error() string {
	return fmt.Sprintf("session %q has %s=%q; expected %q", e.Session.Name, e.Option, e.Value, "1")
}

// RoleResolutionError reports an exact role name that matched other than one
// window in the resolved session.
type RoleResolutionError struct {
	Session   tmuxx.Session
	Role      string
	WindowIDs []tmuxx.WindowID
}

func (e *RoleResolutionError) Error() string {
	if len(e.WindowIDs) == 0 {
		return fmt.Sprintf("role %q matches no windows in session %q", e.Role, e.Session.Name)
	}
	ids := make([]string, len(e.WindowIDs))
	for index, id := range e.WindowIDs {
		ids[index] = string(id)
	}
	return fmt.Sprintf("role %q matches %d windows in session %q (%s)", e.Role, len(ids), e.Session.Name, strings.Join(ids, ", "))
}

// WindowMetadataError reports the selected window row whose managed marker or
// stored role does not match the requested target.
type WindowMetadataError struct {
	Session tmuxx.Session
	Role    string
	Window  tmuxx.Window
}

func (e *WindowMetadataError) Error() string {
	if e.Window.Managed != "1" {
		return fmt.Sprintf("window %s for role %q has @agentctl_managed=%q; expected %q", e.Window.ID, e.Role, e.Window.Managed, "1")
	}
	return fmt.Sprintf("window %s named %q has stored role %q; expected %q", e.Window.ID, e.Window.Name, e.Window.Role, e.Role)
}

// PaneStateError reports observed pane state that is unsafe to control.
type PaneStateError struct {
	Session tmuxx.Session
	Role    string
	Window  tmuxx.Window
	Panes   []tmuxx.Pane
}

func (e *PaneStateError) Error() string {
	if len(e.Panes) != 1 {
		return fmt.Sprintf("window %s for role %q contains %d panes; expected 1", e.Window.ID, e.Role, len(e.Panes))
	}
	pane := e.Panes[0]
	if pane.WindowPanes != 1 {
		return fmt.Sprintf("pane %s reports %d panes in window %s; expected 1", pane.ID, pane.WindowPanes, e.Window.ID)
	}
	return fmt.Sprintf("pane %s for role %q is dead", pane.ID, e.Role)
}

// ProcessIdentityError reports an empty launch baseline, an unavailable
// current identity, or an exact mismatch between the two observed values.
type ProcessIdentityError struct {
	Session       tmuxx.Session
	Role          string
	Window        tmuxx.Window
	Pane          tmuxx.Pane
	ActualProcess string
	Err           error
}

func (e *ProcessIdentityError) Error() string {
	if e.Window.Process == "" {
		return fmt.Sprintf("window %s for role %q has an empty @agentctl_process baseline", e.Window.ID, e.Role)
	}
	if e.Err != nil {
		return fmt.Sprintf("process identity for pane %s is unavailable: %v", e.Pane.ID, e.Err)
	}
	return fmt.Sprintf("pane %s has process %q; recorded process is %q", e.Pane.ID, e.ActualProcess, e.Window.Process)
}

// Unwrap exposes ErrProcessUnavailable without changing this error's unsafe
// target classification.
func (e *ProcessIdentityError) Unwrap() error {
	return e.Err
}

// SelfTargetError reports a validated target pane that is also the calling
// pane named by TMUX_PANE.
type SelfTargetError struct {
	Session    tmuxx.Session
	Role       string
	Window     tmuxx.Window
	Pane       tmuxx.Pane
	CallerPane tmuxx.PaneID
}

func (e *SelfTargetError) Error() string {
	return fmt.Sprintf("pane %s for role %q is the calling pane %s", e.Pane.ID, e.Role, e.CallerPane)
}
