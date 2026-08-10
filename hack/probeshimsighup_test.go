package hack_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

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
			sentinel := exec.Command("sleep", "30")
			if err := sentinel.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = sentinel.Process.Kill()
				_, _ = sentinel.Process.Wait()
			})

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
			if err := sentinel.Process.Signal(syscall.Signal(0)); err != nil {
				t.Fatalf("unrelated sentinel was terminated: %v", err)
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

type probeFixture struct {
	binDir       string
	shimPIDFile  string
	childPIDFile string
	psCountFile  string
	tmuxMarker   string
}

func newProbeFixture(t *testing.T, harness string) probeFixture {
	t.Helper()
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	shimPIDFile := filepath.Join(dir, "shim.pid")
	childPIDFile := filepath.Join(dir, "child.pid")
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
	writeExecutable(t, filepath.Join(binDir, "script"), `#!/bin/sh
echo "$$" >"$AGENTCTL_PROBE_SHIM_PID_FILE"
shift 2
"$@" &
child=$!
echo "$child" >"$AGENTCTL_PROBE_CHILD_PID_FILE"
wait "$child"
`)
	writeExecutable(t, filepath.Join(binDir, "ps"), `#!/bin/sh
shim=$(cat "$AGENTCTL_PROBE_SHIM_PID_FILE")
child=$(cat "$AGENTCTL_PROBE_CHILD_PID_FILE")
count=0
if [ -f "$AGENTCTL_PROBE_PS_COUNT_FILE" ]; then count=$(cat "$AGENTCTL_PROBE_PS_COUNT_FILE"); fi
count=$((count + 1))
echo "$count" >"$AGENTCTL_PROBE_PS_COUNT_FILE"
tty=ttys999
if [ "$count" -eq 1 ]; then tty='??'; fi
printf ' %s %s %s %s\n' "$child" "$shim" "$tty" "$3"
`)
	writeExecutable(t, filepath.Join(binDir, "tmux"), fmt.Sprintf("#!/bin/sh\ntouch %q\nexit 99\n", tmuxMarker))
	return probeFixture{binDir: binDir, shimPIDFile: shimPIDFile, childPIDFile: childPIDFile, psCountFile: psCountFile, tmuxMarker: tmuxMarker}
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
