# tmux-less fleet launch — design notes

Status: **non-normative design notes**, written to ground the spec that follows.
Companion to the README's proposed UX. Refs
[#182](https://github.com/mnbf9rca/agentctl/issues/182) and
[#196](https://github.com/mnbf9rca/agentctl/issues/196).

## The problem

Issue #182's stated goal was that tmux stop being a requirement. What landed on
`main` is that tmux stopped being *identity and control transport* — but it is
still a hard launch requirement. `internal/preflight/preflight.go`
declares `required := []string{"tmux", "amq"}` unconditionally, and `launch`
always creates a presentation. Per-role `run` in N hand-opened terminals is a
workaround, not a fleet launch.

## The goal that reopens a rejected option

The [options paper](../specs/2026-08-09-issue-182-identity-delivery-options.md)
§6 rejected a fleet-wide PTY supervisor (Option C):

> Taking the tmux server's seat means inheriting its rendering and persistence
> jobs: a virtual-terminal screen model (from scratch under the stdlib
> constraint), a daemon whose lifetime is the fleet's lifetime, viewers,
> hot-upgrade handoff. An order of magnitude beyond S, for value S already
> delivers **except headless or fully detached operation. Revisit only if that
> becomes a goal.**

Fully detached operation is now the goal, so exactly that much reopens — and no
more. Everything that made C expensive is still refused: no virtual-terminal
screen model, no daemon whose lifetime is the fleet's, no viewer application,
no hot-upgrade handoff.

The reason this does not become Option C is that the shim **already** owns each
role's PTY and is already resident per role. Detached operation is therefore
*subtraction* — stop requiring tmux to host what the shim already holds — not a
new supervisor. Attach is a byte relay over a stream, and `internal/ptyx`
already implements a byte-preserving bidirectional relay for exactly this PTY.

Every other §6 rejection stands unchanged, including harness-native control
planes (Option B), state-file registries, heal-on-verify, and TIOCSTI.

## Recommended UX, and what was rejected

**Detached is the default; `--tmux` opts in. Operator-ruled 2026-08-13.**
`agentctl launch` with no presentation flag starts every role detached, and a
template with no `presentation` field means detached. `--detached` remains
accepted as an explicit synonym so a command can state its mode out loud.

The ruling follows the plain reading of #182: if tmux is optional, the default
path must be the one that does not need it. Keeping tmux as the default would
have left the headline promise true only for operators who knew to opt out.

*Rejected — and this was the earlier recommendation in this document, now
superseded:* keeping tmux as the default and adding `--detached` as an opt-in.
The reasoning was that flipping the default is a behavioral break for existing
users and deserved its own decision rather than arriving inside this one. The
operator made that decision directly, which is the appropriate way to resolve
it — and the premise turned out not to hold: the release carrying the shim
lifecycle is unpublished and stays unpublished until tmux-less launch lands, so
no published build ever defaulted to tmux and there are no existing users to
break. The flip is the release's intended shape, not a change to it.

*Also rejected:* auto-detecting tmux (a fleet's shape should not depend on what
happens to be installed — the same command on two machines would produce
materially different fleets, and a tmux that exists but is unreachable would
silently change behavior); a template-only field with no flags (hides a mode
choice in a data file).

**`attach` takes an optional role.** From the operator's seat both forms answer
"show me this fleet", and the bare form's refusal can teach the role form by
listing roles. *Rejected:* a separate `view`/`console` verb (two names, one
intent); making `attach ROLE` the only form (discards the working iTerm2 flow).

**No detach key in v1; every byte passes through. Operator-ruled 2026-08-13.**
The attach relay intercepts nothing. To stop viewing, the operator closes the
terminal, and the role keeps running because the shim already survives client
disconnect by design.

The reasoning that removed the key:

- **`Ctrl-C` must reach the harness.** It is how an operator interrupts the
  agent's current turn. Any interception scheme has to carve it out, and a
  carve-out list is a thing that can be got wrong.
- **`/exit` is role shutdown, not view exit.** There was never a harness-level
  gesture that means "stop watching", so the key was not filling a gap the
  harness left.
- **Closing the tab already detaches.** The key bought nothing that tab-close
  did not, at the price of one intercepted byte plus a standing obligation to
  re-check both harnesses' key bindings on every version bump.

*Rejected — this document's earlier recommendation, now superseded:* a single
**Ctrl-\\** detach key on the dtach precedent, chosen as one key a tired
operator can remember and deliberately not a sequence, since a two-key sequence
must buffer the first byte and so either delays input or leaks it to the
harness. That reasoning was sound for a design that needed a key; the ruling is
that v1 does not need one. *Also rejected:* a tmux-style prefix (invents a
second modal vocabulary); `Ctrl-D` (belongs to the harness).

A future **opt-in** detach key remains a compatible later addition: default-off,
it would not change the v1 contract that an unconfigured attach passes every
byte through.

**Detached output is discarded; attach repaints.** The harnesses are
full-screen TUIs whose entire current state is redrawable on demand, so a
window-size signal gets the truth without storing anything. The shim must keep
draining the PTY so the harness never blocks on a full buffer. *Rejected:* a
ring buffer (replaying raw bytes into a fresh terminal can emit partial escape
sequences, and it stores harness output); a disk transcript (unbounded growth
plus a credential-and-transcript exposure surface SECURITY.md deliberately does
not have today). The honest cost — no scrollback across a detach — belongs in
the README body, not a footnote.

**`run` gains `--from-template` with `--role`,** so one file can describe a
fleet you launch detached and a role you occasionally want in front of you.

## Schema discipline for the `presentation` field

Operator rule, stated directly: **if it is not in the schema it is not a real
field, and a non-strict schema is itself a defect.**

That makes `presentation` part of the feature contract rather than a follow-up:

- The spec pins the field **name**, its permitted **values** (`detached`,
  `tmux`), the **default**, and explicitly what **absence** means (detached —
  identical to omitting the flag), alongside flag-override precedence.
- The schema entry ships in the **same implementation PR** as the flag and the
  documentation. Never before — a schema that accepts a field the binary
  ignores invites templates that silently do nothing. Never after — see below.

**Verified: the embedded schema is already strict, so there is no defect to
route.** `skills/agentctl/references/fleet-template.schema.json` (from PR #200)
sets `"additionalProperties": false` at both the document and role-object
levels, and I confirmed a binary built from `main` enforces it rather than
trusting the keyword:

```text
$ agentctl launch --session schematest --from-template tpl.json
exit=2  agentctl: template tpl.json: unknown field "presentation"
exit=2  agentctl: template tpl.json: roles[0]: unknown field "bogus"
```

That result is also the concrete argument for the same-PR rule. Because the
schema is strict, a `presentation` field is *rejected outright* by today's
binary. If the flag shipped before the schema entry, every template using the
documented field would fail to launch; if the schema entry shipped first, the
field would validate and then be ignored. Only landing them together is
correct, and the error text above is what a mismatch looks like.

## The load-bearing SECURITY.md question

Attach admits operator keystrokes to the harness PTY. **The capability is not
new** — it is what typing into a tmux pane already is: same user, same
authority, no added privilege. The *path* is new, and it must not be built by
widening the control protocol.

The wire `Request` carries exactly four fields — version, session, role,
operation — and has no payload-capable field. Structural tests enforce both
that shape and the absence of any payload on control-kind registry entries.
That invariant is what makes "no caller text can reach the PTY" a checkable
claim rather than a promise. Widening it to carry attach bytes would destroy
the guarantee the 0.5.0 batch was built to establish.

The no-detach-key ruling simplifies this story rather than complicating it:
the attach stream carries operator bytes **verbatim, with zero interpretation**.
There is no escape byte, no parser, and no state machine on the operator's input
path — so there is no place for an interception bug to live, and the spec has no
key-collision matrix to maintain against harness releases.

Attach therefore needs its **own per-role stream**, and the spec must settle:

- who may connect (same-uid, descriptor-verified, mode-`0600`);
- how the single-viewer claim is arbitrated, and what a second attach observes;
- **how detach is observed now that it is entirely implicit.** With no key,
  the only detach signal is the client going away, so the spec must say what
  the shim observes on stream EOF or peer death, how promptly the single-viewer
  claim is released, and that a half-open connection cannot strand a role
  un-attachable. This sub-question exists *because* of the no-key ruling and is
  the one thing it makes harder rather than simpler;
- what an attached viewer sees during `stop`/`kill`, and that a viewer is never
  what decides whether a role stopped;
- that the shim's existing single serialized writer continues to order relayed
  keystrokes against registry payloads;
- cleanup rules for the stream artifact, matching the existing socket/lock
  discipline.

When the attach stream's SECURITY.md delta comes, it is **a few plain
sentences in that document's existing register** — the invariant as it stands,
the residual if any, in the same voice as the surrounding text. Not an annex, not a
constraints table, not a per-PR bookkeeping row. SECURITY.md is an operator
document that states what is true now; a mechanically enforced word budget and
current-truth-only vocabulary guard keep it that way, and mechanism detail
belongs in the design specification instead.

The last point is already half-answered: `internal/ptyx` shares **one**
`SerializedWriter` between operator relay input and the control writer, so this
is a transport question rather than a new concurrency model.

## Out of scope

Per-role working directories, multiple simultaneous viewers, any fleet-wide
daemon, any virtual screen model, and harness-native control planes.

## Carried action item

**The tmux-metadata upgrade boundary needs a line in this release's notes.** The
README previously carried an "Upgrade boundary for 0.5.0" section — that fleets
started by the older tmux-metadata lifecycle are not adopted or migrated, and
should be stopped with the binary that started them. That is release content
rather than standing README material, and it was removed from the README.
Nothing in this repository carries it today, and the release notes are a
goreleaser-generated *draft* GitHub Release edited by hand at promotion, so this
is an action for whoever publishes rather than something a document change can
discharge.

There is deliberately no second item here. An earlier revision of this file
recorded the presentation default flip as a behavioral break needing its own
release-note line. That was wrong: the release is unpublished, so nothing was
ever shipped with the tmux default, and the flip is simply what this release
does rather than a change to what an operator already had.
