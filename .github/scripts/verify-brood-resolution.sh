#!/bin/sh
set -eu

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <brood-base-index.json> <brood-platforms.json>" >&2
  exit 2
fi

index=$1
platforms=$2
evidence="sha256:$(sha256sum "$index" | cut -d' ' -f1)"

jq -e 'type == "object" and (.manifests | type == "array")' "$index" >/dev/null
jq -e 'type == "object" and (keys | sort == ["amd64", "arm64"])' "$platforms" >/dev/null

for arch in amd64 arm64; do
  manifest=$(jq -er --arg arch "$arch" \
    '[.manifests[] | select(.platform.os == "linux" and .platform.architecture == $arch) | .digest] |
     if length == 1 then .[0] else error("expected exactly one linux/" + $arch + " manifest") end' "$index")
  if ! printf '%s\n' "$manifest" | grep -E '^sha256:[0-9a-f]{64}$' >/dev/null; then
    echo "invalid linux/$arch manifest digest: $manifest" >&2
    exit 1
  fi
  jq -e --arg arch "$arch" --arg manifest "$manifest" --arg evidence "$evidence" \
    '.[$arch].manifest_digest == $manifest and
     .[$arch].reference == ("ghcr.io/stacklok/brood-box/base@" + $manifest) and
     .[$arch].resolution_evidence == $evidence and
     .[$arch].platform == ("linux/" + $arch) and
     (.[$arch].tree_digest | test("^sha256:[0-9a-f]{64}$")) and
     (.[$arch].discovery_reference | type == "string" and endswith(":latest"))' \
    "$platforms" >/dev/null
done
