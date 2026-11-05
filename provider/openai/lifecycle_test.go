package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type lifecycleReadTool struct{}

func (lifecycleReadTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Read", Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`)}
}
func (lifecycleReadTool) ReadOnly() bool { return true }
func (lifecycleReadTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(call.ID, "read"), nil
}

func TestResponsesEngineSnapshotReplayLifecycle(t *testing.T) {
	toolSSE, err := os.ReadFile("testdata/interleaved_multipart_turn.sse")
	if err != nil {
		t.Fatal(err)
	}
	toolSSE = append(toolSSE, '\n')
	textSSE, err := os.ReadFile("testdata/text_turn.sse")
	if err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "legacy"}[legacy], func(t *testing.T) {
			var requests [][]map[string]any
			serveTool := !legacy
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Input []map[string]any `json:"input"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				requests = append(requests, req.Input)
				w.Header().Set("Content-Type", "text/event-stream")
				if serveTool {
					_, _ = w.Write(toolSSE)
					serveTool = false
				} else {
					_, _ = w.Write(textSSE)
				}
			}))
			defer srv.Close()
			p := New(WithAPIKey("fixture"), WithBaseURL(srv.URL))
			env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, memfs.NewWorkspace("/ws"), memledger.New(), nil)
			catalog := tool.NewCatalog()
			catalog.MustRegister(lifecycleReadTool{})
			eng := agent.NewEngine(agent.Deps{LLM: p, Catalog: catalog, Model: "gpt-5.2", Policy: permpolicy.NewPolicy([]governance.Rule{{Tool: "Read", Effect: governance.Allow}}, nil)})
			var s *session.Session
			if legacy {
				s, err = sessnap.Unmarshal([]byte(`{"id":"old","state":"idle","mode":"default","environment_ref":{"Kind":"mem","ID":"/ws","Revision":"v1"},"created_at":"2023-11-14T22:13:20Z","messages":[{"role":"user","text":"go"},{"role":"assistant","text":"firstsecond","reasoning":"{\"v\":1,\"items\":[{\"i\":\"rs_1\",\"e\":\"OPAQUE_REASONING\"}]}","reasoning_item_id":"stale-ignored","phase":"future_phase","tool_calls":[{"ID":"call_1","Name":"Read","Args":{"path":"a.go"},"item_id":"fc_1"}]},{"role":"tool","tool_result":{"CallID":"call_1","Content":"read"}}]}`))
				if err != nil {
					t.Fatal(err)
				}
			} else {
				s = session.New("current", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
				for ev := range eng.Run(t.Context(), s, env, agent.RunRequest{Text: "go"}).Events() {
					if ev.Result != nil && ev.Result.Error != "" {
						t.Fatalf("initial engine error: %s", ev.Result.Error)
					}
				}
				if s.State != session.StateCompleted {
					t.Fatalf("initial state = %s", s.State)
				}
			}
			wire, err := sessnap.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			s, err = sessnap.Unmarshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			// Recreate the adapter and engine: no in-process replay cache survives.
			p = New(WithAPIKey("fixture"), WithBaseURL(srv.URL))
			eng = agent.NewEngine(agent.Deps{LLM: p, Catalog: catalog, Model: "gpt-5.2", Policy: permpolicy.NewPolicy([]governance.Rule{{Tool: "Read", Effect: governance.Allow}}, nil)})
			want := session.CloneMessages(s.Conversation.Messages)
			if s.State == session.StateCompleted {
				if err := s.Reopen(); err != nil {
					t.Fatal(err)
				}
			}
			for range eng.Run(t.Context(), s, env, agent.RunRequest{Text: "resume"}).Events() {
			}
			last := requests[len(requests)-1]
			foundReasoning, foundCall, foundResult, foundPhase := false, false, false, false
			for _, item := range last {
				if item["phase"] == "future_phase" {
					foundPhase = true
				}
				switch item["type"] {
				case "reasoning":
					foundReasoning = item["id"] == "rs_1" && item["encrypted_content"] == "OPAQUE_REASONING"
				case "function_call":
					foundCall = item["id"] == "fc_1" && item["call_id"] == "call_1"
				case "function_call_output":
					foundResult = item["call_id"] == "call_1"
				case "message":
					if item["phase"] == "future_phase" {
						foundPhase = true
					}
				}
			}
			if !foundReasoning || !foundCall || !foundResult || !foundPhase {
				t.Fatalf("restart replay flags reasoning=%v call=%v result=%v phase=%v", foundReasoning, foundCall, foundResult, foundPhase)
			}
			if !reflect.DeepEqual(s.Conversation.Messages[:len(want)], want) {
				t.Fatal("resume mutated prior carrier or canonical identity")
			}

			// Cross-provider carryover keeps canonical identity, not private state.
			source := session.CloneMessages(want)
			for i := range source {
				if source[i].Replay != nil {
					source[i].Replay.Provider = &session.ProviderReplay{Format: "anthropic.messages", Version: 1, Data: []byte(`{"v":1,"blocks":[{"t":"thinking","x":"source thought","s":"source signature"}]}`)}
				}
			}
			target := session.New("target", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
			if err := target.SeedHistory(session.StripProviderState(source)); err != nil {
				t.Fatal(err)
			}
			seed, err := port.ProjectMessages(target.Conversation.Messages)
			if err != nil {
				t.Fatal(err)
			}
			var sourcePair, sourceItem string
			for _, message := range seed {
				if len(message.ToolCalls) > 0 {
					sourcePair = string(message.ToolCalls[0].ID)
					break
				}
			}
			if sourcePair == "" || sourcePair == "call_1" {
				t.Fatal("target pairing was not reconstructed")
			}
			for range 2 {
				serveTool = true
				if target.State == session.StateCompleted {
					if err := target.Reopen(); err != nil {
						t.Fatal(err)
					}
				}
				for range eng.Run(t.Context(), target, env, agent.RunRequest{Text: "target turn"}).Events() {
				}
				if target.State != session.StateCompleted {
					t.Fatalf("target state = %s", target.State)
				}
				foundCall, foundResult := false, false
				for _, item := range requests[len(requests)-1] {
					if item["call_id"] != sourcePair {
						continue
					}
					if item["type"] == "function_call" {
						id, ok := item["id"].(string)
						if !ok || id == "" || id == "fc_1" || (sourceItem != "" && id != sourceItem) {
							t.Fatal("target item missing, unstable or colliding")
						}
						sourceItem, foundCall = id, true
					}
					if item["type"] == "function_call_output" {
						foundResult = true
					}
				}
				if !foundCall || !foundResult {
					t.Fatal("target turn did not replay exact reconstructed pairing")
				}
			}
			projected, err := port.ProjectMessages(target.Conversation.Messages)
			if err != nil {
				t.Fatal(err)
			}
			params, err := p.buildParams(port.LLMRequest{Model: "gpt-5.2", Messages: projected})
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(params.Input)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte("stale-ignored")) || bytes.Contains(data, []byte("source signature")) {
				t.Fatal("legacy id reactivated")
			}
			var input []map[string]any
			if err := json.Unmarshal(data, &input); err != nil {
				t.Fatal(err)
			}
			pairs := map[string]int{}
			var synthetic string
			for _, item := range input {
				if item["type"] == "function_call" {
					pairs[item["call_id"].(string)]++
					if item["call_id"] != "call_1" {
						synthetic = item["id"].(string)
					}
				}
				if item["type"] == "function_call_output" {
					pairs[item["call_id"].(string)]--
				}
			}
			if synthetic == "" || synthetic == "fc_1" {
				t.Fatal("target adapter did not synthesize a collision-safe item id")
			}
			for _, n := range pairs {
				if n != 0 {
					t.Fatal("target pairing mismatch")
				}
			}
			// Direct auxiliary model callers cross the same DTO/adapter boundary.
			seq, err := p.Stream(t.Context(), port.LLMRequest{Model: "gpt-5.2", Messages: projected})
			if err != nil {
				t.Fatal(err)
			}
			for _, err := range seq {
				if err != nil {
					t.Fatal(err)
				}
			}
			if !reflect.DeepEqual(requests[len(requests)-1], input) {
				t.Fatal("auxiliary request changed target replay")
			}
			conv := &session.Conversation{Messages: []session.Message{session.NewUserMessage("goal")}}
			for range 12 {
				conv.Append(session.NewUserMessage(strings.Repeat("old request ", 40)))
				conv.Append(session.NewAssistantMessage(strings.Repeat("old answer ", 40), nil))
			}
			for _, message := range target.Conversation.Messages {
				conv.Append(message)
			}
			for range 3 {
				conv.Append(session.NewUserMessage("recent tail"))
			}
			compactor := agent.CascadeCompactor{LLM: p, Model: "gpt-5.2", BudgetTokens: 1, KeepLastTurns: 1}
			before := session.CloneMessages(conv.Messages)
			compacted, _, usage, err := compactor.Compact(t.Context(), conv)
			if err != nil || len(usage.Buckets) == 0 {
				t.Fatalf("cascade summary failed: %v", err)
			}
			if err := session.ValidateToolPairing(compacted); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(conv.Messages, before) {
				t.Fatal("cascade mutated source replay")
			}
			foundSynthetic := false
			for _, item := range requests[len(requests)-1] {
				if item["type"] == "function_call" && item["call_id"] != "call_1" && item["id"] != "fc_1" {
					foundSynthetic = true
				}
			}
			if !foundSynthetic {
				t.Fatal("cascade bypassed target item reconstruction")
			}
		})
	}
}
