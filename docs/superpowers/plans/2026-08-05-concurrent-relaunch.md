# Concurrent Relaunch Race Implementation Plan

> **Non-normative working document.** This plan records implementation steps only. The authoritative contract is issue #124 plus [`2026-08-01-agentctl-design.md`](../specs/2026-08-01-agentctl-design.md), especially §§1.1, 6.8, 9, 13.2, and 13.5. If this plan differs from either source, follow the issue and design spec.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Prevent concurrent relaunches of one absent role from leaving duplicate role windows or reporting success for an ambiguous result.

**Architecture:** Keep the existing absence precondition, then re-list the exact session immediately after `NewWindow` returns a typed window ID. Continue only when the role resolves solely to that ID; otherwise route cleanup through `rollbackWindow` and report the observed conflict with the post-ownership failure contract. Reuse the existing typed `ListWindows` and `KillWindow` paths without adding command surface.

**Tech Stack:** Go 1.26, standard library only, existing typed tmux client, and deterministic fake runner.

### Task 1: Lock the concurrent interleaving with RED tests

**Files:**
- Modify: `internal/fleet/relaunch_test.go`
- Modify: `cmd/agentctl/main_relaunch_test.go`

- [x] Script the fake runner so the precondition sees no role window and the post-create listing sees two matching IDs.
- [x] Assert the fleet layer refuses, reports every observed ID, and rolls back only the typed ID returned to this invocation.
- [x] Assert the CLI emits no success output and maps the post-ownership refusal per §§6.8 and 9.
- [x] Run the focused tests and record the expected RED failure before production edits.

### Task 2: Implement post-create ownership verification

**Files:**
- Modify: `internal/fleet/relaunch.go`
- Modify: `cmd/agentctl/main.go`
- Modify: `internal/fleet/relaunch_test.go`
- Modify: `cmd/agentctl/main_relaunch_test.go`

- [x] Add a factual error type for the role-window IDs observed after creation.
- [x] Re-list by exact session ID immediately after `NewWindow`, before stamping or success can occur.
- [x] Require the sole same-role match to equal the newly returned window ID; route every other outcome through `rollbackWindow`.
- [x] Render the conflict as a refusal while preserving the cleanup outcome and post-ownership exit contract.
- [x] Update existing fake-runner scripts and exact call-order assertions for the new verification read.
- [x] Run focused tests and verify GREEN; refactor only while green.

### Task 3: Update the governing security and behavior contracts

**Files:**
- Modify: `SECURITY.md`
- Modify: `docs/superpowers/specs/2026-08-01-agentctl-design.md`

- [x] Amend the relaunch concurrency guarantee in `SECURITY.md` to describe post-create verification and own-ID rollback.
- [x] Update §6.8, §9, and the relaunch test requirements to cover the additional exact-session read and its refusal/rollback behavior.
- [ ] Explain in the PR why deterministic real-tmux coverage would require test-only orchestration and is therefore omitted.

### Task 4: Verify and publish

**Files:**
- Review every file changed above.

- [ ] Run focused race-enabled tests and inspect the final diff.
- [ ] Run all repository and CI gates named by issue #124 and the live workflow.
- [ ] Commit with SSH signing, rebase onto current `main`, rerun the required gates, and push the topic branch.
- [ ] Open a PR closing issue #124, attach it to milestone 0.3.0, and record the exit-code and integration-test rationale.
- [ ] Wait for the PR's own CI, request the reviewer gate with its exact run URL, then detach the worktree.
