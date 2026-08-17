//go:build darwin

package hack_test

import (
	"crypto/sha256"
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
)

func TestShimVersionFixtureExercisesBothBuiltArtifacts(t *testing.T) {
	repository := shimMatrixRepositoryRoot(t)
	temporary := t.TempDir()
	currentBinary := filepath.Join(temporary, "agentctl-current")
	build := exec.Command("go", "build", "-o", currentBinary, "./cmd/agentctl")
	build.Dir = repository
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build current agentctl: %v\n%s", err, output)
	}

	artifactDir := filepath.Join(temporary, "evidence")
	command := exec.Command("bash", "hack/release-verify.sh", "--shim-version-matrix", currentBinary, artifactDir)
	command.Dir = repository
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run version-skew matrix: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "SHIM VERSION MATRIX PASS") {
		t.Fatalf("matrix output = %q, want PASS observation", output)
	}

	results := readShimMatrixFile(t, filepath.Join(artifactDir, "results.tsv"))
	wants := []string{
		"current-client\tforeign-shim\tforeign\tconnected shim hello\tprotocol-skew\tPASS",
		"current-client\tforeign-shim\tabsent\tconnected shim hello\tprotocol-skew\tPASS",
		"current-client\tmatching-shim\tmatching\tconnected shim hello\tnext-typed-gate\tPASS",
		"foreign-client\tcurrent-shim\tforeign\tclient request\tprotocol-skew\tPASS",
		"absent-client\tcurrent-shim\tabsent\tclient request\tprotocol-skew\tPASS",
		"matching-client\tcurrent-shim\tmatching\tclient request\tnext-typed-gate\tPASS",
	}
	for _, want := range wants {
		if !strings.Contains(results, want+"\n") {
			t.Errorf("results.tsv missing %q:\n%s", want, results)
		}
	}
	if got := strings.Count(strings.TrimSpace(results), "\n"); got != len(wants) {
		t.Errorf("results.tsv data row count = %d, want %d:\n%s", got, len(wants), results)
	}

	metadata := readShimMatrixFile(t, filepath.Join(artifactDir, "metadata.txt"))
	currentHash := sha256.Sum256(readShimMatrixBytes(t, currentBinary))
	fixtureBinary := filepath.Join(artifactDir, "shim-version-fixture")
	fixtureHash := sha256.Sum256(readShimMatrixBytes(t, fixtureBinary))
	for _, want := range []string{
		"current_sha256=" + fmt.Sprintf("%x", currentHash),
		"fixture_sha256=" + fmt.Sprintf("%x", fixtureHash),
		"fixture_version=foreign-protocol-v2",
		"protocol_current=1",
		"protocol_foreign=2",
		"owned_root_cleanup=PASS",
	} {
		if !strings.Contains(metadata, want+"\n") {
			t.Errorf("metadata.txt missing %q:\n%s", want, metadata)
		}
	}
	if !strings.Contains(metadata, "current_version=") {
		t.Errorf("metadata.txt missing current_version:\n%s", metadata)
	}
}

func TestShimVersionFixtureSurvivingChildFailsAfterSafeForcedCleanup(t *testing.T) {
	repository := shimMatrixRepositoryRoot(t)
	temporary := t.TempDir()
	currentBinary := filepath.Join(temporary, "agentctl-current")
	build := exec.Command("go", "build", "-o", currentBinary, "./cmd/agentctl")
	build.Dir = repository
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build current agentctl: %v\n%s", err, output)
	}
	artifactDir := filepath.Join(temporary, "evidence")
	command := exec.Command("bash", "hack/release-verify.sh", "--shim-version-matrix", currentBinary, artifactDir)
	command.Dir = repository
	command.Env = append(os.Environ(), "AGENTCTL_SHIM_VERSION_SURVIVE_STOP=1")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("surviving child matrix passed:\n%s", output)
	}
	if !strings.Contains(string(output), "required forced cleanup") || strings.Contains(string(output), "SHIM VERSION MATRIX PASS") {
		t.Fatalf("surviving child output = %q", output)
	}
	match := regexp.MustCompile(`owned child PID ([0-9]+) required forced cleanup`).FindSubmatch(output)
	if len(match) != 2 {
		t.Fatalf("surviving child output omitted owned PID: %q", output)
	}
	pid, parseErr := strconv.Atoi(string(match[1]))
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	if signalErr := syscall.Kill(pid, 0); !errors.Is(signalErr, syscall.ESRCH) {
		t.Fatalf("forced-cleanup child PID %d kill(pid, 0) = %v, want ESRCH", pid, signalErr)
	}
}

func shimMatrixRepositoryRoot(t *testing.T) string {
	t.Helper()
	command := exec.Command("git", "rev-parse", "--show-toplevel")
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(output))
}

func readShimMatrixFile(t *testing.T, path string) string {
	t.Helper()
	return string(readShimMatrixBytes(t, path))
}

func readShimMatrixBytes(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}
