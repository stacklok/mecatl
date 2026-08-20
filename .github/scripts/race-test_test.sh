#!/usr/bin/env bash
# Offline behavior tests for race-test.sh using fake go and jq commands.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
runner="$root/.github/scripts/race-test.sh"
scratch="$root/.scratch/race-test-test.$$"
mkdir -p "$scratch/bin" "$scratch/workspace"
trap 'rm -rf "$scratch"' EXIT

cat >"$scratch/bin/go" <<'FAKE_GO'
#!/usr/bin/env bash
printf '%s\n' "$*" >"$RACE_TEST_ARGS"
printf '{"Action":"output","Output":"visible failure\\n"}\n'
printf '{"Action":"fail","Package":"example.test","Elapsed":1.25}\n'
exit "${RACE_TEST_GO_STATUS:-0}"
FAKE_GO
cat >"$scratch/bin/jq" <<'FAKE_JQ'
#!/usr/bin/env bash
# The production jq renders Output fields. This fixture keeps the test focused
# on invocation, artifact capture, and exit-status preservation.
cat >/dev/null
printf 'visible failure\n'
FAKE_JQ
chmod +x "$scratch/bin/go" "$scratch/bin/jq"

fails=0
fail() {
  printf 'FAIL: %s\n' "$1"
  fails=$((fails + 1))
}

args_file="$scratch/args"
normal_out="$scratch/normal.out"
if ! PATH="$scratch/bin:$PATH" RACE_VET_OFF=false RACE_TEST_ARGS="$args_file" GITHUB_WORKSPACE="$scratch/workspace" \
  bash "$runner" root-complement -count=1 ./pkg >"$normal_out"; then
  fail 'normal mode failed'
elif [[ "$(<"$args_file")" != 'test -race -count=1 ./pkg' ]]; then
  fail "normal mode changed go arguments: $(<"$args_file")"
elif [[ -e "$scratch/workspace/.scratch/race-timing" ]]; then
  fail 'normal mode created a timing directory'
else
  printf 'ok: normal mode is an unredirected non-JSON race test\n'
fi

if ! PATH="$scratch/bin:$PATH" RACE_VET_OFF=true RACE_TEST_ARGS="$args_file" GITHUB_WORKSPACE="$scratch/workspace" \
  bash "$runner" root-complement -count=1 ./pkg >"$normal_out"; then
  fail 'vet-off normal mode failed'
elif [[ "$(<"$args_file")" != 'test -race -vet=off -count=1 ./pkg' ]]; then
  fail "vet-off normal mode did not forward -vet=off: $(<"$args_file")"
elif [[ -e "$scratch/workspace/.scratch/race-timing" ]]; then
  fail 'vet-off normal mode created a timing directory'
else
  printf 'ok: vet-off normal mode is an unredirected non-JSON race test\n'
fi

capture="$scratch/workspace/.scratch/race-timing/ui.jsonl"
if ! PATH="$scratch/bin:$PATH" RACE_TIMING=true RACE_VET_OFF=true RACE_TEST_ARGS="$args_file" GITHUB_WORKSPACE="$scratch/workspace" \
  bash "$runner" ui -count=1 ./cmd/mecatui/ui >"$scratch/diagnostic.out"; then
  fail 'vet-off diagnostic mode failed'
elif [[ "$(<"$args_file")" != 'test -race -vet=off -json -count=1 ./cmd/mecatui/ui' ]]; then
  fail "vet-off diagnostic mode did not forward -vet=off: $(<"$args_file")"
elif [[ ! -s "$capture" ]] || ! grep -Fq '"Elapsed":1.25' "$capture"; then
  fail 'vet-off diagnostic mode did not retain JSONL timing records'
elif ! grep -Fq 'visible failure' "$scratch/diagnostic.out"; then
  fail 'vet-off diagnostic mode did not keep human-readable output'
else
  printf 'ok: vet-off diagnostic mode captures JSONL and renders test output\n'
fi

if ! PATH="$scratch/bin:$PATH" RACE_TIMING=true RACE_TEST_ARGS="$args_file" GITHUB_WORKSPACE="$scratch/workspace" \
  bash "$runner" ui -count=1 ./cmd/mecatui/ui >"$scratch/diagnostic.out"; then
  fail 'diagnostic mode failed'
elif [[ "$(<"$args_file")" != 'test -race -json -count=1 ./cmd/mecatui/ui' ]]; then
  fail "diagnostic mode did not add -json: $(<"$args_file")"
elif [[ ! -s "$capture" ]] || ! grep -Fq '"Elapsed":1.25' "$capture"; then
  fail 'diagnostic mode did not retain JSONL timing records'
elif ! grep -Fq 'visible failure' "$scratch/diagnostic.out"; then
  fail 'diagnostic mode did not keep human-readable output'
else
  printf 'ok: diagnostic mode captures JSONL and renders test output\n'
fi

if PATH="$scratch/bin:$PATH" RACE_TIMING=true RACE_TEST_GO_STATUS=23 RACE_TEST_ARGS="$args_file" \
  GITHUB_WORKSPACE="$scratch/workspace" bash "$runner" failing ./pkg >"$scratch/failing.out"; then
  fail 'diagnostic mode masked a test failure'
elif [[ "$?" -ne 23 ]]; then
  fail 'diagnostic mode did not preserve the go test exit status'
elif ! grep -Fq 'visible failure' "$scratch/failing.out"; then
  fail 'diagnostic failure output was not actionable'
else
  printf 'ok: diagnostic mode preserves failures and output\n'
fi

if [[ "$fails" -ne 0 ]]; then
  printf 'race test wrapper tests: %d failure(s)\n' "$fails"
  exit 1
fi
printf 'race test wrapper tests: all checks passed\n'
