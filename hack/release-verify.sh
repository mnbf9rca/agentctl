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

Runs preflight and the release contract probes. By default it then guides a
three-confirmation live smoke through detached launch, explicit role attach,
script-proven relaunch, viewer closure, status, and teardown. Exhaustive fixed
payload, skill, attach-protocol, and status-semantics coverage belongs to the
automated Task 8 path. With --non-interactive each live checkpoint reads y/n
from stdin while retaining all prompts and expected observations. With
--measure it runs hack/verify-injection.sh in measure mode. Both paths finish
with automated cleanup checks and results rendering.

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

checkpoint() {
  local checkpoint_id=$1
  local checkpoint_name=$2
  local expected_output=$3
  local prompt=$4
  printf '\n===== OPERATOR CHECKPOINT %s: %s =====\n' "$checkpoint_id" "$checkpoint_name"
  printf 'Expected observation:\n> %s\n' "$expected_output"
  if ask "$prompt"; then
    printf '===== END OPERATOR CHECKPOINT %s =====\n' "$checkpoint_id"
    printf '[CHECKPOINT PASS %s] operator confirmed: %s\n' "$checkpoint_id" "$checkpoint_name"
    return 0
  fi
  printf '===== END OPERATOR CHECKPOINT %s =====\n' "$checkpoint_id"
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
PART_B_LAUNCH_ATTEMPTED=0
PART_B_SESSION_ABSENT_OBSERVED=0
PART_B_AMQ_MODE='existing'
PART_B_AMQ_CONFIG=''
PART_B_AMQ_ROOT=''
PART_B_AMQ_CONFIG_ID=''
PART_B_AMQ_ROOT_ID=''
PART_B_AMQ_CONFIG_OWNED=0
PART_B_AMQ_ROOT_OWNED=0

part_b_amq_prepare() {
  local config_id
  local init_status=0
  local root_id
  PART_B_AMQ_CONFIG="$PART_B_TOP/.amqrc"
  PART_B_AMQ_ROOT="$EVIDENCE_DIR/part-b-amq"
  if [ -e "$PART_B_AMQ_CONFIG" ] || [ -L "$PART_B_AMQ_CONFIG" ]; then
    PART_B_AMQ_MODE=existing
    return 0
  fi
  if [ -e "$PART_B_AMQ_ROOT" ] || [ -L "$PART_B_AMQ_ROOT" ]; then
    printf 'PART B AMQ INIT FAIL (temporary root already exists: %s)\n' "$PART_B_AMQ_ROOT" >&2
    return 1
  fi
  amq coop init --root "$PART_B_AMQ_ROOT" --agents a,b,user --no-gitignore || init_status=$?
  if [ -f "$PART_B_AMQ_CONFIG" ] && [ ! -L "$PART_B_AMQ_CONFIG" ]; then
    config_id=$(stat -f '%d:%i' "$PART_B_AMQ_CONFIG") || return 1
    PART_B_AMQ_CONFIG_ID=$config_id
    PART_B_AMQ_CONFIG_OWNED=1
  fi
  if [ -d "$PART_B_AMQ_ROOT" ] && [ ! -L "$PART_B_AMQ_ROOT" ]; then
    root_id=$(stat -f '%d:%i' "$PART_B_AMQ_ROOT") || return 1
    PART_B_AMQ_ROOT_ID=$root_id
    PART_B_AMQ_ROOT_OWNED=1
  fi
  if [ "$init_status" -ne 0 ]; then
    printf 'PART B AMQ INIT FAIL (amq coop init exited %s)\n' "$init_status" >&2
    return 1
  fi
  if [ "$PART_B_AMQ_CONFIG_OWNED" -ne 1 ] || [ "$PART_B_AMQ_ROOT_OWNED" -ne 1 ]; then
    printf 'PART B AMQ INIT FAIL (owned config or root has an unexpected file type)\n' >&2
    return 1
  fi
  PART_B_AMQ_MODE=temporary
  printf 'PART B AMQ INIT PASS (temporary config and root are owned by this verifier run)\n'
}

part_b_amq_teardown() {
  local current_id
  local teardown_status=0
  if [ "$PART_B_AMQ_CONFIG_OWNED" -eq 0 ] && [ "$PART_B_AMQ_ROOT_OWNED" -eq 0 ]; then
    return 0
  fi
  if [ "$PART_B_LAUNCH_ATTEMPTED" -eq 1 ] && [ "$PART_B_SESSION_ABSENT_OBSERVED" -ne 1 ]; then
    printf 'PART B AMQ CLEANUP FAIL (temporary config/root retained because fleet absence was not observed)\n' >&2
    return 1
  fi
  if [ "$PART_B_AMQ_CONFIG_OWNED" -eq 1 ]; then
    current_id=$(stat -f '%d:%i' "$PART_B_AMQ_CONFIG" 2>/dev/null) || current_id=''
    if [ "$current_id" != "$PART_B_AMQ_CONFIG_ID" ]; then
      printf 'PART B AMQ CLEANUP FAIL (temporary config identity changed: %s)\n' "$PART_B_AMQ_CONFIG" >&2
      teardown_status=1
    elif rm -f -- "$PART_B_AMQ_CONFIG" && [ ! -e "$PART_B_AMQ_CONFIG" ] && [ ! -L "$PART_B_AMQ_CONFIG" ]; then
      PART_B_AMQ_CONFIG_OWNED=0
      printf 'PART B AMQ CLEANUP PASS (temporary .amqrc removed)\n'
    else
      printf 'PART B AMQ CLEANUP FAIL (remove temporary config %s)\n' "$PART_B_AMQ_CONFIG" >&2
      teardown_status=1
    fi
  fi
  if [ "$PART_B_AMQ_ROOT_OWNED" -eq 1 ]; then
    current_id=$(stat -f '%d:%i' "$PART_B_AMQ_ROOT" 2>/dev/null) || current_id=''
    if [ "$current_id" != "$PART_B_AMQ_ROOT_ID" ]; then
      printf 'PART B AMQ CLEANUP FAIL (temporary root identity changed: %s)\n' "$PART_B_AMQ_ROOT" >&2
      teardown_status=1
    elif rm -rf -- "$PART_B_AMQ_ROOT" && [ ! -e "$PART_B_AMQ_ROOT" ] && [ ! -L "$PART_B_AMQ_ROOT" ]; then
      PART_B_AMQ_ROOT_OWNED=0
      printf 'PART B AMQ CLEANUP PASS (temporary root removed)\n'
    else
      printf 'PART B AMQ CLEANUP FAIL (remove temporary root %s)\n' "$PART_B_AMQ_ROOT" >&2
      teardown_status=1
    fi
  fi
  return "$teardown_status"
}

part_b_retry_kill() {
  local attempt=1
  while [ "$attempt" -le 6 ]; do
    if "$PART_B_TOP/bin/agentctl" kill --session "$PART_B_SESSION"; then
      return 0
    fi
    if [ "$attempt" -lt 6 ]; then
      sleep 1
    fi
    attempt=$((attempt + 1))
  done
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
  if [ "$kill_status" -eq 9 ]; then
    printf 'PART B CLEANUP OBSERVED (%s kill exited 9; retrying within bounded observation window)\n' "$PART_B_SESSION"
    if part_b_retry_kill; then
      printf 'PART B CLEANUP PASS (%s kill retry exited 0)\n' "$PART_B_SESSION"
      PART_B_SESSION_OWNED=0
      return 0
    else
      kill_status=$?
    fi
    printf 'PART B CLEANUP FAIL (%s kill retry exited %s)\n' "$PART_B_SESSION" "$kill_status" >&2
    return 1
  fi
  printf 'PART B CLEANUP FAIL (%s kill exited %s)\n' "$PART_B_SESSION" "$kill_status" >&2
  return 1
}

cleanup_exit_trap() {
  local original_status=$?
  local cleanup_status=0
  local final_status
  trap - EXIT
  if ! part_b_teardown; then
    printf 'release-verify: Part B cleanup failed during exit\n' >&2
    cleanup_status=1
  fi
  if [ "$PART_B_LAUNCH_ATTEMPTED" -eq 1 ] && [ "$PART_B_SESSION_ABSENT_OBSERVED" -ne 1 ]; then
    if session_absent "$PART_B_SESSION" "$ARTIFACT_DIR/exit-teardown.stdout" "$ARTIFACT_DIR/exit-teardown.stderr"; then
      PART_B_SESSION_ABSENT_OBSERVED=1
      printf 'PART B ABSENCE OBSERVATION PASS (agentctl status exit %s proves %s is absent during exit)\n' "$STATUS_EXIT" "$PART_B_SESSION"
    else
      printf 'PART B ABSENCE OBSERVATION FAIL (could not prove %s absent during exit; status exit %s)\n' "$PART_B_SESSION" "$STATUS_EXIT" >&2
      cleanup_status=1
    fi
  fi
  if ! part_b_amq_teardown; then
    printf 'release-verify: Part B AMQ cleanup failed during exit\n' >&2
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
    *)
      return 2
      ;;
  esac
}

assert_roles_running() {
  local session_name=$1
  local output_file=$2
  local error_file=$3
  local role
  local states
  shift 3
  if ! ./bin/agentctl status --session "$session_name" >"$output_file" 2>"$error_file"; then
    cat "$output_file"
    cat "$error_file" >&2
    return 1
  fi
  cat "$output_file"
  for role in "$@"; do
    states=$(awk -v session="$session_name" -v role="$role" '
      $1 == session && $2 == role {
        for (field = 3; field <= NF; field++) {
          if ($field == "running") print $field
        }
      }
    ' "$output_file")
    [ "$states" = running ] || return 1
  done
}

ROLE_SHIM_PID=''
ROLE_CHILD_PID=''
resolve_running_role_processes() {
  local session_name=$1
  local role=$2
  local output_file=$3
  local error_file=$4
  local records
  local record
  if ! ./bin/agentctl status --session "$session_name" >"$output_file" 2>"$error_file"; then
    cat "$output_file"
    cat "$error_file" >&2
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
    part_b_presentation=$(field part_b_presentation "$metadata")
    part_b_session=$(field part_b_session "$metadata")
    live_human_checkpoint_schema=$(field live_human_checkpoint_schema "$metadata")
    case "$live_human_checkpoint_schema" in
      ''|three-observation-v1) ;;
      *) die 'live metadata has invalid live_human_checkpoint_schema' ;;
    esac
    part_b_amq_mode=$(field part_b_amq_mode "$metadata")
    case "$part_b_amq_mode" in
      existing|temporary) ;;
      '')
        [ "$live_human_checkpoint_schema" != three-observation-v1 ] || die 'live metadata is missing part_b_amq_mode'
        ;;
      *) die 'live metadata has invalid part_b_amq_mode' ;;
    esac
    if [ -n "$part_a_result" ]; then
      [ -n "$part_b_detach_attestation" ] || die 'live metadata new schema is missing part_b_detach_attestation'
      printf -- '- Part A: %s\n' "$part_a_result"
      printf -- '- Part B: %s\n' "$(field part_b_result "$metadata")"
      if [ -n "$part_b_session" ]; then
        printf -- '- Part B session: `%s`\n' "$part_b_session"
      fi
      if [ -n "$part_b_amq_mode" ]; then
        case "$part_b_amq_mode" in
          existing) printf -- '- Part B AMQ mode: existing (pre-existing .amqrc; verifier removed no AMQ path)\n' ;;
          temporary) printf -- '- Part B AMQ mode: temporary (verifier-owned .amqrc and root)\n' ;;
        esac
      fi
      if [ "$live_human_checkpoint_schema" != three-observation-v1 ]; then
        printf -- '- Part C: %s\n' "$(field part_c_result "$metadata")"
      fi
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
      if [ "$live_human_checkpoint_schema" = three-observation-v1 ]; then
        printf -- '- Checkpoint B.C1 live Claude role a and Codex role b surfaces: operator confirmed: %s\n' "$(field attach_attestation "$metadata")"
        printf -- '- Checkpoint B.C2 fresh replacement Claude role a surface: %s; operator confirmed: %s\n' \
          "$(field relaunch_check "$metadata")" "$(field relaunch_attestation "$metadata")"
        printf -- '- Checkpoint B.C3 role viewer terminals closed: operator confirmed: %s; script observed both roles still running\n' "$part_b_detach_attestation"
      else
        if [ "$part_b_presentation" = detached ]; then
          printf -- '- Checkpoint B.C1 explicit role attachments: operator confirmed: %s\n' "$(field attach_attestation "$metadata")"
        else
          printf -- '- Checkpoint B.C1 attach narration: operator confirmed: %s\n' "$(field attach_attestation "$metadata")"
        fi
        printf -- '- Checkpoint B.C3 Claude clear outcome: operator confirmed: %s\n' "$(field claude_clear_attestation "$metadata")"
        printf -- '- Checkpoint B.C5 Codex clear outcome: operator confirmed: %s\n' "$(field codex_clear_attestation "$metadata")"
        printf -- '- Checkpoint B.C7 Claude compact outcome: operator confirmed: %s\n' "$(field compact_attestation "$metadata")"
        printf -- '- Checkpoint B.C9 relaunch: %s; fresh claude input with no junk: operator confirmed: %s\n' \
          "$(field relaunch_check "$metadata")" "$(field relaunch_attestation "$metadata")"
        if [ "$part_b_presentation" = detached ]; then
          printf -- '- Checkpoint B.C10 viewer terminals closed: operator confirmed: %s\n' "$part_b_detach_attestation"
        else
          printf -- '- Checkpoint B.C10 detach: operator confirmed: %s\n' "$part_b_detach_attestation"
        fi
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
      fi
    else
      printf -- '- Attach: operator confirmed: %s\n' "$(field attach_attestation "$metadata")"
      printf -- '- Claude clear: operator confirmed: %s\n' "$(field claude_clear_attestation "$metadata")"
      printf -- '- Codex clear: operator confirmed: %s\n' "$(field codex_clear_attestation "$metadata")"
      printf -- '- Compact (claude): operator confirmed: %s\n' "$(field compact_attestation "$metadata")"
      printf -- '- Relaunch: %s; fresh claude input with no junk: operator confirmed: %s\n' \
        "$(field relaunch_check "$metadata")" "$(field relaunch_attestation "$metadata")"
    fi
    if [ "$part_b_presentation" = detached ]; then
      [ "$(field teardown_status_exit "$metadata")" = 3 ] || die 'detached live metadata has invalid teardown_status_exit'
      printf -- '- Teardown status: exit 3 (detached durable fleet absent)\n'
    else
      case "$(field teardown_status_exit "$metadata")" in
        3) printf -- '- Teardown status: exit 3 (session absent; other tmux sessions remained)\n' ;;
        6) printf -- '- Teardown status: exit 6 (session absent; relverify was last and tmux server exited)\n' ;;
        *) die 'live metadata has invalid teardown_status_exit' ;;
      esac
    fi
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
  task8_run "$artifact_dir/shim-version-matrix.log" env AGENTCTL_SHIM_VERSION_OWNED_ROOT=/tmp \
    "$task8_top/hack/release-verify.sh" \
    --shim-version-matrix "$current_binary" "$artifact_dir/shim-version-matrix" || die 'Task 8 shim-version matrix failed'
  task8_phase matrix

  printf '== Task 8: live isolated layout/lifecycle evidence ==\n'
  task8_run "$artifact_dir/integration.log" env AGENTCTL_INTEGRATION_RELEASE_CANDIDATE="$current_binary" \
    AGENTCTL_INTEGRATION_PROJECT_DIR="$task8_project" AGENTCTL_INTEGRATION_OWNED_ROOT="$task8_root/integration" \
    go test -tags integration ./cmd/agentctl -count=1 -v \
    -run 'TestIntegration(ReleaseCandidateSelection|ReleaseCandidateForegroundExtendsRosterAndRefusesDifferentDirectory|ReleaseCandidateStatusReportsUnanchoredDurableRecord|ReleaseCandidateLayoutOperationsPreserveCLIIdentityAndDelivery|ReleaseCandidateCrashRelaunchAndKillUseObservedAbsence|ReleaseCandidateAttachRepaintsAndReadmitsAfterCleanViewerEOF|PublicForegroundRunUsesRuntimeWithoutTmux|PublicCommandsConsultRuntimeAnchorBeforeReportingDivergentStateRootMissing|PublicAttachRefusesAbsentPresentationForDurableFleet|DetachedRoleAttachReleasesOnSignalAndReadmits|ShimPresentationLayoutDoesNotChangeRuntimeIdentityOrDelivery|ShimSIGKILLLeavesApprovedRecordStateAndConcurrentRelaunchStartsOneChild|ShimKillObservesChildExitBeforePresentationAndFleetCleanup)' \
    || die 'Task 8 live integration evidence failed'
  grep -Fq 'candidate-routed crash/relaunch/kill preserved the private-socket sentinel presentation' "$artifact_dir/integration.log" \
    || die 'Task 8 candidate integration did not record private-socket sentinel preservation'
  grep -Fq 'candidate-routed attach repainted output, released the viewer on VEOF, and admitted a replacement viewer' "$artifact_dir/integration.log" \
    || die 'Task 8 candidate integration did not record repaint, VEOF release, and replacement admission'
  task8_run "$artifact_dir/attach-transcript.log" go test ./internal/attach ./internal/shim -count=1 -v \
    -run 'Test(ViewerResizeEmitsObservedWindowSizeAsOneSerializedControlFrame|AttachServerChildExitMapsExactTailUndeliveredFinal|AttachServerChildExitMapsZeroAndNonzeroTailUnconfirmedFinals|ServerRunDetachedServesAttachAndControlBeforeCleanExit)' \
    || die 'Task 8 detached attach transcript evidence failed'
  task8_run "$artifact_dir/default-live-verifier.log" go test ./hack -count=1 -v \
    -run '^TestLiveVerificationDetachedRelaunchUsesRuntimeRecordsAndExplicitRoleAttach$' \
    || die 'Task 8 full-default live verifier fixture failed'
  grep -qF 'full-default verifier used detached runtime records and explicit role attach' "$artifact_dir/default-live-verifier.log" \
    || die 'Task 8 full-default live verifier fixture did not record detached relaunch evidence'
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

for cmd_name in tmux claude codex amq; do
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
probe_count=0
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
  probe_count=$((probe_count + 1))
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
  probe_count=$((probe_count + 1))
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
  evidence_token=${EVIDENCE_DIR##*.}
  evidence_token=$(printf '%s' "$evidence_token" | tr '[:upper:]' '[:lower:]')
  case "$evidence_token" in
    ''|*[!a-z0-9]*) die 'evidence directory did not provide a safe live-session token' ;;
  esac
  LIVE_SESSION="relverify_$evidence_token"
  LIVE_STATUS=0
  ATTACH_ATTESTATION=''
  RELAUNCH_ATTESTATION=''
  PART_B_DETACH_ATTESTATION=''
  RELAUNCH_CHECK=FAIL
  TEARDOWN_CHECK=FAIL
  TEARDOWN_STATUS_EXIT=''
  PRECHECK_STDOUT="$ARTIFACT_DIR/precheck.stdout"
  PRECHECK_STDERR="$ARTIFACT_DIR/precheck.stderr"
  TEARDOWN_STDOUT="$ARTIFACT_DIR/teardown.stdout"
  TEARDOWN_STDERR="$ARTIFACT_DIR/teardown.stderr"

  PART_B_TOP=$TOP
  PART_B_SESSION=$LIVE_SESSION
  trap cleanup_exit_trap EXIT
  if ! part_b_amq_prepare; then
    die 'could not prepare the Part B AMQ coordination root'
  fi

  if session_absent "$LIVE_SESSION" "$PRECHECK_STDOUT" "$PRECHECK_STDERR"; then
    PART_B_SESSION_ABSENT_OBSERVED=1
  else
    absence_status=$?
    if [ "$absence_status" -eq 1 ]; then
      die "session $LIVE_SESSION already exists; refusing to use or kill it"
    fi
    cat "$PRECHECK_STDERR" >&2
    die "could not prove detached session $LIVE_SESSION is absent (status exit $STATUS_EXIT)"
  fi

  step_start B.1 'launch release-candidate fleet'
  echo 'Running:'
  printf '  ./bin/agentctl launch --session %s --roles a:claude,b:codex --efforts b:high\n' "$LIVE_SESSION"
  PART_B_LAUNCH_ATTEMPTED=1
  PART_B_SESSION_ABSENT_OBSERVED=0
  if ! ./bin/agentctl launch --session "$LIVE_SESSION" --roles a:claude,b:codex --efforts b:high; then
    die 'live release verification launch failed'
  fi
  step_pass B.1 'release-candidate fleet launched'

  # This run owns LIVE_SESSION only after launch succeeds. Keep teardown armed
  # across every later command and attestation; explicit teardown disarms it.
  PART_B_SESSION_OWNED=1

  if [ "$LIVE_STATUS" -eq 0 ]; then
    printf '\n===== OPERATOR ACTION B.A1: open the live role viewers =====\n'
    echo 'Keep this script running in the verifier terminal.'
    echo 'In a separate terminal for the Claude role a viewer, run:'
    printf '  ./bin/agentctl attach --session %s a\n' "$LIVE_SESSION"
    echo 'In another terminal for the Codex role b viewer, run:'
    printf '  ./bin/agentctl attach --session %s b\n' "$LIVE_SESSION"
    echo 'Keep both role viewers open and return to the verifier terminal.'
    printf '===== END OPERATOR ACTION B.A1 =====\n'
    attach_expected=$(cat <<'EOF'
The Claude role a viewer shows a ready Claude harness, and the Codex role b
viewer shows a ready Codex harness. The detached roles continue running
independently of those viewers.
EOF
)
    if checkpoint B.C1 'live role surfaces' "$attach_expected" 'Do the Claude role a viewer and Codex role b viewer each show the named ready harness?'; then
      ATTACH_ATTESTATION=$ASK_ANSWER
    else
      ATTACH_ATTESTATION=$ASK_ANSWER
      LIVE_STATUS=1
    fi
  fi

  if [ "$LIVE_STATUS" -eq 0 ]; then
    echo
    echo '== Relaunch verification =='
    if ! resolve_running_role_processes "$LIVE_SESSION" a "$ARTIFACT_DIR/relaunch-before.status" "$ARTIFACT_DIR/relaunch-before.stderr"; then
      echo 'RELAUNCH FAIL (could not resolve role a to one running shim and child PID)'
      LIVE_STATUS=1
    else
      original_shim_pid=$ROLE_SHIM_PID
      original_child_pid=$ROLE_CHILD_PID
      echo 'Running exact-PID shim termination setup:'
      printf '  kill -HUP %s  # role a shim; recorded child %s\n' "$original_shim_pid" "$original_child_pid"
      if ! kill -HUP "$original_shim_pid"; then
        echo "RELAUNCH FAIL (could not signal role a shim PID $original_shim_pid)"
        LIVE_STATUS=1
      else
        step_pass B.2 'exact-PID role a shim termination completed'
      fi
    fi
  fi

  if [ "$LIVE_STATUS" -eq 0 ]; then
    echo 'Running:'
    printf '  kill -0 %s  # wait for recorded role a child absence\n' "$original_child_pid"
    if wait_process_absent "$original_child_pid"; then
      echo 'RELAUNCH PASS (recorded role a child no longer responds to signal 0)'
      step_pass B.3 'recorded role a child absence observed'
    else
      echo "RELAUNCH FAIL (recorded role a child PID $original_child_pid still responds to signal 0)"
      LIVE_STATUS=1
    fi
  fi

  if [ "$LIVE_STATUS" -eq 0 ]; then
    echo 'Running:'
    printf '  ./bin/agentctl relaunch --session %s a\n' "$LIVE_SESSION"
    if ./bin/agentctl relaunch --session "$LIVE_SESSION" a >"$ARTIFACT_DIR/relaunch.output" 2>&1; then
      cat "$ARTIFACT_DIR/relaunch.output"
      echo 'RELAUNCH PASS (role a relaunched through the ESRCH-gated command)'
      step_pass B.4 'ESRCH-gated relaunch command completed'
    else
      cat "$ARTIFACT_DIR/relaunch.output"
      echo 'RELAUNCH FAIL (agentctl relaunch failed)'
      LIVE_STATUS=1
    fi
  fi

  if [ "$LIVE_STATUS" -eq 0 ]; then
    expected_relaunch=$(printf 'agentctl: relaunched role "a" in session "%s"; the shim is ready' "$LIVE_SESSION")
    actual_relaunch=$(cat "$ARTIFACT_DIR/relaunch.output")
    if [ "$actual_relaunch" != "$expected_relaunch" ]; then
      printf 'RELAUNCH FAIL (success output mismatch):\n  got:  %s\n  want: %s\n' "$actual_relaunch" "$expected_relaunch"
      LIVE_STATUS=1
    elif ! resolve_running_role_processes "$LIVE_SESSION" a "$ARTIFACT_DIR/relaunch-after.status" "$ARTIFACT_DIR/relaunch-after.stderr"; then
      echo 'RELAUNCH FAIL (could not resolve replacement role a to one running shim and child PID)'
      LIVE_STATUS=1
    else
      replacement_shim_pid=$ROLE_SHIM_PID
      replacement_child_pid=$ROLE_CHILD_PID
      if [ "$replacement_shim_pid" = "$original_shim_pid" ] || [ "$replacement_child_pid" = "$original_child_pid" ]; then
        printf 'RELAUNCH FAIL (replacement runtime reused an original identity: shim %s->%s child %s->%s)\n' \
          "$original_shim_pid" "$replacement_shim_pid" "$original_child_pid" "$replacement_child_pid"
        LIVE_STATUS=1
      else
        echo 'RELAUNCH PASS (replacement role a runtime identities observed)'
        printf 'RELAUNCH IDENTITIES (shim %s child %s)\n' "$replacement_shim_pid" "$replacement_child_pid"
        step_pass B.5 'replacement role runtime identities differ from the terminated role'
      fi
    fi
  fi

  if [ "$LIVE_STATUS" -eq 0 ]; then
    printf '\n===== OPERATOR ACTION B.A2: attach the replacement Claude role a viewer =====\n'
    printf '%s\n' 'The original Claude role a viewer ended when the script terminated its recorded shim.'
    printf '%s\n' 'In that viewer terminal, attach to replacement role a with:'
    printf '  ./bin/agentctl attach --session %s a\n' "$LIVE_SESSION"
    printf '%s\n' 'Keep the Codex role b viewer open, then return to the verifier terminal.'
    printf '===== END OPERATOR ACTION B.A2 =====\n'
    relaunch_prompt=$(cat <<'EOF'
The script observed the original role a child become absent, agentctl relaunched
role a from the fleet's stored configuration, and the replacement runtime has
different shim and child identities.

Does the Claude role a viewer show the fresh, ready replacement role a harness?
EOF
)
    if checkpoint B.C2 'fresh replacement role surface' 'The Claude role a viewer shows the fresh, ready replacement role a harness.' "$relaunch_prompt"; then
      RELAUNCH_ATTESTATION=$ASK_ANSWER
      RELAUNCH_CHECK='PASS (old child absent; replacement runtime identities observed; Claude role a viewer reattached)'
    else
      RELAUNCH_ATTESTATION=$ASK_ANSWER
      LIVE_STATUS=1
    fi
  fi

  if [ "$LIVE_STATUS" -eq 0 ]; then
    printf '\n===== OPERATOR ACTION B.A3: close both role viewers =====\n'
    printf '%s\n' 'Close the Claude role a viewer and Codex role b viewer by closing its terminal window or tab,'
    printf '%s\n' 'or by otherwise closing its PTY at the terminal boundary.'
    printf '%s\n' 'Do not type Ctrl-C: attach relays every byte, so Ctrl-C reaches the harness and can interrupt it.'
    printf '%s\n' 'Closing a detached viewer does not stop its role. Return to the verifier terminal afterward.'
    printf '===== END OPERATOR ACTION B.A3 =====\n'
    if checkpoint B.C3 'role viewer terminals closed' 'The Claude role a viewer and Codex role b viewer terminal windows or tabs are closed.' 'Are the Claude role a viewer and Codex role b viewer terminals closed?'; then
      PART_B_DETACH_ATTESTATION=$ASK_ANSWER
      echo 'Running:'
      printf '  ./bin/agentctl status --session %s\n' "$LIVE_SESSION"
      if assert_roles_running "$LIVE_SESSION" "$ARTIFACT_DIR/viewer-close.status" "$ARTIFACT_DIR/viewer-close.stderr" a b; then
        echo 'VIEWER CLOSE PASS (roles a and b remain running after their viewers closed)'
        step_pass B.6 'detached roles remain running after viewer close'
      else
        echo 'VIEWER CLOSE FAIL (one or more detached roles are not running)'
        LIVE_STATUS=1
      fi
    else
      PART_B_DETACH_ATTESTATION=$ASK_ANSWER
      LIVE_STATUS=1
    fi
  fi

  echo
  echo '== Automated teardown =='
  echo 'Running:'
  printf '  ./bin/agentctl kill --session %s\n' "$LIVE_SESSION"
  TEARDOWN_STATUS=0
  if ! part_b_teardown; then
    printf 'TEARDOWN FAIL (%s cleanup coordinator kill failed)\n' "$LIVE_SESSION"
    TEARDOWN_STATUS=1
  fi

  if session_absent "$LIVE_SESSION" "$TEARDOWN_STDOUT" "$TEARDOWN_STDERR"; then
    PART_B_SESSION_ABSENT_OBSERVED=1
    TEARDOWN_STATUS_EXIT=$STATUS_EXIT
    printf 'TEARDOWN PASS (agentctl status exit %s proves %s is absent)\n' "$TEARDOWN_STATUS_EXIT" "$LIVE_SESSION"
  else
    absence_status=$?
    if [ "$absence_status" -eq 1 ]; then
      printf 'TEARDOWN FAIL (agentctl status still finds %s)\n' "$LIVE_SESSION"
    else
      printf 'TEARDOWN FAIL (agentctl status exited %s unexpectedly):\n' "$STATUS_EXIT"
      cat "$TEARDOWN_STDERR"
    fi
    TEARDOWN_STATUS=1
  fi

  if [ "$TEARDOWN_STATUS" -eq 0 ]; then
    TEARDOWN_CHECK=PASS
    step_pass B.7 "detached $LIVE_SESSION teardown checks completed"
  else
    LIVE_STATUS=1
  fi
  if ! part_b_amq_teardown; then
    TEARDOWN_CHECK=FAIL
    LIVE_STATUS=1
  fi

  PART_A_RESULT='PASS — automated probes and isolation checks completed'
  PART_B_RESULT='FAIL — Part B did not complete'
  if [ "$LIVE_STATUS" -eq 0 ]; then
    PART_B_RESULT='PASS — operator confirmed live surfaces, fresh replacement, and viewer closure at checkpoints B.C1-B.C3; script observed runtime-record relaunch and both roles running after viewer close'
  elif [ "$FAILED_CHECKPOINT_RESULT" = refused ]; then
    case "$FAILED_CHECKPOINT_ID" in
      B.C*) PART_B_RESULT="FAIL — operator refused checkpoint $FAILED_CHECKPOINT_ID" ;;
    esac
  elif [ "$FAILED_CHECKPOINT_RESULT" = input ]; then
    case "$FAILED_CHECKPOINT_ID" in
      B.C*) PART_B_RESULT="FAIL — checkpoint input failed at $FAILED_CHECKPOINT_ID" ;;
    esac
  fi
  {
    printf 'date_utc=%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    printf 'mode=verify-live\n'
    printf 'live_human_checkpoint_schema=three-observation-v1\n'
    printf 'harness=both\n'
    printf 'probes=%s completed, no surviving throwaway server\n' "$probe_count"

    printf 'part_a_result=%s\n' "$PART_A_RESULT"
    printf 'part_b_result=%s\n' "$PART_B_RESULT"
    printf 'part_b_presentation=detached\n'
    printf 'part_b_session=%s\n' "$LIVE_SESSION"
    printf 'part_b_amq_mode=%s\n' "$PART_B_AMQ_MODE"
    printf 'attach_attestation=%s\n' "$ATTACH_ATTESTATION"
    printf 'relaunch_check=%s\n' "$RELAUNCH_CHECK"
    printf 'relaunch_attestation=%s\n' "$RELAUNCH_ATTESTATION"
    printf 'part_b_detach_attestation=%s\n' "$PART_B_DETACH_ATTESTATION"
    printf 'teardown_status_exit=%s\n' "$TEARDOWN_STATUS_EXIT"
    printf 'teardown_check=%s\n' "$TEARDOWN_CHECK"
  } >"$ARTIFACT_DIR/metadata.txt"

  # Task 8 owns exhaustive fixed-payload, skill, attach-protocol, and status
  # semantics coverage. The default live path intentionally keeps only the
  # three observations that require human eyes.
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
