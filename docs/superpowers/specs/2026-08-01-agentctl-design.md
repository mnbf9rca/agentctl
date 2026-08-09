# agentctl design — 2026-08-01

Status: approved in design session 2026-08-01.
Companion documents: [`docs/brief.md`](../../brief.md) (normative requirements), [`SECURITY.md`](../../../SECURITY.md) (threat model).

This spec records the decisions, verified external contracts, and architecture agreed in the design session. Where this document and `brief.md` conflict, this document wins — every deviation from the brief is listed in §2.

## 1. Summary

`agentctl` is a standalone Go CLI that launches a named tmux session containing a fleet of autonomous agents (one window per role, each started via `amq coop exec`), records role/harness/model metadata in tmux options, delivers only predefined control payloads (`/clear`, `/compact`) to validated panes, reports objective fleet status, and attaches the fleet as native iTerm2 tabs via tmux control mode.

### 1.1 Principle: every output is a factual claim

Every observable output of agentctl — an exit code, a message, a reported state — is a factual claim about what
actually occurred. It must never assert an event that did not happen, and it must carry as much of what did happen as
the facts allow.

Both halves have already been decided several times, in different subsystems, before the principle was written down:

| Half | Where it bites |
|---|---|
| Never assert what did not happen | Stubs must not borrow a contract code (§9). Exit 6 requires that a tmux command actually ran (§4.1). Delivery is reported as delivery, never as execution (§6.2). `status` reports objective tmux facts and never infers workflow state (§6.3). Identity that is *unverifiable* is not identity *verified* (§6.3). |
| Carry as much as the facts allow | Every launch-failure message names what failed (§6.6). A tmux failure carries tmux's own stderr rather than a translation (§4.1). An ambiguous role names every matching window ID (§13.5). `unexpected-process` renders the executable actually observed, not the one expected (§6.3). |

The rules in those sections are the operative ones; this paragraph is why they are all the same rule. When a new
output is designed and no specific rule covers it, this is the one to apply — and the test is not "is this message
reasonable" but "is every claim in it true, and is anything true being withheld".

### 1.2 Principle: return to the prior state where one exists, and never destroy peers

An invocation may destroy what it created, and only in order to return the system to the state it found. Where there is
no prior state to return to, it destroys nothing and reports instead. Nothing an invocation did not create is ever
destroyed to tidy up after a failure.

Like §1.1, this was decided several times before it was written down, and the decisions look unrelated until the rule
is named:

| Where it bites | What the rule produces |
|---|---|
| §6.6, before ownership | `new-session` returned no parseable session ID, so agentctl created nothing and destroys nothing — the session is left for the operator to see and remove (exit 6). |
| §6.6, after ownership | The session exists because this invocation made it, and no fleet existed before, so rollback returns to nothing: kill the typed session ID (exit 8). |
| §6.6, settle timeout | There is no prior state to return to and the peers are not this invocation's to destroy, so `launch` retains the fleet and reports the unproven role (exit 9). |
| §6.8 step 9 | `relaunch` removes only the window it created, which restores the fleet exactly as it found it (exit 8). |
| §8 | The same timeout produces retention in `launch` and rollback in `relaunch`, because only one of them has a prior state. |
| §13.2 row 11a | `kill-window` is exposed only for windows this invocation created — plus §6.8 step 5a's bounded recovery, the single deliberate exception, which is why that exception is stated as a change rather than absorbed. |

The two principles answer different questions and are both required. §1.1 governs what an output may *claim*; this one
governs what an invocation may *destroy*. A rollback that is honestly reported can still be the wrong act, and a
retention that is silently performed can still be a lie.

When a new failure path is designed, the test is: *what existed before this invocation ran, and does this act return
the system there?* If the answer is "it destroys something that predates me", the act is wrong regardless of how
convenient the cleanup would be.

## 2. Decisions resolved in the design session

These extend or refine `brief.md`:

| Topic | Decision |
|---|---|
| Agent working directory | Windows start in agentctl's invocation cwd, passed **explicitly** to tmux via `-c` (never relying on tmux server default). Optional `--dir PATH` on `launch` overrides. Rationale: `amq coop exec` roots `AM_ROOT`/`.amqrc` in the pane's cwd, so cwd determines the fleet's AMQ session directory. |
| Teardown | New command `agentctl kill [--session S]`. Validates the session is agentctl-managed (same gate as control commands) before `tmux kill-session`. Refuses unmanaged sessions. |
| Model identifier validation | Models are catalogue-free but **not** charset-free: `^[a-zA-Z0-9][a-zA-Z0-9._-]*$`. The mandatory alphanumeric first character makes flag smuggling (e.g. `--dangerously-bypass-approvals-and-sandbox`) unrepresentable in the model slot. |
| Effort validation (added 2026-08-03, issue #88) | Efforts are the **opposite** of models: a closed per-harness allowlist (`low`, `medium`, `high`, `xhigh`, `max`), rejected before anything is created. The value names a harness mode rather than an opaque identifier, and the codex rendering embeds it in an expression codex parses as TOML, so a charset rule would be the weaker instrument. Optional everywhere: a role with no effort emits no harness argument at all. |
| `--if-missing` | Deferred. v1 `launch` always fails when the target session exists. The exact-fleet-comparison metadata is designed in now so `--if-missing` is cheap later; tracked as a deferred backlog issue. Its direction is fixed by §6.5: comparison reads `@agentctl_fleet` and `@agentctl_dir` behind the §12.6 gate — **zero** window reads — which is correct even when roles are missing, and refuses legacy sessions carrying neither option. |
| Harness process check | No name pattern-matching. At launch, the observed executable of each pane's root process is recorded as `@agentctl_process` metadata; control and status require exact equality with that baseline (§8). Motivated by the spike finding that Claude Code's process name is its **version string** (e.g. `2.1.220`), not `claude`. |
| Payload registry policy | The registry is hardcoded and may grow beyond `clear`/`compact`, but only with **argument-free** payloads. Commands that carry caller-supplied text (e.g. `/rename NAME`) are permanently inadmissible. |
| Self-target guard | When invoked from inside tmux, a control command whose resolved target pane equals the caller's own pane (`$TMUX_PANE`) is refused (exit 5). Prevents an agent or planner accidentally wiping its own context; an accident guard, not a security boundary. |

## 3. Verified external contracts (2026-08-01, this machine)

### 3.1 amq (0.50.1)

```
amq coop exec [options] <command> [-- <command-flags>]
```

- Harness flags go **after a `--` separator**:
  `amq coop exec --session S --me ROLE claude -- --model fable`
- Roles without an explicit model omit the trailing `-- --model …` entirely.
- `amq coop exec` auto-initializes `.amqrc` and roots `AM_ROOT` in the pane's cwd (hence the `--dir` decision).
- It `exec()`s into the harness: spike window running `amq coop exec … bash` showed foreground process `bash`. The intended process hierarchy in the brief holds.

### 3.2 Harness slash commands (Claude Code 2.1.220, codex-cli 0.146.0)

Both harnesses natively support `/clear` and `/compact`. One payload registry serves both; no per-harness payload map is needed in v1. The registry stays structured per-operation so a per-harness split remains a small change.

### 3.2.1 Harness effort arguments (verified 2026-08-03, this machine)

| Harness | Version | Rendering | Evidence |
|---|---|---|---|
| claude | Claude Code 2.1.220 | `--effort LEVEL` | `claude --help`: *"Effort level for the current session (low, medium, high, xhigh, max)"* |
| codex | codex-cli 0.146.0 | `--config 'model_reasoning_effort="LEVEL"'` | no `--effort` flag on the main CLI (`codex --help`); `-c/--config` documents that the value portion is parsed as TOML |

codex's own reasoning-effort enum additionally carries `none`, `minimal` and `ultra`. agentctl exposes only the five
levels verified on **both** harnesses, so one accepted set serves both; a per-harness split is already the mechanism
(`internal/config`'s per-harness table plus `harness.Spec.effortArgs`) and would be a small change if the sets diverge.

### 3.3 Keystroke-injection spike results

Method: throwaway tmux server (`tmux -L agentctl-spike`), real harness binaries, `capture-pane` snapshots between steps (spike only — the product never scrapes).

| Check | Claude Code | Codex |
|---|---|---|
| Junk pending input registered | yes | yes |
| `C-u` cleared pending input | yes (TUI offered Ctrl+Y undo) | yes (placeholder text returned) |
| Literal `/clear` + separate `Enter` executed | yes | yes |
| `#{pane_current_command}` | **`2.1.220`** (version string) | `codex` |

Consequences:

- The delivery sequence in the brief (`C-u`; `send-keys -l -- '/PAYLOAD'`; `Enter`) is adopted unchanged for both harnesses, with a fixed delay between payload and Enter. That delay is **1s** — the value the spike established and the only one with evidence behind it. It is not tuned down until issue #13 measures the popup-settle floor under load on both harnesses; see §13.6.
- Typing a slash command opens an autocomplete popup in both TUIs; Enter selects the highlighted entry. With the full command typed, the exact match was highlighted in both. Residual risk (user-defined commands outranking an exact match) is documented in SECURITY.md.
- Process names cannot be pattern-matched against harness names (Claude Code reports `2.1.220`), and `#{pane_current_command}` tracks the *foreground job*, so it flaps to child commands (`bash`, `python`, …) whenever an agent runs a tool. Both problems are solved by the launch-time baseline policy in §8.

### 3.4 iTerm2 force-quit of tmux control mode (2026-08-04)

Observed live against a throwaway single-role fleet — tmux 3.7b, iTerm2 3.6.11, agentctl v0.1.0 (Homebrew) with `main`.
Recorded because §6.4's narration previously declined to say what force-quit leaves behind, on the correct grounds that
it had not been measured. It has now been measured, so §1.1's second half applies: state it.

| # | Observation |
|---|---|
| 1 | **The fleet survives.** `agentctl status` reported the role still running after force-quit. |
| 2 | **The client does not exit.** The `tmux -CC attach-session` process persisted and the session still reported `attached=1`. iTerm2's own exit text — "tmux client may still be running" — is literally true. |
| 3 | **The client is hard to remove.** After `agentctl kill --session`, it printed raw control-protocol lines (`%sessions-changed`, `%exit`) into the terminal, still did not exit, ignored `SIGTERM`, and ended only on `SIGKILL`. |

Consequence for agentctl, mechanical rather than separately observed: `attach` blocks on that client, and the state
report is written only after it returns. Observation 2 is therefore sufficient to establish that **the report is
unreachable on the force-quit path** — the process was seen not to return, and everything after it is sequential.

This is what turns "prefer `esc`" from a preference into a statement with a reason behind it: `esc` detaches cleanly and
agentctl reports; force-quit leaves the fleet running but wedges the client, so the terminal stays occupied and nothing
further is printed.

## 4. CLI surface

```
agentctl launch  --session S --roles R:H,... [--models R:M,...] [--efforts R:L,...] [--dir PATH]
agentctl launch  --session S --from-template FILE [--roles R:H,...] [--models R:M,...] [--efforts R:L,...] [--dir PATH]
agentctl relaunch [--session S] [--harness H] [--model M] [--dir PATH] ROLE
agentctl attach   [--session S]
agentctl status   [--session S | --all] [--json]
agentctl clear    [--session S] ROLE
agentctl compact  [--session S] ROLE
agentctl kill     [--session S]
```

Everything else in the brief's CLI section applies verbatim: no `--launch` alternative syntax, no arbitrary-payload options of any kind, duplicate command-line options rejected.

**`launch` has two forms, and `--roles` is required in the first.** `--from-template` is the only thing that makes
`--roles` optional, and it makes it optional rather than forbidden: a template may supply the whole roster, or the
flag may add roles beside it (§6.9). Without a template, `--roles` remains required exactly as before — a bare
`agentctl launch --session S` is a usage error (exit 2), because a fleet with no roles is not a fleet. The two forms
are written separately above rather than as one line with everything bracketed, because a single line cannot express
"one of these two is required" without saying it in prose anyway.

**`status` never narrows silently.** Bare `agentctl status` reports **every** session on the tmux server (§6.3.1).
Ambient context — `AGENTCTL_SESSION`, or the tmux session the caller happens to be sitting in — does not select a
target for `status` and never reduces its output to one fleet; it may only *mark* which session the caller is in.
Only an explicit `--session S` narrows the report to one session, because only that is the operator saying which fleet
they mean.

There is no `--all` flag. It existed to make the listing reachable from inside tmux, where the current-tmux source
always resolved; a bare `status` that always lists makes it redundant, and a flag that changes nothing invites the
reader to believe it changes something.

`clear`, `compact`, `kill` and `attach` are unaffected: each acts on exactly one target, so each still resolves one
through the full §4.1 chain.

### 4.1 Session resolution

Precedence for non-launch commands: explicit `--session` > `AGENTCTL_SESSION` > the current tmux session. `launch`
always requires an explicit `--session` and never invokes the resolver at all.

**Empty is not the same as absent.** `--session=` (explicitly supplied, empty) is a usage error → exit 2, with **no**
fallback: the user named the source, so falling through would substitute a session they did not ask for.
`AGENTCTL_SESSION` set to empty counts as *absent* and falls through — an exported-but-empty variable is how shells
represent "unset" in practice.

**An invalid higher-priority source blocks the lower ones.** A source the user set is never silently skipped. The full
matrix — every source, every form it can take, and the resulting code:

| Source | State | Validated as | Result |
|---|---|---|---|
| `--session NAME` | supplied | session name | invalid → **2** |
| `--session=` | supplied, empty | — | **2**, no fallback |
| `--session` | omitted | — | fall through |
| `AGENTCTL_SESSION` | set, non-empty | session name | invalid → **3** |
| `AGENTCTL_SESSION` | set, empty | — | treated as absent, fall through |
| `AGENTCTL_SESSION` | unset | — | fall through |
| `TMUX_PANE` | set | **pane ID** (`%` + digits) | invalid → **3** |
| `TMUX_PANE` | unset | — | not inside tmux → **3**, unresolvable |
| displayed name (row 12 output) | — | session name | invalid → **3** |

Session-name candidates are validated by `config.ValidateSessionName` — one validator, so the rule cannot drift between
sources. `TMUX_PANE` is the exception in *what* is checked, not in how it is classified: it carries a pane ID rather
than a session name, so it is validated as one, and an invalid value is still an invalid source the user set.

**`status` does not consult the ambient sources at all.** The matrix above governs `clear`, `compact`, `kill` and
`attach` — commands that must end up holding exactly one target. `status` describes rather than acts, so it takes only
an explicit `--session`; `AGENTCTL_SESSION` and the current tmux session neither select for it nor fail it. When no
`--session` is given it reports the listing (§6.3.1), whatever the environment says.

The current-session **marker** in that listing is a separate, advisory read and is specified in §6.3.1. It is not a
resolution: nothing is targeted by it, so nothing is at risk if it cannot be determined.

**Exit 6 requires that a tmux command actually ran** (§1.1). Every invalid-source case above is decided before any command is
issued, so none of them may report a tmux failure. Exit 6 is reserved for a `Runner` or parse failure on row 1 or row
12 and carries tmux's own stderr; claiming it when no command was executed tells the operator to debug something that
never happened, and leaves nothing for them to read. This is the same rule as §9's prohibition on stubs borrowing
contract codes, applied to resolution: a code is a claim about what occurred.

**Resolution is two-step and never targets a name.** Inside tmux, `display-message` (§13.2 row 12, targeted at
`$TMUX_PANE`) yields a session *name*; that name is then exact-matched against `list-sessions` (row 1) to obtain the
session ID. The displayed name never reaches `-t` (§13.1).

**`TMUX_PANE` alone is the inside-tmux signal**, and its typed value is what `display-message` targets. `TMUX` being set
proves nothing on its own, and a bare `display-message` with no `-t` is never issued — it returns empty against a
detached server and is ambiguous with several clients attached (§13.2 row 12).

**Error mapping** distinguishes a broken world from a broken command:

| Condition | Exit |
|---|---|
| No permitted source resolved a candidate | 3 |
| Candidate matched zero sessions | 3 |
| Candidate matched **more than one** session (duplicate exact names) | 3, both session IDs named |
| `Runner` or parse failure on row 1 or row 12 | 6, carrying tmux's own stderr |
| Context cancellation | preserved as-is |

Duplicate exact session names fail closed at exit 3 rather than 6: tmux returned well-formed output describing a broken
world, so the *operation* did not fail. This parallels window ambiguity being a role/window error (exit 4, §13.5) —
session ambiguity is a session-state error. A `tmux` failure such as "no server running" stays **exit 6 and keeps
tmux's message**; it is never translated into "session not found", because that would be inference presented as fact.

The resolver returns a typed `tmuxx.Session` and performs **zero** `@agentctl_*` reads. The §12.6 managed and version
gates belong to the command packages that act on the session, not to resolution.

## 5. Architecture

Go module, stdlib only (`flag`, `os/exec`, `encoding/json`, `regexp`, `testing`). No CLI framework, no tmux client library. All tmux invocations are argv arrays via `os/exec` — agentctl never invokes a shell. The only shell-interpreted string in the system is the window command tmux itself runs via `sh`, assembled exclusively by `shellq` from charset-validated tokens.

| Package | Responsibility |
|---|---|
| `cmd/agentctl` | Subcommand dispatch, exit-code mapping |
| `internal/cliflags` | Per-subcommand flag parsing, duplicate-option rejection |
| `internal/config` | `--roles`/`--models`/`--efforts` parsing and all validation rules (§7) |
| `internal/harness` | Harness registry (claude, codex): model- and effort-argument rendering, input-clear sequence. (Process identity is *not* harness data — it is the launch-time observed baseline, §8.) |
| `internal/shellq` | POSIX single-quote escaping; tiny, table- and fuzz-tested |
| `internal/tmuxx` | `Runner` interface (real: `os/exec`; fake: records argv for tests) plus typed wrappers, one per §13.2 operation: `ListSessions`, `NewSession`, `NewWindow`, `SetOption`, `ShowOptions`, `ListWindows`, `ListPanes`, `DeliverPayload` (§13.6 — no bare `SendKeys` is exported), `KillSession`, `DisplayMessage`, `AttachSession`, `ProcessName` (§13.7) |
| `internal/preflight` | Pure executable checker: `LookPathFunc` seam, `MissingExecutableError`, required-set derivation (`[tmux, amq]` + first-occurrence deduped harnesses). No `Runner` dependency — ordering relative to `Runner` calls is proven by `fleet` (§6.1 step 2). |
| `internal/fleet` | Launcher, single-role relauncher (§6.8), rollback handlers, metadata writer |
| `internal/session` | Session resolver (precedence chain; explicit failure when unresolvable) |
| `internal/target` | Managed-metadata reader; 8-step target validation chain |
| `internal/control` | Hardcoded registry of predefined, argument-free payloads (`clear → /clear`, `compact → /compact` in v1); dispatcher |
| `internal/kill` | Managed-only teardown: reads `@agentctl_managed` then `@agentctl_version` by session ID and refuses unless both are `1` (§12.6), then kills by that ID. Read-and-kill only — its client exposes no other capability. |
| `internal/status` | Collector (tmux format strings only) + table/JSON renderers |
| `internal/attach` | iTerm2 detection, `tmux -CC attach-session -t '=SESSION'` |

The agent-facing skill contract tests exercise documented invocations through the CLI parsers and compare their
advertised surface with `parsedCommandRegistry`; status state names come from `status.States()`, so those two
inventories are also deliberate contract-test seams.

Each unit is independently testable against the fake `Runner`; no unit reads terminal contents.

## 6. Key flows

### 6.1 launch

0. If `--from-template` was supplied: read and decode the template (§6.9), then compute the **union** of template and
   command line. The union — not the file, and not the flags — is what step 1 validates. Every template failure is
   exit 2 and occurs here, before step 2's preflight and before anything exists.
1. Parse and validate the complete configuration (§7). Any error → exit 2, nothing created.
2. Preflight: `tmux`, `amq`, and each *requested* harness resolve on `PATH` → else exit 7.
3. Existence check, **best-effort and advisory** (§6.7): attempt `list-sessions`; on an exact match → exit 3. On a
   tmux or parse failure, fall through to creation — `new-session` is the authoritative arbiter. A cancelled context
   propagates instead and creates nothing.
4. Resolve cwd: `--dir` if given, else invocation cwd. An explicit relative `--dir` is converted with
   `filepath.Abs` before validation, creation, or metadata stamping; this is lexical resolution only, with no symlink
   evaluation. Pass that one absolute string via `-c` on every window and record it in `@agentctl_dir`. `--dir` must
   name an existing **directory**; a path that does not exist and a path that exists as a regular file are both usage
   errors → exit 2, checked before anything is created (§7).
5. First role: `new-session`; remaining roles: `new-window` — canonical argv in §13.2 rows 2–3, where `CMD = exec amq coop exec --session S --me ROLE HARNESS [-- MODEL-ARGS EFFORT-ARGS]` (§3.2.1; the `--` separator appears only when at least one of the two is present), assembled per §12.1. Both use `-P -F` so the launcher receives session/window/pane IDs at creation and never name-matches its own windows.
6. After each window: stamp metadata in the exact order of §6.5, then capture the process baseline by polling
   `ps -o comm= -p <pane_pid>` (§13.2 row 14) — using the pid returned by the creation record (§13.2 rows 2–3), never a
   lookup — until §8's stable-pair rule accepts a non-`amq` observation, and store the result as
   `@agentctl_process`. Poll parameters are fixed by §8. Timeout does **not** fail the launch: no
   `@agentctl_process` option is set for that window, the window is left in place, nothing is killed, and the
   remaining roles continue to launch (§6.6). The role is then *unproven* — it exists, and agentctl has no evidence
   of what is running in it.
7. Any failure after the session is owned — excluding baseline-capture timeout, which is not a rollback-class
   failure (§6.6): stop, kill by the typed session ID,
   report on stderr, exit 8. Failures *before* ownership are a different case. Both are specified in §6.6.
8. After every role has been attempted, reuse the typed session ID returned by `new-session` and run the same
   roster-driven collection and human-table rendering as `agentctl status --session S`; do not re-resolve the session
   by name. This is a fresh observation, not a rendering of the launch request: a role already missing, dead, unmanaged,
   or otherwise degraded is reported in that observed state. Those rows do not change exit 0, because the fleet launch
   itself succeeded. If collection or rendering cannot complete, write
   `agentctl: session "S" launched, but post-launch status could not be confirmed: CAUSE` to stderr and still exit 0;
   the confirmation is advisory and cannot truthfully reclassify or roll back an already-successful launch. When one
   or more roles are unproven, `launch` emits the §6.6 unproven-role messages, still renders this status observation,
   and exits **9** (§9) instead of 0. A failed status confirmation remains advisory in that case too: it cannot
   reclassify the launch in either direction, so the stderr line above is unchanged and the exit code stays 0, or 9
   when a role is unproven.

### 6.2 clear / compact

1. Validate the ROLE argument's charset (§12.5). Malformed → exit 2, before any `Runner` call.
2. Resolve the session: `list-sessions`, exact name comparison in Go, address the resulting session ID thereafter
   (§13.1). Confirm `@agentctl_managed=1` **and** `@agentctl_version=1` (§12.6) → else exit 3.
3. Resolve the window: `list-windows`, exact name comparison in Go, address the resulting window ID. **No name is ever
   passed to `-t`** (§13.1). Zero matches → exit 4; more than one → exit 4, fail closed (§13.5).
4. Confirm the stored window `@agentctl_role` exactly equals the requested role → exit 4 on absence or mismatch;
   exactly one pane and the pane is alive → exit 5. This stored-role comparison is the sole window-ownership check
   before pane validation; window `@agentctl_managed` and window version metadata are not read.
5. Process identity against the recorded baseline (§8). Mismatch, empty baseline, or identity unavailable → exit 5.
   All three fail closed at the same code, but the message states which one occurred (§1.1), and the empty-baseline
   refusal names the remedy now that one exists:

   ```text
   agentctl: refusing to clear ROLE; window W has no @agentctl_process baseline; recover the role with "agentctl relaunch ROLE"
   ```
6. Self-target guard: when running inside tmux and `$TMUX_PANE` equals the resolved target pane, refuse → exit 5
   (`refusing to clear own pane`).
7. Deliver via `DeliverPayload` (§13.6) to the resolved pane ID.
8. Success means tmux accepted the keystrokes — reported as delivery, never as execution.

### 6.3 status

`status` reports objective facts and refuses as little as possible. It is the one command that renders an unmanaged
session rather than rejecting it (§12.6). This section governs one named session; §6.3.1 covers the listing, which
applies every rule here to each session it reports.

**Reads are an allowlist.** Only §13.2 rows 6 (read session option), 8 (list windows), 9 (list panes) and 14 (process
identity) may be issued. Row 7 (read window option) is permitted by the table but unused, because row 8's format string
already carries every window metadata field that is collected or consumed, not every option stamped on a window —
tests assert row 7 is **absent** from recorded calls, so an accidental per-window read loop is caught rather than
merely discouraged.

**Session-level inputs.** Every value `status` reads at session scope, in every state it can hold:

| `@agentctl_managed` | `@agentctl_version` | `@agentctl_roles` | Result |
|---|---|---|---|
| missing or ≠ `1` | absent or `1` | any | render `{"schema": 1, "session": S, "managed": false, "agents": []}`, exit 0 |
| any | present and ≠ `1` | any | exit 3 — created by a different agentctl version (§12.6) |
| `1` | **absent** | any | exit 3 — *"managed session carries no `@agentctl_version` marker"* |
| `1` | `1` | **absent** | exit 3 — *"managed session has no `@agentctl_roles` roster"* |
| `1` | `1` | present with an **empty entry** | exit 3 — naming the raw roster value |
| `1` | `1` | present, all entries non-empty | proceed to per-role enumeration |

The last three are session-state defects of the same family: the session claims management but its metadata is absent
or malformed. All fail closed, and each message states the fact that is true — an **absent** marker is not "a different
version", and saying so would assert an event that did not happen (§1.1).

The roster is comma-split, so `planner,,codex1`, a leading comma, or a trailing comma each yield an empty entry.
Rendering that as a `missing` role would have `status` assert a role that **never existed** — §1.1's first half again,
and worse than refusing, because it invents a fleet member rather than declining to describe one. `launch` cannot
produce this: role names are charset-validated to exclude commas and empty roles are rejected at parse (§7). It is
therefore purely a corruption or hand-edit detection path, which is exactly why it must fail closed rather than be
smoothed over — the only way to reach it is for something to have gone wrong already.

**Roster drives enumeration.** Roles come from `@agentctl_roles` (§6.5), not from whatever windows happen to exist. A
roster role with no exactly-matching window is `missing` — including the snapshot-then-gone race, where a window
disappears between `list-windows` and `list-panes`.

**Process-identity read failures** (row 14) split by what happened. `ErrProcessUnavailable` — the process is gone, or
`ps` reported nothing for it — is `unexpected-process`, because identity that cannot be verified is not identity
verified. Any *other* failure means the `ps` command itself failed, which is a tool failure: exit 6, carrying its
cause. A command did run, so exit 6 is available here in a way it is not for input validation (§1.1, §4.1).

**State precedence**, evaluated in this order, first match wins:

| Order | State | Condition |
|---|---|---|
| 1 | `ambiguous` | more than one window with this exact name (§13.5) |
| 2 | `unmanaged` | stored window `@agentctl_role` does not exactly equal the roster role, **or more than one pane** |
| 3 | `missing` | no window with this exact name, or the window has zero panes |
| 4 | `dead` | pane reports `pane_dead` |
| 5 | `no-baseline` | the stored `@agentctl_process` is empty |
| 6 | `unexpected-process` | observed executable ≠ stored baseline, **or** identity is unavailable for an alive pane |
| 7 | `running` | everything above passed |

No process probe (row 14) is issued once an earlier state applies — the probe is the last resort, not a precondition.

Three mappings are deliberate and easy to get backwards:

- **Multiple panes → `unmanaged`, not `ambiguous`.** The window no longer satisfies the one-pane contract a managed
  window is created with, so it is no longer ours to describe. This matches control refusing the same window.
- **Alive pane + identity unavailable → `unexpected-process`.** Identity *unverifiable* is not identity *verified*.
  Reporting `running` here would assert something unproven.
- **Empty stored baseline → `no-baseline`, not `unexpected-process`.** Both fail closed identically for control
  (§6.2 step 5, exit 5), but they are different facts and the operator's response differs. `unexpected-process` says a
  proven pane is now occupied by something else — an unexplained event, to be investigated, and `relaunch` refuses it
  (§6.8). `no-baseline` says agentctl never proved anything about this pane, usually because the launch poll timed out
  (§8) — nothing is unexplained, and `relaunch` recovers it when an abandonment record is present (§6.8). That record
  is deliberately **not** a status state of its own: every state here derives from a structural fact — window, pane,
  process — whereas `@agentctl_unproven` is advisory metadata (SECURITY.md residual 4). `relaunch` *acting* on
  advisory metadata is already how it works; `status` *asserting* it as a state would promote a forgeable option into
  a factual claim, and it is transient for a settling window, so it would flip within seconds of launch and invite
  acting on a snapshot of an operation still in progress. The distinction is reported where it is actionable — in
  `relaunch`'s refusal, which names what is missing and gives the remedy.
  Rendering both as `unexpected-process` asserted an event
  ("the process changed") that did not occur in the second case. No §13.2 row 14 probe is issued for a `no-baseline`
  row: the state is decided from metadata already in hand, so the probe-is-last-resort rule above is unchanged, and a
  `ps` tool failure cannot fail a status listing on behalf of a row whose state was already settled.

**Rendering.** `unexpected-process` shows the **currently observed** executable, not the stored baseline: the operator
needs to see what *is* running, not what was expected. Fields that were never probed render as the empty string —
`missing` → pane ID `''` and process `''`; `dead` → the real pane ID, process `''`; `no-baseline` → the real pane ID,
process `''`; `unmanaged`/`ambiguous` → the observed pane ID when trivially known, else `''`, process `''`.

Because managed windows run without `remain-on-exit`, an exited agent's window closes and normally reports `missing`,
not `dead` — documented in `--help` and the README. JSON uses the brief's versioned schema (`"schema": 1`); `state` is a
string field, so the states added here are not a schema change (§13.5). Human output is the brief's table.

### 6.3.1 status listing (bare `status`)

`status` renders a **listing** whenever no explicit `--session` was supplied. That is the whole rule: there is no
environment in which bare `status` describes one fleet and hides the rest. Everything in §6.3 continues to govern each
session in the listing; this section adds only what is true of the set.

**Why the ambient sources do not narrow it.** `AGENTCTL_SESSION` is exported into every window `launch` creates, and
the current-tmux source resolves for anyone sitting in a session, so a `status` that honoured them would answer a
different question depending on where it was run — and would answer it silently. An operator or agent inside one fleet
would be shown that fleet and given no indication that others existed, which is the completeness lie this section
already refuses for unmanaged and defective sessions, arriving by a different route. The caller's own session is a fact
worth *showing* (below), never a reason to withhold the others.

**Discovery is row 1.** Sessions are enumerated with §13.2 row 1 and its canonical format string. The listing's read
allowlist is therefore §6.3's rows 6, 8, 9 and 14 — unchanged argv, once per session — plus row 1 for discovery and row
12 for the marker below, and nothing else. Naming the whole set here means a future read has to argue with this
paragraph rather than slip in. The listing is the single-session command applied N times: no second discovery,
validation, or state path exists, so the two modes cannot drift.

**The caller's session is marked, not selected.** When the caller is inside tmux, `status` resolves the current session
with §13.2 row 12 and marks that session in the listing. The marker is derived **only** from row 12, never from
`AGENTCTL_SESSION`: row 12 observes where the caller actually is, while the environment variable is a value any process
can export and one a moved window can carry from a session it no longer belongs to (§6.5). Marking from the variable
would assert "you are here" on evidence that does not establish it.

The marker is advisory in the strict sense: it targets nothing, so nothing is at risk if it cannot be determined. A
caller outside tmux, or a row-12 read that fails, produces a listing with no marker and no complaint — and, because an
absent marker could otherwise be read as "you are in no managed session", the absence is documented as meaning only
that agentctl did not determine one.

- **Human table:** a leading column, empty in its header, carrying `*` on every row of the marked session and blank
  elsewhere. It is a column rather than a decoration on the session name so that the name field still contains exactly
  the session name.
- **JSON:** `"current": true` on the marked session's report, omitted entirely otherwise. Additive on the same footing
  as `defect` above: it introduces a key rather than altering one a consumer already reads.

**Worked examples.** One server carries every case this section defines: `epic123` managed with two roles, `shell`
unmanaged, and `future` claiming management with a version agentctl cannot interpret. The two runs differ only in where
`status` was invoked from.

Run from **inside** `epic123`:

```text
   SESSION  ROLE     HARNESS  MODEL    EFFORT  PANE  PROCESS  STATE
*  epic123  planner  claude   fable    max     %12   claude   running
*  epic123  codex1   codex    default  high    %13   codex    running
   shell                                                      unmanaged
   future                                                     session "future" was created by a different agentctl version "2"
```

```json
{"schema":1,"sessions":[{"schema":1,"session":"epic123","managed":true,"agents":[{"role":"planner","harness":"claude","model":"fable","effort":"max","window":"planner","pane_id":"%12","process":"claude","state":"running"},{"role":"codex1","harness":"codex","model":"","effort":"high","window":"codex1","pane_id":"%13","process":"codex","state":"running"}],"current":true},{"schema":1,"session":"shell","managed":false,"agents":[]},{"schema":1,"session":"future","managed":true,"agents":[],"defect":"session \"future\" was created by a different agentctl version \"2\""}]}
```

Run from **outside tmux** — same server, same sessions, no marker:

```text
  SESSION  ROLE     HARNESS  MODEL    EFFORT  PANE  PROCESS  STATE
  epic123  planner  claude   fable    max     %12   claude   running
  epic123  codex1   codex    default  high    %13   codex    running
  shell                                                      unmanaged
  future                                                     session "future" was created by a different agentctl version "2"
```

```json
{"schema":1,"sessions":[{"schema":1,"session":"epic123","managed":true,"agents":[{"role":"planner","harness":"claude","model":"fable","effort":"max","window":"planner","pane_id":"%12","process":"claude","state":"running"},{"role":"codex1","harness":"codex","model":"","effort":"high","window":"codex1","pane_id":"%13","process":"codex","state":"running"}]},{"schema":1,"session":"shell","managed":false,"agents":[]},{"schema":1,"session":"future","managed":true,"agents":[],"defect":"session \"future\" was created by a different agentctl version \"2\""}]}
```

Four things these pin that prose does not:

1. **The absence of a marker is not a claim.** The second listing is what a caller outside tmux sees, and it is also
   what an inside caller sees if the row-12 read fails. Nothing in the output distinguishes them, which is why §6.3.1
   says an absent marker means only that agentctl did not determine a current session.
2. **The marker column's width follows its content.** With nothing marked it is two spaces, with something marked it is
   three, so the whole table shifts by one column between the two runs. That is `tabwriter` doing its job, not a second
   layout.
3. **`current` is per-session and absent when false** — `shell` and `future` carry no key at all, exactly like `defect`
   on a healthy session.
4. **The defect text occupies the state cell** of an agentless row, so a long message runs past the nominal column
   width rather than being truncated. Truncating it would withhold the reason.

These were produced by the real `text/tabwriter` settings (`0, 8, 2, ' ', 0`) and the real JSON encoder rather than
transcribed by hand, so they may be pinned as fixtures. They are shown with the `EFFORT` column #101 introduces; if this
section lands first, that column is simply absent and nothing else about the rendering changes.

**Order is tmux's order.** The listing does not sort. Re-ordering would assert a ranking agentctl does not have.

**Every session on the server is rendered.** A session whose `@agentctl_managed` is missing or ≠ `1` appears as
`{"schema": 1, "session": S, "managed": false, "agents": []}` — the rendering §6.3 already defines for an unmanaged
session named directly. Omitting it would make the listing claim a completeness it does not have, and an unmanaged
session is frequently the exact fact the operator is looking for: it is what "agentctl cannot see my fleet" looks like
from the outside. `status` is the one command that describes rather than refuses (§6.3), and that does not stop being
true when it describes more than one.

**A session with no agents still occupies a row.** The human table renders one row per *agent*, so an unmanaged or
otherwise agentless session would contribute nothing and be invisible in the table while present in the JSON. It gets
one row naming the session and its session-level state, with per-agent fields empty. The table and the JSON must agree
about which sessions exist.

**One defective session does not blank the listing.** A session that claims management but carries metadata agentctl
cannot interpret — a foreign `@agentctl_version`, an absent version marker, or a malformed `@agentctl_roles` roster
(§6.3) — is rendered in place with the defect named, the listing continues, and the **command still exits 3**. Skipping
it silently would be the completeness lie above; aborting the whole listing would be its mirror image, withholding
every fact agentctl did observe because one session was unreadable. Reporting everything observed *and* failing is the
only combination that is true on both axes (§1.1): the operator gets the topology, and the exit code still says
something is wrong. Directing them to `--session` instead would be worse than useless — it asks for names that the
listing they just ran was going to supply.

**The defect is named in every rendering, not only the human one.** A defective session's agent list is empty because
agentctl could not read its roster — not because the session has no agents — so a document that carries `"agents": []`
and nothing else asserts an absence that was never observed, which is the same error as skipping the session, made
quieter. The defect therefore appears as a field on that session's report (absent on healthy sessions), and the human
table renders it in the state column of the session's row. Both renderings state what is wrong, and neither leaves the
empty agent list to be read as a finding. An exit code and a line on stderr are not a substitute: they describe the
run, while the document describes the fleet.

**JSON shape.** `{"schema": 1, "sessions": [<schema-1 session report>, ...]}`. Each element is a complete, unchanged
§6.3 document — plus the defect field above where one applies — so a consumer that already parses one session parses
these. Adding that field is not a schema change: it introduces a key rather than altering one a consumer already reads
(§13.5's precedent for the states it added). An empty server is `{"schema": 1, "sessions": []}`, not an error.

**No tmux server is still exit 6.** §6.7's scope note stands for the listing: a row-1 failure exits 6 carrying tmux's
own message, which on a machine with no server reads `no server running on <path>` and answers the question directly.
The alternative — treating any enumeration failure as "no fleets" — would render a tool failure as an observed absence,
and distinguishing the two would require the advisory-lookup stderr matching §6.7 rejects on evidence. Noted here
explicitly because
§6.7 was written when `status` addressed a fleet that must already exist, which the listing does not.

### 6.4 attach / kill

`attach`: refuse when the session is missing, unmanaged, or at a version other than `1` (§12.6) — all exit 3, with control-mode and tmux failures exit 6; detect iTerm2 via `TERM_PROGRAM=iTerm.app` and report clearly when not in iTerm2 or when control mode cannot be established; run `attach-session` in control mode (§13.2 row 13); never create sessions. A refusal names the escape hatch, so the operator is never stuck:

```text
… ; to attach anyway, run: tmux -CC attach-session -t '=SESSION'
```

That suggested command uses tmux's `=` exact-match prefix rather than an ID, and it is **not** a contradiction of §13.1: §13.1 governs argv that *agentctl* constructs, where a resolved ID is always available. The escape hatch is a human typing tmux directly, with no resolution step to draw an ID from, so `=` is the correct idiom there — and it is the operator's own decision, not ours.

**`attach` stays interactive control mode, and narrates instead of apologising.** The `Command Menu` an operator sees
(`esc`, `X`, `L`, `C`) is printed by iTerm2's tmux integration, not by agentctl, which runs row 13 and nothing else and
therefore cannot change those keys, their meaning, or their case sensitivity. Removing the menu was considered and
**rejected** (#105): native tabs exist only while a `tmux -CC` client lives, so agentctl could exit with tabs still open
only if iTerm2 owned that client — and iTerm2 renders a client it owns as a gateway window containing the same menu.
Verified against iTerm2 3.6.11: its scripting dictionary has no tmux vocabulary at all, and its only lever is "run a
shell command in a new window", so the menu would be *relocated*, not removed, at the price of a permission prompt, a
second shell-composition site (§12.1) and a dependency on a scripting dictionary we do not version. agentctl therefore
explains the menu rather than fighting it. No flag switches this off; there is one attach.

*The narration* is written once the environment and ownership gates have passed and before `attach-session`, so it
never announces an attachment a refusal prevented — a refused attach writes nothing to stdout. It names agentctl once
with `buildinfo.Current()` (the same total value printed by `agentctl version`) and does not prefix every subsequent
line. The version line is attach-specific, not a change to other commands. The block states that the session is being
attached in iTerm2 and how many windows it has; that the menu about to appear is iTerm2's; that `esc` detaches, ending
the client and whatever iTerm2 was rendering, while the fleet keeps running; that `X` is uppercase and force-quits;
and that only `kill` stops a fleet:

```text
agentctl 0.2.0
Attaching session "epic123" (2 windows) in iTerm2.

iTerm2 will now show its Command Menu. That menu is iTerm2's, not agentctl's:

  esc   detach cleanly — the tabs close and the fleet keeps running
  X     (uppercase) force-quit — the fleet keeps running, but the tmux client
        does not exit, so this terminal stays busy and agentctl cannot report.
        Prefer esc.

Detaching never stops the fleet. To stop it: agentctl kill --session epic123
```

It deliberately contains no success claim: `attach-session` has not run yet. It also does **not** say what
force-quitting leaves behind — that is iTerm2's path, unobserved here, and §1.1 forbids asserting an outcome we have
not measured however confident the inference. It never asserts that windows *are* rendered as native tabs either:
that depends on an iTerm2 preference agentctl can neither set nor read.

*The window count* is the one new fact the narration carries, read once after the ownership gate with §13.2 row 8. That
completes attach's read set: row 6 for the ownership gate, row 8 for the count, row 13 to attach, row 1 for the
post-exit probe — no other read, and no new argv shape. A failed row-8 read **omits the count and says nothing else
about it**; a guessed or defaulted number would be a claim about a fleet agentctl did not manage to observe. Only the
second line changes in that case: `Attaching session "epic123" in iTerm2.`

*The state report* is one line written **if and when the control-mode client exits**, naming what agentctl observed:
the session is still running, is no longer present, or its state could not be verified and why. Presence is decided by
comparing the resolved session **ID**, never the name, so a session recreated under the same name is not reported as
the one that was attached. The probe reuses row 1 and adds no argv shape. The line states the observation and nothing
else — any suggested command belongs in the block below, so a fact and an instruction are never mixed in one sentence.

**The report is best-effort, and one verified path never reaches it.** `attach` blocks on the control-mode client, so
everything after it — probe, state line, next-steps block — is contingent on that client exiting. Force-quitting from
iTerm2's menu does **not** end it (§3.4), so on that path `attach` never returns and prints nothing further. The spec
says so rather than describing a report that cannot happen: a contract that quietly omits its one unreachable case is
the same failure as a message that overclaims.

**No timeout, deliberately.** Bounding the wait would require agentctl to decide when an attachment has gone on too
long, and it cannot distinguish a wedged client from an operator who is simply still working — the two are identical
from outside. A deadline would therefore be a guess dressed as a policy, and detaching a live session on that guess is
worse than the hang it prevents. The wedge is documented, including its recovery, and left to the operator.

*The next-steps block* follows the state line and its contents are governed by the state observed, because a suggestion
is itself a claim that the suggested action is available:

| Observed state | Block |
|---|---|
| still running | re-attach, check status, and stop it — all three, each copy-pasteable and naming the session as the operator typed it |
| not verifiable | check status only: agentctl does not know whether there is anything to re-attach or to stop |
| no longer present | no block at all: proposing actions on a session observed to be absent would assert it is still there |

The exact rendered variants for session `epic123`, resolved as `$4`, are:

```text
Attachment to session "epic123" ended (tmux exit 0). Session $4 is still running.

  re-attach:     agentctl attach --session epic123
  check status:  agentctl status --session epic123
  stop it:       agentctl kill --session epic123
```

```text
Attachment to session "epic123" ended (tmux exit 0). Could not verify whether session $4 is still running: CAUSE

  check status:  agentctl status --session epic123
```

```text
Attachment to session "epic123" ended (tmux exit 0). Session $4 is no longer present.
```

The report says `Attachment ... ended`, not `Detached`: control mode ending does not establish how it ended. The state
appears once, and the optional block contains commands only.

*The probe is advisory.* The exit code is unchanged in all three outcomes, because what succeeded — the attachment —
succeeded regardless of what the probe could see afterwards, and a probe failure is reported as an unverified state
rather than an absence. One consequence is worth stating rather than leaving to be discovered: killing the attached
session when it is the last one takes the tmux server with it, so row 1 fails and the operator gets the unverified form
carrying tmux's own reason. That is the §6.7 advisory-lookup trade again — separating the two would need the
no-server stderr matching this design rejected on evidence — and `TmuxError` still surfaces tmux's
`no server running` text, so the fact reaches the operator even though agentctl declines to classify it.

`kill`: the full §12.6 gate — `@agentctl_managed=1` **and** `@agentctl_version=1`, anything else exit 3 — then
`kill-session` (§13.2 row 11). Both address the resolved session ID, never a name (§13.1).

### 6.5 Metadata

Exactly as the brief, plus three additions: session options `@agentctl_managed=1`, `@agentctl_version=1`,
`@agentctl_roles`, `@agentctl_fleet`, `@agentctl_dir`; window options `@agentctl_managed=1`, `@agentctl_role`, `@agentctl_harness`, `@agentctl_model`, `@agentctl_effort` (the model and effort are empty strings when defaulted — always set, never omitted, so exact fleet comparison is a straight read), plus `@agentctl_process` (the launch-time observed executable, §8). No metadata database.

Stamping order is **fixed and asserted**, because the fake `Runner` records calls in sequence and a reordering would
otherwise pass silently. It orders the **option-setting calls**, not creation: `new-session` creates the session *and*
its first window together, so the first window necessarily exists before any option can be set. "Session options first"
means before any *window option*, never before any window.

1. session `@agentctl_managed`
2. session `@agentctl_version`
3. session `@agentctl_roles`
4. session `@agentctl_fleet`
5. session `@agentctl_dir`
6. then, per window, in this order: `@agentctl_managed`, `@agentctl_role`, `@agentctl_harness`, `@agentctl_model`,
   `@agentctl_effort`, the baseline poll, and finally `@agentctl_process`.

When the baseline poll times out (§8), the final `@agentctl_process` call is **not issued**. The option is left unset,
never set to an empty string: the two are indistinguishable on read (§13.3), so the distinction exists only in the
recorded call sequence, and that is where it is asserted. Every preceding window option is stamped in the same order —
the timeout replaces the last element of the sequence and reorders nothing.

**In its place, the timeout branch stamps `@agentctl_unproven=1`** as that role's final per-window call. This is the
*abandonment record*: launch completed its attempt on this role, gave up, and left the window standing. It exists
because recoverability must be a **positive observation, not an absence**.

The absence is not usable evidence, and the reason is timing. Between the option loop and the `@agentctl_process`
call, a window is fully stamped with an empty baseline for as long as the poll runs — up to §8's 5s. That state is
byte-identical to a retained post-timeout window, so a concurrent `relaunch` reading metadata alone cannot tell a role
that was abandoned from one that is still settling. Acting on the absence lets it destroy a window mid-launch, and the
consequence is not symmetric with the harm it was meant to fix: if the kill lands after the stable pair but before the
`@agentctl_process` call, that stamp fails as an ordinary option-stamping failure, which is *not* the §6.6 timeout
carve-out — so `launch` rolls back the whole session and destroys the peer roles the carve-out exists to protect. A
kill during the poll is milder but still false: `launch` reaches its deadline and reports that the window "was left in
place" when it no longer exists (§1.1).

This is §1.1 applied to a precondition rather than to an output: **never act on the absence of evidence when presence
is obtainable.** A record of a decision that *completed* is what makes the state readable; a flag asserting work is
*in progress* would not, because an interrupted launch leaves a stale one indistinguishable from a live one, moving
the ambiguity instead of closing it.

**A failed marker stamp is reported, never fatal, and never rolled back**, exactly as §13.2 row 5a's failed clear is,
and for the same reason: rolling back a fleet because an advisory stamp failed would be the harm §6.6's carve-out
removes, one level further out. The role is then unproven *and* unmarked, so §6.8 cannot recover it, and §6.6's
per-role line says so rather than leaving the operator to discover it when `relaunch` refuses.

`relaunch` stamps no marker on its own creations: its settle-timeout rolls back the window it created (§8), so no
abandoned window survives the invocation to be recovered.

The stamped window `@agentctl_managed` marker is advisory metadata and remains part of that fixed stamping contract;
it is not consumed as a window-ownership gate. The stored `@agentctl_role` is the sole window-ownership evidence.
Agentctl writes it only during per-window stamping and never during session stamping, so a handmade window cannot
inherit it from the session by construction.

`@agentctl_process` is last by construction: it is the only value that cannot be known before the window is running.
After the first role's per-window stamping concludes — **whether or not a baseline was accepted** — `launch` clears the
three identity variables from the session environment with §13.2 row 5a, in declaration order, before it creates a
later role window. The clear exists to stop a *hand-made* window inheriting stale identity (§13.2 row 5a note), a
hazard entirely independent of whether the first role settled, so an unproven first role must not suppress it.

**`@agentctl_fleet`** records the per-role configuration the roster alone cannot carry: `role:harness:model:effort` quads,
comma-joined, in roster order. Every field is validated (§7), so neither `:` nor `,` can occur inside a field; a
defaulted model or effort renders as an empty field (`planner:claude::`). Parsing is `strings.Split` on `,` then
`strings.SplitN(entry, ":", 4)`. It never extends `@agentctl_roles`: the roster's meaning — the declared membership —
is unchanged, and a consumer reading only names must keep working.

It exists because `@agentctl_harness`, `@agentctl_model` and `@agentctl_effort` are **window** options: when a role's
window closes they close with it, so nothing surviving records what a missing role was launched with. Without it, relaunching one role
(§6.8) could only ask the caller, and exact fleet comparison (#17, §14) is not correctly implementable at all — a
missing role's configuration would be unknowable.

**`@agentctl_dir`** records the exact absolute string passed to `-c` at launch. An explicit relative `--dir` is first
resolved lexically with `filepath.Abs`; an omitted flag uses the absolute invocation cwd. Neither path is resolved
through symlinks. It is alone in its own option because, unlike every other metadata field, its value is unconstrained
(§13.3's rule that unconstrained values must not share a delimited field). §13.4 is untouched: `pane_current_path` is
still never read, and the recorded string is what agentctl passed, not what tmux resolved.

**No `@agentctl_version` bump.** The quad is the v1 `@agentctl_fleet` schema; its earlier triple form existed only while
the unreleased relaunch work was staged. Sessions launched before `@agentctl_fleet` and `@agentctl_dir` exist carry
neither and are handled as the legacy case in §6.8 rather than being silently guessed at. A development session carrying
the superseded triple is structurally invalid and refused, which is preferable to silently defaulting its missing effort.

**Stored configuration always equals the actual fleet.** Whenever a command changes a role's harness, model or effort, it
rewrites `@agentctl_fleet` in full with the new values, so the option can never disagree with the live windows.

When `--from-template` supplied the fleet, "declaration order" means the union's pinned order (§6.9): template roles in
file order, then flag-added roles in `--roles` order. Everything else in this section is unchanged — the stamping
sequence does not vary by where a value came from.

**`@agentctl_roles`** is the declared roster: the role names from `--roles`, comma-joined, in declaration order. It
exists because without it a dead agent is *unobservable*. Every other status input is derived from windows that still
exist, so a role whose window has closed leaves no trace to report — and since managed windows run without
`remain-on-exit` (§6.3), a crashed agent's window closes. The roster is what lets `status` say `missing` instead of
silently omitting the role, which is the difference between status doing its job and status being actively misleading.
It uses the same tmux-option mechanism as every other field, so "no metadata database" still holds. `launch` stamps it
once, immediately after `@agentctl_version` — it is known from the validated config before any window exists, but the
session must exist to hold it, so it lands with the other session options rather than earlier.

### 6.6 Launch failure, ownership, and exact messages

This section is §1.2 applied to `launch`. Rollback is gated on **ownership**, and ownership begins at exactly one
instant: when `new-session` returns output that
parses into a session ID. Before that, agentctl owns nothing and destroys nothing. After it, the typed ID is the only
thing rollback ever targets — a session is never killed by name (§13.1).

**Baseline-poll timeout is not a launch failure.** It is carved out of everything below. On timeout for a role: set no
`@agentctl_process` for that window, kill nothing, and continue with the remaining roles. Every other failure class —
window creation, option stamping, creation-output parsing, and anything else after ownership — keeps the whole-session
rollback in this section unchanged.

The carve-out exists because the timeout's blast radius was inverted relative to its cause. Settling is slowest exactly
when the host is most loaded, which is when the fleet is largest (SECURITY.md residual 1 measures 18 concurrent workers
as a real operating condition), so the previous rule destroyed the largest fleets at their most likely failure moment
because one role settled slowly. Containment does not require destruction: the target chain already refuses every
control command against an empty `@agentctl_process` (§6.2 step 5, §8), so an unproven role is already inert.

`launch` writes one line per unproven role to stderr, at the point the deadline is reached:

```text
agentctl: ROLE: no process baseline recorded; pane P did not yield two consecutive identical non-amq observations within 5s (OBSERVATION); window W was left in place
```

`OBSERVATION` reports what the poll actually saw at its final attempt, in one of exactly three forms, matching §8's
three terminal conditions:

```text
last observed: "2.1.222", not repeated
last observed: "amq"; the exec chain had not advanced past amq
no identity was available for the pane's root process
```

It is knowable at the moment of timeout and costs no additional §13.2 row 14 call. It exists because the three
conditions call for different operator responses — a flapping non-`amq` value means the harness is starting and the
host is slow, a persistent `amq` means the exec chain is stuck, and an unavailable identity means the pane's root
process could not be read at all — and a message reporting only "did not settle" would withhold the one fact that
distinguishes them (§1.1).

After every role has been attempted, `launch` writes exactly one summary line, listing the unproven roles in roster
order:

```text
agentctl: session "S" launched; 1 of 4 roles unproven: planner; nothing was rolled back; control commands refuse an unproven role; "agentctl relaunch ROLE" recovers planner
```

The trailing remedy is **two conditional clauses, not a fixed sentence**, because a role whose abandonment record could
not be stamped has no `relaunch` remedy (§6.5) and a summary asserting one would be false for exactly that role. After
the fixed prefix, emit each clause only when its own set is non-empty:

- roles carrying an abandonment record → `; "agentctl relaunch ROLE" recovers LIST`
- roles without one → `; no abandonment record was stamped for LIST, which can only be recovered by recreating the fleet`

So a mixed launch reports both, naming which roles fall where:

```text
agentctl: session "S" launched; 2 of 4 roles unproven: planner, worker; nothing was rolled back; control commands refuse an unproven role; "agentctl relaunch ROLE" recovers planner; no abandonment record was stamped for worker, which can only be recovered by recreating the fleet
```

and a launch where every marker stamp failed reports only the second clause. This keeps the summary inside its own
remit: §6.6 already holds that the per-role line is the observation and the summary is the claim about the fleet, with
neither implying the other — a fixed remedy clause quietly broke that by asserting a per-role fact the per-role line
had just contradicted.

If the abandonment record (§6.5) could not be stamped for a role, its per-role line says so, because that role cannot
be recovered by `relaunch` (§6.8) and the operator would otherwise learn it only on being refused:

```text
agentctl: ROLE: no process baseline recorded; pane P did not yield two consecutive identical non-amq observations within 5s (OBSERVATION); window W was left in place, but its abandonment record could not be stamped (CAUSE), so "agentctl relaunch ROLE" cannot recover it
```

The count phrasing takes no plural form — `1 of 4 roles unproven` and `3 of 4 roles unproven` are the same shape — so
no pluralization rule is needed. Both lines are required: the per-role line is the observation, the summary is the
claim about the fleet, and neither implies the other. The exit code is **9** (§9), and it applies whether one role or
every role is unproven; the session is retained in both cases.

**Failure after ownership → exit 8.** Stop, kill the typed session ID, and report exactly one of:

```text
agentctl: failed to launch ROLE; removed incomplete session S: CAUSE
agentctl: failed to launch ROLE; failed to remove incomplete session S: CLEANUP_CAUSE (launch failure: CAUSE)
```

Both exit 8. A failed cleanup does not become a different class of failure; it becomes a more informative message. The
launch is never retried, and cleanup is never attempted by name.

**Every launch-failure message names what failed** (§1.1). `CAUSE` is the error that stopped the launch — a baseline-poll
timeout, an option-stamping failure, a window-creation failure — and it appears in *both* variants. Without it the
three are indistinguishable, and the launch cause is precisely the one the operator can no longer investigate, because
rollback destroyed the session that held the evidence. The pre-ownership message below already leads with its cause, so
all three §6.6 outcomes state what went wrong; a message that reports only *that* a launch failed is not an acceptable
shape here.

**Failure before ownership → exit 6.** If `new-session` returns output that cannot be parsed into a session ID, that is
a tmux failure, not a launch rollback: no typed ID means no ownership, and no ownership means no destruction. Kill
nothing. But tmux may have created a session regardless, so the operator must be told, and the exit-6 message appends:

```text
… ; a session named S may exist; inspect with tmux ls
```

The asymmetry is deliberate. Leaking a session the operator can see and remove is strictly better than destroying one
agentctl cannot prove it created: fail-safe beats leak-free.

### 6.7 The existence check must work with no tmux server

`launch`'s existence check cannot be authoritative, because on a machine with no tmux server there is nothing to ask.
`list-sessions` exits 1 when no server is running, so a first-ever launch on a fresh machine failed at the check and the
fleet could never be created at all. The check is therefore **advisory**: it catches the common case early and cheaply,
and any failure falls through to `new-session`, which decides.

**Context cancellation is not a failure to fall through on.** "Any failure" here means a tmux or parse failure. A
cancelled or expired context is control flow, not a verdict about the session: it propagates as the exact sentinel
(§4.1) and issues **zero** creation calls. Falling through would have agentctl create a fleet after its caller asked it
to stop — the one outcome a cancelled launch must never produce.

| `list-sessions` | Session present | Outcome |
|---|---|---|
| succeeds | no | create → success |
| succeeds | yes | **exit 3**, caught by the advisory check |
| fails (no server) | no — there cannot be one | create → success; `new-session` starts server and session atomically |
| fails (tmux/parse) | yes | `new-session` refuses atomically → **exit 3** carrying tmux's own `duplicate session: NAME` |
| context cancelled | — | propagate the sentinel; **no creation call** |
| — | — | any other creation failure → exit 6 (§6.6 pre-ownership: nothing killed) |

**The same condition always produces exit 3**, whether the advisory check observes it or tmux atomically refuses the
create after a race. The messages remain evidence-sensitive under §1.1: the pre-check path says
`session "NAME" already exists`, while the raced path carries tmux's factual
`tmux create session: exit status 1: duplicate session: NAME` rather than replacing it with an event agentctl did not
observe.

The raced classification is deliberately narrow. It applies only to the `new-session` error path, only when the
wrapped process failure is an `*exec.ExitError` with exit code 1, and only when captured stderr is exactly the single
line `duplicate session: NAME` for the validated requested name (with no terminator or one terminal LF/CRLF). Any
different status, prefix, suffix, extra line, error type, or tmux wording remains the ordinary exit-6 creation failure.
This makes a future tmux wording change degrade toward the honest unclassified-tmux outcome rather than swallowing an
unrelated failure. Neither duplicate path rolls back: `new-session` returned no typed session ID, so agentctl owns
nothing.

Verified on tmux 3.7b (2026-08-01):

- `new-session -d … -P -F …` against **no server** exits 0 and returns its IDs intact — server and session are created
  atomically, so there is no window in which a server exists without the session.
- A duplicate name is refused with exit 1 and `duplicate session: NAME`, and **nothing is created or destroyed**:
  session and window lists are byte-identical before and after.

**Why `start-server` was rejected.** The obvious fix — start a server, then check — does not work. `start-server`
returns 0, but `exit-empty` defaults on, so a server with no sessions exits immediately and the very next
`list-sessions` still fails. Keeping it alive requires `set-option -g exit-empty off`, which mutates a global server
option that outlives the command and leaves an empty server running indefinitely. Neither is acceptable for an
existence check.

**Rejected: chaining `start-server` into the check.** `tmux start-server ; list-sessions -F …` as a single
invocation, with `;` as its own argv element, is **verified working** on tmux 3.7b: exit 0 with empty output against no
server, exit 0 with the session list when one exists, idempotent on repeat. It was rejected because it converges on the
same design without simplifying it — a check-to-create race still needs the identical duplicate-at-`new-session`
fall-through, so the chained form buys only rare corners while adding a novel chained-argv row and a server-lifetime
subtlety to reason about. Recorded rather than omitted because it works, and the next person to reach for it deserves
the evidence and the reason.

**Why the advisory lookup still uses no stderr matching.** The two no-server states emit *different* messages —
`error connecting to <path> (No such file or directory)` when the socket is absent, and
`no server running on <path>` once a server has exited. A string match would have to know both, and neither is a
documented contract. Falling through on *any* failure requires knowledge of neither. This is separate from the bounded
`new-session` duplicate classification above, which relies on the exact tmux 3.7b contract already verified in this
section and fails closed to exit 6 on every near-match.

**Scope.** This applies to `launch` alone. `status`, `kill` and `attach` operate on a fleet that must already exist, so
"no server" genuinely means "nothing to act on", and §4.1's exit 6 carrying tmux's message remains correct for them.
Only a command that is about to create a server has standing to proceed without one.

### 6.8 relaunch

`agentctl relaunch ROLE [--session S] [--harness H] [--model M] [--effort E] [--dir PATH]` recreates **one absent role
window, or one present role window that carries no process baseline,**
inside an existing managed session. ROLE is positional, mirroring `clear`/`compact`; duplicate options are rejected
(§12.9) and every supplied value is validated per §7 before anything runs (violations exit 2).

Stored configuration is authoritative and explicit options override it per field. Both halves matter: reading the
fleet's own record is what makes relaunch correct without the caller remembering how the fleet was launched, and
reporting the provenance of every field is what keeps an override from silently diverging the fleet (§1.1).

1. ROLE charset → exit 2. Resolve the session by listing and address it by ID (§13.1); the §12.6 gate → exit 3.
2. Read `@agentctl_roles`, `@agentctl_fleet`, `@agentctl_dir`. ROLE not in the roster → exit 4. A roster defect (empty
   entry) → exit 3, the same family as §6.3's. `@agentctl_fleet` and `@agentctl_dir` both present → **stored** mode;
   both absent → **legacy** mode; exactly one present, a structurally invalid `@agentctl_fleet`, or fleet roles that
   differ from the roster in names or order → a metadata defect, exit 3, rendering the values observed (§1.1). Stored
   values are re-validated against §7 on read, because tmux options are advisory (SECURITY.md residual 4). A stored
   `@agentctl_dir` is also required to be absolute before it can supply the effective directory. A relative value from
   a pre-fix session is refused as a metadata defect (exit 3), naming that value verbatim and directing the caller to
   an explicit `--dir`; no trustworthy launch-time base remains against which agentctl could resolve it.
3. **Stored mode**: the role's harness, model, effort and directory come from the options; `--harness`, `--model`, `--effort` and `--dir`
   each override their own field. An explicit `--dir` may override an unusable or legacy-relative stored directory;
   as with every directory override, `@agentctl_dir` remains unchanged and the divergence is reported. **Legacy mode**:
   refuse (exit 3) unless `--harness` *and* `--dir` are supplied
   (`--model` and `--effort` are optional; absent means empty, the harness defaults). The directory is **never** defaulted to the
   invocation cwd — relaunching one role somewhere the rest of the fleet does not live is exactly the silent
   divergence this refusal exists to prevent.
4. Window precondition. **Zero** windows match ROLE → proceed. **Exactly one** window matches, its state by §6.3
   precedence is `no-baseline`, **it carries `@agentctl_unproven=1`**, **and the session contains at least one other
   window** → this is the **recovery case**: record that window ID and continue. No destruction happens at this step. Any other observation → exit 4,
   rendering the observed state by §6.3 precedence and every matching window ID (§13.5).

   **The sole-window case is refused, not recovered.** Killing a session's only window destroys the session (§8), so a
   recovery kill there would take the whole fleet with it and then have nothing to create into. §1.2 decides this
   directly: an invocation destroys only what it created, and the session predates the invocation. Refusing costs the
   operator nothing that recovery was designed to protect — recovery exists to spare *peer* roles, and a one-role
   session has none.

   The check is made here, from the window list step 4 already holds, rather than at the kill: refusing before
   preflight keeps step 5a's kill the first and only destructive act, and costs one comparison. It is a count of the
   **session's** windows, not of windows matching ROLE — any surviving window keeps the session alive, roster member
   or not.

   The refusal names both remedy commands, reconstructed from the metadata already read and validated in step 2, so
   the operator is not left to reassemble a launch line from a fleet that is about to be destroyed:

   ```text
   agentctl: refusing to relaunch planner; it is the only window in session alpha, so removing it would destroy the session. Recreate the fleet instead:
     agentctl kill --session alpha
     agentctl launch --session alpha --roles planner:claude --models planner:opus-4-1 --efforts planner:high --dir /srv/work
   ```

   Flags with no stored value are omitted rather than rendered empty — `--models role:` is a usage error (§7), so
   emitting it would print a command that cannot run. In legacy mode the line is built from the effective values
   including supplied flags; if any field cannot be reconstructed, the command block is omitted entirely and the
   refusal says so, because a launch line that would create a *different* fleet is worse than no line at all (§1.1).

   Exit **4**, with every other present-window refusal in this step. Not exit 3: the session is not defective, it is
   single-role, and exit 3 would assert a state error that did not occur.

   **A `no-baseline` window without the abandonment record is refused too**, and for a stronger reason than the
   sole-window case: agentctl holds no evidence that anything finished with it. It may be settling right now inside a
   concurrent `launch` (§6.5), or it may be the husk of a launch that was killed mid-poll. Those are indistinguishable
   from metadata, and only one of them is safe to destroy, so both are refused. The message names what is missing
   rather than restating the state:

   ```text
   agentctl: refusing to relaunch planner; window @7 has no process baseline and no abandonment record, so agentctl cannot tell an abandoned role from one still starting. If no launch is in progress, recreate the fleet:
     agentctl kill --session alpha
     agentctl launch --session alpha --roles planner:claude --models planner:opus-4-1 --efforts planner:high --dir /srv/work
   ```

   The remedy is reconstructed exactly as the sole-window refusal reconstructs it, and under the same rules — valueless
   flags omitted, the whole block dropped rather than rendered wrong if a field cannot be recovered. The two
   irrecoverable cases deliberately share one shape and one remedy instead of being two separate dead ends.

   A window carrying the marker **and** a valid baseline is not recoverable either: a present baseline means the
   ordinary verification path applies, so it never reaches `no-baseline` in §6.3's precedence. No extra rule is
   needed, and fail-closed is the right default for a combination agentctl never writes.

   Sessions launched by a pre-0.4.0 binary carry no marker, which is correct rather than a migration gap: those
   launches rolled back on timeout, so an empty baseline there can only come from tampering, and tampering must not
   be recoverable.

   The precedence ordering does the discrimination, and three of its consequences are load-bearing:

   - **`ambiguous` is never recovered.** Two windows sharing the role name refuse at exit 4 exactly as before.
     Recovery requires exactly one match, so agentctl never chooses which of two windows to destroy.
   - **`dead` is still refused**, including a dead pane carrying no baseline: `dead` precedes `no-baseline` in §6.3,
     and a dead window's pane may still hold evidence, so relaunch never kills and recreates it.
   - **`unmanaged` and a zero-pane window are still refused.** A role-metadata mismatch or a pane count other than one
     means the window is not ours to describe, let alone destroy; and creating beside a zero-pane window would
     manufacture the `ambiguous` state §13.5 fails closed on.

   The recovery power is bounded to the never-proven case on purpose. A window with a *recorded* baseline that
   mismatches the live process stays refused and investigated: a recorded baseline is proof agentctl once observed a
   settled process in that pane, so a mismatch is an unexplained event and the pane is its only artifact. Recycling it
   would also reintroduce heal-on-verify — the operator's remedy for "this role is refused" would become a command
   that destroys the mismatch and mints a fresh baseline — which §8 rejects permanently. A `no-baseline` window holds
   no such evidence: agentctl never proved anything about it, and the fail-closed chain already makes it
   uncontrollable.
5. Preflight `tmux`, `amq` and the effective harness on `PATH` → exit 7. The effective directory must exist and be a
   directory: supplied by `--dir` → exit 2 (parity with launch, §7); read from metadata → exit 3, naming the stored
   path and `--dir` as the remedy, because a stale recorded path is a session-state fact, not a usage error.
5a. **Recovery kill (recovery case only).** Issue §13.2 row 11a against the exact window ID recorded in step 4. This is
   the first and only destructive act, and its position is deliberate: every non-destructive check — session gate,
   metadata validation, roster membership, window classification, the sole-window precondition, `PATH` preflight, and
   the effective-directory check — has already passed, so agentctl never destroys a window and then discovers it
   cannot create the replacement. Step 4's sole-window refusal removes the case agentctl can *observe*, so step 6 has
   a session to create into in every state agentctl saw.

   It does not guarantee more than that, and the difference matters. This is a check-then-act gate like every other
   one in the design (SECURITY.md residual 6): a peer window can close between step 4's count and this kill, and a
   *benign* agent exit is enough, because managed windows run without `remain-on-exit` (§6.3). The target can
   therefore become the session's last window after the check passed, in which case this kill destroys the session
   and step 6's creation fails. The outcome is bounded and reported rather than prevented — the removal fact and the
   creation failure both appear (step 9), so the operator is told what happened to a fleet that is gone. tmux offers
   no conditional "kill unless last", so re-counting immediately before the kill would narrow the interval without
   closing it, and is not required.

   A failed kill is a tmux operation failure, not a rollback, because this invocation has created nothing to roll back:

   ```text
   agentctl: failed to relaunch ROLE; could not remove unproven window W in S: CAUSE; nothing was created
   ```

   exit **6**. There is no retry, and no fallback to creating a second window beside the survivor — that would
   manufacture the `ambiguous` state §13.5 fails closed on.

   **No post-kill re-verification is added.** Step 7's post-create verification already fails closed on any survivor:
   it requires ROLE to resolve to exactly the window this invocation created and rolls back otherwise. A duplicate
   check between kill and create would be redundant and would widen the race window rather than narrow it.
6. Create with §13.2 row 3 exactly: `CMD` per §12.1 through the same `shellq` path, `-c DIR`, `-P -F`, and **no index
   argument** — the window lands at the next free index. Operator-visible ordering is unaffected because `status`
   iterates the roster, not the window list. Unparseable creation output is pre-ownership: exit 6, kill nothing, and
   append `; a window named ROLE may exist; inspect with tmux list-windows`.
7. Immediately after parsing the created window ID, list windows again with §13.2 row 8 against the exact session ID.
   Continue only when ROLE has exactly one match and that match is the just-created ID. Any other observation is a
   concurrent conflict: use step 9's rollback on the created ID and refuse with exit 8, naming every role-window ID
   observed. Zero matches and one match with a different ID also refuse; the message pluralizes `window` by count and
   omits the parenthesized ID list only when none were observed. A contender that sees another same-name window therefore cannot stamp or report
   success. Two contenders may both observe the conflict and roll back, leaving the role absent; this is factual and
   retryable, unlike an ambiguous fleet or false success. For the two-window race, the cleanup-success form is:

   ```text
   agentctl: refusing to relaunch ROLE; post-create verification observed role ROLE in 2 windows in SESSION (W1, W2); expected only created window W2; removed window W2
   ```

   The cleanup-failure form replaces the final clause with `failed to remove window W: CLEANUP_CAUSE`. Both exit 8.
8. Stamp the window options in §6.5's per-window order, poll the baseline with §8's fixed parameters, and stamp
   `@agentctl_process`. If `--harness`, `--model` or `--effort` overrode a stored value, rewrite `@agentctl_fleet` in full
   afterwards. A `--dir` override does **not** rewrite `@agentctl_dir`: that option records the fleet's launch
   directory and the other roles still live there. The divergence is stated in the output instead.
9. Any failure after the new window's ID parses → `kill-window` on **that ID only**, exit 8:

   ```text
   agentctl: failed to relaunch ROLE; removed window W: CAUSE
   agentctl: failed to relaunch ROLE; failed to remove window W: CLEANUP_CAUSE (relaunch failure: CAUSE)
   ```

   Same shape and same rules as §6.6, including that `CAUSE` is mandatory in both variants. Exit 8's meaning therefore
   extends from "the session this invocation created was removed" to "**what this invocation created** was removed" —
   §1.2's rule stated as an exit code.

   Where a recovery kill was performed in step 5a, every terminal message of the invocation — success, post-create
   conflict, and rollback alike — also states that window W was removed. agentctl destroyed it, so §1.1 requires the
   fact to survive whatever happens afterwards. A rollback following a recovery therefore reports both removals, and
   leaves the role absent and retryable.
10. Success (exit 0) states the role, harness, model and effort (`""` in metadata, `default` in human output, §12.7), session,
   window ID, pane ID, directory, and the provenance of each configuration field — `stored`, `flag override`, or
   `flags`. This report is what keeps an override honest: `status` cannot detect a harness swap, so relaunch says so.
   In the recovery case it adds one line naming what was destroyed:

   ```text
   recovered: removed window W, which carried no @agentctl_process baseline, and recreated ROLE
   ```

### 6.9 launch templates

`agentctl launch --from-template FILE` supplies the **fleet shape** — roles with harness, model, effort, and an
optional directory — from a JSON file. It is a source of values, never a second validator: `internal/config` continues
to own all value semantics (§12.9).

**The template never carries the session name.** Session identity is per-invocation, so one template serves
`release_0_4_0`, `release_0_5_0` and every successor unedited, and identity can never come from a stale file. There is
no `session` key, and because a bare `unknown field "session"` would under-explain a mistake people will reasonably
make, it is refused by name:

```text
agentctl: template FILE: "session" is not a template field; session identity is supplied per invocation with --session
```

**Format is JSON**, because the standard library has no YAML or TOML decoder (CLAUDE.md's hard constraint) and
`encoding/json` is the only stdlib decoder offering unknown-field rejection, a token stream for a duplicate-key pass,
and trailing-document detection.

```json
{
  "version": 1,
  "dir": "/srv/work",
  "roles": [
    { "role": "planner",  "harness": "claude", "model": "opus-4-1", "effort": "high" },
    { "role": "reviewer", "harness": "claude", "effort": "max" },
    { "role": "worker",   "harness": "codex" }
  ]
}
```

#### The union, and where each requirement binds

Flags and the template compose: **the effective fleet is the union of the two.** A flag value for a
template-declared role or field overrides it; a flag role the template does not declare is added. No flag removes a
template-declared role — removal was considered and not granted, so a template is a floor, never a ceiling.

Partial templates are legal by construction, because validation applies to the union rather than to the file. That
splits the requirements in two, and the split is the whole point:

| Requirement | Binds on | Why |
|---|---|---|
| `version` | the **file** | It describes the file's own format, so no flag could supply it. |
| `roles[].role` | the **file**, per entry | It is the merge key. An entry with no role name has no identity and cannot participate in a union. Structural, not a value rule. |
| a harness for every role | the **union** | `{"role": "planner"}` plus `--roles planner:claude` is a legal union. |
| at least one role | the **union** | A file with `roles` absent or `[]` is a legal defaults-only template; the error is a *union* with no roles. |

The last row reads oddly at a glance and is deliberate: rejecting an empty `roles` in the file would have the design
second-guess a union that is legal by construction.

It is also the only thing that relaxes `--roles`. Without `--from-template`, `--roles` is required (§4); with one, it
becomes optional because the template can supply the roster instead. What is never optional is the union: whichever
combination of file and flags produced it, a launch whose union declares no roles is a usage error, refused before
anything is created.

**Uniqueness binds per source, and merges across them.** Two `planner` entries inside `roles[]` is an error; two inside
`--roles` is the existing CLI error; `planner` in both is an override, not a collision.

**Union role order is pinned**, because §6.5's stamping order is a tested contract and would otherwise be asserted
against something undefined: every template role in file order, then every flag-added role in `--roles` declaration
order. An override never moves a role — it changes that role's fields and leaves its position alone.

#### Strictness, in decode order

1. **Open the file, then verify the handle** — `os.Open`, then `Stat` on the descriptor. Never `Stat` the path and then
   read the path: checking one object and reading another is a time-of-check/time-of-use gap, and checking the
   descriptor that will actually be read has none.
2. **Regular files only.** A directory, FIFO, device or socket is refused, naming what it is. A template is a regular
   file: `-` is not special and stdin is not accepted, deliberately rather than by omission. A streamed template cannot
   be re-read to quote the offending line in an error, and a FIFO would hang `launch` indefinitely — nothing in this
   design carries a read timeout.
3. **Symlinks are followed, and every rule binds on the target.** Refusing them would break ordinary arrangements — a
   templates directory kept in a dotfile repository — for no threat-model gain, since a same-user process that can
   plant a symlink can plant the file itself.
4. **Size cap, enforced by rejection.** Read through a limit of 1 MiB plus one byte, and refuse a file that exceeds it.
   Truncating instead would launch a fleet that differs from the file the caller wrote (§1.1).
5. **Version before the strict decode.** A token pre-pass reads `version` and rejects any key repeated within any
   object, anywhere in the document — `encoding/json` is silently last-wins where the CLI errors, and
   `{"effort": "low", "effort": "max"}` inside one role object is the same ambiguity as a duplicate role.
   `version` must be present and exactly `1`.
6. **Strict decode**: unknown fields rejected, one document, nothing trailing.
7. **Absent, `null` and empty are three different things.** An absent optional field is omitted; `null` and `""` are
   both errors. The file is therefore *stricter* than the CLI here, and the divergence is one-directional and stated
   rather than discovered.
8. **Every surviving value goes through §7's validators, unchanged**, applied to the union.

`dir` existence and is-a-directory are not checked here; that stays at point of use (§12.9).

**Version policy is stated, not implied.** With unknown fields rejected, every future field addition makes new
templates unreadable by older binaries, **and that is intended**: a binary that ignored a field it did not understand
would launch a fleet differing from the file it was handed, which is §1.1's first half in its most literal form.
`version`'s job is therefore not to make old binaries tolerant but to make their refusal legible — which is why it is
read in the pre-pass and checked *before* the strict decode:

```text
agentctl: template FILE: version 2 is not supported by this agentctl (supports 1)
```

Checked the other way round, the same file would fail as `unknown field "…"` and the version field would do nothing the
unknown-field check was not already doing badly.

#### Errors

Every template failure is **exit 2** (§9), and messages are agentctl-shaped, never decoder-shaped: a bare
`json: unknown field "efort"` leaks the decoder, names no file and points at no role. Each message names the file, the
location, the value and the rule; wrapping the decoder's own error underneath is encouraged, as elsewhere (§4.1).

```text
agentctl: template /srv/fleet.json: unknown field "efort"
agentctl: template /srv/fleet.json: roles[1].effort: harness "codex" does not support effort "extreme"; supported levels are low, medium, high, xhigh, max
agentctl: template /srv/fleet.json: roles[2]: duplicate role "planner"
agentctl: template /srv/fleet.json: roles[0].model: must not be empty; omit the field instead
```

#### Provenance

`launch` prints no provenance today, so this is a new output contract. One line per role, before the status render,
only when `--from-template` was supplied. The vocabulary is `relaunch`'s (§6.8) with one constant added — `template`,
alongside the existing `flag override` and `flags`; `stored` does not apply to `launch`. A parallel triple would give
two vocabularies for one concept.

```text
agentctl: launched planner in alpha: harness claude (template), model opus-4-1 (template), effort low (flag override)
agentctl: launched worker in alpha: harness codex (flags), model default (flags), effort default (flags)
agentctl: template /srv/fleet.json: dir /srv/work (flag override)
```

`default` renders an empty model or effort (§12.7). The trailing line appears only when the template supplied `dir`,
and there is no "recorded value left unchanged" note: `relaunch` has one because `@agentctl_dir` persists, whereas a
template persists nothing, so such a note would describe no record. `@agentctl_fleet` and `@agentctl_dir` are stamped
from the effective merged values by the existing path, and are re-validated on every read (SECURITY.md residual 4)
regardless of origin — being template-sourced confers no trust.

#### Read-only

A template is input. agentctl never generates, echoes, or writes one, so the file-writing invariant in SECURITY.md —
writes only to fixed paths, with no caller-supplied components — is untouched by this feature and needs no amendment on
the write side.

## 7. Validation rules (consolidated)

- Session and role names: `^[a-z0-9][a-z0-9_-]*$`.
- Model identifiers: `^[a-zA-Z0-9][a-zA-Z0-9._-]*$` (catalogue-free; charset-bound).
- Effort levels: allowlist per harness — `low`, `medium`, `high`, `xhigh`, `max` for both `claude` and `codex` (§3.2.1). Rejection names the harness, the rejected value, and the supported levels.
- `--efforts` shares every structural rule with `--models`: optional, non-nil-but-empty is a usage error, entries are `ROLE:VALUE`, duplicate role entries and entries for undefined roles are rejected, and empty list entries name the raw list and the entry index.
- Harnesses: `claude` | `codex` only.
- All rejection cases from the brief's Validation section: unknown harnesses, duplicate roles, duplicate model entries, models for undefined roles, missing values, empty `--roles`, trailing commas, whitespace in names, names beginning with `-`, duplicate command-line options.
- `--dir`: must be an existing **directory**. On `launch`, a relative value is made absolute before validation and
  reuse; on `relaunch`, it is an explicit one-invocation override and is not persisted. Non-existent path and
  existing-but-a-regular-file are both exit 2, evaluated before any tmux call (§6.1 step 4).
- **Template `dir` must be absolute.** A relative value is refused, naming it and pointing at `--dir`. A template is a
  portable artifact, so resolving against the process working directory would let one file launch two different fleets
  from two directories with no warning — the silent divergence §6.8 already refuses when a *stored* directory is
  relative, for the same reason: no trustworthy base remains. Resolving against the template's own directory was
  rejected as worse still, because it would give `--dir foo` and `"dir": "foo"` different meanings for one field.
  Nothing is lost: `--dir` still accepts a relative path and still overrides the template per field. Where a template
  must stay portable across machines, the answer is to omit `dir` from the file and supply `--dir` at invocation.
- **When `--from-template` is supplied, the validation subject is the union** of template and command line (§6.9), not
  either source alone. Values reach these rules identically whichever source they came from.
- **One implementation per rule.** Each rule above has exactly one implementation, and the CLI parsers, the template
  decoder, and the control commands are callers of it — they may format a failure differently (the list parsers name
  the raw list and the entry index; the template names the file and the role index), but none of them re-decides the
  rule. The existing inline duplicates of the role, harness and model rules inside the list parsers are consolidated
  onto the shared predicates as part of this work. That consolidation is behavior-preserving and testable as such: the
  existing tests pin the CLI message text and must stay green **unchanged**. A pinned message that has to move is
  evidence the wrapping is wrong, not that the test needs editing.

## 8. Process-identity policy

No name pattern-matching. Identity is established by observation at launch and verified by equality afterwards:

- **Check target.** The pane's *root* process, `#{pane_pid}` — stable across `exec` and unaffected by agent subprocesses. Never `#{pane_current_command}`, which tracks the foreground job and flaps to child commands (`bash`, `python`, …) while an agent runs tools.
- **Baseline (launch).** The launch contract assumes that `amq coop exec` replaces itself directly with the harness
  (`amq coop exec → exec(harness)`), with no intermediate root process between them. Poll
  `ps -o comm= -p <pane_pid>` and accept a baseline only after **two consecutive identical non-`amq` observations**;
  store that accepted value in `@agentctl_process` (each observation is trimmed exactly once per §13.7). Poll
  parameters are fixed, not tuned per call:

  | Parameter | Value |
  |---|---|
  | Timeout | 5s |
  | Cadence | 100ms |
  | First attempt | immediately, at t=0 |
  | Final attempt | guaranteed at the boundary before declaring timeout |

  A different non-`amq` value replaces the candidate. Two conditions clear the candidate and share the retry path:
  the sentinel for "no identity available yet" (`ps` reporting nothing for a pid that has not been replaced yet) and
  an observed value of literal `amq`. The `amq` comparison is against the exact trimmed value — the bare name, not a
  path — which holds because the window command invokes `amq` by bare name (§13.7). Neither condition is a tmux
  failure; both simply mean "not yet". Any other process-observation error fails immediately. If no stable pair has
  been accepted by the final boundary attempt, the consequence differs by command — and §1.2 is what makes the
  difference principled rather than arbitrary. The two commands stand in different relations to what existed before
  them, so the same rule yields opposite outcomes:

  - **`launch` records no baseline and destroys nothing.** It stamps the `@agentctl_unproven` abandonment record in
    place of the baseline (§6.5), so the role is unproven *and recoverable*; its window and the rest of the fleet are
    retained, and the invocation exits 9 (§6.6, §9). Rolling back would destroy peer roles that launched correctly.
  - **`relaunch` rolls back the single window this invocation created, exit 8** (§6.8 step 9), unchanged. It *has* a
    prior state: its precondition is zero windows matching ROLE, so removing the window it just made restores the
    fleet exactly as it found it, and the role was already absent beforehand, so absent-after is not a regression.

  `launch` cannot take `relaunch`'s path even in principle. Killing a session's only window destroys the session
  (verified, tmux 3.7b, throwaway socket: `kill-window` against the sole window exited 0 and the next `list-sessions`
  reported `no server running`), and the first role's window *is* the session until a second role exists — so a
  per-window rollback during `launch` would destroy the entire fleet for exactly the role the §6.6 carve-out protects.

  The same fact bounds `relaunch`'s recovery power one level deeper, which is easy to miss because the two paths look
  unrelated: a recovery kill against a session's *only* window destroys the session just as surely, and leaves
  nothing to create the replacement into. §6.8 step 4 therefore refuses that case rather than attempting it.

  An unproven role is inert rather than contained by force: verification requires exact equality with a stored
  baseline, and an unset `@agentctl_process` can never satisfy it, so every control command refuses at exit 5 and
  `status` reports `no-baseline` (§6.3). Recovery is `relaunch` (§6.8), which does not re-baseline the existing
  process — it destroys the pane and observes a new one from scratch. That distinction is what keeps the permanent
  rejection of heal-on-verify intact.
- **Verification (control/status).** Re-run the same `ps` query and require **exact equality** with the stored baseline. Mismatch → `unexpected-process` in status; fail closed (exit 5) for control commands. Empty/missing baseline also fails closed.

The stable pair reduces the window for recording a transient process; it does **not** ensure that the process has
settled, because it proves only that two observations 100ms apart were equal. Normal launch cost is one additional
100ms tick per role, approximately 800ms for an eight-role fleet. This handles Claude Code's versioned binary name
(`2.1.220` at the time of the spike) without heuristics and is robust to future harness renames. It remains a safety
guard against accidents, not an authentication mechanism or an idleness proof: a same-user process can forge metadata
or match by renaming an executable.

## 9. Exit codes

The brief's table verbatim (0, 2–8). `kill` uses 3 for unresolvable/missing/unmanaged sessions and 6 for tmux failures.
Exit 8's claim extends from "the session this invocation created was removed" to "**what this invocation created**
was removed": `launch` rolls back a session, `relaunch` rolls back the single window it created (§6.8).
Exit 4 additionally covers a role that resolves to more than one window (§13.5). Exit 6 additionally covers a
pre-ownership creation failure during `launch`, carrying the operator warning in §6.6, and any resolver `Runner`/parse
failure, carrying tmux's own stderr (§4.1). Exit 3 additionally covers an unresolvable or ambiguous session (§4.1) and every `attach` refusal — missing, unmanaged,
or a version other than `1` (§6.4, §12.6); `attach` uses 6 when control mode cannot be established.

**Template failures are exit 2**, uniformly — unreadable, absent, non-regular or oversize file; decoder errors of every
kind; an unsupported version; and any union value that fails §7. They share one code because they share one fact: the
caller supplied an unusable input and nothing ran. That is not an analogy to §6.1 step 1's "exit 2, nothing created" but
the same guarantee, because the entire template path completes before step 2's preflight, so no template failure can
occur after anything exists (§6.9).

**Exit 9 — launched with an unproven role; nothing was rolled back.** `launch` created the session, retained it and
every window it made, and could not establish a process baseline for at least one role within §8's deadline. It extends
the brief's table because no code in it fits: 8 asserts a removal that did not happen, 5 asserts a refusal in which
nothing was done, and 0 asserts unqualified success. Under §1.1 an exit code is a claim, so the correct response to "no
existing code states this" is a new code, not the nearest wrong one. Exit 9 makes no claim about which roles are
unproven or how many; the §6.6 messages carry that. It is `launch`-only: `relaunch` has a prior state to return to and
keeps exit 8 (§8).

**Exit 1 is the unclassified error** (§1.1). It carries no contract semantics and never will: it asserts only "something went
wrong that codes 2–8 do not describe". The codes in the table are the opposite — each one is a claim about what
happened to the system, which is why an unimplemented command must not borrow one. Exit 8 in particular states that a
fleet launch failed *and was rolled back*; returning it from a stub tells an operator or a planner script that a
session was created and destroyed when nothing was touched.

Consequently:

- The temporary not-implemented stubs return **1**, uniformly, for every subcommand.
- A valid invocation that reaches missing functionality is not a usage error, so it is not 2 either.
- Tests from Wave 2 onward assert **behaviour** — argv recorded by the fake `Runner`, metadata written, output rendered
  — and never a stub's exit code. A test that passes because a stub happened to return the expected number proves
  nothing and will keep passing after the real implementation lands.

## 10. Testing

- Unit tests against the fake `Runner` asserting **exact argv** for every case in the brief's Testing section, plus: `kill` refuses unmanaged sessions; `--dir` propagates to `-c`; model charset rejections; effort allowlist rejections and per-harness effort rendering (`--effort LEVEL` for claude, `--config 'model_reasoning_effort="LEVEL"'` for codex), with an absent effort emitting no argument; baseline capture accepts `claude` rather than transient `env` for `[amq, env, claude, claude]`, accepts the first stable pair without extra polling for `[amq, claude, claude]`, and, when observations remain unsettled through the timeout boundary, stamps no `@agentctl_process`, stamps `@agentctl_unproven` as that role's final call instead, issues no kill, and lets the remaining roles continue (§6.6) while `relaunch` still rolls back its own created window (§8); a `no-baseline` window without that record is refused by recovery with no kill issued; equality check against `@agentctl_process` including empty-baseline fail-closed; self-target guard (`$TMUX_PANE` == target pane refused, absent/different pane allowed).
- `status` (§6.3): state precedence exercised in order, each state reached with the higher ones inapplicable; multi-pane
  renders `unmanaged`; alive-pane-with-unavailable-identity renders `unexpected-process` and an empty baseline renders `no-baseline` (§6.3), with no process probe issued for the latter; zero
  panes renders `missing`; a roster role with no window renders `missing`; `unexpected-process` renders the observed
  executable, not the baseline; unmanaged session renders `managed:false` with an empty agents array and exit 0 while a
  non-`1` version still exits 3; the fake `Runner` recorded **no** row-7 calls and **no** row-14 call for any role whose
  state was decided before the process probe.
- Launch failure paths (§§6.6–6.7): both exit-8 messages asserted **verbatim**, including the cleanup-failure variant with a
  cause; pre-ownership malformed creation output asserts exit 6, the `tmux ls` warning, and that the fake `Runner`
  recorded **no** `kill-session`; a duplicate-session refusal after an empty/failed advisory lookup asserts exit 3,
  tmux's original message, the exact `list-sessions`/`new-session` argv, and no rollback, while exit-status/stderr
  near-matches remain exit 6. Baseline poll (§8): t=0 attempt, cadence, and a guaranteed boundary attempt before
  timeout. Metadata stamping asserted as an exact ordered call sequence (§6.5). `--dir` pointing at a regular file
  exits 2 with no tmux call recorded.
- Session resolution (§4.1): precedence with each higher source present; explicit-empty exits 2 without fallback while
  empty `AGENTCTL_SESSION` falls through; an invalid higher source blocks the lower one; the inside-tmux path asserts
  the **exact call order** row 12 then row 1, proving the displayed name is never a `-t` target; duplicate exact names
  exit 3 naming both IDs; a no-server failure exits 6 with tmux's message intact. Forbidden-source canary: with
  `AM_ROOT`, `AM_SESSION` and a suggestive cwd all set and no permitted source, resolution fails **and the fake
  `Runner` recorded zero calls** — proving neither inference nor first-session selection.
- Control chain (§6.2), one case per branch with its exit code: malformed ROLE exits 2 with the fake `Runner` recording
  **zero** calls; a session whose `@agentctl_version` is not `1` exits 3 for control and `kill`; a window whose stored
  role metadata mismatches exits 4; a multi-pane or dead pane exits 5; identity mismatch, empty baseline and
  unavailable identity each exit 5. Each negative branch asserts **no** `DeliverPayload` calls were recorded — the exit
  code alone would pass an implementation that delivered first and reported second.
- Ambiguous roles (§13.5): two windows with the same name — control commands exit 4 with both window IDs named and **no**
  `send-keys` recorded by the fake `Runner`; `status` emits one row per matching window, each with state `ambiguous`.
- `shellq`: table tests + Go fuzz test asserting the **exactly-one-word** round-trip property: the rendered string,
  evaluated by `sh`, yields the original bytes *as a single shell word*. This supersedes issue #2's original
  `sh -c "printf %s <quoted>"` criterion, which cannot detect word splitting — `printf` re-uses its format across
  surplus arguments and concatenates them, so a `Quote` emitting `'planner' ':claude'` for `planner:claude` passes it.
  Assert the word count as well as the bytes:
  `set -- <quoted>; printf %s "$#"; printf ':'; printf %s "$1"` → `1:` + input.
- `relaunch` (§6.8): exact argv for stored-mode recreation, with the stamping order asserted as an ordered call
  sequence, no index argument in the creation call, and the post-create row-8 read against the exact session ID before
  any stamp; deterministic fake-Runner interleaving where the precondition sees zero matches and the post-create read
  sees two, asserting refusal with every observed ID, exit 8, `kill-window` on only the ID this invocation created,
  no success output and no stamp; flag overrides re-encoding `@agentctl_fleet` **after** the
  baseline, and a `--dir` override leaving `@agentctl_dir` untouched; every refusal above with its exit code and
  message — ownership gate, roster defect, role outside the roster, each metadata defect, legacy session with and
  without the required options, and each §6.3 state a surviving window can be in, `dead` included; both rollback
  branches asserting `kill-window` on the created ID and **no** `kill-session`; a pre-ownership creation failure
  killing nothing; provenance rendered for stored, override and legacy cases; and the relaunched role passing `status`
  and the control chain against its **new** `@agentctl_process` baseline.
- Relative launch directories: `launch --dir .` and `launch --dir ../sibling` pass and stamp the same hand-derived
  absolute path; a launch from directory A followed by a relaunch from directory B with the same relative directory
  name still recreates with A's stored absolute `-c` argv. A pre-fix relative `@agentctl_dir` is refused with exit 3,
  its stored value verbatim, an explicit `--dir` remedy, and no creation call.
- Integration tests (build tag `integration`): real tmux on a throwaway socket (`tmux -L agentctl-test-$RANDOM`), windows running stub scripts standing in for harnesses; never the user's server, never real agents.
- Manual verification checklist (tracked as a backlog issue): re-run the §3.3 spike against current harness versions before first release.
- CI: `go test ./...`, `go vet ./...`.

## 11. Delivery plan

One epic, five sequential waves; issues within a wave are parallelizable across implementing agents.

1. **Foundations** — module scaffolding, Makefile (`build`/`test`/`install` to `~/.local/bin`), CI; `shellq`; `config`; `harness`.
2. **Launch** — `tmuxx` Runner + typed wrappers; executable preflight; `fleet` launcher + metadata + rollback.
3. **Observe** — `session` resolver; `status` (table + JSON); `kill`.
4. **Control** — `target` validation chain; `control` dispatcher; manual spike re-verification.
5. **Attach & polish** — `attach` (iTerm2); README + iTerm2 setting documentation; integration suite; deferred `--if-missing` issue filed.

## 12. Pinned clarifications (2026-08-01, post-kickoff)

Authoritative answers to implementation questions raised during Wave 1. These bind all packages and reviews.

1. **Window-command assembly site.** Exactly one place assembles the window command: `internal/fleet`, as the string `"exec " + shellq.Join(harness.AgentArgv(...))`. `harness` returns argv (starting at `amq`); `shellq` quotes and joins; `fleet` prepends the unquoted `exec` shell keyword and passes the string to `tmuxx`. No other package may compose shell-interpreted strings.
2. **`shellq.Quote` is total.** Safe for arbitrary bytes with no validated-input precondition (defense in depth, independent of `internal/config`). Sole documented exclusion: NUL, which cannot exist in an argv element; the fuzz round-trip property skips inputs containing `\x00`.
3. **Canonical tmux argv table.** `internal/tmuxx` owns one canonical argv per tmux operation. The table is §13; any change to it is a spec change.
4. **Exact targeting everywhere.** Names are never passed to `-t`. Sessions, windows and panes are resolved to tmux IDs by listing and comparing exactly in Go, and every subsequent operation addresses the ID. This is a security invariant; reviews fail PRs on it. Superseded in mechanism by §13.1, which records the tmux behaviour that makes the original `=`-prefix formulation unimplementable for `set-option`/`show-options`; the intent — no name matching, ever — is unchanged and strengthened.
5. **Exit code for bad ROLE argument.** A ROLE failing `^[a-z0-9][a-z0-9_-]*$` is a usage error → exit 2. A well-formed ROLE with no matching managed window → exit 4.
6. **Version gate.** For **control, `kill` and `attach`**, the managed-session gate requires `@agentctl_managed=1` **and** `@agentctl_version=1`; anything else fails closed at exit 3 — a future agentctl's sessions are not ours to act on.

   **Refusal messages render the observed value rather than naming a cause.** The two failure modes are distinct facts and must read as such, in every consumer:

   | Observed | Message shape |
   |---|---|
   | `@agentctl_version` absent | *managed session carries no `@agentctl_version` marker* |
   | `@agentctl_version` present and ≠ `1` | *has `@agentctl_version="2"`; expected `"1"`* |

   An absent marker is **not** "a different version": saying so asserts an event that did not happen (§1.1). A rendered fact cannot lie about causation, which is why the value-rendering shape is the rule rather than one acceptable option among several. This applies to `status`, `control`, `kill`, `attach` and any command added later that reads this gate — the shape is a property of the rule, not of the command that happens to be reporting it. `attach` is included deliberately: it is the only command that hands a human a live keyboard into panes whose metadata semantics we cannot interpret, and "agentctl refused" is recoverable where "operator typed into a misunderstood fleet" is not. Its refusal names the escape hatch (§6.4) — an operator tool should be conservative without pretending to be a boundary. **`status` is carved out** for the *unmanaged* case only: a session with `@agentctl_managed` missing or not `1` is reported, not refused (§6.3). A version present but not `1` remains exit 3 everywhere, `status` included: we can read another version's options but cannot trust their semantics, and reporting them as if they were ours would be a false statement rather than a missing one.
7. **Defaulted model and effort rendering.** Metadata and JSON carry the empty string `""`; only the human-readable table renders `default`. `status` gains an `effort` field on each agent and an `EFFORT` column between `MODEL` and `PANE`; the JSON document stays at `"schema": 1` because the addition is a new field on an existing object, not a change to any field a consumer already reads.
8. **Toolchain pin.** `go.mod`'s `go` directive and CI's `go-version` must be identical (initially Go 1.26); drift is a review failure. Owned by issue #1.
9. **Validation ownership.** `internal/config` owns all value semantics: `ParseFleet` (roles/models rules) and `ValidateSessionName`. The launch-template decoder (§6.9) is a **source** of values, not a validator: it decides shape — what keys exist, what is required in the file, what is ambiguous — and hands every value to the same predicates the flags use (§7). `internal/cliflags` owns flag mechanics (duplicate-option rejection). The `--dir` existence/is-directory check happens at point of use in the launch flow (`internal/fleet`), not in `config`. An explicitly supplied but empty `--models` (or `--roles`) value is a usage error; an omitted `--models` is valid. Errors for empty list entries (leading/consecutive/trailing commas) name the raw list and the entry index, since no printable entry exists.

## 13. Canonical tmux argv table

`internal/tmuxx` owns exactly the argv shapes below and exposes nothing else. Every element listed is a separate argv
element passed to `os/exec`; agentctl never invokes a shell (§5). Unit tests assert these element by element against the
fake `Runner`. Any change here is a spec change.

Verified against **tmux 3.7b** on 2026-08-01 on this machine, using throwaway servers (`tmux -L agentctl-rev-*`) and
stub commands — never the user's server, never real agents.

### 13.1 Targeting model

Observed tmux 3.7b behaviour:

| Probe | Result |
|---|---|
| `has-session -t betab`, only session `betabeta` present | exit 0 — **prefix matched** |
| `has-session -t '=betab'` | exit 1 — `=` is exact |
| `list-panes -t 'alpha:rev'`, only window `reviewer` present | resolved to `reviewer` — **prefix matched** |
| `list-panes -t 'alpha:=rev'` | `can't find window: rev` |
| `set-option -t '=alpha' @k v` | **`no such session: =alpha`** — the `=` prefix is *not* honoured |
| `set-option -t alph @k v`, only session `alpha` present | **exit 0 — prefix matched** |
| `set-option -t '$0' …` / `show-options -qv -t '$0' …` | exact; a decoy `alphabet` session was unaffected |
| two windows both named `dup` | permitted by tmux; `-t alpha:dup` then fails to resolve |

`set-option` and `show-options` — the two operations that write and read the managed-metadata gate — therefore cannot
express exact matching by session name at all: `=` is an error and a bare name prefix-matches. The `=`-prefix rule of
§12.4 is unimplementable on precisely the security-critical path it was written to protect.

**Rule.** All targeting is by tmux ID:

1. **Resolve once, by listing.** Compare names byte-for-byte in Go against `list-sessions` / `list-windows` output.
   IDs are `$N` (session), `@N` (window), `%N` (pane).
2. **Address by ID thereafter.** No session, window, or role name is ever passed to `-t` after resolution.
3. **Exactly one match is required.** Zero → not found. **More than one → fail closed**; tmux permits duplicate window
   names, so a role matching two windows is an error, never "take the first". Handling is asymmetric by command type
   and is specified in §13.5.
4. The `=` prefix is not used anywhere. ID targeting is strictly stronger and — unlike `=` — is uniformly accepted by
   every operation in §13.2, including `set-option`/`show-options` and `attach-session` (all verified).

### 13.2 Operations

`⟨sid⟩`, `⟨wid⟩`, `⟨pid⟩` are resolved IDs; `⟨TAB⟩` is a literal 0x09 byte. Rows 1–13 (including 5a and 11a) are the argv
**after** `tmux`; row 14 is the one non-tmux command the `Runner` executes and is shown as a complete argv. Lettered rows
sit beside their siblings without renumbering rows this document cross-references.

| # | Operation | argv |
|---|---|---|
| 1 | Resolve session | `list-sessions -F #{session_id}⟨TAB⟩#{session_name}` |
| 2 | Create session (first role) | `new-session -d -s SESSION -n ROLE -c DIR [-e NAME=VALUE ...] -P -F #{session_id}⟨TAB⟩#{window_id}⟨TAB⟩#{pane_id}⟨TAB⟩#{pane_pid} -- CMD` |
| 3 | Create window (later roles) | `new-window -d -t ⟨sid⟩ -n ROLE -c DIR [-e NAME=VALUE ...] -P -F #{window_id}⟨TAB⟩#{pane_id}⟨TAB⟩#{pane_pid} -- CMD` |
| 4 | Set session option | `set-option -t ⟨sid⟩ NAME VALUE` |
| 5 | Set window option | `set-option -w -t ⟨wid⟩ NAME VALUE` |
| 5a | Clear session environment variable | `set-environment -t ⟨sid⟩ -u NAME` |
| 6 | Read session option | `show-options -qv -t ⟨sid⟩ NAME` |
| 7 | Read window option | `show-options -wqv -t ⟨wid⟩ NAME` |
| 8 | List windows + metadata | `list-windows -t ⟨sid⟩ -F <§13.3 format>` |
| 9 | List panes | `list-panes -t ⟨wid⟩ -F #{pane_id}⟨TAB⟩#{pane_pid}⟨TAB⟩#{pane_dead}⟨TAB⟩#{window_panes}` |
| 10 | Deliver payload (composite, §13.6) | `send-keys -t ⟨pid⟩ C-u` · `send-keys -t ⟨pid⟩ -l -- /PAYLOAD` · `send-keys -t ⟨pid⟩ Enter` |
| 11 | Kill session | `kill-session -t ⟨sid⟩` |
| 11a | Kill window | `kill-window -t ⟨wid⟩` |
| 12 | Current session name | `display-message -p -t $TMUX_PANE #{session_name}` |
| 13 | Attach | `-CC attach-session -t ⟨sid⟩` |
| 14 | Process identity (§13.7) | `ps -o comm= -p PID` — complete argv, not prefixed by `tmux` |

Notes:

- **Row 5a, scope.** Issued only by `launch`, once per identity variable, immediately after the first role's window is created and stamped (§6.5) and before any further window is created. `new-session -e` writes `AGENTCTL_SESSION`, `AGENTCTL_ROLE` and `AGENTCTL_MANAGED` into the *session* environment as well as into the first window; every later window agentctl creates carries its own `-e` values, so the session copy serves nothing and actively misidentifies any window an operator creates by hand — it would claim that window is the first role of a managed fleet. All three are cleared, not just the two that are false: the set is exported as one statement about one window, and leaving a third of it behind is more confusing than removing it. The session name remains discoverable from tmux itself.
- **Row 5a does not disturb what already exists.** A process's environment is fixed when it is exec'd, so clearing the session environment cannot alter the first role's pane. This is why the clear may safely follow window creation rather than having to precede it.
- **Row 5a failure is reported, never fatal, and never rolled back.** At this point the session, its first window, and its metadata are all correct; the only consequence of a failed clear is that a window an operator later creates by hand would inherit a stale identity. Killing a working fleet over advisory metadata would be disproportionate to that. `launch` therefore continues and exits 0, and writes one line to stderr naming the variable it could not clear and what follows from it. Silence would withhold a fact (§1.1); a non-zero exit would claim the launch failed when it did not.
- **Row 5a is classified by exit status only.** No branch reads tmux's message text. Unlike row 2's exact duplicate
  refusal, this operation has no separately verified message contract that would support a narrower classification.
- **Rows 2–3, `-e`.** The `-e` segment is emitted in declaration order, one flag plus one `NAME=VALUE` element per
  variable, from values that already passed identifier validation; it is absent entirely when no variables are
  supplied, so the no-env argv is byte-identical to the previous rows.
- **Rows 2–3, `--`.** Verified that tmux accepts `--` before the shell-command on both. `CMD` is the §12.1 string and
  always begins with `exec `, so it can never be read as a flag; `--` is belt-and-braces and costs nothing.
- **Rows 2–3, `-P -F`.** `-P` prints the requested fields on stdout at creation. This removes a resolve round-trip and
  the race between creating a window and looking it up by name — the launcher never has to name-match its own windows.
- **Rows 2–3 return `#{pane_pid}`** because the launcher must never look up what it just created, and because
  `pane_id` cannot feed the process-identity command: row 14 takes a **pid**, not a pane. Without the pid in the
  creation record, capturing the §8 baseline would require a `list-panes` round-trip keyed on the pane the launcher
  already holds — reintroducing exactly the lookup-after-create step `-P -F` exists to remove.

  The pid is consumed as a validated positive integer. A missing, non-numeric or non-positive pid is a **creation-output
  parse failure**, in the same class as a malformed ID: it means the launcher never obtained ownership evidence it can
  act on, so it takes the pre-ownership branch of §6.6 (exit 6, kill nothing), not the rollback branch.

  Verified on tmux 3.7b (2026-08-01): `#{pane_pid}` renders in `-P -F` for both rows; the returned value equals
  `list-panes`' `#{pane_pid}` for the same pane; and it is **stable across the `exec` chain** — a window created as
  `sh -c 'exec sleep 60'` reported the same pid at creation and after the exec completed. That stability is what makes
  the creation-time pid a valid target for the §8 baseline poll rather than merely a convenient one.
- **Row 11a.** Exposed for exactly two uses, both in `relaunch` (§6.8): rolling back the window ID this invocation
  created (step 9), and the recovery kill of a single window classified `no-baseline` at step 4 (step 5a). Both target
  a window ID agentctl resolved itself and holds as a typed `WindowID`, so a name can never reach `-t`. No other
  command removes a window; `kill-window` is never issued against a window whose state was not classified by §6.3
  precedence in the same invocation; and the recovery kill never runs when the role resolves to more than one window.

  The second use widens what this row previously permitted — before it, agentctl never removed a window it had not
  itself created — so it is stated here as a change rather than absorbed silently. The concurrent case is bounded by
  window-ID behavior: IDs are a monotonic per-server counter and are **not reused within a server lifetime** (probed on
  tmux 3.7b; a restarted server resets the counter, the same caveat pane IDs already carry), so a losing contender's
  recorded ID can never name the winner's freshly created window. It fails its kill and exits 6 having created nothing.
- **Row 10.** Payload and `Enter` stay separate events, with the §3.3 fixed delay between them; `-l` is literal mode and
  `--` guards the leading `/`. Verified end to end: a pane running `cat` received exactly `/clear`.
- **Row 12.** Only used by the session resolver's inside-tmux fallback, and only to obtain a *name*, which is then fed
  through row 1. `-t $TMUX_PANE` is required: `display-message -p` with no target returned empty against a detached
  server, and "the current client" is ambiguous when several clients are attached.
- **Row 13.** `-CC` is a tmux **global** option and precedes the command. The brief's `-t '=epic123'` is its informal
  equivalent; the resolved session ID is exact and verified to work here too.
- `has-session` is deliberately absent. Existence is decided by row 1 plus an exact compare, so no second, weaker
  existence path exists.

### 13.3 Format-string and option-read rules

- **Window collection format** (row 8), fields in this order:
  `#{window_id}⟨TAB⟩#{window_name}⟨TAB⟩#{@agentctl_role}⟨TAB⟩#{@agentctl_harness}⟨TAB⟩#{@agentctl_model}⟨TAB⟩#{@agentctl_effort}⟨TAB⟩#{@agentctl_unproven}⟨TAB⟩#{@agentctl_process}`
  Parse with `strings.SplitN(line, "\t", 8)`.
- **`@agentctl_unproven` is carried here, and read nowhere else.** §6.8 step 4 must observe the abandonment record to
  classify a window as recoverable, and `relaunch` already issues row 8 to classify that window by §6.3 precedence — so
  carrying the field costs no additional command. The alternative, a row 7 read per window, would cost one call each and
  reintroduce exactly the per-window read loop §6.3 removed; row 7 therefore stays unused, and the status tests that
  assert its absence continue to hold. This is the same standard the next note applies: row 8 carries every window field
  that is *collected or consumed*, not every field that is stamped. `status` receives the value and ignores it,
  consistent with the marker deliberately not being a status state (§6.3).

  Its **placement is constrained, not stylistic**. `@agentctl_unproven` is bounded — empty or `1` — so it sits before
  `@agentctl_process`, which must remain last to absorb any residue under the unconstrained-values-last rule below.
  Putting it after would let a delimiter inside an observed process name shift it.
- **Managed/version fields are absent by construction.** Row 8 and the `tmuxx.Window` data model do not carry the
  inherited session `@agentctl_managed` or `@agentctl_version` expansions. Window `@agentctl_managed` remains stamped
  as advisory metadata (§6.5); the prior window-version data-model field was removed rather than promoted to a gate.
- **Unconstrained values go last.** Every field except `@agentctl_process` is charset-validated or allowlisted. `@agentctl_process`
  comes from `ps -o comm=` and may contain spaces (a value `weird name` was verified to round-trip intact), so it is
  placed last and absorbs any residue — a delimiter inside it cannot shift another field.
- **Reads always use `-v`.** Verified: `show-options -w` *without* `-v` quotes values containing spaces
  (`@agentctl_spacey "two words"`), which would require an unquoting routine and create a second quoting site,
  violating §5. With `-v` the raw value is printed verbatim.
- **`-q` is required on reads.** Without it, an unset option is `invalid option: NAME` on stderr with exit 1; with it,
  the result is empty output and exit 0.
- **Unset and set-to-empty are indistinguishable** (both empty, exit 0). This is acceptable and deliberate: the
  session-gate options `@agentctl_managed` and `@agentctl_version`, the window-ownership field `@agentctl_role`, and
  the process baseline `@agentctl_process` are never legitimately empty, so empty means fail closed.
  `@agentctl_model` and `@agentctl_effort` are legitimately empty (§12.7) and gate nothing.
- `#{@name}` user-option interpolation in `-F` is verified working on tmux 3.7b; unset options render empty without error.

### 13.4 Consequences for tests

- **Never assert `pane_current_path` against `-c DIR`.** `-c /tmp` produced `pane_current_path=/private/tmp` (symlink
  resolution). Tests assert the `-c` **argv element**; the integration suite may assert the resolved path only after
  applying the same resolution.
- **No `remain-on-exit`, verified.** A window whose command exited disappeared entirely from `list-windows`, confirming
  §6.3: an exited agent normally reports `missing`, not `dead`.
- Ambiguity is a first-class test case: two windows sharing a role name must fail closed, not resolve (§13.5).

### 13.5 Ambiguous roles

tmux permits two windows in one session to carry the same name, and agentctl cannot prevent it: `--roles` rejects
duplicate roles, but a user or another process can rename a window or create one by hand at any time after launch.
Resolution by exact comparison (§13.1) therefore has three outcomes, not two, and the third is handled differently by
control commands than by reporting commands.

**Control commands** (`clear`, `compact`, `kill`) **fail closed with exit 4.** The error names the role and every
matching window ID, so the operator can see what to fix:

```text
agentctl: refusing to clear codex2; role matches 2 windows in epic123 (@4, @7)
```

Exit 4 is the existing "invalid or missing role/window" code (§9): a role that resolves to two windows is not a usable
target. No keystroke is delivered, and the ambiguity is never broken by picking the lowest ID, the first listed, or the
most recently created.

**`status` reports rather than refuses.** Every matching window is rendered as its own row, each carrying state
`ambiguous`, so the duplicate is visible instead of being hidden behind an error. This adds `ambiguous` to the state
enum in §6.3. The JSON schema is unchanged and stays at `"schema": 1` — `state` is a string field and gains a value,
not a shape. Consumers that switch on `state` must already tolerate unknown values.

`ambiguous` takes precedence over the other states for an affected window: it describes the target's *resolvability*,
which is decided before any process check, so a duplicate row is never reported as `running` or `unexpected-process`.

### 13.6 Payload delivery is one exposed operation

`tmuxx` exposes row 10 **only** as `DeliverPayload(paneID, payload)`. The three `send-keys` argv shapes remain the
canonical truth and are asserted as such against the fake `Runner`, but no individual `SendKeys` wrapper is exported:
there is no partial delivery, and no caller can send one key event without the other two.

The delay between the payload and `Enter` is the package constant `payloadDelay`, not a parameter. Timing is not part
of the call signature, so no caller — and no future subcommand — can influence it.

**`payloadDelay` is 1s and stays 1s until measured.** Unit tests drive a fake `Runner` and therefore cannot validate a
TUI timing constant *at all* — the thing being timed is the harness's autocomplete popup settling on the exact match,
which no fake observes. A shortened delay fails by letting `Enter` select whatever entry is highlighted at that instant
(SECURITY.md residual 1): the wrong command executed inside a live agent, silently, only under load, and never
reproducibly in CI. Issue #13 owns measuring the popup-settle floor on both harnesses under load and licensing any
reduction by name; until it reports, 1s stands. 900ms on an operator-initiated command is not a cost worth trading for
that failure mode.

**Cancellation between the payload and `Enter`** returns the context error with the payload already typed into the pane
but not submitted. This is deliberate: sending `Enter` regardless would execute a command the caller just cancelled. The
residue is bounded — the next delivery begins with `C-u`, which clears it — but a cancelled control command does leave
visible text in the agent's input buffer, and that is reported as a failure, never as a delivery.

This is the same containment argument as the hardcoded payload registry (§2): the narrower the exposed surface, the
fewer places a reviewer must check that caller-supplied text cannot reach `send-keys`. A general-purpose exported
`SendKeys` would reopen precisely the hole the registry closes.

### 13.7 Process identity command

Row 14 is the only non-tmux command agentctl runs. It executes through the same `Runner`, so the fake records its argv
like any other call and no test needs a real process.

Verified on this machine (darwin, 2026-08-01):

| Probe | Result |
|---|---|
| live pid | exit 0, value on stdout |
| dead pid | exit 1, **empty stdout and empty stderr** |
| out-of-range pid (`999999`) | exit 1, `ps: process id too large` on stderr |
| `sh -c 'exec sleep 5'` (PATH-resolved) | `sleep` — bare name |
| pid 1 (invoked by absolute path) | `/sbin/launchd` — full path |
| output bytes for `bash` | `b a s h \n` — **trailing newline** |

Three consequences bind the implementation:

- **`comm` reports the executable as invoked** — a bare name when the kernel resolved it through `PATH`, an absolute
  path when it was invoked by path. The window command (§12.1) runs `exec amq …` by bare name, so the launch poll in
  §6.1 step 6 compares correctly against the literal `amq`. That is *why* it works, not a coincidence; a window command
  that ever invoked `amq` by absolute path would break the transition detection silently.
- **Trim the trailing newline exactly once, in the wrapper.** §8 says the observed value is stored "verbatim"; taken
  literally that stores `"2.1.220\n"`. Baseline capture and later verification must use the same helper so they cannot
  disagree about the newline — a mismatch here fails closed on every control command against a healthy fleet.
- **A dead pid is silent, not an error.** Exit 1 with empty stdout *and* empty stderr is the normal reading for a
  process that has gone. Treat any non-zero exit or empty output as "no identity available" and fail closed (§8), not
  as an unexpected condition to report as a tmux failure.

## 14. Out of scope

Everything in the brief's Out of scope list, plus `--if-missing` (deferred, §2 — unblocked but not implemented by
§6.5's metadata) and restarting a role whose window still exists, **except** the bounded
`no-baseline` recovery in §6.8 (which refuses `dead` rather than folding it into `missing`, and refuses every other
present-window state; a general `restart` command would be filed separately if demand appears). `status` deliberately does not read
`@agentctl_fleet` or `@agentctl_dir`: consistency checking is not its job. The brief's acceptance criteria apply, extended by: `agentctl kill` refuses unmanaged sessions; model charset enforcement; per-harness effort allowlist enforcement; deterministic cwd propagation; process-identity baseline recorded and enforced; self-target guard on control commands.
