#!/bin/sh
# Run from any directory. Pass a NEW output directory under the checkout's .scratch.
set -eu
repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
case "${1-}" in
 "$repo"/.scratch/*) out=$1 ;;
 .scratch/*) out=$repo/$1 ;;
 *) echo 'usage: scripts/prove-external-persistence.sh .scratch/NEW-DIRECTORY [-race]' >&2; exit 2 ;;
esac
case "$out" in *'/../'*|*'/./'*|*'/..'|*'/.' ) echo 'output must stay under .scratch' >&2; exit 2;; esac
if [ -e "$out" ]; then echo "output already exists: $out" >&2; exit 2; fi
case "${2-}" in ''|-race) ;; *) echo 'second argument must be -race' >&2; exit 2;; esac
export GOPROXY=off GOSUMDB=off GOPRIVATE=none GONOPROXY=none GONOSUMDB=none
cache=$(go env GOMODCACHE)
go_bin=$(go env GOROOT)/bin/go
mkdir -p "$out"
(cd "$repo" && GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off "$go_bin" run ./scripts/candidateproxy "$repo" "$out" "$cache")
export GOWORK=off GOPROXY="file://$out/proxy" GOPRIVATE=none GONOPROXY=none GONOSUMDB=none GOSUMDB=off GOMODCACHE="$out/modcache" GOTOOLCHAIN=local
# No fallback proxy or workspace; even a reused build cache cannot supply
# modules absent from the isolated module cache and file proxy.
case "${GOCACHE:-}" in ''|"$repo"/.scratch/*) ;; *) echo 'GOCACHE override must be under this checkout .scratch' >&2; exit 2;; esac
export GOCACHE="${GOCACHE:-$out/buildcache}"
for mod in internal/adaptersupport contracts/gen/go/mecatl/driver adapters; do
 # candidateproxy rewrites adapters' support/driver requirements, so its tidy
 # sums differ from release sums. The release preflight checks those with tidy -diff.
 (cd "$out/source/$mod" && cp go.mod "$out/before-$(basename "$mod").mod" && "$go_bin" mod tidy && diff -u "$out/before-$(basename "$mod").mod" go.mod && if [ "$mod" != adapters ] && [ -f "$repo/$mod/go.sum" ]; then diff -u "$repo/$mod/go.sum" go.sum; fi && "$go_bin" test ${2-} ./...)
done
(cd "$out/engine-only" && "$go_bin" mod download github.com/stacklok/mecatl/engine && "$go_bin" list -m all > "$out/engine-graph.txt")
if grep -E 'github.com/stacklok/mecatl/(adapters|contracts|internal|provider)|github.com/redis/go-redis|google.golang.org/grpc' "$out/engine-graph.txt"; then
 echo 'engine closure contains adapter, transport, or host modules' >&2; exit 1
fi
(cd "$out/consumer" && "$go_bin" mod tidy && "$go_bin" test ${2-} ./... && "$go_bin" list -m all > "$out/graph.txt")
if grep -E '^github.com/stacklok/mecatl v|github.com/stacklok/mecatl/(internal/adapter/|internal/app|provider/)| => ' "$out/graph.txt"; then
 echo 'host module leaked into external graph' >&2; exit 1
fi
printf 'PASS: candidate proxy, standalone modules, external consumer; graph: %s/graph.txt\n' "$out"
