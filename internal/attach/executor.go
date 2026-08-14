// Package attach validates and attempts iTerm2 tmux control-mode attachment.
package attach

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/mnbf9rca/agentctl/internal/buildinfo"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

// LookupEnv is compatible with os.LookupEnv.
type LookupEnv func(string) (string, bool)

// Client is the tmux surface required to validate, attach, and afterwards
// observe a session.
type Client interface {
	ShowSessionOption(context.Context, tmuxx.SessionID, string) (string, error)
	ListWindows(context.Context, tmuxx.SessionID) ([]tmuxx.Window, error)
	AttachSession(context.Context, tmuxx.SessionID) error
	ListSessions(context.Context) ([]tmuxx.Session, error)
	FindPresentationSession(context.Context, string) (tmuxx.Session, bool, error)
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

// NoPresentationError is the factual attach-only limitation: runtime control
// remains available, but no tmux UI was observed for this session.
type NoPresentationError struct {
	Session string
	Roster  []string
}

func (e *NoPresentationError) Error() string {
	return fmt.Sprintf("refusing to attach session %q; no tmux presentation was observed", e.Session)
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
// resolved session ID. The narration is written to out only once the ownership
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

	windowCount := -1
	if windows, countErr := e.client.ListWindows(ctx, target.ID); countErr == nil {
		windowCount = len(windows)
	}
	writeNarration(out, target, windowCount)

	if err := e.client.AttachSession(ctx, target.ID); err != nil {
		return tmuxx.ClassifyError(err)
	}
	return nil
}

// ExecutePresentation attaches only an exact tmux presentation observed by
// name. Legacy tmux metadata is not runtime ownership evidence and is not read.
func (e Executor) ExecutePresentation(ctx context.Context, session string, out io.Writer) (tmuxx.Session, error) {
	target, present, err := e.client.FindPresentationSession(ctx, session)
	if err != nil {
		return tmuxx.Session{}, tmuxx.ClassifyError(err)
	}
	if !present {
		return tmuxx.Session{}, &NoPresentationError{Session: session}
	}
	windowCount := -1
	if windows, countErr := e.client.ListWindows(ctx, target.ID); countErr == nil {
		windowCount = len(windows)
	}
	writeNarration(out, target, windowCount)
	if err := e.client.AttachSession(ctx, target.ID); err != nil {
		return tmuxx.Session{}, tmuxx.ClassifyError(err)
	}
	return target, nil
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

// writeNarration states what iTerm2 is about to do and what its command-menu
// keys mean. A negative windowCount means the advisory count read failed, so
// the count is omitted rather than guessed.
func writeNarration(out io.Writer, target tmuxx.Session, windowCount int) {
	if out == nil {
		return
	}
	fmt.Fprintf(out, "agentctl %s\n", buildinfo.Current())
	if windowCount >= 0 {
		label := "windows"
		if windowCount == 1 {
			label = "window"
		}
		fmt.Fprintf(out, "Attaching session %q (%d %s) in iTerm2.\n\n", target.Name, windowCount, label)
	} else {
		fmt.Fprintf(out, "Attaching session %q in iTerm2.\n\n", target.Name)
	}
	fmt.Fprint(out, "iTerm2 will now show its Command Menu. That menu is iTerm2's, not agentctl's:\n\n")
	fmt.Fprint(out, "  esc   detach cleanly — the tabs close and the fleet keeps running\n")
	fmt.Fprint(out, "  X     (uppercase) force-quit — the fleet keeps running, but the tmux client\n")
	fmt.Fprint(out, "        does not exit, so this terminal stays busy and agentctl cannot report.\n")
	fmt.Fprint(out, "        Prefer esc.\n\n")
	fmt.Fprintf(out, "Detaching never stops the fleet. To stop it: agentctl kill --session %s\n", target.Name)
}
