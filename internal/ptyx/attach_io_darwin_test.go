//go:build darwin

package ptyx

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

type fcntlCall struct {
	fd      int
	command int
	value   int
}

func TestPTYReaderEnablesNonblockingAndRestoresOnlyItsBit(t *testing.T) {
	file := newTestFile(t, "reader")
	flags := 0x20
	var calls []fcntlCall
	var order []string
	ioSystem := attachIOSyscalls{
		fcntl: func(fd, command, value int) (int, error) {
			calls = append(calls, fcntlCall{fd: fd, command: command, value: value})
			if command == syscall.F_GETFL {
				return flags, nil
			}
			flags = value
			order = append(order, "restore-flags")
			return 0, nil
		},
		read:  syscall.Read,
		write: syscall.Write,
	}
	kqueueSystem := registrationOnlyKqueueSystem(&order)

	reader, err := newPTYReader(file, ioSystem, kqueueSystem)
	if err != nil {
		t.Fatalf("newPTYReader() error = %v", err)
	}
	if got, want := calls[:2], []fcntlCall{
		{fd: int(file.Fd()), command: syscall.F_GETFL},
		{fd: int(file.Fd()), command: syscall.F_SETFL, value: 0x20 | syscall.O_NONBLOCK},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("setup fcntl calls = %#v, want %#v", got, want)
	}

	// A peer changes an unrelated status bit while the reader is alive. Close
	// must preserve it and clear only O_NONBLOCK, which this object added.
	flags |= 0x40
	if err := reader.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	wantCalls := []fcntlCall{
		{fd: int(file.Fd()), command: syscall.F_GETFL},
		{fd: int(file.Fd()), command: syscall.F_SETFL, value: 0x20 | syscall.O_NONBLOCK},
		{fd: int(file.Fd()), command: syscall.F_GETFL},
		{fd: int(file.Fd()), command: syscall.F_SETFL, value: 0x60},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("all fcntl calls = %#v, want %#v", calls, wantCalls)
	}
	if !reflect.DeepEqual(order, []string{"restore-flags", "close-kqueue", "restore-flags"}) {
		t.Fatalf("close order = %v, want setup-set, close-kqueue, restore-flags", order)
	}
}

func TestPTYReaderRetriesEINTRWaitsAfterEAGAINAndClassifiesEIOAsEOF(t *testing.T) {
	file := newTestFile(t, "reader")
	readCalls := 0
	waitCalls := 0
	ioSystem := attachIOSyscalls{
		fcntl: func(_ int, command, _ int) (int, error) {
			if command == syscall.F_GETFL {
				return syscall.O_NONBLOCK, nil
			}
			return 0, nil
		},
		read: func(int, []byte) (int, error) {
			readCalls++
			switch readCalls {
			case 1:
				return 0, syscall.EINTR
			case 2:
				return 0, syscall.EAGAIN
			default:
				return 0, syscall.EIO
			}
		},
		write: syscall.Write,
	}
	kqueueSystem := kqueueSyscalls{
		open: func() (int, error) { return 111, nil },
		kevent: func(_ int, changes, events []syscall.Kevent_t, _ *syscall.Timespec) (int, error) {
			if len(events) == 0 {
				return 0, nil
			}
			waitCalls++
			events[0] = syscall.Kevent_t{Ident: uint64(file.Fd()), Filter: syscall.EVFILT_READ}
			return 1, nil
		},
		close: func(int) error { return nil },
	}
	reader, err := newPTYReader(file, ioSystem, kqueueSystem)
	if err != nil {
		t.Fatalf("newPTYReader() error = %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })

	if n, err := reader.Read(context.Background(), make([]byte, 8)); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("Read() = (%d, %v), want (0, EOF)", n, err)
	}
	if readCalls != 3 || waitCalls != 1 {
		t.Fatalf("read calls=%d wait calls=%d, want 3 and 1", readCalls, waitCalls)
	}
}

func TestPTYReaderEmptyBufferDoesNotClaimEOF(t *testing.T) {
	file := newTestFile(t, "reader")
	readCalls := 0
	ioSystem := attachIOSyscalls{
		fcntl: func(_ int, command, _ int) (int, error) {
			if command == syscall.F_GETFL {
				return syscall.O_NONBLOCK, nil
			}
			return 0, nil
		},
		read: func(int, []byte) (int, error) {
			readCalls++
			return 0, nil
		},
		write: syscall.Write,
	}
	reader, err := newPTYReader(file, ioSystem, registrationOnlyKqueueSystem(nil))
	if err != nil {
		t.Fatalf("newPTYReader() error = %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	if n, err := reader.Read(context.Background(), nil); n != 0 || err != nil {
		t.Fatalf("Read(nil) = (%d, %v), want (0, nil)", n, err)
	}
	if readCalls != 0 {
		t.Fatalf("raw read calls = %d, want 0 for empty buffer", readCalls)
	}
}

func TestPTYReaderClassifiesZeroByteRawReadAsEOF(t *testing.T) {
	file := newTestFile(t, "reader")
	ioSystem := attachIOSyscalls{
		fcntl: func(_ int, command, _ int) (int, error) {
			if command == syscall.F_GETFL {
				return syscall.O_NONBLOCK, nil
			}
			return 0, nil
		},
		read:  func(int, []byte) (int, error) { return 0, nil },
		write: syscall.Write,
	}
	reader, err := newPTYReader(file, ioSystem, registrationOnlyKqueueSystem(nil))
	if err != nil {
		t.Fatalf("newPTYReader() error = %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	if n, err := reader.Read(context.Background(), make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("Read() = (%d, %v), want (0, EOF)", n, err)
	}
}

func TestPTYReaderLeavesPreexistingNonblockingModeUntouched(t *testing.T) {
	file := newTestFile(t, "reader")
	var calls []fcntlCall
	ioSystem := attachIOSyscalls{
		fcntl: func(fd, command, value int) (int, error) {
			calls = append(calls, fcntlCall{fd: fd, command: command, value: value})
			return syscall.O_NONBLOCK, nil
		},
		read:  syscall.Read,
		write: syscall.Write,
	}
	reader, err := newPTYReader(file, ioSystem, registrationOnlyKqueueSystem(nil))
	if err != nil {
		t.Fatalf("newPTYReader() error = %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	want := []fcntlCall{{fd: int(file.Fd()), command: syscall.F_GETFL}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("fcntl calls = %#v, want only initial F_GETFL %#v", calls, want)
	}
}

func TestPTYReaderDrainsOnceAfterKqueueEOFThenReturnsEOF(t *testing.T) {
	file := newTestFile(t, "reader")
	readCalls := 0
	ioSystem := attachIOSyscalls{
		fcntl: func(_ int, command, _ int) (int, error) {
			if command == syscall.F_GETFL {
				return syscall.O_NONBLOCK, nil
			}
			return 0, nil
		},
		read: func(int, []byte) (int, error) {
			readCalls++
			return -1, syscall.EAGAIN
		},
		write: syscall.Write,
	}
	kqueueSystem := kqueueSyscalls{
		open: func() (int, error) { return 114, nil },
		kevent: func(_ int, changes, events []syscall.Kevent_t, _ *syscall.Timespec) (int, error) {
			if len(events) == 0 {
				return 0, nil
			}
			events[0] = syscall.Kevent_t{Ident: uint64(file.Fd()), Filter: syscall.EVFILT_READ, Flags: syscall.EV_EOF}
			return 1, nil
		},
		close: func(int) error { return nil },
	}
	reader, err := newPTYReader(file, ioSystem, kqueueSystem)
	if err != nil {
		t.Fatalf("newPTYReader() error = %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	if n, err := reader.Read(context.Background(), make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("Read() = (%d, %v), want (0, EOF)", n, err)
	}
	if readCalls != 2 {
		t.Fatalf("raw read calls = %d, want 2 (one final drain after EV_EOF)", readCalls)
	}
}

func TestPTYReaderCancellationIsNotStarvedByContinuouslyReadablePTY(t *testing.T) {
	pair, err := NewOpener().Open(WindowSize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = pair.Close() })
	slaveFD := int(pair.slave.Fd())
	slaveFlags, err := fcntl(slaveFD, syscall.F_GETFL, 0)
	if err != nil {
		t.Fatalf("read slave flags: %v", err)
	}
	if _, err := fcntl(slaveFD, syscall.F_SETFL, slaveFlags|syscall.O_NONBLOCK); err != nil {
		t.Fatalf("set slave nonblocking: %v", err)
	}
	reader, err := NewPTYReader(pair.Master())
	if err != nil {
		t.Fatalf("NewPTYReader() error = %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		for ctx.Err() == nil {
			_, writeErr := syscall.Write(slaveFD, []byte("x"))
			if writeErr != nil && !errors.Is(writeErr, syscall.EAGAIN) && !errors.Is(writeErr, syscall.EINTR) {
				return
			}
		}
	}()
	reads := make(chan int, 1)
	readErr := make(chan error, 1)
	go func() {
		count := 0
		buffer := make([]byte, 1)
		for {
			if _, err := reader.Read(ctx, buffer); err != nil {
				reads <- count
				readErr <- err
				return
			}
			count++
			if count == 100 {
				cancel()
			}
		}
	}()
	select {
	case err := <-readErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("read loop error = %v, want context.Canceled", err)
		}
		if got := <-reads; got < 100 {
			t.Fatalf("read loop completed %d reads, want at least 100", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("continuous readability starved cancellation")
	}
	<-producerDone
}

func TestPTYReaderCancelsParkedReadOnRealBlockingOpenedPTY(t *testing.T) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open /dev/ptmx: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })
	masterFD := int(master.Fd())
	if err := rawIOCTL(uintptr(masterFD), syscall.TIOCPTYGRANT, nil); err != nil {
		t.Fatalf("grant PTY slave: %v", err)
	}
	if err := rawIOCTL(uintptr(masterFD), syscall.TIOCPTYUNLK, nil); err != nil {
		t.Fatalf("unlock PTY slave: %v", err)
	}
	var slaveName [128]byte
	if err := rawIOCTL(uintptr(masterFD), syscall.TIOCPTYGNAME, unsafe.Pointer(&slaveName[0])); err != nil {
		t.Fatalf("resolve PTY slave name: %v", err)
	}
	slave, err := os.OpenFile(nulTerminatedString(slaveName[:]), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open PTY slave: %v", err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	slaveFD := int(slave.Fd())

	reader, err := NewPTYReader(master)
	if err != nil {
		t.Fatalf("NewPTYReader() error = %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	type readResult struct {
		count int
		err   error
	}
	done := make(chan readResult, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		count, readErr := reader.Read(ctx, make([]byte, 1))
		done <- readResult{count: count, err: readErr}
	}()
	<-started
	select {
	case result := <-done:
		t.Fatalf("Read() returned before cancellation: (%d, %v)", result.count, result.err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case result := <-done:
		if result.count != 0 || !errors.Is(result.err, context.Canceled) {
			t.Fatalf("Read() = (%d, %v), want (0, context.Canceled)", result.count, result.err)
		}
	case <-time.After(2 * time.Second):
		_ = reader.Close()
		_, _ = syscall.Write(slaveFD, []byte("release"))
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("timed-out Read could not be released through its isolated PTY slave")
		}
		t.Fatal("Read() did not return after caller context cancellation")
	}
}

func TestTerminalWriterCompletesPartialWritesWithEINTRAndKqueueRetry(t *testing.T) {
	file := newTestFile(t, "terminal")
	writes := 0
	waits := 0
	var accepted []byte
	ioSystem := attachIOSyscalls{
		fcntl: func(_ int, command, _ int) (int, error) {
			if command == syscall.F_GETFL {
				return syscall.O_NONBLOCK, nil
			}
			return 0, nil
		},
		read: syscall.Read,
		write: func(_ int, value []byte) (int, error) {
			writes++
			switch writes {
			case 1:
				accepted = append(accepted, value[:2]...)
				return 2, nil
			case 2:
				return 0, syscall.EINTR
			case 3:
				return 0, syscall.EAGAIN
			default:
				accepted = append(accepted, value...)
				return len(value), nil
			}
		},
	}
	kqueueSystem := kqueueSyscalls{
		open: func() (int, error) { return 112, nil },
		kevent: func(_ int, changes, events []syscall.Kevent_t, _ *syscall.Timespec) (int, error) {
			if len(events) == 0 {
				return 0, nil
			}
			waits++
			events[0] = syscall.Kevent_t{Ident: uint64(file.Fd()), Filter: syscall.EVFILT_WRITE}
			return 1, nil
		},
		close: func(int) error { return nil },
	}
	writer, err := newTerminalWriter(file, ioSystem, kqueueSystem)
	if err != nil {
		t.Fatalf("newTerminalWriter() error = %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })

	value := []byte("abcdef")
	if n, err := writer.Write(context.Background(), value); n != len(value) || err != nil {
		t.Fatalf("Write() = (%d, %v), want (%d, nil)", n, err, len(value))
	}
	if string(accepted) != string(value) || waits != 1 {
		t.Fatalf("accepted=%q waits=%d, want %q and 1", accepted, waits, value)
	}
}

func TestTerminalWriterEmptyWriteHonorsCancellationAndClose(t *testing.T) {
	newWriter := func(t *testing.T) *TerminalWriter {
		t.Helper()
		file := newTestFile(t, "terminal")
		writer, err := newTerminalWriter(file, attachIOSyscalls{
			fcntl: func(_ int, command, _ int) (int, error) {
				if command == syscall.F_GETFL {
					return syscall.O_NONBLOCK, nil
				}
				return 0, nil
			},
			read:  syscall.Read,
			write: syscall.Write,
		}, registrationOnlyKqueueSystem(nil))
		if err != nil {
			t.Fatalf("newTerminalWriter() error = %v", err)
		}
		t.Cleanup(func() { _ = writer.Close() })
		return writer
	}

	t.Run("canceled context", func(t *testing.T) {
		writer := newWriter(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if n, err := writer.Write(ctx, nil); n != 0 || !errors.Is(err, context.Canceled) {
			t.Fatalf("Write(nil) = (%d, %v), want (0, context.Canceled)", n, err)
		}
	})

	t.Run("closed writer", func(t *testing.T) {
		writer := newWriter(t)
		if err := writer.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		if n, err := writer.Write(context.Background(), nil); n != 0 || !errors.Is(err, ErrAttachDescriptorUnavailable) {
			t.Fatalf("Write(nil) = (%d, %v), want (0, unavailable descriptor)", n, err)
		}
	})

	t.Run("canceled caller on closed writer", func(t *testing.T) {
		writer := newWriter(t)
		if err := writer.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if n, err := writer.Write(ctx, nil); n != 0 || !errors.Is(err, context.Canceled) {
			t.Fatalf("Write(nil) = (%d, %v), want (0, context.Canceled)", n, err)
		}
	})
}

func TestTerminalWriterReturnsExactPrefixOnCancellationAtPermanentSink(t *testing.T) {
	file := newTestFile(t, "terminal")
	waitStarted := make(chan struct{})
	wake := make(chan struct{})
	var wakeOnce sync.Once
	ioSystem := attachIOSyscalls{
		fcntl: func(_ int, command, _ int) (int, error) {
			if command == syscall.F_GETFL {
				return syscall.O_NONBLOCK, nil
			}
			return 0, nil
		},
		read: syscall.Read,
		write: func(_ int, value []byte) (int, error) {
			if len(value) == 6 {
				return 2, nil
			}
			return 0, syscall.EAGAIN
		},
	}
	kqueueSystem := kqueueSyscalls{
		open: func() (int, error) { return 113, nil },
		kevent: func(_ int, changes, events []syscall.Kevent_t, _ *syscall.Timespec) (int, error) {
			if len(events) == 0 {
				if len(changes) == 1 {
					wakeOnce.Do(func() { close(wake) })
				}
				return 0, nil
			}
			select {
			case <-waitStarted:
			default:
				close(waitStarted)
			}
			<-wake
			events[0] = syscall.Kevent_t{Ident: kqueueCancelIdent, Filter: syscall.EVFILT_USER}
			return 1, nil
		},
		close: func(int) error { return nil },
	}
	writer, err := newTerminalWriter(file, ioSystem, kqueueSystem)
	if err != nil {
		t.Fatalf("newTerminalWriter() error = %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct {
		n   int
		err error
	}, 1)
	go func() {
		n, err := writer.Write(ctx, []byte("abcdef"))
		done <- struct {
			n   int
			err error
		}{n, err}
	}()
	<-waitStarted
	cancel()
	select {
	case result := <-done:
		if result.n != 2 || !errors.Is(result.err, context.Canceled) {
			t.Fatalf("Write() = (%d, %v), want (2, context.Canceled)", result.n, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("permanently blocked sink did not wake for cancellation")
	}
}

func TestTerminalWriterReportsPeerCloseAndRefusesClosedReusedFile(t *testing.T) {
	file := newTestFile(t, "terminal")
	originalFD := file.Fd()
	writes := 0
	ioSystem := attachIOSyscalls{
		fcntl: func(_ int, command, _ int) (int, error) {
			if command == syscall.F_GETFL {
				return syscall.O_NONBLOCK, nil
			}
			return 0, nil
		},
		read: syscall.Read,
		write: func(int, []byte) (int, error) {
			writes++
			return 0, syscall.EPIPE
		},
	}
	writer, err := newTerminalWriter(file, ioSystem, registrationOnlyKqueueSystem(nil))
	if err != nil {
		t.Fatalf("newTerminalWriter() error = %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	if n, err := writer.Write(context.Background(), []byte("x")); n != 0 || !errors.Is(err, syscall.EPIPE) {
		t.Fatalf("peer-close Write() = (%d, %v), want (0, EPIPE)", n, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close source file: %v", err)
	}
	replacement, err := os.CreateTemp(t.TempDir(), "replacement")
	if err != nil {
		t.Fatalf("CreateTemp replacement: %v", err)
	}
	t.Cleanup(func() { _ = replacement.Close() })
	if replacement.Fd() != originalFD {
		t.Fatalf("replacement fd = %d, want reused descriptor %d", replacement.Fd(), originalFD)
	}
	if n, err := writer.Write(context.Background(), []byte("y")); n != 0 || !errors.Is(err, ErrAttachDescriptorUnavailable) {
		t.Fatalf("reused-descriptor Write() = (%d, %v), want unavailable", n, err)
	}
	if writes != 1 {
		t.Fatalf("raw writes = %d, want 1 (no write through reused descriptor)", writes)
	}
}

func TestTerminalWriterCloseDoesNotJoinUnexpectedlyBlockedWrite(t *testing.T) {
	file := newTestFile(t, "terminal")
	started := make(chan struct{})
	release := make(chan struct{})
	ioSystem := attachIOSyscalls{
		fcntl: func(_ int, command, value int) (int, error) {
			if command == syscall.F_GETFL {
				return 0, nil
			}
			return value, nil
		},
		read: syscall.Read,
		write: func(int, []byte) (int, error) {
			close(started)
			<-release
			return 0, syscall.EAGAIN
		},
	}
	writer, err := newTerminalWriter(file, ioSystem, registrationOnlyKqueueSystem(nil))
	if err != nil {
		t.Fatalf("newTerminalWriter() error = %v", err)
	}
	writeDone := make(chan error, 1)
	go func() {
		_, err := writer.Write(context.Background(), []byte("blocked"))
		writeDone <- err
	}()
	<-started
	closeDone := make(chan error, 1)
	go func() { closeDone <- writer.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Close() joined an unexpectedly blocked raw write")
	}
	close(release)
	select {
	case err := <-writeDone:
		if errors.Is(err, errKqueueWaiterClosed) {
			t.Fatalf("Write() leaked internal waiter lifecycle error: %v", err)
		}
		if !errors.Is(err, context.Canceled) && !errors.Is(err, ErrAttachDescriptorUnavailable) {
			t.Fatalf("Write() error = %v, want cancellation or unavailable descriptor", err)
		}
	case <-time.After(time.Second):
		t.Fatal("released write did not return")
	}
}

func TestTerminalWriterPinsBorrowedDescriptorAcrossRawWrite(t *testing.T) {
	file := newTestFile(t, "terminal")
	originalFD := file.Fd()
	started := make(chan struct{})
	release := make(chan struct{})
	ioSystem := attachIOSyscalls{
		fcntl: func(_ int, command, _ int) (int, error) {
			if command == syscall.F_GETFL {
				return syscall.O_NONBLOCK, nil
			}
			return 0, nil
		},
		read: syscall.Read,
		write: func(_ int, value []byte) (int, error) {
			close(started)
			<-release
			return len(value), nil
		},
	}
	writer, err := newTerminalWriter(file, ioSystem, registrationOnlyKqueueSystem(nil))
	if err != nil {
		t.Fatalf("newTerminalWriter() error = %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	writeDone := make(chan error, 1)
	go func() {
		_, err := writer.Write(context.Background(), []byte("pinned"))
		writeDone <- err
	}()
	<-started
	fileCloseDone := make(chan error, 1)
	go func() { fileCloseDone <- file.Close() }()
	select {
	case err := <-fileCloseDone:
		if err != nil {
			t.Fatalf("file.Close() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("file.Close() did not mark the borrowed file closed")
	}
	replacement, err := os.CreateTemp(t.TempDir(), "while-pinned")
	if err != nil {
		t.Fatalf("CreateTemp while pinned: %v", err)
	}
	if replacement.Fd() == originalFD {
		t.Fatalf("active raw write did not pin kernel descriptor %d against reuse", replacement.Fd())
	}
	if err := replacement.Close(); err != nil {
		t.Fatalf("close replacement: %v", err)
	}
	close(release)
	if err := <-writeDone; err != nil {
		t.Fatalf("Write() error = %v", err)
	}
}

func registrationOnlyKqueueSystem(order *[]string) kqueueSyscalls {
	return kqueueSyscalls{
		open: func() (int, error) { return 110, nil },
		kevent: func(_ int, _ []syscall.Kevent_t, _ []syscall.Kevent_t, _ *syscall.Timespec) (int, error) {
			return 0, nil
		},
		close: func(int) error {
			if order != nil {
				*order = append(*order, "close-kqueue")
			}
			return nil
		},
	}
}
