package hack_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mnbf9rca/agentctl/internal/shim"
)

func renderResults(t *testing.T, versions, artifactDir string) (string, error) {
	t.Helper()
	cmd := exec.Command("./release-verify.sh", "--render-results", versions, artifactDir)
	out, err := cmd.Output()
	return string(out), err
}

func TestRenderResultsMatchesGolden(t *testing.T) {
	cases := []struct {
		name        string
		artifactDir string
		goldenPath  string
	}{
		{"measure", "testdata/release-verify-measure-artifact", "testdata/release-verify-measure-results.golden"},
		{"verify live", "testdata/release-verify-live-artifact", "testdata/release-verify-live-results.golden"},
		{"verify live pre-auth metadata", "testdata/release-verify-live-pre-auth-artifact", "testdata/release-verify-live-pre-auth-results.golden"},
		{"verify live legacy", "testdata/release-verify-live-legacy-artifact", "testdata/release-verify-live-legacy-results.golden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := renderResults(t, "testdata/release-verify-versions.txt", tc.artifactDir)
			if err != nil {
				t.Fatalf("render-results failed: %v", err)
			}
			golden, err := os.ReadFile(tc.goldenPath)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(golden) {
				t.Fatalf("output does not match golden.\n--- got ---\n%s\n--- want ---\n%s", got, string(golden))
			}
		})
	}
}

func TestRenderResultsRejects(t *testing.T) {
	cases := []struct {
		name        string
		versions    string
		artifactDir string
	}{
		{"missing versions file", "testdata/absent-versions.txt", "testdata/release-verify-artifact"},
		{"missing artifact dir", "testdata/release-verify-versions.txt", "testdata/absent-artifact"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := renderResults(t, tc.versions, tc.artifactDir)
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 1 {
				t.Fatalf("want exit status 1, got %v", err)
			}
		})
	}
}

func TestRenderResultsRejectsUnknownLiveSchemaAndAuthenticationModes(t *testing.T) {
	base, err := os.ReadFile("testdata/release-verify-live-artifact/metadata.txt")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		metadata string
	}{
		{
			name:     "unknown checkpoint schema",
			metadata: string(base) + "live_human_checkpoint_schema=future-v2\n",
		},
		{
			name: "unknown codex authentication mode",
			metadata: strings.Replace(
				string(base),
				"part_c_auth_mode=codex-seeded",
				"part_c_auth_mode=untrusted-future-value",
				1,
			),
		},
		{
			name: "unknown claude authentication mode",
			metadata: strings.Replace(
				string(base),
				"part_c_auth_mode=codex-seeded",
				"part_c_auth_mode=codex-seeded\npart_c_claude_auth_mode=untrusted-future-value",
				1,
			),
		},
		{
			name:     "unknown Part B AMQ mode",
			metadata: string(base) + "part_b_amq_mode=untrusted-future-value\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			artifactDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(artifactDir, "metadata.txt"), []byte(tc.metadata), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := renderResults(t, "testdata/release-verify-versions.txt", artifactDir)
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 1 {
				t.Fatalf("want exit status 1, got %v", err)
			}
		})
	}
}

func processCheck(t *testing.T, versions, artifactDir string) (string, error) {
	t.Helper()
	cmd := exec.Command("./release-verify.sh", "--process-check", versions, artifactDir)
	out, err := cmd.Output()
	return string(out), err
}

func TestTask8RejectsNonemptyEvidenceDirectoryBeforeRunningCandidate(t *testing.T) {
	artifactDir := t.TempDir()
	sentinel := filepath.Join(artifactDir, "sentinel")
	writeTestFile(t, sentinel, []byte("preserve\n"), 0o600)
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", "hack/release-verify.sh", "--task8", current, artifactDir)
	command.Dir = repository
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("Task 8 accepted stale evidence directory:\n%s", output)
	}
	if !strings.Contains(string(output), "artifact directory is not empty") {
		t.Fatalf("Task 8 rejection = %q", output)
	}
	if got := readTestFile(t, sentinel); got != "preserve\n" {
		t.Fatalf("stale evidence sentinel changed to %q", got)
	}
}

// This catches the release walkthrough dropping one of the tmuxless candidate
// transcript legs or ceasing to route those legs through CURRENT_BINARY and
// Task-8-owned roots. It runs the real script with only toolchain boundaries
// faked, and asserts the executed go-test argv and environment.
func TestTask8VerifierRunsTmuxlessCandidateTranscriptInOwnedRoots(t *testing.T) {
	fixture := newTask8TranscriptFixture(t)
	output, err := fixture.run(t)
	if err != nil {
		t.Fatalf("Task 8 transcript fixture failed: %v\n%s", err, output)
	}
	log := readTestFile(t, fixture.goLog)
	wantCandidate := "candidate=" + fixture.candidate
	for _, want := range []string{
		"test -tags integration ./cmd/agentctl -count=1 -v -run TestIntegration(",
		"DetachedRoleAttachReleasesOnSignalAndReadmits",
		"ShimPresentationLayoutDoesNotChangeRuntimeIdentityOrDelivery",
		"ReleaseCandidateCrashRelaunchAndKillUseObservedAbsence",
		"ReleaseCandidateAttachRepaintsAndReadmitsAfterCleanViewerEOF",
		"ShimSIGKILLLeavesApprovedRecordStateAndConcurrentRelaunchStartsOneChild",
		"ShimKillObservesChildExitBeforePresentationAndFleetCleanup",
		"test ./internal/attach ./internal/shim -count=1 -v -run Test(",
		"ViewerResizeEmitsObservedWindowSizeAsOneSerializedControlFrame",
		"AttachServerChildExitMapsExactTailUndeliveredFinal",
		"AttachServerChildExitMapsZeroAndNonzeroTailUnconfirmedFinals",
		"ServerRunDetachedServesAttachAndControlBeforeCleanExit",
		"test ./hack -count=1 -v -run ^TestLiveVerificationDetachedRelaunchUsesRuntimeRecordsAndExplicitRoleAttach$",
		wantCandidate,
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("Task 8 executed transcript log omits %q:\n%s", want, log)
		}
	}
	var integrationLine string
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		if strings.Contains(line, "argv=test -tags integration ./cmd/agentctl") {
			integrationLine = line
			break
		}
	}
	if integrationLine == "" || !strings.Contains(integrationLine, "project=/tmp/a8.") || !strings.Contains(integrationLine, "owned=/tmp/a8.") {
		t.Fatalf("candidate integration did not receive Task-8-owned project and integration roots:\n%s", log)
	}
	integrationTranscript := readTestFile(t, filepath.Join(fixture.root, "evidence", "integration.log"))
	if !strings.Contains(integrationTranscript, "candidate-routed crash/relaunch/kill preserved the private-socket sentinel presentation") {
		t.Fatalf("candidate integration transcript omits the sentinel-preservation result:\n%s", integrationTranscript)
	}
	if !strings.Contains(integrationTranscript, "candidate-routed attach repainted output, released the viewer on VEOF, and admitted a replacement viewer") {
		t.Fatalf("candidate integration transcript omits the repaint/VEOF/replacement result:\n%s", integrationTranscript)
	}
	defaultLiveTranscript := readTestFile(t, filepath.Join(fixture.root, "evidence", "default-live-verifier.log"))
	if !strings.Contains(defaultLiveTranscript, "full-default verifier used detached runtime records and explicit role attach") {
		t.Fatalf("Task 8 default-live transcript omits the detached verifier result:\n%s", defaultLiveTranscript)
	}
	if got := strings.TrimSpace(readTestFile(t, fixture.matrixLog)); got != "root=/tmp" {
		t.Fatalf("version matrix root = %q, want short isolated /tmp parent", got)
	}
}

type task8TranscriptFixture struct {
	root, project, candidate, goLog, matrixLog string
}

func newTask8TranscriptFixture(t *testing.T) task8TranscriptFixture {
	t.Helper()
	root := t.TempDir()
	for _, path := range []string{"hack", "skills/agentctl", "stubs", "bin"} {
		if err := os.MkdirAll(filepath.Join(root, path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script, err := os.ReadFile("release-verify.sh")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "hack/release-verify.sh"), script, 0o755)
	writeTestFile(t, filepath.Join(root, "hack/verify-release-archives.sh"), []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755)
	writeTestFile(t, filepath.Join(root, ".goreleaser.yaml"), []byte("project_name: agentctl\n"), 0o644)
	candidate := filepath.Join(root, "bin/agentctl")
	writeTestFile(t, candidate, []byte(`#!/usr/bin/env bash
set -eu
case "${1:-}" in
  version) echo 'agentctl vfixture' ;;
  --help) printf '  run \n' ;;
  relaunch) printf 'ESRCH-backed stale durable child record\n' ;;
  skill) mkdir -p "$HOME/.claude/skills/agentctl" "$HOME/.agents/skills/agentctl" ;;
esac
`), 0o755)
	goLog := filepath.Join(root, "go.log")
	matrixLog := filepath.Join(root, "matrix.log")
	writeTestFile(t, filepath.Join(root, "stubs/go"), []byte(`#!/usr/bin/env bash
set -eu
printf 'cwd=%s|argv=%s|candidate=%s|project=%s|owned=%s\n' "$PWD" "$*" "${AGENTCTL_INTEGRATION_RELEASE_CANDIDATE:-}" "${AGENTCTL_INTEGRATION_PROJECT_DIR:-}" "${AGENTCTL_INTEGRATION_OWNED_ROOT:-}" >>"$AGENTCTL_TASK8_GO_LOG"
if [ "${1:-}" = test ] && [[ "$*" == *ReleaseCandidateCrashRelaunchAndKillUseObservedAbsence* ]]; then
  printf '%s\n' 'candidate-routed crash/relaunch/kill preserved the private-socket sentinel presentation'
fi
if [ "${1:-}" = test ] && [[ "$*" == *ReleaseCandidateAttachRepaintsAndReadmitsAfterCleanViewerEOF* ]]; then
  printf '%s\n' 'candidate-routed attach repainted output, released the viewer on VEOF, and admitted a replacement viewer'
fi
if [ "${1:-}" = test ] && [[ "$*" == *TestLiveVerificationDetachedRelaunchUsesRuntimeRecordsAndExplicitRoleAttach* ]]; then
  printf '%s\n' 'full-default verifier used detached runtime records and explicit role attach'
fi
if [ "${1:-}" = version ] && [ "${2:-}" = -m ]; then
  printf 'golang.org/x/sys v0.47.0\nvcs.revision=%s\nvcs.modified=false\n' "$(git rev-parse HEAD)"
elif [ "${1:-}" = build ]; then
  out=''
  while [ "$#" -gt 0 ]; do
    if [ "$1" = -o ]; then out=$2; shift 2; continue; fi
    shift
  done
  cat >"$out" <<'SCRIPT'
#!/usr/bin/env bash
set -eu
case "${1:-}" in
  sweep) exit 0 ;;
  matrix)
    printf 'root=%s\n' "${AGENTCTL_SHIM_VERSION_OWNED_ROOT:-}" >"$AGENTCTL_TASK8_MATRIX_LOG"
    while [ "$#" -gt 0 ]; do
      if [ "$1" = --artifact-dir ]; then mkdir -p "$2"; printf 'ok\n' >"$2/results.tsv"; printf 'ok\n' >"$2/metadata.txt"; exit 0; fi
      shift
    done
    ;;
esac
exit 0
SCRIPT
  chmod 755 "$out"
elif [ "${1:-}" = version ]; then
  echo 'go version gofixture darwin/arm64'
fi
`), 0o755)
	writeTestFile(t, filepath.Join(root, "stubs/goreleaser"), []byte(`#!/usr/bin/env bash
set -eu
config=''
while [ "$#" -gt 0 ]; do [ "$1" = --config ] && { config=$2; shift 2; continue; }; shift; done
dist=$(sed -n 's/^dist: "\(.*\)"$/\1/p' "$config")
mkdir -p "$dist"
: >"$dist/fixture.tar.gz"
`), 0o755)
	writeTestFile(t, filepath.Join(root, "stubs/tmux"), []byte("#!/usr/bin/env bash\necho 'tmux 3.7b'\n"), 0o755)
	writeTestFile(t, filepath.Join(root, "stubs/sw_vers"), []byte("#!/usr/bin/env bash\necho fixture\n"), 0o755)
	runCommand(t, root, "git", "init", "-q")
	runCommand(t, root, "git", "add", ".")
	runCommand(t, root, "git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	canonicalCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		t.Fatal(err)
	}
	return task8TranscriptFixture{root: root, project: filepath.Join(root, "task8", "project"), candidate: canonicalCandidate, goLog: goLog, matrixLog: matrixLog}
}

func (fixture task8TranscriptFixture) run(t *testing.T) (string, error) {
	t.Helper()
	evidence := filepath.Join(fixture.root, "evidence")
	command := exec.Command("bash", "hack/release-verify.sh", "--task8", fixture.candidate, evidence)
	command.Dir = fixture.root
	command.Env = append(os.Environ(),
		"AGENTCTL_TASK8_GO_LOG="+fixture.goLog,
		"AGENTCTL_TASK8_MATRIX_LOG="+fixture.matrixLog,
		"PATH="+filepath.Join(fixture.root, "stubs")+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	output, err := command.CombinedOutput()
	return string(output), err
}

func TestTask8SignalsPreserveFailureAndCleanExactlyOnceAtEveryPhase(t *testing.T) {
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"roots", "surface", "skill", "matrix", "integration", "kernel", "safety", "archives", "metadata"} {
		t.Run(phase, func(t *testing.T) {
			artifactDir := t.TempDir()
			output, childPID, err := interruptTask8Phase(t, repository, current, artifactDir, phase, false)
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) || exitError.ExitCode() != 143 {
				t.Fatalf("phase %s interruption error = %v output=%q, want exit 143", phase, err, output)
			}
			if strings.Contains(string(output), "TASK8 RELEASE WALKTHROUGH PASS") {
				t.Fatalf("phase %s interruption claimed walkthrough pass:\n%s", phase, output)
			}
			cleanup := readTestFile(t, filepath.Join(artifactDir, "cleanup.txt"))
			if !strings.Contains(cleanup, "TASK8 CLEANUP PASS") || strings.Count(cleanup, "TASK8 CLEANUP PASS") != 1 {
				t.Fatalf("phase %s cleanup = %q", phase, cleanup)
			}
			if err := syscall.Kill(childPID, 0); !errors.Is(err, syscall.ESRCH) {
				t.Fatalf("phase %s blocking child %d remains after interruption: %v", phase, childPID, err)
			}
			root := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(cleanup), "TASK8 CLEANUP PASS root="), " absent=true")
			if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("phase %s Task 8 root %q remains after interruption: %v", phase, root, err)
			}
		})
	}
}

func TestTask8SignalPreservesSignalStatusWhenCleanupFails(t *testing.T) {
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	artifactDir := t.TempDir()
	output, childPID, err := interruptTask8Phase(t, repository, current, artifactDir, "roots", true)
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 143 {
		t.Fatalf("cleanup-failure interruption error = %v output=%q, want exit 143", err, output)
	}
	if !strings.Contains(string(output), "TASK8 CLEANUP FAIL") || strings.Contains(string(output), "TASK8 RELEASE WALKTHROUGH PASS") {
		t.Fatalf("cleanup-failure output = %q", output)
	}
	if err := syscall.Kill(childPID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("cleanup-failure blocking child %d remains after interruption: %v", childPID, err)
	}
}

type task8OwnedIdentity struct {
	pid   int
	token shim.StartToken
}

func interruptTask8Phase(t *testing.T, repository, current, artifactDir, phase string, cleanupFail bool) ([]byte, int, error) {
	t.Helper()
	pidFile := filepath.Join(artifactDir, "blocking-child-"+phase+".pid")
	testRoot := t.TempDir()
	identityJournal := filepath.Join(testRoot, "owned-identity.txt")
	command := exec.Command("bash", "hack/release-verify.sh", "--task8", current, artifactDir)
	command.Dir = repository
	command.Env = append(os.Environ(),
		"AGENTCTL_TEST_TASK8_PHASE_DRIVER=1",
		"AGENTCTL_TEST_TASK8_BLOCK_PHASE="+phase,
		"AGENTCTL_TEST_TASK8_CHILD_PID_FILE="+pidFile,
		"AGENTCTL_TEST_TASK8_CHILD_IDENTITY_JOURNAL="+identityJournal,
	)
	if phase == "roots" {
		command.Env = append(command.Env, "AGENTCTL_TEST_TASK8_SWEEPER_BUILD_DELAY_SECONDS=6")
	}
	if cleanupFail {
		command.Env = append(command.Env, "AGENTCTL_TEST_TASK8_CLEANUP_FAIL=1")
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	outputFile, err := os.CreateTemp(testRoot, "task8-verifier-output-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = outputFile.Close() }()
	command.Stdout = outputFile
	command.Stderr = outputFile
	readOutput := func() []byte {
		t.Helper()
		if err := outputFile.Sync(); err != nil {
			t.Fatalf("sync Task 8 verifier output: %v", err)
		}
		payload, err := os.ReadFile(outputFile.Name())
		if err != nil {
			t.Fatalf("read Task 8 verifier output: %v", err)
		}
		return payload
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	timeout := time.NewTimer(30 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer timeout.Stop()
	defer ticker.Stop()
	var identity task8OwnedIdentity
	for identity.pid == 0 {
		select {
		case err := <-waited:
			cleanupErr := cleanupTask8IdentityJournal(identityJournal)
			t.Fatalf("phase %s verifier exited before blocking child started: %v cleanup=%v output=%q", phase, err, cleanupErr, readOutput())
		case <-timeout.C:
			waitErr, cleanupErr := terminateTask8Verifier(command, waited, identityJournal, nil)
			t.Fatalf("phase %s blocking child did not start within 30s: verifier=%v cleanup=%v output=%q", phase, waitErr, cleanupErr, readOutput())
		case <-ticker.C:
			contents, err := os.ReadFile(pidFile)
			if err == nil {
				if _, err := fmt.Sscanf(strings.TrimSpace(string(contents)), "%d %d %d", &identity.pid, &identity.token.Sec, &identity.token.Usec); err != nil || identity.pid <= 0 {
					waitErr, cleanupErr := terminateTask8Verifier(command, waited, identityJournal, nil)
					t.Fatalf("parse blocking child identity: %v; verifier=%v cleanup=%v", err, waitErr, cleanupErr)
				}
				continue
			}
			if !errors.Is(err, os.ErrNotExist) {
				waitErr, cleanupErr := terminateTask8Verifier(command, waited, identityJournal, nil)
				t.Fatalf("read blocking child identity: %v; verifier=%v cleanup=%v", err, waitErr, cleanupErr)
			}
		}
	}
	waitErr, cleanupErr := terminateTask8Verifier(command, waited, identityJournal, &identity)
	if cleanupErr != nil {
		t.Fatalf("terminate Task 8 verifier: %v", cleanupErr)
	}
	return readOutput(), identity.pid, waitErr
}

func terminateTask8Verifier(command *exec.Cmd, waited <-chan error, identityJournal string, identity *task8OwnedIdentity) (error, error) {
	signalErr := command.Process.Signal(syscall.SIGTERM)
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case waitErr := <-waited:
		journalErr := cleanupTask8IdentityJournal(identityJournal)
		if signalErr != nil && !errors.Is(signalErr, os.ErrProcessDone) {
			return waitErr, errors.Join(fmt.Errorf("signal verifier TERM: %w", signalErr), journalErr)
		}
		return waitErr, journalErr
	case <-timer.C:
		var cleanupErrors []error
		cleanupErrors = append(cleanupErrors, errors.New("verifier did not exit within 15s of TERM"))
		journaled, journalErr := loadTask8OwnedIdentity(identityJournal)
		if journalErr != nil && !errors.Is(journalErr, os.ErrNotExist) {
			cleanupErrors = append(cleanupErrors, journalErr)
		}
		if journaled != nil {
			identity = journaled
		}
		if identity != nil {
			if err := killTask8OwnedIdentity(*identity); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
		if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("kill verifier process group: %w", err))
		}
		fallback := time.NewTimer(5 * time.Second)
		defer fallback.Stop()
		select {
		case waitErr := <-waited:
			return waitErr, errors.Join(cleanupErrors...)
		case <-fallback.C:
			cleanupErrors = append(cleanupErrors, errors.New("verifier process group was not reaped within 5s of SIGKILL"))
			return nil, errors.Join(cleanupErrors...)
		}
	}
}

func cleanupTask8IdentityJournal(path string) error {
	identity, err := loadTask8OwnedIdentity(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := syscall.Kill(identity.pid, 0); errors.Is(err, syscall.ESRCH) {
		return nil
	} else if err != nil {
		return fmt.Errorf("observe journaled Task 8 identity %d after verifier exit: %w", identity.pid, err)
	}
	cleanupErr := killTask8OwnedIdentity(*identity)
	return fmt.Errorf("journaled Task 8 identity %d survived verifier exit and required cleanup: %v", identity.pid, cleanupErr)
}

func loadTask8OwnedIdentity(path string) (*task8OwnedIdentity, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var identity task8OwnedIdentity
	if _, err := fmt.Sscanf(strings.TrimSpace(string(payload)), "%d %d %d", &identity.pid, &identity.token.Sec, &identity.token.Usec); err != nil {
		return nil, fmt.Errorf("parse Task 8 owned identity journal %q: %w", path, err)
	}
	if identity.pid <= 0 {
		return nil, fmt.Errorf("Task 8 owned identity journal %q has invalid PID %d", path, identity.pid)
	}
	return &identity, nil
}

func killTask8OwnedIdentity(identity task8OwnedIdentity) error {
	observed, err := shim.ReadStartToken(identity.pid)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("observe detached blocker %d before fallback SIGKILL: %w", identity.pid, err)
	}
	if !identity.token.Equal(observed) {
		return fmt.Errorf("detached blocker %d identity changed; refusing fallback SIGKILL", identity.pid)
	}
	if err := syscall.Kill(identity.pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("fallback SIGKILL detached blocker %d: %w", identity.pid, err)
	}
	return waitTestPIDAbsent(identity.pid, 2*time.Second)
}

func waitTestPIDAbsent(pid int, timeout time.Duration) error {
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
			return fmt.Errorf("PID %d did not become ESRCH", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

var requiredProbeEvidence = map[string][]string{
	"probe-1-argv.sh": {
		"OK exit=0",
		"empty-value set OK",
		"read role:  role1",
		"  -v read:   'two words'",
		"name=role1 role=role1 proc=2.1.220",
		"after killing =foo, has-session -t '=foobar': exit0 (foobar survived)",
		"list-panes -t 'probe:rev' resolves to: rev",
		"list-panes -t 'probe:=rev' resolves to: rev",
		"literal send OK",
		"Enter OK",
		"captured: '/clear'",
	},
	"probe-2-targeting.sh": {
		"set-option -t '=alpha':  no such session: =alpha",
		"set-option -t alpha:     OK",
		"show-options -qv -t alpha @k: 'v'",
		"set-option -t alph @k2 v: OK <- PREFIX MATCHED (bad)",
		"has-session -t betab (unique prefix): exit=0 (0 = prefix matched)",
		"has-session -t '=betab':             can't find session: betab\nexit=1",
		"list-panes -t 'alpha:rev'  -> reviewer",
		"list-panes -t 'alpha:=rev' -> can't find window: rev\n   exit=1",
		"list-panes -t 'alpha:dup' picks: can't find window: dup",
		"set:   '1' exit=0",
		"unset: '' exit=0",
		"unset no -q: 'invalid option: @agentctl_absent' exit=1",
		"display-message -p -t $PANE_ID '#{session_name}': alpha",
	},
	"probe-3-ids.sh": {
		"created session id = '$0'",
		"set-option -t $SESSION_ID:  OK",
		"show-options -qv -t $SESSION_ID @agentctl_managed: '1'",
		"decoy 'alphabet' contaminated? managed=''",
		"list-windows -t $SESSION_ID: alpha",
		"has-session  -t $SESSION_ID: exit=0",
		"new-window   -t $SESSION_ID: @2 %2",
		"model set-to-empty via -F: ''",
		"never-set option via -F:   ''",
		"stdout='@4'",
		"killed by id OK",
		"remaining:\n  alphabet",
	},
	"probe-4-attach.sh": {
		"sid=$0",
		"attach-session -t $SESSION_ID     : open terminal failed: not a terminal",
		"attach-session -t '=alpha'  : open terminal failed: not a terminal",
		"attach-session -t '=nope'   : can't find session: nope",
		"-CC attach-session -t $SESSION_ID : tcgetattr failed: Operation not supported by device",
	},
}

func probeAssertion(t *testing.T, probeName, output string) (string, error) {
	t.Helper()
	outputFile := filepath.Join(t.TempDir(), "probe.out")
	writeTestFile(t, outputFile, []byte(output), 0o644)
	command := exec.Command("./release-verify.sh", "--assert-probe", probeName, outputFile)
	result, err := command.CombinedOutput()
	return string(result), err
}

func TestProbeAssertionsRequireEveryExpectedObservation(t *testing.T) {
	for probeName, evidence := range requiredProbeEvidence {
		probeName, evidence := probeName, evidence
		t.Run(probeName, func(t *testing.T) {
			validOutput := strings.Join(evidence, "\n") + "\n"
			if output, err := probeAssertion(t, probeName, validOutput); err != nil {
				t.Fatalf("valid probe output rejected: %v\n%s", err, output)
			}

			for index, missing := range evidence {
				incomplete := append([]string(nil), evidence[:index]...)
				incomplete = append(incomplete, evidence[index+1:]...)
				output, err := probeAssertion(t, probeName, strings.Join(incomplete, "\n")+"\n")
				if err == nil {
					t.Fatalf("probe output passed without %q", missing)
				}
				if !strings.Contains(output, "PROBE ASSERT FAIL ("+probeName+")") {
					t.Fatalf("missing assertion diagnostic for %q:\n%s", missing, output)
				}
			}
		})
	}
}

func TestProcessCheckPasses(t *testing.T) {
	got, err := processCheck(t, "testdata/release-verify-versions.txt", "testdata/release-verify-artifact")
	if err != nil {
		t.Fatalf("process-check failed: %v", err)
	}
	want := "PROCESS CHECK PASS (claude=2.1.220, codex=codex)\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestProcessCheckRejects(t *testing.T) {
	cases := []struct {
		name        string
		versions    string
		artifactDir string
	}{
		{"claude process mismatch", "testdata/release-verify-versions.txt", "testdata/release-verify-artifact-mismatch"},
		{"missing verify row", "testdata/release-verify-versions.txt", "testdata/release-verify-artifact-missing-row"},
		{"missing artifact dir", "testdata/release-verify-versions.txt", "testdata/absent-artifact"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := processCheck(t, tc.versions, tc.artifactDir)
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 1 {
				t.Fatalf("want exit status 1, got %v", err)
			}
		})
	}
}

type liveFixture struct {
	dir            string
	agentctlLog    string
	tmuxLog        string
	amqLog         string
	evidenceDirLog string
	sighupProbeLog string
}

func newLiveFixture(t *testing.T) liveFixture {
	t.Helper()
	dir := t.TempDir()
	for _, path := range []string{"hack", "bin", "docs", "stubs"} {
		if err := os.Mkdir(filepath.Join(dir, path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script, err := os.ReadFile("release-verify.sh")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, "hack/release-verify.sh"), script, 0o755)
	for name, evidence := range requiredProbeEvidence {
		body := "#!/usr/bin/env bash\nif IFS= read -r _; then echo 'stdin was not severed' >&2; exit 9; fi\ncat <<'PROBE_OUTPUT'\n" + strings.Join(evidence, "\n") + "\nPROBE_OUTPUT\n"
		writeTestFile(t, filepath.Join(dir, "hack", name), []byte(body), 0o755)
	}
	writeTestFile(t, filepath.Join(dir, "Makefile"), []byte("build:\n\t@true\n"), 0o644)
	writeTestFile(t, filepath.Join(dir, "docs/release-verification-notes.md"), []byte("# Notes\n\n## Results history\n"), 0o644)

	agentctlLog := filepath.Join(t.TempDir(), "agentctl.log")
	tmuxLog := filepath.Join(t.TempDir(), "tmux.log")
	amqLog := filepath.Join(t.TempDir(), "amq.log")
	evidenceDirLog := filepath.Join(t.TempDir(), "evidence-dir.log")
	sighupProbeLog := filepath.Join(t.TempDir(), "sighup-probe.log")
	agentctlOwned := filepath.Join(t.TempDir(), "owned")
	agentctlKilled := filepath.Join(t.TempDir(), "killed")
	agentctlRoleA := filepath.Join(t.TempDir(), "role-a")
	agentctlRoleB := filepath.Join(t.TempDir(), "role-b")
	agentctlRelaunched := filepath.Join(t.TempDir(), "relaunched")
	keeperOwned := filepath.Join(t.TempDir(), "keeper-owned")
	keeperKilled := filepath.Join(t.TempDir(), "keeper-killed")
	pgrepCalls := filepath.Join(t.TempDir(), "pgrep-calls")
	operatorHome := t.TempDir()
	sighupProbe := `#!/usr/bin/env bash
set -eu
[ "$#" -eq 4 ] || { echo 'probe-shim-sighup: expected --harness and --output' >&2; exit 64; }
[ "$1" = --harness ] || exit 64
harness=$2
[ "$3" = --output ] || exit 64
output=$4
case "$harness" in claude|codex) ;; *) exit 64 ;; esac
case "$output" in /*) ;; *) exit 64 ;; esac
[ ! -e "$output" ] || exit 64
printf '%s|%s\n' "$harness" "$output" >>"$AGENTCTL_TEST_SIGHUP_PROBE_LOG"
case "$harness" in
  claude) harness_version='2.1.220 (Claude Code)' ;;
  codex) harness_version='codex-cli 0.146.0' ;;
esac
cat >"$output" <<PROBE_OUTPUT
harness=$harness
harness_version=$harness_version
topology=shim-parent-of-harness-child-on-pty
shim_pid=123
child_pid=124
child_ppid_matches=true
child_tty=ttys999
child_command=/fixture/bin/$harness
signal_target=owned-shim-only
signal=SIGHUP
shim_terminated=true
child_outcome=terminated
default_tmux_targeted=false
PROBE_OUTPUT
echo "probe-shim-sighup: recorded $harness result in $output"
`
	writeTestFile(t, filepath.Join(dir, "hack/probe-shim-sighup.sh"), []byte(sighupProbe), 0o755)
	agentctl := `#!/usr/bin/env bash
set -u
printf '%s\n' "$*" >>"$AGENTCTL_TEST_LOG"
case "$1" in
  version)
    echo 'agentctl version test'
    ;;
  status)
    if [ "${AGENTCTL_TEST_NO_SERVER_PRECHECK:-0}" = 1 ] && [ ! -e "$AGENTCTL_TEST_KEEPER_OWNED" ] && [ ! -e "$AGENTCTL_TEST_OWNED" ] && [ ! -e "$AGENTCTL_TEST_KILLED" ]; then
      echo 'agentctl: tmux list sessions: exit status 1: error connecting to /private/tmp/tmux-501/default (No such file or directory)' >&2
      exit 6
    fi
    if [ "${AGENTCTL_TEST_COLLISION:-0}" = 1 ] && [ ! -e "$AGENTCTL_TEST_OWNED" ] && [ ! -e "$AGENTCTL_TEST_KILLED" ]; then
      echo 'relverify exists'
      exit 0
    fi
	if [ -e "$AGENTCTL_TEST_OWNED" ]; then
		if [ -e "$AGENTCTL_TEST_RELAUNCHED" ] && [ "${AGENTCTL_TEST_RELAUNCH_STATUS_FAIL:-0}" = 1 ]; then
		  echo 'agentctl: replacement runtime observation failed' >&2
		  exit 6
		fi
		echo 'SESSION ROLE HARNESS MODEL EFFORT CONFIDENCE SHIM CHILD PRESENTATION STATE FACTS'
		if [ -e "$AGENTCTL_TEST_ROLE_A" ]; then
			if [ -e "$AGENTCTL_TEST_RELAUNCHED" ]; then
				if [ "${AGENTCTL_TEST_REUSE_ORIGINAL_RUNTIME:-0}" = 1 ]; then
				  echo "$3 a claude default default anchored $AGENTCTL_TEST_SHIM_PID $AGENTCTL_TEST_CHILD_PID present running -"
				else
				  echo "$3 a claude default default anchored 301 302 present running -"
				fi
        elif kill -0 "$AGENTCTL_TEST_SHIM_PID" 2>/dev/null; then
          echo "$3 a claude default default anchored $AGENTCTL_TEST_SHIM_PID $AGENTCTL_TEST_CHILD_PID present running -"
        else
          echo "$3 a claude default default unanchored $AGENTCTL_TEST_SHIM_PID $AGENTCTL_TEST_CHILD_PID absent stale-record ESRCH"
        fi
      fi
      if [ -e "$AGENTCTL_TEST_ROLE_B" ]; then
        echo "$3 b codex default high anchored 205 206 present running -"
      fi
      exit 0
    fi
    if [ -e "$AGENTCTL_TEST_KILLED" ] && [ -n "${AGENTCTL_TEST_STATUS_AFTER_KILL_CODE:-}" ]; then
      status_message=${AGENTCTL_TEST_STATUS_AFTER_KILL_MESSAGE:-agentctl: transport failure}
      echo "${status_message//relverify/$3}" >&2
      exit "$AGENTCTL_TEST_STATUS_AFTER_KILL_CODE"
    fi
    if [ -n "${AGENTCTL_TEST_INITIAL_STATUS_MESSAGE:-}" ]; then
      status_message=${AGENTCTL_TEST_INITIAL_STATUS_MESSAGE//relverify/$3}
    else
      status_message="agentctl: session \"$3\" not found"
    fi
    echo "$status_message" >&2
    exit 3
    ;;
  launch)
    if [ "${AGENTCTL_TEST_REQUIRE_PART_B_AMQ_INIT:-0}" = 1 ]; then
      expected_amq_root="$(cat "$AGENTCTL_TEST_EVIDENCE_DIR_LOG")/part-b-amq"
      [ -f .amqrc ] || { echo 'Part B temporary .amqrc is missing' >&2; exit 2; }
      grep -Fq "\"root\": \"$expected_amq_root\"" .amqrc || { echo 'Part B temporary .amqrc has the wrong root' >&2; exit 2; }
      [ -d "$expected_amq_root" ] || { echo 'Part B temporary AMQ root is missing' >&2; exit 2; }
    fi
    if [ "${AGENTCTL_TEST_STALE_AMQ_RELVERIFY:-0}" = 1 ] && [ "$3" = relverify ]; then
      echo 'capture final coop wake owner: wake owner boot id is required' >&2
      exit 8
    fi
    if [ "${AGENTCTL_TEST_COLLISION:-0}" = 1 ]; then
      echo "agentctl: session \"$3\" already exists" >&2
      exit 3
    fi
    touch "$AGENTCTL_TEST_OWNED"
    touch "$AGENTCTL_TEST_ROLE_A"
    touch "$AGENTCTL_TEST_ROLE_B"
    echo "launched $3"
    ;;
  relaunch)
    touch "$AGENTCTL_TEST_ROLE_A" "$AGENTCTL_TEST_RELAUNCHED"
    echo "agentctl: relaunched role \"a\" in session \"$3\"; the shim is ready" >&2
    ;;
  kill)
    code=${AGENTCTL_TEST_PART_B_KILL_CODE:-0}
    if [ -n "${AGENTCTL_TEST_PART_B_KILL_CODES:-}" ]; then
      calls=0
      [ ! -e "$AGENTCTL_TEST_PART_B_KILL_CALLS" ] || calls=$(cat "$AGENTCTL_TEST_PART_B_KILL_CALLS")
      calls=$((calls + 1))
      printf '%s\n' "$calls" >"$AGENTCTL_TEST_PART_B_KILL_CALLS"
      IFS=, read -r -a codes <<<"$AGENTCTL_TEST_PART_B_KILL_CODES"
      index=$((calls - 1))
      [ "$index" -lt "${#codes[@]}" ] || index=$((${#codes[@]} - 1))
      code=${codes[$index]}
    fi
    if [ "$code" -ne 0 ]; then
      echo 'simulated relverify kill failure' >&2
      exit "$code"
    fi
    rm -f "$AGENTCTL_TEST_OWNED" "$AGENTCTL_TEST_ROLE_B" "$AGENTCTL_TEST_RELAUNCHED"
    touch "$AGENTCTL_TEST_KILLED"
    if [ "${AGENTCTL_TEST_REPLACE_PART_B_AMQ_CONFIG_AFTER_KILL:-0}" = 1 ]; then
      mv .amqrc .amqrc-original
      printf 'replacement config\n' >.amqrc
    fi
    if [ "${AGENTCTL_TEST_REPLACE_PART_B_AMQ_ROOT_AFTER_KILL:-0}" = 1 ]; then
      amq_root="$(cat "$AGENTCTL_TEST_EVIDENCE_DIR_LOG")/part-b-amq"
      mv "$amq_root" "$amq_root-original"
      mkdir "$amq_root"
      printf 'replacement root\n' >"$amq_root/sentinel"
    fi
    ;;
  *)
    exit 64
    ;;
esac
`
	writeTestFile(t, filepath.Join(dir, "bin/agentctl"), []byte(agentctl), 0o755)
	catCommand := `#!/usr/bin/env bash
set -u
if [ "${AGENTCTL_TEST_UNEXPECTED_EXIT_AFTER_PART_B_LAUNCH:-0}" = 1 ] && [ -e "$AGENTCTL_TEST_OWNED" ]; then
  echo 'simulated unexpected Part B exit' >&2
  exit 23
fi
exec /bin/cat "$@"
`
	writeTestFile(t, filepath.Join(dir, "stubs/cat"), []byte(catCommand), 0o755)
	tmux := `#!/usr/bin/env bash
set -u
printf '%s\n' "$*" >>"$AGENTCTL_TEST_TMUX_LOG"
[ "$1" = -V ] || exit 64
echo 'tmux 3.7b'
`
	writeTestFile(t, filepath.Join(dir, "stubs/tmux"), []byte(tmux), 0o755)
	writeTestFile(t, filepath.Join(dir, "stubs/claude"), []byte("#!/usr/bin/env bash\necho '2.1.220 (Claude Code)'\n"), 0o755)
	writeTestFile(t, filepath.Join(dir, "stubs/codex"), []byte("#!/usr/bin/env bash\necho 'codex-cli 0.146.0'\n"), 0o755)
	amq := `#!/usr/bin/env bash
set -u
printf '%s|%s|%s|%s\n' "$*" "$PWD" "$HOME" "$PATH" >>"$AGENTCTL_TEST_AMQ_LOG"
if [ "$1" = coop ] && [ "$2" = init ] && [ "$3" = --root ] && [ "$5" = --agents ] && [ "$6" = a,b,user ] && [ "$7" = --no-gitignore ] && [ "$#" -eq 7 ]; then
  mkdir -p "$4/meta"
  printf '{\n  "root": "%s"\n}\n' "$4" >.amqrc
  printf '{"agents":["a","b","user"]}\n' >"$4/meta/config.json"
  if [ "${AGENTCTL_TEST_PART_B_AMQ_INIT_FAIL_AFTER_CREATE:-0}" = 1 ]; then
    exit 17
  fi
  exit 0
fi
exit 64
`
	writeTestFile(t, filepath.Join(dir, "stubs/amq"), []byte(amq), 0o755)
	mktemp := `#!/usr/bin/env bash
set -u
created=$(/usr/bin/mktemp "$@") || exit $?
case "$*" in
  "-d /tmp/agentctl-release-verify.XXXXXX") printf '%s\n' "$created" >"$AGENTCTL_TEST_EVIDENCE_DIR_LOG" ;;
esac
printf '%s\n' "$created"
`
	writeTestFile(t, filepath.Join(dir, "stubs/mktemp"), []byte(mktemp), 0o755)
	pgrep := `#!/usr/bin/env bash
case "$*" in
  *agentctl-probe-*) exit 1 ;;
  *relverify*)
    calls=0
    [ ! -e "$AGENTCTL_TEST_PGREP_CALLS" ] || calls=$(cat "$AGENTCTL_TEST_PGREP_CALLS")
    calls=$((calls + 1))
    printf '%s\n' "$calls" >"$AGENTCTL_TEST_PGREP_CALLS"
    code=${AGENTCTL_TEST_PGREP_CODE:-1}
    if [ -n "${AGENTCTL_TEST_PGREP_CODES:-}" ]; then
      IFS=, read -r -a codes <<<"$AGENTCTL_TEST_PGREP_CODES"
      index=$((calls - 1))
      [ "$index" -lt "${#codes[@]}" ] || index=$((${#codes[@]} - 1))
      code=${codes[$index]}
    fi
    [ "$code" -eq 0 ] && echo '123 tmux relverify'
    [ "$code" -gt 1 ] && echo 'pgrep failed' >&2
    exit "$code"
    ;;
  *) exec /usr/bin/pgrep "$@" ;;
esac
`
	writeTestFile(t, filepath.Join(dir, "stubs/pgrep"), []byte(pgrep), 0o755)

	runCommand(t, dir, "git", "init", "-q")
	runCommand(t, dir, "git", "add", ".")
	runCommand(t, dir, "git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")

	t.Setenv("AGENTCTL_TEST_LOG", agentctlLog)
	t.Setenv("AGENTCTL_TEST_TMUX_LOG", tmuxLog)
	t.Setenv("AGENTCTL_TEST_AMQ_LOG", amqLog)
	t.Setenv("AGENTCTL_TEST_EVIDENCE_DIR_LOG", evidenceDirLog)
	t.Setenv("AGENTCTL_TEST_SIGHUP_PROBE_LOG", sighupProbeLog)
	t.Setenv("AGENTCTL_TEST_OWNED", agentctlOwned)
	t.Setenv("AGENTCTL_TEST_KILLED", agentctlKilled)
	t.Setenv("AGENTCTL_TEST_ROLE_A", agentctlRoleA)
	t.Setenv("AGENTCTL_TEST_ROLE_B", agentctlRoleB)
	t.Setenv("AGENTCTL_TEST_RELAUNCHED", agentctlRelaunched)
	t.Setenv("AGENTCTL_TEST_KEEPER_OWNED", keeperOwned)
	t.Setenv("AGENTCTL_TEST_KEEPER_KILLED", keeperKilled)
	t.Setenv("AGENTCTL_TEST_PART_B_KILL_CALLS", filepath.Join(t.TempDir(), "relverify-kill-calls"))
	t.Setenv("AGENTCTL_TEST_PGREP_CALLS", pgrepCalls)
	t.Setenv("HOME", operatorHome)
	t.Setenv("PATH", filepath.Join(dir, "stubs")+string(os.PathListSeparator)+os.Getenv("PATH"))
	return liveFixture{
		dir:            dir,
		agentctlLog:    agentctlLog,
		tmuxLog:        tmuxLog,
		amqLog:         amqLog,
		evidenceDirLog: evidenceDirLog,
		sighupProbeLog: sighupProbeLog,
	}
}

func TestLiveVerificationSuppliesEachHarnessContractToSIGHUPProbe(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 15))
	if err != nil {
		t.Fatalf("release verification did not satisfy the SIGHUP probe argument contract: %v\n%s", err, output)
	}
	invocations := strings.Split(strings.TrimSpace(readTestFile(t, fixture.sighupProbeLog)), "\n")
	if len(invocations) != 2 || !strings.HasPrefix(invocations[0], "claude|") || !strings.HasPrefix(invocations[1], "codex|") {
		t.Fatalf("SIGHUP probe invocations = %q, want exactly claude then codex", invocations)
	}
	for _, invocation := range invocations {
		parts := strings.SplitN(invocation, "|", 2)
		if len(parts) != 2 || !filepath.IsAbs(parts[1]) {
			t.Fatalf("SIGHUP probe invocation did not receive an absolute output path: %q", invocation)
		}
	}
}

func writeTestFile(t *testing.T, path string, body []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatal(err)
	}
}

func runCommand(t *testing.T, dir, name string, arguments ...string) {
	t.Helper()
	command := exec.Command(name, arguments...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, arguments, err, output)
	}
}

func (fixture liveFixture) run(t *testing.T, input string, environment ...string) (string, error) {
	return fixture.runWithTTY(t, input, false, environment...)
}

func (fixture liveFixture) runTTY(t *testing.T, input string, environment ...string) (string, error) {
	return fixture.runWithTTY(t, input, true, environment...)
}

func (fixture liveFixture) runWithTTY(t *testing.T, input string, tty bool, environment ...string) (string, error) {
	t.Helper()
	childPIDFile := filepath.Join(t.TempDir(), "child-pid")
	shimScript := `child_pid=''
on_hup() {
  kill -HUP "$child_pid"
  wait "$child_pid"
  exit 0
}
trap on_hup HUP
sleep 60 &
child_pid=$!
printf '%s\n' "$child_pid" >"$1"
wait "$child_pid"`
	shimProcess := exec.Command("bash", "-c", shimScript, "releaseverify-fixture", childPIDFile)
	if err := shimProcess.Start(); err != nil {
		t.Fatal(err)
	}
	shimDone := make(chan struct{})
	go func() {
		_ = shimProcess.Wait()
		close(shimDone)
	}()
	defer func() {
		_ = shimProcess.Process.Kill()
		<-shimDone
	}()
	var childPID string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(childPIDFile)
		if err == nil && strings.TrimSpace(string(body)) != "" {
			childPID = strings.TrimSpace(string(body))
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == "" {
		t.Fatal("fixture shim did not report its child PID")
	}
	command := exec.Command("bash", "hack/release-verify.sh", "--non-interactive")
	if tty {
		command = exec.Command("/usr/bin/script", "-q", "/dev/null", "bash", "hack/release-verify.sh", "--non-interactive")
	}
	command.Dir = fixture.dir
	command.Env = os.Environ()
	if tty {
		filtered := command.Env[:0]
		for _, entry := range command.Env {
			if !strings.HasPrefix(entry, "NO_COLOR=") {
				filtered = append(filtered, entry)
			}
		}
		command.Env = filtered
	}
	command.Env = append(command.Env, "AGENTCTL_TEST_SHIM_PID="+strconv.Itoa(shimProcess.Process.Pid))
	command.Env = append(command.Env, "AGENTCTL_TEST_CHILD_PID="+childPID)
	command.Env = append(command.Env, environment...)
	var output []byte
	var err error
	if tty {
		var captured bytes.Buffer
		command.Stdout = &captured
		command.Stderr = &captured
		stdin, pipeErr := command.StdinPipe()
		if pipeErr != nil {
			t.Fatal(pipeErr)
		}
		if startErr := command.Start(); startErr != nil {
			t.Fatal(startErr)
		}
		time.Sleep(2 * time.Second)
		if _, writeErr := stdin.Write([]byte(input)); writeErr != nil {
			_ = command.Process.Kill()
			t.Fatal(writeErr)
		}
		err = command.Wait()
		_ = stdin.Close()
		output = captured.Bytes()
	} else {
		command.Stdin = strings.NewReader(input)
		output, err = command.CombinedOutput()
	}
	normalized := string(output)
	if session, ok := fixture.observedLiveSession(); ok {
		normalized = strings.ReplaceAll(normalized, session, "relverify")
	}
	return normalized, err
}

func (fixture liveFixture) observedLiveSession() (string, bool) {
	body, err := os.ReadFile(fixture.evidenceDirLog)
	if err != nil {
		return "", false
	}
	evidenceRoot := strings.TrimSpace(string(body))
	token := strings.ToLower(strings.TrimPrefix(filepath.Ext(evidenceRoot), "."))
	if token == "" {
		return "", false
	}
	return "relverify_" + token, true
}

func (fixture liveFixture) liveSession(t *testing.T) string {
	t.Helper()
	session, ok := fixture.observedLiveSession()
	if !ok {
		t.Fatal("release verifier did not record a run-owned live session")
	}
	return session
}

func (fixture liveFixture) rawCalls(t *testing.T) []string {
	t.Helper()
	body, err := os.ReadFile(fixture.agentctlLog)
	if err != nil {
		t.Fatal(err)
	}
	return strings.FieldsFunc(strings.TrimSpace(string(body)), func(character rune) bool { return character == '\n' })
}

func (fixture liveFixture) calls(t *testing.T) []string {
	t.Helper()
	calls := fixture.rawCalls(t)
	for index := range calls {
		calls[index] = strings.ReplaceAll(calls[index], fixture.liveSession(t), "relverify")
	}
	return calls
}

func (fixture liveFixture) tmuxCalls(t *testing.T) []string {
	t.Helper()
	body, err := os.ReadFile(fixture.tmuxLog)
	if err != nil {
		t.Fatal(err)
	}
	return strings.FieldsFunc(strings.TrimSpace(string(body)), func(character rune) bool { return character == '\n' })
}

func TestLiveVerificationCompletesAndAppendsEvidence(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 3))
	if err != nil {
		t.Fatalf("release verification failed: %v\n%s", err, output)
	}
	for _, want := range []string{
		"=== Part A — Automated release checks ===",
		"[PASS A.",
		"=== Part B — Live release-candidate delivery ===",
		"===== OPERATOR ACTION B.A1: open the live role viewers =====",
		"./bin/agentctl attach --session relverify a",
		"./bin/agentctl attach --session relverify b",
		"VIEWER CLOSE PASS (roles a and b remain running after their viewers closed)",
		"Expected observation:",
		"operator confirmed:",
		"ALL VERIFIED — evidence appended",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	for index, probe := range []string{"probe-1-argv.sh", "probe-2-targeting.sh", "probe-3-ids.sh", "probe-4-attach.sh"} {
		want := fmt.Sprintf("[PASS A.%d] %s assertion completed", index+2, probe)
		if !strings.Contains(output, want) {
			t.Fatalf("probe completion missing %q:\n%s", want, output)
		}
	}
	notes, err := os.ReadFile(filepath.Join(fixture.dir, "docs/release-verification-notes.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(notes), "- Mode: `verify-live`; harness: `both`") {
		t.Fatalf("notes missing live evidence:\n%s", notes)
	}
	if got := strings.Count(string(notes), "- Mode: `verify-live`; harness: `both`"); got != 1 {
		t.Fatalf("notes contain %d appended live evidence blocks, want 1:\n%s", got, notes)
	}
	for _, want := range []string{
		"- Part A:",
		"- Part B:",
		"- Checkpoint B.C1 live Claude role a and Codex role b surfaces: operator confirmed: y",
		"- Checkpoint B.C2 fresh replacement Claude role a surface: PASS",
		"- Checkpoint B.C3 role viewer terminals closed: operator confirmed: y; script observed both roles still running",
	} {
		if !strings.Contains(string(notes), want) {
			t.Fatalf("notes missing %q:\n%s", want, notes)
		}
	}
	wantCalls := []string{
		"version",
		"status --session relverify",
		"launch --session relverify --roles a:claude,b:codex --efforts b:high",
		"status --session relverify",
		"relaunch --session relverify a",
		"status --session relverify",
		"status --session relverify",
		"kill --session relverify",
		"status --session relverify",
	}
	if got := fixture.calls(t); strings.Join(got, "\n") != strings.Join(wantCalls, "\n") {
		t.Fatalf("agentctl calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantCalls, "\n"))
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestLiveVerificationRequiresExactlyOneResultsHistoryMarker(t *testing.T) {
	tests := []struct {
		name      string
		notes     string
		wantCount string
	}{
		{name: "absent", notes: "# Notes\n", wantCount: "found 0"},
		{name: "substring", notes: "# Notes\n\nprefix ## Results history\n", wantCount: "found 0"},
		{name: "suffixed", notes: "# Notes\n\n## Results history (old)\n", wantCount: "found 0"},
		{name: "duplicate", notes: "# Notes\n\n## Results history\n\n## Results history\n", wantCount: "found 2"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLiveFixture(t)
			notesPath := filepath.Join(fixture.dir, "docs/release-verification-notes.md")
			writeTestFile(t, notesPath, []byte(test.notes), 0o644)
			runCommand(t, fixture.dir, "git", "add", "docs/release-verification-notes.md")
			runCommand(t, fixture.dir, "git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-qm", "marker fixture")

			output, err := fixture.run(t, strings.Repeat("y\n", 3))
			if err == nil {
				t.Fatalf("release verification accepted %s marker fixture:\n%s", test.name, output)
			}
			for _, want := range []string{"expected exactly one line equal to ## Results history", test.wantCount} {
				if !strings.Contains(output, want) {
					t.Fatalf("output missing %q for %s marker fixture:\n%s", want, test.name, output)
				}
			}
			if strings.Contains(output, "ALL VERIFIED") {
				t.Fatalf("release verification claimed success without one exact marker:\n%s", output)
			}
			if got := readTestFile(t, notesPath); got != test.notes {
				t.Fatalf("notes changed without one exact marker.\n--- got ---\n%s\n--- want ---\n%s", got, test.notes)
			}
		})
	}
}

func TestLiveVerificationUnexpectedExitReportsPartBCleanupFailure(t *testing.T) {
	fixture := newLiveFixture(t)
	bashEnvironment := filepath.Join(t.TempDir(), "bash-environment")
	writeTestFile(t, bashEnvironment, []byte(`if [ -z "${AGENTCTL_TEST_VERIFIER_BASHPID:-}" ]; then
  export AGENTCTL_TEST_VERIFIER_BASHPID=$BASHPID
fi
trap 'if [ "$BASHPID" = "$AGENTCTL_TEST_VERIFIER_BASHPID" ] && [ "${PART_B_SESSION_OWNED:-0}" = 1 ]; then trap - DEBUG; echo "simulated unexpected Part B exit" >&2; exit 23; fi' DEBUG
`), 0o644)
	output, err := fixture.run(t, "",
		"BASH_ENV="+bashEnvironment,
		"AGENTCTL_TEST_PART_B_KILL_CODE=17",
	)
	if err == nil {
		t.Fatalf("unexpected Part B exit with failed cleanup returned success:\n%s", output)
	}
	for _, want := range []string{
		"simulated unexpected Part B exit",
		"PART B CLEANUP FAIL (relverify kill exited 17)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "PART B CLEANUP PASS") {
		t.Fatalf("failed cleanup was claimed as passing:\n%s", output)
	}
	if _, statErr := os.Stat(os.Getenv("AGENTCTL_TEST_OWNED")); statErr != nil {
		t.Fatalf("fixture did not preserve the relverify ownership marker after failed kill: %v", statErr)
	}
	if _, statErr := os.Stat(os.Getenv("AGENTCTL_TEST_KILLED")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("fixture falsely marked relverify killed: %v", statErr)
	}
}

func TestLiveVerificationRetriesTransientPartBChildObservationOnce(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 15),
		"AGENTCTL_TEST_PART_B_KILL_CODES=9,5,0",
	)
	if err != nil {
		t.Fatalf("release verification did not recover from transient Part B child observation: %v\n%s", err, output)
	}
	for _, want := range []string{
		"PART B CLEANUP OBSERVED (relverify kill exited 9; retrying within bounded observation window)",
		"PART B CLEANUP PASS (relverify kill retry exited 0)",
		"ALL VERIFIED — evidence appended",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	if got := strings.TrimSpace(readTestFile(t, os.Getenv("AGENTCTL_TEST_PART_B_KILL_CALLS"))); got != "3" {
		t.Fatalf("Part B kill call count = %q, want 3", got)
	}
}

func TestLiveVerificationRejectedCheckpointArtifactCannotClaimPartBPass(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 2)+"n\n")
	if err == nil {
		t.Fatalf("release verification passed after B.C3 rejection:\n%s", output)
	}
	evidenceDir := strings.TrimSpace(readTestFile(t, fixture.evidenceDirLog))
	t.Cleanup(func() { _ = os.RemoveAll(evidenceDir) })
	metadataPath := filepath.Join(evidenceDir, "verify-live", "metadata.txt")
	metadata := readTestFile(t, metadataPath)
	wantResult := "part_b_result=FAIL — operator refused checkpoint B.C3"
	if !strings.Contains(metadata, wantResult) || strings.Contains(metadata, "part_b_result=PASS") {
		t.Fatalf("rejected Part B metadata is not fail-closed.\n--- metadata ---\n%s", metadata)
	}

	command := exec.Command("bash", "hack/release-verify.sh", "--render-results", filepath.Join(evidenceDir, "versions.txt"), filepath.Join(evidenceDir, "verify-live"))
	command.Dir = fixture.dir
	rendered, renderErr := command.CombinedOutput()
	if renderErr != nil {
		t.Fatalf("render rejected Part B artifact: %v\n%s", renderErr, rendered)
	}
	if !strings.Contains(string(rendered), "- Part B: FAIL — operator refused checkpoint B.C3") || strings.Contains(string(rendered), "- Part B: PASS") {
		t.Fatalf("renderer fabricated Part B success after B.C3 rejection:\n%s", rendered)
	}
}

func TestLiveVerificationZeroStatusExitBecomesFailureWhenCleanupFails(t *testing.T) {
	fixture := newLiveFixture(t)
	bashEnvironment := filepath.Join(t.TempDir(), "bash-environment")
	writeTestFile(t, bashEnvironment, []byte(`if [ -z "${AGENTCTL_TEST_VERIFIER_BASHPID:-}" ]; then
  export AGENTCTL_TEST_VERIFIER_BASHPID=$BASHPID
fi
trap 'if [ "$BASHPID" = "$AGENTCTL_TEST_VERIFIER_BASHPID" ] && [ "${PART_B_SESSION_OWNED:-0}" = 1 ]; then trap - DEBUG; echo "simulated zero-status exit after Part B launch" >&2; set +e; exit 0; fi' DEBUG
`), 0o644)
	output, err := fixture.run(t, "",
		"BASH_ENV="+bashEnvironment,
		"AGENTCTL_TEST_PART_B_KILL_CODE=17",
	)
	if err == nil {
		t.Fatalf("zero-status operation with failed cleanup returned success:\n%s", output)
	}
	for _, want := range []string{
		"simulated zero-status exit after Part B launch",
		"PART B CLEANUP FAIL (relverify kill exited 17)",
		"release-verify: Part B cleanup failed during exit",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestLiveVerificationKeepsOnlyThreeHumanSmokeCheckpoints(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 3))
	if err != nil {
		t.Fatalf("release verification failed: %v\n%s", err, output)
	}
	for _, checkpointID := range []string{"B.C1", "B.C2", "B.C3"} {
		for _, prefix := range []string{
			"===== OPERATOR CHECKPOINT " + checkpointID + ":",
			"===== END OPERATOR CHECKPOINT " + checkpointID + " =====",
			"[CHECKPOINT PASS " + checkpointID + "]",
		} {
			if !strings.Contains(output, prefix) {
				t.Fatalf("output missing numbered checkpoint result %q:\n%s", prefix, output)
			}
		}
	}
	if got := strings.Count(output, "===== OPERATOR CHECKPOINT "); got != 3 {
		t.Fatalf("human checkpoint count = %d, want exactly 3:\n%s", got, output)
	}
	for _, forbidden := range []string{
		"claude clear setup",
		"codex clear setup",
		"claude compact setup",
		"type junk",
		"type /skills",
		"present-not-ours",
		"=== Part C",
	} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("live smoke retained duplicate human feature drill %q:\n%s", forbidden, output)
		}
	}
	for _, call := range fixture.rawCalls(t) {
		if strings.HasPrefix(call, "clear ") || strings.HasPrefix(call, "compact ") || strings.Contains(call, "launch --session skillverify") {
			t.Fatalf("live smoke executed duplicate feature drill: %q", call)
		}
	}
}

func TestLiveVerificationTTYUsesSemanticColors(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.runTTY(t, strings.Repeat("y\n", 3))
	if err != nil {
		t.Fatalf("TTY live verification failed: %v\n%s", err, output)
	}
	for _, want := range []string{
		"\x1b[1;36m  ./bin/agentctl attach --session relverify a\x1b[0m",
		"\x1b[1;34mKeep this script running in the verifier terminal.\x1b[0m",
		"\x1b[1;35mClaude role a\x1b[1;34m",
		"\x1b[1;33mCodex role b\x1b[1;34m",
		"\x1b[1;32m[CHECKPOINT PASS B.C1]",
		"\x1b[1;97;45m===== OPERATOR CHECKPOINT B.C1:",
		"\x1b[1;30;46m===== OPERATOR CHECKPOINT B.C2:",
		"\x1b[1;97;44m===== OPERATOR CHECKPOINT B.C3:",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("TTY output omits semantic colour sequence %q:\n%s", want, output)
		}
	}
	operatorStart := strings.Index(output, "===== OPERATOR ACTION B.A1:")
	operatorEnd := strings.Index(output, "===== END OPERATOR CHECKPOINT B.C3 =====")
	if operatorStart < 0 || operatorEnd <= operatorStart {
		t.Fatalf("TTY output omits the bounded operator flow:\n%s", output)
	}
	operatorFlow := output[operatorStart:operatorEnd]
	for _, role := range []struct {
		phrase, style string
	}{
		{phrase: "Claude role a", style: "\x1b[1;35m"},
		{phrase: "Codex role b", style: "\x1b[1;33m"},
	} {
		if got, want := strings.Count(operatorFlow, role.style+role.phrase), strings.Count(operatorFlow, role.phrase); got != want {
			t.Fatalf("%s semantic colour count = %d, want every one of %d operator occurrences:\n%s", role.phrase, got, want, operatorFlow)
		}
	}
	if got, want := strings.Count(operatorFlow, "\x1b[1;36m  ./bin/agentctl attach"), strings.Count(operatorFlow, "  ./bin/agentctl attach"); got != want {
		t.Fatalf("typed-command colour count = %d, want every one of %d operator commands:\n%s", got, want, operatorFlow)
	}
}

func TestLiveVerificationNOColorAndCapturedEvidenceStayByteClean(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.runTTY(t, strings.Repeat("y\n", 3), "NO_COLOR=")
	if err != nil {
		t.Fatalf("NO_COLOR TTY live verification failed: %v\n%s", err, output)
	}
	if strings.Contains(output, "\x1b[") {
		t.Fatalf("NO_COLOR TTY output contains ANSI escapes:\n%q", output)
	}

	fixture = newLiveFixture(t)
	output, err = fixture.run(t, strings.Repeat("y\n", 3))
	if err != nil {
		t.Fatalf("captured live verification failed: %v\n%s", err, output)
	}
	if strings.Contains(output, "\x1b[") {
		t.Fatalf("non-TTY captured output contains ANSI escapes:\n%q", output)
	}
	evidenceRoot := strings.TrimSpace(readTestFile(t, fixture.evidenceDirLog))
	t.Cleanup(func() { _ = os.RemoveAll(evidenceRoot) })
	for _, path := range []string{
		filepath.Join(fixture.dir, "docs/release-verification-notes.md"),
		evidenceRoot,
	} {
		walkErr := filepath.Walk(path, func(current string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info.IsDir() {
				return nil
			}
			body, readErr := os.ReadFile(current)
			if readErr != nil {
				return readErr
			}
			if strings.Contains(string(body), "\x1b[") {
				t.Errorf("captured evidence contains ANSI escapes: %s", current)
			}
			return nil
		})
		if walkErr != nil {
			t.Fatal(walkErr)
		}
	}
}

func TestLiveVerificationOperatorProseUsesLogicalUnwrappedLines(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 3))
	if err != nil {
		t.Fatalf("live verification failed: %v\n%s", err, output)
	}
	for _, want := range []string{
		"The Claude role a viewer shows a ready Claude harness, and the Codex role b viewer shows a ready Codex harness. The detached roles continue running independently of those viewers.",
		"The script observed the original Claude role a child become absent, agentctl relaunched Claude role a from the fleet's stored configuration, and the replacement runtime has different shim and child identities.",
		"Close the Claude role a viewer and Codex role b viewer by closing each viewer's terminal window or tab, or by otherwise closing each viewer's PTY at the terminal boundary.",
	} {
		if !strings.Contains(output, want+"\n") {
			t.Fatalf("operator output omits one logical unwrapped line %q:\n%s", want, output)
		}
	}
	for _, stale := range []string{
		"Codex role b\nviewer",
		"agentctl relaunched\nrole a",
		"terminal window or tab,\nor by otherwise",
	} {
		if strings.Contains(output, stale) {
			t.Fatalf("operator output retains manual wrapping %q:\n%s", stale, output)
		}
	}
}

func TestLiveVerificationBoundsEveryHumanStepAndUsesHarnessRoleAnchors(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 15))
	if err != nil {
		t.Fatalf("release verification failed: %v\n%s", err, output)
	}
	blocks := []struct {
		kind, id string
		wants    []string
	}{
		{kind: "ACTION", id: "B.A1", wants: []string{"verifier terminal", "Claude role a viewer", "Codex role b viewer"}},
		{kind: "CHECKPOINT", id: "B.C1", wants: []string{"Claude role a viewer", "Codex role b viewer"}},
		{kind: "ACTION", id: "B.A2", wants: []string{"Claude role a viewer", "replacement Claude role a"}},
		{kind: "CHECKPOINT", id: "B.C2", wants: []string{"Claude role a viewer", "fresh, ready"}},
		{kind: "ACTION", id: "B.A3", wants: []string{"Claude role a viewer", "Codex role b viewer", "closing each viewer's terminal window or tab"}},
		{kind: "CHECKPOINT", id: "B.C3", wants: []string{"Claude role a viewer", "Codex role b viewer", "closed"}},
	}
	for _, block := range blocks {
		start := "===== OPERATOR " + block.kind + " " + block.id + ":"
		end := "===== END OPERATOR " + block.kind + " " + block.id + " ====="
		startAt := strings.Index(output, start)
		endAt := strings.Index(output, end)
		if startAt < 0 || endAt <= startAt {
			t.Fatalf("operator block %s %s is not visibly bounded:\n%s", block.kind, block.id, output)
		}
		body := output[startAt:endAt]
		for _, want := range block.wants {
			if !strings.Contains(body, want) {
				t.Fatalf("operator block %s %s omits human anchor %q:\n%s", block.kind, block.id, want, body)
			}
		}
	}
	if regexp.MustCompile(`(?i)\bwindow[[:space:]]+[0-9]+\b`).MatchString(output) {
		t.Fatalf("operator guidance relies on human-ambiguous window numbers:\n%s", output)
	}
}

func TestLiveVerificationDoesNotCloseViewerBeforeSurfaceCheckpoint(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 3))
	if err != nil {
		t.Fatalf("release verification failed: %v\n%s", err, output)
	}
	positions := map[string]int{
		"live surface checkpoint":        strings.Index(output, "===== OPERATOR CHECKPOINT B.C1:"),
		"replacement surface checkpoint": strings.Index(output, "===== OPERATOR CHECKPOINT B.C2:"),
		"viewer close instruction":       strings.Index(output, "===== OPERATOR ACTION B.A3:"),
		"viewer closure checkpoint":      strings.Index(output, "===== OPERATOR CHECKPOINT B.C3:"),
	}
	for name, position := range positions {
		if position < 0 {
			t.Fatalf("output omits %s:\n%s", name, output)
		}
	}
	if positions["live surface checkpoint"] >= positions["viewer close instruction"] ||
		positions["replacement surface checkpoint"] >= positions["viewer close instruction"] ||
		positions["viewer close instruction"] >= positions["viewer closure checkpoint"] {
		t.Fatalf("viewer-close instruction is not after every surface checkpoint: positions=%v\n%s", positions, output)
	}
	closedSuffix := output[positions["viewer close instruction"]:]
	for _, staleQuestion := range []string{"skill inventory", "present-not-ours", "fresh, ready claude input surface"} {
		if strings.Contains(closedSuffix, staleQuestion) {
			t.Fatalf("checkpoint requiring a live viewer follows the close instruction (%q):\n%s", staleQuestion, closedSuffix)
		}
	}
}

func TestLiveVerificationRequiresAMQBeforePartB(t *testing.T) {
	fixture := newLiveFixture(t)
	if err := os.Remove(filepath.Join(fixture.dir, "stubs", "amq")); err != nil {
		t.Fatal(err)
	}
	runCommand(t, fixture.dir, "git", "add", "stubs/amq")
	runCommand(t, fixture.dir, "git", "-c", "commit.gpgsign=false", "commit", "-m", "remove amq stub")
	pathWithoutAMQ := filepath.Join(fixture.dir, "stubs") + string(os.PathListSeparator) + "/usr/bin:/bin"
	output, err := fixture.run(t, strings.Repeat("y\n", 10), "PATH="+pathWithoutAMQ)
	if err == nil {
		t.Fatalf("release verification passed without amq:\n%s", output)
	}
	if !strings.Contains(output, "required command not found: amq") {
		t.Fatalf("missing amq was not rejected in preflight:\n%s", output)
	}
	if strings.Contains(output, "=== Part B") || strings.Contains(output, "[CHECKPOINT B.C1]") {
		t.Fatalf("Part B began before the missing amq dependency was reported:\n%s", output)
	}
}

func TestLiveVerificationDoesNotCallRefusalOnCheckpointEOF(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 2))
	if err == nil {
		t.Fatalf("release verification accepted checkpoint EOF:\n%s", output)
	}
	if !strings.Contains(output, "input closed — answer y or n") || strings.Contains(output, "operator refused checkpoint:") {
		t.Fatalf("checkpoint EOF was reported as refusal:\n%s", output)
	}
}

func TestLiveVerificationRejectsDetachedRoleAttachmentsAndTearsDown(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, "n\n")
	if err == nil {
		t.Fatalf("release verification accepted detached-role-attachment refusal:\n%s", output)
	}
	if !strings.Contains(output, "[CHECKPOINT FAIL B.C1] operator refused checkpoint: live role surfaces") {
		t.Fatalf("output missing detached-role-attachment refusal:\n%s", output)
	}
	calls := strings.Join(fixture.calls(t), "\n")
	if !strings.Contains(calls, "kill --session relverify") || strings.Contains(calls, "clear --session relverify") || strings.Contains(calls, "compact --session relverify") || strings.Contains(calls, "relaunch --session relverify") {
		t.Fatalf("Part B commands continued after detached-role-attachment refusal:\n%s", calls)
	}
}

func TestLiveVerificationRelaunchesClaudeFromStoredQuadByExactIDs(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 3))
	if err != nil {
		t.Fatalf("release verification failed: %v\n%s", err, output)
	}
	for _, want := range []string{
		"RELAUNCH PASS (role a relaunched through the ESRCH-gated command)",
		`agentctl: relaunched role "a" in session "relverify"; the shim is ready`,
		"RELAUNCH PASS (replacement role a runtime identities observed)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	wantAgentctl := []string{
		"version",
		"status --session relverify",
		"launch --session relverify --roles a:claude,b:codex --efforts b:high",
		"status --session relverify",
		"relaunch --session relverify a",
		"status --session relverify",
		"status --session relverify",
		"kill --session relverify",
		"status --session relverify",
	}
	if got := fixture.calls(t); strings.Join(got, "\n") != strings.Join(wantAgentctl, "\n") {
		t.Fatalf("agentctl calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantAgentctl, "\n"))
	}
	wantTmux := []string{"-V"}
	gotTmux := fixture.tmuxCalls(t)
	if strings.Join(gotTmux, "\n") != strings.Join(wantTmux, "\n") {
		t.Fatalf("tmux calls:\n%s\nwant:\n%s", strings.Join(gotTmux, "\n"), strings.Join(wantTmux, "\n"))
	}
	notes, err := os.ReadFile(filepath.Join(fixture.dir, "docs/release-verification-notes.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"- Checkpoint B.C2 fresh replacement Claude role a surface: PASS (old child absent; replacement runtime identities observed; Claude role a viewer reattached); operator confirmed: y",
		"- Teardown status: exit 3 (detached durable fleet absent)",
	} {
		if !strings.Contains(string(notes), want) {
			t.Fatalf("notes missing %q:\n%s", want, notes)
		}
	}
}

// This catches the default live walkthrough drifting back to the tmux-only
// presentation after launch has created a detached fleet. The script itself
// runs; the fixture controls only its agentctl, tmux, and process boundaries.
func TestLiveVerificationDetachedRelaunchUsesRuntimeRecordsAndExplicitRoleAttach(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 3))
	if err != nil {
		t.Fatalf("detached live verification failed: %v\n%s", err, output)
	}
	for _, want := range []string{
		"./bin/agentctl attach --session relverify a",
		"./bin/agentctl attach --session relverify b",
		"RELAUNCH PASS (replacement role a runtime identities observed)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("detached live output omits %q:\n%s", want, output)
		}
	}
	if got := strings.Count(output, "./bin/agentctl attach --session relverify a"); got < 2 {
		t.Fatalf("role a attach guidance occurred %d times, want initial attach and post-relaunch reattach:\n%s", got, output)
	}
	for _, stale := range []string{"Command Menu", "pane ID", "press esc to detach"} {
		if strings.Contains(output, stale) {
			t.Fatalf("detached Part B output retains tmux-era claim %q:\n%s", stale, output)
		}
	}
	for _, call := range fixture.tmuxCalls(t) {
		if call == "list-sessions" || strings.HasPrefix(call, "list-sessions ") || call == "list-windows" || strings.HasPrefix(call, "list-windows ") {
			t.Fatalf("detached Part B resolved fleet identity through tmux: %q", call)
		}
	}
	t.Log("full-default verifier used detached runtime records and explicit role attach")
}

func TestLiveVerificationUsesRunUniqueSessionOutsideStaleAMQRelverify(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 15), "AGENTCTL_TEST_STALE_AMQ_RELVERIFY=1")
	if err != nil {
		t.Fatalf("stale AMQ relverify namespace blocked live verification: %v\n%s", err, output)
	}
	var launch string
	session := fixture.liveSession(t)
	for _, call := range fixture.rawCalls(t) {
		if strings.HasPrefix(call, "launch --session ") && strings.Contains(call, " --roles a:claude,b:codex --efforts b:high") {
			launch = call
		}
		if strings.Contains(call, "--session relverify") && !strings.Contains(call, "--session "+session) {
			t.Fatalf("Part B command escaped the run-owned session %q: %q", session, call)
		}
	}
	if launch == "" {
		t.Fatalf("live verification did not launch Part B:\n%s", output)
	}
	if strings.HasPrefix(launch, "launch --session relverify ") || !strings.HasPrefix(launch, "launch --session relverify_") {
		t.Fatalf("Part B launch session was not run-unique: %q", launch)
	}
	suffix := strings.TrimPrefix(session, "relverify_")
	if suffix == "" || len(session) > 32 || strings.IndexFunc(suffix, func(character rune) bool {
		return (character < 'a' || character > 'z') && (character < '0' || character > '9')
	}) >= 0 {
		t.Fatalf("run-owned session does not satisfy its lowercase safe-token contract: %q", session)
	}
	metadata := readTestFile(t, filepath.Join(strings.TrimSpace(readTestFile(t, fixture.evidenceDirLog)), "verify-live", "metadata.txt"))
	if !strings.Contains(metadata, "part_b_session="+session+"\n") {
		t.Fatalf("live evidence does not record its run-owned session %q:\n%s", session, metadata)
	}
	script := readTestFile(t, "release-verify.sh")
	fixedSessionCommand := regexp.MustCompile(`--session relverify(?:[^a-z0-9_]|$)`)
	for _, mutant := range []string{
		"./bin/agentctl status --session relverify\n",
		`echo "./bin/agentctl status --session relverify"`,
		"./bin/agentctl attach --session relverify a\n",
	} {
		if !fixedSessionCommand.MatchString(mutant) {
			t.Fatalf("fixed-session source guard missed %q", mutant)
		}
	}
	for _, dynamic := range []string{"--session relverify_%s", "--session relverify_abc123 a"} {
		if fixedSessionCommand.MatchString(dynamic) {
			t.Fatalf("fixed-session source guard rejected dynamic command %q", dynamic)
		}
	}
	if fixedSessionCommand.MatchString(script) {
		t.Fatal("live verifier retains a displayed or executed fixed-session command")
	}
}

func TestLiveVerificationOwnsTemporaryAMQConfigBeforeDetachedLaunchAndRestoresCheckout(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 15), "AGENTCTL_TEST_REQUIRE_PART_B_AMQ_INIT=1")
	if err != nil {
		t.Fatalf("live verifier did not initialize AMQ before detached launch: %v\n%s", err, output)
	}
	evidenceRoot := strings.TrimSpace(readTestFile(t, fixture.evidenceDirLog))
	if _, statErr := os.Lstat(filepath.Join(fixture.dir, ".amqrc")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("temporary Part B .amqrc survived verification: %v", statErr)
	}
	if _, statErr := os.Lstat(filepath.Join(evidenceRoot, "part-b-amq")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("temporary Part B AMQ root survived verification: %v", statErr)
	}
	amqCalls := strings.Split(strings.TrimSpace(readTestFile(t, fixture.amqLog)), "\n")
	if len(amqCalls) != 1 || !strings.HasPrefix(amqCalls[0], "coop init --root "+evidenceRoot+"/part-b-amq --agents a,b,user --no-gitignore|") {
		t.Fatalf("first AMQ call was not the owned Part B initialization: %q", amqCalls)
	}
}

func TestLiveVerificationRendersDerivedProbeCountAndPartBAMQOwnership(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 3))
	if err != nil {
		t.Fatalf("live verifier failed: %v\n%s", err, output)
	}
	notes := readTestFile(t, filepath.Join(fixture.dir, "docs/release-verification-notes.md"))
	for _, want := range []string{
		"- Probes: 6 completed, no surviving throwaway server",
		"- Part B AMQ mode: temporary (verifier-owned .amqrc and root)",
	} {
		if !strings.Contains(notes, want) {
			t.Fatalf("rendered notes omit %q:\n%s", want, notes)
		}
	}
}

func TestLiveVerificationRetainsSubstitutedPartBAMQArtifacts(t *testing.T) {
	cases := []struct {
		name        string
		environment string
		relative    string
		wantBody    string
		wantOutput  string
	}{
		{
			name:        "config",
			environment: "AGENTCTL_TEST_REPLACE_PART_B_AMQ_CONFIG_AFTER_KILL=1",
			relative:    ".amqrc",
			wantBody:    "replacement config\n",
			wantOutput:  "PART B AMQ CLEANUP FAIL (temporary config identity changed:",
		},
		{
			name:        "root",
			environment: "AGENTCTL_TEST_REPLACE_PART_B_AMQ_ROOT_AFTER_KILL=1",
			relative:    "part-b-amq/sentinel",
			wantBody:    "replacement root\n",
			wantOutput:  "PART B AMQ CLEANUP FAIL (temporary root identity changed:",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newLiveFixture(t)
			output, err := fixture.run(t, strings.Repeat("y\n", 3), tc.environment)
			if err == nil {
				t.Fatalf("live verifier removed substituted AMQ %s:\n%s", tc.name, output)
			}
			evidenceRoot := strings.TrimSpace(readTestFile(t, fixture.evidenceDirLog))
			t.Cleanup(func() { _ = os.RemoveAll(evidenceRoot) })
			path := filepath.Join(evidenceRoot, tc.relative)
			if tc.name == "config" {
				path = filepath.Join(fixture.dir, tc.relative)
			}
			if got := readTestFile(t, path); got != tc.wantBody {
				t.Fatalf("substituted %s body = %q, want %q", tc.name, got, tc.wantBody)
			}
			if !strings.Contains(output, tc.wantOutput) {
				t.Fatalf("live verifier omitted substitution failure %q:\n%s", tc.wantOutput, output)
			}
		})
	}
}

func TestLiveVerificationPreservesPreexistingAMQConfig(t *testing.T) {
	fixture := newLiveFixture(t)
	writeTestFile(t, filepath.Join(fixture.dir, ".git", "info", "exclude"), []byte(".amqrc\n"), 0o644)
	existingRoot := filepath.Join(t.TempDir(), "operator-amq")
	if err := os.Mkdir(existingRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	config := "{\n  \"root\": \"" + existingRoot + "\"\n}\n"
	writeTestFile(t, filepath.Join(fixture.dir, ".amqrc"), []byte(config), 0o600)
	output, err := fixture.run(t, strings.Repeat("y\n", 15))
	if err != nil {
		t.Fatalf("live verifier failed with an existing AMQ config: %v\n%s", err, output)
	}
	if got := readTestFile(t, filepath.Join(fixture.dir, ".amqrc")); got != config {
		t.Fatalf("preexisting .amqrc changed: got %q want %q", got, config)
	}
	metadata := readTestFile(t, filepath.Join(strings.TrimSpace(readTestFile(t, fixture.evidenceDirLog)), "verify-live", "metadata.txt"))
	if !strings.Contains(metadata, "part_b_amq_mode=existing\n") {
		t.Fatalf("metadata did not record existing AMQ mode:\n%s", metadata)
	}
	notes := readTestFile(t, filepath.Join(fixture.dir, "docs/release-verification-notes.md"))
	if !strings.Contains(notes, "- Part B AMQ mode: existing (pre-existing .amqrc; verifier removed no AMQ path)") {
		t.Fatalf("rendered notes did not surface existing AMQ mode:\n%s", notes)
	}
	if body, readErr := os.ReadFile(fixture.amqLog); readErr == nil && strings.Contains(string(body), "part-b-amq") {
		t.Fatal("verifier initialized a temporary Part B root despite an existing .amqrc")
	} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		t.Fatalf("read AMQ log: %v", readErr)
	}
}

func TestLiveVerificationCleansPartialAMQInitializationFailure(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, "", "AGENTCTL_TEST_PART_B_AMQ_INIT_FAIL_AFTER_CREATE=1")
	if err == nil {
		t.Fatalf("live verifier accepted a partial AMQ initialization failure:\n%s", output)
	}
	evidenceRoot := strings.TrimSpace(readTestFile(t, fixture.evidenceDirLog))
	for _, path := range []string{filepath.Join(fixture.dir, ".amqrc"), filepath.Join(evidenceRoot, "part-b-amq")} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("partial AMQ initialization artifact survived at %s: %v", path, statErr)
		}
	}
	for _, want := range []string{
		"PART B AMQ INIT FAIL (amq coop init exited 17)",
		"PART B AMQ CLEANUP PASS (temporary .amqrc removed)",
		"PART B AMQ CLEANUP PASS (temporary root removed)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("partial AMQ failure output missing %q:\n%s", want, output)
		}
	}
}

// This catches a later status checkpoint overwriting the evidence that proves
// what the verifier observed earlier in the same run.
func TestLiveVerificationPreservesDistinctStatusEvidenceAtEveryCheckpoint(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 15),
		"AGENTCTL_TEST_STATUS_AFTER_KILL_CODE=3",
		`AGENTCTL_TEST_STATUS_AFTER_KILL_MESSAGE=agentctl: session "relverify" has no durable fleet configuration`,
	)
	if err != nil {
		t.Fatalf("live verification failed: %v\n%s", err, output)
	}
	evidenceRoot := strings.TrimSpace(readTestFile(t, fixture.evidenceDirLog))
	artifactDir := filepath.Join(evidenceRoot, "verify-live")
	checks := map[string]string{
		"precheck.stderr":        `agentctl: session "relverify" not found`,
		"relaunch-before.status": "relverify a claude default default anchored",
		"relaunch-after.status":  "relverify a claude default default anchored 301 302",
		"viewer-close.status":    "relverify b codex default high anchored",
		"teardown.stderr":        `agentctl: session "relverify" has no durable fleet configuration`,
	}
	for name, want := range checks {
		body, readErr := os.ReadFile(filepath.Join(artifactDir, name))
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		observed := strings.ReplaceAll(string(body), fixture.liveSession(t), "relverify")
		if !strings.Contains(observed, want) {
			t.Fatalf("%s = %q, want observation containing %q", name, body, want)
		}
	}
}

func TestLiveVerificationPreservesFailedReplacementStatusDiagnosticBeforeTeardown(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 8), "AGENTCTL_TEST_RELAUNCH_STATUS_FAIL=1")
	if err == nil {
		t.Fatalf("live verification accepted failed replacement status observation:\n%s", output)
	}
	evidenceRoot := strings.TrimSpace(readTestFile(t, fixture.evidenceDirLog))
	artifactDir := filepath.Join(evidenceRoot, "verify-live")
	if got := readTestFile(t, filepath.Join(artifactDir, "relaunch-after.stderr")); !strings.Contains(got, "replacement runtime observation failed") {
		t.Fatalf("relaunch-after.stderr = %q, want replacement observation diagnostic", got)
	}
	if got := strings.ReplaceAll(readTestFile(t, filepath.Join(artifactDir, "teardown.stderr")), fixture.liveSession(t), "relverify"); !strings.Contains(got, `session "relverify" not found`) {
		t.Fatalf("teardown.stderr = %q, want independent teardown observation", got)
	}
}

func TestLiveVerificationWaitsForRecordedClaudeChildAbsenceBeforeOneRelaunch(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 15))
	if err != nil {
		t.Fatalf("release verification did not wait for recorded child absence: %v\n%s", err, output)
	}
	for _, want := range []string{
		"RELAUNCH PASS (recorded role a child no longer responds to signal 0)",
		"RELAUNCH PASS (role a relaunched through the ESRCH-gated command)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("release verification omitted %q:\n%s", want, output)
		}
	}
}

func TestLiveVerificationPairsReplacementRuntimeWithFreshSurfaceObservation(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 15))
	if err != nil {
		t.Fatalf("release verification failed: %v\n%s", err, output)
	}
	for _, want := range []string{
		"===== OPERATOR ACTION B.A2: attach the replacement Claude role a viewer =====",
		"RELAUNCH PASS (replacement role a runtime identities observed)",
		"./bin/agentctl attach --session relverify a",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	wantPrompt := "The Claude role a viewer shows the fresh, ready replacement harness. The script observed the original Claude role a child become absent, agentctl relaunched Claude role a from the fleet's stored configuration, and the replacement runtime has different shim and child identities.\nDoes the Claude role a viewer show the fresh, ready replacement harness?"
	if !strings.Contains(output, wantPrompt) {
		t.Fatalf("release verifier missing final relaunch prompt:\n%s", wantPrompt)
	}
}

func TestLiveVerificationRejectsReusedRelaunchRuntimeIdentity(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 8), "AGENTCTL_TEST_REUSE_ORIGINAL_RUNTIME=1")
	if err == nil {
		t.Fatalf("release verification accepted reused runtime identities:\n%s", output)
	}
	if !strings.Contains(output, "RELAUNCH FAIL (replacement runtime reused an original identity") {
		t.Fatalf("output missing reused-runtime failure:\n%s", output)
	}
}

func TestLiveVerificationPrintsProbeDiagnosticsOnAssertionFailure(t *testing.T) {
	fixture := newLiveFixture(t)
	writeTestFile(t, filepath.Join(fixture.dir, "hack/probe-1-argv.sh"), []byte("#!/usr/bin/env bash\necho 'tmux diagnostic: observed broken behavior'\n"), 0o755)
	runCommand(t, fixture.dir, "git", "add", "hack/probe-1-argv.sh")
	runCommand(t, fixture.dir, "git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-qm", "broken probe")
	output, err := fixture.run(t, "")
	if err == nil {
		t.Fatalf("release verification passed without required probe evidence:\n%s", output)
	}
	for _, want := range []string{
		"tmux diagnostic: observed broken behavior",
		"PROBE ASSERT FAIL (probe-1-argv.sh)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestLiveVerificationRejectsAttestationAndTearsDown(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, "y\ny\nn\n")
	if err == nil {
		t.Fatalf("release verification passed after rejection:\n%s", output)
	}
	calls := strings.Join(fixture.calls(t), "\n")
	if !strings.Contains(calls, "kill --session relverify") {
		t.Fatalf("kill not attempted after rejection:\n%s", calls)
	}
	if strings.Contains(calls, "clear --session relverify b") || strings.Contains(calls, "compact --session relverify a") {
		t.Fatalf("flow continued after rejection:\n%s", calls)
	}
	for _, want := range []string{"TEARDOWN PASS (agentctl status exit 3 proves relverify is absent)"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q after rejection:\n%s", want, output)
		}
	}
}

func TestLiveVerificationRefusesExistingSessionWithoutKilling(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, "", "AGENTCTL_TEST_COLLISION=1")
	if err == nil {
		t.Fatalf("release verification passed with existing session:\n%s", output)
	}
	calls := strings.Join(fixture.calls(t), "\n")
	if strings.Contains(calls, "launch --session relverify") || strings.Contains(calls, "kill --session relverify") {
		t.Fatalf("existing session was launched or killed:\n%s", calls)
	}
}

func TestLiveVerificationRefusesTmuxDependentDetachedPrecheckWithoutCreatingKeeper(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, "", "AGENTCTL_TEST_NO_SERVER_PRECHECK=1")
	if err == nil {
		t.Fatalf("detached verifier accepted a tmux-dependent status precheck:\n%s", output)
	}
	if !strings.Contains(output, "could not prove detached session relverify is absent") {
		t.Fatalf("detached precheck failure was not reported:\n%s", output)
	}
	for _, call := range fixture.tmuxCalls(t) {
		if strings.HasPrefix(call, "new-session ") || strings.HasPrefix(call, "kill-session ") {
			t.Fatalf("detached verifier created or killed a tmux keeper: %q", call)
		}
	}
}

func TestLiveVerificationRejectsUnexpectedStatusFailure(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 12), "AGENTCTL_TEST_STATUS_AFTER_KILL_CODE=6", "AGENTCTL_TEST_STATUS_AFTER_KILL_MESSAGE=agentctl: transport failure")
	if err == nil || !strings.Contains(output, "TEARDOWN FAIL (agentctl status") {
		t.Fatalf("unexpected status failure was not rejected: err=%v\n%s", err, output)
	}
	evidenceRoot := strings.TrimSpace(readTestFile(t, fixture.evidenceDirLog))
	t.Cleanup(func() { _ = os.RemoveAll(evidenceRoot) })
	for _, path := range []string{filepath.Join(fixture.dir, ".amqrc"), filepath.Join(evidenceRoot, "part-b-amq")} {
		if _, statErr := os.Lstat(path); statErr != nil {
			t.Fatalf("AMQ artifact was removed without observed fleet absence at %s: %v", path, statErr)
		}
	}
	if !strings.Contains(output, "PART B AMQ CLEANUP FAIL (temporary config/root retained because fleet absence was not observed)") {
		t.Fatalf("output did not report retained AMQ artifacts after uncertain status:\n%s", output)
	}
}

func TestLiveVerificationUnexpectedExitRetainsAMQArtifactsWithoutObservedAbsence(t *testing.T) {
	fixture := newLiveFixture(t)
	bashEnvironment := filepath.Join(t.TempDir(), "bash-environment")
	writeTestFile(t, bashEnvironment, []byte(`if [ -z "${AGENTCTL_TEST_VERIFIER_BASHPID:-}" ]; then
  export AGENTCTL_TEST_VERIFIER_BASHPID=$BASHPID
fi
trap 'if [ "$BASHPID" = "$AGENTCTL_TEST_VERIFIER_BASHPID" ] && [ "${PART_B_SESSION_OWNED:-0}" = 1 ]; then trap - DEBUG; echo "simulated unexpected Part B exit" >&2; exit 23; fi' DEBUG
`), 0o644)
	output, err := fixture.run(t, "",
		"BASH_ENV="+bashEnvironment,
		"AGENTCTL_TEST_STATUS_AFTER_KILL_CODE=6",
		"AGENTCTL_TEST_STATUS_AFTER_KILL_MESSAGE=agentctl: transport failure",
	)
	if err == nil {
		t.Fatalf("unexpected Part B exit returned success without observed fleet absence:\n%s", output)
	}
	evidenceRoot := strings.TrimSpace(readTestFile(t, fixture.evidenceDirLog))
	t.Cleanup(func() { _ = os.RemoveAll(evidenceRoot) })
	for _, path := range []string{filepath.Join(fixture.dir, ".amqrc"), filepath.Join(evidenceRoot, "part-b-amq")} {
		if _, statErr := os.Lstat(path); statErr != nil {
			t.Fatalf("exit trap removed AMQ artifact without observed fleet absence at %s: %v", path, statErr)
		}
	}
	for _, want := range []string{
		"simulated unexpected Part B exit",
		"PART B CLEANUP PASS",
		"PART B ABSENCE OBSERVATION FAIL",
		"PART B AMQ CLEANUP FAIL (temporary config/root retained because fleet absence was not observed)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestLiveVerificationRejectsTmuxNoServerAsDetachedTeardownEvidence(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 15), "AGENTCTL_TEST_STATUS_AFTER_KILL_CODE=6", "AGENTCTL_TEST_STATUS_AFTER_KILL_MESSAGE=agentctl: tmux list sessions: exit status 1: no server running")
	if err == nil || !strings.Contains(output, "TEARDOWN FAIL (agentctl status exited 6 unexpectedly)") {
		t.Fatalf("tmux no-server result was accepted as detached absence: err=%v\n%s", err, output)
	}
}

func TestLiveVerificationAcceptsMissingDurableFleetAsAbsent(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 15), "AGENTCTL_TEST_INITIAL_STATUS_MESSAGE=agentctl: session \"relverify\" has no durable fleet configuration")
	if err != nil {
		t.Fatalf("missing durable fleet was not accepted as absence: %v\n%s", err, output)
	}
	if !strings.Contains(output, "[PASS B.1] release-candidate fleet launched") {
		t.Fatalf("Part B did not launch after observed fleet absence:\n%s", output)
	}
}

func TestLiveVerificationPromptsRejectInvalidInput(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, "\nmaybe\n"+strings.Repeat("y\n", 15))
	if err != nil {
		t.Fatalf("release verification failed: %v\n%s", err, output)
	}
	for _, want := range []string{"unrecognised: '' — answer y or n", "unrecognised: 'maybe' — answer y or n"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}
