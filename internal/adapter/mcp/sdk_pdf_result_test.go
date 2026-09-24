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
		&mcpsdk.TextContent{Text: "before limit PDF"},
		&mcpsdk.EmbeddedResource{Resource: &mcpsdk.ResourceContents{MIMEType: "application/pdf", Blob: atLimit}},
		&mcpsdk.TextContent{Text: "after limit PDF"},
	}})
	atLimitResult := callPDFTool(t, limitURL, "pdfcontentlimit", "limitpdf", true)
	if atLimitResult.IsError || len(atLimitResult.Parts) != 3 || atLimitResult.Parts[0].Text != "before limit PDF" ||
		atLimitResult.Parts[1].BlockKind != session.BlockEmbeddedResource || !bytes.Equal(atLimitResult.Parts[1].Data, atLimit) ||
		atLimitResult.Parts[2].Text != "after limit PDF" {
		t.Fatalf("mixed 20 MiB PDF rejected or clamped: isError=%v parts=%d content=%q", atLimitResult.IsError, len(atLimitResult.Parts), atLimitResult.Content)
	}
	legacyAtLimit := callPDFTool(t, limitURL, "pdfcontentlimitdisabled", "limitpdf", false)
	if legacyAtLimit.IsError || len(legacyAtLimit.Parts) != 2 || legacyAtLimit.Parts[0].Text != "before limit PDF" ||
		legacyAtLimit.Parts[1].Text != "after limit PDF" || !strings.Contains(legacyAtLimit.Content, "exceeds the inline byte cap") {
		t.Fatalf("storage-disabled mixed result did not retain legacy clamp: isError=%v parts=%d content=%q", legacyAtLimit.IsError, len(legacyAtLimit.Parts), legacyAtLimit.Content)
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
		&mcpsdk.TextContent{Text: "before oversized PDF"},
		&mcpsdk.EmbeddedResource{Resource: &mcpsdk.ResourceContents{URI: "", MIMEType: "application/pdf", Blob: oversized}},
		&mcpsdk.ResourceLink{URI: "https://example.test/survivor", Name: "survivor"},
		&mcpsdk.TextContent{Text: "after oversized PDF"},
	}})
	tooLarge := callPDFTool(t, oversizedURL, "pdfcontent2", "oversizedpdf", true)
	if !tooLarge.IsError || tooLarge.CallID != "call-1" || len(tooLarge.Parts) != 0 || !strings.Contains(tooLarge.Content, "20 MiB") || len(tooLarge.Content) > 256 {
		t.Fatalf("enabled oversized PDF: isError=%v callID=%q parts=%d content=%q, want bounded tool error", tooLarge.IsError, tooLarge.CallID, len(tooLarge.Parts), tooLarge.Content)
	}
	legacyOversized := callPDFTool(t, oversizedURL, "pdfcontent3", "oversizedpdf", false)
	if legacyOversized.IsError || legacyOversized.CallID != "call-1" || len(legacyOversized.Parts) != 3 {
		t.Fatalf("disabled oversized PDF: isError=%v callID=%q parts=%d content=%q, want legacy clamp with mixed blocks", legacyOversized.IsError, legacyOversized.CallID, len(legacyOversized.Parts), legacyOversized.Content)
	}
	if legacyOversized.Parts[0].BlockKind != session.BlockText || legacyOversized.Parts[0].Text != "before oversized PDF" ||
		legacyOversized.Parts[1].BlockKind != session.BlockResourceLink || legacyOversized.Parts[1].URL != "https://example.test/survivor" ||
		legacyOversized.Parts[2].BlockKind != session.BlockText || legacyOversized.Parts[2].Text != "after oversized PDF" {
		t.Fatalf("disabled oversized PDF lost surviving mixed blocks: %+v", legacyOversized.Parts)
	}
	if !strings.Contains(legacyOversized.Content, "before oversized PDF") || !strings.Contains(legacyOversized.Content, "after oversized PDF") ||
		!strings.Contains(legacyOversized.Content, "exceeds the inline byte cap") {
		t.Fatalf("disabled oversized PDF lost text or clamp note: %q", legacyOversized.Content)
	}
	legacy := callPDFTool(t, url, "legacy", "largepdf", false)
	if legacy.IsError || len(legacy.Parts) != 2 {
		t.Fatalf("storage-disabled legacy cap = %+v, want oversized PDF block clamped", legacy)
	}
}
