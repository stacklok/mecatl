#!/usr/bin/env bash
# Offline contract test for the Linux job-family cost gating in ci.yml. Each Go /
# SDK-frontend / Docusaurus-site job family must be gated on its path-relevance
# flag, the flags must be computed fail-closed from the trusted base-ref
# classifier, and the required-check aggregators (test, lint) must tolerate the
# legitimate skips those flags introduce — otherwise a non-Go PR is blocked.
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

# Print the single-line `if:` of a named job (from its `  <name>:` header down to
# the next top-level job header). Every gated job here uses a single-line `if:`.
job_if() {
  awk -v header="  $1:" '
    $0 == header { inj = 1; next }
    inj && /^  [a-zA-Z0-9_-]+:$/ { exit }
    inj && /^    if:/ { print; exit }
  ' "$workflow"
}

# Print a job's whole body (from its `  <name>:` header to the next top-level job
# header). Used to scope an assertion to ONE job when the text it greps for also
# appears in a sibling job (e.g. the identical GO_RELEVANT early-exit in both the
# `test` and `lint` aggregators).
job_block() {
  awk -v header="  $1:" '
    $0 == header { inj = 1; print; next }
    inj && /^  [a-zA-Z0-9_-]+:$/ { exit }
    inj { print }
  ' "$workflow"
}

# Assert a job's `if:` contains (or, with "!", does not contain) a substring.
assert_if() {
  local job="$1" mode="$2" needle="$3" line
  line="$(job_if "$job")"
  if [[ -z "$line" ]]; then
    fail "job '$job' has no single-line if: to classify"
    return
  fi
  case "$mode" in
    has)
      if ! grep -Fq "$needle" <<<"$line"; then
        fail "job '$job' if: must contain \"$needle\" (got: $line)"
      fi
      ;;
    hasnot)
      if grep -Fq "$needle" <<<"$line"; then
        fail "job '$job' if: must NOT contain \"$needle\" (got: $line)"
      fi
      ;;
  esac
  return 0
}

# --- classifier script exists --------------------------------------------------
if [[ ! -f "$root/.github/scripts/relevant-changes.sh" ]]; then
  fail 'relevant-changes.sh must exist'
fi

# --- changes job wiring --------------------------------------------------------
require 'go_relevant=true' 'go_relevant must start fail-closed to RUN'
require 'sdk_relevant=true' 'sdk_relevant must start fail-closed to RUN'
require 'site_relevant=true' 'site_relevant must start fail-closed to RUN'
require 'studio_relevant=true' 'studio_relevant must start fail-closed to RUN'
require 'go_relevant: ${{ steps.classify.outputs.go_relevant }}' 'go_relevant must be a changes-job output'
require 'sdk_relevant: ${{ steps.classify.outputs.sdk_relevant }}' 'sdk_relevant must be a changes-job output'
require 'site_relevant: ${{ steps.classify.outputs.site_relevant }}' 'site_relevant must be a changes-job output'
require 'studio_relevant: ${{ steps.classify.outputs.studio_relevant }}' 'studio_relevant must be a changes-job output'
require 'relevant_classifier="$RUNNER_TEMP/relevant-changes.sh"' 'trusted classifier must be extracted outside the candidate checkout'
require 'if git show "$base:.github/scripts/relevant-changes.sh" > "$relevant_classifier"; then' 'classifier extraction must read the trusted base ref and stay fail-closed'
require 'git diff --name-only --no-renames -z "$compare_base" "$head" | bash "$relevant_classifier" go)" || go_relevant=true' 'go relevance must use merge-base-to-head no-renames NUL paths and fail closed to RUN'
require 'git diff --name-only --no-renames -z "$compare_base" "$head" | bash "$relevant_classifier" sdk)" || sdk_relevant=true' 'sdk relevance must use merge-base-to-head no-renames NUL paths and fail closed to RUN'
require 'git diff --name-only --no-renames -z "$compare_base" "$head" | bash "$relevant_classifier" site)" || site_relevant=true' 'site relevance must use merge-base-to-head no-renames NUL paths and fail closed to RUN'
require 'git diff --name-only --no-renames -z "$compare_base" "$head" | bash "$relevant_classifier" studio)" || studio_relevant=true' 'studio relevance must use merge-base-to-head no-renames NUL paths and fail closed to RUN'
require 'echo "go_relevant=$go_relevant"' 'go_relevant must be written to GITHUB_OUTPUT'
require 'echo "sdk_relevant=$sdk_relevant"' 'sdk_relevant must be written to GITHUB_OUTPUT'
require 'echo "site_relevant=$site_relevant"' 'site_relevant must be written to GITHUB_OUTPUT'
require 'echo "studio_relevant=$studio_relevant"' 'studio_relevant must be written to GITHUB_OUTPUT'

# The candidate checkout's classifier must never run (a PR could tamper with it).
if grep -Fq 'bash .github/scripts/relevant-changes.sh' "$workflow" \
  || grep -Fq 'bash "$GITHUB_WORKSPACE/.github/scripts/relevant-changes.sh"' "$workflow"; then
  fail 'candidate-checkout classifier must never execute for path classification'
fi

# Extraction must follow base/head commit validation (reuse of the docs-only gate).
validate_line="$(grep -nF 'git cat-file -e "$head^{commit}"' "$workflow" | head -n 1 | cut -d: -f1 || true)"
extract_line="$(grep -nF 'git show "$base:.github/scripts/relevant-changes.sh" > "$relevant_classifier"' "$workflow" | head -n 1 | cut -d: -f1 || true)"
if [[ -z "$validate_line" || -z "$extract_line" || "$extract_line" -le "$validate_line" ]]; then
  fail 'classifier extraction must follow base/head commit validation'
fi

# --- per-job gating ------------------------------------------------------------
# The Go build/test matrix gates on go_relevant.
for job in build analysis fuzz-smoke engine-standalone provider-standalone \
  api-compat vuln test-race-root-a test-race-root-b test-race-ui test-non-race-draft; do
  assert_if "$job" has "needs.changes.outputs.go_relevant == 'true'"
done

# The pure-TypeScript SDK unit job gates on sdk_relevant ALONE (no go, no
# docs_only — sdk_relevant already implies a non-docs, TS/contract change).
assert_if sdk has "needs.changes.outputs.sdk_relevant == 'true'"
assert_if sdk hasnot "go_relevant"
assert_if sdk hasnot "docs_only"

# The SDK jobs that build/spawn mecated gate on go OR sdk relevance.
for job in sdk-integration sdk-deno sdk-browser slack-bot-example; do
  assert_if "$job" has "needs.changes.outputs.go_relevant == 'true' || needs.changes.outputs.sdk_relevant == 'true'"
done

# The Docusaurus build gates on site_relevant ALONE — deliberately no docs_only
# guard, so a user-docs/ content change (which IS docs-only) still rebuilds it.
assert_if user-docs has "needs.changes.outputs.site_relevant == 'true'"
assert_if user-docs hasnot "docs_only"

# The Mecatl Studio job gates on studio_relevant OR go_relevant (plus the
# docs_only guard — an apps/README.md-only change is docs-only and needs no Node
# run): its integration suite drives the BFF against the current bin/mecated,
# so a daemon change can break it. Studio consumes the PUBLISHED SDK, so it must
# NOT be tied to sdk relevance.
assert_if studio has "needs.changes.outputs.studio_relevant == 'true' || needs.changes.outputs.go_relevant == 'true'"
assert_if studio has "needs.changes.outputs.docs_only != 'true'"
assert_if studio hasnot "sdk_relevant"

# Drift guard: pin the number of job-level `if:` gates carrying go_relevant so
# adding or removing a Go-gated job forces a conscious update to the job lists
# above. Unlike the macOS jobs (which share the `runs-on: macos-14` marker the
# sibling test counts), Linux Go jobs share `runs-on: ubuntu-24.04` with
# legitimately-ungated jobs (changes, docs, domain-model, the always() aggregators,
# sdk, user-docs), so there is no runner marker to count — this pins the gate set
# instead. Expected 16 = 11 go-family/race/draft (the loop above) + 4 go||sdk jobs
# + the studio job (go||studio).
# The residual this cannot catch is a NEW Go job shipped with NO gate at all; the
# job lists above are the record for that.
go_gate_count="$(grep -cF "needs.changes.outputs.go_relevant == 'true'" "$workflow" || true)"
if [[ "$go_gate_count" -ne 16 ]]; then
  fail "expected 16 job if: gates on go_relevant (11 go-family + 4 go||sdk + studio), found $go_gate_count — update the job lists in this test when gating/ungating a job"
fi

# --- required-check aggregators tolerate the new skips -------------------------
# test and lint are the required checks; both must gain a go_relevant branch or a
# non-Go PR (which legitimately skips the race shards / analysis) fails them.
go_relevant_env="GO_RELEVANT: \${{ needs.changes.outputs.go_relevant }}"
env_count="$(grep -cF "$go_relevant_env" "$workflow" || true)"
if [[ "$env_count" -lt 2 ]]; then
  fail "both test and lint aggregators must read go_relevant (found $env_count of 2)"
fi
# The identical GO_RELEVANT early-exit appears in BOTH aggregators, so a whole-file
# grep cannot tell which one has it. Pin each within its own job body: the test
# aggregator's shard-requirement skip, and the lint aggregator's analysis-skipped
# requirement (its message is already unique to lint).
if ! grep -Fq 'if [[ "$GO_RELEVANT" != true ]]; then' <<<"$(job_block test)"; then
  fail 'the test aggregator must skip the shard requirement for a non-Go change'
fi
if ! grep -Fq 'if [[ "$GO_RELEVANT" != true ]]; then' <<<"$(job_block lint)"; then
  fail 'the lint aggregator must branch on go_relevant for a non-Go change'
fi
require 'non-go-relevant analysis job was not skipped' 'the lint aggregator must require analysis skipped for a non-Go change'

# The Docusaurus build is a required check gated directly on site_relevant, and a
# job skipped via `if:` reports as success for branch protection. It therefore
# needs an always() aggregator (like test/lint) so a classifier bug that wrongly
# marks a content change site-irrelevant cannot let the gate go green unbuilt.
udg="$(job_block user-docs-gate)"
if [[ -z "$udg" ]]; then
  fail 'a user-docs-gate aggregator must exist to protect the site build required check'
else
  grep -Fq 'if: always()' <<<"$udg" || fail 'user-docs-gate must run with if: always()'
  grep -Fq 'needs: [changes, user-docs]' <<<"$udg" || fail 'user-docs-gate must need [changes, user-docs]'
  grep -Fq 'SITE_RELEVANT: ${{ needs.changes.outputs.site_relevant }}' <<<"$udg" \
    || fail 'user-docs-gate must read site_relevant'
  grep -Fq 'non-site-relevant user-docs job was not skipped' <<<"$udg" \
    || fail 'user-docs-gate must require user-docs skipped for a non-site change'
  grep -Fq 'USER_DOCS_RESULT" != success' <<<"$udg" \
    || fail 'user-docs-gate must require user-docs success for a site-relevant change'
fi

# The path classifiers decide which jobs run, so their self-tests must live in the
# always-run `changes` job — never a flag-gated Go job that a misclassification
# could skip. Pin that the changes job runs the relevant-changes tests and that
# they are NOT (also) left in the go_relevant-gated test-race-root-a.
changes_block="$(job_block changes)"
grep -Fq 'bash .github/scripts/relevant-changes_test.sh' <<<"$changes_block" \
  || fail 'the changes job must run relevant-changes_test.sh (always-run, not flag-gated)'
grep -Fq 'bash .github/scripts/relevant-changes-workflow_test.sh' <<<"$changes_block" \
  || fail 'the changes job must run relevant-changes-workflow_test.sh (always-run, not flag-gated)'
if grep -Fq 'bash .github/scripts/relevant-changes_test.sh' <<<"$(job_block test-race-root-a)"; then
  fail 'classifier tests must not run in the go_relevant-gated test-race-root-a (self-gating blind spot)'
fi

if [[ "$failures" -ne 0 ]]; then
  exit 1
fi
printf 'Linux job-family cost-gating workflow wiring: all checks passed\n'
