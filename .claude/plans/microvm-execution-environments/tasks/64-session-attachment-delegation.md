---
id: 64-session-attachment-delegation
title: Attach sessions and delegated children to repository-scoped execution
blocked_by: [63-logical-worktree-protocol]
status: done
branch: "plan-microvm-execution-environments/64-session-attachment-delegation"
worktree: ""
issue: "526"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Session attachment and delegation

Wire session and schedule creation and reattachment to reuse the canonical repository VM while
allocating distinct logical refs and worktrees. Keep direct-write children on the parent Environment
and allocate distinct worktrees for isolated Subagents, Parallel branches, and Team members.
Schedules borrow an origin placement or own one independently provisioned placement, then preserve
the exact ref across fires, restart, ownership handoff, and deletion. Close detaches without destroying
shared state. Reuse the existing basic isolated-child merge behavior; do not add daemon-wide
cross-process serialization or crash-durable merge recovery.

## Acceptance criteria

- AC2.2: Concurrent first use durably converges on one VM/rootfs record for the canonical key; an exact healthy generation reattaches only while every owned dependency remains live in-process, while daemon restart or other missing/inconsistent runtime state fails loudly without minting a replacement and preserves the durable record/rootfs/worktrees.
  - verify: `TestMicroVMMVP_Scenario2_DurableSingletonRegistryReattachesOrFailsLoudly`
- AC5.1: Sessions in one repository attach to the same repository VM with distinct logical refs and worktrees; a direct-write child reuses its parent's logical Environment, while a read-only Subagent, Parallel branch, or Team member receives a distinct logical ref and worktree in that VM.
  - verify: `TestMicroVMOperatorJourneyIsLazyIsolatedAndRestartExact`, `TestMicroVMMVP_Scenario5_SessionsAndChildrenReuseRepositoryVM`
- AC5.2: Closing a session or child detaches its process-local handles without destroying the repository VM, rootfs, shared cache, or another attached logical environment.
  - verify: `TestMicroVMMVP_Scenario5_CloseDetachesWithoutDestroyingRepositoryVM`
- AC5.3: The existing isolated-child merge path applies a non-conflicting child change and preserves the child on conflict; the MVP makes no cross-process serialization or crash-recovery claim.
  - verify: `TestMicroVMMVP_Scenario5_BasicExistingMergeBehavior`
- AC5.4: Production status inventories repository logical attachments—not the superseded session-per-VM registry—in deterministic pages of at most 64, showing two distinct logical worktrees on one repository generation; exact logical deletion removes clean state, retains dirty state for recovery, and never implies repository-VM deletion.
  - verify: `TestRepositoryProductionInventoryPaginationAndLogicalDelete`
- AC5.5: Schedule creation durably pins one exact placement: an origin-backed schedule borrows its session's logical worktree, while an independent MicroVM schedule provisions and owns one logical worktree. Updates cannot change placement or ownership, and every fire exactly reauthorizes the persisted ref without following the current default, including after a harness restart while microvmd remains live. Before the first claim, deletion atomically disables the schedule and cleans only its owned placement, retaining dirty state; cleanup or completion failure leaves a restart-safe tombstone retried against the same ref. The first atomic claim hands placement lifetime to the persisted fire-session lineage, so later deletion removes only the schedule record and retains the worktree for historical or resumable fires. Active fires block deletion, and borrowed, no-FS, host-local, and legacy-ambiguous placements are never destructively cleaned up; no path deletes the repository VM, rootfs, origin session, or sibling worktrees.
  - verify: `TestADR_0291_ScheduleResolvesSelectorBeforePersistingExactEnvironmentRef`, `TestInvariant_scheduled_placement_is_reauthorized_at_fire`, `TestScheduleIndependentPlacementAllocatedOnceAndCleaned`, `TestScheduleClaimHandsPlacementToFireSession`, `TestScheduleCleanupFailureRetainsDisabledExactPlacement`, `TestScheduleLegacyAndNoFSPlacementsAreNeverDeleted`, `TestScheduleDeleteDisablesBeforeActiveFireCleanup`, `TestScheduleDeleteUsesAtomicBeginRecordForClaimHandoff`, `TestScheduleDeleteCompletionFailureLeavesRetryableTombstone`, `TestScheduleCreatePersistenceFailureRollsBackOwnedPlacement`
