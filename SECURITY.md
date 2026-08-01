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

1. **Keystroke delivery is not transactional.** `tmux send-keys` succeeding proves tmux accepted the keys, not that the TUI executed the command. A payload can land while the TUI is in an unexpected state (modal dialog, confirmation prompt, mid-render). Mitigations — the `C-u` input-clear, the foreground-process check, and separate Enter — narrow but do not close this window. The Fable planner, not agentctl, is responsible for only issuing control commands when a role has been released.
2. **Slash-command popup selection.** Both harnesses open an autocomplete popup when a slash command is typed; Enter selects the highlighted entry. Verified 2026-08-01 (Claude Code 2.1.220, codex 0.146.0): with the full payload typed, the exact match is highlighted. A user-defined command that outranks an exact match in a future harness version could be selected instead. Accepted; re-verified by the release checklist.
3. **Process identity is observational, not cryptographic.** The identity check compares the pane root process's executable against a baseline observed and recorded at launch (necessary because e.g. Claude Code's process name is its version string, `2.1.220`, not `claude`). A same-user process can forge the recorded metadata or rename an executable to match — consistent with the same-user exclusion above. The check is a guard against *accidents* (e.g. a shell where an agent should be), not an authentication mechanism.
4. **Environment staleness.** tmux windows inherit the tmux server's environment, which may predate the current shell. Credentials or configuration exported after the server started may not reach agents. Documented behavior; not solved by agentctl.
5. **Metadata is advisory.** `@agentctl_*` tmux options are readable and writable by any same-user process. They gate agentctl's own actions and support status reporting; they are not tamper-proof.

## File and socket permissions

agentctl creates no persistent files of its own (no database, no state directory) and writes nothing inside application repositories. It relies on tmux's default private socket (`0700` directory) and AMQ's documented `0700`/`0600` permissions for session data created by `amq coop exec`.

## Reporting a vulnerability

Please open a GitHub security advisory (or a private report to the repository owner) rather than a public issue for anything exploitable. Include the agentctl, tmux, amq, and harness versions involved. Reports about risks explicitly accepted above are welcome as ordinary issues if you believe the assessment is wrong.

## Security updates

Security-relevant changes (validation rules, the payload registry, the target-validation chain, quoting) are called out in release notes. The manual harness re-verification checklist runs before each release.
