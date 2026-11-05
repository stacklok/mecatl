// Package mockllm provides a deterministic, scripted implementation of
// port.LLMProvider for testing the agent loop without any network access.
//
// A Provider is constructed from an ordered list of "turns"; each turn is a
// canned sequence of port.Chunk values. Every call to Stream emits the next
// turn's chunks and advances an internal cursor, so successive model calls in a
// loop replay successive scripted turns. Turn-builder helpers (TextTurn,
// ToolCallTurn, ...) make scripts terse:
//
//	p := mockllm.New(
//		mockllm.TextTurn("hello"),
//		mockllm.ToolCallTurn(call),
//	)
//
// Stream honours context cancellation: if ctx is done it stops yielding.
package mockllm

import (
	"context"
	"iter"
	"sync"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// Turn is one scripted model response: the ordered chunks Stream emits for a
// single call. Build one with the Turn* helpers or assemble Chunks by hand.
type Turn struct {
	// Chunks is the ordered sequence emitted for this turn.
	Chunks []port.Chunk
	// Err, when non-nil, is yielded as a GENUINE in-stream error (the iterator's
	// error value) AFTER Chunks — the shape of a transient provider failure
	// mid-stream (an upstream 5xx that exhausted the resilience layer's
	// retries). The agent loop maps it to a StopError terminal that Fail()s the
	// session, distinct from a provider-REPORTED terminal carried on the
	// ChunkDone stop (see EmptyTurnWithStop). Build one with ErrorTurn.
	Err error
}

// Provider is a deterministic, scripted port.LLMProvider. Each Stream call
// replays the next programmed Turn. It is safe for concurrent use; the cursor is
// guarded by a mutex.
type Provider struct {
	mu       sync.Mutex
	turns    []Turn
	cursor   int
	caps     port.ProviderCapabilities
	observer func(port.LLMRequest)
}

// Option configures a Provider.
type Option func(*Provider)

// WithCapabilities sets the capabilities the mock advertises. The default is
// text-only (the zero ProviderCapabilities). Tests use it to flip the mock to
// image- or audio-capable without reaching for the OpenAI adapter.
func WithCapabilities(caps port.ProviderCapabilities) Option {
	return func(p *Provider) { p.caps = caps }
}

// WithRequestObserver registers an optional observer invoked with each
// port.LLMRequest the mock receives, BEFORE the scripted turn is yielded. It lets
// a test assert what actually reached the provider — e.g. that a multimodal prompt
// carried its media Parts across the wire→domain→engine→provider path. It is
// purely additive: a Provider built without it behaves exactly as before (the
// default observer is nil and never called), so existing mockllm users are
// unaffected. The observer runs on the calling goroutine, under no lock; keep it
// cheap and side-effect-light (a test capture typically copies the field it needs).
func WithRequestObserver(fn func(port.LLMRequest)) Option {
	return func(p *Provider) { p.observer = fn }
}

// New constructs a Provider that replays the given turns in order, one per
// Stream call. It advertises text-only capabilities unless NewWith is used.
func New(turns ...Turn) *Provider {
	return &Provider{turns: turns}
}

// NewWith constructs a Provider with the given options (e.g. WithCapabilities)
// and scripted turns.
func NewWith(opts []Option, turns ...Turn) *Provider {
	p := &Provider{turns: turns}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Capabilities reports the configured capabilities (text-only by default).
func (p *Provider) Capabilities() port.ProviderCapabilities {
	return p.caps
}

// Calls reports how many times Stream has been invoked (i.e. the current cursor
// position). It is useful for assertions in tests.
func (p *Provider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cursor
}

// Reset rewinds the cursor to the first turn, so the same Provider can be
// replayed again.
func (p *Provider) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cursor = 0
}

// Stream returns an iterator over the next scripted turn's chunks, advancing the
// internal cursor. If the script is exhausted it returns an empty iterator (no
// chunks, no error). The returned iterator stops early if ctx is cancelled. The
// outer error is always nil; the mock never fails to start a stream — a scripted
// Turn.Err is yielded IN-stream after the turn's chunks (a mid-stream failure),
// matching how the real adapters surface a broken stream.
func (p *Provider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.mu.Lock()
	var turn Turn
	if p.cursor < len(p.turns) {
		turn = p.turns[p.cursor]
		p.cursor++
	}
	observer := p.observer
	p.mu.Unlock()

	// Surface the request to an optional test observer (nil in the common case),
	// so a test can assert what actually reached the provider.
	if observer != nil {
		observer(req)
	}

	return func(yield func(port.Chunk, error) bool) {
		for _, c := range turn.Chunks {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if !yield(c, nil) {
				return
			}
		}
		if turn.Err != nil {
			yield(port.Chunk{}, turn.Err)
		}
	}, nil
}

// Compile-time assertion that Provider satisfies the port.
var _ port.LLMProvider = (*Provider)(nil)

// --- Turn builders ---------------------------------------------------------

// TextTurn builds a turn that streams text as a single text delta, then a usage
// chunk (zero usage) and a ChunkDone carrying StopEndTurn. It is the common
// "model answered with prose and stopped" case.
func TextTurn(text string) Turn {
	return Turn{Chunks: []port.Chunk{
		{Kind: port.ChunkText, Text: text},
		{Kind: port.ChunkUsage, Usage: &session.Usage{}},
		{Kind: port.ChunkDone, Stop: session.StopEndTurn},
	}}
}

// EmptyTurn builds an UNCOOPERATIVE turn that emits NO text and NO tool call —
// only a zero usage chunk and a ChunkDone(StopEndTurn). It scripts the "completed
// turn with no progress" shape a reasoning model can produce (it "finished" without
// a deliverable), which the loop's no-progress handler must catch and nudge rather
// than terminate silently. A cooperative happy-path mock (TextTurn/ToolCallTurn)
// can never produce this shape — that is exactly the gap this helper closes.
func EmptyTurn() Turn {
	return EmptyTurnWithStop(session.StopEndTurn)
}

// EmptyTurnWithStop builds an UNCOOPERATIVE turn that emits NO text and NO tool
// call, then a zero usage chunk and a ChunkDone carrying the GIVEN stop reason. It
// scripts the "empty turn caused by a real terminal condition" shape: both adapters'
// mapStop relay max_tokens / refusal / incomplete / failed as session.StopError (and
// cancelled as StopCancelled) on the ChunkDone stop, NOT as a Go error — and such a
// truncated/refused response can come back with no text. The loop must SURFACE that
// real stop reason, NOT nudge "please continue" or relabel it StopNoProgress. This is
// the regression guard for the streamStop-masking bug. EmptyTurn() is this with a
// benign StopEndTurn (the genuine no-progress shape that DOES get nudged).
func EmptyTurnWithStop(stop session.StopReason) Turn {
	return Turn{Chunks: []port.Chunk{
		{Kind: port.ChunkUsage, Usage: &session.Usage{}},
		{Kind: port.ChunkDone, Stop: stop},
	}}
}

// ErrorTurn builds a turn that yields the given chunks (commonly none) and then
// a GENUINE in-stream error — the mid-stream failure shape of a transient
// provider outage (an upstream 5xx that exhausted the resilience layer's
// retries). The agent loop maps a stream error to a StopError terminal that
// calls session.Fail(), landing the session in StateFailed. It is DISTINCT from
// EmptyTurnWithStop(session.StopError), which scripts a provider-REPORTED
// terminal condition relayed on the ChunkDone stop with NO Go error. Use this
// to exercise the failed-session recovery seam (Session.Recover, issue #51).
func ErrorTurn(err error, chunks ...port.Chunk) Turn {
	return Turn{Chunks: chunks, Err: err}
}

// ReasoningOnlyTurn builds an UNCOOPERATIVE turn that emits a reasoning DISPLAY
// delta (displaySummary, human-readable, display-only) and a reasoning REPLAY-item
// blob (replayBlob, the opaque encrypted_content analogue) but NO visible text and
// NO tool call, then a zero usage chunk and a ChunkDone(StopEndTurn). It scripts a
// reasoning-model turn that "thought" but produced no deliverable — the exact
// no-progress trigger — with the replay blob present so a test can also assert the
// blob is preserved on the recorded empty assistant message and replayed across the
// nudge. Either argument may be empty to omit that chunk.
func ReasoningOnlyTurn(displaySummary, replayBlob string) Turn {
	chunks := make([]port.Chunk, 0, 4)
	if displaySummary != "" {
		chunks = append(chunks, port.Chunk{Kind: port.ChunkReasoning, Text: displaySummary})
	}
	if replayBlob != "" {
		chunks = append(chunks, port.Chunk{Kind: port.ChunkReplay, Replay: &session.ProviderReplay{Format: "mockllm", Version: 1, Data: []byte(replayBlob)}, ReplayIsOutput: true})
	}
	chunks = append(chunks,
		port.Chunk{Kind: port.ChunkUsage, Usage: &session.Usage{}},
		port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn},
	)
	return Turn{Chunks: chunks}
}

// ToolCallTurn accepts provider-era scripted fixtures and emits live LLM DTOs.
// Fixture IDs are provider pairing tokens, never accepted engine identities.
// For new scripts use LLMToolCallTurn. A zero-call script simply stops.
//
// The call NAME is NOT validated against any catalog: a test can script a
// wrong/unknown tool name (e.g. session.NewToolCall("c1", "Nonexistent", nil)) to
// exercise the loop's unknown-tool path (which must open a visible card before the
// error result). That is the adversarial shape this helper supports unchanged.
func ToolCallTurn(calls ...session.ToolCall) Turn {
	chunks := make([]port.Chunk, 0, len(calls)+2)
	for i := range calls {
		c := LLMCall(calls[i])
		chunks = append(chunks, port.Chunk{Kind: port.ChunkToolCall, ToolCall: &c})
	}
	chunks = append(chunks,
		port.Chunk{Kind: port.ChunkUsage, Usage: &session.Usage{}},
		port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn},
	)
	return Turn{Chunks: chunks}
}

// LLMCall converts an old scripted fixture's provider-era keys to live DTOs.
// This is a test-script convenience, never a canonical history projection.
func LLMCall(call session.ToolCall) port.LLMToolCall {
	return port.LLMToolCall{ID: session.ProviderCallID(call.ID), Name: call.Name, Args: append([]byte(nil), call.Args...)}
}

// LLMToolCallTurn scripts provider-facing calls directly.
func LLMToolCallTurn(calls ...port.LLMToolCall) Turn {
	chunks := make([]port.Chunk, 0, len(calls)+2)
	for _, call := range calls {
		chunks = append(chunks, port.Chunk{Kind: port.ChunkToolCall, ToolCall: &call})
	}
	chunks = append(chunks, port.Chunk{Kind: port.ChunkUsage, Usage: &session.Usage{}}, port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn})
	return Turn{Chunks: chunks}
}

// ReasoningTurn builds a turn that emits a reasoning delta and then text,
// followed by a zero usage chunk and a ChunkDone carrying StopEndTurn.
func ReasoningTurn(reasoning, text string) Turn {
	return Turn{Chunks: []port.Chunk{
		{Kind: port.ChunkReasoning, Text: reasoning},
		{Kind: port.ChunkText, Text: text},
		{Kind: port.ChunkUsage, Usage: &session.Usage{}},
		{Kind: port.ChunkDone, Stop: session.StopEndTurn},
	}}
}

// ChunksTurn wraps an explicit chunk sequence as a Turn for full control over
// the script (e.g. custom usage, a specific StopReason, or interleaved kinds).
func ChunksTurn(chunks ...port.Chunk) Turn {
	return Turn{Chunks: chunks}
}

// TextChunk builds a ChunkText.
func TextChunk(text string) port.Chunk { return port.Chunk{Kind: port.ChunkText, Text: text} }

// ReasoningChunk builds a ChunkReasoning (human-readable DISPLAY summary).
func ReasoningChunk(text string) port.Chunk {
	return port.Chunk{Kind: port.ChunkReasoning, Text: text}
}

// ReplayChunk builds a ChunkReplay carrying a mock-owned opaque blob.
// The engine never interprets the mockllm format.
func ReplayChunk(blob string) port.Chunk {
	return port.Chunk{Kind: port.ChunkReplay, Replay: &session.ProviderReplay{Format: "mockllm", Version: 1, Data: []byte(blob)}, ReplayIsOutput: true}
}

// PhaseChunk builds a ChunkPhase carrying the opaque phase marker (the analogue
// of OpenAI's assistant-message phase, "commentary"/"final_answer"). The value
// is passed through verbatim — the harness never interprets it — so a test can
// script any opaque string to prove the engine threads it without branching.
func PhaseChunk(phase string) port.Chunk {
	return port.Chunk{Kind: port.ChunkPhase, Text: phase}
}

// ToolCallChunk builds a ChunkToolCall.
func ToolCallChunk(call session.ToolCall) port.Chunk {
	c := LLMCall(call)
	return port.Chunk{Kind: port.ChunkToolCall, ToolCall: &c}
}

// UsageChunk builds a ChunkUsage.
func UsageChunk(u session.Usage) port.Chunk {
	return port.Chunk{Kind: port.ChunkUsage, Usage: &u}
}

// DoneChunk builds a ChunkDone carrying the given stop reason.
func DoneChunk(stop session.StopReason) port.Chunk {
	return port.Chunk{Kind: port.ChunkDone, Stop: stop}
}
