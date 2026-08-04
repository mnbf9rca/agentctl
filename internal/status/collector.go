package status

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

type tmuxClient interface {
	ListSessions(context.Context) ([]tmuxx.Session, error)
	DisplayMessage(context.Context, tmuxx.PaneID) (string, error)
	ShowSessionOption(context.Context, tmuxx.SessionID, string) (string, error)
	ListWindows(context.Context, tmuxx.SessionID) ([]tmuxx.Window, error)
	ListPanes(context.Context, tmuxx.WindowID) ([]tmuxx.Pane, error)
	ProcessName(context.Context, int) (string, error)
}

// LookupEnv is compatible with os.LookupEnv.
type LookupEnv func(string) (string, bool)

// VersionError reports a managed session with an absent or unsupported
// agentctl version marker.
type VersionError struct {
	Session string
	Version string
}

// RosterError reports absent or structurally invalid role roster metadata on a
// managed session.
type RosterError struct {
	Roster string
}

func (e *VersionError) Error() string {
	if e.Version == "" {
		return "managed session carries no @agentctl_version marker"
	}
	return fmt.Sprintf("session %q was created by a different agentctl version %q", e.Session, e.Version)
}

func (e *RosterError) Error() string {
	if e.Roster != "" {
		return fmt.Sprintf("managed session has invalid @agentctl_roles roster %q", e.Roster)
	}
	return "managed session has no @agentctl_roles roster"
}

// Collector gathers one objective snapshot using the typed tmux boundary.
type Collector struct {
	client    tmuxClient
	lookupEnv LookupEnv
}

// NewCollector constructs a status collector.
func NewCollector(client tmuxClient) Collector {
	return Collector{client: client}
}

// WithLookupEnv enables the advisory current-session marker for listings.
func (c Collector) WithLookupEnv(lookupEnv LookupEnv) Collector {
	c.lookupEnv = lookupEnv
	return c
}

// CollectAll gathers status for every session on the tmux server, in the order
// tmux lists them.
func (c Collector) CollectAll(ctx context.Context) (*SessionsReport, error) {
	sessions, err := c.client.ListSessions(ctx)
	if err != nil {
		return nil, tmuxx.ClassifyError(err)
	}
	currentSession := ""
	if c.lookupEnv != nil {
		if pane, insideTmux := c.lookupEnv("TMUX_PANE"); insideTmux {
			currentSession, _ = c.client.DisplayMessage(ctx, tmuxx.PaneID(pane))
		}
	}

	all := SessionsReport{Schema: 1, Sessions: []Report{}}
	var defects []error
	for _, listed := range sessions {
		report, err := c.Collect(ctx, listed.Name, listed.ID)
		if err != nil {
			var versionError *VersionError
			var rosterError *RosterError
			if !errors.As(err, &versionError) && !errors.As(err, &rosterError) {
				return nil, err
			}
			defect := err
			if rosterError != nil || versionError.Version == "" {
				defect = fmt.Errorf("session %q: %w", listed.Name, err)
			}
			report = Report{Schema: 1, Session: listed.Name, Managed: true, Agents: []Agent{}, Defect: defect.Error()}
			defects = append(defects, defect)
		}
		report.Current = listed.Name == currentSession
		all.Sessions = append(all.Sessions, report)
	}
	return &all, errors.Join(defects...)
}

// Collect gathers status for one already-resolved session ID.
func (c Collector) Collect(ctx context.Context, sessionName string, sessionID tmuxx.SessionID) (Report, error) {
	report := Report{Schema: 1, Session: sessionName, Managed: true, Agents: []Agent{}}

	managed, err := c.client.ShowSessionOption(ctx, sessionID, "@agentctl_managed")
	if err != nil {
		return Report{}, tmuxx.ClassifyError(err)
	}
	version, err := c.client.ShowSessionOption(ctx, sessionID, "@agentctl_version")
	if err != nil {
		return Report{}, tmuxx.ClassifyError(err)
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
		return Report{}, tmuxx.ClassifyError(err)
	}
	if roster == "" {
		return Report{}, &RosterError{}
	}
	roles := strings.Split(roster, ",")
	for _, role := range roles {
		if role == "" {
			return Report{}, &RosterError{Roster: roster}
		}
	}
	windows, err := c.client.ListWindows(ctx, sessionID)
	if err != nil {
		return Report{}, tmuxx.ClassifyError(err)
	}

	windowsByName := make(map[string][]tmuxx.Window, len(windows))
	for _, window := range windows {
		windowsByName[window.Name] = append(windowsByName[window.Name], window)
	}
	for _, role := range roles {
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
				return Report{}, tmuxx.ClassifyError(errors.Join(err, fmt.Errorf("recheck windows: %w", recheckErr)))
			}
			stillPresent := false
			for _, current := range currentWindows {
				if current.ID == window.ID {
					stillPresent = true
					break
				}
			}
			if stillPresent {
				return Report{}, tmuxx.ClassifyError(err)
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
			return Report{}, tmuxx.ClassifyError(err)
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

func agentForWindow(role string, window tmuxx.Window) Agent {
	return Agent{
		Role:    role,
		Harness: window.Harness,
		Model:   window.Model,
		Effort:  window.Effort,
		Window:  window.Name,
	}
}
