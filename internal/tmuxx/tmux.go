package tmuxx

import (
	"context"
	"fmt"
	"strings"
)

const (
	sessionFormat        = "#{session_id}\t#{session_name}"
	createdSessionFormat = "#{session_id}\t#{window_id}\t#{pane_id}"
	createdWindowFormat  = "#{window_id}\t#{pane_id}"
)

// SessionID is an exact tmux session ID such as $4.
type SessionID string

// WindowID is an exact tmux window ID such as @7.
type WindowID string

// PaneID is an exact tmux pane ID such as %9.
type PaneID string

// Session identifies one listed tmux session.
type Session struct {
	ID   SessionID
	Name string
}

// CreatedSession contains the IDs tmux prints while creating a session.
type CreatedSession struct {
	SessionID SessionID
	WindowID  WindowID
	PaneID    PaneID
}

// CreatedWindow contains the IDs tmux prints while creating a window.
type CreatedWindow struct {
	WindowID WindowID
	PaneID   PaneID
}

// Client owns agentctl's typed tmux and process operations.
type Client struct {
	runner Runner
}

// New constructs a Client using runner for every external command.
func New(runner Runner) Client {
	return Client{runner: runner}
}

// ListSessions lists every session as a parsed ID/name pair.
func (c Client) ListSessions(ctx context.Context) ([]Session, error) {
	output, err := c.tmuxOutput(ctx, "list sessions", "list-sessions", "-F", sessionFormat)
	if err != nil {
		return nil, err
	}
	records := outputRecords(output)
	if records == nil {
		return nil, nil
	}

	sessions := make([]Session, 0, len(records))
	for index, record := range records {
		fields := strings.SplitN(record, "\t", 2)
		if len(fields) != 2 || fields[1] == "" {
			return nil, fmt.Errorf("parse tmux session record %d: expected nonempty id and name", index+1)
		}
		if err := validateID(fields[0], '$'); err != nil {
			return nil, fmt.Errorf("parse tmux session record %d: %w", index+1, err)
		}
		sessions = append(sessions, Session{ID: SessionID(fields[0]), Name: fields[1]})
	}
	return sessions, nil
}

// NewSession creates the first role and returns the IDs printed by tmux.
func (c Client) NewSession(ctx context.Context, name, role, dir, command string) (CreatedSession, error) {
	output, err := c.tmuxOutput(ctx, "create session",
		"new-session", "-d", "-s", name, "-n", role, "-c", dir,
		"-P", "-F", createdSessionFormat, "--", command,
	)
	if err != nil {
		return CreatedSession{}, err
	}
	fields, err := parseCreationRecord(output, 3)
	if err != nil {
		return CreatedSession{}, fmt.Errorf("parse created session: %w", err)
	}
	if err := validateID(fields[0], '$'); err != nil {
		return CreatedSession{}, fmt.Errorf("parse created session id: %w", err)
	}
	if err := validateID(fields[1], '@'); err != nil {
		return CreatedSession{}, fmt.Errorf("parse created window id: %w", err)
	}
	if err := validateID(fields[2], '%'); err != nil {
		return CreatedSession{}, fmt.Errorf("parse created pane id: %w", err)
	}
	return CreatedSession{
		SessionID: SessionID(fields[0]),
		WindowID:  WindowID(fields[1]),
		PaneID:    PaneID(fields[2]),
	}, nil
}

// NewWindow creates a later role in sid and returns the IDs printed by tmux.
func (c Client) NewWindow(ctx context.Context, sid SessionID, role, dir, command string) (CreatedWindow, error) {
	if err := validateID(string(sid), '$'); err != nil {
		return CreatedWindow{}, fmt.Errorf("new window target: %w", err)
	}
	output, err := c.tmuxOutput(ctx, "create window",
		"new-window", "-d", "-t", string(sid), "-n", role, "-c", dir,
		"-P", "-F", createdWindowFormat, "--", command,
	)
	if err != nil {
		return CreatedWindow{}, err
	}
	fields, err := parseCreationRecord(output, 2)
	if err != nil {
		return CreatedWindow{}, fmt.Errorf("parse created window: %w", err)
	}
	if err := validateID(fields[0], '@'); err != nil {
		return CreatedWindow{}, fmt.Errorf("parse created window id: %w", err)
	}
	if err := validateID(fields[1], '%'); err != nil {
		return CreatedWindow{}, fmt.Errorf("parse created pane id: %w", err)
	}
	return CreatedWindow{WindowID: WindowID(fields[0]), PaneID: PaneID(fields[1])}, nil
}

// SetSessionOption sets one option on an exact session ID.
func (c Client) SetSessionOption(ctx context.Context, sid SessionID, name, value string) error {
	if err := validateID(string(sid), '$'); err != nil {
		return fmt.Errorf("set session option target: %w", err)
	}
	_, err := c.tmuxOutput(ctx, "set session option", "set-option", "-t", string(sid), name, value)
	return err
}

// SetWindowOption sets one option on an exact window ID.
func (c Client) SetWindowOption(ctx context.Context, wid WindowID, name, value string) error {
	if err := validateID(string(wid), '@'); err != nil {
		return fmt.Errorf("set window option target: %w", err)
	}
	_, err := c.tmuxOutput(ctx, "set window option", "set-option", "-w", "-t", string(wid), name, value)
	return err
}

// ShowSessionOption returns one session option value, excluding tmux's line terminator.
func (c Client) ShowSessionOption(ctx context.Context, sid SessionID, name string) (string, error) {
	if err := validateID(string(sid), '$'); err != nil {
		return "", fmt.Errorf("show session option target: %w", err)
	}
	output, err := c.tmuxOutput(ctx, "show session option", "show-options", "-qv", "-t", string(sid), name)
	if err != nil {
		return "", err
	}
	return string(trimOneTrailingNewline(output)), nil
}

// ShowWindowOption returns one window option value, excluding tmux's line terminator.
func (c Client) ShowWindowOption(ctx context.Context, wid WindowID, name string) (string, error) {
	if err := validateID(string(wid), '@'); err != nil {
		return "", fmt.Errorf("show window option target: %w", err)
	}
	output, err := c.tmuxOutput(ctx, "show window option", "show-options", "-wqv", "-t", string(wid), name)
	if err != nil {
		return "", err
	}
	return string(trimOneTrailingNewline(output)), nil
}

// KillSession kills an exact session ID.
func (c Client) KillSession(ctx context.Context, sid SessionID) error {
	if err := validateID(string(sid), '$'); err != nil {
		return fmt.Errorf("kill session target: %w", err)
	}
	_, err := c.tmuxOutput(ctx, "kill session", "kill-session", "-t", string(sid))
	return err
}

// DisplayMessage returns the session name containing an exact pane ID.
func (c Client) DisplayMessage(ctx context.Context, paneID PaneID) (string, error) {
	if err := validateID(string(paneID), '%'); err != nil {
		return "", fmt.Errorf("display message target: %w", err)
	}
	output, err := c.tmuxOutput(ctx, "display current session", "display-message", "-p", "-t", string(paneID), "#{session_name}")
	if err != nil {
		return "", err
	}
	value := string(trimOneTrailingNewline(output))
	if value == "" || strings.Contains(value, "\n") {
		return "", fmt.Errorf("parse displayed session name: expected one nonempty record")
	}
	return value, nil
}

// AttachSession attaches to an exact session ID in tmux control mode.
func (c Client) AttachSession(ctx context.Context, sid SessionID) error {
	if err := validateID(string(sid), '$'); err != nil {
		return fmt.Errorf("attach session target: %w", err)
	}
	_, err := c.tmuxOutput(ctx, "attach session", "-CC", "attach-session", "-t", string(sid))
	return err
}

func (c Client) tmuxOutput(ctx context.Context, operation string, args ...string) ([]byte, error) {
	output, err := c.runner.Output(ctx, "tmux", args...)
	if err != nil {
		return nil, fmt.Errorf("tmux %s: %w", operation, err)
	}
	return output, nil
}

func outputRecords(output []byte) []string {
	trimmed := string(trimOneTrailingNewline(output))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func parseCreationRecord(output []byte, fieldCount int) ([]string, error) {
	record := string(trimOneTrailingNewline(output))
	if record == "" || strings.Contains(record, "\n") {
		return nil, fmt.Errorf("expected exactly one nonempty record")
	}
	fields := strings.Split(record, "\t")
	if len(fields) != fieldCount {
		return nil, fmt.Errorf("expected %d fields, got %d", fieldCount, len(fields))
	}
	for index, field := range fields {
		if field == "" {
			return nil, fmt.Errorf("field %d is empty", index+1)
		}
	}
	return fields, nil
}

func validateID(value string, prefix byte) error {
	if len(value) < 2 || value[0] != prefix {
		return fmt.Errorf("invalid tmux id %q: expected %c followed by digits", value, prefix)
	}
	for _, character := range value[1:] {
		if character < '0' || character > '9' {
			return fmt.Errorf("invalid tmux id %q: expected %c followed by digits", value, prefix)
		}
	}
	return nil
}

func trimOneTrailingNewline(output []byte) []byte {
	if len(output) > 0 && output[len(output)-1] == '\n' {
		return output[:len(output)-1]
	}
	return output
}
