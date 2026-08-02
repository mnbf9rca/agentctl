# agentctl brew packaging design — 2026-08-02

Status: approved in design session 2026-08-02.
Companion documents: [`2026-08-01-agentctl-design.md`](2026-08-01-agentctl-design.md) (v1 spec; §1.1 principle applies to every output this pipeline produces), [`SECURITY.md`](../../../SECURITY.md), [`docs/release-checklist.md`](../../release-checklist.md).

## 1. Summary and goals

Phase 2 makes agentctl installable with `brew install mnbf9rca/tap/agentctl` for the
maintainer and early adopters. Users get a prebuilt binary — never a source build — with
tmux installed alongside, honest caveats about runtime-optional tooling, and verifiable
build provenance. Releases are cut by a deliberate human act (promoting `main` to a
`release` branch); everything downstream of that act is automated and fail-closed:
a failed pipeline leaves no public artifact.

Non-goals for phase 2: Linux builds, homebrew-core submission, true Homebrew bottles
(`brew test-bot` machinery), Apple Developer ID signing/notarization, fully automatic
releases (release-please style), dependabot auto-merge policy.

## 2. Decisions

| Topic | Decision | Rationale |
|---|---|---|
| Audience | Maintainer + early adopters; README advertises the install | Sets ceremony level: real pipeline, no core-grade bureaucracy |
| Tap | New public repo `mnbf9rca/homebrew-tap` | Taps must be separate repos named `homebrew-*`; generic name so future tools share it. Install: `brew install mnbf9rca/tap/agentctl` |
| Formula type | Binary formula (per-arch tarball `url` + pinned `sha256`), not source build, not true bottles | User experience of a bottle without `test-bot` machinery; "I hate source build brews" |
| Release trigger | Push (merged promotion PR) to a protected `release` branch | The promotion PR is the human release act and carries the checklist attestation |
| Versioning | `VERSION` file; if ahead of latest `v*` tag it wins, else latest tag + 1 patch | "If I haven't manually incremented it, do it for me." Workflow never writes the file back, so automation never commits to protected branches |
| Tag creation | Workflow-only, after artifacts build and smoke-test | Single tagging authority; tag remains the version source of truth for existing ldflags stamping |
| Build tool | goreleaser | Build matrix, archives, checksums, release, tap publish in one config; hand-rolling reimplements all of it |
| Platforms | darwin/arm64 + darwin/amd64; no Linux | Intel Macs remain brew-supported through ≥2028 and cost one matrix line; Linux is untested and shipping untested binaries violates §1.1 |
| Runner | `macos-latest` (arm64) | The workflow executes the actual artifacts: arm64 natively, x86_64 under Rosetta 2 |
| Apple signing | None | Homebrew's downloader sets no quarantine attribute, so Gatekeeper notarization never fires; the Go linker ad-hoc signs darwin/arm64 automatically |
| Provenance | Sigstore via `actions/attest-build-provenance` on tarballs + `checksums.txt` | Keyless, ~5 lines. Verified by `gh attestation verify`, not by brew — the formula's pinned SHA256s are the install-time tamper evidence |
| Cross-repo credential | Single-purpose GitHub App installed on `homebrew-tap` only; workflow mints 1-hour installation tokens via `actions/create-github-app-token` | No calendar expiry (kills the "release broke because a PAT expired" failure mode); long-lived key only mints scoped short-lived tokens; one-click revocation. Pure OIDC cannot mint GitHub repo-write tokens |
| Branch protection | `release` mirrors `main` (PRs + required checks); **signed commits required on both** | Promotion merges via GitHub are web-flow-signed. Rebase-merge is excluded on `release` (GitHub recreates those commits unsigned) |
| License | MIT `LICENSE` file in agentctl repo before first release; same in tap | Formula carries `license "MIT"`; goreleaser includes the file in tarballs |
| CodeQL | Enable now (PRs to main + weekly schedule) | Free for public repos; near-zero noise at this size |
| Dependabot | Config now for `gomod` + `github-actions`; auto-merge deferred | go.mod has zero external deps; the Actions pins are the real dependencies. Merges to main never release — decoupled by construction |
| Release checklist | Rewritten as a prescriptive operator runbook (§7) before the first brew release | The checklist becomes the runbook for a repeating pipeline; must be executable without interpretation |

## 3. Architecture

```
main (dev, protected, signed commits)
  │  promotion PR  ←— human release act; checklist attestation in PR body
  ▼
release (protected, signed commits, no direct pushes)
  │  push triggers release.yml (macos-latest)
  ▼
tag vX.Y.Z ──► goreleaser ──► GitHub Release (tarballs + checksums.txt + Sigstore provenance)
                     │
                     └──► mnbf9rca/homebrew-tap  (Formula/agentctl.rb regenerated)
                                │
                                ▼
                  brew install mnbf9rca/tap/agentctl
```

Components, one job each:

- **`VERSION`** (agentctl repo root): the intended next version, edited by a human only,
  and only to jump minor/major. Silence means patch.
- **`hack/next-version.sh`**: version resolution (VERSION file + tag list in → version
  out), unit-tested in the ordinary suite. The workflow calls the tested script; the YAML
  stays dumb.
- **`.github/workflows/release.yml`**: the only place a tag is ever created (§4).
- **`.goreleaser.yaml`**: build matrix, `CGO_ENABLED=0`, ldflags stamping the existing
  `internal/buildinfo.Stamp`, archives, checksums, release notes (commit list since
  previous tag), brew formula generation and tap push.
- **`mnbf9rca/homebrew-tap`**: `Formula/agentctl.rb` + README + MIT license. Written only
  by the release workflow via the GitHub App; humans never edit the formula.

Day-to-day development is unchanged: same main protection, same PR flow, same CI.

## 4. Release workflow

Trigger: push to `release`. Runner: `macos-latest`. Steps:

1. **Resolve version** via `hack/next-version.sh`: if `VERSION` > latest `v*` tag, use
   `VERSION`; else latest tag + 1 patch. A `VERSION` at or below the latest tag is
   ignored — no downgrade release is representable.
2. **Tag locally** (annotated `vX.Y.Z`) — not pushed yet.
3. **Build** via goreleaser in build-only mode: both darwin archs, checksums, archives.
4. **Smoke-test the actual artifacts**: run the arm64 binary natively and the x86_64
   binary under Rosetta; each must print exactly the tag version from `agentctl version`.
   Mismatch fails the run. (§1.1: the version claim is verified, not assumed.)
5. **Push the tag** — only after artifacts are proven. Failure before this point leaves
   nothing public: no tag, no release, no formula change.
6. **Publish**: goreleaser creates the GitHub Release with tarballs, `checksums.txt`, and
   generated notes.
7. **Attest**: `actions/attest-build-provenance` over tarballs and `checksums.txt`.
8. **Update the tap**: goreleaser pushes the regenerated formula using a fresh 1-hour
   GitHub App installation token.
9. **Post-publish verification** (separate job, clean macOS runner):
   `brew tap mnbf9rca/tap && brew install agentctl && brew test agentctl`, asserting the
   installed binary prints the tag. The release is not reported green until what a user
   would run has actually worked; if this fails, the release exists but the workflow is
   red — a true claim about a broken install path.

`workflow_dispatch` with a `dry_run` input runs steps 1–4 and stops: full rehearsal; the
local tag is discarded with the runner, nothing is pushed or published. This is how the pipeline is exercised before the first real release and
debugged afterwards without minting versions.

## 5. Formula and tap

- Binary formula: `on_macos` + `on_arm`/`on_intel` blocks, each with the release tarball
  `url` and pinned `sha256` from `checksums.txt`; `install` copies the binary.
- `depends_on "tmux"` — the one hard runtime dependency. Deliberately **not** declared:
  amq, Claude Code, codex. They are runtime-optional by design; preflight reports their
  absence honestly at the moment of use, and declaring them would force-install tools a
  user may manage differently.
- `caveats`: agentctl launches agents via `amq coop exec` and expects `amq` plus at least
  one harness (`claude`, `codex`) on PATH at launch time; agentctl reports what is
  missing when run. Factual, not aspirational.
- `test do`: `assert_match version.to_s, shell_output("#{bin}/agentctl version")` —
  `brew test agentctl` re-verifies the version claim on the installed binary.
- `license "MIT"`.
- Tap repo: default branch protection only (nothing but the App writes to it), no CI of
  its own in phase 2.

## 6. Checklist gating of promotion

The promotion PR (main → release) is the only new process surface:

- A promotion PR template with two mutually exclusive checkboxes:
  - "This release changes tmux targeting, harness startup, or injected delivery — the
    release checklist was run; results recorded in `docs/release-checklist.md`."
  - "No changes in checklist-covered areas since the last release — checklist not
    required."
- Checklist results continue to live in the results-history section of
  `docs/release-checklist.md`, merged to main **before** promotion, so the release
  tarball contains the evidence it shipped under.
- Deliberately not built: automation that detects whether the checklist "should" run.
  That judgment is exactly what the checklist's own preamble says automation structurally
  cannot make; automating the gate would assert something no machine verified.
- The gate is a recorded attestation, not a technical barrier: nothing stops promotion
  without the checklist. That is consistent with how the checklist binds today — it binds
  the human, and the PR body carries the claim permanently.

## 7. Release checklist rewrite

`docs/release-checklist.md` is restructured into a prescriptive operator runbook,
executable by a tired human without interpretation:

- Numbered steps in exact execution order; each step one imperative with the literal
  command and where to run it ("Open three iTerm windows. Window 1: run `…`. Window 2:
  run `agentctl attach …`, leave it running. Window 3: run `…`").
- Each step states the expected observation, and a record-this-value instruction wherever
  evidence is required.
- Kept unchanged: the "why this checklist exists" section, the attestation and evidence
  requirements, and the results-history format. Simplification targets the procedure's
  foolproofness, not its rigor.
- Lands on main before the first brew release, so release one ships under the new
  runbook.

## 8. Failure modes

| Failure | Observable state | Remediation |
|---|---|---|
| Build or smoke test fails (steps 1–4) | Nothing public: no tag, no release, no formula change | Fix on main, re-promote; the failed run's logs are the evidence |
| Tag pushed, publish fails mid-flight | Tag with no release | Re-run the publish job; goreleaser is idempotent against an existing tag |
| Release published, tap push fails (App uninstalled / key rotated) | Release exists; `brew upgrade` doesn't see it; workflow red at the last step | Fix the App installation or key secret, re-run the publish job. Existing installs are stale, never broken |
| Accidental re-promotion with no changes | A pointless-but-harmless patch release of identical code; release notes visibly contain zero commits | Accepted residual; not worth machinery |
| `VERSION` edited below the latest tag | Ignored by resolution; no downgrade possible | None needed |
| Rosetta absent on a future runner image | x86_64 smoke test fails closed; nothing ships | Decide deliberately (fix runner, or drop Intel with a tested claim behind it) |
| Post-publish verification fails | Release public but workflow red | Investigate; fix forward with a patch release |

## 9. Testing the pipeline

- `hack/next-version.sh` has unit tests in the ordinary suite.
- The existing macOS integration CI job gains `goreleaser check` and
  `goreleaser release --snapshot --skip=publish`: every PR proves the release build
  matrix, archiving, and version stamping still work — not just `go build`.
- The `dry_run` dispatch mode (§4) rehearses the real workflow end-to-end minus
  publication.
- Post-publish verification (§4 step 9) closes the loop on the published chain.
- Deliberately not tested by machine: the checklist runbook (structurally human, per its
  own preamble); the tap repo has no CI.

## 10. Work items (prerequisites before first release)

1. MIT `LICENSE` file in the agentctl repo.
2. `VERSION` file (initialized to the intended first release version) +
   `hack/next-version.sh` + tests.
3. `.goreleaser.yaml` + CI snapshot-build step.
4. `release` branch + branch protection (PRs, required checks, signed commits on both
   branches, no rebase-merge on `release`).
5. GitHub App created, installed on the new `homebrew-tap` repo; private key stored as an
   agentctl repo secret.
6. `mnbf9rca/homebrew-tap` repo (README, MIT license, empty `Formula/`).
7. `release.yml` with dry-run mode; rehearse via dry run.
8. Promotion PR template (§6).
9. Release checklist rewrite (§7).
10. CodeQL workflow; dependabot config (`gomod` + `github-actions`).
11. README install section (`brew install mnbf9rca/tap/agentctl`, caveats, provenance
    verification one-liner).

Sequencing note: items 1–3 are ordinary PRs to main; 4–6 are repo/settings acts by the
maintainer; 7 lands only after 5–6 exist; the first real release happens after 9's
runbook is merged and a dry run has passed.
