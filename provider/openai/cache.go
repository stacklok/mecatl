package openai

import (
	"regexp"
	"strings"

	"github.com/openai/openai-go/v3/responses"
)

// CacheDialect selects which provider-side prompt-cache wire dialect (ADR
// 0100) buildParams (method) emits. The OpenAI Responses API already caches
// implicitly (a breakpoint on the most recent user/tool message, matched
// against the 50 most recent breakpoints, longest match wins) — this Option
// only controls the hints layered on top: prompt_cache_key (routing
// observability), prompt_cache_retention (an OpenAI-only, model-gated
// retention extension), and OpenRouter's request-root cache_control field
// (an OpenRouter-only extension; sending it to real OpenAI 400s).
type CacheDialect string

const (
	// CacheDialectNone emits no cache hints at all — the SAFE DEFAULT, and the
	// byte-identical pre-ADR-0100 wire. The zero value.
	CacheDialectNone CacheDialect = ""
	// CacheDialectOpenAI emits prompt_cache_key, and — on an allow-listed
	// model (retentionFor) — prompt_cache_retention.
	CacheDialectOpenAI CacheDialect = "openai"
	// CacheDialectOpenRouter emits prompt_cache_key and the OpenRouter-only
	// request-root cache_control field. NEVER prompt_cache_retention — an
	// OpenAI-only field a non-OpenAI upstream would reject.
	CacheDialectOpenRouter CacheDialect = "openrouter"
)

// WithCacheDialect selects the cache-hint dialect buildParams (method) emits
// (ADR 0100). Composition gates this on (providerID, resolvedBaseURL) — NEVER
// providerID alone: an operator can point the "openai" id at a non-canonical
// compatible endpoint (vLLM/LiteLLM via --openai-base-url) that would 400 on
// prompt_cache_retention or an unrecognised field, so the dialect for a
// non-canonical base URL must degrade to CacheDialectNone in composition, not
// here. An unrecognised token (this adapter has no validation at Option-
// application time) degrades to CacheDialectNone in buildParams — fail-soft,
// mirroring reasoningEffortFor's omit-on-unknown arm.
func WithCacheDialect(d CacheDialect) Option {
	return func(c *config) { c.cacheDialect = d }
}

// retentionDenyPrefixes are checked BEFORE any allow prefix so a newer,
// narrower id always wins the classification — "gpt-5" is a prefix of
// "gpt-5.6", where prompt_cache_retention is DEPRECATED (implicit caching
// already covers it, and pre-5.6 400s can occur on newer knobs sent to it).
var retentionDenyPrefixes = []string{
	"gpt-5.6",
	"gpt-6",
}

// retentionAllowPrefixes are the model-id prefixes known to accept
// prompt_cache_retention. Ordered longest-prefix-first for readability
// (mirrors the anthropic adapter's adaptiveThinkingPrefixes idiom) — this is
// a WIRE-PROTOCOL fact, not a catalog fact, so it lives here rather than in
// composition.
var retentionAllowPrefixes = []string{
	"gpt-5.5",
	"gpt-5.4",
	"gpt-5.2",
	"gpt-5.1",
	"gpt-5",
	"gpt-4.1",
}

// dateSnapshotSuffix matches a trailing "-YYYY-MM-DD" dated-snapshot suffix
// (e.g. "-2026-01-01") so a dated model id classifies identically to its bare
// alias.
var dateSnapshotSuffix = regexp.MustCompile(`-\d{4}-\d{2}-\d{2}$`)

// retentionFor reports the prompt_cache_retention value for model and
// whether the model is known to accept it at all. ok=false means OMIT the
// field — a value is NEVER guessed, so an unknown model can never 400 on it.
// The match is on the lower-cased, dated-suffix-stripped model id; it does
// NOT strip an "<org>/" routing prefix (an OpenRouter-style id such as
// "openai/gpt-5" reaching here — under CacheDialectOpenAI, the canonical
// OpenAI endpoint — is itself anomalous, so it classifies as unknown rather
// than guessed).
func retentionFor(model string) (responses.ResponseNewParamsPromptCacheRetention, bool) {
	m := normaliseModelID(model)
	if hasAnyPrefix(m, retentionDenyPrefixes) {
		return "", false
	}
	if hasAnyPrefix(m, retentionAllowPrefixes) {
		return responses.ResponseNewParamsPromptCacheRetention24h, true
	}
	return "", false
}

// normaliseModelID lower-cases, trims, and strips a trailing dated-snapshot
// suffix from a model id for prefix classification.
func normaliseModelID(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	return dateSnapshotSuffix.ReplaceAllString(m, "")
}

// hasAnyPrefix reports whether m starts with any of prefixes.
func hasAnyPrefix(m string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(m, prefix) {
			return true
		}
	}
	return false
}
