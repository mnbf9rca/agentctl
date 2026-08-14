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
| Effort validation (added 2026-08-03, issue #88; amended by issue #195) | Efforts are opaque harness-specific mode names validated by the same predicate as models: `^[a-zA-Z0-9][a-zA-Z0-9._-]*$`. The mandatory alphanumeric first character prevents flag smuggling, and the remaining charset makes TOML breakout in codex's configuration expression unrepresentable without freezing agentctl to a harness-version catalogue. Optional everywhere: a role with no effort emits no harness argument at all. |
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

codex's own reasoning-effort enum additionally carries `none`, `minimal` and `ultra`. agentctl treats effort names as
opaque harness-specific values and accepts any name matching the shared model/effort charset
`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`; the selected harness remains
responsible for accepting or rejecting a well-formed name. Rendering remains harness-specific through
`harness.Spec.effortArgs`.

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
agentctl run --session S --role R --harness H [--model M] [--effort L]
agentctl relaunch [--session S] [--harness H] [--model M] [--effort L] [--dir PATH] ROLE
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

At the §15 cutover, `run` is the exact foreground, no-tmux form above. All three identity flags are required and
`--dir` is rejected; the observed cwd is the session-wide durable fleet directory (§15.7).

**`status` never narrows silently.** Bare `agentctl status` reports **every durable fleet** (§15.6).
Ambient context — `AGENTCTL_SESSION`, or the tmux session the caller happens to be sitting in — does not select a
target for `status` and never reduces its output to one fleet; it may only *mark* which session the caller is in.
Only an explicit `--session S` narrows the report to one session, because only that is the operator saying which fleet
they mean.

There is no `--all` flag. It existed to make the listing reachable from inside tmux, where the current-tmux source
always resolved; a bare `status` that always lists makes it redundant, and a flag that changes nothing invites the
reader to believe it changes something.

`relaunch`, `clear`, `compact`, `kill` and `attach` each act on exactly one target, so each resolves one
through the full §4.1 chain.

### 4.1 Session resolution

Precedence for acting commands other than `launch`/`run`: explicit `--session` > `AGENTCTL_SESSION` > the current tmux
session. `launch` and `run` always require an explicit `--session` and never invoke the resolver.

At the §15 cutover, explicit and environment sources are validated selection facts and do not require a tmux session
of the same name. Only the current-tmux fallback calls `display-message` for the validated `TMUX_PANE`; its returned
validated name is selected directly. Runtime commands subsequently open the durable fleet and role namespace. The
pre-cutover list/exact-match rules below remain historical detail for §§1–14 and are superseded by this paragraph.

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

**`status` does not consult the ambient sources at all.** The matrix above governs `relaunch`, `clear`, `compact`, `kill` and
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

The Go module is standard-library-first (`flag`, `os/exec`, `encoding/json`, `regexp`, `testing`). A third-party
dependency is admitted only when it clearly reduces complexity and its package, version, and rationale are recorded in
the governing change. `github.com/santhosh-tekuri/jsonschema/v6` compiles the embedded launch-template schema rather
than maintaining a bespoke structural validator; its indirect `golang.org/x/text` dependency is visible in `go.mod` and
`go.sum`. There is no CLI framework or tmux client library. All tmux invocations are argv arrays via `os/exec` —
agentctl never invokes a shell. The only shell-interpreted string in the system is the window command tmux itself runs
via `sh`, assembled exclusively by `shellq` from charset-validated tokens.

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

`agentctl launch --from-template FILE` supplies fleet values from a JSON file. The embedded
`skills/agentctl/references/fleet-template.schema.json` is the single machine-readable artifact for the file's shape,
and `internal/launchtemplate` executes that artifact. Schema validation is structural only: a schema-valid template can
still be refused at launch because `internal/config` applies value semantics to the merged template-and-flag union
(§12.9).

**The template never carries the session name.** Session identity is per-invocation, so one template serves
`release_0_4_0`, `release_0_5_0` and every successor unedited, and identity can never come from a stale file. There is
no `session` key, and because a bare `unknown field "session"` would under-explain a mistake people will reasonably
make, it is refused by name:

```text
agentctl: template FILE: "session" is not a template field; session identity is supplied per invocation with --session
```

**Format is JSON.** The schema document declares its own JSON Schema dialect; a template never carries `$schema`.
agentctl already embeds the schema, so accepting a caller-supplied schema name would either silently ignore a declared
contract or create a caller-chosen read/fetch path. `$schema` is therefore refused by name and points authors to the
installed skill reference.

#### The union, and where each requirement binds

Flags and the template compose: **the effective fleet is the union of the two.** A flag value for a
template-declared role or field overrides it; a flag role the template does not declare is added. No flag removes a
template-declared role — removal was considered and not granted, so a template is a floor, never a ceiling.

Partial templates are legal by construction, because validation applies to the union rather than to the file. The
schema owns file-local structure; requirements such as one harness per effective role and at least one effective role
bind on the union. An empty or omitted roles list is therefore legal in a defaults-only template, though the effective
union can still be refused for having no roles.

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

1. **Open the file non-blockingly, then verify the handle** — `os.OpenFile` with `O_RDONLY|O_NONBLOCK`, then `Stat` on
   the descriptor. The flag is load-bearing, not defensive: a plain `O_RDONLY` open of a FIFO with no writer **blocks
   inside `open(2)` itself**, before any descriptor exists, so the refusal in rule 2 could never run and `launch` would
   hang exactly where that rule promises it cannot. `O_RDONLY|O_NONBLOCK` returns immediately on a FIFO regardless of
   writers, which is what gives rule 2 a descriptor to refuse. Verified on this platform: a plain open of a writerless
   FIFO did not return, the non-blocking open returned at once and `Stat` reported `ModeNamedPipe`, and the same flags
   on a regular file opened and read normally. `O_NONBLOCK` is inert for regular-file reads, so once rule 2 has
   accepted the descriptor the remaining steps are unaffected and nothing clears the flag. Never `Stat` the path and then
   read the path: checking one object and reading another is a time-of-check/time-of-use gap, and checking the
   descriptor that will actually be read has none.
2. **Regular files only.** A directory, FIFO, device or socket is refused, naming what it is. A template is a regular
   file: `-` is not special and stdin is not accepted, deliberately rather than by omission.

   The reason to refuse a FIFO is **not** that it would hang — rule 1's non-blocking open already prevents that, and a
   reader who takes the hang as this rule's justification will conclude the rule is now redundant. It is not, and the
   hazard it guards is worse for being quiet: under `O_RDONLY|O_NONBLOCK` a writerless FIFO **reads as an empty file**
   — verified on this platform, `read` returned 0 bytes with a nil error — so a template that reached the decoder
   would fail as malformed JSON rather than as the wrong kind of file, sending the operator to inspect a document that
   was never read. With a writer present the same read can return a *partial* document, which is worse again, because
   a prefix of valid JSON can decode successfully into a fleet the caller never described (§1.1).

   The other reasons are structural and apply to every non-regular file: there is no stable size for rule 4's cap to
   bound, and nothing can be re-read to quote the offending line in an error.
3. **Symlinks are followed, and every rule binds on the target.** Refusing them would break ordinary arrangements — a
   templates directory kept in a dotfile repository — for no threat-model gain, since a same-user process that can
   plant a symlink can plant the file itself.
4. **Size cap, enforced by rejection.** Read through a limit of 1 MiB plus one byte, and refuse a file that exceeds it.
   Truncating instead would launch a fleet that differs from the file the caller wrote (§1.1).
5. **Version before schema validation.** A token pre-pass reads `version` and rejects any key repeated within any
   object, anywhere in the document — `encoding/json` is silently last-wins where the CLI errors. It preserves the
   lexical-exact `1` check; the embedded schema carries the matching declarative `const` rule.
6. **Embedded schema validation** rejects instances outside the published file shape, including unknown fields.
   Trailing content remains a decoder-level refusal because a schema validates one JSON value, not a byte stream.
7. **Effective-union values go through §7's validators, unchanged.** The schema has already decided the file-local
   `dir` and role-name rules; harness, model, effort, and effective-fleet requirements bind after the merge.

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
agentctl: template /srv/fleet.json: roles[1].effort: effort "bad effort" must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$
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

- Session names and flag/control role names: `^[a-z0-9][a-z0-9_-]*$`. Template role names carry the same pattern in
  the embedded schema, which alone owns that file-local rule. Session and role names are each capped at **32 ASCII
  bytes**; the regex makes characters one byte, so byte and character counts cannot diverge. The cap is enforced by
  `internal/config` for every flag, environment, template-union, stored-record, and control-role input.
- Model and effort identifiers: `^[a-zA-Z0-9][a-zA-Z0-9._-]*$` (catalogue-free; charset-bound), implemented by one shared compiled expression. Effort rejection names the rejected value and the charset; a well-formed value is left for the selected harness to accept or reject (§3.2.1).
- `--efforts` shares every structural rule with `--models`: optional, non-nil-but-empty is a usage error, entries are `ROLE:VALUE`, duplicate role entries and entries for undefined roles are rejected, and empty list entries name the raw list and the entry index.
- Harnesses: `claude` | `codex` only.
- All rejection cases from the brief's Validation section: unknown harnesses, duplicate roles, duplicate model entries, models for undefined roles, missing values, empty `--roles`, trailing commas, whitespace in names, names beginning with `-`, duplicate command-line options.
- `--dir`: must be an existing **directory**. On `launch`, a relative value is made absolute before validation and
  reuse; on `relaunch`, it is an explicit one-invocation override and is not persisted. Non-existent path and
  existing-but-a-regular-file are both exit 2, evaluated before any tmux call (§6.1 step 4).
- **Template `dir` must be POSIX-absolute.** The embedded schema alone expresses this file-local rule as `^/`; a
  relative value is refused, naming it and pointing at `--dir`. A template is a
  portable artifact, so resolving against the process working directory would let one file launch two different fleets
  from two directories with no warning — the silent divergence §6.8 already refuses when a *stored* directory is
  relative, for the same reason: no trustworthy base remains. Resolving against the template's own directory was
  rejected as worse still, because it would give `--dir foo` and `"dir": "foo"` different meanings for one field.
  Nothing is lost: `--dir` still accepts a relative path and still overrides the template per field. Where a template
  must stay portable across machines, the answer is to omit `dir` from the file and supply `--dir` at invocation.
- **When `--from-template` is supplied, the validation subject is the union** of template and command line (§6.9), not
  either source alone. Effective harness, model, and effort values reach the union rules identically whichever source
  they came from; the schema has already decided the file-local `dir` and role-name rules.
- **One implementation per rule.** The embedded schema owns template-file `dir` and role-name rules.
  `internal/config` owns the matching flag/control role rule and effective-union harness, model, and effort rules.
  The CLI list parsers and control commands may format a shared predicate's failure differently, but none re-decides
  it. Existing tests pin the CLI message text and must stay green **unchanged**. A pinned message that has to move is
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

At the 0.5.0 atomic cutover, §15.8 is the complete public exit map, including foreground child outcomes,
runtime/shim refusal classes, retained ownership, and `fleet-directory-disagreement`. The pre-cutover derivation below
is retained only as history where §15 does not supersede it.

`invalid-root` exit 2 covers those declared-root, fixed-path traversal/creation, and private-boundary failures reported
as `InvalidRootError`. Descriptor observation and substitution failures retain the existing `unclassified` exit-1 row;
during setup its `SESSION` substitution is exactly `""`. A durable ancestor with a mode other than `0700`, including
`$HOME` or the `os.UserConfigDir()` result, selects neither branch and emits no refusal; §15.2 makes the final state
directory the validated durable boundary.

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

- Unit tests against the fake `Runner` asserting **exact argv** for every case in the brief's Testing section, plus: `kill` refuses unmanaged sessions; `--dir` propagates to `-c`; model and effort charset rejections; per-harness effort rendering (`--effort LEVEL` for claude, `--config 'model_reasoning_effort="LEVEL"'` for codex), including a well-formed value outside the originally verified five and an absent effort emitting no argument; baseline capture accepts `claude` rather than transient `env` for `[amq, env, claude, claude]`, accepts the first stable pair without extra polling for `[amq, claude, claude]`, and, when observations remain unsettled through the timeout boundary, stamps no `@agentctl_process`, stamps `@agentctl_unproven` as that role's final call instead, issues no kill, and lets the remaining roles continue (§6.6) while `relaunch` still rolls back its own created window (§8); a `no-baseline` window without that record is refused by recovery with no kill issued; equality check against `@agentctl_process` including empty-baseline fail-closed; self-target guard (`$TMUX_PANE` == target pane refused, absent/different pane allowed).
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

1. **Window-command assembly site.** Assembly remains confined to `internal/fleet` as the string
   `"exec " + shellq.Join(argv)`: `shellq` quotes and joins, `fleet` prepends the unquoted `exec` shell keyword, and
   the result is passed to `tmuxx`. The shipped authorized set is exactly one site:
   `internal/fleet/shim.go:shimWindowCommand` for the hidden-shim argv. The structural guard pins that named site and
   rejects a second, indirection, or a move/rename. No other package may compose shell-interpreted strings.
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
9. **Validation ownership.** The embedded launch-template schema (§6.9) owns the file's shape plus its file-local
   POSIX-absolute `dir` and role-name pattern. `internal/config` owns flag/control value semantics and the
   effective-union harness, model, and effort rules through `ParseFleet`, its shared predicates, and
   `ValidateSessionName`; schema-approved harness/model/effort strings are inputs to those checks after merging.
   `internal/cliflags` owns flag mechanics (duplicate-option rejection). The `--dir` existence/is-directory check
   happens at point of use in the launch flow (`internal/fleet`), not in `config`. An explicitly supplied but empty
   `--models` (or `--roles`) value is a usage error; an omitted `--models` is valid. Errors for empty list entries
   (leading/consecutive/trailing commas) name the raw list and the entry index, since no printable entry exists.

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
- **Window IDs are not reused within one tmux server lifetime, verified on tmux 3.7b (2026-08-09).**
  `hack/probe-3-ids.sh` created window `@5`, killed that exact ID, then created window `@6`; the probe fails if the
  second ID equals the first.
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
`@agentctl_fleet` or `@agentctl_dir`: consistency checking is not its job. The brief's acceptance criteria apply, extended by: `agentctl kill` refuses unmanaged sessions; model and effort charset enforcement; deterministic cwd propagation; process-identity baseline recorded and enforced; self-target guard on control commands.

## 15. Approved 0.5.0 per-agent shim contract

This section supersedes the tmux-window identity and direct `send-keys` parts of §§1–14. The atomic 0.5.0 public
cutover is shipped: runtime shims are the sole production identity and delivery plane, while tmux is optional
presentation. The
[options paper](2026-08-09-issue-182-identity-delivery-options.md) is rationale and evidence only. Option S is selected
and no behavioral branch remains open. An empirical invalidation would require a new approved design delta before the
paper's Option A fallback could replace this contract.

### 15.1 Authority, architecture, and command surface

The runtime plane is authoritative for role identity, liveness, status, and control. One resident agentctl shim owns
one role, holds its kernel claim, runs the unchanged `amq coop exec ... HARNESS` argv on a nested PTY, and serves one
versioned local socket. The request's only delivery instruction is one closed-registry operation name; its other
permitted values are framing/version and validated session/role identity, and the response carries only
framing/version and typed objective facts. The server resolves the operation through `internal/control`; no request or
decoder field can represent payload bytes, raw keys, slash commands, arguments, model values, environment values, or
caller text. The exact §15.5 schema is not broadened. The shim is the sole writer to the harness PTY, serializing
relayed operator input and registered control bytes at one point.

tmux is optional presentation and fleet-launch plumbing. `launch` may create tmux windows whose command starts
the shim, fleet-level `attach` is tmux-only while per-role `attach ROLE` requires no tmux (§15.11), and tmux
observations may enrich status, but no tmux session/window/pane name,
option, layout, or process row establishes role identity or permits delivery. `agentctl run` starts one foreground
role without tmux. The internal shim entrypoint is hidden from public help, `commandUsage`, the embedded skill, and
agent-facing inventories. No migration or dual dialect is supported; the public production paths switch atomically
and protocol version skew fails closed.

The public foreground syntax is exactly one of:

```text
agentctl run --session SESSION --role ROLE --harness HARNESS [--model MODEL] [--effort EFFORT]
agentctl run --session SESSION --role ROLE --from-template FILE
```

`--from-template` excludes `--harness`, `--model`, and `--effort`; supplying any of them with it is a usage refusal
naming both. In the template form `run` reads only the named role's harness, model, and effort. It ignores the file's
`presentation` and `dir` members entirely — the foreground path has no presentation, and §15.7's cwd rule is
unchanged. A role absent from the template, or named more than once in it, is a refusal before any runtime mutation.
In the flag form all three identity flags are required. `--dir`, positional child commands, payloads, raw keys, and environment
overrides are rejected. The runner uses the caller's already-observed cwd and the same lifecycle server as tmux launch.

The hidden argv is exactly:

```text
agentctl __shim --session SESSION --role ROLE --harness HARNESS [--model MODEL] [--effort EFFORT]
```

It accepts no positional child command, payload, raw key, environment override, directory, or arbitrary argument.
The handler validates every supplied value, reconstructs the unchanged child argv through `harness.AgentArgv`, and
inherits its already-selected working directory and environment. Fleet wiring starts the current executable through
this route; it is not a public lifecycle command and does not appear in any command inventory.

| Package | Approved responsibility |
|---|---|
| `internal/shim` | namespace roots; lifetime `flock`; advisory lockfile; durable role records; version-first codec; `LOCAL_PEERPID`; raw process observation; server/client boundary |
| `internal/ptyx` | standard-library Darwin nested PTY, child launch, relay, resize/termios observation, readiness, serialized writes |
| detached spawn boundary | typed parent-specified `os/exec` spawn of the hidden shim command for detached launch (§15.11.6); recording fake in tests; no shell |
| `internal/fleet` | tmux-backed shim launch or foreground composition, roster/config persistence, rollback, relaunch, kill |
| `internal/status` | runtime-first enumeration and §15.6 precedence; tmux presentation is additive only |
| `internal/control` | closed operation registry and shim client dispatch; no caller-payload-bearing delivery API |
| `internal/tmuxx` | optional presentation/create/attach/kill operations only; production payload delivery is removed |
| `internal/target` | removed; no production or compatibility target-chain package remains |

`launch`, `relaunch`, `kill`, `status`, `clear`, and `compact` do not use tmux as identity or delivery evidence.
Fleet-level `attach` requires an observed tmux presentation; per-role `attach ROLE` requires the role's attach
stream instead and is categorically refused for a tmux-presented role (§15.11.1).

### 15.2 Namespace, declared roots, and length predicates

For decimal uid `UID`, the production volatile root is `/tmp/agentctl-UID/v1`. Per-role artifacts are:

```text
/tmp/agentctl-UID/v1/<session>/<role>.lock
/tmp/agentctl-UID/v1/<session>/<role>.sock
/tmp/agentctl-UID/v1/<session>/<role>.attach
```

`UID` is treated at its worst case of 10 decimal bytes. With the §7 caps, the longest production socket pathname is the
attach stream, whose suffix is two bytes longer than the control socket's:
`/tmp/agentctl-<10-digit-uid>/v1/<32-byte-session>/<32-byte-role>.attach`: **100 pathname bytes, 101 including NUL**,
within Darwin `sun_path[104]` with three bytes to spare. The control socket's worst case is 98 bytes, 99 including NUL.
Every resolved socket path is checked against the ceiling, and the attach path is the one the boundary tests use. The resolved socket path is still checked and refused before claim or role mutation when it is
104 bytes or longer. `AGENTCTL_RUNTIME_ROOT` is a test/release-verification override, not a trust anchor; it must be
absolute, at most 1024 bytes, and pass the same descriptor and ownership checks. The name caps are necessary but not
sufficient for an override: the independent resolved-path check always runs.

The production durable root is `os.UserConfigDir()/agentctl/state-v1`; `AGENTCTL_STATE_ROOT` is its declared
test/release-verification override. `$HOME`, the `os.UserConfigDir()` result, and `AGENTCTL_STATE_ROOT` remain bounded
declared inputs: each supplied path must be nonempty, absolute, clean, and at most 1024 bytes. The final resolved
`state-v1` directory is the durable private boundary: it is opened descriptor-relatively and verified as a nonsymlink,
same-user, mode-`0700` directory. Ancestor directory modes, including `$HOME`, the `os.UserConfigDir()` result, and a
pre-existing `agentctl` directory, do not gate execution. A missing `agentctl` ancestor and final `state-v1` directory
are each created mode `0700`; a pre-existing ancestor is not repaired. Traversal and creation must still succeed, and
the fixed `agentctl` component must be a directory rather than a symlink. The state-root override receives the same
final directory validation. Both overrides and `$HOME` are same-user-selectable residual surfaces, never
authentication.

The volatile tree keeps its separate, stricter discipline: every created component is `0700`, and unsafe type, owner,
mode, symlink, or descriptor substitution refuses. Socket, lock, and record files are `0600`. Predictable pre-creation
of `/tmp/agentctl-UID` can deny service; agentctl refuses and never adopts or repairs an unsafe tree. This is a
refusal-only denial surface.

The durable role record is exactly:

```text
<resolved-state-root>/sessions/<session>/roles/<role>.json
```

It holds reservation/child/config facts and survives volatile-tree deletion and reboot. The uid-anchored lockfile body
records the shim PID, nonce, fully resolved state root, and protocol metadata as advisory facts. A client first reads
that root and compares it with its independently resolved root. A mismatch is `state-root-disagreement`; it never
enumerates the alternate tree or reports `missing`.

R19 extends the existing version-1 role record before the 0.5.0 release as a pre-release flag-day schema change. Its
`state` is exactly `child-starting`, `child-recorded`, or `cleanup-failed`. The first two retain their existing closed
fields. `cleanup-failed` requires the existing version/session/role/shim PID/nonce fields and `child_pid`, permits
`child_start_token` only when it was observed, and adds exactly one `cleanup` object:

```json
{"cause":"CAUSE","observation":"present-match","remaining":["child","socket","record","lock"]}
```

`cause` is a nonempty observed failure string. `observation` is exactly `present-match`,
`present-token-disagreement`, `present-not-ours`, or `could-not-observe`. `remaining` is nonempty, contains each
applicable value from the closed `child`, `socket`, `attach`, `record`, `lock` vocabulary at most once, and uses that
fixed order. The nested object rejects duplicate, unknown, missing, wrongly typed, and unknown-valued fields. A cleanup
object on another state, a missing cleanup object on `cleanup-failed`, or any unknown state is malformed record
content and therefore `invalid-record`. Filesystem, permission, descriptor-substitution, and other failures to read a
record are observations that could not be completed, not malformed content. Provenance: planner R19,
2026-08-10/11.

Launch owns a separate session-level durable fleet record; it does not extend the per-role record above. Its path is
exactly:

```text
<resolved-state-root>/sessions/<session>/fleet.json
```

Its version-1 JSON schema is exactly:

```json
{"version":1,"session":"SESSION","directory":"DIRECTORY","presentation":"PRESENTATION","roster":["ROLE"],"roles":{"ROLE":{"harness":"HARNESS","model":"MODEL","effort":"EFFORT"}}}
```

`presentation` is required and is exactly `detached` or `tmux`. It records the launch decision of §15.11.1 so a later
command can distinguish a detached role from a tmux role whose presentation has disappeared — a distinction no runtime
observation can recover. Any other value, or its absence, is `invalid-record`. Foreground `run` creating a new
one-role fleet stores `detached`; `run` adding or replacing a role in an existing fleet neither reads nor changes the
stored value, and that role is relaunched in the fleet's stored presentation like any other.

`roster` is the nonempty declaration-order list. `roles` is an object keyed by role, contains exactly the roster keys,
and each value requires exactly `harness`, `model`, and `effort`; empty model and effort strings retain their existing
default semantics. `session`, every roster entry and role key, harness, model, effort, and the absolute directory pass
their existing validators. The stored session must equal the session selected by the path. The top-level writer order
is `version`, `session`, `directory`, `presentation`, `roster`, `roles`; role-object keys encode in lexical order and their field order
is `harness`, `model`, `effort`. A trailing newline is written. The complete file including that newline is at most
65536 bytes. The decoder performs a version-only pre-pass before interpreting any other field, then rejects duplicate,
unknown, missing, wrongly typed, inconsistent, or trailing data at the top level and inside every role object. The file
is a nonsymlink, same-user, mode-`0600` regular file beneath retained mode-`0700` session directories.

Creating `<session>` with one exclusive `mkdir` serializes concurrent fleet launches: one caller creates and commits
the complete record; an `EEXIST` contender is `fleet-config-exists` and mutates nothing. The fleet record uses a
same-directory mode-`0600` temporary file, complete write, file sync, close, rename to `fleet.json`, session-directory
sync, and `sessions`-directory sync. A failure before rename removes only that invocation's empty session reservation
and synchronizes `sessions`; no role start has been attempted. A failure after rename or either following directory
sync is `RecordCommitUncertainError`: the visible record is retained, no role starts, and no retry or absence inference
is permitted.

An overriding `relaunch` changes this record only after the replacement shim has answered ready with the created shim
PID. It acquires `flock(LOCK_EX|LOCK_NB)` on the retained session directory only for a version-checked read/modify/write,
preserves version/session/roster, and replaces the directory and selected role configuration atomically. Contention or
a record differing from the caller's prior read is `fleet-mutation-conflict`; it never overwrites the peer value.
A definite replacement failure writes no fleet change and enters the owned-role rollback rules below. A post-rename
sync failure is `RecordCommitUncertainError` with phase `fleet-config`: the ready role, presentation, and visible fleet
record are retained, with no retry or cleanup. This session mutation flock is not role ownership; only the role
lockfile's lifetime flock establishes role ownership.

Foreground `run` composes the same record without tmux. For an absent session it creates the complete one-role fleet
record before starting the role. For an existing fleet it reads the record first and requires the caller's observed
cwd to equal the record's one session-wide `directory` byte-for-byte. A mismatch refuses before role start, claim,
presentation, or record mutation and renders both paths through the `fleet-directory-disagreement` row. The version-1
schema continues to have exactly one session-wide directory; per-role directories require a future approved schema
and behavior delta.

When the directory agrees, `run` starts the requested role and waits for the same readiness fact as tmux launch. Only
after readiness does it use the session mutation flock to extend an absent roster role in declaration order or replace
that role's stored harness/model/effort. A definite fleet-record mutation failure stops only the newly owned role and
uses the ordinary owned-cleanup rules. A `fleet-config` commit uncertainty retains the ready role and visible record,
performs no stop/retry/removal, and waits for the foreground child before returning the uncertainty. A failure before
readiness leaves an existing fleet record unchanged; if this invocation created the one-role fleet record, cleanup
removes it only after owned role absence is observed.

### 15.3 Ownership instant and complete failure/rollback rules

Role ownership begins at exactly one instant: successful `flock(LOCK_EX)` on the role lockfile. The lock is held for
the shim lifetime and is the only ownership arbiter. The file body and socket are advisory; bare socket bind and
probe-connect/unlink/rebind reclaim are forbidden. POSIX record locks are forbidden because closing an unrelated
descriptor can release them. Reclaim after death is lock acquisition, never socket probing.

Startup order is fixed:

1. Validate every name, root, descriptor, resolved path length, argv, and durable config before role mutation.
2. Open safely and acquire `flock(LOCK_EX)` once. This is the ownership instant.
3. Write the advisory lockfile body and atomically persist `child-starting` with shim PID and a fresh nonce before
   child fork/start.
4. Create the nested PTY and start the unchanged harness argv through the injected non-shell child boundary.
5. After `os/exec.Start` reports success, observe child PID and raw `kinfo_proc.p_starttime` timeval and atomically
   upgrade the durable record.
6. Bind/listen while the claim remains held, start relay, and run the exact readiness observation below. No control is
   accepted.
7. Publish ready only after listener, relay, durable child identity, and the readiness predicate all exist.

The readiness constants and predicate are fixed:

| Constant | Value |
|---|---:|
| `ShimReadinessPollInterval` | `50ms` |
| `ShimReadinessTimeout` | `5s` |
| `ShimReadinessLocalFlagMask` | `ICANON | ECHO` |
| `ShimOwnedTeardownPollInterval` | `50ms` |
| `ShimOwnedTeardownTimeout` | `5s` |

The shim calls `TIOCGETA` on the retained PTY master; on Darwin that snapshot observes the slave's shared line
discipline. The first observation is at `t=0`, subsequent observations are on fixed 50ms boundaries, and a final
observation is guaranteed at `t=5s` (101 scheduled observations including both boundaries). The harness tty is ready
only when one snapshot has both `ICANON == 0` and `ECHO == 0`, while the listener and relay remain live and the
recorded child has not exited. Terminal contents, prompts, timing since the last byte, and harness-specific strings are
never readiness evidence.

`EINTR` retries `TIOCGETA` immediately within the same scheduled observation; another ioctl error is
`readiness-observation-failed` immediately. If repeated `EINTR` consumes the final deadline, the same outcome carries
the exact cause `TIOCGETA remained interrupted through the 5s readiness deadline`; it is not called a cooked-mode
timeout because no final flag snapshot was observed. An observed child exit before readiness is
`child-exited-before-ready`. If the final successful snapshot still has either masked bit set, the typed cause is
`readiness-timeout` and records the two final booleans exactly as `ICANON=true|false ECHO=true|false`. Every one of
these post-start failures enters the cleanup/absence rules below; none publishes ready.

Failure before step 2 owns no role: remove only artifacts created by this invocation, signal no process, and report
the observed input/tool fact. Failure after ownership but before a successful child start removes this invocation's
socket and `child-starting`, releases the lock, and returns to absence because failed start proves no child started.
Failure after successful child start closes readiness/listening, sends SIGHUP to the owned PTY/process group, and
applies §15.4 immediately, every 50ms, and at the inclusive 5s boundary. Observed `ESRCH` ends the poll. The final
non-ESRCH observation is retained and rendered through the exit-9 row; there is no automatic SIGTERM/SIGKILL
escalation or extra grace period. Version-pinned SIGHUP evidence informs teardown but never proves absence.

Only observed `ESRCH` permits deleting the durable child record or reporting absent/relaunchable. Matching live child,
`EPERM`, token disagreement, token-reader failure, or another observation retains the record and reports the exact
orphan/indeterminate/cleanup fact. If child start succeeded but PID/token upgrade failed, retain `child-starting`.
Listener, readiness, relay, cancellation, and cleanup failures after child start follow the same rule. Fleet rollback
removes only roles/sessions created by that invocation, in child-before-shim order; it never destroys a peer or calls
an uncertain child absent.

Before releasing a still-owned claim after a non-ESRCH cleanup observation, the lifecycle atomically replaces its
existing role record with the R19 `cleanup-failed` form. It records only the child and socket/record/lock artifacts it
observed remaining. This makes the state reachable from both startup rollback and normal server teardown; a write
failure remains a reported cleanup error and never licenses absence. If initial start-token observation failed,
cleanup repeats only `kill(pid,0)`: nil proves presence but cannot prove identity, so the durable observation is
`could-not-observe` and `child_start_token` is omitted. It never compares against a zero or invented token.

The launch-owned fleet record precedes every presentation command and role start. A presentation creation result that
does not return valid typed owner IDs cannot prove whether the command started a shim: launch retains the fleet record,
every presentation, and every prior ready role, and attempts no stop or removal. Once typed created IDs exist, a later
readiness/start failure stops this invocation's roles in reverse roster order. Only a response carrying all three
separate facts `signal_attempted=true`, `signal=SIGHUP`, and `child_exit_observed=true` permits exact owned
presentation removal followed by fleet-record removal. Any stop error, survivor, absent signal-attempt fact, absent
exit-observation fact, presentation cleanup error, record uncertainty, or remaining durable role artifact retains the
fleet record; no rollback may delete it while any role child might live.

Every lifecycle start first reads the durable role path and refuses an existing record before claim or fork; only the
later relaunch/absence path may remove one after §15.4 returns ESRCH. Atomic record writes rename a complete temporary
file and then synchronize the containing directory. If rename succeeded but directory synchronization failed,
`RecordCommitUncertainError` reports that the replacement is visible while crash durability is unproved. Its first
consumer must stop that lifecycle phase, retain the visible record, and refuse to infer whether the prior or
replacement record survives a crash. For `child-starting`, no fork follows. For `child-recorded`, teardown still uses
§15.4, but the uncertain record is not silently overwritten or described as absent. The exact CLI-visible result is
`record-commit-uncertain` in §15.8. Provenance: the standing PR-2 reviewer gate rider, carried at its first consumer in
PR 4 and confirmed by the planner's 2026-08-10 R16 response.

A dead shim plus `child-starting` has no timeout and never self-resolves. For manual recovery, read
`<recorded-state-root>` from the lockfile body—not the current environment—and, only after independently verifying no
child remains, manually remove:

```text
<recorded-state-root>/sessions/<session>/roles/<role>.json
```

No production path automatically removes that indeterminate record.

### 15.4 Sole absence oracle and child identity

`kill(pid, 0)` is the sole child presence/absence oracle:

| Observation | Factual result | May report absent or relaunch? |
|---|---|---|
| `ESRCH` | child absent | yes; the only permitting result |
| nil | child present; read raw start token next | no |
| `EPERM` | child present-not-ours | no |
| any other error | could-not-observe | no |

After nil, read Darwin `kinfo_proc` through `sysctl(KERN_PROC_PID)` and compare raw `p_starttime` timevals. Equality is
`present-match`; inequality is `present-token-disagreement`; a syscall failure, short result, or malformed result is
`could-not-observe`. A token-reader error never substitutes for absence. Formatted `ps`, locale, timezone, child
environment, and wall-clock conversion do not participate.

### 15.5 Socket enforcement and target-chain disposition

A client resolves the validated session/role path, reads the advisory lockfile, connects, and checks protocol version
before parsing any other field. The protocol constants are:

| Constant | Value |
|---|---:|
| `ShimProtocolVersion` | `1` |
| `ShimFrameHeaderBytes` | `4` |
| `ShimFrameMaxPayloadBytes` | `4096` |
| `ShimProtocolIOTimeout` | `2s` |

Each frame is a four-byte unsigned big-endian payload length followed by exactly that many UTF-8 JSON bytes. Lengths
from 1 through 4096 are accepted; zero, a value above 4096, EOF after any header/payload byte, invalid UTF-8, trailing
bytes inside a single JSON value, and a non-object top level are `protocol-frame-read-invalid`. A zero-byte EOF before
the client request is a nonfatal peer abort because clients close at that boundary after skew, answerer, or ancestry
refusal. A connection carries,
in order, exactly one server hello, one client request, and one server response, then the server closes it. The hello
payload encoded by version 1 is exactly `{"version":1}`. The request schema contains only `version`, `session`,
`role`, and `operation`; `operation` is one closed registry name. Ruling R16 (planner answer to the PR-4 builder,
2026-08-10, reviewer-gated in PR 4) divides that registry into the following exact, argument-free kinds:

| Operation | Kind | PTY effect | Exact response outcomes |
|---|---|---|---|
| `clear` | payload | closed clear bytes, registered `/clear` bytes, fixed 1s delay, closed submit bytes | `delivery-submitted`, `delivery-cancelled-clean`, `delivery-cancelled-with-residue` |
| `compact` | payload | closed clear bytes, registered `/compact` bytes, fixed 1s delay, closed submit bytes | `delivery-submitted`, `delivery-cancelled-clean`, `delivery-cancelled-with-residue` |
| `observe` | control | none; it never calls the PTY writer | one applicable §15.6 state outcome with its required recorded/process facts, plus `stopping` or `stopped` during serialized stop |
| `stop` | control | none; it never calls the PTY writer | `stop-child-exited`, `stop-child-retained`, or `stop-already-stopping` |

`observe` applies the §15.4 kill/token oracle and returns advisory-record, child, answerer, and confidence facts only
at their specified provenance. `stop` attempts the closed `SIGHUP` process-group signal and reports
`signal_attempted` separately from `child_exit_observed`; a signal attempt is never exit evidence, and a survivor is
retained. A non-wire signal/file side channel is inadmissible because it cannot return those §1.1 observations. The
versioned socket round trip is the one control channel for all four operations. A partial client frame, timeout, or
non-closure read/write transport failure is not discarded: the server surfaces its typed direction and peer, exits
through the applicable §15.8 client row, and performs the normal owned-runtime cleanup. Zero-byte pre-request closure
and peer closure while writing a response are nonfatal aborts; this preserves safety refusals and cancellation without
claiming request success or tearing down the resident role. A hello-write failure is always typed/fatal, including a
zero- or partial-progress peer closure, because no client refusal has yet been established.

Payload delivery and stop share one serialization gate that remains held through their response write. Stop first
changes the closed phase from `active` to `stopping`, then waits for any already-admitted payload operation to finish
and report its response; only after that response write releases the gate may stop attempt SIGHUP. A `clear` or
`compact` arriving in phase `stopping` or `stopped` returns `shim-stopping` with `state`, `shim_pid`, and `child_pid`
and writes no PTY byte. `observe` bypasses this mutation gate and remains admitted: it returns `stopping` while stop is
pending, or `stopped` after child exit has been observed and before owned teardown finishes, with the same three facts.
A second stop in either phase returns `stop-already-stopping` with `signal_attempted=false`; it attempts no second
signal and makes no PTY write. A stop survivor remains `stopping` through its response write, then returns to `active`;
an observed child exit changes to `stopped` before its response write and never returns to `active`.

The response schema contains only `version`,
`outcome`, and the following typed objective fields: `state`, `shim_pid`, `child_pid`, `bytes_written`,
`submit_observed`, `cause`, `cleanup`, `record_path`, `local_root`, `recorded_root`, `recorded_token`, `observed_token`,
`caller_pid`, `target_pid`, `final_icanon`, `final_echo`, `signal_attempted`, `signal`, and
`child_exit_observed`. `signal` is the closed literal `SIGHUP`; it is not caller text. Each outcome's strict decoder admits only its defined
subset; an irrelevant otherwise-known field is rejected. Neither schema has an extension map or arbitrary
input/payload field.

Each frame read or write sets one absolute two-second deadline covering both header and payload. A partial transfer at
the deadline is reported with its observed byte count; a zero-byte timeout is not EOF. No phase inherits unused time
from the prior phase, and no retry extends a deadline.

Parsing order is normative in both directions. After framing and JSON lexical validity, a token pre-pass examines only
the top-level `version` member. A missing version, a duplicate version member, a non-integer version, or an integer
other than `ShimProtocolVersion` returns `protocol-skew` before unknown fields, schema fields, operation lookup, state
lookup, or side effects. The rendered observed version is respectively `absent`, `duplicate`, the raw JSON token, or
the decimal integer. Only an exact integer `1` permits a second strict pass, which rejects duplicate or unknown fields,
missing required fields, an unknown operation, or the wrong JSON type. Thus a foreign-version frame with otherwise
unknown fields always reports skew; it is never partially interpreted. The server hello lets a current client reject
a foreign shim before sending a request, and the request's version lets a current shim reject a foreign client before
reading its session, role, or operation. A client-side skew diagnostic therefore names the `connected shim hello`
version and may use the client's already validated operation/session/role. A shim-side skew diagnostic names the
`client request` version and cannot name operation/session/role because those fields were not interpreted. There is no
negotiation, downgrade, migration dialect, newline framing, or best-effort decode.

After the current client accepts the hello, Darwin `LOCAL_PEERPID` supplies the kernel-observed answerer PID. It must
equal the advisory shim PID; disagreement is `answerer-disagreement`, not a kernel-vs-kernel claim. Same-user
unlink/rebind stays outside the threat model, while status reports the observable disagreement.

For control, take one typed `ps -eo pid=,ppid=` snapshot through the Runner. Starting at the caller's PID, walk parent
links looking for the `LOCAL_PEERPID` shim. A complete walk finding it is `observed-self-target`. Missing links,
malformed rows, duplicate/looping ancestry, or process-inspection failure are `ancestry-undetermined`. Both refuse,
separately. `TMUX_PANE`, `AGENTCTL_SESSION`, `AGENTCTL_ROLE`, and `AGENTCTL_MANAGED` are never ancestry inputs.

| Current `internal/target` check | 0.5.0 disposition |
|---|---|
| role charset | retained in `internal/config`, with 32-byte cap |
| session managed/version tmux options | retired; runtime claim plus version-first protocol replace them |
| exact role-window resolution and stored window role | retired; validated role namespace plus `flock` replace them |
| exactly one live pane | moot for identity/delivery; tmux layout is optional presentation |
| pane-root baseline equality | replaced by child PID, `kill(pid,0)`, and raw start-token observation |
| `$TMUX_PANE` self-target guard | replaced by fail-closed snapshot ancestry from `LOCAL_PEERPID` |
| tmux `DeliverPayload` | retired; operation name crosses the socket and shim writes registry bytes to its PTY |

### 15.6 Status vocabulary, confidence, precedence, and interim diagnostic

Status enumerates runtime claims first and durable records second, with or without tmux. A volatile lockfile anchors
recorded state root and owner observations. Durable records enumerated without an anchor carry
`"confidence":"unanchored"` on every row and derived absence; they never receive anchored confidence. Anchored rows
carry `"confidence":"anchored"`. Tmux presentation is `present`, `gone`, or `unavailable` and never changes runtime
identity.

Held-claim observation is non-acquiring only: status opens the existing lockfile and issues `F_GETLK` for a write
lock. Darwin reports a conflicting BSD `flock` as `F_WRLCK` with `pid=-1`; `F_UNLCK` means no observed holder.
Status never attempts `LOCK_EX|LOCK_NB`, because owner death between observation steps must not let a diagnostic seize
the role. A held claim with a temporarily absent socket is `starting`, while a socket with no held claim is
`answerer-disagreement`.

Fleet-record harness/model/effort and directory values are operator-claim provenance. The shim response, role record,
answerer, process observation, and readiness are shim-runtime provenance. Status joins those sources without replacing
one with the other: a fleet value never becomes runtime evidence, and a live shim observation never silently rewrites
the fleet claim. The anchored/unanchored vocabulary continues to describe the runtime/durable identity join, not the
trustworthiness of an operator-selected fleet value.

Within one role, first match wins:

| Order | State | Observation |
|---|---|---|
| 1 | `invalid-record` | required volatile/durable data malformed |
| 2 | `state-root-disagreement` | local root differs from lockfile-recorded root |
| 3 | `protocol-skew` | answerer version absent/different, before other response parsing |
| 4 | `answerer-disagreement` | advisory shim PID differs from `LOCAL_PEERPID`, or socket/claim topology conflicts |
| 5 | `cleanup-failed` | prior owned cleanup durably recorded incomplete |
| 6 | `concurrent-contender` | an owner explicitly reports a competing owner decision; status never invents this from a losing launch, which leaves no durable status fact |
| 7 | `starting` | live claimed shim plus `child-starting`, or child recorded but not ready |
| 7a | `stopping` | live claimed shim has begun serialized stop and has not yet reported its stop response |
| 7b | `stopped` | live claimed shim observed child exit and owned teardown is pending |
| 8 | `indeterminate-child-starting` | dead shim plus `child-starting` |
| 9 | `running` | live shim, matching child/answerer, ready protocol |
| 10 | `orphan` | dead shim and child `present-match` |
| 11 | `present-token-disagreement` | PID present but raw token differs |
| 12 | `present-not-ours` | `kill(pid,0)` returned `EPERM` |
| 13 | `could-not-observe` | other presence error or token observation failed after nil |
| 14 | `stale-record` | durable child record and observed `ESRCH` |
| 15 | `missing` | no role record at the applicable confidence |

`tmux-presentation-gone` renders as presentation `gone` beside runtime state, never role absence. Only
`stale-record`/`missing` backed by ESRCH or actual record absence can enter relaunch; disagreements and uncertainty
refuse.

Disagreement output renders both observed sides: advisory and connected answerer PIDs, advisory and durable-record
shim PIDs, or local and recorded roots as applicable. A same-user concurrent loser contributes no separate contender
row; already-observed answerer or state-root disagreement remains the factual family. Provenance: planner R19,
2026-08-10/11.

The runtime collector's optional presentation observer preserves one objective report field/table line from the
pre-cutover incident diagnostic. Trigger only when every
roster role is `missing`, exactly one window has empty `@agentctl_role`, and that window has exactly N panes where N is
roster size. Below, above, multiple role-less windows, or any window whose actual name matches a roster role suppress
it. Stale `@agentctl_role` metadata on a differently named window is presentation metadata and does not establish such
a match. Exact human text:

```text
note: all N roster roles are missing; unmanaged window "W" has N panes
```

The human table emits this exact note immediately after the corresponding session's last row, before any later
session's rows. JSON carries the same optional `"note"` on that session object. This names no cause: a role-less
roster-sized window is not proof that panes were joined or that it contains the missing harnesses.

The shipped path uses `RuntimeShimRoleSource`, `ShimCollector`, `RuntimePresentationSource`, `WriteShimTable`, and
`WriteShimJSON`. The runtime source opens the volatile role side before the durable side; its no-lockfile fallback is
unanchored by construction. `ShimStatusFleetReader` adapts the durable fleet roster without converting its
operator-selected fields into runtime evidence. The legacy tmux identity collector and renderer are removed.

### 15.7 Command outcomes and attach limitation

`launch` persists roster/config before absence can be reported, starts shims in roster order, and waits for readiness.
Tmux launch uses the single §12.1 shell site to start the hidden shim; foreground `run` invokes typed argv directly.
`relaunch` requires §15.4 absence permission and never starts beside orphan, indeterminate, disagreement, or
could-not-observe. An explicit relaunch `--dir` is resolved to an absolute path and validated before presentation or
runtime mutation; the stored fleet directory is already absolute by §15.2. `kill` signals child/process group first,
observes §15.4, then lets shim release its claim; partial cleanup remains reported.

Foreground `run` creates or extends the durable fleet according to §15.2, starts the same resident server and harness
argv as tmux launch, and connects the caller's terminal streams directly to the nested PTY. It creates no shell,
viewer, tmux server, session, window, or pane. The command remains in the foreground through child exit and forwards
terminal signal/resize behavior through the shared PTY lifecycle. Once the nested terminal reaches ready, the outer
terminal copies its observed mode with only `ISIG` cleared so control characters reach the relay as bytes; the nested
PTY retains its own `ISIG` policy and delivers the resulting signal to the harness process group. Its exact syntax is
§15.1; the cwd is observed before runtime mutation and is not caller-overridable.

For an existing fleet whose stored directory differs from that cwd, the exact refusal is:

```text
agentctl: refusing to run role "ROLE" in session "SESSION"; durable fleet directory "STORED" differs from current working directory "CURRENT"; no role was started or durable record mutated (fleet-directory-disagreement)
```

`kill` observes optional presentation by exact session name before role mutation and retains that observation's typed
session ID. After each `stop-child-exited`, it polls every 25ms through the inclusive 5s boundary until the role
inspector reports `missing`; transient `stopped`/`stale-record` artifacts are not fleet-cleanup permission. Any other
state, observation failure, cancellation, or timeout retains the fleet record. Only after every required child
exit/absence and role-artifact cleanup fact does it attempt `kill-session -t SID` exactly once. Tmux
normally removes the last non-`remain-on-exit` shim window and its session as the shim exits, so that exact-ID removal
can race a presentation that has already disappeared. The closed post-removal table is:

| Initial presentation | Exact-ID removal | One post-failure exact-name observation | Result facts | Fleet record |
|---|---|---|---|---|
| gone | not attempted | not attempted | `PresentationRemoved=false`, `PresentationGone=true` | remove after child cleanup |
| present | success | not attempted | `PresentationRemoved=true`, `PresentationGone=false` | remove after child cleanup |
| present | error | gone | `PresentationRemoved=false`, `PresentationGone=true` | remove after child cleanup |
| present | error | present (same or different typed ID) | both facts `false` | retain and return typed error |
| present | error | unavailable/error | both facts `false` | retain and return typed error |

The post-failure observation is attempted exactly once; there is no second removal. The two result booleans are never
both true. `PresentationRemoved` means the exact typed removal command succeeded; a presentation observed already gone
is never called removed. The typed retained errors have exactly these internal literals, with `%q` substitutions:

```text
shim kill retained fleet record for session %q: exact-ID presentation removal of %q failed: %q; post-removal presentation %q remained present
shim kill retained fleet record for session %q: exact-ID presentation removal of %q failed: %q; post-removal presentation observation failed: %q
```

Optional presentation lookup treats only tmux 3.7b's exact single-line `no server running on PATH` and
`error connecting to PATH (No such file or directory)` diagnostics as presentation `gone`; any prefix, suffix,
additional line, different exit diagnostic, or runner failure remains an error. This classification never implies
runtime-role or fleet absence.

The only delivery instruction at the control boundary is the operation name after version, answerer, ancestry, child
identity, and readiness pass. The request's other permitted values are the protocol version and validated
session/role identity pinned in §15.5; the response contains only its protocol version and typed objective facts. The
shim reports only accepted request, bytes written, submit observed, cancellation residue, child exit, and cleanup
facts. It never reports harness execution from a PTY write. `attach` first proves that the selected session has a
durable fleet configuration, then requires an exactly observed tmux presentation by name and attaches only its typed
session ID. It never treats a same-named presentation as fleet identity. Without a presentation:

The exact literal is §15.8's `attach-no-presentation` row, which is one of the three multi-line templates: the refusal
line followed by one `  agentctl attach --session SESSION ROLE` line per roster role. It is not paraphrased here.

### 15.8 Shim-plane exit map

Every diagnostic is one line on stderr with the `agentctl: ` prefix and trailing newline. In the templates below,
uppercase words are typed substitutions, not discretionary prose. `SESSION`, `ROLE`, every path/root/executable/flag,
`CAUSE`, `CLEANUP_CAUSE`, `RULE`, `FIELD`, `OPERATION`, and `OUTCOME` use Go `%q`. PIDs, byte counts, status/version
integers, and roster counts are unsigned decimal. `OP`, `ROOT_KIND`, `STATE`, `OBSERVATION`, `SIGNAL`, `TYPE`,
`EXPECTED_TYPE`, `REMAINING`, `PHASE`, and boolean literals are closed canonical tokens rendered without quotes.
`SIGNAL` renders as the canonical name from `golang.org/x/sys/unix.SignalName` — `SIGINT`, `SIGTERM`, `SIGHUP`,
`SIGQUIT`, `SIGKILL`, and so on — never a number, never a lowercase description like `interrupt` (which is what
`Signal.String()` returns, and is why that function is not used), and never a `signal 2` form. The mapping source is
pinned rather than described because Darwin aliases numbers: 6 is both `SIGABRT` and `SIGIOT`, and "the platform's
uppercase name" would let two conforming implementations print different exact rows. `SignalName` resolves 6 to
`SIGABRT`, and returns the empty string for a number it does not map; **only** that empty result renders as
`SIGNAL_NUMBER_N` with the decimal number, so an unnameable value is still exact and still obviously a signal; that case exists because
`run-child-signaled` reports whatever a child was killed by, which is not confined to any subset agentctl chose. A raw start
token renders as `{sec:SEC,usec:USEC}`. The `%q` rule governs diagnostic substitutions only. In the second line of
`launch-complete-detached`, `launch-complete-tmux`, and in every line of `attach-no-presentation`, `SESSION` and `ROLE`
render as the bare validated identifier so the printed text is a command the operator can copy. A command must select
one typed row and its literal template. It may not
paraphrase, append a generic category, or borrow another row's exit code. The attach surface of §15.11 overrides four clauses of this
paragraph, in its own words and nowhere else. §15.11.9's composition rule emits more than one line for a superseded
outcome. §15.11.7 permits a byte-exact prefix of a selected template in three cases: when the receiving terminal is saturated,
when a caught signal terminates the process while a redirected fd-2 write is in progress, and when that write fails
after partial progress — three distinct causes of one permitted shape, none distinguishable from the sink alone. And
§15.11.7 never writes descriptor 2 itself: it writes a proved-identical private descriptor when, and only when, it has
proved by `fstat` that fd 2 names the same terminal, and otherwise fd 2's destination through a duplicate floored above
2 — the destination is `stderr`'s in both cases, the descriptor is not, and that is stated here rather than left as an
implied equivalence. No other section overrides any of the four. Three rows are explicitly multi-line and
their additional lines are part of the selected template rather than an append: `launch-complete-detached` and
`launch-complete-tmux` each carry exactly one hint line, and `attach-no-presentation` carries one line per roster role.
No other row may emit more than one line. `status` table and JSON documents are the
sole successful status output and therefore add no diagnostic line.

| Typed outcome | Exit | Exact factual message template |
|---|---:|---|
| `launch-complete-detached` | 0 | line 1 `agentctl: launched session SESSION detached; N roles are ready`; line 2 `agentctl: attach a role with: agentctl attach --session SESSION ROLE` |
| `launch-complete-tmux` | 0 | line 1 `agentctl: launched session SESSION; N roles are ready`; line 2 `agentctl: attach the fleet with: agentctl attach --session SESSION` |
| `relaunch-complete` | 0 | `agentctl: relaunched role ROLE in session SESSION; the shim is ready` |
| `kill-complete` | 0 | `agentctl: killed session SESSION; every recorded child was observed absent` |
| `run-child-exited` | 0 | `agentctl: foreground role ROLE in session SESSION exited with status 0` |
| `delivery-submitted` | 0 | `agentctl: OP for role ROLE in session SESSION wrote BYTES bytes and observed submit` |
| `stop-child-exited` | 0 | `agentctl: stop for role ROLE in session SESSION attempted SIGHUP and observed child PID CHILD exit; no PTY input was written` |
| `unclassified` | 1 | `agentctl: OP failed for session SESSION: CAUSE (unclassified)` |
| `run-child-failed` | 1 | `agentctl: foreground role ROLE in session SESSION exited with status STATUS (child-exit)` |
| `run-child-signaled` | 1 | `agentctl: foreground role ROLE in session SESSION terminated by signal SIGNAL (child-signal)` |
| `invalid-session` | 2 | `agentctl: invalid session SESSION: RULE; no role was mutated` |
| `invalid-role` | 2 | `agentctl: invalid role ROLE: RULE; no role was mutated` |
| `invalid-root` | 2 | `agentctl: invalid ROOT_KIND ROOT: RULE; no role was mutated` |
| `invalid-flag` | 2 | `agentctl: invalid flag FLAG: RULE; no role was mutated` |
| `invalid-request` | 2 | `agentctl: invalid shim request for session SESSION role ROLE: RULE; no role was mutated` |
| `session-missing` | 3 | `agentctl: session SESSION was not found` |
| `fleet-config-missing` | 3 | `agentctl: session SESSION has no durable fleet configuration` |
| `fleet-config-exists` | 3 | `agentctl: refusing to launch session SESSION; durable fleet configuration already exists (fleet-config-exists)` |
| `protocol-skew-shim-absent` | 3 | `agentctl: refusing to OP role ROLE in session SESSION; connected shim hello protocol version was absent; expected 1 (protocol-skew)` |
| `protocol-skew-shim-observed` | 3 | `agentctl: refusing to OP role ROLE in session SESSION; connected shim hello protocol version was OBSERVED; expected 1 (protocol-skew)` |
| `protocol-skew-client-absent` | 3 | `agentctl: refusing client request; client request protocol version was absent; expected 1 (protocol-skew)` |
| `protocol-skew-client-observed` | 3 | `agentctl: refusing client request; client request protocol version was OBSERVED; expected 1 (protocol-skew)` |
| `attach-no-presentation` | 3 | `agentctl: refusing to attach session SESSION; no tmux presentation was observed; attach a role directly:` then, in roster order, one line `  agentctl attach --session SESSION ROLE` per role |
| `role-outside-roster` | 4 | `agentctl: role ROLE is not in the durable roster for session SESSION` |
| `role-missing-when-required` | 4 | `agentctl: role ROLE in session SESSION has no live claim or durable role record (missing)` |
| `role-stale-when-required` | 4 | `agentctl: role ROLE in session SESSION has stale child PID CHILD after kill(CHILD, 0) returned ESRCH (stale-record)` |
| `observed-self-target` | 5 | `agentctl: refusing to OP role ROLE in session SESSION; target shim PID SHIM is an ancestor of caller PID CALLER (observed-self-target)` |
| `invalid-record` | 5 | `agentctl: refusing to OP role ROLE in session SESSION; durable record RECORD_PATH is invalid: CAUSE (invalid-record)` |
| `orphan` | 5 | `agentctl: refusing to OP role ROLE in session SESSION; shim PID SHIM was absent and recorded child PID CHILD was present with a matching start token (orphan)` |
| `indeterminate-child-starting` | 5 | `agentctl: refusing to OP role ROLE in session SESSION; shim PID SHIM was absent and the durable record is child-starting; independently prove child absence, then remove RECORD_PATH (indeterminate-child-starting)` |
| `starting` | 5 | `agentctl: refusing to OP role ROLE in session SESSION; shim PID SHIM holds the claim and the durable record is STATE (starting)` |
| `concurrent-contender` | 5 | `agentctl: refusing to OP role ROLE in session SESSION; flock returned EWOULDBLOCK while lockfile shim PID SHIM holds the role claim (concurrent-contender)` |
| `cleanup-failed` | 5 | `agentctl: refusing to OP role ROLE in session SESSION; durable cleanup is incomplete after CAUSE and child observation is OBSERVATION (cleanup-failed)` |
| `answerer-disagreement-pid` | 5 | `agentctl: refusing to OP role ROLE in session SESSION; lockfile shim PID RECORDED differs from connected LOCAL_PEERPID ANSWERER (answerer-disagreement)` |
| `answerer-disagreement-claim` | 5 | `agentctl: refusing to OP role ROLE in session SESSION; LOCAL_PEERPID ANSWERER answered without the matching held role claim (answerer-disagreement)` |
| `state-root-disagreement` | 5 | `agentctl: refusing to OP role ROLE in session SESSION; resolved state root LOCAL_ROOT differs from lockfile-recorded state root RECORDED_ROOT (state-root-disagreement)` |
| `fleet-directory-disagreement` | 5 | `agentctl: refusing to run role ROLE in session SESSION; durable fleet directory STORED differs from current working directory CURRENT; no role was started or durable record mutated (fleet-directory-disagreement)` |
| `present-token-disagreement` | 5 | `agentctl: refusing to OP role ROLE in session SESSION; child PID CHILD start token OBSERVED_TOKEN differs from recorded token RECORDED_TOKEN (present-token-disagreement)` |
| `present-not-ours` | 5 | `agentctl: refusing to OP role ROLE in session SESSION; kill(CHILD, 0) returned EPERM (present-not-ours)` |
| `shim-stopping` | 5 | `agentctl: refusing to OP role ROLE in session SESSION; shim PID SHIM state is STATE for child PID CHILD; no PTY input was written (shim-stopping)` |
| `stop-already-stopping` | 5 | `agentctl: stop for role ROLE in session SESSION found shim PID SHIM state STATE for child PID CHILD; no second signal was attempted and no PTY input was written (stop-already-stopping)` |
| `delivery-cancelled-clean` | 5 | `agentctl: OP for role ROLE in session SESSION was cancelled before any payload byte was written (delivery-cancelled)` |
| `delivery-cancelled-with-residue` | 5 | `agentctl: OP for role ROLE in session SESSION was cancelled after BYTES payload bytes were written but before submit; terminal input may contain residue (delivery-cancelled-with-residue)` |
| `ancestry-undetermined` | 6 | `agentctl: refusing to OP role ROLE in session SESSION; could not determine whether caller PID CALLER descends from target shim PID SHIM: CAUSE (ancestry-undetermined)` |
| `presence-observation-failed` | 6 | `agentctl: could not observe child PID CHILD for role ROLE in session SESSION: kill(CHILD, 0) returned CAUSE (could-not-observe)` |
| `token-observation-failed` | 6 | `agentctl: could not observe the start token for child PID CHILD in session SESSION role ROLE: CAUSE (could-not-observe)` |
| `protocol-read-from-shim-invalid` | 6 | `agentctl: could not read protocol frame from connected shim for role ROLE in session SESSION: CAUSE (protocol-frame-read-invalid)` |
| `protocol-read-from-client-invalid` | 6 | `agentctl: could not read protocol frame from connected client: CAUSE (protocol-frame-read-invalid)` |
| `protocol-write-to-shim-failed` | 6 | `agentctl: could not write protocol request to connected shim for role ROLE in session SESSION: CAUSE (protocol-frame-write-failed)` |
| `protocol-write-to-client-failed` | 6 | `agentctl: could not write protocol frame to connected client: CAUSE (protocol-frame-write-failed)` |
| `protocol-schema-invalid` | 6 | `agentctl: could not interpret version-1 shim protocol for role ROLE in session SESSION: CAUSE (protocol-schema-invalid)` |
| `required-executable-missing` | 7 | `agentctl: required executable EXECUTABLE was not found; no role was mutated` |
| `readiness-timeout-cleaned` | 8 | `agentctl: role ROLE in session SESSION was not ready after 5s; final tty flags were ICANON=ICANON_BOOL ECHO=ECHO_BOOL; cleanup observed child absence and removed every artifact owned by this invocation (readiness-timeout)` |
| `readiness-observation-failed-cleaned` | 8 | `agentctl: could not observe harness tty readiness for role ROLE in session SESSION: CAUSE; cleanup observed child absence and removed every artifact owned by this invocation (readiness-observation-failed)` |
| `child-exited-before-ready` | 8 | `agentctl: child PID CHILD exited before harness tty readiness for role ROLE in session SESSION; cleanup observed absence and removed every artifact owned by this invocation (child-exited-before-ready)` |
| `owned-rollback-complete` | 8 | `agentctl: OP failed for role ROLE in session SESSION: CAUSE; cleanup observed child absence and removed every artifact owned by this invocation (owned-rollback-complete)` |
| `owned-rollback-incomplete` | 8 | `agentctl: OP failed for role ROLE in session SESSION: CAUSE; cleanup left REMAINING: CLEANUP_CAUSE (owned-rollback-incomplete)` |
| `readiness-timeout-retained` | 9 | `agentctl: role ROLE in session SESSION was not ready after 5s; final tty flags were ICANON=ICANON_BOOL ECHO=ECHO_BOOL; child PID CHILD was not observed absent, so ownership and the durable record were retained (readiness-timeout)` |
| `ownership-retained` | 9 | `agentctl: role ROLE in session SESSION failed after child PID CHILD started: CAUSE; cleanup observation was OBSERVATION, so ownership and the durable record were retained (ownership-retained)` |
| `stop-child-retained` | 9 | `agentctl: stop for role ROLE in session SESSION attempted SIGHUP but did not observe child PID CHILD exit; child observation was OBSERVATION; ownership and the durable record were retained (stop-child-retained)` |
| `post-exit-cleanup-retained` | 9 | `agentctl: stop for role ROLE in session SESSION observed child PID CHILD exit, but role cleanup was not observed complete; last outcome was OUTCOME: CAUSE; presentation and fleet record were retained (post-exit-cleanup-retained)` |
| `record-commit-uncertain` | 9 | `agentctl: role ROLE in session SESSION has an uncertain durable PHASE record commit: CAUSE; the record was retained and the role was not reported absent (record-commit-uncertain)` |

`invalid-root` is selected when declared-input syntax, fixed durable-path traversal/creation, or a volatile/final-state
private-boundary failure is represented by `InvalidRootError`. Descriptor observation and substitution errors select
`unclassified`; setup renders that row with `SESSION` exactly `""`. A durable ancestor mode selects neither row, and
there is no alternate durable-ancestor-mode refusal literal.

`protocol-skew-shim-observed` and `protocol-skew-client-observed` substitute `OBSERVED` with `duplicate`, the `%q`
raw JSON token for a non-integer, or the decimal foreign integer according to §15.5. `RECORD_PATH` is always the
lockfile body's recorded root joined with the fixed durable template, never a path recomputed from the reader's
environment. `REMAINING` is a comma-separated list in the fixed order `child, socket, attach, record, lock`; omitted artifacts
are not named. `OBSERVATION` is exactly one of `present-match`, `present-token-disagreement`, `present-not-ours`, or
`could-not-observe`; it is never `missing` unless `kill(pid,0)` returned `ESRCH`, in which case the complete-cleanup
row applies. Observed self-target and ancestry-undetermined deliberately remain different facts, codes, and literals.
`PHASE` is exactly `child-starting`, `child-recorded`, or `fleet-config`. For `shim-stopping` and
`stop-already-stopping`, `STATE` is exactly `stopping` or `stopped`; the former outcome applies only to payload
operations `clear` and `compact`. `stop-already-stopping` carries `signal_attempted=false` and omits `signal` and
`child_exit_observed` because the second request performs neither action. `stop-child-exited` and `stop-child-retained` always carry
`signal_attempted=true`, `signal=SIGHUP`, and respectively `child_exit_observed=true` or `false`; those fields remain
separate even when the selected outcome makes both facts readable.

For `protocol-read-from-shim-invalid` and `protocol-read-from-client-invalid`, `CAUSE` is exactly one of
`zero payload length`, `payload length N exceeds 4096`, `EOF after N of 4 header bytes`, `EOF after N of LENGTH payload
bytes`, `frame read exceeded 2s during header after N of 4 bytes`, `frame read exceeded 2s during payload after N of
LENGTH bytes`, `frame read failed during header after N of 4 bytes: ERROR`, `frame read failed during payload after N
of LENGTH bytes: ERROR`, `payload is not valid UTF-8`, `payload has trailing bytes after its JSON value`, `payload top
level is not an object`, or the `%q` JSON syntax error. For `protocol-write-to-shim-failed` and
`protocol-write-to-client-failed`, `CAUSE` is exactly `frame write exceeded 2s after N of TOTAL bytes` or
`frame write failed after N of TOTAL bytes: ERROR`, where `TOTAL` is the four-byte header plus payload length.
For `protocol-schema-invalid`, `CAUSE` is exactly `duplicate field FIELD`, `unknown field FIELD`,
`missing required field FIELD`, `field FIELD has JSON type TYPE; expected EXPECTED_TYPE`, `operation OPERATION is not
registered`, or `response field FIELD is not valid for outcome OUTCOME`.
For a post-start readiness failure, the final command always uses an exit-8 cleanup row or an exit-9 retained row,
never exit 6. Its exit-8 `CAUSE` is exactly one of `harness tty was not ready after 5s; final flags
ICANON=ICANON_BOOL ECHO=ECHO_BOOL`, `TIOCGETA failed while observing harness tty readiness: ERROR`,
`TIOCGETA remained interrupted through the 5s readiness deadline`, or `child PID CHILD exited before harness tty
readiness`. This makes cleanup outcome, rather than the initiating observation error, the final exit claim.

### 15.9 External calls and dependency boundary

Production invokes no shell beyond the one tmux window-command site. Shipped boundaries are optional
tmux presentation/create/attach/kill argv through `internal/tmuxx.Runner`; one ancestry snapshot
`ps -eo pid=,ppid=` through that Runner's `tmuxx.ParentPIDs` wrapper; PTY child start through a narrow interface receiving only harness/AMQ argv;
and Darwin syscalls for `flock`, `LOCAL_PEERPID`, `kill(pid,0)`, and raw `kinfo_proc` tokens. Detached launch adds one
typed parent-specified `os/exec` spawn of the hidden shim command (§15.11.6), with an asynchronous `Wait` owned by the
launcher; it invokes no shell and composes no shell string.

`golang.org/x/sys/unix` is restricted to the Darwin lock/socket/process syscalls above, plus the
signal-disposition and signal-mask observations of §15.11.7 and the
`SignalName` canonical mapping of §15.8 — added to this list explicitly rather than admitted
silently, because that list is the whole of the sanction and a new use that is not in it is a new dependency
decision. The staged admission is
complete: source tests, pinned govulncheck, Dependabot, and archive-license checks cover v0.47.0, and the production
binary imports the shim path. `go version -m` on the built Darwin binary must record
`golang.org/x/sys v0.47.0`; release archives must include its upstream license. The PTY implementation remains
standard-library-only.

### 15.10 Evidence, tests, and numbered security trace

The [SIGHUP evidence](../../security/2026-08-10-issue-182-shim-probe-evidence.md) records Claude Code 2.1.226 and
codex-cli 0.147.0 as direct children of owned nested-PTY fixtures; each direct child's observed `comm` exactly matched
the selected harness executable path, and both children terminated after SIGHUP targeted only their fixture parent.
An automated PTY-bearing intermediate-child near miss refuses. Orphan handling remains mandatory. The complete
[incident replay](../../security/2026-08-10-issue-182-replay-evidence.md), SHA-256
`d9c14f10df03ec7e7de36adcdd9225b26946c64b9d7f26ec50777b41182f7a01`, was verified byte-for-byte against build2's
full report. Fake tests own exact syscall/argv ordering and failures; live tests own PTY/kernel/socket/layout/harness
behavior and use throwaway resources only.

This section traces the implementation plan's ten-row security matrix: (1) §15.3 claim; (2) §15.5 answerer
disagreement; (3) §15.2 roots/caps; (4) §§15.1/15.7 operation names; (5) §15.5 enforcement/retirement; (6)
§§15.3–15.6 orphan/absence/manual remedy; (7) §§15.3/15.7 readiness; (8) §§15.3/15.8 ownership/exits; (9) §15.7
factual delivery; (10) §§15.5/15.8 version-first refusal. Every implementation PR cites applicable rows; none may
reopen semantics without an approved design delta.

Structural drift guards pin the exact four-field `Request`, empty payloads on every `OperationControl` registry entry,
zero production `internal/target` imports, zero `tmuxx.DeliverPayload` calls, zero production `send-keys`, and the
single `shimWindowCommand` shell-composition site. Review evidence mutation-adds a fifth `Payload` field and a
non-empty `observe` payload separately; each mutant fails its named invariant before source restoration.

Gate S runs after PRs 2 and 3 and before PR 4. It records `PASS` only if the merged Darwin evidence satisfies the
approved `flock`, `LOCAL_PEERPID`, raw `kinfo_proc` token, state-root disagreement, fully resolved socket-length,
nested controlling-PTY, exact §15.3 readiness, and durable pre-fork reservation contracts without a new semantic or
dependency surface. Any one unmet contract records `FAIL`, stops PR 4, and requires the planner to amend issue #182
before selecting the named pane-scoped Option A in the options paper §3. No implementation may weaken the predicate,
continue Option S after `FAIL`, or improvise a third design.

Release verification owns the deterministic second-binary fixture at `hack/fixtures/shim-version/main.go`. It builds
that source separately from the current binary and records both artifact versions and SHA-256 hashes. The mandatory
matrix names each direction separately: current client reads a foreign-version shim hello; foreign-version client
sends a request to the current shim; current client reads an absent-version shim hello; absent-version client sends a
request to the current shim; and current client/current shim matching controls exercise both hello and request. Every
foreign or absent leg must fail at the version pre-pass before schema/operation interpretation and record whether the
observed value came from the `connected shim hello` or `client request`. Each matching control must pass framing and
proceed to its next typed gate.

### 15.11 Detached launch and the per-role attach stream

Provenance: this contract answers issue #225. Its first draft was written against
a stale base and was rebuilt on `6655ed9`; §16 is retained unchanged. The constants below are of
**two kinds, and the difference is stated because a policy rationale is not an
empirical derivation**:

- **Measured.** `AttachLagBufferBytes` = `131072` is derived from recorded
  artifacts named at its point of use — a 61,489-byte initial paint and a
  45,777-byte resize repaint, both from codex-cli 0.147.0 at 200x60, with their
  hashes.
- **Policy.** `AttachTailFlushTimeout` = `10s` and `AttachReportTimeout` = `2s`
  are chosen bounds. Each carries a rationale at its point of use, and each says
  what it is trading, but neither is derived from a measurement and neither
  claims to be. `AttachClientQueueBytes` = `131072` is a policy mirror of the
  measured lag buffer: it serves the same instantaneous-lag purpose on the client
  side, which is why it takes the same value, but no separate measurement
  supports it.

An earlier version of this sentence claimed every constant was measured and not
chosen. That was false for three of the four, and a provenance claim that
overstates its own basis is worse than one that admits which values are
judgement.

The reopening of the options paper's Option-C rejection is exactly and only
per-role attach for detached roles. There is no virtual screen model, no fleet
daemon, no viewer application, and **no fanout**: one PTY has one drain path in
each mode.

#### 15.11.1 Presentation selection and the drain path it chooses

`--detached` and `--tmux` are mutually exclusive, and `--detached` is the
explicit detached choice. **Passing neither means detached.**
The template's optional top-level `"presentation": "detached" | "tmux"` means
the same, absent means detached, any other value is a schema refusal, and an
explicit flag overrides the file. The schema entry, its validation, the flags,
and the documentation land together.

Presentation is not inferable after the fact, so the fleet record persists it.
The version-1 record gains a required `presentation` member with the same two
values; a record without it is `invalid-record`. This is what lets `relaunch`
distinguish a detached role from a tmux role whose presentation vanished — a
distinction the record could not previously express.

The chosen presentation fixes who drains the PTY, and the two are exclusive:

| Presentation | PTY drain path | Direct `attach ROLE` |
|---|---|---|
| tmux | the pane relay, for the role's lifetime | **refused** — the pane is the viewer |
| detached | the shim drains continuously, discarding with no viewer and buffering to one when attached | admitted, one viewer |

A tmux pane is already a persistent reader and writer of that PTY. Admitting a
second reader would split output rather than duplicate it, and duplicating it
would require the screen model this design refuses. `attach ROLE` against a
tmux-presented role therefore refuses and names the operator's actual route.

Preflight requires `tmux` only when the resolved presentation is `tmux`; `amq`
and the selected harnesses are required on both paths, and `run` never requires
tmux.

#### 15.11.2 One connection, framed end to end

A viewer and its role share **one** connection. Every byte on it is inside a
frame, in both directions and for the whole of its life: there is no handshake
phase that hands off to a raw stream, and no second connection.

That is a deliberate reversal of an earlier two-connection design. What one
connection buys is precise, and smaller than an earlier draft of this paragraph
claimed: a stream orders bytes **within each direction**, so client→shim input
and resize frames are ordered with respect to each other, and shim→client output
and final frames are ordered with respect to each other. It does **not** give a
total order across the two directions — the shim can send `attach-final` before
it has read a resize the client already wrote — and it does not by itself stop
work decoded from a departed viewer from being applied afterwards. Those remain
properties the shim must enforce, stated as observable facts in §15.11.3, not
consequences of the transport.

What the single connection does remove is the machinery that existed only to
relate two streams to each other: the claim token, the data handshake, the viewer
epoch, and the resize sequence number are gone, because admission **is** the
connection and each direction is already ordered.

Each frame is a four-byte unsigned big-endian payload length, then a one-byte
`kind`, then exactly that many payload bytes. `kind` is `0` for a control frame,
whose payload is UTF-8 JSON; `1` for viewer input; `2` for role output. Data
frames carry raw bytes and are never re-encoded, so a terminal stream costs five
bytes of framing per chunk and nothing else. Payload length is at most 65536 for
data frames and 4096 for control frames; zero length, a value above the
applicable cap, EOF after any header or payload byte, an unknown `kind`, invalid
UTF-8 in a control payload, trailing bytes inside a single JSON value, and a
non-object top level are all `protocol-frame-read-invalid`.

`ShimProtocolIOTimeout` bounds the completion of a frame once its first byte has
arrived, and nothing else. The wait **between** frames is unbounded: a quiet
attachment must never time out, and zero resizes is a valid attachment.

The control frames are exactly:

```json
← {"version":1,"kind":"attach-shim-hello"}
→ {"version":1,"kind":"attach-hello","session":"SESSION","role":"ROLE","rows":ROWS,"cols":COLS}
← {"version":1,"kind":"attach-admitted"}
← {"version":1,"kind":"attach-refused","outcome":"viewer-present","viewer_pid":PID}
← {"version":1,"kind":"attach-refused","outcome":"peer-unverified","peer_pid":PID,"peer_uid":UID,"shim_uid":UID}
← {"version":1,"kind":"attach-refused","outcome":"peer-unobservable","cause":"CAUSE"}
← {"version":1,"kind":"attach-refused","outcome":"initial-size-failed","rows":ROWS,"cols":COLS,"cause":"CAUSE"}
→ {"version":1,"kind":"attach-resize","rows":ROWS,"cols":COLS}
← {"version":1,"kind":"attach-final","disposition":"DISPOSITION","bytes":BYTES}
← {"version":1,"kind":"attach-final","disposition":"resize-failed","bytes":BYTES,"rows":ROWS,"cols":COLS,"cause":"CAUSE"}
← {"version":1,"kind":"attach-final","disposition":"tail-undelivered","bytes":BYTES,"undelivered":UNDELIVERED}
← {"version":1,"kind":"attach-final","disposition":"tail-unconfirmed","bytes":BYTES,"known_undelivered":UNDELIVERED}
```

Frames are unions on their selector, not one shape with optional members: the
decoder requires exactly the field set its `kind` — and for `attach-refused` its
`outcome`, for `attach-final` its `disposition` — selects, and rejects anything
else. `REFUSAL` is the closed set `viewer-present`, `peer-unverified`,
`peer-unobservable`, `initial-size-failed`. `DISPOSITION` is the closed set
`child-exited`, `viewer-evicted`, `cleanup-retained`, `server-closing`,
`resize-failed`, `tail-undelivered`, `tail-unconfirmed`, `counter-exhausted`.

**`attach-shim-hello` exists so that version skew is observable in both
directions**, exactly as §15.5's control plane already requires. Without it the
attach grammar had no server hello, so the existing skew rows — which name a
`connected shim hello` version — described something the attach path never sent,
and the directional pre-pass had nothing to read. The client reads and
version-checks it before sending anything, and the shim version-checks
`attach-hello` before interpreting its session, role, or size. §15.5's parsing
order applies unchanged: a token pre-pass reads only `version`, and absent,
duplicate, non-integer, or foreign values report skew before any other field is
interpreted.

`protocol-skew` is therefore **not** an `attach-refused` member, and does not
need to be: skew is reported by §15.5's directional rows, which have the observed
version in hand. A refusal frame could not carry it — a v1 frame is decoded only
after its own version pre-pass succeeds — and a conforming v1 client cannot be
told its own hello was skewed. `presented-by-tmux` and `listener-absent` are
likewise absent, being client-side facts determined before connecting, since a
tmux-presented role opens no listener at all. A protocol variant no live endpoint
can emit is a defect, not a completeness.

**A resize needs no sequence number and gets no acknowledgement.** One ordered
stream delivers resizes in the order sent, so a stale resize is not expressible;
the shim applies each and says nothing. A `TIOCSWINSZ` that **fails** is
terminal: the shim sends `attach-final` with disposition `resize-failed`
carrying the rejected size and cause, and releases. That keeps exactly one
terminal frame and one selection rule.

Order is fixed **per direction**. The shim sends `attach-shim-hello` first and
exactly once. The client then sends `attach-hello` first and exactly once, after
which it may send input and resize frames in any interleaving. The shim answers
either `attach-refused` and closes, or `attach-admitted` followed by output
frames, terminated by at most one `attach-final`.

A second hello in either direction, any frame before the applicable hello, or a
resize before admission is a protocol violation, and the response **splits at the
admission boundary**, because a final frame is only reachable after admission:

- **After admission**, the shim makes it a terminal decision with disposition
  `server-closing` and closes, so the client is not left inferring a disposition
  from a bare close.
- **Before admission**, there is no attachment to report a disposition for and no
  grammar path that would carry one, so the shim simply closes. The client
  selects `attach-transport-failed` with `ATTACH_PHASE` of `hello` or
  `admission`. That is not inferring a disposition from an EOF — it is the
  absence of any attachment, named by the phase it failed in.

No pre-admission refusal variant is added for this. Between two conforming
endpoints it is unreachable, the operator has no remedy that differs from the
transport row's, and a member whose only reader is a defective peer is the kind
of vocabulary §15.11 has been removing rather than adding.

`ROWS` and `COLS` are unsigned decimal, at least 1 and at most 65535; any other
value is refused before the PTY is touched. `BYTES`, `UNDELIVERED`, and
`known_undelivered` are unsigned integers in `[0, 2^53-1]`, the range every JSON
decoder represents exactly, and none of them wraps: an implementation that would
exceed the maximum ends the attachment with disposition `counter-exhausted` while
every reported value is still exact. The limit is unreachable in practice and is
stated so no implementation invents a wrap rule. `CAUSE` is the observed error as
a plain string, encoded once by JSON; `%q` is a CLI rendering rule and is never
applied to the wire value.

#### 15.11.3 Admission, single viewer, and release

A connection is admitted only when `LOCAL_PEERPID` resolves and the peer's
effective uid, read from `kinfo_proc` for that PID, equals the shim's. The kernel
accepts a second connection regardless of application state — verified: a second
`net.Dial` to a socket with one accepted connection returns no error — so
single-viewer enforcement happens after accept, never by expecting connect to
fail. A second connection while a viewer is admitted is refused with
`viewer-present` carrying the incumbent's PID.

The initial size from `attach-hello` is applied **before** `attach-admitted`, so
an admitted viewer is one whose terminal size the role already has. A failed
initial `TIOCSWINSZ` refuses with `initial-size-failed` carrying the rejected
size and cause.

These are the observable properties of release, and they hold however an
implementation achieves them:

- **One terminal decision, and at most one final frame.** Many things can end an
  **admitted** attachment — the viewer departing, the lag buffer overflowing, the
  child exiting, cleanup, a failed resize, a protocol violation after admission,
  a transport fault — and they can happen at once. A protocol violation *before*
  admission is not among them: there is no attachment yet, so §15.11.2 has the
  shim close without a final and the client report the transport row for the
  phase. The shim reaches exactly **one** terminal decision
  and attempts exactly one `attach-final` for it. On a healthy writable stream
  the client therefore sees exactly one final frame, one disposition, and one
  close. On a broken one it may see none: delivery is attempted, not promised,
  and a peer that has already aborted, or a stream that failed mid-frame, cannot
  receive one. No ordering among simultaneous causes is promised, because none is
  observable.
- **The disposition is a fact about the attachment, never inferred from a
  close.** A client that does not receive a **complete** final frame selects
  `attach-transport-failed` with its `ATTACH_PHASE`; it never reads a disposition
  out of an EOF or out of a truncated frame.
- **No input and no resize is applied after its connection loses admission.**
  This is enforced by the shim at the point it commits to the PTY, not by the
  transport: bytes and sizes already decoded from a departed viewer are discarded
  there, so work from an old connection can never cross into a replacement. The
  stream orders each direction; it does not know about admission.
- **Input is never delivered while the role is `stopping`** (§15.11.5).
- **A departed viewer's seat is released promptly, and never latched.** Admission
  is revoked, and the seat becomes available to the next connection, on any of:
  stream EOF in either direction; a read or write error on the connection; peer
  death, which surfaces as one of those; or any of the terminal decisions above.
  Release is not deferred until the role next produces output, so a role that has
  gone quiet cannot strand its own seat — the shim does not need the harness to
  say anything in order to notice that its viewer is gone.
- **A half-open peer does not hold the seat.** A viewer that closes only its
  write side produces EOF on the shim's read while remaining able to receive, and
  that EOF revokes admission on its own: the seat is released and the connection
  closed, rather than left readable so the departed viewer keeps receiving output
  beside its replacement. A peer that is neither readable nor draining is bounded
  instead by the lag buffer, which evicts it (§15.11.4), so no combination of
  half-open and non-reading leaves a seat held indefinitely.
- **Ending a viewer is never a role fact.** It changes no runtime state, writes
  no durable record, and is not an input to absence, readiness, or ownership.

Input frames and control deliveries share the role's single ordering, so a
viewer's keystrokes can never interleave with the bytes of a `clear` or
`compact` payload; whichever the shim began first completes first.

#### 15.11.4 The agent is never throttled by a viewer

The binding property is that **a viewer can never slow the agent down**. The shim
reads the PTY at full speed with or without a viewer; with none it discards what
it reads, and with one it copies into a bounded per-viewer lag buffer served by a
separate writer. No PTY read ever waits on a socket write, and appending to that
buffer never waits.

`AttachLagBufferBytes` = `131072`. The derivation is from retained artifacts, both
codex-cli 0.147.0 at 200x60: an initial paint of **61,489 bytes**
(`sha256 75e0cce7…fd2d23`) and a resize repaint of **45,777 bytes**
(`sha256 29784802…8db0a1`). The buffer holds **2.13** of the larger, so a single
paint arriving while an earlier one is still draining cannot overflow it. It
bounds a burst, not a rate.

When the buffer would overflow, the viewer is **evicted** and the role continues
untouched: the client reports `attach-evicted-slow`. Eviction is chosen over
dropping bytes because a terminal stream cannot be sampled — dropping mid-sequence
leaves a partial escape sequence and a corrupted screen, while a re-attach gets a
clean repaint and a correct one.

**Residual, stated rather than hidden:** a viewer that cannot keep up with the
role's sustained output loses its seat. There is no supported minimum throughput,
because the buffer bounds a burst rather than a rate, and a viewer on a link too
slow for the harness's steady output will be evicted repeatedly. That is the
accepted cost of never throttling the agent.

**The tail after child exit.** Child exit is observed independently of the
harness's last output, and it does **not** imply PTY EOF: a surviving grandchild
that still holds the slave keeps the master open indefinitely, so no outcome may
be conditioned on EOF. Before closing, the shim spends up to
`AttachTailFlushTimeout` = `10s` delivering what it has already read. Ten seconds
is a policy cutoff, not a derivation from any byte count: whether a flush
completes depends on the volume of the tail, the rate a surviving producer keeps
adding to it, and the rate the viewer takes it.

The outcomes are distinguished because they are different facts, and each claims
only what the shim can observe — bytes it actually read and counted, never
"everything the harness ever wrote", which is not observable:

- `child-exited` — every byte counted for the flush was written to the stream.
- `tail-undelivered` — `UNDELIVERED` counted bytes were not, and the row says the
  terminal is incomplete. The count is exact.
- `tail-unconfirmed` — the flush ended without establishing its cutoff, so the
  shim reports the loss it can count and claims no total. `known_undelivered` may
  legitimately be 0, and that is the point of the member: zero *known* loss is not
  zero loss. Reporting `child-exited` here would return exit 0 over a final
  screen that may never have been read.

`BYTES` counts bytes **written to the stream**, which is the only thing the shim
can observe; whether the client received or displayed them is the client's own
observation (§15.11.7).

#### 15.11.5 Repaint and delivery ordering

The shim applies the hello's `rows`/`cols` to the retained PTY master with
`TIOCSWINSZ` **before** sending `attach-admitted`, so a failure costs no claim:
it answers `attach-refused` with `initial-size-failed` and closes. Verified:
setting the size on the master delivers `SIGWINCH` to the child's foreground
process group, which is what makes the harness redraw. Nothing is replayed and no
screen contents are stored — the repaint an operator sees on attach is the
harness redrawing, not the shim replaying.

**A control delivery is indivisible with respect to viewer input.** A registry
delivery is not a single write: it is an input-clear, a payload, a fixed delay,
and a submit. Viewer input never lands inside that sequence, and a delivery never
lands inside a viewer's chunk; whichever began first completes first. This is a
property of the role's single input ordering, and an earlier draft that shared
only a per-write serialization was wrong, because that would have let viewer
bytes land between clear and payload.

**Input is only ever delivered on behalf of the currently admitted viewer, and
only while the role is `active`.** Bytes read from a viewer that has since been
released are discarded rather than written, including when a replacement viewer
has been admitted; and while a role is `stopping`, typed bytes are not delivered
at all. That last case is the single honest exception to "every byte you type
reaches the harness", and the README states it as such.

Normative test shape: block after each delivery write and prove no viewer byte
appears anywhere inside the sequence; and prove that a chunk read from a released
viewer, or read while the role is `stopping`, never reaches the PTY.

#### 15.11.6 Detached start: spawn, reaping, readiness, rollback, relaunch

A detached role is started by the launcher spawning the current executable's
hidden shim command directly. No existing seam does this — foreground `run`
invokes the lifecycle in-process — so the launcher gains one typed spawn
boundary whose production implementation is `os/exec` and whose tests use a
recording fake, in the pattern `internal/ptyx.ChildStarter` already
establishes. The sole shell-composition site remains the tmux window command
and is not reached on this path.

The spawn is fully parent-specified; nothing is configured after exec:

- **argv** is exactly §15.1's hidden shim argv, built from validated values;
- **cwd** is the fleet's stored absolute directory;
- **env** is the launcher's environment, **including `AGENTCTL_RUNTIME_ROOT`
  and `AGENTCTL_STATE_ROOT` when they are set**, plus the §15.1 informational
  `AGENTCTL_*` values. The root overrides must be inherited: they are already
  validated by §15.2, and scrubbing them would make the shim resolve different
  roots from its launcher, so the launcher could not find the listener it is
  waiting on. The spawn adds no other override, and never passes a
  presentation, token, or path not already in the launcher's environment;
- **fds 0, 1, and 2** are each opened on `/dev/null` by the parent before exec,
  so the harness can never inherit the launcher's terminal;
- **`Setsid`** is specified as an exec attribute rather than called by the
  child, so the shim leads its own session and has no controlling terminal from
  its first instruction. `Setctty` is not set: the shim's own PTY is the
  child's controlling terminal, not the shim's.

The spawn returns the started PID, which is creation provenance.
`Setsid` does not change it, so it is comparable with the `shim_pid` in the
advisory lockfile exactly as the tmux path compares the pane PID; a mismatch is
the existing `ready-owner-disagreement`.

**The launcher owns an asynchronous `Wait` from the moment of spawn.** Without
one an exited shim becomes a zombie, and the §15.4 oracle reports it present —
verified: a direct child that called `setsid` and exited 0 still answered
`kill(pid, 0)` with success until the parent waited. Therefore, for the launch
and readiness window only, **child-exit facts come from the waiter, not from
`kill(pid, 0)`**. The waiter's observation is authoritative for that window and
the oracle is not consulted for this PID. After the launcher exits, the shim
reparents and the standing §15.4 semantics apply unchanged, because no launcher
remains to hold an unreaped exit.

Start outcomes are three families, distinguished by what is known:

| Family | Condition | Outcome |
|---|---|---|
| no child | spawn returned an error and no PID | `detached-start-failed`, rollback removes this invocation's artifacts |
| child started, then failed | a PID was returned and the waiter observed exit, or readiness failed | `detached-start-rolled-back` when cleanup observed absence; `detached-start-retained` when it did not |
| child started, disposition unknown | a PID was returned and neither readiness nor exit was observed before the deadline | `detached-start-uncertain`; nothing is removed and the record is retained |

Rollback without a presentation ID removes only what this invocation created, in
the §15.3 order: child, then runtime artifacts including `<role>.attach`, then
the durable record, then the fleet record. The absence of a presentation is not
a cleanup failure.

If the launcher dies during the readiness sequence, the started shim is
unaffected: it holds its own claim and owns its child. The role is then
observable as any other running role if the fleet record exists, or as an orphan
by §15.4 if it does not — reported, never adopted.

Readiness requires the control listener, the relay, and the §15.3 PTY
predicate. The **attach listener is required only for a detached role**; a
tmux-presented role, whose direct attach is categorically refused, does not open
one and is not held unready for its absence.

`relaunch` reads the fleet record's `presentation` and recreates the role in
that mode: a detached role is never given a presentation, and a tmux role whose
presentation is gone is recreated with one.

#### 15.11.7 The attach client's terminal

`attach ROLE` needs a real terminal on standard input and standard output, and
**the same** terminal on each — established by comparing their `fstat`
`(st_dev, st_rdev, st_ino)` triples, never by path, which is a name rather than
an identity. If either is not a terminal it refuses `attach-not-a-terminal`; if
they are different terminals it refuses `attach-terminal-mismatch`. Both refuse
before anything is mutated.

**Startup is one total order, and every failing prefix leaves the terminal
untouched.** No stage installs a signal handler or changes the terminal until
every stage that can refuse on observable facts alone has passed:

1. **Terminal checks** — the two rows above.
2. **Target validation, then presentation and listener preflight.** The standing
   fail-closed target and claim validation of §15 runs first, so a missing,
   stale, or orphaned role takes its own existing path. Then: configured mode is
   not observed presentation. A `tmux` role refuses `attach-presented-by-tmux`,
   whose literal asserts a pane and names the fleet-level command, **only when
   that presentation is exactly observed**; a `tmux` role whose presentation is
   not observed refuses `attach-presentation-missing` and names
   `agentctl relaunch ROLE`, because the fleet-level command would itself refuse
   and a remedy that cannot work is worse than none. For a detached role, only an
   observed `ENOENT` on the attach socket licenses `attach-listener-absent`; a
   wrong type or a symlink is a runtime disagreement, and a permission or I/O
   failure is an observation error selecting `attach-listener-unobservable`.
3. **The client's own terminal handle.** The client never mutates the inherited
   descriptor, because `F_SETFL` acts on an open-file description shared with the
   parent shell and would be observable there. It opens its own handle on the
   terminal it identified, confirms by `fstat` that the handle names that same
   terminal, and only then makes it non-blocking. Failures select
   `attach-terminal-observation-failed`, `attach-terminal-open-failed`,
   `attach-terminal-verify-failed`, or `attach-terminal-reopen-mismatch`; each
   attempts one close of any candidate handle, which establishes no release fact.
4. **Signal observation.** The client observes the current disposition and mask
   of `SIGINT`, `SIGTERM`, `SIGHUP`, and `SIGQUIT`, and any observation error
   refuses `attach-signal-observation-failed` naming `SIGNAL`,
   `SIGNAL_OBSERVATION`, and `CAUSE`. Exclusion is safe only for an *observed*
   fact; an error means the client cannot tell whether that signal is ordinary
   and therefore needed for restoration.
5. **Handler installation** for the eligible subset only.
6. **Raw mode.**
7. **Connection.**

**Signals: what the operator observes.** A signal observed ignored or blocked is
excluded and its inherited behaviour preserved — like `SIGKILL` and `SIGSTOP`, an
excluded signal triggers no in-process restoration and makes no promise. For the
signals actually handled:

- The terminal is restored, and the process then dies of the signal it was sent,
  so the shell reports what it always reports.
- If restoration **fails**, that outranks reproducing the signal:
  `attach-terminal-restore-failed` is selected and the process exits 6 without
  re-raising. Neither the sink nor the wait status then evidences the signal — a
  real loss of provenance, accepted because a terminal left in raw mode is the
  fact the operator must act on first.
- A signal arriving once part of a selected row has already reached the terminal
  does not **truncate the emission attempt early**. On the client's own terminal
  handle, which is non-blocking, the attempt runs to its own bound and the signal
  follows it; the signal does not cut it short.

  That bound is `AttachReportTimeout` = `2s`, and it terminates on exactly one of
  three conditions: **complete**, every byte of the selected template written;
  **error**, a write failure other than a would-block; or **deadline**, the bound
  elapsing. "Runs to its bound" means *at most* that long — a row that completes
  immediately does not wait. Two seconds is deliberately far shorter than
  `AttachTailFlushTimeout`: the tail flush is bounding delivery of the session's
  own output, which the operator wants, whereas this bounds a single small
  diagnostic line, and an operator whose terminal has stopped accepting output
  should get their exit code back promptly rather than waiting out a
  session-sized budget to be told the terminal is not draining. What lands may still be a byte-exact prefix — a non-blocking write can
  report partial progress and then `EAGAIN`, so a bounded attempt is not a
  completed one, and saturation remains one of the three permitted prefix causes.
  The promise is about ordering, not completeness: the operator does not get a
  row interrupted by its own signal handler. On a redirected sink the attempt is
  not bounded at all — that write may block indefinitely and cannot be
  interrupted — so the owner acts immediately and the sink is left holding
  whatever it holds. Promising a *finished* row on either path would be false on
  the first and unbounded on the second.
- Exactly **one** termios restoration is attempted per attachment, on every path.

**The relay, and what it costs.** The client relays every byte in both
directions; nothing is intercepted, so `Ctrl-C` reaches the harness as it would
anywhere else. Three properties are guaranteed together, and a fourth is
deliberately given up:

- **Bounded memory.** The client holds at most `AttachClientQueueBytes` =
  `131072` of relay payload, counted over queued **plus** in-flight bytes. This
  is an accounting bound on payload, not a claim about resident memory.
- **Bounded completion.** `attach` always terminates. It never waits without a
  bound on a terminal that has stopped accepting output, and it never leaves a
  worker parked in the kernel holding the process open.
- **The role is never affected.** A client that cannot keep up loses its seat by
  the ordinary eviction rule; the role continues.
- **Given up: lossless relay to an arbitrarily slow terminal.** A terminal that
  will not drain within `AttachTailFlushTimeout` after the role's output is
  finished costs the remainder, and the shortfall is **reported** rather than
  silent.

**Three counts, kept distinct**, because they classify three different failures
and are three observations by two processes: `BYTES` from the final frame (what
the shim wrote to the stream), `RAW` (what the client read from it), and
`WRITTEN` (what the terminal accepted). `RAW` short of `BYTES` with a healthy
terminal is a transport failure; `RAW` above `BYTES` is a protocol disagreement
and likewise transport failure, since one count is wrong and the client cannot
tell which. `WRITTEN` short of `RAW` with an observed write error is
`attach-stdout-failed`; with no error, because the terminal did not drain in
time, it is `attach-terminal-stalled` — a stalled terminal returns neither
success nor error and would otherwise fall through every branch.

**Diagnostics keep `stderr`'s destination.** §15.8's channel rule stands: rows go
where `stderr` goes, so `agentctl attach ROLE 2>file` puts every row in the file.
The one amendment §15.8 records is that they are not necessarily written through
descriptor 2 itself — writing a row to a broken pipe on fd 1 or 2 raises
`SIGPIPE` and **kills the process**, erasing the exit code the attachment
selected, so the row is written through a duplicate whose number is above 2 and
`EPIPE` arrives as an ordinary error. The destination is unchanged; only the
descriptor is.

**Emission is bounded, and may be incomplete.** When the destination is the same
saturated terminal the relay was using, emission is bounded and may produce a
**byte-exact prefix** of the selected template — never a paraphrase, a truncation
marker, or a summary — and when the terminal is already known not to drain it is
skipped entirely rather than manufacturing a fragment about a terminal that
cannot take messages. A signal or an I/O failure mid-write can leave a prefix
too. In every such case the exit status carries the outcome, a reader of the sink
alone cannot tell which cause produced a short row, and that ambiguity is
intrinsic. Exactness concerns the bytes agentctl **submits**; the terminal's own
`OPOST` processing may transform their display exactly as for any other write.

If the terminal cannot take the row at all, the exit status is the only signal
the operator gets, and that is stated rather than left as an implied guarantee
that a message always appears.

#### 15.11.8 Viewer behavior across stop, kill, and survivors

The attach listener stops accepting new viewers when the role's phase leaves
`active`; an admitted viewer is not evicted by the phase change.

Viewer input is **read and discarded** once the phase is `stopping`. The shim
keeps reading the viewer's socket — it must, or the bytes would queue in the
kernel and be delivered after a survivor returns the role to `active`, which
would be a delayed injection rather than a refusal — and writes none of them to
the PTY. This is the single honest exception to "every byte reaches the
harness", and it is stated as such in the README rather than left implicit. Output continues to flow to the
viewer until the child exits, because watching a role stop is the observation an
operator most needs.

On observed child exit the client reports whichever of §15.11.4's child-exit
dispositions the flush produced — `attach-viewer-ended`,
`attach-tail-undelivered`, or either `tail-unconfirmed` row when the flush ended
without establishing its cutoff. A stopping role reaches the same outcomes as any
other child exit; nothing about the stop path narrows them.

- **active → stopping → stopped.** Observed child exit requests release. If it
  wins, the §15.11.4 tail flush runs before anything is closed, so the stream
  closes only once every byte counted for that flush has been written to the
  data stream or the flush ended; the client reports whichever child-exit
  disposition that flush produced — `attach-viewer-ended` when it completed,
  `attach-tail-undelivered` when a counted tail was dropped, or either
  `tail-unconfirmed` row when the flush ended without establishing its cutoff.
- **active → stopping → active** (a stop that did not end the child). The
  listener resumes accepting, and the admitted viewer's input is admitted again.
  No viewer is evicted by the round trip.
- **kill with partial failure, or cleanup that retains ownership.** The stream
  closes when the shim's own cleanup closes it. The client reports only that its
  attachment ended; it never reports the role's disposition, which remains
  whatever `status` and the killing command report.

A viewer is never an input to whether a role stopped.

#### 15.11.9 Attach and detached exit rows

These extend §15.8 and give no existing name a second meaning. Reusing an
existing placeholder with its **own** meaning is not borrowing and is the correct
choice where the value is the same kind of thing — `SIGNAL` below is exactly
that. Cases with existing rows are reused, not duplicated: a role with no live
claim or durable record selects `missing`; a divergent state root or answerer
disagreement selects its existing row, rendered with the attach connection as the
peer; and version disagreement is reported by §15.5's directional pre-passes
rather than by any attach-specific row.

Placeholders added here: `ATTACH_PHASE` is the closed set `hello`, `admission`,
`relay`, `final`, rendered unquoted — deliberately not `PHASE`, which §15.8 fixes
to the record phases. `ROWS`, `COLS`, `BYTES`, `UNDELIVERED`, `RAW`, `WRITTEN`,
and both uid values are unsigned decimal; `BYTES` is the shim's count of bytes
written to the stream, `RAW` the client's count read from it, and `WRITTEN` the
client's count written to its terminal, and they are never substituted for one
another. `STAGE` is the closed set `terminal-check`, `identity-stat`,
`nonblocking`. `SIGNAL_OBSERVATION` is the closed set `disposition`, `mask`;
it is deliberately not `OBSERVATION`, which §15.8 already fixes to a different
closed set. `PRIOR_OUTCOME` is enumerated with the composition rule below.
`SIGNAL` is §15.8's existing placeholder with its existing meaning and rendering,
drawn here from the four candidates the client observes. `PATH` is the exact byte
string observed and then opened, rendered under the `%q` rule.

| Typed outcome | Exit | Exact factual message template |
|---|---:|---|
| `attach-viewer-present` | 5 | `agentctl: refusing to attach role ROLE in session SESSION; a viewer is already attached at PID PID (attach-viewer-present)` |
| `attach-presented-by-tmux` | 5 | `agentctl: refusing to attach role ROLE in session SESSION; the role is presented by tmux and its pane is its viewer; use agentctl attach --session SESSION (attach-presented-by-tmux)` |
| `attach-peer-unverified` | 5 | `agentctl: refusing the attach connection for role ROLE in session SESSION; connected LOCAL_PEERPID PID has uid PEER_UID; expected SHIM_UID (attach-peer-unverified)` |
| `attach-peer-unobservable` | 6 | `agentctl: could not observe the attach peer for role ROLE in session SESSION: CAUSE (attach-peer-unobservable)` |
| `attach-presentation-missing` | 5 | `agentctl: refusing to attach role ROLE in session SESSION; it was launched in tmux mode but no presentation was observed, so it has no viewer to share; recreate it with: agentctl relaunch ROLE (attach-presentation-missing)` |
| `attach-listener-unobservable` | 6 | `agentctl: could not observe the attach stream for role ROLE in session SESSION at PATH: CAUSE; no attachment was made (attach-listener-unobservable)` |
| `attach-listener-absent` | 5 | `agentctl: refusing to attach role ROLE in session SESSION; the role holds its claim but has no attach stream at PATH (attach-listener-absent)` |
| `attach-terminal-open-failed` | 6 | `agentctl: could not open this command's own handle on the attaching terminal for role ROLE in session SESSION: CAUSE; no attachment was made (attach-terminal-open-failed)` |
| `attach-terminal-verify-failed` | 6 | `agentctl: opened a candidate terminal handle for role ROLE in session SESSION but could not complete STAGE: CAUSE; no attachment was made (attach-terminal-verify-failed)` |
| `attach-terminal-reopen-mismatch` | 6 | `agentctl: opening observed terminal name PATH for role ROLE in session SESSION produced a candidate handle whose identity did not match the terminal this command is attached to; no attachment was made (attach-terminal-reopen-mismatch)` |
| `attach-signal-observation-failed` | 6 | `agentctl: could not observe the current handling of SIGNAL for role ROLE in session SESSION: SIGNAL_OBSERVATION query failed: CAUSE; no attachment was made and this terminal was not modified (attach-signal-observation-failed)` |
| `attach-terminal-mismatch` | 2 | `agentctl: refusing to attach role ROLE in session SESSION; standard input and standard output are different terminals (attach-terminal-mismatch)` |
| `attach-not-a-terminal` | 2 | `agentctl: refusing to attach role ROLE in session SESSION; standard input and output must both be terminals (attach-not-a-terminal)` |
| `attach-terminal-observation-failed` | 6 | `agentctl: could not observe the attaching terminal for role ROLE in session SESSION: CAUSE; no attachment was made (attach-terminal-observation-failed)` |
| `attach-terminal-raw-failed` | 6 | `agentctl: could not place the attaching terminal in raw mode for role ROLE in session SESSION: CAUSE; no attachment was made (attach-terminal-raw-failed)` |
| `attach-terminal-stalled` | 6 | `agentctl: attachment to role ROLE in session SESSION ended with PRIOR_OUTCOME, but this terminal stopped accepting output; WRITTEN of RAW received bytes reached it before the wait expired and the rest was not displayed (attach-terminal-stalled)` |
| `attach-stdout-failed` | 6 | `agentctl: attachment to role ROLE in session SESSION ended with PRIOR_OUTCOME, but writing its output to this terminal failed: CAUSE; WRITTEN of RAW received bytes reached the terminal (attach-stdout-failed)` |
| `attach-terminal-restore-failed` | 6 | `agentctl: attachment to role ROLE in session SESSION ended with PRIOR_OUTCOME, but restoring the attaching terminal failed: TERMIOS_CAUSE (attach-terminal-restore-failed)` |
| `attach-transport-failed` | 6 | `agentctl: attach transport for role ROLE in session SESSION failed during ATTACH_PHASE: CAUSE (attach-transport-failed)` |
| `attach-evicted-slow` | 6 | `agentctl: attachment to role ROLE in session SESSION was ended because keeping it would have required buffering more than 131072 bytes of role output; ending it stopped nothing in the role (attach-evicted-slow)` |
| `attach-ended-cleanup-retained` | 6 | `agentctl: attachment to role ROLE in session SESSION ended while the shim retained ownership during cleanup; the role's disposition is not established by this command (attach-ended-cleanup-retained)` |
| `attach-ended-server-closing` | 6 | `agentctl: attachment to role ROLE in session SESSION ended because the shim closed the stream; the role's disposition is not established by this command (attach-ended-server-closing)` |
| `attach-viewer-ended` | 0 | `agentctl: role ROLE in session SESSION ended while attached; BYTES bytes were relayed (attach-viewer-ended)` |
| `attach-tail-unconfirmed` | 6 | `agentctl: role ROLE in session SESSION ended while attached; BYTES bytes were relayed and UNDELIVERED further bytes are known not to have been, but the output cutoff was never confirmed, so whether any more of its final output was missed is unknown (attach-tail-unconfirmed)` |
| `attach-tail-unconfirmed-none-known` | 6 | `agentctl: role ROLE in session SESSION ended while attached; BYTES bytes were relayed and no further bytes are known to have been missed, but the output cutoff was never confirmed, so whether any of its final output was missed is unknown (attach-tail-unconfirmed-none-known)` |
| `attach-counter-exhausted` | 6 | `agentctl: attachment to role ROLE in session SESSION was ended after BYTES bytes because a byte counter reached the largest exactly representable value; ending it stopped nothing in the role (attach-counter-exhausted)` |
| `attach-tail-undelivered` | 6 | `agentctl: role ROLE in session SESSION ended while attached; BYTES bytes were relayed, but UNDELIVERED bytes of its final output could not be delivered before the flush deadline and were dropped; the terminal above is incomplete (attach-tail-undelivered)` |
| `attach-resize-failed` | 6 | `agentctl: could not apply window size ROWSxCOLS to role ROLE in session SESSION: CAUSE (attach-resize-failed)` |
| `presentation-flag-conflict` | 2 | `agentctl: --detached and --tmux are mutually exclusive` |
| `run-template-flag-conflict` | 2 | `agentctl: --from-template excludes --harness, --model, and --effort` |
| `run-template-role-absent` | 2 | `agentctl: role ROLE is not in template FILE (run-template-role-absent)` |
| `run-template-role-duplicate` | 2 | `agentctl: role ROLE appears more than once in template FILE (run-template-role-duplicate)` |
| `detached-start-failed` | 8 | `agentctl: could not start a detached shim for role ROLE in session SESSION: CAUSE; no child was started and cleanup removed every artifact owned by this invocation (detached-start-failed)` |
| `detached-start-rolled-back` | 8 | `agentctl: detached shim PID PID for role ROLE in session SESSION failed before readiness: CAUSE; cleanup observed child absence and removed every artifact owned by this invocation (detached-start-rolled-back)` |
| `detached-start-retained` | 9 | `agentctl: detached shim PID PID for role ROLE in session SESSION failed before readiness: CAUSE; cleanup left REMAINING: CLEANUP_CAUSE (detached-start-retained)` |
| `detached-start-uncertain` | 9 | `agentctl: detached shim PID PID for role ROLE in session SESSION neither became ready nor was observed to exit; nothing was removed and the durable record was retained (detached-start-uncertain)` |

**Selection.** The client selects exactly one row. The attachment's own
disposition — taken from the `attach-final` frame, never from an EOF — selects
the base row: `child-exited` selects `attach-viewer-ended`, `viewer-evicted`
selects `attach-evicted-slow`, `cleanup-retained` selects
`attach-ended-cleanup-retained`, `server-closing` selects
`attach-ended-server-closing`, `resize-failed` selects `attach-resize-failed`,
`tail-undelivered` selects `attach-tail-undelivered`, `counter-exhausted` selects
`attach-counter-exhausted`, and `tail-unconfirmed` selects
`attach-tail-unconfirmed` when its `known_undelivered` is at least 1 and
`attach-tail-unconfirmed-none-known` when it is 0 — two literals because one
sentence cannot state both a known loss and no known loss without asserting
something unobserved. A transport failure that prevents receiving the final frame
selects `attach-transport-failed` with the exact `ATTACH_PHASE` instead,
including when the frame the shim tried to send was the `resize-failed` one,
because the client cannot report a fact it never received.

Each refusal member selects the like-named `attach-` row, with one stated
exception: `initial-size-failed` selects `attach-resize-failed`, whose template
already renders exactly the size and cause it carries.
`attach-presented-by-tmux`, `attach-presentation-missing`,
`attach-listener-absent`, and `attach-listener-unobservable` are selected
client-side before connecting and have no wire member — a row without a frame is
correct; a frame without a possible sender was not.

**Composition.** Supersession has two levels. One **local relay** row may
supersede the base — `attach-stdout-failed` when the terminal returned an error,
`attach-terminal-stalled` when it returned no error and did not drain in time —
and `attach-terminal-restore-failed` is **outermost**, because a terminal left in
raw mode is the fact the operator must act on before anything else.
`viewer-evicted` and `terminal-stalled` are deliberately not made to compete: one
is the shim's fact and the other the client's fact that caused it, and forcing a
choice would discard whichever side noticed second.

A single supersession renders as one line using the scalar `PRIOR_OUTCOME`, the
closed set of each `DISPOSITION` member, each `REFUSAL` member,
`transport-failed`, `locally-terminated` for a caught signal or panic **only when
no base outcome had been observed**, and the local outcomes
`terminal-raw-failed`, `stdout-failed`, and `terminal-stalled`.
`terminal-observation-failed` and `terminal-mismatch` are deliberately **not**
members: the startup order guarantees both occur before any terminal mutation, so
no restore failure can compose over them and a member with no reachable
composition is dead vocabulary. `terminal-raw-failed` is a member and its literal
therefore makes no claim about restoration — an earlier draft asserted that the
prior mode was restored, which would have said restoration both succeeded and
failed in the one chain where it composes. `locally-terminated` is a fallback,
never an eraser: where a base outcome was observed it composes beneath the
restore failure rather than replacing it.

A two-level chain renders as **ordered lines**, because a scalar cannot carry a
chain and squeezing one into `PRIOR_OUTCOME` would discard the base disposition,
its counts, and the intermediate cause:

    agentctl: role ROLE in session SESSION ended while attached; BYTES bytes were relayed, but UNDELIVERED bytes of its final output could not be delivered before the flush deadline and were dropped; the terminal above is incomplete (attach-tail-undelivered)
      writing its output to this terminal failed: STDOUT_CAUSE; WRITTEN of RAW received bytes reached the terminal (attach-stdout-failed)
      restoring the attaching terminal failed: TERMIOS_CAUSE (attach-terminal-restore-failed)

Lines render in occurrence order so the operator reads the attachment's history
forwards; the selected name and exit code are the **outermost** failure's.
Severity determines the verdict, chronology determines the layout, and neither
reorders the other. These are the only compositions in the map.

`attach-listener-absent` takes exit 5, not the role-absence family: a role can
hold a live claim and a valid record while lacking its attach listener, which is
a runtime disagreement rather than an absent role.

#### 15.11.10 Required guards

These prove the observable properties above. Where a property has more than one
possible mechanism, the guard asserts the property, not the mechanism, so an
implementation may satisfy it differently without a design delta. The mechanisms
that are the only known Darwin-viable options, and the probe evidence that rules
the alternatives out, are recorded in
[the implementation notes](../plans/2026-08-14-attach-implementation-notes.md).

- The longest production path is the attach stream. Tests pin `.attach == 100`
  bytes at the §7 caps, accept 103, and refuse 104 or longer before any mutation,
  using the longest artifact rather than `.sock`. Mutation-testing covers the
  suffix, the root template, and the caps.
- Preflight order is asserted exactly per mode before any launch call:
  `[amq, harness...]` detached, `[tmux, amq, harness...]` tmux.
- **One connection, one framing.** Frame boundaries are asserted for each `kind`,
  including a data frame whose payload is byte-identical after a round trip, and
  the decoder is asserted to reject an unknown `kind`, a zero length, a length
  above each cap, invalid UTF-8 in a control payload, and a non-object top level.
- **Per-direction ordering, and nothing wider.** Input and resize frames sent by
  the client are asserted to arrive and apply in send order; output and final
  frames sent by the shim are asserted to arrive in send order. No test asserts
  an order **across** directions, because a full-duplex stream does not provide
  one — a shim may send its final before reading a resize the client has already
  written, and that is conformant.
- **The seat is released, not latched.** Each of stream EOF, a half-close of the
  viewer's write side, a read or write error, and peer death is asserted to
  revoke admission and to make the seat immediately available: a second
  connection attempted afterwards is **admitted**, not refused with
  `viewer-present`. Asserted with the role quiet — producing no output — so a
  shim that only notices a departed viewer on its next write fails the test.
- **A half-open viewer stops receiving.** After the viewer half-closes its write
  side, the shim is asserted to close the connection rather than continue sending
  output to it, so a departed viewer cannot receive beside its replacement.
- **Admission bounds application, not the transport.** Input and resize decoded
  from a viewer that has since lost admission are asserted never to reach the
  PTY, including when a replacement viewer has been admitted in between, and
  including when the bytes were already decoded before release. The assertion is
  on the rejection at the commit point, not on the absence of bytes afterwards,
  which a never-scheduled write would satisfy vacuously.
- **The agent is never throttled.** The PTY drain rate is asserted unchanged by
  viewer presence, viewer speed, and viewer absence; a viewer that stops reading
  is evicted at `AttachLagBufferBytes` and the role continues, with `status`
  unaffected.
- **One terminal decision, split by transport health.** On a healthy stream,
  firing viewer departure, eviction, child exit, cleanup, and a failed
  `TIOCSWINSZ` concurrently against one admitted viewer yields exactly one final
  frame, one disposition, and one close. With an induced transport fault — peer
  abort, or a stream broken mid-frame — the client is asserted to receive **no**
  complete final, to infer no disposition, and to select `attach-transport-failed`
  with its phase. A protocol violation **after** admission is asserted to yield a
  `server-closing` final rather than a bare close; a protocol violation **before**
  admission — a frame preceding the applicable hello, a second hello, or a resize
  before admission — is asserted to close without a final and to select
  `attach-transport-failed` with `ATTACH_PHASE` of `hello` or `admission`
  respectively, with no disposition inferred.
- **A departed viewer's input never reaches the harness.** Bytes read from a
  viewer that has been released are asserted never to be written to the PTY,
  including when a replacement viewer is admitted in between; and input is not
  delivered while the role is `stopping`.
- **The tail outcomes are exact.** A reading viewer receives every byte counted
  for the flush and keeps `child-exited`; a viewer that stops reading yields
  `tail-undelivered` whose `bytes` plus `undelivered` equals the total counted;
  a flush that ends without establishing its cutoff yields `tail-unconfirmed`,
  asserted in both the zero and non-zero `known_undelivered` shapes, and neither
  may exit 0. Child exit with a grandchild still holding the slave completes
  without waiting for an EOF that never arrives.
- **Terminal identity and refusals.** `stdin` and `stdout` on different terminals
  refuse `attach-terminal-mismatch` before either is touched. Identity is
  asserted behaviourally: two descriptors on one terminal are treated as the same
  terminal even when their paths or access modes differ, and two distinct
  terminals are never treated as one.
- **Peer-state isolation.** Asserted from a parent process, not from inside the
  client: while the child relays, after `SIGSTOP`, after `SIGKILL`, and after
  normal exit, the parent's descriptor flags are unchanged — run with the
  original `O_NONBLOCK` clear **and** set.
- **The preflight distinguishes configured from observed.** A tmux-mode record
  whose presentation is not observed selects `attach-presentation-missing`, not
  `attach-presented-by-tmux`; a detached record with an observed `ENOENT` selects
  `attach-listener-absent`, while a permission failure selects
  `attach-listener-unobservable`. Neither arrives as a frame, and the decoder
  rejects both names if a peer ever sends them.
- **Startup order by failing prefix.** A non-TTY invocation selects
  `attach-not-a-terminal` and never reaches the signal stage; a private-handle
  failure selects its own row with no handler installed; a signal-observation
  failure refuses with no signal handling installed, the terminal mode unchanged,
  no connection made, and no role state changed — asserted for each candidate, each
  observation stage, and with all of them failing, with the row naming the first
  in the fixed traversal.
- **The empty eligible set changes nothing outside the candidates.** With all
  four candidates observed ignored or blocked, the handling of every signal —
  the four candidates and unrelated ones such as `SIGPIPE` — is asserted
  unchanged from before the attach, and each retains its inherited behaviour.
- **Signals produce the promised observation.** A handled signal restores the
  terminal and yields a signal wait status; a signal inherited ignored is
  excluded, yields the selected exit code, and makes no wait-status claim; a
  restore failure exits 6 without re-raising. A second registration for the same
  signal must not swallow the re-raise. Exactly one termios restoration is
  asserted on every path.
- **The client is bounded in payload and in time.** Against a terminal that never
  drains — with the queue full, and with a chunk in flight — queued plus
  in-flight stays within `AttachClientQueueBytes`, `attach` exits within its
  stated bound, the terminal mode is restored, and the role survives untouched.
- **The three counts classify separately.** `RAW` short of `BYTES` with a healthy
  terminal selects `attach-transport-failed`; `RAW` above `BYTES` selects the
  same; `WRITTEN` short of `RAW` with a write error selects
  `attach-stdout-failed` and without one selects `attach-terminal-stalled`,
  including when terminal progress was partial rather than zero.
- **A broken redirected sink does not erase the outcome.** With fd 2 a pipe whose
  reader has closed **and fd 1 closed**, the row is asserted to be written to the
  same destination fd 2 names — the same open-file description, not merely a
  similar one — the broken pipe is asserted to surface as an ordinary error
  rather than as process death, and the process is asserted to exit with the
  outcome's own code. No assertion claims the row *reached* a reader that has
  closed.
- **Diagnostic routing follows the destination.** `agentctl attach ROLE 2>file`
  puts every row in the file, including when the relay terminal has stalled;
  rows raised before any private handle exists route the same way.
- **The signal/emission promise is asserted as ordering, not completeness.** A
  signal arriving mid-row on the client's own terminal handle is asserted not to
  cut the attempt short: the attempt runs to `AttachReportTimeout` and the signal
  follows, with what landed asserted to be the whole row **or** a byte-exact
  prefix — never a row interrupted mid-write by the handler, and never asserted
  complete, since a saturated terminal can leave a prefix within the bound. The
  bound is asserted as a maximum, not a wait: a row to a healthy terminal
  completes without consuming `AttachReportTimeout`. On a
  redirected sink that will not drain, the same signal is asserted to yield
  immediate owner action and a sink holding nothing, a prefix, or the whole row,
  with no bound and no full-row guarantee.
- **Emission shapes.** Where the destination is the saturated relay terminal,
  emitted bytes are asserted to be a byte-exact prefix of the selected template,
  never a paraphrase or marker; emission is asserted skipped when
  `attach-terminal-stalled` is already selected; and the exit status is asserted
  in the complete, prefix, and nothing cases.
- **Composition renders exactly.** The three-fact chain is asserted byte-for-byte
  against the normative template — disposition with its counts, the stdout cause
  with `WRITTEN` and `RAW`, the restore cause — exiting with the outermost row's
  code.
- **Byte counters are exact at the boundary.** With a counter near its maximum,
  no reported value ever exceeds the representable range, no reported count is
  ever an estimate, and the attachment ends with `counter-exhausted` reporting
  exact values.

#### 15.11.11 Release obligations

Two statements are owed to this release's notes and no document in the
repository carries them; promotion must not omit either.

1. **Presentation defaults to detached.** `agentctl launch` with no flag starts
   a fleet with no tmux presentation. Operators who want windows pass `--tmux`.
2. **Fleets started by the older tmux-metadata lifecycle are not adopted.** They
   leave no shim record, are neither recognized nor migrated, and should be
   stopped with the binary that started them.


## 16. Embedded skill installation

`agentctl skill install` and `agentctl skill status` write and inspect the
agent-facing skill that ships inside the binary. This section records
long-shipped behavior; it introduces no new surface.

### 16.1 Targets and modes

Two fixed target directories are derived from the resolved home directory, with
no caller-supplied path component:

```text
$HOME/.claude/skills/agentctl
$HOME/.agents/skills/agentctl
```

Directories are created and normalized to mode `0755`; skill files and the
manifest are written mode `0644`. A failed home resolution is a refusal, not a
fallback.

Every file is committed by writing a same-directory temporary file and renaming
it over the target, never by writing in place. Rename replaces the directory
entry, so another name for the previous file — a hard link, or a symlink reached
by some other path — keeps its own content. This is what makes the fixed-target
write boundary hold for alias cases that ownership validation does not
enumerate.

### 16.2 Ownership manifest

Each target carries `.agentctl-skill.json`:

```json
{ "version": "<release>", "files": { "<relative path>": "<sha256 hex>" } }
```

The manifest records every installed file. It is how a later invocation proves
the target is one agentctl wrote, rather than assuming it from the path.

### 16.3 Refusal semantics

An install proceeds only when the target is absent, or is a real directory
whose contents agentctl can account for. The target is **unowned**, and the
install refuses, when any of the following is observed:

- the target path is a symlink;
- the target path exists and is not a directory;
- no manifest is present — the refusal names the first offending file found;
- the manifest is unreadable or malformed;
- any entry under the target is not a regular file — a symlink, for instance,
  which would otherwise let a later write follow the link outside the target;
- any file under the target hashes to neither its recorded entry in the
  installed manifest nor its content in the embedded skill;
- a manifest entry names a path that escapes the target directory, which is
  refused before any mutation.

The last rule is deliberately broader than "files match the manifest": a file
the operator added by hand also makes the target unowned, because agentctl
cannot prove it wrote it. A file already matching the embedded content is
accepted, so an interrupted upgrade re-runs cleanly.

### 16.4 `--force` and no-op

`--force` overrides only the unowned refusal. It enumerates the target, removes
the directory, reports every removed path, and reinstalls. It does not bypass
inspection, hashing, or write failures, which remain refusals with or without
it.

When the existing manifest already matches the embedded skill, the install is a
no-op reported as `current` and nothing is written. Files present in the
previous manifest but absent from the new one are removed and reported.

`agentctl skill status` performs no writes.
