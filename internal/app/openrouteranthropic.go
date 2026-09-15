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
// OpenRouter OpenAI base (ADR 0343, following ADR 0334's derivation rules).
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
// from advertising ids its endpoint would reject (ADR 0343 decision 2).
func isOpenRouterAnthropicModel(id string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(id)), openRouterAnthropicSegment)
}

// promptCachedFor reports whether a session on (providerID, modelID) will cache
// its conversation prefix, for the ModelInfo.prompt_cached picker signal
// (ADR 0343 decision 3). It is computed ONCE here and projected, never
// recomputed per sink, the same discipline modelCapability follows.
//
// Two ways to be true:
//
//   - A native Anthropic Messages entry. Anthropic's cache_control is native to
//     that API rather than an extension, which is exactly why
//     newAnthropicEntryFor applies conversation caching on EVERY endpoint,
//     including ones composition cannot classify.
//   - An OpenAI-Responses entry whose ADR 0100 dialect is not None. The
//     canonical OpenAI endpoint caches implicitly, and the canonical OpenRouter
//     endpoint receives a root cache_control breakpoint.
//
// False is the honest answer for every other Responses endpoint — the ToolHive
// gateway's Responses surface, a base-URL override, a custom openai-responses
// definition — because those emit no breakpoint at all, and Anthropic and
// Alibaba are the only OpenRouter upstreams that cache solely on an explicit
// ask. That false is the whole point of the signal: it marks the row whose
// Claude model would silently re-pay full input every turn.
//
// --no-prompt-cache forces false everywhere, matching the wire.
//
// Cache posture is conceptually a (provider, model) property and the ModelInfo
// projection passes both, the same shape as liveMetaStore.outputLimitFor, but
// today only the provider decides it.
// The trailing model parameter is unnamed: the arity matches the ModelInfo
// projection signature, but nothing reads it today.
func promptCachedFor(reg *providerRegistry, cfg Config, providerID string, _ string) bool {
	if cfg.PromptCacheDisabled {
		return false
	}
	entry, ok := reg.Lookup(providerID)
	if !ok {
		return false
	}
	if entry.anthropicProtocol {
		return true
	}
	return cacheDialectFor(providerID, entry.baseURL, cfg) != openai.CacheDialectNone
}

// promptCacheSource labels WHERE a provider's resolved cache posture came from,
// so an unexpected posture reads as a decision rather than a plausible default
// (ADR 0343 decision 7).
func promptCacheSource(reg *providerRegistry, cfg Config, providerID string) string {
	if cfg.PromptCacheDisabled {
		return "forced off by --no-prompt-cache"
	}
	entry, ok := reg.Lookup(providerID)
	if !ok {
		return "unregistered"
	}
	if entry.anthropicProtocol {
		return "native Anthropic Messages (unconditional)"
	}
	switch cacheDialectFor(providerID, entry.baseURL, cfg) {
	case openai.CacheDialectOpenAI:
		return "openai dialect (implicit + cache key)"
	case openai.CacheDialectOpenRouter:
		return "openrouter dialect (root cache_control)"
	default:
		return "no dialect for this endpoint; an Anthropic model here will NOT cache"
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
