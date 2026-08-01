//go:build integration

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationLaunchRecordsTopologyMetadataAndBaseline(t *testing.T) {
	fixture := newIntegrationFixture(t)
	launchDir := t.TempDir()

	result := fixture.runAgentctl(
		"launch",
		"--session", "integration-launch",
		"--roles", "planner:claude,coder:codex",
		"--models", "planner:opus,coder:gpt-5",
		"--dir", launchDir,
	)
	if result.exitCode != 0 || result.stdout != "" || result.stderr != "" {
		t.Fatalf("launch result = %#v, want silent success", result)
	}

	sessions := fixture.sessions()
	if len(sessions) != 1 || sessions[0].Name != "integration-launch" {
		t.Fatalf("sessions = %#v, want sole integration-launch session", sessions)
	}
	if sessions[0].Managed != "1" || sessions[0].Version != "1" || sessions[0].Roles != "planner,coder" {
		t.Fatalf("session metadata = %#v, want managed version-1 planner,coder roster", sessions[0])
	}

	windows := fixture.windows(sessions[0].ID)
	if len(windows) != 2 {
		t.Fatalf("windows = %#v, want two roles", windows)
	}
	expected := map[string]struct {
		harness string
		model   string
	}{
		"planner": {harness: "claude", model: "opus"},
		"coder":   {harness: "codex", model: "gpt-5"},
	}
	seenWindows := make(map[string]bool, len(expected))
	for _, window := range windows {
		want, ok := expected[window.Name]
		if !ok {
			t.Fatalf("unexpected window %#v", window)
		}
		if seenWindows[window.Name] {
			t.Fatalf("duplicate window for role %q: %#v", window.Name, windows)
		}
		seenWindows[window.Name] = true
		if window.Managed != "1" || window.Role != window.Name || window.Harness != want.harness || window.Model != want.model {
			t.Errorf("window metadata = %#v, want role-specific managed metadata", window)
		}
		gotDirectory, err := os.Stat(window.Directory)
		if err != nil {
			t.Fatalf("stat window %q directory %q: %v", window.Name, window.Directory, err)
		}
		wantDirectory, err := os.Stat(launchDir)
		if err != nil {
			t.Fatalf("stat launch directory %q: %v", launchDir, err)
		}
		if !os.SameFile(gotDirectory, wantDirectory) {
			t.Errorf("window %q directory = %q, want same file as %q", window.Name, window.Directory, launchDir)
		}
		panes := fixture.panes(window.ID)
		if len(panes) != 1 || panes[0].Dead || panes[0].WindowPanes != 1 {
			t.Fatalf("window %q panes = %#v, want one live pane", window.Name, panes)
		}
		if got := fixture.processName(panes[0].PID); got != window.Process || filepath.Base(got) != want.harness {
			t.Errorf("window %q process = %q, metadata baseline = %q", window.Name, got, window.Process)
		}
	}
	for role := range expected {
		if !seenWindows[role] {
			t.Errorf("missing window for role %q", role)
		}
	}

	invocations := fixture.waitStubInvocations(2)
	if len(invocations) != 2 {
		t.Fatalf("stub invocations = %#v, want two", invocations)
	}
	seenInvocations := make(map[string]bool, len(expected))
	for _, invocation := range invocations {
		want, ok := expected[invocation.Role]
		if !ok {
			t.Fatalf("unexpected stub invocation %#v", invocation)
		}
		if seenInvocations[invocation.Role] {
			t.Fatalf("duplicate stub invocation for role %q: %#v", invocation.Role, invocations)
		}
		seenInvocations[invocation.Role] = true
		if invocation.Session != "integration-launch" || invocation.Harness != want.harness || invocation.Model != want.model {
			t.Errorf("stub invocation = %#v, want launch inputs preserved", invocation)
		}
	}
	for role := range expected {
		if !seenInvocations[role] {
			t.Errorf("missing stub invocation for role %q", role)
		}
	}
}

func TestIntegrationLaunchRefusesExistingSessionWithoutMutation(t *testing.T) {
	fixture := newIntegrationFixture(t)
	fixture.createSentinelSession("integration-existing")
	sentinel := fixture.sentinelSnapshot("integration-existing")

	result := fixture.runAgentctl(
		"launch",
		"--session", "integration-existing",
		"--roles", "planner:claude,coder:codex",
	)
	if result.exitCode != 3 || result.stdout != "" || !strings.Contains(result.stderr, `session "integration-existing" already exists`) {
		t.Fatalf("launch result = %#v, want existing-session refusal", result)
	}
	current := fixture.sentinelSnapshot("integration-existing")
	if current != sentinel {
		t.Fatalf("sentinel after refusal = %#v, want unchanged %#v", current, sentinel)
	}
	if invocations := fixture.stubInvocations(); len(invocations) != 0 {
		t.Fatalf("stub invocations after existing-session refusal = %#v, want none", invocations)
	}
}

func TestIntegrationLaunchRollsBackAfterLaterWindowFailure(t *testing.T) {
	fixture := newIntegrationFixture(t)
	fixture.runner.failNextTmuxOperation("new-window")

	result := fixture.runAgentctl(
		"launch",
		"--session", "integration-rollback",
		"--roles", "planner:claude,coder:codex",
	)
	if result.exitCode != 8 || result.stdout != "" || !strings.Contains(result.stderr, "removed incomplete session integration-rollback") {
		t.Fatalf("launch result = %#v, want post-ownership rollback failure", result)
	}
	for _, session := range fixture.sessions() {
		if session.Name == "integration-rollback" {
			t.Fatalf("partially launched session remains after rollback: %#v", session)
		}
	}
}
