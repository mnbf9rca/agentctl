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
	"golang.org/x/sys/unix"
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

func TestAttachServerHalfCloseRevokesSeatClosesOutputAndAdmitsReplacement(t *testing.T) {
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
	first, firstDone := startUnixAttachConnection(t, server)
	admitAttachClient(t, first)
	if err := first.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	final := readAttachControlKind(t, first, AttachControlFinal)
	if final.Disposition != AttachDispositionServerClosing {
		t.Fatalf("half-close final = %#v, want server-closing", final)
	}
	if _, err := ReadAttachFrame(first); !connectionClosedError(err) {
		t.Fatalf("half-closed viewer remained readable: %v", err)
	}
	_ = first.Close()
	waitAttachServer(t, firstDone)

	second, secondDone := startUnixAttachConnection(t, server)
	admitAttachClient(t, second)
	_ = second.Close()
	waitAttachServer(t, secondDone)
}

func TestAttachServerOutputWriteErrorRevokesSeatAndAdmitsReplacement(t *testing.T) {
	reader := &attachTestReader{steps: make(chan attachTestReadStep, 1)}
	relay := ptyx.NewResidentRelay(reader, attachDiscardWriter{})
	relayCtx, stopRelay := context.WithCancel(context.Background())
	relayDone := make(chan error, 1)
	go func() { relayDone <- relay.Run(relayCtx) }()
	defer func() { stopRelay(); <-relayDone }()
	server := newAttachServer(attachServerConfig{
		Session: "fleet", Role: "planner", ShimUID: 501,
		Relay: relay, Input: newRoleInputWriter(&recordingOperationWriter{}),
		PeerIdentity: func(net.Conn) (attachPeerIdentity, error) {
			return attachPeerIdentity{PID: 101, UID: 501}, nil
		},
		Resize: func(ptyx.WindowSize) error { return nil },
	})
	first, rawShim := net.Pipe()
	failingShim := &attachFailWriteConn{Conn: rawShim}
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- server.handleConnection(context.Background(), failingShim)
		_ = failingShim.Close()
	}()
	admitAttachClient(t, first)
	failingShim.mu.Lock()
	failingShim.enabled = true
	failingShim.mu.Unlock()
	reader.steps <- attachTestReadStep{value: []byte("break-output")}
	waitAttachServer(t, firstDone)
	_ = first.Close()

	second, secondDone := startUnixAttachConnection(t, server)
	admitAttachClient(t, second)
	_ = second.Close()
	waitAttachServer(t, secondDone)
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

func TestAttachServerKeepsSeatUntilRelayAndInputReleaseComplete(t *testing.T) {
	relay, stopRelay := newAttachTestRelay(t)
	defer stopRelay()
	input := newRoleInputWriter(&recordingOperationWriter{})
	_, releaseDelivery, err := input.BeginDelivery(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	server := newAttachServer(attachServerConfig{
		Session: "fleet", Role: "planner", ShimUID: 501, Relay: relay, Input: input,
		PeerIdentity: func(net.Conn) (attachPeerIdentity, error) {
			return attachPeerIdentity{PID: 101, UID: 501}, nil
		},
		Resize: func(ptyx.WindowSize) error { return nil },
	})
	first, firstDone := startAttachConnection(t, server)
	admitAttachClient(t, first)
	_ = first.Close()
	waitAttachCondition(t, func() bool {
		server.mu.Lock()
		defer server.mu.Unlock()
		return server.viewer != nil && server.viewer.ctx.Err() != nil
	})
	second, secondDone := startAttachConnection(t, server)
	readAttachControlKind(t, second, AttachControlShimHello)
	writeAttachControl(t, second, AttachControl{Version: 1, Kind: AttachControlHello, Session: "fleet", Role: "planner", Rows: 24, Cols: 80})
	refused := readAttachControlKind(t, second, AttachControlRefused)
	if refused.Outcome != AttachRefusalViewerPresent || refused.ViewerPID != 101 {
		t.Fatalf("replacement during release = %#v, want incumbent PID 101", refused)
	}
	waitAttachServer(t, secondDone)
	releaseDelivery()
	waitAttachServer(t, firstDone)
}

func TestAttachServerCleanupRetainedPinsViewerAndEmitsExactFinal(t *testing.T) {
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
	client, done := startAttachConnection(t, server)
	admitAttachClient(t, client)
	finished := make(chan struct{})
	go func() { server.cleanupRetained(); close(finished) }()
	final := readAttachControlKind(t, client, AttachControlFinal)
	if final.Disposition != AttachDispositionCleanupRetained || final.Bytes != 0 {
		t.Fatalf("cleanup final = %#v, want cleanup-retained bytes=0", final)
	}
	<-finished
	waitAttachServer(t, done)

	replacement, replacementDone := startAttachConnection(t, server)
	readAttachControlKind(t, replacement, AttachControlShimHello)
	writeAttachControl(t, replacement, AttachControl{Version: 1, Kind: AttachControlHello, Session: "fleet", Role: "planner", Rows: 24, Cols: 80})
	if _, err := ReadAttachFrame(replacement); !connectionClosedError(err) {
		t.Fatalf("terminal attach server replacement read = %v, want close", err)
	}
	if err := <-replacementDone; err == nil {
		t.Fatal("terminal attach server accepted replacement")
	}
}

func TestAttachServerTerminalDecisionWaitsForAdmissionCommit(t *testing.T) {
	relay, stopRelay := newAttachTestRelay(t)
	defer stopRelay()
	blocking := &blockingAttachAdmissionRelay{
		ResidentRelay: relay,
		entered:       make(chan struct{}),
		release:       make(chan struct{}),
	}
	server := newAttachServer(attachServerConfig{
		Session: "fleet", Role: "planner", ShimUID: 501,
		Relay: blocking, Input: newRoleInputWriter(&recordingOperationWriter{}),
		PeerIdentity: func(net.Conn) (attachPeerIdentity, error) {
			return attachPeerIdentity{PID: 101, UID: 501}, nil
		},
		Resize: func(ptyx.WindowSize) error { return nil },
	})
	client, done := startAttachConnection(t, server)
	readAttachControlKind(t, client, AttachControlShimHello)
	writeAttachControl(t, client, AttachControl{
		Version: 1, Kind: AttachControlHello, Session: "fleet", Role: "planner", Rows: 24, Cols: 80,
	})
	<-blocking.entered
	cleanupDone := make(chan struct{})
	go func() {
		server.cleanupRetained()
		close(cleanupDone)
	}()
	select {
	case <-cleanupDone:
		t.Fatal("terminal decision bypassed in-flight admission commit")
	case <-time.After(25 * time.Millisecond):
	}
	close(blocking.release)
	readAttachControlKind(t, client, AttachControlAdmitted)
	final := readAttachControlKind(t, client, AttachControlFinal)
	if final.Disposition != AttachDispositionCleanupRetained {
		t.Fatalf("terminal admission final = %#v, want cleanup-retained", final)
	}
	<-cleanupDone
	waitAttachServer(t, done)
}

func TestAttachServerAdmissionThatPassedEarlyCheckCannotCrossPendingStop(t *testing.T) {
	reader := &attachTestReader{steps: make(chan attachTestReadStep)}
	relay := ptyx.NewResidentRelay(reader, attachDiscardWriter{})
	relayCtx, stopRelay := context.WithCancel(context.Background())
	relayDone := make(chan error, 1)
	go func() { relayDone <- relay.Run(relayCtx) }()
	defer func() { stopRelay(); <-relayDone }()
	gate := newRoleInputWriter(&recordingOperationWriter{})
	_, releaseGate, err := gate.BeginDelivery(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	handler := &requestHandler{inputBarrier: gate.(roleInputBarrier)}
	earlyChecked := make(chan struct{})
	releaseEarlyCheck := make(chan struct{})
	resizeCalls := 0
	server := newAttachServer(attachServerConfig{
		Session: "fleet", Role: "planner", ShimUID: 501,
		Relay: relay, Input: gate,
		PeerIdentity: func(net.Conn) (attachPeerIdentity, error) {
			return attachPeerIdentity{PID: 101, UID: 501}, nil
		},
		Resize: func(ptyx.WindowSize) error { resizeCalls++; return nil },
		Phase: func() shimOperationPhase {
			close(earlyChecked)
			<-releaseEarlyCheck
			return shimOperationActive
		},
		WithActive: handler.withActivePhase,
	})
	client, done := startAttachConnection(t, server)
	readAttachControlKind(t, client, AttachControlShimHello)
	writeAttachControl(t, client, AttachControl{
		Version: 1, Kind: AttachControlHello, Session: "fleet", Role: "planner", Rows: 24, Cols: 80,
	})
	<-earlyChecked
	stopDone := make(chan error, 1)
	go func() {
		_, _, stopErr := handler.beginStop(context.Background())
		stopDone <- stopErr
	}()
	waitAttachCondition(t, func() bool { return handler.operationPhase() == shimOperationStopping })
	close(releaseEarlyCheck)
	waitAttachCondition(t, func() bool {
		server.mu.Lock()
		defer server.mu.Unlock()
		return server.viewer != nil && server.viewer.ctx.Err() != nil
	})
	releaseGate()
	if _, err := ReadAttachFrame(client); !connectionClosedError(err) {
		t.Fatalf("pending-stop admission read = %v, want close", err)
	}
	if handleErr := <-done; !errors.Is(handleErr, errAttachAdmissionUnavailable) {
		t.Fatalf("pending-stop admission error = %v, want unavailable", handleErr)
	}
	if resizeCalls != 0 {
		t.Fatalf("pending-stop admission applied %d PTY resizes", resizeCalls)
	}
	if err := <-stopDone; err != nil {
		t.Fatal(err)
	}
}

func TestAttachServerTerminalDecisionWaitsForAdmittedResizeCommit(t *testing.T) {
	relay, stopRelay := newAttachTestRelay(t)
	defer stopRelay()
	resizeEntered := make(chan struct{})
	releaseResize := make(chan struct{})
	server := newAttachServer(attachServerConfig{
		Session: "fleet", Role: "planner", ShimUID: 501,
		Relay: relay, Input: newRoleInputWriter(&recordingOperationWriter{}),
		PeerIdentity: func(net.Conn) (attachPeerIdentity, error) {
			return attachPeerIdentity{PID: 101, UID: 501}, nil
		},
		Resize: func(size ptyx.WindowSize) error {
			if size.Rows == 30 {
				close(resizeEntered)
				<-releaseResize
			}
			return nil
		},
	})
	client, done := startAttachConnection(t, server)
	admitAttachClient(t, client)
	writeAttachControl(t, client, AttachControl{Version: 1, Kind: AttachControlResize, Rows: 30, Cols: 100})
	<-resizeEntered
	cleanupDone := make(chan struct{})
	go func() { server.cleanupRetained(); close(cleanupDone) }()
	select {
	case <-cleanupDone:
		t.Fatal("terminal decision crossed an in-flight resize commit")
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseResize)
	final := readAttachControlKind(t, client, AttachControlFinal)
	if final.Disposition != AttachDispositionCleanupRetained {
		t.Fatalf("resize/terminal final = %#v, want cleanup-retained", final)
	}
	<-cleanupDone
	waitAttachServer(t, done)
}

func TestAttachServerSimultaneousHealthyTerminalCausesEmitOneFinal(t *testing.T) {
	relay, stopRelay := newAttachTestRelay(t)
	defer stopRelay()
	resizeEntered := make(chan struct{})
	releaseResize := make(chan struct{})
	server := newAttachServer(attachServerConfig{
		Session: "fleet", Role: "planner", ShimUID: 501,
		Relay: relay, Input: newRoleInputWriter(&recordingOperationWriter{}),
		PeerIdentity: func(net.Conn) (attachPeerIdentity, error) {
			return attachPeerIdentity{PID: 101, UID: 501}, nil
		},
		Resize: func(size ptyx.WindowSize) error {
			if size.Rows == 40 {
				close(resizeEntered)
				<-releaseResize
				return errors.New("injected resize failure")
			}
			return nil
		},
	})
	client, done := startUnixAttachConnection(t, server)
	admitAttachClient(t, client)
	resizePayload, err := EncodeAttachControl(AttachControl{
		Version: 1, Kind: AttachControlResize, Rows: 40, Cols: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteAttachFrame(client, AttachFrame{Kind: AttachFrameControl, Data: resizePayload}); err != nil {
		t.Fatal(err)
	}
	<-resizeEntered
	start := make(chan struct{})
	var causes sync.WaitGroup
	causes.Add(3)
	go func() {
		defer causes.Done()
		<-start
		server.childExited(context.Background())
	}()
	go func() {
		defer causes.Done()
		<-start
		server.cleanupRetained()
	}()
	go func() {
		defer causes.Done()
		<-start
		_ = client.CloseWrite()
	}()
	close(start)
	close(releaseResize)
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	finals := 0
	for {
		frame, readErr := ReadAttachFrame(client)
		if connectionClosedError(readErr) {
			break
		}
		if readErr != nil {
			t.Fatalf("terminal stream read = %v", readErr)
		}
		if frame.Kind != AttachFrameControl {
			continue
		}
		control, decodeErr := DecodeAttachControl(frame.Data)
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if control.Kind == AttachControlFinal {
			finals++
		}
	}
	causes.Wait()
	if finals != 1 {
		t.Fatalf("simultaneous terminal final frames = %d, want 1", finals)
	}
	select {
	case handleErr := <-done:
		if handleErr != nil && handleErr.Error() != "injected resize failure" {
			t.Fatalf("attach server error = %v", handleErr)
		}
	case <-time.After(time.Second):
		t.Fatal("attach server did not release connection")
	}
	_ = client.Close()
}

func TestAttachServerMidFrameFinalFaultYieldsNoCompleteFinal(t *testing.T) {
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
	client, shim := net.Pipe()
	fault := &attachPartialWriteConn{Conn: shim}
	done := make(chan error, 1)
	go func() {
		done <- server.handleConnection(context.Background(), fault)
		_ = fault.Close()
	}()
	admitAttachClient(t, client)
	fault.mu.Lock()
	fault.enabled = true
	fault.mu.Unlock()
	cleanupDone := make(chan struct{})
	go func() { server.cleanupRetained(); close(cleanupDone) }()
	if frame, err := ReadAttachFrame(client); err == nil {
		t.Fatalf("mid-frame fault produced complete frame %#v", frame)
	} else {
		var frameErr *FrameReadError
		if !errors.As(err, &frameErr) {
			t.Fatalf("mid-frame final error = %T %v, want FrameReadError", err, err)
		}
	}
	<-cleanupDone
	waitAttachServer(t, done)
	_ = client.Close()
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
	waitAttachCondition(t, func() bool {
		server.mu.Lock()
		defer server.mu.Unlock()
		return server.viewer != nil && server.viewer.ctx.Err() != nil
	})
	releaseDelivery()
	waitAttachServer(t, done)
	time.Sleep(25 * time.Millisecond)
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if len(writer.calls) != 0 {
		t.Fatalf("released viewer input reached PTY: %#v", writer.calls)
	}
}

func TestAttachServerStopSurvivorDiscardsStoppedInputWithoutEvictingViewer(t *testing.T) {
	relay, stopRelay := newAttachTestRelay(t)
	defer stopRelay()
	writer := &recordingOperationWriter{}
	var phaseMu sync.Mutex
	phase := shimOperationActive
	resizeSeen := make(chan struct{})
	server := newAttachServer(attachServerConfig{
		Session: "fleet", Role: "planner", ShimUID: 501,
		Relay: relay, Input: newRoleInputWriter(writer),
		PeerIdentity: func(net.Conn) (attachPeerIdentity, error) {
			return attachPeerIdentity{PID: 101, UID: 501}, nil
		},
		Resize: func(size ptyx.WindowSize) error {
			if size.Rows == 30 {
				close(resizeSeen)
			}
			return nil
		},
		Phase: func() shimOperationPhase {
			phaseMu.Lock()
			defer phaseMu.Unlock()
			return phase
		},
	})
	client, done := startAttachConnection(t, server)
	admitAttachClient(t, client)
	phaseMu.Lock()
	phase = shimOperationStopping
	phaseMu.Unlock()
	writeAttachFrame(t, client, AttachFrame{Kind: AttachFrameViewerInput, Data: []byte("discarded")})
	writeAttachControl(t, client, AttachControl{Version: 1, Kind: AttachControlResize, Rows: 30, Cols: 100})
	<-resizeSeen
	writer.mu.Lock()
	stoppedWrites := len(writer.calls)
	writer.mu.Unlock()
	if stoppedWrites != 0 {
		t.Fatalf("stopping viewer input writes = %d, want 0", stoppedWrites)
	}
	phaseMu.Lock()
	phase = shimOperationActive
	phaseMu.Unlock()
	writeAttachFrame(t, client, AttachFrame{Kind: AttachFrameViewerInput, Data: []byte("survivor")})
	waitAttachCondition(t, func() bool {
		writer.mu.Lock()
		defer writer.mu.Unlock()
		return len(writer.calls) == 1
	})
	writer.mu.Lock()
	got := string(writer.calls[0])
	writer.mu.Unlock()
	if got != "survivor" {
		t.Fatalf("survivor viewer write = %q, want survivor", got)
	}
	server.mu.Lock()
	stillAdmitted := server.viewer != nil && server.viewer.pid == 101
	server.mu.Unlock()
	if !stillAdmitted {
		t.Fatal("stop survivor evicted admitted viewer")
	}
	_ = client.Close()
	waitAttachServer(t, done)
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

type blockingAttachAdmissionRelay struct {
	*ptyx.ResidentRelay
	entered chan struct{}
	release chan struct{}
}

type attachPartialWriteConn struct {
	net.Conn
	mu      sync.Mutex
	enabled bool
}

type attachFailWriteConn struct {
	net.Conn
	mu      sync.Mutex
	enabled bool
}

func (c *attachFailWriteConn) Write(value []byte) (int, error) {
	c.mu.Lock()
	enabled := c.enabled
	c.mu.Unlock()
	if enabled {
		return 0, errors.New("injected attach write failure")
	}
	return c.Conn.Write(value)
}

func (c *attachPartialWriteConn) Write(value []byte) (int, error) {
	c.mu.Lock()
	enabled := c.enabled
	c.mu.Unlock()
	if !enabled {
		return c.Conn.Write(value)
	}
	count := len(value) / 2
	if count == 0 {
		count = 1
	}
	written, _ := c.Conn.Write(value[:count])
	return written, errors.New("injected mid-frame transport failure")
}

func (r *blockingAttachAdmissionRelay) AdmitViewer(writer ptyx.ContextWriter) (*ptyx.ResidentViewer, error) {
	close(r.entered)
	<-r.release
	return r.ResidentRelay.AdmitViewer(writer)
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

func startUnixAttachConnection(t *testing.T, server *attachServer) (*net.UnixConn, <-chan error) {
	t.Helper()
	descriptors, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	clientFile := os.NewFile(uintptr(descriptors[0]), "attach-client")
	shimFile := os.NewFile(uintptr(descriptors[1]), "attach-shim")
	clientConnection, err := net.FileConn(clientFile)
	_ = clientFile.Close()
	if err != nil {
		_ = shimFile.Close()
		t.Fatal(err)
	}
	shimConnection, err := net.FileConn(shimFile)
	_ = shimFile.Close()
	if err != nil {
		_ = clientConnection.Close()
		t.Fatal(err)
	}
	client, clientOK := clientConnection.(*net.UnixConn)
	shim, shimOK := shimConnection.(*net.UnixConn)
	if !clientOK || !shimOK {
		_ = clientConnection.Close()
		_ = shimConnection.Close()
		t.Fatal("socketpair did not produce Unix connections")
	}
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
