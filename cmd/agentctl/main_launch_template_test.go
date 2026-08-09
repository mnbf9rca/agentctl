package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestParseLaunchSupportsTheTemplateFormAndRejectsDuplicateTemplateOptions(t *testing.T) {
	t.Parallel()

	options, err := parseLaunch([]string{"--session", "alpha", "--from-template", "/fleet.json"})
	if err != nil {
		t.Fatalf("parseLaunch() error = %v", err)
	}
	if options.fromTemplate == nil || *options.fromTemplate != "/fleet.json" {
		t.Fatalf("fromTemplate = %#v, want /fleet.json", options.fromTemplate)
	}
	if options.rolesSet {
		t.Fatal("rolesSet = true, want false when template is the only fleet source")
	}

	_, err = parseLaunch([]string{"--session", "alpha", "--from-template", "/one.json", "--from-template", "/two.json"})
	if err == nil || !strings.Contains(err.Error(), `--from-template provided more than once`) {
		t.Fatalf("duplicate error = %v, want duplicate option refusal", err)
	}
}

func TestRunLaunchTemplateFailuresExitTwoBeforePreflightOrRunner(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	invalidVersion := writeLaunchTemplateFixture(t, directory, "version.json", `{"version":2}`)
	emptyUnion := writeLaunchTemplateFixture(t, directory, "empty.json", `{"version":1}`)
	relativeDirectory := writeLaunchTemplateFixture(t, directory, "relative.json", `{"version":1,"dir":"relative"}`)
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "missing", path: filepath.Join(directory, "missing.json"), want: "cannot open"},
		{name: "unsupported version", path: invalidVersion, want: "version 2 is not supported"},
		{name: "empty union", path: emptyUnion, want: "effective fleet: must contain at least one role"},
		{name: "relative directory", path: relativeDirectory, want: `dir: path "relative" must be absolute`},
		{name: "non regular", path: directory, want: "must be a regular file; opened object is directory"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := tmuxx.NewFakeRunner()
			deps := launchTestDependencies(runner)
			lookPathCalled := false
			deps.fleet.LookPath = func(string) (string, error) {
				lookPathCalled = true
				return "", errors.New("preflight must not run")
			}
			var stdout, stderr bytes.Buffer

			code := runWith([]string{"launch", "--session", "alpha", "--from-template", test.path}, &stdout, &stderr, deps)

			if code != exitUsage {
				t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitUsage, stderr.String())
			}
			if !strings.Contains(stderr.String(), "agentctl: template "+test.path+": "+test.want) || !strings.Contains(stderr.String(), commandUsage["launch"]) {
				t.Fatalf("stderr = %q, want template error %q and usage", stderr.String(), test.want)
			}
			if lookPathCalled {
				t.Fatal("LookPath was called before template rejection")
			}
			if len(runner.Calls) != 0 {
				t.Fatalf("runner calls = %#v, want none", runner.Calls)
			}
		})
	}
}

func TestRunLaunchDecodesTemplateBeforeValidatingTheSession(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "missing.json")
	var stdout, stderr bytes.Buffer
	runner := tmuxx.NewFakeRunner()

	code := runWith([]string{"launch", "--session", "Invalid", "--from-template", missing}, &stdout, &stderr, launchTestDependencies(runner))

	if code != exitUsage {
		t.Fatalf("runWith() = %d, want %d", code, exitUsage)
	}
	if got := stderr.String(); !strings.Contains(got, "template "+missing+": cannot open") || strings.Contains(got, `invalid session "Invalid"`) {
		t.Fatalf("stderr = %q, want template open failure before session validation", got)
	}
	if len(runner.Calls) != 0 {
		t.Fatalf("runner calls = %#v, want none", runner.Calls)
	}
}

func TestRunLaunchFromTemplateReportsProvenanceBeforeObservedStatusAndUsesEffectiveFleet(t *testing.T) {
	t.Parallel()

	templatePath := writeLaunchTemplateFixture(t, t.TempDir(), "fleet.json", `{
  "version": 1,
  "dir": "/srv/work",
  "roles": [
    {"role":"planner","harness":"claude","model":"opus-4-1","effort":"high"}
  ]
}`)
	responses := append(launchTwoRoleResponses(),
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("planner,worker\n")},
		tmuxx.Response{Stdout: []byte("@23\tplanner\tplanner\tclaude\topus-4-1\tlow\t\tclaude\n@65\tworker\tworker\tcodex\tgpt-5\t\t\tcodex\n")},
		tmuxx.Response{Stdout: []byte("%42\t4242\t0\t1\n")},
		tmuxx.Response{Stdout: []byte("claude\n")},
		tmuxx.Response{Stdout: []byte("%87\t8686\t0\t1\n")},
		tmuxx.Response{Stdout: []byte("codex\n")},
	)
	runner := tmuxx.NewFakeRunner(responses...)
	models := "worker:gpt-5"
	efforts := "planner:low"
	var stdout, stderr bytes.Buffer

	code := runWith([]string{
		"launch", "--session", "alpha", "--from-template", templatePath,
		"--roles", "worker:codex", "--models", models, "--efforts", efforts, "--dir", "/override",
	}, &stdout, &stderr, launchTestDependencies(runner))

	if code != exitOK {
		t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	want := "agentctl: launched planner in alpha: harness claude (template), model opus-4-1 (template), effort low (flag override)\n" +
		"agentctl: launched worker in alpha: harness codex (flags), model gpt-5 (flags), effort default (flags)\n" +
		"agentctl: template " + templatePath + ": dir /override (flag override)\n" +
		"SESSION  ROLE     HARNESS  MODEL     EFFORT   PANE  PROCESS  STATE\n" +
		"alpha    planner  claude   opus-4-1  low      %42   claude   running\n" +
		"alpha    worker   codex    gpt-5     default  %87   codex    running\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	wantCalls := templateLaunchCalls()
	if !reflect.DeepEqual(runner.Calls, wantCalls) {
		t.Fatalf("runner calls = %#v, want %#v", runner.Calls, wantCalls)
	}
}

func TestRunLaunchFromTemplateOmitsDirectoryProvenanceWhenTemplateHasNoDirectory(t *testing.T) {
	t.Parallel()

	templatePath := writeLaunchTemplateFixture(t, t.TempDir(), "fleet.json", `{"version":1,"roles":[{"role":"planner","harness":"claude"}]}`)
	responses := append(launchOneRoleResponses(""), healthyPostLaunchResponses()...)
	runner := tmuxx.NewFakeRunner(responses...)
	var stdout, stderr bytes.Buffer

	code := runWith([]string{"launch", "--session", "fleet", "--from-template", templatePath, "--dir", "/flags"}, &stdout, &stderr, launchTestDependencies(runner))

	if code != exitOK {
		t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	if strings.Contains(stdout.String(), "template "+templatePath+": dir") {
		t.Fatalf("stdout = %q, want no template dir provenance", stdout.String())
	}
	if !strings.HasPrefix(stdout.String(), "agentctl: launched planner in fleet: harness claude (template), model default (template), effort default (template)\n") {
		t.Fatalf("stdout = %q, want template role provenance", stdout.String())
	}
}

func writeLaunchTemplateFixture(t *testing.T, directory, name, contents string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write template: %v", err)
	}
	return path
}

func templateLaunchCalls() []tmuxx.Call {
	return []tmuxx.Call{
		{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}},
		{Executable: "tmux", Args: []string{"new-session", "-d", "-s", "alpha", "-n", "planner", "-c", "/override", "-e", "AGENTCTL_SESSION=alpha", "-e", "AGENTCTL_ROLE=planner", "-e", "AGENTCTL_MANAGED=1", "-P", "-F", "#{session_id}\t#{window_id}\t#{pane_id}\t#{pane_pid}", "--", "exec 'amq' 'coop' 'exec' '--session' 'alpha' '--me' 'planner' 'claude' '--' '--model' 'opus-4-1' '--effort' 'low'"}},
		{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_managed", "1"}},
		{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_version", "1"}},
		{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_roles", "planner,worker"}},
		{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_fleet", "planner:claude:opus-4-1:low,worker:codex:gpt-5:"}},
		{Executable: "tmux", Args: []string{"set-option", "-t", "$17", "@agentctl_dir", "/override"}},
		{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_managed", "1"}},
		{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_role", "planner"}},
		{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_harness", "claude"}},
		{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_model", "opus-4-1"}},
		{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_effort", "low"}},
		{Executable: "ps", Args: []string{"-o", "comm=", "-p", "4242"}},
		{Executable: "ps", Args: []string{"-o", "comm=", "-p", "4242"}},
		{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@23", "@agentctl_process", "claude"}},
		{Executable: "tmux", Args: []string{"set-environment", "-t", "$17", "-u", "AGENTCTL_SESSION"}},
		{Executable: "tmux", Args: []string{"set-environment", "-t", "$17", "-u", "AGENTCTL_ROLE"}},
		{Executable: "tmux", Args: []string{"set-environment", "-t", "$17", "-u", "AGENTCTL_MANAGED"}},
		{Executable: "tmux", Args: []string{"new-window", "-d", "-t", "$17", "-n", "worker", "-c", "/override", "-e", "AGENTCTL_SESSION=alpha", "-e", "AGENTCTL_ROLE=worker", "-e", "AGENTCTL_MANAGED=1", "-P", "-F", "#{window_id}\t#{pane_id}\t#{pane_pid}", "--", "exec 'amq' 'coop' 'exec' '--session' 'alpha' '--me' 'worker' 'codex' '--' '--model' 'gpt-5'"}},
		{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_managed", "1"}},
		{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_role", "worker"}},
		{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_harness", "codex"}},
		{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_model", "gpt-5"}},
		{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_effort", ""}},
		{Executable: "ps", Args: []string{"-o", "comm=", "-p", "8686"}},
		{Executable: "ps", Args: []string{"-o", "comm=", "-p", "8686"}},
		{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@65", "@agentctl_process", "codex"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$17", "@agentctl_managed"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$17", "@agentctl_version"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$17", "@agentctl_roles"}},
		{Executable: "tmux", Args: []string{"list-windows", "-t", "$17", "-F", "#{window_id}\t#{window_name}\t#{@agentctl_role}\t#{@agentctl_harness}\t#{@agentctl_model}\t#{@agentctl_effort}\t#{@agentctl_unproven}\t#{@agentctl_process}"}},
		{Executable: "tmux", Args: []string{"list-panes", "-t", "@23", "-F", "#{pane_id}\t#{pane_pid}\t#{pane_dead}\t#{window_panes}"}},
		{Executable: "ps", Args: []string{"-o", "comm=", "-p", "4242"}},
		{Executable: "tmux", Args: []string{"list-panes", "-t", "@65", "-F", "#{pane_id}\t#{pane_pid}\t#{pane_dead}\t#{window_panes}"}},
		{Executable: "ps", Args: []string{"-o", "comm=", "-p", "8686"}},
	}
}
