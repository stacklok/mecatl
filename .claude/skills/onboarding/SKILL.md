---
name: onboarding
description: >-
  Route contributors through mecatl's human-reviewed development spine: contract PR,
  approved baseline, autonomous TDD implementation, final code-review PR. A router, not
  an executor. Use for onboarding or workflow questions.
---

# onboarding

Read `AGENTS.md`, `docs/architecture.md`, relevant `docs/adr/` records, and
`docs/development-process.md` before changing the repository.

## The spine

Substantive interface-bearing work uses two human checkpoints:

1. **`/to-acceptance-plan`** writes `docs/acceptance/<slug>.md`, including exact
   interfaces and verifiable behavior, opens a **Plan / Interface** PR, then stops.
2. **Human contract review** marks the plan `approved` and merges it. Approved means
   reviewed, not shipped.
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
Trivial/mechanical edits remain exempt. Every path preserves human merge authority.

Issue references on plan PRs are non-closing (`Relates to #N` or `Tracking: #N`). Only a
final implementation PR that fully completes the issue uses `Closes #N` or `Fixes #N`.
Contract drift blocks orchestration; only a separately, explicitly authorized
`/to-acceptance-plan` amendment mode may open the required Split Plan / Interface PR for
human approval and merge before work resumes.

## Specialists

- `/panel-review` — final Spec / Standards / Test adequacy / Domain review.
- `/test-writer` — invariant-first failing tests.
- `/cut-release` — release workflow.
- `/perf-optimization` and `/perf-mcp-interpretation` — performance work.
- `/mecatl-model-router-config` — model-routing configuration.

This skill only routes. See `docs/development-process.md` and
`docs/acceptance/README.md` for the complete contract.
