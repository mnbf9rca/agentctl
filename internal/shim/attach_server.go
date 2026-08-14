//go:build darwin

package shim

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/mnbf9rca/agentctl/internal/ptyx"
	"golang.org/x/sys/unix"
)

const attachPendingActions = 16

var errAttachCounterExhausted = errors.New("attach output counter exhausted")

var (
	errAttachAdmissionUnavailable = errors.New("attach admission is unavailable while role is not active")
	errAttachAdmissionEnded       = errors.New("attach admission ended before PTY commit")
)

type attachInitialResizeError struct{ err error }

func (e *attachInitialResizeError) Error() string { return e.err.Error() }
func (e *attachInitialResizeError) Unwrap() error { return e.err }

type attachPeerIdentity struct {
	PID int
	UID uint32
}

type attachServerConfig struct {
	Session      string
	Role         string
	ShimUID      uint32
	Relay        residentViewerRelay
	Input        roleInputWriter
	PeerIdentity func(net.Conn) (attachPeerIdentity, error)
	Resize       func(ptyx.WindowSize) error
	Phase        func() shimOperationPhase
	WithActive   func(func() error) error
}

type residentViewerRelay interface {
	AdmitViewer(ptyx.ContextWriter) (*ptyx.ResidentViewer, error)
	Flush(context.Context) ptyx.ResidentFlushResult
}

const AttachTailFlushTimeout = 10 * time.Second

type attachServer struct {
	config       attachServerConfig
	mu           sync.Mutex
	viewer       *attachAdmission
	terminal     bool
	terminalOnce sync.Once
}

func newAttachServer(config attachServerConfig) *attachServer {
	if config.PeerIdentity == nil {
		config.PeerIdentity = observeAttachPeerIdentity
	}
	if config.Phase == nil {
		config.Phase = func() shimOperationPhase { return shimOperationActive }
	}
	if config.WithActive == nil {
		config.WithActive = func(action func() error) error {
			if config.Phase() != shimOperationActive {
				return errAttachAdmissionUnavailable
			}
			return action()
		}
	}
	return &attachServer{config: config}
}

func observeAttachPeerIdentity(connection net.Conn) (attachPeerIdentity, error) {
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return attachPeerIdentity{}, errors.New("attach peer is not a Unix connection")
	}
	pid, err := LocalPeerPID(unixConnection)
	if err != nil {
		return attachPeerIdentity{}, err
	}
	process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return attachPeerIdentity{}, err
	}
	if process == nil || int(process.Proc.P_pid) != pid {
		return attachPeerIdentity{}, ErrShortKinfoProc
	}
	return attachPeerIdentity{PID: pid, UID: process.Eproc.Ucred.Uid}, nil
}

type attachAdmission struct {
	server       *attachServer
	pid          int
	connection   net.Conn
	ctx          context.Context
	cancel       context.CancelFunc
	viewer       *ptyx.ResidentViewer
	output       *attachOutputWriter
	finishOnce   sync.Once
	terminalDone chan struct{}
}

type attachAction struct {
	frame AttachFrame
}

type attachActionError struct {
	control AttachControl
	err     error
}

func (s *attachServer) handleConnection(ctx context.Context, connection net.Conn) error {
	if s == nil || s.config.Relay == nil || s.config.Input == nil || s.config.Resize == nil {
		return errors.New("attach server is incomplete")
	}
	if err := writeAttachControlConnection(connection, AttachControl{Version: ShimProtocolVersion, Kind: AttachControlShimHello}); err != nil {
		return err
	}
	frame, err := readAttachConnectionFrame(connection)
	if err != nil {
		return err
	}
	if frame.Kind != AttachFrameControl {
		return errors.New("attach hello must be a control frame")
	}
	hello, err := DecodeAttachControl(frame.Data)
	if err != nil || hello.Kind != AttachControlHello {
		return errors.Join(errors.New("attach client hello is invalid"), err)
	}
	if hello.Session != s.config.Session || hello.Role != s.config.Role {
		return fmt.Errorf("attach identity %q/%q differs from owned role %q/%q", hello.Session, hello.Role, s.config.Session, s.config.Role)
	}
	peer, err := s.config.PeerIdentity(connection)
	if err != nil {
		return s.refuse(connection, AttachControl{Version: 1, Kind: AttachControlRefused, Outcome: AttachRefusalPeerUnobservable, Cause: err.Error()})
	}
	if peer.UID != s.config.ShimUID {
		return s.refuse(connection, AttachControl{
			Version: 1, Kind: AttachControlRefused, Outcome: AttachRefusalPeerUnverified,
			PeerPID: peer.PID, PeerUID: peer.UID, ShimUID: s.config.ShimUID,
		})
	}
	if s.config.Phase() != shimOperationActive {
		return errAttachAdmissionUnavailable
	}
	admission, incumbent, accepting := s.claim(ctx, connection, peer.PID)
	if !accepting {
		return errors.New("attach server is terminal")
	}
	if admission == nil {
		return s.refuse(connection, AttachControl{Version: 1, Kind: AttachControlRefused, Outcome: AttachRefusalViewerPresent, ViewerPID: incumbent})
	}
	defer admission.finishUnlessLifecycleOwned(AttachControl{Version: 1, Kind: AttachControlFinal, Disposition: AttachDispositionServerClosing})

	output := &attachOutputWriter{connection: connection}
	size := ptyx.WindowSize{Rows: uint16(hello.Rows), Cols: uint16(hello.Cols)}
	var viewer *ptyx.ResidentViewer
	err = s.config.WithActive(func() error {
		var commitErr error
		viewer, commitErr = s.commitAdmission(admission, output, size)
		return commitErr
	})
	var resizeErr *attachInitialResizeError
	if errors.As(err, &resizeErr) {
		admission.releaseWithoutFinal()
		return s.refuse(connection, AttachControl{
			Version: 1, Kind: AttachControlRefused, Outcome: AttachRefusalInitialSizeFailed,
			Rows: hello.Rows, Cols: hello.Cols, Cause: resizeErr.Error(),
		})
	}
	if err != nil {
		admission.releaseWithoutFinal()
		if errors.Is(err, ptyx.ErrResidentViewerPresent) {
			return s.refuse(connection, AttachControl{Version: 1, Kind: AttachControlRefused, Outcome: AttachRefusalViewerPresent, ViewerPID: incumbent})
		}
		return err
	}

	frames := make(chan AttachFrame)
	reads := make(chan error, 1)
	go readAttachFrames(admission.ctx, connection, frames, reads)
	actions := make(chan attachAction, attachPendingActions)
	actionErrors := make(chan attachActionError, 1)
	go admission.runActions(actions, actionErrors)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-admission.ctx.Done():
			if admission.lifecycleOwned() {
				<-admission.terminalDone
			}
			return nil
		case err := <-reads:
			if connectionClosedError(err) {
				return nil
			}
			return err
		case frame := <-frames:
			select {
			case actions <- attachAction{frame: frame}:
			case <-admission.ctx.Done():
				return nil
			}
		case failure := <-actionErrors:
			admission.finish(failure.control)
			return failure.err
		case result := <-viewer.Done():
			control := AttachControl{Version: 1, Kind: AttachControlFinal, Disposition: AttachDispositionServerClosing, Bytes: output.bytesWritten()}
			switch {
			case errors.Is(result.Err, ptyx.ErrAttachLagOverflow):
				control.Disposition = AttachDispositionViewerEvicted
			case errors.Is(result.Err, errAttachCounterExhausted):
				control.Disposition = AttachDispositionCounterExhausted
			case errors.Is(result.Err, io.EOF):
				control.Disposition = AttachDispositionChildExited
			}
			admission.finish(control)
			return nil
		}
	}
}

func readAttachFrames(ctx context.Context, connection net.Conn, frames chan<- AttachFrame, result chan<- error) {
	for {
		frame, err := readAttachConnectionFrame(connection)
		if err != nil {
			result <- err
			return
		}
		select {
		case frames <- frame:
		case <-ctx.Done():
			return
		}
	}
}

func (a *attachAdmission) runActions(actions <-chan attachAction, failures chan<- attachActionError) {
	for {
		select {
		case <-a.ctx.Done():
			return
		case action := <-actions:
			frame := action.frame
			switch frame.Kind {
			case AttachFrameViewerInput:
				if a.server.config.Phase() != shimOperationActive || !a.server.current(a) {
					continue
				}
				if _, err := a.server.config.Input.WriteViewer(a.ctx, frame.Data); err != nil && a.ctx.Err() == nil {
					failures <- attachActionError{control: AttachControl{Version: 1, Kind: AttachControlFinal, Disposition: AttachDispositionServerClosing, Bytes: a.output.bytesWritten()}, err: err}
					return
				}
			case AttachFrameControl:
				control, err := DecodeAttachControl(frame.Data)
				if err != nil || control.Kind != AttachControlResize {
					failures <- attachActionError{control: AttachControl{Version: 1, Kind: AttachControlFinal, Disposition: AttachDispositionServerClosing, Bytes: a.output.bytesWritten()}, err: errors.Join(errors.New("admitted attach control is invalid"), err)}
					return
				}
				size := ptyx.WindowSize{Rows: uint16(control.Rows), Cols: uint16(control.Cols)}
				err = a.server.resizeAdmission(a, size)
				if errors.Is(err, errAttachAdmissionEnded) {
					continue
				}
				if err != nil {
					failures <- attachActionError{control: AttachControl{
						Version: 1, Kind: AttachControlFinal, Disposition: AttachDispositionResizeFailed,
						Bytes: a.output.bytesWritten(), Rows: control.Rows, Cols: control.Cols, Cause: err.Error(),
					}, err: err}
					return
				}
			default:
				failures <- attachActionError{control: AttachControl{Version: 1, Kind: AttachControlFinal, Disposition: AttachDispositionServerClosing, Bytes: a.output.bytesWritten()}, err: errors.New("invalid admitted attach frame kind")}
				return
			}
		}
	}
}

func (s *attachServer) claim(parent context.Context, connection net.Conn, pid int) (*attachAdmission, int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal {
		return nil, 0, false
	}
	if s.viewer != nil {
		return nil, s.viewer.pid, true
	}
	ctx, cancel := context.WithCancel(parent)
	admission := &attachAdmission{
		server: s, pid: pid, connection: connection, ctx: ctx, cancel: cancel,
		terminalDone: make(chan struct{}),
	}
	s.viewer = admission
	return admission, 0, true
}

func (s *attachServer) current(admission *attachAdmission) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewer == admission
}

func (s *attachServer) commitAdmission(admission *attachAdmission, output *attachOutputWriter, size ptyx.WindowSize) (*ptyx.ResidentViewer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal || s.viewer != admission || admission.ctx.Err() != nil {
		return nil, errAttachAdmissionEnded
	}
	if err := s.config.Resize(size); err != nil {
		return nil, &attachInitialResizeError{err: err}
	}
	// Keep both lifecycle decisions and role-output writes behind the complete
	// admission transaction: fixed relay viewer, then admitted control frame.
	output.mu.Lock()
	defer output.mu.Unlock()
	viewer, err := s.config.Relay.AdmitViewer(output)
	if err != nil {
		return nil, err
	}
	admission.viewer = viewer
	admission.output = output
	if err := output.writeControlLocked(AttachControl{Version: 1, Kind: AttachControlAdmitted}); err != nil {
		return viewer, err
	}
	return viewer, nil
}

func (s *attachServer) resizeAdmission(admission *attachAdmission, size ptyx.WindowSize) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal || s.viewer != admission || admission.ctx.Err() != nil {
		return errAttachAdmissionEnded
	}
	return s.config.Resize(size)
}

func (s *attachServer) release(admission *attachAdmission) {
	s.mu.Lock()
	if s.viewer == admission {
		s.viewer = nil
	}
	s.mu.Unlock()
}

func (s *attachServer) childExited(ctx context.Context) {
	admission := s.beginTerminal()
	s.terminalOnce.Do(func() {
		if admission == nil {
			return
		}
		if admission.viewer == nil {
			admission.releaseWithoutFinal()
			_ = admission.connection.Close()
			return
		}
		result := admission.viewer.Flush(ctx)
		control := AttachControl{
			Version: 1, Kind: AttachControlFinal, Disposition: AttachDispositionChildExited,
			Bytes: result.Written,
		}
		switch {
		case !result.Confirmed:
			control.Disposition = AttachDispositionTailUnconfirmed
			control.KnownUndelivered = result.Undelivered
		case result.Undelivered != 0:
			control.Disposition = AttachDispositionTailUndelivered
			control.Undelivered = result.Undelivered
		}
		admission.finish(control)
	})
}

func (s *attachServer) cleanupRetained() {
	admission := s.beginTerminal()
	s.terminalOnce.Do(func() {
		if admission == nil {
			return
		}
		admission.finish(AttachControl{
			Version: 1, Kind: AttachControlFinal, Disposition: AttachDispositionCleanupRetained,
		})
	})
}

func (s *attachServer) beginTerminal() *attachAdmission {
	s.mu.Lock()
	s.terminal = true
	admission := s.viewer
	s.mu.Unlock()
	if admission != nil {
		admission.cancel()
		admission.waitInputBarrier()
	}
	return admission
}

func (s *attachServer) refuse(connection net.Conn, control AttachControl) error {
	return writeAttachControlConnection(connection, control)
}

func (a *attachAdmission) releaseWithoutFinal() {
	a.finishOnce.Do(func() {
		a.cancel()
		a.waitInputBarrier()
		if a.viewer != nil {
			a.viewer.Release()
			a.viewer.Wait()
		}
		a.server.release(a)
		close(a.terminalDone)
	})
}

func (a *attachAdmission) finish(control AttachControl) {
	a.finishOnce.Do(func() {
		a.cancel()
		a.waitInputBarrier()
		if a.viewer != nil {
			a.viewer.Release()
			a.viewer.Wait()
		}
		a.server.release(a)
		if a.output != nil {
			control.Bytes = a.output.bytesWritten()
			_ = a.output.writeControl(control)
		}
		_ = a.connection.Close()
		close(a.terminalDone)
	})
}

func (a *attachAdmission) lifecycleOwned() bool {
	a.server.mu.Lock()
	defer a.server.mu.Unlock()
	return a.server.terminal && a.server.viewer == a
}

func (a *attachAdmission) finishUnlessLifecycleOwned(control AttachControl) {
	if a.lifecycleOwned() {
		return
	}
	a.finish(control)
}

func (a *attachAdmission) waitInputBarrier() {
	barrier, ok := a.server.config.Input.(interface{ Barrier(context.Context) error })
	if ok {
		_ = barrier.Barrier(context.Background())
	}
}

type attachOutputWriter struct {
	connection net.Conn
	mu         sync.Mutex
	bytes      uint64
}

func (w *attachOutputWriter) Write(ctx context.Context, value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if uint64(len(value)) > attachCounterMax-w.bytes {
		return 0, errAttachCounterExhausted
	}
	stop := context.AfterFunc(ctx, func() { _ = w.connection.SetWriteDeadline(time.Now()) })
	defer func() {
		stop()
		_ = w.connection.SetWriteDeadline(time.Time{})
	}()
	if err := WriteAttachFrame(w.connection, AttachFrame{Kind: AttachFrameRoleOutput, Data: value}); err != nil {
		return 0, err
	}
	w.bytes += uint64(len(value))
	return len(value), nil
}

func (w *attachOutputWriter) writeControl(control AttachControl) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writeControlLocked(control)
}

func (w *attachOutputWriter) writeControlLocked(control AttachControl) error {
	payload, err := EncodeAttachControl(control)
	if err != nil {
		return err
	}
	if err := w.connection.SetWriteDeadline(time.Now().Add(ShimProtocolIOTimeout)); err != nil {
		return err
	}
	defer func() { _ = w.connection.SetWriteDeadline(time.Time{}) }()
	return WriteAttachFrame(w.connection, AttachFrame{Kind: AttachFrameControl, Data: payload})
}

func (w *attachOutputWriter) bytesWritten() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bytes
}

func writeAttachControlFrame(writer io.Writer, control AttachControl) error {
	payload, err := EncodeAttachControl(control)
	if err != nil {
		return err
	}
	return WriteAttachFrame(writer, AttachFrame{Kind: AttachFrameControl, Data: payload})
}

func writeAttachControlConnection(connection net.Conn, control AttachControl) error {
	if err := connection.SetWriteDeadline(time.Now().Add(ShimProtocolIOTimeout)); err != nil {
		return err
	}
	defer func() { _ = connection.SetWriteDeadline(time.Time{}) }()
	return writeAttachControlFrame(connection, control)
}

func readAttachConnectionFrame(connection net.Conn) (AttachFrame, error) {
	if err := connection.SetReadDeadline(time.Time{}); err != nil {
		return AttachFrame{}, err
	}
	var first [1]byte
	count, err := connection.Read(first[:])
	if count == 0 {
		if err == nil {
			err = io.ErrNoProgress
		}
		return AttachFrame{}, err
	}
	if err := connection.SetReadDeadline(time.Now().Add(ShimProtocolIOTimeout)); err != nil {
		return AttachFrame{}, err
	}
	defer func() { _ = connection.SetReadDeadline(time.Time{}) }()
	return ReadAttachFrame(io.MultiReader(bytes.NewReader(first[:]), connection))
}
