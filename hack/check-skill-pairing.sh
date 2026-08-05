#!/usr/bin/env bash
# Requires command-surface PRs to update the embedded agent skill or carry an
# explicit commit-message override within the PR range.
set -euo pipefail

base_sha="${1:?usage: check-skill-pairing.sh BASE_SHA HEAD_SHA}"
head_sha="${2:?usage: check-skill-pairing.sh BASE_SHA HEAD_SHA}"

surface_changed=0
skill_changed=0
changed_paths="$(git diff --name-only "$base_sha"..."$head_sha")"
while IFS= read -r path; do
  case "$path" in
    cmd/agentctl/*|internal/config/*) surface_changed=1 ;;
    skills/agentctl/*) skill_changed=1 ;;
  esac
done <<<"$changed_paths"

if [ "$surface_changed" -eq 0 ] || [ "$skill_changed" -eq 1 ]; then
  exit 0
fi

commit_messages="$(git log --format=%B "$base_sha".."$head_sha")"
if grep -Fq '[skill-unaffected]' <<<"$commit_messages"; then
  exit 0
fi

echo "command surface changed without skills/agentctl/; update the skill or add [skill-unaffected] to a PR commit message" >&2
exit 1
