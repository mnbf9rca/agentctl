# Release Verification Runbook

## When you must run this

Run this checklist for every release that changes tmux targeting, harness
startup, or injected command delivery — the same three areas Parts A and B
below probe. The harness checks in this runbook intentionally capture TUI
output as release evidence; product code itself must never scrape either TUI.

This checklist gates the main → release promotion PR: its "Checklist run" /
"Checklist not required" attestation checkbox ships with the release, and that
checkbox only exists if the PR is opened from the template. GitHub shows no
template chooser for pull requests, so open the promotion PR explicitly with:

```bash
gh pr create --base release --head main --title "Release vX.Y.Z" --body-file .github/PULL_REQUEST_TEMPLATE/release-promotion.md
```

Without the explicit `--body-file`, the PR body is empty and the attestation
checkbox is silently lost. See "Recording results" below for the full step.

## Why this checklist exists

Automated tests cannot cover the states this checklist covers, and the reason is
structural rather than a matter of effort.

**A fixture that establishes a precondition cannot test the code path that
establishes that precondition.** Wherever setup does something the product must
also do, the product's version of it is untested by construction — the fixture
has already made the state the product is supposed to create, before the first
assertion runs.

The integration suite bootstraps a tmux server so every test starts from a known
state. That is correct practice. It also means the suite can only reach states
that are reachable *from a running server* — and the one state `launch` must
handle before it has done anything is the absence of one. A first-ever launch on
a machine with no tmux server was therefore broken in a way no test in the suite
could observe, and a human found it in ten minutes (§6.7).

The same shape recurs elsewhere here: harness behaviour under CPU saturation,
what a TUI's autocomplete highlights at the instant `Enter` lands, and whether
an operator can actually see eight iTerm2 tabs. In each case something the tests
must assume is exactly the thing in question.

So this document is not a belt-and-braces duplicate of the test suite. It is the
part of verification the suite is structurally unable to perform.

## Setup (10 minutes)

1. Open three iTerm windows side by side. Label them 1, 2, 3. Window 1 runs
   commands and drives each script below. Window 2 stays attached to the
   harness pane for live inspection during Parts B and C. Window 3 is for
   reading recorded snapshot files while you attest.
2. Window 1: `cd` to the release-candidate checkout. Confirm there is no
   unrelated work in progress in it.
3. Window 1: run `make build`. Record the exact output of
   `./bin/agentctl version` here: ______. Release identity must come from
   `make build` — plain `go build` inside a linked worktree records the main
   checkout's revision as clean, not the worktree's revision, so do not
   substitute plain `go build` for this step.
4. Window 1: confirm `tmux`, Claude Code, and `codex` are installed. Record
   their exact versions here: `tmux -V` → ______, `claude --version` →
   ______, `codex --version` → ______.
5. Expect every script in Parts A–C to create and use its own unique
   `tmux -L` socket automatically. Never point these tools at your default,
   interactive tmux server, and never run them against a session you are
   currently using for other work.
6. Confirm you, the operator, are set up to inspect pane snapshots and answer
   each attestation honestly in Parts B and C. None of the scripts use
   permission-bypass flags or authorize either harness to perform repository
   work — you are the only thing standing between a silent regression and a
   passing checklist.

## Part A: Contract probes (Window 1)

1. Run all four probes and retain their stdout:

   ```bash
   for probe in hack/probe-*.sh; do
     bash "$probe"
   done
   ```

2. `hack/probe-1-argv.sh`. Expected: it verifies the accepted `new-session`
   and `new-window` argv shapes, `-c` behavior, user-option round trips
   (including empty and space-containing values), format interpolation,
   exact-versus-prefix target behavior, and literal
   `send-keys -l -- '/clear'` followed by a separate `Enter`.
3. `hack/probe-2-targeting.sh`. Expected: it demonstrates the
   security-sensitive target behavior — session option operations cannot
   express exactness with an `=name` target, bare session/window targets may
   prefix-match, duplicate window names are ambiguous, quiet option reads
   collapse unset to empty, and a pane ID supplies the context needed by
   `display-message`.
4. `hack/probe-3-ids.sh`. Expected: it verifies that session, window, and pane
   IDs work for all required operations, remain exact in the presence of a
   prefix decoy, are returned atomically by `-P -F`, safely delimit arbitrary
   values, and expose the specified liveness fields.
5. `hack/probe-4-attach.sh`. Expected: it verifies that `attach-session`
   resolves a session ID — an existing ID reaches the expected
   non-terminal/control-mode error, while a missing target reports that the
   session cannot be found.
6. Window 1: confirm cleanup left no throwaway probe server running:

   ```bash
   pgrep -fl '[t]mux.*agentctl-probe-'
   ```

   Expected: no output. Part A passes only when all four probes reached
   their cleanup marker, every observation above matched, and this command
   prints nothing.

## Part B: Injection verification (Windows 1–3)

Run:

```bash
bash hack/verify-injection.sh verify --harness both
```

1. Window 1: start the command above. It creates an isolated tmux session
   named `agentctl-injection` on its own `tmux -L agentctl-injection-<pid>`
   socket (`<pid>` is the script's own process ID) and waits for you at
   "Press Enter only after its TUI is fully ready."
2. Window 2: attach to that same isolated session so you can watch the
   harness panes live:

   ```bash
   pid=$(pgrep -f 'hack/verify-injection.sh' | head -1)
   tmux -L "agentctl-injection-${pid}" attach -t agentctl-injection
   ```

   Stay attached in Window 2 for the rest of Part B. (`agentctl attach` does
   not reach this session: it only targets the default tmux server and only
   sessions carrying agentctl's own management marker, neither of which this
   ephemeral verification session has.)
3. For each harness in turn: watch its pane in Window 2 until the TUI is
   fully ready, then return to Window 1 and press Enter at the prompt.
4. Window 1 then runs the injection sequence and prints "Review snapshots:"
   followed by four file paths under its artifact directory. Window 3: open
   all four before answering:
   - `<harness>-junk.txt` — confirm `agentctl-verification-junk` is visibly
     present in the input.
   - `<harness>-cleared.txt` — confirm `C-u` cleared that input.
   - `<harness>-popup.txt` — confirm `/clear` is the exact selected popup
     match, captured after the fixed 1000 ms settle delay.
   - `<harness>-reset.txt` — confirm a separate execution leg (1000 ms wait,
     then `Enter` with no capture in between) shows a completed conversation
     reset.
5. Window 1: type `y` at "Did junk appear, C-u clear it, exact /clear match,
   and the conversation reset?" only if all four snapshots in step 4
   attested clean. Any other answer fails that harness and makes the overall
   command exit nonzero.
6. If running `--harness both`, repeat steps 3–5 for the second harness.
7. Preserve the printed artifact directory in full, including
   `metadata.txt`, `results.tsv` (it records `PASS`/`FAIL` and the observed
   pane process name per harness), and every pane snapshot.

## Part C: Loaded measurement (optional per release; required when timing changes)

Run:

```bash
bash hack/verify-injection.sh measure --harness both
```

1. Save other work first. Measurement mode saturates every logical CPU with
   `/usr/bin/yes`; the machine will be temporarily sluggish, and you should
   not leave the run unattended. The defaults are 10 consecutive paired
   trials at each candidate delay, under one `/usr/bin/yes` worker per
   logical CPU, with candidates descending `1000 750 500 250 100 50 0` ms.
2. Window 1: start the command above.
3. Window 2: immediately attach to the isolated session it just created, the
   same way as Part B:

   ```bash
   pid=$(pgrep -f 'hack/verify-injection.sh' | head -1)
   tmux -L "agentctl-injection-${pid}" attach -t agentctl-injection
   ```

   Stay attached in Window 2 for the rest of Part C.
4. For each harness in turn: watch its pane in Window 2 until the TUI is
   fully ready, then return to Window 1 and press Enter at its "Press Enter
   only after its TUI is fully ready" prompt. With `--harness both`, both
   harnesses' readiness prompts are answered before the load starts.
5. Window 1: at "Start the load?", type `y` only after confirming you have
   saved other work per step 1.
6. Each trial runs an observation leg (captures the exact popup match) and a
   separate execution leg (no capture or command between the candidate wait
   and `Enter`). Window 3: after each candidate's 10 trials complete, review
   every `<harness>-measure-<delay>ms-trial-*-popup.txt` and
   `*-trial-*-reset.txt` file the script names.
7. Window 1: answer "Did every trial show exact /clear selection and a
   completed reset?" truthfully. A candidate passes only when all 10 popup
   and reset snapshot pairs pass your review in step 6.
8. The script stops automatically at the first failed candidate, the same as
   the checklist requires: the preceding passing candidate is the observed
   floor. If the 1000 ms candidate fails, record "no floor established"; do
   not extrapolate upward or call the release ready on that basis.
9. Recording: retain the worker count, the pre-load and post-stabilization
   `uptime` (both written to `metadata.txt`), every row of `results.tsv`, and
   all failing snapshots.
10. Treat this as evidence, not a tuning decision. CPU saturation approximates
    scheduler contention but does not bound I/O, memory, thermal, network,
    terminal, or future harness contention. One worker per logical CPU is an
    adversarial ceiling, not a description of typical fleet load; it bounds
    behavior under deliberate CPU abuse. Idle behavior does not license a
    delay change, and even a loaded passing floor would require a separately
    justified safety margin.
11. If a failed batch needs typed-text diagnosis, rerun only that harness
    with `--capture-pre-enter`, e.g.
    `bash hack/verify-injection.sh measure --harness codex --capture-pre-enter`.
    This asks the tmux server to capture the pane and then send `Enter` in
    one command list, producing a `pre-enter.txt` file for every trial.
    Window 3: review those files for the literal input and highlighted
    command. The capture adds observer overhead, so diagnostic results
    classify a failure; they do not replace the unobserved execution-leg
    timing measurement.

## Cleanup (Window 1)

After every run of Parts A–C:

```bash
pgrep -fl '[t]mux.*agentctl-(probe|injection)-'
pgrep -fl '[y]es'
```

Expected: the first command finds no probe/verifier server — no output. For
the second, compare any output against the `load_pids` recorded in Part C's
`metadata.txt`: none of those PIDs may remain. Do not treat unrelated `yes`
processes on the machine as verifier children.

## Recording results

1. Append a new dated entry to "Results history" below, following the format
   of the existing entries: tool versions, a pass/fail summary for each part
   you ran, the artifact directory path(s), and — for Part C, when there is a
   finding worth one — a results table like the 2026-08-01 entry's.
2. On the main → release promotion PR, tick exactly one attestation checkbox:
   "Checklist run" (Parts A/B were required, ran, and passed, with results
   recorded in this file on `main`) or "Checklist not required" (no changes
   touched checklist-covered areas since the last release).
3. Open the promotion PR with the template explicitly attached — see "When
   you must run this" above for why this is required, not optional:

   ```bash
   gh pr create --base release --head main --title "Release vX.Y.Z" --body-file .github/PULL_REQUEST_TEMPLATE/release-promotion.md
   ```

   Fill in the `Version:` field (the output of `hack/next-version.sh`) in the
   rendered PR body before requesting merge.
4. A green `Release` GitHub Actions run does not by itself prove notarization
   completed. The workflow's `notary-check` job intentionally exits 0 even
   when notarization is still pending, so it will not turn the run red on
   its own. Open the `notary-check` job's log and confirm it printed
   `notarization accepted`. If it instead printed
   `notarization still pending; re-check manually`, re-run
   `xcrun notarytool history` (or re-open the job later) before treating the
   release as fully notarized.

## Results history

### 2026-08-01

- tmux: `3.7b`
- Claude Code: `2.1.220`; pane process `2.1.220`
- codex-cli: `0.146.0`; pane process `codex`
- Contract probes: all four completed with the expected observations and no
  surviving throwaway server.
- Fixed injection verification: both harnesses passed the input-cleared, exact
  popup-match, and conversation-reset criteria. Artifact:
  `/tmp/agentctl-injection.NUfQP9`.
- Loaded measurement: 18 workers, 10 trials per candidate. Artifact:
  `/tmp/agentctl-injection.I3ENZE`.

```text
measure  claude  1000  10  18  PASS
measure  claude   750  10  18  PASS
measure  claude   500  10  18  FAIL
floor    claude   750ms
measure  codex   1000  10  18  FAIL
floor    codex   no floor established
```

Claude's 500 ms batch failed on trial 10 because the reset snapshot still
showed `/clear` queued while the TUI was busy. Codex's first 1000 ms batch had
multiple missing or doubled `/clear` inputs and incomplete resets. Specifically,
Codex trial 2 had no injected payload in the popup snapshot and still showed a
literal `/clear` in the reset snapshot; trial 3 showed `/clearclear`; and trial 6
again had no injected payload in the popup snapshot. Whenever an intact
`/clear` was visible, its popup match was exact. This classifies the failure as
payload delivery/readiness breakdown under starvation, not popup
mis-selection or reset-detection error. Therefore the 2026-08-01 release
baseline does not license reducing the current delay: Codex established no
loaded floor at 1000 ms. This finding does not block merging the manual
verification tooling that surfaced and preserves the evidence.

A diagnostic rerun of Codex at 1000 ms used the same 10 trials and 18 workers
with guaranteed pre-Enter pane captures. Artifact:
`/tmp/agentctl-injection.0JfDWc`.

All three `/tmp` artifact paths are run-local and not preserved; the tables and
summaries in this document are the authoritative durable record.

| Trial | Pre-Enter typed text | Highlight at Enter | Reset evidence |
| --- | --- | --- | --- |
| 1 | `/clear` | exact `/clear` | completed (sole full pass) |
| 2 | `/clear` | exact `/clear` | blank snapshot; no completed reset visible |
| 3 | none visible; pane blank | none visible | blank snapshot |
| 4 | none visible; pane blank | none visible | blank snapshot |
| 5 | none visible; pane blank | none visible | blank snapshot |
| 6 | `/clear` | exact `/clear` | blank snapshot |
| 7 | empty; placeholder only | none | unchanged placeholder |
| 8 | empty; placeholder only | none | unchanged placeholder |
| 9 | empty; placeholder only | none | placeholder visible |
| 10 | empty; placeholder only | none | literal `/clear` appeared unexecuted |

No nonempty truncation such as `/c`, `/cl`, or `/co` appeared, so this rerun
produced neither an ambiguous registry prefix nor a prefix matching another
Codex palette command. No alternate command was highlighted. The earlier
unobserved-execution run did capture `/clearclear` in an observation leg, with
no popup match. These observations do not disprove the coupled risk that a
future truncated prefix could rank a different harness command; they establish
only that this diagnostic run did not instantiate silent wrong-command
selection.

`internal/tmuxx.payloadDelay` remains 1 second. Issue #13 makes no tuning
change.
