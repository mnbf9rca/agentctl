//go:build darwin

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/fleet"
	"github.com/mnbf9rca/agentctl/internal/shim"
)

func TestRunForegroundUsesValidatedSingleRoleAndObservedWorkingDirectory(t *testing.T) {
	t.Parallel()

	var gotSession, gotDirectory string
	var gotRole config.RoleConfig
	var stdout, stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{
		"run", "--session", "fleet", "--role", "planner", "--harness", "codex", "--model", "gpt-5.6-sol", "--effort", "high",
	}, &stdout, &stderr, dependencies{
		getwd: func() (string, error) { return "/work tree", nil },
		foreground: foregroundExecutorFunc(func(_ context.Context, session string, role config.RoleConfig, directory string) error {
			gotSession, gotRole, gotDirectory = session, role, directory
			return nil
		}),
	})
	if code != exitOK {
		t.Fatalf("runWithDependencies() = %d, want 0; stderr=%q", code, stderr.String())
	}
	if gotSession != "fleet" || gotDirectory != "/work tree" || gotRole != (config.RoleConfig{Name: "planner", Harness: config.HarnessCodex, Model: "gpt-5.6-sol", Effort: "high"}) {
		t.Fatalf("foreground inputs = %q %#v %q", gotSession, gotRole, gotDirectory)
	}
	if stdout.Len() != 0 || stderr.String() != "agentctl: foreground role \"planner\" in session \"fleet\" exited with status 0\n" {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunForegroundRequiresEveryIdentityFlagAndRejectsDir(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{
		{"run", "--role", "planner", "--harness", "codex"},
		{"run", "--session", "fleet", "--harness", "codex"},
		{"run", "--session", "fleet", "--role", "planner"},
		{"run", "--session", "fleet", "--role", "planner", "--harness", "codex", "--dir", "/tmp"},
	} {
		var stdout, stderr bytes.Buffer
		code := runWithDependencies(context.Background(), arguments, &stdout, &stderr, dependencies{})
		if code != exitUsage || !strings.Contains(stderr.String(), "Usage: agentctl run") {
			t.Fatalf("arguments=%q code=%d stderr=%q", arguments, code, stderr.String())
		}
	}
}

func TestRunForegroundMapsObservedChildOutcomesExactly(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{name: "exit", err: &shim.ForegroundChildExitError{Status: 17}, want: "agentctl: foreground role \"planner\" in session \"fleet\" exited with status 17 (child-exit)\n"},
		{name: "signal", err: &shim.ForegroundChildExitError{Status: -1, Signal: syscall.SIGHUP}, want: "agentctl: foreground role \"planner\" in session \"fleet\" terminated by signal SIGHUP (child-signal)\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := runWithDependencies(context.Background(), []string{"run", "--session", "fleet", "--role", "planner", "--harness", "codex"}, &bytes.Buffer{}, &stderr, dependencies{
				getwd: func() (string, error) { return "/work", nil }, foreground: foregroundExecutorFunc(func(context.Context, string, config.RoleConfig, string) error { return test.err }),
			})
			if code != exitUnclassified || stderr.String() != test.want {
				t.Fatalf("code=%d stderr=%q, want 1 %q", code, stderr.String(), test.want)
			}
		})
	}
}

func TestRunForegroundMapsLifecycleCleanupOutcomesExactly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		code int
		want string
	}{
		{
			name: "readiness timeout cleaned",
			err:  &shim.LifecycleRunError{Outcome: shim.OutcomeReadinessTimeout, ChildPID: 73, CleanupObservation: shim.ProcessAbsent, FinalICANON: true, FinalECHO: false},
			code: exitLaunch,
			want: "agentctl: role \"planner\" in session \"fleet\" was not ready after 5s; final tty flags were ICANON=true ECHO=false; cleanup observed child absence and removed every artifact owned by this invocation (readiness-timeout)\n",
		},
		{
			name: "readiness observation cleaned",
			err:  &shim.LifecycleRunError{Outcome: shim.OutcomeReadinessObservationFailed, ChildPID: 73, Cause: errors.New("TIOCGETA failed"), CleanupObservation: shim.ProcessAbsent},
			code: exitLaunch,
			want: "agentctl: could not observe harness tty readiness for role \"planner\" in session \"fleet\": \"TIOCGETA failed\"; cleanup observed child absence and removed every artifact owned by this invocation (readiness-observation-failed)\n",
		},
		{
			name: "child exited before ready",
			err:  &shim.LifecycleRunError{Outcome: shim.OutcomeChildExitedBeforeReady, ChildPID: 73, CleanupObservation: shim.ProcessAbsent},
			code: exitLaunch,
			want: "agentctl: child PID 73 exited before harness tty readiness for role \"planner\" in session \"fleet\"; cleanup observed absence and removed every artifact owned by this invocation (child-exited-before-ready)\n",
		},
		{
			name: "readiness timeout retained",
			err:  &shim.LifecycleRunError{Outcome: shim.OutcomeReadinessTimeout, ChildPID: 73, CleanupObservation: shim.ProcessPresentMatch, FinalICANON: true, FinalECHO: true},
			code: exitLaunchUnproven,
			want: "agentctl: role \"planner\" in session \"fleet\" was not ready after 5s; final tty flags were ICANON=true ECHO=true; child PID 73 was not observed absent, so ownership and the durable record were retained (readiness-timeout)\n",
		},
		{
			name: "readiness observation cleanup incomplete",
			err: &shim.LifecycleRunError{
				Outcome: shim.OutcomeReadinessObservationFailed, ChildPID: 73, Cause: errors.New("TIOCGETA failed"),
				CleanupObservation: shim.ProcessAbsent, CleanupErr: errors.New("remove record: permission denied"), Remaining: []string{"record", "lock"},
			},
			code: exitLaunch,
			want: "agentctl: run failed for role \"planner\" in session \"fleet\": \"TIOCGETA failed\"; cleanup left record, lock: \"remove record: permission denied\" (owned-rollback-incomplete)\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := runWithDependencies(context.Background(), []string{"run", "--session", "fleet", "--role", "planner", "--harness", "codex"}, &bytes.Buffer{}, &stderr, dependencies{
				getwd:      func() (string, error) { return "/work", nil },
				foreground: foregroundExecutorFunc(func(context.Context, string, config.RoleConfig, string) error { return test.err }),
			})
			if code != test.code || stderr.String() != test.want {
				t.Fatalf("code=%d stderr=%q, want %d %q", code, stderr.String(), test.code, test.want)
			}
		})
	}
}

func TestRunForegroundNeverClaimsCompleteCleanupWhenFleetRemovalFailed(t *testing.T) {
	t.Parallel()

	lifecycleErr := &shim.LifecycleRunError{
		Outcome: shim.OutcomeReadinessTimeout, ChildPID: 73, CleanupObservation: shim.ProcessAbsent,
	}
	err := &fleet.ShimForegroundRollbackError{
		Session: "fleet", Role: "planner", Cause: lifecycleErr, FleetCleanupErr: errors.New("fleet removal failed"),
	}
	var stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{"run", "--session", "fleet", "--role", "planner", "--harness", "codex"}, &bytes.Buffer{}, &stderr, dependencies{
		getwd:      func() (string, error) { return "/work", nil },
		foreground: foregroundExecutorFunc(func(context.Context, string, config.RoleConfig, string) error { return err }),
	})
	if code != exitUnclassified || !strings.Contains(stderr.String(), "durable fleet cleanup failed: fleet removal failed") || strings.Contains(stderr.String(), "removed every artifact") {
		t.Fatalf("code=%d stderr=%q, want unclassified fleet-cleanup failure without complete-cleanup claim", code, stderr.String())
	}
}

func TestRunForegroundDoesNotInventRetentionWhenCleanupErrorHasNoArtifactFacts(t *testing.T) {
	t.Parallel()

	err := &shim.LifecycleRunError{
		Outcome: shim.OutcomeReadinessObservationFailed, ChildPID: 73, Cause: errors.New("TIOCGETA failed"),
		CleanupObservation: shim.ProcessAbsent, CleanupErr: errors.New("cleanup observation failed"),
	}
	var stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{"run", "--session", "fleet", "--role", "planner", "--harness", "codex"}, &bytes.Buffer{}, &stderr, dependencies{
		getwd:      func() (string, error) { return "/work", nil },
		foreground: foregroundExecutorFunc(func(context.Context, string, config.RoleConfig, string) error { return err }),
	})
	if code != exitUnclassified || !strings.Contains(stderr.String(), "cleanup observation failed") || strings.Contains(stderr.String(), "not observed absent") || strings.Contains(stderr.String(), "were retained") {
		t.Fatalf("code=%d stderr=%q, want unclassified cleanup failure without invented absence or retention fact", code, stderr.String())
	}
}

func TestRunForegroundDoesNotHideTerminalRestoreFailureBehindChildOutcome(t *testing.T) {
	t.Parallel()

	err := &foregroundTerminalRestoreError{
		RunErr:     &shim.ForegroundChildExitError{Status: 17},
		RestoreErr: errors.New("restore outer terminal: input/output error"),
	}
	var stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{"run", "--session", "fleet", "--role", "planner", "--harness", "codex"}, &bytes.Buffer{}, &stderr, dependencies{
		getwd:      func() (string, error) { return "/work", nil },
		foreground: foregroundExecutorFunc(func(context.Context, string, config.RoleConfig, string) error { return err }),
	})
	if code != exitUnclassified || !strings.Contains(stderr.String(), "restore outer terminal: input/output error") || strings.Contains(stderr.String(), "(child-exit)") {
		t.Fatalf("code=%d stderr=%q, want unclassified terminal-restore failure without hiding it behind child outcome", code, stderr.String())
	}
}

func TestRunForegroundRestoreFailurePrecedesSpecificRunErrorRenderers(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		runErr error
	}{
		{name: "directory mismatch", runErr: &fleet.ShimForegroundDirectoryMismatchError{Session: "fleet", Role: "planner", Stored: "/stored", Current: "/current"}},
		{name: "rollback", runErr: &fleet.ShimForegroundRollbackError{Session: "fleet", Role: "planner", Cause: errors.New("readiness failed"), FleetCleanupErr: errors.New("fleet cleanup failed")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := &foregroundTerminalRestoreError{RunErr: test.runErr, RestoreErr: errors.New("restore outer terminal: input/output error")}
			var stderr bytes.Buffer
			code := runWithDependencies(context.Background(), []string{"run", "--session", "fleet", "--role", "planner", "--harness", "codex"}, &bytes.Buffer{}, &stderr, dependencies{
				getwd:      func() (string, error) { return "/work", nil },
				foreground: foregroundExecutorFunc(func(context.Context, string, config.RoleConfig, string) error { return err }),
			})
			if code != exitUnclassified || !strings.Contains(stderr.String(), "restore outer terminal: input/output error") || !strings.Contains(stderr.String(), "(unclassified)") {
				t.Fatalf("code=%d stderr=%q, want restore failure to take precedence", code, stderr.String())
			}
		})
	}
}

func TestRunForegroundDoesNotCallExecutorAfterValidationFailure(t *testing.T) {
	t.Parallel()

	called := false
	var stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{"run", "--session", "INVALID", "--role", "planner", "--harness", "codex"}, &bytes.Buffer{}, &stderr, dependencies{
		foreground: foregroundExecutorFunc(func(context.Context, string, config.RoleConfig, string) error {
			called = true
			return errors.New("must not run")
		}),
	})
	if code != exitUsage || called {
		t.Fatalf("code=%d called=%t stderr=%q", code, called, stderr.String())
	}
}

func TestRunForegroundRendersBothDirectoriesOnExistingFleetMismatch(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{"run", "--session", "fleet", "--role", "planner", "--harness", "codex"}, &bytes.Buffer{}, &stderr, dependencies{
		getwd: func() (string, error) { return "/current", nil },
		foreground: foregroundExecutorFunc(func(context.Context, string, config.RoleConfig, string) error {
			return &fleet.ShimForegroundDirectoryMismatchError{Session: "fleet", Role: "planner", Stored: "/stored", Current: "/current"}
		}),
	})
	want := "agentctl: refusing to run role \"planner\" in session \"fleet\"; durable fleet directory \"/stored\" differs from current working directory \"/current\"; no role was started or durable record mutated (fleet-directory-disagreement)\n"
	if code != exitUnsafe || stderr.String() != want {
		t.Fatalf("code=%d stderr=%q, want %d %q", code, stderr.String(), exitUnsafe, want)
	}
}

type foregroundExecutorFunc func(context.Context, string, config.RoleConfig, string) error

func (f foregroundExecutorFunc) Execute(ctx context.Context, session string, role config.RoleConfig, directory string) error {
	return f(ctx, session, role, directory)
}
