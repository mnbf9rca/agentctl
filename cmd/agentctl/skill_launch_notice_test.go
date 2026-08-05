package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/skillinstall"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestRunLaunchPrintsSkillSkewNoticesInTargetOrder(t *testing.T) {
	home := t.TempDir()
	targets := skillinstall.Targets(home)
	writeLaunchManifest(t, targets[0].Dir, skillinstall.Manifest{
		Version: "0.1.0",
		Files:   map[string]string{"missing-and-never-hashed.md": "not-a-hash"},
	})
	writeLaunchManifest(t, targets[1].Dir, skillinstall.Manifest{Version: "0.2.0", Files: map[string]string{}})
	deps := launchTestDependencies(tmuxx.NewFakeRunner(append(launchOneRoleResponses(""), healthyPostLaunchResponses()...)...))
	deps.skillHome = func() (string, error) { return home, nil }
	deps.skillVersion = func() string { return "0.3.0" }
	var stdout, stderr bytes.Buffer

	code := runWith([]string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &stdout, &stderr, deps)

	if code != exitOK {
		t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	want := "skill: ~/.claude/skills/agentctl is 0.1.0; this binary is 0.3.0 — run 'agentctl skill install'\n" +
		"skill: ~/.agents/skills/agentctl is 0.2.0; this binary is 0.3.0 — run 'agentctl skill install'\n"
	if got := stderr.String(); got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestRunLaunchSkillNoticeIsSilentWhenManifestsAbsentOrCurrent(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "absent"},
		{
			name: "current",
			setup: func(t *testing.T, home string) {
				t.Helper()
				writeLaunchManifest(t, skillinstall.Targets(home)[0].Dir, skillinstall.Manifest{Version: "0.3.0", Files: map[string]string{"unread.md": "ignored"}})
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			if tt.setup != nil {
				tt.setup(t, home)
			}
			deps := launchTestDependencies(tmuxx.NewFakeRunner(append(launchOneRoleResponses(""), healthyPostLaunchResponses()...)...))
			deps.skillHome = func() (string, error) { return home, nil }
			deps.skillVersion = func() string { return "0.3.0" }
			var stdout, stderr bytes.Buffer

			code := runWith([]string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &stdout, &stderr, deps)

			if code != exitOK || stderr.Len() != 0 {
				t.Fatalf("runWith() = %d, stderr = %q; want successful silent notice", code, stderr.String())
			}
		})
	}
}

func TestRunLaunchReportsUnparseableSkillManifestWithoutClaimingVersion(t *testing.T) {
	home := t.TempDir()
	target := skillinstall.Targets(home)[0]
	if err := os.MkdirAll(target.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target.Dir, skillinstall.ManifestName), []byte("not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deps := launchTestDependencies(tmuxx.NewFakeRunner(append(launchOneRoleResponses(""), healthyPostLaunchResponses()...)...))
	deps.skillHome = func() (string, error) { return home, nil }
	deps.skillVersion = func() string { return "0.3.0" }
	var stdout, stderr bytes.Buffer

	code := runWith([]string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &stdout, &stderr, deps)

	if code != exitOK {
		t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	got := stderr.String()
	if strings.Count(got, "\n") != 1 || !strings.Contains(got, "skill: ~/.claude/skills/agentctl manifest could not be read:") {
		t.Fatalf("stderr = %q, want exactly one factual manifest-read line", got)
	}
	if strings.Contains(got, " is ") {
		t.Fatalf("stderr = %q, must not claim a version for unparseable manifest", got)
	}
}

func TestRunLaunchSkillNoticeIsFinalAfterConfirmationFailure(t *testing.T) {
	home := t.TempDir()
	writeLaunchManifest(t, skillinstall.Targets(home)[0].Dir, skillinstall.Manifest{Version: "0.2.0", Files: map[string]string{}})
	responses := append(launchOneRoleResponses(""), tmuxx.Response{Err: errors.New("observation failed")})
	deps := launchTestDependencies(tmuxx.NewFakeRunner(responses...))
	deps.skillHome = func() (string, error) { return home, nil }
	deps.skillVersion = func() string { return "0.3.0" }
	var stdout, stderr bytes.Buffer

	code := runWith([]string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &stdout, &stderr, deps)

	if code != exitOK {
		t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	wantFinal := "skill: ~/.claude/skills/agentctl is 0.2.0; this binary is 0.3.0 — run 'agentctl skill install'\n"
	if !strings.HasPrefix(stderr.String(), "agentctl: session \"fleet\" launched, but post-launch status could not be confirmed:") || !strings.HasSuffix(stderr.String(), wantFinal) {
		t.Fatalf("stderr = %q, want confirmation defect followed by final skill notice", stderr.String())
	}
}

func TestRunFailedLaunchDoesNotReadSkillManifests(t *testing.T) {
	deps := launchTestDependencies(tmuxx.NewFakeRunner(tmuxx.Response{Stdout: []byte("$3\tfleet\n")}))
	homeCalled := false
	deps.skillHome = func() (string, error) {
		homeCalled = true
		return t.TempDir(), nil
	}
	deps.skillVersion = func() string { return "0.3.0" }
	var stdout, stderr bytes.Buffer

	code := runWith([]string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &stdout, &stderr, deps)

	if code != exitSession {
		t.Fatalf("runWith() = %d, want %d; stderr = %q", code, exitSession, stderr.String())
	}
	if homeCalled {
		t.Fatal("skill home lookup called after failed launch")
	}
	if strings.Contains(stderr.String(), "skill:") {
		t.Fatalf("stderr = %q, want no skill notice after failed launch", stderr.String())
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
