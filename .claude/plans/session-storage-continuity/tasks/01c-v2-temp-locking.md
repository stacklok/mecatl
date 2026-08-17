---
id: 01c-v2-temp-locking
title: Cross-process family locking and orphan-temp recovery
blocked_by: [01b-v2-durability]
status: done
branch: "plan-session-storage-continuity/01c-v2-temp-locking"
worktree: ""
issue: "586"
retries: 0
last_error: ""
accumulator: acc/session-storage-continuity
---

# Task brief

Add the cross-process same-family mutation identity and generation-aware temporary-file ownership/recovery for the v2 snapshot implementation. Startup and the next successful save may reap only proven inactive orphan temps while holding the family lock; an active peer temp and committed snapshot must survive. Update ADR-0027 resource inventory and living docs. Do not implement catalog, migration job, cleanup, or TUI work.

## Acceptance criteria

- AC1.5: Startup and the next successful save identify and safely remove abandoned same-family temporary replacements only while holding the cross-process family mutation lock and proving the temp naming/ownership generation is inactive; repeated crashes cannot grow orphan temps without bound, and one process never removes another process's in-progress replacement or a committed snapshot.
  - verify: `TestSessionStorageContinuity_Scenario1_OrphanTemporaryRecovery`, `TestSessionStorageContinuity_Scenario1_ActiveTemporaryNotReaped`
