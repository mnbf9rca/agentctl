package shim

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func TestNamespaceAcceptsExactRootCapAndUsesHOMEThroughUserConfigDir(t *testing.T) {
	exactCap := "/" + strings.Repeat("r", RootPathMaxBytes-1)
	if _, err := resolveNamespaceRoots(501, exactCap, "/tmp/state", "/tmp/config"); err != nil {
		t.Fatalf("exact %d-byte root rejected: %v", RootPathMaxBytes, err)
	}

	parent := shortTempDir(t)
	home := filepath.Join(parent, "home")
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
	if got, want := namespace.StateRoot, filepath.Join(configRoot, "agentctl", "state-v1"); got != want {
		t.Fatalf("HOME-derived StateRoot = %q, want %q", got, want)
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

func TestNamespaceRefusesPredictableRuntimePrecreation(t *testing.T) {
	parent := t.TempDir()
	uidRoot := filepath.Join(parent, "agentctl-501")
	if err := os.Mkdir(uidRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := openProductionNamespaceAt(parent, 501, filepath.Join(parent, "config"))
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
	parent := shortTempDir(t)
	runtimeRoot := filepath.Join(parent, "runtime")
	namespace, err := openNamespaceRoots(namespaceRoots{Runtime: runtimeRoot, State: filepath.Join(parent, "state")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = namespace.Close() })

	original := filepath.Join(parent, "runtime-original")
	if err := os.Rename(runtimeRoot, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(runtimeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = namespace.RolePath("s", "r")
	var substituted *RootSubstitutedError
	if !errors.As(err, &substituted) {
		t.Fatalf("RolePath error = %T %v, want *RootSubstitutedError", err, err)
	}
	for _, root := range []string{runtimeRoot, original} {
		if _, statErr := os.Stat(filepath.Join(root, "s")); !os.IsNotExist(statErr) {
			t.Fatalf("role mutation under %q after substitution: stat error = %v", root, statErr)
		}
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
