#!/bin/sh
set -eu
umask 022

if [ "$#" -ne 1 ] || [ -z "$1" ]; then
  echo "usage: $0 SOURCE_BUILD_IDENTITY" >&2
  exit 2
fi
source_build_identity=$1
repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
case "$(uname -s)-$(uname -m)" in
  Linux-x86_64) platform=linux-amd64 ;;
  *) echo "microVM development releases require Linux amd64" >&2; exit 1 ;;
esac

"$repo_root/environment/microvm/e2e/prepare.sh"
prepared="$repo_root/.scratch/microvm-e2e/$platform"
output="$repo_root/.scratch/microvm-dev/$platform"
rm -rf "$output"
mkdir -p "$output"
bundle="$output/mecatl-microvm-development-$platform.tar.gz"
members="$output/bundle-members"
find "$prepared/package" -mindepth 1 -printf '%P\0' | LC_ALL=C sort -z >"$members"
tar --no-recursion -C "$prepared/package" --null --verbatim-files-from --files-from="$members" -cf - | gzip -n >"$bundle"
rm -f "$members"
chmod 0600 "$bundle"
key="$output/publisher.pub"
cp "$prepared/release-fixture/publisher.pub" "$key"
chmod 0600 "$key"

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}
bundle_sha=$(sha256_file "$bundle")
key_sha=$(sha256_file "$key")
descriptor="$output/release.json"
python3 - "$descriptor" "$source_build_identity" "$bundle" "$bundle_sha" "$key" "$key_sha" <<'PY'
import json, sys
path, source, bundle, bundle_sha, key, key_sha = sys.argv[1:]
value = {
    "schema": "mecatl-microvm-development-release/v1",
    "platform": "linux-amd64",
    "source_build_identity": source,
    "bundle_path": bundle,
    "bundle_sha256": bundle_sha,
    "public_key_path": key,
    "public_key_identity": "sha256:" + key_sha,
    "policy_revision": "development-" + source,
}
with open(path, "x", encoding="utf-8") as stream:
    json.dump(value, stream, separators=(",", ":"))
    stream.write("\n")
PY
chmod 0600 "$descriptor"
printf 'prepared unsupported local microVM development release: %s\n' "$descriptor"
