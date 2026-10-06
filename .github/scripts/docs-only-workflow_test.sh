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
require 'compare_base="$base"' 'push comparisons must retain the event base'
require 'compare_base="$(git merge-base "$base" "$head")" || compare_base=""' 'pull-request comparisons must use the merge base and fail closed on error'
require 'if [[ -n "$compare_base" ]]; then' 'a missing comparison base must retain full validation'
require 'git diff --name-only --no-renames -z "$compare_base" "$head" | bash "$trusted_classifier")" || result=false' 'trusted classifier must receive merge-base-to-head no-renames NUL-delimited paths and fail closed'
require 'base=""' 'unknown events must clear the comparison base'
require 'head=""' 'unknown events must clear the comparison head'

changes_job="$(awk '
  $0 == "  changes:" { in_changes = 1; next }
  in_changes && /^  [a-zA-Z0-9_-]+:$/ { exit }
  in_changes { print }
' "$workflow")"
if ! grep -Fq 'fetch-depth: 100' <<<"$changes_job"; then
  fail 'changes job must fetch bounded history to compute PR merge bases'
fi
if ! grep -Fq 'git fetch --no-tags --depth=100 origin "$base" "$head"' <<<"$changes_job"; then
  fail 'classifier must fetch bounded base and head ancestry for merge-base comparisons'
fi

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
