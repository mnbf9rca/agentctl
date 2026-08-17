# Issue #163 — joint proposal

**Authors:** reviewer (agentctl design) and build1 (AMQ maintainer perspective).
Converged 2026-08-17 on AMQ thread `design/issue-163`.

**Provenance.** Both of us probed the same installed binary, `amq 0.61.0`
(`brew info` → `avivsinai/tap/amq: stable 0.61.0`); build1 additionally read the
AMQ source at HEAD `0867c13f3014d748cb5248ffd08587d82368f903`. Every probe used a
throwaway root, an explicit `--root`, and a full clear of `AM_ROOT`,
`AM_BASE_ROOT`, `AM_SESSION`, `AM_ME`, `AM_ROOT_ID`, `AM_BASE_ROOT_ID`. The live
fleet root was never a target. We ran the populated-base/session-only sequence
independently and got identical results.

Supersedes both Phase-2 documents (`issue-163-design-r2.md`,
`amq-maintainer-issue-163-attack.md`), which remain as working notes.

---

## 1. Joint verdict

Implement #163 now, on a **narrower claim than either document originally
proposed**, and file one upstream ask that would let the claim widen.

- **Create** the declared fleet's AMQ session with `amq init --root <base>/<session> --agents <declared roles>` — but only for an **absent** session. Never as reconcile, never with `--force`. An existing session is **adopted read-only or refused**, never mutated (§6.1).
- **Claim** only what is observable: *AMQ session discovery reports a mailbox
  directory for every declared role.* Not that the mailbox is complete, that the
  session config names the role, or that the role is an authorized recipient —
  `session list` proves none of those stronger facts (§3).
- **Record** the strict-send gap as a stated limitation, with the matrix in §2 as its evidence, and never pass `--strict` from agentctl.
- **File** the upstream contract ask in §5. When it lands, agentctl gates on the capability and widens the claim.

This composes both Phase-2 positions rather than choosing between them.
build1's "agentctl cannot establish that declared roles are usable" is correct and
survives — as a limit on what may be *claimed*. reviewer's "the session-scoped
creator works with zero base footprint" is also correct and survives — as the
*shipping path*. Blocking implementation entirely would leave agentctl depending
on `coop exec`'s deprecated implicit creation until upstream moves, which is the
exact risk this issue exists to retire.

## 2. The composite matrix (dispute 1, settled by probe)

Setup: base root initialized with `--agents baseonly` (populated base config);
session created by `amq init --root <base>/fleet --agents baseonly,rolex`;
`rolex` is the session-only handle — present in the session config, absent from
the base config.

| Operation as/for the session-only handle | Result at 0.61.0 |
|---|---|
| `send --to rolex` (default) | **warn + delivered**, exit 0 |
| `send --to rolex --strict` | **refused**, exit 1 |
| `send --me rolex` (default) | **warn + delivered**, exit 0 — the warning names the sender handle too |
| `send --me rolex --strict` | **refused**, exit 1 |
| `list`, `list --new` as rolex | works, exit 0 |
| `drain` as rolex | works, message consumed, exit 0 |
| `drain --strict` as rolex, undrained message present | **works**, exit 0 — strict does not gate consumption |
| `receipts list` | works, exit 0 |
| `wake check --me rolex` | works, exit 0; no unknown-handle refusal |
| `who --json` | lists rolex as a normal active agent — no authority signal |
| `session list --json` | `agents: [baseonly, rolex]` |
| `doctor --json` at the session root | rolex `configured_and_discovered`, status ok |

**Blast radius: exactly one operation class — `--strict` sends.** Everything an
agentctl fleet actually does (deliver, list, drain the doorbell, receipts, wake)
works.

**Why, from source** (build1, predicted before the matrix ran and confirmed by
every cell): `send` selects the populated **base** config
(`send_mailbox.go`, `openMailboxConfigSelection`); `list`/`drain` call
`validateKnownHandles` against the **session** root, so a session-only handle in
the session config passes even under `--strict`; `receipts list`/`wait` perform no
roster validation at all.

### Two findings neither document had

**2a. The authority split is wider than `who`.** At the *same* session root,
`doctor --json` reports `Config: agents: [baseonly rolex]` and marks rolex `ok`,
while `send --strict` at that root refuses with `not in config.json agents
[baseonly user]` — the base config. Two AMQ surfaces disagree about which config
governs one root. The problem is therefore not that `who` is the wrong surface:
**no 0.61 observation surface reports the authority that strict delivery
applies.** This drives §3 and becomes its own required semantic in the ask.

**2b. Ruling 2 has a cost in `doctor`.** In a session provisioned without `user`,
`doctor` reports `user -> configured error` and the whole Mailboxes check comes
back **status error** with a repair hint. A correctly-shaped agentctl fleet
therefore makes `amq doctor` show an error until `--fix-mailboxes` creates a
`user` mailbox the fleet never asked for. Better stated up front than discovered.

**2c. Environment hygiene, pinned by exit code.** Invoking amq from inside a fleet
with partial `AM_*` leftovers exits **5**: `incomplete AMQ session pin: evidence
from AM_ROOT_ID, AM_BASE_ROOT_ID requires an exact AM_BASE_ROOT`. The full clear
plus explicit `--root` is a hard requirement, not a style preference.

## 3. Verification: what agentctl may claim, and how (dispute 2, settled)

**The fact agentctl can prove:** `amq session list --json` discovers an agent
directory under the session root for every declared role. Its implementation
lists direct children of `agents/`; it does not validate required mailbox leaves
or read the session config. Those stronger facts therefore remain outside the
per-launch claim.

**The fact agentctl cannot prove and must not claim:** that every declared role is
an authorized recipient for strict local delivery. The planner's Phase-2c framing
asked for a post-create verification proving "usable declared roles"; on 0.61.0
that proof is **impossible**, not merely inconvenient — no observation surface
exposes strict-send authority, and `doctor` and `send` actively disagree about one
root (§2a). Each candidate surface fails for a specific, evidenced reason:

| Surface | Why it cannot carry the claim |
|---|---|
| `who --json` | reports discovered mailbox presence; rolex appears as a normal active agent while strict send refuses it |
| `session list --json` | reports directory discovery, not roster membership or authority |
| `doctor --json` | reports the **session** config as the roster (§2a); silent on the base config that governs send |
| `route explain` | `validatePlannedMailbox` returns success as soon as the mailbox layout exists, before any roster-authority check |
| a probe send | mutating; a dry-run that would prove authority does not exist at 0.61.0 |

**Therefore the post-create verification is:**

1. Re-observe with `amq session list --root <base> --json`.
2. Predicate: **declared ⊆ observed** — never equality. Surplus handles are
   ignored; a later AMQ-created `user` is not drift.
3. An exit code alone never determines the post-state: `amq init` against an
   existing root exits **1 while still creating directories** (both of us probed
   this independently), while exit 0 does not prove the requested directories
   are observable. A nonzero exit is still the factual failure of that command
   and must not be hidden: agentctl re-observes for an accurate diagnostic, then
   refuses this launch even if the requested directories appeared. A retry may
   then take the already-present, structurally complete path.

**Output language is part of the contract.** Success output, spec text, and
acceptance criteria may say only that *AMQ session discovery reports a mailbox
directory for every declared role*. They must **never** say complete, registered,
authorized, strict-routable, or use any synonym implying mailbox completeness or
delivery authority. This is agentctl §1.1 applied to facts AMQ does not expose.

**Default-send usability is a compatibility premise, not a per-launch
observation.** The matrix in §2 is what establishes it, and it belongs in the
release-verification evidence for the AMQ version agentctl declares — re-run when
the floor moves, not on every launch.

**Atomicity caveat (build1, from source).** `amq init` is not exclusive: it creates
root dirs and requested mailboxes, then writes config non-force. Two concurrent
creators with different rosters can union the mailbox directories while only one
config wins, and a `declared ⊆ observed` check could pass for both. launchapi's
Apply takes an exclusive child-publication lock; `init` does not. So agentctl must
**never describe `init` as atomic, exclusive, or as exact physical creation under
races**. This proposal does not add a cross-process lock, so independent agentctl
invocations remain capable of racing; the narrowed directory-discovery predicate
can converge, but it says nothing about which config won. A real fix belongs in
the upstream contract and strengthens the ask.

## 4. Surface decisions

| Surface | Joint prescription |
|---|---|
| `amq init --root <base>/<absent session> --agents <declared>` | **The 0.61 creation path.** Sanctioned new-root creator, named by AMQ's own deprecation warning; in isolated no-race probes it produced the declared directories with the base config, base mailboxes and base `meta/` untouched |
| `amq init` against an **existing** root | **Never.** Partial mutation on exit 1, no registration |
| `--force` anywhere | **Never.** Overwrites AMQ-owned authorization state |
| `--strict` from agentctl | **Never.** It converts AMQ's warn-by-default into refusals agentctl does not own |
| `amq session create` | Cannot express an arbitrary fleet: no `--agents` at 0.61, roster comes from `.amq/launch.json` or base config, refuses an existing session |
| `amq setup` | Operator onboarding only. agentctl may *name* it in a refusal; never invoke it |
| `amq coop init` | Legacy plumbing; cannot extend; not a second onboarding flow |
| `amq coop exec` | Execution wrapper **only, after** provisioning. Tests must prove the session already exists and that no implicit-creation warning is emitted |
| Go `launchapi` | Closest correct machinery, deferred: a new third-party Go dependency requiring justification under agentctl's standard-library-first rule; two AMQ versions owning one filesystem (linked library vs installed CLI used by `coop exec`); in-process-only capability negotiation; and the base-config precedence in §2 is unresolved for it too. Revisit when the §5 contract lands |

## 5. The single upstream ask

Expose a **provisioning-only, versioned session-roster contract through the
installed `amq` binary**, backed by the existing launch Apply roster machinery.
Acceptable shape:

```text
amq session roster prepare --root <base> --session <name> --agents <exact-desired-csv> --json
amq session roster apply   --root <base> --session <name> --agents <exact-desired-csv> --subject-digest <digest> --json
amq capabilities --json
```

Required semantics:

1. `prepare` is zero-write; `apply` recomputes and accepts only the exact prepared
   subject, is **concurrency-safe and exclusive** (the `init` race in §3 is the
   motivating case), and either creates the missing session or reconciles an
   existing one.
2. Desired agents are caller-supplied for this operation. No implicit `user`.
3. It provisions or repairs only the requested session mailboxes and the AMQ-owned
   session authorization needed to make every desired handle valid for **strict
   local delivery**. It never creates base agent mailboxes, rewrites the base
   roster, or touches AMQ project declarations.
4. Extras and their history are preserved and returned as extras; they are never
   mutation targets and never fail the operation.
5. A populated base config cannot silently shadow the result. Either AMQ makes
   session authorization authoritative for the target session, or `apply` returns a
   typed conflict/unsupported result **before** mutating.
6. Both phases return versioned JSON carrying outcome and reason code, target and
   physical root identity, **the active authorization source for that root**,
   desired/present/missing/extra, and the exact base and session effects. Human
   stderr is not part of the contract.
7. **`doctor`, the roster surfaces, and `send` must agree on authority
   semantics.** §2a shows they do not today: at one session root, `doctor` reports
   the session config as the roster and marks a handle `ok` that `send --strict`
   refuses using the base config. Whatever the ruling on precedence, one root must
   have one answer, and every surface that reports roster state must report the
   active authorization source alongside it.
8. `amq capabilities --json` advertises a stable feature such as
   `session_roster_provision_v1` with its contract semver and request/result
   versions, so a downstream can negotiate against the **installed binary** rather
   than parse `amq --version`.
9. The contract is documented as safe for external orchestrators that own team
   composition but not AMQ storage, with hostile-path, symlink-swap,
   concurrent-apply, existing-history, foreign-config and zero-base-mailbox tests.
10. **A documented, stable, opaque physical-root identity** that an orchestrator may
   persist and later compare to prove it is looking at the same tree it provisioned.
   `base_root_id` exists today but sits outside the documented `env --json` v1 field
   list, and its `v1:<platform>:<device>:<inode>` form changes on a legitimately
   restored or moved tree — so it cannot carry that weight as it stands. §6.1's
   crash-recovery adoption is the concrete downstream use: without such a token, an
   orchestrator can prove *which session folder it recorded* but not *which AMQ tree
   that folder now belongs to*.

Adding `--agents` to `session create` alone is insufficient: existing-session
reconcile, atomicity, and verification would all remain unsolved.

## 6. The rulings package for the operator

| Ruling | Joint position |
|---|---|
| **1 — Extend an existing root non-destructively** | **Mechanism withdrawn; principle re-scoped, then completed by the operator with a recovery case (see §6.1).** Both of us probed `amq init --agents <superset>` against an existing root at 0.61.0: it **exits 1 while still creating the mailbox directory** and leaves the roster unchanged. An unregistered mailbox is not registration, and a directory observed afterwards does not make the handle usable. Re-scope to: **the declared session is created whole, adopted read-only, or the launch refuses.** For this interim design, "whole" means only that session discovery reports every declared role; it does not imply complete mailbox leaves or authorization. Extension disappears as an agentctl action: agentctl never mutates an existing AMQ session. No-force and no-delete stay. |
| **2 — Fleet shape is the whole contract; no auto `user`** | **Stands**, with wording tightened and two consequences recorded. Wording: agentctl's *desired* set is exactly its declared roles; it never asks AMQ to create `user`; physical extras — including a `user` AMQ creates later — are observed, preserved, and excluded from the desired set. Omitting `user` means agentctl does not request it, **not** that it can never exist. Consequences: (a) sends to `user` still validate and can mint its mailbox on first delivery, so omission does not prevent later presence; (b) **`amq doctor` reports status error for the missing `user` mailbox** in a fleet-shaped session until `--fix-mailboxes` creates it (§2b). Declaring `user` would avoid that diagnostic cost, but contradicts the chosen desired set. |
| **3 — Session-scoped, not base** | **Stands, and is achieved in the isolated 0.61 probes.** Creating the session left base config, base mailboxes, and base `meta/` untouched; the only base-level effect was the session child itself. (launchapi would additionally leave an advisory `meta/launch/create-*.lock`.) Two clarifications: sessions are structural, not registered, so this is the smallest observed base effect; and base-level *observability* is by design — sibling scans and `doctor --ops` surface every session — so the goal is "no base roster/mailbox/metadata mutation", not invisibility. |
| **3a — The daily cost of ruling 3 (needs the operator's explicit acceptance)** | Stated exactly: with a populated base config, **every default send *to or from* a session-only handle emits an unknown-handle warning, and every `--strict` send to or from it is refused, exit 1**. Consumption, listing, receipts and wake are unaffected (§2). Options: **(a) accept it, never invoke strict send from agentctl, and file the §5 ask — our joint recommendation**; (b) also register handles in the base config, which contradicts ruling 3 and pollutes the operator's roster; (c) require a base-configless root, which works but is code-only, at-risk, and documented nowhere. We recommend (a), recorded in the spec as a known limitation with §2 as its evidence — a deliberate operator decision, not hidden debt. |

### 6.1 R1 final — the recovery case (operator amendment, 2026-08-17)

The operator completed R1 after accepting R2 and R3a. A crash or reboot destroys
agentctl's runtime state while the AMQ session folder and its queued mail survive,
and relaunch must resume prior agent contexts with mailbox continuity. Three cases:

**(a) Session absent → create whole**, exactly as §4 and §7 describe.

**(b) Session exists and is provably ours → adopt, read-only.** Ownership evidence
is agentctl's own durable fleet record for that session — it survives reboot while
runtime state does not, is stored 0700/0600 with no-follow and UID checks, and is
already the authority `ShimRelauncher` consumes (build1). The adoption check is:

1. strict `Read(session)` of the state-v1 record, schema and session binding
   validated;
2. the incoming declared roster, normalized, **exactly equals** `record.Roster` on
   a whole-fleet launch; a relaunch derives its declared roster *from* the record
   (build1 — roster equality is the ownership test, distinct from the shape test);
3. resolve the AMQ session folder under the record's **stored project directory**,
   not the current working directory;
4. one observation-only listing, requiring a mailbox directory for every recorded
   role — the same narrow predicate as creation, extras ignored under R2;
5. **no `init`, repair, send, drain, or touch of any kind** during adoption.

The claim adoption may make is word-for-word the creation claim (§3), no stronger.

**(c) Session exists and is not provably ours → typed teaching refusal.** The
refusal renders the observed facts and then **enumerates the operator's ways out**,
following the in-tree precedent at `cmd/agentctl/shim_results.go:369` where the
bare-session attach refusal lists the attachable roles as runnable commands:

```
agentctl: refusing to launch session NAME; an AMQ session folder exists at PATH
that agentctl cannot prove it owns (FACTS); choose one:
  agentctl launch --session OTHER_NAME ...     # launch under a different name
  amq list --session NAME                      # inspect what is in the folder
  agentctl launch --session NAME --adopt ...   # assert ownership yourself

  or: inspect and preserve any mail, then remove the exact folder PATH manually
      — deletion destroys every queued message in it.
```

The removal remedy is deliberately **not** a runnable `rm -rf` template: it names
the exact resolved session folder and states the consequence, so the operator
performs the destructive step themselves rather than pasting a command whose blast
radius depends on a variable being right (build1). The other three remedies remain
runnable commands.

Five distinguishable refusals, each rendering its own facts (build1):

| Observed | Rendered facts |
|---|---|
| folder exists, durable record absent | orphan or foreign; manual removal required before a create |
| record exists, folder absent | recorded fleet whose AMQ session is gone; never silently recreated |
| roster mismatch | the record's roster *and* the requested roster, side by side |
| mailbox directories missing | the exact roles missing from the folder |
| record unreadable or unsafe | the existing strict record error, unchanged |

**The `--adopt` flag** is in scope as part of this amendment. It is *operator
authority substituting for absent evidence* — never agentctl guessing. It:

- performs the **same read-only shape check** and still refuses if the shape does
  not fit (a missing declared role is a shape failure, not an ownership question);
- **mutates nothing** in the AMQ tree, exactly as evidence-based adoption;
- records in the fleet record how ownership was established, as a **new strict-record
  field with exactly two values — `evidence-derived` and `operator-asserted`**. This
  follows the repository's landed factual/provenance conventions; it does not reuse
  an existing vocabulary. `internal/fleet/types.go`'s `Provenance` today labels
  launch-template sources only (`template`, `flag override`, `flags`), and
  `internal/status/status.go:70` calls the durable roster "operator-claim
  provenance" in a comment — neither is a persisted ownership-provenance field
  (build1's correction to an overclaim of mine).

**Reading to confirm with the operator:** `--adopt` covers *absent* evidence (the
records-lost-but-it-is-mine case the operator named). It should **not** override
*contradicted* evidence — a record that exists and names a different session
identity is a positive signal that the folder is something else, and letting a flag
silence a true negative would defeat the purpose. Our proposal: absent evidence →
`--adopt` permitted; contradicted evidence → refuse regardless, with the other
three remedies offered.

**Continuity is probed, not assumed** (reviewer, amq 0.61.0, throwaway root,
explicit `--root`, full `AM_*` clear):

1. `coop exec` into an **existing** session emits **no deprecation warning**,
   exit 0 — the clean post-provisioning execution path the joint doc promises.
2. Two messages left undrained in `agents/<role>/inbox/new` survive relaunch
   **byte-identically** (same digest before and after).
3. After adoption a fresh send lands and `drain` returns all three in order —
   mailbox continuity across the crash boundary, not merely directory survival.

Consistent with build1's source reading: `coop exec` never drains. It resolves the
session and root, configures wake and presence, builds `AM_ROOT`/`AM_ME`, then
exec-replaces itself with the harness — so a queued `inbox/new` file is consumed by
the agent's own later drain, never by adoption or by startup. The acceptance test
therefore fingerprints `inbox/new` before adoption, after the read-only check,
after `coop exec` startup, and finally drains to prove the original message both
remained and was deliverable.

**Launch ordering, and the orphan it creates (build1).** `ShimLauncher` today writes
the durable record *before* starting shims. Under R1 the AMQ provisioning must move
*before* the durable record is written. So a crash **after `init` but before the
record** leaves an intentionally unowned orphan — case (c), refuse, manual removal
of that exact folder. A crash **after the record** is adoptable if the shape passes.
This is the operator's stated edge, now located exactly in our ordering rather than
described in the abstract.

**Residual limitation, stated and deliberately not closed here.** The record
(`<stateRoot>/sessions/<session>/fleet.json`; stateRoot defaults under
`UserConfigDir`, the runtime root under `/tmp`) carries `Session`, `Directory`,
`Presentation`, `Roster`, `Roles` — but **no AMQ root identity**. Resolving the
folder under the record's *stored* directory (step 3) closes most of it: adoption
cannot wander to another project. What remains: the AMQ base root resolves *from*
that directory, so if the directory's AMQ binding changes between creation and
adoption — an `.amqrc` appearing or changing, or a change to the eligible global
configuration or root — the same stored directory can resolve to a different tree,
and "provably ours" fails quietly rather than loudly. (`AM_ROOT` is *not* one of
these paths: §7 step 0 clears `AM_*` before resolution, so following that rule
excludes it — build1.)

I proposed persisting AMQ's `base_root_id` as an adoption gate. **build1 rejected
that and is right**, on the rule this document already states: `base_root_id` is
emitted by 0.61.0 and documented for shell-mode output, but it is **not in the
documented stable `env --json` v1 field list** (build1, reading
`docs/adr-layer-extensions.md` in the source clone; the table is not shipped with
the brew artifact, so this rests on their reading). Its non-Windows form is
`v1:<platform>:<device>:<inode>`, so it also changes on a legitimately restored or
moved tree, which would turn ordinary recovery into a refusal. Building a schema
field and a migration-refusal policy on an undocumented token would have
contradicted our own load-bearing rule, and it would have invented a policy the
operator did not rule on. **Withdrawn.** The interim proof stays exactly as the
operator settled it: the state-v1 record naming session and roles, plus the narrow
read-only folder-shape check. A stronger binding belongs in the §5 upstream ask as a
*documented* opaque physical-root identity, not in this interim design.

The ownership-provenance field remains a genuine addition to the strict record, and
it lands alone rather than alongside a root-identity field.

## 7. Implementation sketch

Preflight, at `launch` and `run`, after value validation and before any tmux or
runtime mutation:

0. **Resolve, never inherit.** `amq env --json` from the project directory with
   `AM_*` cleared → `base_root`. Every later call passes `--root` explicitly.
   (`amq init --help` shows `-root` defaulting to the *caller's* live session root
   when `AM_*` is set; and a partial clear exits 5 — §2c.)
1. **Observe.** `amq session list --root <base> --json`.
2. **Act on the gap only.**
   - base root unresolvable / not a delivery root → refuse, naming `amq setup`.
   - session absent → `amq init --root <base>/<session> --agents <roles>`; always
     re-observe afterward. If `init` returned nonzero, refuse while reporting the
     observed post-state; do not hide the command failure or start roles.
   - session present, declared ⊆ observed, **and ownership provable** from the
     durable fleet record (§6.1b) → **adopt read-only**; run no *mutating* AMQ
     command — the observation calls of steps 0–1 are the only ones issued.
   - session present, declared ⊆ observed, **ownership not provable** → refuse with
     the teaching remedies of §6.1c, unless `--adopt` was passed, in which case
     adopt and record operator-asserted provenance.
   - session present, declared ⊄ observed → refuse, naming the missing handles and
     the remedies; a shape failure is refused **even with `--adopt`**; agentctl
     does not modify an existing session.
3. **Verify after a create attempt** by re-observation, predicate declared ⊆
   observed. Success requires both `init` exit 0 and the predicate; otherwise
   refuse with the command status and observed present/missing handles.

Argv is closed and carries only validated identifiers:

```
amq env --json
amq session list --root <BASE> --json
amq init --root <BASE>/<SESSION> --agents <role1>,<role2>,...
```

Grammar is compatible today: agentctl session names match `^[a-z0-9][a-z0-9_-]*$`
(≤32 bytes), AMQ canonical session names are `[a-z0-9_-]+`, AMQ handles
`^[a-z0-9_-]+$` with no leading `-`. agentctl's grammar is a strict subset on both
axes — pin it with a test, not a comment.

Typed rows (agentctl §15.8 register): `amq-root-unresolved` (6),
`amq-base-absent` (5), `amq-session-create-failed` (6), `amq-session-incomplete`
(6), `amq-session-shape-conflict` (5), `amq-observation-failed` (6), and for the
recovery case `amq-session-unowned` (5) — the teaching refusal of §6.1c, which
renders the observed evidence facts and the four remedies. All argv asserted
exactly against the fake `Runner`. Adoption, evidence-based or `--adopt`, issues
**only the read-only observation calls** — `amq env --json` and
`amq session list --root <base> --json` — so its test asserts exactly those argv,
the **absence of any mutating call** (`init`, repair, send, drain, touch), and the
recorded ownership provenance. The contract is zero AMQ-tree mutation, not zero
subprocesses (build1's correction: §6.1 step 4 and §7 steps 0–1 both make calls, so
"no AMQ command at all" was self-contradictory).

**Compatibility before the upstream handshake exists.** This design is verified
only against AMQ 0.61.0 and declares that support floor, but it does not parse a
version string. Before tmux mutation, failures of `env`, `session list`, or `init`
map to their command-specific typed rows above; agentctl must not call such a
failure an unsupported capability when AMQ exposes no stable discriminator.
`amq env --json` carries `amq_version`, which **is** in the documented v1 field
list (build1) — it is nonetheless diagnostic-only here, useful as context and for
an upgrade hint (`brew upgrade avivsinai/tap/amq`), because a version string is not
a capability discriminator: a patch release could backport the contract and a
future release could withdraw it. Once §5 lands, gate only launch/run
provisioning on `session_roster_provision_v1` before tmux mutation; other agentctl
commands remain unaffected by an old AMQ.

**SECURITY.md clause.** agentctl never creates, repairs, or deletes AMQ state
directly; it invokes AMQ's sanctioned creator with an explicitly resolved
`--root`, passing only validated role identifiers, never `--force`, never
`--strict`. For an absent session it requests exactly the declared handles; it
does not request base config, base mailbox, or base metadata mutation, and the
session child is the only observed base-level effect. It treats surplus and
reserved handles as observations, verifies only the directory-discovery predicate
by re-observation, and refuses rather than inferring success from an exit code. Any
future per-fleet bookkeeping inside an AMQ tree goes in the reserved
`<AM_ROOT>/extensions/agentctl/` directory.

## 8. What we did not settle

- Whether AMQ intends session authorization to become authoritative for strict
  delivery — §5 semantic 5 is the ask, not a prediction.
- Whether `init`'s partial mutation on exit 1 is a bug against AMQ's own
  fail-closed posture. We report it; upstream rules on it.
- The `init` concurrency race (§3) is stated but not eliminated by this design;
  a real cross-process fix belongs in the §5 contract.

---

## SECURITY.md delta — MUST ride the first implementation PR of #163

SECURITY.md is guarded as an operator document stating **current** truth
(`hack/securitydoc_test.go`), so this text may not land while only the
specification has. It is parked here, ready to apply, and the §15.12.8 release
obligations require it to accompany the first implementation PR.

Add to the threat list, after the dependency entry:

> - **Provisioning a fleet inside the operator's message store.** agentctl never
>   writes in an AMQ tree itself: it invokes AMQ's own creator with an explicit
>   root, leaves the base configuration, base mailboxes, and base metadata
>   unchanged, verifies by re-observation, and adopts an existing session
>   read-only or refuses (spec §15.12).

Add to the residual list:

> **Ownership binds a directory, and a directory is not a permission.** If a
> directory's resolved AMQ root changes between creation and adoption, ownership
> evidence can be wrong without being loud; `--adopt` substitutes a human claim
> where evidence is absent, and the record says which. agentctl claims only a
> directory per declared role; stricter delivery acceptance is AMQ's.

Two constraints on applying it. The claim is **not** that the base root is
unchanged — creation adds the session child, which is a base-level effect — but
that the base configuration, mailboxes, and metadata are. And the file's word
budget was at 1553 of 1650 when this was drafted: fitting this text needs
compression elsewhere, and the earlier attempt weakened two release-verification
qualifiers ("three-confirmation" live smoke, "exhaustive" Task 8 checks) to make
room. Those qualifiers are load-bearing; find the words somewhere else.
