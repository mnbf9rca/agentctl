package hack_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initFormRepo creates a temp git repo containing check-promotion-form.sh,
// next-version.sh, a VERSION file, and tags — the same fixture shape as
// initRepo in nextversion_test.go, since the form checker shells out to
// hack/next-version.sh at the repo root.
func initFormRepo(t *testing.T, version string, tags []string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	run("git", "init", "-q")
	run("git", "config", "commit.gpgsign", "false")
	run("git", "config", "tag.gpgsign", "false")
	if err := os.MkdirAll(filepath.Join(dir, "hack"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"next-version.sh", "check-promotion-form.sh"} {
		script, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "hack", name), script, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte(version+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "release-verification-notes.md"), []byte("# committed evidence\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "-A")
	run("git", "commit", "-q", "-m", "init")
	for _, tag := range tags {
		run("git", "tag", tag)
	}
	return dir
}

// runFormCheck writes body to a file inside dir and runs the form checker
// against it, returning combined stdout+stderr and the error (nil on exit 0).
func runFormCheck(t *testing.T, dir, body string) (string, error) {
	t.Helper()
	bodyFile := filepath.Join(dir, "pr-body.md")
	if err := os.WriteFile(bodyFile, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("./hack/check-promotion-form.sh", "pr-body.md")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

const templateBody = `## Release promotion: main -> release

- [%s] **Checklist run.** This release changes tmux targeting, harness startup,
  or injected command delivery. The release verification checklist was run and
  the results are recorded in ` + "`docs/release-verification-notes.md`" + ` on main.
- [%s] **Checklist not required.** No changes in checklist-covered areas since
  the last release.

When **Checklist run.** is checked, complete all four fields below.

- [%s] **Detached launch passed.** The ordinary-terminal detached-launch leg
  passed and is recorded in the evidence location below.
- [%s] **Per-role attach passed.** The attach/repaint/verbatim-input and clean
  disconnect/re-attach legs passed and are recorded below.
- [%s] **Signal and terminal restoration passed.** Every required
  handled/ignored/blocked signal and terminal-restoration leg passed and is
  recorded below.

Evidence location: %s

Version: %s
`

func body(runBox, skipBox, version string) string {
	return bodyWithEvidence(runBox, skipBox, "x", "x", "x", "docs/release-verification-notes.md", version)
}

func bodyWithEvidence(runBox, skipBox, detachedBox, attachBox, signalBox, evidence, version string) string {
	return fmt.Sprintf(templateBody, runBox, skipBox, detachedBox, attachBox, signalBox, evidence, version)
}

func TestCheckPromotionForm_NeitherBoxTicked(t *testing.T) {
	dir := initFormRepo(t, "0.1.0", nil)
	out, err := runFormCheck(t, dir, body(" ", " ", "0.1.0"))
	if err == nil {
		t.Fatalf("expected failure, got success:\n%s", out)
	}
	if !strings.Contains(out, "neither") {
		t.Errorf("expected message naming neither box ticked, got:\n%s", out)
	}
}

func TestCheckPromotionForm_BothBoxesTicked(t *testing.T) {
	dir := initFormRepo(t, "0.1.0", nil)
	out, err := runFormCheck(t, dir, body("x", "x", "0.1.0"))
	if err == nil {
		t.Fatalf("expected failure, got success:\n%s", out)
	}
	if !strings.Contains(out, "both") {
		t.Errorf("expected message naming both boxes ticked, got:\n%s", out)
	}
}

func TestCheckPromotionForm_MissingVersionLine(t *testing.T) {
	dir := initFormRepo(t, "0.1.0", nil)
	noVersion := strings.Split(body("x", " ", "0.1.0"), "\nVersion:")[0] + "\n"
	out, err := runFormCheck(t, dir, noVersion)
	if err == nil {
		t.Fatalf("expected failure, got success:\n%s", out)
	}
	if !strings.Contains(out, "Version:") {
		t.Errorf("expected message naming missing Version: line, got:\n%s", out)
	}
}

func TestCheckPromotionForm_HTMLCommentVersion(t *testing.T) {
	dir := initFormRepo(t, "0.1.0", nil)
	out, err := runFormCheck(t, dir, body("x", " ", "<!-- output of hack/next-version.sh, e.g. 0.1.2 -->"))
	if err == nil {
		t.Fatalf("expected failure, got success:\n%s", out)
	}
	if !strings.Contains(out, "Version:") {
		t.Errorf("expected message naming Version: violation, got:\n%s", out)
	}
}

func TestCheckPromotionForm_VersionMismatch(t *testing.T) {
	dir := initFormRepo(t, "0.1.0", nil)
	out, err := runFormCheck(t, dir, body("x", " ", "9.9.9"))
	if err == nil {
		t.Fatalf("expected failure, got success:\n%s", out)
	}
	if !strings.Contains(out, "9.9.9") || !strings.Contains(out, "0.1.0") {
		t.Errorf("expected message naming both mismatched values, got:\n%s", out)
	}
}

func TestCheckPromotionForm_NotBareSemver(t *testing.T) {
	dir := initFormRepo(t, "0.1.0", nil)
	out, err := runFormCheck(t, dir, body("x", " ", "v0.1.0"))
	if err == nil {
		t.Fatalf("expected failure, got success:\n%s", out)
	}
	if !strings.Contains(out, "Version:") {
		t.Errorf("expected message naming Version: violation, got:\n%s", out)
	}
}

func TestCheckPromotionForm_MissingCheckboxLines(t *testing.T) {
	dir := initFormRepo(t, "0.1.0", nil)
	out, err := runFormCheck(t, dir, "## Release promotion\n\nVersion: 0.1.0\n")
	if err == nil {
		t.Fatalf("expected failure, got success:\n%s", out)
	}
	if !strings.Contains(out, "checkbox") {
		t.Errorf("expected message naming missing checkboxes, got:\n%s", out)
	}
}

func TestCheckPromotionForm_PassesWithOneBoxAndMatchingVersion(t *testing.T) {
	dir := initFormRepo(t, "0.1.0", nil)
	out, err := runFormCheck(t, dir, bodyWithEvidence("x", " ", "x", "x", "x", "docs/release-verification-notes.md", "0.1.0"))
	if err != nil {
		t.Fatalf("expected success, got failure: %v\n%s", err, out)
	}
	out2, err2 := runFormCheck(t, dir, body(" ", "x", "0.1.0"))
	if err2 != nil {
		t.Fatalf("expected success with the other box ticked, got failure: %v\n%s", err2, out2)
	}
}

// This catches a checklist-required promotion that claims the generic
// ceremony ran but omits the committed evidence location or any required
// detached/attach/signal affirmation.
func TestCheckPromotionForm_ChecklistRunRequiresTmuxlessEvidenceAffirmations(t *testing.T) {
	dir := initFormRepo(t, "0.1.0", nil)
	out, err := runFormCheck(t, dir, bodyWithEvidence("x", " ", " ", " ", " ", "", "0.1.0"))
	if err == nil {
		t.Fatalf("checklist-required promotion without tmuxless evidence passed:\n%s", out)
	}
	if !strings.Contains(out, "Evidence location") {
		t.Fatalf("missing evidence location failure = %q", out)
	}
}

func TestCheckPromotionForm_ChecklistRunRequiresEveryTmuxlessLeg(t *testing.T) {
	dir := initFormRepo(t, "0.1.0", nil)
	for _, test := range []struct {
		name     string
		detached string
		attach   string
		signal   string
		want     string
	}{
		{"detached", " ", "x", "x", "Detached launch passed"},
		{"attach", "x", " ", "x", "Per-role attach passed"},
		{"signal", "x", "x", " ", "Signal and terminal restoration passed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			out, err := runFormCheck(t, dir, bodyWithEvidence("x", " ", test.detached, test.attach, test.signal, "docs/release-verification-notes.md", "0.1.0"))
			if err == nil || !strings.Contains(out, test.want) {
				t.Fatalf("missing %s affirmation err=%v output=%q", test.name, err, out)
			}
		})
	}
}

func TestCheckPromotionForm_ChecklistRunRequiresCommittedCleanRelativeEvidence(t *testing.T) {
	dir := initFormRepo(t, "0.1.0", nil)
	if err := os.WriteFile(filepath.Join(dir, "docs", "untracked.md"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		evidence string
		want     string
	}{
		{"invented", "docs/invented.md", "not a tracked evidence location"},
		{"absolute", "/tmp/evidence.md", "repository-relative"},
		{"traversal", "docs/../outside.md", "repository-relative"},
		{"untracked", "docs/untracked.md", "not a tracked evidence location"},
	} {
		t.Run(test.name, func(t *testing.T) {
			out, err := runFormCheck(t, dir, bodyWithEvidence("x", " ", "x", "x", "x", test.evidence, "0.1.0"))
			if err == nil || !strings.Contains(out, test.want) {
				t.Fatalf("evidence %q err=%v output=%q", test.evidence, err, out)
			}
		})
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "staged.md"), []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", "docs/staged.md")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add staged evidence: %v\n%s", err, out)
	}
	out, err := runFormCheck(t, dir, bodyWithEvidence("x", " ", "x", "x", "x", "docs/staged.md", "0.1.0"))
	if err == nil || !strings.Contains(out, "not committed at HEAD") {
		t.Fatalf("staged evidence err=%v output=%q", err, out)
	}
}

func TestCheckPromotionForm_PassesWithCRLFBody(t *testing.T) {
	dir := initFormRepo(t, "0.1.0", nil)
	crlfBody := strings.ReplaceAll(body("x", " ", "0.1.0"), "\n", "\r\n")
	out, err := runFormCheck(t, dir, crlfBody)
	if err != nil {
		t.Fatalf("expected CRLF body to pass, got failure: %v\n%s", err, out)
	}
}

func TestCheckPromotionForm_FailureNamesFormOnly(t *testing.T) {
	dir := initFormRepo(t, "0.1.0", nil)
	out, err := runFormCheck(t, dir, body(" ", " ", "0.1.0"))
	if err == nil {
		t.Fatalf("expected failure, got success:\n%s", out)
	}
	if !strings.Contains(out, "FORM") {
		t.Errorf("expected failure text to state it checks FORM only, got:\n%s", out)
	}
}
