package hack_test

import (
	"os"
	"strings"
	"testing"
)

// This catches an undraft route that can bypass the post-upload draft
// re-fetch and byte-exact release-note verification.
func TestReleaseWorkflowVerifiesDraftNotesBeforeEveryReachableUndraft(t *testing.T) {
	contents, err := os.ReadFile("../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(contents)
	for _, required := range []string{
		"gh release view \"v${{ steps.version.outputs.version }}\" --json body,isDraft,tagName",
		"hack/release-notes.sh inject \"${{ steps.version.outputs.version }}\"",
		"gh release edit \"v${{ steps.version.outputs.version }}\" --notes-file",
		"hack/release-notes.sh verify \"${{ steps.version.outputs.version }}\"",
	} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("release workflow omits required publication-gate command %q", required)
		}
	}

	verify := strings.Index(workflow, "hack/release-notes.sh verify \"${{ steps.version.outputs.version }}\"")
	for _, undraft := range allIndexes(workflow, "gh release edit \"v${{ steps.version.outputs.version }}\" --draft=false") {
		if verify < 0 || verify > undraft {
			t.Fatalf("undraft at byte %d is reachable before draft-note verification at byte %d", undraft, verify)
		}
	}
	if len(allIndexes(workflow, "gh release edit \"v${{ steps.version.outputs.version }}\" --draft=false")) == 0 {
		t.Fatal("release workflow has no undraft command to protect")
	}

	for _, later := range []string{"- name: Smoke-test built artifacts", "- name: Attest release artifacts"} {
		if index := strings.Index(workflow, later); index < 0 || verify > index {
			t.Fatalf("draft-note verification at byte %d must precede %q at byte %d", verify, later, index)
		}
	}
}

func allIndexes(text, needle string) []int {
	var indexes []int
	for offset := 0; ; {
		index := strings.Index(text[offset:], needle)
		if index < 0 {
			return indexes
		}
		index += offset
		indexes = append(indexes, index)
		offset = index + len(needle)
	}
}
