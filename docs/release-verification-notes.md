# Release verification: rationale and results history

See `docs/release-checklist.md` for the runbook this supports.

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
