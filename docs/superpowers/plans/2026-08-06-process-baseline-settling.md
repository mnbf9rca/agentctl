# Process Baseline Settling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Accept a launch process baseline only after two consecutive identical non-`amq` observations while preserving the existing 5s rollback consequence.

**Architecture:** Keep the existing immediate attempt, 100ms cadence, 5s boundary attempt, and typed tmux process probe. Track the previous eligible observation locally; `amq` or an unavailable probe clears the candidate, a different non-`amq` value replaces it, and an identical next value returns it. No harness-name matching and no re-baselining during verification.

**Tech Stack:** Go 1.26 standard library, fake `tmuxx.Runner`, fake clock, Markdown spec and SECURITY policy.

## Global Constraints

- Issue #126 including AMENDED 2026-08-06 is authoritative.
- Timeout still returns the existing error and launch/relaunch rollback behavior; no empty baseline or heal-on-verify change.
- State only that two samples reduce the transient window; they do not prove settling.
- Normal cost is one 100ms tick per role, about 800ms for eight roles.

### Task 1: Require a stable pair

**Files:** `internal/fleet/fleet_test.go`, `internal/fleet/relaunch_test.go`, `internal/fleet/fleet.go`, `docs/superpowers/specs/2026-08-01-agentctl-design.md`, `SECURITY.md`

- [x] Add RED tests for `[amq, env, claude, claude]`, `[amq, claude, claude]`, and changing non-`amq` values through the 5s boundary followed by exact rollback.
- [x] Implement `previous := ""`; accept only `err == nil && process != "amq" && process == previous`; otherwise update or clear `previous`, preserving error and deadline ordering.
- [x] Update successful launch/relaunch response fixtures and sleep assertions to provide the required stable pair and one extra tick.
- [x] Update spec §8 with the explicit `amq coop exec → exec(harness)` assumption, N=2 rule, limitation, numeric cost, and unchanged timeout rollback. Update SECURITY residual 2 consistently and retain the persistent-intruder rationale against heal-on-verify.
- [ ] Run focused tests, `go test ./...`, `go vet ./...`, `shellcheck hack/*.sh`, `golangci-lint run ./...`, `go test -tags integration ./...`, and release snapshot checks after rebasing current main.
- [ ] Create signed commits and PR `Closes #126`, wait for exact `pull_request` CI, detach worktree, and request the gate from planner.
