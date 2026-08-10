//go:build darwin

package shim

import (
	"context"
	"errors"
	"net"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestShimClientUsesVersionedAnswererCheckedRoundTripsForClosedOperations(t *testing.T) {
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
	claim, err := AcquireClaim(path, Advisory{Version: ShimProtocolVersion, ShimPID: os.Getpid(), Nonce: "nonce", StateRoot: path.StateRoot})
	if err != nil {
		t.Fatalf("AcquireClaim() error = %v", err)
	}
	t.Cleanup(func() { _ = claim.Close() })
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path.Socket, Net: "unix"})
	if err != nil {
		t.Fatalf("ListenUnix() error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	written := uint64(6)
	submitted := true
	signalAttempted := true
	exitObserved := true
	signal := "SIGHUP"
	state := "running"
	shimPID, childPID := os.Getpid(), os.Getpid()
	wantRequests := []Request{
		{Version: 1, Session: "fleet", Role: "planner", Operation: "observe"},
		{Version: 1, Session: "fleet", Role: "planner", Operation: "clear"},
		{Version: 1, Session: "fleet", Role: "planner", Operation: "stop"},
	}
	responses := []Response{
		{Version: 1, Outcome: OutcomeRunning, State: &state, ShimPID: &shimPID, ChildPID: &childPID},
		{Version: 1, Outcome: OutcomeDeliverySubmitted, BytesWritten: &written, SubmitObserved: &submitted},
		{Version: 1, Outcome: OutcomeStopChildExited, ChildPID: &childPID, SignalAttempted: &signalAttempted, Signal: &signal, ChildExitObserved: &exitObserved},
	}
	serverDone := make(chan error, 1)
	go func() {
		for index, want := range wantRequests {
			connection, err := listener.AcceptUnix()
			if err != nil {
				serverDone <- err
				return
			}
			hello, _ := EncodeHello()
			if _, err := WriteFrame(connection, hello); err != nil {
				serverDone <- err
				return
			}
			payload, err := ReadFrame(connection)
			if err != nil {
				serverDone <- err
				return
			}
			request, err := DecodeRequest(payload)
			if err != nil {
				serverDone <- err
				return
			}
			if !reflect.DeepEqual(request, want) {
				t.Errorf("request %d = %#v, want %#v", index, request, want)
			}
			response, _ := EncodeResponse(responses[index])
			if _, err := WriteFrame(connection, response); err != nil {
				serverDone <- err
				return
			}
			_ = connection.Close()
		}
		serverDone <- nil
	}()

	client := NewClient(namespace)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if got, err := client.Observe(ctx, "fleet", "planner"); err != nil || got.Outcome != OutcomeRunning {
		t.Fatalf("Observe() = %#v, %v", got, err)
	}
	if got, err := client.DeliverOperation(ctx, "fleet", "planner", "clear"); err != nil || got.Outcome != OutcomeDeliverySubmitted {
		t.Fatalf("DeliverOperation() = %#v, %v", got, err)
	}
	if got, err := client.Stop(ctx, "fleet", "planner"); err != nil || got.Outcome != OutcomeStopChildExited {
		t.Fatalf("Stop() = %#v, %v", got, err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("stub server error = %v", err)
	}
}

func TestShimClientRejectsForeignHelloBeforeSendingRequest(t *testing.T) {
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
	requestObserved := make(chan bool, 1)
	go func() {
		connection, _ := listener.AcceptUnix()
		defer func() { _ = connection.Close() }()
		_, _ = WriteFrame(connection, []byte(`{"version":2}`))
		_ = connection.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		buffer := make([]byte, 1)
		count, _ := connection.Read(buffer)
		requestObserved <- count != 0
	}()

	_, err = NewClient(namespace).DeliverOperation(context.Background(), "fleet", "planner", "clear")
	var skew *ProtocolSkewError
	if !errors.As(err, &skew) || skew.CanonicalObserved() != "2" {
		t.Fatalf("DeliverOperation() error = %T %v, want foreign ProtocolSkewError", err, err)
	}
	if <-requestObserved {
		t.Fatal("client sent request bytes after foreign server hello")
	}
}

func TestShimClientObservationDoesNotCreateMissingRoleDirectories(t *testing.T) {
	base := shortTempDir(t)
	namespace, err := openNamespaceRoots(namespaceRoots{Runtime: base + "/runtime", State: base + "/state"})
	if err != nil {
		t.Fatalf("openNamespaceRoots() error = %v", err)
	}
	t.Cleanup(func() { _ = namespace.Close() })

	if _, err := NewClient(namespace).Observe(context.Background(), "missing", "planner"); err == nil {
		t.Fatal("Observe() succeeded for a missing role")
	}
	for _, path := range []string{base + "/runtime/missing", base + "/state/sessions"} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Lstat(%q) error = %v, want missing after read-only observation", path, err)
		}
	}
}

func TestShimClientCancellationClosesConnectedRequest(t *testing.T) {
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

	requestRead := make(chan struct{})
	peerClosed := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			peerClosed <- acceptErr
			return
		}
		defer func() { _ = connection.Close() }()
		hello, _ := EncodeHello()
		_, _ = WriteFrame(connection, hello)
		_, _ = ReadFrame(connection)
		close(requestRead)
		var one [1]byte
		_, readErr := connection.Read(one[:])
		peerClosed <- readErr
	}()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, roundTripErr := NewClient(namespace).DeliverOperation(ctx, "fleet", "planner", "clear")
		done <- roundTripErr
	}()
	<-requestRead
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("DeliverOperation() error = %T %v, want context.Canceled", err, err)
	}
	if err := <-peerClosed; err == nil {
		t.Fatal("server peer did not observe the cancelled client connection close")
	}
}
