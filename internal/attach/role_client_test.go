//go:build darwin

package attach

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mnbf9rca/agentctl/internal/fleet"
	"github.com/mnbf9rca/agentctl/internal/ptyx"
	"github.com/mnbf9rca/agentctl/internal/shim"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestRoleTransportPreservesVerbatimOutputAndReturnsExactFinalCounters(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	output := &recordingContextWriter{}
	serveDone := make(chan error, 1)
	go func() {
		defer func() { _ = server.Close() }()
		if err := writeAttachControlForClient(server, shim.AttachControl{Version: 1, Kind: shim.AttachControlShimHello}); err != nil {
			serveDone <- err
			return
		}
		frame, err := shim.ReadAttachFrame(server)
		if err != nil {
			serveDone <- err
			return
		}
		hello, err := shim.DecodeAttachControl(frame.Data)
		if err != nil || hello.Kind != shim.AttachControlHello || hello.Session != "fleet" || hello.Role != "planner" || hello.Rows != 24 || hello.Cols != 80 {
			serveDone <- errors.New("wrong client hello")
			return
		}
		if err := writeAttachControlForClient(server, shim.AttachControl{Version: 1, Kind: shim.AttachControlAdmitted}); err != nil {
			serveDone <- err
			return
		}
		if err := shim.WriteAttachFrame(server, shim.AttachFrame{Kind: shim.AttachFrameRoleOutput, Data: []byte{0, 'a', '\r', '\n', 0xff}}); err != nil {
			serveDone <- err
			return
		}
		serveDone <- writeAttachControlForClient(server, shim.AttachControl{Version: 1, Kind: shim.AttachControlFinal, Disposition: shim.AttachDispositionChildExited, Bytes: 5})
	}()

	result, err := runRoleTransport(context.Background(), client, relayTerminal{input: blockingContextReader{}, output: output}, ptyx.WindowSize{Rows: 24, Cols: 80}, "fleet", "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
	if result.Disposition != shim.AttachDispositionChildExited || result.Bytes != 5 || !bytes.Equal(output.Bytes(), []byte{0, 'a', '\r', '\n', 0xff}) {
		t.Fatalf("result=%#v output=%v", result, output.Bytes())
	}
}

func TestRoleTransportValidatesConnectedAttachPeerAfterShimHelloBeforeClientHello(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	want := &RoleObservationError{Observation: fleet.ShimRoleObservation{Outcome: shim.OutcomeAnswererDisagreement, ShimPID: 41, AnswererPID: 42}}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- writeAttachControlForClient(server, shim.AttachControl{Version: 1, Kind: shim.AttachControlShimHello})
	}()
	_, err := runRoleTransportVerified(context.Background(), client, relayTerminal{}, ptyx.WindowSize{Rows: 24, Cols: 80}, "fleet", "planner", func() error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("error=%T %v, want connected-peer refusal", err, err)
	}
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	if _, err := shim.ReadAttachFrame(server); !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.EOF) {
		t.Fatalf("frame after peer refusal error=%v, want no client hello", err)
	}
}

func TestRoleTransportMapsConnectedPeerObservationFailureBeforeClientHello(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	want := errors.New("LOCAL_PEERPID failed")
	roleClient := &RoleClient{peerPID: func(net.Conn) (int, error) { return 0, want }}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- writeAttachControlForClient(server, shim.AttachControl{Version: 1, Kind: shim.AttachControlShimHello})
	}()
	_, err := runRoleTransportVerified(
		context.Background(), client, relayTerminal{}, ptyx.WindowSize{Rows: 24, Cols: 80}, "fleet", "planner",
		roleClient.validateConnectedPeer(client, roleTarget{expectedShimPID: 41}),
	)
	var refusal *RefusalErrorRole
	if !errors.As(err, &refusal) || refusal.Control.Outcome != shim.AttachRefusalPeerUnobservable || refusal.Control.Cause != want.Error() {
		t.Fatalf("error=%T %v, want attach-peer-unobservable carrying %q", err, err, want)
	}
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	if _, err := shim.ReadAttachFrame(server); !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.EOF) {
		t.Fatalf("frame after peer observation failure error=%v, want no client hello", err)
	}
}

func TestRoleTransportInputEOFWakesQuietShimAndReleasesConnection(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	go func() {
		_ = writeAttachControlForClient(server, shim.AttachControl{Version: 1, Kind: shim.AttachControlShimHello})
		_, _ = shim.ReadAttachFrame(server)
		_ = writeAttachControlForClient(server, shim.AttachControl{Version: 1, Kind: shim.AttachControlAdmitted})
	}()
	done := make(chan error, 1)
	go func() {
		_, err := runRoleTransport(context.Background(), client, relayTerminal{input: eofContextReader{}, output: &recordingContextWriter{}}, ptyx.WindowSize{Rows: 24, Cols: 80}, "fleet", "planner")
		done <- err
	}()
	select {
	case err := <-done:
		var transport *TransportError
		if !errors.As(err, &transport) || transport.Phase != "relay" {
			t.Fatalf("error=%T %v, want bounded relay termination", err, err)
		}
	case <-time.After(500 * time.Millisecond):
		_ = client.Close()
		t.Fatal("terminal input EOF retained the quiet attach connection")
	}
}

func TestRoleTransportResizeErrorWakesQuietShimAndReleasesConnection(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	admitted := make(chan struct{})
	go func() {
		_ = writeAttachControlForClient(server, shim.AttachControl{Version: 1, Kind: shim.AttachControlShimHello})
		_, _ = shim.ReadAttachFrame(server)
		_ = writeAttachControlForClient(server, shim.AttachControl{Version: 1, Kind: shim.AttachControlAdmitted})
		close(admitted)
	}()
	want := errors.New("window observation failed")
	done := make(chan error, 1)
	go func() {
		_, err := runRoleTransport(context.Background(), client, relayTerminal{
			input: blockingContextReader{}, output: &recordingContextWriter{},
			size: func() (ptyx.WindowSize, error) { return ptyx.WindowSize{}, want },
		}, ptyx.WindowSize{Rows: 24, Cols: 80}, "fleet", "planner")
		done <- err
	}()
	<-admitted
	deadline := time.After(500 * time.Millisecond)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			var transport *TransportError
			if !errors.As(err, &transport) || !errors.Is(transport.Cause, want) {
				t.Fatalf("error=%T %v, want resize observation failure", err, err)
			}
			return
		case <-ticker.C:
			_ = syscall.Kill(os.Getpid(), syscall.SIGWINCH)
		case <-deadline:
			_ = client.Close()
			t.Fatal("resize observation failure retained the quiet attach connection")
		}
	}
}

func TestRolePreflightUsesConfiguredTmuxModeAndSeparatesObservedPresence(t *testing.T) {
	for _, test := range []struct {
		name          string
		present       bool
		wantPresented bool
	}{
		{name: "observed presentation", present: true, wantPresented: true},
		{name: "missing presentation", present: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			preflight := rolePreflight{
				records:      roleFleetReaderStub{record: fleet.ShimFleetRecord{Presentation: fleet.PresentationTmux, Roster: []string{"planner"}, Roles: map[string]fleet.ShimFleetRoleRecord{"planner": {Harness: "claude"}}}},
				inspector:    roleInspectorStub{observation: fleet.ShimRoleObservation{Outcome: shim.OutcomeRunning}},
				presentation: presentationObserverStub{present: test.present},
			}
			_, err := preflight.prepare(context.Background(), "fleet", "planner")
			if test.wantPresented {
				var presented *PresentedByTmuxError
				if !errors.As(err, &presented) {
					t.Fatalf("error=%T %v", err, err)
				}
			} else {
				var missing *PresentationMissingError
				if !errors.As(err, &missing) {
					t.Fatalf("error=%T %v", err, err)
				}
			}
		})
	}
}

func TestClientOutputQueueCountsQueuedPlusInFlightAtExactBoundary(t *testing.T) {
	queue := newClientOutputQueue(AttachClientQueueBytes)
	chunk := make([]byte, AttachClientQueueBytes/2)
	if err := queue.enqueue(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}
	inFlight, ok, err := queue.take(context.Background())
	if err != nil || !ok || len(inFlight) != len(chunk) {
		t.Fatalf("take=%d/%t/%v", len(inFlight), ok, err)
	}
	if err := queue.enqueue(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := queue.enqueue(ctx, []byte{1}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("one byte above queued+inflight boundary error=%v", err)
	}
	queue.complete(len(inFlight))
	if err := queue.enqueue(context.Background(), []byte{1}); err != nil {
		t.Fatalf("enqueue after in-flight completion: %v", err)
	}
}

func TestClientRawCounterOverflowIsProtocolFailureNotInventedFinalDisposition(t *testing.T) {
	if next, err := advanceAttachRawCounter(attachCounterMax-2, 2); err != nil || next != attachCounterMax {
		t.Fatalf("exact boundary=%d/%v, want %d", next, err, attachCounterMax)
	}
	if _, err := advanceAttachRawCounter(attachCounterMax-2, 3); err == nil {
		t.Fatal("one byte above client RAW boundary was not rejected")
	}
}

func TestDrainClientOutputCapturesWriterPanicForOwnerRestoration(t *testing.T) {
	queue := newClientOutputQueue(8)
	if err := queue.enqueue(context.Background(), []byte("panic")); err != nil {
		t.Fatal(err)
	}
	queue.close()
	result := drainClientOutput(context.Background(), panicContextWriter{value: "writer panic"}, queue, 100*time.Millisecond)
	var worker *workerPanicError
	if !errors.As(result.err, &worker) || worker.Value != "writer panic" {
		t.Fatalf("error=%T %v, want captured writer panic", result.err, result.err)
	}
}

func TestRelayWorkerCapturesPanicsForOwnerArbitration(t *testing.T) {
	err := runRelayWorker(func() error { panic("relay panic") })
	var worker *workerPanicError
	if !errors.As(err, &worker) || worker.Value != "relay panic" {
		t.Fatalf("error=%T %v, want captured relay panic", err, err)
	}
}

func TestRoleOutputPropagatesCapturedWriterPanicWithoutReclassifyingIt(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	go func() {
		_ = shim.WriteAttachFrame(server, shim.AttachFrame{Kind: shim.AttachFrameRoleOutput, Data: []byte("panic")})
	}()
	_, err := relayRoleOutputWithTimeout(context.Background(), client, panicContextWriter{value: "writer panic"}, admittedAttachSequence(t), 100*time.Millisecond)
	var worker *workerPanicError
	if !errors.As(err, &worker) || worker.Value != "writer panic" {
		t.Fatalf("error=%T %v, want captured worker panic", err, err)
	}
	var output *TerminalOutputError
	if errors.As(err, &output) {
		t.Fatalf("worker panic was reclassified as terminal output failure: %#v", output)
	}
}

func TestRoleOutputStopsWithinBoundWithQueueFullAndChunkInFlight(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	go func() {
		defer func() { _ = server.Close() }()
		chunk := make([]byte, AttachClientQueueBytes/2)
		_ = shim.WriteAttachFrame(server, shim.AttachFrame{Kind: shim.AttachFrameRoleOutput, Data: chunk})
		_ = shim.WriteAttachFrame(server, shim.AttachFrame{Kind: shim.AttachFrameRoleOutput, Data: chunk})
		_ = shim.WriteAttachFrame(server, shim.AttachFrame{Kind: shim.AttachFrameRoleOutput, Data: []byte{1}})
	}()
	started := time.Now()
	_, err := relayRoleOutputWithTimeout(context.Background(), client, neverDrainingWriter{}, admittedAttachSequence(t), 30*time.Millisecond)
	var output *TerminalOutputError
	if !errors.As(err, &output) || !output.Stalled || output.Raw != AttachClientQueueBytes+1 || output.Written != 0 {
		t.Fatalf("error=%T %#v", err, output)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("bounded relay took %s", elapsed)
	}
}

func TestRoleOutputStopsWithinBoundWithOnlyChunkInFlight(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	go func() {
		defer func() { _ = server.Close() }()
		_ = shim.WriteAttachFrame(server, shim.AttachFrame{Kind: shim.AttachFrameRoleOutput, Data: []byte("abc")})
		_ = writeAttachControlForClient(server, shim.AttachControl{Version: 1, Kind: shim.AttachControlFinal, Disposition: shim.AttachDispositionChildExited, Bytes: 3})
	}()
	started := time.Now()
	_, err := relayRoleOutputWithTimeout(context.Background(), client, neverDrainingWriter{}, admittedAttachSequence(t), 30*time.Millisecond)
	var output *TerminalOutputError
	if !errors.As(err, &output) || !output.Stalled || output.Raw != 3 || output.Written != 0 || output.Prior == nil || output.Prior.Disposition != shim.AttachDispositionChildExited {
		t.Fatalf("error=%T %#v", err, output)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("bounded relay took %s", elapsed)
	}
}

func TestRoleOutputStopsWithinBoundWithSmallChunkAndQuietShim(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	frameWritten := make(chan struct{})
	go func() {
		_ = shim.WriteAttachFrame(server, shim.AttachFrame{Kind: shim.AttachFrameRoleOutput, Data: []byte("small")})
		close(frameWritten)
	}()
	started := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := relayRoleOutputWithTimeout(context.Background(), client, neverDrainingWriter{}, admittedAttachSequence(t), 30*time.Millisecond)
		done <- err
	}()
	<-frameWritten
	select {
	case err := <-done:
		var output *TerminalOutputError
		if !errors.As(err, &output) || !output.Stalled || output.Raw != 5 || output.Written != 0 {
			t.Fatalf("error=%T %#v", err, output)
		}
	case <-time.After(500 * time.Millisecond):
		_ = client.Close()
		t.Fatalf("quiet shim retained a stalled terminal writer for %s", time.Since(started))
	}
}

func TestRoleOutputReportsWriterErrorImmediatelyWhileProducerKeepsSending(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	want := errors.New("terminal rejected output")
	go func() {
		defer func() { _ = server.Close() }()
		chunk := make([]byte, AttachClientQueueBytes/2)
		_ = shim.WriteAttachFrame(server, shim.AttachFrame{Kind: shim.AttachFrameRoleOutput, Data: chunk})
		_ = shim.WriteAttachFrame(server, shim.AttachFrame{Kind: shim.AttachFrameRoleOutput, Data: chunk})
		_ = shim.WriteAttachFrame(server, shim.AttachFrame{Kind: shim.AttachFrameRoleOutput, Data: []byte{1}})
	}()
	started := time.Now()
	_, err := relayRoleOutputWithTimeout(context.Background(), client, errorContextWriter{err: want}, admittedAttachSequence(t), 2*time.Second)
	var output *TerminalOutputError
	if !errors.As(err, &output) || output.Stalled || !errors.Is(output.Cause, want) {
		t.Fatalf("error=%T %#v", err, output)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("writer error was hidden until timeout: %s", elapsed)
	}
}

func TestRoleOutputReportsWriterErrorWhileShimGoesQuiet(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	want := errors.New("quiet terminal rejected output")
	frameWritten := make(chan struct{})
	go func() {
		_ = shim.WriteAttachFrame(server, shim.AttachFrame{Kind: shim.AttachFrameRoleOutput, Data: []byte("small")})
		close(frameWritten)
	}()
	started := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := relayRoleOutputWithTimeout(context.Background(), client, errorContextWriter{err: want}, admittedAttachSequence(t), 2*time.Second)
		done <- err
	}()
	<-frameWritten
	select {
	case err := <-done:
		var output *TerminalOutputError
		if !errors.As(err, &output) || output.Stalled || !errors.Is(output.Cause, want) || output.Raw != 5 {
			t.Fatalf("error=%T %#v", err, output)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("writer error did not wake quiet frame reader after %s", time.Since(started))
	}
}

func TestViewerResizeEmitsObservedWindowSizeAsOneSerializedControlFrame(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	events := make(chan os.Signal, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- relayViewerResize(ctx, &clientFrameWriter{connection: client}, func() (ptyx.WindowSize, error) {
			return ptyx.WindowSize{Rows: 55, Cols: 144}, nil
		}, events)
	}()
	events <- syscall.SIGWINCH
	frame, err := shim.ReadAttachFrame(server)
	if err != nil {
		t.Fatal(err)
	}
	control, err := shim.DecodeAttachControl(frame.Data)
	if err != nil || control.Kind != shim.AttachControlResize || control.Rows != 55 || control.Cols != 144 {
		t.Fatalf("control=%#v error=%v", control, err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("resize relay error=%v", err)
	}
}

func TestRoleOutputKeepsShimRawAndTerminalWrittenCountsDistinct(t *testing.T) {
	for _, test := range []struct {
		name          string
		shimBytes     uint64
		writer        ptyx.ContextWriter
		wantTransport bool
		wantWritten   uint64
	}{
		{name: "raw short of shim", shimBytes: 4, writer: &recordingContextWriter{}, wantTransport: true},
		{name: "raw above shim", shimBytes: 2, writer: &recordingContextWriter{}, wantTransport: true},
		{name: "terminal partial error", shimBytes: 3, wantWritten: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer func() { _ = client.Close() }()
			sequence := admittedAttachSequence(t)
			writer := test.writer
			var finalRead chan struct{}
			if test.name == "terminal partial error" {
				finalRead = make(chan struct{})
				writer = gatedPartialErrorWriter{finalRead: finalRead}
			}
			go func() {
				defer func() { _ = server.Close() }()
				_ = shim.WriteAttachFrame(server, shim.AttachFrame{Kind: shim.AttachFrameRoleOutput, Data: []byte("abc")})
				_ = writeAttachControlForClient(server, shim.AttachControl{Version: 1, Kind: shim.AttachControlFinal, Disposition: shim.AttachDispositionChildExited, Bytes: test.shimBytes})
				if finalRead != nil {
					close(finalRead)
				}
			}()
			_, err := relayRoleOutputWithTimeout(context.Background(), client, writer, sequence, 100*time.Millisecond)
			if test.wantTransport {
				var transport *TransportError
				if !errors.As(err, &transport) || transport.Phase != "final" {
					t.Fatalf("error=%T %v", err, err)
				}
				return
			}
			var output *TerminalOutputError
			if !errors.As(err, &output) || output.Raw != 3 || output.Written != test.wantWritten || output.Prior == nil || output.Prior.Bytes != 3 {
				t.Fatalf("error=%T %#v", err, output)
			}
		})
	}
}

func admittedAttachSequence(t *testing.T) *shim.AttachSequence {
	t.Helper()
	sequence := &shim.AttachSequence{}
	for _, step := range []struct {
		direction shim.AttachDirection
		control   shim.AttachControl
	}{
		{shim.AttachFromShim, shim.AttachControl{Version: 1, Kind: shim.AttachControlShimHello}},
		{shim.AttachFromClient, shim.AttachControl{Version: 1, Kind: shim.AttachControlHello, Session: "fleet", Role: "planner", Rows: 24, Cols: 80}},
		{shim.AttachFromShim, shim.AttachControl{Version: 1, Kind: shim.AttachControlAdmitted}},
	} {
		payload, err := shim.EncodeAttachControl(step.control)
		if err != nil {
			t.Fatal(err)
		}
		if err := sequence.Observe(step.direction, shim.AttachFrame{Kind: shim.AttachFrameControl, Data: payload}); err != nil {
			t.Fatal(err)
		}
	}
	return sequence
}

type gatedPartialErrorWriter struct{ finalRead <-chan struct{} }

func (w gatedPartialErrorWriter) Write(context.Context, []byte) (int, error) {
	<-w.finalRead
	return 1, errors.New("terminal failed")
}

type neverDrainingWriter struct{}

func (neverDrainingWriter) Write(ctx context.Context, _ []byte) (int, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

type errorContextWriter struct{ err error }

func (w errorContextWriter) Write(context.Context, []byte) (int, error) { return 0, w.err }

type panicContextWriter struct{ value any }

func (w panicContextWriter) Write(context.Context, []byte) (int, error) { panic(w.value) }

type roleFleetReaderStub struct {
	record fleet.ShimFleetRecord
	err    error
}

func (s roleFleetReaderStub) Read(string) (fleet.ShimFleetRecord, error) { return s.record, s.err }

type roleInspectorStub struct {
	observation fleet.ShimRoleObservation
	err         error
}

func (s roleInspectorStub) Inspect(context.Context, string, string) (fleet.ShimRoleObservation, error) {
	return s.observation, s.err
}

type presentationObserverStub struct {
	present bool
	err     error
}

func (s presentationObserverStub) FindPresentationSession(context.Context, string) (tmuxx.Session, bool, error) {
	return tmuxx.Session{ID: "$1", Name: "fleet"}, s.present, s.err
}

func TestRoleClientStartupAndRestorationAreOneTotalOrder(t *testing.T) {
	var events eventLog
	clientConn, serverConn := net.Pipe()
	go func() {
		defer func() { _ = serverConn.Close() }()
		_ = writeAttachControlForClient(serverConn, shim.AttachControl{Version: 1, Kind: shim.AttachControlShimHello})
		_, _ = shim.ReadAttachFrame(serverConn)
		_ = writeAttachControlForClient(serverConn, shim.AttachControl{Version: 1, Kind: shim.AttachControlAdmitted})
		_ = writeAttachControlForClient(serverConn, shim.AttachControl{Version: 1, Kind: shim.AttachControlFinal, Disposition: shim.AttachDispositionChildExited})
	}()
	owner := &fakeRoleTerminalOwner{events: &events, relay: relayTerminal{input: blockingContextReader{}, output: &recordingContextWriter{}}}
	client := &RoleClient{
		terminal:  fakeRoleTerminalProvider{events: &events, owner: owner},
		preflight: fakeRolePreflight{events: &events},
		signals:   fakeRoleSignalProvider{events: &events},
		dial:      func(context.Context, string) (net.Conn, error) { events.add("dial"); return clientConn, nil },
	}
	result, err := client.Execute(context.Background(), "fleet", "planner")
	if err != nil || result.Disposition != shim.AttachDispositionChildExited {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	_ = client.Report(func(DiagnosticSink, bool) int { return 0 })
	want := []string{"terminal-check", "target-preflight", "terminal-open", "signal-observe", "signal-install", "raw", "dial", "restore", "signal-close", "close"}
	if got := events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events=%#v, want %#v", got, want)
	}
}

func TestRoleClientEveryStartupFailureStopsAtItsExactPrefix(t *testing.T) {
	checkErr := &NotTerminalError{}
	preflightErr := &ListenerAbsentError{Path: "/attach"}
	openErr := &TerminalOpenError{Cause: errors.New("open failed")}
	signalErr := &SignalObservationError{Signal: syscall.SIGINT, Observation: "disposition", Cause: errors.New("sigaction failed")}
	rawErr := errors.New("raw failed")
	dialErr := errors.New("dial failed")
	tests := []struct {
		name       string
		terminal   fakeRoleTerminalProvider
		preflight  fakeRolePreflight
		signals    fakeRoleSignalProvider
		dial       func(context.Context, string) (net.Conn, error)
		wantEvents []string
	}{
		{name: "terminal check", terminal: fakeRoleTerminalProvider{checkErr: checkErr}, wantEvents: []string{"terminal-check"}},
		{name: "target preflight", preflight: fakeRolePreflight{err: preflightErr}, wantEvents: []string{"terminal-check", "target-preflight"}},
		{name: "terminal open", terminal: fakeRoleTerminalProvider{openErr: openErr}, wantEvents: []string{"terminal-check", "target-preflight", "terminal-open"}},
		{name: "signal observation", signals: fakeRoleSignalProvider{err: signalErr}, wantEvents: []string{"terminal-check", "target-preflight", "terminal-open", "signal-observe", "restore", "close"}},
		{name: "raw", terminal: fakeRoleTerminalProvider{owner: &fakeRoleTerminalOwner{rawErr: rawErr}}, wantEvents: []string{"terminal-check", "target-preflight", "terminal-open", "signal-observe", "signal-install", "raw", "restore", "signal-close", "close"}},
		{name: "connection", dial: func(context.Context, string) (net.Conn, error) { return nil, dialErr }, wantEvents: []string{"terminal-check", "target-preflight", "terminal-open", "signal-observe", "signal-install", "raw", "dial", "restore", "signal-close", "close"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var events eventLog
			owner := &fakeRoleTerminalOwner{events: &events, relay: relayTerminal{input: blockingContextReader{}, output: &recordingContextWriter{}}}
			terminal := test.terminal
			terminal.events = &events
			if terminal.owner == nil {
				terminal.owner = owner
			} else if configured, ok := terminal.owner.(*fakeRoleTerminalOwner); ok {
				configured.events = &events
				configured.relay = owner.relay
			}
			preflight := test.preflight
			preflight.events = &events
			signals := test.signals
			signals.events = &events
			dial := test.dial
			if dial == nil {
				dial = func(context.Context, string) (net.Conn, error) { events.add("dial"); return nil, dialErr }
			} else {
				configured := dial
				dial = func(ctx context.Context, path string) (net.Conn, error) {
					events.add("dial")
					return configured(ctx, path)
				}
			}
			client := &RoleClient{terminal: terminal, preflight: preflight, signals: signals, dial: dial}
			_, _ = client.Execute(context.Background(), "fleet", "planner")
			_ = client.Report(func(DiagnosticSink, bool) int { return 0 })
			if got := events.snapshot(); !reflect.DeepEqual(got, test.wantEvents) {
				t.Fatalf("events=%#v, want %#v", got, test.wantEvents)
			}
		})
	}
}

func TestRoleClientSignalDuringReportFollowsDestinationOrdering(t *testing.T) {
	for _, test := range []struct {
		name             string
		sharesTerminal   bool
		wantBeforeFinish bool
	}{
		{name: "relay terminal finishes bounded attempt first", sharesTerminal: true},
		{name: "redirected sink lets owner act immediately", wantBeforeFinish: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := make(chan os.Signal, 1)
			reread := make(chan struct{}, 1)
			plan := &reportSignalPlan{reread: reread}
			client := &RoleClient{
				diagnosticSharesTerminal: test.sharesTerminal,
				reportSignals:            &pendingReportSignals{plan: plan, events: events},
			}
			started := make(chan struct{})
			finish := make(chan struct{})
			result := make(chan int, 1)
			go func() {
				result <- client.Report(func(DiagnosticSink, bool) int {
					close(started)
					<-finish
					return 5
				})
			}()
			<-started
			events <- syscall.SIGTERM
			if test.wantBeforeFinish {
				select {
				case <-reread:
				case <-time.After(200 * time.Millisecond):
					t.Fatal("owner did not re-raise while redirected report was blocked")
				}
			} else {
				select {
				case <-reread:
					t.Fatal("signal re-raised before relay-terminal report attempt finished")
				case <-time.After(30 * time.Millisecond):
				}
			}
			close(finish)
			select {
			case code := <-result:
				if test.sharesTerminal && code != 5 {
					t.Fatalf("Report()=%d, want completed row code 5", code)
				}
			case <-time.After(200 * time.Millisecond):
				t.Fatal("Report did not finish")
			}
			if !test.wantBeforeFinish {
				select {
				case <-reread:
				default:
					t.Fatal("signal was not re-raised after relay-terminal report finished")
				}
			}
			if plan.closeCalls != 1 || plan.reraiseCalls != 1 {
				t.Fatalf("close/reraise=%d/%d, want 1/1", plan.closeCalls, plan.reraiseCalls)
			}
		})
	}
}

func TestRoleClientReportReproducesSignalCapturedBeforeStopCompletes(t *testing.T) {
	events := make(chan os.Signal, 1)
	plan := &capturingReportSignalPlan{events: events, captured: syscall.SIGTERM}
	client := &RoleClient{
		diagnosticSharesTerminal: true,
		reportSignals:            &pendingReportSignals{plan: plan, events: events},
	}
	if code := client.Report(func(DiagnosticSink, bool) int { return 0 }); code != 0 {
		t.Fatalf("Report()=%d, want completed row code 0 after successful reproduction", code)
	}
	if plan.stopCalls == 0 || plan.reraiseCalls != 1 || plan.reraised != syscall.SIGTERM {
		t.Fatalf("stop/reraise/signal=%d/%d/%v, want stopped then one SIGTERM reproduction", plan.stopCalls, plan.reraiseCalls, plan.reraised)
	}
}

func TestRoleClientReportReturnsFailureWhenSignalReproductionFails(t *testing.T) {
	events := make(chan os.Signal, 1)
	events <- syscall.SIGTERM
	plan := &reportSignalPlan{reread: make(chan struct{}, 1), reraiseErr: errors.New("kill failed")}
	client := &RoleClient{
		diagnosticSharesTerminal: true,
		reportSignals:            &pendingReportSignals{plan: plan, events: events},
	}
	if code := client.Report(func(DiagnosticSink, bool) int { return 0 }); code != 6 {
		t.Fatalf("Report()=%d, want local failure exit 6 when signal reproduction fails", code)
	}
}

func TestRoleClientRetainsInstalledSignalPlanThroughEveryPostInstallReport(t *testing.T) {
	for _, test := range []struct {
		name   string
		rawErr error
	}{
		{name: "raw failure", rawErr: errors.New("raw failed")},
		{name: "dial failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var events eventLog
			signalEvents := make(chan os.Signal, 1)
			plan := &installedSignalPlan{events: signalEvents}
			owner := &fakeRoleTerminalOwner{events: &events, rawErr: test.rawErr, relay: relayTerminal{diagnostic: discardDiagnosticSink{}}}
			client := &RoleClient{
				terminal:  fakeRoleTerminalProvider{events: &events, owner: owner},
				preflight: fakeRolePreflight{events: &events},
				signals:   fixedRoleSignalProvider{plan: plan},
				dial:      func(context.Context, string) (net.Conn, error) { return nil, errors.New("dial failed") },
			}
			_, _ = client.Execute(context.Background(), "fleet", "planner")
			if client.reportSignals == nil {
				t.Fatal("installed signal ownership ended before result reporting")
			}
			_ = client.Report(func(DiagnosticSink, bool) int { return 6 })
			if plan.closeCalls != 1 {
				t.Fatalf("signal plan close calls=%d, want 1 after report", plan.closeCalls)
			}
		})
	}
}

func TestRoleClientRetainsPrivateTerminalDiagnosticUntilReportCompletes(t *testing.T) {
	var events eventLog
	diagnostic := &recordingDiagnosticSink{events: &events}
	owner := &fakeRoleTerminalOwner{events: &events, relay: relayTerminal{diagnostic: diagnostic}}
	client := &RoleClient{
		terminal:  fakeRoleTerminalProvider{events: &events, owner: owner},
		preflight: fakeRolePreflight{events: &events},
		signals:   fakeRoleSignalProvider{events: &events},
		dial: func(context.Context, string) (net.Conn, error) {
			events.add("dial")
			return nil, errors.New("dial failed")
		},
	}
	_, _ = client.Execute(context.Background(), "fleet", "planner")
	if got := events.snapshot(); containsEvent(got, "close") {
		t.Fatalf("terminal closed before report: %v", got)
	}
	code := client.Report(func(sink DiagnosticSink, _ bool) int {
		if sink == nil {
			t.Fatal("private diagnostic sink was not retained")
		}
		_, _ = sink.Attempt(context.Background(), []byte("row\n"))
		return 6
	})
	if code != 6 || string(diagnostic.bytes) != "row\n" {
		t.Fatalf("code=%d diagnostic=%q", code, diagnostic.bytes)
	}
	got := events.snapshot()
	if !containsEvent(got, "diagnostic") || !containsEvent(got, "close") {
		t.Fatalf("events=%v, want diagnostic then close", got)
	}
}

func TestRoleClientReportPanicClosesRetainedTerminalBeforeRepanic(t *testing.T) {
	var events eventLog
	client := &RoleClient{reportTerminal: &fakeRoleTerminalOwner{events: &events, relay: relayTerminal{diagnostic: discardDiagnosticSink{}}}}
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = client.Report(func(DiagnosticSink, bool) int { panic("report panic") })
	}()
	if recovered != "report panic" {
		t.Fatalf("recovered=%#v, want report panic", recovered)
	}
	if got := events.snapshot(); !containsEvent(got, "close") {
		t.Fatalf("events=%v, retained terminal was not closed", got)
	}
}

func TestRoleClientRestoresOnWorkerPanicThenRepanicsOnOwnerGoroutine(t *testing.T) {
	var events eventLog
	clientConn, serverConn := net.Pipe()
	defer func() { _ = serverConn.Close() }()
	go func() {
		_ = writeAttachControlForClient(serverConn, shim.AttachControl{Version: 1, Kind: shim.AttachControlShimHello})
		_, _ = shim.ReadAttachFrame(serverConn)
		_ = writeAttachControlForClient(serverConn, shim.AttachControl{Version: 1, Kind: shim.AttachControlAdmitted})
		_ = shim.WriteAttachFrame(serverConn, shim.AttachFrame{Kind: shim.AttachFrameRoleOutput, Data: []byte("panic")})
	}()
	owner := &fakeRoleTerminalOwner{events: &events, relay: relayTerminal{input: blockingContextReader{}, output: panicContextWriter{value: "writer panic"}, diagnostic: discardDiagnosticSink{}}}
	client := &RoleClient{
		terminal:  fakeRoleTerminalProvider{events: &events, owner: owner},
		preflight: fakeRolePreflight{events: &events},
		dial:      func(context.Context, string) (net.Conn, error) { events.add("dial"); return clientConn, nil },
	}
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_, _ = client.Execute(context.Background(), "fleet", "planner")
	}()
	if recovered != "writer panic" {
		t.Fatalf("recovered=%#v, want worker panic on owner goroutine", recovered)
	}
	if got := events.snapshot(); !containsEvent(got, "restore") {
		t.Fatalf("events=%v, terminal was not restored before re-panic", got)
	}
	_ = client.Close()
}

func TestRoleClientRestoreFailureSupersedesWorkerPanicAsLocalTermination(t *testing.T) {
	var events eventLog
	clientConn, serverConn := net.Pipe()
	defer func() { _ = serverConn.Close() }()
	go func() {
		_ = writeAttachControlForClient(serverConn, shim.AttachControl{Version: 1, Kind: shim.AttachControlShimHello})
		_, _ = shim.ReadAttachFrame(serverConn)
		_ = writeAttachControlForClient(serverConn, shim.AttachControl{Version: 1, Kind: shim.AttachControlAdmitted})
		_ = shim.WriteAttachFrame(serverConn, shim.AttachFrame{Kind: shim.AttachFrameRoleOutput, Data: []byte("panic")})
	}()
	restoreCause := errors.New("restore failed")
	owner := &fakeRoleTerminalOwner{events: &events, restoreErr: restoreCause, relay: relayTerminal{input: blockingContextReader{}, output: panicContextWriter{value: "writer panic"}, diagnostic: discardDiagnosticSink{}}}
	client := &RoleClient{
		terminal:  fakeRoleTerminalProvider{events: &events, owner: owner},
		preflight: fakeRolePreflight{events: &events},
		dial:      func(context.Context, string) (net.Conn, error) { return clientConn, nil },
	}
	var executeErr error
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_, executeErr = client.Execute(context.Background(), "fleet", "planner")
	}()
	if recovered != nil {
		t.Fatalf("restore failure did not suppress worker panic: %#v", recovered)
	}
	var restore *TerminalRestoreError
	if !errors.As(executeErr, &restore) || !errors.Is(restore.Cause, restoreCause) {
		t.Fatalf("error=%T %v, want restore failure", executeErr, executeErr)
	}
	var local *LocalTerminationError
	if !errors.As(restore.Prior, &local) {
		t.Fatalf("restore prior=%T %v, want local termination", restore.Prior, restore.Prior)
	}
	_ = client.Report(func(DiagnosticSink, bool) int { return 6 })
}

func TestRoleClientHandledSignalRestoreFailureAttemptsRestoreOnceAndDoesNotReraise(t *testing.T) {
	var events eventLog
	clientConn, serverConn := net.Pipe()
	defer func() { _ = serverConn.Close() }()
	go func() {
		_ = writeAttachControlForClient(serverConn, shim.AttachControl{Version: 1, Kind: shim.AttachControlShimHello})
		_, _ = shim.ReadAttachFrame(serverConn)
		_ = writeAttachControlForClient(serverConn, shim.AttachControl{Version: 1, Kind: shim.AttachControlAdmitted})
	}()
	signalEvents := make(chan os.Signal, 1)
	signalEvents <- syscall.SIGTERM
	plan := &installedSignalPlan{events: signalEvents}
	owner := &fakeRoleTerminalOwner{events: &events, restoreErr: errors.New("restore failed"), relay: relayTerminal{input: blockingContextReader{}, output: &recordingContextWriter{}, diagnostic: discardDiagnosticSink{}}}
	client := &RoleClient{
		terminal:  fakeRoleTerminalProvider{events: &events, owner: owner},
		preflight: fakeRolePreflight{events: &events},
		signals:   fixedRoleSignalProvider{plan: plan},
		dial:      func(context.Context, string) (net.Conn, error) { return clientConn, nil },
	}
	_, err := client.Execute(context.Background(), "fleet", "planner")
	var restore *TerminalRestoreError
	if !errors.As(err, &restore) {
		t.Fatalf("error=%T %v, want restore failure", err, err)
	}
	restores := 0
	for _, event := range events.snapshot() {
		if event == "restore" {
			restores++
		}
	}
	if restores != 1 || plan.reraiseCalls != 0 {
		t.Fatalf("restore/reraise=%d/%d, want 1/0", restores, plan.reraiseCalls)
	}
	_ = client.Report(func(DiagnosticSink, bool) int { return 6 })
}

func writeAttachControlForClient(connection net.Conn, control shim.AttachControl) error {
	payload, err := shim.EncodeAttachControl(control)
	if err != nil {
		return err
	}
	return shim.WriteAttachFrame(connection, shim.AttachFrame{Kind: shim.AttachFrameControl, Data: payload})
}

type blockingContextReader struct{}

func (blockingContextReader) Read(ctx context.Context, _ []byte) (int, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

type eofContextReader struct{}

func (eofContextReader) Read(context.Context, []byte) (int, error) { return 0, io.EOF }

type recordingContextWriter struct {
	mu    sync.Mutex
	bytes []byte
}

func (w *recordingContextWriter) Write(_ context.Context, value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.bytes = append(w.bytes, value...)
	return len(value), nil
}
func (w *recordingContextWriter) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.bytes...)
}

type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}
func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

func containsEvent(events []string, want string) bool {
	for _, event := range events {
		if event == want {
			return true
		}
	}
	return false
}

type recordingDiagnosticSink struct {
	events *eventLog
	bytes  []byte
}

func (s *recordingDiagnosticSink) Attempt(_ context.Context, value []byte) (int, error) {
	s.events.add("diagnostic")
	s.bytes = append(s.bytes, value...)
	return len(value), nil
}

type fakeRoleTerminalProvider struct {
	events   *eventLog
	owner    roleTerminalOwner
	checkErr error
	openErr  error
}

func (p fakeRoleTerminalProvider) check() (terminalCheck, error) {
	p.events.add("terminal-check")
	if p.checkErr != nil {
		return terminalCheck{}, p.checkErr
	}
	return terminalCheck{size: ptyx.WindowSize{Rows: 24, Cols: 80}}, nil
}
func (p fakeRoleTerminalProvider) open(terminalCheck) (roleTerminalOwner, error) {
	p.events.add("terminal-open")
	if p.openErr != nil {
		return nil, p.openErr
	}
	return p.owner, nil
}

type fakeRoleTerminalOwner struct {
	events     *eventLog
	relay      relayTerminal
	rawErr     error
	restoreErr error
}

func (o *fakeRoleTerminalOwner) streams() relayTerminal { return o.relay }
func (o *fakeRoleTerminalOwner) makeRaw() error         { o.events.add("raw"); return o.rawErr }
func (o *fakeRoleTerminalOwner) restore() error         { o.events.add("restore"); return o.restoreErr }
func (o *fakeRoleTerminalOwner) close() error           { o.events.add("close"); return nil }

type fakeRolePreflight struct {
	events *eventLog
	err    error
}

func (p fakeRolePreflight) prepare(context.Context, string, string) (roleTarget, error) {
	p.events.add("target-preflight")
	if p.err != nil {
		return roleTarget{}, p.err
	}
	return roleTarget{attachPath: "/attach"}, nil
}

type fakeRoleSignalProvider struct {
	events *eventLog
	err    error
}

func (p fakeRoleSignalProvider) observe() (roleSignalPlan, error) {
	p.events.add("signal-observe")
	if p.err != nil {
		return nil, p.err
	}
	return &fakeRoleSignalPlan{events: p.events}, nil
}

type fakeRoleSignalPlan struct{ events *eventLog }

func (p *fakeRoleSignalPlan) install() <-chan os.Signal    { p.events.add("signal-install"); return nil }
func (p *fakeRoleSignalPlan) stop()                        {}
func (p *fakeRoleSignalPlan) reraise(syscall.Signal) error { return nil }
func (p *fakeRoleSignalPlan) close()                       { p.events.add("signal-close") }

type reportSignalPlan struct {
	reread       chan struct{}
	closeCalls   int
	reraiseCalls int
	reraiseErr   error
}

type capturingReportSignalPlan struct {
	events       chan<- os.Signal
	captured     syscall.Signal
	stopped      bool
	stopCalls    int
	closeCalls   int
	reraiseCalls int
	reraised     syscall.Signal
}

type fixedRoleSignalProvider struct{ plan roleSignalPlan }

func (p fixedRoleSignalProvider) observe() (roleSignalPlan, error) { return p.plan, nil }

type installedSignalPlan struct {
	events       <-chan os.Signal
	closeCalls   int
	reraiseCalls int
}

func (p *installedSignalPlan) install() <-chan os.Signal    { return p.events }
func (*installedSignalPlan) stop()                          {}
func (p *installedSignalPlan) reraise(syscall.Signal) error { p.reraiseCalls++; return nil }
func (p *installedSignalPlan) close()                       { p.closeCalls++ }

func (*reportSignalPlan) install() <-chan os.Signal { return nil }
func (*reportSignalPlan) stop()                     {}
func (p *reportSignalPlan) reraise(syscall.Signal) error {
	p.reraiseCalls++
	p.reread <- struct{}{}
	return p.reraiseErr
}
func (p *reportSignalPlan) close() { p.closeCalls++ }

func (*capturingReportSignalPlan) install() <-chan os.Signal { return nil }
func (p *capturingReportSignalPlan) stop() {
	p.stopCalls++
	if p.stopped {
		return
	}
	p.stopped = true
	p.events <- p.captured
}
func (p *capturingReportSignalPlan) reraise(signal syscall.Signal) error {
	p.reraiseCalls++
	p.reraised = signal
	p.stop()
	return nil
}
func (p *capturingReportSignalPlan) close() {
	p.closeCalls++
	p.stop()
}
