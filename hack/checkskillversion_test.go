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

func TestCheckSkillVersionRejectsDuplicateMetadataVersion(t *testing.T) {
	skill := "---\nmetadata:\n  version: \"0.3.0\"\n  version: \"0.2.0\"\n---\n"
	stderr, err := runSkillVersionCheck(t, skill, "0.3.0")
	if err == nil {
		t.Fatal("expected duplicate metadata.version values to fail")
	}
	if !strings.Contains(stderr, "multiple metadata.version") {
		t.Fatalf("stderr must identify duplicate metadata.version values, got %q", stderr)
	}
}

func TestCheckSkillVersionRejectsUnclosedFrontmatter(t *testing.T) {
	skill := "---\nmetadata:\n  version: \"0.3.0\"\n"
	stderr, err := runSkillVersionCheck(t, skill, "0.3.0")
	if err == nil {
		t.Fatal("expected unclosed frontmatter to fail")
	}
	if !strings.Contains(stderr, "frontmatter is not closed") {
		t.Fatalf("stderr must identify unclosed frontmatter, got %q", stderr)
	}
}

func TestCheckSkillVersionIgnoresNestedMetadata(t *testing.T) {
	skill := "---\nouter:\n  metadata:\n    version: \"0.3.0\"\nmetadata:\n  version: \"0.2.0\"\n---\n"
	stderr, err := runSkillVersionCheck(t, skill, "0.3.0")
	if err == nil {
		t.Fatal("expected direct metadata.version mismatch to fail")
	}
	if !strings.Contains(stderr, "unsupported nesting") {
		t.Fatalf("stderr must reject nested metadata, got %q", stderr)
	}
}

func TestCheckSkillVersionIgnoresNestedVersion(t *testing.T) {
	skill := "---\nmetadata:\n  nested:\n    version: \"0.3.0\"\n  version: \"0.2.0\"\n---\n"
	stderr, err := runSkillVersionCheck(t, skill, "0.3.0")
	if err == nil {
		t.Fatal("expected direct metadata.version mismatch to fail")
	}
	if !strings.Contains(stderr, "unsupported nesting") {
		t.Fatalf("stderr must reject nested version mappings, got %q", stderr)
	}
}

func TestCheckSkillVersionRejectsMissingMappingSeparator(t *testing.T) {
	skill := "---\nmetadata:\n  version:\"0.3.0\"\n---\n"
	stderr, err := runSkillVersionCheck(t, skill, "0.3.0")
	if err == nil {
		t.Fatal("expected version without a mapping separator to fail")
	}
	if !strings.Contains(stderr, "is not a mapping") {
		t.Fatalf("stderr must identify the malformed mapping, got %q", stderr)
	}
}

func TestCheckSkillVersionRejectsInlineMetadataOverride(t *testing.T) {
	skill := "---\nmetadata:\n  version: \"0.3.0\"\nmetadata: { version: \"0.2.0\" }\n---\n"
	stderr, err := runSkillVersionCheck(t, skill, "0.3.0")
	if err == nil {
		t.Fatal("expected inline metadata override to fail")
	}
	if !strings.Contains(stderr, "metadata") {
		t.Fatalf("stderr must identify the invalid metadata mapping, got %q", stderr)
	}
}

func TestCheckSkillVersionRejectsMalformedMetadataContent(t *testing.T) {
	skill := "---\nmetadata:\n  version: \"0.3.0\"\n  malformed\n---\n"
	stderr, err := runSkillVersionCheck(t, skill, "0.3.0")
	if err == nil {
		t.Fatal("expected malformed metadata content to fail")
	}
	if !strings.Contains(stderr, "is not a mapping") {
		t.Fatalf("stderr must identify the malformed mapping, got %q", stderr)
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
