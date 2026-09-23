#!/usr/bin/env bash
# check-acceptance-plan.sh — validate a docs/acceptance/<plan>.md against the
# acceptance-plan contract. Legacy `human-reviewed/v1` plans retain their original
# shape. `human-reviewed/v2` additionally requires a Bounded or Architectural work
# classification and a matching decision-record outcome. Both versions require at
# least one scenario, numbered acceptance criteria, a non-empty human-decisions
# section consistent with status, a non-empty interface contract, >=1 repository
# citation per scenario, and an out-of-scope section.
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
#      missing/empty interface contract, no citations, or no out-of-scope section
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

# The marker opts a plan into a versioned contract without retroactively
# bulk-migrating the historical catalogue.
contract_count=$(grep -cE '^\*\*Contract:\*\*' "$plan" || true)
contract_v1=$(grep -cFx '**Contract:** human-reviewed/v1' "$plan" || true)
contract_v2=$(grep -cFx '**Contract:** human-reviewed/v2' "$plan" || true)
contract_version=''
if [[ "$contract_count" -eq 1 && "$contract_v1" -eq 1 ]]; then
  contract_version='v1'
  printf 'ok: legacy human-reviewed/v1 contract metadata\n'
elif [[ "$contract_count" -eq 1 && "$contract_v2" -eq 1 ]]; then
  contract_version='v2'
  printf 'ok: human-reviewed/v2 contract metadata\n'
else
  note_fail 'expected exactly one supported "**Contract:** human-reviewed/v1|v2" declaration.'
fi

# v2 makes routing and durable-rationale ownership observable. Spike and Routine
# bypass acceptance planning, so a v2 plan may classify only Bounded or Architectural.
if [[ "$contract_version" == v2 ]]; then
  classification_count=$(grep -cE '^\*\*Work classification:\*\*' "$plan" || true)
  classification_line=$(grep -E '^\*\*Work classification:\*\*' "$plan" || true)
  classification=''
  if [[ "$classification_count" -eq 1 && "$classification_line" =~ ^\*\*Work[[:space:]]classification:\*\*[[:space:]]+(Bounded|Architectural)[[:space:]]+(-|–|—)[[:space:]]+[^[:space:]].*$ ]]; then
    classification=${BASH_REMATCH[1]}
    if printf '%s\n' "$classification_line" | grep -qiE '(^|[^[:alnum:]_])(TBD|TODO|PLACEHOLDER|REPLACE ME)([^[:alnum:]_]|$)|<[^>]+>'; then
      note_fail '"**Work classification:**" contains placeholder content.'
    else
      printf 'ok: %s work classification\n' "$classification"
    fi
  else
    note_fail 'v2 requires exactly one "**Work classification:** Bounded|Architectural — <rationale>" declaration.'
  fi

  decision_count=$(grep -cE '^\*\*Decision record:\*\*' "$plan" || true)
  decision_line=$(grep -E '^\*\*Decision record:\*\*' "$plan" || true)
  if [[ "$decision_count" -ne 1 ]] || printf '%s\n' "$decision_line" | grep -qiE '(^|[^[:alnum:]_])(TBD|TODO|PLACEHOLDER|REPLACE ME)([^[:alnum:]_]|$)|<[^>]+>'; then
    note_fail 'v2 requires exactly one non-placeholder "**Decision record:**" outcome.'
  elif [[ "$classification" == Bounded ]]; then
    if [[ "$decision_line" =~ ^\*\*Decision[[:space:]]record:\*\*[[:space:]]+None[[:space:]]+—[[:space:]]+[^[:space:]].*$ ]]; then
      printf 'ok: Bounded work creates no decision record\n'
    else
      note_fail 'Bounded work requires exactly "**Decision record:** None — <substantive rationale>".'
    fi
  elif [[ "$classification" == Architectural ]]; then
    if [[ "$decision_line" =~ ^\*\*Decision[[:space:]]record:\*\*[[:space:]]+\[ADR[[:space:]][0-9]{4}\]\((\.\./adr/[0-9]{4}-[^/\)]*\.md)\)$ ]]; then
      adr_target=${BASH_REMATCH[1]}
      plan_dir=$(cd "$(dirname "$plan")" && pwd)
      adr_path="$plan_dir/$adr_target"
      if [[ -f "$adr_path" ]]; then
        printf 'ok: Architectural work identifies an ADR\n'
      else
        note_fail "Architectural decision-record ADR target is not an existing regular file: $adr_target"
      fi
    else
      note_fail 'Architectural work requires exactly "**Decision record:** [ADR NNNN](../adr/NNNN-*.md)".'
    fi
  fi
fi

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

# --- (c) lifecycle and delivery declarations ----------------------------
status_count=$(grep -cE '^\*\*Status:\*\*' "$plan" || true)
status_line=$(grep -E '^\*\*Status:\*\*' "$plan" || true)
status=''
if [[ "$status_count" -eq 1 && "$status_line" =~ ^\*\*Status:\*\*[[:space:]]+(draft|proposed|approved|in-progress|landed|superseded)([,.[:space:]]|$) ]]; then
  status=${BASH_REMATCH[1]}
  printf 'ok: allowed status declaration\n'
else
  note_fail 'expected exactly one valid "**Status:**" prefix (allowed: draft, proposed, approved, in-progress, landed, superseded).'
fi

# Human decisions are deliberately bounded to one machine-readable section. Do not
# infer unresolved decisions from arbitrary prose elsewhere in the plan.
human_heading_count=$(grep -cE '^##[[:space:]]+Human decisions[[:space:]]*$' "$plan" || true)
human_block=$(awk '
  /^##[[:space:]]+Human decisions[[:space:]]*$/ { in_decisions = 1; next }
  in_decisions && /^#{1,6}[[:space:]]+/ { exit }
  in_decisions { print }
' "$plan")
human_lines=$(printf '%s\n' "$human_block" | grep -vE '^[[:space:]]*$' || true)
human_line_count=$(printf '%s\n' "$human_lines" | grep -cE '.' || true)

if [[ "$human_heading_count" -ne 1 ]]; then
  note_fail 'expected exactly one "## Human decisions" section.'
elif [[ "$human_line_count" -eq 0 ]]; then
  note_fail '"## Human decisions" must be non-empty.'
elif printf '%s\n' "$human_lines" | grep -qiE '(^|[^[:alnum:]_])(TBD|TODO|N/A|PLACEHOLDER|REPLACE ME)([^[:alnum:]_]|$)|<[^>]+>'; then
  note_fail '"## Human decisions" contains placeholder content.'
else
  none_count=$(printf '%s\n' "$human_lines" | grep -cE '^None[[:space:]]+(-|–|—)[[:space:]]+[^[:space:]].*$' || true)
  if [[ "$none_count" -eq 1 && "$human_line_count" -eq 1 ]]; then
    printf 'ok: no open human decisions, with rationale\n'
  else
    invalid_decision_lines=$(printf '%s\n' "$human_lines" | grep -vE '^- \[ \] [^[:space:]].*$|^- \[[xX]\] [^[:space:]].*[[:space:]](-|–|—)[[:space:]]+Decision:[[:space:]]+[^[:space:]].*$' || true)
    if [[ -n "$invalid_decision_lines" ]]; then
      note_fail 'human decisions must be unchecked checklist items or checked items ending "— Decision: <decision>".'
    else
      unchecked_count=$(printf '%s\n' "$human_lines" | grep -cE '^- \[ \] ' || true)
      if [[ "$unchecked_count" -gt 0 ]]; then
        if [[ "$status" == draft ]]; then
          printf 'ok: %s open human decision(s) keep plan in draft\n' "$unchecked_count"
        else
          note_fail 'unchecked human decisions require "**Status:** draft"; proposed, approved, in-progress, and landed require every decision resolved.'
        fi
      else
        printf 'ok: all human decisions are resolved and recorded\n'
      fi
    fi
  fi
fi

if grep -qE '^\*\*Delivery:\*\* (Split|Combined)([,.[:space:]]|$)' "$plan"; then
  printf 'ok: Split or Combined delivery declaration\n'
else
  note_fail 'missing or invalid "**Delivery:** Split|Combined" declaration.'
fi

combined=0
if grep -qE '^\*\*Delivery:\*\* Combined([,.[:space:]]|$)' "$plan"; then
  combined=1
fi

# Combined has machine-readable one-task eligibility metadata. Keep these as
# standalone fields so orchestration does not infer task count or rationale from prose.
if [[ "$combined" -eq 1 ]]; then
  expected_tasks_count=$(grep -cE '^\*\*Expected tasks:\*\*' "$plan" || true)
  expected_tasks_exact=$(grep -cE '^\*\*Expected tasks:\*\* 1$' "$plan" || true)
  if [[ "$expected_tasks_count" -eq 1 && "$expected_tasks_exact" -eq 1 ]]; then
    printf 'ok: Combined expected task count is exactly 1\n'
  else
    note_fail 'Combined delivery requires exactly one exact "**Expected tasks:** 1" declaration.'
  fi

  combined_rationale_count=$(grep -cE '^\*\*Combined rationale:\*\*' "$plan" || true)
  combined_rationale=$(grep -E '^\*\*Combined rationale:\*\*' "$plan" | head -n 1 || true)
  combined_rationale=${combined_rationale#'**Combined rationale:**'}
  combined_rationale=${combined_rationale#" "}
  combined_rationale_compact=${combined_rationale//[[:space:]]/}
  if [[ "$combined_rationale_count" -ne 1 || -z "$combined_rationale_compact" ]]; then
    note_fail 'Combined delivery requires exactly one non-empty "**Combined rationale:** <explanation>" declaration.'
  elif printf '%s\n' "$combined_rationale" | grep -qiE '^[[:space:]]*(TBD|TODO|N/A|None)[[:space:].]*$|<[^>]+>'; then
    note_fail 'Combined rationale must be a non-placeholder explanation of why separate plan review adds no value.'
  else
    printf 'ok: Combined rationale is populated\n'
  fi
fi

# --- (d) complete interface contract -------------------------------------
interface_block=$(awk '
  /^##[[:space:]]+Interface contract[[:space:]]*$/ { in_contract = 1; next }
  in_contract && /^#{1,6}[[:space:]]+/ { exit }
  in_contract { print }
' "$plan")

if grep -qE '^##[[:space:]]+Interface contract[[:space:]]*$' "$plan"; then
  printf 'ok: interface contract section present\n'
else
  note_fail 'missing "## Interface contract" section.'
fi

category_labels=(
  '- **gRPC / protobuf:**'
  '- **Exported Go APIs / interfaces:**'
  '- **Tool schemas:**'
  '- **CLI / config:**'
  '- **Events / persistence:**'
  '- **Security / authority:**'
  '- **Compatibility / migration:**'
)

for label in "${category_labels[@]}"; do
  category_count=$(printf '%s\n' "$interface_block" | awk -v label="$label" 'index($0, label) == 1 && substr($0, length(label) + 1, 1) == " " { count++ } END { print count + 0 }')
  if [[ "$category_count" -ne 1 ]]; then
    note_fail "expected exactly one canonical interface category: $label"
    continue
  fi

  category_line=$(printf '%s\n' "$interface_block" | awk -v label="$label" 'index($0, label) == 1 && substr($0, length(label) + 1, 1) == " " { print }')
  category_content=${category_line#"$label"}
  category_content=${category_content#" "}
  if [[ -z "$category_content" ]]; then
    note_fail "empty interface category: $label"
  elif printf '%s\n' "$category_content" | grep -qiE '(^|[^[:alnum:]_])TBD([^[:alnum:]_]|$)|<[^>]+>'; then
    note_fail "placeholder interface content: $label"
  elif [[ "$category_content" =~ ^None([[:space:]]*)$ ]]; then
    note_fail "bare None requires a dash and rationale: $label"
  elif [[ "$category_content" == None* ]] && ! [[ "$category_content" =~ ^None[[:space:]]+(-|–|—)[[:space:]]+[^[:space:]].* ]]; then
    note_fail "None declaration requires 'None — rationale' (hyphen, en dash, or em dash): $label"
  else
    printf 'ok: populated interface category %s\n' "$label"
  fi
done

# Combined is the workflow-only exception. Its six runtime-facing categories must
# explicitly be absent; compatibility/migration may describe the workflow change.
if [[ "$combined" -eq 1 ]]; then
  combined_labels=(
    '- **gRPC / protobuf:**'
    '- **Exported Go APIs / interfaces:**'
    '- **Tool schemas:**'
    '- **CLI / config:**'
    '- **Events / persistence:**'
    '- **Security / authority:**'
  )
  for label in "${combined_labels[@]}"; do
    category_line=$(printf '%s\n' "$interface_block" | awk -v label="$label" 'index($0, label) == 1 && substr($0, length(label) + 1, 1) == " " { print }')
    category_content=${category_line#"$label"}
    category_content=${category_content#" "}
    if ! [[ "$category_content" =~ ^None[[:space:]]+(-|–|—)[[:space:]]+[^[:space:]].* ]]; then
      note_fail "Combined delivery requires '$label None — <rationale>'"
    fi
  done
fi
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
  if [[ "$combined" -eq 1 && "$n" -ne 1 ]]; then
    note_fail "Combined delivery requires exactly one ### Scenario heading (found $n)."
  fi
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

# --- (e) out-of-scope section ------------------------------------------
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
  printf 'check-acceptance-plan: FAILED (hard absences above). Fix before opening the Plan / Interface PR.\n' >&2
  exit 1
fi
if [[ "$warn" -ne 0 ]]; then
  printf 'check-acceptance-plan: passed with warnings.\n'
else
  printf 'check-acceptance-plan: passed.\n'
fi
exit 0
