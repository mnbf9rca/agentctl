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
