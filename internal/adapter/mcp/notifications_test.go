package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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

func TestReconciliationCandidateListProjectionStaysFrozenAfterNotification(t *testing.T) {
	url, remote := newMutableTestServer(t)
	var invalidations atomic.Int64
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := ConnectComplete(ctx, ServerConfig{
		Name: "fake", URL: url,
		CandidateBudget: NewCandidateListBudget(32, 32, 1<<20),
		ListChanged:     func() { invalidations.Add(1) },
	}, &recordingDiag{})
	if err != nil {
		t.Fatalf("ConnectComplete: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	wantTools, wantResources, wantPrompts := len(s.Tools()), len(s.Resources()), len(s.Prompts())

	mcpsdk.AddTool(remote, &mcpsdk.Tool{Name: "late", Description: "late"},
		func(context.Context, *mcpsdk.CallToolRequest, noArgs) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{}, nil, nil
		})
	remote.AddResource(&mcpsdk.Resource{URI: "test://late", Name: "late"},
		func(_ context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
			return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{{URI: req.Params.URI, Text: "late"}}}, nil
		})
	remote.AddPrompt(&mcpsdk.Prompt{Name: "late"},
		func(context.Context, *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
			return &mcpsdk.GetPromptResult{}, nil
		})
	eventually(t, 3*time.Second, func() bool { return invalidations.Load() >= 3 }, "candidate notifications did not invalidate reconciler")
	if got := len(s.Tools()); got != wantTools {
		t.Fatalf("candidate Tools mutated from %d to %d", wantTools, got)
	}
	if got := len(s.Resources()); got != wantResources {
		t.Fatalf("candidate Resources mutated from %d to %d", wantResources, got)
	}
	if got := len(s.Prompts()); got != wantPrompts {
		t.Fatalf("candidate Prompts mutated from %d to %d", wantPrompts, got)
	}
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

// TestPromptListChangedNotificationRefreshesSnapshot mirrors the tools/resources
// cases for prompts: adding a prompt server-side fires
// notifications/prompts/list_changed and the next Prompts() picks it up.
func TestPromptListChangedNotificationRefreshesSnapshot(t *testing.T) {
	diag := &recordingDiag{}
	url, srv := newMutableTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := Connect(ctx, ServerConfig{Name: "fake", URL: url}, diag)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if got := len(s.Prompts()); got != 0 {
		t.Fatalf("baseline Prompts() = %d, want 0", got)
	}

	srv.AddPrompt(&mcpsdk.Prompt{Name: "late", Description: "added post-connect"},
		func(_ context.Context, _ *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
			return &mcpsdk.GetPromptResult{Messages: []*mcpsdk.PromptMessage{{Role: "user", Content: &mcpsdk.TextContent{Text: "late"}}}}, nil
		})

	eventually(t, 3*time.Second, func() bool {
		return diag.count("mcp server list changed") >= 1 && len(s.Prompts()) == 1
	}, "expected a prompts/list_changed notification to fire and Prompts() to refresh to 1")

	if len(s.Prompts()) != 1 || s.Prompts()[0].Name != "late" {
		t.Errorf("Prompts() = %+v, want [late]", s.Prompts())
	}
}

// TestNotificationStormLogsOncePerRefreshCycle verifies the WARN dedup: a
// notification storm (multiple list_changed events before a re-list) logs only
// ONE "mcp server list changed" line per refresh cycle (the false→true
// transition), NOT one per notification. This bounds the log rate to the read
// rate (caller-bounded), not the notification rate (server-bounded), so a
// malicious server cannot flood the diagnostics sink.
func TestNotificationStormLogsOncePerRefreshCycle(t *testing.T) {
	diag := &recordingDiag{}
	url, srv := newMutableTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := Connect(ctx, ServerConfig{Name: "fake", URL: url}, diag)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Fire multiple AddTool calls server-side, each firing a list_changed
	// notification. They all arrive before a Tools() re-list, so the dirty flag
	// is already true for the 2nd+ — the WARN must fire only once.
	for i := 0; i < 5; i++ {
		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: fmt.Sprintf("storm%d", i), Description: "storm"},
			func(_ context.Context, _ *mcpsdk.CallToolRequest, _ noArgs) (*mcpsdk.CallToolResult, any, error) {
				return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "storm"}}}, nil, nil
			})
	}

	// Wait for at least one notification to arrive + the re-list to pick up the tools.
	eventually(t, 3*time.Second, func() bool {
		return len(s.Tools()) == 6 // echo + 5 storm tools
	}, "expected the storm tools to appear in Tools()")

	// The WARN must be deduped: not 5× (one per AddTool notification). The
	// exact count is interleaving-dependent — the eventually() loop polls
	// Tools(), which clears the dirty flag, so on a loaded/-race runner a
	// notification arriving between two polls re-arms the false→true transition
	// and logs a 2nd WARN. Assert the dedup happened (count < 5, proving not
	// one-per-notification) without over-constraining the exact count.
	if got := diag.count("mcp server list changed"); got >= 5 {
		t.Errorf("notification-storm WARN count = %d, want < 5 (deduped to the false→true transition, not one per notification)", got)
	}
}
