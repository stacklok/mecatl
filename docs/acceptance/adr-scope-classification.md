# Development-spine work classification — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — this substantive workflow change routes work through existing review mechanisms without changing a durable runtime or architecture contract.
**Decision record:** None — the classifier narrows when existing acceptance plans and ADRs are used; its local process rationale is fully recorded here and in the living development-process guide.
**Phase:** development workflow
**Status:** landed, 2026-09-09. Candidate transition after focused checker fixtures, documentation gates, lint, full tests, and offline demo passed; authoritative only when the Combined PR merges.
**Delivery:** Combined. The workflow documents, skills, template, and checker are the complete process interface under review.
**Expected tasks:** 1
**Combined rationale:** The classifier and its deterministic validation form one indivisible workflow-only change, so a separate plan PR would review the same artifact twice without adding review value.

The repository gains one observable classifier that reduces unnecessary acceptance plans and
ADRs while retaining human approval for substantive work and durable rationale where it
belongs.

## Human decisions

None — the agreed four classes, workflow mapping, ADR threshold, legacy compatibility, and human carve-outs are fully specified.

## Interface contract

- **gRPC / protobuf:** None — the development workflow does not change wire contracts.
- **Exported Go APIs / interfaces:** None — no Go source or exported API changes.
- **Tool schemas:** None — no runtime model-facing tool changes.
- **CLI / config:** None — no binary flag, setting, default, or precedence changes.
- **Events / persistence:** None — no runtime event, snapshot, store, or migration changes.
- **Security / authority:** None — runtime trust and permission boundaries are unchanged; existing human merge, explicit Spike, and explicit waiver authority remain intact.
- **Compatibility / migration:** None — `human-reviewed/v1` plans remain valid, old plans are not bulk-edited, and v2 applies only to new or materially amended plans.

## In scope — 1 scenario, in implementation order

### Scenario 1 — Contributors route work without manufacturing ADRs

The canonical [development process](../development-process.md) defines Spike, Routine,
Bounded, and Architectural work, while the concise [agent contract](../../AGENTS.md) routes to
that source instead of duplicating policy.

**Acceptance:**
- AC1.1: Spike and Routine bypass acceptance planning; Bounded and Architectural use it, and uncertainty never silently downgrades the class. Routine means no new durable decision plus an established/mechanical reversible transformation, regardless of locality or file count; a repository-wide mechanical rename remains Routine when it changes no durable contract.
  - verify: inspection — compare the development process, onboarding, and both spine skills.
- AC1.2: New and materially amended plans use v2 classification and decision-record metadata, while valid legacy v1 plans continue to pass.
  - verify: `.claude/skills/to-acceptance-plan/scripts/check-acceptance-plan-test.sh`
- AC1.3: Deterministic fixtures accept valid Bounded and Architectural outcomes and reject missing, placeholder, or mismatched classification/decision-record fields.
  - verify: `.claude/skills/to-acceptance-plan/scripts/check-acceptance-plan-test.sh`
- AC1.4: Only Architectural work introducing or superseding a genuinely durable decision creates an ADR; local rationale, current behavior, procedure, temporary state, and current invariants retain distinct homes.
  - verify: inspection — compare the ADR index/template, development process, plan authoring skill, and test-writing skill.
- AC1.5: Split/Combined eligibility remains orthogonal, Plan PR merge remains approval, and explicit human Spike/waiver carve-outs remain unchanged.
  - verify: inspection — compare the development process, onboarding, and orchestration skills.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Runtime, public, operator, persistence, or trust behavior | future classified work | This change only routes repository development work. |
| Reclassifying or bulk-editing historical plans and ADRs | not planned | Preserve existing records and v1 compatibility. |
| A new ADR for this workflow adjustment | not applicable | The work is Bounded and introduces no durable architecture decision. |

## Definition of done

1. The focused checker fixtures, `task docs`, `task lint`, `task test`, and offline demo pass.
2. The acceptance index links this plan.
3. No frozen ADR, including ADR 0306, is edited.
4. The candidate records one coherent task and no runtime/public/operator/persistence/trust change.

## Deferred decisions and known risks

- None — unexpected evidence that this changes a durable architecture contract is contract drift and requires reclassification rather than silent downgrade.
