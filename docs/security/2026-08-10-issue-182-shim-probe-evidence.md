# Issue #182 shim SIGHUP probe evidence

Date: 2026-08-10 (Asia/Kuala_Lumpur)

## Outcome

Both pinned harnesses terminated after SIGHUP was sent to the parent PTY fixture:

| Harness | Version | Shim PID | Child PID / TTY | Observed child outcome |
|---|---|---:|---|---|
| Claude Code | `2.1.226 (Claude Code)` | 3892 | 3896 / `ttys006` | `terminated` |
| codex-cli | `codex-cli 0.147.0` | 11979 | 11983 / `ttys006` | `terminated` |

This is version-pinned evidence for teardown selection, not an absence oracle. The approved design still records child
identity durably, uses `kill(pid, 0)` as the sole presence/absence observation, and refuses relaunch unless it observes
`ESRCH`.

## Safety boundary

- Script: `hack/probe-shim-sighup.sh` from the issue-182 PR 1 worktree.
- Each leg created its own mode-`0700` temporary HOME and `/usr/bin/script` nested PTY.
- The probe accepted only the literal harness names `claude` and `codex`.
- Process topology was read from one `ps -axo pid=,ppid=,tty=,comm=` snapshot at a time until the direct child had a
  PTY. The recorded child PPID equaled the recorded shim PID.
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

Claude record (SHA-256 `e71d0f4b6019b4881392461df1d05db9dbaaf811cad143873adaaf5273bdf47b`):

```text
harness=claude
harness_version=2.1.226 (Claude Code)
topology=shim-parent-of-harness-child-on-pty
shim_pid=3892
child_pid=3896
child_ppid_matches=true
child_tty=ttys006
signal_target=owned-shim-only
signal=SIGHUP
shim_terminated=true
child_outcome=terminated
default_tmux_targeted=false
```

Codex record (SHA-256 `d2c03c94eb6f151a2a6ee37910de530952a997af2fab7674f4c734447c945aa1`):

```text
harness=codex
harness_version=codex-cli 0.147.0
topology=shim-parent-of-harness-child-on-pty
shim_pid=11979
child_pid=11983
child_ppid_matches=true
child_tty=ttys006
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
multi-line version parsing, transient pre-PTY topology observation, required output keys, owned child cleanup, and an
unrelated sentinel process remaining alive. The fake PATH includes a `tmux` canary and fails if the probe invokes it.
