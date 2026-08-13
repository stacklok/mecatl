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
	"github.com/stacklok/mecatl/engine/tool"
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
	res, err := spew.Execute(context.Background(), call, tool.Environment{})
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

// bigJSONObject returns a JSON object string over toolkit.MaxOutputBytes: an
// object with a single "items" array of N copies of a small object. It is
// guaranteed to parse as JSON and to start with '{'.
func bigJSONObject() string {
	// Each element is ~28 bytes; ~1100 of them exceeds the 25k cap.
	elem := `{"id":1,"name":"item","flag":true},`
	n := 1100
	var b strings.Builder
	b.WriteString(`{"items":[`)
	for i := 0; i < n; i++ {
		b.WriteString(elem)
	}
	b.WriteString(`{"end":true}]}`)
	return b.String()
}

// bigPlainText returns a >cap run of non-JSON plain text (does not start with
// '{' or '[', does not parse as JSON).
func bigPlainText() string {
	return strings.Repeat("the quick brown fox jumps over the lazy dog. ", 700)
}

// newOverCapStructuredServer stands up an in-process MCP server exposing a
// single tool that returns the given CallToolResult verbatim (using the
// lower-level Server.AddTool so raw Content/StructuredContent pass through
// untouched). outputSchema, when non-nil, is advertised on the tool.
func newOverCapStructuredServer(t *testing.T, name string, outputSchema any, result *mcpsdk.CallToolResult) string {
	t.Helper()
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "overcap", Version: "v1"}, nil)
	mcpTool := &mcpsdk.Tool{
		Name:         name,
		Description:  "returns an over-cap result",
		InputSchema:  map[string]any{"type": "object"},
		OutputSchema: outputSchema,
	}
	srv.AddTool(mcpTool, func(_ context.Context, _ *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return result, nil
	})
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return srv }, nil)
	httpSrv := httptest.NewServer(handler)
	t.Cleanup(httpSrv.Close)
	return httpSrv.URL
}

// overCapCall connects to the server and executes its single tool.
func overCapCall(t *testing.T, url, toolName string) session.ToolResult {
	t.Helper()
	s := connectTest(t, ServerConfig{Name: "overcap", URL: url})
	tl := toolsByName(s.Tools())["mcp__overcap__"+toolName]
	if tl == nil {
		t.Fatalf("tool %q not advertised", toolName)
	}
	call := session.NewToolCall("call-1", "mcp__overcap__"+toolName, json.RawMessage(`{}`))
	res, err := tl.Execute(context.Background(), call, tool.Environment{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return res
}

// TestStructuredResultOverCapFailsClosed asserts a structured (JSON) result over
// the output cap is NOT truncated — it is returned as a tool error pointing at
// the two escape hatches (narrow/paginate, or CallMcpWithQuery). Truncating
// JSON would leave an unparseable fragment the model can't reason over.
func TestStructuredResultOverCapFailsClosed(t *testing.T) {
	result := &mcpsdk.CallToolResult{
		StructuredContent: map[string]any{"items": []any{strings.Repeat("x", 30000)}},
	}
	url := newOverCapStructuredServer(t, "bigstruct", nil, result)

	res := overCapCall(t, url, "bigstruct")
	if !res.IsError {
		t.Fatalf("expected fail-closed error result, got non-error: %+v", res)
	}
	if !strings.Contains(res.Content, "exceeded the") {
		t.Errorf("Content missing 'exceeded the': %q", res.Content)
	}
	if !strings.Contains(res.Content, "CallMcpWithQuery") {
		t.Errorf("Content missing 'CallMcpWithQuery' escape hatch: %q", res.Content)
	}
	if !strings.Contains(res.Content, "narrow/paginate") {
		t.Errorf("Content missing 'narrow/paginate' escape hatch: %q", res.Content)
	}
	// The truncated JSON must NOT be present — the whole point of fail-closed.
	if strings.Contains(res.Content, toolkit.TruncationMarker) {
		t.Errorf("Content carried a truncation marker (should not have truncated JSON): %q", tail(res.Content, 60))
	}
}

// TestUnstructuredResultOverCapTruncates pins the existing behaviour: plain
// text over the cap is truncated with the marker (text tolerates truncation).
func TestUnstructuredResultOverCapTruncates(t *testing.T) {
	result := &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: bigPlainText()}},
	}
	url := newOverCapStructuredServer(t, "bigtext", nil, result)

	res := overCapCall(t, url, "bigtext")
	if res.IsError {
		t.Fatalf("plain text over cap must not be an error, got: %+v", res)
	}
	if !strings.Contains(res.Content, toolkit.TruncationMarker) {
		t.Errorf("Content missing truncation marker: suffix %q", tail(res.Content, 60))
	}
	if got := len(res.Content); got > toolkit.MaxOutputBytes+len(toolkit.TruncationMarker) {
		t.Errorf("Content length = %d, want <= cap+marker", got)
	}
}

// TestStructuredResultUnderCapPassesThrough asserts a small JSON result under
// the cap is returned normally — no false-positive fail-close.
func TestStructuredResultUnderCapPassesThrough(t *testing.T) {
	result := &mcpsdk.CallToolResult{
		StructuredContent: map[string]any{"answer": float64(42)},
	}
	url := newOverCapStructuredServer(t, "smallstruct", nil, result)

	res := overCapCall(t, url, "smallstruct")
	if res.IsError {
		t.Fatalf("small structured result must not be an error, got: %+v", res)
	}
	if !strings.Contains(res.Content, "answer") || !strings.Contains(res.Content, "42") {
		t.Errorf("Content missing the structured JSON: %q", res.Content)
	}
}

// TestJSONAsTextContentOverCapFailsClosed covers the operator's exact scenario:
// a tool returning a big JSON array as a bare TextContent (no outputSchema, no
// StructuredContent, no MIME). Signal 3 (text starts with '{'/'[' and parses)
// must catch it.
func TestJSONAsTextContentOverCapFailsClosed(t *testing.T) {
	result := &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: bigJSONObject()}},
	}
	url := newOverCapStructuredServer(t, "barejson", nil, result)

	res := overCapCall(t, url, "barejson")
	if !res.IsError {
		t.Fatalf("expected fail-closed error for bare-JSON-as-text over cap, got non-error: %+v", res)
	}
	if !strings.Contains(res.Content, "exceeded the") {
		t.Errorf("Content missing 'exceeded the': %q", res.Content)
	}
	if !strings.Contains(res.Content, "CallMcpWithQuery") {
		t.Errorf("Content missing 'CallMcpWithQuery' escape hatch: %q", res.Content)
	}
	if strings.Contains(res.Content, toolkit.TruncationMarker) {
		t.Errorf("Content carried a truncation marker (should not have truncated JSON): %q", tail(res.Content, 60))
	}
}

// TestOutputSchemaAloneFailCloses pins Signal 1 of isStructuredResult ALONE: a
// tool that ADVERTISES an outputSchema (so the model expects JSON) but returns
// OVER-CAP PLAIN TEXT (not JSON, no StructuredContent, no JSON MIME) still
// fail-closes. The outputSchema declaration is the hint: a tool that declares
// JSON output must not have its (mis)shapen large body truncated into an
// unparseable fragment. Every other test fires Signal 2 (StructuredContent) or
// Signal 3 (JSON-parseable text / JSON MIME); this is the one that fails
// closed purely because the schema was advertised.
func TestOutputSchemaAloneFailCloses(t *testing.T) {
	result := &mcpsdk.CallToolResult{
		// Plain text, NOT JSON (no leading '{'/'[', does not parse), over the cap.
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: bigPlainText()}},
	}
	url := newOverCapStructuredServer(t, "schemaonly", json.RawMessage(`{"type":"object"}`), result)

	res := overCapCall(t, url, "schemaonly")
	if !res.IsError {
		t.Fatalf("expected fail-closed error (outputSchema advertised + over-cap), got non-error: %+v", res)
	}
	if !strings.Contains(res.Content, "exceeded the") {
		t.Errorf("Content missing 'exceeded the': %q", res.Content)
	}
	if !strings.Contains(res.Content, "CallMcpWithQuery") {
		t.Errorf("Content missing 'CallMcpWithQuery' escape hatch: %q", res.Content)
	}
	if strings.Contains(res.Content, toolkit.TruncationMarker) {
		t.Errorf("Content carried a truncation marker (should not have truncated): %q", tail(res.Content, 60))
	}
}

// TestEmbeddedResourceJSONMIMEFailCloses pins Signal 3's JSON-MIME arm of
// isStructuredResult: a tool returning an EmbeddedResource whose
// Resource.MIMEType is "application/json" over the cap fail-closes. This arm
// (MIME-based, not content-parse-based) was previously unexercised — only the
// TextContent content-parse arm was covered (TestJSONAsTextContentOverCapFailsClosed).
func TestEmbeddedResourceJSONMIMEFailCloses(t *testing.T) {
	// Over-cap JSON text carried as an EmbeddedResource with application/json
	// MIME. The text is genuine JSON so the content-parse arm would ALSO fire,
	// but the MIME arm is reached first in isStructuredResult's EmbeddedResource
	// case — this exercises the JSON-MIME branch. Use a non-JSON-leading body to
	// PROVE the MIME is what trips it: a plain-text body under a JSON MIME must
	// still fail-close (the MIME is the declaration).
	result := &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.EmbeddedResource{
			Resource: &mcpsdk.ResourceContents{
				URI:      "test://big",
				MIMEType: "application/json",
				// Deliberately NOT JSON-shaped text: the MIME declares JSON, the
				// content does not parse as JSON — the MIME arm is the SOLE trigger.
				Text: bigPlainText(),
			},
		}},
	}
	url := newOverCapStructuredServer(t, "embjson", nil, result)

	res := overCapCall(t, url, "embjson")
	if !res.IsError {
		t.Fatalf("expected fail-closed error (EmbeddedResource application/json over cap), got non-error: %+v", res)
	}
	if !strings.Contains(res.Content, "exceeded the") {
		t.Errorf("Content missing 'exceeded the': %q", res.Content)
	}
	if !strings.Contains(res.Content, "CallMcpWithQuery") {
		t.Errorf("Content missing 'CallMcpWithQuery' escape hatch: %q", res.Content)
	}
	if strings.Contains(res.Content, toolkit.TruncationMarker) {
		t.Errorf("Content carried a truncation marker (should not have truncated): %q", tail(res.Content, 60))
	}
}

// TestErrorResultNotFailClosed pins that an error result (IsError: true) is NOT
// fail-closed, even when its payload is JSON-shaped and over the cap. Errors
// stay string-only and are truncated as today, so the model still reads the
// error text and self-corrects.
func TestErrorResultNotFailClosed(t *testing.T) {
	result := &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: bigJSONObject()}},
	}
	url := newOverCapStructuredServer(t, "errstruct", nil, result)

	res := overCapCall(t, url, "errstruct")
	if !res.IsError {
		t.Fatalf("error result must stay IsError, got: %+v", res)
	}
	// Errors are truncated, not fail-closed.
	if !strings.Contains(res.Content, toolkit.TruncationMarker) {
		t.Errorf("error result should be truncated with marker, got suffix %q", tail(res.Content, 60))
	}
	if strings.Contains(res.Content, "exceeded the") {
		t.Errorf("error result must NOT be fail-closed (no 'exceeded the'): %q", res.Content)
	}
}
