# Version Command Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add truthful `agentctl version` and `agentctl --version` output whose identity is stamped by project builds and otherwise derived from recorded Go build facts.

**Architecture:** A focused `internal/buildinfo` package resolves the first available identity from a linker stamp, Go VCS build settings, or the literal `development`. The CLI recognizes version queries before constructing tmux-backed dependencies, while the Makefile injects `git describe` and the operator docs record the command and release check.

**Tech Stack:** Go 1.26 standard library (`runtime/debug`), existing CLI test harness, GNU/BSD Make-compatible Makefile, Markdown documentation.

## Global Constraints

- Identity precedence is linker stamp > Go `vcs.revision` with factual `+dirty` suffix > `development`.
- Both successful forms print exactly `agentctl IDENTITY\n` to stdout, print nothing to stderr, and exit 0.
- Version queries must not touch tmux or initialize tmux-backed dependencies.
- `agentctl version` accepts no extra arguments; extras exit 2 with version usage.
- `agentctl --version` is recognized only as the sole argument.
- Do not infer a tag, semantic version, release status, or clean state that build metadata does not record.

---

### Task 1: Resolve factual build identity

**Files:**
- Create: `internal/buildinfo/buildinfo.go`
- Test: `internal/buildinfo/buildinfo_test.go`

**Interfaces:**
- Consumes: `debug.ReadBuildInfo() (*debug.BuildInfo, bool)` and its `Settings []debug.BuildSetting`.
- Produces: linker-settable `buildinfo.Stamp string` and `buildinfo.Current() string` for the CLI.

- [ ] **Step 1: Write the failing resolver tests**

Create table-driven tests in package `buildinfo` that call the unexported pure helper `resolve(stamp string, info *debug.BuildInfo, ok bool) string`. Cover these exact cases:

```go
tests := []struct {
	name  string
	stamp string
	info  *debug.BuildInfo
	ok    bool
	want  string
}{
	{name: "linker stamp wins", stamp: "v0.1.0-3-gabc123", info: buildInfo("deadbeef", true), ok: true, want: "v0.1.0-3-gabc123"},
	{name: "clean VCS revision", info: buildInfo("abc123", false), ok: true, want: "abc123"},
	{name: "dirty VCS revision", info: buildInfo("abc123", true), ok: true, want: "abc123+dirty"},
	{name: "missing revision", info: buildInfo("", true), ok: true, want: "development"},
	{name: "missing build info", want: "development"},
}
```

The test helper returns settings for `vcs.revision` and `vcs.modified`. Before writing the tests, state the production change that makes them fail: adding `resolve` with the required precedence.

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
go test ./internal/buildinfo -run TestResolve -count=1
```

Expected: compilation fails because `resolve` does not exist. This is the correct RED signal for the missing feature.

- [ ] **Step 3: Implement the minimal resolver**

Create `internal/buildinfo/buildinfo.go` with these boundaries:

```go
package buildinfo

import "runtime/debug"

var Stamp string

func Current() string {
	info, ok := debug.ReadBuildInfo()
	return resolve(Stamp, info, ok)
}

func resolve(stamp string, info *debug.BuildInfo, ok bool) string {
	if stamp != "" {
		return stamp
	}
	if !ok || info == nil {
		return "development"
	}

	var revision string
	modified := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if revision == "" {
		return "development"
	}
	if modified {
		return revision + "+dirty"
	}
	return revision
}
```

- [ ] **Step 4: Verify GREEN and package coverage**

Run:

```bash
go test ./internal/buildinfo -count=1
```

Expected: all resolver cases pass with no warnings.

- [ ] **Step 5: Commit the identity resolver**

```bash
git add internal/buildinfo/buildinfo.go internal/buildinfo/buildinfo_test.go
git commit -m "feat: resolve agentctl build identity"
```

### Task 2: Wire CLI, Makefile stamp, and operator documentation

**Files:**
- Modify: `cmd/agentctl/main.go`
- Modify: `cmd/agentctl/main_test.go`
- Modify: `Makefile`
- Modify: `README.md`
- Modify: `docs/release-checklist.md`

**Interfaces:**
- Consumes: `buildinfo.Current() string` and linker-settable `buildinfo.Stamp` from Task 1.
- Produces: `agentctl version`, exact sole-argument alias `agentctl --version`, stamped `make build`, README command reference, and a release-checklist observation step.

- [ ] **Step 1: Write failing CLI behavior tests**

Add tests in `cmd/agentctl/main_test.go` that temporarily set `buildinfo.Stamp = "v0.1.0-test"` with cleanup restoring the previous value. Assert:

```go
for _, arguments := range [][]string{{"version"}, {"--version"}} {
	var stdout, stderr bytes.Buffer
	code := run(arguments, &stdout, &stderr)
	if code != exitOK || stdout.String() != "agentctl v0.1.0-test\n" || stderr.Len() != 0 {
		t.Fatalf("run(%q) = (%d, %q, %q), want (0, exact version line, empty stderr)", arguments, code, stdout.String(), stderr.String())
	}
}
```

Add a fake-runner test using `runWithRunner` that asserts `runner.Calls` remains empty for both forms. Add exact error assertions for `agentctl version extra` and `agentctl --version extra`: exit 2, empty stdout, and the applicable concise usage on stderr. Before writing, state the production change that makes these tests fail: recognizing and rendering version forms before tmux client/dependency construction.

- [ ] **Step 2: Run the focused CLI tests and verify RED**

Run:

```bash
go test ./cmd/agentctl -run 'TestRunVersion|TestRunRejectsVersionArguments' -count=1
```

Expected: the new tests fail because both forms are currently unknown commands.

- [ ] **Step 3: Implement early version dispatch**

Import `internal/buildinfo`, add `version` to `globalUsage`, and add:

```go
"version": "Usage: agentctl version\n",
```

At the beginning of `runWithRunner`, call an early helper before `tmuxx.New` or any resolver/collector construction:

```go
if handled, code := runVersion(arguments, stdout, stderr); handled {
	return code
}
```

Implement `runVersion(arguments []string, stdout, stderr io.Writer) (bool, int)` so:

- exactly `--version` prints `agentctl <buildinfo.Current()>` and succeeds;
- a first argument of `version` with no remainder prints the same line and succeeds;
- a first argument of `version` with any remainder returns a usage error with `Usage: agentctl version`;
- `--version` with a remainder is not handled and falls through to the existing unknown-command error.

- [ ] **Step 4: Verify CLI GREEN and regression behavior**

Run:

```bash
go test ./cmd/agentctl -run 'TestRunVersion|TestRunRejectsVersionArguments|TestRunRejectsUnknownCommandWithUsage' -count=1
```

Expected: all focused CLI tests pass and no runner call is recorded.

- [ ] **Step 5: Stamp project builds through Make**

Add these Make variables:

```make
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || printf development)
VERSION_PACKAGE := github.com/mnbf9rca/agentctl/internal/buildinfo
LDFLAGS := -X $(VERSION_PACKAGE).Stamp=$(VERSION)
```

Change the build recipe to:

```make
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/agentctl
```

Build and compare the binary output with the Makefile source fact:

```bash
make build
./bin/agentctl version
git describe --tags --always --dirty
./bin/agentctl --version
```

Expected: both binary commands print `agentctl ` followed by the exact `git describe` line and exit 0.

- [ ] **Step 6: Update README and release checklist**

Add a `version` command-reference entry before session-dependent commands:

```text
agentctl version
agentctl --version
```

Document the single-line output and the precedence: Makefile linker stamp, Go VCS revision with factual `+dirty`, then `development`. State that neither form touches tmux. In `docs/release-checklist.md` preconditions, add `./bin/agentctl version` to the exact versions recorded for the release candidate.

- [ ] **Step 7: Run complete verification**

Run:

```bash
gofmt -w internal/buildinfo/buildinfo.go internal/buildinfo/buildinfo_test.go cmd/agentctl/main.go cmd/agentctl/main_test.go
go test ./... -count=1
go vet ./...
go test -race ./... -count=1
go build ./...
go test -tags integration ./... -count=1
git diff --check origin/main...HEAD
```

Expected: every command exits 0, all tests pass, and the diff check prints nothing.

- [ ] **Step 8: Commit the CLI, build, and documentation changes**

```bash
git add cmd/agentctl/main.go cmd/agentctl/main_test.go Makefile README.md docs/release-checklist.md docs/superpowers/plans/2026-08-01-version-command.md
git commit -m "feat: report agentctl build version"
```

- [ ] **Step 9: Request review and publish the normal gate**

Perform an independent read-only review of `origin/main...HEAD`. Address verified findings with new failing tests before fixes. Then push `feat/version`, open a draft PR against `main` referencing issue #67, note the chosen `git describe` format and fallback precedence, include all validation evidence, and send the PR URL to the planner without merging it.
