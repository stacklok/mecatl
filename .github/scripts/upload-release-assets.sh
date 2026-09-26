#!/bin/sh
set -eu

if [ "$#" -lt 2 ]; then
  echo "usage: $0 <tag> <asset>..." >&2
  exit 2
fi

tag=$1
shift
repo=${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}
work=.scratch/release-upload-$$
trap 'rm -rf "$work"' EXIT HUP INT TERM
mkdir -p "$work"

remote_assets=$(gh release view "$tag" --repo "$repo" --json assets --jq '.assets[].name')

# Preflight every existing name before uploading anything, so a mismatch cannot
# leave a partially updated release.
for asset in "$@"; do
  name=$(basename "$asset")
  if printf '%s\n' "$remote_assets" | grep -Fx "$name" >/dev/null; then
    gh release download "$tag" --repo "$repo" --pattern "$name" --dir "$work" --clobber >/dev/null
    local_digest=$(sha256sum "$asset" | cut -d' ' -f1)
    remote_digest=$(sha256sum "$work/$name" | cut -d' ' -f1)
    if [ "$local_digest" != "$remote_digest" ]; then
      echo "refusing to replace release asset $name: published digest $remote_digest differs from candidate $local_digest" >&2
      exit 1
    fi
  fi
done

for asset in "$@"; do
  name=$(basename "$asset")
  if printf '%s\n' "$remote_assets" | grep -Fx "$name" >/dev/null; then
    echo "Reusing identical release asset $name"
  else
    gh release upload "$tag" "$asset" --repo "$repo"
  fi
done
