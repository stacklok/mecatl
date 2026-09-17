#!/bin/sh
set -eu

: "${VERSION:?VERSION is required}"
: "${GITHUB_OUTPUT:?GITHUB_OUTPUT is required}"

if ! printf '%s\n' "$VERSION" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'; then
  echo "release tag must match vMAJOR.MINOR.PATCH" >&2
  exit 1
fi

tag_ref="refs/tags/$VERSION"
if ! git show-ref --verify --quiet "$tag_ref"; then
  echo "release tag does not exist: $VERSION" >&2
  exit 1
fi
commit=$(git rev-parse --verify "$tag_ref^{commit}")
if ! printf '%s\n' "$commit" | grep -Eq '^[0-9a-f]{40}$'; then
  echo "release tag did not peel to a commit" >&2
  exit 1
fi

case "${GITHUB_EVENT_NAME:-}" in
  push)
    if [ "${GITHUB_REF:-}" != "$tag_ref" ]; then
      echo "push ref does not match the release tag" >&2
      exit 1
    fi
    if ! printf '%s\n' "${GITHUB_SHA:-}" | grep -Eq '^[0-9a-f]{40}$'; then
      echo "push commit is not a full SHA" >&2
      exit 1
    fi
    event_commit=$(git rev-parse --verify "${GITHUB_SHA}^{commit}" 2>/dev/null) || {
      echo "push commit did not peel to a commit" >&2
      exit 1
    }
    if [ "$event_commit" != "$commit" ]; then
      echo "push commit does not match the release tag" >&2
      exit 1
    fi
    ;;
  workflow_dispatch)
    # A dispatch runs from the selected workflow branch, not from the tag.
    ;;
  *)
    echo "unsupported release event: ${GITHUB_EVENT_NAME:-unset}" >&2
    exit 1
    ;;
esac
if ! git merge-base --is-ancestor "$commit" origin/main; then
  echo "release tag commit is not on origin/main" >&2
  exit 1
fi

git checkout --detach "$commit" >/dev/null 2>&1
head=$(git rev-parse --verify HEAD)
if [ "$head" != "$commit" ]; then
  echo "checked-out commit does not match the verified release tag" >&2
  exit 1
fi

{
  echo "version=$VERSION"
  echo "commit=$commit"
} >>"$GITHUB_OUTPUT"
printf '%s is verified at %s\n' "$VERSION" "$commit"
