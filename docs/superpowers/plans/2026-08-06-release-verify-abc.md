# Release Verification A+B+C Walkthrough Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `hack/release-verify.sh` walk an unfamiliar operator through release verification Parts A, B, and C, fail closed on rejected checkpoints, clean isolated Part C resources on every exit, and append factual A/B/C evidence.

**Architecture:** Preserve the existing probe, live-delivery, relaunch, teardown, measure, and results-rendering behavior. Add small shell presentation/checkpoint helpers plus a single cleanup coordinator that tracks ownership of `relverify` and the Part C named-socket fleet independently. The existing Go fixture will drive `--non-interactive` through real script execution with stub executables and assert output, exact command order, filesystem cleanup, refusal behavior, and evidence text.

**Tech Stack:** Bash 3-compatible shell, Go standard-library tests using `os/exec`, tmux 3.7b contract, shellcheck 0.11.0.

## Global Constraints

- The current body of GitHub issue #142 is the complete task contract; the approved design spec and `SECURITY.md` remain authoritative.
- Keep the Go module on the standard library and do not add dependencies.
- The verifier must use only throwaway tmux sockets and temporary HOME/project directories; it must never touch the operator's real HOME or default tmux server during Part C.
- Script output reports only observations the script made. Human judgments are recorded as `operator confirmed: ...`; `n` fails the run and names the refused checkpoint.
- Part C setup and teardown must implement the existing `docs/release-checklist.md` blocks: temporary HOME, tmux shim, named socket, `amq coop init`, `agentctl skill install`, harness launch/attach, kill-server, environment restoration, and temporary-root removal.
- Trap-based teardown must run after abort, prompt refusal, command failure, or success and must not silently skip an expected cleanup action.
- Keep the `## Results history` insertion marker in `docs/release-verification-notes.md` unchanged.
- Use fixture-driven TDD: run each new test red against the old script, then implement the smallest behavior that makes it green.
- Required final gates are `go test ./...`, `go vet ./...`, `shellcheck hack/*.sh` with shellcheck 0.11.0, `golangci-lint run`, and `go test -tags integration ./...` plus release snapshot checks when relevant.

---

### Task 1: Implement and fixture-test the complete A+B+C walkthrough

**Files:**

- Modify: `hack/releaseverify_test.go`
- Modify: `hack/release-verify.sh`
- Modify: `hack/testdata/release-verify-live-results.golden`

**Interfaces:**

- Consumes: existing `ask`, probe assertion, exact-ID relaunch, `render_results`, results-history insertion, and live fixture helpers.
- Produces: `bash hack/release-verify.sh --non-interactive`, which reads one `y`/`n` answer per human checkpoint from stdin while still printing every question and expected observation; distinct A/B/C evidence lines; trap-clean Part C resources.

- [ ] **Step 1: Extend the fixture before production code**

Add fixture support for the Part C commands without mocking the verifier itself:

```go
type liveFixture struct {
    dir          string
    agentctlLog  string
    tmuxLog      string
    amqLog       string
    skillRootLog string
}
```

The fixture stubs must record the real command boundary the script invokes:

```text
amq coop init --agents a,b,user
agentctl skill install
agentctl launch --session skillverify --roles a:claude,b:codex --dir <temporary-project>
agentctl attach --session skillverify
agentctl kill --session skillverify
tmux -L agentctl-skill-verify-<pid> kill-server
```

Have the `skill install` stub create both installed-skill directories below the temporary HOME so cleanup is observable. Have `fixture.run` invoke `bash hack/release-verify.sh --non-interactive`.

- [ ] **Step 2: Add failing tests for the successful walkthrough**

Add or update tests that execute the fixture with enough `y\n` answers and require all of these observable outcomes:

```go
for _, want := range []string{
    "=== Part A — Automated release checks ===",
    "[PASS A.",
    "=== Part B — Live release-candidate delivery ===",
    "Expected output:",
    "operator confirmed:",
    "=== Part C — Live skill discovery and meaning ===",
    "harness lists the agentctl skill",
    "probe answer matches references/status-states.md",
    "ALL VERIFIED — evidence appended",
} {
    if !strings.Contains(output, want) {
        t.Fatalf("output missing %q:\n%s", want, output)
    }
}
```

Assert each `probe-*.sh` has its own `[PASS A.N]` completion line, the evidence block contains separate `- Part A:`, `- Part B:`, and `- Part C:` lines, and Part B/C human facts use `operator confirmed` wording.

- [ ] **Step 3: Run the focused successful-path tests and capture RED**

Run:

```bash
GOCACHE=/tmp/agentctl-go-cache go test ./hack -run 'TestLiveVerificationCompletesAndAppendsEvidence|TestLiveVerification.*PartC|TestRenderResultsMatchesGolden' -count=1 -v
```

Expected: FAIL because `--non-interactive`, numbered A/B/C output, Part C execution, and A/B/C evidence do not exist.

- [ ] **Step 4: Add failing refusal and cleanup tests**

Cover at least these failures through script execution:

```text
operator refused checkpoint: <checkpoint name>
```

- Reject the Part B attach-narration checkpoint with `n`; assert no later Part B command runs and `relverify` teardown is attempted.
- Reject the first Part C checkpoint with `n`; assert `skillverify` kill, named-socket `kill-server`, original cwd/HOME/PATH restoration as observed by the fixture, and removal of the temporary Part C root.
- Force a command failure after Part C HOME/PATH/cwd change but before its checkpoints; assert the same teardown observations.
- Assert neither success nor refusal invokes bare/default-socket `tmux kill-server`.

- [ ] **Step 5: Run the refusal/cleanup tests and capture RED**

Run:

```bash
GOCACHE=/tmp/agentctl-go-cache go test ./hack -run 'TestLiveVerification.*(Reject|Abort|Cleanup|PartC)' -count=1 -v
```

Expected: FAIL because Part C is not scripted and no Part C trap state exists.

- [ ] **Step 6: Implement presentation and non-interactive checkpoint helpers**

Update usage to document `--non-interactive`. Parse it only for the default live walkthrough, keeping `--measure` and the pure test subcommands compatible. Add helpers with stable observable output:

```bash
part_header() { printf '\n=== Part %s — %s ===\n' "$1" "$2"; }
step_start()  { printf '\n[%s] %s\n' "$1" "$2"; }
step_pass()   { printf '[PASS %s] %s\n' "$1" "$2"; }
step_fail()   { printf '[FAIL %s] %s\n' "$1" "$2" >&2; }
```

Make checkpoints print a checkpoint name, an `Expected output:` quoted block, and the prompt. A `y` result prints `operator confirmed: <checkpoint>`; `n` prints `operator refused checkpoint: <checkpoint>` and returns failure. In interactive mode read from the terminal as today; in `--non-interactive`, read the same validated answers from stdin without suppressing prompt text.

- [ ] **Step 7: Give every Part A and Part B action a numbered result**

Part A must print a pass/fail line immediately after build/version capture, after every individual probe assertion, and after the no-surviving-probe-server check. Part B must retain exact commands and existing exact-ID/relaunch assertions, add clear step identifiers and quoted expectations to each human checkpoint, and label automated observations separately from operator confirmations.

- [ ] **Step 8: Implement Part C setup, checkpoints, and trap teardown**

Translate the existing checklist blocks directly. Track Part C ownership only after each resource is created. The teardown function must be safe when partially initialized and must:

```bash
"$PART_C_TOP/bin/agentctl" kill --session skillverify
"$PART_C_REAL_TMUX" -L "$PART_C_SOCKET" kill-server
cd "$PART_C_TOP"
export HOME="$PART_C_ORIGINAL_HOME"
export PATH="$PART_C_ORIGINAL_PATH"
rm -rf -- "$PART_C_ROOT"
```

Run setup as numbered steps: make temporary directories, create/chmod the tmux shim, switch HOME/PATH/cwd, initialize AMQ, install the skill, launch the fleet, print/run attach guidance, and verify cleanup. Pause at exactly the two Part C human-judgment checkpoints named by the issue, with the expected inventory and `ambiguous` meaning quoted from the checklist/status-state reference.

- [ ] **Step 9: Extend metadata and rendering without breaking insertion**

Add explicit metadata fields for Part A/B/C results and the two Part C operator confirmations. Render distinct markdown lines, for example:

```markdown
- Part A: PASS — automated probes and isolation checks completed
- Part B: PASS — operator confirmed: attach narration and live delivery/relaunch checkpoints
- Part C: PASS — operator confirmed: harness lists the agentctl skill; probe answer matches references/status-states.md
```

Keep the existing detail lines where they remain useful and keep the results-history marker match exact.

- [ ] **Step 10: Run focused GREEN and the complete hack package**

Run:

```bash
GOCACHE=/tmp/agentctl-go-cache go test ./hack -run 'TestLiveVerification|TestRenderResults' -count=1 -v
GOCACHE=/tmp/agentctl-go-cache go test ./hack -count=1
shellcheck hack/release-verify.sh
```

Expected: PASS, with shellcheck reporting no findings.

- [ ] **Step 11: Commit Task 1 with red/green evidence in the report**

```bash
git add hack/release-verify.sh hack/releaseverify_test.go hack/testdata/release-verify-live-results.golden
git commit -S -m "Walk release verification through Parts A to C"
```

### Task 2: Reframe the release checklist around the wrapper

**Files:**

- Modify: `docs/release-checklist.md`

**Interfaces:**

- Consumes: the Task 1 default `bash hack/release-verify.sh` interactive walkthrough and `--non-interactive` fixture-only path.
- Produces: an operator runbook that directs Parts A–C through the wrapper while retaining manual commands only as a clearly labeled fallback/appendix.

- [ ] **Step 1: Update Parts A–C around one wrapper entrypoint**

State that an operator starts the default walkthrough with:

```bash
bash hack/release-verify.sh
```

Explain that the wrapper numbers every action, prints the exact command/expected observation before human checkpoints, records confirmations as operator claims, and automatically tears down Part B and C resources on success, refusal, interrupt, or failure.

- [ ] **Step 2: Preserve load-bearing live criteria**

Keep the exact attach narration, clear/compact judgments, exact-ID relaunch/provenance contract, `ambiguous` meaning, named-socket isolation, and evidence-append expectations. Move copy/paste setup/teardown blocks to a section titled `Manual fallback for Parts A–C` and label it as troubleshooting/fallback rather than the normal path.

- [ ] **Step 3: Self-review documentation against issue #142**

Verify the runbook assumes no prior checklist memory, never instructs Part C against the default tmux server or real HOME, describes `n` as a named failing operator claim, and keeps Part D/promotion plus notarization unchanged.

- [ ] **Step 4: Run documentation-adjacent verification**

Run:

```bash
GOCACHE=/tmp/agentctl-go-cache go test ./hack -count=1
shellcheck hack/*.sh
git diff --check
```

Expected: PASS and no whitespace errors.

- [ ] **Step 5: Commit Task 2**

```bash
git add docs/release-checklist.md
git commit -S -m "Center the release runbook on the verifier"
```

### Task 3: Run repository gates and prepare review evidence

**Files:**

- Modify only files required to fix a gate failure caused by Tasks 1–2.

**Interfaces:**

- Consumes: completed Task 1 and Task 2 commits.
- Produces: fresh local gate evidence suitable for the PR body and reviewer request.

- [ ] **Step 1: Run the complete required local gates**

```bash
GOCACHE=/tmp/agentctl-go-cache go test ./...
GOCACHE=/tmp/agentctl-go-cache go vet ./...
shellcheck hack/*.sh
golangci-lint run
GOCACHE=/tmp/agentctl-go-cache go test -tags integration ./...
goreleaser check
goreleaser release --snapshot --clean --skip=notarize
```

- [ ] **Step 2: Confirm the snapshot binary reports a stamped version**

Run the same smoke check as `.github/workflows/ci.yml` against `./dist/agentctl_darwin_arm64*/agentctl version`; require output beginning with `agentctl v`.

- [ ] **Step 3: Inspect branch scope and signatures**

```bash
git status -sb
git diff --check origin/main...HEAD
git log --show-signature --oneline origin/main..HEAD
```

Require a clean worktree, only issue #142 files in scope, and good signatures on every commit.

- [ ] **Step 4: Record verification in the task report**

Record each command, exit status, and relevant pass line. Do not claim a gate that was skipped or failed.
