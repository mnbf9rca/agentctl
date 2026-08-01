package main

import (
	"bytes"
	"errors"
	"io/fs"
	"reflect"
	"testing"
	"time"

	"github.com/mnbf9rca/agentctl/internal/fleet"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

type launchTestFileInfo struct{ mode fs.FileMode }

func (info launchTestFileInfo) Name() string       { return "test" }
func (info launchTestFileInfo) Size() int64        { return 0 }
func (info launchTestFileInfo) Mode() fs.FileMode  { return info.mode }
func (info launchTestFileInfo) ModTime() time.Time { return time.Time{} }
func (info launchTestFileInfo) IsDir() bool        { return info.mode.IsDir() }
func (info launchTestFileInfo) Sys() any           { return nil }

func TestRunLaunchRejectsInvalidConfigurationBeforeRunner(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{name: "empty session", args: []string{"launch", "--roles", "planner:claude"}, want: "invalid session \"\""},
		{name: "empty roles", args: []string{"launch", "--session", "fleet"}, want: "invalid --roles value \"\": must not be empty"},
		{name: "invalid session", args: []string{"launch", "--session", "Invalid", "--roles", "planner:claude"}, want: "invalid session \"Invalid\""},
		{name: "invalid fleet", args: []string{"launch", "--session", "fleet", "--roles", "planner:unknown"}, want: "unknown harness \"unknown\""},
		{name: "explicitly empty models", args: []string{"launch", "--session", "fleet", "--roles", "planner:claude", "--models="}, want: "--models value \"\": must not be empty"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner()
			var stdout, stderr bytes.Buffer

			code := runWith(tt.args, &stdout, &stderr, launchTestDependencies(runner))

			if code != exitUsage {
				t.Fatalf("runWith(%q) = %d, want %d", tt.args, code, exitUsage)
			}
			if got := stderr.String(); !bytes.Contains([]byte(got), []byte(tt.want)) || !bytes.Contains([]byte(got), []byte(commandUsage["launch"])) {
				t.Fatalf("stderr = %q, want configuration error %q and usage", got, tt.want)
			}
			if len(runner.Calls) != 0 {
				t.Fatalf("runner calls = %#v, want none", runner.Calls)
			}
		})
	}
}

func TestRunLaunchMissingDependencyDoesNotCallRunner(t *testing.T) {
	runner := tmuxx.NewFakeRunner()
	deps := launchTestDependencies(runner)
	deps.fleet.LookPath = func(name string) (string, error) {
		if name == "amq" {
			return "", errors.New("not found")
		}
		return "/bin/" + name, nil
	}
	var stdout, stderr bytes.Buffer

	code := runWith([]string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &stdout, &stderr, deps)

	if code != exitMissingExecutable {
		t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitMissingExecutable, stderr.String())
	}
	if got, want := stderr.String(), "agentctl: required executable \"amq\" not found\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
	if len(runner.Calls) != 0 {
		t.Fatalf("runner calls = %#v, want none", runner.Calls)
	}
}

func TestRunLaunchRejectsInvalidDirectoryBeforeRunner(t *testing.T) {
	for _, tt := range []struct {
		name string
		stat func(string) (fs.FileInfo, error)
	}{
		{name: "missing", stat: func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist }},
		{name: "regular file", stat: func(string) (fs.FileInfo, error) { return launchTestFileInfo{mode: 0o644}, nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner()
			deps := launchTestDependencies(runner)
			deps.fleet.Stat = tt.stat
			var stdout, stderr bytes.Buffer

			code := runWith([]string{"launch", "--session", "fleet", "--roles", "planner:claude", "--dir", "/bad"}, &stdout, &stderr, deps)

			if code != exitUsage {
				t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitUsage, stderr.String())
			}
			if len(runner.Calls) != 0 {
				t.Fatalf("runner calls = %#v, want none", runner.Calls)
			}
		})
	}
}

func TestRunLaunchRejectsExplicitEmptyDirectoryBeforeRunner(t *testing.T) {
	runner := tmuxx.NewFakeRunner()
	deps := launchTestDependencies(runner)
	deps.fleet.Stat = func(path string) (fs.FileInfo, error) {
		if path != "" {
			t.Fatalf("Stat path = %q, want explicit empty path", path)
		}
		return nil, fs.ErrNotExist
	}
	var stdout, stderr bytes.Buffer

	code := runWith([]string{"launch", "--session", "fleet", "--roles", "planner:claude", "--dir="}, &stdout, &stderr, deps)

	if code != exitUsage {
		t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitUsage, stderr.String())
	}
	if len(runner.Calls) != 0 {
		t.Fatalf("runner calls = %#v, want none", runner.Calls)
	}
}

func TestRunLaunchSuccessStartsWithSessionLookupAndIsSilent(t *testing.T) {
	runner := tmuxx.NewFakeRunner(launchOneRoleResponses("")...)
	var stdout, stderr bytes.Buffer

	code := runWith([]string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &stdout, &stderr, launchTestDependencies(runner))

	if code != exitOK {
		t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("output = stdout %q stderr %q, want clean output", stdout.String(), stderr.String())
	}
	if got, want := runner.Calls[0], (tmuxx.Call{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("first runner call = %#v, want %#v", got, want)
	}
}

func TestRunLaunchMapsSessionCollisionWithoutKilling(t *testing.T) {
	runner := tmuxx.NewFakeRunner(tmuxx.Response{Stdout: []byte("$3\tfleet\n")})
	var stdout, stderr bytes.Buffer

	code := runWith([]string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &stdout, &stderr, launchTestDependencies(runner))

	if code != exitSession {
		t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitSession, stderr.String())
	}
	if got, want := stderr.String(), "agentctl: session \"fleet\" already exists\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
	assertNoKillCall(t, runner)
}

func TestRunLaunchMapsCreationErrorWithMayExistWarningAndNoKill(t *testing.T) {
	runner := tmuxx.NewFakeRunner(tmuxx.Response{}, tmuxx.Response{Stdout: []byte("malformed\n")})
	var stdout, stderr bytes.Buffer

	code := runWith([]string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &stdout, &stderr, launchTestDependencies(runner))

	if code != exitTmux {
		t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitTmux, stderr.String())
	}
	const warning = "a session named fleet may exist; inspect with tmux ls"
	if got := stderr.String(); !bytes.Contains([]byte(got), []byte(warning)) {
		t.Fatalf("stderr = %q, want operator warning %q", got, warning)
	}
	assertNoKillCall(t, runner)
}

func TestRunLaunchMapsOrdinaryTmuxErrorToExitTmux(t *testing.T) {
	runner := tmuxx.NewFakeRunner(tmuxx.Response{Err: errors.New("list failed")})
	var stdout, stderr bytes.Buffer

	code := runWith([]string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &stdout, &stderr, launchTestDependencies(runner))

	if code != exitTmux {
		t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitTmux, stderr.String())
	}
	if got, want := stderr.String(), "agentctl: tmux list sessions: list failed\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestRunLaunchReportsCleanupOutcomeVerbatim(t *testing.T) {
	cause := errors.New("metadata failed")
	cleanupCause := errors.New("cleanup failed")
	for _, tt := range []struct {
		name      string
		responses []tmuxx.Response
		want      string
	}{
		{
			name:      "cleanup succeeds",
			responses: []tmuxx.Response{{}, {Stdout: []byte("$17\t@23\t%42\t4242\n")}, {Err: cause}, {}},
			want:      "agentctl: failed to launch planner; removed incomplete session fleet\n",
		},
		{
			name:      "cleanup fails",
			responses: []tmuxx.Response{{}, {Stdout: []byte("$17\t@23\t%42\t4242\n")}, {Err: cause}, {Err: cleanupCause}},
			want:      "agentctl: failed to launch planner; failed to remove incomplete session fleet: tmux kill session: cleanup failed\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner(tt.responses...)
			var stdout, stderr bytes.Buffer

			code := runWith([]string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &stdout, &stderr, launchTestDependencies(runner))

			if code != exitLaunch {
				t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitLaunch, stderr.String())
			}
			if got := stderr.String(); got != tt.want {
				t.Fatalf("stderr = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunLaunchMultiRoleTranscriptUsesValidatedRosterAndReturnedIDs(t *testing.T) {
	runner := tmuxx.NewFakeRunner(launchTwoRoleResponses()...)
	var stdout, stderr bytes.Buffer

	code := runWith([]string{
		"launch", "--session", "fleet", "--roles", "planner:claude,reviewer:codex",
		"--models", "reviewer:gpt-5.6", "--dir", "/fleet workspace",
	}, &stdout, &stderr, launchTestDependencies(runner))

	if code != exitOK {
		t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("output = stdout %q stderr %q, want clean output", stdout.String(), stderr.String())
	}
	assertLaunchCalls(t, runner,
		tmuxx.Call{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"new-session", "-d", "-s", "fleet", "-n", "planner", "-c", "/fleet workspace", "-P", "-F", "#{session_id}\t#{window_id}\t#{pane_id}\t#{pane_pid}", "--", "exec 'amq' 'coop' 'exec' '--session' 'fleet' '--me' 'planner' 'claude'"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_managed", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_version", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_roles", "planner,reviewer"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_managed", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_role", "planner"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_harness", "claude"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_model", ""}},
		tmuxx.Call{Executable: "ps", Args: []string{"-o", "comm=", "-p", "4242"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_process", "claude"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"new-window", "-d", "-t", "$17", "-n", "reviewer", "-c", "/fleet workspace", "-P", "-F", "#{window_id}\t#{pane_id}\t#{pane_pid}", "--", "exec 'amq' 'coop' 'exec' '--session' 'fleet' '--me' 'reviewer' 'codex' '--' '--model' 'gpt-5.6'"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_managed", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_role", "reviewer"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_harness", "codex"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_model", "gpt-5.6"}},
		tmuxx.Call{Executable: "ps", Args: []string{"-o", "comm=", "-p", "8686"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_process", "codex"}},
	)
}

func launchTestDependencies(runner tmuxx.Runner) launchDependencies {
	return launchDependencies{runner: runner, fleet: fleet.Dependencies{
		LookPath: func(name string) (string, error) { return "/bin/" + name, nil },
		Getwd:    func() (string, error) { return "/invocation", nil },
		Stat: func(string) (fs.FileInfo, error) {
			return launchTestFileInfo{mode: fs.ModeDir | 0o755}, nil
		},
	}}
}

func launchOneRoleResponses(sessions string) []tmuxx.Response {
	return []tmuxx.Response{
		{Stdout: []byte(sessions)},
		{Stdout: []byte("$17\t@23\t%42\t4242\n")},
		{}, {}, {}, {}, {}, {}, {},
		{Stdout: []byte("claude\n")}, {},
	}
}

func launchTwoRoleResponses() []tmuxx.Response {
	return []tmuxx.Response{
		{},
		{Stdout: []byte("$17\t@23\t%42\t4242\n")},
		{}, {}, {}, {}, {}, {}, {}, {Stdout: []byte("claude\n")}, {},
		{Stdout: []byte("@65\t%87\t8686\n")},
		{}, {}, {}, {}, {Stdout: []byte("codex\n")}, {},
	}
}

func assertNoKillCall(t *testing.T, runner *tmuxx.FakeRunner) {
	t.Helper()
	for _, call := range runner.Calls {
		if call.Executable == "tmux" && len(call.Args) != 0 && call.Args[0] == "kill-session" {
			t.Fatalf("runner calls = %#v, want no kill-session", runner.Calls)
		}
	}
}

func assertLaunchCalls(t *testing.T, runner *tmuxx.FakeRunner, want ...tmuxx.Call) {
	t.Helper()
	if !reflect.DeepEqual(runner.Calls, want) {
		t.Fatalf("runner calls = %#v, want %#v", runner.Calls, want)
	}
}
