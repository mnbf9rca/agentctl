//go:build darwin

package fleet

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// DetachedShimRequest is the fully specified direct hidden-shim invocation.
// Its descriptors are opened and owned by the launcher, not this boundary.
type DetachedShimRequest struct {
	Executable  string
	Argv        []string
	Directory   string
	Environment []string
	Stdin       *os.File
	Stdout      *os.File
	Stderr      *os.File
}

// DetachedShimProcess is the creation-provenance and reaping boundary for a
// detached hidden shim. Wait always represents the one parent-owned wait.
type DetachedShimProcess interface {
	PID() int
	Wait() <-chan error
}

// DetachedShimStarter starts a parent-specified hidden shim without a shell.
type DetachedShimStarter interface {
	Start(DetachedShimRequest) (DetachedShimProcess, error)
}

// ExecDetachedShimStarter is the production direct os/exec boundary.
type ExecDetachedShimStarter struct{}

func (ExecDetachedShimStarter) Start(request DetachedShimRequest) (DetachedShimProcess, error) {
	if request.Executable == "" || len(request.Argv) == 0 || request.Argv[0] != request.Executable {
		return nil, errors.New("detached shim request must begin with its executable")
	}
	if request.Stdin == nil || request.Stdout == nil || request.Stderr == nil {
		return nil, errors.New("detached shim request requires parent-owned standard descriptors")
	}
	command := exec.Command(request.Executable, request.Argv[1:]...)
	command.Dir = request.Directory
	command.Env = append([]string(nil), request.Environment...)
	command.Stdin, command.Stdout, command.Stderr = request.Stdin, request.Stdout, request.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return nil, err
	}
	process := &execDetachedShimProcess{pid: command.Process.Pid, done: make(chan error, 1)}
	go func() { process.done <- command.Wait() }()
	return process, nil
}

type execDetachedShimProcess struct {
	pid  int
	done chan error
	once sync.Once
}

func (p *execDetachedShimProcess) PID() int { return p.pid }

func (p *execDetachedShimProcess) Wait() <-chan error {
	p.once.Do(func() {})
	return p.done
}
