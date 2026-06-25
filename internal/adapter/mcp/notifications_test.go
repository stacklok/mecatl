package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// newMutableTestServer stands up an in-process MCP server exposing the echo
// tool and returns the *mcpsdk.Server handle so a test can AddTool/AddResource
// post-connect (firing notifications/tools/list_changed etc.). Unlike
// newTestServer it does not pre-register resources/prompts — the test adds what
// it needs. The httptest listener close is self-registered via t.Cleanup so it
// runs AFTER the *Server.Close cleanup (LIFO), which is required now that the
// standalone SSE GET stream is enabled (ADR 0057): closing the listener while
// the SDK's handleSSE goroutine is still attached wedges httptest.Server.Close.
func newMutableTestServer(t *testing.T) (string, *mcpsdk.Server) {
	t.Helper()
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "fake", Version: "v1"}, nil)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "echo", Description: "echoes"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in echoArgs) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "echo:" + in.Text}}}, nil, nil
		})
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return srv }, nil)
	httpSrv := httptest.NewServer(handler)
	t.Cleanup(httpSrv.Close) // after *Server.Close (LIFO) — see newTestServer
	return httpSrv.URL, srv
}

// eventually polls cond until it returns true or the timeout elapses. It is the
// bounded-async assertion the notification tests use: the SDK fires
// notifications/* asynchronously over the standalone SSE stream, so a test must
// poll for the side effect (the dirty flag → the lazy re-list) rather than
// assert it synchronously.
func eventually(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("eventually: %s (timed out after %v)", msg, timeout)
}

// TestToolListChangedNotificationRefreshesSnapshot is the Phase-1 acceptance
// test (ADR 0057): with the standalone SSE stream enabled, adding a tool
// server-side fires notifications/tools/list_changed, the handler sets the
// toolsDirty flag, and the next Tools() lazily re-lists and picks up the new
// tool.
//
// Mutation-verify: if DisableStandaloneSSE is left true OR the
// ToolListChangedHandler is unwired, the notification never arrives and the
// eventually poll times out — the test fails rather than passes spuriously.
func TestToolListChangedNotificationRefreshesSnapshot(t *testing.T) {
	diag := &recordingDiag{}
	url, srv := newMutableTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := Connect(ctx, ServerConfig{Name: "fake", URL: url}, diag)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Baseline: one tool (echo).
	if got := len(s.Tools()); got != 1 {
		t.Fatalf("baseline Tools() = %d, want 1", got)
	}

	// Add a NEW tool server-side. The SDK server advertises listChanged:true
	// by default, so AddTool fires notifications/tools/list_changed over the
	// standalone SSE stream.
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "late", Description: "added post-connect"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ noArgs) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "late"}}}, nil, nil
		})

	// The notification is async; poll for the handler firing (the WARN line)
	// AND the lazy re-list picking up the new tool.
	eventually(t, 3*time.Second, func() bool {
		return diag.count("mcp server list changed") >= 1 && len(s.Tools()) == 2
	}, "expected a tools/list_changed notification to fire and Tools() to refresh to 2")

	// The new tool is namespaced and present.
	names := make(map[string]bool)
	for _, tl := range s.Tools() {
		names[tl.Spec().Name] = true
	}
	if !names["mcp__fake__late"] {
		t.Errorf("mcp__fake__late not in refreshed Tools(): %v", names)
	}
}

// TestResourceListChangedNotificationRefreshesSnapshot mirrors the tools case
// for resources: adding a resource server-side fires
// notifications/resources/list_changed and the next Resources() picks it up.
func TestResourceListChangedNotificationRefreshesSnapshot(t *testing.T) {
	diag := &recordingDiag{}
	url, srv := newMutableTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := Connect(ctx, ServerConfig{Name: "fake", URL: url}, diag)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if got := len(s.Resources()); got != 0 {
		t.Fatalf("baseline Resources() = %d, want 0", got)
	}

	srv.AddResource(&mcpsdk.Resource{URI: "test://late", Name: "late-doc", MIMEType: "text/plain"},
		func(_ context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
			return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{{URI: req.Params.URI, Text: "late"}}}, nil
		})

	eventually(t, 3*time.Second, func() bool {
		return diag.count("mcp server list changed") >= 1 && len(s.Resources()) == 1
	}, "expected a resources/list_changed notification to fire and Resources() to refresh to 1")

	if len(s.Resources()) != 1 || s.Resources()[0].URI != "test://late" {
		t.Errorf("Resources() = %+v, want [test://late]", s.Resources())
	}
}
