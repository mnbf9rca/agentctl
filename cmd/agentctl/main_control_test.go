//go:build darwin

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/control"
	"github.com/mnbf9rca/agentctl/internal/shim"
)

func TestRunControlSendsOnlyRegisteredOperationAndValidatedIdentity(t *testing.T) {
	t.Parallel()

	written := uint64(7)
	submitted := true
	controller := &controlStub{response: shim.Response{
		Version: shim.ShimProtocolVersion, Outcome: shim.OutcomeDeliverySubmitted,
		BytesWritten: &written, SubmitObserved: &submitted,
	}}
	resolver := &resolverStub{selected: "fleet"}
	var stdout, stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{"clear", "--session", "fleet", "planner"}, &stdout, &stderr, dependencies{resolver: resolver, controller: controller})
	if code != exitOK || controller.operation != "clear" || controller.session != "fleet" || controller.role != "planner" {
		t.Fatalf("code=%d invocation=%q/%q/%q stderr=%q", code, controller.operation, controller.session, controller.role, stderr.String())
	}
	want := "agentctl: clear for role \"planner\" in session \"fleet\" wrote 7 bytes and observed submit\n"
	if stdout.Len() != 0 || stderr.String() != want {
		t.Fatalf("stdout=%q stderr=%q, want %q", stdout.String(), stderr.String(), want)
	}
}

func TestRunControlRejectsCallerPayloadSurfaceBeforeDependencies(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{
		{"clear", "planner", "arbitrary"},
		{"compact", "--payload", "text", "planner"},
		{"clear", "--keys", "C-c", "planner"},
	} {
		controller := &controlStub{}
		var stderr bytes.Buffer
		code := runWithDependencies(context.Background(), arguments, &bytes.Buffer{}, &stderr, dependencies{controller: controller})
		if code != exitUsage || controller.called {
			t.Fatalf("arguments=%q code=%d called=%t stderr=%q", arguments, code, controller.called, stderr.String())
		}
	}
}

func TestRunControlMapsDistinctGuardOutcomesExactly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		code int
		want string
	}{
		{
			name: "observed self target", err: &control.ObservedSelfTargetError{TargetPID: 41, CallerPID: 99}, code: exitUnsafe,
			want: "agentctl: refusing to clear role \"planner\" in session \"fleet\"; target shim PID 41 is an ancestor of caller PID 99 (observed-self-target)\n",
		},
		{
			name: "ancestry undetermined", err: &control.AncestryUndeterminedError{TargetPID: 41, CallerPID: 99, Cause: errors.New("ps failed")}, code: exitTmux,
			want: "agentctl: refusing to clear role \"planner\" in session \"fleet\"; could not determine whether caller PID 99 descends from target shim PID 41: \"ps failed\" (ancestry-undetermined)\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := runWithDependencies(context.Background(), []string{"clear", "planner"}, &bytes.Buffer{}, &stderr, dependencies{
				resolver: &resolverStub{selected: "fleet"}, controller: &controlStub{err: test.err},
			})
			if code != test.code || stderr.String() != test.want {
				t.Fatalf("code=%d stderr=%q, want %d %q", code, stderr.String(), test.code, test.want)
			}
		})
	}
}

func TestRunControlMapsDirectProtocolDecodeFailuresExactly(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		err     error
		outcome string
	}{
		{name: "schema", err: &shim.ProtocolSchemaError{Kind: shim.ProtocolSchemaUnknownField, Field: "payload"}, outcome: "could not interpret version-1 shim protocol for role \"planner\" in session \"fleet\": \"unknown field \\\"payload\\\"\" (protocol-schema-invalid)"},
		{name: "json", err: &shim.JSONError{Kind: shim.JSONTrailingBytes}, outcome: "could not read protocol frame from connected shim for role \"planner\" in session \"fleet\": \"payload has trailing bytes after its JSON value\" (protocol-frame-read-invalid)"},
		{name: "frame read", err: &shim.ProtocolFrameError{Direction: shim.ProtocolFrameRead, Peer: shim.ProtocolPeerShim, Err: errors.New("unexpected EOF")}, outcome: "could not read protocol frame from connected shim for role \"planner\" in session \"fleet\": \"unexpected EOF\" (protocol-frame-read-invalid)"},
		{name: "frame write", err: &shim.ProtocolFrameError{Direction: shim.ProtocolFrameWrite, Peer: shim.ProtocolPeerShim, Err: errors.New("broken pipe")}, outcome: "could not write protocol request to connected shim for role \"planner\" in session \"fleet\": \"broken pipe\" (protocol-frame-write-failed)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := runWithDependencies(context.Background(), []string{"clear", "planner"}, &bytes.Buffer{}, &stderr, dependencies{
				resolver: &resolverStub{selected: "fleet"}, controller: &controlStub{err: test.err},
			})
			want := "agentctl: " + test.outcome + "\n"
			if code != exitTmux || stderr.String() != want {
				t.Fatalf("code=%d stderr=%q, want %d %q", code, stderr.String(), exitTmux, want)
			}
		})
	}
}

func TestRunControlMapsDisagreementAndManualRecoveryOutcomesWithoutCallingThemMissing(t *testing.T) {
	t.Parallel()

	state := "child-starting"
	shimPID, childPID := 41, 73
	local, recorded, path := "/local", "/recorded", "/recorded/sessions/fleet/roles/planner.json"
	recordedToken := shim.StartToken{Sec: 10, Usec: 20}
	observedToken := shim.StartToken{Sec: 11, Usec: 21}
	tests := []struct {
		name     string
		response shim.Response
		want     string
	}{
		{
			name: "state root", response: shim.Response{Version: 1, Outcome: shim.OutcomeStateRootDisagreement, LocalRoot: &local, RecordedRoot: &recorded},
			want: "resolved state root \"/local\" differs from lockfile-recorded state root \"/recorded\" (state-root-disagreement)",
		},
		{
			name: "start token", response: shim.Response{Version: 1, Outcome: shim.OutcomePresentTokenDisagreement, ChildPID: &childPID, RecordedToken: &recordedToken, ObservedToken: &observedToken},
			want: "child PID 73 start token {sec:11,usec:21} differs from recorded token {sec:10,usec:20} (present-token-disagreement)",
		},
		{
			name: "manual only child starting", response: shim.Response{Version: 1, Outcome: shim.OutcomeIndeterminateChildStarting, State: &state, ShimPID: &shimPID, RecordPath: &path},
			want: "shim PID 41 was absent and the durable record is child-starting; independently prove child absence, then remove \"/recorded/sessions/fleet/roles/planner.json\" (indeterminate-child-starting)",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := runWithDependencies(context.Background(), []string{"clear", "planner"}, &bytes.Buffer{}, &stderr, dependencies{
				resolver: &resolverStub{selected: "fleet"}, controller: &controlStub{response: test.response},
			})
			if code != exitUnsafe || !strings.Contains(stderr.String(), test.want) || strings.Contains(stderr.String(), "(missing)") {
				t.Fatalf("code=%d stderr=%q want fragment=%q", code, stderr.String(), test.want)
			}
		})
	}
}

func TestRunControlMapsClosedShimResponseClasses(t *testing.T) {
	t.Parallel()

	state := "child-recorded"
	stopping := "stopping"
	causeBadRole := `role "other" differs from connected role "planner"`
	causeSchema := `unknown field "payload"`
	causeVersion := "2"
	causeClaim := "claim"
	causeCleanup := "SIGHUP failed"
	cleanupObservation := "present-match"
	causeContended := "flock returned EWOULDBLOCK"
	causeObserve := "operation not permitted"
	shimPID, childPID, callerPID, targetPID := 41, 73, 99, 41
	tests := []struct {
		name     string
		response shim.Response
		code     int
		want     string
	}{
		{"invalid request", shim.Response{Version: 1, Outcome: shim.OutcomeInvalidRequest, Cause: &causeBadRole}, exitUsage, "agentctl: invalid shim request for session \"fleet\" role \"planner\": \"role \\\"other\\\" differs from connected role \\\"planner\\\"\"; no role was mutated\n"},
		{"schema", shim.Response{Version: 1, Outcome: shim.OutcomeProtocolSchemaInvalid, Cause: &causeSchema}, exitTmux, "agentctl: could not interpret version-1 shim protocol for role \"planner\" in session \"fleet\": \"unknown field \\\"payload\\\"\" (protocol-schema-invalid)\n"},
		{"skew", shim.Response{Version: 1, Outcome: shim.OutcomeProtocolSkew, Cause: &causeVersion}, exitSession, "agentctl: refusing to clear role \"planner\" in session \"fleet\"; connected shim hello protocol version was 2; expected 1 (protocol-skew)\n"},
		{"answerer claim", shim.Response{Version: 1, Outcome: shim.OutcomeAnswererDisagreement, ShimPID: &shimPID, TargetPID: &targetPID, Cause: &causeClaim}, exitUnsafe, "agentctl: refusing to clear role \"planner\" in session \"fleet\"; LOCAL_PEERPID 41 answered without the matching held role claim (answerer-disagreement)\n"},
		{"cleanup failed", shim.Response{Version: 1, Outcome: shim.OutcomeCleanupFailed, ChildPID: &childPID, Cause: &causeCleanup, Cleanup: &cleanupObservation}, exitUnsafe, "agentctl: refusing to clear role \"planner\" in session \"fleet\"; durable cleanup is incomplete after \"SIGHUP failed\" and child observation is present-match (cleanup-failed)\n"},
		{"contender", shim.Response{Version: 1, Outcome: shim.OutcomeConcurrentContender, ShimPID: &shimPID, Cause: &causeContended}, exitUnsafe, "agentctl: refusing to clear role \"planner\" in session \"fleet\"; flock returned EWOULDBLOCK while lockfile shim PID 41 holds the role claim (concurrent-contender)\n"},
		{"present not ours", shim.Response{Version: 1, Outcome: shim.OutcomePresentNotOurs, ChildPID: &childPID}, exitUnsafe, "agentctl: refusing to clear role \"planner\" in session \"fleet\"; kill(73, 0) returned EPERM (present-not-ours)\n"},
		{"could not observe", shim.Response{Version: 1, Outcome: shim.OutcomeCouldNotObserve, ChildPID: &childPID, Cause: &causeObserve}, exitTmux, "agentctl: could not observe child PID 73 for role \"planner\" in session \"fleet\": kill(73, 0) returned \"operation not permitted\" (could-not-observe)\n"},
		{"starting", shim.Response{Version: 1, Outcome: shim.OutcomeStarting, State: &state, ShimPID: &shimPID, ChildPID: &childPID}, exitUnsafe, "agentctl: refusing to clear role \"planner\" in session \"fleet\"; shim PID 41 holds the claim and the durable record is child-recorded (starting)\n"},
		{"observed self response", shim.Response{Version: 1, Outcome: shim.OutcomeObservedSelfTarget, CallerPID: &callerPID, TargetPID: &targetPID}, exitUnsafe, "agentctl: refusing to clear role \"planner\" in session \"fleet\"; target shim PID 41 is an ancestor of caller PID 99 (observed-self-target)\n"},
		{"stopping", shim.Response{Version: 1, Outcome: shim.OutcomeShimStopping, State: &stopping, ShimPID: &shimPID, ChildPID: &childPID}, exitUnsafe, "agentctl: refusing to clear role \"planner\" in session \"fleet\"; shim PID 41 state is stopping for child PID 73; no PTY input was written (shim-stopping)\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := runWithDependencies(context.Background(), []string{"clear", "planner"}, &bytes.Buffer{}, &stderr, dependencies{resolver: &resolverStub{selected: "fleet"}, controller: &controlStub{response: test.response}})
			if code != test.code || stderr.String() != test.want {
				t.Fatalf("code=%d stderr=%q, want %d %q", code, stderr.String(), test.code, test.want)
			}
		})
	}
}

type controlStub struct {
	response                 shim.Response
	err                      error
	operation, session, role string
	called                   bool
}

func (c *controlStub) Execute(_ context.Context, operation, sessionName, role string) (shim.Response, error) {
	c.called = true
	c.operation, c.session, c.role = operation, sessionName, role
	return c.response, c.err
}
