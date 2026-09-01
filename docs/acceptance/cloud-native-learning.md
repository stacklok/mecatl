# Cloud-native learning v2 — acceptance plan

**Phase:** capability — durable, distributed procedure learning
**Status:** landed, 2026-08-31. Settled design synthesis.
**ADR:** [ADR-0294](../adr/0294-cloud-native-learning.md) — authoritative learning attempts and distributed downstream authority.
**Accumulator branch:** `acc/cloud-native-learning` (off `main`).

The smallest incremental body of work that makes an explicit principal request to learn a procedure durable, recoverable across replicas, and eventually callable from every eligible replica. It fixes the current explicit-intent false negative before adding distributed work, then promotes only work that crossed admission into a durable attempt lifecycle.

This plan is scenario-first: each wave is independently demonstrable and later waves build on the prior durable boundary. It does not decompose work into orchestrator task files.

## Why these scope cuts

- [ADR-0294](../adr/0294-cloud-native-learning.md) makes an admitted attempt, not `EventLog` or a process-local receipt, the workflow authority. This preserves the loop's storage-agnostic boundary.
- [ADR-0114](../adr/0114-configurable-learning-trigger-policy.md) remains authoritative for the weighted admission policy until the later distributed-budget wave; its process-local reset semantics are superseded only where ADR-0294 says so.
- [ADR-0111](../adr/0111-hardened-agent-owned-skill-publication.md) already pins repository CAS, provenance, evaluation, activation, and partitioned catalog safety. The distributed slice preserves rather than replaces those invariants.
- [ADR-0249](../adr/0249-durable-run-identity.md) and [ADR-0250](../adr/0250-durable-cursors-and-watch.md) are explicit dependencies: source reconstruction needs durable `run_id`; cursor/watch transport must not be assumed landed before its own acceptance gate.

## In scope — 6 scenarios, in implementation order

### Scenario 1 — Genuine current imperative admission

A main-session completion with a verified current principal-authored imperative such as “create a skill,” “make/build a skill,” “turn this workflow/procedure into a skill,” or an established learn/save form hard-admits procedure learning. The detector is narrow: negated requests, questions about learning capability, assistant/tool/web/repository/historical/synthetic text, compacted or unverifiable spans, and non-main sessions fail closed. `SkillDraft` stays inactive: requesting learning does not make a direct draft active. This repairs the defect independently of queue durability, as required by [ADR-0114](../adr/0114-configurable-learning-trigger-policy.md) and the existing [architecture learning boundary](../architecture.md#evidence-backed-reflection).

**Acceptance:**
- AC1.1: Each listed genuine, current principal-authored imperative produces hard procedure admission without a weighted signal only when the main-session terminal is one of ADR-0114's exact hard-stop set: `end_turn`, max-turn, max-tool-call, or run-budget. Failed, cancelled, awaiting, no-progress, timeout, and structured-output terminals do not admit.
  - verify: `TestADR_0294_ExplicitIntentUsesOnlyADRElevenFourHardStops`
- AC1.2: Negated imperatives and meta/capability questions do not admit learning.
  - verify: `TestCloudNativeLearning_Scenario1_NegatedAndMetaIntentRejected`
- AC1.3: Assistant, tool, web, repository, historical, synthetic, non-main, compacted, and unverifiable text cannot manufacture explicit learning authority.
  - verify: `TestADR_0294_ExplicitIntentRequiresVerifiedCurrentPrincipalPrompt`
- AC1.4: A direct `SkillDraft` remains Draft/inactive after this detector admits or rejects a procedure request.
  - verify: `TestCloudNativeLearning_Scenario1_DirectSkillDraftRemainsInactive`

---

### Scenario 2 — Authoritative admitted-attempt record

After explicit or weighted admission succeeds, and only after landed ADR-0249 supplies a non-zero durable `RunID`, the harness creates one deterministic, caller/session/run-partitioned `learning.Attempt` before reporting `queued`. Missing, zero, or non-durable `RunID` refuses admission before any queued status and creates no attempt. `learning.AttemptRepository` is the storage-neutral domain seam: opaque CAS versions, closed queued/running/terminal states, expiring fenced worker claims, bounded safe metadata, retry/reconciliation, retention, and deletion. It stores neither raw prompt/tool/event/archive content, paths, principal values, credentials, tokens, headers, secret-shaped values, driver-error text, diagnostics, metrics, watch envelopes, nor optional EventLog projections. Its immutable, content-free admission provenance records the admission class and the exact current-prompt/RunID/canonical-digest binding. Workers use that binding, never replayed user-role text, to establish explicit authority. Non-admitted/skipped work remains an immediate status and content-free metric only; it never becomes durable learning history. This replaces the reset-by-design coordinator/receipt authority described by [ADR-0109](../adr/0109-staged-learning-proposals.md) and is inventoried under [ADR-0027](../adr/0027-cloud-native.md).

**Acceptance:**
- AC2.1: `queued` is returned only after landed ADR-0249 supplies a non-zero durable `RunID` and durable create succeeds; absent, zero, or non-durable RunID refuses admission before queuing and creates no attempt. A reload from a second process finds the same deterministic attempt in `queued`, `running`, or a terminal state; duplicate admission converges to that attempt.
  - verify: `TestADR_0294_QueuedAttemptRequiresDurableRunIDAndIsIdempotent`
- AC2.2: The immutable, content-free provenance records admission class plus the exact current-prompt/RunID/canonical-digest binding. Workers never re-derive explicit authority from replayed user-role text; forged provenance fields and synthetic continuations fail closed.
  - verify: `TestADR_0294_AdmissionProvenanceBindsCurrentPromptAndRejectsForgery`
- AC2.3: Attempts permit only legal CAS transitions. An expired, released, superseded, retried, or abandoned claim cannot renew, checkpoint, release, or finalize its attempt; cannot finalize a successor; and cannot rewrite an abandoned attempt to success.
  - verify: `TestADR_0294_StaleClaimCannotTransitionAttempt`
- AC2.4: Attempt projections and stored records contain only bounded safe metadata and closed failure codes; structural tests reject raw content, paths, principal values, credentials, tokens, headers, secret-shaped values, driver-error text, diagnostics, metrics, watch envelopes, and optional EventLog projections.
  - verify: `TestADR_0294_AttemptSurfacesContainNoContentOrSecrets`
- AC2.5: A skipped or non-admitted completion produces immediate status plus content-free metrics but creates no attempt record.
  - verify: `TestCloudNativeLearning_Scenario2_NonAdmittedWorkIsNotDurable`
- AC2.6: Retention/deletion are caller-partitioned, CAS-safe where applicable, and cannot delete a claimed nonterminal attempt.
  - verify: `TestCloudNativeLearning_Scenario2_RetentionAndDeletionRespectClaimsAndPartition`

---

### Scenario 3 — Explicit durable procedure-learning vertical slice

An explicit accepted request drives a durable attempt through claim, evidence reconstruction, reflection/materialization, proposal/skill linkage, and a distinct terminal status: completed, `abstained`/`no_candidate`, evidence failure, evaluation rejection, or publication failure. The attempt references the exact source session, durable `RunID`, and canonical digest rather than copying a transcript. A worker uses its private immutable owner binding and exact-source delegated access to reconstruct only through the existing bounded, secret-safe canonical learning projection; it applies the governance untrusted fence at every remote/restarted model boundary. It never passes raw transcript, archive, tool, or event text to a model, nor re-derives explicit authority from replayed user-role text. It validates owner, run identity, ordering, and digest, and fails with a safe evidence-unavailable code on missing, gapped, compacted-without-archive, or mismatched evidence. `EventLog` can later receive a content-free projection, but remains neither source of truth nor workflow state, consistent with [ADR-0027](../adr/0027-cloud-native.md) and [ADR-0249](../adr/0249-durable-run-identity.md).

**Acceptance:**
- AC3.1: An explicit imperative request returns a durable queued attempt and, across Build/process replacement, reaches a terminal attempt linked to its authorized proposal and/or skill when downstream capabilities are wired.
  - verify: `TestCloudNativeLearning_Scenario3_ExplicitProcedureAttemptSurvivesRestart`
- AC3.2: The worker uses its private immutable owner binding and exact-source delegated access to reconstruct the existing bounded, secret-safe canonical learning projection from the exact source session, `RunID`, and digest. It fences that projection at every remote/restarted model boundary; raw transcript, archive, tool, and event text never cross the boundary, and prompt-injection/framing payloads cannot alter authority or instructions.
  - verify: `TestADR_0294_EvidenceProjectionIsBoundedFencedAndInjectionSafe`
- AC3.3: Missing, gap-marked, compacted-without-recoverable-archive, unauthorized, or digest/run-mismatched evidence terminally fails closed with a safe code and creates no proposal/skill mutation. No caller-supplied principal or system-principal bypass is accepted; inaccessible foreign or missing source evidence has the same absence-style result.
  - verify: `TestADR_0294_WorkerSourceAuthorityFailsClosedWithoutIdentityOracle`
- AC3.4: `abstained`/`no_candidate` is a valid distinct terminal, separate from evidence failure, evaluation rejection, and publication failure.
  - verify: `TestADR_0294_AbstentionIsASeparateTerminalOutcome`
- AC3.5: Crash after claim or after a downstream durable boundary is reconciled idempotently; a retry neither duplicates a proposal/skill nor reports an invented success.
  - verify: `TestADR_0294_AttemptReconciliationIsIdempotent`
- AC3.6: With learning unwired or off, engine composition and ordinary runs remain byte-identical and allocate no attempt repository, worker, or durable attempt.
  - verify: `TestCloudNativeLearning_Scenario3_UnwiredLearningIsByteIdentical`

---

### Scenario 4 — Independent downstream convergence and replica-safe skill visibility

The distributed contract reaches beyond the queue: `ProposalRepository` and `SkillRepository` need durable/driver implementations preserving current CAS, provenance, evaluation, activation, and caller/project partition invariants. Their deterministic, CAS-protected commits are independent of attempt claim ownership: a late independently valid commit never proves attempt success, and reconciliation may adopt only a compatible deterministic artifact or leave inactive/unlinked residue or safe non-success. Catalogs are derived per-partition monotonic-generation caches, not claim-fenced attempt state. A committed Active skill is hydrated or invalidated replica-safely, so it does not mean “active only on the pod that processed it.” Caller-bound catalog selection remains path-free and partition-isolated as [ADR-0111](../adr/0111-hardened-agent-owned-skill-publication.md) requires.

**Acceptance:**
- AC4.1: Distributed ProposalRepository and SkillRepository implementations satisfy their existing shared conformance suites, including opaque CAS, provenance, evaluation, activation, recovery, and partition isolation.
  - verify: `TestCloudNativeLearning_Scenario4_DistributedRepositoriesConform`
- AC4.2: Crash and claim-loss races converge through deterministic IDs and CAS: no duplicate artifact, overwrite of a newer target revision, two active versions, invented attempt success, or partition crossing. An independently valid late downstream commit is allowed; authoritative reread adopts only a compatible deterministic artifact, otherwise leaves inactive/unlinked residue or reaches safe non-success.
  - verify: `TestADR_0294_IndependentDownstreamCommitReconcilesAfterClaimLoss`
- AC4.3: Catalog publication, hydration, and invalidation converge by authoritative per-partition monotonic generation after Active, archive, rollback, or replacement transitions. An authorized session on replica B serves a wholly old or wholly new partition snapshot; delayed old publish/invalidate cannot replace or revoke a newer generation. Instant claim-driven invalidation is not promised, and an unauthorized or non-admitted partition sees nothing.
  - verify: `TestADR_0294_ReplicaHydrationConvergesAcrossReplacementAndRollback`
- AC4.4: Publication/hydration uncertainty fail-closes only the affected partition and cannot revoke a newer durable active generation.
  - verify: `TestADR_0294_LearnedSkillPartitionPublicationIsolation`

---

### Scenario 5 — Authorized attempt inspection and control

The service exposes caller-authorized attempt get/list state and defined manual retry/abandon controls with bounded pages and opaque cursors, safe closed failure codes, and authorized proposal/skill links. Ownership/non-disclosure applies before locks, signals, diagnostics, and pagination/count/cursor construction: foreign and missing attempts are indistinguishable with no testable timing or existence oracle. The private owner binding created with the attempt is the only authority input; callers cannot supply a principal or system-principal bypass. Driver-backed learning requires workload-authenticated claims, separated caller and infrastructure RPCs, opaque project namespaces (never raw workspace paths), and fail-closed startup if ownership cannot be enforced. Attempt watch is deferred: ADR-0250's session `EventLog` watch is not an attempt watch feed. A later ADR must specify a durable attempt-change feed whose notifications are advisory and whose clients re-read `AttemptRepository` under authority.

**Acceptance:**
- AC5.1: The owner can get and page through attempt projections, while another caller cannot infer existence, metadata, proposal IDs, skill IDs, counts, cursors, timing, or diagnostics. This non-disclosure holds before locks/signals/diagnostics and pagination/count/cursor construction; foreign and missing requests have the same absence-style result.
  - verify: `TestADR_0294_AttemptControlsAreNonDisclosingBeforeSideEffects`
- AC5.2: Attempt API projections expose only bounded state, timestamps, safe codes, and authorized identifiers; they never expose transcript, tool output, provider text, paths, principal values, credentials, tokens, headers, secret-shaped values, driver errors, diagnostics, metrics, watch envelopes, or optional EventLog projections.
  - verify: `TestADR_0294_AttemptAPIIsContentFree`
- AC5.3: Manual retry and abandon perform only defined attempt CAS transitions; abandon is non-compensating and does not promise downstream rollback. Stale versions, terminal conflicts, and a live fenced claim return closed typed errors without changing the attempt. Private owner binding and exact-source delegation are enforced without caller-supplied principal or system-principal bypass.
  - verify: `TestADR_0294_AttemptControlsRequirePrivateOwnerBinding`
- AC5.4: Driver-backed learning enforces workload-authenticated claims; caller and infrastructure RPCs are separated; project namespaces are opaque rather than raw workspace paths; and startup fails closed when the driver cannot enforce ownership.
  - verify: `TestADR_0294_LearningDriversEnforceOwnershipOrFailClosed`
- AC5.5: Attempt watch is deferred. ADR-0250 session `EventLog` watch is explicitly not an attempt-watch feed; no endpoint, cursor, process-local substitute, or watch envelope is introduced in this plan.
  - verify: none — deferred to a separately specified durable attempt-change feed

---

### Scenario 6 — Distributed automatic admission controls

Weighted automatic work joins the same durable attempt queue only after explicit learning is proven. Count/token reservations, cooldowns, and deduplication become process-independent before the capability claims globally bounded semantics; until then, automatic mode explicitly retains ADR-0114's per-process limitation. This final wave has no separate process-local queue and preserves explicit hard admission's distinct policy.

**Acceptance:**
- AC6.1: Automatic weighted admission creates the same deterministic durable attempt lifecycle as explicit admission and cannot bypass queue capacity, evidence reconstruction, or downstream authority.
  - verify: `TestCloudNativeLearning_Scenario6_WeightedAdmissionUsesAttemptLifecycle`
- AC6.2: Concurrent replicas enforce one configured global automatic count/token budget, cooldown, and deduplication window without multiplying spend or durable attempts.
  - verify: `TestADR_0294_AutomaticAdmissionControlsAreProcessIndependent`
- AC6.3: Distributed automatic reservation is tied to deterministic attempt identity. Failure-injection covers reserve/create linkage; crashes before and after each boundary; timeout, expiry, reassignment, and abandonment; retained versus reclaimed charge; and proves that retries never exceed the configured global maximum.
  - verify: `TestADR_0294_AutomaticReservationsReconcileWithoutExceedingGlobalMaximum`
- AC6.4: Documentation and capability reporting do not claim global automatic bounds until AC6.2 is wired; the earlier explicit-only slice says so plainly.
  - verify: `TestCloudNativeLearning_Scenario6_NoPrematureGlobalBoundClaim`

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| A process-local learning status API or receipt cache as durable authority | Never; use AttemptRepository | [ADR-0294](../adr/0294-cloud-native-learning.md) |
| EventLog as learning workflow state or transcript duplication in attempts | Never; EventLog is evidence/projection only | [ADR-0294](../adr/0294-cloud-native-learning.md) |
| Automatic global budgets/cooldowns/dedupe before their durable distributed ledger lands | Scenario 6 | [ADR-0114](../adr/0114-configurable-learning-trigger-policy.md) |
| Attempt watch, including cursor binding to caller/query-or-attempt scope/generation and indistinguishable rejection of tampered, expired, or foreign cursors | Separately specified follow-up after this plan | Must use a durable attempt-change feed; ADR-0250 session EventLog watch is not that feed |
| A process-local or session-EventLog-derived substitute for attempt watch | Never | Attempt notifications are advisory only; clients must re-read AttemptRepository under authority |
| A new direct-SkillDraft activation path | Not part of learning admission | [ADR-0111](../adr/0111-hardened-agent-owned-skill-publication.md) |
| Strict universal prevention of late downstream writes | Separate future decision | Requires a unified linearizable learning authority; independent repositories intentionally permit an independently valid late commit after claim loss |

## Sequencing recommendation

Land Scenario 1 alone first. Scenarios 2 and 3 form the first durable vertical slice and must update both ADR 0027 inventories before code lands. Scenario 4 follows before any distributed claim is made for learned skills, but is the remaining oversized scenario: `/plan-orchestrate` must split it into dependency-ordered small waves for driver conformance and attempt-lifecycle fencing first, then per-partition generation and replica hydration/invalidation convergence. Scenario 5 may land its get/list/control half after Scenario 3; attempt watch is deferred to a separately specified durable attempt-change-feed follow-up, not ADR-0250 session watch. Scenario 6 is last; prior waves must describe automatic controls as process-local.

## Definition of done

1. All six scenarios' acceptance criteria and named tests are green, including cross-Build/cross-replica offline proofs.
2. `task lint`, `task test`, `task api:check`, and `go run ./cmd/mecademo` pass; if an engine export changes, `task api:update` and the classified `engine/CHANGELOG.md` entry are committed.
3. `task docs` and `task site:build` pass; `docs/architecture.md`, `docs/design/IMPLEMENTATION-NOTES.md`, the ADR 0027 List 1/2 inventories, `docs/usage.md`, and relevant `user-docs/` material describe landed behavior.
4. `task ac-trace-strict` passes when this plan becomes `landed`.
5. Every new outlives-call resource and restart-lost state has an explicit ADR 0027 inventory/rehydration decision, and every attempt record/API projection passes the content-free structural guards.
6. The unwired/off configuration is demonstrated byte-identical, and no stage claims distributed automatic bounds before Scenario 6.

## Deferred decisions and known risks

- **Attempt store topology and driver RPC shape.** ADR-0294 fixes the port contract, fencing, and semantics, but the concrete backing-store/driver schema selection should follow the existing driver-conformance pattern in the implementation wave.
- **Evidence retention versus attempt retention.** An attempt may outlive its reconstructable session/log evidence; terminal `evidence_unavailable` is the intentional honest result. Retention durations and operator policy remain a bounded configuration design for the implementation slice.
- **Attempt notifications.** Attempt watch is deferred to a separate ADR. It must define durable attempt-change records and cursors bound to caller, query/attempt scope, and generation; notifications are advisory and clients re-read `AttemptRepository` under authority. ADR-0250's session `EventLog` watch is not this feed.
- **Global automatic ledger mechanics.** Scenario 6 requires atomic distributed reservation semantics, including deterministic reserve/create linkage, crash-boundary charge reconciliation, expiry/reassignment, and a global-maximum proof, but intentionally does not pre-select Redis, a driver, or another backend before its conformance and deployment constraints are specified.
- **Unified late-write prevention.** This plan's independent durable boundaries deliberately allow an independently valid late downstream commit after claim loss; it must never prove attempt success. The kill criterion for claiming strict no-late-downstream-write semantics is a separately designed unified linearizable learning authority.

## Exit criteria

When every point under *Definition of done* holds on `acc/cloud-native-learning`, this plan is satisfied.
