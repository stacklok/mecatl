#!/usr/bin/env bash
# Offline contract test for the macOS-runner cost gating in ci.yml. The macos-14
# jobs cost ~10x a Linux minute, so each must be gated on (a) not docs-only, (b)
# its path-relevance flag, and (c) not a draft PR — and the flags must be computed
# fail-closed from the trusted base-ref classifier.
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

# --- changes job wiring --------------------------------------------------------
require 'macos_go=true' 'macos_go must start fail-closed to RUN'
require 'macos_sdk=true' 'macos_sdk must start fail-closed to RUN'
require 'macos_go: ${{ steps.classify.outputs.macos_go }}' 'macos_go must be a changes-job output'
require 'macos_sdk: ${{ steps.classify.outputs.macos_sdk }}' 'macos_sdk must be a changes-job output'
require 'macos_classifier="$RUNNER_TEMP/macos-relevant-changes.sh"' 'trusted macOS classifier must be extracted outside the candidate checkout'
require 'if git show "$base:.github/scripts/macos-relevant-changes.sh" > "$macos_classifier"; then' 'macOS classifier extraction must read the trusted base ref and stay fail-closed'
require 'git diff --name-only --no-renames -z "$compare_base" "$head" | bash "$macos_classifier" go)" || macos_go=true' 'go relevance must use merge-base-to-head no-renames NUL-delimited paths and fail closed to RUN'
require 'git diff --name-only --no-renames -z "$compare_base" "$head" | bash "$macos_classifier" sdk)" || macos_sdk=true' 'sdk relevance must use merge-base-to-head no-renames NUL-delimited paths and fail closed to RUN'
require 'echo "macos_go=$macos_go"' 'macos_go must be written to GITHUB_OUTPUT'
require 'echo "macos_sdk=$macos_sdk"' 'macos_sdk must be written to GITHUB_OUTPUT'

# The candidate checkout's classifier must never run (a PR could tamper with it).
if grep -Fq 'bash .github/scripts/macos-relevant-changes.sh' "$workflow" \
  || grep -Fq 'bash "$GITHUB_WORKSPACE/.github/scripts/macos-relevant-changes.sh"' "$workflow"; then
  fail 'candidate-checkout macOS classifier must never execute for path classification'
fi

# Extraction must follow base/head commit validation (reuse of the docs-only gate).
validate_line="$(grep -nF 'git cat-file -e "$head^{commit}"' "$workflow" | head -n 1 | cut -d: -f1 || true)"
extract_line="$(grep -nF 'git show "$base:.github/scripts/macos-relevant-changes.sh" > "$macos_classifier"' "$workflow" | head -n 1 | cut -d: -f1 || true)"
if [[ -z "$validate_line" || -z "$extract_line" || "$extract_line" -le "$validate_line" ]]; then
  fail 'macOS classifier extraction must follow base/head commit validation'
fi

# --- per-job gating ------------------------------------------------------------
# Each macos-14 job needs the docs-only guard, its path flag, and the draft gate.
draft_gate="&& (github.event_name != 'pull_request' || github.event.pull_request.draft == false)"

# Count macos-14 jobs so the assertions below cannot silently pass if a job is
# renamed or a fourth is added without gating.
macos_jobs="$(grep -cE '^ +runs-on: macos-14$' "$workflow" || true)"
if [[ "$macos_jobs" -ne 3 ]]; then
  fail "expected 3 macos-14 jobs, found $macos_jobs — new macOS jobs must be cost-gated"
fi

# Both Go smokes gate on macos_go; the SDK spawn gates on macos_sdk. Assert both
# flags are referenced and the draft gate appears for every macos-14 job.
require "needs.changes.outputs.macos_go == 'true'" 'the Go macOS smokes must gate on macos_go'
require "needs.changes.outputs.macos_sdk == 'true'" 'the SDK macOS spawn must gate on macos_sdk'

draft_gates="$(grep -cF "$draft_gate" "$workflow" || true)"
if [[ "$draft_gates" -lt 3 ]]; then
  fail "expected the draft gate on all 3 macos-14 jobs, found $draft_gates occurrence(s)"
fi

# --- closure drift guard wiring ------------------------------------------------
# The terminal-dir allowlist is only sound while a Go-having job re-proves it.
require 'bash .github/scripts/macos-closure-guard.sh' 'the macOS closure guard must run in a modules-resolved job'
guard="$root/.github/scripts/macos-closure-guard.sh"
if [[ ! -f "$guard" ]]; then
  fail 'macos-closure-guard.sh must exist'
fi

if [[ "$failures" -ne 0 ]]; then
  exit 1
fi
printf 'macOS cost-gating workflow wiring: all checks passed\n'
