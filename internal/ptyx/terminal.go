//go:build darwin

package ptyx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"
)

// ErrTerminalStateUnobserved refuses applying a zero or otherwise synthetic
// state that did not come from a successful terminal observation.
var ErrTerminalStateUnobserved = errors.New("terminal state was not observed")

// ErrTerminalWindowSizeUnobserved refuses treating the zero WindowSize in a
// readiness-only terminal observation as kernel-observed size evidence.
var ErrTerminalWindowSizeUnobserved = errors.New("terminal window size was not observed")

// ErrReadinessInterruptedDeadline reports that no final flag snapshot was
// observed because TIOCGETA remained interrupted through the inclusive bound.
var ErrReadinessInterruptedDeadline = errors.New("TIOCGETA remained interrupted through the 5s readiness deadline")

const (
	ReadinessPollInterval = 50 * time.Millisecond
	ReadinessTimeout      = 5 * time.Second
)

// Terminal observes nested-terminal readiness and forwards terminal state
// without inspecting terminal contents.
type Terminal interface {
	Observe(*os.File) (TerminalState, error)
	WaitReady(context.Context, *os.File) (TerminalState, error)
	ForwardWindowSize(source, destination *os.File) error
	ForwardTermios(source, destination *os.File) error
	SetTermios(*os.File, TerminalState) error
	SetWindowSize(*os.File, WindowSize) error
}

// ReadinessTimeoutError carries the final observed tty flags when the
// inclusive readiness boundary remains cooked or echoing.
type ReadinessTimeoutError struct {
	State TerminalState
}

func (e *ReadinessTimeoutError) Error() string {
	return fmt.Sprintf("harness tty was not ready after 5s; final flags ICANON=%t ECHO=%t", e.State.Canonical(), e.State.Echo())
}

// ReadinessObservationError reports a TIOCGETA failure rather than presenting
// it as a cooked-mode timeout.
type ReadinessObservationError struct {
	Cause error
}

func (e *ReadinessObservationError) Error() string { return e.Cause.Error() }

func (e *ReadinessObservationError) Unwrap() error { return e.Cause }

// TerminalState is one ioctl-only observation of terminal flags and size.
type TerminalState struct {
	termiosObserved bool
	sizeObserved    bool
	termios         syscall.Termios
	size            WindowSize
}

// Canonical reports whether ICANON was observed.
func (s TerminalState) Canonical() bool {
	return s.termios.Lflag&syscall.ICANON != 0
}

// Echo reports whether ECHO was observed.
func (s TerminalState) Echo() bool {
	return s.termios.Lflag&syscall.ECHO != 0
}

// Settled reports the approved clean-channel observation: both canonical mode
// and echo have been disabled by the child.
func (s TerminalState) Settled() bool {
	return s.termiosObserved && !s.Canonical() && !s.Echo()
}

// RelayInputState returns the observed nested mode adapted for the outer
// terminal. Disabling outer ISIG makes control characters available to the
// relay as bytes; the nested PTY's own ISIG policy then delivers the signal to
// the harness process group. All other observed termios fields are preserved.
func (s TerminalState) RelayInputState() TerminalState {
	relay := s
	relay.termios.Lflag &^= syscall.ISIG
	return relay
}

// AttachRawState returns the observed outer-terminal mode adapted for a
// byte-transparent direct attach. The original observation remains unchanged
// and is retained for exact restoration.
func (s TerminalState) AttachRawState() TerminalState {
	raw := s
	raw.termios.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	raw.termios.Oflag &^= syscall.OPOST
	raw.termios.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.termios.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.termios.Cflag |= syscall.CS8
	raw.termios.Cc[syscall.VMIN] = 1
	raw.termios.Cc[syscall.VTIME] = 0
	return raw
}

// WindowSize returns the size from the same terminal observation.
func (s TerminalState) WindowSize() (WindowSize, error) {
	if !s.sizeObserved {
		return WindowSize{}, ErrTerminalWindowSizeUnobserved
	}
	return s.size, nil
}

type terminalSystem interface {
	ioctl(uintptr, uintptr, unsafe.Pointer) error
}

type realTerminalSystem struct{}

func (realTerminalSystem) ioctl(fd, request uintptr, argument unsafe.Pointer) error {
	return rawIOCTL(fd, request, argument)
}

type darwinTerminal struct {
	system terminalSystem
	clock  readinessClock
}

// NewTerminal returns the production ioctl-only Darwin terminal boundary.
func NewTerminal() Terminal {
	return darwinTerminal{system: realTerminalSystem{}, clock: realReadinessClock{}}
}

type readinessClock interface {
	Now() time.Time
	WaitUntil(context.Context, time.Time) error
}

type realReadinessClock struct{}

func (realReadinessClock) Now() time.Time { return time.Now() }

func (realReadinessClock) WaitUntil(ctx context.Context, target time.Time) error {
	delay := time.Until(target)
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (t darwinTerminal) Observe(file *os.File) (TerminalState, error) {
	if file == nil {
		return TerminalState{}, errors.New("observe terminal: nil file")
	}
	var state TerminalState
	if err := t.ioctl(file, syscall.TIOCGETA, unsafe.Pointer(&state.termios)); err != nil {
		return TerminalState{}, fmt.Errorf("observe terminal mode: %w", err)
	}
	if err := t.ioctl(file, syscall.TIOCGWINSZ, unsafe.Pointer(&state.size)); err != nil {
		return TerminalState{}, fmt.Errorf("observe terminal window size: %w", err)
	}
	state.termiosObserved = true
	state.sizeObserved = true
	return state, nil
}

// WaitReady observes TIOCGETA at t=0, on fixed 50ms boundaries, and at the
// inclusive 5s boundary. It never inspects terminal contents or window size.
func (t darwinTerminal) WaitReady(ctx context.Context, file *os.File) (TerminalState, error) {
	if file == nil {
		return TerminalState{}, &ReadinessObservationError{Cause: errors.New("observe terminal readiness: nil file")}
	}
	clock := t.clock
	if clock == nil {
		clock = realReadinessClock{}
	}
	start := clock.Now()
	deadline := start.Add(ReadinessTimeout)
	finalObservation := int(ReadinessTimeout / ReadinessPollInterval)
	var final TerminalState
	for observation := 0; observation <= finalObservation; observation++ {
		if err := ctx.Err(); err != nil {
			return final, err
		}
		if observation > 0 {
			target := start.Add(time.Duration(observation) * ReadinessPollInterval)
			if err := clock.WaitUntil(ctx, target); err != nil {
				return final, err
			}
		}
		state, err := t.observeReadiness(ctx, file, clock, deadline)
		if err != nil {
			return final, err
		}
		final = state
		if state.Settled() {
			return state, nil
		}
	}
	return final, &ReadinessTimeoutError{State: final}
}

func (t darwinTerminal) observeReadiness(ctx context.Context, file *os.File, clock readinessClock, deadline time.Time) (TerminalState, error) {
	for {
		var termios syscall.Termios
		err := t.ioctl(file, syscall.TIOCGETA, unsafe.Pointer(&termios))
		if err == nil {
			return TerminalState{termiosObserved: true, termios: termios}, nil
		}
		if !errors.Is(err, syscall.EINTR) {
			return TerminalState{}, &ReadinessObservationError{
				Cause: fmt.Errorf("TIOCGETA failed while observing harness tty readiness: %w", err),
			}
		}
		if err := ctx.Err(); err != nil {
			return TerminalState{}, err
		}
		if !clock.Now().Before(deadline) {
			return TerminalState{}, &ReadinessObservationError{Cause: ErrReadinessInterruptedDeadline}
		}
	}
}

func (t darwinTerminal) ForwardWindowSize(source, destination *os.File) error {
	if source == nil || destination == nil {
		return errors.New("forward terminal window size: nil file")
	}
	var size WindowSize
	if err := t.ioctl(source, syscall.TIOCGWINSZ, unsafe.Pointer(&size)); err != nil {
		return fmt.Errorf("read source terminal window size: %w", err)
	}
	if err := t.SetWindowSize(destination, size); err != nil {
		return err
	}
	return nil
}

func (t darwinTerminal) ForwardTermios(source, destination *os.File) error {
	if source == nil || destination == nil {
		return errors.New("forward terminal mode: nil file")
	}
	var termios syscall.Termios
	if err := t.ioctl(source, syscall.TIOCGETA, unsafe.Pointer(&termios)); err != nil {
		return fmt.Errorf("read source terminal mode: %w", err)
	}
	return t.SetTermios(destination, TerminalState{termiosObserved: true, termios: termios})
}

// SetTermios applies a previously observed terminal mode. This supports exact
// restoration during teardown without treating a zero value as evidence.
func (t darwinTerminal) SetTermios(file *os.File, state TerminalState) error {
	if file == nil {
		return errors.New("set terminal mode: nil file")
	}
	if !state.termiosObserved {
		return ErrTerminalStateUnobserved
	}
	termios := state.termios
	if err := t.ioctl(file, syscall.TIOCSETA, unsafe.Pointer(&termios)); err != nil {
		return fmt.Errorf("set destination terminal mode: %w", err)
	}
	return nil
}

func (t darwinTerminal) SetWindowSize(file *os.File, size WindowSize) error {
	if file == nil {
		return errors.New("set terminal window size: nil file")
	}
	if err := t.ioctl(file, syscall.TIOCSWINSZ, unsafe.Pointer(&size)); err != nil {
		return fmt.Errorf("set destination terminal window size: %w", err)
	}
	return nil
}

func (t darwinTerminal) ioctl(file *os.File, request uintptr, argument unsafe.Pointer) error {
	connection, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var ioctlErr error
	if err := connection.Control(func(fd uintptr) {
		ioctlErr = t.system.ioctl(fd, request, argument)
	}); err != nil {
		return err
	}
	return ioctlErr
}
