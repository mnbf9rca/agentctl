//go:build darwin

package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/mnbf9rca/agentctl/internal/attach"
	"github.com/mnbf9rca/agentctl/internal/control"
	"github.com/mnbf9rca/agentctl/internal/fleet"
	"github.com/mnbf9rca/agentctl/internal/kill"
	"github.com/mnbf9rca/agentctl/internal/preflight"
	"github.com/mnbf9rca/agentctl/internal/shim"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func shimSetupError(stderr io.Writer, operation string, err error) int {
	var invalid *shim.InvalidRootError
	if errors.As(err, &invalid) {
		fmt.Fprintf(stderr, "agentctl: invalid %s root %q: %q; no role was mutated\n", invalid.Kind, invalid.Path, invalid.Reason)
		return exitUsage
	}
	fmt.Fprintf(stderr, "agentctl: %s failed for session %q: %q (unclassified)\n", operation, "", err.Error())
	return exitUnclassified
}

func shimLaunchError(stderr io.Writer, sessionName, role string, err error) int {
	var directory *fleet.DirectoryError
	if errors.As(err, &directory) {
		fmt.Fprintf(stderr, "agentctl: launch failed for session %q: %q (unclassified)\n", sessionName, err.Error())
		return exitUnclassified
	}
	var missing *preflight.MissingExecutableError
	if errors.As(err, &missing) {
		fmt.Fprintf(stderr, "agentctl: required executable %q was not found; no role was mutated\n", missing.Name)
		return exitMissingExecutable
	}
	var exists *fleet.ShimFleetExistsError
	if errors.As(err, &exists) {
		fmt.Fprintf(stderr, "agentctl: refusing to launch session %q; durable fleet configuration already exists (fleet-config-exists)\n", sessionName)
		return exitSession
	}
	var uncertain *shim.RecordCommitUncertainError
	if errors.As(err, &uncertain) {
		fmt.Fprintf(stderr, "agentctl: role %q in session %q has an uncertain durable fleet-config record commit: %q; the record was retained and the role was not reported absent (record-commit-uncertain)\n", role, sessionName, uncertain.Error())
		return exitLaunchUnproven
	}
	var rollback *fleet.ShimLaunchRollbackError
	if errors.As(err, &rollback) {
		if rollback.CleanupErr == nil {
			fmt.Fprintf(stderr, "agentctl: launch failed for role %q in session %q: %q; cleanup observed child absence and removed every artifact owned by this invocation (owned-rollback-complete)\n", rollback.Role, rollback.Session, rollback.Cause.Error())
			return exitLaunch
		}
		// This type does not carry the independently observed §15.8 REMAINING
		// artifact set, so selecting owned-rollback-incomplete would invent it.
		fmt.Fprintf(stderr, "agentctl: launch failed for session %q: %q (unclassified)\n", sessionName, rollback.Error())
		return exitUnclassified
	}
	fmt.Fprintf(stderr, "agentctl: launch failed for session %q: %q (unclassified)\n", sessionName, err.Error())
	return exitUnclassified
}

func shimResponseResult(stderr io.Writer, operation, sessionName, role string, response shim.Response) int {
	value := func(pointer *string) string {
		if pointer == nil {
			return ""
		}
		return *pointer
	}
	pid := func(pointer *int) int {
		if pointer == nil {
			return 0
		}
		return *pointer
	}
	bytesWritten := uint64(0)
	if response.BytesWritten != nil {
		bytesWritten = *response.BytesWritten
	}
	observation := fleet.ShimRoleObservation{
		Outcome: response.Outcome, State: value(response.State), ShimPID: pid(response.ShimPID), ChildPID: pid(response.ChildPID),
		RecordPath: value(response.RecordPath), LocalRoot: value(response.LocalRoot), RecordedRoot: value(response.RecordedRoot), AnswererPID: pid(response.TargetPID),
		CallerPID: pid(response.CallerPID), TargetPID: pid(response.TargetPID), Cleanup: value(response.Cleanup),
		RecordedToken: response.RecordedToken, ObservedToken: response.ObservedToken,
	}
	if response.Cause != nil {
		observation.Cause = errors.New(*response.Cause)
	}
	switch response.Outcome {
	case shim.OutcomeDeliverySubmitted:
		fmt.Fprintf(stderr, "agentctl: %s for role %q in session %q wrote %d bytes and observed submit\n", operation, role, sessionName, bytesWritten)
		return exitOK
	case shim.OutcomeDeliveryCancelledClean:
		fmt.Fprintf(stderr, "agentctl: %s for role %q in session %q was cancelled before any payload byte was written (delivery-cancelled)\n", operation, role, sessionName)
		return exitUnsafe
	case shim.OutcomeDeliveryCancelledWithResidue:
		fmt.Fprintf(stderr, "agentctl: %s for role %q in session %q was cancelled after %d payload bytes were written but before submit; terminal input may contain residue (delivery-cancelled-with-residue)\n", operation, role, sessionName, bytesWritten)
		return exitUnsafe
	case shim.OutcomeShimStopping:
		fmt.Fprintf(stderr, "agentctl: refusing to %s role %q in session %q; shim PID %d state is %s for child PID %d; no PTY input was written (shim-stopping)\n", operation, role, sessionName, observation.ShimPID, observation.State, observation.ChildPID)
		return exitUnsafe
	default:
		return shimObservationResult(stderr, operation, sessionName, role, observation)
	}
}

func shimOperationError(stderr io.Writer, operation, sessionName, role string, err error) int {
	var missingFleet *fleet.ShimFleetMissingError
	if errors.As(err, &missingFleet) {
		fmt.Fprintf(stderr, "agentctl: session %q has no durable fleet configuration\n", missingFleet.Session)
		return exitSession
	}
	var self *control.ObservedSelfTargetError
	if errors.As(err, &self) {
		fmt.Fprintf(stderr, "agentctl: refusing to %s role %q in session %q; target shim PID %d is an ancestor of caller PID %d (observed-self-target)\n", operation, role, sessionName, self.TargetPID, self.CallerPID)
		return exitUnsafe
	}
	var ancestry *control.AncestryUndeterminedError
	if errors.As(err, &ancestry) {
		fmt.Fprintf(stderr, "agentctl: refusing to %s role %q in session %q; could not determine whether caller PID %d descends from target shim PID %d: %q (ancestry-undetermined)\n", operation, role, sessionName, ancestry.CallerPID, ancestry.TargetPID, ancestry.Cause.Error())
		return exitTmux
	}
	var unknown *fleet.UnknownRoleError
	if errors.As(err, &unknown) {
		fmt.Fprintf(stderr, "agentctl: role %q is not in the durable roster for session %q\n", role, sessionName)
		return exitRole
	}
	var root *shim.StateRootDisagreementError
	if errors.As(err, &root) {
		fmt.Fprintf(stderr, "agentctl: refusing to %s role %q in session %q; resolved state root %q differs from lockfile-recorded state root %q (state-root-disagreement)\n", operation, role, sessionName, root.LocalRoot, root.RecordedRoot)
		return exitUnsafe
	}
	var answerer *shim.AnswererDisagreementError
	if errors.As(err, &answerer) {
		fmt.Fprintf(stderr, "agentctl: refusing to %s role %q in session %q; lockfile shim PID %d differs from connected LOCAL_PEERPID %d (answerer-disagreement)\n", operation, role, sessionName, answerer.RecordedPID, answerer.AnswererPID)
		return exitUnsafe
	}
	var skew *shim.ProtocolSkewError
	if errors.As(err, &skew) {
		if skew.Kind == shim.ProtocolSkewAbsent {
			fmt.Fprintf(stderr, "agentctl: refusing to %s role %q in session %q; connected shim hello protocol version was absent; expected 1 (protocol-skew)\n", operation, role, sessionName)
		} else {
			fmt.Fprintf(stderr, "agentctl: refusing to %s role %q in session %q; connected shim hello protocol version was %s; expected 1 (protocol-skew)\n", operation, role, sessionName, skew.CanonicalObserved())
		}
		return exitSession
	}
	var schema *shim.ProtocolSchemaError
	if errors.As(err, &schema) {
		fmt.Fprintf(stderr, "agentctl: could not interpret version-1 shim protocol for role %q in session %q: %q (protocol-schema-invalid)\n", role, sessionName, schema.CanonicalCause())
		return exitTmux
	}
	var jsonError *shim.JSONError
	if errors.As(err, &jsonError) {
		fmt.Fprintf(stderr, "agentctl: could not read protocol frame from connected shim for role %q in session %q: %q (protocol-frame-read-invalid)\n", role, sessionName, jsonError.CanonicalCause())
		return exitTmux
	}
	var frame *shim.ProtocolFrameError
	if errors.As(err, &frame) && frame.Peer == shim.ProtocolPeerShim {
		switch frame.Direction {
		case shim.ProtocolFrameWrite:
			fmt.Fprintf(stderr, "agentctl: could not write protocol request to connected shim for role %q in session %q: %q (protocol-frame-write-failed)\n", role, sessionName, errorText(frame.Err))
		case shim.ProtocolFrameRead:
			fmt.Fprintf(stderr, "agentctl: could not read protocol frame from connected shim for role %q in session %q: %q (protocol-frame-read-invalid)\n", role, sessionName, errorText(frame.Err))
		default:
			break
		}
		if frame.Direction == shim.ProtocolFrameRead || frame.Direction == shim.ProtocolFrameWrite {
			return exitTmux
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "agentctl: role %q in session %q has no live claim or durable role record (missing)\n", role, sessionName)
		return exitRole
	}
	var tmuxFailure *tmuxx.TmuxError
	if errors.As(err, &tmuxFailure) {
		fmt.Fprintf(stderr, "agentctl: %s failed for session %q: %q (unclassified)\n", operation, sessionName, err.Error())
		return exitUnclassified
	}
	fmt.Fprintf(stderr, "agentctl: %s failed for session %q: %q (unclassified)\n", operation, sessionName, err.Error())
	return exitUnclassified
}

func shimObservationResult(stderr io.Writer, operation, sessionName, role string, observation fleet.ShimRoleObservation) int {
	cause := ""
	if observation.Cause != nil {
		cause = observation.Cause.Error()
	}
	switch observation.Outcome {
	case shim.OutcomeInvalidRequest:
		fmt.Fprintf(stderr, "agentctl: invalid shim request for session %q role %q: %q; no role was mutated\n", sessionName, role, cause)
		return exitUsage
	case shim.OutcomeProtocolSchemaInvalid:
		fmt.Fprintf(stderr, "agentctl: could not interpret version-1 shim protocol for role %q in session %q: %q (protocol-schema-invalid)\n", role, sessionName, cause)
		return exitTmux
	case shim.OutcomeProtocolSkew:
		if cause == "absent" {
			fmt.Fprintf(stderr, "agentctl: refusing to %s role %q in session %q; connected shim hello protocol version was absent; expected 1 (protocol-skew)\n", operation, role, sessionName)
		} else {
			fmt.Fprintf(stderr, "agentctl: refusing to %s role %q in session %q; connected shim hello protocol version was %s; expected 1 (protocol-skew)\n", operation, role, sessionName, cause)
		}
		return exitSession
	case shim.OutcomeAnswererDisagreement:
		if cause == "claim" {
			fmt.Fprintf(stderr, "agentctl: refusing to %s role %q in session %q; LOCAL_PEERPID %d answered without the matching held role claim (answerer-disagreement)\n", operation, role, sessionName, observation.AnswererPID)
		} else {
			fmt.Fprintf(stderr, "agentctl: refusing to %s role %q in session %q; lockfile shim PID %d differs from connected LOCAL_PEERPID %d (answerer-disagreement)\n", operation, role, sessionName, observation.ShimPID, observation.AnswererPID)
		}
		return exitUnsafe
	case shim.OutcomeCleanupFailed:
		fmt.Fprintf(stderr, "agentctl: refusing to %s role %q in session %q; durable cleanup is incomplete after %q and child observation is %s (cleanup-failed)\n", operation, role, sessionName, cause, observation.Cleanup)
		return exitUnsafe
	case shim.OutcomeConcurrentContender:
		fmt.Fprintf(stderr, "agentctl: refusing to %s role %q in session %q; flock returned EWOULDBLOCK while lockfile shim PID %d holds the role claim (concurrent-contender)\n", operation, role, sessionName, observation.ShimPID)
		return exitUnsafe
	case shim.OutcomeObservedSelfTarget:
		fmt.Fprintf(stderr, "agentctl: refusing to %s role %q in session %q; target shim PID %d is an ancestor of caller PID %d (observed-self-target)\n", operation, role, sessionName, observation.TargetPID, observation.CallerPID)
		return exitUnsafe
	case shim.OutcomeAncestryUndetermined:
		fmt.Fprintf(stderr, "agentctl: refusing to %s role %q in session %q; could not determine whether caller PID %d descends from target shim PID %d: %q (ancestry-undetermined)\n", operation, role, sessionName, observation.CallerPID, observation.TargetPID, cause)
		return exitTmux
	case shim.OutcomeMissing:
		fmt.Fprintf(stderr, "agentctl: role %q in session %q has no live claim or durable role record (missing)\n", role, sessionName)
		return exitRole
	case shim.OutcomeStaleRecord:
		fmt.Fprintf(stderr, "agentctl: role %q in session %q has stale child PID %d after kill(%d, 0) returned ESRCH (stale-record)\n", role, sessionName, observation.ChildPID, observation.ChildPID)
		return exitRole
	case shim.OutcomeInvalidRecord:
		fmt.Fprintf(stderr, "agentctl: refusing to %s role %q in session %q; durable record %q is invalid: %q (invalid-record)\n", operation, role, sessionName, observation.RecordPath, cause)
		return exitUnsafe
	case shim.OutcomeOrphan:
		fmt.Fprintf(stderr, "agentctl: refusing to %s role %q in session %q; shim PID %d was absent and recorded child PID %d was present with a matching start token (orphan)\n", operation, role, sessionName, observation.ShimPID, observation.ChildPID)
		return exitUnsafe
	case shim.OutcomeIndeterminateChildStarting:
		fmt.Fprintf(stderr, "agentctl: refusing to %s role %q in session %q; shim PID %d was absent and the durable record is child-starting; independently prove child absence, then remove %q (indeterminate-child-starting)\n", operation, role, sessionName, observation.ShimPID, observation.RecordPath)
		return exitUnsafe
	case shim.OutcomeStarting:
		state := observation.State
		if state == "" {
			state = "child-starting"
		}
		fmt.Fprintf(stderr, "agentctl: refusing to %s role %q in session %q; shim PID %d holds the claim and the durable record is %s (starting)\n", operation, role, sessionName, observation.ShimPID, state)
		return exitUnsafe
	case shim.OutcomeStateRootDisagreement:
		localRoot, recordedRoot := observation.LocalRoot, observation.RecordedRoot
		var root *shim.StateRootDisagreementError
		if errors.As(observation.Cause, &root) {
			localRoot, recordedRoot = root.LocalRoot, root.RecordedRoot
		}
		fmt.Fprintf(stderr, "agentctl: refusing to %s role %q in session %q; resolved state root %q differs from lockfile-recorded state root %q (state-root-disagreement)\n", operation, role, sessionName, localRoot, recordedRoot)
		return exitUnsafe
	case shim.OutcomePresentTokenDisagreement:
		fmt.Fprintf(stderr, "agentctl: refusing to %s role %q in session %q; child PID %d start token %s differs from recorded token %s (present-token-disagreement)\n", operation, role, sessionName, observation.ChildPID, renderStartToken(observation.ObservedToken), renderStartToken(observation.RecordedToken))
		return exitUnsafe
	case shim.OutcomePresentNotOurs:
		fmt.Fprintf(stderr, "agentctl: refusing to %s role %q in session %q; kill(%d, 0) returned EPERM (present-not-ours)\n", operation, role, sessionName, observation.ChildPID)
		return exitUnsafe
	case shim.OutcomeStopAlreadyStopping:
		fmt.Fprintf(stderr, "agentctl: stop for role %q in session %q found shim PID %d state %s for child PID %d; no second signal was attempted and no PTY input was written (stop-already-stopping)\n", role, sessionName, observation.ShimPID, observation.State, observation.ChildPID)
		return exitUnsafe
	case shim.OutcomeCouldNotObserve:
		fmt.Fprintf(stderr, "agentctl: could not observe child PID %d for role %q in session %q: kill(%d, 0) returned %q (could-not-observe)\n", observation.ChildPID, role, sessionName, observation.ChildPID, cause)
		return exitTmux
	default:
		fmt.Fprintf(stderr, "agentctl: %s failed for session %q: %q (unclassified)\n", operation, sessionName, string(observation.Outcome))
		return exitUnclassified
	}
}

func renderStartToken(token *shim.StartToken) string {
	if token == nil {
		return "{sec:0,usec:0}"
	}
	return fmt.Sprintf("{sec:%d,usec:%d}", token.Sec, token.Usec)
}

func shimRelaunchError(stderr io.Writer, sessionName, role string, err error) int {
	var detached *fleet.ShimDetachedRelaunchUnsupportedError
	if errors.As(err, &detached) {
		fmt.Fprintf(stderr, "agentctl: refusing to relaunch role %q in session %q; durable fleet presentation is detached and this build cannot recreate a detached role (detached-relaunch-unsupported)\n", detached.Role, detached.Session)
		return exitUnsafe
	}
	var refusal *fleet.ShimRelaunchRefusalError
	if errors.As(err, &refusal) {
		return shimObservationResult(stderr, "relaunch", sessionName, role, refusal.Observation)
	}
	var missing *preflight.MissingExecutableError
	if errors.As(err, &missing) {
		fmt.Fprintf(stderr, "agentctl: required executable %q was not found; no role was mutated\n", missing.Name)
		return exitMissingExecutable
	}
	var uncertain *shim.RecordCommitUncertainError
	if errors.As(err, &uncertain) {
		fmt.Fprintf(stderr, "agentctl: role %q in session %q has an uncertain durable fleet-config record commit: %q; the record was retained and the role was not reported absent (record-commit-uncertain)\n", role, sessionName, uncertain.Error())
		return exitLaunchUnproven
	}
	var rollback *fleet.ShimRelaunchRollbackError
	if errors.As(err, &rollback) && rollback.CleanupErr == nil {
		fmt.Fprintf(stderr, "agentctl: relaunch failed for role %q in session %q: %q; cleanup observed child absence and removed every artifact owned by this invocation (owned-rollback-complete)\n", rollback.Role, rollback.Session, rollback.Cause.Error())
		return exitLaunch
	}
	return shimOperationError(stderr, "relaunch", sessionName, role, err)
}

func shimKillError(stderr io.Writer, sessionName string, err error) int {
	var missingFleet *fleet.ShimFleetMissingError
	if errors.As(err, &missingFleet) {
		fmt.Fprintf(stderr, "agentctl: session %q has no durable fleet configuration\n", missingFleet.Session)
		return exitSession
	}
	var refusal *kill.ShimKillRefusalError
	if errors.As(err, &refusal) {
		return shimObservationResult(stderr, "kill", sessionName, refusal.Role, refusal.Observation)
	}
	var cleanupRetained *kill.ShimKillCleanupRetainedError
	if errors.As(err, &cleanupRetained) {
		fmt.Fprintf(stderr, "agentctl: stop for role %q in session %q observed child PID %d exit, but role cleanup was not observed complete; last outcome was %s: %q; presentation and fleet record were retained (post-exit-cleanup-retained)\n", cleanupRetained.Role, cleanupRetained.Session, cleanupRetained.ChildPID, cleanupRetained.LastOutcome, errorText(cleanupRetained.Cause))
		return exitLaunchUnproven
	}
	var retained *kill.ShimKillRetainedError
	if errors.As(err, &retained) {
		fmt.Fprintf(stderr, "agentctl: stop for role %q in session %q attempted SIGHUP but did not observe child PID %d exit; child observation was %s; ownership and the durable record were retained (stop-child-retained)\n", retained.Role, retained.Session, retained.ChildPID, retained.Observation)
		return exitLaunchUnproven
	}
	fmt.Fprintf(stderr, "agentctl: kill failed for session %q: %q (unclassified)\n", sessionName, err.Error())
	return exitUnclassified
}

func attachNoPresentationError(stderr io.Writer, err *attach.NoPresentationError) int {
	fmt.Fprintf(stderr, "agentctl: %v\n", err)
	return exitSession
}
