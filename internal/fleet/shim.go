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
	"strings"
	"time"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/preflight"
	"github.com/mnbf9rca/agentctl/internal/ptyx"
	"github.com/mnbf9rca/agentctl/internal/shellq"
	"github.com/mnbf9rca/agentctl/internal/shim"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

// The outer launcher starts observing before the shim begins its own bounded
// readiness poll. Give that inner 5s contract a complete window rather than
// racing its inclusive final observation with an outer deadline of the same
// duration.
const shimLaunchObservationTimeout = 2 * ptyx.ReadinessTimeout

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
	LookPath        preflight.LookPathFunc
	Executable      preflight.ExecutableFunc
	Getwd           func() (string, error)
	Stat            func(string) (fs.FileInfo, error)
	Now             func() time.Time
	Sleep           func(time.Duration)
	Environment     func() []string
	OpenDevNull     func() (*os.File, error)
	DetachedStarter DetachedShimStarter
}

// ShimLauncher is the runtime-backed fleet launcher used by the public CLI.
type ShimLauncher struct {
	presentation    ShimPresentation
	lifecycle       ShimLifecycle
	records         ShimFleetRecords
	lookPath        preflight.LookPathFunc
	executable      preflight.ExecutableFunc
	getwd           func() (string, error)
	stat            func(string) (fs.FileInfo, error)
	now             func() time.Time
	sleep           func(time.Duration)
	environment     func() []string
	openDevNull     func() (*os.File, error)
	detachedStarter DetachedShimStarter
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

// ShimDetachedStartUncertainError records that this invocation started a
// detached shim but observed neither readiness nor its exit before deadline.
// The durable record remains because absence was not observed.
type ShimDetachedStartUncertainError struct {
	Session    string
	Role       string
	CreatedPID int
	Cause      error
}

func (e *ShimDetachedStartUncertainError) Error() string {
	return fmt.Sprintf("detached shim for role %q in session %q was neither ready nor observed exited before readiness deadline", e.Role, e.Session)
}

func (e *ShimDetachedStartUncertainError) Unwrap() error { return e.Cause }

// ShimDetachedStartRetainedError records a started detached role that never
// proved responder ownership. Its fleet record is retained rather than risking
// a role-addressed cleanup of an unowned peer.
type ShimDetachedStartRetainedError struct {
	Session    string
	Role       string
	CreatedPID int
	Cause      error
	Remaining  string
	CleanupErr error
}

func (e *ShimDetachedStartRetainedError) Error() string {
	return fmt.Sprintf("detached shim PID %d for role %q in session %q was retained after readiness failed before ownership agreement", e.CreatedPID, e.Role, e.Session)
}

func (e *ShimDetachedStartRetainedError) Unwrap() []error { return []error{e.Cause, e.CleanupErr} }

// ShimDetachedStartFailedError means the typed starter returned no process.
type ShimDetachedStartFailedError struct {
	Session string
	Role    string
	Cause   error
}

func (e *ShimDetachedStartFailedError) Error() string {
	return fmt.Sprintf("detached shim for role %q in session %q did not start", e.Role, e.Session)
}

func (e *ShimDetachedStartFailedError) Unwrap() error { return e.Cause }

// ShimDetachedStartRolledBackError records a started detached process whose
// owned cleanup completed after a pre-readiness failure.
type ShimDetachedStartRolledBackError struct {
	Session    string
	Role       string
	CreatedPID int
	Cause      error
}

func (e *ShimDetachedStartRolledBackError) Error() string {
	return fmt.Sprintf("detached shim PID %d for role %q in session %q failed before readiness and owned cleanup completed", e.CreatedPID, e.Role, e.Session)
}

func (e *ShimDetachedStartRolledBackError) Unwrap() error { return e.Cause }

type detachedShimExitedError struct {
	pid   int
	cause error
}

func (e *detachedShimExitedError) Error() string {
	return fmt.Sprintf("detached shim PID %d exited before readiness", e.pid)
}

func (e *detachedShimExitedError) Unwrap() error { return e.cause }

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

// NewShimLauncher constructs the runtime-backed fleet launcher.
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
	if dependencies.Environment == nil {
		dependencies.Environment = os.Environ
	}
	if dependencies.OpenDevNull == nil {
		dependencies.OpenDevNull = func() (*os.File, error) { return os.OpenFile("/dev/null", os.O_RDWR, 0) }
	}
	if dependencies.DetachedStarter == nil {
		dependencies.DetachedStarter = ExecDetachedShimStarter{}
	}
	return ShimLauncher{
		presentation: presentation, lifecycle: lifecycle, records: records,
		lookPath: dependencies.LookPath, executable: dependencies.Executable,
		getwd: dependencies.Getwd, stat: dependencies.Stat,
		now: dependencies.Now, sleep: dependencies.Sleep,
		environment: dependencies.Environment, openDevNull: dependencies.OpenDevNull,
		detachedStarter: dependencies.DetachedStarter,
	}
}

// Launch persists the complete roster/configuration, then starts resident
// shims in roster order and observes each ready from the runtime plane.
func (l ShimLauncher) Launch(ctx context.Context, session string, fleetConfig config.FleetConfig, presentation Presentation, directory *string) (ShimLaunchResult, error) {
	if l.lifecycle == nil || l.records == nil {
		return ShimLaunchResult{}, errors.New("shim launcher requires lifecycle and fleet-record dependencies")
	}
	if presentation != PresentationTmux && presentation != PresentationDetached {
		return ShimLaunchResult{}, fmt.Errorf("unknown fleet presentation %q", presentation)
	}
	if presentation == PresentationTmux && l.presentation == nil {
		return ShimLaunchResult{}, errors.New("shim launcher requires presentation for tmux fleet")
	}
	executable, err := preflight.CheckShimExecutables(fleetConfig, presentation == PresentationTmux, l.lookPath, l.executable)
	if err != nil {
		return ShimLaunchResult{}, err
	}
	directoryName, err := l.resolveDirectory(directory)
	if err != nil {
		return ShimLaunchResult{}, err
	}
	record, err := NewShimFleetRecord(session, directoryName, presentation, fleetConfig)
	if err != nil {
		return ShimLaunchResult{}, err
	}
	if err := l.records.Create(record); err != nil {
		return ShimLaunchResult{}, err
	}

	if presentation == PresentationDetached {
		return l.launchDetached(ctx, executable, record)
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

func (l ShimLauncher) launchDetached(ctx context.Context, executable string, record ShimFleetRecord) (ShimLaunchResult, error) {
	readyRoles := make([]config.RoleConfig, 0, len(record.Roster))
	for _, roleName := range record.Roster {
		role := config.RoleConfig{Name: roleName, Harness: config.Harness(record.Roles[roleName].Harness), Model: record.Roles[roleName].Model, Effort: record.Roles[roleName].Effort}
		process, err := l.startDetached(executable, record.Session, record.Directory, role)
		if err != nil {
			cleanupErr := l.rollbackDetached(ctx, record, readyRoles, true)
			if cleanupErr != nil {
				return ShimLaunchResult{}, &ShimLaunchRollbackError{Session: record.Session, Role: role.Name, Cause: err, CleanupErr: cleanupErr}
			}
			return ShimLaunchResult{}, &ShimDetachedStartFailedError{Session: record.Session, Role: role.Name, Cause: err}
		}
		if err := l.waitDetachedReady(ctx, record.Session, role.Name, process); err != nil {
			var exited *detachedShimExitedError
			if errors.As(err, &exited) && l.detachedRoleAbsent(ctx, record.Session, role.Name) {
				cleanupErr := l.rollbackDetached(ctx, record, readyRoles, true)
				if cleanupErr == nil {
					return ShimLaunchResult{}, &ShimDetachedStartRolledBackError{Session: record.Session, Role: role.Name, CreatedPID: process.PID(), Cause: err}
				}
				return ShimLaunchResult{}, &ShimDetachedStartRetainedError{Session: record.Session, Role: role.Name, CreatedPID: process.PID(), Cause: err, Remaining: "durable fleet record", CleanupErr: cleanupErr}
			}
			// This role never established that the responder is our direct child.
			// A role-addressed stop could therefore kill a peer. Stop only earlier
			// ready roles and retain the record for the failed role's evidence.
			cleanupErr := errors.Join(l.rollbackDetached(ctx, record, readyRoles, false), errors.New("ownership agreement was not observed"))
			return ShimLaunchResult{}, &ShimDetachedStartRetainedError{Session: record.Session, Role: role.Name, CreatedPID: process.PID(), Cause: err, Remaining: "durable fleet record", CleanupErr: cleanupErr}
		}
		readyRoles = append(readyRoles, role)
	}
	return ShimLaunchResult{Directory: record.Directory, TotalRoles: len(record.Roster)}, nil
}

func (l ShimLauncher) detachedRoleAbsent(ctx context.Context, session, role string) bool {
	response, err := l.lifecycle.Observe(ctx, session, role)
	return err == nil && response.Outcome == shim.OutcomeMissing
}

func (l ShimLauncher) startDetached(executable, session, directory string, role config.RoleConfig) (DetachedShimProcess, error) {
	stdin, err := l.openDevNull()
	if err != nil {
		return nil, err
	}
	defer func() { _ = stdin.Close() }()
	stdout, err := l.openDevNull()
	if err != nil {
		return nil, err
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := l.openDevNull()
	if err != nil {
		return nil, err
	}
	defer func() { _ = stderr.Close() }()
	return l.detachedStarter.Start(DetachedShimRequest{
		Executable: executable, Argv: shimArgv(executable, session, role), Directory: directory,
		Environment: detachedShimEnvironment(l.environment(), session, role.Name), Stdin: stdin, Stdout: stdout, Stderr: stderr,
	})
}

func detachedShimEnvironment(inherited []string, session, role string) []string {
	replaced := map[string]bool{"AGENTCTL_SESSION": true, "AGENTCTL_ROLE": true, "AGENTCTL_MANAGED": true}
	environment := make([]string, 0, len(inherited)+3)
	for _, entry := range inherited {
		name, _, found := strings.Cut(entry, "=")
		if found && replaced[name] {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, "AGENTCTL_SESSION="+session, "AGENTCTL_ROLE="+role, "AGENTCTL_MANAGED=1")
}

func (l ShimLauncher) waitDetachedReady(ctx context.Context, session, role string, process DetachedShimProcess) error {
	deadline := l.now().Add(shimLaunchObservationTimeout)
	waiter := process.Wait()
	for {
		select {
		case <-ctx.Done():
			return &ShimDetachedStartUncertainError{Session: session, Role: role, CreatedPID: process.PID(), Cause: ctx.Err()}
		case exit := <-waiter:
			return detachedExitError(process.PID(), exit)
		default:
		}
		response, err := l.lifecycle.Observe(ctx, session, role)
		// Observe can block while the direct child exits. Its waiter is the
		// authoritative fact, so sample it again before accepting any response.
		select {
		case <-ctx.Done():
			return &ShimDetachedStartUncertainError{Session: session, Role: role, CreatedPID: process.PID(), Cause: ctx.Err()}
		case exit := <-waiter:
			return detachedExitError(process.PID(), exit)
		default:
		}
		if !l.now().Before(deadline) {
			return &ShimDetachedStartUncertainError{Session: session, Role: role, CreatedPID: process.PID(), Cause: err}
		}
		if err == nil {
			switch response.Outcome {
			case shim.OutcomeRunning:
				if response.ShimPID == nil {
					return &ShimRoleStateError{Session: session, Role: role, Outcome: response.Outcome}
				}
				if *response.ShimPID != process.PID() {
					return &ShimReadyOwnerDisagreementError{Session: session, Role: role, CreatedPID: process.PID(), ObservedPID: *response.ShimPID}
				}
				return nil
			case shim.OutcomeStarting, shim.OutcomeMissing:
			default:
				return &ShimRoleStateError{Session: session, Role: role, Outcome: response.Outcome}
			}
		}
		l.sleep(ptyx.ReadinessPollInterval)
	}
}

func detachedExitError(pid int, exit error) error {
	return &detachedShimExitedError{pid: pid, cause: exit}
}

func (l ShimLauncher) rollbackDetached(ctx context.Context, record ShimFleetRecord, roles []config.RoleConfig, removeRecord bool) error {
	var cleanup []error
	allAbsent := true
	for index := len(roles) - 1; index >= 0; index-- {
		response, err := l.lifecycle.Stop(ctx, record.Session, roles[index].Name)
		if err != nil || !shimStopObservedChildExit(response) {
			allAbsent = false
			cleanup = append(cleanup, errors.Join(err, fmt.Errorf("stop role %s did not observe child exit", roles[index].Name)))
		}
	}
	if allAbsent && removeRecord {
		cleanup = append(cleanup, l.records.RemoveOwned(record))
	}
	return errors.Join(cleanup...)
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
	return "exec " + shellq.Join(shimArgv(executable, session, role))
}

func shimArgv(executable, session string, role config.RoleConfig) []string {
	argv := []string{
		executable, "__shim", "--session", session, "--role", role.Name, "--harness", string(role.Harness),
	}
	if role.Model != "" {
		argv = append(argv, "--model", role.Model)
	}
	if role.Effort != "" {
		argv = append(argv, "--effort", role.Effort)
	}
	return argv
}

func (l ShimLauncher) waitReady(ctx context.Context, session, role string, createdPID int) error {
	deadline := l.now().Add(shimLaunchObservationTimeout)
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
			case shim.OutcomeStarting, shim.OutcomeMissing:
			default:
				return &ShimRoleStateError{Session: session, Role: role, Outcome: response.Outcome}
			}
		}
		if !l.now().Before(deadline) {
			if err != nil {
				return err
			}
			return &ShimRoleStateError{Session: session, Role: role, Outcome: response.Outcome}
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
