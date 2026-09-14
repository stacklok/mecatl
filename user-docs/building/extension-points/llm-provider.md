---
sidebar_position: 2
title: LLMProvider
description:
  Implement an LLMProvider to connect the Mecatl agent loop to a model backend.
---

# LLMProvider

`port.LLMProvider` connects the agent loop to an LLM backend. The loop consumes
this interface and assembles its stream into domain types without importing a
provider SDK. Implement it to connect another model service, a local inference
server, or a test double.

---

## The interface

```go
// engine/port/llm.go

type LLMProvider interface {
    Stream(ctx context.Context, req LLMRequest) (iter.Seq2[Chunk, error], error)
    Capabilities() ProviderCapabilities
}
```

`Stream` starts a model call and returns an `iter.Seq2[Chunk, error]`. Return a
startup failure as the outer error and an in-stream failure from the iterator.
Error position alone does not determine whether retrying is safe; report retry
disposition through the typed error contracts described below.

Stop the model request and iterator when the context is canceled.

`port.SessionIDFromContext` returns the active session ID for optional provider
correlation. The supplied HTTP adapters send legal IDs as `X-Mecatl-Session-ID`
and omit invalid values. The header is not authentication, tracing, idempotency,
provider state, user identity, or a cache key.

`Capabilities` reports accepted non-text content. Mecatl enables a modality only
when both the model and adapter advertise it. A decorator must forward the
wrapped provider's capabilities.

---

## LLMRequest and response types

```go
type LLMRequest struct {
    System   prompt.Layered      // two-layer system prompt
    Messages []session.Message   // full conversation history
    Tools    []tool.ToolSpec     // tool schemas
    Model    string              // opaque provider model identifier
}
```

### Provider-neutral discipline

`LLMRequest` is provider-neutral. `Model` is an opaque identifier; API keys,
base URLs, and endpoints belong to adapter construction. The loop does not
branch on provider identity.

Configure provider-specific behavior through adapter options, such as
`openai.WithReasoningEffort("high")`, instead of adding it to `LLMRequest`.

### The stateless contract

Mecatl sends the full conversation on every turn and does not rely on
provider-side conversation state:

1. `System` uses `prompt.Layered` to separate a byte-stable prefix from per-turn
   context for prompt caching.
1. `Messages` contains recorded text, tool calls, and tool results. Compaction
   trims this history near the context limit.

### Reasoning and phase markers

Two fields on `session.Message` carry provider-private blobs that ride the
stateless replay without being interpreted:

- **`Message.Reasoning`** — an opaque replay blob (OpenAI
  `reasoning_item.encrypted_content`, or Anthropic's `(thinking, signature)`
  pair). The loop stores it on the assistant message and sends it back verbatim
  on the next call. It is never displayed and never parsed; the display summary
  arrives on `ChunkReasoning` instead.

  When a provider returns several reasoning units, pack the ordered list into
  one provider-owned envelope and emit one `ChunkReasoningItem` per turn.
  Emitting one chunk per unit loses their structure and can make replay invalid.

- **`Message.ProviderPhase`** — an OpenAI Responses API phase marker
  (`commentary` / `final_answer`). The loop stores and replays it verbatim.

Neither of these widens `LLMRequest`. The structure is neutral (one opaque blob
per message); the contents are provider-private.

---

## Streaming chunks

`Stream` yields a sequence of `port.Chunk` values. Each chunk has a `Kind`
discriminator:

|Kind|Payload field|What it carries|
|-|-|-|
|`ChunkText`|`Text`|An assistant text delta — append to the in-progress message|
|`ChunkReasoning`|`Text`|A human-readable reasoning summary delta — display-only, not replayed|
|`ChunkReasoningItem`|`Text`|The opaque reasoning replay blob — stored on `Message.Reasoning`, never displayed. Emit **one per turn**; pack a multi-unit payload into your own envelope|
|`ChunkToolCall`|`ToolCall`|A fully assembled tool call, emitted once complete (not streamed per-token)|
|`ChunkUsage`|`Usage`|Terminal usage/cache accounting — input tokens, output tokens, cache hits|
|`ChunkDone`|`Stop`|End of stream with the stop reason (`end_turn`, `max_tokens`, `error`, etc.)|
|`ChunkPhase`|`Text`|Provider phase marker — stored on `Message.ProviderPhase`, never interpreted|

The loop assembles these into a `session.Message` and records it. Tool calls
arrive fully assembled (the adapter buffers the per-token JSON and emits the
complete call once it is valid), so the dispatch layer never deals with partial
tool calls.

`ChunkUsage` and `ChunkDone` are always the last two chunks in a normal stream,
in that order. A stream error yields no `ChunkDone`; the loop recognizes it as
`StopError` and records typed failure metadata when the error exposes it.

### Classify failures without leaking provider data

Provider errors can implement `RetryDispositionError` and `StreamProgressError`.
Mark a request `Retryable` only when replaying it is safe, use `Permanent` for a
known rejection, and leave unknown facts `Unknown`.

A provider can implement `ProviderErrorMetadataError` to report a status,
provider code, and one correlation kind and ID. Diagnostics retain numeric
statuses and hash accepted correlation IDs. They omit raw provider codes and raw
correlation IDs. Never include response bodies, prompts, URLs, arbitrary
headers, tokens, or credentials in error metadata.

Only a typed retryable error before semantically visible output can be replayed
transparently. Unknown chunk kinds commit the attempt.

### Why streaming matters

Mecatl emits each `ChunkText` as a `message.delta` and waits for `ChunkDone`
before dispatching complete tool calls.

## ProviderCapabilities

```go
type ProviderCapabilities struct {
    Image           bool
    Audio           bool
    EmbeddedContext bool
}
```

The zero value is text-only. Return `Image: true` for image input. Audio is
defined but is not accepted by the current OpenAI Responses adapter.

`EmbeddedContext` controls capability advertisement. Inline-text resources are
flattened into prompt text regardless of this value.

Mecatl computes:

```text
modelCapability = (per-model input modalities from live catalog) ∩ (adapter Capabilities())
```

The result is shared by model listing, session creation, and ACP admission so
all three surfaces agree.

---

## Reference implementations

|Package|Type|Description|
|-|-|-|
|`provider/openai`|`*openai.Provider`|OpenAI Responses API (GPT-4o, O3, GPT-5.x); streaming SSE → chunk translation; handles reasoning items, phase markers, and function-call streaming|
|`provider/anthropic`|`*anthropic.Provider`|Anthropic Messages API (Claude 3.x, Claude 4.x); native extended thinking; `(thinking, signature)` reasoning replay|
|`provider/openaichat`|`*openaichat.Provider`|OpenAI Chat Completions API (OpenCode Go, Groq, Together, DeepSeek, vLLM, …); generic Chat Completions wire protocol|
|`internal/adapter/openrouter`|thin wrapper|Routes to `provider/openai` with an OpenRouter base URL; used for multi-model deployments|
|`engine/adapter/mockllm`|`*mockllm.Provider`|Deterministic scripted test double; no network access|

Production adapters are opt-in Go modules under `provider/`. Import only the
provider modules your application uses.

---

## The test double: `engine/adapter/mockllm`

`mockllm.Provider` replays one scripted turn per `Stream` call without network
access.

```go
p := mockllm.New(
    mockllm.TextTurn("sure, let me read the file"),
    mockllm.ToolCallTurn(session.NewToolCall("c1", "Read", map[string]any{"path": "main.go"})),
    mockllm.TextTurn("here is the summary"),
)
```

Turn builders cover the common shapes:

|Builder|What it scripts|
|-|-|
|`TextTurn(text)`|Text delta → zero usage → `StopEndTurn`|
|`ToolCallTurn(calls...)`|Tool calls → zero usage → `StopEndTurn`|
|`EmptyTurn()`|No text or calls, then `StopEndTurn`|
|`EmptyTurnWithStop(stop)`|No text or calls, then the supplied stop|
|`ReasoningTurn(reasoning, text)`|Reasoning summary followed by text|
|`ReasoningOnlyTurn(summary, blob)`|Reasoning summary and replay blob without text|
|`ErrorTurn(err, chunks...)`|Scripted chunks then a genuine in-stream Go error|
|`ChunksTurn(chunks...)`|Full control — assemble any chunk sequence by hand|

`WithCapabilities` flips the mock to image- or audio-capable:

```go
p := mockllm.NewWith(
    []mockllm.Option{
        mockllm.WithCapabilities(port.ProviderCapabilities{Image: true}),
    },
    mockllm.TextTurn("I can see the image"),
)
```

`WithRequestObserver` lets a test assert what actually reached the provider:

```go
var received port.LLMRequest
p := mockllm.NewWith(
    []mockllm.Option{
        mockllm.WithRequestObserver(func(req port.LLMRequest) {
            received = req
        }),
    },
    mockllm.TextTurn("ok"),
)
```

Use `p.Calls()` to inspect the number of stream calls and `p.Reset()` to rewind.

---

## Implementing your own

A minimal implementation needs two methods:

```go
type MyProvider struct { /* your HTTP client, config, etc. */ }

func (p *MyProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
    // Translate req → your provider's wire format
    // Start the streaming HTTP call
    // Return an iterator that yields Chunks until done or ctx is cancelled
}

func (p *MyProvider) Capabilities() port.ProviderCapabilities {
    return port.ProviderCapabilities{Image: true} // or whatever your provider supports
}
```

Key implementation notes:

- **Always emit `ChunkUsage` before `ChunkDone`**. The loop records usage on the
  `ChunkUsage` event; a missing usage chunk means the session tracks zero
  tokens, which breaks the `MaxRunTokens` budget check.
- **Emit `ChunkDone` with the correct stop reason**. The loop maps the stop
  reason to session state: `StopEndTurn` continues cleanly, `StopError` fails
  the session, `StopMaxTokens` ends with a non-error terminal. Do not emit
  `ChunkDone` with `StopEndTurn` when the real reason was `max_tokens` — it
  masks truncated responses and breaks the no-progress nudge logic.
- **Tool calls must arrive fully assembled on `ChunkToolCall`**, not fragmented
  per token. The loop dispatches the complete call; partial-call streaming is an
  adapter-internal concern.
- **Honor context cancellation**. When `ctx.Done()` is closed, stop yielding
  chunks and return from the iterator. The loop manages session state for the
  cancelled turn.
- **The outer error is for start failures**. If the provider returns an error
  before any chunks arrive (a bad request, an auth failure, a network timeout),
  return it as the outer `error`. If the stream starts and then breaks, yield
  the error from the iterator.

### The `Capabilities()` method and multimodal gating

`Capabilities()` is not cosmetic. The composition layer reads it to compute the
capability intersection that gates whether image or audio content is admitted
into the session's prompt. If your adapter can handle images but you return the
zero value, multimodal sessions silently fall back to text-only.

If your provider does not support a modality, return `false` for it — even if
some models on the provider do. The per-model modality data in the live catalog
is a further filter on top of the adapter-level capability. The intersection
takes the conservative value: both the adapter and the model must advertise a
capability for it to be enabled.

---

## The resilience decorator

The shipped commands wrap providers with retry, circuit breaking, and a
stream-idle watchdog:

|Behavior|Default|Flag|
|-|-|-|
|Retry with exponential backoff|enabled|n/a|
|Circuit breaker|enabled|n/a|
|Stream-idle watchdog|180 s|`--llm-stream-idle-timeout`|

When no chunk arrives within `StreamIdleTimeout`, the wrapper returns a terminal
`*StreamIdleError` that satisfies `errors.Is(_, context.DeadlineExceeded)`.
Mid-stream stalls are not retried.

---

## Provider-side prompt caching

Prompt caching is on by default and configured through adapter options:

|Knob|Default|Flag|
|-|-|-|
|Caching enabled|enabled|`--no-prompt-cache` disables it|
|Anthropic cache TTL|API default (5 min)|`--anthropic-cache-ttl` (`5m` or `1h`)|

OpenAI and OpenRouter send cache hints only to their canonical base URLs. A
`--openai-base-url` or `--openrouter-base-url` override disables them so a
compatible endpoint does not receive an unsupported field.

`--llm-per-attempt-timeout` defaults to 300 seconds and covers connection plus
the first chunk. It stops after streaming begins, so it does not truncate a long
active turn.

```
PerAttemptTimeout  → bounds:  connect + first chunk
StreamIdleTimeout  → bounds:  gap between any two consecutive chunks
```

The wrapper forwards the provider's capabilities unchanged.

`mecated` and `mecak8s` include this wrapper. Direct engine embeddings must
provide their own retry, breaker, and idle-timeout behavior when needed.

---

## What's next

- [The agent loop](/building/what-you-get/agent-loop.md) — how the loop calls
  `Stream`, assembles chunks, and dispatches tool calls.
- [SessionStore extension point](/building/extension-points/session-store.md) —
  implement the port that persists conversation state.
- [PermissionPolicy extension point](/building/extension-points/permission-policy.md)
  — implement the port that gates tool execution.
- [Choose how to run Mecatl](/building/getting-started/deployment-decision.md) —
  choosing between embedding the engine, `mecated`, `mecak8s`, and `mecatequi`.
