// Package fleet launches configured agent fleets in tmux.
package fleet

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"

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

// Dependencies supplies launch seams. Nil functions use production defaults.
type Dependencies struct {
	LookPath preflight.LookPathFunc
	Getwd    func() (string, error)
	Stat     func(string) (fs.FileInfo, error)
}

// Launcher coordinates preflight and tmux fleet creation.
type Launcher struct {
	tmux     tmuxx.Client
	lookPath preflight.LookPathFunc
	getwd    func() (string, error)
	stat     func(string) (fs.FileInfo, error)
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
	return Launcher{
		tmux:     tmuxx.New(runner),
		lookPath: dependencies.LookPath,
		getwd:    dependencies.Getwd,
		stat:     dependencies.Stat,
	}
}

// Launch performs preflight before taking any tmux action.
func (l Launcher) Launch(ctx context.Context, session string, fleet config.FleetConfig, directory string) error {
	if err := preflight.CheckExecutables(fleet, l.lookPath); err != nil {
		return err
	}
	directory, err := l.resolveDirectory(directory)
	if err != nil {
		return err
	}
	sessions, err := l.tmux.ListSessions(ctx)
	if err != nil {
		return err
	}
	for _, existing := range sessions {
		if existing.Name == session {
			return &SessionExistsError{Name: session}
		}
	}
	if len(fleet.Roles) == 0 {
		return fmt.Errorf("fleet must contain at least one role")
	}

	first := fleet.Roles[0]
	createdSession, err := l.newSession(ctx, session, first, directory)
	if err != nil {
		return err
	}
	if err := l.stampSession(ctx, createdSession.SessionID, fleet.Roles); err != nil {
		return err
	}
	if err := l.stampWindow(ctx, createdSession.WindowID, createdSession.PanePID, first); err != nil {
		return err
	}

	for _, role := range fleet.Roles[1:] {
		createdWindow, err := l.newWindow(ctx, createdSession.SessionID, session, role, directory)
		if err != nil {
			return err
		}
		if err := l.stampWindow(ctx, createdWindow.WindowID, createdWindow.PanePID, role); err != nil {
			return err
		}
	}
	return nil
}

func (l Launcher) newSession(ctx context.Context, session string, role config.RoleConfig, directory string) (tmuxx.CreatedSession, error) {
	command, err := agentCommand(session, role)
	if err != nil {
		return tmuxx.CreatedSession{}, err
	}
	return l.tmux.NewSession(ctx, session, role.Name, directory, command)
}

func (l Launcher) newWindow(ctx context.Context, sessionID tmuxx.SessionID, session string, role config.RoleConfig, directory string) (tmuxx.CreatedWindow, error) {
	command, err := agentCommand(session, role)
	if err != nil {
		return tmuxx.CreatedWindow{}, err
	}
	return l.tmux.NewWindow(ctx, sessionID, role.Name, directory, command)
}

func agentCommand(session string, role config.RoleConfig) (string, error) {
	argv, err := harness.AgentArgv(session, role.Name, string(role.Harness), role.Model)
	if err != nil {
		return "", err
	}
	return "exec " + shellq.Join(argv), nil
}

func (l Launcher) stampSession(ctx context.Context, sessionID tmuxx.SessionID, roles []config.RoleConfig) error {
	if err := l.tmux.SetSessionOption(ctx, sessionID, "@agentctl_managed", "1"); err != nil {
		return err
	}
	if err := l.tmux.SetSessionOption(ctx, sessionID, "@agentctl_version", "1"); err != nil {
		return err
	}
	roleNames := make([]string, len(roles))
	for index, role := range roles {
		roleNames[index] = role.Name
	}
	return l.tmux.SetSessionOption(ctx, sessionID, "@agentctl_roles", strings.Join(roleNames, ","))
}

func (l Launcher) stampWindow(ctx context.Context, windowID tmuxx.WindowID, panePID int, role config.RoleConfig) error {
	for _, option := range []struct{ name, value string }{
		{name: "@agentctl_managed", value: "1"},
		{name: "@agentctl_role", value: role.Name},
		{name: "@agentctl_harness", value: string(role.Harness)},
		{name: "@agentctl_model", value: role.Model},
	} {
		if err := l.tmux.SetWindowOption(ctx, windowID, option.name, option.value); err != nil {
			return err
		}
	}
	process, err := l.tmux.ProcessName(ctx, panePID)
	if err != nil {
		return err
	}
	return l.tmux.SetWindowOption(ctx, windowID, "@agentctl_process", process)
}

func (l Launcher) resolveDirectory(directory string) (string, error) {
	if directory == "" {
		return l.getwd()
	}
	{
		info, err := l.stat(directory)
		if err != nil {
			return "", &DirectoryError{Path: directory, Err: err}
		}
		if !info.IsDir() {
			return "", &DirectoryError{Path: directory}
		}
	}
	return directory, nil
}
