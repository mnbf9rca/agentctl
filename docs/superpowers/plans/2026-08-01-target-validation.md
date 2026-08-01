# Target Validation Chain Implementation Plan

> **Non-normative working document.** This plan records implementation steps only. The authoritative contract is issue #11 plus [`2026-08-01-agentctl-design.md`](../specs/2026-08-01-agentctl-design.md), especially §§1.1, 6.2, 8, 9, 10, 12.5–12.6, 13.1, and 13.5. If this plan differs from either source, follow the issue and design spec.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the delivery-neutral, fail-closed target resolver required by issue #11 and expose the missing single-role validator.

**Architecture:** `internal/config` owns single-value role syntax. `internal/target.Resolver` depends on a narrow read-only client, validates one already-resolved session and role in the ratified order, and returns an exact pane ID. Six fact-only error types preserve the observed session, window, pane, and process facts for command-specific rendering by issue #12; delivery is structurally unavailable.

**Tech Stack:** Go 1.26, standard library only, existing `internal/tmuxx` typed client and fake runner.

## Global Constraints

- Validate ROLE against `^[a-z0-9][a-z0-9_-]*$` before every `Runner` call.
- Pass only typed tmux IDs to `-t`; compare names exactly in Go.
- Refuse zero or multiple exact window matches and preserve every matching ID.
- Require session managed/version markers equal to the invariant `1`.
- Require one live pane whose current root process exactly equals the nonempty launch baseline.
- Refuse when `TMUX_PANE` equals the resolved target pane.
- `internal/target.Client` must not expose `DeliverPayload`.
- Preserve context cancellation; classify other runner/parse failures with `tmuxx.ClassifyError`.

---

### Task 1: Single-role validation

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- Produces: `func ValidateRoleName(role string) error`
- Produces: `*config.ValidationError` whose `Error()` says `invalid role`, never a nonexistent `--role` flag.

- [ ] **Step 1: Write failing validator tests**

Add table tests proving `planner`, `codex_2`, and `review-agent` pass, while empty, uppercase, leading-hyphen, slash, and newline values return `*ValidationError` with literal ROLE-specific messages such as:

```go
err := ValidateRoleName("Plan/ner")
if got, want := err.Error(), `invalid role "Plan/ner": must match ^[a-z0-9][a-z0-9_-]*$`; got != want {
	t.Fatalf("error = %q, want %q", got, want)
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run: `go test ./internal/config -run ValidateRoleName`

Expected: compile failure because `ValidateRoleName` does not exist.

- [ ] **Step 3: Implement the minimal shared validator**

Add a `role` case to `ValidationError.Error` and implement:

```go
func ValidateRoleName(role string) error {
	if !nameExpression.MatchString(role) {
		return &ValidationError{
			Option: "role", Value: role, EntryIndex: -1,
			Reason: "must match " + namePattern,
		}
	}
	return nil
}
```

- [ ] **Step 4: Run config tests and verify GREEN**

Run: `go test ./internal/config`

- [ ] **Step 5: Commit the config deliverable**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "config: validate single role names"
```

### Task 2: Ratified target API and ordered resolver

**Files:**
- Create: `internal/target/resolver.go`
- Create: `internal/target/errors.go`
- Create: `internal/target/resolver_test.go`

**Interfaces:**
- Consumes: `config.ValidateRoleName`, `tmuxx.Session`, `tmuxx.Window`, `tmuxx.Pane`, `tmuxx.ClassifyError`, and `tmuxx.ErrProcessUnavailable`.
- Produces: `Client`, `LookupEnv`, `Resolver`, `New(Client, LookupEnv) Resolver`, and `Resolve(context.Context, tmuxx.Session, string) (tmuxx.PaneID, error)`.
- Produces: `SessionMetadataError`, `RoleResolutionError`, `WindowMetadataError`, `PaneStateError`, `ProcessIdentityError`, and `SelfTargetError` with the ratified fields; exit categories per §9.

- [ ] **Step 1: Write failing API and charset-first tests**

Create the wished-for resolver API and assert a malformed role returns `*config.ValidationError` while a real `tmuxx.FakeRunner` records literal zero calls. This test catches moving validation after any metadata probe.

- [ ] **Step 2: Run target tests and verify RED**

Run: `go test ./internal/target`

Expected: compile failure because the package API does not exist.

- [ ] **Step 3: Add the six fact-only errors and resolver skeleton**

Implement the ratified field shapes exactly. Each `Error()` reports only observed target facts and contains no `clear`, `compact`, payload, or operation label. `ProcessIdentityError.Unwrap()` returns its `Err`.

- [ ] **Step 4: Run charset test and verify GREEN**

Run: `go test ./internal/target -run MalformedRole`

- [ ] **Step 5: Add failing session-gate tests**

Use exact fake-runner argv and probe counts to cover managed unset/wrong, version unset/wrong, tmux failure with stderr, and context preservation. The wrong managed marker must record one option read; the wrong version marker must record two.

- [ ] **Step 6: Implement session gates and verify GREEN**

Probe the session gates in the §6.2 order and classify call errors. Run: `go test ./internal/target -run Session`.

- [ ] **Step 7: Add failing exact-window and metadata tests**

Cover no match, prefix/suffix decoys, two exact matches preserving both IDs, unmanaged window, and stored-role mismatch. Assert only the session ID appears after `-t`; role names never do.

- [ ] **Step 8: Implement exact matching and verify GREEN**

Call `ListWindows` once, collect every `window.Name == role`, refuse unless exactly one, then check managed before stored role. Run: `go test ./internal/target -run 'Window|Role'`.

- [ ] **Step 9: Add failing pane-state tests**

Cover zero panes, multiple panes, one record whose `WindowPanes` is not one, and a dead sole pane. Each case asserts exact probe count and no `send-keys` call.

- [ ] **Step 10: Implement pane-state gates and verify GREEN**

Call `ListPanes` by exact window ID, then enforce the pane-state gates in the §6.2 order. Run: `go test ./internal/target -run Pane`.

- [ ] **Step 11: Add failing identity and self-target tests**

Cover empty baseline with no `ps`, `ErrProcessUnavailable`, other `ps` startup failure, exact mismatch, exact match, and `TMUX_PANE` unset/different/equal. Assert self-target is checked only after identity and success returns the literal pane ID.

- [ ] **Step 12: Implement identity/self-target gates and verify GREEN**

Reject an empty `Window.Process` before `ProcessName`; adapt only `ErrProcessUnavailable` to `ProcessIdentityError`; require exact equality; compare the raw lookup result to the typed pane ID. Run: `go test ./internal/target`.

- [ ] **Step 13: Refactor while green and commit**

Keep error construction and exact-match collection small and local. Run `gofmt` and the target/config tests, then:

```bash
git add internal/target internal/config
git commit -m "target: validate control pane safely"
```

### Task 3: Verification and handoff

**Files:**
- Review: `internal/target/*.go`, `internal/config/*.go`

**Interfaces:**
- Verifies the complete issue #11 contract consumed by issue #12.

- [ ] **Step 1: Run focused race-enabled tests**

Run: `go test -race ./internal/config ./internal/target`

- [ ] **Step 2: Run repository verification**

Run: `go test ./...` and `go vet ./...`.

- [ ] **Step 3: Inspect the final diff and mutation matrix**

Confirm realistic mutations are caught: delayed role validation, name in `-t`, first-match ambiguity selection, delivery capability added to target, dead/count gate reordered, empty baseline probed, unavailable process classified as tmux, and self-target check removed.

- [ ] **Step 4: Push and open the draft PR**

Push `wave4/target`, open a draft PR referencing issue #11, and request review with exact verification evidence. Detach the worktree after the PR is published, as directed by the planner.
