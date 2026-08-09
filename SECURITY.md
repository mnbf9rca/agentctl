# Security Policy

## Security model

`agentctl` is a personal, single-user, on-machine fleet launcher. It creates tmux sessions that run AI coding agents via `amq coop exec`, and delivers a small fixed set of control keystrokes to those agents. Like [AMQ](https://github.com/avivsinai/agent-message-queue), whose posture this document mirrors, it prioritizes **correctness and accident-prevention over defense against a local attacker with code-execution parity**: anyone who can run processes as your user already owns your tmux socket, your shell, and your agents.

### Threat model — what agentctl defends against

- **Command injection through its own inputs.** Session and role names are validated against `^[a-z0-9][a-z0-9_-]*$`; model identifiers against `^[a-zA-Z0-9][a-zA-Z0-9._-]*$`; effort levels against a closed per-harness set (`low`, `medium`, `high`, `xhigh`, `max`), so no caller-supplied text reaches the codex `--config 'model_reasoning_effort="…"'` expression, whose value portion codex parses as TOML. The only shell-interpreted string in the system (the window command tmux runs) is assembled by a dedicated, fuzz-tested quoting layer from tokens that have already passed these checks. agentctl itself never invokes a shell; all tmux calls are argv arrays.
- **Flag smuggling into agent processes.** Model identifiers must start with an alphanumeric character, so a value like `--dangerously-bypass-approvals-and-sandbox` cannot be passed through the model slot into a harness's argv. Effort levels are matched against an allowlist rather than a charset, so the effort slot cannot carry a flag, a quote, or a newline at all.
- **Arbitrary keystroke injection.** The control surface is a hardcoded registry of predefined commands (`clear → /clear`, `compact → /compact` in v1). Every payload is a complete constant: the registry may grow, but commands that carry caller-supplied text (e.g. `/rename NAME`) are permanently inadmissible. There is no option, environment variable, or stdin path by which caller-supplied text can reach `tmux send-keys`. Payloads are sent in tmux literal mode with Enter as a separate event.
- **Controlling the wrong terminal.** Control commands run a fail-closed validation chain: ROLE charset validated before any command runs → session resolved by exact comparison and addressed by tmux ID → session is agentctl-managed at the expected version → window resolved by exact comparison and addressed by tmux ID, with more than one match refused → stored `@agentctl_role` exactly equals the requested role → exactly one pane → pane alive → the pane's root-process executable exactly equals the identity recorded at launch (`@agentctl_process`) → the target pane is not the caller's own pane. Delivery targets the resolved pane ID. `@agentctl_role` is window-scoped by construction: agentctl writes it only during window stamping and never during session stamping, so a handmade same-name window cannot inherit ownership evidence. `TestStampSessionNeverWritesWindowOwnershipMarker` is the named unit invariant for that write boundary, and `TestIntegrationHandmadeRosterWindowIsNeverControlledOrReplaced` pins it against real tmux.
- **Accidental self-lobotomy.** When agentctl is invoked from inside tmux, a control command targeting the caller's own pane (`$TMUX_PANE`) is refused, so an agent — or a confused planner — cannot wipe its own context through agentctl. This is an accident guard: `TMUX_PANE` is ordinary environment and can be unset, and nothing stops an agent typing `/clear` into its own TUI directly; that is the harness's domain.
- **Destroying sessions and windows agentctl does not own.** Launch rollback kills only a session created by the current invocation. `agentctl relaunch` rolls back with `tmux kill-window` against **only** the window ID that invocation just created and parsed; it is never issued against a window found by listing, and the wrapper takes a typed window ID so no name can reach `-t`. `agentctl kill` refuses sessions not marked agentctl-managed. Attach and control commands refuse unmanaged sessions.
- **Repairing the wrong window.** `relaunch` begins only when a role matches **exactly zero** windows. A role whose window still exists — running, dead, unmanaged, pane-less, or ambiguous — is refused with the observed state reported. It never kills and recreates, so a dead pane's evidence survives. Concurrent relaunches can both pass that initial observation and transiently create same-name windows, so every invocation re-lists the exact session immediately after creation and reports success only if its newly parsed window ID is the role's sole match. An invocation that observes a conflict refuses and rolls back only its own window ID; simultaneous contenders may both roll back, leaving the role absent for a factual retry, but they do not both report success or leave a permanent ambiguity during ordinary completion. A failed rollback is reported with the observed IDs and the cleanup failure rather than hidden.
- **Silent fleet divergence through repair.** Per-role configuration is recorded at launch in the session options `@agentctl_fleet` (`role:harness:model:effort` quads, with every field validated) and `@agentctl_dir` (the exact absolute directory string passed to `-c`; relative launch flags are made absolute before creation and stamping). `relaunch` uses those values, re-validating them on read, so a repaired role runs the harness, model, effort and directory the fleet was launched with. A pre-fix relative `@agentctl_dir` is refused rather than resolved against the relaunching process's cwd; an explicit `--dir` is the stated escape hatch because the original base can no longer be known. Explicit `--harness`/`--model`/`--effort`/`--dir` may override a field, but every field's provenance is reported. Within a single successful invocation, an overridden harness, model or effort is written back to `@agentctl_fleet` so the record matches the live fleet. Concurrent overriding relaunches of different roles can each rewrite a roster derived from an earlier read; the last writer wins, so one override can be lost from the record even though it is live. A session predating these options is refused unless the configuration is supplied explicitly; the working directory is never defaulted to the invocation directory.

### Out of scope

- **Same-user malicious processes.** Any process running as your user can drive tmux directly, forge `@agentctl_*` metadata, or invoke agentctl itself. This includes the fleet's own agents: a prompt-injected agent could run `agentctl clear` against a sibling. agentctl does not attempt to defend a trust boundary that the operating system does not provide.
- **Multi-user scenarios.** agentctl has no cross-account hardening; the tmux socket's own `0700` permissions are the boundary.
- **Filesystem namespace manipulation** (bind mounts, pre-existing device files, privileged tricks) — as in AMQ's policy, these require system-level hardening.
- **The agents themselves.** What an agent does after receiving `/clear` or `/compact` — or what it does at all — is governed by the harness's own permission model, not by agentctl.

## Known risks and accepted residuals

1. **Keystroke delivery is not transactional, and degrades under host saturation.** This absorbs what were previously two separate residuals: delivery reliability and popup selection are not independent, and mitigating either alone does not cover the case where they compose.

   The evidence is split and version-pinned. Fixed-injection behavior — junk cleared, payload executed, and conversation reset for both harnesses — was re-verified 2026-08-05 on Claude Code 2.1.222 and codex-cli 0.146.0 (`docs/release-verification-notes.md`, verify-live run at `bb68e3b`; criteria in `docs/release-checklist.md`). The exact-popup-highlight observation and every saturation measurement below remain from 2026-08-01 on Claude Code 2.1.220 and codex-cli 0.146.0. Codex was unchanged between the two evidence legs; Claude Code advanced two patch versions, so this is not a blanket re-verification. The `--measure` suite is gated on `internal/tmuxx.payloadDelay` changing and has not been re-run.

   `tmux send-keys` succeeding proves tmux accepted the keys, not that the TUI executed the command. A payload can land while the TUI is in an unexpected state (modal dialog, confirmation prompt, mid-render). Separately, both harnesses open an autocomplete popup when a slash command is typed, and Enter selects the **highlighted** entry; in the 2026-08-01 evidence, typing the *full* payload highlighted the exact match.

   Those two facts couple, because the popup verification assumes a full payload. **Measured under saturation** (18 concurrent busy workers, codex, 1000ms delay, 20 loaded trials):

   - **Demonstrated:** input corruption. Full-payload delay and loss-to-empty, blank redraws, and one doubled `/clearclear` in the observation leg (which matched nothing and opened no popup).
   - **Not observed:** ambiguous truncation or wrong-highlight selection. Zero ambiguous truncations and zero wrong-highlight events across the trials.
   - **Every observed failure left the payload unexecuted** — the fail-safe direction.

   The composite risk is therefore *possible but unobserved*: corruption is real, and a truncation that happened to form a prefix the popup resolves to a different command would execute the wrong command silently. Nothing in these measurements shows that happening, and nothing in them shows it cannot. The popup ranks the harness's entire command palette, not just agentctl's registry, so the collision space is not one agentctl can enumerate or bound.

   **Timing floors.** Claude: 750ms under load. Codex: **no floor established** at any tested candidate under adversarial saturation — it failed at the production constant. The 1s constant is retained as the value with the best evidence behind it, not as a value proven sufficient. These are adversarial-ceiling figures: they describe a deliberately saturated host, not normal operation.

   **Degradation under this condition is now per role.** Baseline settling is slowest exactly when the host is most loaded, which is when the fleet is largest, so a launch-time poll timeout used to destroy the whole session at its most likely failure moment. It no longer does: the affected role is left unproven and inert via the empty-baseline refusal in residual 2, its peers keep running, and `launch` exits 9 saying so.

   **Responsibility.** The orchestrator — not agentctl, and not the operator alone — must not issue control commands while its own fleet is saturating the host. This extends the existing rule from *whether the role has been released* to *whether the host can carry the delivery*. It sits with the orchestrator by necessity rather than preference: agentctl cannot detect saturation without exactly the machine-state inference the design forbids it (§6.3), while the orchestrator both causes the load and chooses the timing, so it is the only component positioned to know. A human running agentctl by hand inherits the same rule.

2. **Process identity is observational, not cryptographic.** The identity check compares the pane root process's executable against a baseline observed and recorded at launch (necessary because e.g. Claude Code's process name is its version string, `2.1.220`, not `claude`). Baseline capture assumes `amq coop exec` replaces itself directly with the harness, with no intermediate root process, and accepts only two consecutive identical non-`amq` observations 100ms apart. That reduces the chance of recording a transient process but does not prove settling: a transient that persists across both samples can still become the baseline. A same-user process can forge the recorded metadata or rename an executable to match — consistent with the same-user exclusion above. Verification never heals or replaces the recorded baseline: doing so would let a persistent intruder become accepted instead of remaining detected. The check is a guard against *accidents* (e.g. a shell where an agent should be), not an authentication mechanism.

   When the poll reaches its deadline without accepting a pair, `launch` records **no** baseline for that role, leaves the window in place, and exits 9 rather than destroying the fleet. The role is then inert rather than contained by force: verification requires exact equality with a stored value, and an unset baseline can never satisfy it, so control refuses at exit 5 and `status` reports `no-baseline` — a state deliberately distinct from `unexpected-process`, because "never proved" and "proved, then changed" call for different responses. `relaunch` may recover such a window by destroying it and creating a replacement. That is **not** heal-on-verify: no recorded baseline is ever replaced, the pane is destroyed and a new process observed from scratch, and a window whose *recorded* baseline mismatches the live process is never recoverable this way — it stays refused, because it is the one case where the pane is evidence of an unexplained event.
3. **Environment staleness.** tmux windows inherit the tmux server's environment, which may predate the current shell. Credentials or configuration exported after the server started may not reach agents. Documented behavior; not solved by agentctl.
4. **Metadata is advisory.** `@agentctl_*` tmux options are readable and writable by any same-user process. They gate agentctl's own actions and support status reporting; they are not tamper-proof. `relaunch` reads stored per-role configuration out of `@agentctl_fleet` and `@agentctl_dir` and feeds it into a launched process, so it re-applies the same validation that `launch` applies to harnesses, models and effort levels and requires a stored directory to be absolute: an unknown harness, a model that could smuggle a flag, an unknown effort, or a relative stored directory is refused rather than executed. That closes the accident and the trivially-tampered case; it does not make the metadata a trust boundary, and a same-user process able to rewrite it can already run the harness directly.
5. **Launched-window identity variables are informational.** Every window `launch` or `relaunch` creates carries `AGENTCTL_SESSION`, `AGENTCTL_ROLE` and `AGENTCTL_MANAGED=1`, passed as separate tmux `-e NAME=value` argv elements from values that already passed the identifier validation above; no shell-interpreted string is built from them. After the first launch window is stamped, agentctl removes the session-environment copies so windows created later by hand do not inherit stale identity; failure is reported but does not roll back the fleet. The managed windows themselves are unaffected. Any same-user process can export the same names, so they carry the same weight as the advisory metadata in residual 4: agentctl never reads them back when validating a control, `kill`, or `status` target, which continues to rest on `@agentctl_*` and the fail-closed chain. `AGENTCTL_SESSION` remains a session-*selection* source (§4.1 of the design spec), which names a candidate session; every check that follows is unchanged.
6. **Concurrency is unsynchronized.** There is no locking anywhere in agentctl. Ownership and safety gates are check-then-act, so concurrent invocations can interleave even though the observed outcomes below remain bounded:

   | Race | Current outcome |
   | --- | --- |
   | Two `launch` invocations for one session name | Fail-closed. tmux refuses the duplicate atomically; the winner remains intact and fully stamped, while the loser damages nothing, leaves nothing behind, and exits 3 whether the pre-check or `new-session` observes the duplicate. |
   | `launch` mid-stamp vs a concurrent read | Fail-closed. `show-options -qv` returns empty for an unset option, so a half-stamped session reads as unmanaged and every gate refuses it. |
   | `clear`/`compact` vs `kill` | Fail-safe. tmux does not reuse pane IDs, so a stale pane ID cannot address a different pane and `send-keys` to a gone pane fails. |
   | Two `relaunch` invocations for one absent role | Simultaneous contenders may both observe the post-create conflict, roll back only the window each created, and exit 8. The role is then absent, not ambiguous, and a later relaunch can recover it. |
   | Two `relaunch` invocations recovering one unproven role | Bounded. Both classify the same window ID; one kills and recreates, the other's kill fails against a window that no longer exists and it exits 6 having created nothing. Window IDs are a monotonic per-server counter and are not reused within a server lifetime (probed on tmux 3.7b; a restarted server resets the counter, as pane IDs already do), so a stale recorded ID can never name the winner's new window. |
   | Concurrent overriding `relaunch` invocations for different roles | Last writer wins on the non-atomic `@agentctl_fleet` read-modify-write, so one live override can be lost from the record. |
   | Locking | None. |

   `relaunch`'s recovery classification and its kill are check-then-act like every other gate here: the window is classified `no-baseline` from metadata, then removed by the ID that classification produced. Because the classification reads advisory metadata (residual 4), a same-user process can arrange for a window to be destroyed and recreated — but that same process could destroy the window directly, so this adds no capability.

   Delivery has one bounded TOCTOU: the identity check reads the pane PID and runs `ps`, then delivery targets the pane ID. Pane IDs are not reused, so the target cannot shift to a different pane, but `respawn-pane -k` can replace the process in the same pane between those operations while retaining the pane ID and assigning a new PID. That requires same-user action and remains accepted under the threat model above.

7. **`launch --from-template` reads a caller-named path.** This is the first caller-supplied read path on a command that also drives tmux, so its rules are stated rather than inherited. agentctl opens the file and then verifies the **descriptor** it will read, never stat-ing a path and then reading that path — checking one object and reading another is a time-of-check/time-of-use gap, and this construction has none. Only regular files are accepted: a directory, device or socket is refused, and a FIFO is refused specifically because a blocking read would hang `launch` with no timeout anywhere in the design. `-` is not special and stdin is not accepted. Input is bounded at 1 MiB and an oversize file is **refused, not truncated**, because a truncated template that still parsed would launch a fleet differing from the file. Symlinks are followed and every rule binds on the target: refusing them would break ordinary arrangements for no gain, since a same-user process that can plant a symlink can plant the file.

   What this does **not** change: no value from a template reaches `tmux send-keys`, the payload registry, or a `-t` target; template-sourced values traverse exactly the harness-argv and `shellq` path that flag-sourced values do (spec §12.1), and are validated by the same predicates (spec §7). Being template-sourced confers no trust and skips no gate. A template an agent can write sits inside the same same-user band already excluded from the threat model above, so this feature adds a surface to reason about, not a new privilege.

## File and socket permissions

agentctl writes files only under `$HOME/.claude/skills/agentctl/` and
`$HOME/.agents/skills/agentctl/`, and only when the operator runs
`agentctl skill install` (directories `0755`, files `0644`, plus a
`.agentctl-skill.json` manifest recording the version and SHA-256 of every
file written). Installs are manifest-checked and refuse to overwrite files
agentctl cannot prove it wrote. When ownership cannot be proved, `--force`
replaces the target directory and reports every removed file. No launch,
control, status, or kill path writes to the filesystem; `launch` reads the
manifests (only) to report skill/binary version skew. The skill content is a
build-time constant; target **write** paths are fixed with no caller-supplied
components; a failed `$HOME` resolution is a refusal, not a fallback. That
statement is about the write side, and remains true: `launch --from-template`
adds a caller-named *read* path (residual 7) and no write path.
Otherwise agentctl creates no persistent files (no database, no state
directory) and writes nothing inside application repositories. It relies on
tmux's default private socket (`0700` directory) and AMQ's documented
`0700`/`0600` permissions for session data created by `amq coop exec`.

The developer-facing `hack/release-verify.sh` Part C walkthrough is separate
from agentctl's production file-writing surface. For its isolated live harness
check on macOS, it may copy only the existing fixed path
`~/.codex/auth.json` from the operator's captured real HOME. A 2026-08-06 probe
proved that file sufficient for codex-cli 0.146.1. A 2026-08-08 probe proved
Claude Code 2.1.226 authenticated from a fresh HOME containing only the exact
symlink from `$REAL_HOME/Library/Keychains` to
`$TEMP_HOME/Library/Keychains`. The verifier offers that fixed link separately
and states before consent that the probe fleet's harnesses can reach the
operator's login keychain through it; per-item ACLs continue to apply. It
copies no Claude secret or Keychain data, but token refresh writes through the
link reach the real login keychain as they would from the operator's daily
harnesses.

The Codex filename and Claude symlink are printed without credential contents
and each requires its own explicit `y`. Declining the Claude link offers guided
sign-in backed by a mode-`0700` isolated Keychains directory and an empty login
keychain created under the temporary HOME with `security create-keychain`; this
mints a fresh token. Declining both Claude paths aborts before fleet launch.
The verifier never seeds `CLAUDE_CODE_OAUTH_TOKEN` because that path can
silently delete the real Keychain credential on exit
(anthropics/claude-code#37512); `claude setup-token` is documented only as a
manual fallback for Keychain-locked contexts such as SSH or launchd.

The temporary HOME and credential-parent directories are `0700`, the copied
Codex file is `0600`, and no credential contents are written to output or
evidence. On success, failure, refusal, interrupt, and abort, teardown first
ends the owned fleet and named tmux server so no harness can still use the
link, then removes and observes absence of only the exact owned symlink, and
only then removes the credential-bearing HOME. The target directory is never
a recursive-removal operand. The verifier refuses success if link or HOME
removal is not observed; fixture tests retain a sentinel in the fake target on
every exit path, including abort, while unseeded Claude lookalike files never
cross the launch boundary.

## Reporting a vulnerability

Please open a GitHub security advisory (or a private report to the repository owner) rather than a public issue for anything exploitable. Include the agentctl, tmux, amq, and harness versions involved. Reports about risks explicitly accepted above are welcome as ordinary issues if you believe the assessment is wrong.

## Security updates

Security-relevant changes (validation rules, the payload registry, the target-validation chain, quoting) are called out in release notes. The manual harness re-verification checklist runs before releases that change tmux targeting, harness startup, or injected command delivery.
