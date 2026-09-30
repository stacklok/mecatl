#!/bin/sh
set -eu

root=$(git rev-parse --show-toplevel)
cd "$root"
command -v jq >/dev/null 2>&1 || { echo 'module publication check requires jq' >&2; exit 1; }
git rev-parse --verify refs/remotes/origin/main >/dev/null 2>&1 || {
  echo 'module publication check requires a fetched origin/main' >&2
  exit 1
}

# Releaseable manifests must not depend on a PR-head commit that a squash
# merge will leave outside main. Nested tags belong to their exact module.
check_requirements() {
  manifest=$1
  manifest_json=$(go mod edit -json "$manifest")
  requirements=$(printf '%s\n' "$manifest_json" | jq -r '.Require[]? | select(.Path | startswith("github.com/stacklok/mecatl/")) | [.Path, .Version] | @tsv')
  [ -n "$requirements" ] || return 0
  printf '%s\n' "$requirements" | while IFS="$(printf '\t')" read -r module version; do
    nested=${module#github.com/stacklok/mecatl/}
    if commit=$(git rev-parse -q --verify "refs/tags/$nested/$version^{commit}" 2>/dev/null); then
      :
    else
      case "$version" in
        v*.*.*-*-*) revision=${version##*-} ;;
        *) echo "$manifest: $module@$version has no published nested tag" >&2; exit 1 ;;
      esac
      commit=$(git rev-parse -q --verify "$revision^{commit}" 2>/dev/null) || {
        echo "$manifest: $module@$version is neither a published nested tag nor a known commit" >&2
        exit 1
      }
    fi
    git merge-base --is-ancestor "$commit" refs/remotes/origin/main || {
      echo "$manifest: $module@$version is not on origin/main (squash-merge risk)" >&2
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
  commit=$(git rev-parse -q --verify "refs/tags/adapters/$candidate^{commit}" 2>/dev/null) || {
    echo "adapters/$candidate has no published nested tag" >&2
    exit 1
  }
  git merge-base --is-ancestor "$commit" refs/remotes/origin/main || {
    echo "adapters/$candidate is not on origin/main (squash-merge risk)" >&2
    exit 1
  }
done

# A checkout's go.work, replace directives, and warmed cache must not provide
# evidence for a published module. Go may fetch only from the public proxy.
work="$root/.scratch/module-publication-$$"
mkdir -p "$root/.scratch"
mkdir "$work"
mkdir "$work/consumer" "$work/cache"
# Extracted module files are read-only; restore owner write before cleanup.
trap 'chmod -R u+w "$work/cache" && rm -rf "$work"' EXIT
cat > "$work/consumer/go.mod" <<EOF
module example.com/mecatl-module-publication-check

go 1.27.0

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
