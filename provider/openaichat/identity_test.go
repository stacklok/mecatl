package openaichat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestFragmentedCallsPairResultsAcrossReusedProviderKeys(t *testing.T) {
	const sse = `data: {"id":"chat_completion","choices":[{"index":0,"delta":{"reasoning_content":"display only","tool_calls":[{"index":4,"id":"provider_b","type":"function","function":{"name":"lookup","arguments":"{\"key\":"}},{"index":1,"id":"provider_a","type":"function","function":{"name":"lookup","arguments":"{\"key\":"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"\"a\"}"}},{"index":4,"function":{"arguments":"\"b\"}"}}]}}]}

data: {"choices":[{"index":0,"finish_reason":"tool_calls","delta":{}}]}

data: [DONE]

`
	requests := make(chan []byte, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- raw
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()
	p := New(WithAPIKey("synthetic"), WithBaseURL(srv.URL))
	req := port.LLMRequest{Model: "test", Messages: []port.LLMMessage{{Role: session.RoleUser, Text: "look up both keys"}}}
	for turn := range 2 {
		seq, err := p.Stream(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		var calls []port.LLMToolCall
		var reasoning string
		for chunk, err := range seq {
			if err != nil {
				t.Fatal(err)
			}
			switch chunk.Kind {
			case port.ChunkToolCall:
				if chunk.ToolCall == nil {
					t.Fatal("nil tool call")
				}
				calls = append(calls, *chunk.ToolCall)
			case port.ChunkReasoning:
				reasoning += chunk.Text
			case port.ChunkReplay:
				t.Fatal("Chat reasoning must not create private replay")
			}
		}
		if len(calls) != 2 {
			t.Fatalf("turn %d: got %d calls, want two assembled calls", turn, len(calls))
		}
		if reasoning != "display only" {
			t.Fatalf("reasoning = %q", reasoning)
		}
		for i, want := range []struct{ id, args string }{{"provider_b", `{"key":"b"}`}, {"provider_a", `{"key":"a"}`}} {
			if string(calls[i].ID) != want.id || string(calls[i].Args) != want.args || calls[i].Name != "lookup" || calls[i].ItemID != "" {
				t.Fatalf("turn %d call %d = %+v, args %q", turn, i, calls[i], calls[i].Args)
			}
		}
		req.Messages = append(req.Messages, port.LLMMessage{Role: session.RoleAssistant, ToolCalls: calls})
		// Results arrive in reverse call order; pairing must use the exact token,
		// never the function name, index, completion ID, or previous occurrence.
		for i := len(calls) - 1; i >= 0; i-- {
			req.Messages = append(req.Messages, port.LLMMessage{Role: session.RoleTool, ToolResult: &port.LLMToolResult{
				CallID: calls[i].ID, Content: fmt.Sprintf("turn %d result %s", turn, calls[i].ID),
			}})
		}
		raw := <-requests
		if turn == 1 {
			var wire map[string]any
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			assertPairedChatTurns(t, wire, 1)
		}
	}
	assertPairedChatTurns(t, marshalParams(t, req, ""), 2)
}

func assertPairedChatTurns(t *testing.T, wire map[string]any, turns int) {
	t.Helper()
	messages := wire["messages"].([]any)
	if len(messages) != 1+3*turns {
		t.Fatalf("wire has %d messages, want %d", len(messages), 1+3*turns)
	}
	for turn := range turns {
		assistant := messages[1+3*turn].(map[string]any)
		calls := assistant["tool_calls"].([]any)
		if len(calls) != 2 {
			t.Fatalf("turn %d: wire has %d calls", turn, len(calls))
		}
		for i, id := range []string{"provider_b", "provider_a"} {
			if calls[i].(map[string]any)["id"] != id {
				t.Fatalf("turn %d: call %d lost exact provider token", turn, i)
			}
			result := messages[3+3*turn-i].(map[string]any)
			if result["role"] != "tool" || result["tool_call_id"] != id || result["content"] != fmt.Sprintf("turn %d result %s", turn, id) {
				t.Fatalf("turn %d: mismatched result %v", turn, result)
			}
		}
	}
}

func TestMissingProviderCallIDRemainsMissing(t *testing.T) {
	const sse = `data: {"id":"chat_completion_not_a_call_id","choices":[{"index":0,"delta":{"tool_calls":[{"index":7,"type":"function","function":{"name":"lookup","arguments":"{"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":7,"function":{"arguments":"}"}}]}}]}

data: {"choices":[{"index":0,"finish_reason":"tool_calls","delta":{}}]}

data: [DONE]

`
	chunks, err := decodeSSE(strings.NewReader(sse))
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	for _, chunk := range chunks {
		if chunk.Kind != port.ChunkToolCall {
			continue
		}
		calls++
		if chunk.ToolCall == nil || chunk.ToolCall.ID != "" || chunk.ToolCall.ItemID != "" || string(chunk.ToolCall.Args) != "{}" || chunk.ToolCall.Name != "lookup" {
			t.Fatalf("missing-ID fragments were altered: %+v", chunk.ToolCall)
		}
	}
	if calls != 1 {
		t.Fatalf("got %d calls, want one assembled missing-ID call for engine validation", calls)
	}
}

func TestDuplicateProviderKeysRemainSeparateCalls(t *testing.T) {
	const sse = `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"duplicate","type":"function","function":{"name":"first","arguments":"{"}},{"index":1,"id":"duplicate","type":"function","function":{"name":"second","arguments":"{"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"}"}},{"index":0,"function":{"arguments":"}"}}]}}]}

data: {"choices":[{"index":0,"finish_reason":"tool_calls","delta":{}}]}

data: [DONE]

`
	chunks, err := decodeSSE(strings.NewReader(sse))
	if err != nil {
		t.Fatal(err)
	}
	var calls []port.LLMToolCall
	for _, chunk := range chunks {
		if chunk.Kind == port.ChunkToolCall {
			if chunk.ToolCall == nil {
				t.Fatal("nil tool call")
			}
			calls = append(calls, *chunk.ToolCall)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("duplicate provider IDs collapsed: got %d calls", len(calls))
	}
	for i, name := range []string{"first", "second"} {
		if calls[i].ID != "duplicate" || calls[i].Name != name || string(calls[i].Args) != "{}" {
			t.Fatalf("call %d altered before engine batch validation: %+v", i, calls[i])
		}
	}
}

func TestUnsupportedOpaqueReplayDoesNotReachChatWire(t *testing.T) {
	for _, replay := range []session.ProviderReplay{
		{Format: "openai.responses", Version: 1, Data: []byte(`{"encrypted_content":"private-canary"}`)},
		{Format: "anthropic", Version: 1, Data: []byte(`{"signature":"private-canary"}`)},
		{Format: "future-chat", Version: 99, Data: []byte{0xff, 0x00}},
	} {
		t.Run(replay.Format, func(t *testing.T) {
			req := port.LLMRequest{Model: "test", Messages: []port.LLMMessage{
				{Role: session.RoleAssistant, Text: "visible", ToolCalls: []port.LLMToolCall{{ID: "provider_exact", ItemID: "private_item", Name: "lookup"}}},
				{Role: session.RoleTool, ToolResult: &port.LLMToolResult{CallID: "provider_exact", Content: "result"}},
			}}
			p := New(WithAPIKey("synthetic"), WithCacheDialect(CacheDialectOpenAI))
			baseline := marshalMethodParams(t, p, req)
			req.Messages[0].Replay = &replay
			req.Messages[0].ProviderPhase = "private_phase"
			if got := marshalMethodParams(t, p, req); got != baseline {
				t.Fatalf("unsupported replay changed wire: %s", got)
			}
		})
	}
}
