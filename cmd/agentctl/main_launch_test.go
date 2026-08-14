//go:build darwin

package main

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/fleet"
	"github.com/mnbf9rca/agentctl/internal/preflight"
	"github.com/mnbf9rca/agentctl/internal/shim"
)

func TestRunLaunchPassesValidatedFleetToShimAndReportsReadiness(t *testing.T) {
	t.Parallel()

	directory := "/work"
	launcher := &launcherStub{result: fleet.ShimLaunchResult{Directory: directory, TotalRoles: 2}}
	var stdout, stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{
		"launch", "--session", "fleet", "--roles", "planner:claude,coder:codex", "--models", "coder:gpt-5.6-sol", "--dir", directory,
	}, &stdout, &stderr, dependencies{launcher: launcher})
	wantFleet := config.FleetConfig{Roles: []config.RoleConfig{{Name: "planner", Harness: config.HarnessClaude}, {Name: "coder", Harness: config.HarnessCodex, Model: "gpt-5.6-sol"}}}
	if code != exitOK || launcher.session != "fleet" || !reflect.DeepEqual(launcher.fleet, wantFleet) || launcher.presentation != fleet.PresentationTmux || launcher.directory == nil || *launcher.directory != directory {
		t.Fatalf("code=%d session=%q fleet=%#v directory=%v stderr=%q", code, launcher.session, launcher.fleet, launcher.directory, stderr.String())
	}
	if stderr.String() != "agentctl: launched session \"fleet\"; 2 roles are ready\n" {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestRunLaunchMapsClosedPreownershipAndCommitOutcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		code int
		want string
	}{
		{name: "missing executable", err: &preflight.MissingExecutableError{Name: "codex"}, code: exitMissingExecutable, want: "agentctl: required executable \"codex\" was not found; no role was mutated\n"},
		{name: "fleet exists", err: &fleet.ShimFleetExistsError{Session: "fleet"}, code: exitSession, want: "agentctl: refusing to launch session \"fleet\"; durable fleet configuration already exists (fleet-config-exists)\n"},
		{name: "uncertain fleet config", err: &shim.RecordCommitUncertainError{Err: errors.New("sync failed")}, code: exitLaunchUnproven, want: "agentctl: role \"planner\" in session \"fleet\" has an uncertain durable fleet-config record commit: \"durable record replacement is visible but commit is uncertain: sync failed\"; the record was retained and the role was not reported absent (record-commit-uncertain)\n"},
		{name: "ambiguous ownership remains unclassified", err: &fleet.ShimLaunchRollbackError{Session: "fleet", Role: "planner", Cause: errors.New("presentation result indeterminate"), CleanupErr: errors.New("ownership absence unproved")}, code: exitUnclassified, want: "agentctl: launch failed for session \"fleet\": \"shim launch failed for role \\\"planner\\\" in session \\\"fleet\\\": presentation result indeterminate; owned rollback incomplete: ownership absence unproved\" (unclassified)\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := runWithDependencies(context.Background(), []string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &bytes.Buffer{}, &stderr, dependencies{launcher: &launcherStub{err: test.err}})
			if code != test.code || stderr.String() != test.want {
				t.Fatalf("code=%d stderr=%q, want %d %q", code, stderr.String(), test.code, test.want)
			}
		})
	}
}

// This catches detached typed outcomes falling through the generic launch
// renderer and losing their observed PID, cleanup, or uncertainty facts.
func TestRunLaunchRendersDetachedStartOutcomesExactly(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		err  error
		code int
		want string
	}{
		{name: "failed", err: &fleet.ShimDetachedStartFailedError{Session: "fleet", Role: "planner", Cause: errors.New("exec denied")}, code: exitLaunch, want: "agentctl: could not start a detached shim for role \"planner\" in session \"fleet\": exec denied; no child was started and cleanup removed every artifact owned by this invocation (detached-start-failed)\n"},
		{name: "rolled back", err: &fleet.ShimDetachedStartRolledBackError{Session: "fleet", Role: "planner", CreatedPID: 41, Cause: errors.New("exited")}, code: exitLaunch, want: "agentctl: detached shim PID 41 for role \"planner\" in session \"fleet\" failed before readiness: exited; cleanup observed child absence and removed every artifact owned by this invocation (detached-start-rolled-back)\n"},
		{name: "retained", err: &fleet.ShimDetachedStartRetainedError{Session: "fleet", Role: "planner", CreatedPID: 41, Cause: errors.New("exited"), CleanupErr: errors.New("lock remained")}, code: exitLaunchUnproven, want: "agentctl: detached shim PID 41 for role \"planner\" in session \"fleet\" failed before readiness: exited; cleanup left retained artifacts: lock remained (detached-start-retained)\n"},
		{name: "uncertain", err: &fleet.ShimDetachedStartUncertainError{Session: "fleet", Role: "planner", CreatedPID: 41, Cause: errors.New("deadline")}, code: exitLaunchUnproven, want: "agentctl: detached shim PID 41 for role \"planner\" in session \"fleet\" neither became ready nor was observed to exit; nothing was removed and the durable record was retained (detached-start-uncertain)\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := runWithDependencies(context.Background(), []string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &bytes.Buffer{}, &stderr, dependencies{launcher: &launcherStub{err: test.err}})
			if code != test.code || stderr.String() != test.want {
				t.Fatalf("code=%d stderr=%q, want %d %q", code, stderr.String(), test.code, test.want)
			}
		})
	}
}

func TestRunLaunchRejectsInvalidConfigurationBeforeShimLauncher(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{
		{"launch", "--roles", "planner:claude"},
		{"launch", "--session", "fleet"},
		{"launch", "--session", "Invalid", "--roles", "planner:claude"},
		{"launch", "--session", "fleet", "--roles", "planner:unknown"},
	} {
		launcher := &launcherStub{err: errors.New("must not call")}
		var stderr bytes.Buffer
		if code := runWithDependencies(context.Background(), arguments, &bytes.Buffer{}, &stderr, dependencies{launcher: launcher}); code != exitUsage || launcher.called {
			t.Fatalf("arguments=%q code=%d called=%t stderr=%q", arguments, code, launcher.called, stderr.String())
		}
	}
}

type launcherStub struct {
	result       fleet.ShimLaunchResult
	err          error
	called       bool
	session      string
	fleet        config.FleetConfig
	presentation fleet.Presentation
	directory    *string
}

func (l *launcherStub) Launch(_ context.Context, sessionName string, fleetConfig config.FleetConfig, presentation fleet.Presentation, directory *string) (fleet.ShimLaunchResult, error) {
	l.called = true
	l.session, l.fleet, l.presentation, l.directory = sessionName, fleetConfig, presentation, directory
	return l.result, l.err
}
