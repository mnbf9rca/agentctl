//go:build integration

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
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

	"github.com/mnbf9rca/agentctl/internal/shim"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

const (
	integrationMarkerEnv     = "AGENTCTL_INTEGRATION_MARKER"
	integrationCaptureEnv    = "AGENTCTL_INTEGRATION_CAPTURE"
	integrationShimBinaryEnv = "AGENTCTL_INTEGRATION_SHIM_BINARY"
	integrationAgentctlEnv   = "AGENTCTL_INTEGRATION_AGENTCTL_BINARY"
	integrationOwnedPIDsEnv  = "AGENTCTL_INTEGRATION_OWNED_PIDS"
	integrationRawStubEnv    = "AGENTCTL_INTEGRATION_RAW_STUB"
	integrationCandidateEnv  = "AGENTCTL_INTEGRATION_RELEASE_CANDIDATE"
	integrationRealTMUXEnv   = "AGENTCTL_INTEGRATION_REAL_TMUX"
	integrationTMUXSocketEnv = "AGENTCTL_INTEGRATION_TMUX_SOCKET"
	integrationTMUXTmpEnv    = "AGENTCTL_INTEGRATION_TMUX_TMPDIR"
	integrationProjectEnv    = "AGENTCTL_INTEGRATION_PROJECT_DIR"
)

type integrationResult struct {
	exitCode int
	stdout   string
	stderr   string
}

type integrationSession struct {
	ID        tmuxx.SessionID
	Name      string
	Managed   string
	Version   string
	Roles     string
	Fleet     string
	Directory string
}

type integrationWindow struct {
	tmuxx.Window
	Directory string
}

// integrationHandmadeWindow is a same-name window created directly by the
// fixture, outside agentctl's metadata-stamping path.
type integrationHandmadeWindow struct {
	ID      tmuxx.WindowID
	capture string
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
	t              *testing.T
	runner         *socketRunner
	client         tmuxx.Client
	invocations    string
	captureDir     string
	runtimeRoot    string
	stateRoot      string
	agentctlPath   string
	ownedChildPIDs string
}

type socketRunner struct {
	tmuxPath                  string
	socket                    string
	tmuxTmpDir                string
	failureMu                 sync.Mutex
	failOperation             string
	processMu                 sync.Mutex
	firstProcessPID           string
	processUnavailableForRest bool
}

type integrationProcessExitError struct{}

func (integrationProcessExitError) Error() string { return "injected ps exit status 1" }
func (integrationProcessExitError) ExitCode() int { return 1 }

func TestIntegrationFixtureRemovesSocketDirectory(t *testing.T) {
	var socketDirectory string
	var runtimeRoot string
	var stateRoot string
	t.Run("fixture lifetime", func(t *testing.T) {
		fixture := newIntegrationFixture(t)
		socketDirectory = fixture.runner.tmuxTmpDir
		runtimeRoot = fixture.runtimeRoot
		stateRoot = fixture.stateRoot
		if _, err := os.Stat(socketDirectory); err != nil {
			t.Fatalf("private tmux socket directory during test: %v", err)
		}
		for _, root := range []string{runtimeRoot, stateRoot} {
			info, err := os.Stat(root)
			if err != nil {
				t.Fatalf("private shim root %q during test: %v", root, err)
			}
			if got := info.Mode().Perm(); got != 0o700 {
				t.Fatalf("private shim root %q mode = %#o, want 0700", root, got)
			}
		}
	})
	if _, err := os.Stat(socketDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private tmux socket directory after cleanup: %v", err)
	}
	for _, root := range []string{runtimeRoot, stateRoot} {
		if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("private shim root %q after cleanup: %v", root, err)
		}
	}
}

func TestIntegrationReleaseCandidateSelection(t *testing.T) {
	candidate := os.Getenv(integrationCandidateEnv)
	if candidate == "" {
		t.Skip("release-candidate routing is enabled only by the Task 8 walkthrough")
	}
	fixture := newIntegrationFixtureWithoutServer(t)
	resolved, err := filepath.Abs(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.agentctlPath != resolved {
		t.Fatalf("fixture agentctl path = %q, want supplied candidate %q", fixture.agentctlPath, resolved)
	}
	contents, err := os.ReadFile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	result := fixture.runAgentctl("version")
	if result.exitCode != 0 || result.stdout == "" || result.stderr != "" {
		t.Fatalf("supplied candidate version = %#v", result)
	}
	t.Logf("release candidate path=%s sha256=%x version=%s", resolved, sha256.Sum256(contents), strings.TrimSpace(result.stdout))
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
	if executable == "ps" && r.processUnavailable(args) {
		return nil, integrationProcessExitError{}
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

func (r *socketRunner) makeProcessUnavailableAfterFirstPID() {
	r.processMu.Lock()
	defer r.processMu.Unlock()
	r.firstProcessPID = ""
	r.processUnavailableForRest = true
}

func (r *socketRunner) allowAllProcesses() {
	r.processMu.Lock()
	defer r.processMu.Unlock()
	r.firstProcessPID = ""
	r.processUnavailableForRest = false
}

func (r *socketRunner) processUnavailable(args []string) bool {
	r.processMu.Lock()
	defer r.processMu.Unlock()
	if !r.processUnavailableForRest || len(args) == 0 {
		return false
	}
	pid := args[len(args)-1]
	if r.firstProcessPID == "" {
		r.firstProcessPID = pid
	}
	return pid != r.firstProcessPID
}

func TestMain(m *testing.M) {
	switch filepath.Base(os.Args[0]) {
	case "amq":
		integrationAMQMain()
		return
	case "claude", "codex":
		integrationMarkerMain()
		return
	case "tmux":
		integrationTMUXMain()
		return
	}
	if os.Getenv(integrationShimBinaryEnv) == "1" && len(os.Args) > 1 && os.Args[1] == "__shim" {
		os.Setenv(integrationRawStubEnv, "1")
		os.Exit(runWithRunner(context.Background(), os.Args[1:], os.Stdout, os.Stderr, tmuxx.RealRunner{}, os.LookupEnv))
	}
	if os.Getenv(integrationAgentctlEnv) == "1" {
		os.Setenv(integrationRawStubEnv, "1")
		os.Exit(runWithRunner(context.Background(), os.Args[1:], os.Stdout, os.Stderr, tmuxx.RealRunner{}, os.LookupEnv))
	}
	if os.Getenv(integrationMarkerEnv) == "1" {
		integrationMarkerMain()
		return
	}
	os.Exit(m.Run())
}

func integrationTMUXMain() {
	realTMUX := os.Getenv(integrationRealTMUXEnv)
	socket := os.Getenv(integrationTMUXSocketEnv)
	tmpDir := os.Getenv(integrationTMUXTmpEnv)
	if realTMUX == "" || socket == "" || tmpDir == "" {
		fmt.Fprintln(os.Stderr, "integration tmux stub: incomplete private socket environment")
		os.Exit(96)
	}
	arguments := append([]string{realTMUX, "-L", socket}, os.Args[1:]...)
	environment := environmentWith(os.Environ(), "TMUX_TMPDIR", tmpDir)
	if err := syscall.Exec(realTMUX, arguments, environment); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(97)
	}
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
	pidRecord, err := os.OpenFile(os.Getenv(integrationOwnedPIDsEnv), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(89)
	}
	token, err := shim.ReadStartToken(os.Getpid())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(89)
	}
	if _, err := fmt.Fprintf(pidRecord, "%d %d %d\n", os.Getpid(), token.Sec, token.Usec); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(89)
	}
	if err := pidRecord.Close(); err != nil {
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
	if os.Getenv(integrationCandidateEnv) != "" {
		os.Setenv(integrationRawStubEnv, "1")
	}
	os.Setenv(integrationCaptureEnv, filepath.Join(os.Getenv("AGENTCTL_STUB_CAPTURE_DIR"), role+".input"))
	if err := syscall.Exec(harnessPath, append([]string{harnessPath}, harnessArguments...), os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(90)
	}
}

func integrationMarkerMain() {
	if os.Getenv(integrationRawStubEnv) == "1" {
		command := exec.Command("/bin/stty", "-icanon", "-echo")
		command.Stdin = os.Stdin
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Run(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(95)
		}
	}
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
	shimRoot := shortShimTempDir(t)
	fixture := &integrationFixture{
		t:              t,
		runner:         runner,
		client:         tmuxx.New(runner),
		invocations:    filepath.Join(t.TempDir(), "amq-invocations.tsv"),
		captureDir:     t.TempDir(),
		ownedChildPIDs: filepath.Join(shimRoot, "owned-child-pids.txt"),
	}
	fixture.runtimeRoot = filepath.Join(shimRoot, "r")
	fixture.stateRoot = filepath.Join(shimRoot, "s")
	for _, root := range []string{fixture.runtimeRoot, fixture.stateRoot} {
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatalf("create isolated shim root %q: %v", root, err)
		}
	}

	fixture.installStubs()
	t.Setenv("TMUX_PANE", "")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		output, cleanupErr := runner.tmuxCommand(ctx, "kill-server").CombinedOutput()
		if cleanupErr != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Errorf("timed out cleaning tmux socket %q", socket)
		} else if cleanupErr != nil {
			message := string(output)
			if !strings.Contains(message, "no server running") && !strings.Contains(message, "error connecting to") {
				t.Errorf("clean tmux socket %q: %v: %s", socket, cleanupErr, message)
			}
		}
		fixture.cleanupOwnedChildren()
	})
	if bootstrapServer {
		fixture.bootstrapEmptyServer()
	}
	return fixture
}

func shortShimTempDir(t *testing.T) string {
	t.Helper()
	parent := os.Getenv("AGENTCTL_INTEGRATION_OWNED_ROOT")
	if parent == "" {
		parent = "/tmp"
	} else if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("create integration-owned root: %v", err)
	}
	directory, err := os.MkdirTemp(parent, "a5i-")
	if err != nil {
		t.Fatalf("create private shim temp directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Errorf("remove private shim temp directory %q: %v", directory, err)
		}
	})
	return directory
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
	f.agentctlPath = testBinary
	if candidate := os.Getenv(integrationCandidateEnv); candidate != "" {
		candidate, err = filepath.Abs(candidate)
		if err != nil {
			f.t.Fatalf("resolve integration release candidate: %v", err)
		}
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			f.t.Fatalf("integration release candidate %q is not executable: %v", candidate, err)
		}
		f.agentctlPath = candidate
	}

	for _, executable := range []string{"amq", "claude", "codex"} {
		if err := os.Link(testBinary, filepath.Join(binDir, executable)); err != nil {
			f.t.Fatalf("install %s integration stub: %v", executable, err)
		}
	}
	if f.agentctlPath != testBinary {
		if err := os.Link(testBinary, filepath.Join(binDir, "tmux")); err != nil {
			f.t.Fatalf("install tmux integration stub: %v", err)
		}
		f.t.Setenv(integrationRealTMUXEnv, f.runner.tmuxPath)
		f.t.Setenv(integrationTMUXSocketEnv, f.runner.socket)
		f.t.Setenv(integrationTMUXTmpEnv, f.runner.tmuxTmpDir)
	}

	f.t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	f.t.Setenv("HOME", f.t.TempDir())
	f.t.Setenv("AGENTCTL_STUB_INVOCATIONS", f.invocations)
	f.t.Setenv("AGENTCTL_STUB_CAPTURE_DIR", f.captureDir)
	f.t.Setenv(integrationOwnedPIDsEnv, f.ownedChildPIDs)
	f.t.Setenv(integrationShimBinaryEnv, "1")
	f.t.Setenv("AGENTCTL_RUNTIME_ROOT", f.runtimeRoot)
	f.t.Setenv("AGENTCTL_STATE_ROOT", f.stateRoot)

	// Blank the identity variables the test process may itself have been
	// launched with: the tmux server inherits this environment, so a value
	// inherited from outside could otherwise pass for one agentctl exported.
	// Empty is equivalent to absent for session resolution (§4.1).
	f.t.Setenv("AGENTCTL_SESSION", "")
	f.t.Setenv("AGENTCTL_ROLE", "")
	f.t.Setenv("AGENTCTL_MANAGED", "")
}

func (f *integrationFixture) cleanupOwnedChildren() {
	f.t.Helper()
	payload, err := os.ReadFile(f.ownedChildPIDs)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		f.t.Errorf("read owned child PIDs: %v", err)
		return
	}
	type ownedIdentity struct {
		pid   int
		token shim.StartToken
	}
	var identities []ownedIdentity
	for _, line := range nonemptyLines(payload) {
		var identity ownedIdentity
		if _, err := fmt.Sscanf(line, "%d %d %d", &identity.pid, &identity.token.Sec, &identity.token.Usec); err != nil || identity.pid <= 0 {
			f.t.Errorf("parse owned child PID %q: %v", line, err)
			continue
		}
		identities = append(identities, identity)
	}
	for _, identity := range identities {
		deadline := time.Now().Add(5 * time.Second)
		for {
			err := syscall.Kill(identity.pid, 0)
			if errors.Is(err, syscall.ESRCH) {
				break
			}
			if err != nil {
				f.t.Errorf("observe owned child PID %d during fixture teardown: %v", identity.pid, err)
				break
			}
			if !time.Now().Before(deadline) {
				observed, tokenErr := shim.ReadStartToken(identity.pid)
				if tokenErr != nil {
					if !errors.Is(tokenErr, syscall.ESRCH) {
						f.t.Errorf("owned child PID %d start token could not be observed; refusing SIGKILL: %v", identity.pid, tokenErr)
					}
					break
				}
				if !identity.token.Equal(observed) {
					f.t.Errorf("owned child PID %d was reused; refusing SIGKILL: recorded=%#v observed=%#v", identity.pid, identity.token, observed)
					break
				}
				if err := syscall.Kill(identity.pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
					f.t.Errorf("SIGKILL token-matched owned child PID %d: %v", identity.pid, err)
					break
				}
				if err := waitIntegrationPIDAbsent(identity.pid, time.Second); err != nil {
					f.t.Errorf("owned child PID %d survived token-matched SIGKILL: %v", identity.pid, err)
				}
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func waitIntegrationPIDAbsent(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return err
		}
		if !time.Now().Before(deadline) {
			return errors.New("kill(pid, 0) did not return ESRCH")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (f *integrationFixture) runAgentctl(arguments ...string) integrationResult {
	f.t.Helper()
	if f.agentctlPath != mustIntegrationTestBinary(f.t) {
		command := exec.Command(f.agentctlPath, arguments...)
		command.Env = os.Environ()
		command.Dir = os.Getenv(integrationProjectEnv)
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		command.Stdout = &stdout
		command.Stderr = &stderr
		err := command.Run()
		exitCode := 0
		if err != nil {
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) {
				f.t.Fatalf("run release candidate %v: %v", arguments, err)
			}
			exitCode = exitError.ExitCode()
		}
		return integrationResult{exitCode: exitCode, stdout: stdout.String(), stderr: stderr.String()}
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := runWithRunner(context.Background(), arguments, &stdout, &stderr, f.runner, os.LookupEnv)
	return integrationResult{exitCode: exitCode, stdout: stdout.String(), stderr: stderr.String()}
}

func mustIntegrationTestBinary(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve integration test binary: %v", err)
	}
	return path
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
	const format = "#{session_id}\t#{session_name}\t#{@agentctl_managed}\t#{@agentctl_version}\t#{@agentctl_roles}\t#{@agentctl_fleet}\t#{@agentctl_dir}"
	output := f.tmuxOutput("list-sessions", "-F", format)
	lines := nonemptyLines(output)
	sessions := make([]integrationSession, 0, len(lines))
	for _, line := range lines {
		fields := strings.SplitN(line, "\t", 7)
		if len(fields) != 7 {
			f.t.Fatalf("parse integration session %q: got %d fields", line, len(fields))
		}
		sessions = append(sessions, integrationSession{
			ID: tmuxx.SessionID(fields[0]), Name: fields[1], Managed: fields[2], Version: fields[3],
			Roles: fields[4], Fleet: fields[5], Directory: fields[6],
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

func (f *integrationFixture) presentationSession(name string) tmuxx.Session {
	f.t.Helper()
	var found *tmuxx.Session
	for _, observed := range f.sessions() {
		if observed.Name != name {
			continue
		}
		if found != nil {
			f.t.Fatalf("multiple presentations named %q", name)
		}
		value := tmuxx.Session{ID: observed.ID, Name: observed.Name}
		found = &value
	}
	if found == nil {
		f.t.Fatalf("presentation %q is missing", name)
	}
	return *found
}

func (f *integrationFixture) windows(sessionID tmuxx.SessionID) []integrationWindow {
	f.t.Helper()
	const format = "#{window_id}\t#{window_name}\t#{@agentctl_role}\t#{@agentctl_harness}\t#{@agentctl_model}\t#{@agentctl_effort}\t#{@agentctl_unproven}\t#{@agentctl_process}\t#{pane_current_path}"
	output := f.tmuxOutput("list-windows", "-t", string(sessionID), "-F", format)
	lines := nonemptyLines(output)
	windows := make([]integrationWindow, 0, len(lines))
	for _, line := range lines {
		fields := strings.SplitN(line, "\t", 9)
		if len(fields) != 9 {
			f.t.Fatalf("parse integration window %q: got %d fields", line, len(fields))
		}
		windows = append(windows, integrationWindow{
			Window: tmuxx.Window{
				ID: tmuxx.WindowID(fields[0]), Name: fields[1], Role: fields[2], Harness: fields[3],
				Model: fields[4], Effort: fields[5], Unproven: fields[6], Process: fields[7],
			},
			Directory: fields[8],
		})
	}
	return windows
}

func (f *integrationFixture) createHandmadeWindow(sessionID tmuxx.SessionID, name string) integrationHandmadeWindow {
	f.t.Helper()
	capture := filepath.Join(f.captureDir, "handmade-"+name+".input")
	output := f.tmuxOutput(
		"new-window", "-d", "-t", string(sessionID), "-n", name,
		"-e", integrationCaptureEnv+"="+capture,
		"-P", "-F", "#{window_id}", "--", "exec claude",
	)
	windowID := strings.TrimSuffix(string(output), "\n")
	if len(windowID) < 2 || windowID[0] != '@' {
		f.t.Fatalf("parse handmade window creation %q: want @-prefixed window ID", output)
	}
	for _, digit := range windowID[1:] {
		if digit < '0' || digit > '9' {
			f.t.Fatalf("parse handmade window creation %q: want numeric window ID", output)
		}
	}
	return integrationHandmadeWindow{ID: tmuxx.WindowID(windowID), capture: capture}
}

func (f *integrationFixture) waitHandmadeWindowReady(window integrationHandmadeWindow) {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(window.capture); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			f.t.Fatalf("inspect handmade marker readiness: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatalf("wait for handmade marker readiness at %q", window.capture)
}

func (f *integrationFixture) assertHandmadeInputRemains(window integrationHandmadeWindow, want string, quietWindow time.Duration) {
	f.t.Helper()
	deadline := time.Now().Add(quietWindow)
	for {
		data, err := os.ReadFile(window.capture)
		if err != nil {
			f.t.Fatalf("read handmade marker input: %v", err)
		}
		if got := string(data); got != want {
			f.t.Fatalf("handmade marker input changed during quiet window: got %q, want %q", got, want)
		}
		if !time.Now().Before(deadline) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (f *integrationFixture) windowOption(windowID tmuxx.WindowID, name string) string {
	f.t.Helper()
	value, err := f.client.ShowWindowOption(context.Background(), windowID, name)
	if err != nil {
		f.t.Fatalf("show window option %q for %s: %v", name, windowID, err)
	}
	return value
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
