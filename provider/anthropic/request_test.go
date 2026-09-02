package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func testProvider() *Provider {
	return New(WithAPIKey("sk-test"), WithMaxTokens(16000), WithThinkingBudget(4096))
}

func TestBuildParamsCacheControlBreakpoint(t *testing.T) {
	p := testProvider()
	req := port.LLMRequest{
		Model:  "claude-sonnet-4-6",
		System: prompt.Layered{StablePrefix: "STABLE", VolatileSuffix: "VOLATILE"},
	}
	params, err := p.buildParams(req)
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	if len(params.System) != 2 {
		t.Fatalf("system blocks = %d, want 2", len(params.System))
	}
	if params.System[0].Text != "STABLE" {
		t.Errorf("system[0].Text = %q, want STABLE", params.System[0].Text)
	}
	// Assert on the marshalled WIRE form: the single ephemeral breakpoint lands on
	// the StablePrefix block and NOT on the VolatileSuffix block.
	stable, err := json.Marshal(params.System[0])
	if err != nil {
		t.Fatalf("marshal system[0]: %v", err)
	}
	if !strings.Contains(string(stable), `"cache_control":{"type":"ephemeral"}`) {
		t.Errorf("StablePrefix block missing ephemeral cache_control breakpoint: %s", stable)
	}
	volatile, err := json.Marshal(params.System[1])
	if err != nil {
		t.Fatalf("marshal system[1]: %v", err)
	}
	if strings.Contains(string(volatile), "cache_control") {
		t.Errorf("VolatileSuffix block must NOT carry a cache breakpoint: %s", volatile)
	}
}

func TestBuildParamsEmptySystemOmitted(t *testing.T) {
	p := testProvider()
	params, err := p.buildParams(port.LLMRequest{Model: "claude-sonnet-4-6"})
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	if len(params.System) != 0 {
		t.Fatalf("empty system should be omitted, got %d blocks", len(params.System))
	}
}

func TestBuildParamsMaxTokensSet(t *testing.T) {
	p := testProvider()
	params, _ := p.buildParams(port.LLMRequest{Model: "claude-sonnet-4-6"})
	if params.MaxTokens != 16000 {
		t.Fatalf("max_tokens = %d, want 16000 (the WithMaxTokens option)", params.MaxTokens)
	}
}

func TestBuildParamsMaxTokensDefault(t *testing.T) {
	p := New(WithAPIKey("sk"))
	params, _ := p.buildParams(port.LLMRequest{Model: "claude-sonnet-4-6"})
	if params.MaxTokens != defaultMaxTokens {
		t.Fatalf("max_tokens = %d, want default %d", params.MaxTokens, defaultMaxTokens)
	}
}

// TestBuildParamsMaxTokensPerRequestModel is the per-session-routing guard: a
// Provider built with a LARGE construction fallback + a per-model resolver must
// send each REQUEST's model ceiling, not the fallback. A route to a
// smaller-ceiling model (claude-3-5-haiku=8192) sends ≤8192 even though the
// fallback is 64000 — otherwise it 400s every turn. (The mock e2e cannot catch
// this; it is the buildParams unit path.)
func TestBuildParamsMaxTokensPerRequestModel(t *testing.T) {
	// Resolver mirrors the real catalog ceilings for these ids.
	resolver := func(model string) int {
		switch {
		case strings.HasPrefix(model, "claude-3-5-haiku"):
			return 8192
		case strings.HasPrefix(model, "claude-3-opus"):
			return 4096
		case strings.HasPrefix(model, "claude-sonnet-4-6"):
			return 64000
		default:
			return 0 // uncatalogued -> adapter fallback
		}
	}
	// Construction fallback is the BIG default-model ceiling (the bug's source).
	p := New(WithAPIKey("sk"), WithMaxTokens(64000), WithMaxTokensResolver(resolver))

	cases := []struct {
		model string
		want  int64
	}{
		{"claude-3-5-haiku-20241022", 8192},
		{"claude-3-opus-20240229", 4096},
		{"claude-sonnet-4-6", 64000},
		{"some-uncatalogued-model", 64000}, // falls back to WithMaxTokens
	}
	for _, tc := range cases {
		params, err := p.buildParams(port.LLMRequest{Model: tc.model})
		if err != nil {
			t.Fatalf("%s: buildParams: %v", tc.model, err)
		}
		if params.MaxTokens != tc.want {
			t.Errorf("%s: max_tokens = %d, want %d (per-request model ceiling)", tc.model, params.MaxTokens, tc.want)
		}
	}

	// Explicit: a haiku request must NEVER exceed the haiku ceiling even though the
	// construction fallback is 64000.
	params, _ := p.buildParams(port.LLMRequest{Model: "claude-3-5-haiku-20241022"})
	if params.MaxTokens > 8192 {
		t.Fatalf("haiku route sent max_tokens=%d > 8192 ceiling (would 400)", params.MaxTokens)
	}
}

// TestMaxTokensResolverNilFallsBack: with no resolver, every request uses the
// construction fallback (back-compat with WithMaxTokens alone).
func TestMaxTokensResolverNilFallsBack(t *testing.T) {
	p := New(WithAPIKey("sk"), WithMaxTokens(12345))
	params, _ := p.buildParams(port.LLMRequest{Model: "claude-3-5-haiku-20241022"})
	if params.MaxTokens != 12345 {
		t.Fatalf("nil resolver max_tokens = %d, want the 12345 fallback", params.MaxTokens)
	}
}

// TestThinkingConfigModelAware verifies the THREE-class thinking config: adaptive
// (Opus 4.8/4.7/4.6 + Sonnet 4.6 + Mythos), manual enabled+budget (older capable:
// 4.x + 3.7 sonnet), and NONE (3.5 and earlier — incapable). A wrong config 400s,
// so this is load-bearing.
func TestThinkingConfigModelAware(t *testing.T) {
	const maxTokens = 16000
	const budget = 4096

	// Class 1: ADAPTIVE.
	adaptive := []string{
		"claude-opus-4-8",
		"claude-opus-4-7",
		"claude-opus-4-6",
		"claude-sonnet-4-6",
		"claude-opus-4-8-20260101", // dated snapshot still matches
		"claude-mythos-preview",
		"  CLAUDE-OPUS-4-8  ", // whitespace + case-insensitive
	}
	for _, m := range adaptive {
		cfg := thinkingConfigFor(m, maxTokens, budget, nil)
		if cfg.OfAdaptive == nil {
			t.Errorf("%s: want adaptive thinking, got %+v", m, cfg)
		}
		if cfg.OfEnabled != nil {
			t.Errorf("%s: must NOT send manual enabled thinking (400s on Opus 4.8/4.7)", m)
		}
	}

	// Class 2: MANUAL enabled+budget (older thinking-capable).
	manual := []string{
		"claude-sonnet-4-5",
		"claude-opus-4-5",
		"claude-haiku-4-5",
		"claude-sonnet-4-20250514",
		"claude-3-7-sonnet-20250219",
		"some-unknown-future-model", // unknown => capable-manual (safe broad default)
	}
	for _, m := range manual {
		cfg := thinkingConfigFor(m, maxTokens, budget, nil)
		if cfg.OfEnabled == nil {
			t.Errorf("%s: want manual enabled+budget thinking, got %+v", m, cfg)
			continue
		}
		if cfg.OfEnabled.BudgetTokens != budget {
			t.Errorf("%s: budget = %d, want %d", m, cfg.OfEnabled.BudgetTokens, budget)
		}
		if cfg.OfAdaptive != nil {
			t.Errorf("%s: must NOT send adaptive thinking", m)
		}
	}

	// Class 3: NONE — thinking-INCAPABLE (Claude 3.5 and earlier) => omit thinking.
	incapable := []string{
		"claude-3-5-sonnet-20241022",
		"claude-3-5-sonnet-20240620",
		"claude-3-5-haiku-20241022",
		"claude-3-5-haiku-latest",
		"claude-3-opus-20240229",
		"claude-3-sonnet-20240229",
		"claude-3-haiku-20240307",
		"CLAUDE-3-5-SONNET-20241022", // case-insensitive
	}
	for _, m := range incapable {
		cfg := thinkingConfigFor(m, maxTokens, budget, nil)
		if cfg.OfEnabled != nil || cfg.OfAdaptive != nil {
			t.Errorf("%s: must send NO thinking config (incapable model 400s on {type:enabled}), got %+v", m, cfg)
		}
		// A zero union must marshal without a thinking field.
		if (cfg != sdk.ThinkingConfigParamUnion{}) {
			t.Errorf("%s: thinking union must be the zero value (no field), got %+v", m, cfg)
		}
	}
}

// TestBuildParamsNoThinkingForIncapableModel proves the end-to-end buildParams
// path omits thinking for a 3.5 model (the request-level guard, not just the
// helper).
func TestBuildParamsNoThinkingForIncapableModel(t *testing.T) {
	p := testProvider()
	params, err := p.buildParams(port.LLMRequest{Model: "claude-3-5-sonnet-20241022"})
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	if (params.Thinking != sdk.ThinkingConfigParamUnion{}) {
		t.Fatalf("3.5-sonnet request carried a thinking config (would 400): %+v", params.Thinking)
	}
}

func TestThinkingConfigBudgetClampedBelowMaxTokens(t *testing.T) {
	// budget >= max_tokens must be clamped strictly below max_tokens.
	cfg := thinkingConfigFor("claude-sonnet-4-5", 2000, 8000, nil)
	if cfg.OfEnabled == nil {
		t.Fatal("want manual thinking config")
	}
	if cfg.OfEnabled.BudgetTokens >= 2000 {
		t.Fatalf("budget %d not clamped below max_tokens 2000", cfg.OfEnabled.BudgetTokens)
	}
}

func TestBuildParamsThinkingSelectedByRequestModel(t *testing.T) {
	p := testProvider()
	opus, _ := p.buildParams(port.LLMRequest{Model: "claude-opus-4-8"})
	if opus.Thinking.OfAdaptive == nil {
		t.Error("claude-opus-4-8 request should select adaptive thinking")
	}
	sonnet45, _ := p.buildParams(port.LLMRequest{Model: "claude-sonnet-4-5"})
	if sonnet45.Thinking.OfEnabled == nil {
		t.Error("claude-sonnet-4-5 request should select manual enabled thinking")
	}
}

// TestBuildParamsLiveMaxTokensClampsThinkingBudget is the load-bearing 400-trap: the
// LIVE-derived max_tokens (via WithMaxTokensResolver) and the manual thinking budget
// must interact safely END-TO-END through buildParams. Anthropic rejects a request
// where budget_tokens >= max_tokens, so a SMALL live ceiling MUST clamp the (large)
// configured budget strictly below it. This drives the clamp through the resolver,
// not with literal args.
func TestBuildParamsLiveMaxTokensClampsThinkingBudget(t *testing.T) {
	// A live resolver returns a SMALL ceiling (5000) for a MANUAL-thinking model,
	// while the configured budget is LARGE (8000) — without the clamp this 400s.
	resolver := func(string) int { return 5000 }
	p := New(WithAPIKey("sk"), WithMaxTokensResolver(resolver), WithThinkingBudget(8000))

	params, err := p.buildParams(port.LLMRequest{Model: "claude-sonnet-4-5"})
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	if params.MaxTokens != 5000 {
		t.Fatalf("MaxTokens = %d, want the live ceiling 5000", params.MaxTokens)
	}
	if params.Thinking.OfEnabled == nil {
		t.Fatalf("claude-sonnet-4-5 should select manual enabled thinking, got %+v", params.Thinking)
	}
	if params.Thinking.OfEnabled.BudgetTokens >= params.MaxTokens {
		t.Fatalf("budget_tokens %d not clamped below max_tokens %d (would 400)",
			params.Thinking.OfEnabled.BudgetTokens, params.MaxTokens)
	}
}

// TestBuildParamsLiveCeilingBelowMinBudgetOmitsThinking is the degenerate case: a
// live ceiling BELOW the minimum thinking budget floor (1024) leaves no room for a
// valid manual thinking config, so the thinking field must be OMITTED entirely
// rather than sent as an invalid {type:"enabled"} config (which 400s).
func TestBuildParamsLiveCeilingBelowMinBudgetOmitsThinking(t *testing.T) {
	resolver := func(string) int { return 1000 } // below the 1024 minThinkingBudget floor
	p := New(WithAPIKey("sk"), WithMaxTokensResolver(resolver), WithThinkingBudget(8000))

	params, err := p.buildParams(port.LLMRequest{Model: "claude-sonnet-4-5"})
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	if params.MaxTokens != 1000 {
		t.Fatalf("MaxTokens = %d, want the live ceiling 1000", params.MaxTokens)
	}
	if (params.Thinking != sdk.ThinkingConfigParamUnion{}) {
		t.Fatalf("tiny live ceiling must omit thinking (no room for the floor), got %+v", params.Thinking)
	}
}

func TestBuildToolsInputSchema(t *testing.T) {
	p := testProvider()
	req := port.LLMRequest{
		Model: "claude-sonnet-4-6",
		Tools: []tool.ToolSpec{{
			Name:        "read_file",
			Description: "Read a file",
			Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
		}},
	}
	params, err := p.buildParams(req)
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	if len(params.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(params.Tools))
	}
	tp := params.Tools[0].OfTool
	if tp == nil || tp.Name != "read_file" {
		t.Fatalf("tool = %+v, want read_file", tp)
	}
	if len(tp.InputSchema.Required) != 1 || tp.InputSchema.Required[0] != "path" {
		t.Errorf("input_schema.required = %v, want [path]", tp.InputSchema.Required)
	}
}

func TestBuildToolsEmptySchema(t *testing.T) {
	p := testProvider()
	params, err := p.buildParams(port.LLMRequest{
		Model: "claude-sonnet-4-6",
		Tools: []tool.ToolSpec{{Name: "ping"}},
	})
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	tp := params.Tools[0].OfTool
	if tp.InputSchema.Properties == nil {
		t.Error("empty schema must produce an empty object properties, not nil")
	}
}

// TestBuildToolsSchemaFidelity proves the WHOLE tool schema survives onto the wire
// (matching the openai adapter): $defs, additionalProperties, a nested enum, and a
// top-level title must all appear in the marshalled input_schema, not be dropped
// to a bare properties+required.
func TestBuildToolsSchemaFidelity(t *testing.T) {
	p := testProvider()
	schema := `{
		"type":"object",
		"title":"EditArgs",
		"additionalProperties":false,
		"properties":{
			"mode":{"type":"string","enum":["a","b"]},
			"ref":{"$ref":"#/$defs/Ref"}
		},
		"required":["mode"],
		"$defs":{"Ref":{"type":"string","enum":["x","y"]}}
	}`
	params, err := p.buildParams(port.LLMRequest{
		Model: "claude-sonnet-4-6",
		Tools: []tool.ToolSpec{{Name: "edit", Schema: json.RawMessage(schema)}},
	})
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	blob, err := json.Marshal(params.Tools[0].OfTool.InputSchema)
	if err != nil {
		t.Fatalf("marshal input_schema: %v", err)
	}
	got := string(blob)
	for _, want := range []string{
		`"additionalProperties":false`,
		`"$defs"`,
		`"title":"EditArgs"`,
		`"enum":["a","b"]`,
		`"enum":["x","y"]`,
		`"$ref":"#/$defs/Ref"`,
		`"required":["mode"]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("input_schema dropped %s; got: %s", want, got)
		}
	}
	// A duplicate top-level "type":"object" must appear exactly once (the SDK's
	// constant default), not twice.
	if n := strings.Count(got, `"type":"object"`); n != 1 {
		t.Errorf("top-level type:object appears %d times, want exactly 1: %s", n, got)
	}
}

// TestAssistantThinkingBeforeToolUse is the 400-trap replay assertion: an
// assistant message with packed reasoning + a tool call must reconstruct the
// thinking block BEFORE the tool_use block in the outgoing request.
func TestAssistantThinkingBeforeToolUse(t *testing.T) {
	packed := packReasoning([]reasoningBlock{
		{Kind: reasoningKindThinking, Thinking: "ponder", Signature: "SIG=="},
	})
	msg := session.Message{
		Role:      session.RoleAssistant,
		Reasoning: packed,
		ToolCalls: []session.ToolCall{{ID: "toolu_1", Name: "read_file", Args: json.RawMessage(`{"path":"x"}`)}},
	}
	blocks := assistantBlocks(msg)
	if len(blocks) != 2 {
		t.Fatalf("assistant blocks = %d, want 2 (thinking, tool_use)", len(blocks))
	}
	if blocks[0].OfThinking == nil {
		t.Fatal("first block must be a thinking block (sequence rule: thinking before tool_use)")
	}
	if blocks[0].OfThinking.Signature != "SIG==" {
		t.Errorf("thinking signature = %q, want SIG==", blocks[0].OfThinking.Signature)
	}
	if blocks[1].OfToolUse == nil {
		t.Fatal("second block must be the tool_use block")
	}
	if blocks[1].OfToolUse.ID != "toolu_1" {
		t.Errorf("tool_use id = %q, want toolu_1", blocks[1].OfToolUse.ID)
	}
}

func TestAssistantRedactedThinkingReconstructed(t *testing.T) {
	packed := packReasoning([]reasoningBlock{{Kind: reasoningKindRedacted, Data: "RD=="}})
	msg := session.Message{Role: session.RoleAssistant, Reasoning: packed, Text: "hi"}
	blocks := assistantBlocks(msg)
	if blocks[0].OfRedactedThinking == nil {
		t.Fatal("first block must be a redacted_thinking block")
	}
	if blocks[0].OfRedactedThinking.Data != "RD==" {
		t.Errorf("redacted data = %q, want RD==", blocks[0].OfRedactedThinking.Data)
	}
}

func TestBuildMessagesToolResultIsUserRole(t *testing.T) {
	p := testProvider()
	req := port.LLMRequest{
		Model: "claude-sonnet-4-6",
		Messages: []session.Message{
			session.NewToolMessage(session.ToolResult{CallID: "toolu_1", Content: "42"}),
		},
	}
	params, err := p.buildParams(req)
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	if len(params.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(params.Messages))
	}
	m := params.Messages[0]
	if m.Role != "user" {
		t.Errorf("tool_result message role = %q, want user", m.Role)
	}
	if m.Content[0].OfToolResult == nil {
		t.Fatal("expected a tool_result block")
	}
	if m.Content[0].OfToolResult.ToolUseID != "toolu_1" {
		t.Errorf("tool_use_id = %q, want toolu_1", m.Content[0].OfToolResult.ToolUseID)
	}
}

func TestBuildMessagesUserTextFastPath(t *testing.T) {
	p := testProvider()
	params, err := p.buildParams(port.LLMRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []session.Message{session.NewUserMessage("hello")},
	})
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	m := params.Messages[0]
	if m.Role != "user" || m.Content[0].OfText == nil || m.Content[0].OfText.Text != "hello" {
		t.Fatalf("user message = %+v, want a text block 'hello'", m)
	}
}

// TestRequestNoOrphanedToolUseAfterInterrupt drives a session to a cancelled
// turn (assistant tool_use with no result), recovers it via Interrupt (which
// closes out the orphan), and asserts the built request has a matching
// tool_result for every tool_use — no orphan that Anthropic would 400 on.
func TestRequestNoOrphanedToolUseAfterInterrupt(t *testing.T) {
	sess := &session.Session{ID: "s1", Mode: session.ModeDefault, State: session.StateIdle, Conversation: &session.Conversation{}}
	if err := sess.RecordUserPrompt("read the file", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := sess.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	calls := []session.ToolCall{session.NewToolCall("toolu_1", "read_file", json.RawMessage(`{"path":"x"}`))}
	if err := sess.RecordAssistant(session.NewAssistantMessage("", "", calls)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	if err := sess.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := sess.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	p := testProvider()
	params, err := p.buildParams(port.LLMRequest{
		Model:    "claude-sonnet-4-6",
		Messages: sess.Conversation.Messages,
	})
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	toolUseIDs := map[string]bool{}
	toolResultIDs := map[string]bool{}
	for _, m := range params.Messages {
		for _, c := range m.Content {
			if c.OfToolUse != nil {
				toolUseIDs[c.OfToolUse.ID] = true
			}
			if c.OfToolResult != nil {
				toolResultIDs[c.OfToolResult.ToolUseID] = true
			}
		}
	}
	if len(toolUseIDs) == 0 {
		t.Fatal("precondition: expected at least one tool_use block in the request")
	}
	for id := range toolUseIDs {
		if !toolResultIDs[id] {
			t.Fatalf("tool_use %q has no matching tool_result (orphan that would 400)", id)
		}
	}
}

// --- ADR 0100: conversation cache breakpoints -------------------------------

// turn0Fragment builds a RoleUser message that matches prompt.IsInjectedTurn0Fragment
// via the SAME "Project instructions (" marker prefix builder.go's
// instructionFiles renders for AGENTS.md — IsInjectedTurn0Fragment matches on
// that broader prefix (not the exact header string), so this stays robust to a
// reword of the marker's trailing text.
func turn0Fragment(body string) session.Message {
	return session.NewUserMessage("Project instructions (AGENTS.md):\n" + body)
}

// cacheProvider builds a Provider for the conversation-cache test suite, always
// supplying a max-tokens/thinking-budget floor so buildParams never errors on
// an unrelated concern.
func cacheProvider(opts ...Option) *Provider {
	base := []Option{WithAPIKey("sk-test"), WithMaxTokens(16000), WithThinkingBudget(4096)}
	return New(append(base, opts...)...)
}

// countCacheControl counts the "cache_control" JSON keys present in raw — one
// per explicitly-marked content block (the field is omitzero, so an unmarked
// block never contributes).
func countCacheControl(raw []byte) int {
	return strings.Count(string(raw), `"cache_control"`)
}

// collectTTLValues recursively walks a decoded-JSON value (map[string]any /
// []any, the shape json.Unmarshal into `any` produces) and records every "ttl"
// key's string value into out.
func collectTTLValues(v any, out map[string]bool) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if k == "ttl" {
				if s, ok := val.(string); ok {
					out[s] = true
				}
			}
			collectTTLValues(val, out)
		}
	case []any:
		for _, e := range t {
			collectTTLValues(e, out)
		}
	}
}

// explicitCacheMarkerCount returns the number of cache_control markers on the
// system + messages arrays ONLY — the explicit breakpoints (slots 1-3), never
// the top-level automatic marker (slot 4), so the anti-400 breakpoint budget
// (≤3 explicit + the automatic marker) can be asserted precisely.
func explicitCacheMarkerCount(t *testing.T, params sdk.MessageNewParams) int {
	t.Helper()
	sysRaw, err := json.Marshal(params.System)
	if err != nil {
		t.Fatalf("marshal system: %v", err)
	}
	msgRaw, err := json.Marshal(params.Messages)
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}
	return countCacheControl(sysRaw) + countCacheControl(msgRaw)
}

// wideFanOutConversation builds a leading turn-0-fragment pair, a genuine user
// prompt, an assistant turn requesting k parallel tool calls, k tool-result
// messages, and a final assistant text turn — modelling the wide read-parallel
// dispatch the previous-turn-boundary anchor (slot 3) exists to guard (a turn
// adds 1 thinking + K tool_use + 1 text + K tool_result blocks, which for
// K >= 9 pushes the previous turn's automatic-marker entry outside Anthropic's
// 20-block backward lookback).
func wideFanOutConversation(k int) []session.Message {
	msgs := []session.Message{
		turn0Fragment("first fragment"),
		turn0Fragment("last fragment"),
		session.NewUserMessage("Please read these files in parallel."),
	}
	calls := make([]session.ToolCall, 0, k)
	for i := 0; i < k; i++ {
		calls = append(calls, session.NewToolCall(
			session.ToolCallID(fmt.Sprintf("call_%d", i)), "Read",
			json.RawMessage(fmt.Sprintf(`{"path":"f%d.go"}`, i))))
	}
	msgs = append(msgs, session.NewAssistantMessage("", "", calls))
	for i := 0; i < k; i++ {
		msgs = append(msgs, session.NewToolMessage(
			session.NewToolResult(session.ToolCallID(fmt.Sprintf("call_%d", i)), "file body")))
	}
	msgs = append(msgs, session.NewAssistantMessage("Read all files.", "", nil))
	return msgs
}

func TestBuildParamsDefaultTTLIsWireIdentical(t *testing.T) {
	p := cacheProvider()
	params, err := p.buildParams(port.LLMRequest{
		Model:    "claude-sonnet-4-6",
		System:   prompt.Layered{StablePrefix: "STABLE", VolatileSuffix: "VOLATILE"},
		Messages: wideFanOutConversation(3),
	})
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if strings.Contains(string(raw), `"ttl"`) {
		t.Fatalf("default TTL must omit the ttl key entirely: %s", raw)
	}
	if !strings.Contains(string(raw), `"cache_control":{"type":"ephemeral"}`) {
		t.Fatalf("expected at least one byte-identical ephemeral marker: %s", raw)
	}
	if params.CacheControl.Type == "" {
		t.Error("top-level automatic marker (slot 4) must be present")
	}
}

func TestBuildParamsCacheTTLUniformAcrossMarkers(t *testing.T) {
	p := cacheProvider(WithCacheTTL("1h"))
	params, err := p.buildParams(port.LLMRequest{
		Model:    "claude-sonnet-4-6",
		System:   prompt.Layered{StablePrefix: "STABLE", VolatileSuffix: "VOLATILE"},
		Messages: wideFanOutConversation(3),
	})
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	ttls := map[string]bool{}
	collectTTLValues(decoded, ttls)
	if len(ttls) != 1 || !ttls["1h"] {
		t.Fatalf("distinct ttl values = %v, want exactly {\"1h\"}", ttls)
	}
	// Every emitted cache_control marker must carry the SAME ttl — a marker
	// present without one would not show up in ttls above, so cross-check the
	// marker count against the ttl occurrence count directly.
	markers := countCacheControl(raw)
	ttlHits := strings.Count(string(raw), `"ttl":"1h"`)
	if markers != ttlHits {
		t.Fatalf("cache_control markers = %d but only %d carry ttl=1h — uniformity broken", markers, ttlHits)
	}
}

func TestBuildParamsTurn0FragmentAnchor(t *testing.T) {
	p := cacheProvider()
	msgs := []session.Message{
		turn0Fragment("first fragment"),
		turn0Fragment("last fragment"),
		session.NewUserMessage("Please summarise the architecture."),
	}
	params, err := p.buildParams(port.LLMRequest{Model: "claude-sonnet-4-6", Messages: msgs})
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	raw0, _ := json.Marshal(params.Messages[0])
	raw1, _ := json.Marshal(params.Messages[1])
	raw2, _ := json.Marshal(params.Messages[2])
	if countCacheControl(raw0) != 0 {
		t.Errorf("first fragment (not the last of the leading run) must not carry a marker: %s", raw0)
	}
	if countCacheControl(raw1) != 1 {
		t.Errorf("last leading fragment must carry the anchor marker: %s", raw1)
	}
	if countCacheControl(raw2) != 0 {
		t.Errorf("the genuine user message must not carry the fragment marker: %s", raw2)
	}
}

func TestBuildParamsNoFragmentsNoAnchor(t *testing.T) {
	p := cacheProvider()
	params, err := p.buildParams(port.LLMRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []session.Message{session.NewUserMessage("Please summarise the architecture.")},
	})
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	raw, _ := json.Marshal(params.Messages[0])
	if countCacheControl(raw) != 0 {
		t.Errorf("a genuine turn-0 user message (no leading fragment) must not carry a marker: %s", raw)
	}
}

func TestBuildParamsPreviousTurnBoundaryAnchor(t *testing.T) {
	p := cacheProvider()
	tests := []struct {
		name    string
		msgs    []session.Message
		markIdx int // -1 = no marker expected anywhere
	}{
		{
			name: "tool-result tail",
			msgs: []session.Message{
				session.NewUserMessage("read a file"),
				session.NewAssistantMessage("", "", []session.ToolCall{
					session.NewToolCall("call_1", "Read", json.RawMessage(`{"path":"a.go"}`)),
				}),
				session.NewToolMessage(session.NewToolResult("call_1", "package a")),
			},
			markIdx: 0,
		},
		{
			name: "nudge tail",
			msgs: []session.Message{
				session.NewUserMessage("do something"),
				session.NewAssistantMessage("", "", nil), // degenerate empty turn (no-progress nudge)
				session.NewUserMessage("Please continue."),
			},
			markIdx: 0,
		},
		{
			name:    "turn 0 (absent)",
			msgs:    []session.Message{session.NewUserMessage("first prompt")},
			markIdx: -1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params, err := p.buildParams(port.LLMRequest{Model: "claude-sonnet-4-6", Messages: tc.msgs})
			if err != nil {
				t.Fatalf("buildParams: %v", err)
			}
			for i, m := range params.Messages {
				raw, _ := json.Marshal(m)
				got := countCacheControl(raw) != 0
				want := i == tc.markIdx
				if got != want {
					t.Errorf("message[%d] marked = %v, want %v: %s", i, got, want, raw)
				}
			}
		})
	}
}

func TestBuildParamsAnchorDedupe(t *testing.T) {
	p := cacheProvider()
	msgs := []session.Message{
		turn0Fragment("only fragment"),
		session.NewAssistantMessage("", "", []session.ToolCall{
			session.NewToolCall("call_1", "Read", json.RawMessage(`{"path":"a.go"}`)),
		}),
	}
	params, err := p.buildParams(port.LLMRequest{Model: "claude-sonnet-4-6", Messages: msgs})
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	raw, err := json.Marshal(params.Messages)
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}
	if got := countCacheControl(raw); got != 1 {
		t.Fatalf("cache_control markers on messages = %d, want exactly 1 (fragment-end == previous-turn boundary): %s", got, raw)
	}
}

func TestBuildParamsBreakpointBudget(t *testing.T) {
	tests := []struct {
		name string
		msgs []session.Message
	}{
		{
			name: "turn0",
			msgs: []session.Message{
				turn0Fragment("first fragment"),
				turn0Fragment("last fragment"),
				session.NewUserMessage("Please start."),
			},
		},
		{name: "wide fan-out K=32", msgs: wideFanOutConversation(32)},
		{
			name: "post-compaction",
			msgs: []session.Message{
				turn0Fragment("first fragment"),
				turn0Fragment("last fragment"),
				session.NewUserMessage("original genuine instruction, pinned by compaction"),
				session.NewUserMessage("## Compaction summary\nEarlier turns summarised here."),
				session.NewAssistantMessage("Continuing from the summary.", "", nil),
			},
		},
		{
			name: "fork-seeded",
			msgs: []session.Message{
				session.NewUserMessage("forked from parent, no leading fragment"),
				session.NewAssistantMessage("", "", []session.ToolCall{
					session.NewToolCall("call_1", "Read", json.RawMessage(`{"path":"a.go"}`)),
				}),
				session.NewToolMessage(session.NewToolResult("call_1", "package a")),
				session.NewAssistantMessage("Done.", "", nil),
			},
		},
		{
			name: "zero-fragment",
			msgs: []session.Message{
				session.NewUserMessage("no fragments in this session at all"),
				session.NewAssistantMessage("Understood.", "", nil),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := cacheProvider()
			params, err := p.buildParams(port.LLMRequest{
				Model:    "claude-sonnet-4-6",
				System:   prompt.Layered{StablePrefix: "STABLE"},
				Messages: tc.msgs,
			})
			if err != nil {
				t.Fatalf("buildParams: %v", err)
			}
			if n := explicitCacheMarkerCount(t, params); n > 3 {
				t.Errorf("explicit cache_control markers = %d, want <= 3 (the anti-400 budget)", n)
			}
			if params.CacheControl.Type == "" {
				t.Error("top-level automatic marker (slot 4) must be present exactly once")
			}
		})
	}
}

func TestBuildParamsConversationCachingDisabled(t *testing.T) {
	p := cacheProvider(WithConversationCaching(false))
	params, err := p.buildParams(port.LLMRequest{
		Model:    "claude-sonnet-4-6",
		System:   prompt.Layered{StablePrefix: "STABLE", VolatileSuffix: "VOLATILE"},
		Messages: wideFanOutConversation(3),
	})
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	if params.CacheControl.Type != "" {
		t.Error("top-level automatic marker must be absent when conversation caching is disabled")
	}
	msgRaw, err := json.Marshal(params.Messages)
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}
	if countCacheControl(msgRaw) != 0 {
		t.Errorf("no message content block should carry cache_control when disabled: %s", msgRaw)
	}
	// The pre-existing, unconditional StablePrefix breakpoint (slot 1) survives —
	// this option only gates the NEW conversation breakpoints. Byte-identical to
	// the pre-change wire.
	if len(params.System) != 2 {
		t.Fatalf("system blocks = %d, want 2", len(params.System))
	}
	stable, _ := json.Marshal(params.System[0])
	if !strings.Contains(string(stable), `"cache_control":{"type":"ephemeral"}`) {
		t.Errorf("StablePrefix block must keep its pre-existing ephemeral breakpoint: %s", stable)
	}
	volatile, _ := json.Marshal(params.System[1])
	if strings.Contains(string(volatile), "cache_control") {
		t.Errorf("VolatileSuffix block must never carry a cache breakpoint: %s", volatile)
	}
}

func TestBuildParamsMarkerOnUnmarkableBlockIsSkipped(t *testing.T) {
	p := cacheProvider()
	thinkingOnly := session.Message{
		Role: session.RoleAssistant,
		Reasoning: packReasoning([]reasoningBlock{{
			Kind:      reasoningKindThinking,
			Thinking:  "considering the request",
			Signature: "c2ln",
		}}),
	}
	msgs := []session.Message{thinkingOnly, session.NewAssistantMessage("final answer", "", nil)}
	params, err := p.buildParams(port.LLMRequest{Model: "claude-sonnet-4-6", Messages: msgs})
	if err != nil {
		t.Fatalf("buildParams must not error when the anchor target block cannot carry cache_control: %v", err)
	}
	raw, err := json.Marshal(params.Messages[0])
	if err != nil {
		t.Fatalf("marshal messages[0]: %v", err)
	}
	if countCacheControl(raw) != 0 {
		t.Errorf("a thinking-only block has no cache_control field; marking it must be a silent no-op: %s", raw)
	}
}
