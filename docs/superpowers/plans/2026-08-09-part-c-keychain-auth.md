# Part C Keychain Authentication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make release-verifier Part C authenticate Claude Code either through an explicitly consented exact Keychains symlink or through guided sign-in backed by an isolated login keychain, without copying Claude credentials or risking the real keychain target during teardown.

**Architecture:** Preserve the existing Part C state machine and keep Codex `auth.json` consent independent. Add explicit Claude authentication state and exact-link ownership, create the selected Claude prerequisite before fleet launch, and tear down in the safety order fleet/socket → exact symlink → temporary HOME/root. Fixture stubs observe the filesystem and exact `security` invocation while target sentinels prove cleanup never follows the symlink.

**Tech Stack:** Bash 3.2-compatible shell, macOS `security`, tmux, Go standard-library fixture tests, Markdown.

## Global Constraints

- The issue #148 body, including acceptance criteria, is the complete task contract.
- Claude evidence is version-pinned to Claude Code `2.1.226`.
- Never seed `CLAUDE_CODE_OAUTH_TOKEN`; it can silently delete the real Keychain credential on exit.
- Consent must name the exact `REAL_HOME/Library/Keychains -> TEMP_HOME/Library/Keychains` symlink and state that probe harnesses can reach the operator login keychain through it.
- The verifier copies no Claude credential and removes only the link, never its target.
- Link ownership is recorded only after successful creation; teardown never removes an unowned path.
- Teardown kills the fleet and named tmux server before link removal, then removes the temporary HOME/root.
- Declining both Claude paths aborts before AMQ initialization, skill installation, or fleet launch.
- Keep the Go module on the standard library and keep shellcheck clean.

---

### Task 1: Fixture contract for keychain consent and isolated fallback

**Files:**
- Modify: `hack/releaseverify_test.go`
- Test: `hack/releaseverify_test.go`

**Interfaces:**
- Consumes: `newLiveFixture`, `liveFixture.run`, and Part C prompt sequencing.
- Produces: fixture paths for the operator Keychains target, target sentinel, security-call log, and launch-time keychain observation.

- [ ] **Step 1: Extend the fixture without changing production behavior**

Create `operatorHome/Library/Keychains` with a sentinel file. Add a `security` stub that accepts only:

```text
create-keychain -p  <PART_C_HOME>/Library/Keychains/login.keychain-db
```

and logs `argv`, `HOME`, and the created path. Extend the fake launch observation to record whether `HOME/Library/Keychains` is a symlink, its exact target, or an isolated directory containing `login.keychain-db`.

- [ ] **Step 2: Write failing consent-path tests**

Assert that the transcript names the exact source and destination, states that both probe harnesses can reach the operator login keychain, and never prints credential contents. Assert the launch sees the exact symlink plus the independently copied Codex file and no `security create-keychain` call.

- [ ] **Step 3: Write failing fallback/refusal tests**

Assert that declining the Claude link offers guided sign-in, invokes `security create-keychain` under the temporary HOME before launch, and describes minting a fresh token. Assert declining both Claude paths aborts before AMQ/install/launch even when Codex copy was accepted.

- [ ] **Step 4: Write failing cleanup tests**

On success, checkpoint refusal, launch failure, attach failure, and retained-root cleanup failure, assert the exact link is absent and the real target directory plus sentinel survive. Include link-creation failure and isolated-keychain-creation failure as pre-launch aborts.

- [ ] **Step 5: Run the focused tests and record RED**

Run:

```bash
go test ./hack -run 'TestLiveVerificationPartC' -count=1
```

Expected: FAIL because the current verifier neither offers/creates the symlink nor invokes `security create-keychain`.

### Task 2: Minimal Part C implementation

**Files:**
- Modify: `hack/release-verify.sh`
- Test: `hack/releaseverify_test.go`

**Interfaces:**
- Consumes: captured real HOME, temporary Part C HOME, selected Codex state, and exact fixture expectations from Task 1.
- Produces: `PART_C_CLAUDE_AUTH_MODE`, `PART_C_KEYCHAIN_LINK`, `PART_C_KEYCHAIN_LINK_OWNED`, `part_c_link_keychains`, `part_c_create_isolated_keychain`, and exact-link cleanup.

- [ ] **Step 1: Add explicit Claude source and ownership state**

Capture the fixed source `$PART_C_ORIGINAL_HOME/Library/Keychains`, fixed destination `$PART_C_HOME/Library/Keychains`, and a zero-valued ownership flag. Detect only a real source directory; do not accept or follow a caller-supplied path.

- [ ] **Step 2: Implement exact-link consent**

Print the exact `ln -s SOURCE DESTINATION` relationship and its access consequence. Create only `$PART_C_HOME/Library` plus the Keychains symlink. Set ownership only after `ln -s` succeeds and the destination is observed as a link.

- [ ] **Step 3: Implement isolated-keychain fallback**

When link consent is declined or no source directory exists, ask for guided sign-in. After consent, create mode-`0700` `Library/Keychains` and invoke the captured macOS `security` executable as:

```bash
HOME="$PART_C_HOME" "$PART_C_REAL_SECURITY" create-keychain -p '' "$PART_C_HOME/Library/Keychains/login.keychain-db"
```

Abort with a fixed diagnostic before AMQ/install/launch if resolution or creation fails.

- [ ] **Step 4: Update launch guidance and evidence metadata**

Distinguish linked-headless Claude from isolated-keychain guided Claude, and retain Codex-seeded/manual wording independently. Update the render allowlist so metadata reports factual combined authentication modes.

- [ ] **Step 5: Implement safety-ordered cleanup**

After the owned fleet and socket are gone and the original environment is restored, remove only the owned exact symlink with a non-recursive command and verify it is absent. Clear ownership only after observation. Then remove the temporary HOME/root as before; retain retry state on failures.

- [ ] **Step 6: Run focused tests and record GREEN**

Run:

```bash
go test ./hack -run 'TestLiveVerificationPartC' -count=1
```

Expected: PASS.

### Task 3: Release and security documentation

**Files:**
- Modify: `docs/release-checklist.md`
- Modify: `SECURITY.md`
- Test: `hack/releaseverify_test.go`

**Interfaces:**
- Consumes: the exact behavior implemented in Task 2.
- Produces: operator-facing Part C instructions and security boundary wording matching the script.

- [ ] **Step 1: Update documentation minimally**

Replace obsolete “no sufficient Claude seed” wording with the exact symlink mechanism, isolated-keychain decline path, teardown ordering, and manual keychain-locked fallback. Do not add `CLAUDE_CODE_OAUTH_TOKEN` to any executable path.

- [ ] **Step 2: Review prose directly against issue #148**

Human-facing prose earns direct contract review rather than a source-grep test. Confirm the checklist and SECURITY text name the exact Keychains link, no-copy property, access consequence, teardown obligation, isolated fresh-token fallback, `CLAUDE_CODE_OAUTH_TOKEN` prohibition with issue reference, `claude setup-token` SSH/launchd fallback, and Claude Code `2.1.226` evidence pin.

- [ ] **Step 3: Run focused behavior tests**

Run:

```bash
go test ./hack -count=1
```

Expected: PASS.

### Task 4: Mechanism execution and full verification

**Files:**
- Modify only if a failing test or mechanism execution reveals a defect.

**Interfaces:**
- Consumes: completed script, fixture suite, release checklist, and SECURITY wording.
- Produces: live evidence that both authored mechanisms work from inside isolated tmux without affecting the default server or real keychain target.

- [ ] **Step 1: Execute the symlink mechanism inside isolated tmux**

Use a throwaway HOME and throwaway target sentinel, run the exact link/create/cleanup sequence inside a named isolated tmux socket, and observe the link during the pane lifetime, link absence afterward, and target/sentinel survival.

- [ ] **Step 2: Execute the isolated-keychain mechanism inside isolated tmux**

Run the exact `security create-keychain` command with a throwaway HOME from inside a named isolated tmux socket, observe the keychain under that HOME, then delete only the throwaway resources. Do not point the probe at the real operator Keychains directory.

- [ ] **Step 3: Run repository gates**

Run:

```bash
go test ./...
go vet ./...
shellcheck hack/*.sh
golangci-lint run
go test -tags integration ./...
goreleaser check
goreleaser release --snapshot --clean --skip=notarize
```

Smoke-test the generated `agentctl` binary and record exact versions/results.

### Task 5: Current-main PR and reviewer gate

**Files:**
- No source changes unless rebase or review exposes a conflict/defect.

**Interfaces:**
- Consumes: signed topic commits and passing local evidence.
- Produces: ready-for-review PR closing #148, exact merge-result CI URL, independent reviewer gate, detached worktree, and planner handoff.

- [ ] **Step 1: Commit focused signed changes**

Stage only issue #148 files, create SSH-signed commits, and inspect every signature with `git log --show-signature`.

- [ ] **Step 2: Fetch and rebase current main**

Fetch `origin/main`, rebase, rerun affected/full gates, and force-push only with lease if required.

- [ ] **Step 3: Publish and mark ready**

Open a PR whose body says `Closes #148`, summarizes both rejected approaches, and records RED/GREEN plus mechanism evidence. Mark it ready for review before requesting the gate.

- [ ] **Step 4: Verify exact PR CI**

Wait for the PR's own `pull_request` workflow run testing the merge result; quote its exact URL in the gate request.

- [ ] **Step 5: Obtain gate and hand off**

Request review through AMQ, fix every finding in this PR, obtain a PR-comment verdict that states blocking status, detach the worktree, do not merge, and send the planner the signed head, exact CI URL, verdict URL, and detached-worktree status.
