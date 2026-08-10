//go:build darwin

package fleet

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/preflight"
	"github.com/mnbf9rca/agentctl/internal/ptyx"
	"github.com/mnbf9rca/agentctl/internal/shellq"
	"github.com/mnbf9rca/agentctl/internal/shim"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

// ShimFleetRecords is the durable session-level roster/configuration seam.
type ShimFleetRecords interface {
	Create(ShimFleetRecord) error
	Read(string) (ShimFleetRecord, error)
	ReplaceOwned(ShimFleetRecord, ShimFleetRecord) error
	RemoveOwned(ShimFleetRecord) error
}

// ShimPresentation is optional tmux presentation. Its IDs are cleanup facts,
// never runtime role identity or control authorization.
type ShimPresentation interface {
	CreatePresentationSession(context.Context, string, string, string, string) (tmuxx.CreatedSession, error)
	CreatePresentationWindow(context.Context, tmuxx.SessionID, string, string, string) (tmuxx.CreatedWindow, error)
	RemovePresentationWindow(context.Context, tmuxx.WindowID) error
	RemovePresentationSession(context.Context, tmuxx.SessionID) error
	FindPresentationSession(context.Context, string) (tmuxx.Session, bool, error)
}

// ShimLifecycle is the closed observe/stop client boundary. It cannot carry
// caller-provided PTY bytes.
type ShimLifecycle interface {
	Observe(context.Context, string, string) (shim.Response, error)
	Stop(context.Context, string, string) (shim.Response, error)
}

// ShimLaunchDependencies supplies only non-mutating system seams.
type ShimLaunchDependencies struct {
	LookPath   preflight.LookPathFunc
	Executable preflight.ExecutableFunc
	Getwd      func() (string, error)
	Stat       func(string) (fs.FileInfo, error)
	Now        func() time.Time
	Sleep      func(time.Duration)
}

// ShimLauncher is the explicitly named compatibility implementation used
// directly until the atomic CLI cutover.
type ShimLauncher struct {
	presentation ShimPresentation
	lifecycle    ShimLifecycle
	records      ShimFleetRecords
	lookPath     preflight.LookPathFunc
	executable   preflight.ExecutableFunc
	getwd        func() (string, error)
	stat         func(string) (fs.FileInfo, error)
	now          func() time.Time
	sleep        func(time.Duration)
}

// ShimLaunchResult reports optional presentation facts and the roster size.
type ShimLaunchResult struct {
	Session    tmuxx.Session
	Directory  string
	TotalRoles int
}

// ShimRoleStateError reports a factual non-ready outcome observed from the
// runtime plane.
type ShimRoleStateError struct {
	Session string
	Role    string
	Outcome shim.Outcome
}

func (e *ShimRoleStateError) Error() string {
	return fmt.Sprintf("role %q in session %q reported %s while launch waited for running", e.Role, e.Session, e.Outcome)
}

// ShimReadyOwnerDisagreementError means the runtime role that answered ready
// is not the shim process created by this invocation. The pane PID is used only
// as creation provenance; the response remains the runtime identity fact.
type ShimReadyOwnerDisagreementError struct {
	Session     string
	Role        string
	CreatedPID  int
	ObservedPID int
}

func (e *ShimReadyOwnerDisagreementError) Error() string {
	return fmt.Sprintf("role %q in session %q was answered by shim PID %d; this invocation created PID %d", e.Role, e.Session, e.ObservedPID, e.CreatedPID)
}

// ShimLaunchRollbackError preserves the initiating role failure and the
// separately observed child/presentation/record cleanup result.
type ShimLaunchRollbackError struct {
	Session    string
	Role       string
	Cause      error
	CleanupErr error
}

func (e *ShimLaunchRollbackError) Error() string {
	if e.CleanupErr != nil {
		return fmt.Sprintf("shim launch failed for role %q in session %q: %v; owned rollback incomplete: %v", e.Role, e.Session, e.Cause, e.CleanupErr)
	}
	return fmt.Sprintf("shim launch failed for role %q in session %q: %v; owned rollback complete", e.Role, e.Session, e.Cause)
}

func (e *ShimLaunchRollbackError) Unwrap() error { return e.Cause }

// NewShimLauncher constructs the compatibility implementation without
// changing the legacy New/Launcher production path.
func NewShimLauncher(presentation ShimPresentation, lifecycle ShimLifecycle, records ShimFleetRecords, dependencies ShimLaunchDependencies) ShimLauncher {
	if dependencies.LookPath == nil {
		dependencies.LookPath = exec.LookPath
	}
	if dependencies.Executable == nil {
		dependencies.Executable = os.Executable
	}
	if dependencies.Getwd == nil {
		dependencies.Getwd = os.Getwd
	}
	if dependencies.Stat == nil {
		dependencies.Stat = os.Stat
	}
	if dependencies.Now == nil {
		dependencies.Now = time.Now
	}
	if dependencies.Sleep == nil {
		dependencies.Sleep = time.Sleep
	}
	return ShimLauncher{
		presentation: presentation, lifecycle: lifecycle, records: records,
		lookPath: dependencies.LookPath, executable: dependencies.Executable,
		getwd: dependencies.Getwd, stat: dependencies.Stat,
		now: dependencies.Now, sleep: dependencies.Sleep,
	}
}

// Launch persists the complete roster/configuration, then starts resident
// shims in roster order and observes each ready from the runtime plane.
func (l ShimLauncher) Launch(ctx context.Context, session string, fleetConfig config.FleetConfig, directory *string) (ShimLaunchResult, error) {
	if l.presentation == nil || l.lifecycle == nil || l.records == nil {
		return ShimLaunchResult{}, errors.New("shim launcher requires presentation, lifecycle, and fleet-record dependencies")
	}
	executable, err := preflight.CheckShimExecutables(fleetConfig, l.lookPath, l.executable)
	if err != nil {
		return ShimLaunchResult{}, err
	}
	directoryName, err := l.resolveDirectory(directory)
	if err != nil {
		return ShimLaunchResult{}, err
	}
	record, err := NewShimFleetRecord(session, directoryName, fleetConfig)
	if err != nil {
		return ShimLaunchResult{}, err
	}
	if err := l.records.Create(record); err != nil {
		return ShimLaunchResult{}, err
	}

	first := fleetConfig.Roles[0]
	created, err := l.presentation.CreatePresentationSession(ctx, session, first.Name, directoryName, shimWindowCommand(executable, session, first))
	if err != nil {
		return ShimLaunchResult{}, &ShimLaunchRollbackError{
			Session: session, Role: first.Name, Cause: err,
			CleanupErr: errors.New("presentation creation returned no typed owner ID; role ownership was not proved absent and durable fleet record was retained"),
		}
	}
	readyRoles := make([]config.RoleConfig, 0, len(fleetConfig.Roles))
	if err := l.waitReady(ctx, session, first.Name, created.PanePID); err != nil {
		return ShimLaunchResult{}, l.rollback(ctx, record, created.SessionID, append(readyRoles, first), first.Name, err)
	}
	readyRoles = append(readyRoles, first)
	for _, role := range fleetConfig.Roles[1:] {
		window, err := l.presentation.CreatePresentationWindow(ctx, created.SessionID, role.Name, directoryName, shimWindowCommand(executable, session, role))
		if err != nil {
			return ShimLaunchResult{}, &ShimLaunchRollbackError{
				Session: session, Role: role.Name, Cause: err,
				CleanupErr: errors.New("presentation creation returned no typed owner ID; role ownership was not proved absent and durable fleet record was retained"),
			}
		}
		if err := l.waitReady(ctx, session, role.Name, window.PanePID); err != nil {
			return ShimLaunchResult{}, l.rollback(ctx, record, created.SessionID, append(readyRoles, role), role.Name, err)
		}
		readyRoles = append(readyRoles, role)
	}
	return ShimLaunchResult{
		Session: tmuxx.Session{ID: created.SessionID, Name: session}, Directory: directoryName, TotalRoles: len(fleetConfig.Roles),
	}, nil
}

func (l ShimLauncher) rollback(
	ctx context.Context,
	record ShimFleetRecord,
	sessionID tmuxx.SessionID,
	roles []config.RoleConfig,
	failedRole string,
	cause error,
) error {
	var cleanupErrors []error
	allChildrenAbsent := true
	for index := len(roles) - 1; index >= 0; index-- {
		role := roles[index]
		response, err := l.lifecycle.Stop(ctx, record.Session, role.Name)
		if err != nil {
			allChildrenAbsent = false
			cleanupErrors = append(cleanupErrors, fmt.Errorf("stop role %s: %w", role.Name, err))
			continue
		}
		if !shimStopObservedChildExit(response) {
			allChildrenAbsent = false
			cleanupErrors = append(cleanupErrors, fmt.Errorf("stop role %s reported %s without separate SIGHUP-attempt and observed-child-exit facts", role.Name, response.Outcome))
		}
	}
	if allChildrenAbsent {
		if err := l.presentation.RemovePresentationSession(ctx, sessionID); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		} else if err := l.records.RemoveOwned(record); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	return &ShimLaunchRollbackError{
		Session: record.Session, Role: failedRole, Cause: cause, CleanupErr: errors.Join(cleanupErrors...),
	}
}

func shimStopObservedChildExit(response shim.Response) bool {
	return response.Outcome == shim.OutcomeStopChildExited &&
		response.SignalAttempted != nil && *response.SignalAttempted &&
		response.Signal != nil && *response.Signal == "SIGHUP" &&
		response.ChildExitObserved != nil && *response.ChildExitObserved
}

func shimWindowCommand(executable, session string, role config.RoleConfig) string {
	argv := []string{
		executable, "__shim", "--session", session, "--role", role.Name, "--harness", string(role.Harness),
	}
	if role.Model != "" {
		argv = append(argv, "--model", role.Model)
	}
	if role.Effort != "" {
		argv = append(argv, "--effort", role.Effort)
	}
	return "exec " + shellq.Join(argv)
}

func (l ShimLauncher) waitReady(ctx context.Context, session, role string, createdPID int) error {
	deadline := l.now().Add(ptyx.ReadinessTimeout)
	for {
		response, err := l.lifecycle.Observe(ctx, session, role)
		if err == nil {
			switch response.Outcome {
			case shim.OutcomeRunning:
				if response.ShimPID == nil {
					return &ShimRoleStateError{Session: session, Role: role, Outcome: response.Outcome}
				}
				if *response.ShimPID != createdPID {
					return &ShimReadyOwnerDisagreementError{Session: session, Role: role, CreatedPID: createdPID, ObservedPID: *response.ShimPID}
				}
				return nil
			case shim.OutcomeStarting:
			default:
				return &ShimRoleStateError{Session: session, Role: role, Outcome: response.Outcome}
			}
		}
		if !l.now().Before(deadline) {
			if err != nil {
				return err
			}
			return &ShimRoleStateError{Session: session, Role: role, Outcome: shim.OutcomeStarting}
		}
		l.sleep(ptyx.ReadinessPollInterval)
	}
}

func (l ShimLauncher) resolveDirectory(directory *string) (string, error) {
	path := ""
	if directory == nil {
		cwd, err := l.getwd()
		if err != nil {
			return "", &DirectoryError{Err: err}
		}
		path = cwd
	} else {
		path = *directory
		if !filepath.IsAbs(path) {
			cwd, err := l.getwd()
			if err != nil {
				return "", &DirectoryError{Path: path, Err: err}
			}
			path = filepath.Join(cwd, path)
		}
	}
	info, err := l.stat(path)
	if err != nil {
		return "", &DirectoryError{Path: path, Err: err}
	}
	if !info.IsDir() {
		return "", &DirectoryError{Path: path}
	}
	return path, nil
}
