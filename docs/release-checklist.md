# Release Verification Runbook

Run this when a release changes tmux targeting, harness startup, injected
command delivery, or the agent-facing CLI/skill surface. Otherwise tick
"Checklist not required" (box 2) on the promotion PR below and skip this
file. Rationale and results history:
[docs/release-verification-notes.md](release-verification-notes.md).

Run the checklist from the clean primary checkout's repository root, not from
a linked worktree. The launched panes inherit the tmux server environment, and
AMQ auto-discovers the primary checkout's repo-local `.agent-mail`; a linked
worktree instead needs `AMQ_GLOBAL_ROOT` propagated into that server.

The normal path for Parts A–C is the release verifier. It numbers each
automated action and every human checkpoint, prints the exact command and
expected observation before the checkpoint, and records each `y` or `n` as an
operator claim. `[ACTION PASS B.N]` means only that a delivery command exited
successfully; the separately numbered `[CHECKPOINT PASS B.CN]` is the
operator-confirmed outcome. The verifier automatically tears down the Part B
and Part C resources on success, refusal, interrupt, or failure. Answer every
prompt exactly `y` or `n`; empty input and other text re-prompt. `n` is a named
failing operator claim, not a way to skip a judgment, and the verifier fails
closed after attempting teardown.

Standing release-prep rule: before every live release run, execute once every
checklist mechanism whose implementation changed since the previous release
and record the result. This is the execution-audit practice established by
[issue #177](https://github.com/mnbf9rca/agentctl/issues/177).

### Issue #182 shim and interim-diagnostic gate

For a release containing issue #182 work, run the nested-PTY SIGHUP probe once per installed harness version from a
clean checkout. Each invocation creates its own private HOME and PTY, never contacts tmux, signals only its recorded
shim fixture, and refuses to overwrite evidence:

```bash
shim_probe_dir="$(mktemp -d /tmp/agentctl-shim-sighup.XXXXXX)"
hack/probe-shim-sighup.sh --harness claude --output "$shim_probe_dir/claude.txt"
hack/probe-shim-sighup.sh --harness codex --output "$shim_probe_dir/codex.txt"
```

- [ ] Each record names the exact harness version, positive shim/child PIDs, matching parent topology, a nonempty PTY,
      `signal_target=owned-shim-only`, shim termination, child outcome, and `default_tmux_targeted=false`
- [ ] `go test ./hack -run TestProbeShimSIGHUP -count=1` passed, including the unrelated-process and tmux canaries
- [ ] The release notes record both observed child outcomes; no SIGHUP result is used as absence evidence
- [ ] Focused aggregate tests cover below/equal/above pane counts and near misses, and the exact emitted line remains
      `note: all N roster roles are missing; unmanaged window "W" has N panes`
- [ ] Operator recovery guidance still cites the tracked
      [incident replay](security/2026-08-10-issue-182-replay-evidence.md), `classifyRelaunchWindow`, sole-window exit 3,
      the eight-pane duplicate interval, inference-qualified low-duplication order, and `kill` plus `launch`

## Part A — Start the verifier

Resolve the release version and prove that the embedded skill documents that
same version before building release artifacts:

```bash
release_version="$(hack/next-version.sh)"
hack/check-skill-version.sh "$release_version"
```

- [ ] The skill version check exited 0 and printed no mismatch or missing-version error

Start the default interactive walkthrough with a fresh, isolated default tmux
socket directory. This deliberately exercises the connect-ENOENT path without
touching the operator's live default server; the setting is inherited by the
verifier, agentctl, and their tmux children. The leg must not inherit an
ambient tmux client, because `TMUX` takes precedence over `TMUX_TMPDIR` and
`TMUX_PANE` is an agentctl inside-tmux selection and self-targeting input. The
subshell trap first kills any server on the isolated socket while its directory
still exists, then removes only the temporary directory created for this run:

```bash
release_tmux_tmpdir="$(mktemp -d /tmp/agentctl-release-tmux.XXXXXX)"
(
  trap 'TMUX_TMPDIR="$release_tmux_tmpdir" tmux kill-server 2>/dev/null; rm -rf -- "$release_tmux_tmpdir"' EXIT
  unset TMUX TMUX_PANE
  export TMUX_TMPDIR="$release_tmux_tmpdir"
  bash hack/release-verify.sh
)
```

The no-server wrapper leg introduced for #147 was executed and released in
the [final #162 reviewer gate](https://github.com/mnbf9rca/agentctl/pull/162#issuecomment-5229600302).

- [ ] The verifier printed the default-server connect-ENOENT observation,
      created its uniquely named wrapper-owned keeper session, and later
      printed the keeper cleanup pass
- [ ] `PROBES PASS` printed; all four probes completed and no throwaway server survived
- [ ] The verifier ran `./bin/agentctl launch --session relverify --roles
      a:claude,b:codex --efforts b:high` successfully

## Part B — Follow the verifier's live release-candidate walkthrough

The verifier launches `relverify` with `a:claude,b:codex` and effort `high`
for `b`. Leave the verifier running in iTerm2 Window 1. Press `Command-N` to
open a second iTerm2 window, then run the bare attach command printed by the
verifier in Window 2. Keep that attachment open through checkpoint B.C9. After
each visual observation, return to Window 1 to answer the numbered checkpoint.

- [ ] At checkpoint B.C1, the narration starts with `agentctl: attaching session "relverify" (2 windows)
      in iTerm2…` when the advisory window-count read succeeds, and warns that
      the Command Menu belongs to iTerm2. If `(2 windows)` is omitted, record
      the advisory read failure in `docs/release-verification-notes.md`; omission
      is not a release failure and agentctl never guesses the count
- [ ] Window 2 shows the claude and codex tabs; return to Window 1 and answer
      `y` at checkpoint B.C1

Keep this attachment open through the live checks below. At checkpoint B.C10,
press `esc` to detach cleanly; **do not use uppercase `X`**. The
[verified §3.4 contract](superpowers/specs/2026-08-01-agentctl-design.md#34-iterm2-force-quit-of-tmux-control-mode-2026-08-04)
shows that `X` leaves the fleet running but wedges the tmux client, so the
terminal stays busy and agentctl cannot report. After `esc`, expect the
post-detach session-state report beginning with
`agentctl: control-mode attachment to session "relverify" ended (tmux exit 0)`.

### Claude clear

- [ ] At checkpoint B.C2, in the claude tab type junk without pressing Enter;
      return to Window 1 and answer `y` when ready
- [ ] Watch the verifier run `./bin/agentctl clear --session relverify a`; answer
      `y` at checkpoint B.C3 only if junk cleared, `/clear` executed, and the
      conversation reset. `[ACTION PASS B.3]` alone is not that outcome claim

### Codex clear

- [ ] At checkpoint B.C4, in the codex tab type junk without pressing Enter;
      return to Window 1 and answer `y` when ready
- [ ] Watch the verifier run `./bin/agentctl clear --session relverify b`; answer
      `y` at checkpoint B.C5 only if junk cleared, `/clear` executed, and the
      conversation reset. `[ACTION PASS B.4]` alone is not that outcome claim

### Compact spot check

- [ ] At checkpoint B.C6, in the claude tab type junk without pressing Enter;
      return to Window 1 and answer `y` when ready
- [ ] Watch the verifier run `./bin/agentctl compact --session relverify a`; answer
      `y` at checkpoint B.C7 only if junk cleared, `/compact` executed, and the
      conversation compacted. `[ACTION PASS B.5]` alone is not that outcome claim

### Relaunch a missing role

Relaunch deliberately creates a **new process**. It does not preserve the old
conversation, context, or scrollback. The verifier resolves `relverify` to its
exact tmux session ID, resolves role `b` to its exact window and pane IDs, and
records the original pane ID before printing the resolved window ID in this
setup command:

```text
tmux kill-window -t @ID
```

- [ ] At checkpoint B.C8, in the codex tab type junk without pressing Enter;
      return to Window 1 and answer `y` when it is ready for the relaunch
      process-discontinuity check
- [ ] The verifier ran that exact-ID removal, then ran
      `./bin/agentctl status --session relverify` and printed `RELAUNCH PASS
      (role b reported missing after exact-ID removal)`
- [ ] The verifier ran `./bin/agentctl relaunch --session relverify b` and printed
      exactly this line, substituting the observed IDs and primary-checkout root:

      ```text
      agentctl: relaunched b in relverify: window @ID, pane %ID, harness codex (stored), model default (stored), effort high (stored), dir REPO_ROOT (stored)
      ```

- [ ] The verifier compared the resolved pane IDs and printed `RELAUNCH PASS
      (role b pane changed from %OLD to %NEW)`. Reusing `%OLD` is a release
      failure
- [ ] A second `./bin/agentctl status --session relverify` printed `RELAUNCH
      PASS (role b restored to running)`
- [ ] Answer `y` at checkpoint B.C9 only when its exact claim is visible:

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

### Detach, automated teardown, and evidence

- [ ] After checkpoint B.C9 passes, return to Window 2, press `esc` to detach
      cleanly, and do not use uppercase `X`. Wait for the post-detach
      session-state report, return to Window 1, and answer `y` at checkpoint
      B.C10

- [ ] The verifier killed `relverify`; `./bin/agentctl status --session relverify`
      exited `3` when other tmux sessions remained or `6` when `relverify` was
      the last session and the server exited. Both are expected absence results;
      no matching tmux process remained. After successful Parts A–C, the
      verifier requires exactly one line equal to `## Results history` in
      `docs/release-verification-notes.md` and proves the evidence was inserted
      once immediately below it. An absent, suffixed, substring, or duplicate
      marker, or a failed insertion, is a verifier failure. Only a successful
      single insertion prints `ALL VERIFIED — evidence appended`.
- [ ] Commit `docs/release-verification-notes.md` as the **last** step of the
      ceremony, only after the attach-narration restyle has merged and the full
      checklist has been rerun against the shipped output. Never commit
      provisional evidence from an earlier run

Every prompt accepts exactly `y` or `n`. Empty input and other text re-prompt; `n`
fails closed after teardown is attempted.

## Part C — Follow the verifier's live skill-discovery walkthrough

The verifier creates a separate stub fleet on a named throwaway tmux socket,
an empty temporary project, and a mode-`0700` temporary `HOME`. The macOS probe
proved `~/.codex/auth.json` sufficient for codex file-based seeding. Separate
Claude Code 2.1.226 probes proved that an exact
`$REAL_HOME/Library/Keychains -> $TEMP_HOME/Library/Keychains` symlink makes
the existing credential available, but an interactive first start still asks
for re-authentication unless the temporary HOME also contains a synthesized
mode-`0600` `.claude.json` with exactly
`{"hasCompletedOnboarding":true}`. The verifier never offers or copies a
Claude credential file or the operator's real Claude configuration.
If the proven Codex file exists, the verifier prints that exact filename and
asks once for consent before copying it with mode `0600`; its contents are never
printed. Independently, the verifier prints the exact Claude Keychains source
and destination and asks for separate consent, stating that both probe
harnesses can reach the operator's login keychain through the link (per-item
ACLs still apply). Under that same consent, it states the fixed temporary
`.claude.json` destination and synthesizes only the minimal onboarding boolean.
That file is configuration, not credentials: the link copies no secret, and
refresh writes reach the real login keychain as they do for the operator's
daily harnesses. Declining the link also declines the synthesized file.

The Part C wrapper's current authentication, consent, launch, and cleanup legs
introduced for #148 were executed in the [#178 audit and final
re-gate](https://github.com/mnbf9rca/agentctl/pull/178#issuecomment-5230449087).

The Part C synthesized-onboarding seed, link-only consent scope, and C.C1
no-reauthentication mechanism changed for #188 were executed in the [#199
probe, mutation checks, and final reviewer
gate](https://github.com/mnbf9rca/agentctl/pull/199#issuecomment-5236425269).

If the Claude link is declined or its fixed source is absent, the verifier
offers guided Claude sign-in instead. On consent it creates a mode-`0700`
`$TEMP_HOME/Library/Keychains` and an empty `login.keychain-db` there with
`security create-keychain` before launch; guided sign-in mints a fresh token
into that isolated keychain. Declining both Claude paths fails Part C before
AMQ initialization, skill installation, or fleet launch, regardless of the
independent Codex choice. Harness processes receive only the temporary `HOME`;
they never receive the operator's real `HOME` value.

Never seed Part C with `CLAUDE_CODE_OAUTH_TOKEN`: the environment-token path can
silently delete the real Keychain credential on harness exit
([anthropics/claude-code#37512](https://github.com/anthropics/claude-code/issues/37512)).
For a manual run where Keychain access is locked, such as SSH or launchd, use
`claude setup-token` as the documented fallback rather than exporting that
environment variable.

- [ ] At checkpoint C.C1, answer `y` only for the authentication claim the
      verifier prints for the selected paths. With the Claude link consented,
      verify Claude started without requiring re-authentication; when the Codex
      seed was also consented, verify neither harness required re-authentication.
      Theme or workspace-trust prompts do not contradict that narrow claim. On
      the isolated-keychain path, complete Claude onboarding/sign-in and verify
      it minted a fresh token; complete Codex sign-in only if its independent
      file copy was declined or unavailable
- [ ] At checkpoint C.C2, in the
      Claude Code tab, type `/skills`, press Enter, find `agentctl` in the
      displayed inventory, and press `esc` to close it. Repeat those exact
      inventory actions in the codex tab. After detaching back to the verifier,
      answer `y` only if both inventories listed `agentctl`
- [ ] Ask each harness the verifier's exact `ambiguous` question. After both
      answers are visible and the attachment has returned to the verifier,
      compare them with the quoted status-states meaning and answer `y` at
      checkpoint C.C3 only if both match
- [ ] After the observations, press `esc`, not uppercase `X`, and wait for the
      post-detach report. The verifier tears down only its named probe fleet and
      socket before removing the exact Keychains link, restores `HOME` and
      `PATH`, removes only the link and never its target, then removes the
      credential-bearing temporary `HOME` and synthesized onboarding file on
      every successful cleanup path.
      If consent aborts before that named server is created, tmux's exact
      single-line connect-ENOENT response for that wrapper-owned socket is
      observed as socket absence so credential-HOME cleanup still completes.
      If fleet or socket cleanup requires a retry, the link and temporary HOME
      remain together until no owned harness can use them; once fleet and socket
      absence is observed, link removal precedes HOME removal

## Manual fallback for Parts A–C

This appendix is for troubleshooting a verifier failure or interruption, not
the normal release path. Do not paste these blocks during a successful normal
walkthrough: the verifier owns the resources it creates and performs cleanup.
In particular, never point Part C at the default tmux server or a real `HOME`.
The normal verifier owns both exact consent mechanisms and their safety-ordered
cleanup. For manual troubleshooting, copy no authentication files and do not
link the operator's Keychains directory. Create an isolated login keychain,
complete both harness sign-ins after attaching to the isolated fleet, then
continue with the inventory and status-meaning observations. In an SSH or
launchd context where Keychain access stays locked, `claude setup-token` is the
manual fallback; never export `CLAUDE_CODE_OAUTH_TOKEN` for this probe.

Set up the isolated socket and fleet from the clean primary checkout. The tmux
shim scopes every tmux command agentctl runs; `amq coop init` scopes AMQ to the
throwaway project:

```bash
probe_top="$(git rev-parse --show-toplevel)"
probe_root="$(mktemp -d /tmp/agentctl-skill-verify.XXXXXX)"
original_home="$HOME"
original_path="$PATH"
skill_home="$probe_root/home"
probe_project="$probe_root/project"
probe_bin="$probe_root/bin"
probe_socket="agentctl-skill-verify-$$"
real_tmux="$(command -v tmux)"
install -d -m 0700 "$skill_home" "$skill_home/Library/Keychains"
install -d -m 0755 "$probe_project" "$probe_bin"
HOME="$skill_home" security create-keychain -p '' \
  "$skill_home/Library/Keychains/login.keychain-db"
cat >"$probe_bin/tmux" <<EOF
#!/usr/bin/env bash
exec "$real_tmux" -L "$probe_socket" "\$@"
EOF
chmod 0755 "$probe_bin/tmux"
export HOME="$skill_home"
export PATH="$probe_bin:$PATH"
cd "$probe_project"
amq coop init --agents a,b,user
"$probe_top/bin/agentctl" skill install
"$probe_top/bin/agentctl" launch --session skillverify \
  --roles a:claude,b:codex --dir "$probe_project"
"$probe_top/bin/agentctl" attach --session skillverify
```

- [ ] `"$probe_top/bin/agentctl" skill install` reported successful
      installs under both `$skill_home/.claude/skills/agentctl/` and
      `$skill_home/.agents/skills/agentctl/`
- [ ] The stub fleet ran only on its named throwaway tmux socket, both harnesses
      reached a ready prompt, and each harness's skill inventory listed `agentctl`
- [ ] Ask each harness: "What does `ambiguous` mean in agentctl status, and
      which commands refuse on it?" The answer matches
      [`skills/agentctl/references/status-states.md`](../skills/agentctl/references/status-states.md):
      more than one window has the role's exact name, no window is selected as
      real, and role-targeted control commands (`clear` and `compact`) refuse
      until an operator repairs the ambiguity with raw tmux
- [ ] Tear down the stub fleet and its named tmux server, then remove the
      temporary project and `HOME`; no process or skill-probe file survived

After the observations, tear down only the named probe resources and restore
the terminal environment:

```bash
"$probe_top/bin/agentctl" kill --session skillverify
"$real_tmux" -L "$probe_socket" kill-server 2>/dev/null || true
cd "$probe_top"
export HOME="$original_home"
export PATH="$original_path"
rm -rf -- "$probe_root"
```

## --measure (only when `internal/tmuxx.payloadDelay` changes)

```bash
bash hack/release-verify.sh --measure
```
The forensic `verify-injection.sh` rig is measure-only. Judge each prompted
snapshot batch and answer exactly `y` or `n`.
Wait for the first "Is the <harness> TUI fully ready?" prompt, then run the command printed under `ATTACH:` from Window 2.

## Part D — Promotion PR

`--body-file` is mandatory: GitHub shows no template chooser for PRs, so
without it the attestation checkbox is silently lost.

```bash
gh pr create --base release --head main \
  --title "Release v$(hack/next-version.sh)" \
  --body-file .github/PULL_REQUEST_TEMPLATE/release-promotion.md
```

If this normal `main` to `release` PR conflicts only because `release` carries
a prior promotion-only commit, preserve both histories with the fallback used
for the 0.2.0 handoff. First confirm that `release` holds no unique content;
then start from current `origin/main` and merge `origin/release` with the
`ours` strategy:

```bash
git fetch origin
git diff --stat origin/main...origin/release
version="$(hack/next-version.sh)"
git switch --create "promote/$version" origin/main
git merge --strategy ours --no-ff origin/release -m "Promote v$version"
git push -u origin "promote/$version"
gh pr create --base release --head "promote/$version" \
  --title "Release v$version" \
  --body-file .github/PULL_REQUEST_TEMPLATE/release-promotion.md
```

The pre-merge three-dot diff must print no paths. If it reports anything,
stop: `release` carries unique content that an `ours` merge would discard.
Merge the fallback PR with a merge commit; do not squash or rebase it. Do not
use the fallback when the normal promotion PR is mergeable.

- [ ] The correct box is ticked — "Checklist run" (Parts A–C passed, evidence
      committed on main) or "Checklist not required"
- [ ] The `Version:` line is filled in with `hack/next-version.sh`'s output

## Post-promotion — notarization

The `notary-check` job exits 0 even while notarization is still pending, so
a green `Release` run alone proves nothing. Open its log:
- [ ] It printed `notarization accepted` (if "pending" instead, re-check
      later before calling the release fully notarized)
