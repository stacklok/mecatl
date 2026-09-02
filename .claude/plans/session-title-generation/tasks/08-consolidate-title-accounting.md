---
id: 08-consolidate-title-accounting
title: Consolidate title metadata and durable token usage
blocked_by: [05-title-coordinator]
status: in-progress
branch: ""
worktree: ""
issue: "621"
retries: 0
last_error: ""
accumulator: acc/session-title-generation
---

Apply the consolidated operator feedback before mecatui work. `SessionTitle` is the canonical title lifecycle projection; retain but protobuf-deprecate and dual-write legacy bare title provenance fields. Remove title usage from `SessionTitle`. Make a durable canonical `token_usage` map on both Session and SessionSummary, keyed by closed usage kinds `main` and `session_title`. Each value has total Usage plus a map from an opaque server-produced model-attribution string to Usage; total equals the sum of entries. `main` and `main.*` are reserved. The existing Session.Usage/snapshot usage remain deprecated compatibility projections and are dual-written; the map is canonical. Migrate legacy records honestly with an `unknown` model key rather than fabricated attribution. Persist title-attempt usage with the actual selected title provider/model key. Document every constrained proto string field and preserve UTF-8 boundaries. Update snapshots, event-source metadata, HTTP/gRPC/client mappings, API contracts/changelog, plan/ADR/docs as appropriate. No new title RPC or generic dispatcher. Offline tests must pin migration, total invariants, exact title-model attribution, source-free projections, and legacy behavior.

## Acceptance criteria
- Correct the review finding on title provider/model attribution.
- Add durable complete token usage projections for main and title work.
- Preserve all ACs from tasks 01, 02, and 05 that this changes.
- verify: `TestADR_0284_TitleGenerationServerOwnsInputAndModel`
- verify: `TestSessionTitleGeneration_Scenario2_TitleMetadataRoundTrip`
- verify: `TestSessionTitleGeneration_Scenario4_TitleEventIsAuthoritativeAndSanitized`
- verify: `TestSessionTitleGeneration_Scenario5_RecordsAuxiliaryUsage`
- verify: `TestSessionTitleGeneration_Scenario5_AuxiliaryUsageRoundTripAndProjection`
- verify: `TestADR_0284_AuxiliaryUsageDoesNotSpendRunBudget`
