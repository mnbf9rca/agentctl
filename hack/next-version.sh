#!/usr/bin/env bash
# Prints the next release version (no v prefix): VERSION file if it is ahead
# of the latest v* tag, otherwise latest tag + 1 patch. See spec §5 step 1.
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
file_version="$(tr -d '[:space:]' < "$root/VERSION")"
if ! [[ "$file_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "next-version: VERSION file is not X.Y.Z: '$file_version'" >&2
  exit 1
fi

latest="$(git tag -l 'v[0-9]*.[0-9]*.[0-9]*' | sed 's/^v//' | sort -V | tail -n1)"
if [[ -z "$latest" ]]; then
  echo "$file_version"
  exit 0
fi

highest="$(printf '%s\n%s\n' "$latest" "$file_version" | sort -V | tail -n1)"
if [[ "$highest" == "$file_version" && "$file_version" != "$latest" ]]; then
  echo "$file_version"
else
  IFS=. read -r major minor patch <<<"$latest"
  echo "${major}.${minor}.$((patch + 1))"
fi
