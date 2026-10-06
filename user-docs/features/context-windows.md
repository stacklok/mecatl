---
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
provide a context-window resolver. The engine resolves the window for compaction;
server responses report the window separately from the client's context meter.

## Window resolution

Mecatl resolves a window at the point of use, taking the first positive value:

1. the global `--context-window-override`;
2. the operator's exact provider/model entry in `models.context_windows`;
3. retained live metadata for that provider/model; then
4. the matching embedded model catalog entry.

With no positive value, server session entry permits a 128000-token fallback only
when the provider has no model lister or its latest completed non-empty listing
omits the selected model or its window. Otherwise, session entry requests discovery
as eligible before execution. An unattempted provider starts on the first prompt,
including for a cold resumed session. Concurrent demand joins the same attempt.
Native authenticated providers list on demand. Opening the model picker first is
unnecessary.

This admission check covers server prompt entry, failed-step retry, and
restart-restored approval. Direct child, utility, and team engine entry bypass
it and can still use the engine's defensive 128000-token floor. For discovery
retention, retry behavior, and safe recovery from `context_window_unavailable`,
see [Choose models and providers](./choose-models.md#model-context-metadata-is-unavailable).

Compaction uses the resolved window at its next check. The authoritative server
context-window response is 0 when admission is blocked and 128000 when an unknown
window's fallback is permitted. `mecatui`'s context meter uses client-held values;
its existing refresh behavior does not guarantee that every server metadata change,
including a lower window, reaches the footer. A displayed positive value is not
proof that the server currently admits the model. Server resolution and client
freshness are separate.

## Configure an override

Use an override when a provider reports an incorrect limit or a proxy hides the
real model metadata:

```sh
mecated serve --context-window-override 128000
```

The global override wins over exact configuration, retained live metadata, and
the catalog. It controls server-side compaction and the reported context window.
Set it to the limit the provider accepts. For per-model values, use operator-global
[`models.context_windows`](/reference/configuration.md#models), keyed by exact
provider ID and final model ID after alias and slot resolution. See the
[daemon recovery guidance](/building/deployment/mecated.md#context-discovery-recovery)
for applying that configuration.

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
[The agent loop](/building/what-you-get/agent-loop.md). For provider capability
and adapter requirements, see
[LLMProvider](/building/extension-points/llm-provider.md).

## Next steps

- [Choose models and providers](./choose-models.md)
- [Multimodal input](./multimodal-input.md)
- [The agent loop](/building/what-you-get/agent-loop.md)
