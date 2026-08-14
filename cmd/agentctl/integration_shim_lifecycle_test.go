//go:build integration && darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/fleet"
	"github.com/mnbf9rca/agentctl/internal/kill"
	"github.com/mnbf9rca/agentctl/internal/shim"
	statuspkg "github.com/mnbf9rca/agentctl/internal/status"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestIntegrationPublicForegroundRunUsesRuntimeWithoutTmux(t *testing.T) {
	fixture := newIntegrationFixtureWithoutServer(t)
	scriptPath, err := exec.LookPath("script")
	if err != nil {
		t.Skipf("foreground integration requires script PTY wrapper: %v", err)
	}
	input, keepOpen, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer keepOpen.Close()
	var output bytes.Buffer
	command := exec.Command(scriptPath, "-q", "/dev/null", fixture.agentctlPath,
		"run", "--session", "direct", "--role", "planner", "--harness", "claude")
	command.Dir = os.Getenv(integrationProjectEnv)
	command.Env = environmentWith(os.Environ(), integrationAgentctlEnv, "1")
	command.Stdin = input
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatalf("start public foreground run: %v", err)
	}

	deadline := time.Now().Add(8 * time.Second)
	var observations []string
	lastObservation := ""
	for {
		statusResult := fixture.runAgentctl("status", "--session", "direct", "--json")
		observation := fmt.Sprintf("exit=%d stdout=%s stderr=%s invocations=%d", statusResult.exitCode, strings.TrimSpace(statusResult.stdout), strings.TrimSpace(statusResult.stderr), len(fixture.stubInvocations()))
		if observation != lastObservation {
			observations = append(observations, observation)
			lastObservation = observation
		}
		if statusResult.exitCode == exitOK && strings.Contains(statusResult.stdout, `"state":"running"`) {
			break
		}
		if !time.Now().Before(deadline) {
			_ = command.Process.Kill()
			t.Fatalf("foreground role did not become runtime-running; observations=%#v output=%q", observations, output.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
	if _, present, err := fixture.client.FindPresentationSession(context.Background(), "direct"); err != nil || present {
		t.Fatalf("foreground presentation = present %t, error %v; want gone", present, err)
	}
	cleared := fixture.runAgentctl("clear", "--session", "direct", "planner")
	if cleared.exitCode != exitOK {
		t.Fatalf("foreground clear = %#v", cleared)
	}
	fixture.waitRoleInput("planner", "\x15/clear\n")
	killed := fixture.runAgentctl("kill", "--session", "direct")
	if killed.exitCode != exitOK {
		t.Fatalf("foreground kill = %#v", killed)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("foreground process exit: %v; output=%q", err, output.String())
	}
	if !strings.Contains(output.String(), "foreground role \"planner\" in session \"direct\" exited with status 0") {
		t.Fatalf("foreground output=%q, want observed exit status", output.String())
	}
}

func TestIntegrationPublicForegroundRunRelaysCtrlCToNestedPTYAndCleansUp(t *testing.T) {
	fixture := newIntegrationFixtureWithoutServer(t)
	scriptPath, err := exec.LookPath("script")
	if err != nil {
		t.Skipf("foreground integration requires script PTY wrapper: %v", err)
	}
	input, keepOpen, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer keepOpen.Close()
	var output bytes.Buffer
	command := exec.Command(scriptPath, "-q", "/dev/null", fixture.agentctlPath,
		"run", "--session", "direct-signal", "--role", "planner", "--harness", "claude")
	command.Dir = os.Getenv(integrationProjectEnv)
	command.Env = environmentWith(os.Environ(), integrationAgentctlEnv, "1")
	command.Stdin = input
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatalf("start public foreground run: %v", err)
	}

	deadline := time.Now().Add(8 * time.Second)
	for {
		statusResult := fixture.runAgentctl("status", "--session", "direct-signal", "--json")
		if statusResult.exitCode == exitOK && strings.Contains(statusResult.stdout, `"state":"running"`) {
			break
		}
		if !time.Now().Before(deadline) {
			_ = command.Process.Kill()
			t.Fatalf("foreground role did not become runtime-running; output=%q", output.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
	if _, err := keepOpen.Write([]byte{3}); err != nil {
		t.Fatalf("write Ctrl-C: %v", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		t.Fatalf("foreground process did not exit after relayed Ctrl-C; output=%q", output.String())
	}
	if !strings.Contains(output.String(), "foreground role \"planner\" in session \"direct-signal\" terminated by signal SIGINT (child-signal)") {
		t.Fatalf("foreground Ctrl-C output=%q, want observed nested-child SIGINT", output.String())
	}
	statusResult := fixture.runAgentctl("status", "--session", "direct-signal", "--json")
	cleanedRole := statusResult.exitCode == exitOK && strings.Contains(statusResult.stdout, `"state":"missing"`)
	cleanedNewFleet := statusResult.exitCode == exitSession && strings.Contains(statusResult.stderr, `has no durable fleet configuration`)
	if !cleanedRole && !cleanedNewFleet {
		t.Fatalf("status after Ctrl-C = %#v, want cleaned missing role or cleaned newly owned fleet", statusResult)
	}
}

func TestIntegrationReleaseCandidateForegroundExtendsRosterAndRefusesDifferentDirectory(t *testing.T) {
	if os.Getenv(integrationCandidateEnv) == "" {
		t.Skip("release-candidate routing is enabled only by the Task 8 walkthrough")
	}
	fixture := newIntegrationFixtureWithoutServer(t)
	scriptPath, err := exec.LookPath("script")
	if err != nil {
		t.Skipf("foreground integration requires script PTY wrapper: %v", err)
	}
	project := os.Getenv(integrationProjectEnv)
	if project == "" {
		project = t.TempDir()
	}
	type foregroundProcess struct {
		command  *exec.Cmd
		input    *os.File
		keepOpen *os.File
		output   *bytes.Buffer
		waited   bool
	}
	start := func(role, directory string) *foregroundProcess {
		t.Helper()
		input, keepOpen, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		output := &bytes.Buffer{}
		command := exec.Command(scriptPath, "-q", "/dev/null", fixture.agentctlPath,
			"run", "--session", "direct-roster", "--role", role, "--harness", "claude")
		command.Dir = directory
		command.Env = os.Environ()
		command.Stdin = input
		command.Stdout = output
		command.Stderr = output
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		process := &foregroundProcess{command: command, input: input, keepOpen: keepOpen, output: output}
		t.Cleanup(func() {
			_ = input.Close()
			_ = keepOpen.Close()
			if !process.waited && command.Process != nil {
				_ = command.Process.Kill()
				_ = command.Wait()
			}
		})
		return process
	}
	waitRunningRoles := func(count int) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for {
			result := fixture.runAgentctl("status", "--session", "direct-roster", "--json")
			if result.exitCode == exitOK && strings.Count(result.stdout, `"state":"running"`) == count {
				return
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("direct roster did not reach %d running roles: %#v", count, result)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}

	first := start("planner", project)
	waitRunningRoles(1)
	second := start("coder", project)
	waitRunningRoles(2)
	fixture.waitStubInvocations(2)

	other := t.TempDir()
	refused := exec.Command(scriptPath, "-q", "/dev/null", fixture.agentctlPath,
		"run", "--session", "direct-roster", "--role", "reviewer", "--harness", "claude")
	refused.Dir = other
	refused.Env = os.Environ()
	refusalOutput, refusalErr := refused.CombinedOutput()
	var refusalExit *exec.ExitError
	if !errors.As(refusalErr, &refusalExit) || refusalExit.ExitCode() != exitUnsafe {
		t.Fatalf("different-directory run error = %v output=%q, want exit %d", refusalErr, refusalOutput, exitUnsafe)
	}
	for _, want := range []string{project, other, "fleet-directory-disagreement", "no role was started or durable record mutated"} {
		if !strings.Contains(string(refusalOutput), want) {
			t.Fatalf("different-directory refusal %q omits %q", refusalOutput, want)
		}
	}
	if got := len(fixture.stubInvocations()); got != 2 {
		t.Fatalf("stub invocations after refused extension = %d, want 2", got)
	}

	if killed := fixture.runAgentctl("kill", "--session", "direct-roster"); killed.exitCode != exitOK {
		t.Fatalf("kill extended foreground roster = %#v", killed)
	}
	for _, process := range []*foregroundProcess{first, second} {
		if err := process.command.Wait(); err != nil {
			t.Fatalf("foreground process exit: %v; output=%q", err, process.output.String())
		}
		process.waited = true
	}
}

func TestIntegrationPublicShimCommandsSurvivePresentationLayoutChanges(t *testing.T) {
	fixture := newIntegrationFixture(t)
	launched := fixture.runAgentctl("launch", "--session", "public-layout", "--roles", "planner:claude,coder:codex")
	if launched.exitCode != exitOK || launched.stderr != "agentctl: launched session \"public-layout\"; 2 roles are ready\n" {
		t.Fatalf("launch = %#v", launched)
	}
	fixture.waitStubInvocations(2)
	fixture.waitRoleMarkers("planner", "coder")

	session := fixture.presentationSession("public-layout")
	panes := fixture.shimPresentationPanes(session.ID)
	fixture.tmuxOutput("join-pane", "-d", "-s", string(panes["coder"].ID), "-t", string(panes["planner"].ID))

	cleared := fixture.runAgentctl("clear", "--session", "public-layout", "coder")
	if cleared.exitCode != exitOK || cleared.stderr != "agentctl: clear for role \"coder\" in session \"public-layout\" wrote 6 bytes and observed submit\n" {
		t.Fatalf("clear after join-pane = %#v", cleared)
	}
	fixture.waitRoleInput("coder", "\x15/clear\n")

	statusResult := fixture.runAgentctl("status", "--session", "public-layout", "--json")
	if statusResult.exitCode != exitOK || statusResult.stderr != "" {
		t.Fatalf("status after join-pane = %#v", statusResult)
	}
	var report statuspkg.ShimReport
	if err := json.Unmarshal([]byte(statusResult.stdout), &report); err != nil {
		t.Fatalf("decode public status %q: %v", statusResult.stdout, err)
	}
	if len(report.Agents) != 2 || report.Agents[0].State != statuspkg.RuntimeStateRunning || report.Agents[1].State != statuspkg.RuntimeStateRunning {
		t.Fatalf("public status = %#v, want two runtime-running roles", report)
	}
}

func TestIntegrationPublicCommandsConsultRuntimeAnchorBeforeReportingDivergentStateRootMissing(t *testing.T) {
	fixture := newIntegrationFixture(t)
	launched := fixture.runAgentctl("launch", "--session", "root-guard", "--roles", "planner:claude")
	if launched.exitCode != exitOK {
		t.Fatalf("launch = %#v", launched)
	}
	fixture.waitStubInvocations(1)

	wrongRoot := filepath.Join(filepath.Dir(fixture.stateRoot), "wrong-state")
	if err := os.Mkdir(wrongRoot, 0o700); err != nil {
		t.Fatalf("create divergent state root: %v", err)
	}
	localRoot, err := filepath.EvalSymlinks(wrongRoot)
	if err != nil {
		t.Fatalf("resolve divergent state root: %v", err)
	}
	recordedRoot, err := filepath.EvalSymlinks(fixture.stateRoot)
	if err != nil {
		t.Fatalf("resolve recorded state root: %v", err)
	}
	t.Setenv("AGENTCTL_STATE_ROOT", wrongRoot)
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	previousPane, paneWasSet := os.LookupEnv("TMUX_PANE")
	if err := os.Unsetenv("TMUX_PANE"); err != nil {
		t.Fatalf("unset TMUX_PANE for attach refusal: %v", err)
	}
	t.Cleanup(func() {
		if paneWasSet {
			_ = os.Setenv("TMUX_PANE", previousPane)
		} else {
			_ = os.Unsetenv("TMUX_PANE")
		}
	})

	for _, test := range []struct {
		name      string
		arguments []string
		operation string
	}{
		{name: "clear", arguments: []string{"clear", "--session", "root-guard", "planner"}, operation: "clear"},
		{name: "compact", arguments: []string{"compact", "--session", "root-guard", "planner"}, operation: "compact"},
		{name: "relaunch", arguments: []string{"relaunch", "--session", "root-guard", "planner"}, operation: "relaunch"},
		{name: "foreground run", arguments: []string{"run", "--session", "root-guard", "--role", "planner", "--harness", "claude"}, operation: "run"},
		{name: "selected status", arguments: []string{"status", "--session", "root-guard"}, operation: "status"},
		{name: "kill", arguments: []string{"kill", "--session", "root-guard"}, operation: "kill"},
		{name: "attach", arguments: []string{"attach", "--session", "root-guard"}, operation: "attach"},
		{name: "same session launch", arguments: []string{"launch", "--session", "root-guard", "--roles", "planner:claude"}, operation: "launch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := fixture.runAgentctl(test.arguments...)
			want := fmt.Sprintf("agentctl: refusing to %s role %q in session %q; resolved state root %q differs from lockfile-recorded state root %q (state-root-disagreement)\n", test.operation, "planner", "root-guard", localRoot, recordedRoot)
			if result.exitCode != exitUnsafe || result.stdout != "" || result.stderr != want {
				t.Fatalf("result = %#v, want exit %d and %q", result, exitUnsafe, want)
			}
			for _, falseClaim := range []string{"missing", "no durable fleet configuration", "unclassified"} {
				if strings.Contains(result.stderr, falseClaim) {
					t.Fatalf("stderr %q contains forbidden divergent-root claim %q", result.stderr, falseClaim)
				}
			}
		})
	}
	if got := len(fixture.stubInvocations()); got != 1 {
		t.Fatalf("stub harness invocations after refusals = %d, want unchanged one child", got)
	}

	t.Setenv("AGENTCTL_STATE_ROOT", fixture.stateRoot)
	statusResult := fixture.runAgentctl("status", "--session", "root-guard", "--json")
	if statusResult.exitCode != exitOK || !strings.Contains(statusResult.stdout, `"state":"running"`) {
		t.Fatalf("status after restoring state root = %#v, want original live child", statusResult)
	}
	if killed := fixture.runAgentctl("kill", "--session", "root-guard"); killed.exitCode != exitOK {
		t.Fatalf("kill after restoring state root = %#v", killed)
	}
}

func TestIntegrationPublicAttachRefusesAbsentPresentationForDurableFleet(t *testing.T) {
	fixture := newIntegrationFixtureWithoutServer(t)
	_, records, _, _ := fixture.shimStack(t)
	record, err := fleet.NewShimFleetRecord("no-ui", fixture.captureDir, fleet.PresentationTmux, config.FleetConfig{Roles: []config.RoleConfig{{Name: "planner", Harness: config.HarnessClaude}}})
	if err != nil {
		t.Fatalf("NewShimFleetRecord() error = %v", err)
	}
	if err := records.Create(record); err != nil {
		t.Fatalf("Create() durable fleet error = %v", err)
	}
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	if err := os.Unsetenv("TMUX_PANE"); err != nil {
		t.Fatal(err)
	}
	result := fixture.runAgentctl("attach", "--session", "no-ui")
	want := "agentctl: refusing to attach session \"no-ui\"; no tmux presentation was observed; status and control remain available without tmux\n"
	if result.exitCode != exitSession || result.stdout != "" || result.stderr != want {
		t.Fatalf("attach without presentation = %#v, want exit 3 and %q", result, want)
	}
}

func TestIntegrationReleaseCandidateStatusReportsUnanchoredDurableRecord(t *testing.T) {
	if os.Getenv(integrationCandidateEnv) == "" {
		t.Skip("release-candidate routing is enabled only by the Task 8 walkthrough")
	}
	fixture := newIntegrationFixtureWithoutServer(t)
	namespace, records, _, _ := fixture.shimStack(t)
	record, err := fleet.NewShimFleetRecord("unanchored", fixture.captureDir, fleet.PresentationTmux, config.FleetConfig{Roles: []config.RoleConfig{{Name: "planner", Harness: config.HarnessClaude}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := records.Create(record); err != nil {
		t.Fatal(err)
	}
	path, err := namespace.RolePath("unanchored", "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := shim.WriteRecord(path, shim.NewChildStartingRecord("unanchored", "planner", os.Getpid(), "durable-only")); err != nil {
		_ = path.Close()
		t.Fatal(err)
	}
	if err := path.Close(); err != nil {
		t.Fatal(err)
	}

	result := fixture.runAgentctl("status", "--session", "unanchored", "--json")
	if result.exitCode != exitOK || result.stderr != "" {
		t.Fatalf("unanchored candidate status = %#v", result)
	}
	if !strings.Contains(result.stdout, `"confidence":"unanchored"`) || strings.Contains(result.stdout, `"confidence":"anchored"`) {
		t.Fatalf("unanchored candidate status = %q", result.stdout)
	}
}

func TestIntegrationReleaseCandidateLayoutOperationsPreserveCLIIdentityAndDelivery(t *testing.T) {
	if os.Getenv(integrationCandidateEnv) == "" {
		t.Skip("release-candidate routing is enabled only by the Task 8 walkthrough")
	}
	fixture := newIntegrationFixture(t)
	launched := fixture.runAgentctl("launch", "--session", "candidate-layout", "--roles", "planner:claude,coder:codex")
	if launched.exitCode != exitOK {
		t.Fatalf("candidate launch = %#v", launched)
	}
	fixture.waitStubInvocations(2)
	fixture.waitRoleMarkers("planner", "coder")
	report := func() statuspkg.ShimReport {
		t.Helper()
		result := fixture.runAgentctl("status", "--session", "candidate-layout", "--json")
		if result.exitCode != exitOK || result.stderr != "" {
			t.Fatalf("candidate status = %#v", result)
		}
		var decoded statuspkg.ShimReport
		if err := json.Unmarshal([]byte(result.stdout), &decoded); err != nil {
			t.Fatal(err)
		}
		return decoded
	}
	initial := report()
	identities := map[string][2]int{}
	for _, agent := range initial.Agents {
		if agent.ShimPID <= 0 || agent.ChildPID <= 0 || agent.State != statuspkg.RuntimeStateRunning {
			t.Fatalf("initial candidate status agent = %#v", agent)
		}
		identities[agent.Role] = [2]int{agent.ShimPID, agent.ChildPID}
	}
	assertIdentity := func(stage string) {
		t.Helper()
		observed := report()
		if len(observed.Agents) != 2 {
			t.Fatalf("%s candidate agents = %#v", stage, observed.Agents)
		}
		for _, agent := range observed.Agents {
			want, ok := identities[agent.Role]
			if !ok || agent.ShimPID <= 0 || agent.ChildPID <= 0 || agent.State != statuspkg.RuntimeStateRunning ||
				agent.ShimPID != want[0] || agent.ChildPID != want[1] {
				t.Fatalf("%s candidate identity = %#v, want %#v", stage, agent, want)
			}
		}
	}
	deliver := func(stage, operation, role, wantInput string) {
		t.Helper()
		result := fixture.runAgentctl(operation, "--session", "candidate-layout", role)
		if result.exitCode != exitOK || !strings.Contains(result.stderr, "observed submit") {
			t.Fatalf("%s candidate %s = %#v", stage, operation, result)
		}
		fixture.waitRoleInput(role, wantInput)
	}

	session := fixture.presentationSession("candidate-layout")
	panes := fixture.shimPresentationPanes(session.ID)
	plannerPane := panes["planner"]
	coderPane := panes["coder"]
	fixture.tmuxOutput("join-pane", "-d", "-s", string(coderPane.ID), "-t", string(plannerPane.ID))
	assertIdentity("after join-pane")
	deliver("after join-pane", "clear", "planner", "\x15/clear\n")
	brokenWindow := trimIntegrationLine(string(fixture.tmuxOutput("break-pane", "-d", "-s", string(coderPane.ID), "-P", "-F", "#{window_id}")))
	assertIdentity("after break-pane")
	deliver("after break-pane", "compact", "coder", "\x15/compact\n")
	fixture.tmuxOutput("swap-pane", "-d", "-s", string(coderPane.ID), "-t", string(plannerPane.ID))
	assertIdentity("after swap-pane")
	deliver("after swap-pane", "compact", "planner", "\x15/clear\n\x15/compact\n")
	fixture.tmuxOutput("move-window", "-s", brokenWindow, "-t", string(session.ID)+":9")
	assertIdentity("after move-window")
	deliver("after move-window", "clear", "coder", "\x15/compact\n\x15/clear\n")

	if killed := fixture.runAgentctl("kill", "--session", "candidate-layout"); killed.exitCode != exitOK {
		t.Fatalf("candidate layout kill = %#v", killed)
	}
}

func TestIntegrationReleaseCandidateCrashRelaunchAndKillUseObservedAbsence(t *testing.T) {
	if os.Getenv(integrationCandidateEnv) == "" {
		t.Skip("release-candidate routing is enabled only by the Task 8 walkthrough")
	}
	fixture := newIntegrationFixture(t)
	launched := fixture.runAgentctl("launch", "--session", "candidate-relaunch", "--roles", "planner:claude")
	if launched.exitCode != exitOK {
		t.Fatalf("candidate launch = %#v", launched)
	}
	fixture.waitStubInvocations(1)
	statusAgent := func() (statuspkg.ShimAgent, integrationResult) {
		t.Helper()
		result := fixture.runAgentctl("status", "--session", "candidate-relaunch", "--json")
		if result.stdout == "" {
			return statuspkg.ShimAgent{}, result
		}
		var report statuspkg.ShimReport
		if err := json.Unmarshal([]byte(result.stdout), &report); err != nil || len(report.Agents) != 1 {
			t.Fatalf("decode candidate status %q: %v", result.stdout, err)
		}
		return report.Agents[0], result
	}
	initial, initialResult := statusAgent()
	if initialResult.exitCode != exitOK || initial.ShimPID <= 0 || initial.ChildPID <= 0 || initial.State != statuspkg.RuntimeStateRunning {
		t.Fatalf("initial candidate status = %#v, %#v", initial, initialResult)
	}
	if err := syscall.Kill(initial.ShimPID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	var crashed statuspkg.ShimAgent
	childAbsent := false
	for time.Now().Before(deadline) {
		crashed, _ = statusAgent()
		if crashed.State == statuspkg.RuntimeStateMissing {
			t.Fatalf("candidate called crashed role missing before relaunch: %#v", crashed)
		}
		if err := syscall.Kill(initial.ChildPID, 0); errors.Is(err, syscall.ESRCH) {
			childAbsent = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !childAbsent {
		t.Fatalf("crashed candidate child PID %d was not observed ESRCH; last status %#v", initial.ChildPID, crashed)
	}

	relaunched := fixture.runAgentctl("relaunch", "--session", "candidate-relaunch", "planner")
	if relaunched.exitCode != exitOK {
		t.Fatalf("candidate relaunch = %#v", relaunched)
	}
	fixture.waitStubInvocations(2)
	running, runningResult := statusAgent()
	if runningResult.exitCode != exitOK || running.State != statuspkg.RuntimeStateRunning || running.ShimPID <= 0 || running.ChildPID <= 0 || running.ShimPID == initial.ShimPID {
		t.Fatalf("candidate status after relaunch = %#v, %#v", running, runningResult)
	}
	if killed := fixture.runAgentctl("kill", "--session", "candidate-relaunch"); killed.exitCode != exitOK {
		t.Fatalf("candidate kill = %#v", killed)
	}
	if err := syscall.Kill(running.ChildPID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("candidate child after kill(%d, 0) = %v, want ESRCH", running.ChildPID, err)
	}
	if fixture.hasSession("candidate-relaunch") {
		t.Fatal("candidate presentation survived observed kill cleanup")
	}
	after := fixture.runAgentctl("status", "--session", "candidate-relaunch")
	if after.exitCode != exitSession || !strings.Contains(after.stderr, "has no durable fleet configuration") {
		t.Fatalf("candidate status after kill = %#v", after)
	}
}

func TestIntegrationShimPresentationTreatsPrivateTmuxSocketAbsenceAsGone(t *testing.T) {
	fixture := newIntegrationFixtureWithoutServer(t)
	got, present, err := fixture.client.FindPresentationSession(context.Background(), "shim-no-presentation")
	if err != nil || present || got != (tmuxx.Session{}) {
		t.Fatalf("FindPresentationSession() = %#v, %t, %v, want factual presentation absence", got, present, err)
	}
}

func TestIntegrationShimPresentationLayoutDoesNotChangeRuntimeIdentityOrDelivery(t *testing.T) {
	fixture := newIntegrationFixture(t)
	namespace, records, lifecycle, inspector := fixture.shimStack(t)
	_ = namespace
	launcher := fleet.NewShimLauncher(fixture.client, lifecycle, records, fixture.shimLaunchDependencies())
	fleetConfig := config.FleetConfig{Roles: []config.RoleConfig{
		{Name: "planner", Harness: config.HarnessClaude},
		{Name: "coder", Harness: config.HarnessCodex},
	}}
	launched, err := launcher.Launch(context.Background(), "shim-layout", fleetConfig, nil)
	if err != nil {
		t.Fatalf("shim Launch() error = %v", err)
	}
	fixture.waitStubInvocations(2)
	fixture.waitRoleMarkers("planner", "coder")

	initial := map[string]shim.Response{}
	for _, role := range []string{"planner", "coder"} {
		response, err := lifecycle.Observe(context.Background(), "shim-layout", role)
		if err != nil {
			t.Fatalf("initial Observe(%s) error = %v", role, err)
		}
		initial[role] = response
	}
	panes := fixture.shimPresentationPanes(launched.Session.ID)
	plannerPane := panes["planner"]
	coderPane := panes["coder"]
	assertShimIdentity := func(stage string) {
		t.Helper()
		for _, role := range []string{"planner", "coder"} {
			response, err := lifecycle.Observe(context.Background(), "shim-layout", role)
			if err != nil {
				t.Fatalf("%s Observe(%s) error = %v", stage, role, err)
			}
			if response.Outcome != shim.OutcomeRunning || response.ShimPID == nil || response.ChildPID == nil ||
				initial[role].ShimPID == nil || initial[role].ChildPID == nil ||
				*response.ShimPID != *initial[role].ShimPID || *response.ChildPID != *initial[role].ChildPID {
				t.Fatalf("%s Observe(%s) = %#v, want unchanged runtime identity %#v", stage, role, response, initial[role])
			}
		}
	}

	fixture.tmuxOutput("join-pane", "-d", "-s", string(coderPane.ID), "-t", string(plannerPane.ID))
	assertShimIdentity("after join-pane")
	brokenWindow := string(fixture.tmuxOutput("break-pane", "-d", "-s", string(coderPane.ID), "-P", "-F", "#{window_id}"))
	brokenWindow = trimIntegrationLine(brokenWindow)
	assertShimIdentity("after break-pane")
	fixture.tmuxOutput("swap-pane", "-d", "-s", string(coderPane.ID), "-t", string(plannerPane.ID))
	assertShimIdentity("after swap-pane")
	fixture.tmuxOutput("move-window", "-s", brokenWindow, "-t", string(launched.Session.ID)+":9")
	assertShimIdentity("after move-window")

	for _, role := range []string{"planner", "coder"} {
		response, err := lifecycle.DeliverOperation(context.Background(), "shim-layout", role, "clear")
		if err != nil {
			t.Fatalf("DeliverOperation(%s) error = %v", role, err)
		}
		if response.Outcome != shim.OutcomeDeliverySubmitted || response.SubmitObserved == nil || !*response.SubmitObserved {
			t.Fatalf("DeliverOperation(%s) = %#v, want submitted", role, response)
		}
		fixture.waitRoleInput(role, "\x15/clear\n")
	}

	// Keep the inspector live in this test's direct stack: presentation never
	// participates in its runtime decision.
	if observed, err := inspector.Inspect(context.Background(), "shim-layout", "planner"); err != nil || observed.Outcome != shim.OutcomeRunning {
		t.Fatalf("runtime inspector after layout = %#v, %v, want running", observed, err)
	}
}

func TestIntegrationShimSIGKILLLeavesApprovedRecordStateAndConcurrentRelaunchStartsOneChild(t *testing.T) {
	fixture := newIntegrationFixture(t)
	fixture.createSentinelSession("shim-race-sentinel")
	_, records, lifecycle, inspector := fixture.shimStack(t)
	launcher := fleet.NewShimLauncher(fixture.client, lifecycle, records, fixture.shimLaunchDependencies())
	fleetConfig := config.FleetConfig{Roles: []config.RoleConfig{{Name: "planner", Harness: config.HarnessClaude}}}
	launched, err := launcher.Launch(context.Background(), "shim-race", fleetConfig, nil)
	if err != nil {
		t.Fatalf("shim Launch() error = %v", err)
	}
	fixture.waitStubInvocations(1)
	initial, err := lifecycle.Observe(context.Background(), "shim-race", "planner")
	if err != nil || initial.ShimPID == nil || initial.ChildPID == nil {
		t.Fatalf("initial Observe() = %#v, %v, want running identity", initial, err)
	}
	if err := syscall.Kill(*initial.ShimPID, syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL owned shim PID %d: %v", *initial.ShimPID, err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var state fleet.ShimRoleObservation
	for time.Now().Before(deadline) {
		state, err = inspector.Inspect(context.Background(), "shim-race", "planner")
		if err != nil {
			t.Fatalf("Inspect() after shim SIGKILL error = %v", err)
		}
		if state.Outcome != shim.OutcomeCouldNotObserve && state.Outcome != shim.OutcomeOrphan && state.Outcome != shim.OutcomeStaleRecord {
			t.Fatalf("Inspect() after shim SIGKILL = %#v, want approved could-not-observe/orphan/stale-record state", state)
		}
		if state.Outcome == shim.OutcomeStaleRecord {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if state.Outcome != shim.OutcomeStaleRecord {
		t.Fatalf("child PID %d did not reach ESRCH-backed stale-record after shim SIGKILL: %#v", *initial.ChildPID, state)
	}
	if fixture.hasSession("shim-race") {
		t.Fatal("SIGKILLed shim presentation still exists; test requires presentation-gone relaunch")
	}
	if !fixture.hasSession("shim-race-sentinel") {
		t.Fatal("removing stale shim presentation changed the throwaway sentinel session")
	}

	relauncher := fleet.NewShimRelauncher(fixture.client, lifecycle, records, inspector, fixture.shimLaunchDependencies())
	start := make(chan struct{})
	type relaunchResult struct {
		result fleet.ShimRelaunchResult
		err    error
	}
	results := make(chan relaunchResult, 2)
	var contenders sync.WaitGroup
	for index := 0; index < 2; index++ {
		contenders.Add(1)
		go func() {
			defer contenders.Done()
			<-start
			result, err := relauncher.Relaunch(context.Background(), "shim-race", fleet.RelaunchRequest{Role: "planner"})
			results <- relaunchResult{result: result, err: err}
		}()
	}
	close(start)
	contenders.Wait()
	close(results)
	successes := 0
	for result := range results {
		if result.err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent relaunch successes = %d, want one kernel winner", successes)
	}
	fixture.waitStubInvocations(2)
	time.Sleep(300 * time.Millisecond)
	if got := len(fixture.stubInvocations()); got != 2 {
		t.Fatalf("stub harness invocations = %d, want original plus exactly one relaunch", got)
	}
	relaunched, err := lifecycle.Observe(context.Background(), "shim-race", "planner")
	if err != nil || relaunched.Outcome != shim.OutcomeRunning || relaunched.ShimPID == nil || *relaunched.ShimPID == *initial.ShimPID {
		t.Fatalf("Observe() after concurrent relaunch = %#v, %v, want one new running shim", relaunched, err)
	}

	if launched.TotalRoles != 1 {
		t.Fatalf("initial launch result = %#v, want one role", launched)
	}
}

func TestIntegrationShimKillObservesChildExitBeforePresentationAndFleetCleanup(t *testing.T) {
	fixture := newIntegrationFixture(t)
	_, records, lifecycle, inspector := fixture.shimStack(t)
	launcher := fleet.NewShimLauncher(fixture.client, lifecycle, records, fixture.shimLaunchDependencies())
	fleetConfig := config.FleetConfig{Roles: []config.RoleConfig{{Name: "planner", Harness: config.HarnessClaude}}}
	launched, err := launcher.Launch(context.Background(), "shim-kill", fleetConfig, nil)
	if err != nil {
		t.Fatalf("shim Launch() error = %v", err)
	}
	fixture.waitStubInvocations(1)
	before, err := lifecycle.Observe(context.Background(), "shim-kill", "planner")
	if err != nil || before.ChildPID == nil {
		t.Fatalf("Observe() before kill = %#v, %v, want child identity", before, err)
	}

	executor := kill.NewShimExecutor(lifecycle, records, inspector, fixture.client)
	result, err := executor.Execute(context.Background(), "shim-kill")
	if err != nil {
		t.Fatalf("shim kill Execute() error = %v", err)
	}
	if result.StoppedRoles != 1 || result.PresentationRemoved == result.PresentationGone {
		t.Fatalf("shim kill Execute() = %#v, want one observed exit and exactly one presentation removed/gone fact", result)
	}
	if fixture.hasSession("shim-kill") {
		t.Fatal("shim presentation remains after observed kill")
	}
	if _, err := records.Read("shim-kill"); err == nil {
		t.Fatal("durable fleet record remains after complete kill")
	}
	if err := syscall.Kill(*before.ChildPID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("child PID %d after complete kill: kill(pid, 0) = %v, want ESRCH", *before.ChildPID, err)
	}
	if launched.TotalRoles != 1 {
		t.Fatalf("launch result = %#v, want one role", launched)
	}
}

func TestIntegrationShimStopLetsInflightPayloadReportAndRefusesLaterPayload(t *testing.T) {
	fixture := newIntegrationFixture(t)
	_, records, lifecycle, _ := fixture.shimStack(t)
	launcher := fleet.NewShimLauncher(fixture.client, lifecycle, records, fixture.shimLaunchDependencies())
	fleetConfig := config.FleetConfig{Roles: []config.RoleConfig{{Name: "planner", Harness: config.HarnessClaude}}}
	if _, err := launcher.Launch(context.Background(), "shim-stop-order", fleetConfig, nil); err != nil {
		t.Fatalf("shim Launch() error = %v", err)
	}
	fixture.waitStubInvocations(1)
	fixture.waitRoleMarkers("planner")

	type operationResult struct {
		response shim.Response
		err      error
	}
	clearDone := make(chan operationResult, 1)
	go func() {
		response, err := lifecycle.DeliverOperation(context.Background(), "shim-stop-order", "planner", "clear")
		clearDone <- operationResult{response: response, err: err}
	}()
	// The payload operation's closed one-second submit delay provides a live
	// window in which stop can publish its stopping phase.
	time.Sleep(200 * time.Millisecond)
	stopDone := make(chan operationResult, 1)
	go func() {
		response, err := lifecycle.Stop(context.Background(), "shim-stop-order", "planner")
		stopDone <- operationResult{response: response, err: err}
	}()

	deadline := time.Now().Add(2 * time.Second)
	var observed shim.Response
	for time.Now().Before(deadline) {
		response, err := lifecycle.Observe(context.Background(), "shim-stop-order", "planner")
		if err == nil && response.Outcome == shim.OutcomeStopping {
			observed = response
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if observed.Outcome != shim.OutcomeStopping || observed.ShimPID == nil || observed.ChildPID == nil {
		t.Fatalf("Observe() during serialized stop = %#v, want stopping runtime facts", observed)
	}
	refused, err := lifecycle.DeliverOperation(context.Background(), "shim-stop-order", "planner", "compact")
	if err != nil {
		t.Fatalf("compact during stop error = %v", err)
	}
	if refused.Outcome != shim.OutcomeShimStopping || refused.State == nil || *refused.State != "stopping" {
		t.Fatalf("compact during stop = %#v, want shim-stopping refusal", refused)
	}
	secondStop, err := lifecycle.Stop(context.Background(), "shim-stop-order", "planner")
	if err != nil {
		t.Fatalf("second stop error = %v", err)
	}
	if secondStop.Outcome != shim.OutcomeStopAlreadyStopping || secondStop.SignalAttempted == nil || *secondStop.SignalAttempted {
		t.Fatalf("second stop = %#v, want stop-already-stopping without signal", secondStop)
	}

	clearResult := <-clearDone
	if clearResult.err != nil || clearResult.response.Outcome != shim.OutcomeDeliverySubmitted {
		t.Fatalf("in-flight clear = %#v, %v, want reported delivery", clearResult.response, clearResult.err)
	}
	stopResult := <-stopDone
	if stopResult.err != nil || stopResult.response.Outcome != shim.OutcomeStopChildExited ||
		stopResult.response.SignalAttempted == nil || !*stopResult.response.SignalAttempted ||
		stopResult.response.ChildExitObserved == nil || !*stopResult.response.ChildExitObserved {
		t.Fatalf("primary stop = %#v, %v, want separate signal/exit facts", stopResult.response, stopResult.err)
	}
}

func (f *integrationFixture) shimStack(t *testing.T) (*shim.Namespace, *fleet.ShimFleetRecordStore, *shim.Client, *fleet.RuntimeShimRoleInspector) {
	t.Helper()
	namespace, err := shim.OpenNamespace()
	if err != nil {
		t.Fatalf("OpenNamespace() error = %v", err)
	}
	t.Cleanup(func() { _ = namespace.Close() })
	records, err := fleet.OpenShimFleetRecordStore(namespace.StateRoot)
	if err != nil {
		t.Fatalf("OpenShimFleetRecordStore() error = %v", err)
	}
	t.Cleanup(func() { _ = records.Close() })
	lifecycle := shim.NewClient(namespace)
	inspector := fleet.NewRuntimeShimRoleInspector(namespace, lifecycle)
	return namespace, records, lifecycle, inspector
}

func (f *integrationFixture) shimLaunchDependencies() fleet.ShimLaunchDependencies {
	return fleet.ShimLaunchDependencies{
		LookPath: execLookPathIntegration,
		Executable: func() (string, error) {
			return f.agentctlPath, nil
		},
		Getwd: func() (string, error) { return f.captureDir, nil },
		Stat:  os.Stat,
	}
}

func execLookPathIntegration(name string) (string, error) {
	return exec.LookPath(name)
}

func (f *integrationFixture) shimPresentationPanes(sessionID tmuxx.SessionID) map[string]tmuxx.Pane {
	f.t.Helper()
	result := make(map[string]tmuxx.Pane)
	for _, window := range f.windows(sessionID) {
		panes := f.panes(window.ID)
		if len(panes) != 1 {
			f.t.Fatalf("presentation window %s panes = %#v, want one", window.ID, panes)
		}
		result[window.Name] = panes[0]
	}
	for _, role := range []string{"planner", "coder"} {
		if result[role].ID == "" {
			f.t.Fatalf("presentation panes = %#v, missing role %q", result, role)
		}
	}
	return result
}

func trimIntegrationLine(value string) string {
	for len(value) > 0 && (value[len(value)-1] == '\n' || value[len(value)-1] == '\r') {
		value = value[:len(value)-1]
	}
	return value
}
