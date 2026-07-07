package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// TestGLMInterleavedReasoningE2E is the composition end-to-end guard for issue
// #240: a session resolving z-ai/glm-5.2 on openrouter must build an adapter
// whose stream translation RECLASSIFIES a reasoning_content-bearing
// response.output_text.delta to ChunkReasoning (NOT ChunkText), and the model's
// real final answer (a later delta WITHOUT the sibling field) to ChunkText.
//
// It builds the REAL provider registry with openrouter available (the catalog
// carries z-ai/glm-5.2 with interleaved.field="reasoning_content"), drives the
// entry's REAL remint closure (the same one the per-session factory calls) to
// re-mint the adapter with the GLM interleaved field, and streams a recorded GLM
// SSE fixture back from a local stub server. This proves the full seam —
// catalog → composition helper interleavedReasoningField → remint →
// WithInterleavedReasoningField → streamState.interleavedField → translate — is
// wired, and that the catalog-derived field actually reaches the adapter.
//
// It mirrors reasoning_effort_test.go's pattern (build the registry, call
// entry.remint, assert the re-minted provider carries the knob), differing only
// in that the knob is proven via STREAMING (the knob's effect is in translate,
// which a mockllm remint cannot exercise), so the real openai adapter rides
// against a stub SSE server.
func TestGLMInterleavedReasoningE2E(t *testing.T) {
	// A recorded GLM-5.2 turn: two reasoning_content-bearing deltas (interleaved
	// reasoning) followed by a real final-answer delta with NO sibling field.
	fixture, err := os.ReadFile("../adapter/openai/testdata/glm_interleaved_reasoning_only.sse")
	if err != nil {
		t.Fatalf("read GLM fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(fixture)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
	}))
	defer srv.Close()

	// Build the real registry with openrouter available, pointed at the stub.
	// LLMMaxAttempts/PerAttemptTimeout/StreamIdleTimeout are left zero so the
	// resilience wrapper is inert (one attempt, no watchdog) — the test asserts
	// the TRANSLATION, not the resilience path.
	reg, err := buildProviderRegistry(Config{
		OpenRouterBaseURL: srv.URL,
		Diagnostics:       &recordingDiag{},
	}, fakeEnv(map[string]string{"OPENROUTER_API_KEY": "sk-openrouter"}))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	entry, ok := reg.Lookup(providerOpenRouter)
	if !ok {
		t.Fatal("openrouter entry missing")
	}
	if entry.remint == nil {
		t.Fatal("openrouter entry has no remint closure")
	}

	// Half 1 — catalog→composition: the helper resolves the catalog's
	// interleaved.field for z-ai/glm-5.2.
	if got := interleavedReasoningField(reg, providerOpenRouter, "z-ai/glm-5.2"); got != "reasoning_content" {
		t.Fatalf("interleavedReasoningField(openrouter, z-ai/glm-5.2) = %q, want \"reasoning_content\"", got)
	}

	// Half 2 — composition→adapter: the real remint closure threads the field
	// into the openai adapter via WithInterleavedReasoningField. The caps arg is
	// the GLM capability intersection (text-only); effort is "" (unset) so the
	// only knob differing from the shared default is the interleaved field.
	caps := modelCapability(reg, providerOpenRouter, "z-ai/glm-5.2")
	reminted := entry.remint("", caps, "reasoning_content")

	// Stream the GLM fixture through the re-minted provider and assert the
	// translated chunks: the reasoning_content-bearing deltas reclassify to
	// ChunkReasoning (NOT ChunkText), and the real final answer is the one
	// ChunkText. This is the property the marker-based discriminator delivers —
	// a flag-heuristic that blanket-reclassified every output_text.delta would
	// swallow the real answer into ChunkReasoning, leaving the turn textless.
	seq, err := reminted.Stream(context.Background(), port.LLMRequest{
		Model:    "z-ai/glm-5.2",
		Messages: []session.Message{session.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("Stream returned outer error: %v", err)
	}
	var got []port.Chunk
	for c, err := range seq {
		if err != nil {
			t.Fatalf("iterator surfaced error: %v", err)
		}
		got = append(got, c)
	}
	var reasoning, text []string
	for _, c := range got {
		switch c.Kind {
		case port.ChunkReasoning:
			reasoning = append(reasoning, c.Text)
		case port.ChunkText:
			text = append(text, c.Text)
		}
	}
	if joined := joinStrs(reasoning); joined != "Thinking step oneand step two" {
		t.Errorf("ChunkReasoning text = %q, want \"Thinking step oneand step two\" (the reasoning_content deltas reclassified)", joined)
	}
	if len(text) != 1 || text[0] != "Final answer." {
		t.Errorf("ChunkText = %v, want exactly [\"Final answer.\"] (the real answer delta WITHOUT the sibling field stayed visible)", text)
	}
}

// TestGLMInterleavedReasoningToolCallE2E is the composition end-to-end guard for
// issue #240 symptom #2 (tool calls after reasoning are swallowed). The
// reasoning-then-tool-call fixture carries reasoning_content deltas on differing
// content_index (0 then 1) followed by a real answer and a trailing
// response.output_item.done(function_call). Under the OLD code the differing
// content_index deltas tripped the single-visible-text-part guard and ABORTED the
// stream before the function_call's output_item.done was translated, so
// asst.ToolCalls was empty and the turn terminated as a "real answer". This test
// proves the marker-based discriminator keeps the guard off the reasoning deltas
// (so it sees only one visible-text identity) and the function_call survives to a
// ChunkToolCall, with NO iterator error — driven through the REAL composition seam
// (catalog → interleavedReasoningField → entry.remint → WithInterleavedReasoningField
// → streamState.interleavedField → translate), not just the unit-level decodeSSE path.
func TestGLMInterleavedReasoningToolCallE2E(t *testing.T) {
	fixture, err := os.ReadFile("../adapter/openai/testdata/glm_interleaved_reasoning.sse")
	if err != nil {
		t.Fatalf("read GLM fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(fixture)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
	}))
	defer srv.Close()

	reg, err := buildProviderRegistry(Config{
		OpenRouterBaseURL: srv.URL,
		Diagnostics:       &recordingDiag{},
	}, fakeEnv(map[string]string{"OPENROUTER_API_KEY": "sk-openrouter"}))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	entry, ok := reg.Lookup(providerOpenRouter)
	if !ok {
		t.Fatal("openrouter entry missing")
	}
	if got := interleavedReasoningField(reg, providerOpenRouter, "z-ai/glm-5.2"); got != "reasoning_content" {
		t.Fatalf("interleavedReasoningField(openrouter, z-ai/glm-5.2) = %q, want \"reasoning_content\"", got)
	}
	caps := modelCapability(reg, providerOpenRouter, "z-ai/glm-5.2")
	reminted := entry.remint("", caps, "reasoning_content")

	seq, err := reminted.Stream(context.Background(), port.LLMRequest{
		Model:    "z-ai/glm-5.2",
		Messages: []session.Message{session.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("Stream returned outer error: %v", err)
	}
	var got []port.Chunk
	for c, err := range seq {
		if err != nil {
			t.Fatalf("iterator surfaced error (the guard must NOT abort before the function_call): %v", err)
		}
		got = append(got, c)
	}
	// The function_call output_item.done must survive to a ChunkToolCall. Under
	// the old code the guard fired on the differing content_index reasoning deltas
	// and the stream errored before this event was translated.
	var toolCalls []session.ToolCall
	for _, c := range got {
		if c.Kind == port.ChunkToolCall && c.ToolCall != nil {
			toolCalls = append(toolCalls, *c.ToolCall)
		}
	}
	if len(toolCalls) != 1 || toolCalls[0].Name != "read_file" || string(toolCalls[0].Args) != `{"path":"a.go"}` {
		t.Fatalf("ChunkToolCall = %+v, want exactly one read_file call with {\"path\":\"a.go\"} (symptom #2 regression — the tool call was swallowed)", toolCalls)
	}
}

// joinStrs concatenates a slice of strings without separators (the test asserts
// the concatenation of the two reasoning deltas in stream order).
func joinStrs(ss []string) string {
	var b []byte
	for _, s := range ss {
		b = append(b, s...)
	}
	return string(b)
}
