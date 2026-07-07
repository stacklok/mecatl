package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// newCallToolTestServer stands up an in-process MCP server exposing the echo /
// peek / boom tools from newTestServer PLUS a tool that returns a
// StructuredContent, so CallTool's raw result path can be exercised without the
// truncation remoteTool.Execute applies. It returns the server URL.
func newCallToolTestServer(t *testing.T) string {
	t.Helper()
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "fake", Version: "v1"}, nil)

	// echo + boom are reused from newTestServer's set, but we re-register them on
	// this server so a CallTool test does not also pay for the resource/prompt
	// fixtures the shared helper wires up.
	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "echo",
		Description: "echoes the input text",
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest, in echoArgs) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "echo:" + in.Text}},
		}, nil, nil
	})

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "boom",
		Description: "always fails",
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest, _ noArgs) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{
			IsError: true,
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "kaboom"}},
		}, nil, nil
	})

	// struct tool: returns a StructuredContent. Uses the lower-level Server.AddTool
	// so StructuredContent passes through verbatim (mcpsdk.AddTool with a non-any
	// Out would auto-marshal; we want to assert CallTool carries the raw value).
	srv.AddTool(&mcpsdk.Tool{
		Name:         "struct",
		Description:  "returns structured content",
		InputSchema:  map[string]any{"type": "object"},
		OutputSchema: map[string]any{"type": "object", "properties": map[string]any{"n": map[string]any{"type": "number"}}},
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{
			StructuredContent: map[string]any{"n": float64(7), "ok": true},
			Content:           []mcpsdk.Content{&mcpsdk.TextContent{Text: `{"n":7,"ok":true}`}},
		}, nil
	})

	// arraystruct tool: returns a JSON ARRAY as StructuredContent, round-tripped
	// through CallTool to assert arrays (not just objects) survive the wire.
	srv.AddTool(&mcpsdk.Tool{
		Name:        "arraystruct",
		Description: "returns array-typed structured content",
		InputSchema: map[string]any{"type": "object"},
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{
			StructuredContent: []any{
				map[string]any{"id": float64(1), "name": "a"},
				map[string]any{"id": float64(2), "name": "b"},
			},
		}, nil
	})

	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return srv }, nil)
	httpSrv := httptest.NewServer(handler)
	// See newTestServer: close the listener AFTER *Server.Close (LIFO) so the
	// standalone SSE GET stream unwinds first.
	t.Cleanup(httpSrv.Close)
	return httpSrv.URL
}

// managerFromURL connects a Manager to a single server at url, registering the
// close on cleanup. It is the Manager-level analogue of connectTest.
func managerFromURL(t *testing.T, url string) *Manager {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m, err := NewManager(ctx, []ServerConfig{{Name: "fake", URL: url}}, nil, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

// TestManagerCallTool asserts CallTool returns the raw echoed text content
// (untruncated), with IsError false, routing through the Provider interface.
func TestManagerCallTool(t *testing.T) {
	url := newCallToolTestServer(t)
	m := managerFromURL(t, url)

	res, err := m.CallTool(context.Background(), "fake", "echo", json.RawMessage(`{"text":"hello"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected IsError for echo: %+v", res)
	}
	if res.Server != "fake" {
		t.Errorf("Server = %q, want fake", res.Server)
	}
	if res.Tool != "echo" {
		t.Errorf("Tool = %q, want echo", res.Tool)
	}
	if len(res.Content) != 1 {
		t.Fatalf("Content blocks = %d, want 1", len(res.Content))
	}
	if got, want := res.Content[0].Text, "echo:hello"; got != want {
		t.Errorf("Content[0].Text = %q, want %q", got, want)
	}
}

// TestManagerCallToolStructuredContent asserts StructuredContent is carried as a
// non-nil json.RawMessage that round-trips through a re-parse.
func TestManagerCallToolStructuredContent(t *testing.T) {
	url := newCallToolTestServer(t)
	m := managerFromURL(t, url)

	res, err := m.CallTool(context.Background(), "fake", "struct", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected IsError for struct: %+v", res)
	}
	if res.StructuredContent == nil {
		t.Fatalf("StructuredContent is nil; want the raw structured payload")
	}
	// Round-trips: re-parse the raw message and assert the expected fields.
	var got map[string]any
	if err := json.Unmarshal(res.StructuredContent, &got); err != nil {
		t.Fatalf("StructuredContent not valid JSON: %v (%s)", err, res.StructuredContent)
	}
	if got["n"] != float64(7) {
		t.Errorf("StructuredContent.n = %v, want 7", got["n"])
	}
	if got["ok"] != true {
		t.Errorf("StructuredContent.ok = %v, want true", got["ok"])
	}
}

// TestManagerCallToolStructuredContentArray asserts a JSON ARRAY returned as
// StructuredContent round-trips through CallTool and re-parses as []any (NOT a
// map), mirroring TestManagerCallToolStructuredContent over an array-typed
// structured content.
func TestManagerCallToolStructuredContentArray(t *testing.T) {
	url := newCallToolTestServer(t)
	m := managerFromURL(t, url)

	res, err := m.CallTool(context.Background(), "fake", "arraystruct", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected IsError for arraystruct: %+v", res)
	}
	if res.StructuredContent == nil {
		t.Fatalf("StructuredContent is nil; want the raw array payload")
	}
	// Round-trips: re-parse the raw message and assert it is an array, not a map.
	var got []any
	if err := json.Unmarshal(res.StructuredContent, &got); err != nil {
		t.Fatalf("StructuredContent not valid JSON array: %v (%s)", err, res.StructuredContent)
	}
	if len(got) != 2 {
		t.Fatalf("StructuredContent = %d elements, want 2", len(got))
	}
	first, ok := got[0].(map[string]any)
	if !ok {
		t.Fatalf("StructuredContent[0] = %T, want map[string]any", got[0])
	}
	if first["id"] != float64(1) || first["name"] != "a" {
		t.Errorf("StructuredContent[0] = %v, want id=1 name=a", first)
	}
}

// TestManagerCallToolError asserts a remote tool error (IsError: true) is
// surfaced as IsError on the CallResult, not as a Go error — the transport
// succeeded, the tool reported a tool-level failure.
func TestManagerCallToolError(t *testing.T) {
	url := newCallToolTestServer(t)
	m := managerFromURL(t, url)

	res, err := m.CallTool(context.Background(), "fake", "boom", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("CallTool returned Go error for a tool-level error; want IsError on the result: %v", err)
	}
	if !res.IsError {
		t.Errorf("IsError = false, want true")
	}
	if len(res.Content) != 1 || res.Content[0].Text != "kaboom" {
		t.Errorf("Content = %+v, want [kaboom]", res.Content)
	}
}

// TestManagerCallToolUnknownServer asserts an unknown server name wraps
// ErrUnknownServer (a client error), distinct from a transport fault.
func TestManagerCallToolUnknownServer(t *testing.T) {
	url := newCallToolTestServer(t)
	m := managerFromURL(t, url)

	_, err := m.CallTool(context.Background(), "nope", "echo", nil)
	if !errors.Is(err, ErrUnknownServer) {
		t.Errorf("CallTool unknown server: err = %v, want errors.Is(_, ErrUnknownServer)", err)
	}
}
