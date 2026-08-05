#!/usr/bin/env bash
# Prints the next release version (no v prefix): VERSION file if it is ahead
# of the latest v* tag, otherwise latest tag + 1 patch. See spec §5 step 1.
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
version_file="$root/VERSION"
if [[ ! -f "$version_file" ]]; then
  echo "next-version: no VERSION file at $version_file" >&2
  exit 1
fi
file_version="$(tr -d '[:space:]' < "$version_file")"
if ! [[ "$file_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "next-version: VERSION file is not X.Y.Z: '$file_version'" >&2
  exit 1
fi

## The tag glob below is a filesystem-style pattern, not an anchored regex, so
## it also admits pre-release-suffixed tags such as v0.1.0-rc1 (see git-tag(1)
## --list). Those would leave a non-numeric trailing component (e.g. "0-rc1")
## in $latest, and with `set -u` the patch arithmetic below crashes on the
## unbound "rc1" reference. The grep -E filter keeps only bare X.Y.Z tags.
latest="$(git tag -l 'v[0-9]*.[0-9]*.[0-9]*' | sed 's/^v//' | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$' || true)"
latest="$(printf '%s\n' "$latest" | sort -V | tail -n1)"
if [[ -z "$latest" ]]; then
  echo "$file_version"
  exit 0
fi

highest="$(printf '%s\n%s\n' "$latest" "$file_version" | sort -V | tail -n1)"
if [[ "$highest" == "$file_version" && "$file_version" != "$latest" ]]; then
  echo "$file_version"
else
  IFS=. read -r major minor patch <<<"$latest"
  # Force base 10: a patch component with a leading zero (e.g. "08") would
  # otherwise be parsed as an invalid octal literal by bash arithmetic.
  echo "${major}.${minor}.$((10#$patch + 1))"
fi
