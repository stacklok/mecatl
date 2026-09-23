# Acceptance-plan template

Copy the skeleton into `docs/acceptance/<slug>.md` only for Bounded or Architectural work as
classified by the sibling bundled
[`WORK-CLASSIFICATION.md`](WORK-CLASSIFICATION.md). Keep
focused plans compact. New and materially amended plans use exact
`**Contract:** human-reviewed/v2` metadata. Existing v1 plans remain valid until materially
amended. The checker requires a matching work-classification and decision-record outcome,
a scenario, numbered ACs with `verify:` lines, citations, an out-of-scope section, an allowed
status, a non-empty `## Human decisions` section, a Split/Combined declaration, and all seven
interface-category labels with non-placeholder content. Bounded plans create no ADR merely
to describe the change; Architectural plans link the new or superseding durable decision.
Existing ADRs may be cited by either class. Human decisions use either `None — <rationale>` or
checklist items; unchecked items require `draft`, while every later status requires checked
items ending `— Decision: <decision>`. Combined eligibility is orthogonal to classification:
it is a compact one-task, exactly-one-`### Scenario` exception and must declare parseable
`**Expected tasks:** 1` plus a non-placeholder `**Combined rationale:**` explaining why
separate plan review adds no value. gRPC/protobuf, exported Go APIs/interfaces, tool schemas,
CLI/config, events/persistence, and security/authority must each begin
`None — <rationale>`; compatibility/migration may describe workflow migration. A workflow-only
meta-change may instead review process documents and skills as its interface in the same PR.

```markdown
# <Name> — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** <Bounded|Architectural> — <decision/blast-radius rationale>
**Decision record:** <For Bounded: None — substantive rationale; for Architectural: linked new or superseding ADR>
**Phase:** <capability / milestone>
**Status:** draft, <YYYY-MM-DD>. <provenance>
**Delivery:** Split. <why the default two-PR path applies>
**Expected tasks:** <positive count or deferred to orchestration>
<!-- For Combined, the two fields must instead be exactly:
**Expected tasks:** 1
**Combined rationale:** <non-placeholder explanation why separate plan review adds no value>
-->
<!-- Or: **Delivery:** Combined. <why gRPC/protobuf, exported Go APIs/interfaces, tool schemas, CLI/config, events/persistence, and security/authority begin None — rationale; compatibility/migration may describe workflow migration; workflow-only meta-changes may declare their process interface> -->
**Issue:** [stacklok/mecatl#NN](https://github.com/stacklok/mecatl/issues/NN).
**Plan PR:** <added when opened>
**Approved baseline:** <merged plan commit; absent until approved>

<One or two paragraphs defining the smallest demonstrable behavior.>

## Human decisions

<!-- Choose exactly one shape. Any unchecked item requires **Status:** draft. -->
None — <rationale why no human judgment remains>
<!-- Or:
- [ ] <decision a human must make>
- [x] <resolved decision> — Decision: <recorded outcome>
-->

## Interface contract

Public or material choices must be exact; do not defer them to implementation. Use
`None — <rationale>` only when the category is genuinely unaffected.

- **gRPC / protobuf:** <exact messages, fields, methods, numbers, compatibility; or None + rationale>
- **Exported Go APIs / interfaces:** <exact packages, symbols, signatures; or None + rationale>
- **Tool schemas:** <exact tool names and input/output schema changes; or None + rationale>
- **CLI / config:** <exact flags, keys, defaults, precedence; or None + rationale>
- **Events / persistence:** <exact event/persisted fields and migration; or None + rationale>
- **Security / authority:** <exact trust, permission, secret, ownership boundaries; or None + rationale>
- **Compatibility / migration:** <compatibility classification and rollout/migration; or None + rationale>

## In scope — <N> scenarios, in implementation order

### Scenario 1 — <observable outcome>

<Narrative with a link to an ADR, architecture, implementation notes, or AGENTS.md.>

**Acceptance:**
- AC1.1: <present-tense observable behavior>.
  - verify: `Test<Plan>_Scenario1_<Name>`
- AC1.2: <negative or edge behavior>.
  - verify: inspection — <why inspection is the right proof>

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| <item> | <phase/later> | <ADR or rationale> |

## Definition of done

1. Applicable `task lint`, `task test:race`, `task docs`, and `task api:check` gates pass on the final candidate.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green for runtime changes.
4. The implementation PR links the Plan / Interface PR and approved commit and reports
   interface conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- <Only non-material implementation detail may remain. Material behavior or interface
  choices belong in Human decisions and keep the plan in draft while unchecked.>
```

Lifecycle: `draft → proposed → approved → in-progress → landed`. `draft` may carry unchecked
human decisions. `proposed` means the section declares `None — <rationale>` or every decision
needed to implement the contract is checked and records its outcome; it is then ready for
review. Split plans are marked
`proposed` for the plan PR; merging the PR is the approval event, with no separate status-line
edit required. Orchestration proves approval by git ancestry and may correct a lagging
`proposed` label to `approved` on entry. `approved` is not shipped. For
Combined, `/to-acceptance-plan` prepares this plan on the eventual implementation branch
without opening a plan PR; explicit `/plan-orchestrate` invocation adds the one-task
implementation and opens the sole Combined PR. After every gate passes, the completing
candidate puts `landed` in its PR diff; the target branch keeps its prior state until merge.

Citation paths are relative to `docs/acceptance/`: ADR
`[ADR 0036](../adr/0036-engine-module.md)`, architecture
`[architecture](../architecture.md)`, and `[AGENTS.md](../../AGENTS.md)`.

Run:

```sh
bash .claude/skills/to-acceptance-plan/scripts/check-acceptance-plan.sh docs/acceptance/<slug>.md
```

[← back to the skill](../SKILL.md)
