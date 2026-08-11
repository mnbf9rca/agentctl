// Package shim owns the per-role runtime identity and wire boundary.
package shim

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/mnbf9rca/agentctl/internal/config"
)

const (
	// RootPathMaxBytes caps every declared runtime, state, and user-config root.
	RootPathMaxBytes = 1024
	// DarwinUnixSocketPathBytes is Darwin's sockaddr_un.sun_path capacity.
	DarwinUnixSocketPathBytes = 104
	// ShimProtocolVersion is the only supported shim wire protocol version.
	ShimProtocolVersion = 1

	runtimeRootEnvironment = "AGENTCTL_RUNTIME_ROOT"
	stateRootEnvironment   = "AGENTCTL_STATE_ROOT"
)

type namespaceRoots struct {
	Runtime string
	State   string
}

// InvalidRootError reports a declared root that cannot be safely used.
type InvalidRootError struct {
	Kind   string
	Path   string
	Reason string
}

func (e *InvalidRootError) Error() string {
	return fmt.Sprintf("invalid %s root %q: %s", e.Kind, e.Path, e.Reason)
}

// RootSubstitutedError reports that a verified root pathname no longer names
// the directory descriptor retained by Namespace.
type RootSubstitutedError struct {
	Kind string
	Path string
}

func (e *RootSubstitutedError) Error() string {
	return fmt.Sprintf("%s root %q was substituted after descriptor verification", e.Kind, e.Path)
}

// FilesystemObservationError reports that an inode/type comparison could not
// be completed. It is distinct from a successful observation proving that a
// pathname was substituted.
type FilesystemObservationError struct {
	Kind      string
	Path      string
	Operation string
	Err       error
}

func (e *FilesystemObservationError) Error() string {
	return fmt.Sprintf("could not %s for %s %q: %v", e.Operation, e.Kind, e.Path, e.Err)
}

func (e *FilesystemObservationError) Unwrap() error { return e.Err }

// SocketPathTooLongError reports a resolved socket path that Darwin cannot
// represent, including its terminating NUL.
type SocketPathTooLongError struct {
	Path   string
	Length int
}

func (e *SocketPathTooLongError) Error() string {
	return fmt.Sprintf("socket path %q is %d bytes; Darwin requires fewer than %d", e.Path, e.Length, DarwinUnixSocketPathBytes)
}

// Namespace retains verified descriptors for the separately rooted volatile
// and durable trees. Paths remain exported as facts for diagnostics; mutation
// is descriptor-relative.
type Namespace struct {
	RuntimeRoot string
	StateRoot   string

	runtime *os.Root
	state   *os.Root
	mu      sync.Mutex
}

// RolePath is one validated per-role namespace. Its private descriptors keep
// subsequent lock and record operations anchored to the verified directories.
type RolePath struct {
	Session     string
	Role        string
	RuntimeRoot string
	StateRoot   string
	Lock        string
	Socket      string
	Record      string

	runtimeSession *os.Root
	stateRoles     *os.Root
	mu             sync.Mutex
}

// OpenNamespace resolves the declared environment surfaces and opens private,
// descriptor-verified runtime and durable state roots.
func OpenNamespace() (*Namespace, error) {
	runtimeOverride, runtimeSet := os.LookupEnv(runtimeRootEnvironment)
	stateOverride, stateSet := os.LookupEnv(stateRootEnvironment)
	if runtimeSet && runtimeOverride == "" {
		return nil, &InvalidRootError{Kind: "runtime", Path: runtimeOverride, Reason: runtimeRootEnvironment + " must not be empty"}
	}
	if stateSet && stateOverride == "" {
		return nil, &InvalidRootError{Kind: "state", Path: stateOverride, Reason: stateRootEnvironment + " must not be empty"}
	}

	configRoot := ""
	if !stateSet {
		home := os.Getenv("HOME")
		if err := validateRootInput("home", home); err != nil {
			return nil, err
		}
		homeRoot, err := openVerifiedPrivateRoot("home", home)
		if err != nil {
			return nil, err
		}
		_ = homeRoot.Close()
		configRoot, err = os.UserConfigDir()
		if err != nil {
			return nil, &InvalidRootError{Kind: "user-config", Reason: err.Error()}
		}
	}
	roots, err := resolveNamespaceRoots(os.Geteuid(), runtimeOverride, stateOverride, configRoot)
	if err != nil {
		return nil, err
	}
	return openResolvedNamespace(roots, runtimeSet, stateSet, configRoot)
}

func resolveNamespaceRoots(uid int, runtimeOverride, stateOverride, userConfigRoot string) (namespaceRoots, error) {
	runtimeRoot := runtimeOverride
	if runtimeRoot == "" {
		runtimeRoot = filepath.Join("/tmp", "agentctl-"+strconv.Itoa(uid), "v1")
	}
	if err := validateRootInput("runtime", runtimeRoot); err != nil {
		return namespaceRoots{}, err
	}

	stateRoot := stateOverride
	if stateRoot == "" {
		if err := validateRootInput("user-config", userConfigRoot); err != nil {
			return namespaceRoots{}, err
		}
		stateRoot = filepath.Join(userConfigRoot, "agentctl", "state-v1")
	}
	if err := validateRootInput("state", stateRoot); err != nil {
		return namespaceRoots{}, err
	}
	return namespaceRoots{Runtime: runtimeRoot, State: stateRoot}, nil
}

func validateRootInput(kind, path string) error {
	if path == "" {
		return &InvalidRootError{Kind: kind, Path: path, Reason: "must not be empty"}
	}
	if !filepath.IsAbs(path) {
		return &InvalidRootError{Kind: kind, Path: path, Reason: "must be absolute"}
	}
	if len(path) > RootPathMaxBytes {
		return &InvalidRootError{Kind: kind, Path: path, Reason: fmt.Sprintf("must be at most %d bytes", RootPathMaxBytes)}
	}
	if filepath.Clean(path) != path {
		return &InvalidRootError{Kind: kind, Path: path, Reason: "must be clean"}
	}
	return nil
}

func openResolvedNamespace(roots namespaceRoots, runtimeOverride, stateOverride bool, userConfigRoot string) (*Namespace, error) {
	return openResolvedNamespaceWithHook(roots, runtimeOverride, stateOverride, userConfigRoot, func() {})
}

func openResolvedNamespaceWithHook(
	roots namespaceRoots,
	runtimeOverride bool,
	stateOverride bool,
	userConfigRoot string,
	afterUserConfigVerified func(),
) (*Namespace, error) {
	var retainedConfig *os.Root
	if !stateOverride {
		if err := validateRootInput("user-config", userConfigRoot); err != nil {
			return nil, err
		}
		configRoot, err := openVerifiedPrivateRoot("user-config", userConfigRoot)
		if err != nil {
			return nil, err
		}
		retainedConfig = configRoot
		afterUserConfigVerified()
		if err := verifyRetainedRoot("user-config", userConfigRoot, retainedConfig); err != nil {
			_ = retainedConfig.Close()
			return nil, err
		}
	}

	var runtime *os.Root
	var err error
	if runtimeOverride {
		runtime, err = ensureExactPrivateRoot("runtime", roots.Runtime)
	} else {
		runtime, err = ensurePrivateTree("runtime", filepath.Dir(filepath.Dir(roots.Runtime)), filepath.Base(filepath.Dir(roots.Runtime)), filepath.Base(roots.Runtime))
	}
	if err != nil {
		if retainedConfig != nil {
			_ = retainedConfig.Close()
		}
		return nil, err
	}

	var state *os.Root
	if stateOverride {
		state, err = ensureExactPrivateRoot("state", roots.State)
	} else {
		state, err = ensurePrivateTreeFromRoot("state", userConfigRoot, retainedConfig, "agentctl", "state-v1")
		retainedConfig = nil
	}
	if err != nil {
		_ = runtime.Close()
		return nil, err
	}
	resolvedStateRoot, err := resolvedRetainedRoot("state", roots.State, state)
	if err != nil {
		_ = state.Close()
		_ = runtime.Close()
		return nil, err
	}
	return &Namespace{RuntimeRoot: roots.Runtime, StateRoot: resolvedStateRoot, runtime: runtime, state: state}, nil
}

func openNamespaceRoots(roots namespaceRoots) (*Namespace, error) {
	if err := validateRootInput("runtime", roots.Runtime); err != nil {
		return nil, err
	}
	if err := validateRootInput("state", roots.State); err != nil {
		return nil, err
	}
	return openResolvedNamespace(roots, true, true, "")
}

func openProductionNamespaceAt(runtimeBase string, uid int, userConfigRoot string) (*Namespace, error) {
	roots := namespaceRoots{
		Runtime: filepath.Join(runtimeBase, "agentctl-"+strconv.Itoa(uid), "v1"),
		State:   filepath.Join(userConfigRoot, "agentctl", "state-v1"),
	}
	if err := validateRootInput("runtime", roots.Runtime); err != nil {
		return nil, err
	}
	if err := validateRootInput("user-config", userConfigRoot); err != nil {
		return nil, err
	}
	if err := validateRootInput("state", roots.State); err != nil {
		return nil, err
	}
	return openResolvedNamespace(roots, false, false, userConfigRoot)
}

func ensureExactPrivateRoot(kind, path string) (*os.Root, error) {
	parent, err := openNoSymlinkDirectory(filepath.Dir(path))
	if err != nil {
		return nil, &InvalidRootError{Kind: kind, Path: path, Reason: err.Error()}
	}
	defer func() { _ = parent.Close() }()
	return ensurePrivateChild(kind, path, parent, filepath.Base(path))
}

func ensurePrivateTree(kind, base string, components ...string) (*os.Root, error) {
	current, err := openNoSymlinkDirectory(base)
	if err != nil {
		return nil, &InvalidRootError{Kind: kind, Path: base, Reason: err.Error()}
	}
	return ensurePrivateTreeFromRoot(kind, base, current, components...)
}

// ensurePrivateTreeFromRoot consumes current and returns the final retained
// child root. Every lookup and mutation is descriptor-relative.
func ensurePrivateTreeFromRoot(kind, base string, current *os.Root, components ...string) (*os.Root, error) {
	currentPath := base
	for _, component := range components {
		currentPath = filepath.Join(currentPath, component)
		next, childErr := ensurePrivateChild(kind, currentPath, current, component)
		_ = current.Close()
		if childErr != nil {
			return nil, childErr
		}
		current = next
	}
	return current, nil
}

func ensurePrivateChild(kind, fullPath string, parent *os.Root, name string) (*os.Root, error) {
	return ensurePrivateChildWithSync(kind, fullPath, parent, name, syncDirectoryRoot)
}

func openPrivateChild(kind, fullPath string, parent *os.Root, name string) (*os.Root, error) {
	pathInfo, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 {
		return nil, &InvalidRootError{Kind: kind, Path: fullPath, Reason: "must not be a symbolic link"}
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	if err := verifyPrivateDirectory(kind, fullPath, root); err != nil {
		_ = root.Close()
		return nil, err
	}
	descriptorInfo, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, &FilesystemObservationError{Kind: kind, Path: fullPath, Operation: "stat retained directory", Err: err}
	}
	if !os.SameFile(pathInfo, descriptorInfo) {
		_ = root.Close()
		return nil, &RootSubstitutedError{Kind: kind, Path: fullPath}
	}
	return root, nil
}

func ensurePrivateChildWithSync(
	kind string,
	fullPath string,
	parent *os.Root,
	name string,
	syncParent func(*os.Root) error,
) (*os.Root, error) {
	if err := parent.Mkdir(name, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, &InvalidRootError{Kind: kind, Path: fullPath, Reason: err.Error()}
	}
	pathInfo, err := parent.Lstat(name)
	if err != nil {
		return nil, &InvalidRootError{Kind: kind, Path: fullPath, Reason: err.Error()}
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 {
		return nil, &InvalidRootError{Kind: kind, Path: fullPath, Reason: "must not be a symbolic link"}
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, &InvalidRootError{Kind: kind, Path: fullPath, Reason: err.Error()}
	}
	if err := verifyPrivateDirectory(kind, fullPath, root); err != nil {
		_ = root.Close()
		return nil, err
	}
	descriptorInfo, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, &FilesystemObservationError{Kind: kind, Path: fullPath, Operation: "stat retained directory", Err: err}
	}
	if !os.SameFile(pathInfo, descriptorInfo) {
		_ = root.Close()
		return nil, &RootSubstitutedError{Kind: kind, Path: fullPath}
	}
	if err := syncParent(parent); err != nil {
		_ = root.Close()
		return nil, &InvalidRootError{Kind: kind, Path: fullPath, Reason: fmt.Sprintf("sync parent directory: %v", err)}
	}
	return root, nil
}

func openNoSymlinkDirectory(path string) (*os.Root, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("path is not absolute")
	}
	return os.OpenRoot(path)
}

func openVerifiedPrivateRoot(kind, path string) (*os.Root, error) {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, &InvalidRootError{Kind: kind, Path: path, Reason: err.Error()}
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 {
		return nil, &InvalidRootError{Kind: kind, Path: path, Reason: "must not be a symbolic link"}
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, &InvalidRootError{Kind: kind, Path: path, Reason: err.Error()}
	}
	if err := verifyPrivateDirectory(kind, path, root); err != nil {
		_ = root.Close()
		return nil, err
	}
	descriptorInfo, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, &FilesystemObservationError{Kind: kind, Path: path, Operation: "stat retained directory", Err: err}
	}
	if !os.SameFile(pathInfo, descriptorInfo) {
		_ = root.Close()
		return nil, &RootSubstitutedError{Kind: kind, Path: path}
	}
	return root, nil
}

func resolvedRetainedRoot(kind, path string, root *os.Root) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", &InvalidRootError{Kind: kind, Path: path, Reason: err.Error()}
	}
	if err := validateRootInput(kind, resolved); err != nil {
		return "", err
	}
	if err := verifyRetainedRoot(kind, resolved, root); err != nil {
		return "", err
	}
	return resolved, nil
}

func syncDirectoryRoot(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

func verifyPrivateDirectory(kind, path string, root *os.Root) error {
	info, err := root.Stat(".")
	if err != nil {
		return &InvalidRootError{Kind: kind, Path: path, Reason: err.Error()}
	}
	if !info.IsDir() {
		return &InvalidRootError{Kind: kind, Path: path, Reason: "descriptor is not a directory"}
	}
	if info.Mode().Perm() != 0o700 {
		return &InvalidRootError{Kind: kind, Path: path, Reason: fmt.Sprintf("mode is %04o; expected 0700", info.Mode().Perm())}
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return &InvalidRootError{Kind: kind, Path: path, Reason: "descriptor stat has unexpected type"}
	}
	if int(stat.Uid) != os.Geteuid() {
		return &InvalidRootError{Kind: kind, Path: path, Reason: fmt.Sprintf("owner uid is %d; expected %d", stat.Uid, os.Geteuid())}
	}
	return nil
}

func verifyPrivateArtifact(file *os.File, path, kind string) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s %q descriptor is not a regular file", kind, path)
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("%s %q mode is %04o; expected 0600", kind, path, info.Mode().Perm())
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("%s %q is not owned by uid %d", kind, path, os.Geteuid())
	}
	return nil
}

func verifyRetainedRoot(kind, path string, root *os.Root) error {
	descriptorInfo, err := root.Stat(".")
	if err != nil {
		return &FilesystemObservationError{Kind: kind, Path: path, Operation: "stat retained directory", Err: err}
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return &FilesystemObservationError{Kind: kind, Path: path, Operation: "lstat declared path", Err: err}
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(descriptorInfo, pathInfo) {
		return &RootSubstitutedError{Kind: kind, Path: path}
	}
	return nil
}

// RolePath validates all inputs and the complete Darwin socket path before it
// creates any per-session directory.
func (n *Namespace) RolePath(session, role string) (*RolePath, error) {
	return n.rolePath(session, role, true)
}

// ExistingRolePath opens one already-created role namespace without creating
// directories. Clients use it so observation remains read-only.
func (n *Namespace) ExistingRolePath(session, role string) (*RolePath, error) {
	return n.rolePath(session, role, false)
}

// ExistingRuntimeRolePath opens only the already-created volatile session
// side. Status uses it before any durable enumeration so a missing lockfile
// cannot silently acquire anchored confidence.
func (n *Namespace) ExistingRuntimeRolePath(session, role string) (*RolePath, error) {
	lockPath, socketPath, recordPath, err := n.validatedRolePaths(session, role)
	if err != nil {
		return nil, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.runtime == nil {
		return nil, errors.New("namespace is closed")
	}
	if err := verifyRetainedRoot("runtime", n.RuntimeRoot, n.runtime); err != nil {
		return nil, err
	}
	runtimeSession, err := openPrivateChild("runtime", filepath.Join(n.RuntimeRoot, session), n.runtime, session)
	if err != nil {
		return nil, err
	}
	return &RolePath{
		Session: session, Role: role, RuntimeRoot: n.RuntimeRoot, StateRoot: n.StateRoot,
		Lock: lockPath, Socket: socketPath, Record: recordPath, runtimeSession: runtimeSession,
	}, nil
}

// ListRuntimeRoles returns the validated role names that have volatile
// lockfile anchors in one already-created runtime session. It never consults
// or creates the independently resolved durable tree.
func (n *Namespace) ListRuntimeRoles(session string) ([]string, error) {
	if err := config.ValidateSessionName(session); err != nil {
		return nil, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.runtime == nil {
		return nil, errors.New("namespace is closed")
	}
	if err := verifyRetainedRoot("runtime", n.RuntimeRoot, n.runtime); err != nil {
		return nil, err
	}
	runtimeSession, err := openPrivateChild("runtime", filepath.Join(n.RuntimeRoot, session), n.runtime, session)
	if err != nil {
		return nil, err
	}
	defer func() { _ = runtimeSession.Close() }()
	directory, err := runtimeSession.Open(".")
	if err != nil {
		return nil, err
	}
	names, readErr := directory.Readdirnames(-1)
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	roles := make([]string, 0, len(names))
	for _, name := range names {
		if !strings.HasSuffix(name, ".lock") {
			continue
		}
		role := strings.TrimSuffix(name, ".lock")
		if err := config.ValidateRoleName(role); err != nil {
			return nil, fmt.Errorf("runtime lockfile %q has invalid role name: %w", name, err)
		}
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles, nil
}

// ExistingDurableRolePath opens only the already-created durable role side.
// It permits explicitly unanchored observation when the volatile tree is gone.
func (n *Namespace) ExistingDurableRolePath(session, role string) (*RolePath, error) {
	lockPath, socketPath, recordPath, err := n.validatedRolePaths(session, role)
	if err != nil {
		return nil, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.state == nil {
		return nil, errors.New("namespace is closed")
	}
	if err := verifyRetainedRoot("state", n.StateRoot, n.state); err != nil {
		return nil, err
	}
	stateSessions, err := openPrivateChild("state", filepath.Join(n.StateRoot, "sessions"), n.state, "sessions")
	if err != nil {
		return nil, err
	}
	stateSession, err := openPrivateChild("state", filepath.Join(n.StateRoot, "sessions", session), stateSessions, session)
	_ = stateSessions.Close()
	if err != nil {
		return nil, err
	}
	stateRoles, err := openPrivateChild("state", filepath.Join(n.StateRoot, "sessions", session, "roles"), stateSession, "roles")
	_ = stateSession.Close()
	if err != nil {
		return nil, err
	}
	return &RolePath{
		Session: session, Role: role, RuntimeRoot: n.RuntimeRoot, StateRoot: n.StateRoot,
		Lock: lockPath, Socket: socketPath, Record: recordPath, stateRoles: stateRoles,
	}, nil
}

func (n *Namespace) validatedRolePaths(session, role string) (string, string, string, error) {
	if err := config.ValidateSessionName(session); err != nil {
		return "", "", "", err
	}
	if err := config.ValidateRoleName(role); err != nil {
		return "", "", "", err
	}
	lockPath := filepath.Join(n.RuntimeRoot, session, role+".lock")
	socketPath := filepath.Join(n.RuntimeRoot, session, role+".sock")
	recordPath := filepath.Join(n.StateRoot, "sessions", session, "roles", role+".json")
	if len(socketPath) >= DarwinUnixSocketPathBytes {
		return "", "", "", &SocketPathTooLongError{Path: socketPath, Length: len(socketPath)}
	}
	return lockPath, socketPath, recordPath, nil
}

// SocketPresent observes the exact role socket entry through the retained
// runtime-session descriptor. A non-socket artifact is an observation error,
// never socket presence.
type SocketTopologyError struct {
	Path   string
	Reason string
}

func (e *SocketTopologyError) Error() string {
	return fmt.Sprintf("role socket topology at %q is invalid: %s", e.Path, e.Reason)
}

func SocketPresent(path *RolePath) (bool, error) {
	path.mu.Lock()
	defer path.mu.Unlock()
	if path.runtimeSession == nil {
		return false, errors.New("runtime role path is closed")
	}
	if err := verifyRetainedRoot("runtime-session", filepath.Dir(path.Socket), path.runtimeSession); err != nil {
		return false, err
	}
	info, err := path.runtimeSession.Lstat(path.Role + ".sock")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return false, &SocketTopologyError{Path: path.Socket, Reason: "contains a non-socket artifact"}
	}
	return true, nil
}

func (n *Namespace) rolePath(session, role string, create bool) (*RolePath, error) {
	lockPath, socketPath, recordPath, err := n.validatedRolePaths(session, role)
	if err != nil {
		return nil, err
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	if n.runtime == nil || n.state == nil {
		return nil, errors.New("namespace is closed")
	}
	if err := verifyRetainedRoot("runtime", n.RuntimeRoot, n.runtime); err != nil {
		return nil, err
	}
	if err := verifyRetainedRoot("state", n.StateRoot, n.state); err != nil {
		return nil, err
	}
	openChild := openPrivateChild
	if create {
		openChild = ensurePrivateChild
	}
	runtimeSession, err := openChild("runtime", filepath.Join(n.RuntimeRoot, session), n.runtime, session)
	if err != nil {
		return nil, err
	}
	stateSessions, err := openChild("state", filepath.Join(n.StateRoot, "sessions"), n.state, "sessions")
	if err != nil {
		_ = runtimeSession.Close()
		return nil, err
	}
	stateSession, err := openChild("state", filepath.Join(n.StateRoot, "sessions", session), stateSessions, session)
	_ = stateSessions.Close()
	if err != nil {
		_ = runtimeSession.Close()
		return nil, err
	}
	stateRoles, err := openChild("state", filepath.Join(n.StateRoot, "sessions", session, "roles"), stateSession, "roles")
	_ = stateSession.Close()
	if err != nil {
		_ = runtimeSession.Close()
		return nil, err
	}
	return &RolePath{
		Session: session, Role: role,
		RuntimeRoot: n.RuntimeRoot, StateRoot: n.StateRoot,
		Lock: lockPath, Socket: socketPath, Record: recordPath,
		runtimeSession: runtimeSession, stateRoles: stateRoles,
	}, nil
}

// Close releases the namespace's retained directory descriptors.
func (n *Namespace) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	var errs []error
	if n.runtime != nil {
		errs = append(errs, n.runtime.Close())
		n.runtime = nil
	}
	if n.state != nil {
		errs = append(errs, n.state.Close())
		n.state = nil
	}
	return errors.Join(errs...)
}

// Close releases the role's retained directory descriptors.
func (p *RolePath) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var errs []error
	if p.runtimeSession != nil {
		errs = append(errs, p.runtimeSession.Close())
		p.runtimeSession = nil
	}
	if p.stateRoles != nil {
		errs = append(errs, p.stateRoles.Close())
		p.stateRoles = nil
	}
	return errors.Join(errs...)
}
