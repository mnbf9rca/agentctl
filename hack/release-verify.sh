#!/usr/bin/env bash
# hack/release-verify.sh — automates the mechanical steps of release
# verification (preflight, contract probes, cleanup checks, results
# rendering) so docs/release-checklist.md holds only human judgments.
# See docs/release-verification-notes.md for the rationale.
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  hack/release-verify.sh [--non-interactive] [--measure]
  hack/release-verify.sh --render-results VERSIONS_FILE ARTIFACT_DIR
  hack/release-verify.sh --process-check VERSIONS_FILE ARTIFACT_DIR
  hack/release-verify.sh --assert-probe PROBE_NAME OUTPUT_FILE
  hack/release-verify.sh --shim-version-matrix CURRENT_BINARY ARTIFACT_DIR
  hack/release-verify.sh --task8 CURRENT_BINARY ARTIFACT_DIR

Runs preflight and the release contract probes. By default it
then guides a live verification through ./bin/agentctl launch, attach, clear,
compact, relaunch, kill, status, and a separate skill-discovery fleet. With
--non-interactive each live checkpoint reads y/n from stdin while retaining all
prompts and expected observations. With --measure it runs hack/verify-injection.sh in
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

--assert-probe validates captured OUTPUT_FILE against the explicit contract
for PROBE_NAME. It exits 1 and names the missing observation on failure.

--shim-version-matrix separately builds the committed foreign-version binary,
runs both protocol directions plus absent/matching controls against
CURRENT_BINARY, and writes hashes, versions, and observed results to
ARTIFACT_DIR.

--task8 runs the automated 0.5 release-candidate checkpoint with isolated
HOME/runtime/state roots. It records built-binary help and skill behavior, the
second-binary skew matrix, live integration/kernel evidence, drift guards, and
cleanup observations in ARTIFACT_DIR.
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
ASK_RESULT=''
FAILED_CHECKPOINT_ID=''
FAILED_CHECKPOINT_RESULT=''
ask() {
  local question=$1
  local answer
  ASK_ANSWER=''
  ASK_RESULT=''
  while true; do
    answer=''
    printf '%s [y/n]: ' "$question"
    if [ "$NON_INTERACTIVE" -eq 1 ]; then
      IFS= read -r answer || {
        printf 'input closed — answer y or n\n' >&2
        ASK_RESULT=input
        return 2
      }
    elif [ -r /dev/tty ]; then
      IFS= read -r answer </dev/tty || {
        printf 'input closed — answer y or n\n' >&2
        ASK_RESULT=input
        return 2
      }
    elif ! IFS= read -r answer; then
      printf 'input closed — answer y or n\n' >&2
      ASK_RESULT=input
      return 2
    fi
    case "$answer" in
      y|n)
        ASK_ANSWER=$answer
        ASK_RESULT=$answer
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

part_header() { printf '\n=== Part %s — %s ===\n' "$1" "$2"; }
step_start() { printf '\n[%s] %s\n' "$1" "$2"; }
step_pass() { printf '[PASS %s] %s\n' "$1" "$2"; }
step_fail() { printf '[FAIL %s] %s\n' "$1" "$2" >&2; }
action_pass() { printf '[ACTION PASS %s] %s\n' "$1" "$2"; }
action_fail() { printf '[ACTION FAIL %s] %s\n' "$1" "$2" >&2; }

checkpoint() {
  local checkpoint_id=$1
  local checkpoint_name=$2
  local expected_output=$3
  local prompt=$4
  printf '\n[CHECKPOINT %s] %s\n' "$checkpoint_id" "$checkpoint_name"
  printf 'Expected output:\n> %s\n' "$expected_output"
  if ask "$prompt"; then
    printf '[CHECKPOINT PASS %s] operator confirmed: %s\n' "$checkpoint_id" "$checkpoint_name"
    return 0
  fi
  if [ "$ASK_RESULT" = n ]; then
    FAILED_CHECKPOINT_ID=$checkpoint_id
    FAILED_CHECKPOINT_RESULT=refused
    printf '[CHECKPOINT FAIL %s] operator refused checkpoint: %s\n' "$checkpoint_id" "$checkpoint_name" >&2
    return 1
  fi
  FAILED_CHECKPOINT_ID=$checkpoint_id
  FAILED_CHECKPOINT_RESULT=input
  printf '[CHECKPOINT FAIL %s] checkpoint input failed: %s\n' "$checkpoint_id" "$checkpoint_name" >&2
  return 2
}

PART_B_TOP=''
PART_B_SESSION=''
PART_B_SESSION_OWNED=0
PART_B_KEEPER_SESSION=''
PART_B_KEEPER_OWNED=0
PART_B_PRECHECK_OBSERVATION=''

# This calls bare tmux; cleanup order must restore Part C's shimmed PATH first.
part_b_keeper_teardown() {
  local kill_status=0
  if [ "$PART_B_KEEPER_OWNED" -eq 0 ]; then
    return 0
  fi
  if tmux kill-session -t "=$PART_B_KEEPER_SESSION"; then
    printf 'PART B KEEPER CLEANUP PASS (wrapper-owned session %s removed)\n' "$PART_B_KEEPER_SESSION"
    PART_B_KEEPER_OWNED=0
    return 0
  else
    kill_status=$?
  fi
  printf 'PART B KEEPER CLEANUP FAIL (wrapper-owned session %s kill exited %s)\n' "$PART_B_KEEPER_SESSION" "$kill_status" >&2
  return 1
}

part_b_teardown() {
  local kill_status=0
  if [ "$PART_B_SESSION_OWNED" -eq 0 ]; then
    return 0
  fi
  if "$PART_B_TOP/bin/agentctl" kill --session "$PART_B_SESSION"; then
    printf 'PART B CLEANUP PASS (%s kill exited 0)\n' "$PART_B_SESSION"
    PART_B_SESSION_OWNED=0
    return 0
  else
    kill_status=$?
  fi
  printf 'PART B CLEANUP FAIL (%s kill exited %s)\n' "$PART_B_SESSION" "$kill_status" >&2
  return 1
}

PART_C_ROOT=''
PART_C_TOP=''
PART_C_REAL_TMUX=''
PART_C_SOCKET=''
PART_C_ORIGINAL_HOME=''
PART_C_ORIGINAL_PATH=''
PART_C_HOME=''
PART_C_PROJECT=''
PART_C_BIN=''
PART_C_REAL_SECURITY=''
PART_C_SESSION_OWNED=0
PART_C_SOCKET_ARMED=0
PART_C_ACTIVE=0
PART_C_AUTH_MODE=''
PART_C_CLAUDE_AUTH_MODE=''
PART_C_KEYCHAIN_SOURCE=''
PART_C_KEYCHAIN_LINK=''
PART_C_KEYCHAIN_LINK_OWNED=0

# Empirical macOS probes: codex-cli 0.146.1 was authenticated
# with the operator HOME, unauthenticated with an empty HOME, and authenticated
# with only .codex/auth.json copied into that empty HOME. On 2026-08-08, Claude
# Code 2.1.226 authenticated from a fresh HOME containing only an exact symlink
# from its Library/Keychains path to the operator's Library/Keychains directory.
# On 2026-08-10, that link plus a synthesized .claude.json containing only
# hasCompletedOnboarding=true started Claude without requiring re-authentication.
part_c_has_seedable_auth() {
  [ -f "$PART_C_ORIGINAL_HOME/.codex/auth.json" ]
}

part_c_print_seedable_auth() {
  [ ! -f "$PART_C_ORIGINAL_HOME/.codex/auth.json" ] || printf '  ~/.codex/auth.json\n'
}

part_c_seed_auth() {
  install -d -m 0700 "$PART_C_HOME/.codex" 2>/dev/null || return 1
  install -m 0600 "$PART_C_ORIGINAL_HOME/.codex/auth.json" "$PART_C_HOME/.codex/auth.json" 2>/dev/null
}

part_c_has_keychain_source() {
  [ -d "$PART_C_KEYCHAIN_SOURCE" ]
}

part_c_link_keychains() {
  install -d -m 0700 "$PART_C_HOME/Library" 2>/dev/null || return 1
  if ! ln -s "$PART_C_KEYCHAIN_SOURCE" "$PART_C_KEYCHAIN_LINK"; then
    return 1
  fi
  PART_C_KEYCHAIN_LINK_OWNED=1
  [ -L "$PART_C_KEYCHAIN_LINK" ]
}

part_c_seed_claude_onboarding() {
  (
    umask 077
    printf '%s\n' '{"hasCompletedOnboarding":true}' >"$PART_C_HOME/.claude.json"
  )
}

part_c_create_isolated_keychain() {
  PART_C_REAL_SECURITY=$(command -v security) || return 1
  install -d -m 0700 "$PART_C_HOME/Library/Keychains" 2>/dev/null || return 1
  HOME="$PART_C_HOME" "$PART_C_REAL_SECURITY" create-keychain -p '' "$PART_C_HOME/Library/Keychains/login.keychain-db"
}

part_c_kill_session() {
  (
    cd "$PART_C_PROJECT" || exit 1
    HOME="$PART_C_HOME" \
      PATH="$PART_C_BIN:$PART_C_ORIGINAL_PATH" \
      "$PART_C_TOP/bin/agentctl" kill --session skillverify
  )
}

part_c_named_socket_absent() {
  local output=$1
  case "$output" in
    *$'\n'*) return 1 ;;
    'no server running') return 0 ;;
    "no server running on "*"/$PART_C_SOCKET") return 0 ;;
    "error connecting to "*"/$PART_C_SOCKET (No such file or directory)") return 0 ;;
    *) return 1 ;;
  esac
}

part_c_teardown() {
  local teardown_status=0
  local socket_output
  local socket_status=0
  if [ "$PART_C_ACTIVE" -eq 0 ]; then
    return 0
  fi
  if [ "$PART_C_SESSION_OWNED" -eq 1 ]; then
    if part_c_kill_session; then
      printf 'PART C CLEANUP PASS (skillverify killed)\n'
      PART_C_SESSION_OWNED=0
    else
      printf 'PART C CLEANUP FAIL (skillverify kill)\n' >&2
      teardown_status=1
    fi
  fi
  if [ "$PART_C_SOCKET_ARMED" -eq 1 ]; then
    socket_output=$("$PART_C_REAL_TMUX" -L "$PART_C_SOCKET" kill-server 2>&1) || socket_status=$?
    if [ "$socket_status" -eq 0 ]; then
      printf 'PART C CLEANUP PASS (named tmux socket killed)\n'
      PART_C_SOCKET_ARMED=0
      if [ "$PART_C_SESSION_OWNED" -eq 1 ]; then
        printf 'PART C CLEANUP OBSERVED (named tmux socket removal proves skillverify absent)\n'
        PART_C_SESSION_OWNED=0
      fi
    elif part_c_named_socket_absent "$socket_output"; then
      # Accept only tmux's complete single-line response for this internally
      # named socket when it was armed but no server was ever created.
      printf 'PART C CLEANUP OBSERVED (named tmux socket already absent)\n'
      PART_C_SOCKET_ARMED=0
      PART_C_SESSION_OWNED=0
    else
      printf 'PART C CLEANUP FAIL (named tmux socket kill-server exited %s): %s\n' "$socket_status" "$socket_output" >&2
      teardown_status=1
    fi
  fi
  if [ -n "$PART_C_TOP" ]; then
    if cd "$PART_C_TOP"; then
      printf 'PART C CLEANUP PASS (cwd restored)\n'
    else
      printf 'PART C CLEANUP FAIL (restore cwd)\n' >&2
      teardown_status=1
    fi
  fi
  if [ -n "$PART_C_ORIGINAL_HOME" ]; then
    export HOME="$PART_C_ORIGINAL_HOME"
  fi
  if [ -n "$PART_C_ORIGINAL_PATH" ]; then
    export PATH="$PART_C_ORIGINAL_PATH"
  fi
  printf 'PART C CLEANUP PASS (HOME and PATH restored)\n'
  if [ "$PART_C_KEYCHAIN_LINK_OWNED" -eq 1 ] && [ "$PART_C_SESSION_OWNED" -eq 0 ] && [ "$PART_C_SOCKET_ARMED" -eq 0 ]; then
    if [ -L "$PART_C_KEYCHAIN_LINK" ]; then
      # Keep this exactly non-recursive and slash-free: rm -rf link/ follows the symlink and destroys the operator's Keychains target.
      if rm -f -- "$PART_C_KEYCHAIN_LINK" && [ ! -e "$PART_C_KEYCHAIN_LINK" ] && [ ! -L "$PART_C_KEYCHAIN_LINK" ]; then
        printf 'PART C CLEANUP PASS (temporary Keychains symlink removed)\n'
        PART_C_KEYCHAIN_LINK_OWNED=0
      else
        printf 'PART C CLEANUP FAIL (remove temporary Keychains symlink %s)\n' "$PART_C_KEYCHAIN_LINK" >&2
        teardown_status=1
      fi
    elif [ ! -e "$PART_C_KEYCHAIN_LINK" ]; then
      printf 'PART C CLEANUP OBSERVED (temporary Keychains symlink already absent)\n'
      PART_C_KEYCHAIN_LINK_OWNED=0
    else
      printf 'PART C CLEANUP FAIL (owned Keychains path is no longer a symlink: %s)\n' "$PART_C_KEYCHAIN_LINK" >&2
      teardown_status=1
    fi
  elif [ "$PART_C_KEYCHAIN_LINK_OWNED" -eq 1 ]; then
    printf 'PART C CLEANUP OBSERVED (temporary Keychains symlink retained until fleet and socket cleanup completes)\n'
  fi
  if [ -n "$PART_C_ROOT" ] && [ -n "$PART_C_HOME" ] && [ "$PART_C_SESSION_OWNED" -eq 0 ] && [ "$PART_C_SOCKET_ARMED" -eq 0 ] && [ "$PART_C_KEYCHAIN_LINK_OWNED" -eq 0 ]; then
    if [ -e "$PART_C_HOME" ]; then
      if rm -rf -- "$PART_C_HOME" && [ ! -e "$PART_C_HOME" ]; then
        printf 'PART C CLEANUP PASS (temporary credential HOME removed)\n'
      else
        printf 'PART C CLEANUP FAIL (remove temporary credential HOME %s)\n' "$PART_C_HOME" >&2
        teardown_status=1
      fi
    else
      printf 'PART C CLEANUP OBSERVED (temporary credential HOME already absent)\n'
    fi
  elif [ -n "$PART_C_ROOT" ]; then
    printf 'PART C CLEANUP OBSERVED (temporary credential HOME retained for owned-resource cleanup retry)\n'
  fi
  if [ -n "$PART_C_ROOT" ] && [ "$PART_C_SESSION_OWNED" -eq 0 ] && [ "$PART_C_SOCKET_ARMED" -eq 0 ] && [ "$PART_C_KEYCHAIN_LINK_OWNED" -eq 0 ]; then
    if rm -rf -- "$PART_C_ROOT"; then
      printf 'PART C CLEANUP PASS (temporary root removed)\n'
      PART_C_ROOT=''
    else
      printf 'PART C CLEANUP FAIL (remove temporary root %s)\n' "$PART_C_ROOT" >&2
      teardown_status=1
    fi
  elif [ -n "$PART_C_ROOT" ]; then
    printf 'PART C CLEANUP OBSERVED (temporary root retained for owned-resource cleanup retry)\n'
  fi
  if [ "$teardown_status" -eq 0 ] && [ "$PART_C_SESSION_OWNED" -eq 0 ] && [ "$PART_C_SOCKET_ARMED" -eq 0 ] && [ "$PART_C_KEYCHAIN_LINK_OWNED" -eq 0 ] && [ -z "$PART_C_ROOT" ]; then
    PART_C_ACTIVE=0
  fi
  return "$teardown_status"
}

part_c_abort() {
  local reason=$1
  if ! part_c_teardown; then
    die "$reason; Part C cleanup failed"
  fi
  die "$reason"
}

cleanup_exit_trap() {
  local original_status=$?
  local cleanup_status=0
  local final_status
  trap - EXIT
  if ! part_c_teardown; then
    printf 'release-verify: Part C cleanup failed during exit\n' >&2
    cleanup_status=1
  fi
  if ! part_b_teardown; then
    printf 'release-verify: Part B cleanup failed during exit\n' >&2
    cleanup_status=1
  fi
  if ! part_b_keeper_teardown; then
    printf 'release-verify: Part B keeper cleanup failed during exit\n' >&2
    cleanup_status=1
  fi
  final_status=$original_status
  if [ "$cleanup_status" -ne 0 ]; then
    final_status=1
  fi
  exit "$final_status"
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
      grep -qF \
        -e "session \"$session_name\" not found" \
        -e "session \"$session_name\" has no durable fleet configuration" \
        "$stderr_file" || return 2
      ;;
    6)
      # Keep the pre-#147 exited-server contract; the keeper path is scoped to connect ENOENT.
      grep -qF 'no server running' "$stderr_file" || return 2
      ;;
    *)
      return 2
      ;;
  esac
}

default_tmux_server_connect_enoent() {
  local stderr_file=$1
  # The path is deliberately unconstrained because TMUX_TMPDIR relocates the default socket.
  grep -Eq '^agentctl: tmux list sessions: exit status 1: error connecting to .+ \(No such file or directory\)$' "$stderr_file"
}

valid_tmux_id() {
  local value=$1
  local prefix=$2
  local digits
  [ "${value#?}" != "$value" ] || return 1
  [ "${value%"${value#?}"}" = "$prefix" ] || return 1
  digits=${value#?}
  case "$digits" in
    ''|*[!0-9]*) return 1 ;;
  esac
}

LIVE_SESSION_ID=''
resolve_live_session_id() {
  local session_name=$1
  local format
  local matches
  format="#{session_id}$(printf '\t')#{session_name}"
  matches=$(tmux list-sessions -F "$format") || return 1
  LIVE_SESSION_ID=$(printf '%s\n' "$matches" | awk -F '\t' -v session="$session_name" '$2 == session { print $1 }')
  valid_tmux_id "$LIVE_SESSION_ID" '$'
}

ROLE_WINDOW_ID=''
ROLE_PANE_ID=''
resolve_role_window() {
  local session_id=$1
  local role=$2
  local format
  local records
  local record
  local observed_name
  format="#{window_id}$(printf '\t')#{pane_id}$(printf '\t')#{window_name}"
  records=$(tmux list-windows -t "$session_id" -F "$format") || return 1
  record=$(printf '%s\n' "$records" | awk -F '\t' -v role="$role" '$3 == role { print }')
  [ "$(printf '%s\n' "$record" | awk 'NF { count++ } END { print count + 0 }')" -eq 1 ] || return 1
  IFS=$'\t' read -r ROLE_WINDOW_ID ROLE_PANE_ID observed_name <<<"$record"
  [ "$observed_name" = "$role" ] || return 1
  valid_tmux_id "$ROLE_WINDOW_ID" '@' && valid_tmux_id "$ROLE_PANE_ID" '%'
}

assert_role_state() {
  local session_name=$1
  local role=$2
  local expected=$3
  local output_file=$4
  local states
  if ! ./bin/agentctl status --session "$session_name" >"$output_file"; then
    cat "$output_file"
    return 1
  fi
  cat "$output_file"
  states=$(awk -v session="$session_name" -v role="$role" -v expected="$expected" '
    $1 == session && $2 == role {
      for (field = 3; field <= NF; field++) {
        if ($field == expected) print $field
      }
    }
  ' "$output_file")
  [ "$states" = "$expected" ]
}

ROLE_SHIM_PID=''
ROLE_CHILD_PID=''
resolve_running_role_processes() {
  local session_name=$1
  local role=$2
  local output_file=$3
  local records
  local record
  if ! ./bin/agentctl status --session "$session_name" >"$output_file"; then
    cat "$output_file"
    return 1
  fi
  cat "$output_file"
  records=$(awk -v session="$session_name" -v role="$role" '
    $1 == session && $2 == role && $10 == "running" { print $7 "\t" $8 }
  ' "$output_file")
  record=$(printf '%s\n' "$records" | awk 'NF { print }')
  [ "$(printf '%s\n' "$record" | awk 'NF { count++ } END { print count + 0 }')" -eq 1 ] || return 1
  IFS=$'\t' read -r ROLE_SHIM_PID ROLE_CHILD_PID <<<"$record"
  case "$ROLE_SHIM_PID:$ROLE_CHILD_PID" in
    *[!0-9:]*|:*|*:) return 1 ;;
  esac
  [ "$ROLE_SHIM_PID" -gt 0 ] && [ "$ROLE_CHILD_PID" -gt 0 ]
}

wait_process_absent() {
  local pid=$1
  local attempt=0
  while [ "$attempt" -lt 100 ]; do
    if ! kill -0 "$pid" 2>/dev/null; then
      return 0
    fi
    attempt=$((attempt + 1))
    sleep 0.1
  done
  return 1
}

# Markdown backticks below are literal; command substitution is deliberately
# suppressed throughout this function. One function-level directive replaces
# what would otherwise be a repeated per-line disable comment.
# shellcheck disable=SC2016
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
  printf -- '- agentctl: `%s`\n' "$agentctl_version"
  printf -- '- tmux: `%s`\n' "$tmux_version"
  printf -- '- Claude Code: `%s`\n' "$claude_version"
  printf -- '- codex-cli: `%s`\n' "$codex_version"
  printf -- '- Mode: `%s`; harness: `%s`\n' "$mode" "$harness"
  evidence_scope=$(field evidence_scope "$metadata")
  if [ -n "$evidence_scope" ]; then
    printf -- '- Scope: %s\n' "$evidence_scope"
  fi
  printf -- '- Artifact: `%s`\n' "$artifact_dir"

  if [ "$mode" = verify-live ]; then
    part_a_result=$(field part_a_result "$metadata")
    part_b_detach_attestation=$(field part_b_detach_attestation "$metadata")
    if [ -n "$part_a_result" ]; then
      [ -n "$part_b_detach_attestation" ] || die 'live metadata new schema is missing part_b_detach_attestation'
      printf -- '- Part A: %s\n' "$part_a_result"
      printf -- '- Part B: %s\n' "$(field part_b_result "$metadata")"
      printf -- '- Part C: %s\n' "$(field part_c_result "$metadata")"
      part_b_precheck_observation=$(field part_b_precheck_observation "$metadata")
      if [ -n "$part_b_precheck_observation" ]; then
        part_b_keeper_session=$(field part_b_keeper_session "$metadata")
        [ -n "$part_b_keeper_session" ] || die 'live metadata records a no-server pre-check without its keeper session'
        printf -- '- Part B pre-check: %s\n' "$part_b_precheck_observation"
        printf -- '- Part B keeper: created and removed wrapper-owned session `%s`\n' "$part_b_keeper_session"
      fi
    fi
    printf -- '- Probes: %s\n' "$(field probes "$metadata")"
    if [ -n "$part_a_result" ]; then
      printf -- '- Checkpoint B.C1 attach narration: operator confirmed: %s\n' "$(field attach_attestation "$metadata")"
      printf -- '- Checkpoint B.C3 Claude clear outcome: operator confirmed: %s\n' "$(field claude_clear_attestation "$metadata")"
      printf -- '- Checkpoint B.C5 Codex clear outcome: operator confirmed: %s\n' "$(field codex_clear_attestation "$metadata")"
      printf -- '- Checkpoint B.C7 Claude compact outcome: operator confirmed: %s\n' "$(field compact_attestation "$metadata")"
      printf -- '- Checkpoint B.C9 relaunch: %s; fresh claude input with no junk: operator confirmed: %s\n' \
        "$(field relaunch_check "$metadata")" "$(field relaunch_attestation "$metadata")"
      printf -- '- Checkpoint B.C10 detach: operator confirmed: %s\n' "$part_b_detach_attestation"
      if [ -n "$(field part_c_skill_attestation "$metadata")" ]; then
        part_c_auth_mode=$(field part_c_auth_mode "$metadata")
        if [ -n "$part_c_auth_mode" ]; then
          case "$part_c_auth_mode" in
            codex-seeded|manual) ;;
            *) die 'live metadata has invalid part_c_auth_mode' ;;
          esac
          part_c_claude_auth_mode=$(field part_c_claude_auth_mode "$metadata")
          if [ -n "$part_c_claude_auth_mode" ]; then
            case "$part_c_claude_auth_mode" in
              keychain-linked|isolated-keychain) ;;
              *) die 'live metadata has invalid part_c_claude_auth_mode' ;;
            esac
          fi
          part_c_auth_attestation=$(field part_c_auth_attestation "$metadata")
          [ -n "$part_c_auth_attestation" ] || die 'live metadata is missing part_c_auth_attestation'
          if [ -n "$part_c_claude_auth_mode" ]; then
            printf -- '- Checkpoint C.C1 authentication (%s, %s): operator confirmed: %s\n' "$part_c_claude_auth_mode" "$part_c_auth_mode" "$part_c_auth_attestation"
          else
            printf -- '- Checkpoint C.C1 authentication (%s): operator confirmed: %s\n' "$part_c_auth_mode" "$part_c_auth_attestation"
          fi
          printf -- '- Checkpoint C.C2 skill inventory: operator confirmed: %s\n' "$(field part_c_skill_attestation "$metadata")"
          printf -- '- Checkpoint C.C3 status meaning: operator confirmed: %s\n' "$(field part_c_meaning_attestation "$metadata")"
        else
          printf -- '- Checkpoint C.C1 skill inventory: operator confirmed: %s\n' "$(field part_c_skill_attestation "$metadata")"
          printf -- '- Checkpoint C.C2 status meaning: operator confirmed: %s\n' "$(field part_c_meaning_attestation "$metadata")"
        fi
      fi
    else
      printf -- '- Attach: operator confirmed: %s\n' "$(field attach_attestation "$metadata")"
      printf -- '- Claude clear: operator confirmed: %s\n' "$(field claude_clear_attestation "$metadata")"
      printf -- '- Codex clear: operator confirmed: %s\n' "$(field codex_clear_attestation "$metadata")"
      printf -- '- Compact (claude): operator confirmed: %s\n' "$(field compact_attestation "$metadata")"
      printf -- '- Relaunch: %s; fresh claude input with no junk: operator confirmed: %s\n' \
        "$(field relaunch_check "$metadata")" "$(field relaunch_attestation "$metadata")"
    fi
    case "$(field teardown_status_exit "$metadata")" in
      3) printf -- '- Teardown status: exit 3 (session absent; other tmux sessions remained)\n' ;;
      6) printf -- '- Teardown status: exit 6 (session absent; relverify was last and tmux server exited)\n' ;;
      *) die 'live metadata has invalid teardown_status_exit' ;;
    esac
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

require_probe_text() {
  local probe_name=$1
  local expected=$2
  case "$PROBE_OUTPUT" in
    *"$expected"*) ;;
    *)
      printf 'PROBE ASSERT FAIL (%s): missing expected output: %s\n' "$probe_name" "$expected" >&2
      return 1
      ;;
  esac
}

assert_probe_output() {
  local probe_name=$1
  local PROBE_OUTPUT=$2
	local probe_harness probe_version shim_pid child_pid child_outcome

  case "$probe_name" in
    probe-1-argv.sh)
      require_probe_text "$probe_name" 'OK exit=0' &&
        require_probe_text "$probe_name" 'empty-value set OK' &&
        require_probe_text "$probe_name" 'read role:  role1' &&
        require_probe_text "$probe_name" "  -v read:   'two words'" &&
        require_probe_text "$probe_name" 'name=role1 role=role1 proc=2.1.220' &&
        require_probe_text "$probe_name" "after killing =foo, has-session -t '=foobar': exit0 (foobar survived)" &&
        require_probe_text "$probe_name" "list-panes -t 'probe:rev' resolves to: rev" &&
        require_probe_text "$probe_name" "list-panes -t 'probe:=rev' resolves to: rev" &&
        require_probe_text "$probe_name" 'literal send OK' &&
        require_probe_text "$probe_name" 'Enter OK' &&
        require_probe_text "$probe_name" "captured: '/clear'"
      ;;
    probe-2-targeting.sh)
      require_probe_text "$probe_name" "set-option -t '=alpha':  no such session: =alpha" &&
        require_probe_text "$probe_name" 'set-option -t alpha:     OK' &&
        require_probe_text "$probe_name" "show-options -qv -t alpha @k: 'v'" &&
        require_probe_text "$probe_name" 'set-option -t alph @k2 v: OK <- PREFIX MATCHED (bad)' &&
        require_probe_text "$probe_name" 'has-session -t betab (unique prefix): exit=0 (0 = prefix matched)' &&
        require_probe_text "$probe_name" "has-session -t '=betab':             can't find session: betab
exit=1" &&
        require_probe_text "$probe_name" "list-panes -t 'alpha:rev'  -> reviewer" &&
        require_probe_text "$probe_name" "list-panes -t 'alpha:=rev' -> can't find window: rev
   exit=1" &&
        require_probe_text "$probe_name" "list-panes -t 'alpha:dup' picks: can't find window: dup" &&
        require_probe_text "$probe_name" "set:   '1' exit=0" &&
        require_probe_text "$probe_name" "unset: '' exit=0" &&
        require_probe_text "$probe_name" "unset no -q: 'invalid option: @agentctl_absent' exit=1" &&
        require_probe_text "$probe_name" "display-message -p -t \$PANE_ID '#{session_name}': alpha"
      ;;
    probe-3-ids.sh)
      require_probe_text "$probe_name" "created session id = '\$0'" &&
        require_probe_text "$probe_name" "set-option -t \$SESSION_ID:  OK" &&
        require_probe_text "$probe_name" "show-options -qv -t \$SESSION_ID @agentctl_managed: '1'" &&
        require_probe_text "$probe_name" "decoy 'alphabet' contaminated? managed=''" &&
        require_probe_text "$probe_name" "list-windows -t \$SESSION_ID: alpha" &&
        require_probe_text "$probe_name" "has-session  -t \$SESSION_ID: exit=0" &&
        require_probe_text "$probe_name" "new-window   -t \$SESSION_ID: @2 %2" &&
        require_probe_text "$probe_name" "model set-to-empty via -F: ''" &&
        require_probe_text "$probe_name" "never-set option via -F:   ''" &&
        require_probe_text "$probe_name" "stdout='@4'" &&
        require_probe_text "$probe_name" 'killed by id OK' &&
        require_probe_text "$probe_name" "remaining:
  alphabet"
      ;;
    probe-4-attach.sh)
      require_probe_text "$probe_name" "sid=\$0" &&
        require_probe_text "$probe_name" "attach-session -t \$SESSION_ID     : open terminal failed: not a terminal" &&
        require_probe_text "$probe_name" "attach-session -t '=alpha'  : open terminal failed: not a terminal" &&
        require_probe_text "$probe_name" "attach-session -t '=nope'   : can't find session: nope" &&
        require_probe_text "$probe_name" "-CC attach-session -t \$SESSION_ID : tcgetattr failed: Operation not supported by device"
      ;;
    probe-shim-sighup.sh)
      probe_harness=$(printf '%s\n' "$PROBE_OUTPUT" | sed -n 's/^harness=//p' | sed -n '1p')
      probe_version=$(printf '%s\n' "$PROBE_OUTPUT" | sed -n 's/^harness_version=//p' | sed -n '1p')
      shim_pid=$(printf '%s\n' "$PROBE_OUTPUT" | sed -n 's/^shim_pid=//p' | sed -n '1p')
      child_pid=$(printf '%s\n' "$PROBE_OUTPUT" | sed -n 's/^child_pid=//p' | sed -n '1p')
      child_outcome=$(printf '%s\n' "$PROBE_OUTPUT" | sed -n 's/^child_outcome=//p' | sed -n '1p')
      case "$probe_harness" in
        claude|codex) ;;
        *) printf 'PROBE ASSERT FAIL (%s): missing valid harness observation\n' "$probe_name" >&2; return 1 ;;
      esac
      [ -n "$probe_version" ] || { printf 'PROBE ASSERT FAIL (%s): missing harness version observation\n' "$probe_name" >&2; return 1; }
      case "$shim_pid:$child_pid" in
        *[!0-9:]*|:*|*:) printf 'PROBE ASSERT FAIL (%s): missing positive shim/child PID observations\n' "$probe_name" >&2; return 1 ;;
      esac
      case "$child_outcome" in
        survived|terminated) ;;
        *) printf 'PROBE ASSERT FAIL (%s): missing closed child outcome observation\n' "$probe_name" >&2; return 1 ;;
      esac
      require_probe_text "$probe_name" 'topology=shim-parent-of-harness-child-on-pty' &&
        require_probe_text "$probe_name" 'child_ppid_matches=true' &&
        require_probe_text "$probe_name" 'child_tty=' &&
        require_probe_text "$probe_name" 'child_command=' &&
        require_probe_text "$probe_name" 'signal_target=owned-shim-only' &&
        require_probe_text "$probe_name" 'signal=SIGHUP' &&
        require_probe_text "$probe_name" 'shim_terminated=true' &&
        require_probe_text "$probe_name" 'default_tmux_targeted=false'
      ;;
    *)
      printf 'PROBE ASSERT FAIL (%s): no assertion defined\n' "$probe_name" >&2
      return 1
      ;;
  esac
}

task8_release_walkthrough() {
  local current_binary=$1
  local artifact_dir=$2
  local task8_top task8_root runtime_root state_root task8_home task8_project task8_head task8_goreleaser_config task8_sweeper
  local cleanup_status=0
  local task8_cleaned=0
  local task8_active_pid=0
  local task8_active_pgid=0
  local -a task8_archives

  task8_top=$(git rev-parse --show-toplevel 2>/dev/null) || die 'not inside a git repository'
  [ "$PWD" = "$task8_top" ] || die "run from the repo root: $task8_top"
  current_binary=$(cd "$(dirname "$current_binary")" && pwd -P)/$(basename "$current_binary")
  if [ -e "$artifact_dir" ]; then
    [ -d "$artifact_dir" ] || die "artifact path is not a directory: $artifact_dir"
    [ -z "$(find "$artifact_dir" -mindepth 1 -maxdepth 1 -print -quit)" ] || die "artifact directory is not empty: $artifact_dir"
  else
    mkdir -p "$artifact_dir" || die "could not create artifact directory: $artifact_dir"
  fi
  artifact_dir=$(cd "$artifact_dir" && pwd -P)
  [ -x "$current_binary" ] || die "current binary is not executable: $current_binary"
  chmod 0700 "$artifact_dir" || die "could not protect artifact directory: $artifact_dir"

  task8_root=$(mktemp -d /tmp/a8.XXXXXX) || die 'could not create Task 8 root'
  runtime_root="$task8_root/runtime"
  state_root="$task8_root/state"
  task8_home="$task8_root/home"
  task8_project="$task8_root/project"
  install -d -m 0700 "$runtime_root" "$state_root" "$task8_home" "$task8_project" || die 'could not create Task 8 roots'

  task8_cleanup() {
    if [ "$task8_cleaned" -eq 1 ]; then
      return "$cleanup_status"
    fi
    task8_cleaned=1
    if [ "$cleanup_status" -eq 0 ]; then
      if ! rm -rf "$task8_root"; then
        cleanup_status=1
      fi
      if [ -e "$task8_root" ]; then
        cleanup_status=1
      fi
    fi
    if [ "${AGENTCTL_TEST_TASK8_CLEANUP_FAIL:-0}" = 1 ]; then
      cleanup_status=1
    fi
    if [ "$cleanup_status" -ne 0 ]; then
      printf 'TASK8 CLEANUP FAIL root=%s\n' "$task8_root" >&2
      return 1
    fi
    printf 'TASK8 CLEANUP PASS root=%s absent=true\n' "$task8_root" >"$artifact_dir/cleanup.txt"
  }
  # shellcheck disable=SC2329 # invoked indirectly by the signal and EXIT traps below.
  task8_stop_active() {
    local task8_stop_attempt
    if [ "$task8_active_pid" -eq 0 ]; then
      return 0
    fi
    kill -TERM -- "-$task8_active_pgid" 2>/dev/null || true
    task8_stop_attempt=0
    while [ "$task8_stop_attempt" -lt 20 ]; do
      if ! kill -0 -- "-$task8_active_pgid" 2>/dev/null; then
        break
      fi
      sleep 0.05
      task8_stop_attempt=$((task8_stop_attempt + 1))
    done
    if kill -0 -- "-$task8_active_pgid" 2>/dev/null; then
      kill -KILL -- "-$task8_active_pgid" 2>/dev/null || true
    fi
    wait "$task8_active_pid" 2>/dev/null || true
    if kill -0 -- "-$task8_active_pgid" 2>/dev/null; then
      cleanup_status=1
      printf 'TASK8 ACTIVE PROCESS GROUP SURVIVED pgid=%s\n' "$task8_active_pgid" >&2
    fi
    task8_active_pid=0
    task8_active_pgid=0
  }
  task8_run() {
    local task8_log=$1
    local task8_run_status
    shift
    set -m
    "$@" >"$task8_log" 2>&1 &
    task8_active_pid=$!
    task8_active_pgid=$task8_active_pid
    if wait "$task8_active_pid"; then
      task8_run_status=0
    else
      task8_run_status=$?
    fi
    task8_active_pid=0
    task8_active_pgid=0
    set +m
    return "$task8_run_status"
  }
  task8_sweep_owned() {
    if [ ! -x "$task8_sweeper" ]; then
      return 0
    fi
    if ! "$task8_sweeper" sweep --root "$task8_root" --result "$artifact_dir/owned-process-sweep.txt"; then
      cleanup_status=1
      return 1
    fi
  }
  # shellcheck disable=SC2329 # invoked indirectly by the EXIT trap below.
  task8_exit() {
    local exit_status=$?
    trap - EXIT HUP INT TERM
    task8_stop_active
    task8_sweep_owned || true
    if ! task8_cleanup && [ "$exit_status" -eq 0 ]; then
      exit_status=1
    fi
    exit "$exit_status"
  }
  # shellcheck disable=SC2329 # invoked indirectly by signal traps below.
  task8_signal() {
    local signal_status=$1
    trap - EXIT HUP INT TERM
    task8_stop_active
    task8_sweep_owned || true
    task8_cleanup || true
    exit "$signal_status"
  }
  trap 'task8_exit' EXIT
  trap 'task8_signal 129' HUP
  trap 'task8_signal 130' INT
  trap 'task8_signal 143' TERM
  task8_phase() {
    local phase=$1
    if [ "${AGENTCTL_TEST_TASK8_BLOCK_PHASE:-}" = "$phase" ]; then
      # shellcheck disable=SC2016 # Perl and child-shell variables deliberately expand in their processes.
      task8_run "$artifact_dir/task8-blocking-phase.log" bash -c '
        raw_pid_file=$1
        ready_pid_file=$2
        sweeper=$3
        journal=$4
        detach_gate=$5
        detached_ready=$6
        external_journal=$7
        /usr/bin/perl -MPOSIX -e '\''
          open my $pid_file, ">", $ARGV[0] or die "pid file: $!";
          print {$pid_file} "$$\n" or die "pid write: $!";
          close $pid_file or die "pid close: $!";
          while (!-e $ARGV[1]) { select undef, undef, undef, 0.01 }
          POSIX::setsid() >= 0 or die "setsid: $!";
          $SIG{TERM} = "IGNORE";
          $SIG{HUP} = "IGNORE";
          open my $ready_file, ">", $ARGV[2] or die "ready file: $!";
          print {$ready_file} "ready\n" or die "ready write: $!";
          close $ready_file or die "ready close: $!";
          while (1) { select undef, undef, undef, 1 }
        '\'' "$raw_pid_file" "$detach_gate" "$detached_ready" &
        detached_pid=$!
        while [ ! -s "$raw_pid_file" ]; do sleep 0.01; done
        "$sweeper" record --pid-file "$raw_pid_file" --journal "$journal"
        "$sweeper" record --pid-file "$raw_pid_file" --journal "$external_journal"
        : >"$detach_gate"
        while [ ! -s "$detached_ready" ]; do sleep 0.01; done
        read -r recorded_identity <"$journal"
        printf "%s\n" "$recorded_identity" >"$ready_pid_file"
        wait "$detached_pid"
      ' task8-blocker "$task8_root/task8-detached.pid" \
        "${AGENTCTL_TEST_TASK8_CHILD_PID_FILE:?missing Task 8 child PID file}" "$task8_sweeper" \
        "$task8_root/owned-identities.txt" "$task8_root/task8-detach-gate" \
        "$task8_root/task8-detached-ready" \
        "${AGENTCTL_TEST_TASK8_CHILD_IDENTITY_JOURNAL:?missing Task 8 external identity journal}" || return $?
    fi
  }
  task8_sweeper="$task8_root/shim-version-sweeper"
  if [ -n "${AGENTCTL_TEST_TASK8_SWEEPER_BUILD_DELAY_SECONDS:-}" ]; then
    sleep "$AGENTCTL_TEST_TASK8_SWEEPER_BUILD_DELAY_SECONDS"
  fi
  task8_run "$artifact_dir/task8-sweeper-build.log" go build -o "$task8_sweeper" ./hack/fixtures/shim-version || die 'could not build Task 8 owned-process sweeper'
  if [ "${AGENTCTL_TEST_TASK8_PHASE_DRIVER:-0}" = 1 ]; then
    for task8_test_phase in roots surface skill matrix integration kernel safety archives metadata; do
      task8_phase "$task8_test_phase"
    done
    die "unknown Task 8 test phase: ${AGENTCTL_TEST_TASK8_BLOCK_PHASE:-absent}"
  fi
  task8_phase roots

  printf '== Task 8: built release-candidate surface ==\n'
  task8_run "$artifact_dir/current-version.txt" "$current_binary" version || die 'release candidate version failed'
  task8_run "$artifact_dir/help.txt" "$current_binary" --help || die 'release candidate help failed'
  task8_run "$artifact_dir/relaunch-help.txt" "$current_binary" relaunch --help || die 'release candidate relaunch help failed'
  grep -q '  run ' "$artifact_dir/help.txt" || die 'release candidate help omits run'
  if grep -q '__shim' "$artifact_dir/help.txt"; then
    die 'release candidate help exposes __shim'
  fi
  grep -q 'ESRCH-backed stale durable child record' "$artifact_dir/relaunch-help.txt" || die 'relaunch help omits ESRCH contract'
  if grep -q 'no-baseline' "$artifact_dir/relaunch-help.txt"; then
    die 'relaunch help retains pre-shim no-baseline recovery'
  fi
  task8_run "$artifact_dir/go-version-m.txt" go version -m "$current_binary" || die 'could not read release candidate Go metadata'
  grep -q 'golang.org/x/sys[[:space:]]\+v0.47.0' "$artifact_dir/go-version-m.txt" || die 'release candidate does not record golang.org/x/sys v0.47.0'
  task8_head=$(git rev-parse HEAD) || die 'could not resolve current HEAD'
  grep -q "vcs.revision=$task8_head" "$artifact_dir/go-version-m.txt" || die 'release candidate VCS revision differs from current HEAD'
  grep -q 'vcs.modified=false' "$artifact_dir/go-version-m.txt" || die 'release candidate records a modified source tree'
  task8_phase surface

  printf '== Task 8: installed skill from release candidate ==\n'
  task8_run "$artifact_dir/skill-install.txt" env HOME="$task8_home" AGENTCTL_RUNTIME_ROOT="$runtime_root" AGENTCTL_STATE_ROOT="$state_root" \
    "$current_binary" skill install || die 'release candidate skill install failed'
  task8_run "$artifact_dir/skill-status.txt" env HOME="$task8_home" AGENTCTL_RUNTIME_ROOT="$runtime_root" AGENTCTL_STATE_ROOT="$state_root" \
    "$current_binary" skill status || die 'release candidate skill status failed'
  task8_run "$artifact_dir/skill-claude.diff" diff -ru --exclude=.agentctl-skill.json "$task8_top/skills/agentctl" "$task8_home/.claude/skills/agentctl" || die 'installed Claude skill differs from source tree'
  task8_run "$artifact_dir/skill-codex.diff" diff -ru --exclude=.agentctl-skill.json "$task8_top/skills/agentctl" "$task8_home/.agents/skills/agentctl" || die 'installed Codex skill differs from source tree'
  task8_phase skill

  printf '== Task 8: separately built protocol-skew matrix ==\n'
  task8_run "$artifact_dir/shim-version-matrix.log" env AGENTCTL_SHIM_VERSION_OWNED_ROOT="$task8_root/matrix" \
    "$task8_top/hack/release-verify.sh" \
    --shim-version-matrix "$current_binary" "$artifact_dir/shim-version-matrix" || die 'Task 8 shim-version matrix failed'
  task8_phase matrix

  printf '== Task 8: live isolated layout/lifecycle evidence ==\n'
  task8_run "$artifact_dir/integration.log" env AGENTCTL_INTEGRATION_RELEASE_CANDIDATE="$current_binary" \
    AGENTCTL_INTEGRATION_PROJECT_DIR="$task8_project" AGENTCTL_INTEGRATION_OWNED_ROOT="$task8_root/integration" \
    go test -tags integration ./cmd/agentctl -count=1 -v \
    -run 'TestIntegration(ReleaseCandidateSelection|ReleaseCandidateForegroundExtendsRosterAndRefusesDifferentDirectory|ReleaseCandidateStatusReportsUnanchoredDurableRecord|ReleaseCandidateLayoutOperationsPreserveCLIIdentityAndDelivery|ReleaseCandidateCrashRelaunchAndKillUseObservedAbsence|PublicForegroundRunUsesRuntimeWithoutTmux|PublicCommandsConsultRuntimeAnchorBeforeReportingDivergentStateRootMissing|PublicAttachRefusesAbsentPresentationForDurableFleet)' \
    || die 'Task 8 live integration evidence failed'
  task8_phase integration

  printf '== Task 8: kernel absence/refusal and raw-token evidence ==\n'
  task8_run "$artifact_dir/kernel-utc-c.log" env TZ=UTC LC_ALL=C go test ./internal/shim -count=1 -v \
    -run 'Test(ProcessObservationUsesESRCHAsTheSoleAbsencePermission|ReadStartTokenObservesRawDarwinKinfoProc|ProcessObservationLiveForeignProcessAndReapedAbsence|LocalPeerPIDObservesConnectedAnswerer|ShimClientRejectsAnswererDisagreementBeforeSendingRequest)' \
    || die 'Task 8 UTC/C kernel evidence failed'
  task8_run "$artifact_dir/kernel-auckland-utf8.log" env TZ=Pacific/Auckland LC_ALL=en_US.UTF-8 go test ./internal/shim -count=1 -v \
    -run 'Test(ReadStartTokenObservesRawDarwinKinfoProc|ProcessObservationLiveForeignProcessAndReapedAbsence)' \
    || die 'Task 8 timezone/locale raw-token evidence failed'
  task8_phase kernel
  task8_run "$artifact_dir/fail-closed-safety.log" go test ./internal/shim ./internal/fleet -count=1 -v \
    -run 'Test(ShimServerRefusesIdentityAndReadinessBeforePTYMutation|ShimRelaunchRefusesEveryNonAbsentRuntimeStateBeforeMutation|ShimRelaunchRemovesStaleRecordOnlyAfterFreshESRCHThenStartsOneShim|RuntimeShimRoleInspectorNeverAutoDeletesDeadChildStarting)' \
    || die 'Task 8 readiness/orphan/relaunch safety evidence failed'

  printf '== Task 8: structural, archive-license, and skill drift guards ==\n'
  task8_run "$artifact_dir/structural-archive.log" go test ./internal/structural ./hack -count=1 \
    -run 'Test(Production|VerifyReleaseArchives|ShimVersionFixtureExercisesBothBuiltArtifacts)' \
    || die 'Task 8 structural/archive fixture gates failed'
  task8_run "$artifact_dir/skill-drift.log" go test ./cmd/agentctl -count=1 -v \
    -run 'Test(HiddenShimRouteIsAbsentFromAgentFacingInventories|DocumentedAgentCommandContract|ParsedCommandRegistryCouplesParserAndAgentDocumentation|ParsedCommandRegistryProjectsRegisteredOptions|ExitCodeTableMatchesConstants|StatusStatesMatch|RunLaunchPrintsSkillSkewNoticesAfterShimSuccess)' \
    || die 'Task 8 skill drift gates failed'
  task8_phase safety

  printf '== Task 8: actual snapshot archive license contents ==\n'
  task8_goreleaser_config="$task8_root/goreleaser.yaml"
  awk -v task8_dist="$task8_root/dist" '
    { print }
    $0 == "project_name: agentctl" { print "dist: \"" task8_dist "\"" }
  ' "$task8_top/.goreleaser.yaml" >"$task8_goreleaser_config" || die 'could not create isolated goreleaser configuration'
  task8_run "$artifact_dir/goreleaser-snapshot.log" goreleaser release --config "$task8_goreleaser_config" --snapshot --clean --skip=notarize || die 'Task 8 snapshot release failed'
  task8_archives=("$task8_root"/dist/*.tar.gz)
  [ -e "${task8_archives[0]}" ] || die 'Task 8 snapshot produced no tar.gz archives'
  task8_run "$artifact_dir/archive-verification.log" "$task8_top/hack/verify-release-archives.sh" "${task8_archives[@]}" || die 'Task 8 actual archive verification failed'
  task8_phase archives

  {
    printf 'mode=task8-automated-release-candidate\n'
    printf 'current_binary=%s\n' "$current_binary"
    printf 'runtime_root=%s\n' "$runtime_root"
    printf 'state_root=%s\n' "$state_root"
    printf 'home=%s\n' "$task8_home"
    printf 'project=%s\n' "$task8_project"
    printf 'go=%s\n' "$(go version)"
    printf 'darwin=%s\n' "$(sw_vers -productVersion)"
    printf 'tmux=%s\n' "$(tmux -V)"
    printf 'tmux_scope=integration tests use fixture-owned named sockets only\n'
    printf 'skill_pairing=release-candidate install matches source tree for claude and codex\n'
    printf 'r23=verified landed agent-facing surface; no duplicate implementation\n'
    printf 'r23a=verified RuntimeStates drift fixture; no duplicate implementation\n'
  } >"$artifact_dir/metadata.txt"
  task8_phase metadata

  task8_sweep_owned || die 'Task 8 owned-process sweep failed'
  task8_cleanup
  trap - EXIT HUP INT TERM
  printf 'TASK8 RELEASE WALKTHROUGH PASS evidence=%s\n' "$artifact_dir"
}

# Pure, testable subcommands are handled before the live-environment flow.
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

if [ "${1:-}" = '--assert-probe' ]; then
  [ "$#" -eq 3 ] || die '--assert-probe requires PROBE_NAME OUTPUT_FILE'
  [ -r "$3" ] || die "cannot read probe output: $3"
  probe_out=$(cat "$3")
  assert_probe_output "$2" "$probe_out" && exit 0 || exit 1
fi

if [ "${1:-}" = '--shim-version-matrix' ]; then
  [ "$#" -eq 3 ] || die '--shim-version-matrix requires CURRENT_BINARY ARTIFACT_DIR'
  matrix_top=$(git rev-parse --show-toplevel 2>/dev/null) || die 'not inside a git repository'
  [ "$PWD" = "$matrix_top" ] || die "run from the repo root: $matrix_top"
  [ -x "$2" ] || die "current binary is not executable: $2"
  install -d -m 0700 "$3" || die "could not create artifact directory: $3"
  fixture_binary="$3/shim-version-fixture"
  go build -o "$fixture_binary" ./hack/fixtures/shim-version || die 'could not build shim-version fixture'
  "$fixture_binary" matrix --current-binary "$2" --artifact-dir "$3"
  exit 0
fi

if [ "${1:-}" = '--task8' ]; then
  [ "$#" -eq 3 ] || die '--task8 requires CURRENT_BINARY ARTIFACT_DIR'
  task8_release_walkthrough "$2" "$3"
  exit 0
fi

if [ "${1:-}" = '--help' ] || [ "${1:-}" = '-h' ]; then
  usage
  exit 0
fi

MEASURE=0
NON_INTERACTIVE=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --measure) MEASURE=1 ;;
    --non-interactive) NON_INTERACTIVE=1 ;;
    *) die "unsupported argument: $1" ;;
  esac
  shift
done
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

for cmd_name in tmux claude codex amq install; do
  command -v "$cmd_name" >/dev/null 2>&1 || die "required command not found: $cmd_name"
done

EVIDENCE_DIR=$(mktemp -d /tmp/agentctl-release-verify.XXXXXX) || die 'could not create evidence directory'
VERSIONS_FILE="$EVIDENCE_DIR/versions.txt"

part_header A 'Automated release checks'
step_start A.1 'build and capture versions'
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
step_pass A.1 'build and version capture completed'

# ---------------------------------------------------------------------------
# 2. Probes (fully automated)
# ---------------------------------------------------------------------------

step_start A.2 'run contract probes'
echo '== Probes =='
probe_index=1
for probe_name in probe-1-argv.sh probe-2-targeting.sh probe-3-ids.sh probe-4-attach.sh; do
  probe="$TOP/hack/$probe_name"
  echo "-- $probe_name --"
  probe_status=0
  probe_out=$(bash "$probe" </dev/null 2>&1) || probe_status=$?
  printf '%s\n' "$probe_out"
  if [ "$probe_status" -ne 0 ]; then
    step_fail "A.$((probe_index + 1))" "$probe_name failed"
    echo "PROBES FAIL ($probe_name: exit $probe_status)"
    exit 1
  fi
  if ! assert_probe_output "$probe_name" "$probe_out"; then
    step_fail "A.$((probe_index + 1))" "$probe_name assertion failed"
    exit 1
  fi
  step_pass "A.$((probe_index + 1))" "$probe_name assertion completed"
  probe_index=$((probe_index + 1))
done

probe_name='probe-shim-sighup.sh'
probe="$TOP/hack/$probe_name"
for probe_harness in claude codex; do
  probe_output_file="$EVIDENCE_DIR/probe-shim-sighup-$probe_harness.txt"
  echo "-- $probe_name ($probe_harness) --"
  probe_status=0
  probe_stdout=$(bash "$probe" --harness "$probe_harness" --output "$probe_output_file" </dev/null 2>&1) || probe_status=$?
  printf '%s\n' "$probe_stdout"
  if [ "$probe_status" -ne 0 ]; then
    step_fail "A.$((probe_index + 1))" "$probe_name ($probe_harness) failed"
    echo "PROBES FAIL ($probe_name, $probe_harness: exit $probe_status)"
    exit 1
  fi
  [ -r "$probe_output_file" ] || die "$probe_name ($probe_harness) produced no readable evidence"
  probe_out=$(cat "$probe_output_file")
  printf '%s\n' "$probe_out"
  if ! assert_probe_output "$probe_name" "$probe_out"; then
    step_fail "A.$((probe_index + 1))" "$probe_name ($probe_harness) assertion failed"
    exit 1
  fi
  step_pass "A.$((probe_index + 1))" "$probe_name ($probe_harness) assertion completed"
  probe_index=$((probe_index + 1))
done

if pgrep -fl '[t]mux.*agentctl-probe-' >/dev/null 2>&1; then
  step_fail "A.$((probe_index + 1))" 'throwaway probe tmux server survived'
  echo 'PROBES FAIL (throwaway probe tmux server still running)'
  exit 1
fi

step_pass "A.$((probe_index + 1))" 'no throwaway probe tmux server survived'
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
  part_header B 'Live release-candidate delivery'

  ARTIFACT_DIR="$EVIDENCE_DIR/verify-live"
  mkdir "$ARTIFACT_DIR"
  LIVE_SESSION=relverify
  LIVE_STATUS=0
  ATTACH_ATTESTATION=''
  CLAUDE_CLEAR_ATTESTATION=''
  CODEX_CLEAR_ATTESTATION=''
  COMPACT_ATTESTATION=''
  RELAUNCH_ATTESTATION=''
  PART_B_DETACH_ATTESTATION=''
  RELAUNCH_CHECK=FAIL
  TEARDOWN_CHECK=FAIL
  TEARDOWN_STATUS_EXIT=''
  STATUS_STDOUT="$ARTIFACT_DIR/status.stdout"
  STATUS_STDERR="$ARTIFACT_DIR/status.stderr"

  if session_absent "$LIVE_SESSION" "$STATUS_STDOUT" "$STATUS_STDERR"; then
    :
  else
    absence_status=$?
    if [ "$absence_status" -eq 1 ]; then
      die "session $LIVE_SESSION already exists; refusing to use or kill it"
    fi
    if [ "$STATUS_EXIT" -eq 6 ] && default_tmux_server_connect_enoent "$STATUS_STDERR"; then
      cat "$STATUS_STDERR" >&2
      PART_B_PRECHECK_OBSERVATION='default tmux server absent (connect ENOENT)'
      printf 'PART B PRECHECK OBSERVED (default tmux server absent: connect ENOENT)\n'
      PART_B_KEEPER_SESSION="agentctl-release-verify-keeper-$$"
      if ! tmux new-session -d -s "$PART_B_KEEPER_SESSION" -n keeper -- 'exec sleep 86400'; then
        die "could not create wrapper-owned tmux keeper session $PART_B_KEEPER_SESSION"
      fi
      PART_B_KEEPER_OWNED=1
      trap cleanup_exit_trap EXIT
      printf 'PART B KEEPER CREATED (wrapper-owned session %s keeps the default tmux server available)\n' "$PART_B_KEEPER_SESSION"

      if session_absent "$LIVE_SESSION" "$STATUS_STDOUT" "$STATUS_STDERR"; then
        :
      else
        absence_status=$?
        if [ "$absence_status" -eq 1 ]; then
          die "session $LIVE_SESSION appeared after keeper creation; refusing to use or kill it"
        fi
        cat "$STATUS_STDERR" >&2
        die "could not prove session $LIVE_SESSION is absent after keeper creation (status exit $STATUS_EXIT)"
      fi
    else
      cat "$STATUS_STDERR" >&2
      die "could not prove session $LIVE_SESSION is absent (status exit $STATUS_EXIT)"
    fi
  fi

  step_start B.1 'launch release-candidate fleet'
  echo 'Running:'
  echo '  ./bin/agentctl launch --session relverify --roles a:claude,b:codex --efforts b:high'
  if ! ./bin/agentctl launch --session "$LIVE_SESSION" --roles a:claude,b:codex --efforts b:high; then
    die 'live release verification launch failed'
  fi
  step_pass B.1 'release-candidate fleet launched'

  # This run owns relverify only after launch succeeds. Keep teardown armed
  # across every later command and attestation; explicit teardown disarms it.
  PART_B_TOP=$TOP
  PART_B_SESSION=$LIVE_SESSION
  PART_B_SESSION_OWNED=1
  trap cleanup_exit_trap EXIT

  if [ "$LIVE_STATUS" -eq 0 ]; then
    cat <<'EOF'

Part B uses two iTerm2 windows. First, leave this verifier running in Window 1.
In iTerm2, press Command-N to open a second iTerm2 window (Window 2). Run the
attach command below in Window 2. Keep the Window 2 attachment open throughout
the live checks. After each visual observation, return to Window 1 to answer each numbered checkpoint. The verifier will tell you when to detach later.
EOF
    echo 'Attach from Window 2 with:'
    echo '  ./bin/agentctl attach --session relverify'
    attach_expected=$(cat <<'EOF'
agentctl: attaching session "relverify" (2 windows) in iTerm2…
The Command Menu belongs to iTerm2. The claude and codex tabs are visible.
The parenthesized two-window count is advisory: if it is omitted, record the
advisory read failure; omission is not a release failure and agentctl never
guesses the count.
EOF
)
    if checkpoint B.C1 'attach narration' "$attach_expected" 'Is Window 2 attached and showing the claude and codex tabs?'; then
      ATTACH_ATTESTATION=$ASK_ANSWER
    else
      ATTACH_ATTESTATION=$ASK_ANSWER
      LIVE_STATUS=1
    fi
  fi

  if [ "$LIVE_STATUS" -eq 0 ]; then
    echo
    echo 'In the claude tab, type junk into the input box; do NOT press Enter.'
    if checkpoint B.C2 'claude clear setup' 'junk is visible in the claude input without being submitted.' 'Is the claude junk ready for agentctl clear?'; then
      echo 'Running:'
      echo '  ./bin/agentctl clear --session relverify a'
      if ./bin/agentctl clear --session "$LIVE_SESSION" a; then
        echo 'Claude clear delivery result printed above.'
        action_pass B.3 'claude clear delivery command completed; observed outcome pending checkpoint B.C3'
        if checkpoint B.C3 'claude clear delivery' 'junk cleared, /clear executed, and the conversation reset.' 'For claude, was junk visibly cleared, /clear executed, and the conversation reset?'; then
          CLAUDE_CLEAR_ATTESTATION=$ASK_ANSWER
        else
          CLAUDE_CLEAR_ATTESTATION=$ASK_ANSWER
          LIVE_STATUS=1
        fi
      else
        echo 'LIVE VERIFY FAIL (claude clear delivery failed)'
        action_fail B.3 'claude clear delivery command failed'
        LIVE_STATUS=1
      fi
    else
      LIVE_STATUS=1
    fi
  fi

  if [ "$LIVE_STATUS" -eq 0 ]; then
    echo
    echo 'In the codex tab, type junk into the input box; do NOT press Enter.'
    if checkpoint B.C4 'codex clear setup' 'junk is visible in the codex input without being submitted.' 'Is the codex junk ready for agentctl clear?'; then
      echo 'Running:'
      echo '  ./bin/agentctl clear --session relverify b'
      if ./bin/agentctl clear --session "$LIVE_SESSION" b; then
        echo 'Codex clear delivery result printed above.'
        action_pass B.4 'codex clear delivery command completed; observed outcome pending checkpoint B.C5'
        if checkpoint B.C5 'codex clear delivery' 'junk cleared, /clear executed, and the conversation reset.' 'For codex, was junk visibly cleared, /clear executed, and the conversation reset?'; then
          CODEX_CLEAR_ATTESTATION=$ASK_ANSWER
        else
          CODEX_CLEAR_ATTESTATION=$ASK_ANSWER
          LIVE_STATUS=1
        fi
      else
        echo 'LIVE VERIFY FAIL (codex clear delivery failed)'
        action_fail B.4 'codex clear delivery command failed'
        LIVE_STATUS=1
      fi
    else
      LIVE_STATUS=1
    fi
  fi

  if [ "$LIVE_STATUS" -eq 0 ]; then
    echo
    echo 'Before the compact spot check, create compactable context in the claude tab:'
    echo 'type "Reply with FIRST READY and one sentence about this repository." and press Enter.'
    echo "Wait for Claude's complete response, then submit this second message:"
    echo '"Reply with SECOND READY and one different sentence about testing this repository."'
    echo "Wait for Claude's second complete response. Then type junk into the input box; do NOT press Enter."
    if checkpoint B.C6 'claude compact setup' "Claude's two responses are complete, and junk is visible in the claude input without being submitted." 'Are both Claude responses complete and the junk ready for the compact spot check?'; then
      echo 'Running:'
      echo '  ./bin/agentctl compact --session relverify a'
      if ./bin/agentctl compact --session "$LIVE_SESSION" a; then
        echo 'Claude compact delivery result printed above.'
        action_pass B.5 'claude compact delivery command completed; observed outcome pending checkpoint B.C7'
        if checkpoint B.C7 'claude compact delivery' 'junk cleared, /compact executed, and the conversation compacted.' 'For claude, was junk visibly cleared, /compact executed, and the conversation compacted?'; then
          COMPACT_ATTESTATION=$ASK_ANSWER
        else
          COMPACT_ATTESTATION=$ASK_ANSWER
          LIVE_STATUS=1
        fi
      else
        echo 'LIVE VERIFY FAIL (claude compact delivery failed)'
        action_fail B.5 'claude compact delivery command failed'
        LIVE_STATUS=1
      fi
    else
      LIVE_STATUS=1
    fi
  fi

  if [ "$LIVE_STATUS" -eq 0 ]; then
    echo
    echo '== Relaunch verification =='
    if ! resolve_live_session_id "$LIVE_SESSION"; then
      echo 'RELAUNCH FAIL (could not resolve relverify to one exact tmux session ID)'
      LIVE_STATUS=1
    elif ! resolve_role_window "$LIVE_SESSION_ID" a; then
      echo 'RELAUNCH FAIL (could not resolve role a to one exact tmux window and pane ID)'
      LIVE_STATUS=1
    elif ! resolve_running_role_processes "$LIVE_SESSION" a "$ARTIFACT_DIR/relaunch-before.status"; then
      echo 'RELAUNCH FAIL (could not resolve role a to one running shim and child PID)'
      LIVE_STATUS=1
    else
      original_pane_id=$ROLE_PANE_ID
      original_shim_pid=$ROLE_SHIM_PID
      original_child_pid=$ROLE_CHILD_PID
      echo 'In the claude tab, type junk into the input box again; do NOT press Enter.'
    if checkpoint B.C8 'relaunch setup' 'junk is visible in the claude input without being submitted.' 'Is the claude junk ready for the relaunch process-discontinuity check?'; then
        echo 'Running exact-PID shim termination setup:'
        printf '  kill -HUP %s  # role a shim; recorded child %s\n' "$original_shim_pid" "$original_child_pid"
        if ! kill -HUP "$original_shim_pid"; then
          echo "RELAUNCH FAIL (could not signal role a shim PID $original_shim_pid)"
          LIVE_STATUS=1
        else
          step_pass B.6 'exact-PID role shim termination completed'
        fi
      else
        LIVE_STATUS=1
      fi
    fi
  fi

  if [ "$LIVE_STATUS" -eq 0 ]; then
    echo 'Running:'
    printf '  kill -0 %s  # wait for recorded role a child absence\n' "$original_child_pid"
    if wait_process_absent "$original_child_pid"; then
      echo 'RELAUNCH PASS (recorded role a child no longer responds to signal 0)'
      step_pass B.7 'recorded role a child absence observed'
    else
      echo "RELAUNCH FAIL (recorded role a child PID $original_child_pid still responds to signal 0)"
      LIVE_STATUS=1
    fi
  fi

  if [ "$LIVE_STATUS" -eq 0 ]; then
    echo 'Running:'
    echo '  ./bin/agentctl relaunch --session relverify a'
    if ./bin/agentctl relaunch --session "$LIVE_SESSION" a >"$ARTIFACT_DIR/relaunch.stdout"; then
      cat "$ARTIFACT_DIR/relaunch.stdout"
      echo 'RELAUNCH PASS (role a relaunched through the ESRCH-gated command)'
      step_pass B.8 'ESRCH-gated relaunch command completed'
    else
      cat "$ARTIFACT_DIR/relaunch.stdout"
      echo 'RELAUNCH FAIL (agentctl relaunch failed)'
      LIVE_STATUS=1
    fi
  fi

  if [ "$LIVE_STATUS" -eq 0 ]; then
    if ! resolve_role_window "$LIVE_SESSION_ID" a; then
      echo 'RELAUNCH FAIL (could not resolve the recreated role a window and pane IDs)'
      LIVE_STATUS=1
    elif [ "$ROLE_PANE_ID" = "$original_pane_id" ]; then
      echo "RELAUNCH FAIL (recreated role a reused original pane $original_pane_id)"
      LIVE_STATUS=1
    else
      printf 'RELAUNCH PASS (role a pane changed from %s to %s)\n' "$original_pane_id" "$ROLE_PANE_ID"
      step_pass B.9 'replacement pane ID differs from original'
      expected_relaunch="agentctl: relaunched a in relverify: window $ROLE_WINDOW_ID, pane $ROLE_PANE_ID, harness claude (stored), model default (stored), effort default (stored), dir $TOP (stored)"
      actual_relaunch=$(cat "$ARTIFACT_DIR/relaunch.stdout")
      if [ "$actual_relaunch" != "$expected_relaunch" ]; then
        printf 'RELAUNCH FAIL (provenance output mismatch):\n  got:  %s\n  want: %s\n' "$actual_relaunch" "$expected_relaunch"
        LIVE_STATUS=1
      fi
    fi
  fi

  if [ "$LIVE_STATUS" -eq 0 ]; then
    echo 'Running:'
    echo '  ./bin/agentctl status --session relverify'
    if assert_role_state "$LIVE_SESSION" a running "$ARTIFACT_DIR/relaunch-running.status"; then
      echo 'RELAUNCH PASS (role a restored to running)'
      step_pass B.10 'recreated role is running'
    else
      echo 'RELAUNCH FAIL (role a did not return to running)'
      LIVE_STATUS=1
    fi
  fi

  if [ "$LIVE_STATUS" -eq 0 ]; then
    relaunch_prompt=$(cat <<'EOF'
One of the fleet's harnesses was terminated, and agentctl relaunched it from
the fleet's stored configuration. The new pane is a new process: its harness,
model and effort carry over; its conversation does not, so the junk you typed
is gone.

Do you see a fresh, ready claude input surface with no trace of that junk?
EOF
)
    if checkpoint B.C9 'live delivery and relaunch' 'the replacement claude pane is fresh and has no trace of the staged junk.' "$relaunch_prompt"; then
      RELAUNCH_ATTESTATION=$ASK_ANSWER
      RELAUNCH_CHECK='PASS (stored claude/default/default provenance; pane ID changed)'
    else
      RELAUNCH_ATTESTATION=$ASK_ANSWER
      LIVE_STATUS=1
    fi
  fi

  if [ "$LIVE_STATUS" -eq 0 ]; then
    cat <<'EOF'

Return to Window 2, press esc to detach cleanly; do not use uppercase X. Wait
for the post-detach session-state report, then return to Window 1 and confirm
the numbered checkpoint below.
EOF
    if checkpoint B.C10 'Part B attachment detached' 'Window 2 printed the post-detach session-state report and returned to its shell.' 'Did Window 2 detach cleanly and print the post-detach session-state report?'; then
      PART_B_DETACH_ATTESTATION=$ASK_ANSWER
    else
      PART_B_DETACH_ATTESTATION=$ASK_ANSWER
      LIVE_STATUS=1
    fi
  fi

  echo
  echo '== Automated teardown =='
  echo 'Running:'
  echo '  ./bin/agentctl kill --session relverify'
  TEARDOWN_STATUS=0
  if ! part_b_teardown; then
    echo 'TEARDOWN FAIL (relverify cleanup coordinator kill failed)'
    TEARDOWN_STATUS=1
  fi

  if session_absent "$LIVE_SESSION" "$STATUS_STDOUT" "$STATUS_STDERR"; then
    TEARDOWN_STATUS_EXIT=$STATUS_EXIT
    printf 'TEARDOWN PASS (agentctl status exit %s proves relverify is absent)\n' "$TEARDOWN_STATUS_EXIT"
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

  tmux_settle_retries=6
  tmux_settle_attempt=0
  while true; do
    if surviving_tmux=$(pgrep -fl '[t]mux.*relverify' 2>&1); then
      pgrep_status=0
    else
      pgrep_status=$?
    fi
    case "$pgrep_status" in
      0)
        if [ "$tmux_settle_attempt" -ge "$tmux_settle_retries" ]; then
          printf 'TEARDOWN FAIL (relverify tmux process remains):\n%s\n' "$surviving_tmux"
          TEARDOWN_STATUS=1
          break
        fi
        tmux_settle_attempt=$((tmux_settle_attempt + 1))
        sleep 0.5
        ;;
      1)
        echo 'TEARDOWN PASS (no relverify tmux process remains)'
        break
        ;;
      *)
        printf 'TEARDOWN FAIL (pgrep exited %s):\n%s\n' "$pgrep_status" "$surviving_tmux"
        TEARDOWN_STATUS=1
        break
        ;;
    esac
  done

  if [ "$TEARDOWN_STATUS" -eq 0 ]; then
    TEARDOWN_CHECK=PASS
    step_pass B.12 'relverify teardown checks completed'
  else
    LIVE_STATUS=1
  fi

  PART_A_RESULT='PASS — automated probes and isolation checks completed'
  PART_B_RESULT='FAIL — Part B did not complete'
  if [ "$LIVE_STATUS" -eq 0 ]; then
    PART_B_RESULT='PASS — operator confirmed: numbered attach, delivery, relaunch, and detach checkpoints B.C1-B.C10'
  elif [ "$FAILED_CHECKPOINT_RESULT" = refused ]; then
    case "$FAILED_CHECKPOINT_ID" in
      B.C*) PART_B_RESULT="FAIL — operator refused checkpoint $FAILED_CHECKPOINT_ID" ;;
    esac
  elif [ "$FAILED_CHECKPOINT_RESULT" = input ]; then
    case "$FAILED_CHECKPOINT_ID" in
      B.C*) PART_B_RESULT="FAIL — checkpoint input failed at $FAILED_CHECKPOINT_ID" ;;
    esac
  fi
  PART_C_RESULT='FAIL — not run'
  PART_C_AUTH_ATTESTATION=''
  PART_C_SKILL_ATTESTATION=''
  PART_C_MEANING_ATTESTATION=''

  if [ "$LIVE_STATUS" -eq 0 ]; then
    part_header C 'Live skill discovery and meaning'
    PART_C_TOP=$TOP
    PART_C_ROOT=$(mktemp -d /tmp/agentctl-skill-verify.XXXXXX) || die 'could not create Part C temporary root'
    PART_C_ACTIVE=1
    PART_C_ORIGINAL_HOME=$HOME
    PART_C_ORIGINAL_PATH=$PATH
    PART_C_SOCKET="agentctl-skill-verify-$$"
    PART_C_REAL_TMUX=$(command -v tmux) || {
      part_c_abort 'could not resolve tmux for Part C'
    }
    PART_C_SOCKET_ARMED=1
    PART_C_HOME="$PART_C_ROOT/home"
    PART_C_PROJECT="$PART_C_ROOT/project"
    PART_C_BIN="$PART_C_ROOT/bin"
    PART_C_KEYCHAIN_SOURCE="$PART_C_ORIGINAL_HOME/Library/Keychains"
    PART_C_KEYCHAIN_LINK="$PART_C_HOME/Library/Keychains"

    step_start C.1 'create isolated temporary HOME, project, and tmux shim'
    install -d -m 0700 "$PART_C_HOME" || {
      part_c_abort 'could not create Part C temporary HOME'
    }
    install -d -m 0755 "$PART_C_PROJECT" "$PART_C_BIN" || {
      part_c_abort 'could not create Part C directories'
    }
    printf '#!/usr/bin/env bash\nexec %q -L %q "$@"\n' "$PART_C_REAL_TMUX" "$PART_C_SOCKET" >"$PART_C_BIN/tmux"
    chmod 0755 "$PART_C_BIN/tmux"
    step_pass C.1 'isolated Part C filesystem is active'

    step_start C.2 'choose authentication path for the isolated HOME'
    if part_c_has_seedable_auth; then
      printf 'Part C can seed codex authentication from this empirically proven file:\n'
      part_c_print_seedable_auth
      if ask 'Copy only this Codex file into the temporary Part C HOME?'; then
        if ! part_c_seed_auth; then
          step_fail C.2 'authentication file copy failed'
          part_c_abort 'Part C authentication seeding failed'
        fi
        PART_C_AUTH_MODE=codex-seeded
        step_pass C.2 'operator consented and the proven Codex authentication file was copied'
      else
        case "$ASK_RESULT" in
          n) PART_C_AUTH_MODE=manual ;;
          *)
            step_fail C.2 'authentication selection input failed'
            part_c_abort 'Part C authentication selection input failed'
            ;;
        esac
      fi
    else
      printf 'No proven Codex authentication file was found in the real HOME.\n'
      PART_C_AUTH_MODE=manual
    fi

    part_c_keychain_offered=0
    if part_c_has_keychain_source; then
      part_c_keychain_offered=1
      printf 'Claude Code 2.1.226 can authenticate through this exact symlink:\n'
      printf '  %s -> %s\n' "$PART_C_KEYCHAIN_SOURCE" "$PART_C_KEYCHAIN_LINK"
      printf "Both probe harnesses can reach the operator's login keychain through this link; per-item ACLs still apply.\n"
      printf 'No Claude credential is copied into the temporary HOME.\n'
      printf 'Part C will synthesize this minimal Claude onboarding configuration:\n'
      printf '  %s\n' "$PART_C_HOME/.claude.json"
      printf "It contains onboarding state only, not credentials, and does not copy the operator's Claude configuration.\n"
      if ask "Create exactly this Claude Keychains symlink and synthesized onboarding configuration: $PART_C_KEYCHAIN_SOURCE -> $PART_C_KEYCHAIN_LINK?"; then
        if ! part_c_link_keychains; then
          step_fail C.2 'Claude Keychains link creation failed'
          part_c_abort 'Part C Claude Keychains link creation failed'
        fi
        if ! part_c_seed_claude_onboarding; then
          step_fail C.2 'Claude onboarding configuration seeding failed'
          part_c_abort 'Part C Claude onboarding configuration seeding failed'
        fi
        PART_C_CLAUDE_AUTH_MODE=keychain-linked
        step_pass C.2 'operator consented and the exact Claude Keychains symlink plus synthesized onboarding configuration were created'
      else
        case "$ASK_RESULT" in
          n) PART_C_CLAUDE_AUTH_MODE=isolated-keychain ;;
          *)
            step_fail C.2 'Claude authentication selection input failed'
            part_c_abort 'Part C Claude authentication selection input failed'
            ;;
        esac
      fi
    else
      printf 'No operator Library/Keychains directory was found for the exact Claude symlink.\n'
      PART_C_CLAUDE_AUTH_MODE=isolated-keychain
    fi

    if [ "$PART_C_CLAUDE_AUTH_MODE" = isolated-keychain ]; then
      printf 'The fallback creates an isolated empty login keychain under the temporary HOME.\n'
      printf 'A fresh Claude token will be minted into the isolated temporary keychain.\n'
      if ask 'Continue with guided Claude sign-in using an isolated empty login keychain instead?'; then
        if ! part_c_create_isolated_keychain; then
          step_fail C.2 'isolated login keychain creation failed'
          part_c_abort 'Part C isolated login keychain creation failed'
        fi
        step_pass C.2 'operator chose guided Claude sign-in with an isolated empty login keychain'
      else
        case "$ASK_RESULT" in
          n)
            if [ "$part_c_keychain_offered" -eq 1 ]; then
              step_fail C.2 'operator declined both Claude authentication paths'
              part_c_abort 'operator declined Claude keychain link and isolated-keychain guided sign-in'
            fi
            step_fail C.2 'operator declined isolated-keychain guided sign-in'
            part_c_abort 'operator declined isolated-keychain guided sign-in'
            ;;
          *)
            step_fail C.2 'isolated-keychain sign-in selection input failed'
            part_c_abort 'Part C isolated-keychain sign-in selection input failed'
            ;;
        esac
      fi
    fi

    export HOME="$PART_C_HOME"
    export PATH="$PART_C_BIN:$PART_C_ORIGINAL_PATH"
    cd "$PART_C_PROJECT"

    step_start C.3 'initialize isolated AMQ and install the release-candidate skill'
    if ! amq coop init --agents a,b,user; then
      step_fail C.3 'AMQ initialization failed'
      part_c_abort 'Part C AMQ initialization failed'
    fi
    if ! "$PART_C_TOP/bin/agentctl" skill install; then
      step_fail C.3 'skill installation failed'
      part_c_abort 'Part C skill installation failed'
    fi
    step_pass C.3 'AMQ initialized and both skill directories installed'

    step_start C.4 'launch and attach the named-socket skill fleet'
    if ! "$PART_C_TOP/bin/agentctl" launch --session skillverify --roles a:claude,b:codex --dir "$PART_C_PROJECT"; then
      step_fail C.4 'skill fleet launch failed'
      part_c_abort 'Part C skill fleet launch failed'
    fi
    PART_C_SESSION_OWNED=1
    cat <<EOF
The verifier will attach this Window 1 to the isolated skill fleet now. It runs:
  $PART_C_TOP/bin/agentctl attach --session skillverify

While attached, use these concrete actions:

EOF
    if [ "$PART_C_CLAUDE_AUTH_MODE" = isolated-keychain ] && [ "$PART_C_AUTH_MODE" = manual ]; then
      cat <<'EOF'
While attached, complete these authentication steps before checking skills:

1. In the Claude Code tab, complete onboarding and sign in until a ready prompt appears. This mints a fresh token into the isolated temporary keychain.
2. In the codex tab, complete sign-in until a ready prompt appears.

EOF
    elif [ "$PART_C_CLAUDE_AUTH_MODE" = isolated-keychain ]; then
      cat <<'EOF'
While attached, complete these authentication steps before checking skills:

The proven Codex auth.json was copied with your consent.
In the Claude Code tab, complete onboarding and sign in until a ready prompt appears. This
mints a fresh token into the isolated temporary keychain. In the codex tab, wait
for the authenticated ready prompt; do not enter credentials.

EOF
    elif [ "$PART_C_AUTH_MODE" = manual ]; then
      cat <<'EOF'
The Claude Keychains symlink and synthesized onboarding configuration were created
with your consent. Claude Code should start without requiring re-authentication.
In the codex tab, complete sign-in until a ready prompt appears.

EOF
    else
      cat <<'EOF'
The proven Codex auth.json, Claude Keychains symlink, and synthesized onboarding
configuration were created with your consent. Neither harness should require
re-authentication; do not enter credentials.

EOF
    fi
    cat <<'EOF'
After both harnesses are ready:

1. In the Claude Code tab, type /skills and press Enter. Then find agentctl in the displayed skill inventory and press esc to close it.
2. In the codex tab, type /skills and press Enter. Then find agentctl in the displayed skill inventory and press esc to close it.

Then ask each harness exactly:

  What does `ambiguous` mean in agentctl status, and which commands refuse on it?

Expected meaning: more than one window has the role's exact name, no window is
selected as real, and role-targeted `clear` and `compact` refuse until an
operator repairs the ambiguity with raw tmux.

After both observations, press esc to detach cleanly; do not use uppercase X.
Wait for the post-detach session-state report before continuing.
EOF
    if ! "$PART_C_TOP/bin/agentctl" attach --session skillverify; then
      step_fail C.4 'skill fleet attach failed'
      part_c_abort 'Part C attach guidance failed'
    fi
    step_pass C.4 'named-socket skill fleet launched and attach guidance completed'

    if [ "$PART_C_CLAUDE_AUTH_MODE" = isolated-keychain ] && [ "$PART_C_AUTH_MODE" = manual ]; then
      auth_expected='both harnesses completed manual sign-in and reached ready prompts.'
      auth_prompt='Did both harnesses complete manual sign-in and reach ready prompts?'
    elif [ "$PART_C_CLAUDE_AUTH_MODE" = isolated-keychain ]; then
      auth_expected='Claude Code minted a fresh token through guided sign-in, and codex reached an authenticated ready prompt from the seeded auth.json.'
      auth_prompt='Did Claude Code mint a fresh token through guided sign-in and did codex authenticate from the seeded auth.json?'
    elif [ "$PART_C_AUTH_MODE" = manual ]; then
      auth_expected='Claude Code started authenticated through the consented Keychains symlink, and codex completed manual sign-in.'
      auth_prompt='Did Claude Code start authenticated through the consented Keychains symlink and did codex complete manual sign-in?'
    else
      auth_expected='both harnesses started without requiring re-authentication.'
      auth_prompt='Did both harnesses start without requiring re-authentication?'
    fi
    if checkpoint C.C1 'harness authentication ready' "$auth_expected" "$auth_prompt"; then
      PART_C_AUTH_ATTESTATION=$ASK_ANSWER
    else
      PART_C_AUTH_ATTESTATION=$ASK_ANSWER
      part_c_abort 'Part C authentication checkpoint failed'
    fi

    if checkpoint C.C2 'harness lists the agentctl skill' 'Claude Code /skills and codex /skills each list agentctl.' 'Do both harness inventories list the agentctl skill?'; then
      PART_C_SKILL_ATTESTATION=$ASK_ANSWER
    else
      PART_C_SKILL_ATTESTATION=$ASK_ANSWER
      part_c_abort 'Part C skill inventory checkpoint failed'
    fi

    meaning_expected='ambiguous means more than one exact-name role window exists, no window is selected as real, and clear and compact refuse until raw tmux repairs it.'
    if checkpoint C.C3 'probe answer matches references/status-states.md' "$meaning_expected" 'Do both answers match references/status-states.md for ambiguous and the refusing clear/compact commands?'; then
      PART_C_MEANING_ATTESTATION=$ASK_ANSWER
    else
      PART_C_MEANING_ATTESTATION=$ASK_ANSWER
      part_c_abort 'Part C status-state checkpoint failed'
    fi

    step_start C.5 'tear down named-socket fleet and temporary skill root'
    if ! part_c_teardown; then
      die 'Part C teardown failed'
    fi
    step_pass C.5 'Part C resources removed and environment restored'
    PART_C_RESULT='PASS — operator confirmed: authentication, skill inventory, and status-meaning checkpoints C.C1-C.C3'
  fi

  if ! part_b_keeper_teardown; then
    die 'Part B keeper teardown failed'
  fi

  {
    printf 'date_utc=%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    printf 'mode=verify-live\n'
    printf 'harness=both\n'
    printf 'probes=all four completed, no surviving throwaway server\n'

    printf 'part_a_result=%s\n' "$PART_A_RESULT"
    printf 'part_b_result=%s\n' "$PART_B_RESULT"
    printf 'part_c_result=%s\n' "$PART_C_RESULT"
    printf 'part_b_precheck_observation=%s\n' "$PART_B_PRECHECK_OBSERVATION"
    printf 'part_b_keeper_session=%s\n' "$PART_B_KEEPER_SESSION"
    printf 'attach_attestation=%s\n' "$ATTACH_ATTESTATION"
    printf 'claude_clear_attestation=%s\n' "$CLAUDE_CLEAR_ATTESTATION"
    printf 'codex_clear_attestation=%s\n' "$CODEX_CLEAR_ATTESTATION"
    printf 'compact_attestation=%s\n' "$COMPACT_ATTESTATION"
    printf 'relaunch_check=%s\n' "$RELAUNCH_CHECK"
    printf 'relaunch_attestation=%s\n' "$RELAUNCH_ATTESTATION"
    printf 'part_b_detach_attestation=%s\n' "$PART_B_DETACH_ATTESTATION"
    printf 'part_c_auth_mode=%s\n' "$PART_C_AUTH_MODE"
    printf 'part_c_claude_auth_mode=%s\n' "$PART_C_CLAUDE_AUTH_MODE"
    printf 'part_c_auth_attestation=%s\n' "$PART_C_AUTH_ATTESTATION"
    printf 'part_c_skill_attestation=%s\n' "$PART_C_SKILL_ATTESTATION"
    printf 'part_c_meaning_attestation=%s\n' "$PART_C_MEANING_ATTESTATION"
    printf 'teardown_status_exit=%s\n' "$TEARDOWN_STATUS_EXIT"
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
marker_count=$(awk -v marker="$marker" '$0 == marker { count++ } END { print count + 0 }' "$NOTES_FILE") || {
  rm -f "$BLOCK_FILE"
  die "could not count exact results-history markers in $NOTES_FILE"
}
if [ "$marker_count" -ne 1 ]; then
  rm -f "$BLOCK_FILE"
  die "expected exactly one line equal to $marker in $NOTES_FILE; found $marker_count"
fi

TMP_NOTES=$(mktemp) || {
  rm -f "$BLOCK_FILE"
  die 'could not create temp file'
}
if ! awk -v marker="$marker" -v block_file="$BLOCK_FILE" '
  { print }
  $0 == marker {
    insertions++
    print ""
    while ((getline block_line < block_file) > 0) {
      print block_line
    }
    close(block_file)
  }
  END { if (insertions != 1) exit 42 }
' "$NOTES_FILE" >"$TMP_NOTES"; then
  rm -f "$BLOCK_FILE" "$TMP_NOTES"
  die 'evidence insertion did not occur exactly once'
fi
mv "$TMP_NOTES" "$NOTES_FILE"
rm -f "$BLOCK_FILE"

echo 'ALL VERIFIED — evidence appended; commit docs/release-verification-notes.md'
