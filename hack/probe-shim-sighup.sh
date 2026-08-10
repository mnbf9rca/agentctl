#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: hack/probe-shim-sighup.sh --harness claude|codex --output ABSOLUTE_PATH" >&2
}

fail() {
  echo "probe-shim-sighup: $*" >&2
  exit 1
}

harness=""
output=""
while (($# > 0)); do
  case "$1" in
    --harness)
      [[ -z "$harness" && $# -ge 2 ]] || fail "--harness must be supplied exactly once"
      harness=$2
      shift 2
      ;;
    --output)
      [[ -z "$output" && $# -ge 2 ]] || fail "--output must be supplied exactly once"
      output=$2
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage
      fail "unknown argument: $1"
      ;;
  esac
done

case "$harness" in
  claude|codex) ;;
  *) fail "harness must be claude or codex" ;;
esac
[[ -n "$output" ]] || fail "--output is required"
[[ "$output" = /* ]] || fail "output path must be absolute"
[[ ! -e "$output" ]] || fail "output already exists: $output"
[[ -d "$(dirname "$output")" ]] || fail "output parent is not a directory: $(dirname "$output")"

script_bin=${AGENTCTL_PROBE_SCRIPT_BIN:-/usr/bin/script}
ps_bin=${AGENTCTL_PROBE_PS_BIN:-/bin/ps}
[[ -x "$script_bin" ]] || fail "script binary is not executable: $script_bin"
[[ -x "$ps_bin" ]] || fail "ps binary is not executable: $ps_bin"
harness_bin=$(command -v "$harness") || fail "harness binary not found on PATH: $harness"
[[ -x "$harness_bin" ]] || fail "harness binary is not executable: $harness_bin"

fixture=$(mktemp -d "${TMPDIR:-/tmp}/agentctl-shim-sighup.XXXXXX")
chmod 0700 "$fixture"
probe_home="$fixture/home"
mkdir -m 0700 "$probe_home"
shim_log="$fixture/shim.log"
shim_pid=""
child_pid=""

process_exists() {
  kill -0 "$1" 2>/dev/null
}

cleanup() {
  if [[ -n "$child_pid" ]] && process_exists "$child_pid"; then
    kill -TERM "$child_pid" 2>/dev/null || true
    for _ in {1..20}; do
      process_exists "$child_pid" || break
      sleep 0.05
    done
    if process_exists "$child_pid"; then
      kill -KILL "$child_pid" 2>/dev/null || true
    fi
  fi
  if [[ -n "$shim_pid" ]] && process_exists "$shim_pid"; then
    kill -TERM "$shim_pid" 2>/dev/null || true
    wait "$shim_pid" 2>/dev/null || true
  fi
  rm -rf "$fixture"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

version_output=$(HOME="$probe_home" "$harness_bin" --version 2>&1) || fail "could not read $harness version"
case "$harness" in
  claude)
    harness_version=$(printf '%s\n' "$version_output" | awk '/\(Claude Code\)$/ { print }')
    ;;
  codex)
    harness_version=$(printf '%s\n' "$version_output" | awk '/^codex-cli [0-9]/ { print }')
    ;;
esac
[[ -n "$harness_version" && "$harness_version" != *$'\n'* && "$harness_version" != *$'\r'* ]] || fail "could not identify exactly one $harness version line"

"$script_bin" -q /dev/null /usr/bin/env -i \
  HOME="$probe_home" PATH="$PATH" TERM=xterm-256color \
  "$harness_bin" >"$shim_log" 2>&1 &
shim_pid=$!

topology_line=""
child_ppid=""
child_tty=""
for _ in {1..100}; do
  if ! process_exists "$shim_pid"; then
    fail "owned shim fixture exited before topology was observed; log: $(tr '\n' ' ' <"$shim_log")"
  fi
  topology_line=$(
    "$ps_bin" -axo pid=,ppid=,tty=,comm= 2>/dev/null |
      awk -v parent="$shim_pid" '$2 == parent { print; exit }'
  ) || true
  if [[ -n "$topology_line" ]]; then
    read -r child_pid child_ppid child_tty _ <<<"$topology_line"
    if [[ "$child_pid" =~ ^[1-9][0-9]*$ && "$child_ppid" == "$shim_pid" && -n "$child_tty" && "$child_tty" != "??" && "$child_tty" != "?" ]]; then
      break
    fi
    topology_line=""
  fi
  sleep 0.05
done
[[ -n "$topology_line" ]] || fail "could not observe a direct child of owned shim $shim_pid"

[[ "$child_pid" =~ ^[1-9][0-9]*$ ]] || fail "observed invalid child pid: $child_pid"
[[ "$child_ppid" == "$shim_pid" ]] || fail "child $child_pid parent was $child_ppid, expected owned shim $shim_pid"
[[ -n "$child_tty" && "$child_tty" != "??" && "$child_tty" != "?" ]] || fail "child $child_pid had no nested PTY"

kill -HUP "$shim_pid" || fail "could not signal owned shim $shim_pid"
for _ in {1..100}; do
  process_exists "$shim_pid" || break
  sleep 0.05
done
if process_exists "$shim_pid"; then
  fail "owned shim $shim_pid did not terminate after SIGHUP"
fi
wait "$shim_pid" 2>/dev/null || true

child_outcome=survived
for _ in {1..100}; do
  if ! process_exists "$child_pid"; then
    child_outcome=terminated
    break
  fi
  sleep 0.05
done

if ! (set -o noclobber; printf '%s\n' \
  "harness=$harness" \
  "harness_version=$harness_version" \
  "topology=shim-parent-of-harness-child-on-pty" \
  "shim_pid=$shim_pid" \
  "child_pid=$child_pid" \
  "child_ppid_matches=true" \
  "child_tty=$child_tty" \
  "signal_target=owned-shim-only" \
  "signal=SIGHUP" \
  "shim_terminated=true" \
  "child_outcome=$child_outcome" \
  "default_tmux_targeted=false" >"$output"); then
  fail "output already exists: $output"
fi
echo "probe-shim-sighup: recorded $harness result in $output"
