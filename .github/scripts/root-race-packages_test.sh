#!/usr/bin/env bash
# Offline tests for root-race-packages.sh using a fake `go list ./...`.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
partitioner="$root/.github/scripts/root-race-packages.sh"
scratch="$root/.scratch/root-race-packages-test.$$"
mkdir -p "$scratch/bin"
trap 'rm -rf "$scratch"' EXIT

cat >"$scratch/bin/go" <<'FAKE_GO'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$#" -ne 2 || "$1" != list || "$2" != ./... ]]; then
  printf 'fake go: unexpected arguments:' >&2
  printf ' %q' "$@" >&2
  printf '\n' >&2
  exit 90
fi
cat "$ROOT_RACE_TEST_FIXTURE"
FAKE_GO
chmod +x "$scratch/bin/go"

fails=0
run_success() {
  local name="$1" mode="$2" fixture="$3" want="$4" got
  printf '%s' "$fixture" >"$scratch/fixture"
  if ! got="$(PATH="$scratch/bin:$PATH" ROOT_RACE_TEST_FIXTURE="$scratch/fixture" bash "$partitioner" "$mode" 2>"$scratch/err")"; then
    printf 'FAIL: %s — command failed: %s\n' "$name" "$(<"$scratch/err")"
    fails=$((fails + 1))
  elif [[ "$got" != "$want" ]]; then
    printf 'FAIL: %s — expected %q, got %q\n' "$name" "$want" "$got"
    fails=$((fails + 1))
  else
    printf 'ok: %s\n' "$name"
  fi
}

run_failure() {
  local name="$1" fixture="$2" message="$3" got
  printf '%s' "$fixture" >"$scratch/fixture"
  if got="$(PATH="$scratch/bin:$PATH" ROOT_RACE_TEST_FIXTURE="$scratch/fixture" bash "$partitioner" validate 2>"$scratch/err")"; then
    printf 'FAIL: %s — unexpectedly succeeded with %q\n' "$name" "$got"
    fails=$((fails + 1))
  elif ! grep -Fq "$message" "$scratch/err"; then
    printf 'FAIL: %s — expected error containing %q, got %q\n' "$name" "$message" "$(<"$scratch/err")"
    fails=$((fails + 1))
  else
    printf 'ok: %s\n' "$name"
  fi
}

ui='github.com/stacklok/mecatl/cmd/mecatui/ui'
fixture=$'github.com/stacklok/mecatl/zeta\n'"$ui"$'\ngithub.com/stacklok/mecatl/alpha\n'
run_success 'all set is sorted' all "$fixture" $'github.com/stacklok/mecatl/alpha\ngithub.com/stacklok/mecatl/cmd/mecatui/ui\ngithub.com/stacklok/mecatl/zeta'
run_success 'UI set is exactly one package' ui "$fixture" "$ui"
run_success 'complement excludes only exact UI package' complement "$fixture" $'github.com/stacklok/mecatl/alpha\ngithub.com/stacklok/mecatl/zeta'
run_success 'valid partition validates silently' validate "$fixture" ''

run_failure 'missing expected UI package fails loudly' $'github.com/stacklok/mecatl/alpha\n' 'expected UI package exactly once'
run_failure 'duplicate package fails loudly' $'github.com/stacklok/mecatl/alpha\n'"$ui"$'\ngithub.com/stacklok/mecatl/alpha\n' 'duplicate package in all set'
run_failure 'empty complement fails loudly' "$ui"$'\n' 'complement set is empty'
run_failure 'empty package listing fails loudly' '' 'returned no packages'

if [[ "$fails" -ne 0 ]]; then
  printf 'root race package partition tests: %d failure(s)\n' "$fails"
  exit 1
fi
printf 'root race package partition tests: all checks passed\n'
