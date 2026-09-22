---
slug: /features/context-windows
sidebar_position: 130
title: Context windows
description:
  Control how Mecatl resolves model context windows and compacts long sessions.
---

# Context windows

Mecatl compacts stored history when a request approaches the selected model's
context window. You can also request a compaction before the next turn.

## Availability

Context-window resolution and automatic compaction are available in `mecated`,
`mecak8s`, `mecatequi`, `mecatui`'s embedded server, and engine embeddings that
provide a context-window resolver. The same resolved value is used by the engine
and, where applicable, the `mecatui` context meter.

## Window resolution

Mecatl resolves a window at the point of use, in this order:

1. the explicit `--context-window-override`;
2. live provider/model metadata;
3. the embedded model catalog; and
4. a 128K-token floor for an otherwise unknown model.

Mecatl resolves the value when needed, so the next compaction check can use
newer metadata from a catalog refresh. `mecatui` updates its context meter when
that metadata arrives.

For a live-listable model that has no configured, live, or embedded window yet,
Mecatl waits for the bounded initial discovery before admitting a prompt, a
failed-step retry, or a restart-restored approval. This prevents the 128K
unknown-model floor from compacting a durable session before a larger gateway
window arrives. If discovery is unreachable, unauthorized, or returns an empty
inventory, admission returns `context_window_unavailable` (HTTP 503 / gRPC
`Unavailable`) without recording the prompt or starting inference. Restore model
discovery or configure an exact `models.context_windows` value for the final
provider/model ID, then retry. The first rejection does not make a duplicate
startup request; a later retry performs one bounded refresh.

After a successful non-empty listing, a passthrough model omitted from that
listing—or listed without a window—retains the settled 128K compatibility
fallback. Providers with no live model lister also retain that fallback.

## Configure an override

Use an override when a provider reports an incorrect limit or a proxy hides the
real model metadata:

```sh
mecated serve --context-window-override 128000
```

The override wins over live and catalog metadata and controls both compaction
and the `mecatui` context meter. Set it to the limit the provider accepts.

The equivalent server flag is available to `mecatui`'s embedded server. It does
not reconfigure a server used through `mecatui connect`; the connected server's
resolved model and window are authoritative.

## Automatic compaction

By default, compaction starts when the estimated complete model request reaches
80% of the resolved window. The estimate includes the rendered system prompt,
ephemeral project and memory instructions, conversation messages, typed tool
results, and advertised tool schemas. Only persisted conversation history can be
compacted. System instructions, ephemeral fragments, and tool definitions are
fixed overhead, so a large fixed prompt can still leave little room after a
pass. Choose the strategy with `--compaction`:

- `heuristic` (default) preserves the goal, recently touched paths, and recent
  messages while truncating large tool bodies;
- `cascade` tries cheaper reductions first: snip, strip tool bodies, collapse
  large file contents, and then summarize. It uses separate trigger and target
  thresholds to avoid compacting repeatedly at the boundary.

The token estimate is selected with `--tokenizer`:

- `heuristic` is dependency-free and is the default;
- `tiktoken` uses the offline tokenizer vocabulary.

The tokenizer controls measurement. The compaction strategy controls how Mecatl
reduces history.

Both compactors preserve a usable conversation:

- the first genuine user instruction remains pinned;
- recent user instructions survive verbatim instead of falling into the summary;
- the kept tail never begins with an orphaned tool result;
- tool-call/result pairing is validated after compaction; and
- if the candidate history is invalid, the original history is retained and the
  run continues uncompacted.

A successful compaction emits a compaction event and archives the pre-compaction
conversation in the durable event log when one is configured. The archive lets
operators reconstruct earlier context even though the active session history is
shorter.

## Compact manually in `mecatui`

When the server advertises manual compaction, enter `/compact` while the session
is idle. The command runs one pass without waiting for the 80% trigger. It does
not send a prompt or start a chat turn, and the TUI keeps your visible
scrollback. A notice says whether model history changed or was already compact.

The configured strategy still applies. A cascade pass that reaches its summary
tier can make a model call and consume tokens. Active runs and pending approvals
must finish first. API clients can use gRPC `CompactSession` or bodyless
`POST /v1/sessions/{id}/compact`; see the [gRPC](/reference/grpc-api.md) and
[HTTP](/reference/http-sse-api.md) operator references for state, ownership,
lease, and response details.

## Context and cost limits

Context-window compaction is distinct from the cumulative run token budget.
`--max-run-tokens` ends a run at a turn boundary after accumulated input and
output tokens cross the limit; it never interrupts an in-flight stream. Cache
read tokens are excluded from that budget. Compaction instead keeps the next
provider request within the model's context capacity and can occur repeatedly
through a long session.

Internal compaction summary calls can use the `compaction` model slot when model
routing is configured. That changes only the summary call; the session's own
provider, model, token counter, and context-window resolution remain unchanged.

## Limitations

- A context window is model/provider-specific. A value from one endpoint cannot
  safely be assumed for another compatible endpoint.
- The 128K fallback is a bounded safety floor for unknown metadata, not a claim
  that every model accepts 128K tokens. Use an override when the deployment
  knows the actual limit.
- Compaction is lossy by design for old tool bodies and superseded history. The
  recent user goal and valid tool pairing are protected, but every historical
  message is not guaranteed to remain verbatim in the active conversation.
- A nil context-window resolver in an engine embedding disables automatic
  compaction. Embedders should provide one when their provider has a bounded
  context window.

For the loop's preservation guarantees and terminal behavior, see
[The agent loop](/features/sessions/agent-loop.md). For provider capability and
adapter requirements, see
[LLMProvider](/building/extension-points/llm-provider.md).

## Next steps

- [Choose models and providers](/features/sessions/choose-models.md)
- [Multimodal input](/features/sessions/multimodal-input.md)
- [The agent loop](/features/sessions/agent-loop.md)
