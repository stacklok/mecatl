#!/usr/bin/env bash
# Offline contract test for the concurrent analysis/vet jobs and stable Lint aggregate.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
workflow="$root/.github/workflows/ci.yml"

job_block() {
  local job="$1"
  awk -v job="$job" '
    $0 == "  " job ":" { in_job = 1 }
    in_job && $0 ~ /^  [[:alnum:]_-]+:$/ && $0 != "  " job ":" { exit }
    in_job { print }
  ' "$workflow"
}

require_once() {
  local block="$1"
  local contract="$2"
  local message="$3"
  if [[ "$(grep -Fc "$contract" <<<"$block")" -ne 1 ]]; then
    printf 'FAIL: %s: %s\n' "$message" "$contract" >&2
    exit 1
  fi
}

analysis_block="$(job_block analysis)"
vet_block="$(job_block vet)"
lint_block="$(job_block lint)"

[[ -n "$analysis_block" ]] || { echo 'FAIL: analysis job is missing' >&2; exit 1; }
[[ -n "$vet_block" ]] || { echo 'FAIL: independent vet job is missing' >&2; exit 1; }
[[ -n "$lint_block" ]] || { echo 'FAIL: Lint aggregate job is missing' >&2; exit 1; }

if grep -Fq 'go vet' <<<"$analysis_block"; then
  echo 'FAIL: vet must not run serially in the analysis job' >&2
  exit 1
fi
if grep -Fq 'golangci-lint' <<<"$vet_block"; then
  echo 'FAIL: golangci analysis must not run serially in the vet job' >&2
  exit 1
fi

# Keep the complete shell body fixed: no conditions, extra setup, fallbacks, or
# trailing commands may silently weaken cmd/vet coverage.
expected_vet_script=(
  'set -euo pipefail'
  'go vet ./...'
  '(cd engine && go vet ./...)'
  '(cd authn/oidc && go vet ./...)'
  '(cd provider/ssefilter && go vet ./...)'
  '(cd provider/anthropic && go vet ./...)'
  '(cd provider/openai && go vet ./...)'
  '(cd provider/openaichat && go vet ./...)'
)
mapfile -t actual_vet_script < <(
  awk '
    /^      - name: Require complete vet coverage$/ { in_step = 1; next }
    in_step && /^        run: \|$/ { in_run = 1; next }
    in_run && /^          / {
      sub(/^          /, "")
      sub(/[[:space:]]*$/, "")
      print
      next
    }
    in_run { exit }
  ' <<<"$vet_block"
)
if [[ "${#actual_vet_script[@]}" -ne "${#expected_vet_script[@]}" ]]; then
  printf 'FAIL: expected %d exact vet script lines, found %d\n' \
    "${#expected_vet_script[@]}" "${#actual_vet_script[@]}" >&2
  printf '  %s\n' "${actual_vet_script[@]}" >&2
  exit 1
fi
for i in "${!expected_vet_script[@]}"; do
  if [[ "${actual_vet_script[$i]}" != "${expected_vet_script[$i]}" ]]; then
    printf 'FAIL: vet script line %d differs\nexpected: %s\nactual:   %s\n' \
      "$((i + 1))" "${expected_vet_script[$i]}" "${actual_vet_script[$i]}" >&2
    exit 1
  fi
done

if grep -Eq '\|\|[[:space:]]*true([[:space:]]|$)' <<<"$vet_block"; then
  echo 'FAIL: vet failures must not be suppressed with || true' >&2
  exit 1
fi
if grep -Fq 'continue-on-error:' <<<"$vet_block"; then
  echo 'FAIL: vet must not use continue-on-error' >&2
  exit 1
fi
mapfile -t vet_conditions < <(grep -E '^[[:space:]]+if:' <<<"$vet_block" || true)
if [[ "${#vet_conditions[@]}" -ne 1 || "${vet_conditions[0]}" != "    if: needs.changes.outputs.docs_only != 'true'" ]]; then
  echo 'FAIL: vet may only have the exact docs-only job condition; conditional steps are forbidden' >&2
  exit 1
fi

for block in "$analysis_block" "$vet_block"; do
  require_once "$block" 'needs: changes' 'child must depend on classification'
  require_once "$block" "if: needs.changes.outputs.docs_only != 'true'" 'child docs-only condition drifted'
done
for contract in \
  'go-version-file: go.mod' \
  'cache: true' \
  'engine/go.sum' \
  'authn/oidc/go.sum' \
  'provider/ssefilter/go.sum' \
  'provider/anthropic/go.sum' \
  'provider/openai/go.sum' \
  'provider/openaichat/go.sum'; do
  require_once "$vet_block" "$contract" 'vet setup/cache contract missing'
done

require_once "$lint_block" 'name: Lint' 'stable required context is missing'
require_once "$lint_block" 'needs: [changes, analysis, vet]' 'aggregate dependency graph drifted'
require_once "$lint_block" 'if: always()' 'aggregate must run after failed, cancelled, or skipped children'
for contract in \
  'CHANGES_RESULT: ${{ needs.changes.result }}' \
  'DOCS_ONLY: ${{ needs.changes.outputs.docs_only }}' \
  'ANALYSIS_RESULT: ${{ needs.analysis.result }}' \
  'VET_RESULT: ${{ needs.vet.result }}' \
  '[[ "$CHANGES_RESULT" != success ]]' \
  '[[ "$ANALYSIS_RESULT" != skipped || "$VET_RESULT" != skipped ]]' \
  '[[ "$ANALYSIS_RESULT" != success || "$VET_RESULT" != success ]]'; do
  require_once "$lint_block" "$contract" 'aggregate fail-closed contract missing'
done
if [[ "$(grep -Ec '^[[:space:]]+name: Lint$' "$workflow")" -ne 1 ]]; then
  echo 'FAIL: exactly one externally visible Lint check must exist' >&2
  exit 1
fi

aggregate_allows() {
  local changes="$1" docs_only="$2" analysis="$3" vet="$4"
  [[ "$changes" == success ]] || return 1
  if [[ "$docs_only" == true ]]; then
    [[ "$analysis" == skipped && "$vet" == skipped ]]
    return
  fi
  [[ "$analysis" == success && "$vet" == success ]]
}

aggregate_allows success false success success || { echo 'FAIL: full successful run rejected' >&2; exit 1; }
aggregate_allows success true skipped skipped || { echo 'FAIL: legitimate docs-only skips rejected' >&2; exit 1; }
fail_cases=(
  'failure false success success'
  'cancelled false success success'
  'success false failure success'
  'success false cancelled success'
  'success false skipped success'
  'success false success failure'
  'success false success cancelled'
  'success false success skipped'
  'success true success skipped'
  'success true skipped success'
)
for test_case in "${fail_cases[@]}"; do
  read -r changes docs_only analysis vet <<<"$test_case"
  if aggregate_allows "$changes" "$docs_only" "$analysis" "$vet"; then
    printf 'FAIL: aggregate accepted changes=%s docs_only=%s analysis=%s vet=%s\n' \
      "$changes" "$docs_only" "$analysis" "$vet" >&2
    exit 1
  fi
done

printf 'lint/vet workflow coverage: all checks passed\n'
