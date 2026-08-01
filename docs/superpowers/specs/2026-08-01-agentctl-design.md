# agentctl design — 2026-08-01

Status: approved in design session 2026-08-01.
Companion documents: [`brief.md`](../../../brief.md) (normative requirements), [`SECURITY.md`](../../../SECURITY.md) (threat model).

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
| Harness process check | Must accept documented executable-name variations discovered in the 2026-08-01 spike (§3.3): Claude Code's foreground process name is its **version string** (e.g. `2.1.220`), not `claude`. |

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
- The foreground-process check cannot compare `pane_current_command` directly to the harness name for Claude Code. Policy in §8.

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
| `internal/harness` | Harness registry (claude, codex): model-argument rendering, expected-process policy, input-clear sequence |
| `internal/shellq` | POSIX single-quote escaping; tiny, table- and fuzz-tested |
| `internal/tmuxx` | `Runner` interface (real: `os/exec`; fake: records argv for tests) plus typed wrappers: `NewSession`, `NewWindow`, `SetOption`, `ShowOptions`, `ListPanes`, `SendKeys`, `KillSession`, `DisplayMessage` |
| `internal/fleet` | Launcher, rollback handler, metadata writer |
| `internal/session` | Session resolver (precedence chain; explicit failure when unresolvable) |
| `internal/target` | Managed-metadata reader; 8-step target validation chain |
| `internal/control` | Hardcoded payload registry `{clear: "/clear", compact: "/compact"}`; dispatcher |
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
6. After each window: stamp metadata (§6.5).
7. Any failure after session creation: stop, `kill-session` **only if this invocation created it**, report the failed role on stderr, exit 8.

### 6.2 clear / compact

1. Resolve session; confirm it exists and is agentctl-managed.
2. Resolve window by exact name = ROLE; confirm window-managed, stored role matches, exactly one pane, pane alive.
3. Foreground-process check (§8); fail closed → exit 5.
4. Deliver to the resolved pane ID: `send-keys C-u`, `send-keys -l -- '/PAYLOAD'`, brief fixed delay, `send-keys Enter`.
5. Success means tmux accepted the keystrokes — reported as delivery, never as execution.

### 6.3 status

Collector uses only `show-options`, `list-windows -F`, `list-panes -F`. States: `running`, `dead`, `missing`, `unexpected-process`, `unmanaged`. Because managed windows run without `remain-on-exit`, an exited agent's window closes and normally reports `missing`, not `dead` — documented in `--help` and README. JSON output uses the versioned schema from the brief (`"schema": 1`). Human output is the brief's table.

### 6.4 attach / kill

`attach`: refuse when the session is missing or unmanaged; detect iTerm2 via `TERM_PROGRAM=iTerm.app` and report clearly when not in iTerm2 or when control mode cannot be established; run `tmux -CC attach-session -t '=SESSION'`; never create sessions. `kill`: same managed-session gate, then `tmux kill-session -t '=SESSION'`.

### 6.5 Metadata

Exactly as the brief: session options `@agentctl_managed=1`, `@agentctl_version=1`; window options `@agentctl_managed=1`, `@agentctl_role`, `@agentctl_harness`, `@agentctl_model` (empty string when defaulted — always set, never omitted, so exact fleet comparison is a straight read). No metadata database.

## 7. Validation rules (consolidated)

- Session and role names: `^[a-z0-9][a-z0-9_-]*$`.
- Model identifiers: `^[a-zA-Z0-9][a-zA-Z0-9._-]*$` (catalogue-free; charset-bound).
- Harnesses: `claude` | `codex` only.
- All rejection cases from the brief's Validation section: unknown harnesses, duplicate roles, duplicate model entries, models for undefined roles, missing values, empty `--roles`, trailing commas, whitespace in names, names beginning with `-`, duplicate command-line options.
- `--dir`: must be an existing directory.

## 8. Foreground-process policy

The check compares evidence about the pane's foreground process against the stored harness, failing closed (exit 5) on mismatch:

- `codex` → `pane_current_command == "codex"`.
- `claude` → `pane_current_command == "claude"`, **or** a semver-shaped name (`^[0-9]+(\.[0-9]+)*$`) whose executable path — resolved via `ps` against the pane PID — contains a `claude` path segment (spike: Claude Code 2.1.220 reports `2.1.220`).

Any further variation discovered during implementation must be added to this documented list with a test; nothing is accepted by default. The check is a safety guard, not an idleness proof.

## 9. Exit codes

The brief's table verbatim (0, 2–8). `kill` uses 3 for unresolvable/missing/unmanaged sessions and 6 for tmux failures.

## 10. Testing

- Unit tests against the fake `Runner` asserting **exact argv** for every case in the brief's Testing section, plus: `kill` refuses unmanaged sessions; `--dir` propagates to `-c`; model charset rejections; claude version-string process-name acceptance.
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

## 12. Out of scope

Everything in the brief's Out of scope list, plus `--if-missing` (deferred, §2). The brief's acceptance criteria apply, extended by: `agentctl kill` refuses unmanaged sessions; model charset enforcement; deterministic cwd propagation.
