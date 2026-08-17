# Darwin I/O probes behind §15.11's release and flush design

Non-normative. Records what was executed, on this machine, to establish the
kernel and runtime facts §15.11 depends on. The spec states the conclusions; this
file is why they are not guesses. Probes ran in a scratch directory against
throwaway PTYs and unix sockets — never against the running fleet.

Environment: Darwin 25.6.0, arm64, **go1.26.6 darwin/arm64** (`go version`).

Reproducing. The probe source is a single `main.go` with one `case` per probe,
selected by `os.Args[1]`, plus `golang.org/x/sys/unix`; it is reproduced in full
at the end of this file. Each probe installs a watchdog goroutine that prints and
`os.Exit(9)`s, so a call that never returns is reported as such instead of
hanging the run:

    mkdir dlprobe && cd dlprobe
    # write main.go from the appendix below
    go mod init dlprobe && go mod tidy
    for p in pipe-control ptmx-deadline ptmx-close sock-write grandchild-eof \
             nonblock-kqueue kqueue-eof fionread starve tty-write \\
             tty-nonblock flag-restore ofd sig-write; do
        # and: probe broken-pipe fd2 ; probe broken-pipe dup ; probe broken-pipe dup-fd1-closed
        # and: ( trap '' INT; probe ignore reset ) ; probe ignore reset
        echo "== $p =="; timeout 25 go run . "$p"
    done

Every PTY is created in-process from `/dev/ptmx` and every socket in a fresh
`os.MkdirTemp`. Nothing touches the running fleet, the user's tmux server, or
any real agent.

## Control: the harness itself is valid

`os.Pipe()` + `SetReadDeadline(now+300ms)`, then a blocked `Read`:

    pipe SetReadDeadline err=<nil>
    pipe read returned n=0 after 300ms err=read |0: i/o timeout istimeout=true

Deadlines do interrupt a blocked read on a pollable file, so a negative result
below is a property of the file type rather than of the probe.

The conclusions below are scoped to the architecture each probe actually used.
Probes 1–3 test a **blocking** master read; probes 5–6 test a **non-blocking**
master driven by `kqueue`, and reach the opposite conclusion. Saying "Darwin
offers no cancellation path" would have been false — the normative claim in
§15.11.4 is the narrow one, that the blocking-read architecture has none, which
is why the spec requires the other.

## 1. A blocking PTY master read cannot be bounded by a deadline

Master from `/dev/ptmx` (`TIOCPTYGRANT`/`TIOCPTYUNLK`/`TIOCPTYGNAME`), slave held
open, no data available:

    ptmx SetReadDeadline err=<nil>
    ptmx-deadline: WATCHDOG FIRED — call never returned

`SetReadDeadline` **returns nil and then does nothing**. This is the dangerous
shape: the API appears to accept the bound, so a reviewer reading the code sees
a deadline that does not exist. An implementation that trusts it hangs.

## 2. Closing the master does not unblock a blocking read either

Same setup, `m.Close()` from a second goroutine 300 ms into the blocked read:

    closing master...
    ptmx-close: WATCHDOG FIRED — call never returned

So a *blocking* PTY-master read has no cancellation path: not a deadline, not a
close. That is a statement about this architecture only — see probe 5.

## 3. A surviving grandchild means EOF never arrives

`/bin/sh -c 'printf tailbytes; (sleep 30 &)'` on the slave; wait for the child;
close the shim's own slave copy; read the master twice:

    read1 n=9 data="tailbytes" after 0s err=<nil>
    second read ... watchdog fired

The tail written before exit is readable, and then the master stays open
indefinitely because the backgrounded grandchild still holds a slave descriptor.
Observed child exit therefore does not imply EOF, and no outcome may claim the
operator saw everything the harness ever wrote. §15.11.4 restricts its claims to
bytes the shim actually read, which it counts exactly.

## 4. A past write deadline does unblock a socket write

Unix socket, peer accepts and never reads, 8 MiB `Write` blocked on a full
buffer, then `SetWriteDeadline(now-1s)`:

    write is blocked; setting a deadline in the past
    past deadline UNBLOCKED the write: n=8192 after 400ms err=... i/o timeout istimeout=true

Two facts the spec uses. The past deadline is a real interrupt, so §15.11.3
step 4 can join the output writer without stranding the coordinator on a viewer
that stopped reading. And the interrupted write reports its **partial** count
(8192 of 8 MiB), so those bytes were written to the data stream and are counted
in `BYTES`. A returned `n` proves only that the local transport accepted the
bytes; whether the client read them, and whether they reached a terminal, are
the client's own `RAW` and `WRITTEN` observations.

## 5. A non-blocking master driven by kqueue IS cancellable

Master opened with `O_NONBLOCK`, registered on a kqueue for `EVFILT_READ`
alongside an `EVFILT_USER` event used as a cancel channel; a second goroutine
triggers that event 300 ms into an indefinite `kevent` wait:

    A1 nonblocking read with no data: n=-1 err=resource temporarily unavailable
    A2 registered master for EVFILT_READ and a EVFILT_USER cancel channel
    A3 cancel triggered from another goroutine (err=<nil>)
    A4 kevent returned after 300ms: CANCEL (ident=42)

The read never blocks — it returns `EAGAIN` — and the wait *is* interruptible
from another goroutine. This is the architecture §15.11.4 requires.

It does **not** establish that a read-until-`EAGAIN` *loop* terminates: a
continuously-writing grandchild can keep supplying bytes so that `EAGAIN` is
never observed. An earlier draft drew that conclusion from this probe, and it
did not follow. §15.11.4 bounds the pass with a deadline checked between reads
instead.

## 6. EV_EOF on the master is observable, but only when every slave holder closes

Same setup, `tail` written to the slave, slave closed 200 ms later:

    B0 after 0s: EV_EOF=false data=4 bytes="tail" readerr=<nil>
    B1 after 200ms: EV_EOF=true data=0 bytes="" readerr=<nil>

EOF is therefore detectable in this architecture — but probe 3 shows a surviving
grandchild keeps `EV_EOF` unset indefinitely, so no outcome may be conditioned on
it. The spec observes it and never waits for it.

## 7. FIONREAD is not a readable-byte snapshot on a PTY master

Non-blocking master, slave in raw mode, 1024 bytes written to the slave from a
goroutine, then `ioctl(FIONREAD)` on the master:

    C1 FIONREAD on empty master: n=0 err=<nil>
    C2 slave writes completed: 1024 bytes
    C3 FIONREAD with bytes definitely pending: n=0 err=<nil>
    C4 one read returned r=1024 err=<nil>
    C5 FIONREAD after that read: n=0 err=<nil>

C3 reports zero while C4 proves 1024 bytes were queued. `FIONREAD` cannot bound
the barrier pass. §15.11.4 bounds it by the flush deadline, checked between
reads, rather than by any byte count — a bound that needs no kernel counter and
no assumption about how much a viewer can take.

## 8. Starvation of a read-until-EAGAIN loop — INCONCLUSIVE, and not relied on

The `starve` case attempts to answer whether a continuous producer can prevent a
non-blocking read loop from ever observing `EAGAIN`. It did not produce a
trustworthy answer:

    D1 producer has written 0 bytes and is still running
    D2 read-until-EAGAIN TERMINATED after 1024 bytes (producer wrote 0 total)

This is an instrumentation race rather than contradictory kernel observations:
the producer's counter advances only after `Write` *returns*, while a `Write`
still blocked can already have transferred bytes the reader then consumed. The
numbers are consistent with the kernel behaving correctly; they simply measure
two different moments, so the run cannot support a conclusion in either
direction. It is recorded here because it was run, and because a probe
that is quietly dropped is indistinguishable from one that was never attempted.

The spec does not depend on the answer. §15.11.4 bounds the barrier pass with a
deadline checked between reads, which is correct whether or not starvation is
reachable — that is the point of choosing a bound that does not rest on an
unproven termination property.

## 9. A BLOCKING TTY write cannot be interrupted

The attach client's terminal writer writes to a TTY. Using a PTY slave in raw
mode as a stand-in for the operator's terminal, with nobody ever reading the
master, so the write parks:

    E0 slave is a terminal: true
    E1 SetWriteDeadline on a TTY: err=file type does not support deadline
    E2 write is PARKED after 2s despite the 300ms deadline
    E3 closing the tty file...
    E4 write STILL PARKED after close

Three facts. A TTY **honestly refuses** the deadline — contrast probe 1, where
the PTY master accepted one and then ignored it; the refusal is the better
behavior because it cannot be mistaken for a working bound. The write parks
indefinitely. And `Close` does not unblock it, so there is no cancellation path
here any more than there was for a blocking PTY read.

This scopes to the **blocking** architecture only — the probe opens a blocking
slave. It does not show that no cancellable terminal writer exists, and an
earlier draft drew that broader conclusion and designed an orphaned-goroutine
around it. Probe 10 tests the architecture this one did not.

## 10. A non-blocking, event-driven TTY writer IS cancellable

Same stand-in terminal, but `O_NONBLOCK` set before writing, bounded chunks, and
a `kevent` wait on `EVFILT_WRITE` beside an `EVFILT_USER` cancel:

    F0 original descriptor flags: 0x2 (O_NONBLOCK set: false)
    F1 O_NONBLOCK set on the TTY
    F2 wrote 1024 bytes in bounded chunks, then err=resource temporarily unavailable
    F3 registered the TTY for EVFILT_WRITE beside an EVFILT_USER cancel
    F4 cancel triggered from another goroutine (err=<nil>)
    F5 kevent returned after 300ms: CANCEL

The write returns `EAGAIN` instead of parking, and the wait is interrupted on
demand. §15.11.7 therefore requires this architecture and does **not** orphan a
writer. This is the exact writer-side counterpart of probes 1 and 5, and the same
lesson: a blocking call that cannot be interrupted says nothing about whether an
event-driven equivalent can be.

## 11. O_NONBLOCK restores; the whole flag word is not a safe thing to compare

Probe 10 ended by writing the saved flag word back, and `F_GETFL` afterwards
reported `0x10002` against an original `0x2` — `O_NONBLOCK` correctly cleared,
one additional bit present. A focused follow-up could not reproduce that:

    G0 original F_GETFL = 0x2   O_NONBLOCK(0x4) set: false
    G1 after setting O_NONBLOCK: 0x6   O_NONBLOCK set: true
    G2 naive F_SETFL(orig): 0x2  equals orig: true  extra bits: 0x0
    G3 targeted clear of O_NONBLOCK: 0x2  O_NONBLOCK cleared: true
    G5 kqueue EVFILT_WRITE register: F_GETFL 0x2 -> 0x2  delta 0x0

In isolation the word round-trips exactly, and kqueue registration does not alter
what `F_GETFL` reports, so the cause of the extra bit is **unidentified**. The
conservative rule follows from that rather than from a theory: §15.11.7 restores
by clearing `O_NONBLOCK` specifically and judges success on that bit, never on
whole-word equality. An implementation that asserted the word would have a test
that fails for a reason nobody has explained.

## 12. dup leaks O_NONBLOCK to the parent; a fresh open does not

`F_SETFL` acts on the open-file description, which an inherited `stdout` shares
with the parent shell. Setting the flag through a `dup` of that descriptor, then
through a fresh open of the same terminal, and reading the *inherited*
descriptor's flags after each:

    H1 inherited flags before: 0x2 (O_NONBLOCK set: false)
    H2 set O_NONBLOCK on the dup: inherited now 0x6  LEAKED: true
    H3 set O_NONBLOCK on a FRESH open: fresh=0x6  inherited=0x2  LEAKED: false

`dup` is not isolation — it shares the description. A fresh open is. This is why
§15.11.7 never mutates the inherited descriptor: the leak is a peer-state
mutation the parent shell can observe while attach runs, after `SIGSTOP` hands
control back with nothing restored, and permanently after an uncatchable death.

## 13. Terminal identity comes from fstat, not from a path

    H0 stand-in inherited terminal: /dev/ttys009 (buffer PathMax=1024)
    H4 identity: inherited rdev=268435465 ino=15155 | fresh rdev=268435465 ino=15155 | same device: true
    H5 a different terminal: rdev=268435470  compares equal to inherited: false

The `H0` line records a correction rather than a result. The first version of
this probe passed a 128-byte buffer to `F_GETPATH`, which `fcntl(2)` documents as
requiring `MAXPATHLEN` or greater — the short paths it returned made an invalid
call look correct. It now uses a `PathMax` buffer and requires a NUL terminator
within it, and §15.11.7 pins both.

Two descriptors on one terminal agree, and a different terminal compares unequal.
The tuple also survives differing access modes, which matters because production
opens the private handle `O_WRONLY` while the first version of this probe used
`O_RDWR`:

    H6 O_WRONLY handle:        dev=-659841865 rdev=268435465 ino=15151
    H6 vs O_RDWR inherited:    dev=-659841865 rdev=268435465 ino=15151  all three equal: true

Note that the earlier run of this probe compared `st_rdev` alone in code while
its prose said `rdev` and `ino`. That gap is why §15.11.7 pins the tuple as
`(st_dev, st_rdev, st_ino)` in normative text rather than pointing at a probe:
two devices could share an `rdev`, and a probe is evidence for a claim, not a
substitute for stating it. §15.11.7 uses this both to refuse a `stdin`/`stdout` pair that
names two different terminals and to confirm that the fresh output description
it opened is the terminal it already identified. A path is a name, not an
identity, and was never sufficient for either check.

## 14. A blocked fd write is NOT returned by a caught signal

The fd-2 diagnostic path writes through `os.File.Write`. Probing that exact call
path — a blocking pipe whose reader never reads, a 4 MiB write, `SIGINT`
delivered to self 300 ms in:

    I1 delivering SIGINT to self while the write is blocked
    I2 handler OBSERVED the signal: interrupt (the owner goroutine can act)
    I3 the blocked write did NOT return after the signal

The handler sees the signal promptly; the write does not return. The runtime
source says why, and both parts were checked rather than assumed: `setsig` in
`runtime/os_darwin.go` sets `_SA_RESTART`, and `internal/poll.FD.Write` retries
every `EINTR` through `ignoringEINTRIO`. That same function accumulates
successful writes in a private `nn` and returns to the caller only when the whole
slice completes or an error escapes — so a prefix can already be in the pipe
while the caller has observed no return at all.

Two consequences the spec takes: a writing goroutine cannot restore-and-re-raise
itself, so §15.11.7 puts the fd-2 write on a worker the owner never joins; and
the `n > 0` commit boundary is a property of the private non-blocking path only,
since on fd 2 the accepted count is unobservable.

**A first run of this probe reported the opposite** — `Write RETURNED n=65536 ...
err=broken pipe` — and it was a probe artifact, not a result. The read end was
held in an unused variable, so the runtime finalized and closed it, and the write
returned on `EPIPE` rather than on the signal. Keeping the reader alive with
`runtime.KeepAlive` reversed the finding. A returned-write result is exactly what
this probe was hoping to see, which is why it needed checking hardest.

## 15. A diagnostic written to fd 2 on a broken pipe kills the process

Go raises `SIGPIPE` for `EPIPE` on file descriptors 1 and 2 specifically
(`os/file_unix.go`'s `epipecheck`, and `os/signal/doc.go`), and an unregistered
`SIGPIPE` terminates the program. Writing one selected row to a broken pipe
installed as fd 2, versus the same bytes through a `dup` of it:

    ===== fd2 =====
    exit: killed by signal 13 (PIPE)
    ===== dup =====
    RESULT dup write returned n=0 err=write dup-of-stderr: broken pipe isEPIPE=true
    exit: status 6

On fd 2 the process died and the exit code the attachment had selected never
happened — the outcome was not truncated but erased. Through a `dup` the same
condition arrives as an ordinary error and the selected code survives. §15.11.7
therefore writes the row through a `dup` of fd 2 and never fd 2 itself.

**A plain `dup` is not sufficient, and the first version of this probe did not
prove otherwise.** It merely commented that its duplicate was not fd 1 or 2,
while its fixture left those descriptors occupied — the invariant was assumed,
not tested. With fd 1 closed:

    NOTE plain dup(2) returned fd 1 (<=2)
    exit: killed by signal 13

    NOTE F_DUPFD_CLOEXEC(3) returned fd 3
    RESULT dupfd3 n=0 err=write dup-of-stderr: broken pipe
    exit: status 6

`dup` returns the lowest free descriptor, so it hands back 1 and Go's
`stdoutOrErr` rule reintroduces exactly the bug the duplicate was added to avoid.
§15.11.7 therefore requires `F_DUPFD_CLOEXEC` with a minimum of 3 — the floor
atomic with the duplication rather than checked afterwards.

Note this does not contradict probe 12. That probe rejected a `dup` for the
*private handle* because `F_SETFL` through it mutates the shared open-file
description; here nothing is mutated and the bytes written are byte-for-byte what
fd 2 would have received.

## 16. An inherited-ignored signal cannot be re-raised into a wait status

`os/signal/doc.go` states that `Notify` on a `SIGHUP` or `SIGINT` ignored at
program start installs a handler anyway, and that `Reset` or `Stop` restores the
ignored state. The consequence for a design that re-raises:

    ( trap '' INT; ./probe ignore reset )
    P0 inherited SIGINT handler=0x1 ignored=true
    P1 caught interrupt via Notify despite inherited ignore
    P2 after Reset, SIGINT handler=0x1 (0x1 == SIG_IGN, 0 == SIG_DFL)
    P4 STILL ALIVE after re-raise -> no signal wait status is produced
    exit: 6

    ./probe ignore reset            # control: SIGINT not ignored
    exit: 130

With the signal inherited as ignored the re-raise does nothing and the process
exits with the selected code; with it not ignored the re-raise kills as expected.
§15.11.7 therefore reads each candidate's inherited disposition before `Notify`
and excludes any that is `SIG_IGN`, and scopes every wait-status claim to the
signals actually in the caught set.

`signal.Stop` is also the primitive the completion reconciliation needs: its
documented guarantee is that once it returns the channel receives no more
signals, which is the quiescence a bare `Reset` does not give.

## 17. An inherited blocked mask does not survive Go startup — for these signals

The documented hazard is real: `Notify` unblocks a signal inherited blocked, and
`Stop` blocks it again, which would leave a re-raise pending rather than
terminating. Reaching that state took two attempts, and the first was not
evidence:

`os/exec` **resets the child's signal mask**, so a launcher that blocks `SIGINT`
and then spawns through `exec.Command` produces a child that sees it unblocked —
both the "blocked" and "control" runs were the control, and the run proved
nothing. Using `execve`, which preserves the mask:

    LAUNCH mask before exec: SIGINT blocked=true
    B0 child inherited mask has SIGINT blocked=false
    B1 after Notify, SIGINT blocked=false
    B2 after Stop,   SIGINT blocked=false
    exit: 130

The mask was genuinely blocked across the image change, and the Go runtime
unblocked it before user code ran — `os/signal/doc.go` says a program started
with a non-empty mask has some signals explicitly unblocked. The re-raise then
terminated normally.

So the blocked hazard is not reachable for these four signals on this runtime.
§15.11.7 still excludes candidates observed blocked, because the exclusion is
correct either way and a rule resting on a runtime startup detail is worse than
one that does not.

## What would have shipped without these

The round-6 draft bounded the flush with `AttachTailFlushTimeout` applied to a
PTY read, and joined the output writer with a cancellation flag. Probe 1 shows
the first is a no-op that hangs; probe 4 shows the second needed a deadline it
did not have. Both were stated confidently and both were wrong.

The round-7 draft then over-corrected, concluding from probes 1–3 that no
cancellation path exists at all and building the flush around that. Probe 5 shows
that conclusion was broader than its evidence: the untested architecture is
cancellable, and adopting it is what let §15.11.4 replace a racy
buffer-looks-empty cutoff with a barrier whose guarantee is exact.

The round-12 draft then made the identical mistake on the writer side, concluding
from probe 9 that a TTY write cannot be interrupted at all and specifying an
orphaned goroutine. Probe 10 shows the non-blocking event-driven writer is
cancellable, exactly as probe 5 did for the reader. Twice now the trap has been
the same shape: a blocking call that resists interruption says nothing about
whether an event-driven equivalent exists, and the only way to know is to run
it.

## Appendix: probe source

`main.go` (the `nb.go` probes are shown after it; both files sit in the same package):

```go
package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func openpty() (*os.File, *os.File, error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	if err := unix.IoctlSetInt(int(m.Fd()), unix.TIOCPTYGRANT, 0); err != nil {
		return nil, nil, err
	}
	if err := unix.IoctlSetInt(int(m.Fd()), unix.TIOCPTYUNLK, 0); err != nil {
		return nil, nil, err
	}
	var nb [128]byte
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, m.Fd(), uintptr(unix.TIOCPTYGNAME), uintptr(unsafe.Pointer(&nb[0]))); e != 0 {
		return nil, nil, e
	}
	n := 0
	for n < len(nb) && nb[n] != 0 {
		n++
	}
	s, err := os.OpenFile(string(nb[:n]), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	return m, s, nil
}

func watchdog(d time.Duration, label string) {
	go func() {
		time.Sleep(d)
		fmt.Printf("%s: WATCHDOG FIRED — call never returned\n", label)
		os.Exit(9)
	}()
}

func main() {
	switch os.Args[1] {

	case "blocked":
		blockedProbe()

	case "ignore":
		ignoreProbe()

	case "broken-pipe":
		brokenPipeProbe()

	case "sig-write":
		sigWriteProbe()

	case "ofd":
		ofdProbe()

	case "flag-restore":
		flagRestoreProbe()

	case "tty-nonblock":
		ttyNonblockProbe()

	case "tty-write":
		ttyWriteProbe()

	case "fionread":
		fionreadProbe()

	case "starve":
		starveProbe()

	case "nonblock-kqueue":
		nbProbe()

	case "kqueue-eof":
		eofProbe()

	case "pipe-control": // does SetReadDeadline interrupt a blocked pipe read? (harness control)
		r, w, _ := os.Pipe()
		defer w.Close()
		err := r.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		fmt.Printf("pipe SetReadDeadline err=%v\n", err)
		watchdog(4*time.Second, "pipe-control")
		st := time.Now()
		n, rerr := r.Read(make([]byte, 64))
		fmt.Printf("pipe read returned n=%d after %v err=%v istimeout=%v\n",
			n, time.Since(st).Round(10*time.Millisecond), rerr, errors.Is(rerr, os.ErrDeadlineExceeded))

	case "ptmx-deadline": // does SetReadDeadline interrupt a blocked PTY-master read?
		m, s, err := openpty()
		if err != nil {
			fmt.Println("setup failed:", err)
			return
		}
		defer s.Close()
		derr := m.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		fmt.Printf("ptmx SetReadDeadline err=%v (nil means Go accepted it)\n", derr)
		watchdog(4*time.Second, "ptmx-deadline")
		st := time.Now()
		n, rerr := m.Read(make([]byte, 64))
		fmt.Printf("ptmx read returned n=%d after %v err=%v istimeout=%v\n",
			n, time.Since(st).Round(10*time.Millisecond), rerr, errors.Is(rerr, os.ErrDeadlineExceeded))

	case "ptmx-close": // does closing the master from another goroutine unblock the read?
		m, s, err := openpty()
		if err != nil {
			fmt.Println("setup failed:", err)
			return
		}
		defer s.Close()
		go func() { time.Sleep(300 * time.Millisecond); fmt.Println("closing master..."); m.Close() }()
		watchdog(4*time.Second, "ptmx-close")
		st := time.Now()
		n, rerr := m.Read(make([]byte, 64))
		fmt.Printf("ptmx read returned n=%d after %v err=%v\n", n, time.Since(st).Round(10*time.Millisecond), rerr)

	case "sock-write": // does a past write deadline unblock a socket write to a non-reading peer?
		dir, _ := os.MkdirTemp("", "dlp")
		ln, err := net.Listen("unix", dir+"/s")
		if err != nil {
			fmt.Println("listen failed:", err)
			return
		}
		go func() { ln.Accept() }() // accept, never read
		c, err := net.Dial("unix", dir+"/s")
		if err != nil {
			fmt.Println("dial failed:", err)
			return
		}
		res := make(chan string, 1)
		go func() {
			st := time.Now()
			n, werr := c.Write(make([]byte, 8<<20))
			res <- fmt.Sprintf("n=%d after %v err=%v istimeout=%v",
				n, time.Since(st).Round(10*time.Millisecond), werr, errors.Is(werr, os.ErrDeadlineExceeded))
		}()
		time.Sleep(400 * time.Millisecond)
		select {
		case r := <-res:
			fmt.Println("write finished without blocking:", r)
			return
		default:
			fmt.Println("write is blocked; setting a deadline in the past")
		}
		_ = c.SetWriteDeadline(time.Now().Add(-time.Second))
		select {
		case r := <-res:
			fmt.Println("past deadline UNBLOCKED the write:", r)
		case <-time.After(3 * time.Second):
			fmt.Println("STILL BLOCKED after past deadline — a join here would hang")
		}

	case "grandchild-eof": // after the child exits, does a surviving grandchild keep the master open?
		m, s, err := openpty()
		if err != nil {
			fmt.Println("setup failed:", err)
			return
		}
		cmd := exec.Command("/bin/sh", "-c", "printf tailbytes; (sleep 30 &) ")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = s, s, s
		_ = cmd.Start()
		_ = cmd.Wait()
		s.Close() // shim drops its own slave copy; the grandchild still holds one
		buf := make([]byte, 256)
		watchdog(6*time.Second, "grandchild-eof")
		st := time.Now()
		n, e := m.Read(buf)
		fmt.Printf("read1 n=%d data=%q after %v err=%v\n", n, string(buf[:maxi(n, 0)]), time.Since(st).Round(10*time.Millisecond), e)
		fmt.Println("second read (EOF would prove no holder remains); watchdog will fire if it blocks")
		st = time.Now()
		n, e = m.Read(buf)
		fmt.Printf("read2 n=%d after %v err=%v\n", n, time.Since(st).Round(10*time.Millisecond), e)
	}
}

func maxi(a, b int) int {
	if a > b {
		return a
	}
	return b
}
```

`nb.go` (probes 5 and 6):

```go
package main

// Probe: can a PTY master be made cancellable by opening it nonblocking and
// driving it with kqueue, the architecture the round-7 probes did NOT test?

import (
	"fmt"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func openptyRaw(nonblock bool) (int, int, error) {
	flags := unix.O_RDWR
	if nonblock {
		flags |= unix.O_NONBLOCK
	}
	m, err := unix.Open("/dev/ptmx", flags, 0)
	if err != nil {
		return -1, -1, err
	}
	if err := unix.IoctlSetInt(m, unix.TIOCPTYGRANT, 0); err != nil {
		return -1, -1, err
	}
	if err := unix.IoctlSetInt(m, unix.TIOCPTYUNLK, 0); err != nil {
		return -1, -1, err
	}
	var nb [128]byte
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(m), uintptr(unix.TIOCPTYGNAME), uintptr(unsafe.Pointer(&nb[0]))); e != 0 {
		return -1, -1, e
	}
	n := 0
	for n < len(nb) && nb[n] != 0 {
		n++
	}
	s, err := unix.Open(string(nb[:n]), unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return -1, -1, err
	}
	return m, s, nil
}

func nbProbe() {
	m, s, err := openptyRaw(true)
	if err != nil {
		fmt.Println("A: setup failed:", err)
		return
	}
	defer unix.Close(s)
	defer unix.Close(m)

	buf := make([]byte, 256)
	n, rerr := unix.Read(m, buf)
	fmt.Printf("A1 nonblocking read with no data: n=%d err=%v (EAGAIN means it does not block)\n", n, rerr)

	kq, err := unix.Kqueue()
	if err != nil {
		fmt.Println("A: kqueue failed:", err)
		return
	}
	defer unix.Close(kq)

	// register the master for read readiness AND a user event we can trigger to cancel
	const cancelIdent = 42
	ev := []unix.Kevent_t{
		{Ident: uint64(m), Filter: unix.EVFILT_READ, Flags: unix.EV_ADD | unix.EV_ENABLE},
		{Ident: cancelIdent, Filter: unix.EVFILT_USER, Flags: unix.EV_ADD | unix.EV_ENABLE | unix.EV_CLEAR},
	}
	if _, err := unix.Kevent(kq, ev, nil, nil); err != nil {
		fmt.Println("A: kevent register failed:", err)
		return
	}
	fmt.Println("A2 registered master for EVFILT_READ and a EVFILT_USER cancel channel")

	// a second goroutine triggers the user event 300ms in — the cancellation path
	go func() {
		time.Sleep(300 * time.Millisecond)
		trig := []unix.Kevent_t{{Ident: cancelIdent, Filter: unix.EVFILT_USER, Fflags: unix.NOTE_TRIGGER}}
		_, e := unix.Kevent(kq, trig, nil, nil)
		fmt.Printf("A3 cancel triggered from another goroutine (err=%v)\n", e)
	}()

	go func() { time.Sleep(6 * time.Second); fmt.Println("A: WATCHDOG — kevent never returned"); os.Exit(9) }()

	out := make([]unix.Kevent_t, 4)
	st := time.Now()
	nev, err := unix.Kevent(kq, nil, out, nil) // block indefinitely; no timeout used
	el := time.Since(st).Round(10 * time.Millisecond)
	if err != nil {
		fmt.Printf("A4 kevent err=%v after %v\n", err, el)
		return
	}
	for i := 0; i < nev; i++ {
		kind := "read-ready"
		if out[i].Filter == unix.EVFILT_USER {
			kind = "CANCEL"
		}
		fmt.Printf("A4 kevent returned after %v: %s (ident=%d)\n", el, kind, out[i].Ident)
	}
	fmt.Println("A5 => a blocked wait on the master WAS interruptible by another goroutine")
}

func eofProbe() {
	// Does kqueue report EOF on the master when every slave holder closes?
	m, s, err := openptyRaw(true)
	if err != nil {
		fmt.Println("B: setup failed:", err)
		return
	}
	defer unix.Close(m)
	kq, _ := unix.Kqueue()
	defer unix.Close(kq)
	unix.Kevent(kq, []unix.Kevent_t{{Ident: uint64(m), Filter: unix.EVFILT_READ, Flags: unix.EV_ADD | unix.EV_ENABLE}}, nil, nil)
	unix.Write(s, []byte("tail"))
	go func() { time.Sleep(200 * time.Millisecond); unix.Close(s) }()
	go func() { time.Sleep(6 * time.Second); fmt.Println("B: WATCHDOG"); os.Exit(9) }()
	for i := 0; i < 3; i++ {
		out := make([]unix.Kevent_t, 2)
		st := time.Now()
		nev, err := unix.Kevent(kq, nil, out, nil)
		if err != nil || nev == 0 {
			fmt.Printf("B%d kevent err=%v nev=%d\n", i, err, nev)
			return
		}
		eof := out[0].Flags&unix.EV_EOF != 0
		buf := make([]byte, 64)
		n, rerr := unix.Read(m, buf)
		fmt.Printf("B%d after %v: EV_EOF=%v data=%d bytes=%q readerr=%v\n",
			i, time.Since(st).Round(10*time.Millisecond), eof, n, string(buf[:maxi(n, 0)]), rerr)
		if eof && n <= 0 {
			fmt.Println("B => EV_EOF is observable on the master once all slave holders close")
			return
		}
	}
}

```

`fio.go` (probes 7 and 8):

```go
package main

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// FIONREAD is _IOR('f', 127, int) on Darwin; x/sys/unix does not export it.
const fionread = 0x4004667f

func rawSlave(s int) error {
	t, err := unix.IoctlGetTermios(s, unix.TIOCGETA)
	if err != nil {
		return err
	}
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	return unix.IoctlSetTermios(s, unix.TIOCSETA, t)
}

func wd(d time.Duration, label string) {
	go func() { time.Sleep(d); fmt.Printf("%s WATCHDOG\n", label); os.Exit(9) }()
}

func fionreadProbe() {
	m, s, err := openptyRaw(true)
	if err != nil {
		fmt.Println("C setup failed:", err)
		return
	}
	if err := rawSlave(s); err != nil {
		fmt.Println("C rawSlave failed:", err)
	}
	wd(8*time.Second, "C")

	n, e := unix.IoctlGetInt(m, fionread)
	fmt.Printf("C1 FIONREAD on empty master: n=%d err=%v\n", n, e)

	// write in small pieces from a goroutine so a blocking write cannot stall the probe
	var wrote int64
	go func() {
		p := make([]byte, 256)
		for i := 0; i < 4; i++ {
			w, _ := unix.Write(s, p)
			if w > 0 {
				atomic.AddInt64(&wrote, int64(w))
			}
		}
	}()
	time.Sleep(200 * time.Millisecond)
	fmt.Printf("C2 slave writes completed: %d bytes\n", atomic.LoadInt64(&wrote))

	n, e = unix.IoctlGetInt(m, fionread)
	fmt.Printf("C3 FIONREAD with bytes definitely pending: n=%d err=%v\n", n, e)

	buf := make([]byte, 4096)
	r, rerr := unix.Read(m, buf)
	fmt.Printf("C4 one read returned r=%d err=%v (proves bytes really were pending)\n", r, rerr)

	n, e = unix.IoctlGetInt(m, fionread)
	fmt.Printf("C5 FIONREAD after that read: n=%d err=%v\n", n, e)
	fmt.Println("C=> FIONREAD is a usable snapshot only if C3 matched C2 and C5 dropped")
	unix.Close(s)
	unix.Close(m)
}

func starveProbe() {
	m, s, err := openptyRaw(true)
	if err != nil {
		fmt.Println("D setup failed:", err)
		return
	}
	_ = rawSlave(s)

	var produced int64
	stop := make(chan struct{})
	go func() { // continuous producer: the surviving grandchild that keeps writing
		blk := make([]byte, 4096)
		for {
			select {
			case <-stop:
				return
			default:
			}
			w, e := unix.Write(s, blk)
			if w > 0 {
				atomic.AddInt64(&produced, int64(w))
			} else if e != nil && e != unix.EAGAIN {
				return
			}
		}
	}()
	time.Sleep(300 * time.Millisecond)
	fmt.Printf("D1 producer has written %d bytes and is still running\n", atomic.LoadInt64(&produced))

	buf := make([]byte, 65536)
	done := make(chan int64, 1)
	go func() {
		var total int64
		for {
			r, rerr := unix.Read(m, buf)
			if r > 0 {
				total += int64(r)
				continue
			}
			if rerr == unix.EAGAIN {
				done <- total
				return
			}
			done <- -1
			return
		}
	}()
	select {
	case t := <-done:
		fmt.Printf("D2 read-until-EAGAIN TERMINATED after %d bytes (producer wrote %d total)\n",
			t, atomic.LoadInt64(&produced))
		fmt.Println("D=> the loop is NOT starved: the producer blocks on a full PTY queue, so the reader wins")
	case <-time.After(4 * time.Second):
		fmt.Printf("D2 read-until-EAGAIN DID NOT TERMINATE in 4s (producer wrote %d)\n", atomic.LoadInt64(&produced))
		fmt.Println("D=> the loop CAN be starved by a continuous producer")
	}
	close(stop)
	time.Sleep(50 * time.Millisecond)
	unix.Close(s)
	unix.Close(m)
	os.Exit(0)
}
```

`tty.go` (probe 9):

```go
package main

// Probe: can a Write to a TTY be interrupted? The client's terminal writer is a
// TTY (the operator's terminal), and R90 asks whether a deadline bounds it.
// A PTY slave IS a tty, so we use one as a stand-in for the operator's terminal
// and simply never read the master — the parked-terminal case.

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func ttyWriteProbe() {
	m, sfd, err := openptyRaw(false) // blocking slave, like a real terminal
	if err != nil {
		fmt.Println("E setup failed:", err)
		return
	}
	_ = rawSlave(sfd)
	defer unix.Close(m)

	tty := os.NewFile(uintptr(sfd), "/dev/tty-standin")
	fmt.Printf("E0 slave is a terminal: %v\n", func() bool {
		_, e := unix.IoctlGetTermios(sfd, unix.TIOCGETA)
		return e == nil
	}())

	// Is a deadline even accepted on a TTY os.File?
	derr := tty.SetWriteDeadline(time.Now().Add(300 * time.Millisecond))
	fmt.Printf("E1 SetWriteDeadline on a TTY: err=%v (ErrNoDeadline would be an honest refusal)\n", derr)

	res := make(chan string, 1)
	go func() {
		// nobody ever reads the master, so this fills the tty queue and parks
		st := time.Now()
		n, werr := tty.Write(make([]byte, 4<<20))
		res <- fmt.Sprintf("n=%d after %v err=%v istimeout=%v",
			n, time.Since(st).Round(10*time.Millisecond), werr, errors.Is(werr, os.ErrDeadlineExceeded))
	}()

	select {
	case r := <-res:
		fmt.Println("E2 write returned on its own:", r)
	case <-time.After(2 * time.Second):
		fmt.Println("E2 write is PARKED after 2s despite the 300ms deadline")
	}

	// Does closing the file unblock it?
	go func() { time.Sleep(200 * time.Millisecond); fmt.Println("E3 closing the tty file..."); tty.Close() }()
	select {
	case r := <-res:
		fmt.Println("E4 close unblocked the write:", r)
	case <-time.After(2 * time.Second):
		fmt.Println("E4 write STILL PARKED after close — a join on the terminal writer cannot be bounded")
	}
	os.Exit(0)
}
```

`ttynb.go` (probe 10) and `flags.go` (probe 11):

```go
package main

// Probe: is an event-driven, non-blocking TTY writer cancellable? This is the
// writer-side analogue of probe 5, and the architecture probe 9 did NOT test.

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func ttyNonblockProbe() {
	m, sfd, err := openptyRaw(false)
	if err != nil {
		fmt.Println("F setup failed:", err)
		return
	}
	_ = rawSlave(sfd)
	defer unix.Close(m)

	// preserve the original descriptor flags, as a real client must
	orig, err := unix.FcntlInt(uintptr(sfd), unix.F_GETFL, 0)
	if err != nil {
		fmt.Println("F F_GETFL failed:", err)
		return
	}
	fmt.Printf("F0 original descriptor flags: 0x%x (O_NONBLOCK set: %v)\n", orig, orig&unix.O_NONBLOCK != 0)

	if _, err := unix.FcntlInt(uintptr(sfd), unix.F_SETFL, orig|unix.O_NONBLOCK); err != nil {
		fmt.Println("F1 could not set O_NONBLOCK on a TTY:", err)
		return
	}
	fmt.Println("F1 O_NONBLOCK set on the TTY")

	// fill the output queue with bounded chunks until the kernel says EAGAIN
	chunk := make([]byte, 4096)
	total := 0
	var last error
	for i := 0; i < 10000; i++ {
		n, e := unix.Write(sfd, chunk)
		if n > 0 {
			total += n
		}
		if e == unix.EAGAIN {
			last = e
			break
		}
		if e != nil {
			last = e
			break
		}
	}
	fmt.Printf("F2 wrote %d bytes in bounded chunks, then err=%v (EAGAIN means it never parked)\n", total, last)
	if last != unix.EAGAIN {
		fmt.Println("F2 => queue never filled; result inconclusive for this run")
		unix.FcntlInt(uintptr(sfd), unix.F_SETFL, orig)
		return
	}

	// now park in kevent waiting for writability, with a user-event cancel channel
	kq, err := unix.Kqueue()
	if err != nil {
		fmt.Println("F kqueue failed:", err)
		return
	}
	defer unix.Close(kq)
	const cancelIdent = 77
	if _, err := unix.Kevent(kq, []unix.Kevent_t{
		{Ident: uint64(sfd), Filter: unix.EVFILT_WRITE, Flags: unix.EV_ADD | unix.EV_ENABLE},
		{Ident: cancelIdent, Filter: unix.EVFILT_USER, Flags: unix.EV_ADD | unix.EV_ENABLE | unix.EV_CLEAR},
	}, nil, nil); err != nil {
		fmt.Println("F3 kevent register failed:", err)
		return
	}
	fmt.Println("F3 registered the TTY for EVFILT_WRITE beside an EVFILT_USER cancel")

	go func() {
		time.Sleep(300 * time.Millisecond)
		_, e := unix.Kevent(kq, []unix.Kevent_t{{Ident: cancelIdent, Filter: unix.EVFILT_USER, Fflags: unix.NOTE_TRIGGER}}, nil, nil)
		fmt.Printf("F4 cancel triggered from another goroutine (err=%v)\n", e)
	}()
	go func() { time.Sleep(6 * time.Second); fmt.Println("F WATCHDOG — kevent never returned"); os.Exit(9) }()

	out := make([]unix.Kevent_t, 4)
	st := time.Now()
	nev, err := unix.Kevent(kq, nil, out, nil)
	if err != nil {
		fmt.Printf("F5 kevent err=%v\n", err)
		return
	}
	for i := 0; i < nev; i++ {
		kind := "WRITE-READY"
		if out[i].Filter == unix.EVFILT_USER {
			kind = "CANCEL"
		}
		fmt.Printf("F5 kevent returned after %v: %s\n", time.Since(st).Round(10*time.Millisecond), kind)
	}
	fmt.Println("F6 => an event-driven TTY writer IS cancellable while the queue is full")

	// restore the original flags, as the spec will require
	if _, err := unix.FcntlInt(uintptr(sfd), unix.F_SETFL, orig); err != nil {
		fmt.Println("F7 could not restore descriptor flags:", err)
	} else {
		now, _ := unix.FcntlInt(uintptr(sfd), unix.F_GETFL, 0)
		fmt.Printf("F7 descriptor flags restored: 0x%x (matches original: %v)\n", now, now == orig)
	}
	os.Exit(0)
}

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func flagRestoreProbe() {
	_, sfd, err := openptyRaw(false)
	if err != nil {
		fmt.Println("G setup failed:", err)
		return
	}
	orig, _ := unix.FcntlInt(uintptr(sfd), unix.F_GETFL, 0)
	fmt.Printf("G0 original F_GETFL = 0x%x   O_NONBLOCK(0x%x) set: %v\n",
		orig, unix.O_NONBLOCK, orig&unix.O_NONBLOCK != 0)

	_, _ = unix.FcntlInt(uintptr(sfd), unix.F_SETFL, orig|unix.O_NONBLOCK)
	mid, _ := unix.FcntlInt(uintptr(sfd), unix.F_GETFL, 0)
	fmt.Printf("G1 after setting O_NONBLOCK: 0x%x   O_NONBLOCK set: %v\n", mid, mid&unix.O_NONBLOCK != 0)

	// (a) naive restore: write the whole saved word back
	_, _ = unix.FcntlInt(uintptr(sfd), unix.F_SETFL, orig)
	a, _ := unix.FcntlInt(uintptr(sfd), unix.F_GETFL, 0)
	fmt.Printf("G2 naive F_SETFL(orig): 0x%x  equals orig: %v  O_NONBLOCK cleared: %v  extra bits: 0x%x\n",
		a, a == orig, a&unix.O_NONBLOCK == 0, a&^orig)

	// (b) targeted restore: clear only the bit we set
	_, _ = unix.FcntlInt(uintptr(sfd), unix.F_SETFL, mid&^unix.O_NONBLOCK)
	b, _ := unix.FcntlInt(uintptr(sfd), unix.F_GETFL, 0)
	fmt.Printf("G3 targeted clear of O_NONBLOCK: 0x%x  O_NONBLOCK cleared: %v\n", b, b&unix.O_NONBLOCK == 0)

	// does kqueue registration itself alter what F_GETFL reports?
	kq, _ := unix.Kqueue()
	before, _ := unix.FcntlInt(uintptr(sfd), unix.F_GETFL, 0)
	_, kerr := unix.Kevent(kq, []unix.Kevent_t{{Ident: uint64(sfd), Filter: unix.EVFILT_WRITE, Flags: unix.EV_ADD | unix.EV_ENABLE}}, nil, nil)
	after, _ := unix.FcntlInt(uintptr(sfd), unix.F_GETFL, 0)
	fmt.Printf("G5 kqueue EVFILT_WRITE register (err=%v): F_GETFL 0x%x -> 0x%x  delta 0x%x\n", kerr, before, after, after^before)
	unix.Close(kq)

	fmt.Printf("G4 => the bit that matters (O_NONBLOCK) round-trips; the full word does not.\n")
	fmt.Printf("G4    F_GETFL reports bits F_SETFL neither accepts nor stores; 0x%x here.\n", a&^orig)
	os.Exit(0)
}
```

`ofd.go` (probes 12 and 13):

```go
package main

// Probe: does a FRESH open of the same terminal isolate O_NONBLOCK from the
// inherited descriptor, and does dup fail to? And can identity be established
// by fstat rather than by path?

import (
	"fmt"
	"os"

	"unsafe"

	"golang.org/x/sys/unix"
)

func ofdProbe() {
	_, sfd, err := openptyRaw(false)
	if err != nil {
		fmt.Println("H setup failed:", err)
		return
	}
	// sfd stands in for the inherited stdout the shell shares with us
	var nb [128]byte
	name, nerr := ttyName(sfd)
	if nerr != nil {
		fmt.Println("H0 F_GETPATH failed:", nerr)
		return
	}
	fmt.Printf("H0 stand-in inherited terminal: %s (buffer PathMax=%d)\n", name, unix.PathMax)
	_ = nb

	orig, _ := unix.FcntlInt(uintptr(sfd), unix.F_GETFL, 0)
	fmt.Printf("H1 inherited flags before: 0x%x (O_NONBLOCK set: %v)\n", orig, orig&unix.O_NONBLOCK != 0)

	// (a) dup — shares the open-file description?
	d, err := unix.Dup(sfd)
	if err != nil {
		fmt.Println("H2 dup failed:", err)
		return
	}
	df, _ := unix.FcntlInt(uintptr(d), unix.F_GETFL, 0)
	_, _ = unix.FcntlInt(uintptr(d), unix.F_SETFL, df|unix.O_NONBLOCK)
	after, _ := unix.FcntlInt(uintptr(sfd), unix.F_GETFL, 0)
	fmt.Printf("H2 set O_NONBLOCK on the dup: inherited now 0x%x  LEAKED: %v\n",
		after, after&unix.O_NONBLOCK != 0)
	_, _ = unix.FcntlInt(uintptr(d), unix.F_SETFL, df)
	unix.Close(d)

	// (b) fresh open of the same terminal by name
	fresh, err := unix.Open(name, unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		fmt.Println("H3 fresh open failed:", err)
		return
	}
	ff, _ := unix.FcntlInt(uintptr(fresh), unix.F_GETFL, 0)
	_, _ = unix.FcntlInt(uintptr(fresh), unix.F_SETFL, ff|unix.O_NONBLOCK)
	after2, _ := unix.FcntlInt(uintptr(sfd), unix.F_GETFL, 0)
	freshAfter, _ := unix.FcntlInt(uintptr(fresh), unix.F_GETFL, 0)
	fmt.Printf("H3 set O_NONBLOCK on a FRESH open: fresh=0x%x  inherited=0x%x  LEAKED: %v\n",
		freshAfter, after2, after2&unix.O_NONBLOCK != 0)

	// (c) identity by fstat rather than by path
	var a, b unix.Stat_t
	_ = unix.Fstat(sfd, &a)
	_ = unix.Fstat(fresh, &b)
	fmt.Printf("H4 identity: inherited rdev=%d ino=%d | fresh rdev=%d ino=%d | same device: %v\n",
		a.Rdev, a.Ino, b.Rdev, b.Ino, a.Rdev == b.Rdev)

	// (d) production opens O_WRONLY while the earlier probe used O_RDWR:
	// do the tuple components survive differing access modes on one device?
	wo, werr := unix.Open(name, unix.O_WRONLY|unix.O_NOCTTY, 0)
	if werr == nil {
		var w unix.Stat_t
		_ = unix.Fstat(wo, &w)
		fmt.Printf("H6 O_WRONLY handle: dev=%d rdev=%d ino=%d\n", w.Dev, w.Rdev, w.Ino)
		fmt.Printf("H6 vs O_RDWR inherited: dev=%d rdev=%d ino=%d  all three equal: %v\n",
			a.Dev, a.Rdev, a.Ino, w.Dev == a.Dev && w.Rdev == a.Rdev && w.Ino == a.Ino)
		unix.Close(wo)
	} else {
		fmt.Println("H6 O_WRONLY open failed:", werr)
	}

	// (e) a DIFFERENT terminal must not compare equal
	_, other, err2 := openptyRaw(false)
	if err2 == nil {
		var c unix.Stat_t
		_ = unix.Fstat(other, &c)
		fmt.Printf("H5 a different terminal: rdev=%d  compares equal to inherited: %v\n",
			c.Rdev, c.Rdev == a.Rdev)
		unix.Close(other)
	}
	unix.Close(fresh)
	os.Exit(0)
}

func ttyName(fd int) (string, error) {
	// F_GETPATH requires a buffer of MAXPATHLEN or greater (fcntl(2)).
	// PATH_MAX on Darwin is 1024; an undersized buffer is a contract violation
	// even when the path that comes back is short.
	var nb [unix.PathMax]byte
	if _, _, e := unix.Syscall(unix.SYS_FCNTL, uintptr(fd), uintptr(unix.F_GETPATH), uintptr(unsafe.Pointer(&nb[0]))); e != 0 {
		return "", e
	}
	n := 0
	for n < len(nb) && nb[n] != 0 {
		n++
	}
	if n == len(nb) {
		return "", fmt.Errorf("F_GETPATH returned no NUL terminator within %d bytes", len(nb))
	}
	return string(nb[:n]), nil
}
```

`sigw.go` (probe 14):

```go
package main

// Probe: does a SIGINT return an os.File.Write that is blocked on a full pipe?
// This is the exact production call path the fd-2 diagnostic would use.

import (
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func sigWriteProbe() {
	r, w, err := os.Pipe()
	if err != nil {
		fmt.Println("I setup failed:", err)
		return
	}
	// keep the read end alive and open; an unreferenced *os.File is finalized
	// and closed, which returns the write with EPIPE for the wrong reason.
	defer runtime.KeepAlive(r)

	// find the pipe capacity by filling it non-blockingly first, then restore blocking
	fmt.Println("I0 pipe created; writer left in BLOCKING mode (as fd 2 would be)")

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT)

	done := make(chan string, 1)
	go func() {
		// a payload far larger than any pipe buffer, like a row into a full sink
		st := time.Now()
		n, werr := w.Write(make([]byte, 4<<20))
		done <- fmt.Sprintf("Write RETURNED n=%d after %v err=%v", n, time.Since(st).Round(10*time.Millisecond), werr)
	}()

	time.Sleep(300 * time.Millisecond)
	fmt.Println("I1 delivering SIGINT to self while the write is blocked")
	_ = unix.Kill(unix.Getpid(), unix.SIGINT)

	select {
	case sig := <-sigs:
		fmt.Printf("I2 handler OBSERVED the signal: %v (the owner goroutine can act)\n", sig)
	case <-time.After(2 * time.Second):
		fmt.Println("I2 handler did NOT observe the signal")
	}

	runtime.KeepAlive(r)
	select {
	case res := <-done:
		fmt.Println("I3", res)
		fmt.Println("I3 => the blocked write DID return; check the error before concluding")
	case <-time.After(2 * time.Second):
		fmt.Println("I3 the blocked write did NOT return after the signal")
		fmt.Println("I3 => SA_RESTART/ignoringEINTRIO hold: the writing goroutine cannot restore-and-re-raise itself")
	}
	runtime.KeepAlive(r)
	os.Exit(0)
}
```

`pipeprobe.go` (probe 15):

```go
package main

// Probe: writing a diagnostic to a broken pipe on fd 2 vs through a dup of it.
// Does fd 2 die by SIGPIPE (bypassing the chosen exit status), and does a dup
// return EPIPE as an ordinary error instead?

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func brokenPipeProbe() {
	mode := os.Args[2] // "fd2" or "dup"

	r, w, err := os.Pipe()
	if err != nil {
		fmt.Println("setup failed:", err)
		os.Exit(70)
	}
	// make the pipe broken: close the read end
	r.Close()

	// install w as fd 2, exactly as a shell redirect would
	if err := syscall.Dup2(int(w.Fd()), 2); err != nil {
		fmt.Println("dup2 failed:", err)
		os.Exit(70)
	}

	switch mode {
	case "fd2":
		// the ordinary path: write the diagnostic to os.Stderr
		n, werr := os.Stderr.Write([]byte("agentctl: a selected outcome row\n"))
		// if we get here, SIGPIPE did not kill us
		fmt.Printf("RESULT fd2 write returned n=%d err=%v isEPIPE=%v\n", n, werr, errors.Is(werr, syscall.EPIPE))
		os.Exit(6) // the outcome exit code we intended to report
	case "dup-fd1-closed":
		// The dangerous shape: fd 1 closed, so plain dup(2) returns 1 —
		// which Go treats as stdoutOrErr and raises SIGPIPE for.
		syscall.Close(1)
		nfd, derr := syscall.Dup(2)
		if derr != nil {
			fmt.Fprintln(os.Stderr, "dup failed:", derr)
			os.Exit(70)
		}
		if nfd <= 2 {
			// report through a descriptor we know is safe
			msg := fmt.Sprintf("NOTE plain dup(2) returned fd %d (<=2)\n", nfd)
			syscall.Write(9, []byte(msg))
		}
		f := os.NewFile(uintptr(nfd), "dup-of-stderr")
		n, werr := f.Write([]byte("agentctl: a selected outcome row\n"))
		syscall.Write(9, []byte(fmt.Sprintf("RESULT dup-fd1-closed n=%d err=%v\n", n, werr)))
		os.Exit(6)

	case "dupfd3":
		// F_DUPFD_CLOEXEC with a floor of 3, the proposed fix
		syscall.Close(1)
		nfd, derr := unix.FcntlInt(uintptr(2), unix.F_DUPFD_CLOEXEC, 3)
		if derr != nil {
			syscall.Write(9, []byte(fmt.Sprintf("F_DUPFD_CLOEXEC failed: %v\n", derr)))
			os.Exit(70)
		}
		syscall.Write(9, []byte(fmt.Sprintf("NOTE F_DUPFD_CLOEXEC(3) returned fd %d\n", nfd)))
		f := os.NewFile(uintptr(nfd), "dup-of-stderr")
		n, werr := f.Write([]byte("agentctl: a selected outcome row\n"))
		syscall.Write(9, []byte(fmt.Sprintf("RESULT dupfd3 n=%d err=%v\n", n, werr)))
		os.Exit(6)

	case "dup":
		// write through a dup of fd 2 that is NOT fd 1 or 2
		nfd, derr := syscall.Dup(2)
		if derr != nil {
			fmt.Println("dup failed:", derr)
			os.Exit(70)
		}
		f := os.NewFile(uintptr(nfd), "dup-of-stderr")
		n, werr := f.Write([]byte("agentctl: a selected outcome row\n"))
		fmt.Printf("RESULT dup write returned n=%d err=%v isEPIPE=%v\n", n, werr, errors.Is(werr, syscall.EPIPE))
		os.Exit(6)
	}
}
```

`ignprobe.go` (probe 16):

```go
package main

// Probe: if SIGINT was ignored at start, does Notify+Reset+re-raise actually
// kill the process (producing the promised signal wait status)?

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"unsafe"

	"golang.org/x/sys/unix"
)

func ignoreProbe() {
	mode := os.Args[2] // "reset" or "detect"

	// Observe the inherited disposition WITHOUT installing anything.
	h, err := querySigaction(unix.SIGINT)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sigaction query failed:", err)
		os.Exit(70)
	}
	ignored := h == sigIgn
	fmt.Fprintf(os.Stderr, "P0 inherited SIGINT handler=%#x ignored=%v\n", h, ignored)

	switch mode {
	case "detect":
		// The proposed rule: if inherited ignored, do not add it to the caught set.
		if ignored {
			fmt.Fprintln(os.Stderr, "P1 detect: SIGINT inherited ignored -> excluded from caught set")
			os.Exit(6) // exits with the selected code, never claiming a signal wait status
		}
		fmt.Fprintln(os.Stderr, "P1 detect: SIGINT not ignored -> may be caught")
		os.Exit(6)

	case "reset":
		// The naive rule: Notify, then Reset, then re-raise, and expect death.
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGINT)
		_ = unix.Kill(unix.Getpid(), unix.SIGINT)
		select {
		case s := <-c:
			fmt.Fprintf(os.Stderr, "P1 caught %v via Notify despite inherited ignore\n", s)
		case <-time.After(time.Second):
			fmt.Fprintln(os.Stderr, "P1 no signal observed")
		}
		signal.Reset(syscall.SIGINT)
		nowh, _ := querySigaction(unix.SIGINT)
		fmt.Fprintf(os.Stderr, "P2 after Reset, SIGINT handler=%#x (%#x == SIG_IGN, 0 == SIG_DFL)\n", nowh, uintptr(sigIgn))
		fmt.Fprintln(os.Stderr, "P3 re-raising; if the disposition is SIG_IGN this does nothing")
		_ = unix.Kill(unix.Getpid(), unix.SIGINT)
		time.Sleep(300 * time.Millisecond)
		fmt.Fprintln(os.Stderr, "P4 STILL ALIVE after re-raise -> no signal wait status is produced")
		os.Exit(6)
	}
}

const sigIgn = 1

// darwin struct sigaction: handler, mask, flags
type darwinSigaction struct {
	handler uintptr
	mask    uint32
	flags   int32
}

func querySigaction(sig unix.Signal) (uintptr, error) {
	var old darwinSigaction
	_, _, e := unix.Syscall(unix.SYS_SIGACTION, uintptr(sig), 0, uintptr(unsafe.Pointer(&old)))
	if e != 0 {
		return 0, e
	}
	return old.handler, nil
}
```

`blkprobe.go` (probe 17):

```go
package main

// Probe: a signal inherited BLOCKED. Does Notify+Stop+re-raise terminate, or
// does Stop re-block it so the process survives with the selected exit code?

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"unsafe"

	"golang.org/x/sys/unix"
)

// Darwin sigprocmask via raw syscall; x/sys/unix does not export Sigset_t here.
const (
	sigBlock  = 1
	sigSetmsk = 3
)

func procmask(how int, set uint32) (uint32, error) {
	var oldset uint32
	var newset = set
	_, _, e := unix.Syscall(unix.SYS_SIGPROCMASK, uintptr(how), uintptr(unsafe.Pointer(&newset)), uintptr(unsafe.Pointer(&oldset)))
	if e != 0 {
		return 0, e
	}
	return oldset, nil
}

func currentMask() uint32 {
	var oldset uint32
	var dummy uint32
	_, _, _ = unix.Syscall(unix.SYS_SIGPROCMASK, uintptr(sigBlock), uintptr(unsafe.Pointer(&dummy)), uintptr(unsafe.Pointer(&oldset)))
	return oldset
}

func blockedProbe() {
	bit := uint32(1) << (uint(unix.SIGINT) - 1)

	switch os.Args[2] {
	case "launch-blocked", "launch-clear":
		if os.Args[2] == "launch-blocked" {
			if _, err := procmask(sigBlock, bit); err != nil {
				fmt.Println("sigprocmask failed:", err)
				os.Exit(70)
			}
		}
		self, _ := os.Executable()
		if os.Getenv("USE_EXEC") == "1" {
			// execve preserves the signal mask across the image change,
			// unlike fork+exec through os/exec which resets it.
			fmt.Fprintf(os.Stderr, "LAUNCH mask before exec: SIGINT blocked=%v\n", currentMask()&bit != 0)
			_ = unix.Exec(self, []string{self, "blocked", "child"}, os.Environ())
			fmt.Println("exec failed")
			os.Exit(70)
		}
		cmd := exec.Command(self, "blocked", "child")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		err := cmd.Run()
		if ee, ok := err.(*exec.ExitError); ok {
			st := ee.Sys().(syscall.WaitStatus)
			if st.Signaled() {
				fmt.Printf("PARENT: child KILLED by signal %v\n", st.Signal())
			} else {
				fmt.Printf("PARENT: child exited %d\n", st.ExitStatus())
			}
			return
		}
		if err != nil {
			fmt.Println("run failed:", err)
			return
		}
		fmt.Println("PARENT: child exited 0")

	case "child":
		fmt.Fprintf(os.Stderr, "B0 child inherited mask has SIGINT blocked=%v\n", currentMask()&bit != 0)
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGINT)
		fmt.Fprintf(os.Stderr, "B1 after Notify, SIGINT blocked=%v (doc: Notify unblocks)\n", currentMask()&bit != 0)
		signal.Stop(c)
		fmt.Fprintf(os.Stderr, "B2 after Stop,   SIGINT blocked=%v (doc: becomes blocked again)\n", currentMask()&bit != 0)
		_ = unix.Kill(unix.Getpid(), unix.SIGINT)
		time.Sleep(400 * time.Millisecond)
		fmt.Fprintln(os.Stderr, "B3 STILL ALIVE after re-raise -> pending, not terminating")
		os.Exit(6)
	}
}
```
