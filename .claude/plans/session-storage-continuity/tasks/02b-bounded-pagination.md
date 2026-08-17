---
id: 02b-bounded-pagination
title: Generation-bound work-bounded pagination
blocked_by: [02-indexed-inventory]
status: done
branch: "plan-session-storage-continuity/02b-bounded-pagination"
worktree: ""
issue: "587"
retries: 0
last_error: ""
accumulator: acc/session-storage-continuity
---

# Task brief

Build true keyset pagination over the derivative catalog across jsonlstore and required port/driver/conformance surfaces. Pages use deterministic ordering, generation/filter-bound cursors, explicit stale restart, and page-size-proportional work without snapshot/transcript reads or prior-page traversal. Preserve caller ownership filtering before page formation and keep backend format details private.

## Acceptance criteria

- AC2.1: Page latency, bytes read, and allocations are independent of transcript/history size and proportional to page size after catalog readiness; fetching page two does not rediscover every snapshot.
  - verify: `TestSessionStorageContinuity_Scenario2_PageWorkBounded`
- AC2.2: Ordering is `(modified_at DESC, session_id ASC)`; a cursor is filter- and generation-bound, and a stale cursor returns an explicit restart signal rather than mixing generations.
  - verify: `TestSessionStorageContinuity_Scenario2_GenerationBoundCursor`
- AC2.5: In the 807-row/12-GiB-equivalent fixture, a 100-row page visits at most 101 ordered catalog rows, performs zero snapshot/transcript reads, and page two repeats neither catalog rebuild nor prior-page traversal; the benchmark records latency and allocations as a trend signal.
  - verify: `BenchmarkSessionStorageContinuity_LargeInventory`, `TestSessionStorageContinuity_Scenario2_LargeInventoryWorkCounters`
