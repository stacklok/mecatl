package openai

import (
	"github.com/openai/openai-go/v3/responses"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// breakpointSupportPrefixes are the model-id prefixes known to accept
// prompt_cache_breakpoint ON THE CANONICAL OPENAI ENDPOINT. OpenAI documents
// explicit breakpoints as GPT-5.6-and-later and says of earlier models only
// "Only implicit caching is supported" — it does NOT document whether an
// earlier model ignores the field or rejects the request.
//
// This is the ONLY place a model id is consulted for breakpoints, and it is
// scoped to one endpoint whose parameter strictness is documented (ADR 0100
// records this repo being bitten by a strict upstream on an unrecognised cache
// field). It is NOT a vendor-family gate: every other endpoint gets the
// breakpoint without anyone asking who made the model. Delete this table if a
// live probe shows an earlier model ignores the field.
var breakpointSupportPrefixes = []string{
	"gpt-5.6",
	"gpt-6",
}

// supportsExplicitBreakpoint reports whether model is known to accept
// prompt_cache_breakpoint. Used ONLY for the canonical OpenAI endpoint; the
// match is on the lower-cased, dated-suffix-stripped id, reusing
// normaliseModelID so a dated snapshot classifies like its bare alias.
func supportsExplicitBreakpoint(model string) bool {
	return hasAnyPrefix(normaliseModelID(model), breakpointSupportPrefixes)
}

// breakpointIndex returns the index of the message that should carry the
// explicit prompt-cache breakpoint, or -1 for none (ADR 0346 decisions 1, 2
// and 4).
//
// Placement is the LAST RoleUser message, and only once the history already
// contains an assistant turn. Two reasons:
//
//   - A breakpoint must sit on an input_text block. Tool results are
//     function_call_output items, where OpenAI accepts the marker but never
//     writes a cache, so they cannot carry it; the newest user message is the
//     latest boundary that can.
//   - Requiring a prior assistant turn means the very first call of a run marks
//     nothing. A cache write that is never read costs MORE than sending the
//     prompt uncached (Anthropic writes bill at 1.25x), and a one-shot run has
//     no second call to read it. From the second call onward the marker is
//     reused by construction.
//
// Top-level instructions are deliberately not considered: the protocol forbids
// a breakpoint there, so the system prefix relies on implicit caching and the
// byte-stable prefix, exactly as before.
//
// The canonical OpenAI endpoint additionally gates on the model
// (supportsExplicitBreakpoint); no other endpoint consults the model at all.
func (p *Provider) breakpointIndex(req port.LLMRequest) int {
	if !p.breakpoints {
		// --no-prompt-cache. NOT keyed on cacheDialect: an endpoint resolving to
		// CacheDialectNone is precisely where the ask is needed.
		return -1
	}
	// The ONE endpoint that gates on the model (ADR 0346 decision 2). The
	// OpenAI dialect is only ever set for the canonical endpoint.
	if p.cacheDialect == CacheDialectOpenAI && !supportsExplicitBreakpoint(req.Model) {
		return -1
	}
	lastUser := -1
	seenAssistant := false
	for i, m := range req.Messages {
		switch m.Role {
		case session.RoleUser:
			lastUser = i
		case session.RoleAssistant:
			seenAssistant = true
		}
	}
	if !seenAssistant {
		return -1
	}
	return lastUser
}

// markPromptCacheBreakpoint stamps the explicit breakpoint on the LAST
// input_text block of content, in place.
//
// Fail-soft, mirroring the anthropic adapter's setCacheControl: a content list
// with no input_text block (an image-only user message) is silently left
// unmarked rather than erroring. Losing a cache breakpoint is a cost, not a
// correctness failure, and refusing the request would be worse.
func markPromptCacheBreakpoint(content responses.ResponseInputMessageContentListParam) {
	for i := len(content) - 1; i >= 0; i-- {
		if content[i].OfInputText == nil {
			continue
		}
		// Mode must be set EXPLICITLY. The field is `omitzero` and the param
		// struct holds only a constant, so a zero-value literal marshals to
		// nothing at all — the marker would silently vanish. The SDK's own
		// call sites set Mode the same way.
		content[i].OfInputText.PromptCacheBreakpoint = responses.ResponseInputTextPromptCacheBreakpointParam{Mode: "explicit"}
		return
	}
}
