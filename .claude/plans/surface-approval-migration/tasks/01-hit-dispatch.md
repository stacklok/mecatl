---
id: 01-hit-dispatch
title: Render-frame hit dispatch infrastructure
blocked_by: []
status: done
branch: "plan-surface-approval-migration/01-hit-dispatch"
worktree: ""
issue: "555"
retries: 0
last_error: ""
accumulator: acc/surface-approval-migration
---

# Task brief

Create the shared, immediate-mode hit-dispatch infrastructure for clickable
surfaces without widening `surface`. The Model-owned reference collaborator
must survive value-receiver `View()` copies, allocate opaque hit IDs, retain
only the current screen-adjusted rendered frame, and drop stale IDs. Extend
`surfaceDeps` with the allocator. Replace global `ClickAction` payloads on
`ClickableRegion` with opaque hit IDs and local coordinates. Do not migrate
approval itself in this task.

## Acceptance criteria

- AC3.1: A Model-owned opaque allocator is injected through surface dependencies; `ClickableRegion` carries the resulting hit ID rather than a globally meaningful approval action, and every real `View()` render replaces the parent screen-frame and owning-surface ID-to-local-behavior caches with fresh IDs for the current frame.
  - verify: `TestSurfaceApprovalMigration_Scenario3_RealViewPersistsFreshFrameHitCaches`
- AC3.2: The parent applies placement exactly once, hit-tests the current screen-space frame, translates the pointer to region-local coordinates, and delivers the typed hit message through ordinary surface routing without widening `surface`.
  - verify: `TestSurfaceApprovalMigration_Scenario3_HitCoordinatesAndMessageRouting`
- AC3.3: An unknown, stale, closed-surface, or superseded-frame hit ID causes no verdict, notice, transport send, focus change, or underlying transcript selection.
  - verify: `TestSurfaceApprovalMigration_Scenario3_StaleHitsFailClosed`
- AC4.1: Approval vocabulary, state, rendering, hit-ID cache, and input behavior remain confined to approval and shared hit-dispatch files; Model contains only registration, durable effects, and generic routing.
  - verify: `TestApprovalSurfaceStructuralBoundary`
