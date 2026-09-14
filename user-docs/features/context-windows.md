---
sidebar_position: 130
title: Context windows
description:
  Control how Mecatl resolves model context windows and compacts long sessions.
---

# Context windows

A model's context window is the maximum token budget for the complete request:
current instructions, conversation history, and tool definitions. Mecatl checks
that request before each turn and compacts stored history when needed. You can
also request one manual pass before the next turn.

## Availability

Context-window resolution and automatic compaction are available in `mecated`,
`mecak8s`, `mecatequi`, mecatui's embedded server, and engine embeddings that
provide a context-window resolver. The same resolved value is used by the engine
and, where applicable, the mecatui context meter.

## Window resolution

Mecatl resolves a window at the point of use, in this order:

1. the explicit `--context-window-override`;
2. live provider/model metadata;
3. the embedded model catalog; and
4. a 128K-token floor for an otherwise unknown model.

Mecatl resolves the value when it is needed instead of taking a startup
snapshot. If a catalog refresh discovers a better model window while a session
is running, the next compaction check uses it. The engine always uses a positive
floor. `mecatui` may briefly show an unresolved context denominator during the
initial refresh and updates the meter when metadata arrives.

## Configure an override

Use an override when a provider reports an incorrect limit or a proxy hides the
real model metadata:

```sh
mecated serve --context-window-override 128000
```

The override is a token count and wins over live and catalog metadata. It moves
both the compaction trigger and the mecatui context-meter denominator together.
It is an operator workaround, not a way to increase a model beyond the limit the
provider actually accepts. A very small value causes frequent compaction and is
useful mainly for stress-testing.

The equivalent server flag is available to mecatui's embedded server. It does
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

These choices are independent: the tokenizer changes measurement, while the
compaction strategy changes how history is reduced.

Both compactors preserve the conversation's usable shape:

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

## Compact manually in mecatui

When the server advertises manual compaction, enter `/compact` while the session
is idle. The command runs one pass without waiting for the 80% trigger. It does
not send a prompt or start a chat turn, and the TUI keeps your visible
scrollback. A notice says whether model history changed or was already compact.

The configured strategy still applies. A cascade pass that reaches its summary
tier can make a compaction-model call, so the operation may cost tokens even
though it creates no chat turn. Active runs and pending approvals must finish
first. Older servers hide the command. API clients can use gRPC `CompactSession`
or bodyless `POST /v1/sessions/{id}/compact`; see the
[gRPC](/reference/grpc-api.md) and [HTTP](/reference/http-sse-api.md) operator
references for state, ownership, lease, and response details.

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
- A provider can still reject a request for reasons unrelated to context size;
  changing the override does not repair invalid credentials, policy blocks, or
  unsupported content.
- Live metadata refresh is process-local. A restart rebuilds the catalog and
  resolver from deployment configuration and available provider metadata.
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
- [Capability and deployment matrix](./capability-matrix.md)
