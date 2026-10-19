#!/bin/sh
# Decide whether a continuous main build publishes, and under which version.
# Run from a full-history checkout of main, logged in to the chart registry;
# continuous.yml's compute-version job is the only caller.
set -eu

: "${GITHUB_EVENT_NAME:?GITHUB_EVENT_NAME is required}"
: "${GITHUB_SHA:?GITHUB_SHA is required}"
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
: "${GITHUB_OUTPUT:?GITHUB_OUTPUT is required}"
: "${RUN_NUMBER:?RUN_NUMBER is required}"
: "${RUN_ATTEMPT:?RUN_ATTEMPT is required}"
: "${OCI_REGISTRY:?OCI_REGISTRY is required}"
: "${SIGNING_IDENTITY:?SIGNING_IDENTITY is required}"
: "${SIGNING_ISSUER:?SIGNING_ISSUER is required}"

force=false
[ "${FORCE:-}" = true ] && force=true

fail() {
  echo "::error::$1"
  exit 1
}

# A failed lookup of the last publication blocks every later run, so force
# downgrades it to a warning; force already skips the check it feeds.
lookup_failed() {
  if [ "$force" = true ]; then
    echo "::warning::$1; publishing anyway because force is set"
  else
    fail "$1; set force to publish regardless"
  fi
}

# Succeeds for exactly one full lowercase commit SHA (no embedded newlines).
is_full_sha() {
  case "$1" in
    '' | *[!0-9a-f]*) return 1 ;;
  esac
  [ "${#1}" -eq 40 ]
}

# Prints "MAJOR MINOR PATCH RUN ATTEMPT" for a main build version, or nothing.
main_fields() {
  printf '%s\n' "$1" | sed -n 's/^\([0-9][0-9]*\)\.\([0-9][0-9]*\)\.\([0-9][0-9]*\)-main\.\([0-9][0-9]*\)\.\([0-9][0-9]*\)\.g[0-9a-f]\{8\}$/\1 \2 \3 \4 \5/p'
}

# Succeeds when the space-separated numeric tuple $1 sorts above $2.
tuple_above() {
  awk -v a="$1" -v b="$2" 'BEGIN {
    n = split(a, x, " "); split(b, y, " ")
    for (i = 1; i <= n; i++) {
      if (x[i] + 0 > y[i] + 0) exit 0
      if (x[i] + 0 < y[i] + 0) exit 1
    }
    exit 1
  }'
}

case "$RUN_NUMBER$RUN_ATTEMPT" in
  *[!0-9]*) fail "run number and attempt must be numeric" ;;
esac

main=$(git rev-parse HEAD)
case "$GITHUB_EVENT_NAME" in
  workflow_run)
    sha="${RUN_SHA:-}"
    ;;
  workflow_dispatch)
    # Provenance names the dispatched commit, so publish that commit unless
    # another one is forced.
    sha="${INPUT_SHA:-$GITHUB_SHA}"
    if [ "$sha" != "$GITHUB_SHA" ] && [ "$force" != true ]; then
      fail "a dispatch publishes its own commit ${GITHUB_SHA}; set force to publish ${sha}"
    fi
    ;;
  *)
    fail "unsupported event: ${GITHUB_EVENT_NAME}"
    ;;
esac
if ! is_full_sha "$sha"; then
  fail "commit must be a full 40-character SHA, got '${sha}'"
fi
if ! git merge-base --is-ancestor "$sha" "$main"; then
  fail "${sha} is not on main"
fi

# workflow_run only fires after CI passes; hold a dispatch to the same bar.
# Force skips it: CI cancels superseded main runs, so an older rollback target
# often has no successful run of its own.
if [ "$GITHUB_EVENT_NAME" = workflow_dispatch ] && [ "$force" != true ]; then
  passed=$(gh api "repos/${GITHUB_REPOSITORY}/actions/workflows/ci.yml/runs?head_sha=${sha}&event=push&status=success&per_page=1" --jq .total_count)
  case "$passed" in
    '' | *[!0-9]* | 0) fail "CI has not passed on ${sha}; wait for it, or set force" ;;
  esac
fi

# The last publication is the newest mecatl-studio chart, which each run pushes
# last. Its revision annotation names the full commit it was built from.
last=""
last_version=""
last_fields=""
unverified=false
if chart=$(helm show chart "oci://${OCI_REGISTRY}/mecatl-studio" --devel 2>&1); then
  last_version=$(printf '%s\n' "$chart" | awk '/^version:/ {print $2}')
  last_fields=$(main_fields "$last_version")
  revision=$(printf '%s\n' "$chart" | awk '/^  org\.opencontainers\.image\.revision:/ {gsub(/"/, "", $2); print $2}')
  if [ -z "$last_fields" ]; then
    lookup_failed "last published version '${last_version}' is not a main build version"
  elif ! is_full_sha "$revision" \
    || ! git cat-file -e "${revision}^{commit}" 2>/dev/null \
    || ! git merge-base --is-ancestor "$revision" "$main"; then
    lookup_failed "last published ${last_version} names no commit on main (revision '${revision}')"
  else
    last=$revision
    echo "Last published: ${last_version} (${last})"
    # A chart pushed by a run that failed before signing or attesting it does
    # not count as published, so the same commit can publish again.
    ref="${OCI_REGISTRY}/mecatl-studio:${last_version}"
    if ! cosign verify --certificate-identity "$SIGNING_IDENTITY" --certificate-oidc-issuer "$SIGNING_ISSUER" "$ref" >/dev/null 2>&1 \
      || ! cosign verify-attestation --type slsaprovenance1 --certificate-identity "$SIGNING_IDENTITY" --certificate-oidc-issuer "$SIGNING_ISSUER" "$ref" >/dev/null 2>&1; then
      echo "::warning::last published ${last_version} is not signed and attested by this workflow"
      unverified=true
    fi
  fi
elif printf '%s\n' "$chart" | grep -q 'name unknown'; then
  echo "Nothing published yet"
else
  printf '%s\n' "$chart" >&2
  lookup_failed "could not read the last published mecatl-studio chart"
fi

if [ -n "$last" ] && [ "$force" != true ] && git merge-base --is-ancestor "$sha" "$last"; then
  if [ "$unverified" = true ] && [ "$sha" = "$last" ]; then
    echo "Publishing ${sha} again to replace its unverified chart"
  else
    echo "::notice title=Already published::${sha} is not newer than the last published build ${last}."
    echo "publish=false" >>"$GITHUB_OUTPUT"
    exit 0
  fi
fi

base=$(git show "${sha}:VERSION" | tr -d '[:space:]')
if ! printf '%s\n' "$base" | grep -Eqx '(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)'; then
  fail "VERSION at ${sha} is not bare semver: '${base}'"
fi
major=${base%%.*}
rest=${base#*.}
minor=${rest%%.*}
patch=$((${rest#*.} + 1))
# A forced older commit can predate a release's VERSION bump. Keep at least the
# last publication's MAJOR.MINOR.PATCH so the run number still orders it first.
if [ -n "$last_fields" ]; then
  read -r last_major last_minor last_patch _ _ <<EOF
$last_fields
EOF
  if tuple_above "$last_major $last_minor $last_patch" "$major $minor $patch"; then
    major=$last_major minor=$last_minor patch=$last_patch
  fi
  if ! tuple_above "$major $minor $patch $RUN_NUMBER $RUN_ATTEMPT" "$last_fields"; then
    fail "run ${RUN_NUMBER}.${RUN_ATTEMPT} would not sort above the last published ${last_version}"
  fi
fi
# The g prefix keeps an all-digit short SHA a valid semver identifier.
version="${major}.${minor}.${patch}-main.${RUN_NUMBER}.${RUN_ATTEMPT}.g$(printf '%s' "$sha" | cut -c1-8)"
{
  echo "publish=true"
  echo "sha=${sha}"
  echo "version=${version}"
} >>"$GITHUB_OUTPUT"
echo "Publishing ${sha} as ${version}"
