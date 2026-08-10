# Security Policy

## Security model

`agentctl` is a personal, single-user, on-machine fleet launcher. The shipped pre-0.5 path creates tmux sessions
running AI coding agents via `amq coop exec` and delivers fixed control keystrokes. The approved 0.5.0 cutover instead
uses per-role resident shims, local operation-name sockets, durable child records, shim-owned nested PTYs, optional tmux
presentation, and foreground no-tmux roles (design §15). Those paths are design invariants until their atomic cutover
PR marks them shipped. Like [AMQ](https://github.com/avivsinai/agent-message-queue), agentctl prioritizes **correctness
and accident-prevention over defense against a local attacker with code-execution parity**: anyone who can run
processes as your user already owns your local sockets, shell, terminal processes, and agents.

### Threat model — what agentctl defends against

- **Command injection through its own inputs.** Session/role names match `^[a-z0-9][a-z0-9_-]*$`, model and effort identifiers `^[a-zA-Z0-9][a-zA-Z0-9._-]*$`, so only ASCII letters, digits, dots, underscores and hyphens can reach the codex `--config 'model_reasoning_effort="…"'` expression (whose value codex parses as TOML). The one shell-interpreted string (the window command) is assembled by the fuzz-tested `shellq` layer from already-validated tokens; agentctl never invokes a shell — all tmux calls are argv arrays.
- **Flag smuggling into agent processes.** The mandatory alphanumeric first character makes flags like `--dangerously-bypass-approvals-and-sandbox` unrepresentable in either the model or effort slot; their shared charset cannot carry a leading flag, a quote, a backslash, whitespace, a newline, `=`, or `:`.
- **Arbitrary keystroke injection.** The control surface is a hardcoded registry (`clear → /clear`, `compact → /compact` in v1). Payloads are complete constants; commands carrying caller text (e.g. `/rename NAME`) are permanently inadmissible. No option, environment variable, or stdin path lets caller text reach `tmux send-keys`. Payloads go in literal mode, Enter as a separate event.
- **Controlling the wrong terminal.** Control commands run the fail-closed chain of spec §§6.2/13: charset → exact session resolution and managed/version gate → exact window resolution (>1 match refused) → stored `@agentctl_role` equality → exactly one live pane → process identity against the launch baseline → self-target check, with every step addressing tmux IDs, never names. `@agentctl_role` is written only during window stamping, never session stamping, so a handmade same-name window cannot inherit ownership evidence — pinned by `TestStampSessionNeverWritesWindowOwnershipMarker` and, against real tmux, `TestIntegrationHandmadeRosterWindowIsNeverControlledOrReplaced`.
- **Accidental self-lobotomy.** From inside tmux, a control command targeting the caller's own pane (`$TMUX_PANE`) is refused. An accident guard, not a boundary: the variable can be unset, and nothing stops an agent typing `/clear` into its own TUI.
- **Destroying sessions and windows agentctl does not own.** Launch rollback kills only the session its own invocation created. `relaunch` issues `kill-window` in exactly two cases: rolling back the window ID it just created and parsed, and the bounded `no-baseline` recovery of residual 2 against a window it classified by listing in the same invocation carrying `@agentctl_unproven=1` — never against an unclassified window, an ambiguous role, or the session's only window; the wrapper takes a typed window ID so no name reaches `-t`. `kill`, attach, and control refuse unmanaged sessions.
- **Repairing the wrong window.** `relaunch` proceeds only when a role matches exactly zero windows, or in the bounded recovery of one baseline-less window that carries the positive `@agentctl_unproven=1` abandonment record (required because a mid-poll window is indistinguishable from an abandoned one) in a session with at least one other window. Every other state — running, dead, unmanaged, pane-less, ambiguous, or a *recorded* baseline that mismatches the live process — is refused with the observed state reported, preserving evidence and avoiding the heal-on-verify residual 2 rejects. Concurrent contenders re-list after creation, succeed only as the sole match, and roll back only their own window ID; a failed rollback is reported, never hidden. Mechanics: spec §6.8.
- **Silent fleet divergence through repair.** Launch records per-role config in `@agentctl_fleet` (validated quads) and `@agentctl_dir` (exact absolute `-c` string). `relaunch` re-validates both on read (harnesses and the shared model/effort charset): an unknown harness, a model or effort that could smuggle a flag, or a relative stored directory is refused rather than executed (explicit `--dir` is the escape hatch). Overrides are reported with provenance and written back so the record matches the live fleet; concurrent overriding relaunches can lose one override from the record (last writer wins). Sessions predating these options are refused unless config is supplied explicitly; the working directory is never defaulted.

### Out of scope

- **Same-user malicious processes** — including the fleet's own agents (a prompt-injected agent could run `agentctl clear` against a sibling), and same-user socket or lockfile access. agentctl does not defend a boundary the OS does not provide.
- **Multi-user scenarios.** No cross-account hardening; the tmux socket's `0700` permissions are the boundary.
- **Filesystem namespace manipulation** (bind mounts, device files, privileged tricks) — require system-level hardening, as in AMQ's policy.
- **The agents themselves.** What an agent does after `/clear`/`/compact` is the harness's permission model, not agentctl's.

## Third-party build dependencies

agentctl is standard-library-first, but its build graph deliberately includes
`github.com/santhosh-tekuri/jsonschema/v6` to compile and validate the embedded
launch-template schema. Its indirect `golang.org/x/text` dependency is recorded
in `go.mod`; `go.sum` records the module checksums. The compiler replaces a
larger bespoke structural validator, so the dependency reduces security-
critical complexity rather than adding a general extension mechanism.

This changes the supply-chain boundary: a compromised dependency or checksum
verification bypass could affect a built binary. Review every dependency change
with its version, `go mod graph`, and `go.sum`; do not add dependencies merely
for convenience. CI runs the pinned govulncheck scanner on every pull request
and daily on `main`, covering dependencies introduced by a change and advisories
published after merge. Dependabot opens grouped monthly version-update pull
requests for Go modules and GitHub Actions, plus advisory-triggered security-
update pull requests; both pass the required CI and govulncheck checks.
Dependencies are compiled into release artifacts and are not fetched or selected
from template input at runtime. Template schemas remain an embedded, release-
reviewed asset, so a template cannot choose code, a schema location, or a
validator.

Release archives reproduce the upstream licenses for
`github.com/santhosh-tekuri/jsonschema/v6` and `golang.org/x/sys`, plus the
upstream license and patent grant for `golang.org/x/text`, under paths that name
each module unambiguously. CI inspects every snapshot archive and refuses one
missing any of those materials.

The internal shim identity primitives pin `golang.org/x/sys` v0.47.0 and import
`golang.org/x/sys/unix` only for Darwin `flock`, `LOCAL_PEERPID`, `kill(pid,0)`,
and raw `kinfo_proc` process observation. The module checksum and upstream
license are tracked and the existing dependency/vulnerability gates cover the
importer. The separate stdlib PTY lane does not import `x/sys`.

## Known risks and accepted residuals

1. **Keystroke delivery is not transactional, and degrades under host saturation.** `tmux send-keys` succeeding proves tmux accepted the keys, not that the TUI executed the command; a payload can land on a modal dialog or mid-render. Both harnesses open an autocomplete popup on a slash command and Enter selects the **highlighted** entry; in the 2026-08-01 evidence, the full payload highlighted the exact match. Evidence is split and version-pinned: fixed-injection behavior (junk cleared, payload executed, conversation reset) re-verified 2026-08-05 on Claude Code 2.1.222 / codex-cli 0.146.0 (`docs/release-verification-notes.md`, verify-live at `bb68e3b`); the popup-highlight observation and all saturation measurements are from 2026-08-01 on 2.1.220 / 0.146.0 — codex unchanged across the legs, Claude advanced two patches, so this is not a blanket re-verification; the `--measure` suite re-runs only if `internal/tmuxx.payloadDelay` changes. Measured under saturation (18 busy workers, codex, 1000ms delay, 20 loaded trials): **demonstrated** — input corruption (payload delay, loss-to-empty, blank redraws, one doubled `/clearclear` that matched nothing); **not observed** — ambiguous truncation or wrong-highlight selection, zero of each; **every observed failure left the payload unexecuted** — the fail-safe direction. The composite risk (a truncation forming a prefix the popup resolves to a different command, executing it silently) is therefore *possible but unobserved*, and the popup ranks the harness's whole palette, so the collision space cannot be enumerated or bounded by agentctl. Timing floors: Claude 750ms under load; codex **no floor established** at any tested candidate — the 1s constant is retained as best-evidenced, not proven sufficient; both are adversarial-ceiling figures. Saturation degradation is per role: a launch-time baseline-poll timeout leaves that role unproven and inert (residual 2), peers keep running, `launch` exits 9. **Responsibility:** the orchestrator — which both causes the load and chooses the timing, where agentctl is forbidden the machine-state inference (§6.3) — must not issue control commands while its fleet saturates the host; a human operator inherits the same rule.
2. **Process identity is observational, not cryptographic.** The check compares the pane root process's executable against a launch-time baseline (needed because Claude Code's process name is its version string, e.g. `2.1.220`). Capture assumes `amq coop exec` execs directly into the harness and accepts two consecutive identical non-`amq` observations 100ms apart — reducing, not eliminating, the chance of baselining a persistent transient. A same-user process can forge the metadata or rename an executable. Verification never heals or replaces the baseline — healing would let a persistent intruder become accepted instead of remaining detected. It is an accident guard, not authentication.

   On poll timeout, `launch` records **no** baseline, leaves the window in place, and exits 9. The role is inert: an unset baseline can never satisfy the exact-equality gate, so control refuses (exit 5) and `status` reports `no-baseline` — deliberately distinct from `unexpected-process`, because "never proved" and "proved, then changed" call for different responses. `relaunch` may destroy and replace such a window only when it also carries `@agentctl_unproven=1`, the record stamped on abandonment — recovery acts on a positive record of a completed decision, not on an absence, so a concurrent recovery cannot kill a window mid-launch. The marker is advisory (residual 4): forging it gets a window killed, which the forger could do directly — no added capability. The session's only window is never recovered (that would destroy the session; the remedy is `kill` + `launch`). This is **not** heal-on-verify: no recorded baseline is ever replaced, and a recorded baseline that mismatches the live process stays refused — the pane is evidence of an unexplained event.
3. **Environment staleness.** tmux windows inherit the server's environment, which may predate the current shell; later-exported credentials may not reach agents. Documented, not solved.
4. **Metadata is advisory.** `@agentctl_*` options are readable and writable by any same-user process; they gate agentctl's own actions but are not tamper-proof. `relaunch` re-validates everything it reads back (residual "Silent fleet divergence" above), closing the accident and trivially-tampered cases without claiming a trust boundary a same-user process could not already bypass directly.
5. **Launched-window identity variables are informational.** `AGENTCTL_SESSION`, `AGENTCTL_ROLE`, `AGENTCTL_MANAGED=1` are passed as separate `-e NAME=value` argv elements from validated values; session-environment copies are removed after the first window is stamped (failure reported, not fatal; the managed windows themselves are unaffected). Any same-user process can export the same names, so agentctl never reads them back when validating a control, `kill`, or `status` target — validation rests on `@agentctl_*` and the fail-closed chain. `AGENTCTL_SESSION` remains a session-*selection* source only (spec §4.1); every subsequent check is unchanged.
6. **The shipped pre-0.5 lifecycle is unsynchronized.** No locking exists on that current path; gates are
   check-then-act, with observed outcomes bounded. The ratified shim claim below replaces this row only at the atomic
   0.5.0 cutover:

   | Race | Current outcome |
   | --- | --- |
   | Two `launch` invocations for one session name | Fail-closed. tmux refuses the duplicate atomically; the winner remains intact and fully stamped, while the loser damages nothing, leaves nothing behind, and exits 3 whether the pre-check or `new-session` observes the duplicate. |
   | `launch` mid-stamp vs a concurrent read | Fail-closed. `show-options -qv` returns empty for an unset option, so a half-stamped session reads as unmanaged and every gate refuses it. |
   | `clear`/`compact` vs `kill` | Fail-safe. tmux does not reuse pane IDs, so a stale pane ID cannot address a different pane and `send-keys` to a gone pane fails. |
   | Two `relaunch` invocations for one absent role | Simultaneous contenders may both observe the post-create conflict, roll back only the window each created, and exit 8. The role is then absent, not ambiguous, and a later relaunch can recover it. |
   | `relaunch` recovery vs a still-settling window | Closed by evidence rather than timing. A window mid-poll is fully stamped with an empty baseline, so metadata alone cannot distinguish it from an abandoned one; recovery therefore additionally requires `@agentctl_unproven=1`, which only a completed abandonment stamps. Without that requirement a concurrent recovery could kill a window during its launch — reporting it "left in place" when it was not, or, if the kill landed between the stable pair and the baseline stamp, turning a timeout into an ordinary stamping failure and rolling back the entire session. |
   | `relaunch` recovery vs a peer window closing | Bounded and reported, not prevented. The sole-window refusal (design §6.8 step 4) counts the session's windows before the recovery kill, but a peer window closing in the interval — an ordinary agent exit suffices, since managed windows carry no `remain-on-exit` — can leave the target as the last window. The kill then destroys the session and the replacement cannot be created; both the removal and the creation failure are reported. tmux has no conditional kill-unless-last, so the interval can be narrowed but not eliminated. |
   | Two `relaunch` invocations recovering one unproven role | Bounded. Both classify the same window ID; one kills and recreates, the other's kill fails against a window that no longer exists and it exits 6 having created nothing. Window IDs are a monotonic per-server counter and are not reused within a server lifetime (probed on tmux 3.7b; a restarted server resets the counter, as pane IDs already do), so a stale recorded ID can never name the winner's new window. |
   | Concurrent overriding `relaunch` invocations for different roles | Last writer wins on the non-atomic `@agentctl_fleet` read-modify-write, so one live override can be lost from the record. |
   | Locking | None. |

   Recovery classification reads advisory metadata then kills by the classified ID; a same-user process can arrange a window's destruction that way, but could destroy it directly — no added capability. Delivery has one bounded TOCTOU: identity reads the pane PID and runs `ps`, then delivery targets the pane ID; pane IDs are not reused, but `respawn-pane -k` can swap the process in the same pane between the two. Same-user action; accepted.
7. **`launch --from-template` reads a caller-named path** — the first caller-supplied read path on a command that drives tmux. The file is opened `O_RDONLY|O_NONBLOCK` and the **descriptor** is verified — never stat-then-read, so no check/use gap. Only regular files are accepted: directories, devices, and sockets are refused, and a FIFO is refused because under the non-blocking open it reads as empty or partial — malformed, or worse, a valid prefix describing a fleet the caller never wrote (the non-blocking flag is what makes that refusal reachable: a plain open of a writerless FIFO blocks inside `open(2)`; the flag is inert for accepted regular files). `-` is not special; stdin is not accepted. Input is bounded at 1 MiB and oversize is **refused, not truncated** — a truncated-but-parsing template would launch a different fleet than the file. Symlinks are followed with every rule binding on the target: a same-user process that can plant a symlink can plant the file. Template-sourced values confer no trust and skip no gate: they traverse exactly the harness-argv/`shellq` path and §7 predicates flag values do, and nothing from a template reaches `send-keys`, the registry, or a `-t` target.
8. **Approved shim-plane residuals.** Same-user unlink/rebind or lockfile/record edits remain out of scope; the detection
   contract compares the advisory recorded shim PID with kernel `LOCAL_PEERPID` and reports disagreement without
   calling either side authentication. `$HOME`, `os.UserConfigDir()`, `AGENTCTL_RUNTIME_ROOT`, and
   `AGENTCTL_STATE_ROOT` are declared, capped, validated, same-user-selectable inputs, not trust anchors. Predictable
   `/tmp/agentctl-<uid>` pre-creation can cause refusal-only denial of service; agentctl never repairs or adopts an
   unsafe tree. The ancestry accident guard refuses both observed self-target and ancestry-undetermined, with distinct
   facts/codes; process-table restrictions can therefore deny control without creating a bypass. PID identity is
   observational: `EPERM`, token mismatch, or token-read failure refuses and may wedge recovery, but never becomes
   absence. Only `kill(pid,0)` returning `ESRCH` permits absence/relaunch. Version-pinned SIGHUP termination evidence
   reduces expected orphan frequency but is not an oracle. Details and manual `child-starting` remedy: design
   §§15.2–15.8.

## File and socket permissions

The shipped pre-0.5 lifecycle writes files only through skill installation under
`$HOME/.claude/skills/agentctl/` and `$HOME/.agents/skills/agentctl/` (directories `0755`, files `0644`, manifest
checked). No shipped launch/control/status/kill path writes lifecycle state yet. At the approved 0.5.0 cutover,
volatile mode-`0600` socket/lock artifacts live below descriptor-verified mode-`0700`
`/tmp/agentctl-<uid>/v1`; durable mode-`0600` reservation/child/config records live below descriptor-verified
`os.UserConfigDir()/agentctl/state-v1`. The exact record path is
`<resolved-state-root>/sessions/<session>/roles/<role>.json`; the lockfile records the fully resolved durable root.
Declared overrides receive identical validation and never confer trust. No lifecycle path writes inside application
repositories.

The internal, not-yet-wired shim package now implements these private roots,
held role claims, advisory lockfile records, atomic durable role records,
version-first framing, `LOCAL_PEERPID`, and ESRCH-only process absence facts.
This implementation status does not change the preceding shipped-path claim:
the CLI cutover and lifecycle wiring remain owned by later issue-182 PRs.

The developer-facing `hack/release-verify.sh` Part C walkthrough is separate from the production surface. On macOS it may copy only the fixed path `~/.codex/auth.json` from the operator's real HOME (proved sufficient for codex-cli 0.146.1, 2026-08-06). For Claude, a 2026-08-08 probe proved Claude Code 2.1.226 could read the existing authentication from a fresh HOME containing the exact symlink `$TEMP_HOME/Library/Keychains → $REAL_HOME/Library/Keychains`; the verifier offers that link separately, stating before consent that the probe fleet's harnesses can reach the operator's login keychain through it (per-item ACLs still apply). It copies no Claude secret or Keychain data, but token refresh writes through the link reach the real login keychain.

**Synthesized Claude onboarding configuration.** A 2026-08-10 probe on Claude Code 2.1.226 showed that the Keychains link alone still led an interactive first start to request re-authentication, while the same link plus a mode-`0600` `.claude.json` containing only `{"hasCompletedOnboarding":true}` reached the authenticated ready state without re-login. On the consented-link path, Part C synthesizes exactly that non-secret onboarding configuration inside the temporary HOME. It is not an authentication mechanism: the Keychains link supplies credential access, and the verifier never reads or copies the operator's real `.claude.json`, account identifiers, project history, or MCP configuration. The isolated-keychain path receives no synthesized `.claude.json`; interactive sign-in remains its designed behavior. The synthesized file is removed with the temporary HOME on every cleanup path.

The Codex filename and Claude symlink are printed without credential contents and each requires its own explicit `y`. Declining the link offers guided sign-in backed by a mode-`0700` isolated Keychains directory and an empty login keychain created with `security create-keychain` (minting a fresh token); declining both Claude paths aborts before fleet launch. The verifier never seeds `CLAUDE_CODE_OAUTH_TOKEN` — that path can silently delete the real Keychain credential on exit (anthropics/claude-code#37512); `claude setup-token` is documented only as a manual fallback for Keychain-locked contexts (SSH, launchd). The temporary HOME and credential-parent directories are `0700`, the copied Codex file and synthesized Claude onboarding file `0600`, and no credential contents are written to output or evidence. On success, failure, refusal, interrupt, and abort, teardown first ends the owned fleet and named tmux server (so no harness can still use the link), then removes and observes absence of only the exact owned symlink, then removes the credential-bearing HOME; the target directory is never a recursive-removal operand, and the verifier refuses success if link or HOME removal is not observed. When consent aborts before the wrapper-owned tmux server exists, tmux's exact single-line connect-ENOENT response is accepted as factual socket absence so teardown can continue. Fixture tests retain a sentinel in the fake target on every exit path, and unseeded Claude credential lookalikes never cross the launch boundary.

## Ratified constraints for the issue-182 per-agent shim

The [approved design §15](docs/superpowers/specs/2026-08-01-agentctl-design.md#15-approved-050-per-agent-shim-contract)
supersedes the options paper and makes these ratified invariants. They bind every implementation PR. They do not claim
the production cutover has shipped; each behavior PR updates shipped wording with its importer/wiring.

PR 2 implements constraints 1–4, 6, 8, and 10 as internal primitives and live
Darwin tests. It does not activate the shim lifecycle, retire tmux identity, or
broaden the closed operation surface.

Existing shipped claims are amended only at the listed cutover PR; until then their current-path wording remains true:

| Existing shipped claim | Approved Option S amendment | Shipped-wording owner |
|---|---|---|
| The tmux managed/version, exact role-window, one-live-pane, process-baseline, and `$TMUX_PANE` chain gates control. | Runtime name/version/answerer/readiness/ancestry gates replace it; tmux becomes presentation only. | PR 7 atomic cutover |
| `@agentctl_role` and `@agentctl_process` are advisory role identity/baseline evidence. | They cease to establish identity; the held `flock`, durable child identity, and connected answerer observations govern. | PRs 2 and 5 implement; PR 7 cuts over |
| No launch/control/status/kill lifecycle path writes persistent state. | Durable role/config records use the descriptor-verified state root and volatile claim/socket artifacts use the runtime root. | PRs 2 and 5 implement; PR 7 cuts over |
| The lifecycle is unsynchronized and bounded by check-then-act races. | A lifetime `flock` serializes ownership; residual same-user edits and refusal outcomes remain. | PRs 2 and 5 implement; PR 7 cuts over |
| Fixed control payloads reach tmux through `send-keys`. | The client's only delivery instruction is a closed operation name; framing/version, validated session/role identity, and typed response facts remain permitted by the exact wire schema. The shim is the sole PTY writer. Non-transactional TUI-delivery residual 1 remains. | PRs 4 and 6 implement; PR 7 cuts over |
| `AGENTCTL_SESSION`, `AGENTCTL_ROLE`, `AGENTCTL_MANAGED`, and `TMUX_PANE` are informational and never prove a target. | Unchanged: none becomes a runtime identity, answerer, readiness, or ancestry input. | No wording transition required |

1. **The role claim is a kernel-arbitrated lock, not a socket file.** Successful exclusive `flock(LOCK_EX)` on a per-role lockfile is the sole ownership instant, and the lock is held for the shim's lifetime; the socket is bound only while the lock is held, and reclaim after any death is lock acquisition alone. Bare `bind()` claims and probe-connect → `unlink` → `bind` reclaim are inadmissible (demonstrated: the socket file survives SIGKILL and blocks rebind; unlink-based reclaim can silently orphan a live shim).
2. **Socket-path forgery is a named residual with an honest detection contract.** Same-user unlink-and-rebind remains out of scope. The lockfile body is advisory; `LOCAL_PEERPID` is the kernel answerer fact. `status` reports advisory-record/kernel-answerer disagreement and never calls it kernel-vs-kernel proof.
3. **Runtime directory discipline.** The production base is `/tmp/agentctl-<decimal-uid>/v1`; session/role names are each capped at 32 ASCII bytes, producing a 98-byte worst-case production socket path (99 with NUL). Overrides are independently checked against Darwin `sun_path[104]`. Volatile and durable roots are created `0700` exclusively and descriptor-verified. `$HOME`/`os.UserConfigDir()` and both root overrides are declared, capped, same-user-selectable residual surfaces. Predictable `/tmp` pre-creation refuses.
4. **The only delivery instruction is a closed operation name.** The request otherwise carries only framing/version
   and validated session/role identity, and the response carries only framing/version and typed objective facts, exactly
   as design §15.5 specifies. No field can carry caller-supplied PTY text, raw keys, arguments, model values, or
   environment values; the shim is the sole writer to the harness PTY.
5. **The shim is the enforcement point.** Design §15.5 retires tmux metadata/window/pane checks, moves role validation and version/answerer/readiness checks, and replaces `$TMUX_PANE` with one fail-closed ancestry snapshot seeded from `LOCAL_PEERPID`. Advisory environment never targets.
6. **No role is absent while its recorded child may live.** A pre-fork durable `child-starting` reservation upgrades with PID/raw start token. `kill(pid,0)` `ESRCH` is the sole absence permission; nil, `EPERM`, other errors, token mismatch, and token-reader errors refuse distinctly. The live probe observed both pinned harness children terminate after shim SIGHUP, but explicit orphan/indeterminate states remain mandatory. Dead-shim `child-starting` requires the manual recorded-root remedy in design §15.3.
7. **No control delivery before the channel is proven clean.** The nested raw-mode transitions race at startup and
   corrupt early bytes (demonstrated). Design §15.3 fixes the observation: `TIOCGETA` on the retained PTY master at
   `t=0`, every 50ms, and the inclusive 5s boundary; ready means one snapshot has both `ICANON` and `ECHO` clear while
   the listener/relay/child remain live. Errors, child exit, and final cooked/echo flags have distinct bounded factual
   outcomes; prompts and terminal contents never participate.
8. **Ownership and exit codes are specified before code.** Successful `flock(LOCK_EX)` is the sole ownership instant. Design §§15.3/15.8 assign every pre/post-child failure and rollback, and give observed-self-target and ancestry-undetermined distinct typed messages/codes.
9. **Delivery claims stay factual.** Keystroke delivery remains non-transactional (residual 1 applies unchanged); the shim reports what it wrote and observed, and execution is never asserted from delivery.
10. **The wire protocol is version-gated, fail closed.** S makes agentctl a two-process system, so a CLI may meet a
    shim launched by a different agentctl version. Design §15.5 pins `ShimProtocolVersion=1`, a four-byte big-endian
    length header, a 4096-byte maximum JSON payload, a two-second per-frame I/O deadline,
    server-hello/request/response order, and a version-only token pre-pass before schema or operation interpretation
    in both directions. A mismatched, malformed, duplicate, or absent version refuses with the exact §15.8 fact. No
    diagnostic swaps the source: the current client names the connected shim hello version, while the current shim
    names the client request version without interpreting its operation/session/role. Frame-read and frame-write
    failures likewise use distinct literals and closed cause sets. No migration or dual-dialect support is owed across
    the tmux-metadata → shim transition; the gate exists for skew, not coexistence.

## Reporting a vulnerability

Please open a GitHub security advisory (or a private report to the repository owner) rather than a public issue for anything exploitable. Include the agentctl, tmux, amq, and harness versions involved. Reports about risks explicitly accepted above are welcome as ordinary issues if you believe the assessment is wrong.

## Security updates

Security-relevant changes (validation rules, the payload registry, the target-validation chain, quoting) are called out in release notes. The manual harness re-verification checklist runs before releases that change tmux targeting, harness startup, or injected command delivery.
