---
name: agentctl
description: Use when operating an agentctl fleet — checking runtime status, clearing or compacting a role's context, or terminating a managed session. Read this before issuing any agentctl command.
compatibility: Requires the agentctl binary on PATH. Runtime status, control, and detached-role attach work without tmux.
metadata:
  version: "0.5.1"
---

# Driving agentctl

agentctl launches and controls fleets of coding agents. Each role is owned by
a resident PTY shim; tmux is optional presentation, not role identity or the
delivery path. Joining, breaking, moving, swapping, or renaming panes does not
change runtime ownership or control eligibility.

## 1. Verify who and where you are first

An agentctl-launched role normally carries advisory launch identity:

- `AGENTCTL_ROLE` — your role; `AGENTCTL_SESSION` — your fleet;
  `AGENTCTL_MANAGED=1` — agentctl launched this process.
- `AM_ME` / `AM_SESSION` — your AMQ identity, set by `amq coop exec`.
- `TMUX_PANE` may identify a presentation pane, but is never role identity or
  a targeting input.

Check these before your first command. If `AM_ME != AGENTCTL_ROLE` or
`AM_SESSION != AGENTCTL_SESSION`, stop and report the mismatch. These values
describe launch, not current proof. For control, agentctl independently checks
the connected shim and walks process ancestry; it refuses observed self-target
and undetermined ancestry separately.

Single-target session resolution is `--session` > `AGENTCTL_SESSION` > the
displayed tmux session when it can be observed unambiguously. Name a different
fleet explicitly. Bare `status` is different: it enumerates every durable
fleet and never narrows to the ambient session. A `*` marks the caller's
session only when agentctl can observe one.

## 2. Commands you may use

```text
agentctl status --json
agentctl status --session SESSION --json
agentctl clear --session SESSION ROLE
agentctl compact --session SESSION ROLE
agentctl kill --session SESSION
```

`launch`, `run`, `relaunch`, and `attach` are operator-only; do not issue them.
The exact foreground form is `agentctl run --session SESSION --role ROLE
--harness HARNESS [--model MODEL] [--effort LEVEL]`.

It runs one role in the foreground using the current working directory and
creates or extends the durable fleet. It creates or contacts no tmux server,
session, window, or pane. A different stored fleet directory is refused before
the role starts or durable state changes. Before cwd or runtime construction,
`run` requires both standard input and standard output to be terminals. Pipes
select exit 2; another terminal-observation failure selects exit 6. Neither
refusal creates the runtime root, state root, or `sessions/` entry. See
[references/exit-codes.md](references/exit-codes.md) for the exact rows. It
remains attached through child exit. `launch --from-template FILE` supplies
fleet shape from strict JSON. Passing neither `--detached` nor `--tmux` launches detached;
a template may select `presentation`, and an explicit flag overrides it.
Before starting roles, `launch` checks AMQ mailbox-directory presence for every declared role and
runs `amq init` if any are missing; init failure refuses the launch with AMQ's own error text.

### 2.1 Author a launch template when the operator asks

You may author or review a launch template. First read
[references/fleet-template.schema.json](references/fleet-template.schema.json),
the complete shape contract. Schema-valid is not necessarily launch-valid;
value rules apply to the merged template-and-flag union. Do not issue launch.

## 3. Read status as factual claims

Status enumerates volatile runtime claims first and durable records second,
with or without tmux. Its closed states have distinct meanings and precedence;
see [references/status-states.md](references/status-states.md). Never infer
liveness, identity, or execution from pane text, layout, AMQ traffic, or
silence.

`confidence: anchored` means a volatile lockfile anchored the runtime/durable
join. `unanchored` is a weaker durable-only observation, never anchored
absence. Presentation is separately `present`, `gone`, or `unavailable`; it
never changes a role's runtime state. A missing presentation does not disable
status or control.

## 4. Rules the binary does not enforce

- Never reset a role still working. `clear` or `compact` destroys its current
  context. Reset only after its result or handoff, or deliberately abandon it.
- Do not issue control commands while the fleet saturates the host. agentctl
  does not infer machine state.
- Delivery is not execution. Exit 0 for `clear` or `compact` proves the shim
  accepted the closed operation, wrote the registered bytes to its PTY, and
  observed submit; it does not prove the harness ran the command. Confirm via
  AMQ. A tmux pane, when present, may be inspected read-only, but never write
  to a sibling with raw tmux.
- The self-target check is an accident guard, not a security boundary.

## 5. Context hygiene for clear and compact

- `clear` after a finished task when the next task is unrelated, or when you
  deliberately abandon work by a wedged or confused role.
- `compact` after a finished task when the next task continues the subject.
- Never send either while the role is working or the host is saturated.
- Keep new task descriptions self-contained so clearing context loses nothing
  the role still needs.

## 6. Operator-only lifecycle facts

`relaunch` starts only a runtime-observed missing role or ESRCH-backed stale
record; every state that may retain a child refuses. `attach ROLE` streams a detached role directly, with one viewer and no output replay. Bare `attach` remains the fleet-level tmux presentation route; if no presentation is observed it lists the direct role commands and never creates a presentation.

Only the explicit `attach ROLE` terminal stream carries operator keystrokes;
control operations have no arbitrary free-text path. Beyond that launch-time
mailbox check/init, agentctl has no other AMQ-state access, no agent-initiated
per-role restart, and no machine-state inference. Report a missing sibling to
the operator rather than trying to repair it.

## 7. Branch on exit codes, not prose

Exit codes are contracts; see [references/exit-codes.md](references/exit-codes.md).
Typed prose names the observed outcome, but automation branches on the code.
