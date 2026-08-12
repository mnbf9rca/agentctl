package hack_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	dir                    string
	agentctlLog            string
	agentctlEnvironmentLog string
	authObservationLog     string
	claudeConfigLog        string
	securityLog            string
	tmuxLog                string
	amqLog                 string
	skillRootLog           string
	environmentLog         string
	evidenceDirLog         string
	partCRootLog           string
	sighupProbeLog         string
	operatorHome           string
	keychainTarget         string
	keychainSentinel       string
}

const (
	fakeClaudeAuthBody        = "fixture-claude-auth-material-do-not-print"
	fakeClaudeCredentialsBody = "fixture-claude-credentials-material-do-not-print"
	fakeCodexAuthBody         = "fixture-codex-auth-material-do-not-print"
)

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
	agentctlEnvironmentLog := filepath.Join(t.TempDir(), "agentctl-environment.log")
	authObservationLog := filepath.Join(t.TempDir(), "auth-observation.log")
	claudeConfigLog := filepath.Join(t.TempDir(), "claude-config.log")
	securityLog := filepath.Join(t.TempDir(), "security.log")
	tmuxLog := filepath.Join(t.TempDir(), "tmux.log")
	amqLog := filepath.Join(t.TempDir(), "amq.log")
	skillRootLog := filepath.Join(t.TempDir(), "skill-root.log")
	environmentLog := filepath.Join(t.TempDir(), "environment.log")
	evidenceDirLog := filepath.Join(t.TempDir(), "evidence-dir.log")
	partCRootLog := filepath.Join(t.TempDir(), "part-c-root.log")
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
	keychainLibrary := filepath.Join(operatorHome, "Library")
	keychainTarget := filepath.Join(keychainLibrary, "Keychains")
	for _, path := range []string{filepath.Join(operatorHome, ".claude"), filepath.Join(operatorHome, ".codex"), keychainLibrary, keychainTarget} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{operatorHome, filepath.Join(operatorHome, ".claude"), filepath.Join(operatorHome, ".codex"), filepath.Join(operatorHome, "Library"), keychainTarget} {
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
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
	keychainSentinel := filepath.Join(keychainTarget, "operator-login-keychain-sentinel")
	writeTestFile(t, keychainSentinel, []byte("real-keychain-target-must-survive"), 0o600)
	writeTestFile(t, filepath.Join(operatorHome, ".claude.json"), []byte(fakeClaudeAuthBody), 0o600)
	writeTestFile(t, filepath.Join(operatorHome, ".claude", ".credentials.json"), []byte(fakeClaudeCredentialsBody), 0o600)
	writeTestFile(t, filepath.Join(operatorHome, ".codex", "auth.json"), []byte(fakeCodexAuthBody), 0o600)
	agentctl := `#!/usr/bin/env bash
set -u
printf '%s\n' "$*" >>"$AGENTCTL_TEST_LOG"
case "$*" in
  skill\ install|launch\ --session\ skillverify\ *|attach\ --session\ skillverify|kill\ --session\ skillverify)
    printf '%s\t%s\t%s\t%s\n' "$*" "$PWD" "$HOME" "$PATH" >>"$AGENTCTL_TEST_AGENTCTL_ENVIRONMENT_LOG"
    ;;
esac
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
      echo 'SESSION ROLE HARNESS MODEL EFFORT CONFIDENCE SHIM CHILD PRESENTATION STATE FACTS'
      if [ -e "$AGENTCTL_TEST_ROLE_A" ]; then
        if [ -e "$AGENTCTL_TEST_RELAUNCHED" ]; then
          echo 'relverify a claude default default anchored 301 302 present running -'
        elif kill -0 "$AGENTCTL_TEST_SHIM_PID" 2>/dev/null; then
          echo "relverify a claude default default anchored $AGENTCTL_TEST_SHIM_PID $AGENTCTL_TEST_CHILD_PID present running -"
        else
          echo "relverify a claude default default unanchored $AGENTCTL_TEST_SHIM_PID $AGENTCTL_TEST_CHILD_PID absent stale-record ESRCH"
        fi
      fi
      if [ -e "$AGENTCTL_TEST_ROLE_B" ]; then
        echo 'relverify b codex default high anchored 205 206 present running -'
      fi
      exit 0
    fi
    if [ -e "$AGENTCTL_TEST_KILLED" ] && [ -n "${AGENTCTL_TEST_STATUS_AFTER_KILL_CODE:-}" ]; then
      echo "${AGENTCTL_TEST_STATUS_AFTER_KILL_MESSAGE:-agentctl: transport failure}" >&2
      exit "$AGENTCTL_TEST_STATUS_AFTER_KILL_CODE"
    fi
    echo "${AGENTCTL_TEST_INITIAL_STATUS_MESSAGE:-agentctl: session \"relverify\" not found}" >&2
    exit 3
    ;;
  skill)
    [ "$2" = install ] || exit 64
    mkdir -p "$HOME/.claude/skills/agentctl" "$HOME/.agents/skills/agentctl"
    printf '%s\n' "$HOME" >>"$AGENTCTL_TEST_SKILL_ROOT_LOG"
    ;;
  launch)
    if [ "$3" = skillverify ]; then
      if [ "${AGENTCTL_TEST_REQUIRE_PART_C_APP_SUPPORT:-0}" = 1 ] && [ ! -d "$HOME/Library/Application Support" ]; then
        echo 'missing Part C Library/Application Support' >&2
        exit 2
      fi
      : >"$AGENTCTL_TEST_AUTH_OBSERVATION_LOG"
      if [ -f "$HOME/.claude.json" ]; then
        /bin/cp "$HOME/.claude.json" "$AGENTCTL_TEST_CLAUDE_CONFIG_LOG"
      fi
      printf 'dir .|%s\n' "$(/usr/bin/stat -f '%Lp' "$HOME")" >>"$AGENTCTL_TEST_AUTH_OBSERVATION_LOG"
	  for auth_path in "$HOME/.claude.json" "$HOME/.claude/.credentials.json" "$HOME/.codex/auth.json"; do
	    if [ -f "$auth_path" ]; then
	      relative_path=${auth_path#"$HOME"/}
	      parent_path=${auth_path%/*}
	      if [ "$parent_path" != "$HOME" ]; then
	        printf 'dir %s|%s\n' "${parent_path#"$HOME"/}" "$(/usr/bin/stat -f '%Lp' "$parent_path")" >>"$AGENTCTL_TEST_AUTH_OBSERVATION_LOG"
	      fi
	      printf 'file %s|%s\n' "$relative_path" "$(/usr/bin/stat -f '%Lp' "$auth_path")" >>"$AGENTCTL_TEST_AUTH_OBSERVATION_LOG"
	    fi
	  done
	  if [ -L "$HOME/Library/Keychains" ]; then
	    printf 'link Library/Keychains|%s\n' "$(/usr/bin/readlink "$HOME/Library/Keychains")" >>"$AGENTCTL_TEST_AUTH_OBSERVATION_LOG"
	  elif [ -d "$HOME/Library/Keychains" ]; then
	    printf 'dir Library/Keychains|%s\n' "$(/usr/bin/stat -f '%Lp' "$HOME/Library/Keychains")" >>"$AGENTCTL_TEST_AUTH_OBSERVATION_LOG"
	    if [ -f "$HOME/Library/Keychains/login.keychain-db" ]; then
	      printf 'file Library/Keychains/login.keychain-db|%s\n' "$(/usr/bin/stat -f '%Lp' "$HOME/Library/Keychains/login.keychain-db")" >>"$AGENTCTL_TEST_AUTH_OBSERVATION_LOG"
	    fi
	  fi
      if [ "${AGENTCTL_TEST_PART_C_LAUNCH_FAIL:-0}" = 1 ]; then
        echo 'skillverify launch failed' >&2
        exit 1
      fi
      touch "$AGENTCTL_TEST_SKILL_OWNED"
      echo 'launched skillverify'
      exit 0
    fi
    if [ "${AGENTCTL_TEST_COLLISION:-0}" = 1 ]; then
      echo 'agentctl: session "relverify" already exists' >&2
      exit 3
    fi
    touch "$AGENTCTL_TEST_OWNED"
    touch "$AGENTCTL_TEST_ROLE_A"
    touch "$AGENTCTL_TEST_ROLE_B"
    echo 'launched relverify'
    ;;
  clear|compact)
    echo "delivered $1"
    ;;
  relaunch)
    touch "$AGENTCTL_TEST_ROLE_A" "$AGENTCTL_TEST_RELAUNCHED"
    echo 'agentctl: relaunched role "a" in session "relverify"; the shim is ready' >&2
    ;;
  attach)
    if [ "$3" = skillverify ] && [ "${AGENTCTL_TEST_REQUIRE_PART_C_ITERM:-0}" = 1 ] && [ "${TERM_PROGRAM:-}" != iTerm.app ]; then
      echo "Part C attach TERM_PROGRAM=${TERM_PROGRAM:-}" >&2
      exit 2
    fi
    if [ "$3" = skillverify ] && [ "${AGENTCTL_TEST_PART_C_ATTACH_FAIL:-0}" = 1 ]; then
      echo 'attach failed' >&2
      exit 1
    fi
    ;;
  kill)
    if [ "$3" = skillverify ]; then
      code=0
      if [ -n "${AGENTCTL_TEST_PART_C_KILL_CODES:-}" ]; then
        calls=0
        [ ! -e "$AGENTCTL_TEST_PART_C_KILL_CALLS" ] || calls=$(cat "$AGENTCTL_TEST_PART_C_KILL_CALLS")
        calls=$((calls + 1))
        printf '%s\n' "$calls" >"$AGENTCTL_TEST_PART_C_KILL_CALLS"
        IFS=, read -r -a codes <<<"$AGENTCTL_TEST_PART_C_KILL_CODES"
        index=$((calls - 1))
        [ "$index" -lt "${#codes[@]}" ] || index=$((${#codes[@]} - 1))
        code=${codes[$index]}
      fi
      if [ "$code" -ne 0 ]; then
        echo 'simulated skillverify kill failure' >&2
        exit "$code"
      fi
      rm -f "$AGENTCTL_TEST_SKILL_OWNED"
      touch "$AGENTCTL_TEST_SKILL_KILLED"
    else
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
case "$1" in
  -L)
    [ "$3" = kill-server ] || exit 64
    touch "$AGENTCTL_TEST_SKILL_SOCKET_KILLED"
    if [ "${AGENTCTL_TEST_SKILL_KILL_SERVER_ENOENT_WARNING:-0}" = 1 ]; then
      echo 'warning: unexpected tmux diagnostic' >&2
      echo "error connecting to /private/tmp/tmux-501/$2 (No such file or directory)" >&2
      exit 1
    fi
    if [ "${AGENTCTL_TEST_SKILL_KILL_SERVER_ENOENT_WRONG_SOCKET:-0}" = 1 ]; then
      echo "error connecting to /private/tmp/tmux-501/not-$2 (No such file or directory)" >&2
      exit 1
    fi
    if [ "${AGENTCTL_TEST_SKILL_KILL_SERVER_ENOENT:-0}" = 1 ]; then
      echo "error connecting to /private/tmp/tmux-501/$2 (No such file or directory)" >&2
      exit 1
    fi
    if [ "${AGENTCTL_TEST_SKILL_KILL_SERVER_ABSENT:-0}" = 1 ]; then
      echo 'no server running'
      exit 1
    fi
    code=${AGENTCTL_TEST_SKILL_KILL_SERVER_CODE:-0}
    if [ -n "${AGENTCTL_TEST_SKILL_KILL_SERVER_CODES:-}" ]; then
      calls=0
      [ ! -e "$AGENTCTL_TEST_SKILL_KILL_SERVER_CALLS" ] || calls=$(cat "$AGENTCTL_TEST_SKILL_KILL_SERVER_CALLS")
      calls=$((calls + 1))
      printf '%s\n' "$calls" >"$AGENTCTL_TEST_SKILL_KILL_SERVER_CALLS"
      IFS=, read -r -a codes <<<"$AGENTCTL_TEST_SKILL_KILL_SERVER_CODES"
      index=$((calls - 1))
      [ "$index" -lt "${#codes[@]}" ] || index=$((${#codes[@]} - 1))
      code=${codes[$index]}
    fi
    [ "$code" -eq 0 ] || exit "$code"
    ;;
  -V)
    echo 'tmux 3.7b'
    ;;
  list-sessions)
    [ -e "$AGENTCTL_TEST_OWNED" ] && printf '$4\trelverify\n'
    ;;
  new-session)
    [ "$2" = -d ] && [ "$3" = -s ] && [ "$5" = -n ] && [ "$6" = keeper ] && [ "$7" = -- ] && [ "$8" = 'exec sleep 86400' ] || exit 65
    case "$4" in
      agentctl-release-verify-keeper-[0-9]*) ;;
      *) exit 65 ;;
    esac
    if [ "${AGENTCTL_TEST_KEEPER_CREATE_CODE:-0}" -ne 0 ]; then
      echo 'simulated keeper creation failure' >&2
      exit "$AGENTCTL_TEST_KEEPER_CREATE_CODE"
    fi
    touch "$AGENTCTL_TEST_KEEPER_OWNED"
    ;;
  kill-session)
    [ "$2" = -t ] || exit 65
    case "$3" in
      =agentctl-release-verify-keeper-[0-9]*) ;;
      *) exit 65 ;;
    esac
    [ -e "$AGENTCTL_TEST_KEEPER_OWNED" ] || exit 66
    rm -f "$AGENTCTL_TEST_KEEPER_OWNED"
    touch "$AGENTCTL_TEST_KEEPER_KILLED"
    ;;
  list-windows)
    if [ -e "$AGENTCTL_TEST_ROLE_A" ]; then
      if [ -e "$AGENTCTL_TEST_RELAUNCHED" ]; then
        printf '@11\t%s\ta\n' "${AGENTCTL_TEST_RELAUNCHED_PANE_ID:-%12}"
      else
        printf '@7\t%%5\ta\n'
      fi
    fi
    if [ -e "$AGENTCTL_TEST_ROLE_B" ]; then
      printf '@8\t%%9\tb\n'
    fi
    ;;
  *)
    exit 64
    ;;
esac
`
	writeTestFile(t, filepath.Join(dir, "stubs/tmux"), []byte(tmux), 0o755)
	writeTestFile(t, filepath.Join(dir, "stubs/claude"), []byte("#!/usr/bin/env bash\necho '2.1.220 (Claude Code)'\n"), 0o755)
	writeTestFile(t, filepath.Join(dir, "stubs/codex"), []byte("#!/usr/bin/env bash\necho 'codex-cli 0.146.0'\n"), 0o755)
	install := `#!/usr/bin/env bash
set -u
if [ "${AGENTCTL_TEST_AUTH_INSTALL_FAIL:-0}" = 1 ]; then
  case "$*" in
    *codex/auth.json*) printf 'simulated install failure: %s\n' "$*" >&2; exit 1 ;;
  esac
fi
exec /usr/bin/install "$@"
`
	writeTestFile(t, filepath.Join(dir, "stubs/install"), []byte(install), 0o755)
	security := `#!/usr/bin/env bash
set -u
printf '%s\t%s\t%s\t%s\t%s\n' "$HOME" "$#" "${1:-}" "${2:-}" "${4:-}" >>"$AGENTCTL_TEST_SECURITY_LOG"
[ "$#" -eq 4 ] || exit 64
[ "$1" = create-keychain ] && [ "$2" = -p ] && [ -z "$3" ] || exit 64
case "$4" in
  "$HOME"/Library/Keychains/login.keychain-db) ;;
  *) exit 64 ;;
esac
if [ "${AGENTCTL_TEST_SECURITY_CREATE_FAIL:-0}" = 1 ]; then
  echo 'simulated security create-keychain failure' >&2
  exit 19
fi
: >"$4"
chmod 0600 "$4"
`
	writeTestFile(t, filepath.Join(dir, "stubs/security"), []byte(security), 0o755)
	ln := `#!/usr/bin/env bash
set -u
if [ "${AGENTCTL_TEST_KEYCHAIN_LINK_FAIL:-0}" = 1 ]; then
  case "${3:-}" in
    */Library/Keychains) echo 'simulated Keychains link failure' >&2; exit 20 ;;
  esac
fi
exec /bin/ln "$@"
`
	writeTestFile(t, filepath.Join(dir, "stubs/ln"), []byte(ln), 0o755)
	rm := `#!/usr/bin/env bash
set -u
printf '%s|%s|%s\n' "$PWD" "$HOME" "$PATH" >>"$AGENTCTL_TEST_ENVIRONMENT_LOG"
if [ "${AGENTCTL_TEST_PART_C_RM_FAIL:-0}" = 1 ] && [ "$1" = -rf ] && [ "$2" = -- ] && [ "$3" = "$(/bin/cat "$AGENTCTL_TEST_PART_C_ROOT_LOG")" ]; then
  echo 'simulated Part C rm failure' >&2
  exit 1
fi
if [ "${AGENTCTL_TEST_PART_C_HOME_RM_FAIL:-0}" = 1 ] && [ "$1" = -rf ] && [ "$2" = -- ] && [ "$3" = "$(/bin/cat "$AGENTCTL_TEST_PART_C_ROOT_LOG")/home" ]; then
  echo 'simulated Part C HOME rm failure' >&2
  exit 1
fi
exec /bin/rm "$@"
`
	writeTestFile(t, filepath.Join(dir, "stubs/rm"), []byte(rm), 0o755)
	amq := `#!/usr/bin/env bash
set -u
printf '%s|%s|%s|%s\n' "$*" "$PWD" "$HOME" "$PATH" >>"$AGENTCTL_TEST_AMQ_LOG"
[ "$1" = coop ] && [ "$2" = init ] && [ "$3" = --agents ] && [ "$4" = a,b,user ] || exit 64
`
	writeTestFile(t, filepath.Join(dir, "stubs/amq"), []byte(amq), 0o755)
	mktemp := `#!/usr/bin/env bash
set -u
created=$(/usr/bin/mktemp "$@") || exit $?
case "$*" in
  "-d /tmp/agentctl-release-verify.XXXXXX") printf '%s\n' "$created" >"$AGENTCTL_TEST_EVIDENCE_DIR_LOG" ;;
  "-d /tmp/agentctl-skill-verify.XXXXXX") printf '%s\n' "$created" >"$AGENTCTL_TEST_PART_C_ROOT_LOG" ;;
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
	t.Setenv("AGENTCTL_TEST_AGENTCTL_ENVIRONMENT_LOG", agentctlEnvironmentLog)
	t.Setenv("AGENTCTL_TEST_AUTH_OBSERVATION_LOG", authObservationLog)
	t.Setenv("AGENTCTL_TEST_CLAUDE_CONFIG_LOG", claudeConfigLog)
	t.Setenv("AGENTCTL_TEST_SECURITY_LOG", securityLog)
	t.Setenv("AGENTCTL_TEST_TMUX_LOG", tmuxLog)
	t.Setenv("AGENTCTL_TEST_AMQ_LOG", amqLog)
	t.Setenv("AGENTCTL_TEST_SKILL_ROOT_LOG", skillRootLog)
	t.Setenv("AGENTCTL_TEST_ENVIRONMENT_LOG", environmentLog)
	t.Setenv("AGENTCTL_TEST_EVIDENCE_DIR_LOG", evidenceDirLog)
	t.Setenv("AGENTCTL_TEST_PART_C_ROOT_LOG", partCRootLog)
	t.Setenv("AGENTCTL_TEST_SIGHUP_PROBE_LOG", sighupProbeLog)
	t.Setenv("AGENTCTL_TEST_OWNED", agentctlOwned)
	t.Setenv("AGENTCTL_TEST_KILLED", agentctlKilled)
	t.Setenv("AGENTCTL_TEST_ROLE_A", agentctlRoleA)
	t.Setenv("AGENTCTL_TEST_ROLE_B", agentctlRoleB)
	t.Setenv("AGENTCTL_TEST_RELAUNCHED", agentctlRelaunched)
	t.Setenv("AGENTCTL_TEST_KEEPER_OWNED", keeperOwned)
	t.Setenv("AGENTCTL_TEST_KEEPER_KILLED", keeperKilled)
	t.Setenv("AGENTCTL_TEST_SKILL_OWNED", filepath.Join(t.TempDir(), "skill-owned"))
	t.Setenv("AGENTCTL_TEST_SKILL_KILLED", filepath.Join(t.TempDir(), "skill-killed"))
	t.Setenv("AGENTCTL_TEST_PART_C_KILL_CALLS", filepath.Join(t.TempDir(), "skill-kill-calls"))
	t.Setenv("AGENTCTL_TEST_PART_B_KILL_CALLS", filepath.Join(t.TempDir(), "relverify-kill-calls"))
	t.Setenv("AGENTCTL_TEST_SKILL_SOCKET_KILLED", filepath.Join(t.TempDir(), "skill-socket-killed"))
	t.Setenv("AGENTCTL_TEST_SKILL_KILL_SERVER_CALLS", filepath.Join(t.TempDir(), "skill-kill-server-calls"))
	t.Setenv("AGENTCTL_TEST_PGREP_CALLS", pgrepCalls)
	t.Setenv("HOME", operatorHome)
	t.Setenv("PATH", filepath.Join(dir, "stubs")+string(os.PathListSeparator)+os.Getenv("PATH"))
	return liveFixture{
		dir:                    dir,
		agentctlLog:            agentctlLog,
		agentctlEnvironmentLog: agentctlEnvironmentLog,
		authObservationLog:     authObservationLog,
		claudeConfigLog:        claudeConfigLog,
		securityLog:            securityLog,
		tmuxLog:                tmuxLog,
		amqLog:                 amqLog,
		skillRootLog:           skillRootLog,
		environmentLog:         environmentLog,
		evidenceDirLog:         evidenceDirLog,
		partCRootLog:           partCRootLog,
		sighupProbeLog:         sighupProbeLog,
		operatorHome:           operatorHome,
		keychainTarget:         keychainTarget,
		keychainSentinel:       keychainSentinel,
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
	command.Dir = fixture.dir
	command.Stdin = strings.NewReader(input)
	command.Env = append(os.Environ(), "AGENTCTL_TEST_SHIM_PID="+strconv.Itoa(shimProcess.Process.Pid))
	command.Env = append(command.Env, "AGENTCTL_TEST_CHILD_PID="+childPID)
	command.Env = append(command.Env, environment...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func (fixture liveFixture) calls(t *testing.T) []string {
	t.Helper()
	body, err := os.ReadFile(fixture.agentctlLog)
	if err != nil {
		t.Fatal(err)
	}
	return strings.FieldsFunc(strings.TrimSpace(string(body)), func(character rune) bool { return character == '\n' })
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
	output, err := fixture.run(t, strings.Repeat("y\n", 15))
	if err != nil {
		t.Fatalf("release verification failed: %v\n%s", err, output)
	}
	for _, want := range []string{
		"=== Part A — Automated release checks ===",
		"[PASS A.",
		"=== Part B — Live release-candidate delivery ===",
		"The Command Menu belongs to iTerm2.",
		"advisory read failure; omission is not a release failure",
		"Expected output:",
		"operator confirmed:",
		"=== Part C — Live skill discovery and meaning ===",
		"harness lists the agentctl skill",
		"probe answer matches references/status-states.md",
		"no window is selected as real",
		"press esc to detach cleanly; do not use uppercase X",
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
		"- Part C:",
		"- Checkpoint B.C1 attach narration: operator confirmed: y",
		"- Checkpoint B.C10 detach: operator confirmed: y",
		"- Checkpoint C.C1 authentication (keychain-linked, codex-seeded): operator confirmed: y",
		"- Checkpoint C.C2 skill inventory: operator confirmed: y",
		"- Checkpoint C.C3 status meaning: operator confirmed: y",
	} {
		if !strings.Contains(string(notes), want) {
			t.Fatalf("notes missing %q:\n%s", want, notes)
		}
	}
	wantCalls := []string{
		"version",
		"status --session relverify",
		"launch --session relverify --roles a:claude,b:codex --efforts b:high",
		"clear --session relverify a",
		"clear --session relverify b",
		"compact --session relverify a",
		"status --session relverify",
		"relaunch --session relverify a",
		"status --session relverify",
		"kill --session relverify",
		"status --session relverify",
		"skill install",
		"launch --session skillverify --roles a:claude,b:codex --dir " + filepath.Join(strings.TrimSpace(readTestFile(t, fixture.skillRootLog)), "..", "project"),
		"attach --session skillverify",
		"kill --session skillverify",
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

			output, err := fixture.run(t, strings.Repeat("y\n", 15))
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
	output, err := fixture.run(t, "",
		"AGENTCTL_TEST_UNEXPECTED_EXIT_AFTER_PART_B_LAUNCH=1",
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
		"AGENTCTL_TEST_PART_B_KILL_CODES=9,0",
	)
	if err != nil {
		t.Fatalf("release verification did not recover from transient Part B child observation: %v\n%s", err, output)
	}
	for _, want := range []string{
		"PART B CLEANUP OBSERVED (relverify kill exited 9; retrying once)",
		"PART B CLEANUP PASS (relverify kill retry exited 0)",
		"ALL VERIFIED — evidence appended",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	if got := strings.TrimSpace(readTestFile(t, os.Getenv("AGENTCTL_TEST_PART_B_KILL_CALLS"))); got != "2" {
		t.Fatalf("Part B kill call count = %q, want 2", got)
	}
}

func TestLiveVerificationCreatesPartCUserConfigRoot(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 15),
		"AGENTCTL_TEST_REQUIRE_PART_C_APP_SUPPORT=1",
	)
	if err != nil {
		t.Fatalf("release verification omitted the isolated user-config root: %v\n%s", err, output)
	}
	if !strings.Contains(output, "ALL VERIFIED — evidence appended") {
		t.Fatalf("release verification did not complete after creating user-config root:\n%s", output)
	}
}

func TestLiveVerificationPartCAttachDeclaresITermEnvironment(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 15),
		"AGENTCTL_TEST_REQUIRE_PART_C_ITERM=1",
		"TERM_PROGRAM=tmux",
	)
	if err != nil {
		t.Fatalf("release verification omitted Part C iTerm attach environment: %v\n%s", err, output)
	}
	if !strings.Contains(output, "ALL VERIFIED — evidence appended") {
		t.Fatalf("release verification did not complete with pinned Part C iTerm environment:\n%s", output)
	}
}

func TestLiveVerificationRetriesPartCKillAfterSocketRemoval(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 12)+"n\n",
		"AGENTCTL_TEST_PART_C_KILL_CODES=9,0",
	)
	if err == nil {
		t.Fatalf("release verification accepted refused Part C checkpoint:\n%s", output)
	}
	for _, want := range []string{
		"PART C CLEANUP FAIL (skillverify kill)",
		"PART C CLEANUP PASS (named tmux socket killed)",
		"PART C CLEANUP PASS (skillverify kill retry after socket removal)",
		"PART C CLEANUP PASS (temporary root removed)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	if got := strings.TrimSpace(readTestFile(t, os.Getenv("AGENTCTL_TEST_PART_C_KILL_CALLS"))); got != "2" {
		t.Fatalf("Part C kill call count = %q, want 2", got)
	}
}

func TestLiveVerificationRejectedCheckpointArtifactCannotClaimPartBPass(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 9)+"n\n")
	if err == nil {
		t.Fatalf("release verification passed after B.C10 rejection:\n%s", output)
	}
	evidenceDir := strings.TrimSpace(readTestFile(t, fixture.evidenceDirLog))
	t.Cleanup(func() { _ = os.RemoveAll(evidenceDir) })
	metadataPath := filepath.Join(evidenceDir, "verify-live", "metadata.txt")
	metadata := readTestFile(t, metadataPath)
	wantResult := "part_b_result=FAIL — operator refused checkpoint B.C10"
	if !strings.Contains(metadata, wantResult) || strings.Contains(metadata, "part_b_result=PASS") {
		t.Fatalf("rejected Part B metadata is not fail-closed.\n--- metadata ---\n%s", metadata)
	}

	command := exec.Command("bash", "hack/release-verify.sh", "--render-results", filepath.Join(evidenceDir, "versions.txt"), filepath.Join(evidenceDir, "verify-live"))
	command.Dir = fixture.dir
	rendered, renderErr := command.CombinedOutput()
	if renderErr != nil {
		t.Fatalf("render rejected Part B artifact: %v\n%s", renderErr, rendered)
	}
	if !strings.Contains(string(rendered), "- Part B: FAIL — operator refused checkpoint B.C10") || strings.Contains(string(rendered), "- Part B: PASS") {
		t.Fatalf("renderer fabricated Part B success after B.C10 rejection:\n%s", rendered)
	}
}

func TestLiveVerificationZeroStatusExitBecomesFailureWhenCleanupFails(t *testing.T) {
	fixture := newLiveFixture(t)
	bashEnvironment := filepath.Join(t.TempDir(), "bash-environment")
	writeTestFile(t, bashEnvironment, []byte(`cat() {
  if [ "${AGENTCTL_TEST_EXIT_ZERO_AFTER_PART_B_LAUNCH:-0}" = 1 ] && [ -e "$AGENTCTL_TEST_OWNED" ]; then
    echo 'simulated zero-status exit after Part B launch' >&2
    set +e
    exit 0
  fi
  command /bin/cat "$@"
}
`), 0o644)
	output, err := fixture.run(t, "",
		"BASH_ENV="+bashEnvironment,
		"AGENTCTL_TEST_EXIT_ZERO_AFTER_PART_B_LAUNCH=1",
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

func TestLiveVerificationNumbersCheckpointsAndGuidesUnfamiliarOperator(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 15))
	if err != nil {
		t.Fatalf("release verification failed: %v\n%s", err, output)
	}
	for _, checkpointID := range []string{
		"B.C1", "B.C2", "B.C3", "B.C4", "B.C5", "B.C6", "B.C7", "B.C8", "B.C9", "B.C10",
		"C.C1", "C.C2", "C.C3",
	} {
		for _, prefix := range []string{"[CHECKPOINT " + checkpointID + "]", "[CHECKPOINT PASS " + checkpointID + "]"} {
			if !strings.Contains(output, prefix) {
				t.Fatalf("output missing numbered checkpoint result %q:\n%s", prefix, output)
			}
		}
	}
	for _, want := range []string{
		"leave this verifier running in Window 1",
		"press Command-N to open a second iTerm2 window",
		"Keep the Window 2 attachment open",
		"return to Window 1 to answer each numbered checkpoint",
		"Return to Window 2, press esc to detach cleanly; do not use uppercase X",
		"The verifier will attach this Window 1 to the isolated skill fleet now.",
		"While attached, use these concrete actions:",
		"In the Claude Code tab, type /skills",
		"In the codex tab, type /skills",
		"find agentctl in the displayed skill inventory",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing operator guidance %q:\n%s", want, output)
		}
	}
	for _, want := range []string{
		"[ACTION PASS B.3] claude clear delivery command completed; observed outcome pending checkpoint B.C3",
		"[ACTION PASS B.4] codex clear delivery command completed; observed outcome pending checkpoint B.C5",
		"[ACTION PASS B.5] claude compact delivery command completed; observed outcome pending checkpoint B.C7",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing action/checkpoint distinction %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "[PASS B.3] claude clear delivery command completed") {
		t.Fatalf("delivery action was presented as a checkpoint outcome:\n%s", output)
	}
	for _, impossible := range []string{"Before attaching, inspect both skill inventories", "Attach to the isolated skill fleet with:"} {
		if strings.Contains(output, impossible) {
			t.Fatalf("output gives impossible or operator-owned Part C attach guidance %q:\n%s", impossible, output)
		}
	}
}

func TestLiveVerificationCreatesClaudeContextBeforeCompactCheckpoint(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 15))
	if err != nil {
		t.Fatalf("release verification failed: %v\n%s", err, output)
	}
	seed := "Before the compact spot check, create compactable context in the claude tab:"
	firstPrompt := "Reply with FIRST READY and one sentence about this repository."
	firstWait := "Wait for Claude's complete response, then submit this second message:"
	secondPrompt := "Reply with SECOND READY and one different sentence about testing this repository."
	secondWait := "Wait for Claude's second complete response. Then type junk into the input box; do NOT press Enter."
	checkpoint := "[CHECKPOINT B.C6] claude compact setup"
	positions := []int{
		strings.Index(output, seed),
		strings.Index(output, firstPrompt),
		strings.Index(output, firstWait),
		strings.Index(output, secondPrompt),
		strings.Index(output, secondWait),
		strings.Index(output, checkpoint),
	}
	for index, position := range positions {
		if position < 0 {
			t.Fatalf("output missing compact-context instruction %d:\n%s", index, output)
		}
		if index > 0 && position <= positions[index-1] {
			t.Fatalf("compact-context instructions are out of order: positions=%v\n%s", positions, output)
		}
	}
	if !strings.Contains(output, "Claude's two responses are complete, and junk is visible in the claude input without being submitted.") {
		t.Fatalf("B.C6 does not require both compactable context and unsent junk:\n%s", output)
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

func TestLiveVerificationPartCConsentSeedsCodexAndLinksExactClaudeKeychains(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 15))
	if err != nil {
		t.Fatalf("release verification failed: %v\n%s", err, output)
	}
	partCRoot := strings.TrimSpace(readTestFile(t, fixture.partCRootLog))
	wantDestination := filepath.Join(partCRoot, "home", "Library", "Keychains")
	wantClaudeConfig := filepath.Join(partCRoot, "home", ".claude.json")
	wantGuidance := []string{
		"Part C can seed codex authentication from this empirically proven file:",
		"  ~/.codex/auth.json",
		"Copy only this Codex file into the temporary Part C HOME?",
		"Claude Code 2.1.226 can authenticate through this exact symlink:",
		fixture.keychainTarget + " -> " + wantDestination,
		"Both probe harnesses can reach the operator's login keychain through this link; per-item ACLs still apply.",
		"Part C will synthesize this minimal Claude onboarding configuration:",
		wantClaudeConfig,
		"It contains onboarding state only, not credentials, and does not copy the operator's Claude configuration.",
		"Create exactly this Claude Keychains symlink and synthesized onboarding configuration: " + fixture.keychainTarget + " -> " + wantDestination + "?",
		"The proven Codex auth.json, Claude Keychains symlink, and synthesized onboarding",
		"configuration were created with your consent.",
		"Did both harnesses start without requiring re-authentication?",
	}
	previousIndex := -1
	for _, want := range wantGuidance {
		index := strings.Index(output, want)
		if index <= previousIndex {
			t.Fatalf("auth consent guidance missing or out of order at %q:\n%s", want, output)
		}
		previousIndex = index
	}
	for _, forbidden := range []string{
		"Part C can seed Claude authentication from",
		"Copy only this Claude file",
		filepath.Join(fixture.operatorHome, ".claude.json"),
		"~/.claude/.credentials.json",
		"CLAUDE_CODE_OAUTH_TOKEN",
	} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("auth consent offered .claude.json as an authentication mechanism via %q:\n%s", forbidden, output)
		}
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, ".claude.json") && line != "  "+wantClaudeConfig {
			t.Fatalf("auth transcript mentioned .claude.json outside the exact synthesized-config destination: %q", line)
		}
	}
	for _, forbidden := range []string{fakeClaudeAuthBody, fakeClaudeCredentialsBody, fakeCodexAuthBody} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("release verifier printed fake credential contents %q:\n%s", forbidden, output)
		}
	}
	wantObservation := strings.Join([]string{
		"dir .|700",
		"file .claude.json|600",
		"dir .codex|700",
		"file .codex/auth.json|600",
		"link Library/Keychains|" + fixture.keychainTarget,
	}, "\n") + "\n"
	if got := readTestFile(t, fixture.authObservationLog); got != wantObservation {
		t.Fatalf("auth files observed at skillverify launch:\n%s\nwant:\n%s", got, wantObservation)
	}
	if got, want := readTestFile(t, fixture.claudeConfigLog), "{\"hasCompletedOnboarding\":true}\n"; got != want {
		t.Fatalf("synthesized Claude onboarding config = %q, want %q", got, want)
	}
	if _, statErr := os.Stat(fixture.securityLog); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("consented-link path unexpectedly created an isolated keychain: %v", statErr)
	}
	assertOperatorKeychainTargetSurvives(t, fixture)
}

func TestLiveVerificationPartCAuthSelectionEOFIsInputFailure(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 10))
	if err == nil {
		t.Fatalf("release verification accepted closed auth-selection input:\n%s", output)
	}
	for _, want := range []string{"input closed — answer y or n", "Part C authentication selection input failed"} {
		if !strings.Contains(output, want) {
			t.Fatalf("closed auth-selection output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "operator declined") || strings.Contains(output, "Continue with guided manual sign-in instead?") {
		t.Fatalf("closed auth-selection input was reported as a refusal:\n%s", output)
	}
}

func TestLiveVerificationPartCMissingSeedRefusalNamesOnlyOfferedPath(t *testing.T) {
	fixture := newLiveFixture(t)
	if err := os.Remove(filepath.Join(fixture.operatorHome, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(fixture.keychainTarget); err != nil {
		t.Fatal(err)
	}
	output, err := fixture.run(t, strings.Repeat("y\n", 10)+"n\n")
	if err == nil {
		t.Fatalf("release verification accepted refusal of the only available auth path:\n%s", output)
	}
	if !strings.Contains(output, "operator declined isolated-keychain guided sign-in") {
		t.Fatalf("missing-seed refusal did not name the offered path:\n%s", output)
	}
	if strings.Contains(output, "declined both Claude authentication paths") || strings.Contains(output, "Copy only this Codex file") || strings.Contains(output, "Create exactly this Claude Keychains symlink") {
		t.Fatalf("missing-seed refusal claimed an unoffered path:\n%s", output)
	}
}

func TestLiveVerificationPartCAuthCopyFailureHidesSourcePath(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 11), "AGENTCTL_TEST_AUTH_INSTALL_FAIL=1")
	if err == nil {
		t.Fatalf("release verification accepted authentication copy failure:\n%s", output)
	}
	if !strings.Contains(output, "Part C authentication seeding failed") {
		t.Fatalf("copy failure output omitted fixed diagnostic:\n%s", output)
	}
	if strings.Contains(output, fixture.operatorHome) || strings.Contains(output, fakeCodexAuthBody) {
		t.Fatalf("copy failure output exposed source credential data or path:\n%s", output)
	}
}

func TestLiveVerificationPartCConsentDeclineGuidesManualSignIn(t *testing.T) {
	fixture := newLiveFixture(t)
	input := strings.Repeat("y\n", 11) + "n\n" + strings.Repeat("y\n", 4)
	output, err := fixture.run(t, input)
	if err != nil {
		t.Fatalf("manual-auth release verification failed: %v\n%s", err, output)
	}
	for _, want := range []string{
		"Continue with guided Claude sign-in using an isolated empty login keychain instead?",
		"A fresh Claude token will be minted into the isolated temporary keychain.",
		"While attached, complete these authentication steps before checking skills:",
		"In the Claude Code tab, complete onboarding and sign in until a ready prompt appears.",
		"The proven Codex auth.json was copied with your consent.",
		"[CHECKPOINT C.C1] harness authentication ready",
		"Did Claude Code mint a fresh token through guided sign-in and did codex authenticate from the seeded auth.json?",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("manual-auth guidance missing %q:\n%s", want, output)
		}
	}
	wantObservation := strings.Join([]string{
		"dir .|700",
		"dir .codex|700",
		"file .codex/auth.json|600",
		"dir Library/Keychains|700",
		"file Library/Keychains/login.keychain-db|600",
	}, "\n") + "\n"
	if got := readTestFile(t, fixture.authObservationLog); got != wantObservation {
		t.Fatalf("manual-auth launch inherited copied files:\n%s", got)
	}
	if _, statErr := os.Stat(fixture.claudeConfigLog); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("isolated-keychain path received a Claude onboarding config: %v", statErr)
	}
	securityFields := strings.Split(strings.TrimSpace(readTestFile(t, fixture.securityLog)), "\t")
	if len(securityFields) != 5 || securityFields[0] != filepath.Join(strings.TrimSpace(readTestFile(t, fixture.partCRootLog)), "home") || securityFields[1] != "4" || securityFields[2] != "create-keychain" || securityFields[3] != "-p" || securityFields[4] != filepath.Join(strings.TrimSpace(readTestFile(t, fixture.partCRootLog)), "home", "Library", "Keychains", "login.keychain-db") {
		t.Fatalf("isolated keychain creation boundary was wrong: %q", strings.TrimSpace(readTestFile(t, fixture.securityLog)))
	}
	for _, forbidden := range []string{fakeClaudeAuthBody, fakeClaudeCredentialsBody, fakeCodexAuthBody} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("manual-auth transcript printed fake credential contents %q:\n%s", forbidden, output)
		}
	}
	assertOperatorKeychainTargetSurvives(t, fixture)
}

func TestLiveVerificationPartCRefusesConsentAndManualSignIn(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 11)+"n\nn\n", "AGENTCTL_TEST_SKILL_KILL_SERVER_ENOENT=1")
	if err == nil {
		t.Fatalf("release verification accepted refusal of both auth paths:\n%s", output)
	}
	if !strings.Contains(output, "operator declined Claude keychain link and isolated-keychain guided sign-in") {
		t.Fatalf("output did not state both declined auth paths:\n%s", output)
	}
	for _, call := range fixture.calls(t) {
		if call == "skill install" || strings.Contains(call, "--session skillverify") {
			t.Fatalf("Part C work began after both auth paths were refused:\n%s", strings.Join(fixture.calls(t), "\n"))
		}
	}
	if _, statErr := os.Stat(fixture.amqLog); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("AMQ initialization ran after both auth paths were refused: %v", statErr)
	}
	if !strings.Contains(output, "PART C CLEANUP OBSERVED (named tmux socket already absent)") || strings.Contains(output, "PART C CLEANUP FAIL (named tmux socket") {
		t.Fatalf("real tmux connect-ENOENT was not accepted as named-socket absence:\n%s", output)
	}
	partCRoot := strings.TrimSpace(readTestFile(t, fixture.partCRootLog))
	t.Cleanup(func() { _ = os.RemoveAll(partCRoot) })
	if _, statErr := os.Stat(partCRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Part C root survived refusal of both auth paths: %q, err=%v", partCRoot, statErr)
	}
	assertOperatorKeychainTargetSurvives(t, fixture)
}

func TestLiveVerificationPartCAgentctlBoundariesUseIsolatedEnvironment(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 15))
	if err != nil {
		t.Fatalf("release verification failed: %v\n%s", err, output)
	}
	skillHome := strings.TrimSpace(readTestFile(t, fixture.skillRootLog))
	partCRoot := filepath.Dir(skillHome)
	wantProject := filepath.Join(partCRoot, "project")
	wantPathPrefix := filepath.Join(partCRoot, "bin") + string(os.PathListSeparator)
	wantCommands := []string{
		"skill install",
		"launch --session skillverify --roles a:claude,b:codex --dir " + wantProject,
		"attach --session skillverify",
		"kill --session skillverify",
	}
	records := strings.Split(strings.TrimSpace(readTestFile(t, fixture.agentctlEnvironmentLog)), "\n")
	if len(records) != len(wantCommands) {
		t.Fatalf("Part C agentctl environment records = %d, want %d:\n%s", len(records), len(wantCommands), strings.Join(records, "\n"))
	}
	for index, record := range records {
		fields := strings.Split(record, "\t")
		if len(fields) != 4 {
			t.Fatalf("Part C agentctl environment record %d malformed: %q", index, record)
		}
		if fields[0] != wantCommands[index] || fields[1] != wantProject || fields[2] != skillHome || !strings.HasPrefix(fields[3], wantPathPrefix) {
			t.Fatalf("Part C agentctl boundary %d = %q; want command=%q cwd=%q HOME=%q PATH prefix=%q", index, record, wantCommands[index], wantProject, skillHome, wantPathPrefix)
		}
		if fields[2] == os.Getenv("HOME") || !strings.HasPrefix(fields[3], wantPathPrefix) {
			t.Fatalf("Part C agentctl boundary could reach the real HOME or unshimmed tmux context: %q", record)
		}
	}
}

func TestLiveVerificationPartCKillRetryUsesPinnedIsolatedEnvironment(t *testing.T) {
	fixture := newLiveFixture(t)
	originalHome := os.Getenv("HOME")
	originalPath := os.Getenv("PATH")
	output, err := fixture.run(t, strings.Repeat("y\n", 12)+"n\n",
		"AGENTCTL_TEST_PART_C_KILL_CODES=17,0",
		"AGENTCTL_TEST_SKILL_KILL_SERVER_CODES=18,0",
	)
	if err == nil {
		t.Fatalf("release verification accepted Part C checkpoint refusal:\n%s", output)
	}
	skillHome := strings.TrimSpace(readTestFile(t, fixture.skillRootLog))
	partCRoot := filepath.Dir(skillHome)
	wantProject := filepath.Join(partCRoot, "project")
	wantPathPrefix := filepath.Join(partCRoot, "bin") + string(os.PathListSeparator)
	var killRecords []string
	for _, record := range strings.Split(strings.TrimSpace(readTestFile(t, fixture.agentctlEnvironmentLog)), "\n") {
		if strings.HasPrefix(record, "kill --session skillverify\t") {
			killRecords = append(killRecords, record)
		}
	}
	if len(killRecords) != 2 {
		t.Fatalf("Part C kill environment records = %d, want 2:\n%s", len(killRecords), strings.Join(killRecords, "\n"))
	}
	for index, record := range killRecords {
		fields := strings.Split(record, "\t")
		if len(fields) != 4 {
			t.Fatalf("Part C kill environment record %d malformed: %q", index, record)
		}
		if fields[1] != wantProject || fields[2] != skillHome || !strings.HasPrefix(fields[3], wantPathPrefix) {
			t.Fatalf("Part C kill retry %d escaped captured context: %q; want cwd=%q HOME=%q PATH prefix=%q", index, record, wantProject, skillHome, wantPathPrefix)
		}
		if fields[2] == originalHome || fields[3] == originalPath {
			t.Fatalf("Part C kill retry %d reached the real HOME or default PATH: %q", index, record)
		}
	}
	if _, statErr := os.Stat(os.Getenv("AGENTCTL_TEST_SKILL_OWNED")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Part C ownership survived the observed successful retry: %v", statErr)
	}
	if _, statErr := os.Stat(os.Getenv("AGENTCTL_TEST_SKILL_KILLED")); statErr != nil {
		t.Fatalf("Part C successful retry was not observed: %v", statErr)
	}
	assertChildRestoredEnvironment(t, fixture, fixture.dir, originalHome, originalPath)
	assertOperatorKeychainTargetSurvives(t, fixture)
}

func TestLiveVerificationPartCRejectCleansOnlyNamedResources(t *testing.T) {
	fixture := newLiveFixture(t)
	originalHome := os.Getenv("HOME")
	originalPath := os.Getenv("PATH")
	output, err := fixture.run(t, strings.Repeat("y\n", 13)+"n\n")
	if err == nil {
		t.Fatalf("release verification accepted Part C refusal:\n%s", output)
	}
	if !strings.Contains(output, "operator refused checkpoint: harness lists the agentctl skill") {
		t.Fatalf("output missing Part C refusal:\n%s", output)
	}
	calls := strings.Join(fixture.calls(t), "\n")
	for _, want := range []string{"kill --session skillverify", "kill --session relverify"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("calls missing %q:\n%s", want, calls)
		}
	}
	if !strings.Contains(strings.Join(fixture.tmuxCalls(t), "\n"), "-L agentctl-skill-verify-") || !strings.Contains(strings.Join(fixture.tmuxCalls(t), "\n"), "kill-server") {
		t.Fatalf("named Part C tmux socket was not removed:\n%s", strings.Join(fixture.tmuxCalls(t), "\n"))
	}
	for _, path := range []string{os.Getenv("AGENTCTL_TEST_SKILL_KILLED"), os.Getenv("AGENTCTL_TEST_SKILL_SOCKET_KILLED")} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("cleanup marker %q: %v", path, statErr)
		}
	}
	for _, call := range fixture.tmuxCalls(t) {
		if call == "kill-server" {
			t.Fatalf("bare/default-socket kill-server invoked:\n%s", strings.Join(fixture.tmuxCalls(t), "\n"))
		}
	}
	amqRecord := strings.TrimSpace(readTestFile(t, fixture.amqLog))
	if !strings.Contains(amqRecord, "coop init --agents a,b,user|") || strings.Contains(amqRecord, "|"+fixture.dir+"|"+originalHome+"|"+originalPath) {
		t.Fatalf("Part C did not use isolated cwd/HOME/PATH: %q", amqRecord)
	}
	skillHome := strings.TrimSpace(readTestFile(t, fixture.skillRootLog))
	if _, statErr := os.Stat(skillHome); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("temporary Part C HOME survived cleanup: %q, err=%v", skillHome, statErr)
	}
	assertChildRestoredEnvironment(t, fixture, fixture.dir, originalHome, originalPath)
	assertOperatorKeychainTargetSurvives(t, fixture)
}

func assertChildRestoredEnvironment(t *testing.T, fixture liveFixture, wantDir, wantHome, wantPath string) {
	t.Helper()
	canonicalDir, err := filepath.EvalSymlinks(wantDir)
	if err != nil {
		t.Fatal(err)
	}
	records := strings.FieldsFunc(strings.TrimSpace(readTestFile(t, fixture.environmentLog)), func(character rune) bool { return character == '\n' })
	if len(records) == 0 {
		t.Fatal("fixture did not observe a post-teardown child command")
	}
	got := records[len(records)-1]
	want := canonicalDir + "|" + wantHome + "|" + wantPath
	if got != want {
		t.Fatalf("post-teardown child environment = %q, want %q", got, want)
	}
}

func assertPartCCredentialHomeAbsent(t *testing.T, fixture liveFixture) {
	t.Helper()
	partCRoot := strings.TrimSpace(readTestFile(t, fixture.partCRootLog))
	t.Cleanup(func() { _ = os.RemoveAll(partCRoot) })
	partCHome := filepath.Join(partCRoot, "home")
	for _, path := range []string{
		filepath.Join(partCHome, ".claude.json"),
		filepath.Join(partCHome, ".claude", ".credentials.json"),
		filepath.Join(partCHome, ".codex", "auth.json"),
		partCHome,
	} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("credential-bearing Part C path survived cleanup: %q, err=%v", path, statErr)
		}
	}
}

func assertOperatorKeychainTargetSurvives(t *testing.T, fixture liveFixture) {
	t.Helper()
	info, err := os.Stat(fixture.keychainTarget)
	if err != nil || !info.IsDir() {
		t.Fatalf("operator Keychains target did not survive: %q, info=%v, err=%v", fixture.keychainTarget, info, err)
	}
	if got := readTestFile(t, fixture.keychainSentinel); got != "real-keychain-target-must-survive" {
		t.Fatalf("operator Keychains sentinel changed: %q", got)
	}
}

func TestLiveVerificationPartCKeychainLinkFailureAbortsBeforeLaunch(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 12), "AGENTCTL_TEST_KEYCHAIN_LINK_FAIL=1")
	if err == nil {
		t.Fatalf("release verification accepted Keychains link failure:\n%s", output)
	}
	if !strings.Contains(output, "Part C Claude Keychains link creation failed") {
		t.Fatalf("link failure output omitted fixed diagnostic:\n%s", output)
	}
	for _, call := range fixture.calls(t) {
		if call == "skill install" || strings.Contains(call, "--session skillverify") {
			t.Fatalf("Part C work began after Keychains link failure:\n%s", strings.Join(fixture.calls(t), "\n"))
		}
	}
	assertOperatorKeychainTargetSurvives(t, fixture)
}

func TestLiveVerificationPartCIsolatedKeychainFailureAbortsBeforeLaunch(t *testing.T) {
	fixture := newLiveFixture(t)
	input := strings.Repeat("y\n", 11) + "n\ny\n"
	output, err := fixture.run(t, input, "AGENTCTL_TEST_SECURITY_CREATE_FAIL=1")
	if err == nil {
		t.Fatalf("release verification accepted isolated keychain failure:\n%s", output)
	}
	if !strings.Contains(output, "Part C isolated login keychain creation failed") {
		t.Fatalf("isolated-keychain failure output omitted fixed diagnostic:\n%s", output)
	}
	for _, call := range fixture.calls(t) {
		if call == "skill install" || strings.Contains(call, "--session skillverify") {
			t.Fatalf("Part C work began after isolated keychain failure:\n%s", strings.Join(fixture.calls(t), "\n"))
		}
	}
	assertOperatorKeychainTargetSurvives(t, fixture)
}

func TestLiveVerificationPartCCleanupRemovesOnlyOwnedKeychainLinkOnAbort(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 12),
		"AGENTCTL_TEST_PART_C_ATTACH_FAIL=1",
		"AGENTCTL_TEST_PART_C_HOME_RM_FAIL=1",
	)
	if err == nil {
		t.Fatalf("release verification accepted attach abort with retained Part C HOME:\n%s", output)
	}
	for _, want := range []string{
		"PART C CLEANUP PASS (temporary Keychains symlink removed)",
		"PART C CLEANUP FAIL (remove temporary credential HOME",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("attach-abort cleanup output missing %q:\n%s", want, output)
		}
	}
	partCRoot := strings.TrimSpace(readTestFile(t, fixture.partCRootLog))
	t.Cleanup(func() { _ = os.RemoveAll(partCRoot) })
	linkPath := filepath.Join(partCRoot, "home", "Library", "Keychains")
	if _, statErr := os.Lstat(linkPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("owned Keychains link survived abort cleanup: %q, err=%v", linkPath, statErr)
	}
	assertOperatorKeychainTargetSurvives(t, fixture)
}

func TestLiveVerificationPartCAttachAbortRemovesSeededCredentials(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 12),
		"AGENTCTL_TEST_PART_C_ATTACH_FAIL=1",
		"AGENTCTL_TEST_PART_C_RM_FAIL=1",
	)
	if err == nil {
		t.Fatalf("release verification accepted attach abort with failed root cleanup:\n%s", output)
	}
	for _, want := range []string{"Part C attach guidance failed", "PART C CLEANUP FAIL (remove temporary root"} {
		if !strings.Contains(output, want) {
			t.Fatalf("attach-abort cleanup output missing %q:\n%s", want, output)
		}
	}
	assertPartCCredentialHomeAbsent(t, fixture)
	assertOperatorKeychainTargetSurvives(t, fixture)
}

func TestLiveVerificationPartCAttachAbortCleansResources(t *testing.T) {
	fixture := newLiveFixture(t)
	originalHome := os.Getenv("HOME")
	originalPath := os.Getenv("PATH")
	output, err := fixture.run(t, strings.Repeat("y\n", 12), "AGENTCTL_TEST_PART_C_ATTACH_FAIL=1")
	if err == nil {
		t.Fatalf("release verification accepted Part C attach failure:\n%s", output)
	}
	if !strings.Contains(output, "Part C attach guidance failed") {
		t.Fatalf("output missing attach failure:\n%s", output)
	}
	calls := strings.Join(fixture.calls(t), "\n")
	if !strings.Contains(calls, "kill --session skillverify") || !strings.Contains(calls, "kill --session relverify") {
		t.Fatalf("cleanup calls missing:\n%s", calls)
	}
	if _, statErr := os.Stat(os.Getenv("AGENTCTL_TEST_SKILL_SOCKET_KILLED")); statErr != nil {
		t.Fatalf("named socket cleanup missing: %v", statErr)
	}
	assertChildRestoredEnvironment(t, fixture, fixture.dir, originalHome, originalPath)
	assertOperatorKeychainTargetSurvives(t, fixture)
}

func TestLiveVerificationPartCSocketCleanupFailureIsReported(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 13)+"n\n", "AGENTCTL_TEST_SKILL_KILL_SERVER_CODE=1")
	if err == nil {
		t.Fatalf("release verification accepted named-socket cleanup failure:\n%s", output)
	}
	if strings.Contains(output, "[PASS C.5]") || !strings.Contains(output, "PART C CLEANUP FAIL (named tmux socket") {
		t.Fatalf("named-socket cleanup failure was not reported:\n%s", output)
	}
	if !strings.Contains(output, "temporary Keychains symlink retained until fleet and socket cleanup completes") || strings.Contains(output, "temporary Keychains symlink removed") {
		t.Fatalf("Keychains link was not retained behind the failed socket cleanup:\n%s", output)
	}
	partCRoot := strings.TrimSpace(readTestFile(t, fixture.partCRootLog))
	t.Cleanup(func() { _ = os.RemoveAll(partCRoot) })
	linkPath := filepath.Join(partCRoot, "home", "Library", "Keychains")
	if target, readErr := os.Readlink(linkPath); readErr != nil || target != fixture.keychainTarget {
		t.Fatalf("retained Keychains link = %q, err=%v; want target %q", target, readErr, fixture.keychainTarget)
	}
	assertOperatorKeychainTargetSurvives(t, fixture)
}

func TestLiveVerificationPartCSocketAlreadyAbsentIsObserved(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 13)+"n\n", "AGENTCTL_TEST_SKILL_KILL_SERVER_ABSENT=1")
	if err == nil {
		t.Fatalf("release verification accepted Part C refusal:\n%s", output)
	}
	if !strings.Contains(output, "PART C CLEANUP OBSERVED (named tmux socket already absent)") || strings.Contains(output, "PART C CLEANUP FAIL (named tmux socket") {
		t.Fatalf("already-absent named socket was not distinguished:\n%s", output)
	}
}

func TestLiveVerificationPartCRejectsUnboundOrMultilineSocketENOENT(t *testing.T) {
	tests := []struct {
		name string
		env  string
	}{
		{name: "warning before expected line", env: "AGENTCTL_TEST_SKILL_KILL_SERVER_ENOENT_WARNING=1"},
		{name: "different socket", env: "AGENTCTL_TEST_SKILL_KILL_SERVER_ENOENT_WRONG_SOCKET=1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLiveFixture(t)
			output, err := fixture.run(t, strings.Repeat("y\n", 11)+"n\nn\n", test.env)
			if err == nil {
				t.Fatalf("release verification accepted refusal of both auth paths:\n%s", output)
			}
			if !strings.Contains(output, "PART C CLEANUP FAIL (named tmux socket") || strings.Contains(output, "PART C CLEANUP OBSERVED (named tmux socket already absent)") {
				t.Fatalf("untrusted connect-ENOENT was accepted as named-socket absence:\n%s", output)
			}
			partCRoot := strings.TrimSpace(readTestFile(t, fixture.partCRootLog))
			t.Cleanup(func() { _ = os.RemoveAll(partCRoot) })
			if _, statErr := os.Stat(partCRoot); statErr != nil {
				t.Fatalf("Part C root was removed after untrusted connect-ENOENT: %q, err=%v", partCRoot, statErr)
			}
			assertOperatorKeychainTargetSurvives(t, fixture)
		})
	}
}

func TestLiveVerificationPartCRootRemovalFailureIsReported(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 13)+"n\n", "AGENTCTL_TEST_PART_C_RM_FAIL=1")
	if err == nil {
		t.Fatalf("release verification accepted Part C root-removal failure:\n%s", output)
	}
	if strings.Contains(output, "[PASS C.5]") || !strings.Contains(output, "PART C CLEANUP FAIL (remove temporary root") {
		t.Fatalf("Part C root-removal failure was not reported:\n%s", output)
	}
	assertPartCCredentialHomeAbsent(t, fixture)
	assertOperatorKeychainTargetSurvives(t, fixture)
}

func TestLiveVerificationPartCLaunchFailureCleansNamedSocket(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 12), "AGENTCTL_TEST_PART_C_LAUNCH_FAIL=1")
	if err == nil {
		t.Fatalf("release verification accepted Part C launch failure:\n%s", output)
	}
	if !strings.Contains(output, "Part C skill fleet launch failed") || !strings.Contains(strings.Join(fixture.tmuxCalls(t), "\n"), "-L agentctl-skill-verify-") {
		t.Fatalf("named socket cleanup was not attempted after launch failure:\n%s\n%s", output, strings.Join(fixture.tmuxCalls(t), "\n"))
	}
	assertOperatorKeychainTargetSurvives(t, fixture)
}

func TestLiveVerificationPartCSocketCleanupRetryResetsExitStatus(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 13)+"n\n", "AGENTCTL_TEST_SKILL_KILL_SERVER_CODES=17,0")
	if err == nil {
		t.Fatalf("release verification accepted Part C checkpoint refusal:\n%s", output)
	}
	for _, want := range []string{
		"PART C CLEANUP FAIL (named tmux socket kill-server exited 17)",
		"PART C CLEANUP PASS (named tmux socket killed)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("cleanup retry output missing %q:\n%s", want, output)
		}
	}
}

func TestLiveVerificationDoesNotCallRefusalOnCheckpointEOF(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 10))
	if err == nil {
		t.Fatalf("release verification accepted checkpoint EOF:\n%s", output)
	}
	if !strings.Contains(output, "input closed — answer y or n") || strings.Contains(output, "operator refused checkpoint:") {
		t.Fatalf("checkpoint EOF was reported as refusal:\n%s", output)
	}
}

func TestLiveVerificationRejectsAttachNarrationAndTearsDown(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, "n\n")
	if err == nil {
		t.Fatalf("release verification accepted attach-narration refusal:\n%s", output)
	}
	if !strings.Contains(output, "[CHECKPOINT FAIL B.C1] operator refused checkpoint: attach narration") {
		t.Fatalf("output missing attach-narration refusal:\n%s", output)
	}
	calls := strings.Join(fixture.calls(t), "\n")
	if !strings.Contains(calls, "kill --session relverify") || strings.Contains(calls, "clear --session relverify") || strings.Contains(calls, "compact --session relverify") || strings.Contains(calls, "relaunch --session relverify") {
		t.Fatalf("Part B commands continued after attach-narration refusal:\n%s", calls)
	}
}

func TestLiveVerificationRelaunchesClaudeFromStoredQuadByExactIDs(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 15))
	if err != nil {
		t.Fatalf("release verification failed: %v\n%s", err, output)
	}
	for _, want := range []string{
		"RELAUNCH PASS (role a relaunched through the ESRCH-gated command)",
		`agentctl: relaunched role "a" in session "relverify"; the shim is ready`,
		"RELAUNCH PASS (role a restored to running)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	wantAgentctl := []string{
		"version",
		"status --session relverify",
		"launch --session relverify --roles a:claude,b:codex --efforts b:high",
		"clear --session relverify a",
		"clear --session relverify b",
		"compact --session relverify a",
		"status --session relverify",
		"relaunch --session relverify a",
		"status --session relverify",
		"kill --session relverify",
		"status --session relverify",
		"skill install",
		"launch --session skillverify --roles a:claude,b:codex --dir " + filepath.Join(strings.TrimSpace(readTestFile(t, fixture.skillRootLog)), "..", "project"),
		"attach --session skillverify",
		"kill --session skillverify",
	}
	if got := fixture.calls(t); strings.Join(got, "\n") != strings.Join(wantAgentctl, "\n") {
		t.Fatalf("agentctl calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantAgentctl, "\n"))
	}
	wantTmux := []string{
		"-V",
		"list-sessions -F #{session_id}\t#{session_name}",
		"list-windows -t $4 -F #{window_id}\t#{pane_id}\t#{window_name}",
		"list-windows -t $4 -F #{window_id}\t#{pane_id}\t#{window_name}",
	}
	gotTmux := fixture.tmuxCalls(t)
	if len(gotTmux) != len(wantTmux)+1 || strings.Join(gotTmux[:len(wantTmux)], "\n") != strings.Join(wantTmux, "\n") || !strings.HasPrefix(gotTmux[len(wantTmux)], "-L agentctl-skill-verify-") || !strings.HasSuffix(gotTmux[len(wantTmux)], " kill-server") {
		t.Fatalf("tmux calls:\n%s\nwant:\n%s", strings.Join(gotTmux, "\n"), strings.Join(wantTmux, "\n"))
	}
	notes, err := os.ReadFile(filepath.Join(fixture.dir, "docs/release-verification-notes.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"- Checkpoint B.C9 relaunch: PASS (stored claude/default/default provenance; pane ID changed); fresh claude input with no junk: operator confirmed: y",
		"- Teardown status: exit 3 (session absent; other tmux sessions remained)",
	} {
		if !strings.Contains(string(notes), want) {
			t.Fatalf("notes missing %q:\n%s", want, notes)
		}
	}
}

func TestLiveVerificationResolvesShimRoleWindowByExactWindowName(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 15))
	if err != nil {
		t.Fatalf("release verification failed: %v\n%s", err, output)
	}
	var roleResolutionCall string
	for _, call := range fixture.tmuxCalls(t) {
		if strings.HasPrefix(call, "list-windows -t $4 -F ") {
			roleResolutionCall = call
			break
		}
	}
	if roleResolutionCall == "" {
		t.Fatalf("tmux calls omit exact-session role-window resolution:\n%s", strings.Join(fixture.tmuxCalls(t), "\n"))
	}
	if !strings.Contains(roleResolutionCall, "#{window_name}") || strings.Contains(roleResolutionCall, "#{@agentctl_role}") {
		t.Fatalf("role-window resolution uses stale metadata instead of the exact window name: %q", roleResolutionCall)
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

func TestLiveVerificationPairsNewPaneWithFreshInputObservation(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 15))
	if err != nil {
		t.Fatalf("release verification failed: %v\n%s", err, output)
	}
	for _, want := range []string{
		"In the claude tab, type junk into the input box again; do NOT press Enter.",
		"RELAUNCH PASS (role a pane changed from %5 to %12)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	wantPrompt := `One of the fleet's harnesses was terminated, and agentctl relaunched it from
the fleet's stored configuration. The new pane is a new process: its harness,
model and effort carry over; its conversation does not, so the junk you typed
is gone.

Do you see a fresh, ready claude input surface with no trace of that junk?`
	if !strings.Contains(output, wantPrompt) {
		t.Fatalf("release verifier missing final relaunch prompt:\n%s", wantPrompt)
	}
}

func TestLiveVerificationRejectsReusedRelaunchPaneID(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 9), "AGENTCTL_TEST_RELAUNCHED_PANE_ID=%5")
	if err == nil {
		t.Fatalf("release verification accepted reused pane ID:\n%s", output)
	}
	if !strings.Contains(output, "RELAUNCH FAIL (recreated role a reused original pane %5)") {
		t.Fatalf("output missing reused-pane failure:\n%s", output)
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
	for _, want := range []string{
		"TEARDOWN PASS (agentctl status exit 3 proves relverify is absent)",
		"TEARDOWN PASS (no relverify tmux process remains)",
	} {
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

func TestLiveVerificationCreatesKeeperWhenDefaultServerIsAbsent(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 15), "AGENTCTL_TEST_NO_SERVER_PRECHECK=1")
	if err != nil {
		t.Fatalf("no-server verification failed: %v\n%s", err, output)
	}
	for _, want := range []string{
		"error connecting to /private/tmp/tmux-501/default (No such file or directory)",
		"PART B PRECHECK OBSERVED (default tmux server absent: connect ENOENT)",
		"PART B KEEPER CREATED (wrapper-owned session agentctl-release-verify-keeper-",
		"PART B KEEPER CLEANUP PASS (wrapper-owned session agentctl-release-verify-keeper-",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}

	var createCall, killCall string
	for _, call := range fixture.tmuxCalls(t) {
		switch {
		case strings.HasPrefix(call, "new-session -d -s agentctl-release-verify-keeper-"):
			createCall = call
		case strings.HasPrefix(call, "kill-session -t =agentctl-release-verify-keeper-"):
			killCall = call
		}
	}
	if createCall == "" || !strings.HasSuffix(createCall, " -n keeper -- exec sleep 86400") {
		t.Fatalf("keeper create call missing or malformed:\n%s", strings.Join(fixture.tmuxCalls(t), "\n"))
	}
	createdName := strings.TrimSuffix(strings.TrimPrefix(createCall, "new-session -d -s "), " -n keeper -- exec sleep 86400")
	if killCall != "kill-session -t ="+createdName {
		t.Fatalf("keeper cleanup targeted %q, want exact created session %q", killCall, createdName)
	}
	if _, statErr := os.Stat(os.Getenv("AGENTCTL_TEST_KEEPER_OWNED")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("keeper survived verification: %v", statErr)
	}
	if _, statErr := os.Stat(os.Getenv("AGENTCTL_TEST_KEEPER_KILLED")); statErr != nil {
		t.Fatalf("keeper teardown was not observed: %v", statErr)
	}

	notes := readTestFile(t, filepath.Join(fixture.dir, "docs/release-verification-notes.md"))
	for _, want := range []string{
		"- Part B pre-check: default tmux server absent (connect ENOENT)",
		"- Part B keeper: created and removed wrapper-owned session `" + createdName + "`",
	} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes missing %q:\n%s", want, notes)
		}
	}
}

func TestLiveVerificationExitTrapRemovesKeeper(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, "",
		"AGENTCTL_TEST_NO_SERVER_PRECHECK=1",
		"AGENTCTL_TEST_UNEXPECTED_EXIT_AFTER_PART_B_LAUNCH=1",
	)
	if err == nil {
		t.Fatalf("unexpected Part B exit returned success:\n%s", output)
	}
	for _, want := range []string{
		"simulated unexpected Part B exit",
		"PART B CLEANUP PASS (relverify kill exited 0)",
		"PART B KEEPER CLEANUP PASS (wrapper-owned session agentctl-release-verify-keeper-",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("trap output missing %q:\n%s", want, output)
		}
	}
	if _, statErr := os.Stat(os.Getenv("AGENTCTL_TEST_KEEPER_OWNED")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("keeper survived trapped exit: %v", statErr)
	}
	if _, statErr := os.Stat(os.Getenv("AGENTCTL_TEST_KEEPER_KILLED")); statErr != nil {
		t.Fatalf("keeper trap teardown was not observed: %v", statErr)
	}
}

func TestLiveVerificationNeverKillsKeeperItDidNotCreate(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, "",
		"AGENTCTL_TEST_NO_SERVER_PRECHECK=1",
		"AGENTCTL_TEST_KEEPER_CREATE_CODE=17",
	)
	if err == nil {
		t.Fatalf("keeper creation failure returned success:\n%s", output)
	}
	if !strings.Contains(output, "could not create wrapper-owned tmux keeper session agentctl-release-verify-keeper-") {
		t.Fatalf("keeper creation failure was not reported:\n%s", output)
	}
	for _, call := range fixture.tmuxCalls(t) {
		if strings.HasPrefix(call, "kill-session ") {
			t.Fatalf("wrapper killed a keeper it did not create: %q", call)
		}
	}
	if _, statErr := os.Stat(os.Getenv("AGENTCTL_TEST_KEEPER_OWNED")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed keeper creation left an ownership marker: %v", statErr)
	}
	if _, statErr := os.Stat(os.Getenv("AGENTCTL_TEST_KEEPER_KILLED")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed keeper creation recorded a false teardown: %v", statErr)
	}
}

func TestLiveVerificationRejectsUnexpectedStatusFailure(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 12), "AGENTCTL_TEST_STATUS_AFTER_KILL_CODE=6", "AGENTCTL_TEST_STATUS_AFTER_KILL_MESSAGE=agentctl: transport failure")
	if err == nil || !strings.Contains(output, "TEARDOWN FAIL (agentctl status") {
		t.Fatalf("unexpected status failure was not rejected: err=%v\n%s", err, output)
	}
}

func TestLiveVerificationAcceptsNoServerStatusAsAbsent(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 15), "AGENTCTL_TEST_STATUS_AFTER_KILL_CODE=6", "AGENTCTL_TEST_STATUS_AFTER_KILL_MESSAGE=agentctl: tmux list sessions: exit status 1: no server running")
	if err != nil {
		t.Fatalf("no-server status was not accepted as absence: %v\n%s", err, output)
	}
	notes, readErr := os.ReadFile(filepath.Join(fixture.dir, "docs/release-verification-notes.md"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(notes), "- Teardown status: exit 6 (session absent; relverify was last and tmux server exited)") {
		t.Fatalf("notes did not record expected exit 6 outcome:\n%s", notes)
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

func TestLiveVerificationRejectsPgrepFailure(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 12), "AGENTCTL_TEST_PGREP_CODE=2")
	if err == nil || !strings.Contains(output, "TEARDOWN FAIL (pgrep exited 2)") {
		t.Fatalf("pgrep failure was not rejected: err=%v\n%s", err, output)
	}
}

func TestLiveVerificationWaitsForTmuxAttachClientToExit(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 15), "AGENTCTL_TEST_PGREP_CODES=0,1")
	if err != nil {
		t.Fatalf("transient tmux survivor was not given time to exit: %v\n%s", err, output)
	}
	if !strings.Contains(output, "TEARDOWN PASS (no relverify tmux process remains)") {
		t.Fatalf("output missing teardown pass after transient survivor:\n%s", output)
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
