package main

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/buildinfo"
	"github.com/mnbf9rca/agentctl/internal/kill"
	"github.com/mnbf9rca/agentctl/internal/session"
	statuspkg "github.com/mnbf9rca/agentctl/internal/status"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestRunVersion(t *testing.T) {
	restoreBuildStamp(t, "v0.1.0-test")

	for _, arguments := range [][]string{{"version"}, {"--version"}} {
		var stdout, stderr bytes.Buffer
		code := run(arguments, &stdout, &stderr)
		if code != exitOK {
			t.Fatalf("run(%q) = %d, want %d; stderr = %q", arguments, code, exitOK, stderr.String())
		}
		if stdout.String() != "agentctl v0.1.0-test\n" {
			t.Fatalf("run(%q) stdout = %q, want exact version line", arguments, stdout.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("run(%q) stderr = %q, want empty", arguments, stderr.String())
		}
	}
}

func TestRunVersionDoesNotTouchTmux(t *testing.T) {
	restoreBuildStamp(t, "v0.1.0-test")

	for _, arguments := range [][]string{{"version"}, {"--version"}} {
		runner := tmuxx.NewFakeRunner()
		var stdout, stderr bytes.Buffer
		code := runWithRunner(context.Background(), arguments, &stdout, &stderr, runner, lookupValues(nil))
		if code != exitOK {
			t.Fatalf("runWithRunner(%q) = %d, want %d; stderr = %q", arguments, code, exitOK, stderr.String())
		}
		if len(runner.Calls) != 0 {
			t.Fatalf("runWithRunner(%q) calls = %#v, want no tmux calls", arguments, runner.Calls)
		}
	}
}

func TestRunRejectsVersionArguments(t *testing.T) {
	const globalVersionUsage = `Usage: agentctl COMMAND [OPTIONS]

Commands:
  launch    create an agent fleet
  relaunch  recreate an absent role or an eligible no-baseline window
  attach    attach an agent fleet in iTerm2
  status    report fleet status
  clear     deliver /clear to a role
  compact   deliver /compact to a role
  kill      terminate a managed fleet
  skill     install or inspect the embedded agent skill
  version   report this binary's build identity
`
	tests := []struct {
		arguments []string
		want      string
	}{
		{arguments: []string{"version", "extra"}, want: "agentctl: version accepts no arguments\nUsage: agentctl version\n"},
		{arguments: []string{"--version", "extra"}, want: "agentctl: unknown command \"--version\"\n" + globalVersionUsage},
	}

	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		code := run(test.arguments, &stdout, &stderr)
		if code != exitUsage {
			t.Fatalf("run(%q) = %d, want %d", test.arguments, code, exitUsage)
		}
		if stdout.Len() != 0 {
			t.Fatalf("run(%q) stdout = %q, want empty", test.arguments, stdout.String())
		}
		if stderr.String() != test.want {
			t.Fatalf("run(%q) stderr = %q, want %q", test.arguments, stderr.String(), test.want)
		}
	}
}

func restoreBuildStamp(t *testing.T, stamp string) {
	t.Helper()
	previous := buildinfo.Stamp
	buildinfo.Stamp = stamp
	t.Cleanup(func() {
		buildinfo.Stamp = previous
	})
}

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
	const launchUsage = "Usage: agentctl launch --session SESSION --roles ROLE:HARNESS,... [--models ROLE:MODEL,...] [--efforts ROLE:LEVEL,...] [--dir PATH]\n" +
		"   or: agentctl launch --session SESSION --from-template FILE [--roles ROLE:HARNESS,...] [--models ROLE:MODEL,...] [--efforts ROLE:LEVEL,...] [--dir PATH]\n"
	const statusUsage = "Usage: agentctl status [--session SESSION] [--json]\n\n" +
		"Without --session, status reports every session; ambient session sources never narrow the listing.\n" +
		"A leading * marks the caller's session when agentctl can determine it from tmux.\n" +
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
		{name: "from-template spaced", args: []string{"launch", "--session", "fleet", "--from-template", "/one", "--from-template", "/two"}, option: "from-template", usage: launchUsage},
		{name: "from-template equals", args: []string{"launch", "--session", "fleet", "--from-template=/one", "--from-template=/two"}, option: "from-template", usage: launchUsage},
		{name: "models spaced", args: []string{"launch", "--session", "fleet", "--roles", "planner:claude", "--models", "planner:fable", "--models", "planner:opus"}, option: "models", usage: launchUsage},
		{name: "models equals", args: []string{"launch", "--session", "fleet", "--roles", "planner:claude", "--models=planner:fable", "--models=planner:opus"}, option: "models", usage: launchUsage},
		{name: "efforts spaced", args: []string{"launch", "--session", "fleet", "--roles", "planner:claude", "--efforts", "planner:high", "--efforts", "planner:low"}, option: "efforts", usage: launchUsage},
		{name: "efforts equals", args: []string{"launch", "--session", "fleet", "--roles", "planner:claude", "--efforts=planner:high", "--efforts=planner:low"}, option: "efforts", usage: launchUsage},
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

func TestRunRejectsExplicitlyEmptyModelsAndEfforts(t *testing.T) {
	tests := [][]string{
		{"launch", "--session", "fleet", "--roles", "planner:claude", "--models="},
		{"launch", "--session", "fleet", "--roles", "planner:claude", "--models", ""},
		{"launch", "--session", "fleet", "--roles", "planner:claude", "--efforts="},
		{"launch", "--session", "fleet", "--roles", "planner:claude", "--efforts", ""},
	}

	for _, arguments := range tests {
		var stdout, stderr bytes.Buffer
		if code := run(arguments, &stdout, &stderr); code != exitUsage {
			t.Fatalf("run(%q) = %d, want %d", arguments, code, exitUsage)
		}
		if !strings.Contains(stderr.String(), "must not be empty") || !strings.Contains(stderr.String(), "Usage:") {
			t.Fatalf("stderr = %q, want empty-list error and usage", stderr.String())
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout = %q, want empty", stdout.String())
		}
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
			args:     []string{"kill"},
			lookup:   lookupValues(map[string]string{"AM_ROOT": "/tmp/fleet", "AM_SESSION": "fleet", "TMUX": "server"}),
			wantCode: exitSession,
			wantText: "session could not be resolved",
		},
		{
			name:     "invalid environment is session error for acting command",
			args:     []string{"kill"},
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
			code := runWithDependencies(context.Background(), tt.args, &stdout, &stderr, dependencies{resolver: resolver})
			if code != tt.wantCode {
				t.Fatalf("runWithDependencies() = %d, want %d; stderr = %q", code, tt.wantCode, stderr.String())
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

	code := runWithDependencies(context.Background(), []string{"kill", "--session", "fleet"}, &stdout, &stderr, dependencies{resolver: resolver, killer: kill.New(tmuxx.New(runner))})

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
		name       string
		responses  []tmuxx.Response
		wantCalls  []tmuxx.Call
		wantStderr string
	}{
		{
			name:       "unmanaged",
			responses:  []tmuxx.Response{{Stdout: nil}},
			wantCalls:  []tmuxx.Call{{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}}},
			wantStderr: "agentctl: session \"fleet\" is not managed by agentctl\n",
		},
		{
			name:      "version marker absent",
			responses: []tmuxx.Response{{Stdout: []byte("1\n")}, {Stdout: nil}},
			wantCalls: []tmuxx.Call{
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
			},
			wantStderr: "agentctl: managed session carries no @agentctl_version marker\n",
		},
		{
			name:      "version marker observed wrong",
			responses: []tmuxx.Response{{Stdout: []byte("1\n")}, {Stdout: []byte("2\n")}},
			wantCalls: []tmuxx.Call{
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
			},
			wantStderr: "agentctl: session \"fleet\" has @agentctl_version=\"2\"; expected \"1\"\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner(tt.responses...)
			resolver := resolverFunc(func(context.Context, *string) (tmuxx.Session, error) {
				return tmuxx.Session{ID: "$4", Name: "fleet"}, nil
			})
			var stdout, stderr bytes.Buffer

			code := runWithDependencies(context.Background(), []string{"kill", "--session", "fleet"}, &stdout, &stderr, dependencies{resolver: resolver, killer: kill.New(tmuxx.New(runner))})

			if code != exitSession {
				t.Fatalf("runWithDependencies() = %d, want %d; stderr = %q", code, exitSession, stderr.String())
			}
			if stderr.String() != tt.wantStderr {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tt.wantStderr)
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

	code := runWithDependencies(context.Background(), []string{"kill", "--session", "fleet"}, &stdout, &stderr, dependencies{resolver: resolver, killer: killer})

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

			code := runWithDependencies(context.Background(), []string{"kill"}, &stdout, &stderr, dependencies{resolver: resolver, killer: kill.New(tmuxx.New(runner))})

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

func TestActingCommandMalformedTMUXPaneMapsToSessionErrorWithoutTmuxCall(t *testing.T) {
	for _, pane := range []string{"", "%", "7", "$1", "%abc", "not-a-pane"} {
		pane := pane
		t.Run(pane, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner()
			resolver := session.New(tmuxx.New(runner), lookupValues(map[string]string{"TMUX_PANE": pane}))
			var stdout, stderr bytes.Buffer

			code := runWithDependencies(context.Background(), []string{"kill"}, &stdout, &stderr, dependencies{resolver: resolver})

			if code != exitSession {
				t.Fatalf("runWithDependencies() = %d, want %d; stderr = %q", code, exitSession, stderr.String())
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

func TestStatusAllTreatsReportPresenceSeparatelyFromSchemaValue(t *testing.T) {
	collector := statusCollectorStub{report: statuspkg.SessionsReport{Sessions: []statuspkg.Report{}}}
	var stdout, stderr bytes.Buffer

	code := statusAll(context.Background(), &stdout, &stderr, collector, false)

	if code != exitOK {
		t.Fatalf("statusAll() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	if stdout.Len() == 0 {
		t.Fatal("stdout is empty, want rendered report")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunLaunchRequiresAndValidatesExplicitSessionWithoutResolving(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantCode    int
		wantResolve int
	}{
		{name: "missing", args: []string{"launch", "--roles", "planner:claude"}, wantCode: exitUsage},
		{name: "empty", args: []string{"launch", "--session=", "--roles", "planner:claude"}, wantCode: exitUsage},
		{name: "invalid", args: []string{"launch", "--session", "INVALID", "--roles", "planner:claude"}, wantCode: exitUsage},
		{name: "valid", args: []string{"launch", "--session", "fleet", "--roles", "planner:claude"}, wantCode: exitOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner(launchOneRoleResponses("")...)
			resolveCalls := 0
			resolver := resolverFunc(func(_ context.Context, explicit *string) (tmuxx.Session, error) {
				resolveCalls++
				if explicit == nil || *explicit != "fleet" {
					t.Fatalf("Resolve() explicit = %v, want fleet", explicit)
				}
				return tmuxx.Session{ID: "$17", Name: "fleet"}, nil
			})
			collector := selectedStatusCollectorStub{report: statuspkg.Report{
				Schema: 1, Session: "fleet", Managed: true, Agents: []statuspkg.Agent{},
			}}
			var stdout, stderr bytes.Buffer
			code := runWithDependencies(context.Background(), tt.args, &stdout, &stderr, dependencies{
				launch: launchTestDependencies(runner), resolver: resolver, collector: collector,
			})
			if code != tt.wantCode {
				t.Fatalf("runWithDependencies(%q) = %d, want %d; stderr = %q", tt.args, code, tt.wantCode, stderr.String())
			}
			if resolveCalls != tt.wantResolve {
				t.Fatalf("Resolve() calls = %d, want %d", resolveCalls, tt.wantResolve)
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

type statusCollectorStub struct {
	report statuspkg.SessionsReport
	err    error
}

func (s statusCollectorStub) Collect(context.Context, string, tmuxx.SessionID) (statuspkg.Report, error) {
	return statuspkg.Report{}, errors.New("unexpected single-session collection")
}

func (s statusCollectorStub) CollectAll(context.Context) (*statuspkg.SessionsReport, error) {
	return &s.report, s.err
}

type selectedStatusCollectorStub struct {
	report statuspkg.Report
	err    error
}

func (s selectedStatusCollectorStub) Collect(context.Context, string, tmuxx.SessionID) (statuspkg.Report, error) {
	return s.report, s.err
}

func (s selectedStatusCollectorStub) CollectAll(context.Context) (*statuspkg.SessionsReport, error) {
	return nil, errors.New("unexpected all-sessions collection")
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
		{name: "status rejects removed all flag", args: []string{"status", "--all"}},
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

func TestRunBareStatusIgnoresAmbientSessionSourcesAndMarksCurrent(t *testing.T) {
	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("$4\tfleet\n$5\tshell\n")},
		tmuxx.Response{Stdout: []byte("fleet\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("planner\n")},
		tmuxx.Response{Stdout: []byte("@7\tplanner\tplanner\tclaude\t\t\t\tclaude\n")},
		tmuxx.Response{Stdout: []byte("%12\t111\t0\t1\n")},
		tmuxx.Response{Stdout: []byte("claude\n")},
		tmuxx.Response{Stdout: []byte("0\n")},
		tmuxx.Response{},
	)
	var stdout, stderr bytes.Buffer

	code := runWithRunner(context.Background(), []string{"status"}, &stdout, &stderr, runner, lookupValues(map[string]string{
		"AGENTCTL_SESSION": "INVALID",
		"TMUX_PANE":        "%9",
	}))

	if code != exitOK {
		t.Fatalf("runWithRunner() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	want := "   SESSION  ROLE     HARNESS  MODEL    EFFORT   PANE  PROCESS  STATE\n" +
		"*  fleet    planner  claude   default  default  %12   claude   running\n" +
		"   shell                                                       unmanaged\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunStatusAllRendersMetadataDefectsAndContinuesBeforeExitThree(t *testing.T) {
	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("$4\tfleet\n$6\tfuture\n$7\tshell\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("planner\n")},
		tmuxx.Response{Stdout: []byte("@7\tplanner\tplanner\tclaude\t\t\t\tclaude\n")},
		tmuxx.Response{Stdout: []byte("%12\t111\t0\t1\n")},
		tmuxx.Response{Stdout: []byte("claude\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("2\n")},
		tmuxx.Response{Stdout: []byte("0\n")},
		tmuxx.Response{},
	)
	var stdout, stderr bytes.Buffer

	code := runWithRunner(context.Background(), []string{"status"}, &stdout, &stderr, runner, lookupValues(nil))

	if code != exitSession {
		t.Fatalf("runWithRunner() = %d, want %d; stderr = %q", code, exitSession, stderr.String())
	}
	for _, piece := range []string{"fleet", "running", "future", "different agentctl version", "shell", "unmanaged"} {
		if !strings.Contains(stdout.String(), piece) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), piece)
		}
	}
	if !strings.Contains(stderr.String(), "different agentctl version") {
		t.Fatalf("stderr = %q, want version defect", stderr.String())
	}
}

func TestRunStatusAllJSONKeepsEverySessionWhenMetadataIsDefective(t *testing.T) {
	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("$6\tfuture\n$7\tshell\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("2\n")},
		tmuxx.Response{Stdout: []byte("0\n")},
		tmuxx.Response{},
	)
	var stdout, stderr bytes.Buffer

	code := runWithRunner(context.Background(), []string{"status", "--json"}, &stdout, &stderr, runner, lookupValues(nil))

	if code != exitSession {
		t.Fatalf("runWithRunner() = %d, want %d; stderr = %q", code, exitSession, stderr.String())
	}
	want := "{\"schema\":1,\"sessions\":[{\"schema\":1,\"session\":\"future\",\"managed\":true,\"agents\":[]," +
		"\"defect\":\"session \\\"future\\\" was created by a different agentctl version \\\"2\\\"\"}," +
		"{\"schema\":1,\"session\":\"shell\",\"managed\":false,\"agents\":[]}]}\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if !strings.Contains(stderr.String(), "future") || !strings.Contains(stderr.String(), "different agentctl version") {
		t.Fatalf("stderr = %q, want named version defect", stderr.String())
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
	if strings.Contains(stdout.String(), "--all") {
		t.Fatalf("stdout = %q, want removed --all flag omitted", stdout.String())
	}
	if !strings.Contains(stdout.String(), "ambient session sources never narrow") {
		t.Fatalf("stdout = %q, want bare-listing explanation", stdout.String())
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
			want: "SESSION  ROLE     HARNESS  MODEL    EFFORT  PANE  PROCESS  STATE\n" +
				"fleet    planner  claude   default  xhigh   %12   claude   running\n",
		},
		{
			name: "json",
			args: []string{"status", "--session", "fleet", "--json"},
			want: "{\"schema\":1,\"session\":\"fleet\",\"managed\":true,\"agents\":[{\"role\":\"planner\",\"harness\":\"claude\",\"model\":\"\",\"effort\":\"xhigh\",\"window\":\"planner\",\"pane_id\":\"%12\",\"process\":\"claude\",\"state\":\"running\"}]}\n",
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
				{Executable: "tmux", Args: []string{"list-windows", "-t", "$4", "-F", "#{window_id}\t#{window_name}\t#{@agentctl_role}\t#{@agentctl_harness}\t#{@agentctl_model}\t#{@agentctl_effort}\t#{@agentctl_unproven}\t#{@agentctl_process}"}},
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

func TestRunStatusWithoutAnySessionSourceListsEverySession(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "human table",
			args: []string{"status"},
			want: "  SESSION  ROLE     HARNESS  MODEL    EFFORT   PANE  PROCESS  STATE\n" +
				"  fleet    planner  claude   default  default  %12   claude   running\n" +
				"  shell                                                       unmanaged\n",
		},
		{
			name: "json",
			args: []string{"status", "--json"},
			want: "{\"schema\":1,\"sessions\":[{\"schema\":1,\"session\":\"fleet\",\"managed\":true," +
				"\"agents\":[{\"role\":\"planner\",\"harness\":\"claude\",\"model\":\"\",\"effort\":\"\",\"window\":\"planner\"," +
				"\"pane_id\":\"%12\",\"process\":\"claude\",\"state\":\"running\"}]},{\"schema\":1," +
				"\"session\":\"shell\",\"managed\":false,\"agents\":[]}]}\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner(
				tmuxx.Response{Stdout: []byte("$4\tfleet\n$5\tshell\n")},
				tmuxx.Response{Stdout: []byte("1\n")},
				tmuxx.Response{Stdout: []byte("1\n")},
				tmuxx.Response{Stdout: []byte("planner\n")},
				tmuxx.Response{Stdout: []byte("@7\tplanner\tplanner\tclaude\t\t\t\tclaude\n")},
				tmuxx.Response{Stdout: []byte("%12\t111\t0\t1\n")},
				tmuxx.Response{Stdout: []byte("claude\n")},
				tmuxx.Response{Stdout: []byte("0\n")},
				tmuxx.Response{},
			)
			var stdout, stderr bytes.Buffer

			code := runWithRunner(context.Background(), tt.args, &stdout, &stderr, runner, lookupValues(map[string]string{
				"AM_ROOT":    "/tmp/fleet",
				"AM_SESSION": "fleet",
				"TMUX":       "server",
			}))

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
				{Executable: "tmux", Args: []string{"list-windows", "-t", "$4", "-F", "#{window_id}\t#{window_name}\t#{@agentctl_role}\t#{@agentctl_harness}\t#{@agentctl_model}\t#{@agentctl_effort}\t#{@agentctl_unproven}\t#{@agentctl_process}"}},
				{Executable: "tmux", Args: []string{"list-panes", "-t", "@7", "-F", "#{pane_id}\t#{pane_pid}\t#{pane_dead}\t#{window_panes}"}},
				{Executable: "ps", Args: []string{"-o", "comm=", "-p", "111"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$5", "@agentctl_managed"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$5", "@agentctl_version"}},
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

func TestRunBareStatusNoServerCarriesTmuxError(t *testing.T) {
	runner := tmuxx.NewFakeRunner(tmuxx.Response{Err: errors.New("no server running on /tmp/tmux-501/default")})
	var stdout, stderr bytes.Buffer

	code := runWithRunner(context.Background(), []string{"status"}, &stdout, &stderr, runner, lookupValues(nil))

	if code != exitTmux {
		t.Fatalf("runWithRunner() = %d, want %d", code, exitTmux)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "no server running on /tmp/tmux-501/default") {
		t.Fatalf("stderr = %q, want tmux message", stderr.String())
	}
}

func TestRunStatusNarrowsOnlyForExplicitSession(t *testing.T) {
	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("$4\tfleet\n$5\tother\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("planner\n")},
		tmuxx.Response{Stdout: []byte("@7\tplanner\tplanner\tclaude\t\t\t\tclaude\n")},
		tmuxx.Response{Stdout: []byte("%12\t111\t0\t1\n")},
		tmuxx.Response{Stdout: []byte("claude\n")},
	)
	var stdout, stderr bytes.Buffer

	code := runWithRunner(context.Background(), []string{"status", "--session", "fleet"}, &stdout, &stderr, runner, lookupValues(map[string]string{
		"AGENTCTL_SESSION": "other",
		"TMUX_PANE":        "%9",
	}))

	if code != exitOK {
		t.Fatalf("runWithRunner() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	want := "SESSION  ROLE     HARNESS  MODEL    EFFORT   PANE  PROCESS  STATE\n" +
		"fleet    planner  claude   default  default  %12   claude   running\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	for _, call := range runner.Calls {
		if call.Executable == "tmux" && len(call.Args) > 3 && call.Args[3] == "$5" {
			t.Fatalf("Calls = %#v, want no reads of the unnamed session", runner.Calls)
		}
	}
}

func healthyStatusRunner() *tmuxx.FakeRunner {
	return tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("$4\tfleet\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("planner\n")},
		tmuxx.Response{Stdout: []byte("@7\tplanner\tplanner\tclaude\t\txhigh\t\tclaude\n")},
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
