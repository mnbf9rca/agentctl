# Detached release-verifier relaunch — design notes

Status: **approved non-normative design notes**. The current body of
[#225](https://github.com/mnbf9rca/agentctl/issues/225), the approved product
design, `SECURITY.md`, and the release checklist remain authoritative.

## Problem

The default live release walkthrough launches `relverify` without a
presentation flag, so the product correctly creates a detached fleet per the
approved design §15.11.1. Its relaunch verification still resolves a tmux
session, window, and pane. The walkthrough therefore tests the obsolete
presentation and fails before it can verify the default presentation.

The failure evidence has a second defect: precheck and teardown reuse the same
status capture paths, so teardown can overwrite the diagnostic that caused an
earlier failure. Task 8 did not catch either defect because it did not execute
the focused full-default verifier fixture.

## Approved design

Part B remains detached end to end. It does not opt into tmux and does not
create a parallel harness. The verifier resolves role runtime identity through
`agentctl status`, the same durable truth path consumed by product relaunch.
It observes the original child's absence, executes the stored-configuration
relaunch, resolves newly running runtime identities, and asks the operator to
attach to the replacement role and confirm the fresh surface. These checks
exercise §15.11.1 and §15.11.6 without inferring presentation state from tmux.

The operator opens one explicit per-role attachment for each detached role.
After role `a` is terminated, its viewer ends; the walkthrough directs the
operator to attach to role `a` again before making the fresh-surface
attestation. Part B removes Command Menu, tab, pane, and detach-key claims.
Viewing ends by closing the attachment terminals, as specified by §15.11.1.

Every independently meaningful status checkpoint writes distinct evidence.
Precheck, pre-relaunch runtime, post-relaunch runtime, post-viewer-close
runtime, and teardown observations cannot overwrite each other. Metadata and
rendered evidence make only the facts actually observed at those checkpoints.

The live fixture models the detached boundary: launch has no presentation
flag, bare attach is refused, explicit per-role attach is the only valid
instruction, relaunch changes recorded runtime identities, and Part B does not
query tmux for fleet identity. Task 8 executes that focused full-default
fixture and requires its transcript marker so future walkthrough/product
divergence fails the release gate.

## Alternatives rejected

- Passing `--tmux` would verify the opt-in presentation rather than the
  release's default behavior.
- Building a separate verifier-only harness would duplicate product lifecycle
  semantics and could drift independently.
- Retaining pane checks alongside runtime checks would preserve the same false
  prerequisite under a different name.

## Safety and evidence

The change does not alter product validation, command construction, targeting,
or delivery. The verifier continues to use only its owned fleet and exact
recorded PIDs. An observed old-child absence is required before relaunch, and
replacement success requires fresh status observations plus the operator's
separate visual confirmation. The preserved operator artifact identified in
the coordination thread is read-only input to the diagnosis and must not be
modified or removed.

## Live-run correction

The first build2 live run passed Part A and then failed before checkpoint B.C1.
The fixed product session name `relverify` also became the AMQ co-op session
name used by each harness. A pre-existing AMQ `relverify` directory with stale
wake-owner state made `amq coop exec` exit before Claude reached tty readiness;
agentctl consequently observed hidden-shim exit 8 and completed its owned
rollback. The agentctl durable-fleet absence precheck could not observe this
independent namespace collision.

Each default verifier run therefore derives a lowercase `relverify_<token>`
session from the already-created random evidence root. The value remains within
the validated 32-byte session contract, is used consistently by every Part B
command and expected output, and is recorded as `part_b_session` in evidence.
This avoids coupling agentctl release tooling to AMQ's private on-disk layout
and avoids deleting or reusing unrelated coordination state. The failed live
artifact at `/tmp/agentctl-release-verify.qFePqt` remains preserved read-only.

The next signed live run proved the unique name was necessary but insufficient.
Its fresh `relverify_9jej5t` launch still failed before B.C1 because the fresh
worktree had no `.amqrc`; unattended `amq coop exec` attempted interactive
auto-initialization inside the detached child PTY and exited before the harness
became ready. The preserved evidence is
`/tmp/agentctl-release-verify.9JEJ5T`.

Part B now uses an existing operator `.amqrc` without changing it, or creates a
temporary verifier-owned `.amqrc` whose absolute AMQ root is inside the random
evidence root. Cleanup is armed before initialization, records filesystem
identities for both owned artifacts, and removes them only after the fleet is
observed absent. A successful `agentctl kill` is not absence evidence: normal
teardown and the exit trap each require a subsequent `agentctl status` result
that factually proves the run-unique durable fleet is missing. If that
observation is running or indeterminate, the verifier retains the temporary
AMQ config/root and reports cleanup failure. Partial initialization is cleaned;
substituted or unexpected paths are retained and reported rather than deleted.
This is release-fixture setup through AMQ's public CLI, not an AMQ product
change.
