# 0.4.1 Release Preparation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prepare agentctl 0.4.1 by aligning the embedded skill version with the patch release and recording direct evidence for the Part C mechanism changes executed since 0.4.0.

**Architecture:** Preserve the patch-version resolver: `hack/next-version.sh` derives 0.4.1 from tag `v0.4.0`, so `VERSION` remains unchanged. Treat the already-landed template guidance and open-ended effort validation as audit findings, not edit targets; update only the skill metadata stamp and the release checklist's direct pointer to the executed #199 evidence.

**Tech Stack:** Go standard library, POSIX shell release helpers, Markdown embedded with `go:embed`, GitHub Actions release gates.

## Global Constraints

- Issue #205 is the complete contract; do not change product behavior or expand the release surface.
- Do not modify `VERSION`; patch release 0.4.1 is computed from the latest bare release tag.
- Preserve the operator-only template guidance and its schema reference already shipped by PR #200.
- Do not add an effort catalogue; the shipped skill contains only the template schema's `effort` field and relies on launch-time value validation.
- Record the #199 execution audit as a direct evidence pointer, without duplicating its rationale.
- Keep the Go module on the standard library and do not modify AMQ.

---

### Task 1: Align the embedded skill version

**Files:**
- Modify: `skills/agentctl/SKILL.md`
- Verify: `skills/agentctl/references/fleet-template.schema.json`
- Verify: `cmd/agentctl/skill_contract_test.go`

**Interfaces:**
- Consumes: release candidate `0.4.1` from `hack/next-version.sh`.
- Produces: embedded `metadata.version: "0.4.1"`, accepted by `hack/check-skill-version.sh` and installed unchanged by `agentctl skill install`.

- [ ] **Step 1: Preserve direct RED evidence**

  Run `hack/next-version.sh` and require output `0.4.1` with exit 0. Run `hack/check-skill-version.sh 0.4.1` as its own command and require exit 1 with `skill documents 0.4.0; releasing 0.4.1`.

- [ ] **Step 2: Record the shipped-surface audit**

  Run `rg -n -i '\beffort(s)?\b|\b(low|medium|high|xhigh|max|ultra)\b' skills/agentctl` and confirm the only matches are the schema description and `effort` field. Inspect `skills/agentctl/SKILL.md` section 2.1 and confirm that it points to `references/fleet-template.schema.json`, states that schema-valid is not necessarily launch-valid, and keeps launch operator-only.

- [ ] **Step 3: Make the minimal version edit**

  Change only the frontmatter line from `version: "0.4.0"` to `version: "0.4.1"`. Leave `VERSION`, template prose, schema contents, and effort references unchanged.

- [ ] **Step 4: Capture GREEN and backward-mismatch evidence**

  Run `hack/check-skill-version.sh "$(hack/next-version.sh)"` and require exit 0 with no output. Run `hack/check-skill-version.sh 0.4.0` separately and require exit 1 with the reverse mismatch.

### Task 2: Record the changed Part C mechanism audit

**Files:**
- Modify: `docs/release-checklist.md`

**Interfaces:**
- Consumes: the executed probe, mutation checks, fixture checks, repository gates, and final reviewer verdict recorded at `https://github.com/mnbf9rca/agentctl/pull/199#issuecomment-5236425269`.
- Produces: a direct release-runbook pointer proving that the Part C onboarding seeding, consent scope, and C.C1 no-reauthentication mechanism changed since 0.4.0 were each exercised once.

- [ ] **Step 1: Add the direct evidence pointer**

  Immediately after the existing #178 audit pointer, add: `The Part C synthesized-onboarding seed, link-only consent scope, and C.C1 no-reauthentication mechanism changed for #188 were executed in the [#199 probe, mutation checks, and final reviewer gate](https://github.com/mnbf9rca/agentctl/pull/199#issuecomment-5236425269).`

- [ ] **Step 2: Check factual scope**

  Confirm the link is the final #199 reviewer gate, the sentence names only mechanisms evidenced there, and no live Parts A-D result or promotion claim is added.

### Task 3: Verify, publish, and hand off

**Files:**
- Verify: `skills/agentctl/SKILL.md`
- Verify: `docs/release-checklist.md`
- Verify: `docs/superpowers/plans/2026-08-10-release-prep-0.4.1.md`

**Interfaces:**
- Produces: a signed focused commit, a PR closing #205, green `pull_request` CI evidence, a reviewer-gate request, and a detached worktree.

- [ ] **Step 1: Exercise the skill install/status round-trip**

  Build `agentctl` into a temporary directory, create an isolated temporary HOME, run `agentctl skill install`, and run `agentctl skill status`. Require both commands to exit 0 and report the embedded skill consistently without touching the operator's real skill directories.

- [ ] **Step 2: Run focused release checks**

  Run the version GREEN/backward-mismatch sequence, the focused skill contract tests, `hack/check-skill-pairing.sh origin/main HEAD`, and relevant release-verifier tests.

- [ ] **Step 3: Run every issue and CI gate**

  Run `go test ./...`, `go vet ./...`, `go test -tags integration ./...`, `shellcheck hack/*.sh`, and `golangci-lint run`, using a writable task-local Go build cache and only throwaway tmux sockets.

- [ ] **Step 4: Inspect and sign the focused change**

  Confirm `git diff --check` passes; the diff changes no product code, `VERSION`, schema, or security contract; and the signed commit contains only the three planned files.

- [ ] **Step 5: Rebase and repeat merge evidence**

  Fetch current `origin/main`, rebase the topic branch, and repeat the required checks on the rebased tree before pushing.

- [ ] **Step 6: Open the PR and wait for merge-result CI**

  Push the branch, open a PR with `Closes #205`, include direct RED/GREEN exit-code evidence and the no-change audits, then wait for the PR's own `pull_request` CI run to pass.

- [ ] **Step 7: Request review and detach**

  Send the reviewer an AMQ `review_request` containing the PR URL and exact successful run URL, copy the planner, do not merge, and detach the worktree so the branch is not held at merge time.
