// Package kill safely terminates an agentctl-managed tmux session.
package kill

import (
	"context"
	"fmt"

	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

// Client is the tmux surface required to validate and kill a session.
type Client interface {
	ShowSessionOption(context.Context, tmuxx.SessionID, string) (string, error)
	KillSession(context.Context, tmuxx.SessionID) error
}

// RefusalError reports a session that fails the managed/version ownership gate.
type RefusalError struct {
	Session tmuxx.Session
	Option  string
	Value   string
}

func (e *RefusalError) Error() string {
	if e.Option == "@agentctl_managed" {
		return fmt.Sprintf("session %q is not managed by agentctl", e.Session.Name)
	}
	if e.Value == "" {
		return "managed session carries no @agentctl_version marker"
	}
	return fmt.Sprintf("session %q has @agentctl_version=%q; expected %q", e.Session.Name, e.Value, "1")
}

// Executor validates ownership before terminating one resolved session.
type Executor struct {
	client Client
}

// New constructs an Executor.
func New(client Client) Executor {
	return Executor{client: client}
}

// Execute requires the current managed/version markers, then kills by session ID.
func (e Executor) Execute(ctx context.Context, target tmuxx.Session) error {
	managed, err := e.client.ShowSessionOption(ctx, target.ID, "@agentctl_managed")
	if err != nil {
		return tmuxx.ClassifyError(err)
	}
	if managed != "1" {
		return &RefusalError{Session: target, Option: "@agentctl_managed", Value: managed}
	}

	version, err := e.client.ShowSessionOption(ctx, target.ID, "@agentctl_version")
	if err != nil {
		return tmuxx.ClassifyError(err)
	}
	if version != "1" {
		return &RefusalError{Session: target, Option: "@agentctl_version", Value: version}
	}

	if err := e.client.KillSession(ctx, target.ID); err != nil {
		return tmuxx.ClassifyError(err)
	}
	return nil
}
