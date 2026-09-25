package anthropic

import (
	"cmp"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// cacheTTLFor maps the adapter's raw TTL token (WithCacheTTL) to the SDK's
// CacheControlEphemeralTTL. "" degrades to the zero value (the ttl field is
// omitzero-dropped, so the API's own 5m default applies); an unrecognised
// token ALSO degrades to the zero value — fail-soft, mirroring
// outputConfigEffortFor's omit-on-unknown arm, so a stray/forward value can
// never 400 the request.
func cacheTTLFor(token string) sdk.CacheControlEphemeralTTL {
	switch token {
	case "5m":
		return sdk.CacheControlEphemeralTTLTTL5m
	case "1h":
		return sdk.CacheControlEphemeralTTLTTL1h
	default:
		return ""
	}
}

// encodeBase64 renders inline media bytes as a standard base64 string (the form
// Anthropic's base64 image source expects in its data field).
func encodeBase64(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

// emptyToolOutputPlaceholder / emptyMessagePlaceholder / emptyImageMarker are the
// deterministic stand-ins this adapter substitutes wherever a content block would
// otherwise render to the empty string. This is the EMPTY-TEXT RATIONALE ANCHOR
// for this file: the Anthropic Messages API rejects an empty text content block
// ("text content blocks must be non-empty"), and because the adapter replays full
// history STATELESSLY the rejection is PERMANENT once such a message is recorded —
// a single empty text block bricks the session. Every substitution below keeps the
// wire well-formed without rewriting recorded history; the sites reference this
// anchor rather than repeating the rationale.
//
// emptyToolOutputPlaceholder's value coincides with the openai adapter's const of
// the same name, but that byte-equality is NOT a contract: provider is fixed per
// session, so no replay ever crosses adapters. Each adapter carries its own copy
// (not an engine export) and either may diverge its wording freely.
const (
	emptyToolOutputPlaceholder = "(tool returned no output)"
	emptyMessagePlaceholder    = "(empty message)"
	emptyImageMarker           = "(image block with no data)"
)

// minThinkingBudget is the API floor for budget_tokens on the manual
// (type:"enabled") thinking config.
const minThinkingBudget int64 = 1024

// buildParams translates a provider-neutral LLMRequest into a
// sdk.MessageNewParams for a stateless Messages call.
//
// Mapping:
//   - System (Layered)  -> system[]: a StablePrefix text block carrying the
//     single ephemeral cache_control breakpoint, then a VolatileSuffix block.
//   - Tools             -> tools[] with input_schema (the JSON schema).
//   - Messages          -> messages[]: user/assistant/tool turns; an assistant
//     turn reconstructs its thinking/redacted_thinking blocks (unpacked from
//     Message.Reasoning) BEFORE its tool_use blocks (the sequence rule).
//   - Model             -> model (bare opaque string; passthrough verbatim).
//   - max_tokens        -> resolved per req.Model (the per-model ceiling), NOT a
//     fixed value, so a per-session route to a smaller-ceiling model never 400s.
//   - thinking          -> model-CLASS config: adaptive / manual+budget / NONE.
//
// Conversation caching (ADR 0100, gated by p.conversationCaching, default on):
// on top of the unconditional StablePrefix breakpoint above, buildParams stamps
// two conditional conversation anchors (the leading-turn-0-fragment boundary
// and the previous-turn boundary) and sets the top-level automatic marker
// (MessageNewParams.CacheControl), which self-advances to the last cacheable
// block on every turn — the 4-slot budget. Every breakpoint carries the SAME
// p.cacheTTL (the uniform-TTL rule that makes the documented TTL-ordering 400s
// unreachable).
func (p *Provider) buildParams(req port.LLMRequest) (sdk.MessageNewParams, error) {
	tools, err := buildTools(req.Tools)
	if err != nil {
		return sdk.MessageNewParams{}, err
	}
	ttl := cacheTTLFor(p.cacheTTL)
	messages, builtIdx, err := buildMessages(req.Messages, p.sessionCaps())
	if err != nil {
		return sdk.MessageNewParams{}, err
	}
	if p.conversationCaching {
		applyConversationCacheBreakpoints(messages, builtIdx, req.Messages, ttl)
	}

	maxTokens := p.maxTokensForModel(req.Model)
	params := sdk.MessageNewParams{
		Model:     req.Model,
		MaxTokens: maxTokens,
		Messages:  messages,
		Tools:     tools,
		System:    buildSystem(req.System, ttl),
		Thinking:  thinkingConfigFor(req.Model, maxTokens, p.thinkingBudget, p.thinkingFor),
	}
	if p.conversationCaching {
		marker := sdk.NewCacheControlEphemeralParam()
		marker.TTL = ttl
		params.CacheControl = marker
	}
	// Reasoning effort (ADR 0055) rides output_config.effort, INDEPENDENT of the
	// extended-thinking config above (both coexist on the request). Anthropic
	// identity-maps the neutral vocabulary (low/medium/high/xhigh/max); "" / "auto"
	// / an unrecognised token OMITS the field (the model default applies). The field
	// is omitzero, so an empty OutputConfig is wire-omitted and the byte-stable
	// prompt prefix is unchanged for the no-effort path.
	if mapped, ok := outputConfigEffortFor(p.effort); ok {
		params.OutputConfig = sdk.OutputConfigParam{Effort: mapped}
	}
	return params, nil
}

// applyConversationCacheBreakpoints stamps the two conditional conversation
// breakpoints (slots 2 and 3 of the 4-slot budget, ADR 0100) onto the
// already-built message params, mutating messages in place. Slot 1 (the
// StablePrefix marker) is buildSystem's job; slot 4 (the top-level automatic
// marker) is the caller's. Both anchors are derived from msgs (the domain
// conversation) and mapped to their built position via builtIdx — buildMessages
// skips a RoleTool message carrying a nil ToolResult, so the two index spaces
// are not guaranteed 1:1. Deduped when they resolve to the same message index:
// a redundant second marker on the SAME block changes neither the highest-hit
// prefix nor the write span, so it is dropped rather than wasted.
func applyConversationCacheBreakpoints(messages []sdk.MessageParam, builtIdx []int, msgs []session.Message, ttl sdk.CacheControlEphemeralTTL) {
	fragEnd := leadingFragmentEnd(msgs)
	prevTurn := previousTurnBoundary(msgs)
	if prevTurn == fragEnd {
		prevTurn = -1
	}
	markBuiltMessage(messages, builtIdx, fragEnd, ttl)
	markBuiltMessage(messages, builtIdx, prevTurn, ttl)
}

// markBuiltMessage stamps ttl on the LAST content block of the built message
// corresponding to msgIdx (an index into the domain conversation), via the
// builtIdx mapping produced by buildMessages. A negative/out-of-range msgIdx,
// an unmapped (-1) built index, or an empty content list are all no-ops — the
// anchors are best-effort, never a hard requirement.
func markBuiltMessage(messages []sdk.MessageParam, builtIdx []int, msgIdx int, ttl sdk.CacheControlEphemeralTTL) {
	if msgIdx < 0 || msgIdx >= len(builtIdx) {
		return
	}
	j := builtIdx[msgIdx]
	if j < 0 || j >= len(messages) {
		return
	}
	content := messages[j].Content
	if len(content) == 0 {
		return
	}
	setCacheControl(&content[len(content)-1], ttl)
}

// setCacheControl stamps an ephemeral cache_control breakpoint (carrying ttl,
// which may be "" — the API's own 5m default) on blk's underlying content-block
// variant. It is fail-soft: a content-block kind with no CacheControl field
// (thinking / redacted_thinking are the only ContentBlockParamUnion variants
// GetCacheControl returns nil for) is skipped rather than panicking. Returns
// whether the marker was applied.
func setCacheControl(blk *sdk.ContentBlockParamUnion, ttl sdk.CacheControlEphemeralTTL) bool {
	cc := blk.GetCacheControl()
	if cc == nil {
		return false
	}
	marker := sdk.NewCacheControlEphemeralParam()
	marker.TTL = ttl
	*cc = marker
	return true
}

// leadingFragmentEnd returns the index, within msgs, of the LAST message in the
// leading run of harness-injected turn-0 context fragments (RoleUser messages
// whose Text matches prompt.IsInjectedTurn0Fragment) — or -1 when msgs does not
// open with one. The leading-run rule (stop at the first non-match) mirrors
// compaction's preservedHead discipline: an old persisted session may carry a
// fragment mid-history (a prior harness version, or a session resumed from
// before ADR 0043), and only the CONTIGUOUS leading run is the byte-stable
// [system][fragments] prefix worth a breakpoint.
//
// Duplicated (not exported from engine/prompt) in provider/openai's cache-key
// anchor derivation — separate Go modules, same discipline as
// isContextOverflowMessage; keep the two in lockstep by hand.
func leadingFragmentEnd(msgs []session.Message) int {
	end := -1
	for i := range msgs {
		m := msgs[i]
		if m.Role != session.RoleUser || !prompt.IsInjectedTurn0Fragment(m.Text) {
			break
		}
		end = i
	}
	return end
}

// lastAssistantIndex returns the index, within msgs, of the LAST RoleAssistant
// message, or -1 if there is none.
func lastAssistantIndex(msgs []session.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == session.RoleAssistant {
			return i
		}
	}
	return -1
}

// previousTurnBoundary returns the index, within msgs, of the message
// immediately BEFORE the last assistant message — the previous-turn boundary
// breakpoint (slot 3). It returns -1 when there is no assistant message yet
// (turn 0) or the last assistant message is msgs[0] (no predecessor to mark).
// Two adjacent RoleAssistant messages are unreachable (finishTurnNoTools
// records an empty assistant turn then a USER nudge), so lastAssistantIndex's
// scan is unambiguous.
func previousTurnBoundary(msgs []session.Message) int {
	i := lastAssistantIndex(msgs)
	if i <= 0 {
		return -1
	}
	return i - 1
}

// outputConfigEffortFor maps the NEUTRAL composition effort token to the SDK's
// sdk.OutputConfigEffort. Anthropic supports the full neutral vocabulary, so all
// five tiers identity-map; "" / "auto" / any UNRECOGNISED token returns ok=false
// (OMIT the field) — fail-soft, so a stray/forward token never 400s the request.
func outputConfigEffortFor(token string) (sdk.OutputConfigEffort, bool) {
	switch token {
	case "low":
		return sdk.OutputConfigEffortLow, true
	case "medium":
		return sdk.OutputConfigEffortMedium, true
	case "high":
		return sdk.OutputConfigEffortHigh, true
	case "xhigh":
		return sdk.OutputConfigEffortXhigh, true
	case "max":
		return sdk.OutputConfigEffortMax, true
	default:
		// "", "auto", or an unknown/forward token: omit output_config.effort.
		return "", false
	}
}

// buildSystem renders the two-layer system prompt into Anthropic's system[]
// param. The StablePrefix becomes the first text block carrying the SINGLE
// ephemeral cache_control breakpoint (slot 1 of the 4-slot budget, ADR 0100;
// the prefix tools→system is cached up to and including it) — unconditional,
// unaffected by WithConversationCaching, and carrying ttl so it stays uniform
// with every OTHER breakpoint the adapter emits; the VolatileSuffix becomes a
// second, breakpoint-free block. Empty parts are dropped; both empty -> nil (no
// system param).
func buildSystem(l prompt.Layered, ttl sdk.CacheControlEphemeralTTL) []sdk.TextBlockParam {
	var blocks []sdk.TextBlockParam
	if l.StablePrefix != "" {
		marker := sdk.NewCacheControlEphemeralParam()
		marker.TTL = ttl
		blocks = append(blocks, sdk.TextBlockParam{
			Text:         l.StablePrefix,
			CacheControl: marker,
		})
	}
	if l.VolatileSuffix != "" {
		blocks = append(blocks, sdk.TextBlockParam{Text: l.VolatileSuffix})
	}
	return blocks
}

// thinkingConfigFor selects the model-CLASS-appropriate extended-thinking config.
// There are THREE outcomes (a wrong one 400s):
//
//   - ADAPTIVE — {type:"adaptive"} — Opus 4.8/4.7/4.6, Sonnet 4.6, Mythos preview.
//     The only mode on Opus 4.8/4.7 (manual 400s there).
//   - MANUAL — {type:"enabled",budget_tokens:N} (N≥1024, N<max_tokens) — older
//     thinking-CAPABLE families (Claude 4: Sonnet 4.5/4, Opus 4.5/4.1/4, Haiku 4.5;
//     and Claude 3.7 Sonnet).
//   - NONE — omit the thinking field entirely — thinking-INCAPABLE models (Claude
//     3.5 and earlier). Sending {type:"enabled"} to these 400s.
//
// display is set to "summarized" explicitly so the harness receives streamed
// thinking deltas for display (the default flips to "omitted" on Opus 4.8/4.7).
//
// MODE SELECTION is LIVE-FIRST: when resolve reports known=true for the model, its
// adaptive/enabled bits decide the mode authoritatively (the reliability win — a
// newly-released model the prefix lists don't know gets its true mode from the live
// API). When known=false (no resolver, offline, or the model is absent from the live
// list) it falls back to the EXISTING prefix matrix (usesAdaptiveThinking /
// thinkingCapable), the deterministic OFFLINE floor.
func thinkingConfigFor(model string, maxTokens, budget int64, resolve thinkingResolver) sdk.ThinkingConfigParamUnion {
	adaptive, manual := thinkingMode(model, resolve)
	if adaptive {
		return sdk.ThinkingConfigParamUnion{
			OfAdaptive: &sdk.ThinkingConfigAdaptiveParam{
				Display: sdk.ThinkingConfigAdaptiveDisplaySummarized,
			},
		}
	}
	// Thinking-INCAPABLE: omit the thinking field — a manual {type:"enabled"} 400s on
	// these (Claude 3.5 and earlier, or a live "none" model).
	if !manual {
		return sdk.ThinkingConfigParamUnion{}
	}
	// Manual: clamp budget to [minThinkingBudget, max_tokens-1].
	if budget < minThinkingBudget {
		budget = minThinkingBudget
	}
	if budget >= maxTokens {
		budget = maxTokens - 1
	}
	// If max_tokens is too small to fit even the floor, omit thinking rather than
	// send an invalid config (a tiny max_tokens is a misconfiguration, not worth a
	// guaranteed 400).
	if budget < minThinkingBudget {
		return sdk.ThinkingConfigParamUnion{}
	}
	return sdk.ThinkingConfigParamUnion{
		OfEnabled: &sdk.ThinkingConfigEnabledParam{
			BudgetTokens: budget,
			Display:      sdk.ThinkingConfigEnabledDisplaySummarized,
		},
	}
}

// thinkingMode decides a model's extended-thinking mode as (adaptive, manual). It
// is LIVE-FIRST: when resolve reports known=true the live bits are authoritative —
// adaptive wins, else enabled ⇒ manual, else NEITHER ⇒ none (both false). When
// known=false (nil resolver / offline / model absent from the live list) it falls
// back to the embedded prefix matrix (usesAdaptiveThinking / thinkingCapable), the
// deterministic offline floor that is NEVER removed.
func thinkingMode(model string, resolve thinkingResolver) (adaptive, manual bool) {
	if resolve != nil {
		if a, e, known := resolve(model); known {
			// Trust the live descriptor: adaptive precedence, then manual, then none.
			return a, !a && e
		}
	}
	if usesAdaptiveThinking(model) {
		return true, false
	}
	// thinkingCapable returns false only for the prefix-listed incapable families;
	// an unknown/uncatalogued id is treated as manual-capable (the broad safe default).
	return false, thinkingCapable(model)
}

// usesAdaptiveThinking reports whether model REQUIRES (or, for 4.6, recommends and
// accepts) adaptive thinking: Opus 4.8/4.7/4.6, Sonnet 4.6, Mythos preview. The
// match is on the dateless/aliased id prefix so a dated snapshot
// (claude-opus-4-8-20YYMMDD) also matches.
func usesAdaptiveThinking(model string) bool {
	return hasAnyPrefix(model, adaptiveThinkingPrefixes)
}

// thinkingCapable reports whether model supports extended thinking AT ALL (any
// mode). An adaptive model is trivially capable; otherwise only the manual-capable
// families qualify. Verified against the live Anthropic docs (extended-thinking,
// 2026-06-06): extended thinking is a Claude-3.7-and-Claude-4+ feature — Claude
// 3.5 and earlier (3.5 sonnet/haiku, 3 opus/sonnet/haiku) do NOT support it.
// An UNKNOWN/uncatalogued id is treated as capable-manual (the safe broad default:
// only Opus 4.8/4.7 hard-400 on manual and those match usesAdaptiveThinking;
// sending manual to a future capable model is correct, and the only cost of being
// wrong on a future INcapable model is one provider 400 surfaced verbatim).
func thinkingCapable(model string) bool {
	if usesAdaptiveThinking(model) {
		return true
	}
	if hasAnyPrefix(model, thinkingIncapablePrefixes) {
		return false
	}
	return true
}

// hasAnyPrefix reports whether the lower-cased, trimmed model id starts with any
// of the prefixes.
func hasAnyPrefix(model string, prefixes []string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	for _, prefix := range prefixes {
		if strings.HasPrefix(m, prefix) {
			return true
		}
	}
	return false
}

// adaptiveThinkingPrefixes are the model-id prefixes that take adaptive thinking.
// Verified against the live Anthropic docs (adaptive-thinking, 2026-06-06):
// adaptive is supported on Mythos preview, Opus 4.8, Opus 4.7, Opus 4.6, Sonnet
// 4.6; it is the ONLY mode on Opus 4.8/4.7.
var adaptiveThinkingPrefixes = []string{
	"claude-opus-4-8",
	"claude-opus-4-7",
	"claude-opus-4-6",
	"claude-sonnet-4-6",
	"claude-mythos-preview",
}

// thinkingIncapablePrefixes are the model-id prefixes that do NOT support extended
// thinking at all (Claude 3.5 and earlier). Verified against the live Anthropic
// docs (extended-thinking, 2026-06-06): extended thinking is Claude 3.7 + 4+ only.
// Order matters only for readability — each is an exact family-version prefix.
var thinkingIncapablePrefixes = []string{
	"claude-3-5-sonnet",
	"claude-3-5-haiku",
	"claude-3-opus",
	"claude-3-sonnet",
	"claude-3-haiku",
}

// buildTools converts tool specs into ToolUnionParams. Each spec's Schema is the
// JSON schema object for the tool's arguments; it is unmarshalled into the
// input_schema's properties/required. An empty schema is sent as an empty object
// so the tool is always well-formed (mirrors the openai adapter's guard).
func buildTools(specs []tool.ToolSpec) ([]sdk.ToolUnionParam, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	out := make([]sdk.ToolUnionParam, 0, len(specs))
	for _, s := range specs {
		schema, err := toolInputSchema(s)
		if err != nil {
			return nil, err
		}
		tp := sdk.ToolParam{Name: s.Name, InputSchema: schema}
		if s.Description != "" {
			tp.Description = sdk.String(s.Description)
		}
		out = append(out, sdk.ToolUnionParam{OfTool: &tp})
	}
	return out, nil
}

// toolInputSchema decodes a tool spec's JSON-Schema bytes into the SDK's
// ToolInputSchemaParam, passing the WHOLE schema through (not just
// properties+required) so $defs / additionalProperties / nested enums / top-level
// constraints survive on the wire — matching the openai adapter's full-schema
// fidelity. properties+required ride their typed fields; every OTHER top-level key
// rides ExtraFields (the SDK marshals them inline alongside type:"object"). An
// empty schema yields an empty object so a tool with no args is well-formed.
func toolInputSchema(s tool.ToolSpec) (sdk.ToolInputSchemaParam, error) {
	if len(s.Schema) == 0 {
		return sdk.ToolInputSchemaParam{Properties: map[string]any{}}, nil
	}
	var raw map[string]any
	if err := json.Unmarshal(s.Schema, &raw); err != nil {
		return sdk.ToolInputSchemaParam{}, fmt.Errorf("tool %q: invalid JSON schema: %w", s.Name, err)
	}
	schema := sdk.ToolInputSchemaParam{}
	if props, ok := raw["properties"]; ok {
		schema.Properties = props
	} else {
		schema.Properties = map[string]any{}
	}
	if req, ok := raw["required"]; ok {
		schema.Required = toStringSlice(req)
	}
	// Every other top-level key (additionalProperties, $defs, $schema, title,
	// patternProperties, ...) survives via ExtraFields. type is handled by the SDK's
	// constant.Object default, so drop a redundant top-level "type":"object".
	var extras map[string]any
	for k, v := range raw {
		switch k {
		case "properties", "required":
			continue
		case "type":
			if sv, ok := v.(string); ok && sv == "object" {
				continue
			}
		}
		if extras == nil {
			extras = map[string]any{}
		}
		extras[k] = v
	}
	schema.ExtraFields = extras
	return schema, nil
}

// toStringSlice coerces a decoded JSON value (typically []any of strings) into a
// []string for the SDK's typed Required field; non-string elements are skipped.
func toStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// buildMessages converts the conversation history into Anthropic messages.
//
// Anthropic has no in-array system role (the system prompt is the dedicated
// param), tool results ride on a USER-role message as tool_result blocks, and an
// assistant turn places its thinking blocks BEFORE its tool_use blocks. A
// RoleSystem message (the harness puts the system prompt in LLMRequest.System,
// so this is belt-and-suspenders) is folded into a user text block.
//
// The second return value, builtIdx, maps each index of msgs to its position in
// the returned slice, or -1 when that message produced no block (a RoleTool
// message with a nil ToolResult — the only skip case). It is the mapping the
// conversation-cache anchors (leadingFragmentEnd / previousTurnBoundary, both
// derived from msgs) use to locate their target block in the built slice, since
// the two index spaces are not otherwise guaranteed 1:1.
func buildMessages(msgs []session.Message, caps port.ProviderCapabilities) ([]sdk.MessageParam, []int, error) {
	out := make([]sdk.MessageParam, 0, len(msgs))
	builtIdx := make([]int, len(msgs))
	for i, m := range msgs {
		builtIdx[i] = -1
		switch m.Role {
		case session.RoleSystem:
			// No system role inside messages; fold to a user text turn. An empty
			// system Text gets the placeholder (see the const-block anchor comment):
			// an empty text block on the wire is the same permanent-brick poison.
			out = append(out, sdk.NewUserMessage(sdk.NewTextBlock(cmp.Or(m.Text, emptyMessagePlaceholder))))
		case session.RoleUser:
			blocks, err := userBlocks(m, caps)
			if err != nil {
				return nil, nil, err
			}
			out = append(out, sdk.NewUserMessage(blocks...))
		case session.RoleAssistant:
			out = append(out, sdk.NewAssistantMessage(assistantBlocks(m)...))
		case session.RoleTool:
			if m.ToolResult == nil {
				continue
			}
			out = append(out, sdk.NewUserMessage(toolResultBlock(*m.ToolResult, caps)))
		default:
			return nil, nil, fmt.Errorf("anthropic: unsupported message role %q", m.Role)
		}
		builtIdx[i] = len(out) - 1
	}
	return out, builtIdx, nil
}

// toolResultBlock builds the tool_result content block for a tool result.
//
// When the result carries typed Parts AND at least one block survives the per-
// session capability projection (port.RouteToolResultParts), the tool_result
// carries a CONTENT-BLOCK LIST — the Anthropic Messages API's ToolResultBlockParam
// accepts text + image blocks (sdk.NewTextBlock / sdk.NewImageBlockBase64). Text
// / resource-link / embedded-resource-text / structured-content blocks become
// text blocks (the model sees the reference/summary/JSON as text); an image
// block becomes a base64 image block (inline bytes) or a URL image block. An
// audio block never reaches here: the Anthropic adapter declares Audio:false, so
// RouteToolResultParts drops it upstream.
//
// Otherwise (no Parts, or routing returns nil — every block filtered out by the
// capability intersection) it falls back to the single-string
// NewToolResultBlock(callID, Content, isError) — BYTE-IDENTICAL to the pre-T7
// path, so the legacy/mock/mecademo path is unchanged.
func toolResultBlock(tr session.ToolResult, caps port.ProviderCapabilities) sdk.ContentBlockParamUnion {
	blocks := port.RouteToolResultParts(tr, caps)
	if len(blocks) == 0 {
		// Legacy single-string form — byte-identical to the pre-T7 path, which
		// hard-coded is_error=false (it did not project tr.IsError). Preserved
		// verbatim so the legacy/mock/mecademo path is unchanged, EXCEPT an empty
		// Content is substituted with a deterministic placeholder (see the const-block
		// anchor comment for the empty-text-brick rationale).
		return sdk.NewToolResultBlock(string(tr.CallID), cmp.Or(tr.Content, emptyToolOutputPlaceholder), false)
	}
	content := make([]sdk.ToolResultBlockParamContentUnion, 0, len(blocks))
	for _, b := range blocks {
		switch b.BlockKind {
		case session.BlockImage:
			switch {
			case len(b.Data) > 0:
				content = append(content, sdk.ToolResultBlockParamContentUnion{
					OfImage: &sdk.ImageBlockParam{
						Source: sdk.ImageBlockParamSourceUnion{
							OfBase64: &sdk.Base64ImageSourceParam{
								Data:      encodeBase64(b.Data),
								MediaType: sdk.Base64ImageSourceMediaType(b.MIMEType),
							},
						},
					},
				})
			case b.URL != "":
				content = append(content, sdk.ToolResultBlockParamContentUnion{
					OfImage: &sdk.ImageBlockParam{
						Source: sdk.ImageBlockParamSourceUnion{
							OfURL: &sdk.URLImageSourceParam{URL: b.URL},
						},
					},
				})
			default:
				// An image block with neither bytes nor a URL is malformed; render an
				// honest non-empty marker rather than drop the block silently. Its
				// ToolBlockText is almost always "" (an image block carries no Text),
				// so fall back to the marker when the render is empty (see the
				// const-block anchor comment).
				content = append(content, sdk.ToolResultBlockParamContentUnion{
					OfText: &sdk.TextBlockParam{Text: cmp.Or(session.ToolBlockText(b), emptyImageMarker)},
				})
			}
		default:
			// Text-summarised block. RouteToolResultParts (a separately-versioned
			// engine module) drops empty-render blocks upstream, so this branch's
			// empty-guard is DELIBERATELY REDUNDANT cross-module defense — kept, not
			// dead: the drop and this guard live in independently-releasable modules
			// and the failure mode is a permanently bricked session (anchor comment).
			// This is a tool result, so the placeholder is the tool-output wording.
			content = append(content, sdk.ToolResultBlockParamContentUnion{
				OfText: &sdk.TextBlockParam{Text: cmp.Or(session.ToolBlockText(b), emptyToolOutputPlaceholder)},
			})
		}
	}
	blk := sdk.ToolResultBlockParam{
		ToolUseID: string(tr.CallID),
		Content:   content,
	}
	return sdk.ContentBlockParamUnion{OfToolResult: &blk}
}

// userBlocks builds a user message's content blocks: a text block (when Text is
// non-empty) followed by one block per media Part. An image Part becomes a
// base64 image block (media_type from MIMEType); a URL-referenced image is sent
// inline as base64 only when bytes are present — Parts carrying a remote URL with
// no bytes are not supported here (the harness flattens inline media). An audio
// Part is an honest hard error (Audio:false gates it upstream).
func userBlocks(m session.Message, caps port.ProviderCapabilities) ([]sdk.ContentBlockParamUnion, error) {
	var blocks []sdk.ContentBlockParamUnion
	if m.Text != "" {
		blocks = append(blocks, sdk.NewTextBlock(m.Text))
	}
	for _, part := range m.Parts {
		switch part.Kind {
		case session.MediaImage:
			switch {
			case len(part.Data) > 0:
				blocks = append(blocks, sdk.NewImageBlockBase64(part.MIMEType, encodeBase64(part.Data)))
			case part.URL != "":
				blocks = append(blocks, sdk.NewImageBlock(sdk.URLImageSourceParam{URL: part.URL}))
			default:
				return nil, fmt.Errorf("anthropic: image part has neither inline bytes nor a URL")
			}
		case session.MediaAudio:
			return nil, fmt.Errorf("anthropic: audio input not supported by the Messages API")
		case pdfMediaKind:
			if !hasPDFCapability(caps) {
				return nil, fmt.Errorf("anthropic: PDF input not supported by selected model")
			}
			if err := validateHydratedPDF(part); err != nil {
				return nil, err
			}
			doc := sdk.NewDocumentBlock(sdk.Base64PDFSourceParam{Data: encodeBase64(part.Data)})
			doc.OfDocument.Title = sdk.String(part.Name)
			blocks = append(blocks, doc)
		default:
			return nil, fmt.Errorf("anthropic: unsupported media kind %q", part.Kind)
		}
	}
	if len(blocks) == 0 {
		// A degenerate empty user turn: send a single NON-EMPTY placeholder text
		// block. Anthropic rejects an empty text content block ("text content blocks
		// must be non-empty"), and stateless full-replay makes that rejection
		// permanent — an empty sdk.NewTextBlock(m.Text) here (m.Text is "" on this
		// path) is a latent 400.
		blocks = append(blocks, sdk.NewTextBlock(emptyMessagePlaceholder))
	}
	return blocks, nil
}

// validateHydratedPDF accepts only a temporary, session-resolved PDF part. The
// native Messages request carries its bytes, never a private artifact key or URL.
func validateHydratedPDF(part session.Content) error {
	if part.BlockKind != "" || part.MIMEType != "application/pdf" || part.URL != "" {
		return fmt.Errorf("anthropic: invalid PDF input")
	}
	digest, valid := pdfMetadata(part)
	if !valid {
		return fmt.Errorf("anthropic: invalid PDF input metadata")
	}
	if len(part.Data) == 0 || int64(len(part.Data)) != part.Size {
		return fmt.Errorf("anthropic: PDF input bytes not resolved")
	}
	sum := sha256.Sum256(part.Data)
	if hex.EncodeToString(sum[:]) != digest {
		return fmt.Errorf("anthropic: PDF input digest mismatch")
	}
	return nil
}

// assistantBlocks expands an assistant message into its ordered content blocks,
// reconstructing the model's OWN generation order: the reconstructed
// thinking/redacted_thinking blocks (unpacked from Message.Reasoning) FIRST,
// then a text block when the assistant produced visible text, then a tool_use
// block per requested tool call LAST. tool_use must trail — Anthropic requires
// it as the terminal content block of a tool-bearing assistant turn, especially
// under extended thinking; a turn replayed with text AFTER its tool_use (the
// order this function used to emit) 400s with a misleading
// "assistant message prefill" error (issue #1465). Thinking-before-tool_use is
// the §extended-thinking sequence rule: omitting or misordering the thinking
// blocks on a tool-bearing assistant turn 400s too.
func assistantBlocks(m session.Message) []sdk.ContentBlockParamUnion {
	reasoning := unpackReasoning(m.Reasoning)
	out := make([]sdk.ContentBlockParamUnion, 0, len(reasoning)+len(m.ToolCalls)+1)
	for _, blk := range reasoning {
		switch blk.Kind {
		case reasoningKindThinking:
			out = append(out, sdk.NewThinkingBlock(blk.Signature, blk.Thinking))
		case reasoningKindRedacted:
			out = append(out, sdk.NewRedactedThinkingBlock(blk.Data))
		}
	}
	if m.Text != "" {
		out = append(out, sdk.NewTextBlock(m.Text))
	}
	for _, call := range m.ToolCalls {
		out = append(out, sdk.NewToolUseBlock(string(call.ID), call.Args, call.Name))
	}
	if len(out) == 0 {
		// A degenerate empty assistant turn (reachable via the no-progress-nudge
		// path, which records an empty assistant turn): send a NON-EMPTY placeholder
		// text block. Anthropic rejects an empty text content block ("text content
		// blocks must be non-empty"), and stateless full-replay makes that rejection
		// permanent — an empty sdk.NewTextBlock("") here is a latent 400.
		out = append(out, sdk.NewTextBlock(emptyMessagePlaceholder))
	}
	return out
}
