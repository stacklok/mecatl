#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: $0 RELEASE_ASSET_DIR" >&2
  exit 2
fi
assets=$1
if [ ! -d "$assets" ]; then
  echo "microVM release asset directory does not exist: $assets" >&2
  exit 1
fi

found=false
for provenance in "$assets"/*.provenance.json; do
  if [ ! -f "$provenance" ]; then
    continue
  fi
  found=true
  bundle=${provenance%.json}.sigstore.json
  if [ -f "$bundle" ]; then
    if [ -n "${MICROVM_RELEASE_SIGNING_KEY:-}" ]; then
      echo "cannot verify a reused bundle while signing with a local test key: $bundle" >&2
      exit 1
    fi
    repo=${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required to verify a reused bundle}
    signing_ref=${SIGNING_REF:?SIGNING_REF is required to verify a reused bundle}
    identity="https://github.com/$repo/.github/workflows/release.yml@$signing_ref"
    cosign verify-blob --certificate-identity "$identity" \
      --certificate-oidc-issuer https://token.actions.githubusercontent.com \
      --bundle "$bundle" "$provenance" >/dev/null
    echo "Reusing verified signature bundle $(basename "$bundle")"
  elif [ -n "${MICROVM_RELEASE_SIGNING_KEY:-}" ]; then
    COSIGN_PASSWORD=${COSIGN_PASSWORD:-} cosign sign-blob --yes --key "$MICROVM_RELEASE_SIGNING_KEY" --bundle "$bundle" "$provenance"
  else
    cosign sign-blob --yes --bundle "$bundle" "$provenance"
  fi
done
if [ "$found" != true ]; then
  echo "microVM release has no provenance statements to sign: $assets" >&2
  exit 1
fi
