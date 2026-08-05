package openai

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// inputItems marshals the request's input item list and decodes it into a
// generic slice for shape assertions.
func inputItems(t *testing.T, p port.LLMRequest) []map[string]any {
	t.Helper()
	params, err := buildParams(p)
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

func TestBuildInputImagePartInlineDataURL(t *testing.T) {
	data := []byte{0x89, 0x50, 0x4e, 0x47}
	req := port.LLMRequest{
		Model: "gpt-5.2",
		Messages: []session.Message{
			session.NewUserMessageWithParts("what is this", []session.Content{
				{Kind: session.MediaImage, MIMEType: "image/png", Data: data},
			}),
		},
	}
	items := inputItems(t, req)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1: %v", len(items), items)
	}
	msg := items[0]
	if msg["role"] != "user" {
		t.Fatalf("role = %v, want user", msg["role"])
	}
	content, ok := msg["content"].([]any)
	if !ok {
		t.Fatalf("content is not a list: %T %v", msg["content"], msg["content"])
	}
	if len(content) != 2 {
		t.Fatalf("content parts = %d, want 2 (text + image): %v", len(content), content)
	}
	text := content[0].(map[string]any)
	if text["type"] != "input_text" || text["text"] != "what is this" {
		t.Fatalf("part 0 = %v, want input_text", text)
	}
	img := content[1].(map[string]any)
	if img["type"] != "input_image" {
		t.Fatalf("part 1 type = %v, want input_image", img["type"])
	}
	wantURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	if img["image_url"] != wantURL {
		t.Fatalf("image_url = %v, want %q", img["image_url"], wantURL)
	}
}

func TestBuildInputImagePartPassthroughURL(t *testing.T) {
	req := port.LLMRequest{
		Model: "m",
		Messages: []session.Message{
			session.NewUserMessageWithParts("", []session.Content{
				{Kind: session.MediaImage, MIMEType: "image/jpeg", URL: "https://example.com/x.jpg"},
			}),
		},
	}
	items := inputItems(t, req)
	content := items[0]["content"].([]any)
	// No text part because Text == "".
	if len(content) != 1 {
		t.Fatalf("content parts = %d, want 1 (image only): %v", len(content), content)
	}
	img := content[0].(map[string]any)
	if img["type"] != "input_image" || img["image_url"] != "https://example.com/x.jpg" {
		t.Fatalf("image part = %v, want passthrough url", img)
	}
}

func TestBuildInputAudioPartHonestError(t *testing.T) {
	req := port.LLMRequest{
		Model: "m",
		Messages: []session.Message{
			session.NewUserMessageWithParts("listen", []session.Content{
				{Kind: session.MediaAudio, MIMEType: "audio/wav", Data: []byte{1, 2}},
			}),
		},
	}
	if _, err := buildParams(req); err == nil {
		t.Fatal("expected an honest error for an audio part, got nil")
	} else if !strings.Contains(err.Error(), "audio input not supported") {
		t.Fatalf("error = %v, want audio-not-supported", err)
	}
}

// TestBuildParamsBytestablePrefixWithMedia is the byte-stable-prefix regression
// guard: adding an image part to a user message must NOT change params.Instructions
// or params.Tools — media lives ONLY in the volatile Input items, never in the
// cache-stable prefix.
func TestBuildParamsBytestablePrefixWithMedia(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)
	base := port.LLMRequest{
		System: prompt.Layered{StablePrefix: "You are a coding agent.", VolatileSuffix: "<env>linux</env>"},
		Model:  "gpt-5.2",
		Tools:  []tool.ToolSpec{{Name: "read_file", Description: "Read a file.", Schema: schema}},
	}
	textReq := base
	textReq.Messages = []session.Message{session.NewUserMessage("describe the diagram")}

	mediaReq := base
	mediaReq.Messages = []session.Message{
		session.NewUserMessageWithParts("describe the diagram", []session.Content{
			{Kind: session.MediaImage, MIMEType: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}},
		}),
	}

	textParams, err := buildParams(textReq)
	if err != nil {
		t.Fatalf("buildParams(text): %v", err)
	}
	mediaParams, err := buildParams(mediaReq)
	if err != nil {
		t.Fatalf("buildParams(media): %v", err)
	}

	if textParams.Instructions.Value != mediaParams.Instructions.Value {
		t.Fatalf("Instructions differ with media:\n text=%q\nmedia=%q",
			textParams.Instructions.Value, mediaParams.Instructions.Value)
	}

	textTools, err := json.Marshal(textParams.Tools)
	if err != nil {
		t.Fatalf("marshal text tools: %v", err)
	}
	mediaTools, err := json.Marshal(mediaParams.Tools)
	if err != nil {
		t.Fatalf("marshal media tools: %v", err)
	}
	if string(textTools) != string(mediaTools) {
		t.Fatalf("Tools differ with media:\n text=%s\nmedia=%s", textTools, mediaTools)
	}
}

func TestProviderCapabilities(t *testing.T) {
	caps := (&Provider{}).Capabilities()
	if !caps.Image {
		t.Error("Image capability should be true")
	}
	if caps.Audio {
		t.Error("Audio capability should be false (Responses input has no audio member)")
	}
	if !caps.EmbeddedContext {
		t.Error("EmbeddedContext should be true")
	}
}
