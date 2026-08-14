//go:build darwin

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/fleet"
	"github.com/mnbf9rca/agentctl/internal/preflight"
	"github.com/mnbf9rca/agentctl/internal/shim"
)

func TestRunRelaunchPassesOnlySuppliedOverridesAndReportsRuntimeReadiness(t *testing.T) {
	t.Parallel()

	harness, directory := "codex", "/work"
	relauncher := &relauncherStub{result: fleet.ShimRelaunchResult{Session: "fleet", Role: "planner"}}
	var stdout, stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{"relaunch", "--session", "fleet", "--harness", harness, "--dir", directory, "planner"}, &stdout, &stderr, dependencies{
		resolver: &resolverStub{selected: "fleet"}, relauncher: relauncher,
	})
	if code != exitOK || relauncher.session != "fleet" || relauncher.request.Role != "planner" || relauncher.request.Harness == nil || *relauncher.request.Harness != harness || relauncher.request.Directory == nil || *relauncher.request.Directory != directory || relauncher.request.Model != nil || relauncher.request.Effort != nil {
		t.Fatalf("code=%d session=%q request=%#v stderr=%q", code, relauncher.session, relauncher.request, stderr.String())
	}
	want := "agentctl: relaunched role \"planner\" in session \"fleet\"; the shim is ready\n"
	if stdout.Len() != 0 || stderr.String() != want {
		t.Fatalf("stdout=%q stderr=%q, want %q", stdout.String(), stderr.String(), want)
	}
}

func TestRunRelaunchPreservesManualOnlyIndeterminateFacts(t *testing.T) {
	t.Parallel()

	path := "/recorded/sessions/fleet/roles/planner.json"
	observation := fleet.ShimRoleObservation{Outcome: shim.OutcomeIndeterminateChildStarting, ShimPID: 41, RecordPath: path}
	err := &fleet.ShimRelaunchRefusalError{Session: "fleet", Role: "planner", Outcome: observation.Outcome, Observation: observation}
	var stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{"relaunch", "--session", "fleet", "planner"}, &bytes.Buffer{}, &stderr, dependencies{
		resolver: &resolverStub{selected: "fleet"}, relauncher: &relauncherStub{err: err},
	})
	want := "agentctl: refusing to relaunch role \"planner\" in session \"fleet\"; shim PID 41 was absent and the durable record is child-starting; independently prove child absence, then remove \"/recorded/sessions/fleet/roles/planner.json\" (indeterminate-child-starting)\n"
	if code != exitUnsafe || stderr.String() != want || strings.Contains(stderr.String(), "(missing)") {
		t.Fatalf("code=%d stderr=%q, want %d %q", code, stderr.String(), exitUnsafe, want)
	}
}

func TestRunRelaunchMapsCommitAndOwnedRollbackOutcomes(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		err  error
		code int
		want string
	}{
		{name: "missing executable", err: &preflight.MissingExecutableError{Name: "claude"}, code: exitMissingExecutable, want: "agentctl: required executable \"claude\" was not found; no role was mutated\n"},
		{name: "uncertain fleet config", err: &shim.RecordCommitUncertainError{Err: errors.New("sync failed")}, code: exitLaunchUnproven, want: "agentctl: role \"planner\" in session \"fleet\" has an uncertain durable fleet-config record commit: \"durable record replacement is visible but commit is uncertain: sync failed\"; the record was retained and the role was not reported absent (record-commit-uncertain)\n"},
		{name: "complete rollback", err: &fleet.ShimRelaunchRollbackError{Session: "fleet", Role: "planner", Cause: errors.New("replace failed")}, code: exitLaunch, want: "agentctl: relaunch failed for role \"planner\" in session \"fleet\": \"replace failed\"; cleanup observed child absence and removed every artifact owned by this invocation (owned-rollback-complete)\n"},
		{name: "unclassified retained rollback", err: &fleet.ShimRelaunchRollbackError{Session: "fleet", Role: "planner", Cause: errors.New("replace failed"), CleanupErr: errors.New("child absence unproved")}, code: exitUnclassified, want: "agentctl: relaunch failed for session \"fleet\": \"shim relaunch of role \\\"planner\\\" in session \\\"fleet\\\" could not commit its fleet override: replace failed; owned cleanup incomplete: child absence unproved\" (unclassified)\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := runWithDependencies(context.Background(), []string{"relaunch", "--session", "fleet", "planner"}, &bytes.Buffer{}, &stderr, dependencies{
				resolver: &resolverStub{selected: "fleet"}, relauncher: &relauncherStub{err: test.err},
			})
			if code != test.code || stderr.String() != test.want {
				t.Fatalf("code=%d stderr=%q, want %d %q", code, stderr.String(), test.code, test.want)
			}
		})
	}
}

// This catches presentation-blind relaunch output that claims a detached
// fleet was recreated even though this transitional build cannot do so.
func TestRunRelaunchRendersDetachedPresentationTransitionRefusalExactly(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	err := &fleet.ShimDetachedRelaunchUnsupportedError{Session: "fleet", Role: "planner"}
	code := runWithDependencies(context.Background(), []string{"relaunch", "--session", "fleet", "planner"}, &bytes.Buffer{}, &stderr, dependencies{
		resolver: &resolverStub{selected: "fleet"}, relauncher: &relauncherStub{err: err},
	})
	want := "agentctl: refusing to relaunch role \"planner\" in session \"fleet\"; durable fleet presentation is detached and this build cannot recreate a detached role (detached-relaunch-unsupported)\n"
	if code != exitUnsafe || stderr.String() != want {
		t.Fatalf("code=%d stderr=%q, want %d %q", code, stderr.String(), exitUnsafe, want)
	}
}

func TestRunRelaunchRejectsInvalidOverridesBeforeDependencies(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{
		{"relaunch"},
		{"relaunch", "--harness", "bash", "planner"},
		{"relaunch", "--model", "", "planner"},
		{"relaunch", "INVALID"},
	} {
		stub := &relauncherStub{err: errors.New("must not call")}
		var stderr bytes.Buffer
		if code := runWithDependencies(context.Background(), arguments, &bytes.Buffer{}, &stderr, dependencies{relauncher: stub}); code != exitUsage || stub.called {
			t.Fatalf("arguments=%q code=%d called=%t stderr=%q", arguments, code, stub.called, stderr.String())
		}
	}
}

type relauncherStub struct {
	result  fleet.ShimRelaunchResult
	err     error
	called  bool
	session string
	request fleet.RelaunchRequest
}

func (r *relauncherStub) Relaunch(_ context.Context, sessionName string, request fleet.RelaunchRequest) (fleet.ShimRelaunchResult, error) {
	r.called = true
	r.session, r.request = sessionName, request
	return r.result, r.err
}
