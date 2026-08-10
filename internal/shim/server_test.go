//go:build darwin

package shim

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mnbf9rca/agentctl/internal/harness"
	"github.com/mnbf9rca/agentctl/internal/ptyx"
)

func TestServerRunServesClosedOperationsAndCleansOnlyAfterObservedAbsence(t *testing.T) {
	base := shortTempDir(t)
	namespace, err := openNamespaceRoots(namespaceRoots{Runtime: base + "/runtime", State: base + "/state"})
	if err != nil {
		t.Fatalf("openNamespaceRoots() error = %v", err)
	}
	t.Cleanup(func() { _ = namespace.Close() })
	child := newServerRunChild(t, 456)
	writer := &recordingOperationWriter{}
	relay := &serverRunRelay{writer: writer}
	terminal := &serverRunTerminal{resized: make(chan struct{}, 1)}
	resizeSignals := make(chan os.Signal, 1)
	deps := lifecycleDependencies{
		rolePath: namespace.RolePath,
		pid:      os.Getpid,
		nonce:    func() (string, error) { return "server-run", nil },
		acquireClaim: func(path *RolePath, advisory Advisory) (claimHandle, error) {
			return AcquireClaim(path, advisory)
		},
		writeRecord: WriteRecord,
		startToken:  func(int) (StartToken, error) { return StartToken{Sec: 1, Usec: 2}, nil },
		starter: lifecycleStarterFunc(func(context.Context, ptyx.StartRequest) (ptyx.Child, error) {
			return child, nil
		}),
		listen: listenRoleSocket,
		newMasterEndpoint: func(*os.File) (ptyx.ContextReadWriter, func() error, error) {
			return &lifecycleFakeEndpoint{}, func() error { return nil }, nil
		},
		newRelay: func(ptyx.ContextReader, ptyx.ContextWriter, ptyx.ContextReadWriter) lifecycleRelay {
			return relay
		},
		terminal: terminal,
	}
	server := &Server{
		lifecycle: roleLifecycle{deps: deps},
		observeProcess: func(pid int, token StartToken) ProcessResult {
			if pid != 456 || token != (StartToken{Sec: 1, Usec: 2}) {
				t.Fatalf("ObserveProcess(%d, %#v)", pid, token)
			}
			select {
			case <-child.done:
				return ProcessResult{Observation: ProcessAbsent}
			default:
				observed := token
				return ProcessResult{Observation: ProcessPresentMatch, ObservedToken: &observed}
			}
		},
		resizeEvents: func() (<-chan os.Signal, func()) { return resizeSignals, func() {} },
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runDone := make(chan error, 1)
	go func() {
		runDone <- server.Run(ctx, RunRequest{
			Session: "fleet", Role: "planner", Harness: "codex",
			Environment: []string{"PATH=/usr/bin"}, InitialSize: ptyx.WindowSize{Rows: 24, Cols: 80},
			OperatorInput: &lifecycleFakeEndpoint{}, OperatorOutput: &lifecycleFakeEndpoint{},
			OuterTerminal: child.Master(), OuterState: ptyx.TerminalState{},
		})
	}()

	client := NewClient(namespace)
	deadline := time.Now().Add(5 * time.Second)
	for {
		response, observeErr := client.Observe(context.Background(), "fleet", "planner")
		if observeErr == nil && response.Outcome == OutcomeRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not become ready: response=%#v error=%v", response, observeErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	resizeSignals <- syscall.SIGWINCH
	select {
	case <-terminal.resized:
	case <-time.After(time.Second):
		t.Fatal("Server.Run() did not forward SIGWINCH to the child PTY")
	}
	if response, err := client.DeliverOperation(context.Background(), "fleet", "planner", "clear"); err != nil || response.Outcome != OutcomeDeliverySubmitted {
		t.Fatalf("DeliverOperation() = %#v, %v", response, err)
	}
	if response, err := client.Stop(context.Background(), "fleet", "planner"); err != nil || response.Outcome != OutcomeStopChildExited {
		t.Fatalf("Stop() = %#v, %v", response, err)
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Server.Run() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Server.Run() did not return after observed stop")
	}
	if got, want := writer.calls, [][]byte{{0x15}, []byte("/clear"), {'\r'}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PTY writes = %#v, want %#v", got, want)
	}
	for _, artifact := range []string{
		base + "/runtime/fleet/planner.lock",
		base + "/runtime/fleet/planner.sock",
		base + "/state/sessions/fleet/roles/planner.json",
	} {
		if _, err := os.Lstat(artifact); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Lstat(%q) error = %v, want os.ErrNotExist", artifact, err)
		}
	}
}

func TestShimServerRefusesIdentityAndReadinessBeforePTYMutation(t *testing.T) {
	spec, _ := harness.Lookup("codex")
	writer := &recordingOperationWriter{}
	handler := &requestHandler{
		session: "fleet", role: "planner", shimPID: 123, childPID: 456,
		operations: newOperationExecutor(spec, writer, func(context.Context, time.Duration) error { return nil }),
	}

	wrongIdentity := exchangeWithHandler(t, handler, []byte(`{"version":1,"session":"other","role":"planner","operation":"clear"}`))
	if wrongIdentity.Outcome != OutcomeInvalidRequest || wrongIdentity.Cause == nil {
		t.Fatalf("wrong-identity response = %#v, want invalid-request", wrongIdentity)
	}
	preReady := exchangeWithHandler(t, handler, []byte(`{"version":1,"session":"fleet","role":"planner","operation":"clear"}`))
	if preReady.Outcome != OutcomeStarting || preReady.State == nil || *preReady.State != "child-recorded" {
		t.Fatalf("pre-ready response = %#v, want starting child-recorded", preReady)
	}
	if len(writer.calls) != 0 {
		t.Fatalf("refused requests wrote PTY bytes: %#v", writer.calls)
	}
}

func TestShimServerControlOperationsUseNoPTYPath(t *testing.T) {
	spec, _ := harness.Lookup("codex")
	writer := &recordingOperationWriter{}
	state := "running"
	shimPID, childPID := 123, 456
	exitObserved, attempted := true, true
	signal := "SIGHUP"
	observeResponse := Response{Version: 1, Outcome: OutcomeRunning, State: &state, ShimPID: &shimPID, ChildPID: &childPID}
	stopResponse := Response{Version: 1, Outcome: OutcomeStopChildExited, ChildPID: &childPID, SignalAttempted: &attempted, Signal: &signal, ChildExitObserved: &exitObserved}
	handler := &requestHandler{
		session: "fleet", role: "planner", shimPID: shimPID, childPID: childPID, ready: true,
		operations: newOperationExecutor(spec, writer, nil),
		observe:    func() Response { return observeResponse },
		stop:       func(context.Context) (Response, error) { return stopResponse, nil },
	}

	if got := exchangeWithHandler(t, handler, []byte(`{"version":1,"session":"fleet","role":"planner","operation":"observe"}`)); !reflect.DeepEqual(got, observeResponse) {
		t.Fatalf("observe response = %#v, want %#v", got, observeResponse)
	}
	if got := exchangeWithHandler(t, handler, []byte(`{"version":1,"session":"fleet","role":"planner","operation":"stop"}`)); !reflect.DeepEqual(got, stopResponse) {
		t.Fatalf("stop response = %#v, want %#v", got, stopResponse)
	}
	if len(writer.calls) != 0 {
		t.Fatalf("control operations wrote PTY bytes: %#v", writer.calls)
	}
}

func TestShimServerVersionAndRegistryRefusalsPrecedeMutation(t *testing.T) {
	spec, _ := harness.Lookup("codex")
	writer := &recordingOperationWriter{}
	handler := &requestHandler{
		session: "fleet", role: "planner", shimPID: 123, childPID: 456, ready: true,
		operations: newOperationExecutor(spec, writer, nil),
	}

	skew := exchangeWithHandler(t, handler, []byte(`{"version":2,"session":"fleet","role":"planner","operation":"invented"}`))
	if skew.Outcome != OutcomeProtocolSkew || skew.Cause == nil || *skew.Cause != "2" {
		t.Fatalf("foreign-version response = %#v, want protocol-skew observed 2", skew)
	}
	unknown := exchangeWithHandler(t, handler, []byte(`{"version":1,"session":"fleet","role":"planner","operation":"invented"}`))
	if unknown.Outcome != OutcomeProtocolSchemaInvalid || unknown.Cause == nil || *unknown.Cause != `operation "invented" is not registered` {
		t.Fatalf("unknown-operation response = %#v, want protocol-schema-invalid", unknown)
	}
	if len(writer.calls) != 0 {
		t.Fatalf("version/registry refusals wrote PTY bytes: %#v", writer.calls)
	}
}

func TestShimStopDeadlineRaceReturnsObservedExitInsteadOfInvalidRetainedAbsence(t *testing.T) {
	child := &stopRaceChild{pid: 456}
	token := StartToken{Sec: 1, Usec: 2}
	watcher := &childWatcher{done: make(chan struct{})}
	server := &Server{
		stopTimeout: time.Millisecond,
		observeProcess: func(pid int, observedToken StartToken) ProcessResult {
			if pid != child.pid || observedToken != token {
				t.Fatalf("observeProcess(%d, %#v)", pid, observedToken)
			}
			watcher.exit = ptyx.ExitObservation{Observed: true, PID: child.pid, ExitCode: 0}
			close(watcher.done)
			return ProcessResult{Observation: ProcessAbsent}
		},
	}
	record, err := NewChildStartingRecord("fleet", "planner", 123, "nonce").WithChild(child.pid, token)
	if err != nil {
		t.Fatalf("WithChild() error = %v", err)
	}
	runtime := &roleRuntime{child: child, record: record}

	response, err := server.stopRuntime(context.Background(), runtime, watcher)
	if err != nil {
		t.Fatalf("stopRuntime() error = %v", err)
	}
	if response.Outcome != OutcomeStopChildExited || response.ChildExitObserved == nil || !*response.ChildExitObserved {
		t.Fatalf("stopRuntime() response = %#v, want stop-child-exited", response)
	}
	if _, err := EncodeResponse(response); err != nil {
		t.Fatalf("EncodeResponse(stop response) error = %v", err)
	}
}

func TestShimCleanupRemainingListUsesPostCleanupPathObservations(t *testing.T) {
	path := newTestRolePath(t)
	claim, err := AcquireClaim(path, Advisory{Version: 1, ShimPID: os.Getpid(), Nonce: "remaining", StateRoot: path.StateRoot})
	if err != nil {
		t.Fatalf("AcquireClaim() error = %v", err)
	}
	t.Cleanup(func() { _ = claim.Close() })

	if got, err := observeRemainingRoleArtifacts(path); err != nil || !reflect.DeepEqual(got, []string{"lock"}) {
		t.Fatalf("observeRemainingRoleArtifacts() = %#v, %v, want only observed lock", got, err)
	}
	if err := claim.CloseAndRemove(); err != nil {
		t.Fatalf("CloseAndRemove() error = %v", err)
	}
	if got, err := observeRemainingRoleArtifacts(path); err != nil || len(got) != 0 {
		t.Fatalf("observeRemainingRoleArtifacts() after cleanup = %#v, %v, want none", got, err)
	}
}

func exchangeWithHandler(t *testing.T, handler *requestHandler, request []byte) Response {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	done := make(chan error, 1)
	go func() { done <- handler.handleConnection(context.Background(), server) }()
	hello, err := ReadFrame(client)
	if err != nil {
		t.Fatalf("ReadFrame(hello) error = %v", err)
	}
	if err := DecodeHello(hello); err != nil {
		t.Fatalf("DecodeHello() error = %v", err)
	}
	if _, err := WriteFrame(client, request); err != nil {
		t.Fatalf("WriteFrame(request) error = %v", err)
	}
	payload, err := ReadFrame(client)
	if err != nil {
		t.Fatalf("ReadFrame(response) error = %v", err)
	}
	response, err := DecodeResponse(payload)
	if err != nil {
		t.Fatalf("DecodeResponse() error = %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("handleConnection() error = %v", err)
	}
	return response
}

func TestShimPayloadOperationWritesClosedSequenceAndReportsOnlyObservedFacts(t *testing.T) {
	spec, _ := harness.Lookup("codex")
	writer := &recordingOperationWriter{}
	waits := make([]time.Duration, 0, 1)
	executor := newOperationExecutor(spec, writer, func(_ context.Context, duration time.Duration) error {
		waits = append(waits, duration)
		return nil
	})

	response, err := executor.Deliver(context.Background(), "clear")
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if got, want := writer.calls, [][]byte{{0x15}, []byte("/clear"), {'\r'}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PTY writes = %#v, want %#v", got, want)
	}
	if got, want := waits, []time.Duration{time.Second}; !reflect.DeepEqual(got, want) {
		t.Fatalf("waits = %v, want %v", got, want)
	}
	if response.Outcome != OutcomeDeliverySubmitted || response.BytesWritten == nil || *response.BytesWritten != uint64(len("/clear")) || response.SubmitObserved == nil || !*response.SubmitObserved {
		t.Fatalf("Deliver() response = %#v, want exact submitted facts", response)
	}
}

func TestShimControlOperationsNeverReachThePTYWriter(t *testing.T) {
	spec, _ := harness.Lookup("codex")
	writer := &recordingOperationWriter{}
	executor := newOperationExecutor(spec, writer, nil)

	for _, operation := range []string{"observe", "stop"} {
		if _, err := executor.Deliver(context.Background(), operation); !errors.Is(err, ErrOperationHasNoPayload) {
			t.Fatalf("Deliver(%q) error = %v, want ErrOperationHasNoPayload", operation, err)
		}
	}
	if len(writer.calls) != 0 {
		t.Fatalf("control operations wrote PTY bytes: %#v", writer.calls)
	}
}

func TestShimPayloadCancellationReportsResidueWithoutSubmit(t *testing.T) {
	spec, _ := harness.Lookup("claude")
	writer := &recordingOperationWriter{}
	executor := newOperationExecutor(spec, writer, func(context.Context, time.Duration) error {
		return context.Canceled
	})

	response, err := executor.Deliver(context.Background(), "compact")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Deliver() error = %v, want context.Canceled", err)
	}
	if got, want := writer.calls, [][]byte{{0x15}, []byte("/compact")}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PTY writes = %#v, want %#v", got, want)
	}
	if response.Outcome != OutcomeDeliveryCancelledWithResidue || response.BytesWritten == nil || *response.BytesWritten != uint64(len("/compact")) {
		t.Fatalf("Deliver() response = %#v, want payload residue facts", response)
	}
}

func TestShimClientDisconnectCancelsDeliveryBeforeSubmit(t *testing.T) {
	spec, _ := harness.Lookup("codex")
	writer := &recordingOperationWriter{}
	waiting := make(chan struct{})
	handler := &requestHandler{
		session: "fleet", role: "planner", shimPID: 123, childPID: 456, ready: true,
		operations: newOperationExecutor(spec, writer, func(ctx context.Context, _ time.Duration) error {
			close(waiting)
			<-ctx.Done()
			return ctx.Err()
		}),
	}
	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- handler.handleConnection(context.Background(), server) }()
	hello, err := ReadFrame(client)
	if err != nil {
		t.Fatalf("ReadFrame(hello) error = %v", err)
	}
	if err := DecodeHello(hello); err != nil {
		t.Fatalf("DecodeHello() error = %v", err)
	}
	request, _ := EncodeRequest(Request{Version: 1, Session: "fleet", Role: "planner", Operation: "clear"})
	if _, err := WriteFrame(client, request); err != nil {
		t.Fatalf("WriteFrame(request) error = %v", err)
	}
	<-waiting
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handleConnection continued delivery after client disconnect")
	}
	want := [][]byte{{0x15}, []byte("/clear")}
	writer.mu.Lock()
	got := append([][]byte(nil), writer.calls...)
	writer.mu.Unlock()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PTY writes = %#v, want clear and payload without submit %#v", got, want)
	}
}

func TestShimCompletedRequestDeadlineDoesNotCancelLongStop(t *testing.T) {
	spec, _ := harness.Lookup("codex")
	writer := &recordingOperationWriter{}
	childPID := 456
	attempted, exitObserved := true, true
	signalName := "SIGHUP"
	handler := &requestHandler{
		session: "fleet", role: "planner", shimPID: 123, childPID: childPID, ready: true,
		operations: newOperationExecutor(spec, writer, nil),
		stop: func(ctx context.Context) (Response, error) {
			select {
			case <-time.After(ShimProtocolIOTimeout + 100*time.Millisecond):
				return Response{Version: 1, Outcome: OutcomeStopChildExited, ChildPID: &childPID, SignalAttempted: &attempted, Signal: &signalName, ChildExitObserved: &exitObserved}, nil
			case <-ctx.Done():
				return Response{}, ctx.Err()
			}
		},
	}
	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- handler.handleConnection(context.Background(), server) }()
	hello, err := ReadFrame(client)
	if err != nil {
		t.Fatalf("ReadFrame(hello) error = %v", err)
	}
	if err := DecodeHello(hello); err != nil {
		t.Fatalf("DecodeHello() error = %v", err)
	}
	if err := client.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("clear client hello deadline: %v", err)
	}
	request, _ := EncodeRequest(Request{Version: 1, Session: "fleet", Role: "planner", Operation: "stop"})
	if _, err := WriteFrame(client, request); err != nil {
		t.Fatalf("WriteFrame(request) error = %v", err)
	}
	var header [4]byte
	if _, err := io.ReadFull(client, header[:]); err != nil {
		t.Fatalf("read response header: %v", err)
	}
	responsePayload := make([]byte, binary.BigEndian.Uint32(header[:]))
	if _, err := io.ReadFull(client, responsePayload); err != nil {
		t.Fatalf("read response payload: %v", err)
	}
	response, err := DecodeResponse(responsePayload)
	if err != nil || response.Outcome != OutcomeStopChildExited {
		t.Fatalf("DecodeResponse() = %#v, %v, want stop-child-exited", response, err)
	}
	if err := <-done; err != nil {
		t.Fatalf("handleConnection() error = %v", err)
	}
}

func TestShimConcurrentPayloadOperationsRemainWholeSequences(t *testing.T) {
	spec, _ := harness.Lookup("codex")
	writer := &recordingOperationWriter{}
	executor := newOperationExecutor(spec, writer, func(context.Context, time.Duration) error { return nil })

	start := make(chan struct{})
	errorsSeen := make(chan error, 2)
	for _, operation := range []string{"clear", "compact"} {
		operation := operation
		go func() {
			<-start
			_, err := executor.Deliver(context.Background(), operation)
			errorsSeen <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-errorsSeen; err != nil {
			t.Fatalf("Deliver() error = %v", err)
		}
	}

	got := writer.calls
	clearThenCompact := [][]byte{{0x15}, []byte("/clear"), {'\r'}, {0x15}, []byte("/compact"), {'\r'}}
	compactThenClear := [][]byte{{0x15}, []byte("/compact"), {'\r'}, {0x15}, []byte("/clear"), {'\r'}}
	if !reflect.DeepEqual(got, clearThenCompact) && !reflect.DeepEqual(got, compactThenClear) {
		t.Fatalf("concurrent PTY writes = %#v, want two non-interleaved operation sequences", got)
	}
}

type recordingOperationWriter struct {
	mu    sync.Mutex
	calls [][]byte
}

type stopRaceChild struct{ pid int }

func (c *stopRaceChild) PID() int         { return c.pid }
func (*stopRaceChild) Master() *os.File   { return nil }
func (*stopRaceChild) CloseMaster() error { return nil }
func (*stopRaceChild) Wait(context.Context) (ptyx.ExitObservation, error) {
	return ptyx.ExitObservation{}, context.Canceled
}
func (c *stopRaceChild) SignalProcessGroup(signal os.Signal) ptyx.SignalObservation {
	return ptyx.SignalObservation{Attempted: true, Signal: signal, ProcessGroupID: c.pid}
}
func (c *stopRaceChild) Terminate(context.Context, os.Signal) (ptyx.TerminationObservation, error) {
	return ptyx.TerminationObservation{}, errors.New("not used")
}

type serverRunChild struct {
	pid        int
	master     *os.File
	done       chan struct{}
	signalOnce sync.Once
}

func newServerRunChild(t *testing.T, pid int) *serverRunChild {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	return &serverRunChild{pid: pid, master: reader, done: make(chan struct{})}
}

func (c *serverRunChild) PID() int         { return c.pid }
func (c *serverRunChild) Master() *os.File { return c.master }
func (c *serverRunChild) Wait(ctx context.Context) (ptyx.ExitObservation, error) {
	select {
	case <-c.done:
		return ptyx.ExitObservation{Observed: true, PID: c.pid, ExitCode: 0}, nil
	case <-ctx.Done():
		return ptyx.ExitObservation{PID: c.pid}, ctx.Err()
	}
}
func (c *serverRunChild) SignalProcessGroup(signal os.Signal) ptyx.SignalObservation {
	c.signalOnce.Do(func() { close(c.done) })
	return ptyx.SignalObservation{Attempted: true, Signal: signal, ProcessGroupID: c.pid}
}
func (c *serverRunChild) Terminate(ctx context.Context, signal os.Signal) (ptyx.TerminationObservation, error) {
	result := ptyx.TerminationObservation{Signal: c.SignalProcessGroup(signal)}
	exit, err := c.Wait(ctx)
	result.Exit = exit
	return result, err
}
func (c *serverRunChild) CloseMaster() error { return nil }

type serverRunRelay struct {
	writer operationWriter
}

func (*serverRunRelay) Run(ctx context.Context) error      { <-ctx.Done(); return ctx.Err() }
func (r *serverRunRelay) Writer() operationWriter          { return r.writer }
func (*serverRunRelay) MarkReady(ptyx.TerminalState) error { return nil }

type serverRunTerminal struct{ resized chan struct{} }

func (*serverRunTerminal) Observe(*os.File) (ptyx.TerminalState, error) {
	return ptyx.TerminalState{}, nil
}
func (*serverRunTerminal) WaitReady(context.Context, *os.File) (ptyx.TerminalState, error) {
	return ptyx.TerminalState{}, nil
}
func (t *serverRunTerminal) ForwardWindowSize(*os.File, *os.File) error {
	if t.resized != nil {
		t.resized <- struct{}{}
	}
	return nil
}
func (*serverRunTerminal) ForwardTermios(*os.File, *os.File) error       { return nil }
func (*serverRunTerminal) SetTermios(*os.File, ptyx.TerminalState) error { return nil }
func (*serverRunTerminal) SetWindowSize(*os.File, ptyx.WindowSize) error { return nil }

func (w *recordingOperationWriter) Write(_ context.Context, value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, append([]byte(nil), value...))
	return len(value), nil
}
