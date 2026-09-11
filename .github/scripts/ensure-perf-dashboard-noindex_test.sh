#!/usr/bin/env bash
# Offline tests for the generated performance-dashboard noindex guard.
# shellcheck disable=SC2016 # Workflow snippets below must remain literal.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
script="$root/.github/scripts/ensure-perf-dashboard-noindex.sh"
workflow="$root/.github/workflows/perf.yml"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

index="$tmpdir/index.html"
printf '%s\n' '<!DOCTYPE html>' '<html>' '  <head>' '    <meta charset="utf-8" />' '  </head>' '</html>' > "$index"

"$script" "$index"
if [[ "$(grep -Fc '<meta name="robots" content="noindex, nofollow" />' "$index")" -ne 1 ]]; then
  fail 'the robots directive must be inserted exactly once'
fi
if [[ "$(sed -n '4p' "$index")" != '    <meta name="robots" content="noindex, nofollow" />' ]]; then
  fail 'the robots directive must be the first child of <head>'
fi

cp "$index" "$tmpdir/once.html"
"$script" "$index"
cmp -s "$tmpdir/once.html" "$index" || fail 'a second run must be byte-identical'

printf '%s\n' '<html>' '<head>' '<meta name="robots" content="index, follow" />' '</head>' '</html>' > "$index"
if "$script" "$index" >/dev/null 2>&1; then
  fail 'an unexpected robots directive must fail closed'
fi

printf '%s\n' '<html>' '<body></body>' '</html>' > "$index"
if "$script" "$index" >/dev/null 2>&1; then
  fail 'a missing <head> element must fail closed'
fi

if "$script" "$tmpdir/missing.html" >/dev/null 2>&1; then
  fail 'a missing dashboard index must fail closed'
fi

require_workflow_line() {
  local text="$1" message="$2"
  grep -Fq -- "$text" "$workflow" || fail "$message"
}

require_workflow_line 'dashboard_index="$tmp/dev/bench/index.html"' 'the workflow must target the generated dashboard index'
require_workflow_line '.github/scripts/ensure-perf-dashboard-noindex.sh "$dashboard_index"' 'the workflow must enforce noindex after cloning gh-pages'
require_workflow_line 'git -C "$tmp" add "${BENCH_BASELINE_PATH}" "dev/bench/index.html"' 'the workflow must commit the protected dashboard index'

clone_line="$(grep -nF 'git clone --depth 1 --branch gh-pages "$remote" "$tmp"' "$workflow" | cut -d: -f1)"
guard_line="$(grep -nF '.github/scripts/ensure-perf-dashboard-noindex.sh "$dashboard_index"' "$workflow" | cut -d: -f1)"
add_line="$(grep -nF 'git -C "$tmp" add "${BENCH_BASELINE_PATH}" "dev/bench/index.html"' "$workflow" | cut -d: -f1)"
if [[ -z "$clone_line" || -z "$guard_line" || -z "$add_line" || "$clone_line" -ge "$guard_line" || "$guard_line" -ge "$add_line" ]]; then
  fail 'the noindex guard must run after cloning gh-pages and before staging the dashboard'
fi

printf 'Performance dashboard noindex guard: all checks passed\n'
