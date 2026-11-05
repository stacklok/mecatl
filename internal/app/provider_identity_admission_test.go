package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/provider/anthropic"
	"github.com/stacklok/mecatl/provider/openai"
)

type invalidIdentityPolicy struct{ calls int }

func (p *invalidIdentityPolicy) Evaluate(context.Context, session.SessionID, session.PermissionMode, session.ToolCall, tool.WorkspaceReader) port.PermissionResult {
	p.calls++
	return port.PermissionResult{Decision: governance.PermissionDecision{Effect: governance.Ask}}
}
func (*invalidIdentityPolicy) Learn(session.SessionID, session.ToolCall) {}

type invalidIdentityHooks struct{ calls int }

func (h *invalidIdentityHooks) Run(_ context.Context, e governance.HookEvent) (governance.HookOutcome, error) {
	if e.Phase == governance.PhasePreToolUse || e.Phase == governance.PhasePostToolUse {
		h.calls++
	}
	return governance.HookOutcome{}, nil
}

func TestRealAdaptersRejectInvalidIdentityBeforeDispatch(t *testing.T) {
	for _, provider := range []string{"responses", "anthropic"} {
		for _, duplicate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/duplicate=%t", provider, duplicate), func(t *testing.T) {
				var sse string
				if provider == "responses" {
					sse = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp\",\"status\":\"in_progress\"}}\n\n"
					ids := []string{""}
					if duplicate {
						ids = []string{"duplicate", "duplicate"}
					}
					for i, id := range ids {
						sse += fmt.Sprintf("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":%d,\"item\":{\"type\":\"function_call\",\"id\":\"valid-item-%d\",\"call_id\":%q,\"name\":\"Write\",\"arguments\":\"{}\"}}\n\n", i, i, id)
					}
					sse += "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp\",\"status\":\"completed\",\"usage\":{\"input_tokens\":7,\"output_tokens\":3,\"total_tokens\":10}}}\n\n"
				} else {
					sse = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg\",\"role\":\"assistant\",\"usage\":{\"input_tokens\":7}}}\n\n"
					ids := []string{""}
					if duplicate {
						ids = []string{"duplicate", "duplicate"}
					}
					for i, id := range ids {
						sse += fmt.Sprintf("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":%d,\"content_block\":{\"type\":\"tool_use\",\"id\":%q,\"name\":\"Write\",\"input\":{}}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", i, id, i)
					}
					sse += "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":3}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
				}
				if !duplicate {
					sse = strings.ReplaceAll(sse, `"call_id":"",`, "")
					sse = strings.ReplaceAll(sse, `"id":"",`, "")
				}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, sse)
				}))
				defer srv.Close()
				var p port.LLMProvider
				if provider == "responses" {
					p = openai.New(openai.WithAPIKey("fixture"), openai.WithBaseURL(srv.URL))
				} else {
					p = anthropic.New(anthropic.WithAPIKey("fixture"), anthropic.WithBaseURL(srv.URL))
				}
				policy, hooks, w := &invalidIdentityPolicy{}, &invalidIdentityHooks{}, &migratedApprovalWrite{}
				catalog := tool.NewCatalog()
				catalog.MustRegister(w)
				e := agent.NewEngine(agent.Deps{LLM: p, Catalog: catalog, Policy: policy, Hooks: hooks, Model: "fixture"})
				env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, memfs.NewWorkspace("/ws"), memledger.New(), nil)
				s := session.New("invalid-real", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
				failed := false
				for ev := range e.Run(t.Context(), s, env, agent.RunRequest{Text: "go"}).Events() {
					if ev.ToolCall != nil || ev.ToolResult != nil || ev.Ask != nil || (ev.Hook != nil && ev.Hook.CallID != "") {
						t.Fatal("invalid batch reached dispatch/publication")
					}
					if ev.Result != nil {
						failed = ev.Result.Stop == session.StopError && strings.Contains(ev.Result.Error, "invalid provider tool-call batch")
					}
				}
				if !failed || w.calls != 0 || policy.calls != 0 || hooks.calls != 0 {
					t.Fatalf("failure=%t executions=%d permissions=%d hooks=%d", failed, w.calls, policy.calls, hooks.calls)
				}
				usage := s.UsageFor(session.UsageKindMain)
				if usage.InputTokens != 7 || usage.OutputTokens != 3 {
					t.Fatalf("lost failure usage: %+v", usage)
				}
				for _, m := range s.Conversation.Messages {
					if len(m.ToolCalls) != 0 || m.ToolResult != nil || m.Replay != nil {
						t.Fatal("invalid batch retained calls/results/bindings")
					}
				}
			})
		}
	}
}
