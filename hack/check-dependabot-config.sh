#!/usr/bin/env bash
# Enforces the repository's low-noise Dependabot version-update policy.
set -euo pipefail

if [[ $# -gt 1 ]]; then
  echo "usage: check-dependabot-config.sh [DEPENDABOT_CONFIG]" >&2
  exit 2
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
config_path="${1:-$script_dir/../.github/dependabot.yml}"

if [[ ! -f "$config_path" ]]; then
  echo "dependabot.yml: config file not found" >&2
  exit 1
fi

ruby -ryaml -e '
  begin
    config = YAML.safe_load_file(ARGV.fetch(0), aliases: false)
  rescue Psych::SyntaxError => error
    warn "dependabot.yml: invalid YAML: #{error.problem}"
    exit 1
  end

  unless config.is_a?(Hash) && config["version"] == 2 && config["updates"].is_a?(Array)
    warn "dependabot.yml: version 2 with an updates list is required"
    exit 1
  end

  status = 0
  %w[gomod github-actions].each do |ecosystem|
    entries = config["updates"].select do |entry|
      entry.is_a?(Hash) &&
        entry["package-ecosystem"] == ecosystem &&
        entry["directory"] == "/"
    end
    if entries.length != 1
      warn "#{ecosystem}: exactly one root update entry is required"
      status = 1
      next
    end

    entry = entries.first
    schedule = entry["schedule"]
    unless schedule.is_a?(Hash) && schedule["interval"] == "monthly"
      warn "#{ecosystem}: schedule interval must be monthly"
      status = 1
    end
    unless entry["open-pull-requests-limit"] == 1
      warn "#{ecosystem}: open-pull-requests-limit must be 1"
      status = 1
    end

    groups = entry["groups"]
    unless groups.is_a?(Hash) && groups.length == 1
      warn "#{ecosystem}: exactly one dependency group is required"
      status = 1
      next
    end

    group = groups.values.first
    unless group.is_a?(Hash) &&
        group["patterns"] == ["*"] &&
        !group.key?("exclude-patterns") &&
        !group.key?("update-types")
      warn "#{ecosystem}: dependency group must include every version update"
      status = 1
    end
    applies_to = group.is_a?(Hash) ? group["applies-to"] : nil
    unless applies_to.nil? || applies_to == "version-updates"
      warn "#{ecosystem}: dependency group must apply only to version updates"
      status = 1
    end
  end

  exit status
' "$config_path"
