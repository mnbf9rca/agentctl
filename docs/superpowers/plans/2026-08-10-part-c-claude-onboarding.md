# Part C Claude Onboarding Configuration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make release-verifier Part C reuse the consented Claude Keychain authentication without requiring re-authentication by synthesizing the minimal proven onboarding configuration in the temporary HOME.

**Architecture:** Preserve the existing independent Codex and Claude consent state machine. On the consented Keychains-link path only, synthesize a mode-`0600` `$PART_C_HOME/.claude.json` containing exactly `{"hasCompletedOnboarding":true}` before fleet launch; never read or copy the operator's Claude configuration. Keep the isolated-keychain path unchanged, and align the C.C1 evidence question and documentation with the narrow no-reauthentication bar.

**Tech Stack:** Bash 3.2-compatible shell, Go standard-library fixture tests, tmux, macOS Keychain, Markdown.

## Global Constraints

- Issue #188, including its consolidated `AMENDED 2026-08-10` section, is the complete task contract.
- Empirical evidence is pinned to Claude Code `2.1.226` and tmux `3.7b` on macOS.
- Hold the exact Keychains link mechanism from #148 unchanged; it remains the credential-access path.
- Synthesize exactly `{"hasCompletedOnboarding":true}` with mode `0600`; never read, inspect, or copy the operator's real `~/.claude.json`.
- Seed the onboarding file only on the consented-link path; the isolated-keychain path remains interactive by design and receives no `.claude.json`.
- Preserve the test invariant that `.claude.json` is never offered as an authentication mechanism; narrow the predicate rather than deleting it.
- C.C1 records whether the harnesses started without requiring re-authentication; onboarding chrome such as theme or workspace trust is out of scope.
- Preserve the dated 2026-08-09 C.C1 `y` and its `(keychain-linked, codex-seeded)` parenthetical; add a planner annotation rather than rewriting machine evidence.
- Keep the Go module on the standard library and keep every existing Keychain cleanup invariant.

---

### Task 1: Pin the synthesized-config contract in fixture tests

**Files:**
- Modify: `hack/releaseverify_test.go:192-207`
- Modify: `hack/releaseverify_test.go:235-269`
- Modify: `hack/releaseverify_test.go:317-340`
- Test: `hack/releaseverify_test.go:959-1007`
- Test: `hack/releaseverify_test.go:1059-1099`

**Interfaces:**
- Consumes: `newLiveFixture`, the fake `agentctl launch` boundary, `authObservationLog`, and `assertPartCCredentialHomeAbsent`.
- Produces: a `claudeConfigLog` fixture observation containing the exact launch-time bytes of `$HOME/.claude.json`, plus exact consented-link and isolated-keychain assertions.

- [ ] **Step 1: Extend the launch fixture to observe exact synthesized bytes**

Add `claudeConfigLog string` to `liveFixture`, create its path beside `authObservationLog`, export it as `AGENTCTL_TEST_CLAUDE_CONFIG_LOG`, and return it. In the fake `launch --session skillverify` branch, when `$HOME/.claude.json` is a regular file, copy only that fixture file into the observation log before returning. The fixture operator HOME continues to contain `fakeClaudeAuthBody`, so an exact synthesized-content assertion proves production did not copy it.

- [ ] **Step 2: Make the consented-link test demand the new behavior**

Update `TestLiveVerificationPartCConsentSeedsCodexAndLinksExactClaudeKeychains` so its ordered guidance requires:

```text
Part C will synthesize this minimal Claude onboarding configuration:
<PART_C_HOME>/.claude.json
It contains onboarding state only, not credentials, and does not copy the operator's Claude configuration.
Create exactly this Claude Keychains symlink and synthesized onboarding configuration: <SOURCE> -> <DESTINATION>?
Did both harnesses start without requiring re-authentication?
```

Change the exact `wantObservation` to insert `file .claude.json|600` immediately after `dir .|700`, as required by the fixture walk order. Assert `claudeConfigLog` equals `{"hasCompletedOnboarding":true}\n` exactly.

- [ ] **Step 3: Narrow and preserve the authentication-mechanism guard**

Replace the broad ban on every `.claude.json` mention with explicit forbidden authentication offers such as `Part C can seed Claude authentication from`, `Copy only this Claude file`, and the operator source path `filepath.Join(fixture.operatorHome, ".claude.json")`. Retain the existing bans on `~/.claude/.credentials.json`, `CLAUDE_CODE_OAUTH_TOKEN`, and all fake credential bodies. Pair the negative guard with the exact positive “onboarding state only, not credentials” guidance from Step 2.

- [ ] **Step 4: Pin the isolated-keychain non-seeding rule**

Keep the `wantObservation` block in `TestLiveVerificationPartCConsentDeclineGuidesManualSignIn` byte-identical. Add a separate assertion that `claudeConfigLog` does not exist on this path, proving link-declined/manual sign-in never receives the onboarding file.

- [ ] **Step 5: Run the focused test and record RED**

Run:

```bash
go test ./hack -run 'TestLiveVerificationPartC(ConsentSeedsCodexAndLinksExactClaudeKeychains|ConsentDeclineGuidesManualSignIn)$' -count=1
```

Expected: FAIL because the current verifier does not synthesize `.claude.json`, does not emit the config distinction, and still asks the old zero-manual-interaction question.

### Task 2: Synthesize onboarding state on the consented-link path

**Files:**
- Modify: `hack/release-verify.sh:182-217`
- Modify: `hack/release-verify.sh:1293-1346`
- Modify: `hack/release-verify.sh:1394-1405`
- Modify: `hack/release-verify.sh:1432-1444`
- Test: `hack/releaseverify_test.go`

**Interfaces:**
- Consumes: `PART_C_HOME`, `PART_C_KEYCHAIN_SOURCE`, `PART_C_KEYCHAIN_LINK`, successful exact-link consent, and existing Part C teardown.
- Produces: `part_c_seed_claude_onboarding`, exact synthesized contents/mode at launch, revised linked-path guidance, and the no-reauthentication C.C1 observation.

- [ ] **Step 1: Add the minimal synthesizer**

Add this Bash-3.2-compatible helper next to `part_c_link_keychains`:

```bash
part_c_seed_claude_onboarding() {
  (
    umask 077
    printf '%s\n' '{"hasCompletedOnboarding":true}' >"$PART_C_HOME/.claude.json"
  )
}
```

The fresh mode-`0700` temporary HOME is the fixed parent; the helper accepts no path or content arguments and never reads the operator HOME.

- [ ] **Step 2: Put synthesis inside the existing link consent boundary**

Before the Claude link question, print the fixed destination and state that the synthesized file contains onboarding state only, not credentials, and does not copy the operator configuration. Change the question to consent to both the exact symlink and synthesized onboarding file. After `part_c_link_keychains` succeeds, call `part_c_seed_claude_onboarding`; on failure, record a fixed C.2 failure and abort through existing teardown so the owned link and temporary HOME are removed. Set `PART_C_CLAUDE_AUTH_MODE=keychain-linked` only after both operations succeed.

- [ ] **Step 3: Reword every contract-named linked-path observation**

In the linked-Claude/manual-Codex guidance, say Claude should start without requiring re-authentication while Codex still requires manual sign-in. In the both-seeded guidance, say neither harness should require re-authentication. For the both-seeded C.C1 branch, set both `auth_expected` and `auth_prompt` to the exact no-reauthentication observation. Preserve the isolated-keychain C.C1 branches because interactive sign-in remains their designed behavior.

- [ ] **Step 4: Run focused tests and record GREEN**

Run:

```bash
go test ./hack -run 'TestLiveVerificationPartC' -count=1
```

Expected: PASS, including exact mode/content, consent ordering, unchanged isolated-path observation, and cleanup absence.

### Task 3: Align security, checklist, and dated evidence

**Files:**
- Modify: `SECURITY.md:91-132`
- Modify: `docs/release-checklist.md:196-258`
- Modify: `docs/release-verification-notes.md:33-67`
- Test: `hack/releaseverify_test.go`

**Interfaces:**
- Consumes: the exact behavior implemented in Task 2 and the phase-1 probe matrix.
- Produces: security boundary text, an operator runbook matching the no-reauthentication bar, and a planner-authored annotation preserving dated evidence.

- [ ] **Step 1: Add a self-contained SECURITY.md config subsection**

Immediately after the existing Claude Keychains mechanism paragraph, state that the consented-link path also writes a mode-`0600` synthesized `.claude.json` containing only `hasCompletedOnboarding:true`; it is non-secret onboarding configuration, not authentication material, and no real Claude config/account/project/MCP data is read or copied. State that the file is absent from the isolated-keychain path and is removed with the temporary HOME on every cleanup path.

- [ ] **Step 2: Update Part C checklist mechanics and C.C1 wording**

Describe the synthesized file beside the exact Keychains link, its config-versus-credentials distinction, link-only scope, and mode. Replace the old “zero manual pane interaction” acceptance with “started without requiring re-authentication”; keep theme/workspace-trust chrome outside that claim and keep isolated-keychain guided sign-in unchanged.

- [ ] **Step 3: Annotate the 2026-08-09 evidence block without rewriting it**

Extend the existing planner-authored note after the machine-written block with a short #188 paragraph: the recorded C.C1 `y` and `(keychain-linked, codex-seeded)` label remain factual for the then-worded authentication checkpoint, but Claude still required re-login; future C.C1 evidence separately asks whether startup required re-authentication. Do not edit any line inside the dated block.

- [ ] **Step 4: Review prose against the consolidated issue**

Search all three files and the script/test sites for stale linked-path claims about “zero manual interaction” or `.claude.json` as authentication. Confirm every no-reauthentication claim is limited to the consented-link path and the 2026-08-09 machine block is byte-identical.

- [ ] **Step 5: Run the complete hack suite**

Run:

```bash
go test ./hack -count=1
```

Expected: PASS.

### Task 4: Verify, publish, and request the independent gate

**Files:**
- Modify only if a failing gate exposes an issue #188 defect.

**Interfaces:**
- Consumes: completed script, fixture tests, docs, signed topic commits, and current `origin/main`.
- Produces: a ready PR closing #188, its own merge-result CI URL, reviewer gate request, planner status, and detached worktree.

- [ ] **Step 1: Run all five local gates**

Run:

```bash
go test ./...
go vet ./...
shellcheck hack/*.sh
golangci-lint run
go test -tags integration ./...
```

Expected: all five exit 0. Run release snapshot checks only if the changed verifier/docs surface makes them relevant under `.github/workflows/ci.yml`.

- [ ] **Step 2: Commit focused signed changes**

Stage only issue #188 files, create focused SSH-signed commits, and inspect them with `git log --show-signature`. The host signing path was already proven by #198; do not create a new throwaway proof.

- [ ] **Step 3: Rebase onto current main and re-verify**

Fetch `origin/main`, rebase the topic branch, resolve the expected self-contained `SECURITY.md` overlap if #194 has merged, and rerun the affected tests plus all five gates. Force-push only with lease if the already-pushed history changes.

- [ ] **Step 4: Open a ready PR with empirical evidence**

Push the topic branch and open a ready PR whose body says `Closes #188`, records the Claude Code `2.1.226` matrix (missing/empty/false require OAuth re-login; true reaches authenticated ready state with the Keychains link), describes the config-versus-credentials boundary, and includes RED/GREEN plus five-gate evidence.

- [ ] **Step 5: Quote the PR's own merge-result CI and request review**

Wait for the PR's `pull_request` workflow, capture its exact run URL, then send `review_request` to `reviewer` with the PR URL and run URL. Send a status copy to `planner`. Do not merge.

- [ ] **Step 6: Detach the worktree after opening the PR**

Detach `.worktrees/issue-188-onboarding-seed` so the branch is not held at merge time, while retaining enough local context to address review findings through a reattached or fresh worktree if needed.
