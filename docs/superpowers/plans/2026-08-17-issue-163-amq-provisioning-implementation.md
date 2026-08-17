# Issue #163 Launch-time AMQ Provisioning Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use `superpowers:subagent-driven-development` for Tasks 1A and 1B, then `superpowers:executing-plans` for Tasks 2–5. Keep the checkbox steps as the execution record.

**Goal:** Make `agentctl launch` establish or read-only adopt the declared session mailbox shape before any role or presentation starts, while leaving foreground `run` behavior unchanged and reporting only facts AMQ exposes.

**Architecture:** A new standard-library-only `internal/amqx` package owns the no-shell AMQ process boundary, stable JSON decoding, cwd/environment control, and exact exit observations. `internal/fleet` owns a pre-runtime launch phase because it combines AMQ observations with descriptor-verified durable fleet records; its provisioner commits or reuses the fleet record before handing a provenance-bearing record to the existing launcher. Dependency construction may open session-agnostic per-UID roots and containers, but provisioning refusal leaves no session-keyed agentctl artifact. The launcher then retains its present runtime, readiness, and rollback responsibilities. The CLI adds only the launch-scoped operator assertion and renders the spec's closed outcomes.

**Tech Stack:** Go 1.26.6, `os/exec`, `encoding/json`, the existing descriptor-relative fleet-record store, fake runners/records/lifecycles, integration helper binaries, throwaway AMQ/state/runtime roots, shellcheck 0.11.0, golangci-lint 2.12.2, and the existing release-verification harness.

## Authoritative inputs

- Issue [#163](https://github.com/mnbf9rca/agentctl/issues/163), including all three `AMENDED 2026-08-17` sections and especially the final post-#247 implementation-phase rulings, is the complete work contract. The later amendments supersede the earlier one where they differ. The final amendment was verified live at `updatedAt: 2026-08-17T13:02:02Z` before this plan was committed.
- Approved design spec §§15.8 and 15.12 are normative for every state, precedence, command shape, output, exit, mutation boundary, verification claim, and release obligation.
- `docs/superpowers/plans/2026-08-17-issue-163-amq-provisioning-joint-proposal.md` is the evidence source and contains the parked `SECURITY.md` text. It is non-normative where the merged spec differs.
- `docs/superpowers/plans/README.md` forbids duplicating normative strings and state tables here. Implementers must read the cited rows directly.
- `SECURITY.md`, `docs/release-checklist.md`, and `.github/workflows/ci.yml` govern security, live verification, and CI evidence.
- The final issue amendment records the probe-backed absent-base classification and the target-session-attributable ordering boundary. Task 1A turns the former into a repeatable boundary test; Task 4 protects the latter with the real production openers. A changed AMQ floor requires re-probing rather than assuming either observation remains stable.

## Approved design delta that rides the product PR

The final issue amendment is the ratified delta to merged §§15.2 and 15.12. It closes the ownership-provenance vocabulary and serialization, legacy classification, initial writes and one-way transitions, partition/refusal/guard consequences, launch-only operator consequence, target-session ordering boundary, AMQ probe method, and SECURITY obligation. Do not reconstruct those semantics from AMQ messages or temporary worktree artifacts.

This delta is not permission to redesign `run`. Task 1B implements the record vocabulary, Task 2 implements its state-partition consequences, and Task 4 applies the issue's bundled spec/security/release edits in the same atomic product PR.

## Non-negotiable implementation rules

- Do not modify AMQ, invoke a shell, add a third-party module, pass `--force` or `--strict`, or write directly inside an AMQ tree.
- Provisioning belongs only to `launch`. `run` performs no new AMQ call, emits no §15.12 success claim, and keeps the deprecated implicit-create dependency documented in §15.12.6.
- Run provisioning after all launch value/executable/directory validation and before fleet-record creation, tmux, detached spawning, or runtime mutation. Exact event order is asserted with fakes.
- For §15.12.2 ordering, per-UID session-agnostic roots and containers are infrastructure, while every session-keyed record, directory, socket, process, or presentation is a mutation. A provisioning refusal leaves no session-keyed artifact and requires zero cleanup.
- Every AMQ call uses the closed argv and environment rules of §15.12.2. Tests assert executable, argv element boundaries, directory, cleared variables, retained unrelated environment, call order, stdout, stderr, and status separately.
- Probe rather than reason about AMQ behavior. Any implementation claim about a command's status, output, validation, or filesystem effect needs a recorded throwaway-root execution under the explicit root/environment conditions in the final issue amendment.
- Exit status is an observation, never the post-state. Preserve the §15.12.5 phase distinction and always perform the required re-observation after a create attempt.
- The durable-record decoder remains strict for ordinary readers. Only the provisioning read classifies a version-1 record whose sole defect is the missing provenance member as pre-provenance.
- Existing AMQ folders are read-only. Adoption, including `--adopt`, must issue no mutating AMQ command and must preserve queued message bytes.
- Existing fleet records survive launch rollback. A record created by the current launch may be removed only under the existing observed-cleanup rules; a reused or provenance-migrated record is retained.
- Preserve extras and every base configuration/mailbox/metadata surface named by §15.12. Tests compare bytes and types, not only filenames.
- Success output, tests, docs, and release notes use only §15.12.1's closed claim. Add a guard against the stronger vocabulary forbidden there.
- Every behavior begins RED with a focused test, receives the smallest GREEN implementation, and is committed in focused SSH-signed commits.

## Delivery and parallel-work graph

All tasks converge into one atomic implementation PR so `SECURITY.md` never claims behavior before the behavior exists. Tasks 1A and 1B are the only parallel implementation lanes. They begin from the same current `main`, use separate worktrees, and do not publish standalone PRs.

| Lane/task | Owner | Scope while parallel | Depends on |
|---|---|---|---|
| Task 1A | build1 | new `internal/amqx/` only | current `main` |
| Task 1B | build2 | fleet-record codec/store plus foreground provenance only | current `main`; final issue amendment |
| Task 2 | build1 after convergence | fleet provisioning state machine and launcher integration | 1A + 1B |
| Task 3 | build1 | CLI parsing/rendering and production wiring | 2 |
| Task 4 | build1, then build2 adversarial review | integration, structural guards, spec/security/operator/release docs | 3 + final issue amendment |
| Task 5 | build1 | full gates, signed publication, reviewer handoff | 4 + build2 review |

```text
current main ──┬── Task 1A (build1: AMQ boundary) ──┐
              └── Task 1B (build2: records) ───────┴── Task 2 ── Task 3 ── Task 4 ── Task 5
```

Before each lane, invoke `superpowers:using-git-worktrees`, fetch current `main`, verify the worktree base, and prove the 1Password SSH-signing path as required by `AGENTS.md`. The converging owner cherry-picks or merges only reviewed signed lane commits, resolves no semantic disagreement silently, and reruns focused tests after integration.

## Contract coverage map

| Contract area | Owning tasks |
|---|---|
| §15.12.1 closed success claim and vocabulary | Tasks 3–4 |
| §15.12.2 process boundary, environment, order, and launch-only scope | Tasks 1A, 2–4 |
| §15.12.3 complete state partition and subset predicate | Task 2 |
| §15.12.4 ownership, adoption, provenance, and migration | Tasks 1B–2; delta in Task 4 |
| §15.12.5 typed refusals and phased observation | Tasks 1A, 2–3 |
| §15.12.6 accepted AMQ limitations | Tasks 3–4 |
| §15.12.7 required guards and base surfaces | Tasks 1A–4 |
| §15.12.8 release and security obligations | Task 4 |
| §15.8 launch outcome register | Task 3 |

---

## Task 1A: Build the typed AMQ command and observation boundary (parallel build1 lane)

**Files:**

- Create: `internal/amqx/runner.go`
- Create: `internal/amqx/runner_test.go`
- Create: `internal/amqx/client.go`
- Create: `internal/amqx/client_test.go`

**Interfaces:**

```go
type Invocation struct {
    Arguments   []string
    Directory   string
    Environment []string
}

type Result struct {
    Stdout   []byte
    Stderr   []byte
    ExitCode int
}

type Runner interface {
    Run(context.Context, string, Invocation) (Result, error)
}

type Client struct { /* injected Runner and environment source */ }

func (Client) ResolveBase(context.Context, string) (string, error)
func (Client) ListSessions(context.Context, string) (SessionList, Result, error)
func (Client) InitSession(context.Context, string, string, []string) (Result, error)
```

`Runner` distinguishes a started process's nonzero exit from failure to start or observe the process. `Client` owns only command construction, environment sanitization, stable JSON decoding, defensive copies, and documented AMQ result types; it knows nothing about fleet records or launch decisions.

- [ ] **Step 1: Add runner ownership/status tests and capture RED**

  Cover exact executable and argv capture, cwd, full environment replacement, independent stdout/stderr, a nonzero child status returned as a `Result`, context cancellation, spawn failure, and defensive slice ownership.

- [ ] **Step 2: Implement the no-shell runner and run focused GREEN**

  Use `exec.CommandContext` directly. Convert `*exec.ExitError` into a factual `Result`; return other execution errors without fabricating a status.

- [ ] **Step 3: Add client command/environment tests and capture RED**

  Assert every §15.12.2 command as exact argv elements. For root resolution, seed all six prohibited AMQ variables plus unrelated values and prove only the named variables are absent from the invoked environment. Pin role order and CSV construction from already-validated input without re-parsing or shell quoting.

- [ ] **Step 4: Add strict JSON and status-classification tests and capture RED**

  Cover the documented `env --json` and `session list --json` members, unknown/duplicate/missing members as required by the upstream stable contract, malformed/trailing JSON, duplicate session entries, unsafe paths, nonzero statuses, and the probed absent-base status. Preserve output and status for the fleet layer instead of selecting agentctl outcomes here.

- [ ] **Step 5: Implement the client and run focused GREEN**

  Decode only the upstream stable fields needed by §15.12, return sorted/copy-owned observations, and leave state precedence to `internal/fleet`.

- [ ] **Step 6: Verify and hand the signed lane commit to the convergence owner**

  Run `go test ./internal/amqx -count=1`, `go vet ./internal/amqx`, and `git diff --check`. Commit signed. Do not edit fleet, CLI, docs, module files, or AMQ in this lane.

## Task 1B: Extend strict fleet records with factual ownership provenance (parallel build2 lane)

**Files:**

- Modify: `internal/fleet/shim_record.go`
- Modify: `internal/fleet/shim_test.go`
- Modify: `internal/fleet/foreground.go`
- Modify: `internal/fleet/foreground_test.go`
- Modify mechanical constructor fixtures in `internal/kill/shim_executor_test.go` and `cmd/agentctl/integration_shim_lifecycle_test.go` only if the chosen constructor API requires it

**Interfaces:**

```go
type OwnershipProvenance string

type ShimFleetRecordObservation struct {
    Record              ShimFleetRecord
    PreProvenanceRecord bool
}

type AMQProvisioningRecords interface {
    Create(ShimFleetRecord) error
    ReadForAMQProvisioning(string) (ShimFleetRecordObservation, error)
    EstablishOwnership(ShimFleetRecordObservation, OwnershipProvenance) (ShimFleetRecord, error)
}

func (s *ShimFleetRecordStore) ReadForAMQProvisioning(string) (ShimFleetRecordObservation, error)
func (s *ShimFleetRecordStore) EstablishOwnership(ShimFleetRecordObservation, OwnershipProvenance) (ShimFleetRecord, error)
```

The ordinary `Read` remains current-schema strict. `ReadForAMQProvisioning` shares the descriptor, size, owner, mode, duplicate-field, session-binding, and value checks, but exposes only the single structural legacy class defined by §15.12.4. `EstablishOwnership` is a version-checked, mutation-flocked transition; it cannot become a general record-repair API.

- [ ] **Step 1: Add current-schema provenance codec tests and capture RED**

  Cover all approved values including `unclaimed`, the ratified canonical top-level writer order, deterministic JSON independent of incidental struct layout, unknown or missing values, duplicates, unknown members, wrongly typed values, and exact preservation across ordinary replace/extend paths. The Go zero value is not a valid provenance: canonical writers emit one nonempty closed value, and a present empty value is a strict schema error rather than an alias for any value.

- [ ] **Step 2: Implement the current schema and make new non-provisioning records factual**

  Keep schema version 1, make `NewShimFleetRecord` produce a valid `unclaimed` record, and prove foreground `run` writes and extends records without issuing or implying any AMQ observation.

- [ ] **Step 3: Add pre-provenance classification tests and capture RED**

  Use table cases for the sole missing-member exception and every near miss required by §15.12.4: present empty provenance, missing another member, unknown member, invalid or wrongly typed value, wrong session, wrong version, unsafe file/type/owner/mode, and trailing data. Detect field presence before decoding into the Go value so only an actually absent provenance member can reach the legacy classifier. Prove ordinary `Read` still rejects the legacy shape.

- [ ] **Step 4: Implement the narrow provisioning read**

  Refactor parsing only enough to share strict checks. Return the structural class independently of folder presence, roster equality, or mailbox shape.

- [ ] **Step 5: Add one-way establishment tests and capture RED**

  Assert the complete transition rules from the approved delta, concurrent-record replacement detection, mutation-flock contention, atomic-write uncertainty, and preservation of session, directory, presentation, roster, and role configuration. Prove no generic replacement silently rewrites provenance.

- [ ] **Step 6: Implement establishment and run focused GREEN**

  Reuse the descriptor-relative, version-checked atomic writer. Do not add any filesystem access to an AMQ tree.

- [ ] **Step 7: Verify and hand the signed lane commit to the convergence owner**

  Run `go test ./internal/fleet ./internal/kill -count=1`, `go vet ./internal/fleet ./internal/kill`, and `git diff --check`. Commit signed. Do not edit `internal/amqx`, `internal/fleet/shim.go`, CLI, docs, or AMQ in this lane.

## Task 2: Implement the complete provisioning state machine and launch ordering

**Files:**

- Create: `internal/fleet/amq_provisioning.go`
- Create: `internal/fleet/amq_provisioning_test.go`
- Modify: `internal/fleet/shim.go`
- Modify: `internal/fleet/shim_test.go`
- Modify: `internal/fleet/shim_relaunch.go` and tests only for constructor dependency plumbing; relaunch behavior remains unchanged
- Modify: `internal/fleet/foreground.go` and tests only to keep the AMQ provisioner out of `run`

**Interfaces:**

```go
type AMQClient interface {
    ResolveBase(context.Context, string) (string, error)
    ListSessions(context.Context, string) (amqx.SessionList, amqx.Result, error)
    InitSession(context.Context, string, string, []string) (amqx.Result, error)
}

type AMQProvisionRequest struct {
    Record ShimFleetRecord
    Adopt  bool
}

type AMQProvisionResult struct {
    Record        ShimFleetRecord
    RecordCreated bool
}

type AMQProvisioner interface {
    Provision(context.Context, AMQProvisionRequest) (AMQProvisionResult, error)
}
```

The concrete provisioner combines `AMQClient` with the provisioning record-store interface. Its result tells rollback whether this invocation created the durable record. Typed errors carry structured facts; CLI text and exit selection remain in Task 3.

- [ ] **Step 1: Add the ordered partition table and capture RED**

  Drive every first-match rule in §15.12.3 plus the final issue amendment with fakes for AMQ and records, including every `unclaimed` cell. Add explicit precedence cases for malformed records, absent folders with binding records, roster disagreement versus shape, every record class versus shape failure, surplus handles, and later `user` presence.

- [ ] **Step 2: Implement read-only resolution and selection through every no-create state**

  Resolve from the stored/requested project directory, classify the base and listing facts, compare normalized declared and recorded rosters, compute missing handles in declaration order, and return typed outcomes without AMQ mutation.

- [ ] **Step 3: Add creation/re-observation phase tests and capture RED**

  Cover successful creation, nonzero creation with each re-observation class, zero status with an incomplete shape, failed re-observation, and concurrent/partial directory appearance. Assert exact call order and that only the §15.12.3 creation cell invokes `InitSession`.

- [ ] **Step 4: Implement create-whole and mandatory re-observation**

  Select the outcome from both the command result and the observed post-state per §15.12.5. Never convert a nonzero init to success, and never infer complete shape from zero.

- [ ] **Step 5: Add record-commit/adoption tests and capture RED**

  Prove new sessions commit evidence-derived provenance only after successful re-observation; operator adoption commits the asserted value; evidence-based adoption is record- and AMQ-read-only; existing asserted history is preserved; pre-provenance and `unclaimed` migrate only through the approved assertion; absent or contradicted evidence refuses as specified.

- [ ] **Step 6: Implement record commit/reuse and concurrency handling**

  Handle a concurrent record creation by re-reading and re-running the state selection rather than overwriting or assuming ownership. Return whether the current invocation created the record.

- [ ] **Step 7: Add launcher order, refusal-artifact, and rollback-ownership tests and capture RED**

  Assert validation, AMQ calls, record commit, detached/tmux start, and readiness in exact order. For every provisioning refusal, permit only the ruled session-agnostic infrastructure and prove the target has no durable session entry, runtime session directory/socket, shim process, or tmux artifact. For launch failure after pre-existing adoption or provenance migration, prove rollback retains the pre-existing record; retain the existing cleanup behavior for a record created by this invocation.

- [ ] **Step 8: Integrate the provisioner into `ShimLauncher` and run focused GREEN**

  Carry `Adopt` through a typed launch request/options value. Keep relaunch and foreground constructors compile-safe without routing those operations through provisioning.

- [ ] **Step 9: Run focused package verification**

  Run `go test ./internal/amqx ./internal/fleet -count=1`, `go test -race ./internal/amqx ./internal/fleet`, `go vet ./internal/amqx ./internal/fleet`, and `git diff --check`.

## Task 3: Wire launch-only CLI behavior and the closed factual outputs

**Files:**

- Modify: `cmd/agentctl/main.go`
- Modify: `cmd/agentctl/main_launch_test.go`
- Modify: `cmd/agentctl/main_launch_template_test.go`
- Modify: `cmd/agentctl/launch_template_test.go` only to prove the assertion flag is CLI-only
- Modify: `cmd/agentctl/runtime_dependencies.go`
- Modify: `cmd/agentctl/skill_launch_notice_test.go`
- Modify: `cmd/agentctl/skill_contract_test.go`
- Modify: `README.md`
- Modify: `skills/agentctl/SKILL.md`
- Modify: `skills/agentctl/references/exit-codes.md`

**Interfaces:**

- Add `Adopt bool` to the launch request/options path and the `shimFleetLauncher` seam.
- Construct the production `amqx.RealRunner`, `amqx.Client`, and fleet provisioner in `buildShimDependencies`; inject none of them into `productionForegroundExecutor`.
- Add typed CLI renderers for the §15.8 AMQ rows. Render from structured error fields rather than parsing AMQ stderr.

- [ ] **Step 1: Add launch flag boundary tests and capture RED**

  Prove `--adopt` is accepted by `launch`, rejected by other commands, not serialized into templates, not inferred from templates, and passed exactly once through the launcher seam.

- [ ] **Step 2: Implement the smallest launch-only parsing/request change**

  Keep the flag argument-free and operator-explicit. Do not add a run flag, environment shortcut, template member, or implicit adoption path.

- [ ] **Step 3: Add exact outcome-rendering tests and capture RED**

  Assert every new §15.8 row byte-for-byte, remedy admissibility/order, declaration-order handle rendering, observation/status fields, stdout/stderr routing, and exit selection. Use spec fixtures/helpers rather than duplicating production prose across many tests.

- [ ] **Step 4: Implement typed rendering and final success composition**

  Append §15.12.1's final line only after the launcher reports success, for both presentations. Preserve launch-template provenance output and skill notices without letting either emit the AMQ claim early.

- [ ] **Step 5: Add the launch-only and vocabulary guards**

  Add a test where `run` succeeds with a dependency that would fail if any AMQ provisioning method were called, and assert its output lacks the §15.12 claim. Add a source/output guard for the stronger success vocabulary forbidden by §15.12.1.

- [ ] **Step 6: Wire production dependencies and update public/operator documentation**

  Keep the command boundary injected and standard-library-only. Update README quickstarts, command reference, existing-fleet guidance, and the embedded skill/exit reference for the launch assertion and new outcomes. Preserve the closed success vocabulary, do not promise strict delivery authority, and do not change foreground guidance.

- [ ] **Step 7: Run focused CLI and skill verification**

  Run `go test ./cmd/agentctl ./skills -count=1`, `go vet ./cmd/agentctl ./skills`, the skill-pairing script against the task branch's merge base and `HEAD`, and `git diff --check`.

## Task 4: Prove real composition, base-surface preservation, and operator truth

**Files:**

- Create: `cmd/agentctl/integration_amq_provisioning_test.go`
- Modify: `cmd/agentctl/integration_fixture_test.go`
- Modify: `cmd/agentctl/integration_shim_lifecycle_test.go`
- Modify: `internal/structural/invariants_test.go`
- Modify: `hack/release-verify.sh`
- Modify: `hack/releaseverify_test.go`
- Modify: `docs/release-checklist.md`
- Create: `docs/releases/0.5.1.md`
- Modify: `hack/releasenotes_test.go`
- Modify: `SECURITY.md`
- Modify: `hack/securitydoc_test.go` only if a current-truth/presence guard is needed; retain the word budget
- Modify: `docs/superpowers/specs/2026-08-01-agentctl-design.md` for the concurred version-1 schema and `unclaimed` delta only

- [ ] **Step 1: Extend the integration AMQ helper and capture RED**

  Make the helper support the three closed §15.12.2 commands plus existing `coop exec`, record exact argv/cwd/environment, and model session directory discovery in a throwaway tree. It must fail if a role reaches `coop exec` before its mailbox directory exists.

- [ ] **Step 2: Add end-to-end launch creation and adoption tests**

  Cover tmux and detached success, existing-session read-only adoption, `--adopt`, queued-message byte preservation, extras, missing roles, malformed/unowned records, nonzero/partial init, and the closed final output. Drive a refusal through the real production namespace/store openers and prove they create only the permitted shared infrastructure: no target-session state directory/record, runtime artifact, process, or tmux object exists at provisioning time or after refusal. Assert no implicit-create warning is emitted after launch provisioning.

- [ ] **Step 3: Add structural guards for the closed surface**

  Pin the three-command production boundary, the prohibited flags, the launch-only dependency edge, the exact provenance member/order and complete vocabulary/transitions, preservation across run extension and relaunch, the pre-provenance exception, every `unclaimed` partition cell, exact refusal evidence, and the success-claim vocabulary from §15.12.7. The guard should fail on a fourth AMQ command, a rewrite to `unclaimed`, or a second writer into an AMQ tree.

- [ ] **Step 4: Add base-surface snapshot tests and live-verifier logic**

  Snapshot the base configuration, mailbox tree, and metadata tree before/after creation and adoption, including types, modes, names, and bytes. Assert the session child is the only permitted base-level difference. Extend release-verifier fixture tests for success, mutation, substitution, cleanup, and interrupted-run paths before changing the script.

- [ ] **Step 5: Implement the release-verifier/checklist updates**

  Keep all probes on throwaway AMQ roots and preserve the existing identity-gated cleanup. Record the installed AMQ version, repeat the absent-base status probe, exercise default-send warning compatibility without `--strict`, and capture the §15.12 release evidence in the checklist.

- [ ] **Step 6: Apply the concurred spec delta and parked security text**

  Edit only the version-1 schema and sections required to make the provenance vocabulary, initial writes, one-way transitions, partition, refusal evidence, guards, release/operator language, and §15.12.2's session-scoped mutation boundary total. Pin the agreed JSON member name and deterministic writer order in §15.2 rather than leaving them as a struct-layout accident. Apply the joint proposal's parked `SECURITY.md` clauses, compress elsewhere without weakening the existing three-confirmation/exhaustive release qualifiers, and keep `hack/securitydoc_test.go` under its current word budget.

- [ ] **Step 7: Add the 0.5.1 release-obligation source and tests**

  Create the versioned draft-note block from §15.12.8 and test exact extraction against that section without changing `VERSION` or publishing a release. Include the launch-only consequence that a fleet first recorded by `run` has made no AMQ ownership claim and may require one explicit adoption on a later launch.

- [ ] **Step 8: Self-review the complete contract coverage**

  Compare the implementation/tests against every §15.12.7 bullet and the two later issue amendments. Run `rg -n 'TBD|TODO|FIXME|placeholder|registered|authorized|strict-routable'` over changed production/docs/test fixtures and inspect each hit in context. Confirm the older extend-existing-root mechanism did not reappear.

- [ ] **Step 9: Run integration, script, security, and release-note GREEN**

  Run `go test -tags integration ./cmd/agentctl -count=1`, `go test ./internal/structural ./hack -count=1`, `shellcheck hack/*.sh`, and `git diff --check`.

- [ ] **Step 10: Request adversarial build2 review before opening the PR**

  Send the commit and focused evidence to build2 on `design/issue-163`, copying planner. Require an explicit blocking/non-blocking verdict covering the state partition, provenance transitions, launch-only boundary, base surfaces, output vocabulary, and spec/security alignment. Resolve every finding in this branch and rerun its owning tests.

## Task 5: Rebase, verify, publish, and hand off without merging

**Files:**

- No planned product files; only fixes required by review or fresh verification
- The implementation PR body records issue relationship, contract impact, RED/GREEN evidence, and verification

- [ ] **Step 1: Fetch and rebase onto current `main`**

  Confirm a clean worktree, fetch `origin main`, rebase the signed topic branch, inspect the complete diff from the new merge base, and rerun focused tests for every resolved conflict.

- [ ] **Step 2: Run the repository and CI-equivalent gates**

  Run at minimum:

  ```bash
  go test ./...
  go vet ./...
  go test -race ./...
  go test -tags integration ./...
  shellcheck hack/*.sh
  golangci-lint run
  goreleaser check
  goreleaser release --snapshot --clean --skip=notarize
  ./hack/verify-release-archives.sh dist/*.tar.gz
  ```

  Also run the workflow contract scripts named by `.github/workflows/ci.yml`. Record each command and exit status; do not summarize a skipped gate as passing.

- [ ] **Step 3: Run the required live AMQ verification on isolated state**

  Execute the amended release verifier with throwaway roots and authenticated harnesses per `docs/release-checklist.md`. Capture the AMQ version, closed launch output, base-surface comparison, default-send warning behavior, adoption mail preservation, teardown/absence observations, and artifact hashes. Never point a test at the user's default tmux server or live agent fleet.

- [ ] **Step 4: Commit, push, and open one atomic implementation PR**

  Ensure every commit is SSH-signed, push through the approval path, and open a PR that `Closes #163`. Include the design-delta concurrence IDs, the base-absent probe evidence, SECURITY/release obligations, and the build2 verdict. Do not include unrelated cleanup.

- [ ] **Step 5: Wait for the PR's own merge-result CI and obtain reviewer release**

  Quote the exact `pull_request` run URL, not a branch run. Address all findings in the same PR, rerun affected local gates and CI, obtain the reviewer gate, and leave merge to the planner or maintainer.

- [ ] **Step 6: Detach the PR worktree and report the handoff**

  Detach the worktree so the branch is not held at merge time. Report the PR, signed commits, CI run, live evidence location, reviewer/build2 verdicts, and any accepted limitation from §15.12.6.

## Final acceptance checklist

- [ ] Every §15.12.3 state selects exactly one specified outcome, including current `unclaimed` records.
- [ ] Only the create-whole cell invokes AMQ mutation, and every create attempt is re-observed.
- [ ] Adoption is AMQ-read-only; queued messages and extras are preserved byte-for-byte.
- [ ] Base configuration, mailboxes, and metadata are unchanged across creation and adoption.
- [ ] `run` has no new AMQ call, flag, output claim, or dependency.
- [ ] Ordinary record reads are strict; only the sole missing-provenance legacy shape reaches the migration classifier.
- [ ] Exact external argv/environment/cwd and internal record/runtime call order are fake-asserted.
- [ ] Provisioning refusal leaves no target-session durable/runtime/tmux artifact and requires zero cleanup.
- [ ] Launch success emits the one closed discovery sentence only after success; stronger vocabulary is guarded out.
- [ ] The spec delta, `SECURITY.md`, embedded skill, release note source, and release checklist agree with shipped behavior.
- [ ] Focused, full, race, integration, lint, snapshot, live, CI, build2, and reviewer evidence are all recorded before handoff.
