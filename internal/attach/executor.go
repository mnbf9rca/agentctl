// Package attach validates and attempts iTerm2 tmux control-mode attachment.
package attach

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

// LookupEnv is compatible with os.LookupEnv.
type LookupEnv func(string) (string, bool)

// Client is the tmux surface required to validate, attach, and afterwards
// observe a session.
type Client interface {
	ShowSessionOption(context.Context, tmuxx.SessionID, string) (string, error)
	AttachSession(context.Context, tmuxx.SessionID) error
	ListSessions(context.Context) ([]tmuxx.Session, error)
}

// EnvironmentError reports a local terminal environment where iTerm2 control
// mode attachment must not be attempted.
type EnvironmentError struct {
	TermProgram    string
	TermProgramSet bool
	InsideTmux     bool
	Pane           string
}

func (e *EnvironmentError) Error() string {
	if e.InsideTmux {
		return fmt.Sprintf("attach cannot start iTerm2 control mode from inside tmux: TMUX_PANE=%q", e.Pane)
	}
	if !e.TermProgramSet {
		return "attach requires iTerm2: TERM_PROGRAM is unset"
	}
	return fmt.Sprintf("attach requires iTerm2: TERM_PROGRAM=%q", e.TermProgram)
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
	termProgram, termProgramSet := e.lookupEnv("TERM_PROGRAM")
	if termProgram != "iTerm.app" {
		return &EnvironmentError{TermProgram: termProgram, TermProgramSet: termProgramSet}
	}
	if pane, insideTmux := e.lookupEnv("TMUX_PANE"); insideTmux {
		return &EnvironmentError{TermProgram: termProgram, TermProgramSet: termProgramSet, InsideTmux: true, Pane: pane}
	}
	return nil
}

// Execute requires the current managed/version markers, then attaches by the
// resolved session ID. The notice is written to out only once the ownership
// gate has passed and immediately before control mode starts, so it never
// announces an attachment a refusal prevented.
func (e Executor) Execute(ctx context.Context, target tmuxx.Session, out io.Writer) error {
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

	writeNotice(out, target)

	if err := e.client.AttachSession(ctx, target.ID); err != nil {
		return tmuxx.ClassifyError(err)
	}
	return nil
}

// StillRunning reports whether the attached session is still present once
// control mode has ended. It compares the resolved session ID, so a session
// recreated under the same name is not reported as the one just attached. The
// probe is advisory: its own failure is returned rather than rendered as an
// absence, so the caller can state what it could not verify (§1.1).
func (e Executor) StillRunning(ctx context.Context, target tmuxx.Session) (bool, error) {
	sessions, err := e.client.ListSessions(ctx)
	if err != nil {
		return false, tmuxx.ClassifyError(err)
	}
	for _, candidate := range sessions {
		if candidate.ID == target.ID {
			return true, nil
		}
	}
	return false, nil
}

// writeNotice states what the keys in the iTerm2 command menu that follows do,
// and which of them agentctl owns: none. Every claim here is about agentctl or
// about a clean tmux detach; what actually became of the session is reported
// from observation once control mode ends.
func writeNotice(out io.Writer, target tmuxx.Session) {
	if out == nil {
		return
	}
	fmt.Fprintf(out, "agentctl: attaching session %q (%s) in iTerm2 tmux control mode; the command menu printed next comes from iTerm2, not from agentctl.\n", target.Name, target.ID)
	fmt.Fprint(out, "agentctl: in that menu, esc detaches and leaves the fleet running; X (uppercase) force-quits iTerm2's tmux mode without a clean detach.\n")
	fmt.Fprintf(out, "agentctl: agentctl never stops a fleet on detach; only agentctl kill --session %s does.\n", target.Name)
}
