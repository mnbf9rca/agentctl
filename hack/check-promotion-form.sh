#!/usr/bin/env bash
# hack/check-promotion-form.sh — verifies the FORM of a promotion PR's
# release attestation (issue #93):
#
#   1. exactly one of the two attestation checkboxes in the PR body is
#      ticked (`- [x]`);
#   2. the `Version:` line contains a bare semver X.Y.Z that exactly
#      matches the output of hack/next-version.sh at the PR head.
#
# This is a FORM check only. It cannot and does not verify that the
# checklist ceremony a ticked box claims actually ran — that judgment is
# structurally human (spec §7); a syntax check must never masquerade as
# ceremony verification (spec §1.1).
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: hack/check-promotion-form.sh BODY_FILE

Reads a promotion PR body from BODY_FILE and checks its FORM:
  1. exactly one of the two attestation checkboxes is ticked
  2. the Version: line is a bare semver matching hack/next-version.sh

Exits 0 and prints FORM CHECK PASSED on success. Exits 1 and prints
FORM CHECK FAILED with the specific violation on failure.

This is a FORM check only: it cannot detect whether the checklist ceremony
a ticked box claims actually ran.
EOF
}

die() {
  printf 'FORM CHECK FAILED: %s\n' "$*" >&2
  printf 'This check verifies FORM only: it cannot and does not verify that the checklist ceremony occurred.\n' >&2
  exit 1
}

if [[ $# -ne 1 ]]; then
  usage >&2
  exit 2
fi

body_file=$1
if [[ ! -f "$body_file" ]]; then
  die "PR body file not found: $body_file"
fi

root="$(git rev-parse --show-toplevel)"
body="$(cat "$body_file")"

box_state() {
  # box_state LABEL — prints the checkbox mark ('x', 'X', or ' ') for the
  # first line shaped "- [MARK] **LABEL** ..."; empty if no such line exists.
  # grep's no-match exit (1) must not trip `set -e pipefail` here — an
  # absent checkbox is a condition this script reports via die(), not an
  # unhandled script abort.
  local label=$1
  local line
  line="$(printf '%s\n' "$body" | grep -m1 -E "^- \[.\] \*\*${label}\*\*" || true)"
  if [[ -z "$line" ]]; then
    return 0
  fi
  printf '%s\n' "$line" | sed -E 's/^- \[(.)\].*/\1/'
}

run_state="$(box_state 'Checklist run\.')"
skip_state="$(box_state 'Checklist not required\.')"

if [[ -z "$run_state" || -z "$skip_state" ]]; then
  die "could not find both attestation checkboxes in the PR body (expected 'Checklist run.' and 'Checklist not required.' lines)"
fi

ticked=0
[[ "$run_state" == "x" || "$run_state" == "X" ]] && ticked=$((ticked + 1))
[[ "$skip_state" == "x" || "$skip_state" == "X" ]] && ticked=$((ticked + 1))

if [[ "$ticked" -eq 0 ]]; then
  die "neither attestation checkbox is ticked; check exactly one of 'Checklist run.' or 'Checklist not required.'"
elif [[ "$ticked" -eq 2 ]]; then
  die "both attestation checkboxes are ticked; check exactly one of 'Checklist run.' or 'Checklist not required.'"
fi

if [[ "$run_state" == "x" || "$run_state" == "X" ]]; then
  evidence_line="$(printf '%s\n' "$body" | grep -m1 -E '^Evidence location:' || true)"
  if [[ -z "$evidence_line" ]]; then
    die "no 'Evidence location:' line found for checklist-required promotion"
  fi
  evidence_value="$(printf '%s\n' "$evidence_line" | sed -E 's/^Evidence location:[[:space:]]*//; s/<!--.*-->//; s/[[:space:]]+$//')"
  if [[ -z "$evidence_value" ]]; then
    die "Evidence location: has no committed evidence path for checklist-required promotion"
  fi
  for label in 'Detached launch passed\.' 'Per-role attach passed\.' 'Signal and terminal restoration passed\.'; do
    state="$(box_state "$label")"
    if [[ "$state" != "x" && "$state" != "X" ]]; then
      die "checklist-required promotion must affirm '$label'"
    fi
  done
fi

version_line="$(printf '%s\n' "$body" | grep -m1 -E '^Version:' || true)"
if [[ -z "$version_line" ]]; then
  die "no 'Version:' line found in the PR body"
fi

version_value="$(printf '%s\n' "$version_line" | sed -E 's/^Version:[[:space:]]*//; s/<!--.*-->//; s/[[:space:]]+$//')"

if [[ -z "$version_value" ]]; then
  die "the Version: line has no value (still the template placeholder?)"
fi

if ! [[ "$version_value" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  die "Version: value '$version_value' is not a bare semver X.Y.Z"
fi

expected="$("$root/hack/next-version.sh")"
if [[ "$version_value" != "$expected" ]]; then
  die "Version: '$version_value' does not match hack/next-version.sh output '$expected' at the PR head"
fi

printf 'FORM CHECK PASSED: exactly one attestation checkbox ticked; Version %s matches hack/next-version.sh.\n' "$version_value"
printf 'This check verifies FORM only: it cannot and does not verify that the checklist ceremony occurred.\n'
