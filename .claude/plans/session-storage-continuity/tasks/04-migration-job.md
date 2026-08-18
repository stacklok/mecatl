---
id: 04-migration-job
title: Resumable storage migration and compaction
blocked_by: [01-v2-snapshots, 01b-v2-durability, 01c-v2-temp-locking, 02-indexed-inventory, 02b-bounded-pagination, 02c-inventory-locking]
status: done
branch: "plan-session-storage-continuity/04-migration-job"
worktree: ""
issue: "589"
retries: 0
last_error: ""
accumulator: acc/session-storage-continuity
---

# Task brief

Add authenticated server-side physical v1-to-v2 plan/apply jobs with bounded batches, durable progress, per-family crash safety, lease/liveness revalidation, sanitized failures, and no semantic reclassification.

Follow ADR-0226, ADR-0217, ADR-0027, ADR-0104, AGENTS.md layering/security invariants, and existing repository conventions. Keep tests offline. Do not absorb later tasks or weaken fail-closed behavior. Update the living docs and user-docs affected by this task.

## Acceptance criteria

- AC4.1: Dry-run reports v1/v2/invalid/skipped family counts, current bytes, estimated reclaimable bytes, and temporary-space requirements without writing.
  - verify: `TestSessionStorageContinuity_Scenario4_MigrationDryRunIsReadOnly`
- AC4.2: Apply processes bounded batches with durable progress, cancellation, resumability, per-item errors, and idempotent convergence; cancellation stops future items and does not roll back committed items.
  - verify: `TestSessionStorageContinuity_Scenario4_ResumableMigrationJob`
- AC4.3: Each family is verified readable as v2 before v1 removal; a crash or insufficient space leaves v1 or verified v2 authoritative, with peak extra space bounded to one family.
  - verify: `TestSessionStorageContinuity_Scenario4_PerFamilyCrashSafety`
- AC4.4: Migration preserves logical modification order, full snapshot semantics, tool/event sidecars, owner, and durable kind, including `unknown`; corrupt/torn records are reported or quarantined, never silently discarded.
  - verify: `TestSessionStorageContinuity_Scenario4_MigrationPreservesSemantics`
- AC4.5: Before each family mutation, migration acquires the cross-process family lock and maintenance/run-entry lease in the documented lock order, revalidates owner scope, durable kind, state, liveness, and lease, and holds both exclusions through complete promotion/removal; a changed or active family is skipped without discarding its newest write.
  - verify: `TestSessionStorageContinuity_Scenario4_MigrationRevalidatesUnderLease`
- AC4.6: Migration plan/apply/cancel/resume/job inspection require the advertised management authority; plans and resumable job handles bind the authenticated management principal, and unauthorized/cross-caller requests reveal neither family existence, IDs, paths, owner data, nor aggregate scope.
  - verify: `TestSessionStorageContinuity_Scenario4_MigrationAuthorizationAndNoOracle`
- AC4.7: Per-item failures and job status expose bounded stable reason codes plus sanitized messages; APIs, diagnostics, durable job state, and TUI projections never carry raw backend errors, transcript/tool content, secret-shaped data, or unneeded filesystem paths.
  - verify: `TestSessionStorageContinuity_Scenario4_MaintenanceErrorsAreSanitized`
