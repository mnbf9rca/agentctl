# SECURITY.md Amendments Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reconcile SECURITY.md amendments D1-D4 from issue #128 with the behavior and evidence on current `main`.

**Architecture:** This is a documentation-only change. Preserve the stronger wording already landed by issues #123, #124, #126, and #127, then amend the governing security claims so they describe current concurrency outcomes, version-pinned delivery evidence, and the actual release-checklist trigger.

**Tech Stack:** Markdown documentation; Go, shell, integration, lint, and release gates for repository verification.

## Global Constraints

- The current issue #128 body is the complete contract.
- Do not change production code or tests.
- Preserve current-main fixes and wording; do not restore pre-fix findings from the merged review.
- Every SECURITY.md statement must describe observed current behavior under design-spec §1.1.
- Route the final reviewer gate to the planner because the reviewer authored D1-D4.

---

### Task 1: Reconcile D1-D4 against current main

**Files:**
- Modify: `SECURITY.md`
- Create: `docs/superpowers/plans/2026-08-06-security-amendments.md`

**Interfaces:**
- Consumes: issue #128; `docs/security/2026-08-05-merged-v1-security-review.md`; current launch and relaunch behavior on `main`
- Produces: version-pinned security evidence and current concurrency residuals in `SECURITY.md`

- [ ] **Step 1: Amend the single-invocation fleet-record guarantee**

Qualify the override write-back claim so it remains true for one relaunch invocation and explicitly records the concurrent last-writer-wins case for different-role overrides.

- [ ] **Step 2: Split residual 1 evidence by date and harness version**

Record the 2026-08-05 fixed-injection verification on Claude Code 2.1.222 and codex-cli 0.146.0 separately from the 2026-08-01 popup and saturation evidence on Claude Code 2.1.220 and codex-cli 0.146.0. State that `--measure` has not rerun because `internal/tmuxx.payloadDelay` is unchanged.

- [ ] **Step 3: Add the current-main concurrency residual**

Add the six-row table from the merged review, updating duplicate-session races to deterministic exit 3 and same-role relaunch races to the post-fix both-rollback, exit-8, recoverable outcome. State that no locking exists and record the same-pane `respawn-pane -k` TOCTOU shape.

- [ ] **Step 4: Narrow the release-checklist claim**

State that manual harness re-verification runs only for releases changing tmux targeting, harness startup, or injected command delivery.

- [ ] **Step 5: Self-review the documentation diff**

Run:

```bash
git diff --check
git diff -- SECURITY.md docs/superpowers/plans/2026-08-06-security-amendments.md
rg -n "T[B]D|T[O]DO|i[m]plement later|f[i]ll in details" SECURITY.md docs/superpowers/plans/2026-08-06-security-amendments.md
```

Expected: no whitespace errors, no placeholders, no code or test changes, and all D1-D4 acceptance criteria represented.

- [ ] **Step 6: Rebase and run repository gates**

Fetch and rebase onto current `origin/main`, then run:

```bash
go test ./... -count=1
go vet ./...
shellcheck hack/*.sh
golangci-lint run ./...
go test -tags integration ./... -count=1
goreleaser check
goreleaser release --snapshot --clean --skip=notarize
```

Expected: every command exits 0 and the snapshot binary reports an `agentctl v...` version.

- [ ] **Step 7: Publish the signed PR and request the planner gate**

Create a focused signed commit, push the topic branch, and open a PR with `Closes #128, Refs #81`. Wait for the PR's exact `pull_request` CI run, detach the worktree, then send the planner the PR URL, signed head, exact run URL, and verification evidence. Do not merge the PR.
