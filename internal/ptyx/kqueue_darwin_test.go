//go:build darwin

package ptyx

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestKqueueWaiterRegistersDescriptorAndCancellationExactly(t *testing.T) {
	var changes []syscall.Kevent_t
	system := kqueueSyscalls{
		open: func() (int, error) { return 91, nil },
		kevent: func(_ int, incoming, _ []syscall.Kevent_t, _ *syscall.Timespec) (int, error) {
			changes = append(changes, incoming...)
			return 0, nil
		},
		close: func(int) error { return nil },
	}

	waiter, err := newKqueueWaiter(17, syscall.EVFILT_READ, system)
	if err != nil {
		t.Fatalf("newKqueueWaiter() error = %v", err)
	}
	t.Cleanup(func() { _ = waiter.Close() })

	want := []syscall.Kevent_t{
		{Ident: 17, Filter: syscall.EVFILT_READ, Flags: syscall.EV_ADD | syscall.EV_ENABLE},
		{Ident: kqueueCancelIdent, Filter: syscall.EVFILT_USER, Flags: syscall.EV_ADD | syscall.EV_ENABLE | syscall.EV_CLEAR},
	}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("registration = %#v, want %#v", changes, want)
	}
}

func TestKqueueWaiterRetriesEINTRWhileRegisteringEvents(t *testing.T) {
	registrations := 0
	system := kqueueSyscalls{
		open: func() (int, error) { return 98, nil },
		kevent: func(_ int, changes, events []syscall.Kevent_t, _ *syscall.Timespec) (int, error) {
			if len(events) == 0 && len(changes) == 1 {
				return 0, nil
			}
			if len(events) != 0 || len(changes) != 2 {
				t.Fatalf("registration kevent changes=%#v events=%#v", changes, events)
			}
			registrations++
			if registrations == 1 {
				return 0, syscall.EINTR
			}
			return 0, nil
		},
		close: func(int) error { return nil },
	}

	waiter, err := newKqueueWaiter(24, syscall.EVFILT_READ, system)
	if err != nil {
		t.Fatalf("newKqueueWaiter() error = %v", err)
	}
	t.Cleanup(func() { _ = waiter.Close() })
	if registrations != 2 {
		t.Fatalf("registration calls = %d, want 2", registrations)
	}
}

func TestKqueueWaiterWakeRetriesEINTR(t *testing.T) {
	triggers := 0
	system := kqueueSyscalls{
		open: func() (int, error) { return 99, nil },
		kevent: func(_ int, changes, events []syscall.Kevent_t, _ *syscall.Timespec) (int, error) {
			if len(events) == 0 && len(changes) == 1 {
				triggers++
				if triggers == 1 {
					return 0, syscall.EINTR
				}
			}
			return 0, nil
		},
		close: func(int) error { return nil },
	}

	waiter, err := newKqueueWaiter(25, syscall.EVFILT_WRITE, system)
	if err != nil {
		t.Fatalf("newKqueueWaiter() error = %v", err)
	}
	t.Cleanup(func() { _ = waiter.Close() })
	if err := waiter.Wake(); err != nil {
		t.Fatalf("Wake() error = %v", err)
	}
	if triggers != 2 {
		t.Fatalf("wake trigger calls = %d, want 2", triggers)
	}
}

func TestKqueueWaiterWakesImmediatelyForCancellationAndRetriesEINTR(t *testing.T) {
	registered := false
	waitStarted := make(chan struct{})
	wake := make(chan struct{})
	var once sync.Once
	var wakeOnce sync.Once
	waits := 0
	triggers := 0
	system := kqueueSyscalls{
		open: func() (int, error) { return 92, nil },
		kevent: func(_ int, changes, events []syscall.Kevent_t, _ *syscall.Timespec) (int, error) {
			if len(events) == 0 {
				if len(changes) == 2 {
					registered = true
					return 0, nil
				}
				triggers++
				if len(changes) != 1 || changes[0].Ident != kqueueCancelIdent || changes[0].Filter != syscall.EVFILT_USER || changes[0].Fflags != syscall.NOTE_TRIGGER {
					t.Fatalf("cancel trigger = %#v", changes)
				}
				wakeOnce.Do(func() { close(wake) })
				return 0, nil
			}
			waits++
			if waits == 1 {
				return 0, syscall.EINTR
			}
			once.Do(func() { close(waitStarted) })
			<-wake
			events[0] = syscall.Kevent_t{Ident: kqueueCancelIdent, Filter: syscall.EVFILT_USER}
			return 1, nil
		},
		close: func(int) error { return nil },
	}
	waiter, err := newKqueueWaiter(18, syscall.EVFILT_READ, system)
	if err != nil {
		t.Fatalf("newKqueueWaiter() error = %v", err)
	}
	t.Cleanup(func() { _ = waiter.Close() })
	if !registered {
		t.Fatal("descriptor events were not registered")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := waiter.Wait(ctx)
		done <- err
	}()
	<-waitStarted
	started := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Wait() error = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
			t.Fatalf("cancellation took %v, want immediate wake", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait() did not wake for cancellation")
	}
	if waits != 2 || triggers != 1 {
		t.Fatalf("waits=%d triggers=%d, want 2 and 1", waits, triggers)
	}
}

func TestKqueueWaiterCancellationClosesKqueueWhenWakeFails(t *testing.T) {
	wantWakeErr := errors.New("wake failed")
	waitStarted := make(chan struct{})
	kqueueClosed := make(chan struct{})
	var closeOnce sync.Once
	system := kqueueSyscalls{
		open: func() (int, error) { return 100, nil },
		kevent: func(_ int, changes, events []syscall.Kevent_t, _ *syscall.Timespec) (int, error) {
			if len(events) == 0 {
				if len(changes) == 1 {
					return 0, wantWakeErr
				}
				return 0, nil
			}
			close(waitStarted)
			<-kqueueClosed
			return 0, syscall.EBADF
		},
		close: func(int) error {
			closeOnce.Do(func() { close(kqueueClosed) })
			return nil
		},
	}
	waiter, err := newKqueueWaiter(26, syscall.EVFILT_READ, system)
	if err != nil {
		t.Fatalf("newKqueueWaiter() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := waiter.Wait(ctx)
		done <- err
	}()
	<-waitStarted
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Wait() error = %v, want context.Canceled", err)
		}
	case <-time.After(250 * time.Millisecond):
		closeOnce.Do(func() { close(kqueueClosed) })
		<-done
		t.Fatal("Wait() remained blocked after cancellation trigger failed")
	}
}

func TestKqueueWaiterCancellationClosesAfterCloseWakeFails(t *testing.T) {
	wantWakeErr := errors.New("wake failed")
	waitStarted := make(chan struct{})
	kqueueClosed := make(chan struct{})
	var closeOnce sync.Once
	system := kqueueSyscalls{
		open: func() (int, error) { return 101, nil },
		kevent: func(_ int, changes, events []syscall.Kevent_t, _ *syscall.Timespec) (int, error) {
			if len(events) == 0 {
				if len(changes) == 1 {
					return 0, wantWakeErr
				}
				return 0, nil
			}
			close(waitStarted)
			<-kqueueClosed
			return 0, syscall.EBADF
		},
		close: func(int) error {
			closeOnce.Do(func() { close(kqueueClosed) })
			return nil
		},
	}
	waiter, err := newKqueueWaiter(27, syscall.EVFILT_READ, system)
	if err != nil {
		t.Fatalf("newKqueueWaiter() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := waiter.Wait(ctx)
		done <- err
	}()
	<-waitStarted
	if err := waiter.Close(); !errors.Is(err, wantWakeErr) {
		t.Fatalf("Close() error = %v, want wake failure", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Wait() error = %v, want context.Canceled", err)
		}
	case <-time.After(250 * time.Millisecond):
		closeOnce.Do(func() { close(kqueueClosed) })
		<-done
		t.Fatal("Wait() remained blocked when cancellation followed a failed Close wake")
	}
}

func TestKqueueWaiterCancellationFallbackReleasesRealDarwinKevent(t *testing.T) {
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	t.Cleanup(func() {
		_ = readEnd.Close()
		_ = writeEnd.Close()
	})

	wantWakeErr := errors.New("injected wake failure")
	waitStarted := make(chan struct{})
	var waitStartedOnce sync.Once
	waitCalls := 0
	wantTrigger := []syscall.Kevent_t{{Ident: kqueueCancelIdent, Filter: syscall.EVFILT_USER, Fflags: syscall.NOTE_TRIGGER}}
	system := realKqueueSyscalls
	system.kevent = func(kq int, changes, events []syscall.Kevent_t, timeout *syscall.Timespec) (int, error) {
		if len(events) == 0 && reflect.DeepEqual(changes, wantTrigger) {
			return 0, wantWakeErr
		}
		if len(events) > 0 {
			waitCalls++
			waitStartedOnce.Do(func() { close(waitStarted) })
		}
		return syscall.Kevent(kq, changes, events, timeout)
	}
	waiter, err := newKqueueWaiter(int(readEnd.Fd()), syscall.EVFILT_READ, system)
	if err != nil {
		t.Fatalf("newKqueueWaiter() error = %v", err)
	}
	t.Cleanup(func() { _ = waiter.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := waiter.Wait(ctx)
		done <- err
	}()
	<-waitStarted
	select {
	case err := <-done:
		t.Fatalf("real kevent returned before cancellation: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Wait() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		waiter.finalizeClose()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Wait() remained blocked after guarded kqueue cleanup")
		}
		t.Fatal("closing the real Darwin kqueue did not release the active kevent")
	}
	if waitCalls != 1 {
		t.Fatalf("real blocking kevent calls = %d, want 1 (no post-close wait)", waitCalls)
	}
}

func TestKqueueWaiterClassifiesReadEOFAndIgnoresStaleDescriptors(t *testing.T) {
	waits := 0
	system := kqueueSyscalls{
		open: func() (int, error) { return 93, nil },
		kevent: func(_ int, changes, events []syscall.Kevent_t, _ *syscall.Timespec) (int, error) {
			if len(events) == 0 {
				return 0, nil
			}
			waits++
			if waits == 1 {
				events[0] = syscall.Kevent_t{Ident: 999, Filter: syscall.EVFILT_READ, Flags: syscall.EV_EOF}
				return 1, nil
			}
			events[0] = syscall.Kevent_t{Ident: 19, Filter: syscall.EVFILT_READ, Flags: syscall.EV_EOF}
			return 1, nil
		},
		close: func(int) error { return nil },
	}
	waiter, err := newKqueueWaiter(19, syscall.EVFILT_READ, system)
	if err != nil {
		t.Fatalf("newKqueueWaiter() error = %v", err)
	}
	t.Cleanup(func() { _ = waiter.Close() })

	eof, err := waiter.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if !eof || waits != 2 {
		t.Fatalf("Wait() = eof %t after %d waits, want true after 2", eof, waits)
	}
}

func TestKqueueWaiterClosesSetupFailuresAndCloseIsIdempotent(t *testing.T) {
	wantErr := errors.New("register failed")
	var closes []int
	system := kqueueSyscalls{
		open: func() (int, error) { return 94, nil },
		kevent: func(int, []syscall.Kevent_t, []syscall.Kevent_t, *syscall.Timespec) (int, error) {
			return 0, wantErr
		},
		close: func(fd int) error {
			closes = append(closes, fd)
			return nil
		},
	}
	if _, err := newKqueueWaiter(20, syscall.EVFILT_WRITE, system); !errors.Is(err, wantErr) {
		t.Fatalf("newKqueueWaiter() error = %v, want registration failure", err)
	}
	if !reflect.DeepEqual(closes, []int{94}) {
		t.Fatalf("setup closes = %v, want [94]", closes)
	}

	closes = nil
	system.kevent = func(int, []syscall.Kevent_t, []syscall.Kevent_t, *syscall.Timespec) (int, error) { return 0, nil }
	waiter, err := newKqueueWaiter(20, syscall.EVFILT_WRITE, system)
	if err != nil {
		t.Fatalf("newKqueueWaiter() error = %v", err)
	}
	if err := waiter.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := waiter.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if !reflect.DeepEqual(closes, []int{94}) {
		t.Fatalf("Close() calls = %v, want [94]", closes)
	}
}

func TestKqueueWaiterCloseWakesBeforeCloseAndFutureWakeCannotReachReusedFD(t *testing.T) {
	var calls []string
	system := kqueueSyscalls{
		open: func() (int, error) { return 95, nil },
		kevent: func(_ int, changes, _ []syscall.Kevent_t, _ *syscall.Timespec) (int, error) {
			if len(changes) == 1 {
				calls = append(calls, "wake")
			}
			return 0, nil
		},
		close: func(int) error {
			calls = append(calls, "close")
			return nil
		},
	}
	waiter, err := newKqueueWaiter(21, syscall.EVFILT_READ, system)
	if err != nil {
		t.Fatalf("newKqueueWaiter() error = %v", err)
	}
	if err := waiter.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !reflect.DeepEqual(calls, []string{"wake", "close"}) {
		t.Fatalf("Close() order = %v, want [wake close]", calls)
	}
	if err := waiter.Wake(); !errors.Is(err, errKqueueWaiterClosed) {
		t.Fatalf("Wake() after Close error = %v, want errKqueueWaiterClosed", err)
	}
	if !reflect.DeepEqual(calls, []string{"wake", "close"}) {
		t.Fatalf("post-close Wake reached reused kqueue fd: calls = %v", calls)
	}
}

func TestKqueueWaiterCloseJoinsOnlyTheWokenWaitBeforeClosingFD(t *testing.T) {
	waitStarted := make(chan struct{})
	wake := make(chan struct{})
	closed := false
	postCloseWaits := 0
	system := kqueueSyscalls{
		open: func() (int, error) { return 96, nil },
		kevent: func(_ int, changes, events []syscall.Kevent_t, _ *syscall.Timespec) (int, error) {
			if len(events) == 0 {
				if len(changes) == 1 {
					select {
					case <-wake:
					default:
						close(wake)
					}
				}
				return 0, nil
			}
			if closed {
				postCloseWaits++
				return 0, syscall.EBADF
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
		close: func(int) error {
			closed = true
			return nil
		},
	}
	waiter, err := newKqueueWaiter(22, syscall.EVFILT_READ, system)
	if err != nil {
		t.Fatalf("newKqueueWaiter() error = %v", err)
	}
	waitDone := make(chan error, 1)
	go func() {
		_, err := waiter.Wait(context.Background())
		waitDone <- err
	}()
	<-waitStarted
	if err := waiter.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	select {
	case err := <-waitDone:
		if !errors.Is(err, errKqueueWaiterClosed) {
			t.Fatalf("active Wait() error = %v, want errKqueueWaiterClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("active Wait did not return after Close wake")
	}
	if postCloseWaits != 0 {
		t.Fatalf("Wait issued %d kevent calls after kqueue close/reuse", postCloseWaits)
	}
}

func TestKqueueWaiterCloseDefersCleanupAfterFailedActiveWake(t *testing.T) {
	wantErr := errors.New("wake failed")
	waitStarted := make(chan struct{})
	releaseWait := make(chan struct{})
	closed := false
	system := kqueueSyscalls{
		open: func() (int, error) { return 97, nil },
		kevent: func(_ int, changes, events []syscall.Kevent_t, _ *syscall.Timespec) (int, error) {
			if len(events) == 0 {
				if len(changes) == 1 {
					return 0, wantErr
				}
				return 0, nil
			}
			close(waitStarted)
			<-releaseWait
			events[0] = syscall.Kevent_t{Ident: 23, Filter: syscall.EVFILT_READ}
			return 1, nil
		},
		close: func(int) error {
			closed = true
			return nil
		},
	}
	waiter, err := newKqueueWaiter(23, syscall.EVFILT_READ, system)
	if err != nil {
		t.Fatalf("newKqueueWaiter() error = %v", err)
	}
	waitDone := make(chan error, 1)
	go func() {
		_, err := waiter.Wait(context.Background())
		waitDone <- err
	}()
	<-waitStarted
	closeDone := make(chan error, 1)
	go func() { closeDone <- waiter.Close() }()
	select {
	case err := <-closeDone:
		if !errors.Is(err, wantErr) {
			t.Fatalf("Close() error = %v, want wake failure", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Close() blocked forever after cancellation trigger failed")
	}
	if closed {
		t.Fatal("Close() reused/closed a kqueue with an active wait after wake failure")
	}
	close(releaseWait)
	select {
	case err := <-waitDone:
		if !errors.Is(err, errKqueueWaiterClosed) {
			t.Fatalf("released Wait() error = %v, want errKqueueWaiterClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("released Wait did not return")
	}
	if !closed {
		t.Fatal("kqueue was not eventually closed after the active wait returned")
	}
}
