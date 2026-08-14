//go:build darwin

package ptyx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
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

func TestResidentRelayReadEventNeverCrossesIntoLaterViewer(t *testing.T) {
	relay := NewResidentRelay(newResidentStepReader(), &residentBufferWriter{})
	firstSink := &residentBufferWriter{}
	first, err := relay.AdmitViewer(firstSink)
	if err != nil {
		t.Fatal(err)
	}
	readForFirst := residentReadEvent{count: 3, value: []byte("old"), viewer: first.state}
	first.Release()
	first.Wait()
	secondSink := &residentBufferWriter{}
	second, err := relay.AdmitViewer(secondSink)
	if err != nil {
		t.Fatal(err)
	}
	if terminal, processErr := relay.processRead(readForFirst); terminal || processErr != nil {
		t.Fatalf("old read event processing = terminal %v, error %v", terminal, processErr)
	}
	if terminal, processErr := relay.processRead(residentReadEvent{count: 4, value: []byte("none")}); terminal || processErr != nil {
		t.Fatalf("viewerless read event processing = terminal %v, error %v", terminal, processErr)
	}
	time.Sleep(10 * time.Millisecond)
	if got := secondSink.bytes(); len(got) != 0 {
		t.Fatalf("pre-admission output reached replacement viewer: %q", got)
	}
	second.Release()
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

func TestResidentRelayFlushCompletesWithSlaveStillOpenAndNoPTYEOF(t *testing.T) {
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

func TestResidentRelayChildExitFlushDoesNotWaitForGrandchildHeldPTYSlave(t *testing.T) {
	pair, err := NewOpener().Open(WindowSize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestResidentRelayGrandchildHelper$")
	command.Env = append(os.Environ(), "GO_WANT_RESIDENT_GRANDCHILD_PARENT=1")
	command.Stdin = pair.slave
	command.Stdout = pair.slave
	command.Stderr = pair.slave
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if err := pair.closeSlaveDescriptor(); err != nil {
		t.Fatal(err)
	}
	reader, err := NewPTYReader(pair.Master())
	if err != nil {
		t.Fatal(err)
	}
	relay := NewResidentRelay(reader, &residentBufferWriter{})
	sink := &residentBufferWriter{}
	viewer, err := relay.AdmitViewer(sink)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stopRun := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- relay.Run(runCtx) }()
	grandchildPID := 0
	t.Cleanup(func() {
		if grandchildPID != 0 {
			_ = syscall.Kill(grandchildPID, syscall.SIGKILL)
		}
		stopRun()
		select {
		case <-runDone:
		case <-time.After(time.Second):
		}
		_ = pair.Close()
	})
	marker := waitResidentField(t, sink, "PARENT_EXITED_GRANDCHILD_PID=")
	grandchildPID, err = strconv.Atoi(marker)
	if err != nil || grandchildPID <= 0 {
		t.Fatalf("grandchild marker PID = %q, %v", marker, err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("parent child wait = %v", err)
	}
	flushCtx, cancelFlush := context.WithTimeout(context.Background(), time.Second)
	result := viewer.Flush(flushCtx)
	cancelFlush()
	if !result.Confirmed || result.Undelivered != 0 {
		t.Fatalf("grandchild-held-slave Flush() = %#v, want confirmed", result)
	}
	select {
	case err := <-runDone:
		t.Fatalf("resident relay waited for PTY EOF incorrectly ended early: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
}

func TestResidentRelayGrandchildHelper(t *testing.T) {
	if os.Getenv("GO_WANT_RESIDENT_GRANDCHILD_HOLD") == "1" {
		signal.Ignore(syscall.SIGHUP)
		ready := os.NewFile(3, "grandchild-ready")
		if ready != nil {
			_, _ = ready.Write([]byte{1})
			_ = ready.Close()
		}
		time.Sleep(30 * time.Second)
		return
	}
	if os.Getenv("GO_WANT_RESIDENT_GRANDCHILD_PARENT") != "1" {
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^TestResidentRelayGrandchildHelper$")
	command.Env = append(os.Environ(), "GO_WANT_RESIDENT_GRANDCHILD_HOLD=1")
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "GRANDCHILD_PIPE_ERROR=%q\n", err)
		os.Exit(2)
	}
	command.ExtraFiles = []*os.File{readyWriter}
	if err := command.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "GRANDCHILD_START_ERROR=%q\n", err)
		os.Exit(2)
	}
	_ = readyWriter.Close()
	var ready [1]byte
	if _, err := io.ReadFull(readyReader, ready[:]); err != nil {
		fmt.Fprintf(os.Stderr, "GRANDCHILD_READY_ERROR=%q\n", err)
		os.Exit(2)
	}
	_ = readyReader.Close()
	fmt.Fprintf(os.Stdout, "PARENT_EXITED_GRANDCHILD_PID=%d\n", command.Process.Pid)
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

func TestResidentRelayEOFAcknowledgedCutoffRemainsConfirmableAfterRun(t *testing.T) {
	reader := newResidentStepReader()
	relay := NewResidentRelay(reader, &residentBufferWriter{})
	sink := &residentGateWriter{entered: make(chan struct{}), release: make(chan struct{})}
	viewer, err := relay.AdmitViewer(sink)
	if err != nil {
		t.Fatal(err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- relay.Run(context.Background()) }()
	reader.send([]byte("tail"), io.EOF)
	select {
	case <-sink.entered:
	case <-time.After(time.Second):
		t.Fatal("EOF tail writer did not block")
	}
	if err := <-runDone; err != nil {
		t.Fatalf("Run() EOF error = %v", err)
	}
	flushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	result := viewer.Flush(flushCtx)
	if !result.Confirmed || result.Written != 0 || result.Undelivered != 4 {
		t.Fatalf("post-EOF Flush() = %#v, want confirmed four-byte shortfall", result)
	}
}

func TestResidentRelayFlushReportsUnconfirmedWhenDrainCannotAcknowledgeCutoff(t *testing.T) {
	for _, test := range []struct {
		name             string
		knownUndelivered uint64
	}{
		{name: "zero known loss"},
		{name: "nonzero known loss", knownUndelivered: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := newResidentStepReader()
			relay := NewResidentRelay(reader, &residentBufferWriter{})
			sink := &residentGateWriter{entered: make(chan struct{}), release: make(chan struct{})}
			viewer, err := relay.AdmitViewer(sink)
			if err != nil {
				t.Fatal(err)
			}
			runCtx, stopRun := context.WithCancel(context.Background())
			runDone := make(chan error, 1)
			go func() { runDone <- relay.Run(runCtx) }()
			if test.knownUndelivered != 0 {
				reader.send([]byte("tail"), nil)
				select {
				case <-sink.entered:
				case <-time.After(time.Second):
					t.Fatal("tail writer did not block")
				}
			}
			stopRun()
			<-runDone
			result := viewer.Flush(context.Background())
			if result.Confirmed || result.Undelivered != test.knownUndelivered {
				t.Fatalf("Flush() = %#v, want unconfirmed known-undelivered=%d", result, test.knownUndelivered)
			}
		})
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

func waitResidentField(t *testing.T, writer *residentBufferWriter, prefix string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, field := range strings.Fields(string(writer.bytes())) {
			if strings.HasPrefix(field, prefix) {
				return strings.TrimPrefix(field, prefix)
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("resident bytes %q do not contain field %q", writer.bytes(), prefix)
	return ""
}
