#!/bin/sh
set -eu

if [ "$#" -ne 3 ]; then
  echo "usage: $0 OUTPUT_DIR PLATFORM VERSION" >&2
  exit 2
fi

output=$1
platform=$2
version=$3
case "$platform" in
  linux-amd64) host_os=linux; arch=amd64; runtime_digest=4de717eba0c2fcfbce564fc4296536b78644b50772f3682c9be9e8809ec76165; firmware_digest=8c036287c6689bec9e8a01697a5b2f646d21bf6af276a7a02b04b83830461441 ;;
  linux-arm64) host_os=linux; arch=arm64; runtime_digest=2f1c9f0db4c549158f3b253d6b121f4a705ebfce08dad211ec1438838f064c73; firmware_digest=f7c1ccbc2a71de96883ccabbd5bff40ea1a553948365e102309a652326fbaf8f ;;
  darwin-arm64) host_os=darwin; arch=arm64; runtime_digest=c7442f2e6cd6916a5058432a4e2447622b4b509786bb1e636eda90f9dd2facce; firmware_digest=434e803ab08d84b525bb1addfeb8c590d3550d726141fe2c87d906df3e596ffa ;;
  *) echo "unsupported microVM release platform: $platform" >&2; exit 2 ;;
esac
case "$version" in
  *[!A-Za-z0-9._+-]*) echo "invalid release version" >&2; exit 2 ;;
esac

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
mkdir -p "$output"
output=$(CDPATH= cd -- "$output" && pwd)
microvmd="mecatl-microvmd-$platform"
guest="mecatl-guest-agent-$platform"
digest_tool="mecatl-artifact-digest-$platform"

if [ -n "${MICROVM_RELEASE_FIXTURE_DIR:-}" ]; then
  cp "$MICROVM_RELEASE_FIXTURE_DIR/mecatl-microvmd" "$output/$microvmd"
  cp "$MICROVM_RELEASE_FIXTURE_DIR/mecatl-guest-agent" "$output/$guest"
else
  (
    cd "$repo_root/environment/microvm"
    GOWORK=off CGO_ENABLED=0 GOOS="$host_os" GOARCH="$arch" \
      go build -trimpath -buildvcs=false -ldflags='-buildid=' -o "$output/$microvmd" ./cmd/mecatl-microvmd
    GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
      go build -trimpath -buildvcs=false -ldflags='-buildid=' -o "$output/$guest" ./cmd/mecatl-guest-agent
  )
fi
(
  cd "$repo_root/environment/microvm"
  GOWORK=off CGO_ENABLED=0 GOOS="$host_os" GOARCH="$arch" \
    go build -trimpath -buildvcs=false -ldflags='-buildid=' -o "$output/$digest_tool" ./cmd/mecatl-artifact-digest
)
chmod 0755 "$output/$microvmd" "$output/$guest" "$output/$digest_tool"
cp "$repo_root/.github/scripts/install-microvm-release.sh" "$output/install-microvm-release.sh"
chmod 0755 "$output/install-microvm-release.sh"

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
  curl --fail --location --proto '=https' --tlsv1.2 "$url" -o "$destination"
  actual=$(sha256_file "$destination")
  if [ "$actual" != "$expected" ]; then
    echo "artifact digest mismatch for $url: got $actual, want $expected" >&2
    exit 1
  fi
}

artifact_work="$output/.artifact-work"
rm -rf "$artifact_work"
mkdir -p "$artifact_work/runtime" "$artifact_work/firmware" "$artifact_work/guest-agent"
cp "$output/$guest" "$artifact_work/guest-agent/mecatl-guest-agent"
chmod 0755 "$artifact_work/guest-agent/mecatl-guest-agent"
if [ -n "${MICROVM_RELEASE_FIXTURE_DIR:-}" ]; then
  runtime_source=${MICROVM_RELEASE_RUNTIME_DIR:-$MICROVM_RELEASE_FIXTURE_DIR/runtime}
  firmware_source=${MICROVM_RELEASE_FIRMWARE_DIR:-$MICROVM_RELEASE_FIXTURE_DIR/firmware}
  cp -R "$runtime_source/." "$artifact_work/runtime/"
  cp -R "$firmware_source/." "$artifact_work/firmware/"
else
  download_verified "https://github.com/stacklok/go-microvm/releases/download/v0.0.40/go-microvm-runtime-$platform.tar.gz" "$artifact_work/runtime.tar.gz" "$runtime_digest"
  download_verified "https://github.com/stacklok/go-microvm/releases/download/v0.0.40/go-microvm-firmware-$platform.tar.gz" "$artifact_work/firmware.tar.gz" "$firmware_digest"
  tar -xzf "$artifact_work/runtime.tar.gz" -C "$artifact_work/runtime" --strip-components=1
  tar -xzf "$artifact_work/firmware.tar.gz" -C "$artifact_work/firmware" --strip-components=1
fi

execution_ref=${MICROVM_RELEASE_EXECUTION_IMAGE_REF:-}
execution_manifest=${MICROVM_RELEASE_EXECUTION_IMAGE_MANIFEST_DIGEST:-}
execution_tree_digest=${MICROVM_RELEASE_EXECUTION_IMAGE_TREE_DIGEST:-}
execution_discovery_ref=${MICROVM_RELEASE_EXECUTION_IMAGE_DISCOVERY_REFERENCE:-}
execution_resolution_evidence=${MICROVM_RELEASE_EXECUTION_IMAGE_RESOLUTION_EVIDENCE:-}
execution_platform=${MICROVM_RELEASE_EXECUTION_IMAGE_PLATFORM:-}
python3 - "$execution_ref" "$execution_manifest" "$execution_tree_digest" "$execution_discovery_ref" "$execution_resolution_evidence" "$execution_platform" <<'PY'
import re, sys
ref, manifest, tree, discovery_ref, resolution_evidence, platform = sys.argv[1:]
digest = r"sha256:[0-9a-f]{64}"
if not re.fullmatch(r"[a-z0-9][a-z0-9._/-]*@" + digest, ref):
    raise SystemExit("execution image must be an exact lowercase repo@sha256 platform reference")
if not re.fullmatch(digest, manifest) or ref.rsplit("@", 1)[1] != manifest:
    raise SystemExit("execution image manifest digest does not match its OCI reference")
if not re.fullmatch(digest, tree):
    raise SystemExit("execution image must carry its independently computed tree digest")
if discovery_ref != "ghcr.io/stacklok/brood-box/base:latest":
    raise SystemExit("execution image discovery must use the controlled Brood base latest reference")
if not re.fullmatch(digest, resolution_evidence):
    raise SystemExit("execution image must record immutable resolution evidence")
if platform not in ("linux/amd64", "linux/arm64"):
    raise SystemExit("execution image must record an admitted Brood platform")
PY

artifact_digest() {
  (cd "$repo_root/environment/microvm" && GOWORK=off go run ./cmd/mecatl-artifact-digest "$1")
}

write_artifact() {
  artifact_kind=$1
  artifact_tree=$2
  if [ "$artifact_kind" = guest-agent ]; then
    artifact_name="mecatl-guest-agent-artifact-$platform"
  else
    artifact_name="mecatl-$artifact_kind-$platform"
  fi
  artifact_tree_digest=$(artifact_digest "$artifact_tree")
  tar --sort=name --mtime="@${SOURCE_DATE_EPOCH:-0}" --owner=0 --group=0 --numeric-owner -C "$artifact_tree" -cf - . | gzip -n >"$output/$artifact_name.tar.gz"
  write_metadata "$artifact_name" "${artifact_tree_digest#sha256:}"
  printf '%s\t%s\t%s\n' "$artifact_kind" "$artifact_name" "$artifact_tree_digest"
}

microvmd_digest=$(sha256_file "$output/$microvmd")
guest_digest=$(sha256_file "$output/$guest")
digest_tool_digest=$(sha256_file "$output/$digest_tool")
printf '%s  %s\n%s  %s\n%s  %s\n' "$guest_digest" "$guest" "$microvmd_digest" "$microvmd" "$digest_tool_digest" "$digest_tool" >"$output/SHA256SUMS-$platform"

write_metadata() {
  metadata_name=$1
  metadata_digest=$2
  cat >"$output/$metadata_name.provenance.json" <<EOF
{"_type":"https://in-toto.io/Statement/v1","subject":[{"name":"$metadata_name","digest":{"sha256":"$metadata_digest"}}],"predicateType":"https://slsa.dev/provenance/v1","predicate":{"buildDefinition":{"buildType":"https://github.com/stacklok/mecatl/microvm-release/v1","externalParameters":{"platform":"$platform","version":"$version"},"resolvedDependencies":[{"uri":"pkg:golang/github.com/stacklok/go-microvm@v0.0.40"}]},"runDetails":{"builder":{"id":"https://github.com/stacklok/mecatl/.github/workflows/release.yml"}}}}
EOF
}
write_metadata "$microvmd" "$microvmd_digest"
write_metadata "$guest" "$guest_digest"
write_metadata "$digest_tool" "$digest_tool_digest"

runtime_record=$(write_artifact runtime "$artifact_work/runtime")
firmware_record=$(write_artifact firmware "$artifact_work/firmware")
guest_agent_record=$(write_artifact guest-agent "$artifact_work/guest-agent")
image_name="mecatl-execution-image-$platform"
cat >"$output/$image_name.provenance.json" <<EOF
{"_type":"https://in-toto.io/Statement/v1","subject":[{"name":"$image_name","digest":{"sha256":"${execution_tree_digest#sha256:}"}}],"predicateType":"https://slsa.dev/provenance/v1","predicate":{"buildDefinition":{"buildType":"https://github.com/stacklok/mecatl/microvm-release/v1","externalParameters":{"platform":"$platform","version":"$version"},"resolvedDependencies":[{"uri":"pkg:golang/github.com/stacklok/go-microvm@v0.0.40"},{"uri":"$execution_discovery_ref","digest":{"sha256":"${execution_manifest#sha256:}","resolutionEvidence":"${execution_resolution_evidence#sha256:}"},"platform":"$execution_platform"}]},"runDetails":{"builder":{"id":"https://github.com/stacklok/mecatl/.github/workflows/release.yml"}}}}
EOF
runtime_name=$(printf '%s' "$runtime_record" | cut -f2)
firmware_name=$(printf '%s' "$firmware_record" | cut -f2)
guest_agent_name=$(printf '%s' "$guest_agent_record" | cut -f2)
runtime_tree_digest=$(printf '%s' "$runtime_record" | cut -f3)
firmware_tree_digest=$(printf '%s' "$firmware_record" | cut -f3)
guest_agent_tree_digest=$(printf '%s' "$guest_agent_record" | cut -f3)
runtime_payload_digest=$(sha256_file "$output/$runtime_name.tar.gz")
firmware_payload_digest=$(sha256_file "$output/$firmware_name.tar.gz")
guest_agent_payload_digest=$(sha256_file "$output/$guest_agent_name.tar.gz")
printf '%s  %s\n%s  %s\n%s  %s\n' "$runtime_payload_digest" "$runtime_name.tar.gz" "$firmware_payload_digest" "$firmware_name.tar.gz" "$guest_agent_payload_digest" "$guest_agent_name.tar.gz" >>"$output/SHA256SUMS-$platform"

cat >"$output/microvm-release-$platform.json" <<EOF
{"schema":"mecatl-microvm-release/v2","version":"$version","platform":"$platform","admission_artifacts":[{"kind":"runtime","payload":"$runtime_name.tar.gz","payload_digest":"sha256:$runtime_payload_digest","reference":"$runtime_name.tar.gz@$runtime_tree_digest","digest":"$runtime_tree_digest","provenance":"$runtime_name.provenance.json","sigstore_bundle":"$runtime_name.provenance.sigstore.json","upstream":"https://github.com/stacklok/go-microvm/releases/download/v0.0.40/go-microvm-runtime-$platform.tar.gz"},{"kind":"firmware","payload":"$firmware_name.tar.gz","payload_digest":"sha256:$firmware_payload_digest","reference":"$firmware_name.tar.gz@$firmware_tree_digest","digest":"$firmware_tree_digest","provenance":"$firmware_name.provenance.json","sigstore_bundle":"$firmware_name.provenance.sigstore.json","upstream":"https://github.com/stacklok/go-microvm/releases/download/v0.0.40/go-microvm-firmware-$platform.tar.gz"},{"kind":"guest-agent","payload":"$guest_agent_name.tar.gz","payload_digest":"sha256:$guest_agent_payload_digest","reference":"$guest_agent_name.tar.gz@$guest_agent_tree_digest","digest":"$guest_agent_tree_digest","provenance":"$guest_agent_name.provenance.json","sigstore_bundle":"$guest_agent_name.provenance.sigstore.json"},{"kind":"execution-image","reference":"$execution_ref","manifest_digest":"$execution_manifest","digest":"$execution_tree_digest","discovery_reference":"$execution_discovery_ref","resolution_evidence":"$execution_resolution_evidence","platform":"$execution_platform","provenance":"$image_name.provenance.json","sigstore_bundle":"$image_name.provenance.sigstore.json"}],"artifacts":[{"name":"$microvmd","digest":"sha256:$microvmd_digest","sbom":"$microvmd.spdx.json","evidence":"$microvmd.provenance.sigstore.json","provenance":"$microvmd.provenance.json"},{"name":"$guest","digest":"sha256:$guest_digest","sbom":"$guest.spdx.json","evidence":"$guest.provenance.sigstore.json","provenance":"$guest.provenance.json"},{"name":"$digest_tool","digest":"sha256:$digest_tool_digest","sbom":"$digest_tool.spdx.json","evidence":"$digest_tool.provenance.sigstore.json","provenance":"$digest_tool.provenance.json"}]}
EOF
rm -rf "$artifact_work"
