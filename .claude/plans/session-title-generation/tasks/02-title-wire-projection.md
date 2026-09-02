---
id: 02-title-wire-projection
title: Additive session and title-event wire projection
blocked_by: [01-session-title-domain]
status: pending
branch: ""
worktree: ""
issue: "621"
retries: 0
last_error: ""
accumulator: acc/session-title-generation
---

Extend the existing Session/Summary/Event protocol and server/client mappers additively for durable title lifecycle, provenance, bounded latest-attempt/usage summaries, and structured `session.title`. Generate contracts. Apply UTF-8 handling at all mappings. Never expose title sources/provider errors and never add a title-generation RPC, generic dispatcher, or LLMRequest fields.

## Acceptance criteria
- AC1.4, AC2.3, AC4.5, AC5.3.
- verify: `TestSessionTitleGeneration_Scenario1_TitleCommandIsClientOnly`
- verify: `TestSessionTitleGeneration_Scenario2_TitleMetadataRoundTrip`
- verify: `TestSessionTitleGeneration_Scenario4_TitleEventIsAuthoritativeAndSanitized`
- verify: `TestSessionTitleGeneration_Scenario5_AuxiliaryUsageRoundTripAndProjection`
