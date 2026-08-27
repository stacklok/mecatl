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
root_a=$'github.com/stacklok/mecatl/docs/lint\ngithub.com/stacklok/mecatl/internal/adapter/acp\ngithub.com/stacklok/mecatl/internal/adapter/grpcdriver\ngithub.com/stacklok/mecatl/internal/adapter/mcp\ngithub.com/stacklok/mecatl/internal/adapter/mcp/jq\ngithub.com/stacklok/mecatl/internal/adapter/redisstore\ngithub.com/stacklok/mecatl/internal/adapter/server\ngithub.com/stacklok/mecatl/internal/adapter/store/jsonlstore\ngithub.com/stacklok/mecatl/internal/apicheck\ngithub.com/stacklok/mecatl/internal/app'
fixture=$'github.com/stacklok/mecatl/zeta\n'"$ui"$'\ngithub.com/stacklok/mecatl/alpha\n'"$root_a"$'\n'
run_success 'all set is sorted' all "$fixture" $'github.com/stacklok/mecatl/alpha\ngithub.com/stacklok/mecatl/cmd/mecatui/ui\ngithub.com/stacklok/mecatl/docs/lint\ngithub.com/stacklok/mecatl/internal/adapter/acp\ngithub.com/stacklok/mecatl/internal/adapter/grpcdriver\ngithub.com/stacklok/mecatl/internal/adapter/mcp\ngithub.com/stacklok/mecatl/internal/adapter/mcp/jq\ngithub.com/stacklok/mecatl/internal/adapter/redisstore\ngithub.com/stacklok/mecatl/internal/adapter/server\ngithub.com/stacklok/mecatl/internal/adapter/store/jsonlstore\ngithub.com/stacklok/mecatl/internal/apicheck\ngithub.com/stacklok/mecatl/internal/app\ngithub.com/stacklok/mecatl/zeta'
run_success 'UI set is exactly one package' ui "$fixture" "$ui"
run_success 'root-a is the maintained explicit set' root-a "$fixture" "$root_a"
run_success 'root-b automatically includes every other non-UI package' root-b "$fixture" $'github.com/stacklok/mecatl/alpha\ngithub.com/stacklok/mecatl/zeta'
run_success 'valid partition validates silently' validate "$fixture" ''

run_failure 'missing expected UI package fails loudly' "$root_a"$'\ngithub.com/stacklok/mecatl/alpha\n' 'expected UI package exactly once'
run_failure 'missing root-a package fails loudly' "$ui"$'\ngithub.com/stacklok/mecatl/alpha\n' 'root-a package is not in go list ./...'
run_failure 'duplicate package fails loudly' "$fixture"$'github.com/stacklok/mecatl/alpha\n' 'duplicate package in all set'
run_failure 'empty root-b fails loudly' "$ui"$'\n'"$root_a"$'\n' 'root-b set is empty'
run_failure 'empty package listing fails loudly' '' 'returned no packages'

if [[ "$fails" -ne 0 ]]; then
  printf 'root race package partition tests: %d failure(s)\n' "$fails"
  exit 1
fi
printf 'root race package partition tests: all checks passed\n'
