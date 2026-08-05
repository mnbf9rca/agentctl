package hack_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runSkillVersionCheck(t *testing.T, skillBody, releaseVersion string) (string, error) {
	t.Helper()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "hack"), 0o755); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("check-skill-version.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hack", "check-skill-version.sh"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(skillBody), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("./hack/check-skill-version.sh", releaseVersion, skillPath)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err = cmd.Run()
	return stderr.String(), err
}

func TestCheckSkillVersionAcceptsMatchingMetadata(t *testing.T) {
	stderr, err := runSkillVersionCheck(t, "---\nmetadata:\n  version: \"0.3.0\"\n---\n", "0.3.0")
	if err != nil {
		t.Fatalf("matching version failed: %v\n%s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func TestCheckSkillVersionRejectsMismatch(t *testing.T) {
	stderr, err := runSkillVersionCheck(t, "---\nmetadata:\n  version: \"0.2.0\"\n---\n", "0.3.0")
	if err == nil {
		t.Fatal("expected mismatched versions to fail")
	}
	if !strings.Contains(stderr, "0.2.0") || !strings.Contains(stderr, "0.3.0") {
		t.Fatalf("stderr must name skill and release versions, got %q", stderr)
	}
}

func TestCheckSkillVersionReadsVersionOnlyFromMetadata(t *testing.T) {
	skill := "---\nversion: \"0.3.0\"\nmetadata:\n  version: \"0.2.0\"\n---\n"
	stderr, err := runSkillVersionCheck(t, skill, "0.3.0")
	if err == nil {
		t.Fatal("expected metadata.version mismatch to fail despite another matching version key")
	}
	if !strings.Contains(stderr, "0.2.0") || !strings.Contains(stderr, "0.3.0") {
		t.Fatalf("stderr must name metadata and release versions, got %q", stderr)
	}
}

func TestCheckSkillVersionRejectsMissingMetadataVersion(t *testing.T) {
	stderr, err := runSkillVersionCheck(t, "---\nmetadata:\n  owner: agentctl\n---\n", "0.3.0")
	if err == nil {
		t.Fatal("expected missing metadata.version to fail")
	}
	if !strings.Contains(stderr, "no metadata.version") {
		t.Fatalf("stderr must identify the missing metadata.version, got %q", stderr)
	}
}
