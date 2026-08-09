package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/fleet"
	"github.com/mnbf9rca/agentctl/internal/launchtemplate"
)

// launchConfiguration is the effective, validated input to fleet.Launch. The
// template report is nil for the original flag-only launch form.
type launchConfiguration struct {
	fleet     config.FleetConfig
	directory *string
	template  *launchTemplateProvenance
}

type launchTemplateProvenance struct {
	path              string
	roles             []launchRoleProvenance
	directorySupplied bool
	directoryFrom     fleet.Provenance
}

type launchRoleProvenance struct {
	name    string
	harness fleet.Provenance
	model   fleet.Provenance
	effort  fleet.Provenance
}

type mergedTemplateRole struct {
	name          string
	harness       *string
	model         *string
	effort        *string
	templateIndex int
	fromTemplate  bool
	sources       launchRoleProvenance
}

func mergeLaunchTemplate(document launchtemplate.Document, options launchOptions) (launchConfiguration, error) {
	if document.Directory != nil {
		if err := config.ValidateTemplateDirectory(*document.Directory); err != nil {
			return launchConfiguration{}, wrapTemplateDirectoryError(document.Path, *document.Directory, err)
		}
	}

	roles := make([]mergedTemplateRole, 0, len(document.Roles))
	roleIndexes := make(map[string]int, len(document.Roles))
	for index, role := range document.Roles {
		if err := config.ValidateRoleName(role.Name); err != nil {
			return launchConfiguration{}, wrapTemplateValidation(document.Path, fmt.Sprintf("roles[%d].role", index), err)
		}
		roles = append(roles, mergedTemplateRole{
			name:          role.Name,
			harness:       role.Harness,
			model:         role.Model,
			effort:        role.Effort,
			templateIndex: index,
			fromTemplate:  true,
			sources: launchRoleProvenance{
				name: role.Name, harness: fleet.ProvenanceTemplate, model: fleet.ProvenanceTemplate, effort: fleet.ProvenanceTemplate,
			},
		})
		roleIndexes[role.Name] = index
	}

	if options.rolesSet {
		flagRoles, err := config.ParseFleet(options.roles, nil, nil)
		if err != nil {
			return launchConfiguration{}, err
		}
		for _, role := range flagRoles.Roles {
			harness := string(role.Harness)
			if index, exists := roleIndexes[role.Name]; exists {
				roles[index].harness = &harness
				roles[index].sources.harness = fleet.ProvenanceOverride
				continue
			}
			roleIndexes[role.Name] = len(roles)
			roles = append(roles, mergedTemplateRole{
				name:    role.Name,
				harness: &harness,
				sources: launchRoleProvenance{
					name: role.Name, harness: fleet.ProvenanceFlags, model: fleet.ProvenanceFlags, effort: fleet.ProvenanceFlags,
				},
			})
		}
	}

	if len(roles) == 0 {
		return launchConfiguration{}, &launchtemplate.Error{
			Path: document.Path, Location: "effective fleet", Reason: "must contain at least one role",
		}
	}

	roleAssignments := make([]string, len(roles))
	for index := range roles {
		role := &roles[index]
		if role.harness == nil {
			return launchConfiguration{}, &launchtemplate.Error{
				Path: document.Path, Location: fmt.Sprintf("roles[%d].harness", role.templateIndex),
				Reason: "is required after merging template and flags",
			}
		}
		if role.fromTemplate && role.sources.harness == fleet.ProvenanceTemplate {
			if _, err := config.ParseHarness(*role.harness); err != nil {
				return launchConfiguration{}, wrapTemplateValidation(
					document.Path, fmt.Sprintf("roles[%d].harness", role.templateIndex), err,
				)
			}
		}
		roleAssignments[index] = role.name + ":" + *role.harness
	}

	effective, err := config.ParseFleet(strings.Join(roleAssignments, ","), options.models, options.efforts)
	if err != nil {
		return launchConfiguration{}, err
	}
	for index := range effective.Roles {
		merged := &roles[index]
		role := &effective.Roles[index]
		if !merged.fromTemplate {
			continue
		}
		if role.Model != "" {
			merged.sources.model = fleet.ProvenanceOverride
		} else if merged.model != nil {
			if err := config.ValidateModelName(*merged.model); err != nil {
				return launchConfiguration{}, wrapTemplateValidation(
					document.Path, fmt.Sprintf("roles[%d].model", merged.templateIndex), err,
				)
			}
			role.Model = *merged.model
		}
		if role.Effort != "" {
			merged.sources.effort = fleet.ProvenanceOverride
		} else if merged.effort != nil {
			if err := config.ValidateEffort(role.Harness, *merged.effort); err != nil {
				return launchConfiguration{}, wrapTemplateValidation(
					document.Path, fmt.Sprintf("roles[%d].effort", merged.templateIndex), err,
				)
			}
			role.Effort = *merged.effort
		}
	}

	directory := options.directory
	directoryFrom := fleet.ProvenanceFlags
	if document.Directory != nil {
		directoryFrom = fleet.ProvenanceTemplate
		if directory == nil {
			directory = document.Directory
		} else {
			directoryFrom = fleet.ProvenanceOverride
		}
	}
	provenance := make([]launchRoleProvenance, len(roles))
	for index, role := range roles {
		provenance[index] = role.sources
	}
	return launchConfiguration{
		fleet: effective, directory: directory,
		template: &launchTemplateProvenance{
			path: document.Path, roles: provenance,
			directorySupplied: document.Directory != nil, directoryFrom: directoryFrom,
		},
	}, nil
}

func wrapTemplateValidation(path, location string, err error) error {
	var validation *config.ValidationError
	if !errors.As(err, &validation) {
		return &launchtemplate.Error{Path: path, Location: location, Reason: err.Error(), Cause: err}
	}
	return &launchtemplate.Error{Path: path, Location: location, Reason: validation.Reason, Cause: err}
}

func wrapTemplateDirectoryError(path, value string, err error) error {
	var validation *config.ValidationError
	if !errors.As(err, &validation) {
		return &launchtemplate.Error{Path: path, Location: "dir", Reason: err.Error(), Cause: err}
	}
	reason := strings.TrimPrefix(validation.Reason, "template path ")
	return &launchtemplate.Error{
		Path: path, Location: "dir", Reason: fmt.Sprintf("path %q %s", value, reason), Cause: err,
	}
}
