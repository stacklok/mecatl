#!/usr/bin/env bash
# Produce and validate the root-module race-test package partition.
#
# Usage: root-race-packages.sh {all|ui|complement|validate}
# Package discovery is always dynamic via `go list ./...`. Output modes print one
# sorted import path per line; validate prints nothing on success.
set -euo pipefail

readonly ui_package='github.com/stacklok/mecatl/cmd/mecatui/ui'

usage() {
  printf 'usage: %s {all|ui|complement|validate}\n' "${0##*/}" >&2
  exit 2
}

[[ "$#" -eq 1 ]] || usage
mode="$1"
case "$mode" in
  all|ui|complement|validate) ;;
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
declare -A seen=()
ui_count=0
for package in "${all_packages[@]}"; do
  if [[ -z "$package" ]]; then
    printf 'root race partition: go list ./... returned an empty package path\n' >&2
    exit 1
  fi
  if [[ -n "${seen[$package]+present}" ]]; then
    printf 'root race partition: duplicate package in all set: %s\n' "$package" >&2
    exit 1
  fi
  seen["$package"]=1
  if [[ "$package" == "$ui_package" ]]; then
    ui_count=$((ui_count + 1))
  fi
done

if [[ "$ui_count" -ne 1 ]]; then
  printf 'root race partition: expected UI package exactly once, found %d: %s\n' "$ui_count" "$ui_package" >&2
  exit 1
fi

ui_packages=("$ui_package")
complement_packages=()
for package in "${all_packages[@]}"; do
  if [[ "$package" != "$ui_package" ]]; then
    complement_packages+=("$package")
  fi
done
if [[ "${#complement_packages[@]}" -eq 0 ]]; then
  printf 'root race partition: complement set is empty\n' >&2
  exit 1
fi

# The sets are derived by exact equality above, but validate the partition
# independently so later edits cannot silently introduce overlap or omissions.
declare -A union=()
for package in "${ui_packages[@]}"; do
  union["$package"]=1
done
for package in "${complement_packages[@]}"; do
  if [[ -n "${union[$package]+present}" ]]; then
    printf 'root race partition: UI/complement intersection at %s\n' "$package" >&2
    exit 1
  fi
  union["$package"]=1
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
  all)
    printf '%s\n' "${all_packages[@]}"
    ;;
  ui)
    printf '%s\n' "${ui_packages[@]}"
    ;;
  complement)
    printf '%s\n' "${complement_packages[@]}"
    ;;
  validate)
    ;;
esac
