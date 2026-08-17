# Attach implementation notes

Non-normative. The contract is [spec §15.11]; this file records *how* an
implementation can satisfy it on Darwin, and — more importantly — which
approaches were tried and **do not work**. Nothing here constrains a future
implementation that satisfies the contract differently.

Read this before reimplementing any part of the attach path. Several sections
below are the only known Darwin-viable mechanism for the property they serve, and
the alternatives that fail are the obvious ones.

**Two kinds of evidence appear below, and they are labelled differently on
purpose.** A claim marked *(probe N)* was established by running the rejected
alternative and observing it fail — the log is
[the probe log](2026-08-14-attach-darwin-io-probes.md). A claim marked
*(documented)* rests on Go's own documentation or runtime source, cited exactly,
with **no probe exercising the rejected alternative**. Both are sound reasons to
follow the note; only the first is evidence that the alternative was tried. An
earlier version of this file presented all of them as probe-established, which
would have left a future reader believing an untested claim had been tested.

## Mechanisms that are the only known Darwin-viable option

These are not preferences. Each has at least one probed alternative that fails,
usually silently.

### The PTY master must be non-blocking and kqueue-driven *(probes 1, 2, 3, 5, 6, 7)*

A blocking master **cannot be cancelled at all**: `SetReadDeadline` returns `nil`
and then does nothing (probe 1 — the read never returns), and closing the master
from another goroutine does not release it either (probe 2). Both failures are
silent; code written against them reads as though it has a bound it does not
have.

A non-blocking master driven by `kqueue` is cancellable: reads return `EAGAIN`
rather than parking, and a wait parked in `kevent` is interrupted on demand by an
`EVFILT_USER` event (probe 5). `EV_EOF` is observable once every slave holder
closes (probe 6), but a surviving grandchild keeps it unset indefinitely (probe
3), so no outcome may be conditioned on EOF.

There is no readable-byte snapshot to bound a drain pass with: `FIONREAD` reports
0 while bytes are genuinely pending (probe 7).

### The terminal writer must be non-blocking and kqueue-driven *(probes 9, 10)*

Exactly the same shape as the reader, and the same trap. A blocking TTY write
cannot be interrupted — `SetWriteDeadline` is *honestly refused* with `file type
does not support deadline`, the write parks, and `Close` does not release it
(probe 9). With `O_NONBLOCK` set, the write returns `EAGAIN` and a `kevent` wait
is cancellable (probe 10).

An earlier draft concluded from probe 9 alone that no cancellable terminal writer
existed and specified an orphaned goroutine. That conclusion was broader than its
evidence, and probe 10 reversed it. This mistake was made twice — once on the
reader, once on the writer — and the lesson is the same both times: **a blocking
call that resists interruption says nothing about whether an event-driven
equivalent exists.**

### The client must not mutate the inherited terminal descriptor *(probes 11, 12, 13)*

`F_SETFL` acts on the open-file description, which an inherited `stdout` shares
with the parent shell. Setting `O_NONBLOCK` through a `dup` **leaks it to the
parent** (probe 12), where it is observable while attach runs, after `SIGSTOP`
hands control back, and permanently after an uncatchable death.

A fresh `open` of the same terminal does not leak. Terminal identity must come
from `fstat` — the `(st_dev, st_rdev, st_ino)` triple — not from a path, which is
a name rather than an identity (probe 13). The triple survives differing access
modes, so an `O_WRONLY` private handle compares equal to an inherited `O_RDWR`
descriptor on the same terminal.

Restoration should clear the specific bit rather than writing back a saved flag
word: in one run `F_GETFL` afterwards reported an extra bit whose cause was never
identified, while `O_NONBLOCK` itself cleared correctly (probe 11).

### Diagnostics must not be written to descriptor 2 itself *(probe 15)*

Go raises `SIGPIPE` for `EPIPE` on descriptors 1 and 2 specifically. Writing a
selected outcome row to a broken pipe on fd 2 **kills the process** — the exit
code the attachment selected never happens, so the outcome is not truncated but
erased (probe 15).

Writing the identical bytes through a duplicate returns `EPIPE` as an ordinary
error. The duplicate must be taken with `F_DUPFD_CLOEXEC` and a **floor of 3**,
atomically: plain `dup` returns the lowest free descriptor, so with fd 1 closed
it returns 1 and reintroduces the bug it was added to fix (probe 15).

### A blocked write cannot be interrupted by a signal *(probe 14, plus runtime source)*

`SA_RESTART` is set by the Go runtime's `setsig`, and `internal/poll.FD.Write`
retries every `EINTR` through `ignoringEINTRIO`. A write blocked on a full pipe
therefore need not return: the handler observes the signal promptly while the
write stays parked (probe 14). A writing goroutine cannot restore-and-re-raise
itself; the owner must act independently and terminate without joining it.

`FD.Write` also accumulates successful kernel writes privately and returns only
on completion or error, so a prefix can be in the pipe while the caller has
observed no return at all — which is why an accepted count is unobservable while
a write is in flight, and known once it returns.

### Signal handling has four traps

1. **Inherited-ignored signals** *(probe 16)*. `Notify` on a `SIGINT` ignored at
   start installs a handler anyway, and `Reset`/`Stop` restores the ignored state
   — so a re-raise does nothing and the promised wait status never arrives. The
   probe ran it: the process stayed alive and exited with the selected code,
   while the not-ignored control was killed. Observe disposition before `Notify`
   and exclude what is ignored.
2. **Empty signal sets** *(documented — `os/signal` `Notify` doc: "If no signals
   are provided, all incoming signals will be relayed to c")*. The natural
   spelling `Notify(ch, set...)` therefore inverts the design when the set is
   empty, catching the candidates you excluded plus unrelated signals such as
   `SIGPIPE`. Do not call `Notify` at all in that case. No probe exercises this;
   the documentation is unambiguous.
3. **Refcounted `Stop`** *(documented — `os/signal/signal.go`: `Stop` decrements
   `handlers.ref[n]` and calls `disableSignal(n)` only at zero)*. Another
   registration for the same signal therefore leaves the process handler
   installed, and a process-directed re-raise can be consumed by that other
   channel instead of terminating. `Reset` the retained signal after `Stop` and
   before re-raising. No probe exercises the competing-registration race.
4. **Per-thread masks** *(documented — POSIX `pthread_sigmask`; `runtime`
   `LockOSThread` "wires the calling goroutine to its current operating system
   thread")*. A mask read from an arbitrary goroutine proves nothing durable,
   since the goroutine may migrate; lock the OS thread before observing and hold
   it through the re-raise. Probe 17 is related but proves something narrower: a
   mask blocked across `execve` is unblocked by the Go runtime before user code
   runs, so the inherited-blocked case may not be reachable in practice. It does
   **not** establish the per-thread requirement, which is why the exclusion is
   kept on documented grounds.

`unix.SignalName` is the canonical number-to-name mapping *(documented, plus a
one-off check retained here: `SignalName(6)` returns `SIGABRT` — Darwin aliases 6
as both `SIGABRT` and `SIGIOT` — and `Signal(6).String()` returns `"abort trap"`,
while an unmapped number returns `""`)*. `Signal.String()` returns lowercase
descriptions like `interrupt`, so an unpinned "platform name" lets two conforming
implementations print different rows. This is a naming-stability requirement, not
a Darwin-viability one, and is recorded here because the same re-derivation risk
applies.

## Mechanisms that are ordinary engineering

These satisfy contract properties and could reasonably be built another way.

- **Never throttling the agent.** Drain the PTY at full speed always; with no
  viewer, discard. With a viewer, copy into a bounded lag buffer and let a
  separate writer serve it. Appending never waits; overflow evicts rather than
  blocking. Two earlier designs failed this: a per-write deadline evicted correct
  slow readers, and a sustained-progress rule let a slow reader hold the drain to
  its own rate.
- **Bounding the tail after child exit.** Child exit does not imply PTY EOF, so a
  flush must be bounded by time rather than by EOF, and its claims scoped to
  bytes actually read and committed. A cutoff established by a barrier the drain
  acknowledges is one way; whatever the mechanism, the contract's requirement is
  that the reported counts be exact and the outcome distinguish a complete flush
  from an incomplete one from an unconfirmed one.
- **Exactly-once release.** Many triggers can fire concurrently; an atomic
  transition with the winner fixing the outcome is one way to guarantee one final
  frame, one disposition, and one close.
- **Client relay shape.** A bounded queue between the socket reader and the
  terminal writer, with capacity counted over queued **plus** in-flight bytes, is
  one way to hold bounded memory and bounded completion simultaneously. Lossless
  relay to an arbitrarily slow terminal is the property that must be given up;
  the contract requires only that the shortfall be reported.

## What the single-connection transport removed

The two-connection design required a claim token, a data handshake, and
cross-connection race handling — resize versus release, delayed data connections
authenticating against a previous viewer, and epoch revalidation spanning
connections. A single ordered stream makes those ordering guarantees properties
of the stream itself, and the mechanisms that existed only to reconcile two
streams were deleted rather than demoted.
