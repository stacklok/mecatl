// Package port — cycle note: LLMRequest references tool.ToolSpec and
// prompt.Layered, so port imports tool and prompt (and session, governance).
// FileSystem/Workspace/Environment deliberately live in engine/tool, not here, to avoid a
// port↔tool import cycle (Tool.Execute takes an Environment).
package port

import (
	"context"
	"iter"

	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// LLMRequest is the provider-neutral input to a model call. System is the
// two-layer system prompt (stable prefix + volatile suffix for cache
// breakpoints); Tools are the schemas, stable across turns for caching.
//
// DTO-neutrality guardrail (multi-provider Finding B): this struct is
// deliberately provider-NEUTRAL and must stay so. Model is a BARE opaque string
// (no provider/endpoint/key rides the request — those are server-side registry
// concerns). Provider-PRIVATE knobs (OpenAI's store/include flags, an Anthropic
// thinking-budget, a reasoning-effort) are an ADAPTER CONSTRUCTION concern — a
// WithThinkingBudget-style Option like the existing openai.WithBaseURL — NOT a new
// LLMRequest field, because the domain/agent loop never branches on provider. A
// reflection guard test (llm_neutral_test.go) tripwires any silent field addition.
type LLMRequest struct {
	// System is the layered system prompt.
	System prompt.Layered
	// Messages is the conversation history to send.
	Messages []session.Message
	// Tools are the tool schemas the model may call.
	Tools []tool.ToolSpec
	// Model is the provider model identifier (an opaque string; the adapter maps it
	// to the concrete wire model). Provider-neutral — see the struct doc-comment.
	Model string
}

// ChunkKind is the kind of a streamed Chunk.
type ChunkKind int

const (
	// ChunkText is an assistant text delta.
	ChunkText ChunkKind = iota
	// ChunkReasoning is a human-readable reasoning summary delta. It is
	// DISPLAY-ONLY: clients render it for visibility into the model's thinking;
	// it is NOT the blob replayed to the provider. (See ChunkReasoningItem.)
	//
	// DTO-neutrality note (multi-provider Finding C): the ChunkReasoning (display
	// summary) vs ChunkReasoningItem (replay blob) split is the PROVIDER-NEUTRAL
	// seam P1 (the native Anthropic adapter) validates — Anthropic's thinking delta
	// maps to ChunkReasoning, its (thinking,signature) replay token to
	// ChunkReasoningItem. Keep the split; do not collapse the two.
	ChunkReasoning
	// ChunkReasoningItem carries the provider's opaque reasoning REPLAY blob
	// (e.g. OpenAI's reasoning-item encrypted_content, or Anthropic's
	// (thinking,signature) pair), emitted once the reasoning output item is
	// assembled. The Text field holds the opaque blob, which the loop stores on
	// Message.Reasoning and the adapter sends back verbatim on subsequent stateless
	// calls. It is never displayed or interpreted — the contents are provider-
	// private; only the STRUCTURE (one opaque blob per message) is neutral. The
	// item's provider id rides ReasoningItemID alongside it (stored on
	// Message.ReasoningItemID); a provider with no per-item id (Anthropic) leaves
	// it empty.
	ChunkReasoningItem
	// ChunkToolCall is a fully-assembled tool call, emitted once complete.
	ChunkToolCall
	// ChunkUsage is the terminal usage/cache accounting.
	ChunkUsage
	// ChunkDone is the end of stream; it carries the StopReason.
	ChunkDone
	// ChunkPhase carries the provider's opaque PHASE marker on Text (mirrors
	// ChunkReasoningItem). The OpenAI Responses API tags an assistant output
	// message as intermediate commentary or the final answer; for store:false
	// manual-replay apps the phase must be preserved and resent on the assistant
	// message item, or GPT-5.x models treat preambles as final answers / stop
	// early. The loop stores it on Message.ProviderPhase and the adapter sends it back
	// verbatim on subsequent stateless calls. Like ChunkReasoningItem it is never
	// displayed or interpreted — the STRUCTURE is neutral (one opaque phase string
	// per message), the CONTENTS are provider-private (the harness never branches
	// on or validates the value). Placed LAST in the block — chunks are in-process
	// only, never serialized as ints, so ordinal stability is not a concern.
	ChunkPhase
	// ChunkProviderRoute carries the opaque DOWNSTREAM provider display label that
	// actually served a routed request on Text (issue #480). It is emitted by the
	// OpenAI adapter ONLY when OpenRouter metadata is explicitly armed, at the
	// terminal response.completed event. Like ChunkPhase it is DISPLAY-ONLY and
	// provider-private in CONTENTS: the loop relays it verbatim onto an
	// EvProviderRoute event for clients, never branches on it, never replays it,
	// never anchors TTFT or feeds usage. It is absent on a cache hit (OpenRouter
	// strips openrouter_metadata from cached responses) — the loop simply emits
	// nothing. The STRUCTURE is neutral (one opaque label per turn); mecatl's
	// "provider" stays the wire adapter — this is the DOWNSTREAM inference provider
	// OpenRouter routed to. The label is human-readable and is not a routing slug or
	// round-trippable identifier. Same discipline as ChunkPhase.
	ChunkProviderRoute
)

// Chunk is a single provider-neutral unit of a model stream. The loop assembles
// a sequence of Chunks into a domain Message. The OpenAI Responses specifics
// (function_call items, reasoning items, SSE framing, cache accounting) live
// entirely inside the openai adapter; the loop never sees a provider type.
type Chunk struct {
	// Kind discriminates the payload.
	Kind ChunkKind
	// Text carries the assistant text on ChunkText, the human-readable reasoning
	// summary on ChunkReasoning (display-only), the provider's opaque reasoning
	// replay blob on ChunkReasoningItem (e.g. OpenAI encrypted_content or Anthropic
	// (thinking,signature); never displayed), and the provider's opaque phase
	// marker on ChunkPhase (stored on Message.Phase, replayed verbatim).
	Text string
	// ReasoningItemID is set on ChunkReasoningItem. It carries the provider's
	// opaque reasoning ITEM id (e.g. OpenAI's "rs_…" id on a reasoning output
	// item), which the loop stores on Message.ReasoningItemID and the adapter
	// stamps back verbatim on subsequent stateless calls: the OpenAI Responses
	// reasoning item's id field is api:"required" with no omitzero, so a replay
	// without the captured id serialises "id":"" and strict OpenAI-compatible
	// gateways reject it (HTTP 400). Like the Text blob it accompanies, it is
	// never displayed, interpreted, or validated — the STRUCTURE is neutral
	// (one opaque id per reasoning item), the CONTENTS are provider-private.
	// Same discipline as ToolCall.ItemID and the ChunkPhase carrier.
	ReasoningItemID string
	// ToolCall is set on ChunkToolCall.
	ToolCall *session.ToolCall
	// Usage is set on ChunkUsage.
	Usage *session.Usage
	// Stop is set on ChunkDone.
	Stop session.StopReason
}

// ProviderCapabilities declares which non-text prompt input a provider can
// consume. The capability seam is the single switch that gates multimodal
// prompt content: a surface adapter (e.g. ACP) consults it to advertise its
// promptCapabilities and to loud-reject unsupported content rather than
// silently dropping it. The zero value is text-only (every field false).
type ProviderCapabilities struct {
	// Image reports whether the provider consumes image parts.
	Image bool
	// Audio reports whether the provider consumes audio parts.
	Audio bool
	// EmbeddedContext reports whether the provider ADVERTISES embedded-context
	// support. Inline-text resources always flatten into the prompt text
	// regardless; this gates whether the adapter declares the capability.
	EmbeddedContext bool
}

// LLMProvider is the provider-agnostic seam for model calls. Stream yields
// provider-neutral chunks until ctx is cancelled or the model stops; ctx
// cancellation is how the API "cancel" verb interrupts an in-flight turn. The
// returned iter.Seq2 yields (Chunk, error) pairs; a non-nil error terminates the
// stream. The outer error reports a failure to start the stream.
//
// Capabilities reports which non-text prompt input the provider can consume, so
// a surface adapter can advertise it and gate unsupported content. A decorator
// MUST forward the inner provider's Capabilities.
type LLMProvider interface {
	Stream(ctx context.Context, req LLMRequest) (iter.Seq2[Chunk, error], error)
	Capabilities() ProviderCapabilities
}

// RetryDispositionError exposes a failure's causal retry classification through
// errors.As without adding provider-specific fields to LLMRequest.
type RetryDispositionError interface {
	error
	RetryDisposition() session.RetryDisposition
}

// StreamProgressError exposes the semantic progress of a terminal stream error.
type StreamProgressError interface {
	error
	StreamProgress() session.StreamProgress
}
