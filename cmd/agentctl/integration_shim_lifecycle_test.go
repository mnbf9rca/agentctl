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

func TestIntegrationPublicAttachRefusesAbsentPresentationForDurableFleet(t *testing.T) {
	fixture := newIntegrationFixtureWithoutServer(t)
	_, records, _, _ := fixture.shimStack(t)
	record, err := fleet.NewShimFleetRecord("no-ui", fixture.captureDir, config.FleetConfig{Roles: []config.RoleConfig{{Name: "planner", Harness: config.HarnessClaude}}})
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
