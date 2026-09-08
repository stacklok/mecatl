---
id: 04-title-generator
title: Private bounded title generator
blocked_by: [01-session-title-domain, 03-title-slot-resolution]
status: pending
branch: ""
worktree: ""
issue: "621"
retries: 0
last_error: ""
accumulator: acc/session-title-generation
---

Implement an internal server-private `SessionTitleGenerator`: receives only selected provider-scoped LLMProvider, resolved model, and capped sources. Make one zero-tool direct stream with canonical untrusted fences, 30-second timeout, 128 output tokens, strict title/defer parser, usage capture, and safe outcome classification. It has no registry, credential, store, or mutation authority. Use offline mock-provider tests.

## Acceptance criteria
- AC3.3, AC3.4, AC3.5.
- verify: `TestADR_0284_TitleGenerationServerOwnsInputAndModel`
- verify: `TestADR_0284_TitleGenerationInputOutputBoundary`
- verify: `TestSessionTitleGeneration_Scenario3_DeferAndTerminalOutcomes`
