package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func callPDFTool(t *testing.T, url, serverName, toolName string, enabled bool) session.ToolResult {
	t.Helper()
	s := connectTest(t, ServerConfig{Name: serverName, URL: url, PDFArtifactResults: enabled})
	tl := toolsByName(s.Tools())["mcp__"+serverName+"__"+toolName]
	if tl == nil {
		t.Fatalf("tool %q not advertised", toolName)
	}
	call := session.NewToolCall("call-1", tl.Spec().Name, json.RawMessage(`{}`))
	result, err := tl.Execute(context.Background(), call, tool.Environment{})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSDKPDFArtifacts_Scenario2_MCPAndReplay(t *testing.T) {
	pdf := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("x"), 11<<20)...)
	pdf = append(pdf, []byte("\n%%EOF")...)
	result := &mcpsdk.CallToolResult{Content: []mcpsdk.Content{
		&mcpsdk.TextContent{Text: "before"},
		&mcpsdk.EmbeddedResource{Resource: &mcpsdk.ResourceContents{URI: "file:///hostile.pdf", MIMEType: "application/pdf", Blob: pdf}},
		&mcpsdk.TextContent{Text: "after"},
	}}
	url := newContentServer(t, "largepdf", nil, result)
	got := callPDFTool(t, url, "pdfcontent", "largepdf", true)
	if got.IsError || len(got.Parts) != 3 {
		t.Fatalf("MCP PDF size gate lost eligible blob: isError=%v parts=%d content=%q", got.IsError, len(got.Parts), got.Content)
	}
	if got.Parts[1].BlockKind != session.BlockEmbeddedResource || !bytes.Equal(got.Parts[1].Data, pdf) {
		t.Fatalf("MCP PDF block kind=%q size=%d, want embedded %d bytes", got.Parts[1].BlockKind, len(got.Parts[1].Data), len(pdf))
	}
	atLimit := make([]byte, session.MaxPDFBytes)
	copy(atLimit, []byte("%PDF-1.7\n"))
	copy(atLimit[len(atLimit)-len("\n%%EOF"):], []byte("\n%%EOF"))
	limitURL := newContentServer(t, "limitpdf", nil, &mcpsdk.CallToolResult{Content: []mcpsdk.Content{
		&mcpsdk.EmbeddedResource{Resource: &mcpsdk.ResourceContents{MIMEType: "application/pdf", Blob: atLimit}},
	}})
	atLimitResult := callPDFTool(t, limitURL, "pdfcontentlimit", "limitpdf", true)
	if atLimitResult.IsError || len(atLimitResult.Parts) != 1 || len(atLimitResult.Parts[0].Data) != session.MaxPDFBytes {
		t.Fatalf("20 MiB PDF rejected: isError=%v parts=%d", atLimitResult.IsError, len(atLimitResult.Parts))
	}
	block, err := session.NewPDFArtifactBlock("pdf-id", "artifact.pdf", int64(len(pdf)), strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	for _, replay := range []session.ToolResult{
		session.NewToolResultWithParts("call-1", "old summary", []session.Content{session.NewTextBlock("before"), block, session.NewTextBlock("after")}),
		session.NewToolResultWithParts("call-1", "", []session.Content{block}),
	} {
		parts := port.RouteToolResultParts(replay, port.ProviderCapabilities{})
		found := false
		for _, part := range parts {
			found = found || strings.Contains(session.ToolBlockText(part), "PDF artifact:")
		}
		if !found {
			t.Fatalf("replay lost PDF summary: %+v", parts)
		}
		for _, part := range parts {
			if len(part.Data) != 0 {
				t.Fatal("replay includes PDF bytes")
			}
		}
	}
	oversized := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("x"), session.MaxPDFBytes)...)
	oversized = append(oversized, []byte("\n%%EOF")...)
	oversizedURL := newContentServer(t, "oversizedpdf", nil, &mcpsdk.CallToolResult{Content: []mcpsdk.Content{
		&mcpsdk.EmbeddedResource{Resource: &mcpsdk.ResourceContents{URI: "", MIMEType: "application/pdf", Blob: oversized}},
	}})
	tooLarge := callPDFTool(t, oversizedURL, "pdfcontent2", "oversizedpdf", true)
	if !tooLarge.IsError || tooLarge.CallID != "call-1" || len(tooLarge.Parts) != 0 {
		t.Fatalf("oversized PDF = %+v, want bounded tool error", tooLarge)
	}
	legacy := callPDFTool(t, url, "legacy", "largepdf", false)
	if legacy.IsError || len(legacy.Parts) != 2 {
		t.Fatalf("storage-disabled legacy cap = %+v, want oversized PDF block clamped", legacy)
	}
}
