#!/usr/bin/env bash
# hack/release-verify.sh — automates the mechanical steps of release
# verification (preflight, contract probes, cleanup checks, results
# rendering) so docs/release-checklist.md holds only human judgments.
# See docs/release-verification-notes.md for the rationale.
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  hack/release-verify.sh [--measure]
  hack/release-verify.sh --render-results VERSIONS_FILE ARTIFACT_DIR
  hack/release-verify.sh --process-check VERSIONS_FILE ARTIFACT_DIR

Runs preflight and the four hack/probe-*.sh contract probes. By default it
then guides a live verification through ./bin/agentctl launch, attach, clear,
compact, kill, and status. With --measure it runs hack/verify-injection.sh in
measure mode. Both paths finish with automated cleanup checks and results
rendering.

--render-results prints a results-history markdown block to stdout, given
a VERSIONS_FILE (agentctl_version=/tmux_version=/claude_version=/
codex_version= lines) and an ARTIFACT_DIR containing metadata.txt plus,
for measure mode, results.tsv. No append; testable.

--process-check prints PROCESS CHECK PASS/FAIL and exits 0/1: every "verify"
row in ARTIFACT_DIR/results.tsv must be PASS and its process= value must
match the expected pane process (codex: "codex"; claude: the version token
from VERSIONS_FILE's claude_version= line). No append; testable.
EOF
}

die() {
  printf 'release-verify: %s\n' "$*" >&2
  exit 1
}

field() {
  # field KEY FILE — print the value of the first "KEY=..." line in FILE.
  sed -n "s/^$1=//p" "$2" | sed -n '1p'
}

ASK_ANSWER=''
ask() {
  local question=$1
  local answer
  ASK_ANSWER=''
  while true; do
    answer=''
    if ! IFS= read -r -p "$question [y/n]: " answer; then
      printf 'input closed — answer y or n\n' >&2
      return 1
    fi
    case "$answer" in
      y|n)
        ASK_ANSWER=$answer
        printf 'recorded: %s\n' "$answer"
        [ "$answer" = y ]
        return
        ;;
      *)
        printf "unrecognised: '%s' — answer y or n\n" "$answer"
        ;;
    esac
  done
}

STATUS_EXIT=0
session_absent() {
  local session_name=$1
  local stdout_file=$2
  local stderr_file=$3

  if ./bin/agentctl status --session "$session_name" >"$stdout_file" 2>"$stderr_file"; then
    STATUS_EXIT=0
    return 1
  else
    STATUS_EXIT=$?
  fi

  case "$STATUS_EXIT" in
    3)
      grep -qF "session \"$session_name\" not found" "$stderr_file" || return 2
      ;;
    6)
      grep -qF 'no server running' "$stderr_file" || return 2
      ;;
    *)
      return 2
      ;;
  esac
}

render_results() {
  versions_file=$1
  artifact_dir=$2

  [ -r "$versions_file" ] || die "cannot read versions file: $versions_file"
  metadata="$artifact_dir/metadata.txt"
  [ -r "$metadata" ] || die "cannot read metadata: $metadata"

  agentctl_version=$(field agentctl_version "$versions_file")
  tmux_version=$(field tmux_version "$versions_file")
  claude_version=$(field claude_version "$versions_file")
  codex_version=$(field codex_version "$versions_file")

  mode=$(field mode "$metadata")
  harness=$(field harness "$metadata")
  date_utc=$(field date_utc "$metadata")
  date_only=${date_utc%%T*}

  printf '### %s\n\n' "$date_only"
  # Markdown backticks are literal; command substitution deliberately suppressed.
  # shellcheck disable=SC2016
  printf -- '- agentctl: `%s`\n' "$agentctl_version"
  # Markdown backticks are literal; command substitution deliberately suppressed.
  # shellcheck disable=SC2016
  printf -- '- tmux: `%s`\n' "$tmux_version"
  # Markdown backticks are literal; command substitution deliberately suppressed.
  # shellcheck disable=SC2016
  printf -- '- Claude Code: `%s`\n' "$claude_version"
  # Markdown backticks are literal; command substitution deliberately suppressed.
  # shellcheck disable=SC2016
  printf -- '- codex-cli: `%s`\n' "$codex_version"
  # Markdown backticks are literal; command substitution deliberately suppressed.
  # shellcheck disable=SC2016
  printf -- '- Mode: `%s`; harness: `%s`\n' "$mode" "$harness"
  # Markdown backticks are literal; command substitution deliberately suppressed.
  # shellcheck disable=SC2016
  printf -- '- Artifact: `%s`\n' "$artifact_dir"

  if [ "$mode" = verify-live ]; then
    printf -- '- Probes: %s\n' "$(field probes "$metadata")"
    printf -- '- Attach: recorded %s\n' "$(field attach_attestation "$metadata")"
    printf -- '- Claude clear: recorded %s\n' "$(field claude_clear_attestation "$metadata")"
    printf -- '- Codex clear: recorded %s\n' "$(field codex_clear_attestation "$metadata")"
    printf -- '- Compact (claude): recorded %s\n' "$(field compact_attestation "$metadata")"
    printf -- '- Teardown check: %s\n' "$(field teardown_check "$metadata")"
    return
  fi

  results="$artifact_dir/results.tsv"
  [ -r "$results" ] || die "cannot read results: $results"
  printf '\n```text\n'
  cat "$results"
  printf '```\n'
}

claude_version_token() {
  # claude_version_token VERSIONS_FILE — extract the X.Y.Z token from the
  # captured claude_version= line. The pane process name IS this string
  # (e.g. "2.1.220"), so this is the expectation the process check compares
  # against, not the full "2.1.220 (Claude Code)" version banner.
  versions_file=$1
  [ -r "$versions_file" ] || die "cannot read versions file: $versions_file"
  claude_version=$(field claude_version "$versions_file")
  token=$(printf '%s\n' "$claude_version" | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -n1)
  [ -n "$token" ] || die "could not derive a version token from claude_version: $claude_version"
  printf '%s' "$token"
}

process_check() {
  # process_check RESULTS_TSV CLAUDE_EXPECTED — every "verify" row must be
  # PASS and its process= value must match the expected pane process:
  # codex rows expect exactly "codex"; claude rows expect CLAUDE_EXPECTED.
  results_file=$1
  claude_expected=$2

  if [ ! -r "$results_file" ]; then
    echo 'PROCESS CHECK FAIL (results.tsv not found)'
    return 1
  fi

  found_claude=0
  found_codex=0
  while IFS=$'\t' read -r row_mode row_harness row_delay row_trials row_workers row_result row_detail; do
    [ "$row_mode" = verify ] || continue
    case "$row_harness" in
      claude) expected=$claude_expected; found_claude=1 ;;
      codex) expected=codex; found_codex=1 ;;
      *) continue ;;
    esac
    process=$(printf '%s\n' "$row_detail" | sed -n 's/^process=\([^;]*\);.*/\1/p')
    if [ "$row_result" != PASS ] || [ "$process" != "$expected" ]; then
      printf 'PROCESS CHECK FAIL (%s\t%s\t%s\t%s\t%s\t%s\t%s)\n' \
        "$row_mode" "$row_harness" "$row_delay" "$row_trials" "$row_workers" "$row_result" "$row_detail"
      return 1
    fi
  done <"$results_file"

  if [ "$found_claude" -ne 1 ] || [ "$found_codex" -ne 1 ]; then
    echo 'PROCESS CHECK FAIL (missing verify row for claude and/or codex)'
    return 1
  fi

  printf 'PROCESS CHECK PASS (claude=%s, codex=codex)\n' "$claude_expected"
}

# --render-results and --process-check are pure, testable subcommands:
# handle them before any of the interactive/live-environment flow below.
if [ "${1:-}" = '--render-results' ]; then
  [ "$#" -eq 3 ] || die '--render-results requires VERSIONS_FILE ARTIFACT_DIR'
  render_results "$2" "$3"
  exit 0
fi

if [ "${1:-}" = '--process-check' ]; then
  [ "$#" -eq 3 ] || die '--process-check requires VERSIONS_FILE ARTIFACT_DIR'
  token=$(claude_version_token "$2")
  process_check "$3/results.tsv" "$token" && exit 0 || exit 1
fi

if [ "${1:-}" = '--help' ] || [ "${1:-}" = '-h' ]; then
  usage
  exit 0
fi

MEASURE=0
if [ "${1:-}" = '--measure' ]; then
  MEASURE=1
  shift
fi
[ "$#" -eq 0 ] || die "unsupported argument: $1"

# ---------------------------------------------------------------------------
# 1. Preflight
# ---------------------------------------------------------------------------

TOP=$(git rev-parse --show-toplevel 2>/dev/null) || die 'not inside a git repository'
[ "$PWD" = "$TOP" ] || die "run from the repo root: $TOP"

if [ -n "$(git status --porcelain)" ]; then
  echo 'DIRTY TREE — release evidence must come from a clean checkout'
  exit 1
fi

for cmd_name in tmux claude codex; do
  command -v "$cmd_name" >/dev/null 2>&1 || die "required command not found: $cmd_name"
done

EVIDENCE_DIR=$(mktemp -d /tmp/agentctl-release-verify.XXXXXX) || die 'could not create evidence directory'
VERSIONS_FILE="$EVIDENCE_DIR/versions.txt"

echo '== Preflight: make build =='
make build

AGENTCTL_VERSION=$(./bin/agentctl version)
TMUX_VERSION=$(tmux -V)
CLAUDE_VERSION=$(claude --version 2>&1 | sed -n '1p')
CODEX_VERSION=$(codex --version 2>&1 | sed -n '1p')

{
  printf 'agentctl_version=%s\n' "$AGENTCTL_VERSION"
  printf 'tmux_version=%s\n' "$TMUX_VERSION"
  printf 'claude_version=%s\n' "$CLAUDE_VERSION"
  printf 'codex_version=%s\n' "$CODEX_VERSION"
} >"$VERSIONS_FILE"

printf 'agentctl: %s\n' "$AGENTCTL_VERSION"
printf 'tmux:     %s\n' "$TMUX_VERSION"
printf 'claude:   %s\n' "$CLAUDE_VERSION"
printf 'codex:    %s\n' "$CODEX_VERSION"

# ---------------------------------------------------------------------------
# 2. Probes (fully automated)
# ---------------------------------------------------------------------------

# Each probe's final marker line, read from its own source (hack/probe-*.sh):
#   probe-1-argv.sh:      final `echo 'done'` after `echo "== cleanup =="`
#   probe-2-targeting.sh: final `echo cleanup-done`
#   probe-3-ids.sh:       final `echo cleanup-done`
#   probe-4-attach.sh:    has no "done"-style marker; its last statement is
#                         `echo -n "-CC attach-session -t \$SESSION_ID : "`
#                         followed by the (variable) attach output, so the
#                         fixed literal prefix "-CC attach-session -t " is
#                         the only reliable proof it ran to completion.
probe_marker() {
  case "$1" in
    probe-1-argv.sh) printf 'done' ;;
    probe-2-targeting.sh) printf 'cleanup-done' ;;
    probe-3-ids.sh) printf 'cleanup-done' ;;
    probe-4-attach.sh) printf -- '-CC attach-session -t ' ;;
    *) die "no known marker for probe: $1" ;;
  esac
}

echo
echo '== Probes =='
for probe in "$TOP"/hack/probe-*.sh; do
  probe_name=$(basename "$probe")
  marker=$(probe_marker "$probe_name")
  echo "-- $probe_name --"
  if ! probe_out=$(bash "$probe" </dev/null); then
    echo "PROBES FAIL ($probe_name)"
    exit 1
  fi
  printf '%s\n' "$probe_out"
  case "$probe_out" in
    *"$marker"*) ;;
    *)
      echo "PROBES FAIL ($probe_name: did not reach marker '$marker')"
      exit 1
      ;;
  esac
done

if pgrep -fl '[t]mux.*agentctl-probe-' >/dev/null 2>&1; then
  echo 'PROBES FAIL (throwaway probe tmux server still running)'
  exit 1
fi

echo 'PROBES PASS'

# ---------------------------------------------------------------------------
# 3. Interactive verification
# ---------------------------------------------------------------------------

if [ "$MEASURE" -eq 1 ]; then
  echo
  echo '== Injection measurement =='

  set +e
  bash hack/verify-injection.sh measure --harness both --output "$EVIDENCE_DIR/verify-injection"
  VERIFY_STATUS=$?
  set -e

  ARTIFACT_DIR="$EVIDENCE_DIR/verify-injection"

  # Cleanup checks always run, even when the measurement rig failed: a failed
  # measurement must never skip the throwaway-server/load-pid checks.
  echo
  echo '== Cleanup checks =='
  CLEANUP_STATUS=0
  if pgrep -fl '[t]mux.*agentctl-(probe|injection)-' >/dev/null 2>&1; then
    echo 'CLEANUP FAIL (throwaway tmux server still running)'
    CLEANUP_STATUS=1
  fi

  LOAD_PIDS=''
  metadata_file="$ARTIFACT_DIR/metadata.txt"
  if [ -r "$metadata_file" ]; then
    LOAD_PIDS=$(field load_pids "$metadata_file")
  fi

  if [ -n "$LOAD_PIDS" ]; then
    yes_pids=$(pgrep -fl '[y]es' | awk '{print $1}')
    for pid in $LOAD_PIDS; do
      case " $yes_pids " in
        *" $pid "*)
          echo "CLEANUP FAIL (load pid $pid from metadata.txt still running)"
          CLEANUP_STATUS=1
          ;;
      esac
    done
  fi

  if [ "$CLEANUP_STATUS" -eq 0 ]; then
    echo 'CLEANUP PASS'
  fi

  echo
  if [ "$VERIFY_STATUS" -eq 0 ]; then
    echo 'MEASUREMENT RESULT: PASS'
  else
    echo "MEASUREMENT RESULT: FAIL (hack/verify-injection.sh measure exited $VERIFY_STATUS)"
  fi
  if [ "$CLEANUP_STATUS" -eq 0 ]; then
    echo 'CLEANUP RESULT: PASS'
  else
    echo 'CLEANUP RESULT: FAIL'
  fi

  if [ "$VERIFY_STATUS" -ne 0 ] || [ "$CLEANUP_STATUS" -ne 0 ]; then
    die "release measurement failed (verifier=$VERIFY_STATUS, cleanup=$CLEANUP_STATUS)"
  fi
else
  echo
  echo '== Live product verification =='

  ARTIFACT_DIR="$EVIDENCE_DIR/verify-live"
  mkdir "$ARTIFACT_DIR"
  LIVE_SESSION=relverify
  LIVE_STATUS=0
  ATTACH_ATTESTATION=''
  CLAUDE_CLEAR_ATTESTATION=''
  CODEX_CLEAR_ATTESTATION=''
  COMPACT_ATTESTATION=''
  TEARDOWN_CHECK=FAIL
  STATUS_STDOUT="$ARTIFACT_DIR/status.stdout"
  STATUS_STDERR="$ARTIFACT_DIR/status.stderr"

  if session_absent "$LIVE_SESSION" "$STATUS_STDOUT" "$STATUS_STDERR"; then
    :
  else
    absence_status=$?
    if [ "$absence_status" -eq 1 ]; then
      die "session $LIVE_SESSION already exists; refusing to use or kill it"
    fi
    cat "$STATUS_STDERR" >&2
    die "could not prove session $LIVE_SESSION is absent (status exit $STATUS_EXIT)"
  fi

  echo 'Running:'
  echo '  ./bin/agentctl launch --session relverify --roles a:claude,b:codex'
  if ! ./bin/agentctl launch --session "$LIVE_SESSION" --roles a:claude,b:codex; then
    die 'live release verification launch failed'
  fi

  # This run owns relverify only after launch succeeds. Keep teardown armed
  # across every later command and attestation; explicit teardown disarms it.
  trap './bin/agentctl kill --session relverify >/dev/null 2>&1 || true' EXIT

  if [ "$LIVE_STATUS" -eq 0 ]; then
    echo
    echo 'Attach from Window 2 with:'
    echo '  ./bin/agentctl attach --session relverify'
    if ask 'Is Window 2 attached and showing the claude and codex tabs?'; then
      ATTACH_ATTESTATION=$ASK_ANSWER
    else
      ATTACH_ATTESTATION=$ASK_ANSWER
      LIVE_STATUS=1
    fi
  fi

  if [ "$LIVE_STATUS" -eq 0 ]; then
    echo
    echo 'In the claude tab, type junk into the input box; do NOT press Enter.'
    if ask 'Is the claude junk ready for agentctl clear?'; then
      echo 'Running:'
      echo '  ./bin/agentctl clear --session relverify a'
      if ./bin/agentctl clear --session "$LIVE_SESSION" a; then
        echo 'Claude clear delivery result printed above.'
        if ask 'For claude, was junk visibly cleared, /clear executed, and the conversation reset?'; then
          CLAUDE_CLEAR_ATTESTATION=$ASK_ANSWER
        else
          CLAUDE_CLEAR_ATTESTATION=$ASK_ANSWER
          LIVE_STATUS=1
        fi
      else
        echo 'LIVE VERIFY FAIL (claude clear delivery failed)'
        LIVE_STATUS=1
      fi
    else
      LIVE_STATUS=1
    fi
  fi

  if [ "$LIVE_STATUS" -eq 0 ]; then
    echo
    echo 'In the codex tab, type junk into the input box; do NOT press Enter.'
    if ask 'Is the codex junk ready for agentctl clear?'; then
      echo 'Running:'
      echo '  ./bin/agentctl clear --session relverify b'
      if ./bin/agentctl clear --session "$LIVE_SESSION" b; then
        echo 'Codex clear delivery result printed above.'
        if ask 'For codex, was junk visibly cleared, /clear executed, and the conversation reset?'; then
          CODEX_CLEAR_ATTESTATION=$ASK_ANSWER
        else
          CODEX_CLEAR_ATTESTATION=$ASK_ANSWER
          LIVE_STATUS=1
        fi
      else
        echo 'LIVE VERIFY FAIL (codex clear delivery failed)'
        LIVE_STATUS=1
      fi
    else
      LIVE_STATUS=1
    fi
  fi

  if [ "$LIVE_STATUS" -eq 0 ]; then
    echo
    echo 'In the claude tab, type junk into the input box; do NOT press Enter.'
    if ask 'Is the claude junk ready for the compact spot check?'; then
      echo 'Running:'
      echo '  ./bin/agentctl compact --session relverify a'
      if ./bin/agentctl compact --session "$LIVE_SESSION" a; then
        echo 'Claude compact delivery result printed above.'
        if ask 'For claude, was junk visibly cleared, /compact executed, and the conversation compacted?'; then
          COMPACT_ATTESTATION=$ASK_ANSWER
        else
          COMPACT_ATTESTATION=$ASK_ANSWER
          LIVE_STATUS=1
        fi
      else
        echo 'LIVE VERIFY FAIL (claude compact delivery failed)'
        LIVE_STATUS=1
      fi
    else
      LIVE_STATUS=1
    fi
  fi

  echo
  echo '== Automated teardown =='
  echo 'Running:'
  echo '  ./bin/agentctl kill --session relverify'
  TEARDOWN_STATUS=0
  if ! ./bin/agentctl kill --session "$LIVE_SESSION"; then
    echo 'TEARDOWN FAIL (kill failed)'
    TEARDOWN_STATUS=1
  fi

  if session_absent "$LIVE_SESSION" "$STATUS_STDOUT" "$STATUS_STDERR"; then
    echo 'TEARDOWN PASS (agentctl status proves relverify is absent)'
  else
    absence_status=$?
    if [ "$absence_status" -eq 1 ]; then
      echo 'TEARDOWN FAIL (agentctl status still finds relverify)'
    else
      printf 'TEARDOWN FAIL (agentctl status exited %s unexpectedly):\n' "$STATUS_EXIT"
      cat "$STATUS_STDERR"
    fi
    TEARDOWN_STATUS=1
  fi

  if surviving_tmux=$(pgrep -fl '[t]mux.*relverify' 2>&1); then
    pgrep_status=0
  else
    pgrep_status=$?
  fi
  case "$pgrep_status" in
    0)
      printf 'TEARDOWN FAIL (relverify tmux process remains):\n%s\n' "$surviving_tmux"
      TEARDOWN_STATUS=1
      ;;
    1)
      echo 'TEARDOWN PASS (no relverify tmux process remains)'
      ;;
    *)
      printf 'TEARDOWN FAIL (pgrep exited %s):\n%s\n' "$pgrep_status" "$surviving_tmux"
      TEARDOWN_STATUS=1
      ;;
  esac

  trap - EXIT
  if [ "$TEARDOWN_STATUS" -eq 0 ]; then
    TEARDOWN_CHECK=PASS
  else
    LIVE_STATUS=1
  fi

  {
    printf 'date_utc=%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    printf 'mode=verify-live\n'
    printf 'harness=both\n'
    printf 'probes=all four completed, no surviving throwaway server\n'
    printf 'attach_attestation=%s\n' "$ATTACH_ATTESTATION"
    printf 'claude_clear_attestation=%s\n' "$CLAUDE_CLEAR_ATTESTATION"
    printf 'codex_clear_attestation=%s\n' "$CODEX_CLEAR_ATTESTATION"
    printf 'compact_attestation=%s\n' "$COMPACT_ATTESTATION"
    printf 'teardown_check=%s\n' "$TEARDOWN_CHECK"
  } >"$ARTIFACT_DIR/metadata.txt"

  # The live clear/compact commands traverse agentctl's fail-closed
  # @agentctl_process exact-match validation before delivering a literal
  # payload and Enter. That checks pane identity on every delivery, so the
  # results.tsv process_check remains rig-artifact tooling, not a weaker
  # duplicate in the default live path.
  if [ "$LIVE_STATUS" -ne 0 ]; then
    die 'live release verification failed; teardown attempted'
  fi
fi

# ---------------------------------------------------------------------------
# 5. Results (automated)
# ---------------------------------------------------------------------------

echo
echo '== Results =='
NOTES_FILE="$TOP/docs/release-verification-notes.md"
BLOCK_FILE=$(mktemp) || die 'could not create evidence block file'
if ! render_results "$VERSIONS_FILE" "$ARTIFACT_DIR" >"$BLOCK_FILE"; then
  rm -f "$BLOCK_FILE"
  die 'could not render evidence block'
fi

cat "$BLOCK_FILE"

marker='## Results history'
if ! grep -qF "$marker" "$NOTES_FILE"; then
  rm -f "$BLOCK_FILE"
  die "marker not found in $NOTES_FILE: $marker"
fi

TMP_NOTES=$(mktemp) || {
  rm -f "$BLOCK_FILE"
  die 'could not create temp file'
}
if ! awk -v marker="$marker" -v block_file="$BLOCK_FILE" '
  { print }
  index($0, marker) == 1 && !done {
    print ""
    while ((getline block_line < block_file) > 0) {
      print block_line
    }
    close(block_file)
    done = 1
  }
' "$NOTES_FILE" >"$TMP_NOTES"; then
  rm -f "$BLOCK_FILE" "$TMP_NOTES"
  die 'could not append evidence block'
fi
mv "$TMP_NOTES" "$NOTES_FILE"
rm -f "$BLOCK_FILE"

echo 'ALL VERIFIED — evidence appended; commit docs/release-verification-notes.md'
