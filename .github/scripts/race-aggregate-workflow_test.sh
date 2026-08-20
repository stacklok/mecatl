#!/usr/bin/env bash
# Offline contract test for race aggregate check-context isolation.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
workflow="$root/.github/workflows/ci.yml"
failures=0

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  failures=$((failures + 1))
}

aggregate_name="name: \${{ github.event_name == 'workflow_dispatch' && 'Test (race experiment)' || 'Test (race)' }}"
if [[ "$(grep -Fc "$aggregate_name" "$workflow")" -ne 1 ]]; then
  fail 'aggregate must select exactly one dispatch-isolated check context'
fi
if grep -Eq '^[[:space:]]+name:[[:space:]]+Test \(race\)[[:space:]]*$' "$workflow"; then
  fail 'stable Test (race) context must not be published by a separate dispatch-visible job'
fi

for contract in \
  'needs: [changes, test-race-root, test-race-ui]' \
  'CHANGES_RESULT: ${{ needs.changes.result }}' \
  'DOCS_ONLY: ${{ needs.changes.outputs.docs_only }}' \
  'ROOT_RESULT: ${{ needs.test-race-root.result }}' \
  'UI_RESULT: ${{ needs.test-race-ui.result }}'; do
  if ! grep -Fq "$contract" "$workflow"; then
    fail "aggregate outcome contract missing: $contract"
  fi
done

if [[ "$failures" -ne 0 ]]; then
  exit 1
fi
printf 'race aggregate workflow wiring: all checks passed\n'
