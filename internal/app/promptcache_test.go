package app

import (
	"testing"

	"github.com/stacklok/mecatl/provider/openai"
	"github.com/stacklok/mecatl/provider/openaichat"
)

// TestCacheDialectTable pins cacheDialectFor's pure (id, baseURL) gate (ADR
// 0100) — the composition half of the leak guard (the adapter-level
// TestCacheDialectOpenAINeverSendsCacheControl proves the wire is safe once a
// dialect is chosen; this proves the RIGHT dialect is chosen). Covers both
// base-URL-override rows from the ADR's table and a PromptCacheDisabled
// sweep that must force None regardless of (id, baseURL).
func TestCacheDialectTable(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		baseURL string
		want    openai.CacheDialect
	}{
		{"openai canonical", providerOpenAI, "", openai.CacheDialectOpenAI},
		{"openai non-canonical base URL", providerOpenAI, "https://my-vllm.internal/v1", openai.CacheDialectNone},
		{"openrouter default base URL", providerOpenRouter, openRouterDefaultBaseURL, openai.CacheDialectOpenRouter},
		{"openrouter overridden base URL", providerOpenRouter, "https://my-openrouter-proxy.internal/v1", openai.CacheDialectNone},
		{"toolhive any base URL", providerToolhive, "http://127.0.0.1:4000/v1", openai.CacheDialectNone},
		{"unknown id", "some-future-provider", "", openai.CacheDialectNone},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cacheDialectFor(tc.id, tc.baseURL, Config{}); got != tc.want {
				t.Errorf("cacheDialectFor(%q, %q) = %q, want %q", tc.id, tc.baseURL, got, tc.want)
			}
		})
	}

	// PromptCacheDisabled sweep: every row above must degrade to None,
	// regardless of (id, baseURL) — --no-prompt-cache is an unconditional override.
	for _, tc := range tests {
		t.Run(tc.name+"/disabled", func(t *testing.T) {
			if got := cacheDialectFor(tc.id, tc.baseURL, Config{PromptCacheDisabled: true}); got != openai.CacheDialectNone {
				t.Errorf("cacheDialectFor(%q, %q) with PromptCacheDisabled = %q, want None", tc.id, tc.baseURL, got)
			}
		})
	}
}

// TestOpenAIChatCacheDialectTable pins openaichatCacheDialectFor — dormant
// today (no registered Chat-Completions consumer matches providerOpenAI),
// but the gate logic itself is tested directly so a future
// OpenAI-over-Chat-Completions entry is covered from day one.
func TestOpenAIChatCacheDialectTable(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		baseURL string
		want    openaichat.CacheDialect
	}{
		{"hypothetical openai canonical", providerOpenAI, "", openaichat.CacheDialectOpenAI},
		{"hypothetical openai non-canonical", providerOpenAI, "https://my-vllm.internal/v1", openaichat.CacheDialectNone},
		{"opencode (today's only consumer)", providerOpenCode, openCodeDefaultBaseURL, openaichat.CacheDialectNone},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := openaichatCacheDialectFor(tc.id, tc.baseURL, Config{}); got != tc.want {
				t.Errorf("openaichatCacheDialectFor(%q, %q) = %q, want %q", tc.id, tc.baseURL, got, tc.want)
			}
		})
	}
	if got := openaichatCacheDialectFor(providerOpenAI, "", Config{PromptCacheDisabled: true}); got != openaichat.CacheDialectNone {
		t.Errorf("openaichatCacheDialectFor with PromptCacheDisabled = %q, want None", got)
	}
}

// TestAnthropicCacheTTLNormalisation asserts the WARN-through-port.Diagnostics
// contract of normaliseAnthropicCacheTTL: the two accepted values pass
// through silently, "" (unset) passes through silently (not a mistake), and
// any other value normalises to "" WITH a WARN via the injected diagnostics.
func TestAnthropicCacheTTLNormalisation(t *testing.T) {
	tests := []struct {
		name     string
		ttl      string
		want     string
		wantWarn bool
	}{
		{"unset", "", "", false},
		{"5m accepted", "5m", "5m", false},
		{"1h accepted", "1h", "1h", false},
		{"garbage value warns and omits", "30s", "", true},
		{"case-sensitive garbage warns and omits", "1H", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recordingDiag{}
			got := normaliseAnthropicCacheTTL(Config{AnthropicCacheTTL: tc.ttl, Diagnostics: rec})
			if got != tc.want {
				t.Errorf("normaliseAnthropicCacheTTL(%q) = %q, want %q", tc.ttl, got, tc.want)
			}
			gotWarn := rec.has("anthropic-cache-ttl")
			if gotWarn != tc.wantWarn {
				t.Errorf("normaliseAnthropicCacheTTL(%q) warned = %v, want %v (messages: %v)", tc.ttl, gotWarn, tc.wantWarn, rec.messages())
			}
		})
	}
}
