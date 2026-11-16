#!/bin/sh
# Vet every tracked Go module for linux and darwin, untagged and with every
# custom build tag at once, so tag-gated files (e2e, kind) compile before CI.
set -eu

cd "$(git rev-parse --show-toplevel)"

# Identifiers in //go:build lines, minus GOOS/GOARCH, toolchain tags, and the
# ignore/tools conventions (never meant to compile as a package).
builtin=" $(go tool dist list | tr '/' '\n' | sort -u | tr '\n' ' ')"
builtin="$builtin unix cgo race msan asan gc gccgo ignore tools "
tags=
for tag in $(git ls-files -z '*.go' | xargs -0 grep -h '^//go:build' |
	sed 's|^//go:build||' | tr -c 'A-Za-z0-9_.\n' ' ' | tr ' ' '\n' | sort -u); do
	case "$builtin" in *" $tag "*) continue ;; esac
	case "$tag" in go1.* | goexperiment.*) continue ;; esac
	tags="$tags${tags:+,}$tag"
done
echo "custom build tags: $tags"

# The live e2e harness is Linux-only by design (Pdeathsig; see e2e/README.md).
darwin_tags=$(printf '%s\n' "$tags" | tr ',' '\n' | grep -vx e2e | paste -sd, -)

status=0
for mod in $(git ls-files '*go.mod' | xargs -n1 dirname); do
	for goos in linux darwin; do
		set_tags=$tags
		[ "$goos" = darwin ] && set_tags=$darwin_tags
		for t in "" "$set_tags"; do
			echo "==> $mod GOOS=$goos tags=${t:-none}"
			(cd "$mod" && GOOS=$goos go vet -tags="$t" ./...) || status=1
		done
	done
done
exit "$status"
