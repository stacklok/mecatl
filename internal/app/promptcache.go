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
// "" (anthropic.WithCacheTTL's own omit-on-unrecognised behaviour) but WARNs,
// since the operator DID supply something and it was not honoured. Call this
// ONCE per registry build (a build-time, not a per-remint, call site — see
// newAnthropicEntry) so the WARN fires at most once per process.
func normaliseAnthropicCacheTTL(cfg Config) string {
	switch cfg.AnthropicCacheTTL {
	case "", "5m", "1h":
		return cfg.AnthropicCacheTTL
	default:
		cfg.diag().Log(context.Background(), port.LevelWarn,
			"anthropic-cache-ttl: ignoring unrecognised value; caching stays enabled with the API's own default TTL",
			"value", cfg.AnthropicCacheTTL)
		return ""
	}
}
