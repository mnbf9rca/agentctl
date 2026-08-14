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
}

type residentViewerRelay interface {
	AdmitViewer(ptyx.ContextWriter) (*ptyx.ResidentViewer, error)
	Flush(context.Context) ptyx.ResidentFlushResult
}

const AttachTailFlushTimeout = 10 * time.Second

type attachServer struct {
	config attachServerConfig
	mu     sync.Mutex
	viewer *attachAdmission
}

func newAttachServer(config attachServerConfig) *attachServer {
	if config.PeerIdentity == nil {
		config.PeerIdentity = observeAttachPeerIdentity
	}
	if config.Phase == nil {
		config.Phase = func() shimOperationPhase { return shimOperationActive }
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
	server     *attachServer
	pid        int
	connection net.Conn
	ctx        context.Context
	cancel     context.CancelFunc
	viewer     *ptyx.ResidentViewer
	output     *attachOutputWriter
	finishOnce sync.Once
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
		return errors.New("attach admission is unavailable while role is not active")
	}
	admission, incumbent := s.claim(ctx, connection, peer.PID)
	if admission == nil {
		return s.refuse(connection, AttachControl{Version: 1, Kind: AttachControlRefused, Outcome: AttachRefusalViewerPresent, ViewerPID: incumbent})
	}
	defer admission.finish(AttachControl{Version: 1, Kind: AttachControlFinal, Disposition: AttachDispositionServerClosing})

	size := ptyx.WindowSize{Rows: uint16(hello.Rows), Cols: uint16(hello.Cols)}
	if err := s.config.Resize(size); err != nil {
		admission.releaseWithoutFinal()
		return s.refuse(connection, AttachControl{
			Version: 1, Kind: AttachControlRefused, Outcome: AttachRefusalInitialSizeFailed,
			Rows: hello.Rows, Cols: hello.Cols, Cause: err.Error(),
		})
	}
	output := &attachOutputWriter{connection: connection}
	viewer, err := s.config.Relay.AdmitViewer(output)
	if err != nil {
		admission.releaseWithoutFinal()
		if errors.Is(err, ptyx.ErrResidentViewerPresent) {
			return s.refuse(connection, AttachControl{Version: 1, Kind: AttachControlRefused, Outcome: AttachRefusalViewerPresent, ViewerPID: incumbent})
		}
		return err
	}
	admission.viewer = viewer
	admission.output = output
	if err := output.writeControl(AttachControl{Version: 1, Kind: AttachControlAdmitted}); err != nil {
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
		case err := <-reads:
			if connectionClosedError(err) {
				return nil
			}
			return err
		case frame := <-frames:
			select {
			case actions <- attachAction{frame: frame}:
			default:
				return errors.New("attach input action queue overflow")
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
				if !a.server.current(a) {
					continue
				}
				size := ptyx.WindowSize{Rows: uint16(control.Rows), Cols: uint16(control.Cols)}
				if err := a.server.config.Resize(size); err != nil {
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

func (s *attachServer) claim(parent context.Context, connection net.Conn, pid int) (*attachAdmission, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.viewer != nil {
		return nil, s.viewer.pid
	}
	ctx, cancel := context.WithCancel(parent)
	admission := &attachAdmission{server: s, pid: pid, connection: connection, ctx: ctx, cancel: cancel}
	s.viewer = admission
	return admission, 0
}

func (s *attachServer) current(admission *attachAdmission) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewer == admission
}

func (s *attachServer) release(admission *attachAdmission) {
	s.mu.Lock()
	if s.viewer == admission {
		s.viewer = nil
	}
	s.mu.Unlock()
}

func (s *attachServer) childExited(ctx context.Context) {
	result := s.config.Relay.Flush(ctx)
	s.mu.Lock()
	admission := s.viewer
	s.mu.Unlock()
	if admission == nil {
		return
	}
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
}

func (s *attachServer) refuse(connection net.Conn, control AttachControl) error {
	return writeAttachControlConnection(connection, control)
}

func (a *attachAdmission) releaseWithoutFinal() {
	a.finishOnce.Do(func() {
		a.server.release(a)
		a.cancel()
		if a.viewer != nil {
			a.viewer.Release()
		}
	})
}

func (a *attachAdmission) finish(control AttachControl) {
	a.finishOnce.Do(func() {
		a.server.release(a)
		a.cancel()
		if a.viewer != nil {
			a.viewer.Release()
		}
		if a.output != nil {
			control.Bytes = a.output.bytesWritten()
			_ = a.output.writeControl(control)
		}
		_ = a.connection.Close()
	})
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
	payload, err := EncodeAttachControl(control)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
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
