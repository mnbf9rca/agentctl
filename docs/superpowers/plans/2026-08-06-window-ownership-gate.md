# Window Ownership Gate Implementation Plan

> **Non-normative working document.** This plan records implementation steps only. The authoritative contract is issue #125 plus [`2026-08-01-agentctl-design.md`](../specs/2026-08-01-agentctl-design.md), especially §§6.2, 6.3, 6.5, 12, and 13. If this plan differs from either source, follow the issue and design spec.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make the window-ownership gate depend only on metadata that is actually window-scoped, and prevent future session stamping from making that gate inheritable.

**Architecture:** Treat the stored role marker as the sole window-ownership evidence consumed by target resolution, status collection, and relaunch observation. Remove inherited, unread fields from `tmuxx.Window` and its collection format while retaining the existing metadata-stamping contract. Pin the construction that keeps the role marker window-scoped, and exercise the resulting refusal against a handmade window on a throwaway real-tmux server.

**Tech Stack:** Go 1.26, standard library only, existing typed tmux client, deterministic fake runner, and the integration fixture's private tmux socket.

### Task 1: Establish focused RED coverage

**Files:**
- Modify: `internal/tmuxx/tmux_test.go`
- Modify: `internal/target/resolver_test.go`
- Modify: `internal/status/collector_test.go`
- Modify: `internal/fleet/relaunch_test.go`
- Modify: `internal/fleet/fleet_test.go`

- [x] Change the window-list parser expectation to the approved metadata model and assert the canonical call exactly.
- [x] Add focused consumer cases showing that a same-name row with no stored role is rejected before later probes.
- [x] Add a named session-stamping test proving `stampSession` never writes the window-ownership marker.
- [x] Run the focused tests and record the expected RED failures before production edits.

### Task 2: Implement the stored-role-only gate

**Files:**
- Modify: `internal/tmuxx/tmux.go`
- Modify: `internal/target/resolver.go`
- Modify: `internal/target/errors.go`
- Modify: `internal/status/collector.go`
- Modify: `internal/fleet/relaunch.go`
- Modify: `cmd/agentctl/main.go`
- Modify: affected unit tests under `internal/` and `cmd/agentctl/`

- [x] Remove the inherited fields from `tmuxx.Window` and the canonical collection format.
- [x] Make target, status, and relaunch evaluate only the stored role marker at the window-ownership step.
- [x] Remove the unreachable refusal branch while preserving factual role-mismatch reporting.
- [x] Update exact fake-runner records and fixture rows without weakening call-order assertions.
- [x] Run focused tests and verify GREEN; refactor only while green.

### Task 3: Pin the real-tmux regression

**Files:**
- Modify: `cmd/agentctl/integration_fixture_test.go`
- Modify: `cmd/agentctl/integration_launch_test.go`
- Modify: relevant lifecycle or relaunch integration test file

- [x] Add fixture support for creating and inspecting a handmade window on the private tmux server.
- [x] Verify launch metadata at its actual scope rather than through inherited format expansion.
- [x] Exercise clear, status, and relaunch against a handmade same-name window and assert no neighboring replacement is created.
- [x] Run the focused integration test and verify GREEN.

### Task 4: Update the governing contracts

**Files:**
- Modify: `SECURITY.md`
- Modify: `docs/superpowers/specs/2026-08-01-agentctl-design.md`

- [x] Describe stored-role equality as the real window-scoped gate and state why it cannot inherit by construction.
- [x] Update the affected flow, state-precedence, collection-format, and option-read descriptions.
- [x] Keep window metadata stamping documented without claiming the advisory marker is consumed as a gate.

### Task 5: Verify and publish

**Files:**
- Review every changed file.

- [ ] Run focused tests and inspect the final diff.
- [ ] Run every repository and CI gate required by issue #125 and the live workflow.
- [ ] Commit with SSH signing, fetch and rebase onto current `main`, rerun all required gates, and push the topic branch.
- [ ] Open a PR closing issue #125, attach it to milestone 0.3.0, and record red/green evidence plus any observable output impact.
- [ ] Wait for the PR's own CI, request the planner gate with its exact run URL, then detach the worktree.
