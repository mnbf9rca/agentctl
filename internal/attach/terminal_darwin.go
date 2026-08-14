//go:build darwin

package attach

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"github.com/mnbf9rca/agentctl/internal/ptyx"
	"golang.org/x/sys/unix"
)

// DiagnosticSink attempts one bounded diagnostic emission to the destination
// inherited as stderr without requiring the caller to write descriptor 2.
type DiagnosticSink interface {
	Attempt(context.Context, []byte) (int, error)
}

type OwnedDiagnosticSink interface {
	DiagnosticSink
	Close() error
}

type relayTerminal struct {
	input      ptyx.ContextReader
	output     ptyx.ContextWriter
	diagnostic DiagnosticSink
	size       func() (ptyx.WindowSize, error)
}

type terminalIdentity struct {
	device uint64
	rdev   uint64
	inode  uint64
}

type terminalCheck struct {
	identity terminalIdentity
	state    ptyx.TerminalState
	size     ptyx.WindowSize
	name     string
}

type NotTerminalError struct{}

func (*NotTerminalError) Error() string { return "standard input and output must both be terminals" }

type TerminalMismatchError struct{}

func (*TerminalMismatchError) Error() string {
	return "standard input and standard output are different terminals"
}

type TerminalObservationError struct{ Cause error }

func (e *TerminalObservationError) Error() string { return e.Cause.Error() }
func (e *TerminalObservationError) Unwrap() error { return e.Cause }

type TerminalOpenError struct{ Cause error }

func (e *TerminalOpenError) Error() string { return e.Cause.Error() }
func (e *TerminalOpenError) Unwrap() error { return e.Cause }

type TerminalVerifyError struct {
	Stage string
	Cause error
}

func (e *TerminalVerifyError) Error() string { return fmt.Sprintf("%s: %v", e.Stage, e.Cause) }
func (e *TerminalVerifyError) Unwrap() error { return e.Cause }

type TerminalReopenMismatchError struct{ Path string }

func (e *TerminalReopenMismatchError) Error() string {
	return fmt.Sprintf("reopened terminal %q has a different identity", e.Path)
}

type terminalFactorySystem struct {
	observe func(*os.File) (ptyx.TerminalState, error)
	stat    func(*os.File) (terminalIdentity, error)
	name    func(*os.File) (string, error)
	open    func(string) (*os.File, error)
	input   func(*os.File) (*ptyx.FileEndpoint, error)
	output  func(*os.File) (*ptyx.TerminalWriter, error)
	dup     func(*os.File) (*os.File, error)
}

func realTerminalFactorySystem() terminalFactorySystem {
	terminal := ptyx.NewTerminal()
	return terminalFactorySystem{
		observe: terminal.Observe,
		stat:    fileTerminalIdentity,
		name:    terminalDescriptorPath,
		open: func(name string) (*os.File, error) {
			return os.OpenFile(name, os.O_RDWR|syscall.O_CLOEXEC, 0)
		},
		input:  ptyx.NewFileEndpoint,
		output: ptyx.NewTerminalWriter,
		dup:    duplicateDiagnostic,
	}
}

func terminalDescriptorPath(file *os.File) (string, error) {
	if file == nil {
		return "", errors.New("terminal descriptor is unavailable")
	}
	buffer := make([]byte, 1024)
	if err := withFileDescriptor(file, func(fd uintptr) error {
		_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, uintptr(unix.F_GETPATH), uintptr(unsafe.Pointer(&buffer[0])))
		if errno != 0 {
			return errno
		}
		return nil
	}); err != nil {
		return "", err
	}
	name := string(buffer)
	if index := strings.IndexByte(name, 0); index >= 0 {
		name = name[:index]
	}
	if name == "" {
		return "", errors.New("terminal descriptor path is empty")
	}
	return name, nil
}

type relayTerminalFactory struct {
	stdin  *os.File
	stdout *os.File
	stderr *os.File
	system terminalFactorySystem
}

func newRelayTerminalFactory(stdin, stdout, stderr *os.File) relayTerminalFactory {
	return relayTerminalFactory{stdin: stdin, stdout: stdout, stderr: stderr, system: realTerminalFactorySystem()}
}

func (f relayTerminalFactory) check() (terminalCheck, error) {
	if f.stdin == nil || f.stdout == nil {
		return terminalCheck{}, &NotTerminalError{}
	}
	inputState, err := f.system.observe(f.stdin)
	if err != nil {
		if errors.Is(err, syscall.ENOTTY) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENODEV) {
			return terminalCheck{}, &NotTerminalError{}
		}
		return terminalCheck{}, &TerminalObservationError{Cause: err}
	}
	if _, err := f.system.observe(f.stdout); err != nil {
		if errors.Is(err, syscall.ENOTTY) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENODEV) {
			return terminalCheck{}, &NotTerminalError{}
		}
		return terminalCheck{}, &TerminalObservationError{Cause: err}
	}
	inputIdentity, err := f.system.stat(f.stdin)
	if err != nil {
		return terminalCheck{}, &TerminalObservationError{Cause: err}
	}
	outputIdentity, err := f.system.stat(f.stdout)
	if err != nil {
		return terminalCheck{}, &TerminalObservationError{Cause: err}
	}
	if inputIdentity != outputIdentity {
		return terminalCheck{}, &TerminalMismatchError{}
	}
	size, err := inputState.WindowSize()
	if err != nil {
		return terminalCheck{}, &TerminalObservationError{Cause: err}
	}
	name, err := f.system.name(f.stdin)
	if err != nil {
		return terminalCheck{}, &TerminalObservationError{Cause: err}
	}
	return terminalCheck{identity: inputIdentity, state: inputState, size: size, name: name}, nil
}

func (f relayTerminalFactory) open(check terminalCheck) (*ownedRelayTerminal, error) {
	file, err := f.system.open(check.name)
	if err != nil {
		return nil, &TerminalOpenError{Cause: err}
	}
	closeCandidate := func(err error) (*ownedRelayTerminal, error) {
		_ = file.Close()
		return nil, err
	}
	identity, err := f.system.stat(file)
	if err != nil {
		return closeCandidate(&TerminalVerifyError{Stage: "identity-stat", Cause: err})
	}
	if identity != check.identity {
		return closeCandidate(&TerminalReopenMismatchError{Path: check.name})
	}
	input, err := f.system.input(file)
	if err != nil {
		return closeCandidate(&TerminalVerifyError{Stage: "nonblocking", Cause: err})
	}
	output, err := f.system.output(file)
	if err != nil {
		_ = input.Restore()
		return closeCandidate(&TerminalVerifyError{Stage: "nonblocking", Cause: err})
	}
	owner := &ownedRelayTerminal{file: file, input: input, output: output, terminal: ptyx.NewTerminal(), original: check.state}
	owner.relay = relayTerminal{input: input, output: output, diagnostic: outputDiagnosticSink{writer: output}, size: func() (ptyx.WindowSize, error) {
		state, err := owner.terminal.Observe(file)
		if err != nil {
			return ptyx.WindowSize{}, err
		}
		return state.WindowSize()
	}}
	if f.stderr != nil {
		if identity, statErr := f.system.stat(f.stderr); statErr == nil && identity == check.identity {
			return owner, nil
		}
		owner.relay.diagnostic = nil
		diagnostic, dupErr := f.system.dup(f.stderr)
		if dupErr != nil {
			return owner, nil
		}
		owner.diagnostic = diagnostic
		owner.relay.diagnostic = fileDiagnosticSink{file: diagnostic}
	}
	return owner, nil
}

type ownedRelayTerminal struct {
	relay      relayTerminal
	file       *os.File
	input      *ptyx.FileEndpoint
	output     *ptyx.TerminalWriter
	diagnostic *os.File
	terminal   ptyx.Terminal
	original   ptyx.TerminalState
	raw        bool
}

func (t *ownedRelayTerminal) makeRaw() error {
	if err := t.terminal.SetTermios(t.file, t.original.AttachRawState()); err != nil {
		return err
	}
	t.raw = true
	return nil
}

func (t *ownedRelayTerminal) restore() error {
	if !t.raw {
		return nil
	}
	t.raw = false
	return t.terminal.SetTermios(t.file, t.original)
}

func (t *ownedRelayTerminal) close() error {
	var diagnosticErr error
	if t.diagnostic != nil {
		diagnosticErr = t.diagnostic.Close()
	}
	return errors.Join(t.output.Close(), t.input.Restore(), diagnosticErr, t.file.Close())
}

type outputDiagnosticSink struct{ writer ptyx.ContextWriter }

func (s outputDiagnosticSink) Attempt(ctx context.Context, value []byte) (int, error) {
	return s.writer.Write(ctx, value)
}

type fileDiagnosticSink struct{ file *os.File }

func (s fileDiagnosticSink) Attempt(ctx context.Context, value []byte) (int, error) {
	type result struct {
		written int
		err     error
	}
	done := make(chan result, 1)
	go func() {
		written, err := s.file.Write(value)
		done <- result{written: written, err: err}
	}()
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case result := <-done:
		return result.written, result.err
	}
}

func (s fileDiagnosticSink) Close() error { return s.file.Close() }

type discardDiagnosticSink struct{}

func (discardDiagnosticSink) Attempt(_ context.Context, value []byte) (int, error) {
	return len(value), nil
}

// NewDiagnosticSink duplicates stderr atomically above the standard
// descriptors so a broken destination returns EPIPE instead of raising the
// Go runtime's special fd-2 SIGPIPE path.
func NewDiagnosticSink(stderr *os.File) (OwnedDiagnosticSink, error) {
	file, err := duplicateDiagnostic(stderr)
	if err != nil {
		return nil, err
	}
	return fileDiagnosticSink{file: file}, nil
}

func fileTerminalIdentity(file *os.File) (terminalIdentity, error) {
	info, err := file.Stat()
	if err != nil {
		return terminalIdentity{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return terminalIdentity{}, errors.New("terminal stat has unexpected representation")
	}
	return terminalIdentity{device: uint64(stat.Dev), rdev: uint64(stat.Rdev), inode: stat.Ino}, nil
}

func duplicateDiagnostic(file *os.File) (*os.File, error) {
	if file == nil {
		return nil, errors.New("stderr is unavailable")
	}
	fd := -1
	if err := withFileDescriptor(file, func(source uintptr) error {
		var err error
		fd, err = unix.FcntlInt(source, unix.F_DUPFD_CLOEXEC, 3)
		return err
	}); err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "agentctl-attach-diagnostic"), nil
}

func withFileDescriptor(file *os.File, operation func(uintptr) error) error {
	connection, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var operationErr error
	if err := connection.Control(func(fd uintptr) { operationErr = operation(fd) }); err != nil {
		return err
	}
	return operationErr
}
