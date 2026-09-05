---
id: 07-api-and-documentation-reconciliation
title: Engine API and selected-evidence documentation reconciliation
blocked_by: [02-automatic-materialization-admission, 03-explicit-materialization-lifecycle, 04-selected-evidence-coordinator, 05-proposal-manifest-verification, 06-reflection-client-projection]
status: done
attempt: 1
branch: plan-scalable-reflection-evidence/07-api-and-documentation-reconciliation-attempt-1
worktree: .scratch/worker-scalable-reflection-evidence-07-api-and-documentation-reconciliation-attempt-1
issue: ""
retries: 0
last_error: ""
accumulator: acc/scalable-reflection-evidence
---

# Task brief

Perform the final single-writer reconciliation for intentional exported `engine/learning` evidence/provenance changes and selected-evidence behavior documentation. Run `task api:update`, review and retain only intentional `engine/api/*.txt` changes, classify them in `engine/CHANGELOG.md`, update living architecture and implementation notes, and update public user documentation if the explicit reflection behavior is user-visible. Do not edit frozen ADR 0300 or orchestrator-owned acceptance-plan status.

**Likely scope:** `engine/api/*.txt`, `engine/CHANGELOG.md`, `docs/architecture.md`, `docs/design/IMPLEMENTATION-NOTES.md`, focused `user-docs/` material, and generated configuration reference only through `task docs`.

**Invariants:** engine remains standalone and layering-clean; documentation describes one bounded selected-evidence unit, source-free outcomes, exact manifest-driven verification, and compatibility boundaries without exposing private source text or inventing protocol behavior. This is the only task to update API snapshots/changelog and living docs.

## Acceptance criteria

- AC8.6: The intentional exported `engine/learning` evidence/provenance changes are present in the API snapshots and classified in `engine/CHANGELOG.md`; both modules remain standalone and layering-clean.
  - verify: `TestScalableReflectionEvidence_Scenario8_EngineAPIAndLayeringGates`
