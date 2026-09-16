package app

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestADR_0346_CacheKeySaltedPerProcess pins the composition half of AC3.1: each
// process mints its own salt, so two installations cannot emit an identical
// prompt_cache_key. The adapter half — that a distinct salt yields a distinct
// key on the wire — is TestADR_0346_CacheKeySaltChangesKey in provider/openai.
func TestADR_0346_CacheKeySaltedPerProcess(t *testing.T) {
	a, b := newPromptCacheKeySalt(), newPromptCacheKeySalt()
	if a == "" || b == "" {
		t.Fatalf("salt must be non-empty when crypto/rand is healthy (a=%q b=%q)", a, b)
	}
	if a == b {
		t.Errorf("two mints produced the same salt %q — it is not random", a)
	}
	if len(a) != 32 {
		t.Errorf("salt hex length = %d, want 32 (16 random bytes)", len(a))
	}
}

// TestADR_0346_CacheKeySaltNeverPersistedOrLogged pins AC3.3 structurally: the
// salt is a private Config field with no persistence or diagnostics path, and the
// posture line — the one place composition narrates cache state — must not carry
// it. A salt in a log or a snapshot would reinstate exactly the durable
// correlatable identifier the salt exists to avoid.
func TestADR_0346_CacheKeySaltNeverPersistedOrLogged(t *testing.T) {
	cfg := orCfg()
	cfg.promptCacheKeySalt = "SENTINEL-SALT-VALUE"
	reg, err := buildProviderRegistry(cfg, noEnv)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	if got := promptCachePostureLine(reg, cfg); got == "" {
		t.Fatal("expected a posture line")
	} else if strings.Contains(got, "SENTINEL-SALT-VALUE") {
		t.Errorf("the posture line leaked the salt: %q", got)
	}
	for _, id := range reg.Available() {
		if src := promptCacheSource(reg, cfg, id); strings.Contains(src, "SENTINEL-SALT-VALUE") {
			t.Errorf("provider %q source string leaked the salt: %q", id, src)
		}
	}
}

// TestADR_0346_OpenAICompatEntryRefusesRedirect pins AC5.1 for a generic
// openai-compat entry (openai / openrouter / openai-codex), which
// newOpenAICompatEntry previously built with the SDK's default client. A 307
// re-sends the BODY — system prompt, file contents, tool results — and Go only
// strips Authorization, not the payload.
//
// TWO independent layers now enforce this, and the assertion is deliberately the
// security property (attacker hit count == 0) rather than either mechanism:
// openai-go >= v3.54.0 refuses a cross-origin redirect in its own request layer
// (internal/requestconfig/origin.go), and ADR 0346 additionally gives the entry
// a RefuseRedirects client. The composition layer is what survives an SDK
// downgrade or a future regression that drops the SDK guard, which is precisely
// why it is worth having even though the SDK currently also refuses. Mirrors
// TestGatewayInferenceRefusesRedirects, whose comment records the same history.
func TestADR_0346_OpenAICompatEntryRefusesRedirect(t *testing.T) {
	var attackerHits atomic.Int32
	attacker := httptest.NewServer(terminalSSEHandler(&attackerHits))
	defer attacker.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	cfg := orCfg()
	cfg.LLMMaxAttempts = 1 // no retries: keeps the hit-count assertion deterministic
	cfg.ProviderOverrides = providerOverridesFor(providerOpenRouter, redirector.URL)
	reg, err := buildProviderRegistry(cfg, noEnv)
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	entry, ok := reg.Lookup(providerOpenRouter)
	if !ok {
		t.Fatal("openrouter entry missing")
	}

	chunks, streamErr := driveStream(entry.provider)
	if !errors.Is(streamErr, io.ErrUnexpectedEOF) {
		t.Fatalf("stream error = %v, want truncation after the refused redirect", streamErr)
	}
	if chunks != 0 {
		t.Errorf("stream yielded %d chunks from a redirect response, want 0", chunks)
	}
	if got := attackerHits.Load(); got != 0 {
		t.Errorf("the conversation body reached the redirect target %d time(s), want 0", got)
	}
}
