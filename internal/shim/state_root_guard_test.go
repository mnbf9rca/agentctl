//go:build darwin

package shim

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStateRootGuardFindsRoleAndSessionDisagreementFromRuntimeAnchor(t *testing.T) {
	parent := shortTempDir(t)
	runtimeRoot := filepath.Join(parent, "runtime")
	recordedNamespace, err := openNamespaceRoots(namespaceRoots{
		Runtime: runtimeRoot,
		State:   filepath.Join(parent, "recorded-state"),
	})
	if err != nil {
		t.Fatalf("open recorded namespace: %v", err)
	}
	t.Cleanup(func() { _ = recordedNamespace.Close() })
	path, err := recordedNamespace.RolePath("fleet", "planner")
	if err != nil {
		t.Fatalf("create recorded role path: %v", err)
	}
	t.Cleanup(func() { _ = path.Close() })
	claim, err := AcquireClaim(path, Advisory{
		Version: ShimProtocolVersion, ShimPID: os.Getpid(), Nonce: "root-guard",
		StateRoot: recordedNamespace.StateRoot,
	})
	if err != nil {
		t.Fatalf("acquire recorded role claim: %v", err)
	}
	t.Cleanup(func() { _ = claim.Close() })

	localNamespace, err := openNamespaceRoots(namespaceRoots{
		Runtime: runtimeRoot,
		State:   filepath.Join(parent, "local-state"),
	})
	if err != nil {
		t.Fatalf("open divergent local namespace: %v", err)
	}
	t.Cleanup(func() { _ = localNamespace.Close() })
	guard := NewStateRootGuard(localNamespace)

	roleErr := guard.CheckRole("fleet", "planner")
	var roleDisagreement *StateRootDisagreementError
	if !errors.As(roleErr, &roleDisagreement) {
		t.Fatalf("CheckRole() error = %T %v, want *StateRootDisagreementError", roleErr, roleErr)
	}
	if roleDisagreement.LocalRoot != localNamespace.StateRoot || roleDisagreement.RecordedRoot != recordedNamespace.StateRoot {
		t.Fatalf("CheckRole() disagreement = %#v, want local %q recorded %q", roleDisagreement, localNamespace.StateRoot, recordedNamespace.StateRoot)
	}

	role, sessionErr := guard.CheckSession("fleet")
	var sessionDisagreement *StateRootDisagreementError
	if role != "planner" || !errors.As(sessionErr, &sessionDisagreement) {
		t.Fatalf("CheckSession() = role %q, error %T %v, want planner disagreement", role, sessionErr, sessionErr)
	}
	if sessionDisagreement.LocalRoot != localNamespace.StateRoot || sessionDisagreement.RecordedRoot != recordedNamespace.StateRoot {
		t.Fatalf("CheckSession() disagreement = %#v, want local %q recorded %q", sessionDisagreement, localNamespace.StateRoot, recordedNamespace.StateRoot)
	}

	if role, err := guard.CheckSession("absent"); role != "" || err != nil {
		t.Fatalf("CheckSession(absent) = role %q, error %v, want clean absence", role, err)
	}
}
