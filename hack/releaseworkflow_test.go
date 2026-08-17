package hack_test

import (
	"fmt"
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
	if err := validateReleaseWorkflow(string(contents)); err != nil {
		t.Fatal(err)
	}
}

// This catches a publication command hidden behind a shell environment prefix:
// it must not evade the draft-note verification ordering gate.
func TestReleaseWorkflowRejectsEnvironmentPrefixedReleaseUndraft(t *testing.T) {
	contents, err := os.ReadFile("../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	mutated := strings.Replace(string(contents), releaseViewBefore,
		`GH_TOKEN="$GH_TOKEN" gh release edit "v${{ steps.version.outputs.version }}" --draft=false`+"\n          "+releaseViewBefore, 1)
	if err := validateReleaseWorkflow(mutated); err == nil || !strings.Contains(err.Error(), "unknown release publication command") {
		t.Fatal("environment-prefixed early gh release undraft passed the workflow guard")
	}
}

// This catches a draft=false publication routed through gh api rather than the
// normal gh release spelling; non-publication GraphQL formula calls remain valid.
func TestReleaseWorkflowRejectsEnvironmentPrefixedAPIDraftMutation(t *testing.T) {
	contents, err := os.ReadFile("../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	mutated := strings.Replace(string(contents), releaseViewBefore,
		`GH_TOKEN="$GH_TOKEN" gh api repos/mnbf9rca/agentctl/releases/example -X PATCH -f draft=false`+"\n          "+releaseViewBefore, 1)
	if err := validateReleaseWorkflow(mutated); err == nil || !strings.Contains(err.Error(), "unknown API publication command") {
		t.Fatal("environment-prefixed early gh api draft mutation passed the workflow guard")
	}
}

func validateReleaseWorkflow(workflow string) error {
	lines := workflowRelevantLines(workflow)
	if got, want := countLine(lines, releaseViewBefore), 1; got != want {
		return fmt.Errorf("draft release-before view calls = %d, want %d:\n%s", got, want, strings.Join(lines, "\n"))
	}
	if got, want := countLine(lines, releaseViewAfter), 1; got != want {
		return fmt.Errorf("draft release-after view calls = %d, want %d:\n%s", got, want, strings.Join(lines, "\n"))
	}
	if got, want := countPrefix(lines, `gh release view "v${{ steps.version.outputs.version }}"`), 2; got != want {
		return fmt.Errorf("draft gh release view calls = %d, want %d:\n%s", got, want, strings.Join(lines, "\n"))
	}

	before := lineIndex(lines, releaseViewBefore)
	inject := lineIndex(lines, releaseInject)
	edit := lineIndex(lines, releaseEdit)
	after := lineIndex(lines, releaseViewAfter)
	verify := lineIndex(lines, releaseVerify)
	if before < 0 || inject < 0 || edit < 0 || after < 0 || verify < 0 {
		return fmt.Errorf("draft note gate omits a required command; relevant lines:\n%s", strings.Join(lines, "\n"))
	}
	if before >= inject || inject >= edit || edit >= after || after >= verify {
		return fmt.Errorf("draft note gate must be view-before -> inject -> edit -> view-after -> verify; relevant lines:\n%s", strings.Join(lines, "\n"))
	}

	undrafts := allLineIndexes(lines, releaseUndraft)
	if len(undrafts) != 1 {
		return fmt.Errorf("release workflow undraft commands = %d, want exactly one allowed undraft:\n%s", len(undrafts), strings.Join(lines, "\n"))
	}
	if verify > undrafts[0] {
		return fmt.Errorf("undraft at relevant line %d is reachable before draft-note verification at line %d", undrafts[0], verify)
	}
	for _, line := range lines {
		if strings.Contains(line, "gh release ") && line != releaseViewBefore && line != releaseInject && line != releaseEdit && line != releaseViewAfter && line != releaseVerify && line != releaseUndraft {
			return fmt.Errorf("release workflow has an unknown release publication command %q", line)
		}
		if strings.Contains(line, "gh api ") && strings.Contains(line, "draft=false") {
			return fmt.Errorf("release workflow has an unknown API publication command %q", line)
		}
		if strings.Contains(line, "draft=false") && line != releaseUndraft {
			return fmt.Errorf("release workflow has an unknown release publication command %q", line)
		}
	}
	for _, later := range []string{"- name: Smoke-test built artifacts", "- name: Attest release artifacts", "- name: Attest checksums file"} {
		if index := lineIndex(lines, later); index < 0 || verify > index {
			return fmt.Errorf("draft-note verification at relevant line %d must precede %q at line %d", verify, later, index)
		}
	}
	return nil
}

func workflowRelevantLines(workflow string) []string {
	var relevant []string
	for _, raw := range strings.Split(workflow, "\n") {
		line := strings.TrimSpace(raw)
		line = strings.TrimPrefix(line, "run: ")
		if strings.Contains(line, "gh release ") || strings.Contains(line, "gh api ") || strings.Contains(line, "draft=false") || strings.HasPrefix(line, "hack/release-notes.sh ") || strings.HasPrefix(line, "- name: Smoke-test") || strings.HasPrefix(line, "- name: Attest") {
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
