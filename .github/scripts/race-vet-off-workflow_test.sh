#!/usr/bin/env bash
# Offline contract test for the manual-only race vet experiment wiring.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
workflow="$root/.github/workflows/ci.yml"
forward='RACE_VET_OFF: ${{ github.event_name == '\''workflow_dispatch'\'' && inputs.race_vet_off }}'

failures=0
fail() {
  printf 'FAIL: %s\n' "$1" >&2
  failures=$((failures + 1))
}

if ! grep -Fq 'race_vet_off:' "$workflow"; then
  fail 'missing manual race_vet_off input'
fi

for job in test-race-root-a test-race-root-b test-race-ui; do
  block="$(awk -v job="$job" '
    $0 == "  " job ":" { in_job = 1 }
    in_job && $0 ~ /^  [[:alnum:]_-]+:$/ && $0 != "  " job ":" { exit }
    in_job { print }
  ' "$workflow")"
  if ! grep -Fq "$forward" <<<"$block"; then
    fail "$job does not forward race_vet_off through a workflow_dispatch-only environment"
  fi

  case "$job" in
    test-race-root-a) expected_wrappers=7 ;;
    test-race-root-b|test-race-ui) expected_wrappers=1 ;;
  esac
  wrapper_calls="$(grep -Fc 'race-test.sh' <<<"$block")"
  if [[ "$wrapper_calls" -ne "$expected_wrappers" ]]; then
    fail "$job has $wrapper_calls race-test.sh calls; expected $expected_wrappers"
  fi

  commands="$(grep -Ev '^[[:space:]]*-[[:space:]]*name:' <<<"$block")"
  if grep -Eq '(^|[;&|(][[:space:]]*)go[[:space:]]+test([[:space:]]|$)' <<<"$commands"; then
    fail "$job has a direct go test invocation instead of race-test.sh"
  fi
done

if [[ "$(grep -Fc "$forward" "$workflow")" -ne 3 ]]; then
  fail 'race_vet_off forwarding is not limited to the three race jobs'
fi

if [[ "$failures" -ne 0 ]]; then
  exit 1
fi
printf 'race vet-off workflow wiring: all checks passed\n'
