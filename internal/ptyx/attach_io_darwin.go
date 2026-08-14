//go:build darwin

package ptyx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
)

// ErrAttachDescriptorUnavailable reports that the borrowed os.File was closed
// or no longer exposes the descriptor registered by this primitive. Refusing
// before a raw syscall prevents an old descriptor number from reaching a newly
// opened, unrelated file.
var ErrAttachDescriptorUnavailable = errors.New("attach descriptor is closed or changed")

type attachIOSyscalls struct {
	fcntl func(int, int, int) (int, error)
	read  func(int, []byte) (int, error)
	write func(int, []byte) (int, error)
}

var realAttachIOSyscalls = attachIOSyscalls{
	fcntl: fcntl,
	read:  syscall.Read,
	write: syscall.Write,
}

// attachDescriptor borrows file; it never closes the supplied descriptor. It
// owns its kqueue and any O_NONBLOCK bit it added. Callers retain file ownership
// and must keep it open until Close returns and every started Read or Write has
// returned. A deliberately abandoned raw syscall keeps the file pinned against
// descriptor reuse and may be left only for immediate process termination.
type attachDescriptor struct {
	file         *os.File
	raw          syscall.RawConn
	fd           int
	changedFlags bool
	system       attachIOSyscalls
	waiter       *kqueueWaiter
	lifetime     context.Context
	cancel       context.CancelFunc
	closed       atomic.Bool
	closeOnce    sync.Once
	closeErr     error
}

func newAttachDescriptor(file *os.File, filter int16, ioSystem attachIOSyscalls, kqueueSystem kqueueSyscalls) (*attachDescriptor, error) {
	if file == nil {
		return nil, errors.New("attach I/O requires a file")
	}
	fdValue := file.Fd()
	if fdValue == ^uintptr(0) || uint64(fdValue) > uint64(^uint(0)>>1) {
		return nil, ErrAttachDescriptorUnavailable
	}
	fd := int(fdValue)
	raw, err := file.SyscallConn()
	if err != nil {
		return nil, fmt.Errorf("access attach descriptor: %w", err)
	}
	flags, err := ioSystem.fcntl(fd, syscall.F_GETFL, 0)
	if err != nil {
		return nil, fmt.Errorf("read attach descriptor flags: %w", err)
	}
	changed := flags&syscall.O_NONBLOCK == 0
	if changed {
		if _, err := ioSystem.fcntl(fd, syscall.F_SETFL, flags|syscall.O_NONBLOCK); err != nil {
			return nil, fmt.Errorf("set attach descriptor nonblocking: %w", err)
		}
	}
	waiter, err := newKqueueWaiter(fd, filter, kqueueSystem)
	if err != nil {
		if changed {
			err = errors.Join(err, restoreNonblocking(fd, ioSystem))
		}
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	return &attachDescriptor{
		file: file, raw: raw, fd: fd, changedFlags: changed, system: ioSystem,
		waiter: waiter, lifetime: lifetime, cancel: cancel,
	}, nil
}

func (d *attachDescriptor) operationContext(ctx context.Context) (context.Context, func()) {
	operation, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(d.lifetime, cancel)
	return operation, func() {
		stop()
		cancel()
	}
}

func (d *attachDescriptor) available() error {
	if d.closed.Load() || d.file.Fd() != uintptr(d.fd) {
		return ErrAttachDescriptorUnavailable
	}
	return nil
}

func (d *attachDescriptor) call(operation func(int) (int, error)) (int, error) {
	count := 0
	var operationErr error
	controlErr := d.raw.Control(func(fd uintptr) {
		if d.closed.Load() || fd != uintptr(d.fd) {
			operationErr = ErrAttachDescriptorUnavailable
			return
		}
		count, operationErr = operation(d.fd)
	})
	if controlErr != nil {
		return 0, errors.Join(ErrAttachDescriptorUnavailable, controlErr)
	}
	return count, operationErr
}

func attachWaitError(ctx context.Context, err error) error {
	if !errors.Is(err, errKqueueWaiterClosed) {
		return err
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	return ErrAttachDescriptorUnavailable
}

func restoreNonblocking(fd int, system attachIOSyscalls) error {
	flags, err := system.fcntl(fd, syscall.F_GETFL, 0)
	if err != nil {
		return fmt.Errorf("read attach descriptor flags for restoration: %w", err)
	}
	if flags&syscall.O_NONBLOCK == 0 {
		return nil
	}
	if _, err := system.fcntl(fd, syscall.F_SETFL, flags&^syscall.O_NONBLOCK); err != nil {
		return fmt.Errorf("restore attach descriptor blocking mode: %w", err)
	}
	return nil
}

func (d *attachDescriptor) Close() error {
	d.closeOnce.Do(func() {
		d.closed.Store(true)
		d.cancel()
		waiterErr := d.waiter.Close()
		var restoreErr error
		if d.changedFlags {
			if d.file.Fd() != uintptr(d.fd) {
				restoreErr = ErrAttachDescriptorUnavailable
			} else {
				restoreErr = restoreNonblocking(d.fd, d.system)
			}
		}
		d.closeErr = errors.Join(waiterErr, restoreErr)
	})
	return d.closeErr
}

// PTYReader transports bytes from a borrowed PTY master. Close releases the
// reader's kqueue and restores only O_NONBLOCK when the reader added it; it does
// not close the master.
type PTYReader struct {
	descriptor *attachDescriptor
}

// NewPTYReader constructs the nonblocking, kqueue-driven reader established by
// Darwin probes 1-3 and 5-7.
func NewPTYReader(master *os.File) (*PTYReader, error) {
	return newPTYReader(master, realAttachIOSyscalls, realKqueueSyscalls)
}

func newPTYReader(master *os.File, ioSystem attachIOSyscalls, kqueueSystem kqueueSyscalls) (*PTYReader, error) {
	descriptor, err := newAttachDescriptor(master, syscall.EVFILT_READ, ioSystem, kqueueSystem)
	if err != nil {
		return nil, err
	}
	return &PTYReader{descriptor: descriptor}, nil
}

// Read performs bounded raw descriptor reads. PTY EIO and zero-byte success are
// both classified as EOF; EV_EOF is observed but never awaited as an outcome.
func (r *PTYReader) Read(ctx context.Context, buffer []byte) (int, error) {
	operation, finish := r.descriptor.operationContext(ctx)
	defer finish()
	if len(buffer) == 0 {
		if err := operation.Err(); err != nil {
			return 0, err
		}
		if err := r.descriptor.available(); err != nil {
			return 0, err
		}
		return 0, nil
	}
	eofObserved := false
	for {
		if err := operation.Err(); err != nil {
			return 0, err
		}
		if err := r.descriptor.available(); err != nil {
			return 0, err
		}
		count, err := r.descriptor.call(func(fd int) (int, error) {
			return r.descriptor.system.read(fd, buffer)
		})
		if count < 0 && err != nil {
			count = 0
		}
		if count < 0 || count > len(buffer) {
			return 0, fmt.Errorf("PTY read returned invalid byte count %d", count)
		}
		if count > 0 {
			return count, nil
		}
		if err == nil || errors.Is(err, syscall.EIO) {
			return 0, io.EOF
		}
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
			if eofObserved {
				return 0, io.EOF
			}
			eofObserved, err = r.descriptor.waiter.Wait(operation)
			if err != nil {
				return 0, attachWaitError(operation, err)
			}
			continue
		}
		return 0, err
	}
}

func (r *PTYReader) Close() error { return r.descriptor.Close() }

// TerminalWriter transports bytes to a borrowed, independently opened terminal
// descriptor. It must never be constructed from inherited stdout: O_NONBLOCK is
// visible to every descriptor sharing that open-file description. Close does
// not close the terminal handle; its caller retains that ownership.
type TerminalWriter struct {
	descriptor *attachDescriptor
}

// NewTerminalWriter constructs the nonblocking, kqueue-driven writer
// established by Darwin probes 9 and 10.
func NewTerminalWriter(terminal *os.File) (*TerminalWriter, error) {
	return newTerminalWriter(terminal, realAttachIOSyscalls, realKqueueSyscalls)
}

func newTerminalWriter(terminal *os.File, ioSystem attachIOSyscalls, kqueueSystem kqueueSyscalls) (*TerminalWriter, error) {
	descriptor, err := newAttachDescriptor(terminal, syscall.EVFILT_WRITE, ioSystem, kqueueSystem)
	if err != nil {
		return nil, err
	}
	return &TerminalWriter{descriptor: descriptor}, nil
}

// Write writes the whole value or returns the exact accepted prefix with the
// cancellation or kernel error that stopped progress.
func (w *TerminalWriter) Write(ctx context.Context, value []byte) (int, error) {
	operation, finish := w.descriptor.operationContext(ctx)
	defer finish()
	if len(value) == 0 {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if err := w.descriptor.available(); err != nil {
			return 0, err
		}
		if err := operation.Err(); err != nil {
			return 0, err
		}
		return 0, nil
	}
	written := 0
	for written < len(value) {
		if err := operation.Err(); err != nil {
			return written, err
		}
		if err := w.descriptor.available(); err != nil {
			return written, err
		}
		count, err := w.descriptor.call(func(fd int) (int, error) {
			return w.descriptor.system.write(fd, value[written:])
		})
		if count < 0 && err != nil {
			count = 0
		}
		if count < 0 || count > len(value)-written {
			return written, fmt.Errorf("terminal write returned invalid byte count %d", count)
		}
		written += count
		if err == nil {
			if count == 0 {
				return written, io.ErrNoProgress
			}
			continue
		}
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
			eof, waitErr := w.descriptor.waiter.Wait(operation)
			if waitErr != nil {
				return written, attachWaitError(operation, waitErr)
			}
			if eof {
				return written, syscall.EPIPE
			}
			continue
		}
		return written, err
	}
	return written, nil
}

func (w *TerminalWriter) Close() error { return w.descriptor.Close() }
