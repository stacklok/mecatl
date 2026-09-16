package openai

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// multiTurnReq is cacheReq advanced to a second turn: the history already holds
// an assistant turn, which is the precondition for a breakpoint (a first call
// has no later call to read what it would write).
func multiTurnReq(model string) port.LLMRequest {
	req := cacheReq(model)
	req.Messages = append(req.Messages, session.NewUserMessage("now fix the bug"))
	return req
}

const breakpointJSON = `"prompt_cache_breakpoint":{"mode":"explicit"}`

// TestADR_0346_BreakpointEmittedVendorAgnostic pins AC1.1: the marker goes out
// on every Responses request, whatever the model's vendor. This is the whole
// point of ADR 0346 — a name-keyed rule breaks when a second vendor ships
// explicit-ask caching, and one staging gateway already exposes a single model
// under four different ids.
func TestADR_0346_BreakpointEmittedVendorAgnostic(t *testing.T) {
	// Deliberately spans vendors, namespacing conventions and a made-up vendor.
	for _, model := range []string{
		"anthropic/claude-opus-4.8",
		"claude-opus-4-8",
		"us.anthropic.claude-opus-4-8",
		"google/gemini-2.5-pro",
		"qwen/qwen3-max",
		"unthropic/unthropic-max-1", // the future vendor nobody has a table for
		"",
	} {
		t.Run(model, func(t *testing.T) {
			raw := marshalParams(t, New(WithCacheDialect(CacheDialectOpenRouter), WithPromptCacheBreakpoints(true)), multiTurnReq(model))
			if !strings.Contains(raw, breakpointJSON) {
				t.Errorf("no breakpoint emitted for model %q: %s", model, raw)
			}
			if got := strings.Count(raw, breakpointJSON); got != 1 {
				t.Errorf("breakpoint count = %d, want exactly 1: %s", got, raw)
			}
		})
	}
}

// TestADR_0346_BreakpointAtPreviousTurnBoundary pins AC1.2: the marker sits on
// the LAST user message, so turn N writes what turn N+1 reads, and never on
// top-level instructions, which the protocol forbids.
func TestADR_0346_BreakpointAtPreviousTurnBoundary(t *testing.T) {
	req := multiTurnReq("anthropic/claude-opus-4.8")
	p := New(WithCacheDialect(CacheDialectOpenRouter), WithPromptCacheBreakpoints(true))

	if got, want := p.breakpointIndex(req), len(req.Messages)-1; got != want {
		t.Errorf("breakpointIndex = %d, want the last user message at %d", got, want)
	}
	raw := marshalParams(t, p, req)
	// The marked block must be the newest user text, not an earlier one.
	marked := strings.Index(raw, breakpointJSON)
	newest := strings.Index(raw, "now fix the bug")
	earlier := strings.Index(raw, "Please summarise the architecture")
	if marked < newest {
		t.Errorf("breakpoint appears before the newest user text; wrong block marked: %s", raw)
	}
	if earlier > newest {
		t.Fatalf("fixture ordering changed; test needs updating: %s", raw)
	}
	// instructions carries the system prefix and must never be marked.
	instrEnd := strings.Index(raw, `"prompt_cache_key"`)
	if instrEnd > 0 && strings.Contains(raw[:instrEnd], "prompt_cache_breakpoint") {
		t.Errorf("breakpoint must never ride top-level instructions: %s", raw[:instrEnd])
	}
}

// TestADR_0346_NoBreakpointWithoutAPreviousTurn pins AC1.3: a first call marks
// nothing. A cache write that is never read costs MORE than sending the prompt
// uncached, and a one-shot run has no second call to read it.
func TestADR_0346_NoBreakpointWithoutAPreviousTurn(t *testing.T) {
	req := cacheReq("anthropic/claude-opus-4.8")
	// cacheReq's fixture ends on an assistant message; strip it to model a
	// genuine first call (fragments + prompt, nothing answered yet).
	var firstCall []session.Message
	for _, m := range req.Messages {
		if m.Role == session.RoleAssistant {
			continue
		}
		firstCall = append(firstCall, m)
	}
	req.Messages = firstCall

	p := New(WithCacheDialect(CacheDialectOpenRouter), WithPromptCacheBreakpoints(true))
	if got := p.breakpointIndex(req); got != -1 {
		t.Errorf("breakpointIndex = %d on a first call, want -1", got)
	}
	if raw := marshalParams(t, p, req); strings.Contains(raw, "prompt_cache_breakpoint") {
		t.Errorf("a first call must mark nothing: %s", raw)
	}

	// An empty history likewise marks nothing rather than panicking.
	empty := req
	empty.Messages = nil
	if got := p.breakpointIndex(empty); got != -1 {
		t.Errorf("breakpointIndex = %d on an empty history, want -1", got)
	}
}

// TestADR_0346_RootCacheControlRetired pins AC1.4 across every dialect.
func TestADR_0346_RootCacheControlRetired(t *testing.T) {
	for _, d := range []CacheDialect{CacheDialectNone, CacheDialectOpenAI, CacheDialectOpenRouter} {
		raw := marshalParams(t, New(WithCacheDialect(d), WithPromptCacheBreakpoints(true)), multiTurnReq("gpt-5.6"))
		if strings.Contains(raw, "cache_control") {
			t.Errorf("dialect %q still emits root cache_control: %s", d, raw)
		}
	}
}

// TestADR_0346_CanonicalOpenAICarveOut pins AC1.5. This is the ONLY place a
// model id gates a breakpoint, and it is scoped to the one endpoint whose
// parameter strictness is documented: OpenAI states explicit breakpoints are
// GPT-5.6-and-later and does NOT say whether an earlier model ignores or
// rejects the field.
func TestADR_0346_CanonicalOpenAICarveOut(t *testing.T) {
	supported := []string{"gpt-5.6", "gpt-5.6-2026-01-01", "gpt-6"}
	unsupported := []string{"gpt-5", "gpt-5.2", "gpt-4.1", "gpt-5.5"}

	for _, model := range supported {
		if raw := marshalParams(t, New(WithCacheDialect(CacheDialectOpenAI), WithPromptCacheBreakpoints(true)), multiTurnReq(model)); !strings.Contains(raw, breakpointJSON) {
			t.Errorf("canonical OpenAI %q supports breakpoints but none emitted: %s", model, raw)
		}
	}
	for _, model := range unsupported {
		if raw := marshalParams(t, New(WithCacheDialect(CacheDialectOpenAI), WithPromptCacheBreakpoints(true)), multiTurnReq(model)); strings.Contains(raw, "prompt_cache_breakpoint") {
			t.Errorf("canonical OpenAI %q predates explicit breakpoints; field must be omitted: %s", model, raw)
		}
	}
	// The carve-out is endpoint-scoped: the SAME unsupported ids still get a
	// breakpoint on any other endpoint, because no other endpoint consults the
	// model at all.
	for _, model := range unsupported {
		if raw := marshalParams(t, New(WithCacheDialect(CacheDialectOpenRouter), WithPromptCacheBreakpoints(true)), multiTurnReq(model)); !strings.Contains(raw, breakpointJSON) {
			t.Errorf("model gating leaked off the canonical OpenAI endpoint for %q: %s", model, raw)
		}
	}
}

// TestADR_0346_NoPromptCacheSuppressesBreakpoint pins AC1.6: --no-prompt-cache
// resolves every dialect to None in composition, which must also drop the
// breakpoint, reproducing the pre-ADR-0100 wire.
func TestADR_0346_NoPromptCacheSuppressesBreakpoint(t *testing.T) {
	raw := marshalParams(t, New(WithCacheDialect(CacheDialectNone)), multiTurnReq("anthropic/claude-opus-4.8"))
	for _, key := range []string{"prompt_cache_breakpoint", "prompt_cache_key", "prompt_cache_retention", "cache_control"} {
		if strings.Contains(raw, key) {
			t.Errorf("CacheDialectNone must emit no cache hint, found %q: %s", key, raw)
		}
	}
	// And the marked message must fall back to the bare-string fast path, so the
	// wire is byte-identical to the pre-caching form.
	if strings.Contains(raw, `"type":"input_text"`) {
		t.Errorf("with no breakpoint the text-only fast path must be kept: %s", raw)
	}
}

// TestADR_0346_BreakpointMarksMultimodalTextBlock covers the multimodal path and
// the fail-soft floor: an image-only user message has no input_text block to
// mark, and losing a breakpoint is a cost, never a reason to fail the request.
func TestADR_0346_BreakpointMarksMultimodalTextBlock(t *testing.T) {
	req := multiTurnReq("anthropic/claude-opus-4.8")
	last := len(req.Messages) - 1
	req.Messages[last] = session.Message{
		Role:  session.RoleUser,
		Text:  "look at this",
		Parts: []session.Content{{Kind: session.MediaImage, URL: "https://example.test/x.png"}},
	}
	raw := marshalParams(t, New(WithCacheDialect(CacheDialectOpenRouter), WithPromptCacheBreakpoints(true)), req)
	if !strings.Contains(raw, breakpointJSON) {
		t.Errorf("a multimodal message with text must still carry the marker: %s", raw)
	}

	// Image-only: no text block exists, so nothing is marked and no error.
	req.Messages[last] = session.Message{
		Role:  session.RoleUser,
		Parts: []session.Content{{Kind: session.MediaImage, URL: "https://example.test/x.png"}},
	}
	raw = marshalParams(t, New(WithCacheDialect(CacheDialectOpenRouter), WithPromptCacheBreakpoints(true)), req)
	if strings.Contains(raw, "prompt_cache_breakpoint") {
		t.Errorf("an image-only message has no text block to mark: %s", raw)
	}
}

// TestADR_0346_BreakpointNotGatedOnDialect is the regression guard for the exact
// mistake this implementation made once: gating the breakpoint on cacheDialect.
//
// Composition resolves an unrecognised endpoint — the ToolHive gateway, a
// base-URL override, a custom openai-responses definition — to
// CacheDialectNone. That is PRECISELY where an explicit-ask upstream has no
// other way to ask, and it is the shape of the reported incident. A breakpoint
// gated on the dialect would look correct in every dialect-bearing test and
// deliver nothing to the one case that matters.
func TestADR_0346_BreakpointNotGatedOnDialect(t *testing.T) {
	req := multiTurnReq("anthropic/claude-opus-4.8")
	p := New(WithCacheDialect(CacheDialectNone), WithPromptCacheBreakpoints(true))
	raw := marshalParams(t, p, req)

	if !strings.Contains(raw, breakpointJSON) {
		t.Fatalf("CacheDialectNone must STILL carry a breakpoint; that endpoint is the whole point: %s", raw)
	}
	// And it carries no vendor-shaped hint, so the dialect still means what it meant.
	for _, key := range []string{"prompt_cache_key", "prompt_cache_retention", "cache_control"} {
		if strings.Contains(raw, key) {
			t.Errorf("CacheDialectNone must emit no dialect hint, found %q: %s", key, raw)
		}
	}
}

// TestADR_0346_BreakpointOffByDefaultForLibraryConsumers pins the module's
// compatibility story: a consumer that passes no Option keeps the pre-ADR-0346
// wire, so the Added/Changed classification in the plan holds.
func TestADR_0346_BreakpointOffByDefaultForLibraryConsumers(t *testing.T) {
	raw := marshalParams(t, New(WithCacheDialect(CacheDialectOpenRouter)), multiTurnReq("anthropic/claude-opus-4.8"))
	if strings.Contains(raw, "prompt_cache_breakpoint") {
		t.Errorf("no Option passed: the breakpoint must be off by default: %s", raw)
	}
}
