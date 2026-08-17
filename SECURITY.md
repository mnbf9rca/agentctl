# Security Policy

## Security model

`agentctl` is a personal, single-user, on-machine fleet launcher. It starts AI coding agents, keeps a resident per-role shim that owns each agent's terminal, and delivers a small fixed set of control operations to them. Like [AMQ](https://github.com/avivsinai/agent-message-queue), whose posture this document mirrors, it prioritizes **correctness and accident-prevention over defense against a local attacker with code-execution parity**: anyone who can run processes as your user already owns your terminals, your files, and your agents. It listens on no network port and never invokes a shell. Behavior is defined by the [design specification](docs/superpowers/specs/2026-08-01-agentctl-design.md); this document records the threats considered, what was done about each, and what risk remains.

### Threat model — what agentctl defends against

- **Command injection through its own inputs.** Session and role names are validated against a closed lowercase grammar and capped in length; model and effort identifiers against their own ASCII grammar. The only shell-interpreted string in the system — the window command tmux runs — is assembled at a single structurally pinned site by a dedicated, fuzz-tested quoting layer, from tokens that already passed those checks. All tmux and process calls are argv arrays.
- **Flag smuggling into agent processes.** Model and effort values cannot begin with `-` or contain whitespace, quotes, backslashes, `=`, `:`, or newlines, so a value like `--dangerously-bypass-approvals-and-sandbox` cannot travel through those slots into a harness argv. Harness argv is rebuilt from a closed registry.
- **Arbitrary keystroke injection through control operations.** A control client sends only a protocol version, validated identity, and one name from the closed argument-free registry. Direct `attach ROLE` is the separate, explicit terminal-input path: after target and same-uid peer validation it relays the admitted viewer's bytes verbatim, with one viewer at a time (spec §§15.5, 15.11).
- **Controlling the wrong role.** A lifetime kernel `flock` is the sole ownership instant. Before acting, a command checks the state root recorded in the runtime lockfile, connects to the exact role socket, version-checks it, compares the kernel-reported peer against the recorded shim, and confirms the claim is held and the role ready (spec §15). tmux names, windows, panes, and layouts authorize nothing.
- **Clearing the agent you are typing into.** Before a control request the client walks process ancestry from itself toward the connected peer. "Observed ancestor" and "could not determine" are distinct outcomes and both refuse (spec §15.5). No environment variable establishes this guard.
- **Starting a second agent beside a live one, or reporting a live agent gone.** Only an observed `ESRCH` permits calling a recorded child absent or starting a replacement; every other observation refuses and keeps its evidence (spec §15.4).
- **Delivering into a terminal that is not ready.** Control delivery waits for an observed readiness condition on the agent's terminal rather than assuming startup finished (spec §15.3).
- **Destroying sessions, records, or presentation agentctl does not own.** Rollback and shutdown remove only typed identifiers the invocation created or observed, and a fleet record is removed only once every role's cleanup has actually been observed. A survivor, an unobserved cleanup, or an ambiguous result retains the evidence instead (spec §15).
- **A caller-named file becoming a trusted input.** `launch --from-template` verifies the descriptor, accepts only a regular file, bounds input without truncating it, and subjects template values to exactly the validation flag values receive (spec §7).
- **Overwriting files in your home directory.** Skill installation writes only its two declared directories, replacing each entry rather than writing through it, and refuses any target it cannot prove it wrote unless `--force` is explicit (spec §16).
- **A compromised dependency reaching a release.** The module graph is standard-library-first and small, a pinned vulnerability scanner runs in CI, Dependabot watches modules and Actions, and archive verification refuses a release missing a required upstream license.
- **Release verification handling temporary state.** The three-confirmation live smoke uses the operator's already-authenticated harnesses without copying or linking credentials. Its temporary AMQ configuration is removed only after agentctl status proves the owned fleet absent; exhaustive Task 8 checks use isolated roots and no live harness credentials.

### Out of scope

- **Same-user malicious processes.** Any process running as your user can act on your terminals and files directly, edit the records agentctl keeps, or invoke agentctl itself. This includes the fleet's own agents: a prompt-injected agent could run `agentctl clear` against a sibling. agentctl does not attempt to defend a trust boundary the operating system does not provide.
- **Multi-user scenarios.** There is no cross-account hardening; file and directory permissions are the boundary.
- **The network.** agentctl exposes no listening network surface.
- **Filesystem namespace manipulation** (bind mounts, pre-existing device files, privileged tricks) — as in AMQ's policy, these require system-level hardening.
- **The agents themselves.** What an agent does after receiving `/clear` or `/compact` — or what it does at all — is governed by the harness's own permission model.
- **Authenticating advisory metadata.** The lockfile body is compared against kernel observations to detect disagreement; it is not a trust boundary.

## Known risks and accepted residuals

1. **Control delivery is not transactional.** That the shim wrote the fixed bytes and observed submit does not prove the agent executed the command. A payload can land while the TUI is in an unexpected state — modal dialog, confirmation prompt, mid-render — and a saturated host widens that window. The orchestrator, not agentctl, is responsible for issuing controls only when a role is free.
2. **Process identity is observational, not authentication.** A recorded process identifier and start token guard against *accidents*, not against a same-user process arranged to match. Mismatches and failed observations wedge automatic recovery rather than guessing — the intended direction.
3. **Same-user edits remain possible.** The held claim prevents two cooperating shims from owning one role, and comparing the recorded shim against the connected peer detects a class of socket substitution. Neither is authentication, and neither is meant to be.
4. **The runtime directory name is predictable.** Another account pre-creating it denies service. agentctl refuses an unsafe or substituted tree rather than repairing or adopting it.
5. **Declared roots are selectable, not trusted.** The home directory, the resolved configuration directory, and the two root overrides are bounded inputs. Ancestor *writability* can redirect where state resolves, and a changed home directory can resolve a different durable tree; a recorded anchor makes that disagreement refuse rather than pass silently.
6. **A reservation left by a dead shim clears only by hand.** It records no child process, so agentctl cannot prove absence and will not guess. Recovery is a documented manual step, taken after you independently confirm no agent remains.
7. **Environment staleness.** tmux windows inherit the tmux server's environment, which may predate the current shell. Credentials or configuration exported after the server started may not reach agents. Documented behavior; not solved by agentctl.
8. **Template symlinks are followed.** Anyone who can plant a symlink where you point `--from-template` can plant the file itself, so this adds no capability — but the target, not the link, is what is read.
9. **Presentation cleanup can race tmux.** Only an exact observation that a presentation is gone permits removing the fleet record. "Gone" and "removed" remain different facts.
10. **Fleets without a shim record are not adopted.** A fleet started by the older tmux-metadata lifecycle leaves nothing agentctl can read, so it is neither recognized nor migrated; stop it with the binary that started it. Compatibility is decided by the recorded schema and the wire protocol version, never by binary identity — a different build speaking the same versions operates the same fleet normally.
11. **Direct viewing is bounded, not replayable or lossless at every viewer speed.** Detached shims discard output while no viewer is present; a viewer that cannot keep up is evicted rather than throttling the role, and output is not replayed on re-attach (spec §15.11).

## File and socket permissions

agentctl creates two private trees and writes nothing inside application repositories.

Volatile artifacts — the per-role lifetime lock, control socket, and direct-attach socket — are mode `0600` below a descriptor-verified mode `0700` runtime directory. The attach peer's uid is kernel-observed and must match the shim's; this is same-user accident prevention, not authentication. Durable records — the fleet roster and per-role child records — are mode `0600` below a descriptor-verified, owner-only mode `0700` state directory. Both roots are validated before use, whether they come from the defaults or from the declared overrides, and an unsafe owner, mode, type, symlink, or substituted descriptor is refused rather than repaired.

Skill installation is the only path that writes outside those trees. It writes its two declared directories, mode `0755`, with files mode `0644`, and proves ownership through a manifest before replacing anything (spec §16).

The tmux socket's own `0700` directory and AMQ's documented `0700`/`0600` permissions continue to govern data those tools create.

## Reporting a vulnerability

Please open a GitHub security advisory (or a private report to the repository owner) rather than a public issue for anything exploitable. Include the agentctl, tmux, amq, and harness versions involved. Reports about risks explicitly accepted above are welcome as ordinary issues if you believe the assessment is wrong.

## Security updates

Security-relevant changes (validation rules, the operation registry, runtime targeting, command construction, lifecycle evidence) are called out in release notes. The manual harness re-verification checklist runs before each release.
