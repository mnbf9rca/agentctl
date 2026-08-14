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
