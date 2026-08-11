# Security Policy

## Security model

`agentctl` is a personal, single-user, on-machine fleet launcher. The shipped
0.5.0 lifecycle uses per-role resident shims, lifetime kernel claims, local
operation-name sockets, durable child and fleet records, shim-owned nested
PTYs, optional tmux presentation, and foreground no-tmux roles (design §15).
It prioritizes correctness and accident prevention over defense against a
local attacker with code-execution parity: a process running as the same user
already has access to that user's terminal processes, files, and agents.

### What agentctl defends against

- **Command injection through its inputs.** Session and role names match the
  closed lowercase identifier grammar and are capped at 32 ASCII bytes. Model
  and effort identifiers use the separate validated ASCII grammar. The only
  shell-interpreted string is the tmux window command, assembled at the one
  structurally pinned `internal/fleet/shim.go:shimWindowCommand` site from
  validated argv through fuzz-tested `internal/shellq`. agentctl itself never
  invokes a shell.
- **Flag smuggling.** Model and effort values cannot start with `-` or contain
  whitespace, quotes, backslashes, `=`, `:`, or newlines. Harness argv is
  reconstructed by the closed harness registry.
- **Arbitrary terminal input.** A client can send only protocol version,
  validated session/role identity, and one registered argument-free operation
  name. `clear` and `compact` resolve fixed bytes inside the shim; `observe`
  and `stop` carry no payload. No caller text, raw key, slash command, model,
  environment value, or argument can reach the PTY writer. Production contains
  no tmux `send-keys` path.
- **Controlling the wrong role.** A lifetime `flock` is the sole ownership
  instant. The client checks the advisory lockfile's recorded state root,
  connects to the exact role socket, version-gates the hello, obtains the
  kernel `LOCAL_PEERPID`, compares it with the advisory shim PID, and verifies
  the held claim/readiness before delivery. tmux names, metadata, windows,
  panes, layouts, and process rows are presentation facts only.
- **Accidental self-targeting.** Before writing a payload request, the client
  takes one typed `ps -eo pid=,ppid=` snapshot and walks from the caller PID
  toward the connected `LOCAL_PEERPID`. An observed ancestor and an
  indeterminate ancestry are distinct fail-closed outcomes. Environment
  variables, including `TMUX_PANE`, never establish this guard.
- **Starting beside a possible survivor.** Relaunch and foreground replacement
  require a missing role record or a fresh `kill(pid,0)` `ESRCH` observation.
  Matching live children, `EPERM`, token disagreement, token-read failure,
  cleanup failure, commit uncertainty, orphan state, and dead-shim
  `child-starting` each refuse without being called absent.
- **Deleting presentation or records without evidence.** Launch rollback,
  relaunch rollback, and kill remove only typed IDs/artifacts owned or observed
  by that invocation. Child-before-presentation-before-fleet cleanup order is
  required. Any survivor, absent signal-attempt/exit fact, ambiguous
  presentation result, record uncertainty, or remaining artifact retains
  evidence.
- **Silent configuration drift.** The strict durable fleet record owns one
  session-wide absolute directory, declaration-order roster, and per-role
  harness/model/effort. Relaunch and foreground overrides are persisted only
  after the replacement shim answers ready, through a version-checked
  session-mutation flock. A cwd disagreement on foreground `run` prints both
  paths and refuses before role start or record mutation.

### Out of scope

- Same-user malicious processes, including a prompt-injected fleet agent.
- Multi-user or cross-account hardening.
- Privileged filesystem namespace manipulation such as bind mounts or device
  substitution.
- What a harness does after it receives `/clear` or `/compact`.
- Authentication of advisory metadata. The lockfile body is compared with
  kernel observations but is not a trust boundary.

## Third-party build dependencies

agentctl is standard-library-first. It deliberately includes
`github.com/santhosh-tekuri/jsonschema/v6` to compile and validate the embedded
launch-template schema; its indirect `golang.org/x/text` dependency is visible
in `go.mod` and `go.sum`.

The shipped Darwin shim imports `golang.org/x/sys/unix` v0.47.0 only for
`flock`, `LOCAL_PEERPID`, `kill(pid,0)`, and raw `kinfo_proc` process
observation. Production linkage is verified with `go version -m`. CI runs the
pinned govulncheck scanner and Dependabot monitors module and Actions updates.

Release archives reproduce the upstream licenses for jsonschema, x/sys, and
x/text (including x/text's patent grant) under unambiguous module paths.
Archive verification refuses a release missing any required material.

## Known risks and accepted residuals

1. **PTY delivery is not transactional.** `delivery-submitted` proves that the
   shim wrote the fixed bytes and observed submit, not that the TUI executed
   the command. A modal surface, rendering race, or host saturation can leave
   input unexecuted or interpreted differently. Version-pinned evidence is in
   `docs/release-verification-notes.md`. Operators and orchestrators must avoid
   controls while their fleet saturates the host and must not promote delivery
   to execution.
2. **Process identity is observational.** Child identity uses a recorded PID,
   `kill(pid,0)`, and the raw Darwin `kinfo_proc.p_starttime` timeval. It is an
   accident guard, not cryptographic authentication. PID/token mismatch,
   `EPERM`, and observation failures can wedge automatic recovery; they never
   become absence. Only `ESRCH` permits absence or relaunch.
3. **Same-user unlink, rebind, and record edits remain possible.** The held
   claim prevents two cooperating shims from owning one role. The advisory
   PID/`LOCAL_PEERPID` comparison detects a class of socket substitution but is
   not authentication. A same-user attacker can edit files or act directly on
   processes and tmux.
4. **Predictable runtime-root pre-creation can deny service.** The default
   `/tmp/agentctl-<uid>/v1` name is predictable. Unsafe owner, mode, type,
   symlink, or descriptor substitution causes refusal; agentctl never repairs
   or adopts the tree.
5. **Declared roots are selectable, not trusted.** `$HOME`,
   `os.UserConfigDir()`, `AGENTCTL_RUNTIME_ROOT`, and
   `AGENTCTL_STATE_ROOT` are bounded and validated inputs. A changed HOME can
   resolve a different durable tree. The uid-rooted advisory lockfile anchors
   the recorded root; disagreement refuses before alternate-tree enumeration.
6. **Dead-shim `child-starting` is manually recoverable only.** The reservation
   has no child PID and never expires. Read the recorded state root from the
   existing lockfile body, independently prove no child remains, and only then
   remove `<recorded-state-root>/sessions/<session>/roles/<role>.json`. Never
   derive that path from the reader's current environment.
7. **Environment staleness.** tmux windows inherit the server's environment,
   which may predate the invoking shell. Resolve credentials/configuration
   before launch; agentctl does not synchronize them.
8. **Template paths are caller-named reads.** `launch --from-template` opens
   the descriptor with `O_RDONLY|O_NONBLOCK`, accepts only a regular file,
   bounds input at 1 MiB without truncation, and strictly decodes before
   preflight or mutation. Symlinks are followed to their target; `-` is an
   ordinary filename and stdin is never read. Template values skip no gate.
9. **Optional presentation cleanup can race tmux auto-removal.** Kill attempts
   the previously observed exact session ID once after child cleanup. If that
   fails, only one exact-name observation of presentation `gone` permits fleet
   record removal. Present or unavailable retains the record. “Gone” and
   “removed” remain different facts.
10. **No migration dialect exists.** A pre-0.5 tmux-metadata fleet has no shim
    durable record and is not adopted. Operators should stop old fleets before
    upgrading; protocol version skew is a refusal gate, not negotiation.

## File and socket permissions

Volatile mode-`0600` lock/socket artifacts live below a descriptor-verified
mode-`0700` root:

```text
/tmp/agentctl-<decimal-uid>/v1/<session>/<role>.lock
/tmp/agentctl-<decimal-uid>/v1/<session>/<role>.sock
```

Durable mode-`0600` records live below descriptor-verified mode-`0700`
`os.UserConfigDir()/agentctl/state-v1`:

```text
<state-root>/sessions/<session>/roles/<role>.json
<state-root>/sessions/<session>/fleet.json
```

Overrides receive identical validation and confer no trust. No lifecycle path
writes inside application repositories. Atomic record writes use a complete
same-directory temporary file, file sync, rename, and directory sync. A
post-rename sync failure is typed commit uncertainty and retains the visible
record.

The skill installer separately writes only its declared user-scope skill
directories, proves ownership through its manifest, and refuses unmanaged or
modified targets unless `--force` is explicit.

## Binding constraints for the issue-182 per-agent shim

The shipped [design §15](docs/superpowers/specs/2026-08-01-agentctl-design.md#15-approved-050-per-agent-shim-contract)
supersedes the former tmux identity/delivery path. These constraints remain
release invariants:

1. **Claim, not socket.** Successful exclusive lifetime `flock(LOCK_EX)` is
   the sole ownership instant. Reclaim is lock acquisition, never probe/unlink.
2. **Honest answerer detection.** The advisory PID is compared with kernel
   `LOCAL_PEERPID`; neither side is mislabeled authentication.
3. **Private bounded roots.** Default and override roots are absolute, capped,
   private, descriptor-verified, and independently checked against Darwin's
   `sun_path[104]` limit before mutation.
4. **Closed registry.** Requests cannot carry arbitrary PTY bytes or arguments.
   Only `clear`/`compact` write fixed payloads; `observe`/`stop` never invoke
   the PTY writer.
5. **Shim enforcement.** Runtime name/version/answerer/readiness/ancestry gates
   replace tmux metadata/window/pane/process gates. `internal/target`,
   `DeliverPayload`, and production `send-keys` are removed.
6. **ESRCH-only absence.** Reservation, token, cleanup, commit-uncertain, and
   orphan states remain distinct and retain evidence whenever absence is not
   observed.
7. **Observed readiness.** The retained PTY master is sampled with `TIOCGETA`
   at `t=0`, every 50ms, and the inclusive 5s boundary. Ready requires both
   `ICANON` and `ECHO` clear while child/listener/relay remain live.
8. **Typed ownership outcomes.** Pre/post-child failures, rollback, observed
   self-target, and ancestry-undetermined have distinct exact facts and codes.
9. **Factual delivery.** Output reports only request acceptance, bytes written,
   submit, cancellation residue, signal attempt, child exit, and cleanup facts;
   it never asserts harness execution.
10. **Version-first fail-closed protocol.** `ShimProtocolVersion=1`, a four-byte
    big-endian length, 4096-byte JSON maximum, two-second frame deadline, and
    server-hello/request/response order are fixed. Version is pre-parsed before
    schema or operation fields; skew has no negotiation or migration fallback.

Payload operations and stop share one mutation gate. Stop publishes
`stopping` before waiting, lets an already-admitted payload finish and report,
refuses later payloads without a PTY write, keeps observe available, and never
attempts a second signal for `stop-already-stopping`.

Structural tests pin zero production `internal/target` imports, zero
`DeliverPayload`/`send-keys` calls, the four-field request schema, empty
payloads on control registry entries, and the single shell-composition site.

## Release verifier credential handling

`hack/release-verify.sh` uses isolated temporary HOME and tmux resources. On
macOS it may copy only `~/.codex/auth.json` after explicit consent. For Claude,
it separately offers an exact temporary `Library/Keychains` symlink to the
operator's real Keychains and states that probe harnesses can reach the login
keychain through it; with that consent it synthesizes only mode-`0600`
`{"hasCompletedOnboarding":true}` in the temporary `.claude.json`. It never
reads or copies the operator's real Claude configuration or Keychain data.

Declining the link offers an isolated login keychain and guided sign-in.
`CLAUDE_CODE_OAUTH_TOKEN` is never seeded because that path can delete the real
Keychain credential on exit; `claude setup-token` is the manual fallback for
Keychain-locked contexts.

Cleanup stops owned harnesses and the named tmux server before removing the
Keychains link, then removes the credential-bearing temporary HOME. The link
target is never a recursive-removal operand. Credential contents are never
printed or recorded in evidence.

## Reporting a vulnerability

Open a GitHub security advisory or private report to the repository owner for
anything exploitable. Include the agentctl, tmux, AMQ, and harness versions.
Risks listed above are still welcome as ordinary issues if their assessment is
wrong.

## Security updates

Security-relevant changes to validation, registry, runtime targeting, command
construction, or lifecycle evidence are called out in release notes and are
covered by the live release checklist when applicable.
