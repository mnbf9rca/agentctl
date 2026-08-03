# Security Policy

## Security model

`agentctl` is a personal, single-user, on-machine fleet launcher. It creates tmux sessions that run AI coding agents via `amq coop exec`, and delivers a small fixed set of control keystrokes to those agents. Like [AMQ](https://github.com/avivsinai/agent-message-queue), whose posture this document mirrors, it prioritizes **correctness and accident-prevention over defense against a local attacker with code-execution parity**: anyone who can run processes as your user already owns your tmux socket, your shell, and your agents.

### Threat model — what agentctl defends against

- **Command injection through its own inputs.** Session and role names are validated against `^[a-z0-9][a-z0-9_-]*$`; model identifiers against `^[a-zA-Z0-9][a-zA-Z0-9._-]*$`. The only shell-interpreted string in the system (the window command tmux runs) is assembled by a dedicated, fuzz-tested quoting layer from tokens that have already passed these checks. agentctl itself never invokes a shell; all tmux calls are argv arrays.
- **Flag smuggling into agent processes.** Model identifiers must start with an alphanumeric character, so a value like `--dangerously-bypass-approvals-and-sandbox` cannot be passed through the model slot into a harness's argv.
- **Arbitrary keystroke injection.** The control surface is a hardcoded registry of predefined commands (`clear → /clear`, `compact → /compact` in v1). Every payload is a complete constant: the registry may grow, but commands that carry caller-supplied text (e.g. `/rename NAME`) are permanently inadmissible. There is no option, environment variable, or stdin path by which caller-supplied text can reach `tmux send-keys`. Payloads are sent in tmux literal mode with Enter as a separate event.
- **Controlling the wrong terminal.** Control commands run a fail-closed validation chain: ROLE charset validated before any command runs → session resolved by exact comparison and addressed by tmux ID → session is agentctl-managed at the expected version → window resolved by exact comparison and addressed by tmux ID, with more than one match refused → window is managed → stored role metadata matches → exactly one pane → pane alive → the pane's root-process executable exactly equals the identity recorded at launch (`@agentctl_process`) → the target pane is not the caller's own pane. Delivery targets the resolved pane ID.
- **Accidental self-lobotomy.** When agentctl is invoked from inside tmux, a control command targeting the caller's own pane (`$TMUX_PANE`) is refused, so an agent — or a confused planner — cannot wipe its own context through agentctl. This is an accident guard: `TMUX_PANE` is ordinary environment and can be unset, and nothing stops an agent typing `/clear` into its own TUI directly; that is the harness's domain.
- **Destroying sessions agentctl does not own.** Launch rollback kills only a session created by the current invocation. `agentctl kill` refuses sessions not marked agentctl-managed. Attach and control commands refuse unmanaged sessions.

### Out of scope

- **Same-user malicious processes.** Any process running as your user can drive tmux directly, forge `@agentctl_*` metadata, or invoke agentctl itself. This includes the fleet's own agents: a prompt-injected agent could run `agentctl clear` against a sibling. agentctl does not attempt to defend a trust boundary that the operating system does not provide.
- **Multi-user scenarios.** agentctl has no cross-account hardening; the tmux socket's own `0700` permissions are the boundary.
- **Filesystem namespace manipulation** (bind mounts, pre-existing device files, privileged tricks) — as in AMQ's policy, these require system-level hardening.
- **The agents themselves.** What an agent does after receiving `/clear` or `/compact` — or what it does at all — is governed by the harness's own permission model, not by agentctl.

## Known risks and accepted residuals

1. **Keystroke delivery is not transactional, and degrades under host saturation.** This absorbs what were previously two separate residuals: delivery reliability and popup selection are not independent, and mitigating either alone does not cover the case where they compose.

   `tmux send-keys` succeeding proves tmux accepted the keys, not that the TUI executed the command. A payload can land while the TUI is in an unexpected state (modal dialog, confirmation prompt, mid-render). Separately, both harnesses open an autocomplete popup when a slash command is typed, and Enter selects the **highlighted** entry — verified 2026-08-01 (Claude Code 2.1.220, codex 0.146.0) that with the *full* payload typed, the exact match is highlighted.

   Those two facts couple, because the popup verification assumes a full payload. **Measured under saturation** (18 concurrent busy workers, codex, 1000ms delay, 20 loaded trials):

   - **Demonstrated:** input corruption. Full-payload delay and loss-to-empty, blank redraws, and one doubled `/clearclear` in the observation leg (which matched nothing and opened no popup).
   - **Not observed:** ambiguous truncation or wrong-highlight selection. Zero ambiguous truncations and zero wrong-highlight events across the trials.
   - **Every observed failure left the payload unexecuted** — the fail-safe direction.

   The composite risk is therefore *possible but unobserved*: corruption is real, and a truncation that happened to form a prefix the popup resolves to a different command would execute the wrong command silently. Nothing in these measurements shows that happening, and nothing in them shows it cannot. The popup ranks the harness's entire command palette, not just agentctl's registry, so the collision space is not one agentctl can enumerate or bound.

   **Timing floors.** Claude: 750ms under load. Codex: **no floor established** at any tested candidate under adversarial saturation — it failed at the production constant. The 1s constant is retained as the value with the best evidence behind it, not as a value proven sufficient. These are adversarial-ceiling figures: they describe a deliberately saturated host, not normal operation.

   **Responsibility.** The orchestrator — not agentctl, and not the operator alone — must not issue control commands while its own fleet is saturating the host. This extends the existing rule from *whether the role has been released* to *whether the host can carry the delivery*. It sits with the orchestrator by necessity rather than preference: agentctl cannot detect saturation without exactly the machine-state inference the design forbids it (§6.3), while the orchestrator both causes the load and chooses the timing, so it is the only component positioned to know. A human running agentctl by hand inherits the same rule.

2. **Process identity is observational, not cryptographic.** The identity check compares the pane root process's executable against a baseline observed and recorded at launch (necessary because e.g. Claude Code's process name is its version string, `2.1.220`, not `claude`). A same-user process can forge the recorded metadata or rename an executable to match — consistent with the same-user exclusion above. The check is a guard against *accidents* (e.g. a shell where an agent should be), not an authentication mechanism.
3. **Environment staleness.** tmux windows inherit the tmux server's environment, which may predate the current shell. Credentials or configuration exported after the server started may not reach agents. Documented behavior; not solved by agentctl.
4. **Metadata is advisory.** `@agentctl_*` tmux options are readable and writable by any same-user process. They gate agentctl's own actions and support status reporting; they are not tamper-proof.
5. **Launched-window identity variables are informational.** Every window `launch` creates carries `AGENTCTL_SESSION`, `AGENTCTL_ROLE` and `AGENTCTL_MANAGED=1`, passed as separate tmux `-e NAME=value` argv elements from values that already passed the identifier validation above; no shell-interpreted string is built from them. They exist so an agent can recognize its own fleet. Any same-user process can export the same names, so they carry the same weight as the advisory metadata in residual 4: agentctl never reads them back when validating a control, `kill`, or `status` target, which continues to rest on `@agentctl_*` and the fail-closed chain. `AGENTCTL_SESSION` remains a session-*selection* source (§4.1 of the design spec), which names a candidate session; every check that follows is unchanged.

## File and socket permissions

agentctl creates no persistent files of its own (no database, no state directory) and writes nothing inside application repositories. It relies on tmux's default private socket (`0700` directory) and AMQ's documented `0700`/`0600` permissions for session data created by `amq coop exec`.

## Reporting a vulnerability

Please open a GitHub security advisory (or a private report to the repository owner) rather than a public issue for anything exploitable. Include the agentctl, tmux, amq, and harness versions involved. Reports about risks explicitly accepted above are welcome as ordinary issues if you believe the assessment is wrong.

## Security updates

Security-relevant changes (validation rules, the payload registry, the target-validation chain, quoting) are called out in release notes. The manual harness re-verification checklist runs before each release.
