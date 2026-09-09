package mcpbroker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/adapter/localauthority"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestADR_0234_CallMcpWithQueryBrokerSupport_AuthorityUnchanged(t *testing.T) {
	for _, capability := range []string{"mcp__search__query", "mcp__calendar__create"} {
		t.Run(capability, func(t *testing.T) {
			catalogue, err := Compile(anonymousConfig(), discoveredTools(), nil)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			runtime, err := New(catalogue, func(context.Context, SessionRef, string, session.ToolCall) (session.ToolResult, error) {
				t.Fatal("raw caller invoked")
				return session.ToolResult{}, nil
			}, WithQueryCaller(func(_ context.Context, _ SessionRef, _ string, call session.ToolCall, _ oauth2.TokenSource, _ string) (session.ToolResult, error) {
				calls++
				return session.NewToolResult(call.ID, "42"), nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			attachment, _ := attach(t, runtime, "authority-query")
			cat := tool.NewCatalog()
			cat.MustRegister(attachment.CallMcpWithQueryTool())
			for _, candidate := range attachment.Tools() {
				cat.MustRegister(candidate)
			}
			call := session.NewToolCall("query", "CallMcpWithQuery", json.RawMessage(`{"server":"search","tool":"query","jq_filter":"."}`))
			eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(call), mockllm.TextTurn("done")), Catalog: cat, Policy: queryAllowPolicy{}, AuthorityEvaluator: localauthority.New()})
			placement, err := (queryPlacement{}).Bind(t.Context(), server.PlacementBindRequest{})
			if err != nil {
				t.Fatal(err)
			}
			sess := session.New("authority-query", session.ModeDefault, placement.Ref, session.Limits{}, time.Now())
			if err := sess.BindAuthority(session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{capability}}, Provenance: "test"}); err != nil {
				t.Fatal(err)
			}
			result := ""
			for ev := range eng.Run(t.Context(), sess, placement.Environment, agent.RunRequest{Text: "query", CanPresentAuthorization: true}).Events() {
				if ev.ToolResult != nil && ev.ToolResult.CallID == call.ID {
					result = ev.ToolResult.Content
				}
			}
			if capability == "mcp__search__query" {
				if calls != 1 || result != "42" {
					t.Fatalf("allowed target: calls=%d result=%q", calls, result)
				}
			} else if calls != 0 || !strings.Contains(result, "mcp__search__query") {
				t.Fatalf("denied target: calls=%d result=%q", calls, result)
			}
		})
	}
}
