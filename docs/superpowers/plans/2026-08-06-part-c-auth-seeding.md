# Part C Authentication Seeding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Make the isolated Part C walkthrough start authenticated harnesses after explicit filename-only consent, while retaining a guided manual-sign-in path and deleting every copied credential on all exits.

**Architecture:** Keep the real HOME isolated from every harness process. Inspect only a fixed allowlist of harness auth files under the captured real HOME, print only their ~/ names, and either copy them with restrictive modes after consent or guide manual sign-in. Remove the temporary HOME independently of tmux/session cleanup so a retained retry root contains no credentials.

**Tech Stack:** Bash 3.2-compatible release tooling, Go standard-library fixture tests, macOS Keychain-backed Claude Code, codex-cli auth files, tmux named sockets, AMQ.

## Global Constraints

- Issue #144 is the complete contract; do not add an auth-management command.
- Never print or log credential contents. Consent and evidence name fixed filenames only.
- Tests use fake auth files under a fake operator HOME, never the real HOME.
- Auth directories are mode 0700; copied files are mode 0600.
- Preserve named-socket, captured-HOME/PATH, exact-resource, cleanup-retry, and fail-closed invariants.
- Update SECURITY.md because the verifier gains a credential-copying path.

---

### Task 1: Consent branches and secure copy

**Files:**
- Modify: hack/releaseverify_test.go
- Modify: hack/release-verify.sh
- Modify: hack/testdata/release-verify-live-artifact/metadata.txt
- Modify: hack/testdata/release-verify-live-results.golden

**Interfaces:**
- Consumes: PART_C_ORIGINAL_HOME, PART_C_HOME, ask, and part_c_abort.
- Produces: PART_C_AUTH_MODE (seeded or manual) and an isolated HOME containing only consented allowlist files.

- [ ] **Step 1: Make the fixture auth-safe**

Create a mode-0700 fake operator HOME containing literal fake auth files and set HOME to it before the verifier runs. Add a launch-boundary log with filenames and modes only.

- [ ] **Step 2: Write the failing consent-yes test**

Add TestLiveVerificationPartCConsentSeedsOnlyNamedAuthFiles. Assert the transcript lists exactly the present allowlist filenames before consent, contains no fake body, and launch observes only .claude.json and .codex/auth.json at mode 600 beneath mode-700 parents.

- [ ] **Step 3: Capture RED**

Run:

~~~bash
GOCACHE=/tmp/agentctl-go-cache go test ./hack -run TestLiveVerificationPartCConsentSeedsOnlyNamedAuthFiles -count=1 -v
~~~

Expected: FAIL because no consent prompt or auth copies exist.

- [ ] **Step 4: Implement the fixed allowlist**

Enumerate only these present source/destination pairs:

~~~text
~/.claude.json              -> PART_C_HOME/.claude.json
~/.claude/.credentials.json -> PART_C_HOME/.claude/.credentials.json
~/.codex/auth.json          -> PART_C_HOME/.codex/auth.json
~~~

Print source names, call ask once, create required directories with install -d -m 0700, and copy files with install -m 0600. Any copy error aborts through Part C teardown.

- [ ] **Step 5: Capture GREEN**

Rerun Step 3 and require PASS.

- [ ] **Step 6: Write failing manual and refuse-both tests**

Add TestLiveVerificationPartCConsentDeclineGuidesManualSignIn and TestLiveVerificationPartCRefusesConsentAndManualSignIn. Manual mode proves no auth file exists at launch and requires concrete Claude/codex sign-in guidance plus a numbered post-attach confirmation. Refuse-both proves AMQ init, skill install, and skillverify launch never begin.

- [ ] **Step 7: Capture RED**

~~~bash
GOCACHE=/tmp/agentctl-go-cache go test ./hack -run 'TestLiveVerificationPartC(ConsentDeclineGuidesManualSignIn|RefusesConsentAndManualSignIn)$' -count=1 -v
~~~

- [ ] **Step 8: Implement manual mode and authentication evidence**

On declined copy consent, offer guided manual sign-in. Accepted manual mode prints actions to complete both sign-ins while attached; refusal calls part_c_abort before AMQ init. Add C.C1 for authenticated ready prompts, shift inventory/meaning to C.C2/C.C3, and record auth mode/attestation without inferring authentication.

- [ ] **Step 9: Run all consent and guidance tests**

~~~bash
GOCACHE=/tmp/agentctl-go-cache go test ./hack -run 'TestLiveVerification(PartC(Consent|Refuses|AgentctlBoundaries)|NumbersCheckpointsAndGuidesUnfamiliarOperator)' -count=1 -v
~~~

- [ ] **Step 10: Commit**

~~~bash
git add hack/release-verify.sh hack/releaseverify_test.go hack/testdata
git commit -S -m "Seed Part C authentication with consent"
~~~

### Task 2: Credential deletion independent of resource retry

**Files:**
- Modify: hack/releaseverify_test.go
- Modify: hack/release-verify.sh

**Interfaces:**
- Consumes: PART_C_HOME, PART_C_ROOT, and session/socket ownership flags.
- Produces: teardown that removes the credential HOME before retaining any retry root.

- [ ] **Step 1: Write failing abort and retained-root tests**

Add TestLiveVerificationPartCAttachAbortRemovesSeededCredentials and extend TestLiveVerificationPartCRootRemovalFailureIsReported. Accept seeding, force abort/root-removal failure, and assert every copied auth path plus the HOME are absent while named-resource cleanup behavior remains pinned.

- [ ] **Step 2: Capture RED**

~~~bash
GOCACHE=/tmp/agentctl-go-cache go test ./hack -run 'TestLiveVerificationPartC(AttachAbortRemovesSeededCredentials|RootRemovalFailureIsReported)$' -count=1 -v
~~~

Expected: the retained-root assertion fails because HOME deletion is not independent.

- [ ] **Step 3: Implement independent HOME removal**

After resource cleanup attempts and environment restoration, remove PART_C_HOME regardless of whether PART_C_ROOT must remain for retry. Report success only after observing absence. Keep the captured path for kill retries and never fall back to the real HOME.

- [ ] **Step 4: Run every Part C lifecycle test**

~~~bash
GOCACHE=/tmp/agentctl-go-cache go test ./hack -run TestLiveVerificationPartC -count=1 -v
~~~

- [ ] **Step 5: Commit**

~~~bash
git add hack/release-verify.sh hack/releaseverify_test.go
git commit -S -m "Remove Part C credentials on every exit"
~~~

### Task 3: Evidence, checklist, and security contract

**Files:**
- Modify: hack/release-verify.sh
- Modify: hack/testdata/release-verify-live-artifact/metadata.txt
- Modify: hack/testdata/release-verify-live-results.golden
- Modify: docs/release-checklist.md
- Modify: SECURITY.md

**Interfaces:**
- Consumes: empirical auth-path findings and new metadata fields.
- Produces: operator instructions and persisted evidence distinguishing seeded/manual auth.

- [ ] **Step 1: Finish the signed-in Claude probe**

From a signed-in real HOME, compare auth-status exit/logged-in boolean for empty HOME and .claude.json-only HOME, discarding all other output. Reconfirm codex current/empty/auth.json-only. Remove every probe root with a validated trap.

- [ ] **Step 2: Post standalone issue evidence**

Record only versions, candidate filenames, safe status booleans/exit codes, cleanup, and inferred minimum. Never post values, hashes, sizes, account identifiers, or tokens.

- [ ] **Step 3: Add concise script evidence comments**

Name probe date/tool versions and why each fixed filename is included. Distinguish macOS Keychain credentials from HOME-scoped Claude state.

- [ ] **Step 4: Update results schema and goldens**

Render Part C auth mode and C.C1 attestation. Update current goldens; keep legacy rendering backward compatible and fabricate nothing.

- [ ] **Step 5: Update checklist and SECURITY.md**

Document filename-only consent, seeded/manual paths, C.C1-C.C3, permissions, and cleanup. Document the fixed allowlist, explicit consent, no-content output, and independent credential-HOME deletion.

- [ ] **Step 6: Run focused gates and commit**

~~~bash
GOCACHE=/tmp/agentctl-go-cache go test ./hack -count=1
shellcheck hack/*.sh
git add hack docs/release-checklist.md SECURITY.md
git commit -S -m "Document Part C authentication isolation"
~~~

### Task 4: Verify, publish, and review

**Files:**
- Verify: .github/workflows/ci.yml
- Create: GitHub PR closing issue #144

- [ ] **Step 1: Run required local gates**

~~~bash
GOCACHE=/tmp/agentctl-go-cache go test -count=1 -timeout=3m ./...
GOCACHE=/tmp/agentctl-go-cache go vet ./...
shellcheck hack/*.sh
GOLANGCI_LINT_CACHE=/tmp/agentctl-golangci-cache GOCACHE=/tmp/agentctl-go-cache golangci-lint run
GOCACHE=/tmp/agentctl-go-cache go test -count=1 -timeout=3m -tags integration ./...
goreleaser check
goreleaser release --snapshot --clean --skip=notarize
./dist/agentctl_darwin_arm64_v8.0/agentctl version
~~~

- [ ] **Step 2: Fetch/rebase current main and rerun affected gates**

Verify every commit signature and rerun go test, go vet, and ShellCheck after any merge-base change.

- [ ] **Step 3: Push and open PR**

Use Closes #144, milestone 0.3.0, empirical evidence link, security impact, red/green evidence, and gate results.

- [ ] **Step 4: Detach and wait for exact PR CI**

Quote only the pull_request run for the exact pushed head after test and integration pass.

- [ ] **Step 5: Obtain reviewer RELEASE**

Send the exact run URL to reviewer via AMQ. Fix every finding in this PR, rerun, push, detach, and re-request. Do not merge.

- [ ] **Step 6: Reply to planner**

Send PR URL, signed head, exact CI run, reviewer comment, and detached-worktree status on the original dispatch.

