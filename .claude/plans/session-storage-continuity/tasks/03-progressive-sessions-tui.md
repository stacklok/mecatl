---
id: 03-progressive-sessions-tui
title: Progressive Sessions inventory
blocked_by: [02-indexed-inventory, 02b-bounded-pagination, 02c-inventory-locking]
status: done
branch: "plan-session-storage-continuity/03-progressive-sessions-tui"
worktree: ""
issue: "588"
retries: 0
last_error: ""
accumulator: acc/session-storage-continuity
---

# Task brief

Drive startup and interactive Sessions through one progressive client/UI path: first-page rendering, stable incremental state, cancellation, stale-cursor restart, and partial failure/retry.

Follow ADR-0226, ADR-0217, ADR-0027, ADR-0104, AGENTS.md layering/security invariants, and existing repository conventions. Keep tests offline. Do not absorb later tasks or weaken fail-closed behavior. Update the living docs and user-docs affected by this task.

## Acceptance criteria

- AC3.1: Startup and interactive inventory display the first usable page before requesting the next page.
  - verify: `TestSessionStorageContinuity_Scenario3_FirstPageRendersImmediately`
- AC3.2: Appending pages preserves active tab, search query, selection by exact session ID, scroll position, and deduplicated deterministic rows.
  - verify: `TestSessionStorageContinuity_Scenario3_IncrementalStateStable`
- AC3.3: Initial loading, loading-more, complete, cancelled, stale-cursor restart, and retryable later-page failure are distinct; a later failure retains already-rendered rows and retry never duplicates them.
  - verify: `TestSessionStorageContinuity_Scenario3_PartialFailureAndRetry`
- AC3.4: Cancelling or closing the panel stops further page requests without creating or rebinding a session.
  - verify: `TestSessionStorageContinuity_Scenario3_CancelStopsPagination`
