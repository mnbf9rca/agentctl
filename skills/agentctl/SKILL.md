---
name: agentctl
description: Use when operating an agentctl fleet from inside it — checking sibling agent status, clearing or compacting a role's context, or terminating a managed session. Read this before issuing any agentctl command.
compatibility: Requires the agentctl binary on PATH, run from inside an agentctl-managed tmux window.
metadata:
  version: "0.4.0"
---

# Driving agentctl

agentctl launches and controls tmux fleets of coding agents. You are one of
those agents. This skill covers the commands you may use and the rules the
binary cannot enforce for you.

## 1. Verify who and where you are first

Every agentctl-launched window carries identity in its environment:

- `AGENTCTL_ROLE` — your role name; `AGENTCTL_SESSION` — your fleet's
  session; `AGENTCTL_MANAGED=1` — this window is fleet-managed.
- `AM_ME` / `AM_SESSION` — your AMQ identity, set by `amq coop exec`.
- `TMUX_PANE` — your own pane ID; agentctl refuses a control command that
  resolves to it (an accident guard, not a permission boundary).

Check these before your first command. If `AM_ME != AGENTCTL_ROLE` or
`AM_SESSION != AGENTCTL_SESSION`, your environment is not what you assume:
stop and report instead of targeting anything. These variables are advisory
identity fixed when your process started — treat them as facts about launch,
not proof of the present.

Session resolution order is `--session` > `AGENTCTL_SESSION` > the tmux
session you are in. So a bare command acts on **your own fleet**; name any
other fleet explicitly with `--session`. Exception: bare `status` always
lists every session (a `*` marks yours) — it never narrows to the ambient
session.

## 2. Commands you may use

```
agentctl status --json
agentctl status --session SESSION --json
agentctl clear --session SESSION ROLE
agentctl compact --session SESSION ROLE
agentctl kill --session SESSION
```

`launch`, `relaunch`, and `attach` are operator-only; do not issue them.
`launch --from-template FILE` lets the operator supply fleet shape from a
strict JSON file; launch remains operator-only.

### 2.1 Author a launch template when the operator asks

You may author or review a launch template for an operator. Before doing so,
read [references/fleet-template.schema.json](references/fleet-template.schema.json):
it is the complete template-shape contract. Schema-valid does not mean
launch-valid; value rules apply to the merged template-and-flag union at launch.
Do not issue the operator's `launch` command.

## 3. Read status as factual claims

Status is roster-driven: roles come from fleet metadata, not from whatever
windows exist. The states `ambiguous`, `unmanaged`, `missing`, `dead`,
`no-baseline`, `unexpected-process`, `running` are distinct claims with
distinct meanings — see
[references/status-states.md](references/status-states.md). Never infer
liveness from anything else (pane text, AMQ traffic, silence). An exited
agent normally reports `missing`, not `dead`, because managed windows close
on exit. `no-baseline` means the stored process baseline is empty, so agentctl
never proved the pane's launch identity; control commands fail closed and no
current-process probe is issued. Operator-only `relaunch` can recover it only
when the window carries launch's positive `@agentctl_unproven=1` abandonment
record and the session has at least one other listed window. An unmarked
`no-baseline` window is refused because it may still be settling. A
sole-window case is also refused and requires the operator's managed `kill`
plus `launch` remedy.

## 4. Rules the binary does not enforce

- **Never reset a role that is still working on a task.** A mid-task `clear`
  or `compact` destroys its working context and work in progress. Reset only
  when the current task is finished — its result has been delivered or
  control explicitly handed back — or when you deliberately abandon that
  work.
- **Do not issue control commands while the fleet is saturating the host.**
  agentctl cannot detect saturation without inferring machine state, which
  its design forbids; the obligation is yours.
- **Delivery is not execution.** Exit 0 proves tmux accepted the keys, not
  that the agent's TUI ran the command. Use an AMQ message ping and `status`
  only for the facts they can establish. To verify the reset itself, take the
  pane ID from `status --json` and inspect the screen read-only:

  ```tmux
  tmux capture-pane -p -t PANE
  ```

  Confirm the captured screen shows that the TUI actually reset.
  `capture-pane` is observation only. Never write to a sibling with raw tmux;
  doing so bypasses every guard agentctl provides.
- **The self-target guard is an accident guard.** It stops you wiping your
  own context by mistake; it is not a security boundary.

## 5. Context hygiene for clear and compact

- `clear` after a finished task when the next task is unrelated, or when you
  deliberately abandon work by a wedged or confused role.
- `compact` after a finished task when the next task continues the same
  subject and the prior context remains useful.
- Never send either while the role is still working or while the fleet
  saturates the host. Confirm the reset as described above before assigning
  the next task.
- Keep every new task description self-contained so clearing prior context
  does not discard information the role still needs.

## 6. What agentctl deliberately cannot do

No arbitrary keystrokes or free-text payloads (the payload registry is
closed and argument-free), no reading or writing AMQ state, no attaching for
you, no agent-initiated per-window restart, no machine-state inference.
`relaunch` is operator-only because role recovery is a fleet-level decision;
report a missing sibling to the operator instead of trying to repair it.

## 7. Branch on exit codes, not prose

Exit codes are contracts — see
[references/exit-codes.md](references/exit-codes.md). Prose output may
change; the codes may not.
