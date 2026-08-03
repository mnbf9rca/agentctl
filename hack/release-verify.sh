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

Runs preflight, the four hack/probe-*.sh contract probes, then
hack/verify-injection.sh in the foreground (verify mode, or measure mode
with --measure), followed by cleanup checks, a pane-process-name check
(verify mode only), and results rendering.

--render-results prints a results-history markdown block to stdout, given
a VERSIONS_FILE (agentctl_version=/tmux_version=/claude_version=/
codex_version= lines) and an ARTIFACT_DIR containing metadata.txt and
results.tsv as written by hack/verify-injection.sh. No append; testable.

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

render_results() {
  versions_file=$1
  artifact_dir=$2

  [ -r "$versions_file" ] || die "cannot read versions file: $versions_file"
  metadata="$artifact_dir/metadata.txt"
  results="$artifact_dir/results.tsv"
  [ -r "$metadata" ] || die "cannot read metadata: $metadata"
  [ -r "$results" ] || die "cannot read results: $results"

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
#   probe-1-argv.sh:      final `echo done` after `echo "== cleanup =="`
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
  if ! probe_out=$(bash "$probe"); then
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
# 3. Injection verification (interactive core, unchanged)
# ---------------------------------------------------------------------------

echo
echo '== Injection verification =='
echo 'hack/verify-injection.sh prints its own ATTACH: line below. Its session'
echo 'does not exist yet at that point — wait for the first "Press Enter only'
echo 'after its TUI is fully ready" prompt, THEN run that line from Window 2.'

VERIFY_MODE=verify
[ "$MEASURE" -eq 1 ] && VERIFY_MODE=measure

set +e
bash hack/verify-injection.sh "$VERIFY_MODE" --harness both --output "$EVIDENCE_DIR/verify-injection"
VERIFY_STATUS=$?
set -e

ARTIFACT_DIR="$EVIDENCE_DIR/verify-injection"

# ---------------------------------------------------------------------------
# 4. Cleanup checks (automated) — always run, even when the verifier failed:
#    a failed verifier must never skip the throwaway-server/load-pid checks.
# ---------------------------------------------------------------------------

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

# Both facts are always reported, regardless of which (if either) failed —
# a failing cleanup check must not hide a failing verifier, or vice versa.
echo
if [ "$VERIFY_STATUS" -eq 0 ]; then
  echo 'VERIFIER RESULT: PASS'
else
  echo "VERIFIER RESULT: FAIL (hack/verify-injection.sh $VERIFY_MODE exited $VERIFY_STATUS)"
fi
if [ "$CLEANUP_STATUS" -eq 0 ]; then
  echo 'CLEANUP RESULT: PASS'
else
  echo 'CLEANUP RESULT: FAIL'
fi

if [ "$VERIFY_STATUS" -ne 0 ] || [ "$CLEANUP_STATUS" -ne 0 ]; then
  die "release verification failed (verifier=$VERIFY_STATUS, cleanup=$CLEANUP_STATUS)"
fi

# ---------------------------------------------------------------------------
# 4b. Pane-process-name check (automated, verify mode only) — the mechanical
#     replacement for the old checklist's "confirm the row records PASS and
#     the expected pane process name" step (a human-eyeballed text compare).
# ---------------------------------------------------------------------------

if [ "$VERIFY_MODE" = verify ]; then
  echo
  echo '== Process check =='
  claude_token=$(claude_version_token "$VERSIONS_FILE")
  process_check "$ARTIFACT_DIR/results.tsv" "$claude_token" || die 'process check failed'
fi

# ---------------------------------------------------------------------------
# 5. Results (automated)
# ---------------------------------------------------------------------------

echo
echo '== Results =='
NOTES_FILE="$TOP/docs/release-verification-notes.md"
BLOCK=$(render_results "$VERSIONS_FILE" "$ARTIFACT_DIR")

printf '%s\n' "$BLOCK"

marker='## Results history'
if ! grep -qF "$marker" "$NOTES_FILE"; then
  die "marker not found in $NOTES_FILE: $marker"
fi

TMP_NOTES=$(mktemp) || die 'could not create temp file'
awk -v marker="$marker" -v block="$BLOCK" '
  { print }
  index($0, marker) == 1 && !done {
    print ""
    print block
    done = 1
  }
' "$NOTES_FILE" >"$TMP_NOTES"
mv "$TMP_NOTES" "$NOTES_FILE"

echo 'ALL VERIFIED — evidence appended; commit docs/release-verification-notes.md'
