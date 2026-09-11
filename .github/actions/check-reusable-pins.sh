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
# before bumping). The DEFAULT is DERIVED from the repo-root VERSION file — the single authored
# source of truth for the release version — so this script holds NO copy of the version and
# cannot drift from it. VERSION is bare semver (0.0.33); the leading `v` is added here.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${HERE}/.." && pwd)"           # the .github/ dir (.github/actions/ -> ..)
REPO_ROOT="$(cd "${ROOT}/.." && pwd)"      # the repo root (.github/ -> ..)
WF="${ROOT}/workflows/mecatequi-reusable.yml"
VERSION_FILE="${REPO_ROOT}/VERSION"

# The CURRENT release tag the reusable workflow's sibling-action pins must equal, DERIVED from
# VERSION rather than duplicated here. `create-release-pr.yml` bumps VERSION and the pins in the
# same commit; nothing hand-edits a version literal in this file.
if [ -z "${EXPECTED_TAG:-}" ]; then
  if [ ! -f "${VERSION_FILE}" ]; then
    echo "check-reusable-pins: ${VERSION_FILE} not found — it is the source of the expected tag" >&2
    exit 1
  fi
  # tr, not $(cat): strips the trailing newline and any stray whitespace in one portable step.
  EXPECTED_TAG="v$(tr -d '[:space:]' < "${VERSION_FILE}")"
fi

case "${EXPECTED_TAG}" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *)
    echo "check-reusable-pins: expected tag '${EXPECTED_TAG}' is not a vX.Y.Z semver tag" >&2
    echo "  (VERSION must hold BARE semver, e.g. 1.2.3 — not v1.2.3)" >&2
    exit 1
    ;;
esac

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
  echo "  Bump VERSION and the @vX.Y.Z pins in ${WF} in the SAME tagged commit." >&2
  echo "  create-release-pr.yml does this; .claude/skills/cut-release/scripts/bump-release-pins.sh is the local path." >&2
  exit 1
fi

echo "check-reusable-pins: all ${#refs[@]} first-party sibling-action pins == ${EXPECTED_TAG} (no version skew)"
