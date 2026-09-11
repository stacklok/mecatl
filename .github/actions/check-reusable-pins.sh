#!/usr/bin/env bash
# check-reusable-pins.sh — assert every first-party sibling-action ref inside the reusable
# workflow pins to the CURRENT release tag. Driven by `task lint:reusable-pins`.
#
# WHY THIS EXISTS (the version-skew guard). The reusable workflow
# (.github/workflows/mecatequi-reusable.yml) references its sibling composite actions by FULL
# path with a HARDCODED literal tag (`stacklok/mecatl/.github/actions/<name>@<tag>`) — an
# expression is illegal in `uses:`, so the tag cannot be derived at run time. The release
# process must therefore bump these pins in the SAME commit it tags; if it forgets, the
# tagged workflow ships pins pointing at the PREVIOUS release (version skew — a consumer on
# @vNEW silently runs the vOLD actions). This check makes that mechanical: it extracts every
# `stacklok/mecatl/.github/actions/*@REF` pin and asserts they ALL equal EXPECTED_TAG.
#
# Override the expected tag with EXPECTED_TAG=vX.Y.Z (the release process passes the new tag
# before bumping); the default is the value currently committed in the workflow header, kept
# in ONE place below so a human bumping the release edits exactly one literal here + the pins.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${HERE}/.." && pwd)"           # repo root (.github/ -> ..)
WF="${ROOT}/workflows/mecatequi-reusable.yml"

# The CURRENT release tag the reusable workflow's sibling-action pins must equal. Bump this
# in lockstep with the @vX.Y.Z pins in mecatequi-reusable.yml at release time.
EXPECTED_TAG="${EXPECTED_TAG:-v0.0.34}"

if [ ! -f "${WF}" ]; then
  echo "check-reusable-pins: ${WF} not found" >&2
  exit 1
fi

# Extract every first-party sibling-action ref and its pinned tag. The grep matches a
# `uses: stacklok/mecatl/.github/actions/<name>@<ref>` line (with optional leading dash/space)
# and the sed captures the <ref>. Third-party `actions/*@<sha>` lines do NOT match this prefix.
# A plain while-read loop, not `mapfile` (bash 4+ only) — macOS ships bash 3.2 as /bin/bash, and
# this script is also run by maintainers locally (task lint:reusable-pins / cut-release).
refs=()
while IFS= read -r ref; do
  refs+=("${ref}")
done < <(
  grep -E 'uses:[[:space:]]+stacklok/mecatl/\.github/actions/[^@]+@' "${WF}" |
    sed -E 's#.*@([^[:space:]#]+).*#\1#'
)

if [ "${#refs[@]}" -eq 0 ]; then
  echo "check-reusable-pins: found NO stacklok/mecatl/.github/actions/* pins in ${WF}" >&2
  echo "  (the reusable workflow must reference its sibling actions by full path — did the file move?)" >&2
  exit 1
fi

fail=0
for ref in "${refs[@]}"; do
  if [ "${ref}" != "${EXPECTED_TAG}" ]; then
    echo "check-reusable-pins: pin @${ref} != expected @${EXPECTED_TAG}" >&2
    fail=1
  fi
done

if [ "${fail}" -ne 0 ]; then
  echo "check-reusable-pins: VERSION SKEW — every first-party sibling-action pin must equal ${EXPECTED_TAG}." >&2
  echo "  Bump the @vX.Y.Z pins in ${WF} (and EXPECTED_TAG in this script) in the SAME tagged commit." >&2
  exit 1
fi

echo "check-reusable-pins: all ${#refs[@]} first-party sibling-action pins == ${EXPECTED_TAG} (no version skew)"
