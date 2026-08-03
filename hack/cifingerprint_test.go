package hack_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestCIFingerprintWritesStdoutAndSummary(t *testing.T) {
	summaryPath := t.TempDir() + "/summary.md"
	command := exec.Command("./ci-fingerprint.sh")
	command.Env = append(os.Environ(), "GITHUB_STEP_SUMMARY="+summaryPath)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("ci-fingerprint failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "go version") {
		t.Fatalf("stdout does not contain go version:\n%s", output)
	}
	summary, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(summary), "## Environment fingerprint") {
		t.Fatalf("summary does not contain heading:\n%s", summary)
	}
}

func TestCIFingerprintHandlesMissingTools(t *testing.T) {
	summaryPath := t.TempDir() + "/summary.md"
	command := exec.Command("./ci-fingerprint.sh")
	command.Env = []string{
		"PATH=/usr/bin:/bin",
		"GITHUB_STEP_SUMMARY=" + summaryPath,
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("ci-fingerprint failed with missing tools: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "shellcheck: not installed") {
		t.Fatalf("stdout does not report missing shellcheck:\n%s", output)
	}
}
