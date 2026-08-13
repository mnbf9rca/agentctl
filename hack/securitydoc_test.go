package hack_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// SECURITY.md is an operator document. It states current truth; provenance
// lives in git history and PR gates. These guards catch the two ways it has
// regrown before: process residue accreting during a batch, and steady
// growth in how much there is to read.
//
// The budget is words, not lines. Line count tracks wrapping rather than
// reading burden: this document's earliest form held ~1180 words for five
// threats and five residuals on very long lines, and a later form reached 2103
// words largely by restating behavior the design specification already owned.
//
// The ceiling is set against item density, not against whatever the file
// happens to weigh. The document currently carries roughly twice the original's
// threats and residuals in a comparable number of words. Headroom is deliberate
// and small: enough for a new threat row and its residual, not enough to absorb
// a section of restated mechanics. If a change needs more, the content probably
// belongs in the specification instead.
const securityDocWordBudget = 1650

func TestSecurityDocStaysWithinReadingBudget(t *testing.T) {
	contents, err := os.ReadFile("../SECURITY.md")
	if err != nil {
		t.Fatal(err)
	}
	if words := len(strings.Fields(string(contents))); words > securityDocWordBudget {
		t.Errorf("SECURITY.md is %d words, over the %d-word operator budget; compress or move mechanism detail to the design specification",
			words, securityDocWordBudget)
	}
}

func TestSecurityDocStatesCurrentTruthOnly(t *testing.T) {
	contents, err := os.ReadFile("../SECURITY.md")
	if err != nil {
		t.Fatal(err)
	}

	forbidden := []struct {
		name    string
		pattern string
	}{
		{"per-PR bookkeeping", `(?i)(^|[[:space:]])(pr[[:space:]]*#?[0-9]+|carrying pr|owning pr)([[:space:]]|$|\.|,)`},
		{"transition scaffolding", `(?i)(cuts? over|cutover|staged claim|transitional|amendment table|supersed)`},
		{"unshipped or deferred behavior", `(?i)(not yet|will be|once [a-z]+ lands|deferred to|in a later pr|design phase)`},
		{"work-tracking residue", `(?i)(^|[[:space:]])(TODO|TBD|FIXME|XXX)([[:space:]]|$|:)`},
	}

	lines := strings.Split(string(contents), "\n")
	for _, rule := range forbidden {
		re := regexp.MustCompile(rule.pattern)
		for number, line := range lines {
			if re.MatchString(line) {
				t.Errorf("SECURITY.md line %d carries %s: %s", number+1, rule.name, strings.TrimSpace(line))
			}
		}
	}
}
