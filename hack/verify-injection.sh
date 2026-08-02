#!/bin/bash
set -u

usage() {
  cat <<'EOF'
Usage:
  hack/verify-injection.sh verify [--harness both|claude|codex] [--output DIR]
  hack/verify-injection.sh measure [--harness both|claude|codex] [--output DIR]
                                   [--trials N] [--load-workers N]
                                   [--capture-pre-enter]

Runs real Claude Code and/or Codex harnesses inside an isolated tmux server.
All TUI snapshots are preserved in the printed artifact directory for review.
EOF
}

die() {
  printf 'verify-injection: %s\n' "$*" >&2
  exit 2
}

MODE=''
HARNESS=both
TRIALS=10
LOAD_WORKERS=$(getconf _NPROCESSORS_ONLN 2>/dev/null || printf '1')
OUTPUT=''
OUTPUT_EXPLICIT=0
CAPTURE_PRE_ENTER=0
SOCKET="agentctl-injection-$$"
SESSION=agentctl-injection
printf 'ATTACH: tmux -L %s attach -t %s\n' "$SOCKET" "$SESSION"
SESSION_ID=''
CLAUDE_PANE=''
CODEX_PANE=''
LOAD_PIDS=''

if [ "$#" -eq 0 ]; then
  usage >&2
  exit 2
fi

if [ "$1" = '--help' ] || [ "$1" = '-h' ]; then
  usage
  exit 0
fi

MODE=$1
shift

while [ "$#" -gt 0 ]; do
  case "$1" in
    --help|-h)
      usage
      exit 0
      ;;
    --harness)
      [ "$#" -ge 2 ] || die '--harness requires a value'
      HARNESS=$2
      shift 2
      ;;
    --output)
      [ "$#" -ge 2 ] || die '--output requires a value'
      OUTPUT=$2
      OUTPUT_EXPLICIT=1
      shift 2
      ;;
    --trials)
      [ "$#" -ge 2 ] || die '--trials requires a value'
      TRIALS=$2
      shift 2
      ;;
    --load-workers)
      [ "$#" -ge 2 ] || die '--load-workers requires a value'
      LOAD_WORKERS=$2
      shift 2
      ;;
    --capture-pre-enter)
      CAPTURE_PRE_ENTER=1
      shift
      ;;
    *)
      die "unsupported argument: $1"
      ;;
  esac
done

case "$MODE" in
  verify|measure) ;;
  *) die "unsupported mode: $MODE" ;;
esac

case "$HARNESS" in
  both|claude|codex) ;;
  *) die "unsupported harness: $HARNESS" ;;
esac

case "$TRIALS" in
  ''|*[!0-9]*|0) die '--trials must be a positive decimal integer' ;;
esac

case "$LOAD_WORKERS" in
  ''|*[!0-9]*|0) die '--load-workers must be a positive decimal integer' ;;
esac

if [ "$CAPTURE_PRE_ENTER" -eq 1 ] && [ "$MODE" != measure ]; then
  die '--capture-pre-enter is valid only in measure mode'
fi

tmux_cmd() {
  tmux -L "$SOCKET" "$@"
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

cleanup() {
  for pid in $LOAD_PIDS; do
    kill "$pid" >/dev/null 2>&1 || true
  done
  for pid in $LOAD_PIDS; do
    wait "$pid" >/dev/null 2>&1 || true
  done
  tmux_cmd kill-server >/dev/null 2>&1 || true
  if [ -n "$OUTPUT" ] && [ -d "$OUTPUT" ]; then
    printf 'Artifacts preserved at %s\n' "$OUTPUT"
  fi
}

trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

sleep_ms() {
  ms=$1
  [ "$ms" -eq 0 ] && return 0
  sleep "$(printf '%d.%03d' "$((ms / 1000))" "$((ms % 1000))")"
}

capture_snapshot() {
  harness=$1
  phase=$2
  pane=$3
  tmux_cmd capture-pane -p -S - -t "$pane" >"$OUTPUT/${harness}-${phase}.txt"
}

write_metadata() {
  {
    printf 'date_utc=%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    printf 'mode=%s\n' "$MODE"
    printf 'harness=%s\n' "$HARNESS"
    printf 'trials=%s\n' "$TRIALS"
    printf 'load_workers=%s\n' "$LOAD_WORKERS"
    printf 'capture_pre_enter=%s\n' "$CAPTURE_PRE_ENTER"
    printf 'working_directory=%s\n' "$PWD"
    printf 'tmux_version=%s\n' "$(tmux_cmd -V)"
    if [ "$HARNESS" = both ] || [ "$HARNESS" = claude ]; then
      printf 'claude_version=%s\n' "$(claude --version 2>&1 | sed -n '1p')"
    fi
    if [ "$HARNESS" = both ] || [ "$HARNESS" = codex ]; then
      printf 'codex_version=%s\n' "$(codex --version 2>&1 | sed -n '1p')"
    fi
    printf 'uptime_before=%s\n' "$(uptime)"
  } >"$OUTPUT/metadata.txt"
  printf 'mode\tharness\tdelay_ms\ttrials\tload_workers\tresult\tdetail\n' >"$OUTPUT/results.tsv"
}

start_first_harness() {
  harness=$1
  created=$(tmux_cmd new-session -d -s "$SESSION" -n "$harness" -c "$PWD" -P -F '#{session_id} #{pane_id}' -- "$harness") || die "failed to start $harness"
  SESSION_ID=${created%% *}
  pane=${created#* }
  case "$harness" in
    claude) CLAUDE_PANE=$pane ;;
    codex) CODEX_PANE=$pane ;;
  esac
}

start_next_harness() {
  harness=$1
  pane=$(tmux_cmd new-window -d -t "$SESSION_ID" -n "$harness" -c "$PWD" -P -F '#{pane_id}' -- "$harness") || die "failed to start $harness"
  case "$harness" in
    claude) CLAUDE_PANE=$pane ;;
    codex) CODEX_PANE=$pane ;;
  esac
}

start_harnesses() {
  case "$HARNESS" in
    claude)
      start_first_harness claude
      ;;
    codex)
      start_first_harness codex
      ;;
    both)
      start_first_harness claude
      start_next_harness codex
      ;;
  esac
  tmux_cmd resize-window -t "$SESSION_ID" -x 160 -y 50
}

verify_harness() {
  harness=$1
  pane=$2

  printf '\nWaiting for %s in pane %s.\n' "$harness" "$pane"
  printf 'Press Enter only after its TUI is fully ready: '
  IFS= read -r _ready

  tmux_cmd send-keys -t "$pane" -l -- 'agentctl-verification-junk'
  sleep_ms 500
  capture_snapshot "$harness" junk "$pane"

  tmux_cmd send-keys -t "$pane" C-u
  sleep_ms 500
  capture_snapshot "$harness" cleared "$pane"

  tmux_cmd send-keys -t "$pane" -l -- '/clear'
  sleep_ms 1000
  capture_snapshot "$harness" popup "$pane"
  tmux_cmd send-keys -t "$pane" C-u

  tmux_cmd send-keys -t "$pane" -l -- '/clear'
  sleep_ms 1000
  tmux_cmd send-keys -t "$pane" Enter
  sleep_ms 2000
  capture_snapshot "$harness" reset "$pane"

  process=$(tmux_cmd display-message -p -t "$pane" '#{pane_current_command}')
  printf 'Process: %s\n' "$process"
  printf 'Review snapshots:\n  %s\n  %s\n  %s\n  %s\n' \
    "$OUTPUT/${harness}-junk.txt" \
    "$OUTPUT/${harness}-cleared.txt" \
    "$OUTPUT/${harness}-popup.txt" \
    "$OUTPUT/${harness}-reset.txt"
  printf 'Did junk appear, C-u clear it, exact /clear match, and the conversation reset? [y/N] '
  IFS= read -r answer
  case "$answer" in
    y|Y)
      printf 'verify\t%s\t1000\t1\t0\tPASS\tprocess=%s; operator-attested\n' "$harness" "$process" >>"$OUTPUT/results.tsv"
      return 0
      ;;
    *)
      printf 'verify\t%s\t1000\t1\t0\tFAIL\tprocess=%s; operator-rejected\n' "$harness" "$process" >>"$OUTPUT/results.tsv"
      return 1
      ;;
  esac
}

wait_for_harness() {
  harness=$1
  pane=$2
  printf '\nWaiting for %s in pane %s.\n' "$harness" "$pane"
  printf 'Press Enter only after its TUI is fully ready: '
  IFS= read -r _ready
}

start_load() {
  printf '\nWARNING: measure mode will saturate %s logical CPUs with /usr/bin/yes.\n' "$LOAD_WORKERS"
  printf 'This may make the machine temporarily sluggish. Start the load? [y/N] '
  IFS= read -r answer
  case "$answer" in
    y|Y) ;;
    *) die 'CPU load was declined' ;;
  esac

  printf 'uptime_pre_load=%s\n' "$(uptime)" >>"$OUTPUT/metadata.txt"
  worker=1
  while [ "$worker" -le "$LOAD_WORKERS" ]; do
    /usr/bin/yes >/dev/null &
    pid=$!
    LOAD_PIDS="$LOAD_PIDS $pid"
    worker=$((worker + 1))
  done
  printf 'load_pids=%s\n' "$LOAD_PIDS" >>"$OUTPUT/metadata.txt"
  printf 'Stabilizing CPU load for 10 seconds...\n'
  sleep 10
  printf 'uptime_post_stabilization=%s\n' "$(uptime)" >>"$OUTPUT/metadata.txt"
}

measure_candidate() {
  harness=$1
  pane=$2
  delay=$3
  trial=1

  printf '\n%s: running %s trial(s) at %sms under %s workers.\n' \
    "$harness" "$TRIALS" "$delay" "$LOAD_WORKERS"
  while [ "$trial" -le "$TRIALS" ]; do
    tmux_cmd send-keys -t "$pane" C-u
    tmux_cmd send-keys -t "$pane" -l -- '/clear'
    sleep_ms "$delay"
    capture_snapshot "$harness" "measure-${delay}ms-trial-${trial}-popup" "$pane"
    tmux_cmd send-keys -t "$pane" C-u

    tmux_cmd send-keys -t "$pane" -l -- '/clear'
    sleep_ms "$delay"
    if [ "$CAPTURE_PRE_ENTER" -eq 1 ]; then
      tmux_cmd capture-pane -p -S - -t "$pane" \; \
        send-keys -t "$pane" Enter \
        >"$OUTPUT/${harness}-measure-${delay}ms-trial-${trial}-pre-enter.txt"
    else
      tmux_cmd send-keys -t "$pane" Enter
    fi
    sleep_ms 2000
    capture_snapshot "$harness" "measure-${delay}ms-trial-${trial}-reset" "$pane"

    printf '  completed trial %s/%s\n' "$trial" "$TRIALS"
    trial=$((trial + 1))
  done

  printf 'Review %s/%s-measure-%sms-trial-*-{popup,reset}.txt\n' "$OUTPUT" "$harness" "$delay"
  printf 'Did every trial show exact /clear selection and a completed reset? [y/N] '
  IFS= read -r answer
  case "$answer" in
    y|Y)
      printf 'measure\t%s\t%s\t%s\t%s\tPASS\tall paired trials operator-attested; capture_pre_enter=%s\n' \
        "$harness" "$delay" "$TRIALS" "$LOAD_WORKERS" "$CAPTURE_PRE_ENTER" >>"$OUTPUT/results.tsv"
      return 0
      ;;
    *)
      printf 'measure\t%s\t%s\t%s\t%s\tFAIL\tbatch rejected; descending search stopped; capture_pre_enter=%s\n' \
        "$harness" "$delay" "$TRIALS" "$LOAD_WORKERS" "$CAPTURE_PRE_ENTER" >>"$OUTPUT/results.tsv"
      return 1
      ;;
  esac
}

measure_harness() {
  harness=$1
  pane=$2
  last_pass=''

  for delay in 1000 750 500 250 100 50 0; do
    if measure_candidate "$harness" "$pane" "$delay"; then
      last_pass=$delay
    else
      break
    fi
  done

  if [ -z "$last_pass" ]; then
    printf 'measure\t%s\tNONE\t%s\t%s\tFLOOR\tno floor established; 1000ms failed\n' \
      "$harness" "$TRIALS" "$LOAD_WORKERS" >>"$OUTPUT/results.tsv"
    printf '%s: FLOOR NONE (1000ms failed).\n' "$harness"
    return 1
  fi

  if [ "$last_pass" -eq 0 ]; then
    floor='0ms-at-script-resolution'
  else
    floor="${last_pass}ms"
  fi
  printf 'measure\t%s\t%s\t%s\t%s\tFLOOR\tobserved_floor=%s\n' \
    "$harness" "$last_pass" "$TRIALS" "$LOAD_WORKERS" "$floor" >>"$OUTPUT/results.tsv"
  printf '%s: FLOOR %s.\n' "$harness" "$floor"
  return 0
}

for command_name in tmux sleep date uptime mktemp getconf sed; do
  require_command "$command_name"
done
if [ "$HARNESS" = both ] || [ "$HARNESS" = claude ]; then
  require_command claude
fi
if [ "$HARNESS" = both ] || [ "$HARNESS" = codex ]; then
  require_command codex
fi
if [ "$MODE" = measure ]; then
  require_command /usr/bin/yes
fi

if [ "$OUTPUT_EXPLICIT" -eq 1 ]; then
  [ ! -e "$OUTPUT" ] || die "output path already exists: $OUTPUT"
  mkdir "$OUTPUT" || die "could not create output directory: $OUTPUT"
else
  OUTPUT=$(mktemp -d /tmp/agentctl-injection.XXXXXX) || die 'could not create artifact directory'
fi

write_metadata
start_harnesses

overall_status=0
if [ "$MODE" = verify ]; then
  case "$HARNESS" in
    claude)
      verify_harness claude "$CLAUDE_PANE" || overall_status=1
      ;;
    codex)
      verify_harness codex "$CODEX_PANE" || overall_status=1
      ;;
    both)
      verify_harness claude "$CLAUDE_PANE" || overall_status=1
      verify_harness codex "$CODEX_PANE" || overall_status=1
      ;;
  esac
else
  case "$HARNESS" in
    claude)
      wait_for_harness claude "$CLAUDE_PANE"
      ;;
    codex)
      wait_for_harness codex "$CODEX_PANE"
      ;;
    both)
      wait_for_harness claude "$CLAUDE_PANE"
      wait_for_harness codex "$CODEX_PANE"
      ;;
  esac
  start_load
  case "$HARNESS" in
    claude)
      measure_harness claude "$CLAUDE_PANE" || overall_status=1
      ;;
    codex)
      measure_harness codex "$CODEX_PANE" || overall_status=1
      ;;
    both)
      measure_harness claude "$CLAUDE_PANE" || overall_status=1
      measure_harness codex "$CODEX_PANE" || overall_status=1
      ;;
  esac
  printf '\nEvidence only: a future payloadDelay change requires a separately justified safety margin.\n'
fi

exit "$overall_status"
