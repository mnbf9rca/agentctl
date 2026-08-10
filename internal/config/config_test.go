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

	got, err := ParseFleet("planner:claude,codex1:codex,codex-r:codex", nil, nil)
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

func TestParseFleetRolesAppliesAssignmentsWithoutChangingRoleOrder(t *testing.T) {
	t.Parallel()

	models := "planner:opus-4-1"
	efforts := "worker:high"
	got, err := ParseFleetRoles([]RoleConfig{
		{Name: "planner", Harness: HarnessClaude},
		{Name: "worker", Harness: HarnessCodex},
	}, &models, &efforts)
	if err != nil {
		t.Fatalf("ParseFleetRoles() error = %v", err)
	}
	want := FleetConfig{Roles: []RoleConfig{
		{Name: "planner", Harness: HarnessClaude, Model: "opus-4-1"},
		{Name: "worker", Harness: HarnessCodex, Effort: "high"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseFleetRoles() = %#v, want %#v", got, want)
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

			_, err := ParseFleet(test.roles, nil, nil)
			assertValidationError(t, err, "roles", test.roles, test.entryIndex, test.entry, test.reason, test.message)
		})
	}
}

func TestParseFleetAppliesModelsWithoutChangingRoleOrder(t *testing.T) {
	t.Parallel()

	models := "a1:Fable_1.2-x"
	got, err := ParseFleet("a1:claude,b-2:codex", &models, nil)
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
	got, err := ParseFleet(roles, &models, nil)
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

			_, err := ParseFleet(test.roles, &test.models, nil)
			assertValidationError(t, err, "models", test.models, test.entryIndex, test.entry, test.reason, test.message)
		})
	}
}

func TestParseFleetAppliesEffortsWithoutChangingRoleOrder(t *testing.T) {
	t.Parallel()

	models := "a1:fable"
	efforts := "b-2:xhigh,a1:max"
	got, err := ParseFleet("a1:claude,b-2:codex", &models, &efforts)
	if err != nil {
		t.Fatalf("ParseFleet() error = %v", err)
	}
	want := FleetConfig{Roles: []RoleConfig{
		{Name: "a1", Harness: HarnessClaude, Model: "fable", Effort: "max"},
		{Name: "b-2", Harness: HarnessCodex, Effort: "xhigh"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseFleet() = %#v, want %#v", got, want)
	}
}

func TestParseFleetOmittedEffortsLeaveEveryRoleUnset(t *testing.T) {
	t.Parallel()

	got, err := ParseFleet("a1:claude,b-2:codex", nil, nil)
	if err != nil {
		t.Fatalf("ParseFleet() error = %v", err)
	}
	for _, role := range got.Roles {
		if role.Effort != "" {
			t.Fatalf("role %q effort = %q, want %q", role.Name, role.Effort, "")
		}
	}
}

func TestValidateEffortEnforcesCharset(t *testing.T) {
	t.Parallel()

	for _, effort := range []string{"low", "minimal", "ultra", "EXtreme", "High_2.0", "a"} {
		if err := ValidateEffort(effort); err != nil {
			t.Errorf("ValidateEffort(%q) error = %v, want nil", effort, err)
		}
	}

	for _, tt := range []struct {
		name   string
		effort string
	}{
		{name: "empty", effort: ""},
		{name: "single quote", effort: "high'"},
		{name: "double quote", effort: `high"`},
		{name: "backslash", effort: `high\`},
		{name: "newline", effort: "high\n"},
		{name: "space", effort: "hi gh"},
		{name: "equals", effort: "high=evil"},
		{name: "colon", effort: "high:evil"},
		{name: "leading hyphen", effort: "-high"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateEffort(tt.effort)
			assertValidationError(t, err, "effort", tt.effort, -1, "",
				"must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$",
				fmt.Sprintf("invalid --effort value %q: effort %q must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$", tt.effort, tt.effort),
			)
		})
	}
}

func TestValidateModelAndEffortReasonsExcludeTheRejectedValue(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		err   error
		value string
	}{
		{name: "model", err: ValidateModelName("bad model"), value: "bad model"},
		{name: "effort", err: ValidateEffort("bad effort"), value: "bad effort"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			var validation *ValidationError
			if !errors.As(test.err, &validation) {
				t.Fatalf("error = %T %v, want *ValidationError", test.err, test.err)
			}
			if validation.Reason != "must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$" {
				t.Fatalf("Reason = %q, want value-free reason", validation.Reason)
			}
			if validation.Value != test.value {
				t.Fatalf("Value = %q, want %q", validation.Value, test.value)
			}
		})
	}
}

func TestValidationErrorsDeclareTheirDisplaySubjects(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name            string
		err             error
		directSubject   string
		templateSubject string
		wantTemplate    string
	}{
		{
			name: "model", err: ValidateModelName("bad model"),
			directSubject: "", templateSubject: "value",
			wantTemplate: `value "bad model" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$`,
		},
		{
			name: "effort", err: ValidateEffort("bad effort"),
			directSubject: "effort", templateSubject: "effort",
			wantTemplate: `effort "bad effort" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$`,
		},
		{
			name: "harness", err: invalidHarnessErrorForDisplaySubjects(),
			directSubject: "", templateSubject: "value",
			wantTemplate: `value "future" must be claude or codex`,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			var validation *ValidationError
			if !errors.As(test.err, &validation) {
				t.Fatalf("error = %T %v, want *ValidationError", test.err, test.err)
			}
			if validation.DirectSubject != test.directSubject || validation.TemplateSubject != test.templateSubject {
				t.Fatalf("display subjects = (%q, %q), want (%q, %q)", validation.DirectSubject, validation.TemplateSubject, test.directSubject, test.templateSubject)
			}
			if got := validation.FormatReason(validation.TemplateSubject); got != test.wantTemplate {
				t.Fatalf("FormatReason() = %q, want %q", got, test.wantTemplate)
			}
		})
	}
}

func invalidHarnessErrorForDisplaySubjects() error {
	_, err := ParseHarness("future")
	return err
}

func TestParseFleetAcceptsMixedCaseEffortWithoutChangingIt(t *testing.T) {
	t.Parallel()

	efforts := "p:EXtreme"
	got, err := ParseFleet("p:codex", nil, &efforts)
	if err != nil {
		t.Fatalf("ParseFleet() error = %v", err)
	}
	want := FleetConfig{Roles: []RoleConfig{{Name: "p", Harness: HarnessCodex, Effort: "EXtreme"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseFleet() = %#v, want %#v", got, want)
	}
}

func TestParseFleetRejectsInvalidEffortLists(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		roles      string
		efforts    string
		entryIndex int
		entry      string
		reason     string
		message    string
	}{
		{name: "explicit empty efforts", roles: "p:claude", efforts: "", entryIndex: -1, reason: "must not be empty", message: "invalid --efforts value \"\": must not be empty"},
		{name: "duplicate effort same value", roles: "p:claude", efforts: "p:high,p:high", entryIndex: 2, entry: "p:high", reason: "duplicate effort entry for role \"p\"", message: "invalid --efforts entry 2 \"p:high\": duplicate effort entry for role \"p\""},
		{name: "duplicate effort different value", roles: "p:claude", efforts: "p:high,p:low", entryIndex: 2, entry: "p:low", reason: "duplicate effort entry for role \"p\"", message: "invalid --efforts entry 2 \"p:low\": duplicate effort entry for role \"p\""},
		{name: "undefined role", roles: "p:claude", efforts: "q:high", entryIndex: 1, entry: "q:high", reason: "effort references undefined role \"q\"", message: "invalid --efforts entry 1 \"q:high\": effort references undefined role \"q\""},
		{name: "missing role", roles: "p:claude", efforts: ":high", entryIndex: 1, entry: ":high", reason: "role is empty", message: "invalid --efforts entry 1 \":high\": role is empty"},
		{name: "missing effort", roles: "p:claude", efforts: "p:", entryIndex: 1, entry: "p:", reason: "effort is empty", message: "invalid --efforts entry 1 \"p:\": effort is empty"},
		{name: "missing separator", roles: "p:claude", efforts: "p", entryIndex: 1, entry: "p", reason: "must contain exactly one ':' separator", message: "invalid --efforts entry 1 \"p\": must contain exactly one ':' separator"},
		{name: "extra separator", roles: "p:claude", efforts: "p:high:extra", entryIndex: 1, entry: "p:high:extra", reason: "must contain exactly one ':' separator", message: "invalid --efforts entry 1 \"p:high:extra\": must contain exactly one ':' separator"},
		{name: "trailing comma", roles: "p:claude", efforts: "p:high,", entryIndex: 2, reason: "entry is empty", message: "invalid --efforts value \"p:high,\": entry 2 is empty"},
		{name: "leading comma", roles: "p:claude", efforts: ",p:high", entryIndex: 1, reason: "entry is empty", message: "invalid --efforts value \",p:high\": entry 1 is empty"},
		{name: "consecutive commas", roles: "p:claude,q:codex", efforts: "p:high,,q:low", entryIndex: 2, reason: "entry is empty", message: "invalid --efforts value \"p:high,,q:low\": entry 2 is empty"},
		{name: "invalid effort role", roles: "p:claude", efforts: "P:high", entryIndex: 1, entry: "P:high", reason: "role \"P\" must match ^[a-z0-9][a-z0-9_-]*$", message: "invalid --efforts entry 1 \"P:high\": role \"P\" must match ^[a-z0-9][a-z0-9_-]*$"},
		{name: "whitespace level", roles: "p:claude", efforts: "p:hi gh", entryIndex: 1, entry: "p:hi gh", reason: "effort \"hi gh\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$", message: "invalid --efforts entry 1 \"p:hi gh\": effort \"hi gh\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$"},
		{name: "trailing newline level", roles: "p:claude", efforts: "p:high\n", entryIndex: 1, entry: "p:high\n", reason: "effort \"high\\n\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$", message: "invalid --efforts entry 1 \"p:high\\n\": effort \"high\\n\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$"},
		{name: "flag smuggling", roles: "p:claude", efforts: "p:--dangerously-bypass-approvals-and-sandbox", entryIndex: 1, entry: "p:--dangerously-bypass-approvals-and-sandbox", reason: "effort \"--dangerously-bypass-approvals-and-sandbox\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$", message: "invalid --efforts entry 1 \"p:--dangerously-bypass-approvals-and-sandbox\": effort \"--dangerously-bypass-approvals-and-sandbox\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$"},
		{name: "toml string escape", roles: "p:codex", efforts: "p:high\"\nmodel=\"evil", entryIndex: 1, entry: "p:high\"\nmodel=\"evil", reason: "effort \"high\\\"\\nmodel=\\\"evil\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$", message: "invalid --efforts entry 1 \"p:high\\\"\\nmodel=\\\"evil\": effort \"high\\\"\\nmodel=\\\"evil\" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := ParseFleet(test.roles, nil, &test.efforts)
			assertValidationError(t, err, "efforts", test.efforts, test.entryIndex, test.entry, test.reason, test.message)
		})
	}
}

func TestValidateModelNameEnforcesCharsetIncludingEmptyAndFlagSmuggling(t *testing.T) {
	t.Parallel()

	for _, model := range []string{"fable", "gpt-5.6", "Opus_4", "a"} {
		if err := ValidateModelName(model); err != nil {
			t.Fatalf("ValidateModelName(%q) error = %v, want nil", model, err)
		}
	}
	for _, tt := range []struct {
		model   string
		message string
	}{
		{model: "", message: `invalid --model value "": must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$`},
		{model: "--dangerously-bypass-approvals-and-sandbox", message: `invalid --model value "--dangerously-bypass-approvals-and-sandbox": must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$`},
		{model: "gpt 5", message: `invalid --model value "gpt 5": must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$`},
		{model: "with:colon", message: `invalid --model value "with:colon": must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$`},
		{model: "with,comma", message: `invalid --model value "with,comma": must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$`},
	} {
		err := ValidateModelName(tt.model)
		if err == nil {
			t.Fatalf("ValidateModelName(%q) error = nil, want rejection", tt.model)
		}
		if got := err.Error(); got != tt.message {
			t.Fatalf("ValidateModelName(%q) error = %q, want %q", tt.model, got, tt.message)
		}
	}
}

func TestParseHarnessAcceptsOnlyRegisteredHarnesses(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		want Harness
	}{
		{name: "claude", want: HarnessClaude},
		{name: "codex", want: HarnessCodex},
	} {
		got, err := ParseHarness(tt.name)
		if err != nil || got != tt.want {
			t.Fatalf("ParseHarness(%q) = %q, %v, want %q, nil", tt.name, got, err, tt.want)
		}
	}
	for _, name := range []string{"", "bash", "Claude", "claude ", "sh -c"} {
		got, err := ParseHarness(name)
		if err == nil {
			t.Fatalf("ParseHarness(%q) = %q, nil, want rejection", name, got)
		}
		want := `invalid --harness value "` + name + `": must be claude or codex`
		if err.Error() != want {
			t.Fatalf("ParseHarness(%q) error = %q, want %q", name, err.Error(), want)
		}
	}
}

func TestValidateTemplateDirectoryRequiresAnAbsolutePathAndNamesTheFlagEscapeHatch(t *testing.T) {
	t.Parallel()

	if err := ValidateTemplateDirectory("/srv/work"); err != nil {
		t.Fatalf("ValidateTemplateDirectory() error = %v, want nil", err)
	}
	err := ValidateTemplateDirectory("relative/work")
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %T %v, want *ValidationError", err, err)
	}
	if validation.Option != "dir" || validation.Value != "relative/work" || validation.EntryIndex != -1 {
		t.Fatalf("ValidationError = %#v, want option=dir value=relative/work entryIndex=-1", validation)
	}
	want := `invalid --dir value "relative/work": template path must be absolute; omit dir and supply --dir at invocation`
	if got := err.Error(); got != want {
		t.Fatalf("error = %q, want %q", got, want)
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
