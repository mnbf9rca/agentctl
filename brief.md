# Implementation brief: `agentctl`

## Objective

Build a standalone Go CLI named `agentctl` that:

1. Creates a named tmux session containing a fleet of autonomous agents.
2. Launches every agent through `amq coop exec`.
3. Records each agent’s role, harness and optional model.
4. Exposes only predefined terminal-control operations such as `clear` and `compact`.
5. Opens the fleet as native iTerm2 tabs using tmux control mode.
6. Reports basic fleet and process status without scraping agent output.

Example:

```bash
agentctl launch \
  --session epic123 \
  --roles planner:claude,codex1:codex,codex2:codex,codex-r:codex \
  --models planner:fable,codex-r:gpt5.6-sol-xhigh
```

Then:

```bash
agentctl attach --session epic123
agentctl clear --session epic123 codex2
agentctl compact --session epic123 codex-r
agentctl status --session epic123
```

## Architecture

Responsibilities remain separate:

* **GitHub epic/issues:** work graph, issues and waves.
* **Fable planner:** orchestration, allocation and test/review/merge policy.
* **AMQ:** durable communication and workflow protocols.
* **tmux:** process hosting, persistence and terminal input.
* **iTerm2:** native visual interface for the tmux windows.
* **agentctl:** fleet launch, metadata, predefined terminal controls and attachment.

Do not modify AMQ.

## Repository and installation

Maintain `agentctl` in its own Git repository outside application project directories, for example:

```text
~/src/agentctl
```

Implement it as a normal Go module with source, tests and documentation committed to that repository.

Build and install the binary at:

```text
~/.local/bin/agentctl
```

Provide a `Makefile` or equivalent commands for:

```bash
make build
make test
make install
```

`make install` should build and copy the binary to `~/.local/bin/agentctl`.

The installed command must work regardless of the current working directory and must not read or write files inside application repositories.

## CLI

Canonical commands:

```text
agentctl launch --session SESSION --roles ROLE:HARNESS,... [--models ROLE:MODEL,...]
agentctl attach [--session SESSION]
agentctl status [--session SESSION] [--json]
agentctl clear [--session SESSION] ROLE
agentctl compact [--session SESSION] ROLE
```

Do not implement the alternative `agentctl --launch` syntax.

### Fleet launch example

```bash
agentctl launch \
  --session epic123 \
  --roles planner:claude,codex1:codex,codex2:codex,codex3:codex,codex4:codex,reviewer-opus:claude,reviewer-codex:codex,designer:claude \
  --models planner:fable,reviewer-opus:opus-4.8,reviewer-codex:gpt5.6-sol-xhigh
```

## Roles, harnesses and models

Treat these as separate concepts:

* **Role:** stable fleet identity, tmux window name and AMQ handle.
* **Harness:** executable used to run the agent.
* **Model:** optional model identifier passed to the selected harness.

Initial supported harnesses:

```text
codex
claude
```

Fable is not a harness. It is a model or Claude configuration selected for the `claude` harness.

### `--roles`

`--roles` defines the complete fleet:

```text
planner:claude,codex1:codex,codex-r:codex
```

It is required for `launch`.

### `--models`

`--models` is optional:

```text
planner:fable,codex-r:gpt5.6-sol-xhigh
```

Requirements:

* Every model entry must reference a role present in `--roles`.
* Roles omitted from `--models` use the harness default.
* Duplicate role entries are invalid.
* Empty model identifiers are invalid.
* Model identifiers are opaque strings.
* Do not maintain a hardcoded catalogue of valid models.
* Pass models through using the appropriate harness-specific model argument.
* Store the selected model in tmux metadata.
* Show the model in `status`.

Centralise harness command construction so model syntax can be changed independently for each harness.

Conceptually:

```text
claude harness + fable model
→ claude --model fable

codex harness + gpt5.6-sol-xhigh model
→ codex --model gpt5.6-sol-xhigh
```

The final argument ordering must be compatible with `amq coop exec` and tested against the installed AMQ version.

## Validation

Session and role names must match:

```regex
^[a-z0-9][a-z0-9_-]*$
```

Reject:

* Unknown harnesses.
* Duplicate roles.
* Duplicate model entries.
* Models for undefined roles.
* Missing role, harness or model values.
* Empty `--roles`.
* Trailing commas.
* Whitespace in session, role or harness names.
* Session or role names beginning with `-`.
* Duplicate command-line options.

Before creating anything, confirm that these executables exist:

```text
tmux
amq
codex, when requested
claude, when requested
```

## Fleet launch

The tmux session name and AMQ session name must be identical:

```text
tmux session: epic123
AMQ session:  epic123
```

Given:

```bash
agentctl launch \
  --session epic123 \
  --roles planner:claude,codex1:codex,reviewer:claude \
  --models planner:fable
```

create these tmux windows:

```text
epic123:planner
epic123:codex1
epic123:reviewer
```

Each window must contain exactly one pane.

### First role

Equivalent behaviour:

```bash
tmux new-session -d \
  -s "$SESSION" \
  -n "$ROLE" \
  "exec amq coop exec --session '$SESSION' --me '$ROLE' '$HARNESS' ..."
```

### Remaining roles

Equivalent behaviour:

```bash
tmux new-window -d \
  -t "$SESSION" \
  -n "$ROLE" \
  "exec amq coop exec --session '$SESSION' --me '$ROLE' '$HARNESS' ..."
```

Do not build unsafe shell strings from unvalidated user input.

Prefer a small internal command-rendering layer with explicit quoting and unit tests.

### Process hierarchy

The intended hierarchy is:

```text
tmux pane
└── amq coop exec --session epic123 --me codex1 codex ...
    ├── AMQ wake helper
    └── exec() → codex
```

`amq coop exec` must remain the direct launcher of every agent.

Use `exec` so that, when the agent exits, no interactive shell remains behind to receive later commands.

Do not enable tmux `remain-on-exit` for managed windows.

## Launch failure and rollback

Validate the complete configuration before creating the tmux session.

If any role fails after session creation:

1. Stop creating further windows.
2. Kill the session created by that invocation.
3. Report the failed role.
4. Exit non-zero.

Example:

```text
agentctl: failed to launch reviewer-opus; removed incomplete session epic123
```

Never kill a session that existed before the current invocation.

By default, fail when the target session already exists.

Do not initially implement:

* Fleet mutation.
* Adding roles to an existing session.
* Automatic repair.
* Session replacement.
* A `--force` option.

An optional `--if-missing` may be implemented only if it verifies that the existing fleet exactly matches the requested role, harness and model configuration.

## tmux metadata

Set session-level options:

```text
@agentctl_managed = 1
@agentctl_version = 1
```

Set window-level options:

```text
@agentctl_managed = 1
@agentctl_role = planner
@agentctl_harness = claude
@agentctl_model = fable
```

For a role without an explicit model, store an empty model value or omit the option consistently.

Use this metadata for:

* Status reporting.
* Target validation.
* Harness/process validation.
* Exact fleet comparison.
* Refusing to control unmanaged windows.

Do not create a persistent metadata database.

## Session resolution

For commands other than `launch`, resolve the session in this order:

1. Explicit `--session`.
2. `AGENTCTL_SESSION`.
3. Current tmux session when invoked from inside tmux.

The current session may be obtained with:

```bash
tmux display-message -p '#{session_name}'
```

Fail when no session can be resolved.

`launch` always requires an explicit `--session`.

Do not infer sessions from:

* The current directory.
* A Git repository.
* `AM_ROOT`.
* `AM_SESSION`.
* The first tmux session returned by tmux.

## Predefined control commands

Initial operations:

```text
clear   → /clear
compact → /compact
```

The payload registry must be hardcoded.

The CLI must not support:

* Arbitrary prompts.
* Arbitrary slash commands.
* Raw key names.
* Caller-supplied text.
* stdin input.
* Options such as `--text`, `--command`, `--raw` or `--keys`.
* Environment variables that alter the payload.

### Target validation

Before sending a predefined command:

1. Confirm the session exists.
2. Confirm it is marked as agentctl-managed.
3. Resolve the exact window name.
4. Confirm the window is agentctl-managed.
5. Confirm stored role metadata matches.
6. Confirm the window contains exactly one pane.
7. Confirm the pane is alive.
8. Confirm the foreground process is consistent with the stored harness.

Use the resolved pane ID for delivery.

Do not use partial window-name matching.

### Foreground process checks

Expected basic mappings:

```text
codex harness  → codex foreground process
claude harness → claude foreground process
```

Allow only documented executable-name variations found during implementation.

Fail closed when the foreground process is unexpected:

```text
agentctl: refusing to send clear; epic123:codex2 is running zsh
```

This check is a safety guard. It does not prove that the agent is idle.

### Sending a command

Before inserting a slash command, clear unsubmitted input using a predefined harness-specific sequence.

Initial candidate:

```bash
tmux send-keys -t "$PANE_ID" C-u
```

Then send the slash command literally and submit it separately:

```bash
tmux send-keys -t "$PANE_ID" -l -- '/clear'
tmux send-keys -t "$PANE_ID" Enter
```

Requirements:

* Use literal mode for the slash command.
* Send Enter separately.
* Do not use `eval`.
* Do not execute the slash command through a shell.
* Do not scrape the TUI.
* Do not expose generic `send-keys`.

Manually verify the input-clearing sequence in current Codex and Claude Code versions. Hardcode any necessary harness-specific difference.

A successful result means tmux accepted the keystrokes. It does not prove that the TUI executed the slash command.

## Planner responsibility

The Fable planner remains responsible for determining when a role may be cleared or compacted.

It should only invoke a control command after the AMQ workflow confirms that:

1. Previous work is complete.
2. Required tests completed.
3. Required reviews completed.
4. Merge or handoff obligations completed.
5. The role has been released for reuse.

`agentctl` must not infer `idle`, `working`, `blocked` or `done` from terminal output.

## iTerm2 attachment

Implement:

```bash
agentctl attach --session epic123
```

It should run the equivalent of:

```bash
tmux -CC attach-session -t '=epic123'
```

This uses iTerm2’s tmux control mode.

When iTerm2 is configured to restore tmux windows as tabs, every managed tmux window appears as a native iTerm2 tab.

For the eight-role fleet:

```text
planner
codex1
codex2
codex3
codex4
reviewer-opus
reviewer-codex
designer
```

the user should see eight native iTerm2 tabs and be able to jump between them normally to inspect each agent.

Document the relevant iTerm2 setting:

```text
iTerm2 Settings
→ General
→ tmux
→ When attaching, restore windows as tabs in the attaching window
```

`attach` is an operator command, not a planner operation.

Requirements:

* Refuse attachment when the session does not exist.
* Refuse attachment when the session is not agentctl-managed.
* Clearly report when the command is not being run from iTerm2 or tmux control mode cannot be established.
* Do not create another session during attachment.

Detaching or closing iTerm2 must not terminate the underlying agents. Re-running `agentctl attach --session epic123` should reopen the tmux windows as native tabs.

## Status

Implement:

```bash
agentctl status --session epic123
agentctl status --session epic123 --json
```

Human-readable output should include:

```text
SESSION    ROLE             HARNESS   MODEL                 PANE   PROCESS   STATE
epic123    planner          claude    fable                 %12    claude    running
epic123    codex1           codex     default               %13    codex     running
epic123    reviewer-codex   codex     gpt5.6-sol-xhigh      %18    codex     running
```

JSON should use a versioned schema:

```json
{
  "schema": 1,
  "session": "epic123",
  "managed": true,
  "agents": [
    {
      "role": "planner",
      "harness": "claude",
      "model": "fable",
      "window": "planner",
      "pane_id": "%12",
      "process": "claude",
      "state": "running"
    }
  ]
}
```

State should remain limited to objective tmux/process facts, such as:

```text
running
dead
missing
unexpected-process
unmanaged
```

Do not report inferred workflow states.

## Exit codes

Suggested exit codes:

| Code | Meaning                                   |
| ---: | ----------------------------------------- |
|  `0` | Success                                   |
|  `2` | Invalid CLI usage                         |
|  `3` | Session resolution or session-state error |
|  `4` | Invalid or missing role/window            |
|  `5` | Unsafe or invalid pane/process state      |
|  `6` | tmux operation failed                     |
|  `7` | Required executable missing               |
|  `8` | Fleet launch failed and was rolled back   |

Errors must be concise and written to stderr.

## Go implementation

Use Go’s standard library where practical.

Suggested internal abstractions:

```text
Config parsing
Role/model validation
Harness command builder
tmux command runner interface
Fleet launcher
Rollback handler
Session resolver
Managed metadata reader/writer
Agent target resolver
Predefined command dispatcher
Status renderer
iTerm2 attachment command
```

Represent external command execution behind an interface so tests can inspect commands without launching real tmux processes.

Avoid large dependencies unless they clearly reduce complexity.

## Testing

Add unit and integration-style tests for:

### Parsing and validation

* Valid and invalid role lists.
* Valid and invalid model lists.
* Duplicate roles.
* Duplicate model assignments.
* Models assigned to undefined roles.
* Unknown harnesses.
* Invalid session and role names.
* Empty values and malformed separators.

### Launching

* First role uses `tmux new-session`.
* Later roles use `tmux new-window`.
* Every role launches through `amq coop exec`.
* `--session` and `--me` values are correct.
* Harness-specific model arguments are correct.
* Roles without models use the harness default.
* tmux metadata is recorded.
* Existing sessions fail.
* Missing executables fail before creation.
* Partial launch failures roll back the new session.
* Pre-existing sessions are never killed.
* Commands work from unrelated directories.

### Control commands

* `clear` maps only to `/clear`.
* `compact` maps only to `/compact`.
* Unknown operations are rejected.
* Arbitrary caller-controlled text cannot reach tmux.
* Unmanaged sessions and windows are rejected.
* Missing, dead and multi-pane targets are rejected.
* Unexpected foreground processes are rejected.
* Literal payload mode is used.
* Enter is sent separately.
* The resolved pane ID is targeted.

### Session resolution

* Explicit session takes precedence.
* `AGENTCTL_SESSION` is the second choice.
* Current tmux session is the fallback.
* Missing session context fails.

### Attachment

* Correct `tmux -CC attach-session` command is constructed.
* Missing or unmanaged sessions are rejected.
* Exact session matching is used.

### Status

* Human-readable output includes role, harness and model.
* JSON is valid and versioned.
* Dead and inconsistent panes are reported.
* No terminal screen contents are read.

Run:

```bash
go test ./...
go vet ./...
```

## Out of scope

Do not implement:

* Arbitrary terminal text or prompt submission.
* Generic tmux key injection.
* TUI output scraping.
* Agent activity inference.
* GitHub issue management.
* AMQ protocol changes.
* Worktree management.
* Fleet scaling after launch.
* Automatic agent restart.
* Automatic fleet repair.
* Session replacement.
* A daemon or background service.
* Herdr integration.

## Acceptance criteria

The implementation is complete when:

* A complete fleet launches with one command.
* Each role receives one named tmux window and one pane.
* Every agent launches through `amq coop exec`.
* Role, harness and optional model are stored and reported correctly.
* The tmux and AMQ session names are identical.
* Failed launches are rolled back safely.
* Multiple project fleets can coexist.
* `clear` and `compact` can be delivered only as predefined operations.
* Arbitrary caller-controlled terminal input is impossible.
* The fleet opens as native iTerm2 tabs using `agentctl attach`.
* The user can switch between the agent tabs to inspect their activity.
* The source lives in its own Git repository.
* The binary installs to `~/.local/bin/agentctl`.
* AMQ remains unchanged.
* `go test ./...` and `go vet ./...` pass.
