#!/bin/sh
set -eu

root=$(git rev-parse --show-toplevel)
cd "$root"
command -v jq >/dev/null 2>&1 || { echo 'module publication check requires jq' >&2; exit 1; }
git rev-parse --verify refs/remotes/origin/main >/dev/null 2>&1 || {
  echo 'module publication check requires a fetched origin/main' >&2
  exit 1
}

actual_main=$(git rev-parse refs/remotes/origin/main)
tag_main=$actual_main
tag_main_name=origin/main
prospective_main=
pr_head=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --prospective-main|--pr-head)
      [ "$#" -ge 2 ] || { echo "missing value for $1" >&2; exit 1; }
      case "$2" in
        *[!0-9a-f]*|'') echo "$1 requires a full commit SHA" >&2; exit 1 ;;
      esac
      [ "${#2}" -eq 40 ] || { echo "$1 requires a full commit SHA" >&2; exit 1; }
      case "$1" in
        --prospective-main) prospective_main=$2 ;;
        --pr-head) pr_head=$2 ;;
      esac
      shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 1 ;;
  esac
done
if [ -n "$prospective_main$pr_head" ]; then
  [ -n "$prospective_main" ] && [ -n "$pr_head" ] &&
    [ "$(git rev-parse HEAD)" = "$prospective_main" ] &&
    [ "$(git show -s --format=%P "$prospective_main")" = "$actual_main $pr_head" ] || {
    echo 'prospective main must be the checked-out two-parent merge of fetched origin/main and reviewed PR head' >&2
    exit 1
  }
  tag_main=$prospective_main
  tag_main_name="origin/main's validated prospective merge"
fi

# Canonical tags may enter main through the reviewed PR merge. Untagged
# pseudo-versions must already belong to actual main, never just a PR head.
check_requirements() {
  manifest=$1
  manifest_json=$(go mod edit -json "$manifest")
  requirements=$(printf '%s\n' "$manifest_json" | jq -r '.Require[]? | select(.Path | startswith("github.com/stacklok/mecatl/")) | [.Path, .Version] | @tsv')
  [ -n "$requirements" ] || return 0
  printf '%s\n' "$requirements" | while IFS="$(printf '\t')" read -r module version; do
    nested=${module#github.com/stacklok/mecatl/}
    ancestry=$tag_main
    ancestry_name=$tag_main_name
    if commit=$(git -C "$root" rev-parse -q --verify "refs/tags/$nested/$version^{commit}" 2>/dev/null); then
      :
    else
      ancestry=$actual_main
      ancestry_name=origin/main
      case "$version" in
        v*.*.*-*-*) revision=${version##*-} ;;
        *) echo "$manifest: $module@$version has no published nested tag" >&2; exit 1 ;;
      esac
      commit=$(git -C "$root" rev-parse -q --verify "$revision^{commit}" 2>/dev/null) || {
        echo "$manifest: $module@$version is neither a published nested tag nor a known commit" >&2
        exit 1
      }
    fi
    git -C "$root" merge-base --is-ancestor "$commit" "$ancestry" || {
      echo "$manifest: $module@$version is not on $ancestry_name (squash-merge risk)" >&2
      exit 1
    }
  done
}
check_requirements internal/adaptersupport/go.mod
check_requirements contracts/gen/go/mecatl/driver/go.mod
check_requirements adapters/go.mod

pinned=$(go mod edit -json go.mod | jq -r '.Require[] | select(.Path == "github.com/stacklok/mecatl/adapters") | .Version')
integration=$(go mod edit -json integration/microvm/go.mod | jq -r '.Require[] | select(.Path == "github.com/stacklok/mecatl/adapters") | .Version')
[ -n "$pinned" ] && [ "$pinned" = "$integration" ] || {
  echo 'root and microVM integration must pin the same published adapters version' >&2
  exit 1
}
version=${MODULE_PUBLICATION_VERSION:-$pinned}
for candidate in "$pinned" "$version"; do
  commit=$(git -C "$root" rev-parse -q --verify "refs/tags/adapters/$candidate^{commit}" 2>/dev/null) || {
    echo "adapters/$candidate has no published nested tag" >&2
    exit 1
  }
  git -C "$root" merge-base --is-ancestor "$commit" "$tag_main" || {
    echo "adapters/$candidate is not on $tag_main_name (squash-merge risk)" >&2
    exit 1
  }
done

# A checkout's go.work, replace directives, and warmed cache must not provide
# evidence for a published module. Go may fetch only from the public proxy.
work=$(mktemp -d "${TMPDIR:-/tmp}/module-publication.XXXXXXXX")
# Extracted module files are read-only; restore owner write before cleanup.
trap 'chmod -R u+w "$work"; rm -rf "$work"' EXIT
mkdir "$work/consumer" "$work/cache"
cat > "$work/consumer/go.mod" <<EOF
module example.com/mecatl-module-publication-check

go 1.27

require github.com/stacklok/mecatl/adapters $version
EOF
cat > "$work/consumer/smoke_test.go" <<'EOF'
package publicationcheck

import (
    _ "github.com/stacklok/mecatl/adapters/grpcdriver"
    _ "github.com/stacklok/mecatl/adapters/jsonlstore"
    _ "github.com/stacklok/mecatl/adapters/redisstore"
)
EOF
cd "$work/consumer"
export GOWORK=off GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org
export GOPRIVATE=none GONOPROXY=none GONOSUMDB=none GOMODCACHE="$work/cache"
GOFLAGS=-mod=mod go mod tidy
published_mod=$(GOFLAGS=-mod=readonly go list -m -f '{{.GoMod}}' github.com/stacklok/mecatl/adapters)
check_requirements "$published_mod"
GOFLAGS=-mod=readonly go test ./...
