#!/usr/bin/env bash
# check-acceptance-plan.sh — validate a docs/acceptance/<plan>.md against the
# acceptance-plan contract: at least one scenario, numbered acceptance criteria,
# >=1 ADR / architecture / AGENTS.md citation per scenario, and an out-of-scope
# section.
#
# This is the authoring-time check for /to-acceptance-plan. The runtime gate on
# a landed plan's verify: contract is `task ac-trace-strict` (the ac-trace tool;
# see docs/acceptance/README.md). A plan that fails the hard checks below would
# otherwise be graded against a soft spec by panel-review — so the skill runs
# this first.
#
# Usage:
#   bash check-acceptance-plan.sh docs/acceptance/<plan>.md
#
# Exit codes:
#   0  hard checks pass (advisory warnings may still print)
#   1  hard absence: no scenarios, no numbered ACs, missing verify: coverage,
#      no citations, or no out-of-scope section
#   2  usage / file-not-found

set -euo pipefail

if [[ $# -ne 1 ]]; then
  printf 'usage: %s docs/acceptance/<plan>.md\n' "$(basename "$0")" >&2
  exit 2
fi

plan="$1"
if [[ ! -f "$plan" ]]; then
  printf 'error: file not found: %s\n' "$plan" >&2
  exit 2
fi

fail=0
warn=0
note_fail() { printf 'FAIL: %s\n' "$1" >&2; fail=1; }
note_warn() { printf 'WARN: %s\n' "$1" >&2; warn=1; }

# A citation is a markdown link into ../adr/**, ../architecture*,
# ../design/**, or ../../AGENTS.md (relative to docs/acceptance/), or a bare
# ADR-NNNN / invariant reference.
CITE_LINK='\]\((\.\./(adr/|architecture|design/)|\.\./\.\./AGENTS\.md)'

# --- (a) numbered acceptance criteria ----------------------------------
ac_labeled=$(grep -cE '(\*\*)?AC[0-9]+\.[0-9]+(\*\*)?:' "$plan" || true)

if [[ "$ac_labeled" -gt 0 ]]; then
  printf 'ok: %s numbered AC<n>.<m> criteria\n' "$ac_labeled"
else
  note_fail 'no numbered acceptance criteria (expected stable AC<scenario>.<n>: labels).'
fi

# --- (b) verify: sub-line coverage (hard authoring contract) -----------
ac_count=$(grep -cE '(\*\*)?AC[0-9]+\.[0-9]+(\*\*)?:' "$plan" || true)
missing_verify=$(awk '
  function flush() { if (ac != "" && !verified) print ac }
  function start_ac(line) {
    ac = line
    sub(/^.*AC/, "AC", ac)
    sub(/:.*/, "", ac)
    gsub(/\*\*/, "", ac)
    verified = 0
  }
  /(\*\*)?AC[0-9]+\.[0-9]+(\*\*)?:/ {
    flush()
    start_ac($0)
    next
  }
  ac != "" && /^#{1,6}[[:space:]]/ {
    flush()
    ac = ""
    verified = 0
    next
  }
  ac != "" && /^[[:space:]]*-?[[:space:]]*verify:[[:space:]]*[^[:space:]]/ {
    verified = 1
  }
  END { flush() }
' "$plan")
if [[ -n "$missing_verify" ]]; then
  missing_csv=$(printf '%s\n' "$missing_verify" | paste -sd, -)
  note_fail "missing or empty verify: sub-line in AC block for: $missing_csv"
elif [[ "$ac_count" -gt 0 ]]; then
  printf 'ok: %s ACs each have non-empty verify: coverage\n' "$ac_count"
fi

# --- (c) >=1 citation per scenario -------------------------------------
total_citations=$(grep -cE "$CITE_LINK" "$plan" || true)
if [[ "$total_citations" -eq 0 ]]; then
  note_fail 'no citations — every scenario must link at least once to ../adr/**, ../architecture.md, ../design/**, or ../../AGENTS.md (relative to docs/acceptance/).'
else
  printf 'ok: %s citation link(s)\n' "$total_citations"
fi

scenario_lines=$(grep -nE '^###[[:space:]]+Scenario[[:space:]]' "$plan" | cut -d: -f1 || true)
if [[ -n "$scenario_lines" ]]; then
  starts=()
  while IFS= read -r ln; do [[ -n "$ln" ]] && starts+=("$ln"); done <<< "$scenario_lines"
  total_lines=$(wc -l < "$plan")
  n=${#starts[@]}
  for ((i = 0; i < n; i++)); do
    start=${starts[i]}
    if ((i + 1 < n)); then end=$(( ${starts[i + 1]} - 1 )); else end=$total_lines; fi
    title=$(sed -n "${start}p" "$plan" | sed -E 's/^###[[:space:]]+//')
    block=$(sed -n "${start},${end}p" "$plan")
    link_cites=$(printf '%s' "$block" | grep -cE "$CITE_LINK" || true)
    bare_cites=$(printf '%s' "$block" | grep -cE 'ADR-[0-9]{4}|invariant' || true)
    if [[ "$link_cites" -eq 0 && "$bare_cites" -eq 0 ]]; then
      note_fail "scenario cites no ADR / architecture / AGENTS.md invariant: ${title}"
    elif [[ "$link_cites" -eq 0 ]]; then
      note_warn "scenario cites only a bare ADR/invariant (no markdown link): ${title}."
    fi
  done
else
  note_fail 'no "### Scenario N" headings — scenario-first plans require at least one scenario. Focused plans should use one compact scenario, a small AC set, and may map to one orchestration task.'
fi

# --- (d) out-of-scope section ------------------------------------------
if grep -qiE '^##+ +Out of scope' "$plan"; then
  printf 'ok: out-of-scope section present\n'
else
  note_fail 'no "## Out of scope" section — every plan must name what it defers.'
fi

# --- advisory: named-test ACs and Definition of done ------------------
if ! grep -qE 'Test(ADR_[0-9]+|Invariant)_' "$plan"; then
  note_warn 'no named tests (TestADR_NNNN_* / TestInvariant_*) — most scenarios should pin their ACs with a named test.'
fi
if ! grep -qiE '^##+ +Definition of done' "$plan"; then
  note_warn 'no "## Definition of done" section — the gold-standard plans always carry one.'
fi

printf '\n'
if [[ "$fail" -ne 0 ]]; then
  printf 'check-acceptance-plan: FAILED (hard absences above). Fix before handing to /plan-orchestrate.\n' >&2
  exit 1
fi
if [[ "$warn" -ne 0 ]]; then
  printf 'check-acceptance-plan: passed with warnings.\n'
else
  printf 'check-acceptance-plan: passed.\n'
fi
exit 0
