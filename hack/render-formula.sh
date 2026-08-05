#!/usr/bin/env bash
# Renders Formula/agentctl.rb from hack/formula.rb.tmpl.
# Usage: render-formula.sh VERSION CHECKSUMS_FILE   (VERSION bare, e.g. 0.1.0)
set -euo pipefail

version="${1:?usage: render-formula.sh VERSION CHECKSUMS_FILE}"
checksums="${2:?usage: render-formula.sh VERSION CHECKSUMS_FILE}"
[[ -r "$checksums" ]] || { echo "render-formula: cannot read $checksums" >&2; exit 1; }
tmpl="$(dirname "$0")/formula.rb.tmpl"

if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "render-formula: version must be bare X.Y.Z, got '$version'" >&2
  exit 1
fi

sha_for() {
  local arch="$1" sha want
  # Exact string match on the goreleaser name_template output, not a regex:
  # a regex here would need both left-anchoring and dot-escaping (".tar.gz"
  # otherwise matches any char for the dots) to avoid false positives.
  want="agentctl_${version}_darwin_${arch}.tar.gz"
  sha="$(awk -v want="$want" '$2 == want {print $1}' "$checksums")"
  if ! [[ "$sha" =~ ^[0-9a-f]{64}$ ]]; then
    echo "render-formula: no single valid sha256 for darwin_${arch} in $checksums" >&2
    exit 1
  fi
  echo "$sha"
}

sha_arm64="$(sha_for arm64)"
sha_amd64="$(sha_for amd64)"

sed -e "s/__VERSION__/${version}/" \
    -e "s/__SHA_ARM64__/${sha_arm64}/" \
    -e "s/__SHA_AMD64__/${sha_amd64}/" \
    "$tmpl"
