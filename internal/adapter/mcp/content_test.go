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
)

// newContentServer stands up an in-process MCP server exposing a single tool
// whose handler returns the given CallToolResult verbatim, and advertises the
// given (optional) OutputSchema. It uses the lower-level Server.AddTool (NOT the
// generic mcpsdk.AddTool) so the handler's raw Content / StructuredContent pass
// through untouched — the generic path would re-wrap StructuredContent and
// re-validate it server-side, defeating the adapter-mapping test.
func newContentServer(t *testing.T, name string, outputSchema any, result *mcpsdk.CallToolResult) string {
	t.Helper()
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "content", Version: "v1"}, nil)

	tool := &mcpsdk.Tool{
		Name:        name,
		Description: "returns typed content for the mapper test",
		// An object input schema is required by Server.AddTool.
		InputSchema:  map[string]any{"type": "object"},
		OutputSchema: outputSchema,
	}
	srv.AddTool(tool, func(_ context.Context, _ *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return result, nil
	})

	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return srv }, nil)
	httpSrv := httptest.NewServer(handler)
	t.Cleanup(httpSrv.Close)
	return httpSrv.URL
}

// callTool runs the single tool exposed by a content test server and returns
// its mapped ToolResult.
func callTool(t *testing.T, url, serverName, toolName string) session.ToolResult {
	t.Helper()
	s := connectTest(t, ServerConfig{Name: serverName, URL: url})
	tl := toolsByName(s.Tools())["mcp__"+serverName+"__"+toolName]
	if tl == nil {
		t.Fatalf("tool %q not advertised", toolName)
	}
	call := session.NewToolCall("call-1", "mcp__"+serverName+"__"+toolName, json.RawMessage(`{}`))
	res, err := tl.Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return res
}

// findBlock returns the first block with the given BlockKind, or fails the test.
func findBlock(t *testing.T, parts []session.Content, kind session.BlockKind) session.Content {
	t.Helper()
	for _, p := range parts {
		if p.BlockKind == kind {
			return p
		}
	}
	t.Fatalf("no %q block in parts %+v", kind, parts)
	return session.Content{}
}

// TestMapContentResourceLink asserts a *mcpsdk.ResourceLink with full metadata
// + Annotations.Audience maps to a BlockResourceLink carrying URI/Name/
// Description/Audience, and that the model-facing Content includes the URI and
// name (not a bare placeholder).
func TestMapContentResourceLink(t *testing.T) {
	size := int64(2048)
	result := &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{
			&mcpsdk.ResourceLink{
				URI:         "https://example.test/doc",
				Name:        "doc",
				Title:       "The Doc",
				Description: "a linked resource",
				MIMEType:    "text/html",
				Size:        &size,
				Annotations: &mcpsdk.Annotations{Audience: []mcpsdk.Role{"user"}},
			},
		},
	}
	url := newContentServer(t, "link", nil, result)

	res := callTool(t, url, "content", "link")
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}

	blk := findBlock(t, res.Parts, session.BlockResourceLink)
	if blk.URL != "https://example.test/doc" {
		t.Errorf("block URI = %q, want https://example.test/doc", blk.URL)
	}
	if blk.Name != "doc" {
		t.Errorf("block Name = %q, want doc", blk.Name)
	}
	if blk.Description != "a linked resource" {
		t.Errorf("block Description = %q", blk.Description)
	}
	if len(blk.Audience) != 1 || blk.Audience[0] != "user" {
		t.Errorf("block Audience = %v, want [user]", blk.Audience)
	}
	if blk.Size != size {
		t.Errorf("block Size = %d, want %d", blk.Size, size)
	}

	// The model-facing string must carry the URI + name, not a bare placeholder.
	if !strings.Contains(res.Content, "https://example.test/doc") {
		t.Errorf("Content missing URI: %q", res.Content)
	}
	if !strings.Contains(res.Content, "doc") {
		t.Errorf("Content missing name: %q", res.Content)
	}
	if strings.Contains(res.Content, "[resource link: ]") {
		t.Errorf("Content has a bare placeholder: %q", res.Content)
	}
}

// TestMapContentEmbeddedResource covers the text variant (text verbatim in both
// Parts and Content) and the blob variant (summarized in Content, raw bytes in
// Parts).
func TestMapContentEmbeddedResource(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		result := &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{
				&mcpsdk.EmbeddedResource{
					Resource: &mcpsdk.ResourceContents{
						URI:      "test://text",
						MIMEType: "text/plain",
						Text:     "hello embedded",
					},
				},
			},
		}
		url := newContentServer(t, "embtext", nil, result)

		res := callTool(t, url, "content", "embtext")
		if res.IsError {
			t.Fatalf("unexpected error result: %+v", res)
		}
		blk := findBlock(t, res.Parts, session.BlockEmbeddedResource)
		if blk.Text != "hello embedded" {
			t.Errorf("block Text = %q, want hello embedded", blk.Text)
		}
		if len(blk.Data) != 0 {
			t.Errorf("text variant should have no blob, got %d bytes", len(blk.Data))
		}
		if !strings.Contains(res.Content, "hello embedded") {
			t.Errorf("Content missing verbatim text: %q", res.Content)
		}
	})

	t.Run("blob", func(t *testing.T) {
		blob := []byte{0x89, 0x50, 0x4e, 0x47, 0x0a}
		result := &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{
				&mcpsdk.EmbeddedResource{
					Resource: &mcpsdk.ResourceContents{
						URI:      "test://binary",
						MIMEType: "image/png",
						Blob:     blob,
					},
				},
			},
		}
		url := newContentServer(t, "embblob", nil, result)

		res := callTool(t, url, "content", "embblob")
		if res.IsError {
			t.Fatalf("unexpected error result: %+v", res)
		}
		blk := findBlock(t, res.Parts, session.BlockEmbeddedResource)
		if string(blk.Data) != string(blob) {
			t.Errorf("block Data = %v, want %v", blk.Data, blob)
		}
		if blk.Text != "" {
			t.Errorf("blob variant should have no text, got %q", blk.Text)
		}
		// Content summarizes the blob — never base64-dumps it.
		if !strings.Contains(res.Content, "[binary resource: image/png, 5 bytes]") {
			t.Errorf("Content missing blob summary: %q", res.Content)
		}
		if strings.Contains(res.Content, "iVBOR") {
			t.Errorf("Content must not base64-dump the blob: %q", res.Content)
		}
	})
}

// TestMapContentStructuredContentMirror asserts a tool returning
// StructuredContent (a JSON object) + a TextContent produces a
// BlockStructuredContent block AND that the JSON appears in Content (the spec's
// SHOULD: serialize structured content as a TextContent block).
func TestMapContentStructuredContentMirror(t *testing.T) {
	result := &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{
			&mcpsdk.TextContent{Text: "prelude"},
		},
		StructuredContent: map[string]any{"answer": float64(42)},
	}
	url := newContentServer(t, "struct", nil, result)

	res := callTool(t, url, "content", "struct")
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}

	blk := findBlock(t, res.Parts, session.BlockStructuredContent)
	var got map[string]any
	if err := json.Unmarshal([]byte(blk.Text), &got); err != nil {
		t.Fatalf("structured block Text is not valid JSON: %v (%q)", err, blk.Text)
	}
	if got["answer"] != float64(42) {
		t.Errorf("structured block = %v, want answer=42", got)
	}

	// The JSON must also appear in the model-facing Content (the mirror).
	if !strings.Contains(res.Content, `"answer"`) || !strings.Contains(res.Content, "42") {
		t.Errorf("Content missing the structured JSON mirror: %q", res.Content)
	}
	// The text content still rides through too.
	if !strings.Contains(res.Content, "prelude") {
		t.Errorf("Content missing the TextContent: %q", res.Content)
	}
}

// TestMapContentImageContent asserts an image block is constructed via
// NewImageContent and the model-facing Content carries the [image content: <mime>]
// note rather than dumping the bytes.
func TestMapContentImageContent(t *testing.T) {
	// A 1x1 transparent PNG.
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	result := &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{
			&mcpsdk.ImageContent{MIMEType: "image/png", Data: png},
		},
	}
	url := newContentServer(t, "img", nil, result)

	res := callTool(t, url, "content", "img")
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}

	blk := findBlock(t, res.Parts, session.BlockImage)
	if blk.MIMEType != "image/png" {
		t.Errorf("block MIMEType = %q, want image/png", blk.MIMEType)
	}
	if string(blk.Data) != string(png) {
		t.Errorf("block Data = %v, want %v", blk.Data, png)
	}
	if !strings.Contains(res.Content, "[image content: image/png]") {
		t.Errorf("Content missing image note: %q", res.Content)
	}
}

// TestMapContentImageContentAllowlist asserts the image-MIME allowlist
// (Finding #9 / F-MIME): a core web image format still produces a typed
// BlockImage (unchanged behavior, incl. a parameterized MIME), while a
// valid-but-unsupported image MIME degrades to a text-only note instead of a
// block — so a hostile/misbehaving MCP server can never push an
// Anthropic/OpenAI-incompatible image MIME through to the provider.
func TestMapContentImageContentAllowlist(t *testing.T) {
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

	t.Run("allowlisted gif", func(t *testing.T) {
		result := &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{
				&mcpsdk.ImageContent{MIMEType: "image/gif", Data: png},
			},
		}
		url := newContentServer(t, "imgok", nil, result)

		res := callTool(t, url, "content", "imgok")
		if res.IsError {
			t.Fatalf("unexpected error result: %+v", res)
		}
		blk := findBlock(t, res.Parts, session.BlockImage)
		if blk.MIMEType != "image/gif" {
			t.Errorf("block MIMEType = %q, want image/gif", blk.MIMEType)
		}
		if !strings.Contains(res.Content, "[image content: image/gif]") {
			t.Errorf("Content missing image note: %q", res.Content)
		}
	})

	t.Run("allowlisted with MIME parameter", func(t *testing.T) {
		result := &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{
				&mcpsdk.ImageContent{MIMEType: "image/jpeg; foo=bar", Data: png},
			},
		}
		url := newContentServer(t, "imgparam", nil, result)

		res := callTool(t, url, "content", "imgparam")
		if res.IsError {
			t.Fatalf("unexpected error result: %+v", res)
		}
		blk := findBlock(t, res.Parts, session.BlockImage)
		if blk.MIMEType != "image/jpeg; foo=bar" {
			t.Errorf("block MIMEType = %q, want image/jpeg; foo=bar (verbatim)", blk.MIMEType)
		}
	})

	for _, mime := range []string{"image/svg+xml", "image/tiff"} {
		t.Run("unsupported "+mime, func(t *testing.T) {
			result := &mcpsdk.CallToolResult{
				Content: []mcpsdk.Content{
					&mcpsdk.ImageContent{MIMEType: mime, Data: png},
				},
			}
			url := newContentServer(t, "imgbad_"+strings.NewReplacer("/", "_", "+", "_").Replace(mime), nil, result)

			res := callTool(t, url, "content", "imgbad_"+strings.NewReplacer("/", "_", "+", "_").Replace(mime))
			if res.IsError {
				t.Fatalf("unexpected error result: %+v", res)
			}
			for _, p := range res.Parts {
				if p.BlockKind == session.BlockImage {
					t.Fatalf("unsupported MIME %q must not produce a BlockImage, got %+v", mime, p)
				}
			}
			if !strings.Contains(res.Content, "unsupported image type, not sent") {
				t.Errorf("Content missing unsupported-image note for %q: %q", mime, res.Content)
			}
			if !strings.Contains(res.Content, mime) {
				t.Errorf("Content missing the MIME %q: %q", mime, res.Content)
			}
		})
	}
}

// TestMapContentAudioContent asserts an audio block is constructed via
// NewAudioContent and the model-facing Content carries the
// [audio content: <mime>] note rather than dumping the bytes. Unlike images,
// mapContent has no MIME allowlist gate for audio — any audio/* MIME that
// session.NewAudioContent accepts reaches the constructor directly.
func TestMapContentAudioContent(t *testing.T) {
	wav := []byte{0x52, 0x49, 0x46, 0x46, 0x24, 0x00, 0x00, 0x00}
	result := &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{
			&mcpsdk.AudioContent{MIMEType: "audio/wav", Data: wav},
		},
	}
	url := newContentServer(t, "audio", nil, result)

	res := callTool(t, url, "content", "audio")
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}

	blk := findBlock(t, res.Parts, session.BlockAudio)
	if blk.MIMEType != "audio/wav" {
		t.Errorf("block MIMEType = %q, want audio/wav", blk.MIMEType)
	}
	if string(blk.Data) != string(wav) {
		t.Errorf("block Data = %v, want %v", blk.Data, wav)
	}
	if !strings.Contains(res.Content, "[audio content: audio/wav]") {
		t.Errorf("Content missing audio note: %q", res.Content)
	}
}

// TestMapContentInvalidImage exercises the
// "[image content: %s (invalid: %v)]" degrade branch. An allowlisted image
// MIME clears isAllowlistedImageMIME, so the call reaches
// session.NewImageContent — which then fails its exactly-one-of-data/url
// invariant because mcpsdk.ImageContent carries no URL field, so an empty
// Data can never satisfy it. The block is omitted; the model-facing note
// surfaces the construction error instead of silently dropping the part.
func TestMapContentInvalidImage(t *testing.T) {
	result := &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{
			&mcpsdk.ImageContent{MIMEType: "image/png", Data: nil},
		},
	}
	url := newContentServer(t, "imgempty", nil, result)

	res := callTool(t, url, "content", "imgempty")
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	for _, p := range res.Parts {
		if p.BlockKind == session.BlockImage {
			t.Fatalf("empty-data image must not produce a BlockImage, got %+v", p)
		}
	}
	if !strings.Contains(res.Content, "[image content: image/png (invalid:") {
		t.Errorf("Content missing invalid-image note: %q", res.Content)
	}
}

// TestMapContentInvalidAudio is the audio analogue of TestMapContentInvalidImage:
// a syntactically valid audio/* MIME with empty Data fails
// session.NewAudioContent's exactly-one-of-data/url invariant (no URL field on
// mcpsdk.AudioContent either), surfacing the
// "[audio content: %s (invalid: %v)]" note.
func TestMapContentInvalidAudio(t *testing.T) {
	result := &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{
			&mcpsdk.AudioContent{MIMEType: "audio/wav", Data: nil},
		},
	}
	url := newContentServer(t, "audioempty", nil, result)

	res := callTool(t, url, "content", "audioempty")
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	for _, p := range res.Parts {
		if p.BlockKind == session.BlockAudio {
			t.Fatalf("empty-data audio must not produce a BlockAudio, got %+v", p)
		}
	}
	if !strings.Contains(res.Content, "[audio content: audio/wav (invalid:") {
		t.Errorf("Content missing invalid-audio note: %q", res.Content)
	}
}

// TestMapContentEmbeddedResourceMissing covers the c.Resource == nil branch:
// an EmbeddedResource with no Resource at all produces no block and the
// "[embedded resource: missing resource]" note.
func TestMapContentEmbeddedResourceMissing(t *testing.T) {
	result := &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{
			&mcpsdk.EmbeddedResource{Resource: nil},
		},
	}
	url := newContentServer(t, "embmissing", nil, result)

	res := callTool(t, url, "content", "embmissing")
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	if len(res.Parts) != 0 {
		t.Errorf("nil-resource EmbeddedResource must not produce a block, got %+v", res.Parts)
	}
	if !strings.Contains(res.Content, "[embedded resource: missing resource]") {
		t.Errorf("Content missing missing-resource note: %q", res.Content)
	}
}

// TestMapContentEmbeddedResourceEmpty covers the neither-Text-nor-Blob branch:
// a Resource that is present but carries neither Text nor Blob produces no
// block and the "[embedded resource: empty resource]" note.
func TestMapContentEmbeddedResourceEmpty(t *testing.T) {
	result := &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{
			&mcpsdk.EmbeddedResource{Resource: &mcpsdk.ResourceContents{
				URI:      "test://empty",
				MIMEType: "text/plain",
			}},
		},
	}
	url := newContentServer(t, "embempty", nil, result)

	res := callTool(t, url, "content", "embempty")
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	if len(res.Parts) != 0 {
		t.Errorf("empty resource EmbeddedResource must not produce a block, got %+v", res.Parts)
	}
	if !strings.Contains(res.Content, "[embedded resource: empty resource]") {
		t.Errorf("Content missing empty-resource note: %q", res.Content)
	}
}

// TestMapContentUnsupportedKind exercises mapContent's default branch
// ("[unsupported content]"). mcpsdk.Content carries an unexported marker
// method (fromWire), so it cannot be implemented by a test stub outside the
// SDK package — there is no way to author a wholly foreign Content
// implementation. Instead this uses a real SDK type mapContent does not
// special-case: *mcpsdk.ToolUseContent. ToolUseContent/ToolResultContent are
// documented as valid only in sampling message contexts, but
// CallToolResult.Content is decoded with an UNRESTRICTED allow-map
// (mcp/protocol.go's contentsFromWire(wire.Content, nil)), so a
// misbehaving/legacy server can still smuggle one through as a tool-call
// result, and mapContent must degrade rather than mis-render it.
func TestMapContentUnsupportedKind(t *testing.T) {
	result := &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{
			&mcpsdk.ToolUseContent{ID: "tu-1", Name: "some-tool"},
		},
	}
	url := newContentServer(t, "unsupported", nil, result)

	res := callTool(t, url, "content", "unsupported")
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	if len(res.Parts) != 0 {
		t.Errorf("unsupported content kind must not produce a block, got %+v", res.Parts)
	}
	if !strings.Contains(res.Content, "[unsupported content]") {
		t.Errorf("Content missing unsupported-content note: %q", res.Content)
	}
}

// TestMapContentStructuredContentSchemaValidation asserts the OPTIONAL
// defense-in-depth validator fires against a misbehaving server: a tool that
// advertises an output schema but returns structured content that violates it
// surfaces a model-facing NOTE, never a silent drop and never a hard error. The
// structured content still rides through as a block.
func TestMapContentStructuredContentSchemaValidation(t *testing.T) {
	// Schema requires answer to be a string; the server returns a number.
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"answer": map[string]any{"type": "string"},
		},
		"required": []string{"answer"},
	}
	result := &mcpsdk.CallToolResult{
		StructuredContent: map[string]any{"answer": float64(42)},
	}
	url := newContentServer(t, "badschema", schema, result)

	res := callTool(t, url, "content", "badschema")
	if res.IsError {
		t.Fatalf("schema validation must not turn into a tool error: %+v", res)
	}
	// The block still rides through.
	blk := findBlock(t, res.Parts, session.BlockStructuredContent)
	if !strings.Contains(blk.Text, "answer") {
		t.Errorf("structured block missing answer: %q", blk.Text)
	}
	// The model-facing note must surface.
	if !strings.Contains(res.Content, "structured output validation warning") {
		t.Errorf("Content missing validation warning note: %q", res.Content)
	}
}
