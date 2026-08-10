# agentctl

`agentctl` launches and operates a named fleet of AI coding agents in tmux. Each role gets one tmux window and starts
through `amq coop exec`, so the tmux session name and AMQ session name stay aligned. The CLI records enough metadata to
report objective fleet state and exposes only predefined controls such as `clear` and `compact`.

## Architecture and responsibilities

The responsibility split is deliberate:

| Component | Responsibility |
| --- | --- |
| GitHub issues | Work graph, issues, and waves |
| Planner | Orchestration, allocation, and merge policy |
| AMQ | Durable agent communication and workflow protocols |
| tmux | Process hosting, persistence, and terminal input |
| iTerm2 | Native visual interface for tmux windows |
| agentctl | Fleet launch, single-role relaunch, metadata, status, predefined controls, and managed teardown |

```
+----------+
| Operator |
+----+-----+
     | invokes
     v
+--------------------------+
| agentctl CLI (transient) |
+------------+-------------+
             | tmux argv:
             | create session/windows; set options metadata;
             | DeliverPayload keystrokes; kill; attach via tmux -CC
             v
+---------------------------------------+    +--------------------+
| tmux server                           |    | AMQ                |
| +-----------------------------------+ |    | mailboxes + wake   |
| | session "epic123" (one per fleet) | |    | agent <-> agent    |
| | +-------------------------------+ | |    +----------+---------+
| | | windows: one per role         | | |               ^
| | | planner, codex1, ...          | | |               |
| | | +---------------------------+ | | |               | shared by
| | | | one pane each             | | | |               | all agents
| | | | amq coop exec -> exec()   | | | |               v
| | | |   -> harness agent <------+--+--+---------------+
| | | +---------------------------+ | | |
| | +-------------------------------+ | |
| +-----------------------------------+ |
+-------------------+-------------------+
                    |
                    | tmux -CC control-mode stream
                    | operator-only attach
                    v
              +----------------------+
              | iTerm2 viewer        |
              | native tabs          |
              +----------------------+
              detach/close: agents keep running
```

agentctl does not read or write AMQ state: it does not touch mailboxes or `.amqrc`, and it never infers a target from
`AM_ROOT` or `AM_SESSION`. It also does not infer workflow state from terminal output or accept arbitrary keystroke
payloads.

## Installation

Homebrew is the recommended installation path:

```sh
brew install mnbf9rca/tap/agentctl
```

This installs a prebuilt, signed binary and brings in `tmux` as a dependency. Every fleet launch also requires `amq`
and each harness in the effective fleet (`claude` or `codex`) on `PATH`; agentctl reports the exact missing executable
before creating anything. iTerm2 is needed only for `agentctl attach`.

After installing or upgrading the binary, install its embedded agent-facing skill into both harnesses' user-scope
skill directories:

```sh
agentctl skill install
```

Run `agentctl skill status` to compare each installed copy with the binary. The installer updates files it can prove
it owns and refuses unmanaged or modified targets unless the operator explicitly supplies `--force`.

Release artifacts carry Sigstore build provenance. To verify a downloaded
tarball:

```sh
gh attestation verify agentctl_<version>_darwin_arm64.tar.gz --repo mnbf9rca/agentctl
```

### Build from source

Building from source requires Go and Make:

```bash
make build
make test
make install
```

Release identities must come from `make build`: plain `go build` inside a linked worktree records the main checkout's revision as clean, not the worktree's revision.

By default, `make install` writes `agentctl` to `~/.local/bin/agentctl`. Ensure `~/.local/bin` is on `PATH`:

```bash
export PATH="$HOME/.local/bin:$PATH"
agentctl --help
```

`PREFIX` and `DESTDIR` may be supplied to Make for packaging or a different installation prefix.

Harness and tmux behavior can change between releases. The dated versions actually exercised by the project are kept
in the [release verification checklist](docs/release-checklist.md); they are evidence, not compatibility pins.

## Quickstart: launch an eight-role fleet

Run `launch` from the application repository the agents should work in. That directory becomes every agent window's
working directory unless `--dir` names a different existing directory.

```bash
cd /path/to/application

agentctl launch \
  --session epic123 \
  --roles planner:claude,codex1:codex,codex2:codex,codex3:codex,codex4:codex,reviewer-opus:claude,reviewer-codex:codex,designer:claude \
  --models planner:fable,reviewer-opus:opus-4.8,reviewer-codex:gpt5.6-sol-xhigh \
  --efforts planner:max,reviewer-codex:high
```

The role is the stable fleet identity, tmux window name, and AMQ handle. The harness selects `claude` or `codex`; an
optional model selects a harness configuration without changing the role, and an optional effort selects how much
reasoning effort that role's harness spends. Roles omitted from `--models` or `--efforts` use their harness default,
and no corresponding flag is passed to the harness at all.

Launch ends by showing the fleet state it observed. Refresh that view, or request its JSON form, with:

```bash
agentctl status --session epic123
agentctl status --session epic123 --json
```

Without an explicit `--session`, `status` lists every tmux session on the server. `AGENTCTL_SESSION` and the current
tmux session never narrow that listing:

```bash
agentctl status
agentctl status --json
```

To open the eight role windows as native tabs, first enable this exact setting:

```text
iTerm2 Settings
→ General
→ tmux
→ When attaching, restore windows as tabs in the attaching window
```

Then run the operator command from iTerm2, outside tmux:

```bash
agentctl attach --session epic123
```

The attaching window opens the `planner`, `codex1`–`codex4`, `reviewer-opus`, `reviewer-codex`, and `designer` tmux
windows as native iTerm2 tabs. Detaching or closing iTerm2 does not terminate the agents; run the same command again to
reopen them.

> **Before sending a control:** do not run `clear` or `compact` while the fleet is saturating the host. Delivery is
> keystroke-based and is not transactional: success means tmux accepted the keys, not that the agent TUI executed the
> command. The retained one-second settle delay has the best evidence behind it, but it is not a proven floor under
> adversarial load. The orchestrator must avoid issuing controls while its own fleet saturates the machine; a human
> operator inherits the same rule. See [Security](#security-and-operating-model) for the measured residual risk.

After confirming the target role and host state, deliver a predefined control:

```bash
agentctl clear --session epic123 codex2
agentctl compact --session epic123 reviewer-codex
```

If one role's window disappears — status reports it `missing` — bring that role back on its own, without disturbing the
other seven:

```bash
agentctl relaunch --session epic123 codex2
```

When the fleet is no longer needed, terminate the managed tmux session:

```bash
agentctl kill --session epic123
```

## Command reference

### `version`

```text
agentctl version
agentctl --version
```

Prints one line in the form `agentctl IDENTITY` and exits without contacting tmux. Project builds made with `make
build` use the exact `git describe --tags --always --dirty` identity stamped by the Makefile. A binary built directly
with Go instead reports its recorded VCS revision, followed by `+dirty` when Go recorded a modified checkout, or
`development` when the binary carries no build identity. The `--version` alias is accepted only as the sole argument.

### `launch`

```text
agentctl launch --session SESSION --roles ROLE:HARNESS,... [--models ROLE:MODEL,...] [--efforts ROLE:LEVEL,...] [--dir PATH]
agentctl launch --session SESSION --from-template FILE [--roles ROLE:HARNESS,...] [--models ROLE:MODEL,...] [--efforts ROLE:LEVEL,...] [--dir PATH]
```

Creates a new managed tmux session. `--session` is always required. Without `--from-template`, `--roles` is required
as before. A template makes `--roles` optional, not forbidden: template roles come first in file order, flags override
fields on those roles, and roles added by `--roles` follow in flag order. No flag removes a template role. Supported
harnesses are `claude` and `codex`; `--models` and `--efforts` may name any role in the effective union. `--dir`
overrides the template or invocation working directory and must name an existing directory. Launch fails rather than
adopting an existing session.

Templates are strict, read-only JSON inputs with this shape:

```json
{
  "version": 1,
  "dir": "/srv/work",
  "roles": [
    { "role": "planner", "harness": "claude", "model": "opus-4-1", "effort": "high" },
    { "role": "worker", "harness": "codex" }
  ]
}
```

The file never contains a session name. `version` is required; `dir` and `roles` may be omitted, and a role's harness
may be supplied later by a matching `--roles` entry. Unknown or duplicate fields, duplicate roles, `null`, empty
strings, trailing JSON documents, non-regular files, and files over 1 MiB are refused. A template
`dir` must be absolute; omit it and use `--dir` when the invocation should choose a relative or machine-specific path.
Every effective role and field passes the same validators used by flags before launch continues.

When a template is used, launch prints one provenance line per effective role before the observed status table. It
labels fields as `template`, `flag override`, or `flags`; a template-supplied directory gets its own line. These lines
describe the values actually passed to the launch path and recorded in session metadata.

After creation succeeds, `launch` prints the same observed human-readable table as `status --session SESSION`. The
table is collected from the managed roster and live tmux state rather than echoed from the launch arguments, so a role
that has already disappeared is reported as `missing`. If that confirmation cannot be collected, launch reports the
unverified confirmation on stderr but keeps exit 0 because the fleet creation itself succeeded.

Launch records a role's process baseline only after observing the same non-`amq` root executable twice in succession.
If that bounded settle poll expires, launch leaves the role window and the rest of the fleet in place, reports the
unproven role, and exits 9. The retained role reports `no-baseline` and control commands refuse it until an operator
resolves it; other roles continue launching and keep their own independently observed state.

#### Effort levels

`--efforts` accepts opaque harness-specific mode names matching the same charset as model identifiers:
`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`. A value outside that charset, a value for an undefined role, a duplicate role entry,
or an explicitly empty `--efforts=` is a usage error (exit 2) before any tmux command runs. A well-formed name agentctl
does not recognize is forwarded unchanged; the selected harness owns the acceptance decision, so an unsupported name
surfaces as a harness startup failure rather than a pre-launch agentctl refusal. A role with no effort entry is launched
with **no** effort argument at all, leaving the harness on its own default.

Each harness receives the exact validated value in its own syntax:

| Harness | Rendered arguments | Verified against |
| --- | --- | --- |
| `claude` | `--effort LEVEL` | Claude Code 2.1.220 |
| `codex` | `--config 'model_reasoning_effort="LEVEL"'` | codex-cli 0.146.0 |

The main codex CLI has no `--effort` flag, so the level is supplied as a configuration override; the `--effort` flag
that exists for the separate Codex Security CLI is not used. Names such as codex's `none`, `minimal`, and `ultra` pass
the charset and are forwarded; agentctl does not maintain a catalogue of harness capabilities.

The selected level is recorded in window metadata (`@agentctl_effort`) and reported by `status`.

Launch records the fleet's configuration on the session so a single role can be recreated later without the operator
remembering how it was started:

| Session option | Value |
| --- | --- |
| `@agentctl_roles` | the declared role names, comma-joined, in declaration order (template roles first, then flag-added roles) |
| `@agentctl_fleet` | `role:harness:model:effort` quads, comma-joined, in the same order; defaulted model and effort values are empty (`planner:claude::`) |
| `@agentctl_dir` | the exact directory passed to tmux `-c`: the `--dir` value, or the invocation working directory |

### `relaunch`

```text
agentctl relaunch [--session SESSION] [--harness HARNESS] [--model MODEL] [--effort LEVEL] [--dir PATH] ROLE
```

Recreates one role's window inside an existing managed session, using the harness, model, effort, and directory recorded when
the fleet launched. Use it when a single agent's window has gone — closed deliberately to reload a harness, or lost to
a crash — instead of killing and relaunching the whole fleet.

It creates what is **absent**. It can also recover exactly one live managed `no-baseline` window by removing that exact
window ID and creating a newly observed replacement, but only while another window keeps the session alive. Every
other present-window state is refused and reported using the same vocabulary as `status`:

```text
agentctl: refusing to relaunch coder; role coder already has 1 window in epic123 (@7 running); relaunch accepts only an absent role or a recoverable no-baseline window
agentctl: refusing to relaunch coder; role coder already has 2 windows in epic123 (@7 ambiguous, @9 ambiguous); relaunch accepts only an absent role or a recoverable no-baseline window
```

A `dead` window is refused too. Relaunch is not a restart: a dead pane still exists and may hold the output that
explains why the agent stopped, so it is never killed and recreated for you. Remove the window yourself once you have
finished with it, then relaunch.

If the `no-baseline` window is the session's only window, relaunch refuses before preflight or mutation because tmux
would destroy the session along with that window. The refusal reconstructs the managed remedy from validated fleet
metadata: `agentctl kill --session SESSION`, followed by the complete equivalent `agentctl launch ...` command. It
omits empty model and effort maps; if legacy metadata cannot reconstruct the whole fleet, it prints no potentially
incorrect command block and says so.

Success states exactly what was created and where each part of the configuration came from:

```text
agentctl: relaunched coder in epic123: window @11, pane %24, harness codex (stored), model gpt-5.6 (stored), effort high (stored), dir /work/epic123 (stored)
```

`--harness`, `--model`, `--effort`, and `--dir` each override one field, and the override is reported as such — a role relaunched
onto a different harness never reads as an ordinary member of the fleet. `--harness`, `--model`, and `--effort` overrides are
written back into `@agentctl_fleet`, so the recorded configuration always matches the running fleet. A `--dir`
override is **not**: `@agentctl_dir` records where the fleet was launched, and the other roles still run there, so the
divergence is reported instead:

```text
agentctl: coder now runs in /tmp/scratch; the fleet's recorded directory /work/epic123 is unchanged
```

A session launched by a version of agentctl that predates `@agentctl_fleet` and `@agentctl_dir` records no per-role
configuration. Relaunch refuses it rather than guessing, and asks for the configuration explicitly:

```text
agentctl: refusing to relaunch coder; session records no per-role configuration; it was launched before agentctl recorded @agentctl_fleet and @agentctl_dir; supply --harness [--model] [--effort] --dir
```

The working directory is never defaulted to wherever you happen to be standing: a role silently relaunched outside the
fleet's directory would join a different AMQ session.

### `attach`

```text
agentctl attach [--session SESSION]
```

Starts tmux control-mode attachment for the resolved managed session. Run it from iTerm2 (`TERM_PROGRAM=iTerm.app`)
and outside tmux. It validates the current agentctl management and version markers, attaches by the resolved session
ID, and never creates a session. This is a human operator command, not a planner operation.

Once the ownership gate passes, `attach` reads the session's window count once and narrates what iTerm2 is about to do
immediately before control mode starts. For session `epic123` with three windows:

```text
agentctl 0.2.0
Attaching session "epic123" (3 windows) in iTerm2.

iTerm2 will now show its Command Menu. That menu is iTerm2's, not agentctl's:

  esc   detach cleanly — the tabs close and the fleet keeps running
  X     (uppercase) force-quit — the fleet keeps running, but the tmux client
        does not exit, so this terminal stays busy and agentctl cannot report.
        Prefer esc.

Detaching never stops the fleet. To stop it: agentctl kill --session epic123
```

The opening version is the same build identity reported by `agentctl version`. It appears on `attach` only. If the
window-count read fails, only the second line changes to `Attaching session "epic123" in iTerm2.` and the attach
continues; agentctl never guesses a count. The `Command Menu` that follows (`esc`, `X`, `L`, `C`) belongs to iTerm2's
tmux integration. agentctl neither prints it nor controls its keys, so their case sensitivity is iTerm2's behaviour,
not agentctl's: press uppercase `X`.

If `X` wedges the `tmux -CC` client, killing the session or sending that client `SIGTERM` does not release the
terminal. Terminate the client with `kill -9 PID`; agentctl then reports `agentctl: tmux attach session: signal: killed`
and exits. See [design spec §3.4 (PR #107)](https://github.com/mnbf9rca/agentctl/pull/107) for the dated verified contract.

When control mode ends, `attach` lists sessions once more and reports the state it observed, by session ID, so a
session recreated under the same name is not reported as the one you attached. Exit code 0 in all three cases — the
attachment completed; only the state report and available commands differ:

```text
Attachment to session "epic123" ended (tmux exit 0). Session $4 is still running.

  re-attach:     agentctl attach --session epic123
  check status:  agentctl status --session epic123
  stop it:       agentctl kill --session epic123
```

When the state is not verifiable, only the action that does not assume presence is shown:

```text
Attachment to session "epic123" ended (tmux exit 0). Could not verify whether session $4 is still running: CAUSE

  check status:  agentctl status --session epic123
```

When the resolved session ID is absent, the report has no command block:

```text
Attachment to session "epic123" ended (tmux exit 0). Session $4 is no longer present.
```

The report says `Attachment ... ended`, not `Detached`, because control mode ending does not establish how it ended.
The observed state appears once; any following block contains commands only. No next-steps block follows the `no
longer present` state because suggesting an action would imply the session exists. That final probe is advisory: it
never changes the exit code, and a probe failure is reported as an unverified state rather than as an absence. If the
killed session was the last one, tmux takes its server down too, so the probe reports the unverified form with tmux's
own reason instead of the `no longer present` form.

An ownership-gate refusal gives the direct-tmux escape hatch. For session `epic123`, the exact forms are:

```text
agentctl: refusing to attach; session "epic123" is not managed by agentctl; to attach anyway, run: tmux -CC attach-session -t '=epic123'
agentctl: refusing to attach; managed session carries no @agentctl_version marker; to attach anyway, run: tmux -CC attach-session -t '=epic123'
agentctl: refusing to attach; session "epic123" has @agentctl_version="2"; expected "1"; to attach anyway, run: tmux -CC attach-session -t '=epic123'
```

The escape hatch deliberately bypasses agentctl's ownership gate; using it is the operator's decision. A missing or
ambiguous session, a non-iTerm2 environment, an invocation already inside tmux, or failure to establish control mode
is reported instead and does not create a new session.

### `status`

```text
agentctl status [--session SESSION] [--json]
```

Reports objective tmux, metadata, and root-process facts. The default is a human-readable table; `--json` emits the
versioned machine-readable report. The table renders `default` in the `MODEL` and `EFFORT` columns for a role launched
without one; metadata and JSON carry the empty string for the same role.

Without an explicit `--session`, `status` always reports every tmux session. `AGENTCTL_SESSION` and the current tmux
session neither narrow nor fail the listing. Only `--session SESSION` requests a single-session report:

```bash
agentctl status
agentctl status --json
agentctl status --session epic123
```

The listing table has a leading column with an empty header. When tmux can determine the caller's session from
`TMUX_PANE`, every row for that session carries `*`; the marker never comes from `AGENTCTL_SESSION`. Outside tmux, or
when that advisory read fails, the column remains blank with no warning. A missing marker means only that agentctl did
not determine the caller's session.

The table renders one row per role across all listed sessions. An unmanaged or otherwise agentless session still gets
one row naming the session and its state. The JSON document wraps the same per-session reports, adding `current: true`
only to the marked session and omitting it elsewhere. Unmanaged sessions use `managed: false` with an empty agent list:

```json
{"schema": 1, "sessions": [{"schema": 1, "session": "shell", "managed": false, "agents": []}]}
```

A session that claims agentctl management but carries metadata agentctl cannot interpret — a foreign
`@agentctl_version`, an absent version marker, or a malformed role roster — is rendered in place with the defect named.
The JSON session report includes a `defect` field, so its empty agent list is not presented as an observed absence. The
listing continues so the remaining topology stays visible, and the command still exits 3.

If no tmux server is running, the listing exits 6 and carries tmux's own message. It does not infer an empty server by
matching stderr text.

### `clear`

```text
agentctl clear [--session SESSION] ROLE
```

Validates the managed session, role window, sole live pane, recorded process identity, and self-target guard before
delivering the fixed `/clear` payload. No caller-supplied payload is accepted.

### `compact`

```text
agentctl compact [--session SESSION] ROLE
```

Runs the same fail-closed target checks as `clear`, then delivers the fixed `/compact` payload.

### `kill`

```text
agentctl kill [--session SESSION]
```

Terminates the resolved tmux session only when its agentctl management and version markers are valid. It refuses
unmanaged sessions.

### Session selection

`launch` always requires `--session`. Bare `status` lists every session and consults no ambient source for selection;
only an explicit `status --session SESSION` narrows its report. `relaunch`, `clear`, `compact`, `kill`, and `attach` resolve the
session in this order:

1. an explicit `--session SESSION`;
2. a nonempty `AGENTCTL_SESSION` environment variable;
3. the current tmux session, when `TMUX_PANE` identifies the caller.

An explicit empty `--session=` is rejected. For the acting commands, any invalid nonempty explicit or environment value
is also rejected rather than silently falling through; an empty `AGENTCTL_SESSION` is treated as absent. When no source
names a session, `relaunch`, `clear`, `compact`, and `kill` fail because they act on exactly one target. `attach` accepts the
explicit and environment sources, but it must run outside tmux, so the current-tmux fallback is not available to that
command.

## Understanding status

Status is roster-driven: it reports the roles recorded when the fleet launched, including a role whose window has
since disappeared. State precedence is fail-closed, so the first applicable state below wins.

| State | Meaning |
| --- | --- |
| `ambiguous` | More than one window has the role's exact name; each match is shown and no single target is assumed. |
| `unmanaged` | Window metadata does not describe the expected managed role, or the window has more than one pane. |
| `missing` | No exact role window exists, or the matching window has no pane. |
| `dead` | A surviving pane explicitly reports that it is dead. |
| `no-baseline` | The live managed pane has no recorded launch baseline, so its process identity was never proved. |
| `unexpected-process` | The live pane's observed root executable does not match its launch baseline, or identity cannot be verified. |
| `running` | The managed window, sole live pane, and recorded process identity all match. |

Exited agents normally report `missing`, not `dead`. Managed windows do not use tmux `remain-on-exit`, so a window
normally disappears when its agent exits. `dead` is reserved for the distinct case where a pane still exists and tmux
reports it dead. A `missing` role is the one `relaunch` can bring back; every other state means the window is still
there. The one bounded exception is `no-baseline`, which relaunch can replace when another session window survives;
the sole-window case is refused with a whole-fleet recreation remedy.

Status does not claim that a `running` agent is idle, healthy at the application level, or following the intended
workflow. It reports only the objective state agentctl can verify without scraping agent output.

## Security and operating model

agentctl is a single-user accident-prevention tool, not a security boundary against other processes running as the
same user. It validates identifiers, hardcodes its control payloads, addresses tmux objects by resolved IDs, checks
management metadata and launch-time process identity, and refuses to target its own pane when invoked from inside
tmux. Recorded metadata is re-validated when it is read back: `relaunch` applies the same harness, model, and effort rules to
`@agentctl_fleet` that `launch` applies to `--roles`, `--models`, and `--efforts`. Relaunch rollback removes only the window the
same invocation created; recovery removes only the exact typed ID of a uniquely classified managed `no-baseline`
window, after every non-destructive check has passed.

`launch --from-template` is a caller-named read path, not a write path. agentctl opens the path, verifies the opened
descriptor is a regular file, bounds the read, and strictly decodes it before the effective fleet can reach preflight
or tmux. Symlinks are followed to their target; `-` is an ordinary filename and stdin is never read. Template values
gain no trust from their source and follow the same validated harness-argv and quoting path as flag values.

These checks reduce wrong-target accidents but cannot make terminal input transactional. Under deliberate CPU
saturation, verification observed delayed, missing, and doubled input. No wrong command selection was observed, but a
future truncated payload could still select another harness command. Therefore:

- do not issue controls while the fleet is saturating the host;
- treat a successful `clear` or `compact` as delivery, not confirmed execution;
- check `status` and the named role before sending a control;
- investigate `ambiguous`, `unmanaged`, and `unexpected-process` rather than bypassing them with direct tmux input.

Read [SECURITY.md](SECURITY.md) for the threat model, accepted residuals, and measured evidence.

## Agent identity in managed windows

Every window `agentctl launch` or `agentctl relaunch` creates is given three environment variables, passed to tmux as separate `-e NAME=value`
arguments when the window is created:

| Variable | Value |
| --- | --- |
| `AGENTCTL_SESSION` | the validated session name the window belongs to |
| `AGENTCTL_ROLE` | the validated role name of that window |
| `AGENTCTL_MANAGED` | always `1` |

They exist so that whoever is in the pane — an agent or a human — can read its own identity:

```text
printenv AGENTCTL_SESSION AGENTCTL_ROLE AGENTCTL_MANAGED
```

**Guidance for agents: check `AGENTCTL_*`, `AM_*`, and `TMUX` before reasoning about fleet topology** — the fleet you
are looking at may be the one you are running inside, and killing or reusing it would end your own session.

These variables are advisory. `launch` clears all three copies from the tmux session environment immediately after
the first window is fully stamped, while each managed window retains the values passed directly in its creation argv.
This prevents a window created later by hand from inheriting the first role's identity. A clear failure is reported but
does not roll back a working fleet. Existing sessions are never retrofitted. Any same-user
process can export the same names, so their presence is a hint, not proof. agentctl itself never reads them back when
deciding what to control, kill, or report on: that decision rests on the `@agentctl_*` tmux options and the fail-closed
target chain described in [SECURITY.md](SECURITY.md). The one place `AGENTCTL_SESSION` does matter to agentctl is
session selection for acting commands: `relaunch`, `clear`, `compact`, and `kill` default to that pane's own fleet,
while `attach` can use it only when invoked outside tmux. Bare `status` deliberately ignores ambient selection and
lists every session; only an explicit `status --session` narrows it. Every selected target is still validated the same way.

## Troubleshooting tmux environment staleness

A tmux server keeps an environment that may be older than the shell running agentctl. Credentials or configuration
exported after the server started might therefore be absent from newly launched agent windows even though they are
visible in the current shell.

If an agent starts without an expected variable, inspect the tmux server environment and update it deliberately with
tmux's environment commands, or restart the tmux server after preserving any sessions you still need. Resolve the
environment before launching a replacement fleet; agentctl does not infer or synchronize credentials.

## Maintainers and release verification

- The approved [design specification](docs/superpowers/specs/2026-08-01-agentctl-design.md) is the source of contract
  detail; [the implementation brief](docs/brief.md) records the original requirements.
- [SECURITY.md](SECURITY.md) defines the security posture and accepted residuals.
- [docs/release-checklist.md](docs/release-checklist.md) records observed tool versions and the manual tmux/harness
  verification required for relevant releases. In particular, it explains the evidence behind the retained
  keystroke-settle constant.
- `docs/superpowers/plans/` contains working plans only; plans are never normative.
