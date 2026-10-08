#!/bin/sh
set -eu

if [ "$#" -ne 2 ]; then
  echo "usage: $0 RELEASE_MANIFEST INSTALL_ROOT" >&2
  exit 2
fi
manifest=$1
install=$2
case "$manifest" in /*) ;; *) manifest=$(CDPATH= cd -- "$(dirname -- "$manifest")" && pwd)/$(basename -- "$manifest") ;; esac
assets=$(dirname -- "$manifest")
manifest_name=$(basename -- "$manifest")
case "$manifest_name" in
  microvm-release-linux-amd64.json) platform=linux-amd64 ;;
  microvm-release-linux-arm64.json) platform=linux-arm64 ;;
  microvm-release-darwin-arm64.json) platform=darwin-arm64 ;;
  *) echo "unsupported microVM release manifest name: $manifest_name" >&2; exit 2 ;;
esac
digest_tool="$assets/mecatl-artifact-digest-$platform"
test -f "$digest_tool" && test -x "$digest_tool" && test ! -L "$digest_tool"
mkdir -p "$install/artifacts"
install=$(CDPATH= cd -- "$install" && pwd)
records="$install/.admission-artifacts"

python3 - "$manifest" >"$records" <<'PY'
import json, os, re, sys
with open(sys.argv[1], encoding="utf-8") as stream:
    manifest = json.load(stream)
if manifest.get("schema") != "mecatl-microvm-release/v2":
    raise SystemExit("unsupported microVM release manifest schema")
artifacts = manifest.get("admission_artifacts")
if not isinstance(artifacts, list) or {a.get("kind") for a in artifacts} != {"runtime", "firmware", "execution-image", "guest-agent"}:
    raise SystemExit("release manifest must contain exactly all four admission artifact kinds")
for artifact in artifacts:
    kind = artifact.get("kind", "")
    reference = artifact.get("reference", "")
    digest = artifact.get("digest", "")
    provenance = artifact.get("provenance", "")
    bundle = artifact.get("sigstore_bundle", "")
    if any(not isinstance(v, str) or not v for v in (kind, reference, digest, provenance, bundle)):
        raise SystemExit(f"incomplete {kind or 'unknown'} admission artifact")
    if any(os.path.basename(v) != v for v in (provenance, bundle)):
        raise SystemExit(f"unsafe release asset name for {kind}")
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
        raise SystemExit(f"invalid tree digest for {kind}")
    if kind == "execution-image":
        payload = artifact.get("payload", "")
        payload_digest = artifact.get("payload_digest", "")
        manifest_digest = artifact.get("manifest_digest", "")
        discovery_reference = artifact.get("discovery_reference", "")
        resolution_evidence = artifact.get("resolution_evidence", "")
        platform = artifact.get("platform", "")
        if payload or payload_digest:
            raise SystemExit("OCI execution-image must not require a local payload")
        if not re.fullmatch(r"[a-z0-9][a-z0-9._/-]*@sha256:[0-9a-f]{64}", reference):
            raise SystemExit("execution-image reference must be canonical repo@sha256, never a tag")
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", manifest_digest) or not reference.endswith("@" + manifest_digest):
            raise SystemExit("execution-image manifest digest does not match its OCI reference")
        if discovery_reference != "ghcr.io/stacklok/brood-box/base:latest":
            raise SystemExit("execution-image discovery reference is not the controlled Brood base latest")
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", resolution_evidence):
            raise SystemExit("execution-image resolution evidence is invalid")
        if platform not in ("linux/amd64", "linux/arm64"):
            raise SystemExit("execution-image platform is invalid")
        payload = payload_digest = "-"
    else:
        payload = artifact.get("payload", "")
        payload_digest = artifact.get("payload_digest", "")
        manifest_digest = "-"
        if not isinstance(payload, str) or not payload or os.path.basename(payload) != payload:
            raise SystemExit(f"unsafe release payload for {kind}")
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", payload_digest):
            raise SystemExit(f"invalid payload digest for {kind}")
        if not reference.endswith("@" + digest):
            raise SystemExit(f"mutable reference for {kind}")
    print("\t".join((kind, payload, payload_digest, reference, digest, manifest_digest, provenance, bundle)))
PY

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

while IFS="	" read -r kind payload payload_digest reference digest manifest_digest provenance bundle; do
  if [ "$payload" = "-" ]; then
    test "$kind" = execution-image
    continue
  fi
  archive="$assets/$payload"
  test "sha256:$(sha256_file "$archive")" = "$payload_digest"
  destination="$install/artifacts/$kind"
  rm -rf "$destination"
  mkdir -p "$destination"
  python3 - "$archive" "$destination" <<'PY'
import posixpath, sys, tarfile

with tarfile.open(sys.argv[1], "r:gz") as archive:
    members = archive.getmembers()
    symlinks = set()
    for member in members:
        name = posixpath.normpath(member.name)
        if posixpath.isabs(member.name) or name == ".." or name.startswith("../"):
            raise SystemExit(f"unsafe archive member: {member.name}")
        if not (member.isdir() or member.isfile() or member.issym() or member.islnk()):
            raise SystemExit(f"unsupported archive member type: {member.name}")
        if member.issym():
            symlinks.add(name)
        if member.islnk():
            target = posixpath.normpath(posixpath.join(posixpath.dirname(name), member.linkname))
            if posixpath.isabs(member.linkname) or target == ".." or target.startswith("../"):
                raise SystemExit(f"unsafe hard link: {member.name}")
    for member in members:
        name = posixpath.normpath(member.name)
        parent = posixpath.dirname(name)
        while parent not in ("", "."):
            if parent in symlinks:
                raise SystemExit(f"archive member traverses symlink: {member.name}")
            parent = posixpath.dirname(parent)
    # The release tree digest includes executable and special permission bits, so
    # filtering modes would change the signed subject. The complete preflight above
    # confines every write and prevents archive-order symlink traversal; extraction
    # can therefore preserve the exact packaged tree identity.
    archive.extractall(sys.argv[2], filter="fully_trusted")
PY
  actual=$("$digest_tool" "$destination")
  test "$actual" = "$digest"
done <"$records"

python3 - "$manifest" "$assets" "$install" >"$install/microvmd-artifacts.json" <<'PY'
import json, os, sys
with open(sys.argv[1], encoding="utf-8") as stream:
    manifest = json.load(stream)
assets, install = sys.argv[2:]
artifacts = []
for item in manifest["admission_artifacts"]:
    kind = item["kind"]
    is_oci = kind == "execution-image"
    artifacts.append({
        "kind": kind,
        "reference": item["reference"],
        "digest": item["digest"],
        "manifest_digest": item.get("manifest_digest", ""),
        "discovery_reference": item.get("discovery_reference", ""),
        "resolution_evidence": item.get("resolution_evidence", ""),
        "platform": item.get("platform", ""),
        "path": "" if is_oci else os.path.join(install, "artifacts", kind),
        "provenance": os.path.join(assets, item["provenance"]),
        "sigstore_bundle": os.path.join(assets, item["sigstore_bundle"]),
    })
json.dump({"schema": "mecatl-microvmd-artifacts/v1", "artifacts": artifacts}, sys.stdout, separators=(",", ":"))
PY
rm -f "$records"
