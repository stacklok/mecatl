---
id: 03-approval-render-input
title: Approval render modes and pointer input
blocked_by: [02-approval-surface-core]
status: done
branch: "plan-surface-approval-migration/03-approval-render-input"
worktree: ""
issue: "555"
retries: 0
last_error: ""
accumulator: acc/surface-approval-migration
---

# Task brief

Complete approval's surface-owned rendering and input paths: generic card,
plan fill view, args fill view, keyboard, wheel, hit-ID allocation/cache, and
pointer-message handling. Preserve existing layout/viewport behavior and
goldens. Parent placement and hit testing must use the shared infrastructure;
the approval surface maps only its current-frame IDs to local behavior.

## Acceptance criteria

- AC2.2: A plan ask remains a scrollable fill view with pinned verdict controls; its plan viewport rewraps when its ask, queued-count/model presentation, or geometry changes and preserves the reading offset on a no-op relayout.
  - verify: `TestSurfaceApprovalMigration_Scenario2_PlanReviewLayoutAndOffset`
- AC2.3: Non-plan/non-diff asks retain their centered mini-args view and full args review, including raw/pretty switching, preserved offset, and keyboard verdicts from the expanded view; diff asks retain their existing expansion path.
  - verify: `TestSurfaceApprovalMigration_Scenario2_ArgsAndDiffModes`
- AC2.4: A `plan_approved` continuation begins only after the terminal result, never immediately after a verdict send, preserving ADR-0069’s same-stream ordering rule.
  - verify: `TestSurfaceApprovalMigration_Scenario2_PlanContinuationOrdering`
- AC3.4: Approval button hits in generic, plan, and full-args layouts resolve the same verdicts as their keyboard equivalents; clicks elsewhere in the approval view remain swallowed and no-mouse/inline modes remain inert.
  - verify: `TestSurfaceApprovalMigration_Scenario3_ApprovalMouseParity`
- AC3.5: Plan and full-args wheel capture plus generic-card-only mini-scroll/outside-card transcript delegation remain unchanged.
  - verify: `TestSurfaceApprovalMigration_Scenario3_WheelCaptureAndDelegation`
- AC4.2: Approval rendering and golden frames are unchanged for all existing approval fixtures.
  - verify: demonstration — `task test:golden` proves the existing approval frames are byte-identical
