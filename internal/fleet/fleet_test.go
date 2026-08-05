package fleet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

	_, err := launcher.Launch(context.Background(), "epic123", config.FleetConfig{Roles: []config.RoleConfig{
		{Name: "planner", Harness: config.HarnessClaude},
		{Name: "reviewer", Harness: config.HarnessClaude},
		{Name: "worker", Harness: config.HarnessCodex},
	}}, nil)

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

			_, err := launcher.Launch(context.Background(), "epic123", oneRoleFleet(), directoryPtr("/not-a-directory"))

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

func TestLaunchRejectsExplicitEmptyDirectoryBeforeTmux(t *testing.T) {
	runner := tmuxx.NewFakeRunner()
	directory := ""
	launcher := New(runner, Dependencies{
		LookPath: presentExecutable,
		Stat: func(path string) (fs.FileInfo, error) {
			if path != "" {
				t.Fatalf("Stat path = %q, want explicit empty path", path)
			}
			return nil, fs.ErrNotExist
		},
	})

	_, err := launcher.Launch(context.Background(), "epic123", oneRoleFleet(), &directory)

	var invalid *DirectoryError
	if !errors.As(err, &invalid) {
		t.Fatalf("Launch() error = %v, want *DirectoryError", err)
	}
	if len(runner.Calls) != 0 {
		t.Fatalf("Runner calls = %#v, want none", runner.Calls)
	}
}

func TestLaunchRejectsOnlyExactExistingSession(t *testing.T) {
	runner := tmuxx.NewFakeRunner(tmuxx.Response{Stdout: []byte("$1\tepic1234\n$2\tEpic123\n$3\tepic123\n$4\txepic123\n")})
	launcher := New(runner, Dependencies{
		LookPath: presentExecutable,
		Getwd:    func() (string, error) { return "/invocation", nil },
	})

	_, err := launcher.Launch(context.Background(), "epic123", oneRoleFleet(), nil)

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

func TestLaunchContinuesToCreationWhenSessionLookupFails(t *testing.T) {
	responses := successfulOneRoleResponses("")
	responses[0] = tmuxx.Response{Err: errors.New("no tmux server")}
	runner := tmuxx.NewFakeRunner(responses...)
	launcher := New(runner, Dependencies{
		LookPath: presentExecutable,
		Getwd:    func() (string, error) { return "/invocation", nil },
	})

	launched, err := launcher.Launch(context.Background(), "epic123", oneRoleFleet(), nil)
	if err != nil {
		t.Fatalf("Launch() error = %v, want successful launch", err)
	}
	if want := (tmuxx.Session{ID: "$17", Name: "epic123"}); launched != want {
		t.Fatalf("Launch() session = %+v, want %+v", launched, want)
	}
	if len(runner.Calls) != 17 {
		t.Fatalf("Runner call count = %d, want 17 (advisory lookup plus successful launch)", len(runner.Calls))
	}
	if got := runner.Calls[0]; got.Executable != "tmux" || !reflect.DeepEqual(got.Args, []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}) {
		t.Fatalf("first Runner call = %#v, want advisory list-sessions", got)
	}
	if got := runner.Calls[1]; got.Executable != "tmux" || len(got.Args) == 0 || got.Args[0] != "new-session" {
		t.Fatalf("second Runner call = %#v, want new-session", got)
	}
}

func TestLaunchPreservesContextFailureFromAdvisoryLookup(t *testing.T) {
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(sentinel.Error(), func(t *testing.T) {
			runner := tmuxx.NewFakeRunner(tmuxx.Response{Err: sentinel})
			launcher := New(runner, Dependencies{
				LookPath: presentExecutable,
				Getwd:    func() (string, error) { return "/invocation", nil },
			})

			_, err := launcher.Launch(context.Background(), "epic123", oneRoleFleet(), nil)

			if err != sentinel {
				t.Fatalf("Launch() error = %T %v, want exact %v", err, err, sentinel)
			}
			assertCalls(t, runner, tmuxx.Call{Executable: "tmux", Args: []string{
				"list-sessions", "-F", "#{session_id}\t#{session_name}",
			}})
		})
	}
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

	_, err := launcher.Launch(context.Background(), "epic123", fleet, nil)

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

func TestLaunchDoesNotTreatPrefixSuffixOrCaseCanariesAsExistingSession(t *testing.T) {
	tests := []struct {
		name     string
		sessions string
	}{
		{name: "prefix", sessions: "$1\tepic1234\n"},
		{name: "suffix", sessions: "$4\txepic123\n"},
		{name: "case", sessions: "$2\tEpic123\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner(successfulOneRoleResponses(tt.sessions)...)
			launcher := New(runner, Dependencies{
				LookPath: presentExecutable,
				Getwd:    func() (string, error) { return "/invocation", nil },
			})

			if _, err := launcher.Launch(context.Background(), "epic123", oneRoleFleet(), nil); err != nil {
				t.Fatalf("Launch() error = %v, want successful launch", err)
			}
			if len(runner.Calls) != 17 {
				t.Fatalf("Runner call count = %d, want 17 (successful launch)", len(runner.Calls))
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
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("claude\n")},
		tmuxx.Response{},
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
	)
	launcher := New(runner, Dependencies{
		LookPath: presentExecutable,
		Getwd:    func() (string, error) { return "/invocation cwd", nil },
	})
	fleet := config.FleetConfig{Roles: []config.RoleConfig{{
		Name: "planner", Harness: config.HarnessClaude, Model: "fable",
	}}}

	if _, err := launcher.Launch(context.Background(), "epic123", fleet, nil); err != nil {
		t.Fatalf("Launch() error = %v", err)
	}

	assertCalls(t, runner,
		tmuxx.Call{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}},
		tmuxx.Call{Executable: "tmux", Args: []string{
			"new-session", "-d", "-s", "epic123", "-n", "planner", "-c", "/invocation cwd",
			"-e", "AGENTCTL_SESSION=epic123", "-e", "AGENTCTL_ROLE=planner", "-e", "AGENTCTL_MANAGED=1",
			"-P", "-F", "#{session_id}\t#{window_id}\t#{pane_id}\t#{pane_pid}", "--",
			"exec 'amq' 'coop' 'exec' '--session' 'epic123' '--me' 'planner' 'claude' '--' '--model' 'fable'",
		}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_managed", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_version", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_roles", "planner"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_fleet", "planner:claude:fable:"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_dir", "/invocation cwd"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_managed", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_role", "planner"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_harness", "claude"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_model", "fable"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_effort", ""}},
		tmuxx.Call{Executable: "ps", Args: []string{"-o", "comm=", "-p", "4242"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_process", "claude"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-environment", "-t", "$17", "-u", "AGENTCTL_SESSION"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-environment", "-t", "$17", "-u", "AGENTCTL_ROLE"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-environment", "-t", "$17", "-u", "AGENTCTL_MANAGED"}},
	)
}

func TestLaunchRendersAndStampsPerRoleEffort(t *testing.T) {
	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("$17\t@23\t%42\t4242\n")},
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("claude\n")},
		tmuxx.Response{},
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("@65\t%77\t8686\n")},
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("codex\n")},
		tmuxx.Response{},
	)
	launcher := New(runner, Dependencies{
		LookPath: presentExecutable,
		Getwd:    func() (string, error) { return "/invocation cwd", nil },
	})
	fleet := config.FleetConfig{Roles: []config.RoleConfig{
		{Name: "planner", Harness: config.HarnessClaude, Model: "fable", Effort: "max"},
		{Name: "reviewer", Harness: config.HarnessCodex, Effort: "high"},
	}}

	if _, err := launcher.Launch(context.Background(), "epic123", fleet, nil); err != nil {
		t.Fatalf("Launch() error = %v", err)
	}

	assertCalls(t, runner,
		tmuxx.Call{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}},
		tmuxx.Call{Executable: "tmux", Args: []string{
			"new-session", "-d", "-s", "epic123", "-n", "planner", "-c", "/invocation cwd",
			"-e", "AGENTCTL_SESSION=epic123", "-e", "AGENTCTL_ROLE=planner", "-e", "AGENTCTL_MANAGED=1",
			"-P", "-F", "#{session_id}\t#{window_id}\t#{pane_id}\t#{pane_pid}", "--",
			"exec 'amq' 'coop' 'exec' '--session' 'epic123' '--me' 'planner' 'claude' '--' '--model' 'fable' '--effort' 'max'",
		}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_managed", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_version", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_roles", "planner,reviewer"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_fleet", "planner:claude:fable:max,reviewer:codex::high"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_dir", "/invocation cwd"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_managed", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_role", "planner"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_harness", "claude"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_model", "fable"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_effort", "max"}},
		tmuxx.Call{Executable: "ps", Args: []string{"-o", "comm=", "-p", "4242"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_process", "claude"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-environment", "-t", "$17", "-u", "AGENTCTL_SESSION"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-environment", "-t", "$17", "-u", "AGENTCTL_ROLE"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-environment", "-t", "$17", "-u", "AGENTCTL_MANAGED"}},
		tmuxx.Call{Executable: "tmux", Args: []string{
			"new-window", "-d", "-t", "$17", "-n", "reviewer", "-c", "/invocation cwd",
			"-e", "AGENTCTL_SESSION=epic123", "-e", "AGENTCTL_ROLE=reviewer", "-e", "AGENTCTL_MANAGED=1",
			"-P", "-F", "#{window_id}\t#{pane_id}\t#{pane_pid}", "--",
			"exec 'amq' 'coop' 'exec' '--session' 'epic123' '--me' 'reviewer' 'codex' '--' '--config' 'model_reasoning_effort=\"high\"'",
		}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_managed", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_role", "reviewer"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_harness", "codex"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_model", ""}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_effort", "high"}},
		tmuxx.Call{Executable: "ps", Args: []string{"-o", "comm=", "-p", "8686"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_process", "codex"}},
	)
}

func TestLaunchMultipleRolesUsesCreationIDsAndDeclarationOrder(t *testing.T) {
	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("$1\tunrelated\n$7\tepic1234\n")},
		tmuxx.Response{Stdout: []byte("$17\t@23\t%42\t4242\n")},
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("claude\n")}, tmuxx.Response{},
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("@65\t%87\t8686\n")},
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
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

	if _, err := launcher.Launch(context.Background(), "epic123", fleet, directoryPtr("/fleet workspace")); err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if want := []string{"tmux", "amq", "claude", "codex"}; !reflect.DeepEqual(lookedUp, want) {
		t.Fatalf("LookPath calls = %#v, want %#v", lookedUp, want)
	}

	assertCalls(t, runner,
		tmuxx.Call{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}},
		tmuxx.Call{Executable: "tmux", Args: []string{
			"new-session", "-d", "-s", "epic123", "-n", "planner", "-c", "/fleet workspace",
			"-e", "AGENTCTL_SESSION=epic123", "-e", "AGENTCTL_ROLE=planner", "-e", "AGENTCTL_MANAGED=1",
			"-P", "-F", "#{session_id}\t#{window_id}\t#{pane_id}\t#{pane_pid}", "--",
			"exec 'amq' 'coop' 'exec' '--session' 'epic123' '--me' 'planner' 'claude'",
		}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_managed", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_version", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_roles", "planner,reviewer"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_fleet", "planner:claude::,reviewer:codex:gpt-5.6:"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_dir", "/fleet workspace"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_managed", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_role", "planner"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_harness", "claude"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_model", ""}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_effort", ""}},
		tmuxx.Call{Executable: "ps", Args: []string{"-o", "comm=", "-p", "4242"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_process", "claude"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-environment", "-t", "$17", "-u", "AGENTCTL_SESSION"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-environment", "-t", "$17", "-u", "AGENTCTL_ROLE"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-environment", "-t", "$17", "-u", "AGENTCTL_MANAGED"}},
		tmuxx.Call{Executable: "tmux", Args: []string{
			"new-window", "-d", "-t", "$17", "-n", "reviewer", "-c", "/fleet workspace",
			"-e", "AGENTCTL_SESSION=epic123", "-e", "AGENTCTL_ROLE=reviewer", "-e", "AGENTCTL_MANAGED=1",
			"-P", "-F", "#{window_id}\t#{pane_id}\t#{pane_pid}", "--",
			"exec 'amq' 'coop' 'exec' '--session' 'epic123' '--me' 'reviewer' 'codex' '--' '--model' 'gpt-5.6'",
		}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_managed", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_role", "reviewer"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_harness", "codex"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_model", "gpt-5.6"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_effort", ""}},
		tmuxx.Call{Executable: "ps", Args: []string{"-o", "comm=", "-p", "8686"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_process", "codex"}},
	)
}

func TestLaunchSessionEnvironmentClearFailureIsNonfatalAndDoesNotRollback(t *testing.T) {
	cause := errors.New("permission denied despite arbitrary stderr text")
	responses := launchPrefixResponses(25)
	responses[15] = tmuxx.Response{Err: cause}
	runner := tmuxx.NewFakeRunner(responses...)
	var stderr bytes.Buffer
	launcher := New(runner, Dependencies{
		LookPath: presentExecutable,
		Getwd:    func() (string, error) { return "/repo", nil },
		Stderr:   &stderr,
	})

	if _, err := launcher.Launch(context.Background(), "epic123", twoRoleFleet(), nil); err != nil {
		t.Fatalf("Launch() error = %v, want success", err)
	}
	if got, want := stderr.String(), "agentctl: could not clear AGENTCTL_ROLE from the tmux session environment; windows created by hand may inherit the first role's identity: tmux clear session environment: permission denied despite arbitrary stderr text\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
	assertNoKill(t, runner)
	if got, want := len(runner.Calls), 25; got != want {
		t.Fatalf("runner calls = %d, want %d (all clears and later role continue)", got, want)
	}
}

func TestLaunchExportsIdentityEnvironmentOnBothCreationPaths(t *testing.T) {
	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("$17\t@23\t%42\t4242\n")},
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("claude\n")}, tmuxx.Response{},
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("@65\t%87\t8686\n")},
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("codex\n")}, tmuxx.Response{},
	)
	launcher := New(runner, Dependencies{
		LookPath: presentExecutable,
		Getwd:    func() (string, error) { return "/repo", nil },
	})
	fleet := config.FleetConfig{Roles: []config.RoleConfig{
		{Name: "planner", Harness: config.HarnessClaude},
		{Name: "reviewer", Harness: config.HarnessCodex},
	}}

	if _, err := launcher.Launch(context.Background(), "epic123", fleet, nil); err != nil {
		t.Fatalf("Launch() error = %v", err)
	}

	tests := []struct {
		command string
		want    []string
	}{
		{
			command: "new-session",
			want:    []string{"-e", "AGENTCTL_SESSION=epic123", "-e", "AGENTCTL_ROLE=planner", "-e", "AGENTCTL_MANAGED=1"},
		},
		{
			command: "new-window",
			want:    []string{"-e", "AGENTCTL_SESSION=epic123", "-e", "AGENTCTL_ROLE=reviewer", "-e", "AGENTCTL_MANAGED=1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got := environmentArgs(t, runner, tt.command)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("%s environment argv = %#v, want %#v", tt.command, got, tt.want)
			}
		})
	}
}

// environmentArgs returns every -e flag and its assignment, in order, from the
// sole tmux call whose first argument is command.
func environmentArgs(t *testing.T, runner *tmuxx.FakeRunner, command string) []string {
	t.Helper()
	var environment []string
	matches := 0
	for _, call := range runner.Calls {
		if call.Executable != "tmux" || len(call.Args) == 0 || call.Args[0] != command {
			continue
		}
		matches++
		for index := 0; index < len(call.Args); index++ {
			if call.Args[index] != "-e" {
				continue
			}
			if index+1 == len(call.Args) {
				t.Fatalf("%s call ends with a dangling -e: %#v", command, call.Args)
			}
			environment = append(environment, call.Args[index], call.Args[index+1])
			index++
		}
	}
	if matches != 1 {
		t.Fatalf("%s calls = %d, want exactly one", command, matches)
	}
	return environment
}

func presentExecutable(name string) (string, error) { return "/bin/" + name, nil }

func directoryPtr(value string) *string { return &value }

func oneRoleFleet() config.FleetConfig {
	return config.FleetConfig{Roles: []config.RoleConfig{{Name: "planner", Harness: config.HarnessClaude}}}
}

func successfulOneRoleResponses(sessions string) []tmuxx.Response {
	return []tmuxx.Response{
		{Stdout: []byte(sessions)},
		{Stdout: []byte("$17\t@23\t%42\t4242\n")},
		{}, {}, {}, {}, {},
		{}, {}, {}, {}, {},
		{Stdout: []byte("claude\n")},
		{},
		{}, {}, {},
	}
}

func assertCalls(t *testing.T, runner *tmuxx.FakeRunner, want ...tmuxx.Call) {
	t.Helper()
	if !reflect.DeepEqual(runner.Calls, want) {
		t.Fatalf("Runner calls = %#v, want %#v", runner.Calls, want)
	}
}

func TestLaunchStoresImmediateProcessIdentityLiterallyWithoutSleeping(t *testing.T) {
	clock := &fakeClock{}
	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("$17\t@23\t%42\t4242\n")},
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("/opt/Agent Tools/claude runner\n")},
		tmuxx.Response{},
	)
	launcher := New(runner, Dependencies{
		LookPath: presentExecutable,
		Getwd:    func() (string, error) { return "/repo", nil },
		Now:      clock.Now,
		Sleep:    clock.Sleep,
	})

	if _, err := launcher.Launch(context.Background(), "epic123", oneRoleFleet(), nil); err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if len(clock.sleeps) != 0 {
		t.Fatalf("sleep calls = %#v, want none", clock.sleeps)
	}
	if got := runner.Calls[len(runner.Calls)-4]; !reflect.DeepEqual(got, tmuxx.Call{Executable: "tmux", Args: []string{
		"set-option", "-w", "-t", "@23", "@agentctl_process", "/opt/Agent Tools/claude runner",
	}}) {
		t.Fatalf("last call = %#v, want literal process identity option", got)
	}
}

func TestLaunchPollsAmqAndUnavailableUntilHarnessIdentity(t *testing.T) {
	for _, tt := range []struct {
		name        string
		unavailable tmuxx.Response
	}{
		{name: "dead pid", unavailable: tmuxx.Response{Err: fakeExitError{code: 1}}},
		{name: "empty identity", unavailable: tmuxx.Response{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clock := &fakeClock{}
			runner := tmuxx.NewFakeRunner(
				tmuxx.Response{},
				tmuxx.Response{Stdout: []byte("$17\t@23\t%42\t4242\n")},
				tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
				tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
				tmuxx.Response{Stdout: []byte("amq\n")},
				tt.unavailable,
				tmuxx.Response{Stdout: []byte("amq\n")},
				tmuxx.Response{Stdout: []byte("claude\n")},
				tmuxx.Response{},
			)
			launcher := New(runner, Dependencies{
				LookPath: presentExecutable,
				Getwd:    func() (string, error) { return "/repo", nil },
				Now:      clock.Now,
				Sleep:    clock.Sleep,
			})

			if _, err := launcher.Launch(context.Background(), "epic123", oneRoleFleet(), nil); err != nil {
				t.Fatalf("Launch() error = %v", err)
			}
			if want := []time.Duration{100 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond}; !reflect.DeepEqual(clock.sleeps, want) {
				t.Fatalf("sleep calls = %#v, want %#v", clock.sleeps, want)
			}
			if got := runner.Calls[len(runner.Calls)-4]; !reflect.DeepEqual(got, tmuxx.Call{Executable: "tmux", Args: []string{
				"set-option", "-w", "-t", "@23", "@agentctl_process", "claude",
			}}) {
				t.Fatalf("last call = %#v, want harness identity option", got)
			}
		})
	}
}

func TestLaunchProcessPollRetriesThroughFiveSecondBoundaryThenRollsBack(t *testing.T) {
	for _, tt := range []struct {
		name     string
		response tmuxx.Response
	}{
		{name: "amq", response: tmuxx.Response{Stdout: []byte("amq\n")}},
		{name: "unavailable", response: tmuxx.Response{Err: fakeExitError{code: 1}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clock := &fakeClock{}
			responses := launchPrefixResponses(12)
			for range 51 {
				responses = append(responses, tt.response)
			}
			responses = append(responses, tmuxx.Response{}) // rollback
			runner := tmuxx.NewFakeRunner(responses...)
			launcher := New(runner, Dependencies{
				LookPath: presentExecutable,
				Getwd:    func() (string, error) { return "/repo", nil },
				Now:      clock.Now,
				Sleep:    clock.Sleep,
			})

			_, err := launcher.Launch(context.Background(), "epic123", oneRoleFleet(), nil)
			var launchErr *LaunchError
			if !errors.As(err, &launchErr) {
				t.Fatalf("Launch() error = %v, want *LaunchError", err)
			}
			if launchErr.Role != "planner" || launchErr.Session != "epic123" {
				t.Fatalf("LaunchError = %#v, want planner / epic123", launchErr)
			}
			if len(clock.sleeps) != 50 {
				t.Fatalf("sleep count = %d, want 50", len(clock.sleeps))
			}
			for index, duration := range clock.sleeps {
				if duration != 100*time.Millisecond {
					t.Fatalf("sleep %d = %s, want 100ms", index, duration)
				}
			}
			if got, want := clock.now, 5*time.Second; got != want {
				t.Fatalf("final fake time = %s, want final attempt boundary %s", got, want)
			}
			if got, want := processCallCount(runner), 51; got != want {
				t.Fatalf("ps call count = %d, want %d (t=0 through t=5s)", got, want)
			}
			assertLastKill(t, runner, "$17")
		})
	}
}

func TestLaunchProcessPollFailsImmediatelyForNonSentinelError(t *testing.T) {
	clock := &fakeClock{}
	cause := errors.New("ps start failed")
	responses := append(launchPrefixResponses(12), tmuxx.Response{Err: cause}, tmuxx.Response{})
	runner := tmuxx.NewFakeRunner(responses...)
	launcher := New(runner, Dependencies{
		LookPath: presentExecutable,
		Getwd:    func() (string, error) { return "/repo", nil },
		Now:      clock.Now,
		Sleep:    clock.Sleep,
	})

	_, err := launcher.Launch(context.Background(), "epic123", oneRoleFleet(), nil)
	if !errors.Is(err, cause) {
		t.Fatalf("Launch() error = %v, want wrapped %v", err, cause)
	}
	if len(clock.sleeps) != 0 {
		t.Fatalf("sleep calls = %#v, want none", clock.sleeps)
	}
	assertLastKill(t, runner, "$17")
}

func TestLaunchRollsBackEveryPostOwnershipFailureAndStops(t *testing.T) {
	cause := errors.New("injected failure")
	tests := []struct {
		name  string
		index int
		role  string
	}{
		{name: "session managed option", index: 2, role: "planner"},
		{name: "session version option", index: 3, role: "planner"},
		{name: "session roles option", index: 4, role: "planner"},
		{name: "session fleet option", index: 5, role: "planner"},
		{name: "session dir option", index: 6, role: "planner"},
		{name: "first window managed option", index: 7, role: "planner"},
		{name: "first window role option", index: 8, role: "planner"},
		{name: "first window harness option", index: 9, role: "planner"},
		{name: "first window model option", index: 10, role: "planner"},
		{name: "first window effort option", index: 11, role: "planner"},
		{name: "first window baseline", index: 12, role: "planner"},
		{name: "first window process option", index: 13, role: "planner"},
		{name: "later window creation", index: 17, role: "reviewer"},
		{name: "later window managed option", index: 18, role: "reviewer"},
		{name: "later window role option", index: 19, role: "reviewer"},
		{name: "later window harness option", index: 20, role: "reviewer"},
		{name: "later window model option", index: 21, role: "reviewer"},
		{name: "later window effort option", index: 22, role: "reviewer"},
		{name: "later window baseline", index: 23, role: "reviewer"},
		{name: "later window process option", index: 24, role: "reviewer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := &fakeClock{}
			responses := launchPrefixResponses(tt.index)
			responses = append(responses, tmuxx.Response{Err: cause}, tmuxx.Response{})
			runner := tmuxx.NewFakeRunner(responses...)
			launcher := New(runner, Dependencies{
				LookPath: presentExecutable,
				Getwd:    func() (string, error) { return "/repo", nil },
				Now:      clock.Now,
				Sleep:    func(time.Duration) { t.Fatal("unexpected process poll sleep") },
			})

			_, err := launcher.Launch(context.Background(), "epic123", twoRoleFleet(), nil)
			var launchErr *LaunchError
			if !errors.As(err, &launchErr) {
				t.Fatalf("Launch() error = %v, want *LaunchError", err)
			}
			if launchErr.Role != tt.role {
				t.Fatalf("LaunchError.Role = %q, want %q", launchErr.Role, tt.role)
			}
			if !errors.Is(err, cause) {
				t.Fatalf("Launch() error = %v, want wrapped injected cause", err)
			}
			if got, want := len(runner.Calls), tt.index+2; got != want {
				t.Fatalf("runner calls = %d, want %d (failure, one rollback, no later calls)", got, want)
			}
			assertLastKill(t, runner, "$17")
		})
	}
}

func TestLaunchRollsBackMalformedLaterWindowOutput(t *testing.T) {
	responses := launchPrefixResponses(17)
	responses = append(responses, tmuxx.Response{Stdout: []byte("bad creation output\n")}, tmuxx.Response{})
	runner := tmuxx.NewFakeRunner(responses...)
	launcher := New(runner, Dependencies{LookPath: presentExecutable, Getwd: func() (string, error) { return "/repo", nil }})

	_, err := launcher.Launch(context.Background(), "epic123", twoRoleFleet(), nil)
	var launchErr *LaunchError
	if !errors.As(err, &launchErr) || launchErr.Role != "reviewer" {
		t.Fatalf("Launch() error = %v, want reviewer *LaunchError", err)
	}
	if !errors.Is(err, tmuxx.ErrCreationOutput) {
		t.Fatalf("Launch() error = %v, want wrapped tmuxx.ErrCreationOutput", err)
	}
	if launchErr.CleanupErr != nil {
		t.Fatalf("LaunchError.CleanupErr = %v, want successful cleanup", launchErr.CleanupErr)
	}
	if got, want := len(runner.Calls), 19; got != want {
		t.Fatalf("runner calls = %d, want %d (malformed new-window, one rollback, no recovery lookup)", got, want)
	}
	if got, want := runner.Calls[17], (tmuxx.Call{Executable: "tmux", Args: []string{
		"new-window", "-d", "-t", "$17", "-n", "reviewer", "-c", "/repo",
		"-e", "AGENTCTL_SESSION=epic123", "-e", "AGENTCTL_ROLE=reviewer", "-e", "AGENTCTL_MANAGED=1",
		"-P", "-F", "#{window_id}\t#{pane_id}\t#{pane_pid}", "--",
		"exec 'amq' 'coop' 'exec' '--session' 'epic123' '--me' 'reviewer' 'codex'",
	}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("malformed new-window call = %#v, want %#v", got, want)
	}
	if got, want := runner.Calls[18], (tmuxx.Call{Executable: "tmux", Args: []string{"kill-session", "-t", "$17"}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("rollback call = %#v, want returned-ID kill %#v", got, want)
	}
	assertNoRecoveryLookup(t, runner.Calls[1:18])
}

func TestLaunchErrorReportsCleanupResultAndUnwrapsFailureCause(t *testing.T) {
	cause := errors.New("set metadata failed")
	cleanupCause := errors.New("kill failed")
	for _, tt := range []struct {
		name        string
		cleanup     tmuxx.Response
		wantMessage string
		wantCleanup bool
	}{
		{name: "removed", cleanup: tmuxx.Response{}, wantMessage: "failed to launch planner; removed incomplete session epic123: tmux set session option: set metadata failed"},
		{name: "cleanup failure", cleanup: tmuxx.Response{Err: cleanupCause}, wantMessage: "failed to launch planner; failed to remove incomplete session epic123: tmux kill session: kill failed (launch failure: tmux set session option: set metadata failed)", wantCleanup: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			responses := append(launchPrefixResponses(2), tmuxx.Response{Err: cause}, tt.cleanup)
			runner := tmuxx.NewFakeRunner(responses...)
			launcher := New(runner, Dependencies{LookPath: presentExecutable, Getwd: func() (string, error) { return "/repo", nil }})

			_, err := launcher.Launch(context.Background(), "epic123", oneRoleFleet(), nil)
			var launchErr *LaunchError
			if !errors.As(err, &launchErr) {
				t.Fatalf("Launch() error = %v, want *LaunchError", err)
			}
			if got, want := len(runner.Calls), 4; got != want {
				t.Fatalf("runner calls = %d, want %d (failure, one cleanup attempt)", got, want)
			}
			assertExactlyOneKill(t, runner, "$17")
			if got := err.Error(); got != tt.wantMessage {
				t.Fatalf("Launch() error = %q, want %q", got, tt.wantMessage)
			}
			if !errors.Is(err, cause) {
				t.Fatalf("Launch() error = %v, want wrapped failure cause", err)
			}
			if (launchErr.CleanupErr != nil) != tt.wantCleanup {
				t.Fatalf("LaunchError.CleanupErr = %v, want cleanup present %t", launchErr.CleanupErr, tt.wantCleanup)
			}
		})
	}
}

func TestLaunchCreationFailuresDoNotClaimOwnership(t *testing.T) {
	tests := []struct {
		name          string
		response      tmuxx.Response
		wantCreation  bool
		wantMalformed bool
	}{
		{name: "tmux command error", response: tmuxx.Response{Err: errors.New("tmux failed")}},
		{name: "malformed output", response: tmuxx.Response{Stdout: []byte("not a record\n")}, wantCreation: true, wantMalformed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner(tmuxx.Response{}, tt.response)
			launcher := New(runner, Dependencies{LookPath: presentExecutable, Getwd: func() (string, error) { return "/repo", nil }})

			_, err := launcher.Launch(context.Background(), "epic123", oneRoleFleet(), nil)
			var creationErr *CreationError
			if errors.As(err, &creationErr) != tt.wantCreation {
				t.Fatalf("CreationError presence = %t, want %t; err = %v", errors.As(err, &creationErr), tt.wantCreation, err)
			}
			if tt.wantMalformed {
				wantSuffix := "; a session named epic123 may exist; inspect with tmux ls"
				if got := err.Error(); len(got) < len(wantSuffix) || got[len(got)-len(wantSuffix):] != wantSuffix {
					t.Fatalf("creation error = %q, want suffix %q", got, wantSuffix)
				}
				if !errors.Is(err, tmuxx.ErrCreationOutput) {
					t.Fatalf("creation error = %v, want wrapped tmuxx.ErrCreationOutput", err)
				}
			}
			assertNoKill(t, runner)
		})
	}
}

func TestLaunchRejectsEmptyFleetBeforeTmux(t *testing.T) {
	runner := tmuxx.NewFakeRunner()
	launcher := New(runner, Dependencies{LookPath: presentExecutable, Getwd: func() (string, error) { return "/repo", nil }})

	_, err := launcher.Launch(context.Background(), "epic123", config.FleetConfig{}, nil)

	if err == nil {
		t.Fatal("Launch() error = nil, want empty-fleet error")
	}
	if len(runner.Calls) != 0 {
		t.Fatalf("Runner calls = %#v, want none for complete-validation failure", runner.Calls)
	}
}

func TestLaunchOtherPreOwnershipFailuresDoNotKill(t *testing.T) {
	tests := []struct {
		name     string
		launcher func(*tmuxx.FakeRunner) Launcher
		fleet    config.FleetConfig
	}{
		{
			name: "list sessions",
			launcher: func(r *tmuxx.FakeRunner) Launcher {
				return New(r, Dependencies{LookPath: presentExecutable, Getwd: func() (string, error) { return "/repo", nil }})
			},
			fleet: oneRoleFleet(),
		},
		{
			name: "first role command",
			launcher: func(r *tmuxx.FakeRunner) Launcher {
				return New(r, Dependencies{LookPath: presentExecutable, Getwd: func() (string, error) { return "/repo", nil }})
			},
			fleet: config.FleetConfig{Roles: []config.RoleConfig{{Name: "planner", Harness: "unknown"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			responses := []tmuxx.Response{{Err: errors.New("list failed")}}
			if tt.name != "list sessions" {
				responses = []tmuxx.Response{{}}
			}
			runner := tmuxx.NewFakeRunner(responses...)
			_, err := tt.launcher(runner).Launch(context.Background(), "epic123", tt.fleet, nil)
			if err == nil {
				t.Fatal("Launch() error = nil, want pre-ownership error")
			}
			assertNoKill(t, runner)
		})
	}
}

type fakeClock struct {
	now    time.Duration
	sleeps []time.Duration
}

func (c *fakeClock) Now() time.Time { return time.Unix(0, 0).Add(c.now) }

func (c *fakeClock) Sleep(duration time.Duration) {
	c.sleeps = append(c.sleeps, duration)
	c.now += duration
}

type fakeExitError struct{ code int }

func (e fakeExitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e fakeExitError) ExitCode() int { return e.code }

func twoRoleFleet() config.FleetConfig {
	return config.FleetConfig{Roles: []config.RoleConfig{
		{Name: "planner", Harness: config.HarnessClaude},
		{Name: "reviewer", Harness: config.HarnessCodex},
	}}
}

// launchPrefixResponses returns the successful scripted calls before zero-based
// call index end, for a two-role launch. Process identities are immediate.
func launchPrefixResponses(end int) []tmuxx.Response {
	all := []tmuxx.Response{
		{},
		{Stdout: []byte("$17\t@23\t%42\t4242\n")},
		{}, {}, {}, {}, {},
		{}, {}, {}, {}, {},
		{Stdout: []byte("claude\n")}, {},
		{}, {}, {},
		{Stdout: []byte("@65\t%87\t8686\n")},
		{}, {}, {}, {}, {},
		{Stdout: []byte("codex\n")}, {},
	}
	return append([]tmuxx.Response(nil), all[:end]...)
}

func processCallCount(runner *tmuxx.FakeRunner) int {
	count := 0
	for _, call := range runner.Calls {
		if call.Executable == "ps" {
			count++
		}
	}
	return count
}

func assertLastKill(t *testing.T, runner *tmuxx.FakeRunner, sessionID string) {
	t.Helper()
	if len(runner.Calls) == 0 {
		t.Fatal("runner calls = none, want rollback")
	}
	if got, want := runner.Calls[len(runner.Calls)-1], (tmuxx.Call{Executable: "tmux", Args: []string{"kill-session", "-t", sessionID}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("last call = %#v, want %#v", got, want)
	}
}

func assertExactlyOneKill(t *testing.T, runner *tmuxx.FakeRunner, sessionID string) {
	t.Helper()
	want := tmuxx.Call{Executable: "tmux", Args: []string{"kill-session", "-t", sessionID}}
	var kills []tmuxx.Call
	for _, call := range runner.Calls {
		if call.Executable == "tmux" && len(call.Args) > 0 && call.Args[0] == "kill-session" {
			kills = append(kills, call)
		}
	}
	if got := len(kills); got != 1 {
		t.Fatalf("kill-session calls = %#v, want exactly one", kills)
	}
	if got := kills[0]; !reflect.DeepEqual(got, want) {
		t.Fatalf("kill-session call = %#v, want %#v", got, want)
	}
}

func assertNoRecoveryLookup(t *testing.T, calls []tmuxx.Call) {
	t.Helper()
	for _, call := range calls {
		if call.Executable != "tmux" || len(call.Args) == 0 {
			continue
		}
		switch call.Args[0] {
		case "list-sessions", "list-windows", "list-panes":
			t.Fatalf("unexpected recovery lookup before rollback: %#v", call)
		}
	}
}

func assertNoKill(t *testing.T, runner *tmuxx.FakeRunner) {
	t.Helper()
	for _, call := range runner.Calls {
		if call.Executable == "tmux" && len(call.Args) > 0 && call.Args[0] == "kill-session" {
			t.Fatalf("unexpected kill-session call: %#v", call)
		}
	}
}
