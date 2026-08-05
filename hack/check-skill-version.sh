#!/usr/bin/env bash
# Fails when the skill's metadata.version does not equal the release version.
set -euo pipefail

release_version="${1:?usage: check-skill-version.sh RELEASE_VERSION [SKILL_MD]}"
skill_md="${2:-skills/agentctl/SKILL.md}"
if ! skill_version="$(awk '
  NR == 1 {
    if ($0 !~ /^---[[:space:]]*$/) {
      print "skill frontmatter does not start on line 1 in " FILENAME > "/dev/stderr"
      parse_error = 1
      exit
    }
    in_frontmatter = 1
    next
  }
  in_frontmatter && /^---[[:space:]]*$/ {
    closed = 1
    exit
  }
  /^metadata:[[:space:]]*$/ {
    metadata_count++
    in_metadata = 1
    child_indent = 0
    next
  }
  /^[^[:space:]]/ {
    in_metadata = 0
    next
  }
  in_metadata {
    if ($0 ~ /^[[:space:]]*$/ || $0 ~ /^[[:space:]]*#/) next
    match($0, /[^ \t]/)
    indent = RSTART - 1
    if (child_indent == 0) child_indent = indent
    if (indent != child_indent) next
    if ($0 !~ /^[[:space:]]+version:[[:space:]]*/) next

    version_count++
    if (version_count == 1) {
      value = $0
      sub(/^[[:space:]]+version:[[:space:]]*/, "", value)
      sub(/[[:space:]]*$/, "", value)
      if (value ~ /^".*"$/) {
        sub(/^"/, "", value)
        sub(/"$/, "", value)
      }
      version_value = value
    }
  }
  END {
    if (parse_error) exit 1
    if (!closed) {
      print "skill frontmatter is not closed in " FILENAME > "/dev/stderr"
      exit 1
    }
    if (metadata_count > 1) {
      print "multiple metadata mappings in " FILENAME > "/dev/stderr"
      exit 1
    }
    if (version_count > 1) {
      print "multiple metadata.version in " FILENAME > "/dev/stderr"
      exit 1
    }
    if (version_count == 1) print version_value
  }
' "$skill_md")"; then
  exit 1
fi
if [ -z "$skill_version" ]; then
  echo "no metadata.version in $skill_md" >&2
  exit 1
fi
if [ "$skill_version" != "$release_version" ]; then
  echo "skill documents $skill_version; releasing $release_version — update the skill with the surface it ships" >&2
  exit 1
fi
