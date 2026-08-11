# agentctl

`agentctl` launches and operates named fleets of AI coding agents. Each role is
owned by a resident per-role shim: the shim holds a kernel `flock`, records the
child process identity, owns the harness PTY, and accepts only closed operation
names such as `clear` and `compact`. tmux is optional presentation and launch
plumbing, not role identity or control transport.

The CLI never reads or writes AMQ state and never infers a fleet from
`AM_ROOT` or `AM_SESSION`. Harnesses still normally start through
`amq coop exec`, so operators can use the same validated session and role names
for agentctl and AMQ.

## Architecture

| Component | Responsibility |
| --- | --- |
| GitHub issues | Work graph and release scope |
| Planner | Orchestration, allocation, and merge policy |
| AMQ | Durable agent communication |
| agentctl CLI | Validation, durable fleet configuration, lifecycle commands, status, and exact output |
| Per-role shim | Lifetime role claim, child record, nested PTY, readiness, fixed payload delivery, and stop |
| tmux | Optional windows, attachment, and launch presentation |
| iTerm2 | Optional native visual interface for tmux windows |

```text
operator / agent
       |
       v
agentctl CLI ---- validated operation name ----> per-role Unix socket
       |                                           |
       | optional typed tmux argv                  | fixed registry payload
       v                                           v
tmux presentation --------------------------> shim-owned nested PTY
                                                   |
                                                   v
                                             harness child
```

Moving, joining, breaking, swapping, or regrouping tmux panes does not change
the role claim, socket, durable child identity, or delivery target. A tmux
presentation may be absent while status and control remain available.

## Installation

Homebrew is the recommended installation path:

```sh
brew install mnbf9rca/tap/agentctl
```

Every role start requires the current agentctl binary, `amq`, tmux, and each
selected harness (`claude` or `codex`) to resolve during preflight. Foreground
`run` creates and contacts no tmux server despite that executable check.
iTerm2 is needed only for `agentctl attach`.

Install the embedded agent-facing skill after installing or upgrading:

```sh
agentctl skill install
agentctl skill status
```

The installer updates files it can prove it owns and refuses unmanaged or
modified targets unless `--force` is supplied.

Release archives carry Sigstore build provenance:

```sh
gh attestation verify agentctl_<version>_darwin_arm64.tar.gz \
  --repo mnbf9rca/agentctl
```

### Build from source

```bash
make build
make test
make install
```

`make install` defaults to `~/.local/bin`. Release identities must come from
`make build`; a plain `go build` in a linked worktree can record the main
checkout revision instead of the worktree revision.

## Upgrade boundary for 0.5.0

The tmux-metadata lifecycle and the shim lifecycle are a flag-day transition.
There is no migration or dual-dialect support. Before replacing a pre-0.5
binary, stop its managed fleets with that binary. A surviving pre-0.5 tmux
session has no durable fleet record and is not adopted as a shim fleet; inspect
and remove it deliberately with tmux if the old binary is no longer available.
Never copy tmux metadata into the new state tree or synthesize a fleet record.

## Quickstart

Launch from the repository the agents should work in. The effective directory
is persisted for the whole fleet.

```bash
cd /path/to/application

agentctl launch \
  --session epic123 \
  --roles planner:claude,coder:codex,reviewer:claude \
  --models coder:gpt-5.6-sol \
  --efforts planner:max,coder:high
```

`launch` writes the complete durable roster/configuration before starting any
role, creates one optional tmux window per role, and reports success only after
every shim answers ready:

```text
agentctl: launched session "epic123"; 3 roles are ready
```

Inspect one fleet or every durable fleet:

```bash
agentctl status --session epic123
agentctl status --session epic123 --json
agentctl status
agentctl status --json
```

Deliver one fixed registry operation:

```bash
agentctl clear --session epic123 coder
agentctl compact --session epic123 reviewer
```

Success means the shim wrote the fixed payload and observed submit. It does
not claim that the harness executed the command.

Recreate a role only after runtime observation permits absence:

```bash
agentctl relaunch --session epic123 coder
```

Stop the fleet only after each recorded child exit or absence is observed:

```bash
agentctl kill --session epic123
```

## Foreground operation without tmux

`run` starts the same resident shim lifecycle directly on the caller terminal.
It creates no tmux server, window, shell, or viewer and remains in the
foreground until the harness exits.

```text
agentctl run --session SESSION --role ROLE --harness HARNESS [--model MODEL] [--effort LEVEL]
```

Example:

```bash
cd /path/to/application
agentctl run --session epic123 --role planner --harness claude --effort max
```

The current working directory is the session-wide fleet directory; `run` has
no `--dir` flag. For a new session it creates a one-role fleet. For an existing
fleet it adds a new role, or replaces that role's stored harness/model/effort
only after the new shim is ready. If the existing fleet records a different
directory, `run` refuses before starting a role or mutating the record and
prints both paths. Per-role working directories would be a separate design
change.

Other terminals can use `status`, `clear`, `compact`, `relaunch`, and `kill`
against the foreground fleet. `attach` factually refuses because there is no
tmux presentation.

## Runtime and durable roots

The shim uses two private, descriptor-verified trees:

| Purpose | Default | Override |
| --- | --- | --- |
| Volatile socket and lifetime lock | `/tmp/agentctl-<decimal-uid>/v1` | `AGENTCTL_RUNTIME_ROOT` |
| Durable fleet and child records | `os.UserConfigDir()/agentctl/state-v1` | `AGENTCTL_STATE_ROOT` |

On macOS, `os.UserConfigDir()` normally resolves beneath
`$HOME/Library/Application Support`; `$HOME`, the resolved config directory,
and both overrides are declared same-user-selectable inputs, not trust
anchors. Roots and their children are private, bounded, and rejected when
unsafe or substituted. Role/session names are capped so the production socket
path stays below Darwin's `sun_path[104]` limit.

Exact artifacts are:

```text
<runtime-root>/<session>/<role>.lock
<runtime-root>/<session>/<role>.sock
<state-root>/sessions/<session>/roles/<role>.json
<state-root>/sessions/<session>/fleet.json
```

The lockfile body records the resolved durable state root. A command whose
locally resolved root differs reports `state-root-disagreement`; it does not
follow the local tree, silently choose a side, or call the role missing. Use
the same `HOME`/override context as the running shim, or inspect both recorded
paths before deciding how to recover.

Normal stop removes owned socket/role/lock artifacts only after child absence
is observed. Fleet removal follows complete role cleanup. Commit uncertainty,
surviving children, ambiguous presentation cleanup, and filesystem observation
failures retain evidence and fail closed.

## Command reference

### `launch`

```text
agentctl launch --session SESSION --roles ROLE:HARNESS,... [--models ROLE:MODEL,...] [--efforts ROLE:LEVEL,...] [--dir PATH]
agentctl launch --session SESSION --from-template FILE [--roles ROLE:HARNESS,...] [--models ROLE:MODEL,...] [--efforts ROLE:LEVEL,...] [--dir PATH]
```

Supported harnesses are `claude` and `codex`. Template roles come first in
file order; flags can override their fields and append roles. Templates are
strict, bounded, regular-file JSON inputs. Values from a template receive the
same validation as flag values and cannot add payload operations or raw argv.
`--dir` must name an existing directory. Launch refuses an existing durable
fleet instead of adopting it.

### `run`

```text
agentctl run --session SESSION --role ROLE --harness HARNESS [--model MODEL] [--effort LEVEL]
```

All identity flags are required. `--dir` is deliberately rejected; the
observed current working directory must agree with an existing fleet.

### `relaunch`

```text
agentctl relaunch [--session SESSION] [--harness HARNESS] [--model MODEL] [--effort LEVEL] [--dir PATH] ROLE
```

Relaunch accepts only `missing` or an ESRCH-backed `stale-record`. It refuses
starting, stopping, orphaned, indeterminate, disagreement, malformed, and
could-not-observe states without starting beside a possible survivor. Stored
configuration is used by default. Successful overrides are durably persisted
only after the replacement shim answers ready; a commit-uncertain replacement
is retained and reported rather than retried or called absent.

### `attach`

```text
agentctl attach [--session SESSION]
```

Attach is an optional human-viewer operation for iTerm2, outside tmux. It
requires an exactly observed tmux presentation and attaches by typed session
ID. It never creates or infers a presentation. Without one it exits 3 with:

```text
agentctl: refusing to attach session "S"; no tmux presentation was observed; status and control remain available without tmux
```

Enable iTerm2 Settings → General → tmux → “When attaching, restore windows as
tabs in the attaching window.” In iTerm2's Command Menu, `esc` detaches while
the fleet continues. The post-attach report distinguishes an observed still
running presentation, an observed disappearance, and an unavailable probe; it
never turns probe failure into absence.

### `status`

```text
agentctl status [--session SESSION] [--json]
```

Bare status lists every durable fleet and ignores ambient session selection.
`--session` selects one. Table and schema-1 JSON report runtime state,
`anchored`/`unanchored` confidence, shim and child PIDs when observed, and
`present`/`gone`/`unavailable` tmux presentation as a separate fact. A leading
`*` comes only from resolving the caller's validated `TMUX_PANE`; it never
comes from `AGENTCTL_SESSION`.

Anchored status joined the durable record through the volatile lockfile's
recorded state root. Unanchored status found durable evidence without that
anchor, so derived absence is weaker and is labeled rather than hidden.

Runtime state precedence is:

```text
invalid-record, state-root-disagreement, protocol-skew,
answerer-disagreement, cleanup-failed, concurrent-contender, starting,
stopping, stopped, indeterminate-child-starting, running, orphan,
present-token-disagreement, present-not-ours, could-not-observe,
stale-record, missing
```

`running` is a runtime claim/readiness fact, not an assertion that the agent is
idle, healthy, or following its workflow. Presentation never changes runtime
state. A malformed durable fleet remains visible as a defective session row;
listing does not silently omit it.

### `clear` and `compact`

```text
agentctl clear [--session SESSION] ROLE
agentctl compact [--session SESSION] ROLE
```

The client sends only the validated identity and one registered operation
name. The connected shim checks the version, advisory-record/kernel-answerer
agreement, held claim, readiness/stopping state, and caller ancestry before
resolving fixed registry bytes server-side. No caller text, raw key, model,
environment value, or argument can enter the PTY payload.

### `kill`

```text
agentctl kill [--session SESSION]
```

Kill observes the entire durable roster before mutation, requests stop only
through each role socket, and requires separate SIGHUP-attempt and child-exit
facts. It then removes an optional presentation by its previously observed ID
and removes the fleet record last. Any survivor or uncertain cleanup retains
the record.

### `version` and `skill`

```text
agentctl version
agentctl --version
agentctl skill install [--force]
agentctl skill status
```

`version` and help do not open runtime roots or contact tmux. The hidden
`__shim` route is internal and is never listed as an agent-facing command.

## Session selection

`launch` and `run` require explicit `--session`. Bare `status` enumerates all
durable fleets. Acting commands resolve a session in this order:

1. explicit `--session SESSION`;
2. nonempty `AGENTCTL_SESSION`;
3. the current tmux session observed from a validated `TMUX_PANE`.

Explicit and environment names select a durable fleet without requiring a
tmux session of the same name. Invalid higher-priority input refuses instead
of falling through. `AM_ROOT`, `AM_SESSION`, `TMUX`, cwd names, and “first tmux
session” are never selection sources. Attach cannot use the current-tmux
fallback because it must run outside tmux.

## Layout-proof operation and incident recovery

Runtime identity is independent of tmux window names, metadata, pane counts,
and layout. `join-pane`, `break-pane`, `swap-pane`, and window regrouping can
change or remove presentation while the lifetime shim claim and nested PTY
continue to govern status and control. Use grouped sessions when convenient,
but they confer no management authority.

The pre-0.5 merged-layout incident remains useful transition evidence. Its
[throwaway replay](docs/security/2026-08-10-issue-182-replay-evidence.md)
observed that closing the sole absorber destroyed the old tmux session, while
relaunching all roles first caused a transient duplicate-pane interval. The
safe old-path recovery order was replacement first, exact absorber cleanup,
then remaining relaunches; `kill` plus a complete `launch` was the alternative.
That replay describes only the retired metadata path. In the shim path, layout
is presentation-only and is not a reason to relaunch a running role. The old
`classifyRelaunchWindow` seam was deleted with that path; it is an evidence
reference, not a current recovery API.

The historical aggregate note remains an observation, never a causal claim:

```text
note: all N roster roles are missing; unmanaged window "W" has N panes
```

## Orphan and indeterminate recovery

Only `kill(pid, 0)` returning `ESRCH` permits agentctl to call a recorded child
absent or allow relaunch. A matching live child with a dead shim is `orphan`;
token disagreement, `EPERM`, and other process-observation failures each
refuse. Stop or investigate the surviving process first; do not delete its
record to force a relaunch beside it.

A dead shim with a `child-starting` reservation is
`indeterminate-child-starting` and never expires automatically. Agentctl has no
recorded child PID from which to prove absence. Read `state_root` from the
existing lockfile body at
`<runtime-root>/<session>/<role>.lock`—not from the reader's current environment.
Only after independently proving that no child remains may an operator remove:

```text
<recorded-state-root>/sessions/<session>/roles/<role>.json
```

Preserve the lockfile and record while investigating; they are evidence. Never
recompute the cleanup target from a divergent `HOME` or
`AGENTCTL_STATE_ROOT`.

## Security and operating model

agentctl is a single-user accident-prevention tool, not a security boundary
against same-user processes. A role claim is a lifetime kernel `flock`; the
socket is not ownership. The lockfile body is advisory and the connection's
`LOCAL_PEERPID` is the kernel answerer fact. Their disagreement refuses; it is
not described as authentication.

Delivery remains non-transactional because a TUI may be modal or may interpret
input differently. The shim reports bytes written and submit observed, never
harness execution. Check status and the role before control, avoid controls
while the host is saturated, and investigate disagreement/indeterminate states
instead of deleting evidence or writing directly to a pane.

The one shell-interpreted value is the tmux window command, assembled at one
audited `internal/shellq` site from validated argv. agentctl itself never
invokes a shell. Every tmux/process command crosses a typed runner boundary;
tests assert exact executable and argv element boundaries.

Read [SECURITY.md](SECURITY.md) for the complete threat model and accepted
residuals.

## Agent identity variables

The launched harness receives `AGENTCTL_SESSION`, `AGENTCTL_ROLE`, and
`AGENTCTL_MANAGED=1` as informational environment values. They help a human or
agent describe its own context, but they never establish a claim, answerer,
child identity, readiness, or ancestry. `AGENTCTL_SESSION` may select a fleet
for an acting CLI command; all runtime gates still follow.

Check `AGENTCTL_*`, `AM_*`, and `TMUX` before reasoning about topology, but do
not treat any of them as ownership evidence.

## Maintainers and release verification

- The approved [design specification](docs/superpowers/specs/2026-08-01-agentctl-design.md)
  is normative.
- [SECURITY.md](SECURITY.md) defines the threat model and residuals.
- [docs/release-checklist.md](docs/release-checklist.md) defines live release
  evidence and isolation rules.
- `docs/superpowers/plans/` contains non-normative implementation plans.
