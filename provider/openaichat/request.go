package openaichat

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	oai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// buildParams translates a provider-neutral LLMRequest into a
// ChatCompletionNewParams for a stateless, client-owned Chat Completions call.
//
// Mapping:
//   - System (Layered) -> a leading system message (rendered).
//   - Messages -> the messages array (system/user/assistant-with-tool_calls/tool).
//   - Tools -> function tools carrying their JSON schemas (non-strict).
//   - Model -> Model (a plain string).
//   - stream_options.include_usage:true so a terminal usage frame arrives.
//   - reasoning_effort stamped verbatim for the recognised token set (omitted
//     otherwise). effort is an adapter-CONSTRUCTION knob, NOT an LLMRequest field
//     — passed in from the Provider so the request stays provider-neutral.
func buildParams(req port.LLMRequest, effort string) (oai.ChatCompletionNewParams, error) {
	msgs, err := buildMessages(req.System, req.Messages)
	if err != nil {
		return oai.ChatCompletionNewParams{}, err
	}
	tools, err := buildTools(req.Tools)
	if err != nil {
		return oai.ChatCompletionNewParams{}, err
	}
	params := oai.ChatCompletionNewParams{
		Model:         req.Model,
		Messages:      msgs,
		Tools:         tools,
		StreamOptions: oai.ChatCompletionStreamOptionsParam{IncludeUsage: oai.Bool(true)},
	}
	if mapped, ok := reasoningEffortFor(effort); ok {
		params.ReasoningEffort = mapped
	}
	return params, nil
}

// buildParams (method) builds the base params (the free buildParams above,
// which stays the byte-identical baseline for existing callers/tests) and
// then applies the cache-dialect hints (ADR 0100). Mirrors the openai
// (Responses) adapter's free/method split: only the method form — the live
// Stream path — carries the cache dialect.
func (p *Provider) buildParams(req port.LLMRequest) (oai.ChatCompletionNewParams, error) {
	params, err := buildParams(req, p.effort)
	if err != nil {
		return oai.ChatCompletionNewParams{}, err
	}
	p.applyCacheDialect(&params, req)
	return params, nil
}

// applyCacheDialect stamps the ADR 0100 cache hint onto params per
// p.cacheDialect. CacheDialectNone (the zero value) and any unrecognised
// token both fall through the switch's default arm — emit nothing,
// byte-identical to the pre-ADR-0100 wire (fail-soft, mirrors
// reasoningEffortFor's omit-on-unknown arm).
func (p *Provider) applyCacheDialect(params *oai.ChatCompletionNewParams, req port.LLMRequest) {
	switch p.cacheDialect {
	case CacheDialectOpenAI:
		params.PromptCacheKey = oai.String(p.promptCacheKey(req.System.StablePrefix, req.Messages))
	default:
		// CacheDialectNone, or an unrecognised token: emit nothing.
	}
}

// reasoningEffortFor maps a NEUTRAL composition effort token to the SDK's
// shared.ReasoningEffort (a string alias). It passes through mecatl's neutral
// vocabulary — low/medium/high/xhigh/max (see NormalizeReasoningEffort / ADR
// 0055) — VERBATIM, and returns ok=false (OMIT the field) for "" / "auto" / any
// unrecognised token (fail-soft, so a stray token never 400s the request). NOTE:
// unlike the openai (Responses) adapter there is NO xhigh/max->high clamp — this
// endpoint accepts them (verified 2026-07-17). The OpenCode Go endpoint ALSO
// accepts none/adaptive, but mecatl's neutral vocabulary does not expose those, so
// they never reach here; extending the vocabulary is an ADR-0055 concern, not this
// adapter's.
func reasoningEffortFor(token string) (shared.ReasoningEffort, bool) {
	switch token {
	case "low", "medium", "high", "xhigh", "max":
		return shared.ReasoningEffort(token), true
	default:
		return "", false
	}
}

// buildTools converts tool specs into function tool params. Each spec's Schema is
// the JSON schema object for the tool's arguments; an empty schema is sent as an
// empty object so the tool is always well-formed. Tools are NON-STRICT (same
// rationale as the openai adapter: arguments are validated at the execution edge,
// and strict mode would reject genuinely-optional params).
func buildTools(specs []tool.ToolSpec) ([]oai.ChatCompletionToolUnionParam, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	out := make([]oai.ChatCompletionToolUnionParam, 0, len(specs))
	for _, s := range specs {
		var params shared.FunctionParameters
		if len(s.Schema) > 0 {
			if err := json.Unmarshal(s.Schema, &params); err != nil {
				return nil, fmt.Errorf("openaichat: tool %q: invalid JSON schema: %w", s.Name, err)
			}
		} else {
			params = shared.FunctionParameters{"type": "object", "properties": map[string]any{}}
		}
		fn := shared.FunctionDefinitionParam{Name: s.Name, Parameters: params}
		if s.Description != "" {
			fn.Description = oai.String(s.Description)
		}
		out = append(out, oai.ChatCompletionFunctionTool(fn))
	}
	return out, nil
}

// buildMessages converts the layered system prompt + conversation history into
// Chat Completions messages. The system prompt renders to a leading system
// message; user/system text map directly; a user message with media Parts maps to
// a content-part list (text + image_url); an assistant message maps its text and
// its tool calls (function tool_calls); a tool message maps to a role:"tool"
// message keyed by tool_call_id.
func buildMessages(sys prompt.Layered, msgs []session.Message) ([]oai.ChatCompletionMessageParamUnion, error) {
	out := make([]oai.ChatCompletionMessageParamUnion, 0, len(msgs)+1)
	if instr := sys.Render(); instr != "" {
		out = append(out, oai.SystemMessage(instr))
	}
	for _, m := range msgs {
		switch m.Role {
		case session.RoleSystem:
			out = append(out, oai.SystemMessage(m.Text))
		case session.RoleUser:
			if len(m.Parts) == 0 {
				out = append(out, oai.UserMessage(m.Text))
				break
			}
			content, err := userContent(m)
			if err != nil {
				return nil, err
			}
			out = append(out, oai.UserMessage(content))
		case session.RoleAssistant:
			out = append(out, assistantMessage(m))
		case session.RoleTool:
			if m.ToolResult != nil {
				out = append(out, toolMessage(*m.ToolResult))
			}
		default:
			return nil, fmt.Errorf("openaichat: unsupported message role %q", m.Role)
		}
	}
	return out, nil
}

// userContent builds the content-part list for a multimodal user message: a text
// part (only when Text is non-empty) followed by one image_url part per image
// Part (inline bytes as a base64 data URL, or a passthrough URL). Audio is an
// honest hard error — the provider declares Audio:false.
func userContent(m session.Message) ([]oai.ChatCompletionContentPartUnionParam, error) {
	parts := make([]oai.ChatCompletionContentPartUnionParam, 0, len(m.Parts)+1)
	if m.Text != "" {
		parts = append(parts, oai.TextContentPart(m.Text))
	}
	for _, p := range m.Parts {
		switch p.Kind {
		case session.MediaImage:
			url := p.URL
			if url == "" {
				url = dataURL(p.MIMEType, p.Data)
			}
			parts = append(parts, oai.ImageContentPart(oai.ChatCompletionContentPartImageImageURLParam{URL: url}))
		case session.MediaAudio:
			return nil, fmt.Errorf("openaichat: audio input not supported")
		default:
			return nil, fmt.Errorf("openaichat: unsupported media kind %q", p.Kind)
		}
	}
	return parts, nil
}

// assistantMessage maps an assistant turn to a Chat Completions assistant message:
// its visible text (if any) as the content, and each requested tool call as a
// function tool_call. An empty Arguments is sent as "{}" so a no-arg call replays
// as valid JSON. Reasoning/phase are intentionally NOT replayed (Chat Completions
// is stateless across turns).
func assistantMessage(m session.Message) oai.ChatCompletionMessageParamUnion {
	asst := oai.ChatCompletionAssistantMessageParam{}
	if m.Text != "" {
		asst.Content.OfString = oai.String(m.Text)
	}
	for _, call := range m.ToolCalls {
		args := string(call.Args)
		if args == "" {
			args = "{}"
		}
		asst.ToolCalls = append(asst.ToolCalls, oai.ChatCompletionMessageToolCallUnionParam{
			OfFunction: &oai.ChatCompletionMessageFunctionToolCallParam{
				ID: string(call.ID),
				Function: oai.ChatCompletionMessageFunctionToolCallFunctionParam{
					Name:      call.Name,
					Arguments: args,
				},
			},
		})
	}
	return oai.ChatCompletionMessageParamUnion{OfAssistant: &asst}
}

// toolMessage projects a tool result into a role:"tool" message. Chat Completions
// tool-role messages are TEXT-ONLY (the tool content union has no image_url
// member), so typed Parts are routed with a text-only capability:
// text / resource-link / embedded-resource / structured blocks flatten to text
// via session.ToolBlockText; image/audio blocks cannot ride a tool message and
// are dropped by RouteToolResultParts (an explicit decision — a tool-result
// image is not transmittable here, so the model gets the remaining text; parity
// with the openai/anthropic adapters modulo that API limitation). A result with
// no Parts (or none surviving) falls back to the plain Content string —
// byte-identical to the pre-Parts shape and the common NewToolResult path.
func toolMessage(tr session.ToolResult) oai.ChatCompletionMessageParamUnion {
	blocks := port.RouteToolResultParts(tr, port.ProviderCapabilities{EmbeddedContext: true})
	if len(blocks) == 0 {
		return oai.ToolMessage(tr.Content, string(tr.CallID))
	}
	var b strings.Builder
	for i, blk := range blocks {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(session.ToolBlockText(blk))
	}
	return oai.ToolMessage(b.String(), string(tr.CallID))
}

// dataURL renders inline media bytes as an RFC 2397 base64 data URL, the form the
// Chat Completions image_url field accepts.
func dataURL(mime string, data []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}
