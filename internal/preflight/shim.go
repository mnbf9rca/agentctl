package preflight

import (
	"fmt"
	"path/filepath"

	"github.com/mnbf9rca/agentctl/internal/config"
)

// ExecutableFunc resolves the currently running agentctl executable.
type ExecutableFunc func() (string, error)

// ShimExecutableError reports that the current executable cannot safely be
// used as the first argv element of the hidden resident-shim command.
type ShimExecutableError struct {
	Path string
	Err  error
}

func (e *ShimExecutableError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("resolve current agentctl executable: %v", e.Err)
	}
	return fmt.Sprintf("current agentctl executable %q is not an absolute path", e.Path)
}

func (e *ShimExecutableError) Unwrap() error { return e.Err }

// CheckShimExecutables resolves the exact current binary before checking the
// external programs needed by the unchanged harness argv. It performs no
// process or filesystem mutation.
func CheckShimExecutables(fleet config.FleetConfig, lookPath LookPathFunc, executable ExecutableFunc) (string, error) {
	path, err := executable()
	if err != nil {
		return "", &ShimExecutableError{Err: err}
	}
	if !filepath.IsAbs(path) {
		return "", &ShimExecutableError{Path: path}
	}
	if err := CheckExecutables(fleet, lookPath); err != nil {
		return "", err
	}
	return path, nil
}
