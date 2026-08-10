#!/usr/bin/env bash
# Enforces the repository's pinned PR and daily govulncheck workflow contract.
set -euo pipefail

if [[ $# -gt 1 ]]; then
  echo "usage: check-govulncheck-workflows.sh [WORKFLOW_DIR]" >&2
  exit 2
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
workflow_dir="${1:-$script_dir/../.github/workflows}"
ci_workflow="$workflow_dir/ci.yml"
scheduled_workflow="$workflow_dir/vulnerability.yml"
pinned_command='go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...'

status=0
for workflow in "$ci_workflow" "$scheduled_workflow"; do
  if [[ ! -f "$workflow" ]]; then
    echo "$(basename "$workflow"): workflow file not found" >&2
    status=1
  fi
done
if [[ $status -ne 0 ]]; then
  exit "$status"
fi

if grep -Fq 'govulncheck@latest' "$ci_workflow" "$scheduled_workflow"; then
  echo "govulncheck@latest is not permitted; pin v1.6.0" >&2
  status=1
fi

count_pinned_invocations() {
  local required_job="${2:-}"
  awk -v expected="$pinned_command" -v required_job="$required_job" '
    /^jobs:[[:space:]]*(#.*)?$/ {
      in_jobs = 1
      next
    }
    in_jobs && /^[^[:space:]#]/ {
      in_jobs = 0
      job_name = ""
    }
    in_jobs && /^  [A-Za-z_][A-Za-z0-9_-]*:[[:space:]]*(#.*)?$/ {
      job_name = substr($0, 3)
      sub(/:.*/, "", job_name)
      next
    }
    {
      line = $0
      sub(/^[[:space:]]*run:[[:space:]]*/, "", line)
      if (line == expected && (required_job == "" || job_name == required_job)) count++
    }
    END { print count + 0 }
  ' "$1"
}

if [[ "$(count_pinned_invocations "$ci_workflow")" -ne 1 ]]; then
  echo "ci.yml must contain exactly one pinned govulncheck invocation" >&2
  status=1
fi
if [[ "$(count_pinned_invocations "$ci_workflow" test)" -ne 1 ]]; then
  echo "ci.yml test job must contain exactly one pinned govulncheck invocation" >&2
  status=1
fi

if ! grep -Fqx '  schedule:' "$scheduled_workflow" ||
  ! grep -Fqx '    - cron: "17 8 * * *"' "$scheduled_workflow"; then
  echo "vulnerability.yml must declare the daily schedule" >&2
  status=1
fi

if [[ "$(count_pinned_invocations "$scheduled_workflow")" -ne 1 ]]; then
  echo "vulnerability.yml must contain exactly one pinned govulncheck invocation" >&2
  status=1
fi

exit "$status"
