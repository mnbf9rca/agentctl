#!/usr/bin/env bash
# Fails when the skill's metadata.version does not equal the release version.
set -euo pipefail

release_version="${1:?usage: check-skill-version.sh RELEASE_VERSION [SKILL_MD]}"
skill_md="${2:-skills/agentctl/SKILL.md}"
skill_version="$(sed -n 's/^[[:space:]]*version:[[:space:]]*"\{0,1\}\([^"]*\)"\{0,1\}$/\1/p' "$skill_md" | head -n 1)"
if [ -z "$skill_version" ]; then
  echo "no metadata.version in $skill_md" >&2
  exit 1
fi
if [ "$skill_version" != "$release_version" ]; then
  echo "skill documents $skill_version; releasing $release_version — update the skill with the surface it ships" >&2
  exit 1
fi
