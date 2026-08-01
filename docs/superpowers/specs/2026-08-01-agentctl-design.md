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
| `internal/tmuxx` | `Runner` interface (real: `os/exec`; fake: records argv for tests) plus typed wrappers: `NewSession`, `NewWindow`, `SetOption`, `ShowOptions`, `ListPanes`, `SendKeys`, `KillSession`, `DisplayMessage` |
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
4. Resolve cwd: `--dir` if given (must exist), else invocation cwd; pass via `-c` on every window.
5. First role: `tmux new-session -d -s S -n ROLE -c DIR CMD`; remaining roles: `tmux new-window -d -t S -n ROLE -c DIR CMD`, where `CMD = exec amq coop exec --session S --me ROLE HARNESS [-- --model MODEL]`, assembled by `shellq`.
6. After each window: stamp metadata (§6.5), then capture the process baseline — poll `ps -o comm= -p <pane_pid>` (bounded, ~5s) until the value is no longer `amq` (the `exec` chain has completed), and store it as `@agentctl_process`. Timeout means the role failed to launch.
7. Any failure after session creation — including baseline-capture timeout: stop, `kill-session` **only if this invocation created it**, report the failed role on stderr, exit 8.

### 6.2 clear / compact

1. Resolve session; confirm it exists and is agentctl-managed.
2. Resolve window by exact name = ROLE; confirm window-managed, stored role matches, exactly one pane, pane alive.
3. Process-identity check against the recorded baseline (§8); fail closed → exit 5.
4. Self-target guard: when running inside tmux and `$TMUX_PANE` equals the resolved target pane, refuse → exit 5 (`refusing to clear own pane`).
5. Deliver to the resolved pane ID: `send-keys C-u`, `send-keys -l -- '/PAYLOAD'`, brief fixed delay, `send-keys Enter`.
6. Success means tmux accepted the keystrokes — reported as delivery, never as execution.

### 6.3 status

Collector uses only `show-options`, `list-windows -F`, `list-panes -F`. States: `running`, `dead`, `missing`, `unexpected-process`, `unmanaged`. Because managed windows run without `remain-on-exit`, an exited agent's window closes and normally reports `missing`, not `dead` — documented in `--help` and README. JSON output uses the versioned schema from the brief (`"schema": 1`). Human output is the brief's table.

### 6.4 attach / kill

`attach`: refuse when the session is missing or unmanaged; detect iTerm2 via `TERM_PROGRAM=iTerm.app` and report clearly when not in iTerm2 or when control mode cannot be established; run `tmux -CC attach-session -t '=SESSION'`; never create sessions. `kill`: same managed-session gate, then `tmux kill-session -t '=SESSION'`.

### 6.5 Metadata

Exactly as the brief: session options `@agentctl_managed=1`, `@agentctl_version=1`; window options `@agentctl_managed=1`, `@agentctl_role`, `@agentctl_harness`, `@agentctl_model` (empty string when defaulted — always set, never omitted, so exact fleet comparison is a straight read), plus `@agentctl_process` (the launch-time observed executable, §8). No metadata database.

## 7. Validation rules (consolidated)

- Session and role names: `^[a-z0-9][a-z0-9_-]*$`.
- Model identifiers: `^[a-zA-Z0-9][a-zA-Z0-9._-]*$` (catalogue-free; charset-bound).
- Harnesses: `claude` | `codex` only.
- All rejection cases from the brief's Validation section: unknown harnesses, duplicate roles, duplicate model entries, models for undefined roles, missing values, empty `--roles`, trailing commas, whitespace in names, names beginning with `-`, duplicate command-line options.
- `--dir`: must be an existing directory.

## 8. Process-identity policy

No name pattern-matching. Identity is established by observation at launch and verified by equality afterwards:

- **Check target.** The pane's *root* process, `#{pane_pid}` — stable across `exec` and unaffected by agent subprocesses. Never `#{pane_current_command}`, which tracks the foreground job and flaps to child commands (`bash`, `python`, …) while an agent runs tools.
- **Baseline (launch).** Poll `ps -o comm= -p <pane_pid>` until the `amq coop exec → exec(harness)` chain completes (value no longer `amq`; bounded, ~5s), then store the observed value verbatim in `@agentctl_process`. Timeout → launch failure and rollback.
- **Verification (control/status).** Re-run the same `ps` query and require **exact equality** with the stored baseline. Mismatch → `unexpected-process` in status; fail closed (exit 5) for control commands. Empty/missing baseline also fails closed.

This handles Claude Code's versioned binary name (`2.1.220` at the time of the spike) without heuristics and is robust to future harness renames. It remains a safety guard against accidents, not an authentication mechanism or an idleness proof: a same-user process can forge metadata or match by renaming an executable.

## 9. Exit codes

The brief's table verbatim (0, 2–8). `kill` uses 3 for unresolvable/missing/unmanaged sessions and 6 for tmux failures.

## 10. Testing

- Unit tests against the fake `Runner` asserting **exact argv** for every case in the brief's Testing section, plus: `kill` refuses unmanaged sessions; `--dir` propagates to `-c`; model charset rejections; baseline capture (polling, `amq`-transition, timeout → rollback); equality check against `@agentctl_process` including empty-baseline fail-closed; self-target guard (`$TMUX_PANE` == target pane refused, absent/different pane allowed).
- `shellq`: table tests + Go fuzz test (round-trip property: rendered string, evaluated by `sh`, yields the original token).
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
3. **Canonical tmux argv table.** `internal/tmuxx` owns one canonical argv per tmux operation; the table will be added to this spec before Wave 2 GO (drafted by reviewer, ratified by planner) and any change to it is a spec change.
4. **Exact targeting everywhere.** Session targets always use the `=` exact-match prefix. Windows and panes are never resolved by passing names to `-t` matching: resolve via `list-windows`/`list-panes` output with exact string comparison in Go, then address by window ID / pane ID. This is a security invariant; reviews fail PRs on it.
5. **Exit code for bad ROLE argument.** A ROLE failing `^[a-z0-9][a-z0-9_-]*$` is a usage error → exit 2. A well-formed ROLE with no matching managed window → exit 4.
6. **Version gate.** The managed-session gate for control/status/kill requires `@agentctl_managed=1` **and** `@agentctl_version=1`. Any other version fails closed (exit 3, "created by a different agentctl version") — a future agentctl's sessions are not ours to control.
7. **Defaulted model rendering.** Metadata and JSON carry the empty string `""`; only the human-readable table renders `default`.
8. **Toolchain pin.** `go.mod`'s `go` directive and CI's `go-version` must be identical (initially Go 1.26); drift is a review failure. Owned by issue #1.
9. **Validation ownership.** `internal/config` owns all value semantics: `ParseFleet` (roles/models rules) and `ValidateSessionName`. `internal/cliflags` owns flag mechanics (duplicate-option rejection). The `--dir` existence/is-directory check happens at point of use in the launch flow (`internal/fleet`), not in `config`. An explicitly supplied but empty `--models` (or `--roles`) value is a usage error; an omitted `--models` is valid. Errors for empty list entries (leading/consecutive/trailing commas) name the raw list and the entry index, since no printable entry exists.

## 13. Out of scope

Everything in the brief's Out of scope list, plus `--if-missing` (deferred, §2). The brief's acceptance criteria apply, extended by: `agentctl kill` refuses unmanaged sessions; model charset enforcement; deterministic cwd propagation; process-identity baseline recorded and enforced; self-target guard on control commands.
