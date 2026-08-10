package hack_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const validDependabotConfig = `version: 2
updates:
  - package-ecosystem: gomod
    directory: /
    schedule:
      interval: monthly
    open-pull-requests-limit: 1
    groups:
      gomod-dependencies:
        patterns:
          - "*"
  - package-ecosystem: github-actions
    directory: /
    schedule:
      interval: monthly
    open-pull-requests-limit: 1
    groups:
      github-actions-dependencies:
        patterns:
          - "*"
`

func runDependabotConfigCheck(t *testing.T, body string) (string, error) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "dependabot.yml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("./check-dependabot-config.sh", path)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestCheckDependabotConfigAcceptsMonthlyVersionGroups(t *testing.T) {
	output, err := runDependabotConfigCheck(t, validDependabotConfig)
	if err != nil {
		t.Fatalf("valid config failed: %v\n%s", err, output)
	}
	if output != "" {
		t.Fatalf("output = %q, want empty", output)
	}
}

func TestCheckDependabotConfigRejectsPolicyDrift(t *testing.T) {
	firstGroup := `    groups:
      gomod-dependencies:
        patterns:
          - "*"
`
	tests := []struct {
		name          string
		body          string
		wantSubstring string
	}{
		{
			name:          "weekly routine updates",
			body:          strings.Replace(validDependabotConfig, "interval: monthly", "interval: weekly", 1),
			wantSubstring: "gomod: schedule interval must be monthly",
		},
		{
			name:          "routine updates are not grouped",
			body:          strings.Replace(validDependabotConfig, firstGroup, "", 1),
			wantSubstring: "gomod: exactly one dependency group is required",
		},
		{
			name: "security updates are deferred into the routine group",
			body: strings.Replace(
				validDependabotConfig,
				"      gomod-dependencies:\n        patterns:",
				"      gomod-dependencies:\n        applies-to: security-updates\n        patterns:",
				1,
			),
			wantSubstring: "gomod: dependency group must apply only to version updates",
		},
		{
			name:          "multiple routine update PRs are allowed",
			body:          strings.Replace(validDependabotConfig, "open-pull-requests-limit: 1", "open-pull-requests-limit: 2", 1),
			wantSubstring: "gomod: open-pull-requests-limit must be 1",
		},
		{
			name:          "supported ecosystem is missing",
			body:          strings.Replace(validDependabotConfig, "package-ecosystem: github-actions", "package-ecosystem: npm", 1),
			wantSubstring: "github-actions: exactly one root update entry is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output, err := runDependabotConfigCheck(t, tc.body)
			if err == nil {
				t.Fatalf("invalid config passed:\n%s", output)
			}
			if !strings.Contains(output, tc.wantSubstring) {
				t.Fatalf("output must contain %q, got %q", tc.wantSubstring, output)
			}
		})
	}
}

func TestCheckDependabotConfigAcceptsRepositoryConfig(t *testing.T) {
	cmd := exec.Command("./check-dependabot-config.sh")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("repository config failed: %v\n%s", err, out)
	}
	if len(out) != 0 {
		t.Fatalf("output = %q, want empty", out)
	}
}
