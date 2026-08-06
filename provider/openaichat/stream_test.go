package openaichat

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	oai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// decodeSSE drives the REAL openai-go ssestream decoder over recorded SSE bytes
// and folds each chunk through translate. Using the SDK decoder (not a bespoke
// scanner) is deliberate: it proves translate composes with the exact production
// stream path, INCLUDING OpenCode Go's non-standard frames (the x-opencode-type
// cost frame and the frame after [DONE]) which the decoder must tolerate.
func decodeSSE(r io.Reader) ([]port.Chunk, error) {
	res := &http.Response{
		Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body:   io.NopCloser(r),
	}
	stream := ssestream.NewStream[oai.ChatCompletionChunk](ssestream.NewDecoder(res), nil)
	var st streamState
	var out []port.Chunk
	for stream.Next() {
		chunks, terr := translate(stream.Current(), &st)
		out = append(out, chunks...)
		if terr != nil {
			return out, terr
		}
	}
	if err := stream.Err(); err != nil {
		return out, err
	}
	// Mirror Stream: fail closed on a truncated stream (clean EOF, no finish_reason);
	// otherwise flush the buffered terminal via finalize.
	if !st.finished {
		return out, errTruncatedStream
	}
	out = append(out, finalize(&st)...)
	return out, nil
}

func decodeFixture(t *testing.T, name string) []port.Chunk {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	chunks, derr := decodeSSE(bytes.NewReader(data))
	if derr != nil {
		t.Fatalf("decodeSSE(%s) error = %v", name, derr)
	}
	return chunks
}

func joinText(chunks []port.Chunk) string {
	var b bytes.Buffer
	for _, c := range chunks {
		if c.Kind == port.ChunkText {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

func lastOfKind(chunks []port.Chunk, k port.ChunkKind) (port.Chunk, bool) {
	for i := len(chunks) - 1; i >= 0; i-- {
		if chunks[i].Kind == k {
			return chunks[i], true
		}
	}
	return port.Chunk{}, false
}

// TestTranslateTextStream folds the recorded plain-text stream (captured live
// from OpenCode Go glm-5.2 on 2026-07-17). It must yield the assistant text, a
// terminal usage chunk, and exactly one ChunkDone with a benign stop — and the
// non-standard cost/post-[DONE] frames must NOT produce an error or extra chunks.
func TestTranslateTextStream(t *testing.T) {
	chunks := decodeFixture(t, "stream_text.sse")

	if got := joinText(chunks); got != "1\n2\n3" {
		t.Errorf("assistant text = %q, want %q", got, "1\n2\n3")
	}

	done, ok := lastOfKind(chunks, port.ChunkDone)
	if !ok {
		t.Fatal("no ChunkDone emitted")
	}
	if done.Stop != session.StopEndTurn {
		t.Errorf("stop = %q, want %q", done.Stop, session.StopEndTurn)
	}
	// ChunkDone must be the LAST chunk (nothing emitted after the terminal).
	if chunks[len(chunks)-1].Kind != port.ChunkDone {
		t.Errorf("ChunkDone is not last; trailing kind = %v", chunks[len(chunks)-1].Kind)
	}
	// Exactly one terminal.
	var dones int
	for _, c := range chunks {
		if c.Kind == port.ChunkDone {
			dones++
		}
	}
	if dones != 1 {
		t.Errorf("ChunkDone count = %d, want 1", dones)
	}

	usage, ok := lastOfKind(chunks, port.ChunkUsage)
	if !ok || usage.Usage == nil {
		t.Fatal("no ChunkUsage emitted")
	}
	if usage.Usage.InputTokens != 15 {
		t.Errorf("InputTokens = %d, want 15", usage.Usage.InputTokens)
	}
	if usage.Usage.OutputTokens == 0 {
		t.Error("OutputTokens = 0, want non-zero")
	}
}

// TestTranslateToolCallStream folds the recorded tool-call stream. It must yield
// exactly one assembled ChunkToolCall (get_weather / {"city":"Paris"}) with the
// provider-assigned id, a usage chunk, and a benign terminal.
func TestTranslateToolCallStream(t *testing.T) {
	chunks := decodeFixture(t, "stream_toolcall.sse")

	var calls []*session.ToolCall
	for _, c := range chunks {
		if c.Kind == port.ChunkToolCall {
			calls = append(calls, c.ToolCall)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.Name != "get_weather" {
		t.Errorf("tool name = %q, want get_weather", call.Name)
	}
	if call.ID == "" {
		t.Error("tool call ID is empty (provider-assigned id lost)")
	}
	var args struct {
		City string `json:"city"`
	}
	if err := json.Unmarshal(call.Args, &args); err != nil {
		t.Fatalf("tool args %q not valid JSON: %v", string(call.Args), err)
	}
	if args.City != "Paris" {
		t.Errorf("args.city = %q, want Paris", args.City)
	}

	done, ok := lastOfKind(chunks, port.ChunkDone)
	if !ok || done.Stop != session.StopEndTurn {
		t.Errorf("terminal stop = %q (ok=%v), want end_turn", done.Stop, ok)
	}
	if _, ok := lastOfKind(chunks, port.ChunkUsage); !ok {
		t.Error("no ChunkUsage emitted for tool-call stream")
	}
}

// TestTranslateToolArgsSizeCap proves accumulated tool-call argument fragments are
// bounded: a compatible endpoint that streams unbounded argument bytes fails the
// stream instead of growing memory without limit (mirrors the anthropic guard).
func TestTranslateToolArgsSizeCap(t *testing.T) {
	var st streamState
	// Feed argument fragments well past the cap across many deltas on one index.
	frag := strings.Repeat("x", 1<<20) // 1 MiB per fragment
	var err error
	for i := 0; i < 16 && err == nil; i++ { // 16 MiB > 8 MiB cap
		chunk := oai.ChatCompletionChunk{Choices: []oai.ChatCompletionChunkChoice{{
			Delta: oai.ChatCompletionChunkChoiceDelta{ToolCalls: []oai.ChatCompletionChunkChoiceDeltaToolCall{{
				Index:    0,
				Function: oai.ChatCompletionChunkChoiceDeltaToolCallFunction{Arguments: frag},
			}}},
		}}}
		_, err = translate(chunk, &st)
	}
	if err == nil {
		t.Fatal("unbounded tool-args accumulation did not fail the stream")
	}
	if !strings.Contains(err.Error(), "exceeded") {
		t.Errorf("error = %v, want a size-cap error", err)
	}
}

// TestTranslateTruncatedStreamFailsClosed proves a stream that ends cleanly (no
// error frame, no [DONE]) but WITHOUT a finish_reason is surfaced as a truncation
// error — NOT a fabricated successful terminal — and buffered tool calls are NOT
// flushed. This is the fail-closed guard: a dropped connection after partial
// tool-call deltas must never become a tool-executing turn.
func TestTranslateTruncatedStreamFailsClosed(t *testing.T) {
	// A tool-call delta arrived, then the connection dropped before finish_reason.
	const sse = `data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]}}]}

`
	chunks, err := decodeSSE(bytes.NewReader([]byte(sse)))
	if err == nil {
		t.Fatal("truncated stream returned nil error (fail-open: it could execute a partial tool call)")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("error = %v, want it to wrap io.ErrUnexpectedEOF (retryable-when-safe truncation)", err)
	}
	for _, c := range chunks {
		if c.Kind == port.ChunkToolCall {
			t.Error("a tool call was flushed from a truncated stream (fail-open)")
		}
		if c.Kind == port.ChunkDone {
			t.Error("a terminal ChunkDone was fabricated for a truncated stream (fail-open)")
		}
	}
}

// TestTranslateEmitsReasoning proves reasoning_content deltas surface as
// ChunkReasoning — read from the raw-JSON extra fields since the SDK has no typed
// field. This is display parity AND the resilience-watchdog fix (a dropped
// reasoning stream looks like a stall). The captured fixture front-loads many
// reasoning_content frames before visible content.
func TestTranslateEmitsReasoning(t *testing.T) {
	chunks := decodeFixture(t, "stream_text.sse")
	var n int
	var b bytes.Buffer
	for _, c := range chunks {
		if c.Kind == port.ChunkReasoning {
			n++
			b.WriteString(c.Text)
		}
	}
	if n == 0 {
		t.Fatal("no ChunkReasoning emitted; reasoning_content was dropped (F1 regression)")
	}
	if b.Len() == 0 {
		t.Error("ChunkReasoning emitted but all text empty")
	}
}

// TestTranslateTrailingUsageChunk proves the standard OpenAI include_usage shape
// is counted: usage arrives in a trailing choices:[] chunk AFTER the finish chunk
// (which itself carries usage:null). The terminal ChunkDone must still come LAST,
// after that late usage. This is the F1 regression — the earlier emit-at-finish
// design dropped this usage.
func TestTranslateTrailingUsageChunk(t *testing.T) {
	const sse = `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}]}

data: {"choices":[{"index":0,"finish_reason":"stop","delta":{}}],"usage":null}

data: {"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":22,"total_tokens":33}}

data: [DONE]

`
	chunks, err := decodeSSE(bytes.NewReader([]byte(sse)))
	if err != nil {
		t.Fatalf("decodeSSE error = %v", err)
	}
	usage, ok := lastOfKind(chunks, port.ChunkUsage)
	if !ok || usage.Usage == nil {
		t.Fatal("no ChunkUsage emitted for the trailing-usage shape (F1 regression)")
	}
	if usage.Usage.InputTokens != 11 || usage.Usage.OutputTokens != 22 {
		t.Errorf("usage = %+v, want in=11 out=22", usage.Usage)
	}
	if done, ok := lastOfKind(chunks, port.ChunkDone); !ok || done.Stop != session.StopEndTurn {
		t.Errorf("terminal = %q ok=%v, want end_turn", done.Stop, ok)
	}
	if chunks[len(chunks)-1].Kind != port.ChunkDone {
		t.Error("ChunkDone must be the last chunk (emitted after the trailing usage)")
	}
}

// TestUsageCacheReadSubsetOfInput is the openaichat half of the cross-provider
// parity guard (the missing member — anthropic and openai already carry it):
// every Usage chunk produced from the recorded fixtures must satisfy
// CacheReadTokens <= InputTokens, the engine/session contract that
// CacheReadTokens ⊂ InputTokens. Fixtures are globbed so a newly recorded turn
// is covered automatically.
func TestUsageCacheReadSubsetOfInput(t *testing.T) {
	// Deliberately malformed / partial-stream fixtures: decodeFixture's
	// t.Fatalf on decode error would abort the happy-path glob loop.
	skip := map[string]bool{
		"stream_keepalive_ping.sse": true,
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

// TestTranslateMultiFragmentToolCall proves the index-keyed accumulator assembles
// a tool call whose arguments are FRAGMENTED across deltas — the spec-compliant
// shape that the live provider (which sends whole calls) does not exercise.
func TestTranslateMultiFragmentToolCall(t *testing.T) {
	const sse = `data: {"choices":[{"index":0,"finish_reason":null,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_frag","type":"function","function":{"name":"get_weather","arguments":"{\"city\":"}}]}}]}

data: {"choices":[{"index":0,"finish_reason":null,"delta":{"tool_calls":[{"index":0,"function":{"arguments":" \"Paris\"}"}}]}}]}

data: {"choices":[{"index":0,"finish_reason":"tool_calls","delta":{}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}

data: [DONE]

`
	chunks, err := decodeSSE(bytes.NewReader([]byte(sse)))
	if err != nil {
		t.Fatalf("decodeSSE error = %v", err)
	}
	var call *session.ToolCall
	for _, c := range chunks {
		if c.Kind == port.ChunkToolCall {
			call = c.ToolCall
		}
	}
	if call == nil {
		t.Fatal("no ChunkToolCall emitted")
	}
	if string(call.Args) != `{"city": "Paris"}` {
		t.Errorf("assembled args = %q, want %q", string(call.Args), `{"city": "Paris"}`)
	}
	if call.ID != "call_frag" || call.Name != "get_weather" {
		t.Errorf("call id/name = %q/%q, want call_frag/get_weather", call.ID, call.Name)
	}
}

// TestOpenAIChatStreamErrorPermanent exercises Permanent() on openaichatStreamError.
// Non-retryable 4xx (≠408/429) and context-overflow messages are permanent;
// transient codes (408, 429, 5xx) and unknown (0) are NOT permanent (fail-open).
func TestOpenAIChatStreamErrorPermanent(t *testing.T) {
	tests := []struct {
		msg    string
		status int
		want   bool
	}{
		// Non-retryable 4xx — permanent client-side rejections.
		{"request failed: 400 bad request", 400, true},
		{"request failed: 403 forbidden", 403, true},
		{"request failed: 404 not found", 404, true},
		// Retryable codes — transient, NOT permanent.
		{"request failed: 429 too many requests", 429, false},
		{"request failed: 503 service unavailable", 503, false},
		{"request failed: 500 internal server error", 500, false},
		// Status 0 (unknown) — NOT permanent, fail-open.
		{"request failed: unknown error", 0, false},
		{"", 0, false},
		// Context overflow — permanent even with transient-looking status.
		{"request failed: 500 Your input exceeds the context window of this model.", 500, true},
		{"request failed: 500 input exceeds the context length", 500, true},
		{"request failed: 500 maximum context length exceeded", 500, true},
		{"request failed: 500 prompt exceeds the token limit", 500, true},
		{"request failed: 500 request exceeded the token limit for this model", 500, true},
	}
	for _, tt := range tests {
		e := &openaichatStreamError{msg: tt.msg, status: tt.status}
		if got := e.Permanent(); got != tt.want {
			t.Errorf("Permanent() = %v for msg=%q status=%d, want %v", got, tt.msg, tt.status, tt.want)
		}
	}
}

// TestOpenAIChatStreamErrorErrorMessageUnchanged pins the invariant that wrapping
// does not change the Error() string.
func TestOpenAIChatStreamErrorErrorMessageUnchanged(t *testing.T) {
	e := &openaichatStreamError{msg: "request failed: 429 too many requests", status: 429}
	if got := e.Error(); got != "request failed: 429 too many requests" {
		t.Errorf("Error() = %q, want unchanged message", got)
	}
}

// TestOpenAIChatStreamErrorUnwrap verifies Unwrap returns the inner error.
func TestOpenAIChatStreamErrorUnwrap(t *testing.T) {
	inner := errors.New("inner transport error")
	e := &openaichatStreamError{err: inner, msg: "wrapped: " + inner.Error(), status: 0}
	if !errors.Is(e, inner) {
		t.Error("Unwrap should reach the inner error")
	}
}
