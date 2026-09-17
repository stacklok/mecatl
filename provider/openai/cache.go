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
// only controls the vendor-shaped hints layered on top: prompt_cache_key
// (routing observability) and prompt_cache_retention (an OpenAI-only,
// model-gated retention extension).
//
// It does NOT control the ASK for a cache. ADR 0346 retired the request-root
// cache_control field this type once carried for OpenRouter and replaced it
// with the protocol-native prompt_cache_breakpoint, which rides the separate
// WithPromptCacheBreakpoints Option precisely so an endpoint whose identity
// the dialect cannot classify still gets the ask.
type CacheDialect string

const (
	// CacheDialectNone emits no cache hints at all — the SAFE DEFAULT, and the
	// byte-identical pre-ADR-0100 wire. The zero value.
	CacheDialectNone CacheDialect = ""
	// CacheDialectOpenAI emits prompt_cache_key, and — on an allow-listed
	// model (retentionFor) — prompt_cache_retention.
	CacheDialectOpenAI CacheDialect = "openai"
	// CacheDialectOpenRouter emits prompt_cache_key and nothing else. It once
	// also emitted an OpenRouter-private request-root cache_control field;
	// ADR 0346 retired that in favour of the protocol-native breakpoint, so the
	// two OpenAI-shaped dialects now differ ONLY in retention. It must still
	// NEVER emit prompt_cache_retention — an OpenAI-only field a non-OpenAI
	// upstream would reject.
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

// WithCacheKeySalt folds a per-process random value into the prompt_cache_key
// prefix hash (ADR 0346). Composition mints one value per app.Build and passes
// it to every openai-adapter entry.
//
// Why: the ADR 0100 derivation has no installation-specific input, so two
// unrelated installations sharing harness version, soul, agent def and tool
// inventory emit an IDENTICAL key. That lets a recipient correlate sessions
// across principals and credentials, and confirm guessed configuration — and
// routing metadata frequently outlives prompt bodies in a provider's retention
// tiers, so the key outlives the content it fingerprints.
//
// "" (the Option unset) reproduces ADR 0100's derivation byte-for-byte, so a
// consumer passing no Option is unaffected. The salt is never sent as itself,
// never logged, and never persisted: a fresh value per process costs one
// sticky-routing lane change per restart, which the derivation already treats
// as fail-soft for an anchor change.
func WithCacheKeySalt(salt string) Option {
	return func(c *config) { c.cacheKeySalt = salt }
}

// WithPromptCacheBreakpoints arms the protocol-native explicit prompt-cache
// breakpoint (ADR 0346). Composition passes !cfg.PromptCacheDisabled.
//
// It is a SEPARATE Option from WithCacheDialect on purpose. The dialect gates
// vendor-shaped hints on endpoint identity; the breakpoint is part of the
// Responses protocol and is exactly what an unrecognised endpoint needs, so
// tying it to the dialect would leave CacheDialectNone endpoints — the ToolHive
// gateway among them — with no way to ask for a cache, which is the bug this
// ADR exists to fix.
//
// Default false, so a consumer passing no Option keeps the pre-ADR-0346 wire.
func WithPromptCacheBreakpoints(on bool) Option {
	return func(c *config) { c.breakpoints = on }
}

// explicitCacheModelPrefixes are the canonical-OpenAI model-id prefixes at or
// after the GPT-5.6 cache-semantics cutover, where TWO things changed together:
// prompt_cache_breakpoint became supported, AND prompt_cache_retention became
// deprecated (implicit caching already covers it, and pre-5.6 400s can occur on
// newer knobs sent to it).
//
// ONE list, because it is ONE documented event read from two sides —
// retentionFor DENIES on it and supportsExplicitBreakpoint ALLOWS on it. ADR
// 0346 decision 2 says this table is reused, and a second literal holding the
// same two values is exactly how the two sides silently diverge on the next
// model release.
//
// It is checked BEFORE any retention allow prefix so a newer, narrower id
// always wins the classification — "gpt-5" is a prefix of "gpt-5.6".
var explicitCacheModelPrefixes = []string{
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
	if hasAnyPrefix(m, explicitCacheModelPrefixes) {
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
