package openai

import (
	"encoding/json"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// toolResultInputItems drives a one-tool-message request through the method
// buildParams (so the per-session caps thread in) and decodes the marshalled
// input item list into a generic slice for shape assertions.
func toolResultInputItems(t *testing.T, p *Provider, tr session.ToolResult) []map[string]any {
	t.Helper()
	params, err := p.buildParams(port.LLMRequest{
		Model:    "gpt-5.2",
		Messages: []session.Message{session.NewToolMessage(tr)},
	})
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	raw, err := json.Marshal(params.Input.OfInputItemList)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}
	return items
}

// TestToolResultStringOnlyUnchanged is the byte-identical-legacy guard: a tool
// result with NO Parts (the pre-T7 shape) produces the EXACT single-string
// function_call_output the adapter always produced — a string "output" field,
// never a content list — regardless of caps.
func TestToolResultStringOnlyUnchanged(t *testing.T) {
	p := New(WithAPIKey("sk-test"), WithProviderCapabilities(port.ProviderCapabilities{Image: true}))
	items := toolResultInputItems(t, p, session.NewToolResult("call_1", "package main"))
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1: %v", len(items), items)
	}
	item := items[0]
	if item["type"] != "function_call_output" {
		t.Fatalf("type = %v, want function_call_output", item["type"])
	}
	if item["call_id"] != "call_1" {
		t.Errorf("call_id = %v, want call_1", item["call_id"])
	}
	out, ok := item["output"].(string)
	if !ok {
		t.Fatalf("output = %T %v, want the legacy string form (not a content list)", item["output"], item["output"])
	}
	if out != "package main" {
		t.Errorf("output = %q, want \"package main\"", out)
	}
}

// TestToolResultWithPartsMapsToMultimodal: when a tool result carries typed
// Parts AND the per-session caps admit the image block, the OpenAI Responses
// function_call_output carries a MULTIMODAL content list (input_text +
// input_image), NOT the legacy single string.
func TestToolResultWithPartsMapsToMultimodal(t *testing.T) {
	imgData := []byte{0x89, 0x50, 0x4e, 0x47}
	tr := session.NewToolResultWithParts("call_1", "fallback", []session.Content{
		session.NewTextBlock("screenshot follows"),
		{BlockKind: session.BlockImage, Kind: session.MediaImage, MIMEType: "image/png", Data: imgData},
	})
	p := New(WithAPIKey("sk-test"), WithProviderCapabilities(port.ProviderCapabilities{Image: true}))
	items := toolResultInputItems(t, p, tr)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1: %v", len(items), items)
	}
	item := items[0]
	if item["type"] != "function_call_output" {
		t.Fatalf("type = %v, want function_call_output", item["type"])
	}
	if item["call_id"] != "call_1" {
		t.Errorf("call_id = %v, want call_1", item["call_id"])
	}
	content, ok := item["output"].([]any)
	if !ok {
		t.Fatalf("output = %T %v, want a multimodal content list", item["output"], item["output"])
	}
	if len(content) != 2 {
		t.Fatalf("content parts = %d, want 2 (input_text + input_image): %v", len(content), content)
	}
	text := content[0].(map[string]any)
	if text["type"] != "input_text" || text["text"] != "screenshot follows" {
		t.Errorf("part 0 = %v, want input_text \"screenshot follows\"", text)
	}
	img := content[1].(map[string]any)
	if img["type"] != "input_image" {
		t.Errorf("part 1 type = %v, want input_image", img["type"])
	}
	wantURL := dataURL("image/png", imgData)
	if img["image_url"] != wantURL {
		t.Errorf("image_url = %v, want %q", img["image_url"], wantURL)
	}
}

// TestToolResultWithPartsDropsImageWhenNoImageCap: a text-only capability
// intersection (Image:false) drops the image block from the Parts; only the
// text block survives, so the output is still a content list but with the image
// omitted — the per-session intersection, NOT the static adapter caps, gates it.
func TestToolResultWithPartsDropsImageWhenNoImageCap(t *testing.T) {
	imgData := []byte{0x89, 0x50, 0x4e, 0x47}
	tr := session.NewToolResultWithParts("call_1", "fallback", []session.Content{
		session.NewTextBlock("screenshot follows"),
		{BlockKind: session.BlockImage, Kind: session.MediaImage, MIMEType: "image/png", Data: imgData},
	})
	// Text-only intersection: a text-only model on this image-capable adapter.
	p := New(WithAPIKey("sk-test"), WithProviderCapabilities(port.ProviderCapabilities{}))
	items := toolResultInputItems(t, p, tr)
	item := items[0]
	content, ok := item["output"].([]any)
	if !ok {
		t.Fatalf("output = %T %v, want a content list (text block survived)", item["output"], item["output"])
	}
	if len(content) != 1 {
		t.Fatalf("content parts = %d, want 1 (image dropped by text-only caps): %v", len(content), content)
	}
	if text := content[0].(map[string]any); text["type"] != "input_text" {
		t.Errorf("surviving part = %v, want input_text", text)
	}
}

// TestToolResultWithPartsAllDroppedFallsBackToString: when EVERY block is
// filtered out by the capability intersection (e.g. an image-only result on a
// text-only model), RouteToolResultParts returns nil and the adapter falls back
// to the legacy single-string function_call_output (Content) — byte-identical
// to the no-Parts path.
func TestToolResultWithPartsAllDroppedFallsBackToString(t *testing.T) {
	imgData := []byte{0x89, 0x50, 0x4e, 0x47}
	tr := session.NewToolResultWithParts("call_1", "the image only", []session.Content{
		{BlockKind: session.BlockImage, Kind: session.MediaImage, MIMEType: "image/png", Data: imgData},
	})
	p := New(WithAPIKey("sk-test"), WithProviderCapabilities(port.ProviderCapabilities{})) // text-only
	items := toolResultInputItems(t, p, tr)
	item := items[0]
	out, ok := item["output"].(string)
	if !ok {
		t.Fatalf("output = %T %v, want the legacy string fallback (all blocks dropped)", item["output"], item["output"])
	}
	if out != "the image only" {
		t.Errorf("fallback output = %q, want the Content string", out)
	}
}
