package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/skillinstall"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestRunSkillRequiresKnownSubcommand(t *testing.T) {
	for _, args := range [][]string{
		{"skill"},
		{"skill", "unknown"},
		{"skill", "status", "extra"},
		{"skill", "install", "extra"},
		{"skill", "install", "--unknown"},
	} {
		var stdout, stderr bytes.Buffer
		code := run(args, &stdout, &stderr)
		if code != exitUsage {
			t.Errorf("run(%q) = %d, want %d; stderr = %q", args, code, exitUsage, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Errorf("run(%q) stdout = %q, want empty", args, stdout.String())
		}
		if !strings.Contains(stderr.String(), "Usage: agentctl skill install [--force] | agentctl skill status") {
			t.Errorf("run(%q) stderr = %q, want skill usage", args, stderr.String())
		}
	}
}

func TestRunSkillInstallReportsInstalledThenCurrentPerTarget(t *testing.T) {
	restoreBuildStamp(t, "0.3.0")
	home := t.TempDir()
	t.Setenv("HOME", home)
	targets := skillinstall.Targets(home)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"skill", "install"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("first install exit = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	wantInstalled := targets[0].Dir + ": installed\n" + targets[1].Dir + ": installed\n"
	if got := stdout.String(); got != wantInstalled {
		t.Fatalf("first install stdout = %q, want %q", got, wantInstalled)
	}
	if stderr.Len() != 0 {
		t.Fatalf("first install stderr = %q, want empty", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"skill", "install"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("second install exit = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	wantCurrent := targets[0].Dir + ": current\n" + targets[1].Dir + ": current\n"
	if got := stdout.String(); got != wantCurrent {
		t.Fatalf("second install stdout = %q, want %q", got, wantCurrent)
	}
	if stderr.Len() != 0 {
		t.Fatalf("second install stderr = %q, want empty", stderr.String())
	}
}

func TestRunSkillInstallMapsUnownedRefusalAndForce(t *testing.T) {
	restoreBuildStamp(t, "0.3.0")
	home := t.TempDir()
	t.Setenv("HOME", home)
	targets := skillinstall.Targets(home)
	offending := filepath.Join(targets[0].Dir, "mine.txt")
	if err := os.MkdirAll(targets[0].Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(offending, []byte("operator-owned\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"skill", "install"}, &stdout, &stderr); code != exitUnsafe {
		t.Fatalf("install exit = %d, want %d; stdout = %q, stderr = %q", code, exitUnsafe, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), offending) || !strings.Contains(stderr.String(), "refused") {
		t.Fatalf("install stderr = %q, want refusal naming %q", stderr.String(), offending)
	}
	if content, err := os.ReadFile(offending); err != nil || string(content) != "operator-owned\n" {
		t.Fatalf("offending file after refusal = %q, %v; want untouched", content, err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"skill", "install", "--force"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("forced install exit = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	if !strings.Contains(stdout.String(), targets[0].Dir+": installed") || !strings.Contains(stdout.String(), targets[1].Dir+": current") {
		t.Fatalf("forced install stdout = %q, want installed first target and current second target", stdout.String())
	}
	if _, err := os.Stat(offending); !os.IsNotExist(err) {
		t.Fatalf("offending file survives forced replacement: %v", err)
	}
}

func TestRunSkillStatusReportsEveryStateAndAlwaysSucceeds(t *testing.T) {
	restoreBuildStamp(t, "0.3.0")
	home := t.TempDir()
	t.Setenv("HOME", home)
	targets := skillinstall.Targets(home)
	if err := os.MkdirAll(targets[1].Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"skill", "status"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("status exit = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	want := targets[0].Dir + ": absent (installed none, binary 0.3.0)\n" +
		targets[1].Dir + ": unmanaged (installed none, binary 0.3.0)\n"
	if got := stdout.String(); got != want {
		t.Fatalf("status stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("status stderr = %q, want empty", stderr.String())
	}
}

func TestRunSkillHomeResolutionFailureIsUnclassifiedRefusal(t *testing.T) {
	restoreBuildStamp(t, "0.3.0")
	t.Setenv("HOME", "")
	var stdout, stderr bytes.Buffer

	if code := run([]string{"skill", "status"}, &stdout, &stderr); code != exitUnclassified {
		t.Fatalf("status exit = %d, want %d; stderr = %q", code, exitUnclassified, stderr.String())
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "home directory") {
		t.Fatalf("stdout = %q, stderr = %q; want home-resolution refusal", stdout.String(), stderr.String())
	}
}

func TestRunSkillNeverCallsTmux(t *testing.T) {
	restoreBuildStamp(t, "0.3.0")
	t.Setenv("HOME", t.TempDir())
	runner := tmuxx.NewFakeRunner()
	var stdout, stderr bytes.Buffer

	code := runWithRunner(context.Background(), []string{"skill", "status"}, &stdout, &stderr, runner, lookupValues(nil))
	if code != exitOK {
		t.Fatalf("runWithRunner() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	if len(runner.Calls) != 0 {
		t.Fatalf("runner calls = %#v, want none", runner.Calls)
	}
}
