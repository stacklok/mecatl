---
id: 05-title-coordinator
title: Server-owned title coordinator and durable commit
blocked_by: [01-session-title-domain, 02-title-wire-projection, 03-title-slot-resolution, 04-title-generator]
status: pending
branch: ""
worktree: ""
issue: "621"
retries: 0
last_error: ""
accumulator: acc/session-title-generation
---

Build the Service/Build-owned coordinator: two workers, non-blocking 64-item process-wide queue, session/attempt dedupe, cancel/join shutdown. Submit only after successful eligible normal exchanges. Claim/reload/save under run-entry lock and lease; release before streaming; revalidate then conditionally commit. Use one five-second retry only for timeout/provider failures. Interruptions are terminal/unknown. Rename wins. Save then append/publish sanitized `session.title`; main run is never delayed or changed. Add ADR-0027 resource inventory/restart decisions.

## Acceptance criteria
- AC2.4, AC4.1, AC4.2, AC4.3, AC4.4, AC4.5, AC5.1, AC5.4, AC5.5.
- verify: `TestSessionTitleGeneration_Scenario2_AutomaticWorkIsOutsideChatRun`
- verify: `TestSessionTitleGeneration_Scenario4_TwoPhaseAttemptAdmission`
- verify: `TestSessionTitleGeneration_Scenario4_OperatorTitleWinsRace`
- verify: `TestSessionTitleGeneration_Scenario4_InterruptionAndRetryPolicy`
- verify: `TestSessionTitleGeneration_Scenario4_CoordinatorShutdownAndInventory`
- verify: `TestSessionTitleGeneration_Scenario4_TitleEventIsAuthoritativeAndSanitized`
