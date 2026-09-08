#!/usr/bin/env bash
set -euo pipefail

checker=$(cd "$(dirname "$0")" && pwd)/check-acceptance-plan.sh
root=.scratch/check-acceptance-plan-fixtures-$$
if [[ -e "$root" ]]; then
  printf 'fixture path already exists: %s\n' "$root" >&2
  exit 1
fi
mkdir -p "$root"

write_plan() {
  cat >"$1" <<'PLAN'
# Fixture — acceptance plan

**Contract:** human-reviewed/v1
**Status:** draft, fixture.
**Delivery:** Split. Default review path.
**Expected tasks:** deferred to orchestration

## Human decisions

None — the fixture leaves no decision for a human.

## Interface contract

- **gRPC / protobuf:** None — no wire change.
- **Exported Go APIs / interfaces:** None — no Go API change.
- **Tool schemas:** None — no tool change.
- **CLI / config:** None — no operator change.
- **Events / persistence:** None — no storage change.
- **Security / authority:** None — no authority change.
- **Compatibility / migration:** None — no migration required.

### Scenario 1 — Checker fixture

See [AGENTS.md](../../AGENTS.md) for the invariant.

- AC1.1: The fixture is accepted.
  - verify: inspection — shell fixture

## Out of scope

- Runtime behavior.
PLAN
}

expect_fail() {
  if bash "$checker" "$1" >/dev/null 2>&1; then
    printf 'expected checker failure: %s\n' "$1" >&2
    exit 1
  fi
}

valid="$root/valid.md"
write_plan "$valid"
bash "$checker" "$valid" >/dev/null

draft_unchecked="$root/draft-unchecked.md"
cp "$valid" "$draft_unchecked"
sed -i 's/None — the fixture leaves no decision for a human\./- [ ] Choose the fixture behavior./' "$draft_unchecked"
bash "$checker" "$draft_unchecked" >/dev/null

proposed_resolved="$root/proposed-resolved.md"
cp "$valid" "$proposed_resolved"
sed -i 's/\*\*Status:\*\* draft/\*\*Status:\*\* proposed/' "$proposed_resolved"
sed -i 's/None — the fixture leaves no decision for a human\./- [X] Choose the fixture behavior. — Decision: Keep the fixture deterministic./' "$proposed_resolved"
bash "$checker" "$proposed_resolved" >/dev/null

combined_valid="$root/combined-valid.md"
cp "$valid" "$combined_valid"
sed -i 's/\*\*Delivery:\*\* Split/\*\*Delivery:\*\* Combined/' "$combined_valid"
sed -i 's/\*\*Expected tasks:\*\* deferred to orchestration/\*\*Expected tasks:\*\* 1/' "$combined_valid"
sed -i '/\*\*Expected tasks:\*\* 1/a **Combined rationale:** The fixture is one indivisible documentation check, so separate plan review adds no value.' "$combined_valid"
bash "$checker" "$combined_valid" >/dev/null

for case_name in missing-contract invalid-contract missing-category bare-none placeholder bad-delivery bad-status proposed-unchecked missing-human placeholder-human checked-without-decision; do
  cp "$valid" "$root/$case_name.md"
done
for case_name in combined-material-category combined-multiple-scenarios combined-missing-expected-tasks combined-wrong-expected-tasks combined-missing-rationale combined-placeholder-rationale; do
  cp "$combined_valid" "$root/$case_name.md"
done
sed -i '/^\*\*Contract:\*\*/d' "$root/missing-contract.md"
sed -i 's/\*\*Contract:\*\* human-reviewed\/v1/\*\*Contract:\*\* human-reviewed\/v2/' "$root/invalid-contract.md"
sed -i '/Tool schemas/d' "$root/missing-category.md"
sed -i 's/None — no tool change\./None/' "$root/bare-none.md"
sed -i 's/None — no operator change\./TBD/' "$root/placeholder.md"
sed -i 's/\*\*Delivery:\*\* Split/\*\*Delivery:\*\* Later/' "$root/bad-delivery.md"
sed -i 's/\*\*Status:\*\* draft/\*\*Status:\*\* done/' "$root/bad-status.md"
sed -i 's/\*\*Status:\*\* draft/\*\*Status:\*\* proposed/' "$root/proposed-unchecked.md"
sed -i 's/None — the fixture leaves no decision for a human\./- [ ] Choose the fixture behavior./' "$root/proposed-unchecked.md"
sed -i '/^## Human decisions$/,/^## Interface contract$/ { /^## Human decisions$/d; /^None — the fixture leaves no decision for a human\.$/d; }' "$root/missing-human.md"
sed -i 's/None — the fixture leaves no decision for a human\./None — <rationale>/' "$root/placeholder-human.md"
sed -i 's/None — the fixture leaves no decision for a human\./- [x] Choose the fixture behavior./' "$root/checked-without-decision.md"
sed -i 's/None — no operator change\./Adds --fixture operator configuration./' "$root/combined-material-category.md"
cat >>"$root/combined-multiple-scenarios.md" <<'PLAN'

### Scenario 2 — A second scenario

See [AGENTS.md](../../AGENTS.md) for the invariant.

- AC2.1: The second fixture is rejected for Combined delivery.
  - verify: inspection — shell fixture
PLAN
sed -i '/\*\*Expected tasks:\*\* 1/d' "$root/combined-missing-expected-tasks.md"
sed -i 's/\*\*Expected tasks:\*\* 1/\*\*Expected tasks:\*\* 2/' "$root/combined-wrong-expected-tasks.md"
sed -i '/\*\*Combined rationale:\*\*/d' "$root/combined-missing-rationale.md"
sed -i 's/\*\*Combined rationale:\*\*.*/\*\*Combined rationale:\*\* TBD/' "$root/combined-placeholder-rationale.md"
for case_name in missing-contract invalid-contract missing-category bare-none placeholder bad-delivery bad-status proposed-unchecked missing-human placeholder-human checked-without-decision combined-material-category combined-multiple-scenarios combined-missing-expected-tasks combined-wrong-expected-tasks combined-missing-rationale combined-placeholder-rationale; do
  expect_fail "$root/$case_name.md"
done

adopted_plans=()
for plan in docs/acceptance/*.md; do
  if grep -qFx '**Contract:** human-reviewed/v1' "$plan"; then
    adopted_plans+=("$plan")
  fi
done
if [[ ${#adopted_plans[@]} -eq 0 ]]; then
  printf 'expected at least one adopted human-reviewed/v1 plan\n' >&2
  exit 1
fi
for plan in "${adopted_plans[@]}"; do
  bash "$checker" "$plan" >/dev/null
done

printf 'check-acceptance-plan fixtures: passed (%s)\n' "$root"
