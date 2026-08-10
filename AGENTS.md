# Working on agentctl

This file is the repository-wide instruction source for agents changing
`agentctl`. `CLAUDE.md` contains only the import line `@AGENTS.md` so Codex
and Claude Code read the same rules from this one file.

This file is about working **on** agentctl. The agent-facing skill tracked in
[#78](https://github.com/mnbf9rca/agentctl/issues/78) is about **using** agentctl
to operate a fleet. Keep those concerns separate.

## Sources of truth

- The current issue body, including every `AMENDED YYYY-MM-DD` section, is the
  complete contract for that piece of work. Implement its acceptance criteria
  and stay inside its scope.
- The approved [design spec](docs/superpowers/specs/2026-08-01-agentctl-design.md)
  governs product behavior and architecture. It wins over the older
  [implementation brief](docs/brief.md) where they differ.
- [SECURITY.md](SECURITY.md) governs the threat model and security invariants.
  Read it before changing validation, command construction, targeting,
  metadata gates, process checks, or payload delivery.
- The [release checklist](docs/release-checklist.md) governs live verification
  when a release changes tmux targeting, harness startup, or injected command
  delivery.
- Do not copy design rationale into this file. State the working rule and link
  to the governing section so the rationale has one home.

## Hard constraints

- Keep the Go module standard-library-first. Add a third-party Go dependency
  only when it clearly reduces complexity, and justify each addition in the
  governing spec and PR.
- Production external-command execution goes through an interface. tmux and
  process calls use `internal/tmuxx.Runner`; tests use fakes that record exact
  executable and argv elements.
- agentctl never invokes a shell. The one shell-interpreted value is the tmux
  window command assembled at the site pinned by
  [spec §12.1](docs/superpowers/specs/2026-08-01-agentctl-design.md#12-pinned-clarifications-2026-08-01-post-kickoff)
  from validated argv via `internal/shellq`.
- Never add a path for caller-supplied text, arbitrary slash commands, or raw
  keys to reach `tmux send-keys`. The payload registry is closed and
  argument-free.
- Apply
  [spec §1.1](docs/superpowers/specs/2026-08-01-agentctl-design.md#11-principle-every-output-is-a-factual-claim)
  to every output, exit code, and state: do not infer facts from terminal
  contents, report success that was not observed, hide a known defect, or
  silently omit an expected role.
- Do not modify AMQ as part of agentctl work.

## Repository map

- `cmd/agentctl/` owns CLI parsing, dependency wiring, output, and exit-code
  mapping. Integration tests with the `integration` build tag also live here.
- `internal/` contains the product packages. Preserve the boundaries in
  [spec §5](docs/superpowers/specs/2026-08-01-agentctl-design.md#5-architecture),
  especially `config` for value validation, `harness` for harness argv,
  `shellq` for shell-word quoting, `tmuxx` for typed command execution,
  `fleet` for launch/rollback, and `target`/`control` for fail-closed delivery.
- `hack/` contains probes, release tooling, and CI scripts. Go tests in that
  directory execute scripts against temporary fixtures and are part of
  `go test ./...`.
- `docs/superpowers/specs/` contains approved design contracts.
  `docs/superpowers/plans/` contains non-normative implementation plans.
- `.github/workflows/ci.yml` is the live CI definition; inspect it rather than
  relying on remembered checks or versions.

## Testing and evidence

- Use TDD for behavior changes: establish the failing test, make the smallest
  change that passes it, and include the red/green evidence in the PR.
- Assert exact argv against the fake `Runner`, including element boundaries,
  target IDs, and call order. Metadata stamping order is a tested contract in
  [spec §6.5](docs/superpowers/specs/2026-08-01-agentctl-design.md#65-metadata),
  not a comment that may be reordered casually.
- Start with focused tests, then run at minimum:

  ```bash
  go test ./...
  go vet ./...
  ```

- Match the additional gates in `.github/workflows/ci.yml`: `shellcheck
  hack/*.sh`, `golangci-lint`, the real-tmux suite `go test -tags integration
  ./...`, and the release snapshot checks where relevant.
- Integration tests use a throwaway tmux socket and stub harnesses. Never point
  tests or probes at the user's default tmux server or real agents.
- Before reporting gates, fetch and rebase onto current `main`. A green run on
  a stale branch is not merge evidence. After pushing, quote the PR's own
  `pull_request` CI run, which tests the merge result.

## Security-relevant changes

- Review the complete path before editing it: validation in
  `internal/config`, payload registration in `internal/control/registry.go`,
  the target-validation chain in `internal/target`, quoting in
  `internal/shellq`, window-command assembly in `internal/fleet`, and typed
  command argv in `internal/tmuxx`.
- Preserve exact ID targeting and the fail-closed chain described in
  [spec §§6.2, 12, and 13](docs/superpowers/specs/2026-08-01-agentctl-design.md).
- Update `SECURITY.md` in the same PR whenever security behavior, assumptions,
  or residual risk changes.
- A spec edit that describes argv, metadata, or output implemented by the PR
  rides in that PR. A spec edit that creates new surface or semantics is a
  design delta: obtain reviewer agreement and land it before or with the
  implementation.

## Worktrees, signing, and Git

- Invoke the `/using-git-worktrees` skill before starting work. The skill owns
  isolation detection, native-tool preference, fallback placement, and ignore
  checks; do not duplicate that logic in dispatch instructions.
- The repository fallback is the ignored `.worktrees/` directory. After
  opening a PR, detach its worktree so the branch is not held at merge time.
- Commits must be SSH-signed through 1Password. Before batch work, prove the
  signing path in a throwaway repository with an empty signed commit and
  inspect it using `git log --show-signature`; remove the throwaway repository
  afterward.
- Sandboxed agents run `git commit` and `git push` through the command-approval
  flow so the 1Password agent socket is reachable. A paused signing command is
  usually waiting for biometric approval; allow about two minutes before
  treating it as blocked.
- Work on a topic branch from current `main`. Keep commits focused and signed.
  Do not mix unrelated cleanup into a PR.

## Issues, review, and merge

- Issue bodies are self-contained. When scope changes, edit the body, add an
  `AMENDED YYYY-MM-DD` marker, and fold in the load-bearing rationale. Remove
  superseded text; edit history preserves it. Do not construct the current
  contract from a comment trail.
- Point new agents at this file instead of repeating repository boilerplate in
  every issue or dispatch message; keep task-specific contracts in issue bodies.
- Comments are reserved for artifacts that must stand alone, such as reviewer
  gate verdicts and externally consumed evidence. Gate verdicts use PR comments
  because all agents share the PR author's GitHub identity, so GitHub refuses
  `gh pr review --approve`; each verdict states its blocking status in its own
  text because the checks tab cannot show it.
- Fix review findings in the PR that exposed them, including findings labeled
  non-blocking, or absorb them into an already-open PR touching the same code.
  A follow-up issue requires maintainer-visible justification for genuinely
  out-of-scope design work.
- Every release-scoped issue belongs to that release's milestone as soon as it
  enters scope, including issues created and closed during the batch.
- Nothing merges without a reviewer gate. `main` is protected for pull
  requests and the required `test` context with admin enforcement; repository
  settings allow squash merges only.
- Do not merge your own PR. After the reviewer releases it, the planner or
  maintainer performs the merge.
- PR bodies name the issue relationship (`Closes`, `Refs`), summarize the
  contract and impact, and record verification. Rebase onto current `main`,
  push, wait for the PR's own CI, then request the reviewer gate with the exact
  run URL.
