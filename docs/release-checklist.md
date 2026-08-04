# Release Verification Runbook

Run this when a release changes tmux targeting, harness startup, or injected
command delivery. Otherwise tick "Checklist not required" (box 2) on the
promotion PR below and skip this file. Rationale and results history:
[docs/release-verification-notes.md](release-verification-notes.md).

Run the checklist from the clean primary checkout's repository root, not from
a linked worktree. The launched panes inherit the tmux server environment, and
AMQ auto-discovers the primary checkout's repo-local `.agent-mail`; a linked
worktree instead needs `AMQ_GLOBAL_ROOT` propagated into that server.

## Part A — Run the wrapper

```bash
bash hack/release-verify.sh
```
- [ ] `PROBES PASS` printed; all four probes completed and no throwaway server survived
- [ ] The wrapper ran `./bin/agentctl launch --session relverify --roles a:claude,b:codex` successfully

## Part B — Watch the live release-candidate delivery path

From Window 2, run the bare command printed after `Attach from Window 2 with:`:

```bash
./bin/agentctl attach --session relverify
```
- [ ] The narration starts with `agentctl: attaching session "relverify" (2 windows) in iTerm2…`
      and warns that the Command Menu belongs to iTerm2
- [ ] Window 2 shows the claude and codex tabs; answer `y` at the attach prompt

Keep this attachment open through the live checks below. After the last visual
check, press `esc` to detach cleanly; **do not use uppercase `X`**. The
[verified §3.4 contract](superpowers/specs/2026-08-01-agentctl-design.md#34-iterm2-force-quit-of-tmux-control-mode-2026-08-04)
shows that `X` leaves the fleet running but wedges the tmux client, so the
terminal stays busy and agentctl cannot report. After `esc`, expect the
post-detach session-state report beginning with
`agentctl: control-mode attachment to session "relverify" ended (tmux exit 0)`.

### Claude clear

- [ ] In the claude tab, type junk without pressing Enter; answer `y` when ready
- [ ] Watch the wrapper run `./bin/agentctl clear --session relverify a`; answer
      `y` only if junk cleared, `/clear` executed, and the conversation reset

### Codex clear

- [ ] In the codex tab, type junk without pressing Enter; answer `y` when ready
- [ ] Watch the wrapper run `./bin/agentctl clear --session relverify b`; answer
      `y` only if junk cleared, `/clear` executed, and the conversation reset

### Compact spot check

- [ ] In the claude tab, type junk without pressing Enter; answer `y` when ready
- [ ] Watch the wrapper run `./bin/agentctl compact --session relverify a`; answer
      `y` only if junk cleared, `/compact` executed, and the conversation compacted

### Relaunch a missing role

This step is staged for the single-role `relaunch` surface. Finalize its exact
commands and output assertions against the merged implementation before marking
this runbook ready.

- [ ] The wrapper resolved the codex role's window ID, removed that exact ID,
      and `./bin/agentctl status --session relverify` reported role `b` missing
- [ ] The wrapper ran `./bin/agentctl relaunch --session relverify b`, asserted
      the printed harness/model/directory provenance, and confirmed explicit
      status restored role `b` to running
- [ ] In the relaunched codex tab, wait for the TUI to become ready; answer `y`
      only after observing the ready input surface

### Automated teardown and evidence

- [ ] The wrapper killed `relverify`; `./bin/agentctl status --session relverify`
      exited `3` (`6` only when no tmux server remained); no matching tmux
      process remained; and it printed `ALL VERIFIED — evidence appended`
- [ ] `docs/release-verification-notes.md` committed

Every prompt accepts exactly `y` or `n`. Empty input and other text re-prompt; `n`
fails closed after teardown is attempted.

## --measure (only when `internal/tmuxx.payloadDelay` changes)

```bash
bash hack/release-verify.sh --measure
```
The forensic `verify-injection.sh` rig is measure-only. Judge each prompted
snapshot batch and answer exactly `y` or `n`.
Wait for the first "Is the <harness> TUI fully ready?" prompt, then run the command printed under `ATTACH:` from Window 2.

## Part C — Promotion PR

`--body-file` is mandatory: GitHub shows no template chooser for PRs, so
without it the attestation checkbox is silently lost.

```bash
gh pr create --base release --head main \
  --title "Release v$(hack/next-version.sh)" \
  --body-file .github/PULL_REQUEST_TEMPLATE/release-promotion.md
```
- [ ] The correct box is ticked — "Checklist run" (Parts A–B passed, evidence
      committed on main) or "Checklist not required"
- [ ] The `Version:` line is filled in with `hack/next-version.sh`'s output

## Post-promotion — notarization

The `notary-check` job exits 0 even while notarization is still pending, so
a green `Release` run alone proves nothing. Open its log:
- [ ] It printed `notarization accepted` (if "pending" instead, re-check
      later before calling the release fully notarized)
