package llmresilience

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/provider/anthropic"
	openaiadapter "github.com/stacklok/mecatl/provider/openai"
	"github.com/stacklok/mecatl/provider/openaichat"
)

func TestVisibleReasoning_Scenario3_SummaryEventsStayDisplayOnly(t *testing.T) {
	for _, variant := range []string{"response.reasoning_summary_text.delta", "response.reasoning_text.delta"} {
		t.Run(variant, func(t *testing.T) {
			const summary = "VISIBLE SUMMARY"
			const blob = "OPAQUE_REPLAY_BLOB"
			var requests [][]byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read body: %v", err)
					return
				}
				requests = append(requests, body)
				w.Header().Set("Content-Type", "text/event-stream")
				if len(requests) == 1 {
					_, _ = fmt.Fprintf(w, "event: %s\ndata: {\"type\":%q,\"sequence_number\":0,\"delta\":%q}\n\n", variant, variant, summary)
					_, _ = io.WriteString(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":1,\"item\":{\"type\":\"reasoning\",\"id\":\"rs_1\",\"encrypted_content\":\""+blob+"\"}}\n\n")
				}
				_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":2,\"delta\":\"answer\"}\n\n"+
					"event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":3,\"response\":{\"status\":\"completed\"}}\n\n")
			}))
			defer srv.Close()
			provider := openaiadapter.New(openaiadapter.WithAPIKey("test-key"), openaiadapter.WithBaseURL(srv.URL+"/v1"))
			eng := agent.NewEngine(agent.Deps{LLM: provider, Catalog: tool.NewCatalog(), Model: "gpt-test"})
			sess := session.New("summary-replay", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, session.Limits{}, time.Unix(0, 0))
			environment := tool.MustEnvironment(sess.EnvironmentRef, memfs.NewWorkspace("/ws"), memledger.New(), nil)
			run := func(s *session.Session, text string) string {
				t.Helper()
				var reasoning strings.Builder
				var result *session.ResultPayload
				for ev := range eng.Run(context.Background(), s, environment, agent.RunRequest{Text: text}).Events() {
					if ev.Type == session.EvReasoningDelta {
						reasoning.WriteString(ev.Text)
					}
					if ev.Type == session.EvResult {
						result = ev.Result
					}
				}
				if result == nil || result.Stop != session.StopEndTurn {
					t.Fatalf("turn result = %+v", result)
				}
				return reasoning.String()
			}
			if got := run(sess, "first"); got != summary {
				t.Errorf("reasoning event = %q, want %q", got, summary)
			}
			initial := sess.Conversation.Messages[len(sess.Conversation.Messages)-1].Reasoning
			if err := sess.Reopen(); err != nil {
				t.Fatalf("reopen: %v", err)
			}
			if got := run(sess, "second"); got != "" {
				t.Errorf("no-summary turn emitted %q", got)
			}
			store := memstore.New()
			if err := store.Save(context.Background(), sess); err != nil {
				t.Fatalf("save: %v", err)
			}
			restored, err := store.Load(context.Background(), sess.ID)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if len(restored.Conversation.Messages) < 2 {
				t.Fatalf("messages = %+v", restored.Conversation.Messages)
			}
			msg := restored.Conversation.Messages[1]
			if msg.Reasoning != initial || !strings.Contains(msg.Reasoning, blob) || strings.Contains(msg.Reasoning, summary) {
				t.Errorf("persisted replay differs from original opaque replay")
			}
			if err := restored.Reopen(); err != nil {
				t.Fatalf("reopen: %v", err)
			}
			if got := run(restored, "third"); got != "" {
				t.Errorf("no-summary turn emitted %q", got)
			}
			if len(requests) != 3 {
				t.Fatalf("requests = %d, want 3", len(requests))
			}
			for i, raw := range requests[1:] {
				var request struct {
					Input []map[string]any `json:"input"`
				}
				if err := json.Unmarshal(raw, &request); err != nil {
					t.Fatalf("request %d: %v", i+2, err)
				}
				found := false
				for _, item := range request.Input {
					if item["type"] == "reasoning" {
						if item["id"] == "rs_1" && item["encrypted_content"] == blob {
							found = true
						}
						if v, ok := item["summary"]; ok && fmt.Sprint(v) != "[]" {
							t.Errorf("request %d replay contains display summary: %v", i+2, item)
						}
					}
				}
				if !found || strings.Contains(string(raw), summary) {
					t.Errorf("request %d lost opaque replay or leaked display summary", i+2)
				}
			}
		})
	}
}

type rejectedSummaryTool struct{ calls int }

func (*rejectedSummaryTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Count", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*rejectedSummaryTool) ReadOnly() bool { return true }
func (t *rejectedSummaryTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	t.calls++
	return session.NewToolResult(call.ID, "executed"), nil
}

func TestUnsupportedSummaryHTTP400CannotExecuteTool(t *testing.T) {
	for _, tc := range []struct{ name, errorBody, private string }{
		{"unsupported", `{"error":{"message":"Unsupported parameter: reasoning.summary","type":"invalid_request_error"}}`, "reasoning.summary"},
		{"unrelated", `{"error":{"message":"model unavailable","type":"invalid_request_error"}}`, "model unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var payload struct {
					Reasoning struct {
						Summary string `json:"summary"`
					} `json:"reasoning"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Errorf("decode: %v", err)
				}
				if payload.Reasoning.Summary != "auto" {
					t.Errorf("summary = %q", payload.Reasoning.Summary)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, tc.errorBody)
			}))
			defer srv.Close()
			counter := &rejectedSummaryTool{}
			catalog := tool.NewCatalog()
			catalog.MustRegister(counter)
			provider := Wrap(openaiadapter.New(openaiadapter.WithAPIKey("test-key"), openaiadapter.WithBaseURL(srv.URL+"/v1")), Config{MaxAttempts: 3})
			eng := agent.NewEngine(agent.Deps{LLM: provider, Catalog: catalog, Model: "gpt-test"})
			sess := session.New("rejected-summary", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, session.Limits{}, time.Unix(0, 0))
			env := tool.MustEnvironment(sess.EnvironmentRef, memfs.NewWorkspace("/ws"), memledger.New(), nil)
			var result *session.ResultPayload
			for ev := range eng.Run(context.Background(), sess, env, agent.RunRequest{Text: "go"}).Events() {
				if ev.Type == session.EvResult {
					result = ev.Result
				}
				if ev.Type == session.EvMessageDelta || ev.Type == session.EvToolCall || ev.Type == session.EvToolResult || ev.Type == session.EvReasoningDelta {
					t.Errorf("fabricated output/tool event: %s", ev.Type)
				}
			}
			if result == nil || result.Stop != session.StopError || !strings.Contains(result.Error, "provider request failed (400 Bad Request)") || strings.Contains(result.Error, tc.private) {
				t.Errorf("result = %+v, want safe 400 without provider body %q", result, tc.private)
			}
			if requests != 1 || counter.calls != 0 {
				t.Errorf("requests = %d, tool executions = %d, want 1 and 0", requests, counter.calls)
			}
		})
	}
}

func TestVisibleReasoning_Scenario3_NonResponsesAdaptersOmitReasoningFields(t *testing.T) {
	for _, tc := range []struct {
		name        string
		newProvider func(string) port.LLMProvider
	}{
		{"messages", func(url string) port.LLMProvider {
			return anthropic.New(anthropic.WithAPIKey("test-key"), anthropic.WithBaseURL(url))
		}},
		{"chat", func(url string) port.LLMProvider {
			return openaichat.New(openaichat.WithAPIKey("test-key"), openaichat.WithBaseURL(url))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var err error
				body, err = io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read request: %v", err)
				}
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"message":"fixture error"}}`)
			}))
			defer srv.Close()
			seq, err := tc.newProvider(srv.URL).Stream(context.Background(), port.LLMRequest{Model: "test-model", Messages: []session.Message{session.NewUserMessage("hello")}})
			if err == nil {
				for range seq {
				}
			}
			if len(body) == 0 {
				t.Fatal("protocol adapter sent no HTTP request")
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("decode protocol request: %v", err)
			}
			if _, ok := payload["reasoning"]; ok {
				t.Errorf("Responses reasoning request leaked into %s", tc.name)
			}
			if _, ok := payload["include"]; ok {
				t.Errorf("Responses replay include leaked into %s", tc.name)
			}
		})
	}
}
