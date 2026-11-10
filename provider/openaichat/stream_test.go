package openaichat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	oai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
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

	var calls []*port.LLMToolCall
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

// TestChatStreamRequiresFinishReason drives HTTP, the SDK, and the production
// EOF guard. A [DONE] sentinel alone does not make a tool-bearing turn complete.
func TestChatStreamRequiresFinishReason(t *testing.T) {
	const text = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"tentative\"}}]}\n\n"
	const call = "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_x\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{\\\"key\\\":\\\"a\\\"}\"}}]}}]}\n\n"
	const finish = "data: {\"choices\":[{\"index\":0,\"finish_reason\":\"tool_calls\",\"delta\":{}}]}\n\n"
	const done = "data: [DONE]\n\n"
	for _, tc := range []struct {
		name     string
		body     string
		complete bool
		calls    int
	}{
		{name: "empty"},
		{name: "done-only", body: done},
		{name: "text-eof", body: text},
		{name: "tool-eof", body: text + call},
		{name: "done-without-finish", body: text + call + done},
		{name: "complete-tool", body: text + call + finish + done, complete: true, calls: 1},
		{name: "finish-without-done", body: text + call + finish, complete: true, calls: 1},
		{name: "complete-text", body: text + strings.ReplaceAll(finish, "tool_calls", "stop") + done, complete: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			p := New(WithAPIKey("synthetic"), WithBaseURL(srv.URL))
			seq, err := p.Stream(t.Context(), port.LLMRequest{Model: "test", Messages: []port.LLMMessage{{Role: session.RoleUser, Text: "lookup"}}})
			if err != nil {
				t.Fatal(err)
			}
			var streamErr error
			calls, dones := 0, 0
			for chunk, err := range seq {
				if err != nil {
					if streamErr != nil {
						t.Fatal("multiple stream errors")
					}
					streamErr = err
					continue
				}
				switch chunk.Kind {
				case port.ChunkToolCall:
					calls++
				case port.ChunkDone:
					dones++
				}
			}
			if tc.complete {
				if streamErr != nil || calls != tc.calls || dones != 1 {
					t.Fatalf("complete stream: err=%v calls=%d done=%d", streamErr, calls, dones)
				}
			} else if !errors.Is(streamErr, io.ErrUnexpectedEOF) || calls != 0 || dones != 0 {
				t.Fatalf("truncated stream: err=%v calls=%d done=%d", streamErr, calls, dones)
			}

			lookup := &canonicalLookupTool{}
			catalog := tool.NewCatalog()
			catalog.MustRegister(lookup)
			eng := agent.NewEngine(agent.Deps{LLM: p, Catalog: catalog, Model: "test", Policy: permpolicy.NewPolicy([]governance.Rule{{Tool: "lookup", Effect: governance.Allow}}, nil)})
			env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, memfs.NewWorkspace("/ws"), memledger.New(), nil)
			s := session.New("truncation", session.ModeDefault, env.Ref(), session.Limits{MaxTurns: 1}, time.Unix(0, 0))
			calls, results := 0, 0
			var terminal *session.ResultPayload
			for ev := range eng.Run(t.Context(), s, env, agent.RunRequest{Text: "lookup"}).Events() {
				switch ev.Type {
				case session.EvToolCall:
					calls++
				case session.EvToolResult:
					results++
				case session.EvResult:
					terminal = ev.Result
				}
			}
			if terminal == nil {
				t.Fatal("missing terminal result")
			}
			if tc.complete {
				if terminal.Error != "" || len(lookup.calls) != tc.calls || calls != tc.calls || results != tc.calls || len(s.Conversation.Messages) != 2+tc.calls {
					t.Fatalf("complete turn: result=%+v executions=%d calls=%d results=%d history=%+v", terminal, len(lookup.calls), calls, results, s.Conversation.Messages)
				}
			} else {
				if terminal.Stop != session.StopError || terminal.Error == "" || len(lookup.calls) != 0 || calls != 0 || results != 0 {
					t.Fatalf("truncated turn: result=%+v executions=%d calls=%d results=%d", terminal, len(lookup.calls), calls, results)
				}
				if len(s.Conversation.Messages) != 1 || s.Conversation.Messages[0].Role != session.RoleUser {
					t.Fatalf("truncated turn entered accepted history: %+v", s.Conversation.Messages)
				}
			}
		})
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
	var call *port.LLMToolCall
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

// TestOpenAIChatStreamErrorRetryDisposition exercises typed retry classification.
// Non-retryable 4xx (≠408/429) and context-overflow messages are permanent;
// transient codes (408, 429, 5xx) and unknown (0) are NOT permanent (fail-open).
func TestOpenAIChatStreamErrorRetryDisposition(t *testing.T) {
	tests := []struct {
		msg    string
		status int
		want   session.RetryDisposition
	}{
		// Non-retryable 4xx — permanent client-side rejections.
		{"request failed: 400 bad request", 400, session.RetryDispositionPermanent},
		{"request failed: 403 forbidden", 403, session.RetryDispositionPermanent},
		{"request failed: 404 not found", 404, session.RetryDispositionPermanent},
		// Retryable codes.
		{"request failed: 429 too many requests", 429, session.RetryDispositionRetryable},
		{"request failed: 503 service unavailable", 503, session.RetryDispositionRetryable},
		{"request failed: 500 internal server error", 500, session.RetryDispositionRetryable},
		// Status 0 is conservatively unknown.
		{"request failed: unknown error", 0, session.RetryDispositionUnknown},
		{"", 0, session.RetryDispositionUnknown},
		// Context overflow is permanent even with a transient-looking status.
		{"request failed: 500 Your input exceeds the context window of this model.", 500, session.RetryDispositionPermanent},
		{"request failed: 500 input exceeds the context length", 500, session.RetryDispositionPermanent},
		{"request failed: 500 maximum context length exceeded", 500, session.RetryDispositionPermanent},
		{"request failed: 500 prompt exceeds the token limit", 500, session.RetryDispositionPermanent},
		{"request failed: 500 request exceeded the token limit for this model", 500, session.RetryDispositionPermanent},
	}
	for _, tt := range tests {
		e := &openaichatStreamError{msg: tt.msg, status: tt.status}
		if got := e.RetryDisposition(); got != tt.want {
			t.Errorf("RetryDisposition() = %v for msg=%q status=%d, want %v", got, tt.msg, tt.status, tt.want)
		}
	}
}

func TestOpenAIChatStreamErrorSafeCategory(t *testing.T) {
	e := &openaichatStreamError{msg: "request failed: 429 too many requests", status: 429}
	if got := e.Error(); got != "provider request failed (429 Too Many Requests)" {
		t.Errorf("Error() = %q, want safe rate-limit category", got)
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

// TestStopDetailNamesCollapsedFinishReason pins that every finish_reason mapStop
// collapses into StopError is named on the done chunk, and benign ends are not.
func TestStopDetailNamesCollapsedFinishReason(t *testing.T) {
	for reason, want := range map[string]string{
		"length":         "length",
		"content_filter": "content_filter",
		"stop":           "",
		"tool_calls":     "",
		"surprise":       "",
	} {
		if got := stopDetail(reason); got != want {
			t.Errorf("stopDetail(%q) = %q, want %q", reason, got, want)
		}
		if (mapStop(reason) == session.StopError) != (want != "") {
			t.Errorf("finish_reason %q: mapStop=%q but stopDetail=%q; a StopError must carry a detail", reason, mapStop(reason), want)
		}
	}
}

// TestDecodeLengthFinishCarriesStopDetail drives a "length" finish through the
// real decode and finalize path, so the done chunk must carry the detail, not
// only the stopDetail helper.
func TestDecodeLengthFinishCarriesStopDetail(t *testing.T) {
	sse := `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"finish_reason":null,"delta":{"role":"assistant","content":"","reasoning_content":"thinking"}}]}` + "\n\n" +
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"finish_reason":"length","delta":{}}]}` + "\n\n" +
		"data: [DONE]\n\n"
	chunks, err := decodeSSE(strings.NewReader(sse))
	if err != nil {
		t.Fatalf("decodeSSE: %v", err)
	}
	done, ok := lastOfKind(chunks, port.ChunkDone)
	if !ok {
		t.Fatalf("no ChunkDone in %+v", chunks)
	}
	if done.Stop != session.StopError || done.StopDetail != "length" {
		t.Fatalf("done = {Stop:%q StopDetail:%q}, want {error length}", done.Stop, done.StopDetail)
	}
}
