package app

import (
	"net/url"
	"strings"

	"github.com/stacklok/mecatl/provider/openai"
)

// openRouterAnthropicSegment is the OpenRouter model-id namespace whose models
// execute on the Anthropic Messages surface. OpenRouter namespaces every id as
// "<vendor>/<model>", so the vendor segment is the family test.
const openRouterAnthropicSegment = "anthropic/"

// openRouterAnthropicBaseURL derives the Anthropic Messages base from the
// OpenRouter OpenAI base (ADR 0346, following ADR 0334's derivation rules).
//
// OpenRouter documents its Anthropic-protocol base as https://openrouter.ai/api
// — note the absent /v1 — because the Anthropic SDK appends /v1/messages and
// /v1/models itself. So the derivation strips exactly one terminal "v1" path
// segment and appends nothing; the ToolHive sibling appends an "anthropic"
// segment instead because that gateway exposes the surface at a sub-path.
//
// Built with net/url, never string concatenation, and userinfo, query and
// fragment are dropped so none of them can ride into a request URL or a
// diagnostic (ADR 0334). A base that does not end in /v1 is returned sanitised
// but otherwise untouched: an operator who overrode --openrouter-base-url to a
// non-standard shape gets their path preserved rather than silently rewritten.
// "" in, "" out, and "" out on any parse failure — the caller treats that as
// "no Anthropic surface for this endpoint" and registers nothing.
func openRouterAnthropicBaseURL(openAIBaseURL string) string {
	if openAIBaseURL == "" {
		return ""
	}
	u, err := url.Parse(openAIBaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	u.RawPath = ""
	clean := strings.TrimRight(u.Path, "/")
	switch {
	case clean == "/v1":
		u.Path = ""
	case strings.HasSuffix(clean, "/v1"):
		u.Path = strings.TrimSuffix(clean, "/v1")
	default:
		u.Path = clean
	}
	return u.String()
}

// isOpenRouterAnthropicModel reports whether an OpenRouter model id executes on
// the Anthropic Messages surface. OpenRouter's Anthropic endpoint does not serve
// non-Anthropic models, so this is what keeps the openrouter-anthropic entry
// from advertising ids its endpoint would reject (ADR 0346 decision 2).
func isOpenRouterAnthropicModel(id string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(id)), openRouterAnthropicSegment)
}

// promptCachedFor reports whether a session on (providerID, modelID) will have
// its conversation prefix cached, for the ModelInfo.prompt_cached picker signal.
//
// After ADR 0346 the honest answer is "yes, unless caching is switched off",
// because the protocol-native breakpoint is armed on every Responses endpoint
// and Messages entries cache unconditionally. The three ways to be true:
//
//   - --no-prompt-cache is not set, AND
//   - a native Anthropic Messages entry (unconditional), or
//   - an OpenAI-Responses entry, which now always carries a breakpoint — except
//     the canonical OpenAI endpoint on a model that predates explicit
//     breakpoints, where implicit caching covers it anyway.
//
// So the signal no longer marks the ADR-0100-era "this endpoint emits nothing"
// case, because that case no longer exists. It now reports only whether mecatl
// asks for caching at all. Whether a given upstream HONOURS the ask is not
// statically knowable, and this must not pretend otherwise.
func promptCachedFor(reg *providerRegistry, cfg Config, providerID string, _ string) bool {
	if cfg.PromptCacheDisabled {
		return false
	}
	_, ok := reg.Lookup(providerID)
	return ok
}

// promptCacheSource labels WHERE a provider's resolved cache posture came from,
// so an unexpected posture reads as a decision rather than a plausible default
// (ADR 0346 decision 7).
func promptCacheSource(reg *providerRegistry, cfg Config, providerID string) string {
	if cfg.PromptCacheDisabled {
		return "forced off by --no-prompt-cache"
	}
	entry, ok := reg.Lookup(providerID)
	if !ok {
		return "unregistered"
	}
	if entry.anthropicProtocol {
		return "native Anthropic Messages (breakpoints + ttl)"
	}
	switch cacheDialectFor(providerID, entry.baseURL, cfg) {
	case openai.CacheDialectOpenAI:
		return "responses breakpoint (gpt-5.6+) + implicit + cache key"
	case openai.CacheDialectOpenRouter:
		return "responses breakpoint + cache key"
	default:
		return "responses breakpoint (no dialect hints for this endpoint)"
	}
}

// promptCachePostureLine composes the build-once prompt-cache posture line as a
// PURE helper, so it is table-testable directly (the guardrailsPostureLine
// idiom). It names every registered provider and its resolved source in sorted
// order, because ADR 0100's failure mode was SILENCE: a provider emitting no
// breakpoint for a Claude model looked exactly like a provider that was fine,
// for as long as it took to exhaust a budget.
//
// It reports the SOURCE, not just a boolean, because an operator needs to tell
// "this endpoint has no dialect" apart from "caching is switched off".
func promptCachePostureLine(reg *providerRegistry, cfg Config) string {
	if reg == nil {
		return ""
	}
	ids := reg.Available()
	if len(ids) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, id+"="+promptCacheSource(reg, cfg, id))
	}
	return "prompt cache: " + strings.Join(parts, "; ")
}
