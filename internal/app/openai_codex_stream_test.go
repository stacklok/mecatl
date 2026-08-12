package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/llmresilience"
	"github.com/stacklok/mecatl/internal/adapter/openaicodex"
)

type codexFixtureTransport struct {
	status int
	body   []byte
	hits   atomic.Int32
	last   atomic.Pointer[codexCapturedRequest]
}

func (t *codexFixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.hits.Add(1)
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	t.last.Store(&codexCapturedRequest{
		method: req.Method,
		url:    req.URL.String(),
		header: req.Header.Clone(),
		body:   body,
	})
	status := t.status
	if status == 0 {
		status = http.StatusOK
	}
	contentType := "text/event-stream"
	if status != http.StatusOK {
		contentType = "application/json"
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(bytes.NewReader(t.body)),
		Request:    req,
	}, nil
}

func newCodexFixtureProvider(t *testing.T, transport http.RoundTripper, maxAttempts int) port.LLMProvider {
	t.Helper()
	credential := codexRegistryCredential(t)
	policy, err := openaicodex.NewRequestPolicy(credential, func() time.Time { return codexRegistryNow }, transport)
	if err != nil {
		t.Fatalf("NewRequestPolicy: %v", err)
	}
	entry := newOpenAICompatEntry(
		Config{LLMMaxAttempts: maxAttempts},
		providerOpenAICodex,
		"policy-owned",
		openaicodex.BaseURL,
		codexPolicyOptions(policy)...,
	)
	return entry.provider
}

func collectCodexChunks(ctx context.Context, t *testing.T, provider port.LLMProvider, messages []session.Message) ([]port.Chunk, error) {
	t.Helper()
	seq, err := provider.Stream(ctx, port.LLMRequest{Model: "gpt-5", Messages: messages})
	if err != nil {
		return nil, err
	}
	var chunks []port.Chunk
	for chunk, streamErr := range seq {
		if streamErr != nil {
			return chunks, streamErr
		}
		chunks = append(chunks, chunk)
	}
	return chunks, nil
}

func readCodexFixture(t *testing.T, path ...string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(path...))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return body
}

// TestOpenAICodexResponsesFixtures sends the sanitized subscription fixtures
// through the real Codex request policy and the shared provider/openai stream
// translator. The reasoning fixture is the shared translator's recorded proof
// for opaque encrypted-content replay; Codex has no second decoder.
func TestOpenAICodexResponsesFixtures(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		messages []session.Message
		assert   func(*testing.T, []port.Chunk, *codexCapturedRequest)
	}{
		{
			name:     "text phase usage and done",
			body:     readCodexFixture(t, "..", "..", "provider", "openai", "testdata", "subscription_compatibility_text.sse"),
			messages: []session.Message{session.NewUserMessage("hello")},
			assert: func(t *testing.T, chunks []port.Chunk, _ *codexCapturedRequest) {
				assertCodexChunkKinds(t, chunks, port.ChunkText, port.ChunkPhase, port.ChunkUsage, port.ChunkDone)
				if chunks[0].Text != "TEXT_SYNTHETIC" || chunks[1].Text != "final_answer" {
					t.Fatalf("text/phase = %q/%q, want TEXT_SYNTHETIC/final_answer", chunks[0].Text, chunks[1].Text)
				}
				assertCodexUsageAndDone(t, chunks[2], chunks[3], 1, 1, 0, 0)
			},
		},
		{
			name:     "tool call",
			body:     readCodexFixture(t, "..", "..", "provider", "openai", "testdata", "subscription_compatibility_tool_call.sse"),
			messages: []session.Message{session.NewUserMessage("call the tool")},
			assert: func(t *testing.T, chunks []port.Chunk, _ *codexCapturedRequest) {
				assertCodexChunkKinds(t, chunks, port.ChunkToolCall, port.ChunkUsage, port.ChunkDone)
				if got := chunks[0].ToolCall; got == nil || got.ID != "call_synthetic" || got.ItemID != "function_synthetic" || got.Name != "compatibility_probe" || string(got.Args) != `{"value":"OK"}` {
					t.Fatalf("translated tool call = %+v", got)
				}
				assertCodexUsageAndDone(t, chunks[1], chunks[2], 1, 1, 0, 0)
			},
		},
		{
			name: "tool result continuation",
			body: readCodexFixture(t, "..", "..", "provider", "openai", "testdata", "subscription_compatibility_tool_continuation.sse"),
			messages: []session.Message{
				session.NewUserMessage("call the tool"),
				func() session.Message {
					call := session.NewToolCall("call_synthetic", "compatibility_probe", json.RawMessage(`{"value":"OK"}`))
					call.ItemID = "function_synthetic"
					return session.NewAssistantMessage("", "", []session.ToolCall{call})
				}(),
				session.NewToolMessage(session.NewToolResult("call_synthetic", `{"value":"OK"}`)),
			},
			assert: func(t *testing.T, chunks []port.Chunk, request *codexCapturedRequest) {
				assertCodexChunkKinds(t, chunks, port.ChunkText, port.ChunkPhase, port.ChunkUsage, port.ChunkDone)
				if chunks[0].Text != "TOOL_RESULT_SYNTHETIC" || chunks[1].Text != "final_answer" {
					t.Fatalf("continuation text/phase = %q/%q, want TOOL_RESULT_SYNTHETIC/final_answer", chunks[0].Text, chunks[1].Text)
				}
				assertCodexUsageAndDone(t, chunks[2], chunks[3], 1, 1, 0, 0)
				assertCodexToolReplay(t, request.body)
			},
		},
		{
			name:     "opaque encrypted reasoning",
			body:     readCodexFixture(t, "..", "..", "provider", "openai", "testdata", "reasoning_turn.sse"),
			messages: []session.Message{session.NewUserMessage("reason")},
			assert: func(t *testing.T, chunks []port.Chunk, _ *codexCapturedRequest) {
				assertCodexChunkKinds(t, chunks, port.ChunkReasoning, port.ChunkReasoning, port.ChunkText, port.ChunkReasoningItem, port.ChunkUsage, port.ChunkDone)
				wantText := []string{"Let me think", " about this.", "Answer."}
				for i, want := range wantText {
					if chunks[i].Text != want {
						t.Fatalf("reasoning fixture chunk %d text = %q, want %q", i, chunks[i].Text, want)
					}
				}
				var envelope struct {
					Version int `json:"v"`
					Items   []struct {
						ID   string `json:"i"`
						Blob string `json:"e"`
					} `json:"items"`
				}
				if err := json.Unmarshal([]byte(chunks[3].Text), &envelope); err != nil {
					t.Fatalf("decode reasoning envelope: %v", err)
				}
				if envelope.Version != 1 || len(envelope.Items) != 1 || envelope.Items[0].ID != "rs_1" || envelope.Items[0].Blob != "ENCRYPTED_BLOB" {
					t.Fatalf("reasoning envelope = %+v, want v1 rs_1/ENCRYPTED_BLOB", envelope)
				}
				assertCodexUsageAndDone(t, chunks[4], chunks[5], 100, 50, 80, 40)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &codexFixtureTransport{body: tt.body}
			chunks, err := collectCodexChunks(context.Background(), t, newCodexFixtureProvider(t, transport, 1), tt.messages)
			if err != nil {
				t.Fatalf("stream fixture: %v", err)
			}
			request := transport.last.Load()
			if request == nil {
				t.Fatal("request was not captured")
			}
			tt.assert(t, chunks, request)
		})
	}

	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
			bodyReader, bodyWriter := io.Pipe()
			go func() {
				_, _ = io.WriteString(bodyWriter, "event: response.output_text.delta\n"+`data: {"type":"response.output_text.delta","sequence_number":0,"item_id":"message_synthetic","output_index":0,"content_index":0,"delta":"partial"}`+"\n\n")
				<-req.Context().Done()
				_ = bodyWriter.Close()
			}()
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: bodyReader, Request: req}, nil
		})
		provider := newCodexFixtureProvider(t, transport, 1)
		seq, err := provider.Stream(ctx, port.LLMRequest{Model: "gpt-5", Messages: []session.Message{session.NewUserMessage("cancel")}})
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		var got []port.Chunk
		for chunk, streamErr := range seq {
			if streamErr != nil {
				t.Fatalf("cancellation surfaced as stream error: %v", streamErr)
			}
			got = append(got, chunk)
			cancel()
		}
		if len(got) != 1 || got[0].Kind != port.ChunkText || got[0].Text != "partial" {
			t.Fatalf("chunks before cancellation = %+v, want exact partial text chunk", got)
		}
	})
}

func assertCodexChunkKinds(t *testing.T, chunks []port.Chunk, want ...port.ChunkKind) {
	t.Helper()
	if len(chunks) != len(want) {
		t.Fatalf("chunk count = %d, want %d: %+v", len(chunks), len(want), chunks)
	}
	for i := range want {
		if chunks[i].Kind != want[i] {
			t.Errorf("chunk %d kind = %v, want %v", i, chunks[i].Kind, want[i])
		}
	}
}

func assertCodexUsageAndDone(t *testing.T, usageChunk, doneChunk port.Chunk, input, output, cacheRead, reasoning int) {
	t.Helper()
	got := usageChunk.Usage
	if got == nil || got.InputTokens != input || got.OutputTokens != output ||
		got.CacheReadTokens != cacheRead || got.ReasoningTokens != reasoning {
		t.Fatalf("usage = %+v, want input=%d output=%d cache=%d reasoning=%d", got, input, output, cacheRead, reasoning)
	}
	if doneChunk.Stop != session.StopEndTurn {
		t.Fatalf("done stop = %q, want %q", doneChunk.Stop, session.StopEndTurn)
	}
}

func assertCodexToolReplay(t *testing.T, body []byte) {
	t.Helper()
	var request struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("decode continuation request: %v", err)
	}
	var functionCall, functionOutput bool
	for _, item := range request.Input {
		switch item["type"] {
		case "function_call":
			if item["id"] == "function_synthetic" && item["call_id"] == "call_synthetic" &&
				item["name"] == "compatibility_probe" && item["arguments"] == `{"value":"OK"}` {
				functionCall = true
			}
		case "function_call_output":
			if item["call_id"] == "call_synthetic" && item["output"] == `{"value":"OK"}` {
				functionOutput = true
			}
		}
	}
	if !functionCall || !functionOutput {
		t.Fatalf("exact function call/output replay missing (call=%t output=%t): %s", functionCall, functionOutput, body)
	}
}

// TestCodexActionableEventPolicy is the pre-commit compatibility gate: every
// event in the three sanitized Codex fixtures is either translated by the
// shared adapter or is known assembly/metadata. A newly recorded actionable
// event therefore fails this test until the shared neutral mapping is decided.
func TestCodexActionableEventPolicy(t *testing.T) {
	known := map[string]bool{
		"response.created":                       true,
		"response.function_call_arguments.delta": true,
		"response.output_text.delta":             true,
		"response.output_item.done":              true,
		"response.completed":                     true,
	}
	for _, name := range []string{
		"subscription_compatibility_text.sse",
		"subscription_compatibility_tool_call.sse",
		"subscription_compatibility_tool_continuation.sse",
	} {
		body := readCodexFixture(t, "..", "..", "provider", "openai", "testdata", name)
		if err := validateCodexFixtureEvents(body, known); err != nil {
			t.Errorf("%s: %v; add a shared neutral translator case or reject the compatibility candidate", name, err)
		}
	}

	// Mutation guards for the pre-commit gate itself. These are intentionally
	// separate from provider/openai's runtime policy, where harmless unknown
	// metadata remains forward-compatibly ignored.
	for _, tc := range []struct {
		name, body, want string
	}{
		{
			name: "unknown data type under known label",
			body: "event: response.created\n" +
				`data: {"type":"response.provider_action.required"}` + "\n\n",
			want: "unsupported actionable event",
		},
		{
			name: "data-only unknown type",
			body: `data: {"type":"response.provider_action.required"}` + "\n\n",
			want: "unsupported actionable event",
		},
		{
			name: "known label and data mismatch",
			body: "event: response.created\n" +
				`data: {"type":"response.completed"}` + "\n\n",
			want: "does not match data type",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCodexFixtureEvents([]byte(tc.body), known)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("fixture policy error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func validateCodexFixtureEvents(body []byte, known map[string]bool) error {
	var eventLabel string
	labelHasData := false
	for lineNumber, rawLine := range strings.Split(string(body), "\n") {
		line := strings.TrimSuffix(rawLine, "\r")
		if line == "" {
			if eventLabel != "" && !labelHasData {
				return fmt.Errorf("line %d: event label %q has no data object", lineNumber+1, eventLabel)
			}
			eventLabel, labelHasData = "", false
			continue
		}
		if strings.HasPrefix(line, "event:") {
			if eventLabel != "" && !labelHasData {
				return fmt.Errorf("line %d: event label %q has no data object", lineNumber+1, eventLabel)
			}
			eventLabel = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			labelHasData = false
			if eventLabel == "" {
				return fmt.Errorf("line %d: empty event label", lineNumber+1)
			}
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &envelope); err != nil {
			return fmt.Errorf("line %d: decode event data: %w", lineNumber+1, err)
		}
		var dataType string
		if rawType, ok := envelope["type"]; !ok || json.Unmarshal(rawType, &dataType) != nil || dataType == "" {
			return fmt.Errorf("line %d: event data has no nonempty string type", lineNumber+1)
		}
		if !known[dataType] {
			return fmt.Errorf("line %d: unsupported actionable event %q", lineNumber+1, dataType)
		}
		if eventLabel != "" && eventLabel != dataType {
			return fmt.Errorf("line %d: event label %q does not match data type %q", lineNumber+1, eventLabel, dataType)
		}
		labelHasData = true
	}
	if eventLabel != "" && !labelHasData {
		return fmt.Errorf("event label %q has no data object", eventLabel)
	}
	return nil
}

// TestOpenAICodexRetryCounts proves retry ownership stays entirely in the outer
// resilience wrapper: permanent auth errors make one HTTP request, while a
// transient response makes exactly the configured number of outer attempts.
func TestOpenAICodexRetryCounts(t *testing.T) {
	for _, tc := range []struct {
		status       int
		maxAttempts  int
		wantRequests int32
		wantExhaust  bool
	}{
		{status: http.StatusUnauthorized, maxAttempts: 3, wantRequests: 1},
		{status: http.StatusForbidden, maxAttempts: 3, wantRequests: 1},
		{status: http.StatusTooManyRequests, maxAttempts: 2, wantRequests: 2, wantExhaust: true},
		{status: http.StatusInternalServerError, maxAttempts: 2, wantRequests: 2, wantExhaust: true},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			transport := &codexFixtureTransport{
				status: tc.status,
				body:   []byte(`{"error":{"message":"synthetic failure","type":"synthetic","code":"synthetic"}}`),
			}
			_, err := collectCodexChunks(context.Background(), t, newCodexFixtureProvider(t, transport, tc.maxAttempts), []session.Message{session.NewUserMessage("retry")})
			if err == nil {
				t.Fatal("stream unexpectedly succeeded")
			}
			var exhausted *llmresilience.ExhaustedError
			if errors.As(err, &exhausted) != tc.wantExhaust {
				t.Fatalf("ExhaustedError = %t, want %t: %v", errors.As(err, &exhausted), tc.wantExhaust, err)
			}
			if got := transport.hits.Load(); got != tc.wantRequests {
				t.Fatalf("HTTP requests = %d, want %d (SDK retry multiplication detected)", got, tc.wantRequests)
			}
			request := transport.last.Load()
			if request == nil {
				t.Fatal("request was not captured")
			}
			if request.header.Get("X-Stainless-Retry-Count") != "0" {
				t.Fatalf("SDK retry-count header = %q, want 0", request.header.Get("X-Stainless-Retry-Count"))
			}
		})
	}
}
