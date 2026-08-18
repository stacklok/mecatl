---
id: 10-maintenance-tui
title: Storage optimization and cleanup TUI workflows
blocked_by: [03-progressive-sessions-tui, 04-migration-job, 05-cleanup-planner, 07-storage-health]
status: done
branch: "plan-session-storage-continuity/10-maintenance-tui"
worktree: ""
issue: "595"
retries: 0
last_error: ""
accumulator: acc/session-storage-continuity
---

# Task brief

Add distinct Optimize storage and Clean up sessions workflows, durable progress reattachment, truthful cancellation/partial-completion semantics, and high-friction destructive confirmation.

Follow ADR-0226, ADR-0217, ADR-0027, ADR-0104, AGENTS.md layering/security invariants, and existing repository conventions. Keep tests offline. Do not absorb later tasks or weaken fail-closed behavior. Update the living docs and user-docs affected by this task.

## Acceptance criteria

- AC8.3: Optimize storage shows reclaim estimate, invalid/skipped counts, temporary-space requirement, resumable progress, and states that sessions are preserved.
  - verify: `TestSessionStorageContinuity_Scenario8_OptimizeStorageFlow`
- AC8.4: Cleanup begins with a dry-run partitioned by main/child/scheduled/unknown/protected/live/awaiting, protects unknown by default, requires high-friction bulk confirmation, reports apply-time skips/partial completion, and never reuses single-row delete consent.
  - verify: `TestSessionStorageContinuity_Scenario8_CleanupFlow`
- AC8.5: Closing/reopening the panel reattaches to maintenance progress; cancellation is worded as stopping future items, never rollback.
  - verify: `TestSessionStorageContinuity_Scenario8_MaintenanceProgressReattach`
