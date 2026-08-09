# Settle-timeout Retention and Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement issue #152's approved role-fails-closed launch outcome, distinguish the resulting status state, and let `relaunch` recover exactly that state.

**Architecture:** Keep process settling in `internal/fleet`, but return a typed launch result that carries the created session and roster-ordered unproven roles to the CLI. Add the approved state to the shared status classifier, then reuse that classifier in relaunch so recovery is gated by the same precedence and exact typed window ID. Preserve `internal/tmuxx` as the only external-command boundary.

**Tech Stack:** Go standard library, fake `tmuxx.Runner` unit tests, real-tmux integration tests, POSIX shell probes.

## Global Constraints

- The issue #152 body, including its AMENDED section and dated test-contract bullet, is the task contract.
- The approved design is `docs/superpowers/specs/2026-08-01-agentctl-design.md`, especially §§1.2, 6.1–6.8, 8–10, and 13.2–13.4.
- `SECURITY.md` already carries the approved change; stop and report any discrepancy instead of editing it.
- Production external-command calls remain typed `internal/tmuxx` operations; no shell and no caller-controlled targeting.
- Every behavior change follows red/green TDD and records exact fake-Runner call order where the issue requires it.

---

### Task 1: Launch result and settle-timeout retention

**Files:**
- Modify: `internal/fleet/fleet.go`
- Modify: `internal/fleet/fleet_test.go`

**Interfaces:**
- Produces: `fleet.LaunchResult`, carrying the created `tmuxx.Session` and roster-ordered unproven roles.
- Produces: a typed process-settle timeout observation used to render the per-role launch diagnostic from the final poll attempt.
- Consumes: existing `Launcher.processBaseline`, `Launcher.stampWindow`, and `tmuxx.Runner` call recording.

- [ ] **Step 1: Write failing launch tests**

  Add focused cases for every terminal observation form and for a timeout in the first and later roles. Assert the returned unproven roster, absence of the final baseline stamp, absence of rollback, later-role creation, row-5a ordering, and unchanged rollback for non-timeout failures per §§6.1, 6.5, 6.6, and 8.

- [ ] **Step 2: Run the focused tests and capture RED**

  Run `go test ./internal/fleet -run 'TestLaunch.*(Unproven|Timeout|Boundary|Ordering)' -count=1` and preserve the expected failure summary for the PR.

- [ ] **Step 3: Implement the minimal fleet change**

  Introduce the typed result and timeout observation, make launch consume only the timeout class while continuing the roster, and leave relaunch and every other launch error on their existing rollback paths.

- [ ] **Step 4: Run the focused package tests and capture GREEN**

  Run `go test ./internal/fleet -count=1` and confirm the complete package passes.

### Task 2: CLI reporting and exit mapping

**Files:**
- Modify: `cmd/agentctl/main.go`
- Modify: `cmd/agentctl/main_launch_test.go`

**Interfaces:**
- Consumes: `fleet.LaunchResult`.
- Produces: `exitLaunchUnproven` and the summary/reporting flow defined by §§6.1, 6.6, and 9.

- [ ] **Step 1: Write failing command tests**

  Cover one and multiple unproven roles, roster ordering, status confirmation success/failure, skill notices, and exact output destinations. Assert the new exit class without weakening post-launch status rendering.

- [ ] **Step 2: Run the focused tests and capture RED**

  Run `go test ./cmd/agentctl -run 'TestRunLaunch.*Unproven' -count=1`.

- [ ] **Step 3: Implement the minimal CLI mapping**

  Thread `fleet.LaunchResult.Session` through confirmation, emit the roster summary after launch attempts, and select the new exit after advisory confirmation.

- [ ] **Step 4: Run the focused package tests and capture GREEN**

  Run `go test ./cmd/agentctl -count=1`.

### Task 3: Status classification and control remedy

**Files:**
- Modify: `internal/status/status.go`
- Modify: `internal/status/collector.go`
- Modify: `internal/status/collector_test.go`
- Modify: `internal/status/render_test.go`
- Modify: `cmd/agentctl/main.go`
- Modify: `cmd/agentctl/main_control_test.go`

**Interfaces:**
- Produces: `status.StateNoBaseline` through `status.States()`.
- Consumes: the baseline field already present in `tmuxx.Window`.

- [ ] **Step 1: Write failing status and control tests**

  Prove the §6.3 state, rendering, and no-probe behavior, plus the §6.2 remedy wording for empty-baseline control refusal.

- [ ] **Step 2: Run the focused tests and capture RED**

  Run `go test ./internal/status ./cmd/agentctl -run 'Test.*(NoBaseline|EmptyBaseline)' -count=1`.

- [ ] **Step 3: Implement the minimal classification and message change**

  Add the state at the approved precedence point, return it before any process observation, and update only the empty-baseline control branch.

- [ ] **Step 4: Run focused tests and capture GREEN**

  Run `go test ./internal/status ./cmd/agentctl -count=1`.

### Task 4: Relaunch recovery by exact window ID

**Files:**
- Modify: `internal/fleet/relaunch.go`
- Modify: `internal/fleet/relaunch_test.go`
- Modify: `cmd/agentctl/main.go`
- Modify: `cmd/agentctl/main_relaunch_test.go`

**Interfaces:**
- Produces: recovery metadata on `fleet.RelaunchResult` and a typed pre-creation recovery-kill error.
- Consumes: `status.StateNoBaseline`, `tmuxx.Client.KillWindow`, and the existing relaunch creation/rollback path.

- [ ] **Step 1: Write failing recovery tests**

  Cover successful recovery, every later terminal outcome retaining the recovery fact, recovery-kill failure, and refusal of ambiguous, dead, unmanaged, zero-pane, running, and recorded-baseline mismatch states. Assert the issue's required preflight/directory/kill/create order and exact-ID targeting.

- [ ] **Step 2: Run the focused tests and capture RED**

  Run `go test ./internal/fleet ./cmd/agentctl -run 'Test.*Relaunch.*(Recover|NoBaseline|Unproven)' -count=1`.

- [ ] **Step 3: Implement the minimal recovery path**

  Replace the absent-only precondition with an absent-or-recovery classification, perform the recovery kill only after all non-destructive checks, carry the removed ID through success and error reporting, and leave recorded mismatch outside recovery.

- [ ] **Step 4: Run focused tests and capture GREEN**

  Run `go test ./internal/fleet ./cmd/agentctl -count=1`.

### Task 5: Real-tmux recovery and window-ID reuse evidence

**Files:**
- Modify: `cmd/agentctl/integration_relaunch_test.go`
- Modify: `cmd/agentctl/integration_launch_test.go`
- Modify: `hack/probe-3-ids.sh`
- Modify: `docs/superpowers/specs/2026-08-01-agentctl-design.md`

**Interfaces:**
- Consumes: throwaway tmux sockets and stub harnesses only.
- Produces: durable create/kill/create window-ID reuse evidence cited from §13.4.

- [ ] **Step 1: Write failing integration coverage**

  Add real-tmux cases proving a baseline-less launch is retained and recoverable while a recorded mismatch is refused.

- [ ] **Step 2: Run the focused integration tests and capture RED**

  Run `go test -tags integration ./cmd/agentctl -run 'TestIntegration.*(Unproven|Recover)' -count=1`.

- [ ] **Step 3: Implement and run the durable probe**

  Extend the throwaway-socket ID probe with create/kill/create comparison and make it fail if a live server reuses the removed ID. Record the observed version/result in the evidence line under §13.4.

- [ ] **Step 4: Run focused integration tests and capture GREEN**

  Run the probe and `go test -tags integration ./cmd/agentctl -count=1`.

### Task 6: Embedded skill drift and version discipline

**Files:**
- Modify: `skills/agentctl/SKILL.md`
- Modify: `skills/agentctl/references/status-states.md`
- Modify: `skills/agentctl/references/exit-codes.md`
- Modify: `cmd/agentctl/skill_contract_test.go` only if the existing inventory tests need a behavior-level extension.

**Interfaces:**
- Consumes: `status.States()` and command exit constants through the existing drift tests.
- Produces: 0.4.0 skill content that matches the binary surface.

- [ ] **Step 1: Run the drift tests and capture RED**

  Run `go test ./cmd/agentctl -run 'Test(ExitCodeTableMatchesConstants|StatusStatesMatch)' -count=1` after the production constants land.

- [ ] **Step 2: Update the embedded skill minimally**

  Add the new state and exit class to their machine-readable references, update the skill's state inventory/remedy guidance, and advance `metadata.version` for the 0.4.0 milestone.

- [ ] **Step 3: Run the skill contract tests and capture GREEN**

  Run `go test ./cmd/agentctl -run 'TestSkill|TestExitCodeTableMatchesConstants|TestStatusStatesMatch' -count=1`.

### Task 7: Full verification, publication, and handoff

**Files:**
- Verify: all changed files

**Interfaces:**
- Produces: signed commits, ready PR closing #152, pull-request CI evidence, and reviewer-gate AMQ handoff.

- [ ] **Step 1: Run local gates**

  Run `gofmt`, `go test ./...`, `go vet ./...`, `shellcheck hack/*.sh`, `golangci-lint run`, `go test -tags integration ./...`, and the release snapshot checks required by `.github/workflows/ci.yml` where relevant.

- [ ] **Step 2: Rebase and repeat merge evidence**

  Fetch current `main`, rebase the topic branch, then repeat the required gates on the rebased tree.

- [ ] **Step 3: Commit and publish**

  Create focused SSH-signed commits, push the topic branch, open a ready-for-review PR with `Closes #152`, and include red/green plus full-gate evidence.

- [ ] **Step 4: Wait for PR CI and request review**

  Quote the PR's own `pull_request` run URL in the reviewer request, detach the worktree, and report the handoff through AMQ.
