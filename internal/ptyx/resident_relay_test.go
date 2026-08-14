//go:build darwin

package ptyx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func TestResidentRelayDiscardsOutputWithoutViewerAndTreatsPTYClosureAsNormal(t *testing.T) {
	reader := newResidentStepReader()
	relay := NewResidentRelay(reader, &residentBufferWriter{})
	done := make(chan error, 1)
	go func() { done <- relay.Run(context.Background()) }()

	reader.send([]byte("discarded"), nil)
	reader.send(nil, io.EOF)
	if err := waitResidentError(t, done); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestResidentRelayNeverBlocksDrainAndEvictsOnlyAboveLagBound(t *testing.T) {
	reader := newResidentStepReader()
	relay := NewResidentRelay(reader, &residentBufferWriter{})
	sink := &residentGateWriter{entered: make(chan struct{}), release: make(chan struct{})}
	viewer, err := relay.AdmitViewer(sink)
	if err != nil {
		t.Fatalf("AdmitViewer() error = %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- relay.Run(context.Background()) }()

	chunk := bytes.Repeat([]byte{'a'}, AttachLagBufferBytes/4)
	reader.send(chunk, nil)
	select {
	case <-sink.entered:
	case <-time.After(time.Second):
		t.Fatal("viewer writer never blocked on the first chunk")
	}
	reader.send(chunk, nil)
	reader.send(chunk, nil)
	reader.send(chunk, nil)
	select {
	case result := <-viewer.Done():
		t.Fatalf("viewer ended at exact lag capacity: %#v", result)
	case <-time.After(25 * time.Millisecond):
	}

	reader.send([]byte{'x'}, nil)
	result := waitResidentViewer(t, viewer.Done())
	if !errors.Is(result.Err, ErrAttachLagOverflow) {
		t.Fatalf("viewer result error = %v, want ErrAttachLagOverflow", result.Err)
	}
	if result.Written != 0 || result.Undelivered != uint64(AttachLagBufferBytes+1) {
		t.Fatalf("viewer result = %#v, want written=0 undelivered=%d", result, AttachLagBufferBytes+1)
	}

	reader.send(nil, io.EOF)
	if err := waitResidentError(t, done); err != nil {
		t.Fatalf("Run() after eviction error = %v", err)
	}
}

func TestResidentRelayReleaseFixesQueuedBytesToTheirOriginalSink(t *testing.T) {
	reader := newResidentStepReader()
	relay := NewResidentRelay(reader, &residentBufferWriter{})
	firstSink := &residentGateWriter{entered: make(chan struct{}), release: make(chan struct{})}
	first, err := relay.AdmitViewer(firstSink)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- relay.Run(context.Background()) }()

	reader.send([]byte("old-viewer"), nil)
	select {
	case <-firstSink.entered:
	case <-time.After(time.Second):
		t.Fatal("first sink never received its fixed output chunk")
	}
	first.Release()
	if result := waitResidentViewer(t, first.Done()); !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("released viewer error = %v, want context.Canceled", result.Err)
	}

	secondSink := &residentBufferWriter{}
	second, err := relay.AdmitViewer(secondSink)
	if err != nil {
		t.Fatalf("replacement AdmitViewer() error = %v", err)
	}
	reader.send([]byte("new-viewer"), nil)
	waitResidentBytes(t, secondSink, []byte("new-viewer"))
	if bytes.Contains(secondSink.bytes(), []byte("old-viewer")) {
		t.Fatalf("replacement sink received departed viewer bytes %q", secondSink.bytes())
	}
	close(firstSink.release)
	second.Release()
	reader.send(nil, io.EOF)
	if err := waitResidentError(t, done); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestResidentRelayPTYClosureEndsViewerAfterWritingQueuedOutput(t *testing.T) {
	reader := newResidentStepReader()
	relay := NewResidentRelay(reader, &residentBufferWriter{})
	sink := &residentBufferWriter{}
	viewer, err := relay.AdmitViewer(sink)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- relay.Run(context.Background()) }()

	reader.send([]byte("tail"), nil)
	reader.send(nil, io.EOF)
	if err := waitResidentError(t, done); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	result := waitResidentViewer(t, viewer.Done())
	if !errors.Is(result.Err, io.EOF) || result.Written != 4 || result.Undelivered != 0 {
		t.Fatalf("viewer result = %#v, want EOF after four written bytes", result)
	}
	if got := string(sink.bytes()); got != "tail" {
		t.Fatalf("sink bytes = %q, want tail", got)
	}
}

func TestResidentRelayCancellationReturnsPromptlyAndEndsViewer(t *testing.T) {
	reader := newResidentStepReader()
	relay := NewResidentRelay(reader, &residentBufferWriter{})
	viewer, err := relay.AdmitViewer(&residentBufferWriter{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- relay.Run(ctx) }()
	cancel()
	if err := waitResidentError(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if result := waitResidentViewer(t, viewer.Done()); !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("viewer error = %v, want context.Canceled", result.Err)
	}
}

func TestResidentRelayExposesOneSerializedWriter(t *testing.T) {
	target := &residentBufferWriter{}
	relay := NewResidentRelay(newResidentStepReader(), target)
	firstWriter := relay.Writer()
	secondWriter := relay.Writer()
	if firstWriter == nil || firstWriter != secondWriter {
		t.Fatal("Writer() did not return one stable SerializedWriter")
	}
	if _, err := relay.Writer().Write(context.Background(), []byte("input")); err != nil {
		t.Fatal(err)
	}
	if got := string(target.bytes()); got != "input" {
		t.Fatalf("PTY input = %q, want input", got)
	}
}

func TestResidentRelayFlushCompletesCountedTailWithoutWaitingForPTYEOF(t *testing.T) {
	reader := newResidentStepReader()
	relay := NewResidentRelay(reader, &residentBufferWriter{})
	sink := &residentBufferWriter{}
	viewer, err := relay.AdmitViewer(sink)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = relay.Run(ctx) }()
	reader.send([]byte("tail"), nil)
	waitResidentBytes(t, sink, []byte("tail"))
	result := relay.Flush(context.Background())
	if !result.Confirmed || result.Written != 4 || result.Undelivered != 0 {
		t.Fatalf("Flush() = %#v, want confirmed four-byte tail", result)
	}
	viewer.Release()
}

func TestResidentRelayFlushTimeoutFixesExactUndeliveredTail(t *testing.T) {
	reader := newResidentStepReader()
	relay := NewResidentRelay(reader, &residentBufferWriter{})
	sink := &residentGateWriter{entered: make(chan struct{}), release: make(chan struct{})}
	viewer, err := relay.AdmitViewer(sink)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = relay.Run(ctx) }()
	reader.send([]byte("tail"), nil)
	select {
	case <-sink.entered:
	case <-time.After(time.Second):
		t.Fatal("tail writer did not block")
	}
	flushCtx, stopFlush := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stopFlush()
	result := relay.Flush(flushCtx)
	if !result.Confirmed || result.Written != 0 || result.Undelivered != 4 {
		t.Fatalf("Flush() = %#v, want confirmed four-byte shortfall", result)
	}
	if viewerResult := waitResidentViewer(t, viewer.Done()); viewerResult.Undelivered != 4 {
		t.Fatalf("viewer result = %#v, want fixed four-byte shortfall", viewerResult)
	}
}

type residentReadStep struct {
	value []byte
	err   error
}

type residentStepReader struct {
	steps chan residentReadStep
}

func newResidentStepReader() *residentStepReader {
	return &residentStepReader{steps: make(chan residentReadStep, 8)}
}

func (r *residentStepReader) send(value []byte, err error) {
	r.steps <- residentReadStep{value: append([]byte(nil), value...), err: err}
}

func (r *residentStepReader) Read(ctx context.Context, buffer []byte) (int, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case step := <-r.steps:
		return copy(buffer, step.value), step.err
	}
}

type residentBufferWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (w *residentBufferWriter) Write(_ context.Context, value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.Write(value)
}

func (w *residentBufferWriter) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buffer.Bytes()...)
}

type residentGateWriter struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (w *residentGateWriter) Write(ctx context.Context, value []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-w.release:
		return len(value), nil
	}
}

func waitResidentError(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("resident relay did not finish")
		return nil
	}
}

func waitResidentViewer(t *testing.T, result <-chan ResidentViewerResult) ResidentViewerResult {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(time.Second):
		t.Fatal("resident viewer did not finish")
		return ResidentViewerResult{}
	}
}

func waitResidentBytes(t *testing.T, writer *residentBufferWriter, want []byte) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if bytes.Equal(writer.bytes(), want) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("resident sink bytes = %q, want %q", writer.bytes(), want)
}
