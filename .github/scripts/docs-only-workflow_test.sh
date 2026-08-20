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
require 'trusted_classifier="$RUNNER_TEMP/docs-only-changes.sh"' 'trusted classifier must be extracted outside the candidate checkout'
require 'if git show "$base:.github/scripts/docs-only-changes.sh" > "$trusted_classifier"; then' 'classifier extraction failure must remain fail-closed'
require 'git diff --name-only --no-renames -z "$base" "$head" | bash "$trusted_classifier")" || result=false' 'trusted classifier must receive no-renames NUL-delimited paths and fail closed'
require 'base=""' 'unknown events must clear the comparison base'
require 'head=""' 'unknown events must clear the comparison head'

if grep -Fq 'bash .github/scripts/docs-only-changes.sh' "$workflow" \
  || grep -Fq 'bash "$GITHUB_WORKSPACE/.github/scripts/docs-only-changes.sh"' "$workflow"; then
  fail 'candidate-checkout classifier must never execute for path classification'
fi

validate_line="$(grep -nF 'git cat-file -e "$head^{commit}"' "$workflow" | head -n 1 | cut -d: -f1 || true)"
extract_line="$(grep -nF 'git show "$base:.github/scripts/docs-only-changes.sh" > "$trusted_classifier"' "$workflow" | head -n 1 | cut -d: -f1 || true)"
if [[ -z "$validate_line" || -z "$extract_line" || "$extract_line" -le "$validate_line" ]]; then
  fail 'trusted classifier extraction must follow base/head commit validation'
fi

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
