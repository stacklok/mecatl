---
id: 01-explicit-procedure-intent
title: Recognize verified affirmative procedure-learning imperatives
blocked_by: []
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Extend the pure `engine/learning` signal detector and admission policy for the narrowly enumerated create/make/build/turn-into-skill imperatives. Keep detection bound to the genuine current principal prompt and ADR-0114 hard-stop set; reject negation, questions, non-main provenance, synthetic/history/compacted text, and all other terminal states. Add the named policy tests without changing queue or persistence behavior.

## Acceptance criteria

- AC1.1: Each listed genuine, current principal-authored imperative produces hard procedure admission without a weighted signal only when the main-session terminal is one of ADR-0114's exact hard-stop set: `end_turn`, max-turn, max-tool-call, or run-budget. Failed, cancelled, awaiting, no-progress, timeout, and structured-output terminals do not admit.
  - verify: `TestADR_0254_ExplicitIntentUsesOnlyADRElevenFourHardStops`
- AC1.2: Negated imperatives and meta/capability questions do not admit learning.
  - verify: `TestCloudNativeLearning_Scenario1_NegatedAndMetaIntentRejected`
- AC1.3: Assistant, tool, web, repository, historical, synthetic, non-main, compacted, and unverifiable text cannot manufacture explicit learning authority.
  - verify: `TestADR_0254_ExplicitIntentRequiresVerifiedCurrentPrincipalPrompt`
