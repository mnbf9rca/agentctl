#!/usr/bin/env bash
# release-notes.sh injects and verifies the versioned release obligations
# before a draft GitHub release may be made public.
set -euo pipefail

usage() {
  printf '%s\n' 'usage: release-notes.sh inject|verify VERSION RELEASE_JSON' >&2
}

die() {
  printf 'release-notes: %s\n' "$*" >&2
  exit 1
}

if [[ $# -ne 3 ]]; then
  usage
  exit 2
fi

mode=$1
version=$2
release_json=$3
case "$mode" in
  inject|verify) ;;
  *) usage; exit 2 ;;
esac
if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  die "release version must be a bare semver: $version"
fi
[[ -r "$release_json" ]] || die "cannot read GitHub release JSON: $release_json"

root=$(git rev-parse --show-toplevel 2>/dev/null) || die 'not inside a git repository'
source_file="$root/docs/releases/$version.md"
[[ -r "$source_file" ]] || die "cannot read release-note source: $source_file"

if ! jq -e 'type == "object" and (.body | type == "string") and (.isDraft | type == "boolean") and (.tagName | type == "string")' \
  "$release_json" >/dev/null 2>&1; then
  die 'invalid GitHub release JSON; require string body/tagName and boolean isDraft'
fi

tag_name=$(jq -er '.tagName' "$release_json") || die 'invalid GitHub release JSON tagName'
if [[ "$tag_name" != "v$version" ]]; then
  die "GitHub release tagName $tag_name does not match v$version"
fi
is_draft=$(jq -r '.isDraft' "$release_json") || die 'invalid GitHub release JSON isDraft'
if [[ "$is_draft" != true ]]; then
  die "GitHub release isDraft=false for v$version"
fi

temporary_root=$(mktemp -d "${TMPDIR:-/tmp}/agentctl-release-notes.XXXXXX") || die 'could not create temporary directory'
cleanup() {
  rm -rf -- "$temporary_root"
}
trap cleanup EXIT HUP INT TERM
body_file="$temporary_root/body.md"
block_file="$temporary_root/block.md"
jq -j '.body' "$release_json" >"$body_file" || die 'could not extract GitHub release body'

start_marker="<!-- agentctl-release-obligations:$version -->"
end_marker="<!-- /agentctl-release-obligations:$version -->"
count_line() {
  local marker=$1
  grep -Fxc "$marker" "$body_file" || true
}
start_count=$(count_line "$start_marker")
end_count=$(count_line "$end_marker")

if [[ "$start_count" == 1 && "$end_count" == 1 ]]; then
  awk -v start="$start_marker" -v end="$end_marker" '
    $0 == start { inside = 1 }
    inside { print }
    $0 == end { exit }
  ' "$body_file" >"$block_file"
  if ! cmp -s "$source_file" "$block_file"; then
    die "release obligation block for v$version was altered"
  fi
elif [[ "$start_count" == 0 && "$end_count" == 0 ]]; then
  if [[ "$mode" == verify ]]; then
    die "missing obligation block for v$version"
  fi
  if [[ -s "$body_file" ]]; then
    cat "$body_file"
    printf '\n\n'
  fi
  cat "$source_file"
  exit 0
else
  die "release obligation block for v$version must appear exactly once"
fi

if [[ "$mode" == inject ]]; then
  cat "$body_file"
else
  printf 'release notes verified for v%s\n' "$version"
fi
