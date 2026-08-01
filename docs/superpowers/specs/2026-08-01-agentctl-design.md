# agentctl design — 2026-08-01

Status: approved in design session 2026-08-01.
Companion documents: [`docs/brief.md`](../../brief.md) (normative requirements), [`SECURITY.md`](../../../SECURITY.md) (threat model).

This spec records the decisions, verified external contracts, and architecture agreed in the design session. Where this document and `brief.md` conflict, this document wins — every deviation from the brief is listed in §2.

## 1. Summary

`agentctl` is a standalone Go CLI that launches a named tmux session containing a fleet of autonomous agents (one window per role, each started via `amq coop exec`), records role/harness/model metadata in tmux options, delivers only predefined control payloads (`/clear`, `/compact`) to validated panes, reports objective fleet status, and attaches the fleet as native iTerm2 tabs via tmux control mode.

## 2. Decisions resolved in the design session

These extend or refine `brief.md`:

| Topic | Decision |
|---|---|
| Agent working directory | Windows start in agentctl's invocation cwd, passed **explicitly** to tmux via `-c` (never relying on tmux server default). Optional `--dir PATH` on `launch` overrides. Rationale: `amq coop exec` roots `AM_ROOT`/`.amqrc` in the pane's cwd, so cwd determines the fleet's AMQ session directory. |
| Teardown | New command `agentctl kill [--session S]`. Validates the session is agentctl-managed (same gate as control commands) before `tmux kill-session`. Refuses unmanaged sessions. |
| Model identifier validation | Models are catalogue-free but **not** charset-free: `^[a-zA-Z0-9][a-zA-Z0-9._-]*$`. The mandatory alphanumeric first character makes flag smuggling (e.g. `--dangerously-bypass-approvals-and-sandbox`) unrepresentable in the model slot. |
| `--if-missing` | Deferred. v1 `launch` always fails when the target session exists. The exact-fleet-comparison metadata is designed in now so `--if-missing` is cheap later; tracked as a deferred backlog issue. |
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

### 3.3 Keystroke-injection spike results

Method: throwaway tmux server (`tmux -L agentctl-spike`), real harness binaries, `capture-pane` snapshots between steps (spike only — the product never scrapes).

| Check | Claude Code | Codex |
|---|---|---|
| Junk pending input registered | yes | yes |
| `C-u` cleared pending input | yes (TUI offered Ctrl+Y undo) | yes (placeholder text returned) |
| Literal `/clear` + separate `Enter` executed | yes | yes |
| `#{pane_current_command}` | **`2.1.220`** (version string) | `codex` |

Consequences:

- The delivery sequence in the brief (`C-u`; `send-keys -l -- '/PAYLOAD'`; `Enter`) is adopted unchanged for both harnesses, with a short fixed delay between payload and Enter (the spike used 1s; implementation may tune down with testing).
- Typing a slash command opens an autocomplete popup in both TUIs; Enter selects the highlighted entry. With the full command typed, the exact match was highlighted in both. Residual risk (user-defined commands outranking an exact match) is documented in SECURITY.md.
- Process names cannot be pattern-matched against harness names (Claude Code reports `2.1.220`), and `#{pane_current_command}` tracks the *foreground job*, so it flaps to child commands (`bash`, `python`, …) whenever an agent runs a tool. Both problems are solved by the launch-time baseline policy in §8.

## 4. CLI surface

```
agentctl launch  --session S --roles R:H,... [--models R:M,...] [--dir PATH]
agentctl attach  [--session S]
agentctl status  [--session S] [--json]
agentctl clear   [--session S] ROLE
agentctl compact [--session S] ROLE
agentctl kill    [--session S]
```

Everything else in the brief's CLI section applies verbatim: no `--launch` alternative syntax, no arbitrary-payload options of any kind, duplicate command-line options rejected. Session resolution for non-launch commands: `--session` > `AGENTCTL_SESSION` > current tmux session; `launch` requires explicit `--session`.

## 5. Architecture

Go module, stdlib only (`flag`, `os/exec`, `encoding/json`, `regexp`, `testing`). No CLI framework, no tmux client library. All tmux invocations are argv arrays via `os/exec` — agentctl never invokes a shell. The only shell-interpreted string in the system is the window command tmux itself runs via `sh`, assembled exclusively by `shellq` from charset-validated tokens.

| Package | Responsibility |
|---|---|
| `cmd/agentctl` | Subcommand dispatch, exit-code mapping |
| `internal/cliflags` | Per-subcommand flag parsing, duplicate-option rejection |
| `internal/config` | `--roles`/`--models` parsing and all validation rules (§7) |
| `internal/harness` | Harness registry (claude, codex): model-argument rendering, input-clear sequence. (Process identity is *not* harness data — it is the launch-time observed baseline, §8.) |
| `internal/shellq` | POSIX single-quote escaping; tiny, table- and fuzz-tested |
| `internal/tmuxx` | `Runner` interface (real: `os/exec`; fake: records argv for tests) plus typed wrappers, one per §13.2 operation: `ListSessions`, `NewSession`, `NewWindow`, `SetOption`, `ShowOptions`, `ListWindows`, `ListPanes`, `DeliverPayload` (§13.6 — no bare `SendKeys` is exported), `KillSession`, `DisplayMessage`, `AttachSession`, `ProcessName` (§13.7) |
| `internal/preflight` | Pure executable checker: `LookPathFunc` seam, `MissingExecutableError`, required-set derivation (`[tmux, amq]` + first-occurrence deduped harnesses). No `Runner` dependency — ordering relative to `Runner` calls is proven by `fleet` (§6.1 step 2). |
| `internal/fleet` | Launcher, rollback handler, metadata writer |
| `internal/session` | Session resolver (precedence chain; explicit failure when unresolvable) |
| `internal/target` | Managed-metadata reader; 8-step target validation chain |
| `internal/control` | Hardcoded registry of predefined, argument-free payloads (`clear → /clear`, `compact → /compact` in v1); dispatcher |
| `internal/status` | Collector (tmux format strings only) + table/JSON renderers |
| `internal/attach` | iTerm2 detection, `tmux -CC attach-session -t '=SESSION'` |

Each unit is independently testable against the fake `Runner`; no unit reads terminal contents.

## 6. Key flows

### 6.1 launch

1. Parse and validate the complete configuration (§7). Any error → exit 2, nothing created.
2. Preflight: `tmux`, `amq`, and each *requested* harness resolve on `PATH` → else exit 7.
3. Fail if target session already exists (exact match) → exit 3.
4. Resolve cwd: `--dir` if given, else invocation cwd; pass via `-c` on every window. `--dir` must name an existing
   **directory**; a path that does not exist and a path that exists as a regular file are both usage errors → exit 2,
   checked before anything is created (§7).
5. First role: `new-session`; remaining roles: `new-window` — canonical argv in §13.2 rows 2–3, where `CMD = exec amq coop exec --session S --me ROLE HARNESS [-- --model MODEL]`, assembled per §12.1. Both use `-P -F` so the launcher receives session/window/pane IDs at creation and never name-matches its own windows.
6. After each window: stamp metadata in the exact order of §6.5, then capture the process baseline by polling
   `ps -o comm= -p <pane_pid>` (§13.2 row 14) until the `amq coop exec → exec(harness)` chain has completed, and store
   the result as `@agentctl_process`. Poll parameters are fixed by §8. Timeout means the role failed to launch.
7. Any failure after the session is owned — including baseline-capture timeout: stop, kill by the typed session ID,
   report on stderr, exit 8. Failures *before* ownership are a different case. Both are specified in §6.6.

### 6.2 clear / compact

1. Resolve session; confirm it exists and is agentctl-managed.
2. Resolve window by exact name = ROLE; confirm window-managed, stored role matches, exactly one pane, pane alive.
3. Process-identity check against the recorded baseline (§8); fail closed → exit 5.
4. Self-target guard: when running inside tmux and `$TMUX_PANE` equals the resolved target pane, refuse → exit 5 (`refusing to clear own pane`).
5. Deliver to the resolved pane ID (§13.2 row 10): `send-keys C-u`, `send-keys -l -- '/PAYLOAD'`, brief fixed delay, `send-keys Enter`.
6. Success means tmux accepted the keystrokes — reported as delivery, never as execution.

### 6.3 status

`status` reports objective facts and refuses as little as possible. It is the one command that renders an unmanaged
session rather than rejecting it (§12.6).

**Reads are an allowlist.** Only §13.2 rows 6 (read session option), 8 (list windows), 9 (list panes) and 14 (process
identity) may be issued. Row 7 (read window option) is permitted by the table but unused, because row 8's format string
already carries every window option — tests assert it is **absent** from recorded calls, so an accidental per-window
read loop is caught rather than merely discouraged.

**Unmanaged session.** `@agentctl_managed` missing or not `1` → render `{"schema": 1, "session": S, "managed": false,
"agents": []}` and exit 0. A version present but not `1` is still exit 3 (§12.6).

**Roster drives enumeration.** Roles come from `@agentctl_roles` (§6.5), not from whatever windows happen to exist. A
roster role with no exactly-matching window is `missing` — including the snapshot-then-gone race, where a window
disappears between `list-windows` and `list-panes`.

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

### 6.4 attach / kill

`attach`: refuse when the session is missing or unmanaged; detect iTerm2 via `TERM_PROGRAM=iTerm.app` and report clearly when not in iTerm2 or when control mode cannot be established; run `attach-session` in control mode (§13.2 row 13); never create sessions. `kill`: same managed-session gate, then `kill-session` (§13.2 row 11). Both address the resolved session ID.

### 6.5 Metadata

Exactly as the brief, plus one addition: session options `@agentctl_managed=1`, `@agentctl_version=1`,
`@agentctl_roles`; window options `@agentctl_managed=1`, `@agentctl_role`, `@agentctl_harness`, `@agentctl_model` (empty string when defaulted — always set, never omitted, so exact fleet comparison is a straight read), plus `@agentctl_process` (the launch-time observed executable, §8). No metadata database.

Stamping order is **fixed and asserted**, because the fake `Runner` records calls in sequence and a reordering would
otherwise pass silently:

1. session `@agentctl_managed`
2. session `@agentctl_version`
3. session `@agentctl_roles`
4. then, per window, in this order: `@agentctl_managed`, `@agentctl_role`, `@agentctl_harness`, `@agentctl_model`,
   the baseline poll, and finally `@agentctl_process`.

`@agentctl_process` is last by construction: it is the only value that cannot be known before the window is running.

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
agentctl: failed to launch ROLE; removed incomplete session S
agentctl: failed to launch ROLE; failed to remove incomplete session S: CAUSE
```

Both exit 8. A failed cleanup does not become a different class of failure; it becomes a more informative message. The
launch is never retried, and cleanup is never attempted by name.

**Failure before ownership → exit 6.** If `new-session` returns output that cannot be parsed into a session ID, that is
a tmux failure, not a launch rollback: no typed ID means no ownership, and no ownership means no destruction. Kill
nothing. But tmux may have created a session regardless, so the operator must be told, and the exit-6 message appends:

```text
… ; a session named S may exist; inspect with tmux ls
```

The asymmetry is deliberate. Leaking a session the operator can see and remove is strictly better than destroying one
agentctl cannot prove it created: fail-safe beats leak-free.

## 7. Validation rules (consolidated)

- Session and role names: `^[a-z0-9][a-z0-9_-]*$`.
- Model identifiers: `^[a-zA-Z0-9][a-zA-Z0-9._-]*$` (catalogue-free; charset-bound).
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
Exit 4 additionally covers a role that resolves to more than one window (§13.5). Exit 6 additionally covers a
pre-ownership creation failure during `launch`, carrying the operator warning in §6.6.

**Exit 1 is the unclassified error.** It carries no contract semantics and never will: it asserts only "something went
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

- Unit tests against the fake `Runner` asserting **exact argv** for every case in the brief's Testing section, plus: `kill` refuses unmanaged sessions; `--dir` propagates to `-c`; model charset rejections; baseline capture (polling, `amq`-transition, timeout → rollback); equality check against `@agentctl_process` including empty-baseline fail-closed; self-target guard (`$TMUX_PANE` == target pane refused, absent/different pane allowed).
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
- Ambiguous roles (§13.5): two windows with the same name — control commands exit 4 with both window IDs named and **no**
  `send-keys` recorded by the fake `Runner`; `status` emits one row per matching window, each with state `ambiguous`.
- `shellq`: table tests + Go fuzz test asserting the **exactly-one-word** round-trip property: the rendered string,
  evaluated by `sh`, yields the original bytes *as a single shell word*. This supersedes issue #2's original
  `sh -c "printf %s <quoted>"` criterion, which cannot detect word splitting — `printf` re-uses its format across
  surplus arguments and concatenates them, so a `Quote` emitting `'planner' ':claude'` for `planner:claude` passes it.
  Assert the word count as well as the bytes:
  `set -- <quoted>; printf %s "$#"; printf ':'; printf %s "$1"` → `1:` + input.
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
6. **Version gate.** For **control and `kill`**, the managed-session gate requires `@agentctl_managed=1` **and** `@agentctl_version=1`; anything else fails closed (exit 3, "created by a different agentctl version") — a future agentctl's sessions are not ours to act on. **`status` is carved out** for the *unmanaged* case only: a session with `@agentctl_managed` missing or not `1` is reported, not refused (§6.3). A version present but not `1` remains exit 3 everywhere, `status` included: we can read another version's options but cannot trust their semantics, and reporting them as if they were ours would be a false statement rather than a missing one.
7. **Defaulted model rendering.** Metadata and JSON carry the empty string `""`; only the human-readable table renders `default`.
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

`⟨sid⟩`, `⟨wid⟩`, `⟨pid⟩` are resolved IDs; `⟨TAB⟩` is a literal 0x09 byte. Rows 1–13 are the argv **after** `tmux`;
row 14 is the one non-tmux command the `Runner` executes and is shown as a complete argv.

| # | Operation | argv |
|---|---|---|
| 1 | Resolve session | `list-sessions -F #{session_id}⟨TAB⟩#{session_name}` |
| 2 | Create session (first role) | `new-session -d -s SESSION -n ROLE -c DIR -P -F #{session_id}⟨TAB⟩#{window_id}⟨TAB⟩#{pane_id} -- CMD` |
| 3 | Create window (later roles) | `new-window -d -t ⟨sid⟩ -n ROLE -c DIR -P -F #{window_id}⟨TAB⟩#{pane_id} -- CMD` |
| 4 | Set session option | `set-option -t ⟨sid⟩ NAME VALUE` |
| 5 | Set window option | `set-option -w -t ⟨wid⟩ NAME VALUE` |
| 6 | Read session option | `show-options -qv -t ⟨sid⟩ NAME` |
| 7 | Read window option | `show-options -wqv -t ⟨wid⟩ NAME` |
| 8 | List windows + metadata | `list-windows -t ⟨sid⟩ -F <§13.3 format>` |
| 9 | List panes | `list-panes -t ⟨wid⟩ -F #{pane_id}⟨TAB⟩#{pane_pid}⟨TAB⟩#{pane_dead}⟨TAB⟩#{window_panes}` |
| 10 | Deliver payload (composite, §13.6) | `send-keys -t ⟨pid⟩ C-u` · `send-keys -t ⟨pid⟩ -l -- /PAYLOAD` · `send-keys -t ⟨pid⟩ Enter` |
| 11 | Kill session | `kill-session -t ⟨sid⟩` |
| 12 | Current session name | `display-message -p -t $TMUX_PANE #{session_name}` |
| 13 | Attach | `-CC attach-session -t ⟨sid⟩` |
| 14 | Process identity (§13.7) | `ps -o comm= -p PID` — complete argv, not prefixed by `tmux` |

Notes:

- **Rows 2–3, `--`.** Verified that tmux accepts `--` before the shell-command on both. `CMD` is the §12.1 string and
  always begins with `exec `, so it can never be read as a flag; `--` is belt-and-braces and costs nothing.
- **Rows 2–3, `-P -F`.** `-P` prints the requested IDs on stdout at creation. This removes a resolve round-trip and the
  race between creating a window and looking it up by name — the launcher never has to name-match its own windows.
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
  `#{window_id}⟨TAB⟩#{window_name}⟨TAB⟩#{@agentctl_managed}⟨TAB⟩#{@agentctl_version}⟨TAB⟩#{@agentctl_role}⟨TAB⟩#{@agentctl_harness}⟨TAB⟩#{@agentctl_model}⟨TAB⟩#{@agentctl_process}`
  Parse with `strings.SplitN(line, "\t", 8)`.
- **Unconstrained values go last.** Every field except `@agentctl_process` is charset-validated. `@agentctl_process`
  comes from `ps -o comm=` and may contain spaces (a value `weird name` was verified to round-trip intact), so it is
  placed last and absorbs any residue — a delimiter inside it cannot shift another field.
- **Reads always use `-v`.** Verified: `show-options -w` *without* `-v` quotes values containing spaces
  (`@agentctl_spacey "two words"`), which would require an unquoting routine and create a second quoting site,
  violating §5. With `-v` the raw value is printed verbatim.
- **`-q` is required on reads.** Without it, an unset option is `invalid option: NAME` on stderr with exit 1; with it,
  the result is empty output and exit 0.
- **Unset and set-to-empty are indistinguishable** (both empty, exit 0). This is acceptable and deliberate: the gate
  options `@agentctl_managed`, `@agentctl_version` and `@agentctl_process` are never legitimately empty, so empty means
  fail closed. `@agentctl_model` is legitimately empty (§12.7) and gates nothing.
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

The delay between the payload and `Enter` (§3.3) is a **package constant**, not a parameter. Timing is not part of the
call signature, so no caller — and no future subcommand — can influence it.

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

Everything in the brief's Out of scope list, plus `--if-missing` (deferred, §2). The brief's acceptance criteria apply, extended by: `agentctl kill` refuses unmanaged sessions; model charset enforcement; deterministic cwd propagation; process-identity baseline recorded and enforced; self-target guard on control commands.
