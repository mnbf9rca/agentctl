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

## PROPOSED §7 Resolutions — Planner Approval Required

No task that depends on one of these choices starts until the planner records its ruling in issue #182 and Task 1 folds the result into the approved spec.

1. **PROPOSED — retain the self-target guard with a process-ancestry walk.** Starting at the control CLI's own PID, walk parent PIDs through a typed `ps` wrapper and refuse when the resolved target shim PID appears in that ancestry. Obtain the target PID from the kernel-reported lock holder and require the socket answerer's PID to match it before the walk. This works inside tmux, plain terminal tabs, and SSH; it does not promote `TMUX_PANE` or any advisory environment variable into targeting evidence.
2. **PROPOSED — implement an explicit orphan state even if the SIGHUP probes pass.** Persist the child identity before the role becomes ready, and treat shim death with a possibly live recorded child as an observable orphan. `relaunch` refuses while that child identity remains live and may create a replacement only after absence is observed; teardown attempts reap first but never equates a sent signal with death. Version-pinned SIGHUP evidence still runs first and informs the normal kill path, but harness-version behavior is not the only barrier preventing a double launch.
3. **PROPOSED — make runtime role records authoritative for `status`, keep `attach` tmux-only, and add a foreground no-tmux role path.** `status` counts the same runtime sessions and declared roles whether or not tmux exists; tmux may add presentation facts but cannot change role identity or liveness. A foreground `agentctl run` path starts one validated role/shim in the caller's terminal so operators can compose a fleet from terminal tabs or SSH. `attach` remains an iTerm2/tmux viewer and factually refuses a runtime fleet with no tmux presentation instead of growing a terminal multiplexer or viewer.

## Pull Request and Dependency Graph

Each PR gets its own focused signed commits, reviewer gate, and fresh pull-request CI. PR numbers below are sequencing labels, not GitHub numbers.

| PR | Scope | Depends on | Parallel lane |
|---|---|---|---|
| PR 1 | Decisions, probes, superseding spec/security amendments, and interim measures | none | Builder 1; Builder 2 reviews probe/spec evidence |
| PR 2 | Runtime namespace, role claim, durable record, and protocol codec | PR 1 | Builder 1, parallel with PR 3 |
| PR 3 | Nested PTY, child lifecycle, relay, resize, and terminal-state observation | PR 1 | Builder 2, parallel with PR 2 |
| PR 4 | Resident shim server, hidden shim entrypoint, readiness, and closed operation delivery | PR 2 + PR 3 | integration point; one builder implements, the other adversarially reviews |
| PR 5 | Fleet launch/relaunch/kill lifecycle cutover | PR 4 | Builder 1, parallel with PR 6; no `cmd/agentctl/main.go` edits |
| PR 6 | Control and status cutover; target/send-keys retirement | PR 4 | Builder 2, parallel with PR 5; no `cmd/agentctl/main.go` edits |
| PR 7 | CLI wiring, no-tmux foreground path, attach behavior, integration and operator docs | PR 5 + PR 6 | convergence PR |
| PR 8 | Release verification, embedded skill, release notes, and complete 0.5.0 gate evidence | PR 7 | final release-scoped verification |

Dependency edges:

```text
PR 1 ──┬──> PR 2 ──┐
       └──> PR 3 ──┴──> PR 4 ──┬──> PR 5 ──┐
                               └──> PR 6 ──┴──> PR 7 ──> PR 8
```

## Spec Amendments That Supersede the Options Paper

PR 1 carries every semantic design delta below and lands before shim code. Later PRs may add only implementation-descriptive evidence, exact argv, and exact output fixtures for behavior already approved in PR 1; any new surface or semantics returns to the planner before code.

| Approved-design area | Amendment | Carrying PR |
|---|---|---|
| §§1 and 5 | Make the shim/runtime plane authoritative; define the new package boundaries and tmux as optional presentation rather than identity/delivery | PR 1 |
| §§3 and 10 | Record the version-pinned SIGHUP, PTY, lock-holder, socket-path, readiness, ancestry, and incident-replay evidence and the required fake/live test boundaries | PR 1; evidence refinements in PRs 2–4 and 8 |
| §4 | Add the planner-approved foreground no-tmux surface, keep the internal shim entrypoint non-agent-facing, and pin which commands require or merely enrich from tmux | PR 1; exact help fixtures in PR 7 |
| §§6.1 and 6.6 | Replace direct-harness window startup with shim startup; name lock acquisition as the role-ownership instant; specify every pre/post-ownership failure, child cleanup, orphan retention, and rollback boundary | PR 1; exact creation/cleanup output fixtures in PRs 4–5 |
| §§6.2 and 13.6 | Replace pane resolution and `tmux send-keys` with version-first socket resolution, lock-holder/socket-answerer cross-check, ancestry guard, readiness gate, and operation-name delivery | PR 1; exact protocol and process argv in PRs 2, 4, and 6 |
| §§6.3–6.4 | Define runtime-driven session/role enumeration, the complete state vocabulary and precedence, presentation-only tmux observations, interim aggregate note, and tmux-only attach behavior | PR 1; exact table/JSON/help fixtures in PRs 6–7 |
| §§6.5 and 6.8 | Move authoritative role/config/child records out of tmux; specify durable roster/config facts, orphan-safe relaunch, and which tmux metadata remains presentation-only | PR 1; exact lifecycle fixtures in PR 5 |
| §7 and §12 | Pin session/role length caps derived from the fixed runtime template and Darwin `sun_path` bound; preserve validation-before-side-effects, quoting, and informational-environment rules | PR 1; exact validation and window-command tests in PRs 2 and 5 |
| §8 | Replace pane-root executable equality with shim/child parentage and durable child identity; specify readiness, orphan detection, PID-reuse-safe refusal, and ancestry inspection | PR 1; empirical/argv details in PRs 2–4 |
| §9 | Assign exit codes to version refusal, unsafe/forged topology, unsettled role, orphan refusal, partial cleanup, no-tmux attach, and observed delivery outcomes without borrowing old tmux meanings | PR 1; exact command mappings in PRs 5–7 |
| §13 | Split canonical external calls into optional tmux-presentation operations, process ancestry/identity operations, and the non-shell PTY child boundary; retire the production send-keys row | PR 1; exact tables alongside implementations in PRs 2–6 |
| §14 | Keep terminal layout repair, terminal emulation, multi-user hardening, same-user socket/lock tampering, and harness-native control planes out of scope | PR 1 |

PR 1 updates `SECURITY.md` from unresolved design-phase constraints to ratified implementation invariants, names the socket-forgery and PID-observation residuals, records the complete `internal/target` disposition, and documents the admitted `golang.org/x/sys` supply-chain and release-license impact. Each later behavior PR changes the section's shipped-behavior claims in the same PR as the corresponding code.

## SECURITY.md Binding-Constraint Coverage

Reviewers check this table in every implementation PR and reject any row whose cited task has not supplied its evidence.

| Constraint | Satisfied by |
|---|---|
| 1. Kernel-arbitrated role claim | Task 2 claim acquisition/reclaim tests; Task 4 lifetime ownership; Task 5 concurrent launch/relaunch integration |
| 2. Socket forgery detection | Task 2 kernel lock-holder query and protocol identity; Task 6 status disagreement state/rendering; Task 8 live adversarial replay |
| 3. Runtime-directory discipline | Task 1 spec/cap derivation; Task 2 descriptor-verified creation, modes, path-bound tests; Task 8 release fixture audit |
| 4. Operation names only | Task 2 protocol decoder; Task 4 server-to-registry dispatch and sole PTY writer; Task 6 client/structural tests |
| 5. Shim enforcement and target-chain disposition | Task 1 approved ancestry decision and check inventory; Task 4 enforcement; Task 6 retirement/move/moot audit; Task 7 CLI wiring |
| 6. Never report absent while child may live | Task 1 SIGHUP evidence and orphan contract; Task 3 child observation; Task 4 durable record; Tasks 5–6 relaunch/status behavior; Task 8 crash replay |
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
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `LICENSES/golang.org/x/sys/LICENSE`
- Modify: `LICENSES/README.md`
- Modify: `.goreleaser.yaml`
- Modify: `hack/verify-release-archives.sh`
- Modify: `hack/verifyreleasearchives_test.go`
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
- The planner has ruled on all three PROPOSED §7 resolutions in the issue body; the spec contains no unresolved behavioral branch.
- The spec names one ownership instant, complete failure/rollback behavior, complete state precedence, and a §9-discipline exit map before any shim implementation lands.
- The SIGHUP probe covers each pinned harness in an isolated nested PTY, records child/shim PIDs and observed termination, and never targets a default tmux server or existing agent.
- The reviewed `x/sys` version is exercised by the probe fixture, recorded in the module graph, documented in the supply-chain boundary, and included in release archives before the parallel syscall implementation PRs begin.
- Every §5 interim measure ships: aggregate observation note, join-pane warning, verified per-role recovery guidance, and grouped-session viewing guidance.
- Recovery prose cites and matches build2's report; if the report does not establish the claim, the PR narrows the prose instead of inferring success.
- The status note is emitted only from the exact aggregate observation approved in the spec and never names a cause.

- [ ] **Step 1: Obtain and record the planner rulings**

  Reply on the issue-182 planning thread with the three proposals above. Stop affected spec/code work until the planner supplies a ruling, then edit issue #182 with an `AMENDED 2026-08-10` contract section if the ruling changes or completes its behavior.

- [ ] **Step 2: Write failing cap and interim-status tests**

  Add boundary cases for the spec-selected session/role caps and status cases immediately below, equal to, and above the aggregate diagnostic threshold. Assert no note for near misses and no causal language.

- [ ] **Step 3: Run focused tests and capture RED**

  Run `go test ./internal/config ./internal/status ./cmd/agentctl -run 'Test.*(NameLength|MergedLayout|AggregateNote)' -count=1` and retain the failure summary for PR evidence.

- [ ] **Step 4: Add the SIGHUP probe contract and fixtures**

  Make the script fail unless it records pinned binary versions, establishes the nested PTY parent/child topology, terminates only its owned shim fixture, and observes the harness child outcome. Cover refusal, cleanup, and output parsing in `hack/probeshimsighup_test.go`.

- [ ] **Step 5: Execute the live probes and consume build2's incident report**

  Run the SIGHUP probe once per harness in its throwaway fixture. Import build2's report path and exact observations into the evidence document and release notes; do not reproduce the incident against a real fleet.

- [ ] **Step 6: Amend the governing spec and security contract**

  Apply every row in “Spec Amendments” above, update the dependency/threat/residual text, admit and license the probe-tested `x/sys` version, and replace the unresolved design-phase wording with ratified implementation invariants plus a numbered trace back to this plan's coverage matrix. Do not describe unimplemented behavior as shipped.

- [ ] **Step 7: Implement the minimal interim behavior and documentation**

  Add only the approved observational note and cap enforcement, then document the incident recovery and safer viewing recipe at the granularity established by build2.

- [ ] **Step 8: Run GREEN and reviewer gates**

  Run the focused packages, `go test ./...`, `go vet ./...`, `shellcheck hack/*.sh`, and `golangci-lint run`. Commit the spec before or with its behavior, push, wait for the PR's own CI, and obtain a correctness/adversarial reviewer gate before PRs 2 or 3 start.

### Task 2: Build the runtime namespace, kernel claim, durable record, and versioned codec (PR 2)

**Files:**
- Create: `internal/shim/namespace.go`
- Create: `internal/shim/namespace_test.go`
- Create: `internal/shim/claim_darwin.go`
- Create: `internal/shim/claim_darwin_test.go`
- Create: `internal/shim/record.go`
- Create: `internal/shim/record_test.go`
- Create: `internal/shim/protocol.go`
- Create: `internal/shim/protocol_test.go`
- Modify: `SECURITY.md`

**Interfaces:**
- Produces: `shim.Namespace`, `shim.RolePath`, `shim.Claim`, `shim.Record`, `shim.Request`, `shim.Response`, and version-first encode/decode helpers.
- Produces: a kernel-derived lock-holder PID query used by self-target and socket-forgery checks.
- Consumes: Task 1's exact caps, runtime template, state vocabulary, protocol version, and ownership rules.

**Acceptance criteria:**
- Runtime creation is descriptor-verified, private, short by construction, and uses only validated components.
- Claim contention is kernel-arbitrated; stale socket cleanup happens only after claim acquisition; SIGKILL releases the claim without treating socket unlink/rebind as ownership evidence.
- Durable records are written atomically, never become liveness evidence, and contain the spec-approved child identity needed for orphan-safe refusal.
- Decode rejects missing/mismatched version before interpreting any other field and rejects unknown operations without passing text onward.
- The implementation uses only Task 1's reviewed `x/sys` surface and introduces no additional dependency.

- [ ] **Step 1: Write namespace and claim RED tests**

  Cover cap boundaries, worst-case socket length, pre-existing/symlinked/wrong-mode directories, descriptor substitution, two claimants, SIGKILL release, stale sockets, and lock-holder PID observation.

- [ ] **Step 2: Implement the smallest namespace and claim layer**

  Use `golang.org/x/sys/unix` only behind the Darwin claim implementation. Keep all filesystem mutation scoped to the validated runtime path and the exact claim held by this process.

- [ ] **Step 3: Write durable-record and codec RED tests**

  Cover partial writes, crash residue, malformed records, PID-reuse token mismatches, absent/foreign protocol versions, oversized frames, unknown fields, unknown operations, and attempts to encode payload text.

- [ ] **Step 4: Implement record and codec GREEN**

  Keep the public codec types incapable of representing arbitrary PTY input. Run `go test ./internal/shim -run 'Test.*(Namespace|Claim|Record|Protocol)' -count=1`.

- [ ] **Step 5: Re-verify dependency and archive evidence**

  Inspect `go mod graph`, run `go mod tidy` and require no unexpected diff, then run govulncheck and the snapshot archive check against Task 1's admitted dependency materials.

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
- Modify: `docs/security/2026-08-10-issue-182-shim-probe-evidence.md`
- Modify: `docs/superpowers/specs/2026-08-01-agentctl-design.md`

**Interfaces:**
- Produces: `ptyx.Opener`, `ptyx.ChildStarter`, `ptyx.Child`, `ptyx.Relay`, and a terminal-state observer used by Task 4.
- Consumes: validated argv and environment only; it has no session/role resolution, protocol parsing, registry lookup, or filesystem ownership.

**Acceptance criteria:**
- The child starts with the nested PTY as its controlling terminal and the parent observes its exact PID before readiness.
- Input/output relay is byte-preserving, resize and termios changes are forwarded, EOF/half-close behavior is bounded, and relayed terminal input enters the same serialized writer used later by control operations.
- The terminal observer distinguishes pre-ready cooked/echo state from the approved settled state without reading terminal contents.
- Teardown records signal attempts and observed child exit separately; a surviving child is returned as a typed outcome rather than reported dead.

- [ ] **Step 1: Write PTY-open and child-start RED tests**

  Cover syscall failure at every ordered setup point, controlling-terminal assignment, process-group ownership, exact argv/env, child PID capture, and cleanup of only resources created by the invocation.

- [ ] **Step 2: Implement PTY open and injected child start**

  Keep `os/exec` behind `ptyx.ChildStarter`; unit tests use a recording fake, while Darwin integration tests use a stub child in a throwaway PTY.

- [ ] **Step 3: Write relay/readiness/resize RED tests**

  Cover binary byte round trips, concurrent resize, early control attempts, simultaneous operator input and control bytes, context cancellation, parent input EOF, child exit, shim-side failure, and exactly one serialized PTY writer.

- [ ] **Step 4: Implement relay and terminal observation GREEN**

  Run `go test ./internal/ptyx -count=1` and `go test -race ./internal/ptyx -count=1`.

- [ ] **Step 5: Add throwaway live PTY evidence**

  Exercise the stub relay and both version-pinned harness startup transitions without tmux, append the observed facts to the evidence document, and run the PR's full unit/vet/lint gates.

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
- Modify: `internal/tmuxx/control.go`
- Modify: `internal/tmuxx/control_test.go`
- Modify: `docs/superpowers/specs/2026-08-01-agentctl-design.md`
- Modify: `SECURITY.md`

**Interfaces:**
- Produces: `shim.Server.Run`, `shim.Client.Observe`, `shim.Client.DeliverOperation`, and `shim.Client.Stop` with the exact typed outcomes approved in Task 1.
- Produces: a hidden, validated shim command that reconstructs child argv through `harness.AgentArgv`; it accepts no raw child command or payload.
- Consumes: Task 2's claim/record/protocol and Task 3's PTY/child interfaces.

**Acceptance criteria:**
- The server acquires ownership once, publishes no ready endpoint before PTY settle, and rolls back or records an orphan exactly as the amended ownership section requires.
- Every request validates protocol version, claimed session/role, lock-holder/socket-answerer identity, readiness, and registry membership before PTY mutation.
- The shim is the sole PTY writer. Relayed operator input and registry-resolved control bytes pass through one serialization point; registry lookup occurs server-side, and structural tests prove no client path can provide payload bytes.
- Response facts distinguish accepted request, bytes written, submit observed, cancellation residue, child exit, and orphan/cleanup outcomes without asserting harness execution.

- [ ] **Step 1: Write lifecycle and ownership RED tests**

  Table-drive every failure point from claim through settle, including listener failure, child-start failure, record failure, readiness timeout, shim cancellation, child refusal to exit, and cleanup failure. Assert ownership and mutation order.

- [ ] **Step 2: Write protocol-enforcement RED tests**

  Cover foreign/absent version, wrong role/session, forged answerer PID, pre-ready delivery, unknown operation, cancellation during delivery, concurrent clients, and socket replacement after connection.

- [ ] **Step 3: Implement the minimal server/client lifecycle**

  Keep status observation read-only, serialize PTY operations per role, and preserve the child record whenever its process may still be live.

- [ ] **Step 4: Add and wire the hidden shim command**

  Parse only the internal validated fields approved in §4, build harness argv through the existing registry, and keep it out of agent-facing help and the embedded skill command inventory.

- [ ] **Step 5: Run GREEN and adversarial review**

  Run `go test -race ./internal/shim ./internal/ptyx ./internal/control ./internal/harness ./cmd/agentctl -run 'Test.*Shim' -count=1`, then full unit/vet/lint gates. The PR review explicitly attacks raw-input reachability, claim races, pre-ready delivery, version skew, and orphan cleanup.

### Task 5: Cut fleet launch, relaunch, and kill over to shim lifecycle (PR 5, parallel with PR 6)

**Files:**
- Modify: `internal/fleet/fleet.go`
- Modify: `internal/fleet/fleet_test.go`
- Modify: `internal/fleet/relaunch.go`
- Modify: `internal/fleet/relaunch_test.go`
- Modify: `internal/kill/executor.go`
- Modify: `internal/kill/executor_test.go`
- Modify: `internal/preflight/preflight.go`
- Modify: `internal/preflight/preflight_test.go`
- Modify: `internal/tmuxx/tmux.go`
- Modify: `internal/tmuxx/tmux_test.go`
- Modify: `internal/shellq/shellq_test.go`
- Modify: `cmd/agentctl/integration_launch_test.go`
- Modify: `cmd/agentctl/integration_relaunch_test.go`
- Modify: `docs/superpowers/specs/2026-08-01-agentctl-design.md`
- Modify: `SECURITY.md`

**Interfaces:**
- Produces: shim-backed `fleet.Launch`, `fleet.Relaunch`, and `kill.Executor` results without changing `cmd/agentctl/main.go` in this PR.
- Consumes: Task 4's hidden shim argv and client lifecycle plus optional typed tmux presentation creation.

**Acceptance criteria:**
- tmux windows start the current agentctl executable's hidden shim command assembled at the sole shell-string site; harness argv never appears as a caller-controlled shim argument.
- Launch records the complete roster/config before roles can be reported missing, waits for each shim's approved readiness observation, and applies the amended ownership/rollback table exactly.
- Concurrent launch/relaunch claims have one kernel winner. Losing invocations create no second harness and remove nothing they do not own.
- Relaunch refuses every live, unsettled, forged, indeterminate, and orphan child state; it creates only after the approved absence observation.
- Kill quiesces operations, requests child termination, observes outcomes, and removes optional tmux presentation only under the approved prior-state rule.

- [ ] **Step 1: Replace launch transcript expectations with shim-start RED tests**

  Assert exact executable/argv element boundaries, `shellq` use, self-binary resolution, roster/config ordering, ready observation, per-role failures, and rollback calls.

- [ ] **Step 2: Implement shim-backed launch GREEN**

  Preserve optional tmux presentation creation behind typed `tmuxx` calls and keep runtime identity independent from returned pane/window IDs.

- [ ] **Step 3: Write relaunch and kill RED matrices**

  Cover absent, running, starting, orphan, forged-answerer, protocol-skew, stale-record, cleanup-failed, concurrent-contender, and tmux-presentation-gone cases. Assert no mutation before every non-destructive check completes.

- [ ] **Step 4: Implement relaunch and kill GREEN**

  Run `go test ./internal/fleet ./internal/kill ./internal/preflight ./internal/tmuxx -count=1`.

- [ ] **Step 5: Run throwaway-tmux lifecycle integration**

  Prove join/break/swap/move operations do not change shim identity or delivery, shim SIGKILL produces the approved child state, relaunch cannot double-launch, and every test uses the fixture socket and stub harnesses.

### Task 6: Cut control and status over to runtime observations and retire pane delivery (PR 6, parallel with PR 5)

**Files:**
- Modify: `internal/control/dispatcher.go`
- Modify: `internal/control/dispatcher_test.go`
- Modify: `internal/status/status.go`
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
- Modify: `cmd/agentctl/integration_lifecycle_test.go`
- Modify: `docs/superpowers/specs/2026-08-01-agentctl-design.md`
- Modify: `SECURITY.md`

**Interfaces:**
- Produces: an operation-name-only `control.Dispatcher` and runtime-backed `status.Collector` with optional presentation enrichment.
- Produces: a typed `tmuxx.ParentPID` process wrapper for Task 1's approved ancestry guard.
- Consumes: `shim.Client` observation/delivery interfaces and Task 1's full state/output contract.

**Acceptance criteria:**
- The CLI-to-dispatcher boundary carries operation name, session, and role only; the registry payload is resolved inside the shim server.
- The former target checks are explicitly classified in `SECURITY.md`: runtime validation or shim enforcement, presentation-only/moot, or retired with rationale linked to the approved spec.
- Self-target detection uses process ancestry and the verified lock-holder/socket-answerer PID, never `TMUX_PANE` or advisory role/session values.
- Status enumerates the approved runtime roster in both tmux and no-tmux cases, cross-checks claim holder against answerer, preserves build2's presentation note, and reports every state at the approved precedence.
- No production `send-keys` literal or exported payload-text delivery function remains.

- [ ] **Step 1: Write dispatcher and ancestry RED tests**

  Assert exact parent-PID argv and call order for self, sibling, human-terminal, disappeared-parent, loop, malformed-output, and tool-failure cases. Assert the dispatcher never receives or exposes payload text.

- [ ] **Step 2: Write status RED matrix**

  Cover every approved runtime state and precedence collision, missing records, live lock/no socket, socket/no lock, holder/answerer disagreement, protocol skew, orphan child, optional tmux unavailable, and merged presentation.

- [ ] **Step 3: Implement control/status GREEN**

  Keep observation read-only and delivery serialized by the shim. Run `go test ./internal/control ./internal/status ./internal/tmuxx -count=1`.

- [ ] **Step 4: Retire the tmux target/send-keys path**

  Delete `internal/target`, remove `tmuxx.DeliverPayload`, strengthen AST invariants to reject production `send-keys` and payload-bearing socket APIs, and update SECURITY.md's check disposition.

- [ ] **Step 5: Run race and layout integration**

  Run `go test -race ./internal/control ./internal/status ./internal/shim -count=1` and the focused real-tmux incident replay on a throwaway socket, proving role identity/delivery survive presentation rearrangement.

### Task 7: Converge CLI wiring, no-tmux foreground operation, attach, and operator docs (PR 7)

**Files:**
- Modify: `cmd/agentctl/main.go`
- Create: `cmd/agentctl/main_run_test.go`
- Modify: `cmd/agentctl/main_control_test.go`
- Modify: `cmd/agentctl/main_launch_test.go`
- Modify: `cmd/agentctl/main_relaunch_test.go`
- Modify: `cmd/agentctl/main_attach_test.go`
- Modify: `cmd/agentctl/main_test.go`
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
- Produces: final command wiring for shim-backed launch/control/status/relaunch/kill, the approved foreground `run` command, and tmux-only attach.
- Consumes: PR 5 lifecycle interfaces and PR 6 control/status interfaces without adding a second behavior path.

**Acceptance criteria:**
- The foreground path validates one role through `internal/config`, starts the same shim lifecycle used by tmux-backed launch, attaches its streams to the caller terminal, and creates no shell or viewer.
- Session resolution keeps `AGENTCTL_SESSION` as selection-only input; no advisory variable gates a role, child, socket, lock, or PTY action.
- Attach succeeds only for an observed tmux presentation of the runtime fleet and emits the approved no-presentation refusal otherwise.
- Help, README, examples, and exit rendering match the amended spec; the hidden shim entrypoint remains absent from agent-facing command inventories.
- The integration fixture supports both throwaway tmux presentation and isolated direct-terminal shim processes and always reaps owned children/runtime paths.

- [ ] **Step 1: Write final CLI dispatch/exit RED tests**

  Cover every shim result class, explicit/ambient session selection, JSON/table status, foreground run validation and signal forwarding, no-tmux attach, and unchanged `version`/`skill` behavior.

- [ ] **Step 2: Wire shared dependencies once**

  Construct one runtime namespace/client and inject it into lifecycle, control, status, attach, and run paths. Do not duplicate resolution or error mapping between tmux and no-tmux modes.

- [ ] **Step 3: Implement foreground run and attach behavior**

  Reuse the Task 4 server lifecycle directly, keep presentation optional, and render only outcomes observed by the final command.

- [ ] **Step 4: Update operator documentation and integration fixtures**

  Document the flag-day transition, runtime files and cleanup, no-tmux composition, attach limitation, orphan remedy, layout-proof controls, interim merged-layout note, and factual-delivery limitation.

- [ ] **Step 5: Run CLI and integration GREEN**

  Run `go test ./cmd/agentctl ./internal/attach ./internal/session -count=1` and `go test -tags integration ./cmd/agentctl -count=1`.

### Task 8: Update release verification, embedded skill, and complete 0.5.0 evidence (PR 8)

**Files:**
- Modify: `hack/release-verify.sh`
- Modify: `hack/releaseverify_test.go`
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
- Produces: version-pinned live evidence for PTY startup, clear/compact delivery, layout rearrangement, shim crash/orphan handling, version skew, no-tmux status, and attach refusal.
- Produces: embedded-skill state/exit inventories paired to the 0.5.0 binary.
- Consumes: build2's incident replay, Task 1/3 probe evidence, and the complete merged implementation.

**Acceptance criteria:**
- The live verifier uses only its owned named tmux server, runtime root, temporary HOME, and stub/consented harness processes; teardown observes absence of every owned child, socket, lock, PTY helper, tmux server, and credential fixture.
- Verification proves the four layout operations leave identity and delivery intact, early delivery refuses until ready, a forged answerer is reported, version skew fails closed, and no orphan can be called missing or relaunched beside a survivor.
- Release archives contain the `x/sys` license and all prior license materials.
- The embedded skill exposes only the approved agent-facing surface and matches `status.States()` plus CLI exit constants through drift tests.

- [ ] **Step 1: Write release-verifier fixture RED tests**

  Add deterministic stubs for every new checkpoint and cleanup path, including interruption at each phase and surviving-child simulation.

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
- [ ] The planner has ruled on all three PROPOSED §7 resolutions before affected code.
- [ ] build2's incident-replay report is consumed by Task 1 and cited by the interim recovery guidance.
- [ ] Parallel PRs share no edited production files; convergence happens only after both dependencies land.
- [ ] Each behavior PR preserves RED and GREEN commands/results in its PR body.
- [ ] Every integration test uses a throwaway tmux socket or an isolated no-tmux runtime root and reaps what it owns.
- [ ] Every PR is rebased on current `main`, reruns the relevant gates, receives fresh PR CI, and passes a reviewer gate.
