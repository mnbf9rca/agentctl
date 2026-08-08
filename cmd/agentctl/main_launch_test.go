package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"reflect"
	"strconv"
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
		{name: "explicitly empty efforts", args: []string{"launch", "--session", "fleet", "--roles", "planner:claude", "--efforts="}, want: "--efforts value \"\": must not be empty"},
		{name: "unsupported effort level", args: []string{"launch", "--session", "fleet", "--roles", "planner:claude", "--efforts", "planner:turbo"}, want: "harness \"claude\" does not support effort \"turbo\"; supported levels are low, medium, high, xhigh, max"},
		{name: "effort for undefined role", args: []string{"launch", "--session", "fleet", "--roles", "planner:claude", "--efforts", "worker:high"}, want: "effort references undefined role \"worker\""},
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

func TestRunLaunchSuccessRendersObservedStatus(t *testing.T) {
	responses := append(launchOneRoleResponses(""), healthyPostLaunchResponses()...)
	runner := tmuxx.NewFakeRunner(responses...)
	var stdout, stderr bytes.Buffer

	code := runWith([]string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &stdout, &stderr, launchTestDependencies(runner))

	if code != exitOK {
		t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	want := "SESSION  ROLE     HARNESS  MODEL    EFFORT   PANE  PROCESS  STATE\n" +
		"fleet    planner  claude   default  default  %42   claude   running\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if got, want := runner.Calls[0], (tmuxx.Call{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("first runner call = %#v, want %#v", got, want)
	}
	wantPostLaunch := postLaunchStatusCalls()
	if len(runner.Calls) < len(wantPostLaunch) {
		t.Fatalf("runner calls = %#v, want post-launch suffix %#v", runner.Calls, wantPostLaunch)
	}
	if got := runner.Calls[len(runner.Calls)-len(wantPostLaunch):]; !reflect.DeepEqual(got, wantPostLaunch) {
		t.Fatalf("post-launch runner calls = %#v, want %#v", got, wantPostLaunch)
	}
}

func TestRunLaunchRendersObservedMissingRoleWithoutChangingSuccess(t *testing.T) {
	responses := append(launchOneRoleResponses(""),
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("planner\n")},
		tmuxx.Response{},
	)
	runner := tmuxx.NewFakeRunner(responses...)
	var stdout, stderr bytes.Buffer

	code := runWith([]string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &stdout, &stderr, launchTestDependencies(runner))

	if code != exitOK {
		t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	want := "SESSION  ROLE     HARNESS  MODEL    EFFORT   PANE  PROCESS  STATE\n" +
		"fleet    planner           default  default                 missing\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunLaunchReportsUnverifiedConfirmationWithoutChangingSuccess(t *testing.T) {
	responses := append(launchOneRoleResponses(""), tmuxx.Response{Err: errors.New("observation failed")})
	runner := tmuxx.NewFakeRunner(responses...)
	var stdout, stderr bytes.Buffer

	code := runWith([]string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &stdout, &stderr, launchTestDependencies(runner))

	if code != exitOK {
		t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	want := "agentctl: session \"fleet\" launched, but post-launch status could not be confirmed: tmux show session option: observation failed\n"
	if got := stderr.String(); got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestRunLaunchReportsSessionEnvironmentClearFailureButStillSucceeds(t *testing.T) {
	responses := launchOneRoleResponses("")
	responses[16] = tmuxx.Response{Err: errors.New("permission denied")}
	responses = append(responses, healthyPostLaunchResponses()...)
	runner := tmuxx.NewFakeRunner(responses...)
	var stdout, stderr bytes.Buffer

	code := runWith([]string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &stdout, &stderr, launchTestDependencies(runner))

	if code != exitOK {
		t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	wantStatus := "SESSION  ROLE     HARNESS  MODEL    EFFORT   PANE  PROCESS  STATE\n" +
		"fleet    planner  claude   default  default  %42   claude   running\n"
	if got := stdout.String(); got != wantStatus {
		t.Fatalf("stdout = %q, want %q", got, wantStatus)
	}
	want := "agentctl: could not clear AGENTCTL_ROLE from the tmux session environment; windows created by hand may inherit the first role's identity: tmux clear session environment: permission denied\n"
	if got := stderr.String(); got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
	assertNoKillCall(t, runner)
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
	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{},
		tmuxx.Response{Err: errors.New("create failed")},
	)
	var stdout, stderr bytes.Buffer

	code := runWith([]string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &stdout, &stderr, launchTestDependencies(runner))

	if code != exitTmux {
		t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitTmux, stderr.String())
	}
	if got, want := stderr.String(), "agentctl: tmux create session: create failed\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestRunLaunchClassifiesDuplicateSessionRace(t *testing.T) {
	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Err: errors.New("no tmux server")},
		tmuxx.Response{Err: launchExitError(t, 1, "duplicate session: fleet")},
	)
	var stdout, stderr bytes.Buffer

	code := runWith([]string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &stdout, &stderr, launchTestDependencies(runner))

	if code != exitSession {
		t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitSession, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got, want := stderr.String(), "agentctl: tmux create session: exit status 1: duplicate session: fleet\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
	assertLaunchCalls(t, runner,
		tmuxx.Call{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"new-session", "-d", "-s", "fleet", "-n", "planner", "-c", "/invocation", "-e", "AGENTCTL_SESSION=fleet", "-e", "AGENTCTL_ROLE=planner", "-e", "AGENTCTL_MANAGED=1", "-P", "-F", "#{session_id}\t#{window_id}\t#{pane_id}\t#{pane_pid}", "--", "exec 'amq' 'coop' 'exec' '--session' 'fleet' '--me' 'planner' 'claude'"}},
	)
}

func TestRunLaunchBoundsDuplicateSessionRaceClassification(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		message  string
	}{
		{name: "matching words inside unrelated stderr", exitCode: 1, message: "permission denied: duplicate session: fleet"},
		{name: "duplicate refusal has an unrelated suffix", exitCode: 1, message: "duplicate session: fleet: permission denied"},
		{name: "matching stderr from a different exit status", exitCode: 23, message: "duplicate session: fleet"},
		{name: "duplicate refusal names a different session", exitCode: 1, message: "duplicate session: other"},
		{name: "duplicate refusal has an extra stderr line", exitCode: 1, message: "duplicate session: fleet\npermission denied"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner(
				tmuxx.Response{},
				tmuxx.Response{Err: launchExitError(t, tt.exitCode, tt.message)},
			)
			var stdout, stderr bytes.Buffer

			code := runWith([]string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &stdout, &stderr, launchTestDependencies(runner))

			if code != exitTmux {
				t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitTmux, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if got, want := stderr.String(), fmt.Sprintf("agentctl: tmux create session: exit status %d: %s\n", tt.exitCode, tt.message); got != want {
				t.Fatalf("stderr = %q, want %q", got, want)
			}
			assertNoKillCall(t, runner)
		})
	}
}

func TestRunLaunchClassifiesDuplicateSessionRaceLineEndings(t *testing.T) {
	tests := []struct {
		name       string
		terminator string
	}{
		{name: "no terminator"},
		{name: "CRLF", terminator: "\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner(
				tmuxx.Response{},
				tmuxx.Response{Err: launchExitErrorWithTerminator(t, 1, "duplicate session: fleet", tt.terminator)},
			)
			var stdout, stderr bytes.Buffer

			code := runWith([]string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &stdout, &stderr, launchTestDependencies(runner))

			if code != exitSession {
				t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitSession, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if got, want := stderr.String(), "agentctl: tmux create session: exit status 1: duplicate session: fleet\n"; got != want {
				t.Fatalf("stderr = %q, want %q", got, want)
			}
			assertNoKillCall(t, runner)
		})
	}
}

func launchExitError(t *testing.T, exitCode int, message string) error {
	t.Helper()
	return launchExitErrorWithTerminator(t, exitCode, message, "\n")
}

func launchExitErrorWithTerminator(t *testing.T, exitCode int, message, terminator string) error {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestLaunchExitErrorHelper$")
	command.Env = append(os.Environ(),
		"GO_WANT_LAUNCH_EXIT_ERROR_HELPER=1",
		"LAUNCH_EXIT_CODE="+strconv.Itoa(exitCode),
		"LAUNCH_EXIT_STDERR="+message,
		"LAUNCH_EXIT_TERMINATOR="+terminator,
	)
	_, err := command.Output()
	if err == nil {
		t.Fatal("launch exit-error helper returned nil, want nonzero exit")
	}
	return err
}

func TestLaunchExitErrorHelper(t *testing.T) {
	if os.Getenv("GO_WANT_LAUNCH_EXIT_ERROR_HELPER") != "1" {
		return
	}
	fmt.Fprint(os.Stderr, os.Getenv("LAUNCH_EXIT_STDERR"), os.Getenv("LAUNCH_EXIT_TERMINATOR"))
	exitCode, err := strconv.Atoi(os.Getenv("LAUNCH_EXIT_CODE"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(exitCode)
}

func TestRunNonLaunchCommandsKeepNoServerFailure(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		lookup map[string]string
	}{
		{name: "status", args: []string{"status", "--session", "fleet", "--json"}},
		{name: "kill", args: []string{"kill", "--session", "fleet"}},
		{name: "attach", args: []string{"attach", "--session", "fleet"}, lookup: map[string]string{"TERM_PROGRAM": "iTerm.app"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner(tmuxx.Response{Err: launchExitError(t, 23, "no server running")})
			var stdout, stderr bytes.Buffer

			code := runWithRunner(context.Background(), tt.args, &stdout, &stderr, runner, lookupValues(tt.lookup))

			if code != exitTmux {
				t.Fatalf("runWithRunner() = %d, want %d; stderr = %q", code, exitTmux, stderr.String())
			}
			if got, want := stderr.String(), "agentctl: tmux list sessions: exit status 23: no server running\n"; got != want {
				t.Fatalf("stderr = %q, want %q", got, want)
			}
			if got, want := runner.Calls, []tmuxx.Call{{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}}}; !reflect.DeepEqual(got, want) {
				t.Fatalf("Runner calls = %#v, want plain non-launch list-sessions %#v", got, want)
			}
		})
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
			want:      "agentctl: failed to launch planner; removed incomplete session fleet: tmux set session option: metadata failed\n",
		},
		{
			name:      "cleanup fails",
			responses: []tmuxx.Response{{}, {Stdout: []byte("$17\t@23\t%42\t4242\n")}, {Err: cause}, {Err: cleanupCause}},
			want:      "agentctl: failed to launch planner; failed to remove incomplete session fleet: tmux kill session: cleanup failed (launch failure: tmux set session option: metadata failed)\n",
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
	responses := append(launchTwoRoleResponses(), healthyMultiRolePostLaunchResponses()...)
	runner := tmuxx.NewFakeRunner(responses...)
	var stdout, stderr bytes.Buffer

	code := runWith([]string{
		"launch", "--session", "fleet", "--roles", "planner:claude,reviewer:codex",
		"--models", "reviewer:gpt-5.6", "--efforts", "planner:high", "--dir", "/fleet workspace",
	}, &stdout, &stderr, launchTestDependencies(runner))

	if code != exitOK {
		t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	wantStatus := "SESSION  ROLE      HARNESS  MODEL    EFFORT   PANE  PROCESS  STATE\n" +
		"fleet    planner   claude   default  high     %42   claude   running\n" +
		"fleet    reviewer  codex    gpt-5.6  default  %87   codex    running\n"
	if got := stdout.String(); got != wantStatus {
		t.Fatalf("stdout = %q, want %q", got, wantStatus)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertLaunchCalls(t, runner,
		tmuxx.Call{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"new-session", "-d", "-s", "fleet", "-n", "planner", "-c", "/fleet workspace", "-e", "AGENTCTL_SESSION=fleet", "-e", "AGENTCTL_ROLE=planner", "-e", "AGENTCTL_MANAGED=1", "-P", "-F", "#{session_id}\t#{window_id}\t#{pane_id}\t#{pane_pid}", "--", "exec 'amq' 'coop' 'exec' '--session' 'fleet' '--me' 'planner' 'claude' '--' '--effort' 'high'"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_managed", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_version", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_roles", "planner,reviewer"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_fleet", "planner:claude::high,reviewer:codex:gpt-5.6:"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_dir", "/fleet workspace"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_managed", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_role", "planner"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_harness", "claude"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_model", ""}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_effort", "high"}},
		tmuxx.Call{Executable: "ps", Args: []string{"-o", "comm=", "-p", "4242"}},
		tmuxx.Call{Executable: "ps", Args: []string{"-o", "comm=", "-p", "4242"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_process", "claude"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-environment", "-t", "$17", "-u", "AGENTCTL_SESSION"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-environment", "-t", "$17", "-u", "AGENTCTL_ROLE"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-environment", "-t", "$17", "-u", "AGENTCTL_MANAGED"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"new-window", "-d", "-t", "$17", "-n", "reviewer", "-c", "/fleet workspace", "-e", "AGENTCTL_SESSION=fleet", "-e", "AGENTCTL_ROLE=reviewer", "-e", "AGENTCTL_MANAGED=1", "-P", "-F", "#{window_id}\t#{pane_id}\t#{pane_pid}", "--", "exec 'amq' 'coop' 'exec' '--session' 'fleet' '--me' 'reviewer' 'codex' '--' '--model' 'gpt-5.6'"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_managed", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_role", "reviewer"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_harness", "codex"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_model", "gpt-5.6"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_effort", ""}},
		tmuxx.Call{Executable: "ps", Args: []string{"-o", "comm=", "-p", "8686"}},
		tmuxx.Call{Executable: "ps", Args: []string{"-o", "comm=", "-p", "8686"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_process", "codex"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$17", "@agentctl_managed"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$17", "@agentctl_version"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$17", "@agentctl_roles"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"list-windows", "-t", "$17", "-F", "#{window_id}\t#{window_name}\t#{@agentctl_role}\t#{@agentctl_harness}\t#{@agentctl_model}\t#{@agentctl_effort}\t#{@agentctl_process}"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"list-panes", "-t", "@23", "-F", "#{pane_id}\t#{pane_pid}\t#{pane_dead}\t#{window_panes}"}},
		tmuxx.Call{Executable: "ps", Args: []string{"-o", "comm=", "-p", "4242"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"list-panes", "-t", "@65", "-F", "#{pane_id}\t#{pane_pid}\t#{pane_dead}\t#{window_panes}"}},
		tmuxx.Call{Executable: "ps", Args: []string{"-o", "comm=", "-p", "8686"}},
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
		{}, {}, {}, {}, {}, {}, {}, {}, {}, {},
		{Stdout: []byte("claude\n")}, {Stdout: []byte("claude\n")}, {},
		{}, {}, {},
	}
}

func launchTwoRoleResponses() []tmuxx.Response {
	return []tmuxx.Response{
		{},
		{Stdout: []byte("$17\t@23\t%42\t4242\n")},
		{}, {}, {}, {}, {}, {}, {}, {}, {}, {}, {Stdout: []byte("claude\n")}, {Stdout: []byte("claude\n")}, {},
		{}, {}, {},
		{Stdout: []byte("@65\t%87\t8686\n")},
		{}, {}, {}, {}, {}, {Stdout: []byte("codex\n")}, {Stdout: []byte("codex\n")}, {},
	}
}

func healthyPostLaunchResponses() []tmuxx.Response {
	return []tmuxx.Response{
		{Stdout: []byte("1\n")},
		{Stdout: []byte("1\n")},
		{Stdout: []byte("planner\n")},
		{Stdout: []byte("@23\tplanner\tplanner\tclaude\t\t\tclaude\n")},
		{Stdout: []byte("%42\t4242\t0\t1\n")},
		{Stdout: []byte("claude\n")},
	}
}

func healthyMultiRolePostLaunchResponses() []tmuxx.Response {
	return []tmuxx.Response{
		{Stdout: []byte("1\n")},
		{Stdout: []byte("1\n")},
		{Stdout: []byte("planner,reviewer\n")},
		{Stdout: []byte("@23\tplanner\tplanner\tclaude\t\thigh\tclaude\n@65\treviewer\treviewer\tcodex\tgpt-5.6\t\tcodex\n")},
		{Stdout: []byte("%42\t4242\t0\t1\n")},
		{Stdout: []byte("claude\n")},
		{Stdout: []byte("%87\t8686\t0\t1\n")},
		{Stdout: []byte("codex\n")},
	}
}

func postLaunchStatusCalls() []tmuxx.Call {
	return []tmuxx.Call{
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$17", "@agentctl_managed"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$17", "@agentctl_version"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$17", "@agentctl_roles"}},
		{Executable: "tmux", Args: []string{"list-windows", "-t", "$17", "-F", "#{window_id}\t#{window_name}\t#{@agentctl_role}\t#{@agentctl_harness}\t#{@agentctl_model}\t#{@agentctl_effort}\t#{@agentctl_process}"}},
		{Executable: "tmux", Args: []string{"list-panes", "-t", "@23", "-F", "#{pane_id}\t#{pane_pid}\t#{pane_dead}\t#{window_panes}"}},
		{Executable: "ps", Args: []string{"-o", "comm=", "-p", "4242"}},
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
