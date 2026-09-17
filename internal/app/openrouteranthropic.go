package app

import (
	"strings"

	"github.com/stacklok/mecatl/provider/openai"
	"github.com/stacklok/mecatl/provider/openaichat"
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
//
// It is deriveGatewayBaseURL with an EMPTY segment, which is exactly what "the
// SDK appends the version itself" means: the ToolHive sibling passes "anthropic"
// because that gateway exposes the surface at a sub-path, and OpenRouter passes
// nothing because its Anthropic surface IS the /api root. Sharing the one
// derivation is what keeps the sanitisation rules from drifting between two
// call sites that must obey the same ADR 0334 contract.
func openRouterAnthropicBaseURL(openAIBaseURL string) string {
	return deriveGatewayBaseURL(openAIBaseURL, "", true)
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
// It answers PER PROTOCOL, because the three protocols ask for a cache in three
// different ways and one of them cannot ask at all:
//
//   - Anthropic Messages — true. cache_control is native to the protocol and the
//     adapter emits it on every endpoint.
//   - OpenAI Responses — true. ADR 0346 arms the protocol-native
//     prompt_cache_breakpoint on every endpoint, dialect or no dialect. The
//     canonical OpenAI endpoint gates the marker on the model, but implicit
//     caching covers the pre-breakpoint models there anyway, so the answer is
//     still yes.
//   - OpenAI Chat Completions — true ONLY under the canonical-OpenAI dialect,
//     where the endpoint caches IMPLICITLY with nothing sent. openaichat has no
//     breakpoint mechanism, so for every other Chat Completions endpoint —
//     opencode, and every custom api_flavor: openai-chat-completions definition
//     today — mecatl sends no cache ask whatsoever and this is honestly FALSE.
//
// --no-prompt-cache forces false regardless, and an unregistered provider is
// false because nothing will run there.
//
// The signal no longer marks the ADR-0100-era "this endpoint emits nothing"
// case for Responses, because ADR 0346 abolished it there. It reports only
// whether mecatl asks for caching at all. Whether a given upstream HONOURS the
// ask is not statically knowable, and this must not pretend otherwise.
func promptCachedFor(reg *providerRegistry, cfg Config, providerID string, _ string) bool {
	if cfg.PromptCacheDisabled {
		return false
	}
	entry, ok := reg.Lookup(providerID)
	if !ok {
		return false
	}
	if entry.protocol == protocolOpenAIChatCompletions {
		return openaichatCacheDialectFor(providerID, entry.baseURL, cfg) == openaichat.CacheDialectOpenAI
	}
	return true
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
	switch entry.protocol {
	case protocolAnthropicMessages:
		return "native Anthropic Messages (breakpoints + ttl)"
	case protocolOpenAIChatCompletions:
		// Never "responses breakpoint": this entry does not speak Responses, and
		// openaichat has no breakpoint to send. Under the canonical-OpenAI dialect
		// the endpoint caches implicitly with nothing on the wire; anywhere else
		// mecatl asks for nothing, and the operator needs to read that plainly.
		if openaichatCacheDialectFor(providerID, entry.baseURL, cfg) == openaichat.CacheDialectOpenAI {
			return "chat completions implicit + cache key (no breakpoint in this protocol)"
		}
		return "none: chat completions has no cache breakpoint and this endpoint has no dialect"
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
