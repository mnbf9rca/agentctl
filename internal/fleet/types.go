package fleet

import (
	"errors"
	"fmt"
	"os"

	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

// ShimFleetMissingError distinguishes an absent session-level fleet record
// from an absent per-role record at command boundaries.
type ShimFleetMissingError struct{ Session string }

func (e *ShimFleetMissingError) Error() string {
	return fmt.Sprintf("session %q has no durable fleet configuration", e.Session)
}

func (e *ShimFleetMissingError) Unwrap() error { return os.ErrNotExist }

func fleetMissing(session string, err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return &ShimFleetMissingError{Session: session}
	}
	return err
}

// Provenance labels launch-template fields without affecting runtime facts.
type Provenance string

const (
	ProvenanceTemplate Provenance = "template"
	ProvenanceOverride Provenance = "flag override"
	ProvenanceFlags    Provenance = "flags"
)

// DirectoryError reports an explicit launch directory that cannot be used.
type DirectoryError struct {
	Path string
	Err  error
}

func (e *DirectoryError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("invalid launch directory %q: %v", e.Path, e.Err)
	}
	return fmt.Sprintf("invalid launch directory %q: not a directory", e.Path)
}

func (e *DirectoryError) Unwrap() error { return e.Err }

// RelaunchRequest contains only optional operator-selected replacements. Nil
// fields retain the durable fleet configuration.
type RelaunchRequest struct {
	Role      string
	Harness   *string
	Model     *string
	Effort    *string
	Directory *string
}

// UnknownRoleError reports a role absent from the durable fleet roster.
type UnknownRoleError struct {
	Session tmuxx.Session
	Role    string
	Roster  string
}

func (e *UnknownRoleError) Error() string {
	return fmt.Sprintf("role %q is not in durable roster %q", e.Role, e.Roster)
}

// StoredDirectoryError reports a durable directory that cannot be used.
type StoredDirectoryError struct {
	Session tmuxx.Session
	Role    string
	Path    string
	Err     error
}

func (e *StoredDirectoryError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("durable fleet directory %q for role %q cannot be used: %v", e.Path, e.Role, e.Err)
	}
	return fmt.Sprintf("durable fleet directory %q for role %q is not a directory", e.Path, e.Role)
}

func (e *StoredDirectoryError) Unwrap() error { return e.Err }
