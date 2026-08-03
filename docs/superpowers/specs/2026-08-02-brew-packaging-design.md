# agentctl brew packaging design — 2026-08-02

Status: approved in design session 2026-08-02; amended the same day after multi-agent
verification of every external contract the pipeline depends on (§3), and re-approved.
Companion documents: [`2026-08-01-agentctl-design.md`](2026-08-01-agentctl-design.md)
(v1 spec; §1.1 principle applies to every output this pipeline produces),
[`SECURITY.md`](../../../SECURITY.md), [`docs/release-checklist.md`](../../release-checklist.md).

## 1. Summary and goals

Phase 2 makes agentctl installable with `brew install mnbf9rca/tap/agentctl` for the
maintainer and early adopters. Users get a prebuilt, Developer-ID-signed and notarized
binary — never a source build — with tmux installed alongside, honest caveats about
runtime-optional tooling, and verifiable build provenance. Releases are cut by a
deliberate human act (promoting `main` to a `release` branch); everything downstream of
that act is automated and fail-closed: nothing becomes public until the exact bytes
being shipped have been executed and verified.

Non-goals for phase 2: Linux builds, homebrew-core submission, Homebrew *cask*
packaging (rejected — §2), goreleaser Pro, fully automatic releases (release-please
style), dependabot auto-merge policy.

## 2. Decisions

| Topic | Decision | Rationale |
|---|---|---|
| Audience | Maintainer + early adopters; README advertises the install | Sets ceremony level: real pipeline, no core-grade bureaucracy |
| Tap | New public repo `mnbf9rca/homebrew-tap` | Taps must be separate repos named `homebrew-*`; generic name so future tools share it |
| Install command | `brew install mnbf9rca/tap/agentctl` — fully qualified, the **only** documented form | Homebrew 6.0 tap trust (§3.4): the fully qualified name implicitly trusts just this formula; the old `brew tap` + short-name install now requires an intervening `brew trust` |
| Packaging | Binary **formula**, rendered by our own template step — not goreleaser's formula or cask generation | goreleaser hard-deprecated formula generation (`brews:`) in v2.16 (works until v3, but fails `goreleaser check`); its `homebrew_casks:` replacement produces a cask, which cannot carry a `test do` block or `license` stanza, and whose downloads Homebrew deliberately quarantines — forcing either notarization-with-online-first-run or a quarantine-stripping hook. A formula has none of that: formula downloads are never quarantined (§3.5). Hand-rolling matches what HashiCorp and Bun ship today, and no maintained off-the-shelf tool covers our requirement set (§3.6) |
| Signing | Developer ID sign + notarize the darwin binaries in-pipeline, via goreleaser OSS's `notarize.macos` pipe with `wait: false` | Not load-bearing for brew installs (no quarantine on formulas) but protects browser-downloaded tarballs and MDM-assessed fleets, and costs ~12 config lines given the maintainer's Apple Developer account. `wait: false` because a bare Mach-O cannot be stapled, so waiting changes nothing about the shipped artifact — and Apple notary outages (two multi-week episodes in 2026) must not block releases. A non-blocking post-release check confirms the ticket, keeping the "notarized" claim honest |
| Release trigger | Push (merged promotion PR) to a protected `release` branch | The promotion PR is the human release act and carries the checklist attestation |
| Versioning | `VERSION` file; if ahead of latest `v*` tag it wins, else latest tag + 1 patch | "If I haven't manually incremented it, do it for me." The workflow never writes the file back, so automation never commits to protected branches |
| Verify-before-publish | Draft-gate: goreleaser publishes to a **draft** GitHub release; the workflow smoke-tests the exact built bytes, then undrafts | goreleaser OSS has no split build/publish (`goreleaser publish`/`continue` are Pro-only). The draft-gate reproduces it: nothing is public until the artifacts that will ship have been executed |
| Build tool | goreleaser OSS v2.x for builds, archives, checksums, signing/notarization, changelog, and release upload — **not** formula publishing | Those parts it does uncontroversially well; the formula step is ~40 lines we own and test |
| Platforms | darwin/arm64 + darwin/amd64; no Linux | Intel is still ~27% of macOS brew installs and costs one matrix cell. Review points: **Sept 2026** (homebrew-core stops building Intel bottles — the risk shifts to the tmux *dependency*, not our binary) and **Sept 2027** (Homebrew drops Intel entirely) |
| Runner | Pinned `macos-26` (arm64), not `macos-latest` | The `-latest` label rolled to macOS 26 in July 2026 and rolls again ~yearly; pinning stops the smoke-test environment drifting silently. Rosetta 2 is on the arm64 image (verified in the image build scripts), so the amd64 binary is executable too; the workflow gates that step on `pgrep oahd` |
| Provenance | Sigstore via `actions/attest@v4` with `subject-checksums: dist/checksums.txt`, plus a second step attesting `checksums.txt` itself by path | `attest-build-provenance` is now a thin wrapper over `actions/attest`; new implementations are told to use `attest` directly. Verified by users with `gh attestation verify <file> --repo mnbf9rca/agentctl`. Brew does not check attestations; the formula's pinned SHA256s are the install-time tamper evidence |
| Cross-repo credential | Single-purpose GitHub App installed on `homebrew-tap` only; workflow mints 1-hour installation tokens via `actions/create-github-app-token@v3` (`client-id` input — `app-id` is deprecated; scoped with `permission-contents: write`) | Honest rationale — **not** PAT expiry (fine-grained PATs on personal accounts can be non-expiring): the minted token lives 1 hour and is auto-revoked post-job, the App key can rotate without touching consuming config, the App is an independently revocable identity, and installing it on future repos extends it to anything else published later |
| Tap commit mechanism | GraphQL `createCommitOnBranch` with the App token | Documented by GitHub as automatically signed — the tap history stays Verified, which no surveyed off-the-shelf tool achieves (§3.6) |
| Branch protection | `release` mirrors `main` (PRs + required checks); **signed commits required on both**; **squash-merge only** on `release` | GitHub-performed squash commits are web-flow-signed; rebase-merge re-creates commits *unsigned* and fails the rule; merge commits pass only if every head commit is already signed. Dependabot commits are GitHub-signed, so bot PRs still merge |
| Tag rules | Tag ruleset: restrict deletion + block non-fast-forward on `v*`; **creation left open**; release job declares `permissions: contents: write` | Creation must stay open because `GITHUB_TOKEN` cannot be a ruleset bypass actor; the repo's default workflow permission is read-only, so the job must request write explicitly |
| License | MIT `LICENSE` file in the agentctl repo before first release; same in tap | Formula carries `license "MIT"`; goreleaser includes the file in tarballs |
| CodeQL | Enable now (PRs to main + weekly schedule) | Free for public repos; near-zero noise at this size |
| Dependabot | Config now for `gomod` + `github-actions`; auto-merge deferred | go.mod has zero external deps; the Actions pins are the real dependencies. Merges to main never release — decoupled by construction |
| Release checklist | Rewritten as a prescriptive operator runbook (§8) before the first brew release | The checklist becomes the runbook for a repeating pipeline; must be executable without interpretation |

## 3. Verified external contracts (2026-08-02)

Verified by parallel research agents against current official documentation, source
code, and — where marked **[observed]** — local execution on macOS 26.6 (arm64). These
are the facts the pipeline hard-codes; re-verify before changing the pinned versions.

### 3.1 goreleaser (v2.17.1)

- `brews:` hard-deprecated since v2.16: still functions (removal only at v3) but warns
  and makes `goreleaser check` exit 2. `homebrew_casks:` generates a cask into `Casks/`.
- No OSS split publish: `goreleaser publish`/`continue`/`before_publish` hooks are
  Pro-only. OSS subcommands: `build`, `release`, `check`, `healthcheck`, `init`, `man`,
  `schema`.
- PR/CI snapshot invocation: `goreleaser release --snapshot --clean` (snapshot implies
  `--skip=announce,publish,validate`). Config lint: `goreleaser check` (exit 2 on
  deprecated keys — our config must stay deprecation-free for CI to gate on it).
- Re-runs against an existing tag fail on asset upload (HTTP 422) unless
  `release.replace_existing_artifacts: true`; set it.
- `release.mode: keep-existing` is the notes default; changelog generation (`changelog:`)
  is built in; `--draft` / `release.draft: true` publishes a draft release.
- ldflags replace the default entirely — re-add `-s -w`. `{{.Version}}` strips the `v`
  prefix; `{{.Tag}}` keeps it. `archives[].formats` (plural); `checksum.name_template:
  "checksums.txt"` (default is versioned and unpredictable).
- `notarize.macos` is OSS (only `notarize.macos_native` is Pro). It signs **binaries**
  (not archives) in place *before* the archive pipe — tarballs contain signed binaries
  and checksums are computed after. Runs on any OS (bundled `goreleaser/quill` fork of
  anchore/quill; no Xcode, no `codesign` binary). Sign-only (no notary keys) is valid.
  With `wait: true` a notary timeout **fails the release**; with `wait: false` the
  submission proceeds asynchronously. Hardened runtime + secure timestamp are set
  automatically (both mandatory for notarization).

### 3.2 GitHub Actions / attestation / App tokens

- `actions/attest@v4` supersedes `attest-build-provenance` (now a wrapper). Inputs used:
  `subject-checksums: dist/checksums.txt` (subjects = every file listed; the checksums
  file itself is **not** a subject — attest it by `subject-path` in a second step).
  Permissions: `id-token: write`, `attestations: write` (+ our `contents: write`).
  Always pass an explicit subject input (since v4.2.0 an input-less call silently
  auto-discovers). Verification matches by content digest only, so upload order and
  renames are irrelevant. User command: `gh attestation verify <file> --repo
  mnbf9rca/agentctl`.
- `actions/create-github-app-token@v3`: `client-id` (the `app-id` input is deprecated),
  `private-key`, `owner`, `repositories`, optional `permission-contents: write`
  down-scoping. Outputs `token` (1-hour, auto-revoked post-job). Installation tokens are
  now variable-length `ghs_…` strings — never regex/length-validate them.
- `GITHUB_TOKEN` cannot write to other repos; no OIDC path mints GitHub repo-write
  tokens. App-token or PAT are the only options; App chosen (§2).
- Events triggered by `GITHUB_TOKEN` do not start workflow runs (tag pushes, release
  creation). Our single-run workflow is immune by construction; this is why the design
  must never split into a tag-triggered second workflow.
- `macos-26` (arm64, 3-core M1): Homebrew preinstalled; **Go is in the tool cache but
  not on PATH** — use `actions/setup-go` with `go-version-file: go.mod`. Rosetta 2 is
  provisioned and acceptance-tested (`pgrep oahd`) but undocumented — gate on it.
  Standard runners are free on public repos; `*-large`/`*-intel`/`*-xlarge` labels are
  billed even on public repos — do not use them.
- Squash merges are web-flow-signed; rebase merges are re-created unsigned; merge
  commits require every head commit signed. `createCommitOnBranch` commits are
  documented as auto-signed; REST contents-API commits are signed **only** when no
  custom author/committer is supplied. Legacy tag protection is gone (rulesets only);
  `GITHUB_TOKEN` cannot be a ruleset bypass actor.

### 3.3 Apple signing and notarization

- Notary service accepts zip-wrapped bare Mach-O submissions; the quill path zips
  automatically. **Stapling a bare binary is impossible** (Apple, explicitly), so
  Gatekeeper's first-run check on a *quarantined* copy requires an online ticket
  lookup; offline first-run fails closed and the negative result is cached until
  re-download.
- **[observed]** Under quarantine, unsigned, ad-hoc-signed, and Apple-Development-signed
  binaries (even with hardened runtime + timestamp) are all SIGKILLed. A production
  Developer-ID-signed + notarized + unstapled + quarantined `binary`-stanza cask binary
  (1password-cli) executes cleanly. An "Apple Development" certificate does not satisfy
  Gatekeeper for distribution — only "Developer ID Application" does; the maintainer's
  keychain currently holds only the former, so the cert must be created (Account Holder
  role required).
- Developer ID Application certs last 5 years (up to five concurrent; expiry breaks new
  releases, never shipped ones — the secure timestamp pins validity). Notary API needs
  an App Store Connect **Team** key (Developer role). Apple's target: 98% of
  submissions within 15 min; outages happen and have no SLA — hence `wait: false`.
- Finder double-click of a bare CLI tool fails Gatekeeper assessment regardless of
  signing (long-standing macOS bug). Documentation must only ever show terminal usage.

### 3.4 Homebrew 6.0 tap trust

Non-official taps require explicit trust before their code runs (Homebrew ≥ 6.0.0,
June 2026). Fully-qualified `brew install mnbf9rca/tap/agentctl` implicitly trusts
exactly that formula and needs no extra step. The two-step form now requires
`brew trust --formula mnbf9rca/tap/agentctl` between `brew tap` and `brew install`.
`HOMEBREW_NO_REQUIRE_TAP_TRUST=1` is a discouraged, to-be-removed escape hatch — CI
uses the fully-qualified form instead.

### 3.5 Homebrew quarantine boundary

Quarantine lives exclusively in Homebrew's cask code path (`Quarantine.cask!` +
propagation over extracted files); formula downloads have no quarantine call site.
**[observed]** cask-installed binaries carry `com.apple.quarantine: …;Homebrew Cask;…`.
Formula installs never trip Gatekeeper regardless of signing. One formula-specific
hazard: Homebrew ad-hoc **re-signs** Mach-O files it relocates, which would destroy the
Developer ID signature — a static CGO-free Go binary with no placeholder paths is not
relocated, so ours is untouched; do not introduce either.

### 3.6 Ecosystem survey (why hand-rolled)

charmbracelet, dagger, k9s still ship goreleaser `brews:` formulas today; HashiCorp
(~35 formulas) and Bun run bespoke render-and-push pipelines; goreleaser's own tap is
the notable cask migration. No maintained tool satisfies formula + per-arch sha256 +
`depends_on` + caveats + license + `test do`: axo `dist` emits no caveats/test block
(and its non-Rust path is second-class), `homebrew-releaser` (quiet since Jan 2026)
lacks caveats and scrapes desc/homepage, the bump actions cannot express multi-arch.
`tap_migrations.json` is the documented escape hatch if the formula ever becomes a
cask. Binary formulas in third-party taps violate no Homebrew policy (the cask rule is
scoped to homebrew-core).

## 4. Architecture

```
main (dev, protected, signed commits)
  │  promotion PR (squash)  ←— human release act; checklist attestation in PR body
  ▼
release (protected, signed commits, squash-only, no direct pushes)
  │  push triggers release.yml (macos-26)
  ▼
tag vX.Y.Z ──► goreleaser: build (CGO off, both darwin archs)
                 ├─ sign + notarize binaries (wait: false)
                 ├─ tarballs + checksums.txt + changelog
                 └─ upload to DRAFT GitHub Release
                      │
        smoke-test the exact dist/ bytes (arm64 native, amd64 via Rosetta)
                      │  pass
                      ▼
        undraft (gh release edit --draft=false)   ←— the publish moment
                      │
        attest (actions/attest@v4) ──► Sigstore transparency log
                      │
        render Formula/agentctl.rb ──► createCommitOnBranch ──► mnbf9rca/homebrew-tap
                      │
        verify job: brew install mnbf9rca/tap/agentctl && brew test
```

Components, one job each:

- **`VERSION`** (repo root): the intended next version, edited by a human only, and
  only to jump minor/major. Silence means patch.
- **`hack/next-version.sh`**: version resolution (VERSION file + tag list in → version
  out), unit-tested. The workflow calls the tested script; the YAML stays dumb.
- **`hack/render-formula.sh`** + **`hack/formula.rb.tmpl`**: renders the formula from
  the version and `checksums.txt`; unit-tested against golden output.
- **`.github/workflows/release.yml`**: the only place a tag is ever created (§5).
- **`.goreleaser.yaml`**: builds (ldflags `-s -w -X
  github.com/mnbf9rca/agentctl/internal/buildinfo.Stamp={{.Version}}`), archives
  (`formats: [tar.gz]`), `checksum.name_template: "checksums.txt"`, `release.draft:
  true`, `release.replace_existing_artifacts: true`, changelog, `notarize.macos` with
  `wait: false`.
- **`mnbf9rca/homebrew-tap`**: `Formula/agentctl.rb` + README + MIT license. Written
  only by the release workflow via the GitHub App; humans never edit the formula.

Day-to-day development is unchanged: same main protection, same PR flow, same CI.

## 5. Release workflow

Trigger: push to `release`. Runner: `macos-26`. Single workflow, single run — never a
tag-triggered second workflow (§3.2 event suppression). Job permissions: `contents:
write`, `id-token: write`, `attestations: write`. Steps:

1. **Resolve version** via `hack/next-version.sh`: if `VERSION` > latest `v*` tag, use
   `VERSION`; else latest tag + 1 patch. A `VERSION` at or below the latest tag is
   ignored — no downgrade release is representable.
2. **Create and push the annotated tag** `vX.Y.Z` (goreleaser requires it). From here a
   failure leaves a visible tag and at most an invisible draft — no release, no
   installable artifact, no formula change.
3. **goreleaser release --clean**: builds both darwin archs (`CGO_ENABLED=0`), signs and
   submits the binaries for notarization (`wait: false`), archives, checksums,
   changelog, and uploads everything to a **draft** GitHub Release.
4. **Smoke-test the exact artifacts** in `dist/`: run the arm64 binary natively and the
   amd64 binary under Rosetta (step gated on `pgrep oahd`; if Rosetta is ever absent
   the step fails closed rather than shipping untested bytes). Each must print exactly
   the tag version from `agentctl version`. (§1.1: the version claim is verified on the
   bytes being shipped, not on a rebuild.)
5. **Undraft**: `gh release edit vX.Y.Z --draft=false` — the publish moment.
6. **Attest**: `actions/attest@v4` with `subject-checksums: dist/checksums.txt`, then a
   second step with `subject-path: dist/checksums.txt`.
7. **Render and push the formula**: `hack/render-formula.sh` produces
   `Formula/agentctl.rb`; the workflow commits it to `mnbf9rca/homebrew-tap` via
   GraphQL `createCommitOnBranch` using a fresh App installation token — a Verified,
   GitHub-signed commit.
8. **Post-publish verification** (separate job, clean `macos-26` runner,
   `HOMEBREW_NO_AUTO_UPDATE=1`): `brew install mnbf9rca/tap/agentctl && brew test
   mnbf9rca/tap/agentctl`, asserting the installed binary prints the tag. The release
   is not reported green until what a user would run has actually worked.
9. **Notarization confirmation** (non-blocking follow-up step): query the submission
   status; a rejection or still-pending state is surfaced as a workflow annotation and
   issue, not a retraction — the release shipped on the strength of steps 4 and 8, and
   the notarization claim is only made once Apple accepts.

`workflow_dispatch` with a `dry_run` input runs `goreleaser release --snapshot --clean
--skip=notarize` plus the smoke tests: full rehearsal, no tag, no Apple submission,
nothing published.

## 6. Formula and tap

- Binary formula, rendered from `hack/formula.rb.tmpl`: `version` declared before the
  URL blocks, `on_macos` + `on_arm`/`on_intel` blocks with per-arch release-tarball
  `url` + pinned `sha256` from `checksums.txt`; `def install` is `bin.install
  "agentctl"`.
- `depends_on "tmux"` — the one hard runtime dependency. Deliberately **not** declared:
  amq, Claude Code, codex. They are runtime-optional by design; preflight reports their
  absence honestly at the moment of use.
- `caveats`: agentctl launches agents via `amq coop exec` and expects `amq` plus at
  least one harness (`claude`, `codex`) on PATH at launch time; agentctl reports what
  is missing when run. Factual, not aspirational.
- `test do`: `assert_match version.to_s, shell_output("#{bin}/agentctl version")` —
  `brew test` re-verifies the version claim on the installed binary. (This block is the
  headline reason the packaging is a formula: casks cannot have one.)
- `license "MIT"`.
- Tap repo: README documents only the fully-qualified install command and the
  `gh attestation verify` one-liner; MIT license; no CI of its own in phase 2.
- The README never suggests opening the binary from Finder (§3.3) and never documents
  the `brew tap` two-step without its now-required `brew trust`.

## 7. Checklist gating of promotion

The promotion PR (main → release) is the only new process surface:

- A promotion PR template with two mutually exclusive checkboxes:
  - "This release changes tmux targeting, harness startup, or injected delivery — the
    release checklist was run; results recorded in `docs/release-verification-notes.md`."
  - "No changes in checklist-covered areas since the last release — checklist not
    required."
- Checklist results continue to live in the results-history section of
  `docs/release-verification-notes.md`, merged to main **before** promotion, so the
  release tarball contains the evidence it shipped under.
- Deliberately not built: automation that detects whether the checklist "should" run.
  That judgment is exactly what the checklist's own preamble says automation
  structurally cannot make.
- The gate is a recorded attestation, not a technical barrier — consistent with how the
  checklist binds today; the PR body carries the claim permanently.

## 8. Release checklist rewrite

Everything mechanical about release verification is automated in
`hack/release-verify.sh`: preflight (tool checks, `make build`, version capture), the
four `hack/probe-*.sh` contract probes, cleanup checks, and results rendering
(`--render-results`, tested with measure and live golden fixtures). Its default path
launches a real fleet with the just-built release candidate, then runs
`./bin/agentctl clear` for both harnesses and `./bin/agentctl compact` for one while
the human watches the attached TUIs and attests the outcomes. The forensic
`hack/verify-injection.sh` snapshot rig is retained only by `--measure` for payload
delay experiments. Both paths append their rendered evidence to
`docs/release-verification-notes.md`.

`docs/release-checklist.md` is reduced to a checkbox runbook holding only what a human
must judge: the live attach, junk readiness, watched clear/reset outcomes for claude
and codex, and the compact spot check. The wrapper runs launch, clear, compact, kill,
and teardown assertions itself, always through `./bin/agentctl`; prompts have no
defaults and fail closed on rejection. Rationale and the results history live in
`docs/release-verification-notes.md`.

"No rigor lost" now means, precisely: every command the old checklist had a human type
still runs, by machine, in the same order; every human-observable delivery outcome has
a corresponding checkbox; and clean-checkout plus teardown are machine-enforced. In
live mode, pane-process identity is checked more strongly by the product's own
fail-closed validation chain on every clear/compact delivery: exact
`@agentctl_process` match, literal payload, then Enter. One thing remains a recorded
weakening, not a silent one: the old checklist's step-by-step "read each probe's output
against its written description" review is replaced by scripted marker-plus-exit-code
assertions per probe, which check less of the probe output than a full human read did.

## 9. Failure modes

| Failure | Observable state | Remediation |
|---|---|---|
| Build, signing, or draft upload fails (step 3) | Tag visible; draft release partial or absent; no installable artifact public | Fix on main, re-promote (the burned patch number is the honest record that an attempt failed); or admin-delete tag + draft to reuse the number |
| Smoke test fails (step 4) | Tag visible + complete draft; no installable artifact public | Same as above — fail-closed |
| Undraft fails (step 5) | Transient API failure; draft persists | Re-run the job; `replace_existing_artifacts: true` makes goreleaser re-runs survivable |
| Attest or formula push fails (steps 6–7) | Release public; formula stale or attestation missing; workflow red at the failing step | Fix (App installation, key secret) and re-run from the failed step. Existing installs are stale, never broken |
| Apple notary outage | Cannot block a release (`wait: false`); step 9 reports pending/failed | If ultimately rejected: investigate, fix, patch release; the binary remains signed either way |
| Post-publish verification fails (step 8) | Release + formula public but workflow red | Investigate; fix forward with a patch release |
| Accidental re-promotion with no changes | A pointless-but-harmless patch release; release notes visibly contain zero commits | Accepted residual |
| `VERSION` edited below the latest tag | Ignored by resolution; no downgrade possible | None needed |
| Rosetta absent on a future `macos-26` image | amd64 smoke test fails closed; nothing ships | Decide deliberately (explicit Intel runner is billed; or drop Intel with a tested claim behind it) |

## 10. Testing the pipeline

- `hack/next-version.sh` and `hack/render-formula.sh` have unit tests in the ordinary
  suite (render tested against golden formula output).
- The existing macOS integration CI job gains `goreleaser check` (must exit 0 — our
  config uses no deprecated keys, so exit 2 is a regression signal) and `goreleaser
  release --snapshot --clean --skip=notarize`: every PR proves the release build
  matrix, archiving, and version stamping still work.
- The `dry_run` dispatch mode (§5) rehearses the real workflow minus tag, Apple, and
  publication.
- Post-publish verification (§5 step 8) closes the loop on the published chain.
- Deliberately not tested by machine: the checklist runbook (structurally human); the
  tap repo has no CI.

## 11. Work items (prerequisites before first release)

1. MIT `LICENSE` file in the agentctl repo.
2. `VERSION` file + `hack/next-version.sh` + tests.
3. `.goreleaser.yaml` (per §4) + CI `goreleaser check`/snapshot step.
4. `hack/formula.rb.tmpl` + `hack/render-formula.sh` + golden tests.
5. `release` branch + protection (PRs, required checks, signed commits on both
   branches, squash-only merges on `release`) + tag ruleset (restrict deletion, block
   non-fast-forward on `v*`, creation open).
6. GitHub App created, installed on `homebrew-tap` only; client ID as a repo variable,
   private key as a secret.
7. **Maintainer, at Apple** (Account Holder role): create a Developer ID Application
   certificate, export as `.p12`; create an App Store Connect **Team** API key.
   Three repo secrets (`MACOS_SIGN_P12`, `MACOS_SIGN_PASSWORD`, `MACOS_NOTARY_KEY`)
   and two repo variables for the non-credential identifiers (`MACOS_NOTARY_KEY_ID`,
   `MACOS_NOTARY_ISSUER_ID`). **Done 2026-08-02** — all five verified present, and
   the Developer ID identity confirmed in the keychain.
8. `mnbf9rca/homebrew-tap` repo (README per §6, MIT license, empty `Formula/`).
9. `release.yml` with dry-run mode; rehearse via dry run.
10. Promotion PR template (§7).
11. Release checklist rewrite (§8).
12. CodeQL workflow; dependabot config (`gomod` + `github-actions`).
13. README install section: fully-qualified `brew install mnbf9rca/tap/agentctl`,
    caveats, `gh attestation verify` one-liner, terminal-only usage.

Sequencing: items 1–4 are ordinary PRs to main; 5–8 are repo/settings/Apple acts by the
maintainer; 9 lands only after 5–8 exist; the first real release happens after 11's
runbook is merged and a dry run has passed. Review points: Intel (Sept 2026 / Sept
2027, §2); Rosetta availability when the pinned runner image next changes.
