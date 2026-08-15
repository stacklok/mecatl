---
sidebar_position: 2
title: LLMProvider
---

# LLMProvider

`port.LLMProvider` is the seam between the agent loop and any LLM backend. The loop never imports the OpenAI or Anthropic SDKs; it calls this interface and assembles the resulting stream into domain types. Swap the implementation to point the loop at a different model, a local inference server, or a test double.

---

## The interface

```go
// engine/port/llm.go

type LLMProvider interface {
    Stream(ctx context.Context, req LLMRequest) (iter.Seq2[Chunk, error], error)
    Capabilities() ProviderCapabilities
}
```

**`Stream`** starts a streaming model call and returns a Go 1.23 `iter.Seq2[Chunk, error]` iterator. The outer error reports a failure to start the stream (network unreachable, bad request). An error emitted from the iterator itself is a mid-stream failure — a dropped connection after the model started responding. The loop treats these differently: a start failure is retried by the resilience decorator; a mid-stream error is terminal (no replay after the first chunk).

Context cancellation is the API "cancel" verb — cancel the context to interrupt an in-flight turn mid-stream. Both the OpenAI and Anthropic adapters stop yielding on `ctx.Done()`.

The same context carries the exact active session ID through
`port.SessionIDFromContext`. Mecatl's three real HTTP adapters send a legal value as
`X-Mecatl-Session-ID` on each inference request, which lets gateways correlate a
request with the durable parent, child, member, or auxiliary session that made it;
compaction inside a run keeps that run's ID. The header is optional and
correlation-only—not authentication, tracing, idempotency, provider conversation
state, safety/user identity, or a cache key. Missing or Go-illegal HTTP header values
are omitted without failing the model call. Custom providers may use the context
helper without adding a field to `LLMRequest`. See
[ADR 0216](https://github.com/stacklok/mecatl/blob/main/docs/adr/0216-provider-session-correlation-header.md).

**`Capabilities`** reports which non-text prompt content the provider accepts. The composition layer computes a capability intersection (model-level modalities ∩ adapter capabilities) and uses it to gate multimodal content at the ACP surface — rejecting unsupported image or audio parts loudly rather than silently dropping them. A decorator that wraps another `LLMProvider` **must** forward the inner provider's `Capabilities()` unchanged; replacing it with the zero value breaks multimodal gating.

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

`LLMRequest` is deliberately provider-neutral, enforced by a reflection guard test at `engine/port/llm_neutral_test.go`. If a new field is added, the test fails until the design is reviewed.

`Model` is a bare opaque string — no API key, no base URL, no endpoint. Those are server-side registry concerns handled at construction time by the adapter. The loop never branches on provider identity.

Provider-private knobs that differ by adapter — OpenAI's `store`/`include` flags, Anthropic's `thinking_budget`, reasoning effort on O-series models — are adapter construction `Option` values (e.g. `openai.WithReasoningEffort("high")`), not `LLMRequest` fields. Adding them to the request would force every adapter to ignore fields it does not understand and would make the domain layer provider-aware.

### The stateless contract

mecatl sends the **full conversation history on every turn** (`store: false`). There is no server-side conversation state. This has two consequences:

1. **The system prompt is a `prompt.Layered` struct**, not a raw string. It has a stable prefix (the agent persona, tool schemas, and instructions — the part that does not change turn-to-turn) and a volatile suffix (per-turn context that does change). The byte-stable prefix is what provider-side prompt caching originally keyed on; it now also caches the growing **conversation itself** — Anthropic places breakpoints on the conversation history (not just the system prefix), and OpenAI/OpenRouter carry a routing/observability hint (`prompt_cache_key`) alongside the API's own implicit caching. Changing anything in the stable prefix still busts the cache. See [ADR 0100](https://github.com/stacklok/mecatl/blob/main/docs/adr/0100-provider-prompt-caching.md).
2. **`Messages` carries every message the session has recorded**, including tool calls and results. The adapters re-serialise this into the provider's wire format on each request. Compaction trims the history when it approaches the context limit, but it never enables server-side state as a workaround.

### Reasoning and phase markers

Two fields on `session.Message` carry provider-private blobs that ride the stateless replay without being interpreted:

- **`Message.Reasoning`** — an opaque replay blob (OpenAI `reasoning_item.encrypted_content`, or Anthropic's `(thinking, signature)` pair). The loop stores it on the assistant message and sends it back verbatim on the next call. It is never displayed and never parsed; the display summary arrives on `ChunkReasoning` instead.

  It is one string, but a provider's replay unit may be a *list* — several OpenAI reasoning items, several Anthropic thinking blocks, one per step of a turn that interleaves reasoning with tool calls. When that happens the adapter packs the ordered list into its own JSON envelope inside this one string and unpacks it on replay. Emitting one `ChunkReasoningItem` per unit and letting the loop concatenate them does **not** work: the loop keeps only the last item id, so per-unit ids are lost and OpenAI rejects the replay with `invalid_encrypted_content`. Structure belongs in your envelope, never in the loop's concatenation.
- **`Message.ProviderPhase`** — an OpenAI Responses API phase marker (`commentary` / `final_answer`). GPT-5.x uses it to distinguish intermediate preambles from the actual answer. The loop stores and replays it verbatim; dropping it causes the model to treat every preamble as the final answer and stop early.

Neither of these widens `LLMRequest`. The structure is neutral (one opaque blob per message); the contents are provider-private.

---

## Streaming chunks

`Stream` yields a sequence of `port.Chunk` values. Each chunk has a `Kind` discriminator:

| Kind | Payload field | What it carries |
|------|--------------|-----------------|
| `ChunkText` | `Text` | An assistant text delta — append to the in-progress message |
| `ChunkReasoning` | `Text` | A human-readable reasoning summary delta — display-only, not replayed |
| `ChunkReasoningItem` | `Text` | The opaque reasoning replay blob — stored on `Message.Reasoning`, never displayed. Emit **one per turn**; pack a multi-unit payload into your own envelope |
| `ChunkToolCall` | `ToolCall` | A fully assembled tool call, emitted once complete (not streamed per-token) |
| `ChunkUsage` | `Usage` | Terminal usage/cache accounting — input tokens, output tokens, cache hits |
| `ChunkDone` | `Stop` | End of stream with the stop reason (`end_turn`, `max_tokens`, `error`, etc.) |
| `ChunkPhase` | `Text` | Provider phase marker — stored on `Message.ProviderPhase`, never interpreted |

The loop assembles these into a `session.Message` and records it. Tool calls arrive fully assembled (the adapter buffers the per-token JSON and emits the complete call once it is valid), so the dispatch layer never deals with partial tool calls.

`ChunkUsage` and `ChunkDone` are always the last two chunks in a normal stream, in that order. A mid-stream error yields no `ChunkDone`; the loop recognizes a stream error as `StopError` and fails the session.

### Why streaming matters

The loop emits a `message.delta` event for each `ChunkText` chunk. The client sees text as the model produces it rather than after the turn completes. For the agent loop this also means the model can start streaming a reasoning preamble while the next tool results are still being assembled — the loop records the text as it arrives and dispatches the tool calls only after `ChunkDone`.

---

## ProviderCapabilities

```go
type ProviderCapabilities struct {
    Image           bool
    Audio           bool
    EmbeddedContext bool
}
```

The zero value is text-only. An image-capable adapter returns `Image: true`. Audio is wired but dormant — the OpenAI Responses path has no audio input in the current wire format.

`EmbeddedContext` gates whether the adapter advertises embedded-context support. Inline-text resources always flatten into the prompt text regardless of this flag; it controls the capability advertisement to clients.

The composition layer in `internal/app/capability.go` computes the effective capability as:

```
modelCapability = (per-model input modalities from live catalog) ∩ (adapter Capabilities())
```

It reads live modalities first (the same source the model picker uses), with the catalog as a floor and the adapter as the final constraint. This avoids a class of bugs where a text-only model on a shared multimodal adapter would falsely report `Image: true`. The result is stored once and fed to `ListModels`, the `CreateSessionResponse` echo, and the ACP gate — computed once so all three agree.

---

## Reference implementations

| Package | Type | Description |
|---------|------|-------------|
| `provider/openai` | `*openai.Provider` | OpenAI Responses API (GPT-4o, O3, GPT-5.x); streaming SSE → chunk translation; handles reasoning items, phase markers, and function-call streaming |
| `provider/anthropic` | `*anthropic.Provider` | Anthropic Messages API (Claude 3.x, Claude 4.x); native extended thinking; `(thinking, signature)` reasoning replay |
| `provider/openaichat` | `*openaichat.Provider` | OpenAI Chat Completions API (OpenCode Go, Groq, Together, DeepSeek, vLLM, …); generic Chat Completions wire protocol |
| `internal/adapter/openrouter` | thin wrapper | Routes to `provider/openai` with an OpenRouter base URL; used for multi-model deployments |
| `engine/adapter/mockllm` | `*mockllm.Provider` | Deterministic scripted test double; no network access |

The production wire adapters are their own **opt-in Go submodules** under `provider/` ([ADR 0093](https://github.com/stacklok/mecatl/blob/main/docs/adr/0093-provider-modules.md)) — they import the OpenAI and Anthropic SDKs, which the engine module is not allowed to depend on. A consumer `go get`s exactly the provider(s) it wants and pulls only that SDK, never the root module. The composition layer in `internal/app` wires the appropriate adapter based on the session's provider selection.

The mock lives in `engine/adapter/mockllm` because the engine module's own tests use it — nothing under `engine/` is allowed to import `internal/...`.

---

## The test double: `engine/adapter/mockllm`

`mockllm.Provider` is the implementation you reach for in any engine test. It replays a scripted list of turns — one per `Stream` call — and never touches the network.

```go
p := mockllm.New(
    mockllm.TextTurn("sure, let me read the file"),
    mockllm.ToolCallTurn(session.NewToolCall("c1", "Read", map[string]any{"path": "main.go"})),
    mockllm.TextTurn("here is the summary"),
)
```

Turn builders cover the common shapes:

| Builder | What it scripts |
|---------|----------------|
| `TextTurn(text)` | Text delta → zero usage → `StopEndTurn` |
| `ToolCallTurn(calls...)` | Tool calls → zero usage → `StopEndTurn` |
| `EmptyTurn()` | No text, no calls → zero usage → `StopEndTurn` (the no-progress shape) |
| `EmptyTurnWithStop(stop)` | No text, no calls → zero usage → `stop` (scripts `max_tokens`, `error`, etc.) |
| `ReasoningTurn(reasoning, text)` | Reasoning display delta → text delta → done |
| `ReasoningOnlyTurn(summary, blob)` | Reasoning display + replay blob, no text (the no-progress reasoning shape) |
| `ErrorTurn(err, chunks...)` | Scripted chunks then a genuine in-stream Go error |
| `ChunksTurn(chunks...)` | Full control — assemble any chunk sequence by hand |

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

`p.Calls()` returns the number of `Stream` calls made (useful for turn-count assertions). `p.Reset()` rewinds to the first turn.

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

- **Always emit `ChunkUsage` before `ChunkDone`**. The loop records usage on the `ChunkUsage` event; a missing usage chunk means the session tracks zero tokens, which breaks the `MaxRunTokens` budget check.
- **Emit `ChunkDone` with the correct stop reason**. The loop maps the stop reason to session state: `StopEndTurn` continues cleanly, `StopError` fails the session, `StopMaxTokens` ends with a non-error terminal. Do not emit `ChunkDone` with `StopEndTurn` when the real reason was `max_tokens` — it masks truncated responses and breaks the no-progress nudge logic.
- **Tool calls must arrive fully assembled on `ChunkToolCall`**, not fragmented per token. The loop dispatches the complete call; partial-call streaming is an adapter-internal concern.
- **Honour context cancellation**. When `ctx.Done()` is closed, stop yielding chunks and return from the iterator. The loop manages the session state for a cancelled turn; the adapter just needs to stop.
- **The outer error is for start failures**. If the provider returns an error before any chunks arrive (a bad request, an auth failure, a network timeout), return it as the outer `error`. If the stream starts and then breaks, yield the error from the iterator.

### The `Capabilities()` method and multimodal gating

`Capabilities()` is not cosmetic. The composition layer reads it to compute the capability intersection that gates whether image or audio content is admitted into the session's prompt. If your adapter can handle images but you return the zero value, multimodal sessions silently fall back to text-only.

If your provider does not support a modality, return `false` for it — even if some models on the provider do. The per-model modality data in the live catalog is a further filter on top of the adapter-level capability. The intersection takes the conservative value: both the adapter and the model must advertise a capability for it to be enabled.

---

## The resilience decorator

`internal/adapter/llmresilience` wraps any `LLMProvider` with three behaviours:

| Behaviour | Default | Flag |
|-----------|---------|------|
| Retry with exponential backoff | enabled | n/a |
| Circuit breaker | enabled | n/a |
| Stream-idle watchdog | 180 s | `--llm-stream-idle-timeout` |

**The stream-idle watchdog** bounds mid-stream stalls. After the first chunk arrives, a per-chunk timer runs. If no new chunk arrives within `StreamIdleTimeout`, the decorator synthesizes a terminal `*StreamIdleError` (which satisfies `errors.Is(_, context.DeadlineExceeded)`) and ends the stream. This matters because the OpenAI and Anthropic adapters swallow context errors on cancel (they yield nothing), so the wrapper must synthesize the terminal signal rather than waiting for the inner iterator.

A mid-stream stall is **terminal, never retried** — the no-replay-after-first-chunk contract holds.

---

## Provider-side prompt caching

Caching is ON by default and adapter-construction-Option-driven, like the reasoning-effort knob above ([ADR 0100](https://github.com/stacklok/mecatl/blob/main/docs/adr/0100-provider-prompt-caching.md)):

| Knob | Default | Flag |
|-----------|---------|------|
| Caching enabled | enabled | `--no-prompt-cache` disables it |
| Anthropic cache TTL | API default (5 min) | `--anthropic-cache-ttl` (`5m` or `1h`) |

The OpenAI/OpenRouter cache dialect — which hints get sent, if any — is gated on `(provider id, resolved base URL)`, never the provider id alone: a non-canonical base URL (`--openai-base-url` pointed at vLLM/LiteLLM, or `--openrouter-base-url` overridden) degrades to no hints at all, since a strict-compatible upstream can 400 on an unrecognised field.

**`PerAttemptTimeout`** (default 300 s, `--llm-per-attempt-timeout`) bounds only the establishment phase: connect plus the first chunk. It is implemented as a `time.Timer` that fires `cancel()` if no first chunk arrives in time. It is explicitly **not** a `context.WithTimeout` — a fixed deadline that stays live through streaming would truncate slow reasoning turns when the absolute deadline passes.

```
PerAttemptTimeout  → bounds:  connect + first chunk
StreamIdleTimeout  → bounds:  gap between any two consecutive chunks
```

Both decorators are transparent to `Capabilities()` — the resilience wrapper forwards the inner provider's capabilities unchanged.

**Consumer note.** If you use `mecated` or `mecak8s`, the resilience decorator is already wired for you at composition time. If you embed the engine directly (the `Embed the engine` deployment shape), you wire it yourself:

```go
import "github.com/stacklok/mecatl/internal/adapter/llmresilience"

decorated := llmresilience.Wrap(myProvider, llmresilience.Config{
    StreamIdleTimeout: 3 * time.Minute,
    PerAttemptTimeout: 5 * time.Minute,
})
engine := agent.NewEngine(agent.Deps{LLM: decorated, ...})
```

---

## What's next

- [The agent loop](/what-you-get/agent-loop.md) — how the loop calls `Stream`, assembles chunks, and dispatches tool calls.
- [SessionStore extension point](/extension-points/session-store.md) — implement the port that persists conversation state.
- [PermissionPolicy extension point](/extension-points/permission-policy.md) — implement the port that gates tool execution.
- [Pick your deployment shape](/getting-started/deployment-decision.md) — choosing between embedding the engine, `mecated`, `mecak8s`, and `mecatequi`.
