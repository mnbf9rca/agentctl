# Detached Release-Verifier Relaunch Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make the default live release walkthrough verify the detached fleet
it actually launches, preserve every diagnostic checkpoint, and make Task 8
execute the exact guarded path.

**Architecture:** Keep the verifier's existing lifecycle ownership and
checkpoint model. Replace Part B's tmux identity probes with the existing
runtime-record resolver, guide two explicit role attachments and a post-relaunch
reattachment, and give every status observation an independent artifact. Extend
the real script fixture at the external command boundary, then add the focused
fixture to Task 8's executed transcripts.

**Tech Stack:** Bash 3-compatible shell, Go standard-library tests using
`os/exec`, existing agentctl/tmux command fixtures.

---

### Task 1: Capture detached walkthrough and evidence failures

**Files:**

- Modify: `hack/releaseverify_test.go`

**Interfaces:**

- Consumes: `liveFixture`, `hack/release-verify.sh --non-interactive`.
- Protects: §15.11.1 detached attach behavior, §15.11.6 stored-presentation
  relaunch, and factual evidence capture.

- [ ] Add a behavior test that runs the full-default fixture and requires
  explicit role `a` and role `b` attachment guidance, post-relaunch role `a`
  reattachment, runtime-record replacement observations, and no Part B tmux
  identity calls.
- [ ] Make the fixture reject bare detached attach and return distinct original
  and replacement runtime records.
- [ ] Add a behavior test that forces a later teardown result and proves it
  cannot replace the precheck/relaunch evidence files.
- [ ] Run the focused tests against current production and record RED from the
  stale tmux/bare-attach/overwritten-capture behavior.

### Task 2: Implement the detached Part B walkthrough

**Files:**

- Modify: `hack/release-verify.sh`
- Modify: `hack/releaseverify_test.go`
- Modify if rendered evidence changes: `hack/testdata/release-verify-live-results.golden`

**Interfaces:**

- Consumes: `resolve_running_role_processes`, `wait_process_absent`,
  `assert_role_state`, lifecycle cleanup helpers.
- Removes from Part B: `resolve_live_session_id`, `resolve_role_window`, and
  pane-derived relaunch evidence.

- [ ] Replace the Part B tmux resolution branch with pre- and post-relaunch
  runtime-record observations.
- [ ] Rewrite operator checkpoints around two explicit role attachments,
  closing the old role `a` viewer after termination, explicit replacement role
  `a` attachment, and terminal-close viewing completion.
- [ ] Allocate unique precheck, relaunch-before, relaunch-after,
  post-viewer-close, and teardown artifacts.
- [ ] Update metadata/result rendering to report only the new observations.
- [ ] Run the focused tests to GREEN.
- [ ] Temporarily restore one tmux identity query and one shared status path;
  require the corresponding tests to fail, then restore GREEN.

### Task 3: Close the Task 8 walkthrough gap

**Files:**

- Modify: `hack/release-verify.sh`
- Modify: `hack/releaseverify_test.go`

**Interfaces:**

- Consumes: Task 8 `task8_run` transcript machinery.
- Produces: a Task 8 artifact from the focused full-default live fixture plus a
  required factual transcript marker.

- [ ] Add a failing executed-transcript test proving Task 8 currently omits
  the focused default-live verifier fixture.
- [ ] Add the focused Go test invocation to Task 8 and require its marker in
  the resulting artifact.
- [ ] Run focused Task 8 and live verifier tests to GREEN.
- [ ] Temporarily omit the Task 8 invocation and require the executed-transcript
  test to fail, then restore GREEN.

### Task 4: Verify, sign, and run the real walkthrough

**Files:**

- Modify only files needed to fix failures caused by Tasks 1–3.

- [ ] Prove the SSH signing path in a throwaway repository and remove it.
- [ ] Run focused tests, `go test ./...`, `go vet ./...`, `shellcheck
  hack/*.sh`, `golangci-lint run`, `go test -tags integration ./...`, and the
  release snapshot checks required by `.github/workflows/ci.yml`.
- [ ] Commit the focused changes with SSH signatures and verify each signature.
- [ ] From the clean signed branch, run `make build && bash
  hack/release-verify.sh` through every default part, perform the displayed
  live actions, and give only observations personally made.
- [ ] Preserve the successful verifier artifact and confirm owned resources
  were removed.

### Task 5: Independent one-attempt verification and PR

**Files:**

- No product changes unless independent verification finds a defect; any such
  fix returns to Tasks 1–4 on this branch.

- [ ] Send the exact signed commit and live evidence location to the planner.
- [ ] Have build1 run the exact operator path once from a fresh worktree and
  login environment; do not open a PR until build1 reports green.
- [ ] Fetch and rebase onto current `main`, rerun required gates, and push the
  single topic branch.
- [ ] Open one draft PR with issue relationship, RED/GREEN evidence, both live
  walkthroughs, and local gates.
- [ ] Wait for the PR's own `pull_request` CI run, then request the reviewer
  gate with the exact run URL. Do not merge the PR.

### Task 6: Prevent cross-namespace collision found by the first live run

**Files:**

- Modify: `hack/release-verify.sh`
- Modify: `hack/releaseverify_test.go`
- Modify: detached relaunch design notes

**Interfaces:**

- Consumes: the random default-verifier evidence root and agentctl's validated
  session-name boundary.
- Protects: repeated live runs from unrelated, pre-existing AMQ co-op state.

- [x] Preserve the failed live artifact and reproduce the hidden child failure
  directly through `amq coop exec`.
- [x] Add a RED fixture in which fixed `relverify` has stale AMQ wake-owner
  state and fails before harness readiness.
- [x] Derive one lowercase run-unique Part B session from the evidence token,
  thread it through every command/output check, and record it in metadata.
- [x] Run affected fixtures to GREEN.
- [x] Temporarily restore `LIVE_SESSION=relverify`; require the stale-AMQ test
  to fail with the reproduced wake-owner diagnostic, then restore GREEN.
- [ ] Repeat full gates, create a signed follow-up commit, and rerun the entire
  real walkthrough before independent verification.

### Task 7: Pre-initialize detached AMQ coordination found by the signed live run

**Files:**

- Modify: `hack/release-verify.sh`
- Modify: `hack/releaseverify_test.go`
- Modify: detached relaunch design notes

**Interfaces:**

- Consumes: `amq coop init`, the random evidence root, and the existing Part B
  exit trap.
- Protects: unattended detached harness startup from interactive AMQ
  auto-initialization.

- [x] Preserve `/tmp/agentctl-release-verify.9JEJ5T` and reproduce the failure
  with a fresh unique session.
- [x] Prove in real iTerm controls that direct AMQ coop and exact detached
  agentctl launch succeed after explicit initialization.
- [x] Capture RED when launch requires a prepared `.amqrc` and none exists.
- [x] Initialize a temporary AMQ root when `.amqrc` is absent; preserve an
  existing config byte-for-byte.
- [x] Arm cleanup before initialization, identity-pin owned paths, and cover
  successful cleanup plus partial-init failure cleanup.
- [x] Capture RED for successful kill followed by indeterminate status in both
  normal teardown and the unexpected-exit trap; retain AMQ artifacts until a
  distinct status observation proves absence.
- [x] Mutate away the absence gate and require both cleanup-order regressions to
  fail, then restore focused GREEN.
- [x] Complete independent re-review with no remaining Critical, Important, or
  Minor findings.
- [x] Complete focused, full Go, vet, race, real-tmux integration, ShellCheck,
  lint, govulncheck, GoReleaser snapshot, archive, and binary-smoke gates.
- [ ] Sign the focused follow-up and rerun the exact complete live walkthrough.
