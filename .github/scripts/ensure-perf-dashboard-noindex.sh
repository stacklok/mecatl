#!/usr/bin/env bash
# Keep the generated performance dashboard out of search results.
set -euo pipefail

if [[ "$#" -ne 1 ]]; then
  echo "usage: $0 <dashboard-index.html>" >&2
  exit 2
fi

index_file="$1"
directive='    <meta name="robots" content="noindex, nofollow" />'
robots_pattern="name=[\"']robots[\"']"

if [[ ! -f "$index_file" ]]; then
  echo "dashboard index does not exist: $index_file" >&2
  exit 1
fi

robots_count="$(grep -Eic "$robots_pattern" "$index_file" || true)"
if grep -Fqx "$directive" "$index_file"; then
  if [[ "$robots_count" -ne 1 ]]; then
    echo "dashboard index contains multiple robots directives: $index_file" >&2
    exit 1
  fi
  exit 0
fi
if [[ "$robots_count" -ne 0 ]]; then
  echo "dashboard index contains an unexpected robots directive: $index_file" >&2
  exit 1
fi

head_count="$(grep -Ec '^[[:space:]]*<head>[[:space:]]*$' "$index_file" || true)"
if [[ "$head_count" -ne 1 ]]; then
  echo "dashboard index must contain exactly one <head> element: $index_file" >&2
  exit 1
fi

tmp="$(mktemp "${index_file}.tmp.XXXXXX")"
trap 'rm -f "$tmp"' EXIT
awk -v directive="$directive" '
  /^[[:space:]]*<head>[[:space:]]*$/ {
    print
    print directive
    next
  }
  { print }
' "$index_file" > "$tmp"
mv "$tmp" "$index_file"
trap - EXIT
