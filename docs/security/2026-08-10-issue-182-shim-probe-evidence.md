# Issue #182 shim SIGHUP probe evidence

Date: 2026-08-10 (Asia/Kuala_Lumpur)

## Outcome

Both pinned harnesses terminated after SIGHUP was sent to the parent PTY fixture:

| Harness | Version | Shim PID | Child PID / TTY | Observed child outcome |
|---|---|---:|---|---|
| Claude Code | `2.1.226 (Claude Code)` | 55862 | 55866 / `ttys006` | `terminated` |
| codex-cli | `codex-cli 0.147.0` | 56309 | 56313 / `ttys006` | `terminated` |

This is version-pinned evidence for teardown selection, not an absence oracle. The approved design still records child
identity durably, uses `kill(pid, 0)` as the sole presence/absence observation, and refuses relaunch unless it observes
`ESRCH`.

## Safety boundary

- Script: `hack/probe-shim-sighup.sh` from the issue-182 PR 1 worktree.
- Each leg created its own mode-`0700` temporary HOME and `/usr/bin/script` nested PTY.
- The probe accepted only the literal harness names `claude` and `codex`.
- Process topology was read from one `ps -axo pid=,ppid=,tty=,comm=` snapshot at a time until the direct child had a
  PTY and its `comm` exactly matched the executable path selected for that harness. The recorded child PPID equaled
  the recorded shim PID. A PTY-bearing intermediate or unrelated direct child is refused, not labeled a harness.
- The only probe signal was `SIGHUP` to the recorded, invocation-owned shim PID. Cleanup was restricted to that shim,
  its recorded direct child, and its exact temporary directory.
- No tmux command appears in the script. The default tmux server and every existing agent/fleet were untouched.
- Output creation used no-clobber behavior and refused a pre-existing evidence path.

## Commands and raw records

The two legs were run with:

```sh
probe_evidence_dir=$(mktemp -d /private/tmp/agentctl-issue182-sighup-evidence.XXXXXX)
hack/probe-shim-sighup.sh --harness claude --output "$probe_evidence_dir/claude.txt"
hack/probe-shim-sighup.sh --harness codex --output "$probe_evidence_dir/codex.txt"
```

Claude record (SHA-256 `65b77ef95b64c4d75823b23e3088c85c884d106970a0f4e1dcb4c25a9c907c12`):

```text
harness=claude
harness_version=2.1.226 (Claude Code)
topology=shim-parent-of-harness-child-on-pty
shim_pid=55862
child_pid=55866
child_ppid_matches=true
child_tty=ttys006
child_command=/Users/rob/.local/bin/claude
signal_target=owned-shim-only
signal=SIGHUP
shim_terminated=true
child_outcome=terminated
default_tmux_targeted=false
```

Codex record (SHA-256 `d539bbb1a9ccb736e201af3e47c586901626776d00d3bc8bbd26a9839f0a53fb`):

```text
harness=codex
harness_version=codex-cli 0.147.0
topology=shim-parent-of-harness-child-on-pty
shim_pid=56309
child_pid=56313
child_ppid_matches=true
child_tty=ttys006
child_command=/Users/rob/.local/bin/codex
signal_target=owned-shim-only
signal=SIGHUP
shim_terminated=true
child_outcome=terminated
default_tmux_targeted=false
```

Codex printed a PATH-alias warning before its version line in this execution environment. The probe selects exactly
one harness-specific version line (`^codex-cli [0-9]` or a line ending in `(Claude Code)`) and refuses zero or multiple
matches; the warning is not mistaken for the pinned version.

## Automated fixture coverage

`go test ./hack -run TestProbeShimSIGHUP -count=1` covers closed harness-name refusal, existing-output refusal,
multi-line version parsing, transient pre-PTY topology observation, realistic nonempty command fields, required output
keys, owned child cleanup, and an unrelated sentinel answering a bounded `SIGUSR1` liveness handshake after probe
cleanup. A dedicated boundary fixture refuses an exited/zombie sentinel even where `kill(pid, 0)` still reports the
PID present. A dedicated intermediate fixture puts a PTY-bearing bridge between the shim and selected harness and
requires factual refusal plus cleanup of both owned processes. If that fixture omits its grandchild PID record, fake
`ps` delays 100ms and then returns no row. This proves that the nominal five-second elapsed deadline, checked between
returning samples with 50ms pauses, is not extended by a fixed observation count; it does not claim to preempt a hung
`ps` invocation. The bounded refusal still cleans the owned bridge and grandchild. The fake PATH includes a `tmux`
canary and fails if the probe invokes it.
