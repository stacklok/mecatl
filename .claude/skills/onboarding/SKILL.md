---
name: onboarding
description: >-
  Route contributors by work class to direct implementation or mecatl's human-reviewed
  development spine. A router, not an executor. Use for onboarding or workflow questions.
---

# onboarding

Read `AGENTS.md`, `docs/architecture.md`, and relevant `docs/adr/` records before changing the
repository.

## Route by work class

Classify by decision and blast radius, never diff size:

- **Spike:** explicit-question evidence gathering that is not shipped as-is. It bypasses the
  spine only with explicit human authorization and must be reclassified before shipping.
- **Routine:** an established or mechanical, reversible change with no new durable decision or
  durable-contract change; it bypasses acceptance planning regardless of file count.
- **Cleanup:** removal of obsolete aliases, shims, duplicate representations, or legacy readers
  in favor of established canonical behavior after the operator has chosen the breaking-state
  treatment. Implement it directly and record the scope, current contract, and approved treatment
  in the implementation PR. Interface count and diff size do not require a separate plan PR or
  formulaic waiver. New product behavior, new design choices, and unresolved data destruction
  require a real decision and classification outside Cleanup.
- **Bounded:** substantive contract work with no durable architecture decision; use the spine.
- **Architectural:** a durable public/API, persistence/data ownership, security/trust,
  deployment/operator, module/system-boundary, or cross-subsystem-invariant decision; use the
  spine and a new or superseding ADR.

If the lower class is not supported by evidence, escalate rather than silently downgrade. Choose
Split or Combined only after classification: it is a delivery choice, not a work class. For
classification detail and plan drafting, use `/to-acceptance-plan`.

## The spine

Bounded and Architectural work use two human checkpoints:

1. **`/to-acceptance-plan`** writes `docs/acceptance/<slug>.md`, including exact
   `**Contract:** human-reviewed/v2` metadata, a Bounded/Architectural classification with
   matching decision-record outcome, interfaces, verifiable behavior, and a machine-readable
   `## Human decisions` section.
   Unchecked decisions keep it `draft`; `proposed` means every human decision needed for
   implementation is resolved and recorded. It opens a **Plan / Interface** PR, then stops.
2. **Human contract review** merges the plan PR. Merging is the approval event;
   no separate status-line edit is required. `/plan-orchestrate` proves approval by git
   ancestry and corrects a lagging `proposed` label on entry.
3. **`/plan-orchestrate <slug>`** starts from the merged approved commit, uses run-local
   `.scratch/orchestrate/<slug>/` state and isolated TDD workers, gates and panel-reviews
   the implementation, then opens only the **Implementation** PR.
4. **Human code review** merges the implementation.

Combined delivery is a narrow exception for a compact one-task plan with exactly one
`### Scenario`, exact `**Expected tasks:** 1` metadata, and a non-placeholder
`**Combined rationale:**` explaining why separate plan review adds no value. gRPC/protobuf,
exported Go APIs/interfaces, tool schemas, CLI/config, events/persistence, and
security/authority each begin `None — <rationale>`.
Compatibility/migration may describe workflow migration, and splitting must add no review
value. A workflow-only meta-change may review its process-document/skill interface in the
same PR. `/to-acceptance-plan` prepares
the plan on the eventual combined branch and stops without opening a plan PR; only an
explicit `/plan-orchestrate` invocation adds implementation and opens the sole Combined PR.
Routine and Cleanup work remain exempt. Every path preserves human merge authority.

Two carve-outs put discretion with the human, never the agent: explicitly
requested **exploratory/spike work** skips the plan and orchestration entirely — build it
locally, never merge it to `main` as-is, and re-enter the spine normally if it's worth
shipping. Separately, the directing human may **explicitly waive the spine** for a named
piece of work; the waiver lifts only the plan/interface ceremony, not the layering rules, the
AGENTS.md invariants, or the human-merge requirement. Do not infer either carve-out yourself
or push the spine onto a request that already named one.

Issue references on plan PRs are non-closing (`Relates to #N` or `Tracking: #N`). Only a
final implementation PR that fully completes the issue uses `Closes #N` or `Fixes #N`.
Contract drift, including a worker discovering an unrecorded human decision, blocks
orchestration; the worker does not make that decision. Only a separately, explicitly
authorized `/to-acceptance-plan` amendment mode may open the required Split Plan / Interface
PR for human approval and merge before work resumes.

## Specialists

- `/panel-review` — final Spec / Standards / Test adequacy / Domain review.
- `/test-writer` — invariant-first failing tests.
- `/cut-release` — release workflow.
- `/perf-optimization` and `/perf-mcp-interpretation` — performance work.
- `/mecatl-model-router-config` — model-routing configuration.

This skill only routes. Use `/to-acceptance-plan` for classification detail and acceptance-plan
authoring, and `docs/acceptance/README.md` for acceptance-plan repository navigation.
