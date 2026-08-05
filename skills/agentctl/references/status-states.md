# status states

Evaluated in precedence order; first match wins (product spec §6.3).

| Order | State | The claim it makes | What it does not claim |
|---|---|---|---|
| 1 | `ambiguous` | More than one window bears this role's exact name. Control commands refuse the role (exit 4) until an operator repairs it with raw tmux. | Nothing about which window is "real". |
| 2 | `unmanaged` | The window no longer satisfies the managed contract: metadata mismatch or more than one pane. Not agentctl's to describe or control. | Not a statement that the agent is gone. |
| 3 | `missing` | The roster names this role but no exactly-matching window exists (or it has zero panes). The normal state of an **exited** agent. | Not proof it never ran. |
| 4 | `dead` | The window exists and its pane reports dead. Rare: managed windows close on exit. | — |
| 5 | `unexpected-process` | The pane's observed root executable differs from the launch baseline, the baseline is empty, or identity could not be verified for an alive pane. Identity unverifiable is not identity verified. The rendered process is the one **observed now**, not the expected one. | Not proof of compromise; often a wrapper or restart. |
| 6 | `running` | Window, pane, and process identity all match the launch baseline. | Not that the agent is responsive or idle. |

A session that is not agentctl-managed renders `"managed": false` with an
empty agent list, exit 0. A managed session with absent or malformed
metadata refuses with exit 3 rather than guessing.
