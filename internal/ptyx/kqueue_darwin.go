//go:build darwin

package ptyx

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"syscall"
)

const kqueueCancelIdent = ^uint64(0)

var errKqueueWaiterClosed = errors.New("kqueue waiter is closed")

// kqueueSyscalls is the complete kernel boundary used by the attach I/O
// waiters. Keeping these three calls injectable pins registration, wake, and
// close behavior without replacing the byte-transport code under test.
type kqueueSyscalls struct {
	open   func() (int, error)
	kevent func(int, []syscall.Kevent_t, []syscall.Kevent_t, *syscall.Timespec) (int, error)
	close  func(int) error
}

var realKqueueSyscalls = kqueueSyscalls{
	open:   syscall.Kqueue,
	kevent: syscall.Kevent,
	close:  syscall.Close,
}

// kqueueWaiter owns only its kqueue descriptor. The watched descriptor remains
// owned by the reader or writer that constructed it.
type kqueueWaiter struct {
	fd           int
	filter       int16
	kq           int
	system       kqueueSyscalls
	waitMu       sync.Mutex
	lifeMu       sync.Mutex
	closed       bool
	closeOnce    sync.Once
	fdClose      sync.Once
	closePending bool
	wakeFailed   bool
	closeErr     error
}

func newKqueueWaiter(fd int, filter int16, system kqueueSyscalls) (*kqueueWaiter, error) {
	kq, err := system.open()
	if err != nil {
		return nil, fmt.Errorf("open kqueue: %w", err)
	}
	waiter := &kqueueWaiter{fd: fd, filter: filter, kq: kq, system: system}
	changes := []syscall.Kevent_t{
		{Ident: uint64(fd), Filter: filter, Flags: syscall.EV_ADD | syscall.EV_ENABLE},
		{Ident: kqueueCancelIdent, Filter: syscall.EVFILT_USER, Flags: syscall.EV_ADD | syscall.EV_ENABLE | syscall.EV_CLEAR},
	}
	for {
		_, err := system.kevent(kq, changes, nil, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return nil, errors.Join(fmt.Errorf("register kqueue events: %w", err), system.close(kq))
		}
		break
	}
	return waiter, nil
}

// Wait blocks for descriptor readiness or EOF. Context cancellation explicitly
// triggers EVFILT_USER, the cancellation mechanism established by Darwin probes
// 5 and 10; it does not poll and does not park a goroutine in descriptor I/O.
func (w *kqueueWaiter) Wait(ctx context.Context) (bool, error) {
	w.waitMu.Lock()
	defer func() {
		w.waitMu.Unlock()
		w.finalizePendingClose()
	}()
	if w.isClosed() {
		return false, errKqueueWaiterClosed
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	stopWake := make(chan struct{})
	wakeFinished := make(chan struct{})
	go func() {
		defer close(wakeFinished)
		select {
		case <-ctx.Done():
			w.wakeForCancellation()
		case <-stopWake:
		}
	}()
	defer func() {
		close(stopWake)
		<-wakeFinished
	}()

	events := make([]syscall.Kevent_t, 4)
	for {
		count, err := w.system.kevent(w.kq, nil, events, nil)
		if contextErr := ctx.Err(); contextErr != nil {
			return false, contextErr
		}
		if w.isClosed() {
			return false, errKqueueWaiterClosed
		}
		if errors.Is(err, syscall.EINTR) {
			if contextErr := ctx.Err(); contextErr != nil {
				return false, contextErr
			}
			continue
		}
		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return false, contextErr
			}
			return false, fmt.Errorf("wait for descriptor readiness: %w", err)
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return false, contextErr
		}
		for _, event := range events[:count] {
			if event.Filter == syscall.EVFILT_USER && event.Ident == kqueueCancelIdent {
				continue
			}
			if event.Filter != w.filter || event.Ident != uint64(w.fd) {
				continue
			}
			if event.Flags&syscall.EV_ERROR != 0 && event.Data != 0 {
				return false, fmt.Errorf("descriptor readiness event: %w", syscall.Errno(event.Data))
			}
			return event.Flags&syscall.EV_EOF != 0, nil
		}
	}
}

// wakeForCancellation falls back to closing the waiter-owned kqueue when a
// trigger fails. On Darwin, closing the kqueue releases the active kevent with
// EBADF; Wait checks the canceled context before the closed lifecycle state and
// never re-enters kevent, so descriptor reuse cannot redirect another wait.
func (w *kqueueWaiter) wakeForCancellation() {
	w.lifeMu.Lock()
	if w.closed {
		fallbackClose := w.closePending && w.wakeFailed
		w.lifeMu.Unlock()
		if fallbackClose {
			w.finalizeClose()
		}
		return
	}
	wakeErr := w.wakeLocked()
	if wakeErr == nil {
		w.lifeMu.Unlock()
		return
	}
	w.closed = true
	w.wakeFailed = true
	w.closeErr = errors.Join(w.closeErr, wakeErr)
	w.lifeMu.Unlock()
	w.finalizeClose()
}

func (w *kqueueWaiter) isClosed() bool {
	w.lifeMu.Lock()
	defer w.lifeMu.Unlock()
	return w.closed
}

// Wake explicitly triggers the user event registered for cancellation.
func (w *kqueueWaiter) Wake() error {
	w.lifeMu.Lock()
	defer w.lifeMu.Unlock()
	if w.closed {
		return errKqueueWaiterClosed
	}
	return w.wakeLocked()
}

func (w *kqueueWaiter) wakeLocked() error {
	trigger := []syscall.Kevent_t{{Ident: kqueueCancelIdent, Filter: syscall.EVFILT_USER, Fflags: syscall.NOTE_TRIGGER}}
	for {
		_, err := w.system.kevent(w.kq, trigger, nil, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("trigger kqueue wake: %w", err)
		}
		return nil
	}
}

// Close wakes and joins an active kevent wait before releasing the waiter-owned
// kqueue. It never joins descriptor read or write syscalls.
func (w *kqueueWaiter) Close() error {
	w.closeOnce.Do(func() {
		w.lifeMu.Lock()
		if w.closed {
			w.lifeMu.Unlock()
			w.finalizeClose()
			return
		}
		w.closed = true
		wakeErr := w.wakeLocked()
		w.closePending = true
		w.wakeFailed = wakeErr != nil
		w.closeErr = errors.Join(w.closeErr, wakeErr)
		w.lifeMu.Unlock()

		if wakeErr != nil {
			// A failed trigger cannot prove an active kevent returned. Close only
			// when no wait owns the descriptor; otherwise retain the kqueue and
			// report the wake defect rather than risking descriptor reuse.
			if w.waitMu.TryLock() {
				w.finalizeClose()
				w.waitMu.Unlock()
			}
			return
		}

		w.waitMu.Lock()
		w.finalizeClose()
		w.waitMu.Unlock()
	})
	w.lifeMu.Lock()
	defer w.lifeMu.Unlock()
	return w.closeErr
}

func (w *kqueueWaiter) finalizePendingClose() {
	w.lifeMu.Lock()
	pending := w.closePending
	w.lifeMu.Unlock()
	if !pending {
		return
	}
	w.waitMu.Lock()
	w.finalizeClose()
	w.waitMu.Unlock()
}

func (w *kqueueWaiter) finalizeClose() {
	w.fdClose.Do(func() {
		var closeErr error
		if err := w.system.close(w.kq); err != nil {
			closeErr = fmt.Errorf("close kqueue: %w", err)
		}
		w.lifeMu.Lock()
		w.closePending = false
		w.closeErr = errors.Join(w.closeErr, closeErr)
		w.lifeMu.Unlock()
	})
}
