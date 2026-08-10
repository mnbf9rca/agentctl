//go:build darwin

package ptyx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestRelayPreservesBinaryBytesInBothDirections(t *testing.T) {
	operatorBytes := []byte{0x00, 0xff, 'o', 'p', '\n'}
	childBytes := []byte{'c', 0x00, 0xfe, '\r', '\n'}
	operatorInput := &scriptedReader{steps: []readStep{{value: operatorBytes}, {err: io.EOF}}}
	master := newRoundTripEndpoint(len(operatorBytes), childBytes)
	operatorOutput := &bufferWriter{}
	relay := NewRelay(operatorInput, operatorOutput, master)

	if err := relay.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !bytes.Equal(master.written(), operatorBytes) {
		t.Fatalf("PTY input = %x, want %x", master.written(), operatorBytes)
	}
	if !bytes.Equal(operatorOutput.bytes(), childBytes) {
		t.Fatalf("operator output = %x, want %x", operatorOutput.bytes(), childBytes)
	}
}

func TestRelayKeepsReadingChildOutputAfterOperatorInputEOF(t *testing.T) {
	operatorInput := &scriptedReader{steps: []readStep{{err: io.EOF}}}
	master := &scriptedEndpoint{reader: scriptedReader{steps: []readStep{
		{value: []byte("final child bytes")},
		{err: io.EOF},
	}}}
	operatorOutput := &bufferWriter{}

	if err := NewRelay(operatorInput, operatorOutput, master).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := string(operatorOutput.bytes()), "final child bytes"; got != want {
		t.Fatalf("operator output = %q, want %q", got, want)
	}
}

func TestRelayCancelsBothDirectionsWhenContextEnds(t *testing.T) {
	operatorInput := &blockingReader{done: make(chan struct{})}
	master := &blockingEndpoint{done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- NewRelay(operatorInput, &bufferWriter{}, master).Run(ctx)
	}()

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not return after cancellation")
	}
	assertChannelClosed(t, operatorInput.done, "operator reader")
	assertChannelClosed(t, master.done, "master reader")
}

func TestRelayAlwaysReportsPreCanceledContext(t *testing.T) {
	for attempt := 0; attempt < 100; attempt++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		relay := NewRelay(
			&blockingReader{done: make(chan struct{})},
			&bufferWriter{},
			&blockingEndpoint{done: make(chan struct{})},
		)

		if err := relay.Run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() attempt %d error = %v, want context.Canceled", attempt, err)
		}
	}
}

func TestRelayResultReportsCancellationWhenDoneWasNotSelected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := relayResult(ctx, nil, context.Canceled, context.Canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("relayResult() error = %v, want context.Canceled", err)
	}
}

func TestRelayReportsShimSideReadAndWriteFailures(t *testing.T) {
	readFailure := errors.New("operator read failed")
	writeFailure := errors.New("operator output failed")
	tests := []struct {
		name   string
		input  ContextReader
		output ContextWriter
		master ContextReadWriter
		want   error
	}{
		{
			name:   "operator input read",
			input:  &scriptedReader{steps: []readStep{{err: readFailure}}},
			output: &bufferWriter{},
			master: &blockingEndpoint{done: make(chan struct{})},
			want:   readFailure,
		},
		{
			name:   "operator output write",
			input:  &blockingReader{done: make(chan struct{})},
			output: writerFunc(func(context.Context, []byte) (int, error) { return 0, writeFailure }),
			master: &scriptedEndpoint{reader: scriptedReader{steps: []readStep{{value: []byte("child")}}}},
			want:   writeFailure,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := NewRelay(test.input, test.output, test.master).Run(context.Background())
			if !errors.Is(err, test.want) {
				t.Fatalf("Run() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestRelayTreatsPTYEIOAsObservedChildSideClosure(t *testing.T) {
	relay := NewRelay(
		&blockingReader{done: make(chan struct{})},
		&bufferWriter{},
		&scriptedEndpoint{reader: scriptedReader{steps: []readStep{{err: syscall.EIO}}}},
	)

	if err := relay.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v, want nil for PTY EIO closure", err)
	}
}

func TestRelayTreatsConcurrentPTYReadAndWriteEIOAsChildClosure(t *testing.T) {
	relay := NewRelay(
		&scriptedReader{steps: []readStep{{value: []byte("input during child exit")}}},
		&bufferWriter{},
		closureRaceEndpoint{},
	)

	if err := relay.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v, want nil when both PTY directions observe EIO closure", err)
	}
}

func TestRelayOperatorAndControlWritesUseOneSerializationPoint(t *testing.T) {
	operatorInput := &gatedReader{
		started: make(chan struct{}),
		value:   []byte("operator-input"),
	}
	target := newOverlapDetectingEndpoint()
	relay := NewRelay(operatorInput, &bufferWriter{}, target)
	if err := relay.MarkReady(TerminalState{termiosObserved: true, termios: syscall.Termios{Lflag: syscall.ISIG}}); err != nil {
		t.Fatalf("MarkReady() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- relay.Run(ctx) }()
	<-operatorInput.started

	controlDone := make(chan error, 1)
	go func() {
		_, err := relay.Writer().Write(ctx, []byte("/clear\n"))
		controlDone <- err
	}()

	select {
	case err := <-controlDone:
		if err != nil {
			t.Fatalf("control Write() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("control Write() timed out")
	}
	if !target.waitForBytes(len("operator-input/clear\n"), time.Second) {
		t.Fatalf("serialized PTY bytes = %q, want both complete writes", target.written())
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if target.overlap {
		t.Fatal("PTY target observed overlapping writes")
	}
	got := string(target.written())
	if got != "operator-input/clear\n" && got != "/clear\noperator-input" {
		t.Fatalf("serialized PTY bytes = %q, want two non-interleaved writes", got)
	}
}

func TestRelayRefusesControlWritesUntilSettledTerminalWasObserved(t *testing.T) {
	target := &scriptedEndpoint{}
	relay := NewRelay(&blockingReader{done: make(chan struct{})}, &bufferWriter{}, target)

	if _, err := relay.Writer().Write(context.Background(), []byte("/clear\n")); !errors.Is(err, ErrControlBeforeReady) {
		t.Fatalf("early control Write() error = %v, want ErrControlBeforeReady", err)
	}
	if got := target.written(); len(got) != 0 {
		t.Fatalf("early control wrote %x, want no bytes", got)
	}
	if err := relay.MarkReady(TerminalState{termiosObserved: true, termios: syscall.Termios{Lflag: syscall.ICANON | syscall.ECHO}}); !errors.Is(err, ErrTerminalNotSettled) {
		t.Fatalf("MarkReady(cooked) error = %v, want ErrTerminalNotSettled", err)
	}
	if _, err := relay.Writer().Write(context.Background(), []byte("/compact\n")); !errors.Is(err, ErrControlBeforeReady) {
		t.Fatalf("cooked control Write() error = %v, want ErrControlBeforeReady", err)
	}

	if err := relay.MarkReady(TerminalState{termiosObserved: true, termios: syscall.Termios{Lflag: syscall.ISIG}}); err != nil {
		t.Fatalf("MarkReady(settled) error = %v", err)
	}
	if _, err := relay.Writer().Write(context.Background(), []byte("/clear\n")); err != nil {
		t.Fatalf("ready control Write() error = %v", err)
	}
	if got, want := string(target.written()), "/clear\n"; got != want {
		t.Fatalf("ready control bytes = %q, want %q", got, want)
	}
}

func TestSerializedWriterHonorsCancellationWhileAnotherWriteOwnsTheGate(t *testing.T) {
	target := &gateWriter{entered: make(chan struct{}), release: make(chan struct{})}
	writer := NewSerializedWriter(target)
	firstDone := make(chan error, 1)
	go func() {
		_, err := writer.Write(context.Background(), []byte("first"))
		firstDone <- err
	}()
	<-target.entered

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := writer.Write(ctx, []byte("second")); !errors.Is(err, context.Canceled) {
		t.Fatalf("second Write() error = %v, want context.Canceled", err)
	}
	close(target.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Write() error = %v", err)
	}
}

func TestFileEndpointReadIsDeadlineBoundWithoutInput(t *testing.T) {
	reader, writer, err := osPipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	t.Cleanup(func() {
		if err := reader.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Errorf("close pipe reader: %v", err)
		}
		if err := writer.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Errorf("close pipe writer: %v", err)
		}
	})
	endpoint, err := NewFileEndpoint(reader)
	if err != nil {
		t.Fatalf("NewFileEndpoint() error = %v", err)
	}
	t.Cleanup(func() {
		if err := endpoint.Restore(); err != nil {
			t.Errorf("Restore(): %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := endpoint.Read(ctx, make([]byte, 1)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Read() error = %v, want context.DeadlineExceeded", err)
	}
}

func TestFileEndpointRestoresExactOriginalDescriptorFlags(t *testing.T) {
	reader, writer, err := osPipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	original, err := fcntl(int(reader.Fd()), syscall.F_GETFL, 0)
	if err != nil {
		t.Fatalf("read original flags: %v", err)
	}

	endpoint, err := NewFileEndpoint(reader)
	if err != nil {
		t.Fatalf("NewFileEndpoint() error = %v", err)
	}
	during, err := fcntl(endpoint.fd, syscall.F_GETFL, 0)
	if err != nil {
		t.Fatalf("read active flags: %v", err)
	}
	if during&syscall.O_NONBLOCK == 0 {
		t.Fatalf("active flags = %#x, want O_NONBLOCK", during)
	}
	if err := endpoint.Restore(); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	restored, err := fcntl(endpoint.fd, syscall.F_GETFL, 0)
	if err != nil {
		t.Fatalf("read restored flags: %v", err)
	}
	if restored != original {
		t.Fatalf("restored flags = %#x, want exact original %#x", restored, original)
	}
}

func TestFileEndpointWriteIsDeadlineBoundWhenDescriptorIsFull(t *testing.T) {
	reader, writer, err := osPipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	endpoint, err := NewFileEndpoint(writer)
	if err != nil {
		t.Fatalf("NewFileEndpoint() error = %v", err)
	}
	t.Cleanup(func() {
		if err := endpoint.Restore(); err != nil {
			t.Errorf("Restore(): %v", err)
		}
	})

	filler := make([]byte, 32*1024)
	for {
		_, writeErr := syscall.Write(endpoint.fd, filler)
		if errors.Is(writeErr, syscall.EAGAIN) || errors.Is(writeErr, syscall.EWOULDBLOCK) {
			break
		}
		if writeErr != nil {
			t.Fatalf("fill pipe: %v", writeErr)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := endpoint.Write(ctx, []byte("blocked")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Write() error = %v, want context.DeadlineExceeded", err)
	}
}

type readStep struct {
	value []byte
	err   error
}

type scriptedReader struct {
	mu    sync.Mutex
	steps []readStep
}

func (r *scriptedReader) Read(_ context.Context, buffer []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.steps) == 0 {
		return 0, io.EOF
	}
	step := r.steps[0]
	r.steps = r.steps[1:]
	return copy(buffer, step.value), step.err
}

type scriptedEndpoint struct {
	reader scriptedReader
	writer bufferWriter
}

func (e *scriptedEndpoint) Read(ctx context.Context, buffer []byte) (int, error) {
	return e.reader.Read(ctx, buffer)
}

func (e *scriptedEndpoint) Write(ctx context.Context, value []byte) (int, error) {
	return e.writer.Write(ctx, value)
}

func (e *scriptedEndpoint) written() []byte { return e.writer.bytes() }

type roundTripEndpoint struct {
	mu        sync.Mutex
	wantInput int
	input     bytes.Buffer
	response  []byte
	sent      bool
	changed   chan struct{}
}

func newRoundTripEndpoint(wantInput int, response []byte) *roundTripEndpoint {
	return &roundTripEndpoint{
		wantInput: wantInput,
		response:  append([]byte(nil), response...),
		changed:   make(chan struct{}, 1),
	}
}

func (e *roundTripEndpoint) Read(ctx context.Context, buffer []byte) (int, error) {
	for {
		e.mu.Lock()
		if e.input.Len() >= e.wantInput {
			if e.sent {
				e.mu.Unlock()
				return 0, io.EOF
			}
			e.sent = true
			count := copy(buffer, e.response)
			e.mu.Unlock()
			return count, nil
		}
		e.mu.Unlock()
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-e.changed:
		}
	}
}

func (e *roundTripEndpoint) Write(_ context.Context, value []byte) (int, error) {
	e.mu.Lock()
	count, err := e.input.Write(value)
	e.mu.Unlock()
	select {
	case e.changed <- struct{}{}:
	default:
	}
	return count, err
}

func (e *roundTripEndpoint) written() []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]byte(nil), e.input.Bytes()...)
}

type bufferWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (w *bufferWriter) Write(_ context.Context, value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.Write(value)
}

func (w *bufferWriter) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buffer.Bytes()...)
}

type writerFunc func(context.Context, []byte) (int, error)

func (f writerFunc) Write(ctx context.Context, value []byte) (int, error) {
	return f(ctx, value)
}

type blockingReader struct {
	done chan struct{}
	once sync.Once
}

func (r *blockingReader) Read(ctx context.Context, _ []byte) (int, error) {
	<-ctx.Done()
	r.once.Do(func() { close(r.done) })
	return 0, ctx.Err()
}

type blockingEndpoint struct {
	done chan struct{}
	once sync.Once
}

type closureRaceEndpoint struct{}

func (closureRaceEndpoint) Read(ctx context.Context, _ []byte) (int, error) {
	<-ctx.Done()
	return 0, syscall.EIO
}

func (closureRaceEndpoint) Write(context.Context, []byte) (int, error) {
	return 0, syscall.EIO
}

func (e *blockingEndpoint) Read(ctx context.Context, _ []byte) (int, error) {
	<-ctx.Done()
	e.once.Do(func() { close(e.done) })
	return 0, ctx.Err()
}

func (e *blockingEndpoint) Write(ctx context.Context, value []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return len(value), nil
}

type gatedReader struct {
	started chan struct{}
	once    sync.Once
	value   []byte
	sent    bool
}

func (r *gatedReader) Read(ctx context.Context, buffer []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	if !r.sent {
		r.sent = true
		return copy(buffer, r.value), nil
	}
	<-ctx.Done()
	return 0, ctx.Err()
}

type overlapDetectingEndpoint struct {
	mu      sync.Mutex
	buffer  bytes.Buffer
	active  bool
	overlap bool
	changed chan struct{}
}

func newOverlapDetectingEndpoint() *overlapDetectingEndpoint {
	return &overlapDetectingEndpoint{changed: make(chan struct{}, 1)}
}

func (e *overlapDetectingEndpoint) Read(ctx context.Context, _ []byte) (int, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

func (e *overlapDetectingEndpoint) Write(ctx context.Context, value []byte) (int, error) {
	e.mu.Lock()
	if e.active {
		e.overlap = true
	}
	e.active = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.active = false
		e.mu.Unlock()
	}()

	for _, current := range value {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(time.Millisecond):
		}
		e.mu.Lock()
		e.buffer.WriteByte(current)
		e.mu.Unlock()
		select {
		case e.changed <- struct{}{}:
		default:
		}
	}
	return len(value), nil
}

func (e *overlapDetectingEndpoint) written() []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]byte(nil), e.buffer.Bytes()...)
}

func (e *overlapDetectingEndpoint) waitForBytes(count int, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		if len(e.written()) >= count {
			return true
		}
		select {
		case <-e.changed:
		case <-deadline.C:
			return false
		}
	}
}

type gateWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *gateWriter) Write(ctx context.Context, value []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-w.release:
		return len(value), nil
	}
}

func assertChannelClosed(t *testing.T, channel <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(time.Second):
		t.Fatalf("%s did not observe cancellation", name)
	}
}

func osPipe() (*os.File, *os.File, error) {
	return os.Pipe()
}
