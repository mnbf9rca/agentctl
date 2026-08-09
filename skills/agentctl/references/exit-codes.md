# exit codes

| Code | Constant | Claim |
|---|---|---|
| 0 | `exitOK` | The command's stated effect was observed. For control commands: delivery, not execution. |
| 1 | `exitUnclassified` | Something failed that codes 2–9 do not describe. No contract semantics. |
| 2 | `exitUsage` | The invocation was invalid; nothing was attempted. |
| 3 | `exitSession` | The session could not be resolved, does not exist, is not managed, or carries an incompatible/malformed management marker. Also every `attach` refusal. |
| 4 | `exitRole` | The role is not in the roster, has no window, or resolves to more than one window. |
| 5 | `exitUnsafe` | A control target was refused as unsafe: invalid pane count, state, or death; unavailable, empty, or mismatched process identity; or the caller's own pane. |
| 6 | `exitTmux` | A tmux (or `ps`) command actually ran and failed; the message carries the tool's own stderr. |
| 7 | `exitMissingExecutable` | A required executable was not found on PATH. |
| 8 | `exitLaunch` | A post-ownership `launch` or `relaunch` failed; cleanup of the created session or window was attempted, and output states whether it succeeded. |
| 9 | `exitLaunchUnproven` | `launch` created every requested role window, but at least one role's process identity remained unproven after the bounded settle poll. The retained fleet is reported by `status`. |
