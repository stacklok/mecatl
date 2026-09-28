# ADR 0370 - Publish live tool-result availability before canonical ordering

- Status: Proposed
- Date: 2026-09-27
- Scope: engine event taxonomy, tool-result presentation timing, read-batch ordering, guardrail release, event persistence, SDK projection, ACP, and Mecatui
- Supersedes: ADR 0363 only for its requirement that every client-visible read-batch result wait for the joined original-order drain; its hold/release authority, serialized asks, canonical ordering, cancellation, recorder, and model-history decisions remain unchanged
- Superseded by: none

## Context

Read-only siblings execute concurrently, but the dispatcher retains every successful result until all siblings finish. It then resolves inbound decisions and emits canonical `tool.result` events in original call order. A fast call therefore looks active while an unrelated slow call runs, even after the fast call has completed PostToolUse processing, repair, and inbound review.

Operators use live tool timing to understand how an agent spends time. Independent card settlement makes the long-running call visible and helps an operator guide later work toward fewer or more efficient calls. Canonical result timing does not provide that signal because it includes delay from unrelated siblings.

Canonical `tool.result` cannot move to completion order. Event-source reconstruction appends tool messages and updates order-sensitive counters from canonical event order, while the live aggregate records the completed result slice in model-call order. Reordering canonical events would let durable reconstruction disagree with the live session.

A display event also cannot expose the raw tool return. PostToolUse mutation, UTF-8 repair, contextual inbound review, and any result-release decision define the effective payload that may cross the client boundary.

## Decision

Add the exported engine event constant:

```go
const EvToolResultAvailable EventType = "tool.result.available"
```

The event carries the existing `Event.ToolResult` payload. The engine emits at most one availability event per call ID when that call first has a complete effective payload that is safe to display. This is a uniform contract across concurrent reads, serial tools, and synthetic error paths. Clients do not inspect or infer dispatcher batching.

Availability follows tool execution, PostToolUse processing, repair, inbound review, and any required result-release answer. A held result emits no availability event until Release once returns the exact held payload or another final disposition produces the existing synthetic safe error. A later clean sibling may become available before or during an earlier sibling's release prompt. Result-release prompts remain serialized.

Treat availability as a transient presentation event. Live engine consumers, the Converse and HTTP/SSE run relays, `StreamSessionLive`, the TypeScript SDK, telemetry, ACP, and Mecatui receive it. Every `RunEventRecorder` path excludes it from `port.EventLog`, including drain-to-discard after a client disconnect. Durable `StreamSessionEvents`, `WatchSessionEvents`, HTTP replay/watch, event-source reconstruction, snapshots, `ToolCallRecorder`, session counters, and model history therefore ignore it. The existing canonical `tool.result` remains the only durable result event.

Keep canonical read-batch behavior unchanged. The dispatcher waits for the complete batch decision set and emits canonical results in original model-call order. Existing cancellation semantics also remain unchanged. Cancellation may replace a previously available success with a canonical synthetic error, in which case no second availability event is emitted. A cancelled call that had no availability event emits one availability event carrying only its synthetic safe error before the matching canonical error. Availability is not a commit acknowledgment.

Clients correlate both events by call ID. Availability settles the existing presentation. An identical canonical result confirms it without creating another card or body. A differing canonical result replaces it. A client that does not recognize availability, misses the transient event, reconnects, or talks to an older server remains correct by consuming canonical `tool.result` alone.

Use availability, rather than canonical batch drain, to end the telemetry span for execution-through-safe-result readiness. Canonical confirmation does not count or finish the tool again. Recorder and product outcome accounting remain on the canonical path.

## Consequences

Concurrent cards settle independently, so their pending durations show which call is the batch's long pole. The duration includes the work required before safe display, including PostToolUse processing, inbound review, and an approval wait.

The event adds a stable public engine and wire identifier. The engine API snapshot, changelog, TypeScript event union, transports, ACP, Mecatui, tracing, and tests must recognize it. Every canonical result producer must pass through an availability-aware seam to satisfy the uniform contract.

The live-only boundary avoids durable duplicate payloads and restart recovery semantics. A reconnect can miss availability and wait for canonical result. That is safe because canonical result implies availability and remains authoritative.

Presentation can temporarily differ from canonical history when cancellation intervenes. Updated clients replace the earlier payload on canonical arrival. The session, recorder, durable reconstruction, and future model view retain the existing cancellation result.

## See also

- [Tool-result availability acceptance plan](../acceptance/tool-result-availability.md)
- [Agent-loop architecture](../architecture/agent-loop.md)
- [API surface](../architecture/api-surface.md)
- [ADR 0363](./0363-contextual-investigative-guardrails.md)
- [Issue #1976](https://github.com/stacklok/mecatl/issues/1976)
