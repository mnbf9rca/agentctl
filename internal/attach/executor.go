// Package attach validates and attempts iTerm2 tmux control-mode attachment.
package attach

import (
	"context"
	"fmt"
	"os"

	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

// LookupEnv is compatible with os.LookupEnv.
type LookupEnv func(string) (string, bool)

// Client is the tmux surface required to validate and attach a session.
type Client interface {
	ShowSessionOption(context.Context, tmuxx.SessionID, string) (string, error)
	AttachSession(context.Context, tmuxx.SessionID) error
}

// EnvironmentError reports a local terminal environment where iTerm2 control
// mode attachment must not be attempted.
type EnvironmentError struct {
	TermProgram string
	InsideTmux  bool
	Pane        string
}

func (e *EnvironmentError) Error() string {
	if e.InsideTmux {
		return fmt.Sprintf("attach cannot start iTerm2 control mode from inside tmux: TMUX_PANE=%q", e.Pane)
	}
	if e.TermProgram == "" {
		return "attach requires iTerm2: TERM_PROGRAM is unset"
	}
	return fmt.Sprintf("attach requires iTerm2: TERM_PROGRAM=%q", e.TermProgram)
}

// RefusalError reports a session that fails the managed/version ownership gate.
type RefusalError struct {
	Session tmuxx.Session
	Reason  string
}

func (e *RefusalError) Error() string {
	return fmt.Sprintf("session %q %s", e.Session.Name, e.Reason)
}

// Executor validates the terminal environment and one resolved session before
// attempting control-mode attachment.
type Executor struct {
	client    Client
	lookupEnv LookupEnv
}

// New constructs an Executor.
func New(client Client, lookupEnv LookupEnv) Executor {
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	return Executor{client: client, lookupEnv: lookupEnv}
}

// CheckEnvironment refuses before any tmux command unless this process is in
// iTerm2 and outside tmux.
func (e Executor) CheckEnvironment() error {
	termProgram, _ := e.lookupEnv("TERM_PROGRAM")
	if termProgram != "iTerm.app" {
		return &EnvironmentError{TermProgram: termProgram}
	}
	if pane, insideTmux := e.lookupEnv("TMUX_PANE"); insideTmux {
		return &EnvironmentError{TermProgram: termProgram, InsideTmux: true, Pane: pane}
	}
	return nil
}

// Execute requires the current managed/version markers, then attaches by the
// resolved session ID.
func (e Executor) Execute(ctx context.Context, target tmuxx.Session) error {
	managed, err := e.client.ShowSessionOption(ctx, target.ID, "@agentctl_managed")
	if err != nil {
		return tmuxx.ClassifyError(err)
	}
	if managed != "1" {
		return &RefusalError{Session: target, Reason: "is not managed by agentctl"}
	}

	version, err := e.client.ShowSessionOption(ctx, target.ID, "@agentctl_version")
	if err != nil {
		return tmuxx.ClassifyError(err)
	}
	if version != "1" {
		return &RefusalError{Session: target, Reason: "was created by a different agentctl version"}
	}

	if err := e.client.AttachSession(ctx, target.ID); err != nil {
		return tmuxx.ClassifyError(err)
	}
	return nil
}
