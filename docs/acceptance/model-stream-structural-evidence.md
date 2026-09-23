# Durable structural evidence for model streams — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — introduces a new durable, target-bound evidence contract across provider adapters, the engine loop, EventLog relay, and debugger projection.
**Decision record:** [ADR 0357](../adr/0357-durable-model-stream-structural-evidence.md)
**Phase:** capability
**Status:** proposed, 2026-09-17. Human decisions settled from the Sol recommendation pass; ready for Plan / Interface review.
**Delivery:** Split. The durable evidence boundary and cross-module interfaces need human review before implementation.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1676](https://github.com/stacklok/mecatl/issues/1676).
**Plan PR:** <added when opened>
**Approved baseline:** <absent until approved>

Add bounded, content-free structural evidence for every provider attempt, including successful and incomplete streams. Provider adapters observe only validated wire structure; a provider-neutral run-local observer carries the facts to the agent loop; the loop emits one debugger/EventLog-only event per attempt; the relay persists it; and the target-bound debugger projects it. The ordinary client/event-log surfaces remain unchanged, and streaming remains incremental.

This plan extends the existing failed-attempt evidence boundary in [ADR 0255](../adr/0255-sanitized-network-attempt-evidence.md); it does not diagnose or reproduce issue #1639.

## Human decisions

- [x] Confirm the additive shape on the existing `network.attempt` payload versus a new event — Decision: extend `session.NetworkAttemptPayload`; retain `EvNetworkAttempt`, the existing observer, relay persistence, debugger projection, and ordinary-surface filtering.
- [x] Choose the smallest structural vocabulary — Decision: add `ProviderTerminalObserved *bool` and `StreamOutcome string`; use closed outcomes `complete`, `incomplete`, `stream_error`, `cancelled`, and `unavailable`; omit accepted-chunk, byte, framing, and raw protocol-event counts from v1.
- [x] Define availability semantics and the retained unit — Decision: retain one final row per outer resilience attempt/decision slot; fold adapter-internal repair requests into that slot; leave structural fields absent for pre-stream/suppressed/legacy rows; use explicit `unavailable` only when an entered stream lacks valid structural support; no retained row means “no retained structural evidence; cause indeterminate.”

## Interface contract

- **gRPC / protobuf:** No new public protobuf fields or RPCs. `InspectSession` remains the only model-visible route; ordinary Converse, HTTP/SSE, Team, and public EventLog readback continue to omit the new debugger-only event.
- **Exported Go APIs / interfaces:** Reuse `port.AttemptObserver`; do not add a second observer seam. Extend `session.NetworkAttemptPayload` with `ProviderTerminalObserved *bool` and `StreamOutcome string`. `llmresilience` owns one final summary per outer resilience attempt/decision slot, folding adapter-internal repair requests into that slot. The loop remains the sole event producer and validates/rebinds target, run, turn, and attempt identity before emission.
- **Tool schemas:** `InspectSession` keeps its existing input grammar. Its `network` projection gains the two bounded structural fields and explicit unavailable/incomplete labels without accepting a session ID or provider target from model-controlled arguments.
- **CLI / config:** No new CLI flag, capture directory, wire-capture mode, or plaintext artifact. The existing durable-evidence enablement gate controls installation of the observer; zero/default behavior remains unchanged.
- **Events / persistence:** Extend the existing debugger/EventLog-only `network.attempt` payload with optional `ProviderTerminalObserved *bool` and `StreamOutcome string` fields, persisted through the existing loop → relay → `port.EventLog` path. Retain one row per outer resilience attempt/decision slot, with retries separate; EventLog append order is the durable row identity and the existing run/turn/attempt fields are diagnostic correlation, not a global unique key. Legacy and pre-stream rows retain their ADR 0255 meaning and lack only the new structural extension. Structural outcomes are the closed values `complete`, `incomplete`, `stream_error`, `cancelled`, and `unavailable`; missing provider terminal semantics are never fabricated.
- **Security / authority:** Never retain or project prompts, messages, tools, arguments, results, response text/fragments, raw errors/codes, URLs, headers, cookies, credentials, environment values, provider IDs, or content hashes. Producer-controlled evidence is rejected as a whole when invalid. Instrumentation performs no I/O or response buffering; the observer cannot block chunk delivery. Debugger ownership/incarnation checks and fencing remain authoritative.
- **Compatibility / migration:** Preserve `network.attempt`, old event readers, event-source folding, SDK attachment filters, and generated engine API artifacts. Engine public API changes are additive and require `task api:update`, `engine/CHANGELOG.md`, and `task api:check`. Existing failure fields remain valid; absent structural fields are reported as unavailable rather than making the legacy row disappear or appear healthy.

## In scope — 4 scenarios, in implementation order

### Scenario 1 — Successful stream records structural evidence

A fixture-backed OpenAI Responses, OpenAI Chat Completions, and Anthropic Messages stream produces visible text and a provider-specific semantic terminal marker. The text is never retained in the structural payload. The completed outer resilience attempt produces one validated summary correlated to its logical turn; facts not available at the current adapter seam remain unavailable.

Relevant boundaries are the provider adapters, `engine/port/sessioncontext.go`, `engine/agent/loop.go`, and the existing `network.attempt` lifecycle described in [ADR 0255](../adr/0255-sanitized-network-attempt-evidence.md).

**Acceptance:**
- AC1.1: Each supported in-tree provider fixture emits one structural summary for a successful outer resilience attempt with `StreamOutcome=complete`, `ProviderTerminalObserved=true`, and no content fields.
  - verify: `TestADR_0357_Scenario1_SuccessfulProviderFixtures`
- AC1.2: The first model chunk is observable while the fixture source remains open; structural evidence is finalized at attempt termination, and existing cancellation/establishment-timeout/stream-idle tests remain green without whole-response buffering.
  - verify: `TestADR_0357_Scenario1_PreservesStreaming`

### Scenario 2 — Incomplete, errored, and cancelled streams are honest

Fixture streams truncate before a provider terminal event, return a stream error, and are cancelled. Pre-stream transport/HTTP failures remain represented by the existing resilience decision evidence; this change adds structural fields only when the outer attempt reaches the observation seam. The local outcome is recorded without fabricating normal completion, and facts unavailable at the adapter seam remain unavailable.

This preserves the fail-closed producer boundary in [ADR 0255](../adr/0255-sanitized-network-attempt-evidence.md#decision).

**Acceptance:**
- AC2.1: Truncated, errored, and cancelled attempts produce the approved closed outcomes; `ProviderTerminalObserved=false` is recorded when the adapter can determine that no recognized terminal semantic was accepted, while unavailable support remains distinguishable.
  - verify: `TestADR_0357_Scenario2_IncompleteErrorCancelled`
- AC2.2: Invalid structural vocabulary or scalar values are rejected before persistence and never become a healthy/complete row; existing failed-attempt evidence remains intact.
  - verify: `TestADR_0357_Scenario2_InvalidEvidenceRejected`
- AC2.3: Pre-stream transport/HTTP failures retain the existing failure evidence and do not receive a fabricated structural completion summary.
  - verify: `TestADR_0357_Scenario2_PreStreamFailureEvidence`
- AC2.4: A missing structural field is projected as unavailable, not as a complete, zero-valued healthy fact.
  - verify: `TestADR_0357_Scenario2_UnavailableIsExplicit`

### Scenario 3 — Retries persist separately and survive client loss

A logical turn has a failed/retried outer resilience attempt followed by a successful or terminal attempt. Both remain separately correlated by attempt ordinal while sharing the trusted logical turn identity; EventLog append order, not that diagnostic tuple, is the durable row identity. The relay persists evidence after the client disconnects, and a persistence warning does not fail inference.

The relay-only durability rule follows [AGENTS.md](../../AGENTS.md) and the existing EventLog architecture in [observability](../architecture/observability.md).

**Acceptance:**
- AC3.1: Retry attempts are distinct appended rows with the same target/run/turn correlation and distinct outer-attempt ordinals; no durable uniqueness is inferred from that diagnostic tuple.
  - verify: `TestADR_0357_Scenario3_RetryCorrelation`
- AC3.2: The resilience wrapper emits at most one final structural summary for each outer attempt/decision slot, and adapter-internal repair requests do not create a second durable correlation hierarchy or block chunk delivery.
  - verify: `TestADR_0357_Scenario3_ObservationLifecycle`
- AC3.3: A disconnected client does not prevent the structural event from reaching EventLog, and EventLog failure remains non-fatal to the model run.
  - verify: `TestADR_0357_Scenario3_DurableAfterDisconnect`

### Scenario 4 — Debugger is the sole bounded observation surface

A target-bound debug session inspects structural summaries after a second build/process and receives observation-only wording. Legacy rows retain their existing failed-attempt meaning and show the structural extension as unavailable; no-row causes are not guessed. Every ordinary live/replay/subscription/attachment/event-log-readback/ACP/Team surface omits the debugger-only evidence. Existing bounds, target authorization, incarnation checks, UTF-8 repair, and fencing remain enforced.

The projection must retain the debugger boundary established by [ADR 0254](../adr/0254-session-debugger-admin-transport.md) and [ADR 0257](../adr/0257-session-debugger-hardening.md).

**Acceptance:**
- AC4.1: The debugger projects bounded structural rows only through the already authorized exact target incarnation and reports observations without assigning fault to Mecatl, a gateway, or a model.
  - verify: `TestADR_0357_Scenario4_DebuggerProjection`
- AC4.2: Ordinary gRPC/HTTP/SSE live streams, subscriptions/watch and SDK attachment surfaces, ACP, direct Team streams, and public EventLog readback contain no structural-evidence event.
  - verify: `TestADR_0357_Scenario4_PublicSurfacesOmitEvidence`
- AC4.3: A second build/process can inspect retained evidence only for the exact target incarnation; legacy rows retain their existing evidence and absent structural fields are unavailable rather than inferred healthy.
  - verify: `TestADR_0357_Scenario4_RestartAndAvailability`
- AC4.4: Projection bounds and observation-only wording remain enforced for the additive structural fields; malformed evidence cannot bypass validation or fencing.
  - verify: `TestADR_0357_Scenario4_BoundedProjection`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Raw provider traffic, packet capture, response text/fragments, or response-content hashes | Future support tooling | Explicitly prohibited by issue #1676 and the debugger privacy boundary |
| Causal attribution to a gateway, provider, model, or Mecatl | Future causal-analysis work | Structural evidence reports observations only |
| New CLI capture flags, capture files, or public protobuf/event-stream exposure | Future API decision | Not required; debugger-only projection is the boundary |
| Provider-specific protocol semantics beyond validated structural facts | Future provider diagnostics | Keep the observer provider-neutral and bounded |

## Definition of done

1. Applicable `task lint`, `task test`, `task docs`, and `task api:check` gates pass.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green for runtime changes.
4. The implementation PR links the Plan / Interface PR and approved commit and reports interface conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.
6. All in-tree provider fixtures, durable relay tests, debugger privacy tests, and engine compatibility artifacts are updated.

## Deferred decisions and known risks

- Provider SDK upgrades may expose different terminal semantics. Unsupported facts must remain unavailable rather than inferred.
- `ProviderTerminalObserved` means a recognized provider semantic terminal marker, not necessarily successful completion; adapter-specific mappings must preserve that distinction.
- Consumer abandonment where the iterator stops without a trustworthy cancellation signal remains classified by the existing resilience semantics; this change must not guess `cancelled`.
- Existing public-surface omission predicates and debugger projection paths should be reused; no new event kind or cross-transport matrix is part of this contract.
