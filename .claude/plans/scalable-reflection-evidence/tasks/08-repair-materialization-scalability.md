---
id: 08-repair-materialization-scalability
title: Repair cancellable linear materialization and bounded manifest verification
blocked_by: []
status: in-progress
attempt: 1
branch: plan-scalable-reflection-evidence/08-repair-materialization-scalability-attempt-1
worktree: .scratch/worker-scalable-reflection-evidence-08-repair-materialization-scalability-attempt-1
issue: ""
retries: 0
last_error: ""
accumulator: acc/scalable-reflection-evidence
---

# Repair brief

Repair the two panel-review ship blockers without widening scope:

1. `engine/learning/materializer.go` must materialize with cancellation support and linear-time source traversal. It must not repeatedly scan later messages for every tool call, and it must observe the Build/request cancellation while scanning a large source.
2. `internal/adapter/server/learning.go` proposal detail/approval manifest verification must reconstruct only manifest-referenced source messages/events. It must not construct `learning.NewInput` from the complete retained source trajectory before extracting selected evidence.

Preserve the ADR 0300 protocol, deterministic selection, source-coordinate/digest verification, bidirectional pairing, no raw-content disclosure, and all existing public behavior except the corrected bounded/cancellation behavior. Add focused offline regression tests demonstrating cancellation, non-quadratic/component-indexed traversal, and bounded selected-only verification over a large unrelated source. Run Taskfile lint/test. Do not change plan status, push, or edit other orchestration state.

## Protected acceptance criteria

- AC1.3: Materialization working memory is bounded by selection limits rather than source size; excluded large values are classified before copying and no full canonical projection exists.
  - verify: `TestADR_0300_MaterializerWorkingStateIsBounded`
- AC4.2: Proposal list remains metadata-only and performs no source read/materialization. Proposal detail owner-authorizes and re-materializes the exact manifest once, without ranking or substituting nearby content.
  - verify: `TestScalableReflectionEvidence_Scenario4_ListIsMetadataOnlyDetailRematerializesOnce`
- AC4.3: Approval independently re-materializes the exact manifest once and validates identity, every source digest/coordinate/event sequence/component binding, aggregate digest, and candidate citation before entering existing CAS/promotion flow.
  - verify: `TestScalableReflectionEvidence_Scenario4_ApprovalRevalidatesManifestOnce`
- AC4.4: A compacted, deleted, unavailable, reordered, or changed source, unsupported protocol, identity mismatch, binding mismatch, or aggregate mismatch returns failed precondition and performs no memory or learned-skill promotion.
  - verify: `TestScalableReflectionEvidence_Scenario4_MismatchFailsPreconditionWithoutPromotion`
- AC7.1: Explicit cancellation during a blocked/large streaming scan returns the existing typed cancellation promptly and leaves no queue, receipt, singleflight, reservation, provider, repository-open, proposal, or promotion state.
  - verify: `TestScalableReflectionEvidence_Scenario7_ExplicitCancellationStopsMaterialization`
- AC7.3: `Built.Close` closes the materialization gate, rejects new scans, cancels and joins all active scans, then performs existing coordinator queued/running shutdown; it cannot return while materialization-owned work remains.
  - verify: `TestScalableReflectionEvidence_Scenario7_BuiltCloseCancelsAndJoinsMaterialization`
- AC7.4: Race/leak proofs show no per-job materialization goroutine, double receipt/proposal, post-close admission, leaked active operation, or provider call after cancellation/close.
  - verify: `TestADR_0300_MaterializationCancelCloseRaceAndNoPerJobGoroutine`
