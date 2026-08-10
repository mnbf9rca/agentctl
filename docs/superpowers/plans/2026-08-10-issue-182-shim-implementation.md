# Issue #182 Per-Agent PTY Shim Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace tmux-window identity and keystroke delivery with a layout-independent per-agent PTY shim for release 0.5.0, while shipping the issue's interim diagnostic and recovery guidance.

**Architecture:** Each role is owned by a resident shim that holds a kernel-arbitrated role claim, runs the unchanged harness command on a nested PTY, and serves a versioned local control socket. Runtime observations become authoritative for role identity, liveness, and control; tmux remains an optional presentation and fleet-launch mechanism, never an identity or delivery dependency. The CLI sends only closed-registry operation names, and the shim is the sole writer to the harness PTY.

**Tech Stack:** Go 1.26, Go standard library plus a reviewed `golang.org/x/sys/unix` dependency for Darwin PTY and lock syscalls, AF_UNIX sockets, fake `tmuxx.Runner` and shim-boundary fakes, throwaway tmux sockets, stub harnesses, and version-pinned live harness probes.

## Global Constraints

- Issue [#182](https://github.com/mnbf9rca/agentctl/issues/182), including its `AMENDED 2026-08-09` section, is the implementation contract; it adopts Option S and the 0.5.0 milestone.
- The options paper at `docs/superpowers/specs/2026-08-09-issue-182-identity-delivery-options.md` supplies rationale and evidence only. Task 1 amends the approved design spec, and those amendments supersede the paper before shim implementation starts.
- All ten constraints in `SECURITY.md` under “Binding constraints for the issue-182 per-agent shim” bind every PR. The coverage matrix below is a required review checklist.
- Preserve the factual-output and prior-state rules in approved-design §§1.1–1.2. A socket response, PTY write, signal, or process probe may be reported only at the granularity actually observed.
- Keep the wire protocol closed, argument-free, and version-gated. No caller text, payload string, slash command, raw key, model value, or environment value may become PTY input.
- Production external commands remain behind interfaces. tmux and `ps` calls use `internal/tmuxx.Runner`; the PTY child launcher has its own narrow injected interface and receives only argv produced by `internal/harness`.
- The only shell-interpreted string remains the tmux window command assembled in `internal/fleet` from validated argv through `internal/shellq`. The no-tmux path invokes argv directly.
- No migration or dual-dialect compatibility is implemented. The tmux-metadata-to-shim transition is a flag day; protocol version skew still refuses before parsing or acting.
- Every behavior change uses red/green TDD. Unit tests assert exact executable/argv elements and operation order; integration tests use throwaway tmux sockets and stub harnesses only.
- Plans are non-normative. Exact messages, state precedence, exit meanings, validation caps, protocol constants, ownership order, and argv shapes belong in the approved spec, not here; implementation tasks cite the amended sections.

---

## Binding Planner Decisions — R1–R8

The planner arbitrated both review reports in `/Users/rob/git/agentctl/.worktrees/issue-182-planner-rulings-2026-08-10.md`. These outcomes replace the former proposals and are prerequisites to the implementation graph below.

1. **Claim and answerer identity.** `flock(LOCK_EX)` remains the crash-released role arbiter. The lockfile body records the shim PID, nonce, and metadata as advisory facts; it is never described as kernel proof. The connected client obtains the answerer PID from Darwin `LOCAL_PEERPID` and compares that kernel fact with the advisory record. POSIX record locks are inadmissible because closing an unrelated descriptor can silently release them.
2. **Self-target guard.** Build one `ps -eo pid=,ppid=` snapshot, take the target shim PID from `LOCAL_PEERPID`, and walk from the caller's own PID through that snapshot looking for the target. Broken ancestry, malformed output, a loop, or a process-inspection failure refuses the operation; `TMUX_PANE` and advisory identity variables remain ineligible.
3. **Orphan safety.** Write a durable `child-starting` reservation carrying shim PID and nonce before fork. After `os/exec` reports a successful child start, atomically upgrade it with child PID and a PID-reuse token. A dead shim plus `child-starting` is indeterminate and refuses; a recorded child whose PID and start token still match is orphaned and blocks relaunch. SIGHUP evidence informs teardown but never substitutes for observing child absence.
4. **Two artifacts, two homes.** Socket and lockfile live in the descriptor-verified `0700` volatile tree `/tmp/agentctl-<decimal-uid>/v1`. The child/config record lives in the descriptor-verified durable tree returned by `os.UserConfigDir()` plus `agentctl/state-v1`, and survives tmp sweeps and reboot. The spec names and validates `AGENTCTL_RUNTIME_ROOT` and `AGENTCTL_STATE_ROOT` as test/release-verification overrides; both are absolute, capped, fail closed on unsafe descriptors, and documented as same-user-selectable residuals rather than trust anchors.
5. **Runtime path bound.** Validate the fully resolved socket path at runtime against Darwin's `sun_path` ceiling and refuse before claim or mutation when it cannot fit. `internal/config` caps derive from the approved worst-case template, and tests cover both production roots and declared overrides.
6. **Status and viewing.** Runtime records are authoritative for `status` with or without tmux. `attach` stays tmux-only and gives the approved factual no-presentation message. A foreground per-role `agentctl run` path supplies tmux-less operation.
7. **Compile-safe cutover.** PRs 5 and 6 add shim-backed implementations beside the current production paths and keep compatibility adapters compiling. PR 7 alone rewires `cmd/agentctl`, removes `internal/target` and tmux delivery, and performs the flag-day transition atomically. PR 5 owns the shared integration fixture before PR 6 consumes it. No PR 5/PR 6 parallel edge remains.
8. **Fallback and live skew evidence.** After PRs 2 and 3, an explicit Option S viability gate tests the Darwin claim/socket and PTY/readiness contracts. Any failure that cannot be fixed inside the approved surface stops PR 4 and returns the planner to pane-scoped Option A in paper §3. The release verifier builds a deterministic foreign-version socket peer that exercises both mismatch directions against the real current binary.

## Pull Request and Dependency Graph

Each PR gets its own focused signed commits, reviewer gate, and fresh pull-request CI. PR numbers below are sequencing labels, not GitHub numbers.

| PR | Scope | Depends on | Parallel lane |
|---|---|---|---|
| PR 1 | Probes, tracked replay-evidence consumption, superseding spec/security amendments, and interim measures | Planner rulings R1–R8 published into issue #182 | Builder 1; Builder 2 reviews probe/spec evidence |
| PR 2 | Runtime namespace, role claim, durable record, and protocol codec | PR 1 | Builder 1, parallel with PR 3 |
| PR 3 | Nested PTY, child lifecycle, relay, resize, and terminal-state observation | PR 1 | Builder 2, parallel with PR 2 |
| Gate S | Option S viability decision; choose continued shim work or the paper-§3 pane fallback | PR 2 + PR 3 | Planner decision with both builders' empirical evidence |
| PR 4 | Resident shim server, hidden shim entrypoint, readiness, and closed operation delivery | Gate S continues Option S | integration point; one builder implements, the other adversarially reviews |
| PR 5 | Shim-backed fleet/kill implementations plus the shared integration fixture, kept behind compiling compatibility seams | PR 4 | Builder 1; serial predecessor of PR 6 |
| PR 6 | Shim-backed control/status implementations, kept beside the current production path | PR 5 | Builder 2; consumes PR 5's fixture |
| PR 7 | Atomic CLI cutover, target/send-keys retirement, no-tmux foreground path, attach behavior, and operator docs | PR 6 | convergence PR; one owner for wiring and deletion |
| PR 8 | Release verification, embedded skill, release notes, and complete 0.5.0 gate evidence | PR 7 | final release-scoped verification |

Dependency edges:

```text
R1–R8 in issue #182 ──> PR 1 ──┬──> PR 2 ──┐
                               └──> PR 3 ──┴──> Gate S ──> PR 4 ──> PR 5 ──> PR 6 ──> PR 7 ──> PR 8
                                                  └──────> Option A decision when Option S is invalidated
```

## Spec Amendments That Supersede the Options Paper

PR 1 carries every semantic design delta below and lands before shim code. Later PRs may add only implementation-descriptive evidence, exact argv, and exact output fixtures for behavior already approved in PR 1; any new surface or semantics returns to the planner before code.

| Approved-design area | Amendment | Carrying PR |
|---|---|---|
| §§1 and 5 | Make the shim/runtime plane authoritative; define the new package boundaries and tmux as optional presentation rather than identity/delivery | PR 1 |
| §§3 and 10 | Record version-pinned SIGHUP, PTY, `flock`, advisory-record, `LOCAL_PEERPID`, socket-path, readiness, ancestry, and tracked incident-replay evidence plus the fake/live test boundaries | PR 1; evidence refinements in PRs 2–4 and 8 |
| §4 | Add the planner-approved foreground no-tmux surface, keep the internal shim entrypoint non-agent-facing, and pin which commands require or merely enrich from tmux | PR 1; exact help fixtures in PR 7 |
| §§6.1 and 6.6 | Replace direct-harness window startup with shim startup; name lock acquisition as the role-ownership instant; specify every pre/post-ownership failure, child cleanup, orphan retention, and rollback boundary | PR 1; exact creation/cleanup output fixtures in PRs 4–5 |
| §§6.2 and 13.6 | Replace pane resolution and `tmux send-keys` with version-first socket resolution, advisory lockfile-record/kernel-answerer comparison, fail-closed snapshot ancestry guard, readiness gate, and operation-name delivery | PR 1; exact protocol and process argv in PRs 2, 4, 6, and 7 |
| §§6.3–6.4 | Define runtime-driven session/role enumeration, the complete state vocabulary and precedence, presentation-only tmux observations, interim aggregate note, and tmux-only attach behavior | PR 1; exact table/JSON/help fixtures in PRs 6–7 |
| §§6.5 and 6.8 | Separate volatile claim/socket artifacts from durable reservation/child/config records; specify `child-starting`, orphan-safe relaunch, PID-reuse-token comparison, and presentation-only tmux metadata | PR 1; exact lifecycle fixtures in PRs 2, 4, and 5 |
| §7 and §12 | Pin the short production runtime base, durable state base, named root overrides, runtime socket-length refusal, and derived session/role caps; preserve validation-before-side-effects, quoting, and informational-environment rules | PR 1; exact validation and window-command tests in PRs 2 and 5 |
| §8 | Replace pane-root executable equality with shim/child parentage, pre-fork reservation, durable child identity, foreign-process start-token observation, orphan detection, PID-reuse-safe refusal, and snapshot ancestry inspection | PR 1; empirical/argv details in PRs 2–4 and 6 |
| §9 | Assign exit codes to version refusal, unsafe/forged topology, unsettled role, orphan refusal, partial cleanup, no-tmux attach, and observed delivery outcomes without borrowing old tmux meanings | PR 1; exact command mappings in PRs 5–7 |
| §13 | Split canonical external calls into optional tmux-presentation operations, process ancestry/identity operations, and the non-shell PTY child boundary; retire the production send-keys row | PR 1; exact tables alongside implementations in PRs 2–6 |
| §14 | Keep terminal layout repair, terminal emulation, multi-user hardening, same-user socket/lock tampering, and harness-native control planes out of scope | PR 1 |

PR 1 updates `SECURITY.md` from unresolved design-phase constraints to ratified implementation invariants, rewords constraint 2 as an advisory-record/kernel-answerer comparison with reviewer provenance, names the socket-forgery, root-override, ancestry-failure, and PID-observation residuals, records the future `internal/target` disposition, and describes the planned `golang.org/x/sys` supply-chain impact without adding the module early. Each later behavior PR changes shipped-behavior claims in the same PR as the corresponding production cutover.

## Existing SECURITY.md Claims Amended by Option S

| Existing claim that becomes false | Required amendment | Carrying PR |
|---|---|---|
| The security-model opening describes tmux sessions and direct control keystrokes as the complete product surface | Describe runtime records, local sockets, the shim-owned PTY, foreground no-tmux roles, and tmux as optional presentation | PR 1 specifies; PR 7 marks shipped |
| Launch/control/status/kill write no files; only skill installation writes under the home directory; agentctl creates no persistent files | Enumerate volatile socket/lock artifacts, durable reservation/child/config records, their two homes, permissions, overrides, cleanup, and residuals | PR 1 specifies; PR 2 records dependency/filesystem implementation status; PR 7 marks production paths shipped |
| Residual 6 says `Locking | None` | Replace it with the `flock` role claim, advisory body, concurrency outcomes, and crash/reclaim boundaries | PR 1 specifies; PR 4 records server implementation; PR 7 marks the atomic product cutover |

The existing controlling-the-wrong-terminal and metadata-gate paragraphs are amended in PR 1 with a per-check disposition table. PR 7 changes them to shipped wording in the same commit that removes `internal/target` and direct tmux delivery.

## SECURITY.md Binding-Constraint Coverage

Reviewers check this table in every implementation PR and reject any row whose cited task has not supplied its evidence.

| Constraint | Satisfied by |
|---|---|
| 1. Kernel-arbitrated role claim | Task 2 claim acquisition/reclaim tests; Task 4 lifetime ownership; Task 5 concurrent launch/relaunch integration |
| 2. Socket forgery detection | Task 1 honest constraint rewording; Task 2 advisory lockfile body plus `LOCAL_PEERPID`; Task 6 status disagreement state/rendering; Task 8 live adversarial replay |
| 3. Runtime-directory discipline | Task 1 roots/override/cap contract; Task 2 descriptor-verified volatile and durable homes plus runtime length refusal; Task 8 release fixture audit |
| 4. Operation names only | Task 2 protocol decoder; Task 4 server-to-registry dispatch and sole PTY writer; Task 6 client/structural tests |
| 5. Shim enforcement and target-chain disposition | Task 1 approved snapshot-ancestry decision and check inventory; Task 4 enforcement; Task 6 compatibility implementation; Task 7 atomic retirement/move/moot audit |
| 6. Never report absent while child may live | Task 1 SIGHUP evidence and orphan contract; Task 2 durable state and foreign start-token reader; Task 3 child observation; Task 4 reservation/upgrade ordering; Tasks 5–7 relaunch/status behavior; Task 8 crash replay |
| 7. Prove a clean channel before delivery | Task 3 terminal-state observation; Task 4 readiness gate; Task 6 pre-ready refusal; Task 8 live early-delivery replay |
| 8. Specify ownership and exit codes first | Task 1 approved §§6.6/9 amendments; Tasks 4–7 implement only those outcomes |
| 9. Keep delivery claims factual | Task 4 write/observation response; Task 6 CLI wording and cancellation tests; Task 8 live verification |
| 10. Version-gate the wire protocol | Task 2 version-first codec; Task 4 server handshake; Task 6 client/status refusal; Task 8 skew matrix |

---

### Task 1: Land decisions, empirical gates, superseding contract, and interim measures (PR 1)

**Files:**
- Create: `hack/probe-shim-sighup.sh`
- Create: `hack/probeshimsighup_test.go`
- Create: `docs/security/2026-08-10-issue-182-shim-probe-evidence.md`
- Verify: `docs/security/2026-08-10-issue-182-replay-evidence.md`
- Modify: `docs/superpowers/specs/2026-08-01-agentctl-design.md`
- Modify: `SECURITY.md`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/status/collector.go`
- Modify: `internal/status/collector_test.go`
- Modify: `internal/status/render.go`
- Modify: `internal/status/render_test.go`
- Modify: `cmd/agentctl/main.go`
- Modify: `cmd/agentctl/main_test.go`
- Modify: `README.md`
- Modify: `docs/release-checklist.md`
- Modify: `docs/release-verification-notes.md`

**Interfaces:**
- Produces: the approved Option S contract that supersedes the options paper.
- Produces: exact config length predicates consumed by Tasks 2, 5, and 7.
- Produces: a noncausal presentation diagnostic consumed unchanged by the final status collector.
- Consumes: build2's live incident-replay report as evidence before making any recovery claim.

**Acceptance criteria:**
- Issue #182 contains an `AMENDED 2026-08-10` section publishing binding rulings R1–R8 before this PR is implemented; the spec contains no unresolved behavioral branch.
- The spec names one ownership instant, complete failure/rollback behavior, complete state precedence, and a §9-discipline exit map before any shim implementation lands.
- The SIGHUP probe covers each pinned harness in an isolated nested PTY, records child/shim PIDs and observed termination, and never targets a default tmux server or existing agent.
- The spec describes the intended narrow `x/sys/unix` surface and supply-chain boundary, but this PR does not add the dependency before a production importer exists.
- Every §5 interim measure ships: aggregate observation note, join-pane warning, verified per-role recovery guidance, and grouped-session viewing guidance.
- The planning artifact already contains a byte-for-byte tracked copy of build2's full replay report; PR 1 verifies and cites it. Recovery prose pins all observed outcomes: closing the sole absorber first destroys the session and makes `status`/`relaunch` exit 3; relaunching all roles first succeeds but temporarily duplicates four stale and four replacement panes; the supported low-duplication order is replacement first, absorber cleanup second, remaining relaunches last, while `kill` plus `launch` remains the full-session alternative.
- The recovery explanation cites current `classifyRelaunchWindow`; it records that the paper's `requireAbsentWindow` identifier is stale and labels the low-duplication sequence as an inference from observed endpoints unless it is separately replayed.
- The status note is emitted only from the exact aggregate observation approved in the spec and never names a cause.

- [ ] **Step 1: Verify and consume the published planner rulings**

  Require the planner to publish R1–R8 in issue #182 as an `AMENDED 2026-08-10` contract predecessor. Verify the issue body contains the binding text, then implement that text without reopening a resolved branch in plan prose.

- [ ] **Step 2: Write failing cap and interim-status tests**

  Add boundary cases for the spec-selected session/role caps and status cases immediately below, equal to, and above the aggregate diagnostic threshold. Assert no note for near misses and no causal language.

- [ ] **Step 3: Run focused tests and capture RED**

  Run `go test ./internal/config ./internal/status ./cmd/agentctl -run 'Test.*(NameLength|MergedLayout|AggregateNote)' -count=1` and retain the failure summary for PR evidence.

- [ ] **Step 4: Add the SIGHUP probe contract and fixtures**

  Make the script fail unless it records pinned binary versions, establishes the nested PTY parent/child topology, terminates only its owned shim fixture, and observes the harness child outcome. Cover refusal, cleanup, and output parsing in `hack/probeshimsighup_test.go`.

- [ ] **Step 5: Execute the live probes and track build2's incident report**

  Run the SIGHUP probe once per harness in its throwaway fixture. Verify `docs/security/2026-08-10-issue-182-replay-evidence.md` remains the complete committed replay report, preserve its observed-versus-inferred distinctions, and cite that tracked path from the spec and recovery documentation. Do not reproduce the incident against a real fleet.

- [ ] **Step 6: Amend the governing spec and security contract**

  Apply every row in “Spec Amendments” above, update the dependency/threat/residual text, describe the future narrow `x/sys` use without editing the module or license archive, and replace the unresolved design-phase wording with ratified implementation invariants plus a numbered trace back to this plan's coverage matrix. Do not describe unimplemented behavior as shipped.

- [ ] **Step 7: Implement the minimal interim behavior and documentation**

  Add only the approved observational note and cap enforcement, then document the incident recovery and safer viewing recipe at the granularity established by build2.

- [ ] **Step 8: Run GREEN and reviewer gates**

  Run the focused packages, `go test ./...`, `go vet ./...`, `shellcheck hack/*.sh`, and `golangci-lint run`. Commit the spec before or with its behavior, push, wait for the PR's own CI, and obtain a correctness/adversarial reviewer gate before PRs 2 or 3 start.

### Task 2: Build the runtime namespace, kernel claim, durable record, and versioned codec (PR 2)

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `LICENSES/golang.org/x/sys/LICENSE`
- Modify: `LICENSES/README.md`
- Modify: `.goreleaser.yaml`
- Modify: `hack/verify-release-archives.sh`
- Modify: `hack/verifyreleasearchives_test.go`
- Create: `internal/shim/namespace.go`
- Create: `internal/shim/namespace_test.go`
- Create: `internal/shim/claim_darwin.go`
- Create: `internal/shim/claim_darwin_test.go`
- Create: `internal/shim/record.go`
- Create: `internal/shim/record_test.go`
- Create: `internal/shim/process_darwin.go`
- Create: `internal/shim/process_darwin_test.go`
- Create: `internal/shim/protocol.go`
- Create: `internal/shim/protocol_test.go`
- Modify: `SECURITY.md`

**Interfaces:**
- Produces: `shim.Namespace`, `shim.RolePath`, `shim.Claim`, `shim.Record`, `shim.Request`, `shim.Response`, and version-first encode/decode helpers.
- Produces: a held `flock` role claim, advisory lockfile identity, a `LOCAL_PEERPID` query for the connected socket answerer, and a foreign-process start-token reader.
- Consumes: Task 1's exact caps, runtime template, state vocabulary, protocol version, and ownership rules.

**Acceptance criteria:**
- Volatile runtime and durable state creation are separately descriptor-verified and private. The production socket/lock base is `/tmp/agentctl-<decimal-uid>/v1`; the child/config base is `os.UserConfigDir()/agentctl/state-v1` and survives runtime-tree deletion and reboot boundaries.
- `AGENTCTL_RUNTIME_ROOT` and `AGENTCTL_STATE_ROOT` are named, capped, absolute test/release overrides. They receive the same descriptor checks and are documented as same-user-controlled residuals, not trust anchors.
- The fully resolved socket path is rejected before claim or mutation when it exceeds Darwin's 104-byte `sun_path` capacity; name caps are derived from the approved worst-case resolved template.
- Claim contention is kernel-arbitrated by held `flock`; stale socket cleanup happens only after claim acquisition; SIGKILL releases the claim without treating the advisory lockfile body, socket unlink, or rebind as ownership evidence.
- Durable records are written atomically, never become liveness evidence, and support `child-starting` with shim PID/nonce plus the upgraded child PID/start-token identity needed for orphan-safe refusal. Deleting the volatile runtime tree does not delete or invalidate the durable record.
- Connected answerer identity comes from Darwin `getsockopt(SOL_LOCAL, LOCAL_PEERPID)`. The advisory record comparison detects disagreement without being described as a second kernel identity.
- The foreign start token is read through the pinned `ps -o lstart= -p PID` interface and malformed, missing, or failed observations refuse rather than guessing.
- Decode rejects missing/mismatched version before interpreting any other field and rejects unknown operations without passing text onward.
- This first production importer adds only the reviewed `x/sys/unix` surface, records its license and module graph, and makes the release archive carry the license.

- [ ] **Step 1: Write namespace and claim RED tests**

  Cover production and override roots, cap boundaries, the fully resolved 103/104/105-byte socket boundary, pre-existing/symlinked/wrong-mode directories, descriptor substitution, two claimants, SIGKILL release, stale sockets, advisory lockfile bodies, and `LOCAL_PEERPID` observation.

- [ ] **Step 2: Implement the smallest namespace and claim layer**

  Add the module/license/archive files in this first importer PR. Use `golang.org/x/sys/unix` only behind Darwin implementations. Keep volatile and durable filesystem mutation scoped to their separately validated roots and the exact claim held by this process.

- [ ] **Step 3: Write durable-record and codec RED tests**

  Cover pre-fork `child-starting`, upgrade, partial writes, runtime-tree deletion, simulated reboot residue, malformed records, PID-reuse token matches/mismatches, the pinned foreign `ps -o lstart=` reader and all its failures, absent/foreign protocol versions, oversized frames, unknown fields, unknown operations, and attempts to encode payload text.

- [ ] **Step 4: Implement record and codec GREEN**

  Keep the public codec types incapable of representing arbitrary PTY input. Run `go test ./internal/shim -run 'Test.*(Namespace|Claim|Record|Protocol)' -count=1`.

- [ ] **Step 5: Re-verify dependency and archive evidence**

  Inspect `go mod graph`, run `go mod tidy` and require no unexpected diff, then run govulncheck and the snapshot archive check against this PR's newly admitted dependency materials.

- [ ] **Step 6: Run concurrency and package GREEN**

  Run `go test -race ./internal/shim -count=1`, `go test ./...`, and `go vet ./...`; preserve exact contention and reclaim evidence in the PR.

### Task 3: Build the nested PTY and child-lifecycle boundary (PR 3, parallel with PR 2)

**Files:**
- Create: `internal/ptyx/pty_darwin.go`
- Create: `internal/ptyx/pty_darwin_test.go`
- Create: `internal/ptyx/child.go`
- Create: `internal/ptyx/child_test.go`
- Create: `internal/ptyx/relay.go`
- Create: `internal/ptyx/relay_test.go`
- Create: `internal/ptyx/terminal.go`
- Create: `internal/ptyx/terminal_test.go`

**Interfaces:**
- Produces: `ptyx.Opener`, `ptyx.ChildStarter`, `ptyx.Child`, `ptyx.Relay`, and a terminal-state observer used by Task 4.
- Consumes: validated argv and environment only; it has no session/role resolution, protocol parsing, registry lookup, or filesystem ownership.

**Acceptance criteria:**
- The child starts with the nested PTY as its controlling terminal and the parent observes its exact PID before readiness.
- Input/output relay is byte-preserving, resize and termios changes are forwarded, EOF/half-close behavior is bounded, and relayed terminal input enters the same serialized writer used later by control operations.
- The terminal observer distinguishes pre-ready cooked/echo state from the approved settled state without reading terminal contents.
- Teardown records signal attempts and observed child exit separately; a surviving child is returned as a typed outcome rather than reported dead.
- This PR edits no Task 2 artifact, including shared docs, so the only parallel implementation lanes remain file-disjoint.

- [ ] **Step 1: Write PTY-open and child-start RED tests**

  Cover syscall failure at every ordered setup point, controlling-terminal assignment, process-group ownership, exact argv/env, child PID capture, and cleanup of only resources created by the invocation.

- [ ] **Step 2: Implement PTY open and injected child start**

  Keep `os/exec` behind `ptyx.ChildStarter`; unit tests use a recording fake, while Darwin integration tests use a stub child in a throwaway PTY.

- [ ] **Step 3: Write relay/readiness/resize RED tests**

  Cover binary byte round trips, concurrent resize, early control attempts, simultaneous operator input and control bytes, context cancellation, parent input EOF, child exit, shim-side failure, and exactly one serialized PTY writer.

- [ ] **Step 4: Implement relay and terminal observation GREEN**

  Run `go test ./internal/ptyx -count=1` and `go test -race ./internal/ptyx -count=1`.

- [ ] **Step 5: Capture throwaway live PTY evidence in the PR**

  Exercise the stub relay and both version-pinned harness startup transitions without tmux, preserve the commands and observed facts in the PR body, and run the PR's full unit/vet/lint gates. Do not edit PR 2's spec, security, dependency, or evidence artifacts from this parallel lane.

### Gate S: Decide whether Option S remains viable after PRs 2 and 3

The planner reviews the merged Darwin claim/socket evidence from PR 2 and PTY/readiness evidence from PR 3 before PR 4 starts. Continue Option S only when `flock`, `LOCAL_PEERPID`, the fully resolved socket-length policy, nested controlling-PTY startup, clean-channel observation, and durable reservation boundary all satisfy the approved contract without a new semantic surface. If any failure cannot be fixed inside that surface, stop the shim graph, amend issue #182, and return to the named pane-scoped Option A fallback in options-paper §3; do not silently weaken the gate or improvise a third design.

### Task 4: Assemble the resident shim server and closed operation path (PR 4)

**Files:**
- Create: `internal/shim/server.go`
- Create: `internal/shim/server_test.go`
- Create: `internal/shim/client.go`
- Create: `internal/shim/client_test.go`
- Create: `internal/shim/lifecycle.go`
- Create: `internal/shim/lifecycle_test.go`
- Create: `cmd/agentctl/shim_command.go`
- Create: `cmd/agentctl/shim_command_test.go`
- Modify: `internal/control/registry.go`
- Modify: `internal/control/registry_test.go`
- Modify: `internal/harness/harness.go`
- Modify: `internal/harness/harness_test.go`
- Modify: `docs/superpowers/specs/2026-08-01-agentctl-design.md`
- Modify: `SECURITY.md`

**Interfaces:**
- Produces: `shim.Server.Run`, `shim.Client.Observe`, `shim.Client.DeliverOperation`, and `shim.Client.Stop` with the exact typed outcomes approved in Task 1.
- Produces: a hidden, validated shim command that reconstructs child argv through `harness.AgentArgv`; it accepts no raw child command or payload.
- Consumes: Task 2's claim/record/protocol and Task 3's PTY/child interfaces.

**Acceptance criteria:**
- The server acquires the `flock` ownership claim once, writes the durable `child-starting` reservation with its PID and nonce before `os/exec.Start`, upgrades the record atomically with the observed child PID/start token only after successful start, publishes no ready endpoint before PTY settle, and rolls back or records an indeterminate/orphan outcome exactly as the amended ownership section requires.
- Every request validates protocol version, claimed session/role, advisory-record/`LOCAL_PEERPID` answerer agreement, readiness, and registry membership before PTY mutation.
- The shim is the sole PTY writer. Relayed operator input and registry-resolved control bytes pass through one serialization point; registry lookup occurs server-side, and structural tests prove no client path can provide payload bytes.
- Response facts distinguish accepted request, bytes written, submit observed, cancellation residue, child exit, and orphan/cleanup outcomes without asserting harness execution.

- [ ] **Step 1: Write lifecycle and ownership RED tests**

  Table-drive every failure point from claim through settle, including failure to persist `child-starting`, fork/start failure, post-start token/upgrade failure, listener failure, readiness timeout, shim cancellation, child refusal to exit, and cleanup failure. Assert that reservation precedes fork, upgrade follows observed start, and no uncertain child state is erased or reported absent.

- [ ] **Step 2: Write protocol-enforcement RED tests**

  Cover foreign/absent version, wrong role/session, advisory-record/`LOCAL_PEERPID` mismatch, pre-ready delivery, unknown operation, cancellation during delivery, concurrent clients, and socket replacement after connection.

- [ ] **Step 3: Implement the minimal server/client lifecycle**

  Keep status observation read-only, serialize PTY operations per role, and preserve the child record whenever its process may still be live.

- [ ] **Step 4: Add and wire the hidden shim command**

  Parse only the internal validated fields approved in §4, build harness argv through the existing registry, and keep it out of agent-facing help and the embedded skill command inventory.

- [ ] **Step 5: Run GREEN and adversarial review**

  Run `go test -race ./internal/shim ./internal/ptyx ./internal/control ./internal/harness ./cmd/agentctl -run 'Test.*Shim' -count=1`, then full unit/vet/lint gates. The PR review explicitly attacks raw-input reachability, claim races, pre-ready delivery, version skew, and orphan cleanup.

### Task 5: Add shim-backed fleet/kill implementations and the shared fixture (PR 5)

**Files:**
- Create: `internal/fleet/shim.go`
- Create: `internal/fleet/shim_test.go`
- Create: `internal/fleet/shim_relaunch.go`
- Create: `internal/fleet/shim_relaunch_test.go`
- Create: `internal/kill/shim_executor.go`
- Create: `internal/kill/shim_executor_test.go`
- Create: `internal/preflight/shim.go`
- Create: `internal/preflight/shim_test.go`
- Create: `internal/tmuxx/presentation.go`
- Create: `internal/tmuxx/presentation_test.go`
- Modify: `internal/shellq/shellq_test.go`
- Modify: `cmd/agentctl/integration_fixture_test.go`
- Create: `cmd/agentctl/integration_shim_lifecycle_test.go`
- Modify: `docs/superpowers/specs/2026-08-01-agentctl-design.md`
- Modify: `SECURITY.md`

**Interfaces:**
- Produces: explicitly named shim-backed fleet launch/relaunch and kill implementations behind compatibility seams, without changing the current `fleet.Launch`, `fleet.Relaunch`, `kill.Executor`, or `cmd/agentctl/main.go` production path in this PR.
- Produces and owns: the shared integration fixture for isolated runtime/state roots, throwaway tmux presentation, stub harnesses, and deterministic cleanup; PR 6 consumes it.
- Consumes: Task 4's hidden shim argv and client lifecycle plus optional typed tmux presentation creation.

**Acceptance criteria:**
- tmux windows start the current agentctl executable's hidden shim command assembled at the sole shell-string site; harness argv never appears as a caller-controlled shim argument.
- Launch records the complete roster/config before roles can be reported missing, waits for each shim's approved readiness observation, and applies the amended ownership/rollback table exactly.
- Concurrent launch/relaunch claims have one kernel winner. Losing invocations create no second harness and remove nothing they do not own.
- Relaunch refuses every live, unsettled, forged, indeterminate, and orphan child state; it creates only after the approved absence observation.
- Kill quiesces operations, requests child termination, observes outcomes, and removes optional tmux presentation only under the approved prior-state rule.
- Existing CLI behavior and the legacy target/send-keys path still compile and retain their current tests. The new shim path is invoked directly by unit/integration tests until PR 7 performs the atomic cutover.

- [ ] **Step 1: Replace launch transcript expectations with shim-start RED tests**

  Against the new shim implementation, assert exact executable/argv element boundaries, `shellq` use, self-binary resolution, roster/config ordering, ready observation, per-role failures, and rollback calls. Also run the legacy launch tests unchanged to prove this PR did not cut over production.

- [ ] **Step 2: Implement shim-backed launch GREEN**

  Preserve optional tmux presentation creation behind new typed `tmuxx` calls and keep runtime identity independent from returned pane/window IDs. Do not rewire existing exported production constructors or CLI dependencies.

- [ ] **Step 3: Write relaunch and kill RED matrices**

  Cover absent, running, starting, orphan, forged-answerer, protocol-skew, stale-record, cleanup-failed, concurrent-contender, and tmux-presentation-gone cases. Assert no mutation before every non-destructive check completes.

- [ ] **Step 4: Implement relaunch and kill GREEN**

  Run `go test ./internal/fleet ./internal/kill ./internal/preflight ./internal/tmuxx -count=1`, including unchanged legacy tests and direct shim-path tests.

- [ ] **Step 5: Run throwaway-tmux lifecycle integration**

  First extend and own `cmd/agentctl/integration_fixture_test.go` with both isolated roots and owned-child cleanup. Then prove through the direct shim API that join/break/swap/move operations do not change shim identity or delivery, shim SIGKILL produces the approved child state, relaunch cannot double-launch, and every test uses the fixture socket and stub harnesses.

### Task 6: Add shim-backed control/status compatibility implementations (PR 6)

**Files:**
- Create: `internal/control/shim_dispatcher.go`
- Create: `internal/control/shim_dispatcher_test.go`
- Create: `internal/control/ancestry.go`
- Create: `internal/control/ancestry_test.go`
- Create: `internal/status/shim_collector.go`
- Create: `internal/status/shim_collector_test.go`
- Create: `internal/status/shim_render.go`
- Create: `internal/status/shim_render_test.go`
- Modify: `internal/status/status.go`
- Create: `internal/status/status_test.go`
- Modify: `internal/structural/invariants_test.go`
- Modify: `cmd/agentctl/integration_lifecycle_test.go`
- Modify: `docs/superpowers/specs/2026-08-01-agentctl-design.md`
- Modify: `SECURITY.md`

**Interfaces:**
- Produces: explicitly named operation-name-only shim dispatcher and runtime-backed status collector/renderers with optional presentation enrichment, kept beside the current production implementations.
- Produces: an injected ancestry observer that executes exactly one `ps -eo pid=,ppid=` snapshot per guard decision.
- Consumes: `shim.Client` observation/delivery interfaces and Task 1's full state/output contract.
- Consumes: PR 5's integration fixture; it does not modify or duplicate fixture ownership.

**Acceptance criteria:**
- The CLI-to-dispatcher boundary carries operation name, session, and role only; the registry payload is resolved inside the shim server.
- The former target checks are explicitly classified in `SECURITY.md`: runtime validation or shim enforcement, presentation-only/moot, or retired with rationale linked to the approved spec.
- Self-target detection starts at the caller PID, walks the single parsed snapshot toward the target PID obtained from the already connected peer's `LOCAL_PEERPID`, and refuses on a broken chain, disappeared PID, malformed row, duplicate PID, loop, or command failure. It never uses the advisory lockfile PID, `TMUX_PANE`, or role/session environment values.
- Status enumerates the approved runtime roster in both tmux and no-tmux cases, compares the advisory lockfile identity with the `LOCAL_PEERPID` answerer, preserves build2's presentation note, and reports every state at the approved precedence.
- This PR adds no new `send-keys` or payload-text surface. The existing `internal/target` and `tmuxx.DeliverPayload` remain temporarily because `cmd/agentctl/main.go` still imports them; PR 7 retires them in the same commit that rewires the CLI.

- [ ] **Step 1: Write dispatcher and ancestry RED tests**

  Assert the exact single `ps -eo pid=,ppid=` argv and one-snapshot call count for self, sibling, human-terminal, disappeared parent/target, broken chain, duplicate PID, loop, malformed output, and tool failure. Source the target only from a fake `LOCAL_PEERPID` observation and assert the dispatcher never receives or exposes payload text.

- [ ] **Step 2: Write status RED matrix**

  Cover every approved runtime state and precedence collision, missing records, live lock/no socket, socket/no lock, holder/answerer disagreement, protocol skew, orphan child, optional tmux unavailable, and merged presentation.

- [ ] **Step 3: Implement control/status GREEN**

  Keep observation read-only and delivery serialized by the shim. Run `go test ./internal/control ./internal/status ./internal/tmuxx -count=1`.

- [ ] **Step 4: Preserve the compatibility boundary for PR 7**

  Add AST invariants that reject payload-bearing additions to the new shim APIs while explicitly inventorying the legacy `internal/target` and `tmuxx.DeliverPayload` exception that PR 7 must delete. Prove `cmd/agentctl` still compiles against the legacy path and the shim-backed implementations work when constructed directly.

- [ ] **Step 5: Run race and layout integration**

  Run `go test -race ./internal/control ./internal/status ./internal/shim -count=1` and the focused real-tmux incident replay using PR 5's throwaway fixture, proving role identity/delivery survive presentation rearrangement without changing the default CLI path.

### Task 7: Converge CLI wiring, no-tmux foreground operation, attach, and operator docs (PR 7)

**Files:**
- Modify: `cmd/agentctl/main.go`
- Create: `cmd/agentctl/main_run_test.go`
- Modify: `cmd/agentctl/main_control_test.go`
- Modify: `cmd/agentctl/main_launch_test.go`
- Modify: `cmd/agentctl/main_relaunch_test.go`
- Modify: `cmd/agentctl/main_attach_test.go`
- Modify: `cmd/agentctl/main_test.go`
- Modify: `internal/fleet/fleet.go`
- Modify: `internal/fleet/fleet_test.go`
- Modify: `internal/fleet/relaunch.go`
- Modify: `internal/fleet/relaunch_test.go`
- Modify: `internal/kill/executor.go`
- Modify: `internal/kill/executor_test.go`
- Modify: `internal/control/dispatcher.go`
- Modify: `internal/control/dispatcher_test.go`
- Modify: `internal/status/collector.go`
- Modify: `internal/status/collector_test.go`
- Modify: `internal/status/render.go`
- Modify: `internal/status/render_test.go`
- Delete: `internal/target/resolver.go`
- Delete: `internal/target/resolver_test.go`
- Delete: `internal/target/errors.go`
- Modify: `internal/tmuxx/control.go`
- Modify: `internal/tmuxx/control_test.go`
- Modify: `internal/structural/invariants_test.go`
- Modify: `internal/attach/executor.go`
- Modify: `internal/attach/executor_test.go`
- Modify: `internal/session/resolver.go`
- Modify: `internal/session/resolver_test.go`
- Modify: `cmd/agentctl/integration_fixture_test.go`
- Modify: `cmd/agentctl/integration_lifecycle_test.go`
- Modify: `README.md`
- Modify: `docs/release-checklist.md`
- Modify: `docs/superpowers/specs/2026-08-01-agentctl-design.md`
- Modify: `SECURITY.md`

**Interfaces:**
- Produces: the atomic flag-day wiring for shim-backed launch/control/status/relaunch/kill, the approved foreground `run` command, and tmux-only attach.
- Consumes: PR 5 lifecycle interfaces and PR 6 control/status interfaces without adding a second behavior path.

**Acceptance criteria:**
- The foreground path validates one role through `internal/config`, starts the same shim lifecycle used by tmux-backed launch, attaches its streams to the caller terminal, and creates no shell or viewer.
- Session resolution keeps `AGENTCTL_SESSION` as selection-only input; no advisory variable gates a role, child, socket, lock, or PTY action.
- Attach succeeds only for an observed tmux presentation of the runtime fleet and emits the approved no-presentation refusal otherwise.
- Help, README, examples, and exit rendering match the amended spec; the hidden shim entrypoint remains absent from agent-facing command inventories.
- The integration fixture supports both throwaway tmux presentation and isolated direct-terminal shim processes and always reaps owned children/runtime paths.
- The cutover commit removes every `internal/target` import and production `tmux send-keys`/payload-text delivery entrypoint while preserving compilation throughout the commit; no intermediate PR deletes a dependency still imported by `cmd/agentctl/main.go`.

- [ ] **Step 1: Write final CLI dispatch/exit RED tests**

  Cover every shim result class, explicit/ambient session selection, JSON/table status, foreground run validation and signal forwarding, no-tmux attach, and unchanged `version`/`skill` behavior.

- [ ] **Step 2: Wire shared dependencies once**

  Construct one runtime namespace/client and inject it into lifecycle, control, status, attach, and run paths. Rewire legacy compatibility constructors to the shim-backed implementations, delete `internal/target`, remove `tmuxx.DeliverPayload`, and strengthen structural tests in this same atomic step. Do not duplicate resolution or error mapping between tmux and no-tmux modes.

- [ ] **Step 3: Implement foreground run and attach behavior**

  Reuse the Task 4 server lifecycle directly, keep presentation optional, and render only outcomes observed by the final command.

- [ ] **Step 4: Update operator documentation and integration fixtures**

  Document the flag-day transition, volatile and durable roots plus both overrides, cleanup, no-tmux composition, attach limitation and exact factual refusal, orphan/indeterminate remedies, layout-proof controls, interim merged-layout note, replay-qualified recovery order, and factual-delivery limitation. Change SECURITY/spec claims from planned to shipped in the same PR.

- [ ] **Step 5: Run CLI and integration GREEN**

  Run `go test ./cmd/agentctl ./internal/attach ./internal/session -count=1` and `go test -tags integration ./cmd/agentctl -count=1`.

### Task 8: Update release verification, embedded skill, and complete 0.5.0 evidence (PR 8)

**Files:**
- Modify: `hack/release-verify.sh`
- Modify: `hack/releaseverify_test.go`
- Create: `hack/fixtures/shim-version/main.go`
- Create: `hack/shimversionfixture_test.go`
- Modify: `hack/testdata/release-verify-live-artifact/metadata.txt`
- Modify: `hack/testdata/release-verify-live-results.golden`
- Modify: `docs/release-checklist.md`
- Modify: `docs/release-verification-notes.md`
- Modify: `skills/agentctl/SKILL.md`
- Modify: `skills/agentctl/references/status-states.md`
- Modify: `skills/agentctl/references/exit-codes.md`
- Modify: `cmd/agentctl/skill_contract_test.go`
- Modify: `cmd/agentctl/skill_launch_notice_test.go`
- Modify: `.github/workflows/ci.yml` only if an approved new deterministic gate cannot live in an existing command
- Verify: `.goreleaser.yaml`, `hack/verify-release-archives.sh`, and every changed file

**Interfaces:**
- Produces: version-pinned live evidence for PTY startup, clear/compact delivery, layout rearrangement, shim crash/orphan handling, version skew in both directions, no-tmux status, and attach refusal.
- Produces: embedded-skill state/exit inventories paired to the 0.5.0 binary.
- Consumes: build2's tracked incident replay, Task 1's probe evidence, Task 3's PTY evidence retained in its PR, and the complete merged implementation.

**Acceptance criteria:**
- The live verifier uses only its owned named tmux server, isolated `AGENTCTL_RUNTIME_ROOT`, isolated `AGENTCTL_STATE_ROOT`, temporary HOME, and stub/consented harness processes; teardown observes absence of every owned child, socket, lock, PTY helper, tmux server, and credential fixture while retaining only evidence the checklist explicitly owns.
- Verification proves the four layout operations leave identity and delivery intact, early delivery refuses until ready, an advisory-record/`LOCAL_PEERPID` mismatch is reported, version skew fails closed, and no orphan or `child-starting` indeterminate can be called missing or relaunched beside a possible survivor.
- The committed deterministic foreign-version fixture is a separately built second binary artifact. The matrix runs current CLI → foreign shim and foreign client → current shim, plus absent-version and matching-current controls, and records both artifact hashes/versions.
- Release archives contain the `x/sys` license and all prior license materials.
- The embedded skill exposes only the approved agent-facing surface and matches `status.States()` plus CLI exit constants through drift tests.

- [ ] **Step 1: Write release-verifier fixture RED tests**

  Add deterministic stubs for every new checkpoint and cleanup path, including interruption at each phase and surviving-child simulation. Build `hack/fixtures/shim-version/main.go` as the second protocol artifact and cover current-client/foreign-server plus foreign-client/current-server directions, absent version, expected refusal phase, and matching controls.

- [ ] **Step 2: Implement the 0.5.0 live walkthrough**

  Extend the runbook and script with the approved checkpoints, consuming rather than duplicating the Task 1/3 evidence where a live step would add no new fact.

- [ ] **Step 3: Update embedded skill and drift fixtures**

  Advance its release version, document new states/remedies and no-tmux rules, and ensure the internal shim command remains undiscoverable to agents.

- [ ] **Step 4: Run all local gates on current main**

  Fetch and rebase onto current `main`, then run `go test ./...`, `go vet ./...`, `go test -race ./...`, `shellcheck hack/*.sh`, `golangci-lint run`, `go test -tags integration ./...`, `go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...`, `goreleaser check`, `goreleaser release --snapshot --clean --skip=notarize`, and `./hack/verify-release-archives.sh dist/*.tar.gz`.

- [ ] **Step 5: Capture live release evidence**

  Run the amended release checklist against the release candidate, record exact tool/harness versions and outcomes, and stop on any mismatch instead of weakening the contract.

- [ ] **Step 6: Publish and obtain the reviewer gate**

  Push the rebased branch, wait for its own `pull_request` CI run, quote the exact run URL in the review request, obtain the reviewer verdict, and detach the worktree after the PR is opened. Do not self-merge.

## Plan Completion Checklist

- [ ] Every spec-amendment row is assigned to a PR and no semantic delta is deferred into implementation prose.
- [ ] Every SECURITY.md constraint maps to at least one unit test, one owning task, and live evidence where the constraint is empirical.
- [ ] Binding planner rulings R1–R8 are published in issue #182 before PR 1 and no task reintroduces a resolved proposal.
- [ ] build2's complete incident-replay report is tracked by Task 1; interim recovery guidance cites `classifyRelaunchWindow`, the sole-window exit-3 outcome, the eight-pane duplicate interval, the supported ordering, and the `kill` plus `launch` alternative without presenting inference as replay.
- [ ] The only parallel implementation PRs, PR 2 and PR 3, share no edited file including docs; all later implementation PRs are serial, and PR 5 owns the shared integration fixture before PR 6 consumes it.
- [ ] Gate S records an explicit continue-Option-S or fall-back-to-Option-A decision before PR 4; no failing empirical contract is silently weakened.
- [ ] PRs 5 and 6 keep legacy imports compiling beside their shim implementations; PR 7 alone performs the atomic CLI rewire and target/send-keys deletion.
- [ ] Release verification uses a committed second binary fixture and exercises both protocol-version mismatch directions plus absent/matching controls.
- [ ] Each behavior PR preserves RED and GREEN commands/results in its PR body.
- [ ] Every integration test uses a throwaway tmux socket or an isolated no-tmux runtime root and reaps what it owns.
- [ ] Every PR is rebased on current `main`, reruns the relevant gates, receives fresh PR CI, and passes a reviewer gate.
