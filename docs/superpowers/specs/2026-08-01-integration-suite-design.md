# Integration Suite Design

**Status:** Approved by the planner on 2026-08-01 for issue #16.

## Purpose

Add a CI-safe integration suite that exercises the CLI dispatcher and the
production launch, status, control, and kill paths against real tmux. The suite
must never touch the user's default tmux server, start a real agent, create AMQ
state, use the network, or spend API quota.

The normative behavioral requirements remain in the agentctl design spec,
especially §10. This document records the test architecture and the decisions
specific to issue #16.

## Chosen boundary

Integration tests live in package `main` and call `runWithRunner`. They supply
a test-only `socketRunner` implementing `tmuxx.Runner`:

- calls whose executable is `tmux` execute the absolute real tmux binary with
  `-L agentctl-test-<random>` prepended;
- all other calls, notably `ps`, execute normally through `os/exec`;
- one test may arm a one-shot failure for a selected tmux operation to exercise
  rollback after ownership has been established.

This is the intended end-to-end boundary. `RealRunner` is deliberately not
covered here: it is a thin `os/exec` adapter with unit coverage, while using it
would require a shell wrapper in `PATH` to inject `-L`. The selected Runner
boundary keeps socket containment structural and non-bypassable within the
suite.

No production flag, environment variable, socket option, or test hook is
added.

## Fixture and stub processes

Every test creates an independent fixture with:

- a cryptographically random socket name carrying the `agentctl-test-` prefix;
- a test-owned `TMUX_TMPDIR` whose entire socket tree is removed after the
  scoped server has stopped;
- a temporary `bin` directory prepended to `PATH`;
- stub `amq`, `claude`, and `codex` executables;
- role-specific invocation and input-capture files;
- cleanup registered before the first tmux creation attempt.

The `amq` stub accepts the launch shape produced by the real harness package,
records the selected session and role, exports the role to its child, and
`exec`s the requested harness. Harness stubs then `exec` the current Go test
binary in marker mode. Marker mode remains alive as the pane root process,
reads terminal input line by line, and appends it to the role's capture file.
Model arguments are accepted and recorded but do not affect marker behavior.

This shape exercises real tmux window processes and the launch-time process
baseline without invoking installed AMQ or agent harnesses. It also makes
control delivery observable: the terminal line discipline handles the input
clear key, and the marker records the submitted literal line.

The fixture blanks `TMUX_PANE` before control commands. The developer or CI
worker may itself be inside an unrelated tmux server whose pane ID happens to
equal a throwaway-server pane ID; carrying that value into the test would
trigger the self-target guard even though the sockets differ.

## Isolation and cleanup invariants

The real tmux path is resolved before the fixture mutates `PATH`. Every helper
that starts, queries, mutates, or stops tmux routes through the same
socket-scoped execution function. Tests never run in parallel because their
stub selection uses process-global environment.

Immediately after choosing the socket, the fixture registers `t.Cleanup` that
uses the absolute real tmux binary and the same `-L` name to issue
`kill-server` under a bounded context. Cleanup tolerates only the expected
already-absent server case and runs after failures, panics, and `t.Fatal`.
The short, test-owned socket directory and other temporary directories are
removed by registered test cleanup after the server has been stopped, so tmux
cannot leave stale socket nodes in the user's global temporary tmux directory.

The suite never inspects, creates a sentinel in, or otherwise contacts the
default tmux server. Containment is proven by the absence of any integration
helper capable of invoking tmux without its socket name.

## Coverage organization

`cmd/agentctl/integration_fixture_test.go` owns the fixture, Runner, stub
scripts, marker-mode `TestMain`, tmux helpers, and bounded polling helpers.

`cmd/agentctl/integration_launch_test.go` covers:

- successful two-role launch, window topology, launch directory, metadata,
  and the process baseline compared with the process observed from the real
  pane PID;
- refusal when the throwaway server already contains the requested session,
  proving the pre-existing sentinel remains alive;
- a one-shot failure during a later window creation, proving the partially
  owned session is rolled back.

`cmd/agentctl/integration_lifecycle_test.go` covers:

- status while both marker panes are live;
- status after one non-remain-on-exit pane/window is killed, which is reported
  as missing according to the normative status contract;
- `clear` delivery reaching the target role's marker input file, with no input
  reaching the sibling;
- `kill` removing the managed throwaway session.

Assertions operate on returned exit status, parsed CLI output, real tmux
state, and marker side effects. They do not mirror production builders or
assert merely that a fake was called.

## CI

The existing Linux unit job remains unchanged. A separate macOS integration
job installs Homebrew tmux, pins the installed keg, and asserts `tmux 3.7b`
before running `go test -tags integration ./...`. The version assertion makes
formula drift fail closed until the integration baseline is deliberately
revalidated and updated.

The job is a hard gate. It never conditionally skips because a green skipped
job would provide no integration evidence.

## Non-goals

- No real Claude Code, Codex, or AMQ execution.
- No TUI capture or scraping.
- No testing of tmux's default socket.
- No change to product timing, metadata, validation, target resolution, or
  command delivery behavior.
- No general-purpose shell fixture framework.
