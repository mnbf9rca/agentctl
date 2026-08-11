# exit codes

| Code | Constant | Claim |
|---|---|---|
| 0 | `exitOK` | The command's stated effect was observed. For control, registered bytes were written and submit observed, not harness execution. A foreground child exit with status 0 is also success. |
| 1 | `exitUnclassified` | An unclassified failure, foreground nonzero child exit, or foreground signal termination. |
| 2 | `exitUsage` | Invalid invocation, name, root, flag, or request; no role was mutated. |
| 3 | `exitSession` | Missing/incompatible fleet configuration, protocol skew, launch collision, or attach without an observed tmux presentation. |
| 4 | `exitRole` | The role is outside the durable roster or is missing/stale when the operation requires it. |
| 5 | `exitUnsafe` | Runtime identity, ancestry, state, record, cleanup, or observation did not positively authorize the operation. |
| 6 | `exitTmux` | A required tmux presentation command actually ran and failed. |
| 7 | `exitMissingExecutable` | A required executable was not found on `PATH`. |
| 8 | `exitLaunch` | Launch/relaunch/run failed after ownership or readiness work; typed cleanup facts accompany it. |
| 9 | `exitLaunchUnproven` | Launch retained a fleet whose requested roles were created but not all observed ready. |

Every diagnostic is one stderr line beginning `agentctl: `. Typed prose is
factual evidence for people; automation must branch on the numeric code.
