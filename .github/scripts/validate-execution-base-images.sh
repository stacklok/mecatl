#!/bin/sh
set -eu

check_image() {
  name=$1
  image=$2
  case "$image" in
    ?*@sha256:*) digest=${image##*@sha256:} ;;
    *) echo "$name must be digest-pinned" >&2; exit 1 ;;
  esac
  case "$digest" in
    *[!0-9a-f]*|'') echo "$name must have a lowercase SHA-256 digest" >&2; exit 1 ;;
  esac
  if [ "${#digest}" -ne 64 ]; then
    echo "$name must have a 64-character SHA-256 digest" >&2
    exit 1
  fi
}

check_image EXECUTION_GO_IMAGE "${GO_IMAGE:-}"
check_image EXECUTION_PROVIDER_RUNTIME_IMAGE "${PROVIDER_RUNTIME_IMAGE:-}"
