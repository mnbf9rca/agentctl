package hack_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests catch a release being published with an omitted, duplicated, or
// edited 0.5.0 obligation block. They exercise the release-note script against
// GitHub-release-shaped JSON instead of inspecting its implementation.
func TestReleaseNotesInjectIsIdempotentAndPreservesTheVersionedSourceBlock(t *testing.T) {
	source := releaseNoteSource(t)
	first := runReleaseNotes(t, "inject", "0.5.0", releaseJSON(t, "", true, "v0.5.0"))
	want := source
	if first != want {
		t.Fatalf("first injection = %q, want %q", first, want)
	}

	second := runReleaseNotes(t, "inject", "0.5.0", releaseJSON(t, first, true, "v0.5.0"))
	if second != want {
		t.Fatalf("idempotent injection = %q, want exactly one source block %q", second, want)
	}
}

func TestReleaseNotesVerifyAcceptsExactlyOneUnalteredDraftBlock(t *testing.T) {
	source := releaseNoteSource(t)
	out := runReleaseNotes(t, "verify", "0.5.0", releaseJSON(t, source, true, "v0.5.0"))
	if out != "release notes verified for v0.5.0\n" {
		t.Fatalf("verify output = %q", out)
	}
}

func TestReleaseNotesObligationsMatchApprovedSpec(t *testing.T) {
	spec, err := os.ReadFile(filepath.Join("..", "docs", "superpowers", "specs", "2026-08-01-agentctl-design.md"))
	if err != nil {
		t.Fatal(err)
	}

	want := numberedObligationsAfterHeading(t, string(spec), "#### 15.11.11 Release obligations")
	got := numberedObligationsAfterHeading(t, releaseNoteSource(t), "## Upgrade notes")
	if got != want {
		t.Fatalf("release-note obligations drifted from approved spec:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// This catches line-oriented extraction silently restoring a final newline
// that was absent from the uploaded release body.
func TestReleaseNotesVerifyRejectsBlockMissingOnlyItsFinalNewline(t *testing.T) {
	source := releaseNoteSource(t)
	withoutFinalNewline := strings.TrimSuffix(source, "\n")
	if withoutFinalNewline == source {
		t.Fatal("release-note source unexpectedly lacks its final newline")
	}
	output, err := runReleaseNotesResult(t, "verify", "0.5.0", releaseJSON(t, withoutFinalNewline, true, "v0.5.0"))
	if err == nil || !strings.Contains(output, "was altered") {
		t.Fatalf("missing-final-newline verify err=%v output=%q", err, output)
	}
}

func TestReleaseNotesRefusesPublicationUnsafeReleaseFacts(t *testing.T) {
	source := releaseNoteSource(t)
	cases := []struct {
		name    string
		mode    string
		payload string
		want    string
	}{
		{"missing block", "verify", releaseJSON(t, "release body", true, "v0.5.0"), "missing obligation block"},
		{"duplicate block", "verify", releaseJSON(t, source+"\n"+source, true, "v0.5.0"), "must appear exactly once"},
		{"altered block", "verify", releaseJSON(t, strings.Replace(source, "detached", "tmux", 1), true, "v0.5.0"), "was altered"},
		{"wrong tag", "verify", releaseJSON(t, source, true, "v0.5.1"), "tagName"},
		{"malformed JSON", "verify", `{"body":`, "invalid GitHub release JSON"},
		{"published release", "verify", releaseJSON(t, source, false, "v0.5.0"), "isDraft=false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			output, err := runReleaseNotesResult(t, tc.mode, "0.5.0", tc.payload)
			if err == nil {
				t.Fatalf("%s unexpectedly succeeded: %s", tc.name, output)
			}
			if !strings.Contains(output, tc.want) {
				t.Fatalf("%s output = %q, want %q", tc.name, output, tc.want)
			}
		})
	}
}

func releaseNoteSource(t *testing.T) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "docs", "releases", "0.5.0.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func numberedObligationsAfterHeading(t *testing.T, contents, heading string) string {
	t.Helper()
	headingStart := strings.Index(contents, heading+"\n")
	if headingStart < 0 {
		t.Fatalf("missing heading %q", heading)
	}
	afterHeading := contents[headingStart+len(heading)+1:]
	listStart := strings.Index(afterHeading, "1. ")
	if listStart < 0 {
		t.Fatalf("heading %q has no numbered obligations", heading)
	}
	obligations := afterHeading[listStart:]
	listEnd := strings.Index(obligations, "\n\n")
	if listEnd < 0 {
		t.Fatalf("heading %q has no obligation-list boundary", heading)
	}
	return obligations[:listEnd]
}

func releaseJSON(t *testing.T, body string, draft bool, tag string) string {
	t.Helper()
	encoded, err := json.Marshal(struct {
		Body    string `json:"body"`
		IsDraft bool   `json:"isDraft"`
		TagName string `json:"tagName"`
	}{body, draft, tag})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func runReleaseNotes(t *testing.T, mode, version, payload string) string {
	t.Helper()
	output, err := runReleaseNotesResult(t, mode, version, payload)
	if err != nil {
		t.Fatalf("release notes %s: %v\n%s", mode, err, output)
	}
	return output
}

func runReleaseNotesResult(t *testing.T, mode, version, payload string) (string, error) {
	t.Helper()
	jsonPath := filepath.Join(t.TempDir(), "release.json")
	if err := os.WriteFile(jsonPath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("./release-notes.sh", mode, version, jsonPath)
	command.Dir = "."
	output, err := command.CombinedOutput()
	return string(output), err
}
