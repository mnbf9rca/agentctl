# Security review of merged v1 against SECURITY.md

Issue: [#81](https://github.com/mnbf9rca/agentctl/issues/81)
Reviewed tree: `origin/main` at `ec615b9`
Reviewer: reviewer (Claude Opus), 2026-08-05
Environment: macOS 26 (darwin 25.6.0), tmux 3.7b, go1.26.5, Claude Code 2.1.222,
codex-cli 0.146.0, amq 0.52.3

Supporting evidence: [`2026-08-05-claim-verification-evidence.md`](2026-08-05-claim-verification-evidence.md)

## What this review is

An adversarial audit of merged `main` that treats every SECURITY.md assertion as a
*claim*: each one is either verified against the code that implements it, or
reported as unsupported. It is not a code-quality pass — [#82](https://github.com/mnbf9rca/agentctl/issues/82)
and `/simplify` cover that.

The same-user exclusion in SECURITY.md stands and is not relitigated. A finding of
the form "a process running as your user could do X" is already accepted. This
review targets accidents, wrong-target delivery, crashes on malformed input, and
gaps between what the document promises and what the code does.

## Method

Three independent passes, deliberately not sharing conclusions:

1. **Direct read.** The whole non-test tree (`cmd/agentctl` plus fourteen `internal`
   packages) read end to end, plus spec §§6.5, 6.6, 7, 8, 12, 13.
2. **Eight-cluster adversarial audit.** One auditor per claim cluster — quoting,
   validation, target chain, payload and kill, metadata trust, concurrency, spec
   conformance, test coverage — each instructed to *refute* its claims. Every
   candidate finding was then put to an independent refuter told to default to
   "refuted" under uncertainty. 100 claims verified with file:line evidence; 34
   candidate findings raised; **27 refuted, 7 surviving**.
3. **Whole-tree `/security-review` lens.** Applied to the merged tree rather than a
   diff, since there is no diff. Returned **no** findings at confidence ≥ 8.

Behavioural claims were then checked against real tmux rather than reasoned about.
Every probe used a throwaway `tmux -L` socket or a PATH shim that rewrote `tmux` to
a throwaway socket, with stub harnesses; no probe touched the live fleet.

**Verification is uneven by design.** Findings F1, F2 and F3 are reproduced against
real tmux with the real binary, and the transcripts are below. F4 is reproduced only
in the shape that a stub harness can produce, and I say so where it matters. F5 is
reproduced with the real binary. The document findings are textual comparisons.

## Verdict summary

The system delivers what SECURITY.md's *security* claims promise. Nothing found
lets caller-supplied text reach a shell or `send-keys`, smuggle a flag into a
harness, deliver a keystroke to a wrong terminal, or destroy a session or window
agentctl does not own. Three independent passes agree on that.

What the review did find is a cluster of **truthfulness** defects: places where
agentctl reports success for work it did not do, and places where SECURITY.md
promises a guarantee the code does not deliver. Under spec §1.1 those are defects in
their own right, and one of them (F1) is a silent fleet divergence of exactly the
kind SECURITY.md has a section devoted to preventing.

| ID | Finding | Severity | Fix in | Issue |
| --- | --- | --- | --- | --- |
| F1 | Relative `--dir` is stored verbatim and re-resolved against the relaunching process's working directory | medium | code | [#123](https://github.com/mnbf9rca/agentctl/issues/123) |
| F2 | Concurrent `relaunch ROLE` creates two windows for one role; both report success; agentctl cannot repair it | medium | code | [#124](https://github.com/mnbf9rca/agentctl/issues/124) |
| F3 | The "window is managed" chain link is vacuous — the format read inherits the session option agentctl itself sets | low | code + doc | [#125](https://github.com/mnbf9rca/agentctl/issues/125) |
| F4 | `processBaseline` accepts the first process that is not literally `amq`, so a transient one can become a permanent baseline | low | code + spec | [#126](https://github.com/mnbf9rca/agentctl/issues/126) |
| F5 | "session already exists" reports exit 3 or exit 6 depending on timing | low | code | [#127](https://github.com/mnbf9rca/agentctl/issues/127) |
| D1–D4 | SECURITY.md amendments: concurrency, fleet-record atomicity, release-checklist claim, residual 1 restatement | low / informational | document | [#128](https://github.com/mnbf9rca/agentctl/issues/128) |

## Findings

### F1 — a relative `--dir` is stored verbatim, so a repaired role can run in a different directory from the rest of the fleet

**Severity: medium. Code fix.** Claim T18/T20 ("Silent fleet divergence through repair").

SECURITY.md says `@agentctl_dir` holds "the exact directory passed to `-c`", that
`relaunch` "uses those values ... so a repaired role runs the harness, model, effort
and directory the fleet was launched with", and that "the working directory is never
defaulted to the invocation directory".

`resolveDirectory` (`internal/fleet/fleet.go:332-346`) stats the supplied path and
returns it **verbatim** at line 345. There is no `path/filepath` import anywhere in
production code. A relative `--dir` is therefore stamped into `@agentctl_dir` as a
relative string (`internal/fleet/fleet.go:284`), read back unchanged
(`internal/fleet/relaunch.go:338`), stated again against the *relaunching* process's
working directory (`relaunch.go:505`), and handed to tmux `-c`
(`internal/tmuxx/tmux.go:168`). Only the omitted-flag path is absolute, because it
goes through `os.Getwd`.

Reproduced with the real binary against a throwaway tmux server:

```
=== launch from $WORK/alpha with a RELATIVE --dir of 'payload' ===
launch exit=0
stored @agentctl_dir = [payload]
pane cwd for each window:
  a: .../dirrepro.Tc27I1/alpha/payload
  b: .../dirrepro.Tc27I1/alpha/payload

=== make role b absent, then relaunch FROM A DIFFERENT CWD ($WORK/beta) ===
agentctl: relaunched b in dirtest: window @2, pane %2, harness codex (stored), model default (stored), effort default (stored), dir payload (stored)
relaunch exit=0
pane cwd for each window after relaunch:
  a: .../dirrepro.Tc27I1/alpha/payload
  b: .../dirrepro.Tc27I1/beta/payload        <-- different directory from the rest of the fleet
```

Two things are wrong, and the second is the worse one:

1. Role `b` was repaired into `beta/payload` while the rest of the fleet lives in
   `alpha/payload`. This is silent fleet divergence — precisely the hazard the
   "Silent fleet divergence through repair" section exists to prevent — and a
   relative stored path *is* resolution against the invocation directory, which is
   what "never defaulted to the invocation directory" was written to forbid.
2. The provenance label says **`dir payload (stored)`**. That is true of the string
   and false of the directory. agentctl told the operator the role was repaired to
   the fleet's recorded directory at the moment it did the opposite. Under §1.1 that
   is the defect, not a cosmetic detail.

When the relative path does not exist in the relaunching cwd, it does fail closed
(exit 3, "records launch directory \"payload\": stat payload: no such file or
directory"), so the damage needs a same-named directory to exist — which, for
`.`, `..`, or a common name like `payload`/`src`/`repo`, is not exotic.

Suggested fix: absolutise the validated `--dir` before stamping `@agentctl_dir`
(`filepath.Abs`, standard library, no new dependency). Optionally refuse a
non-absolute stored value on read so legacy sessions fail closed rather than
diverge.

### F2 — concurrent `relaunch` of the same role creates two windows, both report success, and agentctl cannot repair the result

**Severity: medium. Code fix.** Claim T17.

SECURITY.md: "`relaunch` recreates a role window only when the role matches
**exactly zero** windows ... it never creates a second window beside an existing
one."

`requireAbsentWindow` (`internal/fleet/relaunch.go:432`) lists windows and returns
nil on zero matches; `newWindow` follows at line 251. Nothing holds between them,
and tmux permits duplicate window names (verified on 3.7b).

Reproduced with the real binary:

```
relaunch 1: exit 0 stdout=[agentctl: relaunched b in raceb: window @3, pane %3, harness codex (stored), ...]
relaunch 2: exit 0 stdout=[agentctl: relaunched b in raceb: window @4, pane %4, harness codex (stored), ...]
--- windows after the race ---
window: @1 name=a managed=1 role=a
window: @3 name=b managed=1 role=b
window: @4 name=b managed=1 role=b
--- status after the race ---
raceb    b     codex    default  default                 ambiguous
raceb    b     codex    default  default                 ambiguous
--- clear role b ---
agentctl: refusing to send clear; role b matches 2 windows in raceb (@3, @4)     exit 4
--- relaunch role b again ---
agentctl: refusing to relaunch b; role b already has 2 windows in raceb (@3 ambiguous, @4 ambiguous) exit 4
```

Delivery stays fail-closed: no keystroke goes anywhere, which is the property that
matters most and it holds. But the fleet lands in exactly the ambiguity §13.5 and
the target chain exist to refuse, **agentctl has no command that can undo it** —
`relaunch` refuses, `clear`/`compact` refuse, and nothing removes a window — and two
agent processes are left running, one of them permanently unreachable. Recovery
requires raw tmux.

The §1.1 violation is the load-bearing part: both invocations printed a success line,
and neither is true of the resulting fleet. An orchestrator reading exit 0 believes
the role is healthy.

Suggested fix: make the create atomic with respect to the check, or re-verify after
creating and roll back this invocation's own window (`rollbackWindow` already exists
and kills only the window this invocation created and parsed) when a second window
for the role is observed. The refusal must then be honest about which invocation
lost.

### F3 — the "window is managed" link cannot fail inside a managed session

**Severity: low. Code fix plus a SECURITY.md amendment.** Claim T12.

SECURITY.md enumerates "**window is managed**" as a distinct link in the fail-closed
chain. It is not a link. tmux resolves `#{@agentctl_managed}` in a *window* format by
falling back to the session option, and `stampSession`
(`internal/fleet/fleet.go:268`) sets `@agentctl_managed=1` on the session. So every
window in an agentctl session reports managed, including ones agentctl never created.

Verified directly on tmux 3.7b — window `@1` was created by hand with no window
options at all:

```
=== what does list-windows report for @agentctl_managed on each? ===
window=@0 name=managedrole managed=[1] role=[managedrole] version=[1]
window=@1 name=handmade    managed=[1] role=[]            version=[1]

=== and via show-options -w (the per-window read) ===
window @0: show-options -wqv @agentctl_managed = [1]  @agentctl_role = [managedrole]
window @1: show-options -wqv @agentctl_managed = []   @agentctl_role = []
```

The format read says `1`; the window genuinely has no such option. `windowFormat`
(`internal/tmuxx/tmux.go:15`) uses the format read, so `Window.Managed` is inherited
at all three decision sites: `internal/target/resolver.go:74`,
`internal/status/collector.go:165`, `internal/fleet/relaunch.go:471`.

**Nothing is exploitable today**, and that is worth stating plainly: all three sites
are written `window.Managed != "1" || window.Role != role`, and `@agentctl_role` is
set only at window level, so the *role* half rejects the hand-made window. The chain
still fails closed. The defects are:

1. SECURITY.md claims a link that does no work, overstating the defence in depth by
   one gate. The issue's own framing applies: a threat model that overstates its
   guarantees is itself the defect.
2. The `WindowMetadataError` branch that reports `has @agentctl_managed=%q; expected
   %q` (`internal/target/errors.go:50` and `cmd/agentctl/main.go:695`) is
   **unreachable** inside a managed session. An operator will always get the "stored
   role" branch instead, so a message written for a case can never appear in it.
3. `Window.Version` is parsed into the struct and never read in non-test code — and
   would be inherited in the same way if it were.

Suggested fix: stop depending on inheritance for a window-scoped gate. Either read
the window option explicitly, or make the role-metadata comparison the documented
gate and amend SECURITY.md's enumeration to match what actually rejects. Whichever
is chosen, pin it with a test that creates a hand-made window in a managed session
and asserts the refusal — no current test does.

### F4 — `processBaseline` accepts the first process that is not literally `amq`

**Severity: low. Code fix or spec amendment.** Claim R2, spec §8.

Spec §8 enumerates exactly two retry conditions: `ps` reporting nothing, and an
observed value of literal `amq`. `processBaseline`
(`internal/fleet/fleet.go:306-321`) implements that faithfully — so **the code
conforms to the spec**, and the gap is in §8's own premise that the chain is
`amq coop exec → exec(harness)` with nothing in between. If anything transient
occupies the pane's root process when the poll first looks, that transient becomes
the permanent `@agentctl_process`, every later verification compares against it, and
the role reports `unexpected-process` for good with `clear`/`compact` refusing at
exit 5.

Observed during the concurrency repro: with a stub harness whose shebang is
`#!/usr/bin/env bash`, agentctl stamped `@agentctl_process=/usr/bin/env`, `ps`
reported `sleep` moments later, and status showed `unexpected-process`.

**Stated plainly: that is a stub artifact, not a demonstration against real
`amq coop exec`.** The 2026-08-05 verify-live run passed `clear`, `compact` and
`relaunch` against Claude Code 2.1.222 and codex 0.146.0, so the production exec
chain does settle today. The finding is that the guard is "not `amq`" rather than
"settled", the failure mode is a permanent false refusal, and nothing detects it.

This became more visible with #122: post-launch confirmation now runs §8's
verification step milliseconds after its baseline step, so any instability is
printed by `launch` itself as a factual claim. Recorded on that PR as a non-blocking
note.

### F5 — "session already exists" reports exit 3 or exit 6 depending on timing

**Severity: low. Code fix.** No SECURITY.md claim; this is undocumented concurrency
behaviour.

`Launch` pre-checks `ListSessions` and returns `SessionExistsError` → exit 3
(`internal/fleet/fleet.go:174-187`). Under a race the pre-check passes and tmux
refuses the create instead. tmux's refusal is atomic and correct, so the outcome is
safe, but `launchResult` (`cmd/agentctl/main.go:440`) has no branch for it and falls
through to `ClassifyError` → exit 6.

```
invocation 1: exit 6 stderr=[agentctl: tmux create session: exit status 1: duplicate session: racea]
invocation 2: exit 0
```

The winner's session is intact and fully stamped; the loser damages nothing and
leaves nothing behind. The defect is that one condition reports two exit codes
depending on scheduling, and exit codes are a machine contract (spec §9): an
orchestrator branching on exit 3 to mean "already running" misclassifies the racing
case as a tmux failure.

### D1–D4 — SECURITY.md amendments

Bundled as one issue because they are a single coherent edit to one governing
document. None requires a code change.

- **D1 (low).** The `@agentctl_fleet` rewrite after a flag override
  (`internal/fleet/relaunch.go:263`) is a non-atomic read-modify-write. SECURITY.md's
  "an overridden harness, model or effort is written back ... so the record cannot
  disagree with the live fleet" is stated unconditionally and is not safe under
  concurrent overriding relaunches.
- **D2 (informational).** SECURITY.md is silent on concurrency. Every ownership and
  safety gate in the system is check-then-act with no serialization, and there is no
  locking of any kind. The document should say so, and state the outcomes this
  review established (F2, F5, D1) rather than leaving the reader to assume the
  single-invocation design is enforced.
- **D3 (low).** SECURITY.md's closing line claims "The manual harness re-verification
  checklist runs before each release." `docs/release-checklist.md:3-5` scopes the
  checklist to releases that change tmux targeting, harness startup, or injected
  command delivery, and the promotion template offers a "Checklist not required"
  box. The claim is broader than the process.
- **D4 (informational).** Residual 1's evidence should be restated as version-pinned,
  with its two legs separated — see below.

## Answers to the issue's acceptance criteria

**Every SECURITY.md claim verified or reported.** 100 claims verified against code
with file:line evidence, recorded in the evidence appendix. The claims not verified
are the ones named in F1–F5 and D1–D3.

**Concurrency behaviour documented, whatever it is.** Established by direct
reproduction, not inference:

| Race | Outcome |
| --- | --- |
| Two `launch` on one session name | Fail-closed. tmux refuses the duplicate atomically. Winner intact and fully stamped; loser damages nothing and leaves nothing behind. Exit code is nondeterministic (F5). |
| `launch` mid-stamp vs a concurrent read | Fail-closed. `show-options -qv` returns empty for an unset option, so a half-stamped session reads as unmanaged and every gate refuses. |
| `clear`/`compact` vs `kill` | Fail-safe. tmux never reuses pane IDs (verified: killing `@1`/`%1` yields `@2`/`%2` next), so a stale pane ID cannot address a different pane; `send-keys` to a gone pane fails with "can't find pane". |
| Two `relaunch` of one absent role | **Wrong state (F2).** Two windows, two false successes, unrecoverable by agentctl. |
| Concurrent overriding `relaunch` | **Metadata clobber (D1).** Last writer wins on `@agentctl_fleet`. |
| Locking | None, anywhere. |

The one genuine TOCTOU in the delivery path is bounded and worth recording: the
identity check reads the pane PID and runs `ps`, then delivery targets the pane ID.
Pane IDs are never reused, so the window cannot shift to a *different* pane. It can
shift to a *replaced* process in the same pane — `respawn-pane -k` keeps the pane ID
and gives it a new PID (verified: `%4` before and after, different PID). That is
same-user action and therefore accepted, but it is the precise shape of the residual
and SECURITY.md does not currently state it.

**Residual 1 re-verification.** The issue asked to re-verify against current harness
versions or restate the residual as version-pinned evidence. The evidence is split,
and the honest move is to restate:

- Fixed-injection behaviour — junk cleared, payload executed, conversation reset,
  both harnesses — **was re-verified 2026-08-05** on Claude Code **2.1.222** and
  codex-cli **0.146.0** (`docs/release-verification-notes.md`, verify-live run at
  `bb68e3b`; criteria in `docs/release-checklist.md`).
- The exact-popup-highlight observation and every saturation measurement (18
  workers, timing floors, the `/clearclear` corruption) remain from **2026-08-01** on
  Claude Code **2.1.220** / codex **0.146.0**. `--measure` is gated on
  `internal/tmuxx.payloadDelay` changing and has not been re-run.

codex is unchanged since the measurements; Claude Code has moved two patch versions.
Claiming a blanket re-verification would overstate what was run.

**Regression tests.** Each filed issue carries its own test requirement. The gaps
worth naming: no test pins the "exactly one `shellq` call site" or "`send-keys` only
inside `DeliverPayload`" invariants (both are structural properties nothing enforces),
there is no concurrency coverage at all, and CI does not run `-race`. Those were
raised by the audit and judged informational rather than defects, and are recorded in
the evidence appendix rather than filed.

## What was checked and found sound

Recorded so the ground is not re-covered. Full evidence in the appendix.

- **The quoting layer.** `shellq.Join` has exactly one non-test call site. `Quote` is
  total single-quote escaping; the fuzz target round-trips through a real `sh` and
  asserts both word count and byte equality — I ran it for 45s, 48,837 executions,
  no crashers. Every token reaching it is charset-bound first, on both the launch and
  the relaunch path, including `session.Name` read back from tmux. An auditor
  delivered a payload containing `s'; touch /tmp/PWNED; echo '`, `$(...)`, backticks,
  newlines and invalid UTF-8 through a real tmux window command and confirmed argv
  arrived byte-identical with nothing executed.
- **No shell.** Two `exec.CommandContext` sites, argv arrays, no `sh -c` anywhere in
  production code.
- **Flag smuggling.** Model cannot begin with `-`; effort is allowlist-matched, so
  the codex TOML expression can only receive one of five ASCII words, where Go's `%q`
  and TOML escaping coincide.
- **The target chain.** Present, in the documented order, at
  `internal/target/resolver.go:40-119`, with the one caveat in F3. No early return
  skips a later gate. Delivery targets the resolved pane ID.
- **ID targeting.** All 16 `-t` sites take a typed ID that passed `validateID`. No
  name reaches `-t`. Verified that `%1` does not prefix-match `%10`, and that
  `-t '$0'` targets the session whose *ID* is `$0` even when a decoy session is
  *named* `$0`.
- **The payload registry.** Closed, constant, argument-free; `send-keys` appears at
  three sites, all inside `DeliverPayload`, with `-l --` and Enter as a separate
  event.
- **Destruction gates.** `kill` requires managed+version then kills by session ID;
  launch rollback kills only the created session ID; relaunch rollback kills only the
  window it created and parsed.
- **Metadata as untrusted input.** `decodeFleet` re-validates every field on read. No
  reachable panic was found on malformed tmux or `ps` output — the candidate
  index-out-of-range at `relaunch.go:380` is guarded by `containsRole` plus
  `sameRoles`, and every other index site is length-checked.
- **Identity variables.** `AGENTCTL_MANAGED` and `AGENTCTL_ROLE` are written and
  never read; `AGENTCTL_SESSION` is read only as a session-selection source, exactly
  as residual 5 states.
