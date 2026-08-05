//go:build integration

package main

import (
	"os"
	"strings"
	"testing"

	statuspkg "github.com/mnbf9rca/agentctl/internal/status"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestIntegrationRelaunchRecreatesOnlyTheAbsentRole(t *testing.T) {
	fixture := newIntegrationFixture(t)
	launch := fixture.runAgentctl(
		"launch",
		"--session", "integration-relaunch",
		"--roles", "planner:claude,coder:codex",
		"--models", "coder:gpt-5.6",
	)
	if launch.exitCode != 0 {
		t.Fatalf("launch result = %#v, want success", launch)
	}
	fixture.waitStubInvocations(2)
	fixture.waitRoleMarkers("planner", "coder")

	session := fixture.sessions()[0]
	if want := "planner:claude::,coder:codex:gpt-5.6:"; session.Fleet != want {
		t.Fatalf("@agentctl_fleet = %q, want %q", session.Fleet, want)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	if session.Directory != workingDirectory {
		t.Fatalf("@agentctl_dir = %q, want the invocation directory %q", session.Directory, workingDirectory)
	}

	refusal := fixture.runAgentctl("relaunch", "--session", "integration-relaunch", "coder")
	if refusal.exitCode != exitRole || !strings.Contains(refusal.stderr, "already has 1 window") {
		t.Fatalf("relaunch of a live role = %#v, want a refusal naming the surviving window", refusal)
	}

	var coderWindowID tmuxx.WindowID
	for _, window := range fixture.windows(session.ID) {
		if window.Role == "coder" {
			coderWindowID = window.ID
		}
	}
	if coderWindowID == "" {
		t.Fatalf("coder window absent from %#v", fixture.windows(session.ID))
	}
	fixture.killWindow(coderWindowID)

	result := fixture.runAgentctl("relaunch", "--session", "integration-relaunch", "coder")
	if result.exitCode != 0 || result.stderr != "" {
		t.Fatalf("relaunch result = %#v, want success", result)
	}
	if !strings.Contains(result.stdout, "harness codex (stored), model gpt-5.6 (stored)") {
		t.Fatalf("relaunch stdout = %q, want the stored configuration reported", result.stdout)
	}
	fixture.waitStubInvocations(3)
	fixture.waitRoleMarkers("coder")

	var relaunched tmuxx.WindowID
	for _, window := range fixture.windows(session.ID) {
		if window.Role == "coder" {
			relaunched = window.ID
		}
	}
	if relaunched == "" || relaunched == coderWindowID {
		t.Fatalf("relaunched coder window = %q, want a new window ID distinct from %q", relaunched, coderWindowID)
	}

	report := parseIntegrationStatus(t, fixture.runAgentctl("status", "--session", "integration-relaunch", "--json"))
	if len(report.Agents) != 2 {
		t.Fatalf("status after relaunch = %#v, want two roster rows", report)
	}
	for _, agent := range report.Agents {
		if agent.State != statuspkg.StateRunning {
			t.Fatalf("agent after relaunch = %#v, want running", agent)
		}
	}

	clear := fixture.runAgentctl("clear", "--session", "integration-relaunch", "coder")
	if clear.exitCode != 0 || !strings.Contains(clear.stdout, "delivered /clear to integration-relaunch:coder") {
		t.Fatalf("clear after relaunch = %#v, want delivery against the fresh baseline", clear)
	}
	fixture.waitRoleInput("coder", "/clear\n")
}
