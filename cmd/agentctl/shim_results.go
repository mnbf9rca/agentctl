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
	var amqInit *fleet.AMQInitError
	if errors.As(err, &amqInit) {
		return exitUnclassified
	}
	if code, handled := shimDetachedStartError(stderr, err); handled {
		return code
	}
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
	if code, handled := shimDetachedStartError(stderr, err); handled {
		return code
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

func shimDetachedStartError(stderr io.Writer, err error) (int, bool) {
	var failed *fleet.ShimDetachedStartFailedError
	if errors.As(err, &failed) {
		if failed.CleanupErr != nil {
			remaining := failed.Remaining
			if remaining == "" {
				remaining = "retained artifacts"
			}
			fmt.Fprintf(stderr, "agentctl: could not start a detached shim for role %q in session %q: %v; cleanup left %s: %v (detached-start-retained)\n", failed.Role, failed.Session, failed.Cause, remaining, failed.CleanupErr)
			return exitLaunchUnproven, true
		}
		fmt.Fprintf(stderr, "agentctl: could not start a detached shim for role %q in session %q: %v; no child was started and cleanup removed every artifact owned by this invocation (detached-start-failed)\n", failed.Role, failed.Session, failed.Cause)
		return exitLaunch, true
	}
	var rolledBack *fleet.ShimDetachedStartRolledBackError
	if errors.As(err, &rolledBack) {
		fmt.Fprintf(stderr, "agentctl: detached shim PID %d for role %q in session %q failed before readiness: %v; cleanup observed child absence and removed every artifact owned by this invocation (detached-start-rolled-back)\n", rolledBack.CreatedPID, rolledBack.Role, rolledBack.Session, rolledBack.Cause)
		return exitLaunch, true
	}
	var retained *fleet.ShimDetachedStartRetainedError
	if errors.As(err, &retained) {
		remaining := retained.Remaining
		if remaining == "" {
			remaining = "retained artifacts"
		}
		fmt.Fprintf(stderr, "agentctl: detached shim PID %d for role %q in session %q failed before readiness: %v; cleanup left %s: %v (detached-start-retained)\n", retained.CreatedPID, retained.Role, retained.Session, retained.Cause, remaining, retained.CleanupErr)
		return exitLaunchUnproven, true
	}
	var uncertain *fleet.ShimDetachedStartUncertainError
	if errors.As(err, &uncertain) {
		fmt.Fprintf(stderr, "agentctl: detached shim PID %d for role %q in session %q neither became ready nor was observed to exit; nothing was removed and the durable record was retained (detached-start-uncertain)\n", uncertain.CreatedPID, uncertain.Role, uncertain.Session)
		return exitLaunchUnproven, true
	}
	return exitUnclassified, false
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
	fmt.Fprintf(stderr, "agentctl: refusing to attach session %s; no tmux presentation was observed; attach a role directly:\n", err.Session)
	for _, role := range err.Roster {
		fmt.Fprintf(stderr, "  agentctl attach --session %s %s\n", err.Session, role)
	}
	return exitSession
}

func writeRoleAttachResult(stderr io.Writer, sessionName, role string, result attach.RoleResult) int {
	switch result.Disposition {
	case shim.AttachDispositionChildExited:
		fmt.Fprintf(stderr, "agentctl: role %s in session %s ended while attached; %d bytes were relayed (attach-viewer-ended)\n", role, sessionName, result.Bytes)
		return exitOK
	case shim.AttachDispositionViewerEvicted:
		fmt.Fprintf(stderr, "agentctl: attachment to role %s in session %s was ended because keeping it would have required buffering more than 131072 bytes of role output; ending it stopped nothing in the role (attach-evicted-slow)\n", role, sessionName)
	case shim.AttachDispositionCleanupRetained:
		fmt.Fprintf(stderr, "agentctl: attachment to role %s in session %s ended while the shim retained ownership during cleanup; the role's disposition is not established by this command (attach-ended-cleanup-retained)\n", role, sessionName)
	case shim.AttachDispositionServerClosing:
		fmt.Fprintf(stderr, "agentctl: attachment to role %s in session %s ended because the shim closed the stream; the role's disposition is not established by this command (attach-ended-server-closing)\n", role, sessionName)
	case shim.AttachDispositionTailUndelivered:
		fmt.Fprintf(stderr, "agentctl: role %s in session %s ended while attached; %d bytes were relayed, but %d bytes of its final output could not be delivered before the flush deadline and were dropped; the terminal above is incomplete (attach-tail-undelivered)\n", role, sessionName, result.Bytes, result.Undelivered)
	case shim.AttachDispositionTailUnconfirmed:
		if result.KnownUndelivered == 0 {
			fmt.Fprintf(stderr, "agentctl: role %s in session %s ended while attached; %d bytes were relayed and no further bytes are known to have been missed, but the output cutoff was never confirmed, so whether any more of its final output was missed is unknown (attach-tail-unconfirmed-none-known)\n", role, sessionName, result.Bytes)
		} else {
			fmt.Fprintf(stderr, "agentctl: role %s in session %s ended while attached; %d bytes were relayed and %d further bytes are known not to have been, but the output cutoff was never confirmed, so whether any more of its final output was missed is unknown (attach-tail-unconfirmed)\n", role, sessionName, result.Bytes, result.KnownUndelivered)
		}
	case shim.AttachDispositionCounterExhausted:
		fmt.Fprintf(stderr, "agentctl: attachment to role %s in session %s was ended after %d bytes because a byte counter reached the largest exactly representable value; ending it stopped nothing in the role (attach-counter-exhausted)\n", role, sessionName, result.Bytes)
	case shim.AttachDispositionResizeFailed:
		fmt.Fprintf(stderr, "agentctl: could not apply window size %dx%d to role %s in session %s: %q (attach-resize-failed)\n", result.Rows, result.Cols, role, sessionName, result.Cause)
	default:
		fmt.Fprintf(stderr, "agentctl: attach transport for role %s in session %s failed during final: unknown final disposition %q (attach-transport-failed)\n", role, sessionName, result.Disposition)
		return exitTmux
	}
	return exitTmux
}

func roleAttachError(stderr io.Writer, sessionName, role string, err error) int {
	var restore *attach.TerminalRestoreError
	if errors.As(err, &restore) {
		prior := "locally-terminated"
		if restore.PriorResult != nil {
			prior = attachPriorOutcome(restore.PriorResult)
		}
		var output *attach.TerminalOutputError
		if errors.As(restore.Prior, &output) {
			if output.Prior == nil {
				prior = "stdout-failed"
				if output.Stalled {
					prior = "terminal-stalled"
				}
				fmt.Fprintf(stderr, "agentctl: attachment to role %s in session %s ended with %s, but restoring the attaching terminal failed: %q (attach-terminal-restore-failed)\n", role, sessionName, prior, restore.Cause.Error())
				return exitTmux
			}
			if output.Prior != nil {
				writeRoleAttachResult(stderr, sessionName, role, *output.Prior)
			}
			writeRoleAttachLocalOutput(stderr, sessionName, role, output, output.Prior != nil)
			fmt.Fprintf(stderr, "  restoring the attaching terminal failed: %q (attach-terminal-restore-failed)\n", restore.Cause.Error())
			return exitTmux
		}
		var transport *attach.TransportError
		if errors.As(restore.Prior, &transport) {
			prior = "transport-failed"
		}
		var refusal *attach.RefusalErrorRole
		if errors.As(restore.Prior, &refusal) {
			switch refusal.Control.Outcome {
			case shim.AttachRefusalViewerPresent,
				shim.AttachRefusalPeerUnverified,
				shim.AttachRefusalPeerUnobservable,
				shim.AttachRefusalInitialSizeFailed:
				prior = string(refusal.Control.Outcome)
			}
		}
		var roleObservation *attach.RoleObservationError
		if errors.As(restore.Prior, &roleObservation) && roleObservation.Observation.Outcome == shim.OutcomeAnswererDisagreement {
			prior = string(shim.OutcomeAnswererDisagreement)
		}
		var raw *attach.TerminalRawError
		if errors.As(restore.Prior, &raw) {
			prior = "terminal-raw-failed"
		}
		fmt.Fprintf(stderr, "agentctl: attachment to role %s in session %s ended with %s, but restoring the attaching terminal failed: %q (attach-terminal-restore-failed)\n", role, sessionName, prior, restore.Cause.Error())
		return exitTmux
	}
	var notTerminal *attach.NotTerminalError
	if errors.As(err, &notTerminal) {
		if sessionName == "" {
			fmt.Fprintf(stderr, "agentctl: refusing to attach role %s; standard input and output must both be terminals (attach-not-a-terminal)\n", role)
			return exitUsage
		}
		fmt.Fprintf(stderr, "agentctl: refusing to attach role %s in session %s; standard input and output must both be terminals (attach-not-a-terminal)\n", role, sessionName)
		return exitUsage
	}
	var mismatch *attach.TerminalMismatchError
	if errors.As(err, &mismatch) {
		if sessionName == "" {
			fmt.Fprintf(stderr, "agentctl: refusing to attach role %s; standard input and standard output are different terminals (attach-terminal-mismatch)\n", role)
			return exitUsage
		}
		fmt.Fprintf(stderr, "agentctl: refusing to attach role %s in session %s; standard input and standard output are different terminals (attach-terminal-mismatch)\n", role, sessionName)
		return exitUsage
	}
	var observation *attach.TerminalObservationError
	if errors.As(err, &observation) {
		if sessionName == "" {
			fmt.Fprintf(stderr, "agentctl: could not observe the attaching terminal for role %s: %q; no attachment was made (attach-terminal-observation-failed)\n", role, observation.Cause.Error())
			return exitTmux
		}
		fmt.Fprintf(stderr, "agentctl: could not observe the attaching terminal for role %s in session %s: %q; no attachment was made (attach-terminal-observation-failed)\n", role, sessionName, observation.Cause.Error())
		return exitTmux
	}
	var presented *attach.PresentedByTmuxError
	if errors.As(err, &presented) {
		fmt.Fprintf(stderr, "agentctl: refusing to attach role %s in session %s; the role is presented by tmux and its pane is its viewer; use agentctl attach --session %s (attach-presented-by-tmux)\n", role, sessionName, sessionName)
		return exitUnsafe
	}
	var presentationMissing *attach.PresentationMissingError
	if errors.As(err, &presentationMissing) {
		fmt.Fprintf(stderr, "agentctl: refusing to attach role %s in session %s; it was launched in tmux mode but no presentation was observed, so it has no viewer to share; recreate it with: agentctl relaunch %s (attach-presentation-missing)\n", role, sessionName, role)
		return exitUnsafe
	}
	var listenerAbsent *attach.ListenerAbsentError
	if errors.As(err, &listenerAbsent) {
		fmt.Fprintf(stderr, "agentctl: refusing to attach role %s in session %s; the role holds its claim but has no attach stream at %q (attach-listener-absent)\n", role, sessionName, listenerAbsent.Path)
		return exitUnsafe
	}
	var listenerUnobservable *attach.ListenerUnobservableError
	if errors.As(err, &listenerUnobservable) {
		fmt.Fprintf(stderr, "agentctl: could not observe the attach stream for role %s in session %s at %q: %q; no attachment was made (attach-listener-unobservable)\n", role, sessionName, listenerUnobservable.Path, listenerUnobservable.Cause.Error())
		return exitTmux
	}
	var terminalOpen *attach.TerminalOpenError
	if errors.As(err, &terminalOpen) {
		fmt.Fprintf(stderr, "agentctl: could not open this command's own handle on the attaching terminal for role %s in session %s: %q; no attachment was made (attach-terminal-open-failed)\n", role, sessionName, terminalOpen.Cause.Error())
		return exitTmux
	}
	var terminalVerify *attach.TerminalVerifyError
	if errors.As(err, &terminalVerify) {
		fmt.Fprintf(stderr, "agentctl: opened a candidate terminal handle for role %s in session %s but could not complete %s: %q; no attachment was made (attach-terminal-verify-failed)\n", role, sessionName, terminalVerify.Stage, terminalVerify.Cause.Error())
		return exitTmux
	}
	var reopen *attach.TerminalReopenMismatchError
	if errors.As(err, &reopen) {
		fmt.Fprintf(stderr, "agentctl: opening observed terminal name %q for role %s in session %s produced a candidate handle whose identity did not match the terminal this command is attached to; no attachment was made (attach-terminal-reopen-mismatch)\n", reopen.Path, role, sessionName)
		return exitTmux
	}
	var raw *attach.TerminalRawError
	if errors.As(err, &raw) {
		fmt.Fprintf(stderr, "agentctl: could not place the attaching terminal in raw mode for role %s in session %s: %q; no attachment was made (attach-terminal-raw-failed)\n", role, sessionName, raw.Cause.Error())
		return exitTmux
	}
	var signalObservation *attach.SignalObservationError
	if errors.As(err, &signalObservation) {
		fmt.Fprintf(stderr, "agentctl: could not observe the current handling of %s for role %s in session %s: %s query failed: %q; no attachment was made and this terminal was not modified (attach-signal-observation-failed)\n", canonicalSignal(signalObservation.Signal), role, sessionName, signalObservation.Observation, signalObservation.Cause.Error())
		return exitTmux
	}
	var output *attach.TerminalOutputError
	if errors.As(err, &output) {
		writeRoleAttachLocalOutput(stderr, sessionName, role, output, false)
		return exitTmux
	}
	var refusal *attach.RefusalErrorRole
	if errors.As(err, &refusal) {
		switch refusal.Control.Outcome {
		case shim.AttachRefusalViewerPresent:
			fmt.Fprintf(stderr, "agentctl: refusing to attach role %s in session %s; a viewer is already attached at PID %d (attach-viewer-present)\n", role, sessionName, refusal.Control.ViewerPID)
			return exitUnsafe
		case shim.AttachRefusalPeerUnverified:
			fmt.Fprintf(stderr, "agentctl: refusing the attach connection for role %s in session %s; connected LOCAL_PEERPID %d has uid %d; expected %d (attach-peer-unverified)\n", role, sessionName, refusal.Control.PeerPID, refusal.Control.PeerUID, refusal.Control.ShimUID)
			return exitUnsafe
		case shim.AttachRefusalPeerUnobservable:
			fmt.Fprintf(stderr, "agentctl: could not observe the attach peer for role %s in session %s: %q (attach-peer-unobservable)\n", role, sessionName, refusal.Control.Cause)
			return exitTmux
		case shim.AttachRefusalInitialSizeFailed:
			fmt.Fprintf(stderr, "agentctl: could not apply window size %dx%d to role %s in session %s: %q (attach-resize-failed)\n", refusal.Control.Rows, refusal.Control.Cols, role, sessionName, refusal.Control.Cause)
			return exitTmux
		}
	}
	var roleObservation *attach.RoleObservationError
	if errors.As(err, &roleObservation) {
		return shimObservationResult(stderr, "attach", sessionName, role, roleObservation.Observation)
	}
	var transport *attach.TransportError
	if errors.As(err, &transport) {
		fmt.Fprintf(stderr, "agentctl: attach transport for role %s in session %s failed during %s: %q (attach-transport-failed)\n", role, sessionName, transport.Phase, transport.Cause.Error())
		return exitTmux
	}
	return shimOperationError(stderr, "attach", sessionName, role, err)
}

func attachPriorOutcome(result *attach.RoleResult) string {
	if result == nil || result.Disposition == "" {
		return "locally-terminated"
	}
	return string(result.Disposition)
}

func writeRoleAttachLocalOutput(stderr io.Writer, sessionName, role string, output *attach.TerminalOutputError, indented bool) {
	prior := attachPriorOutcome(output.Prior)
	prefix := fmt.Sprintf("agentctl: attachment to role %s in session %s ended with %s, but ", role, sessionName, prior)
	if indented {
		prefix = "  "
	}
	if output.Stalled {
		fmt.Fprintf(stderr, "%sthis terminal stopped accepting output; %d of %d received bytes reached it before the wait expired and the rest was not displayed (attach-terminal-stalled)\n", prefix, output.Written, output.Raw)
		return
	}
	fmt.Fprintf(stderr, "%swriting its output to this terminal failed: %q; %d of %d received bytes reached the terminal (attach-stdout-failed)\n", prefix, output.Cause.Error(), output.Written, output.Raw)
}
