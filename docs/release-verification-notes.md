# Release verification: rationale and results history

See `docs/release-checklist.md` for the runbook this supports.

## Why this checklist exists

For 0.5.0, the shim cutover replaces the old human Parts A–C walkthrough with
the Task 8 automated release-candidate fixture. Kernel, PTY, Unix-socket, tmux
layout, installed-skill, and cleanup observations are captured as named
artifacts. The historical explanation below describes why the pre-0.5
walkthrough existed; its results remain history, not evidence for 0.5 or later.

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

### 2026-08-15

- agentctl: `agentctl 2bd91cd`
- tmux: `tmux 3.7b`
- Claude Code: `2.1.233 (Claude Code)`
- codex-cli: `codex-cli 0.147.0`
- Mode: `verify-live`; harness: `both`
- Artifact: `/tmp/agentctl-release-verify.Jmk27g/verify-live`
- Part A: PASS — automated probes and isolation checks completed
- Part B: PASS — operator confirmed: explicit role attach, delivery, runtime-record relaunch, reattach, and viewer-close checkpoints B.C1-B.C10
- Part B session: `relverify_jmk27g`
- Part C: PASS — operator confirmed: authentication, skill inventory, and status-meaning checkpoints C.C1-C.C3
- Probes: all four completed, no surviving throwaway server
- Checkpoint B.C1 explicit role attachments: operator confirmed: y
- Checkpoint B.C3 Claude clear outcome: operator confirmed: y
- Checkpoint B.C5 Codex clear outcome: operator confirmed: y
- Checkpoint B.C7 Claude compact outcome: operator confirmed: y
- Checkpoint B.C9 relaunch: PASS (old child absent; replacement runtime identities observed; explicit role reattach confirmed); fresh claude input with no junk: operator confirmed: y
- Checkpoint B.C10 viewer terminals closed: operator confirmed: y
- Checkpoint C.C1 authentication (keychain-linked, codex-seeded): operator confirmed: y
- Checkpoint C.C2 skill inventory: operator confirmed: y
- Checkpoint C.C3 status meaning: operator confirmed: y
- Teardown status: exit 3 (detached durable fleet absent)
- Teardown check: PASS

### 2026-08-15

- agentctl: `agentctl 9441d19`
- tmux: `tmux 3.7b`
- Claude Code: `2.1.233 (Claude Code)`
- codex-cli: `codex-cli 0.147.0`
- Mode: `verify-live`; harness: `both`
- Artifact: `/tmp/agentctl-release-verify.sOZSK1/verify-live`
- Part A: PASS — automated probes and isolation checks completed
- Part B: PASS — operator confirmed: explicit role attach, delivery, runtime-record relaunch, reattach, and viewer-close checkpoints B.C1-B.C10
- Part B session: `relverify_sozsk1`
- Part C: PASS — operator confirmed: authentication, skill inventory, and status-meaning checkpoints C.C1-C.C3
- Probes: all four completed, no surviving throwaway server
- Checkpoint B.C1 explicit role attachments: operator confirmed: y
- Checkpoint B.C3 Claude clear outcome: operator confirmed: y
- Checkpoint B.C5 Codex clear outcome: operator confirmed: y
- Checkpoint B.C7 Claude compact outcome: operator confirmed: y
- Checkpoint B.C9 relaunch: PASS (old child absent; replacement runtime identities observed; explicit role reattach confirmed); fresh claude input with no junk: operator confirmed: y
- Checkpoint B.C10 viewer terminals closed: operator confirmed: y
- Checkpoint C.C1 authentication (keychain-linked, codex-seeded): operator confirmed: y
- Checkpoint C.C2 skill inventory: operator confirmed: y
- Checkpoint C.C3 status meaning: operator confirmed: y
- Teardown status: exit 3 (detached durable fleet absent)
- Teardown check: PASS

### 2026-08-15

- agentctl: `agentctl 25764cb`
- tmux: `tmux 3.7b`
- Claude Code: `2.1.233 (Claude Code)`
- codex-cli: `codex-cli 0.147.0`
- Mode: `verify-live`; harness: `both`
- Artifact: `/tmp/agentctl-release-verify.LjdUVE/verify-live`
- Part A: PASS — automated probes and isolation checks completed
- Part B: PASS — operator confirmed: explicit role attach, delivery, runtime-record relaunch, reattach, and viewer-close checkpoints B.C1-B.C10
- Part B session: `relverify_ljduve`
- Part C: PASS — operator confirmed: authentication, skill inventory, and status-meaning checkpoints C.C1-C.C3
- Probes: all four completed, no surviving throwaway server
- Checkpoint B.C1 explicit role attachments: operator confirmed: y
- Checkpoint B.C3 Claude clear outcome: operator confirmed: y
- Checkpoint B.C5 Codex clear outcome: operator confirmed: y
- Checkpoint B.C7 Claude compact outcome: operator confirmed: y
- Checkpoint B.C9 relaunch: PASS (old child absent; replacement runtime identities observed; explicit role reattach confirmed); fresh claude input with no junk: operator confirmed: y
- Checkpoint B.C10 viewer terminals closed: operator confirmed: y
- Checkpoint C.C1 authentication (keychain-linked, codex-seeded): operator confirmed: y
- Checkpoint C.C2 skill inventory: operator confirmed: y
- Checkpoint C.C3 status meaning: operator confirmed: y
- Teardown status: exit 3 (detached durable fleet absent)
- Teardown check: PASS

### 2026-08-12

- agentctl: `agentctl 21cfecf`
- tmux: `tmux 3.7b`
- Claude Code: `2.1.228 (Claude Code)`
- codex-cli: `codex-cli 0.147.0`
- Mode: `verify-live`; harness: `both`
- Artifact: `/tmp/agentctl-release-verify.Ui9Fw8/verify-live`
- Part A: PASS — automated probes and isolation checks completed
- Part B: PASS — operator confirmed: numbered attach, delivery, relaunch, and detach checkpoints B.C1-B.C10
- Part C: PASS — operator confirmed: authentication, skill inventory, and status-meaning checkpoints C.C1-C.C3
- Probes: all four completed, no surviving throwaway server
- Checkpoint B.C1 attach narration: operator confirmed: y
- Checkpoint B.C3 Claude clear outcome: operator confirmed: y
- Checkpoint B.C5 Codex clear outcome: operator confirmed: y
- Checkpoint B.C7 Claude compact outcome: operator confirmed: y
- Checkpoint B.C9 relaunch: PASS (stored claude/default/default provenance; pane ID changed); fresh claude input with no junk: operator confirmed: y
- Checkpoint B.C10 detach: operator confirmed: y
- Checkpoint C.C1 authentication (keychain-linked, codex-seeded): operator confirmed: y
- Checkpoint C.C2 skill inventory: operator confirmed: y
- Checkpoint C.C3 status meaning: operator confirmed: y
- Teardown status: exit 3 (session absent; other tmux sessions remained)
- Teardown check: PASS

### 2026-08-11

Issue #182 Task 8 implementation evidence (pre-PR release-candidate run):

- Candidate: `agentctl 9b3ecd78ca8b129c90c7c0bda1b4f2d03672f065`; SHA-256
  `1b20f9c5c7204908c33c749e10e488053f58e41b3d2e3273f6ca4ceb927370a0`.
- Foreign fixture: `foreign-protocol-v2`; SHA-256
  `0fcdb1861fa469b43d82e5f1af91c272cff120efa1adde26627eb8afc00b3d0b`.
- All six matrix legs passed: the current client rejected foreign and absent
  `connected shim hello` versions; foreign and absent clients were rejected
  from `client request`; matching hello/request controls reached their next
  typed runtime gates.
- Candidate-backed, fixture-owned integration tests attested the candidate
  path/hash and passed no-tmux foreground operation,
  `join-pane`/`break-pane`/`swap-pane`/`move-window` identity preservation,
  delivery, roster extension/directory refusal, unanchored status, divergent
  state roots, attach refusal, shim-crash/relaunch, and child-exit-before-cleanup
  (`9.040s`).
- Live kernel tests observed a reaped child as ESRCH/absent; the EPERM, other
  kill-error, and post-presence token-read failure table refused absence. Raw
  start-token legs passed under `TZ=UTC, LC_ALL=C` and
  `TZ=Pacific/Auckland, LC_ALL=en_US.UTF-8`.
- Deterministic TERM injection at all nine walkthrough phases used an actual
  `setsid` descendant that ignored TERM/HUP, preserved exit 143, cleaned
  exactly once, and never printed PASS. The owned-process sweep required
  peer-verified shim identity or matching PID/start-token identity before
  cleanup and then observed ESRCH; cleanup-failure injection preserved the
  signal status.
- The candidate installed matching Claude and Codex skill trees into an
  isolated HOME. R23's agent-facing surface and R23a's production
  `RuntimeStates` pairing were verified by their landed drift tests; they were
  not reimplemented.
- Structural and archive-license fixtures passed. Both actual Darwin snapshot
  archives contained the x/sys license and all prior required license material;
  `go version -m` recorded `golang.org/x/sys v0.47.0`.
- Cleanup recorded the owned Task 8 root absent. The run retained evidence
  only under `/private/tmp/agentctl-task8-final.20ncWP`; this path is
  run-local and this summary is the durable record.

### 2026-08-10

Issue #182 pre-cutover evidence (separate from the release-verifier block below):

- `hack/probe-shim-sighup.sh` used isolated mode-`0700` homes and nested `/usr/bin/script` PTYs; it invoked no tmux
  command and signaled only each invocation's recorded shim PID.
- Claude Code `2.1.226 (Claude Code)`: shim PID 55862, direct child PID 55866 on `ttys006`, observed command
  `/Users/rob/.local/bin/claude`; child observed terminated after shim SIGHUP.
- codex-cli `0.147.0`: shim PID 56309, direct child PID 56313 on `ttys006`, observed command
  `/Users/rob/.local/bin/codex`; child observed terminated after shim SIGHUP.
- The command field exactly matched the selected harness path in each live leg. The fixture suite separately refuses a
  PTY-bearing intermediate direct child and observes cleanup without signaling an unrelated sentinel.
- Full records and safety boundary:
  [`docs/security/2026-08-10-issue-182-shim-probe-evidence.md`](security/2026-08-10-issue-182-shim-probe-evidence.md).
  These observations inform teardown but do not replace `kill(pid,0)` ESRCH-only absence.
- The committed incident replay was byte-identical to build2's full report, SHA-256
  `d9c14f10df03ec7e7de36adcdd9225b26946c64b9d7f26ec50777b41182f7a01`.

- agentctl: `agentctl 25fe900`
- tmux: `tmux 3.7b`
- Claude Code: `2.1.226 (Claude Code)`
- codex-cli: `codex-cli 0.147.0`
- Mode: `verify-live`; harness: `both`
- Artifact: `/tmp/agentctl-release-verify.lYjPVN/verify-live`
- Part A: PASS — automated probes and isolation checks completed
- Part B: PASS — operator confirmed: numbered attach, delivery, relaunch, and detach checkpoints B.C1-B.C10
- Part C: PASS — operator confirmed: authentication, skill inventory, and status-meaning checkpoints C.C1-C.C3
- Probes: all four completed, no surviving throwaway server
- Checkpoint B.C1 attach narration: operator confirmed: y
- Checkpoint B.C3 Claude clear outcome: operator confirmed: y
- Checkpoint B.C5 Codex clear outcome: operator confirmed: y
- Checkpoint B.C7 Claude compact outcome: operator confirmed: y
- Checkpoint B.C9 relaunch: PASS (stored codex/default/high provenance; pane ID changed); fresh codex input with no junk: operator confirmed: y
- Checkpoint B.C10 detach: operator confirmed: y
- Checkpoint C.C1 authentication (keychain-linked, codex-seeded): operator confirmed: y
- Checkpoint C.C2 skill inventory: operator confirmed: y
- Checkpoint C.C3 status meaning: operator confirmed: y
- Teardown status: exit 3 (session absent; other tmux sessions remained)
- Teardown check: PASS

### 2026-08-09

- agentctl: `agentctl 72be6fc`
- tmux: `tmux 3.7b`
- Claude Code: `2.1.226 (Claude Code)`
- codex-cli: `codex-cli 0.147.0`
- Mode: `verify-live`; harness: `both`
- Artifact: `/tmp/agentctl-release-verify.yhK28L/verify-live`
- Part A: PASS — automated probes and isolation checks completed
- Part B: PASS — operator confirmed: numbered attach, delivery, relaunch, and detach checkpoints B.C1-B.C10
- Part C: PASS — operator confirmed: authentication, skill inventory, and status-meaning checkpoints C.C1-C.C3
- Probes: all four completed, no surviving throwaway server
- Checkpoint B.C1 attach narration: operator confirmed: y
- Checkpoint B.C3 Claude clear outcome: operator confirmed: y
- Checkpoint B.C5 Codex clear outcome: operator confirmed: y
- Checkpoint B.C7 Claude compact outcome: operator confirmed: y
- Checkpoint B.C9 relaunch: PASS (stored codex/default/high provenance; pane ID changed); fresh codex input with no junk: operator confirmed: y
- Checkpoint B.C10 detach: operator confirmed: y
- Checkpoint C.C1 authentication (keychain-linked, codex-seeded): operator confirmed: y
- Checkpoint C.C2 skill inventory: operator confirmed: y
- Checkpoint C.C3 status meaning: operator confirmed: y
- Teardown status: exit 3 (session absent; other tmux sessions remained)
- Teardown check: PASS

**Operator/planner note on the 2026-08-09 block (corrected — the first
version of this note stated a false cause, see PR #192's gate):** the
`Part B pre-check:` / `Part B keeper:` lines are absent because this run
invoked `hack/release-verify.sh` directly rather than through Part A's
isolated `TMUX_TMPDIR` subshell, so the default socket was the operator's
live one and the connect-ENOENT branch (#147) did not fire. The branch is
NOT incapable of firing on a machine with a live server — relocating the
socket so that it does is the isolated leg's entire purpose, demonstrated
on this machine during PR #162's gate. The branch's execution evidence for
this release is PR #162's gate (executed live, trap exercised) and the
#177/#178 audit, which ran the wrapper as written against an absent inner
server; the #177 standing rule is satisfied by those executions rather
than by this run. This note is authored by the planner, outside the
machine-written block above.

The C.C1 `y` in the same machine-written block predates #188's narrower
evidence question. Its `(keychain-linked, codex-seeded)` label correctly
records the selected authentication mechanisms, but Claude's first temporary-
HOME start still required manual re-login. Issue #188 changes future C.C1
evidence to ask whether startup required re-authentication; the recorded `y`
above is intentionally unchanged. This annotation is authored by the planner,
outside the machine-written block.

### 2026-08-08

- agentctl: `agentctl e81f83a`
- tmux: `tmux 3.7b`
- Claude Code: `2.1.226 (Claude Code)`
- codex-cli: `codex-cli 0.146.1`
- Mode: `verify-live`; harness: `both`
- Artifact: `/tmp/agentctl-release-verify.JvSmWw/verify-live`
- Part A: PASS — automated probes and isolation checks completed
- Part B: PASS — operator confirmed: numbered attach, delivery, relaunch, and detach checkpoints B.C1-B.C10
- Part C: PASS — operator confirmed: authentication, skill inventory, and status-meaning checkpoints C.C1-C.C3
- Probes: all four completed, no surviving throwaway server
- Checkpoint B.C1 attach narration: operator confirmed: y
- Checkpoint B.C3 Claude clear outcome: operator confirmed: y
- Checkpoint B.C5 Codex clear outcome: operator confirmed: y
- Checkpoint B.C7 Claude compact outcome: operator confirmed: y
- Checkpoint B.C9 relaunch: PASS (stored codex/default/high provenance; pane ID changed); fresh codex input with no junk: operator confirmed: y
- Checkpoint B.C10 detach: operator confirmed: y
- Checkpoint C.C1 authentication (codex-seeded): operator confirmed: y
- Checkpoint C.C2 skill inventory: operator confirmed: y
- Checkpoint C.C3 status meaning: operator confirmed: y
- Teardown status: exit 3 (session absent; other tmux sessions remained)
- Manual interventions outside the verifier (recorded by planner): the default
  tmux server was absent at start, so a keeper session was created by hand
  (#147); Claude sign-in inside the temp HOME required a manually created
  symlink to the operator's `~/Library/Keychains` (#148). All checkpoint
  outcomes above were observed after these interventions; both gaps are filed
  against the verifier, not the released binary.
- Teardown check: PASS

### 2026-08-05

- agentctl: `agentctl bb68e3b`
- tmux: `tmux 3.7b`
- Claude Code: `2.1.222 (Claude Code)`
- codex-cli: `codex-cli 0.146.0`
- Mode: `verify-live`; harness: `both`
- Artifact: `/tmp/agentctl-release-verify.KJb4PF/verify-live`
- Probes: all four completed, no surviving throwaway server
- Attach: recorded y
- Claude clear: recorded y
- Codex clear: recorded y
- Compact (claude): recorded y
- Relaunch: PASS (stored codex/default/high provenance; pane ID changed); fresh codex input with no junk: recorded y
- Teardown status: exit 3 (session absent; other tmux sessions remained)
- Teardown check: PASS

### 2026-08-03

- agentctl: `agentctl 4be8604`
- tmux: `tmux 3.7b`
- Claude Code: `2.1.220 (Claude Code)`
- codex-cli: `codex-cli 0.146.0`
- Mode: `verify-live`; harness: `both`
- Artifact: `/tmp/agentctl-release-verify.MWtiqh/verify-live`
- Probes: all four completed, no surviving throwaway server
- Attach: recorded y
- Claude clear: recorded y
- Codex clear: recorded y
- Compact (claude): recorded y
- Teardown check: PASS

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
