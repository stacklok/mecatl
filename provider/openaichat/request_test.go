package openaichat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// marshalParams builds params and marshals them to the wire JSON so assertions
// run against the exact bytes the SDK would send.
func marshalParams(t *testing.T, req port.LLMRequest, effort string) map[string]any {
	t.Helper()
	params, err := buildParams(req, effort)
	if err != nil {
		t.Fatalf("buildParams error = %v", err)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	return out
}

// TestBuildParamsFullConversation covers the full role mapping (system prompt,
// user text, assistant-with-tool_call, tool result), tools, and stream_options.
func TestBuildParamsFullConversation(t *testing.T) {
	req := port.LLMRequest{
		System: prompt.Layered{StablePrefix: "You are a coding agent.", VolatileSuffix: "<env>x</env>"},
		Model:  "glm-5.2",
		Messages: []session.Message{
			{Role: session.RoleUser, Text: "weather in Paris?"},
			{Role: session.RoleAssistant, ToolCalls: []session.ToolCall{
				{ID: "call_1", Name: "get_weather", Args: json.RawMessage(`{"city":"Paris"}`)},
			}},
			{Role: session.RoleTool, ToolResult: ptr(session.NewToolResult("call_1", "sunny, 21C"))},
		},
		Tools: []tool.ToolSpec{
			{Name: "get_weather", Description: "Get weather", Schema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)},
		},
	}
	got := marshalParams(t, req, "")

	if got["model"] != "glm-5.2" {
		t.Errorf("model = %v, want glm-5.2", got["model"])
	}
	// stream_options.include_usage present.
	so, _ := got["stream_options"].(map[string]any)
	if so == nil || so["include_usage"] != true {
		t.Errorf("stream_options.include_usage not set: %v", got["stream_options"])
	}
	// reasoning_effort must be ABSENT when effort is "".
	if _, ok := got["reasoning_effort"]; ok {
		t.Errorf("reasoning_effort present with empty effort: %v", got["reasoning_effort"])
	}
	// Messages: system, user, assistant(tool_calls), tool.
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages len = %d, want 4", len(msgs))
	}
	roles := make([]string, len(msgs))
	for i, m := range msgs {
		mm, _ := m.(map[string]any)
		roles[i], _ = mm["role"].(string)
	}
	if strings.Join(roles, ",") != "system,user,assistant,tool" {
		t.Errorf("roles = %v, want system,user,assistant,tool", roles)
	}
	// Assistant message carries the tool call.
	asst, _ := msgs[2].(map[string]any)
	tcs, _ := asst["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Fatalf("assistant tool_calls = %d, want 1", len(tcs))
	}
	// Tool result message carries tool_call_id.
	toolMsg, _ := msgs[3].(map[string]any)
	if toolMsg["tool_call_id"] != "call_1" {
		t.Errorf("tool_call_id = %v, want call_1", toolMsg["tool_call_id"])
	}
	// Tools present with the function name.
	tools, _ := got["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(tools))
	}
}

// TestBuildParamsReasoningEffort proves xhigh is passed VERBATIM (no clamp to
// high, unlike the openai Responses adapter) and an unknown token is omitted.
func TestBuildParamsReasoningEffort(t *testing.T) {
	req := port.LLMRequest{Model: "glm-5.2", Messages: []session.Message{{Role: session.RoleUser, Text: "hi"}}}

	if got := marshalParams(t, req, "xhigh"); got["reasoning_effort"] != "xhigh" {
		t.Errorf("reasoning_effort = %v, want xhigh (no clamp)", got["reasoning_effort"])
	}
	if got := marshalParams(t, req, "max"); got["reasoning_effort"] != "max" {
		t.Errorf("reasoning_effort = %v, want max", got["reasoning_effort"])
	}
	if got := marshalParams(t, req, "banana"); got["reasoning_effort"] != nil {
		t.Errorf("reasoning_effort = %v, want omitted for unknown token", got["reasoning_effort"])
	}
	// none/adaptive are endpoint-accepted but NOT in mecatl's neutral vocabulary,
	// so they never reach the adapter through composition; if one slips through it
	// is omitted (fail-soft), not passed as an unrecognised token.
	if got := marshalParams(t, req, "none"); got["reasoning_effort"] != nil {
		t.Errorf("reasoning_effort = %v, want omitted (none is not a neutral token)", got["reasoning_effort"])
	}
}

// TestBuildParamsMultimodal covers an image user part (data URL) and the audio
// hard error.
func TestBuildParamsMultimodal(t *testing.T) {
	img := port.LLMRequest{Model: "m", Messages: []session.Message{{
		Role:  session.RoleUser,
		Text:  "what is this?",
		Parts: []session.Content{{Kind: session.MediaImage, MIMEType: "image/png", Data: []byte{0x1, 0x2}}},
	}}}
	got := marshalParams(t, img, "")
	msgs, _ := got["messages"].([]any)
	um, _ := msgs[0].(map[string]any)
	content, ok := um["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("user content parts = %v, want text+image list", um["content"])
	}

	audio := port.LLMRequest{Model: "m", Messages: []session.Message{{
		Role:  session.RoleUser,
		Parts: []session.Content{{Kind: session.MediaAudio, MIMEType: "audio/wav", Data: []byte{0x1}}},
	}}}
	if _, err := buildParams(audio, ""); err == nil {
		t.Error("buildParams with audio part: expected error, got nil")
	}
}

func ptr[T any](v T) *T { return &v }

// TestBuildParamsToolResultParts proves typed tool-result Parts are projected into
// the tool message (F2) — text/resource/structured blocks flatten to text via
// session.ToolBlockText rather than falling back to the plain Content string.
func TestBuildParamsToolResultParts(t *testing.T) {
	tr := session.NewToolResultWithParts("call_1", "PLAIN-FALLBACK", []session.Content{
		{BlockKind: session.BlockText, Text: "block one"},
		{BlockKind: session.BlockResourceLink, Title: "doc", URL: "https://x/y"},
	})
	req := port.LLMRequest{Model: "m", Messages: []session.Message{
		{Role: session.RoleTool, ToolResult: &tr},
	}}
	got := marshalParams(t, req, "")
	msgs, _ := got["messages"].([]any)
	tm, _ := msgs[0].(map[string]any)
	content, _ := tm["content"].(string)
	if !strings.Contains(content, "block one") || !strings.Contains(content, "doc (https://x/y)") {
		t.Errorf("tool content = %q, want the flattened block text", content)
	}
	if strings.Contains(content, "PLAIN-FALLBACK") {
		t.Errorf("tool content used the plain Content fallback despite Parts: %q", content)
	}
	if tm["tool_call_id"] != "call_1" {
		t.Errorf("tool_call_id = %v, want call_1", tm["tool_call_id"])
	}
}

// TestBuildParamsToolResultNoParts confirms the byte-identical fallback: a result
// with no Parts uses the plain Content string.
func TestBuildParamsToolResultNoParts(t *testing.T) {
	tr := session.NewToolResult("call_2", "just text")
	req := port.LLMRequest{Model: "m", Messages: []session.Message{
		{Role: session.RoleTool, ToolResult: &tr},
	}}
	got := marshalParams(t, req, "")
	msgs, _ := got["messages"].([]any)
	tm, _ := msgs[0].(map[string]any)
	if tm["content"] != "just text" {
		t.Errorf("tool content = %v, want \"just text\"", tm["content"])
	}
}
