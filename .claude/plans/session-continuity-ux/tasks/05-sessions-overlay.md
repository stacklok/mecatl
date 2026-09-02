---
id: 05-sessions-overlay
title: Honest searchable non-destructive sessions overlay
blocked_by: [02-run-purpose-gates, 03-authoritative-transcript, 04-paged-inventory]
status: done
branch: "plan-session-continuity-ux/05-sessions-overlay"
worktree: ".scratch/worktrees/session-continuity-ux-task-05"
issue: "471"
retries: 0
last_error: ""
accumulator: acc/session-continuity-ux
---

# Task brief

Rework `/sessions` onto the server-authored taxonomy, capability reason codes, pager, and authoritative transcript client. Provide Chats/Scheduled runs/Child runs, current-row marking, ordinary handles, complete search, and modal non-destructive inspection. Remove client prefix classification. Update help/docs/user-docs in this task, but leave generated llms reconciliation to the orchestrator.

## Acceptance criteria

- AC4.1: `/sessions` groups rows by server-authored kind into Chats, Scheduled runs, and Child runs; team-member rows are not presented as resumable teams.
  - verify: `TestSessionContinuityUX_Scenario4_FamilyTabs`
- AC4.2: Each titled row shows ADR-0285's fixed ordinary session handle; the current chat stays visible with a `current` marker and cannot be redundantly opened.
  - verify: `TestPredictableSessionHandles_Scenario1_SharedNormalHandle`
- AC4.3: Ordinary handles are terminal-safe fixed projections and are never sent to server APIs as session IDs; debug uses one TARGET grammar where exact full-ID equality wins and ambiguous projections require the copied full ID.
  - verify: `TestPredictableSessionHandles_Scenario3_PresentationParitySafetyAndLayering`
- AC4.4: Filtering matches title, full ID, visible handle, model, workspace, and child relationship names case-insensitively across fetched pages.
  - verify: `TestSessionContinuityUX_Scenario4_SearchFields`
- AC4.5: Inspecting a scheduled or child run leaves the active prompt target, live subscription, capabilities, title, model, and conversation unchanged; Escape restores the prior view.
  - verify: `TestSessionContinuityUX_Scenario4_InspectionPreservesActiveChat`
- AC4.6: Snapshot transcript load failure blocks continuation and offers Retry and Back; no default path enables input with hidden context.
  - verify: `TestInvariant_transcript_failure_never_enables_hidden_context`
- AC4.7: Closed capability reason codes, not parsed prose, determine enabled actions; caller-visible text reveals no owner/lease identity or backend path.
  - verify: `TestSessionContinuityUX_Scenario4_ReasonCodes`
- AC4.8: Palette text, `?` help, overlay hints, `docs/tui.md`, `docs/usage.md`, and `user-docs/` consistently say Continue for chats and Inspect for scheduled/child runs.
  - verify: inspection — `task docs` and `task site:build` pass with the reviewed wording
