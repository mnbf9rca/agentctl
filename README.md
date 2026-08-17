# agentctl

`agentctl` launches and operates named fleets of AI coding agents. Each role is
owned by a resident per-role shim that holds a kernel lock, owns the harness
PTY, and accepts only a closed set of operation names such as `clear` and
`compact`.

**Fleets run detached by default.** Roles start without any terminal attached;
you attach the ones you want to watch, in whatever terminals you like. Pass
`--tmux` if you would rather have one tmux window per role. The choice changes
how you *see* roles. It changes nothing about how you *manage* them.

## Quickstart

Launch from the repository the agents should work in. The directory is
persisted for the whole fleet.

### Default: detached

```bash
cd /path/to/application

agentctl launch \
  --session epic123 \
  --roles planner:claude,coder:codex,reviewer:claude
```

```text
agentctl: launched session "epic123" detached; 3 roles are ready
agentctl: attach a role with: agentctl attach --session epic123 ROLE
```

Nothing is displayed until you ask for it. Open as many terminals as you want
and attach the roles you care about:

```bash
agentctl attach --session epic123 planner    # in one terminal
agentctl attach --session epic123 coder      # in another
```

tmux is not installed, started, or required on this path. `--detached` is
accepted and means the same thing as the default, for when you want the command
to say so out loud.

### Opt in: tmux

```bash
cd /path/to/application

agentctl launch --tmux \
  --session epic123 \
  --roles planner:claude,coder:codex,reviewer:claude
```

```text
agentctl: launched session "epic123"; 3 roles are ready
agentctl: attach the fleet with: agentctl attach --session epic123
```

One tmux window per role, viewable together in iTerm2. This is the only path
that needs tmux installed.

### Managing the fleet is the same either way

These commands name a **session and a role**. They never name a terminal, a
window, or a pane, so they behave identically for both launches above — and
they work from a third terminal that attached nothing at all:

```bash
agentctl status  --session epic123
agentctl clear   --session epic123 coder
agentctl compact --session epic123 reviewer
agentctl relaunch --session epic123 coder
agentctl kill    --session epic123
```

`status` reports tmux presentation as one extra observed fact — `present`,
`gone`, or `unavailable`. A detached fleet has none, and that is a mode, not a
fault. Every other column reads the same.

Delivery success means the shim wrote the fixed payload and observed submit. It
does not claim the harness executed anything.

### Templates

A template describes the fleet, including how it should be presented:

```json
{
  "version": 1,
  "presentation": "tmux",
  "roles": [
    { "role": "planner",  "harness": "claude", "effort": "max" },
    { "role": "coder",    "harness": "codex",  "model": "gpt-5.6-sol" },
    { "role": "reviewer", "harness": "claude" }
  ]
}
```

```bash
agentctl launch --session epic123 --from-template fleet.json
```

`presentation` is `detached` or `tmux`. Omitting it means detached, exactly as
omitting the flag does. An explicit `--detached` or `--tmux` overrides the file.

## Installation

### Preferred — via Homebrew

```sh
brew install mnbf9rca/tap/agentctl
agentctl skill install
```

Reinstall the embedded agent skill after every upgrade.

### Build from source

```sh
make build
```

Every role start requires the agentctl binary, `amq`, and each selected harness
(`claude` or `codex`) to resolve during preflight. **tmux is required only when
you ask for a tmux presentation.** iTerm2 is needed only for fleet-level
`agentctl attach`.

## Seeing a role

```bash
agentctl attach --session epic123 planner
```

Your terminal becomes that role's terminal — any terminal, including one inside
tmux. Type into it as you would a tmux pane. **To stop viewing, close the
terminal — the role keeps running.**

Roles launched with `--tmux` are viewed in tmux instead: that pane is already
the role's viewer, so `attach ROLE` refuses and points you at
`agentctl attach --session SESSION`.

- Every byte you type goes to the harness. Nothing is intercepted, so `Ctrl-C`
  interrupts the agent's current turn exactly as it would anywhere else. The one
  exception: while a role is stopping, typed bytes are not delivered.
- `attach` needs a real terminal on both input and output, and the same one on
  each. Redirecting either — `agentctl attach ROLE > file`, or reading input
  from elsewhere — is refused rather than half-handled. Redirecting `stderr`
  works normally and still captures what agentctl reports.
- On attach the screen repaints, so you see where the role is now.
- Output produced while nobody was attached is discarded — there is no
  scrollback across a detach. agentctl keeps reading so the harness never
  blocks.
- One viewer at a time. A second attach refuses and names the attached PID.

Bare `agentctl attach --session epic123` uses the tmux presentation. Without
one it refuses and lists the roles you can attach individually.

## Foreground: `run`

`run` starts one role on your terminal and stays in the foreground until the
harness exits. Use it for the single role you want to watch continuously; use
`launch` to start a fleet.

```bash
agentctl run --session epic123 --role planner --harness claude --effort max
agentctl run --session epic123 --role planner --from-template fleet.json
```

The current working directory is the session-wide fleet directory; `run` has no
`--dir`. For a new session it creates a one-role fleet; for an existing one it
adds a role, or replaces that role's stored settings after the new shim is
ready. A directory mismatch refuses before anything starts and prints both
paths.

## Command reference

```text
agentctl launch [--tmux|--detached] --session S --roles ROLE:HARNESS,... [--models ...] [--efforts ...] [--dir PATH]
agentctl launch [--tmux|--detached] --session S --from-template FILE [--roles ...] [--models ...] [--efforts ...] [--dir PATH]
agentctl run --session S --role ROLE (--harness H [--model M] [--effort L] | --from-template FILE)
agentctl attach [--session S] [ROLE]
agentctl status [--session S] [--json]
agentctl clear   [--session S] ROLE
agentctl compact [--session S] ROLE
agentctl relaunch [--session S] [--harness H] [--model M] [--effort L] [--dir PATH] ROLE
agentctl kill [--session S]
agentctl version | skill install [--force] | skill status
```

- **launch** writes the durable roster before starting any role and reports
  success only after every shim answers ready. It refuses an existing fleet
  rather than adopting it.
- **attach** with a role connects your terminal to that role; without one it
  requires an observed tmux presentation and attaches by typed session ID.
- **status** without `--session` lists every durable fleet. `running` is a
  claim/readiness fact, not a statement that the agent is healthy or on task.
- **relaunch** starts a replacement only for `missing` or an ESRCH-backed
  `stale-record`, never beside a possible survivor.
- **kill** requires separate signal-attempt and child-exit observations, then
  removes presentation and the fleet record last.

Session selection for acting commands is explicit `--session`, then
`AGENTCTL_SESSION`, then the current tmux session from a validated
`TMUX_PANE`. `AM_ROOT`, `AM_SESSION`, `TMUX`, and directory names are never
selection sources.

## Where state lives

| Purpose | Default | Override |
| --- | --- | --- |
| Volatile socket, attach stream, lifetime lock | `/tmp/agentctl-<uid>/v1` | `AGENTCTL_RUNTIME_ROOT` |
| Durable fleet and child records | `os.UserConfigDir()/agentctl/state-v1` | `AGENTCTL_STATE_ROOT` |

Both roots and their children are private and descriptor-verified. The lockfile
records the resolved durable root; a command that resolves a different one
reports `state-root-disagreement` rather than choosing a side or calling the
role missing. Use the same `HOME`/override context as the running shim.

## When something looks wrong

Only an `ESRCH` process observation lets agentctl call a recorded child absent
or permit a relaunch. A live child with a dead shim is `orphan`; a dead shim
with no recorded child is `indeterminate-child-starting` and never expires on
its own. Both preserve evidence deliberately: investigate or stop the surviving
process rather than deleting records to force a relaunch beside it. Full
recovery procedure is in the
[design specification](docs/superpowers/specs/2026-08-01-agentctl-design.md).

tmux layout operations — join, break, swap, regroup — never change role
identity or control. A detached fleet cannot encounter that class of problem at
all.

## Learn more

- [Design specification](docs/superpowers/specs/2026-08-01-agentctl-design.md) —
  normative behavior, architecture, state vocabulary, and exit codes.
- [SECURITY.md](SECURITY.md) — threat model, operating posture, and accepted
  residuals.
- [tmux-less launch design notes](docs/superpowers/plans/2026-08-13-tmuxless-launch-design-notes.md) —
  why this UX, and what was rejected.
- [Release checklist](docs/release-checklist.md) — live release evidence.
- Agent-facing usage: `agentctl skill install`, then `/skills` in a harness.
