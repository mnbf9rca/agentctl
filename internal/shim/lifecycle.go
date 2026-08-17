//go:build darwin

package shim

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/mnbf9rca/agentctl/internal/harness"
	"github.com/mnbf9rca/agentctl/internal/ptyx"
)

// RunRequest contains only validated shim identity/configuration and typed
// local stream boundaries. Child argv is reconstructed through harness.
type RunRequest struct {
	Session        string
	Role           string
	Harness        string
	HarnessOptions harness.Options
	Environment    []string
	InitialSize    ptyx.WindowSize
	OperatorInput  ptyx.ContextReader
	OperatorOutput ptyx.ContextWriter
	OuterTerminal  *os.File
	OuterState     ptyx.TerminalState
	OperatorMode   OperatorMode
}

// OperatorMode is selected by trusted launcher wiring. It is never decoded
// from the hidden shim argv or any attach/control request.
type OperatorMode uint8

const (
	OperatorForeground OperatorMode = iota
	OperatorTmux
	OperatorDetached
)

// LifecycleCommitUncertainError preserves the fail-closed durable-record
// contract at its first consumer. The visible record is retained and no later
// lifecycle step may infer which record survived a crash.
type LifecycleCommitUncertainError struct {
	Phase string
	Err   error
}

func (e *LifecycleCommitUncertainError) Error() string {
	return fmt.Sprintf("durable %s record commit is uncertain; ownership cannot proceed: %v", e.Phase, e.Err)
}

func (e *LifecycleCommitUncertainError) Unwrap() error { return e.Err }

// LifecycleOwnershipRetainedError means cleanup could not observe ESRCH, so
// the durable child record was retained and the role is not reported absent.
type LifecycleOwnershipRetainedError struct {
	ChildPID    int
	Observation ProcessObservation
	Cause       error
}

func (e *LifecycleOwnershipRetainedError) Error() string {
	return fmt.Sprintf("child was not observed absent; durable ownership retained after %v (%s)", e.Cause, e.Observation)
}

func (e *LifecycleOwnershipRetainedError) Unwrap() error { return e.Cause }

// ExistingRoleRecordError refuses overwriting durable evidence whose child
// absence has not been established by the sole ESRCH oracle.
type ExistingRoleRecordError struct {
	Path  string
	State RecordState
}

func (e *ExistingRoleRecordError) Error() string {
	return fmt.Sprintf("durable role record %q already exists in state %s", e.Path, e.State)
}

// LifecycleRunError preserves the initiating readiness fact and the separate
// cleanup observation needed to select one exact lifecycle outcome row.
type LifecycleRunError struct {
	Outcome            Outcome
	ChildPID           int
	Cause              error
	CleanupObservation ProcessObservation
	CleanupErr         error
	Remaining          []string
	FinalICANON        bool
	FinalECHO          bool
}

func (e *LifecycleRunError) Error() string {
	return fmt.Sprintf("shim lifecycle %s for child pid %d: %v (cleanup %s: %v)", e.Outcome, e.ChildPID, e.Cause, e.CleanupObservation, e.CleanupErr)
}

func (e *LifecycleRunError) Unwrap() error { return e.Cause }

type claimHandle interface {
	Close() error
}

type orderedCleanupClaim interface {
	RemoveRuntimeArtifacts() error
	CloseAndRemoveLock() error
}

func removeClaimRuntimeArtifacts(claim claimHandle) error {
	if ordered, ok := claim.(orderedCleanupClaim); ok {
		return ordered.RemoveRuntimeArtifacts()
	}
	return nil
}

func releaseClaimAfterRecord(claim claimHandle) error {
	if ordered, ok := claim.(orderedCleanupClaim); ok {
		return ordered.CloseAndRemoveLock()
	}
	if combined, ok := claim.(interface{ CloseAndRemove() error }); ok {
		return combined.CloseAndRemove()
	}
	return claim.Close()
}

type roleListener interface {
	Accept() (net.Conn, error)
	Close() error
}

type lifecycleRelay interface {
	Run(context.Context) error
	Writer() operationWriter
	MarkReady(ptyx.TerminalState) error
}

type lifecycleResidentRelay interface {
	lifecycleRelay
	AdmitViewer(ptyx.ContextWriter) (*ptyx.ResidentViewer, error)
	Flush(context.Context) ptyx.ResidentFlushResult
}

type lifecycleDependencies struct {
	rolePath            func(string, string) (*RolePath, error)
	pid                 func() int
	nonce               func() (string, error)
	acquireClaim        func(*RolePath, Advisory) (claimHandle, error)
	writeRecord         func(*RolePath, Record) error
	readRecord          func(*RolePath) (Record, error)
	removeRecord        func(*RolePath) error
	startToken          func(int) (StartToken, error)
	observeProcess      func(int, StartToken) ProcessResult
	observePresence     func(int) ProcessResult
	observeRemaining    func(*RolePath) ([]string, error)
	cleanupPollInterval time.Duration
	cleanupTimeout      time.Duration
	starter             ptyx.ChildStarter
	listen              func(string) (roleListener, error)
	listenAttach        func(string) (roleListener, error)
	newMasterEndpoint   func(*os.File) (ptyx.ContextReadWriter, func() error, error)
	newRelay            func(ptyx.ContextReader, ptyx.ContextWriter, ptyx.ContextReadWriter) lifecycleRelay
	newResidentRelay    func(*os.File, ptyx.ContextWriter) (lifecycleResidentRelay, func() error, error)
	terminal            ptyx.Terminal
}

type roleLifecycle struct {
	deps lifecycleDependencies
}

type roleRuntime struct {
	mu               sync.Mutex
	path             *RolePath
	claim            claimHandle
	record           Record
	child            ptyx.Child
	listener         roleListener
	attachListener   roleListener
	relay            lifecycleRelay
	resident         lifecycleResidentRelay
	terminal         ptyx.Terminal
	restoreEndpoint  func() error
	closeResident    func() error
	outerTerminal    *os.File
	outerState       ptyx.TerminalState
	outerModeApplied bool
}

func newRoleLifecycle(namespace *Namespace) roleLifecycle {
	deps := lifecycleDependencies{
		rolePath:            namespace.RolePath,
		pid:                 os.Getpid,
		nonce:               generateLifecycleNonce,
		acquireClaim:        func(path *RolePath, advisory Advisory) (claimHandle, error) { return AcquireClaim(path, advisory) },
		writeRecord:         WriteRecord,
		readRecord:          ReadRecord,
		removeRecord:        RemoveRecord,
		startToken:          ReadStartToken,
		observeProcess:      ObserveProcess,
		observePresence:     ObserveProcessPresence,
		observeRemaining:    observeRemainingRoleArtifacts,
		cleanupPollInterval: ptyx.ReadinessPollInterval,
		cleanupTimeout:      ptyx.ReadinessTimeout,
		starter:             ptyx.ExecChildStarter{},
		listen:              listenRoleSocket,
		listenAttach:        listenRoleSocket,
		newMasterEndpoint: func(file *os.File) (ptyx.ContextReadWriter, func() error, error) {
			endpoint, err := ptyx.NewFileEndpoint(file)
			if err != nil {
				return nil, nil, err
			}
			return endpoint, endpoint.Restore, nil
		},
		newRelay: func(input ptyx.ContextReader, output ptyx.ContextWriter, master ptyx.ContextReadWriter) lifecycleRelay {
			return ptyxRelayAdapter{relay: ptyx.NewRelay(input, output, master)}
		},
		newResidentRelay: func(master *os.File, writer ptyx.ContextWriter) (lifecycleResidentRelay, func() error, error) {
			reader, err := ptyx.NewPTYReader(master)
			if err != nil {
				return nil, nil, err
			}
			relay := ptyx.NewResidentRelay(reader, writer)
			return residentRelayAdapter{relay: relay}, reader.Close, nil
		},
		terminal: ptyx.NewTerminal(),
	}
	return roleLifecycle{deps: deps}
}

func generateLifecycleNonce() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate shim nonce: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func listenRoleSocket(path string) (roleListener, error) {
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("set shim socket mode: %w", err)
	}
	return listener, nil
}

type ptyxRelayAdapter struct{ relay *ptyx.Relay }

func (a ptyxRelayAdapter) Run(ctx context.Context) error { return a.relay.Run(ctx) }
func (a ptyxRelayAdapter) Writer() operationWriter       { return a.relay.Writer() }
func (a ptyxRelayAdapter) MarkReady(state ptyx.TerminalState) error {
	return a.relay.MarkReady(state)
}

type residentRelayAdapter struct{ relay *ptyx.ResidentRelay }

func (a residentRelayAdapter) Run(ctx context.Context) error { return a.relay.Run(ctx) }
func (a residentRelayAdapter) Writer() operationWriter       { return a.relay.Writer() }
func (a residentRelayAdapter) MarkReady(state ptyx.TerminalState) error {
	if !state.Settled() {
		return ptyx.ErrTerminalNotSettled
	}
	return nil
}
func (a residentRelayAdapter) AdmitViewer(writer ptyx.ContextWriter) (*ptyx.ResidentViewer, error) {
	return a.relay.AdmitViewer(writer)
}
func (a residentRelayAdapter) Flush(ctx context.Context) ptyx.ResidentFlushResult {
	return a.relay.Flush(ctx)
}

func (l roleLifecycle) start(ctx context.Context, request RunRequest, spec harness.Spec) (*roleRuntime, error) {
	if request.OperatorMode != OperatorForeground && request.OperatorMode != OperatorTmux && request.OperatorMode != OperatorDetached {
		return nil, fmt.Errorf("unknown operator mode %d", request.OperatorMode)
	}
	if l.deps.removeRecord == nil {
		l.deps.removeRecord = RemoveRecord
	}
	if l.deps.observeProcess == nil {
		l.deps.observeProcess = ObserveProcess
	}
	path, err := l.deps.rolePath(request.Session, request.Role)
	if err != nil {
		return nil, err
	}
	if l.deps.readRecord != nil {
		existing, readErr := l.deps.readRecord(path)
		switch {
		case readErr == nil:
			return nil, errors.Join(&ExistingRoleRecordError{Path: path.Record, State: existing.State}, path.Close())
		case !errors.Is(readErr, os.ErrNotExist):
			return nil, errors.Join(readErr, path.Close())
		}
	}
	nonce, err := l.deps.nonce()
	if err != nil {
		return nil, errors.Join(err, path.Close())
	}
	shimPID := l.deps.pid()
	claim, err := l.deps.acquireClaim(path, Advisory{
		Version: ShimProtocolVersion, ShimPID: shimPID, Nonce: nonce, StateRoot: path.StateRoot,
	})
	if err != nil {
		return nil, errors.Join(err, path.Close())
	}
	closeBeforeChild := func(cause error) (*roleRuntime, error) {
		return nil, errors.Join(cause, claim.Close(), path.Close())
	}
	cleanupNoChild := func(cause error) (*roleRuntime, error) {
		var cleanupErrors []error
		cleanupErrors = append(cleanupErrors, removeClaimRuntimeArtifacts(claim))
		cleanupErrors = append(cleanupErrors, l.deps.removeRecord(path))
		cleanupErrors = append(cleanupErrors, releaseClaimAfterRecord(claim))
		cleanupErrors = append(cleanupErrors, path.Close())
		return nil, errors.Join(cause, errors.Join(cleanupErrors...))
	}

	reservation := NewChildStartingRecord(request.Session, request.Role, shimPID, nonce)
	if err := l.deps.writeRecord(path, reservation); err != nil {
		var uncertain *RecordCommitUncertainError
		if errors.As(err, &uncertain) {
			return closeBeforeChild(&LifecycleCommitUncertainError{Phase: "child-starting", Err: err})
		}
		return cleanupNoChild(err)
	}
	argv, err := harness.AgentArgv(request.Session, request.Role, request.Harness, request.HarnessOptions)
	if err != nil {
		return cleanupNoChild(err)
	}
	child, err := l.deps.starter.Start(ctx, ptyx.StartRequest{
		Argv: argv, Env: append([]string(nil), request.Environment...), Size: request.InitialSize,
	})
	if err != nil {
		var started *ptyx.StartedChildError
		if errors.As(err, &started) {
			return l.cleanupStartedChild(path, claim, reservation, started.Child, StartToken{}, err, nil)
		}
		return cleanupNoChild(err)
	}
	token, err := l.deps.startToken(child.PID())
	if err != nil {
		return l.cleanupStartedChild(path, claim, reservation, child, StartToken{}, err, nil)
	}
	childRecord, err := reservation.WithChild(child.PID(), token)
	if err != nil {
		return l.cleanupStartedChild(path, claim, reservation, child, token, err, nil)
	}
	if err := l.deps.writeRecord(path, childRecord); err != nil {
		var uncertain *RecordCommitUncertainError
		if errors.As(err, &uncertain) {
			err = &LifecycleCommitUncertainError{Phase: "child-recorded", Err: err}
		}
		return l.cleanupStartedChild(path, claim, childRecord, child, token, err, nil)
	}
	listener, err := l.deps.listen(path.Socket)
	if err != nil {
		return l.cleanupStartedChild(path, claim, childRecord, child, token, err, nil)
	}
	var attachListener roleListener
	if request.OperatorMode == OperatorDetached {
		attachListener, err = l.deps.listenAttach(path.Attach)
		if err != nil {
			return l.cleanupStartedChild(path, claim, childRecord, child, token, err, listener)
		}
	}
	master, restore, err := l.deps.newMasterEndpoint(child.Master())
	if err != nil {
		return l.cleanupStartedChild(path, claim, childRecord, child, token, err, listener, attachListener)
	}
	var relay lifecycleRelay
	var resident lifecycleResidentRelay
	var closeResident func() error
	if request.OperatorMode == OperatorDetached {
		resident, closeResident, err = l.deps.newResidentRelay(child.Master(), master)
		if err != nil {
			return l.cleanupStartedChild(path, claim, childRecord, child, token, errors.Join(err, restore()), listener, attachListener)
		}
		relay = resident
	} else {
		relay = l.deps.newRelay(request.OperatorInput, request.OperatorOutput, master)
	}
	return &roleRuntime{
		path: path, claim: claim, record: childRecord, child: child, listener: listener, attachListener: attachListener,
		relay: relay, resident: resident, terminal: l.deps.terminal, restoreEndpoint: restore, closeResident: closeResident,
		outerTerminal: request.OuterTerminal, outerState: request.OuterState,
	}, nil
}

func (l roleLifecycle) cleanupStartedChild(
	path *RolePath,
	claim claimHandle,
	record Record,
	child ptyx.Child,
	token StartToken,
	cause error,
	listeners ...roleListener,
) (*roleRuntime, error) {
	var cleanupErrors []error
	for _, listener := range listeners {
		if listener != nil {
			cleanupErrors = append(cleanupErrors, listener.Close())
		}
	}
	signal := child.SignalProcessGroup(syscall.SIGHUP)
	cleanupErrors = append(cleanupErrors, signal.Err)
	var retainedToken *StartToken
	if token.validate() == nil {
		retainedToken = &token
	}
	result := l.observeStartedCleanup(child.PID(), retainedToken)
	cleanupErrors = append(cleanupErrors, child.CloseMaster())
	var uncertain *LifecycleCommitUncertainError
	commitUncertain := errors.As(cause, &uncertain)
	if result.MayReportAbsent() && !commitUncertain {
		cleanupErrors = append(cleanupErrors, removeClaimRuntimeArtifacts(claim))
		cleanupErrors = append(cleanupErrors, l.deps.removeRecord(path))
		cleanupErrors = append(cleanupErrors, releaseClaimAfterRecord(claim))
		cleanupErrors = append(cleanupErrors, path.Close())
		return nil, errors.Join(cause, errors.Join(cleanupErrors...))
	}
	if commitUncertain {
		cleanupErrors = append(cleanupErrors, claim.Close(), path.Close())
		return nil, errors.Join(cause, errors.Join(cleanupErrors...))
	}
	remaining := map[string]bool{"child": true}
	if l.deps.observeRemaining != nil {
		observed, err := l.deps.observeRemaining(path)
		cleanupErrors = append(cleanupErrors, err)
		for _, artifact := range observed {
			remaining[artifact] = true
		}
	}
	ordered := orderedCleanupArtifacts(remaining)
	failure := CleanupFailure{
		Cause:       errors.Join(cause, errors.Join(cleanupErrors...)).Error(),
		Observation: cleanupObservationFromProcess(result.Observation),
		Remaining:   ordered,
	}
	cleanupRecord, recordErr := record.WithCleanupFailure(child.PID(), retainedToken, failure)
	if recordErr == nil {
		recordErr = l.deps.writeRecord(path, cleanupRecord)
	}
	cleanupErrors = append(cleanupErrors, recordErr)
	retained := &LifecycleOwnershipRetainedError{ChildPID: child.PID(), Observation: result.Observation, Cause: cause}
	cleanupErrors = append(cleanupErrors, claim.Close(), path.Close())
	return nil, errors.Join(retained, errors.Join(cleanupErrors...))
}

func cleanupObservationFromProcess(observation ProcessObservation) CleanupObservation {
	switch observation {
	case ProcessPresentMatch:
		return CleanupObservationPresentMatch
	case ProcessPresentTokenDisagreement:
		return CleanupObservationPresentTokenDisagreement
	case ProcessPresentNotOurs:
		return CleanupObservationPresentNotOurs
	default:
		return CleanupObservationCouldNotObserve
	}
}

func orderedCleanupArtifacts(present map[string]bool) []string {
	var ordered []string
	for _, artifact := range []string{"child", "socket", "attach", "record", "lock"} {
		if present[artifact] {
			ordered = append(ordered, artifact)
		}
	}
	return ordered
}

func (l roleLifecycle) observeStartedCleanup(pid int, token *StartToken) ProcessResult {
	deadline := time.Now().Add(l.deps.cleanupTimeout)
	for {
		var result ProcessResult
		if token == nil {
			if l.deps.observePresence == nil {
				return ProcessResult{Observation: ProcessCouldNotObserve, Err: errors.New("process presence observer is unavailable")}
			}
			result = l.deps.observePresence(pid)
		} else {
			result = l.deps.observeProcess(pid, *token)
		}
		if result.MayReportAbsent() || l.deps.cleanupTimeout <= 0 || !time.Now().Before(deadline) {
			return result
		}
		time.Sleep(l.deps.cleanupPollInterval)
	}
}

func (r *roleRuntime) waitReady(ctx context.Context) error {
	state, err := r.terminal.WaitReady(ctx, r.child.Master())
	if err != nil {
		return err
	}
	if r.outerTerminal != nil {
		if err := r.terminal.SetTermios(r.outerTerminal, state.RelayInputState()); err != nil {
			return err
		}
		r.mu.Lock()
		r.outerModeApplied = true
		r.mu.Unlock()
	}
	return r.relay.MarkReady(state)
}

func (r *roleRuntime) restoreOuterTerminal() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.outerModeApplied || r.outerTerminal == nil {
		return nil
	}
	r.outerModeApplied = false
	return r.terminal.SetTermios(r.outerTerminal, r.outerState)
}
