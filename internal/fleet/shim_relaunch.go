//go:build darwin

package fleet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/preflight"
	"github.com/mnbf9rca/agentctl/internal/shim"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

// ShimRoleObservation is one closed runtime/durable state decision used by
// relaunch. Outcome is authoritative; the remaining fields preserve the facts
// needed for a fresh absence check or diagnostics.
type ShimRoleObservation struct {
	Outcome       shim.Outcome
	State         string
	ShimPID       int
	ChildPID      int
	RecordPath    string
	LocalRoot     string
	RecordedRoot  string
	AnswererPID   int
	CallerPID     int
	TargetPID     int
	Cleanup       string
	RecordedToken *shim.StartToken
	ObservedToken *shim.StartToken
	Cause         error
}

// ShimRoleInspector performs the non-mutating state checks and the separate
// ESRCH-only stale-record removal gate.
type ShimRoleInspector interface {
	Inspect(context.Context, string, string) (ShimRoleObservation, error)
	RemoveStale(context.Context, string, string, int) (shim.ProcessResult, error)
}

// RuntimeShimRoleInspector composes the landed namespace, advisory, client,
// durable-record, and process-oracle primitives without tmux identity.
type RuntimeShimRoleInspector struct {
	namespace      *shim.Namespace
	lifecycle      ShimLifecycle
	observeProcess func(int, shim.StartToken) shim.ProcessResult
}

// NewRuntimeShimRoleInspector constructs the production inspection boundary.
func NewRuntimeShimRoleInspector(namespace *shim.Namespace, lifecycle ShimLifecycle) *RuntimeShimRoleInspector {
	return &RuntimeShimRoleInspector{namespace: namespace, lifecycle: lifecycle, observeProcess: shim.ObserveProcess}
}

// Inspect is read-only. A missing runtime-session directory is indeterminate,
// because it cannot prove the separately durable role record is absent.
func (i *RuntimeShimRoleInspector) Inspect(ctx context.Context, session, role string) (ShimRoleObservation, error) {
	if i == nil || i.namespace == nil || i.lifecycle == nil {
		return ShimRoleObservation{}, errors.New("runtime shim role inspector requires namespace and lifecycle")
	}
	path, err := i.namespace.ExistingRolePath(session, role)
	if err != nil {
		return ShimRoleObservation{Outcome: shim.OutcomeCouldNotObserve, Cause: err}, nil
	}
	defer func() { _ = path.Close() }()
	advisory, err := shim.ReadAdvisory(path)
	if err != nil {
		_, recordErr := shim.ReadRecord(path)
		if errors.Is(err, os.ErrNotExist) && errors.Is(recordErr, os.ErrNotExist) {
			return ShimRoleObservation{Outcome: shim.OutcomeMissing}, nil
		}
		return ShimRoleObservation{Outcome: shim.OutcomeInvalidRecord, RecordPath: path.Record, Cause: err}, nil
	}
	if err := advisory.CompareStateRoot(i.namespace.StateRoot); err != nil {
		return ShimRoleObservation{Outcome: shim.OutcomeStateRootDisagreement, ShimPID: advisory.ShimPID, Cause: err}, nil
	}
	record, err := shim.ReadRecord(path)
	if errors.Is(err, os.ErrNotExist) {
		return ShimRoleObservation{Outcome: shim.OutcomeInvalidRecord, ShimPID: advisory.ShimPID, RecordPath: path.Record, Cause: err}, nil
	}
	if err != nil {
		return ShimRoleObservation{Outcome: shim.OutcomeInvalidRecord, ShimPID: advisory.ShimPID, RecordPath: path.Record, Cause: err}, nil
	}
	response, clientErr := i.lifecycle.Observe(ctx, session, role)
	if clientErr == nil {
		return observationFromShimResponse(response, record), nil
	}
	var stateRoot *shim.StateRootDisagreementError
	if errors.As(clientErr, &stateRoot) {
		return ShimRoleObservation{Outcome: shim.OutcomeStateRootDisagreement, ShimPID: advisory.ShimPID, Cause: clientErr}, nil
	}
	var answerer *shim.AnswererDisagreementError
	if errors.As(clientErr, &answerer) {
		return ShimRoleObservation{Outcome: shim.OutcomeAnswererDisagreement, ShimPID: advisory.ShimPID, Cause: clientErr}, nil
	}
	var skew *shim.ProtocolSkewError
	if errors.As(clientErr, &skew) {
		return ShimRoleObservation{Outcome: shim.OutcomeProtocolSkew, ShimPID: advisory.ShimPID, Cause: clientErr}, nil
	}
	var networkError *net.OpError
	if !errors.As(clientErr, &networkError) || networkError.Op != "dial" {
		return ShimRoleObservation{Outcome: shim.OutcomeCouldNotObserve, ShimPID: advisory.ShimPID, Cause: clientErr}, nil
	}
	if record.State == shim.RecordStateChildStarting {
		return ShimRoleObservation{
			Outcome: shim.OutcomeIndeterminateChildStarting, ShimPID: advisory.ShimPID, RecordPath: path.Record, Cause: clientErr,
		}, nil
	}
	result := i.observeProcess(record.ChildPID, *record.ChildStartToken)
	return observationFromProcessResult(advisory.ShimPID, record, result), nil
}

func observationFromShimResponse(response shim.Response, record shim.Record) ShimRoleObservation {
	observation := ShimRoleObservation{
		Outcome: response.Outcome, ChildPID: record.ChildPID, RecordedToken: record.ChildStartToken,
	}
	if response.State != nil {
		observation.State = *response.State
	}
	if response.ShimPID != nil {
		observation.ShimPID = *response.ShimPID
	}
	if response.ChildPID != nil {
		observation.ChildPID = *response.ChildPID
	}
	if response.LocalRoot != nil {
		observation.LocalRoot = *response.LocalRoot
	}
	if response.RecordedRoot != nil {
		observation.RecordedRoot = *response.RecordedRoot
	}
	if response.TargetPID != nil {
		observation.AnswererPID = *response.TargetPID
		observation.TargetPID = *response.TargetPID
	}
	if response.CallerPID != nil {
		observation.CallerPID = *response.CallerPID
	}
	if response.Cleanup != nil {
		observation.Cleanup = *response.Cleanup
	}
	observation.ObservedToken = response.ObservedToken
	if response.Cause != nil {
		observation.Cause = errors.New(*response.Cause)
	}
	return observation
}

func observationFromProcessResult(shimPID int, record shim.Record, result shim.ProcessResult) ShimRoleObservation {
	observation := ShimRoleObservation{
		ShimPID: shimPID, ChildPID: record.ChildPID, RecordedToken: record.ChildStartToken,
		ObservedToken: result.ObservedToken, Cause: result.Err,
	}
	switch result.Observation {
	case shim.ProcessAbsent:
		observation.Outcome = shim.OutcomeStaleRecord
	case shim.ProcessPresentMatch:
		observation.Outcome = shim.OutcomeOrphan
	case shim.ProcessPresentTokenDisagreement:
		observation.Outcome = shim.OutcomePresentTokenDisagreement
	case shim.ProcessPresentNotOurs:
		observation.Outcome = shim.OutcomePresentNotOurs
	default:
		observation.Outcome = shim.OutcomeCouldNotObserve
	}
	return observation
}

// RemoveStale repeats the sole absence oracle immediately before mutation.
// Only ESRCH permits durable role-record removal.
func (i *RuntimeShimRoleInspector) RemoveStale(_ context.Context, session, role string, expectedPID int) (shim.ProcessResult, error) {
	path, err := i.namespace.ExistingRolePath(session, role)
	if err != nil {
		return shim.ProcessResult{Observation: shim.ProcessCouldNotObserve, Err: err}, nil
	}
	defer func() { _ = path.Close() }()
	record, err := shim.ReadRecord(path)
	if err != nil {
		return shim.ProcessResult{Observation: shim.ProcessCouldNotObserve, Err: err}, nil
	}
	if record.State != shim.RecordStateChildRecorded || record.ChildPID != expectedPID || record.ChildStartToken == nil {
		cause := errors.New("durable child identity changed before stale-record removal")
		return shim.ProcessResult{Observation: shim.ProcessCouldNotObserve, Err: cause}, nil
	}
	result := i.observeProcess(record.ChildPID, *record.ChildStartToken)
	if !result.MayAuthorizeRelaunch() {
		return result, nil
	}
	if err := shim.RemoveRecord(path); err != nil {
		return result, err
	}
	return result, nil
}

// ShimRelaunchRefusalError preserves the exact state that refused mutation.
type ShimRelaunchRefusalError struct {
	Session     string
	Role        string
	Outcome     shim.Outcome
	Cause       error
	Observation ShimRoleObservation
}

func (e *ShimRelaunchRefusalError) Error() string {
	return fmt.Sprintf("refusing shim relaunch of role %q in session %q: %s", e.Role, e.Session, e.Outcome)
}

func (e *ShimRelaunchRefusalError) Unwrap() error { return e.Cause }

// ShimRelaunchResult reports the created presentation only as optional UI
// facts; readiness came from the shim runtime response.
type ShimRelaunchResult struct {
	Session               string
	Role                  string
	Directory             string
	WindowID              tmuxx.WindowID
	PaneID                tmuxx.PaneID
	PresentationSessionID tmuxx.SessionID
}

// ShimRelaunchRollbackError preserves a definite post-readiness fleet-record
// update failure and the separately observed cleanup of the newly owned role.
type ShimRelaunchRollbackError struct {
	Session    string
	Role       string
	Cause      error
	CleanupErr error
}

func (e *ShimRelaunchRollbackError) Error() string {
	if e.CleanupErr != nil {
		return fmt.Sprintf("shim relaunch of role %q in session %q could not commit its fleet override: %v; owned cleanup incomplete: %v", e.Role, e.Session, e.Cause, e.CleanupErr)
	}
	return fmt.Sprintf("shim relaunch of role %q in session %q could not commit its fleet override: %v; owned cleanup complete", e.Role, e.Session, e.Cause)
}

func (e *ShimRelaunchRollbackError) Unwrap() error { return e.Cause }

// ShimRelauncher is the runtime-backed single-role relaunch implementation.
type ShimRelauncher struct {
	launcher  ShimLauncher
	inspector ShimRoleInspector
}

// NewShimRelauncher constructs the runtime-backed relaunch implementation.
func NewShimRelauncher(
	presentation ShimPresentation,
	lifecycle ShimLifecycle,
	records ShimFleetRecords,
	inspector ShimRoleInspector,
	dependencies ShimLaunchDependencies,
) ShimRelauncher {
	return ShimRelauncher{
		launcher:  NewShimLauncher(presentation, lifecycle, records, dependencies),
		inspector: inspector,
	}
}

// Relaunch refuses every state except durable-record absence or a stale record
// whose child receives a fresh ESRCH observation immediately before removal.
func (r ShimRelauncher) Relaunch(ctx context.Context, session string, request RelaunchRequest) (ShimRelaunchResult, error) {
	record, err := r.launcher.records.Read(session)
	if err != nil {
		return ShimRelaunchResult{}, fleetMissing(session, err)
	}
	stored, ok := record.Roles[request.Role]
	if !ok {
		return ShimRelaunchResult{}, &UnknownRoleError{Role: request.Role, Roster: joinShimRoster(record.Roster)}
	}
	observation, err := r.inspector.Inspect(ctx, session, request.Role)
	if err != nil {
		return ShimRelaunchResult{}, err
	}
	if observation.Outcome != shim.OutcomeMissing && observation.Outcome != shim.OutcomeStaleRecord {
		return ShimRelaunchResult{}, &ShimRelaunchRefusalError{
			Session: session, Role: request.Role, Outcome: observation.Outcome, Cause: observation.Cause, Observation: observation,
		}
	}
	role, directory, err := resolveShimRelaunchConfig(record, request, stored)
	if err != nil {
		return ShimRelaunchResult{}, err
	}
	executable, err := preflight.CheckShimExecutables(
		config.FleetConfig{Roles: []config.RoleConfig{role}}, r.launcher.lookPath, r.launcher.executable,
	)
	if err != nil {
		return ShimRelaunchResult{}, err
	}
	directoryInfo, err := r.launcher.stat(directory)
	if err != nil || directoryInfo == nil || !directoryInfo.IsDir() {
		return ShimRelaunchResult{}, &StoredDirectoryError{Role: request.Role, Path: directory, Err: err}
	}
	if observation.Outcome == shim.OutcomeStaleRecord {
		fresh, err := r.inspector.RemoveStale(ctx, session, request.Role, observation.ChildPID)
		if err != nil {
			return ShimRelaunchResult{}, err
		}
		if !fresh.MayAuthorizeRelaunch() {
			outcome := outcomeFromProcessObservation(fresh.Observation)
			return ShimRelaunchResult{}, &ShimRelaunchRefusalError{
				Session: session, Role: request.Role, Outcome: outcome, Cause: fresh.Err,
				Observation: ShimRoleObservation{Outcome: outcome, ChildPID: observation.ChildPID, Cause: fresh.Err},
			}
		}
	}
	presentationSession, present, err := r.launcher.presentation.FindPresentationSession(ctx, session)
	if err != nil {
		return ShimRelaunchResult{}, err
	}
	command := shimWindowCommand(executable, session, role)
	var windowID tmuxx.WindowID
	var paneID tmuxx.PaneID
	var panePID int
	createdSessionID := presentationSession.ID
	if present {
		created, err := r.launcher.presentation.CreatePresentationWindow(ctx, presentationSession.ID, role.Name, directory, command)
		if err != nil {
			return ShimRelaunchResult{}, err
		}
		windowID, paneID, panePID = created.WindowID, created.PaneID, created.PanePID
	} else {
		created, err := r.launcher.presentation.CreatePresentationSession(ctx, session, role.Name, directory, command)
		if err != nil {
			return ShimRelaunchResult{}, err
		}
		createdSessionID = created.SessionID
		windowID, paneID, panePID = created.WindowID, created.PaneID, created.PanePID
	}
	if err := r.launcher.waitReady(ctx, session, role.Name, panePID); err != nil {
		var disagreement *ShimReadyOwnerDisagreementError
		if errors.As(err, &disagreement) {
			var cleanupErr error
			if present {
				cleanupErr = r.launcher.presentation.RemovePresentationWindow(ctx, windowID)
			} else {
				cleanupErr = r.launcher.presentation.RemovePresentationSession(ctx, createdSessionID)
			}
			return ShimRelaunchResult{}, &ShimRelaunchRefusalError{
				Session: session, Role: role.Name, Outcome: shim.OutcomeConcurrentContender,
				Cause: errors.Join(err, cleanupErr), Observation: ShimRoleObservation{
					Outcome: shim.OutcomeConcurrentContender, ShimPID: disagreement.ObservedPID, Cause: errors.Join(err, cleanupErr),
				},
			}
		}
		return ShimRelaunchResult{}, err
	}
	if shimRelaunchHasOverride(request) {
		replacement := shimFleetRecordWithRelaunchOverride(record, role, directory)
		if err := r.launcher.records.ReplaceOwned(record, replacement); err != nil {
			var uncertain *shim.RecordCommitUncertainError
			if errors.As(err, &uncertain) {
				return ShimRelaunchResult{}, err
			}
			cleanupErr := r.rollbackOwnedRole(ctx, session, role.Name, present, windowID, createdSessionID)
			return ShimRelaunchResult{}, &ShimRelaunchRollbackError{
				Session: session, Role: role.Name, Cause: err, CleanupErr: cleanupErr,
			}
		}
	}
	return ShimRelaunchResult{
		Session: session, Role: role.Name, Directory: directory,
		WindowID: windowID, PaneID: paneID, PresentationSessionID: createdSessionID,
	}, nil
}

func shimRelaunchHasOverride(request RelaunchRequest) bool {
	return request.Harness != nil || request.Model != nil || request.Effort != nil || request.Directory != nil
}

func shimFleetRecordWithRelaunchOverride(record ShimFleetRecord, role config.RoleConfig, directory string) ShimFleetRecord {
	replacement := record
	replacement.Roster = append([]string(nil), record.Roster...)
	replacement.Roles = make(map[string]ShimFleetRoleRecord, len(record.Roles))
	for name, stored := range record.Roles {
		replacement.Roles[name] = stored
	}
	replacement.Directory = directory
	replacement.Roles[role.Name] = ShimFleetRoleRecord{
		Harness: string(role.Harness), Model: role.Model, Effort: role.Effort,
	}
	return replacement
}

func (r ShimRelauncher) rollbackOwnedRole(
	ctx context.Context,
	session string,
	role string,
	presentationExisted bool,
	windowID tmuxx.WindowID,
	sessionID tmuxx.SessionID,
) error {
	response, err := r.launcher.lifecycle.Stop(ctx, session, role)
	if err != nil {
		return err
	}
	if !shimStopObservedChildExit(response) {
		return fmt.Errorf("stop reported %s without separate SIGHUP-attempt and observed-child-exit facts", response.Outcome)
	}
	if presentationExisted {
		return r.launcher.presentation.RemovePresentationWindow(ctx, windowID)
	}
	return r.launcher.presentation.RemovePresentationSession(ctx, sessionID)
}

func resolveShimRelaunchConfig(record ShimFleetRecord, request RelaunchRequest, stored ShimFleetRoleRecord) (config.RoleConfig, string, error) {
	role := config.RoleConfig{Name: request.Role, Harness: config.Harness(stored.Harness), Model: stored.Model, Effort: stored.Effort}
	if request.Harness != nil {
		harness, err := config.ParseHarness(*request.Harness)
		if err != nil {
			return config.RoleConfig{}, "", err
		}
		role.Harness = harness
	}
	if request.Model != nil {
		if err := config.ValidateModelName(*request.Model); err != nil {
			return config.RoleConfig{}, "", err
		}
		role.Model = *request.Model
	}
	if request.Effort != nil {
		if err := config.ValidateEffort(*request.Effort); err != nil {
			return config.RoleConfig{}, "", err
		}
		role.Effort = *request.Effort
	}
	directory := record.Directory
	if request.Directory != nil {
		directory = *request.Directory
	}
	return role, directory, nil
}

func outcomeFromProcessObservation(observation shim.ProcessObservation) shim.Outcome {
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
