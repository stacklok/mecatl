#!/usr/bin/env bash
set -euo pipefail

root=$(pwd -P)
work="$root/.scratch/task-test-suite-test.$$"
bin="$work/bin"
mkdir -p "$bin"
trap 'rm -rf "$work"' EXIT

cat >"$bin/go" <<'EOF'
#!/usr/bin/env sh
set -eu
record="$(pwd -P)|$*|${GOWORK:-}"
printf '%s\n' "$record" >>"$MECATL_TASK_TEST_LOG"
if [ -n "${MECATL_TASK_TEST_FAIL:-}" ] && [ "$record" = "$MECATL_TASK_TEST_FAIL" ]; then
  exit 23
fi
EOF
chmod +x "$bin/go"

# A nested task test reaches this script. Skip that one command so the outer
# invocation can exercise the real Task graph without recursing.
cat >"$bin/bash" <<'EOF'
#!/usr/bin/env sh
[ "$#" -eq 1 ] && [ "$1" = .github/scripts/task-test-suite_test.sh ]
EOF
chmod +x "$bin/bash"

run_task() {
  local target=$1 log=$2 failure=${3:-}
  PATH="$bin:$PATH" GOWORK='' MECATL_TASK_TEST_FAIL="$failure" \
    MECATL_TASK_TEST_LOG="$log" task "$target" >"$log.out" 2>&1
}

assert_line() {
  local line=$1 file=$2
  if ! grep -Fqx "$line" "$file"; then
    echo "missing command: $line" >&2
    cat "$file" >&2
    exit 1
  fi
}

assert_count() {
  local want=$1 file=$2 count
  count=$(wc -l <"$file")
  if [ "$count" -ne "$want" ]; then
    echo "command count: got $count, want $want" >&2
    cat "$file" >&2
    exit 1
  fi
}

assert_suite() {
  local log=$1 race=$2 module dir
  for module in . engine authn/oidc provider/ssefilter provider/anthropic provider/openai provider/openaichat; do
    dir=$root
    [ "$module" = . ] || dir="$root/$module"
    if [ "$race" = yes ]; then
      assert_line "$dir|test -race ./...|" "$log"
    else
      assert_line "$dir|test ./...|" "$log"
    fi
  done

  for module in engine authn/oidc provider/ssefilter provider/anthropic provider/openai provider/openaichat; do
    assert_line "$root/$module|build ./...|off" "$log"
    assert_line "$root/$module|test ./...|off" "$log"
  done
  assert_count 19 "$log"
}

fast_log="$work/test.log"
race_log="$work/race.log"
alias_log="$work/alias.log"
run_task test "$fast_log"
run_task test:race "$race_log"
run_task test:non-race "$alias_log"
assert_suite "$fast_log" no
assert_suite "$race_log" yes
cmp "$fast_log" "$alias_log"

ci_summary="$work/ci-summary.out"
all_summary="$work/all-summary.out"
task --summary ci >"$ci_summary"
task --summary all >"$all_summary"
if ! grep -Fq 'Task: test:race' "$ci_summary"; then
  echo 'task ci does not invoke test:race' >&2
  cat "$ci_summary" >&2
  exit 1
fi
if ! grep -Fq 'Task: ci' "$all_summary"; then
  echo 'task all does not invoke ci' >&2
  cat "$all_summary" >&2
  exit 1
fi

assert_failure() {
  local target=$1 failure=$2 count=$3 log="$work/failure.log"
  : >"$log"
  if run_task "$target" "$log" "$failure"; then
    echo "task $target ignored failure: $failure" >&2
    exit 1
  fi
  assert_line "$failure" "$log"
  assert_count "$count" "$log"
}

assert_failure test "$root/engine|test ./...|" 2
assert_failure test:race "$root/engine|test -race ./...|" 2
for target in test test:race; do
  assert_failure "$target" "$root/engine|test ./...|off" 9
done

echo 'task test wiring: PASS'
