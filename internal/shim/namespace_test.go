package shim

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestNamespaceResolvesProductionAndDeclaredRoots(t *testing.T) {
	production, err := resolveNamespaceRoots(1234567890, "", "", "/Users/test/Library/Application Support")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := production.Runtime, "/tmp/agentctl-1234567890/v1"; got != want {
		t.Fatalf("Runtime = %q, want %q", got, want)
	}
	if got, want := production.State, "/Users/test/Library/Application Support/agentctl/state-v1"; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
	productionSocket := filepath.Join(production.Runtime, strings.Repeat("s", 32), strings.Repeat("r", 32)+".sock")
	if got, want := len(productionSocket), 98; got != want {
		t.Fatalf("worst-case production socket length = %d, want %d", got, want)
	}

	overridden, err := resolveNamespaceRoots(501, "/private/tmp/runtime", "/private/tmp/state", "/ignored")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := overridden, (namespaceRoots{Runtime: "/private/tmp/runtime", State: "/private/tmp/state"}); got != want {
		t.Fatalf("roots = %#v, want %#v", got, want)
	}
}

func TestNamespaceAcceptsExactRootCap(t *testing.T) {
	exactCap := "/" + strings.Repeat("r", RootPathMaxBytes-1)
	if _, err := resolveNamespaceRoots(501, exactCap, "/tmp/state", "/tmp/config"); err != nil {
		t.Fatalf("exact %d-byte root rejected: %v", RootPathMaxBytes, err)
	}
}

func TestNamespaceAcceptsFactoryDefaultMacOSHome(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("factory-default home fixture is specific to macOS")
	}

	parent := shortTempDir(t)
	home := filepath.Join(parent, "home")
	if err := os.Mkdir(home, 0o750); err != nil {
		t.Fatal(err)
	}
	staff, err := user.LookupGroup("staff")
	if err != nil {
		t.Fatalf("look up stock macOS staff group: %v", err)
	}
	staffGID, err := strconv.Atoi(staff.Gid)
	if err != nil {
		t.Fatalf("parse staff gid %q: %v", staff.Gid, err)
	}
	if err := os.Chown(home, os.Geteuid(), staffGID); err != nil {
		t.Fatalf("set stock macOS home group: %v", err)
	}
	if err := os.Chmod(home, 0o750); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("home stat type = %T, want *syscall.Stat_t", info.Sys())
	}
	if got := info.Mode().Perm(); got != 0o750 {
		t.Fatalf("stock home mode = %04o, want 0750", got)
	}
	if got := int(stat.Gid); got != staffGID {
		t.Fatalf("stock home gid = %d, want staff gid %d", got, staffGID)
	}

	configRoot := filepath.Join(home, "Library", "Application Support")
	if err := os.MkdirAll(configRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(configRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv(runtimeRootEnvironment, filepath.Join(parent, "runtime"))
	stateValue, stateWasSet := os.LookupEnv(stateRootEnvironment)
	if err := os.Unsetenv(stateRootEnvironment); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if stateWasSet {
			_ = os.Setenv(stateRootEnvironment, stateValue)
		} else {
			_ = os.Unsetenv(stateRootEnvironment)
		}
	})

	namespace, err := OpenNamespace()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = namespace.Close() }()
	wantStateRoot, err := filepath.EvalSymlinks(filepath.Join(configRoot, "agentctl", "state-v1"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := namespace.StateRoot, wantStateRoot; got != want {
		t.Fatalf("HOME-derived StateRoot = %q, want %q", got, want)
	}
	for _, path := range []string{filepath.Dir(namespace.StateRoot), namespace.StateRoot} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("new durable directory mode(%q) = %04o, want 0700", path, got)
		}
	}
}

func TestNamespaceValidatesHOMEAsItsOwnDeclaredSurface(t *testing.T) {
	parent := shortTempDir(t)
	t.Setenv("HOME", "relative-home")
	t.Setenv(runtimeRootEnvironment, filepath.Join(parent, "runtime"))
	stateValue, stateWasSet := os.LookupEnv(stateRootEnvironment)
	if err := os.Unsetenv(stateRootEnvironment); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if stateWasSet {
			_ = os.Setenv(stateRootEnvironment, stateValue)
		} else {
			_ = os.Unsetenv(stateRootEnvironment)
		}
	})
	_, err := OpenNamespace()
	var invalid *InvalidRootError
	if !errors.As(err, &invalid) || invalid.Kind != "home" {
		t.Fatalf("error = %T %#v, want home *InvalidRootError", err, err)
	}
}

func TestNamespaceAcceptsNonPrivateDurableAncestorModes(t *testing.T) {
	parent := shortTempDir(t)
	configRoot := filepath.Join(parent, "config")
	agentctlRoot := filepath.Join(configRoot, "agentctl")
	if err := os.MkdirAll(agentctlRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{configRoot, agentctlRoot} {
		if err := os.Chmod(path, 0o750); err != nil {
			t.Fatal(err)
		}
	}

	namespace, err := openProductionNamespaceAt(parent, 501, configRoot)
	if err != nil {
		t.Fatalf("open namespace below traversable durable ancestors: %v", err)
	}
	defer func() { _ = namespace.Close() }()
	for _, path := range []string{configRoot, agentctlRoot} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o750 {
			t.Fatalf("ancestor mode(%q) = %04o, want unchanged 0750", path, got)
		}
	}
	info, err := os.Stat(filepath.Join(agentctlRoot, "state-v1"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("state root mode = %04o, want 0700", got)
	}
}

func TestNamespacePublishesFullyResolvedStateRoot(t *testing.T) {
	parent := shortTempDir(t)
	realParent := filepath.Join(parent, "real")
	configRoot := filepath.Join(realParent, "config")
	if err := os.MkdirAll(configRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(realParent, alias); err != nil {
		t.Fatal(err)
	}
	namespace, err := openProductionNamespaceAt(parent, 501, filepath.Join(alias, "config"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = namespace.Close() }()
	want, err := filepath.EvalSymlinks(filepath.Join(configRoot, "agentctl", "state-v1"))
	if err != nil {
		t.Fatal(err)
	}
	if namespace.StateRoot != want {
		t.Fatalf("StateRoot = %q, want fully resolved %q", namespace.StateRoot, want)
	}
}

func TestNamespaceSyncsEachNewDirectoryEntryToItsParent(t *testing.T) {
	parentPath := shortTempDir(t)
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	syncCalls := 0
	syncParent := func(got *os.Root) error {
		syncCalls++
		if got != parent {
			t.Fatalf("synced root = %p, want parent %p", got, parent)
		}
		return nil
	}
	childPath := filepath.Join(parentPath, "child")
	child, err := ensurePrivateChildWithSync("state", childPath, parent, "child", syncParent)
	if err != nil {
		t.Fatal(err)
	}
	_ = child.Close()
	if syncCalls != 1 {
		t.Fatalf("sync calls after creation = %d, want 1", syncCalls)
	}
	child, err = ensurePrivateChildWithSync("state", childPath, parent, "child", syncParent)
	if err != nil {
		t.Fatal(err)
	}
	_ = child.Close()
	if syncCalls != 2 {
		t.Fatalf("sync calls after reopening existing child = %d, want 2", syncCalls)
	}
}

func TestNamespaceRejectsMissingRelativeAndOverCapDeclaredRoots(t *testing.T) {
	tests := []struct {
		name        string
		runtimeRoot string
		stateRoot   string
		configRoot  string
	}{
		{name: "missing runtime override", runtimeRoot: " ", stateRoot: "/tmp/state", configRoot: "/tmp/config"},
		{name: "relative runtime override", runtimeRoot: "relative", stateRoot: "/tmp/state", configRoot: "/tmp/config"},
		{name: "over-cap runtime override", runtimeRoot: "/" + strings.Repeat("r", RootPathMaxBytes), stateRoot: "/tmp/state", configRoot: "/tmp/config"},
		{name: "relative state override", runtimeRoot: "/tmp/runtime", stateRoot: "relative", configRoot: "/tmp/config"},
		{name: "missing user config root", runtimeRoot: "/tmp/runtime", configRoot: ""},
		{name: "relative user config root", runtimeRoot: "/tmp/runtime", configRoot: "relative"},
		{name: "over-cap user config root", runtimeRoot: "/tmp/runtime", configRoot: "/" + strings.Repeat("c", RootPathMaxBytes)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolveNamespaceRoots(501, tt.runtimeRoot, tt.stateRoot, tt.configRoot)
			var invalid *InvalidRootError
			if !errors.As(err, &invalid) {
				t.Fatalf("error = %T %v, want *InvalidRootError", err, err)
			}
		})
	}
}

func TestNamespaceCreatesAndDescriptorVerifiesPrivateRoots(t *testing.T) {
	parent := t.TempDir()
	runtimeRoot := filepath.Join(parent, "runtime")
	stateRoot := filepath.Join(parent, "state")

	namespace, err := openNamespaceRoots(namespaceRoots{Runtime: runtimeRoot, State: stateRoot})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = namespace.Close() })
	for _, path := range []string{runtimeRoot, stateRoot} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := info.Mode().Perm(), os.FileMode(0o700); got != want {
			t.Fatalf("mode(%q) = %04o, want %04o", path, got, want)
		}
	}

	_ = namespace.Close()
	namespace, err = openNamespaceRoots(namespaceRoots{Runtime: runtimeRoot, State: stateRoot})
	if err != nil {
		t.Fatalf("reopen safe roots: %v", err)
	}
}

func TestNamespaceRefusesSymlinkAndWrongModeRoots(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		parent := t.TempDir()
		target := filepath.Join(parent, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(parent, "runtime")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		_, err := openNamespaceRoots(namespaceRoots{Runtime: link, State: filepath.Join(parent, "state")})
		var invalid *InvalidRootError
		if !errors.As(err, &invalid) {
			t.Fatalf("error = %T %v, want *InvalidRootError", err, err)
		}
	})

	t.Run("wrong mode", func(t *testing.T) {
		parent := t.TempDir()
		runtimeRoot := filepath.Join(parent, "runtime")
		if err := os.Mkdir(runtimeRoot, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := openNamespaceRoots(namespaceRoots{Runtime: runtimeRoot, State: filepath.Join(parent, "state")})
		var invalid *InvalidRootError
		if !errors.As(err, &invalid) {
			t.Fatalf("error = %T %v, want *InvalidRootError", err, err)
		}
		info, statErr := os.Stat(runtimeRoot)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if got := info.Mode().Perm(); got != 0o755 {
			t.Fatalf("unsafe root mode was repaired to %04o; want refusal without repair", got)
		}
	})
}

func TestNamespaceRefusesUnsafeFinalStateRoot(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		parent := t.TempDir()
		target := filepath.Join(parent, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		stateRoot := filepath.Join(parent, "state")
		if err := os.Symlink(target, stateRoot); err != nil {
			t.Fatal(err)
		}
		_, err := openNamespaceRoots(namespaceRoots{Runtime: filepath.Join(parent, "runtime"), State: stateRoot})
		var invalid *InvalidRootError
		if !errors.As(err, &invalid) || invalid.Kind != "state" || invalid.Path != stateRoot || invalid.Reason != "must not be a symbolic link" {
			t.Fatalf("error = %T %#v, want exact state-root symlink refusal", err, err)
		}
	})

	t.Run("wrong mode", func(t *testing.T) {
		parent := t.TempDir()
		stateRoot := filepath.Join(parent, "state")
		if err := os.Mkdir(stateRoot, 0o750); err != nil {
			t.Fatal(err)
		}
		_, err := openNamespaceRoots(namespaceRoots{Runtime: filepath.Join(parent, "runtime"), State: stateRoot})
		var invalid *InvalidRootError
		if !errors.As(err, &invalid) || invalid.Kind != "state" || invalid.Path != stateRoot || invalid.Reason != "mode is 0750; expected 0700" {
			t.Fatalf("error = %T %#v, want exact state-root mode refusal", err, err)
		}
		info, statErr := os.Stat(stateRoot)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if got := info.Mode().Perm(); got != 0o750 {
			t.Fatalf("unsafe state-root mode was repaired to %04o; want refusal without repair", got)
		}
	})
}

func TestNamespaceRefusesPredictableRuntimePrecreation(t *testing.T) {
	parent := t.TempDir()
	configRoot := filepath.Join(parent, "config")
	if err := os.Mkdir(configRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	uidRoot := filepath.Join(parent, "agentctl-501")
	if err := os.Mkdir(uidRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := openProductionNamespaceAt(parent, 501, configRoot)
	var invalid *InvalidRootError
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %T %v, want *InvalidRootError", err, err)
	}
	info, statErr := os.Stat(uidRoot)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("predictable root mode was repaired to %04o; want refusal-only denial", got)
	}
}

func TestSocketPresentReportsNonSocketAsTopologyDisagreement(t *testing.T) {
	path := newTestRolePath(t)
	file, err := path.runtimeSession.OpenFile(path.Role+".sock", os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	present, err := SocketPresent(path)
	var topology *SocketTopologyError
	if present || !errors.As(err, &topology) {
		t.Fatalf("SocketPresent() = %v, %T %v, want non-socket topology error", present, err, err)
	}
}

func TestNamespaceRolePathEnforcesNameCapsAndResolvedSocketCapacityBeforeMutation(t *testing.T) {
	parent := shortTempDir(t)
	stateRoot := filepath.Join(parent, "state")
	for _, socketBytes := range []int{103, 104, 105} {
		t.Run(fmt.Sprintf("socket-%d", socketBytes), func(t *testing.T) {
			runtimeRoot := rootForSocketLength(t, parent, socketBytes)
			namespace, err := openNamespaceRoots(namespaceRoots{Runtime: runtimeRoot, State: stateRoot + fmt.Sprint(socketBytes)})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = namespace.Close() })
			rolePath, err := namespace.RolePath("s", "r")
			if socketBytes == 103 {
				if err != nil {
					t.Fatalf("RolePath at 103 bytes: %v", err)
				}
				if got := len(rolePath.Socket); got != 103 {
					t.Fatalf("socket length = %d, want 103", got)
				}
				_ = rolePath.Close()
				return
			}
			var tooLong *SocketPathTooLongError
			if !errors.As(err, &tooLong) {
				t.Fatalf("RolePath error = %T %v, want *SocketPathTooLongError", err, err)
			}
			if _, statErr := os.Stat(filepath.Join(runtimeRoot, "s")); !os.IsNotExist(statErr) {
				t.Fatalf("runtime session mutation before length refusal: stat error = %v", statErr)
			}
		})
	}

	namespace, err := openNamespaceRoots(namespaceRoots{Runtime: filepath.Join(parent, "caps-runtime"), State: filepath.Join(parent, "caps-state")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = namespace.Close() })
	if _, err := namespace.RolePath(strings.Repeat("s", 33), "r"); err == nil {
		t.Fatal("33-byte session accepted")
	}
	if _, err := namespace.RolePath("s", strings.Repeat("r", 33)); err == nil {
		t.Fatal("33-byte role accepted")
	}
}

func TestNamespaceRefusesDescriptorSubstitutionBeforeRoleMutation(t *testing.T) {
	for _, kind := range []string{"runtime", "state"} {
		t.Run(kind, func(t *testing.T) {
			parent := shortTempDir(t)
			roots := namespaceRoots{Runtime: filepath.Join(parent, "runtime"), State: filepath.Join(parent, "state")}
			namespace, err := openNamespaceRoots(roots)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = namespace.Close() })

			selectedRoot := roots.Runtime
			if kind == "state" {
				selectedRoot = namespace.StateRoot
			}
			original := selectedRoot + "-original"
			if err := os.Rename(selectedRoot, original); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(selectedRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			_, err = namespace.RolePath("s", "r")
			var substituted *RootSubstitutedError
			if !errors.As(err, &substituted) || substituted.Kind != kind || substituted.Path != selectedRoot {
				t.Fatalf("RolePath error = %T %#v, want exact %s *RootSubstitutedError", err, err, kind)
			}
			for _, root := range []string{selectedRoot, original} {
				if _, statErr := os.Stat(filepath.Join(root, "s")); !os.IsNotExist(statErr) {
					t.Fatalf("role mutation under %q after substitution: stat error = %v", root, statErr)
				}
			}
		})
	}
}

func TestNamespaceObservationFailureIsNotReportedAsSubstitution(t *testing.T) {
	parent := shortTempDir(t)
	namespace, err := openNamespaceRoots(namespaceRoots{
		Runtime: filepath.Join(parent, "runtime"),
		State:   filepath.Join(parent, "state"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = namespace.Close() }()
	if err := namespace.runtime.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = namespace.RolePath("session", "role")
	if err == nil {
		t.Fatal("RolePath succeeded after retained descriptor observation failed")
	}
	var substituted *RootSubstitutedError
	if errors.As(err, &substituted) {
		t.Fatalf("descriptor observation error was reported as substitution: %v", err)
	}
	var observation *FilesystemObservationError
	if !errors.As(err, &observation) {
		t.Fatalf("error = %T %v, want *FilesystemObservationError", err, err)
	}
}

func rootForSocketLength(t *testing.T, parent string, socketBytes int) string {
	t.Helper()
	const suffixBytes = len("/s/r.sock")
	wantRootBytes := socketBytes - suffixBytes
	fillerBytes := wantRootBytes - len(parent) - 1
	if fillerBytes < 1 {
		t.Fatalf("temporary path %q is too long for socket fixture", parent)
	}
	return filepath.Join(parent, strings.Repeat("x", fillerBytes))
}

func TestNamespaceOpensRuntimeAndDurableRoleSidesIndependentlyForStatus(t *testing.T) {
	t.Run("durable record without runtime session", func(t *testing.T) {
		base := shortTempDir(t)
		namespace, err := openNamespaceRoots(namespaceRoots{Runtime: base + "/runtime", State: base + "/state"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = namespace.Close() })
		path, err := namespace.RolePath("fleet", "planner")
		if err != nil {
			t.Fatal(err)
		}
		if err := WriteRecord(path, NewChildStartingRecord("fleet", "planner", os.Getpid(), "nonce")); err != nil {
			t.Fatal(err)
		}
		_ = path.Close()
		if err := os.RemoveAll(base + "/runtime/fleet"); err != nil {
			t.Fatal(err)
		}

		durable, err := namespace.ExistingDurableRolePath("fleet", "planner")
		if err != nil {
			t.Fatalf("ExistingDurableRolePath() error = %v", err)
		}
		defer func() { _ = durable.Close() }()
		if got, err := ReadRecord(durable); err != nil || got.Role != "planner" {
			t.Fatalf("ReadRecord() = %#v, %v, want durable role without runtime side", got, err)
		}
	})

	t.Run("runtime session without durable session", func(t *testing.T) {
		base := shortTempDir(t)
		namespace, err := openNamespaceRoots(namespaceRoots{Runtime: base + "/runtime", State: base + "/state"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = namespace.Close() })
		path, err := namespace.RolePath("fleet", "planner")
		if err != nil {
			t.Fatal(err)
		}
		_ = path.Close()
		if err := os.RemoveAll(base + "/state/sessions"); err != nil {
			t.Fatal(err)
		}

		runtimePath, err := namespace.ExistingRuntimeRolePath("fleet", "planner")
		if err != nil {
			t.Fatalf("ExistingRuntimeRolePath() error = %v", err)
		}
		defer func() { _ = runtimePath.Close() }()
		if present, err := SocketPresent(runtimePath); err != nil || present {
			t.Fatalf("SocketPresent() = %v, %v, want factual absence", present, err)
		}
	})
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	path, err := os.MkdirTemp("/tmp", "a2-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(path); err != nil {
			t.Errorf("remove short temporary directory: %v", err)
		}
	})
	return path
}
