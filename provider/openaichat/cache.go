package openaichat

// CacheDialect selects which provider-side prompt-cache wire dialect (ADR
// 0100) buildParams (method) emits. Unlike the openai (Responses) adapter,
// Chat Completions has no OpenRouter-style request-root cache_control
// extension and this adapter does not gate a retention knob — so there are
// only two dialects: None and OpenAI (prompt_cache_key alone).
type CacheDialect string

const (
	// CacheDialectNone emits no cache hints at all — the SAFE DEFAULT, and the
	// byte-identical pre-ADR-0100 wire. The zero value.
	CacheDialectNone CacheDialect = ""
	// CacheDialectOpenAI emits prompt_cache_key.
	CacheDialectOpenAI CacheDialect = "openai"
)

// WithCacheDialect selects the cache-hint dialect buildParams (method) emits
// (ADR 0100). Composition gates this on (providerID, resolvedBaseURL) — the
// only registered Chat-Completions-protocol consumer today (OpenCode Go) is
// not the canonical OpenAI endpoint, so this ships fully tested but changes
// no runtime behaviour until a future OpenAI-over-Chat-Completions entry
// exists. An unrecognised token degrades to CacheDialectNone in buildParams —
// fail-soft, mirroring reasoningEffortFor's omit-on-unknown arm.
func WithCacheDialect(d CacheDialect) Option {
	return func(c *config) { c.cacheDialect = d }
}
