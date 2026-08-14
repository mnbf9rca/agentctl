//go:build darwin

package shim

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"strings"
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

func TestServerRunDetachedServesAttachAndControlBeforeCleanExit(t *testing.T) {
	base := shortTempDir(t)
	namespace, err := openNamespaceRoots(namespaceRoots{Runtime: base + "/runtime", State: base + "/state"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = namespace.Close() })
	child := newServerRunChild(t, 457)
	writer := &recordingOperationWriter{}
	terminal := &serverRunTerminal{}
	deps := lifecycleDependencies{
		rolePath: namespace.RolePath, pid: os.Getpid,
		nonce: func() (string, error) { return "server-run-detached", nil },
		acquireClaim: func(path *RolePath, advisory Advisory) (claimHandle, error) {
			return AcquireClaim(path, advisory)
		},
		writeRecord: WriteRecord,
		startToken:  func(int) (StartToken, error) { return StartToken{Sec: 1, Usec: 3}, nil },
		starter: lifecycleStarterFunc(func(context.Context, ptyx.StartRequest) (ptyx.Child, error) {
			return child, nil
		}),
		listen:       listenRoleSocket,
		listenAttach: listenRoleSocket,
		newMasterEndpoint: func(*os.File) (ptyx.ContextReadWriter, func() error, error) {
			return &lifecycleFakeEndpoint{}, func() error { return nil }, nil
		},
		newResidentRelay: func(*os.File, ptyx.ContextWriter) (lifecycleResidentRelay, func() error, error) {
			reader := &serverResidentReader{}
			return &serverTestResidentRelay{ResidentRelay: ptyx.NewResidentRelay(reader, writer)}, func() error { return nil }, nil
		},
		terminal: terminal,
	}
	server := &Server{
		lifecycle: roleLifecycle{deps: deps},
		observeProcess: func(_ int, token StartToken) ProcessResult {
			select {
			case <-child.done:
				return ProcessResult{Observation: ProcessAbsent}
			default:
				observed := token
				return ProcessResult{Observation: ProcessPresentMatch, ObservedToken: &observed}
			}
		},
		resizeEvents: func() (<-chan os.Signal, func()) { return make(chan os.Signal), func() {} },
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runDone := make(chan error, 1)
	go func() {
		runDone <- server.Run(ctx, RunRequest{
			Session: "fleet", Role: "planner", Harness: "codex", OperatorMode: OperatorDetached,
			Environment: []string{"PATH=/usr/bin"}, InitialSize: ptyx.WindowSize{Rows: 24, Cols: 80},
		})
	}()

	client := NewClient(namespace)
	waitServerRunning(t, client, "fleet", "planner")
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: base + "/runtime/fleet/planner.attach", Net: "unix"})
	if err != nil {
		t.Fatalf("DialUnix(attach) error = %v", err)
	}
	defer func() { _ = connection.Close() }()
	admitAttachClient(t, connection)
	writeAttachFrame(t, connection, AttachFrame{Kind: AttachFrameViewerInput, Data: []byte("typed")})
	waitAttachCondition(t, func() bool {
		writer.mu.Lock()
		defer writer.mu.Unlock()
		return reflect.DeepEqual(writer.calls, [][]byte{[]byte("typed")})
	})
	if response, err := client.Stop(context.Background(), "fleet", "planner"); err != nil || response.Outcome != OutcomeStopChildExited {
		t.Fatalf("Stop() = %#v, %v", response, err)
	}
	final := readAttachControlKind(t, connection, AttachControlFinal)
	if final.Disposition != AttachDispositionChildExited {
		t.Fatalf("attach final = %#v, want child-exited", final)
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Server.Run() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("detached Server.Run() did not cleanly stop")
	}
}

func TestServerRunReturnsTypedProtocolFailureAfterStoppedChildResponseWriteFault(t *testing.T) {
	base := shortTempDir(t)
	namespace, err := openNamespaceRoots(namespaceRoots{Runtime: base + "/runtime", State: base + "/state"})
	if err != nil {
		t.Fatalf("openNamespaceRoots() error = %v", err)
	}
	t.Cleanup(func() { _ = namespace.Close() })
	child := newServerRunChild(t, 456)
	terminal := &serverRunTerminal{}
	var listener *toggleResponseFailureListener
	deps := lifecycleDependencies{
		rolePath: namespace.RolePath,
		pid:      os.Getpid,
		nonce:    func() (string, error) { return "server-run-fatal-write", nil },
		acquireClaim: func(path *RolePath, advisory Advisory) (claimHandle, error) {
			return AcquireClaim(path, advisory)
		},
		writeRecord: WriteRecord,
		startToken:  func(int) (StartToken, error) { return StartToken{Sec: 1, Usec: 2}, nil },
		starter: lifecycleStarterFunc(func(context.Context, ptyx.StartRequest) (ptyx.Child, error) {
			return child, nil
		}),
		listen: func(path string) (roleListener, error) {
			inner, listenErr := listenRoleSocket(path)
			if listenErr != nil {
				return nil, listenErr
			}
			listener = &toggleResponseFailureListener{roleListener: inner, err: errors.New("injected response transport failure")}
			return listener, nil
		},
		newMasterEndpoint: func(*os.File) (ptyx.ContextReadWriter, func() error, error) {
			return &lifecycleFakeEndpoint{}, func() error { return nil }, nil
		},
		newRelay: func(ptyx.ContextReader, ptyx.ContextWriter, ptyx.ContextReadWriter) lifecycleRelay {
			return &serverRunRelay{writer: &recordingOperationWriter{}}
		},
		terminal: terminal,
	}
	server := &Server{
		lifecycle: roleLifecycle{deps: deps},
		observeProcess: func(_ int, token StartToken) ProcessResult {
			select {
			case <-child.done:
				return ProcessResult{Observation: ProcessAbsent}
			default:
				observed := token
				return ProcessResult{Observation: ProcessPresentMatch, ObservedToken: &observed}
			}
		},
		resizeEvents: func() (<-chan os.Signal, func()) { return make(chan os.Signal), func() {} },
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
	listener.EnableFailure()
	_, _ = client.Stop(context.Background(), "fleet", "planner")
	select {
	case runErr := <-runDone:
		var frame *ProtocolFrameError
		if !errors.As(runErr, &frame) || frame.Peer != ProtocolPeerClient || frame.Direction != ProtocolFrameWrite || frame.Err == nil || !strings.Contains(frame.Err.Error(), "injected response transport failure") {
			t.Fatalf("Server.Run() error = %T %v, want typed fatal response-write failure", runErr, runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Server.Run() did not finish fatal response-write cleanup")
	}
}

func TestForegroundChildExitOutcomeKeepsExitAndSignalFactsDistinct(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		exit       ptyx.ExitObservation
		wantStatus int
		wantSignal syscall.Signal
		wantError  bool
	}{
		{name: "zero", exit: ptyx.ExitObservation{Observed: true, ExitCode: 0}},
		{name: "nonzero", exit: ptyx.ExitObservation{Observed: true, ExitCode: 17}, wantStatus: 17, wantError: true},
		{name: "signal", exit: ptyx.ExitObservation{Observed: true, ExitCode: -1, Signal: syscall.SIGHUP}, wantStatus: -1, wantSignal: syscall.SIGHUP, wantError: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			err := foregroundChildExitOutcome(test.exit)
			if !test.wantError {
				if err != nil {
					t.Fatalf("outcome error = %v, want nil", err)
				}
				return
			}
			var child *ForegroundChildExitError
			if !errors.As(err, &child) || child.Status != test.wantStatus || child.Signal != test.wantSignal {
				t.Fatalf("outcome error = %#v, want status=%d signal=%v", child, test.wantStatus, test.wantSignal)
			}
		})
	}
}

func TestShimServerRefusesIdentityAndReadinessBeforePTYMutation(t *testing.T) {
	spec, _ := harness.Lookup("codex")
	writer := &recordingOperationWriter{}
	handler := &requestHandler{
		session: "fleet", role: "planner", shimPID: 123, childPID: 456,
		operations: newOperationExecutor(spec, newRoleInputWriter(writer), func(context.Context, time.Duration) error { return nil }),
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
		operations: newOperationExecutor(spec, newRoleInputWriter(writer), nil),
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

func TestShimStopWaitsForInflightPayloadReportAndRefusesLaterMutatingOperationsWithoutSecondSignal(t *testing.T) {
	spec, _ := harness.Lookup("codex")
	writer := &recordingOperationWriter{}
	payloadStarted := make(chan struct{})
	releasePayload := make(chan struct{})
	stopCalled := make(chan struct{}, 1)
	childPID := 456
	attempted, exited := true, true
	signalName := "SIGHUP"
	handler := &requestHandler{
		session: "fleet", role: "planner", shimPID: 123, childPID: childPID, ready: true,
		operations: newOperationExecutor(spec, newRoleInputWriter(writer), func(context.Context, time.Duration) error {
			close(payloadStarted)
			<-releasePayload
			return nil
		}),
		observe: func() Response {
			state := "running"
			shimPID := 123
			return Response{Version: 1, Outcome: OutcomeRunning, State: &state, ShimPID: &shimPID, ChildPID: &childPID}
		},
		stop: func(context.Context) (Response, error) {
			stopCalled <- struct{}{}
			return Response{
				Version: 1, Outcome: OutcomeStopChildExited, ChildPID: &childPID,
				SignalAttempted: &attempted, Signal: &signalName, ChildExitObserved: &exited,
			}, nil
		},
	}

	payloadClient, payloadDone := beginHandlerRequest(t, handler, "clear")
	<-payloadStarted
	stopClient, stopDone := beginHandlerRequest(t, handler, "stop")
	waitForShimOperationPhase(t, handler, shimOperationStopping)

	refusedPayload := exchangeWithHandler(t, handler, []byte(`{"version":1,"session":"fleet","role":"planner","operation":"compact"}`))
	if refusedPayload.Outcome != OutcomeShimStopping || refusedPayload.State == nil || *refusedPayload.State != "stopping" {
		t.Fatalf("payload during stop = %#v, want shim-stopping/stopping refusal", refusedPayload)
	}
	observed := exchangeWithHandler(t, handler, []byte(`{"version":1,"session":"fleet","role":"planner","operation":"observe"}`))
	if observed.Outcome != OutcomeStopping || observed.State == nil || *observed.State != "stopping" {
		t.Fatalf("observe during stop = %#v, want admitted stopping fact", observed)
	}
	secondStop := exchangeWithHandler(t, handler, []byte(`{"version":1,"session":"fleet","role":"planner","operation":"stop"}`))
	if secondStop.Outcome != OutcomeStopAlreadyStopping || secondStop.SignalAttempted == nil || *secondStop.SignalAttempted {
		t.Fatalf("second stop = %#v, want already-stopping with signal_attempted=false", secondStop)
	}

	close(releasePayload)
	select {
	case <-stopCalled:
		t.Fatal("stop signaled before the in-flight payload response was read")
	case <-time.After(50 * time.Millisecond):
	}
	payloadResponse := readHandlerResponse(t, payloadClient, payloadDone)
	if payloadResponse.Outcome != OutcomeDeliverySubmitted {
		t.Fatalf("in-flight payload response = %#v, want delivery-submitted", payloadResponse)
	}
	select {
	case <-stopCalled:
	case <-time.After(time.Second):
		t.Fatal("stop did not signal after in-flight payload reported")
	}
	stopResponse := readHandlerResponse(t, stopClient, stopDone)
	if stopResponse.Outcome != OutcomeStopChildExited {
		t.Fatalf("primary stop response = %#v, want stop-child-exited", stopResponse)
	}
	stopped := exchangeWithHandler(t, handler, []byte(`{"version":1,"session":"fleet","role":"planner","operation":"observe"}`))
	if stopped.Outcome != OutcomeStopped || stopped.State == nil || *stopped.State != "stopped" {
		t.Fatalf("observe after stop = %#v, want admitted stopped fact", stopped)
	}

	writer.mu.Lock()
	writes := append([][]byte(nil), writer.calls...)
	writer.mu.Unlock()
	if want := [][]byte{{0x15}, []byte("/clear"), {'\r'}}; !reflect.DeepEqual(writes, want) {
		t.Fatalf("PTY writes = %#v, want only the admitted in-flight payload %#v", writes, want)
	}
}

func TestShimPayloadParkedBeforeStopRefusesAfterStopBeginsWithoutPTYWrite(t *testing.T) {
	spec, _ := harness.Lookup("codex")
	writer := &recordingOperationWriter{}
	firstPayloadStarted := make(chan struct{})
	releaseFirstPayload := make(chan struct{})
	var firstPayload sync.Once
	childPID := 456
	attempted, exited := true, true
	signalName := "SIGHUP"
	handler := &requestHandler{
		session: "fleet", role: "planner", shimPID: 123, childPID: childPID, ready: true,
		operations: newOperationExecutor(spec, newRoleInputWriter(writer), func(context.Context, time.Duration) error {
			firstPayload.Do(func() { close(firstPayloadStarted) })
			<-releaseFirstPayload
			return nil
		}),
		stop: func(context.Context) (Response, error) {
			return Response{
				Version: 1, Outcome: OutcomeStopChildExited, ChildPID: &childPID,
				SignalAttempted: &attempted, Signal: &signalName, ChildExitObserved: &exited,
			}, nil
		},
	}

	activeClient, activeDone := beginHandlerRequest(t, handler, "clear")
	<-firstPayloadStarted
	parkedClient, parkedDone := beginHandlerRequest(t, handler, "compact")
	if err := parkedClient.SetReadDeadline(time.Now().Add(25 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline(parked payload) error = %v", err)
	}
	var unexpected [1]byte
	if _, err := parkedClient.Read(unexpected[:]); err == nil {
		t.Fatal("parked payload completed while the active payload held the operation gate")
	} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("read parked payload response error = %v, want timeout while parked", err)
	}
	if err := parkedClient.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("clear parked payload read deadline: %v", err)
	}

	stopClient, stopDone := beginHandlerRequest(t, handler, "stop")
	waitForShimOperationPhase(t, handler, shimOperationStopping)
	close(releaseFirstPayload)
	if response := readHandlerResponse(t, activeClient, activeDone); response.Outcome != OutcomeDeliverySubmitted {
		t.Fatalf("active payload response = %#v, want delivery-submitted", response)
	}
	parkedResponse := readHandlerResponse(t, parkedClient, parkedDone)
	if parkedResponse.Outcome != OutcomeShimStopping || parkedResponse.State == nil || *parkedResponse.State != "stopping" {
		t.Fatalf("parked payload response = %#v, want shim-stopping/stopping refusal", parkedResponse)
	}
	if response := readHandlerResponse(t, stopClient, stopDone); response.Outcome != OutcomeStopChildExited {
		t.Fatalf("stop response = %#v, want stop-child-exited", response)
	}

	writer.mu.Lock()
	writes := append([][]byte(nil), writer.calls...)
	writer.mu.Unlock()
	if want := [][]byte{{0x15}, []byte("/clear"), {'\r'}}; !reflect.DeepEqual(writes, want) {
		t.Fatalf("PTY writes = %#v, want only the active payload %#v", writes, want)
	}
}

func TestShimRetainedStopStaysStoppingUntilItsResponseIsReported(t *testing.T) {
	childPID := 456
	attempted, exited := true, false
	signalName := "SIGHUP"
	state := string(ProcessPresentMatch)
	stopReturned := make(chan struct{})
	handler := &requestHandler{
		session: "fleet", role: "planner", shimPID: 123, childPID: childPID, ready: true,
		stop: func(context.Context) (Response, error) {
			close(stopReturned)
			return Response{
				Version: 1, Outcome: OutcomeStopChildRetained, ChildPID: &childPID,
				SignalAttempted: &attempted, Signal: &signalName, ChildExitObserved: &exited, State: &state,
			}, nil
		},
	}

	stopClient, stopDone := beginHandlerRequest(t, handler, "stop")
	<-stopReturned
	time.Sleep(25 * time.Millisecond)
	if got := handler.operationPhase(); got != shimOperationStopping {
		t.Fatalf("operation phase before retained stop response read = %q, want stopping", got)
	}
	response := readHandlerResponse(t, stopClient, stopDone)
	if response.Outcome != OutcomeStopChildRetained {
		t.Fatalf("retained stop response = %#v", response)
	}
	if got := handler.operationPhase(); got != shimOperationActive {
		t.Fatalf("operation phase after retained stop response = %q, want active", got)
	}
}

func TestShimStopCompletesCleanupWhenClientAbortsAfterObservedChildExit(t *testing.T) {
	childPID := 456
	attempted, exited := true, true
	signalName := "SIGHUP"
	stopComplete := make(chan struct{}, 1)
	stopEntered := make(chan struct{})
	releaseStop := make(chan struct{})
	handler := &requestHandler{
		session: "fleet", role: "planner", shimPID: 123, childPID: childPID, ready: true,
		stop: func(context.Context) (Response, error) {
			close(stopEntered)
			<-releaseStop
			return Response{
				Version: 1, Outcome: OutcomeStopChildExited, ChildPID: &childPID,
				SignalAttempted: &attempted, Signal: &signalName, ChildExitObserved: &exited,
			}, nil
		},
		stopComplete: func() { stopComplete <- struct{}{} },
	}
	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- handler.handleConnection(context.Background(), server) }()
	if _, err := ReadFrame(client); err != nil {
		t.Fatalf("ReadFrame(hello) error = %v", err)
	}
	request, _ := EncodeRequest(Request{Version: 1, Session: "fleet", Role: "planner", Operation: "stop"})
	if _, err := WriteFrame(client, request); err != nil {
		t.Fatalf("WriteFrame(stop) error = %v", err)
	}
	<-stopEntered
	_ = client.Close()
	close(releaseStop)
	err := <-done
	_ = server.Close()
	var abort *ProtocolPeerAbortError
	if !errors.As(err, &abort) || abort.Direction != ProtocolFrameWrite {
		t.Fatalf("handleConnection() error = %T %v, want response-write peer abort", err, err)
	}
	select {
	case <-stopComplete:
	case <-time.After(time.Second):
		t.Fatal("observed child exit did not complete stop cleanup after response abort")
	}
}

func beginHandlerRequest(t *testing.T, handler *requestHandler, operation string) (net.Conn, <-chan error) {
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
	request, err := EncodeRequest(Request{Version: 1, Session: "fleet", Role: "planner", Operation: operation})
	if err != nil {
		t.Fatalf("EncodeRequest(%q) error = %v", operation, err)
	}
	if _, err := WriteFrame(client, request); err != nil {
		t.Fatalf("WriteFrame(%q request) error = %v", operation, err)
	}
	return client, done
}

func readHandlerResponse(t *testing.T, client net.Conn, done <-chan error) Response {
	t.Helper()
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

func TestShimHandlerClassifiesConnectedClientFrameTransportFailures(t *testing.T) {
	for _, test := range []struct {
		name      string
		direction ProtocolFrameDirection
		wrap      func(net.Conn) net.Conn
		complete  func(*testing.T, net.Conn)
	}{
		{
			name: "request read", direction: ProtocolFrameRead,
			complete: func(t *testing.T, client net.Conn) {
				if _, err := ReadFrame(client); err != nil {
					t.Fatalf("ReadFrame(hello) error = %v", err)
				}
				if _, err := client.Write([]byte{0, 0}); err != nil {
					t.Fatalf("write partial request header: %v", err)
				}
			},
		},
		{
			name: "response write", direction: ProtocolFrameWrite,
			wrap: func(connection net.Conn) net.Conn {
				return &failNthWriteConn{Conn: connection, failAt: 2, err: errors.New("injected response write failure")}
			},
			complete: func(t *testing.T, client net.Conn) {
				if _, err := ReadFrame(client); err != nil {
					t.Fatalf("ReadFrame(hello) error = %v", err)
				}
				request, err := EncodeRequest(Request{Version: 1, Session: "fleet", Role: "planner", Operation: "observe"})
				if err != nil {
					t.Fatalf("EncodeRequest() error = %v", err)
				}
				if _, err := WriteFrame(client, request); err != nil {
					t.Fatalf("WriteFrame(request) error = %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, server := net.Pipe()
			state := "running"
			shimPID, childPID := 123, 456
			handler := &requestHandler{
				session: "fleet", role: "planner", ready: true, shimPID: shimPID, childPID: childPID,
				observe: func() Response {
					return Response{Version: 1, Outcome: OutcomeRunning, State: &state, ShimPID: &shimPID, ChildPID: &childPID}
				},
			}
			handlerConnection := net.Conn(server)
			if test.wrap != nil {
				handlerConnection = test.wrap(server)
			}
			done := make(chan error, 1)
			go func() { done <- handler.handleConnection(context.Background(), handlerConnection) }()
			test.complete(t, client)
			if test.direction == ProtocolFrameRead {
				_ = client.Close()
			}
			err := <-done
			_ = server.Close()
			var frame *ProtocolFrameError
			if !errors.As(err, &frame) || frame.Peer != ProtocolPeerClient || frame.Direction != test.direction {
				t.Fatalf("handleConnection() error = %T %v, want %s-to-client ProtocolFrameError", err, err, test.direction)
			}
		})
	}
}

func TestShimHandlerTreatsEveryHelloWriteFailureAsFatal(t *testing.T) {
	for _, progress := range []int{0, 2} {
		t.Run(fmt.Sprintf("progress-%d", progress), func(t *testing.T) {
			client, server := net.Pipe()
			defer func() { _ = client.Close(); _ = server.Close() }()
			connection := &progressWriteFailureConn{Conn: server, progress: progress, err: syscall.EPIPE}
			err := (&requestHandler{}).handleConnection(context.Background(), connection)
			var frame *ProtocolFrameError
			var abort *ProtocolPeerAbortError
			if !errors.As(err, &frame) || errors.As(err, &abort) || frame.Direction != ProtocolFrameWrite || frame.Peer != ProtocolPeerClient {
				t.Fatalf("hello write error = %T %v, want fatal write-to-client ProtocolFrameError", err, err)
			}
		})
	}
}

func TestShimStopLeavesNonClosureResponseFailureForFatalCleanupPath(t *testing.T) {
	childPID := 456
	attempted, exited := true, true
	signalName := "SIGHUP"
	stopComplete := make(chan struct{}, 1)
	handler := &requestHandler{
		session: "fleet", role: "planner", shimPID: 123, childPID: childPID, ready: true,
		stop: func(context.Context) (Response, error) {
			return Response{
				Version: 1, Outcome: OutcomeStopChildExited, ChildPID: &childPID,
				SignalAttempted: &attempted, Signal: &signalName, ChildExitObserved: &exited,
			}, nil
		},
		stopComplete: func() { stopComplete <- struct{}{} },
	}
	client, server := net.Pipe()
	connection := &failNthWriteConn{Conn: server, failAt: 2, err: errors.New("injected non-closure response failure")}
	done := make(chan error, 1)
	go func() { done <- handler.handleConnection(context.Background(), connection) }()
	if _, err := ReadFrame(client); err != nil {
		t.Fatalf("ReadFrame(hello) error = %v", err)
	}
	request, _ := EncodeRequest(Request{Version: 1, Session: "fleet", Role: "planner", Operation: "stop"})
	if _, err := WriteFrame(client, request); err != nil {
		t.Fatalf("WriteFrame(stop) error = %v", err)
	}
	err := <-done
	_ = client.Close()
	_ = server.Close()
	var frame *ProtocolFrameError
	if !errors.As(err, &frame) || frame.Direction != ProtocolFrameWrite {
		t.Fatalf("handleConnection() error = %T %v, want fatal response-write ProtocolFrameError", err, err)
	}
	select {
	case <-stopComplete:
		t.Fatal("non-closure response failure raced stopComplete against fatal cleanup")
	case <-time.After(25 * time.Millisecond):
	}
}

func TestShimServeConnectionsSurfacesHandlerProtocolFailure(t *testing.T) {
	client, server := net.Pipe()
	listener := &singleConnectionRoleListener{connection: server, closed: make(chan struct{})}
	fatal := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go NewServer(nil).serveConnections(ctx, listener, &requestHandler{}, fatal)
	if _, err := ReadFrame(client); err != nil {
		t.Fatalf("ReadFrame(hello) error = %v", err)
	}
	if _, err := client.Write([]byte{0, 0}); err != nil {
		t.Fatalf("write partial request header: %v", err)
	}
	_ = client.Close()
	select {
	case err := <-fatal:
		var frame *ProtocolFrameError
		if !errors.As(err, &frame) || frame.Peer != ProtocolPeerClient || frame.Direction != ProtocolFrameRead {
			t.Fatalf("serveConnections() error = %T %v, want read-from-client ProtocolFrameError", err, err)
		}
	case <-time.After(time.Second):
		t.Fatal("serveConnections discarded handler protocol failure")
	}
	_ = listener.Close()
	_ = server.Close()
}

func TestShimServeConnectionsKeepsResidentHandlerAfterExpectedClientAbort(t *testing.T) {
	for _, phase := range []string{"pre-request-refusal", "request-cancellation"} {
		t.Run(phase, func(t *testing.T) {
			listener := &channelRoleListener{connections: make(chan net.Conn, 2), closed: make(chan struct{})}
			fatal := make(chan error, 1)
			state := "running"
			shimPID, childPID := 123, 456
			firstObserveEntered := make(chan struct{})
			releaseFirstObserve := make(chan struct{})
			observeCalls := 0
			handler := &requestHandler{
				session: "fleet", role: "planner", ready: true, shimPID: shimPID, childPID: childPID,
				observe: func() Response {
					observeCalls++
					if phase == "request-cancellation" && observeCalls == 1 {
						close(firstObserveEntered)
						<-releaseFirstObserve
					}
					return Response{Version: 1, Outcome: OutcomeRunning, State: &state, ShimPID: &shimPID, ChildPID: &childPID}
				},
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go NewServer(nil).serveConnections(ctx, listener, handler, fatal)

			abortClient, abortServer := net.Pipe()
			listener.connections <- abortServer
			if _, err := ReadFrame(abortClient); err != nil {
				t.Fatalf("ReadFrame(abort hello) error = %v", err)
			}
			if phase == "request-cancellation" {
				request, _ := EncodeRequest(Request{Version: 1, Session: "fleet", Role: "planner", Operation: "observe"})
				if _, err := WriteFrame(abortClient, request); err != nil {
					t.Fatalf("WriteFrame(cancelled request) error = %v", err)
				}
				<-firstObserveEntered
			}
			_ = abortClient.Close()
			if phase == "request-cancellation" {
				close(releaseFirstObserve)
			}

			select {
			case err := <-fatal:
				t.Fatalf("expected %s peer abort became fatal: %v", phase, err)
			case <-time.After(25 * time.Millisecond):
			}

			validClient, validServer := net.Pipe()
			listener.connections <- validServer
			if _, err := ReadFrame(validClient); err != nil {
				t.Fatalf("ReadFrame(valid hello) error = %v", err)
			}
			request, _ := EncodeRequest(Request{Version: 1, Session: "fleet", Role: "planner", Operation: "observe"})
			if _, err := WriteFrame(validClient, request); err != nil {
				t.Fatalf("WriteFrame(valid request) error = %v", err)
			}
			response, err := ReadFrame(validClient)
			if err != nil {
				t.Fatalf("ReadFrame(valid response) error = %v", err)
			}
			decoded, err := DecodeResponse(response)
			if err != nil || decoded.Outcome != OutcomeRunning {
				t.Fatalf("valid response = %#v, %v; resident handler did not survive", decoded, err)
			}
			_ = validClient.Close()
			_ = listener.Close()
		})
	}
}

func TestShimResidentSurvivesRealClientGuardRefusalAndCancellation(t *testing.T) {
	base := shortTempDir(t)
	namespace, err := openNamespaceRoots(namespaceRoots{Runtime: base + "/runtime", State: base + "/state"})
	if err != nil {
		t.Fatalf("openNamespaceRoots() error = %v", err)
	}
	t.Cleanup(func() { _ = namespace.Close() })
	path, err := namespace.RolePath("fleet", "planner")
	if err != nil {
		t.Fatalf("RolePath() error = %v", err)
	}
	t.Cleanup(func() { _ = path.Close() })
	claim, err := AcquireClaim(path, Advisory{Version: 1, ShimPID: os.Getpid(), Nonce: "nonce", StateRoot: path.StateRoot})
	if err != nil {
		t.Fatalf("AcquireClaim() error = %v", err)
	}
	t.Cleanup(func() { _ = claim.Close() })
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path.Socket, Net: "unix"})
	if err != nil {
		t.Fatalf("ListenUnix() error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	state := "running"
	shimPID, childPID := os.Getpid(), 456
	blockObserve := make(chan chan struct{}, 1)
	observeEntered := make(chan struct{}, 1)
	handler := &requestHandler{
		session: "fleet", role: "planner", ready: true, shimPID: shimPID, childPID: childPID,
		observe: func() Response {
			select {
			case release := <-blockObserve:
				observeEntered <- struct{}{}
				<-release
			default:
			}
			return Response{Version: 1, Outcome: OutcomeRunning, State: &state, ShimPID: &shimPID, ChildPID: &childPID}
		},
	}
	serverCtx, stopServer := context.WithCancel(context.Background())
	t.Cleanup(stopServer)
	fatal := make(chan error, 1)
	go NewServer(nil).serveConnections(serverCtx, listener, handler, fatal)
	client := NewClient(namespace)

	wantGuard := errors.New("ancestry refused")
	if _, err := client.DeliverOperationGuarded(context.Background(), "fleet", "planner", "clear", func(context.Context, int) error { return wantGuard }); !errors.Is(err, wantGuard) {
		t.Fatalf("DeliverOperationGuarded() error = %T %v, want guard refusal", err, err)
	}
	assertNoServerFatal(t, fatal, "guard refusal")
	if response, err := client.Observe(context.Background(), "fleet", "planner"); err != nil || response.Outcome != OutcomeRunning {
		t.Fatalf("Observe() after guard refusal = %#v, %v; resident did not survive", response, err)
	}

	releaseObserve := make(chan struct{})
	blockObserve <- releaseObserve
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	requestDone := make(chan error, 1)
	connected := make(chan *net.UnixConn, 1)
	cancelClient := NewClient(namespace)
	cancelClient.dial = func(ctx context.Context, socket string) (*net.UnixConn, error) {
		connection, dialErr := dialRoleSocket(ctx, socket)
		if dialErr == nil {
			connected <- connection
		}
		return connection, dialErr
	}
	go func() {
		_, requestErr := cancelClient.Observe(requestCtx, "fleet", "planner")
		requestDone <- requestErr
	}()
	connection := <-connected
	<-observeEntered
	cancelRequest()
	closeDeadline := time.Now().Add(time.Second)
	connectionClosed := false
	for time.Now().Before(closeDeadline) {
		if connection.SetDeadline(time.Time{}) != nil {
			connectionClosed = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !connectionClosed {
		t.Fatal("cancelled client connection did not close")
	}
	close(releaseObserve)
	if err := <-requestDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Observe() error = %T %v, want context.Canceled", err, err)
	}
	assertNoServerFatal(t, fatal, "request cancellation")
	if response, err := client.Observe(context.Background(), "fleet", "planner"); err != nil || response.Outcome != OutcomeRunning {
		t.Fatalf("Observe() after cancellation = %#v, %v; resident did not survive", response, err)
	}
}

func assertNoServerFatal(t *testing.T, fatal <-chan error, phase string) {
	t.Helper()
	select {
	case err := <-fatal:
		t.Fatalf("%s became fatal: %v", phase, err)
	case <-time.After(25 * time.Millisecond):
	}
}

type singleConnectionRoleListener struct {
	connection net.Conn
	closed     chan struct{}
	once       sync.Once
	accepted   bool
}

type channelRoleListener struct {
	connections chan net.Conn
	closed      chan struct{}
	once        sync.Once
}

func (l *channelRoleListener) Accept() (net.Conn, error) {
	select {
	case connection := <-l.connections:
		return connection, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *channelRoleListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

type failNthWriteConn struct {
	net.Conn
	writes int
	failAt int
	err    error
}

type toggleResponseFailureListener struct {
	roleListener
	mu      sync.RWMutex
	enabled bool
	err     error
}

func (l *toggleResponseFailureListener) EnableFailure() {
	l.mu.Lock()
	l.enabled = true
	l.mu.Unlock()
}

func (l *toggleResponseFailureListener) Accept() (net.Conn, error) {
	connection, err := l.roleListener.Accept()
	if err != nil {
		return nil, err
	}
	l.mu.RLock()
	enabled := l.enabled
	l.mu.RUnlock()
	if enabled {
		return &failNthWriteConn{Conn: connection, failAt: 2, err: l.err}, nil
	}
	return connection, nil
}

type progressWriteFailureConn struct {
	net.Conn
	progress int
	err      error
}

func (c *progressWriteFailureConn) Write([]byte) (int, error) {
	return c.progress, c.err
}

func (c *failNthWriteConn) Write(payload []byte) (int, error) {
	c.writes++
	if c.writes == c.failAt {
		return 0, c.err
	}
	return c.Conn.Write(payload)
}

func (l *singleConnectionRoleListener) Accept() (net.Conn, error) {
	if !l.accepted {
		l.accepted = true
		return l.connection, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *singleConnectionRoleListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func waitForShimOperationPhase(t *testing.T, handler *requestHandler, want shimOperationPhase) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if handler.operationPhase() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("operation phase = %q, want %q", handler.operationPhase(), want)
}

func TestShimServerVersionAndRegistryRefusalsPrecedeMutation(t *testing.T) {
	spec, _ := harness.Lookup("codex")
	writer := &recordingOperationWriter{}
	handler := &requestHandler{
		session: "fleet", role: "planner", shimPID: 123, childPID: 456, ready: true,
		operations: newOperationExecutor(spec, newRoleInputWriter(writer), nil),
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

func TestShimRuntimeCleanupPersistsCleanupFailedFactsBeforeReleasingClaim(t *testing.T) {
	path := newTestRolePath(t)
	claim, err := AcquireClaim(path, Advisory{Version: 1, ShimPID: os.Getpid(), Nonce: "cleanup", StateRoot: path.StateRoot})
	if err != nil {
		t.Fatal(err)
	}
	token := StartToken{Sec: 1, Usec: 2}
	record, err := NewChildStartingRecord(path.Session, path.Role, os.Getpid(), "cleanup").WithChild(456, token)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteRecord(path, record); err != nil {
		t.Fatal(err)
	}
	runtime := &roleRuntime{path: path, claim: claim, record: record, child: &stopRaceChild{pid: 456}}
	server := &Server{
		cleanupTimeout: -time.Nanosecond,
		observeProcess: func(int, StartToken) ProcessResult {
			return ProcessResult{Observation: ProcessPresentMatch}
		},
	}

	result := server.cleanupRuntime(runtime, false)
	if result.Observation != ProcessPresentMatch || !reflect.DeepEqual(result.Remaining, []string{"child", "record", "lock"}) {
		t.Fatalf("cleanupRuntime() = %#v, want retained child/record/lock facts", result)
	}
	payload, err := os.ReadFile(path.Record)
	if err != nil {
		t.Fatal(err)
	}
	var got Record
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if err := validateRecord(got); err != nil {
		t.Fatalf("persisted cleanup record invalid: %v", err)
	}
	if got.State != RecordStateCleanupFailed || got.Cleanup == nil || got.Cleanup.Observation != CleanupObservationPresentMatch || !reflect.DeepEqual(got.Cleanup.Remaining, result.Remaining) {
		t.Fatalf("persisted cleanup record = %#v, want exact cleanup-failed facts", got)
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
	executor := newOperationExecutor(spec, newRoleInputWriter(writer), func(_ context.Context, duration time.Duration) error {
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
	executor := newOperationExecutor(spec, newRoleInputWriter(writer), nil)

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
	executor := newOperationExecutor(spec, newRoleInputWriter(writer), func(context.Context, time.Duration) error {
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
		operations: newOperationExecutor(spec, newRoleInputWriter(writer), func(ctx context.Context, _ time.Duration) error {
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
		operations: newOperationExecutor(spec, newRoleInputWriter(writer), nil),
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
	executor := newOperationExecutor(spec, newRoleInputWriter(writer), func(context.Context, time.Duration) error { return nil })

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

func TestShimViewerInputCannotSplitDeliveryTransaction(t *testing.T) {
	spec, _ := harness.Lookup("codex")
	writer := &recordingOperationWriter{}
	input := newRoleInputWriter(writer)
	waiting := make(chan struct{})
	releaseWait := make(chan struct{})
	executor := newOperationExecutor(spec, input, func(context.Context, time.Duration) error {
		close(waiting)
		<-releaseWait
		return nil
	})

	deliveryDone := make(chan error, 1)
	go func() {
		_, err := executor.Deliver(context.Background(), "clear")
		deliveryDone <- err
	}()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("delivery did not reach its transaction wait")
	}
	viewerDone := make(chan error, 1)
	go func() {
		_, err := input.WriteViewer(context.Background(), []byte("viewer"))
		viewerDone <- err
	}()
	select {
	case err := <-viewerDone:
		t.Fatalf("viewer input split the delivery transaction: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseWait)
	if err := <-deliveryDone; err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if err := <-viewerDone; err != nil {
		t.Fatalf("WriteViewer() error = %v", err)
	}
	if got, want := writer.calls, [][]byte{{0x15}, []byte("/clear"), {'\r'}, []byte("viewer")}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PTY writes = %#v, want indivisible delivery followed by viewer chunk %#v", got, want)
	}
}

func TestShimViewerInputRechecksCancellationAndRolePhaseAtCommit(t *testing.T) {
	writer := &recordingOperationWriter{}
	active := true
	input := newRoleInputWriter(writer, func() bool { return active })

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := input.WriteViewer(cancelled, []byte("departed")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled WriteViewer() error = %v, want context.Canceled", err)
	}
	active = false
	if count, err := input.WriteViewer(context.Background(), []byte("stopping")); err != nil || count != len("stopping") {
		t.Fatalf("stopping WriteViewer() = %d, %v, want consumed discard", count, err)
	}
	if len(writer.calls) != 0 {
		t.Fatalf("disallowed viewer bytes reached PTY: %#v", writer.calls)
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

type serverResidentReader struct{}

func (*serverResidentReader) Read(ctx context.Context, _ []byte) (int, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

type serverTestResidentRelay struct{ *ptyx.ResidentRelay }

func (*serverTestResidentRelay) MarkReady(ptyx.TerminalState) error { return nil }
func (r *serverTestResidentRelay) Writer() operationWriter          { return r.ResidentRelay.Writer() }

func waitServerRunning(t *testing.T, client *Client, session, role string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		response, err := client.Observe(context.Background(), session, role)
		if err == nil && response.Outcome == OutcomeRunning {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not become ready: response=%#v error=%v", response, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
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
