package mcpbroker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestCallMcpWithQueryBrokerSupport_UnenrolledTargetDoesNotInvoke(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	profile := protectedToolHiveProfile("private")
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{CallbackURL: "https://broker.example/callback", Profiles: []ToolHiveProfile{profile}})
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	attachment, _ := attach(t, process.Runtime, "unenrolled-query")
	if attachment.CallMcpWithQueryTool() != nil {
		t.Fatal("unenrolled target advertised query wrapper")
	}
	// A retained/forged wrapper still cannot turn a model target into a route.
	query := &attachmentQueryTool{attachment: attachment}
	result, err := query.Execute(t.Context(), session.NewToolCall("query", "CallMcpWithQuery", json.RawMessage(`{"server":"private","tool":"list","jq_filter":"."}`)), tool.Environment{})
	if err != nil || !result.IsError || !strings.Contains(result.Content, "route is unavailable") {
		t.Fatalf("unenrolled result = %+v, %v", result, err)
	}
}

func TestCallMcpWithQueryBrokerSupport_CancelledQueryDoesNotInvoke(t *testing.T) {
	catalogue, err := Compile(anonymousConfig(), discoveredTools(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	runtime, err := New(catalogue, func(context.Context, SessionRef, string, session.ToolCall) (session.ToolResult, error) {
		t.Fatal("raw caller invoked")
		return session.ToolResult{}, nil
	}, WithQueryCaller(func(context.Context, SessionRef, string, session.ToolCall, oauth2.TokenSource, string) (session.ToolResult, error) {
		calls.Add(1)
		return session.NewToolResult("query", "ok"), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	attachment, _ := attach(t, runtime, "cancelled-query")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := attachment.CallMcpWithQueryTool().Execute(ctx,
		session.NewToolCall("query", "CallMcpWithQuery", json.RawMessage(`{"server":"search","tool":"query","jq_filter":"."}`)), tool.Environment{})
	if !errors.Is(err, context.Canceled) || result.CallID != "" || result.Content != "" || result.IsError || len(result.Parts) != 0 || calls.Load() != 0 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls.Load())
	}
}

func TestCallMcpWithQueryBrokerSupport_BundledToolHiveProjection(t *testing.T) {
	var calls atomic.Int32
	upstream := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "query-fixture", Version: "1"}, nil)
	upstream.AddTool(&mcpsdk.Tool{Name: "list", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		calls.Add(1)
		return &mcpsdk.CallToolResult{StructuredContent: map[string]any{"keep": "structured", "raw_canary": strings.Repeat("secret", 6000)}, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: `{"keep":"wrong-text"}`}}}, nil
	})
	httpServer := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return upstream }, nil))
	defer httpServer.Close()
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{Profiles: []ToolHiveProfile{{Name: "search", URL: httpServer.URL, Auth: "none"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	attachment, _ := attach(t, process.Runtime, "bundled-query")
	for _, tc := range []struct {
		filter, want string
		fail         bool
	}{{".keep", `"structured"`, false}, {"error(.raw_canary)", "automatic replay refused", true}, {".", "automatic replay refused", true}} {
		before := calls.Load()
		args, _ := json.Marshal(map[string]any{"server": "search", "tool": "list", "jq_filter": tc.filter})
		result, err := attachment.CallMcpWithQueryTool().Execute(t.Context(), session.NewToolCall(session.ToolCallID(tc.filter), "CallMcpWithQuery", args), tool.Environment{})
		if err != nil || result.IsError != tc.fail || !strings.Contains(result.Content, tc.want) || strings.Contains(result.Content, "secret") || calls.Load() != before+1 {
			t.Fatalf("projection = %+v, %v, calls=%d", result, err, calls.Load())
		}
	}
}

func TestCallMcpWithQueryBrokerSupport_InvalidTargetSkipsAuthorization(t *testing.T) {
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	attachment, _ := attach(t, process.Runtime, "invalid-target")
	query := &attachmentQueryTool{attachment: attachment}
	call := session.NewToolCall("query", "CallMcpWithQuery", json.RawMessage(`{"server":"broker","tool":"mcp__broker__list","jq_filter":"."}`))

	_, required, err := query.RequestAuthorization(t.Context(), call)
	if err != nil || required {
		t.Fatalf("invalid target authorization = required %v, err %v; want no authorization and no error", required, err)
	}
	result, err := query.Execute(t.Context(), call, tool.Environment{})
	if err != nil || !result.IsError || !strings.Contains(result.Content, "bare remote tool name") {
		t.Fatalf("invalid target execution = %+v, %v", result, err)
	}
}
