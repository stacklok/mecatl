---
id: 05-proposal-manifest-verification
title: Durable manifest detail and approval verification
blocked_by: [03-explicit-materialization-lifecycle, 04-selected-evidence-coordinator]
status: done
attempt: 1
branch: plan-scalable-reflection-evidence/05-proposal-manifest-verification-attempt-1
worktree: .scratch/worker-scalable-reflection-evidence-05-proposal-manifest-verification-attempt-1
issue: ""
retries: 0
last_error: ""
accumulator: acc/scalable-reflection-evidence
---

# Task brief

Persist the complete immutable selected-evidence manifest with each staged proposal and make proposal detail and approval re-materialize that manifest exactly once. Detail and approval must validate protocol, source identity boundary, original coordinates/event sequences, entry digests, tool-component bindings, aggregate digest, and candidate citations without reranking or substituting nearby source. Keep proposal list metadata-only and preserve the existing bounded redacted preview behavior. Do not alter materializer ranking, coordinator identity, client transport/UI mapping, API snapshots, or documentation.

**Likely scope:** learning proposal records/repository adapters and `internal/adapter/server/learning.go`, with storage/service tests using offline stores.

**Invariants:** source mismatch is failed precondition and causes no promotion; owner authorization precedes source reads; legacy protocol dispatch remains explicit; approval continues through existing CAS/conflict/promotion/undo controls only after exact evidence verification; no manifest or raw source reaches list/detail output.

## Acceptance criteria

- AC4.1: Every proposal persists the complete source-ordered aggregate manifest: protocol, exact source boundary `{domain: "mecatl/reflection-evidence/source/v1", session_id}`, every selected original message coordinate or event sequence, canonical entry digest, complete tool-component binding, and aggregate selected-evidence digest. Candidate citations resolve only into its entries.
  - verify: `TestScalableReflectionEvidence_Scenario4_ProposalPersistsCompleteAggregateManifest`
- AC4.2: Proposal list remains metadata-only and performs no source read/materialization. Proposal detail owner-authorizes and re-materializes the exact manifest once, without ranking or substituting nearby content.
  - verify: `TestScalableReflectionEvidence_Scenario4_ListIsMetadataOnlyDetailRematerializesOnce`
- AC4.3: Approval independently re-materializes the exact manifest once and validates identity, every source digest/coordinate/event sequence/component binding, aggregate digest, and candidate citation before entering existing CAS/promotion flow.
  - verify: `TestScalableReflectionEvidence_Scenario4_ApprovalRevalidatesManifestOnce`
- AC4.4: A compacted, deleted, unavailable, reordered, or changed source, unsupported protocol, identity mismatch, binding mismatch, or aggregate mismatch returns failed precondition and performs no memory or learned-skill promotion.
  - verify: `TestScalableReflectionEvidence_Scenario4_MismatchFailsPreconditionWithoutPromotion`
- AC4.5: Existing evidence preview is not repurposed: detail keeps the current at-most-1024-byte, UTF-8-safe, canonical redacted/digest-verified projection and never exposes raw source or a manifest dump.
  - verify: `TestADR_0300_EvidencePreviewCompatibilityRemainsRedactedAndBounded`
- AC8.5: Review/auto staging, trust and ownership checks, proposal CAS, conflict handling, promotion eligibility, and undo semantics are unchanged after evidence verification.
  - verify: `TestADR_0300_StagingPromotionAndUndoControlsUnchanged`
