# Version Command Design — 2026-08-01

Status: approved by the planner on the `work/issue-67` AMQ thread.

## Goal

Give operators a dependency-free way to identify the `agentctl` binary they
are running without claiming a build identity the binary does not carry.

## Interface

Both of these exact invocations print one line to stdout and exit 0:

```text
agentctl version
agentctl --version
```

The line has the form `agentctl IDENTITY`. Neither form touches tmux, resolves
a session, nor initializes command dependencies. `agentctl version` accepts no
options or positional arguments; extras produce its concise usage on stderr
and exit 2. The `--version` alias is accepted only as the sole argument, so it
cannot hide malformed command invocations.

## Build identity

A focused `internal/buildinfo` package exposes the current identity. It selects
the first fact available in this order:

1. a nonempty linker stamp supplied by the Makefile;
2. Go build information's nonempty `vcs.revision`, with `+dirty` appended when
   `vcs.modified=true`;
3. the literal `development` when no build identity was recorded.

An explicit stamp wins even when Go also embedded VCS settings. A clean VCS
revision is printed unchanged. The dirty suffix states only the recorded dirty
fact; no tag, semantic version, or release status is inferred. The final
fallback makes an unstamped build's lack of recorded identity explicit while
keeping the successful query at exit 0, as required by the factual-output rule
in the main design's section 1.1.

`make build` and `make install` pass `git describe --tags --always --dirty` as
the linker stamp. Direct `go build` remains valid and uses the VCS or
`development` fallback instead.

## Code and documentation boundaries

- `internal/buildinfo` owns stamp selection and rendering of VCS dirtiness.
- `cmd/agentctl` owns CLI recognition, exact output, usage, and exit codes.
- `Makefile` owns the ldflags stamp for project-built binaries.
- `README.md` documents both invocations and the identity precedence.
- `docs/release-checklist.md` requires recording `./bin/agentctl version` with
  the other observed release-tool versions.

No generated source file, build-time mutation, network lookup, or tmux probe is
introduced.

## Verification

Unit tests cover linker-stamp precedence, clean and dirty Go VCS identities,
malformed/absent settings, and the `development` fallback. CLI tests cover both
successful forms, exact single-line stdout, empty stderr, zero dependency
calls, and rejection of extra `version` arguments. Build verification runs the
Makefile-built binary and compares its output with the computed Makefile stamp,
then the full unit, vet, race, integration, and build gates run unchanged.
