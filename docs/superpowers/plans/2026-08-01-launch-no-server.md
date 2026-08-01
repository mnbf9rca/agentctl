# First Launch Without a tmux Server Implementation Plan

> **Non-normative working document.** This plan records implementation steps only. The authoritative contract is issue #66 plus [`2026-08-01-agentctl-design.md`](../specs/2026-08-01-agentctl-design.md), especially §§1.1, 4.1, 6.1, 6.6, 9, 13.1, and 13.2. If this plan differs from either source, follow the issue and design spec.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make `launch` succeed on a fresh machine with no tmux server while retaining exact existing-session refusal and carrying tmux stderr on launch failures.

**Architecture:** Keep the ordinary session lookup as the advisory launch check described by §6.7, with creation deciding when that check cannot report a fact. All other commands remain untouched. The CLI classifies pre-ownership tmux failures before rendering so the shared tmux error type carries captured stderr.

**Tech Stack:** Go 1.26, standard library only, existing typed tmux client, fake runner, and real-socket integration fixture.

### Task 1: Lock the advisory-check and stderr contracts with RED tests

**Files:**
- Modify: `internal/fleet/fleet_test.go`
- Modify: `cmd/agentctl/main_launch_test.go`

- [x] Add fleet tests for the §6.7 outcomes, including lookup failure followed by successful creation.
- [x] Add a CLI regression using a captured `exec.ExitError` to prove launch emits the tmux stderr and the correct failure category.
- [x] Assert a creation refusal after lookup failure performs no cleanup kill.
- [x] Run focused tests and verify RED for the premature lookup failure and dropped stderr.

### Task 2: Implement the minimal unit-level fix

**Files:**
- Modify: `internal/fleet/fleet.go`
- Modify: `cmd/agentctl/main.go`

- [x] Make the launch existence check advisory as specified by §6.7 while preserving exact-match refusal after successful lookup.
- [x] Classify the pre-ownership fallback in `launchResult` before rendering it.
- [x] Run focused tests and verify GREEN; refactor only while green.

### Task 3: Prove the fresh-server behavior with integration tests

**Files:**
- Modify: `cmd/agentctl/integration_fixture_test.go`
- Modify: `cmd/agentctl/integration_launch_test.go`

- [x] Add an integration-fixture option that skips the existing empty-server bootstrap without changing current test defaults.
- [x] Add a launch test using a genuinely absent private tmux socket and verify it succeeds.
- [x] Strengthen duplicate coverage to verify both observed and creation-time refusal paths preserve tmux-side state and perform no cleanup.
- [x] Run the new integration tests against real tmux and verify GREEN.

### Task 4: Verification and handoff

**Files:**
- Review all files changed above.

- [x] Run focused race-enabled tests.
- [x] Run all unit tests, `go vet`, and the full integration suite.
- [x] Inspect the final diff for scope, preserved non-launch behavior, and regression strength.
- [ ] Commit the implementation, publish the branch, open a PR closing issue #66, and request the normal review gate.
