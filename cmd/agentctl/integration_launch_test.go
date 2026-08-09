//go:build integration

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	statuspkg "github.com/mnbf9rca/agentctl/internal/status"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestIntegrationLaunchStartsWithoutTmuxServer(t *testing.T) {
	fixture := newIntegrationFixtureWithoutServer(t)

	result := fixture.runAgentctl(
		"launch",
		"--session", "integration-first-launch",
		"--roles", "planner:claude",
	)
	assertLaunchMatchesStatus(t, fixture, result, "integration-first-launch")

	sessions := fixture.sessions()
	if len(sessions) != 1 || sessions[0].Name != "integration-first-launch" {
		t.Fatalf("sessions = %#v, want sole integration-first-launch session", sessions)
	}
}

func TestIntegrationLaunchRecordsTopologyMetadataAndBaseline(t *testing.T) {
	fixture := newIntegrationFixture(t)
	launchDir := t.TempDir()

	result := fixture.runAgentctl(
		"launch",
		"--session", "integration-launch",
		"--roles", "planner:claude,coder:codex",
		"--models", "planner:opus,coder:gpt-5",
		"--efforts", "planner:max,coder:high",
		"--dir", launchDir,
	)
	assertLaunchMatchesStatus(t, fixture, result, "integration-launch")

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
		effort  string
	}{
		"planner": {harness: "claude", model: "opus", effort: "max"},
		"coder":   {harness: "codex", model: "gpt-5", effort: "high"},
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
		if managed := fixture.windowOption(window.ID, "@agentctl_managed"); managed != "1" {
			t.Errorf("window @agentctl_managed = %q, want \"1\"", managed)
		}
		if window.Role != window.Name || window.Harness != want.harness || window.Model != want.model || window.Effort != want.effort {
			t.Errorf("window metadata = %#v, want role-specific metadata", window)
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
		if invocation.Session != "integration-launch" || invocation.Harness != want.harness || invocation.Model != want.model || invocation.Effort != want.effort {
			t.Errorf("stub invocation = %#v, want launch inputs preserved", invocation)
		}
	}
	for role := range expected {
		if !seenInvocations[role] {
			t.Errorf("missing stub invocation for role %q", role)
		}
	}
}

func TestIntegrationLaunchTemplateCreatesTheEffectiveUnionInPinnedOrderWithoutWritingTheSource(t *testing.T) {
	fixture := newIntegrationFixture(t)
	templateDirectory := t.TempDir()
	launchDirectory := t.TempDir()
	templatePath := writeLaunchTemplateFixture(t, templateDirectory, "fleet.json", `{
  "version": 1,
  "dir": "/template/default",
  "roles": [
    {"role":"planner","harness":"claude","model":"opus","effort":"high"}
  ]
}`)
	before, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatal(err)
	}

	result := fixture.runAgentctl(
		"launch",
		"--session", "integration-template",
		"--from-template", templatePath,
		"--roles", "coder:codex",
		"--models", "coder:gpt-5",
		"--efforts", "planner:max,coder:high",
		"--dir", launchDirectory,
	)
	if result.exitCode != exitOK || result.stderr != "" {
		t.Fatalf("launch result = %#v, want success", result)
	}
	provenance := "agentctl: launched planner in integration-template: harness claude (template), model opus (template), effort max (flag override)\n" +
		"agentctl: launched coder in integration-template: harness codex (flags), model gpt-5 (flags), effort high (flags)\n" +
		"agentctl: template " + templatePath + ": dir " + launchDirectory + " (flag override)\n"
	if !strings.HasPrefix(result.stdout, provenance) {
		t.Fatalf("launch stdout = %q, want provenance prefix %q", result.stdout, provenance)
	}
	status := fixture.runAgentctl("status", "--session", "integration-template")
	if status.exitCode != exitOK || status.stderr != "" || !strings.HasSuffix(result.stdout, status.stdout) {
		t.Fatalf("status = %#v; launch stdout = %q, want observed status suffix", status, result.stdout)
	}

	sessions := fixture.sessions()
	if len(sessions) != 1 || sessions[0].Roles != "planner,coder" ||
		sessions[0].Fleet != "planner:claude:opus:max,coder:codex:gpt-5:high" || sessions[0].Directory != launchDirectory {
		t.Fatalf("session metadata = %#v, want effective ordered union and override directory", sessions)
	}
	windows := fixture.windows(sessions[0].ID)
	if len(windows) != 2 || windows[0].Role != "planner" || windows[1].Role != "coder" {
		t.Fatalf("windows = %#v, want template role then flag-added role", windows)
	}
	after, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("template contents changed from %q to %q", before, after)
	}
}

func TestIntegrationLaunchRetainsUnprovenRoleForRelaunchRecovery(t *testing.T) {
	fixture := newIntegrationFixture(t)
	fixture.runner.makeProcessUnavailableAfterFirstPID()

	result := fixture.runAgentctl(
		"launch",
		"--session", "integration-launch-unproven",
		"--roles", "planner:claude,coder:codex",
	)
	if result.exitCode != exitLaunchUnproven {
		t.Fatalf("launch result = %#v, want exit %d", result, exitLaunchUnproven)
	}
	if !strings.Contains(result.stderr, "coder: no process baseline recorded") ||
		!strings.Contains(result.stderr, `session "integration-launch-unproven" launched; 1 of 2 roles unproven: coder; nothing was rolled back`) {
		t.Fatalf("launch stderr = %q, want retained-role diagnostic and summary", result.stderr)
	}
	fixture.waitStubInvocations(2)
	fixture.waitRoleMarkers("planner", "coder")

	session := fixture.sessions()[0]
	windows := fixture.windows(session.ID)
	if len(windows) != 2 {
		t.Fatalf("windows after unproven launch = %#v, want both roles retained", windows)
	}
	var originalCoder tmuxx.WindowID
	for _, window := range windows {
		switch window.Role {
		case "planner":
			if window.Process == "" || window.Unproven != "" {
				t.Fatalf("planner window = %#v, want recorded baseline", window)
			}
		case "coder":
			if window.Process != "" || window.Unproven != "1" {
				t.Fatalf("coder window = %#v, want empty baseline and abandonment record", window)
			}
			originalCoder = window.ID
		}
	}
	if originalCoder == "" {
		t.Fatalf("windows after unproven launch = %#v, want coder window", windows)
	}
	report := parseIntegrationStatus(t, fixture.runAgentctl("status", "--session", session.Name, "--json"))
	if len(report.Agents) != 2 || report.Agents[0].State != statuspkg.StateRunning || report.Agents[1].State != statuspkg.StateNoBaseline {
		t.Fatalf("status after unproven launch = %#v, want running then no-baseline", report.Agents)
	}

	fixture.runner.allowAllProcesses()
	recovery := fixture.runAgentctl("relaunch", "--session", session.Name, "coder")
	if recovery.exitCode != exitOK || recovery.stderr != "" {
		t.Fatalf("relaunch result = %#v, want recovery success", recovery)
	}
	if !strings.Contains(recovery.stdout, "recovered: removed window "+string(originalCoder)) {
		t.Fatalf("relaunch stdout = %q, want recovered window %s", recovery.stdout, originalCoder)
	}
	fixture.waitStubInvocations(3)
	fixture.waitRoleMarkers("coder")
	for _, window := range fixture.windows(session.ID) {
		if window.Role == "coder" && (window.ID == originalCoder || window.Process == "" || window.Unproven != "") {
			t.Fatalf("coder after recovery = %#v, want new proven window", window)
		}
	}
}

func TestIntegrationLaunchExportsIdentityEnvironmentIntoEveryPane(t *testing.T) {
	fixture := newIntegrationFixture(t)

	result := fixture.runAgentctl(
		"launch",
		"--session", "integration-identity-env",
		"--roles", "planner:claude,coder:codex",
	)
	assertLaunchMatchesStatus(t, fixture, result, "integration-identity-env")

	// planner comes from the new-session path and coder from new-window, so
	// both creation paths are observed from inside a real pane.
	want := map[string]stubEnvironment{
		"planner": {Session: "integration-identity-env", Role: "planner", Managed: "1"},
		"coder":   {Session: "integration-identity-env", Role: "coder", Managed: "1"},
	}
	invocations := fixture.waitStubInvocations(2)
	if len(invocations) != 2 {
		t.Fatalf("stub invocations = %#v, want two", invocations)
	}
	seen := make(map[string]bool, len(want))
	for _, invocation := range invocations {
		wantEnvironment, ok := want[invocation.Role]
		if !ok {
			t.Fatalf("unexpected stub invocation %#v", invocation)
		}
		if seen[invocation.Role] {
			t.Fatalf("duplicate stub invocation for role %q: %#v", invocation.Role, invocations)
		}
		seen[invocation.Role] = true
		if invocation.Environment != wantEnvironment {
			t.Errorf("role %q pane environment = %#v, want %#v", invocation.Role, invocation.Environment, wantEnvironment)
		}
	}
	for role := range want {
		if !seen[role] {
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

func TestIntegrationLaunchClassifiesDuplicateSessionWhenAdvisoryLookupFails(t *testing.T) {
	fixture := newIntegrationFixture(t)
	fixture.createSentinelSession("integration-raced-existing")
	sentinel := fixture.sentinelSnapshot("integration-raced-existing")
	fixture.runner.failNextTmuxOperation("list-sessions")

	result := fixture.runAgentctl(
		"launch",
		"--session", "integration-raced-existing",
		"--roles", "planner:claude,coder:codex",
	)
	if result.exitCode != 3 || result.stdout != "" || !strings.Contains(result.stderr, "duplicate session: integration-raced-existing") {
		t.Fatalf("launch result = %#v, want existing-session classification with captured tmux stderr", result)
	}
	current := fixture.sentinelSnapshot("integration-raced-existing")
	if current != sentinel {
		t.Fatalf("sentinel after creation refusal = %#v, want unchanged %#v", current, sentinel)
	}
	if invocations := fixture.stubInvocations(); len(invocations) != 0 {
		t.Fatalf("stub invocations after creation refusal = %#v, want none", invocations)
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

func assertLaunchMatchesStatus(t *testing.T, fixture *integrationFixture, launch integrationResult, session string) {
	t.Helper()
	if launch.exitCode != 0 || launch.stdout == "" || launch.stderr != "" {
		t.Fatalf("launch result = %#v, want successful observed status table", launch)
	}
	status := fixture.runAgentctl("status", "--session", session)
	if status.exitCode != 0 || status.stderr != "" {
		t.Fatalf("status after launch = %#v, want success", status)
	}
	if launch.stdout != status.stdout {
		t.Fatalf("launch stdout = %q, want status stdout %q", launch.stdout, status.stdout)
	}
}
