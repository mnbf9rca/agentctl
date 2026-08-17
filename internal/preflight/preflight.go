// Package preflight checks launch prerequisites without executing commands.
package preflight

import (
	"fmt"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/harness"
)

// LookPathFunc resolves an executable name using PATH semantics.
type LookPathFunc func(string) (string, error)

// MissingExecutableError identifies a required executable that did not resolve.
type MissingExecutableError struct {
	Name string
}

func (e *MissingExecutableError) Error() string {
	return fmt.Sprintf("required executable %q not found", e.Name)
}

// CheckExecutables confirms that the presentation's base tools and requested
// harnesses resolve. Detached shims do not use tmux.
func CheckExecutables(fleet config.FleetConfig, requireTmux bool, lookPath LookPathFunc) error {
	required := []string{"amq"}
	seen := map[string]struct{}{"amq": {}}
	if requireTmux {
		required = append([]string{"tmux"}, required...)
		seen["tmux"] = struct{}{}
	}
	for _, role := range fleet.Roles {
		spec, ok := harness.Lookup(string(role.Harness))
		if !ok {
			return fmt.Errorf("no executable registered for harness %q", role.Harness)
		}
		if _, duplicate := seen[spec.Executable]; duplicate {
			continue
		}
		seen[spec.Executable] = struct{}{}
		required = append(required, spec.Executable)
	}

	for _, name := range required {
		if _, err := lookPath(name); err != nil {
			return &MissingExecutableError{Name: name}
		}
	}
	return nil
}
