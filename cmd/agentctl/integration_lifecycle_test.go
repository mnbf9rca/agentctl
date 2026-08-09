//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"os"
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

func TestIntegrationStatusWithoutSessionListsEverySession(t *testing.T) {
	fixture := newIntegrationFixture(t)
	for _, session := range []string{"integration-all-a", "integration-all-b"} {
		launch := fixture.runAgentctl("launch", "--session", session, "--roles", "planner:claude")
		if launch.exitCode != 0 {
			t.Fatalf("launch %s result = %#v, want success", session, launch)
		}
	}
	fixture.waitStubInvocations(2)
	fixture.createSentinelSession("integration-all-plain")

	t.Setenv("AGENTCTL_SESSION", "")
	if err := os.Unsetenv("AGENTCTL_SESSION"); err != nil {
		t.Fatalf("unset AGENTCTL_SESSION: %v", err)
	}
	if err := os.Unsetenv("TMUX_PANE"); err != nil {
		t.Fatalf("unset TMUX_PANE: %v", err)
	}

	result := fixture.runAgentctl("status", "--json")
	if result.exitCode != 0 || result.stderr != "" {
		t.Fatalf("status result = %#v, want JSON success", result)
	}
	var report statuspkg.SessionsReport
	if err := json.Unmarshal([]byte(result.stdout), &report); err != nil {
		t.Fatalf("parse status JSON %q: %v", result.stdout, err)
	}
	if report.Schema != 1 {
		t.Fatalf("status schema = %d, want 1", report.Schema)
	}
	listed := make(map[string]statuspkg.Report, len(report.Sessions))
	for _, session := range report.Sessions {
		if _, duplicate := listed[session.Session]; duplicate {
			t.Fatalf("status listed %q twice: %#v", session.Session, report.Sessions)
		}
		listed[session.Session] = session
	}
	if len(listed) != 3 {
		t.Fatalf("status listed %#v, want the managed and unmanaged sessions", report.Sessions)
	}
	for _, name := range []string{"integration-all-a", "integration-all-b"} {
		session, ok := listed[name]
		if !ok {
			t.Fatalf("status omitted managed session %q: %#v", name, report.Sessions)
		}
		if !session.Managed || len(session.Agents) != 1 || session.Agents[0].Role != "planner" {
			t.Fatalf("status session %q = %#v, want one managed planner row", name, session)
		}
	}
	sentinel, listedSentinel := listed["integration-all-plain"]
	if !listedSentinel {
		t.Fatalf("status omitted the unmanaged sentinel session: %#v", report.Sessions)
	}
	if sentinel.Managed || len(sentinel.Agents) != 0 {
		t.Fatalf("unmanaged sentinel = %#v, want managed false and no agents", sentinel)
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

func TestIntegrationHandmadeRosterWindowIsNeverControlledOrReplaced(t *testing.T) {
	fixture := newIntegrationFixture(t)
	launch := fixture.runAgentctl(
		"launch",
		"--session", "integration-handmade-window",
		"--roles", "planner:claude,coder:codex",
	)
	if launch.exitCode != 0 {
		t.Fatalf("launch result = %#v, want success", launch)
	}
	fixture.waitStubInvocations(2)
	fixture.waitRoleMarkers("planner", "coder")

	sessions := fixture.sessions()
	if len(sessions) != 1 {
		t.Fatalf("sessions = %#v, want one managed session", sessions)
	}
	session := sessions[0]
	var coderWindowID tmuxx.WindowID
	for _, window := range fixture.windows(session.ID) {
		if window.Name == "coder" {
			coderWindowID = window.ID
		}
	}
	if coderWindowID == "" {
		t.Fatalf("coder window absent from %#v", fixture.windows(session.ID))
	}
	fixture.killWindow(coderWindowID)

	handmade := fixture.createHandmadeWindow(session.ID, "coder")
	fixture.waitHandmadeWindowReady(handmade)
	for _, option := range []string{
		"@agentctl_managed", "@agentctl_version", "@agentctl_role", "@agentctl_harness",
		"@agentctl_model", "@agentctl_effort", "@agentctl_unproven", "@agentctl_process",
	} {
		if got := fixture.windowOption(handmade.ID, option); got != "" {
			t.Fatalf("handmade window option %s = %q, want absent", option, got)
		}
	}

	clear := fixture.runAgentctl("clear", "--session", "integration-handmade-window", "coder")
	wantClear := fmt.Sprintf("agentctl: refusing to send clear; window %s named coder has stored role \"\"; expected \"coder\"\n", handmade.ID)
	if clear.exitCode != exitRole || clear.stdout != "" || clear.stderr != wantClear {
		t.Fatalf("clear against handmade window = %#v, want stored-role refusal", clear)
	}
	fixture.assertHandmadeInputRemains(handmade, "", 750*time.Millisecond)

	report := parseIntegrationStatus(t, fixture.runAgentctl("status", "--session", "integration-handmade-window", "--json"))
	if len(report.Agents) != 2 {
		t.Fatalf("status with handmade window = %#v, want two roster rows", report)
	}
	wantStates := map[string]statuspkg.State{
		"planner": statuspkg.StateRunning,
		"coder":   statuspkg.StateUnmanaged,
	}
	for _, agent := range report.Agents {
		if want, ok := wantStates[agent.Role]; !ok || agent.State != want {
			t.Fatalf("status with handmade window agent = %#v, want states %#v", agent, wantStates)
		}
		delete(wantStates, agent.Role)
	}
	if len(wantStates) != 0 {
		t.Fatalf("status with handmade window omitted roles %#v", wantStates)
	}

	before := make(map[tmuxx.WindowID]struct{})
	for _, window := range fixture.windows(session.ID) {
		before[window.ID] = struct{}{}
	}
	if _, present := before[handmade.ID]; !present {
		t.Fatalf("handmade window %s absent before relaunch: %#v", handmade.ID, fixture.windows(session.ID))
	}
	relaunch := fixture.runAgentctl("relaunch", "--session", "integration-handmade-window", "coder")
	wantRelaunch := fmt.Sprintf("agentctl: refusing to relaunch coder; role coder already has 1 window in integration-handmade-window (%s unmanaged); relaunch accepts only an absent role or a recoverable no-baseline window\n", handmade.ID)
	if relaunch.exitCode != exitRole || relaunch.stdout != "" || relaunch.stderr != wantRelaunch {
		t.Fatalf("relaunch against handmade window = %#v, want existing-window refusal", relaunch)
	}
	after := make(map[tmuxx.WindowID]struct{})
	for _, window := range fixture.windows(session.ID) {
		after[window.ID] = struct{}{}
	}
	if len(after) != len(before) {
		t.Fatalf("window IDs after relaunch = %#v, want %#v", after, before)
	}
	for windowID := range before {
		if _, present := after[windowID]; !present {
			t.Fatalf("window %s missing after relaunch: %#v", windowID, after)
		}
	}
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
