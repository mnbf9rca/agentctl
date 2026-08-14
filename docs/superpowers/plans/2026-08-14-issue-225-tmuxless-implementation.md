# Issue #225 Tmux-less Launch and Per-role Attach Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use `superpowers:subagent-driven-development` for the explicitly parallel foundation lanes, then `superpowers:executing-plans` for the serial convergence tasks. Keep the checkbox steps as the execution record.

**Goal:** Make fleet launch detached by default, retain tmux as an explicit presentation choice, and provide a bounded single-viewer per-role attach stream without weakening the closed control protocol or the resident shim's ownership guarantees.

**Architecture:** The existing per-role shim remains the only role owner and the only writer to the harness PTY. A second versioned Unix socket carries a strict attach protocol: the shim continuously drains PTY output, discards it without a viewer, buffers only a bounded admitted-viewer lag window, and serializes admitted viewer input with registered control delivery. Fleet records persist `detached` or `tmux`; detached launch uses a typed `os/exec` boundary and the unchanged hidden-shim argv, while tmux creation remains the sole shell-composition path. The CLI attach client owns a fresh descriptor for its current terminal, exact raw-mode restoration, bounded output completion, and the specified signal behavior.

**Tech stack:** The repository-pinned Go toolchain, Darwin PTYs and kqueue, `golang.org/x/sys/unix` only for the calls approved by §15.9, framed AF_UNIX sockets, injected typed process/tmux boundaries, fake clocks and descriptors for unit tests, throwaway runtime/state roots and tmux sockets for integration tests, and `hack/release-verify.sh` for live release evidence.

## Authoritative inputs

- Issue [#225](https://github.com/mnbf9rca/agentctl/issues/225) is the complete work contract.
- Approved design spec §15.11 is normative for every field, order, timeout, outcome, exit code, cleanup decision, and guard. §§15.1, 15.2, and 15.9 remain binding at the package and external-call boundaries.
- Per `docs/superpowers/plans/README.md`, this plan names files, interfaces, dependency edges, and tests but does not replace the cited contract. Implementers must read the cited spec rows rather than deriving product semantics from this plan.
- `docs/superpowers/plans/2026-08-14-attach-implementation-notes.md` guides implementation mechanics; it does not relax the spec.
- `docs/superpowers/plans/2026-08-14-attach-darwin-io-probes.md` is the evidence source for Darwin-only mechanisms. Tasks cite the applicable probe numbers explicitly.
- `SECURITY.md` governs the current threat model. Its change in the final product PR must be a concise current-truth delta, not a design appendix.
- `docs/release-checklist.md` and `.github/workflows/ci.yml` govern release and CI evidence.
- The approved replacement README input from ruling R46 is branch `design/tmuxless-readme-approved` at signed, pushed commit `a75417f6c2efaec2d7d967694ae9a125d2f8167f`, based on `main` commit `cfb28e8`, with `README.md` SHA-256 `c80cec13ff7d52e409418a424fe1359a3077945d6c5ff22b42278bc3349c1939`; the source, commit, and remote bytes were verified before this pin was issued. Task 5 reads only those immutable bytes and verifies that digest, then deletes the `## Not yet implemented` block because that atomic public PR makes the described UX true. The design-notes file already landed on `main` and is not copied.

## Non-negotiable implementation rules

- Do not edit §15.11 to fit an implementation. A semantic mismatch stops the task and returns to the planner as a design delta.
- Do not modify AMQ.
- Do not widen `shim.Request`, `control.Command`, or the operation registry. Attach frames and viewer bytes use the separate `.attach` socket only.
- Keep the separately specified `AttachLagBufferBytes` and `AttachClientQueueBytes` as distinct named constants with distinct tests; see §15.11.4 and §15.11.7. Consume the approved `AttachLagBufferBytes` value and provenance; do not re-measure it.
- No screen model, replay transcript, daemon, viewer application, detach key, multiple viewers, auto-detected presentation, or old-fleet migration.
- A viewer must never throttle PTY drain. A blocked terminal writer must never delay restoration, signal disposition, terminal reporting, or release of the server's single-viewer claim.
- Production detached spawning receives typed executable, argv, directory, environment, and stdio facts and calls `os/exec` directly. It never accepts a command string and never invokes a shell.
- A role-level transaction gate, not only `ptyx.SerializedWriter`, owns input ordering. It holds one registered delivery across clear, payload, wait, and submit, and admits viewer chunks only between complete transactions; see §15.11.5.
- Preserve the §15.1 hidden argv. Presentation comes from the durable fleet record; it is not added as a hidden flag or inferred from terminal contents.
- Every behavior starts with a focused failing test and records RED before implementation and GREEN after. Exact argv, frame order, cleanup order, output bytes, and exit codes are assertions, not comments.
- Integration and release tests use throwaway roots, tmux sockets, terminals, and stub harnesses. Never target the user's default tmux server or live agents.

## Pull-request and dependency graph

PR labels below are sequencing labels, not assigned GitHub numbers.

| Lane/PR | Scope | Depends on | Exclusive ownership while active |
|---|---|---|---|
| PR 1A | Attach frame codec, `.attach` namespace, cleanup-record vocabulary | current `main` | `internal/shim/attach_protocol*`, `internal/shim/namespace*`, `internal/shim/record*`, `internal/structural/invariants_test.go` |
| PR 1B | Darwin cancellable PTY reader and terminal writer primitives | current `main` | new `internal/ptyx/attach_io_darwin*` and `internal/ptyx/kqueue_darwin*` only |
| PR 2 | Resident attach server, single-viewer arbitration, drain/lag/tail behavior | PR 1A + PR 1B | `internal/shim`, remaining `internal/ptyx` integration files |
| PR 3 | Required fleet presentation persistence and internal selection plumbing | PR 2 | `internal/fleet/shim_record*`, foreground/hidden-shim wiring, record-facing tests |
| PR 4 | Internal detached spawn/launch/relaunch and conditional preflight; current CLI passes tmux explicitly, with no new flags, schema, hints, or docs | PR 3 | `internal/fleet`, `internal/preflight`, minimal compile-safe launch seam in `cmd/agentctl` |
| PR 5 | Atomic public cutover: launch flags/default/schema/hints, per-role attach, run-template role selection, skill, README, SECURITY, and all owning guards | PR 4 | `internal/launchtemplate`, `internal/attach`, public `cmd/agentctl`, embedded schema/skill, integration tests, `README.md`, `SECURITY.md` |
| PR 6 | Release verifier, explicit human checklist legs, draft-note injection/publication gate, and artifact checks | PR 5 | `hack/`, `.github/workflows/release.yml`, release-note source, `docs/release-checklist.md`, release snapshots |
| Release tail | Rebuilt 0.5.0 source promotion PR and fresh operator verification | PR 6 merged and current `main` | `VERSION`, verification evidence, promotion PR and release artifacts |

```text
current main ──┬──> PR 1A ──┐
              └──> PR 1B ──┴──> PR 2 ──> PR 3 ──> PR 4 ──> PR 5 ──> PR 6 ──> 0.5.0 promotion
```

PR 1A and PR 1B are the only approved parallel builder lanes. They are file-disjoint, including specs and docs: neither lane edits docs, module files, or the other's package files. After both merge, use one owner at a time through PR 6. Review can still run in parallel, but reviewers do not edit the implementation worktree.

Each implementation PR starts in a fresh topic-branch worktree from current `main` after invoking `superpowers:using-git-worktrees`. Before batch commits, prove the SSH/1Password signing path as required by `AGENTS.md`. Keep commits focused and signed; fetch/rebase and rerun gates before review; quote the PR's own merge-result CI; obtain the reviewer gate; never merge your own PR; detach the PR worktree after handoff.

## Contract coverage map

| Spec area | Owning tasks |
|---|---|
| §15.2 attach namespace, cleanup vocabulary, and required fleet presentation | Tasks 1A, 2–3 |
| §15.8 launch completion hints and attach/detached output rows | Task 5 |
| §15.11 introduction: constants and scope | Tasks 2, 5 |
| §15.11.1 presentation selection and exclusive drain paths | Tasks 3–5 |
| §15.11.2 single-connection framing, unions, sequencing, and skew | Tasks 1A, 2, 5 |
| §15.11.3 admission, single viewer, terminal decision, and release | Task 2 |
| §15.11.4 non-throttling drain, lag buffer, and tail flush | Task 2 |
| §15.11.5 repaint and transaction-level delivery ordering | Task 2 |
| §15.11.6 detached spawn/readiness/rollback/relaunch | Task 4 |
| §15.11.7 attach terminal, signals, client queue/counts/reporting | Tasks 1B, 5 |
| §15.11.8 viewer behavior across stop, kill, and survivors | Tasks 2, 5 |
| §15.11.9 exact attach/detached outcomes and exits | Task 5 |
| §15.11.10 required guards | Named individually below; each rides in its owning implementation PR |
| §15.11.11 release obligations | Tasks 6–7 |

### §15.11.10 guard ownership

The table owns every bullet in merged §15.11.10: its two boundary/order guards plus all 23 named property guards. Each guard's failing test is captured in the PR that implements the protected behavior, and that PR turns the guard green. Task 6 verifies the merged set but does not introduce a late guard for already-landed behavior.

| Guard name from merged §15.11.10 | Owning task/PR |
|---|---|
| Longest production path is the attach stream | Task 1A / PR 1A |
| Preflight order is exact per mode | Task 4 / PR 4 |
| One connection, one framing | Task 2 / PR 2 |
| Per-direction ordering, and nothing wider | Task 5 / PR 5 |
| The seat is released, not latched | Task 2 / PR 2 |
| A half-open viewer stops receiving | Task 2 / PR 2 |
| Admission bounds application, not the transport | Task 2 / PR 2 |
| The agent is never throttled | Task 2 / PR 2 |
| One terminal decision, split by transport health | Task 2 / PR 2 |
| A departed viewer's input never reaches the harness | Task 2 / PR 2 |
| The tail outcomes are exact | Task 2 / PR 2 |
| Terminal identity and refusals | Task 5 / PR 5 |
| Peer-state isolation | Task 5 / PR 5 |
| Preflight distinguishes configured from observed | Task 5 / PR 5 |
| Startup order by failing prefix | Task 5 / PR 5 |
| Empty eligible set changes nothing outside the candidates | Task 5 / PR 5 |
| Signals produce the promised observation | Task 5 / PR 5 |
| Client is bounded in payload and time | Task 5 / PR 5 |
| Three counts classify separately | Task 5 / PR 5 |
| Broken redirected sink does not erase the outcome | Task 5 / PR 5 |
| Diagnostic routing follows the destination | Task 5 / PR 5 |
| Signal/emission promise is ordering, not completeness | Task 5 / PR 5 |
| Emission shapes | Task 5 / PR 5 |
| Composition renders exactly | Task 5 / PR 5 |
| Byte counters are exact at the boundary | Task 5 / PR 5 |

---

## Task 1A: Strict attach protocol and namespace foundation

**Files:**

- Create: `internal/shim/attach_protocol.go`
- Create: `internal/shim/attach_protocol_test.go`
- Modify: `internal/shim/namespace.go`
- Modify: `internal/shim/namespace_test.go`
- Modify: `internal/shim/record.go`
- Modify: `internal/shim/record_test.go`
- Modify: `internal/structural/invariants_test.go`

**Interfaces:**

- Add `RolePath.Attach string` and validate it as the longest role socket path before claim or mutation per §15.2 and the first §15.11.10 guard.
- Add the closed frame kinds and control-message types from §15.11.2. Keep wire helpers separate from the existing control codec, for example:

```go
type AttachFrameKind uint8

type AttachFrame struct {
    Kind AttachFrameKind
    Data []byte
}

func ReadAttachFrame(io.Reader) (AttachFrame, error)
func WriteAttachFrame(io.Writer, AttachFrame) error
func DecodeAttachControl([]byte) (AttachControl, error)
func EncodeAttachControl(AttachControl) ([]byte, error)
```

- The codec implements §15.11.2's version pre-pass, frame bounds, strict unions, and per-direction sequencing while owning no socket or lifecycle policy.
- Extend cleanup artifact handling with the attach artifact exactly as §15.2 specifies.

- [ ] **Step 1: Add namespace boundary tests and capture RED**

  Assert §15.2's production/override pathname boundary and the matching §15.11.10 longest-path guard, with `RolePath.Attach` as the limiting case and refusal before mutation. Lifecycle cleanup and stale-artifact mutation remain wholly owned by Task 2.

- [ ] **Step 2: Implement the `.attach` namespace and cleanup vocabulary**

  Derive all role paths in one validation pass, preserve descriptor-relative access, and extend strict cleanup-record validation without changing live cleanup or claim behavior in this parallel lane.

- [ ] **Step 3: Add frame and strict-union tests and capture RED**

  Cover every §15.11.2 frame kind and closed control variant, boundary lengths/counters/dimensions, truncated or malformed framing, version precedence, duplicate/unknown/cross-variant fields, trailing data, and both directional sequences.

- [ ] **Step 4: Implement the smallest codec and run GREEN**

  Implement the §15.11.2 framing literally, copy payloads at ownership boundaries, and do not reuse or expand the control `Request` codec.

- [ ] **Step 5: Add structural mutation guards**

  In `internal/structural/invariants_test.go`, capture RED then GREEN for the longest-attach-path guard and for the control/attach separation invariants: no fifth control-request field, no attach bytes in the control registry, no extra attach union field, and no second production attach protocol version. Task 2 owns the runtime one-connection guard.

- [ ] **Step 6: Verify and publish PR 1A**

  Run `go test ./internal/shim -count=1`, `go test ./internal/structural -count=1`, `go vet ./internal/shim ./internal/structural`, then the repository minimum gates. Commit signed, push, wait for the PR's own CI, and obtain reviewer release without merging it yourself.

## Task 1B: Darwin cancellable attach I/O primitives

**Files:**

- Create: `internal/ptyx/kqueue_darwin.go`
- Create: `internal/ptyx/kqueue_darwin_test.go`
- Create: `internal/ptyx/attach_io_darwin.go`
- Create: `internal/ptyx/attach_io_darwin_test.go`
- Create build-safe non-Darwin stubs only if cross-platform compilation requires them

**Interfaces:**

- Provide a context-cancellable, nonblocking descriptor reader for the PTY master and a context-cancellable, nonblocking descriptor writer for an independently opened terminal descriptor.
- Keep kqueue setup behind narrow injected syscall functions so tests can assert registration, wake, retry, EOF/EIO, cancellation, and close order. These primitives transport bytes only; they know nothing about frames, roles, viewers, or outcomes.
- Use the implementation-note mechanisms backed by Darwin probes 1, 2, 3, 5, 6, and 7 for PTY reads, and probes 9 and 10 for terminal writes. These are the only known Darwin-viable mechanisms for the stated cancellation guarantees; cite those probes in the implementation PR.

- [ ] **Step 1: Re-run the applicable probes on the implementation host**

  Record the Darwin version and PASS/FAIL for probes 1–3, 5–7, 9, and 10. A failed premise stops this lane before production code; do not substitute polling or a blocking goroutine.

- [ ] **Step 2: Add nonblocking reader tests and capture RED**

  Assert exact `F_GETFL`/`F_SETFL`, `EVFILT_READ` plus `EVFILT_USER` registration, immediate cancellation wake, EINTR retry, EAGAIN wait, EOF/EIO classification, restoration of only flags this object changed, and idempotent close. Include a continuously readable PTY case proving cancellation is not starved.

- [ ] **Step 3: Implement the PTY reader and run focused GREEN**

  Preserve the original descriptor flags, make the cancellation trigger explicit, and keep ownership/close responsibilities documented in types rather than inferred from finalizers.

- [ ] **Step 4: Add terminal writer tests and capture RED**

  Cover partial writes, EAGAIN, EINTR, kqueue writability, cancellation, peer close, descriptor reuse protection, and a permanently blocked sink. Prove the caller can abandon a blocked writer and proceed to restoration without joining it.

- [ ] **Step 5: Implement the terminal writer and run GREEN**

  Use the fresh terminal descriptor supplied by the attach client; never change inherited stdout's flags. Bound each write through context and expose exact bytes written.

- [ ] **Step 6: Verify and publish PR 1B**

  Run `go test ./internal/ptyx -count=1`, `go vet ./internal/ptyx`, then repository minimum gates. Commit signed, push, wait for its own CI, and obtain reviewer release. Do not touch PR 1A files while this lane is active.

## Task 2: Resident output drain and single-viewer attach server

**Files:**

- Create: `internal/ptyx/resident_relay.go`
- Create: `internal/ptyx/resident_relay_test.go`
- Modify: `internal/ptyx/relay.go` and tests only to share the existing `SerializedWriter`; preserve foreground behavior
- Create: `internal/shim/attach_server.go`
- Create: `internal/shim/attach_server_test.go`
- Modify: `internal/shim/claim_darwin.go`
- Modify: `internal/shim/claim_darwin_test.go`
- Modify: `internal/shim/lifecycle.go`
- Modify: `internal/shim/lifecycle_test.go`
- Modify: `internal/shim/server.go`
- Modify: `internal/shim/server_test.go`

**Interfaces:**

- Extend `shim.RunRequest` with a closed `OperatorMode` selected by trusted caller wiring, not caller text. Define the modes required by §15.11.1 without exposing them through the hidden argv:

```go
type OperatorMode uint8
const (
    OperatorForeground OperatorMode = iota
    OperatorTmux
    OperatorDetached
)
```

  Implement each mode's endpoint and listener behavior per §15.11.1.
- `residentRelay` owns the low-level `ptyx.SerializedWriter`. `internal/shim` adds a role-level transaction gate above it:

```go
type roleInputWriter interface {
    WriteViewer(context.Context, []byte) (int, error)
    BeginDelivery(context.Context) (operationWriter, func(), error)
}
```

  `BeginDelivery` acquires the same gate used by `WriteViewer`; its returned release function is held across the complete registry transaction in `operationExecutor.Deliver`, including its wait. A viewer chunk holds the gate for that whole chunk. This makes §15.11.5 indivisibility transaction-level rather than per-write.
- `attachServer` owns listener admission, same-uid peer verification, single-viewer state, bounded lag, final-decision arbitration, and frame emission. It receives typed callbacks for window size and serialized PTY writing; it does not own the child or control listener.

- [ ] **Step 1: Add resident relay tests and capture RED**

  Prove §15.11.4's no-viewer, stalled-viewer, approved lag-bound, overflow, fixed-sink, cancellation, and PTY-closure behaviors. Add §15.11.5 transaction-gate tests that block after every delivery write and prove viewer chunks appear only before or after the whole transaction.

- [ ] **Step 2: Implement the bounded drain path**

  Use the Task 1B PTY primitive and a bounded queue owned by the relay. Do not add scrollback or replay. Consume the already-approved `AttachLagBufferBytes` value and provenance from §15.11.4; do not re-measure or derive it.

- [ ] **Step 3: Add listener/admission tests and capture RED**

  Cover all §15.11.3 listener, topology, kernel peer, identity, initial-size, single-viewer, pre-admission failure, disconnect, half-close, peer-death, and prompt-release cases. Do not use advisory identity as authorization.

- [ ] **Step 4: Implement admission and exactly-once release**

  Keep the viewer claim behind one owner and one terminal-decision function. Enforce §15.11.3's commit-point and release properties and §15.11.2's pre/post-admission protocol split.

- [ ] **Step 5: Add active-runtime and tail tests and capture RED**

  Cover §15.11.4's drain/tail matrix, §15.11.5's repaint and indivisible delivery, and §15.11.8's stop/kill/survivor behavior. Cover §15.11.3's single terminal decision and Task 2's owned stale-attach removal, claim release, cleanup vocabulary, artifact observation, and cleanup order.

- [ ] **Step 6: Integrate with `Server.Run` and run GREEN**

  Start both control and attach listeners before detached readiness can be reported; keep the existing child watcher authoritative. Treat attach connection failures as viewer outcomes unless the spec marks a runtime invariant fatal. Do not let a client decide child liveness or cleanup.

- [ ] **Step 7: Add source and race guards**

  Capture RED then GREEN for Task 2's named §15.11.10 guards: one connection/one framing; seat released/not latched; half-open viewer stops receiving; admission bounds application; agent never throttled; terminal decision split by transport health; departed input never reaches the harness; and exact tail outcomes. Also pin the transaction gate above one serialized child writer, zero attach use of control `Request`, bounded viewer storage, no detach-key parser, one admitted viewer, one consumed `AttachLagBufferBytes`, listener readiness only in detached mode, and attach-artifact claim/cleanup ownership. Run the race suite over repeated attach/release/stop interleavings.

- [ ] **Step 8: Verify and publish PR 2**

  Run focused shim/ptyx tests, `go test -race ./internal/shim ./internal/ptyx -count=1`, minimum repository gates, and the applicable throwaway socket tests. Commit signed, rebase on both merged foundation PRs, push, wait for merge-result CI, and request reviewer gate.

## Task 3: Persist required presentation and select hidden-shim mode

**Files:**

- Modify: `internal/fleet/shim_record.go`
- Modify: `internal/fleet/shim_record_test.go`
- Modify: `internal/fleet/foreground.go`
- Modify: `internal/fleet/foreground_test.go`
- Modify: `internal/fleet/shim.go`
- Modify: `internal/fleet/shim_test.go`
- Modify: `internal/fleet/shim_relaunch_test.go`
- Modify: `internal/fleet/shim_status_test.go`
- Modify: `internal/kill/shim_executor_test.go`
- Modify: `cmd/agentctl/shim_command.go`
- Modify: `cmd/agentctl/shim_command_test.go`
- Modify: `cmd/agentctl/runtime_dependencies.go`
- Modify: `cmd/agentctl/integration_shim_lifecycle_test.go`
- Modify: `hack/fixtures/shim-version/main.go`

**Interfaces:**

- Add the closed `fleet.Presentation` type required by §15.2 and §15.11.1.
- Add required `Presentation` to `ShimFleetRecord` between `Directory` and `Roster`; change construction to `NewShimFleetRecord(session, directory, presentation, fleet)`.
- Implement §15.2's flag-day schema, strict decoding, and writer order without a compatibility dialect.
- The hidden command reads its session fleet record before constructing `shim.RunRequest` and selects the matching trusted operator mode. Foreground `run` explicitly selects its mode; follow §15.2 for record creation and existing-record behavior.

- [ ] **Step 1: Add strict record tests and capture RED**

  Cover the full §15.2 presentation domain, malformed variants, version precedence, byte encoding, mutation preservation, size bound, flag-day refusal, and no-adoption rule.

- [ ] **Step 2: Implement the required field and migrate every constructor fixture**

  Keep this PR's public launch behavior unchanged while populating the new required record field. Implement foreground creation and existing-fleet behavior exactly as §15.2 specifies.

- [ ] **Step 3: Add hidden-command mode-selection tests and capture RED**

  Assert durable-record read precedes terminal mutation and server start; tmux mode supplies current endpoints; detached mode supplies none and enables attach; missing/invalid/disagreeing records refuse with no role mutation. The hidden argv and public command inventory remain byte-for-byte unchanged.

- [ ] **Step 4: Wire trusted mode selection and run GREEN**

  Inject a narrow fleet-record reader into the hidden-command test seam. Do not add `--presentation`, environment authority, terminal detection, or a shim-to-fleet import cycle.

- [ ] **Step 5: Verify and publish PR 3**

  Run `go test ./internal/fleet ./cmd/agentctl -run 'Test.*(FleetRecord|HiddenShim|Foreground)' -count=1`, then minimum gates, race, integration record fixtures, and reviewer gate.

## Task 4: Detached spawn, presentation-aware launch, and relaunch

**Files:**

- Create: `internal/fleet/detached_spawn.go`
- Create: `internal/fleet/detached_spawn_test.go`
- Modify: `internal/fleet/shim.go`
- Modify: `internal/fleet/shim_test.go`
- Modify: `internal/fleet/shim_relaunch.go`
- Modify: `internal/fleet/shim_relaunch_test.go`
- Modify: `internal/preflight/preflight.go`
- Modify: `internal/preflight/preflight_test.go`
- Modify: `internal/preflight/shim.go`
- Modify: `internal/preflight/shim_test.go`
- Modify: the `shimFleetLauncher` signature, production wiring, and unchanged-behavior fixtures in `cmd/agentctl/main.go`, `runtime_dependencies.go`, and `main_launch_test.go`

**Interfaces:**

- Add a typed detached process seam:

```go
type DetachedShimRequest struct {
    Executable string
    Argv       []string
    Directory  string
    Environment []string
    Stdin      *os.File
    Stdout     *os.File
    Stderr     *os.File
}

type DetachedShimProcess interface {
    PID() int
    Wait() <-chan error
}

type DetachedShimStarter interface {
    Start(DetachedShimRequest) (DetachedShimProcess, error)
}
```

  The launcher opens and owns the typed stdio handles before calling `Start`; the production starter passes those handles unchanged to `exec.Cmd`, and the launcher closes its copies on every result path. Production implements the rest of §15.11.6's `os/exec` contract and immediately owns its asynchronous waiter. The recording fake asserts every typed request, handle, ownership transition, and waiter event.
- `ShimLauncher` receives a selected `fleet.Presentation`. Tmux uses the existing creation path; detached starts roles in roster order through `DetachedShimStarter` and compares readiness `ShimPID` with the created PID.
- Preserve the acyclic dependency direction: `internal/fleet` imports `internal/preflight`; `internal/preflight` does not import `internal/fleet`. Extend `preflight.CheckShimExecutables` with `requireTmux bool`, and have fleet derive it from `fleet.Presentation`.
- This PR exposes no new flag, template field, completion hint, README text, skill prose, or default. Its minimal CLI seam passes `fleet.PresentationTmux` explicitly so the changed launcher signature compiles while byte-for-byte public behavior stays unchanged until Task 5's atomic cutover.

- [ ] **Step 1: Add detached spawn boundary tests and capture RED**

  Assert every typed spawn field and syscall/process property in §15.11.6, no shell, one start, one asynchronous waiter, all pre-start and immediate-exit failures, readiness/exit races, cancellation, and no zombie-producing path.

- [ ] **Step 2: Implement production spawn and waiter authority**

  Follow §15.11.6 exactly. The waiter, not `kill(pid,0)`, is authoritative during the readiness window. Never derive ownership from a PID after a failed `Start`.

- [ ] **Step 3: Add presentation-aware launch/rollback tests and capture RED**

  Cover the complete §15.11.6 internal creation, readiness, ownership-agreement, rollback, retention, uncertainty, and cleanup matrix for both presentations. Preserve §6.5 tmux metadata order and the single `shimWindowCommand` site. Public completion rows remain Task 5-owned.

- [ ] **Step 4: Implement the two launch branches and run focused GREEN**

  Share directory resolution, record creation, readiness observation, and outcome rendering. Branch only at process/presentation creation and owned presentation cleanup.

- [ ] **Step 5: Add relaunch tests and capture RED**

  Prove relaunch reads stored presentation, detached relaunch never consults tmux, tmux relaunch keeps exact-ID cleanup, overrides preserve presentation, stale-record gates remain unchanged, and readiness/commit uncertainty retain exactly the artifacts stated in §15.11.6.

- [ ] **Step 6: Implement presentation-preserving relaunch**

  Do not infer presentation from whether tmux exists. Keep the stored field unchanged through every `ReplaceOwned`/`ExtendOwned` path.

- [ ] **Step 7: Add the preflight-order guard and capture RED/GREEN**

  In `internal/preflight` and `internal/fleet` tests, assert the exact per-mode ordering named by §15.11.10 before any spawn/presentation call, then implement the preflight-local boolean/options seam without importing fleet. Add source guards proving detached spawn cannot call a shell or tmux and that `shimWindowCommand` remains the sole shell-composition site.

- [ ] **Step 8: Verify and publish PR 4**

  Run fleet/preflight unit tests, structural guards, race, and internal detached/tmux tests through recording fakes. Verify public CLI/help/schema/skill/README bytes remain unchanged. Run minimum gates, rebase, repeat, and request reviewer gate.

## Task 5: Atomic public cutover and terminal-safe per-role attach

**Files:**

- Modify: `internal/launchtemplate/template.go`
- Modify: `internal/launchtemplate/template_test.go` and fixtures
- Modify: `skills/agentctl/references/fleet-template.schema.json`
- Modify: `skills/agentctl/SKILL.md`
- Modify: relevant skill/schema contract tests under `cmd/agentctl/` and `skills/`
- Modify: launch parsing/wiring/output/tests in `cmd/agentctl/main.go`, `launch_template.go`, `runtime_dependencies.go`, and `main_launch*_test.go`
- Modify: `internal/attach/executor.go` and `internal/attach/executor_test.go` only to keep the tmux executor as the fleet-level delegate; put role-stream behavior in the new client files below
- Create: `internal/attach/role_client.go`
- Create: `internal/attach/role_client_test.go`
- Create: `internal/attach/terminal_darwin.go`
- Create: `internal/attach/terminal_darwin_test.go`
- Modify attach interfaces, parsing, dispatch, result rendering, and tests in `cmd/agentctl/main.go`, `runtime_dependencies.go`, `main_attach_test.go`, and `shim_results.go`
- Modify run parsing/merge/tests in `cmd/agentctl/main.go`, `launch_template.go`, `main_run_test.go`, and related files
- Modify/add the public behavior's integration and structural tests under `cmd/agentctl/`, `internal/structural/`, and relevant packages
- Modify: `README.md` from the immutable R46 input named under Authoritative inputs
- Modify: `SECURITY.md`

**Interfaces:**

- Launch parsing, template decoding, defaults, schema validation, help, completion rows, docs, and skill prose switch atomically in this PR per §15.11.1 and §15.8. No preceding PR advertises or defaults to a route this PR has not implemented.
- Bare/role attach routing implements §15.11.1 from the durable record and preserves the existing tmux executor only as the fleet-level delegate.
- Keep terminal ownership concrete and small rather than imposing a broad session interface. `internal/attach/terminal_darwin.go` returns one private `relayTerminal` whose relay endpoints use the Task 1B types; tests inject the syscall/factory functions at the same narrow boundaries already used by `internal/ptyx`:

```go
type relayTerminal struct {
    input      ptyx.ContextReader
    output     ptyx.ContextWriter
    diagnostic DiagnosticSink
}

type DiagnosticSink interface {
    Attempt(context.Context, []byte) (int, error)
}
```

  The concrete owner retains identity/raw/restoration state and exposes private restore/close operations to `roleClient`. `DiagnosticSink` stays separate because §15.11.7 gives the proved terminal destination and a redirected destination different blocking/signal semantics. Do not add interface methods until a test seam consumes them.
- The named-role template form of `run` produces one role configuration and implements §15.1's field-selection boundary.

- [ ] **Step 1: Add atomic public-launch/schema/hint/skill tests and capture RED**

  Cover every §15.11.1 flag/template/default/override/refusal case, strict schema handling, provenance, validation-before-mutation, help, and shipped skill statements. Assert byte-for-byte both two-line §15.8 templates `launch-complete-detached` and `launch-complete-tmux`, including their second-line attach hints. Add the corresponding integration and skill-contract guards in this same RED.

- [ ] **Step 2: Implement the atomic public launch cutover and hints**

  Pass one typed presentation through parsing, template merge, record creation, conditional preflight, and internal Task 4 launch. Update the embedded schema, CLI/default/help, exact §15.8 row selection, and skill prose together; do not expose a partial state in a separate commit or PR.

- [ ] **Step 3: Add attach form and durable-routing tests and capture RED**

  Cover every §15.11.1 bare/role attach route and refusal, durable-record authority, roster handling, invalid/old/root-disagreement records, and absence of tmux calls on detached paths. Assert §15.11.9's exact output and exit rows.

- [ ] **Step 4: Implement routing without widening session resolution**

  Resolve the selected session as today, then use the durable presentation/roster as authority. Keep tmux session IDs presentation-only and keep `.attach` identity exact.

- [ ] **Step 5: Add terminal startup-order and identity guards; capture RED**

  Assert §15.11.7's startup ordering and every failure boundary. In the same RED, add the §15.11.10 terminal-identity, peer-state-isolation, configured-versus-observed preflight, and startup-prefix guards; peer-state isolation is observed from the parent process in both inherited flag states.

- [ ] **Step 6: Implement fresh-terminal ownership using proven mechanisms**

  Use implementation-note probes 11, 12, and 13 for fresh descriptor and targeted flag restoration. Use probe 15 for `F_DUPFD_CLOEXEC` floor 3 diagnostics. These are the only known Darwin-viable mechanisms for the spec's isolation guarantees; cite the probe log in the PR.

- [ ] **Step 7: Add signal/restoration guards and capture RED**

  Cover §15.11.7's complete signal set, inherited disposition/mask states, eligibility boundaries, exact-once restoration, re-raise/restore-failure rows, and canonical naming. Add the §15.11.10 empty-eligible-set, signal-observation, and signal/emission-order guards. Follow probes 14, 16, and 17 plus their narrower implementation-note conclusions.

- [ ] **Step 8: Implement the signal owner and bounded teardown**

  The owner proceeds after cancellation without joining a stuck writer. Preserve prior dispositions and masks exactly and use `signal.Stop` with its documented refcount behavior. **When the observed eligible subset is empty, do not call `signal.Notify` at all**; an empty-argument call would subscribe to every incoming signal and violates §15.11.7.

- [ ] **Step 9: Add client protocol/queue/output guards and capture RED**

  Cover §15.11.2 and §15.11.7's client sequencing, verbatim input, queue accounting, partial writes, peer closure, final dispositions/counters, bounded reporting, blocked sinks, SIGPIPE avoidance, diagnostic bytes, and every §15.11.9 composition row. Capture RED for the remaining Task 5 guards: per-direction ordering with no cross-direction assertion; bounded client; three counts; broken redirected sink; destination-based diagnostic routing; emission shapes; exact composition; and exact boundary counters.

- [ ] **Step 10: Implement the role client and exact output mapping**

  Use Task 1B's terminal writer. Count bytes only at the specified observation points, never infer delivery from queueing, EOF, or child state. Bound final report attempts independently of terminal restoration.

- [ ] **Step 11: Add named-role `run` template tests and capture RED**

  Cover the exact two run forms, exclusions with `--harness/--model/--effort`, absent and duplicate named role, ignored template `presentation`/`dir`, unchanged current cwd, validation before mutation, and unchanged flag-form behavior.

- [ ] **Step 12: Implement named-role template selection**

  Reuse the strict decoder but do not reuse launch presentation/directory merge. Return one validated `config.RoleConfig` and preserve the current foreground lifecycle.

- [ ] **Step 13: Add the complete public integration matrix and run GREEN**

  In throwaway roots/PTYs/tmux sockets with stub harnesses, exercise the §15.11.1 public launch matrix, both exact §15.8 hint rows, multiple detached roles, bare/role attach, second-viewer refusal, verbatim input, repaint, disconnect/re-attach, transaction-level control coexistence, stored-presentation relaunch, stop/kill/survivors, tail outcomes, signals/restoration, and complete cleanup. Add adversarial foreign/absent versions, wrong identity, malformed frames, slow/blocking destinations, and signal races. This is the owning implementation PR's integration RED/GREEN, not deferred to Task 6.

- [ ] **Step 14: Install immutable README, skill prose, and concise SECURITY truth**

  Verify the pinned commit and README SHA-256 from Authoritative inputs, copy that file's bytes, delete its `## Not yet implemented` block, and prove all five items—including §15.8 completion hints—are now implemented. Update `skills/agentctl/SKILL.md` and its contract fixtures in this same atomic PR so it no longer states tmux-only attach. Add only the concise SECURITY sentences required by §15.11; do not add a constraints table or design rationale.

- [ ] **Step 15: Verify and publish PR 5**

  Run focused launch/attach/run tests, `go test -race ./internal/attach ./internal/shim ./cmd/agentctl -count=1`, every named Task 5 guard, structural/doc/schema/skill contract tests, and the complete throwaway integration matrix. Confirm the preceding commit on `main` still exposes no new public behavior and this PR exposes all of it together. Run minimum gates, rebase, repeat, push, wait for the PR's own CI, and request reviewer gate.

## Task 6: Release verification and publication gates

**Files:**

- Modify: `hack/release-verify.sh`
- Modify: `hack/releaseverify_test.go`
- Modify deterministic helper fixtures under `hack/fixtures/` only where required
- Create: `docs/releases/0.5.0.md`
- Create: `hack/release-notes.sh`
- Create: `hack/releasenotes_test.go`
- Modify: `.github/workflows/release.yml`
- Create: `hack/releaseworkflow_test.go`
- Modify: `docs/release-checklist.md`
- Modify: `hack/releasechecklist_test.go`
- Modify: `.github/PULL_REQUEST_TEMPLATE/release-promotion.md`
- Modify: `hack/check-promotion-form.sh`
- Modify: `hack/checkpromotionform_test.go`

- [ ] **Step 1: Add draft-note injection/verification tests and capture RED**

  Add `hack/releasenotes_test.go` fixtures for idempotent injection, missing/duplicate/altered obligation blocks, wrong release version, malformed GitHub release JSON, and a release whose `isDraft` fact is false. Pin `docs/releases/0.5.0.md` as the sole source of the two distinct §15.11.11 statements. Add `hack/releaseworkflow_test.go` source-order assertions that fail until injection and re-fetch verification occur before every reachable undraft command.

- [ ] **Step 2: Implement the publication-blocking workflow step and run GREEN**

  Implement `hack/release-notes.sh` with explicit `inject` and `verify` modes. In `.github/workflows/release.yml`, immediately after goreleaser uploads the draft and before smoke tests, attestations, or `gh release edit --draft=false`: fetch the draft's body plus `isDraft`; refuse unless it is still draft; inject the versioned source block; update the draft; fetch it again; and verify it is still draft and contains the byte-exact source block exactly once. Any failure exits nonzero, so the existing undraft step is unreachable. Dry-run remains non-publishing and exercises the script through its tests.

- [ ] **Step 3: Extend the release verifier and capture RED**

  Add deterministic detached launch/attach legs while retaining every existing shim-version leg. Where the public guard intentionally refuses a foreign or absent peer before a public attach can proceed, add detached helper legs at the exact protocol boundary rather than weakening the guard. Record which observation supplied each version value and preserve current/current controls.

- [ ] **Step 4: Implement verifier legs and transcript assertions**

  The verifier must prove detached launch without tmux, explicit tmux with the private socket, one-viewer arbitration, repaint, control coexistence, clean detach/re-attach, child exit/tail rows, role/fleet cleanup, and no effect on sentinel resources. Keep all waits bounded and cleanup traps exact.

- [ ] **Step 5: Add explicit pre-promotion human and artifact checks**

  Extend `docs/release-checklist.md` with separately recorded human legs for: detached launch in an ordinary terminal; per-role attach plus repaint/verbatim input and clean disconnect/re-attach; SIGWINCH resize observation; and each specified handled/ignored/blocked signal plus exact terminal restoration. Add the §15.9 `go version -m` and archive-license evidence for the built and extracted Darwin binaries, and pin the checklist fields in `hack/releasechecklist_test.go`. Update the promotion template and `hack/check-promotion-form.sh` so a checklist-required promotion must name the committed evidence location and affirm the detached/attach/signal legs passed; add fail/pass fixtures in `hack/checkpromotionform_test.go`. These observations run on the rebuilt candidate and are committed to `docs/release-verification-notes.md` on `main` before the promotion PR can be merged into `release`.

- [ ] **Step 6: Run every local gate and publish PR 6**

  Run the live CI definition, every already-landed §15.11.10 guard, `go test ./...`, `go vet ./...`, race, `shellcheck hack/*.sh`, `golangci-lint run`, `go test -tags integration ./...`, release-note workflow/script fixtures, release snapshots, archive/license checks, and `hack/release-verify.sh` with fresh throwaway resources. Task 6 may verify owning guards but must not first introduce one for Task 1–5 behavior. Fetch and rebase current main, repeat all gates, push signed commits, wait for the PR's own merge-result CI, request reviewer gate, and do not merge your own PR.

## Task 7: Rebuild and verify the 0.5.0 release promotion

**Prerequisite:** PR 6 and every prior product PR are merged; `main` is current and all source gates pass.

- [ ] **Step 1: Start a fresh promotion worktree from current `main`**

  Reuse the mechanical lessons from parked promotion commit `aed15be`, but do not rebase or promote its stale artifacts. Create the release branch from the final feature commit, apply the issue's release version, regenerate required snapshots/artifacts with the repository-pinned toolchain, and inspect every generated diff.

- [ ] **Step 2: Write the two mandatory release-note obligations**

  Verify the promotion version selects `docs/releases/0.5.0.md`, and run `hack/release-notes.sh` in verification mode against a fixture of the draft body. The workflow, not a post-publication edit, performs the real injection and re-fetch verification while the release is still draft.

- [ ] **Step 3: Run a fresh full operator verification**

  A release verifier/operator other than the promotion author reruns `hack/release-verify.sh` from the rebuilt branch, performs every explicit checklist terminal/attach/repaint/resize/signal/restoration leg, records `go version -m` for build and extracted artifacts, verifies upstream license inclusion, and captures commands, hashes, host/tool versions, exit statuses, and cleanup results. Commit that evidence to `main` before the promotion PR is eligible to merge and trigger the release-branch push.

- [ ] **Step 4: Publish promotion through the normal reviewed path**

  Commit the source promotion changes (`VERSION` plus fresh verification evidence), push them, and open the promotion PR with the named pre-trigger human evidence. Wait for its own CI and form check, obtain reviewer release, and leave merge/publish to the planner or maintainer. After merge triggers `.github/workflows/release.yml`, confirm the draft-note injection/verification step passed before the undraft step. Do not reuse CI or artifacts from the parked branch.

## Completion checklist

- [ ] Every PR maps its changes to the coverage table and cites the exact probe evidence used.
- [ ] All red/green evidence is present in PR descriptions or standalone reviewer artifacts.
- [ ] No control request/registry widening, shell call, unbounded buffer, detach parser, extra viewer, or inferred outcome entered production.
- [ ] README describes only shipped behavior; SECURITY describes current truth concisely; release notes carry both upgrade obligations.
- [ ] Final main is rebased and fully green under local gates, PR CI, integration, live verifier, artifact inspection, and independent reviewer gate.
