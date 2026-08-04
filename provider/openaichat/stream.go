package openaichat

import (
	"encoding/json"
	"fmt"
	"io"

	oai "github.com/openai/openai-go/v3"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// errTruncatedStream is surfaced when the stream ends cleanly (no error frame)
// but WITHOUT a finish_reason. The SDK's ssestream returns Err()==nil on a plain
// mid-stream EOF — a dropped connection is indistinguishable from a normal close
// at that layer, and [DONE] is NOT proven. Failing closed here (rather than
// fabricating a terminal in finalize) stops a truncated stream from becoming a
// successful tool-executing turn. It wraps io.ErrUnexpectedEOF so the resilience
// classifier treats a PRE-commit truncation as retryable (a post-commit one is
// terminal by the no-replay rule).
var errTruncatedStream = fmt.Errorf("openaichat: stream ended without finish_reason: %w", io.ErrUnexpectedEOF)

// maxToolArgsBytes bounds the TOTAL streamed tool-call argument JSON accumulated
// across a stream. Chat Completions delivers tool-call arguments as fragments
// concatenated here, so a buggy or hostile compatible endpoint could otherwise
// grow memory unbounded. Mirrors the anthropic adapter's per-block guard (8 MiB is
// generous for any real tool call); exceeding it fails the stream.
const maxToolArgsBytes = 8 << 20

// streamState carries the state translate needs across chat.completion.chunk
// events. Chat Completions streams tool-call arguments FRAGMENTED across deltas
// keyed by index (unlike the Responses API, which delivers an assembled item), so
// tool calls are accumulated here and flushed once on the terminal chunk. Usage
// arrives on (or alongside) the finish chunk; it is captured whenever seen so a
// finish chunk that also carries usage emits it before the terminal ChunkDone.
type streamState struct {
	toolCalls map[int64]*session.ToolCall // partial calls, keyed by delta index
	order     []int64                     // index arrival order, for deterministic flush
	argBytes  int                         // running total of accumulated tool-arg bytes (bounded by maxToolArgsBytes)
	usage     session.Usage
	hasUsage  bool
	finished  bool               // finish_reason seen; content/tool deltas stop
	stop      session.StopReason // the mapped terminal, emitted by finalize
}

// translate converts a single chat.completion.chunk into zero or more
// provider-neutral chunks. It is a pure function (apart from the carried
// streamState) so it can be driven directly from recorded SSE fixtures in tests.
//
// Mapping:
//   - delta.content            -> ChunkText (emitted per-chunk as it streams)
//   - delta.reasoning_content  -> ChunkReasoning (display-only; read from raw JSON
//     extra fields since the SDK has no typed field; never replayed)
//   - delta.tool_calls[]       -> accumulated by index (flushed by finalize)
//   - finish_reason != ""      -> record the stop reason (terminal deferred)
//   - usage (finish chunk OR a trailing choices:[] frame) -> captured
//
// The terminal chunks (ChunkToolCall(s), ChunkUsage, ChunkDone) are NOT emitted
// here — they are buffered and flushed once by finalize at clean stream end, so a
// usage frame that arrives AFTER finish_reason (the standard OpenAI shape) is not
// lost. A terminal PROVIDER error (an SSE frame carrying a top-level "error") is
// surfaced by the SDK's ssestream decoder as stream.Err() in Stream, not here.
func translate(chunk oai.ChatCompletionChunk, st *streamState) ([]port.Chunk, error) {
	var out []port.Chunk

	// Usage can ride the finish chunk (OpenCode Go) OR a trailing choices:[] chunk
	// AFTER finish_reason (the standard OpenAI include_usage shape, where the finish
	// chunk itself carries usage:null). Capture it whenever seen; the terminal
	// ChunkUsage is emitted from finalize at stream end, so a post-finish usage
	// frame is counted, never dropped.
	if u := chunk.Usage; u.PromptTokens != 0 || u.CompletionTokens != 0 || u.TotalTokens != 0 {
		st.usage = mapUsage(u)
		st.hasUsage = true
	}

	if len(chunk.Choices) == 0 {
		return out, nil
	}
	choice := chunk.Choices[0]

	// After finish_reason, Chat Completions sends no further content/tool-call
	// deltas — guard defensively so a stray post-finish delta can't double-emit.
	if !st.finished {
		// reasoning_content (GLM/DeepSeek "thinking") is not in the openai-go typed
		// delta — read it from the raw JSON extra fields and emit ChunkReasoning.
		// DISPLAY-ONLY and non-committing (never replayed; Chat Completions is
		// stateless), but LOAD-BEARING for resilience: a mid-stream thinking phase
		// that yielded nothing would look like a stall to llmresilience's idle
		// watchdog. Emitting it keeps the watchdog fed while keeping the turn
		// retryable through the reasoning prefix (ChunkReasoning is non-committing).
		if rc := reasoningText(choice.Delta); rc != "" {
			out = append(out, port.Chunk{Kind: port.ChunkReasoning, Text: rc})
		}
		if choice.Delta.Content != "" {
			out = append(out, port.Chunk{Kind: port.ChunkText, Text: choice.Delta.Content})
		}
		for _, tc := range choice.Delta.ToolCalls {
			if err := st.mergeToolCall(tc); err != nil {
				return out, err
			}
		}
	}

	if choice.FinishReason != "" && !st.finished {
		st.finished = true
		st.stop = mapStop(choice.FinishReason)
	}
	return out, nil
}

// finalize emits the buffered terminal chunks once the stream ends cleanly AND a
// finish_reason was observed: the assembled tool calls (in arrival order), the
// usage chunk, then the terminal ChunkDone. Deferring these to stream end — rather
// than emitting them the instant finish_reason arrives — is what lets a standard
// trailing choices:[] usage frame be counted (the finish chunk itself often
// carries usage:null). It is called ONCE by Stream, and ONLY when st.finished is
// true — a clean EOF WITHOUT a finish_reason is a truncated stream, handled as
// errTruncatedStream (fail closed), never finalized. Callers MUST enforce that.
func finalize(st *streamState) []port.Chunk {
	out := make([]port.Chunk, 0, len(st.order)+2)
	for _, idx := range st.order {
		out = append(out, port.Chunk{Kind: port.ChunkToolCall, ToolCall: st.toolCalls[idx]})
	}
	if st.hasUsage {
		u := st.usage
		out = append(out, port.Chunk{Kind: port.ChunkUsage, Usage: &u})
	}
	out = append(out, port.Chunk{Kind: port.ChunkDone, Stop: st.stop})
	return out
}

// mergeToolCall accumulates one streamed tool-call fragment by its index: the
// first fragment for an index carries id + function name; later fragments append
// argument text. (OpenCode Go/GLM currently sends a whole call in one fragment;
// the accumulator handles both that degenerate case and spec-compliant
// multi-fragment streaming.)
func (st *streamState) mergeToolCall(tc oai.ChatCompletionChunkChoiceDeltaToolCall) error {
	if st.toolCalls == nil {
		st.toolCalls = make(map[int64]*session.ToolCall)
	}
	call, ok := st.toolCalls[tc.Index]
	if !ok {
		call = &session.ToolCall{}
		st.toolCalls[tc.Index] = call
		st.order = append(st.order, tc.Index)
	}
	if tc.ID != "" {
		call.ID = session.ToolCallID(tc.ID)
	}
	if tc.Function.Name != "" {
		call.Name = tc.Function.Name
	}
	if args := tc.Function.Arguments; args != "" {
		// Bound total accumulated arg bytes (a hostile endpoint could stream
		// fragments unbounded) — fail the stream rather than grow memory.
		if st.argBytes+len(args) > maxToolArgsBytes {
			return fmt.Errorf("openaichat: tool-call arguments exceeded %d bytes", maxToolArgsBytes)
		}
		st.argBytes += len(args)
		call.Args = append(call.Args, args...)
	}
	return nil
}

// reasoningText extracts the provider's reasoning_content delta from the raw-JSON
// extra fields (the openai-go typed delta has no such member). Returns "" when
// absent, null, or unparseable — fail-soft, never an error. See translate for why
// this is emitted (display + resilience idle-watchdog progress) yet never replayed.
func reasoningText(delta oai.ChatCompletionChunkChoiceDelta) string {
	f, ok := delta.JSON.ExtraFields["reasoning_content"]
	if !ok {
		return ""
	}
	// NOTE: Field.Valid() is false for EXTRA (non-typed) fields even when present,
	// so gate on Raw() instead: "" = omitted, "null" = JSON null, else a raw JSON
	// value (a quoted string) to unmarshal.
	raw := f.Raw()
	if raw == "" || raw == "null" {
		return ""
	}
	var rc string
	if err := json.Unmarshal([]byte(raw), &rc); err != nil {
		return ""
	}
	return rc
}

// mapUsage maps the Chat Completions usage totals into the neutral session.Usage,
// carrying the cached-tokens subset into CacheReadTokens and the reasoning-tokens
// subset into ReasoningTokens (both subsets of their inclusive totals). Absent
// detail sub-objects decode to zero values.
func mapUsage(u oai.CompletionUsage) session.Usage {
	return session.Usage{
		InputTokens:     int(u.PromptTokens),
		OutputTokens:    int(u.CompletionTokens),
		CacheReadTokens: int(u.PromptTokensDetails.CachedTokens),
		ReasoningTokens: int(u.CompletionTokensDetails.ReasoningTokens),
	}
}

// mapStop maps a Chat Completions finish_reason to a domain StopReason. "stop"
// and the tool-call reasons are a benign end (the loop decides whether tool calls
// mean it should continue); "length" (max tokens hit) and "content_filter" are
// terminal errors, mirroring the openai adapter's treatment of incomplete/failed.
func mapStop(reason string) session.StopReason {
	switch reason {
	case "stop", "tool_calls", "function_call":
		return session.StopEndTurn
	case "length", "content_filter":
		return session.StopError
	default:
		return session.StopEndTurn
	}
}
