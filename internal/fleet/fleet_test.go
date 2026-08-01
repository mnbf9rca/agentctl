package fleet

import (
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

func TestLaunchStoresImmediateProcessIdentityLiterallyWithoutSleeping(t *testing.T) {
	clock := &fakeClock{}
	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("$17\t@23\t%42\t4242\n")},
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("/opt/Agent Tools/claude runner\n")},
		tmuxx.Response{},
	)
	launcher := New(runner, Dependencies{
		LookPath: presentExecutable,
		Getwd:    func() (string, error) { return "/repo", nil },
		Now:      clock.Now,
		Sleep:    clock.Sleep,
	})

	if err := launcher.Launch(context.Background(), "epic123", oneRoleFleet(), ""); err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if len(clock.sleeps) != 0 {
		t.Fatalf("sleep calls = %#v, want none", clock.sleeps)
	}
	if got := runner.Calls[len(runner.Calls)-1]; !reflect.DeepEqual(got, tmuxx.Call{Executable: "tmux", Args: []string{
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
				tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
				tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
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

			if err := launcher.Launch(context.Background(), "epic123", oneRoleFleet(), ""); err != nil {
				t.Fatalf("Launch() error = %v", err)
			}
			if want := []time.Duration{100 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond}; !reflect.DeepEqual(clock.sleeps, want) {
				t.Fatalf("sleep calls = %#v, want %#v", clock.sleeps, want)
			}
			if got := runner.Calls[len(runner.Calls)-1]; !reflect.DeepEqual(got, tmuxx.Call{Executable: "tmux", Args: []string{
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
			responses := launchPrefixResponses(9)
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

			err := launcher.Launch(context.Background(), "epic123", oneRoleFleet(), "")
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
	responses := append(launchPrefixResponses(9), tmuxx.Response{Err: cause}, tmuxx.Response{})
	runner := tmuxx.NewFakeRunner(responses...)
	launcher := New(runner, Dependencies{
		LookPath: presentExecutable,
		Getwd:    func() (string, error) { return "/repo", nil },
		Now:      clock.Now,
		Sleep:    clock.Sleep,
	})

	err := launcher.Launch(context.Background(), "epic123", oneRoleFleet(), "")
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
		{name: "first window managed option", index: 5, role: "planner"},
		{name: "first window role option", index: 6, role: "planner"},
		{name: "first window harness option", index: 7, role: "planner"},
		{name: "first window model option", index: 8, role: "planner"},
		{name: "first window baseline", index: 9, role: "planner"},
		{name: "first window process option", index: 10, role: "planner"},
		{name: "later window creation", index: 11, role: "reviewer"},
		{name: "later window managed option", index: 12, role: "reviewer"},
		{name: "later window role option", index: 13, role: "reviewer"},
		{name: "later window harness option", index: 14, role: "reviewer"},
		{name: "later window model option", index: 15, role: "reviewer"},
		{name: "later window baseline", index: 16, role: "reviewer"},
		{name: "later window process option", index: 17, role: "reviewer"},
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

			err := launcher.Launch(context.Background(), "epic123", twoRoleFleet(), "")
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
	responses := launchPrefixResponses(11)
	responses = append(responses, tmuxx.Response{Stdout: []byte("bad creation output\n")}, tmuxx.Response{})
	runner := tmuxx.NewFakeRunner(responses...)
	launcher := New(runner, Dependencies{LookPath: presentExecutable, Getwd: func() (string, error) { return "/repo", nil }})

	err := launcher.Launch(context.Background(), "epic123", twoRoleFleet(), "")
	var launchErr *LaunchError
	if !errors.As(err, &launchErr) || launchErr.Role != "reviewer" {
		t.Fatalf("Launch() error = %v, want reviewer *LaunchError", err)
	}
	if !errors.Is(err, tmuxx.ErrCreationOutput) {
		t.Fatalf("Launch() error = %v, want wrapped tmuxx.ErrCreationOutput", err)
	}
	assertLastKill(t, runner, "$17")
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
		{name: "removed", cleanup: tmuxx.Response{}, wantMessage: "failed to launch planner; removed incomplete session epic123"},
		{name: "cleanup failure", cleanup: tmuxx.Response{Err: cleanupCause}, wantMessage: "failed to launch planner; failed to remove incomplete session epic123: tmux kill session: kill failed", wantCleanup: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			responses := append(launchPrefixResponses(2), tmuxx.Response{Err: cause}, tt.cleanup)
			runner := tmuxx.NewFakeRunner(responses...)
			launcher := New(runner, Dependencies{LookPath: presentExecutable, Getwd: func() (string, error) { return "/repo", nil }})

			err := launcher.Launch(context.Background(), "epic123", oneRoleFleet(), "")
			var launchErr *LaunchError
			if !errors.As(err, &launchErr) {
				t.Fatalf("Launch() error = %v, want *LaunchError", err)
			}
			if got := err.Error(); got != tt.wantMessage {
				t.Fatalf("Launch() error = %q, want %q", got, tt.wantMessage)
			}
			if !errors.Is(err, cause) {
				t.Fatalf("Launch() error = %v, want wrapped failure cause", err)
			}
			if (launchErr.CleanupErr != nil) != tt.wantCleanup {
				t.Fatalf("LaunchError.CleanupErr = %v, want cleanup present %t", launchErr.CleanupErr, tt.wantCleanup)
			}
			assertLastKill(t, runner, "$17")
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

			err := launcher.Launch(context.Background(), "epic123", oneRoleFleet(), "")
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
			name: "empty fleet",
			launcher: func(r *tmuxx.FakeRunner) Launcher {
				return New(r, Dependencies{LookPath: presentExecutable, Getwd: func() (string, error) { return "/repo", nil }})
			},
			fleet: config.FleetConfig{},
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
			err := tt.launcher(runner).Launch(context.Background(), "epic123", tt.fleet, "")
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
		{}, {}, {},
		{}, {}, {}, {},
		{Stdout: []byte("claude\n")}, {},
		{Stdout: []byte("@65\t%87\t8686\n")},
		{}, {}, {}, {},
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

func assertNoKill(t *testing.T, runner *tmuxx.FakeRunner) {
	t.Helper()
	for _, call := range runner.Calls {
		if call.Executable == "tmux" && len(call.Args) > 0 && call.Args[0] == "kill-session" {
			t.Fatalf("unexpected kill-session call: %#v", call)
		}
	}
}
