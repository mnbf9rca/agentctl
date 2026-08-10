package tmuxx

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// CreatePresentationSession creates optional tmux presentation for the first
// shim role. Runtime identity remains outside tmux; the returned IDs exist only
// for factual observation and rollback of this invocation's own presentation.
func (c Client) CreatePresentationSession(ctx context.Context, name, role, dir, command string) (CreatedSession, error) {
	return c.NewSession(ctx, name, role, dir, command, nil)
}

// CreatePresentationWindow adds optional tmux presentation for a later shim
// role to an already-created presentation session.
func (c Client) CreatePresentationWindow(ctx context.Context, sid SessionID, role, dir, command string) (CreatedWindow, error) {
	return c.NewWindow(ctx, sid, role, dir, command, nil)
}

// RemovePresentationWindow removes only an exact typed window ID selected by
// its caller's prior-state decision.
func (c Client) RemovePresentationWindow(ctx context.Context, wid WindowID) error {
	return c.KillWindow(ctx, wid)
}

// RemovePresentationSession removes only an exact typed session ID selected
// by its caller's prior-state decision.
func (c Client) RemovePresentationSession(ctx context.Context, sid SessionID) error {
	return c.KillSession(ctx, sid)
}

// FindPresentationSession observes optional tmux presentation by exact name.
// Absence is returned as a fact, not as runtime-fleet absence.
func (c Client) FindPresentationSession(ctx context.Context, name string) (Session, bool, error) {
	sessions, err := c.ListSessions(ctx)
	if err != nil {
		if presentationServerAbsent(err) {
			return Session{}, false, nil
		}
		return Session{}, false, err
	}
	for _, session := range sessions {
		if session.Name == name {
			return session, true, nil
		}
	}
	return Session{}, false, nil
}

func presentationServerAbsent(err error) bool {
	message := presentationErrorDiagnostic(err)
	if strings.ContainsAny(message, "\r\n") {
		return false
	}
	if path := strings.TrimPrefix(message, "no server running on "); path != message {
		return path != ""
	}
	const prefix = "error connecting to "
	const suffix = " (No such file or directory)"
	return strings.HasPrefix(message, prefix) && strings.HasSuffix(message, suffix) &&
		len(message) > len(prefix)+len(suffix)
}

func presentationErrorDiagnostic(err error) string {
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && len(exitError.Stderr) != 0 {
		return strings.TrimSuffix(strings.TrimSuffix(string(exitError.Stderr), "\n"), "\r")
	}
	return strings.TrimPrefix(err.Error(), "tmux list sessions: ")
}
