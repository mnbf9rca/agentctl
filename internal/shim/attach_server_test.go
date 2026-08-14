//go:build darwin

package shim

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mnbf9rca/agentctl/internal/ptyx"
)

func TestAttachServerAdmitsSameUIDAfterApplyingSizeAndRoutesFramesInOrder(t *testing.T) {
	relay, stopRelay := newAttachTestRelay(t)
	defer stopRelay()
	writer := &recordingOperationWriter{}
	var sizeMu sync.Mutex
	var sizes []ptyx.WindowSize
	server := newAttachServer(attachServerConfig{
		Session: "fleet", Role: "planner", ShimUID: 501,
		Relay: relay, Input: newRoleInputWriter(writer),
		PeerIdentity: func(net.Conn) (attachPeerIdentity, error) {
			return attachPeerIdentity{PID: 101, UID: 501}, nil
		},
		Resize: func(size ptyx.WindowSize) error {
			sizeMu.Lock()
			defer sizeMu.Unlock()
			sizes = append(sizes, size)
			return nil
		},
		Phase: func() shimOperationPhase { return shimOperationActive },
	})
	client, done := startAttachConnection(t, server)
	readAttachControlKind(t, client, AttachControlShimHello)
	writeAttachControl(t, client, AttachControl{Version: 1, Kind: AttachControlHello, Session: "fleet", Role: "planner", Rows: 24, Cols: 80})
	readAttachControlKind(t, client, AttachControlAdmitted)

	sizeMu.Lock()
	initialSizes := append([]ptyx.WindowSize(nil), sizes...)
	sizeMu.Unlock()
	if got, want := initialSizes, []ptyx.WindowSize{{Rows: 24, Cols: 80}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sizes before admission = %#v, want %#v", got, want)
	}
	writeAttachFrame(t, client, AttachFrame{Kind: AttachFrameViewerInput, Data: []byte("typed")})
	writeAttachControl(t, client, AttachControl{Version: 1, Kind: AttachControlResize, Rows: 30, Cols: 100})
	waitAttachCondition(t, func() bool {
		writer.mu.Lock()
		writeCount := len(writer.calls)
		writer.mu.Unlock()
		sizeMu.Lock()
		sizeCount := len(sizes)
		sizeMu.Unlock()
		return writeCount == 1 && sizeCount == 2
	})
	writer.mu.Lock()
	gotWrites := append([][]byte(nil), writer.calls...)
	writer.mu.Unlock()
	if !reflect.DeepEqual(gotWrites, [][]byte{[]byte("typed")}) {
		t.Fatalf("viewer writes = %#v, want typed", gotWrites)
	}
	sizeMu.Lock()
	resized := sizes[1]
	sizeMu.Unlock()
	if got, want := resized, (ptyx.WindowSize{Rows: 30, Cols: 100}); got != want {
		t.Fatalf("resize = %#v, want %#v", got, want)
	}
	_ = client.Close()
	waitAttachServer(t, done)
}

func TestAttachServerRefusesUnverifiedPeerBeforePTYMutation(t *testing.T) {
	relay, stopRelay := newAttachTestRelay(t)
	defer stopRelay()
	writer := &recordingOperationWriter{}
	resizeCalls := 0
	server := newAttachServer(attachServerConfig{
		Session: "fleet", Role: "planner", ShimUID: 501,
		Relay: relay, Input: newRoleInputWriter(writer),
		PeerIdentity: func(net.Conn) (attachPeerIdentity, error) {
			return attachPeerIdentity{PID: 101, UID: 502}, nil
		},
		Resize: func(ptyx.WindowSize) error { resizeCalls++; return nil },
	})
	client, done := startAttachConnection(t, server)
	readAttachControlKind(t, client, AttachControlShimHello)
	writeAttachControl(t, client, AttachControl{Version: 1, Kind: AttachControlHello, Session: "fleet", Role: "planner", Rows: 24, Cols: 80})
	control := readAttachControlKind(t, client, AttachControlRefused)
	if control.Outcome != AttachRefusalPeerUnverified || control.PeerPID != 101 || control.PeerUID != 502 || control.ShimUID != 501 {
		t.Fatalf("refusal = %#v, want kernel identity facts", control)
	}
	if resizeCalls != 0 || len(writer.calls) != 0 {
		t.Fatalf("refusal mutated PTY: resizes=%d writes=%#v", resizeCalls, writer.calls)
	}
	waitAttachServer(t, done)
}

func TestAttachServerReleasesQuietViewerOnEOFAndAdmitsReplacement(t *testing.T) {
	relay, stopRelay := newAttachTestRelay(t)
	defer stopRelay()
	var peerMu sync.Mutex
	peerPID := 101
	server := newAttachServer(attachServerConfig{
		Session: "fleet", Role: "planner", ShimUID: 501,
		Relay: relay, Input: newRoleInputWriter(&recordingOperationWriter{}),
		PeerIdentity: func(net.Conn) (attachPeerIdentity, error) {
			peerMu.Lock()
			defer peerMu.Unlock()
			return attachPeerIdentity{PID: peerPID, UID: 501}, nil
		},
		Resize: func(ptyx.WindowSize) error { return nil },
	})
	first, firstDone := startAttachConnection(t, server)
	admitAttachClient(t, first)

	peerMu.Lock()
	peerPID = 202
	peerMu.Unlock()
	second, secondDone := startAttachConnection(t, server)
	readAttachControlKind(t, second, AttachControlShimHello)
	writeAttachControl(t, second, AttachControl{Version: 1, Kind: AttachControlHello, Session: "fleet", Role: "planner", Rows: 24, Cols: 80})
	refused := readAttachControlKind(t, second, AttachControlRefused)
	if refused.Outcome != AttachRefusalViewerPresent || refused.ViewerPID != 101 {
		t.Fatalf("second refusal = %#v, want incumbent PID 101", refused)
	}
	waitAttachServer(t, secondDone)

	_ = first.Close()
	waitAttachServer(t, firstDone)
	third, thirdDone := startAttachConnection(t, server)
	admitAttachClient(t, third)
	_ = third.Close()
	waitAttachServer(t, thirdDone)
}

func TestAttachServerInitialResizeFailureCostsNoSeat(t *testing.T) {
	relay, stopRelay := newAttachTestRelay(t)
	defer stopRelay()
	fail := true
	server := newAttachServer(attachServerConfig{
		Session: "fleet", Role: "planner", ShimUID: 501,
		Relay: relay, Input: newRoleInputWriter(&recordingOperationWriter{}),
		PeerIdentity: func(net.Conn) (attachPeerIdentity, error) {
			return attachPeerIdentity{PID: 101, UID: 501}, nil
		},
		Resize: func(ptyx.WindowSize) error {
			if fail {
				fail = false
				return errors.New("injected resize failure")
			}
			return nil
		},
	})
	first, firstDone := startAttachConnection(t, server)
	readAttachControlKind(t, first, AttachControlShimHello)
	writeAttachControl(t, first, AttachControl{Version: 1, Kind: AttachControlHello, Session: "fleet", Role: "planner", Rows: 24, Cols: 80})
	refused := readAttachControlKind(t, first, AttachControlRefused)
	if refused.Outcome != AttachRefusalInitialSizeFailed || refused.Rows != 24 || refused.Cols != 80 {
		t.Fatalf("resize refusal = %#v", refused)
	}
	waitAttachServer(t, firstDone)
	second, secondDone := startAttachConnection(t, server)
	admitAttachClient(t, second)
	_ = second.Close()
	waitAttachServer(t, secondDone)
}

func TestAttachServerProtocolViolationBeforeAndAfterAdmissionHasExactTerminalShape(t *testing.T) {
	relay, stopRelay := newAttachTestRelay(t)
	defer stopRelay()
	server := newAttachServer(attachServerConfig{
		Session: "fleet", Role: "planner", ShimUID: 501,
		Relay: relay, Input: newRoleInputWriter(&recordingOperationWriter{}),
		PeerIdentity: func(net.Conn) (attachPeerIdentity, error) {
			return attachPeerIdentity{PID: 101, UID: 501}, nil
		},
		Resize: func(ptyx.WindowSize) error { return nil },
	})
	before, beforeDone := startAttachConnection(t, server)
	readAttachControlKind(t, before, AttachControlShimHello)
	writeAttachFrame(t, before, AttachFrame{Kind: AttachFrameViewerInput, Data: []byte("early")})
	if _, err := ReadAttachFrame(before); !errors.Is(err, io.EOF) {
		t.Fatalf("pre-admission violation read = %v, want bare EOF", err)
	}
	if err := <-beforeDone; err == nil {
		t.Fatal("pre-admission violation returned no server error")
	}

	after, afterDone := startAttachConnection(t, server)
	admitAttachClient(t, after)
	writeAttachControl(t, after, AttachControl{Version: 1, Kind: AttachControlShimHello})
	final := readAttachControlKind(t, after, AttachControlFinal)
	if final.Disposition != AttachDispositionServerClosing {
		t.Fatalf("post-admission final = %#v, want server-closing", final)
	}
	if err := <-afterDone; err == nil {
		t.Fatal("post-admission violation returned no server error")
	}
}

func TestAttachServerDiscardsDecodedInputAfterAdmissionIsReleased(t *testing.T) {
	relay, stopRelay := newAttachTestRelay(t)
	defer stopRelay()
	writer := &recordingOperationWriter{}
	input := newRoleInputWriter(writer)
	_, releaseDelivery, err := input.BeginDelivery(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	server := newAttachServer(attachServerConfig{
		Session: "fleet", Role: "planner", ShimUID: 501,
		Relay: relay, Input: input,
		PeerIdentity: func(net.Conn) (attachPeerIdentity, error) {
			return attachPeerIdentity{PID: 101, UID: 501}, nil
		},
		Resize: func(ptyx.WindowSize) error { return nil },
		Phase:  func() shimOperationPhase { return shimOperationActive },
	})
	client, done := startAttachConnection(t, server)
	admitAttachClient(t, client)
	writeAttachFrame(t, client, AttachFrame{Kind: AttachFrameViewerInput, Data: []byte("stale")})
	_ = client.Close()
	waitAttachServer(t, done)
	releaseDelivery()
	time.Sleep(25 * time.Millisecond)
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if len(writer.calls) != 0 {
		t.Fatalf("released viewer input reached PTY: %#v", writer.calls)
	}
}

func TestAttachServerChildExitFlushesCountedOutputBeforeOneFinal(t *testing.T) {
	reader := &attachTestReader{steps: make(chan attachTestReadStep, 1)}
	relay := ptyx.NewResidentRelay(reader, attachDiscardWriter{})
	relayCtx, stopRelay := context.WithCancel(context.Background())
	relayDone := make(chan error, 1)
	go func() { relayDone <- relay.Run(relayCtx) }()
	defer func() {
		stopRelay()
		<-relayDone
	}()
	server := newAttachServer(attachServerConfig{
		Session: "fleet", Role: "planner", ShimUID: 501,
		Relay: relay, Input: newRoleInputWriter(&recordingOperationWriter{}),
		PeerIdentity: func(net.Conn) (attachPeerIdentity, error) {
			return attachPeerIdentity{PID: 101, UID: 501}, nil
		},
		Resize: func(ptyx.WindowSize) error { return nil },
	})
	client, done := startAttachConnection(t, server)
	admitAttachClient(t, client)
	reader.steps <- attachTestReadStep{value: []byte("tail")}
	frame, err := ReadAttachFrame(client)
	if err != nil || frame.Kind != AttachFrameRoleOutput || string(frame.Data) != "tail" {
		t.Fatalf("role output frame = %#v, %v, want tail", frame, err)
	}
	childFinished := make(chan struct{})
	go func() {
		server.childExited(context.Background())
		close(childFinished)
	}()
	final := readAttachControlKind(t, client, AttachControlFinal)
	if final.Disposition != AttachDispositionChildExited || final.Bytes != 4 {
		t.Fatalf("final = %#v, want child-exited bytes=4", final)
	}
	<-childFinished
	waitAttachServer(t, done)
}

func TestAttachRuntimeSourceGuardsKeepSurfacesClosedAndBuffersBounded(t *testing.T) {
	attachSource, err := os.ReadFile("attach_server.go")
	if err != nil {
		t.Fatal(err)
	}
	serverSource, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	relaySource, err := os.ReadFile("../ptyx/resident_relay.go")
	if err != nil {
		t.Fatal(err)
	}
	attachText := string(attachSource)
	for _, forbidden := range []string{"DecodeRequest(", "control.Lookup(", "DetachKey", "detach-key"} {
		if strings.Contains(attachText, forbidden) {
			t.Fatalf("attach server reopened forbidden surface %q", forbidden)
		}
	}
	if strings.Count(attachText, "make(chan attachAction, attachPendingActions)") != 1 || !strings.Contains(attachText, "const attachPendingActions = 16") {
		t.Fatal("attach input actions are not held by one explicit bounded queue")
	}
	if strings.Count(string(serverSource), "newRoleInputWriter(runtime.relay.Writer()") != 1 {
		t.Fatal("server does not construct exactly one role gate above the relay writer")
	}
	if strings.Count(string(relaySource), "AttachLagBufferBytes") != 3 || !strings.Contains(string(relaySource), "const AttachLagBufferBytes = 131072") {
		t.Fatal("resident relay does not declare and consume the approved lag bound exactly once")
	}
}

type attachTestReadStep struct {
	value []byte
	err   error
}

type attachTestReader struct{ steps chan attachTestReadStep }

func (r *attachTestReader) Read(ctx context.Context, buffer []byte) (int, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case step := <-r.steps:
		return copy(buffer, step.value), step.err
	}
}

type attachDiscardWriter struct{}

func (attachDiscardWriter) Write(_ context.Context, value []byte) (int, error) {
	return len(value), nil
}

func newAttachTestRelay(t *testing.T) (*ptyx.ResidentRelay, func()) {
	t.Helper()
	reader := &attachTestReader{steps: make(chan attachTestReadStep)}
	relay := ptyx.NewResidentRelay(reader, attachDiscardWriter{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- relay.Run(ctx) }()
	return relay, func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("resident relay did not stop")
		}
	}
}

func startAttachConnection(t *testing.T, server *attachServer) (net.Conn, <-chan error) {
	t.Helper()
	client, shim := net.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- server.handleConnection(context.Background(), shim)
		_ = shim.Close()
	}()
	return client, done
}

func admitAttachClient(t *testing.T, client net.Conn) {
	t.Helper()
	readAttachControlKind(t, client, AttachControlShimHello)
	writeAttachControl(t, client, AttachControl{Version: 1, Kind: AttachControlHello, Session: "fleet", Role: "planner", Rows: 24, Cols: 80})
	readAttachControlKind(t, client, AttachControlAdmitted)
}

func writeAttachControl(t *testing.T, connection net.Conn, control AttachControl) {
	t.Helper()
	payload, err := EncodeAttachControl(control)
	if err != nil {
		t.Fatal(err)
	}
	writeAttachFrame(t, connection, AttachFrame{Kind: AttachFrameControl, Data: payload})
}

func writeAttachFrame(t *testing.T, connection net.Conn, frame AttachFrame) {
	t.Helper()
	if err := WriteAttachFrame(connection, frame); err != nil {
		t.Fatalf("WriteAttachFrame() error = %v", err)
	}
}

func readAttachControlKind(t *testing.T, connection net.Conn, want AttachControlKind) AttachControl {
	t.Helper()
	frame, err := ReadAttachFrame(connection)
	if err != nil {
		t.Fatalf("ReadAttachFrame() error = %v", err)
	}
	if frame.Kind != AttachFrameControl {
		t.Fatalf("frame kind = %v, want control", frame.Kind)
	}
	control, err := DecodeAttachControl(frame.Data)
	if err != nil {
		t.Fatal(err)
	}
	if control.Kind != want {
		t.Fatalf("control kind = %q, want %q", control.Kind, want)
	}
	return control
}

func waitAttachServer(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("attach server error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("attach server did not release connection")
	}
}

func waitAttachCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("attach condition was not observed")
}
