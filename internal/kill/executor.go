// Package kill safely terminates an agentctl-managed tmux session.
package kill

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

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
	Reason  string
}

func (e *RefusalError) Error() string {
	return fmt.Sprintf("session %q %s", e.Session.Name, e.Reason)
}

// TmuxError reports a tmux failure while validating or killing a session.
type TmuxError struct {
	Err error
}

func (e *TmuxError) Error() string {
	message := e.Err.Error()
	var exitError *exec.ExitError
	if errors.As(e.Err, &exitError) {
		stderr := strings.TrimRight(string(exitError.Stderr), "\r\n")
		if stderr != "" && !strings.Contains(message, stderr) {
			return message + ": " + stderr
		}
	}
	return message
}

func (e *TmuxError) Unwrap() error {
	return e.Err
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
		return classifyTmuxError(err)
	}
	if managed != "1" {
		return &RefusalError{Session: target, Reason: "is not managed by agentctl"}
	}

	version, err := e.client.ShowSessionOption(ctx, target.ID, "@agentctl_version")
	if err != nil {
		return classifyTmuxError(err)
	}
	if version != "1" {
		return &RefusalError{Session: target, Reason: "was created by a different agentctl version"}
	}

	if err := e.client.KillSession(ctx, target.ID); err != nil {
		return classifyTmuxError(err)
	}
	return nil
}

func classifyTmuxError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return &TmuxError{Err: err}
}
