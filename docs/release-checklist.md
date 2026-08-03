# Release Verification Runbook

Run this when a release changes tmux targeting, harness startup, or injected
command delivery. Otherwise tick "Checklist not required" (box 2) on the
promotion PR below and skip this file. Rationale and results history:
[docs/release-verification-notes.md](release-verification-notes.md).

## Test 1 — Run the wrapper

```bash
bash hack/release-verify.sh
```
- [ ] `PROBES PASS` printed

## Test 2 — Attach Window 2

`hack/verify-injection.sh` prints its own `ATTACH:` line early — before its
session exists. Wait for the first "Press Enter only after its TUI is fully
ready" prompt, then run that line from Window 2.
- [ ] Window 2 is attached and shows the harness panes live

## Test 3 — Per-harness judgment (once for claude, once for codex)

- [ ] The harness's TUI is actually ready in Window 2 — only then return to
      Window 1 and press Enter

Window 1 prints "Review snapshots:" with four file paths. Open all four in
Window 3 and judge each from what you see there:
- [ ] `junk` snapshot: `agentctl-verification-junk` is visibly present
- [ ] `cleared` snapshot: `C-u` cleared it
- [ ] `popup` snapshot: `/clear` is the exact selected match
- [ ] `reset` snapshot: the conversation shows a completed reset

Answer `y` at Window 1's prompt only if all four are true; otherwise `N`.

## Test 4 — Completion

- [ ] `ALL VERIFIED — evidence appended; commit docs/release-verification-notes.md`
      printed
- [ ] `docs/release-verification-notes.md` committed

## --measure (only when `internal/tmuxx.payloadDelay` changes)

```bash
bash hack/release-verify.sh --measure
```
Judge each trial pair the same way as Test 3's `popup`/`reset` snapshots.

## Test 5 — Promotion PR

`--body-file` is mandatory: GitHub shows no template chooser for PRs, so
without it the attestation checkbox is silently lost.

```bash
gh pr create --base release --head main \
  --title "Release v$(hack/next-version.sh)" \
  --body-file .github/PULL_REQUEST_TEMPLATE/release-promotion.md
```
- [ ] The correct box is ticked — "Checklist run" (Tests 1–4 passed, evidence
      committed on main) or "Checklist not required"
- [ ] The `Version:` line is filled in with `hack/next-version.sh`'s output

## Post-promotion — notarization

The `notary-check` job exits 0 even while notarization is still pending, so
a green `Release` run alone proves nothing. Open its log:
- [ ] It printed `notarization accepted` (if "pending" instead, re-check
      later before calling the release fully notarized)
