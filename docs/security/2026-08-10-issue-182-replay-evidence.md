# Issue #182 live incident replay — recovery-granularity evidence

Date: 2026-08-10 (Asia/Kuala_Lumpur)

## Outcome

The live throwaway-tmux replay confirms the three behavioral claims in paper §5, with an important recovery-order qualification:

1. A #182-style join made all four roster roles report `missing` while a single role-less absorbing window retained the four live harness panes.
2. Per-role `relaunch` succeeded for every vanished role window.
3. After those relaunches, closing only the absorbing window removed all four stale panes and left all four new role windows `running`.
4. `agentctl kill` followed by `agentctl launch` also recovered the fleet fully.

Operational qualification: the absorbing window is the session's only window immediately after the incident. Closing it before creating any replacement role window destroys the session; subsequent `status` and `relaunch` both return exit 3, `session not found`. Conversely, relaunching all roles before closing it temporarily leaves eight harness panes (four stale plus four replacements). Recovery documentation needs an explicit order, not just the list of actions.

## Scope and safety boundary

- Observation only; no product files were changed and nothing was committed.
- Source checkout: `/Users/rob/git/agentctl/.worktrees/issue-182-replay-evidence`
- Source commit: `3d0f690534a1a70e55c4367c5d9ff07de6fed4f3`
- Branch: `issue-182-replay-evidence`, created from freshly pulled `main`
- Binary: `/private/tmp/agentctl-issue182`, reporting `agentctl development`
- tmux: `/opt/homebrew/bin/tmux`, version `tmux 3.7b`
- Unique socket name: `agentctl-issue182-build2-20260810`
- Private socket directory: `/private/tmp/agentctl-issue182-build2-20260810/tmux`
- Private HOME: `/private/tmp/agentctl-issue182-build2-20260810/home`
- Stub harness pattern: a compiled `amq` stub parsed `coop exec`, then `exec`'d compiled stdin-blocking binaries named `claude` or `codex`, matching the integration fixture's stable exec-chain behavior. No real agent harness ran.
- Every tmux invocation carried either the wrapper's `-L agentctl-issue182-build2-20260810` or the literal `/opt/homebrew/bin/tmux -L agentctl-issue182-build2-20260810`.
- The default tmux server was never targeted.

Two fixture-only attempts were discarded before the evidence timeline: a shell-to-`sleep` stub produced transition-race `unexpected-process` rows, and a blocker named `amq` correctly produced `no-baseline` because launch refuses to stamp the intermediary. The final compiled exec-chain fixture produced four stable `running` baselines before the incident.

## Preparation commands

```sh
git pull --ff-only
# Already up to date.

git worktree add .worktrees/issue-182-replay-evidence -b issue-182-replay-evidence main
go test ./...
go build -buildvcs=false -o /private/tmp/agentctl-issue182 ./cmd/agentctl
```

Baseline `go test ./...` exited 0. The command built current main at the commit above.

All agentctl evidence commands used this literal environment prefix:

```sh
env PATH=/private/tmp/agentctl-issue182-build2-20260810/bin:/usr/bin:/bin:/usr/sbin:/sbin \
  HOME=/private/tmp/agentctl-issue182-build2-20260810/home \
  TMUX_TMPDIR=/private/tmp/agentctl-issue182-build2-20260810/tmux \
  TMUX= TMUX_PANE= AGENTCTL_SESSION= AGENTCTL_ROLE= AGENTCTL_MANAGED= \
  /private/tmp/agentctl-issue182 <arguments>
```

All direct tmux evidence commands used this literal prefix:

```sh
env TMUX_TMPDIR=/private/tmp/agentctl-issue182-build2-20260810/tmux \
  /opt/homebrew/bin/tmux -L agentctl-issue182-build2-20260810 <arguments>
```

The server was started with:

```sh
tmux -L agentctl-issue182-build2-20260810 start-server \; set-option -g exit-empty off
```

## Replay 1: per-role relaunch and absorber cleanup

### Launch and before-incident status

Exact agentctl suffix:

```sh
launch --session issue182-relaunch \
  --roles planner:claude,build1:codex,build2:codex,review:claude \
  --dir /Users/rob/git/agentctl/.worktrees/issue-182-replay-evidence
status --session issue182-relaunch
```

Observed status, exit 0:

```text
SESSION            ROLE     HARNESS  MODEL    EFFORT   PANE  PROCESS                                                    STATE
issue182-relaunch  planner  claude   default  default  %8    /private/tmp/agentctl-issue182-build2-20260810/bin/claude  running
issue182-relaunch  build1   codex    default  default  %9    /private/tmp/agentctl-issue182-build2-20260810/bin/codex   running
issue182-relaunch  build2   codex    default  default  %10   /private/tmp/agentctl-issue182-build2-20260810/bin/codex   running
issue182-relaunch  review   claude   default  default  %11   /private/tmp/agentctl-issue182-build2-20260810/bin/claude  running
```

The four original windows/panes were `@8/%8`, `@9/%9`, `@10/%10`, and `@11/%11`.

### Incident construction

Exact commands:

```sh
tmux -L agentctl-issue182-build2-20260810 new-window -d -t issue182-relaunch -n joined -P -F '#{window_id} #{pane_id}' -- sleep 3600
# @12 %12

tmux -L agentctl-issue182-build2-20260810 join-pane -d -s %8 -t %12
tmux -L agentctl-issue182-build2-20260810 join-pane -d -s %9 -t %12
tmux -L agentctl-issue182-build2-20260810 join-pane -d -s %10 -t %12
tmux -L agentctl-issue182-build2-20260810 join-pane -d -s %11 -t %12
tmux -L agentctl-issue182-build2-20260810 kill-pane -t %12
```

`kill-pane -t %12` removed only the placeholder; the four agent panes remained in `@12`. All four original role windows died when their final pane moved.

Post-incident status, exit 0:

```text
SESSION            ROLE     HARNESS  MODEL    EFFORT   PANE  PROCESS  STATE
issue182-relaunch  planner           default  default                 missing
issue182-relaunch  build1            default  default                 missing
issue182-relaunch  build2            default  default                 missing
issue182-relaunch  review            default  default                 missing
```

Direct tmux observation:

```text
@12|joined|4||1
@12|%11|claude
@12|%10|codex
@12|%9|codex
@12|%8|claude
```

The fourth field on the window record is `@agentctl_role`; it was empty. Thus the single absorbing window held exactly the roster-sized four panes but no role identity usable by status/relaunch.

### Per-role relaunches

Exact commands, each followed by `status --session issue182-relaunch`:

```sh
agentctl relaunch --session issue182-relaunch planner
agentctl relaunch --session issue182-relaunch build1
agentctl relaunch --session issue182-relaunch build2
agentctl relaunch --session issue182-relaunch review
```

All relaunch commands exited 0:

```text
agentctl: relaunched planner in issue182-relaunch: window @13, pane %13, harness claude (stored), model default (stored), effort default (stored), dir /Users/rob/git/agentctl/.worktrees/issue-182-replay-evidence (stored)
agentctl: relaunched build1 in issue182-relaunch: window @14, pane %14, harness codex (stored), model default (stored), effort default (stored), dir /Users/rob/git/agentctl/.worktrees/issue-182-replay-evidence (stored)
agentctl: relaunched build2 in issue182-relaunch: window @15, pane %15, harness codex (stored), model default (stored), effort default (stored), dir /Users/rob/git/agentctl/.worktrees/issue-182-replay-evidence (stored)
agentctl: relaunched review in issue182-relaunch: window @16, pane %16, harness claude (stored), model default (stored), effort default (stored), dir /Users/rob/git/agentctl/.worktrees/issue-182-replay-evidence (stored)
```

Status progression was monotonic: after planner, only planner was `running`; after build1, planner/build1 were `running`; after build2, planner/build1/build2 were `running`; after review, all four were `running`. Final table before absorber cleanup:

```text
SESSION            ROLE     HARNESS  MODEL    EFFORT   PANE  PROCESS                                                    STATE
issue182-relaunch  planner  claude   default  default  %13   /private/tmp/agentctl-issue182-build2-20260810/bin/claude  running
issue182-relaunch  build1   codex    default  default  %14   /private/tmp/agentctl-issue182-build2-20260810/bin/codex   running
issue182-relaunch  build2   codex    default  default  %15   /private/tmp/agentctl-issue182-build2-20260810/bin/codex   running
issue182-relaunch  review   claude   default  default  %16   /private/tmp/agentctl-issue182-build2-20260810/bin/claude  running
```

Direct tmux still showed eight panes at that point:

```text
@13|planner|1|planner|1
@14|build1|1|build1|1
@15|build2|1|build2|1
@16|review|1|review|1
@12|joined|4||1
@13|%13|claude
@14|%14|codex
@15|%15|codex
@16|%16|claude
@12|%11|claude
@12|%10|codex
@12|%9|codex
@12|%8|claude
```

This is a critical factual distinction: status truthfully reported the replacement role windows as `running`, but that claim does not imply the stale panes were gone.

### Close only the absorbing window

Exact command:

```sh
tmux -L agentctl-issue182-build2-20260810 kill-window -t @12
```

Exit 0. Status still reported all four roles `running`. Direct tmux observation then showed exactly four one-pane role windows and no `joined` window:

```text
@13|planner|1|planner
@14|build1|1|build1
@15|build2|1|build2
@16|review|1|review
@13|%13|claude
@14|%14|codex
@15|%15|codex
@16|%16|claude
```

This confirms that no replacement role window needed cleanup; only the absorber contained stale panes.

## Replay 2: full-session kill plus launch

The same join sequence was repeated with absorber `@17/%17` and role panes `%13` through `%16`. Status again reported all four roles `missing`.

Exact recovery commands:

```sh
agentctl kill --session issue182-relaunch
agentctl status --session issue182-relaunch
agentctl launch --session issue182-relaunch \
  --roles planner:claude,build1:codex,build2:codex,review:claude \
  --dir /Users/rob/git/agentctl/.worktrees/issue-182-replay-evidence
agentctl status --session issue182-relaunch
```

`kill` exited 0. The immediate status exited 3 and printed:

```text
agentctl: session "issue182-relaunch" not found
```

`launch` and final status exited 0:

```text
SESSION            ROLE     HARNESS  MODEL    EFFORT   PANE  PROCESS                                                    STATE
issue182-relaunch  planner  claude   default  default  %18   /private/tmp/agentctl-issue182-build2-20260810/bin/claude  running
issue182-relaunch  build1   codex    default  default  %19   /private/tmp/agentctl-issue182-build2-20260810/bin/codex   running
issue182-relaunch  build2   codex    default  default  %20   /private/tmp/agentctl-issue182-build2-20260810/bin/codex   running
issue182-relaunch  review   claude   default  default  %21   /private/tmp/agentctl-issue182-build2-20260810/bin/claude  running
```

Direct tmux showed exactly those four one-pane role windows, with no stale absorber.

## Replay 3: cleanup-before-relaunch ordering check

The join was repeated with absorber `@22/%22` and role panes `%18` through `%21`. Before any relaunch, the following exact command closed the sole remaining session window:

```sh
tmux -L agentctl-issue182-build2-20260810 kill-window -t @22
```

Observed:

```text
kill-window exit: 0
agentctl status --session issue182-relaunch exit: 3
agentctl: session "issue182-relaunch" not found
agentctl relaunch --session issue182-relaunch planner exit: 3
agentctl: session "issue182-relaunch" not found
```

Therefore the per-role recovery path cannot begin by closing the entire absorbing window. At least one replacement window must exist to preserve the session, or the operator must choose the full-session `kill` + `launch` path. Inference for documentation from the observed endpoints: a lower-duplication per-role order is relaunch one role, close the absorber, then relaunch the remaining missing roles. That exact optimized sequence was not separately replayed here; the constituent behaviors were observed.

## Code-reading cross-check

Current main implements the absent-role gate in `internal/fleet/relaunch.go:578-591`:

- it lists windows in the resolved session;
- it matches `window.Name == role`;
- zero matching windows returns an accepted classification;
- otherwise only a bounded `no-baseline` recovery case is accepted.

The joined absorber was named `joined`, so every role had zero matching windows even though the four old harness processes remained live in its panes. This explains the successful live relaunches.

## Divergences and documentation qualifications

1. **No behavioral contradiction in the three core §5 recovery claims.** Per-role relaunch, absorber-only stale-pane cleanup, and session kill+launch all worked live.
2. **Recovery order is load-bearing but absent from §5.** Closing the absorber first destroyed the session. Relaunching all roles first worked but temporarily produced eight live harness panes. Documentation must state an order and the transient-duplicate consequence.
3. **The proposed aggregate status note is not present on this current-main revision.** Post-incident `status` printed only the four `missing` roster rows; it emitted no note about the role-less four-pane absorber. This is an implementation gap if §5's proposed interim note is expected to ship, not a contradiction in the underlying aggregate observation.
4. **The paper's code identifier is stale.** §5 names `requireAbsentWindow`; current main has no symbol by that name. The operative implementation is `classifyRelaunchWindow` in `internal/fleet/relaunch.go:578-591`. The semantics claimed by the paper were nevertheless observed.
5. **`running` does not claim stray-pane cleanup.** After all four relaunches, status correctly reported all replacement roles `running` while direct tmux still showed the four old panes in the absorber. Recovery docs must require the direct operator cleanup action rather than treating green status as evidence that old panes are gone.

## Cleanup evidence

Exact commands:

```sh
env TMUX_TMPDIR=/private/tmp/agentctl-issue182-build2-20260810/tmux \
  /opt/homebrew/bin/tmux -L agentctl-issue182-build2-20260810 kill-server
env TMUX_TMPDIR=/private/tmp/agentctl-issue182-build2-20260810/tmux \
  /opt/homebrew/bin/tmux -L agentctl-issue182-build2-20260810 list-sessions
```

Observed:

```text
kill-server exit: 0
list-sessions exit: 1
no server running on /private/tmp/agentctl-issue182-build2-20260810/tmux/tmux-501/agentctl-issue182-build2-20260810
```

The throwaway server was gone at report time.
