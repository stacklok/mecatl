package anthropic

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func decodeFixture(t *testing.T, name string) []port.Chunk {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	chunks, err := decodeSSE(f)
	if err != nil {
		t.Fatalf("decodeSSE: %v", err)
	}
	return chunks
}

func decodeFixtureErr(t *testing.T, name string) ([]port.Chunk, error) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	return decodeSSE(f)
}

func assertChunks(t *testing.T, got, want []port.Chunk) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("chunk count = %d, want %d\n got: %+v\nwant: %+v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i].Kind != want[i].Kind {
			t.Errorf("chunk[%d].Kind = %v, want %v", i, got[i].Kind, want[i].Kind)
		}
		if got[i].Text != want[i].Text {
			t.Errorf("chunk[%d].Text = %q, want %q", i, got[i].Text, want[i].Text)
		}
		if !reflect.DeepEqual(got[i].ToolCall, want[i].ToolCall) {
			t.Errorf("chunk[%d].ToolCall = %+v, want %+v", i, got[i].ToolCall, want[i].ToolCall)
		}
		if !reflect.DeepEqual(got[i].Usage, want[i].Usage) {
			t.Errorf("chunk[%d].Usage = %+v, want %+v", i, got[i].Usage, want[i].Usage)
		}
		if got[i].Stop != want[i].Stop {
			t.Errorf("chunk[%d].Stop = %v, want %v", i, got[i].Stop, want[i].Stop)
		}
	}
}

func TestTranslateTextTurn(t *testing.T) {
	got := decodeFixture(t, "text_turn.sse")
	want := []port.Chunk{
		{Kind: port.ChunkText, Text: "Hello"},
		{Kind: port.ChunkText, Text: ", world"},
		{Kind: port.ChunkUsage, Usage: &session.Usage{InputTokens: 12, OutputTokens: 4, CacheReadTokens: 0}},
		{Kind: port.ChunkDone, Stop: session.StopEndTurn},
	}
	assertChunks(t, got, want)
}

func TestTranslateToolCallTurn(t *testing.T) {
	got := decodeFixture(t, "tool_call_turn.sse")
	want := []port.Chunk{
		{Kind: port.ChunkText, Text: "Let me read it."},
		{Kind: port.ChunkToolCall, ToolCall: &session.ToolCall{
			ID:   "toolu_abc",
			Name: "read_file",
			Args: json.RawMessage(`{"path":"main.go"}`),
		}},
		// Raw input_tokens:40 + cache_read_input_tokens:32 fold into the full-prompt
		// InputTokens (Anthropic reports input_tokens EXCLUDING cache reads/writes;
		// the adapter normalizes so CacheReadTokens ⊂ InputTokens, matching OpenAI).
		{Kind: port.ChunkUsage, Usage: &session.Usage{InputTokens: 72, OutputTokens: 9, CacheReadTokens: 32}},
		{Kind: port.ChunkDone, Stop: session.StopEndTurn},
	}
	assertChunks(t, got, want)
}

// TestTranslateCacheWriteTurn exercises cache_creation_input_tokens: the cache
// write maps to CacheWriteTokens AND is folded into InputTokens alongside the
// cache read, so InputTokens is the full billed prompt (raw 10 + read 20 +
// write 30 = 60) and CacheReadTokens stays a true subset.
func TestTranslateCacheWriteTurn(t *testing.T) {
	got := decodeFixture(t, "cache_write_turn.sse")
	want := []port.Chunk{
		{Kind: port.ChunkText, Text: "Cached."},
		{Kind: port.ChunkUsage, Usage: &session.Usage{
			InputTokens:      60,
			OutputTokens:     5,
			CacheReadTokens:  20,
			CacheWriteTokens: 30,
		}},
		{Kind: port.ChunkDone, Stop: session.StopEndTurn},
	}
	assertChunks(t, got, want)
}

// TestTranslateCacheDeltaTurn exercises the message_delta cache-token capture
// path: Anthropic's real streams ride the final cumulative usage on
// message_delta (absolute totals, overwrite semantics), so a stream whose
// message_start carries cache read/write 0 must still end with the DELTA's
// cache values. Final Usage: InputTokens = raw 8 + delta read 25 + delta
// write 15 = 48; CacheReadTokens/CacheWriteTokens are the delta values.
func TestTranslateCacheDeltaTurn(t *testing.T) {
	got := decodeFixture(t, "cache_delta_turn.sse")
	want := []port.Chunk{
		{Kind: port.ChunkText, Text: "From delta."},
		{Kind: port.ChunkUsage, Usage: &session.Usage{
			InputTokens:      48,
			OutputTokens:     6,
			CacheReadTokens:  25,
			CacheWriteTokens: 15,
		}},
		{Kind: port.ChunkDone, Stop: session.StopEndTurn},
	}
	assertChunks(t, got, want)
}

// TestUsageCacheReadSubsetOfInput is the anthropic half of the cross-provider
// parity guard: every Usage chunk produced from the recorded fixtures must
// satisfy CacheReadTokens <= InputTokens (the engine/session contract that
// CacheReadTokens ⊂ InputTokens — Anthropic's raw input_tokens EXCLUDES cache
// tokens, so this fails if the adapter stops folding them in). The openai
// package carries the identical assertion over its own fixtures. Fixtures are
// globbed so a newly recorded turn is covered automatically.
func TestUsageCacheReadSubsetOfInput(t *testing.T) {
	// Deliberately malformed / error-path fixtures: decodeSSE returns a
	// terminal error for these (exercised via decodeFixtureErr elsewhere),
	// so the happy-path helper can't decode them.
	skip := map[string]bool{
		"error_event.sse":     true,
		"two_text_blocks.sse": true,
	}
	paths, err := filepath.Glob(filepath.Join("testdata", "*.sse"))
	if err != nil {
		t.Fatalf("glob fixtures: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no .sse fixtures found under testdata")
	}
	sawCacheRead := false
	for _, path := range paths {
		name := filepath.Base(path)
		if skip[name] {
			continue
		}
		chunks := decodeFixture(t, name)
		for _, c := range chunks {
			if c.Kind != port.ChunkUsage {
				continue
			}
			if c.Usage.CacheReadTokens > 0 {
				sawCacheRead = true
			}
			if c.Usage.CacheReadTokens > c.Usage.InputTokens {
				t.Errorf("%s: CacheReadTokens %d > InputTokens %d — cache reads must be a subset of the full prompt",
					name, c.Usage.CacheReadTokens, c.Usage.InputTokens)
			}
		}
	}
	// Vacuity guard: if a fixture refresh drops every cache-bearing turn, the
	// subset assertion above is trivially green — fail loudly instead.
	if !sawCacheRead {
		t.Error("no fixture yielded CacheReadTokens > 0 — the subset guard is vacuous; keep at least one cache-bearing fixture")
	}
}

// TestTranslateThinkingToolTurn is the streaming half of the 400-trap round-trip:
// a thinking block (thinking deltas + a signature delta) precedes a tool_use, and
// the adapter emits the DISPLAY reasoning deltas, ONE packed ChunkReasoningItem
// (the replay blob), and the assembled ChunkToolCall.
func TestTranslateThinkingToolTurn(t *testing.T) {
	got := decodeFixture(t, "thinking_tool_turn.sse")

	// Display reasoning deltas first.
	if got[0].Kind != port.ChunkReasoning || got[0].Text != "I should " {
		t.Fatalf("chunk[0] = %+v, want ChunkReasoning 'I should '", got[0])
	}
	if got[1].Kind != port.ChunkReasoning || got[1].Text != "read the file." {
		t.Fatalf("chunk[1] = %+v, want ChunkReasoning 'read the file.'", got[1])
	}
	// Tool call assembled at its content_block_stop.
	if got[2].Kind != port.ChunkToolCall || got[2].ToolCall.Name != "read_file" {
		t.Fatalf("chunk[2] = %+v, want ChunkToolCall read_file", got[2])
	}
	if string(got[2].ToolCall.Args) != `{"path":"main.go"}` {
		t.Fatalf("tool args = %q", got[2].ToolCall.Args)
	}
	// The packed reasoning replay blob is emitted ONCE at message_stop.
	if got[3].Kind != port.ChunkReasoningItem {
		t.Fatalf("chunk[3].Kind = %v, want ChunkReasoningItem", got[3].Kind)
	}
	blocks := unpackReasoning(got[3].Text)
	if len(blocks) != 1 || blocks[0].Kind != reasoningKindThinking {
		t.Fatalf("packed reasoning = %+v, want one thinking block", blocks)
	}
	if blocks[0].Thinking != "I should read the file." {
		t.Errorf("packed thinking text = %q", blocks[0].Thinking)
	}
	if blocks[0].Signature != "SIG-abc123==" {
		t.Errorf("packed signature = %q, want SIG-abc123==", blocks[0].Signature)
	}
	// Then usage + done.
	if got[4].Kind != port.ChunkUsage {
		t.Fatalf("chunk[4].Kind = %v, want ChunkUsage", got[4].Kind)
	}
	if got[5].Kind != port.ChunkDone || got[5].Stop != session.StopEndTurn {
		t.Fatalf("chunk[5] = %+v, want ChunkDone StopEndTurn", got[5])
	}
}

func TestTranslateRedactedThinkingTurn(t *testing.T) {
	got := decodeFixture(t, "redacted_thinking_turn.sse")
	// text delta, then packed reasoning item, usage, done.
	if got[0].Kind != port.ChunkText || got[0].Text != "Done." {
		t.Fatalf("chunk[0] = %+v, want ChunkText 'Done.'", got[0])
	}
	if got[1].Kind != port.ChunkReasoningItem {
		t.Fatalf("chunk[1].Kind = %v, want ChunkReasoningItem", got[1].Kind)
	}
	blocks := unpackReasoning(got[1].Text)
	if len(blocks) != 1 || blocks[0].Kind != reasoningKindRedacted {
		t.Fatalf("packed reasoning = %+v, want one redacted block", blocks)
	}
	if blocks[0].Data != "REDACTED-OPAQUE-DATA==" {
		t.Errorf("packed redacted data = %q", blocks[0].Data)
	}
}

// TestTranslateReasoningTokensFromMessageDelta is a SYNTHETIC test (no live
// re-recording) for the thinking_tokens → ReasoningTokens capture: a
// message_delta carrying output_tokens_details.thinking_tokens must land the
// value in ReasoningTokens on the terminal Usage chunk. It mirrors the
// overwrite-if-nonzero semantics of outputTokens (the delta's cumulative total
// replaces the message_start seed).
func TestTranslateReasoningTokensFromMessageDelta(t *testing.T) {
	var st streamState
	// message_start seeds input/output; reasoning comes from the delta here.
	start := sdk.MessageStreamEventUnion{Type: "message_start"}
	start.Message.Usage.InputTokens = 100
	start.Message.Usage.OutputTokens = 5
	if _, err := translate(start, &st); err != nil {
		t.Fatalf("message_start: %v", err)
	}
	// message_delta carries the final cumulative usage incl. thinking_tokens.
	delta := sdk.MessageStreamEventUnion{Type: "message_delta"}
	delta.Delta.StopReason = sdk.StopReasonEndTurn
	delta.Usage.OutputTokens = 50
	delta.Usage.OutputTokensDetails.ThinkingTokens = 40
	if _, err := translate(delta, &st); err != nil {
		t.Fatalf("message_delta: %v", err)
	}
	// message_stop emits the terminal ChunkUsage.
	chunks, err := translate(sdk.MessageStreamEventUnion{Type: "message_stop"}, &st)
	if err != nil {
		t.Fatalf("message_stop: %v", err)
	}
	var usage *session.Usage
	for _, c := range chunks {
		if c.Kind == port.ChunkUsage {
			usage = c.Usage
		}
	}
	if usage == nil {
		t.Fatal("no ChunkUsage emitted at message_stop")
		return
	}
	if usage.ReasoningTokens != 40 {
		t.Errorf("ReasoningTokens = %d, want 40 (from output_tokens_details.thinking_tokens)", usage.ReasoningTokens)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want 50 (unchanged)", usage.OutputTokens)
	}
	// Subset guard: reasoning must not exceed the inclusive output total.
	if usage.ReasoningTokens > usage.OutputTokens {
		t.Errorf("ReasoningTokens %d > OutputTokens %d — must be a subset", usage.ReasoningTokens, usage.OutputTokens)
	}
}

// TestUsageReasoningSubsetOfOutput is the anthropic reasoning half of the
// cross-provider parity guard: every Usage chunk produced from the recorded
// fixtures must satisfy ReasoningTokens <= OutputTokens. None of the current
// fixtures carry thinking_tokens (so the vacuity guard is NOT asserted here —
// the synthetic test above covers the positive case), but the subset invariant
// is still pinned so a future fixture with thinking_tokens can't regress it.
func TestUsageReasoningSubsetOfOutput(t *testing.T) {
	skip := map[string]bool{
		"error_event.sse":     true,
		"two_text_blocks.sse": true,
	}
	paths, err := filepath.Glob(filepath.Join("testdata", "*.sse"))
	if err != nil {
		t.Fatalf("glob fixtures: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no .sse fixtures found under testdata")
	}
	for _, path := range paths {
		name := filepath.Base(path)
		if skip[name] {
			continue
		}
		chunks := decodeFixture(t, name)
		for _, c := range chunks {
			if c.Kind != port.ChunkUsage {
				continue
			}
			if c.Usage.ReasoningTokens > c.Usage.OutputTokens {
				t.Errorf("%s: ReasoningTokens %d > OutputTokens %d — reasoning must be a subset of the inclusive output total",
					name, c.Usage.ReasoningTokens, c.Usage.OutputTokens)
			}
		}
	}
}

func TestTranslateErrorEvent(t *testing.T) {
	_, err := decodeFixtureErr(t, "error_event.sse")
	if err == nil {
		t.Fatal("expected a terminal error from the error event")
	}
	if got := err.Error(); got != "stream error: overloaded_error: Overloaded" {
		t.Errorf("error = %q", got)
	}
}

func TestTranslateMaxTokensStop(t *testing.T) {
	got := decodeFixture(t, "max_tokens_turn.sse")
	last := got[len(got)-1]
	if last.Kind != port.ChunkDone || last.Stop != session.StopError {
		t.Fatalf("terminal chunk = %+v, want ChunkDone StopError (max_tokens)", last)
	}
}

// TestTranslateToolArgsSplitAcrossDeltas guards the accumulator's one corruption
// point: a tool_use whose JSON is split across THREE input_json_delta events must
// reassemble byte-exact.
func TestTranslateToolArgsSplitAcrossDeltas(t *testing.T) {
	got := decodeFixture(t, "tool_call_split_args.sse")
	var call *session.ToolCall
	for _, c := range got {
		if c.Kind == port.ChunkToolCall {
			call = c.ToolCall
		}
	}
	if call == nil {
		t.Fatal("no tool call assembled")
		return
	}
	want := `{"path":"a/b.go","content":"package main"}`
	if string(call.Args) != want {
		t.Fatalf("reassembled args = %q, want %q", call.Args, want)
	}
}

// TestTranslateEmptyArgsToolCall: a tool_use with no input_json_delta yields an
// empty JSON object {} (a valid argument-less call).
func TestTranslateEmptyArgsToolCall(t *testing.T) {
	got := decodeFixture(t, "tool_call_empty_args.sse")
	var call *session.ToolCall
	for _, c := range got {
		if c.Kind == port.ChunkToolCall {
			call = c.ToolCall
		}
	}
	if call == nil {
		t.Fatal("no tool call assembled")
		return
	}
	if string(call.Args) != "{}" {
		t.Fatalf("empty-args call Args = %q, want {}", call.Args)
	}
}

// TestTranslateTwoTextBlocksTripwire: two distinct assistant text blocks in one
// turn is a LOUD error (the domain Message.Text is a single string).
func TestTranslateTwoTextBlocksTripwire(t *testing.T) {
	_, err := decodeFixtureErr(t, "two_text_blocks.sse")
	if err == nil {
		t.Fatal("expected a loud error for two distinct text blocks in one turn")
	}
	if !strings.Contains(err.Error(), "single visible text part") &&
		!strings.Contains(err.Error(), "second assistant text block") {
		t.Fatalf("error = %q, want the multi-text tripwire", err)
	}
}

// TestStreamBufferCap proves the per-block accumulation is bounded: a flood of
// input_json_delta fragments exceeding maxBlockBufBytes fails the translation
// rather than growing unbounded (cheap MITM/DoS hardening).
func TestStreamBufferCap(t *testing.T) {
	var st streamState
	start := sdk.MessageStreamEventUnion{Type: "content_block_start", Index: 0}
	start.ContentBlock.Type = "tool_use"
	start.ContentBlock.ID = "toolu_flood"
	start.ContentBlock.Name = "x"
	if _, err := translate(start, &st); err != nil {
		t.Fatalf("block start: %v", err)
	}
	chunk := strings.Repeat("a", 1<<20) // 1 MiB
	var capErr error
	for i := 0; i < (maxBlockBufBytes/(1<<20))+2; i++ {
		ev := sdk.MessageStreamEventUnion{Type: "content_block_delta", Index: 0}
		ev.Delta.Type = "input_json_delta"
		ev.Delta.PartialJSON = chunk
		if _, err := translate(ev, &st); err != nil {
			capErr = err
			break
		}
	}
	if capErr == nil {
		t.Fatal("tool-args buffer grew past the cap without failing the stream")
	}
	if !strings.Contains(capErr.Error(), "buffer exceeded") {
		t.Fatalf("cap error = %q, want a buffer-exceeded error", capErr)
	}
}
