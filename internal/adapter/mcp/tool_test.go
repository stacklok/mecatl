package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/toolkit"
)

// newBigOutputServer stands up an in-process MCP server exposing a single tool
// that returns a TextContent body larger than toolkit.MaxOutputBytes, so the
// truncation cap in remoteTool.Execute is exercised.
func newBigOutputServer(t *testing.T) string {
	t.Helper()
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "big", Version: "v1"}, nil)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "spew",
		Description: "returns a body over the output cap",
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest, _ noArgs) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: strings.Repeat("x", 30000)}},
		}, nil, nil
	})

	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return srv }, nil)
	httpSrv := httptest.NewServer(handler)
	t.Cleanup(httpSrv.Close)
	return httpSrv.URL
}

// TestMCPRemoteResultTruncatedToCap asserts an MCP tool result over the shared
// output cap is truncated to the cap (plus room for the marker) and carries the
// truncation marker. This closes issue #178: MCP results are now bounded exactly
// like every built-in tool.
func TestMCPRemoteResultTruncatedToCap(t *testing.T) {
	url := newBigOutputServer(t)
	s := connectTest(t, ServerConfig{Name: "big", URL: url})

	spew := toolsByName(s.Tools())["mcp__big__spew"]
	if spew == nil {
		t.Fatalf("spew tool not advertised")
	}

	call := session.NewToolCall("call-1", "mcp__big__spew", json.RawMessage(`{}`))
	res, err := spew.Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}

	if got := len(res.Content); got > toolkit.MaxOutputBytes+50 {
		t.Errorf("Content length = %d, want <= %d (cap+marker slack)", got, toolkit.MaxOutputBytes+50)
	}
	if !strings.Contains(res.Content, toolkit.TruncationMarker) {
		t.Errorf("Content missing truncation marker %q; got suffix %q", toolkit.TruncationMarker, tail(res.Content, len(toolkit.TruncationMarker)+10))
	}
}

// tail returns the last n bytes of s, or s itself if shorter.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
