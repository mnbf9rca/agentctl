package main

import (
	"errors"
	"reflect"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/fleet"
	"github.com/mnbf9rca/agentctl/internal/launchtemplate"
)

func TestMergeLaunchTemplateD2AddsOverridesAndPinsUnionOrder(t *testing.T) {
	t.Parallel()

	roles := "planner:codex,reviewer:claude,worker:codex"
	models := "planner:gpt-5.6,reviewer:sonnet-4,worker:gpt-5"
	efforts := "planner:low,worker:max"
	directory := "/override"
	document := launchtemplate.Document{
		Path:      "/fleet.json",
		Directory: stringPointerForTemplate("/srv/work"),
		Roles: []launchtemplate.Role{
			{Name: "planner", Harness: stringPointerForTemplate("claude"), Model: stringPointerForTemplate("opus-4-1"), Effort: stringPointerForTemplate("high")},
			{Name: "reviewer"},
		},
	}

	got, err := mergeLaunchTemplate(document, launchOptions{
		roles: roles, rolesSet: true, models: &models, efforts: &efforts, directory: &directory,
	})
	if err != nil {
		t.Fatalf("mergeLaunchTemplate() error = %v", err)
	}
	want := launchConfiguration{
		fleet: config.FleetConfig{Roles: []config.RoleConfig{
			{Name: "planner", Harness: config.HarnessCodex, Model: "gpt-5.6", Effort: "low"},
			{Name: "reviewer", Harness: config.HarnessClaude, Model: "sonnet-4"},
			{Name: "worker", Harness: config.HarnessCodex, Model: "gpt-5", Effort: "max"},
		}},
		directory: &directory,
		template: &launchTemplateProvenance{
			path: "/fleet.json",
			roles: []launchRoleProvenance{
				{name: "planner", harness: fleet.ProvenanceOverride, model: fleet.ProvenanceOverride, effort: fleet.ProvenanceOverride},
				{name: "reviewer", harness: fleet.ProvenanceOverride, model: fleet.ProvenanceOverride, effort: fleet.ProvenanceTemplate},
				{name: "worker", harness: fleet.ProvenanceFlags, model: fleet.ProvenanceFlags, effort: fleet.ProvenanceFlags},
			},
			directorySupplied: true,
			directoryFrom:     fleet.ProvenanceOverride,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeLaunchTemplate() = %#v, want %#v", got, want)
	}
}

func TestMergeLaunchTemplateValidatesOnlySurvivingFleetValues(t *testing.T) {
	t.Parallel()

	roles := "planner:codex"
	models := "planner:gpt-5.6"
	efforts := "planner:max"
	document := launchtemplate.Document{
		Path: "/fleet.json",
		Roles: []launchtemplate.Role{{
			Name: "planner", Harness: stringPointerForTemplate("future"), Model: stringPointerForTemplate("bad model"), Effort: stringPointerForTemplate("extreme"),
		}},
	}

	got, err := mergeLaunchTemplate(document, launchOptions{roles: roles, rolesSet: true, models: &models, efforts: &efforts})
	if err != nil {
		t.Fatalf("mergeLaunchTemplate() error = %v", err)
	}
	wantRole := config.RoleConfig{Name: "planner", Harness: config.HarnessCodex, Model: "gpt-5.6", Effort: "max"}
	if len(got.fleet.Roles) != 1 || got.fleet.Roles[0] != wantRole {
		t.Fatalf("roles = %#v, want %#v", got.fleet.Roles, []config.RoleConfig{wantRole})
	}
}

func TestMergeLaunchTemplateDefaultsKeepTheDeclaringSourcesProvenance(t *testing.T) {
	t.Parallel()

	roles := "worker:codex"
	document := launchtemplate.Document{
		Path:  "/fleet.json",
		Roles: []launchtemplate.Role{{Name: "planner", Harness: stringPointerForTemplate("claude")}},
	}

	got, err := mergeLaunchTemplate(document, launchOptions{roles: roles, rolesSet: true})
	if err != nil {
		t.Fatalf("mergeLaunchTemplate() error = %v", err)
	}
	want := []launchRoleProvenance{
		{name: "planner", harness: fleet.ProvenanceTemplate, model: fleet.ProvenanceTemplate, effort: fleet.ProvenanceTemplate},
		{name: "worker", harness: fleet.ProvenanceFlags, model: fleet.ProvenanceFlags, effort: fleet.ProvenanceFlags},
	}
	if !reflect.DeepEqual(got.template.roles, want) {
		t.Fatalf("provenance = %#v, want %#v", got.template.roles, want)
	}
}

func TestMergeLaunchTemplateAcceptsModelsAndEffortsForTemplateOnlyRoles(t *testing.T) {
	t.Parallel()

	models := "planner:opus-4-1"
	efforts := "planner:high"
	document := launchtemplate.Document{
		Path:  "/fleet.json",
		Roles: []launchtemplate.Role{{Name: "planner", Harness: stringPointerForTemplate("claude")}},
	}

	got, err := mergeLaunchTemplate(document, launchOptions{models: &models, efforts: &efforts})
	if err != nil {
		t.Fatalf("mergeLaunchTemplate() error = %v", err)
	}
	want := config.RoleConfig{Name: "planner", Harness: config.HarnessClaude, Model: "opus-4-1", Effort: "high"}
	if got.fleet.Roles[0] != want {
		t.Fatalf("role = %#v, want %#v", got.fleet.Roles[0], want)
	}
	if sources := got.template.roles[0]; sources.model != fleet.ProvenanceOverride || sources.effort != fleet.ProvenanceOverride {
		t.Fatalf("sources = %#v, want model and effort flag override", sources)
	}
}

func TestMergeLaunchTemplateUnionFailuresNameTheirSourceAndRule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		document launchtemplate.Document
		options  launchOptions
		want     string
	}{
		{
			name:     "empty union",
			document: launchtemplate.Document{Path: "/fleet.json"},
			want:     `template /fleet.json: effective fleet: must contain at least one role`,
		},
		{
			name: "invalid merge key",
			document: launchtemplate.Document{Path: "/fleet.json", Roles: []launchtemplate.Role{{
				Name: "Planner", Harness: stringPointerForTemplate("claude"),
			}}},
			want: `template /fleet.json: roles[0].role: value "Planner" must match ^[a-z0-9][a-z0-9_-]*$`,
		},
		{
			name: "missing effective harness",
			document: launchtemplate.Document{Path: "/fleet.json", Roles: []launchtemplate.Role{{
				Name: "planner",
			}}},
			want: `template /fleet.json: roles[0].harness: is required after merging template and flags`,
		},
		{
			name: "unknown effective harness",
			document: launchtemplate.Document{Path: "/fleet.json", Roles: []launchtemplate.Role{{
				Name: "planner", Harness: stringPointerForTemplate("future"),
			}}},
			want: `template /fleet.json: roles[0].harness: value "future" must be claude or codex`,
		},
		{
			name: "invalid effective model",
			document: launchtemplate.Document{Path: "/fleet.json", Roles: []launchtemplate.Role{{
				Name: "planner", Harness: stringPointerForTemplate("claude"), Model: stringPointerForTemplate("bad model"),
			}}},
			want: `template /fleet.json: roles[0].model: value "bad model" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$`,
		},
		{
			name: "invalid effective effort",
			document: launchtemplate.Document{Path: "/fleet.json", Roles: []launchtemplate.Role{{
				Name: "planner", Harness: stringPointerForTemplate("codex"), Effort: stringPointerForTemplate("extreme"),
			}}},
			want: `template /fleet.json: roles[0].effort: harness "codex" does not support effort "extreme"; supported levels are low, medium, high, xhigh, max`,
		},
		{
			name:     "relative template directory",
			document: launchtemplate.Document{Path: "/fleet.json", Directory: stringPointerForTemplate("relative/work")},
			want:     `template /fleet.json: dir: path "relative/work" must be absolute; omit dir and supply --dir at invocation`,
		},
		{
			name:     "explicit empty flag roles",
			document: launchtemplate.Document{Path: "/fleet.json", Roles: []launchtemplate.Role{{Name: "planner", Harness: stringPointerForTemplate("claude")}}},
			options:  launchOptions{rolesSet: true},
			want:     `invalid --roles value "": must not be empty`,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := mergeLaunchTemplate(test.document, test.options)
			if err == nil || err.Error() != test.want {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestMergeLaunchTemplatePreservesFlagParserErrors(t *testing.T) {
	t.Parallel()

	document := launchtemplate.Document{
		Path:  "/fleet.json",
		Roles: []launchtemplate.Role{{Name: "planner", Harness: stringPointerForTemplate("claude")}},
	}
	models := "worker:gpt-5"
	_, err := mergeLaunchTemplate(document, launchOptions{models: &models})
	var validation *config.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %T %v, want *config.ValidationError", err, err)
	}
	want := `invalid --models entry 1 "worker:gpt-5": model references undefined role "worker"`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func stringPointerForTemplate(value string) *string { return &value }
