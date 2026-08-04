package tmuxx

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	sessionFormat        = "#{session_id}\t#{session_name}"
	createdSessionFormat = "#{session_id}\t#{window_id}\t#{pane_id}\t#{pane_pid}"
	createdWindowFormat  = "#{window_id}\t#{pane_id}\t#{pane_pid}"
	windowFormat         = "#{window_id}\t#{window_name}\t#{@agentctl_managed}\t#{@agentctl_version}\t#{@agentctl_role}\t#{@agentctl_harness}\t#{@agentctl_model}\t#{@agentctl_process}"
	paneFormat           = "#{pane_id}\t#{pane_pid}\t#{pane_dead}\t#{window_panes}"
)

// ErrCreationOutput reports that a tmux creation command succeeded but its
// output could not be parsed or validated.
var ErrCreationOutput = errors.New("invalid tmux creation output")

// InvalidIDError reports a value that is not a typed tmux ID.
type InvalidIDError struct {
	Value  string
	Prefix byte
}

func (e *InvalidIDError) Error() string {
	return fmt.Sprintf("invalid tmux id %q: expected %c followed by digits", e.Value, e.Prefix)
}

// SessionID is an exact tmux session ID such as $4.
type SessionID string

// WindowID is an exact tmux window ID such as @7.
type WindowID string

// PaneID is an exact tmux pane ID such as %9.
type PaneID string

// EnvVar is one environment variable exported into a window tmux creates. Each
// variable becomes a separate -e argv pair, so no value is ever concatenated
// into a shell-interpreted string.
type EnvVar struct {
	Name  string
	Value string
}

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
	PanePID   int
}

// CreatedWindow contains the IDs tmux prints while creating a window.
type CreatedWindow struct {
	WindowID WindowID
	PaneID   PaneID
	PanePID  int
}

// Window is one parsed window and its agentctl metadata.
type Window struct {
	ID      WindowID
	Name    string
	Managed string
	Version string
	Role    string
	Harness string
	Model   string
	Process string
}

// Pane is one parsed pane and its objective tmux state.
type Pane struct {
	ID          PaneID
	PID         int
	Dead        bool
	WindowPanes int
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

// NewSession creates the first role and returns the IDs printed by tmux. Each
// entry of env is exported into the created window's environment.
func (c Client) NewSession(ctx context.Context, name, role, dir, command string, env []EnvVar) (CreatedSession, error) {
	arguments := []string{"new-session", "-d", "-s", name, "-n", role, "-c", dir}
	arguments = append(arguments, environmentArguments(env)...)
	arguments = append(arguments, "-P", "-F", createdSessionFormat, "--", command)
	output, err := c.tmuxOutput(ctx, "create session", arguments...)
	if err != nil {
		return CreatedSession{}, err
	}
	fields, err := parseCreationRecord(output, 4)
	if err != nil {
		return CreatedSession{}, creationOutputError("parse created session", err)
	}
	if err := validateID(fields[0], '$'); err != nil {
		return CreatedSession{}, creationOutputError("parse created session id", err)
	}
	if err := validateID(fields[1], '@'); err != nil {
		return CreatedSession{}, creationOutputError("parse created window id", err)
	}
	if err := validateID(fields[2], '%'); err != nil {
		return CreatedSession{}, creationOutputError("parse created pane id", err)
	}
	panePID, err := parsePositiveDecimal(fields[3], "pane pid")
	if err != nil {
		return CreatedSession{}, creationOutputError("parse created pane pid", err)
	}
	return CreatedSession{
		SessionID: SessionID(fields[0]),
		WindowID:  WindowID(fields[1]),
		PaneID:    PaneID(fields[2]),
		PanePID:   panePID,
	}, nil
}

// NewWindow creates a later role in sid and returns the IDs printed by tmux.
// Each entry of env is exported into the created window's environment.
func (c Client) NewWindow(ctx context.Context, sid SessionID, role, dir, command string, env []EnvVar) (CreatedWindow, error) {
	if err := validateID(string(sid), '$'); err != nil {
		return CreatedWindow{}, fmt.Errorf("new window target: %w", err)
	}
	arguments := []string{"new-window", "-d", "-t", string(sid), "-n", role, "-c", dir}
	arguments = append(arguments, environmentArguments(env)...)
	arguments = append(arguments, "-P", "-F", createdWindowFormat, "--", command)
	output, err := c.tmuxOutput(ctx, "create window", arguments...)
	if err != nil {
		return CreatedWindow{}, err
	}
	fields, err := parseCreationRecord(output, 3)
	if err != nil {
		return CreatedWindow{}, creationOutputError("parse created window", err)
	}
	if err := validateID(fields[0], '@'); err != nil {
		return CreatedWindow{}, creationOutputError("parse created window id", err)
	}
	if err := validateID(fields[1], '%'); err != nil {
		return CreatedWindow{}, creationOutputError("parse created pane id", err)
	}
	panePID, err := parsePositiveDecimal(fields[2], "pane pid")
	if err != nil {
		return CreatedWindow{}, creationOutputError("parse created pane pid", err)
	}
	return CreatedWindow{WindowID: WindowID(fields[0]), PaneID: PaneID(fields[1]), PanePID: panePID}, nil
}

// environmentArguments renders env as tmux -e argv pairs in declaration order.
func environmentArguments(env []EnvVar) []string {
	if len(env) == 0 {
		return nil
	}
	arguments := make([]string, 0, 2*len(env))
	for _, variable := range env {
		arguments = append(arguments, "-e", variable.Name+"="+variable.Value)
	}
	return arguments
}

func creationOutputError(operation string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrCreationOutput, operation, err)
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

// ListWindows lists windows and agentctl metadata for an exact session ID.
func (c Client) ListWindows(ctx context.Context, sid SessionID) ([]Window, error) {
	if err := validateID(string(sid), '$'); err != nil {
		return nil, fmt.Errorf("list windows target: %w", err)
	}
	output, err := c.tmuxOutput(ctx, "list windows", "list-windows", "-t", string(sid), "-F", windowFormat)
	if err != nil {
		return nil, err
	}
	records := outputRecords(output)
	if records == nil {
		return nil, nil
	}

	windows := make([]Window, 0, len(records))
	for index, record := range records {
		fields := strings.SplitN(record, "\t", 8)
		if len(fields) != 8 || fields[1] == "" {
			return nil, fmt.Errorf("parse tmux window record %d: expected 8 fields and a nonempty name", index+1)
		}
		if err := validateID(fields[0], '@'); err != nil {
			return nil, fmt.Errorf("parse tmux window record %d: %w", index+1, err)
		}
		windows = append(windows, Window{
			ID:      WindowID(fields[0]),
			Name:    fields[1],
			Managed: fields[2],
			Version: fields[3],
			Role:    fields[4],
			Harness: fields[5],
			Model:   fields[6],
			Process: fields[7],
		})
	}
	return windows, nil
}

// ListPanes lists panes and objective state for an exact window ID.
func (c Client) ListPanes(ctx context.Context, wid WindowID) ([]Pane, error) {
	if err := validateID(string(wid), '@'); err != nil {
		return nil, fmt.Errorf("list panes target: %w", err)
	}
	output, err := c.tmuxOutput(ctx, "list panes", "list-panes", "-t", string(wid), "-F", paneFormat)
	if err != nil {
		return nil, err
	}
	records := outputRecords(output)
	if records == nil {
		return nil, nil
	}

	panes := make([]Pane, 0, len(records))
	for index, record := range records {
		fields := strings.Split(record, "\t")
		if len(fields) != 4 {
			return nil, fmt.Errorf("parse tmux pane record %d: expected 4 fields, got %d", index+1, len(fields))
		}
		if err := validateID(fields[0], '%'); err != nil {
			return nil, fmt.Errorf("parse tmux pane record %d: %w", index+1, err)
		}
		pid, err := parsePositiveDecimal(fields[1], "pane pid")
		if err != nil {
			return nil, fmt.Errorf("parse tmux pane record %d: %w", index+1, err)
		}
		var dead bool
		switch fields[2] {
		case "0":
			dead = false
		case "1":
			dead = true
		default:
			return nil, fmt.Errorf("parse tmux pane record %d: invalid pane dead value %q", index+1, fields[2])
		}
		windowPanes, err := parsePositiveDecimal(fields[3], "window pane count")
		if err != nil {
			return nil, fmt.Errorf("parse tmux pane record %d: %w", index+1, err)
		}
		panes = append(panes, Pane{
			ID:          PaneID(fields[0]),
			PID:         pid,
			Dead:        dead,
			WindowPanes: windowPanes,
		})
	}
	return panes, nil
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
	if err := c.runner.RunInteractive(ctx, "tmux", "-CC", "attach-session", "-t", string(sid)); err != nil {
		return fmt.Errorf("tmux attach session: %w", err)
	}
	return nil
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
		return &InvalidIDError{Value: value, Prefix: prefix}
	}
	for _, character := range value[1:] {
		if character < '0' || character > '9' {
			return &InvalidIDError{Value: value, Prefix: prefix}
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

func parsePositiveDecimal(value, field string) (int, error) {
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("invalid %s %q: expected a positive decimal", field, value)
		}
	}
	number, err := strconv.Atoi(value)
	if err != nil || number <= 0 {
		return 0, fmt.Errorf("invalid %s %q: expected a positive decimal", field, value)
	}
	return number, nil
}
