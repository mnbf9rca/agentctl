# Issue #163 Launch-time AMQ Provisioning Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement issue #163's amended `launch` provisioning, ownership, and reporting contract while preserving the registered foreground `run` contract.

**Architecture:** First make the approved spec total for the final issue amendments and obtain reviewer agreement on that exact spec commit. Then extend the existing `internal/tmuxx.Runner` process boundary, build a standard-library-only `internal/amqx` client on it, and implement the amended §15.12 workflow in `internal/fleet`; CLI code remains responsible for launch-only parsing and registered rendering.

**Tech Stack:** Go 1.26.6, standard library, existing descriptor-relative state stores and fake runners, integration-tagged throwaway AMQ/tmux fixtures, shellcheck 0.11.0, golangci-lint 2.12.2, and the repository release verifier.

## Global Constraints

- Issue [#163](https://github.com/mnbf9rca/agentctl/issues/163), verified live at `updatedAt: 2026-08-17T13:29:37Z`, including its frozen five-bullet plan-review amendment, is the complete contract.
- Approved spec §§15.2, 15.8, 15.9, and 15.12 govern behavior; this plan cites them rather than duplicating their command forms, state precedence, output strings, or exit meanings.
- `SECURITY.md`, `docs/release-checklist.md`, `.github/workflows/ci.yml`, and `docs/superpowers/plans/README.md` remain binding.
- No AMQ source change, shell invocation, caller-controlled process boundary, or third-party Go dependency is permitted.
- Production AMQ execution must use `internal/tmuxx.Runner`; tests assert exact executable, argv element boundaries, cwd, environment, output streams, termination, and call order.
- Every behavior change begins with a focused failing test, receives the smallest passing implementation, and ends in a focused SSH-signed commit.
- The spec and implementation remain one atomic product PR. The reviewed spec commit precedes the two file-disjoint implementation lanes; no lane publishes a standalone PR.

---

## Delivery graph and ownership

| Task | Owner | Files while parallel | Dependency |
|---|---|---|---|
| 0 | planner/build1, reviewer gate | approved design spec only | current `main` |
| 1A | build1 | `internal/tmuxx`, new `internal/amqx` | reviewed Task 0 commit |
| 1B | build2 | fleet record codec/store and foreground provenance | reviewed Task 0 commit |
| 2 | build1 after convergence | fleet preparation/provisioning/launcher | 1A + 1B |
| 3 | build1 | CLI provenance, parsing, rendering, wiring | 2 |
| 4 | build1 | integration, guards, security/release docs | 3 |
| 5 | build1 | rebase, gates, publication, reviewer handoff | 4 |

Before Task 0 and each implementation lane, invoke `superpowers:using-git-worktrees`, verify the exact required base commit, and prove SSH signing using the repository procedure. The convergence owner accepts only reviewed signed commits and reruns each lane's focused tests after integration.

## Contract coverage

| Contract surface | Owning task |
|---|---|
| Exact amended contract in §§15.2, 15.8, 15.9, and 15.12 | 0 |
| Process and AMQ observation boundary (§§15.9, 15.12.2) | 1A |
| Current, legacy, reservation, and ownership record states (§§15.2, 15.12.3–4; final issue amendments) | 1B |
| Complete partition, ordering, record refresh, races, rollback (§§15.12.2–7) | 2 |
| Launch-only flag, provenance, typed output (§§15.8, 15.12.1, 15.12.5–6) | 3 |
| Real composition, security, release, and every required guard (§15.12.7–8) | 4 |
| Current-main and merge-result evidence | 5 |

---

### Task 0: Land the reviewed exact spec delta before production TDD

**Files:**

- Modify: `docs/superpowers/specs/2026-08-01-agentctl-design.md`

**Produces:** A signed spec-only commit making §§15.2, 15.8, 15.9, and every affected §15.12 subsection total for issue #163; Tasks 1A–4 consume its exact rows and templates.

- [ ] **Step 1: Draft the complete spec delta from the frozen issue**

  Apply every final issue amendment to the named sections, including all registered outcomes, substitutions, streams, exits, process facts, record facts, guards, and release obligations. Do not edit production code, `SECURITY.md`, public docs, or release notes in this commit.

- [ ] **Step 2: Check the spec-only diff**

  Run `git diff --check`, inspect `git diff -- docs/superpowers/specs/2026-08-01-agentctl-design.md`, and map each frozen issue clause to one exact spec location.

- [ ] **Step 3: Run repository regression checks**

  Run `go test ./...` and `go vet ./...`; report any test that encodes the superseded contract instead of weakening the proposed spec.

- [ ] **Step 4: Commit and obtain reviewer agreement**

  Create one SSH-signed spec-only commit, verify its signature, and send its exact hash plus issue timestamp and clause-to-section map to reviewer and planner. Do not start Task 1A or 1B until reviewer explicitly agrees that exact commit is the approved design delta.

---

### Task 1A: Extend the typed process runner and add the AMQ client (parallel build1 lane)

**Files:**

- Modify: `internal/tmuxx/runner.go`
- Modify: `internal/tmuxx/runner_test.go`
- Modify: `internal/tmuxx/tmux_test.go`
- Create: `internal/amqx/client.go`
- Create: `internal/amqx/client_test.go`

**Interfaces:**

```go
type CommandInvocation struct {
    Arguments   []string
    Directory   string
    Environment []string
}

type CommandTerminationKind uint8

type CommandTermination struct {
    Kind     CommandTerminationKind
    ExitCode int
    Signal   syscall.Signal
}

type CommandResult struct {
    Started     bool
    Stdout      []byte
    Stderr      []byte
    Termination CommandTermination
}

type Runner interface {
    Output(context.Context, string, ...string) ([]byte, error)
    RunInteractive(context.Context, string, ...string) error
    RunCommand(context.Context, string, CommandInvocation) (CommandResult, error)
}

type Client struct {
    Runner      tmuxx.Runner
    Environment func() []string
}

func (Client) ResolveBase(context.Context, string) (string, error)
func (Client) ListSessions(context.Context, string) (SessionList, tmuxx.CommandResult, error)
func (Client) InitSession(context.Context, string, string, []string) (tmuxx.CommandResult, error)
```

The concrete runner owns `os/exec`; `internal/amqx` owns only AMQ command construction, environment selection, strict decoding, and defensive result types. Fleet decisions do not enter either package.

- [ ] **Step 1: Add failing `internal/tmuxx` tests for the typed command path**

  Cover every invocation and termination fact required by amended §§15.8 and 15.9, named and unmapped raw signals, cancellation-versus-independent-signal races, defensive copies, and compatibility with the existing output/interactive methods. Update the local `operationRunner` test double to record the new method.

- [ ] **Step 2: Run the runner RED test**

  Run `go test ./internal/tmuxx -run 'Test.*RunCommand' -count=1` and retain the expected compile/test failure in the lane evidence.

- [ ] **Step 3: Implement the typed method on real and fake runners**

  Implement the final issue D2 process-fact distinction without selecting CLI outcomes in this package.

- [ ] **Step 4: Run the runner GREEN test**

  Run `go test ./internal/tmuxx -count=1`.

- [ ] **Step 5: Add failing AMQ client boundary tests**

  Assert every §15.12.2 invocation through `tmuxx.FakeRunner`, including exact executable/argv, cwd, environment, call ordering, strict decoding failures, probe inputs named by the final issue amendment, and copy ownership. Add the final environment-premise matrix as table cases.

- [ ] **Step 6: Run the AMQ client RED test**

  Run `go test ./internal/amqx -count=1` and retain the focused failure.

- [ ] **Step 7: Implement the AMQ client**

  Decode only the stable upstream members required by §15.12 and preserve raw command facts for the fleet layer. Do not select agentctl outcomes here.

- [ ] **Step 8: Verify and commit the lane**

  Run `go test ./internal/tmuxx ./internal/amqx -count=1`, `go vet ./internal/tmuxx ./internal/amqx`, and `git diff --check`; create one SSH-signed commit and send its hash to the convergence owner.

### Task 1B: Extend durable fleet records and recover safe reservations (parallel build2 lane)

**Files:**

- Modify: `internal/fleet/types.go`
- Modify: `internal/fleet/shim_record.go`
- Modify: `internal/fleet/shim_test.go`
- Modify: `internal/fleet/foreground.go`
- Modify: `internal/fleet/foreground_test.go`
- Modify mechanical constructor fixtures in `internal/kill/shim_executor_test.go` and `cmd/agentctl/integration_shim_lifecycle_test.go` only when required by the selected constructor API

**Interfaces:**

```go
type OwnershipProvenance string

type ShimFleetRecordClass uint8

type ShimFleetRecordObservation struct {
    Record ShimFleetRecord
    Class  ShimFleetRecordClass
    Path   string
}

func (s *ShimFleetRecordStore) ReadForAMQProvisioning(string) (ShimFleetRecordObservation, error)
func (s *ShimFleetRecordStore) CreateForAMQProvisioning(ShimFleetRecordObservation, ShimFleetRecord) error
func (s *ShimFleetRecordStore) EstablishOwnership(ShimFleetRecordObservation, ShimFleetRecord) error
```

These methods implement the amended §15.12.4 and final D3 record-store contract; ordinary `Read` continues to implement the general §15.2 contract.

- [ ] **Step 1: Add failing codec tests for the amended current schema**

  Cover the complete codec matrix required by §15.2 and the post-#247 issue amendment, including exact-byte serialization and ordinary replacement/extension preservation.

- [ ] **Step 2: Run the codec RED test**

  Run `go test ./internal/fleet -run 'TestShimFleetRecord.*Provenance' -count=1`.

- [ ] **Step 3: Implement current-schema provenance and foreground initialization**

  Implement the amended §15.2 codec/initialization contract and add the registered record source to `fleet.Provenance`.

- [ ] **Step 4: Add failing provisioning-read and reservation tests**

  Cover every §15.12.4 provisioning-read class and every final issue D3 reservation/integrity/race cell. Assert stable structured observations for later rendering.

- [ ] **Step 5: Run the record-state RED tests**

  Run `go test ./internal/fleet -run 'TestShimFleetRecordStore.*(Provisioning|Reservation|Ownership)' -count=1`.

- [ ] **Step 6: Implement narrow reads and locked record mutations**

  Reuse the existing descriptor-relative verified-open path, mutation flock, and atomic writer. Preserve the §15.8 record-commit distinctions as typed facts.

- [ ] **Step 7: Add failing one-way ownership/config refresh tests**

  Cover the complete D1/post-#247 ownership and config-refresh matrix, rollback retention, stale observations, and the final D3 concurrent-winner behavior.

- [ ] **Step 8: Implement ownership/config refresh and verify the lane**

  Run `go test ./internal/fleet ./internal/kill -count=1`, `go test -race ./internal/fleet -count=1`, `go vet ./internal/fleet ./internal/kill`, and `git diff --check`; create one SSH-signed commit and send its hash to the convergence owner.

### Task 2: Implement launch preparation, the full partition, and mutation ordering

**Files:**

- Create: `internal/fleet/amq_provisioning.go`
- Create: `internal/fleet/amq_provisioning_test.go`
- Modify: `internal/fleet/shim.go`
- Modify: `internal/fleet/shim_test.go`
- Modify: `internal/fleet/shim_relaunch.go` and its tests only for dependency/request plumbing
- Modify: `internal/fleet/foreground.go` and its tests only to prove the provisioner is absent from `run`

**Interfaces:**

```go
type LaunchFieldSources struct {
    Roles []RoleFieldSources
}

type LaunchFieldSource struct {
    Provenance Provenance
    Declared   bool
}

type RoleFieldSources struct {
    Harness LaunchFieldSource
    Model   LaunchFieldSource
    Effort  LaunchFieldSource
}

type ShimLaunchRequest struct {
    Session      string
    Fleet        config.FleetConfig
    Sources      LaunchFieldSources
    Presentation Presentation
    Directory    *string
    Adopt        bool
}

type AMQProvisionResult struct {
    DurableRecord ShimFleetRecord
    Effective     config.FleetConfig
    Sources       LaunchFieldSources
    RecordCreated bool
}
```

The request keeps invocation execution facts separate from durable identity; see the final D1 spec rows for selection and use of those inputs.

- [ ] **Step 1: Converge Tasks 1A and 1B**

  Integrate only the reviewed signed lane commits, resolve type names explicitly, and run `go test ./internal/tmuxx ./internal/amqx ./internal/fleet ./internal/kill -count=1`.

- [ ] **Step 2: Add failing preparation tests**

  Cover the complete amended D1 input/source/refresh matrix and the associated §§15.12.2 and 15.12.7 guards.

- [ ] **Step 3: Run preparation RED and implement the smallest preparation phase**

  Run `go test ./internal/fleet -run 'TestAMQLaunchPreparation' -count=1`, implement preparation, then rerun the same command for GREEN.

- [ ] **Step 4: Add the complete failing partition table**

  Drive every §15.12.3 rule, §15.12.4 record class, and final issue D3 addition with fake AMQ and record observations. Include every precedence-sensitive and retry guard required by §15.12.7.

- [ ] **Step 5: Run partition RED and implement read-only selection**

  Run `go test ./internal/fleet -run 'TestAMQProvision.*Partition' -count=1`, implement the selector without rendering strings, and rerun for GREEN.

- [ ] **Step 6: Add failing creation/termination/re-observation tests**

  Cover the complete §15.8/§15.12.5 and final issue D2 matrix. Assert the exact mutation and re-observation call counts required by §15.12.7.

- [ ] **Step 7: Implement creation and phased outcome selection**

  Select from typed runner and AMQ observation facts according to the cited outcome register.

- [ ] **Step 8: Add failing record-commit and race tests**

  Cover every §15.12.4 commit/adoption path, the final D1/D3 record mutations, final reread/compare races, both §15.8 record-commit outcomes, rollback ownership, and next-launch classification.

- [ ] **Step 9: Implement record commit/reuse with final comparisons**

  Preserve the exact record snapshot required by the amended §15.12.4 rollback matrix.

- [ ] **Step 10: Add failing end-to-end launcher order tests**

  Assert the ordering required by amended §15.12.2 and the corresponding §15.12.7 guards through production namespace/store openers.

- [ ] **Step 11: Integrate with `ShimLauncher` and verify**

  Run `go test ./internal/amqx ./internal/fleet -count=1`, `go test -race ./internal/amqx ./internal/fleet -count=1`, `go vet ./internal/amqx ./internal/fleet`, and `git diff --check`.

### Task 3: Wire launch-only parsing, provenance, and typed output

**Files:**

- Modify: `cmd/agentctl/launch_template.go`
- Modify: `cmd/agentctl/launch_template_test.go`
- Modify: `cmd/agentctl/main.go`
- Modify: `cmd/agentctl/shim_results.go`
- Modify: `cmd/agentctl/main_launch_test.go`
- Modify: `cmd/agentctl/main_launch_template_test.go`
- Create: `cmd/agentctl/amq_output_contract_test.go`
- Modify: `cmd/agentctl/runtime_dependencies.go`
- Modify: `cmd/agentctl/skill_launch_notice_test.go`
- Modify: `cmd/agentctl/skill_contract_test.go`
- Modify: `README.md`
- Modify: `skills/agentctl/SKILL.md`
- Modify: `skills/agentctl/references/exit-codes.md`

**Interfaces:**

- Add `Adopt bool` and `LaunchFieldSources` to the launch-only request seam.
- Build `amqx.Client` from the production `tmuxx.Runner`; do not inject it into foreground execution.
- Render all new §15.8 outcomes from typed fields, never by parsing AMQ stderr.

- [ ] **Step 1: Add a failing regression test for launch-field provenance**

  Pin every amended D1 per-field provenance source, including the regression identified in the frozen issue amendment.

- [ ] **Step 2: Run provenance RED and fix source tracking**

  Run `go test ./cmd/agentctl -run 'Test.*Launch.*Provenance' -count=1`, make the minimal source-tracking correction, and rerun for GREEN.

- [ ] **Step 3: Add failing launch-flag boundary tests**

  Assert the launch-option scope and dependency path registered by amended §§15.8 and 15.12.4.

- [ ] **Step 4: Implement launch-only parsing and request composition**

  Preserve raw declaration/source facts until `ShimLaunchRequest` is built; do not collapse them into effective values early.

- [ ] **Step 5: Add failing exact renderer tests**

  Create the shared row fixtures/helpers in `cmd/agentctl/amq_output_contract_test.go` and assert every affected amended §15.8 row and §15.12.1 success template there. Keep orchestration assertions in `main_launch_test.go`.

- [ ] **Step 6: Implement typed rendering and production wiring**

  Compose registered output according to amended §§15.8 and 15.12.1.

- [ ] **Step 7: Add launch-only and foreground isolation guards**

  Assert the launch-only scope and foreground limitation registered by amended §15.12.6.

- [ ] **Step 8: Update public docs and verify**

  Update README, embedded skill, and exit reference using the spec literals. Run `go test ./cmd/agentctl ./skills -count=1`, `go vet ./cmd/agentctl ./skills`, the repository skill-pairing check, and `git diff --check`.

### Task 4: Prove real composition and land current-truth security/release docs

**Files:**

- Create: `cmd/agentctl/integration_amq_provisioning_test.go`
- Modify: `cmd/agentctl/integration_fixture_test.go`
- Modify: `cmd/agentctl/integration_shim_lifecycle_test.go`
- Modify: `internal/structural/invariants_test.go`
- Modify: `SECURITY.md`
- Modify: `hack/securitydoc_test.go` only if needed for the current-truth guard and existing word budget
- Modify: `hack/release-verify.sh`
- Modify: `hack/releaseverify_test.go`
- Modify: `docs/release-checklist.md`
- Create: `docs/releases/0.5.1.md`
- Modify: `hack/releasenotes_test.go`

- [ ] **Step 1: Extend the integration process double and capture RED**

  Add the typed `RunCommand` method to the integration runner, model the amended §15.12 process facts, keep existing tmux socket isolation, and assert the corresponding §15.12.7 composition guard.

- [ ] **Step 2: Add end-to-end creation/adoption/refusal tests**

  Cover both presentations and every §15.12.7 end-to-end creation, adoption, refusal, config-refresh/relaunch, commit, preservation, and fresh-deliverability guard.

- [ ] **Step 3: Add real-opener and base-surface guards**

  Exercise production namespace/store openers on throwaway roots and assert the corresponding amended §15.12.7 snapshots and mutation guards.

- [ ] **Step 4: Add structural guards**

  Pin every structural invariant registered by amended §§15.2, 15.9, and 15.12.7, including the `tmuxx.Runner` production dependency edge.

- [ ] **Step 5: Update release-verifier fixture tests and script**

  Add fixture-first tests for success, substitution, interruption, commit uncertainty, mail preservation/deliverability, base surfaces, and identity-gated cleanup. Keep all live probes on throwaway AMQ roots and throwaway tmux sockets.

- [ ] **Step 6: Apply current-truth security documentation**

  Apply amended §15.12.8's SECURITY obligations only after the corresponding production behavior exists, and preserve the existing security-document guard budget.

- [ ] **Step 7: Add release-note/checklist obligations**

  Apply the amended §15.12.8 release obligations and pin their evidence extraction without changing `VERSION` or publishing a release.

- [ ] **Step 8: Run integration/docs/release GREEN**

  Run `go test -tags integration ./cmd/agentctl -count=1`, `go test ./internal/structural ./hack -count=1`, `shellcheck hack/*.sh`, and `git diff --check`.

- [ ] **Step 9: Self-review against the contract**

  Map every §15.12.7 bullet and final issue-amendment clause to a named test. Run the repository red-flag and forbidden-success-vocabulary scans, inspect each hit, and check interface/type names against Tasks 1A–3.

### Task 5: Rebase, verify, publish, and hand off without merging

**Files:**

- No planned product files; only review or verification fixes
- The PR body records `Closes #163`, contract impact, RED/GREEN evidence, and every executed gate

- [ ] **Step 1: Fetch and perform a signed rebase onto current `main`**

  Confirm a clean worktree, fetch `origin main`, rebase using the repository's SSH-signing path so rewritten commits remain signed, inspect the full new merge-base diff, and rerun every test affected by conflict resolution.

- [ ] **Step 2: Verify every rebased commit signature**

  Run `git log --show-signature --format=fuller origin/main..HEAD` and require a good SSH signature for every commit before pushing.

- [ ] **Step 3: Run repository and CI-equivalent gates**

  Run every current `.github/workflows/ci.yml` gate explicitly:

  - `hack/check-skill-pairing.sh "$BASE_SHA" "$HEAD_SHA"`
  - `hack/check-workflow-timeouts.sh`
  - `./hack/ci-fingerprint.sh` at the workflow's pre/post-tool checkpoints
  - `hack/check-govulncheck-workflows.sh`
  - `go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...`
  - `go test ./...` and `go vet ./...`
  - verify `shellcheck --version` reports `0.11.0`, then run `shellcheck hack/*.sh`
  - `golangci-lint run` using `v2.12.2`
  - `go test -race ./...`
  - verify `tmux -V` reports `tmux 3.7b`, then run `go test -tags integration ./...`
  - `goreleaser check`
  - `goreleaser release --snapshot --clean --skip=notarize`
  - `./hack/verify-release-archives.sh dist/*.tar.gz`
  - run `./dist/agentctl_darwin_arm64*/agentctl version` and apply the workflow's snapshot-version smoke assertion

  Record every skip or failure factually.

- [ ] **Step 4: Run isolated live AMQ verification**

  Execute the amended release verifier per `docs/release-checklist.md`; capture installed AMQ version, the pinned environment/absent-base probes, creation/adoption/refusal facts, mail preservation and fresh deliverability, base surfaces, teardown observations, and artifact hashes.

- [ ] **Step 5: Obtain fresh build2 review of the exact verified commit**

  Send the post-rebase exact commit and evidence to build2 on `design/issue-163`, copying planner. Require an explicit verdict on process ownership, partition/races, record truth, launch-only scope, deliverability, and spec/security/release alignment.

- [ ] **Step 6: Restart verification after any content change**

  Any review fix, conflict resolution, or other content change returns to its owning RED/GREEN step, then repeats Task 5 Steps 1–5, including all affected local/live gates, signature verification, and a fresh exact-commit build2 verdict. Publication uses only the unchanged reviewed commit.

- [ ] **Step 7: Push and open one atomic PR**

  Confirm `origin/main` is unchanged since Step 1, then push the unchanged verified/reviewed commit through the approval path and open the implementation PR with issue, security, release, probe, build2-review, and test evidence. If `main` moved, restart at Step 1. Do not merge it.

- [ ] **Step 8: Wait for merge-result CI and reviewer release**

  Quote the PR's own `pull_request` run URL and obtain the reviewer gate. Any resulting content change repeats Task 5 Steps 1–7 before another push.

- [ ] **Step 9: Detach the worktree and hand off**

  Detach the PR worktree so it does not hold the branch at merge time. Report the PR, signed commit set, CI run, live evidence, build2 verdict, reviewer verdict, and accepted §15.12.6 limitations to planner/maintainer.

## Final acceptance checklist

- [ ] Every final issue amendment and §15.12.7 guard maps to a passing test.
- [ ] AMQ execution uses the typed `internal/tmuxx.Runner` boundary with exact fake assertions.
- [ ] The amended §15.12.2 ordering and mutation guards pass.
- [ ] The amended §§15.8 and 15.12.3–5 partition/ownership guards pass.
- [ ] Every frozen D1 and D3 integration guard passes.
- [ ] The amended §15.12.6 foreground-scope guard passes.
- [ ] Spec, SECURITY, release notes, checklist, README, and embedded skill agree with shipped behavior.
- [ ] Focused, race, integration, lint, release, live, CI, signature, build2, and reviewer evidence are recorded before handoff.
