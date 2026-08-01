# Implementation plans

Plans in this directory are **working documents**. They are never normative.

The approved design spec — [`docs/superpowers/specs/2026-08-01-agentctl-design.md`](../specs/2026-08-01-agentctl-design.md) —
is the single source of contract detail. A plan **cites** the spec; it does not restate it.

## What a plan must not contain

Copying any of these creates a second source that nothing keeps current. When the spec changes, the copy silently
disagrees, and the next reader has no way to tell which one is authoritative:

- exit codes and their meanings
- check orderings, stamping orders, precedence chains
- argv shapes and tmux format strings
- exact error or diagnostic message text
- validation regexes and charsets
- state names and their conditions
- timing constants

Write `see §6.2` or `per §13.2 row 3`, not the content itself.

## What a plan is for

- task decomposition and file lists
- step sequencing, including red/green TDD steps
- verification commands to run
- the **names** of interfaces being produced or consumed

The distinction is names versus semantics. A plan may say *"produces `config.ValidateRoleName`"*. It may not say
*"role names must match `^[a-z0-9][a-z0-9_-]*$`"* — that belongs to §7, and a plan repeating it is a copy that will
outlive its accuracy.

## Lifecycle

A plan has served its purpose once the work it describes is merged. Committing one is optional; if a plan is
committed, it is subject to the rules above from the moment it lands.

## Why this is written down

The rule was ruled twice on the same class of problem — PR #36 (a plan restating §13's canonical format strings) and
PR #54 (a plan restating §6.2's chain order and §9's exit categories) — before being made general. It lives here rather
than in the spec so that it is found by whoever is about to write a plan, which is the moment it applies.
