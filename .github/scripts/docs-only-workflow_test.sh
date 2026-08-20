#!/usr/bin/env bash
# Offline contract test for conservative docs-only workflow classification.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
workflow="$root/.github/workflows/ci.yml"
failures=0

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  failures=$((failures + 1))
}

require() {
  local text="$1" message="$2"
  if ! grep -Fq "$text" "$workflow"; then
    fail "$message"
  fi
}

require 'docs_only=false' 'classifier must start fail-closed'
require 'pull_request)' 'classifier must support pull-request comparisons'
require 'push)' 'classifier must support push comparisons'
require 'git diff --name-only --no-renames -z "$base" "$head" | bash .github/scripts/docs-only-changes.sh' 'classifier must use no-renames NUL-delimited paths'
require 'base=""' 'unknown events must clear the comparison base'
require 'head=""' 'unknown events must clear the comparison head'

if grep -Fq 'workflow_dispatch)' "$workflow"; then
  fail 'workflow_dispatch must fall through to full validation'
fi
if grep -Fq 'compare_base_sha' "$workflow" || grep -Fq 'compare_head_sha' "$workflow"; then
  fail 'manual dispatch must not accept comparison SHAs'
fi

if [[ "$failures" -ne 0 ]]; then
  exit 1
fi
printf 'docs-only workflow wiring: all checks passed\n'
