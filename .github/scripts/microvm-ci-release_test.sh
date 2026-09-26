#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
taskfile="$repo_root/Taskfile.yml"
ci="$repo_root/.github/workflows/ci.yml"
e2e="$repo_root/.github/workflows/microvm-e2e.yml"
release="$repo_root/.github/workflows/release.yml"
package="$repo_root/.github/scripts/package-microvm-release.sh"
prepare_dev="$repo_root/.github/scripts/prepare-microvm-development-release.sh"
sign="$repo_root/.github/scripts/sign-microvm-release-evidence.sh"
install="$repo_root/.github/scripts/install-microvm-release.sh"
validate_release_ref="$repo_root/.github/scripts/validate-release-ref.sh"
reuse_platform_assets="$repo_root/.github/scripts/reuse-platform-release-assets.sh"
goreleaser="$repo_root/.goreleaser.yaml"

require() {
  pattern=$1
  file=$2
  if ! grep -F -- "$pattern" "$file" >/dev/null; then
    echo "missing '$pattern' in ${file#"$repo_root/"}" >&2
    exit 1
  fi
}

forbid() {
  pattern=$1
  file=$2
  if grep -F -- "$pattern" "$file" >/dev/null; then
    echo "forbidden '$pattern' in ${file#"$repo_root/"}" >&2
    exit 1
  fi
}

# Release archive creation must stay portable across GNU and BSD hosts.
forbid 'find "$prepared/package" -mindepth 1 -printf' "$prepare_dev"
forbid 'sort -z' "$prepare_dev"
forbid 'tar --sort=name' "$package"
forbid 'tar --no-recursion' "$prepare_dev"
require 'export COPYFILE_DISABLE=1' "$package"
require 'export COPYFILE_DISABLE=1' "$prepare_dev"
require 'if not os.path.isdir(root):' "$package"
require 'if not os.path.isdir(root):' "$prepare_dev"
require 'onerror=traversal_error' "$package"
require 'onerror=traversal_error' "$prepare_dev"

# Ordinary PR CI must cross the nested-module boundary at every relevant gate.
require 'environment/microvm/go.sum' "$ci"
require 'cd environment/microvm && go build ./...' "$ci"
require '(cd environment/microvm && bash "$GITHUB_WORKSPACE/.github/scripts/race-test.sh" microvm ./...)' "$ci"
require 'cd environment/microvm && golangci-lint run --config ../../.golangci.yml' "$ci"
require 'name: MicroVM module (standalone, GOWORK=off)' "$ci"
require 'GOWORK: off' "$ci"

# Hosted KVM repair makes the daemon runner's primary group the device-owning
# kvm group. The libkrun user namespace maps only the daemon's real UID/GID and
# drops supplementary groups, so a UID ACL or supplementary membership alone
# cannot authorize every parent/child runner process. The wrong-peer probe uses
# an explicitly separate UID/GID.
require 'sudo usermod --append --groups kvm "$(id -un)"' "$e2e"
require 'sg kvm -c' "$e2e"
require 'MECATL_MICROVM_E2E_KVM_GID="$(getent group kvm | cut -d: -f3)"' "$e2e"
require 'export MECATL_MICROVM_E2E_KVM_GID' "$e2e"
forbid 'export MECATL_MICROVM_E2E_KVM_GID="$(' "$e2e"
require 'expected_kvm_gid=${MECATL_MICROVM_E2E_KVM_GID}' "$e2e"
require 'test \"\$(id -g)\" = \"${MECATL_MICROVM_E2E_KVM_GID}\"' "$e2e"
require 'MECATL_MICROVM_E2E_WRONG_PEER_UID: "65534"' "$e2e"

# Artifact preparation may need Docker but must not require KVM access before
# the explicit ACL refresh makes the device available to this runner.
preflight=$(awk '/^      - name: Explicit artifact preparation preflight$/{in_block=1; next} in_block && /^      - name: /{exit} in_block{print}' "$e2e")
refresh=$(awk '/^      - name: Refresh KVM access after artifact preparation$/{in_block=1; next} in_block && /^      - name: /{exit} in_block{print}' "$e2e")
printf '%s\n' "$preflight" | grep -Fx '          docker info >/dev/null' >/dev/null
printf '%s\n' "$preflight" | grep -Fx '          test -c /dev/kvm' >/dev/null
if printf '%s\n' "$preflight" | grep -E 'test -[rw] /dev/kvm|os\.open\("/dev/kvm"' >/dev/null; then
  echo 'KVM access was required before the ACL refresh' >&2
  exit 1
fi
printf '%s\n' "$refresh" | grep -Fx '          fd = os.open("/dev/kvm", os.O_RDWR | os.O_CLOEXEC)' >/dev/null
preflight_line=$(grep -n '^      - name: Explicit artifact preparation preflight$' "$e2e" | cut -d: -f1)
prepare_line=$(grep -n '^      - name: Prepare packaged microVM artifacts$' "$e2e" | cut -d: -f1)
refresh_line=$(grep -n '^      - name: Refresh KVM access after artifact preparation$' "$e2e" | cut -d: -f1)
cleanup_line=$(grep -n '^      - name: Reclaim preparation-only disk$' "$e2e" | cut -d: -f1)
live_line=$(grep -n '^      - name: Run pinned v0.0.41 real-hypervisor journey$' "$e2e" | cut -d: -f1)
test "$preflight_line" -lt "$prepare_line"
test "$prepare_line" -lt "$refresh_line"
test "$refresh_line" -lt "$cleanup_line"
test "$cleanup_line" -lt "$live_line"

# Preparation materializes an Alpine fixture and the Brood resolver cache only to
# package and sign evidence. The live tests intentionally admit Brood again, and
# task e2e:microvm gives each default test a fresh run root and artifact cache, so
# reclaim only those two preparation trees and Docker's image/build cache here.
cleanup=$(awk '/^      - name: Reclaim preparation-only disk$/{in_block=1; next} in_block && /^      - name: /{exit} in_block{print}' "$e2e")
printf '%s\n' "$cleanup" | grep -F '            .scratch/microvm-e2e/linux-amd64/rootfs \' >/dev/null
printf '%s\n' "$cleanup" | grep -Fx '            .scratch/microvm-e2e/linux-amd64/oci/resolver-cache' >/dev/null
printf '%s\n' "$cleanup" | grep -Fx '          docker builder prune --all --force' >/dev/null
printf '%s\n' "$cleanup" | grep -Fx '          docker image prune --all --force' >/dev/null
test "$(printf '%s\n' "$cleanup" | grep -Fc '.scratch/microvm-e2e/linux-amd64/')" -eq 2
for required in package runtime firmware guest-agent release-fixture VERIFIED oci/manifest.json oci/tree-digest; do
  if printf '%s\n' "$cleanup" | grep -F ".scratch/microvm-e2e/linux-amd64/$required" >/dev/null; then
    echo "cleanup deletes required prepared artifact: $required" >&2
    exit 1
  fi
done

# The default live gate runs each evidence journey in isolation and discards its
# large run root/cache only after success. A failing journey remains intact, and
# an explicit regex override retains the single-invocation operator behavior.
require "DEFAULT_LIVE_TEST_NAMES='TestMicroVMDefaultPlacementDailyHarnessJourney'" "$taskfile"
require 'if [ -n "${MECATL_MICROVM_E2E_TESTS:-}" ]; then' "$taskfile"
require 'run_live_tests "${MECATL_MICROVM_E2E_TESTS}" "${RUN_ROOT_BASE}"' "$taskfile"
require 'for LIVE_TEST in ${DEFAULT_LIVE_TEST_NAMES}; do' "$taskfile"
require 'TEST_RUN_ROOT="${RUN_ROOT_BASE}/${LIVE_TEST}"' "$taskfile"
require 'run_live_tests "^${LIVE_TEST}$" "${TEST_RUN_ROOT}"' "$taskfile"
require 'rm -rf "${TEST_RUN_ROOT}"' "$taskfile"
forbid 'LIVE_TESTS="${MECATL_MICROVM_E2E_TESTS:-' "$taskfile"

# create-release owns release creation in this workflow. The CLI publisher remains
# independent of microVM artifact production, but cannot race create-release.
create_release_section=$(awk '/^  create-release:/{on=1; next} on && /^  [a-zA-Z0-9_-]+:/{exit} on' "$release")
publish_microvm_needs=$(awk '/^  publish-microvm:/{on=1; next} on && /^  [a-zA-Z0-9_-]+:/{exit} on' "$release")
publish_cli_needs=$(awk '/^  publish-cli:/{on=1; next} on && /^  [a-zA-Z0-9_-]+:/{exit} on' "$release")
printf '%s\n' "$create_release_section" | grep -Fx '    needs: validate-release-ref' >/dev/null
printf '%s\n' "$publish_microvm_needs" | grep -Fx '    needs: [validate-release-ref, create-release, endorse-brood-resolution]' >/dev/null
printf '%s\n' "$publish_cli_needs" | grep -Fx '    needs: [guard, create-release]' >/dev/null
# Every publishing job must have an explicit or transitive dependency on the
# immutable-ref validator. This graph check catches a newly added publisher as
# well as a one-off dependency typo.
python3 - "$release" <<'PY'
import re
import sys

workflow = open(sys.argv[1], encoding="utf-8").read().splitlines()
jobs = {}
job_lines = {}
current = None
for line in workflow:
    match = re.fullmatch(r"  ([A-Za-z0-9_-]+):", line)
    if match:
        current = match.group(1)
        jobs[current] = []
        job_lines[current] = []
        continue
    if current is None:
        continue
    job_lines[current].append(line)
    match = re.fullmatch(r"    needs: (.+)", line)
    if not match:
        continue
    value = match.group(1).strip()
    if value.startswith("[") and value.endswith("]"):
        jobs[current] = [item.strip() for item in value[1:-1].split(",")]
    else:
        jobs[current] = [value]

def reaches_validation(job, seen=None):
    if job == "validate-release-ref":
        return True
    seen = set() if seen is None else seen
    if job in seen:
        return False
    seen.add(job)
    return any(reaches_validation(dep, seen) for dep in jobs.get(job, []))

for job, lines in job_lines.items():
    for producer in re.findall(r"needs\.([A-Za-z0-9_-]+)\.outputs\.", "\n".join(lines)):
        if producer not in jobs[job]:
            raise SystemExit(f"job {job} reads outputs without directly needing {producer}")

validation = "\n".join(job_lines["validate-release-ref"])
assert '    needs: guard' in validation
assert '          ref: ${{ github.sha }}' in validation
assert '        run: test "$(git rev-parse HEAD)" = "${GITHUB_SHA}"' in validation
assert '''      - name: Assert guard and immutable run commits agree
        env:
          RELEASE_COMMIT: ${{ needs.guard.outputs.commit }}
        run: test "$(git rev-parse HEAD)" = "${RELEASE_COMMIT}"''' in validation

for job in sorted(name for name in jobs if name == "publish" or name.startswith("publish-")):
    if not reaches_validation(job):
        raise SystemExit(f"publishing job {job} bypasses validate-release-ref")
PY
# Exercise the real guard and validator agreement step with divergent on-main
# commits; counting checkout producers alone cannot establish revision authority.
sh "$repo_root/.github/scripts/release-tag-guard_test.sh"
# GoReleaser uploads to the release created above without replacing its notes.
require 'mode: keep-existing' "$goreleaser"

# The PR-only hypervisor journey installs cosign for verification only; it does
# not sign or attest, so OIDC must not be available to it.
hypervisor_section=$(awk '/^  live-hypervisor:/{on=1; next} on && /^  [a-zA-Z0-9_-]+:/{exit} on' "$e2e")
printf '%s\n' "$hypervisor_section" | grep -Fx '      contents: read' >/dev/null
if printf '%s\n' "$hypervisor_section" | grep -F 'id-token: write' >/dev/null; then
  echo 'live-hypervisor grants unnecessary id-token: write' >&2
  exit 1
fi
if printf '%s\n' "$hypervisor_section" | grep -E 'cosign (sign|attest)' >/dev/null; then
  echo 'live-hypervisor performs keyless signing' >&2
  exit 1
fi

# The release job must package and publish the nested runtime payloads and evidence.
require 'package-microvm-release.sh' "$release"
require 'gh release create' "$release"
require 'needs: [validate-release-ref, create-release, endorse-brood-resolution]' "$release"
require 'needs: [validate-release-ref, create-release]' "$release"
require 'needs: [validate-release-ref, resolve-brood-base]' "$release"
require 'name: Validate immutable release tag ref' "$release"
require 'if [ "${GITHUB_REF}" != "${signing_ref}" ]; then' "$release"
validation_line=$(grep -n 'name: Require the run itself to use the requested tag ref' "$release" | head -n1 | cut -d: -f1)
validation_checkout_line=$(grep -n 'ref: ${{ github.sha }}' "$release" | head -n1 | cut -d: -f1)
test "$validation_line" -lt "$validation_checkout_line"
require 'signing_ref: ${{ steps.validate.outputs.signing_ref }}' "$release"
assemble_bundle=$(awk '/^      - name: Assemble versioned platform bootstrap bundle$/{in_block=1; next} in_block && /^      - name: /{exit} in_block{print}' "$release")
test "$(printf '%s\n' "$assemble_bundle" | grep -Fc 'signing_ref="${{ needs.validate-release-ref.outputs.signing_ref }}"')" -eq 1
require 'release.yml@${signing_ref}' "$release"
checkout_count=$(grep -c 'uses: actions/checkout@' "$release")
bound_checkout_count=$(grep -c 'ref: ${{ github.sha }}' "$release")
verified_commit_checkout_count=$(grep -c 'ref: .*needs.guard.outputs.commit' "$release")
trusted_guard_checkout_count=$(grep -c 'ref: refs/heads/main' "$release")
head_assertion_count=$(grep -c 'run: test "$(git rev-parse HEAD)" = "${GITHUB_SHA}"' "$release")
# The guard implementation is checked out once from protected main. Jobs that need
# tag history use the guard's verified commit; all other publishing checkouts are
# bound to the workflow SHA and immediately assert it.
test "$trusted_guard_checkout_count" -eq 1
test "$verified_commit_checkout_count" -eq 3
test "$checkout_count" -eq "$((bound_checkout_count + verified_commit_checkout_count + trusted_guard_checkout_count))"
test "$bound_checkout_count" -eq "$head_assertion_count"
if "$validate_release_ref" v1.2.3 refs/heads/main >/dev/null 2>&1; then
  echo 'branch-dispatched release tag input was accepted' >&2
  exit 1
fi
signing_ref=$("$validate_release_ref" v1.2.3 refs/tags/v1.2.3)
test "$signing_ref" = refs/tags/v1.2.3
require 'brood-endorsed-platforms' "$release"
forbid 'publish-guest-tools:' "$release"
forbid 'environment/microvm/images/guest-tools' "$release"
require 'docker buildx imagetools inspect' "$release"
require 'verify-brood-resolution.sh dist/brood/brood-base-index.json dist/brood/brood-platforms.json' "$release"
require 'brood-base-index.json' "$release"
require 'upload-release-assets.sh' "$release"
forbid '--clobber' "$release"
require 'manifest_digest' "$package"
require 'MICROVM_RELEASE_EXECUTION_IMAGE_REF' "$package"
require 'mecatl-microvm-${VERSION}-${PLATFORM}.tar.gz' "$release"
require 'mecatl-artifact-digest-$platform' "$package"
require 'mecatl-artifact-digest-${{ matrix.platform }}.spdx.json' "$release"
forbid 'go run ./cmd/mecatl-artifact-digest' "$install"
require 'MICROVM_RELEASE_DEFAULTS_B64' "$release"
require 'main.microVMReleaseDefaultsB64' "$repo_root/.ko.yaml"
require 'anchore/sbom-action@' "$release"
require 'sign-microvm-release-evidence.sh' "$release"
require 'reuse-platform-release-assets.sh' "$release"
upload_microvm=$(awk '/^      - name: Upload microVM release assets$/{in_block=1; next} in_block && /^      - name: /{exit} in_block{print}' "$release")
payload_upload_line=$(printf '%s\n' "$upload_microvm" | grep -n '"${assets\[@\]}"' | cut -d: -f1)
manifest_upload_line=$(printf '%s\n' "$upload_microvm" | grep -n '"${manifest}"' | cut -d: -f1)
completion_upload_line=$(printf '%s\n' "$upload_microvm" | grep -n '"${completion}"' | cut -d: -f1)
test "$payload_upload_line" -lt "$manifest_upload_line"
test "$manifest_upload_line" -lt "$completion_upload_line"

# Every release asset has one job-level producer. In particular, the microVM
# publisher must not recreate host binaries owned by publish-mecatui-host.
publish_microvm_section=$(awk '/^  publish-microvm:/{on=1} /^  resolve-brood-base:/{on=0} on' "$release")
publish_host_section=$(awk '/^  publish-mecatui-host:/{on=1} /^  publish-mecatui:/{on=0} on' "$release")
if printf '%s\n' "$publish_microvm_section" | grep -F '${binary_name}-${VERSION}-${platform}' >/dev/null; then
  echo 'host release asset has multiple producers' >&2
  exit 1
fi
printf '%s\n' "$publish_host_section" | grep -F '${binary_name}-${VERSION}-${platform}' >/dev/null
printf '%s\n' "$publish_host_section" | grep -F 'binary: [mecated, mecatui]' >/dev/null
printf '%s\n' "$publish_host_section" | grep -F 'main.microVMReleaseVersion' >/dev/null
printf '%s\n' "$publish_host_section" | grep -F 'main.microVMReleaseStampRequired=release' >/dev/null
require '[ "${PLATFORM}" != linux-amd64 ] && [ "${name}" = install-microvm-release.sh ]' "$release"

# Functional host-stamp/entrypoint contract: both real binaries consume the same
# defaults through their package-specific version symbol. Mecated exercises its offline
# administration entrypoint; mecatui proves the stamp reaches embedded app composition.
host_scratch="$repo_root/.scratch/microvm-host-entrypoint-test"
rm -rf "$host_scratch"
mkdir -p "$host_scratch/home" "$host_scratch/config" "$host_scratch/data" "$host_scratch/state" "$host_scratch/runtime"
host_version=v0.0.0-host-contract
host_platform=linux-amd64
host_defaults=$(printf '{"%s":{"version":"%s","platform":"%s","url":"https://example.invalid/microvm.tar.gz","sha256":"%064d","policy_revision":"contract","certificate_identity":"https://example.invalid/release.yml","oidc_issuer":"https://token.actions.githubusercontent.com"}}' "$host_platform" "$host_version" "$host_platform" 0 | base64 -w0)
# mecated owns lifecycle administration; exercise its real status entrypoint.
binary=mecated
version_symbol=main.microVMReleaseVersion
wrong_version_symbol=main.version
go build -trimpath -buildvcs=false \
  -ldflags="-X ${version_symbol}=${host_version} -X main.microVMReleaseDefaultsB64=${host_defaults} -X main.microVMReleaseStampRequired=release" \
  -o "$host_scratch/$binary" "./cmd/$binary"
HOME="$host_scratch/home" XDG_CONFIG_HOME="$host_scratch/config" XDG_DATA_HOME="$host_scratch/data" \
  XDG_STATE_HOME="$host_scratch/state" XDG_RUNTIME_DIR="$host_scratch/runtime" \
  "$host_scratch/$binary" microvm status --output json >"$host_scratch/$binary.json"

go build -trimpath -buildvcs=false \
  -ldflags="-X ${wrong_version_symbol}=${host_version} -X main.microVMReleaseDefaultsB64=${host_defaults} -X main.microVMReleaseStampRequired=release" \
  -o "$host_scratch/$binary-wrong-version" "./cmd/$binary"
if HOME="$host_scratch/home" XDG_CONFIG_HOME="$host_scratch/config" XDG_DATA_HOME="$host_scratch/data" \
  XDG_STATE_HOME="$host_scratch/state" XDG_RUNTIME_DIR="$host_scratch/runtime" \
  "$host_scratch/$binary-wrong-version" microvm status --output json >/dev/null 2>&1; then
  echo "$binary accepted defaults stamped through the wrong version symbol" >&2
  exit 1
fi

go build -trimpath -buildvcs=false \
  -ldflags="-X ${version_symbol}=${host_version} -X main.missingMicroVMReleaseDefaults=${host_defaults} -X main.microVMReleaseStampRequired=release" \
  -o "$host_scratch/$binary-missing-defaults" "./cmd/$binary"
if HOME="$host_scratch/home" XDG_CONFIG_HOME="$host_scratch/config" XDG_DATA_HOME="$host_scratch/data" \
  XDG_STATE_HOME="$host_scratch/state" XDG_RUNTIME_DIR="$host_scratch/runtime" \
  "$host_scratch/$binary-missing-defaults" microvm status --output json >/dev/null 2>&1; then
  echo "$binary accepted a missing defaults stamp symbol" >&2
  exit 1
fi

# mecatui has no administration frontend. Verify its package-specific linker
# symbols feed embedded app composition without starting KVM or an interactive TUI.
go test -run '^TestMecatuiReleaseStampFeedsEmbeddedReadinessDefaults$' \
  -ldflags="-X main.version=${host_version} -X main.microVMReleaseDefaultsB64=${host_defaults} -X main.microVMReleaseStampRequired=release" \
  ./cmd/mecatui

expected_status=$(printf '{"backend":"microvm-local","configured":false,"running":false,"state":"unconfigured","error":"","remediation":"Not configured; run '\''mecated microvm doctor'\'' to check host readiness.","socket":"/tmp/mv-%s/microvmd.sock","guest_egress":"","generations":[],"continuation":""}\n' "$(id -u)")
test "$(cat "$host_scratch/mecated.json")" = "$expected_status"

scratch="$repo_root/.scratch/microvm-release-test"
rm -rf "$scratch"
mkdir -p "$scratch/fixture/runtime" "$scratch/fixture/firmware" "$scratch/fixture/execution-image/usr/local/bin" "$scratch/fixture/execution-image/bin" "$scratch/one" "$scratch/two"
printf 'microvmd-fixture\n' >"$scratch/fixture/mecatl-microvmd"
printf 'guest-agent-fixture\n' >"$scratch/fixture/mecatl-guest-agent"
printf 'runtime-fixture\n' >"$scratch/fixture/runtime/libkrun.so"
printf 'leading-dash-fixture\n' >"$scratch/fixture/runtime/-leading"
printf 'legitimate-dot-underscore-fixture\n' >"$scratch/fixture/runtime/._legitimate"
python3 - "$scratch/fixture/runtime" <<'PY'
import pathlib
import sys
pathlib.Path(sys.argv[1], "line\nbreak").write_text("newline-fixture\n", encoding="utf-8")
PY
printf 'firmware-fixture\n' >"$scratch/fixture/firmware/libkrunfw.so"
printf 'guest-agent-fixture\n' >"$scratch/fixture/execution-image/usr/local/bin/mecatl-guest-agent"
ln -s /bin/busybox "$scratch/fixture/execution-image/bin/arch"

fixture_manifest=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
fixture_tree=sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
fixture_ref=ghcr.io/stacklok/brood-box/base@${fixture_manifest}
discovery_ref=ghcr.io/stacklok/brood-box/base:latest
resolution_evidence=sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
execution_platform=linux/amd64
MICROVM_RELEASE_FIXTURE_DIR="$scratch/fixture" SOURCE_DATE_EPOCH=0 \
  MICROVM_RELEASE_EXECUTION_IMAGE_REF="$fixture_ref" \
  MICROVM_RELEASE_EXECUTION_IMAGE_MANIFEST_DIGEST="$fixture_manifest" \
  MICROVM_RELEASE_EXECUTION_IMAGE_TREE_DIGEST="$fixture_tree" \
  MICROVM_RELEASE_EXECUTION_IMAGE_DISCOVERY_REFERENCE="$discovery_ref" \
  MICROVM_RELEASE_EXECUTION_IMAGE_RESOLUTION_EVIDENCE="$resolution_evidence" \
  MICROVM_RELEASE_EXECUTION_IMAGE_PLATFORM="$execution_platform" \
  "$package" "$scratch/one" linux-amd64 v0.0.0-test
MICROVM_RELEASE_FIXTURE_DIR="$scratch/fixture" SOURCE_DATE_EPOCH=0 \
  MICROVM_RELEASE_EXECUTION_IMAGE_REF="$fixture_ref" \
  MICROVM_RELEASE_EXECUTION_IMAGE_MANIFEST_DIGEST="$fixture_manifest" \
  MICROVM_RELEASE_EXECUTION_IMAGE_TREE_DIGEST="$fixture_tree" \
  MICROVM_RELEASE_EXECUTION_IMAGE_DISCOVERY_REFERENCE="$discovery_ref" \
  MICROVM_RELEASE_EXECUTION_IMAGE_RESOLUTION_EVIDENCE="$resolution_evidence" \
  MICROVM_RELEASE_EXECUTION_IMAGE_PLATFORM="$execution_platform" \
  "$package" "$scratch/two" linux-amd64 v0.0.0-test

diff -ru "$scratch/one" "$scratch/two"
python3 - "$scratch/one/mecatl-runtime-linux-amd64.tar.gz" <<'PY'
import sys
import tarfile
with tarfile.open(sys.argv[1], "r:gz") as archive:
    members = archive.getmembers()
    names = {member.name for member in members}
    expected = {
        "-leading": b"leading-dash-fixture\n",
        "line\nbreak": b"newline-fixture\n",
        "._legitimate": b"legitimate-dot-underscore-fixture\n",
        "libkrun.so": b"runtime-fixture\n",
    }
    if not expected.keys() <= names:
        raise SystemExit(f"archive omitted fixture filenames: {expected.keys() - names}")
    for name, content in expected.items():
        member = archive.extractfile(name)
        if member is None or member.read() != content:
            raise SystemExit(f"archive changed fixture contents: {name!r}")
    if any(member.uid != 0 or member.gid != 0 or int(member.mtime) != 0 for member in members):
        raise SystemExit("archive metadata is not normalized")
PY
mkdir -p "$scratch/failure/mecatl-runtime-linux-amd64.tar.gz"
if MICROVM_RELEASE_FIXTURE_DIR="$scratch/fixture" SOURCE_DATE_EPOCH=0 \
  MICROVM_RELEASE_EXECUTION_IMAGE_REF="$fixture_ref" \
  MICROVM_RELEASE_EXECUTION_IMAGE_MANIFEST_DIGEST="$fixture_manifest" \
  MICROVM_RELEASE_EXECUTION_IMAGE_TREE_DIGEST="$fixture_tree" \
  MICROVM_RELEASE_EXECUTION_IMAGE_DISCOVERY_REFERENCE="$discovery_ref" \
  MICROVM_RELEASE_EXECUTION_IMAGE_RESOLUTION_EVIDENCE="$resolution_evidence" \
  MICROVM_RELEASE_EXECUTION_IMAGE_PLATFORM="$execution_platform" \
  "$package" "$scratch/failure" linux-amd64 v0.0.0-test >/dev/null 2>&1; then
  echo "package script swallowed archive creation failure" >&2
  exit 1
fi

test ! -e "$scratch/failure/microvm-release-linux-amd64.json"
missing_output="$scratch/missing-source"
if MICROVM_RELEASE_FIXTURE_DIR="$scratch/fixture" \
  MICROVM_RELEASE_RUNTIME_DIR="$scratch/does-not-exist" \
  MICROVM_RELEASE_EXECUTION_IMAGE_REF="$fixture_ref" \
  MICROVM_RELEASE_EXECUTION_IMAGE_MANIFEST_DIGEST="$fixture_manifest" \
  MICROVM_RELEASE_EXECUTION_IMAGE_TREE_DIGEST="$fixture_tree" \
  MICROVM_RELEASE_EXECUTION_IMAGE_DISCOVERY_REFERENCE="$discovery_ref" \
  MICROVM_RELEASE_EXECUTION_IMAGE_RESOLUTION_EVIDENCE="$resolution_evidence" \
  MICROVM_RELEASE_EXECUTION_IMAGE_PLATFORM="$execution_platform" \
  "$package" "$missing_output" linux-amd64 v0.0.0-test >/dev/null 2>&1; then
  echo "package script accepted a missing package source" >&2
  exit 1
fi
test ! -e "$missing_output/mecatl-runtime-linux-amd64.tar.gz"
test ! -e "$missing_output/microvm-release-linux-amd64.json"

if [ "$(id -u)" -ne 0 ]; then
  mkdir -p "$scratch/unreadable-source"
  printf 'unreadable\n' >"$scratch/unreadable-source/member"
  chmod 000 "$scratch/unreadable-source"
  unreadable_output="$scratch/unreadable-output"
  unreadable_succeeded=false
  if MICROVM_RELEASE_FIXTURE_DIR="$scratch/fixture" \
    MICROVM_RELEASE_RUNTIME_DIR="$scratch/unreadable-source" \
    MICROVM_RELEASE_EXECUTION_IMAGE_REF="$fixture_ref" \
    MICROVM_RELEASE_EXECUTION_IMAGE_MANIFEST_DIGEST="$fixture_manifest" \
    MICROVM_RELEASE_EXECUTION_IMAGE_TREE_DIGEST="$fixture_tree" \
    MICROVM_RELEASE_EXECUTION_IMAGE_DISCOVERY_REFERENCE="$discovery_ref" \
    MICROVM_RELEASE_EXECUTION_IMAGE_RESOLUTION_EVIDENCE="$resolution_evidence" \
    MICROVM_RELEASE_EXECUTION_IMAGE_PLATFORM="$execution_platform" \
    "$package" "$unreadable_output" linux-amd64 v0.0.0-test >/dev/null 2>&1; then
    unreadable_succeeded=true
  fi
  chmod 700 "$scratch/unreadable-source"
  if [ "$unreadable_succeeded" = true ]; then
    echo "package script accepted an unreadable package source" >&2
    exit 1
  fi
  test ! -e "$unreadable_output/mecatl-runtime-linux-amd64.tar.gz"
  test ! -e "$unreadable_output/microvm-release-linux-amd64.json"
fi
require 'go-microvm/releases/download/v0.0.41' "$scratch/one/microvm-release-linux-amd64.json"
for kind in runtime firmware; do
  require "\"kind\":\"$kind\"" "$scratch/one/microvm-release-linux-amd64.json"
  require "mecatl-$kind-linux-amd64.tar.gz" "$scratch/one/microvm-release-linux-amd64.json"
  require "mecatl-$kind-linux-amd64.provenance.json" "$scratch/one/microvm-release-linux-amd64.json"
  require "mecatl-$kind-linux-amd64.provenance.sigstore.json" "$scratch/one/microvm-release-linux-amd64.json"
  test -f "$scratch/one/mecatl-$kind-linux-amd64.tar.gz"
  test -f "$scratch/one/mecatl-$kind-linux-amd64.provenance.json"
done
require '"kind":"execution-image"' "$scratch/one/microvm-release-linux-amd64.json"
require "\"reference\":\"$fixture_ref\"" "$scratch/one/microvm-release-linux-amd64.json"
require "\"manifest_digest\":\"$fixture_manifest\"" "$scratch/one/microvm-release-linux-amd64.json"
require "\"digest\":\"$fixture_tree\"" "$scratch/one/microvm-release-linux-amd64.json"
require "\"discovery_reference\":\"$discovery_ref\"" "$scratch/one/microvm-release-linux-amd64.json"
require "\"resolution_evidence\":\"$resolution_evidence\"" "$scratch/one/microvm-release-linux-amd64.json"
require "\"platform\":\"$execution_platform\"" "$scratch/one/microvm-release-linux-amd64.json"
require "$discovery_ref" "$scratch/one/mecatl-execution-image-linux-amd64.provenance.json"
require "${fixture_manifest#sha256:}" "$scratch/one/mecatl-execution-image-linux-amd64.provenance.json"
forbid 'mecatl-execution-image-linux-amd64.tar.gz' "$scratch/one/microvm-release-linux-amd64.json"
test ! -e "$scratch/one/mecatl-execution-image-linux-amd64.tar.gz"
require 'mecatl-microvmd-linux-amd64.provenance.sigstore.json' "$scratch/one/microvm-release-linux-amd64.json"
require 'mecatl-microvmd-linux-amd64.spdx.json' "$scratch/one/microvm-release-linux-amd64.json"
require 'mecatl-microvmd-linux-amd64.provenance.json' "$scratch/one/microvm-release-linux-amd64.json"
require 'mecatl-artifact-digest-linux-amd64.spdx.json' "$scratch/one/microvm-release-linux-amd64.json"
require 'mecatl-artifact-digest-linux-amd64.provenance.json' "$scratch/one/microvm-release-linux-amd64.json"
require 'sha256:' "$scratch/one/microvm-release-linux-amd64.json"

# The publisher fixture uses the publisher's signing step once. Strict admission
# consumes those exact uploaded bundles; altered statements cannot reuse them.
COSIGN_PASSWORD= cosign generate-key-pair --output-key-prefix "$scratch/publisher" >/dev/null
COSIGN_PASSWORD= MICROVM_RELEASE_SIGNING_KEY="$scratch/publisher.key" "$sign" "$scratch/one"
for kind in runtime firmware execution-image; do
  statement="$scratch/one/mecatl-$kind-linux-amd64.provenance.json"
  bundle="$scratch/one/mecatl-$kind-linux-amd64.provenance.sigstore.json"
  cosign verify-blob --key "$scratch/publisher.pub" --bundle "$bundle" "$statement" >/dev/null
  cp "$statement" "$scratch/tampered.provenance.json"
  printf ' ' >>"$scratch/tampered.provenance.json"
  if cosign verify-blob --key "$scratch/publisher.pub" --bundle "$bundle" "$scratch/tampered.provenance.json" >/dev/null 2>&1; then
    echo "tampered $kind publisher evidence was accepted" >&2
    exit 1
  fi
done

# Packaged installation extracts the exact payloads and projects, rather than
# re-manufacturing, the evidence names consumed by strict microvmd admission.
mkdir -p "$scratch/install"
"$install" "$scratch/one/microvm-release-linux-amd64.json" "$scratch/install"
for kind in runtime firmware guest-agent; do
  test -d "$scratch/install/artifacts/$kind"
  if [ "$kind" = guest-agent ]; then
    evidence=mecatl-guest-agent-artifact-linux-amd64
    test -x "$scratch/install/artifacts/guest-agent/mecatl-guest-agent"
  else
    evidence="mecatl-$kind-linux-amd64"
  fi
  require "$evidence.provenance.json" "$scratch/install/microvmd-artifacts.json"
  require "$evidence.provenance.sigstore.json" "$scratch/install/microvmd-artifacts.json"
done
python3 - "$scratch/install/artifacts/runtime" <<'PY'
import pathlib
import sys
root = pathlib.Path(sys.argv[1])
expected = {
    "-leading": b"leading-dash-fixture\n",
    "line\nbreak": b"newline-fixture\n",
    "._legitimate": b"legitimate-dot-underscore-fixture\n",
}
for name, content in expected.items():
    path = root / name
    if path.read_bytes() != content:
        raise SystemExit(f"installed archive changed fixture contents: {name!r}")
PY
test ! -e "$scratch/install/artifacts/execution-image"
require "\"reference\":\"$fixture_ref\"" "$scratch/install/microvmd-artifacts.json"
require "\"manifest_digest\":\"$fixture_manifest\"" "$scratch/install/microvmd-artifacts.json"
require "\"discovery_reference\":\"$discovery_ref\"" "$scratch/install/microvmd-artifacts.json"
require "\"resolution_evidence\":\"$resolution_evidence\"" "$scratch/install/microvmd-artifacts.json"
require "\"platform\":\"$execution_platform\"" "$scratch/install/microvmd-artifacts.json"
require '"path":""' "$scratch/install/microvmd-artifacts.json"

# Mutable tags must fail before any execution-image projection is accepted.
python3 - "$scratch/one/microvm-release-linux-amd64.json" "$scratch/tagged.json" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as stream:
    manifest = json.load(stream)
for artifact in manifest["admission_artifacts"]:
    if artifact["kind"] == "execution-image":
        artifact["reference"] = "ghcr.io/stacklok/brood-box/base:latest"
with open(sys.argv[2], "w", encoding="utf-8") as stream:
    json.dump(manifest, stream, separators=(",", ":"))
PY
if "$install" "$scratch/tagged.json" "$scratch/tagged-install" >/dev/null 2>&1; then
  echo "installer accepted mutable execution-image tag" >&2
  exit 1
fi

python3 - "$scratch/one/microvm-release-linux-amd64.json" "$scratch/base-tagged.json" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as stream:
    manifest = json.load(stream)
for artifact in manifest["admission_artifacts"]:
    if artifact["kind"] == "execution-image":
        artifact["discovery_reference"] = "ghcr.io/attacker/base:latest"
with open(sys.argv[2], "w", encoding="utf-8") as stream:
    json.dump(manifest, stream, separators=(",", ":"))
PY
if "$install" "$scratch/base-tagged.json" "$scratch/base-tagged-install" >/dev/null 2>&1; then
  echo "installer accepted mutable Brood Box base tag" >&2
  exit 1
fi

# Every manifest subject must be the digest of the exact bytes an operator downloads.
while read -r digest name; do
  actual=$(sha256sum "$scratch/one/$name" | cut -d' ' -f1)
  test "$actual" = "$digest"
done <"$scratch/one/SHA256SUMS-linux-amd64"

# Every matrix cell owns uniquely named payload and checksum assets.
require 'SHA256SUMS-$platform' "$package"
require 'mecatl-guest-agent-$platform' "$package"

# Brood resolution is replayable from the raw index, and release publication is
# immutable across a rerun even if the mutable latest reference has moved.
verify_brood="$repo_root/.github/scripts/verify-brood-resolution.sh"
upload_assets="$repo_root/.github/scripts/upload-release-assets.sh"
mkdir -p "$scratch/bin" "$scratch/release" "$scratch/brood-one" "$scratch/brood-two"
cat >"$scratch/bin/gh" <<'SH'
#!/bin/sh
set -eu
test "$1" = release
case "$2" in
  view)
    for asset in "$RELEASE_STORE"/*; do
      [ ! -e "$asset" ] || basename "$asset"
    done
    true
    ;;
  upload)
    shift 3
    while [ "$#" -gt 0 ] && [ "$1" != --repo ]; do
      cp "$1" "$RELEASE_STORE/$(basename "$1")"
      shift
    done
    ;;
  download)
    shift 3
    patterns=
    dir=.
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --pattern) patterns="${patterns}${patterns:+ }$2"; shift 2 ;;
        --dir) dir=$2; shift 2 ;;
        --repo) shift 2 ;;
        --clobber) shift ;;
        *) shift ;;
      esac
    done
    mkdir -p "$dir"
    for name in $patterns; do cp "$RELEASE_STORE/$name" "$dir/$name"; done
    ;;
  *) exit 2 ;;
esac
SH
chmod +x "$scratch/bin/gh"

make_brood_resolution() {
  out=$1
  amd=$2
  arm=$3
  cat >"$out/brood-base-index.json" <<EOF
{"manifests":[{"digest":"sha256:$amd","platform":{"os":"linux","architecture":"amd64"}},{"digest":"sha256:$arm","platform":{"os":"linux","architecture":"arm64"}}]}
EOF
  evidence="sha256:$(sha256sum "$out/brood-base-index.json" | cut -d' ' -f1)"
  jq -n --arg amd "sha256:$amd" --arg arm "sha256:$arm" --arg evidence "$evidence" \
    '{amd64:{reference:("ghcr.io/stacklok/brood-box/base@"+$amd),manifest_digest:$amd,tree_digest:("sha256:"+("c"*64)),discovery_reference:"ghcr.io/stacklok/brood-box/base:latest",resolution_evidence:$evidence,platform:"linux/amd64"},arm64:{reference:("ghcr.io/stacklok/brood-box/base@"+$arm),manifest_digest:$arm,tree_digest:("sha256:"+("d"*64)),discovery_reference:"ghcr.io/stacklok/brood-box/base:latest",resolution_evidence:$evidence,platform:"linux/arm64"}}' \
    >"$out/brood-platforms.json"
}

make_brood_resolution "$scratch/brood-one" aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
make_brood_resolution "$scratch/brood-two" eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff
"$verify_brood" "$scratch/brood-one/brood-base-index.json" "$scratch/brood-one/brood-platforms.json"
"$verify_brood" "$scratch/brood-two/brood-base-index.json" "$scratch/brood-two/brood-platforms.json"
jq '.manifests += [.manifests[0]]' "$scratch/brood-one/brood-base-index.json" >"$scratch/duplicate-index.json"
if "$verify_brood" "$scratch/duplicate-index.json" "$scratch/brood-one/brood-platforms.json" >/dev/null 2>&1; then
  echo 'duplicate linux architecture in Brood index was accepted' >&2
  exit 1
fi
PATH="$scratch/bin:$PATH" RELEASE_STORE="$scratch/release" GITHUB_REPOSITORY=stacklok/mecatl \
  "$upload_assets" v0.0.0-test "$scratch/brood-one/brood-base-index.json" "$scratch/brood-one/brood-platforms.json"
first_index=$(sha256sum "$scratch/release/brood-base-index.json" | cut -d' ' -f1)
first_platforms=$(sha256sum "$scratch/release/brood-platforms.json" | cut -d' ' -f1)
PATH="$scratch/bin:$PATH" RELEASE_STORE="$scratch/release" GITHUB_REPOSITORY=stacklok/mecatl \
  "$upload_assets" v0.0.0-test "$scratch/brood-one/brood-base-index.json" "$scratch/brood-one/brood-platforms.json"
if PATH="$scratch/bin:$PATH" RELEASE_STORE="$scratch/release" GITHUB_REPOSITORY=stacklok/mecatl \
  "$upload_assets" v0.0.0-test "$scratch/brood-two/brood-base-index.json" "$scratch/brood-two/brood-platforms.json" >/dev/null 2>&1; then
  echo 'changed Brood latest resolution replaced frozen release evidence' >&2
  exit 1
fi
test "$(sha256sum "$scratch/release/brood-base-index.json" | cut -d' ' -f1)" = "$first_index"
test "$(sha256sum "$scratch/release/brood-platforms.json" | cut -d' ' -f1)" = "$first_platforms"

# A manual rerun reuses the complete platform set before generating fresh
# keyless bundles or a new bootstrap archive. Every downloaded byte must match
# the first run, while incomplete and internally inconsistent sets fail closed.
for name in mecatl-microvmd-linux-amd64 mecatl-guest-agent-linux-amd64 mecatl-artifact-digest-linux-amd64; do
  printf '{"name":"%s"}\n' "$name" >"$scratch/one/$name.spdx.json"
done
bootstrap=mecatl-microvm-v0.0.0-test-linux-amd64.tar.gz
(
  cd "$scratch/one"
  tar --sort=name --mtime='@0' --owner=0 --group=0 --numeric-owner --exclude="$bootstrap" -cf - . | gzip -n >"$bootstrap"
)
bootstrap_sha=$(sha256sum "$scratch/one/$bootstrap" | cut -d' ' -f1)
jq -n --arg version v0.0.0-test --arg platform linux-amd64 \
  --arg url "https://github.com/stacklok/mecatl/releases/download/v0.0.0-test/$bootstrap" \
  --arg sha256 "$bootstrap_sha" \
  '{version:$version,platform:$platform,url:$url,sha256:$sha256}' >"$scratch/one/microvm-default-linux-amd64.json"
rm -rf "$scratch/release"
mkdir -p "$scratch/release"
PATH="$scratch/bin:$PATH" RELEASE_STORE="$scratch/release" GITHUB_REPOSITORY=stacklok/mecatl \
  "$upload_assets" v0.0.0-test "$scratch/one"/*
(
  cd "$scratch/release"
  sha256sum * | sort >"$scratch/first-platform-digests"
)
cp -R "$scratch/one" "$scratch/generated-rerun"
for bundle in "$scratch/generated-rerun"/*.sigstore.json; do
  printf '{"different_keyless_bundle":true}\n' >"$bundle"
done
(
  cd "$scratch/generated-rerun"
  rm -f "$bootstrap" microvm-default-linux-amd64.json
  tar --sort=name --mtime='@0' --owner=0 --group=0 --numeric-owner --exclude="$bootstrap" -cf - . | gzip -n >"$bootstrap"
)
rerun_sha=$(sha256sum "$scratch/generated-rerun/$bootstrap" | cut -d' ' -f1)
jq -n --arg version v0.0.0-test --arg platform linux-amd64 \
  --arg url "https://github.com/stacklok/mecatl/releases/download/v0.0.0-test/$bootstrap" \
  --arg sha256 "$rerun_sha" \
  '{version:$version,platform:$platform,url:$url,sha256:$sha256}' >"$scratch/generated-rerun/microvm-default-linux-amd64.json"
cat >"$scratch/bin/cosign" <<'SH'
#!/bin/sh
exit 0
SH
chmod +x "$scratch/bin/cosign"
PATH="$scratch/bin:$PATH" RELEASE_STORE="$scratch/release" GITHUB_REPOSITORY=stacklok/mecatl \
  SIGNING_REF=refs/tags/v0.0.0-test \
  "$reuse_platform_assets" v0.0.0-test microvm linux-amd64 v0.0.0-test "$scratch/generated-rerun"
(
  cd "$scratch/generated-rerun"
  sha256sum * | sort >"$scratch/second-platform-digests"
)
cmp "$scratch/first-platform-digests" "$scratch/second-platform-digests"

# Simulate interruption before one payload and the final completion marker. The
# rerun downloads prior bytes, reconstructs candidates, verifies preserved
# Sigstore bundles against the reconstructed statements, and uploads only gaps.
rm -f "$scratch/release/mecatl-runtime-linux-amd64.tar.gz" \
  "$scratch/release/microvm-default-linux-amd64.json"
rm -rf "$scratch/incomplete"
mkdir -p "$scratch/incomplete"
: >"$scratch/reuse-output"
PATH="$scratch/bin:$PATH" RELEASE_STORE="$scratch/release" GITHUB_REPOSITORY=stacklok/mecatl \
  SIGNING_REF=refs/tags/v0.0.0-test GITHUB_OUTPUT="$scratch/reuse-output" \
  "$reuse_platform_assets" v0.0.0-test microvm linux-amd64 v0.0.0-test "$scratch/incomplete"
require 'reused=false' "$scratch/reuse-output"
require 'partial=true' "$scratch/reuse-output"
test -f "$scratch/incomplete/mecatl-microvmd-linux-amd64"
test ! -e "$scratch/incomplete/mecatl-runtime-linux-amd64.tar.gz"
test ! -e "$scratch/incomplete/microvm-default-linux-amd64.json"
for candidate in "$scratch/one"/*; do
  case "$candidate" in *.sigstore.json) continue ;; esac
  cp "$candidate" "$scratch/incomplete/$(basename "$candidate")"
done
: >"$scratch/cosign-calls"
cat >"$scratch/bin/cosign" <<'SH'
#!/bin/sh
set -eu
[ -z "${COSIGN_CALLS:-}" ] || printf '%s\n' "$*" >>"$COSIGN_CALLS"
case "$1" in verify-blob) exit 0 ;; *) exit 1 ;; esac
SH
chmod +x "$scratch/bin/cosign"
PATH="$scratch/bin:$PATH" COSIGN_CALLS="$scratch/cosign-calls" \
  GITHUB_REPOSITORY=stacklok/mecatl SIGNING_REF=refs/tags/v0.0.0-test \
  "$sign" "$scratch/incomplete"
require 'verify-blob --certificate-identity https://github.com/stacklok/mecatl/.github/workflows/release.yml@refs/tags/v0.0.0-test' "$scratch/cosign-calls"
PATH="$scratch/bin:$PATH" RELEASE_STORE="$scratch/release" GITHUB_REPOSITORY=stacklok/mecatl \
  "$upload_assets" v0.0.0-test "$scratch/incomplete"/*
test -f "$scratch/release/mecatl-runtime-linux-amd64.tar.gz"
test -f "$scratch/release/microvm-default-linux-amd64.json"

# A completion marker on an incomplete set is corruption, not resumable state.
rm -f "$scratch/release/mecatl-runtime-linux-amd64.tar.gz"
if PATH="$scratch/bin:$PATH" RELEASE_STORE="$scratch/release" GITHUB_REPOSITORY=stacklok/mecatl \
  SIGNING_REF=refs/tags/v0.0.0-test \
  "$reuse_platform_assets" v0.0.0-test microvm linux-amd64 v0.0.0-test "$scratch/completed-incomplete" >/dev/null 2>&1; then
  echo 'incomplete platform set with a completion marker was accepted' >&2
  exit 1
fi
cp "$scratch/one/mecatl-runtime-linux-amd64.tar.gz" "$scratch/release/"

# A partial rerun may proceed to candidate reconstruction, but the immutable
# uploader must reject any existing byte that differs from that candidate.
rm -f "$scratch/release/microvm-default-linux-amd64.json"
printf 'corrupt existing byte\n' >"$scratch/release/mecatl-microvmd-linux-amd64"
rm -rf "$scratch/mismatched"
PATH="$scratch/bin:$PATH" RELEASE_STORE="$scratch/release" GITHUB_REPOSITORY=stacklok/mecatl \
  SIGNING_REF=refs/tags/v0.0.0-test \
  "$reuse_platform_assets" v0.0.0-test microvm linux-amd64 v0.0.0-test "$scratch/mismatched"
cp "$scratch/one"/* "$scratch/mismatched/"
if PATH="$scratch/bin:$PATH" RELEASE_STORE="$scratch/release" GITHUB_REPOSITORY=stacklok/mecatl \
  "$upload_assets" v0.0.0-test "$scratch/mismatched"/* >/dev/null 2>&1; then
  echo 'mismatched partial platform asset was overwritten' >&2
  exit 1
fi
cp "$scratch/one/mecatl-microvmd-linux-amd64" "$scratch/release/"
cp "$scratch/one/microvm-default-linux-amd64.json" "$scratch/release/"

for kind in mecated mecatui; do
  host_first="$scratch/host-first-$kind"
  host_rerun="$scratch/host-rerun-$kind"
  rm -rf "$scratch/release" "$host_first" "$host_rerun"
  mkdir -p "$scratch/release" "$host_first" "$host_rerun"
  host_binary=$kind-v0.0.0-test-linux-amd64
  printf 'host-binary\n' >"$host_first/$host_binary"
  host_sha=$(sha256sum "$host_first/$host_binary" | cut -d' ' -f1)
  printf '%s  %s\n' "$host_sha" "$host_binary" >"$host_first/$host_binary.sha256"
  printf '{"sbom":true}\n' >"$host_first/$host_binary.spdx.json"
  printf '{"first_keyless_bundle":true}\n' >"$host_first/$host_binary.sigstore.json"
  PATH="$scratch/bin:$PATH" RELEASE_STORE="$scratch/release" GITHUB_REPOSITORY=stacklok/mecatl \
    "$upload_assets" v0.0.0-test "$host_first"/*
  printf '{"different_keyless_bundle":true}\n' >"$host_rerun/$host_binary.sigstore.json"
  PATH="$scratch/bin:$PATH" RELEASE_STORE="$scratch/release" GITHUB_REPOSITORY=stacklok/mecatl \
    SIGNING_REF=refs/tags/v0.0.0-test \
    "$reuse_platform_assets" v0.0.0-test "$kind" linux-amd64 v0.0.0-test "$host_rerun"
  (
    cd "$host_first"
    sha256sum * | sort >"$scratch/first-host-digests-$kind"
  )
  (
    cd "$host_rerun"
    sha256sum * | sort >"$scratch/second-host-digests-$kind"
  )
  cmp "$scratch/first-host-digests-$kind" "$scratch/second-host-digests-$kind"

  mv "$scratch/release/$host_binary.spdx.json" "$scratch/missing-host-asset"
  if PATH="$scratch/bin:$PATH" RELEASE_STORE="$scratch/release" GITHUB_REPOSITORY=stacklok/mecatl \
    SIGNING_REF=refs/tags/v0.0.0-test \
    "$reuse_platform_assets" v0.0.0-test "$kind" linux-amd64 v0.0.0-test "$scratch/incomplete-host-$kind" >/dev/null 2>&1; then
    echo "incomplete $kind host release asset set was reused" >&2
    exit 1
  fi
  mv "$scratch/missing-host-asset" "$scratch/release/$host_binary.spdx.json"
  printf '0%.0s' $(seq 1 64) >"$scratch/release/$host_binary.sha256"
  if PATH="$scratch/bin:$PATH" RELEASE_STORE="$scratch/release" GITHUB_REPOSITORY=stacklok/mecatl \
    SIGNING_REF=refs/tags/v0.0.0-test \
    "$reuse_platform_assets" v0.0.0-test "$kind" linux-amd64 v0.0.0-test "$scratch/mismatched-host-$kind" >/dev/null 2>&1; then
    echo "mismatched $kind host release asset set was reused" >&2
    exit 1
  fi
done

rm -rf "$scratch"
