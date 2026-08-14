# exit codes

| Code | Constant | Claim |
|---|---|---|
| 0 | `exitOK` | The command's stated effect was observed. For control, registered bytes were written and submit observed, not harness execution. A foreground child exit with status 0 is also success. |
| 1 | `exitUnclassified` | An unclassified failure, foreground nonzero child exit, or foreground signal termination. |
| 2 | `exitUsage` | Invalid invocation, name, root, flag, or request, or foreground `run` without terminal stdin/stdout; no role was mutated. |
| 3 | `exitSession` | Missing/incompatible fleet configuration, protocol skew, launch collision, or attach without an observed tmux presentation. |
| 4 | `exitRole` | The role is outside the durable roster or is missing/stale when the operation requires it. |
| 5 | `exitUnsafe` | Runtime identity, ancestry, state, record, cleanup, or observation did not positively authorize the operation. |
| 6 | `exitTmux` | An ancestry, process/token, foreground-terminal, protocol frame/schema, or required tmux presentation observation failed without a safe positive fact. |
| 7 | `exitMissingExecutable` | A required executable was not found on `PATH`. |
| 8 | `exitLaunch` | Launch/relaunch/run failed after ownership or readiness work; typed cleanup facts accompany it. |
| 9 | `exitLaunchUnproven` | Readiness, ownership, stop, or durable-commit evidence was retained because absence or cleanup was not proven. |

Foreground terminal setup has these exact typed rows:

- `run-not-a-terminal` (exit 2): `agentctl: refusing to run role "ROLE" in session "SESSION"; standard input and output must both be terminals (run-not-a-terminal)`
- `run-terminal-observation-failed` (exit 6): `agentctl: could not observe the foreground terminal for role "ROLE" in session "SESSION": "CAUSE"; no role was started (run-terminal-observation-failed)`

Both rows are selected before cwd observation or runtime construction and
neither creates or mutates the runtime root, state root, or `sessions/` entry.

Every typed outcome diagnostic is one stderr line beginning `agentctl: `.
Usage errors may append multiline command help. Prose is factual evidence for
people; automation must branch on the numeric code.
