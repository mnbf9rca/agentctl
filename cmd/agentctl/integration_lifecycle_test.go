//go:build integration

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	statuspkg "github.com/mnbf9rca/agentctl/internal/status"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestIntegrationStatusReportsLiveAndKilledRoleWindows(t *testing.T) {
	fixture := newIntegrationFixture(t)
	launch := fixture.runAgentctl(
		"launch",
		"--session", "integration-status",
		"--roles", "planner:claude,coder:codex",
	)
	if launch.exitCode != 0 {
		t.Fatalf("launch result = %#v, want success", launch)
	}
	fixture.waitStubInvocations(2)
	fixture.waitRoleMarkers("planner", "coder")

	initial := fixture.runAgentctl("status", "--session", "integration-status", "--json")
	report := parseIntegrationStatus(t, initial)
	if report.Session != "integration-status" || !report.Managed || len(report.Agents) != 2 {
		t.Fatalf("initial status = %#v, want managed two-role report", report)
	}
	initialRoles := map[string]bool{"planner": false, "coder": false}
	for _, agent := range report.Agents {
		seen, expected := initialRoles[agent.Role]
		if !expected || seen {
			t.Fatalf("initial status has unexpected or duplicate role %q: %#v", agent.Role, report.Agents)
		}
		initialRoles[agent.Role] = true
		if agent.State != statuspkg.StateRunning || agent.PaneID == "" || agent.Process == "" {
			t.Errorf("initial agent = %#v, want live process facts", agent)
		}
	}
	for role, seen := range initialRoles {
		if !seen {
			t.Errorf("initial status missing role %q", role)
		}
	}

	session := fixture.sessions()[0]
	windows := fixture.windows(session.ID)
	var coderWindowID tmuxx.WindowID
	for _, window := range windows {
		if window.Role == "coder" {
			coderWindowID = window.ID
		}
	}
	if coderWindowID == "" {
		t.Fatalf("coder window absent from %#v", windows)
	}
	fixture.killWindow(coderWindowID)

	afterKill := fixture.runAgentctl("status", "--session", "integration-status", "--json")
	report = parseIntegrationStatus(t, afterKill)
	if len(report.Agents) != 2 {
		t.Fatalf("status after window kill = %#v, want two roster rows", report)
	}
	wantStates := map[string]statuspkg.State{
		"planner": statuspkg.StateRunning,
		"coder":   statuspkg.StateMissing,
	}
	seenRoles := make(map[string]bool, len(wantStates))
	for _, agent := range report.Agents {
		want, ok := wantStates[agent.Role]
		if !ok || seenRoles[agent.Role] {
			t.Fatalf("status after window kill has unexpected or duplicate role %q: %#v", agent.Role, report.Agents)
		}
		seenRoles[agent.Role] = true
		if agent.State != want {
			t.Errorf("agent after window kill = %#v, want state %q", agent, want)
		}
	}
	for role := range wantStates {
		if !seenRoles[role] {
			t.Errorf("status after window kill missing role %q", role)
		}
	}
}

func TestIntegrationClearReachesOnlyTargetMarker(t *testing.T) {
	fixture := newIntegrationFixture(t)
	launch := fixture.runAgentctl(
		"launch",
		"--session", "integration-clear",
		"--roles", "planner:claude,coder:codex",
	)
	if launch.exitCode != 0 {
		t.Fatalf("launch result = %#v, want success", launch)
	}
	fixture.waitStubInvocations(2)
	fixture.waitRoleMarkers("planner", "coder")

	result := fixture.runAgentctl("clear", "--session", "integration-clear", "planner")
	if result.exitCode != 0 || result.stderr != "" || !strings.Contains(result.stdout, "delivered /clear to integration-clear:planner") {
		t.Fatalf("clear result = %#v, want target delivery success", result)
	}
	fixture.waitRoleInput("planner", "/clear\n")
	fixture.assertRoleInputRemains("coder", "", 750*time.Millisecond)
}

func TestIntegrationKillRemovesManagedSession(t *testing.T) {
	fixture := newIntegrationFixture(t)
	launch := fixture.runAgentctl(
		"launch",
		"--session", "integration-kill",
		"--roles", "planner:claude,coder:codex",
	)
	if launch.exitCode != 0 {
		t.Fatalf("launch result = %#v, want success", launch)
	}
	fixture.waitStubInvocations(2)

	result := fixture.runAgentctl("kill", "--session", "integration-kill")
	if result.exitCode != 0 || result.stdout != "" || result.stderr != "" {
		t.Fatalf("kill result = %#v, want silent success", result)
	}
	if fixture.hasSession("integration-kill") {
		t.Fatal("managed session remains after kill")
	}
}

func parseIntegrationStatus(t *testing.T, result integrationResult) statuspkg.Report {
	t.Helper()
	if result.exitCode != 0 || result.stderr != "" {
		t.Fatalf("status result = %#v, want JSON success", result)
	}
	var report statuspkg.Report
	if err := json.Unmarshal([]byte(result.stdout), &report); err != nil {
		t.Fatalf("parse status JSON %q: %v", result.stdout, err)
	}
	return report
}
