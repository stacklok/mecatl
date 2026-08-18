---
id: 05-cleanup-planner
title: Retention planner and safe cleanup API
blocked_by: [02-indexed-inventory, 02b-bounded-pagination, 02c-inventory-locking]
status: done
branch: "repair/05-cleanup-integration"
worktree: ""
issue: "590"
retries: 0
last_error: ""
accumulator: acc/session-storage-continuity
---

# Task brief

Extract one deterministic planner shared by automatic retention and manual cleanup. Add caller-bound generation/policy plan tokens, management authorization, mutation-time lease/state revalidation, safe partial failure, and no-oracle projections.

Follow ADR-0226, ADR-0217, ADR-0027, ADR-0104, AGENTS.md layering/security invariants, and existing repository conventions. Keep tests offline. Do not absorb later tasks or weaken fail-closed behavior. Update the living docs and user-docs affected by this task.

## Acceptance criteria

- AC5.1: Dry-run writes nothing and reports eligible/protected counts by durable kind/state, age/cap reason, modified time, and estimated bytes without transcript, tool-argument, secret, or foreign-owner content.
  - verify: `TestSessionStorageContinuity_Scenario5_CleanupDryRunIsReadOnly`
- AC5.2: Unknown, invalid, corrupt, running, awaiting, live, and leased sessions are protected by default and do not consume eligible-main cap slots.
  - verify: `TestInvariant_retention_requires_durable_taxonomy`
- AC5.3: Apply requires an opaque confirmation token bound to the authenticated management principal, catalog generation, exact scope, and effective retention-policy version; it acquires the family lock and maintenance/run-entry lease in documented order, rechecks ownership, kind, state, liveness, and lease, and holds both exclusions through sidecar-first/snapshot-last deletion. Cross-caller replay is existence-indistinguishable; changed candidates or policy return an explicit stale-plan result and are skipped.
  - verify: `TestSessionStorageContinuity_Scenario5_ApplyRevalidatesPlan`, `TestSessionStorageContinuity_Scenario5_CrossCallerPlanReplayDenied`
- AC5.4: Partial failure is accurately reported with bounded stable reason codes and sanitized messages, and is retryable; unsupported backends report unsupported rather than zero impact, and no raw backend error/path/content/secret reaches API, diagnostics, durable job state, or TUI.
  - verify: `TestSessionStorageContinuity_Scenario5_PartialFailureAndUnsupported`, `TestSessionStorageContinuity_Scenario5_CleanupErrorsAreSanitized`
- AC5.5: Automatic sweeps and manual plans select the same candidates for the same policy and metadata generation.
  - verify: `TestSessionStorageContinuity_Scenario5_AutomaticManualPlannerParity`
- AC5.6: Age/cap selection is deterministic across adapters and catalog rebuilds, ordered oldest-first by `(modified_at ASC, session_id ASC)` for equal timestamps.
  - verify: `TestSessionStorageContinuity_Scenario5_DeterministicCleanupOrdering`
- AC5.7: Plan, apply, cancel, and store-wide health/job inspection require the advertised management authority; unauthorized and foreign-owner requests reveal neither session existence, IDs, paths, owner data, nor aggregate maintenance scope.
  - verify: `TestSessionStorageContinuity_Scenario5_ManagementAuthorizationAndNoOracle`
