package openaichat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type canonicalLookupTool struct {
	calls []session.ToolCall
}

func (*canonicalLookupTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "lookup", Schema: json.RawMessage(`{"type":"object","properties":{"key":{"type":"string"}},"required":["key"],"additionalProperties":false}`)}
}
func (*canonicalLookupTool) ReadOnly() bool { return true }
func (r *canonicalLookupTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	r.calls = append(r.calls, call)
	return session.NewToolResult(call.ID, fmt.Sprintf("result-%d", len(r.calls))), nil
}

func TestChatEngineCanonicalIdentityRoundTrip(t *testing.T) {
	const toolSSE = `data: {"id":"chat_completion","choices":[{"index":0,"delta":{"reasoning_content":"display only","tool_calls":[{"index":4,"id":"provider_reused","type":"function","function":{"name":"lookup","arguments":"{\"key\":"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":4,"function":{"arguments":"\"a\"}"}}]}}]}

data: {"choices":[{"index":0,"finish_reason":"tool_calls","delta":{}}]}

data: [DONE]

`
	textSSE, err := os.ReadFile("testdata/stream_text.sse")
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan []json.RawMessage, 3)
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		turn := count.Add(1)
		if turn > 3 {
			t.Error("unexpected extra HTTP request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- body.Messages
		w.Header().Set("Content-Type", "text/event-stream")
		if turn <= 2 {
			_, _ = io.WriteString(w, toolSSE)
		} else {
			_, _ = w.Write(textSSE)
		}
	}))
	defer srv.Close()

	lookup := &canonicalLookupTool{}
	catalog := tool.NewCatalog()
	catalog.MustRegister(lookup)
	p := New(WithAPIKey("synthetic"), WithBaseURL(srv.URL))
	eng := agent.NewEngine(agent.Deps{LLM: p, Catalog: catalog, Model: "test", Policy: permpolicy.NewPolicy([]governance.Rule{{Tool: "lookup", Effect: governance.Allow}}, nil)})
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, memfs.NewWorkspace("/ws"), memledger.New(), nil)
	s := session.New("canonical", session.ModeDefault, env.Ref(), session.Limits{MaxTurns: 3}, time.Unix(0, 0))
	var calls []session.ToolCallID
	var results []session.ToolResult
	var terminal *session.ResultPayload
	for ev := range eng.Run(t.Context(), s, env, agent.RunRequest{Text: "look up twice"}).Events() {
		if ev.Type == session.EvToolCall {
			calls = append(calls, ev.ToolCall.ID)
		}
		if ev.Type == session.EvToolResult {
			results = append(results, *ev.ToolResult)
		}
		if ev.Type == session.EvResult {
			terminal = ev.Result
		}
	}
	if terminal == nil || terminal.Stop != session.StopEndTurn || len(lookup.calls) != 2 || len(calls) != 2 || len(results) != 2 || count.Load() != 3 {
		t.Fatalf("terminal=%+v executed=%v calls=%v results=%v requests=%d", terminal, lookup.calls, calls, results, count.Load())
	}
	if lookup.calls[0].ID == lookup.calls[1].ID {
		t.Fatal("completed provider key reuse did not get a fresh canonical ID")
	}
	for i, call := range lookup.calls {
		if call.ID == "" || call.ID == "provider_reused" || call.ID == "chat_completion" || call.ID != calls[i] || call.ID != results[i].CallID || call.Name != "lookup" || string(call.Args) != `{"key":"a"}` {
			t.Fatalf("execution/event identity or fragmented arguments changed: %+v", call)
		}
		m, result := s.Conversation.Messages[1+2*i], s.Conversation.Messages[2+2*i]
		if len(m.ToolCalls) != 1 || m.ToolCalls[0].ID != call.ID || m.Replay == nil || len(m.Replay.Bindings) != 1 || m.Replay.Bindings[0] != (session.ToolCallBinding{CallID: call.ID, PairingID: "provider_reused"}) || m.Replay.Provider != nil {
			t.Fatalf("history lost occurrence binding or fabricated private replay: %+v", m)
		}
		if result.ToolResult == nil || result.ToolResult.CallID != call.ID || result.ToolResult.IsError || result.ToolResult.Content != fmt.Sprintf("result-%d", i+1) {
			t.Fatalf("recorded result mismatched execution: %+v", result)
		}
		if results[i].Content != result.ToolResult.Content || results[i].IsError != result.ToolResult.IsError {
			t.Fatalf("event result mismatched recorded result: %+v", results[i])
		}
	}
	if err := session.ValidateToolPairing(s.Conversation.Messages); err != nil {
		t.Fatal(err)
	}

	for turn := range 3 {
		messages := <-requests
		if len(messages) != 2+2*turn {
			t.Fatalf("request %d has %d messages, want %d including system prompt", turn+1, len(messages), 2+2*turn)
		}
		var system struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(messages[0], &system); err != nil {
			t.Fatal(err)
		}
		if system.Role != "system" {
			t.Fatalf("expected leading system prompt, got %s", messages[0])
		}
		messages = messages[1:]
		for occurrence := 1; occurrence <= turn; occurrence++ {
			want := []string{
				`{"role":"assistant","tool_calls":[{"id":"provider_reused","type":"function","function":{"name":"lookup","arguments":"{\"key\":\"a\"}"}}]}`,
				fmt.Sprintf(`{"role":"tool","tool_call_id":"provider_reused","content":"result-%d"}`, occurrence),
			}
			for i, expected := range want {
				var gotJSON, wantJSON any
				if err := json.Unmarshal(messages[2*occurrence-1+i], &gotJSON); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(expected), &wantJSON); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(gotJSON, wantJSON) {
					t.Fatalf("request %d occurrence %d wire mismatch: got %s, want %s", turn+1, occurrence, messages[2*occurrence-1+i], expected)
				}
			}
		}
	}
}
