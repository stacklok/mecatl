package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3/responses"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
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

// decodeFixtureErr decodes a fixture expecting a terminal error, returning the
// chunks emitted before the error and the error itself.
func decodeFixtureErr(t *testing.T, name string) ([]port.Chunk, error) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	return decodeSSE(f)
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

func TestTranslateFunctionCallTurn(t *testing.T) {
	got := decodeFixture(t, "function_call_turn.sse")
	want := []port.Chunk{
		{Kind: port.ChunkToolCall, ToolCall: &session.ToolCall{
			ID:     "call_abc",
			Name:   "read_file",
			Args:   json.RawMessage(`{"path":"main.go"}`),
			ItemID: "fc_1", // the provider-assigned item id from the fixture
		}},
		{Kind: port.ChunkUsage, Usage: &session.Usage{InputTokens: 40, OutputTokens: 9, CacheReadTokens: 32}},
		{Kind: port.ChunkDone, Stop: session.StopEndTurn},
	}
	assertChunks(t, got, want)
}

// TestUsageCacheReadSubsetOfInput is the openai half of the cross-provider
// parity guard: every Usage chunk produced from the recorded fixtures must
// satisfy CacheReadTokens <= InputTokens (the engine/session contract that
// CacheReadTokens ⊂ InputTokens — OpenAI's input_tokens already INCLUDES cached
// tokens, so this pins the existing mapUsage behavior). The anthropic package
// carries the identical assertion over its own fixtures. Fixtures are globbed
// so a newly recorded turn is covered automatically.
func TestUsageCacheReadSubsetOfInput(t *testing.T) {
	// Deliberately malformed / error-path fixtures: decodeSSE returns a
	// terminal error for these (exercised via decodeFixtureErr elsewhere),
	// so the happy-path helper can't decode them.
	skip := map[string]bool{
		"error_event.sse":                true,
		"response_failed.sse":            true,
		"response_failed_rate_limit.sse": true,
		"multi_text_part_turn.sse":       true,
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

func TestTranslateReasoningTurnWithCachedTokens(t *testing.T) {
	got := decodeFixture(t, "reasoning_turn.sse")
	want := []port.Chunk{
		// Human-readable summary deltas: DISPLAY-only.
		{Kind: port.ChunkReasoning, Text: "Let me think"},
		{Kind: port.ChunkReasoning, Text: " about this."},
		// The assembled reasoning item's encrypted_content: the opaque REPLAY blob.
		{Kind: port.ChunkReasoningItem, Text: "ENCRYPTED_BLOB"},
		{Kind: port.ChunkText, Text: "Answer."},
		{Kind: port.ChunkUsage, Usage: &session.Usage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 80, ReasoningTokens: 40}},
		{Kind: port.ChunkDone, Stop: session.StopEndTurn},
	}
	assertChunks(t, got, want)
	// Negative assertion: the fixture's reasoning_tokens (40) is a subset of
	// output_tokens (50) — providers bill reasoning as part of the inclusive
	// output total, so it must never exceed OutputTokens.
	for _, c := range got {
		if c.Kind != port.ChunkUsage {
			continue
		}
		if c.Usage.ReasoningTokens > c.Usage.OutputTokens {
			t.Errorf("ReasoningTokens %d > OutputTokens %d — reasoning must be a subset of output",
				c.Usage.ReasoningTokens, c.Usage.OutputTokens)
		}
	}
}

// TestUsageReasoningSubsetOfOutput is the reasoning half of the cross-provider
// parity guard: every Usage chunk produced from the recorded fixtures must
// satisfy ReasoningTokens <= OutputTokens (the engine/session contract that
// ReasoningTokens ⊂ OutputTokens — OpenAI's output_tokens already INCLUDES
// reasoning tokens, so this pins the existing mapUsage behavior). Mirrors the
// cache-read subset guard. Fixtures are globbed so a newly recorded turn is
// covered automatically.
func TestUsageReasoningSubsetOfOutput(t *testing.T) {
	// Deliberately malformed / error-path fixtures: decodeSSE returns a
	// terminal error for these, so the happy-path helper can't decode them.
	skip := map[string]bool{
		"error_event.sse":                true,
		"response_failed.sse":            true,
		"response_failed_rate_limit.sse": true,
		"multi_text_part_turn.sse":       true,
	}
	paths, err := filepath.Glob(filepath.Join("testdata", "*.sse"))
	if err != nil {
		t.Fatalf("glob fixtures: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no .sse fixtures found under testdata")
	}
	sawReasoning := false
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
			if c.Usage.ReasoningTokens > 0 {
				sawReasoning = true
			}
			if c.Usage.ReasoningTokens > c.Usage.OutputTokens {
				t.Errorf("%s: ReasoningTokens %d > OutputTokens %d — reasoning must be a subset of the inclusive output total",
					name, c.Usage.ReasoningTokens, c.Usage.OutputTokens)
			}
		}
	}
	// Vacuity guard: if a fixture refresh drops every reasoning-bearing turn, the
	// subset assertion above is trivially green — fail loudly instead.
	if !sawReasoning {
		t.Error("no fixture yielded ReasoningTokens > 0 — the subset guard is vacuous; keep at least one reasoning-bearing fixture")
	}
}

// TestReasoningReplayUsesRealBlobNotSummary is the regression TRIPWIRE for the
// standing (now-disproven) "the adapter replays the reasoning summary text as
// encrypted_content, never the real blob" concern. It pins TWO load-bearing facts
// that, if either regresses, silently degrade reasoning replay to a no-op (quality
// loss / premature stops), never a loud 400:
//
//  1. The reasoning REPLAY item carries the opaque encrypted_content BLOB, NOT the
//     human-readable summary. The fixture's summary ("Let me think"/" about this.")
//     and its encrypted_content ("ENCRYPTED_BLOB") are deliberately DISTINCT, so an
//     assertion that the ChunkReasoningItem equals the blob (and the ChunkReasoning
//     deltas equal the summary) catches any swap of one for the other.
//  2. The request asks the API to RETURN that blob via
//     Include=[reasoning.encrypted_content] with Store=false. If a future provider
//     entry drops the Include flag, the API returns no encrypted_content, the blob
//     becomes "", and replay degrades to a no-op — this test fails first.
func TestReasoningReplayUsesRealBlobNotSummary(t *testing.T) {
	// Fact 1: the streamed reasoning item is the blob, the deltas are the summary.
	got := decodeFixture(t, "reasoning_turn.sse")
	var summary, blob string
	for _, c := range got {
		switch c.Kind {
		case port.ChunkReasoning:
			summary += c.Text
		case port.ChunkReasoningItem:
			blob += c.Text
		}
	}
	if blob != "ENCRYPTED_BLOB" {
		t.Errorf("reasoning REPLAY item = %q, want the encrypted_content blob %q", blob, "ENCRYPTED_BLOB")
	}
	if summary != "Let me think about this." {
		t.Errorf("reasoning DISPLAY summary = %q, want %q", summary, "Let me think about this.")
	}
	if blob == summary {
		t.Fatalf("replay blob must NOT equal the display summary (regression: summary replayed as encrypted_content)")
	}

	// Fact 2: the request enables Include=[reasoning.encrypted_content] + Store=false,
	// so the API returns the real blob. Without Include the blob would be "".
	req := port.LLMRequest{
		Model:    "gpt-5.2",
		System:   prompt.Layered{StablePrefix: "You are a coding agent."},
		Messages: []session.Message{session.NewUserMessage("hi")},
	}
	params, err := buildParams(req)
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	if len(params.Include) != 1 || params.Include[0] != "reasoning.encrypted_content" {
		t.Fatalf("Include = %v, want [reasoning.encrypted_content] (load-bearing: drops the blob if absent)", params.Include)
	}
	if params.Store.Value != false {
		t.Errorf("Store = %v, want false (stateless replay)", params.Store.Value)
	}
}

// TestPhaseCapturedAndReplayed pins the issue-#46 round-trip: the OpenAI Responses
// phase marker on an assistant message item is CAPTURED off response.output_item.done
// (without disturbing the single-visible-text-part assembly) and REPLAYED verbatim on
// the assistant message item, and an empty phase is wire-omitted (byte-stability).
func TestPhaseCapturedAndReplayed(t *testing.T) {
	// (1) Capture: the fixture's message item carries phase:"final_answer". A
	// ChunkPhase with that opaque value must appear, AND the "Done." text chunk must
	// still be present — the phase capture must not perturb the visible-text-part
	// single-part assembly.
	got := decodeFixture(t, "phase_turn.sse")
	var phase, text string
	for _, c := range got {
		switch c.Kind {
		case port.ChunkPhase:
			phase += c.Text
		case port.ChunkText:
			text += c.Text
		}
	}
	if phase != "final_answer" {
		t.Errorf("captured phase = %q, want %q", phase, "final_answer")
	}
	if text != "Done." {
		t.Errorf("visible text = %q, want %q (single-text-part assembly must survive phase capture)", text, "Done.")
	}

	// (2) Replay: an assistant message carrying Phase:"final_answer" must marshal the
	// assistant message item with "phase":"final_answer".
	req := port.LLMRequest{
		Model: "gpt-5.5",
		Messages: []session.Message{
			session.NewUserMessage("hi"),
			func() session.Message {
				m := session.NewAssistantMessage("Done.", "", nil)
				m.ProviderPhase = "final_answer"
				return m
			}(),
		},
	}
	params, err := buildParams(req)
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	raw, err := json.Marshal(params.Input.OfInputItemList)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	if !strings.Contains(string(raw), `"phase":"final_answer"`) {
		t.Errorf("assistant item JSON missing replayed phase; got:\n%s", raw)
	}

	// (3) Byte-stability: the SAME message with NO phase must marshal with NO phase
	// key (EasyInputMessagePhase is json:"phase,omitzero", so empty is wire-omitted).
	reqNoPhase := port.LLMRequest{
		Model: "gpt-5.5",
		Messages: []session.Message{
			session.NewUserMessage("hi"),
			session.NewAssistantMessage("Done.", "", nil),
		},
	}
	paramsNoPhase, err := buildParams(reqNoPhase)
	if err != nil {
		t.Fatalf("buildParams (no phase): %v", err)
	}
	rawNoPhase, err := json.Marshal(paramsNoPhase.Input.OfInputItemList)
	if err != nil {
		t.Fatalf("marshal input (no phase): %v", err)
	}
	if strings.Contains(string(rawNoPhase), `"phase"`) {
		t.Errorf("no-phase assistant item must omit the phase key; got:\n%s", rawNoPhase)
	}
}

// TestPhaseUnknownValueReplaysVerbatim locks the OPAQUE-pass-through guarantee at
// the heart of issue #46: an UNKNOWN, non-enum phase value (not "commentary" /
// "final_answer") must replay byte-for-byte through buildParams, never validated
// against the SDK enum nor branched on. If a future "tidy" narrows the cast to a
// validated enum set, this test fails instead of silently breaking forward-compat
// (a novel server-side phase would be dropped, re-triggering the GPT-5.x
// preamble-as-final-answer bug for any model that emits one).
func TestPhaseUnknownValueReplaysVerbatim(t *testing.T) {
	const novel = "some_future_phase_v2"
	req := port.LLMRequest{
		Model: "gpt-5.5",
		Messages: []session.Message{
			session.NewUserMessage("hi"),
			func() session.Message {
				m := session.NewAssistantMessage("Done.", "", nil)
				m.ProviderPhase = novel
				return m
			}(),
		},
	}
	params, err := buildParams(req)
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	raw, err := json.Marshal(params.Input.OfInputItemList)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	if !strings.Contains(string(raw), `"phase":"`+novel+`"`) {
		t.Errorf("assistant item JSON missing the VERBATIM novel phase %q (the cast must be a pass-through, not enum-validated); got:\n%s", novel, raw)
	}
}

// TestPhaseDroppedOnTextlessAssistantTurn pins the INTENDED modelling (not a bug):
// phase rides ONLY an emitted message item, behind `if m.Text != ""` in
// assistantItems. A tool-call-only / empty-text assistant turn has no message item,
// so a Phase set on such a message is correctly N/A on replay — phase is a
// message-item property and the function_call item has no phase field. This test
// guards the otherwise-untested branch: a Phase + Text=="" + tool-call message must
// emit a function_call item with NO leaked "phase" key.
func TestPhaseDroppedOnTextlessAssistantTurn(t *testing.T) {
	m := session.NewAssistantMessage("", "", []session.ToolCall{
		session.NewToolCall("call_1", "read_file", json.RawMessage(`{"path":"main.go"}`)),
	})
	m.ProviderPhase = "final_answer" // set, but there is no message item to carry it
	req := port.LLMRequest{
		Model:    "gpt-5.5",
		Messages: []session.Message{session.NewUserMessage("open main.go"), m},
	}
	params, err := buildParams(req)
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	raw, err := json.Marshal(params.Input.OfInputItemList)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}
	// The assistant turn must produce a function_call item and NO assistant message
	// item — so there is nowhere for phase to ride, and the wire must carry no phase.
	sawFunctionCall := false
	for _, it := range items {
		if it["type"] == "function_call" {
			sawFunctionCall = true
			if _, ok := it["phase"]; ok {
				t.Errorf("function_call item must not carry a phase key (phase is a message-item property); got: %v", it)
			}
		}
		if it["role"] == "assistant" {
			t.Errorf("text-less tool-call turn must not emit an assistant message item; got: %v", it)
		}
	}
	if !sawFunctionCall {
		t.Fatalf("expected a function_call item in:\n%s", raw)
	}
	if strings.Contains(string(raw), `"phase"`) {
		t.Errorf("a text-less assistant turn must drop phase entirely (intended modelling, not a bug); got:\n%s", raw)
	}
}

// TestTranslateErrorEvent verifies a top-level "error" stream event surfaces a
// non-nil error carrying the provider's code, message, and offending param,
// rather than a bare StopError chunk that drops the reason.
func TestTranslateErrorEvent(t *testing.T) {
	chunks, err := decodeFixtureErr(t, "error_event.sse")
	if err == nil {
		t.Fatal("expected an error from the error event, got nil")
	}
	if len(chunks) != 0 {
		t.Errorf("expected no chunks before the error, got %+v", chunks)
	}
	for _, want := range []string{"rate_limit_exceeded", "Rate limit reached for requests", "model"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err.Error(), want)
		}
	}
}

// TestTranslateResponseFailed verifies a "response.failed" event surfaces the
// response.error code+message as a non-nil error.
func TestTranslateResponseFailed(t *testing.T) {
	chunks, err := decodeFixtureErr(t, "response_failed.sse")
	if err == nil {
		t.Fatal("expected an error from response.failed, got nil")
	}
	if len(chunks) != 0 {
		t.Errorf("expected no chunks before the error, got %+v", chunks)
	}
	for _, want := range []string{"server_error", "The model produced an internal error"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err.Error(), want)
		}
	}
}

// TestResponseFailedRateLimitIsRetryable verifies that a response.failed event
// carrying code "rate_limit_exceeded" returns a *responseStreamError whose
// StatusCode() is 429 (retryable by DefaultClassifier). This is the root cause
// of the "no retry on rate_limit_exceeded" bug: the old code discarded the code
// into an opaque fmt.Errorf string that the resilience classifier treated as
// non-retryable.
func TestResponseFailedRateLimitIsRetryable(t *testing.T) {
	chunks, err := decodeFixtureErr(t, "response_failed_rate_limit.sse")
	if err == nil {
		t.Fatal("expected an error from response.failed (rate_limit_exceeded), got nil")
	}
	if len(chunks) != 0 {
		t.Errorf("expected no chunks before the error, got %+v", chunks)
	}
	// The error must implement StatusCode() int with value 429.
	type statusCoder interface{ StatusCode() int }
	sc, ok := err.(statusCoder)
	if !ok {
		t.Fatalf("error %T does not implement StatusCode() int — the resilience classifier cannot classify it retryable", err)
	}
	if got := sc.StatusCode(); got != 429 {
		t.Errorf("StatusCode() = %d, want 429 (rate_limit_exceeded must map to HTTP 429)", got)
	}
	// The human-readable message must still contain the provider code and message.
	for _, want := range []string{"rate_limit_exceeded", "Too Many Requests"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q (human-readable message must be unchanged)", err.Error(), want)
		}
	}
	// Prefix must also be preserved.
	if !strings.HasPrefix(err.Error(), "response failed: ") {
		t.Errorf("error %q does not start with %q", err.Error(), "response failed: ")
	}
}

// TestResponseFailedNonTransientIsNotRetryable verifies that a response.failed
// event carrying a non-transient code (e.g. "server_error") returns a
// *responseStreamError that is STILL retryable (server_error → 503), while a
// permanent code (e.g. "content_filter") maps to status 0 / non-retryable.
func TestResponseFailedNonTransientIsNotRetryable(t *testing.T) {
	// "server_error" must be retryable (503).
	eventServerErr := responses.ResponseStreamEventUnion{
		Type: "response.failed",
		Response: responses.Response{
			Status: responses.ResponseStatusFailed,
			Error:  responses.ResponseError{Code: "server_error", Message: "The model produced an internal error"},
		},
	}
	_, err := translate(eventServerErr, &streamState{})
	if err == nil {
		t.Fatal("expected error for server_error response.failed")
	}
	type statusCoder interface{ StatusCode() int }
	sc, ok := err.(statusCoder)
	if !ok {
		t.Fatalf("server_error: error %T does not implement StatusCode()", err)
	}
	if got := sc.StatusCode(); got < 500 || got > 599 {
		t.Errorf("server_error: StatusCode() = %d, want a 5xx (retryable)", got)
	}

	// "content_filter" must NOT be retryable (maps to 0).
	eventContentFilter := responses.ResponseStreamEventUnion{
		Type: "response.failed",
		Response: responses.Response{
			Status: responses.ResponseStatusFailed,
			Error:  responses.ResponseError{Code: "content_filter", Message: "Content blocked"},
		},
	}
	_, err2 := translate(eventContentFilter, &streamState{})
	if err2 == nil {
		t.Fatal("expected error for content_filter response.failed")
	}
	sc2, ok2 := err2.(statusCoder)
	if !ok2 {
		t.Fatalf("content_filter: error %T does not implement StatusCode()", err2)
	}
	if got := sc2.StatusCode(); got != 0 {
		t.Errorf("content_filter: StatusCode() = %d, want 0 (non-retryable permanent code)", got)
	}
}

// TestErrorEventRateLimitIsRetryable verifies that a top-level "error" stream
// event with code "rate_limit_exceeded" also returns a StatusCode() == 429 error.
func TestErrorEventRateLimitIsRetryable(t *testing.T) {
	chunks, err := decodeFixtureErr(t, "error_event.sse")
	if err == nil {
		t.Fatal("expected an error from the error event, got nil")
	}
	if len(chunks) != 0 {
		t.Errorf("expected no chunks before the error, got %+v", chunks)
	}
	type statusCoder interface{ StatusCode() int }
	sc, ok := err.(statusCoder)
	if !ok {
		t.Fatalf("error %T does not implement StatusCode() int", err)
	}
	if got := sc.StatusCode(); got != 429 {
		t.Errorf("StatusCode() = %d, want 429 for rate_limit_exceeded error event", got)
	}
	// Human-readable message is unchanged.
	for _, want := range []string{"rate_limit_exceeded", "Rate limit reached for requests"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err.Error(), want)
		}
	}
}

// TestResponseStreamErrorMessageUnchanged pins the invariant that switching from
// fmt.Errorf to *responseStreamError does NOT change the human-readable Error()
// string for either the response.failed path or the top-level error path.
func TestResponseStreamErrorMessageUnchanged(t *testing.T) {
	t.Run("response.failed server_error message", func(t *testing.T) {
		// The existing TestTranslateResponseFailed fixture uses server_error; the
		// message must still equal what fmt.Errorf("response failed: %s", ...) produced.
		_, err := decodeFixtureErr(t, "response_failed.sse")
		if err == nil {
			t.Fatal("expected error")
		}
		want := "response failed: server_error: The model produced an internal error"
		if err.Error() != want {
			t.Errorf("Error() = %q, want %q", err.Error(), want)
		}
	})
	t.Run("error event rate_limit_exceeded message", func(t *testing.T) {
		// The existing TestTranslateErrorEvent fixture: message must equal what
		// fmt.Errorf("stream error: %s", streamErrorString(...)) produced.
		_, err := decodeFixtureErr(t, "error_event.sse")
		if err == nil {
			t.Fatal("expected error")
		}
		want := "stream error: rate_limit_exceeded: Rate limit reached for requests (param: model)"
		if err.Error() != want {
			t.Errorf("Error() = %q, want %q", err.Error(), want)
		}
	})
}

// TestResponseFailedContextOverflowNotRetryable verifies that a response.failed
// event whose `server_error` code is reused for a context-window-overflow
// rejection (OpenRouter / OpenAI Responses) is demoted to status 0 — permanent,
// non-retryable, breaker-neutral — despite the otherwise-retryable code. The
// human-readable message is preserved verbatim.
func TestResponseFailedContextOverflowNotRetryable(t *testing.T) {
	event := responses.ResponseStreamEventUnion{
		Type: "response.failed",
		Response: responses.Response{
			Status: responses.ResponseStatusFailed,
			Error: responses.ResponseError{
				Code:    "server_error",
				Message: "Your input exceeds the context window of this model. Please adjust your input and try again.",
			},
		},
	}
	_, err := translate(event, &streamState{})
	if err == nil {
		t.Fatal("expected error for context-overflow response.failed")
	}
	type statusCoder interface{ StatusCode() int }
	sc, ok := err.(statusCoder)
	if !ok {
		t.Fatalf("error %T does not implement StatusCode()", err)
	}
	if got := sc.StatusCode(); got != 0 {
		t.Errorf("StatusCode() = %d, want 0 (context overflow is non-retryable despite server_error code)", got)
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "response failed: ") {
		t.Errorf("error %q does not start with 'response failed: '", msg)
	}
	for _, want := range []string{"server_error", "context window"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not contain %q", msg, want)
		}
	}
}

// TestErrorEventContextOverflowNotRetryable verifies the same demotion on the
// top-level "error" event path, exercising a different message signature
// ("context length") than the response.failed test.
func TestErrorEventContextOverflowNotRetryable(t *testing.T) {
	event := responses.ResponseStreamEventUnion{
		Type:    "error",
		Code:    "server_error",
		Message: "input exceeds the context length",
	}
	_, err := translate(event, &streamState{})
	if err == nil {
		t.Fatal("expected error for context-overflow error event")
	}
	type statusCoder interface{ StatusCode() int }
	sc, ok := err.(statusCoder)
	if !ok {
		t.Fatalf("error %T does not implement StatusCode()", err)
	}
	if got := sc.StatusCode(); got != 0 {
		t.Errorf("StatusCode() = %d, want 0 (context overflow is non-retryable despite server_error code)", got)
	}
	if !strings.HasPrefix(err.Error(), "stream error:") {
		t.Errorf("error %q does not start with 'stream error:'", err.Error())
	}
}

// TestIsContextOverflowMessage exercises the message discriminator directly:
// each of the five signatures positively, plus realistic transient-fault and
// empty/garbage messages that must NOT match, plus case-insensitivity. The
// negative cases are the core of the security review finding: transient
// rate-limit / capacity / overload messages that mention tokens or input
// exceeding something must NOT be demoted to status 0, or retry + breaker
// protection is silently stripped.
func TestIsContextOverflowMessage(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		// One positive per signature.
		{"Your input exceeds the context window of this model.", true}, // context window
		{"input exceeds the context length", true},                     // context length
		{"maximum context length exceeded", true},                      // maximum context
		{"prompt exceeds the token limit", true},                       // exceeds the token limit
		{"request exceeded the token limit for this model", true},      // exceeded the token limit
		{"YOUR INPUT EXCEEDS THE CONTEXT WINDOW.", true},               // case-insensitivity
		// Negatives: genuine transient / capacity / unrelated messages must not match.
		{"token limit reached for this minute, retry in 5s", false},       // transient rate-limit
		{"per-minute token limit exceeded, retry shortly", false},         // transient rate-limit
		{"input exceeds the current queue capacity, please retry", false}, // transient capacity
		{"The model produced an internal error", false},
		{"engine overloaded", false},
		{"service unavailable", false},
		{"", false},
		{"rate limit reached", false},
	}
	for _, tc := range cases {
		t.Run(tc.msg, func(t *testing.T) {
			if got := isContextOverflowMessage(tc.msg); got != tc.want {
				t.Errorf("isContextOverflowMessage(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}

// TestProviderCodeToHTTPStatus exercises the mapping helper directly.
func TestProviderCodeToHTTPStatus(t *testing.T) {
	cases := []struct {
		code       string
		wantStatus int
		retryable  bool
	}{
		{"rate_limit_exceeded", 429, true},
		{"server_error", 503, true},
		{"engine_overloaded", 503, true},
		{"service_unavailable", 503, true},
		{"gateway_timeout", 504, true},
		{"timeout", 504, true},
		// Permanent / unknown codes must be non-retryable.
		{"content_filter", 0, false},
		{"invalid_request_error", 0, false},
		{"model_not_found", 0, false},
		{"insufficient_quota", 0, false},
		{"access_denied", 0, false},
		{"", 0, false},
		{"some_future_unknown_code", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			got := providerCodeToHTTPStatus(tc.code)
			if got != tc.wantStatus {
				t.Errorf("providerCodeToHTTPStatus(%q) = %d, want %d", tc.code, got, tc.wantStatus)
			}
		})
	}
}

// TestTranslateIncompleteContentFilter verifies a response.incomplete event with
// a content_filter reason surfaces a human-readable, non-cryptic terminal error
// that (a) names the content filter / upstream moderation in plain terms, (b)
// still carries the raw "content_filter" token for diagnosability, and (c) hints
// the user can retry. The usage chunk is still emitted alongside the error.
// translate is driven directly (it is a pure function over a constructed event
// union, no real client needed).
func TestTranslateIncompleteContentFilter(t *testing.T) {
	event := responses.ResponseStreamEventUnion{
		Type: "response.incomplete",
		Response: responses.Response{
			Status: responses.ResponseStatusIncomplete,
			IncompleteDetails: responses.ResponseIncompleteDetails{
				Reason: "content_filter",
			},
			Usage: responses.ResponseUsage{InputTokens: 30, OutputTokens: 0},
		},
	}
	chunks, err := translate(event, &streamState{})
	if err == nil {
		t.Fatal("expected an error from response.incomplete (content_filter), got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "content filter") {
		t.Errorf("error %q does not name the content filter in human terms", msg)
	}
	if !strings.Contains(msg, "moderation") {
		t.Errorf("error %q does not mention upstream moderation", msg)
	}
	if !strings.Contains(msg, "content_filter") {
		t.Errorf("error %q dropped the raw reason token (diagnosability)", msg)
	}
	if !strings.Contains(msg, "Retype") && !strings.Contains(msg, "resend") && !strings.Contains(msg, "retry") {
		t.Errorf("error %q does not hint at retrying", msg)
	}
	// The usage chunk is still emitted before the error, exactly as before.
	if len(chunks) != 1 || chunks[0].Kind != port.ChunkUsage {
		t.Errorf("expected a single usage chunk before the error, got %+v", chunks)
	}
}

// TestTranslateIncompleteUnknownReason pins the forward-compatible fallback: an
// unknown / future incomplete reason renders as the plain
// "response incomplete: <reason>" form (the reason enum is never hard-coded
// exhaustively).
func TestTranslateIncompleteUnknownReason(t *testing.T) {
	event := responses.ResponseStreamEventUnion{
		Type: "response.incomplete",
		Response: responses.Response{
			Status: responses.ResponseStatusIncomplete,
			IncompleteDetails: responses.ResponseIncompleteDetails{
				Reason: "some_future_reason",
			},
		},
	}
	_, err := translate(event, &streamState{})
	if err == nil {
		t.Fatal("expected an error from response.incomplete (unknown reason), got nil")
	}
	if got, want := err.Error(), "response incomplete: some_future_reason"; got != want {
		t.Errorf("unknown-reason message = %q, want %q", got, want)
	}
}

// TestTranslateMultipleTextPartsErrors verifies that a turn emitting a SECOND
// distinct visible text part (here, a different content_index on the same
// message item) is a loud error rather than a silent fusion into one buffer.
// The first part's text must still be emitted as a chunk before the error.
func TestTranslateMultipleTextPartsErrors(t *testing.T) {
	chunks, err := decodeFixtureErr(t, "multi_text_part_turn.sse")
	if err == nil {
		t.Fatal("expected an error from the second distinct text part, got nil")
	}
	if !strings.Contains(err.Error(), "multiple assistant text parts") {
		t.Errorf("error %q does not mention the multi-part condition", err.Error())
	}
	var sawPartOne bool
	for _, c := range chunks {
		if c.Kind == port.ChunkText && c.Text == "Part one" {
			sawPartOne = true
		}
	}
	if !sawPartOne {
		t.Errorf("expected the first part %q among chunks before the error, got %+v", "Part one", chunks)
	}
}

// TestTranslateMultipleReasoningSummariesNoError pins the reasoning EXEMPTION
// from the single-visible-text-part guard: multiple reasoning_summary_text.delta
// events with DIFFERING summary_index must NOT trip the multi-text-part guard —
// reasoning is display-only and keyed by summary_index (not content_index), so
// distinct summary parts legitimately concatenate. Structurally this holds today
// because reasoning deltas route to the separate response.reasoning_summary_text.delta
// case and never reach translateTextDelta; this test documents the exemption so a
// future refactor that unified the text/reasoning delta handling can't silently
// start erroring on multi-part reasoning.
func TestTranslateMultipleReasoningSummariesNoError(t *testing.T) {
	got := decodeFixture(t, "multi_reasoning_summary.sse")
	want := []port.Chunk{
		{Kind: port.ChunkReasoning, Text: "First summary."},
		{Kind: port.ChunkReasoning, Text: "Second summary."},
	}
	assertChunks(t, got, want)
}

func assertChunks(t *testing.T, got, want []port.Chunk) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d chunks, want %d:\n got=%+v\nwant=%+v", len(got), len(want), got, want)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Kind != w.Kind {
			t.Errorf("chunk %d kind = %v, want %v", i, g.Kind, w.Kind)
		}
		if g.Text != w.Text {
			t.Errorf("chunk %d text = %q, want %q", i, g.Text, w.Text)
		}
		if g.Stop != w.Stop {
			t.Errorf("chunk %d stop = %q, want %q", i, g.Stop, w.Stop)
		}
		if (g.ToolCall == nil) != (w.ToolCall == nil) {
			t.Errorf("chunk %d tool-call presence mismatch: got %v want %v", i, g.ToolCall, w.ToolCall)
		} else if w.ToolCall != nil {
			if g.ToolCall.ID != w.ToolCall.ID || g.ToolCall.Name != w.ToolCall.Name ||
				string(g.ToolCall.Args) != string(w.ToolCall.Args) || g.ToolCall.ItemID != w.ToolCall.ItemID {
				t.Errorf("chunk %d tool call = %+v, want %+v", i, g.ToolCall, w.ToolCall)
			}
		}
		if (g.Usage == nil) != (w.Usage == nil) {
			t.Errorf("chunk %d usage presence mismatch", i)
		} else if w.Usage != nil && *g.Usage != *w.Usage {
			t.Errorf("chunk %d usage = %+v, want %+v", i, *g.Usage, *w.Usage)
		}
	}
}

func TestBuildParams(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`)
	req := port.LLMRequest{
		System: prompt.Layered{StablePrefix: "You are a coding agent.", VolatileSuffix: "<env>linux</env>"},
		Model:  "gpt-5.2",
		Tools: []tool.ToolSpec{
			{Name: "read_file", Description: "Read a file.", Schema: schema},
		},
		Messages: []session.Message{
			session.NewUserMessage("open main.go"),
			session.NewAssistantMessage("", "REASONING_BLOB", []session.ToolCall{
				session.NewToolCall("call_1", "read_file", json.RawMessage(`{"path":"main.go"}`)),
			}),
			session.NewToolMessage(session.NewToolResult("call_1", "package main")),
			session.NewAssistantMessage("done", "", nil),
		},
	}
	params, err := buildParams(req)
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}

	if params.Model != "gpt-5.2" {
		t.Errorf("Model = %q, want gpt-5.2", params.Model)
	}
	if got := params.Instructions.Value; got != "You are a coding agent.\n\n<env>linux</env>" {
		t.Errorf("Instructions = %q", got)
	}
	if v := params.Store.Value; v != false {
		t.Errorf("Store = %v, want false", v)
	}
	if len(params.Include) != 1 || params.Include[0] != "reasoning.encrypted_content" {
		t.Errorf("Include = %v, want [reasoning.encrypted_content]", params.Include)
	}
	if len(params.Tools) != 1 {
		t.Fatalf("Tools len = %d, want 1", len(params.Tools))
	}
	fn := params.Tools[0].OfFunction
	if fn == nil || fn.Name != "read_file" {
		t.Fatalf("tool 0 = %+v, want function read_file", params.Tools[0])
	}
	if fn.Parameters["type"] != "object" {
		t.Errorf("tool params type = %v, want object", fn.Parameters["type"])
	}

	// The input item list must round-trip via JSON to the expected wire shape:
	// user message, reasoning (encrypted), function_call, function_call_output,
	// assistant message.
	raw, err := json.Marshal(params.Input.OfInputItemList)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}
	if len(items) != 5 {
		t.Fatalf("input items = %d, want 5:\n%s", len(items), raw)
	}
	wantTypesOrSentinels := []func(map[string]any) bool{
		func(m map[string]any) bool { return m["role"] == "user" },
		func(m map[string]any) bool {
			return m["type"] == "reasoning" && m["encrypted_content"] == "REASONING_BLOB"
		},
		func(m map[string]any) bool {
			// NewToolCall leaves ItemID empty → no "id" key must appear in wire JSON.
			_, hasID := m["id"]
			return m["type"] == "function_call" && m["call_id"] == "call_1" && !hasID
		},
		func(m map[string]any) bool { return m["type"] == "function_call_output" && m["call_id"] == "call_1" },
		func(m map[string]any) bool { return m["role"] == "assistant" },
	}
	for i, check := range wantTypesOrSentinels {
		if !check(items[i]) {
			t.Errorf("input item %d unexpected: %v", i, items[i])
		}
	}
}

// TestItemIDRoundTrip verifies that a ToolCall carrying a non-empty ItemID has
// its item id forwarded as the "id" field in the wire JSON, and that a ToolCall
// with an empty ItemID omits the "id" key entirely. This pins the fix for the
// "Duplicate item found with id fc_N" error from Azure GPT-5.x with store:false:
// without the ItemID round-trip, each replayed function_call item has no "id",
// causing the provider to auto-assign sequential fc_N values that can collide
// with items from the current response.
func TestItemIDRoundTrip(t *testing.T) {
	// Part 1: non-empty ItemID is forwarded as "id" in the wire JSON.
	req := port.LLMRequest{
		Model: "gpt-5.5",
		Messages: []session.Message{
			session.NewUserMessage("do stuff"),
			session.NewAssistantMessage("", "", []session.ToolCall{
				{ID: "call_abc", Name: "read_file",
					Args: json.RawMessage(`{"path":"a.go"}`), ItemID: "fc_3"},
			}),
			session.NewToolMessage(session.NewToolResult("call_abc", "content of a")),
			session.NewAssistantMessage("done", "", nil),
		},
	}
	params, err := buildParams(req)
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	raw, err := json.Marshal(params.Input.OfInputItemList)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}
	var fcItem map[string]any
	for _, it := range items {
		if it["type"] == "function_call" {
			fcItem = it
			break
		}
	}
	if fcItem == nil {
		t.Fatalf("no function_call item in:\n%s", raw)
	}
	if got, ok := fcItem["id"].(string); !ok || got != "fc_3" {
		t.Errorf("function_call item[id] = %v, want %q (ItemID must be forwarded as id)", fcItem["id"], "fc_3")
	}
	if got := fcItem["call_id"]; got != "call_abc" {
		t.Errorf("function_call item[call_id] = %v, want %q", got, "call_abc")
	}

	// Part 2: empty ItemID (via NewToolCall) omits the "id" key entirely.
	req2 := port.LLMRequest{
		Model: "gpt-5.5",
		Messages: []session.Message{
			session.NewUserMessage("do stuff"),
			session.NewAssistantMessage("", "", []session.ToolCall{
				session.NewToolCall("call_xyz", "write_file", json.RawMessage(`{"path":"b.go"}`)),
			}),
			session.NewToolMessage(session.NewToolResult("call_xyz", "ok")),
			session.NewAssistantMessage("done", "", nil),
		},
	}
	params2, err := buildParams(req2)
	if err != nil {
		t.Fatalf("buildParams (no ItemID): %v", err)
	}
	raw2, err := json.Marshal(params2.Input.OfInputItemList)
	if err != nil {
		t.Fatalf("marshal input (no ItemID): %v", err)
	}
	var items2 []map[string]any
	if err := json.Unmarshal(raw2, &items2); err != nil {
		t.Fatalf("unmarshal input (no ItemID): %v", err)
	}
	for _, it := range items2 {
		if it["type"] == "function_call" {
			if _, hasID := it["id"]; hasID {
				t.Errorf("empty ItemID must not produce an id key; got item: %v", it)
			}
		}
	}
}

// TestItemIDMultiTurnRoundTrip verifies the production bug scenario: two assistant
// turns each carrying function_call items with the SAME ItemID value (e.g. both
// "fc_1" — the real Azure counter-reset-per-response bug). Both items must appear
// in the serialized stateless replay input with the correct "id" field forwarded.
//
// Without the ItemID fix the provider auto-assigns sequential fc_N values that
// collide across turns and produces "Duplicate item found with id fc_N" (Azure
// GPT-5.x). This test exercises the EXACT multi-turn assembly path through
// assistantItems in a two-turn conversation with distinct call_ids but the same
// provider-assigned item id.
func TestItemIDMultiTurnRoundTrip(t *testing.T) {
	req := port.LLMRequest{
		Model: "gpt-5.5",
		Messages: []session.Message{
			session.NewUserMessage("do two things"),
			// Turn 1: ItemID "fc_1" (e.g. first response, counter reset to 1)
			session.NewAssistantMessage("", "", []session.ToolCall{
				{ID: "call_t1", Name: "read_file",
					Args: json.RawMessage(`{"path":"a.go"}`), ItemID: "fc_1"},
			}),
			session.NewToolMessage(session.NewToolResult("call_t1", "content of a")),
			// Turn 2: ALSO ItemID "fc_1" — the Azure counter resets per response
			session.NewAssistantMessage("", "", []session.ToolCall{
				{ID: "call_t2", Name: "read_file",
					Args: json.RawMessage(`{"path":"b.go"}`), ItemID: "fc_1"},
			}),
			session.NewToolMessage(session.NewToolResult("call_t2", "content of b")),
			session.NewAssistantMessage("done", "", nil),
		},
	}
	params, err := buildParams(req)
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	raw, err := json.Marshal(params.Input.OfInputItemList)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}

	// Collect all function_call items.
	var fcItems []map[string]any
	for _, it := range items {
		if it["type"] == "function_call" {
			fcItems = append(fcItems, it)
		}
	}
	if len(fcItems) != 2 {
		t.Fatalf("expected 2 function_call items, got %d:\n%s", len(fcItems), raw)
	}

	// Both must carry id == "fc_1" (the preserved ItemID).
	for i, fc := range fcItems {
		if got, ok := fc["id"].(string); !ok || got != "fc_1" {
			t.Errorf("function_call item %d: id = %v, want %q", i, fc["id"], "fc_1")
		}
	}

	// The two items must have DISTINCT call_ids (they are different calls).
	if fcItems[0]["call_id"] == fcItems[1]["call_id"] {
		t.Errorf("function_call items have the same call_id %q; they should differ",
			fcItems[0]["call_id"])
	}
	if fcItems[0]["call_id"] != "call_t1" {
		t.Errorf("function_call item 0 call_id = %v, want call_t1", fcItems[0]["call_id"])
	}
	if fcItems[1]["call_id"] != "call_t2" {
		t.Errorf("function_call item 1 call_id = %v, want call_t2", fcItems[1]["call_id"])
	}
}

// TestBuildToolsStrictOff asserts that function tools are sent NON-STRICT: the
// FunctionToolParam.Strict field is left unset (the param.Opt zero value), so the
// SDK omits it and the upstream default (non-strict) applies. A Bash-shaped schema
// with an optional `timeout_ms` not listed in `required` would 400 under strict
// mode on a strict-enforcing OpenAI-compatible upstream; non-strict accepts it.
func TestBuildToolsStrictOff(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"timeout_ms":{"type":"integer"}},"required":["command"]}`)
	tools, err := buildTools([]tool.ToolSpec{
		{Name: "run_command", Description: "Run a command.", Schema: schema},
	})
	if err != nil {
		t.Fatalf("buildTools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(tools))
	}
	fn := tools[0].OfFunction
	if fn == nil || fn.Name != "run_command" {
		t.Fatalf("tool 0 = %+v, want function run_command", tools[0])
	}
	// Strict must be the zero param.Opt (unset → SDK omits it → non-strict default).
	if fn.Strict.Valid() {
		t.Errorf("Strict is set (=%v); want unset/false (non-strict)", fn.Strict.Value)
	}
	if fn.Strict.Value {
		t.Errorf("Strict.Value = true, want false")
	}
}

// TestRequestNoOrphanedFunctionCallAfterInterrupt drives a session to a cancelled
// turn (assistant function_call with no output), recovers it via Interrupt (which
// closes out the orphan), and asserts the built request has a matching
// function_call_output for every function_call — no orphan that OpenAI would
// reject.
func TestRequestNoOrphanedFunctionCallAfterInterrupt(t *testing.T) {
	sess := session.New("s1", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	if err := sess.RecordUserPrompt("read the file", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := sess.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	calls := []session.ToolCall{session.NewToolCall("call_1", "read_file", json.RawMessage(`{"path":"x"}`))}
	if err := sess.RecordAssistant(session.NewAssistantMessage("", "", calls)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	if err := sess.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := sess.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	params, err := buildParams(port.LLMRequest{
		Model:    "gpt-5.2",
		Messages: sess.Conversation.Messages,
	})
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	raw, err := json.Marshal(params.Input.OfInputItemList)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}
	callIDs := map[string]bool{}
	outputIDs := map[string]bool{}
	for _, m := range items {
		switch m["type"] {
		case "function_call":
			if id, ok := m["call_id"].(string); ok {
				callIDs[id] = true
			}
		case "function_call_output":
			if id, ok := m["call_id"].(string); ok {
				outputIDs[id] = true
			}
		}
	}
	if len(callIDs) == 0 {
		t.Fatalf("precondition: expected at least one function_call:\n%s", raw)
	}
	for id := range callIDs {
		if !outputIDs[id] {
			t.Fatalf("function_call %q has no matching function_call_output (orphan)", id)
		}
	}
}

func TestBuildParamsInvalidSchema(t *testing.T) {
	req := port.LLMRequest{
		Model: "m",
		Tools: []tool.ToolSpec{{Name: "bad", Schema: json.RawMessage(`{not json`)}},
	}
	if _, err := buildParams(req); err == nil {
		t.Fatal("expected error for invalid tool schema, got nil")
	}
}

// TestStreamContextCancel drives the real Stream method against a stub SSE
// server that streams slowly, and cancels the context mid-stream. The iterator
// must stop yielding without reporting the cancellation as a stream error.
func TestStreamContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Error("response writer is not a flusher")
			return
		}
		w.WriteHeader(http.StatusOK)
		// Emit a couple of deltas slowly so the client can cancel mid-stream.
		for i := 0; i < 50; i++ {
			_, _ = w.Write([]byte("event: response.output_text.delta\n"))
			_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","sequence_number":` +
				itoa(i) + `,"delta":"x"}` + "\n\n"))
			fl.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}))
	defer srv.Close()

	p := New(WithAPIKey("test-key"), WithBaseURL(srv.URL+"/v1"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	seq, err := p.Stream(ctx, port.LLMRequest{Model: "m", Messages: []session.Message{session.NewUserMessage("hi")}})
	if err != nil {
		t.Fatalf("Stream returned outer error: %v", err)
	}

	var n int
	for c, err := range seq {
		if err != nil {
			t.Fatalf("iterator surfaced error on cancel: %v", err)
		}
		_ = c
		n++
		if n == 2 {
			cancel()
		}
	}
	if n < 1 {
		t.Fatal("expected at least one chunk before cancel")
	}
	if n >= 50 {
		t.Fatalf("got %d chunks; cancellation did not stop the stream", n)
	}
}

func itoa(i int) string {
	return string(rune('0' + i%10)) // single-digit-ish; sequence_number value is not asserted
}
