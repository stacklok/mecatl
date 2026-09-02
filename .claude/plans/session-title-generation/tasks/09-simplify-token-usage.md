---
id: 09-simplify-token-usage
title: Simplify canonical token usage and HTTP contract
blocked_by: [08-consolidate-title-accounting]
status: in-progress
branch: ""
worktree: ""
issue: "621"
retries: 0
last_error: ""
accumulator: acc/session-title-generation
---

Apply review feedback. Keep `token_usage` comments generic: do not enumerate current keys on each field; place total=sum(models) only on TokenUsage. Retain mecatui's proto-free message boundary but remove obsolete AuxiliaryUsageSummary copies. Remove title-specific `session.AuxiliaryUsage`, AuxiliaryOperation, and its 16-entry persisted/wire/event-source detail ledger. Title lifecycle retains only attempt identity/outcome/time. Canonical durable token usage is the sole long-lived usage accounting: map usage kind to TokenUsage(total + opaque model-key map), including title usage aggregated by its selected model. Update snapshots/event source/proto/server HTTP/gRPC/client mappings and docs. Correct HTTP documentation: run SSE has no out-of-band title live push; title changes are discoverable via authoritative snapshot and durable event stream, while gRPC StreamSessionLive remains live push. Preserve compatibility only where needed, with tests and generated artifacts.

## Acceptance criteria
- Review consolidation: remove per-attempt title usage detail and 16-entry limit.
- Keep durable aggregate accounting by usage kind and model attribution.
- Accurate transport behavior documentation.
- verify: `TestSessionTitleGeneration_Scenario2_TitleMetadataRoundTrip`
- verify: `TestSessionTitleGeneration_Scenario4_TitleEventIsAuthoritativeAndSanitized`
- verify: `TestSessionTitleGeneration_Scenario5_RecordsAuxiliaryUsage`
- verify: `TestSessionTitleGeneration_Scenario5_AuxiliaryUsageRoundTripAndProjection`
- verify: `TestADR_0284_AuxiliaryUsageDoesNotSpendRunBudget`
