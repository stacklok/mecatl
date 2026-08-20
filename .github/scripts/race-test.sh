#!/usr/bin/env bash
# Run one race-test command normally, or capture Go's JSONL stream for a manually
# dispatched timing diagnostic while preserving human-readable test output.
set -euo pipefail

if [[ "$#" -lt 2 ]]; then
  printf 'usage: race-test.sh LABEL GO_TEST_ARGUMENT...\n' >&2
  exit 2
fi

label="$1"
shift

race_args=(-race)
if [[ "${RACE_VET_OFF:-false}" == true ]]; then
  race_args+=(-vet=off)
fi

if [[ "${RACE_TIMING:-false}" != true ]]; then
  exec go test "${race_args[@]}" "$@"
fi

if [[ ! "$label" =~ ^[a-z0-9][a-z0-9-]*$ ]]; then
  printf 'race timing: invalid label: %s\n' "$label" >&2
  exit 2
fi
if ! command -v jq >/dev/null 2>&1; then
  printf 'race timing: jq is required in diagnostic mode\n' >&2
  exit 2
fi

workspace="${GITHUB_WORKSPACE:-$(pwd)}"
out_dir="$workspace/.scratch/race-timing"
out_file="$out_dir/$label.jsonl"
mkdir -p "$out_dir"

printf 'race timing: capturing %s\n' "$out_file"
set +e
go test "${race_args[@]}" -json "$@" >"$out_file"
test_status=$?
set -e

# Go's JSON protocol is stdout-only. Keep stderr separate so an ordinary compiler
# or toolchain diagnostic cannot corrupt the timing artifact.
if ! jq --unbuffered -rj 'if .Action == "output" then .Output else empty end' "$out_file"; then
  if [[ "$test_status" -ne 0 ]]; then
    exit "$test_status"
  fi
  printf 'race timing: failed to render go test JSON output\n' >&2
  exit 1
fi

# The test status is authoritative even when its JSON output rendered cleanly.
if [[ "$test_status" -ne 0 ]]; then
  exit "$test_status"
fi
