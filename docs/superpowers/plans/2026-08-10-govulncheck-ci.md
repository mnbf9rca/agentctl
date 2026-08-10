# Govulncheck CI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a deterministic, repository-owned govulncheck gate to pull requests and a daily scan of `main`.

**Architecture:** The existing CI `test` job runs the exact pinned govulncheck command so its required check fails before vulnerable code can merge. A dedicated scheduled workflow runs the same command daily on `main`. A small fixture-tested policy script protects the exact pin, the two invocation sites, and the daily trigger from accidental drift; the existing timeout checker continues to enforce job-level bounds across every workflow.

**Tech Stack:** GitHub Actions YAML, Bash, Go test harnesses, `golang.org/x/vuln/cmd/govulncheck@v1.6.0`.

## Global Constraints

- Use the exact command `go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...`; never use `@latest`.
- Match existing CI pins exactly: `macos-26`, `actions/checkout@v7`, `actions/setup-go@v7`, Go `1.26`, and job-level `timeout-minutes: 15`.
- The scheduled scan runs daily on `main`; a failing scheduled run is the only notification mechanism.
- Keep the Go module dependency graph unchanged; the pinned tool is invoked by `go run` in CI and is not added to `go.mod`.
- Preserve the amended issue statement: Sourcery noticed GO-2026-5970 nondeterministically and is not the vulnerability gate.
- Run the issue gates: `go test ./...`, `go vet ./...`, `go test -tags integration ./...`, `shellcheck hack/*.sh`, and `golangci-lint run`.

---

### Task 1: Protect the govulncheck workflow contract

**Files:**
- Create: `hack/check-govulncheck-workflows.sh`
- Create: `hack/checkgovulncheckworkflows_test.go`

**Interfaces:**
- Consumes: a workflow directory containing `ci.yml` and `vulnerability.yml`.
- Produces: exit 0 with empty output only when both workflows use the exact pinned command and the scheduled workflow has the selected daily cron; otherwise exit 1 with a filename-specific diagnostic.

- [ ] **Step 1: Write fixture tests that name the protected failures**

Add table-driven Go tests which execute the real shell checker against temporary workflow directories. Hand-write fixtures for: a valid PR-plus-daily pair; a missing PR invocation; `@latest`; an absent schedule; and a scheduled workflow missing the pinned invocation. Also add a repository-workflows test which invokes the checker with no directory argument.

- [ ] **Step 2: Run the focused test and verify RED**

Run: `go test ./hack -run 'TestCheckGovulncheckWorkflows' -count=1`

Expected: FAIL because `hack/check-govulncheck-workflows.sh` does not exist.

- [ ] **Step 3: Implement the minimal policy checker**

Write a Bash script using `set -euo pipefail`, an optional workflow-directory argument, literal matching for the exact pinned command, explicit rejection of `govulncheck@latest`, and a literal daily cron contract. Keep diagnostics factual and identify the failing workflow.

- [ ] **Step 4: Run fixture tests while repository integration remains RED**

Run: `go test ./hack -run 'TestCheckGovulncheckWorkflows' -count=1`

Expected: fixture cases PASS; the repository-workflows case still FAILS because the workflow changes do not exist yet.

### Task 2: Add the PR and daily vulnerability gates

**Files:**
- Modify: `.github/workflows/ci.yml`
- Create: `.github/workflows/vulnerability.yml`

**Interfaces:**
- Consumes: repository checkout and Go 1.26 module graph.
- Produces: the required `test` job runs the pinned scanner on pushes and pull requests; the scheduled workflow runs the same scanner once per day.

- [ ] **Step 1: Add the PR policy and scanner steps**

In the existing `test` job, run `hack/check-govulncheck-workflows.sh`, then run exactly:

```yaml
      - name: Govulncheck
        run: go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
```

Place both after Go setup so the required `test` context owns the gate.

- [ ] **Step 2: Add the bounded daily workflow**

Create a workflow triggered by one daily cron and containing one `govulncheck` job with `runs-on: macos-26`, `timeout-minutes: 15`, checkout v7, setup-go v7 with Go 1.26, and the exact pinned scan command.

- [ ] **Step 3: Verify GREEN for the policy and timeout checks**

Run:

```bash
go test ./hack -run 'TestCheckGovulncheckWorkflows|TestCheckWorkflowTimeoutsAcceptsRepositoryWorkflows' -count=1
hack/check-govulncheck-workflows.sh
hack/check-workflow-timeouts.sh
```

Expected: all commands exit 0 with no checker output.

- [ ] **Step 4: Run the exact vulnerability scan locally**

Run: `go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...`

Expected: exit 0 and `No vulnerabilities found.` against main commit `7af1d0b` plus the workflow-only changes.

### Task 3: Record the automated dependency review layer

**Files:**
- Modify: `SECURITY.md`

**Interfaces:**
- Consumes: the existing manual dependency-review rule.
- Produces: one adjacent sentence identifying the deterministic PR and daily govulncheck checks.

- [ ] **Step 1: Update the dependency-security paragraph**

Add one sentence beside the existing `go mod graph` and `go.sum` review rule stating that CI runs the pinned govulncheck scanner on every PR and daily on `main` to catch both introduced dependencies and advisories published after merge.

- [ ] **Step 2: Review the diff for scope and factual wording**

Run: `git diff --check && git diff -- .github/workflows hack/check-govulncheck-workflows.sh hack/checkgovulncheckworkflows_test.go SECURITY.md`

Expected: no whitespace errors; no claim that Sourcery failed to notice GO-2026-5970; no unrelated files.

### Task 4: Verify, publish, and request review

**Files:**
- Modify: `docs/superpowers/plans/2026-08-10-govulncheck-ci.md` only to check completed steps if useful.

**Interfaces:**
- Consumes: the completed focused diff.
- Produces: a signed topic-branch commit and ready PR closing issue #201.

- [ ] **Step 1: Run all repository gates**

Run:

```bash
go test ./...
go vet ./...
go test -tags integration ./...
shellcheck hack/*.sh
golangci-lint run
go test -race ./...
goreleaser check
goreleaser release --snapshot --clean --skip=notarize
```

Expected: every command exits 0; the snapshot binary smoke check is run if the release build changes or repository convention requires it.

- [ ] **Step 2: Rebase onto current main and repeat affected gates**

Fetch `origin/main`, verify the branch base, rebase if necessary, and repeat the full gates after any rewritten or conflict-resolved tree.

- [ ] **Step 3: Create a focused signed commit**

Stage only the plan, workflows, policy checker/tests, and SECURITY change. Commit with an SSH signature and verify it using `git log --show-signature`.

- [ ] **Step 4: Push and open a ready PR**

Push `agent/201-govulncheck-ci`, create a non-draft PR whose body says `Closes #201`, names the exact red/green evidence, explains Sourcery's nondeterminism accurately, and records every verification command.

- [ ] **Step 5: Wait for the PR's own run and request the reviewer gate**

Wait for the `pull_request` CI run, confirm the new Govulncheck step is green, and send the reviewer a `review_request` with the PR URL and exact run URL. Fix all findings in the same PR, do not merge, and detach the worktree after opening the PR.
