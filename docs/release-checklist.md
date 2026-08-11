# Release verification checklist

## Prerequisites

- Start at the repository root. The worktree must be clean; release evidence must come from a clean checkout.
- Use macOS and iTerm2. Install `make`, Go, `tmux`, `claude`, `codex`, `amq`, and
  the `install(1)` utility.
- Be ready to sign in to Claude Code and Codex if the verifier asks.

## Commands

Run these two commands from the repository root:

```bash
make build
hack/release-verify.sh
```

Stop if either command exits nonzero. Otherwise, leave the verifier running and
follow the numbered prompts it prints.

## Human-only steps

1. Follow the verifier's prompts. Answer `y` only after making the observation
   it names. Answer `n` at the first mismatch; delivery output alone is not
   evidence that a harness action executed.
2. When the run ends, record the verdict. Record a pass only when the command
   exits 0 and prints `ALL VERIFIED — evidence appended`; otherwise record a
   failure and preserve the evidence.
3. After the fleet promotes the release, open the `Release` workflow's
   `notary-check` log. Confirm it printed `notarization accepted`.
   `notarization still pending; re-check manually` also exits 0 and is not
   acceptance.

## Evidence

The verifier creates its folders, removes the test fleets and temporary setup
it owns, and keeps raw files under `/tmp/agentctl-release-verify.*/`. The
results name the exact `verify-live` path. An exit-0 run adds one dated block
beneath `## Results history` in
[`docs/release-verification-notes.md`](release-verification-notes.md).

On request, the fleet reviews and commits the result block. It then opens the
promotion PR with the
[release-promotion template](../.github/PULL_REQUEST_TEMPLATE/release-promotion.md).
That template names [`hack/next-version.sh`](../hack/next-version.sh) as the
version source. The [promotion form
check](../.github/workflows/promotion-form-check.yml) checks the handoff.
