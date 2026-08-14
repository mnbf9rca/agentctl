//go:build darwin

package ptyx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
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

	barriers      chan residentBarrier
	runDone       chan struct{}
	readCompleted atomic.Uint64
}

// NewResidentRelay constructs the detached drain and its sole serialized PTY
// input writer.
func NewResidentRelay(reader ContextReader, writer ContextWriter) *ResidentRelay {
	return &ResidentRelay{
		reader: reader, serialized: NewSerializedWriter(writer),
		barriers: make(chan residentBarrier), runDone: make(chan struct{}),
	}
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
	return r.flushViewer(ctx, viewer)
}

// ResidentViewer is one admission-bound handle. Release revokes only this
// handle; it cannot detach a replacement viewer.
type ResidentViewer struct {
	state *residentViewerState
}

// Done reports exactly one viewer result.
func (v *ResidentViewer) Done() <-chan ResidentViewerResult { return v.state.done }

// Wait observes the fixed result without consuming Done, so release ownership
// can be synchronized independently from the terminal-decision watcher.
func (v *ResidentViewer) Wait() ResidentViewerResult {
	<-v.state.finishedCh
	v.state.mu.Lock()
	defer v.state.mu.Unlock()
	return v.state.finalResult
}

// Flush establishes and drains a cutoff for this exact admission.
func (v *ResidentViewer) Flush(ctx context.Context) ResidentFlushResult {
	return v.state.owner.flushViewer(ctx, v.state)
}

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
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer close(r.runDone)
	reads := make(chan residentReadEvent)
	acknowledge := make(chan struct{}, 1)
	go r.readLoop(runCtx, reads, acknowledge)
	var processed uint64
	for {
		select {
		case <-ctx.Done():
			return r.close(ctx.Err())
		case event := <-reads:
			terminal, result := r.processRead(event)
			processed = event.sequence
			acknowledge <- struct{}{}
			if terminal {
				return result
			}
		case barrier := <-r.barriers:
			var terminal bool
			var result error
			for processed < barrier.target {
				select {
				case <-ctx.Done():
					barrier.response <- residentBarrierResult{}
					return r.close(ctx.Err())
				case event := <-reads:
					terminal, result = r.processRead(event)
					processed = event.sequence
					acknowledge <- struct{}{}
				}
			}
			barrier.response <- residentBarrierResult{confirmed: true, cutoff: barrier.viewer.cutoff()}
			if terminal {
				return result
			}
		}
	}
}

type residentReadEvent struct {
	sequence uint64
	value    []byte
	count    int
	err      error
}

type residentBarrier struct {
	target   uint64
	viewer   *residentViewerState
	response chan residentBarrierResult
}

type residentBarrierResult struct {
	confirmed bool
	cutoff    uint64
}

func (r *ResidentRelay) readLoop(ctx context.Context, reads chan<- residentReadEvent, acknowledge <-chan struct{}) {
	buffer := make([]byte, relayBufferSize)
	for {
		count, err := r.reader.Read(ctx, buffer)
		event := residentReadEvent{sequence: r.readCompleted.Add(1), count: count, err: err}
		if count > 0 && count <= len(buffer) {
			event.value = append([]byte(nil), buffer[:count]...)
		}
		select {
		case reads <- event:
		case <-ctx.Done():
			return
		}
		select {
		case <-acknowledge:
		case <-ctx.Done():
			return
		}
		if err != nil || count <= 0 || count > len(buffer) {
			return
		}
	}
}

func (r *ResidentRelay) processRead(event residentReadEvent) (bool, error) {
	if event.count < 0 || event.count > relayBufferSize {
		err := fmt.Errorf("resident PTY reader returned invalid byte count %d", event.count)
		return true, r.close(err)
	}
	if event.count > 0 {
		viewer := r.currentViewer()
		if viewer != nil {
			viewer.offer(event.value)
		}
	}
	if event.err != nil {
		if errors.Is(event.err, io.EOF) {
			r.closeAfterDrain(io.EOF)
			return true, nil
		}
		return true, r.close(event.err)
	}
	if event.count == 0 {
		return true, r.close(io.ErrNoProgress)
	}
	return false, nil
}

func (r *ResidentRelay) flushViewer(ctx context.Context, viewer *residentViewerState) ResidentFlushResult {
	request := residentBarrier{
		target: r.readCompleted.Load(), viewer: viewer,
		response: make(chan residentBarrierResult, 1),
	}
	select {
	case r.barriers <- request:
	case <-r.runDone:
		return viewer.unconfirmed(ctx)
	case <-ctx.Done():
		return viewer.unconfirmed(ctx)
	}
	select {
	case result := <-request.response:
		return viewer.finishFlushResult(ctx, result)
	case <-r.runDone:
		select {
		case result := <-request.response:
			return viewer.finishFlushResult(ctx, result)
		default:
			return viewer.unconfirmed(ctx)
		}
	case <-ctx.Done():
		select {
		case result := <-request.response:
			return viewer.finishFlushResult(ctx, result)
		default:
			return viewer.unconfirmed(ctx)
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

func (v *residentViewerState) cutoff() uint64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.ended {
		return v.finalResult.Written + v.finalResult.Undelivered
	}
	return v.written + uint64(v.buffered)
}

func (v *residentViewerState) finishFlushResult(ctx context.Context, result residentBarrierResult) ResidentFlushResult {
	if result.confirmed {
		return v.flushCutoff(ctx, result.cutoff)
	}
	return v.unconfirmed(ctx)
}

func (v *residentViewerState) flushCutoff(ctx context.Context, target uint64) ResidentFlushResult {
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

func (v *residentViewerState) unconfirmed(ctx context.Context) ResidentFlushResult {
	cause := ctx.Err()
	if cause == nil {
		cause = ErrResidentRelayClosed
	}
	v.endNow(cause)
	<-v.finishedCh
	v.mu.Lock()
	result := v.finalResult
	v.mu.Unlock()
	return ResidentFlushResult{Confirmed: false, Written: result.Written, Undelivered: result.Undelivered}
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
