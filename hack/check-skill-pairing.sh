#!/usr/bin/env bash
# Requires command-surface PRs to update the embedded agent skill or carry an
# explicit commit-message override within the PR range.
set -euo pipefail

base_sha="${1:?usage: check-skill-pairing.sh BASE_SHA HEAD_SHA}"
head_sha="${2:?usage: check-skill-pairing.sh BASE_SHA HEAD_SHA}"

surface_changed=0
skill_changed=0
surface_status=0
git diff --quiet "$base_sha"..."$head_sha" -- cmd/agentctl internal/config || surface_status=$?
case "$surface_status" in
  0) ;;
  1) surface_changed=1 ;;
  *) exit "$surface_status" ;;
esac

skill_status=0
git diff --quiet "$base_sha"..."$head_sha" -- skills/agentctl || skill_status=$?
case "$skill_status" in
  0) ;;
  1) skill_changed=1 ;;
  *) exit "$skill_status" ;;
esac

if [ "$surface_changed" -eq 0 ] || [ "$skill_changed" -eq 1 ]; then
  exit 0
fi

commit_messages="$(git log --format=%B "$base_sha".."$head_sha")"
if grep -Fq '[skill-unaffected]' <<<"$commit_messages"; then
  exit 0
fi

echo "command surface changed without skills/agentctl/; update the skill or add [skill-unaffected] to a PR commit message" >&2
exit 1
