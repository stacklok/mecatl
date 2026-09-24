# Root-conversation provider correlation — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — adds a durable outbound HTTP correlation contract and an exported engine context API across delegation, model routing, independently versioned providers, and the Jev adapter while preserving the existing affinity contract.
**Decision record:** [ADR 0360](../adr/0360-root-session-provider-correlation.md)
**Phase:** delegated model-request correlation
**Status:** proposed, 2026-09-24. Exact interface contract ready for human Plan / Interface review; it remains proposed until this plan PR merges.
**Delivery:** Split docs → implementation. On 2026-09-24 the directing human asked for the design and implementation branches to be pushed and stacked, and confirmed "yes, keep on going", so the implementation proceeds as a stacked PR on this proposed-docs commit before the plan merges. This does not mark the plan approved, authorize any merge, relax contract-drift stops, or remove either human merge gate.
**Expected tasks:** 1 (implemented on the stacked implementation branch).
**Issue:** None — directed operator request.
**Plan PR:** [#1882](https://github.com/stacklok/mecatl/pull/1882).
**Approved baseline:** absent until contract approval by merge.

Add an outbound-only `X-Mecatl-Root-Session-ID` alongside the existing active-session
`X-Mecatl-Session-ID`. Provider calls made by a main run and its nested engines, plus both
delegated-model router backends, can then be grouped by the root value without collapsing durable
child sessions or changing client-to-server affinity. The behavior follows [ADR 0360](../adr/0360-root-session-provider-correlation.md)
and preserves the existing active-session contract in
[ADR 0216](../adr/0216-provider-session-correlation-header.md) and
[ADR 0294](../adr/0294-session-correlation-and-affinity.md).

## Human decisions

None — the operator accepted the separately named root header after confirming that the existing active-session header is also an ingress-affinity contract; the remaining propagation and compatibility behavior is fixed by ADR 0360.

## Interface contract

- **gRPC / protobuf:** None — no request, response, service, or field changes; `X-Mecatl-Root-Session-ID` is not accepted as gRPC/HTTP ingress affinity metadata.
- **Exported Go APIs / interfaces:** Add `port.WithRootSessionID(context.Context, session.SessionID) context.Context` and `port.RootSessionIDFromContext(context.Context) (session.SessionID, bool)`. `Engine` run entry sets the active run session as root only when no non-empty trusted-composition root exists and otherwise preserves the inherited value. Official service run entry always overwrites caller context with the authoritative loaded/source session before invoking an engine; the setter is a trusted composition seam, never a transport-metadata decoder. `port.LLMRequest` and `port.LLMProvider` remain unchanged.
- **Tool schemas:** None — Subagent, Parallel, Team, model-router, and inspection tool parameters/results retain their current identities and schemas.
- **CLI / config:** None — the root header is unconditional optional outbound correlation metadata with no flag, setting, precedence rule, or model-selection effect.
- **Events / persistence:** No durable or event change — root correlation is run-context-only and adds no session field, relationship, event, snapshot, store key, ledger value, lease key, or migration. A source-derived direct team's process-local declaration retains the authoritative source session ID from creation through `RunTeam` solely to seed member root context; cleanup discards it. Existing active child IDs and persisted lineage remain authoritative.
- **Security / authority:** The root is seeded only from an authoritative running/source session or from the current session when absent; official gRPC/HTTP run boundaries overwrite any caller-carried root, and `X-Mecatl-Root-Session-ID` ingress is ignored rather than decoded. It is sent only to the selected LLM provider/gateway and selected Jev classifier, grants no authority, and is never used for affinity, ownership, permissions, leases, fencing, tracing, idempotency, caching, provider state, or cancellation. Its exact outbound predicate is 1–256 bytes of printable ASCII with no boundary spaces; invalid/absent values omit only the optional root header without failing inference.
- **Compatibility / migration:** Additive outbound HTTP metadata and additive pre-v1 engine API. Existing `X-Mecatl-Session-ID` bytes and semantics remain unchanged. Run `task api:update`; include API snapshots and an Added/minor `engine/CHANGELOG.md` entry. A composition-owned final HTTP-client transport decorator projects the root header for the existing provider modules and Jev, so the independently versioned provider modules keep their production code and released engine dependency unchanged and remain standalone under `GOWORK=off`; no staged engine release or rollout flag is required.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — One causal root survives nested run identities

Run entry establishes a root only when one is absent, then preserves it through nested engines
without changing the active session identity described by
[the provider architecture](../architecture/providers.md). Official service boundaries overwrite
any caller-carried root with the authoritative loaded session before entering the run tree. Direct
teams derived from a source session retain that source in their process-local declaration and seed
it when `RunTeam` starts; source-less direct teams have no external conversation to claim as root.

**Acceptance:**
- AC1.1: A main session exposes both active and root context identities as its own ID; compaction retains that pair, while a nested Subagent, Parallel branch, team member, LLM model router, Parallel judge, child reviewer, or guardrail engine exposes its own active ID and the unchanged main root.
  - verify: `TestRootSessionProviderCorrelation_Scenario1_NestedRunsPreserveRoot`, `TestRootSessionProviderCorrelation_Scenario1_DetachedReviewerPreservesRoot`
- AC1.2: A resumed child uses the current invoking run tree's root while retaining its persisted child ID and relationship; no root value changes a session, store, event, ledger, lease, permission, inspection, or cancellation key.
  - verify: `TestRootSessionProviderCorrelation_Scenario1_ResumeUsesCurrentCausalRoot`
- AC1.3: A direct team created for a source session retains that source through its process-local declaration and uses it as every member's root when `RunTeam` starts, while a source-less default-placement team roots each member in its own session and never fabricates a main conversation ID.
  - verify: `TestRootSessionProviderCorrelation_Scenario1_DirectTeamRooting`
- AC1.4: Official gRPC and HTTP run entry ignores a forged `X-Mecatl-Root-Session-ID` and overwrites any pre-populated root context with the authoritative requested session before provider or Jev work; nested internal engines still preserve that authoritative root.
  - verify: `TestRootSessionProviderCorrelation_Scenario1_IngressCannotChooseRoot`, `TestRootSessionProviderCorrelation_Scenario1_IngressCannotChooseRootGRPC`, `TestADR_0360_RootHeaderIsCorrelationOnly`

### Scenario 2 — Providers emit active and root correlation independently

The three real provider modules keep their existing active-session projection as required by
[ADR 0216](../adr/0216-provider-session-correlation-header.md). A composition-owned final
HTTP-client transport decorator adds the root value from request context without coupling those
independently versioned modules to the new engine API. The existing header continues to identify
the active engine; the new header groups the causal conversation.

**Acceptance:**
- AC2.1: Official OpenAI Responses, OpenAI Chat Completions, and Anthropic composition sends `X-Mecatl-Session-ID` with the exact active ID and `X-Mecatl-Root-Session-ID` with the exact root on initial attempts, retries, and provider-specific fallbacks; main and compaction calls carry equal values, while child/auxiliary engine calls carry distinct values where their sessions differ.
  - verify: `TestRootSessionProviderCorrelation_Scenario2_ProviderAttemptsCarryBothIDs`, `TestStreamRecoversInvalidEncryptedContent`
- AC2.2: Concurrent requests sharing one provider client never cross-stamp either active or root IDs, including interleaved retry attempts.
  - verify: `TestRootSessionProviderCorrelation_Scenario2_ConcurrentProviderIsolation`
- AC2.3: A root outside the exact 1–256-byte printable-ASCII/no-boundary-space predicate omits only `X-Mecatl-Root-Session-ID` and does not alter the existing active header or inference result; values are never normalized, encoded, or truncated, and the decorator never mutates shared-client defaults.
  - verify: `TestRootSessionProviderCorrelation_Scenario2_InvalidRootFailsOpen`

### Scenario 3 — Every delegated model-router backend carries the root

The LLM semantic router naturally runs through a nested engine, while Jev uses its direct bounded
HTTP adapter. Both correlation paths identify the same causal conversation without changing
routing candidates, outcomes, usage, breaker behavior, or the Jev credential boundary documented
by [ADR 0352](../adr/0352-jev-delegated-model-router.md).

**Acceptance:**
- AC3.1: An LLM semantic-router provider request carries its ephemeral active router ID in `X-Mecatl-Session-ID` and the invoking conversation ID in `X-Mecatl-Root-Session-ID`.
  - verify: `TestRootSessionProviderCorrelation_Scenario3_LLMRouterCarriesRoot`
- AC3.2: Every Jev classifier HTTP attempt produced by its existing zero-SDK-retry client carries `X-Mecatl-Root-Session-ID` from request context without mutating a shared client; it does not add or fabricate `X-Mecatl-Session-ID` because Jev is not an engine session.
  - verify: `TestRootSessionProviderCorrelation_Scenario3_JevCarriesRoot`
- AC3.3: A root outside the exact outbound predicate causes Jev to omit the root header and continue its existing bounded classification behavior; the header never contains prompts, credentials, routing verdicts, or active ingress metadata.
  - verify: `TestRootSessionProviderCorrelation_Scenario3_JevInvalidRootFailsOpen`

## Out of scope

| Item | Defer-to | Decision |
| --- | --- | --- |
| Reusing the root header for client/server or gateway affinity | Future affinity design | Active `X-Mecatl-Session-ID` remains the sole affinity hint. |
| Collapsing child and parent session IDs | Not planned | Separate aggregates are required for persistence, transcripts, resume, permissions, cancellation, and ledgers. |
| Persisting or emitting root IDs in session snapshots/events | Future observability work | This change is outbound request correlation only. |
| Adding root correlation to MCP, forge, or arbitrary tool traffic | Future outbound-identity design | The approved scope is provider calls inside engine run trees and delegated model routers. |
| Asynchronous title generation, evidence reflection, learning, or other direct provider calls outside an engine run tree | Future auxiliary-call correlation work | Those paths need an explicit authoritative source and lifecycle contract; this change does not fabricate one. |
| New CLI/configuration controls | Not planned | Optional correlation is always derived from authoritative context. |

## Definition of done

1. `task lint`, `task test:race`, `task docs`, and `task api:check` pass on the final candidate.
2. `task ac-trace-strict` resolves every named proof when this plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green and still demonstrates tool use plus permission approval.
4. `engine/api/*.txt` and `engine/CHANGELOG.md` record the additive exported context helpers.
5. Standalone `GOWORK=off` tests pass unchanged for the OpenAI Responses, OpenAI Chat Completions, and Anthropic provider modules; root projection is proved through official composition tests.
6. The implementation PR links the Plan / Interface PR and approved commit and reports interface conformance.
7. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Provider and classifier infrastructure gains another conversation-correlatable metadata field and must apply its existing log-access and retention posture.
- The composition-owned transport decorator must wrap the final inference client without weakening existing redirect refusal, credential injection, retry, or provider-specific fallback behavior.
- Direct source-less teams cannot truthfully claim an external main conversation root; member-self rooting preserves non-empty correlation without inventing lineage.
- Asynchronous direct-provider paths remain explicitly outside this run-tree contract until each has an authoritative source-session and lifecycle decision.
