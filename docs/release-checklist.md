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

For 0.5.0 and later, the normal path is the Task 8 automated release-candidate
fixture and its evidence record. It must implement the atomic-cutover
checkpoint below against isolated runtime/state roots, a throwaway project,
stub or explicitly consented harnesses, and a named throwaway tmux server.
The retired pre-0.5 Parts A–C interactive walkthrough is not release evidence.

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
      and a nonempty `child_command` exactly equal to the selected harness path, plus
      `signal_target=owned-shim-only`, shim termination, child outcome, and `default_tmux_targeted=false`
- [ ] `go test ./hack -run TestProbeShimSIGHUP -count=1` passed, including the intermediate-child refusal,
      owned-descendant cleanup, unrelated-process sentinel, and tmux canary
- [ ] The release notes record both observed child outcomes; no SIGHUP result is used as absence evidence
- [ ] Focused aggregate tests cover below/equal/above pane counts and near misses, and the exact emitted line remains
      `note: all N roster roles are missing; unmanaged window "W" has N panes`
- [ ] Transition guidance cites the tracked
      [incident replay](security/2026-08-10-issue-182-replay-evidence.md), sole-window exit 3, the eight-pane duplicate
      interval, inference-qualified low-duplication order, and `kill` plus `launch`, while labeling
      `classifyRelaunchWindow` a retired pre-0.5 seam rather than a current recovery path

### 0.5.0 atomic-cutover checkpoint

For the public shim cutover, verify the release candidate rather than a package-only fixture. These checks use an
isolated `AGENTCTL_RUNTIME_ROOT`, isolated `AGENTCTL_STATE_ROOT`, throwaway HOME/project, stub or explicitly consented
harnesses, and a named throwaway tmux server. They never point at the operator's default tmux server or live agents.
Task 8 adds the complete automated release fixture and evidence record; this checkpoint pins what that fixture must
demonstrate and is not a substitute for it.

- [ ] `agentctl --help` lists public `run` and never lists `__shim`; `relaunch --help` describes only runtime-observed
      missing/ESRCH-backed stale roles and contains no `no-baseline` window recovery
- [ ] A public `launch` creates the durable fleet record before role start, every role reaches runtime `running`, and
      `status --session` reports `anchored` confidence separately from tmux presentation
- [ ] On the same throwaway fleet, `join-pane`, `break-pane`, `swap-pane`, and `move-window` each leave runtime identity
      and closed `clear`/`compact` delivery available; no assertion derives identity from window name, metadata, pane
      count, or process row
- [ ] `agentctl run --session direct --role a --harness HARNESS` reaches ready on an isolated PTY with no tmux server;
      a second process observes status, delivery, and kill, and `attach` prints the exact no-presentation refusal
- [ ] A second foreground role in the same cwd extends the durable roster after readiness. The same invocation from a
      different cwd refuses before role start and record mutation, and prints both stored/current paths with
      `fleet-directory-disagreement`
- [ ] Removing a volatile anchor while retaining a durable record yields `unanchored`; changing HOME/state override
      while the uid-rooted lock remains yields `state-root-disagreement` with both roots and no alternate-tree adoption
- [ ] A dead-shim `child-starting` fixture remains indeterminate with no expiry. Documentation directs the operator to
      the lockfile body's recorded state root and requires independent child-absence proof before manual record removal
- [ ] `kill` proves child exit/absence before optional presentation and fleet cleanup. The auto-disappearing final tmux
      window leg distinguishes presentation `gone` from `removed`; present/unavailable post-failure retains the fleet
      record
- [ ] Structural guards prove one `shellq.Join` site, no production `internal/target`, `DeliverPayload`, or `send-keys`,
      and no caller-payload field in the version-1 request
- [ ] `go version -m` on the built Darwin release candidate records `golang.org/x/sys v0.47.0`, and both Darwin archives
      include the x/sys license along with all previously required license material
- [ ] The upgrade notes state the flag day: stop pre-0.5 fleets with the old binary before upgrade; no tmux-metadata
      fleet is migrated, adopted, or spoken to through a dual protocol

## Task 8 — Automated 0.5.0 release-candidate walkthrough

Task 8 owns the complete isolated release fixture and evidence record for the
atomic cutover. It implements every observation pinned in the checkpoint above
against a built release candidate, including skill discovery. The retired
pre-0.5 Parts A–C walkthrough is removed because its window-identity attach and
pane-removal relaunch steps are not valid shim-plane evidence.

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

- [ ] The correct box is ticked — "Checklist run" (the Task 8 release-candidate
      fixture passed and its evidence is committed on main) or "Checklist not
      required"
- [ ] The `Version:` line is filled in with `hack/next-version.sh`'s output

## Post-promotion — notarization

The `notary-check` job exits 0 even while notarization is still pending, so
a green `Release` run alone proves nothing. Open its log:
- [ ] It printed `notarization accepted` (if "pending" instead, re-check
      later before calling the release fully notarized)
