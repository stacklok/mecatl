---
sidebar_position: 2
title: LLMProvider
description:
  Implement LLMProvider to connect the Mecatl agent loop to a model backend.
---

# LLMProvider

`port.LLMProvider` connects the agent loop to a model backend. Implement it to
use another model service, a local inference server, or a test double.

## The interface

```go
type LLMProvider interface {
    Stream(ctx context.Context, req LLMRequest) (iter.Seq2[Chunk, error], error)
    Capabilities() ProviderCapabilities
}
```

`Stream` starts a model call and returns provider-neutral chunks. Return a
startup failure as the outer error. After streaming starts, yield failures from
the iterator. Stop the request and iterator when the context is canceled.

`Capabilities` reports the non-text content the adapter accepts. Decorators must
forward the wrapped provider's capabilities.

## Translate requests

```go
type LLMRequest struct {
    System   prompt.Layered
    Messages []session.Message
    Tools    []tool.ToolSpec
    Model    string
}
```

Treat `Model` as an opaque provider identifier. Keep API keys, base URLs, and
provider-specific options in your adapter's configuration.

Use `port.SessionIDFromContext` to retrieve the session ID for correlation or
provider routing. The value grants no authority.

Mecatl sends the full conversation on every request. Preserve these values when
translating messages:

- `Message.Reasoning` and `Message.ReasoningItemID` contain opaque,
  provider-owned reasoning replay data.
- `Message.ProviderPhase` contains an opaque phase marker.
- `ToolCall.ItemID` contains a provider-owned tool-call identifier.

Send these values back to the provider unchanged. Do not display, parse, or
reinterpret them.

`prompt.Layered` separates a stable system-prompt prefix from per-turn context.
An adapter can use that boundary for provider-side prompt caching.

## Emit streaming chunks

Each `port.Chunk` has a `Kind` and the corresponding payload:

|Kind|Payload|Use|
|-|-|-|
|`ChunkText`|`Text`|Assistant text delta|
|`ChunkReasoning`|`Text`|Display-only reasoning summary delta|
|`ChunkReasoningItem`|`Text`, `ReasoningItemID`|Opaque reasoning replay data|
|`ChunkToolCall`|`ToolCall`|One complete tool call|
|`ChunkUsage`|`Usage`|Terminal token and cache accounting|
|`ChunkDone`|`Stop`|Stream completion and stop reason|
|`ChunkPhase`|`Text`|Opaque phase marker for replay|
|`ChunkProviderRoute`|`Text`|Display label for the downstream provider that served a routed request|

Buffer provider tool-call deltas inside the adapter and emit one `ChunkToolCall`
only after the call is complete and its arguments are valid.

For a normal stream, emit `ChunkUsage` and then `ChunkDone`. Use the actual stop
reason so Mecatl can distinguish a completed turn, a token limit, and an error.
If the stream fails, yield the error without emitting `ChunkDone`.

If the provider returns several reasoning units, encode their ordered data in
one provider-owned envelope and emit one `ChunkReasoningItem` per turn.

## Classify errors

Provider errors can implement these optional interfaces:

- `RetryDispositionError` reports whether replay is safe, permanently rejected,
  or unknown.
- `StreamProgressError` reports whether the failed request produced semantically
  visible output.
- `ProviderErrorMetadataError` supplies bounded status, provider-code, and
  correlation metadata for diagnostics.

Only classify a request as retryable when replaying it is safe. Keep response
bodies, prompts, URLs, arbitrary headers, tokens, and credentials out of error
metadata.

## Report capabilities

```go
type ProviderCapabilities struct {
    Image           bool
    Audio           bool
    EmbeddedContext bool
}
```

The zero value is text-only. For a model with catalog or live metadata, Mecatl
enables a modality only when both that metadata and the adapter report support.
For an uncatalogued model, the adapter's capabilities apply directly. Return
`false` when the adapter cannot translate a modality.

`EmbeddedContext` controls capability advertisement. Inline text resources are
flattened into prompt text regardless of this value.

## Implement a provider

A minimal adapter translates the request, opens a stream, and yields chunks:

```go
type Provider struct {
    client *Client
}

func (p *Provider) Stream(
    ctx context.Context,
    req port.LLMRequest,
) (iter.Seq2[port.Chunk, error], error) {
    return p.client.Stream(ctx, req)
}

func (p *Provider) Capabilities() port.ProviderCapabilities {
    return port.ProviderCapabilities{Image: true}
}
```

Before using the adapter, verify that it:

1. Preserves opaque replay fields.
1. Emits complete tool calls.
1. Emits usage before the final stop chunk.
1. Reports the provider's real stop reason.
1. Separates startup errors from in-stream errors.
1. Stops promptly on context cancellation.

## Use the test provider

`engine/adapter/mockllm` scripts deterministic turns without network access:

```go
provider := mockllm.New(
    mockllm.ToolCallTurn(
        session.NewToolCall(
            "call-1",
            "Read",
            json.RawMessage(`{"path":"main.go"}`),
        ),
    ),
    mockllm.TextTurn("The file defines the main package."),
)
```

Use `mockllm.NewWith` to set capabilities or observe incoming requests. Use
`Calls` to inspect the stream-call count and `Reset` to replay the script.

## Supplied providers

Real providers are opt-in Go modules. Import only the modules your application
uses.

|Package|API|
|-|-|
|`provider/openai`|OpenAI Responses API|
|`provider/anthropic`|Anthropic Messages API|
|`provider/openaichat`|OpenAI Chat Completions-compatible APIs|
|`internal/adapter/openrouter`|OpenRouter through the OpenAI provider|
|`engine/adapter/mockllm`|Offline scripted test provider|

The shipped commands add retry, circuit breaking, and a stream-idle watchdog
around providers. They configure a 300-second limit for connection and
first-chunk establishment and a 180-second limit between streamed chunks. A
direct engine embedding must configure any required resilience and timeouts.

## Next steps

- [Understand the agent loop](/features/agent-loop.md).
- [Implement session storage](session-store.md).
- [Add tools to the catalog](tool-catalog.md).
