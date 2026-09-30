# Tool-result availability - acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural - adds a stable public engine and wire event, defines a live-only persistence boundary, and separates client presentation order from canonical conversation order across the engine, transports, SDK, ACP, and Mecatui.
**Decision record:** [ADR 0370](../adr/0370-live-tool-result-availability.md)
**Phase:** live per-call tool-result availability
**Status:** landed, 2026-09-29. Proposed on the implementation PR; authoritative after merge.
**Delivery:** Split. The public event, persistence exclusion, guardrail release boundary, and multi-client replacement semantics need interface review before implementation.
**Expected tasks:** 3
**Issue:** [stacklok/mecatl#1976](https://github.com/stacklok/mecatl/issues/1976).
**Plan PR:** [stacklok/mecatl#1982](https://github.com/stacklok/mecatl/pull/1982)

A client needs to see when each tool call becomes safe to display so an operator can identify the long-running call in a concurrent batch. Every effective tool result therefore gets a transient `tool.result.available` projection as soon as its own execution, PostToolUse processing, repair, inbound review, and any release decision finish. The canonical `tool.result` remains authoritative and ordered for recorder, conversation, reconstruction, and model use.

The owning current-behavior pages are the contributor [agent-loop architecture](../architecture/agent-loop.md) and the public [embedded agent-loop guide](../../user-docs/building/what-you-get/agent-loop.md). Their event-stream and read-batch descriptions will be corrected in the implementation PR after the behavior lands. The protobuf `Event.type` and `Event.tool_result` source comments will own the exact wire reference and regenerate the gRPC schema.

## Human decisions

- [x] Classify the change as Architectural - Decision: the stable engine event, wire behavior, persistence boundary, and cross-client ordering contract require a new ADR and Split Plan / Interface review.
- [x] Use a uniform availability event - Decision: every tool-result path emits `tool.result.available`, including serial tools and synthetic errors, rather than exposing internal batch classification to clients.
- [x] Emit only safe effective payloads - Decision: availability follows PostToolUse, repair, inbound guardrail review, and any result-release decision. A held result remains private until release or replacement with a synthetic error.
- [x] Let independent siblings appear in completion order - Decision: a clean later sibling may become available before or during an earlier sibling's release prompt. Result-release prompts remain serialized and comprehensible.
- [x] Keep availability live-only - Decision: the event reaches live engine, gRPC/SSE, SDK, ACP, telemetry, and Mecatui consumers but is excluded from durable event logs, event-source reconstruction, snapshots, recorder output, counters, and model history.
- [x] Preserve canonical ordering and cancellation - Decision: canonical `tool.result` events remain behind the complete read-batch decision barrier and drain in original model-call order. Existing cancellation may replace a previously available success with a canonical synthetic error.
- [x] Make canonical results authoritative - Decision: clients correlate by call ID, create no second card, treat an identical canonical result as confirmation, and replace an earlier available payload when the canonical payload differs.
- [x] Use availability for latency intuition - Decision: a card remains pending until its own availability event. Concurrent cards settle independently so the operator can identify which execution, post-processing, guardrail review, or approval wait is the batch's long pole.

## Interface contract

- **gRPC / protobuf:** Add wire event type string `tool.result.available` using the existing `Event.type` and existing `Event.tool_result` payload arm. No protobuf message, field, or field number changes. Update those source comments to distinguish transient availability from canonical result, then run `task generate` to refresh the gRPC reference. The live Converse relay, live HTTP/SSE run stream, and `StreamSessionLive` relay the event. Durable `StreamSessionEvents`, `WatchSessionEvents`, and the HTTP replay/watch surface omit it because their source is the filtered event log.
- **Exported Go APIs / interfaces:** Add `session.EvToolResultAvailable EventType = "tool.result.available"`. `session.Event.ToolResult` applies to `EvToolResultAvailable` and `EvToolResult`. No new result type, port method, or recorder interface is introduced. Update the engine API snapshot and classify the constant as Added (minor) in `engine/CHANGELOG.md`.
- **Tool schemas:** None - tool names, input schemas, output schemas, and tool execution interfaces do not change.
- **CLI / config:** None - availability is unconditional engine behavior with no flag, setting, mode, or precedence rule.
- **Events / persistence:** Emit at most one live `tool.result.available` per call ID when that call first has a safe display payload. Ordinarily its complete `ToolResult`, including content, error status, structured content, and typed parts, equals the later canonical result. A later run cancellation may make the canonical result a synthetic error, which replaces the earlier display payload. The availability event does not call `ToolCallRecorder`, mutate `Session`, update counters, enter `port.EventLog`, participate in event-source folding, or reach model history. Canonical `tool.result` retains those contracts and original-call ordering. PostToolUse and inbound-review events needed to explain availability precede the matching availability event; held PostToolUse annotation suppression remains unchanged.
- **Security / authority:** Availability creates no new release authority. It occurs only after the result's effective inbound disposition is final for display. Held bytes remain absent before Release once; deny, unattended enforcement, stale binding, and cancelled release produce only the existing synthetic safe payload. Independently clean results may become available around another call's serialized ask without approving, denying, or exposing that held call.
- **Compatibility / migration:** Existing clients ignore the additive event and continue settling on canonical `tool.result`. Updated Mecatui and ACP clients settle the existing call-ID-correlated card on availability, then handle canonical result as an idempotent confirmation or authoritative replacement. The TypeScript SDK adds the event kind using its existing tool-result payload type. No stored-data migration is required because availability is never durable.

## In scope - 3 scenarios, in implementation order

### Scenario 1 - each card exposes the batch's long pole

Read-only siblings continue to execute concurrently under the [agent-loop dispatch contract](../architecture/agent-loop.md#read-parallel--mutate-serial-dispatch-dispatchgo). Availability separates safe client presentation from the canonical result order used by the session aggregate.

**Acceptance:**

- AC1.1: when two read-only siblings execute concurrently, the fast sibling emits `tool.result.available` and settles its existing card while the slow sibling remains blocked and pending; no sleep-based proof is used.
  - verify: `TestADR_0370_Scenario1_FastSiblingAvailableBeforeSlowSibling`
- AC1.2: every completed tool-result path, including serial execution, read batches, permission or preparation errors, and synthetic errors, emits at most one availability event for its call ID before that call's canonical result; clients never need to know the dispatch classification.
  - verify: `TestADR_0370_Scenario1_UniformAvailability`
- AC1.3: canonical results remain behind the complete read-batch decision barrier and emit in original model-call order, while availability events may emit in completion order and do not advance session history or counters.
  - verify: `TestADR_0370_Scenario1_PresentationAndCanonicalOrdering`
- AC1.4: the telemetry tool span ends when the engine observes availability, measuring per-call execution-through-safe-result readiness rather than the unrelated canonical batch drain; canonical confirmation neither ends nor counts the execution again.
  - verify: `TestADR_0370_Scenario1_AvailabilityLatency`
- AC1.5: simultaneous result completions publish exactly one availability event per call with unique monotonic sequence numbers whose order matches channel observation, without concurrent event emission races.
  - verify: `TestADR_0370_Scenario1_SerializedAvailabilityPublication`

### Scenario 2 - guardrails release only safe display payloads

The event preserves the inbound hold boundary from [ADR 0363](../adr/0363-contextual-investigative-guardrails.md) while superseding its requirement that all client-visible results wait for the ordered canonical drain.

**Acceptance:**

- AC2.1: availability follows tool execution, PostToolUse, UTF-8 and effective-payload repair, principal-revision validation, inbound review, and the final per-result release decision; no original held byte appears in an event, recorder, client, or model before Release once.
  - verify: `TestADR_0370_Scenario2_AvailabilityAfterEffectiveRelease`
- AC2.2: a later independently clean sibling becomes available before or while an earlier sibling waits on a result-release prompt; two held siblings still surface at most one prompt at a time in original held-call order.
  - verify: `TestADR_0370_Scenario2_CleanSiblingBypassesHeldPresentation`
- AC2.3: Release once emits the exact held payload once; denial emits only the synthetic withholding payload; neither path reruns the tool, hooks, reviewer, permission learning, or side effects.
  - verify: `TestADR_0370_Scenario2_ExactReleasedAvailability`
- AC2.4: run cancellation retains the existing canonical closure behavior. If a call already emitted a successful availability event, a later canonical synthetic error replaces it without a second availability event. If a held or other call had emitted no availability event, cancellation destroys any held bytes and emits one availability event carrying only its synthetic safe error before the matching canonical error.
  - verify: `TestADR_0370_Scenario2_CanonicalCancellationReplacement`

### Scenario 3 - live clients update one call without changing durable history

Availability uses the existing tool-result payload and call ID across the [API surface](../architecture/api-surface.md). Canonical results remain the only durable conversation events.

**Acceptance:**

- AC3.1: the live Converse relay, live HTTP/SSE run stream, and `StreamSessionLive` deliver `tool.result.available` with the existing complete tool-result projection. Every `RunEventRecorder` path, including gRPC and HTTP drain-to-discard after client disconnect, excludes it from the event log; `StreamSessionEvents`, `WatchSessionEvents`, HTTP replay/watch, event-source folding, snapshots, recorder output, counters, and model history therefore omit it.
  - verify: `TestADR_0370_Scenario3_LiveOnlyProjection`
- AC3.2: event-source reconstruction from completion-order availability plus call-order canonical events equals reconstruction from canonical events alone and counts every tool call exactly once.
  - verify: `TestADR_0370_Scenario3_CanonicalReconstructionOnly`
- AC3.3: Mecatui and ACP correlate availability and canonical result by call ID and keep one card/update lifecycle. Identical canonical confirmation leaves the displayed payload unchanged but may advance an internal card revision or invalidate a render cache; a differing canonical result, including cancellation, replaces the displayed payload.
  - verify: `TestADR_0370_Scenario3_ClientConfirmationAndReplacement`
- AC3.4: the TypeScript SDK recognizes `tool.result.available` as the existing tool-result payload type; old clients and streams where availability is missed remain correct because canonical `tool.result` implies availability.
  - verify: `TestADR_0370_Scenario3_SDKCompatibility`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Canonical result emission from completed original-order prefixes | Later optimization | Availability provides live latency without changing canonical batch commitment. |
| Durable availability replay or recovery after reconnect/process loss | Separate persistence design | Canonical `tool.result` remains the sole durable and reconstructive result event. |
| Tool-result chunk streaming | Separate streaming contract | Availability carries one complete effective result. |
| Changes to guardrail approval authority or held-result storage | Existing contextual-guardrail contract | This plan changes presentation timing only after the existing release decision. |
| Explicit duration fields or historical batch-performance reports | Observability follow-up | Live card timing and tracing expose the current long pole without adding persisted measurements. |

## Definition of done

1. Focused engine, event-source, server, SDK, ACP, and Mecatui tests prove every numbered acceptance criterion with offline, test-owned fixtures.
2. `task api:update` updates the engine snapshot, `task generate` refreshes the protobuf-derived reference after source-comment changes, and `engine/CHANGELOG.md` classifies the exported event constant as Added (minor).
3. The public embedded agent-loop guide documents availability as the live per-call presentation signal and canonical result as the model/history signal without duplicating the exact wire reference.
4. Applicable `task lint`, `task test:race`, `task docs`, `task api:check`, and `task site:build` gates pass on the final candidate.
5. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
6. `go run ./cmd/mecademo` remains green and demonstrates tool call, permission approval, availability, and canonical result behavior.
7. The agent-loop architecture replaces the stale mutex description with the implemented private-record, availability, serialized-release, and ordered-canonical behavior.
8. The implementation PR links the Plan / Interface PR and approved commit and reports interface conformance.
9. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- A uniform event requires every canonical result path to use one availability-aware emission seam. Missing a direct error path would violate the client contract, so parity tests must enumerate result producers rather than rely on source-text checks.
- Availability is deliberately not a commit acknowledgment. Cancellation can replace its payload canonically, and clients must render that transition without creating a duplicate card.
- Live-only filtering must be explicit at the durable relay. Persisting the payload accidentally would create a second reconstructive-looking result and expand restart semantics outside this plan.
