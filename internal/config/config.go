// Package config parses and validates fleet configuration values.
package config

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	namePattern  = `^[a-z0-9][a-z0-9_-]*$`
	modelPattern = `^[a-zA-Z0-9][a-zA-Z0-9._-]*$`
)

var (
	nameExpression  = regexp.MustCompile(namePattern)
	modelExpression = regexp.MustCompile(modelPattern)
)

// Harness identifies an agent harness supported by agentctl.
type Harness string

const (
	HarnessClaude Harness = "claude"
	HarnessCodex  Harness = "codex"
)

// effortLevels lists the effort levels each harness accepts, in increasing
// order. Unlike models, efforts are a closed set: the value names a harness
// mode rather than an opaque identifier, and the codex rendering embeds it in a
// configuration expression the harness parses. Levels verified 2026-08-03
// against Claude Code 2.1.220 (`--effort <level>`: low, medium, high, xhigh,
// max) and codex-cli 0.146.0 (`model_reasoning_effort`, whose enum also carries
// none, minimal and ultra — not exposed here because agentctl accepts only
// levels verified on both harnesses).
var effortLevels = map[Harness][]string{
	HarnessClaude: {"low", "medium", "high", "xhigh", "max"},
	HarnessCodex:  {"low", "medium", "high", "xhigh", "max"},
}

// RoleConfig is the validated configuration for one fleet role.
type RoleConfig struct {
	Name    string
	Harness Harness
	Model   string
	Effort  string
}

// EffortLevels returns the effort levels a harness accepts, in increasing
// order, or nil for an unsupported harness.
func EffortLevels(harness Harness) []string {
	levels, ok := effortLevels[harness]
	if !ok {
		return nil
	}
	return append([]string(nil), levels...)
}

// FleetConfig is an ordered, validated fleet declaration.
type FleetConfig struct {
	Roles []RoleConfig
}

// ValidationError describes an invalid configuration value.
type ValidationError struct {
	Option string
	Value  string
	// EntryIndex is one-based for list entries and -1 when the whole value is invalid.
	EntryIndex int
	Entry      string
	Reason     string
}

func (e *ValidationError) Error() string {
	if e.Option == "session" || e.Option == "role" {
		return fmt.Sprintf("invalid %s %q: %s", e.Option, e.Value, e.Reason)
	}
	if e.EntryIndex >= 1 && e.Entry == "" {
		return fmt.Sprintf("invalid --%s value %q: entry %d is empty", e.Option, e.Value, e.EntryIndex)
	}
	if e.EntryIndex >= 1 {
		return fmt.Sprintf("invalid --%s entry %d %q: %s", e.Option, e.EntryIndex, e.Entry, e.Reason)
	}
	return fmt.Sprintf("invalid --%s value %q: %s", e.Option, e.Value, e.Reason)
}

// ValidateSessionName validates a tmux session name accepted by agentctl.
func ValidateSessionName(name string) error {
	if !nameExpression.MatchString(name) {
		return &ValidationError{
			Option:     "session",
			Value:      name,
			EntryIndex: -1,
			Reason:     "must match " + namePattern,
		}
	}
	return nil
}

// ValidateRoleName validates one role name accepted by agentctl control commands.
func ValidateRoleName(role string) error {
	if !nameExpression.MatchString(role) {
		return &ValidationError{
			Option:     "role",
			Value:      role,
			EntryIndex: -1,
			Reason:     "must match " + namePattern,
		}
	}
	return nil
}

// ParseFleet parses ordered role declarations and optional model and effort
// assignments. A nil models or efforts value means the option was omitted; a
// non-nil empty value means it was explicitly supplied as empty.
func ParseFleet(roles string, models, efforts *string) (FleetConfig, error) {
	fleet, err := parseRoles(roles)
	if err != nil {
		return FleetConfig{}, err
	}
	if models != nil {
		if *models == "" {
			return FleetConfig{}, &ValidationError{
				Option:     "models",
				Value:      *models,
				EntryIndex: -1,
				Reason:     "must not be empty",
			}
		}
		fleet, err = applyModels(fleet, *models)
		if err != nil {
			return FleetConfig{}, err
		}
	}
	if efforts == nil {
		return fleet, nil
	}
	if *efforts == "" {
		return FleetConfig{}, &ValidationError{
			Option:     "efforts",
			Value:      *efforts,
			EntryIndex: -1,
			Reason:     "must not be empty",
		}
	}
	return applyEfforts(fleet, *efforts)
}

func parseRoles(roles string) (FleetConfig, error) {
	if roles == "" {
		return FleetConfig{}, &ValidationError{
			Option:     "roles",
			Value:      roles,
			EntryIndex: -1,
			Reason:     "must not be empty",
		}
	}

	entries := strings.Split(roles, ",")
	fleet := FleetConfig{Roles: make([]RoleConfig, 0, len(entries))}
	seen := make(map[string]struct{}, len(entries))
	for index, entry := range entries {
		entryIndex := index + 1
		if entry == "" {
			return FleetConfig{}, listEntryError("roles", roles, entryIndex, entry, "entry is empty")
		}
		if strings.Count(entry, ":") != 1 {
			return FleetConfig{}, listEntryError("roles", roles, entryIndex, entry, "must contain exactly one ':' separator")
		}

		role, harnessName, _ := strings.Cut(entry, ":")
		if role == "" {
			return FleetConfig{}, listEntryError("roles", roles, entryIndex, entry, "role is empty")
		}
		if harnessName == "" {
			return FleetConfig{}, listEntryError("roles", roles, entryIndex, entry, "harness is empty")
		}
		if !nameExpression.MatchString(role) {
			return FleetConfig{}, listEntryError("roles", roles, entryIndex, entry, fmt.Sprintf("role %q must match %s", role, namePattern))
		}

		var harness Harness
		switch harnessName {
		case string(HarnessClaude):
			harness = HarnessClaude
		case string(HarnessCodex):
			harness = HarnessCodex
		default:
			return FleetConfig{}, listEntryError("roles", roles, entryIndex, entry, fmt.Sprintf("unknown harness %q", harnessName))
		}
		if _, duplicate := seen[role]; duplicate {
			return FleetConfig{}, listEntryError("roles", roles, entryIndex, entry, fmt.Sprintf("duplicate role %q", role))
		}
		seen[role] = struct{}{}

		fleet.Roles = append(fleet.Roles, RoleConfig{
			Name:    role,
			Harness: harness,
		})
	}
	return fleet, nil
}

func applyModels(fleet FleetConfig, models string) (FleetConfig, error) {
	roleIndexes := make(map[string]int, len(fleet.Roles))
	for index, role := range fleet.Roles {
		roleIndexes[role.Name] = index
	}
	modelEntries := strings.Split(models, ",")
	modelRoles := make(map[string]struct{}, len(modelEntries))
	for index, entry := range modelEntries {
		entryIndex := index + 1
		if entry == "" {
			return FleetConfig{}, listEntryError("models", models, entryIndex, entry, "entry is empty")
		}
		if strings.Count(entry, ":") != 1 {
			return FleetConfig{}, listEntryError("models", models, entryIndex, entry, "must contain exactly one ':' separator")
		}

		role, model, _ := strings.Cut(entry, ":")
		if role == "" {
			return FleetConfig{}, listEntryError("models", models, entryIndex, entry, "role is empty")
		}
		if model == "" {
			return FleetConfig{}, listEntryError("models", models, entryIndex, entry, "model is empty")
		}
		if !nameExpression.MatchString(role) {
			return FleetConfig{}, listEntryError("models", models, entryIndex, entry, fmt.Sprintf("role %q must match %s", role, namePattern))
		}
		if !modelExpression.MatchString(model) {
			return FleetConfig{}, listEntryError("models", models, entryIndex, entry, fmt.Sprintf("model %q must match %s", model, modelPattern))
		}
		if _, duplicate := modelRoles[role]; duplicate {
			return FleetConfig{}, listEntryError("models", models, entryIndex, entry, fmt.Sprintf("duplicate model entry for role %q", role))
		}
		roleIndex, defined := roleIndexes[role]
		if !defined {
			return FleetConfig{}, listEntryError("models", models, entryIndex, entry, fmt.Sprintf("model references undefined role %q", role))
		}

		modelRoles[role] = struct{}{}
		fleet.Roles[roleIndex].Model = model
	}

	return fleet, nil
}

func applyEfforts(fleet FleetConfig, efforts string) (FleetConfig, error) {
	roleIndexes := make(map[string]int, len(fleet.Roles))
	for index, role := range fleet.Roles {
		roleIndexes[role.Name] = index
	}
	effortEntries := strings.Split(efforts, ",")
	effortRoles := make(map[string]struct{}, len(effortEntries))
	for index, entry := range effortEntries {
		entryIndex := index + 1
		if entry == "" {
			return FleetConfig{}, listEntryError("efforts", efforts, entryIndex, entry, "entry is empty")
		}
		if strings.Count(entry, ":") != 1 {
			return FleetConfig{}, listEntryError("efforts", efforts, entryIndex, entry, "must contain exactly one ':' separator")
		}

		role, effort, _ := strings.Cut(entry, ":")
		if role == "" {
			return FleetConfig{}, listEntryError("efforts", efforts, entryIndex, entry, "role is empty")
		}
		if effort == "" {
			return FleetConfig{}, listEntryError("efforts", efforts, entryIndex, entry, "effort is empty")
		}
		if !nameExpression.MatchString(role) {
			return FleetConfig{}, listEntryError("efforts", efforts, entryIndex, entry, fmt.Sprintf("role %q must match %s", role, namePattern))
		}
		if _, duplicate := effortRoles[role]; duplicate {
			return FleetConfig{}, listEntryError("efforts", efforts, entryIndex, entry, fmt.Sprintf("duplicate effort entry for role %q", role))
		}
		roleIndex, defined := roleIndexes[role]
		if !defined {
			return FleetConfig{}, listEntryError("efforts", efforts, entryIndex, entry, fmt.Sprintf("effort references undefined role %q", role))
		}
		harness := fleet.Roles[roleIndex].Harness
		if !supportsEffort(harness, effort) {
			return FleetConfig{}, listEntryError("efforts", efforts, entryIndex, entry, fmt.Sprintf(
				"harness %q does not support effort %q; supported levels are %s",
				harness, effort, strings.Join(effortLevels[harness], ", "),
			))
		}

		effortRoles[role] = struct{}{}
		fleet.Roles[roleIndex].Effort = effort
	}

	return fleet, nil
}

func supportsEffort(harness Harness, effort string) bool {
	for _, level := range effortLevels[harness] {
		if level == effort {
			return true
		}
	}
	return false
}

func listEntryError(option, value string, entryIndex int, entry, reason string) *ValidationError {
	return &ValidationError{
		Option:     option,
		Value:      value,
		EntryIndex: entryIndex,
		Entry:      entry,
		Reason:     reason,
	}
}
