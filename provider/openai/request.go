package openai

import (
	"cmp"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"

	oai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// emptyToolOutputPlaceholder is the deterministic stand-in the single-string
// function_call_output fallback sends when a tool result's model-facing Content
// is empty. A strict Responses upstream (Moonshot via OpenRouter) rejects an
// empty text output ("Invalid request: text content is empty"), and because the
// adapter replays full history statelessly the rejection is PERMANENT once such a
// result is recorded. The placeholder keeps the wire well-formed without
// rewriting recorded history. Its value coincides with the anthropic adapter's
// const of the same name, but that byte-equality is NOT a contract: provider is
// fixed per session, so no replay crosses adapters. Each adapter carries its own
// copy (not an engine export) and either may diverge its wording freely.
const emptyToolOutputPlaceholder = "(tool returned no output)"

// buildParams translates a provider-neutral LLMRequest into a
// responses.ResponseNewParams for a stateless, client-owned Responses call.
//
// Mapping:
//   - System (Layered) -> Instructions (StablePrefix + VolatileSuffix rendered).
//   - Tools ([]tool.ToolSpec) -> function tools carrying their JSON schemas.
//   - Messages ([]session.Message) -> the input item array (message,
//     reasoning, function_call, function_call_output items).
//   - Model -> Model (a plain string for compatible-endpoint friendliness).
//   - Store:false + Include reasoning.encrypted_content so reasoning survives
//     across turns statelessly.
//
// This free form projects tool results with the adapter's STATIC transmit caps
// (text+image; the byte-identical pre-T7 default for tests and any caller that
// does not hold a per-session intersection). The method form below threads the
// per-session intersection (WithProviderCapabilities) so a text-only model on an
// image-capable adapter drops image blocks from a tool result's Parts.
func buildParams(req port.LLMRequest) (responses.ResponseNewParams, error) {
	tools, err := buildTools(req.Tools)
	if err != nil {
		return responses.ResponseNewParams{}, err
	}
	// -1: the free buildParams is the pre-existing default-path form and stays
	// byte-identical, the same scoping ADR 0100 used for its cache hints.
	items, err := buildInput(req.Messages, port.ProviderCapabilities{Image: true, EmbeddedContext: true}, -1)
	if err != nil {
		return responses.ResponseNewParams{}, err
	}

	params := responses.ResponseNewParams{
		Model: req.Model,
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: items},
		Tools: tools,
		Store: oai.Bool(false),
		Include: []responses.ResponseIncludable{
			responses.ResponseIncludableReasoningEncryptedContent,
		},
	}
	if instr := req.System.Render(); instr != "" {
		params.Instructions = oai.String(instr)
	}
	return params, nil
}

// buildParams (method) builds the base params (the free buildParams) and then
// stamps the construction-configured reasoning effort onto reasoning.effort (ADR
// 0055). Effort is an adapter-CONSTRUCTION knob, NOT a port.LLMRequest field — the
// request stays provider-neutral; the per-session engine factory re-mints the
// adapter when a session's effort differs from the operator default. p.effort is
// an ALREADY-CLAMPED neutral token (composition clamps xhigh/max→high for OpenAI,
// with a diagnostic). reasoningEffortFor maps a recognised token verbatim and
// returns ok=false (omit) for empty/"auto"/unknown (fail-soft — a stray token
// never 400s the request).
func (p *Provider) buildParams(req port.LLMRequest) (responses.ResponseNewParams, error) {
	tools, err := buildTools(req.Tools)
	if err != nil {
		return responses.ResponseNewParams{}, err
	}
	// Thread the per-SESSION capability intersection (WithProviderCapabilities)
	// into the input build so a tool result's typed Parts are projected honestly
	// — a text-only model on this image-capable adapter drops image blocks. The
	// free buildParams above uses the static transmit caps (the byte-identical
	// pre-T7 default); only the method (the live Stream path) carries the session
	// intersection.
	items, err := buildInput(req.Messages, p.sessionCaps(), p.breakpointIndex(req))
	if err != nil {
		return responses.ResponseNewParams{}, err
	}

	params := responses.ResponseNewParams{
		Model: req.Model,
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: items},
		Tools: tools,
		Store: oai.Bool(false),
		Include: []responses.ResponseIncludable{
			responses.ResponseIncludableReasoningEncryptedContent,
		},
	}
	if instr := req.System.Render(); instr != "" {
		params.Instructions = oai.String(instr)
	}
	if mapped, ok := reasoningEffortFor(p.effort); ok {
		params.Reasoning = shared.ReasoningParam{Effort: mapped}
	}
	p.applyCacheDialect(&params, req)
	return params, nil
}

// applyCacheDialect stamps the ADR 0100 cache hints onto params per
// p.cacheDialect. CacheDialectNone (the zero value) and any unrecognised
// token both fall through the switch's default arm — emit nothing,
// byte-identical to the pre-ADR-0100 wire (fail-soft, mirrors
// reasoningEffortFor's omit-on-unknown arm).
//
//   - CacheDialectOpenAI: prompt_cache_key always; prompt_cache_retention
//     only on an allow-listed model (retentionFor) — never guessed.
//   - CacheDialectOpenRouter: prompt_cache_key only. NEVER
//     prompt_cache_retention (an OpenAI-only field), and since ADR 0346 no
//     root cache_control either — the explicit prompt_cache_breakpoint
//     buildInput places is the protocol-native ask, on every endpoint.
func (p *Provider) applyCacheDialect(params *responses.ResponseNewParams, req port.LLMRequest) {
	switch p.cacheDialect {
	case CacheDialectOpenAI:
		params.PromptCacheKey = oai.String(p.promptCacheKey(req.System.StablePrefix, req.Messages))
		if retention, ok := retentionFor(req.Model); ok {
			params.PromptCacheRetention = retention
		}
	case CacheDialectOpenRouter:
		params.PromptCacheKey = oai.String(p.promptCacheKey(req.System.StablePrefix, req.Messages))
		// Root cache_control is RETIRED (ADR 0346 decision 3). It was an
		// OpenRouter-private extension, so it had to be gated on endpoint
		// identity — and that gate is what silently disabled caching on three
		// other endpoint shapes. OpenRouter converts an explicit
		// prompt_cache_breakpoint for Anthropic and Google, so the protocol
		// field subsumes it, and emitting both would be two mechanisms for one
		// intent (the root field self-advances; explicit markers name a block).
	default:
		// CacheDialectNone, or an unrecognised token: emit nothing.
	}
}

// reasoningEffortFor maps a NEUTRAL composition effort token to the SDK's
// shared.ReasoningEffort. It returns ok=false (OMIT the field) for "" / "auto" and
// for any UNRECOGNISED token — fail-soft, so a stray/forward token never 400s a
// reasoning request. Composition clamps xhigh/max→high for OpenAI BEFORE the
// adapter sees the value (this adapter has no port.Diagnostics to narrate a
// clamp), so in practice only low/medium/high reach here; the remaining native SDK
// tiers (none/minimal/xhigh) are mapped too, for forward safety should composition
// ever loosen the clamp.
func reasoningEffortFor(token string) (shared.ReasoningEffort, bool) {
	switch token {
	case "low":
		return shared.ReasoningEffortLow, true
	case "medium":
		return shared.ReasoningEffortMedium, true
	case "high":
		return shared.ReasoningEffortHigh, true
	case "none":
		return shared.ReasoningEffortNone, true
	case "minimal":
		return shared.ReasoningEffortMinimal, true
	case "xhigh":
		return shared.ReasoningEffortXhigh, true
	default:
		// "", "auto", or an unknown/forward token: omit reasoning.effort entirely.
		return "", false
	}
}

// buildTools converts tool specs into function ToolUnionParams. Each spec's
// Schema is the JSON schema object for the tool's arguments; it is unmarshalled
// into the map[string]any the SDK expects. An empty schema is sent as an empty
// object so the tool is always well-formed.
//
// Tools are sent NON-STRICT (Strict left unset → SDK omits it → upstream default
// is non-strict). Strict mode requires every tool schema's `required` to list ALL
// of its `properties` keys, but many built-in tools carry genuinely optional
// params (Shell timeout_ms, Edit replace_all, Read offset/limit, Grep path, the
// memory Remember/query tools, ToolSearch, Fork, Team, Subagent,…). A
// strict-enforcing OpenAI-compatible upstream (e.g. Azure reached via OpenRouter)
// would 400 those schemas. We do not need strict's guarantee: tool arguments are
// validated at the execution edge — every tool re-parses and validates its args
// via session.ParseArgs / NewToolError before doing anything — so a malformed or
// missing-optional argument is caught there, not by the wire schema. This adapter
// is shared by the "openai" and "openrouter" provider ids, so non-strict here
// covers both.
func buildTools(specs []tool.ToolSpec) ([]responses.ToolUnionParam, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	out := make([]responses.ToolUnionParam, 0, len(specs))
	for _, s := range specs {
		var params map[string]any
		if len(s.Schema) > 0 {
			if err := json.Unmarshal(s.Schema, &params); err != nil {
				return nil, fmt.Errorf("tool %q: invalid JSON schema: %w", s.Name, err)
			}
		} else {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		fn := responses.FunctionToolParam{
			Name:       s.Name,
			Parameters: params,
			// Strict intentionally left unset (non-strict) — see buildTools doc.
		}
		if s.Description != "" {
			fn.Description = oai.String(s.Description)
		}
		out = append(out, responses.ToolUnionParam{OfFunction: &fn})
	}
	return out, nil
}

// buildInput converts the conversation history into Responses input items.
//
// Per the reasoning-preservation rule the ordering within an assistant turn is:
// reasoning item -> function_call item(s) -> (then tool results as
// function_call_output items in subsequent tool messages). User/system text
// become message items; tool messages become function_call_output items keyed by
// call_id.
// breakpointIdx names the message that carries the explicit prompt-cache
// breakpoint, or -1 for none. See breakpointIndex.
func buildInput(msgs []session.Message, caps port.ProviderCapabilities, breakpointIdx int) (responses.ResponseInputParam, error) {
	items := make(responses.ResponseInputParam, 0, len(msgs))
	for i, m := range msgs {
		switch m.Role {
		case session.RoleSystem:
			items = append(items, responses.ResponseInputItemParamOfMessage(
				m.Text, responses.EasyInputMessageRoleSystem))
		case session.RoleUser:
			// Text-only fast path: keep the EXACT simple-string message form so the
			// byte-stable prompt prefix and every existing fixture are unchanged.
			// A marked message cannot take it: prompt_cache_breakpoint lives on an
			// input_text BLOCK (ADR 0346), so the marked message is promoted to a
			// one-element content list. That shifts its bytes once, then it is
			// stable again.
			if len(m.Parts) == 0 && i != breakpointIdx {
				items = append(items, responses.ResponseInputItemParamOfMessage(
					m.Text, responses.EasyInputMessageRoleUser))
				break
			}
			// Multimodal, or the marked message: build a content-list message
			// (input_text + per-part media).
			content, perr := userContentList(m, caps)
			if perr != nil {
				return nil, perr
			}
			if i == breakpointIdx {
				markPromptCacheBreakpoint(content)
			}
			items = append(items, responses.ResponseInputItemParamOfMessage(
				content, responses.EasyInputMessageRoleUser))
		case session.RoleAssistant:
			items = append(items, assistantItems(m)...)
		case session.RoleTool:
			if m.ToolResult != nil {
				items = append(items, toolOutputItem(*m.ToolResult, caps))
			}
		default:
			return nil, fmt.Errorf("unsupported message role %q", m.Role)
		}
	}
	return items, nil
}

// toolOutputItem builds the function_call_output input item for a tool result.
//
// When the result carries typed Parts AND at least one block survives the
// per-session capability projection (port.RouteToolResultParts), the output is a
// MULTIMODAL content list — the Responses API accepts input_text + input_image
// parts for a function_call_output (openai-go v3.37.0,
// ResponseFunctionCallOutputItemListParam). Text / resource-link / embedded-
// resource-text / structured-content blocks become input_text parts (the model
// sees the reference/summary/JSON as text); an image block becomes an input_image
// part (inline bytes as a base64 data URL, or a passthrough URL) — reusing the
// same dataURL rendering as the user-message image path. An audio block never
// reaches here: the OpenAI adapter declares Audio:false, so RouteToolResultParts
// drops it upstream.
//
// Otherwise (no Parts, or routing returns nil — every block filtered out by the
// capability intersection) it falls back to the single-string
// function_call_output(callID, Content) — BYTE-IDENTICAL to the pre-T7 path, so
// the legacy/mock/mecademo path is unchanged.
func toolOutputItem(tr session.ToolResult, caps port.ProviderCapabilities) responses.ResponseInputItemUnionParam {
	blocks := port.RouteToolResultParts(tr, caps)
	if len(blocks) == 0 {
		// Single-string fallback. Substitute a deterministic placeholder for an
		// EMPTY Content (see emptyToolOutputPlaceholder's doc for the
		// permanent-brick rationale). Non-empty Content stays BYTE-IDENTICAL
		// (prompt-cache byte-stability + existing fixtures).
		item := responses.ResponseInputItemParamOfFunctionCallOutput(
			cmp.Or(tr.Content, emptyToolOutputPlaceholder))
		item.OfFunctionCallOutput.CallID = oai.String(string(tr.CallID))
		return item
	}
	list := make(responses.ResponseFunctionCallOutputItemListParam, 0, len(blocks))
	for _, b := range blocks {
		switch b.BlockKind {
		case session.BlockImage:
			url := b.URL
			if url == "" {
				url = dataURL(b.MIMEType, b.Data)
			}
			list = append(list, responses.ResponseFunctionCallOutputItemUnionParam{
				OfInputImage: &responses.ResponseInputImageContentParam{ImageURL: oai.String(url)},
			})
		default:
			// BlockText / BlockResourceLink / BlockEmbeddedResource (text form) /
			// BlockStructuredContent all render as text — the model sees the
			// reference/summary/JSON as text. An embedded-resource BLOB (Data, no
			// Text) has no Responses input member; render its MIME/URI as a text
			// pointer rather than base64-dumping (mirrors the user-message path's
			// honest "summarize, don't dump" stance).
			list = append(list, responses.ResponseFunctionCallOutputItemParamOfInputText(session.ToolBlockText(b)))
		}
	}
	item := responses.ResponseInputItemParamOfFunctionCallOutput(list)
	item.OfFunctionCallOutput.CallID = oai.String(string(tr.CallID))
	return item
}

// userContentList builds the Responses input-message content list for a
// multimodal user message: an input_text part (only when Text is non-empty)
// followed by one part per media Part. An image Part becomes an input_image
// whose image_url is the part's URL or a base64 data URL of its inline bytes
// (the Responses API accepts both in the same field). An audio Part is an honest
// hard error: the Responses input-message content union has no audio member
// (openai-go v3.37.0), and the provider declares Audio:false, so a surface
// adapter rejects audio upstream — this is the belt-and-suspenders guard for a
// part that slips through.
func userContentList(m session.Message, caps port.ProviderCapabilities) (responses.ResponseInputMessageContentListParam, error) {
	content := responses.ResponseInputMessageContentListParam{}
	if m.Text != "" {
		content = append(content, responses.ResponseInputContentUnionParam{
			OfInputText: &responses.ResponseInputTextParam{Text: m.Text},
		})
	}
	for _, p := range m.Parts {
		switch p.Kind {
		case session.MediaImage:
			url := p.URL
			if url == "" {
				url = dataURL(p.MIMEType, p.Data)
			}
			content = append(content, responses.ResponseInputContentUnionParam{
				OfInputImage: &responses.ResponseInputImageParam{ImageURL: oai.String(url)},
			})
		case session.MediaAudio:
			return nil, fmt.Errorf("openai: audio input not supported by Responses API")
		case session.MediaPDF:
			if !caps.PDF {
				return nil, fmt.Errorf("openai: PDF input not supported by selected model")
			}
			if err := validateHydratedPDF(p); err != nil {
				return nil, err
			}
			content = append(content, responses.ResponseInputContentUnionParam{
				OfInputFile: &responses.ResponseInputFileParam{
					FileData: oai.String(dataURL("application/pdf", p.Data)),
					Filename: oai.String(p.Name),
				},
			})
		default:
			return nil, fmt.Errorf("openai: unsupported media kind %q", p.Kind)
		}
	}
	return content, nil
}

// validateHydratedPDF accepts only a temporary, session-resolved PDF part. The
// provider never dereferences a caller URL or treats an artifact ID as a public
// file ID. The digest binds the transient bytes to the server-resolved metadata.
func validateHydratedPDF(p session.Content) error {
	if p.BlockKind != "" || p.MIMEType != "application/pdf" || p.URL != "" {
		return fmt.Errorf("openai: invalid PDF input")
	}
	if _, err := session.NewPDFContent(p.ArtifactID, p.Name, p.Size, p.SHA256); err != nil {
		return fmt.Errorf("openai: invalid PDF input metadata")
	}
	if len(p.Data) == 0 || int64(len(p.Data)) != p.Size {
		return fmt.Errorf("openai: PDF input bytes not resolved")
	}
	sum := sha256.Sum256(p.Data)
	if hex.EncodeToString(sum[:]) != p.SHA256 {
		return fmt.Errorf("openai: PDF input digest mismatch")
	}
	return nil
}

// dataURL renders inline media bytes as an RFC 2397 base64 data URL
// ("data:<mime>;base64,<...>"), the form the Responses API accepts in an
// input_image image_url field.
func dataURL(mime string, data []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// assistantItems expands an assistant message into its ordered input items: the
// reasoning items (carrying encrypted_content verbatim) INTERLEAVED with the
// function_call items in the order the model originally emitted them, then a text
// message item if the assistant produced visible text.
//
// The interleaving is the point. A reasoning turn that calls tools emits
// rs_a, fc_1, rs_b, fc_2, rs_c — and the stateless-replay rule is to pass those
// prior output items back untouched, so hoisting every reasoning item to the
// front (the shape this used to emit, when a turn only ever had one) rewrites the
// model's own trajectory. session.Message keeps reasoning and tool calls in two
// separate fields with no relative order between them, so the adapter records
// each reasoning item's slot in its own envelope (reasoningItem.After) and
// rebuilds the sequence here. A single-item turn and any pre-packing blob both
// carry After 0 and replay reasoning-then-calls exactly as before.
//
// The opaque PHASE marker (m.ProviderPhase, "commentary"/"final_answer") rides ONLY the
// emitted message item — phase is a message-item property, and a tool-call-only
// turn has no message item, so phase is correctly N/A there (not a dropped
// field). For store:false manual-replay apps OpenAI requires preserving and
// resending phase on the assistant message item, or GPT-5.x models treat
// preambles as final answers / stop early. An empty phase is wire-omitted (the
// SDK tags it json:"phase,omitzero"), so the byte-stable prompt prefix is
// unchanged for non-tagging models. The value is passed through verbatim — never
// validated against the enum (forward-compat).
func assistantItems(m session.Message) []responses.ResponseInputItemUnionParam {
	out := make([]responses.ResponseInputItemUnionParam, 0, len(m.ToolCalls)+2)
	// One input item per captured reasoning item, in emission order, each under
	// the id ITS blob is bound to. Sending several blobs under one id is what the
	// provider rejects as invalid_encrypted_content, so the pairing is preserved
	// end to end (see reasoning.go). Only a complete current envelope is
	// replayable; historical bare ciphertext and malformed/unsupported envelopes
	// are omitted rather than reinterpreted.
	//
	// The SDK's ResponseReasoningItemParam.ID is a PLAIN string tagged
	// `json:"id" api:"required"` with NO omitzero, so an unset id serialises
	// unconditionally as `"id":""`, which strict OpenAI-compatible gateways (Azure
	// GPT-5.x) reject with HTTP 400 on turn 2+ during store:false stateless
	// replay. unpackReasoningItems validates the complete envelope, so every item
	// reaching this loop carries both its opaque id and encrypted content.
	items := unpackReasoningItems(m.Reasoning)
	next := 0
	emitReasoning := func(it reasoningItem) {
		reasoning := responses.ResponseReasoningItemParam{
			ID:               it.ID,
			Summary:          []responses.ResponseReasoningItemSummaryParam{},
			EncryptedContent: oai.String(it.Blob),
		}
		out = append(out, responses.ResponseInputItemUnionParam{OfReasoning: &reasoning})
	}
	// emitReasoningBefore flushes every reasoning item the model produced before
	// tool call #idx. Items arrive with a non-decreasing After, so one forward
	// cursor walks them in order.
	emitReasoningBefore := func(idx int) {
		for next < len(items) && items[next].After <= idx {
			emitReasoning(items[next])
			next++
		}
	}
	for i, call := range m.ToolCalls {
		emitReasoningBefore(i)
		fc := responses.ResponseFunctionToolCallParam{
			Arguments: string(call.Args),
			CallID:    string(call.ID),
			Name:      call.Name,
		}
		// Replay the provider-assigned item ID (e.g. "fc_1" from the OpenAI
		// Responses API) so the provider can de-duplicate items across turns in
		// store:false stateless replay. Without this, each turn's function_call
		// items have no "id" field and the provider auto-assigns sequential fc_N
		// values — which can collide with items from the current response and
		// produce "Duplicate item found with id fc_N" (Azure GPT-5.x). An empty
		// ItemID (non-OpenAI or pre-fix captures) is wire-omitted via omitzero.
		if call.ItemID != "" {
			fc.ID = oai.String(call.ItemID)
		}
		out = append(out, responses.ResponseInputItemUnionParam{OfFunctionCall: &fc})
	}
	// Drain whatever is left: reasoning the model produced after its last tool
	// call, every item on a turn with no tool calls, and — the reason this is
	// UNCONDITIONAL rather than one more emitReasoningBefore — any item whose
	// stored After overruns the calls this message actually carries (a truncated
	// or hand-edited snapshot). Position is best-effort; never dropping a blob is
	// not, since a dropped one is exactly the continuity loss the envelope exists
	// to prevent.
	for ; next < len(items); next++ {
		emitReasoning(items[next])
	}
	if m.Text != "" {
		item := responses.ResponseInputItemParamOfMessage(
			m.Text, responses.EasyInputMessageRoleAssistant)
		if m.ProviderPhase != "" {
			item.OfMessage.Phase = responses.EasyInputMessagePhase(m.ProviderPhase)
		}
		out = append(out, item)
	}
	return out
}
