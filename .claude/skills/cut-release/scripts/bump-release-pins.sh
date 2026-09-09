#!/usr/bin/env bash
# bump-release-pins.sh — bump the reusable-workflow version pins for a new release tag.
#
# The mecatequi reusable workflow references its three first-party sibling composite
# actions by a HARDCODED literal tag (an expression is illegal in `uses:`), so a release
# MUST bump those pins in the same tagged commit or the release ships pins pointing at the
# previous tag — the version skew the `lint:reusable-pins` gate fails the release on.
#
# This edits the two files that gate cares about and runs the gate to prove it passes:
#   .github/workflows/mecatequi-reusable.yml  (the three `uses:` pins + the header comment)
#   .github/actions/check-reusable-pins.sh    (the EXPECTED_TAG default, in lockstep)
#
# It does NOT touch the illustrative tag refs in user-docs/building/deployment/mecatequi.md (a release MAY
# bump those too, but they don't gate the release). It does NOT commit, tag, or push —
# that's the human/agent's job (see SKILL.md), so the version choice and the tag
# annotation stay deliberate.
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
for f in "${WF}" "${GATE}"; do
  [[ -f "${f}" ]] || { echo "error: ${f} not found — run from the repo root" >&2; exit 1; }
done

# Derive the current (old) tag from the gate's committed EXPECTED_TAG default.
OLD_TAG="$(sed -nE 's/^EXPECTED_TAG="\$\{EXPECTED_TAG:-(v[0-9]+\.[0-9]+\.[0-9]+)\}".*/\1/p' "${GATE}")"
if [[ -z "${OLD_TAG}" ]]; then
  echo "error: could not read the current EXPECTED_TAG from ${GATE}" >&2
  exit 1
fi
if [[ "${OLD_TAG}" == "${NEW_TAG}" ]]; then
  echo "error: ${GATE} already pins ${NEW_TAG} — nothing to bump" >&2
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
# The gate's EXPECTED_TAG default, in lockstep.
OLD_TAG="${OLD_TAG}" NEW_TAG="${NEW_TAG}" perl -pi -e \
  's/(EXPECTED_TAG="\$\{EXPECTED_TAG:-)\Q$ENV{OLD_TAG}\E(\}")/$1$ENV{NEW_TAG}$2/' \
  "${GATE}"

# Prove the gate passes both with an explicit override and on the committed default.
EXPECTED_TAG="${NEW_TAG}" bash "${GATE}"
bash "${GATE}"

echo
echo "Pins bumped. Next (see SKILL.md):"
echo "  git add ${GATE} ${WF}"
echo "  git commit -m 'chore(release): bump reusable-workflow pins ${OLD_TAG} -> ${NEW_TAG}'"
echo "  git tag -a ${NEW_TAG} -m '<release notes>'"
echo "  git push origin main && git push origin ${NEW_TAG}"
