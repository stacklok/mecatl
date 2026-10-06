#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)

# Read literal global ARG defaults as data, never as shell code. Keep the
# Dockerfiles as the sole source of truth, including when run outside the root.
read_pin() {
  awk -v name="$2" '
    /^[[:space:]]*FROM[[:space:]]/ { exit }
    $0 ~ "^[[:space:]]*ARG[[:space:]]+" name "([[:space:]]|=|$)" {
      count++
      if ($0 !~ "^ARG " name "=") invalid = 1
      else value = substr($0, length(name) + 6)
    }
    END { if (count != 1 || invalid) exit 1; print value }
  ' "$repo_root/$1" || {
    echo "$1 must define exactly one global $2 default" >&2
    exit 1
  }
}

check_image() {
  name=$1
  image=$2
  if ! printf '%s\n' "$image" | LC_ALL=C grep -Eq '^[a-z0-9][a-z0-9./:_-]*/[a-z0-9][a-z0-9._/-]*:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}@sha256:[0-9a-f]{64}$'; then
    echo "$name must be a tagged image with a lowercase 64-character SHA-256 digest" >&2
    exit 1
  fi
}

check_platforms() {
  name=$1
  image=$2
  if ! manifest=$(docker buildx imagetools inspect --raw "$image"); then
    echo "$name could not be inspected" >&2
    exit 1
  fi
  if ! printf '%s\n' "$manifest" | jq -e '
    [.manifests[] | .platform | select(.os == "linux") | .architecture] as $arches
    | ($arches | map(select(. == "amd64")) | length) == 1
      and ($arches | map(select(. == "arm64")) | length) == 1
  ' >/dev/null; then
    echo "$name must provide a multiarch index with linux/amd64 and linux/arm64" >&2
    exit 1
  fi
}

provider_go=$(read_pin build/execution-provider/Dockerfile GO_IMAGE)
workload_go=$(read_pin build/execution-workload/Dockerfile GO_IMAGE)
provider_runtime=$(read_pin build/execution-provider/Dockerfile RUNTIME_IMAGE)
workload_runtime=$(read_pin build/execution-workload/Dockerfile RUNTIME_IMAGE)
check_image provider/GO_IMAGE "$provider_go"
check_image workload/GO_IMAGE "$workload_go"
check_image provider/RUNTIME_IMAGE "$provider_runtime"
check_image workload/RUNTIME_IMAGE "$workload_runtime"
if [ "$provider_go" != "$workload_go" ]; then
  echo "execution images must use the same Go builder pin" >&2
  exit 1
fi
check_platforms GO_IMAGE "$provider_go"
check_platforms provider/RUNTIME_IMAGE "$provider_runtime"
check_platforms workload/RUNTIME_IMAGE "$workload_runtime"
