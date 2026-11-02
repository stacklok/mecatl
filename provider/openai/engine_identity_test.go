package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type canonicalReadTool struct {
	lifecycleReadTool
	calls []session.ToolCall
}

func (r *canonicalReadTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	r.calls = append(r.calls, call)
	return session.NewToolResult(call.ID, fmt.Sprintf("result-%d", len(r.calls))), nil
}

// Repeated wire IDs in one batch are invalid; completed-turn reuse remains an
// occurrence binding, covered by TestResponsesEngineCanonicalIdentityRoundTrip.
func TestResponsesEngineWireCallIDAdmission(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ids   []string
		valid bool
	}{
		{name: "empty", ids: []string{""}},
		{name: "valid-then-empty", ids: []string{"pair", ""}},
		{name: "repeated-in-batch", ids: []string{"pair", "pair"}},
		{name: "valid", ids: []string{"pair"}, valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body strings.Builder
			body.WriteString("data: {\"type\":\"response.output_text.delta\",\"delta\":\"tentative\"}\n\n")
			body.WriteString("data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"rs_admission\",\"type\":\"reasoning\",\"encrypted_content\":\"OPAQUE\"}}\n\n")
			for i, id := range tc.ids {
				// Distinct item IDs must not mask empty or repeated wire call_id.
				fmt.Fprintf(&body, "data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc_%d\",\"type\":\"function_call\",\"call_id\":%q,\"name\":\"Read\",\"arguments\":\"{\\\"path\\\":\\\"a.go\\\"}\"}}\n\n", i, id)
			}
			body.WriteString("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_admission\",\"status\":\"completed\",\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}\n\n")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, body.String())
			}))
			defer srv.Close()
			p := New(WithAPIKey("synthetic"), WithBaseURL(srv.URL))
			seq, err := p.Stream(t.Context(), port.LLMRequest{Model: "gpt-5.2", Messages: []port.LLMMessage{{Role: session.RoleUser, Text: "read"}}})
			if err != nil {
				t.Fatal(err)
			}
			var parsed []port.LLMToolCall
			done, replay := 0, 0
			for chunk, err := range seq {
				if err != nil {
					t.Fatal(err)
				}
				if chunk.Kind == port.ChunkToolCall {
					parsed = append(parsed, *chunk.ToolCall)
				}
				if chunk.Kind == port.ChunkReplay {
					replay++
				}
				if chunk.Kind == port.ChunkDone {
					done++
				}
			}
			if len(parsed) != len(tc.ids) || done != 1 || replay != 1 {
				t.Fatalf("parser calls=%+v done=%d replay=%d", parsed, done, replay)
			}
			for i, call := range parsed {
				if string(call.ID) != tc.ids[i] || string(call.ItemID) != fmt.Sprintf("fc_%d", i) {
					t.Fatalf("parser synthesized pairing identity: %+v", call)
				}
			}

			read := &canonicalReadTool{}
			catalog := tool.NewCatalog()
			catalog.MustRegister(read)
			eng := agent.NewEngine(agent.Deps{LLM: p, Catalog: catalog, Model: "gpt-5.2", Policy: permpolicy.NewPolicy([]governance.Rule{{Tool: "Read", Effect: governance.Allow}}, nil)})
			env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, memfs.NewWorkspace("/ws"), memledger.New(), nil)
			s := session.New("admission", session.ModeDefault, env.Ref(), session.Limits{MaxTurns: 1}, time.Unix(0, 0))
			calls, results := 0, 0
			var terminal *session.ResultPayload
			for ev := range eng.Run(t.Context(), s, env, agent.RunRequest{Text: "read"}).Events() {
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
			if !tc.valid {
				if terminal.Stop != session.StopError || terminal.Error == "" || len(read.calls) != 0 || calls != 0 || results != 0 {
					t.Fatalf("invalid identity escaped admission: result=%+v executions=%d calls=%d results=%d", terminal, len(read.calls), calls, results)
				}
				if len(s.Conversation.Messages) != 1 || s.Conversation.Messages[0].Role != session.RoleUser {
					t.Fatalf("invalid batch entered accepted history: %+v", s.Conversation.Messages)
				}
				return
			}
			if terminal.Error != "" || len(read.calls) != 1 || calls != 1 || results != 1 {
				t.Fatalf("valid batch rejected: result=%+v executions=%d calls=%d results=%d", terminal, len(read.calls), calls, results)
			}
			m := s.Conversation.Messages[1]
			if m.Replay == nil || m.Replay.Provider == nil || len(m.Replay.Bindings) != 1 {
				t.Fatalf("missing occurrence bindings: %+v", m)
			}
			for i, binding := range m.Replay.Bindings {
				if binding != (session.ToolCallBinding{CallID: read.calls[i].ID, PairingID: parsed[i].ID, ItemID: parsed[i].ItemID}) || binding.CallID == session.ToolCallID(binding.PairingID) || binding.CallID == session.ToolCallID(binding.ItemID) {
					t.Fatalf("identity domains conflated: %+v", binding)
				}
			}
			if err := session.ValidateToolPairing(s.Conversation.Messages); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestResponsesEngineCanonicalIdentityRoundTrip(t *testing.T) {
	toolSSE, err := os.ReadFile("testdata/interleaved_multipart_turn.sse")
	if err != nil {
		t.Fatal(err)
	}
	textSSE, err := os.ReadFile("testdata/text_turn.sse")
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan json.RawMessage, 3)
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input json.RawMessage `json:"input"`
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
		requests <- body.Input
		w.Header().Set("Content-Type", "text/event-stream")
		if turn <= 2 {
			// Reuse call_id, but keep each occurrence's private items distinct.
			sse := strings.NewReplacer("fc_1", fmt.Sprintf("fc_%d", turn), "rs_1", fmt.Sprintf("rs_%d", turn), "OPAQUE_REASONING", fmt.Sprintf("OPAQUE_%d", turn)).Replace(string(toolSSE))
			fmt.Fprintln(w, sse) // Complete the fixture's final SSE blank line.
		} else {
			_, _ = w.Write(textSSE)
		}
	}))
	defer srv.Close()

	read := &canonicalReadTool{}
	catalog := tool.NewCatalog()
	catalog.MustRegister(read)
	p := New(WithAPIKey("synthetic"), WithBaseURL(srv.URL))
	eng := agent.NewEngine(agent.Deps{LLM: p, Catalog: catalog, Model: "gpt-5.2", Policy: permpolicy.NewPolicy([]governance.Rule{{Tool: "Read", Effect: governance.Allow}}, nil)})
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, memfs.NewWorkspace("/ws"), memledger.New(), nil)
	s := session.New("canonical", session.ModeDefault, env.Ref(), session.Limits{MaxTurns: 3}, time.Unix(0, 0))
	var calls []session.ToolCallID
	var results []session.ToolResult
	var terminal *session.ResultPayload
	for ev := range eng.Run(t.Context(), s, env, agent.RunRequest{Text: "read twice"}).Events() {
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
	if terminal == nil || terminal.Stop != session.StopEndTurn || len(read.calls) != 2 || len(calls) != 2 || len(results) != 2 || count.Load() != 3 {
		t.Fatalf("terminal=%+v executed=%v calls=%v results=%v requests=%d", terminal, read.calls, calls, results, count.Load())
	}
	if read.calls[0].ID == read.calls[1].ID {
		t.Fatal("completed provider key reuse did not get a fresh canonical ID")
	}
	for i, call := range read.calls {
		if call.ID == "" || call.ID == "call_1" || call.ID == "fc_1" || call.ID == "fc_2" || call.ID != calls[i] || call.ID != results[i].CallID || call.Name != "Read" || string(call.Args) != `{"path":"a.go"}` {
			t.Fatalf("execution/event identity or arguments changed: %+v", call)
		}
		m, result := s.Conversation.Messages[1+2*i], s.Conversation.Messages[2+2*i]
		if len(m.ToolCalls) != 1 || m.ToolCalls[0].ID != call.ID || m.Replay == nil || len(m.Replay.Bindings) != 1 || m.Replay.Bindings[0] != (session.ToolCallBinding{CallID: call.ID, PairingID: "call_1", ItemID: session.ProviderItemID(fmt.Sprintf("fc_%d", i+1))}) || m.Replay.Provider == nil {
			t.Fatalf("history lost occurrence-bound identity/replay: %+v", m)
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

	want := `[{"content":"read twice","role":"user"}`
	for turn := range 3 {
		if turn > 0 {
			want += fmt.Sprintf(`,{"encrypted_content":"OPAQUE_%d","id":"rs_%d","summary":[],"type":"reasoning"},{"arguments":"{\"path\":\"a.go\"}","call_id":"call_1","id":"fc_%d","name":"Read","type":"function_call"},{"content":"firstsecond","phase":"future_phase","role":"assistant"},{"call_id":"call_1","output":"result-%d","type":"function_call_output"}`, turn, turn, turn, turn)
		}
		assertWireJSON(t, <-requests, []byte(want+"]"))
	}
}
