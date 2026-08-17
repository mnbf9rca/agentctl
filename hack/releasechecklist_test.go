package hack_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestReleaseChecklistKeepsMechanicalStepsInVerifier(t *testing.T) {
	contents, err := os.ReadFile("../docs/release-checklist.md")
	if err != nil {
		t.Fatal(err)
	}

	forbidden := []struct {
		name    string
		pattern string
	}{
		{"directory setup", `(?i)(^|[[:space:]])(mkdir|mktemp|chmod)([[:space:]]|$)`},
		{"install directory setup", `(?i)(^|[[:space:]])install[[:space:]]+-d([[:space:]]|$)`},
		{"filesystem setup", `(?i)(^|[[:space:]])(cp|mv|rm|ln|touch|sed|awk|printf)([[:space:]]|$)`},
		{"tmux command", `(?i)(^|[[:space:]])tmux[[:space:]]+(new|kill|set|show|list|has|send|attach|display|wait|rename|select|split)(-|[[:space:]])`},
		{"environment staging", `(?i)(AGENTCTL_(RUNTIME|STATE)_ROOT=|(^|[[:space:]])(export|source)[[:space:]]|live\.env)`},
		{"git preparation", `(?i)(^|[[:space:]])git[[:space:]]+(fetch|switch|pull|status)([[:space:]]|$)`},
		{"direct agentctl command", `(?i)(\./bin/agentctl|(^|[[:space:]])agentctl[[:space:]]+(version|launch|attach|clear|compact|relaunch|kill|status|skill)([[:space:]]|$))`},
		{"AMQ setup", `(?i)(^|[[:space:]])amq[[:space:]]+coop([[:space:]]|$)`},
		{"standalone probe", `(?i)(hack/(probe-|verify-injection)|--task8)`},
		{"standalone release gate", `(?i)(^|[[:space:]])(go[[:space:]]+(test|vet)|shellcheck|golangci-lint|goreleaser)([[:space:]]|$)`},
	}

	lines := strings.Split(string(contents), "\n")
	for _, rule := range forbidden {
		re := regexp.MustCompile(rule.pattern)
		for lineNumber, line := range lines {
			if re.MatchString(line) {
				t.Errorf("release checklist contains script-owned %s on line %d: %s", rule.name, lineNumber+1, line)
			}
		}
	}
}

// This catches a promotion checklist that records a generic pass while omitting
// one of the live, automated-guard, or release-pipeline claim sources.
func TestReleaseChecklistNamesEachPromotionClaimSource(t *testing.T) {
	contents, err := os.ReadFile("../docs/release-checklist.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Detached launch in an ordinary terminal",
		"Per-role attach, repaint, verbatim input, and clean disconnect/re-attach",
		"Exact SIGWINCH frame emission and TIOCSWINSZ application",
		"handled/ignored/blocked signal and terminal restoration",
		".github/workflows/release.yml",
		"Publish (undraft) release",
		".goreleaser.yaml",
		"archives.files",
		"LICENSES/golang.org/x/sys/LICENSE",
		"docs/release-verification-notes.md",
	} {
		if !strings.Contains(string(contents), want) {
			t.Fatalf("release checklist omits required evidence field %q", want)
		}
	}
}

func TestReleaseChecklistNamesTheCleanViewerDisconnectBoundary(t *testing.T) {
	contents, err := os.ReadFile("../docs/release-checklist.md")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.Join(strings.Fields(string(contents)), " ")
	for _, want := range []string{
		"closing the viewer's terminal window or tab, or otherwise closing its PTY at the terminal boundary",
		"typed `Ctrl-C` reaches the harness and can interrupt it",
	} {
		if !strings.Contains(normalized, want) {
			t.Fatalf("release checklist omits clean viewer disconnect guidance %q", want)
		}
	}
}
