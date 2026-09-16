#!/usr/bin/env bash
# Offline contract test for the Linux job-family cost gating in ci.yml. Each Go /
# SDK-frontend / Docusaurus-site job family must be gated on its path-relevance
# flag, the flags must be computed fail-closed from the trusted base-ref
# classifier, and the required-check aggregators (test, lint) must tolerate the
# legitimate skips those flags introduce — otherwise a non-Go PR is blocked.
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

# Print the single-line `if:` of a named job (from its `  <name>:` header down to
# the next top-level job header). Every gated job here uses a single-line `if:`.
job_if() {
  awk -v header="  $1:" '
    $0 == header { inj = 1; next }
    inj && /^  [a-zA-Z0-9_-]+:$/ { exit }
    inj && /^    if:/ { print; exit }
  ' "$workflow"
}

# Assert a job's `if:` contains (or, with "!", does not contain) a substring.
assert_if() {
  local job="$1" mode="$2" needle="$3" line
  line="$(job_if "$job")"
  if [[ -z "$line" ]]; then
    fail "job '$job' has no single-line if: to classify"
    return
  fi
  case "$mode" in
    has)
      if ! grep -Fq "$needle" <<<"$line"; then
        fail "job '$job' if: must contain \"$needle\" (got: $line)"
      fi
      ;;
    hasnot)
      if grep -Fq "$needle" <<<"$line"; then
        fail "job '$job' if: must NOT contain \"$needle\" (got: $line)"
      fi
      ;;
  esac
  return 0
}

# --- classifier script exists --------------------------------------------------
if [[ ! -f "$root/.github/scripts/relevant-changes.sh" ]]; then
  fail 'relevant-changes.sh must exist'
fi

# --- changes job wiring --------------------------------------------------------
require 'go_relevant=true' 'go_relevant must start fail-closed to RUN'
require 'sdk_relevant=true' 'sdk_relevant must start fail-closed to RUN'
require 'site_relevant=true' 'site_relevant must start fail-closed to RUN'
require 'go_relevant: ${{ steps.classify.outputs.go_relevant }}' 'go_relevant must be a changes-job output'
require 'sdk_relevant: ${{ steps.classify.outputs.sdk_relevant }}' 'sdk_relevant must be a changes-job output'
require 'site_relevant: ${{ steps.classify.outputs.site_relevant }}' 'site_relevant must be a changes-job output'
require 'relevant_classifier="$RUNNER_TEMP/relevant-changes.sh"' 'trusted classifier must be extracted outside the candidate checkout'
require 'if git show "$base:.github/scripts/relevant-changes.sh" > "$relevant_classifier"; then' 'classifier extraction must read the trusted base ref and stay fail-closed'
require 'bash "$relevant_classifier" go)" || go_relevant=true' 'go relevance must use no-renames NUL paths and fail closed to RUN'
require 'bash "$relevant_classifier" sdk)" || sdk_relevant=true' 'sdk relevance must use no-renames NUL paths and fail closed to RUN'
require 'bash "$relevant_classifier" site)" || site_relevant=true' 'site relevance must use no-renames NUL paths and fail closed to RUN'
require 'echo "go_relevant=$go_relevant"' 'go_relevant must be written to GITHUB_OUTPUT'
require 'echo "sdk_relevant=$sdk_relevant"' 'sdk_relevant must be written to GITHUB_OUTPUT'
require 'echo "site_relevant=$site_relevant"' 'site_relevant must be written to GITHUB_OUTPUT'

# The candidate checkout's classifier must never run (a PR could tamper with it).
if grep -Fq 'bash .github/scripts/relevant-changes.sh' "$workflow" \
  || grep -Fq 'bash "$GITHUB_WORKSPACE/.github/scripts/relevant-changes.sh"' "$workflow"; then
  fail 'candidate-checkout classifier must never execute for path classification'
fi

# Extraction must follow base/head commit validation (reuse of the docs-only gate).
validate_line="$(grep -nF 'git cat-file -e "$head^{commit}"' "$workflow" | head -n 1 | cut -d: -f1 || true)"
extract_line="$(grep -nF 'git show "$base:.github/scripts/relevant-changes.sh" > "$relevant_classifier"' "$workflow" | head -n 1 | cut -d: -f1 || true)"
if [[ -z "$validate_line" || -z "$extract_line" || "$extract_line" -le "$validate_line" ]]; then
  fail 'classifier extraction must follow base/head commit validation'
fi

# --- per-job gating ------------------------------------------------------------
# The Go build/test matrix gates on go_relevant.
for job in build analysis fuzz-smoke engine-standalone provider-standalone \
  api-compat vuln test-race-root-a test-race-root-b test-race-ui test-non-race-draft; do
  assert_if "$job" has "needs.changes.outputs.go_relevant == 'true'"
done

# The pure-TypeScript SDK unit job gates on sdk_relevant ALONE (no go, no
# docs_only — sdk_relevant already implies a non-docs, TS/contract change).
assert_if sdk has "needs.changes.outputs.sdk_relevant == 'true'"
assert_if sdk hasnot "go_relevant"
assert_if sdk hasnot "docs_only"

# The SDK jobs that build/spawn mecated gate on go OR sdk relevance.
for job in sdk-integration sdk-deno sdk-browser slack-bot-example; do
  assert_if "$job" has "needs.changes.outputs.go_relevant == 'true' || needs.changes.outputs.sdk_relevant == 'true'"
done

# The Docusaurus build gates on site_relevant ALONE — deliberately no docs_only
# guard, so a user-docs/ content change (which IS docs-only) still rebuilds it.
assert_if user-docs has "needs.changes.outputs.site_relevant == 'true'"
assert_if user-docs hasnot "docs_only"

# --- required-check aggregators tolerate the new skips -------------------------
# test and lint are the required checks; both must gain a go_relevant branch or a
# non-Go PR (which legitimately skips the race shards / analysis) fails them.
go_relevant_env="GO_RELEVANT: \${{ needs.changes.outputs.go_relevant }}"
env_count="$(grep -cF "$go_relevant_env" "$workflow" || true)"
if [[ "$env_count" -lt 2 ]]; then
  fail "both test and lint aggregators must read go_relevant (found $env_count of 2)"
fi
require 'if [[ "$GO_RELEVANT" != true ]]; then' 'the test aggregator must skip the shard requirement for a non-Go change'
require 'non-go-relevant analysis job was not skipped' 'the lint aggregator must require analysis skipped for a non-Go change'

if [[ "$failures" -ne 0 ]]; then
  exit 1
fi
printf 'Linux job-family cost-gating workflow wiring: all checks passed\n'
