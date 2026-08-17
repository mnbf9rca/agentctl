# Issue #182 — the structural change: move identity off tmux and into a per-agent PTY shim

Status: **options paper for the #182 design session. Decides nothing; not an approved contract.**
When the session decides, the chosen option becomes amendments to the
[approved design spec](2026-08-01-agentctl-design.md) and this paper is superseded.
Refs [#182](https://github.com/mnbf9rca/agentctl/issues/182).

Evidence: spec + SECURITY.md read in full; metadata write/read-site trace on `main`; AMQ 0.60.0
review; empirical probes on throwaway tmux servers only (tmux 3.7b, this machine, 2026-08-09);
harness control-surface survey including a generated `codex app-server` schema dump. Claims from
a single unreproduced observation are marked.

## 1. The problem

Role identity and the process baseline are stored as tmux **window** options, but a window is a
presentation object the operator legitimately rearranges. In the #182 incident, joining four
role panes into one window destroyed every role window — and the identity metadata died with
them — while every agent process survived untouched. `status` truthfully reported the whole
roster `missing`; `clear`/`compact` correctly refused; the management surface was gone.

The mismatch is structural: delivery already targets the pane (§6.2 step 7), and the pane
travels with the process — the window is only the resolution path, and it is the fragile part.
Documentation-only remedies ("don't join panes") were rejected as an endpoint: the fix must make
the failure impossible, not warn about it.

## 2. The change: a per-agent PTY shim (Option S)

Move identity out of tmux entirely, to the one boundary agentctl already controls: the moment it
spawns the agent.

**Mechanism.** Each role's pane runs a small agentctl shim instead of the harness directly. The
shim creates a nested PTY, spawns the unchanged `amq coop exec … HARNESS` command on it, and
passes every byte through untouched in both directions, forwarding resize and termios state.
The operator's existing terminal — tmux pane, Terminal.app tab, plain SSH — still renders
everything, so there is no terminal emulator, no daemon, and no viewer to build; this is
`script(1)`/dtach-scale plumbing, not a multiplexer.

- **Identity is the shim process, arbitrated by a kernel lock.** Each shim claims its role by
  acquiring an exclusive `flock` on a per-role lockfile and holding it for its lifetime; the
  per-role unix socket (same `0700` runtime directory, keyed by session) is bound only while the
  lock is held, and reclaim after a crash is lock acquisition, nothing else. The lock is the
  liveness test: the kernel releases it atomically on process death including SIGKILL (verified:
  `EWOULDBLOCK` while the owner lives, immediate acquisition after `kill -9`). An earlier draft
  claimed bare socket bind was the atomic claim; adversarial review refuted that (verified: a
  SIGKILL'd shim's socket file blocks rebind, and unlink-based reclaim is check-then-act and can
  silently orphan a healthy shim), so the lock, not the bind, is the claim.
- **Delivery.** `agentctl clear ROLE` connects to the role's socket; the shim types the closed
  payload into the PTY it owns. The registry stays hardcoded and argument-free; the shim accepts
  operation names, never text. The shim is the sole writer to that PTY and observes what it
  typed. No delivery is accepted until the harness's tty has left cooked mode (verified: the two
  nested raw-mode transitions race at startup and corrupt early bytes; a settle barrier in the
  spirit of spec §8's baseline poll is required).
- **Process identity improves.** The shim knows its child PID directly — a parent-of relation
  instead of today's launch-time `ps` baseline and equality comparison.
- **Layout-proofness of the identity/delivery plane is total.** Join, break, swap panes; drop
  tmux entirely. The shim and harness travel together as ordinary processes; the
  identity-and-control half of the #182 failure class becomes meaningless, and tmux stops being
  a requirement for identity and delivery. Viewing is untouched: layout remains whatever the
  operator's terminal does, so the incident's cosmetic half (four roles crammed into one
  window) is out of S's scope.

**Why ownership is the right shape here.** AMQ deliberately owns nothing because it coordinates
an open set of peers it did not create, over a latency-tolerant durable mailbox — detachment is
its mandate. agentctl is the mirror image: a closed set of processes it launched itself,
controlled by imperative, time-bound operations that §1.1 requires it to account for factually.
Ownership at the launch boundary is natural for agentctl exactly where it would be a layering
violation for AMQ. The two stay complementary: AMQ carries messages between agents; the shim
carries control of an agent agentctl owns.

**Costs, honestly.**

- A resident shim in every agent's process chain, with a per-agent (not fleet-wide) blast
  radius — but shim death does **not** reliably end the harness: SIGHUP is delivered, and
  whether it terminates is the harness's choice (verified: a child ignoring SIGHUP survives its
  shim, orphaned and unreachable). Reporting such a role `missing` while its process burns
  tokens would be a §1.1 violation worse than anything in the current design, so S must either
  prove per-harness SIGHUP termination with version-pinned evidence or define an explicit
  orphan state and a relaunch rule that cannot double-launch beside one.
- The spec-§8 process-identity policy is redesigned around shim-held child PIDs; the pane's
  root process becomes the shim.
- A new control-socket surface for SECURITY.md: namespace, permissions, forged-socket posture
  (same-user remains out of the threat model; identity evidence stays advisory).
- PTY syscall plumbing — verified feasible even in pure stdlib (a working prototype on this
  machine covers `ptmx` open, controlling-tty assignment, job control, SIGWINCH forwarding,
  and exit propagation). `golang.org/x/sys` is the natural implementation choice over
  hand-copied ioctl constants in the frozen `syscall` package; there is no repository rule
  against it (the maintainer has confirmed the absolute "no third-party dependencies" wording
  was over-hardened instruction text; SECURITY.md's "Third-party build dependencies" section
  now governs the posture, and the module graph already carries `jsonschema/v6`).
- The `AF_UNIX` path ceiling is 104 bytes on Darwin (verified): macOS `$TMPDIR` alone is ~50,
  and session/role names have no length cap today, so the runtime directory must be short by
  construction and `internal/config` gains name-length caps — a behavior change needing its own
  spec amendment.
- Spec analogs must be authored, not ported: a §6.6-equivalent naming the single ownership
  instant among fork → lock → bind → PTY → exec → settle and what rollback destroys at each
  failure point, a §6.3-equivalent state vocabulary (including socket-present/connect-refused
  and the orphan state above), and a §9 exit-code map.
- launch/status/relaunch/kill reworked to speak to shims; the shim, not `internal/target`,
  becomes the enforcement point for socket callers; release-verification updates.
- Delivery is still keystrokes into a TUI: SECURITY.md residual 1 (non-transactional delivery,
  popup timing) carries over unchanged.

**Adversarial review outcome (2026-08-09, three lenses, empirical prototypes).** The PTY
mechanics, the closed wire protocol, first-claim atomicity, parent-of process identity, and
layout-proofness of the identity plane all survived attack. The bare-socket claim story did not
and is replaced by the flock design above; the orphaned-harness risk, the Darwin path ceiling,
the startup byte-corruption race, and the missing §6.6/§9 analogs were all found here and are
now recorded as binding constraints in SECURITY.md ("Binding constraints for the issue-182
per-agent shim"), which any S implementation PR must satisfy. S's forged-claim analog — a
same-user process unlinking and rebinding a role's socket — is less enumerable than A's forged
pane claims and is written down there as a residual with its detection contract.

## 3. The alternative: pane-scoped identity (Option A)

Stay inside tmux and move the `@agentctl_*` stamps from window scope to pane scope
(`set-option -p`). Empirical core (tmux 3.7b, throwaway servers): pane options survive
`join-pane`, `break-pane`, `swap-pane`, `move-window`, and the death of their original window —
the exact #182 event — and `list-panes -s` enumerates a session's roster in one call. (Pane
options are believed to require tmux ≥ 3.0; not verified here.) Delivery already targets panes,
so A moves resolution onto the object delivery trusts.

A is sound but inherits three problems S answers structurally — though the adversarial review
of S narrowed the earlier "dissolves by construction" claim: S's own role claim needed the
flock design (§2) rather than coming free, and S has a forgery residual of its own, differently
shaped:

1. **Inheritance (verified).** Pane-scoped reads return a same-named *window* option when the
   pane lacks its own. A single stamp call accidentally left at window scope is invisible under
   every one-pane-per-window test and misfires exactly during a #182-style join, when an
   unstamped bystander pane inherits a claim. Requires a named write-scope test and an
   integration replay with an extra never-stamped pane.
2. **No atomic role claim.** Today's concurrent-relaunch tie-break rests on window names being
   set atomically at creation (`new-window -n ROLE`); pane claims land in a separate later
   call, so the race closure must be redesigned — most plausibly by retaining window-name
   collision as a documented creation-time exception.
3. **Forged claims get cheaper.** One silent `set-option -p @agentctl_role X` on any existing
   pane both erases that pane's real claim and makes role X ambiguous — two roles degraded with
   no footprint in the window list. Same-user stays out of the threat model, but this is a new
   SECURITY.md residual, and ambiguity refusals must render the contending pane IDs.

**Role:** the fallback if S fails its adversarial review. A fixes the rearrangement class but
keeps identity inside tmux metadata; S removes the coupling that produced the incident.

## 4. What any change must preserve

- §1.1: every output is a factual claim; delivery is never reported as execution unless
  execution was observed.
- No shell; the single shell-interpreted string stays at the §12.1 site, built by `shellq` from
  validated tokens.
- The payload registry stays closed and argument-free; no path for caller-supplied text to
  reach the agent's input.
- Exact resolution once, an unambiguous handle thereafter, fail closed on more than one match.
- Verification never heals or re-stamps identity.
- Same-user actions stay out of the threat model; identity evidence stays advisory, not a trust
  boundary.

## 5. Interim measures while the structural fix lands

Shippable in 0.4.x, explicitly not the fix: a `status` note naming the observable aggregate
(roster entirely missing while one unmanaged window in the session holds a pane count equal to
the roster size — worded as observation, never causation), and recovery documentation at the
verified granularity: per-role `relaunch` already works for roles whose windows vanished
(`requireAbsentWindow` requires zero matching windows); only the window that absorbed the
joined panes needs its stray panes closed, or session kill+launch. That granularity is
established by code reading; a live throwaway-tmux replay of the incident is required before
the documentation ships. The join-pane warning and the grouped-sessions viewing recipe
(verified: session options are not shared across a group, so a viewer session cannot pass any
managed gate) may ride along as interim guidance.

## 6. Considered and rejected

- **Documentation as the endpoint.** Rejected by the maintainer: warn-and-recipe is "don't
  hold it wrong"; the fix must remove the failure mode.
- **Redundant pane tags** (window identity kept authoritative, pane-scoped copies for
  diagnosis). Upgrades the status heuristic to per-pane fact but gates nothing and fixes
  nothing; at most an interim add-on, not an endpoint.
- **Fleet-wide PTY supervisor (Option C).** Taking the tmux server's seat means inheriting its
  rendering and persistence jobs: a virtual-terminal screen model (from scratch under the
  stdlib constraint), a daemon whose lifetime is the fleet's lifetime, viewers, hot-upgrade
  handoff. An order of magnitude beyond S, for value S already delivers except headless or
  fully detached operation. Revisit only if that becomes a goal.
- **Harness-native control planes (Option B).** `codex app-server` is a real local RPC surface
  with true clear/compact/interrupt calls — execution acknowledged, a genuine §1.1 upgrade —
  but the subcommand is vendor-labeled experimental, its listen transport is
  configuration-dependent (`ws://IP:PORT`, remote-control toggles) and would need pinning to a
  same-user unix socket, and Claude Code has no local equivalent: hooks can observe or block
  context operations but not trigger them, and Remote Control is cloud-mediated — a different
  trust boundary. Revisit per harness if the surfaces stabilize and converge.
- **State-file registry.** A record, not an observation: it goes stale where a bound socket
  proves liveness on every connect. Strictly worse than S's mechanism.
- **Heal-on-verify / re-stamp on read.** Permanently rejected: healing converts detection of a
  wrong process into acceptance of it.
- **TIOCSTI injection.** Verified on this machine: `EACCES` against any tty that is not the
  caller's controlling terminal, so it requires a resident sidecar anyway — and S's nested PTY
  is the strictly better sidecar, needing no deprecated ioctl (Linux ≥ 6.2 can disable
  TIOCSTI system-wide; OpenBSD removed it).
- **Terminal-emulator APIs** (iTerm2, kitty, WezTerm, AppleScript, VS Code): one integration
  per product, no common identity model, no headless story.
- **Signals.** Both harnesses forward only SIGINT/TERM/HUP; nothing context-related; the one
  upstream proposal was declined (codex#16060).

## 7. Open questions for the design session

The review converted most of the previous open questions into the binding constraints recorded
in SECURITY.md. What genuinely remains open:

1. The self-target guard's S mechanism: `TMUX_PANE` does not exist without tmux, and an
   env-var substitute inverts SECURITY.md residual 5 (advisory variables must never gate
   targeting). Candidates: process-ancestry walk from the caller to the role's lock-holding
   shim, or an explicit, documented decision to drop the guard for tmux-less operation.
2. The orphan-harness answer: version-pinned SIGHUP-termination evidence per harness, or an
   explicit orphan state plus a relaunch rule that cannot double-launch beside one (which of
   the two S implements is a design choice; SECURITY.md constrains it to be one of them).
3. Attach and viewing: the fleet remains launchable with and without tmux — what does `status`
   count in each mode, and does `attach` gain a no-tmux message or stay tmux-only?

Resolved since the review (maintainer, 2026-08-09): sequencing — this work is the next release
after 0.4.1, interim measures riding with it, not ahead of it; and coexistence — no
migration or dual-dialect support across the tmux-metadata → shim transition (single-operator
install, flag-day acceptable). Version *skew* between CLI and shim is still gated fail-closed:
SECURITY.md constraint 10.
