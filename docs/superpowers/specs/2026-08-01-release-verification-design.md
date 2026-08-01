# Release Verification Tooling Design — 2026-08-01

Status: approved by the planner on `work/issue-13`.

Companion documents: [`2026-08-01-agentctl-design.md`](2026-08-01-agentctl-design.md), [`../../brief.md`](../../brief.md), [`../../../SECURITY.md`](../../../SECURITY.md), and GitHub issue #13.

## 1. Goal and evidence boundary

Issue #13 turns the 2026-08-01 tmux and real-harness spikes into repeatable release tooling. The tooling verifies the external behavior that unit tests deliberately cannot prove: tmux targeting semantics, real TUI input clearing, exact slash-command popup selection, conversation reset, and the popup-settle floor under CPU load.

The product never scrapes a TUI. The verification script may capture panes because it is an operator-run spike tool, not product code. Its observations are evidence, not a product API or an automated correctness oracle.

A loaded settle-floor result licenses a future engineer to evaluate a shorter `payloadDelay`; it does not select a new value by itself. Any tuning change must cite the loaded measurement and independently choose and justify a safety margin. Idle results are informative but never licensing.

## 2. Artifacts

The change creates these files:

- `hack/probe-1-argv.sh`: argv shapes, option round-tripping, format interpolation, exact-name behavior, and literal payload delivery.
- `hack/probe-2-targeting.sh`: unsafe name-prefix resolution, duplicate windows, option reads, and creation-ID output.
- `hack/probe-3-ids.sh`: exact session/window/pane ID targeting and creation records.
- `hack/probe-4-attach.sh`: attach and control-mode targeting by session ID.
- `hack/verify-injection.sh`: the shared real-harness driver with fixed verification and loaded measurement modes.
- `docs/release-checklist.md`: release procedure, pass criteria, evidence scope, and dated baseline results.

The four probes remain separate because each answers one external-contract question and can be cited and rerun independently. Their command bodies preserve the reviewer-posted probes. Each gains a unique socket name and signal/exit cleanup so an interruption cannot leave a probe server behind.

## 3. Injection verifier architecture

`hack/verify-injection.sh` is Bash 3.2-compatible for the repository's macOS environment. It accepts a small explicit CLI:

```text
hack/verify-injection.sh verify [--harness both|claude|codex] [--output DIR]
hack/verify-injection.sh measure [--harness both|claude|codex] [--output DIR]
                                 [--trials N] [--load-workers N]
```

Defaults are `both`, a unique directory below `/tmp`, 10 trials per candidate delay, and one CPU load worker per detected logical processor. Numeric options accept positive decimal integers only.

Both modes share one lifecycle:

1. Validate `tmux`, `claude`, `codex`, `yes`, and required POSIX utilities before creating a server.
2. Record the tmux and selected harness versions in the artifact directory.
3. Start selected real harnesses in detached windows on the unique socket `agentctl-injection-$$`; never invoke tmux without `-L`.
4. Ask the operator to confirm each TUI is ready before injecting keys.
5. Capture named pane snapshots for each observation point.
6. On normal exit or `HUP`, `INT`, or `TERM`, stop all load workers and kill only the unique tmux server.
7. Preserve the artifact directory and print its path for operator review; no snapshots are committed.

Harness windows run `claude` and `codex` without bypass flags. The tool verifies TUI behavior and does not authorize either harness to perform repository work.

## 4. Fixed verification mode

For each selected harness, `verify` reproduces spec §3.3:

1. Send a literal junk string and capture that pending input is visible.
2. Send `C-u` and capture that pending input is cleared.
3. Send literal `/clear` with `send-keys -l --`.
4. Wait the current product delay of 1000ms and capture the popup before submission.
5. Send `Enter` separately, wait for rendering, and capture the resulting reset conversation.
6. Record `#{pane_current_command}`.

The operator marks a harness pass only when all of these facts are visible:

- junk pending input was registered;
- `C-u` removed it;
- the exact `/clear` match was highlighted immediately before `Enter`;
- `Enter` reset the conversation; and
- the observed process name was recorded.

The script reports delivery and captures observations. It never claims that successful `send-keys` calls alone prove TUI execution.

## 5. Loaded popup-settle measurement

`measure` first starts the harnesses, then starts `/usr/bin/yes` workers equal to the selected `--load-workers` value. The default is the logical CPU count from `getconf _NPROCESSORS_ONLN`. It records the worker count and `uptime` output and waits ten seconds for load to establish before trials begin.

For each harness, candidate delays descend through:

```text
1000ms, 750ms, 500ms, 250ms, 100ms, 50ms, 0ms
```

At each delay the tool performs 10 consecutive `/clear` trials by default. Every trial captures the popup immediately before `Enter` and the TUI after submission. The operator reviews the batch and records pass or fail. A candidate passes only when every trial visibly highlights exact `/clear` and resets the conversation. Measurement stops descending after the first candidate with any failed trial; the smallest preceding all-pass candidate is the observed loaded floor. If 1000ms fails, no floor is recorded and the release check fails. If 0ms passes every trial, the observation is recorded as `0ms at the script's measurement resolution`, without claiming that scheduler or rendering latency is zero or bounded.

CPU saturation approximates one adverse scheduling condition. It does not bound real-world I/O contention, memory pressure, thermal throttling, remote-terminal latency, or future harness behavior. The checklist states this limitation next to the result.

## 6. Release checklist and baseline

`docs/release-checklist.md` explains:

- prerequisites and safe rerun commands for all four probes;
- the expected fact each probe establishes;
- fixed injection pass criteria for both harnesses;
- loaded measurement setup, batch review, stop condition, and evidence limitation;
- confirmation that no throwaway tmux server or load worker remains;
- the rule that only loaded results may support a future delay-tuning proposal; and
- a dated results table.

The first results entry records the already-established 2026-08-01 baseline:

- tmux 3.7b;
- Claude Code 2.1.220, process name `2.1.220`, `C-u` plus literal `/clear` plus separate `Enter` verified;
- codex-cli 0.146.0, process name `codex`, the same sequence verified.

The implementation run appends the observed loaded settle-floor result from this machine without changing `payloadDelay`.

## 7. Validation

Validation is proportional to the artifact type:

1. `bash -n hack/*.sh` validates every script's syntax.
2. Run all four tmux probes and confirm normal cleanup.
3. Run `verify` against both installed harnesses and inspect every named snapshot.
4. Run `measure` against both harnesses with CPU saturation, record the evidence and limitation, and confirm load-worker cleanup.
5. Confirm no `agentctl-*` throwaway tmux server remains.
6. Run `go test ./...` and `go vet ./...` to prove documentation/tooling changes did not disturb product code.

Harness-driving checks are manual release verification and are not added to CI.

## 8. Non-goals

- No change to `internal/tmuxx.payloadDelay` in issue #13.
- No product TUI scraping, activity inference, or generic key-injection API.
- No automatic interpretation of captured TUI output.
- No claim that CPU saturation covers all production load conditions.
- No committed terminal snapshots containing operator or repository context.
