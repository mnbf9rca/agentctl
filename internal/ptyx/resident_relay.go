//go:build darwin

package ptyx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

// AttachLagBufferBytes is the measured per-viewer burst bound from spec
// §15.11.4. It holds 2.13 of the larger retained codex-cli repaint artifact.
const AttachLagBufferBytes = 131072

var (
	// ErrResidentViewerPresent reports an attempted second resident viewer.
	ErrResidentViewerPresent = errors.New("resident relay already has a viewer")
	// ErrAttachLagOverflow reports eviction before a viewer could throttle PTY
	// drain. The role and drain continue after this viewer-local outcome.
	ErrAttachLagOverflow = errors.New("attach viewer lag buffer overflow")
	// ErrResidentRelayClosed reports admission after the PTY drain ended.
	ErrResidentRelayClosed = errors.New("resident relay is closed")
)

// ResidentViewerResult is the relay's exact observation for one fixed viewer
// sink. Written counts bytes accepted by that sink; Undelivered counts bytes
// retained or offered to that viewer but not accepted when it ended.
type ResidentViewerResult struct {
	Err         error
	Written     uint64
	Undelivered uint64
}

// ResidentFlushResult describes the child-exit cutoff established against the
// currently admitted viewer. Confirmed means the cutoff was established; its
// Written and Undelivered counts cover exactly that cutoff.
type ResidentFlushResult struct {
	Confirmed   bool
	Written     uint64
	Undelivered uint64
}

// ResidentRelay continuously drains a PTY, discarding with no viewer and
// offering bytes without waiting when one fixed viewer sink is present.
type ResidentRelay struct {
	reader     ContextReader
	serialized *SerializedWriter

	mu       sync.Mutex
	viewer   *residentViewerState
	closed   bool
	closeErr error
}

// NewResidentRelay constructs the detached drain and its sole serialized PTY
// input writer.
func NewResidentRelay(reader ContextReader, writer ContextWriter) *ResidentRelay {
	return &ResidentRelay{reader: reader, serialized: NewSerializedWriter(writer)}
}

// Writer returns the exact serialization point shared by viewer input and
// registered control delivery.
func (r *ResidentRelay) Writer() *SerializedWriter { return r.serialized }

// Flush establishes a cutoff over bytes already offered to the current viewer
// and waits for that prefix to drain. It never waits for PTY EOF. Cancellation
// ends the viewer, fixes its accepted prefix, and returns an exact shortfall.
func (r *ResidentRelay) Flush(ctx context.Context) ResidentFlushResult {
	viewer := r.currentViewer()
	if viewer == nil {
		return ResidentFlushResult{Confirmed: true}
	}
	return viewer.flush(ctx)
}

// ResidentViewer is one admission-bound handle. Release revokes only this
// handle; it cannot detach a replacement viewer.
type ResidentViewer struct {
	state *residentViewerState
}

// Done reports exactly one viewer result.
func (v *ResidentViewer) Done() <-chan ResidentViewerResult { return v.state.done }

// Release promptly revokes the viewer and fixes queued bytes to its old sink.
func (v *ResidentViewer) Release() { v.state.endNow(context.Canceled) }

// AdmitViewer fixes future output to writer until the returned handle ends.
func (r *ResidentRelay) AdmitViewer(writer ContextWriter) (*ResidentViewer, error) {
	if writer == nil {
		return nil, errors.New("resident viewer requires a writer")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, errors.Join(ErrResidentRelayClosed, r.closeErr)
	}
	if r.viewer != nil {
		return nil, ErrResidentViewerPresent
	}
	state := newResidentViewerState(r, writer)
	r.viewer = state
	go state.writeLoop()
	return &ResidentViewer{state: state}, nil
}

// Run drains until cancellation, PTY EOF, or a PTY read failure. Viewer-local
// write failures and lag overflow never stop the drain.
func (r *ResidentRelay) Run(ctx context.Context) error {
	buffer := make([]byte, relayBufferSize)
	for {
		count, err := r.reader.Read(ctx, buffer)
		if count < 0 || count > len(buffer) {
			return r.close(fmt.Errorf("resident PTY reader returned invalid byte count %d", count))
		}
		if count > 0 {
			viewer := r.currentViewer()
			if viewer != nil {
				viewer.offer(buffer[:count])
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				r.closeAfterDrain(io.EOF)
				return nil
			}
			return r.close(err)
		}
		if count == 0 {
			return r.close(io.ErrNoProgress)
		}
	}
}

func (r *ResidentRelay) currentViewer() *residentViewerState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.viewer
}

func (r *ResidentRelay) detach(viewer *residentViewerState) {
	r.mu.Lock()
	if r.viewer == viewer {
		r.viewer = nil
	}
	r.mu.Unlock()
}

func (r *ResidentRelay) close(err error) error {
	r.mu.Lock()
	r.closed = true
	r.closeErr = err
	viewer := r.viewer
	r.mu.Unlock()
	if viewer != nil {
		viewer.endNow(err)
	}
	return err
}

func (r *ResidentRelay) closeAfterDrain(err error) {
	r.mu.Lock()
	r.closed = true
	r.closeErr = err
	viewer := r.viewer
	r.mu.Unlock()
	if viewer != nil {
		viewer.endAfterDrain(err)
	}
}

type residentViewerState struct {
	owner      *ResidentRelay
	writer     ContextWriter
	ctx        context.Context
	cancel     context.CancelFunc
	wake       chan struct{}
	progress   chan struct{}
	done       chan ResidentViewerResult
	finishedCh chan struct{}

	mu               sync.Mutex
	queue            [][]byte
	buffered         int
	written          uint64
	drainErr         error
	drainClosed      bool
	ended            bool
	writing          bool
	endRequested     bool
	endErr           error
	extraUndelivered int
	finalResult      ResidentViewerResult
	endOnce          sync.Once
}

func newResidentViewerState(owner *ResidentRelay, writer ContextWriter) *residentViewerState {
	ctx, cancel := context.WithCancel(context.Background())
	return &residentViewerState{
		owner: owner, writer: writer, ctx: ctx, cancel: cancel,
		wake: make(chan struct{}, 1), progress: make(chan struct{}, 1),
		done: make(chan ResidentViewerResult, 1), finishedCh: make(chan struct{}),
	}
}

func (v *residentViewerState) offer(value []byte) {
	v.mu.Lock()
	if v.ended || v.drainClosed {
		v.mu.Unlock()
		return
	}
	if len(value) > AttachLagBufferBytes-v.buffered {
		v.mu.Unlock()
		v.endNowWithExtra(ErrAttachLagOverflow, len(value))
		return
	}
	v.queue = append(v.queue, append([]byte(nil), value...))
	v.buffered += len(value)
	v.mu.Unlock()
	v.signal()
}

func (v *residentViewerState) endNow(err error) {
	v.endNowWithExtra(err, 0)
}

func (v *residentViewerState) endNowWithExtra(err error, extra int) {
	v.mu.Lock()
	if v.ended || v.endRequested {
		v.mu.Unlock()
		return
	}
	v.endRequested = true
	v.endErr = err
	v.extraUndelivered = extra
	writing := v.writing
	result := ResidentViewerResult{Err: err, Written: v.written, Undelivered: uint64(v.buffered + extra)}
	v.mu.Unlock()
	v.cancel()
	if writing {
		return
	}
	v.finish(result)
}

func (v *residentViewerState) endAfterDrain(err error) {
	v.mu.Lock()
	if v.ended {
		v.mu.Unlock()
		return
	}
	v.drainClosed = true
	v.drainErr = err
	empty := v.buffered == 0
	result := ResidentViewerResult{Err: err, Written: v.written}
	v.mu.Unlock()
	if empty {
		v.finish(result)
		return
	}
	v.signal()
}

func (v *residentViewerState) signal() {
	select {
	case v.wake <- struct{}{}:
	default:
	}
}

func (v *residentViewerState) writeLoop() {
	for {
		select {
		case <-v.ctx.Done():
			return
		case <-v.wake:
		}
		for {
			value, finish, result := v.next()
			if finish {
				v.finish(result)
				return
			}
			if len(value) == 0 {
				break
			}
			v.mu.Lock()
			if v.endRequested {
				v.mu.Unlock()
				v.finishRequested()
				return
			}
			v.writing = true
			v.mu.Unlock()
			count, err := v.writer.Write(v.ctx, value)
			if count < 0 || count > len(value) {
				v.mu.Lock()
				v.writing = false
				v.mu.Unlock()
				v.endNow(fmt.Errorf("resident viewer writer returned invalid byte count %d", count))
				return
			}
			v.commitWrite(count)
			if v.finishRequested() {
				return
			}
			if err != nil {
				v.endNow(err)
				return
			}
			if count == 0 {
				v.endNow(io.ErrNoProgress)
				return
			}
		}
	}
}

func (v *residentViewerState) next() ([]byte, bool, ResidentViewerResult) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.ended {
		return nil, false, ResidentViewerResult{}
	}
	if len(v.queue) != 0 {
		return v.queue[0], false, ResidentViewerResult{}
	}
	if v.drainClosed {
		return nil, true, ResidentViewerResult{Err: v.drainErr, Written: v.written}
	}
	return nil, false, ResidentViewerResult{}
}

func (v *residentViewerState) commitWrite(count int) {
	v.mu.Lock()
	v.writing = false
	if v.ended || len(v.queue) == 0 {
		v.mu.Unlock()
		return
	}
	v.written += uint64(count)
	v.buffered -= count
	if count == len(v.queue[0]) {
		v.queue[0] = nil
		v.queue = v.queue[1:]
	} else {
		v.queue[0] = v.queue[0][count:]
	}
	v.mu.Unlock()
	v.signalProgress()
}

func (v *residentViewerState) finishRequested() bool {
	v.mu.Lock()
	if !v.endRequested || v.ended {
		v.mu.Unlock()
		return false
	}
	result := ResidentViewerResult{
		Err: v.endErr, Written: v.written,
		Undelivered: uint64(v.buffered + v.extraUndelivered),
	}
	v.mu.Unlock()
	v.finish(result)
	return true
}

func (v *residentViewerState) flush(ctx context.Context) ResidentFlushResult {
	v.mu.Lock()
	target := v.written + uint64(v.buffered)
	v.mu.Unlock()
	for {
		v.mu.Lock()
		written := v.written
		ended := v.ended
		v.mu.Unlock()
		if written >= target {
			return ResidentFlushResult{Confirmed: true, Written: target}
		}
		if ended {
			return ResidentFlushResult{Confirmed: true, Written: written, Undelivered: target - written}
		}
		select {
		case <-ctx.Done():
			v.endNow(ctx.Err())
			<-v.finishedCh
		case <-v.progress:
		}
	}
}

func (v *residentViewerState) signalProgress() {
	select {
	case v.progress <- struct{}{}:
	default:
	}
}

func (v *residentViewerState) finish(result ResidentViewerResult) {
	v.endOnce.Do(func() {
		v.mu.Lock()
		v.ended = true
		v.finalResult = result
		v.queue = nil
		v.buffered = 0
		v.mu.Unlock()
		v.owner.detach(v)
		v.cancel()
		v.done <- result
		close(v.done)
		close(v.finishedCh)
		v.signalProgress()
	})
}
