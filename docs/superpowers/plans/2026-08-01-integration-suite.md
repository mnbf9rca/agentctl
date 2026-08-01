# Isolated tmux Integration Suite Implementation Plan

> **Non-normative working document.** The authoritative contract is issue #16 plus [`2026-08-01-agentctl-design.md`](../specs/2026-08-01-agentctl-design.md), especially §§6, 8–10, 12, and 13. The approved test architecture and CI decisions are in [`2026-08-01-integration-suite-design.md`](../specs/2026-08-01-integration-suite-design.md). If this plan differs from those sources, follow the issue and design documents.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Apply superpowers:test-driven-development to every fixture capability and behavioral test. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the real-tmux, no-real-agent integration gate required by issue #16.

**Architecture:** Build-tagged package-main tests call `runWithRunner` with a test-only `socketRunner`. That runner makes the unique tmux socket mandatory for every tmux operation, while temporary executable stubs replace AMQ and agent harnesses. The existing production dispatcher, fleet, status, target, control, and kill paths remain unchanged.

**Tech Stack:** Go 1.26 standard library, real tmux, POSIX test stub scripts, GitHub Actions macOS runners, Homebrew.

## Global constraints

- Keep all executable requirements and assertions sourced from issue #16 and the design documents cited above; do not duplicate normative CLI, metadata, process, state, error, or tmux-argument contracts here.
- Add no production flags, environment variables, hooks, or behavior changes.
- Every integration test file uses the `integration` build constraint and package `main`.
- Resolve the real tmux binary before changing `PATH`; all tmux access stays structurally scoped through the fixture socket.
- Register bounded server cleanup before any operation can create tmux state, and do not run these tests in parallel.
- Stub every external agent/AMQ executable and make test failures observable through real tmux state or marker-process side effects.
- Blank inherited `TMUX_PANE` in fixtures so an unrelated caller socket cannot cause a false self-target refusal.

---

### Task 1: Build the socket-scoped fixture through the launch test

**Files:**
- Create: `cmd/agentctl/integration_fixture_test.go`
- Create: `cmd/agentctl/integration_launch_test.go`

**Interfaces:**
- Produces: `integrationFixture`, `socketRunner`, `newIntegrationFixture(*testing.T)`, `runAgentctl(...string)`, socket-scoped tmux query helpers, marker-process `TestMain`, and stub executable installation.
- Consumes: `runWithRunner`, `tmuxx.Runner`, the production launch path, and the process observation contract cited above.

- [ ] **Step 1: Write the successful launch test against the wished-for fixture API**

Add `TestIntegrationLaunchRecordsTopologyMetadataAndBaseline`. Assert the required launch outcome using parsed CLI results, real socket-scoped tmux observations, stub invocation records, and the real marker pane process. Keep expected values literal and independent of production builders.

- [ ] **Step 2: Run the focused integration test and verify RED**

Run: `go test -tags integration ./cmd/agentctl -run TestIntegrationLaunchRecordsTopologyMetadataAndBaseline -count=1`

Expected: compile failure because the fixture API does not exist.

- [ ] **Step 3: Implement the minimal fixture and Runner**

Implement the random socket allocation, absolute tmux lookup, mandatory socket injection, bounded `kill-server` cleanup, environment setup, and dispatcher invocation. Route non-tmux Runner calls through ordinary `os/exec`.

- [ ] **Step 4: Add the AMQ, harness, and marker stubs**

Install temporary executable scripts for every external program the launch preflight and harness can select. Record launch inputs, then replace the stub process with the current test binary in marker mode. Marker mode must stay alive and append submitted terminal lines to a role-specific capture file.

- [ ] **Step 5: Run the focused test and verify GREEN**

Run: `go test -tags integration ./cmd/agentctl -run TestIntegrationLaunchRecordsTopologyMetadataAndBaseline -count=1`

- [ ] **Step 6: Mutation-check the fixture boundary**

Temporarily bypass socket injection for one tmux query and confirm the test or containment check fails; restore immediately. Temporarily replace one independent launch assertion with a wrong literal and confirm it fails; restore immediately.

- [ ] **Step 7: Refactor while green and commit**

Run `gofmt`, the focused integration test, and `go test ./cmd/agentctl`. Then commit the fixture and successful launch coverage.

---

### Task 2: Cover pre-existing ownership and mid-launch rollback

**Files:**
- Modify: `cmd/agentctl/integration_fixture_test.go`
- Modify: `cmd/agentctl/integration_launch_test.go`

**Interfaces:**
- Produces: sentinel-session helpers and `socketRunner.failNextTmuxOperation`.
- Consumes: the production launch ownership and rollback behavior cited above.

- [ ] **Step 1: Write the pre-existing-session refusal test**

Create a sentinel on the fixture socket, attempt launch through `runWithRunner`, and assert refusal without mutation of the sentinel.

- [ ] **Step 2: Run the focused test and verify RED**

Run the new test alone. Expected: compile failure because the sentinel helpers do not exist.

- [ ] **Step 3: Implement only the sentinel helpers and verify GREEN**

All creation, inspection, and cleanup must use the existing socket-scoped execution function.

- [ ] **Step 4: Write the partial-launch rollback test**

Arm a one-shot failure for the later tmux operation identified by issue #16, launch, and assert that no partially owned session remains.

- [ ] **Step 5: Run the focused test and verify RED**

Run the rollback test alone. Expected: compile failure because failure injection is absent.

- [ ] **Step 6: Add operation-level one-shot failure injection and verify GREEN**

Keep fault state test-only, synchronized, and consumed once. Do not make arbitrary full-argv matching part of the fixture API.

- [ ] **Step 7: Mutation-check ownership safeguards and commit**

Confirm the refusal test detects sentinel mutation and the rollback test detects skipped cleanup, restoring each mutation immediately. Run both launch tests, default package tests, and commit.

---

### Task 3: Cover status, clear delivery, and kill

**Files:**
- Modify: `cmd/agentctl/integration_fixture_test.go`
- Create: `cmd/agentctl/integration_lifecycle_test.go`

**Interfaces:**
- Produces: role-window mutation helpers, parsed status helpers, and bounded marker-input polling.
- Consumes: the production status, target resolution, control delivery, and kill paths cited above.

- [ ] **Step 1: Write status coverage against real marker panes**

Launch once, assert both roles through parsed status output, remove one role's tmux window, then assert the resulting mixed status facts required by issue #16.

- [ ] **Step 2: Run the status test and verify RED**

Expected: compile failure because lifecycle helpers do not exist.

- [ ] **Step 3: Implement minimal status/window helpers and verify GREEN**

Select roles using fixture observations and typed results. Do not reproduce production target-resolution logic inside test helpers.

- [ ] **Step 4: Write clear-delivery coverage**

Launch a fresh fixture, invoke `clear`, wait for the target marker's observable input, and prove the sibling marker received nothing.

- [ ] **Step 5: Run the clear test and verify RED**

Expected: compile failure because bounded input polling is absent.

- [ ] **Step 6: Implement bounded marker polling and verify GREEN**

Poll only test-owned files. Report the last observed bytes on timeout so failures remain diagnosable.

- [ ] **Step 7: Write and run kill coverage**

Launch a fresh fixture, invoke `kill`, and assert through the scoped server that the managed session no longer exists. Add only the smallest helper needed.

- [ ] **Step 8: Mutation-check lifecycle assertions and commit**

Confirm the status test detects a wrong role state, the clear test detects sibling delivery, and the kill test detects a remaining session; restore each mutation immediately. Run all integration package tests plus default package tests, then commit.

---

### Task 4: Add the hard macOS integration gate

**Files:**
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Produces: a separate macOS integration job using the approved Homebrew install, pin, version check, and tagged test command from the integration design.

- [ ] **Step 1: Add the separate integration job**

Leave the existing Linux unit and vet job unchanged. Make installation, pinning, version validation, and the tagged integration run unconditional sequential steps so any failure fails the job.

- [ ] **Step 2: Review the workflow as executable policy**

Verify the integration test cannot be skipped through shell conditionals or tolerated failures. Verify permissions remain read-only and action versions remain consistent with the existing workflow.

- [ ] **Step 3: Validate and commit the workflow**

Run available repository workflow validation, `git diff --check`, and inspect the rendered diff. Commit the CI gate separately.

---

### Task 5: Full verification and handoff

**Files:**
- Review: `cmd/agentctl/integration_*_test.go`
- Review: `.github/workflows/ci.yml`
- Review: the approved integration design and this plan

**Interfaces:**
- Verifies the complete issue #16 deliverable without changing production code.

- [ ] **Step 1: Format and run default verification**

Run: `gofmt -w cmd/agentctl/integration_*_test.go`, `go test ./...`, `go test -race ./...`, `go vet ./...`, and `go build ./...`.

- [ ] **Step 2: Run the integration suite uncached**

Run: `go test -tags integration ./... -count=1`.

- [ ] **Step 3: Audit containment and cleanup**

Inspect the final helpers to prove every tmux invocation receives the fixture socket. Check for leftover test-prefixed tmux servers/processes after both passing and intentionally failing focused tests. Do not query or mutate the default tmux server during this audit.

- [ ] **Step 4: Inspect the complete diff and history**

Run `git diff --check`, review the branch diff from its merge base, confirm no production file changed, and confirm commits remain scoped to design, fixture/coverage, CI, and any final review fix.

- [ ] **Step 5: Request review, publish, and detach**

Send the normal AMQ review gate with exact verification evidence. Address feedback, push `wave5/integration`, open the issue #16 draft PR, and detach the worktree after publication as directed by the planner.
