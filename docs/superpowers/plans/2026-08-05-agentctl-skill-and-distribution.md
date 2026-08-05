# agentctl Skill and Distribution Implementation Plan

> **For agentic workers:** this plan is executed by fleet workers as three
> issue-scoped PRs behind the reviewer gate, per `CLAUDE.md`. Each PR is one
> worker's contract. Steps use checkbox (`- [ ]`) syntax for tracking. Read
> the governing spec first:
> `docs/superpowers/specs/2026-08-05-agentctl-skill-and-distribution-design.md`
> (called **the spec** below). Where this plan and the spec disagree, the spec
> wins; report the disagreement instead of improvising.

**Goal:** Ship the agent-facing `agentctl` skill, embed it in the binary, add
`agentctl skill install` / `agentctl skill status`, and land the three drift
gates.

**Architecture:** Skill source at `skills/agentctl/` (embedded via a small
`skills` package because `go:embed` cannot reference parent directories);
install/status logic in `internal/skillinstall`; CLI wiring in `cmd/agentctl`;
process gates in `hack/`.

**Tech Stack:** Go stdlib only. Shell scripts pass `shellcheck`.

## Global Constraints

- No third-party Go dependencies (CLAUDE.md).
- Every output is a factual claim (product spec §1.1); partial failures name
  every path written and not written.
- TDD: failing test first; red/green evidence in each PR.
- Exit codes per product spec §9; new subcommands use `exitUsage` (2),
  `exitUnsafe` (5), `exitUnclassified` (1) only as specified below.
- SKILL.md ≤150 lines (spec §3.1), enforced by the contract test.
- Commits SSH-signed; worktrees per `/using-git-worktrees`; PR bodies per
  CLAUDE.md; every PR carries milestone 0.3.0.

---

## PR 1 (#78): skill content + contract test

### Task 1: Skill source tree

**Files:**
- Create: `skills/agentctl/SKILL.md`
- Create: `skills/agentctl/references/status-states.md`
- Create: `skills/agentctl/references/exit-codes.md`

**Interfaces:**
- Produces: the skill tree later tasks embed and install verbatim.

- [ ] **Step 1: Write `skills/agentctl/SKILL.md` exactly as follows** (the
  contract test in Task 3 parses the fenced block and enforces the ≤150-line
  budget; keep every documented invocation inside a fenced block):

````markdown
---
name: agentctl
description: Use when operating an agentctl fleet from inside it — checking sibling agent status, clearing or compacting a role's context, or terminating a managed session. Read this before issuing any agentctl command.
compatibility: Requires the agentctl binary on PATH, run from inside an agentctl-managed tmux window.
metadata:
  version: "0.3.0"
---

# Driving agentctl

agentctl launches and controls tmux fleets of coding agents. You are one of
those agents. This skill covers the commands you may use and the rules the
binary cannot enforce for you.

## 1. Verify who and where you are first

Every agentctl-launched window carries identity in its environment:

- `AGENTCTL_ROLE` — your role name; `AGENTCTL_SESSION` — your fleet's
  session; `AGENTCTL_MANAGED=1` — this window is fleet-managed.
- `AM_ME` / `AM_SESSION` — your AMQ identity, set by `amq coop exec`.
- `TMUX_PANE` — your own pane ID; agentctl refuses a control command that
  resolves to it (an accident guard, not a permission boundary).

Check these before your first command. If `AM_SESSION` and `AGENTCTL_SESSION`
disagree, your environment is not what you assume: stop and report instead of
targeting anything. These variables are advisory identity fixed when your
process started — treat them as facts about launch, not proof of the present.

Session resolution order is `--session` > `AGENTCTL_SESSION` > the tmux
session you are in. So a bare command acts on **your own fleet**; name any
other fleet explicitly with `--session`. Exception: bare `status` always
lists every session (a `*` marks yours) — it never narrows to the ambient
session.

## 2. Commands you may use

```
agentctl status --json
agentctl status --session SESSION --json
agentctl clear --session SESSION ROLE
agentctl compact --session SESSION ROLE
agentctl kill --session SESSION
```

`launch` and `attach` are operator-only; do not issue them.

## 3. Read status as factual claims

Status is roster-driven: roles come from fleet metadata, not from whatever
windows exist. The states `ambiguous`, `unmanaged`, `missing`, `dead`,
`unexpected-process`, `running` are distinct claims with distinct meanings —
see [references/status-states.md](references/status-states.md). Never infer
liveness from anything else (pane text, AMQ traffic, silence). An exited
agent normally reports `missing`, not `dead`, because managed windows close
on exit.

## 4. Rules the binary does not enforce

- **Do not send `clear`/`compact` to a role that has not been released back
  to you.** Mid-task, it destroys work in progress.
- **Do not issue control commands while the fleet is saturating the host.**
  agentctl cannot detect saturation without inferring machine state, which
  its design forbids; the obligation is yours.
- **Delivery is not execution.** Exit 0 proves tmux accepted the keys, not
  that the agent's TUI ran the command. Verify by observing the role's
  subsequent behaviour (message ping or `status`), never by trusting exit 0.
- **The self-target guard is an accident guard.** It stops you wiping your
  own context by mistake; it is not a security boundary.

## 5. Context hygiene for clear and compact

- `clear` between unrelated tasks (default for build workers), after a PR is
  opened and handed off, between batches (all roles), and for any wedged or
  confused worker — instead of arguing with it.
- `compact` when continuity has value: the next task continues the same
  subsystem, context pressure mid-task, and a reviewer role mid-batch —
  never `clear` a reviewer while a PR batch is in flight; cross-PR
  consistency lives in its context. Clear the reviewer at batch end.
- Never send either mid review-fix loop or while the fleet saturates the
  host. Confirm the reset (ping or `status`) before dispatching the next
  task.
- Corollary: routine clearing is only safe because dispatch messages are
  self-contained. Keep them self-contained; the two rules come as a pair.

## 6. What agentctl deliberately cannot do

No arbitrary keystrokes or free-text payloads (the payload registry is
closed and argument-free), no reading or writing AMQ state, no attaching for
you, no machine-state inference. Do not ask.

## 7. Branch on exit codes, not prose

Exit codes are contracts — see
[references/exit-codes.md](references/exit-codes.md). Prose output may
change; the codes may not.
````

- [ ] **Step 2: Write `skills/agentctl/references/status-states.md`:**

````markdown
# status states

Evaluated in precedence order; first match wins (product spec §6.3).

| Order | State | The claim it makes | What it does not claim |
|---|---|---|---|
| 1 | `ambiguous` | More than one window bears this role's exact name. Control commands refuse the role (exit 4) until an operator repairs it with raw tmux. | Nothing about which window is "real". |
| 2 | `unmanaged` | The window no longer satisfies the managed contract: metadata mismatch or more than one pane. Not agentctl's to describe or control. | Not a statement that the agent is gone. |
| 3 | `missing` | The roster names this role but no exactly-matching window exists (or it has zero panes). The normal state of an **exited** agent. | Not proof it never ran. |
| 4 | `dead` | The window exists and its pane reports dead. Rare: managed windows close on exit. | — |
| 5 | `unexpected-process` | The pane's observed root executable differs from the launch baseline, the baseline is empty, or identity could not be verified for an alive pane. Identity unverifiable is not identity verified. The rendered process is the one **observed now**, not the expected one. | Not proof of compromise; often a wrapper or restart. |
| 6 | `running` | Window, pane, and process identity all match the launch baseline. | Not that the agent is responsive or idle. |

A session that is not agentctl-managed renders `"managed": false` with an
empty agent list, exit 0. A managed session with absent or malformed
metadata refuses with exit 3 rather than guessing.
````

- [ ] **Step 3: Write `skills/agentctl/references/exit-codes.md`:**

````markdown
# exit codes

| Code | Constant | Claim |
|---|---|---|
| 0 | `exitOK` | The command's stated effect was observed. For control commands: delivery, not execution. |
| 1 | `exitUnclassified` | Something failed that codes 2–8 do not describe. No contract semantics. |
| 2 | `exitUsage` | The invocation was invalid; nothing was attempted. |
| 3 | `exitSession` | The session could not be resolved, does not exist, is not managed, or carries an incompatible/malformed management marker. Also every `attach` refusal. |
| 4 | `exitRole` | The role is not in the roster, has no window, or resolves to more than one window. |
| 5 | `exitUnsafe` | Refused as unsafe: the resolved target is the caller's own pane, or an overwrite of files agentctl cannot prove it wrote. |
| 6 | `exitTmux` | A tmux (or `ps`) command actually ran and failed; the message carries the tool's own stderr. |
| 7 | `exitMissingExecutable` | A required executable was not found on PATH. |
| 8 | `exitLaunch` | What this invocation created was removed: `launch` rolled back the session, `relaunch` the window. |
````

- [ ] **Step 4: Commit** — `git add skills/ && git commit -m "Add agentctl agent-facing skill content"`

### Task 2: `skills` embed package

**Files:**
- Create: `skills/embed.go`
- Test: `skills/embed_test.go`

**Interfaces:**
- Produces: `package skills; var Tree embed.FS` rooted so
  `skills.Tree.ReadFile("agentctl/SKILL.md")` works; consumed by Task 3 and
  PR 2. Also `const Root = "agentctl"`.

- [ ] **Step 1: Failing test** in `skills/embed_test.go`:

```go
package skills

import "testing"

func TestTreeCarriesSkillAndReferences(t *testing.T) {
	for _, path := range []string{
		"agentctl/SKILL.md",
		"agentctl/references/status-states.md",
		"agentctl/references/exit-codes.md",
	} {
		content, err := Tree.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%q): %v", path, err)
		}
		if len(content) == 0 {
			t.Fatalf("ReadFile(%q): empty", path)
		}
	}
}
```

- [ ] **Step 2: Run** `go test ./skills/` — expect FAIL (no `Tree`).
- [ ] **Step 3: Implement** `skills/embed.go`:

```go
// Package skills carries the agent-facing skill tree that documents this
// binary, embedded so distribution can never drift from the release.
package skills

import "embed"

// Root is the skill directory name inside Tree.
const Root = "agentctl"

//go:embed agentctl
var Tree embed.FS
```

- [ ] **Step 4: Run** `go test ./skills/` — expect PASS. **Commit.**

### Task 3: Contract test

**Files:**
- Create: `cmd/agentctl/skill_contract_test.go`

**Interfaces:**
- Consumes: `skills.Tree`, `commandUsage`, `parseCommand`, the `exit*`
  constants, and `statuspkg` state names.

The test enforces spec §5.1. Mechanics that make it non-fragile: documented
invocations are exactly the lines beginning `agentctl ` inside fenced blocks;
flags are checked by running `parseCommand` with placeholder values and
rejecting only the flag package's "flag provided but not defined" error.

- [ ] **Step 1: Write the test** (all assertions in one file; each is its own
  `t.Run`):

```go
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/skills"
)

func skillLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := skills.Tree.ReadFile(path)
	if err != nil {
		t.Fatalf("embedded %s: %v", path, err)
	}
	var lines []string
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines
}

// fencedCommands returns every line beginning "agentctl " inside fenced blocks.
func fencedCommands(lines []string) []string {
	var out []string
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence && strings.HasPrefix(trimmed, "agentctl ") {
			out = append(out, trimmed)
		}
	}
	return out
}

func TestSkillBudget(t *testing.T) {
	if n := len(skillLines(t, "agentctl/SKILL.md")); n > 150 {
		t.Fatalf("SKILL.md is %d lines; budget is 150 (spec §3.1)", n)
	}
}

func TestSkillVersionParses(t *testing.T) {
	lines := skillLines(t, "agentctl/SKILL.md")
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "version:") {
			value := strings.Trim(strings.TrimPrefix(strings.TrimSpace(line), "version:"), ` "`)
			if value == "" {
				t.Fatal("metadata.version is empty")
			}
			return
		}
	}
	t.Fatal("SKILL.md carries no metadata.version line")
}

func TestDocumentedCommandsExist(t *testing.T) {
	for _, invocation := range fencedCommands(skillLines(t, "agentctl/SKILL.md")) {
		fields := strings.Fields(invocation)
		command := fields[1]
		if _, ok := commandUsage[command]; !ok {
			t.Errorf("skill documents %q; not in commandUsage", command)
			continue
		}
		var args []string
		for _, field := range fields[2:] {
			if flag, ok := strings.CutPrefix(field, "--"); ok {
				args = append(args, "--"+flag+"=x")
			}
		}
		args = append(args, "role")
		if _, err := parseCommand(command, args); err != nil &&
			strings.Contains(err.Error(), "not defined") {
			t.Errorf("skill documents %q; %v", invocation, err)
		}
	}
}

func TestExitCodeTableMatchesConstants(t *testing.T) {
	constants := map[string]int{
		"exitOK": exitOK, "exitUnclassified": exitUnclassified,
		"exitUsage": exitUsage, "exitSession": exitSession,
		"exitRole": exitRole, "exitUnsafe": exitUnsafe,
		"exitTmux": exitTmux, "exitMissingExecutable": exitMissingExecutable,
		"exitLaunch": exitLaunch,
	}
	documented := map[string]int{}
	for _, line := range skillLines(t, "agentctl/references/exit-codes.md") {
		cells := strings.Split(line, "|")
		if len(cells) < 4 || strings.TrimSpace(cells[1]) == "Code" {
			continue
		}
		name := strings.Trim(strings.TrimSpace(cells[2]), "`")
		var code int
		if _, err := fmt.Sscanf(strings.TrimSpace(cells[1]), "%d", &code); err != nil {
			continue
		}
		documented[name] = code
	}
	for name, code := range constants {
		got, ok := documented[name]
		if !ok {
			t.Errorf("exit constant %s undocumented in exit-codes.md", name)
		} else if got != code {
			t.Errorf("exit-codes.md says %s=%d; binary says %d", name, got, code)
		}
	}
	for name := range documented {
		if _, ok := constants[name]; !ok {
			t.Errorf("exit-codes.md documents %s; no such constant", name)
		}
	}
}

func TestStatusStatesMatch(t *testing.T) {
	// binaryStates must enumerate the states the status package can emit.
	// If internal/status exports no such list, add one there (exported
	// slice or constants) in this PR and assert against it here.
	binaryStates := []string{"ambiguous", "unmanaged", "missing", "dead", "unexpected-process", "running"}
	doc := strings.Join(skillLines(t, "agentctl/references/status-states.md"), "\n")
	for _, state := range binaryStates {
		if !strings.Contains(doc, "`"+state+"`") {
			t.Errorf("status-states.md missing state %q", state)
		}
	}
}
```

- [ ] **Step 2: Run** `go test ./cmd/agentctl/ -run 'Skill|Documented|ExitCode|StatusStates' -v`
  — expect PASS if Tasks 1–2 are correct; any FAIL is a doc/binary mismatch:
  fix the doc (or the test's mechanics if the doc is right), never weaken the
  assertion.
- [ ] **Step 3:** If `internal/status` does not already export its state
  names, add an exported `States` list there and replace the local
  `binaryStates` literal with it (both directions of the check then hold by
  construction on the binary side).
- [ ] **Step 4: Full gates:** `go test ./... && go vet ./...` — PASS. **Commit.**

### Task 4: PR 1 assembly

- [ ] Spec edit riding this PR (spec §4.1 wording): "embedded with `go:embed`
  from `cmd/agentctl`" → "embedded with `go:embed` via the `skills` package".
- [ ] Rebase onto current `main`; push; open PR "Closes #78", milestone
  0.3.0, red/green evidence in body; request reviewer gate with the PR's own
  `pull_request` run URL; detach the worktree.

---

## PR 2 (#80): install/status subcommands + launch notice + SECURITY.md

### Task 5: `internal/skillinstall` — manifest and hashing

**Files:**
- Create: `internal/skillinstall/manifest.go`
- Test: `internal/skillinstall/manifest_test.go`

**Interfaces:**
- Produces:

```go
package skillinstall

const ManifestName = ".agentctl-skill.json"

type Manifest struct {
	Version string            `json:"version"`
	Files   map[string]string `json:"files"` // rel path -> hex sha256
}

func BuildManifest(tree fs.FS, root, version string) (Manifest, error)
func ReadManifest(dir string) (Manifest, bool, error) // ok=false: absent
func WriteManifest(dir string, m Manifest) error       // 0644, atomic rename
```

- [ ] **Step 1: Failing test** — `BuildManifest` over `skills.Tree` returns
  one entry per embedded file with correct sha256 (compute one expected hash
  in the test via `crypto/sha256` over `skills.Tree.ReadFile`); `ReadManifest`
  on an empty temp dir returns `ok=false, err=nil`; Write→Read round-trips.
- [ ] **Step 2:** Run — FAIL. **Step 3:** Implement with `fs.WalkDir`,
  `crypto/sha256`, `encoding/json`, `os.CreateTemp`+`os.Rename`.
- [ ] **Step 4:** Run — PASS. **Commit.**

### Task 6: `internal/skillinstall` — Install

**Files:**
- Create: `internal/skillinstall/install.go`
- Test: `internal/skillinstall/install_test.go`

**Interfaces:**
- Produces:

```go
type Target struct{ Harness, Dir string } // e.g. {"claude", "$HOME/.claude/skills/agentctl"}

func Targets(home string) []Target // claude then agents order, fixed

type Outcome struct {
	Target  Target
	Action  string   // "installed", "current", "refused", "failed"
	Detail  string   // refusal/failure cause; offending path first
	Written []string // §1.1: every path written even on later failure
	Removed []string // manifest-listed files the new tree no longer ships
}

var ErrUnowned = errors.New("existing files not written by agentctl")

func Install(tree fs.FS, root, version string, targets []Target, force bool) ([]Outcome, error)
```

- [ ] **Step 1: Failing tests** against a `t.TempDir()` home, in this order:
  fresh install writes all files 0644, dirs 0755, manifest present, action
  `installed`; re-run → `current` with zero writes (assert mtimes unchanged);
  version bump → overwrite when hashes match old manifest; old manifest lists a
  file the new tree does not ship with its hash still matching that manifest →
  removed and reported in `Removed` (hash mismatched → the ownership refusal
  below, never silent deletion); **refusals**:
  directory with no manifest and a stray file → `refused`/`ErrUnowned` naming
  the stray path, nothing written; manifest present but a file hash matches
  neither old manifest nor new tree (user edit) → `refused` unless
  `force=true`; **partial failure**: make the second target dir read-only,
  assert first target's `Written` is complete and error is returned.
- [ ] **Step 2:** Run — FAIL. **Step 3:** Implement. Rules: refuse before the
  first write per target (validate, then write); `--force` bypasses only the
  ownership refusal, never permission errors; never follow symlinked target
  dirs silently — `os.Lstat` the target, a symlink is reported in `Detail`
  and treated as unowned.
- [ ] **Step 4:** Run — PASS; `go vet ./...`. **Commit.**

### Task 7: `internal/skillinstall` — Status

**Files:**
- Create: `internal/skillinstall/status.go`
- Test: `internal/skillinstall/status_test.go`

**Interfaces:**
- Produces:

```go
type State string // "current", "stale", "modified", "absent", "unmanaged"

type Report struct {
	Target           Target
	State            State
	InstalledVersion string // "" when absent/unmanaged
}

func Status(tree fs.FS, root, version string, targets []Target) ([]Report, error)
```

- [ ] **Step 1: Failing tests**, asserting the spec §4.3 precedence order
  (first match wins): no directory → `absent`; directory without manifest →
  `unmanaged`; manifest version differs → `stale` (report installed version;
  hashes are not compared across versions); version matches but a hash
  differs → `modified`; version and hashes match → `current`.
- [ ] **Steps 2–4:** red, implement, green. **Commit.**

### Task 8: CLI wiring — `skill` command group

**Files:**
- Modify: `cmd/agentctl/main.go` (globalUsage, commandUsage, dispatch)
- Test: `cmd/agentctl/skill_command_test.go`

**Interfaces:**
- Consumes: Tasks 5–7. Home resolution: `os.UserHomeDir()`; failure →
  refusal with `exitUnclassified`, never a fallback path (spec §6).

Usage strings (add to `commandUsage`; add `skill` line to `globalUsage`):

```go
"skill": "Usage: agentctl skill install [--force] | agentctl skill status\n\n" +
	"install writes this binary's embedded agent skill to ~/.claude/skills/agentctl\n" +
	"and ~/.agents/skills/agentctl; it refuses to overwrite files it cannot prove\n" +
	"it wrote (--force overrides). status reports current|stale|modified|absent|unmanaged\n" +
	"per target.\n",
```

- [ ] **Step 1: Failing CLI tests** (table-driven through `run` with a temp
  `$HOME` via `t.Setenv("HOME", ...)`): `skill` alone → usage, exit 2;
  `skill install` fresh → exit 0, output names both targets and `installed`;
  second run → exit 0, `current`; unowned target without `--force` → exit 5,
  message names the offending path; with `--force` → exit 0; `skill status`
  → exit 0 in every state, one line per target
  (`<dir>: <state> (installed <v>, binary <v>)`).
- [ ] **Step 2:** red. **Step 3:** wire dispatch: `skill` parses its
  subcommand before `parseCommand` (it takes no `--session`); map
  `ErrUnowned` → `exitUnsafe`, usage errors → `exitUsage`, other errors →
  `exitUnclassified`. Exit-code additions ride into the skill's
  `references/exit-codes.md` row for `exitUnsafe` (done in PR 1 text already).
- [ ] **Step 4:** green; full gates. **Commit.**

### Task 9: Launch notice

**Files:**
- Modify: the launch success path in `cmd/agentctl` (where launch output is
  rendered — locate the confirmation added by PR #122 and print after it)
- Test: extend `cmd/agentctl` launch tests

- [ ] **Step 1: Failing tests**: after successful launch with a manifest of
  a different version present in temp `$HOME` → stderr gains exactly one
  line: `skill: ~/.claude/skills/agentctl is 0.2.0; this binary is 0.3.0 —
  run 'agentctl skill install'` (per target, oldest first, at most one line
  per target); no manifest anywhere → no line; manifest current → no line;
  manifest present but unreadable/unparseable (chmod 000 or junk content) →
  one stderr line stating the read failure, never a claimed version; the
  notice lines are the **final** stderr lines, after the #121 confirmation
  output. Launch exit code is unchanged in every case — a skill-notice
  failure is never a launch failure.
- [ ] **Steps 2–4:** red, implement (read manifests only — never hash file
  contents at launch; version comparison only), green. **Commit.**

### Task 10: SECURITY.md amendment + PR 2 assembly

- [ ] Replace the sentence at `SECURITY.md:50` ("agentctl creates no
  persistent files of its own (no database, no state directory) and writes
  nothing inside application repositories.") with:

```markdown
agentctl writes files only under `$HOME/.claude/skills/agentctl/` and
`$HOME/.agents/skills/agentctl/`, and only when the operator runs
`agentctl skill install` (directories `0755`, files `0644`, plus a
`.agentctl-skill.json` manifest recording the version and SHA-256 of every
file written). Installs are manifest-checked and refuse to overwrite files
agentctl cannot prove it wrote, absent `--force`. No launch, control,
status, or kill path writes to the filesystem; `launch` reads the manifests
(only) to report skill/binary version skew. The skill content is a
build-time constant; target paths are fixed with no caller-supplied
components; a failed `$HOME` resolution is a refusal, not a fallback.
Otherwise agentctl creates no persistent files (no database, no state
directory) and writes nothing inside application repositories.
```

- [ ] Update spec §4.2/§4.4 only if implementation deviated (report first).
- [ ] Sequencing: #128 amends other parts of SECURITY.md on the same
  milestone; whichever PR lands second rebases onto the other's wording
  rather than reverting it.
- [ ] Rebase; full gates incl. integration suite; open PR "Refs #80" (PR 3
  completes it), milestone 0.3.0; reviewer gate; detach worktree.

### Task 11: Integration test (rides in PR 2)

**Files:**
- Create: `cmd/agentctl/skill_integration_test.go` (build tag `integration`)

- [ ] Test: with temp `$HOME`, run the real binary path (`run(...)`) for
  `skill install`, then assert both harness directories contain byte-identical
  trees matching `skills.Tree` and consistent manifests; then `skill status`
  reports `current` for both. **Commit** with the task it verifies.

---

## PR 3 (#80): release and CI gates

### Task 12: `hack/check-skill-version.sh`

**Files:**
- Create: `hack/check-skill-version.sh`
- Test: `hack/checkskillversion_test.go` (follow `nextversion_test.go`'s
  fixture pattern: run the script against a temp dir)

- [ ] **Step 1: Failing Go test cases**: version argument matches the
  `version:` line in `skills/agentctl/SKILL.md` → exit 0; mismatch → exit 1
  and stderr names both values; missing version line → exit 1.
- [ ] **Step 2: Script** (shellcheck-clean):

```bash
#!/usr/bin/env bash
# Fails when the skill's metadata.version does not equal the release version.
set -euo pipefail
release_version="${1:?usage: check-skill-version.sh RELEASE_VERSION [SKILL_MD]}"
skill_md="${2:-skills/agentctl/SKILL.md}"
skill_version="$(sed -n 's/^[[:space:]]*version:[[:space:]]*"\{0,1\}\([^"]*\)"\{0,1\}$/\1/p' "$skill_md" | head -n 1)"
if [ -z "$skill_version" ]; then
  echo "no metadata.version in $skill_md" >&2
  exit 1
fi
if [ "$skill_version" != "$release_version" ]; then
  echo "skill documents $skill_version; releasing $release_version — update the skill with the surface it ships" >&2
  exit 1
fi
```

- [ ] **Step 3:** green; wire into `hack/release-verify.sh` (or the release
  snapshot CI step — inspect `.github/workflows/ci.yml` and put it where the
  other release checks run). `shellcheck hack/*.sh` clean. **Commit.**

### Task 13: `hack/check-skill-pairing.sh` + CI job

**Files:**
- Create: `hack/check-skill-pairing.sh`
- Test: `hack/checkskillpairing_test.go` (fixture git repos in temp dirs)
- Modify: `.github/workflows/ci.yml`

Security constraints (spec §5.3, blocking finding B1 on PR #129): the
override token lives in **commit messages within the PR range**, never the
PR body — a body is attacker-editable free text that can change after a
green run. The script discovers the token itself with `git log`; the
workflow passes only SHAs, via `env:`, and **never** interpolates
`${{ github.event.* }}` text inside `run:`.

- [ ] **Step 1: Failing Go test cases** (each builds a small git repo
  fixture): change under `cmd/agentctl/` only → exit 1; change under
  `cmd/agentctl/` plus `skills/agentctl/` → exit 0; change under
  `cmd/agentctl/` only, with `[skill-unaffected]` in any commit message in
  the range → exit 0; token present only in a commit outside the range →
  exit 1; change elsewhere only → exit 0; `internal/config/` change → exit 1;
  base branch advanced with a surface change after the branch point while the
  PR touches no surface path → exit 0 (pins the three-dot/merge-base diff —
  a two-dot diff fails this fixture).
- [ ] **Step 2: Script**: args `BASE_SHA HEAD_SHA`;
  `git diff --name-only "$BASE_SHA"..."$HEAD_SHA"` for the surface check —
  three dots (merge-base comparison), because `base.sha` is the base branch
  TIP: a two-dot tree diff would attribute changes landed on main after the
  branch point to the PR
  (surface paths `cmd/agentctl/` and `internal/config/`; skill path
  `skills/agentctl/`); override check is
  `git log --format=%B "$BASE_SHA".."$HEAD_SHA" | grep -Fq '[skill-unaffected]'`.
- [ ] **Step 3:** green; CI wiring in the `test` job, guarded so push runs
  skip it (the squash merge may not carry the token and must not fail the
  required context):

```yaml
      - name: Skill pairing check
        if: github.event_name == 'pull_request'
        env:
          BASE_SHA: ${{ github.event.pull_request.base.sha }}
          HEAD_SHA: ${{ github.event.pull_request.head.sha }}
        run: hack/check-skill-pairing.sh "$BASE_SHA" "$HEAD_SHA"
```

  SHAs are not free text, but they still cross via `env:` — the rule is no
  event payload in `run:`, without exceptions a future edit could widen.
  Ensure checkout fetch-depth covers the PR range (fetch base explicitly or
  set `fetch-depth: 0` for this step's job). **Commit.**

### Task 14: Release checklist + runbook

**Files:**
- Modify: `docs/release-checklist.md`

- [ ] Add the skill step: run `hack/check-skill-version.sh` with the release
  version; and the §5.4 live probe: stub fleet on a throwaway socket,
  `agentctl skill install` under a temp `$HOME` pointed at by the harness
  under test, confirm the harness lists the `agentctl` skill, ask one
  semantic question ("what does `ambiguous` mean and which commands refuse
  on it") and check the answer against `references/status-states.md`.
- [ ] PR 3 assembly: rebase, gates, PR "Closes #80", milestone, reviewer
  gate, detach worktree.

---

## Self-review record

- Spec coverage: §3 → Task 1; §4.1 → Task 2; §5.1 → Task 3; §4.2 → Tasks
  5–6, 8; §4.3 → Tasks 7–8; §4.4 → Task 9; §6 → Task 10; §5.2 → Task 12;
  §5.3 → Task 13; §5.4/§7 → Tasks 11, 14. No uncovered spec section.
- The skill's `metadata.version` is `0.3.0` in-tree; Task 12 enforces it at
  release; workers do not bump it mid-milestone.
- Type consistency: `skills.Tree`/`skills.Root` (Task 2) are the only
  producers consumed across PRs; `Manifest`/`Target`/`Outcome`/`Report`
  names match between Tasks 5–8.
