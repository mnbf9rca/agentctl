#!/usr/bin/env bash
# Fails when the skill's metadata.version does not equal the release version.
set -euo pipefail

release_version="${1:?usage: check-skill-version.sh RELEASE_VERSION [SKILL_MD]}"
skill_md="${2:-skills/agentctl/SKILL.md}"
skill_version="$(awk '
  /^---[[:space:]]*$/ {
    if (in_frontmatter) exit
    in_frontmatter = 1
    next
  }
  !in_frontmatter { next }
  /^[^[:space:]]/ { in_metadata = 0 }
  /^[[:space:]]*metadata:[[:space:]]*$/ { in_metadata = 1; next }
  in_metadata && /^[[:space:]]+version:[[:space:]]*/ {
    value = $0
    sub(/^[[:space:]]+version:[[:space:]]*/, "", value)
    sub(/[[:space:]]*$/, "", value)
    if (value ~ /^".*"$/) {
      sub(/^"/, "", value)
      sub(/"$/, "", value)
    }
    print value
    exit
  }
' "$skill_md")"
if [ -z "$skill_version" ]; then
  echo "no metadata.version in $skill_md" >&2
  exit 1
fi
if [ "$skill_version" != "$release_version" ]; then
  echo "skill documents $skill_version; releasing $release_version — update the skill with the surface it ships" >&2
  exit 1
fi
