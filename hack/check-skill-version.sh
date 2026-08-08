#!/usr/bin/env bash
# Fails when the skill's metadata.version does not equal the release version.
set -euo pipefail

release_version="${1:?usage: check-skill-version.sh RELEASE_VERSION [SKILL_MD]}"
skill_md="${2:-skills/agentctl/SKILL.md}"
if ! skill_version="$(awk '
  function trim(value) {
    sub(/^[[:space:]]+/, "", value)
    sub(/[[:space:]]+$/, "", value)
    return value
  }
  function fail(message) {
    print message " in " FILENAME > "/dev/stderr"
    parse_error = 1
    exit
  }
  NR == 1 {
    if ($0 != "---") fail("skill frontmatter does not start on line 1")
    next
  }
  $0 == "---" {
    closed = 1
    exit
  }
  trim($0) == "" { next }
  /^[[:space:]]/ {
    if (!in_metadata || $0 !~ /^  [^[:space:]]/) {
      fail("frontmatter line " NR " has unsupported nesting")
    }
    line = substr($0, 3)
    colon = index(line, ":")
    if (colon <= 1) fail("frontmatter line " NR " is not a mapping")
    separator = substr(line, colon + 1, 1)
    if (separator != "" && separator !~ /[[:space:]]/) {
      fail("frontmatter line " NR " is not a mapping")
    }
    key = substr(line, 1, colon - 1)
    if (key !~ /^[A-Za-z0-9_-]+$/) {
      fail("frontmatter line " NR " has unsupported key syntax")
    }
    if (key != "version") next
    if (version_seen) fail("multiple metadata.version")
    version_seen = 1
    value = trim(substr(line, colon + 1))
    if (value == "") fail("metadata.version is empty")
    if (substr(value, 1, 1) == "\"") {
      if (length(value) < 2 || substr(value, length(value), 1) != "\"") {
        fail("metadata.version has an unmatched quote")
      }
      value = substr(value, 2, length(value) - 2)
    } else if (index(value, "\"") > 0) {
      fail("metadata.version has an unmatched quote")
    }
    if (value !~ /^[0-9]+(\.[0-9]+)+$/) {
      fail("metadata.version is not numeric and dotted")
    }
    version_value = value
    next
  }
  {
    colon = index($0, ":")
    if (colon <= 1) fail("frontmatter line " NR " is not a mapping")
    separator = substr($0, colon + 1, 1)
    if (separator != "" && separator !~ /[[:space:]]/) {
      fail("frontmatter line " NR " is not a mapping")
    }
    key = substr($0, 1, colon - 1)
    if (key !~ /^[A-Za-z0-9_-]+$/) {
      fail("frontmatter line " NR " has unsupported key syntax")
    }
    value = trim(substr($0, colon + 1))
    in_metadata = 0
    if (key != "metadata") next
    if (metadata_seen) fail("multiple metadata mappings")
    metadata_seen = 1
    if (value != "") fail("frontmatter metadata must be a mapping")
    in_metadata = 1
  }
  END {
    if (parse_error) exit 1
    if (!closed) {
      print "skill frontmatter is not closed in " FILENAME > "/dev/stderr"
      exit 1
    }
    if (!metadata_seen) exit
    if (version_seen) print version_value
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
