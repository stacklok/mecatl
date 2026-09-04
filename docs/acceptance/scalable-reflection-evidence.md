# Scalable reflection evidence — acceptance plan

**Phase:** capability — deterministic bounded evidence materialization for reflection
**Status:** draft
**ADR:** [ADR 0298](../adr/0298-bounded-reflection-evidence-materialization.md) — replace raw-size rejection with one versioned bounded-evidence protocol shared by automatic and explicit reflection.
**Accumulator branch:** `acc/scalable-reflection-evidence` (off `main`).

## Outcome

A normally eligible completed session is reflectable even when its retained transcript and
event history exceed 256 KiB. Automatic admission streams the full eligible source/current span
without constructing an unbounded `learning.Input`; after admission, automatic and explicit
reflection use the same deterministic bounded selector. The resulting immutable aggregate
materialization manifest records the protocol, exact identity boundary, complete source-ordered
original coordinates/event sequences, per-entry digests and tool-component bindings, and the
canonical selected-evidence digest. Candidate references cite entries in that manifest. Selection
preserves complete connected tool-turn components and the verified automatic current span,
excludes unsafe material, and is exactly re-materialized once at detail or approval.

This intentionally fixes automatic reflection too. There is **no compatibility promise that
automatic reflection continues rejecting a session merely because its raw retained
trajectory or events exceed the old job-byte bound**. Compatibility applies to the existing
policy, resource, lifecycle, provider/model, staging, promotion, and client contracts around
the new bounded selected-evidence unit.

## Scope

### In scope

- One exported, storage-neutral, versioned aggregate materialization protocol in
  `engine/learning`, with a complete immutable provenance manifest and host integration shared by
  explicit and automatic reflection.
- Full-source streaming automatic admission followed by bounded selected evidence, with no
  unbounded intermediate `learning.Input` or canonical projection.
- Deterministic whole-component relevance ranking and source-order output under the existing
  message, event, evidence, and encoded job/request limits.
- Exact selected-evidence identity, distinct selected-local handles and durable original
  coordinates, legacy ADR-0109 protocol compatibility, and one-time manifest-driven
  re-materialization at detail/approval.
- Closed materialization dispositions/reasons, typed transport errors, explicit abstention
  projection, existing evidence-preview compatibility, and mecatui receipt/error behavior.
- Build-owned cancellable/joined materialization lifecycle with no goroutine per job.
- Engine API snapshots and `engine/CHANGELOG.md` updates required by exported learning
  evidence/provenance changes.
- Living architecture and implementation-note updates for the selected-evidence bound.

### Out of scope

- Multi-pass reflection, chunking, map/reduce, multiple provider calls, or merging candidates
  across windows.
- New learning policy thresholds, cooldowns, budgets, queue limits, timeouts, promotion
  authority, or automatic historical/restart sweeps.
- Persisting transient queue, worker, receipt, reservation, or singleflight state.
- Exposing raw selected text in proposal list/detail, receipts, errors, diagnostics, or TUI
  status.
- Implementing code in this planning-artifact change.

## Scenarios

### Scenario 1 — ordinary large sessions use one bounded path

A tool-heavy completed session contains more than 256 KiB of retained messages and events,
but enough ordinary text and paired tool evidence fits in one reflection request. Both entry
points materialize first and submit the same bounded unit; raw retained size is not an
independent eligibility gate.

**Acceptance:**

- AC1.1: Automatic admission evaluates the full eligible source and verified current span
  incrementally, retaining only bounded counters/coordinates/digests/ranking state and never
  constructing or copying an unbounded `learning.Input`; an otherwise eligible trajectory over
  256 KiB then reaches exactly one reflector call over bounded selected evidence.
  - verify: `TestScalableReflectionEvidence_Scenario1_AutomaticFullSourceAdmissionThenBoundedSelection`
- AC1.2: Explicit reflection of the same completed session succeeds through the same selector
  and gives the reflector byte-identical selected evidence; it is not rejected for raw retained
  size.
  - verify: `TestScalableReflectionEvidence_Scenario1_ExplicitLargeTrajectoryMatchesAutomatic`
- AC1.3: Materialization working memory is bounded by selection limits rather than source size;
  excluded large values are classified before copying and no full canonical projection exists.
  - verify: `TestADR_0298_MaterializerWorkingStateIsBounded`
- AC1.4: Only after automatic admission succeeds is a bounded selected `learning.Input` created;
  existing `MaxInputMessages`, `MaxInputEvents`, candidate/evidence limits, encoded coordinator
  bytes, and provider request/token limits remain hard bounds on it.
  - verify: `TestADR_0298_AdmissionPrecedesBoundedInputConstruction`

---

### Scenario 2 — selection is deterministic and identity-bearing

The protocol version is `reflection-evidence/v1`. It ranks whole eligible units by the closed
priority table, applies original source coordinates as tie-breakers, and emits selected units in
source order. Every result carries one immutable aggregate manifest and one domain-separated
selected-evidence identity; reload and retry reproduce them without transcript excerpts in
coordination state.

**Acceptance:**

- AC2.1: Repeated materialization of the same source boundary and event sequence, including
  after store reload, produces the same protocol, explicit identity-boundary domain/value,
  complete source-ordered manifest entries, canonical bytes, and SHA-256 aggregate digest.
  - verify: `TestScalableReflectionEvidence_Scenario2_ManifestDeterministicAcrossReloadAndRetry`
- AC2.2: Priority is exactly mandatory verified current span/closure; explicit remember/learn
  intent; correction/failure-recovery/repeated-tool-sequence context; most-recent eligible
  user/assistant context; then events. Within a tier original coordinates are the stable
  tie-breaker, and final output is source ordered.
  - verify: `TestADR_0298_ClosedRankingTiersTieBreakAndSourceOrder`
- AC2.3: Automatic and explicit attempts over identical selected source evidence share identity
  `(protocol, identity boundary, selected digest)` and singleflight, even when invocation mode,
  host signals, or existing-memory comparison context differ; changing selected evidence or its
  identity boundary cannot alias.
  - verify: `TestScalableReflectionEvidence_Scenario2_SelectedEvidenceIdentityBoundary`
- AC2.4: Deterministic proposal IDs incorporate selected-evidence identity plus partition and
  candidate identity; retry converges, while host signals, invocation mode, and comparison-only
  existing facts do not perturb selected-evidence identity.
  - verify: `TestScalableReflectionEvidence_Scenario2_SelectedDigestDrivesProposalID`
- AC2.5: One materialized selected input performs at most one provider call; no chunking,
  multi-pass extraction, or candidate merge is introduced.
  - verify: `TestADR_0298_OneSelectedInputOneProviderCall`

---

### Scenario 3 — structural evidence and the automatic current span survive selection

Selection treats each connected tool-turn component atomically: one assistant message, all its
tool calls, and every corresponding result. Automatic reflection's previously verified current
span and all closure it reaches are mandatory before lower-priority evidence.

**Acceptance:**

- AC3.1: Selection includes or omits a connected component whole and selected history passes
  bidirectional tool pairing; source order is preserved for every included message.
  - verify: `TestScalableReflectionEvidence_Scenario3_ConnectedToolTurnComponentsAreAtomic`
- AC3.2: Automatic materialization includes the complete verified current span in canonical
  projected form plus every connected paired-tool closure before spending remaining capacity.
  - verify: `TestScalableReflectionEvidence_Scenario3_AutomaticCurrentSpanClosureIsMandatory`
- AC3.3: An individually oversized component is omitted whole after existing canonical
  per-field projection rules; no extra text truncation is introduced to force it under bounds.
  If required current closure cannot fit, automatic skips and explicit returns closed successful
  abstention `mandatory_span_exceeds_bounds`.
  - verify: `TestADR_0298_OversizedComponentOmittedAndMandatoryClosureAbstains`
- AC3.4: Dropping a unit cannot renumber durable original coordinates. Model handles are compact
  selected-local `m:n`/`e:n`; persisted references keep distinct original message coordinates or
  event sequences plus digest.
  - verify: `TestADR_0298_SelectedLocalHandlesDoNotReplaceOriginalCoordinates`
- AC3.5: Pre-version ADR-0109 records decode only as `reflection-evidence/legacy-v0`, where
  `EvidenceRef.Ordinal` retains its historical input-local meaning. New records write explicit
  `reflection-evidence/v1`; readers dispatch by resolved protocol and never infer/rewrite ordinal
  meaning from new field presence.
  - verify: `TestADR_0298_LegacyEvidenceOrdinalCompatibilityIsVersioned`

---

### Scenario 4 — proposal evidence can be reconstructed exactly

A staged proposal persists one immutable aggregate manifest rather than raw excerpts. Candidate
`EvidenceRef`s cite entries in it and never stand in for it. Detail and approval use original
coordinates/event sequences, entry digests, component bindings, protocol, identity boundary,
and aggregate digest to re-materialize exactly what the reflector saw once.

**Acceptance:**

- AC4.1: Every proposal persists the complete source-ordered aggregate manifest: protocol,
  exact source boundary `{domain: "mecatl/reflection-evidence/source/v1", session_id}`,
  every selected original message coordinate or event sequence, canonical entry digest, complete tool-component binding, and aggregate
  selected-evidence digest. Candidate citations resolve only into its entries.
  - verify: `TestScalableReflectionEvidence_Scenario4_ProposalPersistsCompleteAggregateManifest`
- AC4.2: Proposal list remains metadata-only and performs no source read/materialization.
  Proposal detail owner-authorizes and re-materializes the exact manifest once, without ranking
  or substituting nearby content.
  - verify: `TestScalableReflectionEvidence_Scenario4_ListIsMetadataOnlyDetailRematerializesOnce`
- AC4.3: Approval independently re-materializes the exact manifest once and validates identity,
  every source digest/coordinate/event sequence/component binding, aggregate digest, and candidate
  citation before entering existing CAS/promotion flow.
  - verify: `TestScalableReflectionEvidence_Scenario4_ApprovalRevalidatesManifestOnce`
- AC4.4: A compacted, deleted, unavailable, reordered, or changed source, unsupported protocol,
  identity mismatch, binding mismatch, or aggregate mismatch returns failed precondition and
  performs no memory or learned-skill promotion.
  - verify: `TestScalableReflectionEvidence_Scenario4_MismatchFailsPreconditionWithoutPromotion`
- AC4.5: Existing evidence preview is not repurposed: detail keeps the current at-most-1024-byte,
  UTF-8-safe, canonical redacted/digest-verified projection and never exposes raw source or a
  manifest dump.
  - verify: `TestADR_0298_EvidencePreviewCompatibilityRemainsRedactedAndBounded`

---

### Scenario 5 — no-safe-selection is path-specific and work-free

A source may contain no safely eligible unit or an automatic mandatory closure may exceed bounds.
Materialization uses closed dispositions `selected|abstained|skipped` and closed reasons
`selected|no_eligible_evidence|mandatory_span_exceeds_bounds|cancelled|closed`; these outcomes are
decided before coordinator, reservation, persistence, or proposal work.

**Acceptance:**

- AC5.1: Automatic no-evidence or unfit mandatory closure returns `skipped` with the applicable
  closed reason and creates no receipt, queue/singleflight entry, reservation, provider call,
  repository open/stage, proposal, or promotion.
  - verify: `TestScalableReflectionEvidence_Scenario5_AutomaticSkipLeavesNoAdmissionState`
- AC5.2: Explicit no-safe evidence returns successful `abstained/no_eligible_evidence`; an
  applicable unfit mandatory closure returns `abstained/mandatory_span_exceeds_bounds`, with the
  same no-work guarantee.
  - verify: `TestScalableReflectionEvidence_Scenario5_ExplicitClosedAbstentionReasons`
- AC5.3: Clients receive only stable harness-authored text mapped from the closed vocabulary.
  Cancellation and close retain typed cancellation/closed errors rather than successful
  abstention; no arbitrary source/provider/repository error becomes a reason.
  - verify: `TestADR_0298_MaterializationDispositionReasonAndErrorMatrix`
- AC5.4: Increasing only excluded/unselected retained bytes cannot turn a normally eligible
  automatic selection into an oversize failure.
  - verify: `TestADR_0298_NoAutomaticRawSizeRejectionCompatibility`

---

### Scenario 6 — hostile, secret, media, and delegation payloads never enter evidence

The source mixes prompt-injection markers, provider reasoning, binary image/audio data,
permission requests, raw tool arguments, credential-shaped values, and delegation results.
The materializer classifies and excludes prohibited fields before counting/copying selected
content, and every outward failure/abstention surface stays content-free.

**Acceptance:**

- AC6.1: Projection retains only canonical bounded user/assistant text, tool call ID/name,
  safe textual tool-result fields/error bit, admitted public textual media metadata, and eligible
  content-free event metadata. Provider reasoning/item IDs, binary media/data, actors, permission
  or raw tool arguments, credentials/secret-shaped fields, and all delegation payloads/previews
  are omitted before copying/accounting.
  - verify: `TestScalableReflectionEvidence_Scenario6_SafeFieldProjectionMatrix`
- AC6.2: Omitted material uses only a fixed marker already defined by canonical projection or
  disappears; it is never hashed into a diagnostic label or content-derived reason. Retained
  hostile/control text is normalized and untrusted-fenced before digest/provider use.
  - verify: `TestADR_0298_OmissionAndHostileTextProjectionAreCanonical`
- AC6.3: An empty-after-projection unit is ineligible. An unsafe-only source follows Scenario 5
  before queue/provider work, and every outward surface excludes source bytes, paths, arguments,
  credentials, media, and child output.
  - verify: `TestScalableReflectionEvidence_Scenario6_UnsafeOnlyInputDoesNoQueueOrProviderWork`
- AC6.4: Provider, persistence, validation, queue, timeout, and source-mismatch faults retain
  their existing non-Internal typed mapping; diagnostics and errors remain bounded/content-free.
  - verify: `TestADR_0298_NonMaterializationFaultsRetainTypedMappings`

---

### Scenario 7 — cancellation and Build close bound materialization

Materialization is synchronous caller-run pre-admission work behind one Build-owned lifecycle
gate/context and bounded active-operation accounting. It has no queue and starts no goroutine per
job.

**Acceptance:**

- AC7.1: Explicit cancellation during a blocked/large streaming scan returns the existing typed
  cancellation promptly and leaves no queue, receipt, singleflight, reservation, provider,
  repository-open, proposal, or promotion state.
  - verify: `TestScalableReflectionEvidence_Scenario7_ExplicitCancellationStopsMaterialization`
- AC7.2: Automatic run cancellation before admission terminates the caller-run scan without a
  detached goroutine or published reflection job; detachment may occur only after successful
  bounded materialization and coordinator admission.
  - verify: `TestADR_0298_AutomaticCancellationStopsPreAdmissionMaterialization`
- AC7.3: `Built.Close` closes the materialization gate, rejects new scans, cancels and joins all
  active scans, then performs existing coordinator queued/running shutdown; it cannot return while
  materialization-owned work remains.
  - verify: `TestScalableReflectionEvidence_Scenario7_BuiltCloseCancelsAndJoinsMaterialization`
- AC7.4: Race/leak proofs show no per-job materialization goroutine, double receipt/proposal,
  post-close admission, leaked active operation, or provider call after cancellation/close.
  - verify: `TestADR_0298_MaterializationCancelCloseRaceAndNoPerJobGoroutine`

---

### Scenario 8 — surrounding policy, provider, and coordinator controls remain authoritative

Selected evidence changes the bounded unit, not authority. Explicit reflection still reloads
the completed session's durable provider/model and works when automatic learning is off;
automatic reflection still earns admission and reserves spend under existing controls.

**Acceptance:**

- AC8.1: Explicit reflection reloads the session's persisted provider/model after restart,
  keeps a configured reflection model on that provider, and never silently uses the process
  default; selection is provider-neutral.
  - verify: `TestScalableReflectionEvidence_Scenario8_ExplicitUsesPersistedProviderModel`
- AC8.2: In learning mode `off`, explicit reflection lazily materializes and then uses the
  existing synchronous coordinator/repository path, while no automatic observer, controller,
  worker, provider call, or eager proposal repository exists.
  - verify: `TestScalableReflectionEvidence_Scenario8_OffModeExplicitOnly`
- AC8.3: Automatic policy admission, sensitivity, current-span verification, cooldown,
  completed cache, process/principal budgets, and reservation ordering remain enforced; an
  admitted selected input consumes reservation on timeout/failure/reflector abstention as
  before, while pre-selection skip consumes none.
  - verify: `TestADR_0298_AutomaticAdmissionCooldownBudgetReservationUnchanged`
- AC8.4: Coordinator global/per-principal count limits, selected-job and aggregate queued-byte
  limits, fair FIFO rotation, receipt capacity, timeout, cancellation, singleflight, and
  close behavior remain enforced around the selected-evidence job.
  - verify: `TestADR_0298_CoordinatorResourceSafetyUnchanged`
- AC8.5: Review/auto staging, trust and ownership checks, proposal CAS, conflict handling,
  promotion eligibility, and undo semantics are unchanged after evidence verification.
  - verify: `TestADR_0298_StagingPromotionAndUndoControlsUnchanged`
- AC8.6: The intentional exported `engine/learning` evidence/provenance changes are present in
  the API snapshots and classified in `engine/CHANGELOG.md`; both modules remain standalone
  and layering-clean.
  - verify: `TestScalableReflectionEvidence_Scenario8_EngineAPIAndLayeringGates`

---

### Scenario 9 — clients distinguish abstention, refusal, and completed receipts

The wire carries the bounded explicit abstention reason needed by clients. mecatui keeps
`/reflect` synchronous and shows a safe status without opening proposal review or pretending
a proposal was staged.

**Acceptance:**

- AC9.1: gRPC and HTTP project explicit no-safe-evidence as successful abstention using only
  the closed reason and stable harness text; cancellation, closed, failed precondition,
  queue-full, timeout, validation, persistence, and provider faults retain existing non-Internal
  typed classifications.
  - verify: `TestScalableReflectionEvidence_Scenario9_TransportDispositionAndTypedErrorMatrix`
- AC9.2: The proto-free mecatui client preserves only the closed reason/stable text and repairs
  every producer-influenced string; malformed UTF-8/control content cannot reach output.
  - verify: `TestADR_0298_MecatuiClientMapsClosedSafeAbstentionReason`
- AC9.3: `/reflect` displays in-progress, then muted stable abstention text, a success count, or
  a sanitized typed failure; stale generations cannot overwrite newer status.
  - verify: `TestScalableReflectionEvidence_Scenario9_MecatuiReflectStatusMatrix`
- AC9.4: Proposal detail marks manifest/source mismatch non-approvable, retains the bounded
  redacted evidence preview contract, and never displays manifest entries or raw source text.
  - verify: `TestADR_0298_MecatuiMismatchAndPreviewRemainNonDisclosing`

## Settled protocol decisions

ADR 0298 fixes the complete v1 ranking table, connected tool-turn component, whole-component
oversize behavior, mandatory-span outcomes, safe-field projection, aggregate manifest and identity
boundary, selected-local versus durable coordinates, legacy ordinal dispatch, lifecycle ownership,
and closed outcome vocabulary. No material protocol choice remains open in this plan.

## Implementation sequence

1. Add failing engine tests and the versioned bounded selector, streaming admission support,
   immutable aggregate manifest, selected-local handle resolution, legacy protocol reader, closed
   outcomes, and API snapshots/changelog.
2. Route automatic and explicit host construction through the materializer lifecycle gate and
   switch coordinator/proposal identity to selected-evidence identity.
3. Persist the complete manifest and make metadata-only list, detail, and approval use their
   specified one-time exact re-materialization and fail-precondition guards.
4. Project closed abstention reasons and typed faults through transports/client/TUI, update the
   ADR-0027 inventories, then run coordinator, lifecycle, race, standalone-engine, API, and docs
   gates.

## Required aggregate verification

- `task api:update` only for the intentional exported engine API delta, with the resulting
  `engine/api/*.txt` and `engine/CHANGELOG.md` change reviewed.
- `task lint`
- `task test`
- `task docs`
- `go run ./cmd/mecademo`

## See also

- [ADR 0109 — Evidence-backed reflection and durable staged learning](../adr/0109-staged-learning-proposals.md)
- [ADR 0114 — Configurable learning trigger policy](../adr/0114-configurable-learning-trigger-policy.md)
- [Architecture: Evidence-backed reflection](../architecture.md#evidence-backed-reflection)
- [Implementation notes](../design/IMPLEMENTATION-NOTES.md)
