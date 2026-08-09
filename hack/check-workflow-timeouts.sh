#!/usr/bin/env bash
# Fails when a GitHub Actions workflow job omits a job-level timeout-minutes.
set -euo pipefail

if [[ $# -gt 1 ]]; then
  echo "usage: check-workflow-timeouts.sh [WORKFLOW_DIR]" >&2
  exit 2
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
workflow_dir="${1:-$script_dir/../.github/workflows}"

shopt -s nullglob
workflow_files=("$workflow_dir"/*.yml "$workflow_dir"/*.yaml)
if [[ ${#workflow_files[@]} -eq 0 ]]; then
  echo "no workflow files found in $workflow_dir" >&2
  exit 1
fi

status=0
for workflow_file in "${workflow_files[@]}"; do
  if ! awk '
    function finish_job() {
      if (job_name == "") return
      job_count++
      if (!has_timeout) {
        print FILENAME ":" job_name ": missing job-level timeout-minutes" > "/dev/stderr"
        failed = 1
      }
      job_name = ""
      has_timeout = 0
    }

    /^jobs:[[:space:]]*(#.*)?$/ {
      jobs_seen = 1
      in_jobs = 1
      next
    }

    in_jobs && /^[^[:space:]#]/ {
      finish_job()
      in_jobs = 0
      next
    }

    in_jobs && /^  [A-Za-z_][A-Za-z0-9_-]*:[[:space:]]*(#.*)?$/ {
      finish_job()
      job_name = substr($0, 3)
      sub(/:.*/, "", job_name)
      next
    }

    in_jobs && job_name != "" && /^    timeout-minutes:[[:space:]]*/ {
      has_timeout = 1
      next
    }

    END {
      finish_job()
      if (!jobs_seen) {
        print FILENAME ": missing top-level jobs mapping" > "/dev/stderr"
        failed = 1
      } else if (job_count == 0) {
        print FILENAME ": no workflow jobs discovered" > "/dev/stderr"
        failed = 1
      }
      exit failed
    }
  ' "$workflow_file"; then
    status=1
  fi
done

exit "$status"
