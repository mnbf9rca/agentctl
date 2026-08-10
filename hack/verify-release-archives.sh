#!/usr/bin/env bash
set -euo pipefail

if (($# == 0)); then
  printf '%s\n' 'usage: verify-release-archives.sh ARCHIVE...' >&2
  exit 2
fi

required_files=(
  LICENSE
  LICENSES/README.md
  LICENSES/github.com/santhosh-tekuri/jsonschema/v6/LICENSE
  LICENSES/golang.org/x/sys/LICENSE
  LICENSES/golang.org/x/text/LICENSE
  LICENSES/golang.org/x/text/PATENTS
)

for archive in "$@"; do
  if [[ ! -f "$archive" ]]; then
    printf 'archive %s: file does not exist\n' "$archive" >&2
    exit 1
  fi
  if ! contents="$(tar -tzf "$archive")"; then
    printf 'archive %s: cannot list tar contents\n' "$archive" >&2
    exit 1
  fi
  for required in "${required_files[@]}"; do
    if ! grep -Fqx "$required" <<<"$contents"; then
      printf 'archive %s: missing required file %s\n' "$archive" "$required" >&2
      exit 1
    fi
  done
  printf 'archive %s: required license materials present\n' "$archive"
done
