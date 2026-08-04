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
agentctl relaunch [--session S] [--harness H] [--model M] [--dir PATH] ROLE
agentctl attach   [--session S]
agentctl status   [--session S | --all] [--json]
agentctl clear    [--session S] ROLE
agentctl compact  [--session S] ROLE
agentctl kill     [--session S]
```

Everything else in the brief's CLI section applies verbatim: no `--launch` alternative syntax, no arbitrary-payload options of any kind, duplicate command-line options rejected.

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

Each unit is independently testable against the fake `Runner`; no unit reads terminal contents.

## 6. Key flows

### 6.1 launch

1. Parse and validate the complete configuration (§7). Any error → exit 2, nothing created.
2. Preflight: `tmux`, `amq`, and each *requested* harness resolve on `PATH` → else exit 7.
3. Existence check, **best-effort and advisory** (§6.7): attempt `list-sessions`; on an exact match → exit 3. On a
   tmux or parse failure, fall through to creation — `new-session` is the authoritative arbiter. A cancelled context
   propagates instead and creates nothing.
4. Resolve cwd: `--dir` if given, else invocation cwd; pass via `-c` on every window. `--dir` must name an existing
   **directory**; a path that does not exist and a path that exists as a regular file are both usage errors → exit 2,
   checked before anything is created (§7).
5. First role: `new-session`; remaining roles: `new-window` — canonical argv in §13.2 rows 2–3, where `CMD = exec amq coop exec --session S --me ROLE HARNESS [-- MODEL-ARGS EFFORT-ARGS]` (§3.2.1; the `--` separator appears only when at least one of the two is present), assembled per §12.1. Both use `-P -F` so the launcher receives session/window/pane IDs at creation and never name-matches its own windows.
6. After each window: stamp metadata in the exact order of §6.5, then capture the process baseline by polling
   `ps -o comm= -p <pane_pid>` (§13.2 row 14) — using the pid returned by the creation record (§13.2 rows 2–3), never a
   lookup — until the `amq coop exec → exec(harness)` chain has completed, and store the result as `@agentctl_process`. Poll parameters are fixed by §8. Timeout means the role failed to launch.
7. Any failure after the session is owned — including baseline-capture timeout: stop, kill by the typed session ID,
   report on stderr, exit 8. Failures *before* ownership are a different case. Both are specified in §6.6.

### 6.2 clear / compact

1. Validate the ROLE argument's charset (§12.5). Malformed → exit 2, before any `Runner` call.
2. Resolve the session: `list-sessions`, exact name comparison in Go, address the resulting session ID thereafter
   (§13.1). Confirm `@agentctl_managed=1` **and** `@agentctl_version=1` (§12.6) → else exit 3.
3. Resolve the window: `list-windows`, exact name comparison in Go, address the resulting window ID. **No name is ever
   passed to `-t`** (§13.1). Zero matches → exit 4; more than one → exit 4, fail closed (§13.5).
4. Confirm the window is managed and its stored role matches → exit 4; exactly one pane and the pane is alive → exit 5.
5. Process identity against the recorded baseline (§8). Mismatch, empty baseline, or identity unavailable → exit 5.
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
already carries every window option — tests assert it is **absent** from recorded calls, so an accidental per-window
read loop is caught rather than merely discouraged.

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
| 2 | `unmanaged` | window `@agentctl_managed` ≠ `1`, stored role metadata mismatches, **or more than one pane** |
| 3 | `missing` | no window with this exact name, or the window has zero panes |
| 4 | `dead` | pane reports `pane_dead` |
| 5 | `unexpected-process` | observed executable ≠ stored baseline, **or** the baseline is empty, **or** identity is unavailable for an alive pane |
| 6 | `running` | everything above passed |

No process probe (row 14) is issued once an earlier state applies — the probe is the last resort, not a precondition.

Three mappings are deliberate and easy to get backwards:

- **Multiple panes → `unmanaged`, not `ambiguous`.** The window no longer satisfies the one-pane contract a managed
  window is created with, so it is no longer ours to describe. This matches control refusing the same window.
- **Alive pane + identity unavailable → `unexpected-process`.** Identity *unverifiable* is not identity *verified*.
  Reporting `running` here would assert something unproven.
- **Empty stored baseline → `unexpected-process`.** Same reasoning, and consistent with §8's fail-closed rule.

**Rendering.** `unexpected-process` shows the **currently observed** executable, not the stored baseline: the operator
needs to see what *is* running, not what was expected. Fields that were never probed render as the empty string —
`missing` → pane ID `''` and process `''`; `dead` → the real pane ID, process `''`; `unmanaged`/`ambiguous` → the
observed pane ID when trivially known, else `''`, process `''`.

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
and distinguishing the two would require the stderr matching §6.7 rejected on evidence. Noted here explicitly because
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
never announces an attachment a refusal prevented — a refused attach writes nothing to stdout. It states that the
session is being attached in iTerm2 and how many windows it has; that the menu about to appear is iTerm2's; that `esc`
detaches, ending the client and whatever iTerm2 was rendering, while the fleet keeps running; that `X` is uppercase and
force-quits; and that only `kill` stops a fleet. It deliberately does **not** say what force-quitting leaves behind —
that is iTerm2's path, unobserved here, and §1.1 forbids asserting an outcome we have not measured however confident
the inference. It also never asserts that windows *are* rendered as native tabs: that depends on an iTerm2 preference
agentctl can neither set nor read.

*The window count* is the one new fact the narration carries, read once after the ownership gate with §13.2 row 8. That
completes attach's read set: row 6 for the ownership gate, row 8 for the count, row 13 to attach, row 1 for the
post-exit probe — no other read, and no new argv shape. A failed row-8 read **omits the count and says nothing else
about it**; a guessed or defaulted number would be a claim about a fleet agentctl did not manage to observe.

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

*The probe is advisory.* The exit code is unchanged in all three outcomes, because what succeeded — the attachment —
succeeded regardless of what the probe could see afterwards, and a probe failure is reported as an unverified state
rather than an absence. One consequence is worth stating rather than leaving to be discovered: killing the attached
session when it is the last one takes the tmux server with it, so row 1 fails and the operator gets the unverified form
carrying tmux's own reason. That is the §6.7 trade again — separating the two would need the stderr matching this
design rejected on evidence — and `TmuxError` still surfaces tmux's `no server running` text, so the fact reaches the
operator even though agentctl declines to classify it.

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

`@agentctl_process` is last by construction: it is the only value that cannot be known before the window is running.
After the first role reaches that fully stamped state, `launch` clears the three identity variables from the session
environment with §13.2 row 5a, in declaration order, before it creates a later role window.

**`@agentctl_fleet`** records the per-role configuration the roster alone cannot carry: `role:harness:model:effort` quads,
comma-joined, in roster order. Every field is validated (§7), so neither `:` nor `,` can occur inside a field; a
defaulted model or effort renders as an empty field (`planner:claude::`). Parsing is `strings.Split` on `,` then
`strings.SplitN(entry, ":", 4)`. It never extends `@agentctl_roles`: the roster's meaning — the declared membership —
is unchanged, and a consumer reading only names must keep working.

It exists because `@agentctl_harness`, `@agentctl_model` and `@agentctl_effort` are **window** options: when a role's
window closes they close with it, so nothing surviving records what a missing role was launched with. Without it, relaunching one role
(§6.8) could only ask the caller, and exact fleet comparison (#17, §14) is not correctly implementable at all — a
missing role's configuration would be unknowable.

**`@agentctl_dir`** records the exact resolved string passed to `-c` at launch — the `--dir` value or the invocation
cwd, verbatim, with no symlink resolution. It is alone in its own option because, unlike every other metadata field, its
value is unconstrained (§13.3's rule that unconstrained values must not share a delimited field). §13.4 is untouched:
`pane_current_path` is still never read, and the recorded string is what agentctl passed, not what tmux resolved.

**No `@agentctl_version` bump.** The quad is the v1 `@agentctl_fleet` schema; its earlier triple form existed only while
the unreleased relaunch work was staged. Sessions launched before `@agentctl_fleet` and `@agentctl_dir` exist carry
neither and are handled as the legacy case in §6.8 rather than being silently guessed at. A development session carrying
the superseded triple is structurally invalid and refused, which is preferable to silently defaulting its missing effort.

**Stored configuration always equals the actual fleet.** Whenever a command changes a role's harness, model or effort, it
rewrites `@agentctl_fleet` in full with the new values, so the option can never disagree with the live windows.

**`@agentctl_roles`** is the declared roster: the role names from `--roles`, comma-joined, in declaration order. It
exists because without it a dead agent is *unobservable*. Every other status input is derived from windows that still
exist, so a role whose window has closed leaves no trace to report — and since managed windows run without
`remain-on-exit` (§6.3), a crashed agent's window closes. The roster is what lets `status` say `missing` instead of
silently omitting the role, which is the difference between status doing its job and status being actively misleading.
It uses the same tmux-option mechanism as every other field, so "no metadata database" still holds. `launch` stamps it
once, immediately after `@agentctl_version` — it is known from the validated config before any window exists, but the
session must exist to hold it, so it lands with the other session options rather than earlier.

### 6.6 Launch failure, ownership, and exact messages

Rollback is gated on **ownership**, and ownership begins at exactly one instant: when `new-session` returns output that
parses into a session ID. Before that, agentctl owns nothing and destroys nothing. After it, the typed ID is the only
thing rollback ever targets — a session is never killed by name (§13.1).

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
| fails (tmux/parse) | yes | `new-session` refuses → **exit 6** carrying tmux's own `duplicate session: NAME` |
| context cancelled | — | propagate the sentinel; **no creation call** |
| — | — | any other creation failure → exit 6 (§6.6 pre-ownership: nothing killed) |

**The same condition can produce exit 3 or exit 6**, depending on whether the advisory check ran. That is deliberate,
not a defect: exit 3 is agentctl reporting a session-state fact it observed, and exit 6 is agentctl relaying a tmux
command that failed, carrying tmux's own message. Both are true statements about what happened (§1.1); they differ
because what happened differs.

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

**Why no stderr matching.** The two no-server states emit *different* messages — `error connecting to <path> (No such
file or directory)` when the socket is absent, and `no server running on <path>` once a server has exited. A string
match would have to know both, and neither is a documented contract. Falling through on *any* failure requires
knowledge of neither.

**Scope.** This applies to `launch` alone. `status`, `kill` and `attach` operate on a fleet that must already exist, so
"no server" genuinely means "nothing to act on", and §4.1's exit 6 carrying tmux's message remains correct for them.
Only a command that is about to create a server has standing to proceed without one.

### 6.8 relaunch

`agentctl relaunch ROLE [--session S] [--harness H] [--model M] [--effort E] [--dir PATH]` recreates **one absent** role window
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
   values are re-validated against §7 on read, because tmux options are advisory (SECURITY.md residual 4).
3. **Stored mode**: the role's harness, model, effort and directory come from the options; `--harness`, `--model`, `--effort` and `--dir`
   each override their own field. **Legacy mode**: refuse (exit 3) unless `--harness` *and* `--dir` are supplied
   (`--model` and `--effort` are optional; absent means empty, the harness defaults). The directory is **never** defaulted to the
   invocation cwd — relaunching one role somewhere the rest of the fleet does not live is exactly the silent
   divergence this refusal exists to prevent.
4. Window precondition: **exactly zero** windows match ROLE. Any match → exit 4, rendering the observed state by §6.3
   precedence and every matching window ID (§13.5). `dead` is refused like any other survivor: a dead window exists and
   its pane may still hold evidence, so relaunch never kills and recreates it. A **zero-pane** window refuses too —
   creating beside it would manufacture the `ambiguous` §13.5 fails closed on.
5. Preflight `tmux`, `amq` and the effective harness on `PATH` → exit 7. The effective directory must exist and be a
   directory: supplied by `--dir` → exit 2 (parity with launch, §7); read from metadata → exit 3, naming the stored
   path and `--dir` as the remedy, because a stale recorded path is a session-state fact, not a usage error.
6. Create with §13.2 row 3 exactly: `CMD` per §12.1 through the same `shellq` path, `-c DIR`, `-P -F`, and **no index
   argument** — the window lands at the next free index. Operator-visible ordering is unaffected because `status`
   iterates the roster, not the window list. Unparseable creation output is pre-ownership: exit 6, kill nothing, and
   append `; a window named ROLE may exist; inspect with tmux list-windows`.
7. Stamp the window options in §6.5's per-window order, poll the baseline with §8's fixed parameters, and stamp
   `@agentctl_process`. If `--harness`, `--model` or `--effort` overrode a stored value, rewrite `@agentctl_fleet` in full
   afterwards. A `--dir` override does **not** rewrite `@agentctl_dir`: that option records the fleet's launch
   directory and the other roles still live there. The divergence is stated in the output instead.
8. Any failure after the new window's ID parses → `kill-window` on **that ID only**, exit 8:

   ```text
   agentctl: failed to relaunch ROLE; removed window W: CAUSE
   agentctl: failed to relaunch ROLE; failed to remove window W: CLEANUP_CAUSE (relaunch failure: CAUSE)
   ```

   Same shape and same rules as §6.6, including that `CAUSE` is mandatory in both variants. Exit 8's meaning therefore
   extends from "the session this invocation created was removed" to "**what this invocation created** was removed".
9. Success (exit 0) states the role, harness, model and effort (`""` in metadata, `default` in human output, §12.7), session,
   window ID, pane ID, directory, and the provenance of each configuration field — `stored`, `flag override`, or
   `flags`. This report is what keeps an override honest: `status` cannot detect a harness swap, so relaunch says so.

## 7. Validation rules (consolidated)

- Session and role names: `^[a-z0-9][a-z0-9_-]*$`.
- Model identifiers: `^[a-zA-Z0-9][a-zA-Z0-9._-]*$` (catalogue-free; charset-bound).
- Effort levels: allowlist per harness — `low`, `medium`, `high`, `xhigh`, `max` for both `claude` and `codex` (§3.2.1). Rejection names the harness, the rejected value, and the supported levels.
- `--efforts` shares every structural rule with `--models`: optional, non-nil-but-empty is a usage error, entries are `ROLE:VALUE`, duplicate role entries and entries for undefined roles are rejected, and empty list entries name the raw list and the entry index.
- Harnesses: `claude` | `codex` only.
- All rejection cases from the brief's Validation section: unknown harnesses, duplicate roles, duplicate model entries, models for undefined roles, missing values, empty `--roles`, trailing commas, whitespace in names, names beginning with `-`, duplicate command-line options.
- `--dir`: must be an existing **directory**. Non-existent path and existing-but-a-regular-file are both exit 2, evaluated before any tmux call (§6.1 step 4).

## 8. Process-identity policy

No name pattern-matching. Identity is established by observation at launch and verified by equality afterwards:

- **Check target.** The pane's *root* process, `#{pane_pid}` — stable across `exec` and unaffected by agent subprocesses. Never `#{pane_current_command}`, which tracks the foreground job and flaps to child commands (`bash`, `python`, …) while an agent runs tools.
- **Baseline (launch).** Poll `ps -o comm= -p <pane_pid>` until the `amq coop exec → exec(harness)` chain completes,
  then store the observed value in `@agentctl_process` (trimmed exactly once per §13.7). Timeout → launch failure and
  rollback (§6.6). Poll parameters are fixed, not tuned per call:

  | Parameter | Value |
  |---|---|
  | Timeout | 5s |
  | Cadence | 100ms |
  | First attempt | immediately, at t=0 |
  | Final attempt | guaranteed at the boundary before declaring timeout |

  Two conditions share the retry path: the sentinel for "no identity available yet" (`ps` reporting nothing for a pid
  that has not been replaced yet) and an observed value of literal `amq`. The `amq` comparison is against the exact
  trimmed value — the bare name, not a path — which holds because the window command invokes `amq` by bare name
  (§13.7). Neither condition is a tmux failure; both simply mean "not yet".
- **Verification (control/status).** Re-run the same `ps` query and require **exact equality** with the stored baseline. Mismatch → `unexpected-process` in status; fail closed (exit 5) for control commands. Empty/missing baseline also fails closed.

This handles Claude Code's versioned binary name (`2.1.220` at the time of the spike) without heuristics and is robust to future harness renames. It remains a safety guard against accidents, not an authentication mechanism or an idleness proof: a same-user process can forge metadata or match by renaming an executable.

## 9. Exit codes

The brief's table verbatim (0, 2–8). `kill` uses 3 for unresolvable/missing/unmanaged sessions and 6 for tmux failures.
Exit 8's claim extends from "the session this invocation created was removed" to "**what this invocation created**
was removed": `launch` rolls back a session, `relaunch` rolls back the single window it created (§6.8).
Exit 4 additionally covers a role that resolves to more than one window (§13.5). Exit 6 additionally covers a
pre-ownership creation failure during `launch`, carrying the operator warning in §6.6, and any resolver `Runner`/parse
failure, carrying tmux's own stderr (§4.1). Exit 3 additionally covers an unresolvable or ambiguous session (§4.1) and every `attach` refusal — missing, unmanaged,
or a version other than `1` (§6.4, §12.6); `attach` uses 6 when control mode cannot be established.

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

- Unit tests against the fake `Runner` asserting **exact argv** for every case in the brief's Testing section, plus: `kill` refuses unmanaged sessions; `--dir` propagates to `-c`; model charset rejections; effort allowlist rejections and per-harness effort rendering (`--effort LEVEL` for claude, `--config 'model_reasoning_effort="LEVEL"'` for codex), with an absent effort emitting no argument; baseline capture (polling, `amq`-transition, timeout → rollback); equality check against `@agentctl_process` including empty-baseline fail-closed; self-target guard (`$TMUX_PANE` == target pane refused, absent/different pane allowed).
- `status` (§6.3): state precedence exercised in order, each state reached with the higher ones inapplicable; multi-pane
  renders `unmanaged`; alive-pane-with-unavailable-identity and empty-baseline both render `unexpected-process`; zero
  panes renders `missing`; a roster role with no window renders `missing`; `unexpected-process` renders the observed
  executable, not the baseline; unmanaged session renders `managed:false` with an empty agents array and exit 0 while a
  non-`1` version still exits 3; the fake `Runner` recorded **no** row-7 calls and **no** row-14 call for any role whose
  state was decided before the process probe.
- Launch failure paths (§6.6): both exit-8 messages asserted **verbatim**, including the cleanup-failure variant with a
  cause; pre-ownership malformed creation output asserts exit 6, the `tmux ls` warning, and that the fake `Runner`
  recorded **no** `kill-session`. Baseline poll (§8): t=0 attempt, cadence, and a guaranteed boundary attempt before
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
  sequence and no index argument in the creation call; flag overrides re-encoding `@agentctl_fleet` **after** the
  baseline, and a `--dir` override leaving `@agentctl_dir` untouched; every refusal above with its exit code and
  message — ownership gate, roster defect, role outside the roster, each metadata defect, legacy session with and
  without the required options, and each §6.3 state a surviving window can be in, `dead` included; both rollback
  branches asserting `kill-window` on the created ID and **no** `kill-session`; a pre-ownership creation failure
  killing nothing; provenance rendered for stored, override and legacy cases; and the relaunched role passing `status`
  and the control chain against its **new** `@agentctl_process` baseline.
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
9. **Validation ownership.** `internal/config` owns all value semantics: `ParseFleet` (roles/models rules) and `ValidateSessionName`. `internal/cliflags` owns flag mechanics (duplicate-option rejection). The `--dir` existence/is-directory check happens at point of use in the launch flow (`internal/fleet`), not in `config`. An explicitly supplied but empty `--models` (or `--roles`) value is a usage error; an omitted `--models` is valid. Errors for empty list entries (leading/consecutive/trailing commas) name the raw list and the entry index, since no printable entry exists.

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
- **Row 5a is classified by exit status only.** No branch reads tmux's message text, consistent with §6.7's rejection of stderr matching on evidence.
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
- **Row 11a.** Exposed **solely** to roll back the window ID the current `relaunch` invocation created (§6.8). No
  command removes a window it did not create, and `kill-window` is never issued against a window discovered by
  listing. The `tmuxx` wrapper takes a typed `WindowID`, so a name can never reach `-t`.
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
  `#{window_id}⟨TAB⟩#{window_name}⟨TAB⟩#{@agentctl_managed}⟨TAB⟩#{@agentctl_version}⟨TAB⟩#{@agentctl_role}⟨TAB⟩#{@agentctl_harness}⟨TAB⟩#{@agentctl_model}⟨TAB⟩#{@agentctl_effort}⟨TAB⟩#{@agentctl_process}`
  Parse with `strings.SplitN(line, "\t", 9)`.
- **Unconstrained values go last.** Every field except `@agentctl_process` is charset-validated or allowlisted. `@agentctl_process`
  comes from `ps -o comm=` and may contain spaces (a value `weird name` was verified to round-trip intact), so it is
  placed last and absorbs any residue — a delimiter inside it cannot shift another field.
- **Reads always use `-v`.** Verified: `show-options -w` *without* `-v` quotes values containing spaces
  (`@agentctl_spacey "two words"`), which would require an unquoting routine and create a second quoting site,
  violating §5. With `-v` the raw value is printed verbatim.
- **`-q` is required on reads.** Without it, an unset option is `invalid option: NAME` on stderr with exit 1; with it,
  the result is empty output and exit 0.
- **Unset and set-to-empty are indistinguishable** (both empty, exit 0). This is acceptable and deliberate: the gate
  options `@agentctl_managed`, `@agentctl_version` and `@agentctl_process` are never legitimately empty, so empty means
  fail closed. `@agentctl_model` and `@agentctl_effort` are legitimately empty (§12.7) and gate nothing.
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
§6.5's metadata) and restarting a role whose window still exists (§6.8 refuses `dead` rather than folding it into
`missing`; a `restart` command would be filed separately if demand appears). `status` deliberately does not read
`@agentctl_fleet` or `@agentctl_dir`: consistency checking is not its job. The brief's acceptance criteria apply, extended by: `agentctl kill` refuses unmanaged sessions; model charset enforcement; per-harness effort allowlist enforcement; deterministic cwd propagation; process-identity baseline recorded and enforced; self-target guard on control commands.
