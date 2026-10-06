package app

import (
	"context"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/provider/openai"
	"github.com/stacklok/mecatl/provider/openaichat"
)

// cacheDialectFor is the PURE (id, baseURL) -> openai.CacheDialect gate (ADR
// 0100) for the openai (Responses) adapter, shared by the openai, openrouter,
// and toolhive-gateway entries (all three ride newOpenAICompatEntry). It is
// NEVER keyed on providerID alone: an operator can point the "openai" id at a
// non-canonical OpenAI-compatible endpoint (vLLM/LiteLLM) via
// --openai-base-url, and sending prompt_cache_retention or an unrecognised
// field to a strict-compatible upstream 400s — the SAME class of trap
// documented at the emptyToolOutputPlaceholder rationale
// (provider/openai/request.go). --no-prompt-cache (cfg.PromptCacheDisabled)
// forces CacheDialectNone on every row, unconditionally.
//
//	id            baseURL                    dialect
//	openai        canonical (empty)          OpenAI
//	openrouter    openRouterDefaultBaseURL   OpenRouter
//	toolhive      any                        None
//	anything else, or any non-canonical baseURL for openai/openrouter: None
func cacheDialectFor(id, baseURL string, cfg Config) openai.CacheDialect {
	if cfg.PromptCacheDisabled {
		return openai.CacheDialectNone
	}
	switch id {
	case providerOpenAI:
		if baseURL == "" {
			return openai.CacheDialectOpenAI
		}
	case providerOpenRouter:
		if baseURL == openRouterDefaultBaseURL {
			return openai.CacheDialectOpenRouter
		}
	}
	return openai.CacheDialectNone
}

// openaichatCacheDialectFor mirrors cacheDialectFor's (id, baseURL) gate for
// the openaichat (Chat Completions) adapter — a SEPARATE Go module with its
// own CacheDialect type (only None/OpenAI; there is no Chat-Completions-over-
// OpenRouter path), so the mapping is duplicated rather than shared, the same
// discipline as isContextOverflowMessage. Today's only registered
// Chat-Completions-protocol consumer is providerOpenCode
// (https://opencode.ai/zen/go/v1), which never matches providerOpenAI, so
// this always resolves to CacheDialectNone in production — it earns its keep
// when a future OpenAI-over-Chat-Completions entry appears.
func openaichatCacheDialectFor(id, baseURL string, cfg Config) openaichat.CacheDialect {
	if cfg.PromptCacheDisabled {
		return openaichat.CacheDialectNone
	}
	if id == providerOpenAI && baseURL == "" {
		return openaichat.CacheDialectOpenAI
	}
	return openaichat.CacheDialectNone
}

// normaliseAnthropicCacheTTL validates cfg.AnthropicCacheTTL
// (--anthropic-cache-ttl, ADR 0100) against the two values Anthropic's
// ephemeral cache_control TTL accepts: "5m" (the API's own default) and "1h".
// "" (the flag's zero value — it is optional) normalises silently to "" (no
// WARN: an unset flag is not a mistake). Any OTHER value also normalises to
// "" but WARNs, since the operator DID supply something and it was not
// honoured. "" means "no operator choice": anthropicCacheTTLFor then applies
// the per-endpoint default. Call this ONCE per registry build (a build-time,
// not a per-remint, call site — see newAnthropicEntryFor) so the WARN fires at
// most once per process.
func normaliseAnthropicCacheTTL(cfg Config) string {
	switch cfg.AnthropicCacheTTL {
	case "", "5m", "1h":
		return cfg.AnthropicCacheTTL
	default:
		cfg.diag().Log(context.Background(), port.LevelWarn,
			"anthropic-cache-ttl: ignoring unrecognised value; caching stays enabled with the default TTL",
			"value", cfg.AnthropicCacheTTL)
		return ""
	}
}

// anthropicCacheTTLFor resolves the TTL an Anthropic Messages entry stamps on
// its cache_control markers. An operator choice (the normalised
// --anthropic-cache-ttl) applies to every endpoint unchanged. Without one, the
// built-in Anthropic Messages providers — anthropic, openrouter-anthropic and
// toolhive-anthropic — share ONE rule: they default to the longest lifetime,
// "1h"; operators restore the shorter, cheaper-to-write lifetime with
// --anthropic-cache-ttl=5m. The rule is keyed on the built-in id alone so the
// three cannot diverge (a base-URL override does not change it). Custom
// anthropic-messages definitions are unknown endpoints that may reject ttl, so
// they keep the API default (""). --no-prompt-cache also keeps "" so it still
// reproduces the pre-ADR-0100 wire byte-for-byte.
func anthropicCacheTTLFor(id, operatorTTL string, cfg Config) string {
	if operatorTTL != "" {
		return operatorTTL
	}
	if cfg.PromptCacheDisabled {
		return ""
	}
	switch id {
	case providerAnthropic, providerOpenRouterAnthropic, providerToolhiveAnthropic:
		return "1h"
	}
	return ""
}
