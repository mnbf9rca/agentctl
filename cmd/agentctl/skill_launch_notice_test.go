package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/fleet"
	"github.com/mnbf9rca/agentctl/internal/skillinstall"
)

func TestRunLaunchPrintsSkillSkewNoticesAfterShimSuccess(t *testing.T) {
	home := t.TempDir()
	targets := skillinstall.Targets(home)
	writeLaunchManifest(t, targets[0].Dir, skillinstall.Manifest{Version: "0.1.0", Files: map[string]string{}})
	writeLaunchManifest(t, targets[1].Dir, skillinstall.Manifest{Version: "0.2.0", Files: map[string]string{}})
	var stdout, stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &stdout, &stderr, dependencies{
		launcher: &launcherStub{result: fleet.ShimLaunchResult{Directory: "/work", TotalRoles: 1}},
		launch:   launchDependencies{skillHome: func() (string, error) { return home, nil }, skillVersion: func() string { return "0.3.0" }},
	})
	want := "agentctl: launched session \"fleet\"; 1 roles are ready\n" +
		"skill: ~/.claude/skills/agentctl is 0.1.0; this binary is 0.3.0 — run 'agentctl skill install'\n" +
		"skill: ~/.agents/skills/agentctl is 0.2.0; this binary is 0.3.0 — run 'agentctl skill install'\n"
	if code != exitOK || stderr.String() != want {
		t.Fatalf("code=%d stderr=%q, want %q", code, stderr.String(), want)
	}
}

func TestRunFailedLaunchDoesNotReadSkillManifests(t *testing.T) {
	homeCalled := false
	var stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &bytes.Buffer{}, &stderr, dependencies{
		launcher: &launcherStub{err: &fleet.ShimFleetExistsError{Session: "fleet"}},
		launch:   launchDependencies{skillHome: func() (string, error) { homeCalled = true; return t.TempDir(), nil }, skillVersion: func() string { return "0.3.0" }},
	})
	if code != exitSession || homeCalled || strings.Contains(stderr.String(), "skill:") {
		t.Fatalf("code=%d homeCalled=%t stderr=%q", code, homeCalled, stderr.String())
	}
}

func TestRunLaunchReportsUnreadableSkillManifestWithoutInventingVersion(t *testing.T) {
	home := t.TempDir()
	target := skillinstall.Targets(home)[0]
	if err := os.MkdirAll(target.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target.Dir, skillinstall.ManifestName), []byte("not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &bytes.Buffer{}, &stderr, dependencies{
		launcher: &launcherStub{result: fleet.ShimLaunchResult{Directory: "/work", TotalRoles: 1}},
		launch:   launchDependencies{skillHome: func() (string, error) { return home, nil }, skillVersion: func() string { return "0.3.0" }},
	})
	if code != exitOK || !strings.Contains(stderr.String(), "manifest could not be read:") || strings.Contains(stderr.String(), " is ") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func writeLaunchManifest(t *testing.T, dir string, manifest skillinstall.Manifest) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := skillinstall.WriteManifest(dir, manifest); err != nil {
		t.Fatal(err)
	}
}
