# Stable Relative Launch Directory Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ensure a relative launch `--dir` identifies the same absolute directory when a missing fleet role is relaunched from any working directory.

**Architecture:** Normalize explicit launch directories at the `internal/fleet` source boundary with standard-library `filepath.Abs` before stat, tmux creation, and metadata stamping. Keep relaunch overrides unchanged, but reject a stored relative `@agentctl_dir` when it would otherwise become authoritative; the existing `StoredDirectoryError` and CLI refusal mapping preserve exit 3 and provide the explicit `--dir` escape hatch.

**Tech Stack:** Go 1.26, standard library only (`path/filepath`), existing `tmuxx.Runner` fake, throwaway-socket tmux integration tests.

## Global Constraints

- The issue #123 body and approved design spec are the behavioral contract.
- Production external commands continue through `internal/tmuxx.Runner`; tests assert exact executable and argv elements.
- Relative launch paths are resolved lexically, without symlink evaluation.
- A legacy relative stored directory is never guessed from the relaunching process's cwd.
- Refusals render the observed stored value verbatim and retain the §9 exit-code meanings.
- No third-party Go dependency and no shell invocation may be added.

---

### Task 1: Normalize launch directories and fail closed on legacy metadata

**Files:**
- Modify: `internal/fleet/fleet.go`
- Modify: `internal/fleet/relaunch.go`
- Test: `internal/fleet/fleet_test.go`
- Test: `internal/fleet/relaunch_test.go`
- Test: `cmd/agentctl/main_relaunch_test.go`
- Modify: `SECURITY.md`
- Modify: `docs/superpowers/specs/2026-08-01-agentctl-design.md`

**Interfaces:**
- Consumes: `filepath.Abs(path string) (string, error)` on an explicit launch flag and `filepath.IsAbs(path string)` on stored metadata.
- Produces: `Launcher.resolveDirectory(*string) (string, error)` returning one absolute explicit launch directory and `StoredDirectoryError` for an authoritative relative `@agentctl_dir`.

- [x] **Step 1: Write the failing launch regression tests**

Add table cases for `.` and `../sibling`. Create real temporary directories, change the test process to the chosen launch cwd, launch a one-role fleet, and assert literal tmux calls contain the same hand-derived path in both locations:

```go
tmuxx.Call{Executable: "tmux", Args: []string{
	"new-session", "-d", "-s", "epic123", "-n", "planner", "-c", wantDirectory,
	"-e", "AGENTCTL_SESSION=epic123", "-e", "AGENTCTL_ROLE=planner", "-e", "AGENTCTL_MANAGED=1",
	"-P", "-F", "#{session_id}\t#{window_id}\t#{pane_id}\t#{pane_pid}", "--",
	"exec 'amq' 'coop' 'exec' '--session' 'epic123' '--me' 'planner' 'claude'",
}}
tmuxx.Call{Executable: "tmux", Args: []string{
	"set-option", "-t", "$17", "@agentctl_dir", wantDirectory,
}}
```

The production mutation these tests catch is returning the raw relative flag to either tmux or metadata.

- [x] **Step 2: Run the launch test to verify RED**

Run:

```bash
go test ./internal/fleet -run '^TestLaunchMakesRelativeDirectoryAbsoluteBeforeCreationAndStamping$' -count=1
```

Expected: FAIL because `new-session -c` and `@agentctl_dir` still receive `.` or `../sibling`.

- [x] **Step 3: Write the failing cross-cwd relaunch and legacy-refusal tests**

Script a launch from `/work/alpha` with `--dir payload`, then feed its expected stored `/work/alpha/payload` into a relaunch whose `Getwd` fails if called. Assert the exact recreation argv contains:

```go
[]string{
	"new-window", "-d", "-t", "$4", "-n", "planner", "-c", "/work/alpha/payload",
	"-e", "AGENTCTL_SESSION=epic123", "-e", "AGENTCTL_ROLE=planner", "-e", "AGENTCTL_MANAGED=1",
	"-P", "-F", "#{window_id}\t#{pane_id}\t#{pane_pid}", "--",
	"exec 'amq' 'coop' 'exec' '--session' 'epic123' '--me' 'planner' 'claude'",
}
```

Add a stored-mode case with `@agentctl_dir=payload` and no override. Assert `StoredDirectoryError.Path == "payload"`, no `new-window`, CLI exit 3, and exact stderr:

```text
agentctl: refusing to relaunch planner; managed session "epic123" records launch directory "payload": path is not absolute; supply --dir to relaunch planner elsewhere
```

Add an override case proving `--dir /elsewhere` remains the explicit escape hatch and leaves `@agentctl_dir` unchanged.

- [x] **Step 4: Run the relaunch tests to verify RED**

Run:

```bash
go test ./internal/fleet ./cmd/agentctl -run 'RelativeDirectory|StoredRelativeDirectory' -count=1
```

Expected: FAIL because relaunch currently treats a relative stored directory as authoritative and the CLI has no pinned relative-path refusal fixture.

- [x] **Step 5: Implement the minimal source and read-side fix**

Resolve a non-empty explicit launch directory before calling `Stat`:

```go
resolved, err := filepath.Abs(*directory)
if err != nil {
	return "", &DirectoryError{Path: *directory, Err: err}
}
info, err := l.stat(resolved)
```

Return `resolved` so new-session, every new-window, and `@agentctl_dir` share one value. In stored-mode relaunch, before adopting `directoryValue`, refuse `!filepath.IsAbs(directoryValue)` only when `request.Directory == nil`, using a `StoredDirectoryError` variant whose message is exactly `path is not absolute`; an explicit override continues through the existing directory validation and provenance flow.

- [x] **Step 6: Run focused tests to verify GREEN**

Run:

```bash
go test ./internal/fleet ./cmd/agentctl -run 'RelativeDirectory|StoredRelativeDirectory|RelaunchDirectoryOverride' -count=1
```

Expected: PASS with exact argv, metadata, message, and exit-code assertions.

- [x] **Step 7: Run all repository gates**

Fetch and rebase onto current `origin/main`, then run:

```bash
go test ./...
go vet ./...
shellcheck hack/*.sh
golangci-lint run ./...
go test -tags integration ./...
git diff --check
```

Expected: all commands exit 0. After push, wait for and cite the PR's own `pull_request` CI run.

- [x] **Step 8: Commit the implementation**

Stage only issue #123 files and create signed focused commits. The repository workflow then publishes a non-draft PR whose body includes `Closes #123`, the red/green evidence, documentation impact, and full verification; after the PR's own CI, request the reviewer gate with the exact run URL and detach this worktree without merging the PR.
