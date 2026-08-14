//go:build darwin

package attach

import (
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

var attachSignalCandidates = []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT}

type SignalObservationError struct {
	Signal      syscall.Signal
	Observation string
	Cause       error
}

func (e *SignalObservationError) Error() string {
	return fmt.Sprintf("observe %s %s: %v", canonicalSignalName(e.Signal), e.Observation, e.Cause)
}
func (e *SignalObservationError) Unwrap() error { return e.Cause }

type roleSignalPlan interface {
	install() <-chan os.Signal
	stop()
	reraise(syscall.Signal) error
	close()
}

type roleSignalProvider interface {
	observe() (roleSignalPlan, error)
}

type signalSystem struct {
	lockThread         func()
	unlockThread       func()
	disposition        func(os.Signal) (bool, error)
	mask               func(syscall.Signal) (bool, error)
	notify             func(chan<- os.Signal, ...os.Signal)
	stop               func(chan<- os.Signal)
	reset              func(...os.Signal)
	defaultDisposition func(syscall.Signal) error
	kill               func(int, syscall.Signal) error
	pid                func() int
}

func realSignalSystem() signalSystem {
	return signalSystem{
		lockThread: runtime.LockOSThread, unlockThread: runtime.UnlockOSThread,
		disposition: func(value os.Signal) (bool, error) {
			signalValue, ok := value.(syscall.Signal)
			if !ok {
				return false, fmt.Errorf("signal has type %T", value)
			}
			handler, err := currentDarwinSignalDisposition(signalValue)
			return handler == darwinSignalIgnore, err
		},
		mask: func(value syscall.Signal) (bool, error) {
			mask, err := currentDarwinSignalMask()
			if err != nil {
				return false, err
			}
			return mask&(uint32(1)<<uint(value-1)) != 0, nil
		},
		notify: signal.Notify, stop: signal.Stop, reset: signal.Reset, defaultDisposition: setDarwinSignalDefault,
		kill: syscall.Kill, pid: os.Getpid,
	}
}

type signalProvider struct{ system signalSystem }

func newSignalProvider() signalProvider { return signalProvider{system: realSignalSystem()} }

func (p signalProvider) observe() (roleSignalPlan, error) {
	p.system.lockThread()
	eligible := make([]os.Signal, 0, len(attachSignalCandidates))
	for _, candidate := range attachSignalCandidates {
		ignored, err := p.system.disposition(candidate)
		if err != nil {
			p.system.unlockThread()
			return nil, &SignalObservationError{Signal: candidate, Observation: "disposition", Cause: err}
		}
		blocked, err := p.system.mask(candidate)
		if err != nil {
			p.system.unlockThread()
			return nil, &SignalObservationError{Signal: candidate, Observation: "mask", Cause: err}
		}
		if ignored || blocked {
			continue
		}
		eligible = append(eligible, candidate)
	}
	return &signalPlan{system: p.system, eligible: eligible}, nil
}

type signalPlan struct {
	system    signalSystem
	eligible  []os.Signal
	events    chan os.Signal
	installed bool
	closed    bool
}

func (p *signalPlan) install() <-chan os.Signal {
	if len(p.eligible) == 0 {
		return nil
	}
	p.events = make(chan os.Signal, 1)
	p.system.notify(p.events, p.eligible...)
	p.installed = true
	return p.events
}

func (p *signalPlan) stop() {
	if p.installed {
		p.system.stop(p.events)
		p.installed = false
	}
}

func (p *signalPlan) reraise(observed syscall.Signal) error {
	p.stop()
	p.system.reset(observed)
	if err := p.system.defaultDisposition(observed); err != nil {
		return err
	}
	return p.system.kill(p.system.pid(), observed)
}

func (p *signalPlan) close() {
	if p.closed {
		return
	}
	p.closed = true
	p.stop()
	p.system.unlockThread()
}

func currentDarwinSignalMask() (uint32, error) {
	var mask uint32
	_, _, errno := syscall.Syscall(syscall.SYS_SIGPROCMASK, uintptr(1), 0, uintptr(unsafe.Pointer(&mask)))
	if errno != 0 {
		return 0, errno
	}
	return mask, nil
}

const darwinSignalIgnore = uintptr(1)

type darwinSignalAction struct {
	handler uintptr
	mask    uint32
	flags   int32
}

func currentDarwinSignalDisposition(value syscall.Signal) (uintptr, error) {
	var action darwinSignalAction
	_, _, errno := syscall.Syscall(syscall.SYS_SIGACTION, uintptr(value), 0, uintptr(unsafe.Pointer(&action)))
	if errno != 0 {
		return 0, errno
	}
	return action.handler, nil
}

func setDarwinSignalDefault(value syscall.Signal) error {
	action := darwinSignalAction{}
	_, _, errno := syscall.Syscall(syscall.SYS_SIGACTION, uintptr(value), uintptr(unsafe.Pointer(&action)), 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func canonicalSignalName(value syscall.Signal) string {
	if name := unix.SignalName(value); name != "" {
		return name
	}
	return fmt.Sprintf("SIGNAL_NUMBER_%d", value)
}
