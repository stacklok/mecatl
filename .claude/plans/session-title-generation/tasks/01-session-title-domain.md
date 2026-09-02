---
id: 01-session-title-domain
title: Durable session-title lifecycle and accounting
blocked_by: []
status: in-progress
branch: ""
worktree: ""
issue: "621"
retries: 0
last_error: ""
accumulator: acc/session-title-generation
---

Implement ADR 0284's domain foundation in `engine/session`, prompt ingress in `engine/agent`, and snapshot/event-source support. Add generated provenance; title-generation lifecycle, sources, attempts, outcomes, and bounded 16-entry `session_title` auxiliary usage. Capture only first three genuine non-empty principal text prompts at original prompt ingress (2,000 runes each/6,000 total), never continuations/history scans. Normalize all title whitespace to one line; generated titles clamp to 80 total runes while existing title lengths stay unchanged. Aggregate methods own mutation. Persist/reconstruct metadata without changing main usage, conversation, or budgets. Update API compatibility artifacts if exported surface changes.

## Acceptance criteria
- AC2.2, AC2.3, AC5.1, AC5.2, AC5.3, AC5.4, AC5.5.
- verify: `TestSessionTitleGeneration_Scenario2_GenuinePromptCandidatesRoundTrip`
- verify: `TestSessionTitleGeneration_Scenario2_TitleMetadataRoundTrip`
- verify: `TestSessionTitleGeneration_Scenario5_RecordsAuxiliaryUsage`
- verify: `TestSessionTitleGeneration_Scenario5_AuxiliaryUsageIsBoundedAndClosed`
- verify: `TestSessionTitleGeneration_Scenario5_AuxiliaryUsageRoundTripAndProjection`
- verify: `TestADR_0284_AuxiliaryUsageDoesNotSpendRunBudget`
- verify: `TestSessionTitleGeneration_Scenario5_OnlyTitleIsPlumbed`
