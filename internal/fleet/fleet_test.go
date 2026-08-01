package fleet

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"testing"
	"time"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/preflight"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

type testFileInfo struct{ mode fs.FileMode }

func (info testFileInfo) Name() string       { return "test" }
func (info testFileInfo) Size() int64        { return 0 }
func (info testFileInfo) Mode() fs.FileMode  { return info.mode }
func (info testFileInfo) ModTime() time.Time { return time.Time{} }
func (info testFileInfo) IsDir() bool        { return info.mode.IsDir() }
func (info testFileInfo) Sys() any           { return nil }

func TestLaunchMissingDependencyReturnsPreflightErrorBeforeTmux(t *testing.T) {
	runner := tmuxx.NewFakeRunner()
	var lookedUp []string
	launcher := New(runner, Dependencies{
		LookPath: func(name string) (string, error) {
			lookedUp = append(lookedUp, name)
			if name == "amq" {
				return "", errors.New("not found")
			}
			return "/bin/" + name, nil
		},
	})

	err := launcher.Launch(context.Background(), "epic123", config.FleetConfig{Roles: []config.RoleConfig{
		{Name: "planner", Harness: config.HarnessClaude},
		{Name: "reviewer", Harness: config.HarnessClaude},
		{Name: "worker", Harness: config.HarnessCodex},
	}}, "")

	var missing *preflight.MissingExecutableError
	if !errors.As(err, &missing) {
		t.Fatalf("Launch() error = %v, want *preflight.MissingExecutableError", err)
	}
	if missing.Name != "amq" {
		t.Fatalf("missing executable = %q, want amq", missing.Name)
	}
	if want := []string{"tmux", "amq"}; !reflect.DeepEqual(lookedUp, want) {
		t.Fatalf("LookPath calls = %#v, want %#v", lookedUp, want)
	}
	if len(runner.Calls) != 0 {
		t.Fatalf("Runner calls = %#v, want none", runner.Calls)
	}
}

func TestLaunchRejectsInvalidExplicitDirectoryBeforeTmux(t *testing.T) {
	tests := []struct {
		name string
		stat func(string) (fs.FileInfo, error)
	}{
		{
			name: "missing",
			stat: func(string) (fs.FileInfo, error) {
				return nil, fs.ErrNotExist
			},
		},
		{
			name: "regular file",
			stat: func(string) (fs.FileInfo, error) {
				return testFileInfo{mode: 0o644}, nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner()
			launcher := New(runner, Dependencies{
				LookPath: presentExecutable,
				Stat:     tt.stat,
			})

			err := launcher.Launch(context.Background(), "epic123", oneRoleFleet(), "/not-a-directory")

			var invalid *DirectoryError
			if !errors.As(err, &invalid) {
				t.Fatalf("Launch() error = %v, want *DirectoryError", err)
			}
			if invalid.Path != "/not-a-directory" {
				t.Fatalf("DirectoryError.Path = %q, want /not-a-directory", invalid.Path)
			}
			if len(runner.Calls) != 0 {
				t.Fatalf("Runner calls = %#v, want none", runner.Calls)
			}
		})
	}
}

func TestLaunchRejectsOnlyExactExistingSession(t *testing.T) {
	runner := tmuxx.NewFakeRunner(tmuxx.Response{Stdout: []byte("$1\tepic1234\n$2\tEpic123\n$3\tepic123\n$4\txepic123\n")})
	launcher := New(runner, Dependencies{
		LookPath: presentExecutable,
		Getwd:    func() (string, error) { return "/invocation", nil },
	})

	err := launcher.Launch(context.Background(), "epic123", oneRoleFleet(), "")

	var exists *SessionExistsError
	if !errors.As(err, &exists) {
		t.Fatalf("Launch() error = %v, want *SessionExistsError", err)
	}
	if exists.Name != "epic123" {
		t.Fatalf("SessionExistsError.Name = %q, want epic123", exists.Name)
	}
	assertCalls(t, runner, tmuxx.Call{Executable: "tmux", Args: []string{
		"list-sessions", "-F", "#{session_id}\t#{session_name}",
	}})
}

func TestLaunchChecksRequestedHarnessesInFirstSeenDeduplicatedOrder(t *testing.T) {
	runner := tmuxx.NewFakeRunner(tmuxx.Response{Stdout: []byte("$9\tepic123\n")})
	var lookedUp []string
	launcher := New(runner, Dependencies{
		LookPath: func(name string) (string, error) {
			lookedUp = append(lookedUp, name)
			return "/bin/" + name, nil
		},
		Getwd: func() (string, error) { return "/invocation", nil },
	})
	fleet := config.FleetConfig{Roles: []config.RoleConfig{
		{Name: "first", Harness: config.HarnessCodex},
		{Name: "second", Harness: config.HarnessClaude},
		{Name: "third", Harness: config.HarnessCodex},
		{Name: "fourth", Harness: config.HarnessClaude},
	}}

	err := launcher.Launch(context.Background(), "epic123", fleet, "")

	var exists *SessionExistsError
	if !errors.As(err, &exists) {
		t.Fatalf("Launch() error = %v, want *SessionExistsError", err)
	}
	if want := []string{"tmux", "amq", "codex", "claude"}; !reflect.DeepEqual(lookedUp, want) {
		t.Fatalf("LookPath calls = %#v, want %#v", lookedUp, want)
	}
	assertCalls(t, runner, tmuxx.Call{Executable: "tmux", Args: []string{
		"list-sessions", "-F", "#{session_id}\t#{session_name}",
	}})
}

func TestLaunchDoesNotTreatPrefixOrCaseCanariesAsExistingSession(t *testing.T) {
	tests := []struct {
		name     string
		sessions string
	}{
		{name: "prefix", sessions: "$1\tepic1234\n"},
		{name: "case", sessions: "$2\tEpic123\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner(successfulOneRoleResponses(tt.sessions)...)
			launcher := New(runner, Dependencies{
				LookPath: presentExecutable,
				Getwd:    func() (string, error) { return "/invocation", nil },
			})

			if err := launcher.Launch(context.Background(), "epic123", oneRoleFleet(), ""); err != nil {
				t.Fatalf("Launch() error = %v, want successful launch", err)
			}
			if len(runner.Calls) != 11 {
				t.Fatalf("Runner call count = %d, want 11 (successful launch)", len(runner.Calls))
			}
			if got := runner.Calls[1]; got.Executable != "tmux" || got.Args[0] != "new-session" {
				t.Fatalf("first post-list call = %#v, want new-session", got)
			}
		})
	}
}

func TestLaunchOneRoleCreatesAndStampsReturnedIDs(t *testing.T) {
	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("$17\t@23\t%42\t4242\n")},
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("claude\n")},
		tmuxx.Response{},
	)
	launcher := New(runner, Dependencies{
		LookPath: presentExecutable,
		Getwd:    func() (string, error) { return "/invocation cwd", nil },
	})
	fleet := config.FleetConfig{Roles: []config.RoleConfig{{
		Name: "planner", Harness: config.HarnessClaude, Model: "fable",
	}}}

	if err := launcher.Launch(context.Background(), "epic123", fleet, ""); err != nil {
		t.Fatalf("Launch() error = %v", err)
	}

	assertCalls(t, runner,
		tmuxx.Call{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}},
		tmuxx.Call{Executable: "tmux", Args: []string{
			"new-session", "-d", "-s", "epic123", "-n", "planner", "-c", "/invocation cwd",
			"-P", "-F", "#{session_id}\t#{window_id}\t#{pane_id}\t#{pane_pid}", "--",
			"exec 'amq' 'coop' 'exec' '--session' 'epic123' '--me' 'planner' 'claude' '--' '--model' 'fable'",
		}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_managed", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_version", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_roles", "planner"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_managed", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_role", "planner"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_harness", "claude"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_model", "fable"}},
		tmuxx.Call{Executable: "ps", Args: []string{"-o", "comm=", "-p", "4242"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_process", "claude"}},
	)
}

func TestLaunchMultipleRolesUsesCreationIDsAndDeclarationOrder(t *testing.T) {
	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("$1\tunrelated\n$7\tepic1234\n")},
		tmuxx.Response{Stdout: []byte("$17\t@23\t%42\t4242\n")},
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("claude\n")}, tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("@65\t%87\t8686\n")},
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("codex\n")}, tmuxx.Response{},
	)
	var lookedUp []string
	launcher := New(runner, Dependencies{
		LookPath: func(name string) (string, error) {
			lookedUp = append(lookedUp, name)
			return "/bin/" + name, nil
		},
		Stat: func(path string) (fs.FileInfo, error) {
			if path != "/fleet workspace" {
				t.Fatalf("Stat path = %q, want /fleet workspace", path)
			}
			return testFileInfo{mode: fs.ModeDir | 0o755}, nil
		},
	})
	fleet := config.FleetConfig{Roles: []config.RoleConfig{
		{Name: "planner", Harness: config.HarnessClaude},
		{Name: "reviewer", Harness: config.HarnessCodex, Model: "gpt-5.6"},
	}}

	if err := launcher.Launch(context.Background(), "epic123", fleet, "/fleet workspace"); err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if want := []string{"tmux", "amq", "claude", "codex"}; !reflect.DeepEqual(lookedUp, want) {
		t.Fatalf("LookPath calls = %#v, want %#v", lookedUp, want)
	}

	assertCalls(t, runner,
		tmuxx.Call{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}},
		tmuxx.Call{Executable: "tmux", Args: []string{
			"new-session", "-d", "-s", "epic123", "-n", "planner", "-c", "/fleet workspace",
			"-P", "-F", "#{session_id}\t#{window_id}\t#{pane_id}\t#{pane_pid}", "--",
			"exec 'amq' 'coop' 'exec' '--session' 'epic123' '--me' 'planner' 'claude'",
		}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_managed", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_version", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_roles", "planner,reviewer"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_managed", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_role", "planner"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_harness", "claude"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_model", ""}},
		tmuxx.Call{Executable: "ps", Args: []string{"-o", "comm=", "-p", "4242"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_process", "claude"}},
		tmuxx.Call{Executable: "tmux", Args: []string{
			"new-window", "-d", "-t", "$17", "-n", "reviewer", "-c", "/fleet workspace",
			"-P", "-F", "#{window_id}\t#{pane_id}\t#{pane_pid}", "--",
			"exec 'amq' 'coop' 'exec' '--session' 'epic123' '--me' 'reviewer' 'codex' '--' '--model' 'gpt-5.6'",
		}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_managed", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_role", "reviewer"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_harness", "codex"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_model", "gpt-5.6"}},
		tmuxx.Call{Executable: "ps", Args: []string{"-o", "comm=", "-p", "8686"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_process", "codex"}},
	)
}

func presentExecutable(name string) (string, error) { return "/bin/" + name, nil }

func oneRoleFleet() config.FleetConfig {
	return config.FleetConfig{Roles: []config.RoleConfig{{Name: "planner", Harness: config.HarnessClaude}}}
}

func successfulOneRoleResponses(sessions string) []tmuxx.Response {
	return []tmuxx.Response{
		{Stdout: []byte(sessions)},
		{Stdout: []byte("$17\t@23\t%42\t4242\n")},
		{}, {}, {},
		{}, {}, {}, {},
		{Stdout: []byte("claude\n")},
		{},
	}
}

func assertCalls(t *testing.T, runner *tmuxx.FakeRunner, want ...tmuxx.Call) {
	t.Helper()
	if !reflect.DeepEqual(runner.Calls, want) {
		t.Fatalf("Runner calls = %#v, want %#v", runner.Calls, want)
	}
}
