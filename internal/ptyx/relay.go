//go:build darwin

package ptyx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const relayBufferSize = 32 * 1024

var (
	// ErrControlBeforeReady reports an attempted control write before the
	// child terminal reached the approved settled state.
	ErrControlBeforeReady = errors.New("control delivery refused before terminal readiness")
	// ErrTerminalNotSettled reports a readiness observation that is still in
	// canonical or echo mode.
	ErrTerminalNotSettled = errors.New("terminal remains in cooked or echo mode")
)

// ContextReader is a read boundary that must return when ctx ends.
type ContextReader interface {
	Read(context.Context, []byte) (int, error)
}

// ContextWriter is a write boundary that must return when ctx ends.
type ContextWriter interface {
	Write(context.Context, []byte) (int, error)
}

// ContextReadWriter is the PTY endpoint contract used by Relay.
type ContextReadWriter interface {
	ContextReader
	ContextWriter
}

// SerializedWriter is the only byte-writing path into a child PTY. Relay input
// and later control operations share the same instance.
type SerializedWriter struct {
	target ContextWriter
	gate   chan struct{}
}

// ControlWriter gates control bytes on readiness and delegates successful
// calls to the exact SerializedWriter used for operator input.
type ControlWriter struct {
	serialized *SerializedWriter
	ready      *atomic.Bool
}

// Write refuses before readiness without touching the PTY.
func (w *ControlWriter) Write(ctx context.Context, value []byte) (int, error) {
	if !w.ready.Load() {
		return 0, ErrControlBeforeReady
	}
	return w.serialized.Write(ctx, value)
}

// NewSerializedWriter wraps target with context-aware whole-call serialization.
func NewSerializedWriter(target ContextWriter) *SerializedWriter {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &SerializedWriter{target: target, gate: gate}
}

// Write writes all of value unless an error or cancellation is observed. The
// serialization gate is held across short writes, so calls cannot interleave.
func (w *SerializedWriter) Write(ctx context.Context, value []byte) (int, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-w.gate:
	}
	defer func() { w.gate <- struct{}{} }()

	written := 0
	for written < len(value) {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		count, err := w.target.Write(ctx, value[written:])
		if count < 0 || count > len(value)-written {
			return written, fmt.Errorf("PTY writer returned invalid byte count %d", count)
		}
		written += count
		if err != nil {
			return written, err
		}
		if count == 0 {
			return written, io.ErrNoProgress
		}
	}
	return written, nil
}

// Relay copies operator input to the one serialized PTY writer and copies PTY
// output back to the operator without interpreting bytes.
type Relay struct {
	operatorInput  ContextReader
	operatorOutput ContextWriter
	master         ContextReadWriter
	serialized     *SerializedWriter
	control        *ControlWriter
	ready          atomic.Bool
}

// NewRelay constructs a byte-preserving two-way relay.
func NewRelay(operatorInput ContextReader, operatorOutput ContextWriter, master ContextReadWriter) *Relay {
	relay := &Relay{
		operatorInput:  operatorInput,
		operatorOutput: operatorOutput,
		master:         master,
		serialized:     NewSerializedWriter(master),
	}
	relay.control = &ControlWriter{serialized: relay.serialized, ready: &relay.ready}
	return relay
}

// Writer returns the exact serialization point used by relayed operator input.
func (r *Relay) Writer() *ControlWriter {
	return r.control
}

// MarkReady latches readiness only from an observed settled terminal state.
func (r *Relay) MarkReady(state TerminalState) error {
	if !state.Settled() {
		return ErrTerminalNotSettled
	}
	r.ready.Store(true)
	return nil
}

type relayDirection uint8

const (
	operatorToPTY relayDirection = iota
	ptyToOperator
)

type relayEvent struct {
	direction relayDirection
	err       error
}

// Run relays until the child side closes, an I/O failure occurs, or ctx ends.
// Operator-input EOF is a bounded half-close: output continues until the child
// side closes or ctx ends.
func (r *Relay) Run(ctx context.Context) error {
	relayCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events := make(chan relayEvent, 2)
	go func() {
		events <- relayEvent{direction: operatorToPTY, err: copyContext(relayCtx, r.serialized, r.operatorInput)}
	}()
	go func() {
		events <- relayEvent{direction: ptyToOperator, err: copyContext(relayCtx, r.operatorOutput, r.master)}
	}()

	var inputErr error
	var outputErr error
	var contextErr error
	seenInput := false
	seenOutput := false
	ctxDone := ctx.Done()
	for !seenInput || !seenOutput {
		select {
		case event := <-events:
			switch event.direction {
			case operatorToPTY:
				seenInput = true
				inputErr = event.err
				if event.err != nil && !errors.Is(event.err, io.EOF) {
					cancel()
				}
			case ptyToOperator:
				seenOutput = true
				outputErr = event.err
				cancel()
			}
		case <-ctxDone:
			contextErr = ctx.Err()
			ctxDone = nil
			cancel()
		}
	}

	return relayResult(ctx, contextErr, inputErr, outputErr)
}

func relayResult(ctx context.Context, contextErr, inputErr, outputErr error) error {
	if contextErr == nil {
		contextErr = ctx.Err()
	}
	if contextErr != nil {
		return contextErr
	}
	childSideClosed := errors.Is(outputErr, io.EOF) || errors.Is(outputErr, syscall.EIO)
	inputEndedWithChild := errors.Is(inputErr, context.Canceled) || errors.Is(inputErr, syscall.EIO)
	if childSideClosed && (inputErr == nil || errors.Is(inputErr, io.EOF) || inputEndedWithChild) {
		return nil
	}
	if inputErr != nil && !errors.Is(inputErr, io.EOF) && !errors.Is(inputErr, context.Canceled) {
		return fmt.Errorf("relay operator input: %w", inputErr)
	}
	if outputErr != nil && !errors.Is(outputErr, context.Canceled) {
		return fmt.Errorf("relay child output: %w", outputErr)
	}
	return nil
}

func copyContext(ctx context.Context, destination ContextWriter, source ContextReader) error {
	buffer := make([]byte, relayBufferSize)
	for {
		count, readErr := source.Read(ctx, buffer)
		if count < 0 || count > len(buffer) {
			return fmt.Errorf("reader returned invalid byte count %d", count)
		}
		if count > 0 {
			written, writeErr := destination.Write(ctx, buffer[:count])
			if writeErr != nil {
				return writeErr
			}
			if written != count {
				return io.ErrShortWrite
			}
		}
		if readErr != nil {
			return readErr
		}
		if count == 0 {
			return io.ErrNoProgress
		}
	}
}

// FileEndpoint performs raw non-blocking reads and writes against one file
// descriptor, making each retry interruptible by context.
type FileEndpoint struct {
	file          *os.File
	fd            int
	originalFlags int
	changedFlags  bool
	restoreOnce   sync.Once
	restoreErr    error
}

// NewFileEndpoint enables O_NONBLOCK while retaining the exact original flags
// for Restore. It does not take ownership of file.
func NewFileEndpoint(file *os.File) (*FileEndpoint, error) {
	if file == nil {
		return nil, errors.New("file endpoint requires a file")
	}
	fd := int(file.Fd())
	flags, err := fcntl(fd, syscall.F_GETFL, 0)
	if err != nil {
		return nil, fmt.Errorf("read descriptor flags: %w", err)
	}
	endpoint := &FileEndpoint{file: file, fd: fd, originalFlags: flags}
	if flags&syscall.O_NONBLOCK == 0 {
		if _, err := fcntl(fd, syscall.F_SETFL, flags|syscall.O_NONBLOCK); err != nil {
			return nil, fmt.Errorf("set descriptor non-blocking: %w", err)
		}
		endpoint.changedFlags = true
	}
	return endpoint, nil
}

// Read performs a context-bounded raw descriptor read.
func (e *FileEndpoint) Read(ctx context.Context, buffer []byte) (int, error) {
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		count, err := syscall.Read(e.fd, buffer)
		if count > 0 {
			return count, nil
		}
		if err == nil {
			return 0, io.EOF
		}
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
			if err := waitForRetry(ctx); err != nil {
				return 0, err
			}
			continue
		}
		return 0, err
	}
}

// Write performs one context-bounded raw descriptor write. SerializedWriter
// owns whole-value completion and call ordering.
func (e *FileEndpoint) Write(ctx context.Context, value []byte) (int, error) {
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		count, err := syscall.Write(e.fd, value)
		if count > 0 {
			return count, nil
		}
		if err == nil {
			return 0, io.ErrNoProgress
		}
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
			if err := waitForRetry(ctx); err != nil {
				return 0, err
			}
			continue
		}
		return 0, err
	}
}

// Restore restores descriptor flags changed by NewFileEndpoint. It does not
// close the file.
func (e *FileEndpoint) Restore() error {
	e.restoreOnce.Do(func() {
		if !e.changedFlags {
			return
		}
		_, e.restoreErr = fcntl(e.fd, syscall.F_SETFL, e.originalFlags)
		if e.restoreErr != nil {
			e.restoreErr = fmt.Errorf("restore descriptor flags: %w", e.restoreErr)
		}
	})
	return e.restoreErr
}

func fcntl(fd int, command int, argument int) (int, error) {
	result, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(command), uintptr(argument))
	if errno != 0 {
		return 0, errno
	}
	return int(result), nil
}

func waitForRetry(ctx context.Context) error {
	timer := time.NewTimer(5 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
