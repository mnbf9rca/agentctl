package config

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestValidateSessionNameAcceptsValidNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"s", "0", "epic_123-x"} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := ValidateSessionName(name); err != nil {
				t.Fatalf("ValidateSessionName(%q) error = %v, want nil", name, err)
			}
		})
	}
}

func TestValidateSessionNameRejectsInvalidNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "leading hyphen", value: "-epic"},
		{name: "space", value: "epic 1"},
		{name: "tab", value: "epic\t1"},
		{name: "trailing newline", value: "planner\n"},
		{name: "carriage return", value: "planner\r"},
		{name: "NUL", value: "planner\x00"},
		{name: "uppercase", value: "Epic"},
		{name: "dot", value: "epic.1"},
		{name: "slash", value: "epic/1"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateSessionName(test.value)
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("ValidateSessionName(%q) error = %T %v, want *ValidationError", test.value, err, err)
			}
			if validation.Option != "session" || validation.Value != test.value || validation.EntryIndex != -1 {
				t.Fatalf("ValidationError = %#v, want option=session value=%q entryIndex=-1", validation, test.value)
			}
			want := fmt.Sprintf("invalid session %q: must match ^[a-z0-9][a-z0-9_-]*$", test.value)
			if got := err.Error(); got != want {
				t.Fatalf("error = %q, want %q", got, want)
			}
		})
	}
}

func TestValidateRoleNameAcceptsValidNames(t *testing.T) {
	t.Parallel()

	for _, role := range []string{"planner", "codex_2", "review-agent"} {
		role := role
		t.Run(role, func(t *testing.T) {
			t.Parallel()
			if err := ValidateRoleName(role); err != nil {
				t.Fatalf("ValidateRoleName(%q) error = %v, want nil", role, err)
			}
		})
	}
}

func TestValidateRoleNameRejectsInvalidNames(t *testing.T) {
	t.Parallel()

	for _, role := range []string{"", "Planner", "-planner", "plan/ner", "plan\nner"} {
		role := role
		t.Run(fmt.Sprintf("%q", role), func(t *testing.T) {
			t.Parallel()

			err := ValidateRoleName(role)
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("ValidateRoleName(%q) error = %T %v, want *ValidationError", role, err, err)
			}
			if validation.Option != "role" || validation.Value != role || validation.EntryIndex != -1 {
				t.Fatalf("ValidationError = %#v, want option=role value=%q entryIndex=-1", validation, role)
			}
			want := fmt.Sprintf("invalid role %q: must match ^[a-z0-9][a-z0-9_-]*$", role)
			if got := err.Error(); got != want {
				t.Fatalf("error = %q, want %q", got, want)
			}
		})
	}
}

func TestParseFleetPreservesRoleDeclarationOrder(t *testing.T) {
	t.Parallel()

	got, err := ParseFleet("planner:claude,codex1:codex,codex-r:codex", nil)
	if err != nil {
		t.Fatalf("ParseFleet() error = %v", err)
	}
	want := FleetConfig{Roles: []RoleConfig{
		{Name: "planner", Harness: HarnessClaude},
		{Name: "codex1", Harness: HarnessCodex},
		{Name: "codex-r", Harness: HarnessCodex},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseFleet() = %#v, want %#v", got, want)
	}
}

func TestParseFleetRejectsInvalidRoleLists(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		roles      string
		entryIndex int
		entry      string
		reason     string
		message    string
	}{
		{name: "empty roles", roles: "", entryIndex: -1, reason: "must not be empty", message: "invalid --roles value \"\": must not be empty"},
		{name: "unknown harness", roles: "p:rust", entryIndex: 1, entry: "p:rust", reason: "unknown harness \"rust\"", message: "invalid --roles entry 1 \"p:rust\": unknown harness \"rust\""},
		{name: "duplicate role same harness", roles: "p:claude,p:claude", entryIndex: 2, entry: "p:claude", reason: "duplicate role \"p\"", message: "invalid --roles entry 2 \"p:claude\": duplicate role \"p\""},
		{name: "duplicate role different harness", roles: "p:claude,p:codex", entryIndex: 2, entry: "p:codex", reason: "duplicate role \"p\"", message: "invalid --roles entry 2 \"p:codex\": duplicate role \"p\""},
		{name: "missing role", roles: ":claude", entryIndex: 1, entry: ":claude", reason: "role is empty", message: "invalid --roles entry 1 \":claude\": role is empty"},
		{name: "missing harness", roles: "p:", entryIndex: 1, entry: "p:", reason: "harness is empty", message: "invalid --roles entry 1 \"p:\": harness is empty"},
		{name: "missing separator", roles: "p", entryIndex: 1, entry: "p", reason: "must contain exactly one ':' separator", message: "invalid --roles entry 1 \"p\": must contain exactly one ':' separator"},
		{name: "extra separator", roles: "p:claude:extra", entryIndex: 1, entry: "p:claude:extra", reason: "must contain exactly one ':' separator", message: "invalid --roles entry 1 \"p:claude:extra\": must contain exactly one ':' separator"},
		{name: "trailing comma", roles: "p:claude,", entryIndex: 2, reason: "entry is empty", message: "invalid --roles value \"p:claude,\": entry 2 is empty"},
		{name: "leading comma", roles: ",p:claude", entryIndex: 1, reason: "entry is empty", message: "invalid --roles value \",p:claude\": entry 1 is empty"},
		{name: "consecutive commas", roles: "p:claude,,q:codex", entryIndex: 2, reason: "entry is empty", message: "invalid --roles value \"p:claude,,q:codex\": entry 2 is empty"},
		{name: "whitespace role", roles: "plan ner:claude", entryIndex: 1, entry: "plan ner:claude", reason: "role \"plan ner\" must match ^[a-z0-9][a-z0-9_-]*$", message: "invalid --roles entry 1 \"plan ner:claude\": role \"plan ner\" must match ^[a-z0-9][a-z0-9_-]*$"},
		{name: "newline in role", roles: "plan\nner:claude", entryIndex: 1, entry: "plan\nner:claude", reason: "role \"plan\\nner\" must match ^[a-z0-9][a-z0-9_-]*$", message: "invalid --roles entry 1 \"plan\\nner:claude\": role \"plan\\nner\" must match ^[a-z0-9][a-z0-9_-]*$"},
		{name: "whitespace before harness", roles: "p: claude", entryIndex: 1, entry: "p: claude", reason: "unknown harness \" claude\"", message: "invalid --roles entry 1 \"p: claude\": unknown harness \" claude\""},
		{name: "whitespace after harness", roles: "p:claude ", entryIndex: 1, entry: "p:claude ", reason: "unknown harness \"claude \"", message: "invalid --roles entry 1 \"p:claude \": unknown harness \"claude \""},
		{name: "role begins hyphen", roles: "-p:claude", entryIndex: 1, entry: "-p:claude", reason: "role \"-p\" must match ^[a-z0-9][a-z0-9_-]*$", message: "invalid --roles entry 1 \"-p:claude\": role \"-p\" must match ^[a-z0-9][a-z0-9_-]*$"},
		{name: "uppercase role", roles: "P:claude", entryIndex: 1, entry: "P:claude", reason: "role \"P\" must match ^[a-z0-9][a-z0-9_-]*$", message: "invalid --roles entry 1 \"P:claude\": role \"P\" must match ^[a-z0-9][a-z0-9_-]*$"},
		{name: "dot in role", roles: "p.x:claude", entryIndex: 1, entry: "p.x:claude", reason: "role \"p.x\" must match ^[a-z0-9][a-z0-9_-]*$", message: "invalid --roles entry 1 \"p.x:claude\": role \"p.x\" must match ^[a-z0-9][a-z0-9_-]*$"},
		{name: "slash in role", roles: "p/x:claude", entryIndex: 1, entry: "p/x:claude", reason: "role \"p/x\" must match ^[a-z0-9][a-z0-9_-]*$", message: "invalid --roles entry 1 \"p/x:claude\": role \"p/x\" must match ^[a-z0-9][a-z0-9_-]*$"},
		{name: "uppercase harness", roles: "p:Claude", entryIndex: 1, entry: "p:Claude", reason: "unknown harness \"Claude\"", message: "invalid --roles entry 1 \"p:Claude\": unknown harness \"Claude\""},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := ParseFleet(test.roles, nil)
			assertValidationError(t, err, "roles", test.roles, test.entryIndex, test.entry, test.reason, test.message)
		})
	}
}

func TestParseFleetAppliesModelsWithoutChangingRoleOrder(t *testing.T) {
	t.Parallel()

	models := "a1:Fable_1.2-x"
	got, err := ParseFleet("a1:claude,b-2:codex", &models)
	if err != nil {
		t.Fatalf("ParseFleet() error = %v", err)
	}
	want := FleetConfig{Roles: []RoleConfig{
		{Name: "a1", Harness: HarnessClaude, Model: "Fable_1.2-x"},
		{Name: "b-2", Harness: HarnessCodex},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseFleet() = %#v, want %#v", got, want)
	}
}

func TestParseFleetAcceptsEightRoleExample(t *testing.T) {
	t.Parallel()

	roles := "planner:claude,codex1:codex,codex2:codex,codex3:codex,codex4:codex,reviewer-opus:claude,reviewer-codex:codex,designer:claude"
	models := "planner:fable,reviewer-opus:opus-4.8,reviewer-codex:gpt5.6-sol-xhigh"
	got, err := ParseFleet(roles, &models)
	if err != nil {
		t.Fatalf("ParseFleet() error = %v", err)
	}
	want := FleetConfig{Roles: []RoleConfig{
		{Name: "planner", Harness: HarnessClaude, Model: "fable"},
		{Name: "codex1", Harness: HarnessCodex},
		{Name: "codex2", Harness: HarnessCodex},
		{Name: "codex3", Harness: HarnessCodex},
		{Name: "codex4", Harness: HarnessCodex},
		{Name: "reviewer-opus", Harness: HarnessClaude, Model: "opus-4.8"},
		{Name: "reviewer-codex", Harness: HarnessCodex, Model: "gpt5.6-sol-xhigh"},
		{Name: "designer", Harness: HarnessClaude},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseFleet() = %#v, want %#v", got, want)
	}
}

func TestParseFleetRejectsInvalidModelLists(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		roles      string
		models     string
		entryIndex int
		entry      string
		reason     string
		message    string
	}{
		{name: "explicit empty models", roles: "p:claude", models: "", entryIndex: -1, reason: "must not be empty", message: "invalid --models value \"\": must not be empty"},
		{name: "duplicate model same value", roles: "p:claude", models: "p:fable,p:fable", entryIndex: 2, entry: "p:fable", reason: "duplicate model entry for role \"p\"", message: "invalid --models entry 2 \"p:fable\": duplicate model entry for role \"p\""},
		{name: "duplicate model different value", roles: "p:claude", models: "p:fable,p:opus", entryIndex: 2, entry: "p:opus", reason: "duplicate model entry for role \"p\"", message: "invalid --models entry 2 \"p:opus\": duplicate model entry for role \"p\""},
		{name: "undefined role", roles: "p:claude", models: "q:fable", entryIndex: 1, entry: "q:fable", reason: "model references undefined role \"q\"", message: "invalid --models entry 1 \"q:fable\": model references undefined role \"q\""},
		{name: "missing role", roles: "p:claude", models: ":fable", entryIndex: 1, entry: ":fable", reason: "role is empty", message: "invalid --models entry 1 \":fable\": role is empty"},
		{name: "missing model", roles: "p:claude", models: "p:", entryIndex: 1, entry: "p:", reason: "model is empty", message: "invalid --models entry 1 \"p:\": model is empty"},
		{name: "missing separator", roles: "p:claude", models: "p", entryIndex: 1, entry: "p", reason: "must contain exactly one ':' separator", message: "invalid --models entry 1 \"p\": must contain exactly one ':' separator"},
		{name: "extra separator", roles: "p:claude", models: "p:fable:extra", entryIndex: 1, entry: "p:fable:extra", reason: "must contain exactly one ':' separator", message: "invalid --models entry 1 \"p:fable:extra\": must contain exactly one ':' separator"},
		{name: "trailing comma", roles: "p:claude", models: "p:fable,", entryIndex: 2, reason: "entry is empty", message: "invalid --models value \"p:fable,\": entry 2 is empty"},
		{name: "leading comma", roles: "p:claude", models: ",p:fable", entryIndex: 1, reason: "entry is empty", message: "invalid --models value \",p:fable\": entry 1 is empty"},
		{name: "consecutive commas", roles: "p:claude,q:codex", models: "p:fable,,q:opus", entryIndex: 2, reason: "entry is empty", message: "invalid --models value \"p:fable,,q:opus\": entry 2 is empty"},
		{name: "whitespace model", roles: "p:claude", models: "p:gpt 5", entryIndex: 1, entry: "p:gpt 5", reason: "model \"gpt 5\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$", message: "invalid --models entry 1 \"p:gpt 5\": model \"gpt 5\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$"},
		{name: "tab in model", roles: "p:claude", models: "p:gpt\t5", entryIndex: 1, entry: "p:gpt\t5", reason: "model \"gpt\\t5\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$", message: "invalid --models entry 1 \"p:gpt\\t5\": model \"gpt\\t5\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$"},
		{name: "trailing newline in model", roles: "p:claude", models: "p:fable\n", entryIndex: 1, entry: "p:fable\n", reason: "model \"fable\\n\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$", message: "invalid --models entry 1 \"p:fable\\n\": model \"fable\\n\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$"},
		{name: "newline flag smuggling", roles: "p:claude", models: "p:fable\n--dangerously", entryIndex: 1, entry: "p:fable\n--dangerously", reason: "model \"fable\\n--dangerously\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$", message: "invalid --models entry 1 \"p:fable\\n--dangerously\": model \"fable\\n--dangerously\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$"},
		{name: "flag smuggling", roles: "p:claude", models: "p:--dangerously-bypass-approvals-and-sandbox", entryIndex: 1, entry: "p:--dangerously-bypass-approvals-and-sandbox", reason: "model \"--dangerously-bypass-approvals-and-sandbox\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$", message: "invalid --models entry 1 \"p:--dangerously-bypass-approvals-and-sandbox\": model \"--dangerously-bypass-approvals-and-sandbox\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$"},
		{name: "leading dot", roles: "p:claude", models: "p:.hidden", entryIndex: 1, entry: "p:.hidden", reason: "model \".hidden\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$", message: "invalid --models entry 1 \"p:.hidden\": model \".hidden\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$"},
		{name: "leading underscore", roles: "p:claude", models: "p:_hidden", entryIndex: 1, entry: "p:_hidden", reason: "model \"_hidden\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$", message: "invalid --models entry 1 \"p:_hidden\": model \"_hidden\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$"},
		{name: "slash in model", roles: "p:claude", models: "p:gpt/5", entryIndex: 1, entry: "p:gpt/5", reason: "model \"gpt/5\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$", message: "invalid --models entry 1 \"p:gpt/5\": model \"gpt/5\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$"},
		{name: "plus in model", roles: "p:claude", models: "p:gpt+5", entryIndex: 1, entry: "p:gpt+5", reason: "model \"gpt+5\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$", message: "invalid --models entry 1 \"p:gpt+5\": model \"gpt+5\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$"},
		{name: "invalid model role", roles: "p:claude", models: "P:fable", entryIndex: 1, entry: "P:fable", reason: "role \"P\" must match ^[a-z0-9][a-z0-9_-]*$", message: "invalid --models entry 1 \"P:fable\": role \"P\" must match ^[a-z0-9][a-z0-9_-]*$"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := ParseFleet(test.roles, &test.models)
			assertValidationError(t, err, "models", test.models, test.entryIndex, test.entry, test.reason, test.message)
		})
	}
}

func assertValidationError(t *testing.T, err error, option, value string, entryIndex int, entry, reason, message string) {
	t.Helper()

	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %T %v, want *ValidationError", err, err)
	}
	if validation.Option != option || validation.Value != value || validation.EntryIndex != entryIndex || validation.Entry != entry || validation.Reason != reason {
		t.Fatalf("ValidationError = %#v, want option=%q value=%q entryIndex=%d entry=%q reason=%q", validation, option, value, entryIndex, entry, reason)
	}
	if got := err.Error(); got != message {
		t.Fatalf("error = %q, want %q", got, message)
	}
}
