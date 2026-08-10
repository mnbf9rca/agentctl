package hack_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const probeSentinelHelperEnv = "AGENTCTL_PROBE_SENTINEL_HELPER"

func TestProbeResponsiveSentinelHelper(t *testing.T) {
	if os.Getenv(probeSentinelHelperEnv) != "1" {
		return
	}

	readyFile := os.Getenv("AGENTCTL_PROBE_SENTINEL_READY_FILE")
	ackFile := os.Getenv("AGENTCTL_PROBE_SENTINEL_ACK_FILE")
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGUSR1)
	defer signal.Stop(signals)
	if err := os.WriteFile(readyFile, []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("AGENTCTL_PROBE_SENTINEL_EXIT_AFTER_READY") == "1" {
		return
	}
	for range signals {
		if err := os.WriteFile(ackFile, []byte("responsive\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProbeResponsiveSentinelCheckRejectsExitedZombie(t *testing.T) {
	live := startProbeSentinel(t, false)
	if !probeSentinelResponsive(live.command.Process, live.ackFile) {
		t.Fatal("responsive live sentinel was not accepted")
	}

	zombie := startProbeSentinel(t, true)
	waitForZombie(t, zombie.command.Process.Pid)
	if !processExists(zombie.command.Process.Pid) {
		t.Fatal("zombie fixture did not expose the kill(pid, 0) false-positive boundary")
	}
	if probeSentinelResponsive(zombie.command.Process, zombie.ackFile) {
		t.Fatal("exited/zombie sentinel was accepted as responsive")
	}
}

func TestProbeShimSIGHUPRefusesUnsupportedHarness(t *testing.T) {
	t.Parallel()

	output := filepath.Join(t.TempDir(), "probe.txt")
	command := exec.Command("bash", "hack/probe-shim-sighup.sh", "--harness", "other", "--output", output)
	command.Dir = repositoryRoot(t)
	combined, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("probe exit = 0, want refusal; output:\n%s", combined)
	}
	if !strings.Contains(string(combined), "harness must be claude or codex") {
		t.Fatalf("probe output = %q, want closed harness refusal", combined)
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("output file exists after refusal: %v", statErr)
	}
}

func TestProbeShimSIGHUPRefusesExistingOutput(t *testing.T) {
	t.Parallel()

	output := filepath.Join(t.TempDir(), "probe.txt")
	if err := os.WriteFile(output, []byte("sentinel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", "hack/probe-shim-sighup.sh", "--harness", "claude", "--output", output)
	command.Dir = repositoryRoot(t)
	combined, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("probe exit = 0, want refusal; output:\n%s", combined)
	}
	if !strings.Contains(string(combined), "output already exists") {
		t.Fatalf("probe output = %q, want existing-output refusal", combined)
	}
	contents, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(contents); got != "sentinel\n" {
		t.Fatalf("existing output = %q, want sentinel unchanged", got)
	}
}

func TestProbeShimSIGHUPRecordsTopologyOutcomeAndCleansOwnedFixture(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			fixture := newProbeFixture(t, harness)
			output := filepath.Join(t.TempDir(), harness+".txt")
			sentinel := startProbeSentinel(t, false)

			command := exec.Command("bash", "hack/probe-shim-sighup.sh", "--harness", harness, "--output", output)
			command.Dir = repositoryRoot(t)
			command.Env = append(os.Environ(),
				"PATH="+fixture.binDir+":"+os.Getenv("PATH"),
				"AGENTCTL_PROBE_SCRIPT_BIN="+filepath.Join(fixture.binDir, "script"),
				"AGENTCTL_PROBE_PS_BIN="+filepath.Join(fixture.binDir, "ps"),
				"AGENTCTL_PROBE_SHIM_PID_FILE="+fixture.shimPIDFile,
				"AGENTCTL_PROBE_CHILD_PID_FILE="+fixture.childPIDFile,
				"AGENTCTL_PROBE_PS_COUNT_FILE="+fixture.psCountFile,
			)
			combined, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("probe error = %v; output:\n%s", err, combined)
			}
			if !probeSentinelResponsive(sentinel.command.Process, sentinel.ackFile) {
				t.Fatal("unrelated sentinel did not answer the post-cleanup liveness handshake")
			}

			fields := readProbeFields(t, output)
			wantVersion := "1.0 (Claude Code)"
			if harness == "codex" {
				wantVersion = "codex-cli 1.0"
			}
			want := map[string]string{
				"harness":               harness,
				"harness_version":       wantVersion,
				"topology":              "shim-parent-of-harness-child-on-pty",
				"child_ppid_matches":    "true",
				"child_tty":             "ttys999",
				"child_command":         fixture.directCommand,
				"signal_target":         "owned-shim-only",
				"signal":                "SIGHUP",
				"shim_terminated":       "true",
				"child_outcome":         "survived",
				"default_tmux_targeted": "false",
			}
			for key, value := range want {
				if fields[key] != value {
					t.Errorf("%s = %q, want %q; fields=%#v", key, fields[key], value, fields)
				}
			}
			for _, key := range []string{"shim_pid", "child_pid"} {
				pid, parseErr := strconv.Atoi(fields[key])
				if parseErr != nil || pid <= 0 {
					t.Errorf("%s = %q, want positive pid", key, fields[key])
				}
			}
			childPID, err := strconv.Atoi(strings.TrimSpace(readFile(t, fixture.childPIDFile)))
			if err != nil {
				t.Fatal(err)
			}
			if processExists(childPID) {
				t.Fatalf("owned fixture child %d remains after probe cleanup", childPID)
			}
			if _, err := os.Stat(fixture.tmuxMarker); !os.IsNotExist(err) {
				t.Fatalf("tmux marker exists; probe targeted tmux: %v", err)
			}
		})
	}
}

func TestProbeShimSIGHUPRefusesIntermediateDirectChildAndCleansOwnedFixture(t *testing.T) {
	fixture := newIntermediateProbeFixture(t, "claude")
	output := filepath.Join(t.TempDir(), "claude.txt")
	sentinel := startProbeSentinel(t, false)

	command := exec.Command("bash", "hack/probe-shim-sighup.sh", "--harness", "claude", "--output", output)
	command.Dir = repositoryRoot(t)
	command.Env = append(os.Environ(),
		"PATH="+fixture.binDir+":"+os.Getenv("PATH"),
		"AGENTCTL_PROBE_SCRIPT_BIN="+filepath.Join(fixture.binDir, "script"),
		"AGENTCTL_PROBE_PS_BIN="+filepath.Join(fixture.binDir, "ps"),
		"AGENTCTL_PROBE_SHIM_PID_FILE="+fixture.shimPIDFile,
		"AGENTCTL_PROBE_CHILD_PID_FILE="+fixture.childPIDFile,
		"AGENTCTL_PROBE_PS_COUNT_FILE="+fixture.psCountFile,
		"AGENTCTL_PROBE_INTERMEDIATE_BIN="+fixture.intermediateBin,
		"AGENTCTL_PROBE_GRANDCHILD_PID_FILE="+fixture.grandchildPIDFile,
	)
	combined, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("probe exit = 0, want wrong-direct-child refusal; output:\n%s", combined)
	}
	wantRefusal := fmt.Sprintf(`direct child command "/opt/agentctl-probe/bridge" did not match selected claude harness command %q`, fixture.selectedCommand)
	if !strings.Contains(string(combined), wantRefusal) {
		t.Fatalf("probe output = %q, want factual wrong-direct-child refusal", combined)
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("output file exists after wrong-direct-child refusal: %v", statErr)
	}
	if !probeSentinelResponsive(sentinel.command.Process, sentinel.ackFile) {
		t.Fatal("unrelated sentinel did not answer the post-cleanup liveness handshake")
	}
	for _, pidFile := range []string{fixture.childPIDFile, fixture.grandchildPIDFile} {
		pid, parseErr := strconv.Atoi(strings.TrimSpace(readFile(t, pidFile)))
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if processExists(pid) {
			t.Fatalf("owned fixture process %d from %s remains after refusal cleanup", pid, pidFile)
		}
	}
	if _, err := os.Stat(fixture.tmuxMarker); !os.IsNotExist(err) {
		t.Fatalf("tmux marker exists; probe targeted tmux: %v", err)
	}
}

func TestProbeShimSIGHUPMissingGrandchildPIDIsBoundedAndCleansOwnedFixture(t *testing.T) {
	fixture := newIntermediateProbeFixture(t, "claude")
	output := filepath.Join(t.TempDir(), "claude.txt")
	actualGrandchildPIDFile := filepath.Join(filepath.Dir(fixture.grandchildPIDFile), "actual-grandchild.pid")
	fakePSPIDFile := filepath.Join(filepath.Dir(fixture.grandchildPIDFile), "fake-ps.pid")
	cleanupProbePIDFiles(t, fakePSPIDFile, actualGrandchildPIDFile, fixture.childPIDFile, fixture.shimPIDFile)
	sentinel := startProbeSentinel(t, false)

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "bash", "hack/probe-shim-sighup.sh", "--harness", "claude", "--output", output)
	command.Dir = repositoryRoot(t)
	command.Env = append(os.Environ(),
		"PATH="+fixture.binDir+":"+os.Getenv("PATH"),
		"AGENTCTL_PROBE_SCRIPT_BIN="+filepath.Join(fixture.binDir, "script"),
		"AGENTCTL_PROBE_PS_BIN="+filepath.Join(fixture.binDir, "ps"),
		"AGENTCTL_PROBE_SHIM_PID_FILE="+fixture.shimPIDFile,
		"AGENTCTL_PROBE_CHILD_PID_FILE="+fixture.childPIDFile,
		"AGENTCTL_PROBE_PS_COUNT_FILE="+fixture.psCountFile,
		"AGENTCTL_PROBE_INTERMEDIATE_BIN="+fixture.intermediateBin,
		"AGENTCTL_PROBE_GRANDCHILD_PID_FILE="+fixture.grandchildPIDFile,
		"AGENTCTL_PROBE_ACTUAL_GRANDCHILD_PID_FILE="+actualGrandchildPIDFile,
		"AGENTCTL_PROBE_FAKE_PS_PID_FILE="+fakePSPIDFile,
		"AGENTCTL_PROBE_SUPPRESS_GRANDCHILD_PID_FILE=1",
	)
	combinedFile, err := os.CreateTemp(t.TempDir(), "combined-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	command.Stdout = combinedFile
	command.Stderr = combinedFile
	runErr := command.Run()
	if err := combinedFile.Close(); err != nil {
		t.Fatal(err)
	}
	combined, err := os.ReadFile(combinedFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("probe hung instead of using its bounded topology loop; output:\n%s", combined)
	}
	if runErr == nil {
		t.Fatalf("probe exit = 0, want missing-topology refusal; output:\n%s", combined)
	}
	if !strings.Contains(string(combined), "could not observe the selected claude harness as a direct child of owned shim") {
		t.Fatalf("probe output = %q, want bounded missing-topology refusal", combined)
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("output file exists after missing-topology refusal: %v", statErr)
	}
	if !probeSentinelResponsive(sentinel.command.Process, sentinel.ackFile) {
		t.Fatal("unrelated sentinel did not answer the post-cleanup liveness handshake")
	}
	for _, pidFile := range []string{fixture.childPIDFile, actualGrandchildPIDFile} {
		pid, parseErr := strconv.Atoi(strings.TrimSpace(readFile(t, pidFile)))
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if processExists(pid) {
			t.Fatalf("owned fixture process %d from %s remains after bounded refusal cleanup", pid, pidFile)
		}
	}
	if _, err := os.Stat(fixture.tmuxMarker); !os.IsNotExist(err) {
		t.Fatalf("tmux marker exists; probe targeted tmux: %v", err)
	}
}

type probeFixture struct {
	binDir            string
	shimPIDFile       string
	childPIDFile      string
	grandchildPIDFile string
	psCountFile       string
	tmuxMarker        string
	directCommand     string
	selectedCommand   string
	intermediateBin   string
}

func newProbeFixture(t *testing.T, harness string) probeFixture {
	t.Helper()
	return newProbeFixtureWithDirectChild(t, harness, "", false)
}

func newIntermediateProbeFixture(t *testing.T, harness string) probeFixture {
	t.Helper()
	return newProbeFixtureWithDirectChild(t, harness, "/opt/agentctl-probe/bridge", true)
}

func newProbeFixtureWithDirectChild(t *testing.T, harness, directCommand string, intermediate bool) probeFixture {
	t.Helper()
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	selectedCommand := filepath.Join(binDir, harness)
	if directCommand == "" {
		directCommand = selectedCommand
	}
	shimPIDFile := filepath.Join(dir, "shim.pid")
	childPIDFile := filepath.Join(dir, "child.pid")
	grandchildPIDFile := filepath.Join(dir, "grandchild.pid")
	psCountFile := filepath.Join(dir, "ps.count")
	tmuxMarker := filepath.Join(dir, "tmux-called")
	versionOutput := "1.0 (Claude Code)"
	if harness == "codex" {
		versionOutput = "WARNING: fixture could not create PATH aliases\ncodex-cli 1.0"
	}
	writeExecutable(t, filepath.Join(binDir, harness), fmt.Sprintf(`#!/bin/sh
if [ "${1-}" = "--version" ]; then
  printf '%%s\n' '%s'
  exit 0
fi
trap '' HUP
while :; do sleep 1; done
`, versionOutput))
	intermediateBin := ""
	if intermediate {
		intermediateBin = filepath.Join(binDir, "bridge")
		writeExecutable(t, intermediateBin, `#!/bin/sh
"$@" &
grandchild=$!
if [ -n "${AGENTCTL_PROBE_ACTUAL_GRANDCHILD_PID_FILE-}" ]; then
  echo "$grandchild" >"$AGENTCTL_PROBE_ACTUAL_GRANDCHILD_PID_FILE"
fi
if [ "${AGENTCTL_PROBE_SUPPRESS_GRANDCHILD_PID_FILE-0}" != 1 ]; then
  echo "$grandchild" >"$AGENTCTL_PROBE_GRANDCHILD_PID_FILE"
fi
terminate() {
  kill -TERM "$grandchild" 2>/dev/null || true
  wait "$grandchild" 2>/dev/null || true
  exit 0
}
trap terminate TERM INT
trap '' HUP
wait "$grandchild"
`)
	}
	writeExecutable(t, filepath.Join(binDir, "script"), `#!/bin/sh
echo "$$" >"$AGENTCTL_PROBE_SHIM_PID_FILE"
shift 2
if [ -n "${AGENTCTL_PROBE_INTERMEDIATE_BIN-}" ]; then
  set -- "$AGENTCTL_PROBE_INTERMEDIATE_BIN" "$@"
fi
"$@" &
child=$!
echo "$child" >"$AGENTCTL_PROBE_CHILD_PID_FILE"
terminate() {
  kill -TERM "$child" 2>/dev/null || true
  wait "$child" 2>/dev/null || true
  exit 0
}
trap terminate TERM INT
wait "$child"
`)
	writeExecutable(t, filepath.Join(binDir, "ps"), fmt.Sprintf(`#!/bin/sh
if [ -n "${AGENTCTL_PROBE_FAKE_PS_PID_FILE-}" ]; then
  echo "$$" >"$AGENTCTL_PROBE_FAKE_PS_PID_FILE"
fi
shim=$(cat "$AGENTCTL_PROBE_SHIM_PID_FILE")
child=$(cat "$AGENTCTL_PROBE_CHILD_PID_FILE")
count=0
if [ -f "$AGENTCTL_PROBE_PS_COUNT_FILE" ]; then count=$(cat "$AGENTCTL_PROBE_PS_COUNT_FILE"); fi
count=$((count + 1))
echo "$count" >"$AGENTCTL_PROBE_PS_COUNT_FILE"
if [ -n "${AGENTCTL_PROBE_GRANDCHILD_PID_FILE-}" ] && [ ! -s "$AGENTCTL_PROBE_GRANDCHILD_PID_FILE" ]; then
  exit 0
fi
tty=ttys999
if [ "$count" -eq 1 ]; then tty='??'; fi
direct_command=%q
printf ' %%s %%s %%s %%s\n' "$child" "$shim" "$tty" "$direct_command"
`, directCommand))
	writeExecutable(t, filepath.Join(binDir, "tmux"), fmt.Sprintf("#!/bin/sh\ntouch %q\nexit 99\n", tmuxMarker))
	return probeFixture{
		binDir:            binDir,
		shimPIDFile:       shimPIDFile,
		childPIDFile:      childPIDFile,
		grandchildPIDFile: grandchildPIDFile,
		psCountFile:       psCountFile,
		tmuxMarker:        tmuxMarker,
		directCommand:     directCommand,
		selectedCommand:   selectedCommand,
		intermediateBin:   intermediateBin,
	}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readProbeFields(t *testing.T, path string) map[string]string {
	t.Helper()
	fields := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(readFile(t, path)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" || value == "" {
			t.Fatalf("malformed probe output line %q", line)
		}
		if _, duplicate := fields[key]; duplicate {
			t.Fatalf("duplicate probe output key %q", key)
		}
		fields[key] = value
	}
	return fields
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func processExists(pid int) bool {
	process, err := os.FindProcess(pid)
	return err == nil && process.Signal(syscall.Signal(0)) == nil
}

type probeSentinel struct {
	command *exec.Cmd
	ackFile string
}

func startProbeSentinel(t *testing.T, exitAfterReady bool) probeSentinel {
	t.Helper()
	dir := t.TempDir()
	readyFile := filepath.Join(dir, "ready")
	ackFile := filepath.Join(dir, "ack")
	command := exec.Command(os.Args[0], "-test.run=^TestProbeResponsiveSentinelHelper$")
	command.Env = append(os.Environ(),
		probeSentinelHelperEnv+"=1",
		"AGENTCTL_PROBE_SENTINEL_READY_FILE="+readyFile,
		"AGENTCTL_PROBE_SENTINEL_ACK_FILE="+ackFile,
	)
	if exitAfterReady {
		command.Env = append(command.Env, "AGENTCTL_PROBE_SENTINEL_EXIT_AFTER_READY=1")
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	})
	if !waitForProbeFile(readyFile, time.Second) {
		t.Fatalf("sentinel %d did not become ready", command.Process.Pid)
	}
	return probeSentinel{command: command, ackFile: ackFile}
}

func probeSentinelResponsive(process *os.Process, ackFile string) bool {
	if err := os.Remove(ackFile); err != nil && !os.IsNotExist(err) {
		return false
	}
	if err := process.Signal(syscall.SIGUSR1); err != nil {
		return false
	}
	if !waitForProbeFile(ackFile, time.Second) {
		return false
	}
	contents, err := os.ReadFile(ackFile)
	return err == nil && string(contents) == "responsive\n"
}

func waitForZombie(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var lastOutput string
	var lastErr error
	for time.Now().Before(deadline) {
		output, err := exec.Command("/bin/ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		lastOutput = strings.TrimSpace(string(output))
		lastErr = err
		if err == nil && strings.HasPrefix(lastOutput, "Z") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d did not enter zombie state; last ps stat = %q, error = %v", pid, lastOutput, lastErr)
}

func waitForProbeFile(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func cleanupProbePIDFiles(t *testing.T, paths ...string) {
	t.Helper()
	t.Cleanup(func() {
		for _, path := range paths {
			contents, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(contents)))
			if err != nil || pid <= 0 {
				continue
			}
			if process, err := os.FindProcess(pid); err == nil {
				_ = process.Kill()
			}
		}
	})
}
