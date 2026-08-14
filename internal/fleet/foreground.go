//go:build darwin

package fleet

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/preflight"
	"github.com/mnbf9rca/agentctl/internal/ptyx"
	"github.com/mnbf9rca/agentctl/internal/shim"
)

// ShimRoleServer is the same resident lifecycle used by tmux-backed launch.
type ShimRoleServer interface {
	Run(context.Context, shim.RunRequest) error
}

type shimFleetExtender interface {
	ExtendOwned(ShimFleetRecord, ShimFleetRecord) error
}

// ShimForegroundRequest contains one validated role and its direct terminal
// stream request. Directory is the caller's observed current working directory.
type ShimForegroundRequest struct {
	Session       string
	Role          config.RoleConfig
	Directory     string
	ServerRequest shim.RunRequest
}

// ShimForegroundDirectoryMismatchError preserves both sides of the
// session-wide working-directory disagreement required by R22.
type ShimForegroundDirectoryMismatchError struct {
	Session string
	Role    string
	Stored  string
	Current string
}

// ShimForegroundRollbackError keeps the role-lifecycle failure distinct from
// a failed removal of the newly created session fleet record. Callers must not
// report complete cleanup when FleetCleanupErr is non-nil.
type ShimForegroundRollbackError struct {
	Session         string
	Role            string
	Cause           error
	FleetCleanupErr error
}

func (e *ShimForegroundRollbackError) Error() string {
	return fmt.Sprintf("foreground role %q in session %q failed: %v; durable fleet cleanup failed: %v", e.Role, e.Session, e.Cause, e.FleetCleanupErr)
}

func (e *ShimForegroundRollbackError) Unwrap() []error {
	return []error{e.Cause, e.FleetCleanupErr}
}

func (e *ShimForegroundDirectoryMismatchError) Error() string {
	return fmt.Sprintf("durable fleet directory %q differs from current working directory %q", e.Stored, e.Current)
}

// ShimForegroundRunner composes a no-tmux role with the same durable fleet,
// runtime claim, readiness, and child lifecycle used by tmux launch.
type ShimForegroundRunner struct {
	server    ShimRoleServer
	lifecycle ShimLifecycle
	records   ShimFleetRecords
	inspector ShimRoleInspector
	launcher  ShimLauncher
}

func NewShimForegroundRunner(server ShimRoleServer, lifecycle ShimLifecycle, records ShimFleetRecords, inspector ShimRoleInspector, dependencies ShimLaunchDependencies) ShimForegroundRunner {
	return ShimForegroundRunner{
		server: server, lifecycle: lifecycle, records: records, inspector: inspector,
		launcher: NewShimLauncher(nil, lifecycle, records, dependencies),
	}
}

// Run creates a one-role fleet record or joins the role to the existing roster.
// Existing records are changed only after the newly owned shim answers ready.
func (r ShimForegroundRunner) Run(ctx context.Context, request ShimForegroundRequest) error {
	if r.server == nil || r.lifecycle == nil || r.records == nil || r.inspector == nil {
		return errors.New("foreground shim runner requires server, lifecycle, records, and inspector")
	}
	if _, err := preflight.CheckShimExecutables(config.FleetConfig{Roles: []config.RoleConfig{request.Role}}, r.launcher.lookPath, r.launcher.executable); err != nil {
		return err
	}

	expected, created, roleExists, err := r.prepareFleetRecord(request)
	if err != nil {
		return err
	}
	if !created {
		observation, inspectErr := r.inspector.Inspect(ctx, request.Session, request.Role.Name)
		if inspectErr != nil {
			return inspectErr
		}
		switch observation.Outcome {
		case shim.OutcomeMissing:
		case shim.OutcomeStaleRecord:
			fresh, removeErr := r.inspector.RemoveStale(ctx, request.Session, request.Role.Name, observation.ChildPID)
			if removeErr != nil {
				return removeErr
			}
			if !fresh.MayAuthorizeRelaunch() {
				return &ShimRelaunchRefusalError{
					Session: request.Session, Role: request.Role.Name, Outcome: outcomeFromProcessObservation(fresh.Observation), Cause: fresh.Err,
					Observation: ShimRoleObservation{Outcome: outcomeFromProcessObservation(fresh.Observation), ChildPID: observation.ChildPID, Cause: fresh.Err},
				}
			}
		default:
			return &ShimRelaunchRefusalError{Session: request.Session, Role: request.Role.Name, Outcome: observation.Outcome, Cause: observation.Cause, Observation: observation}
		}
	}

	serverDone := make(chan error, 1)
	go func() { serverDone <- r.server.Run(ctx, request.ServerRequest) }()
	if err := r.waitForegroundReady(ctx, request.Session, request.Role.Name, serverDone); err != nil {
		if created {
			if cleanupErr := r.records.RemoveOwned(expected); cleanupErr != nil {
				return &ShimForegroundRollbackError{
					Session: request.Session, Role: request.Role.Name, Cause: err, FleetCleanupErr: cleanupErr,
				}
			}
		}
		return err
	}

	if !created {
		replacement := foregroundReplacement(expected, request.Role, roleExists)
		if roleExists {
			err = r.records.ReplaceOwned(expected, replacement)
		} else {
			extender, ok := r.records.(shimFleetExtender)
			if !ok {
				err = errors.New("durable fleet records do not support roster extension")
			} else {
				err = extender.ExtendOwned(expected, replacement)
			}
		}
		if err != nil {
			var uncertain *shim.RecordCommitUncertainError
			if errors.As(err, &uncertain) {
				return errors.Join(err, <-serverDone)
			}
			stopResponse, stopErr := r.lifecycle.Stop(ctx, request.Session, request.Role.Name)
			if stopErr == nil && !shimStopObservedChildExit(stopResponse) {
				stopErr = fmt.Errorf("foreground cleanup reported %s without observed child exit", stopResponse.Outcome)
			}
			serverErr := <-serverDone
			return errors.Join(err, stopErr, serverErr)
		}
	}
	return <-serverDone
}

func (r ShimForegroundRunner) prepareFleetRecord(request ShimForegroundRequest) (ShimFleetRecord, bool, bool, error) {
	record, err := r.records.Read(request.Session)
	if err == nil {
		if record.Directory != request.Directory {
			return ShimFleetRecord{}, false, false, &ShimForegroundDirectoryMismatchError{
				Session: request.Session, Role: request.Role.Name, Stored: record.Directory, Current: request.Directory,
			}
		}
		_, exists := record.Roles[request.Role.Name]
		return record, false, exists, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return ShimFleetRecord{}, false, false, err
	}
	record, err = NewShimFleetRecord(request.Session, request.Directory, PresentationDetached, config.FleetConfig{Roles: []config.RoleConfig{request.Role}})
	if err != nil {
		return ShimFleetRecord{}, false, false, err
	}
	if err := r.records.Create(record); err != nil {
		var exists *ShimFleetExistsError
		if errors.As(err, &exists) {
			return r.prepareFleetRecord(request)
		}
		return ShimFleetRecord{}, false, false, err
	}
	return record, true, false, nil
}

func (r ShimForegroundRunner) waitForegroundReady(ctx context.Context, session, role string, serverDone <-chan error) error {
	for {
		select {
		case err := <-serverDone:
			if err == nil {
				return errors.New("foreground child exited before its shim was observed ready")
			}
			return err
		default:
		}
		response, err := r.lifecycle.Observe(ctx, session, role)
		if err == nil && response.Outcome == shim.OutcomeRunning {
			return nil
		}
		if err == nil && response.Outcome != shim.OutcomeStarting {
			return &ShimRoleStateError{Session: session, Role: role, Outcome: response.Outcome}
		}
		r.launcher.sleep(ptyx.ReadinessPollInterval)
	}
}

func foregroundReplacement(expected ShimFleetRecord, role config.RoleConfig, exists bool) ShimFleetRecord {
	replacement := expected
	replacement.Roster = append([]string(nil), expected.Roster...)
	replacement.Roles = make(map[string]ShimFleetRoleRecord, len(expected.Roles)+1)
	for name, configured := range expected.Roles {
		replacement.Roles[name] = configured
	}
	if !exists {
		replacement.Roster = append(replacement.Roster, role.Name)
	}
	replacement.Roles[role.Name] = ShimFleetRoleRecord{Harness: string(role.Harness), Model: role.Model, Effort: role.Effort}
	return replacement
}
