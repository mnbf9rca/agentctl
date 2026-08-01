package status

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

type tmuxClient interface {
	ShowSessionOption(context.Context, tmuxx.SessionID, string) (string, error)
	ListWindows(context.Context, tmuxx.SessionID) ([]tmuxx.Window, error)
	ListPanes(context.Context, tmuxx.WindowID) ([]tmuxx.Pane, error)
	ProcessName(context.Context, int) (string, error)
}

// VersionError reports a managed session whose metadata belongs to another
// agentctl version.
type VersionError struct {
	Session string
	Version string
}

// TmuxError reports a tmux or process-runner failure during status collection.
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

func (e *VersionError) Error() string {
	return fmt.Sprintf("session %q was created by a different agentctl version %q", e.Session, e.Version)
}

// Collector gathers one objective snapshot using the typed tmux boundary.
type Collector struct {
	client tmuxClient
}

// NewCollector constructs a status collector.
func NewCollector(client tmuxClient) Collector {
	return Collector{client: client}
}

// Collect gathers status for one already-resolved session ID.
func (c Collector) Collect(ctx context.Context, sessionName string, sessionID tmuxx.SessionID) (Report, error) {
	report := Report{Schema: 1, Session: sessionName, Managed: true, Agents: []Agent{}}

	managed, err := c.client.ShowSessionOption(ctx, sessionID, "@agentctl_managed")
	if err != nil {
		return Report{}, classifyTmuxError(err)
	}
	version, err := c.client.ShowSessionOption(ctx, sessionID, "@agentctl_version")
	if err != nil {
		return Report{}, classifyTmuxError(err)
	}
	if version != "" && version != "1" {
		return Report{}, &VersionError{Session: sessionName, Version: version}
	}
	if managed != "1" {
		report.Managed = false
		return report, nil
	}
	if version != "1" {
		return Report{}, &VersionError{Session: sessionName, Version: version}
	}
	roster, err := c.client.ShowSessionOption(ctx, sessionID, "@agentctl_roles")
	if err != nil {
		return Report{}, classifyTmuxError(err)
	}
	if roster == "" {
		return Report{}, fmt.Errorf("session %q has empty @agentctl_roles metadata", sessionName)
	}
	windows, err := c.client.ListWindows(ctx, sessionID)
	if err != nil {
		return Report{}, classifyTmuxError(err)
	}

	windowsByName := make(map[string][]tmuxx.Window, len(windows))
	for _, window := range windows {
		windowsByName[window.Name] = append(windowsByName[window.Name], window)
	}
	for _, role := range strings.Split(roster, ",") {
		matches := windowsByName[role]
		if len(matches) == 0 {
			report.Agents = append(report.Agents, Agent{Role: role, State: StateMissing})
			continue
		}
		if len(matches) > 1 {
			for _, window := range matches {
				agent := agentForWindow(role, window)
				agent.State = StateAmbiguous
				report.Agents = append(report.Agents, agent)
			}
			continue
		}
		window := matches[0]
		agent := agentForWindow(role, window)
		if window.Managed != "1" || window.Role != role {
			agent.State = StateUnmanaged
			report.Agents = append(report.Agents, agent)
			continue
		}
		panes, err := c.client.ListPanes(ctx, window.ID)
		if err != nil {
			currentWindows, recheckErr := c.client.ListWindows(ctx, sessionID)
			if recheckErr != nil {
				return Report{}, classifyTmuxError(errors.Join(err, fmt.Errorf("recheck windows: %w", recheckErr)))
			}
			stillPresent := false
			for _, current := range currentWindows {
				if current.ID == window.ID {
					stillPresent = true
					break
				}
			}
			if stillPresent {
				return Report{}, classifyTmuxError(err)
			}
			agent.State = StateMissing
			report.Agents = append(report.Agents, agent)
			continue
		}
		if len(panes) == 0 {
			agent.State = StateMissing
			report.Agents = append(report.Agents, agent)
			continue
		}
		if len(panes) > 1 || panes[0].WindowPanes != 1 {
			agent.State = StateUnmanaged
			report.Agents = append(report.Agents, agent)
			continue
		}
		pane := panes[0]
		agent.PaneID = string(pane.ID)
		if pane.Dead {
			agent.State = StateDead
			report.Agents = append(report.Agents, agent)
			continue
		}
		if window.Process == "" {
			agent.State = StateUnexpectedProcess
			report.Agents = append(report.Agents, agent)
			continue
		}
		process, err := c.client.ProcessName(ctx, pane.PID)
		if err != nil {
			if errors.Is(err, tmuxx.ErrProcessUnavailable) {
				agent.State = StateUnexpectedProcess
				report.Agents = append(report.Agents, agent)
				continue
			}
			return Report{}, classifyTmuxError(err)
		}
		agent.Process = process
		if process != window.Process {
			agent.State = StateUnexpectedProcess
		} else {
			agent.State = StateRunning
		}
		report.Agents = append(report.Agents, agent)
	}
	return report, nil
}

func classifyTmuxError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &TmuxError{Err: err}
}

func agentForWindow(role string, window tmuxx.Window) Agent {
	return Agent{
		Role:    role,
		Harness: window.Harness,
		Model:   window.Model,
		Window:  window.Name,
	}
}
