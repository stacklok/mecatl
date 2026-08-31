#!/usr/bin/env bash
# Produce and validate the root-module race-test package partition.
#
# Usage: root-race-packages.sh {all|ui|root-a|root-b|validate}
# Package discovery is always dynamic via `go list ./...`. root-a is the
# deliberately maintained expensive-package shard; root-b automatically gets
# every other non-UI root package, including newly added packages.
#
# To rebalance, dispatch ci.yml with race_timing=true at a fixed commit ref and
# inspect both race-timing-root-{a,b} JSONL artifacts. Compare package totals and
# slow tests, not only job elapsed time:
#   jq -c 'select((.Action == "pass" or .Action == "fail") and (.Elapsed != null)) | {Package, Test, Action, Elapsed}' *.jsonl
#   jq -s 'map(select(.Test != null)) | sort_by(.Elapsed) | reverse' *.jsonl
# Move packages in root_a_packages and update root-race-packages_test.sh in the
# same change. Run task test:root-race-partition, task test:race-root-a, and
# task test:race-root-b. Re-dispatch the same ref; use several same-ref runs when
# runner variance makes the result unclear.
set -euo pipefail

readonly ui_package='github.com/stacklok/mecatl/cmd/mecatui/ui'
readonly -a root_a_packages=(
  'github.com/stacklok/mecatl/docs/lint'
  'github.com/stacklok/mecatl/internal/adapter/acp'
  'github.com/stacklok/mecatl/internal/adapter/grpcdriver'
  'github.com/stacklok/mecatl/internal/adapter/mcp'
  'github.com/stacklok/mecatl/internal/adapter/mcp/jq'
  'github.com/stacklok/mecatl/internal/adapter/server'
  'github.com/stacklok/mecatl/internal/adapter/store/jsonlstore'
  'github.com/stacklok/mecatl/internal/adapter/redisstore'
  'github.com/stacklok/mecatl/internal/apicheck'
  'github.com/stacklok/mecatl/internal/app'
)

usage() {
  printf 'usage: %s {all|ui|root-a|root-b|validate}\n' "${0##*/}" >&2
  exit 2
}

[[ "$#" -eq 1 ]] || usage
mode="$1"
case "$mode" in
  all|ui|root-a|root-b|validate) ;;
  *) usage ;;
esac

if ! listed="$(go list ./...)"; then
  printf 'root race partition: go list ./... failed\n' >&2
  exit 1
fi
if [[ -z "$listed" ]]; then
  printf 'root race partition: go list ./... returned no packages\n' >&2
  exit 1
fi

mapfile -t all_packages < <(printf '%s\n' "$listed" | LC_ALL=C sort)
declare -A discovered=()
for package in "${all_packages[@]}"; do
  if [[ -z "$package" ]]; then
    printf 'root race partition: go list ./... returned an empty package path\n' >&2
    exit 1
  fi
  if [[ -n "${discovered[$package]+present}" ]]; then
    printf 'root race partition: duplicate package in all set: %s\n' "$package" >&2
    exit 1
  fi
  discovered["$package"]=1
done

if [[ -z "${discovered[$ui_package]+present}" ]]; then
  printf 'root race partition: expected UI package exactly once: %s\n' "$ui_package" >&2
  exit 1
fi

ui_packages=("$ui_package")
declare -A root_a_seen=()
for package in "${root_a_packages[@]}"; do
  if [[ -n "${root_a_seen[$package]+present}" ]]; then
    printf 'root race partition: duplicate root-a package: %s\n' "$package" >&2
    exit 1
  fi
  root_a_seen["$package"]=1
  if [[ "$package" == "$ui_package" ]]; then
    printf 'root race partition: UI package must not be in root-a: %s\n' "$package" >&2
    exit 1
  fi
  if [[ -z "${discovered[$package]+present}" ]]; then
    printf 'root race partition: root-a package is not in go list ./...: %s\n' "$package" >&2
    exit 1
  fi
done

root_b_packages=()
for package in "${all_packages[@]}"; do
  if [[ "$package" != "$ui_package" && -z "${root_a_seen[$package]+present}" ]]; then
    root_b_packages+=("$package")
  fi
done
if [[ "${#root_b_packages[@]}" -eq 0 ]]; then
  printf 'root race partition: root-b set is empty\n' >&2
  exit 1
fi

# Check the three sets independently so later edits cannot silently overlap or
# omit a package. This exact union also makes newly discovered packages fail
# validation unless they land automatically in root-b.
declare -A union=()
for group in ui_packages root_a_packages root_b_packages; do
  declare -n packages="$group"
  for package in "${packages[@]}"; do
    if [[ -n "${union[$package]+present}" ]]; then
      printf 'root race partition: group intersection at %s\n' "$package" >&2
      exit 1
    fi
    union["$package"]=1
  done
done
mapfile -t sorted_union < <(printf '%s\n' "${!union[@]}" | LC_ALL=C sort)
if [[ "${#sorted_union[@]}" -ne "${#all_packages[@]}" ]]; then
  printf 'root race partition: sorted union size differs from all set\n' >&2
  exit 1
fi
for i in "${!all_packages[@]}"; do
  if [[ "${sorted_union[$i]}" != "${all_packages[$i]}" ]]; then
    printf 'root race partition: sorted union differs from all set at %s\n' "${all_packages[$i]}" >&2
    exit 1
  fi
done

case "$mode" in
  all) printf '%s\n' "${all_packages[@]}" ;;
  ui) printf '%s\n' "${ui_packages[@]}" ;;
  root-a) printf '%s\n' "${root_a_packages[@]}" | LC_ALL=C sort ;;
  root-b) printf '%s\n' "${root_b_packages[@]}" ;;
  validate) ;;
esac
