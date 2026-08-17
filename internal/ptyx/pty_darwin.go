// Package ptyx owns agentctl's Darwin nested-PTY and child-process boundary.
package ptyx

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// WindowSize is the Darwin winsize layout used by TIOCGWINSZ and TIOCSWINSZ.
type WindowSize struct {
	Rows   uint16
	Cols   uint16
	XPixel uint16
	YPixel uint16
}

// Opener creates one nested PTY pair at the requested initial size.
type Opener interface {
	Open(WindowSize) (*PTY, error)
}

// PTY owns a master and slave descriptor until ownership is transferred to a
// successfully started Child.
type PTY struct {
	master     *os.File
	slave      *os.File
	slaveName  string
	closeSlave func() error
}

// Master returns the PTY master descriptor.
func (p *PTY) Master() *os.File {
	return p.master
}

// SlaveName returns the kernel-resolved PTY slave path.
func (p *PTY) SlaveName() string {
	return p.slaveName
}

// Close closes each descriptor still owned by the pair.
func (p *PTY) Close() error {
	var closeErrors []error
	if p.slave != nil {
		if err := p.closeSlaveDescriptor(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close PTY slave: %w", err))
		}
	}
	if p.master != nil {
		if err := p.master.Close(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close PTY master: %w", err))
		}
		p.master = nil
	}
	return errors.Join(closeErrors...)
}

func (p *PTY) closeSlaveDescriptor() error {
	if p.slave == nil {
		return nil
	}
	closeSlave := p.closeSlave
	if closeSlave == nil {
		closeSlave = p.slave.Close
	}
	err := closeSlave()
	p.slave = nil
	p.closeSlave = nil
	return err
}

type ptySystem interface {
	openFile(string, int, os.FileMode) (*os.File, error)
	ioctl(uintptr, uintptr, unsafe.Pointer) error
}

type realPTYSystem struct{}

func (realPTYSystem) openFile(path string, flag int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flag, perm)
}

func (realPTYSystem) ioctl(fd, request uintptr, argument unsafe.Pointer) error {
	return rawIOCTL(fd, request, argument)
}

type darwinOpener struct {
	system ptySystem
}

// NewOpener returns the production Darwin PTY opener.
func NewOpener() Opener {
	return darwinOpener{system: realPTYSystem{}}
}

func (o darwinOpener) Open(size WindowSize) (_ *PTY, resultErr error) {
	master, err := o.system.openFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}
	pair := &PTY{master: master}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, pair.Close())
		}
	}()

	if err := o.system.ioctl(master.Fd(), syscall.TIOCPTYGRANT, nil); err != nil {
		return nil, fmt.Errorf("grant PTY slave: %w", err)
	}
	if err := o.system.ioctl(master.Fd(), syscall.TIOCPTYUNLK, nil); err != nil {
		return nil, fmt.Errorf("unlock PTY slave: %w", err)
	}

	var nameBuffer [128]byte
	if err := o.system.ioctl(master.Fd(), syscall.TIOCPTYGNAME, unsafe.Pointer(&nameBuffer[0])); err != nil {
		return nil, fmt.Errorf("resolve PTY slave name: %w", err)
	}
	slaveName := nulTerminatedString(nameBuffer[:])
	if !strings.HasPrefix(slaveName, "/dev/tty") {
		return nil, fmt.Errorf("kernel returned invalid PTY slave name %q", slaveName)
	}
	pair.slaveName = slaveName

	slave, err := o.system.openFile(slaveName, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, fmt.Errorf("open PTY slave %q: %w", slaveName, err)
	}
	pair.slave = slave

	if err := o.system.ioctl(master.Fd(), syscall.TIOCSWINSZ, unsafe.Pointer(&size)); err != nil {
		return nil, fmt.Errorf("set PTY window size: %w", err)
	}
	return pair, nil
}

func rawIOCTL(fd, request uintptr, argument unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(argument))
	if errno != 0 {
		return errno
	}
	return nil
}

func nulTerminatedString(value []byte) string {
	if index := bytes.IndexByte(value, 0); index >= 0 {
		value = value[:index]
	}
	return string(value)
}
