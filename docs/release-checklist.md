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
- [ ] The wrapper ran `./bin/agentctl launch --session relverify --roles
      a:claude,b:codex --efforts b:high` successfully

## Part B — Watch the live release-candidate delivery path

From Window 2, run the bare command printed after `Attach from Window 2 with:`:

```bash
./bin/agentctl attach --session relverify
```
- [ ] The narration starts with `agentctl: attaching session "relverify" (2 windows)
      in iTerm2…` when the advisory window-count read succeeds, and warns that
      the Command Menu belongs to iTerm2. If `(2 windows)` is omitted, record
      the advisory read failure in `docs/release-verification-notes.md`; omission
      is not a release failure and agentctl never guesses the count
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

Relaunch deliberately creates a **new process**. It does not preserve the old
conversation, context, or scrollback. The wrapper resolves `relverify` to its
exact tmux session ID, resolves role `b` to its exact window and pane IDs, and
records the original pane ID before printing the resolved window ID in this
setup command:

```text
tmux kill-window -t @ID
```

- [ ] In the codex tab, type junk without pressing Enter; answer `y` when it is
      ready for the relaunch process-discontinuity check
- [ ] The wrapper ran that exact-ID removal, then ran
      `./bin/agentctl status --session relverify` and printed `RELAUNCH PASS
      (role b reported missing after exact-ID removal)`
- [ ] The wrapper ran `./bin/agentctl relaunch --session relverify b` and printed
      exactly this line, substituting the observed IDs and primary-checkout root:

      ```text
      agentctl: relaunched b in relverify: window @ID, pane %ID, harness codex (stored), model default (stored), effort high (stored), dir REPO_ROOT (stored)
      ```

- [ ] The wrapper compared the resolved pane IDs and printed `RELAUNCH PASS
      (role b pane changed from %OLD to %NEW)`. Reusing `%OLD` is a release
      failure
- [ ] A second `./bin/agentctl status --session relverify` printed `RELAUNCH
      PASS (role b restored to running)`
- [ ] Answer `y` to the wrapper's single observation prompt only when its exact
      claim is visible:

      ```text
      One of the fleet's harnesses was terminated, and agentctl relaunched it from
      the fleet's stored configuration. The new pane is a new process: its harness,
      model and effort carry over; its conversation does not, so the junk you typed
      is gone.

      Do you see a fresh, ready codex input surface with no trace of that junk?
      ```

The pane-ID inequality is the machine proof of process discontinuity. Junk
absence alone is nearly tautological because a new pane trivially has no input
history; its value is direct human observability when paired with that machine
proof. Together they distinguish a new process holding role `b` from the old
process merely being reattached, and they guard any future restart command that
might reuse a pane.

The provenance line proves **CONFIG CONTINUITY**: harness, model, effort, and
directory came from the fleet's stored configuration. The pane-ID and junk
check proves **PROCESS DISCONTINUITY**: this is genuinely a new process with no
surviving conversation. Neither half alone is the relaunch contract.

### Automated teardown and evidence

- [ ] The wrapper killed `relverify`; `./bin/agentctl status --session relverify`
      exited `3` when other tmux sessions remained or `6` when `relverify` was
      the last session and the server exited. Both are expected absence results;
      the wrapper recorded which occurred in `docs/release-verification-notes.md`,
      no matching tmux process remained, and it printed `ALL VERIFIED — evidence appended`
- [ ] Commit `docs/release-verification-notes.md` as the **last** step of the
      ceremony, only after the attach-narration restyle has merged and the full
      checklist has been rerun against the shipped output. Never commit
      provisional evidence from an earlier run

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
