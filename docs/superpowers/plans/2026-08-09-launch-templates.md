# Launch Templates Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement issue #151's approved JSON launch-template source, merge it with launch flags, and report the effective configuration's provenance.

**Architecture:** Keep value semantics in `internal/config`, isolate file/JSON shape handling in a small `internal/launchtemplate` package, and keep union wiring plus output in the launch CLI path. The existing `fleet.Launcher` remains the only component that performs preflight and tmux creation; templates only produce the already-validated `config.FleetConfig` and directory input it consumes.

**Tech Stack:** Go standard library, filesystem fixtures, fake `tmuxx.Runner` unit tests, and real-tmux integration tests.

## Global Constraints

- Issue #151's complete amended body and the landed design are the contract.
- Follow `docs/superpowers/specs/2026-08-01-agentctl-design.md` §§4, 6.1, 6.5, 6.9, 7, 9, and 12.9 without restating their normative details here.
- Follow `SECURITY.md` residual 7 for the caller-named read path.
- Keep the Go module standard-library-only and keep production tmux/process execution behind `internal/tmuxx.Runner`.
- The validator consolidation is the first commit and must preserve every existing CLI diagnostic without changing its tests.
- Every behavior change follows red/green TDD; non-template launch must retain its exact fake-Runner transcript.

---

### Task 1: Consolidate list parsing onto exported validators

**Files:**
- Modify: `internal/config/config.go`
- Test unchanged: `internal/config/config_test.go`

**Interfaces:**
- Consumes: `config.ValidateRoleName`, `config.ParseHarness`, `config.ValidateModelName`, and `config.ValidateEffort`.
- Produces: list-parser error wrapping that preserves the existing `ValidationError` fields and CLI-rendered messages.

- [ ] **Step 1: Run the pinned parser tests before editing**

  Run `go test ./internal/config -run 'TestParseFleetRejectsInvalid(Role|Model|Effort)Lists' -count=1` and retain the clean baseline.

- [ ] **Step 2: Replace each inline value rule with the exported predicate**

  Add only the minimal error-reason adapter needed to preserve the caller-specific list shape. Do not change parsing order, duplicate detection, defined-role checks, or any existing message test.

- [ ] **Step 3: Run the entire config package and commit first**

  Run `go test ./internal/config -count=1`, inspect the diff for behavior preservation, and make this the topic branch's first signed commit.

### Task 2: Decode a bounded strict JSON source

**Files:**
- Create: `internal/launchtemplate/template.go`
- Create: `internal/launchtemplate/template_test.go`
- Create: `internal/launchtemplate/testdata/*.json`

**Interfaces:**
- Produces: `launchtemplate.Document`, `launchtemplate.Role`, `launchtemplate.Decoder`, and a typed `launchtemplate.Error` that renders the §6.9 file/location context.
- Consumes: `os.Open`, descriptor `Stat`, `io.LimitReader`, and `encoding/json`; it does not validate fleet values or inspect directories at point of use.

- [ ] **Step 1: Add file-boundary tests and capture RED**

  Cover open failure, descriptor-stat failure, every non-regular classification available through the file seam, symlink-to-regular success, `-` as an ordinary path, the exact cap, and one byte over it. Assert close behavior and that the read occurs only after descriptor verification.

- [ ] **Step 2: Implement open, descriptor verification, and bounded reading**

  Keep the injectable seam local to this package and make the production constructor use `os.Open`.

- [ ] **Step 3: Add fixture-driven pre-pass tests and capture RED**

  Cover malformed input, absent/unsupported version, duplicate root keys, duplicate nested keys, and precedence of the version pre-pass over strict unknown-field decoding per §6.9.

- [ ] **Step 4: Implement the token pre-pass**

  Walk objects and arrays with `json.Decoder.Token`, retain object paths for duplicate reporting, and record the root version before strict decoding.

- [ ] **Step 5: Add strict-decode and tri-state fixture tests and capture RED**

  Cover the named `session` refusal, root and role unknown fields, trailing documents, invalid field types, absent legal fields, every `null` rejection, every empty-string rejection, absent/empty roles legality, and missing role merge-key rejection.

- [ ] **Step 6: Implement strict decoding and run GREEN**

  Decode root and role objects with unknown-field rejection, reject trailing documents, and return only structural source values. Run `go test ./internal/launchtemplate -count=1`.

### Task 3: Merge template values with flag values through config validators

**Files:**
- Create: `cmd/agentctl/launch_template.go`
- Create: `cmd/agentctl/launch_template_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- Produces: a private `launchConfiguration` containing `config.FleetConfig`, the effective directory input, template identity, and per-field `fleet.Provenance`.
- Produces: `fleet.ProvenanceTemplate` while preserving the existing relaunch provenance constants.
- Consumes: `launchtemplate.Document`, `config.ParseFleet`, and the exported config predicates.

- [ ] **Step 1: Add the D2 union/add/override matrix and capture RED**

  Cover a template-only roster, flags-only behavior through the same entry point, template roles filled or overridden by flags, flag-added roles, model/effort overrides against template-only roles, directory override, per-source duplicates, and the pinned union order.

- [ ] **Step 2: Implement the minimal ordered merge**

  Parse flag roles with the existing parser, preserve template positions, append only flag-added roles, apply field overrides without moving roles, then validate the effective union through the exported predicates and `config.ParseFleet`.

- [ ] **Step 3: Add union validation and provenance tests and capture RED**

  Cover empty unions, unresolved partial roles, effective harness/model/effort failures with template locations, template directory absolute validation, defaults, and all template/flag provenance combinations required by §6.9.

- [ ] **Step 4: Implement validation wrapping and run GREEN**

  Preserve CLI-origin errors exactly and wrap template-origin failures with typed file/role context. Run `go test ./internal/config ./internal/launchtemplate ./cmd/agentctl -run 'Test.*LaunchTemplate' -count=1`.

### Task 4: Wire the second launch form and provenance output

**Files:**
- Modify: `cmd/agentctl/main.go`
- Modify: `cmd/agentctl/main_launch_test.go`
- Modify: `internal/fleet/fleet.go`
- Modify: `internal/fleet/fleet_test.go`
- Modify: `cmd/agentctl/integration_launch_test.go`

**Interfaces:**
- Consumes: `launchConfiguration` and the existing `fleet.Launcher.Launch` path.
- Produces: `launchOptions.fromTemplate`, effective-directory reporting on `fleet.LaunchResult`, and launch provenance rendering before status confirmation.

- [ ] **Step 1: Add CLI parsing/exit tests and capture RED**

  Cover both §4 launch forms, duplicate `--from-template`, missing/empty template paths, no-role unions, and every template failure occurring before preflight or any Runner call.

- [ ] **Step 2: Wire decode and merge before launch**

  Keep the no-template branch on its current `config.ParseFleet` call shape and classify every typed template/union error as usage.

- [ ] **Step 3: Add exact output and transcript tests and capture RED**

  Assert every role provenance line, the conditional directory line, ordering before the existing status table, template-derived creation/stamping argv, and byte-for-byte unchanged non-template argv.

- [ ] **Step 4: Implement provenance rendering and directory return**

  Add only the launch result fact needed to render the effective directory; do not move directory resolution or creation behavior out of `internal/fleet`.

- [ ] **Step 5: Add real-tmux coverage and run GREEN**

  Launch from a fixture on a throwaway socket, verify union order and effective metadata, then run the focused unit and integration launch suites.

### Task 5: Operator documentation and drift coverage

**Files:**
- Modify: `README.md`
- Modify: `cmd/agentctl/main.go`
- Modify: relevant help tests under `cmd/agentctl/`
- Verify unchanged: `skills/agentctl/SKILL.md`, whose contract keeps `launch` operator-only

**Interfaces:**
- Consumes: the approved CLI and template examples in §§4 and 6.9.
- Produces: discoverable operator help without adding a template writer, generator, or agent-facing launch authority.

- [ ] **Step 1: Add behavior-level help tests and capture RED**

  Assert that operators can discover both launch forms from the rendered binary help.

- [ ] **Step 2: Update help and README minimally**

  Document the operator-facing composition and read-only boundary while leaving the agent-facing skill's operator-only rule intact.

- [ ] **Step 3: Run command and embedded-artifact GREEN**

  Run `go test ./cmd/agentctl ./skills -count=1` plus the skill pairing/version checks required by CI.

### Task 6: Full verification, publication, and reviewer gate

**Files:**
- Verify: all changed files

**Interfaces:**
- Produces: focused signed commits, a ready PR closing #151, fresh pull-request CI evidence, reviewer release, and a detached worktree.

- [ ] **Step 1: Run local gates**

  Run the exact checks in `.github/workflows/ci.yml`, including unit, vet, race, lint, ShellCheck, integration, skill checks, and relevant release snapshot checks.

- [ ] **Step 2: Rebase and repeat merge evidence**

  Fetch current `main`, rebase the topic branch, and repeat all required gates on the rebased tree.

- [ ] **Step 3: Publish the ready PR**

  Push signed focused commits, open a non-draft PR with `Closes #151`, and include red/green evidence plus local gate results.

- [ ] **Step 4: Wait for fresh PR CI and request review**

  Quote the PR's own `pull_request` run URL in the reviewer request, detach the worktree, and report the handoff through AMQ. Do not merge the PR.
