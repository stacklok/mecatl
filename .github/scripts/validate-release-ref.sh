#!/bin/sh
set -eu

if [ "$#" -ne 2 ]; then
  echo "usage: validate-release-ref.sh VERSION ACTUAL_REF" >&2
  exit 2
fi

version=$1
actual_ref=$2
expected_ref="refs/tags/${version}"

case "$version" in
  v*) ;;
  *)
    echo "release version must name a v* tag: ${version}" >&2
    exit 1
    ;;
esac

if [ "$actual_ref" != "$expected_ref" ]; then
  echo "release ref mismatch: got ${actual_ref}, want ${expected_ref}" >&2
  exit 1
fi

printf '%s\n' "$actual_ref"
