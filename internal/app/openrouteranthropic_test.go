package app

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/provider/openai"
)

// noEnv is the offline detector: no ambient credential leaks into these tests,
// so a registry contains exactly what the Config asked for.
func noEnv(string) string { return "" }

// orCfg is a Config with ONLY an OpenRouter credential, the deployment shape the
// reported incident came from.
func orCfg() Config {
	return Config{OpenRouterKey: "test-key", ToolhiveLLM: false}
}

// TestUnifiedPromptCache_Scenario1_OpenRouterRegistersBothProtocolEntries pins
// AC1.1: one credential registers both protocol entries (ADR 0346 decision 2,
// following ADR 0334's register-on-intent), so an existing user's Claude default
// starts caching on upgrade with no config change.
func TestUnifiedPromptCache_Scenario1_OpenRouterRegistersBothProtocolEntries(t *testing.T) {
	reg, err := buildProviderRegistry(orCfg(), noEnv)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	for _, id := range []string{providerOpenRouter, providerOpenRouterAnthropic} {
		if _, ok := reg.Lookup(id); !ok {
			t.Errorf("provider %q not registered from one OpenRouter credential", id)
		}
	}
	// The Anthropic entry must ride the NATIVE Messages adapter — that flag is
	// what makes caching unconditional, and therefore what makes this work.
	entry, _ := reg.Lookup(providerOpenRouterAnthropic)
	if !entry.anthropicProtocol {
		t.Error("openrouter-anthropic must be marked as the native Anthropic Messages protocol")
	}
	if !strings.HasSuffix(entry.baseURL, "/api") {
		t.Errorf("openrouter-anthropic baseURL = %q, want the /api Anthropic base", entry.baseURL)
	}
}

// TestADR_0346_OpenRouterAnthropicBaseDerivation pins AC1.2. The SDK appends
// /v1/messages itself, so the base must SHED a terminal v1 — and per ADR 0334 it
// must carry no userinfo, query, or fragment into a request URL.
func TestADR_0346_OpenRouterAnthropicBaseDerivation(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"canonical openrouter base", "https://openrouter.ai/api/v1", "https://openrouter.ai/api"},
		{"trailing slash", "https://openrouter.ai/api/v1/", "https://openrouter.ai/api"},
		{"bare v1 path", "https://example.test/v1", "https://example.test"},
		{"no terminal v1 keeps the path", "https://example.test/gw", "https://example.test/gw"},
		{"userinfo stripped", "https://user:pw@openrouter.ai/api/v1", "https://openrouter.ai/api"},
		{"query and fragment stripped", "https://openrouter.ai/api/v1?k=v#frag", "https://openrouter.ai/api"},
		{"empty in, empty out", "", ""},
		{"unparseable, empty out", "://nope", ""},
		{"schemeless, empty out", "openrouter.ai/api/v1", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := openRouterAnthropicBaseURL(tc.in); got != tc.want {
				t.Errorf("openRouterAnthropicBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestADR_0346_OpenRouterAnthropicListsAnthropicOnly pins AC1.5: OpenRouter's
// Anthropic surface does not serve non-Anthropic models, so advertising them
// would offer ids that cannot execute.
func TestADR_0346_OpenRouterAnthropicListsAnthropicOnly(t *testing.T) {
	for _, id := range []string{"anthropic/claude-opus-4.8", "anthropic/claude-sonnet-4-6", "ANTHROPIC/Claude-X"} {
		if !isOpenRouterAnthropicModel(id) {
			t.Errorf("%q should classify as Anthropic-family", id)
		}
	}
	for _, id := range []string{"openai/gpt-5", "google/gemini-2.5-pro", "qwen/qwen3-max", "", "not-anthropic/claude"} {
		if isOpenRouterAnthropicModel(id) {
			t.Errorf("%q must NOT classify as Anthropic-family", id)
		}
	}
}

// TestADR_0346_OpenRouterResponsesEntryByteIdentical pins AC1.6: adding a sibling
// must not disturb the existing entry's endpoint.
func TestADR_0346_OpenRouterResponsesEntryByteIdentical(t *testing.T) {
	reg, err := buildProviderRegistry(orCfg(), noEnv)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	entry, ok := reg.Lookup(providerOpenRouter)
	if !ok {
		t.Fatal("openrouter not registered")
	}
	if entry.baseURL != openRouterDefaultBaseURL {
		t.Errorf("openrouter baseURL = %q, want the unchanged %q", entry.baseURL, openRouterDefaultBaseURL)
	}
	if entry.anthropicProtocol {
		t.Error("the openrouter Responses entry must not be marked Anthropic-protocol")
	}
}

// TestUnifiedPromptCache_Scenario2_ClaudeDefaultPrefersMessagesEntry pins AC2.1:
// the reported session reached the uncached entry by DEFAULT, not by choosing it.
func TestUnifiedPromptCache_Scenario2_ClaudeDefaultPrefersMessagesEntry(t *testing.T) {
	cfg := orCfg()
	cfg.Model = "anthropic/claude-opus-4.8"
	reg, err := buildProviderRegistry(cfg, noEnv)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	if got := reg.Default(); got != providerOpenRouterAnthropic {
		t.Errorf("default provider for a Claude model = %q, want %q", got, providerOpenRouterAnthropic)
	}
	if got := reg.ResolvedDefaultModel(); got != cfg.Model {
		t.Errorf("model must be carried verbatim, got %q want %q", got, cfg.Model)
	}
}

// TestADR_0346_NonClaudeDefaultPrecedenceUnchanged pins AC2.3: the preference is
// narrow. A non-Anthropic default must resolve exactly as it does today.
func TestADR_0346_NonClaudeDefaultPrecedenceUnchanged(t *testing.T) {
	for _, model := range []string{"", "openai/gpt-5", "google/gemini-2.5-pro"} {
		cfg := orCfg()
		cfg.Model = model
		reg, err := buildProviderRegistry(cfg, noEnv)
		if err != nil {
			t.Fatalf("build registry (model=%q): %v", model, err)
		}
		if got := reg.Default(); got != providerOpenRouter {
			t.Errorf("model=%q: default = %q, want the unchanged %q", model, got, providerOpenRouter)
		}
	}
}

// TestADR_0346_ExplicitDefaultProviderOverridesCachingPreference pins AC2.4: the
// preference applies to a DEFAULT, never over an operator who named the provider.
func TestADR_0346_ExplicitDefaultProviderOverridesCachingPreference(t *testing.T) {
	cfg := orCfg()
	cfg.Model = "anthropic/claude-opus-4.8"
	cfg.DefaultProvider = providerOpenRouter
	reg, err := buildProviderRegistry(cfg, noEnv)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	if got := reg.Default(); got != providerOpenRouter {
		t.Errorf("explicit --default-provider = %q was overridden to %q", providerOpenRouter, got)
	}
}

// TestADR_0346_PromptCachedProjectedOncePerModel pins AC3.4: the picker signal
// is computed by composition's single projection, so the picker and the wire
// cannot disagree. The FALSE case is asserted by the sibling
// TestADR_0346_PromptCachedTrueWithoutDialect, which owns the
// --no-prompt-cache sweep now that it is the only way to reach false.
func TestADR_0346_PromptCachedProjectedOncePerModel(t *testing.T) {
	reg, err := buildProviderRegistry(orCfg(), noEnv)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	if reg.promptCached == nil {
		t.Fatal("promptCached projection not bound on the registry")
	}
	if !reg.promptCached(providerOpenRouterAnthropic, "anthropic/claude-opus-4.8") {
		t.Error("the native Messages entry must report prompt_cached true")
	}
	// The canonical OpenRouter Responses endpoint carries the protocol-native
	// prompt_cache_breakpoint (root cache_control is RETIRED by decision 3), so
	// it is honestly true.
	if !reg.promptCached(providerOpenRouter, "anthropic/claude-opus-4.8") {
		t.Error("canonical openrouter carries the OpenRouter dialect, so it must report true")
	}
	if got := promptCacheSource(reg, orCfg(), providerOpenRouterAnthropic); !strings.Contains(got, "Messages") {
		t.Errorf("source for the Messages entry = %q, want it to name the Messages protocol", got)
	}
}

// TestADR_0346_PromptCachedTrueWithoutDialect REVERSES what an earlier draft of
// this work asserted, and the reversal is the headline of ADR 0346.
//
// Before: an endpoint with no cache dialect emitted nothing, so an Anthropic
// model there silently re-paid full input every turn — the reported incident.
// After: the protocol-native breakpoint goes out regardless of dialect, so that
// same endpoint caches. The dialect still governs the vendor-shaped hints, and
// only --no-prompt-cache turns caching off.
func TestADR_0346_PromptCachedTrueWithoutDialect(t *testing.T) {
	cfg := orCfg()
	cfg.ProviderOverrides = permconfig.ProviderOverrides{
		providerOpenRouter: {BaseURL: "https://my-proxy.internal/v1"},
	}
	reg, err := buildProviderRegistry(cfg, noEnv)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	if !reg.promptCached(providerOpenRouter, "anthropic/claude-opus-4.8") {
		t.Error("an overridden base URL now caches via the breakpoint; prompt_cached must be true")
	}
	if got := promptCacheSource(reg, cfg, providerOpenRouter); !strings.Contains(got, "breakpoint") {
		t.Errorf("source = %q, want it to name the breakpoint", got)
	}

	off := orCfg()
	off.PromptCacheDisabled = true
	regOff, err := buildProviderRegistry(off, noEnv)
	if err != nil {
		t.Fatalf("build registry (disabled): %v", err)
	}
	for _, id := range []string{providerOpenRouter, providerOpenRouterAnthropic} {
		if regOff.promptCached(id, "anthropic/claude-opus-4.8") {
			t.Errorf("--no-prompt-cache must force prompt_cached false, %q reported true", id)
		}
	}
	if got := promptCacheSource(regOff, off, providerOpenRouter); !strings.Contains(got, "--no-prompt-cache") {
		t.Errorf("disabled source = %q, want it to name the flag", got)
	}
}

// TestUnifiedPromptCache_Scenario4_PostureLineNamesPostureAndSource pins AC4.1:
// ADR 0100's failure mode was silence, so the resolved posture must be operator
// visible AND name its source, not just a boolean.
func TestUnifiedPromptCache_Scenario4_PostureLineNamesPostureAndSource(t *testing.T) {
	reg, err := buildProviderRegistry(orCfg(), noEnv)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	line := promptCachePostureLine(reg, orCfg())
	if !strings.HasPrefix(line, "prompt cache: ") {
		t.Fatalf("posture line = %q, want the prompt-cache prefix", line)
	}
	for _, want := range []string{providerOpenRouter + "=", providerOpenRouterAnthropic + "="} {
		if !strings.Contains(line, want) {
			t.Errorf("posture line %q omits %q", line, want)
		}
	}

	off := orCfg()
	off.PromptCacheDisabled = true
	if got := promptCachePostureLine(reg, off); !strings.Contains(got, "--no-prompt-cache") {
		t.Errorf("disabled posture line = %q, want it to name the flag", got)
	}
	if got := promptCachePostureLine(nil, orCfg()); got != "" {
		t.Errorf("nil registry must produce no line, got %q", got)
	}
}

// TestADR_0346_AnthropicFamilyClassifier pins the narrow matcher behind the
// default preference: it keys on the vendor namespace and the product name,
// never a substring anywhere in the id.
func TestADR_0346_AnthropicFamilyClassifier(t *testing.T) {
	for _, id := range []string{"anthropic/claude-opus-4.8", "claude-sonnet-4-6", "  Claude-Opus  "} {
		if !isAnthropicFamilyModel(id) {
			t.Errorf("%q should be Anthropic-family", id)
		}
	}
	for _, id := range []string{"", "openai/gpt-5", "google/gemini-2.5-pro", "my-claude-clone/x", "openai/claude-ish"} {
		if isAnthropicFamilyModel(id) {
			t.Errorf("%q must NOT be Anthropic-family", id)
		}
	}
}

// providerOverridesFor builds a one-entry operator base-URL override map.
func providerOverridesFor(id, baseURL string) permconfig.ProviderOverrides {
	return permconfig.ProviderOverrides{id: {BaseURL: baseURL}}
}

// TestADR_0346_GatewayPairNotRedirected pins AC3.2, which REVERSES an earlier
// draft of this work. The gateway's two surfaces do not share a model-id
// namespace — staging exposes one Claude Opus 4.8 as `anthropic/claude-opus-4.8`
// on its OpenRouter downstream, `claude-opus-4-8` on its Anthropic downstream
// and `us.anthropic.claude-opus-4-8` on Bedrock — so switching the provider
// while carrying the id verbatim would yield a pair that cannot resolve.
//
// The gateway case is covered by the protocol-native breakpoint instead, which
// needs no id translation. This test exists so nobody "helpfully" restores the
// toolhive row in anthropicProtocolSibling.
func TestADR_0346_GatewayPairNotRedirected(t *testing.T) {
	cfg := Config{
		ToolhiveLLMBaseURL: "http://127.0.0.1:14000/v1",
		Model:              "anthropic/claude-opus-4.8",
	}
	reg, err := buildProviderRegistry(cfg, noEnv)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	if _, ok := reg.Lookup(providerToolhiveAnthropic); !ok {
		t.Fatal("toolhive-anthropic not registered; the gateway fixture did not resolve")
	}
	if got := reg.Default(); got != providerToolhive {
		t.Errorf("gateway default = %q, want %q — the pair must NOT be redirected", got, providerToolhive)
	}
	if _, listed := anthropicProtocolSibling[providerToolhive]; listed {
		t.Error("providerToolhive must not appear in anthropicProtocolSibling: its surfaces do not share an id namespace")
	}
}

// TestADR_0346_LeakGuardBothDirectionsStillHolds pins AC4.4. ADR 0100's dialect
// table is deliberately UNTOUCHED by ADR 0346 — routing replaced the declaration
// surface that would have widened it — so the guard that root cache_control never
// reaches canonical OpenAI must still hold exactly as before.
func TestADR_0346_LeakGuardBothDirectionsStillHolds(t *testing.T) {
	if got := cacheDialectFor(providerOpenAI, "", Config{}); got != openai.CacheDialectOpenAI {
		t.Errorf("canonical openai dialect = %q, want the openai dialect (never openrouter)", got)
	}
	if got := cacheDialectFor(providerOpenRouter, openRouterDefaultBaseURL, Config{}); got != openai.CacheDialectOpenRouter {
		t.Errorf("canonical openrouter dialect = %q, want the openrouter dialect", got)
	}
	// No new provider id may acquire a dialect implicitly.
	if got := cacheDialectFor(providerOpenRouterAnthropic, "https://openrouter.ai/api", Config{}); got != openai.CacheDialectNone {
		t.Errorf("openrouter-anthropic must take NO openai dialect (it is a Messages entry), got %q", got)
	}
}
