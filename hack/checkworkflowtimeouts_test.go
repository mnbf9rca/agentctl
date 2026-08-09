package hack_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runWorkflowTimeoutCheck(t *testing.T, workflows map[string]string) (string, error) {
	t.Helper()

	dir := t.TempDir()
	for name, body := range workflows {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.Command("./check-workflow-timeouts.sh", dir)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestCheckWorkflowTimeoutsAcceptsJobLevelTimeouts(t *testing.T) {
	workflows := map[string]string{
		"ci.yml": `jobs:
  test:
    timeout-minutes: 15
    runs-on: macos-26
    steps:
      - run: go test ./...
`,
		"future.yaml": `jobs:
  analyze:
    runs-on: ubuntu-latest
    timeout-minutes: ${{ 15 }}
    steps:
      - run: go test ./...
`,
	}

	output, err := runWorkflowTimeoutCheck(t, workflows)
	if err != nil {
		t.Fatalf("valid workflows failed: %v\n%s", err, output)
	}
	if output != "" {
		t.Fatalf("output = %q, want empty", output)
	}
}

func TestCheckWorkflowTimeoutsRejectsMissingJobLevelTimeout(t *testing.T) {
	tests := []struct {
		name       string
		filename   string
		body       string
		wantOutput string
	}{
		{
			name:     "missing key",
			filename: "ci.yml",
			body: `jobs:
  test:
    runs-on: macos-26
    steps:
      - run: go test ./...
`,
			wantOutput: "ci.yml:test: missing job-level timeout-minutes",
		},
		{
			name:     "step-level key does not satisfy job",
			filename: "ci.yml",
			body: `jobs:
  test:
    runs-on: macos-26
    steps:
      - run: go test ./...
        timeout-minutes: 5
`,
			wantOutput: "ci.yml:test: missing job-level timeout-minutes",
		},
		{
			name:     "commented key does not satisfy job",
			filename: "ci.yml",
			body: `jobs:
  test:
    runs-on: macos-26
    # timeout-minutes: 15
    steps:
      - run: go test ./...
`,
			wantOutput: "ci.yml:test: missing job-level timeout-minutes",
		},
		{
			name:     "new yaml workflow is discovered",
			filename: "previously-unknown.yaml",
			body: `jobs:
  new-job:
    runs-on: ubuntu-latest
    steps:
      - run: true
`,
			wantOutput: "previously-unknown.yaml:new-job: missing job-level timeout-minutes",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output, err := runWorkflowTimeoutCheck(t, map[string]string{tc.filename: tc.body})
			if err == nil {
				t.Fatalf("expected missing job-level timeout to fail, got success:\n%s", output)
			}
			exitErr, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("workflow timeout check returned non-exit error: %v", err)
			}
			if exitErr.ExitCode() != 1 {
				t.Fatalf("exit = %d, want 1\n%s", exitErr.ExitCode(), output)
			}
			if !strings.Contains(output, tc.wantOutput) {
				t.Fatalf("output must contain %q, got %q", tc.wantOutput, output)
			}
		})
	}
}

func TestCheckWorkflowTimeoutsFailsWhenNoWorkflowsAreDiscovered(t *testing.T) {
	output, err := runWorkflowTimeoutCheck(t, nil)
	if err == nil {
		t.Fatalf("expected empty workflow directory to fail, got success:\n%s", output)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("workflow timeout check returned non-exit error: %v", err)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("exit = %d, want 1\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(output, "no workflow files found") {
		t.Fatalf("output must identify failed workflow discovery, got %q", output)
	}
}

func TestCheckWorkflowTimeoutsAcceptsRepositoryWorkflows(t *testing.T) {
	cmd := exec.Command("./check-workflow-timeouts.sh")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("repository workflows failed: %v\n%s", err, out)
	}
	if len(out) != 0 {
		t.Fatalf("output = %q, want empty", out)
	}
}
