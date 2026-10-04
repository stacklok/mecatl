# Purpose-attributed auxiliary token usage — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — adds recognized durable session token-accounting purposes, forward-compatible opaque-kind persistence, and their existing public projection while preserving budget and run-usage semantics.
**Decision record:** [ADR 0354](../adr/0354-returned-auxiliary-usage-results.md)
**Phase:** canonical auxiliary usage accounting
**Status:** landed, 2026-09-23. Material API revision approved with the issue owner.
**Delivery:** Split. Implementation restarted after amendment approval.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1216](https://github.com/stacklok/mecatl/issues/1216).
**Plan PR:** [#1817](https://github.com/stacklok/mecatl/pull/1817)
**Approved baseline:** `36a618a621b5ec53b593c72930bc229e60c2adf7`

Extend the canonical `Session.tokenUsage` ledger from title generation to reachable
session-associated auxiliary LLM calls other than evidence reflection. Recorded purposes
retain actual server-selected provider/model attribution without changing normal run usage;
only `router` contributes to the internal `MaxRunTokens` calculation. Reflection still
returns attributed usage but never writes it to the source session, as decided in
[ADR 0373](../adr/0373-reflection-usage-without-session-writes.md). The other purposes
follow [ADR 0350](../adr/0350-purpose-attributed-auxiliary-token-usage.md) and the
ledger/budget separation in [ADR 0307](../adr/0307-canonical-durable-token-accounting.md).

## Human decisions

- [x] Scope is reachable, session-associated auxiliary LLM calls other than reflection; normal runs retain their existing `main` accounting, while dream and consolidation planning remains outside session accounting. — Decision: reflection returns attributed usage without a session write (ADR 0373); track dream and consolidation separately in [#1791](https://github.com/stacklok/mecatl/issues/1791).
- [x] The recognized usage kinds are `main`, `session_title`, `compaction`, `reflection`, `router`, `ask_reviewer`, `guardrail`, and `parallel_judge`. — Decision: readers preserve existing `reflection` buckets, but reflection no longer writes them; other kinds describe current writer purpose. Non-empty unrecognized kinds remain opaque forward-compatible buckets.
- [x] Each bucket records the actual server-selected provider/model and does not roll into main usage. — Decision: auxiliary usage never changes `Session.Usage`, `EvResult.Usage`, team budgets, or conversation history; only the separate `router` bucket contributes to the internal `MaxRunTokens` calculation, preserving the existing classifier spend bound without a main rollup.
- [x] Accounting is forward-only and best-effort. — Decision: record reported partial-stream usage; count each physical retry attempt exactly once; do not retry uncertain calls solely for accounting, backfill historical records, or introduce pricing.
- [x] Mixed-version writers need not preserve new auxiliary buckets. — Decision: an older binary may discard unknown buckets after reading and saving a new snapshot; this early-product tradeoff does not add rollout configuration or compatibility preservation.
- [x] No client/UI presentation is part of this change. — Decision: existing canonical ledger projections remain the only exposure.
- [x] Auxiliary producers return a full purpose/model-attributed dictionary. — Decision: add `session.ProviderModelID{ProviderID, ModelID}` as the opaque identity of the server-selected provider/model for an auxiliary call; it carries no selector/default, context-window, reasoning-effort, provider-instance, or credential semantics. Jev uses `ProviderID: "jev"` and its configured classifier as `ModelID`, despite not appearing in the provider catalog. Break pre-v1 APIs append `session.AuxiliaryUsage` immediately before `error`; `ModelRouteResult.Usage` becomes `session.AuxiliaryUsage`. The result wrapper remains distinct from the durable ledger map. Parent callers preserve valid producer attribution while validating/remapping purpose buckets. An empty purpose is normalized to non-empty `unknown`, preserving reported spend under a separate `unknown` model attribution when needed.
- [x] Detached accounting does not create ownership. — Decision: a current Service owner records non-reflection returned usage only through an already-valid capability; detached or post-lease-loss paths drop it with bounded diagnostics, without accounting-only lease acquisition, load, replay, or persistence. Reflection usage is never recorded on the source session, regardless of ownership; see ADR 0373.

## Interface contract

- **gRPC / protobuf:** None — existing session and session-summary canonical `token_usage` map projections carry recognized and opaque forward-compatible kind keys; no RPC, message, or field changes.
- **Exported Go APIs / interfaces:** Add `session.ProviderModelID{ProviderID, ModelID}` as an opaque server-selected call identity and `session.AuxiliaryUsage{Buckets map[UsageKind]TokenUsage}` with an owned-copy `Merge(AuxiliaryUsage) AuxiliaryUsage` operation. `AuxiliaryUsage` remains a returned-result wrapper, distinct from the durable ledger map. `ProviderModelID` has no selector/default, context-window, reasoning-effort, provider-instance, or credential semantics. Break `agent.Compactor.Compact` to return `(compacted, summary, usage, err)`; `agent.EvidenceReflector.Reflect` and `ReflectProjection` to return `(outcome, usage, err)`; `agent.ChildAskReviewer.Review` to return `(review, usage, err)`; `agent.BranchJudge.Judge` to return `(winner, rationale, usage, err)`; and `agent.RunGuardrailCheck` to return `(text, usage, err)`, where `usage` is `session.AuxiliaryUsage`. `agent.RunModelRouter` and `agent.Deps.SubagentModelRouter` return the complete value through `ModelRouteResult.Usage`, whose type changes to `session.AuxiliaryUsage`. Break `port.PermissionPolicy.Evaluate` to return `port.PermissionResult{Decision governance.PermissionDecision, Usage session.AuxiliaryUsage}`; its path-escape checker returns usage in that result for every evaluation, including failed and post-wait evaluations. Producers assign purpose and exact attribution; parent callers validate/remap buckets but preserve valid producer attribution, except reflection callers discard returned usage after diagnostics. An empty purpose normalizes to non-empty `unknown`, retaining reported spend and a separate `unknown` model attribution when required. Jev uses `ProviderModelID{ProviderID: "jev"}` with its configured classifier ID as `ModelID`, without becoming a provider-registry entry. Add recognized `UsageKind` constants and preserve opaque non-empty kinds. Add `agent.UtilityEngineUsage(kind, identity, session)` to convert a utility session's `main` usage to the caller-selected purpose and provider/model while retaining nested auxiliary buckets; the owner separately applies `RemapAuxiliaryUsage`. Do not widen `port.LLMProvider` or `port.LLMRequest`.
- **Tool schemas:** None — no tool name, parameter, result, or model-visible affordance changes.
- **CLI / config:** None — existing `models.slots.compaction`, `reflection`, `router`, `ask-reviewer`, and `guardrail` continue to select models without new keys or defaults; Parallel judging gains no slot.
- **Events / persistence:** Persist non-reflection returned auxiliary buckets through existing `token_usage` snapshots, stores, event-source metadata, and authorized session/session-summary projections. No new event or per-attempt ledger is added. Producers aggregate each provider attempt's emissions once, including reported usage from failed attempts. A current Service owner records non-reflection results only through an already-valid capability; detached or post-lease-loss paths drop them with bounded diagnostics and never acquire a lease, load/reload, replay, or persist a session solely for accounting. Automatic inline, explicit, detached, and recovered reflection results never write usage to a session; non-empty returned usage instead produces one bounded structured diagnostic at each discard boundary. Reflection uses no accounting-only lock, load/reload, save, or lease operation.
- **Security / authority:** Composition/server code selects the provider/model and binds every recorded call to its true source session. Accounting stores purpose, aggregate token counts, and provider/model attribution only; it never stores prompts, model output, raw provider errors, request IDs, credentials, or client-selected billing controls. Calls without a source session are not attributed to any session.
- **Compatibility / migration:** Intentional pre-v1 breaking engine API change: regenerate `engine/api/*.txt` and classify the changed signatures in `engine/CHANGELOG.md` as Changed/minor. There is no old/new callback bridge. Current and future readers preserve non-empty unrecognized kind buckets across restore/save; existing snapshots retain their `main`, `session_title`, and legacy `unknown` attribution behavior, with no backfill or rewrite.

## In scope — 4 scenarios, in implementation order

### Scenario 1 — Recognized auxiliary usage kinds preserve main accounting

The aggregate owns recognized usage kinds and canonical per-model totals while accepting
unrecognized non-empty kinds as opaque forward-compatible buckets. This extends
the title-ledger boundary from [ADR 0307](../adr/0307-canonical-durable-token-accounting.md)
without changing the normal-run compatibility mirror or client-visible run usage. The separate
`router` bucket alone preserves the model router's existing internal spend bound.

**Acceptance:**
- AC1.1: The session accepts and preserves the recognized `compaction`, `reflection`, `router`, `ask_reviewer`, `guardrail`, and `parallel_judge` kinds in addition to existing kinds, each with totals equal to its provider/model entries; it also preserves non-empty unrecognized kinds as opaque buckets.
  - verify: `TestAuxiliaryTokenUsage_Scenario1_RecognizedAndOpaqueKindTotals`
- AC1.2: Recording any auxiliary kind leaves `Session.Usage`, `token_usage[main]`, normal `EvResult.Usage`, and Team budget calculations unchanged; only `router` independently contributes to the invoking session's internal `MaxRunTokens` calculation.
  - verify: `TestAuxiliaryTokenUsage_Scenario1_AuxiliaryKindsDoNotSpendMainBudget`, `TestADR_0350_RouterUsageRetainsSpendBound`
- AC1.3: An unrecognized non-empty usage kind round-trips as an opaque ledger bucket and never changes `Session.Usage`, `token_usage[main]`, or a budget; only recognized `router` usage has the explicit separate budget treatment.
  - verify: `TestAuxiliaryTokenUsage_Scenario1_PreservesOpaqueKindsWithoutBudgetEffect`

### Scenario 2 — Direct session work preserves actual selected-model attribution

Compaction and reflection invoke providers outside a normal `Engine.runTurn` with a
source session. Compaction persists usage under its selected provider/model; reflection
returns the attributed usage, including partial/error usage, but does not write it to
the session, as decided in [ADR 0373](../adr/0373-reflection-usage-without-session-writes.md).
Model slots select calls, not persisted accounting keys.

**Acceptance:**
- AC2.1: A tier-4 compaction summary records all provider-reported usage under `compaction` for the compacted session and its actual selected provider/model.
  - verify: `TestAuxiliaryTokenUsage_Scenario2_CompactionRecordsSelectedModel`
- AC2.2: Automatic inline, explicit, detached, and recovered evidence reflection return all provider-reported usage under `reflection` with actual selected provider/model attribution, including usage before an error. None records reflection usage on the source session. Explicit reflection retains its original single authorized load for reflection work, but no reflection path takes an accounting-only lock, loads/reloads, saves, or acquires a lease to persist usage. Each non-empty returned usage value is discarded with one bounded structured diagnostic.
  - verify: `TestAuxiliaryTokenUsage_Scenario2_ReflectionRecordsSelectedModel`, `TestAuxiliaryTokenUsage_Scenario2_DetachedReflectionUsageIsDropped`
- AC2.3: Reported usage observed before an auxiliary direct-call terminal error or cancellation is returned; a failed physical attempt followed by a retry contributes each attempt's reported usage exactly once. A current owner may record non-reflection returned usage through its existing capability; reflection usage is not saved, and an uncertain crash/persistence window is not retried solely for accounting.
  - verify: `TestAuxiliaryTokenUsage_Scenario2_RetryAndPartialUsageAreCountedOnce`

### Scenario 3 — Ephemeral utility engines keep purpose and ownership

Model routing, child-ask review, guardrail checks, and Parallel judging create bounded utility
engines or calls whose temporary sessions are not the durable owner. Their token usage belongs to
the invoking parent session, in a purpose-specific bucket rather than `main`, as required by
[ADR 0350](../adr/0350-purpose-attributed-auxiliary-token-usage.md). The model-router
case replaces its existing main-usage folding; ordinary Subagent, Parallel branch, Team member,
and lead runs remain their own normal sessions.

**Acceptance:**
- AC3.1: A semantic model-router invocation records its reported usage under `router` for the invoking session, with its classifier's actual provider/model, and no longer folds that usage into `main`; the separate router total continues to enforce the existing internal `MaxRunTokens` spend bound.
  - verify: `TestAuxiliaryTokenUsage_Scenario3_RouterDoesNotFoldIntoMain`, `TestADR_0350_RouterUsageRetainsSpendBound`
- AC3.2: A child-ask reviewer and a guardrail checker return reported usage under `ask_reviewer` and `guardrail`, respectively, for the parent session that caused the check. Parent callers remap results to their authorized purpose while preserving valid provider/model attribution and record them only while the owner remains valid.
  - verify: `TestAuxiliaryTokenUsage_Scenario3_SafetyChecksRecordParentUsage`
- AC3.3: A Parallel `join: "judge"` or `join: "best"` call records reported usage under `parallel_judge` for its invoking session, using the judge's actual inherited/resolved model and without introducing a model slot.
  - verify: `TestAuxiliaryTokenUsage_Scenario3_ParallelJudgeRecordsInheritedModel`
- AC3.4: A custom or non-LLM child reviewer or branch judge that reports no usage records no fabricated auxiliary usage; normal main, Subagent, Parallel-branch, and Team model runs preserve their existing `main` accounting in their own sessions.
  - verify: `TestAuxiliaryTokenUsage_Scenario3_NoUsageFromNonLLMHelpers`, `TestAuxiliaryTokenUsage_Scenario3_NormalChildRunsRemainMain`

### Scenario 4 — Durable projection is forward-only and source-safe

The canonical ledger already round-trips through snapshots, stores, event-sourced session
metadata, and authorized session projections. New purpose buckets must use that path without
inventing a client event or leaking accounting inputs, preserving the durable-accounting rules in
[ADR 0307](../adr/0307-canonical-durable-token-accounting.md). Calls with no source session remain
outside this ledger, as recorded in [#1791](https://github.com/stacklok/mecatl/issues/1791).

**Acceptance:**
- AC4.1: Every non-reflection auxiliary bucket round-trips through in-tree snapshot/store implementations, event-sourced reconstruction, and existing authorized session and session-summary projections. Previously stored reflection buckets remain readable; new reflection results never enter the ledger.
  - verify: `TestAuxiliaryTokenUsage_Scenario4_RoundTripAndProjection`
- AC4.2: Current and future readers preserve non-empty unrecognized buckets across restore/save; existing and legacy snapshots retain their current `main`, `session_title`, and `unknown` model attribution behavior without backfill. A ledger entry with an empty purpose is normalized to the non-empty `unknown` purpose bucket, retaining reported model usage. When an auxiliary result has a known caller-owned purpose, remapping instead charges that purpose even if the producer supplied an empty kind. Purpose and model attribution normalize independently. Older writers may discard unknown new auxiliary kinds after read/save, and no new per-attempt usage record is persisted.
  - verify: `TestAuxiliaryTokenUsage_Scenario4_OpaqueKindAndForwardOnlyCompatibility`, `TestRemapAuxiliaryUsageMissingPurposeRetainsSpend`
- AC4.3: Accounting records contain no prompt, model output, raw provider-error, request-id, credential, or client-selected provider/model field; dream and consolidation calls receive no fabricated session attribution.
  - verify: `TestAuxiliaryTokenUsage_Scenario4_NoSensitiveOrFabricatedAttribution`

## Out of scope

| Item | Defer-to | Decision |
| --- | --- | --- |
| Dream and memory-consolidation planner usage | [#1791](https://github.com/stacklok/mecatl/issues/1791) | It has no source session and must not fabricate session attribution. |
| Monetary price, rate cards, currency conversion, and billing reconciliation | Later accounting feature | Provider-reported token usage is the sole accounting unit in this plan. |
| New model slots, including a Parallel-judge slot | Later model-selection decision | Usage purpose and slot selection are related but not mechanically coupled. |
| New client/UI usage breakdown | Later client-observability work | Existing durable session projections are sufficient for this accounting change. |
| Historical usage backfill or a per-attempt billing ledger | Later migration/accounting work | Best-effort forward recording avoids unverifiable retry or replay claims. |

## Definition of done

1. `task lint`, `task test`, `task api:check`, and `task docs` pass.
2. `task ac-trace-strict` resolves every named proof when this plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green.
4. `engine/api/*.txt` and `engine/CHANGELOG.md` document the pre-v1 `ProviderModelID`, usage-kind, and auxiliary-result API changes.
5. The implementation PR links the approved Plan / Interface PR and reports exact interface conformance.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Provider-reported tokens are best effort rather than end-to-end billable-request reconciliation.
- A physical provider retry is accounted through the logical caller's forwarded usage; a crash after provider execution may lose accounting rather than justify replay.
- Accounting for source-less dream and consolidation planning is intentionally deferred to #1791.
