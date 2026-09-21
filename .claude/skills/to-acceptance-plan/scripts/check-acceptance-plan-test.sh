#!/usr/bin/env bash
set -euo pipefail

checker=$(cd "$(dirname "$0")" && pwd)/check-acceptance-plan.sh
root=.scratch/check-acceptance-plan-fixtures-$$
if [[ -e "$root" ]]; then
  printf 'fixture path already exists: %s\n' "$root" >&2
  exit 1
fi
mkdir -p "$root/acceptance" "$root/adr"
cat >"$root/adr/9999-fixture-architectural-decision.md" <<'ADR'
# ADR 9999 — fixture architectural decision
ADR

write_plan() {
  cat >"$1" <<'PLAN'
# Fixture — acceptance plan

**Contract:** human-reviewed/v1
**Status:** draft, fixture.
**Delivery:** Split. Default review path.
**Expected tasks:** deferred to orchestration
<!-- combined rationale fixture placeholder -->

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

sed_in_place() {
  if command sed --version >/dev/null 2>&1; then
    command sed -i "$@"
    return
  fi
  command sed -i '' "$@"
}

valid="$root/acceptance/valid-v1.md"
write_plan "$valid"
bash "$checker" "$valid" >/dev/null

# A large interface block must not trip pipefail when a first-match consumer
# finishes before the producer has drained its command-substitution buffer.
large_interface="$root/acceptance/valid-large-interface.md"
awk '
  { print }
  /^- \*\*Compatibility \/ migration:\*\*/ {
    for (i = 0; i < 20000; i++) {
      print "  Continuation line " i ": deterministic interface detail used to exceed the pipe buffer."
    }
  }
' "$valid" >"$large_interface"
bash "$checker" "$large_interface" >/dev/null

bounded_valid="$root/acceptance/valid-bounded-v2.md"
cp "$valid" "$bounded_valid"
sed_in_place 's/human-reviewed\/v1/human-reviewed\/v2/' "$bounded_valid"
sed_in_place '/^\*\*Status:\*\*/a\
**Work classification:** Bounded — this substantive fixture changes only local workflow validation.\
**Decision record:** None — it introduces no durable architecture decision.' "$bounded_valid"
bash "$checker" "$bounded_valid" >/dev/null

architectural_valid="$root/acceptance/valid-architectural-v2.md"
cp "$bounded_valid" "$architectural_valid"
sed_in_place 's/Bounded — this substantive fixture changes only local workflow validation\./Architectural — this fixture changes a durable public contract./' "$architectural_valid"
sed_in_place 's|None — it introduces no durable architecture decision\.|[ADR 9999](../adr/9999-fixture-architectural-decision.md)|' "$architectural_valid"
bash "$checker" "$architectural_valid" >/dev/null

draft_unchecked="$root/acceptance/draft-unchecked.md"
cp "$valid" "$draft_unchecked"
sed_in_place 's/None — the fixture leaves no decision for a human\./- [ ] Choose the fixture behavior./' "$draft_unchecked"
bash "$checker" "$draft_unchecked" >/dev/null

proposed_resolved="$root/acceptance/proposed-resolved.md"
cp "$valid" "$proposed_resolved"
sed_in_place 's/\*\*Status:\*\* draft/\*\*Status:\*\* proposed/' "$proposed_resolved"
sed_in_place 's/None — the fixture leaves no decision for a human\./- [X] Choose the fixture behavior. — Decision: Keep the fixture deterministic./' "$proposed_resolved"
bash "$checker" "$proposed_resolved" >/dev/null

combined_valid="$root/acceptance/combined-valid.md"
cp "$valid" "$combined_valid"
sed_in_place 's/\*\*Delivery:\*\* Split/\*\*Delivery:\*\* Combined/' "$combined_valid"
sed_in_place 's/\*\*Expected tasks:\*\* deferred to orchestration/\*\*Expected tasks:\*\* 1/' "$combined_valid"
sed_in_place 's|<!-- combined rationale fixture placeholder -->|**Combined rationale:** The fixture is one indivisible documentation check, so separate plan review adds no value.|' "$combined_valid"
bash "$checker" "$combined_valid" >/dev/null

for case_name in missing-contract invalid-contract missing-category bare-none placeholder bad-delivery bad-status proposed-unchecked missing-human placeholder-human checked-without-decision; do
  cp "$valid" "$root/acceptance/$case_name.md"
done
for case_name in missing-classification placeholder-classification missing-decision-record placeholder-decision-record bounded-with-adr architectural-with-none; do
  cp "$bounded_valid" "$root/acceptance/$case_name.md"
done
for case_name in architectural-none-with-adr architectural-missing-adr-target; do
  cp "$architectural_valid" "$root/acceptance/$case_name.md"
done
for case_name in combined-material-category combined-multiple-scenarios combined-missing-expected-tasks combined-wrong-expected-tasks combined-missing-rationale combined-placeholder-rationale; do
  cp "$combined_valid" "$root/acceptance/$case_name.md"
done
sed_in_place '/^\*\*Contract:\*\*/d' "$root/acceptance/missing-contract.md"
sed_in_place 's/\*\*Contract:\*\* human-reviewed\/v1/\*\*Contract:\*\* human-reviewed\/v2/' "$root/acceptance/invalid-contract.md"
sed_in_place '/Tool schemas/d' "$root/acceptance/missing-category.md"
sed_in_place 's/None — no tool change\./None/' "$root/acceptance/bare-none.md"
sed_in_place 's/None — no operator change\./TBD/' "$root/acceptance/placeholder.md"
sed_in_place 's/\*\*Delivery:\*\* Split/\*\*Delivery:\*\* Later/' "$root/acceptance/bad-delivery.md"
sed_in_place 's/\*\*Status:\*\* draft/\*\*Status:\*\* done/' "$root/acceptance/bad-status.md"
sed_in_place 's/\*\*Status:\*\* draft/\*\*Status:\*\* proposed/' "$root/acceptance/proposed-unchecked.md"
sed_in_place 's/None — the fixture leaves no decision for a human\./- [ ] Choose the fixture behavior./' "$root/acceptance/proposed-unchecked.md"
sed_in_place '/^## Human decisions$/,/^## Interface contract$/ { /^## Human decisions$/d; /^None — the fixture leaves no decision for a human\.$/d; }' "$root/acceptance/missing-human.md"
sed_in_place 's/None — the fixture leaves no decision for a human\./None — <rationale>/' "$root/acceptance/placeholder-human.md"
sed_in_place 's/None — the fixture leaves no decision for a human\./- [x] Choose the fixture behavior./' "$root/acceptance/checked-without-decision.md"
sed_in_place '/^\*\*Work classification:\*\*/d' "$root/acceptance/missing-classification.md"
sed_in_place 's/Bounded — this substantive fixture changes only local workflow validation\./Bounded — TBD/' "$root/acceptance/placeholder-classification.md"
sed_in_place '/^\*\*Decision record:\*\*/d' "$root/acceptance/missing-decision-record.md"
sed_in_place 's/None — it introduces no durable architecture decision\./None — TBD/' "$root/acceptance/placeholder-decision-record.md"
sed_in_place 's|None — it introduces no durable architecture decision\.|[ADR 9999](../adr/9999-fixture-architectural-decision.md)|' "$root/acceptance/bounded-with-adr.md"
sed_in_place 's/Bounded — this substantive fixture changes only local workflow validation\./Architectural — this fixture changes a durable public contract./' "$root/acceptance/architectural-with-none.md"
sed_in_place 's|\*\*Decision record:\*\* \[ADR 9999\](../adr/9999-fixture-architectural-decision.md)|**Decision record:** None — a bypass attempt that embeds [ADR 9999](../adr/9999-fixture-architectural-decision.md)|' "$root/acceptance/architectural-none-with-adr.md"
sed_in_place 's|9999-fixture-architectural-decision.md|9998-nonexistent-architectural-decision.md|' "$root/acceptance/architectural-missing-adr-target.md"
sed_in_place 's/None — no operator change\./Adds --fixture operator configuration./' "$root/acceptance/combined-material-category.md"
cat >>"$root/acceptance/combined-multiple-scenarios.md" <<'PLAN'

### Scenario 2 — A second scenario

See [AGENTS.md](../../AGENTS.md) for the invariant.

- AC2.1: The second fixture is rejected for Combined delivery.
  - verify: inspection — shell fixture
PLAN
sed_in_place '/\*\*Expected tasks:\*\* 1/d' "$root/acceptance/combined-missing-expected-tasks.md"
sed_in_place 's/\*\*Expected tasks:\*\* 1/\*\*Expected tasks:\*\* 2/' "$root/acceptance/combined-wrong-expected-tasks.md"
sed_in_place '/\*\*Combined rationale:\*\*/d' "$root/acceptance/combined-missing-rationale.md"
sed_in_place 's/\*\*Combined rationale:\*\*.*/\*\*Combined rationale:\*\* TBD/' "$root/acceptance/combined-placeholder-rationale.md"
for case_name in missing-contract invalid-contract missing-category bare-none placeholder bad-delivery bad-status proposed-unchecked missing-human placeholder-human checked-without-decision missing-classification placeholder-classification missing-decision-record placeholder-decision-record bounded-with-adr architectural-with-none architectural-none-with-adr architectural-missing-adr-target combined-material-category combined-multiple-scenarios combined-missing-expected-tasks combined-wrong-expected-tasks combined-missing-rationale combined-placeholder-rationale; do
  expect_fail "$root/acceptance/$case_name.md"
done

adopted_plans=()
for plan in docs/acceptance/*.md; do
  if grep -qE '^\*\*Contract:\*\* human-reviewed/v(1|2)$' "$plan"; then
    adopted_plans+=("$plan")
  fi
done
if [[ ${#adopted_plans[@]} -eq 0 ]]; then
  printf 'expected at least one adopted human-reviewed plan\n' >&2
  exit 1
fi
for plan in "${adopted_plans[@]}"; do
  bash "$checker" "$plan" >/dev/null
done

printf 'check-acceptance-plan fixtures: passed (%s)\n' "$root"
