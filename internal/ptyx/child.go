//go:build darwin

package ptyx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// StartRequest is an already-validated direct child invocation. Argv[0] is
// executed without a shell and Env is installed exactly as supplied.
type StartRequest struct {
	Argv []string
	Env  []string
	Size WindowSize
}

// ChildStarter is the injectable external-process boundary consumed by the
// resident shim.
type ChildStarter interface {
	Start(context.Context, StartRequest) (Child, error)
}

// ExecChildStarter opens a nested PTY and starts a direct os/exec child on it.
type ExecChildStarter struct {
	Opener Opener
}

// ExitObservation records a process exit that this parent actually observed.
type ExitObservation struct {
	Observed bool
	PID      int
	ExitCode int
	Signal   syscall.Signal
	Err      error
}

// SignalObservation records one signal attempt independently from exit.
type SignalObservation struct {
	Attempted      bool
	Signal         os.Signal
	ProcessGroupID int
	Err            error
}

// TerminationObservation keeps the signal attempt and child-exit observation
// as separate facts.
type TerminationObservation struct {
	Signal SignalObservation
	Exit   ExitObservation
}

// SurvivingChildError reports a child whose continued presence was observed
// after the requested signal and wait deadline.
type SurvivingChildError struct {
	PID   int
	Cause error
}

// StartedChildError reports a failure after the process started. Child carries
// the live process and PTY ownership that the caller must tear down.
type StartedChildError struct {
	Child Child
	Err   error
}

func (e *StartedChildError) Error() string {
	return fmt.Sprintf("child pid %d started before setup failed: %v", e.Child.PID(), e.Err)
}

func (e *StartedChildError) Unwrap() error {
	return e.Err
}

func (e *SurvivingChildError) Error() string {
	return fmt.Sprintf("child pid %d remains present after termination deadline: %v", e.PID, e.Cause)
}

func (e *SurvivingChildError) Unwrap() error {
	return e.Cause
}

// Child is the injectable started-process boundary consumed by the resident
// shim. Implementations own their PTY master and one wait operation.
type Child interface {
	PID() int
	Master() *os.File
	Wait(context.Context) (ExitObservation, error)
	SignalProcessGroup(os.Signal) SignalObservation
	Terminate(context.Context, os.Signal) (TerminationObservation, error)
	CloseMaster() error
}

type execChild struct {
	pid      int
	master   *os.File
	process  *os.Process
	signaler childSignaler
	done     chan struct{}
	exit     ExitObservation
}

type childSignaler interface {
	Kill(int, syscall.Signal) error
}

type realChildSignaler struct{}

func (realChildSignaler) Kill(pid int, signal syscall.Signal) error {
	return syscall.Kill(pid, signal)
}

// Start creates the PTY before fork and closes every owned descriptor if the
// process is not started. On success, the child owns the master and the parent
// closes its slave descriptor immediately.
func (s ExecChildStarter) Start(ctx context.Context, request StartRequest) (Child, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(request.Argv) == 0 || request.Argv[0] == "" {
		return nil, errors.New("child argv must include a non-empty executable")
	}
	opener := s.Opener
	if opener == nil {
		opener = NewOpener()
	}
	pair, err := opener.Open(request.Size)
	if err != nil {
		return nil, err
	}

	command := exec.Command(request.Argv[0], request.Argv[1:]...)
	command.Env = append([]string{}, request.Env...)
	command.Stdin = pair.slave
	command.Stdout = pair.slave
	command.Stderr = pair.slave
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := command.Start(); err != nil {
		return nil, errors.Join(fmt.Errorf("start PTY child: %w", err), pair.Close())
	}

	child := &execChild{
		pid:      command.Process.Pid,
		master:   pair.master,
		process:  command.Process,
		signaler: realChildSignaler{},
		done:     make(chan struct{}),
	}
	pair.master = nil
	go child.observeWait(command)

	closeErr := pair.closeSlaveDescriptor()
	if closeErr != nil {
		return nil, &StartedChildError{
			Child: child,
			Err:   fmt.Errorf("close parent PTY slave after child start: %w", closeErr),
		}
	}
	return child, nil
}

// SignalProcessGroup records one signal attempt against the child-owned
// process group. It does not claim that any process exited.
func (c *execChild) SignalProcessGroup(signal os.Signal) SignalObservation {
	result := SignalObservation{
		Attempted:      true,
		Signal:         signal,
		ProcessGroupID: c.pid,
	}
	signalValue, ok := signal.(syscall.Signal)
	if !ok {
		result.Err = fmt.Errorf("signal %T is not a syscall signal", signal)
		return result
	}
	signaler := c.signaler
	if signaler == nil {
		signaler = realChildSignaler{}
	}
	result.Err = signaler.Kill(-c.pid, signalValue)
	return result
}

// PID returns the exact PID observed from os/exec.Start.
func (c *execChild) PID() int {
	return c.pid
}

// Master returns the nested PTY master owned by this child boundary.
func (c *execChild) Master() *os.File {
	return c.master
}

// Wait waits for the one parent-owned wait observation or for ctx to end.
func (c *execChild) Wait(ctx context.Context) (ExitObservation, error) {
	select {
	case <-c.done:
		return c.exit, nil
	case <-ctx.Done():
		return ExitObservation{PID: c.pid}, ctx.Err()
	}
}

// Terminate records one signal attempt and then waits for an observed exit. A
// timeout is classified as a surviving child only after signal 0 observes the
// process still present.
func (c *execChild) Terminate(ctx context.Context, signal os.Signal) (TerminationObservation, error) {
	result := TerminationObservation{
		Signal: c.SignalProcessGroup(signal),
	}
	if result.Signal.Err != nil && !errors.Is(result.Signal.Err, os.ErrProcessDone) && !errors.Is(result.Signal.Err, syscall.ESRCH) {
		return result, fmt.Errorf("signal child process group %d with %v: %w", c.pid, signal, result.Signal.Err)
	}

	exit, err := c.Wait(ctx)
	result.Exit = exit
	if err == nil {
		return result, nil
	}
	select {
	case <-c.done:
		result.Exit = c.exit
		return result, nil
	default:
	}
	if presenceErr := c.process.Signal(syscall.Signal(0)); presenceErr == nil {
		return result, &SurvivingChildError{PID: c.pid, Cause: err}
	}
	return result, err
}

// CloseMaster closes the owned PTY master. It makes no claim about child exit.
func (c *execChild) CloseMaster() error {
	if c.master == nil {
		return nil
	}
	err := c.master.Close()
	c.master = nil
	return err
}

func (c *execChild) observeWait(command *exec.Cmd) {
	waitErr := command.Wait()
	var signal syscall.Signal
	if status, ok := command.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		signal = status.Signal()
	}
	c.exit = ExitObservation{
		Observed: true,
		PID:      c.pid,
		ExitCode: command.ProcessState.ExitCode(),
		Signal:   signal,
		Err:      waitErr,
	}
	close(c.done)
}
