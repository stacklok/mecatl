---
id: 02-indexed-inventory
title: Rebuildable session metadata catalog
blocked_by: [01-v2-snapshots, 01b-v2-durability, 01c-v2-temp-locking]
status: done
branch: "plan-session-storage-continuity/02-indexed-inventory"
worktree: ""
issue: "587"
retries: 0
last_error: ""
accumulator: acc/session-storage-continuity
---

# Task brief

Implement the derivative, rebuildable jsonlstore metadata catalog foundation. It stores only inventory-safe metadata, rebuilds from v2 headers or bounded v1 tails, detects missing/corrupt/stale state and external/shared-directory changes, and never becomes transcript authority. Keep catalog persistence/format adapter-private and update ADR-0027 resource inventory. Leave cursor wire semantics, page-work bounds, and lock decomposition to 02b/02c.

Follow ADR-0226, ADR-0217, ADR-0027, ADR-0104 and AGENTS.md. Use stdlib/existing dependencies, offline tests, and no TUI/maintenance/adoption work.

## Acceptance criteria

- AC2.3: A missing/corrupt/stale catalog rebuilds from v2 headers or bounded v1 tails without becoming transcript authority, and shared-directory changes are detected rather than hidden by a process-local cache.
  - verify: `TestSessionStorageContinuity_Scenario2_CatalogRebuildAndExternalChange`
