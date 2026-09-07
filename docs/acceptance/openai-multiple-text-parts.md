# OpenAI Responses multiple visible text parts — acceptance plan

**Phase:** bug fix — ordered Responses stream translation
**Status:** landed, 2026-09-06. Derived from [stacklok/mecatl#738](https://github.com/stacklok/mecatl/issues/738).
**Issue:** [stacklok/mecatl#738](https://github.com/stacklok/mecatl/issues/738).
**Accumulator branch:** `acc/openai-multiple-text-parts` (off `main`).

The smallest set of work that lets a valid OpenAI Responses turn containing visible text deltas from more than one `(item_id, output_index, content_index)` identity complete as one ordered assistant message. The adapter emits each delta through the existing `ChunkText` path; the existing loop assembles `Message.Text` by concatenation with no inserted separator.

This correction supersedes only ADR 0017's former *Single visible text part* subsection through [ADR 0302](../adr/0302-openai-visible-text-delta-projection.md). It does not create a text-part boundary in `Message.Text` or widen `port.LLMRequest`; provider item, output, and content identities are deliberately discarded at the adapter boundary.

## In scope — 1 scenario, in implementation order

### Scenario 1 — a Responses turn preserves every visible text delta

A recorded OpenAI Responses SSE turn emits visible text deltas for distinct message items or content parts, including a stream that interleaves phase, reasoning, and function-call events. The OpenAI adapter preserves serial event order at the existing `ChunkText` seam, and the running engine exposes one assistant `Message.Text` made by direct, separator-free concatenation. [ADR 0302](../adr/0302-openai-visible-text-delta-projection.md) narrows ADR 0017's former single-visible-text-part policy to that projection only: item, output, and content identities are intentionally discarded. This follows [`architecture.md § Semantic stream retry`](../architecture.md#semantic-stream-retry) and [ADR 0239](../adr/0239-semantic-stream-retry.md): the first meaningful visible text commits the semantic stream, while tentative chunks retain wire order and an error after visibility is terminal rather than replayed. The provider-neutral `LLMProvider` boundary and stateless replay rule remain as specified by [`AGENTS.md` — provider neutrality](../../AGENTS.md).

**Acceptance:**

- AC1.1: A completed Responses stream whose non-empty `response.output_text.delta` events use distinct item, output, or content identities succeeds rather than reporting a multi-part-text error; it emits every delta in provider stream order through `ChunkText`.
  - verify: `TestADR_0302_DistinctIdentitiesEmitOrderedChunkText`
- AC1.2: The completed assistant turn contains the exact ordered concatenation of all visible text deltas, with no synthetic spaces, newlines, delimiters, dropped bytes, or text-part metadata exposed through `Message.Text`.
  - verify: `TestADR_0302_EngineConcatenatesDistinctPartDeltasWithoutSeparators`
- AC1.3: A multi-text-part turn retains existing independent behavior for opaque message phase, buffered reasoning replay items, function calls, usage, and terminal stop; interleaving those events does not reorder, duplicate, or turn visible text into reasoning/tool output.
  - verify: `TestADR_0302_InterleavedPhaseReasoningAndToolCallPreserveChunkSemantics`
- AC1.4: A distinct-multipart Responses stream that emits two meaningful visible deltas and then fails retryably is exercised through the OpenAI adapter, engine, and resilience wrapper: both deltas are observed in SSE arrival order, the visible commit suppresses a second provider attempt, the terminal result retains the retryable/visible failure semantics, and the incomplete assistant text is not persisted. A failure before visible text remains eligible for the existing precommit retry policy.
  - verify: `TestADR_0302_VisibleMultipartFailureIsTerminalWithoutReplayOrPersistence`

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Representing visible text-part boundaries, annotations, or identities in the domain message or public API | Separate product/API decision | Existing one-string `Message.Text` contract; no boundary is needed for ordered visible text. |
| Changes to OpenAI request construction, stateless assistant replay, phase/reasoning encoding, tool-call protocol, or retry policy | Not part of #738 | [ADR 0239](../adr/0239-semantic-stream-retry.md) keeps semantic retry and replay behavior unchanged. |
| Behavioral changes to Anthropic, OpenAI Chat Completions, or non-OpenAI-compatible adapters | Separate provider regression | [`architecture.md` provider-adapter boundary](../architecture.md). |

## Definition of done

1. The named Scenario 1 tests pass, including the recorded distinct-message-item regression fixture, the engine-level ordered-concatenation assertion, and the adapter+engine+resilience visible-failure proof.
2. `docs/architecture/providers.md` documents [ADR 0302](../adr/0302-openai-visible-text-delta-projection.md)'s ordered multipart `ChunkText` projection and the intentional loss of text-part identities without changing the phase, reasoning, function-call, or ADR 0239 retry contracts.
3. `cd provider/openai && GOWORK=off go test ./...` passes for the independently importable OpenAI provider module.
4. `task lint` and `task test` pass.
5. `go run ./cmd/mecademo` still prints a full offline session.
6. `task ac-trace-strict` passes once this plan is `landed` and every named proof resolves.
7. `task docs` passes after the acceptance index, ADR index, ADR, plan, and provider architecture reference are linked.

## Deferred decisions and known risks

- **Narrow ADR supersession.** [ADR 0302](../adr/0302-openai-visible-text-delta-projection.md) supersedes only ADR 0017 §2's single-visible-text-part policy. It preserves the one-string domain representation and ADR 0239's ordered semantic-stream behavior; a future need to preserve part boundaries would be a separate, costly API decision.
- **Provider ordering is authoritative.** The regression fixtures must use the emitted SSE order; this fix must not infer a different order from item/output/content indexes.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this plan is satisfied.
