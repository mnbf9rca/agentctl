//go:build darwin

package kill

import (
	"context"
	"errors"
	"fmt"

	"github.com/mnbf9rca/agentctl/internal/fleet"
	"github.com/mnbf9rca/agentctl/internal/shim"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

// ShimExecutor is the explicitly named runtime-backed compatibility
// implementation. The legacy Executor remains unchanged until CLI cutover.
type ShimExecutor struct {
	lifecycle    fleet.ShimLifecycle
	records      fleet.ShimFleetRecords
	inspector    fleet.ShimRoleInspector
	presentation fleet.ShimPresentation
}

// ShimKillResult reports only observed stop and optional presentation facts.
type ShimKillResult struct {
	Session             string
	StoppedRoles        int
	PresentationRemoved bool
	PresentationGone    bool
}

// ShimKillPresentationRetainedError reports that exact-ID presentation
// removal failed and the one permitted post-failure observation did not prove
// the optional presentation gone. The fleet record remains retained.
type ShimKillPresentationRetainedError struct {
	Session        string
	PresentationID tmuxx.SessionID
	RemoveErr      error
	ObservedID     tmuxx.SessionID
	ObservationErr error
}

func (e *ShimKillPresentationRetainedError) Error() string {
	if e.ObservationErr != nil {
		return fmt.Sprintf("shim kill retained fleet record for session %q: exact-ID presentation removal of %q failed: %q; post-removal presentation observation failed: %q", e.Session, e.PresentationID, e.RemoveErr, e.ObservationErr)
	}
	return fmt.Sprintf("shim kill retained fleet record for session %q: exact-ID presentation removal of %q failed: %q; post-removal presentation %q remained present", e.Session, e.PresentationID, e.RemoveErr, e.ObservedID)
}

func (e *ShimKillPresentationRetainedError) Unwrap() error {
	return errors.Join(e.RemoveErr, e.ObservationErr)
}

// ShimKillRefusalError reports a role state that cannot safely enter stop or
// cleanup. No role is stopped before the whole roster passes this gate.
type ShimKillRefusalError struct {
	Session string
	Role    string
	Outcome shim.Outcome
	Cause   error
}

func (e *ShimKillRefusalError) Error() string {
	return fmt.Sprintf("refusing shim kill of session %q: role %q is %s", e.Session, e.Role, e.Outcome)
}

func (e *ShimKillRefusalError) Unwrap() error { return e.Cause }

// ShimKillRetainedError keeps signal-attempt and child-exit observations
// separate when stop does not prove the child exited.
type ShimKillRetainedError struct {
	Session         string
	Role            string
	ChildPID        int
	SignalAttempted bool
	Observation     shim.ProcessObservation
	Cause           error
}

func (e *ShimKillRetainedError) Error() string {
	return fmt.Sprintf("stop for role %q in session %q did not observe child PID %d exit; child observation was %s", e.Role, e.Session, e.ChildPID, e.Observation)
}

func (e *ShimKillRetainedError) Unwrap() error { return e.Cause }

// NewShimExecutor constructs the compatibility implementation without
// changing New or the current CLI's kill dependency.
func NewShimExecutor(
	lifecycle fleet.ShimLifecycle,
	records fleet.ShimFleetRecords,
	inspector fleet.ShimRoleInspector,
	presentation fleet.ShimPresentation,
) ShimExecutor {
	return ShimExecutor{lifecycle: lifecycle, records: records, inspector: inspector, presentation: presentation}
}

// Execute observes the complete roster before mutation, then sends only the
// closed stop operation. Presentation and fleet records are removed only after
// every recorded child is separately observed exited or absent.
func (e ShimExecutor) Execute(ctx context.Context, session string) (ShimKillResult, error) {
	if e.lifecycle == nil || e.records == nil || e.inspector == nil || e.presentation == nil {
		return ShimKillResult{}, errors.New("shim kill executor requires lifecycle, records, inspector, and presentation")
	}
	record, err := e.records.Read(session)
	if err != nil {
		return ShimKillResult{}, err
	}
	presentationSession, presentationPresent, err := e.presentation.FindPresentationSession(ctx, session)
	if err != nil {
		return ShimKillResult{}, err
	}
	observations := make(map[string]fleet.ShimRoleObservation, len(record.Roster))
	for _, role := range record.Roster {
		observation, err := e.inspector.Inspect(ctx, session, role)
		if err != nil {
			return ShimKillResult{}, err
		}
		observations[role] = observation
		switch observation.Outcome {
		case shim.OutcomeRunning, shim.OutcomeStarting, shim.OutcomeMissing, shim.OutcomeStaleRecord:
		default:
			return ShimKillResult{}, &ShimKillRefusalError{
				Session: session, Role: role, Outcome: observation.Outcome, Cause: observation.Cause,
			}
		}
	}

	result := ShimKillResult{Session: session, PresentationGone: !presentationPresent}
	for _, role := range record.Roster {
		observation := observations[role]
		if observation.Outcome != shim.OutcomeRunning && observation.Outcome != shim.OutcomeStarting {
			continue
		}
		response, err := e.lifecycle.Stop(ctx, session, role)
		if err != nil {
			return result, err
		}
		if response.Outcome != shim.OutcomeStopChildExited || response.SignalAttempted == nil || !*response.SignalAttempted || response.Signal == nil || *response.Signal != "SIGHUP" || response.ChildExitObserved == nil || !*response.ChildExitObserved {
			retained := &ShimKillRetainedError{Session: session, Role: role}
			if response.ChildPID != nil {
				retained.ChildPID = *response.ChildPID
			}
			if response.SignalAttempted != nil {
				retained.SignalAttempted = *response.SignalAttempted
			}
			if response.State != nil {
				retained.Observation = shim.ProcessObservation(*response.State)
			}
			if response.Cause != nil {
				retained.Cause = errors.New(*response.Cause)
			}
			return result, retained
		}
		result.StoppedRoles++
	}
	for _, role := range record.Roster {
		observation := observations[role]
		if observation.Outcome != shim.OutcomeStaleRecord {
			continue
		}
		fresh, err := e.inspector.RemoveStale(ctx, session, role, observation.ChildPID)
		if err != nil {
			return result, err
		}
		if !fresh.MayReportAbsent() {
			return result, &ShimKillRefusalError{
				Session: session, Role: role, Outcome: outcomeFromKillProcess(fresh.Observation), Cause: fresh.Err,
			}
		}
	}
	if presentationPresent {
		if err := e.presentation.RemovePresentationSession(ctx, presentationSession.ID); err != nil {
			observed, present, observationErr := e.presentation.FindPresentationSession(ctx, session)
			if observationErr != nil || present {
				retained := &ShimKillPresentationRetainedError{
					Session: session, PresentationID: presentationSession.ID,
					RemoveErr: err, ObservationErr: observationErr,
				}
				if present {
					retained.ObservedID = observed.ID
				}
				return result, retained
			}
			result.PresentationGone = true
		} else {
			result.PresentationRemoved = true
		}
	}
	if err := e.records.RemoveOwned(record); err != nil {
		return result, err
	}
	return result, nil
}

func outcomeFromKillProcess(observation shim.ProcessObservation) shim.Outcome {
	switch observation {
	case shim.ProcessAbsent:
		return shim.OutcomeStaleRecord
	case shim.ProcessPresentMatch:
		return shim.OutcomeOrphan
	case shim.ProcessPresentTokenDisagreement:
		return shim.OutcomePresentTokenDisagreement
	case shim.ProcessPresentNotOurs:
		return shim.OutcomePresentNotOurs
	default:
		return shim.OutcomeCouldNotObserve
	}
}
