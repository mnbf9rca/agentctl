package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/fleet"
)

func TestParseLaunchSupportsTheTemplateFormAndRejectsDuplicateTemplateOptions(t *testing.T) {
	t.Parallel()

	options, err := parseLaunch([]string{"--session", "alpha", "--from-template", "/fleet.json"})
	if err != nil || options.fromTemplate == nil || *options.fromTemplate != "/fleet.json" || options.rolesSet {
		t.Fatalf("options=%#v err=%v", options, err)
	}
	_, err = parseLaunch([]string{"--session", "alpha", "--from-template", "/one.json", "--from-template", "/two.json"})
	if err == nil || !strings.Contains(err.Error(), `--from-template provided more than once`) {
		t.Fatalf("duplicate error=%v", err)
	}
}

func TestRunLaunchHelpShowsBothLaunchForms(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := runWithDependencies(context.Background(), []string{"launch", "--help"}, &stdout, &stderr, dependencies{}); code != exitOK {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, form := range []string{"--roles ROLE:HARNESS,...", "--from-template FILE"} {
		if !strings.Contains(stdout.String(), form) {
			t.Fatalf("stdout=%q, want %q", stdout.String(), form)
		}
	}
}

func TestRunLaunchTemplateFailuresPrecedeShimLauncher(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	invalidVersion := writeLaunchTemplateFixture(t, directory, "version.json", `{"version":2}`)
	emptyUnion := writeLaunchTemplateFixture(t, directory, "empty.json", `{"version":1}`)
	relativeDirectory := writeLaunchTemplateFixture(t, directory, "relative.json", `{"version":1,"dir":"relative"}`)
	for _, test := range []struct{ path, want string }{
		{filepath.Join(directory, "missing.json"), "cannot open"},
		{invalidVersion, "version 2 is not supported"},
		{emptyUnion, "effective fleet: must contain at least one role"},
		{relativeDirectory, `dir: path "relative" must be absolute`},
		{directory, "must be a regular file; opened object is directory"},
	} {
		launcher := &launcherStub{}
		var stderr bytes.Buffer
		code := runWithDependencies(context.Background(), []string{"launch", "--session", "alpha", "--from-template", test.path}, &bytes.Buffer{}, &stderr, dependencies{launcher: launcher})
		if code != exitUsage || launcher.called || !strings.Contains(stderr.String(), test.want) {
			t.Fatalf("path=%q code=%d called=%t stderr=%q", test.path, code, launcher.called, stderr.String())
		}
	}
}

func TestRunLaunchFromTemplatePassesEffectiveFleetAndReportsProvenance(t *testing.T) {
	t.Parallel()

	templatePath := writeLaunchTemplateFixture(t, t.TempDir(), "fleet.json", `{"version":1,"dir":"/srv/work","roles":[{"role":"planner","harness":"claude","model":"opus-4-1","effort":"high"}]}`)
	launcher := &launcherStub{result: fleet.ShimLaunchResult{Directory: "/override", TotalRoles: 2}}
	var stdout, stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{
		"launch", "--session", "alpha", "--from-template", templatePath,
		"--roles", "worker:codex", "--models", "worker:gpt-5", "--efforts", "planner:low", "--dir", "/override",
	}, &stdout, &stderr, dependencies{launcher: launcher})
	wantFleet := config.FleetConfig{Roles: []config.RoleConfig{
		{Name: "planner", Harness: config.HarnessClaude, Model: "opus-4-1", Effort: "low"},
		{Name: "worker", Harness: config.HarnessCodex, Model: "gpt-5"},
	}}
	if code != exitOK || !reflect.DeepEqual(launcher.fleet, wantFleet) {
		t.Fatalf("code=%d fleet=%#v stderr=%q", code, launcher.fleet, stderr.String())
	}
	for _, fact := range []string{"harness claude (template)", "effort low (flag override)", "harness codex (flags)", "dir /override (flag override)"} {
		if !strings.Contains(stdout.String(), fact) {
			t.Fatalf("stdout=%q, want %q", stdout.String(), fact)
		}
	}
}

func TestRunLaunchTemplatePresentationAndExplicitFlagPrecedence(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name         string
		presentation string
		flag         string
		want         fleet.Presentation
	}{
		{name: "absent defaults detached", want: fleet.PresentationDetached},
		{name: "template detached", presentation: `,"presentation":"detached"`, want: fleet.PresentationDetached},
		{name: "template tmux", presentation: `,"presentation":"tmux"`, want: fleet.PresentationTmux},
		{name: "flag overrides template", presentation: `,"presentation":"tmux"`, flag: "--detached", want: fleet.PresentationDetached},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := writeLaunchTemplateFixture(t, t.TempDir(), "fleet.json", `{"version":1`+test.presentation+`,"roles":[{"role":"planner","harness":"claude"}]}`)
			arguments := []string{"launch", "--session", "fleet", "--from-template", path}
			if test.flag != "" {
				arguments = append(arguments, test.flag)
			}
			launcher := &launcherStub{result: fleet.ShimLaunchResult{Directory: "/work", TotalRoles: 1}}
			var stderr bytes.Buffer
			code := runWithDependencies(context.Background(), arguments, &bytes.Buffer{}, &stderr, dependencies{launcher: launcher})
			if code != exitOK || launcher.presentation != test.want {
				t.Fatalf("code=%d presentation=%q stderr=%q, want %q", code, launcher.presentation, stderr.String(), test.want)
			}
		})
	}
}

func TestRunLaunchTemplateRejectsInvalidPresentationBeforeMutation(t *testing.T) {
	t.Parallel()

	for _, value := range []string{`"screen"`, `null`, `1`} {
		path := writeLaunchTemplateFixture(t, t.TempDir(), "fleet.json", `{"version":1,"presentation":`+value+`,"roles":[{"role":"planner","harness":"claude"}]}`)
		launcher := &launcherStub{}
		var stderr bytes.Buffer
		code := runWithDependencies(context.Background(), []string{"launch", "--session", "fleet", "--from-template", path}, &bytes.Buffer{}, &stderr, dependencies{launcher: launcher})
		if code != exitUsage || launcher.called || !strings.Contains(stderr.String(), "presentation") {
			t.Fatalf("value=%s code=%d called=%t stderr=%q", value, code, launcher.called, stderr.String())
		}
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
