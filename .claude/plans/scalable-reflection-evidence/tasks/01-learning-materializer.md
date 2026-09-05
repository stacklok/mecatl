---
id: 01-learning-materializer
title: Versioned bounded evidence materializer
blocked_by: []
status: done
attempt: 1
branch: plan-scalable-reflection-evidence/01-learning-materializer-attempt-1
worktree: .scratch/worker-scalable-reflection-evidence-01-learning-materializer-attempt-1
issue: ""
retries: 0
last_error: ""
accumulator: acc/scalable-reflection-evidence
---

# Task brief

Create the storage-neutral `reflection-evidence/v1` materialization protocol in `engine/learning`. Start with failing engine tests, then replace input-local evidence assumptions with deterministic selected-local handles backed by an immutable aggregate manifest. The selector must classify safe fields before copying them, rank complete connected tool-turn components deterministically, preserve source order, and keep durable original coordinates distinct from selected-local handles. Retain a versioned legacy reader for ADR-0109 records. Do not wire automatic/explicit host paths, coordinator admission, proposal storage, transports, API snapshots, or documentation here.

**Likely scope:** `engine/learning/evidence.go`, `reflection.go`, proposal/evidence value objects, and focused engine tests.

**Invariants:** exported protocol values remain storage-neutral; no unbounded canonical projection or source copy; existing per-field canonical projection is reused rather than adding truncation; forbidden source fields never influence diagnostics or outward reasons; selected history always passes bidirectional tool pairing.

## Acceptance criteria

- AC1.3: Materialization working memory is bounded by selection limits rather than source size; excluded large values are classified before copying and no full canonical projection exists.
  - verify: `TestADR_0300_MaterializerWorkingStateIsBounded`
- AC2.1: Repeated materialization of the same source boundary and event sequence, including after store reload, produces the same protocol, explicit identity-boundary domain/value, complete source-ordered manifest entries, canonical bytes, and SHA-256 aggregate digest.
  - verify: `TestScalableReflectionEvidence_Scenario2_ManifestDeterministicAcrossReloadAndRetry`
- AC2.2: Priority is exactly mandatory verified current span/closure; explicit remember/learn intent; correction/failure-recovery/repeated-tool-sequence context; most-recent eligible user/assistant context; then events. Within a tier original coordinates are the stable tie-breaker, and final output is source ordered.
  - verify: `TestADR_0300_ClosedRankingTiersTieBreakAndSourceOrder`
- AC3.1: Selection includes or omits a connected component whole and selected history passes bidirectional tool pairing; source order is preserved for every included message.
  - verify: `TestScalableReflectionEvidence_Scenario3_ConnectedToolTurnComponentsAreAtomic`
- AC3.3: An individually oversized component is omitted whole after existing canonical per-field projection rules; no extra text truncation is introduced to force it under bounds. If required current closure cannot fit, automatic skips and explicit returns closed successful abstention `mandatory_span_exceeds_bounds`.
  - verify: `TestADR_0300_OversizedComponentOmittedAndMandatoryClosureAbstains`
- AC3.4: Dropping a unit cannot renumber durable original coordinates. Model handles are compact selected-local `m:n`/`e:n`; persisted references keep distinct original message coordinates or event sequences plus digest.
  - verify: `TestADR_0300_SelectedLocalHandlesDoNotReplaceOriginalCoordinates`
- AC3.5: Pre-version ADR-0109 records decode only as `reflection-evidence/legacy-v0`, where `EvidenceRef.Ordinal` retains its historical input-local meaning. New records write explicit `reflection-evidence/v1`; readers dispatch by resolved protocol and never infer/rewrite ordinal meaning from new field presence.
  - verify: `TestADR_0300_LegacyEvidenceOrdinalCompatibilityIsVersioned`
- AC6.1: Projection retains only canonical bounded user/assistant text, tool call ID/name, safe textual tool-result fields/error bit, admitted public textual media metadata, and eligible content-free event metadata. Provider reasoning/item IDs, binary media/data, actors, permission or raw tool arguments, credentials/secret-shaped fields, and all delegation payloads/previews are omitted before copying/accounting.
  - verify: `TestScalableReflectionEvidence_Scenario6_SafeFieldProjectionMatrix`
- AC6.2: Omitted material uses only a fixed marker already defined by canonical projection or disappears; it is never hashed into a diagnostic label or content-derived reason. Retained hostile/control text is normalized and untrusted-fenced before digest/provider use.
  - verify: `TestADR_0300_OmissionAndHostileTextProjectionAreCanonical`
- AC6.3: An empty-after-projection unit is ineligible. An unsafe-only source follows Scenario 5 before queue/provider work, and every outward surface excludes source bytes, paths, arguments, credentials, media, and child output.
  - verify: `TestScalableReflectionEvidence_Scenario6_UnsafeOnlyInputDoesNoQueueOrProviderWork`
