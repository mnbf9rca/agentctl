# README Operator Documentation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the non-attach portion of issue #15's operator README, leaving attachment documentation for the issue #14 caboose.

**Architecture:** Create one task-oriented `README.md` whose operator claims cite the approved design, threat model, and release evidence instead of becoming a second specification. Validate every published command and example against the built CLI; add attachment only after issue #14 merges and its exact behavior can be observed.

**Tech Stack:** Markdown, Go CLI help output, Make

## Global Constraints

- `docs/superpowers/specs/2026-08-01-agentctl-design.md` and `docs/brief.md` remain normative; this plan and the README explain use rather than redefine contracts.
- Follow issue #15's required content and acceptance criteria.
- Use `SECURITY.md` for the operating-risk language and `docs/release-checklist.md` for dated compatibility evidence.
- Do not document attachment behavior until issue #14 is merged and verified.

---

### Task 1: Non-attach operator README

**Files:**
- Create: `README.md`

**Interfaces:**
- Consumes: the CLI surface and behavior specified in design §§1–9 and implemented under `cmd/agentctl`
- Produces: task-first installation, launch, status, control, teardown, safety, and troubleshooting documentation

- [x] **Step 1: Capture the implemented command surface**

Run the root and subcommand help for every currently implemented non-attach operation, and inspect `Makefile` installation behavior.

- [x] **Step 2: Write the operator path**

Create `README.md` using the planner-approved outline: responsibilities, prerequisites and installation, eight-role quickstart, pre-control safety guidance, command reference, status interpretation, tmux environment troubleshooting, and maintainer/release links.

- [x] **Step 3: Check prose against authoritative sources**

Compare status language with design §6.3, command behavior with §§4 and 9, launch details with §§3 and 6.1, and safety language with `SECURITY.md`.

- [x] **Step 4: Verify commands and examples**

Run:

```bash
GOCACHE=/private/tmp/agentctl-go-cache make test
make build
./bin/agentctl --help
./bin/agentctl launch --help
./bin/agentctl status --help
./bin/agentctl clear --help
./bin/agentctl compact --help
./bin/agentctl kill --help
```

Expected: tests and build pass; every README command and flag agrees with the corresponding help output.

- [x] **Step 5: Commit the non-attach slice**

```bash
git add README.md docs/superpowers/plans/2026-08-01-readme-operator-docs.md
git commit -m "docs: add agentctl operator README"
```

### Task 2: Attachment caboose after issue #14

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: merged issue #14 implementation and its exact root/subcommand help
- Produces: verified iTerm2 setup and attachment instructions completing issue #15

- [ ] **Step 1: Rebase on the merged issue #14 result**

Update the branch only after issue #14 is merged.

- [ ] **Step 2: Observe the shipped attachment interface**

Run the merged binary's root and attachment help and inspect its operator-visible errors and tests.

- [ ] **Step 3: Add the attachment section**

Document only the observed CLI behavior and the exact iTerm2 setting required by issue #15.

- [ ] **Step 4: Re-run README and repository verification**

Run the complete test suite, build, all documented help commands, and copy-paste checks before opening the PR.
