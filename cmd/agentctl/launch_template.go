package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/fleet"
	"github.com/mnbf9rca/agentctl/internal/launchtemplate"
)

// launchConfiguration is the effective, validated input to fleet.Launch. The
// template report is nil for the original flag-only launch form.
type launchConfiguration struct {
	fleet        config.FleetConfig
	directory    *string
	presentation fleet.Presentation
	template     *launchTemplateProvenance
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

type runTemplateRoleAbsentError struct {
	path string
	role string
}

type runTemplateRoleDuplicateError struct {
	path string
	role string
}

func (e *runTemplateRoleDuplicateError) Error() string {
	return fmt.Sprintf("role %s appears more than once in template %s (run-template-role-duplicate)", e.role, e.path)
}

func (e *runTemplateRoleAbsentError) Error() string {
	return fmt.Sprintf("role %s is not in template %s (run-template-role-absent)", e.role, e.path)
}

func selectRunTemplateRole(path, roleName string) (config.RoleConfig, error) {
	document, err := launchtemplate.Decode(path)
	if err != nil {
		var templateErr *launchtemplate.Error
		if errors.As(err, &templateErr) && templateErr.Reason == fmt.Sprintf("duplicate role %q", roleName) {
			return config.RoleConfig{}, &runTemplateRoleDuplicateError{path: path, role: roleName}
		}
		return config.RoleConfig{}, err
	}
	for index, role := range document.Roles {
		if role.Name != roleName {
			continue
		}
		if role.Harness == nil {
			return config.RoleConfig{}, &launchtemplate.Error{Path: path, Location: fmt.Sprintf("roles[%d].harness", index), Reason: "is required for the selected run role"}
		}
		harness, err := config.ParseHarness(*role.Harness)
		if err != nil {
			return config.RoleConfig{}, wrapTemplateValidation(path, fmt.Sprintf("roles[%d].harness", index), err)
		}
		selected := config.RoleConfig{Name: role.Name, Harness: harness}
		if role.Model != nil {
			if err := config.ValidateModelName(*role.Model); err != nil {
				return config.RoleConfig{}, wrapTemplateValidation(path, fmt.Sprintf("roles[%d].model", index), err)
			}
			selected.Model = *role.Model
		}
		if role.Effort != nil {
			if err := config.ValidateEffort(*role.Effort); err != nil {
				return config.RoleConfig{}, wrapTemplateValidation(path, fmt.Sprintf("roles[%d].effort", index), err)
			}
			selected.Effort = *role.Effort
		}
		return selected, nil
	}
	return config.RoleConfig{}, &runTemplateRoleAbsentError{path: path, role: roleName}
}

func decodeLaunchTemplate(path *string) (*launchtemplate.Document, error) {
	if path == nil {
		return nil, nil
	}
	document, err := launchtemplate.Decode(*path)
	if err != nil {
		return nil, err
	}
	return &document, nil
}

func mergeLaunchTemplate(document launchtemplate.Document, options launchOptions) (launchConfiguration, error) {
	roles := make([]mergedTemplateRole, 0, len(document.Roles))
	roleIndexes := make(map[string]int, len(document.Roles))
	for index, role := range document.Roles {
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

	roleAssignments := make([]config.RoleConfig, len(roles))
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
		roleAssignments[index] = config.RoleConfig{Name: role.name, Harness: config.Harness(*role.harness)}
	}

	effective, err := config.ParseFleetRoles(roleAssignments, options.models, options.efforts)
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
			if err := config.ValidateEffort(*merged.effort); err != nil {
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
		fleet: effective, directory: directory, presentation: resolveLaunchPresentation(document.Presentation, options),
		template: &launchTemplateProvenance{
			path: document.Path, roles: provenance,
			directorySupplied: document.Directory != nil, directoryFrom: directoryFrom,
		},
	}, nil
}

func resolveLaunchPresentation(templateValue *string, options launchOptions) fleet.Presentation {
	presentation := fleet.PresentationDetached
	if templateValue != nil {
		presentation = fleet.Presentation(*templateValue)
	}
	if options.presentation != "" {
		presentation = options.presentation
	}
	return presentation
}

func wrapTemplateValidation(path, location string, err error) error {
	var validation *config.ValidationError
	if !errors.As(err, &validation) {
		return &launchtemplate.Error{Path: path, Location: location, Reason: err.Error(), Cause: err}
	}
	return &launchtemplate.Error{Path: path, Location: location, Reason: validation.FormatReason(validation.TemplateSubject), Cause: err}
}

func writeLaunchTemplateProvenance(stdout io.Writer, session string, configuration launchConfiguration, directory string) {
	if configuration.template == nil {
		return
	}
	for index, role := range configuration.fleet.Roles {
		source := configuration.template.roles[index]
		fmt.Fprintf(stdout,
			"agentctl: launched %s in %s: harness %s (%s), model %s (%s), effort %s (%s)\n",
			role.Name, session,
			role.Harness, source.harness,
			renderModel(role.Model), source.model,
			renderEffort(role.Effort), source.effort,
		)
	}
	if configuration.template.directorySupplied {
		fmt.Fprintf(stdout, "agentctl: template %s: dir %s (%s)\n",
			configuration.template.path, directory, configuration.template.directoryFrom)
	}
}
