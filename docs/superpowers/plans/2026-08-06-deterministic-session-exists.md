# Deterministic Session-Exists Classification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every `agentctl launch` duplicate-session refusal return exit 3 while preserving tmux's observed message and leaving unrelated creation failures on exit 6.

**Architecture:** Keep the advisory `list-sessions` pre-check unchanged. At the single `fleet.Launch` boundary where `new-session` fails, recognize tmux's duplicate refusal only when the wrapped error is an `*exec.ExitError` with exit code 1 and stderr exactly `duplicate session: NAME` plus at most one terminal line ending; return `SessionExistsError` carrying that cause so the CLI's existing typed branch selects exit 3 and renders the observed tmux failure. No typed created-session ID exists on this path, so rollback remains impossible and unattempted.

**Tech Stack:** Go 1.26 standard library, `internal/tmuxx.Runner` fake, real tmux 3.7b integration fixture, Markdown design spec.

## Global Constraints

- The issue #127 body is the complete task contract; stay within its acceptance criteria.
- Keep the Go module on the standard library and execute production external commands only through `internal/tmuxx.Runner`.
- Preserve exact tmux argv element boundaries and never invoke a shell in production.
- Every output and exit code must remain a factual claim under design spec §1.1.
- A strict-match miss must degrade to the existing exit-6 tmux failure, never to exit 3.
- Use TDD, signed focused commits, current-main rebase, all CI gates, exact `pull_request` run evidence, and an independent reviewer RELEASE before handoff.

---

### Task 1: Classify the atomic duplicate-session refusal

**Files:**
- Modify: `cmd/agentctl/main_launch_test.go`
- Modify: `cmd/agentctl/integration_launch_test.go`
- Modify: `internal/fleet/fleet.go`
- Modify: `docs/superpowers/specs/2026-08-01-agentctl-design.md`

**Interfaces:**
- Consumes: `tmuxx.Client.NewSession(...) (tmuxx.CreatedSession, error)`, whose runner failure wraps the concrete process error as `tmux create session: %w`.
- Produces: `fleet.SessionExistsError{Name string, Cause error}` with `Cause == nil` for the advisory pre-check and a non-nil original `new-session` error for the raced case; `Error()` preserves the existing pre-check wording for nil causes and tmux's classified factual message otherwise.

- [x] **Step 1: Write the failing CLI regression and strict-boundary canaries**

  Change the subprocess error helper to accept an explicit exit code, then make the exact duplicate case expect exit 3, the complete tmux message, empty stdout, the exact `list-sessions`/`new-session` argv sequence, and no `kill-session`. Add table canaries showing that exit code 1 with stderr such as `permission denied: duplicate session: fleet`, or the exact duplicate text with a non-1 exit code, remains exit 6.

  The production mutation these tests catch is removing or loosening the exact `new-session` duplicate classification.

- [x] **Step 2: Run the focused test and verify RED**

  Run:

  ```bash
  go test ./cmd/agentctl -run 'TestRunLaunch(ClassifiesDuplicateSessionRace|BoundsDuplicateSessionRaceClassification)$' -count=1 -v
  ```

  Expected: the exact race test fails because current code returns exit 6; strict-boundary canaries continue to demonstrate the old generic path.

- [x] **Step 3: Add the minimal strict classifier and cause-carrying domain error**

  In `internal/fleet/fleet.go`, extend `SessionExistsError` with `Cause error`, preserve `session %q already exists` when it is nil, render `tmuxx.ClassifyError(Cause)` when it is non-nil, and unwrap the cause. Add one helper with this effective predicate:

  ```go
  var exitError *exec.ExitError
  if !errors.As(err, &exitError) || exitError.ExitCode() != 1 {
      return false
  }
  stderr := string(exitError.Stderr)
  switch {
  case strings.HasSuffix(stderr, "\r\n"):
      stderr = strings.TrimSuffix(stderr, "\r\n")
  case strings.HasSuffix(stderr, "\n"):
      stderr = strings.TrimSuffix(stderr, "\n")
  }
  return stderr == "duplicate session: "+session
  ```

  Call it only in the error branch immediately following `l.newSession(...)`; on a match return `&SessionExistsError{Name: session, Cause: err}` before malformed-output and generic-error handling. Do not call rollback.

- [x] **Step 4: Run the focused test and verify GREEN**

  Run the Step 2 command again.

  Expected: PASS; exact refusal is exit 3 with tmux stderr intact, and canaries are exit 6.

- [x] **Step 5: Perform the mutation-coverage check**

  Confirm the CLI regression already proves the fleet-domain error contract: `launchResult` can select exit 3 only by
  receiving `*fleet.SessionExistsError`. Its exact two-call transcript also proves no rollback. Do not add a duplicate
  fleet test after production is green; that would be post-hoc coverage rather than an independently observed RED.

- [x] **Step 6: Update the real-tmux integration contract**

  Rename the existing advisory-lookup-failure duplicate test to describe classification, change its expected code from 6 to 3, retain its assertion that stderr contains real tmux's `duplicate session: integration-raced-existing`, and retain the byte-identical sentinel plus zero-harness-invocation assertions.

- [x] **Step 7: Update the governing design spec**

  In §6.7, supersede the sentence that called exit 3 versus exit 6 deliberate. State that both pre-check and atomic refusal are exit 3, while only the raced path carries tmux's factual message. Document the strict boundary: `new-session` only, wrapped `*exec.ExitError`, code 1, exact one-line stderr for the validated requested name, one terminal line ending allowed; every near-match remains exit 6. Retain the no-stderr-matching rule for the advisory `list-sessions` no-server path and update the §10 launch-failure test inventory.

- [x] **Step 8: Format and run focused plus package tests**

  Run:

  ```bash
  gofmt -w internal/fleet/fleet.go cmd/agentctl/main_launch_test.go cmd/agentctl/integration_launch_test.go
  go test ./internal/fleet ./cmd/agentctl
  go test -tags integration ./cmd/agentctl -run 'TestIntegrationLaunchClassifiesDuplicateSessionWhenAdvisoryLookupFails' -count=1 -v
  ```

  Expected: all PASS with no warnings.

- [x] **Step 9: Commit the implementation**

  ```bash
  git add cmd/agentctl/main_launch_test.go cmd/agentctl/integration_launch_test.go internal/fleet/fleet.go docs/superpowers/specs/2026-08-01-agentctl-design.md docs/superpowers/plans/2026-08-06-deterministic-session-exists.md
  git commit -S -m "Classify duplicate session races consistently"
  ```

- [ ] **Step 10: Rebase and run every repository gate**

  Fetch and rebase onto current `origin/main`, resolving #131 overlap if it has landed, then run:

  ```bash
  go test ./...
  go vet ./...
  shellcheck hack/*.sh
  golangci-lint run ./...
  go test -tags integration ./...
  goreleaser check
  goreleaser release --snapshot --clean --skip=notarize
  ```

  Expected: all PASS. Inspect the signed commit with `git log -1 --show-signature`.

- [ ] **Step 11: Publish and obtain independent release evidence**

  Push the topic branch, open a PR with `Closes #127`, the 0.3.0 milestone, red/green evidence, the strict-bound explanation, no-rollback evidence, and the superseded §6.7 sentence quoted explicitly. Wait for the PR's exact `pull_request` CI run, then request an independent reviewer gate with that run URL. Address every finding, re-run affected/full gates, and obtain a standalone `RELEASED` PR comment.

- [ ] **Step 12: Detach and hand off**

  Detach the issue worktree so the topic branch is not held at merge time. Reply to the planner with the PR URL, exact CI URL, reviewer verdict URL, signed commit, and explicit note that this agent did not merge its own PR.
