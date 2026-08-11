//go:build darwin

package shim

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/mnbf9rca/agentctl/internal/control"
	"github.com/mnbf9rca/agentctl/internal/harness"
	"github.com/mnbf9rca/agentctl/internal/ptyx"
)

const payloadSubmitDelay = time.Second

// ErrOperationHasNoPayload protects the lifecycle control operations from
// accidentally entering the PTY-delivery path.
var ErrOperationHasNoPayload = errors.New("operation has no PTY payload")

type requestHandler struct {
	mu           sync.RWMutex
	operationMu  sync.Mutex
	phaseMu      sync.RWMutex
	phase        shimOperationPhase
	session      string
	role         string
	shimPID      int
	childPID     int
	ready        bool
	operations   *operationExecutor
	observe      func() Response
	stop         func(context.Context) (Response, error)
	stopComplete func()
}

type shimOperationPhase string

const (
	shimOperationActive   shimOperationPhase = "active"
	shimOperationStopping shimOperationPhase = "stopping"
	shimOperationStopped  shimOperationPhase = "stopped"
)

func (h *requestHandler) operationPhase() shimOperationPhase {
	h.phaseMu.RLock()
	defer h.phaseMu.RUnlock()
	if h.phase == "" {
		return shimOperationActive
	}
	return h.phase
}

func (h *requestHandler) beginStop() (shimOperationPhase, bool) {
	h.phaseMu.Lock()
	defer h.phaseMu.Unlock()
	if h.phase == "" || h.phase == shimOperationActive {
		h.phase = shimOperationStopping
		return shimOperationStopping, true
	}
	return h.phase, false
}

func (h *requestHandler) setOperationPhase(phase shimOperationPhase) {
	h.phaseMu.Lock()
	h.phase = phase
	h.phaseMu.Unlock()
}

func (h *requestHandler) operationPhaseResponse(outcome Outcome, phase shimOperationPhase) Response {
	state := string(phase)
	shimPID, childPID := h.shimPID, h.childPID
	return Response{
		Version: ShimProtocolVersion, Outcome: outcome, State: &state,
		ShimPID: &shimPID, ChildPID: &childPID,
	}
}

func (h *requestHandler) alreadyStoppingResponse(phase shimOperationPhase) Response {
	response := h.operationPhaseResponse(OutcomeStopAlreadyStopping, phase)
	attempted := false
	response.SignalAttempted = &attempted
	return response
}

func (h *requestHandler) setReady() {
	h.mu.Lock()
	h.ready = true
	h.mu.Unlock()
}

func (h *requestHandler) readiness() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.ready
}

func (h *requestHandler) handleConnection(ctx context.Context, connection net.Conn) error {
	hello, err := EncodeHello()
	if err != nil {
		return err
	}
	if _, err := WriteFrame(connection, hello); err != nil {
		return err
	}
	payload, err := ReadFrame(connection)
	if err != nil {
		return err
	}
	request, err := DecodeRequest(payload)
	if err != nil {
		var skew *ProtocolSkewError
		if errors.As(err, &skew) {
			cause := skew.CanonicalObserved()
			return writeHandlerResponse(connection, Response{Version: ShimProtocolVersion, Outcome: OutcomeProtocolSkew, Cause: &cause})
		}
		var schema *ProtocolSchemaError
		if errors.As(err, &schema) {
			cause := schema.CanonicalCause()
			return writeHandlerResponse(connection, Response{Version: ShimProtocolVersion, Outcome: OutcomeProtocolSchemaInvalid, Cause: &cause})
		}
		cause := err.Error()
		return writeHandlerResponse(connection, Response{Version: ShimProtocolVersion, Outcome: OutcomeInvalidRequest, Cause: &cause})
	}
	if err := connection.SetReadDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clear completed request-frame read deadline: %w", err)
	}
	requestCtx, cancelRequest := context.WithCancel(ctx)
	defer cancelRequest()
	peerReadDone := make(chan struct{})
	go func() {
		defer close(peerReadDone)
		var unexpected [1]byte
		_, _ = connection.Read(unexpected[:])
		cancelRequest()
	}()
	defer func() {
		_ = connection.SetReadDeadline(time.Now())
		<-peerReadDone
	}()
	if request.Session != h.session || request.Role != h.role {
		cause := fmt.Sprintf("request identity %q/%q differs from owned role %q/%q", request.Session, request.Role, h.session, h.role)
		return writeHandlerResponse(connection, Response{Version: ShimProtocolVersion, Outcome: OutcomeInvalidRequest, Cause: &cause})
	}
	command, err := control.Lookup(request.Operation)
	if err != nil {
		cause := err.Error()
		return writeHandlerResponse(connection, Response{Version: ShimProtocolVersion, Outcome: OutcomeProtocolSchemaInvalid, Cause: &cause})
	}
	if command.Kind == control.OperationControl {
		switch request.Operation {
		case "observe":
			if h.observe == nil {
				return errors.New("shim observe handler is unavailable")
			}
			switch phase := h.operationPhase(); phase {
			case shimOperationStopping:
				return writeHandlerResponse(connection, h.operationPhaseResponse(OutcomeStopping, phase))
			case shimOperationStopped:
				return writeHandlerResponse(connection, h.operationPhaseResponse(OutcomeStopped, phase))
			}
			return writeHandlerResponse(connection, h.observe())
		case "stop":
			if h.stop == nil {
				return errors.New("shim stop handler is unavailable")
			}
			phase, admitted := h.beginStop()
			if !admitted {
				return writeHandlerResponse(connection, h.alreadyStoppingResponse(phase))
			}
			h.operationMu.Lock()
			defer h.operationMu.Unlock()
			response, err := h.stop(requestCtx)
			if err != nil && response.Outcome == "" {
				h.setOperationPhase(shimOperationActive)
				return err
			}
			childExited := response.Outcome == OutcomeStopChildExited
			if childExited {
				h.setOperationPhase(shimOperationStopped)
			}
			if writeErr := writeHandlerResponse(connection, response); writeErr != nil {
				if !childExited {
					h.setOperationPhase(shimOperationActive)
				}
				return writeErr
			}
			if !childExited {
				h.setOperationPhase(shimOperationActive)
			}
			if childExited && h.stopComplete != nil {
				h.stopComplete()
			}
			return nil
		default:
			return errors.New("registered shim control operation has no handler")
		}
	}
	if !h.readiness() {
		state := string(RecordStateChildRecorded)
		return writeHandlerResponse(connection, Response{
			Version: ShimProtocolVersion, Outcome: OutcomeStarting, State: &state,
			ShimPID: &h.shimPID, ChildPID: &h.childPID,
		})
	}
	if phase := h.operationPhase(); phase != shimOperationActive {
		return writeHandlerResponse(connection, h.operationPhaseResponse(OutcomeShimStopping, phase))
	}
	h.operationMu.Lock()
	defer h.operationMu.Unlock()
	if phase := h.operationPhase(); phase != shimOperationActive {
		return writeHandlerResponse(connection, h.operationPhaseResponse(OutcomeShimStopping, phase))
	}
	response, err := h.operations.Deliver(requestCtx, request.Operation)
	if err != nil && response.Outcome == "" {
		return err
	}
	return writeHandlerResponse(connection, response)
}

// Server owns one role lifecycle and its versioned local control socket.
type Server struct {
	lifecycle      roleLifecycle
	observeProcess func(int, StartToken) ProcessResult
	resizeEvents   func() (<-chan os.Signal, func())
	stopTimeout    time.Duration
	cleanupTimeout time.Duration
}

// NewServer composes the production namespace, PTY, process, and protocol
// boundaries without changing any public CLI lifecycle route.
func NewServer(namespace *Namespace) *Server {
	return &Server{lifecycle: newRoleLifecycle(namespace), observeProcess: ObserveProcess, resizeEvents: productionResizeEvents}
}

func productionResizeEvents() (<-chan os.Signal, func()) {
	events := make(chan os.Signal, 1)
	signal.Notify(events, syscall.SIGWINCH)
	return events, func() { signal.Stop(events) }
}

type childWatcher struct {
	done chan struct{}
	exit ptyx.ExitObservation
	err  error
}

func watchChild(ctx context.Context, child ptyx.Child) *childWatcher {
	watcher := &childWatcher{done: make(chan struct{})}
	go func() {
		watcher.exit, watcher.err = child.Wait(ctx)
		close(watcher.done)
	}()
	return watcher
}

func (w *childWatcher) wait(ctx context.Context) (ptyx.ExitObservation, error) {
	select {
	case <-ctx.Done():
		return ptyx.ExitObservation{}, ctx.Err()
	case <-w.done:
		return w.exit, w.err
	}
}

// Run starts, serves, and tears down one resident role. It returns only after
// the child outcome and owned-artifact cleanup have been observed.
func (s *Server) Run(ctx context.Context, request RunRequest) error {
	if s == nil {
		return errors.New("nil shim server")
	}
	spec, ok := harness.Lookup(request.Harness)
	if !ok {
		return fmt.Errorf("unknown harness %q", request.Harness)
	}
	if request.OperatorInput == nil || request.OperatorOutput == nil {
		return errors.New("shim server requires operator input and output")
	}
	runtime, err := s.lifecycle.start(ctx, request, spec)
	if err != nil {
		return err
	}
	serverCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	watcher := watchChild(serverCtx, runtime.child)
	operations := newOperationExecutor(spec, runtime.relay.Writer(), nil)
	stopDone := make(chan struct{}, 1)
	handler := &requestHandler{
		session: request.Session, role: request.Role,
		shimPID: runtime.record.ShimPID, childPID: runtime.record.ChildPID,
		operations: operations,
	}
	handler.observe = func() Response { return s.observeRuntime(runtime, handler.readiness()) }
	handler.stop = func(stopCtx context.Context) (Response, error) {
		return s.stopRuntime(stopCtx, runtime, watcher)
	}
	handler.stopComplete = func() {
		select {
		case stopDone <- struct{}{}:
		default:
		}
	}

	relayErr := make(chan error, 1)
	go func() { relayErr <- runtime.relay.Run(serverCtx) }()
	resizeErr := s.forwardResizeEvents(serverCtx, runtime)
	acceptErr := make(chan error, 1)
	go s.serveConnections(serverCtx, runtime.listener, handler, acceptErr)
	readyErr := make(chan error, 1)
	go func() { readyErr <- runtime.waitReady(serverCtx) }()

	ready := false
	for {
		select {
		case err := <-readyErr:
			readyErr = nil
			if err != nil {
				cancel()
				cleanup := s.cleanupRuntime(runtime, true)
				return lifecycleReadinessFailure(runtime.record.ChildPID, err, cleanup)
			}
			handler.setReady()
			ready = true
		case <-watcher.done:
			cancel()
			cleanup := s.cleanupRuntime(runtime, false)
			if ready {
				return cleanup.Err
			}
			return &LifecycleRunError{
				Outcome: OutcomeChildExitedBeforeReady, ChildPID: runtime.record.ChildPID,
				Cause:              errors.New("child exited before harness tty readiness"),
				CleanupObservation: cleanup.Observation, CleanupErr: cleanup.Err,
			}
		case <-stopDone:
			cancel()
			return s.cleanupRuntime(runtime, false).Err
		case err := <-relayErr:
			if errors.Is(err, context.Canceled) && ctx.Err() == nil {
				cancel()
				continue
			}
			if exit, observed := waitObservedChildExit(watcher, ptyx.ReadinessPollInterval); observed {
				cancel()
				cleanup := s.cleanupRuntime(runtime, false)
				if ready {
					return cleanup.Err
				}
				return &LifecycleRunError{
					Outcome: OutcomeChildExitedBeforeReady, ChildPID: exit.PID,
					Cause:              errors.New("child exited before harness tty readiness"),
					CleanupObservation: cleanup.Observation, CleanupErr: cleanup.Err,
				}
			}
			cancel()
			return runtimeFailure(runtime.record.ChildPID, err, s.cleanupRuntime(runtime, true))
		case err := <-resizeErr:
			cancel()
			return runtimeFailure(runtime.record.ChildPID, err, s.cleanupRuntime(runtime, true))
		case err := <-acceptErr:
			cancel()
			if errors.Is(err, net.ErrClosed) && ctx.Err() != nil {
				return runtimeFailure(runtime.record.ChildPID, ctx.Err(), s.cleanupRuntime(runtime, true))
			}
			return runtimeFailure(runtime.record.ChildPID, err, s.cleanupRuntime(runtime, true))
		case <-ctx.Done():
			cancel()
			return runtimeFailure(runtime.record.ChildPID, ctx.Err(), s.cleanupRuntime(runtime, true))
		}
	}
}

func waitObservedChildExit(watcher *childWatcher, timeout time.Duration) (ptyx.ExitObservation, bool) {
	waitCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	exit, err := watcher.wait(waitCtx)
	return exit, err == nil && exit.Observed
}

type runtimeCleanupResult struct {
	Observation ProcessObservation
	Err         error
	Remaining   []string
}

func lifecycleReadinessFailure(childPID int, cause error, cleanup runtimeCleanupResult) error {
	result := &LifecycleRunError{
		Outcome: OutcomeReadinessObservationFailed, ChildPID: childPID, Cause: cause,
		CleanupObservation: cleanup.Observation, CleanupErr: cleanup.Err, Remaining: cleanup.Remaining,
	}
	var timeout *ptyx.ReadinessTimeoutError
	if errors.As(cause, &timeout) {
		result.Outcome = OutcomeReadinessTimeout
		result.FinalICANON = timeout.State.Canonical()
		result.FinalECHO = timeout.State.Echo()
		return result
	}
	var observation *ptyx.ReadinessObservationError
	if errors.As(cause, &observation) {
		return result
	}
	return runtimeFailure(childPID, cause, cleanup)
}

func runtimeFailure(childPID int, cause error, cleanup runtimeCleanupResult) error {
	if cleanup.Observation == ProcessAbsent {
		return errors.Join(cause, cleanup.Err)
	}
	return errors.Join(&LifecycleOwnershipRetainedError{
		ChildPID: childPID, Observation: cleanup.Observation, Cause: cause,
	}, cleanup.Err)
}

func (s *Server) forwardResizeEvents(ctx context.Context, runtime *roleRuntime) <-chan error {
	if runtime.outerTerminal == nil || s.resizeEvents == nil {
		return nil
	}
	events, stop := s.resizeEvents()
	errorsSeen := make(chan error, 1)
	go func() {
		defer stop()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-events:
				if !ok {
					return
				}
				if err := runtime.terminal.ForwardWindowSize(runtime.outerTerminal, runtime.child.Master()); err != nil {
					errorsSeen <- err
					return
				}
			}
		}
	}()
	return errorsSeen
}

func (s *Server) serveConnections(ctx context.Context, listener roleListener, handler *requestHandler, fatal chan<- error) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			fatal <- err
			return
		}
		go func() {
			defer func() { _ = connection.Close() }()
			_ = handler.handleConnection(ctx, connection)
		}()
	}
}

func (s *Server) observeRuntime(runtime *roleRuntime, ready bool) Response {
	result := s.observeProcess(runtime.record.ChildPID, *runtime.record.ChildStartToken)
	childPID := runtime.record.ChildPID
	shimPID := runtime.record.ShimPID
	switch result.Observation {
	case ProcessAbsent:
		return Response{Version: 1, Outcome: OutcomeStaleRecord, ChildPID: &childPID}
	case ProcessPresentTokenDisagreement:
		return Response{Version: 1, Outcome: OutcomePresentTokenDisagreement, ChildPID: &childPID, RecordedToken: runtime.record.ChildStartToken, ObservedToken: result.ObservedToken}
	case ProcessPresentNotOurs:
		return Response{Version: 1, Outcome: OutcomePresentNotOurs, ChildPID: &childPID}
	case ProcessCouldNotObserve:
		cause := fmt.Sprint(result.Err)
		return Response{Version: 1, Outcome: OutcomeCouldNotObserve, ChildPID: &childPID, Cause: &cause}
	default:
		if !ready {
			state := string(RecordStateChildRecorded)
			return Response{Version: 1, Outcome: OutcomeStarting, State: &state, ShimPID: &shimPID, ChildPID: &childPID}
		}
		state := string(OutcomeRunning)
		return Response{Version: 1, Outcome: OutcomeRunning, State: &state, ShimPID: &shimPID, ChildPID: &childPID}
	}
}

func (s *Server) stopRuntime(ctx context.Context, runtime *roleRuntime, watcher *childWatcher) (Response, error) {
	signalObservation := runtime.child.SignalProcessGroup(syscall.SIGHUP)
	attempted := signalObservation.Attempted
	signal := "SIGHUP"
	childPID := runtime.child.PID()
	stopTimeout := s.stopTimeout
	if stopTimeout <= 0 {
		// A stop outcome must still leave time for the protocol's fixed
		// two-second response-frame write deadline.
		stopTimeout = ShimProtocolIOTimeout / 2
	}
	waitCtx, cancel := context.WithTimeout(ctx, stopTimeout)
	defer cancel()
	exit, waitErr := watcher.wait(waitCtx)
	if waitErr == nil && exit.Observed {
		exitObserved := true
		response := Response{Version: 1, Outcome: OutcomeStopChildExited, ChildPID: &childPID, SignalAttempted: &attempted, Signal: &signal, ChildExitObserved: &exitObserved}
		if signalObservation.Err != nil {
			cause := signalObservation.Err.Error()
			response.Cause = &cause
		}
		return response, nil
	}
	result := s.observeProcess(childPID, *runtime.record.ChildStartToken)
	if result.MayReportAbsent() {
		recheckCtx, recheckCancel := context.WithTimeout(context.Background(), ptyx.ReadinessPollInterval)
		exit, recheckErr := watcher.wait(recheckCtx)
		recheckCancel()
		if recheckErr == nil && exit.Observed {
			exitObserved := true
			response := Response{Version: 1, Outcome: OutcomeStopChildExited, ChildPID: &childPID, SignalAttempted: &attempted, Signal: &signal, ChildExitObserved: &exitObserved}
			if signalObservation.Err != nil {
				cause := signalObservation.Err.Error()
				response.Cause = &cause
			}
			return response, nil
		}
		result = ProcessResult{Observation: ProcessCouldNotObserve, Err: errors.New("child was absent but parent exit observation was not available")}
	}
	exitObserved := false
	state := string(result.Observation)
	response := Response{
		Version: 1, Outcome: OutcomeStopChildRetained, ChildPID: &childPID,
		SignalAttempted: &attempted, Signal: &signal, ChildExitObserved: &exitObserved, State: &state,
	}
	if signalObservation.Err != nil {
		cause := signalObservation.Err.Error()
		response.Cause = &cause
	} else if result.Err != nil {
		cause := result.Err.Error()
		response.Cause = &cause
	}
	return response, waitErr
}

func (s *Server) cleanupRuntime(runtime *roleRuntime, signalChild bool) runtimeCleanupResult {
	var cleanupErrors []error
	remaining := make(map[string]bool)
	if runtime.listener != nil {
		if err := runtime.listener.Close(); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	var signalErr error
	if signalChild {
		observation := runtime.child.SignalProcessGroup(syscall.SIGHUP)
		signalErr = observation.Err
	}
	result := s.observeUntilDeadline(runtime.record.ChildPID, *runtime.record.ChildStartToken)
	if signalErr != nil && (!result.MayReportAbsent() || (!errors.Is(signalErr, syscall.ESRCH) && !errors.Is(signalErr, os.ErrProcessDone))) {
		cleanupErrors = append(cleanupErrors, signalErr)
	}
	cleanupErrors = append(cleanupErrors, runtime.restoreOuterTerminal())
	if runtime.restoreEndpoint != nil {
		cleanupErrors = append(cleanupErrors, runtime.restoreEndpoint())
	}
	cleanupErrors = append(cleanupErrors, runtime.child.CloseMaster())
	if result.MayReportAbsent() {
		if err := RemoveRecord(runtime.path); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
		if cleanClaim, ok := runtime.claim.(interface{ CloseAndRemove() error }); ok {
			if err := cleanClaim.CloseAndRemove(); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		} else {
			if err := runtime.claim.Close(); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
	} else {
		remaining["child"] = true
		cleanupErrors = append(cleanupErrors, fmt.Errorf("child cleanup observation was %s", result.Observation))
	}
	observedRemaining, observationErr := observeRemainingRoleArtifacts(runtime.path)
	cleanupErrors = append(cleanupErrors, observationErr)
	for _, artifact := range observedRemaining {
		remaining[artifact] = true
	}
	if !result.MayReportAbsent() {
		failure := CleanupFailure{
			Cause:       errors.Join(cleanupErrors...).Error(),
			Observation: cleanupObservationFromProcess(result.Observation),
			Remaining:   orderedCleanupArtifacts(remaining),
		}
		cleanupRecord, err := runtime.record.WithCleanupFailure(runtime.record.ChildPID, runtime.record.ChildStartToken, failure)
		if err == nil {
			err = WriteRecord(runtime.path, cleanupRecord)
		}
		cleanupErrors = append(cleanupErrors, err, runtime.claim.Close())
	}
	cleanupErrors = append(cleanupErrors, runtime.path.Close())
	remainingOrdered := orderedCleanupArtifacts(remaining)
	return runtimeCleanupResult{Observation: result.Observation, Err: errors.Join(cleanupErrors...), Remaining: remainingOrdered}
}

func observeRemainingRoleArtifacts(path *RolePath) ([]string, error) {
	path.mu.Lock()
	defer path.mu.Unlock()
	if path.runtimeSession == nil || path.stateRoles == nil {
		return nil, errors.New("cannot observe remaining role artifacts through closed role path")
	}
	checks := []struct {
		artifact string
		root     *os.Root
		name     string
	}{
		{artifact: "socket", root: path.runtimeSession, name: path.Role + ".sock"},
		{artifact: "record", root: path.stateRoles, name: path.Role + ".json"},
		{artifact: "lock", root: path.runtimeSession, name: path.Role + ".lock"},
	}
	var remaining []string
	var observationErrors []error
	for _, check := range checks {
		_, err := check.root.Lstat(check.name)
		switch {
		case err == nil:
			remaining = append(remaining, check.artifact)
		case errors.Is(err, os.ErrNotExist):
		default:
			observationErrors = append(observationErrors, fmt.Errorf("observe remaining %s artifact: %w", check.artifact, err))
		}
	}
	return remaining, errors.Join(observationErrors...)
}

func (s *Server) observeUntilDeadline(pid int, token StartToken) ProcessResult {
	timeout := ptyx.ReadinessTimeout
	if s.cleanupTimeout != 0 {
		timeout = s.cleanupTimeout
	}
	deadline := time.Now().Add(timeout)
	for {
		result := s.observeProcess(pid, token)
		if result.MayReportAbsent() || !time.Now().Before(deadline) {
			return result
		}
		time.Sleep(ptyx.ReadinessPollInterval)
	}
}

func writeHandlerResponse(connection net.Conn, response Response) error {
	payload, err := EncodeResponse(response)
	if err != nil {
		return err
	}
	_, err = WriteFrame(connection, payload)
	return err
}

type operationWriter interface {
	Write(context.Context, []byte) (int, error)
}

type operationWait func(context.Context, time.Duration) error

type operationExecutor struct {
	harness harness.Spec
	writer  operationWriter
	wait    operationWait
	gate    chan struct{}
}

func newOperationExecutor(spec harness.Spec, writer operationWriter, wait operationWait) *operationExecutor {
	if wait == nil {
		wait = waitOperationDelay
	}
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &operationExecutor{harness: spec, writer: writer, wait: wait, gate: gate}
}

func waitOperationDelay(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Deliver resolves the operation server-side and holds one per-role gate from
// input clear through submit. No caller-provided bytes enter this method.
func (e *operationExecutor) Deliver(ctx context.Context, operation string) (Response, error) {
	command, err := control.Lookup(operation)
	if err != nil {
		return Response{}, err
	}
	if command.Kind != control.OperationPayload {
		return Response{}, ErrOperationHasNoPayload
	}
	select {
	case <-ctx.Done():
		return cancelledDelivery(0), ctx.Err()
	case <-e.gate:
	}
	defer func() { e.gate <- struct{}{} }()

	if _, err := e.writer.Write(ctx, e.harness.InputClearBytes()); err != nil {
		return cancelledOrFailedDelivery(0, err)
	}
	payloadWritten, err := e.writer.Write(ctx, []byte(command.Payload))
	if err != nil {
		return cancelledOrFailedDelivery(payloadWritten, err)
	}
	if err := e.wait(ctx, payloadSubmitDelay); err != nil {
		return cancelledOrFailedDelivery(payloadWritten, err)
	}
	if _, err := e.writer.Write(ctx, e.harness.SubmitBytes()); err != nil {
		return cancelledOrFailedDelivery(payloadWritten, err)
	}
	written := uint64(payloadWritten)
	submitted := true
	return Response{
		Version: ShimProtocolVersion, Outcome: OutcomeDeliverySubmitted,
		BytesWritten: &written, SubmitObserved: &submitted,
	}, nil
}

func cancelledOrFailedDelivery(payloadWritten int, err error) (Response, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return cancelledDelivery(payloadWritten), err
	}
	return Response{}, err
}

func cancelledDelivery(payloadWritten int) Response {
	if payloadWritten == 0 {
		return Response{Version: ShimProtocolVersion, Outcome: OutcomeDeliveryCancelledClean}
	}
	written := uint64(payloadWritten)
	return Response{
		Version: ShimProtocolVersion, Outcome: OutcomeDeliveryCancelledWithResidue,
		BytesWritten: &written,
	}
}
