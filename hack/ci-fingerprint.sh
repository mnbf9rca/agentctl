#!/usr/bin/env bash
set -euo pipefail

print_command() {
  local label="$1"
  local executable="$2"
  shift 2

  if ! command -v "$executable" >/dev/null 2>&1; then
    printf '%s: not installed\n' "$label"
    return
  fi

  local output
  if output="$("$@" 2>&1)"; then
    printf '%s: %s\n' "$label" "$output"
  else
    printf '%s: probe failed\n' "$label"
  fi
}

print_first_line() {
  local label="$1"
  local executable="$2"
  shift 2

  if ! command -v "$executable" >/dev/null 2>&1; then
    printf '%s: not installed\n' "$label"
    return
  fi

  local output
  if output="$("$@" 2>&1)"; then
    printf '%s: %s\n' "$label" "${output%%$'\n'*}"
  else
    printf '%s: probe failed\n' "$label"
  fi
}

fingerprint() {
  local kernel_name=""
  if command -v uname >/dev/null 2>&1; then
    if ! kernel_name="$(uname -s 2>/dev/null)"; then
      kernel_name=""
    fi
    print_command "uname" uname uname -a
  else
    printf '%s\n' "uname: not installed"
  fi

  if [[ "$kernel_name" == "Darwin" ]]; then
    if command -v sw_vers >/dev/null 2>&1; then
      local sw_vers_output
      if sw_vers_output="$(sw_vers 2>&1)"; then
        printf 'sw_vers: %s\n' "${sw_vers_output//$'\n'/; }"
      else
        printf '%s\n' "sw_vers: probe failed"
      fi
    else
      printf '%s\n' "sw_vers: not installed"
    fi
  fi

  if [[ -n "${ImageOS:-}" || -n "${ImageVersion:-}" ]]; then
    printf 'runner image: %s/%s\n' "${ImageOS:-not set}" "${ImageVersion:-not set}"
  fi

  print_command "go" go go version
  print_command "tmux" tmux tmux -V

  if command -v shellcheck >/dev/null 2>&1; then
    local shellcheck_output
    if shellcheck_output="$(shellcheck --version 2>&1)"; then
      local shellcheck_version=""
      while IFS= read -r line; do
        if [[ "$line" == version:* ]]; then
          shellcheck_version="$line"
          break
        fi
      done <<< "$shellcheck_output"
      printf 'shellcheck: %s\n' "${shellcheck_version:-version unavailable}"
    else
      printf '%s\n' "shellcheck: probe failed"
    fi
  else
    printf '%s\n' "shellcheck: not installed"
  fi

  print_command "golangci-lint" golangci-lint golangci-lint version
  if command -v goreleaser >/dev/null 2>&1; then
    local goreleaser_version
    if goreleaser_version="$(goreleaser --version 2>/dev/null | awk '/^GitVersion:/ {print $2; exit}')"; then
      printf 'goreleaser: %s\n' "${goreleaser_version:-unknown}"
    else
      printf '%s\n' "goreleaser: probe failed"
    fi
  else
    printf '%s\n' "goreleaser: not installed"
  fi
  print_first_line "brew" brew brew --version
}

fingerprint_output="$(fingerprint)"
printf '%s\n' "$fingerprint_output"

if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  {
    printf '%s\n\n' "## Environment fingerprint"
    printf '%s\n' '```text'
    printf '%s\n' "$fingerprint_output"
    printf '%s\n' '```'
  } >> "$GITHUB_STEP_SUMMARY"
fi
