#!/bin/sh
set -eu

if [ "$#" -ne 5 ]; then
  echo "usage: $0 TAG microvm|mecated|mecatui PLATFORM VERSION OUTPUT_DIR" >&2
  exit 2
fi

tag=$1
kind=$2
platform=$3
version=$4
output=$5
repo=${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}

case "$platform" in
  linux-amd64|linux-arm64|darwin-arm64) ;;
  *) echo "unsupported release platform: $platform" >&2; exit 2 ;;
esac
case "$version" in
  *[!A-Za-z0-9._+-]*) echo "invalid release version" >&2; exit 2 ;;
esac

case "$kind" in
  microvm)
    names="SHA256SUMS-$platform
mecatl-microvmd-$platform
mecatl-microvmd-$platform.spdx.json
mecatl-microvmd-$platform.provenance.json
mecatl-microvmd-$platform.provenance.sigstore.json
mecatl-guest-agent-$platform
mecatl-guest-agent-$platform.spdx.json
mecatl-guest-agent-$platform.provenance.json
mecatl-guest-agent-$platform.provenance.sigstore.json
mecatl-artifact-digest-$platform
mecatl-artifact-digest-$platform.spdx.json
mecatl-artifact-digest-$platform.provenance.json
mecatl-artifact-digest-$platform.provenance.sigstore.json
mecatl-runtime-$platform.tar.gz
mecatl-runtime-$platform.provenance.json
mecatl-runtime-$platform.provenance.sigstore.json
mecatl-firmware-$platform.tar.gz
mecatl-firmware-$platform.provenance.json
mecatl-firmware-$platform.provenance.sigstore.json
mecatl-guest-agent-artifact-$platform.tar.gz
mecatl-guest-agent-artifact-$platform.provenance.json
mecatl-guest-agent-artifact-$platform.provenance.sigstore.json
mecatl-execution-image-$platform.provenance.json
mecatl-execution-image-$platform.provenance.sigstore.json
microvm-release-$platform.json
mecatl-microvm-$version-$platform.tar.gz
microvm-default-$platform.json"
    if [ "$platform" = linux-amd64 ]; then
      names="install-microvm-release.sh
$names"
    fi
    ;;
  mecated|mecatui)
    binary="$kind-$version-$platform"
    names="$binary
$binary.sha256
$binary.spdx.json
$binary.sigstore.json"
    ;;
  *) echo "unsupported release asset set: $kind" >&2; exit 2 ;;
esac

remote=$(gh release view "$tag" --repo "$repo" --json assets --jq '.assets[].name')
found=0
total=0
for name in $names; do
  total=$((total + 1))
  if printf '%s\n' "$remote" | grep -Fx "$name" >/dev/null; then
    found=$((found + 1))
  fi
done

if [ "$found" -eq 0 ]; then
  mkdir -p "$output"
  [ -z "${GITHUB_OUTPUT:-}" ] || echo 'reused=false' >>"$GITHUB_OUTPUT"
  echo "No existing $kind asset set for $platform"
  exit 0
fi
if [ "$found" -ne "$total" ]; then
  if [ "$kind" != microvm ]; then
    echo "release contains an incomplete $kind asset set for $platform ($found of $total assets)" >&2
    exit 1
  fi
  completion="microvm-default-$platform.json"
  if printf '%s\n' "$remote" | grep -Fx "$completion" >/dev/null; then
    echo "release completion marker exists for an incomplete microVM asset set for $platform ($found of $total assets)" >&2
    exit 1
  fi
  rm -rf "$output"
  mkdir -p "$output"
  for name in $names; do
    if printf '%s\n' "$remote" | grep -Fx "$name" >/dev/null; then
      gh release download "$tag" --repo "$repo" --pattern "$name" --dir "$output" >/dev/null
    fi
  done
  [ -z "${GITHUB_OUTPUT:-}" ] || {
    echo 'reused=false' >>"$GITHUB_OUTPUT"
    echo 'partial=true' >>"$GITHUB_OUTPUT"
  }
  echo "Resuming incomplete microVM asset set for $platform ($found of $total assets); existing bytes will be checked against reconstructed candidates before upload"
  exit 0
fi

rm -rf "$output"
mkdir -p "$output"
for name in $names; do
  gh release download "$tag" --repo "$repo" --pattern "$name" --dir "$output" >/dev/null
done
if [ "$kind" = microvm ] && [ "$platform" != linux-amd64 ]; then
  if ! printf '%s\n' "$remote" | grep -Fx install-microvm-release.sh >/dev/null; then
    echo "release is missing the shared microVM installer asset" >&2
    exit 1
  fi
  gh release download "$tag" --repo "$repo" --pattern install-microvm-release.sh --dir "$output" >/dev/null
fi

python3 - "$kind" "$platform" "$version" "$repo" "$output" <<'PY'
import hashlib
import json
import pathlib
import re
import sys
import tarfile

kind, platform, version, repo, raw_output = sys.argv[1:]
output = pathlib.Path(raw_output)

def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()

def load(name):
    with (output / name).open(encoding="utf-8") as stream:
        return json.load(stream)

if kind in ("mecated", "mecatui"):
    binary = f"{kind}-{version}-{platform}"
    line = (output / f"{binary}.sha256").read_text(encoding="utf-8")
    if line != f"{digest(output / binary)}  {binary}\n":
        raise SystemExit(f"host {kind} checksum manifest does not match its binary")
    raise SystemExit(0)

checksums = output / f"SHA256SUMS-{platform}"
expected_checksum_names = {
    f"mecatl-microvmd-{platform}",
    f"mecatl-guest-agent-{platform}",
    f"mecatl-artifact-digest-{platform}",
    f"mecatl-runtime-{platform}.tar.gz",
    f"mecatl-firmware-{platform}.tar.gz",
    f"mecatl-guest-agent-artifact-{platform}.tar.gz",
}
seen = set()
for line in checksums.read_text(encoding="utf-8").splitlines():
    match = re.fullmatch(r"([0-9a-f]{64})  ([A-Za-z0-9._+-]+)", line)
    if not match or match.group(2) not in expected_checksum_names:
        raise SystemExit("invalid microVM checksum manifest")
    want, name = match.groups()
    if name in seen or digest(output / name) != want:
        raise SystemExit(f"microVM checksum mismatch for {name}")
    seen.add(name)
if seen != expected_checksum_names:
    raise SystemExit("microVM checksum manifest is incomplete")

manifest = load(f"microvm-release-{platform}.json")
if manifest.get("schema") != "mecatl-microvm-release/v2" or manifest.get("version") != version or manifest.get("platform") != platform:
    raise SystemExit("microVM release manifest identity mismatch")
artifacts = manifest.get("artifacts", [])
expected_artifact_names = {
    f"mecatl-microvmd-{platform}",
    f"mecatl-guest-agent-{platform}",
    f"mecatl-artifact-digest-{platform}",
}
if len(artifacts) != 3 or {artifact.get("name") for artifact in artifacts} != expected_artifact_names:
    raise SystemExit("microVM release manifest artifact set mismatch")
for artifact in artifacts:
    name = artifact.get("name", "")
    if name not in expected_checksum_names or artifact.get("digest") != f"sha256:{digest(output / name)}":
        raise SystemExit(f"microVM manifest digest mismatch for {name}")
    for key in ("sbom", "evidence", "provenance"):
        ref = artifact.get(key, "")
        if pathlib.PurePath(ref).name != ref or not (output / ref).is_file():
            raise SystemExit(f"invalid microVM manifest {key} relationship")
admission_artifacts = manifest.get("admission_artifacts", [])
if len(admission_artifacts) != 4 or {artifact.get("kind") for artifact in admission_artifacts} != {"runtime", "firmware", "execution-image", "guest-agent"}:
    raise SystemExit("microVM admission artifact set mismatch")
for artifact in admission_artifacts:
    for key in ("provenance", "sigstore_bundle"):
        ref = artifact.get(key, "")
        if pathlib.PurePath(ref).name != ref or not (output / ref).is_file():
            raise SystemExit(f"invalid admission {key} relationship")
    payload = artifact.get("payload")
    if payload:
        if payload not in expected_checksum_names or artifact.get("payload_digest") != f"sha256:{digest(output / payload)}":
            raise SystemExit(f"admission payload digest mismatch for {payload}")

bundle_name = f"mecatl-microvm-{version}-{platform}.tar.gz"
defaults = load(f"microvm-default-{platform}.json")
expected_url = f"https://github.com/{repo}/releases/download/{version}/{bundle_name}"
if defaults.get("version") != version or defaults.get("platform") != platform or defaults.get("url") != expected_url or defaults.get("sha256") != digest(output / bundle_name):
    raise SystemExit("microVM bootstrap defaults do not match the published archive")

excluded = {bundle_name, f"microvm-default-{platform}.json"}
expected_members = {path.name for path in output.iterdir() if path.is_file()} - excluded
with tarfile.open(output / bundle_name, "r:gz") as archive:
    members = archive.getmembers()
    actual_members = set()
    for member in members:
        name = member.name.removeprefix("./")
        if not name or member.isdir():
            continue
        if "/" in name or not member.isfile() or name in actual_members:
            raise SystemExit("unsafe or duplicate microVM bootstrap archive member")
        actual_members.add(name)
        stream = archive.extractfile(member)
        if stream is None or hashlib.sha256(stream.read()).hexdigest() != digest(output / name):
            raise SystemExit(f"bootstrap archive member mismatch for {name}")
if actual_members != expected_members:
    raise SystemExit("microVM bootstrap archive asset set mismatch")
PY

issuer=https://token.actions.githubusercontent.com
identity="https://github.com/$repo/.github/workflows/release.yml@${SIGNING_REF:?SIGNING_REF is required to verify existing signatures}"
case "$kind" in
  microvm)
    for statement in "$output"/*.provenance.json; do
      cosign verify-blob --certificate-identity "$identity" --certificate-oidc-issuer "$issuer" \
        --bundle "${statement%.json}.sigstore.json" "$statement" >/dev/null
    done
    ;;
  mecated|mecatui)
    cosign verify-blob --certificate-identity "$identity" --certificate-oidc-issuer "$issuer" \
      --bundle "$output/$binary.sigstore.json" "$output/$binary" >/dev/null
    ;;
esac

[ -z "${GITHUB_OUTPUT:-}" ] || echo 'reused=true' >>"$GITHUB_OUTPUT"
echo "Reusing complete verified $kind asset set for $platform"
