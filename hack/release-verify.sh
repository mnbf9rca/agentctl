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

Runs preflight, the four hack/probe-*.sh contract probes, then
hack/verify-injection.sh in the foreground (verify mode, or measure mode
with --measure), followed by cleanup checks and results rendering.

--render-results prints a results-history markdown block to stdout, given
a VERSIONS_FILE (agentctl_version=/tmux_version=/claude_version=/
codex_version= lines) and an ARTIFACT_DIR containing metadata.txt and
results.tsv as written by hack/verify-injection.sh. No append; testable.
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
  printf -- '- agentctl: `%s`\n' "$agentctl_version"
  printf -- '- tmux: `%s`\n' "$tmux_version"
  printf -- '- Claude Code: `%s`\n' "$claude_version"
  printf -- '- codex-cli: `%s`\n' "$codex_version"
  printf -- '- Mode: `%s`; harness: `%s`\n' "$mode" "$harness"
  printf -- '- Artifact: `%s`\n' "$artifact_dir"
  printf '\n```text\n'
  cat "$results"
  printf '```\n'
}

# --render-results is a pure, testable subcommand: handle it before any of
# the interactive/live-environment flow below.
if [ "${1:-}" = '--render-results' ]; then
  [ "$#" -eq 3 ] || die '--render-results requires VERSIONS_FILE ARTIFACT_DIR'
  render_results "$2" "$3"
  exit 0
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
echo 'ATTACH: from Window 2, run the "ATTACH:" line hack/verify-injection.sh prints below.'

VERIFY_MODE=verify
[ "$MEASURE" -eq 1 ] && VERIFY_MODE=measure

set +e
bash hack/verify-injection.sh "$VERIFY_MODE" --harness both --output "$EVIDENCE_DIR/verify-injection"
VERIFY_STATUS=$?
set -e
[ "$VERIFY_STATUS" -eq 0 ] || die "hack/verify-injection.sh $VERIFY_MODE exited $VERIFY_STATUS"

ARTIFACT_DIR="$EVIDENCE_DIR/verify-injection"

# ---------------------------------------------------------------------------
# 4. Cleanup checks (automated)
# ---------------------------------------------------------------------------

echo
echo '== Cleanup checks =='
if pgrep -fl '[t]mux.*agentctl-(probe|injection)-' >/dev/null 2>&1; then
  echo 'CLEANUP FAIL (throwaway tmux server still running)'
  exit 1
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
        exit 1
        ;;
    esac
  done
fi

echo 'CLEANUP PASS'

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
