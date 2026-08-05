package anthropic

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// blockKind identifies what a streaming content block (keyed by SSE index) is
// accumulating across deltas.
type blockKind int

const (
	blockNone blockKind = iota
	blockText
	blockToolUse
	blockThinking
	blockRedacted
)

// maxBlockBufBytes bounds the per-block accumulation of streamed tool-args JSON
// and thinking text. A malicious/buggy upstream (or a MITM on a non-TLS proxy)
// must not grow these unbounded; exceeding the cap fails the stream rather than
// OOMing the process. 8 MiB is generous for any real tool call / thinking summary
// yet a hard ceiling — consistent with the openrouter lister's LimitReader cap
// and the test-only decodeSSE scanner cap.
const maxBlockBufBytes = 8 << 20

// blockState is the per-index accumulator. Unlike the OpenAI Responses stream
// (mostly self-describing per event), the Anthropic stream REQUIRES assembly
// across deltas keyed by index: tool_use args stream as input_json_delta
// fragments, and a thinking block's signature arrives once via signature_delta
// near the block end.
type blockState struct {
	kind blockKind
	// tool_use accumulation.
	toolID   string
	toolName string
	argsBuf  strings.Builder
	// thinking accumulation.
	thinkingBuf strings.Builder
	signature   string
	// redacted_thinking payload.
	redactedData string
}

// streamState carries the state the Anthropic SSE→Chunk translation needs across
// events: the per-index block accumulators, the running usage (input tokens
// arrive in message_start, output tokens in message_delta), the final
// stop_reason, the ordered reasoning blocks assembled across the turn (packed
// into ONE ChunkReasoningItem at message_stop), a single-visible-text-part
// guard, and a done guard.
type streamState struct {
	blocks map[int64]*blockState

	inputTokens      int64
	outputTokens     int64
	cacheReadTokens  int64
	cacheWriteTokens int64
	// reasoningTokens is the cumulative count of output tokens spent on internal
	// reasoning (Anthropic's output_tokens_details.thinking_tokens) — a subset of
	// outputTokens (providers bill reasoning as part of the inclusive output
	// total). Captured from both message_start (initial seed) and message_delta
	// (final cumulative, overwrite-if-nonzero) mirroring outputTokens.
	reasoningTokens int64
	stopReason      sdk.StopReason

	// reasoning is the ordered list of thinking/redacted blocks assembled across
	// the turn; packed into one ChunkReasoningItem at message_stop so the port's
	// "one opaque blob per message" contract holds even with interleaved thinking.
	reasoning []reasoningBlock

	// textBlockIndex pins the single visible assistant text block. The domain
	// Message.Text is one string, so a SECOND distinct text block is a loud error
	// rather than a silent fusion (mirrors the openai adapter's tripwire).
	textBlockIndex int64
	textIndexSet   bool

	done bool
}

func (s *streamState) block(index int64) *blockState {
	if s.blocks == nil {
		s.blocks = map[int64]*blockState{}
	}
	b, ok := s.blocks[index]
	if !ok {
		b = &blockState{}
		s.blocks[index] = b
	}
	return b
}

// translate converts a single Anthropic Messages SSE event into zero or more
// provider-neutral chunks. It is pure (apart from the carried streamState) so it
// can be driven directly from recorded fixtures in tests, with no real client.
//
// Mapping (verified against the live streaming docs, 2026-06-06):
//   - message_start                          -> record input/cache-read tokens
//   - content_block_start (text)             -> mark index; single-text guard
//   - content_block_start (tool_use)         -> stash id+name
//   - content_block_start (thinking)         -> mark index
//   - content_block_start (redacted_thinking)-> stash data
//   - content_block_delta text_delta         -> ChunkText
//   - content_block_delta input_json_delta   -> accumulate tool args
//   - content_block_delta thinking_delta     -> ChunkReasoning (DISPLAY) + accumulate
//   - content_block_delta signature_delta    -> accumulate signature
//   - content_block_stop (tool_use)          -> ChunkToolCall (assembled)
//   - content_block_stop (thinking/redacted) -> append to reasoning list
//   - message_delta                          -> record stop_reason + output tokens
//   - message_stop                           -> ChunkReasoningItem (packed)? +
//     ChunkUsage + ChunkDone
//
// The reasoning DISPLAY deltas (ChunkReasoning) and the reasoning REPLAY blob
// (ChunkReasoningItem) are deliberately distinct and MUST NOT be conflated: the
// deltas are human-readable prose; the replay blob is the packed
// {thinking,signature}+redacted envelope sent back verbatim (request.go).
func translate(event sdk.MessageStreamEventUnion, st *streamState) ([]port.Chunk, error) {
	switch event.Type {
	case "message_start":
		st.inputTokens = event.Message.Usage.InputTokens
		st.cacheReadTokens = event.Message.Usage.CacheReadInputTokens
		st.cacheWriteTokens = event.Message.Usage.CacheCreationInputTokens
		if event.Message.Usage.OutputTokensDetails.ThinkingTokens != 0 {
			st.reasoningTokens = event.Message.Usage.OutputTokensDetails.ThinkingTokens
		}
		return nil, nil

	case "content_block_start":
		return translateBlockStart(event, st)

	case "content_block_delta":
		return translateBlockDelta(event, st)

	case "content_block_stop":
		return translateBlockStop(event, st)

	case "message_delta":
		if event.Delta.StopReason != "" {
			st.stopReason = event.Delta.StopReason
		}
		if event.Usage.OutputTokens != 0 {
			st.outputTokens = event.Usage.OutputTokens
		}
		if event.Usage.CacheReadInputTokens != 0 {
			st.cacheReadTokens = event.Usage.CacheReadInputTokens
		}
		if event.Usage.CacheCreationInputTokens != 0 {
			st.cacheWriteTokens = event.Usage.CacheCreationInputTokens
		}
		if event.Usage.OutputTokensDetails.ThinkingTokens != 0 {
			st.reasoningTokens = event.Usage.OutputTokensDetails.ThinkingTokens
		}
		return nil, nil

	case "message_stop":
		return translateMessageStop(st)

	case "error":
		if st.done {
			return nil, nil
		}
		st.done = true
		return nil, fmt.Errorf("stream error: %s", eventErrorString(event))

	default:
		// ping and any unknown/forward-compatible event: ignore.
		return nil, nil
	}
}

func translateBlockStart(event sdk.MessageStreamEventUnion, st *streamState) ([]port.Chunk, error) {
	cb := event.ContentBlock
	b := st.block(event.Index)
	switch cb.Type {
	case "text":
		b.kind = blockText
		if !st.textIndexSet {
			st.textBlockIndex = event.Index
			st.textIndexSet = true
		} else if event.Index != st.textBlockIndex {
			return nil, fmt.Errorf(
				"anthropic: multiple assistant text blocks in one turn not supported "+
					"(first index %d, then index %d); the harness models a single visible text part per turn",
				st.textBlockIndex, event.Index)
		}
		return nil, nil
	case "tool_use":
		b.kind = blockToolUse
		b.toolID = cb.ID
		b.toolName = cb.Name
		return nil, nil
	case "thinking":
		b.kind = blockThinking
		// A thinking block may carry initial text/signature on start (rare); seed.
		if cb.Thinking != "" {
			b.thinkingBuf.WriteString(cb.Thinking)
		}
		if cb.Signature != "" {
			b.signature = cb.Signature
		}
		return nil, nil
	case "redacted_thinking":
		b.kind = blockRedacted
		b.redactedData = cb.Data
		return nil, nil
	default:
		b.kind = blockNone
		return nil, nil
	}
}

func translateBlockDelta(event sdk.MessageStreamEventUnion, st *streamState) ([]port.Chunk, error) {
	b := st.block(event.Index)
	switch event.Delta.Type {
	case "text_delta":
		if event.Delta.Text == "" {
			return nil, nil
		}
		// Enforce the single-visible-text-part guard against a delta on a second
		// text block (a start may be skipped in malformed streams).
		if st.textIndexSet && event.Index != st.textBlockIndex {
			return nil, fmt.Errorf(
				"anthropic: text delta on a second assistant text block (index %d != %d) not supported",
				event.Index, st.textBlockIndex)
		}
		if !st.textIndexSet {
			st.textBlockIndex = event.Index
			st.textIndexSet = true
		}
		return []port.Chunk{{Kind: port.ChunkText, Text: event.Delta.Text}}, nil
	case "input_json_delta":
		if b.argsBuf.Len()+len(event.Delta.PartialJSON) > maxBlockBufBytes {
			return nil, fmt.Errorf("anthropic: tool-args buffer exceeded %d bytes", maxBlockBufBytes)
		}
		b.argsBuf.WriteString(event.Delta.PartialJSON)
		return nil, nil
	case "thinking_delta":
		if event.Delta.Thinking == "" {
			return nil, nil
		}
		if b.thinkingBuf.Len()+len(event.Delta.Thinking) > maxBlockBufBytes {
			return nil, fmt.Errorf("anthropic: thinking buffer exceeded %d bytes", maxBlockBufBytes)
		}
		b.thinkingBuf.WriteString(event.Delta.Thinking)
		// DISPLAY-only prose delta.
		return []port.Chunk{{Kind: port.ChunkReasoning, Text: event.Delta.Thinking}}, nil
	case "signature_delta":
		if len(b.signature)+len(event.Delta.Signature) > maxBlockBufBytes {
			return nil, fmt.Errorf("anthropic: signature buffer exceeded %d bytes", maxBlockBufBytes)
		}
		b.signature += event.Delta.Signature
		return nil, nil
	default:
		// citations_delta and any unknown delta: ignore.
		return nil, nil
	}
}

func translateBlockStop(event sdk.MessageStreamEventUnion, st *streamState) ([]port.Chunk, error) {
	b := st.block(event.Index)
	switch b.kind {
	case blockToolUse:
		args := b.argsBuf.String()
		if args == "" {
			// An argument-less tool call is a valid empty object.
			args = "{}"
		}
		call := session.ToolCall{
			ID:   session.ToolCallID(b.toolID),
			Name: b.toolName,
			Args: json.RawMessage(args),
		}
		return []port.Chunk{{Kind: port.ChunkToolCall, ToolCall: &call}}, nil
	case blockThinking:
		st.reasoning = append(st.reasoning, reasoningBlock{
			Kind:      reasoningKindThinking,
			Thinking:  b.thinkingBuf.String(),
			Signature: b.signature,
		})
		return nil, nil
	case blockRedacted:
		st.reasoning = append(st.reasoning, reasoningBlock{
			Kind: reasoningKindRedacted,
			Data: b.redactedData,
		})
		return nil, nil
	default:
		return nil, nil
	}
}

// translateMessageStop emits the turn's terminal chunks: the packed reasoning
// replay blob (one ChunkReasoningItem carrying the ordered envelope, when the
// turn produced any thinking/redacted blocks), then ChunkUsage, then ChunkDone.
func translateMessageStop(st *streamState) ([]port.Chunk, error) {
	if st.done {
		return nil, nil
	}
	st.done = true
	var chunks []port.Chunk
	if packed := packReasoning(st.reasoning); packed != "" {
		chunks = append(chunks, port.Chunk{Kind: port.ChunkReasoningItem, Text: packed})
	}
	// Anthropic reports input_tokens EXCLUDING cache reads/writes; we fold them
	// in so InputTokens is the full prompt and CacheReadTokens ⊂ InputTokens,
	// matching OpenAI and the engine/session usage contract. Cache writes are
	// part of the prompt (and billed), so they belong in the full-prompt total.
	usage := session.Usage{
		InputTokens:      int(st.inputTokens + st.cacheReadTokens + st.cacheWriteTokens),
		OutputTokens:     int(st.outputTokens),
		CacheReadTokens:  int(st.cacheReadTokens),
		CacheWriteTokens: int(st.cacheWriteTokens),
		ReasoningTokens:  int(st.reasoningTokens),
	}
	chunks = append(chunks,
		port.Chunk{Kind: port.ChunkUsage, Usage: &usage},
		port.Chunk{Kind: port.ChunkDone, Stop: mapStop(st.stopReason)},
	)
	return chunks, nil
}

// mapStop maps an Anthropic stop_reason to a domain StopReason. A natural stop
// (end_turn / stop_sequence) and a tool_use stop both map to StopEndTurn — the
// loop decides to continue when the turn carried tool calls (mirrors the openai
// adapter's completed-with-calls handling). pause_turn is treated as end in P1
// (a long-running-turn continuation is deferred). max_tokens and refusal map to
// StopError so the run is diagnosable.
func mapStop(reason sdk.StopReason) session.StopReason {
	switch reason {
	case sdk.StopReasonEndTurn, sdk.StopReasonToolUse, sdk.StopReasonStopSequence, sdk.StopReasonPauseTurn:
		return session.StopEndTurn
	case sdk.StopReasonMaxTokens, sdk.StopReasonRefusal:
		return session.StopError
	default:
		return session.StopEndTurn
	}
}

// eventErrorString renders a top-level "error" event. The SDK's flattened union
// does not surface the nested error payload on MessageStreamEventUnion, so the
// raw JSON is parsed for the message/type.
func eventErrorString(event sdk.MessageStreamEventUnion) string {
	var payload struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if raw := event.RawJSON(); raw != "" {
		if err := json.Unmarshal([]byte(raw), &payload); err == nil {
			switch {
			case payload.Error.Type != "" && payload.Error.Message != "":
				return fmt.Sprintf("%s: %s", payload.Error.Type, payload.Error.Message)
			case payload.Error.Message != "":
				return payload.Error.Message
			case payload.Error.Type != "":
				return payload.Error.Type
			}
		}
	}
	return "unknown error"
}

// decodeSSE reads an Anthropic SSE byte stream and translates it into a flat
// slice of chunks, applying translate to each event in order. It exists so tests
// can drive the exact translation path from a recorded fixture with no real
// client. Lines are parsed as "data: <json>" records; "event:" lines are ignored
// because each event's JSON carries its own "type".
func decodeSSE(r io.Reader) ([]port.Chunk, error) {
	var out []port.Chunk
	var st streamState
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		var event sdk.MessageStreamEventUnion
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return nil, err
		}
		chunks, terr := translate(event, &st)
		out = append(out, chunks...)
		if terr != nil {
			return out, terr
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
