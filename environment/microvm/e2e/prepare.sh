#!/bin/sh
set -eu

release=v0.0.41
alpine_version=3.22.1
repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)

case "$(uname -s)-$(uname -m)" in
  Linux-x86_64) platform=linux-amd64; alpine_arch=x86_64; goarch=amd64 ;;
  Linux-aarch64|Linux-arm64) platform=linux-arm64; alpine_arch=aarch64; goarch=arm64 ;;
  Darwin-arm64)
    major=$(sw_vers -productVersion | cut -d. -f1)
    if [ "$major" -lt 15 ]; then
      echo "microVM E2E requires macOS 15 or newer" >&2
      exit 1
    fi
    platform=darwin-arm64; alpine_arch=aarch64; goarch=arm64
    ;;
  *)
    echo "unsupported microVM E2E platform: $(uname -s)/$(uname -m)" >&2
    exit 1
    ;;
esac

case "$platform" in
  linux-amd64)
    runtime_sha=01371a0149ca39065f73d0c78d480f40236714a314bf7bef8ea292bacbfdf483
    firmware_sha=df7fb76e31ccbd9709e04ccc1a9d33546ad07100e8c4650d90b939553dd20007
    rootfs_sha=0e5cc5702ad72a4e151f219976ba946d50161c3acce210ef3b122a529aba1270
    ;;
  linux-arm64)
    runtime_sha=4731386229166040b9b8eab54f38ecf27ce229c8c723e212354e271a5e684588
    firmware_sha=cf851c509aaa8eafc77c4df28947cd1bfaf083d8ee4ece9cc0f87c6451969f9d
    rootfs_sha=188416d41f9f0c9a6e9427b75149e43ccf3a89587b2d27c9ad506e7ffca78d1c
    ;;
  darwin-arm64)
    runtime_sha=2641838c11064cd9b896825eeee263aab35e6cca9a2d7b3cfddd649ae4c5125d
    firmware_sha=02ff1ca992c3b104cf6c4d0c2995fac5aa8646e7f7cafa0ee72b734b69fc5216
    rootfs_sha=188416d41f9f0c9a6e9427b75149e43ccf3a89587b2d27c9ad506e7ffca78d1c
    ;;
esac

state_root="$repo_root/.scratch/microvm-e2e"
mkdir -p "$state_root"
state_root_physical=$(CDPATH= cd -- "$state_root" && pwd -P)
if [ "$state_root_physical" != "$state_root" ]; then
  echo "refusing symlinked microVM E2E state root: $state_root -> $state_root_physical" >&2
  exit 1
fi
cache="$state_root/$platform"
if [ -L "$cache" ]; then
  echo "refusing symlinked microVM E2E state root: $cache" >&2
  exit 1
fi
mkdir -p "$cache"
cache_physical=$(CDPATH= cd -- "$cache" && pwd -P)
if [ "$cache_physical" != "$cache" ]; then
  echo "refusing microVM E2E platform state outside exact root: $cache -> $cache_physical" >&2
  exit 1
fi
downloads="$cache/downloads"
mkdir -p "$downloads"

cleanup_prepared_tree() {
  target=$1
  case "$target" in
    "$cache/runtime"|"$cache/firmware"|"$cache/rootfs"|"$cache/guest-agent") ;;
    *)
      echo "refusing to clean outside microVM E2E state root: $target" >&2
      exit 1
      ;;
  esac
  if rm -rf -- "$target" 2>/dev/null; then
    return
  fi
  if command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
    sudo -n rm -rf -- "$target"
    return
  fi
  echo "cannot clean microVM E2E state tree $target; remove its user-namespace-owned files or configure non-interactive sudo" >&2
  exit 1
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

download_verified() {
  url=$1
  destination=$2
  expected=$3
  if [ ! -f "$destination" ] || [ "$(sha256_file "$destination")" != "$expected" ]; then
    rm -f "$destination.tmp"
    curl --fail --location --proto '=https' --tlsv1.2 "$url" -o "$destination.tmp"
    actual=$(sha256_file "$destination.tmp")
    if [ "$actual" != "$expected" ]; then
      rm -f "$destination.tmp"
      echo "artifact digest mismatch for $url: got $actual, want $expected" >&2
      exit 1
    fi
    mv "$destination.tmp" "$destination"
  fi
}

runtime_archive="$downloads/go-microvm-runtime-$platform.tar.gz"
firmware_archive="$downloads/go-microvm-firmware-$platform.tar.gz"
rootfs_archive="$downloads/alpine-minirootfs-$alpine_version-$alpine_arch.tar.gz"
download_verified "https://github.com/stacklok/go-microvm/releases/download/$release/go-microvm-runtime-$platform.tar.gz" "$runtime_archive" "$runtime_sha"
download_verified "https://github.com/stacklok/go-microvm/releases/download/$release/go-microvm-firmware-$platform.tar.gz" "$firmware_archive" "$firmware_sha"
download_verified "https://dl-cdn.alpinelinux.org/alpine/v3.22/releases/$alpine_arch/alpine-minirootfs-$alpine_version-$alpine_arch.tar.gz" "$rootfs_archive" "$rootfs_sha"

cleanup_prepared_tree "$cache/runtime"
cleanup_prepared_tree "$cache/firmware"
cleanup_prepared_tree "$cache/rootfs"
cleanup_prepared_tree "$cache/guest-agent"
mkdir -p "$cache/runtime" "$cache/firmware" "$cache/rootfs" "$cache/guest-agent"
tar -xzf "$runtime_archive" -C "$cache/runtime" --strip-components=1
tar -xzf "$firmware_archive" -C "$cache/firmware" --strip-components=1
tar -xzf "$rootfs_archive" -C "$cache/rootfs"

# Install real guest Git from Alpine's signed repositories in a digest-pinned
# container. This works identically from Linux and Apple Silicon hosts while
# keeping package tooling out of the production guest.
alpine_builder='docker.io/library/alpine@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce'
docker run --rm --platform "linux/$goarch" \
  -v "$cache/rootfs:/target" "$alpine_builder" \
  sh -c 'apk --root /target --initdb --repositories-file /etc/apk/repositories add git ca-certificates curl >/dev/null'

(
  cd "$repo_root/environment/microvm"
  GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" go build -trimpath -o "$cache/guest-agent/mecatl-guest-agent" ./cmd/mecatl-guest-agent
)
chmod 0755 "$cache/guest-agent/mecatl-guest-agent"
printf '%s\n' "$release $runtime_sha $firmware_sha alpine-$alpine_version $rootfs_sha" >"$cache/VERIFIED"

fixture="$cache/release-fixture"
package="$cache/package"
oci="$cache/oci"
rm -rf "$fixture" "$package" "$oci"
mkdir -p "$fixture" "$oci/context"
(
  cd "$repo_root/environment/microvm"
  GOWORK=off go build -trimpath -o "$fixture/mecatl-microvmd" ./cmd/mecatl-microvmd
)
cp "$cache/guest-agent/mecatl-guest-agent" "$fixture/mecatl-guest-agent"

# Resolve Brood's mutable discovery reference, then consume only the selected
# immutable platform manifest. The downstream release statement endorses this
# resolution; no Brood rebuild or derived execution image is published.
discovery_ref=ghcr.io/stacklok/brood-box/base:latest
docker buildx imagetools inspect "$discovery_ref" --raw >"$oci/manifest.json"
resolution_evidence="sha256:$(sha256_file "$oci/manifest.json")"
image_manifest=$(python3 - "$oci/manifest.json" "$goarch" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as stream:
    manifest = json.load(stream)
matches = [item["digest"] for item in manifest.get("manifests", [])
           if item.get("platform", {}).get("os") == "linux"
           and item.get("platform", {}).get("architecture") == sys.argv[2]]
if len(matches) != 1:
    raise SystemExit("Brood base index did not contain one exact platform manifest")
print(matches[0])
PY
)
image_ref="ghcr.io/stacklok/brood-box/base@$image_manifest"
(
  cd "$repo_root/environment/microvm"
  GOWORK=off go run ./cmd/mecatl-oci-tree-digest "$image_ref" "$oci/resolver-cache"
) >"$oci/tree-digest"
image_tree_digest=$(cat "$oci/tree-digest")
MICROVM_RELEASE_FIXTURE_DIR="$fixture" \
MICROVM_RELEASE_RUNTIME_DIR="$cache/runtime" \
MICROVM_RELEASE_FIRMWARE_DIR="$cache/firmware" \
MICROVM_RELEASE_EXECUTION_IMAGE_REF="$image_ref" \
MICROVM_RELEASE_EXECUTION_IMAGE_MANIFEST_DIGEST="$image_manifest" \
MICROVM_RELEASE_EXECUTION_IMAGE_TREE_DIGEST="$image_tree_digest" \
MICROVM_RELEASE_EXECUTION_IMAGE_DISCOVERY_REFERENCE="$discovery_ref" \
MICROVM_RELEASE_EXECUTION_IMAGE_RESOLUTION_EVIDENCE="$resolution_evidence" \
MICROVM_RELEASE_EXECUTION_IMAGE_PLATFORM="linux/$goarch" \
SOURCE_DATE_EPOCH=0 \
  "$repo_root/.github/scripts/package-microvm-release.sh" "$package" "$platform" e2e
publisher_key="$fixture/publisher"
COSIGN_PASSWORD= cosign generate-key-pair --output-key-prefix "$publisher_key" >/dev/null
COSIGN_PASSWORD= MICROVM_RELEASE_SIGNING_KEY="$publisher_key.key" \
  "$repo_root/.github/scripts/sign-microvm-release-evidence.sh" "$package"
chmod 0600 "$publisher_key.pub"
rm -f "$publisher_key.key"
cleanup_prepared_tree "$cache/runtime"
cleanup_prepared_tree "$cache/firmware"
mkdir -p "$cache/runtime" "$cache/firmware"
tar -xzf "$package/mecatl-runtime-$platform.tar.gz" -C "$cache/runtime"
tar -xzf "$package/mecatl-firmware-$platform.tar.gz" -C "$cache/firmware"

printf 'prepared packaged, verified microVM E2E artifacts in %s\n' "$cache"
