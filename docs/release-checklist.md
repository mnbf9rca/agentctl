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

## 0.5.0 pre-promotion evidence

Run these observations against the rebuilt candidate. Record the command,
candidate commit, terminal/host details, result, and the committed evidence
location in `docs/release-verification-notes.md` before opening the promotion
PR.

1. **Detached launch in an ordinary terminal** — record that the default launch
   completed without a tmux presentation and that its role remained running
   after the terminal returned to the shell.
2. **Per-role attach, repaint, verbatim input, and clean disconnect/re-attach**
   — record the role, observed repaint, exact input observation, disconnect,
   and successful replacement viewer. A clean viewer disconnect means closing
   the viewer's terminal window or tab, or otherwise closing its PTY at the
   terminal boundary; typed `Ctrl-C` reaches the harness and can interrupt it.
   Record the one-viewer refusal separately.
3. **SIGWINCH resize observation** — record the terminal dimensions before and
   after resize and the role-side observation of the changed dimensions.
4. **handled/ignored/blocked signal and terminal restoration** — separately
   record handled `SIGINT`, `SIGTERM`, `SIGHUP`, and `SIGQUIT` with their signal
   exit observation and restored terminal. Separately record inherited ignored
   or blocked behavior for each eligible signal; record that `SIGKILL` and
   `SIGSTOP` remain excluded and make no restoration promise. Record the exact
   terminal restoration observation for every completed attachment.
5. **§15.9 built-artifact metadata** — record `go version -m` output for each
   built Darwin binary and each extracted Darwin binary. Each record must show
   `golang.org/x/sys v0.47.0` and identify the binary hash it describes.
6. **Archive-license evidence** — record the archive hash and extracted-file
   observation for every Darwin archive. Each extraction must contain
   `LICENSES/golang.org/x/sys/LICENSE` alongside the release's required license
   materials.

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
