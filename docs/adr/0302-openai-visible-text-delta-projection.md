# ADR 0302 — OpenAI Responses visible text delta projection

- Status: Accepted
- Date: 2026-09-06
- Scope: `response.output_text.delta` projection in the OpenAI Responses SSE adapter
- Supersedes: only the **Single visible text part** subsection of ADR 0017 §2

## Context

ADR 0017 deliberately treated more than one visible text-part identity in a Responses
stream as an unsupported shape: it pinned the first `(item_id, output_index,
content_index)` and raised an error for a later identity. The provider emits a serial
SSE stream, however, and the engine's provider-neutral representation has exactly one
visible text field, `session.Message.Text`. A valid response with multiple non-empty
visible text deltas should not fail merely because its deltas belong to distinct
provider items or content parts.

The correction must not widen the domain model or alter semantic retry. In particular,
ADR 0239 commits an attempt at the first meaningful visible text and suppresses replay
if a later retryable error occurs. The adapter's opaque phase/reasoning/function-call
replay contracts are independent of visible text-part identity.

## Decision

Project every non-empty `response.output_text.delta` to `ChunkText` in SSE arrival
order, regardless of its `(item_id, output_index, content_index)` identity. The engine
concatenates those chunks into the one `session.Message.Text` value without inserting a
separator.

Discard provider item, output, and content identities at that adapter boundary. Do not
add text-part metadata to `session.Message`, `port.LLMRequest`, or the public API.

Keep phase capture and replay, reasoning-item buffering and replay, function-call
assembly and replay, and ADR 0239 semantic stream retry unchanged. A retryable failure
after the first meaningful visible delta remains terminal, is not replayed, and does
not persist the incomplete assistant text.

## Consequences

- Valid multipart visible-text streams complete as one ordered assistant message.
- The provider-neutral model intentionally cannot reconstruct source text-part
  boundaries, annotations, or identities; a future need for that fidelity requires a
  separate domain/API decision.
- Tests must cover both normal multipart completion and a multipart stream that fails
  after visible output through the adapter, engine, and resilience layers, so the
  projection cannot accidentally weaken ADR 0239's commit boundary.

## See also

- [ADR 0017 — OpenAI Responses API adapter](./0017-openai-responses-api.md)
- [ADR 0239 — Semantic stream retry and failed-step retry transport](./0239-semantic-stream-retry.md)
- [OpenAI provider architecture](../architecture/providers.md)
- [OpenAI Responses multiple visible text parts acceptance plan](../acceptance/openai-multiple-text-parts.md)
- [ADR 0002 — Documentation lifecycle](./0002-documentation-lifecycle.md)
