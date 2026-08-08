package skills

import (
	"strings"
	"testing"
)

func TestTreeCarriesSkillAndReferences(t *testing.T) {
	skillPath := Root + "/SKILL.md"
	for _, path := range []string{
		skillPath,
		"agentctl/references/status-states.md",
		"agentctl/references/exit-codes.md",
	} {
		content, err := Tree.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%q): %v", path, err)
		}
		if len(content) == 0 {
			t.Fatalf("ReadFile(%q): empty", path)
		}
		if path == skillPath && !strings.Contains(string(content), "name: agentctl") {
			t.Fatalf("ReadFile(%q): missing skill name", path)
		}
	}
}
