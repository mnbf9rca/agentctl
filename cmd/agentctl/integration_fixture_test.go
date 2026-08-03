//go:build integration

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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

	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

const (
	integrationMarkerEnv  = "AGENTCTL_INTEGRATION_MARKER"
	integrationCaptureEnv = "AGENTCTL_INTEGRATION_CAPTURE"
)

type integrationResult struct {
	exitCode int
	stdout   string
	stderr   string
}

type integrationSession struct {
	ID      tmuxx.SessionID
	Name    string
	Managed string
	Version string
	Roles   string
}

type integrationWindow struct {
	tmuxx.Window
	Directory string
}

type stubInvocation struct {
	Session     string
	Role        string
	Harness     string
	Model       string
	Effort      string
	Environment stubEnvironment
}

// stubEnvironment is the identity environment the launched pane process itself
// observes, which is what an agent running there would read.
type stubEnvironment struct {
	Session string
	Role    string
	Managed string
}

type sentinelSnapshot struct {
	Session integrationSession
	Window  integrationWindow
	Pane    tmuxx.Pane
	Process string
}

type integrationFixture struct {
	t           *testing.T
	runner      *socketRunner
	client      tmuxx.Client
	invocations string
	captureDir  string
}

type socketRunner struct {
	tmuxPath      string
	socket        string
	tmuxTmpDir    string
	failureMu     sync.Mutex
	failOperation string
}

func TestIntegrationFixtureRemovesSocketDirectory(t *testing.T) {
	var socketDirectory string
	t.Run("fixture lifetime", func(t *testing.T) {
		fixture := newIntegrationFixture(t)
		socketDirectory = fixture.runner.tmuxTmpDir
		if _, err := os.Stat(socketDirectory); err != nil {
			t.Fatalf("private tmux socket directory during test: %v", err)
		}
	})
	if _, err := os.Stat(socketDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private tmux socket directory after cleanup: %v", err)
	}
}

func (r *socketRunner) Output(ctx context.Context, executable string, args ...string) ([]byte, error) {
	if executable == "tmux" {
		r.failureMu.Lock()
		shouldFail := len(args) > 0 && args[0] == r.failOperation
		if shouldFail {
			r.failOperation = ""
		}
		r.failureMu.Unlock()
		if shouldFail {
			return nil, errors.New("injected tmux operation failure")
		}
		return r.tmuxCommand(ctx, args...).Output()
	}
	return exec.CommandContext(ctx, executable, args...).Output()
}

func (r *socketRunner) RunInteractive(ctx context.Context, executable string, args ...string) error {
	var command *exec.Cmd
	if executable == "tmux" {
		command = r.tmuxCommand(ctx, args...)
	} else {
		command = exec.CommandContext(ctx, executable, args...)
	}
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func (r *socketRunner) tmuxCommand(ctx context.Context, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, r.tmuxPath, append([]string{"-L", r.socket}, args...)...)
	command.Env = environmentWith(os.Environ(), "TMUX_TMPDIR", r.tmuxTmpDir)
	return command
}

func environmentWith(environment []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func (r *socketRunner) failNextTmuxOperation(operation string) {
	r.failureMu.Lock()
	defer r.failureMu.Unlock()
	r.failOperation = operation
}

func TestMain(m *testing.M) {
	switch filepath.Base(os.Args[0]) {
	case "amq":
		integrationAMQMain()
		return
	case "claude", "codex":
		integrationMarkerMain()
		return
	}
	if os.Getenv(integrationMarkerEnv) == "1" {
		integrationMarkerMain()
		return
	}
	os.Exit(m.Run())
}

func integrationAMQMain() {
	arguments := append([]string(nil), os.Args[1:]...)
	if len(arguments) < 2 || arguments[0] != "coop" || arguments[1] != "exec" {
		fmt.Fprintln(os.Stderr, "integration amq stub: expected coop exec")
		os.Exit(81)
	}
	arguments = arguments[2:]
	session := ""
	role := ""
	for len(arguments) > 0 {
		switch arguments[0] {
		case "--session":
			if len(arguments) < 2 {
				fmt.Fprintln(os.Stderr, "integration amq stub: missing session")
				os.Exit(82)
			}
			session = arguments[1]
			arguments = arguments[2:]
		case "--me":
			if len(arguments) < 2 {
				fmt.Fprintln(os.Stderr, "integration amq stub: missing role")
				os.Exit(83)
			}
			role = arguments[1]
			arguments = arguments[2:]
		default:
			goto parsedOptions
		}
	}

parsedOptions:
	if session == "" || role == "" || len(arguments) == 0 {
		fmt.Fprintln(os.Stderr, "integration amq stub: incomplete launch")
		os.Exit(84)
	}
	harness := arguments[0]
	arguments = arguments[1:]
	if harness != "claude" && harness != "codex" {
		fmt.Fprintln(os.Stderr, "integration amq stub: unsupported harness")
		os.Exit(85)
	}
	model := ""
	effort := ""
	harnessArguments := []string(nil)
	if len(arguments) != 0 {
		if arguments[0] != "--" || len(arguments) == 1 {
			fmt.Fprintln(os.Stderr, "integration amq stub: unexpected harness arguments")
			os.Exit(86)
		}
		harnessArguments = arguments[1:]
		remaining := harnessArguments
		for len(remaining) != 0 {
			if len(remaining) < 2 || remaining[1] == "" {
				fmt.Fprintln(os.Stderr, "integration amq stub: unexpected harness arguments")
				os.Exit(86)
			}
			switch remaining[0] {
			case "--model":
				model = remaining[1]
			case "--effort":
				effort = remaining[1]
			case "--config":
				value := strings.TrimPrefix(remaining[1], `model_reasoning_effort="`)
				if value == remaining[1] || !strings.HasSuffix(value, `"`) {
					fmt.Fprintln(os.Stderr, "integration amq stub: unexpected harness arguments")
					os.Exit(86)
				}
				effort = strings.TrimSuffix(value, `"`)
			default:
				fmt.Fprintln(os.Stderr, "integration amq stub: unexpected harness arguments")
				os.Exit(86)
			}
			remaining = remaining[2:]
		}
	}

	record, err := os.OpenFile(os.Getenv("AGENTCTL_STUB_INVOCATIONS"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(87)
	}
	if _, err := fmt.Fprintf(record, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", session, role, harness, model, effort,
		os.Getenv("AGENTCTL_SESSION"), os.Getenv("AGENTCTL_ROLE"), os.Getenv("AGENTCTL_MANAGED")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(88)
	}
	if err := record.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(89)
	}

	harnessPath, err := exec.LookPath(harness)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(90)
	}
	os.Setenv("AGENTCTL_STUB_ROLE", role)
	os.Setenv(integrationMarkerEnv, "1")
	os.Setenv(integrationCaptureEnv, filepath.Join(os.Getenv("AGENTCTL_STUB_CAPTURE_DIR"), role+".input"))
	if err := syscall.Exec(harnessPath, append([]string{harnessPath}, harnessArguments...), os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(90)
	}
}

func integrationMarkerMain() {
	capture := os.Getenv(integrationCaptureEnv)
	file, err := os.OpenFile(capture, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(91)
	}
	defer file.Close()

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if _, err := fmt.Fprintln(file, scanner.Text()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(92)
		}
		if err := file.Sync(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(93)
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(94)
	}
}

func newIntegrationFixture(t *testing.T) *integrationFixture {
	return newIntegrationFixtureWithServer(t, true)
}

func newIntegrationFixtureWithoutServer(t *testing.T) *integrationFixture {
	return newIntegrationFixtureWithServer(t, false)
}

func newIntegrationFixtureWithServer(t *testing.T, bootstrapServer bool) *integrationFixture {
	t.Helper()
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skipf("integration test requires tmux: %v", err)
	}
	tmuxPath, err = filepath.Abs(tmuxPath)
	if err != nil {
		t.Fatalf("resolve tmux path: %v", err)
	}

	socket := "agentctl-test-" + randomHex(t, 8)
	runner := &socketRunner{tmuxPath: tmuxPath, socket: socket, tmuxTmpDir: shortTmuxTempDir(t)}
	fixture := &integrationFixture{
		t:           t,
		runner:      runner,
		client:      tmuxx.New(runner),
		invocations: filepath.Join(t.TempDir(), "amq-invocations.tsv"),
		captureDir:  t.TempDir(),
	}

	fixture.installStubs()
	t.Setenv("TMUX_PANE", "")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		output, cleanupErr := runner.tmuxCommand(ctx, "kill-server").CombinedOutput()
		if cleanupErr == nil {
			return
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Errorf("timed out cleaning tmux socket %q", socket)
			return
		}
		message := string(output)
		if strings.Contains(message, "no server running") || strings.Contains(message, "error connecting to") {
			return
		}
		t.Errorf("clean tmux socket %q: %v: %s", socket, cleanupErr, message)
	})
	if bootstrapServer {
		fixture.bootstrapEmptyServer()
	}
	return fixture
}

func randomHex(t *testing.T, byteCount int) string {
	t.Helper()
	buffer := make([]byte, byteCount)
	if _, err := rand.Read(buffer); err != nil {
		t.Fatalf("create random tmux socket name: %v", err)
	}
	return hex.EncodeToString(buffer)
}

func shortTmuxTempDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "agentctl-tmux-")
	if err != nil {
		t.Fatalf("create private tmux temp directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Errorf("remove private tmux temp directory %q: %v", directory, err)
		}
	})
	return directory
}

func (f *integrationFixture) bootstrapEmptyServer() {
	f.t.Helper()
	if _, err := f.runner.Output(context.Background(), "tmux", "start-server", ";", "set-option", "-g", "exit-empty", "off"); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			f.t.Fatalf("start empty tmux server: %v: %s", err, exitError.Stderr)
		}
		f.t.Fatalf("start empty tmux server: %v", err)
	}
}

func (f *integrationFixture) installStubs() {
	f.t.Helper()
	binDir := f.t.TempDir()
	testBinary, err := os.Executable()
	if err != nil {
		f.t.Fatalf("resolve test binary: %v", err)
	}

	for _, executable := range []string{"amq", "claude", "codex"} {
		if err := os.Link(testBinary, filepath.Join(binDir, executable)); err != nil {
			f.t.Fatalf("install %s integration stub: %v", executable, err)
		}
	}

	f.t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	f.t.Setenv("AGENTCTL_STUB_INVOCATIONS", f.invocations)
	f.t.Setenv("AGENTCTL_STUB_CAPTURE_DIR", f.captureDir)

	// Blank the identity variables the test process may itself have been
	// launched with: the tmux server inherits this environment, so a value
	// inherited from outside could otherwise pass for one agentctl exported.
	// Empty is equivalent to absent for session resolution (§4.1).
	f.t.Setenv("AGENTCTL_SESSION", "")
	f.t.Setenv("AGENTCTL_ROLE", "")
	f.t.Setenv("AGENTCTL_MANAGED", "")
}

func (f *integrationFixture) runAgentctl(arguments ...string) integrationResult {
	f.t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := runWithRunner(context.Background(), arguments, &stdout, &stderr, f.runner, os.LookupEnv)
	return integrationResult{exitCode: exitCode, stdout: stdout.String(), stderr: stderr.String()}
}

func (f *integrationFixture) createSentinelSession(name string) {
	f.t.Helper()
	const format = "#{session_id}\t#{window_id}\t#{pane_id}"
	output := f.tmuxOutput(
		"new-session", "-d", "-s", name, "-n", "sentinel",
		"-P", "-F", format, "--", "exec sleep 300",
	)
	fields := strings.Split(strings.TrimSuffix(string(output), "\n"), "\t")
	if len(fields) != 3 {
		f.t.Fatalf("parse sentinel creation %q: got %d fields", output, len(fields))
	}
	for index, prefix := range []byte{'$', '@', '%'} {
		if len(fields[index]) < 2 || fields[index][0] != prefix {
			f.t.Fatalf("parse sentinel creation field %d %q: want %c-prefixed ID", index+1, fields[index], prefix)
		}
	}
}

func (f *integrationFixture) sentinelSnapshot(name string) sentinelSnapshot {
	f.t.Helper()
	var found *integrationSession
	for _, session := range f.sessions() {
		if session.Name == name {
			if found != nil {
				f.t.Fatalf("multiple sentinel sessions named %q", name)
			}
			copy := session
			found = &copy
		}
	}
	if found == nil {
		f.t.Fatalf("sentinel session %q is missing", name)
	}
	windows := f.windows(found.ID)
	if len(windows) != 1 {
		f.t.Fatalf("sentinel session %q windows = %#v, want one", name, windows)
	}
	panes := f.panes(windows[0].ID)
	if len(panes) != 1 {
		f.t.Fatalf("sentinel session %q panes = %#v, want one", name, panes)
	}
	process := f.waitProcessBase(panes[0].PID, "sleep")
	return sentinelSnapshot{Session: *found, Window: windows[0], Pane: panes[0], Process: process}
}

func (f *integrationFixture) sessions() []integrationSession {
	f.t.Helper()
	const format = "#{session_id}\t#{session_name}\t#{@agentctl_managed}\t#{@agentctl_version}\t#{@agentctl_roles}"
	output := f.tmuxOutput("list-sessions", "-F", format)
	lines := nonemptyLines(output)
	sessions := make([]integrationSession, 0, len(lines))
	for _, line := range lines {
		fields := strings.SplitN(line, "\t", 5)
		if len(fields) != 5 {
			f.t.Fatalf("parse integration session %q: got %d fields", line, len(fields))
		}
		sessions = append(sessions, integrationSession{
			ID: tmuxx.SessionID(fields[0]), Name: fields[1], Managed: fields[2], Version: fields[3], Roles: fields[4],
		})
	}
	return sessions
}

func (f *integrationFixture) hasSession(name string) bool {
	f.t.Helper()
	for _, session := range f.sessions() {
		if session.Name == name {
			return true
		}
	}
	return false
}

func (f *integrationFixture) windows(sessionID tmuxx.SessionID) []integrationWindow {
	f.t.Helper()
	const format = "#{window_id}\t#{window_name}\t#{@agentctl_managed}\t#{@agentctl_version}\t#{@agentctl_role}\t#{@agentctl_harness}\t#{@agentctl_model}\t#{@agentctl_effort}\t#{@agentctl_process}\t#{pane_current_path}"
	output := f.tmuxOutput("list-windows", "-t", string(sessionID), "-F", format)
	lines := nonemptyLines(output)
	windows := make([]integrationWindow, 0, len(lines))
	for _, line := range lines {
		fields := strings.SplitN(line, "\t", 10)
		if len(fields) != 10 {
			f.t.Fatalf("parse integration window %q: got %d fields", line, len(fields))
		}
		windows = append(windows, integrationWindow{
			Window: tmuxx.Window{
				ID: tmuxx.WindowID(fields[0]), Name: fields[1], Managed: fields[2], Version: fields[3],
				Role: fields[4], Harness: fields[5], Model: fields[6], Effort: fields[7], Process: fields[8],
			},
			Directory: fields[9],
		})
	}
	return windows
}

func (f *integrationFixture) panes(windowID tmuxx.WindowID) []tmuxx.Pane {
	f.t.Helper()
	panes, err := f.client.ListPanes(context.Background(), windowID)
	if err != nil {
		f.t.Fatalf("list panes for %s: %v", windowID, err)
	}
	return panes
}

func (f *integrationFixture) killWindow(windowID tmuxx.WindowID) {
	f.t.Helper()
	f.tmuxOutput("kill-window", "-t", string(windowID))
}

func (f *integrationFixture) processName(pid int) string {
	f.t.Helper()
	process, err := f.client.ProcessName(context.Background(), pid)
	if err != nil {
		f.t.Fatalf("inspect process %d: %v", pid, err)
	}
	return process
}

func (f *integrationFixture) waitProcessBase(pid int, wantBase string) string {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	last := ""
	var lastErr error
	for time.Now().Before(deadline) {
		last, lastErr = f.client.ProcessName(context.Background(), pid)
		if lastErr == nil && filepath.Base(last) == wantBase {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatalf("wait for process %d base %q: last = %q, error = %v", pid, wantBase, last, lastErr)
	return ""
}

func (f *integrationFixture) stubInvocations() []stubInvocation {
	f.t.Helper()
	invocations, err := f.readStubInvocations()
	if err != nil {
		f.t.Fatalf("read stub invocations: %v", err)
	}
	return invocations
}

func (f *integrationFixture) waitStubInvocations(count int) []stubInvocation {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last []stubInvocation
	var lastErr error
	for time.Now().Before(deadline) {
		last, lastErr = f.readStubInvocations()
		if lastErr == nil && len(last) >= count {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatalf("wait for %d stub invocations: last = %#v, error = %v", count, last, lastErr)
	return nil
}

func (f *integrationFixture) waitRoleInput(role, want string) {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	last := ""
	for time.Now().Before(deadline) {
		last = f.roleInput(role)
		if last == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatalf("wait for %q marker input: got %q, want %q", role, last, want)
}

func (f *integrationFixture) waitRoleMarkers(roles ...string) {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		allReady := true
		for _, role := range roles {
			_, err := os.Stat(filepath.Join(f.captureDir, role+".input"))
			if errors.Is(err, os.ErrNotExist) {
				allReady = false
				continue
			}
			if err != nil {
				f.t.Fatalf("inspect %q marker readiness: %v", role, err)
			}
		}
		if allReady {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatalf("wait for marker readiness: roles = %q", roles)
}

func (f *integrationFixture) assertRoleInputRemains(role, want string, quietWindow time.Duration) {
	f.t.Helper()
	deadline := time.Now().Add(quietWindow)
	for {
		if got := f.roleInput(role); got != want {
			f.t.Fatalf("%q marker input changed during quiet window: got %q, want %q", role, got, want)
		}
		if !time.Now().Before(deadline) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (f *integrationFixture) roleInput(role string) string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.captureDir, role+".input"))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		f.t.Fatalf("read %q marker input: %v", role, err)
	}
	return string(data)
}

func (f *integrationFixture) readStubInvocations() ([]stubInvocation, error) {
	data, err := os.ReadFile(f.invocations)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lines := nonemptyLines(data)
	invocations := make([]stubInvocation, 0, len(lines))
	for _, line := range lines {
		fields := strings.SplitN(line, "\t", 8)
		if len(fields) != 8 {
			return nil, fmt.Errorf("parse stub invocation %q: got %d fields", line, len(fields))
		}
		invocations = append(invocations, stubInvocation{
			Session: fields[0], Role: fields[1], Harness: fields[2], Model: fields[3], Effort: fields[4],
			Environment: stubEnvironment{Session: fields[5], Role: fields[6], Managed: fields[7]},
		})
	}
	return invocations, nil
}

func (f *integrationFixture) tmuxOutput(arguments ...string) []byte {
	f.t.Helper()
	output, err := f.runner.Output(context.Background(), "tmux", arguments...)
	if err != nil {
		f.t.Fatalf("tmux %s: %v", strings.Join(arguments, " "), err)
	}
	return output
}

func nonemptyLines(data []byte) []string {
	trimmed := strings.TrimSuffix(string(data), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}
