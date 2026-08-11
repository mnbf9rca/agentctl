# status states

Evaluated in precedence order; first match wins. Presentation is reported
separately and never changes one of these runtime claims.

| Order | State | The claim it makes | What it does not claim |
|---|---|---|---|
| 1 | `invalid-record` | Required volatile or durable data is malformed. | Nothing about liveness. |
| 2 | `state-root-disagreement` | Local and lockfile-recorded state roots differ. | Which root is authoritative. |
| 3 | `protocol-skew` | The answerer protocol version is absent or different. | Child absence. |
| 4 | `answerer-disagreement` | Shim PID or socket/claim topology conflicts. | Which claimant is legitimate. |
| 5 | `cleanup-failed` | Prior owned cleanup is durably incomplete. | That cleanup may be retried safely. |
| 6 | `concurrent-contender` | An owner reported a competing-owner decision. | A separate durable contender row. |
| 7 | `starting` | A live claimed shim has not observed child readiness. | That startup will complete. |
| 7a | `stopping` | Serialized stop began; no stop response yet. | Child absence. |
| 7b | `stopped` | Child exit was observed; teardown is pending. | Cleanup completion. |
| 8 | `indeterminate-child-starting` | The shim died while the durable record says `child-starting`. | Child absence. |
| 9 | `running` | Shim, answerer, child identity, and readiness match. | Responsiveness or idleness. |
| 10 | `orphan` | The dead shim's recorded child is present with matching token. | That relaunch is safe. |
| 11 | `present-token-disagreement` | The PID is present but its start token differs. | Recorded-child absence. |
| 12 | `present-not-ours` | `kill(pid, 0)` returned `EPERM`. | Absence or ownership. |
| 13 | `could-not-observe` | Presence or token observation failed. | Any positive liveness fact. |
| 14 | `stale-record` | The durable child PID returned `ESRCH`. | Why the child disappeared. |
| 15 | `missing` | No role record exists at applicable confidence. | That a role never ran. |

`anchored` confidence means a volatile lockfile anchored the durable join;
`unanchored` is durable-only. Tmux presentation is independently `present`,
`gone`, or `unavailable`. Only `stale-record` or `missing` can authorize
relaunch; every disagreement or uncertainty refuses.
