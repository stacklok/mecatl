#!/usr/bin/env bash
# bump-release-pins.sh — bump the release version and the reusable-workflow pins LOCALLY.
#
# THE NORMAL PATH IS CI, NOT THIS SCRIPT. `.github/workflows/create-release-pr.yml` opens the
# release PR and drives the same bump through `stacklok/releaseo`. This script exists for two
# narrower jobs: a local dry run (see what the release PR will contain, without dispatching a
# workflow), and the emergency fallback if releaseo is unavailable.
#
# The mecatequi reusable workflow references its three first-party sibling composite
# actions by a HARDCODED literal tag (an expression is illegal in `uses:`), so a release
# MUST bump those pins in the same tagged commit or the release ships pins pointing at the
# previous tag — the version skew the `lint:reusable-pins` gate fails the release on.
#
# This edits the two files that carry the version and runs the gate to prove it passes:
#   VERSION                                   (BARE semver — the single authored source of truth)
#   .github/workflows/mecatequi-reusable.yml  (the three `uses:` pins)
#
# It does NOT edit .github/actions/check-reusable-pins.sh: that script DERIVES its expected tag
# from VERSION and holds no copy of the version. It does NOT touch the illustrative tag refs in
# user-docs/building/deployment/mecatequi.md (a release MAY bump those too, but they don't gate
# the release). It does NOT commit, tag, or push — that's the human/agent's job (see SKILL.md).
#
# Usage: scripts/bump-release-pins.sh vX.Y.Z   (run from the repo root)
set -euo pipefail

NEW_TAG="${1:-}"
if [[ -z "${NEW_TAG}" ]]; then
  echo "usage: $0 vX.Y.Z" >&2
  exit 2
fi
if [[ ! "${NEW_TAG}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "error: tag '${NEW_TAG}' is not a vX.Y.Z semver tag" >&2
  exit 2
fi

WF=".github/workflows/mecatequi-reusable.yml"
GATE=".github/actions/check-reusable-pins.sh"
VERSION_FILE="VERSION"
for f in "${WF}" "${GATE}" "${VERSION_FILE}"; do
  [[ -f "${f}" ]] || { echo "error: ${f} not found — run from the repo root" >&2; exit 1; }
done

# Derive the current (old) tag from VERSION, which holds BARE semver (0.0.33, not v0.0.33).
OLD_TAG="v$(tr -d '[:space:]' < "${VERSION_FILE}")"
if [[ ! "${OLD_TAG}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "error: ${VERSION_FILE} does not hold bare semver (read '${OLD_TAG#v}')" >&2
  exit 1
fi
if [[ "${OLD_TAG}" == "${NEW_TAG}" ]]; then
  echo "error: ${VERSION_FILE} already holds ${NEW_TAG#v} — nothing to bump" >&2
  exit 1
fi

echo "Bumping reusable-workflow pins ${OLD_TAG} -> ${NEW_TAG}"

# perl -pi -e, not `sed -i -E`: BSD sed (macOS's default /bin/sed) parses `-i -E` as `-i` taking
# `-E` itself as its (mandatory-argument) backup suffix, silently running in BASIC regex mode —
# the `(...)`/`\1` groups below then fail with "\1 not defined in the RE". perl's -i is
# consistent across BSD/GNU/macOS/Linux. OLD_TAG/NEW_TAG cross via %ENV (already validated
# vX.Y.Z above), never interpolated into the regex/replacement source, so no quoting hazard.
# The three sibling-action `uses:` pins (and the matching header-comment reference).
OLD_TAG="${OLD_TAG}" NEW_TAG="${NEW_TAG}" perl -pi -e \
  's/(stacklok\/mecatl\/\.github\/actions\/[^@]+@)\Q$ENV{OLD_TAG}\E/$1$ENV{NEW_TAG}/g' \
  "${WF}"
# VERSION carries BARE semver and is the source the gate derives its default from, so writing
# it here is all that is needed — the gate needs no edit and cannot fall out of lockstep.
printf '%s\n' "${NEW_TAG#v}" > "${VERSION_FILE}"

# Prove the gate passes both with an explicit override and on the committed default.
EXPECTED_TAG="${NEW_TAG}" bash "${GATE}"
bash "${GATE}"

echo
echo "Version and pins bumped locally (${OLD_TAG} -> ${NEW_TAG}). Changed:"
echo "  ${VERSION_FILE}"
echo "  ${WF}"
echo
echo "This is a DRY RUN of what create-release-pr.yml produces. To cut a real release, revert"
echo "these edits and dispatch the workflow instead (see SKILL.md):"
echo "  git checkout -- ${VERSION_FILE} ${WF}"
echo "  gh workflow run create-release-pr.yml -f bump_type=patch"
