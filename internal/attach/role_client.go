//go:build darwin

package attach

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/mnbf9rca/agentctl/internal/fleet"
	"github.com/mnbf9rca/agentctl/internal/ptyx"
	"github.com/mnbf9rca/agentctl/internal/shim"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

const (
	AttachClientQueueBytes = 131072
	AttachTailFlushTimeout = 10 * time.Second
	AttachReportTimeout    = 2 * time.Second
	attachCounterMax       = uint64(1<<53 - 1)
)

type roleFleetReader interface {
	Read(string) (fleet.ShimFleetRecord, error)
}

type roleInspector interface {
	Inspect(context.Context, string, string) (fleet.ShimRoleObservation, error)
}

type presentationObserver interface {
	FindPresentationSession(context.Context, string) (tmuxx.Session, bool, error)
}

type roleTarget struct {
	attachPath      string
	expectedShimPID int
}

type PresentedByTmuxError struct{}

func (*PresentedByTmuxError) Error() string { return "role has an observed tmux presentation" }

type PresentationMissingError struct{}

func (*PresentationMissingError) Error() string {
	return "role was configured for tmux but its presentation is absent"
}

type ListenerAbsentError struct{ Path string }

func (e *ListenerAbsentError) Error() string { return "attach listener is absent at " + e.Path }

type ListenerUnobservableError struct {
	Path  string
	Cause error
}

func (e *ListenerUnobservableError) Error() string {
	return fmt.Sprintf("observe attach listener at %s: %v", e.Path, e.Cause)
}
func (e *ListenerUnobservableError) Unwrap() error { return e.Cause }

type RoleObservationError struct{ Observation fleet.ShimRoleObservation }

func (e *RoleObservationError) Error() string {
	return fmt.Sprintf("role observation was %s", e.Observation.Outcome)
}

type rolePreflight struct {
	records      roleFleetReader
	inspector    roleInspector
	presentation presentationObserver
	namespace    *shim.Namespace
}

func (p rolePreflight) prepare(ctx context.Context, sessionName, role string) (roleTarget, error) {
	record, err := p.records.Read(sessionName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return roleTarget{}, &fleet.ShimFleetMissingError{Session: sessionName}
		}
		return roleTarget{}, err
	}
	if _, ok := record.Roles[role]; !ok {
		return roleTarget{}, &fleet.UnknownRoleError{Role: role, Roster: joinRoster(record.Roster)}
	}
	observation, err := p.inspector.Inspect(ctx, sessionName, role)
	if err != nil {
		return roleTarget{}, err
	}
	if observation.Outcome != shim.OutcomeRunning {
		return roleTarget{}, &RoleObservationError{Observation: observation}
	}
	if record.Presentation == fleet.PresentationTmux {
		_, present, err := p.presentation.FindPresentationSession(ctx, sessionName)
		if err != nil {
			return roleTarget{}, err
		}
		if present {
			return roleTarget{}, &PresentedByTmuxError{}
		}
		return roleTarget{}, &PresentationMissingError{}
	}
	path, err := p.namespace.ExistingRuntimeRolePath(sessionName, role)
	if err != nil {
		return roleTarget{}, err
	}
	defer func() { _ = path.Close() }()
	present, err := shim.AttachPresent(path)
	if err != nil {
		return roleTarget{}, &ListenerUnobservableError{Path: path.Attach, Cause: err}
	}
	if !present {
		return roleTarget{}, &ListenerAbsentError{Path: path.Attach}
	}
	return roleTarget{attachPath: path.Attach, expectedShimPID: observation.ShimPID}, nil
}

func joinRoster(roster []string) string {
	joined := ""
	for index, role := range roster {
		if index != 0 {
			joined += ","
		}
		joined += role
	}
	return joined
}

type roleTerminalOwner interface {
	streams() relayTerminal
	makeRaw() error
	restore() error
	close() error
}

func (t *ownedRelayTerminal) streams() relayTerminal { return t.relay }

type roleTerminalProvider interface {
	check() (terminalCheck, error)
	open(terminalCheck) (roleTerminalOwner, error)
}

type productionRoleTerminalProvider struct{ factory relayTerminalFactory }

func (p productionRoleTerminalProvider) check() (terminalCheck, error) { return p.factory.check() }
func (p productionRoleTerminalProvider) open(check terminalCheck) (roleTerminalOwner, error) {
	return p.factory.open(check)
}

type roleTargetPreflight interface {
	prepare(context.Context, string, string) (roleTarget, error)
}

type TransportError struct {
	Phase string
	Cause error
}

type TerminalOutputError struct {
	Prior   *RoleResult
	Raw     uint64
	Written uint64
	Cause   error
	Stalled bool
}

type workerPanicError struct{ Value any }

func (e *workerPanicError) Error() string { return fmt.Sprintf("worker panic: %v", e.Value) }

type LocalTerminationError struct{ Cause any }

func (e *LocalTerminationError) Error() string { return fmt.Sprintf("locally terminated: %v", e.Cause) }

func (e *TerminalOutputError) Error() string {
	if e.Stalled {
		return "terminal output stalled"
	}
	return fmt.Sprintf("terminal output failed: %v", e.Cause)
}
func (e *TerminalOutputError) Unwrap() error { return e.Cause }

func (e *TransportError) Error() string { return fmt.Sprintf("%s: %v", e.Phase, e.Cause) }
func (e *TransportError) Unwrap() error { return e.Cause }

type RefusalErrorRole struct{ Control shim.AttachControl }

func (e *RefusalErrorRole) Error() string { return string(e.Control.Outcome) }

// RoleClient owns one terminal-safe, framed direct-role attachment.
type RoleClient struct {
	terminal                 roleTerminalProvider
	prechecked               *terminalCheck
	preflight                roleTargetPreflight
	signals                  roleSignalProvider
	dial                     func(context.Context, string) (net.Conn, error)
	peerPID                  func(net.Conn) (int, error)
	diagnostic               OwnedDiagnosticSink
	reportTerminal           roleTerminalOwner
	stderr                   *os.File
	diagnosticSharesTerminal bool
	reportSignals            *pendingReportSignals
}

// PreparedRoleTerminal holds the read-only terminal observations that must
// precede construction of the mutable shim runtime.
type PreparedRoleTerminal struct {
	provider                 productionRoleTerminalProvider
	check                    terminalCheck
	diagnosticSharesTerminal bool
	stderr                   *os.File
}

// PrepareRoleTerminal performs the attach terminal checks without opening or
// mutating either the terminal or the shim runtime.
func PrepareRoleTerminal(stdin, stdout, stderr *os.File) (*PreparedRoleTerminal, error) {
	provider := productionRoleTerminalProvider{factory: newRelayTerminalFactory(stdin, stdout, stderr)}
	check, err := provider.check()
	if err != nil {
		return nil, err
	}
	shares := false
	if stderr != nil {
		identity, statErr := fileTerminalIdentity(stderr)
		shares = statErr == nil && identity == check.identity
	}
	return &PreparedRoleTerminal{provider: provider, check: check, diagnosticSharesTerminal: shares, stderr: stderr}, nil
}

type pendingReportSignals struct {
	plan   roleSignalPlan
	events <-chan os.Signal
}

func NewRoleClient(namespace *shim.Namespace, records roleFleetReader, inspector roleInspector, presentation presentationObserver, prepared *PreparedRoleTerminal, stdin, stdout, stderr *os.File) *RoleClient {
	provider := productionRoleTerminalProvider{factory: newRelayTerminalFactory(stdin, stdout, stderr)}
	client := &RoleClient{
		terminal:  provider,
		preflight: rolePreflight{records: records, inspector: inspector, presentation: presentation, namespace: namespace},
		signals:   newSignalProvider(),
		dial: func(ctx context.Context, path string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
		peerPID: func(connection net.Conn) (int, error) {
			unixConnection, ok := connection.(*net.UnixConn)
			if !ok {
				return 0, fmt.Errorf("attach connection has type %T, want Unix", connection)
			}
			return shim.LocalPeerPID(unixConnection)
		},
		stderr: stderr,
	}
	if prepared != nil {
		check := prepared.check
		client.terminal = prepared.provider
		client.prechecked = &check
		client.diagnosticSharesTerminal = prepared.diagnosticSharesTerminal
		client.stderr = prepared.stderr
	}
	return client
}

func (c *RoleClient) Diagnostic() DiagnosticSink {
	if c.reportTerminal != nil {
		if diagnostic := c.reportTerminal.streams().diagnostic; diagnostic != nil {
			return diagnostic
		}
		return discardDiagnosticSink{}
	}
	if c.diagnostic == nil && c.stderr != nil {
		diagnostic, err := NewDiagnosticSink(c.stderr)
		if err == nil {
			c.diagnostic = diagnostic
		}
	}
	if c.diagnostic == nil && c.stderr != nil {
		return discardDiagnosticSink{}
	}
	return c.diagnostic
}
func (c *RoleClient) DiagnosticSharesTerminal() bool { return c.diagnosticSharesTerminal }
func (c *RoleClient) Close() error {
	if c.reportSignals != nil {
		c.reportSignals.plan.close()
		c.reportSignals = nil
	}
	var terminalErr error
	if c.reportTerminal != nil {
		terminalErr = c.reportTerminal.close()
		c.reportTerminal = nil
	}
	var diagnosticErr error
	if c.diagnostic != nil {
		diagnosticErr = c.diagnostic.Close()
		c.diagnostic = nil
	}
	return errors.Join(terminalErr, diagnosticErr)
}

func (c *RoleClient) Execute(ctx context.Context, sessionName, role string) (result RoleResult, resultErr error) {
	var check terminalCheck
	var err error
	if c.prechecked != nil {
		check = *c.prechecked
	} else {
		check, err = c.terminal.check()
		if err != nil {
			return RoleResult{}, err
		}
	}
	if c.prechecked == nil && c.stderr != nil {
		identity, statErr := fileTerminalIdentity(c.stderr)
		c.diagnosticSharesTerminal = statErr == nil && identity == check.identity
	}
	target, err := c.preflight.prepare(ctx, sessionName, role)
	if err != nil {
		return RoleResult{}, err
	}
	terminal, err := c.terminal.open(check)
	if err != nil {
		return RoleResult{}, err
	}
	var signalPlan roleSignalPlan
	var signalEvents <-chan os.Signal
	retainReportSignals := false
	restoreAttempted := false
	defer func() {
		panicValue := recover()
		var restoreErr error
		if !restoreAttempted {
			restoreAttempted = true
			restoreErr = terminal.restore()
		}
		c.reportTerminal = terminal
		if panicValue != nil && restoreErr == nil {
			if signalPlan != nil {
				signalPlan.close()
			}
			panic(panicValue)
		}
		if retainReportSignals && signalPlan != nil {
			c.reportSignals = &pendingReportSignals{plan: signalPlan, events: signalEvents}
			signalPlan = nil
		}
		if signalPlan != nil {
			signalPlan.close()
		}
		if restoreErr != nil {
			if panicValue != nil {
				result = RoleResult{}
				resultErr = &TerminalRestoreError{Prior: &LocalTerminationError{Cause: panicValue}, Cause: restoreErr}
				return
			}
			var priorResult *RoleResult
			if result.Disposition != "" {
				copyResult := result
				priorResult = &copyResult
			}
			resultErr = &TerminalRestoreError{Prior: resultErr, PriorResult: priorResult, Cause: restoreErr}
		}
	}()
	if c.signals != nil {
		signalPlan, err = c.signals.observe()
		if err != nil {
			return RoleResult{}, err
		}
		signalEvents = signalPlan.install()
		retainReportSignals = true
	}
	if err := terminal.makeRaw(); err != nil {
		return RoleResult{}, &TerminalRawError{Cause: err}
	}
	connection, err := c.dial(ctx, target.attachPath)
	if err != nil {
		return RoleResult{}, &TransportError{Phase: "hello", Cause: err}
	}
	defer func() { _ = connection.Close() }()
	transportCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type transportResult struct {
		result     RoleResult
		err        error
		panicValue any
		panicked   bool
	}
	done := make(chan transportResult, 1)
	go func() {
		result, err := runRoleTransportVerified(transportCtx, connection, terminal.streams(), check.size, sessionName, role, c.validateConnectedPeer(connection, target))
		var panicErr *workerPanicError
		if errors.As(err, &panicErr) {
			done <- transportResult{panicValue: panicErr.Value, panicked: true}
			return
		}
		done <- transportResult{result: result, err: err}
	}()
	if signalEvents == nil {
		completed := <-done
		if completed.panicked {
			panic(completed.panicValue)
		}
		return completed.result, completed.err
	}
	select {
	case completed := <-done:
		retainReportSignals = true
		if completed.panicked {
			panic(completed.panicValue)
		}
		return completed.result, completed.err
	case observed := <-signalEvents:
		caught, ok := observed.(syscall.Signal)
		if !ok {
			return RoleResult{}, &TransportError{Phase: "relay", Cause: fmt.Errorf("observed signal has type %T", observed)}
		}
		cancel()
		_ = connection.Close()
		retainReportSignals = false
		restoreAttempted = true
		if restoreErr := terminal.restore(); restoreErr != nil {
			return RoleResult{}, &TerminalRestoreError{Cause: restoreErr}
		}
		if err := signalPlan.reraise(caught); err != nil {
			return RoleResult{}, &SignalReraiseError{Signal: caught, Cause: err}
		}
		return RoleResult{}, &SignalReraiseError{Signal: caught, Cause: errors.New("signal re-raise returned without terminating the process")}
	}
}

func (c *RoleClient) validateConnectedPeer(connection net.Conn, target roleTarget) func() error {
	if c.peerPID == nil || target.expectedShimPID == 0 {
		return nil
	}
	return func() error {
		answererPID, err := c.peerPID(connection)
		if err != nil {
			return &RefusalErrorRole{Control: shim.AttachControl{Outcome: shim.AttachRefusalPeerUnobservable, Cause: err.Error()}}
		}
		if answererPID != target.expectedShimPID {
			return &RoleObservationError{Observation: fleet.ShimRoleObservation{Outcome: shim.OutcomeAnswererDisagreement, ShimPID: target.expectedShimPID, AnswererPID: answererPID}}
		}
		return nil
	}
}

// Report keeps eligible signal handling active through diagnostic emission.
// A relay-terminal row finishes its bounded attempt before a caught signal is
// reproduced; a redirected row may remain blocked while the owner re-raises.
func (c *RoleClient) Report(render func(DiagnosticSink, bool) int) (code int) {
	pending := c.reportSignals
	c.reportSignals = nil
	defer func() {
		if value := recover(); value != nil {
			if pending != nil {
				pending.plan.close()
			}
			_ = c.closeReportTerminal()
			panic(value)
		}
	}()
	diagnostic := c.Diagnostic()
	if pending == nil {
		code = render(diagnostic, c.diagnosticSharesTerminal)
		_ = c.closeReportTerminal()
		return code
	}
	type reportCompletion struct {
		code       int
		panicValue any
		panicked   bool
	}
	done := make(chan reportCompletion, 1)
	go func() {
		completed := reportCompletion{}
		defer func() {
			if value := recover(); value != nil {
				completed.panicValue = value
				completed.panicked = true
			}
			done <- completed
		}()
		completed.code = render(diagnostic, c.diagnosticSharesTerminal)
	}()
	select {
	case completed := <-done:
		pending.plan.stop()
		var caught syscall.Signal
		select {
		case observed := <-pending.events:
			caught, _ = observed.(syscall.Signal)
		default:
		}
		if caught != 0 {
			reraiseErr := pending.plan.reraise(caught)
			pending.plan.close()
			_ = c.closeReportTerminal()
			if reraiseErr != nil {
				return 6
			}
			if completed.panicked {
				panic(completed.panicValue)
			}
			return completed.code
		}
		if completed.panicked {
			panic(completed.panicValue)
		}
		pending.plan.close()
		_ = c.closeReportTerminal()
		return completed.code
	case observed := <-pending.events:
		caught, ok := observed.(syscall.Signal)
		if !ok {
			pending.plan.close()
			completed := <-done
			if completed.panicked {
				panic(completed.panicValue)
			}
			_ = c.closeReportTerminal()
			return completed.code
		}
		completedCode := 0
		completed := false
		if c.diagnosticSharesTerminal {
			report := <-done
			if report.panicked {
				panic(report.panicValue)
			}
			completedCode = report.code
			completed = true
		}
		reraiseErr := pending.plan.reraise(caught)
		pending.plan.close()
		if completed {
			_ = c.closeReportTerminal()
			if reraiseErr != nil {
				return 6
			}
			return completedCode
		}
		if reraiseErr != nil {
			_ = c.closeReportTerminal()
			return 6
		}
		select {
		case report := <-done:
			if report.panicked {
				panic(report.panicValue)
			}
			_ = c.closeReportTerminal()
			return report.code
		default:
			return 6
		}
	}
}

func (c *RoleClient) closeReportTerminal() error {
	if c.reportTerminal == nil {
		return nil
	}
	terminal := c.reportTerminal
	c.reportTerminal = nil
	return terminal.close()
}

type SignalReraiseError struct {
	Signal syscall.Signal
	Cause  error
}

func (e *SignalReraiseError) Error() string {
	return fmt.Sprintf("re-raise %s: %v", canonicalSignalName(e.Signal), e.Cause)
}
func (e *SignalReraiseError) Unwrap() error { return e.Cause }

type TerminalRawError struct{ Cause error }

func (e *TerminalRawError) Error() string { return e.Cause.Error() }
func (e *TerminalRawError) Unwrap() error { return e.Cause }

type TerminalRestoreError struct {
	Prior       error
	PriorResult *RoleResult
	Cause       error
}

func (e *TerminalRestoreError) Error() string   { return errors.Join(e.Prior, e.Cause).Error() }
func (e *TerminalRestoreError) Unwrap() []error { return []error{e.Prior, e.Cause} }

func runRoleTransport(ctx context.Context, connection net.Conn, terminal relayTerminal, size ptyx.WindowSize, sessionName, role string) (RoleResult, error) {
	return runRoleTransportVerified(ctx, connection, terminal, size, sessionName, role, nil)
}

func runRoleTransportVerified(ctx context.Context, connection net.Conn, terminal relayTerminal, size ptyx.WindowSize, sessionName, role string, validatePeer func() error) (RoleResult, error) {
	sequence := &shim.AttachSequence{}
	frame, err := shim.ReadAttachFrame(connection)
	if err != nil {
		return RoleResult{}, &TransportError{Phase: "hello", Cause: err}
	}
	if err := sequence.Observe(shim.AttachFromShim, frame); err != nil {
		return RoleResult{}, &TransportError{Phase: "hello", Cause: err}
	}
	if validatePeer != nil {
		if err := validatePeer(); err != nil {
			return RoleResult{}, err
		}
	}
	hello, err := shim.EncodeAttachControl(shim.AttachControl{Version: shim.ShimProtocolVersion, Kind: shim.AttachControlHello, Session: sessionName, Role: role, Rows: uint32(size.Rows), Cols: uint32(size.Cols)})
	if err != nil {
		return RoleResult{}, &TransportError{Phase: "hello", Cause: err}
	}
	clientHello := shim.AttachFrame{Kind: shim.AttachFrameControl, Data: hello}
	if err := sequence.Observe(shim.AttachFromClient, clientHello); err != nil {
		return RoleResult{}, &TransportError{Phase: "hello", Cause: err}
	}
	if err := shim.WriteAttachFrame(connection, clientHello); err != nil {
		return RoleResult{}, &TransportError{Phase: "hello", Cause: err}
	}
	decision, err := shim.ReadAttachFrame(connection)
	if err != nil {
		return RoleResult{}, &TransportError{Phase: "admission", Cause: err}
	}
	if err := sequence.Observe(shim.AttachFromShim, decision); err != nil {
		return RoleResult{}, &TransportError{Phase: "admission", Cause: err}
	}
	control, err := shim.DecodeAttachControl(decision.Data)
	if err != nil {
		return RoleResult{}, &TransportError{Phase: "admission", Cause: err}
	}
	if control.Kind == shim.AttachControlRefused {
		return RoleResult{}, &RefusalErrorRole{Control: control}
	}

	relayCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	clientWriter := &clientFrameWriter{connection: connection}
	inputDone := make(chan error, 1)
	go func() {
		err := runRelayWorker(func() error { return relayViewerInput(relayCtx, clientWriter, terminal.input) })
		inputDone <- err
		if err != nil {
			_ = connection.Close()
		}
	}()
	var resizeEvents chan os.Signal
	var resizeDone chan error
	if terminal.size != nil {
		resizeEvents = make(chan os.Signal, 1)
		resizeDone = make(chan error, 1)
		signal.Notify(resizeEvents, syscall.SIGWINCH)
		defer signal.Stop(resizeEvents)
		go func() {
			err := runRelayWorker(func() error { return relayViewerResize(relayCtx, clientWriter, terminal.size, resizeEvents) })
			resizeDone <- err
			if err != nil {
				_ = connection.Close()
			}
		}()
	}
	result, outputErr := relayRoleOutput(relayCtx, connection, terminal.output, sequence)
	cancel()
	_ = connection.Close()
	var inputErr error
	select {
	case inputErr = <-inputDone:
	default:
	}
	var resizeErr error
	if resizeDone != nil {
		select {
		case resizeErr = <-resizeDone:
		default:
		}
	}
	if resizeErr != nil && !errors.Is(resizeErr, context.Canceled) && !errors.Is(resizeErr, net.ErrClosed) {
		if panicErr := capturedWorkerPanic(resizeErr); panicErr != nil {
			return RoleResult{}, panicErr
		}
		return RoleResult{}, &TransportError{Phase: "relay", Cause: resizeErr}
	}
	if inputErr != nil && !errors.Is(inputErr, context.Canceled) && !errors.Is(inputErr, io.EOF) && !errors.Is(inputErr, net.ErrClosed) {
		if panicErr := capturedWorkerPanic(inputErr); panicErr != nil {
			return RoleResult{}, panicErr
		}
		return RoleResult{}, &TransportError{Phase: "relay", Cause: inputErr}
	}
	if outputErr != nil {
		return RoleResult{}, outputErr
	}
	return result, nil
}

func runRelayWorker(operation func() error) (resultErr error) {
	defer func() {
		if value := recover(); value != nil {
			resultErr = &workerPanicError{Value: value}
		}
	}()
	return operation()
}

type clientFrameWriter struct {
	mu         sync.Mutex
	connection net.Conn
}

func (w *clientFrameWriter) write(frame shim.AttachFrame) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return shim.WriteAttachFrame(w.connection, frame)
}

func relayViewerInput(ctx context.Context, writer *clientFrameWriter, reader ptyx.ContextReader) error {
	buffer := make([]byte, 32*1024)
	for {
		count, err := reader.Read(ctx, buffer)
		if count > 0 {
			if writeErr := writer.write(shim.AttachFrame{Kind: shim.AttachFrameViewerInput, Data: append([]byte(nil), buffer[:count]...)}); writeErr != nil {
				return writeErr
			}
		}
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrNoProgress
		}
	}
}

func relayViewerResize(ctx context.Context, writer *clientFrameWriter, observe func() (ptyx.WindowSize, error), events <-chan os.Signal) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-events:
			size, err := observe()
			if err != nil {
				return err
			}
			payload, err := shim.EncodeAttachControl(shim.AttachControl{Version: shim.ShimProtocolVersion, Kind: shim.AttachControlResize, Rows: uint32(size.Rows), Cols: uint32(size.Cols)})
			if err != nil {
				return err
			}
			if err := writer.write(shim.AttachFrame{Kind: shim.AttachFrameControl, Data: payload}); err != nil {
				return err
			}
		}
	}
}

func relayRoleOutput(ctx context.Context, connection net.Conn, writer ptyx.ContextWriter, sequence *shim.AttachSequence) (RoleResult, error) {
	return relayRoleOutputWithTimeout(ctx, connection, writer, sequence, AttachTailFlushTimeout)
}

func relayRoleOutputWithTimeout(ctx context.Context, connection net.Conn, writer ptyx.ContextWriter, sequence *shim.AttachSequence, timeout time.Duration) (RoleResult, error) {
	queue := newClientOutputQueue(AttachClientQueueBytes)
	writerCtx, cancelWriter := context.WithCancel(ctx)
	defer cancelWriter()
	writes := make(chan clientWriteResult, 1)
	go func() {
		write := drainClientOutput(writerCtx, writer, queue, timeout)
		writes <- write
		if write.err != nil || write.stalled {
			_ = connection.Close()
		}
	}()
	var raw uint64
	for {
		frame, err := shim.ReadAttachFrame(connection)
		if err != nil {
			queue.close()
			select {
			case write := <-writes:
				if panicErr := capturedWorkerPanic(write.err); panicErr != nil {
					return RoleResult{}, panicErr
				}
				if write.stalled {
					return RoleResult{}, &TerminalOutputError{Raw: raw, Written: write.written, Stalled: true}
				}
				if write.err != nil && !errors.Is(write.err, context.Canceled) {
					return RoleResult{}, &TerminalOutputError{Raw: raw, Written: write.written, Cause: write.err}
				}
			default:
			}
			return RoleResult{}, &TransportError{Phase: "relay", Cause: err}
		}
		if err := sequence.Observe(shim.AttachFromShim, frame); err != nil {
			return RoleResult{}, &TransportError{Phase: "relay", Cause: err}
		}
		switch frame.Kind {
		case shim.AttachFrameRoleOutput:
			nextRaw, counterErr := advanceAttachRawCounter(raw, len(frame.Data))
			if counterErr != nil {
				queue.close()
				return RoleResult{}, &TransportError{Phase: "relay", Cause: counterErr}
			}
			raw = nextRaw
			enqueueCtx, cancel := context.WithTimeout(ctx, timeout)
			write, err := queue.enqueueObserved(enqueueCtx, frame.Data, writes)
			cancel()
			if write != nil {
				queue.close()
				if panicErr := capturedWorkerPanic(write.err); panicErr != nil {
					return RoleResult{}, panicErr
				}
				if write.stalled {
					return RoleResult{}, &TerminalOutputError{Raw: raw, Written: write.written, Stalled: true}
				}
				cause := write.err
				if cause == nil {
					cause = io.ErrUnexpectedEOF
				}
				return RoleResult{}, &TerminalOutputError{Raw: raw, Written: write.written, Cause: cause}
			}
			if err != nil {
				queue.close()
				cancelWriter()
				write := <-writes
				if panicErr := capturedWorkerPanic(write.err); panicErr != nil {
					return RoleResult{}, panicErr
				}
				if errors.Is(err, context.DeadlineExceeded) {
					if write.stalled {
						return RoleResult{}, &TerminalOutputError{Raw: raw, Written: write.written, Stalled: true}
					}
					if write.err != nil && !errors.Is(write.err, context.Canceled) && !errors.Is(write.err, context.DeadlineExceeded) {
						return RoleResult{}, &TerminalOutputError{Raw: raw, Written: write.written, Cause: write.err}
					}
					return RoleResult{}, &TerminalOutputError{Raw: raw, Written: write.written, Stalled: true}
				}
				return RoleResult{}, err
			}
		case shim.AttachFrameControl:
			control, err := shim.DecodeAttachControl(frame.Data)
			if err != nil {
				return RoleResult{}, &TransportError{Phase: "final", Cause: err}
			}
			final := RoleResult{Disposition: control.Disposition, Bytes: control.Bytes, Raw: raw, Undelivered: control.Undelivered, KnownUndelivered: control.KnownUndelivered, Rows: control.Rows, Cols: control.Cols, Cause: control.Cause}
			queue.close()
			timer := time.NewTimer(timeout)
			defer timer.Stop()
			select {
			case write := <-writes:
				final.Written = write.written
				if panicErr := capturedWorkerPanic(write.err); panicErr != nil {
					return RoleResult{}, panicErr
				}
				if write.stalled {
					return RoleResult{}, &TerminalOutputError{Prior: &final, Raw: raw, Written: write.written, Stalled: true}
				}
				if write.err != nil {
					return RoleResult{}, &TerminalOutputError{Prior: &final, Raw: raw, Written: write.written, Cause: write.err}
				}
				if raw != control.Bytes {
					return RoleResult{}, &TransportError{Phase: "final", Cause: fmt.Errorf("client read %d role-output bytes; shim reported %d", raw, control.Bytes)}
				}
				return final, nil
			case <-timer.C:
				cancelWriter()
				write := <-writes
				return RoleResult{}, &TerminalOutputError{Prior: &final, Raw: raw, Written: write.written, Stalled: true}
			case <-ctx.Done():
				cancelWriter()
				<-writes
				return RoleResult{}, ctx.Err()
			}
		}
	}
}

func capturedWorkerPanic(err error) error {
	var panicErr *workerPanicError
	if errors.As(err, &panicErr) {
		return panicErr
	}
	return nil
}

func advanceAttachRawCounter(current uint64, count int) (uint64, error) {
	if count < 0 || current > attachCounterMax || uint64(count) > attachCounterMax-current {
		return current, fmt.Errorf("client RAW byte counter would exceed %d", attachCounterMax)
	}
	return current + uint64(count), nil
}

type clientOutputQueue struct {
	mu       sync.Mutex
	capacity int
	bytes    int
	items    [][]byte
	closed   bool
	changed  chan struct{}
}

func newClientOutputQueue(capacity int) *clientOutputQueue {
	return &clientOutputQueue{capacity: capacity, changed: make(chan struct{}, 1)}
}

func (q *clientOutputQueue) notify() {
	select {
	case q.changed <- struct{}{}:
	default:
	}
}

func (q *clientOutputQueue) enqueue(ctx context.Context, value []byte) error {
	_, err := q.enqueueObserved(ctx, value, nil)
	return err
}

func (q *clientOutputQueue) enqueueObserved(ctx context.Context, value []byte, writes <-chan clientWriteResult) (*clientWriteResult, error) {
	copyValue := append([]byte(nil), value...)
	for {
		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			return nil, net.ErrClosed
		}
		if len(copyValue) <= q.capacity-q.bytes {
			q.items = append(q.items, copyValue)
			q.bytes += len(copyValue)
			q.mu.Unlock()
			q.notify()
			return nil, nil
		}
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-q.changed:
		case write := <-writes:
			return &write, nil
		}
	}
}

func (q *clientOutputQueue) take(ctx context.Context) ([]byte, bool, error) {
	for {
		q.mu.Lock()
		if len(q.items) != 0 {
			value := q.items[0]
			q.items = q.items[1:]
			q.mu.Unlock()
			return value, true, nil
		}
		if q.closed {
			q.mu.Unlock()
			return nil, false, nil
		}
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-q.changed:
		}
	}
}

func (q *clientOutputQueue) complete(count int) {
	q.mu.Lock()
	q.bytes -= count
	q.mu.Unlock()
	q.notify()
}

func (q *clientOutputQueue) close() { q.mu.Lock(); q.closed = true; q.mu.Unlock(); q.notify() }

type clientWriteResult struct {
	written uint64
	err     error
	stalled bool
}

func drainClientOutput(ctx context.Context, writer ptyx.ContextWriter, queue *clientOutputQueue, timeout time.Duration) (result clientWriteResult) {
	var total uint64
	defer func() {
		if value := recover(); value != nil {
			result = clientWriteResult{written: total, err: &workerPanicError{Value: value}}
		}
	}()
	for {
		value, ok, err := queue.take(ctx)
		if err != nil {
			return clientWriteResult{written: total, err: err}
		}
		if !ok {
			return clientWriteResult{written: total}
		}
		accepted := 0
		for accepted < len(value) {
			writeCtx, cancel := context.WithTimeout(ctx, timeout)
			count, writeErr := writer.Write(writeCtx, value[accepted:])
			timedOut := errors.Is(writeErr, context.DeadlineExceeded) && ctx.Err() == nil
			cancel()
			if count < 0 || count > len(value)-accepted {
				return clientWriteResult{written: total, err: fmt.Errorf("terminal writer returned invalid count %d", count)}
			}
			accepted += count
			total += uint64(count)
			if timedOut {
				return clientWriteResult{written: total, stalled: true}
			}
			if writeErr != nil {
				return clientWriteResult{written: total, err: writeErr}
			}
			if count == 0 {
				return clientWriteResult{written: total, err: io.ErrNoProgress}
			}
		}
		queue.complete(len(value))
	}
}
