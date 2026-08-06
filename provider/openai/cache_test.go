package openai

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
)

// cacheReq builds a small but non-trivial conversation (a leading turn-0
// fragment, a genuine user prompt, and an assistant reply) for the cache
// dialect test suite.
func cacheReq(model string) port.LLMRequest {
	return port.LLMRequest{
		Model:  model,
		System: prompt.Layered{StablePrefix: "You are a coding agent.", VolatileSuffix: "cwd: /repo"},
		Messages: []session.Message{
			session.NewUserMessage("Project instructions (AGENTS.md):\nfollow the layering rules"),
			session.NewUserMessage("Please summarise the architecture."),
			session.NewAssistantMessage("Sure, looking now.", "", nil),
		},
	}
}

func marshalParams(t *testing.T, p *Provider, req port.LLMRequest) string {
	t.Helper()
	params, err := p.buildParams(req)
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return string(raw)
}

func TestCacheDialectNoneEmitsNoHints(t *testing.T) {
	p := New(WithAPIKey("sk-test")) // CacheDialect unset -> CacheDialectNone
	raw := marshalParams(t, p, cacheReq("gpt-5.2"))
	for _, key := range []string{"prompt_cache_key", "prompt_cache_retention", "cache_control"} {
		if strings.Contains(raw, key) {
			t.Errorf("CacheDialectNone must emit no cache hints, found %q: %s", key, raw)
		}
	}
}

func TestCacheDialectOpenAINeverSendsCacheControl(t *testing.T) {
	p := New(WithAPIKey("sk-test"), WithCacheDialect(CacheDialectOpenAI))
	raw := marshalParams(t, p, cacheReq("gpt-5.2"))
	// The leak guard: cache_control is an OpenRouter-only field. Sending it to
	// real OpenAI 400s, so it must NEVER appear under this dialect.
	if strings.Contains(raw, "cache_control") {
		t.Fatalf("CacheDialectOpenAI must never send cache_control (OpenRouter-only field, 400s on real OpenAI): %s", raw)
	}
	if !strings.Contains(raw, `"prompt_cache_key":"mecatl-`) {
		t.Errorf("CacheDialectOpenAI must send prompt_cache_key: %s", raw)
	}
	if !strings.Contains(raw, `"prompt_cache_retention":"24h"`) {
		t.Errorf("gpt-5.2 is allow-listed; expected prompt_cache_retention=24h: %s", raw)
	}
}

func TestCacheDialectOpenAIOmitsRetentionForUnknownModel(t *testing.T) {
	p := New(WithAPIKey("sk-test"), WithCacheDialect(CacheDialectOpenAI))
	raw := marshalParams(t, p, cacheReq("gpt-5.6"))
	if strings.Contains(raw, "prompt_cache_retention") {
		t.Errorf("gpt-5.6 is deny-listed (deprecated); prompt_cache_retention must be omitted, not guessed: %s", raw)
	}
	if !strings.Contains(raw, `"prompt_cache_key":"mecatl-`) {
		t.Errorf("prompt_cache_key must still be sent regardless of retention eligibility: %s", raw)
	}
}

func TestCacheDialectOpenRouterSendsCacheControlNoRetention(t *testing.T) {
	p := New(WithAPIKey("sk-test"), WithCacheDialect(CacheDialectOpenRouter))
	// Use a model string that WOULD be retention-allow-listed under
	// CacheDialectOpenAI, to prove the OpenRouter arm never even consults
	// retentionFor.
	raw := marshalParams(t, p, cacheReq("gpt-5.2"))
	if !strings.Contains(raw, `"cache_control":{"type":"ephemeral"}`) {
		t.Errorf("CacheDialectOpenRouter must send the request-root cache_control field: %s", raw)
	}
	if !strings.Contains(raw, `"prompt_cache_key":"mecatl-`) {
		t.Errorf("CacheDialectOpenRouter must send prompt_cache_key: %s", raw)
	}
	if strings.Contains(raw, "prompt_cache_retention") {
		t.Errorf("CacheDialectOpenRouter must NEVER send prompt_cache_retention (OpenAI-only field): %s", raw)
	}
}

func TestRetentionModelAllowlist(t *testing.T) {
	positives := []string{
		"gpt-5", "gpt-5-codex", "gpt-5.1", "gpt-5.1-2026-01-01",
		"gpt-5.2", "gpt-5.4", "gpt-5.5", "gpt-5.5-pro", "gpt-4.1",
	}
	for _, model := range positives {
		t.Run(model, func(t *testing.T) {
			retention, ok := retentionFor(model)
			if !ok {
				t.Fatalf("retentionFor(%q) ok = false, want true", model)
			}
			if retention != "24h" {
				t.Errorf("retentionFor(%q) = %q, want 24h", model, retention)
			}
		})
	}
	negatives := []string{"gpt-5.6", "gpt-5.6-codex", "gpt-6", "gpt-4o", "openai/gpt-5", ""}
	for _, model := range negatives {
		t.Run(fmt.Sprintf("neg_%s", model), func(t *testing.T) {
			if _, ok := retentionFor(model); ok {
				t.Errorf("retentionFor(%q) ok = true, want false (omit, never guess)", model)
			}
		})
	}
}

func TestPromptCacheKeyStableAcrossTurns(t *testing.T) {
	p := New(WithAPIKey("sk-test"))
	prefix := "You are a coding agent."
	turn1 := []session.Message{session.NewUserMessage("Please summarise the architecture.")}
	turn2 := []session.Message{
		session.NewUserMessage("Please summarise the architecture."),
		session.NewAssistantMessage("Looking now.", "", nil),
		session.NewToolMessage(session.NewToolResult("call_1", "package a")),
	}
	key1 := p.promptCacheKey(prefix, turn1)
	key2 := p.promptCacheKey(prefix, turn2)
	if key1 != key2 {
		t.Errorf("prompt_cache_key changed as the conversation grew: %q != %q", key1, key2)
	}
}

func TestPromptCacheKeyDiffersPerConversation(t *testing.T) {
	p := New(WithAPIKey("sk-test"))
	prefix := "You are a coding agent."
	keyA := p.promptCacheKey(prefix, []session.Message{session.NewUserMessage("Task A: summarise the architecture.")})
	keyB := p.promptCacheKey(prefix, []session.Message{session.NewUserMessage("Task B: fix the flaky test.")})
	if keyA == keyB {
		t.Errorf("two different conversations must not share a prompt_cache_key: %q", keyA)
	}
}

func TestPromptCacheKeySkipsTurn0Fragments(t *testing.T) {
	p := New(WithAPIKey("sk-test"))
	prefix := "You are a coding agent."
	genuine := "Please summarise the architecture."
	withFragmentA := []session.Message{
		session.NewUserMessage("Project instructions (AGENTS.md):\nfragment body A"),
		session.NewUserMessage(genuine),
	}
	withFragmentB := []session.Message{
		session.NewUserMessage("Project instructions (AGENTS.md):\na COMPLETELY different fragment body"),
		session.NewUserMessage(genuine),
	}
	keyA := p.promptCacheKey(prefix, withFragmentA)
	keyB := p.promptCacheKey(prefix, withFragmentB)
	if keyA != keyB {
		t.Errorf("prompt_cache_key must be derived from the genuine user turn, not the leading fragment: %q != %q", keyA, keyB)
	}
}

func TestPromptCacheKeyMemoUnderAlternatingPrefixes(t *testing.T) {
	p := New(WithAPIKey("sk-test"))
	msgs := []session.Message{session.NewUserMessage("Please summarise the architecture.")}
	prefixA := "You are a coding agent. Variant A."
	prefixB := "You are a coding agent. Variant B."
	keyA1 := p.promptCacheKey(prefixA, msgs)
	_ = p.promptCacheKey(prefixB, msgs) // evicts the single-slot memo
	keyA2 := p.promptCacheKey(prefixA, msgs)
	if keyA1 != keyA2 {
		t.Errorf("prompt_cache_key for prefix A must be stable across an intervening prefix-B call (memo churn must not corrupt correctness): %q != %q", keyA1, keyA2)
	}
	keyB1 := p.promptCacheKey(prefixB, msgs)
	keyB2 := p.promptCacheKey(prefixB, msgs)
	if keyB1 != keyB2 {
		t.Errorf("prompt_cache_key for prefix B must be stable across repeated calls: %q != %q", keyB1, keyB2)
	}
	if keyA1 == keyB1 {
		t.Errorf("different prefixes must not share a prompt_cache_key: %q", keyA1)
	}
}

// BenchmarkPromptCacheKey measures the steady-state cost of promptCacheKey —
// repeated calls with the SAME StablePrefix (the common case: it is
// byte-identical across every turn of one Engine.Run), which should hit the
// single-slot memo and pay only a string-equality memcmp plus one anchor hash
// per call.
func BenchmarkPromptCacheKey(b *testing.B) {
	p := New(WithAPIKey("sk-test"))
	prefix := strings.Repeat("You are a careful coding agent. Follow the repository's layering rules. ", 200)
	msgs := []session.Message{session.NewUserMessage("Please summarise the architecture.")}
	var sink string
	b.ReportAllocs()
	for b.Loop() {
		sink = p.promptCacheKey(prefix, msgs)
	}
	_ = sink
}
