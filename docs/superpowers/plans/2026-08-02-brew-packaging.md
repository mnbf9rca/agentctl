# Brew Packaging Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship agentctl as a signed, notarized, prebuilt-binary Homebrew formula in `mnbf9rca/homebrew-tap`, released by a fail-closed GitHub Actions pipeline triggered by promoting `main` to `release`.

**Architecture:** goreleaser OSS builds/signs/notarizes both darwin archs and uploads to a *draft* GitHub release; the workflow smoke-tests the exact built bytes, undrafts, attests via Sigstore, then renders `Formula/agentctl.rb` from an in-repo template and commits it to the tap via a GitHub App token (`createCommitOnBranch`, Verified commits). Spec: `docs/superpowers/specs/2026-08-02-brew-packaging-design.md` — its §3 records the verified external contracts every task below hard-codes.

**Tech Stack:** Go 1.26, bash, goreleaser v2.x, GitHub Actions (`macos-26`), Homebrew formula DSL.

**Process:** Implementer subagents per task, in-session. Independent review between tasks: an agentctl-launched fleet runs one reviewer agent; review requests go over amq, and the orchestrator runs `agentctl clear --session brew --role reviewer` between reviews so each review is fresh-eyes (dogfooding the tool being shipped). Fallback if the fleet is down: a fresh in-session review subagent per task. Maintainer merges every PR; the orchestrator never merges or pushes main.

## Global Constraints

- Module path: `github.com/mnbf9rca/agentctl`; Go `1.26` (from `go.mod`).
- Version stamping variable: `github.com/mnbf9rca/agentctl/internal/buildinfo.Stamp` — stamp **`{{.Tag}}`** (v-prefixed, e.g. `v0.1.0`) to match `make build`'s `git describe` output.
- Tarball name: `agentctl_<version-no-v>_darwin_<arch>.tar.gz`; checksums file name: exactly `checksums.txt`.
- Runner for all release/verify jobs: **`macos-26`** (never `macos-latest`, never `*-large`/`*-intel`). Go is NOT on PATH on macOS runners — always `actions/setup-go@v7` with `go-version-file: go.mod`.
- Action pins: `actions/checkout@v7`, `actions/setup-go@v7`, `actions/attest@v4`, `actions/create-github-app-token@v3`.
- goreleaser config must contain **no deprecated keys** (`goreleaser check` must exit 0; it exits 2 on deprecations). Use `archives[].formats` (plural), `homebrew_casks`/`brews` NOT AT ALL, `checksum.name_template`.
- Install command documented anywhere: only the fully-qualified `brew install mnbf9rca/tap/agentctl` (Homebrew 6 tap trust).
- Shell scripts: `#!/usr/bin/env bash`, `set -euo pipefail`, no shell-interpreted interpolation of untrusted input.
- Script tests are Go tests in `package hack_test` under `hack/` (picked up by `go test ./...`), exec'ing the scripts against temp fixtures.
- Commits: imperative subject, PRs to main, squash-merged by the maintainer. If 1Password is locked (maintainer AFK), commit feature-branch work with `git -c commit.gpgsign=false commit …` — allowed; squash merges are GitHub-signed regardless.
- Worktrees: project-local `.worktrees/<branch>` (gitignored), removed after the PR opens.

---

### Task 1: MIT license

**Files:**
- Create: `LICENSE`

**Interfaces:**
- Produces: `LICENSE` at repo root — referenced by `.goreleaser.yaml` (Task 4, archive `files:`) and by the formula's `license "MIT"` (Task 3).

- [ ] **Step 1: Write the file** — standard MIT text, verbatim, with this copyright line:

```text
MIT License

Copyright (c) 2026 Robert Aleck

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

- [ ] **Step 2: Verify** — `head -3 LICENSE` shows the MIT header; `go test ./...` still passes (no code touched).
- [ ] **Step 3: Commit** — `git add LICENSE && git commit -m 'Add MIT license'`; open PR.

---

### Task 2: VERSION file and next-version script

**Files:**
- Create: `VERSION`, `hack/next-version.sh`, `hack/nextversion_test.go`

**Interfaces:**
- Produces: `hack/next-version.sh` — no arguments; prints the next release version **without** `v` prefix (e.g. `0.1.0`) to stdout; exit 1 with a message on malformed `VERSION`. Rule: if `VERSION` (semver) is greater than the latest `v*` tag, print `VERSION`; else print latest tag + 1 patch; if no tags exist, print `VERSION`. Consumed by `release.yml` (Task 8).
- Produces: `VERSION` containing `0.1.0` (the intended first release).

- [ ] **Step 1: Write the failing test** — `hack/nextversion_test.go`:

```go
package hack_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo creates a temp git repo containing the script, a VERSION file, and tags.
func initRepo(t *testing.T, version string, tags []string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	run("git", "init", "-q")
	run("git", "config", "commit.gpgsign", "false")
	run("git", "config", "tag.gpgsign", "false")
	script, err := os.ReadFile("next-version.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "hack"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hack", "next-version.sh"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte(version+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "-A")
	run("git", "commit", "-q", "-m", "init")
	for _, tag := range tags {
		run("git", "tag", tag)
	}
	return dir
}

func nextVersion(t *testing.T, dir string) (string, error) {
	t.Helper()
	cmd := exec.Command("./hack/next-version.sh")
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func TestNextVersion(t *testing.T) {
	cases := []struct {
		name    string
		version string
		tags    []string
		want    string
	}{
		{"no tags uses VERSION", "0.1.0", nil, "0.1.0"},
		{"VERSION ahead wins", "0.2.0", []string{"v0.1.0", "v0.1.1"}, "0.2.0"},
		{"VERSION equal bumps patch", "0.1.1", []string{"v0.1.0", "v0.1.1"}, "0.1.2"},
		{"VERSION behind bumps patch", "0.1.0", []string{"v0.1.0", "v0.1.4"}, "0.1.5"},
		{"sorts semver not lexically", "0.1.0", []string{"v0.1.9", "v0.1.10"}, "0.1.11"},
		{"ignores non-version tags", "0.1.0", []string{"v0.1.0", "vendor-snapshot"}, "0.1.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nextVersion(t, initRepo(t, tc.version, tc.tags))
			if err != nil {
				t.Fatalf("script failed: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNextVersionRejectsMalformed(t *testing.T) {
	if _, err := nextVersion(t, initRepo(t, "not-a-version", nil)); err == nil {
		t.Fatal("expected failure on malformed VERSION")
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./hack/ -run TestNextVersion -v`. Expected: FAIL (script missing).
- [ ] **Step 3: Write `VERSION`** — content: `0.1.0` (single line).
- [ ] **Step 4: Write `hack/next-version.sh`** (mode 0755):

```bash
#!/usr/bin/env bash
# Prints the next release version (no v prefix): VERSION file if it is ahead
# of the latest v* tag, otherwise latest tag + 1 patch. See spec §5 step 1.
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
file_version="$(tr -d '[:space:]' < "$root/VERSION")"
if ! [[ "$file_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "next-version: VERSION file is not X.Y.Z: '$file_version'" >&2
  exit 1
fi

latest="$(git tag -l 'v[0-9]*.[0-9]*.[0-9]*' | sed 's/^v//' | sort -V | tail -n1)"
if [[ -z "$latest" ]]; then
  echo "$file_version"
  exit 0
fi

highest="$(printf '%s\n%s\n' "$latest" "$file_version" | sort -V | tail -n1)"
if [[ "$highest" == "$file_version" && "$file_version" != "$latest" ]]; then
  echo "$file_version"
else
  IFS=. read -r major minor patch <<<"$latest"
  echo "${major}.${minor}.$((patch + 1))"
fi
```

- [ ] **Step 5: Run tests to verify they pass** — `go test ./hack/ -run TestNextVersion -v`. Expected: PASS (all cases).
- [ ] **Step 6: Full suite** — `go test ./...`. Expected: PASS.
- [ ] **Step 7: Commit** — `git add VERSION hack/next-version.sh hack/nextversion_test.go && git commit -m 'Add VERSION file and next-version resolution script'`; open PR.

---

### Task 3: Formula template and render script

**Files:**
- Create: `hack/formula.rb.tmpl`, `hack/render-formula.sh`, `hack/renderformula_test.go`, `hack/testdata/checksums.txt`, `hack/testdata/agentctl.rb.golden`

**Interfaces:**
- Consumes: tarball naming + `checksums.txt` format (global constraints).
- Produces: `hack/render-formula.sh VERSION CHECKSUMS_FILE` — prints the complete formula to stdout. `VERSION` is bare (`0.1.0`). Exits 1 if either darwin sha256 is missing or not exactly 64 hex chars. Consumed by `release.yml` (Task 8).

- [ ] **Step 1: Write the fixture** — `hack/testdata/checksums.txt`:

```text
1111111111111111111111111111111111111111111111111111111111111111  agentctl_0.1.0_darwin_amd64.tar.gz
2222222222222222222222222222222222222222222222222222222222222222  agentctl_0.1.0_darwin_arm64.tar.gz
```

- [ ] **Step 2: Write the golden file** — `hack/testdata/agentctl.rb.golden`:

```ruby
# Generated by agentctl's release workflow (hack/render-formula.sh). DO NOT EDIT.
class Agentctl < Formula
  desc "Personal tmux fleet launcher for AI coding agents"
  homepage "https://github.com/mnbf9rca/agentctl"
  version "0.1.0"
  license "MIT"

  depends_on "tmux"

  on_macos do
    on_arm do
      url "https://github.com/mnbf9rca/agentctl/releases/download/v#{version}/agentctl_#{version}_darwin_arm64.tar.gz"
      sha256 "2222222222222222222222222222222222222222222222222222222222222222"
    end
    on_intel do
      url "https://github.com/mnbf9rca/agentctl/releases/download/v#{version}/agentctl_#{version}_darwin_amd64.tar.gz"
      sha256 "1111111111111111111111111111111111111111111111111111111111111111"
    end
  end

  def install
    bin.install "agentctl"
  end

  def caveats
    <<~EOS
      agentctl launches agents via `amq coop exec` and expects `amq` plus at
      least one harness (`claude`, `codex`) on PATH at launch time. agentctl
      reports what is missing when you run it.
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/agentctl version")
  end
end
```

- [ ] **Step 3: Write the failing test** — `hack/renderformula_test.go`:

```go
package hack_test

import (
	"os"
	"os/exec"
	"testing"
)

func render(t *testing.T, version, checksums string) (string, error) {
	t.Helper()
	cmd := exec.Command("./render-formula.sh", version, checksums)
	out, err := cmd.Output()
	return string(out), err
}

func TestRenderFormulaMatchesGolden(t *testing.T) {
	got, err := render(t, "0.1.0", "testdata/checksums.txt")
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	golden, err := os.ReadFile("testdata/agentctl.rb.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(golden) {
		t.Fatalf("output does not match golden.\n--- got ---\n%s\n--- want ---\n%s", got, golden)
	}
}

func TestRenderFormulaRejects(t *testing.T) {
	cases := []struct {
		name      string
		version   string
		checksums string
	}{
		{"v-prefixed version", "v0.1.0", "testdata/checksums.txt"},
		{"missing checksums file", "0.1.0", "testdata/absent.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := render(t, tc.version, tc.checksums); err == nil {
				t.Fatal("expected failure")
			}
		})
	}
}

func TestRenderFormulaRejectsMissingArch(t *testing.T) {
	tmp := t.TempDir() + "/checksums.txt"
	only := "1111111111111111111111111111111111111111111111111111111111111111  agentctl_0.1.0_darwin_amd64.tar.gz\n"
	if err := os.WriteFile(tmp, []byte(only), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := render(t, "0.1.0", tmp); err == nil {
		t.Fatal("expected failure when an arch checksum is absent")
	}
}
```

- [ ] **Step 4: Run to verify it fails** — `go test ./hack/ -run TestRenderFormula -v`. Expected: FAIL.
- [ ] **Step 5: Write `hack/formula.rb.tmpl`** — identical to the golden file but with `__VERSION__`, `__SHA_ARM64__`, `__SHA_AMD64__` in place of `0.1.0` (the `version "…"` line only — the `url` lines keep `#{version}`) and the two shas.
- [ ] **Step 6: Write `hack/render-formula.sh`** (mode 0755):

```bash
#!/usr/bin/env bash
# Renders Formula/agentctl.rb from hack/formula.rb.tmpl.
# Usage: render-formula.sh VERSION CHECKSUMS_FILE   (VERSION bare, e.g. 0.1.0)
set -euo pipefail

version="${1:?usage: render-formula.sh VERSION CHECKSUMS_FILE}"
checksums="${2:?usage: render-formula.sh VERSION CHECKSUMS_FILE}"
tmpl="$(dirname "$0")/formula.rb.tmpl"

if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "render-formula: version must be bare X.Y.Z, got '$version'" >&2
  exit 1
fi

sha_for() {
  local arch="$1" sha
  sha="$(awk -v pat="_darwin_${arch}.tar.gz" '$2 ~ pat"$" {print $1}' "$checksums")"
  if ! [[ "$sha" =~ ^[0-9a-f]{64}$ ]]; then
    echo "render-formula: no single valid sha256 for darwin_${arch} in $checksums" >&2
    exit 1
  fi
  echo "$sha"
}

sha_arm64="$(sha_for arm64)"
sha_amd64="$(sha_for amd64)"

sed -e "s/__VERSION__/${version}/" \
    -e "s/__SHA_ARM64__/${sha_arm64}/" \
    -e "s/__SHA_AMD64__/${sha_amd64}/" \
    "$tmpl"
```

- [ ] **Step 7: Run tests to verify they pass** — `go test ./hack/ -v`. Expected: PASS (this task's and Task 2's).
- [ ] **Step 8: Sanity-lint the golden formula** (best-effort, requires brew): `brew ruby -e "load 'hack/testdata/agentctl.rb.golden'"` — expected: no syntax error. If `brew ruby` is unavailable, note that in the PR body.
- [ ] **Step 9: Commit** — `git add hack/ && git commit -m 'Add formula template and render script'`; open PR.

---

### Task 4: goreleaser config and CI snapshot gate

**Files:**
- Create: `.goreleaser.yaml`
- Modify: `.github/workflows/ci.yml` (integration job)

**Interfaces:**
- Consumes: `LICENSE` (Task 1).
- Produces: `.goreleaser.yaml` used verbatim by `release.yml` (Task 8); dist layout `dist/agentctl_darwin_<arch>*/agentctl` and `dist/checksums.txt`; tarballs named per global constraints.

- [ ] **Step 1: Write `.goreleaser.yaml`**:

```yaml
version: 2
project_name: agentctl

builds:
  - id: agentctl
    main: ./cmd/agentctl
    binary: agentctl
    env:
      - CGO_ENABLED=0
    goos: [darwin]
    goarch: [amd64, arm64]
    ldflags:
      - -s -w -X github.com/mnbf9rca/agentctl/internal/buildinfo.Stamp={{.Tag}}

archives:
  - id: default
    formats: [tar.gz]
    name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
    files:
      - LICENSE
      - README.md

checksum:
  name_template: "checksums.txt"

release:
  draft: true
  replace_existing_artifacts: true
  mode: keep-existing

changelog:
  use: github
  sort: asc

notarize:
  macos:
    - enabled: true
      ids: [agentctl]
      sign:
        certificate: "{{ .Env.MACOS_SIGN_P12 }}"
        password: "{{ .Env.MACOS_SIGN_PASSWORD }}"
      notarize:
        issuer_id: "{{ .Env.MACOS_NOTARY_ISSUER_ID }}"
        key_id: "{{ .Env.MACOS_NOTARY_KEY_ID }}"
        key: "{{ .Env.MACOS_NOTARY_KEY }}"
        wait: false
```

Notes for the implementer: `MACOS_SIGN_P12` and `MACOS_NOTARY_KEY` are base64 *contents* (goreleaser accepts path or base64). Every invocation that lacks these env vars MUST pass `--skip=notarize` (CI snapshot and dry-run do). `{{.Tag}}` (not `{{.Version}}`) is deliberate — global constraints.

- [ ] **Step 2: Verify config** — `goreleaser check` (install locally with `brew install goreleaser` if needed). Expected: exit 0, "1 configuration file(s) validated". Exit 2 means a deprecated key — fix, do not tolerate.
- [ ] **Step 3: Local snapshot proof** — `goreleaser release --snapshot --clean --skip=notarize` then `./dist/agentctl_darwin_arm64*/agentctl version`. Expected: build succeeds; binary prints a version string containing the snapshot tag.
- [ ] **Step 4: Extend CI** — in `.github/workflows/ci.yml`, change the integration job's `runs-on: macos-latest` to `runs-on: macos-26` (pin, matching the release pipeline; the tmux 3.7b assertion already guards toolchain drift loudly), and append these steps after "Run integration tests":

```yaml
      - name: Install goreleaser
        run: brew install goreleaser
      - name: Validate goreleaser config
        run: goreleaser check
      - name: Snapshot release build
        run: goreleaser release --snapshot --clean --skip=notarize
      - name: Smoke-test snapshot binary
        run: |
          out="$(./dist/agentctl_darwin_arm64*/agentctl version)"
          echo "snapshot binary reports: $out"
          test -n "$out"
```

- [ ] **Step 5: Run full suite** — `go test ./...`. Expected: PASS.
- [ ] **Step 6: Commit** — `git add .goreleaser.yaml .github/workflows/ci.yml && git commit -m 'Add goreleaser config and CI snapshot gate'`; open PR. Confirm the integration job passes on the PR before requesting review.

---

### Task 5: CodeQL and dependabot

**Files:**
- Create: `.github/workflows/codeql.yml`, `.github/dependabot.yml`

**Interfaces:** none consumed by other tasks.

- [ ] **Step 1: Write `.github/workflows/codeql.yml`**:

```yaml
name: CodeQL

on:
  push:
    branches: [main]
  pull_request:
    branches: [main]
  schedule:
    - cron: "26 7 * * 1"

permissions:
  contents: read

jobs:
  analyze:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      security-events: write
    steps:
      - name: Check out repository
        uses: actions/checkout@v7
      - name: Initialize CodeQL
        uses: github/codeql-action/init@v4
        with:
          languages: go
      - name: Autobuild
        uses: github/codeql-action/autobuild@v4
      - name: Analyze
        uses: github/codeql-action/analyze@v4
```

- [ ] **Step 2: Write `.github/dependabot.yml`**:

```yaml
version: 2
updates:
  - package-ecosystem: gomod
    directory: /
    schedule:
      interval: weekly
  - package-ecosystem: github-actions
    directory: /
    schedule:
      interval: weekly
```

- [ ] **Step 3: Verify** — `python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/codeql.yml')); yaml.safe_load(open('.github/dependabot.yml'))"` (or any YAML parse). Expected: no error.
- [ ] **Step 4: Commit** — `git add .github && git commit -m 'Add CodeQL scanning and dependabot config'`; open PR. After merge, confirm the CodeQL workflow's first run goes green on main.

---

### Task 6: Promotion PR template

**Files:**
- Create: `.github/PULL_REQUEST_TEMPLATE/release-promotion.md`

**Interfaces:**
- Produces: the template referenced by the release runbook (Task 9) via URL query `?template=release-promotion.md` on promotion PRs.

- [ ] **Step 1: Write the template**:

```markdown
## Release promotion: main → release

Merging this PR triggers the release workflow (tag, build, sign, publish,
formula update). Check exactly one box; the claim below ships with the release.

- [ ] **Checklist run.** This release changes tmux targeting, harness startup,
  or injected command delivery. The release verification checklist was run and
  the results are recorded in `docs/release-checklist.md` on main.
- [ ] **Checklist not required.** No changes in checklist-covered areas since
  the last release.

Version: <!-- output of hack/next-version.sh, e.g. 0.1.2 -->
```

- [ ] **Step 2: Commit** — `git add .github/PULL_REQUEST_TEMPLATE && git commit -m 'Add release-promotion PR template'`; open PR.

---

### Task 7: Repo, tap, App, and protection settings (maintainer + orchestrator)

No repository files. Exact commands, run from the repo root with the maintainer's authorization (orchestrator may execute with standing approval; the App creation is browser-only and strictly maintainer).

**Interfaces:**
- Produces: `release` branch; `mnbf9rca/homebrew-tap` repo; GitHub App with `TAP_APP_CLIENT_ID` variable + `TAP_APP_PRIVATE_KEY` secret — consumed by `release.yml` (Task 8).

- [ ] **Step 1: Create the release branch** (no new commit; from current main):

```bash
git fetch origin && git push origin origin/main:refs/heads/release
```

- [ ] **Step 2: Protect `release`** (PRs + same required check as main + signatures):

```bash
gh api -X PUT repos/mnbf9rca/agentctl/branches/release/protection \
  --input - <<'JSON'
{
  "required_status_checks": {"strict": true, "contexts": ["test"]},
  "enforce_admins": true,
  "required_pull_request_reviews": {"required_approving_review_count": 0},
  "restrictions": null,
  "required_linear_history": true,
  "allow_force_pushes": false,
  "allow_deletions": false
}
JSON
gh api -X POST repos/mnbf9rca/agentctl/branches/release/protection/required_signatures
```

- [ ] **Step 3: Require signatures on main too**:

```bash
gh api -X POST repos/mnbf9rca/agentctl/branches/main/protection/required_signatures
```

- [ ] **Step 4: Squash-only merges** (repo-level; rebase-merge would break the signature rule):

```bash
gh api -X PATCH repos/mnbf9rca/agentctl \
  -F allow_squash_merge=true -F allow_merge_commit=false -F allow_rebase_merge=false
```

- [ ] **Step 5: Tag ruleset** — protect `v*` tags from deletion/rewrite, creation open:

```bash
gh api -X POST repos/mnbf9rca/agentctl/rulesets --input - <<'JSON'
{
  "name": "protect-release-tags",
  "target": "tag",
  "enforcement": "active",
  "conditions": {"ref_name": {"include": ["refs/tags/v*"], "exclude": []}},
  "rules": [{"type": "deletion"}, {"type": "non_fast_forward"}]
}
JSON
```

- [ ] **Step 6: Create the tap repo**:

```bash
tmp=$(mktemp -d) && cd "$tmp"
git init -q -b main tap && cd tap
cp /Users/rob/git/agentctl/LICENSE LICENSE
mkdir Formula
cat > README.md <<'EOF'
# homebrew-tap

Homebrew formulas for [mnbf9rca](https://github.com/mnbf9rca)'s tools.
`Formula/*.rb` files are generated by each project's release workflow —
do not edit them by hand.

## agentctl

```sh
brew install mnbf9rca/tap/agentctl
```

Verify a downloaded release artifact's provenance:

```sh
gh attestation verify <file> --repo mnbf9rca/agentctl
```
EOF
git add -A && git commit -m 'Initialize tap'
gh repo create mnbf9rca/homebrew-tap --public --source . --push \
  --description "Homebrew tap for mnbf9rca's tools"
```

(`Formula/` is empty until the first release; git ignores empty dirs — that is fine, `createCommitOnBranch` creates the path on first push.)

- [ ] **Step 7 (maintainer, browser): Create the GitHub App** — github.com → Settings → Developer settings → GitHub Apps → New GitHub App: name `agentctl-tap-publisher`; homepage `https://github.com/mnbf9rca/agentctl`; **uncheck** Webhook → Active; Repository permissions: **Contents: Read and write** only; "Where can this app be installed": Only on this account. Create, then: **Generate a private key** (downloads a `.pem`), note the **Client ID**, and **Install App** → only select repositories → `homebrew-tap`.
- [ ] **Step 8: Store the App credentials**:

```bash
gh variable set TAP_APP_CLIENT_ID --repo mnbf9rca/agentctl   # paste Client ID
gh secret set TAP_APP_PRIVATE_KEY --repo mnbf9rca/agentctl < agentctl-tap-publisher.*.pem
rm agentctl-tap-publisher.*.pem
```

- [ ] **Step 9: Verify** — `gh api repos/mnbf9rca/agentctl/branches/release/protection --jq '.required_signatures.enabled'` prints `true`; `gh api repos/mnbf9rca/agentctl/rulesets --jq '.[].name'` includes `protect-release-tags`; `gh repo view mnbf9rca/homebrew-tap` succeeds; `gh variable list --repo mnbf9rca/agentctl` shows `TAP_APP_CLIENT_ID`; `gh secret list --repo mnbf9rca/agentctl` shows `TAP_APP_PRIVATE_KEY`.

---

### Task 8: Release workflow

**Files:**
- Create: `.github/workflows/release.yml`

**Interfaces:**
- Consumes: `hack/next-version.sh` (Task 2), `hack/render-formula.sh` (Task 3), `.goreleaser.yaml` (Task 4), App credentials (Task 7), signing secrets/variables (already set: `MACOS_SIGN_P12`, `MACOS_SIGN_PASSWORD`, `MACOS_NOTARY_KEY` secrets; `MACOS_NOTARY_KEY_ID`, `MACOS_NOTARY_ISSUER_ID` variables).

- [ ] **Step 1: Write `.github/workflows/release.yml`**:

```yaml
name: Release

on:
  push:
    branches: [release]
  workflow_dispatch:
    inputs:
      dry_run:
        description: "Rehearse: build + smoke test only; no tag, no publish, no Apple submission"
        type: boolean
        default: true

permissions:
  contents: write
  id-token: write
  attestations: write

env:
  DRY_RUN: ${{ github.event_name == 'workflow_dispatch' && inputs.dry_run }}

jobs:
  release:
    runs-on: macos-26
    outputs:
      version: ${{ steps.version.outputs.version }}
      dry_run: ${{ steps.version.outputs.dry_run }}
    steps:
      - name: Check out repository
        uses: actions/checkout@v7
        with:
          fetch-depth: 0
      - name: Set up Go
        uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
      - name: Install goreleaser
        run: brew install goreleaser
      - name: Resolve version
        id: version
        run: |
          v="$(./hack/next-version.sh)"
          echo "version=$v" >> "$GITHUB_OUTPUT"
          echo "dry_run=$DRY_RUN" >> "$GITHUB_OUTPUT"
          echo "resolved next version: $v (dry_run=$DRY_RUN)"
      - name: Create and push tag
        if: env.DRY_RUN != 'true'
        run: |
          git config user.name "github-actions[bot]"
          git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
          git tag -a "v${{ steps.version.outputs.version }}" -m "agentctl v${{ steps.version.outputs.version }}"
          git push origin "v${{ steps.version.outputs.version }}"
      - name: Build, sign, and upload draft release
        if: env.DRY_RUN != 'true'
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          MACOS_SIGN_P12: ${{ secrets.MACOS_SIGN_P12 }}
          MACOS_SIGN_PASSWORD: ${{ secrets.MACOS_SIGN_PASSWORD }}
          MACOS_NOTARY_KEY: ${{ secrets.MACOS_NOTARY_KEY }}
          MACOS_NOTARY_KEY_ID: ${{ vars.MACOS_NOTARY_KEY_ID }}
          MACOS_NOTARY_ISSUER_ID: ${{ vars.MACOS_NOTARY_ISSUER_ID }}
        run: goreleaser release --clean
      - name: Build snapshot (dry run)
        if: env.DRY_RUN == 'true'
        run: goreleaser release --snapshot --clean --skip=notarize
      - name: Smoke-test built artifacts
        run: |
          set -euo pipefail
          expected="agentctl v${{ steps.version.outputs.version }}"
          arm_out="$(./dist/agentctl_darwin_arm64*/agentctl version)"
          echo "arm64 reports: $arm_out"
          if [ "$DRY_RUN" != "true" ] && [ "$arm_out" != "$expected" ]; then
            echo "arm64 version mismatch: got '$arm_out', want '$expected'" >&2; exit 1
          fi
          if ! pgrep -q oahd; then
            echo "Rosetta 2 (oahd) not running on this runner; cannot verify amd64 binary" >&2
            exit 1
          fi
          amd_out="$(arch -x86_64 ./dist/agentctl_darwin_amd64*/agentctl version)"
          echo "amd64 reports: $amd_out"
          if [ "$DRY_RUN" != "true" ] && [ "$amd_out" != "$expected" ]; then
            echo "amd64 version mismatch: got '$amd_out', want '$expected'" >&2; exit 1
          fi
      - name: Publish (undraft) release
        if: env.DRY_RUN != 'true'
        env:
          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: gh release edit "v${{ steps.version.outputs.version }}" --draft=false
      - name: Attest release artifacts
        if: env.DRY_RUN != 'true'
        uses: actions/attest@v4
        with:
          subject-checksums: ./dist/checksums.txt
      - name: Attest checksums file
        if: env.DRY_RUN != 'true'
        uses: actions/attest@v4
        with:
          subject-path: ./dist/checksums.txt
      - name: Mint tap token
        if: env.DRY_RUN != 'true'
        id: tap-token
        uses: actions/create-github-app-token@v3
        with:
          client-id: ${{ vars.TAP_APP_CLIENT_ID }}
          private-key: ${{ secrets.TAP_APP_PRIVATE_KEY }}
          owner: mnbf9rca
          repositories: homebrew-tap
          permission-contents: write
      - name: Render and push formula
        if: env.DRY_RUN != 'true'
        env:
          GH_TOKEN: ${{ steps.tap-token.outputs.token }}
          VERSION: ${{ steps.version.outputs.version }}
        run: |
          set -euo pipefail
          bash hack/render-formula.sh "$VERSION" dist/checksums.txt > /tmp/agentctl.rb
          head_oid="$(gh api graphql \
            -f query='query($o:String!,$r:String!){repository(owner:$o,name:$r){defaultBranchRef{target{oid}}}}' \
            -f o=mnbf9rca -f r=homebrew-tap \
            --jq '.data.repository.defaultBranchRef.target.oid')"
          jq -n \
            --arg oid "$head_oid" \
            --arg msg "agentctl v${VERSION}" \
            --arg contents "$(base64 -i /tmp/agentctl.rb)" \
            '{query: "mutation($input:CreateCommitOnBranchInput!){createCommitOnBranch(input:$input){commit{oid}}}",
              variables: {input: {
                branch: {repositoryNameWithOwner: "mnbf9rca/homebrew-tap", branchName: "main"},
                expectedHeadOid: $oid,
                message: {headline: $msg},
                fileChanges: {additions: [{path: "Formula/agentctl.rb", contents: $contents}]}}}}' \
            | gh api graphql --input -
          echo "formula pushed for v${VERSION}"

  verify-install:
    needs: release
    if: needs.release.outputs.dry_run != 'true'
    runs-on: macos-26
    env:
      HOMEBREW_NO_AUTO_UPDATE: "1"
    steps:
      - name: Install from tap
        run: brew install mnbf9rca/tap/agentctl
      - name: brew test
        run: brew test mnbf9rca/tap/agentctl
      - name: Verify installed version
        run: |
          got="$(agentctl version)"
          want="agentctl v${{ needs.release.outputs.version }}"
          echo "installed binary reports: $got"
          [ "$got" = "$want" ] || { echo "installed version mismatch: got '$got', want '$want'" >&2; exit 1; }

  notary-check:
    needs: [release, verify-install]
    if: needs.release.outputs.dry_run != 'true'
    runs-on: macos-26
    steps:
      - name: Confirm notarization accepted
        env:
          KEY_B64: ${{ secrets.MACOS_NOTARY_KEY }}
          KEY_ID: ${{ vars.MACOS_NOTARY_KEY_ID }}
          ISSUER_ID: ${{ vars.MACOS_NOTARY_ISSUER_ID }}
        run: |
          set -euo pipefail
          key_file="$RUNNER_TEMP/notary.p8"
          printf '%s' "$KEY_B64" | base64 -d > "$key_file"
          # Two submissions expected (arm64 + amd64), newest first.
          hist="$(xcrun notarytool history --key "$key_file" --key-id "$KEY_ID" --issuer "$ISSUER_ID" --output-format json)"
          echo "$hist" | /usr/bin/python3 -c '
          import json,sys
          subs = json.load(sys.stdin).get("history", [])[:2]
          if not subs:
              print("::warning::no notary submissions found yet"); sys.exit(0)
          bad = [s for s in subs if s.get("status") == "Invalid"]
          pending = [s for s in subs if s.get("status") in ("In Progress", None)]
          for s in subs:
              print(f"{s.get(\"createdDate\")}: {s.get(\"name\")}: {s.get(\"status\")}")
          if bad:
              print("::error::notarization rejected"); sys.exit(1)
          if pending:
              print("::warning::notarization still pending; re-check manually"); sys.exit(0)
          print("notarization accepted")
          '
```

- [ ] **Step 2: Static verification** — `gh workflow view` is unavailable pre-merge; instead run `python3 -c "import yaml; yaml.safe_load(open('.github/workflows/release.yml'))"` and `bash -n` each embedded script block (copy to temp files). Expected: no errors.
- [ ] **Step 3: Commit** — `git add .github/workflows/release.yml && git commit -m 'Add release workflow (draft-gate, sign, attest, formula push)'`; open PR.
- [ ] **Step 4: After merge — dry-run rehearsal**: `gh workflow run release.yml -f dry_run=true`, then `gh run watch`. Expected: release job green through the smoke test; no tag created (`git ls-remote --tags origin | grep -c v` unchanged); verify-install and notary-check skipped.

---

### Task 9: Release checklist rewrite

**Files:**
- Modify: `docs/release-checklist.md`

**Interfaces:**
- Consumes: promotion PR template (Task 6) and release workflow (Task 8) — the runbook references both.

Requirements (spec §8): restructure the *procedure* into numbered, literally-executable steps; each step = one imperative + the exact command + where to run it + the expected observation + what to record. Preserve **verbatim**: the "Why this checklist exists" section, the attestation/evidence requirements, and the entire "Results history" section. The existing probe/verify commands are the source of truth — reorganize them, do not invent new ones.

- [ ] **Step 1: Restructure** into this shape (headings fixed; content drawn from the current document):

```markdown
# Release Verification Runbook

## Why this checklist exists          ← keep current section verbatim
## When you must run this             ← new, 3 sentences: which diffs require it,
                                        link to the promotion PR template checkbox
## Setup (10 minutes)                 ← preconditions as numbered imperatives:
   1. Open three iTerm windows side by side. Label them 1, 2, 3.
   2. Window 1: cd to the release-candidate checkout; run `make build`;
      record `./bin/agentctl version` output here: ______
   3. Window 1: record versions: `tmux -V`, `claude --version`, `codex --version`
   … (each current precondition becomes one numbered, boxed step)
## Part A: Contract probes (Window 1) ← the four probe scripts, one step each,
                                        with "Expected:" line and the pgrep
                                        cleanup assertion as its own step
## Part B: Injection verification (Windows 1–3)
   ← the verify-injection.sh flow, prescribed window by window:
     Window 1 runs the script; Window 2 runs `agentctl attach --session …`
     and stays attached for pane inspection; Window 3 is for the operator's
     snapshot review commands. Each attestation question becomes a numbered
     step with the exact thing to look at and the y/n to type.
## Part C: Loaded measurement (optional per release; required when timing changes)
   ← current measure flow as numbered steps, with the CPU-saturation warning
     as step 1 ("Save other work first…")
## Cleanup (Window 1)                 ← current cleanup commands + expected empty output
## Recording results                  ← how to append to Results history + the
                                        promotion PR checkbox to tick
## Results history                    ← keep current section verbatim, including 2026-08-01
```

- [ ] **Step 2: Verify no rigor lost** — diff the old and new documents; confirm every command, expected observation, and warning in the old procedure appears in the new one (a table in the PR body mapping old section → new step is the evidence).
- [ ] **Step 3: Commit** — `git add docs/release-checklist.md && git commit -m 'Restructure release checklist as a prescriptive runbook'`; open PR.

---

### Task 10: README install section

**Files:**
- Modify: `README.md` (add an Installation section near the top; leave existing content otherwise untouched)

**Interfaces:**
- Consumes: install command + verification command (global constraints, Task 8 outputs).

- [ ] **Step 1: Add the section**:

```markdown
## Installation

```sh
brew install mnbf9rca/tap/agentctl
```

This installs a prebuilt, signed binary and `tmux` alongside it. agentctl
also expects `amq` and at least one agent harness (`claude`, `codex`) on
PATH at launch time — it will tell you exactly what is missing when you run
it.

Release artifacts carry Sigstore build provenance. To verify a downloaded
tarball:

```sh
gh attestation verify agentctl_<version>_darwin_arm64.tar.gz --repo mnbf9rca/agentctl
```

To build from source instead: `make build` (requires Go), then
`make install`.
```

Constraints: do not document `brew tap` + short-name install (needs `brew trust` since Homebrew 6); do not suggest opening the binary from Finder anywhere.

- [ ] **Step 2: Verify** — README renders correctly (`gh markdown-preview` or visual check); no other section altered (`git diff --stat` shows only README.md).
- [ ] **Step 3: Commit** — `git add README.md && git commit -m 'Document brew installation'`; open PR.

---

### Task 11: First release

Maintainer-driven; the orchestrator assists. Prerequisites: Tasks 1–10 merged, Task 8's dry run green.

- [ ] **Step 1:** Run the release verification runbook (Task 9's rewritten document) against main — this is the first release, so the checklist-covered areas count as changed. Record results in `docs/release-checklist.md` via PR.
- [ ] **Step 2:** Open the promotion PR: `gh pr create --base release --head main --title 'Release v0.1.0' --body-file .github/PULL_REQUEST_TEMPLATE/release-promotion.md`, tick the "Checklist run" box, fill in the version line (`0.1.0`).
- [ ] **Step 3:** Maintainer merges (squash). Watch: `gh run watch` on the Release workflow. Expected: all three jobs green; tag `v0.1.0`; public release with two tarballs + `checksums.txt`; `Formula/agentctl.rb` appears in the tap with a Verified commit.
- [ ] **Step 4:** Human acceptance on a real machine: `brew install mnbf9rca/tap/agentctl && agentctl version` → `v0.1.0`; `brew test mnbf9rca/tap/agentctl` passes; `gh attestation verify` on a downloaded tarball passes.
- [ ] **Step 5:** Record the release in the runbook's Results history and close out the phase-2 tracking issue.
