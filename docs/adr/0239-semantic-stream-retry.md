# ADR 0239 - Semantic stream retry and failed-step retry transport

- Status: Accepted
- Date: 2026-08-27
- Scope: provider stream semantics, retry classification, durable failure metadata, failed-step retry APIs, and mecatui recovery
- Supersedes: ADR 0203 for retry classification and client retry behavior; its compatibility fields remain supported

## Context

ADR 0203 added a permanent-error bit. That answered whether an identical request was known to fail again, but it could not distinguish an unclassified failure from a retryable one or say whether any model output had become visible. Treating the first raw stream chunk as the retry boundary was also too early. Providers commonly emit reasoning, replay state, phase, route, usage, whitespace, or a partial tool call before useful assistant text. None of those facts alone means the attempt is safe to expose or unsafe to replay.

Clients also lacked a prompt-free way to repeat the failed model step. Resending the last prompt would add another user message, rerun prompt hooks and boundary injections, and could duplicate tool effects. Automatic retry therefore needed both a semantic commit boundary and an aggregate-owned, durable replay intent.

## Decision

Use four independent facts instead of one overloaded transient/permanent decision:

1. **Retry disposition** is `unknown`, `retryable`, or `permanent`. It describes the cause only.
2. **Stream progress** is `unknown`, `precommit`, `visible`, or `complete`. It describes what escaped the semantic buffer.
3. **Retry policy** decides whether another attempt is allowed by attempt limits, classifier vetoes, provider-internal retry limits, and cancellation.
4. **Breaker health** decides whether the provider may accept an attempt. Only transient provider-health failures affect it. Permanent and caller-cancelled failures are breaker-neutral.

The resilience adapter buffers tentative chunks in wire order. Leading whitespace, display reasoning, opaque reasoning replay, phase, route, usage, and tool calls remain tentative. The first text delta that makes cumulative text meaningful commits the attempt and flushes the buffer. A clean `ChunkDone` also flushes the buffer, including whitespace-only and tool-only turns. Only a retryable failure while progress is still `precommit` may be discarded and retried transparently. Once output is `visible`, replay is suppressed, but visibility is not provider success: circuit-breaker success is recorded only after clean stream completion. A visible transient failure remains terminal and counts against breaker health; permanent and caller-cancelled failures remain breaker-neutral.

Carry retry disposition and stream progress through the typed error interfaces, terminal result event, durable event log, session snapshot, and optional protobuf fields. Keep `Result.permanent` and the aggregate permanence methods as compatibility projections of `disposition == permanent`. Protobuf presence distinguishes an old server that did not send typed facts from a new server that explicitly reports `unknown`.

Make event-sourced reconstruction match snapshots. A failed incomplete stream does not contribute its partial assistant message to reconstructed history. A clean `ChunkDone`, including a text-bearing `StopError`, keeps the completed assistant turn and lands in `StateCompleted`, matching the live aggregate.

Add explicit prompt-free retry entry points:

- the first gRPC `Converse` frame may be `RetryStart{session_id}`;
- HTTP exposes `POST /v1/sessions/{id}/retry` with no body and an SSE response.

Failed-step retry is eligible only for a typed `retryable` failure with `precommit` or `visible` progress. The session aggregate consumes that failure into persisted retry intent before the run starts. Ordinary prompts are blocked while the intent is pending. The retry run adds no user message, skips prompt and run-start hooks, and skips first-iteration boundary injections. It reuses persisted conversation and tool state, but the request builder re-resolves live turn-0 instructions, operator profile, and system-prompt sources; byte identity across restart is neither promised nor achieved. Later turns use the normal loop.

`model.retry` carries a structured disposition/progress payload. Event-source folding treats it as a new prompt-free run segment: before `turn.start` the aggregate reconstructs idle+pending; after `turn.start` and before a terminal result it reconstructs running+pending and discards partial retry deltas from conversation history. A clean pre-turn brake such as the cumulative token budget emits a terminal Run result but defers the retry, leaving the aggregate idle+pending. Cancellation explicitly abandons and clears retry intent.

Mecatui automatically starts one failed-step retry only when both typed fields are present and equal `retryable` plus `precommit`. Manual `/retry` is available for every bound idle session and delegates eligibility to the server. Queued prompts remain paused until `turn.start` proves an authoritative model attempt; transport rejection or a clean pre-turn brake preserves the queue and manual affordance.

Emit one structured diagnostic for every failed attempt decision. Include attempt number, limit, elapsed time, disposition, progress, retry or terminal decision, suppression reason or backoff, and session/model correlation. Provider adapters may add validated HTTP or in-band status, a bounded provider code, and one bounded correlation ID. Never log raw error bodies, prompts, headers, or credentials. ToolHive uses the OpenAI Responses adapter and can report only metadata the gateway and that adapter expose. Mecatl does not infer missing gateway details.

## Consequences

- Retries follow semantic visibility rather than raw transport activity, so reasoning-first and tool-first streams can recover without leaking a discarded attempt.
- Clients can distinguish retryable, permanent, unknown, precommit, visible, and complete outcomes without parsing error text.
- Failed-step retry preserves conversation/tool state and the non-duplication of the user prompt; live instruction and system-prompt inputs are deliberately re-resolved.
- Tentative reasoning is displayed later because it remains buffered until meaningful text or clean completion.
- Mecatui's automatic behavior is deliberately bounded to one typed precommit retry. Visible and ambiguous failures remain operator decisions.
- The tentative buffer remains unbounded for one model attempt, matching the existing stream assembly posture. Add a separate bound if observed provider behavior makes that necessary.
- A clean text-bearing `StopError` remains `StateCompleted`. The stop is an honest completed provider outcome, not an interrupted iterator failure.
- ADR 0203's permanent boolean and methods remain for source and wire compatibility, but new code should use the typed disposition and progress facts.

## See also

- [ADR 0203](./0203-permanent-provider-error-signal.md)
- [Architecture](../architecture.md)
- [Implementation notes](../design/IMPLEMENTATION-NOTES.md)
- [gRPC API](../usage/grpc-api.md)
- [HTTP/SSE API](../usage/http-sse-api.md)
- [TUI behavior](../tui.md)
