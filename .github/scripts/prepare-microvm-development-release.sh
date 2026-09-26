#!/bin/sh
set -eu
umask 022
export COPYFILE_DISABLE=1

if [ "$#" -ne 1 ] || [ -z "$1" ]; then
  echo "usage: $0 SOURCE_BUILD_IDENTITY" >&2
  exit 2
fi
source_build_identity=$1
repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
case "$(uname -s)-$(uname -m)" in
  Linux-x86_64) platform=linux-amd64 ;;
  Darwin-arm64) platform=darwin-arm64 ;;
  *) echo "microVM development releases require Linux amd64 or Darwin arm64" >&2; exit 1 ;;
esac

"$repo_root/environment/microvm/e2e/prepare.sh"
prepared="$repo_root/.scratch/microvm-e2e/$platform"
output="$repo_root/.scratch/microvm-dev/$platform"
rm -rf "$output"
mkdir -p "$output"
bundle="$output/mecatl-microvm-development-$platform.tar.gz"
if ! python3 - "$prepared/package" "$bundle" <<'PY'
import gzip
import os
import sys
import tarfile
import tempfile

root, output = map(os.path.abspath, sys.argv[1:])
if not os.path.isdir(root):
    raise SystemExit(f"archive root is not a directory: {root}")

def traversal_error(error):
    raise error

def names():
    for current, dirs, files in os.walk(root, topdown=True, onerror=traversal_error, followlinks=False):
        dirs.sort()
        files.sort()
        for name in sorted(dirs + files):
            yield os.path.relpath(os.path.join(current, name), root).replace(os.sep, "/")

fd, temporary = tempfile.mkstemp(prefix=".microvm-development-", dir=os.path.dirname(output))
os.close(fd)
try:
    with open(temporary, "wb") as stream:
        with gzip.GzipFile(filename="", mode="wb", fileobj=stream, mtime=0) as compressed:
            with tarfile.open(fileobj=compressed, mode="w", format=tarfile.PAX_FORMAT) as archive:
                for name in names():
                    source = os.path.join(root, *name.split("/"))
                    info = archive.gettarinfo(source, arcname=name)
                    if not (info.isdir() or info.isreg() or info.issym()):
                        raise SystemExit(f"unsupported archive member type: {name}")
                    info.pax_headers = {}
                    if info.isreg():
                        with open(source, "rb") as member:
                            archive.addfile(info, member)
                    else:
                        archive.addfile(info)
    os.replace(temporary, output)
except BaseException:
    try:
        os.unlink(temporary)
    except FileNotFoundError:
        pass
    raise
PY
then
  exit 1
fi
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
python3 - "$descriptor" "$source_build_identity" "$bundle" "$bundle_sha" "$key" "$key_sha" "$platform" <<'PY'
import json, sys
path, source, bundle, bundle_sha, key, key_sha, platform = sys.argv[1:]
value = {
    "schema": "mecatl-microvm-development-release/v1",
    "platform": platform,
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
