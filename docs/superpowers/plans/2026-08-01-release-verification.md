# Release Verification Tooling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Commit repeatable tmux probes and real-harness release verification that records the 2026-08-01 injection baseline and measures the popup-settle floor under reproducible CPU load.

**Architecture:** Four independent `hack/probe-*.sh` scripts preserve the reviewer-posted tmux experiments with interruption-safe throwaway-server cleanup. One Bash 3.2-compatible `hack/verify-injection.sh` owns real-harness startup, snapshot capture, fixed-delay verification, CPU-load workers, descending settle-delay trials, operator attestation, and cleanup; `docs/release-checklist.md` defines the evidence contract and records literal results.

**Tech Stack:** Bash 3.2, tmux 3.7b-compatible CLI, real Claude Code and codex CLIs, standard macOS/POSIX utilities, Markdown, Go verification commands.

## Global Constraints

- The approved design is `docs/superpowers/specs/2026-08-01-release-verification-design.md`; the project spec wins over `docs/brief.md`.
- Every tmux invocation in `hack/` must use a unique `-L` socket; no script may touch the default tmux server.
- Every script must trap normal exit and `HUP`, `INT`, and `TERM` so its throwaway server and load workers are stopped.
- `hack/verify-injection.sh` runs real harnesses without permission-bypass flags and never authorizes repository work.
- Captured TUI output is manual spike evidence only; product code continues to expose no TUI scraping.
- Measurement uses 10 consecutive trials per candidate by default under positive CPU load, stops descending after the first failed candidate, and never changes `internal/tmuxx.payloadDelay`.
- CPU saturation via `/usr/bin/yes` approximates scheduler contention but does not bound I/O, memory, thermal, network, terminal, or future-harness contention.
- Only loaded results may support a future delay-tuning proposal, and any such proposal must independently choose a safety margin.
- The harness-driving checks remain manual release tooling and are not added to CI.
- Final verification requires `bash -n hack/*.sh`, all four real tmux probes, both real harness modes, `go test ./...`, and `go vet ./...`.

---

### Task 1: Commit the four independent tmux probes

**Files:**
- Create: `hack/probe-1-argv.sh`
- Create: `hack/probe-2-targeting.sh`
- Create: `hack/probe-3-ids.sh`
- Create: `hack/probe-4-attach.sh`

**Interfaces:**
- Consumes: the four fenced Bash scripts in the first comment on GitHub issue #13.
- Produces: standalone executable probes whose stdout is the evidence log and whose exit cleanup kills only their unique tmux server.

- [ ] **Step 1: Re-read the authoritative scripts and materialize four files**

Run:

```bash
gh issue view 13 --repo mnbf9rca/agentctl --json comments --jq '.comments[0].body'
```

Use the four fenced scripts in order. Name them exactly as listed above. Preserve every experiment and expected-output label. For each file, replace the posted socket assignment with its own prefix plus PID:

```bash
SOCKET="agentctl-probe-1-$$" # use 2, 3, or 4 in the matching file
tmux_cmd() { tmux -L "$SOCKET" "$@"; }
cleanup() { tmux_cmd kill-server >/dev/null 2>&1 || true; }
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
```

Replace each `$T ...` call with `tmux_cmd ...`. In probe 1, make the capture path collision-free:

```bash
KEY_CAPTURE="/tmp/agentctl-probe-keys-$$.txt"
```

Have `cleanup` remove it with `rm -f "$KEY_CAPTURE"`. Keep the posted cleanup output at the end, but rely on the trap for interruption safety.

- [ ] **Step 2: Verify script syntax before execution**

Run:

```bash
bash -n hack/probe-1-argv.sh \
  hack/probe-2-targeting.sh \
  hack/probe-3-ids.sh \
  hack/probe-4-attach.sh
```

Expected: exit 0 and no output.

- [ ] **Step 3: Run each probe and retain its stdout for checklist review**

Run:

```bash
for probe in hack/probe-1-argv.sh hack/probe-2-targeting.sh hack/probe-3-ids.sh hack/probe-4-attach.sh; do
  bash "$probe" >"/tmp/$(basename "$probe").out" 2>&1
done
```

Expected: each exits 0. Probe 4's attach attempts fail only for terminal/control-mode reasons when the target exists, while the missing target reports that it cannot find the session.

- [ ] **Step 4: Verify all probe servers were cleaned up**

Run:

```bash
if pgrep -fl '[t]mux.*agentctl-probe-' >/tmp/agentctl-probe-processes.out; then
  cat /tmp/agentctl-probe-processes.out
  exit 1
fi
```

Expected: exit 0 and no output.

- [ ] **Step 5: Make scripts executable and commit**

Run:

```bash
chmod +x hack/probe-*.sh
git add hack/probe-1-argv.sh hack/probe-2-targeting.sh hack/probe-3-ids.sh hack/probe-4-attach.sh
git commit -m "Add release tmux probes"
```

---

### Task 2: Build the safe real-harness verifier and fixed mode

**Files:**
- Create: `hack/verify-injection.sh`

**Interfaces:**
- Consumes: `tmux`, selected real harness binaries, `getconf`, `/usr/bin/yes`, `uptime`, `mktemp`, `date`, and operator stdin.
- Produces: `verify` and `measure` subcommands, a preserved artifact directory, `metadata.txt`, `results.tsv`, and named `*.txt` pane snapshots.

- [ ] **Step 1: Write a CLI smoke test before implementation**

The production change that makes this check pass is a parser that recognizes help and rejects unsupported modes before invoking tmux.

Run before creating the script:

```bash
bash hack/verify-injection.sh --help
```

Expected: FAIL because the file does not exist.

- [ ] **Step 2: Create the Bash 3.2-compatible skeleton**

Start the script with:

```bash
#!/bin/bash
set -u

usage() {
  cat <<'EOF'
Usage:
  hack/verify-injection.sh verify [--harness both|claude|codex] [--output DIR]
  hack/verify-injection.sh measure [--harness both|claude|codex] [--output DIR]
                                   [--trials N] [--load-workers N]
EOF
}

die() { printf 'verify-injection: %s\n' "$*" >&2; exit 2; }
```

Parse one required mode plus the four named options. Defaults:

```bash
HARNESS=both
TRIALS=10
LOAD_WORKERS=$(getconf _NPROCESSORS_ONLN 2>/dev/null || printf '1')
SOCKET="agentctl-injection-$$"
SESSION=agentctl-injection
```

Require positive decimal `TRIALS` and `LOAD_WORKERS`; accept only `both`, `claude`, or `codex`; refuse an existing explicit output directory. Create the default with `mktemp -d /tmp/agentctl-injection.XXXXXX`.

- [ ] **Step 3: Verify parser behavior**

Run:

```bash
bash hack/verify-injection.sh --help
bash hack/verify-injection.sh nope >/tmp/agentctl-invalid.out 2>&1; test "$?" -eq 2
bash hack/verify-injection.sh measure --trials 0 >/tmp/agentctl-invalid.out 2>&1; test "$?" -eq 2
```

Expected: help exits 0; invalid mode and zero trials exit 2 without creating a tmux server.

- [ ] **Step 4: Implement shared lifecycle functions**

Add these responsibilities as focused functions:

```bash
tmux_cmd() { tmux -L "$SOCKET" "$@"; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"; }
cleanup() {
  for pid in $LOAD_PIDS; do kill "$pid" >/dev/null 2>&1 || true; done
  for pid in $LOAD_PIDS; do wait "$pid" >/dev/null 2>&1 || true; done
  tmux_cmd kill-server >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
```

Initialize `LOAD_PIDS=''` before installing the traps. Validate common utilities plus only the selected harness binaries before starting tmux. Write exact tool versions, current date, mode, trial count, load-worker count, current directory, and `uptime` to `metadata.txt`. Initialize `results.tsv` with literal columns `mode`, `harness`, `delay_ms`, `trials`, `load_workers`, `result`, and `detail`.

Implement `start_harnesses` using `new-session` for the first harness and `new-window` for the second. Both commands use `-d`, `-c "$PWD"`, `-P -F '#{pane_id}'`, `--`, and the bare harness executable. Resize the detached session to 160 columns by 50 rows. Store `CLAUDE_PANE` and `CODEX_PANE` as returned tmux pane IDs.

Implement:

```bash
capture_snapshot() {
  harness=$1 phase=$2 pane=$3
  tmux_cmd capture-pane -p -S - -t "$pane" >"$OUTPUT/${harness}-${phase}.txt"
}
```

Every tmux call must route through `tmux_cmd`. Until Task 3 implements measurement, selecting `measure` must fail with `die "measure mode is not implemented"` after parsing and before creating tmux or load processes.

- [ ] **Step 5: Implement fixed verification behavior**

For each selected harness:

1. Prompt the operator to press Enter only after the TUI is ready.
2. Send literal `agentctl-verification-junk`; wait 500ms; capture `junk`.
3. Send `C-u`; wait 500ms; capture `cleared`.
4. Observation leg: send literal `/clear` with `send-keys -l --`; wait exactly 1000ms; capture `popup`; send `C-u` to cancel the unsubmitted text.
5. Execution leg: send literal `/clear` again; wait exactly 1000ms; invoke `send-keys Enter` immediately, with no capture or other command between the wait and `Enter`; wait 2000ms; capture `reset`.
6. Record `#{pane_current_command}` in `results.tsv`.
7. Print the popup and reset snapshot paths and require an explicit `y` attestation that all fixed-mode criteria passed. Record `verify PASS` or `verify FAIL`; any fail makes the script exit 1 after cleanup.

Use a Bash 3.2-compatible millisecond sleep helper:

```bash
sleep_ms() {
  ms=$1
  [ "$ms" -eq 0 ] && return 0
  sleep "$(printf '%d.%03d' "$((ms / 1000))" "$((ms % 1000))")"
}
```

- [ ] **Step 6: Run fixed mode against one harness as the green check**

Run in a PTY:

```bash
bash hack/verify-injection.sh verify --harness claude
```

Expected: the operator can inspect four snapshots, attest the exact criteria, the script exits 0 on pass, prints the artifact path, and `tmux -L <printed-socket> has-session` fails afterward.

- [ ] **Step 7: Commit fixed verification mode**

Run:

```bash
chmod +x hack/verify-injection.sh
git add hack/verify-injection.sh
git commit -m "Add harness injection verifier"
```

---

### Task 3: Add loaded popup-settle measurement

**Files:**
- Modify: `hack/verify-injection.sh`

**Interfaces:**
- Consumes: the lifecycle, capture, send, and sleep functions from Task 2.
- Produces: loaded candidate batches at `1000 750 500 250 100 50 0`, operator attestations, and a literal observed-floor record in `results.tsv`.

- [ ] **Step 1: Demonstrate the missing measure behavior**

Run:

```bash
bash hack/verify-injection.sh measure --harness claude --trials 1 --load-workers 1
```

Expected: exit 2 with `verify-injection: measure mode is not implemented`; it must not create tmux or load processes.

- [ ] **Step 2: Implement CPU-load worker lifecycle**

Add `start_load` that prints a warning, asks the operator to confirm, starts exactly `LOAD_WORKERS` instances of `/usr/bin/yes >/dev/null` in the background, appends every PID to `LOAD_PIDS`, records pre-load and post-stabilization `uptime`, and waits ten seconds before the first trial. Cleanup already kills every recorded PID.

- [ ] **Step 3: Implement one candidate batch**

For one harness, pane, delay, and trial count:

```text
send C-u
send literal /clear                          # observation leg
sleep candidate delay
capture <harness>-measure-<delay>ms-trial-<N>-popup.txt
send C-u                                     # cancel observation text
send literal /clear                          # execution leg
sleep candidate delay
send Enter immediately                       # no capture/command in this gap
sleep 2000ms
capture <harness>-measure-<delay>ms-trial-<N>-reset.txt
```

After the batch, print its snapshot glob and require an explicit operator `y` only if every trial highlighted exact `/clear` and reset. Append harness, delay, trials, worker count, and `PASS`/`FAIL` to `results.tsv`.

- [ ] **Step 4: Implement descending stop and floor recording**

For each selected harness, iterate `1000 750 500 250 100 50 0`. Track the last passing delay. Stop after the first failed candidate. Outcomes:

- first candidate fails: record `FLOOR NONE` and make the script exit 1;
- a lower candidate fails: record the preceding pass as `FLOOR <N>ms`;
- every candidate passes: record `FLOOR 0ms-at-script-resolution`.

Never edit or invoke product code. Print that the result is evidence only and that future tuning needs a separately justified safety margin.

- [ ] **Step 5: Verify measurement cleanup with a short smoke run**

Run in a PTY:

```bash
bash hack/verify-injection.sh measure --harness claude --trials 1 --load-workers 1
```

Expected: load warning and confirmation, one or more candidate batches, an observed-floor line, preserved snapshots, exit 0 when 1000ms passes, no `yes` child from the script, and no throwaway tmux server afterward.

- [ ] **Step 6: Commit measure mode**

Run:

```bash
git add hack/verify-injection.sh
git commit -m "Measure popup settling under load"
```

---

### Task 4: Run the real baseline and write the release checklist

**Files:**
- Create: `docs/release-checklist.md`

**Interfaces:**
- Consumes: all five scripts and their literal artifacts/results.
- Produces: the release operator procedure and the 2026-08-01 baseline including the actually observed loaded floors.

- [ ] **Step 1: Run fixed verification for both harnesses**

Run in a PTY:

```bash
bash hack/verify-injection.sh verify --harness both
```

Inspect every junk, cleared, popup, and reset snapshot before attesting. Record the script's artifact path and literal `results.tsv` values.

- [ ] **Step 2: Run the licensing measurement under full CPU saturation**

Run in a PTY with the default logical-CPU worker count and 10 trials:

```bash
bash hack/verify-injection.sh measure --harness both
```

Review every candidate batch honestly. Stop at the script's first failed candidate. Copy the literal worker count, load metadata, per-candidate results, and observed floors; do not infer an untested lower value.

- [ ] **Step 3: Write the checklist sections**

Create `docs/release-checklist.md` with these concrete sections:

```markdown
# Release Verification Checklist

## Preconditions
## Tmux contract probes
## Real-harness injection verification
## Loaded popup-settle measurement
## Cleanup confirmation
## Results history
```

For each probe, state its filename, run command, and the exact fact described in issue #13's reviewer comment. For injection, require visible junk, cleared input, exact `/clear` popup selection, conversation reset, and observed process name. For measurement, document candidates, default 10/10 pass threshold, full logical-CPU `yes` load, first-failure stop, artifact review, evidence-only interpretation, and the fact that CPU saturation does not bound I/O/memory/thermal/terminal/future-harness contention.

- [ ] **Step 4: Record literal 2026-08-01 results**

Record:

- tmux `3.7b`;
- Claude Code `2.1.220`, pane command `2.1.220`, fixed sequence pass;
- codex-cli `0.146.0`, pane command `codex`, fixed sequence pass;
- the actual loaded worker count and observed floors copied from the measure artifact.

State `payloadDelay remains 1s; no tuning is made by issue #13` beside the floor result. Do not leave an empty result cell or placeholder if the measurement fails; record `no floor established` plus the failed candidate and make the PR not ready for merge.

- [ ] **Step 5: Verify checklist claims against artifacts and commit**

Run:

```bash
rg -n "[T]BD|[T]ODO|fill [i]n|[i]mplement later" docs/release-checklist.md
git diff --check
```

Expected: the placeholder scan is empty and diff check exits 0.

Then:

```bash
git add docs/release-checklist.md
git commit -m "Document release verification baseline"
```

---

### Task 5: Final verification and publication

**Files:**
- Verify: `hack/*.sh`
- Verify: `docs/release-checklist.md`
- Verify: `docs/superpowers/specs/2026-08-01-release-verification-design.md`

**Interfaces:**
- Consumes: the complete issue #13 branch.
- Produces: a clean draft PR with issue closure, verification evidence, normal AMQ review gate, and a detached worktree after publication.

- [ ] **Step 1: Run script syntax and help checks**

Run:

```bash
bash -n hack/*.sh
bash hack/verify-injection.sh --help
```

Expected: exit 0.

- [ ] **Step 2: Re-run all four safe tmux probes**

Run:

```bash
for probe in hack/probe-*.sh; do bash "$probe"; done
```

Expected: every probe reaches its cleanup marker and exits 0; no `agentctl-probe-*` tmux server remains.

- [ ] **Step 3: Run product verification**

Run:

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./...
git diff --check origin/main...HEAD
```

Expected: all exit 0.

- [ ] **Step 4: Self-review requirements**

Confirm line by line:

- four independent executable probes;
- no bare tmux invocation in any operational script path;
- trap cleanup covers servers, temp capture file, and load PIDs;
- fixed verification covers both harnesses and exact pass criteria;
- measure covers both harnesses under positive load and stops after first failure;
- checklist records literal versions, process names, loaded results, and limitations;
- `internal/tmuxx.payloadDelay` is unchanged.

- [ ] **Step 5: Publish the draft PR**

Commit any final documentation-only correction, push `wave4/spike`, and open a draft PR titled `Add release verification tooling` with:

- summary of five scripts and checklist;
- the loaded measurement result and explicit non-licensing limitation;
- full verification evidence;
- `Closes #13`.

- [ ] **Step 6: Request the normal AMQ gate and detach**

Send the PR URL, base/head SHAs, real-harness evidence, loaded-floor results, and verification commands to `reviewer` on `work/issue-13`; notify `planner`. Once both messages are drained, switch the clean worktree to detached `origin/main`. Address any blocking findings by reattaching `wave4/spike`, applying test-first fixes, and re-gating the final head.
