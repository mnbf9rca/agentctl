package hack_test

import (
	"os"
	"strings"
	"testing"
)

const (
	releaseViewBefore = `gh release view "v${{ steps.version.outputs.version }}" --json body,isDraft,tagName > "$release_before"`
	releaseInject     = `hack/release-notes.sh inject "${{ steps.version.outputs.version }}" "$release_before" > "$release_notes"`
	releaseEdit       = `gh release edit "v${{ steps.version.outputs.version }}" --notes-file "$release_notes"`
	releaseViewAfter  = `gh release view "v${{ steps.version.outputs.version }}" --json body,isDraft,tagName > "$release_after"`
	releaseVerify     = `hack/release-notes.sh verify "${{ steps.version.outputs.version }}" "$release_after"`
	releaseUndraft    = `gh release edit "v${{ steps.version.outputs.version }}" --draft=false`
)

// This catches a draft publication path that does not re-fetch the uploaded
// body and byte-verify it before later release gates or every undraft route.
func TestReleaseWorkflowVerifiesDraftNotesBeforeEveryReachableUndraft(t *testing.T) {
	contents, err := os.ReadFile("../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	lines := workflowRelevantLines(string(contents))
	if got, want := countLine(lines, releaseViewBefore), 1; got != want {
		t.Fatalf("draft release-before view calls = %d, want %d:\n%s", got, want, strings.Join(lines, "\n"))
	}
	if got, want := countLine(lines, releaseViewAfter), 1; got != want {
		t.Fatalf("draft release-after view calls = %d, want %d:\n%s", got, want, strings.Join(lines, "\n"))
	}
	if got, want := countPrefix(lines, `gh release view "v${{ steps.version.outputs.version }}"`), 2; got != want {
		t.Fatalf("draft gh release view calls = %d, want %d:\n%s", got, want, strings.Join(lines, "\n"))
	}

	before := lineIndex(lines, releaseViewBefore)
	inject := lineIndex(lines, releaseInject)
	edit := lineIndex(lines, releaseEdit)
	after := lineIndex(lines, releaseViewAfter)
	verify := lineIndex(lines, releaseVerify)
	if before < 0 || inject < 0 || edit < 0 || after < 0 || verify < 0 {
		t.Fatalf("draft note gate omits a required command; relevant lines:\n%s", strings.Join(lines, "\n"))
	}
	if before >= inject || inject >= edit || edit >= after || after >= verify {
		t.Fatalf("draft note gate must be view-before -> inject -> edit -> view-after -> verify; relevant lines:\n%s", strings.Join(lines, "\n"))
	}

	undrafts := allLineIndexes(lines, releaseUndraft)
	if len(undrafts) == 0 {
		t.Fatal("release workflow has no undraft command to protect")
	}
	for _, undraft := range undrafts {
		if verify > undraft {
			t.Fatalf("undraft at relevant line %d is reachable before draft-note verification at line %d", undraft, verify)
		}
	}
	for _, later := range []string{"- name: Smoke-test built artifacts", "- name: Attest release artifacts", "- name: Attest checksums file"} {
		if index := lineIndex(lines, later); index < 0 || verify > index {
			t.Fatalf("draft-note verification at relevant line %d must precede %q at line %d", verify, later, index)
		}
	}
}

func workflowRelevantLines(workflow string) []string {
	var relevant []string
	for _, raw := range strings.Split(workflow, "\n") {
		line := strings.TrimSpace(raw)
		line = strings.TrimPrefix(line, "run: ")
		if strings.HasPrefix(line, "gh release ") || strings.HasPrefix(line, "hack/release-notes.sh ") || strings.HasPrefix(line, "- name: Smoke-test") || strings.HasPrefix(line, "- name: Attest") {
			relevant = append(relevant, line)
		}
	}
	return relevant
}

func countLine(lines []string, want string) int {
	count := 0
	for _, line := range lines {
		if line == want {
			count++
		}
	}
	return count
}

func countPrefix(lines []string, prefix string) int {
	count := 0
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			count++
		}
	}
	return count
}

func lineIndex(lines []string, want string) int {
	indexes := allLineIndexes(lines, want)
	if len(indexes) != 1 {
		return -1
	}
	return indexes[0]
}

func allLineIndexes(lines []string, want string) []int {
	var indexes []int
	for index, line := range lines {
		if line == want {
			indexes = append(indexes, index)
		}
	}
	return indexes
}
