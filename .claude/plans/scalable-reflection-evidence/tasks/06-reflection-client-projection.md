---
id: 06-reflection-client-projection
title: Reflection abstention transport and mecatui status
blocked_by: [03-explicit-materialization-lifecycle, 05-proposal-manifest-verification]
status: done
attempt: 1
branch: plan-scalable-reflection-evidence/06-reflection-client-projection-attempt-1
worktree: .scratch/worker-scalable-reflection-evidence-06-reflection-client-projection-attempt-1
issue: ""
retries: 0
last_error: ""
accumulator: acc/scalable-reflection-evidence
---

# Task brief

Project explicit closed materialization abstentions and preserved typed failures through gRPC, HTTP, and the proto-free mecatui client. Keep `/reflect` synchronous, generation-safe, and non-disclosing; show only stable closed outcome text, a success count, or a sanitized typed failure. Mark source/manifest mismatch as non-approvable without exposing manifests or source. Do not change materialization, lifecycle, coordinator, or proposal verification semantics; do not hand-edit generated protobuf output.

**Likely scope:** contracts/proto source if a wire field is necessary, generated contracts through `task generate`, server HTTP/gRPC mapping, `cmd/mecatui` client/UI status code, and offline tests.

**Invariants:** all producer-influenced displayed strings are UTF-8 repaired and control-safe; stable abstention text is harness-authored; cancellation, closure, failed precondition, queue-full, timeout, validation, persistence, and provider failures preserve their existing non-Internal classes; no raw evidence, manifest entry, source text, path, argument, credential, media, or child output is surfaced.

## Acceptance criteria

- AC9.1: gRPC and HTTP project explicit no-safe-evidence as successful abstention using only the closed reason and stable harness text; cancellation, closed, failed precondition, queue-full, timeout, validation, persistence, and provider faults retain existing non-Internal typed classifications.
  - verify: `TestScalableReflectionEvidence_Scenario9_TransportDispositionAndTypedErrorMatrix`
- AC9.2: The proto-free mecatui client preserves only the closed reason/stable text and repairs every producer-influenced string; malformed UTF-8/control content cannot reach output.
  - verify: `TestADR_0298_MecatuiClientMapsClosedSafeAbstentionReason`
- AC9.3: `/reflect` displays in-progress, then muted stable abstention text, a success count, or a sanitized typed failure; stale generations cannot overwrite newer status.
  - verify: `TestScalableReflectionEvidence_Scenario9_MecatuiReflectStatusMatrix`
- AC9.4: Proposal detail marks manifest/source mismatch non-approvable, retains the bounded redacted evidence preview contract, and never displays manifest entries or raw source text.
  - verify: `TestADR_0298_MecatuiMismatchAndPreviewRemainNonDisclosing`
