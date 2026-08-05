// Package fleet launches configured agent fleets in tmux.
package fleet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/harness"
	"github.com/mnbf9rca/agentctl/internal/preflight"
	"github.com/mnbf9rca/agentctl/internal/shellq"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
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

// SessionExistsError reports that the requested session name is already in use.
type SessionExistsError struct{ Name string }

func (e *SessionExistsError) Error() string {
	return fmt.Sprintf("session %q already exists", e.Name)
}

// CreationError reports malformed successful new-session output. It is a
// pre-ownership error because no typed session ID was obtained.
type CreationError struct {
	Session string
	Cause   error
}

func (e *CreationError) Error() string {
	return fmt.Sprintf("%v; a session named %s may exist; inspect with tmux ls", e.Cause, e.Session)
}

func (e *CreationError) Unwrap() error { return e.Cause }

// LaunchError reports a post-ownership launch failure and its one cleanup
// attempt. Its cause is the failure that stopped the launch.
type LaunchError struct {
	Role       string
	Session    string
	Cause      error
	CleanupErr error
}

func (e *LaunchError) Error() string {
	if e.CleanupErr != nil {
		return fmt.Sprintf("failed to launch %s; failed to remove incomplete session %s: %v (launch failure: %v)", e.Role, e.Session, e.CleanupErr, e.Cause)
	}
	return fmt.Sprintf("failed to launch %s; removed incomplete session %s: %v", e.Role, e.Session, e.Cause)
}

func (e *LaunchError) Unwrap() error { return e.Cause }

// Dependencies supplies launch seams. Nil functions use production defaults.
type Dependencies struct {
	LookPath preflight.LookPathFunc
	Getwd    func() (string, error)
	Stat     func(string) (fs.FileInfo, error)
	Now      func() time.Time
	Sleep    func(time.Duration)
	Stderr   io.Writer
}

// Launcher coordinates preflight and tmux fleet creation.
type Launcher struct {
	tmux     tmuxx.Client
	lookPath preflight.LookPathFunc
	getwd    func() (string, error)
	stat     func(string) (fs.FileInfo, error)
	now      func() time.Time
	sleep    func(time.Duration)
	stderr   io.Writer
}

const (
	processPollTimeout  = 5 * time.Second
	processPollInterval = 100 * time.Millisecond
	envSession          = "AGENTCTL_SESSION"
	envRole             = "AGENTCTL_ROLE"
	envManaged          = "AGENTCTL_MANAGED"
)

// Session and window metadata option names (§6.5).
const (
	optionManaged   = "@agentctl_managed"
	optionVersion   = "@agentctl_version"
	optionRoles     = "@agentctl_roles"
	optionFleet     = "@agentctl_fleet"
	optionDirectory = "@agentctl_dir"
	optionRole      = "@agentctl_role"
	optionHarness   = "@agentctl_harness"
	optionModel     = "@agentctl_model"
	optionEffort    = "@agentctl_effort"
	optionProcess   = "@agentctl_process"
)

// EncodeFleet renders per-role configuration as comma-joined
// role:harness:model:effort quads in roster order. Every field is
// charset-bound (§7), so neither separator can occur inside a field and a
// defaulted model or effort renders as an empty field.
func EncodeFleet(roles []config.RoleConfig) string {
	entries := make([]string, len(roles))
	for index, role := range roles {
		entries[index] = role.Name + ":" + string(role.Harness) + ":" + role.Model + ":" + role.Effort
	}
	return strings.Join(entries, ",")
}

// New constructs a launcher with production defaults for omitted dependencies.
func New(runner tmuxx.Runner, dependencies Dependencies) Launcher {
	if dependencies.LookPath == nil {
		dependencies.LookPath = exec.LookPath
	}
	if dependencies.Getwd == nil {
		dependencies.Getwd = os.Getwd
	}
	if dependencies.Stat == nil {
		dependencies.Stat = os.Stat
	}
	if dependencies.Now == nil {
		dependencies.Now = time.Now
	}
	if dependencies.Sleep == nil {
		dependencies.Sleep = time.Sleep
	}
	if dependencies.Stderr == nil {
		dependencies.Stderr = io.Discard
	}
	return Launcher{
		tmux:     tmuxx.New(runner),
		lookPath: dependencies.LookPath,
		getwd:    dependencies.Getwd,
		stat:     dependencies.Stat,
		now:      dependencies.Now,
		sleep:    dependencies.Sleep,
		stderr:   dependencies.Stderr,
	}
}

// Launch performs preflight before taking any tmux action and returns the exact
// session created by this invocation. A nil directory uses the invocation
// working directory; a non-nil value must name a directory.
func (l Launcher) Launch(ctx context.Context, session string, fleet config.FleetConfig, directory *string) (tmuxx.Session, error) {
	if len(fleet.Roles) == 0 {
		return tmuxx.Session{}, fmt.Errorf("fleet must contain at least one role")
	}
	if err := preflight.CheckExecutables(fleet, l.lookPath); err != nil {
		return tmuxx.Session{}, err
	}
	directoryName, err := l.resolveDirectory(directory)
	if err != nil {
		return tmuxx.Session{}, err
	}
	sessions, err := l.tmux.ListSessions(ctx)
	if errors.Is(err, context.Canceled) {
		return tmuxx.Session{}, context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return tmuxx.Session{}, context.DeadlineExceeded
	}
	if err == nil {
		for _, existing := range sessions {
			if existing.Name == session {
				return tmuxx.Session{}, &SessionExistsError{Name: session}
			}
		}
	}
	first := fleet.Roles[0]
	createdSession, err := l.newSession(ctx, session, first, directoryName)
	if err != nil {
		if errors.Is(err, tmuxx.ErrCreationOutput) {
			return tmuxx.Session{}, &CreationError{Session: session, Cause: err}
		}
		return tmuxx.Session{}, err
	}
	if err := l.stampSession(ctx, createdSession.SessionID, fleet.Roles, directoryName); err != nil {
		return tmuxx.Session{}, l.rollback(ctx, createdSession.SessionID, session, first.Name, err)
	}
	if err := l.stampWindow(ctx, createdSession.WindowID, createdSession.PanePID, first); err != nil {
		return tmuxx.Session{}, l.rollback(ctx, createdSession.SessionID, session, first.Name, err)
	}
	l.clearSessionIdentity(ctx, createdSession.SessionID)

	for _, role := range fleet.Roles[1:] {
		createdWindow, err := l.newWindow(ctx, createdSession.SessionID, session, role, directoryName)
		if err != nil {
			return tmuxx.Session{}, l.rollback(ctx, createdSession.SessionID, session, role.Name, err)
		}
		if err := l.stampWindow(ctx, createdWindow.WindowID, createdWindow.PanePID, role); err != nil {
			return tmuxx.Session{}, l.rollback(ctx, createdSession.SessionID, session, role.Name, err)
		}
	}
	return tmuxx.Session{ID: createdSession.SessionID, Name: session}, nil
}

func (l Launcher) clearSessionIdentity(ctx context.Context, sessionID tmuxx.SessionID) {
	for _, name := range []string{envSession, envRole, envManaged} {
		if err := l.tmux.ClearSessionEnvironment(ctx, sessionID, name); err != nil {
			fmt.Fprintf(l.stderr, "agentctl: could not clear %s from the tmux session environment; windows created by hand may inherit the first role's identity: %v\n", name, err)
		}
	}
}

func (l Launcher) newSession(ctx context.Context, session string, role config.RoleConfig, directory string) (tmuxx.CreatedSession, error) {
	command, err := agentCommand(session, role)
	if err != nil {
		return tmuxx.CreatedSession{}, err
	}
	return l.tmux.NewSession(ctx, session, role.Name, directory, command, identityEnvironment(session, role.Name))
}

func (l Launcher) newWindow(ctx context.Context, sessionID tmuxx.SessionID, session string, role config.RoleConfig, directory string) (tmuxx.CreatedWindow, error) {
	command, err := agentCommand(session, role)
	if err != nil {
		return tmuxx.CreatedWindow{}, err
	}
	return l.tmux.NewWindow(ctx, sessionID, role.Name, directory, command, identityEnvironment(session, role.Name))
}

// identityEnvironment names the fleet and role a window belongs to, so an agent
// (or human) in that pane can read its own identity. Session and role have
// already passed config validation by the time a window is created.
//
// The variables are informational. agentctl never reads them back when deciding
// what to control, kill, or report on: that stays with the @agentctl_* tmux
// options and the fail-closed target chain, because a same-user process can
// forge either (see SECURITY.md).
func identityEnvironment(session, role string) []tmuxx.EnvVar {
	return []tmuxx.EnvVar{
		{Name: envSession, Value: session},
		{Name: envRole, Value: role},
		{Name: envManaged, Value: "1"},
	}
}

func agentCommand(session string, role config.RoleConfig) (string, error) {
	argv, err := harness.AgentArgv(session, role.Name, string(role.Harness), harness.Options{
		Model:  role.Model,
		Effort: role.Effort,
	})
	if err != nil {
		return "", err
	}
	return "exec " + shellq.Join(argv), nil
}

func (l Launcher) stampSession(ctx context.Context, sessionID tmuxx.SessionID, roles []config.RoleConfig, directory string) error {
	if err := l.tmux.SetSessionOption(ctx, sessionID, optionManaged, "1"); err != nil {
		return err
	}
	if err := l.tmux.SetSessionOption(ctx, sessionID, optionVersion, "1"); err != nil {
		return err
	}
	roleNames := make([]string, len(roles))
	for index, role := range roles {
		roleNames[index] = role.Name
	}
	if err := l.tmux.SetSessionOption(ctx, sessionID, optionRoles, strings.Join(roleNames, ",")); err != nil {
		return err
	}
	if err := l.tmux.SetSessionOption(ctx, sessionID, optionFleet, EncodeFleet(roles)); err != nil {
		return err
	}
	return l.tmux.SetSessionOption(ctx, sessionID, optionDirectory, directory)
}

func (l Launcher) stampWindow(ctx context.Context, windowID tmuxx.WindowID, panePID int, role config.RoleConfig) error {
	for _, option := range []struct{ name, value string }{
		{name: optionManaged, value: "1"},
		{name: optionRole, value: role.Name},
		{name: optionHarness, value: string(role.Harness)},
		{name: optionModel, value: role.Model},
		{name: optionEffort, value: role.Effort},
	} {
		if err := l.tmux.SetWindowOption(ctx, windowID, option.name, option.value); err != nil {
			return err
		}
	}
	process, err := l.processBaseline(ctx, panePID)
	if err != nil {
		return err
	}
	return l.tmux.SetWindowOption(ctx, windowID, optionProcess, process)
}

func (l Launcher) processBaseline(ctx context.Context, panePID int) (string, error) {
	deadline := l.now().Add(processPollTimeout)
	for {
		process, err := l.tmux.ProcessName(ctx, panePID)
		if err == nil && process != "amq" {
			return process, nil
		}
		if err != nil && !errors.Is(err, tmuxx.ErrProcessUnavailable) {
			return "", err
		}
		if !l.now().Before(deadline) {
			return "", fmt.Errorf("process identity did not become available within %s", processPollTimeout)
		}
		l.sleep(processPollInterval)
	}
}

func (l Launcher) rollback(ctx context.Context, sessionID tmuxx.SessionID, session, role string, cause error) error {
	return &LaunchError{
		Role:       role,
		Session:    session,
		Cause:      cause,
		CleanupErr: l.tmux.KillSession(ctx, sessionID),
	}
}

func (l Launcher) resolveDirectory(directory *string) (string, error) {
	if directory == nil {
		return l.getwd()
	}
	if *directory == "" {
		return "", &DirectoryError{Path: *directory, Err: fs.ErrNotExist}
	}
	resolved, err := filepath.Abs(*directory)
	if err != nil {
		return "", &DirectoryError{Path: *directory, Err: err}
	}
	{
		info, err := l.stat(resolved)
		if err != nil {
			return "", &DirectoryError{Path: *directory, Err: err}
		}
		if !info.IsDir() {
			return "", &DirectoryError{Path: *directory}
		}
	}
	return resolved, nil
}
