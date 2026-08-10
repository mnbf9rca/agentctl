package hack_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const pinnedGovulncheckRun = "go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./..."

func runGovulncheckWorkflowCheck(t *testing.T, ci, scheduled string) (string, error) {
	t.Helper()

	dir := t.TempDir()
	workflows := map[string]string{
		"ci.yml":            ci,
		"vulnerability.yml": scheduled,
	}
	for name, body := range workflows {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.Command("./check-govulncheck-workflows.sh", dir)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func validGovulncheckCI(command string) string {
	return "jobs:\n  test:\n    steps:\n      - name: Govulncheck\n        run: " + command + "\n"
}

func validScheduledGovulncheck(command string) string {
	return "on:\n  schedule:\n    - cron: \"17 8 * * *\"\njobs:\n  govulncheck:\n    steps:\n      - name: Govulncheck\n        run: " + command + "\n"
}

func TestCheckGovulncheckWorkflowsAcceptsPinnedPRAndDailyScans(t *testing.T) {
	output, err := runGovulncheckWorkflowCheck(
		t,
		validGovulncheckCI(pinnedGovulncheckRun),
		validScheduledGovulncheck(pinnedGovulncheckRun),
	)
	if err != nil {
		t.Fatalf("valid workflows failed: %v\n%s", err, output)
	}
	if output != "" {
		t.Fatalf("output = %q, want empty", output)
	}
}

func TestCheckGovulncheckWorkflowsRejectsMissingOrUnpinnedCoverage(t *testing.T) {
	tests := []struct {
		name          string
		ci            string
		scheduled     string
		wantSubstring string
	}{
		{
			name:          "PR invocation missing",
			ci:            "jobs:\n  test:\n    steps:\n      - run: go test ./...\n",
			scheduled:     validScheduledGovulncheck(pinnedGovulncheckRun),
			wantSubstring: "ci.yml must contain exactly one pinned govulncheck invocation",
		},
		{
			name:          "latest is not a pin",
			ci:            validGovulncheckCI("go run golang.org/x/vuln/cmd/govulncheck@latest ./..."),
			scheduled:     validScheduledGovulncheck(pinnedGovulncheckRun),
			wantSubstring: "govulncheck@latest is not permitted",
		},
		{
			name:          "daily schedule missing",
			ci:            validGovulncheckCI(pinnedGovulncheckRun),
			scheduled:     strings.ReplaceAll(validScheduledGovulncheck(pinnedGovulncheckRun), "  schedule:\n    - cron: \"17 8 * * *\"\n", "  workflow_dispatch:\n"),
			wantSubstring: "vulnerability.yml must declare the daily schedule",
		},
		{
			name:          "scheduled invocation missing",
			ci:            validGovulncheckCI(pinnedGovulncheckRun),
			scheduled:     validScheduledGovulncheck("go test ./..."),
			wantSubstring: "vulnerability.yml must contain exactly one pinned govulncheck invocation",
		},
		{
			name: "PR invocation relocated outside required test job",
			ci: "jobs:\n  test:\n    steps:\n      - run: go test ./...\n" +
				"  integration:\n    steps:\n      - name: Govulncheck\n        run: " + pinnedGovulncheckRun + "\n",
			scheduled:     validScheduledGovulncheck(pinnedGovulncheckRun),
			wantSubstring: "ci.yml test job must contain exactly one pinned govulncheck invocation",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output, err := runGovulncheckWorkflowCheck(t, tc.ci, tc.scheduled)
			if err == nil {
				t.Fatalf("invalid workflows passed:\n%s", output)
			}
			if !strings.Contains(output, tc.wantSubstring) {
				t.Fatalf("output must contain %q, got %q", tc.wantSubstring, output)
			}
		})
	}
}

func TestCheckGovulncheckWorkflowsAcceptsRepositoryWorkflows(t *testing.T) {
	cmd := exec.Command("./check-govulncheck-workflows.sh")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("repository workflows failed: %v\n%s", err, out)
	}
	if len(out) != 0 {
		t.Fatalf("output = %q, want empty", out)
	}
}
