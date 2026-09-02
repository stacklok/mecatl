---
id: 06-active-session-details
title: Active session details and exact-ID copy
blocked_by: [03-authoritative-transcript, 04-paged-inventory]
status: done
branch: "plan-session-continuity-ux/06-active-session-details"
worktree: ".scratch/worktrees/session-continuity-ux-task-06"
issue: "525"
retries: 0
last_error: ""
accumulator: acc/session-continuity-ux
---

# Task brief

Add a read-only `/session` details surface using the active session metadata and existing clipboard abstraction. Render full IDs reversibly/safely, copy exact valid UTF-8 IDs, use ADR-0284's fixed ordinary handle in the width-safe header, and keep every rebind path authoritative. Update help/docs/user-docs; generated docs remain orchestrator-owned.

## Acceptance criteria

- AC5.1: `/session` displays a reversible safe representation of the full ID plus title, state, workspace, created/modified timestamps when known, and provider/model for the active chat.
  - verify: `TestSessionContinuityUX_Scenario5_DetailsSurface`
- AC5.2: One explicit action copies the byte-exact opaque ID through the clipboard abstraction and reports success/failure without claiming an empty or stale copy.
  - verify: `TestSessionContinuityUX_Scenario5_CopyExactID`
- AC5.3: Stored-session continuation, model carryover, effort fork, and worktree switch each update the details/copy target to the final adopted ID.
  - verify: `TestSessionContinuityUX_Scenario5_RebindMatrix`
- AC5.4: The compact header uses ADR-0284's fixed ordinary handle, remains width-safe, and `/session` is discoverable from slash completion and `?` help.
  - verify: `TestSessionContinuityUX_Scenario5_HeaderAndHelp`
- AC5.5: Newline/control-bearing, empty, and very long valid-UTF-8 IDs render safely while clipboard copy remains exact; a persisted invalid-UTF-8 ID is rejected as corrupt before protobuf mapping rather than repaired into a different handle.
  - verify: `TestInvariant_session_details_render_safe_copy_exact`
