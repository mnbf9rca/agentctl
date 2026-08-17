//go:build darwin

package shim

import (
	"errors"
	"os"
)

// StateRootGuard compares independently resolved durable state with the
// advisory facts anchored in the unchanged volatile runtime namespace.
type StateRootGuard struct {
	namespace *Namespace
}

// NewStateRootGuard constructs the read-only pre-dispatch root guard.
func NewStateRootGuard(namespace *Namespace) StateRootGuard {
	return StateRootGuard{namespace: namespace}
}

// CheckRole consults one runtime lockfile anchor without opening the durable
// role tree. Clean anchor absence permits the caller's ordinary lookup path.
func (g StateRootGuard) CheckRole(session, role string) error {
	if g.namespace == nil {
		return errors.New("state-root guard requires runtime namespace")
	}
	path, err := g.namespace.ExistingRuntimeRolePath(session, role)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = path.Close() }()
	advisory, err := ReadAdvisory(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return advisory.CompareStateRoot(g.namespace.StateRoot)
}

// CheckSession checks every runtime lockfile anchor in lexical role order and
// returns the role whose observation refused the session-level operation.
func (g StateRootGuard) CheckSession(session string) (string, error) {
	if g.namespace == nil {
		return "", errors.New("state-root guard requires runtime namespace")
	}
	roles, err := g.namespace.ListRuntimeRoles(session)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	for _, role := range roles {
		if err := g.CheckRole(session, role); err != nil {
			return role, err
		}
	}
	return "", nil
}
