#!/usr/bin/env bash
# Offline contract test for race aggregate contexts and shard wiring.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
workflow="$root/.github/workflows/ci.yml"
failures=0

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  failures=$((failures + 1))
}

aggregate_name="name: \${{ needs.changes.outputs.docs_only == 'true' && 'Test (docs-only)' || github.event_name == 'pull_request' && github.event.pull_request.draft && 'Test (non-race draft)' || github.event_name == 'workflow_dispatch' && 'Test (race experiment)' || 'Test (race)' }}"
if [[ "$(grep -Fc "$aggregate_name" "$workflow")" -ne 1 ]]; then
  fail 'aggregate must isolate docs-only, draft, dispatch, and stable race contexts'
fi
if grep -Eq '^[[:space:]]+name:[[:space:]]+Test \(race\)[[:space:]]*$' "$workflow"; then
  fail 'stable Test (race) context must only be selected by the aggregate expression'
fi

for contract in \
  'needs: [changes, test-race-root-a, test-race-root-b, test-race-ui, test-non-race-draft]' \
  'ROOT_A_RESULT: ${{ needs.test-race-root-a.result }}' \
  'ROOT_B_RESULT: ${{ needs.test-race-root-b.result }}' \
  'UI_RESULT: ${{ needs.test-race-ui.result }}' \
  'DRAFT_RESULT: ${{ needs.test-non-race-draft.result }}' \
  'IS_DRAFT: ${{ github.event_name == '\''pull_request'\'' && github.event.pull_request.draft }}' \
  'Test (non-race draft coverage)' \
  'mapfile -t root_a < <(bash .github/scripts/root-race-packages.sh root-a)'; do
  if ! grep -Fq "$contract" "$workflow"; then
    fail "aggregate outcome contract missing: $contract"
  fi
done

for job in test-race-root-a test-race-root-b test-race-ui; do
  block="$(awk -v job="$job" '
    $0 == "  " job ":" { in_job = 1 }
    in_job && $0 ~ /^  [[:alnum:]_-]+:$/ && $0 != "  " job ":" { exit }
    in_job { print }
  ' "$workflow")"
  if ! grep -Fq "github.event.pull_request.draft == false" <<<"$block"; then
    fail "$job must skip draft pull requests"
  fi
done

if ! grep -Fq 'github.event.pull_request.draft == true' "$workflow"; then
  fail 'draft coverage job must be draft-gated'
fi

cache_dependencies=$'cache-dependency-path: |\n            go.sum\n            engine/go.sum\n            authn/oidc/go.sum\n            provider/ssefilter/go.sum\n            provider/anthropic/go.sum\n            provider/openai/go.sum\n            provider/openaichat/go.sum'
for job in test-race-root-a test-race-root-b test-race-ui test-non-race-draft; do
  block="$(awk -v job="$job" '
    $0 == "  " job ":" { in_job = 1 }
    in_job && $0 ~ /^  [[:alnum:]_-]+:$/ && $0 != "  " job ":" { exit }
    in_job { print }
  ' "$workflow")"
  if ! grep -Fq "$cache_dependencies" <<<"$block"; then
    fail "$job must use the shared multi-module Go cache key"
  fi
done

if ! grep -Fq 'GOPROXY: https://proxy.golang.org|direct' "$workflow"; then
  fail 'workflow must fall back to direct VCS when the public module proxy fails'
fi
if ! grep -Fq 'name: Download Go modules' "$workflow" || ! grep -Fq 'go mod download' "$workflow"; then
  fail 'build job must resolve the complete module graph before compiling'
fi

if ! grep -Fq 'types: [opened, synchronize, reopened, ready_for_review, converted_to_draft]' "$workflow"; then
  fail 'pull-request transitions must trigger the applicable coverage mode'
fi

if [[ "$failures" -ne 0 ]]; then
  exit 1
fi
printf 'race aggregate workflow wiring: all checks passed\n'
