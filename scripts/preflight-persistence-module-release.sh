#!/bin/sh
# Manual release gate; never run from offline CI or during ordinary tidy/test.
# From a reviewed main commit, tag internal/adaptersupport/v0.1.0 and
# contracts/gen/go/mecatl/driver/v0.1.0 first (nested-module tag convention).
# Only after both are published, run adapters preflight, commit the real
# standalone adapters/go.sum, and tag adapters/v0.1.0 on that later commit.
# No root v* release workflow publishes these nested modules.
set -eu
repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
case "${1-}" in
  support) module=internal/adaptersupport ;;
  driver) module=contracts/gen/go/mecatl/driver ;;
  adapters) module=adapters ;;
  *) echo 'usage: scripts/preflight-persistence-module-release.sh {support|driver|adapters}' >&2; exit 2 ;;
esac
# Force the public, checksum-backed proxy: a local checkout, replace, VCS tag
# not yet indexed by the proxy, or private module cache is not a release.
export GOWORK=off GOPROXY=https://proxy.golang.org GONOPROXY=none GOPRIVATE=none GONOSUMDB=none GOSUMDB=sum.golang.org GOMODCACHE="$repo/.scratch/persistence-release-modcache" GOTOOLCHAIN=local
if [ "$module" = adapters ]; then
  for dependency in \
    github.com/stacklok/mecatl/internal/adaptersupport@v0.1.0 \
    github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver@v0.1.0; do
    go mod download "$dependency" || { echo "publish $dependency before releasing adapters" >&2; exit 1; }
  done
  if [ ! -f "$repo/adapters/go.sum" ]; then
    echo 'adapters/go.sum is absent: after publishing support and driver, tidy GOWORK=off and commit the real checksums before tagging adapters' >&2
    exit 1
  fi
fi
(cd "$repo/$module" && go mod tidy -diff)
