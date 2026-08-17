# Release verification checklist

## Prerequisites

- Start at the repository root. The worktree must be clean; release evidence must come from a clean checkout.
- Use macOS. Install `make`, Go, `tmux`, `claude`, `codex`, and `amq`.
- Have two additional terminal windows or tabs available for the harness+role
  viewers. The verifier names each viewer and prints its exact attach command.

## Commands

Run these two commands from the repository root:

```bash
make build
hack/release-verify.sh
```

Stop if either command exits nonzero. Otherwise, leave the verifier running and
follow the numbered prompts it prints.

## Human-only steps

The live walkthrough has exactly three human confirmations. Each appears in a
bounded `OPERATOR ACTION` / `OPERATOR CHECKPOINT` block and identifies the
viewer by harness and role, never by a terminal-window number:

1. Confirm the Claude role `a` viewer and Codex role `b` viewer each show the
   named live harness.
2. After the verifier proves the original Claude role `a` child absent,
   relaunches it, and observes different runtime identities, confirm the
   reattached Claude role `a` viewer shows the fresh replacement harness.
3. Close the Claude role `a` and Codex role `b` viewers at the terminal
   boundary, then confirm the viewer terminals are closed. A clean viewer
   disconnect means closing the viewer's terminal window or tab, or otherwise
   closing its PTY at the terminal boundary; typed `Ctrl-C` reaches the harness
   and can interrupt it. The verifier, not the operator, checks that both roles
   remain running.

Answer `y` only after making the named observation and `n` at the first
mismatch. The verifier performs all command execution, PID comparison, status
checking, cleanup, skill/content checks, and other objective evidence capture.

When the run ends, record the verdict. Record a pass only when the command
   exits 0 and prints `ALL VERIFIED — evidence appended`; otherwise record a
   failure and preserve the evidence.

After the fleet promotes the release, open the `Release` workflow's
   `notary-check` log. Confirm it printed `notarization accepted`.
   `notarization still pending; re-check manually` also exits 0 and is not
   acceptance.

## 0.5.0 pre-promotion claim sources

The three-confirmation live walkthrough is a smoke test. Its committed result
block in `docs/release-verification-notes.md` records only the observations at
checkpoints B.C1–B.C3 and the verifier's teardown result. Properties not
observed there are proven by the named automated guards below. A checked
promotion-form box claims that its listed sources passed; it does not turn an
automated result into a live observation.

1. **Detached launch in an ordinary terminal** — the live result block records
   the detached fleet, both roles still running after the B.C3 viewer closure,
   and teardown exit 3 with the detached durable fleet absent.
2. **Per-role attach, repaint, verbatim input, and clean disconnect/re-attach**
   — the live result block records the B.C1 role surfaces, B.C2 fresh replacement
   and reattach, and B.C3 viewer closure with both roles still running. The
   named automated guards are
   `TestAttachServerAdmitsSameUIDAfterApplyingSizeAndRoutesFramesInOrder` for
   initial-size-before-admission ordering,
   `TestRoleTransportPreservesVerbatimOutputAndReturnsExactFinalCounters` for
   verbatim transport,
   `TestIntegrationDetachedRoleAttachReleasesOnSignalAndReadmits` for
   single-viewer refusal and signal-driven readmission, and
   `TestAttachServerReleasesQuietViewerOnEOFAndAdmitsReplacement` for clean-EOF
   readmission. A clean viewer disconnect means closing the viewer's terminal
   window or tab, or otherwise closing its PTY at the terminal boundary; typed
   `Ctrl-C` reaches the harness and can interrupt it.
3. **Exact SIGWINCH frame emission and TIOCSWINSZ application** — these are
   automated properties, not live result-block observations. Their named
   guards are
   `TestViewerResizeEmitsObservedWindowSizeAsOneSerializedControlFrame` and
   `TestTerminalConcurrentResizeUsesIndependentExactValues`.
4. **handled/ignored/blocked signal and terminal restoration** — these are
   automated properties, not live result-block observations. The named guards
   are `TestIntegrationDetachedRoleAttachReleasesOnSignalAndReadmits` for
   handled `SIGINT`, `SIGTERM`, `SIGHUP`, and `SIGQUIT` plus restoration and
   readmission; `TestSignalProviderSubscribesOnlyObservedOrdinaryUnblockedCandidates`
   for inherited ignored and blocked signals;
   `TestRoleClientStartupAndRestorationAreOneTotalOrder` for terminal-mode
   ordering and exactly-once restoration; and
   `TestIntegrationRoleAttachNeverMutatesParentDescriptorFlagsAcrossStopAndKill`
   for the `SIGSTOP` and `SIGKILL` exclusions.
5. **Built-artifact executable smoke** — `.github/workflows/release.yml` runs
   the exact Darwin arm64 and amd64 binaries produced in `dist/` by the release
   build, requires both to report the expected release version, and completes
   that smoke before `Publish (undraft) release`.
6. **Archive-license contents** — `.goreleaser.yaml` declares
   `LICENSES/golang.org/x/sys/LICENSE` and the other required license materials
   in `archives.files`, which includes them in every Darwin archive before the
   draft release can be published.

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
