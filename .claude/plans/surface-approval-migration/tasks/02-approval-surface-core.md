---
id: 02-approval-surface-core
title: Dynamic approval surface lifecycle and intents
blocked_by: [01-hit-dispatch]
status: done
branch: "plan-surface-approval-migration/02-approval-surface-core"
worktree: ""
issue: "555"
retries: 0
last_error: ""
accumulator: acc/surface-approval-migration
---

# Task brief

Move approval's ephemeral state and queue/message/key behavior into a
dynamically-created surface. Model retains stream sends, notices, phase,
spinner, focus, and terminal plan continuation through typed intents. Remove
the permanent approval state from Model, preserve queue/retract behavior, and
clear all surface and frame caches on ordinary close plus run/session teardown.
Do not change product approval semantics.

## Acceptance criteria

- AC1.1: The first permission ask installs one dynamic approval surface; a duplicate is ignored and later surfaced asks queue FIFO behind its visible head.
  - verify: `TestSurfaceApprovalMigration_Scenario1_DynamicSurfaceQueue`
- AC1.2: Resolving or retracting a visible ask keeps the same surface and `phaseAwaitingApproval` while a successor exists; resolving or retracting the final ask clears both render-frame caches, removes the surface, restores the interrupted idle/running phase, refocuses the textarea, and re-arms the spinner only for a running resume.
  - verify: `TestSurfaceApprovalMigration_Scenario1_QueueDrainRetractAndCloseLifecycle`
- AC1.3: Session reset and terminal-run cleanup close the dynamic approval surface and discard its queued asks and render caches, so no approval view state crosses a session/run boundary.
  - verify: `TestSurfaceApprovalMigration_Scenario1_RunAndSessionTeardown`
- AC1.4: `Model` has no permanent approval-state tombstone; the approval surface is dynamically constructed only at ask open, consistent with the surface migration contract.
  - verify: `TestApprovalSurfaceStateIsNotModelOwned`
- AC2.1: Keyboard verdicts, focus movement, child-ask withholding of Allow Always, `/debug-ask` with a nil stream, exact ask-ID verdict sending, and the no-extra-reader rule retain current behavior.
  - verify: `TestSurfaceApprovalMigration_Scenario2_VerdictTransportAndChildPolicy`
