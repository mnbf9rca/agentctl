# Dependabot Hygiene Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Apply superpowers:test-driven-development to the policy checker before changing the repository configuration.

**Goal:** Batch routine Dependabot updates into one monthly pull request per supported ecosystem while leaving security updates advisory-triggered and immediate.

**Architecture:** A small fixture-tested policy checker parses Dependabot YAML with Ruby's standard-library YAML parser and enforces the repository-owned scheduling, grouping, and security-update boundaries. The live `.github/dependabot.yml` supplies one version-update group for Go modules and one for GitHub Actions, with no security-update grouping. SchemaStore's Dependabot schema provides independent structural validation, and `SECURITY.md` records the automated update layer.

**Tech Stack:** Dependabot v2 YAML, Bash 3.2-compatible shell, Ruby standard-library YAML, Go standard-library fixture tests, Markdown.

## Global Constraints

- Configure exactly the `gomod` and `github-actions` ecosystems at repository root.
- Schedule routine version updates monthly and group every dependency once per ecosystem.
- Do not apply either group to `security-updates`; security updates remain triggered by advisories rather than the monthly version-update schedule.
- Limit routine open pull requests to one per ecosystem to match the one-group-per-cycle contract.
- Keep Dependabot's default labels and commit-message prefix because the repository has no established dependency labels or prefix convention to preserve.
- Validate `.github/dependabot.yml` with pinned `check-jsonschema` 0.37.4 against SchemaStore's `dependabot-2.0.json`, and name the exact command in the PR body.
- Keep the Go module dependency graph unchanged.

---

### Task 1: Protect the Dependabot update policy

**Files:**
- Create: `hack/check-dependabot-config.sh`
- Create: `hack/checkdependabotconfig_test.go`

**Interfaces:**
- Consumes: a Dependabot v2 YAML file path, defaulting to `.github/dependabot.yml`.
- Produces: exit 0 with empty output only when the two supported ecosystems each have one monthly all-dependency version-update group and one open version-update PR slot, without grouping security updates; otherwise exit 1 with a factual diagnostic.

- [x] **Step 1: Write fixture tests before the checker**

Add table-driven Go tests that execute the real shell checker against temporary YAML files. Hand-write fixtures for a valid policy, weekly scheduling, an ungrouped ecosystem, a group applied to security updates, the wrong pull-request limit, and a missing ecosystem. Add a repository-config test that invokes the checker with no path argument.

- [x] **Step 2: Verify RED**

Run:

```bash
go test ./hack -run 'TestCheckDependabotConfig' -count=1
```

Expected: FAIL because `hack/check-dependabot-config.sh` does not exist.

- [x] **Step 3: Implement the smallest policy checker**

Parse the YAML through Ruby's standard-library `YAML.safe_load_file`, validate the top-level v2 shape, select entries by exact ecosystem and root directory, and emit one diagnostic for each broken repository-owned rule. Do not duplicate general YAML/schema validation beyond the fields needed for the policy.

- [x] **Step 4: Verify fixture GREEN while repository integration remains RED**

Run the focused test again. Expected: fixture cases PASS and the repository-config test FAILS because the live config is still weekly and ungrouped.

### Task 2: Configure low-noise routine updates and document automation

**Files:**
- Modify: `.github/dependabot.yml`
- Modify: `SECURITY.md`

**Interfaces:**
- Consumes: Go module and GitHub Actions dependency graphs.
- Produces: one grouped monthly version-update pull request per ecosystem when updates exist, while security updates remain advisory-triggered; security documentation names automated update PRs.

- [x] **Step 1: Make the live policy GREEN**

For each ecosystem, change the schedule to monthly, set `open-pull-requests-limit: 1`, and add exactly one group with `patterns: ["*"]`. Do not set `applies-to: security-updates`, custom labels, or a custom commit-message prefix.

- [x] **Step 2: Add the SECURITY.md sentence**

Add one adjacent sentence stating that Dependabot opens grouped monthly version-update PRs and advisory-triggered security-update PRs, both subject to the same required CI and govulncheck checks.

- [x] **Step 3: Verify policy GREEN**

Run:

```bash
go test ./hack -run 'TestCheckDependabotConfig' -count=1
hack/check-dependabot-config.sh
```

Expected: both commands exit 0 with no checker output.

### Task 3: Validate schema and repository gates

**Files:**
- No additional production files.

**Interfaces:**
- Consumes: the completed Dependabot policy and repository tree.
- Produces: structural schema evidence and complete local gate evidence.

- [x] **Step 1: Validate the live YAML against the pinned schema checker**

Run:

```bash
pipx run --spec check-jsonschema==0.37.4 check-jsonschema --schemafile https://json.schemastore.org/dependabot-2.0.json .github/dependabot.yml
```

Expected: `ok -- validation done` with exit 0.

- [x] **Step 2: Run repository gates**

Run `go test ./...`, `go vet ./...`, `go test -tags integration ./...`, `shellcheck hack/*.sh`, `golangci-lint run`, `go test -race ./...`, the pinned govulncheck command, and release snapshot checks required by CI.

- [x] **Step 3: Review scope and factual claims**

Run `git diff --check` and review the entire branch diff from its merge base. Confirm the implementation does not claim that the monthly schedule controls security updates and does not add labels, commit prefixes, or Go dependencies.

### Task 4: Publish and request the reviewer gate

**Files:**
- Modify this plan only to check completed steps if useful.

**Interfaces:**
- Consumes: completed implementation, green gates, and current `origin/main`.
- Produces: a signed topic commit and ready PR closing issue #202.

- [ ] **Step 1: Rebase onto current main and repeat affected gates**

Fetch `origin/main`, rebase if needed, and repeat the complete gates after any rewritten or conflict-resolved tree.

- [ ] **Step 2: Create and verify a focused signed commit**

Stage only the plan, Dependabot config, policy checker/tests, and `SECURITY.md`. Commit with an SSH signature and verify it with `git log --show-signature`.

- [ ] **Step 3: Open a ready PR with explicit rationale and validation**

Push the branch and open a non-draft PR with `Closes #202`. Name the exact pinned `check-jsonschema` command and explain the pull-request limit, default labels, default commit prefix, and why the groups intentionally do not apply to security updates.

- [ ] **Step 4: Wait for pull-request CI and request review**

Detach the PR worktree, wait for the PR's own `pull_request` CI run, confirm the required `test` job includes green govulncheck, then send the reviewer the PR URL and exact run URL. Address every finding in the same PR and do not merge it.
