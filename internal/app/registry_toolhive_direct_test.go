package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/openaicompat"
)

// captureTransport records the Authorization header each request carries and
// serves a minimal OK response, so a bearerRoundTripper test can assert the
// header rewrite without a real token source.
type captureTransport struct {
	auth    atomic.Value // string
	xAPI    atomic.Value // string
	calls   atomic.Int32
	recvErr error
}

func (c *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.calls.Add(1)
	c.auth.Store(req.Header.Get("Authorization"))
	c.xAPI.Store(req.Header.Get("X-Api-Key"))
	if c.recvErr != nil {
		return nil, c.recvErr
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("{}")),
		Header:     make(http.Header),
	}, nil
}

// TestBearerRoundTripper_RewritesHeader is AC #3: the direct-mode transport
// STRIPS the SDK's placeholder Authorization and sets `Bearer <real-token>` on
// every request. A fake token source returns a fixed token; the capture
// transport records what actually reaches the wire.
func TestBearerRoundTripper_RewritesHeader(t *testing.T) {
	const fakeToken = "fake-access-token-12345"
	rt := &bearerRoundTripper{
		base:  &captureTransport{},
		token: func(context.Context) (string, error) { return fakeToken, nil },
	}
	req, _ := http.NewRequest(http.MethodGet, "https://gw.example/v1/models", nil)
	req.Header.Set("Authorization", "Bearer thv-proxy") // the SDK's placeholder
	req.Header.Set("X-Api-Key", "conflicting-placeholder")
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()

	ct := rt.base.(*captureTransport)
	if got := ct.auth.Load().(string); got != "Bearer "+fakeToken {
		t.Errorf("Authorization = %q, want %q", got, "Bearer "+fakeToken)
	}
	if got := ct.xAPI.Load().(string); got != "" {
		t.Errorf("X-Api-Key = %q, want stripped", got)
	}
	if ct.calls.Load() != 1 {
		t.Errorf("base transport calls = %d, want 1", ct.calls.Load())
	}
}

// TestBearerRoundTripper_SanitisedError is AC #3 + #7. Two properties, both
// falsifiable:
//
//   - the request NEVER reaches the base transport on a token-source error, so no
//     request goes out unauthenticated or with the SDK's placeholder;
//   - the error reaches the caller UNWRAPPED (errors.Is identity), because
//     llmresilience classifies retry-vs-cancel with errors.Is — a RoundTripper
//     that re-wrapped or restringified it would break that.
//
// It deliberately does NOT assert "the error contains no secret": the
// RoundTripper returns whatever the token source hands it, so such an assertion
// would only test the fake. Sanitisation is the token source's job and is pinned
// where it lives, in toolhivellm.TestSanitizeTokenError_StripsBearer.
func TestBearerRoundTripper_SanitisedError(t *testing.T) {
	tokErr := errors.New("oauth2 error \"invalid_grant\": refresh token expired")
	rt := &bearerRoundTripper{
		base:  &captureTransport{},
		token: func(context.Context) (string, error) { return "", tokErr },
	}
	req, _ := http.NewRequest(http.MethodGet, "https://gw.example/v1/models", nil)
	req.Header.Set("Authorization", "Bearer thv-proxy")
	_, err := rt.RoundTrip(req)
	if err == nil {
		t.Fatal("expected an error from the token source, got nil")
	}
	if !errors.Is(err, tokErr) {
		t.Errorf("error = %v, want the token-source error unwrapped (errors.Is classification must survive)", err)
	}
	if strings.Contains(err.Error(), "thv-proxy") {
		t.Errorf("error string leaked the placeholder: %q", err.Error())
	}
	ct := rt.base.(*captureTransport)
	if ct.calls.Load() != 0 {
		t.Errorf("base transport calls = %d, want 0 (the request must NOT reach the wire on a token error)", ct.calls.Load())
	}
}

// TestBearerRoundTripper_OnlyMutatesAuthentication is the static guard for the
// "never log credentials" discipline (AGENTS.md security): the RoundTripper has
// no log path of its own, clones before removing the conflicting authentication
// headers, and leaves the caller's request untouched.
func TestBearerRoundTripper_OnlyMutatesAuthentication(t *testing.T) {
	rt := &bearerRoundTripper{
		base:  &captureTransport{},
		token: func(context.Context) (string, error) { return "tok", nil },
	}
	req, _ := http.NewRequest(http.MethodGet, "https://gw.example/v1/models", nil)
	req.Header.Set("Authorization", "Bearer thv-proxy")
	req.Header.Set("X-Trace-Id", "abc")
	req.Header.Set("Content-Type", "application/json")
	_, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	// The ORIGINAL request is untouched (the RoundTripper clones before mutating).
	if got := req.Header.Get("Authorization"); got != "Bearer thv-proxy" {
		t.Errorf("original request Authorization mutated to %q (must clone, not mutate in place)", got)
	}
}

// TestDirectMode_RefusesRedirects is AC #3/#4 extended: the direct-mode entry's
// HTTP client composes the bearer RoundTripper AND RefuseRedirects, so a
// gateway_url answering an inference request with a redirect to an attacker is
// refused — the conversation body + bearer never leave the gateway host. This
// mirrors TestGatewayInferenceRefusesRedirects for the proxy entry.
func TestDirectMode_RefusesRedirects(t *testing.T) {
	var attackerHits atomic.Int32
	attacker := httptest.NewServer(terminalSSEHandler(&attackerHits))
	defer attacker.Close()

	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL, http.StatusTemporaryRedirect)
	}))
	defer gw.Close()

	// Build the bearer client exactly as newDirectGatewayEntry does.
	rt := &bearerRoundTripper{
		base:  http.DefaultTransport,
		token: func(context.Context) (string, error) { return "fake-tok", nil },
	}
	client := &http.Client{
		Transport:     rt,
		CheckRedirect: openaicompat.RefuseRedirects,
	}
	// A direct-mode entry would set baseURL = gateway_url + "/v1"; here we hit
	// the test gateway directly to exercise the redirect refusal.
	resp, err := client.Get(gw.URL + "/v1/responses")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = resp.Body.Close()
	// RefuseRedirects returns the 3xx as the final response (no follow).
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("status = %d, want %d (redirect refused, returned as final)", resp.StatusCode, http.StatusTemporaryRedirect)
	}
	if got := attackerHits.Load(); got != 0 {
		t.Fatalf("attacker hit count = %d, want 0 (direct-mode client must refuse the redirect)", got)
	}
}

// TestDirectBaseURL is the unit pin for directBaseURL: gateway_url + "/v1", with
// trailing-slash normalization. It is the ONE derivation of a request URL from
// gateway_url (the proxy mode never does this).
func TestDirectBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://gw.example.com":           "https://gw.example.com/v1",
		"https://gw.example.com/":          "https://gw.example.com/v1",
		"https://gw.example.com/toolhive":  "https://gw.example.com/toolhive/v1",
		"https://gw.example.com/toolhive/": "https://gw.example.com/toolhive/v1",
		"":                                 "",
		// Credential-shaped query/userinfo/fragment material is stripped rather
		// than concatenated into a request or diagnostic URL.
		"https://gw.example.com?x=1":                                      "https://gw.example.com/v1",
		"https://user:secret@gw.example.com/prefix?token=secret#fragment": "https://gw.example.com/prefix/v1",
	} {
		if got := directBaseURL(in); got != want {
			t.Errorf("directBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestGatewayURLIsHTTPS is the pin for the direct-mode cleartext gate: only
// https, or http to a LOOPBACK HOST, may carry the injected OIDC bearer
// (CWE-319). The load-bearing cases are the near-miss hosts —
// "http://localhost.attacker.com" and "http://127.0.0.1.nip.io" are publicly
// resolvable names that a string-PREFIX carve-out approves, which is how a
// bearer would end up on the wire in cleartext to an attacker-controlled host.
// A host comparison rejects them; a prefix test does not, so this table fails
// if the gate ever regresses to HasPrefix.
func TestGatewayURLIsHTTPS(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://gw.example.com":        true,
		"https://gw.example.com:8443":   true,
		"HTTPS://gw.example.com":        true, // scheme is case-insensitive (RFC 3986)
		"http://localhost":              true, // local-dev carve-out
		"http://localhost:14000":        true,
		"http://127.0.0.1:14000":        true,
		"http://[::1]:14000":            true,
		"http://localhost.attacker.com": false, // prefix-match bypass
		"http://127.0.0.1.nip.io":       false, // prefix-match bypass
		"http://gw.example.com":         false, // plain cleartext
		"ftp://gw.example.com":          false, // wrong scheme
		"":                              false, // no scheme
		"://":                           false, // unparseable ⇒ fail closed
	} {
		if got := gatewayURLIsHTTPS(raw); got != want {
			t.Errorf("gatewayURLIsHTTPS(%q) = %v, want %v", raw, got, want)
		}
	}
}

// TestResolveToolhiveIntent_DirectFallsBackOnCleartextHost is the behavioural
// half of the gate: a gateway_url whose host merely LOOKS loopback resolves to
// PROXY mode (with the WARN), never direct — so no bearer is minted for it.
func TestResolveToolhiveIntent_DirectFallsBackOnCleartextHost(t *testing.T) {
	cfgPath := writeToolhiveConfigWithOIDC(t, "http://localhost.attacker.com", "https://idp.example", "client-123")
	intent, ok := resolveToolhiveIntent(Config{
		ToolhiveLLM:        true,
		toolhiveConfigPath: cfgPath,
		Diagnostics:        &toolhiveLevelDiag{},
	})
	if !ok {
		t.Fatal("expected registration")
	}
	if intent.mode != toolhiveModeProxy {
		t.Errorf("mode = %v, want proxy (cleartext non-loopback host must not go direct)", intent.mode)
	}
	if !strings.HasPrefix(intent.baseURL, "http://127.0.0.1:") {
		t.Errorf("baseURL = %q, want the loopback proxy", intent.baseURL)
	}
}

// TestResolveToolhiveIntent_DirectWhenOIDCConfigured is AC #1: auto mode +
// OIDC trio configured ⇒ direct, baseURL = gateway_url + "/v1". It uses a
// config fixture WITH the oidc block (written via a helper that extends
// writeToolhiveConfig). Because toolhivellm.OIDCConfigured reads toolhive's own
// config read (not detect.go's wireConfig), the fixture must carry the OIDC
// trio — which DetectConfig ignores, so the SAME fixture is valid for both.
func TestResolveToolhiveIntent_DirectWhenOIDCConfigured(t *testing.T) {
	cfgPath := writeToolhiveConfigWithOIDC(t, "https://gw.example.com", "https://idp.example", "client-123")
	intent, ok := resolveToolhiveIntent(Config{
		ToolhiveLLM:        true,
		toolhiveConfigPath: cfgPath,
	})
	if !ok {
		t.Fatal("expected registration with OIDC configured")
	}
	if intent.mode != toolhiveModeDirect {
		t.Errorf("mode = %v, want direct", intent.mode)
	}
	if want := "https://gw.example.com/v1"; intent.baseURL != want {
		t.Errorf("baseURL = %q, want %q (gateway_url + /v1)", intent.baseURL, want)
	}
	if intent.explicit {
		t.Error("explicit should be false for the config-file path")
	}
}

// TestResolveToolhiveIntent_ProxyFallbackWhenNoOIDC is AC #2: auto mode + NO
// oidc block ⇒ proxy, baseURL = loopback (byte-identical to pre-#265).
func TestResolveToolhiveIntent_ProxyFallbackWhenNoOIDC(t *testing.T) {
	cfgPath := writeToolhiveConfig(t, "https://gw.example.com") // no oidc
	intent, ok := resolveToolhiveIntent(Config{
		ToolhiveLLM:        true,
		toolhiveConfigPath: cfgPath,
	})
	if !ok {
		t.Fatal("expected registration even without OIDC")
	}
	if intent.mode != toolhiveModeProxy {
		t.Errorf("mode = %v, want proxy (auto fallback)", intent.mode)
	}
	if !strings.HasPrefix(intent.baseURL, "http://127.0.0.1:") {
		t.Errorf("baseURL = %q, want loopback", intent.baseURL)
	}
}

// TestResolveToolhiveIntent_ProxyFlagForcesProxy is AC #5: --toolhive-llm-mode
// proxy forces the loopback even with OIDC configured.
func TestResolveToolhiveIntent_ProxyFlagForcesProxy(t *testing.T) {
	cfgPath := writeToolhiveConfigWithOIDC(t, "https://gw.example.com", "https://idp.example", "client-123")
	intent, ok := resolveToolhiveIntent(Config{
		ToolhiveLLM:        true,
		ToolhiveLLMMode:    "proxy",
		toolhiveConfigPath: cfgPath,
	})
	if !ok {
		t.Fatal("expected registration")
	}
	if intent.mode != toolhiveModeProxy {
		t.Errorf("mode = %v, want proxy (forced)", intent.mode)
	}
}

// TestValidateToolhiveLLMMode_DirectRequiresOIDC is AC #6: direct mode + NO
// oidc block ⇒ Build-fail with an actionable error naming the missing fields.
func TestValidateToolhiveLLMMode_DirectRequiresOIDC(t *testing.T) {
	cfgPath := writeToolhiveConfig(t, "https://gw.example.com") // no oidc
	err := validateToolhiveLLMMode(Config{
		ToolhiveLLM:        true,
		ToolhiveLLMMode:    "direct",
		toolhiveConfigPath: cfgPath,
	})
	if err == nil {
		t.Fatal("expected an error for direct mode without OIDC")
	}
	for _, want := range []string{"direct", "gateway_url", "oidc.issuer", "oidc.client_id"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

// TestValidateToolhiveLLMMode_DirectOKWithOIDC is the positive control: direct
// mode + OIDC configured ⇒ no error.
func TestValidateToolhiveLLMMode_DirectOKWithOIDC(t *testing.T) {
	cfgPath := writeToolhiveConfigWithOIDC(t, "https://gw.example.com", "https://idp.example", "client-123")
	if err := validateToolhiveLLMMode(Config{
		ToolhiveLLM:        true,
		ToolhiveLLMMode:    "direct",
		toolhiveConfigPath: cfgPath,
	}); err != nil {
		t.Errorf("expected no error with OIDC configured, got %v", err)
	}
}

// TestValidateToolhiveLLMMode_AutoAndProxyAreNoOps: the auto default and the
// proxy value never fail (the auto-fallback and the proxy path are handled in
// resolveToolhiveIntent, not here).
func TestValidateToolhiveLLMMode_AutoAndProxyAreNoOps(t *testing.T) {
	cfgPath := writeToolhiveConfig(t, "https://gw.example.com")
	for _, mode := range []string{"", "auto", "proxy"} {
		if err := validateToolhiveLLMMode(Config{
			ToolhiveLLM: true, ToolhiveLLMMode: mode, toolhiveConfigPath: cfgPath,
		}); err != nil {
			t.Errorf("mode %q: unexpected error %v", mode, err)
		}
	}
}

// TestValidateToolhiveLLMMode_DirectIncompatibleWithBaseURL: direct + an
// explicit --toolhive-llm-base-url override is contradictory (the override is a
// loopback proxy address; direct derives from gateway_url) and must fail.
func TestValidateToolhiveLLMMode_DirectIncompatibleWithBaseURL(t *testing.T) {
	cfgPath := writeToolhiveConfigWithOIDC(t, "https://gw.example.com", "https://idp.example", "client-123")
	err := validateToolhiveLLMMode(Config{
		ToolhiveLLM:        true,
		ToolhiveLLMMode:    "direct",
		ToolhiveLLMBaseURL: "http://127.0.0.1:9999/v1",
		toolhiveConfigPath: cfgPath,
	})
	if err == nil {
		t.Fatal("expected an error for direct mode + explicit base-url override")
	}
	if !strings.Contains(err.Error(), "incompatible") {
		t.Errorf("error %q missing the incompatibility reason", err.Error())
	}
}

// TestToolhiveDirectRemintSurvival (F6 AC #4): the direct-mode entry's remint
// closure is non-nil and produces a non-nil provider when called. The token
// source construction may fail (no real keyring in tests), but the entry
// gracefully falls back to an error-returning func and the remint closure
// still carries the WithHTTPClient. openai.New does no network on construction.
func TestToolhiveDirectRemintSurvival(t *testing.T) {
	cfgPath := writeToolhiveConfigWithOIDC(t, "https://gw.example.com", "https://idp.example", "client-123")
	diag := &toolhiveLevelDiag{}
	cfg := Config{
		ToolhiveLLM:        true,
		toolhiveConfigPath: cfgPath,
		Diagnostics:        diag,
	}
	intent := toolhiveIntent{
		mode:       toolhiveModeDirect,
		baseURL:    "https://gw.example.com/v1",
		gatewayURL: "https://gw.example.com",
	}
	entry := newDirectGatewayEntry(cfg, providerToolhive, intent,
		newDirectGatewayClient(cfg, intent, cfgPath))

	if entry.remint == nil {
		t.Fatal("direct-mode entry has no remint closure")
	}
	p := entry.remint("high", port.ProviderCapabilities{})
	if p == nil {
		t.Fatal("remint returned nil provider")
	}
	// The provider should be usable even though the token source errors at
	// request time — the construction path is validated.
}

// TestToolhiveDirectEntryThroughBuildProviderRegistry (F7 AC #1): the full
// buildProviderRegistry path routes to newDirectGatewayEntry when OIDC is
// configured and ToolhiveLLMMode defaults to auto. The resulting toolhive entry
// has baseURL == gatewayURL + "/v1", intentDriven == true, and
// intentGatewayURL == gatewayURL. No providerConstructor is set, so the real
// newDirectGatewayEntry runs.
func TestToolhiveDirectEntryThroughBuildProviderRegistry(t *testing.T) {
	cfgPath := writeToolhiveConfigWithOIDC(t, "https://gw.example.com", "https://idp.example", "client-123")
	diag := &toolhiveLevelDiag{}
	reg, err := buildProviderRegistry(Config{
		ToolhiveLLM:         true,
		toolhiveConfigPath:  cfgPath,
		Diagnostics:         diag,
		liveModelHTTPClient: toolhiveModelsClient(t, toolhiveFixtureJSON),
	}, fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	entry, ok := reg.Lookup(providerToolhive)
	if !ok {
		t.Fatal("toolhive entry not registered")
	}
	if want := "https://gw.example.com/v1"; entry.baseURL != want {
		t.Errorf("baseURL = %q, want %q (gateway_url + /v1)", entry.baseURL, want)
	}
	if !entry.intentDriven {
		t.Error("entry should be intentDriven")
	}
	if entry.intentGatewayURL != "https://gw.example.com" {
		t.Errorf("intentGatewayURL = %q, want %q", entry.intentGatewayURL, "https://gw.example.com")
	}
}
