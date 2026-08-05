package skills

import "testing"

func TestTreeCarriesSkillAndReferences(t *testing.T) {
	for _, path := range []string{
		"agentctl/SKILL.md",
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
	}
}
