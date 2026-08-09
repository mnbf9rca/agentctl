# 0.4.0 Release Preparation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prepare agentctl 0.4.0 by aligning the release version rail and embedded skill with the shipped binary, and by carrying the completed release-verification work into the standing runbook.

**Architecture:** Keep version resolution in the existing `VERSION`/`hack/next-version.sh` contract and verify the embedded skill through `hack/check-skill-version.sh`. Treat the skill as agent-facing shipped content: update only operator-boundary claims that changed in the 0.4.0 batch, then pressure-test the rendered embedded artifact with the existing contract suite. Keep release history and fallback mechanics in the release checklist, with direct links to the executed evidence rather than duplicated rationale.

**Tech Stack:** Go standard library, POSIX shell release helpers, Markdown embedded with `go:embed`, GitHub Actions/GoReleaser release gates.

## Global Constraints

- Issue #185 is the complete task contract; stay inside its listed release-preparation scope.
- `docs/superpowers/specs/2026-08-01-agentctl-design.md` and `SECURITY.md` remain normative. This task records already-shipped behavior and does not create new behavior.
- Human-facing prose earns no source-text tests. Use the existing executable skill-contract, version, release-verifier, and snapshot gates.
- Record the #177 execution-audit practice as one standing rule and link to its source; do not repeat its rationale.
- Document the historical promote-branch path only as a fallback for a conflicting `main` to `release` promotion, preserving an exact-tree check and append-only release history.

---

### Task 1: Align the release version rail

**Files:**
- Modify: `VERSION`

**Interfaces:**
- Consumes: the latest bare release tag through `hack/next-version.sh`.
- Produces: release version `0.4.0`, accepted by the embedded skill-version gate.

- [ ] **Step 1: Preserve the RED evidence**

  On unmodified `main`, run `hack/next-version.sh` and then run `hack/check-skill-version.sh 0.3.1`. Record that version resolution returns `0.3.1` and the pairing fails because the embedded skill documents `0.4.0`.

- [ ] **Step 2: Make the minimal version change**

  Change `VERSION` from `0.3.0` to `0.4.0`. Do not alter the already-tested version resolver.

- [ ] **Step 3: Capture GREEN and backward-mismatch evidence**

  Run `hack/next-version.sh`, `hack/check-skill-version.sh 0.4.0`, and `hack/check-skill-version.sh 0.3.1`. Require `0.4.0`, success with no output, and the expected nonzero mismatch respectively.

### Task 2: Audit and align the shipped embedded skill

**Files:**
- Modify: `skills/agentctl/SKILL.md`
- Verify: `skills/agentctl/references/status-states.md`
- Verify: `skills/agentctl/references/exit-codes.md`
- Verify: `cmd/agentctl/skill_contract_test.go`

**Interfaces:**
- Consumes: merged `main` command usage, status classification, exit constants, README operator semantics, and `SECURITY.md` recovery gates.
- Produces: the agent-facing 0.4.0 skill installed and printed by the binary.

- [ ] **Step 1: Render-audit every batch-changed claim**

  Compare the embedded skill and references with the real CLI/status/exit surfaces for `no-baseline`, exit 9, relaunch recovery, sole-window and unmarked refusals, and `launch --from-template`.

- [ ] **Step 2: Update only stale operator-boundary claims**

  State that template-based launch remains operator-only. State the positive abandonment-marker and surviving-window conditions for recovery, and the refusal/remedy for sole-window or unmarked no-baseline cases. Preserve the closed command surface for agents.

- [ ] **Step 3: Exercise the embedded artifact**

  Run the skill contract tests, including command/flag parsing, status-state parity, exit-code parity, metadata version, line budget, and the integration test that installs the embedded tree and byte-compares every installed file with it.

### Task 3: Carry release-verification evidence into the checklist

**Files:**
- Modify: `docs/release-checklist.md`

**Interfaces:**
- Consumes: the executed #162 no-server gate, the #178 Part C wrapper audit/re-gate, and issue #177's audit shape.
- Produces: standing release-prep obligations and direct evidence pointers for previously manual checklist legs.

- [ ] **Step 1: Mark the completed verifier legs**

  Add direct executed-verification pointers to the Part A no-server leg and the Part C wrapper/auth/cleanup legs, using the final #162 and #178 reviewer-gate artifacts.

- [ ] **Step 2: Add the standing execution-audit rule**

  Require one execution before the live release run for every checklist mechanism whose implementation changed since the prior release. Link to #177 without restating rationale.

- [ ] **Step 3: Document the promotion fallback once**

  Keep the normal `main` to `release` PR first. If prior promotion-only history makes it conflict, document a `promote/VERSION` branch from current `main`, an `ours` merge of `origin/release`, an exact tree-equivalence check against `main`, and a merge-commit promotion PR from that branch.

### Task 4: Run release and repository verification

**Files:**
- Verify: all changed files and generated release artifacts

**Interfaces:**
- Produces: local merge evidence matching `.github/workflows/ci.yml` plus the release snapshot and smoke gates.

- [ ] **Step 1: Run focused release-prep checks**

  Run the red/green skill-version sequence, relevant Go skill tests, release-verifier automated tests, workflow timeout checks, skill-pairing checks, and release snapshot/smoke scripts.

- [ ] **Step 2: Run the complete local gates**

  Run `go test ./...`, `go vet ./...`, `shellcheck hack/*.sh`, `golangci-lint run`, the race gate, and `go test -tags integration ./...` on throwaway tmux sockets only.

- [ ] **Step 3: Inspect the final diff as factual claims**

  Confirm every skill statement maps to merged behavior, every checklist link names executed evidence, and no unrelated spec/security/product change entered the branch.

### Task 5: Publish and hand off

**Files:**
- Verify: all changed files

**Interfaces:**
- Produces: signed focused commits, a ready PR closing #185, the PR's own CI URL, reviewer-gate handoff, and a detached worktree.

- [ ] **Step 1: Rebase and repeat merge evidence**

  Fetch current `main`, rebase the topic branch, and repeat the required release and repository gates on the rebased tree.

- [ ] **Step 2: Commit and open the PR**

  Create focused SSH-signed commits and a ready PR with `Closes #185`. In the PR body enumerate the successful and failed/skipped steps from #167's dry-run evidence and include the version red/green results.

- [ ] **Step 3: Wait for the PR's own CI and request the gate**

  Quote the successful `pull_request` run URL to the reviewer over AMQ. Do not merge the PR.

- [ ] **Step 4: Detach and report**

  Detach the issue worktree so the branch is not held at merge time, then report the ready PR and reviewer status to the planner.
