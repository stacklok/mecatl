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
require 'microvm_relevant=true' 'microvm_relevant must start fail-closed to RUN'
require 'go_relevant: ${{ steps.classify.outputs.go_relevant }}' 'go_relevant must be a changes-job output'
require 'sdk_relevant: ${{ steps.classify.outputs.sdk_relevant }}' 'sdk_relevant must be a changes-job output'
require 'site_relevant: ${{ steps.classify.outputs.site_relevant }}' 'site_relevant must be a changes-job output'
require 'studio_relevant: ${{ steps.classify.outputs.studio_relevant }}' 'studio_relevant must be a changes-job output'
require 'microvm_relevant: ${{ steps.classify.outputs.microvm_relevant }}' 'microvm_relevant must be a changes-job output'
require 'relevant_classifier="$RUNNER_TEMP/relevant-changes.sh"' 'trusted classifier must be extracted outside the candidate checkout'
require 'if git show "$base:.github/scripts/relevant-changes.sh" > "$relevant_classifier"; then' 'classifier extraction must read the trusted base ref and stay fail-closed'
require 'git diff --name-only --no-renames -z "$compare_base" "$head" | bash "$relevant_classifier" go)" || go_relevant=true' 'go relevance must use merge-base-to-head no-renames NUL paths and fail closed to RUN'
require 'git diff --name-only --no-renames -z "$compare_base" "$head" | bash "$relevant_classifier" sdk)" || sdk_relevant=true' 'sdk relevance must use merge-base-to-head no-renames NUL paths and fail closed to RUN'
require 'git diff --name-only --no-renames -z "$compare_base" "$head" | bash "$relevant_classifier" site)" || site_relevant=true' 'site relevance must use merge-base-to-head no-renames NUL paths and fail closed to RUN'
require 'git diff --name-only --no-renames -z "$compare_base" "$head" | bash "$relevant_classifier" studio)" || studio_relevant=true' 'studio relevance must use merge-base-to-head no-renames NUL paths and fail closed to RUN'
require 'git diff --name-only --no-renames -z "$compare_base" "$head" | bash "$relevant_classifier" microvm)" || microvm_relevant=true' 'microvm relevance must use merge-base-to-head no-renames NUL paths and fail closed to RUN'
require 'echo "go_relevant=$go_relevant"' 'go_relevant must be written to GITHUB_OUTPUT'
require 'echo "sdk_relevant=$sdk_relevant"' 'sdk_relevant must be written to GITHUB_OUTPUT'
require 'echo "site_relevant=$site_relevant"' 'site_relevant must be written to GITHUB_OUTPUT'
require 'echo "studio_relevant=$studio_relevant"' 'studio_relevant must be written to GITHUB_OUTPUT'
require 'echo "microvm_relevant=$microvm_relevant"' 'microvm_relevant must be written to GITHUB_OUTPUT'

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
# The remaining Go build/analysis jobs gate on go_relevant; the required test
# context checks changes.result itself, even when no Go tests need running.
for job in build analysis fuzz-smoke engine-standalone provider-standalone api-compat vuln; do
  assert_if "$job" has "needs.changes.outputs.go_relevant == 'true'"
done

# Publication is a conditional step in Build, not another runner on every PR.
build_block="$(job_block build)"
grep -Fq "if: needs.changes.outputs.module_publication_relevant == 'true'" <<<"$build_block" \
  || fail 'Build must gate the publication step on module_publication_relevant'
grep -Fq 'task test:module-publication' <<<"$build_block" \
  || fail 'Build must run the publication task'
grep -Fq 'git fetch --unshallow --tags origin main' <<<"$build_block" \
  || fail 'Build must fetch ancestry and nested tags before checking pins'

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

# The opt-in MicroVM standalone job still uses the dedicated relevance output.
assert_if microvm-standalone has "needs.changes.outputs.microvm_relevant == 'true'"

# --- lint aggregator tolerates non-Go skips ------------------------------------
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
# always-run `changes` job, not a flag-gated Go job.
changes_block="$(job_block changes)"
grep -Fq 'bash .github/scripts/relevant-changes_test.sh' <<<"$changes_block" \
  || fail 'the changes job must run relevant-changes_test.sh (always-run, not flag-gated)'
grep -Fq 'bash .github/scripts/relevant-changes-workflow_test.sh' <<<"$changes_block" \
  || fail 'the changes job must run relevant-changes-workflow_test.sh (always-run, not flag-gated)'

if [[ "$failures" -ne 0 ]]; then
  exit 1
fi
printf 'Linux job-family cost-gating workflow wiring: all checks passed\n'
