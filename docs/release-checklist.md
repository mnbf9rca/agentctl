# Release Verification Checklist

Run this manual checklist for every release that changes tmux targeting, harness
startup, or injected command delivery. The harness checks intentionally capture
TUI output as release evidence; product code must not scrape either TUI.

## Preconditions

- Use the release candidate checkout with no unrelated work in progress.
- Install `tmux`, Claude Code, and `codex`; record their exact versions.
- Expect every script to use a unique `tmux -L` socket. Never run these probes
  against the default tmux server.
- Run the real-harness checks in a terminal where the operator can inspect pane
  snapshots and answer each attestation honestly. The scripts do not use
  permission-bypass flags or authorize repository work.
- Expect measurement mode to saturate every logical CPU with `/usr/bin/yes`.
  Save other work first and do not leave the run unattended.

## Tmux contract probes

Run all four probes and retain their stdout:

```bash
for probe in hack/probe-*.sh; do
  bash "$probe"
done
```

- `hack/probe-1-argv.sh` verifies the accepted `new-session` and `new-window`
  argv shapes, `-c` behavior, user-option round trips (including empty and
  space-containing values), format interpolation, exact-versus-prefix target
  behavior, and literal `send-keys -l -- '/clear'` followed by a separate
  `Enter`.
- `hack/probe-2-targeting.sh` demonstrates the security-sensitive target
  behavior: session option operations cannot express exactness with an
  `=name` target, bare session/window targets may prefix-match, duplicate
  window names are ambiguous, quiet option reads collapse unset to empty, and
  a pane ID supplies the context needed by `display-message`.
- `hack/probe-3-ids.sh` verifies that session, window, and pane IDs work for all
  required operations, remain exact in the presence of a prefix decoy, are
  returned atomically by `-P -F`, safely delimit arbitrary values, and expose
  the specified liveness fields.
- `hack/probe-4-attach.sh` verifies that `attach-session` resolves a session ID:
  an existing ID reaches the expected non-terminal/control-mode error, while a
  missing target reports that the session cannot be found.

Passing means every probe reaches its cleanup marker, its observations match
the facts above, and this command prints nothing:

```bash
pgrep -fl '[t]mux.*agentctl-probe-'
```

## Real-harness injection verification

Run:

```bash
bash hack/verify-injection.sh verify --harness both
```

For each harness, wait for the TUI-ready prompt and review all four named
snapshots before entering `y`. Passing requires all of the following:

1. `agentctl-verification-junk` is visibly present in the input.
2. `C-u` clears that input.
3. After the fixed 1000 ms settle delay, `/clear` is the exact selected popup
   match.
4. A separate execution leg waits 1000 ms and sends `Enter` immediately, then
   the captured TUI shows a completed conversation reset.
5. `results.tsv` records `PASS` and the observed pane process name.

Any rejected attestation makes the command exit nonzero. Preserve the printed
artifact directory, including `metadata.txt`, `results.tsv`, and every pane
snapshot.

## Loaded popup-settle measurement

Run the release measurement with its defaults:

```bash
bash hack/verify-injection.sh measure --harness both
```

The default contract is 10 consecutive paired trials at each candidate delay,
under one `/usr/bin/yes` worker per logical CPU. Candidates descend through
`1000 750 500 250 100 50 0` ms. Each trial uses an observation leg for the
exact popup match and a separate execution leg with no capture or command
between the candidate wait and `Enter`. A candidate passes only when all 10
popup and reset snapshot pairs pass operator review.

Stop at the first failed candidate, as the script does. The preceding passing
candidate is the observed floor. If 1000 ms fails, record `no floor
established`; do not extrapolate upward or call the release ready. Retain the
worker count, pre-load and post-stabilization `uptime`, every result row, and
all failing snapshots.

This is evidence, not a tuning decision. CPU saturation approximates scheduler
contention but does not bound I/O, memory, thermal, network, terminal, or future
harness contention. One worker per logical CPU is an adversarial ceiling, not a
description of typical fleet load; it bounds behavior under deliberate CPU
abuse. Idle behavior does not license a delay change, and even a loaded passing
floor would require a separately justified safety margin.

## Cleanup confirmation

After every run:

```bash
pgrep -fl '[t]mux.*agentctl-(probe|injection)-'
pgrep -fl '[y]es'
```

The first command must find no probe/verifier server. For the second, compare
against the `load_pids` in the measurement metadata: none of those PIDs may
remain. Do not treat unrelated `yes` processes as verifier children.

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
baseline is **not ready for merge**: Codex established no loaded floor at the
current delay.

`internal/tmuxx.payloadDelay` remains 1 second. Issue #13 makes no tuning
change.
