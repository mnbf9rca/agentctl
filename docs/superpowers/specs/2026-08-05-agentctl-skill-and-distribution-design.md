# agentctl skill and distribution design — 2026-08-05

Status: approved in design session 2026-08-05 (planner + Rob).
Governs: issues [#78](https://github.com/mnbf9rca/agentctl/issues/78) (the skill) and
[#80](https://github.com/mnbf9rca/agentctl/issues/80) (its distribution and update mechanism).
Companion documents: [`2026-08-01-agentctl-design.md`](2026-08-01-agentctl-design.md) (the
product spec; its §1.1 factual-claim principle and §9 exit-code contract bind everything
here), [`SECURITY.md`](../../../SECURITY.md) (amended by this work, §6 below),
[`docs/release-checklist.md`](../../release-checklist.md) (gains a skill step, §5.2).

Research provenance: decisions below rest on four research streams run 2026-08-05 —
Claude Code skill/plugin mechanics, codex skill mechanics (introspected on codex-cli
0.146.0), distribution prior art, and drift-detection prior art. Load-bearing findings are
restated inline with their source; nothing in this spec depends on an unverified claim.

## 1. Summary and scope

One agent-facing skill named `agentctl` teaches fleet agents to drive agentctl on sibling
sessions. The skill source lives in this repository at `skills/agentctl/`, is embedded
into the binary at build time with `go:embed`, and reaches user machines exclusively
through a new explicit subcommand, `agentctl skill install`, which writes it to the
user-scope skill directories both harnesses natively load. Drift between skill and binary
is made structurally impossible at the byte level (the skill ships inside the binary it
documents) and is guarded at the prose level by three gates: a contract test, a release
version check, and a paired-file CI check.

In scope: skill content and budget (#78), embedding, the `skill` command group, the
SECURITY.md amendment, the three drift gates, and release-checklist integration (#80).
Out of scope: #81 (security review) and #121 (launch confirmation), which run under their
own issue contracts; marketplace or npx packaging (rejected, §2).

## 2. Decisions and rejected alternatives

| Decision | Choice | Why |
|---|---|---|
| Skill count | One skill; no operator/agent split | Agents are the only audience that loads skills; a second document doubles drift surface. `launch`/`attach` appear once, marked operator-only. |
| Source of truth | `skills/agentctl/` in this repo, embedded via `go:embed` | Single canonical tree; the embedded copy can never diverge from the release that carries it. |
| Harness coverage | One SKILL.md serves both harnesses | Both Claude Code and codex implement the Agent Skills format (verified on codex-cli 0.146.0; user skills at `~/.agents/skills` observed live). Only discovery paths differ. |
| Delivery | `agentctl skill install`, explicit and user-invoked; never automatic at launch or control time | Structural version-lockstep with zero new dependencies. Writing files is a deliberate, opt-in act, matching the SECURITY.md posture. |
| In-repo symlinks (`.claude/skills`, `.agents/skills`) | None | Users get agentctl via brew, not by cloning this repo; user-scope install covers the development fleet too, and avoids same-name shadowing between scopes, for which codex documents no precedence rule. |
| Marketplace plugin | Rejected | codex needs a structurally separate manifest/catalog — two artifacts forever — and the failure mode (maintainer forgets the version bump, updates silently stop) is the exact drift #80 exists to close. |
| `npx skills` / `skild` | Rejected | Adds a Node runtime dependency class the project has avoided; upstream lockfile acknowledged immature; would still require our own drift gates, so it buys nothing structural. |
| Launch-time injection of skill content | Rejected | No prior art anywhere for delivering skill content (vs task content) by keystroke injection; would require a new payload-registry entry; both harnesses already hot-detect skill files, which D-delivery serves better. |

## 3. Skill content contract (#78)

### 3.1 Layout and budget

```
skills/agentctl/
  SKILL.md            # ≤150 lines; the budget is enforced by the contract test
  references/
    status-states.md  # full semantics table for the six status states
    exit-codes.md     # exit-code table (§3.3)
```

Frontmatter uses only portable Agent Skills fields: `name: agentctl`, `description`
(trigger prose), `compatibility`, and `metadata` with `version` equal to the agentctl
version the skill documents. No harness-specific fields; neither harness enforces
`compatibility` or `metadata.version`, so they are documentation read by our own tooling
(§5), not runtime gates.

### 3.2 Required content

SKILL.md covers, in order:

1. **Self-identification before any command**: every agentctl-launched window carries
   `AGENTCTL_ROLE` (own role), `AGENTCTL_SESSION` (own fleet), and `AGENTCTL_MANAGED`
   in its environment (product spec §13.2 row 5a), alongside the AMQ identity `AM_ME`/
   `AM_SESSION` set by `amq coop exec` and tmux's `TMUX_PANE`. The agent verifies who
   and where it is from these before issuing commands: `AGENTCTL_SESSION` is what a bare
   invocation resolves to (it sits second in §4.1 precedence), so inside a pane, bare
   commands act on the agent's own fleet, and targeting any other fleet requires an
   explicit `--session`. `AM_SESSION` disagreeing with `AGENTCTL_SESSION` means the
   environment is not what the agent assumes — stop and report rather than target. The
   skill also states the limit: these variables are advisory identity fixed at exec
   time, the basis of the accident guards, not a permission boundary.
2. **Agent command surface**: `status` (including `--json`), `clear`, `compact`, `kill`,
   and the session resolution order (product spec §4.1).
3. **Reading `status` as claims**: `ambiguous`, `unmanaged`, `missing`, `dead`,
   `unexpected-process`, `running` — the §6.3 precedence order —
   are distinct factual claims (product spec §6.3); liveness must not be inferred from
   anything else. Full table in `references/status-states.md`.
4. **Operational rules the binary does not enforce**, each stated with its reason:
   no control commands to a role not released back to you; none while the fleet
   saturates the host (SECURITY.md residual 1); delivery is not execution — verify by
   observing subsequent behaviour, not exit 0; the self-target guard is an accident
   guard, not a permission boundary.
5. **Context-hygiene policy** (from #78's AMENDED 2026-08-04 section): `clear` between
   unrelated tasks and wedged workers; `compact` for same-subsystem continuity, mid-task
   context pressure, and the reviewer mid-batch; never mid review-fix loop or under host
   saturation; confirm the reset before the next dispatch. Stated with its corollary:
   routine clearing is only safe because dispatch messages are self-contained.
6. **Deliberate can't-dos**: no arbitrary keystrokes, no free-text payloads, no AMQ
   state access, no per-window restart until #79's successor lands.
7. **Operator-only surface**: `launch` and `attach`, one line each.
8. **Exit-code branching**: pointer to `references/exit-codes.md`.

### 3.3 Machine-readable conventions

The contract test (§5.1) parses these conventions, so the skill must maintain them:

- Every documented invocation appears in a fenced code block as a line starting
  `agentctl `; flags appear in their `-flag` spelling.
- `references/exit-codes.md` holds one table whose first column is the numeric code and
  whose second column is the constant name (`exitOK`, `exitUnclassified`, `exitUsage`,
  `exitSession`, `exitRole`, `exitUnsafe`, `exitTmux`, `exitMissingExecutable`,
  `exitLaunch`, plus any added later).
- `references/status-states.md` names each status state in a backticked table cell.

## 4. Embedding and the `skill` command group (#80)

### 4.1 Embedding

`skills/agentctl/` is embedded with `go:embed` from `cmd/agentctl`. The embedded tree is
the only distribution artifact; there is no network fetch and no separate skill release.

### 4.2 `agentctl skill install [--force]`

Writes the embedded tree to both user-scope discovery directories:

- `$HOME/.claude/skills/agentctl/` (Claude Code, user scope)
- `$HOME/.agents/skills/agentctl/` (codex, user scope)

Directories `0755`, files `0644`. The install additionally writes a manifest file
`.agentctl-skill.json` inside each target directory recording the agentctl version and
the SHA-256 of every installed file.

Ownership rule (fail-closed): if a target directory exists and either has no manifest or
contains files whose hashes match neither the current manifest nor the tree being
installed, it is treated as someone else's work — the install refuses with `exitUnsafe`
(5) and names the offending path, unless `--force` is given. A target matching the
manifest is overwritten freely (that includes downgrade on binary downgrade — the skill
always tracks the invoking binary). A fully current install is a silent success, exit 0.
Partial-write failure reports every path written and every path not written (§1.1) and
exits `exitUnclassified` (1). Usage errors exit `exitUsage` (2). No other command writes
these paths; `launch`, control commands, and `status` never install or repair the skill.

Files listed in the target's existing manifest but absent from the tree being installed
are removed by the install — they are provably agentctl's — and every removal is
reported by path (§1.1). A manifest-listed file whose hash no longer matches falls under
the ownership rule above, not silent deletion.

There is no uninstall subcommand in v1; the manifest makes manual removal safe and
auditable. YAGNI until someone asks.

### 4.3 `agentctl skill status`

Reports, per target directory, one factual claim, evaluated in this order with the
first match winning: `absent` (no target directory), `unmanaged` (directory present, no
manifest), `stale` (manifest version differs from the binary — content comparison
across versions is not meaningful), `modified` (version matches, hashes differ),
`current` (version matches, hashes match). Exit 0 whenever the report was produced;
the state lives in the output, mirroring `status` semantics. This answers #80's "does
this skill match this binary?" acceptance question in one command.

### 4.4 Launch notice

After a successful `launch`, agentctl checks the two manifests (its own files, nothing
else). If a manifest exists and its version differs from the binary, launch prints one
line to stderr: the installed version, the binary version, and the remediation command.
If no manifest exists anywhere, launch stays silent — never installed means never opted
in, and nagging is not a factual claim anyone needs. A manifest that exists but cannot
be read or parsed produces one stderr line stating that fact — never silence, and never
a claimed version (§1.1). The notice lines print after the launch confirmation output
(#121), as the final stderr lines; the launch exit code is unchanged in every case — a
skill-notice failure is never a launch failure. This is the only skill-related
behaviour attached to any other command.

## 5. Drift gates

Byte-level drift is impossible by construction (§4.1). The gates below cover prose drift
and process drift. None is contingent on the delivery mechanism.

### 5.1 Contract test (runs in `go test ./...`)

A test in `cmd/agentctl` reads the embedded skill tree and asserts:

- every `agentctl `-prefixed line in fenced blocks names a command that is a key in
  `commandUsage`, and every flag on that line is registered on that command's `FlagSet`;
- every constant name in `references/exit-codes.md` exists with the documented numeric
  value, and every `exit*` constant in `cmd/agentctl` appears in the table (both
  directions — a new exit code cannot ship undocumented);
- every status state documented in `references/status-states.md` exists in the status
  package's state set, and vice versa;
- SKILL.md is within the §3.1 line budget and its `metadata.version` parses.

This is a floor, not a proof: it verifies documented tokens exist, not that prose is
true. Prose truth is the review gate's job, plus §5.3's live probe.

### 5.2 Release version check

The release checklist gains a step, backed by a `hack/` script run in CI's release
snapshot checks: the skill's `metadata.version` must equal the version being released.
A CLI-surface release cannot ship without the embedded skill moving with it (#80
acceptance). Shape follows Terraform's `required_version`: an explicit value enforced
fail-closed — at release time, not at runtime, because agentctl never consumes its own
skill.

### 5.3 Paired-file CI check

A `hack/` script (shellcheck-clean, exercised by a Go test like the other hack scripts)
diffs the PR range: if `cmd/agentctl/**` or `internal/**` paths that define the command
surface changed and `skills/agentctl/**` did not, the check fails unless a commit
message within the PR range carries the literal token `[skill-unaffected]`. The
override lives in commit messages, not the PR body: a body is attacker-editable free
text that can change after a green run, while a commit message is immutable per head
SHA and re-triggers CI when it changes. The script reads the messages itself
(`git log` over the range); the workflow passes only refs, via `env:`, and never
interpolates event text into `run:`. The check executes only on `pull_request` events —
on push (including the squash merge, which may not carry the token) it is skipped. The
override exists because surface-neutral refactors under those paths are common; it is
cheap, explicit, and visible to the reviewer gate. This catches the human failure
(touched the CLI, forgot the doc); §5.1 catches the mechanical one.

### 5.4 Live verification (release checklist)

The release checklist's live-verification runbook gains: launch a stub fleet on a
throwaway socket, `agentctl skill install` on a temp `$HOME`, confirm the harness lists
the skill, and probe one semantic question (e.g. what `ambiguous` means and which
commands refuse on it) to confirm the skill answers it correctly. This is the only gate
that tests the prose against a real agent.

## 6. SECURITY.md amendment

The claim "agentctl creates no persistent files of its own (no database, no state
directory) and writes nothing inside application repositories" is amended in the same PR
that lands §4, to state: agentctl writes files only under `$HOME/.claude/skills/agentctl/`
and `$HOME/.agents/skills/agentctl/`, only when the operator runs `agentctl skill
install`, with `0755`/`0644` permissions; writes are manifest-checked and refuse to
overwrite files agentctl cannot prove it wrote (absent `--force`); no launch, control,
status, or kill path writes to the filesystem. The threat analysis covers the new write
path: the skill content is a build-time constant, the target paths are fixed (no
caller-supplied path components), and `$HOME` resolution failure is a refusal, not a
fallback.

## 7. Testing and iteration plan

- **TDD throughout** (repository rule): each behaviour lands with its failing test
  first; red/green evidence in the PR.
- **Unit**: install/status against a temp `$HOME` — exact paths, permissions, manifest
  round-trip, every refusal case in §4.2, downgrade, partial-failure reporting; the
  §5.1 contract test; launch-notice rendering (audited at print-time).
- **Integration** (`-tags integration`): real install on a temp `$HOME`, then assert
  both harness discovery paths resolve to identical, manifest-consistent trees.
- **Process gates**: `go test ./...`, `go vet ./...`, `shellcheck hack/*.sh`,
  `golangci-lint`, integration suite, release snapshot checks — per CI.
- **Iteration loop**: this repository's own fleet is the dogfood. Skill prose changes
  are driven by observed agent behaviour (an agent misreading `status`, over-clearing,
  or asking for a can't-do), land as ordinary PRs, and are bounded by the same contract
  test and reviewer gate. The 0.3.0 release runbook (§5.4) is the acceptance probe.

## 8. Work decomposition

1. **PR 1 (#78)**: `skills/agentctl/` content + the §5.1 contract test. No behaviour
   change to the binary. Closes #78's acceptance (skill exists, rules with reasons,
   budget enforced, drift-test present).
2. **PR 2 (#80)**: `go:embed`, the `skill` command group, the launch notice, the
   SECURITY.md amendment. Depends on PR 1.
3. **PR 3 (#80)**: §5.2 release script + §5.3 paired-file check + release-checklist and
   runbook updates. May fold into PR 2 if small; the reviewer gate decides.

Issue bodies of #78 and #80 are amended (with `AMENDED 2026-08-05` markers) to bind to
this spec before dispatch. All resulting PRs carry the 0.3.0 milestone and pass the
reviewer gate; nothing merges without it.

## 9. Known non-blocking gaps

- codex documents no same-name skill precedence across scopes; avoided by the unique
  `agentctl` name and by shipping to user scope only.
- Neither harness enforces `compatibility`/`metadata.version`; our gates (§5) carry that
  weight deliberately.
- The `skill status` output format is plain text in v1; a `--json` form is deferred
  until an orchestrator actually needs to branch on it.
