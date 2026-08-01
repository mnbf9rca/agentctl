package main

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/kill"
	"github.com/mnbf9rca/agentctl/internal/session"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestRunRejectsUnknownCommandWithUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"bogus"}, &stdout, &stderr)

	if code != exitUsage {
		t.Fatalf("run() = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "unknown command") || !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("stderr = %q, want concise error and usage", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestRunRejectsNoCommandWithUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run(nil, &stdout, &stderr)

	if code != exitUsage {
		t.Fatalf("run() = %d, want %d", code, exitUsage)
	}
	const want = "agentctl: command required\nUsage: agentctl COMMAND [OPTIONS]\n"
	if !strings.HasPrefix(stderr.String(), want) {
		t.Fatalf("stderr = %q, want prefix %q", stderr.String(), want)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestRunRejectsEveryDuplicateOptionSpelling(t *testing.T) {
	const launchUsage = "Usage: agentctl launch --session SESSION --roles ROLE:HARNESS,... [--models ROLE:MODEL,...] [--dir PATH]\n"
	const statusUsage = "Usage: agentctl status [--session SESSION] [--json]\n\n" +
		"Exited agents normally report missing, not dead, because managed windows do not use remain-on-exit.\n"
	tests := []struct {
		name   string
		args   []string
		option string
		usage  string
	}{
		{name: "session spaced", args: []string{"launch", "--session", "one", "--session", "two", "--roles", "planner:claude"}, option: "session", usage: launchUsage},
		{name: "session equals", args: []string{"launch", "--session=one", "--session=two", "--roles", "planner:claude"}, option: "session", usage: launchUsage},
		{name: "roles spaced", args: []string{"launch", "--session", "fleet", "--roles", "planner:claude", "--roles", "reviewer:codex"}, option: "roles", usage: launchUsage},
		{name: "roles equals", args: []string{"launch", "--session", "fleet", "--roles=planner:claude", "--roles=reviewer:codex"}, option: "roles", usage: launchUsage},
		{name: "models spaced", args: []string{"launch", "--session", "fleet", "--roles", "planner:claude", "--models", "planner:fable", "--models", "planner:opus"}, option: "models", usage: launchUsage},
		{name: "models equals", args: []string{"launch", "--session", "fleet", "--roles", "planner:claude", "--models=planner:fable", "--models=planner:opus"}, option: "models", usage: launchUsage},
		{name: "dir spaced", args: []string{"launch", "--session", "fleet", "--roles", "planner:claude", "--dir", "/tmp", "--dir", "/var/tmp"}, option: "dir", usage: launchUsage},
		{name: "dir equals", args: []string{"launch", "--session", "fleet", "--roles", "planner:claude", "--dir=/tmp", "--dir=/var/tmp"}, option: "dir", usage: launchUsage},
		{name: "json spaced", args: []string{"status", "--json", "--json"}, option: "json", usage: statusUsage},
		{name: "json equals", args: []string{"status", "--json=true", "--json=false"}, option: "json", usage: statusUsage},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(tt.args, &stdout, &stderr); code != exitUsage {
				t.Fatalf("run(%q) = %d, want %d", tt.args, code, exitUsage)
			}
			want := "agentctl: --" + tt.option + " provided more than once\n" + tt.usage
			if stderr.String() != want {
				t.Fatalf("stderr = %q, want %q", stderr.String(), want)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
		})
	}
}

func TestRunRejectsExplicitlyEmptyModels(t *testing.T) {
	tests := [][]string{
		{"launch", "--session", "fleet", "--roles", "planner:claude", "--models="},
		{"launch", "--session", "fleet", "--roles", "planner:claude", "--models", ""},
	}

	for _, arguments := range tests {
		var stdout, stderr bytes.Buffer
		if code := run(arguments, &stdout, &stderr); code != exitUsage {
			t.Fatalf("run(%q) = %d, want %d", arguments, code, exitUsage)
		}
		if !strings.Contains(stderr.String(), "must not be empty") || !strings.Contains(stderr.String(), "Usage:") {
			t.Fatalf("stderr = %q, want empty-model error and usage", stderr.String())
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout = %q, want empty", stdout.String())
		}
	}
}

func TestRunAcceptsEachNonLaunchCommandShapeBeforeReachingStub(t *testing.T) {
	resolver := resolverFunc(func(context.Context, *string) (tmuxx.Session, error) {
		return tmuxx.Session{ID: "$1", Name: "fleet"}, nil
	})
	tests := []struct {
		name string
		args []string
	}{
		{name: "attach", args: []string{"attach", "--session", "fleet"}},
		{name: "clear", args: []string{"clear", "--session", "fleet", "planner"}},
		{name: "compact", args: []string{"compact", "--session", "fleet", "planner"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runWithResolver(context.Background(), tt.args, &stdout, &stderr, resolver)
			if code != exitNotImplemented {
				t.Fatalf("run(%q) = %d, want %d; stderr = %q", tt.args, code, exitNotImplemented, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.name+": not implemented") {
				t.Fatalf("stderr = %q, want command stub message", stderr.String())
			}
		})
	}
}

func TestRunMapsSessionResolverErrorsToOwnedExitCodes(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		lookup    session.LookupEnv
		responses []tmuxx.Response
		wantCode  int
		wantText  string
		wantUsage bool
		wantCalls int
	}{
		{
			name:      "explicit empty is usage",
			args:      []string{"status", "--session="},
			lookup:    lookupValues(map[string]string{"AGENTCTL_SESSION": "environment", "TMUX_PANE": "%9"}),
			wantCode:  exitUsage,
			wantText:  "invalid session",
			wantUsage: true,
		},
		{
			name:     "no permitted source is session error",
			args:     []string{"status"},
			lookup:   lookupValues(map[string]string{"AM_ROOT": "/tmp/fleet", "AM_SESSION": "fleet", "TMUX": "server"}),
			wantCode: exitSession,
			wantText: "session could not be resolved",
		},
		{
			name:     "invalid environment is session error",
			args:     []string{"status"},
			lookup:   lookupValues(map[string]string{"AGENTCTL_SESSION": "INVALID", "TMUX_PANE": "%9"}),
			wantCode: exitSession,
			wantText: "AGENTCTL_SESSION",
		},
		{
			name:      "tmux failure keeps cause",
			args:      []string{"status", "--session", "fleet"},
			lookup:    lookupValues(nil),
			responses: []tmuxx.Response{{Err: errors.New("no server running")}},
			wantCode:  exitTmux,
			wantText:  "no server running",
			wantCalls: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner(tt.responses...)
			resolver := session.New(tmuxx.New(runner), tt.lookup)
			var stdout, stderr bytes.Buffer
			code := runWithResolver(context.Background(), tt.args, &stdout, &stderr, resolver)
			if code != tt.wantCode {
				t.Fatalf("runWithResolver() = %d, want %d; stderr = %q", code, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantText) {
				t.Fatalf("stderr = %q, want substring %q", stderr.String(), tt.wantText)
			}
			if got := strings.Contains(stderr.String(), "Usage:"); got != tt.wantUsage {
				t.Fatalf("stderr contains usage = %v, want %v; stderr = %q", got, tt.wantUsage, stderr.String())
			}
			if len(runner.Calls) != tt.wantCalls {
				t.Fatalf("Calls = %#v, want %d calls", runner.Calls, tt.wantCalls)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
		})
	}
}

func TestRunValidNonLaunchResolutionReachesCommandStub(t *testing.T) {
	runner := tmuxx.NewFakeRunner(tmuxx.Response{Stdout: []byte("$4\tfleet\n")})
	resolver := session.New(tmuxx.New(runner), lookupValues(nil))
	var stdout, stderr bytes.Buffer

	code := runWithResolver(context.Background(), []string{"status", "--session", "fleet"}, &stdout, &stderr, resolver)

	if code != exitNotImplemented {
		t.Fatalf("runWithResolver() = %d, want %d; stderr = %q", code, exitNotImplemented, stderr.String())
	}
	if !strings.Contains(stderr.String(), "status: not implemented") {
		t.Fatalf("stderr = %q, want status stub", stderr.String())
	}
}

func TestRunKillExecutesManagedSessionByResolvedID(t *testing.T) {
	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{},
	)
	resolver := resolverFunc(func(context.Context, *string) (tmuxx.Session, error) {
		return tmuxx.Session{ID: "$4", Name: "fleet"}, nil
	})
	var stdout, stderr bytes.Buffer

	code := runWithDependencies(context.Background(), []string{"kill", "--session", "fleet"}, &stdout, &stderr, resolver, kill.New(tmuxx.New(runner)))

	if code != exitOK {
		t.Fatalf("runWithDependencies() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	wantCalls := []tmuxx.Call{
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
		{Executable: "tmux", Args: []string{"kill-session", "-t", "$4"}},
	}
	if !reflect.DeepEqual(runner.Calls, wantCalls) {
		t.Fatalf("Calls = %#v, want %#v", runner.Calls, wantCalls)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q; want both empty", stdout.String(), stderr.String())
	}
}

func TestRunKillRefusalsMapToSessionExitWithoutKilling(t *testing.T) {
	tests := []struct {
		name      string
		responses []tmuxx.Response
		wantCalls []tmuxx.Call
		wantText  string
	}{
		{
			name:      "unmanaged",
			responses: []tmuxx.Response{{Stdout: nil}},
			wantCalls: []tmuxx.Call{{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}}},
			wantText:  "not managed",
		},
		{
			name:      "different version",
			responses: []tmuxx.Response{{Stdout: []byte("1\n")}, {Stdout: []byte("2\n")}},
			wantCalls: []tmuxx.Call{
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
			},
			wantText: "different agentctl version",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner(tt.responses...)
			resolver := resolverFunc(func(context.Context, *string) (tmuxx.Session, error) {
				return tmuxx.Session{ID: "$4", Name: "fleet"}, nil
			})
			var stdout, stderr bytes.Buffer

			code := runWithDependencies(context.Background(), []string{"kill", "--session", "fleet"}, &stdout, &stderr, resolver, kill.New(tmuxx.New(runner)))

			if code != exitSession {
				t.Fatalf("runWithDependencies() = %d, want %d; stderr = %q", code, exitSession, stderr.String())
			}
			if !strings.Contains(stderr.String(), "fleet") || !strings.Contains(stderr.String(), tt.wantText) {
				t.Fatalf("stderr = %q, want session name and %q", stderr.String(), tt.wantText)
			}
			if !reflect.DeepEqual(runner.Calls, tt.wantCalls) {
				t.Fatalf("Calls = %#v, want %#v", runner.Calls, tt.wantCalls)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
		})
	}
}

func TestRunKillMissingSessionStopsBeforeOwnershipChecks(t *testing.T) {
	resolver := resolverFunc(func(context.Context, *string) (tmuxx.Session, error) {
		return tmuxx.Session{}, &session.ResolutionError{Name: "fleet"}
	})
	killCalled := false
	killer := sessionKillerFunc(func(context.Context, tmuxx.Session) error {
		killCalled = true
		return nil
	})
	var stdout, stderr bytes.Buffer

	code := runWithDependencies(context.Background(), []string{"kill", "--session", "fleet"}, &stdout, &stderr, resolver, killer)

	if code != exitSession {
		t.Fatalf("runWithDependencies() = %d, want %d; stderr = %q", code, exitSession, stderr.String())
	}
	if killCalled {
		t.Fatal("session killer was called for a missing session")
	}
	if !strings.Contains(stderr.String(), "not found") {
		t.Fatalf("stderr = %q, want missing-session error", stderr.String())
	}
}

func TestRunKillTmuxFailuresMapToTmuxExit(t *testing.T) {
	tests := []struct {
		name      string
		responses []tmuxx.Response
	}{
		{name: "managed read", responses: []tmuxx.Response{{Err: errors.New("managed read failed")}}},
		{name: "version read", responses: []tmuxx.Response{{Stdout: []byte("1\n")}, {Err: errors.New("version read failed")}}},
		{name: "kill", responses: []tmuxx.Response{{Stdout: []byte("1\n")}, {Stdout: []byte("1\n")}, {Err: errors.New("kill failed")}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner(tt.responses...)
			resolver := resolverFunc(func(context.Context, *string) (tmuxx.Session, error) {
				return tmuxx.Session{ID: "$4", Name: "fleet"}, nil
			})
			var stdout, stderr bytes.Buffer

			code := runWithDependencies(context.Background(), []string{"kill"}, &stdout, &stderr, resolver, kill.New(tmuxx.New(runner)))

			if code != exitTmux {
				t.Fatalf("runWithDependencies() = %d, want %d; stderr = %q", code, exitTmux, stderr.String())
			}
			if !strings.Contains(stderr.String(), "failed") {
				t.Fatalf("stderr = %q, want tmux failure", stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
		})
	}
}

func TestRunMalformedTMUXPaneMapsToSessionErrorWithoutTmuxCall(t *testing.T) {
	for _, pane := range []string{"", "%", "7", "$1", "%abc", "not-a-pane"} {
		pane := pane
		t.Run(pane, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner()
			resolver := session.New(tmuxx.New(runner), lookupValues(map[string]string{"TMUX_PANE": pane}))
			var stdout, stderr bytes.Buffer

			code := runWithResolver(context.Background(), []string{"status"}, &stdout, &stderr, resolver)

			if code != exitSession {
				t.Fatalf("runWithResolver() = %d, want %d; stderr = %q", code, exitSession, stderr.String())
			}
			if !strings.Contains(stderr.String(), "current tmux session") {
				t.Fatalf("stderr = %q, want current-source error", stderr.String())
			}
			if len(runner.Calls) != 0 {
				t.Fatalf("Calls = %#v, want no tmux calls", runner.Calls)
			}
		})
	}
}

func TestRunLaunchRequiresAndValidatesExplicitSessionWithoutResolving(t *testing.T) {
	resolver := resolverFunc(func(context.Context, *string) (tmuxx.Session, error) {
		t.Fatal("launch called session resolver")
		return tmuxx.Session{}, nil
	})
	tests := []struct {
		name     string
		args     []string
		wantCode int
	}{
		{name: "missing", args: []string{"launch", "--roles", "planner:claude"}, wantCode: exitUsage},
		{name: "empty", args: []string{"launch", "--session=", "--roles", "planner:claude"}, wantCode: exitUsage},
		{name: "invalid", args: []string{"launch", "--session", "INVALID", "--roles", "planner:claude"}, wantCode: exitUsage},
		{name: "valid", args: []string{"launch", "--session", "fleet", "--roles", "planner:claude"}, wantCode: exitOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner(launchOneRoleResponses("")...)
			var stdout, stderr bytes.Buffer
			code := runWithAllDependencies(context.Background(), tt.args, &stdout, &stderr, launchTestDependencies(runner), resolver, nil, nil, nil)
			if code != tt.wantCode {
				t.Fatalf("runWithAllDependencies(%q) = %d, want %d; stderr = %q", tt.args, code, tt.wantCode, stderr.String())
			}
		})
	}
}

type resolverFunc func(context.Context, *string) (tmuxx.Session, error)

func (f resolverFunc) Resolve(ctx context.Context, explicit *string) (tmuxx.Session, error) {
	return f(ctx, explicit)
}

type sessionKillerFunc func(context.Context, tmuxx.Session) error

func (f sessionKillerFunc) Execute(ctx context.Context, target tmuxx.Session) error {
	return f(ctx, target)
}

func lookupValues(values map[string]string) session.LookupEnv {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func TestRunRejectsInvalidCommandShapes(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "launch requires session", args: []string{"launch", "--roles", "planner:claude"}},
		{name: "launch requires roles", args: []string{"launch", "--session", "fleet"}},
		{name: "attach rejects positional", args: []string{"attach", "extra"}},
		{name: "status rejects positional", args: []string{"status", "extra"}},
		{name: "clear requires role", args: []string{"clear", "--session", "fleet"}},
		{name: "compact rejects extra role", args: []string{"compact", "planner", "extra"}},
		{name: "kill rejects positional", args: []string{"kill", "extra"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(tt.args, &stdout, &stderr); code != exitUsage {
				t.Fatalf("run(%q) = %d, want %d; stderr = %q", tt.args, code, exitUsage, stderr.String())
			}
			if !strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr = %q, want usage", stderr.String())
			}
		})
	}
}

func TestRunHelpWritesUsageToStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"status", "--help"}, &stdout, &stderr)

	if code != exitOK {
		t.Fatalf("run() = %d, want %d", code, exitOK)
	}
	if !strings.Contains(stdout.String(), "Usage: agentctl status") {
		t.Fatalf("stdout = %q, want status usage", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Exited agents normally report missing, not dead") {
		t.Fatalf("stdout = %q, want remain-on-exit status explanation", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunStatusResolvesSessionAndRendersSelectedFormat(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "human table",
			args: []string{"status", "--session", "fleet"},
			want: "SESSION  ROLE     HARNESS  MODEL    PANE  PROCESS  STATE\n" +
				"fleet    planner  claude   default  %12   claude   running\n",
		},
		{
			name: "json",
			args: []string{"status", "--session", "fleet", "--json"},
			want: "{\"schema\":1,\"session\":\"fleet\",\"managed\":true,\"agents\":[{\"role\":\"planner\",\"harness\":\"claude\",\"model\":\"\",\"window\":\"planner\",\"pane_id\":\"%12\",\"process\":\"claude\",\"state\":\"running\"}]}\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := healthyStatusRunner()
			var stdout, stderr bytes.Buffer
			code := runWithRunner(context.Background(), tt.args, &stdout, &stderr, runner, lookupValues(nil))
			if code != exitOK {
				t.Fatalf("runWithRunner() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
			}
			if stdout.String() != tt.want {
				t.Fatalf("stdout = %q, want %q", stdout.String(), tt.want)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}

			wantCalls := []tmuxx.Call{
				{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_roles"}},
				{Executable: "tmux", Args: []string{"list-windows", "-t", "$4", "-F", "#{window_id}\t#{window_name}\t#{@agentctl_managed}\t#{@agentctl_version}\t#{@agentctl_role}\t#{@agentctl_harness}\t#{@agentctl_model}\t#{@agentctl_process}"}},
				{Executable: "tmux", Args: []string{"list-panes", "-t", "@7", "-F", "#{pane_id}\t#{pane_pid}\t#{pane_dead}\t#{window_panes}"}},
				{Executable: "ps", Args: []string{"-o", "comm=", "-p", "111"}},
			}
			if len(runner.Calls) != len(wantCalls) {
				t.Fatalf("Calls = %#v, want %#v", runner.Calls, wantCalls)
			}
			for index := range wantCalls {
				if runner.Calls[index].Executable != wantCalls[index].Executable || !equalStrings(runner.Calls[index].Args, wantCalls[index].Args) {
					t.Fatalf("Calls[%d] = %#v, want %#v", index, runner.Calls[index], wantCalls[index])
				}
			}
		})
	}
}

func TestRunStatusMapsCollectorErrorsToOwnedExitCodes(t *testing.T) {
	tests := []struct {
		name      string
		responses []tmuxx.Response
		wantCode  int
		wantText  string
		exact     bool
	}{
		{
			name: "different version",
			responses: []tmuxx.Response{
				{Stdout: []byte("$4\tfleet\n")},
				{Stdout: []byte("1\n")},
				{Stdout: []byte("2\n")},
			},
			wantCode: exitSession,
			wantText: "created by a different agentctl version",
		},
		{
			name: "absent version marker",
			responses: []tmuxx.Response{
				{Stdout: []byte("$4\tfleet\n")},
				{Stdout: []byte("1\n")},
				{},
			},
			wantCode: exitSession,
			wantText: "agentctl: managed session carries no @agentctl_version marker\n",
			exact:    true,
		},
		{
			name: "tmux failure",
			responses: []tmuxx.Response{
				{Stdout: []byte("$4\tfleet\n")},
				{Err: errors.New("server disappeared")},
			},
			wantCode: exitTmux,
			wantText: "server disappeared",
		},
		{
			name: "absent roles roster",
			responses: []tmuxx.Response{
				{Stdout: []byte("$4\tfleet\n")},
				{Stdout: []byte("1\n")},
				{Stdout: []byte("1\n")},
				{},
			},
			wantCode: exitSession,
			wantText: "agentctl: managed session has no @agentctl_roles roster\n",
			exact:    true,
		},
		{
			name: "roster with empty entry",
			responses: []tmuxx.Response{
				{Stdout: []byte("$4\tfleet\n")},
				{Stdout: []byte("1\n")},
				{Stdout: []byte("1\n")},
				{Stdout: []byte("planner,,codex1\n")},
			},
			wantCode: exitSession,
			wantText: "agentctl: managed session has invalid @agentctl_roles roster \"planner,,codex1\"\n",
			exact:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner(tt.responses...)
			var stdout, stderr bytes.Buffer
			code := runWithRunner(
				context.Background(),
				[]string{"status", "--session", "fleet", "--json"},
				&stdout,
				&stderr,
				runner,
				lookupValues(nil),
			)
			if code != tt.wantCode {
				t.Fatalf("runWithRunner() = %d, want %d; stderr = %q", code, tt.wantCode, stderr.String())
			}
			if tt.exact && stderr.String() != tt.wantText {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tt.wantText)
			}
			if !tt.exact && !strings.Contains(stderr.String(), tt.wantText) {
				t.Fatalf("stderr = %q, want substring %q", stderr.String(), tt.wantText)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
		})
	}
}

func healthyStatusRunner() *tmuxx.FakeRunner {
	return tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("$4\tfleet\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("planner\n")},
		tmuxx.Response{Stdout: []byte("@7\tplanner\t1\t1\tplanner\tclaude\t\tclaude\n")},
		tmuxx.Response{Stdout: []byte("%12\t111\t0\t1\n")},
		tmuxx.Response{Stdout: []byte("claude\n")},
	)
}

func equalStrings(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}
