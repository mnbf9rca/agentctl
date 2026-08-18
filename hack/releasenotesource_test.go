package hack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCurrentVersionHasMatchingReleaseNoteObligations(t *testing.T) {
	versionBytes, err := os.ReadFile(filepath.Join("..", "VERSION"))
	if err != nil {
		t.Fatalf("read VERSION: %v", err)
	}
	version := strings.TrimSpace(string(versionBytes))
	notesPath := filepath.Join("..", "docs", "releases", version+".md")
	notesBytes, err := os.ReadFile(notesPath)
	if err != nil {
		t.Fatalf("read release notes for VERSION %q at %s: %v", version, notesPath, err)
	}

	startMarker := "<!-- agentctl-release-obligations:" + version + " -->"
	endMarker := "<!-- /agentctl-release-obligations:" + version + " -->"
	counts := map[string]int{}
	for _, line := range strings.Split(string(notesBytes), "\n") {
		counts[line]++
	}
	for _, marker := range []string{startMarker, endMarker} {
		if counts[marker] != 1 {
			t.Errorf("release notes %s contain marker %q %d times; want exactly once", notesPath, marker, counts[marker])
		}
	}
}
