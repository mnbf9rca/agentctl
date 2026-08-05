# Launch Status Confirmation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every successful `agentctl launch --session S` render the observed single-session status table for `S` without changing launch's existing exit-code contract.

**Architecture:** Keep fleet creation in `internal/fleet` and observation in `internal/status`. `fleet.Launch` returns the exact typed session ID obtained from `new-session`; the CLI passes that session directly to one shared helper that collects and renders it without a name re-resolution. Launch treats confirmation failures as advisory because fleet creation has already succeeded.

**Tech Stack:** Go 1.26, standard library only, existing `tmuxx.Runner` fake, existing `internal/status` collector and renderers.

## Global Constraints

- The issue body and approved design spec are the behavioral contract.
- Every rendered claim must come from observed post-launch state; the launch request is not a status source.
- Successful fleet creation keeps exit code `0`, including when confirmation cannot be completed.
- Production external commands continue through `internal/tmuxx.Runner`; tests assert exact executable and argv elements.
- No third-party Go dependency and no shell invocation may be added.

---

### Task 1: Render observed status after launch

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-08-01-agentctl-design.md`
- Modify: `cmd/agentctl/main.go`
- Modify: `internal/fleet/fleet.go`
- Test: `cmd/agentctl/main_launch_test.go`
- Test: `cmd/agentctl/main_test.go`
- Test: `internal/fleet/fleet_test.go`

**Interfaces:**
- Produces: `fleet.Launch(context.Context, string, config.FleetConfig, *string) (tmuxx.Session, error)` and `writeSelectedStatus(context.Context, io.Writer, statusCollector, tmuxx.Session, bool) error`, the latter shared by explicit `status` and post-launch confirmation.

- [x] **Step 1: Write the failing success-path test**

Change the former silent-launch test to script launch followed by the canonical selected-status responses, then assert the literal table:

```go
want := "SESSION  ROLE     HARNESS  MODEL    EFFORT   PANE  PROCESS  STATE\n" +
	"fleet    planner  claude   default  default  %42   claude   running\n"
```

Assert the post-launch calls exactly: three `show-options` calls on `$17`, `list-windows` on `$17`, `list-panes` on `@23`, and `ps -o comm= -p 4242`, in that order after the existing launch transcript. Assert there is no second `list-sessions` call after creation.

- [x] **Step 2: Run the focused test to verify RED**

Run:

```bash
env GOCACHE=/private/tmp/agentctl-issue121-go-cache go test ./cmd/agentctl -run '^TestRunLaunchSuccessRendersObservedStatus$' -count=1
```

Expected: FAIL because launch still writes no table and performs no post-launch status calls.

- [x] **Step 3: Add degraded-state and confirmation-failure tests while still RED**

Add one test whose post-launch roster contains `planner` but whose window list is empty; assert a `missing` row and exit `0`. Add one test whose post-launch collection fails; assert exit `0` and a factual stderr line that launch succeeded but status confirmation could not be completed.

- [x] **Step 4: Implement the minimal shared path**

Make `fleet.Launch` return the exact session ID obtained from its successful `new-session` response. After a successful launch, pass that session directly to `writeSelectedStatus` and return `exitOK`. Extract the existing explicit-status collection/rendering body into:

```go
func writeSelectedStatus(ctx context.Context, stdout io.Writer, collector statusCollector, target tmuxx.Session, asJSON bool) error
```

Both callers use the helper. Launch maps collector and renderer failures to an advisory stderr message and `exitOK`; the status command keeps its existing error mapping.

- [x] **Step 5: Update the approved design spec**

Extend spec §6.1 to state that successful launch reuses its held session ID to render the same observed roster-driven table as `status --session S`, that degraded rows remain honest, and that confirmation failure is advisory and cannot change the already-established launch exit code.

Update the README launch and quickstart sections so operator guidance describes the observed confirmation and the remaining use of explicit status refreshes.

- [x] **Step 6: Run focused tests to verify GREEN**

Run:

```bash
env GOCACHE=/private/tmp/agentctl-issue121-go-cache go test ./cmd/agentctl -run 'TestRunLaunch' -count=1
```

Expected: PASS with exact output and runner-call assertions.

- [x] **Step 7: Run all repository gates**

Run `go test ./...`, `go vet ./...`, `shellcheck hack/*.sh`, `golangci-lint run ./...`, and `go test -tags integration ./...` with the writable Go cache where needed. Run the release snapshot gates from `.github/workflows/ci.yml` only if the changed surface makes them relevant.

- [x] **Step 8: Commit**

```bash
git add README.md docs/superpowers/plans/2026-08-05-launch-status-confirmation.md docs/superpowers/specs/2026-08-01-agentctl-design.md cmd/agentctl/main.go cmd/agentctl/main_launch_test.go cmd/agentctl/main_test.go internal/fleet/fleet.go internal/fleet/fleet_test.go
git commit -S -m "Confirm observed fleet status after launch"
```
