---
id: 02-indexed-inventory
title: Indexed metadata, bounded pagination, and lock decomposition
blocked_by: [01-v2-snapshots]
status: pending
branch: ""
worktree: ""
issue: "587"
retries: 0
last_error: ""
accumulator: acc/session-storage-continuity
---

# Task brief

Implement the rebuildable derivative metadata catalog, generation-bound keyset cursors, work-bounded pages, cross-process external-change detection, and per-family/narrow locking across in-tree stores and the remote driver.

Follow ADR-0225, ADR-0217, ADR-0027, ADR-0104, AGENTS.md layering/security invariants, and existing repository conventions. Keep tests offline. Do not absorb later tasks or weaken fail-closed behavior. Update the living docs and user-docs affected by this task.

## Acceptance criteria

- AC2.1: Page latency, bytes read, and allocations are independent of transcript/history size and proportional to page size after catalog readiness; fetching page two does not rediscover every snapshot.
  - verify: `TestSessionStorageContinuity_Scenario2_PageWorkBounded`
- AC2.2: Ordering is `(modified_at DESC, session_id ASC)`; a cursor is filter- and generation-bound, and a stale cursor returns an explicit restart signal rather than mixing generations.
  - verify: `TestSessionStorageContinuity_Scenario2_GenerationBoundCursor`
- AC2.3: A missing/corrupt/stale catalog rebuilds from v2 headers or bounded v1 tails without becoming transcript authority, and shared-directory changes are detected rather than hidden by a process-local cache.
  - verify: `TestSessionStorageContinuity_Scenario2_CatalogRebuildAndExternalChange`
- AC2.4: A blocked inventory or catalog rebuild does not delay Save, Load, EventLog.Append, or ToolCall for a different session; Save, Delete, migration promotion/removal, EventLog.Append, and ToolCall for the same family coordinate under one cross-process mutation identity so snapshot-last deletion or migration cannot race a sidecar append.
  - verify: `TestSessionStorageContinuity_Scenario2_UnrelatedMutationNotBlocked`, `TestSessionStorageContinuity_Scenario2_SameFamilyMutationSerialized`
- AC2.5: In the 807-row/12-GiB-equivalent fixture, a 100-row page visits at most 101 ordered catalog rows, performs zero snapshot/transcript reads, and page two repeats neither catalog rebuild nor prior-page traversal; the benchmark records latency and allocations as a trend signal.
  - verify: `BenchmarkSessionStorageContinuity_LargeInventory`, `TestSessionStorageContinuity_Scenario2_LargeInventoryWorkCounters`
