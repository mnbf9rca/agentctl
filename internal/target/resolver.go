// Package target resolves one control target through agentctl's fail-closed
// metadata, pane-state, and process-identity checks.
package target

import (
	"context"
	"errors"
	"os"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

// Client is the read-only tmux surface required to validate a target.
type Client interface {
	ShowSessionOption(context.Context, tmuxx.SessionID, string) (string, error)
	ListWindows(context.Context, tmuxx.SessionID) ([]tmuxx.Window, error)
	ListPanes(context.Context, tmuxx.WindowID) ([]tmuxx.Pane, error)
	ProcessName(context.Context, int) (string, error)
}

// LookupEnv is compatible with os.LookupEnv.
type LookupEnv func(string) (string, bool)

// Resolver validates one already-resolved session and role.
type Resolver struct {
	client    Client
	lookupEnv LookupEnv
}

// New constructs a target resolver. A nil lookup uses the process environment.
func New(client Client, lookupEnv LookupEnv) Resolver {
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	return Resolver{client: client, lookupEnv: lookupEnv}
}

// Resolve returns the exact pane ID only after every safety gate succeeds.
func (r Resolver) Resolve(ctx context.Context, session tmuxx.Session, role string) (tmuxx.PaneID, error) {
	if err := config.ValidateRoleName(role); err != nil {
		return "", err
	}
	managed, err := r.client.ShowSessionOption(ctx, session.ID, "@agentctl_managed")
	if err != nil {
		return "", tmuxx.ClassifyError(err)
	}
	if managed != "1" {
		return "", &SessionMetadataError{Session: session, Option: "@agentctl_managed", Value: managed}
	}
	version, err := r.client.ShowSessionOption(ctx, session.ID, "@agentctl_version")
	if err != nil {
		return "", tmuxx.ClassifyError(err)
	}
	if version != "1" {
		return "", &SessionMetadataError{Session: session, Option: "@agentctl_version", Value: version}
	}
	windows, err := r.client.ListWindows(ctx, session.ID)
	if err != nil {
		return "", tmuxx.ClassifyError(err)
	}
	matches := make([]tmuxx.Window, 0, 1)
	windowIDs := make([]tmuxx.WindowID, 0, 1)
	for _, window := range windows {
		if window.Name == role {
			matches = append(matches, window)
			windowIDs = append(windowIDs, window.ID)
		}
	}
	if len(matches) != 1 {
		return "", &RoleResolutionError{Session: session, Role: role, WindowIDs: windowIDs}
	}
	window := matches[0]
	if window.Role != role {
		return "", &WindowMetadataError{Session: session, Role: role, Window: window}
	}
	panes, err := r.client.ListPanes(ctx, window.ID)
	if err != nil {
		return "", tmuxx.ClassifyError(err)
	}
	if len(panes) != 1 {
		return "", &PaneStateError{Session: session, Role: role, Window: window, Panes: panes}
	}
	pane := panes[0]
	if pane.WindowPanes != 1 {
		return "", &PaneStateError{Session: session, Role: role, Window: window, Panes: panes}
	}
	if pane.Dead {
		return "", &PaneStateError{Session: session, Role: role, Window: window, Panes: panes}
	}
	if window.Process == "" {
		return "", &ProcessIdentityError{Session: session, Role: role, Window: window, Pane: pane}
	}
	actualProcess, err := r.client.ProcessName(ctx, pane.PID)
	if err != nil {
		if errors.Is(err, tmuxx.ErrProcessUnavailable) {
			return "", &ProcessIdentityError{Session: session, Role: role, Window: window, Pane: pane, Err: err}
		}
		return "", tmuxx.ClassifyError(err)
	}
	if actualProcess != window.Process {
		return "", &ProcessIdentityError{
			Session:       session,
			Role:          role,
			Window:        window,
			Pane:          pane,
			ActualProcess: actualProcess,
		}
	}
	if callerPane, set := r.lookupEnv("TMUX_PANE"); set && callerPane == string(pane.ID) {
		return "", &SelfTargetError{
			Session:    session,
			Role:       role,
			Window:     window,
			Pane:       pane,
			CallerPane: tmuxx.PaneID(callerPane),
		}
	}
	return pane.ID, nil
}
