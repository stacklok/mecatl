---
id: 03-title-slot-resolution
title: Resolve opt-in title slot at session creation
blocked_by: [01-session-title-domain]
status: done
branch: "plan-session-title-generation/03-title-slot-resolution"
worktree: ""
issue: "621"
retries: 0
last_error: ""
accumulator: acc/session-title-generation
---

Add `models.slots.title` to existing validation/resolution in composition. It has no default-tier fallback. Resolve aliases under ordinary operator/project policy on the fixed session provider. Persist pending only when resolvable at creation, otherwise disabled, with no provider call and no main-model change. Inject only a private resolver to server composition.

## Acceptance criteria
- AC2.1, AC3.1, AC3.2.
- verify: `TestSessionTitleGeneration_Scenario2_CreationPersistsEligibility`
- verify: `TestSessionTitleGeneration_Scenario3_AbsentTitleSlotDisablesGeneration`
- verify: `TestSessionTitleGeneration_Scenario3_TitleSlotRoutesOnlyGenerator`
