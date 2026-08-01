package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

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
	const statusUsage = "Usage: agentctl status [--session SESSION] [--json]\n"
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
		if !strings.Contains(stderr.String(), "--models must not be empty") || !strings.Contains(stderr.String(), "Usage:") {
			t.Fatalf("stderr = %q, want empty-model error and usage", stderr.String())
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout = %q, want empty", stdout.String())
		}
	}
}

func TestRunAcceptsOmittedModelsBeforeReachingStub(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &stdout, &stderr)

	if code == exitUsage {
		t.Fatalf("run() = usage error; stderr = %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "launch: not implemented") {
		t.Fatalf("stderr = %q, want launch stub message", stderr.String())
	}
}

func TestRunAcceptsEachCommandShapeBeforeReachingStub(t *testing.T) {
	resolver := resolverFunc(func(context.Context, *string) (tmuxx.Session, error) {
		return tmuxx.Session{ID: "$1", Name: "fleet"}, nil
	})
	tests := []struct {
		name string
		args []string
	}{
		{name: "launch", args: []string{"launch", "--session", "fleet", "--roles", "planner:claude", "--models", "planner:fable", "--dir", "/tmp"}},
		{name: "attach", args: []string{"attach", "--session", "fleet"}},
		{name: "status", args: []string{"status", "--session", "fleet", "--json"}},
		{name: "clear", args: []string{"clear", "--session", "fleet", "planner"}},
		{name: "compact", args: []string{"compact", "--session", "fleet", "planner"}},
		{name: "kill", args: []string{"kill", "--session", "fleet"}},
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

	code := runWithResolver(context.Background(), []string{"kill", "--session", "fleet"}, &stdout, &stderr, resolver)

	if code != exitNotImplemented {
		t.Fatalf("runWithResolver() = %d, want %d; stderr = %q", code, exitNotImplemented, stderr.String())
	}
	if !strings.Contains(stderr.String(), "kill: not implemented") {
		t.Fatalf("stderr = %q, want kill stub", stderr.String())
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
		{name: "valid", args: []string{"launch", "--session", "fleet", "--roles", "planner:claude"}, wantCode: exitNotImplemented},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runWithResolver(context.Background(), tt.args, &stdout, &stderr, resolver); code != tt.wantCode {
				t.Fatalf("runWithResolver(%q) = %d, want %d; stderr = %q", tt.args, code, tt.wantCode, stderr.String())
			}
		})
	}
}

type resolverFunc func(context.Context, *string) (tmuxx.Session, error)

func (f resolverFunc) Resolve(ctx context.Context, explicit *string) (tmuxx.Session, error) {
	return f(ctx, explicit)
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
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}
