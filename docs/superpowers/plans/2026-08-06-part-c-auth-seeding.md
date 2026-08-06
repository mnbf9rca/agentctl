# Part C Authentication Seeding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Make the isolated Part C walkthrough seed every empirically proven HOME credential after explicit filename-only consent, guide the remaining sign-ins, and delete every copied credential on all exits.

**Architecture:** Keep the real HOME isolated from every harness process. On macOS, offer only the empirically proven `~/.codex/auth.json`, print only that ~/ name, and copy it with restrictive modes after consent. Claude has no proven HOME-file seed and always receives guided interactive sign-in; declining Codex seeding offers fully manual sign-in. Remove the temporary HOME independently of tmux/session cleanup so a retained retry root contains no credentials.

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
- Produces: PART_C_AUTH_MODE (`codex-seeded` or `manual`) and an isolated HOME containing only the consented proven Codex file.

- [ ] **Step 1: Make the fixture auth-safe**

Create a mode-0700 fake operator HOME containing literal fake auth files and set HOME to it before the verifier runs. Add a launch-boundary log with filenames and modes only.

- [ ] **Step 2: Write the failing consent-yes test**

Add TestLiveVerificationPartCConsentSeedsOnlyProvenCodexAuthFile. Assert the transcript lists exactly the proven Codex filename before consent, states Claude requires interactive sign-in, contains no fake body, rejects both Claude lookalike paths, and launch observes only .codex/auth.json at mode 600 beneath mode-700 parents.

- [ ] **Step 3: Capture RED**

Run:

~~~bash
GOCACHE=/tmp/agentctl-go-cache go test ./hack -run TestLiveVerificationPartCConsentSeedsOnlyProvenCodexAuthFile -count=1 -v
~~~

Expected: FAIL because no consent prompt or auth copies exist.

- [ ] **Step 4: Implement the fixed allowlist**

Enumerate only this proven source/destination pair:

~~~text
~/.codex/auth.json          -> PART_C_HOME/.codex/auth.json
~~~

Print the source name, call ask once, create the required directory with install -d -m 0700, and copy the file with install -m 0600. Any copy error aborts through Part C teardown. Never present or copy a Claude file without a new successful sufficiency probe.

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
- Produces: operator instructions and persisted evidence distinguishing `codex-seeded` from fully manual auth.

- [ ] **Step 1: Record the bounded harness probes**

Compare only auth-status exits/logged-in booleans, discarding all other output. Codex current/empty/auth.json-only proves auth.json sufficient. From an authenticated Claude source HOME, empty HOME, `.claude.json`-only, `settings.json`-only, and both candidates together remain logged out; macOS Keychain storage therefore leaves guided interactive sign-in as the fail-closed branch. Remove every probe root with a validated trap.

- [ ] **Step 2: Post standalone issue evidence**

Record only versions, candidate filenames, safe status booleans/exit codes, cleanup, and inferred minimum. Never post values, hashes, sizes, account identifiers, or tokens.

- [ ] **Step 3: Add concise script evidence comments**

Name probe date/tool versions and why only the Codex filename is included. Distinguish macOS Keychain credentials from non-credential Claude HOME state.

- [ ] **Step 4: Update results schema and goldens**

Render Part C auth mode and C.C1 attestation. Update current goldens; keep legacy rendering backward compatible and fabricate nothing.

- [ ] **Step 5: Update checklist and SECURITY.md**

Document filename-only consent, codex-seeded/manual paths, C.C1-C.C3, permissions, and cleanup. Document the one-file fixed allowlist, explicit consent, no-content output, mandatory fresh-HOME Claude sign-in, and independent credential-HOME deletion.

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
