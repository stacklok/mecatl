package anthropic

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// toolResultMessages drives a one-tool-message request through buildParams
// (the method form, so the per-session caps thread in) and decodes the
// marshalled messages into a generic slice for shape assertions.
func toolResultMessages(t *testing.T, p *Provider, tr session.ToolResult) []map[string]any {
	t.Helper()
	params, err := p.buildParams(port.LLMRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []session.Message{session.NewToolMessage(tr)},
	})
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	raw, err := json.Marshal(params.Messages)
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}
	var msgs []map[string]any
	if err := json.Unmarshal(raw, &msgs); err != nil {
		t.Fatalf("unmarshal messages: %v", err)
	}
	return msgs
}

// TestToolResultStringOnlyUnchanged is the byte-identical-legacy guard: a tool
// result with NO Parts produces the EXACT single-string tool_result the adapter
// always produced — a content list with ONE text block carrying Content —
// regardless of caps, and is_error stays false (the pre-T7 hard-coded value).
func TestToolResultStringOnlyUnchanged(t *testing.T) {
	p := New(WithAPIKey("sk-test"), WithMaxTokens(16000),
		WithProviderCapabilities(port.ProviderCapabilities{Image: true}))
	msgs := toolResultMessages(t, p, session.NewToolResult("toolu_1", "42"))
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1: %v", len(msgs), msgs)
	}
	m := msgs[0]
	if m["role"] != "user" {
		t.Errorf("role = %v, want user", m["role"])
	}
	content, ok := m["content"].([]any)
	if !ok {
		t.Fatalf("content = %T %v, want a list", m["content"], m["content"])
	}
	if len(content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(content))
	}
	blk := content[0].(map[string]any)
	if blk["type"] != "tool_result" {
		t.Errorf("block type = %v, want tool_result", blk["type"])
	}
	if blk["tool_use_id"] != "toolu_1" {
		t.Errorf("tool_use_id = %v, want toolu_1", blk["tool_use_id"])
	}
	if v, hasErr := blk["is_error"]; hasErr && v != false {
		t.Errorf("is_error = %v, want false (the legacy hard-coded value)", v)
	}
	inner, ok := blk["content"].([]any)
	if !ok {
		t.Fatalf("tool_result content = %T %v, want a one-text-block list", blk["content"], blk["content"])
	}
	if len(inner) != 1 {
		t.Fatalf("tool_result content blocks = %d, want 1", len(inner))
	}
	txt := inner[0].(map[string]any)
	if txt["type"] != "text" || txt["text"] != "42" {
		t.Errorf("inner block = %v, want text \"42\"", txt)
	}
}

// TestToolResultWithPartsMapsToMultimodal: when a tool result carries typed
// Parts AND the per-session caps admit the image block, the Anthropic
// tool_result carries a content-block LIST with a text block AND a base64 image
// block — NOT the legacy single-string form.
func TestToolResultWithPartsMapsToMultimodal(t *testing.T) {
	imgData := []byte{0x89, 0x50, 0x4e, 0x47}
	tr := session.NewToolResultWithParts("toolu_1", "fallback", []session.Content{
		session.NewTextBlock("screenshot follows"),
		{BlockKind: session.BlockImage, Kind: session.MediaImage, MIMEType: "image/png", Data: imgData},
	})
	p := New(WithAPIKey("sk-test"), WithMaxTokens(16000),
		WithProviderCapabilities(port.ProviderCapabilities{Image: true}))
	msgs := toolResultMessages(t, p, tr)
	m := msgs[0]
	content := m["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content blocks = %d, want 1 tool_result", len(content))
	}
	blk := content[0].(map[string]any)
	if blk["type"] != "tool_result" {
		t.Fatalf("block type = %v, want tool_result", blk["type"])
	}
	inner := blk["content"].([]any)
	if len(inner) != 2 {
		t.Fatalf("tool_result content blocks = %d, want 2 (text + image): %v", len(inner), inner)
	}
	txt := inner[0].(map[string]any)
	if txt["type"] != "text" || txt["text"] != "screenshot follows" {
		t.Errorf("block 0 = %v, want text \"screenshot follows\"", txt)
	}
	img := inner[1].(map[string]any)
	if img["type"] != "image" {
		t.Errorf("block 1 type = %v, want image", img["type"])
	}
	src, ok := img["source"].(map[string]any)
	if !ok {
		t.Fatalf("image source = %T %v, want a base64 source object", img["source"], img["source"])
	}
	if src["type"] != "base64" {
		t.Errorf("source type = %v, want base64", src["type"])
	}
	if src["media_type"] != "image/png" {
		t.Errorf("source media_type = %v, want image/png", src["media_type"])
	}
	if src["data"] != encodeBase64(imgData) {
		t.Errorf("source data = %v, want base64 of the inline bytes", src["data"])
	}
}

// TestToolResultWithPartsDropsImageWhenNoImageCap: a text-only capability
// intersection drops the image block; only the text block survives — the
// per-session intersection (not the static adapter caps) gates it.
func TestToolResultWithPartsDropsImageWhenNoImageCap(t *testing.T) {
	imgData := []byte{0x89, 0x50, 0x4e, 0x47}
	tr := session.NewToolResultWithParts("toolu_1", "fallback", []session.Content{
		session.NewTextBlock("screenshot follows"),
		{BlockKind: session.BlockImage, Kind: session.MediaImage, MIMEType: "image/png", Data: imgData},
	})
	p := New(WithAPIKey("sk-test"), WithMaxTokens(16000),
		WithProviderCapabilities(port.ProviderCapabilities{})) // text-only
	msgs := toolResultMessages(t, p, tr)
	blk := msgs[0]["content"].([]any)[0].(map[string]any)
	inner := blk["content"].([]any)
	if len(inner) != 1 {
		t.Fatalf("tool_result content blocks = %d, want 1 (image dropped by text-only caps): %v", len(inner), inner)
	}
	if txt := inner[0].(map[string]any); txt["type"] != "text" {
		t.Errorf("surviving block = %v, want text", txt)
	}
}

// TestToolResultWithPartsAllDroppedFallsBackToString: when EVERY block is
// filtered out (image-only result on a text-only model), the adapter falls back
// to the legacy single-string tool_result(Content) — byte-identical to no-Parts.
func TestToolResultWithPartsAllDroppedFallsBackToString(t *testing.T) {
	imgData := []byte{0x89, 0x50, 0x4e, 0x47}
	tr := session.NewToolResultWithParts("toolu_1", "the image only", []session.Content{
		{BlockKind: session.BlockImage, Kind: session.MediaImage, MIMEType: "image/png", Data: imgData},
	})
	p := New(WithAPIKey("sk-test"), WithMaxTokens(16000),
		WithProviderCapabilities(port.ProviderCapabilities{})) // text-only
	msgs := toolResultMessages(t, p, tr)
	blk := msgs[0]["content"].([]any)[0].(map[string]any)
	inner := blk["content"].([]any)
	if len(inner) != 1 {
		t.Fatalf("tool_result content blocks = %d, want 1 (legacy fallback text)", len(inner))
	}
	txt := inner[0].(map[string]any)
	if txt["type"] != "text" || txt["text"] != "the image only" {
		t.Errorf("fallback = %v, want text \"the image only\"", txt)
	}
}

// messagesJSON drives an arbitrary message list through buildParams and returns
// both the decoded messages and the raw marshaled bytes (for empty-text scans).
func messagesJSON(t *testing.T, p *Provider, msgs []session.Message) ([]map[string]any, []byte) {
	t.Helper()
	params, err := p.buildParams(port.LLMRequest{Model: "claude-sonnet-4-6", Messages: msgs})
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	raw, err := json.Marshal(params.Messages)
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal messages: %v", err)
	}
	return decoded, raw
}

// firstContentBlock extracts msg["content"][0] as a JSON object, failing (not
// panicking) with a shape-naming message if the decoded message drifts from the
// expected {content: [ {...} ]} shape.
func firstContentBlock(t *testing.T, msg map[string]any) map[string]any {
	t.Helper()
	list, ok := msg["content"].([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("message content = %T %v, want a non-empty block list", msg["content"], msg["content"])
	}
	blk, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("content[0] = %T %v, want a JSON object", list[0], list[0])
	}
	return blk
}

// TestToolResultEmptyContentPoisonHealsToPlaceholder mirrors the openai regression
// for the Anthropic Messages API: an MCP fetch past the end of a document returned
// a tool result with Content:"" and a single empty-text Part. Anthropic rejects an
// empty text content block ("text content blocks must be non-empty"), and
// stateless full-replay makes the rejection permanent. The empty block is dropped
// upstream, the adapter falls back to the single-string path, and the empty
// Content becomes the deterministic placeholder — no "text":"" on the wire.
func TestToolResultEmptyContentPoisonHealsToPlaceholder(t *testing.T) {
	tr := session.NewToolResultWithParts("toolu_1", "", []session.Content{
		session.NewTextBlock(""),
	})
	p := New(WithAPIKey("sk-test"), WithMaxTokens(16000),
		WithProviderCapabilities(port.ProviderCapabilities{Image: true}))
	msgs, raw := messagesJSON(t, p, []session.Message{session.NewToolMessage(tr)})
	blk := firstContentBlock(t, msgs[0])
	if blk["type"] != "tool_result" {
		t.Fatalf("block type = %v, want tool_result", blk["type"])
	}
	// The legacy single-string tool_result serializes content as a one-text-block
	// list carrying the placeholder.
	inner, ok := blk["content"].([]any)
	if !ok || len(inner) != 1 {
		t.Fatalf("tool_result content = %T %v, want a one-text-block list", blk["content"], blk["content"])
	}
	txt, ok := inner[0].(map[string]any)
	if !ok {
		t.Fatalf("tool_result inner[0] = %T %v, want a JSON object", inner[0], inner[0])
	}
	if txt["type"] != "text" || txt["text"] != emptyToolOutputPlaceholder {
		t.Errorf("tool_result inner block = %v, want text %q", txt, emptyToolOutputPlaceholder)
	}
	if bytes.Contains(raw, []byte(`"text":""`)) {
		t.Fatalf("marshaled messages contain an empty text field (Anthropic 400 poison): %s", raw)
	}
}

// TestDegenerateEmptySystemTurnGetsPlaceholder: an in-history system message with
// empty Text (folded to a user turn by buildMessages) serializes to a NON-EMPTY
// text block — the fourth empty-text emitter, the same permanent-brick poison
// class as the user/assistant turns.
func TestDegenerateEmptySystemTurnGetsPlaceholder(t *testing.T) {
	p := New(WithAPIKey("sk-test"), WithMaxTokens(16000),
		WithProviderCapabilities(port.ProviderCapabilities{}))
	msgs, raw := messagesJSON(t, p, []session.Message{{Role: session.RoleSystem, Text: ""}})
	blk := firstContentBlock(t, msgs[0])
	if blk["type"] != "text" || blk["text"] != emptyMessagePlaceholder {
		t.Errorf("empty system turn block = %v, want text %q", blk, emptyMessagePlaceholder)
	}
	if bytes.Contains(raw, []byte(`"text":""`)) {
		t.Fatalf("empty system turn put an empty text block on the wire: %s", raw)
	}
}

// TestDegenerateEmptyUserTurnGetsPlaceholder: an empty user turn (no text, no
// parts) serializes to a NON-EMPTY text block, not an empty one Anthropic would
// 400.
func TestDegenerateEmptyUserTurnGetsPlaceholder(t *testing.T) {
	p := New(WithAPIKey("sk-test"), WithMaxTokens(16000),
		WithProviderCapabilities(port.ProviderCapabilities{}))
	msgs, raw := messagesJSON(t, p, []session.Message{session.NewUserMessage("")})
	blk := firstContentBlock(t, msgs[0])
	if blk["type"] != "text" || blk["text"] != emptyMessagePlaceholder {
		t.Errorf("empty user turn block = %v, want text %q", blk, emptyMessagePlaceholder)
	}
	if bytes.Contains(raw, []byte(`"text":""`)) {
		t.Fatalf("empty user turn put an empty text block on the wire: %s", raw)
	}
}

// TestDegenerateEmptyAssistantTurnGetsPlaceholder: an empty assistant turn (no
// text, no tool calls, no reasoning — reachable via the no-progress-nudge path)
// serializes to a NON-EMPTY text block.
func TestDegenerateEmptyAssistantTurnGetsPlaceholder(t *testing.T) {
	p := New(WithAPIKey("sk-test"), WithMaxTokens(16000),
		WithProviderCapabilities(port.ProviderCapabilities{}))
	msgs, raw := messagesJSON(t, p, []session.Message{session.NewAssistantMessage("", "", nil)})
	blk := firstContentBlock(t, msgs[0])
	if blk["type"] != "text" || blk["text"] != emptyMessagePlaceholder {
		t.Errorf("empty assistant turn block = %v, want text %q", blk, emptyMessagePlaceholder)
	}
	if bytes.Contains(raw, []byte(`"text":""`)) {
		t.Fatalf("empty assistant turn put an empty text block on the wire: %s", raw)
	}
}
