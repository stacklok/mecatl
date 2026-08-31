package server_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

const testOrigin = "https://app.example.com"

// corsServer stands up the HTTP API behind a CORS policy for the given origins.
// It mirrors the production wiring order — CORS OUTSIDE everything else — so the
// tests exercise the arrangement mecated actually installs.
func corsServer(t *testing.T, origins ...string) *httptest.Server {
	t.Helper()
	policy, err := server.NewCORSPolicy(origins)
	if err != nil {
		t.Fatalf("NewCORSPolicy(%v): %v", origins, err)
	}
	svc := compatibilityInfoService(t, "", port.ProviderCapabilities{})
	srv := httptest.NewServer(policy.Middleware(server.NewHTTPHandler(svc)))
	t.Cleanup(srv.Close)
	return srv
}

// do issues a request with the given method, origin, and optional preflight
// headers, and returns the response (body closed).
func do(t *testing.T, srv *httptest.Server, method, path, origin string, preflightFor string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if preflightFor != "" {
		req.Header.Set("Access-Control-Request-Method", preflightFor)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestSDKServerEnablers_Scenario3_ExactOriginAllowed is AC3.1.
func TestSDKServerEnablers_Scenario3_ExactOriginAllowed(t *testing.T) {
	srv := corsServer(t, testOrigin)
	resp := do(t, srv, http.MethodGet, "/v1/compatibility", testOrigin, "")

	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != testOrigin {
		t.Errorf("Access-Control-Allow-Origin = %q, want the exact origin %q", got, testOrigin)
	}
	if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want %q", got, "true")
	}
	if !containsVary(resp, "Origin") {
		t.Errorf("Vary = %q, want it to include Origin", resp.Header.Values("Vary"))
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 — CORS must not change the response itself", resp.StatusCode)
	}
}

// TestSDKServerEnablers_Scenario3_NearMissOriginsRejected is AC3.2.
//
// The near misses are the point. Suffix matching is the classic CORS bug, and
// mecatl's HTTP API can START AGENT RUNS — so a wrong match is not information
// disclosure, it is arbitrary action taken with the victim's credentials.
func TestSDKServerEnablers_Scenario3_NearMissOriginsRejected(t *testing.T) {
	srv := corsServer(t, testOrigin)

	nearMisses := []struct{ origin, why string }{
		{"https://evil-app.example.com", "a different subdomain"},
		{"https://app.example.com.attacker.net", "the allowed origin as a PREFIX of a hostile domain"},
		{"https://notapp.example.com", "a hostname that merely ends with the allowed one"},
		{"http://app.example.com", "the same host over a different SCHEME"},
		{"https://app.example.com:8443", "the same host on a different PORT"},
		{"https://app.example.com/", "a trailing slash (not a bare origin)"},
		{"https://APP.example.com", "a case variant — Origin is compared byte-exactly"},
		{"null", "the opaque origin any sandboxed page can present"},
	}
	for _, nm := range nearMisses {
		t.Run(nm.origin, func(t *testing.T) {
			resp := do(t, srv, http.MethodGet, "/v1/compatibility", nm.origin, "")
			if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
				t.Errorf("origin %q (%s) was granted Access-Control-Allow-Origin %q", nm.origin, nm.why, got)
			}
			if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "" {
				t.Errorf("origin %q (%s) was granted credentials", nm.origin, nm.why)
			}
			// Vary: Origin must be present even on a REFUSAL, or a shared cache
			// could serve this bodiless-CORS response to an allowed origin.
			if !containsVary(resp, "Origin") {
				t.Errorf("origin %q: Vary is %q, want it to include Origin even when refused", nm.origin, resp.Header.Values("Vary"))
			}
		})
	}
}

// TestADR_0244_NoWildcardWithCredentials is AC3.3.
//
// Wildcard-with-credentials is forbidden by the CORS specification and is the
// single most dangerous misconfiguration available here. It must be impossible
// to reach by configuration, not merely absent by default.
func TestADR_0244_NoWildcardWithCredentials(t *testing.T) {
	for _, bad := range []string{"*", "null", "NULL", "https://*.example.com"} {
		if _, err := server.NewCORSPolicy([]string{bad}); err == nil {
			t.Errorf("NewCORSPolicy(%q) succeeded; it must be refused at startup", bad)
		}
	}

	// And no configuration produces a "*" on the wire.
	srv := corsServer(t, testOrigin, "https://second.example.com")
	for _, origin := range []string{testOrigin, "https://second.example.com", "https://unlisted.example.com", ""} {
		resp := do(t, srv, http.MethodGet, "/v1/compatibility", origin, "")
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got == "*" {
			t.Fatalf("origin %q produced a wildcard Access-Control-Allow-Origin", origin)
		}
		if resp.Header.Get("Access-Control-Allow-Credentials") == "true" && resp.Header.Get("Access-Control-Allow-Origin") == "" {
			t.Errorf("origin %q: credentials granted with no allowed origin", origin)
		}
	}
}

// TestSDKServerEnablers_Scenario3_PreflightDoesNotInvokeHandler is AC3.4.
//
// A preflight is the browser asking permission. It must be answered from headers
// alone: it carries no credentials (the CORS spec forbids them on a preflight),
// so letting it reach a handler would run API logic for an unauthenticated
// request.
func TestSDKServerEnablers_Scenario3_PreflightDoesNotInvokeHandler(t *testing.T) {
	policy, err := server.NewCORSPolicy([]string{testOrigin})
	if err != nil {
		t.Fatalf("NewCORSPolicy: %v", err)
	}
	var invoked bool
	sentinel := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		invoked = true
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(policy.Middleware(sentinel))
	defer srv.Close()

	resp := do(t, srv, http.MethodOptions, "/v1/sessions", testOrigin, http.MethodPost)
	if invoked {
		t.Error("the preflight reached the wrapped handler; it must be answered by the middleware alone")
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Methods"); !strings.Contains(got, http.MethodPost) {
		t.Errorf("Access-Control-Allow-Methods = %q, want it to include POST", got)
	}
	if got := resp.Header.Get("Access-Control-Max-Age"); got == "" {
		t.Error("Access-Control-Max-Age is unset; every request would re-preflight")
	}

	// A REFUSED preflight is also short-circuited, and carries no CORS headers —
	// which is what makes the browser block the real request. It answers 204
	// rather than 403 so a page cannot probe the allowlist.
	invoked = false
	bad := do(t, srv, http.MethodOptions, "/v1/sessions", "https://unlisted.example.com", http.MethodPost)
	if invoked {
		t.Error("a refused preflight reached the wrapped handler")
	}
	if got := bad.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("refused preflight granted Access-Control-Allow-Origin %q", got)
	}
	if bad.StatusCode != resp.StatusCode {
		t.Errorf("refused preflight status %d differs from allowed %d; the difference leaks allowlist membership",
			bad.StatusCode, resp.StatusCode)
	}
}

// TestADR_0244_PreflightEchoesRequestedHeaders covers the header-echo path,
// which the review found had no test at all — the near-miss table asserts
// methods, origin, and status, but nothing ever sent
// Access-Control-Request-Headers to see what came back.
//
// The echo is the real spec of that branch, and it is the branch where getting
// it wrong is worst: publishing a fixed header list silently breaks any client
// needing a header we did not predict, while echoing to an UNVALIDATED origin
// would tell an attacker's page which headers it may send. Both failure modes
// are invisible without an assertion here, because a browser enforces them and
// a Go test client does not.
func TestADR_0244_PreflightEchoesRequestedHeaders(t *testing.T) {
	policy, err := server.NewCORSPolicy([]string{testOrigin})
	if err != nil {
		t.Fatalf("NewCORSPolicy: %v", err)
	}
	srv := httptest.NewServer(policy.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	defer srv.Close()

	preflight := func(t *testing.T, origin, reqHeaders string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodOptions, srv.URL+"/v1/sessions", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", http.MethodPost)
		if reqHeaders != "" {
			req.Header.Set("Access-Control-Request-Headers", reqHeaders)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("preflight: %v", err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	t.Run("an allowed origin gets its requested headers echoed", func(t *testing.T) {
		// Deliberately a header nobody would hardcode: a fixed allow-list would
		// pass a test that only ever asked for Content-Type.
		const want = "authorization, content-type, x-mecatl-idempotency-key"
		resp := preflight(t, testOrigin, want)
		if got := resp.Header.Get("Access-Control-Allow-Headers"); got != want {
			t.Errorf("Access-Control-Allow-Headers = %q, want the request echoed back as %q", got, want)
		}
		// Vary must name the request header too, or a shared cache will serve one
		// origin's allowed-header set to a request that asked for a different one.
		if got := resp.Header.Values("Vary"); !slices.Contains(got, "Access-Control-Request-Headers") {
			t.Errorf("Vary = %v, want it to include Access-Control-Request-Headers", got)
		}
	})

	t.Run("a refused origin gets no echo", func(t *testing.T) {
		resp := preflight(t, "https://unlisted.example.com", "authorization")
		if got := resp.Header.Get("Access-Control-Allow-Headers"); got != "" {
			t.Errorf("a refused origin was told it may send %q; the echo must be gated on the allowlist", got)
		}
	})

	t.Run("no requested headers means no echo header", func(t *testing.T) {
		resp := preflight(t, testOrigin, "")
		if got := resp.Header.Get("Access-Control-Allow-Headers"); got != "" {
			t.Errorf("Access-Control-Allow-Headers = %q with nothing requested; it should be absent, not empty-or-invented", got)
		}
	})
}

// TestSDKServerEnablers_Scenario3_DefaultOffUnchanged is AC3.5.
//
// With no --cors-origins the policy is nil, Middleware returns the handler
// unchanged, and no response carries a CORS header. The default path must be
// byte-identical to before this middleware existed.
func TestSDKServerEnablers_Scenario3_DefaultOffUnchanged(t *testing.T) {
	policy, err := server.NewCORSPolicy(nil)
	if err != nil {
		t.Fatalf("NewCORSPolicy(nil): %v", err)
	}
	if policy != nil {
		t.Fatalf("NewCORSPolicy(nil) = %v, want a nil policy", policy)
	}
	// An all-empty/whitespace list is also "off" — an operator passing an empty
	// value gets the safe default, not an empty allowlist that behaves subtly
	// differently.
	blank, err := server.NewCORSPolicy([]string{"", "   "})
	if err != nil {
		t.Fatalf("NewCORSPolicy(blank): %v", err)
	}
	if blank != nil {
		t.Fatalf("NewCORSPolicy(blank entries) = %v, want a nil policy", blank)
	}

	svc := compatibilityInfoService(t, "", port.ProviderCapabilities{})
	srv := httptest.NewServer(policy.Middleware(server.NewHTTPHandler(svc)))
	defer srv.Close()

	resp := do(t, srv, http.MethodGet, "/v1/compatibility", testOrigin, "")
	for _, h := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Credentials", "Access-Control-Allow-Methods"} {
		if got := resp.Header.Get(h); got != "" {
			t.Errorf("with CORS off, %s = %q, want it absent", h, got)
		}
	}
	if containsVary(resp, "Origin") {
		t.Error("with CORS off, Vary: Origin was added; the default path must be unchanged")
	}
}

// TestSDKServerEnablers_Scenario3_OriginValidationIsStrict pins the startup
// refusals. Each rejected form would otherwise be a silently dead allowlist
// entry — indistinguishable from a working one until a browser quietly fails.
func TestSDKServerEnablers_Scenario3_OriginValidationIsStrict(t *testing.T) {
	bad := []struct{ origin, why string }{
		{"example.com", "no scheme"},
		{"ftp://example.com", "not an http(s) scheme"},
		{"https://", "no host"},
		{"https://example.com/path", "has a path"},
		{"https://example.com/", "trailing slash is a path"},
		{"https://example.com?q=1", "has a query"},
		{"https://example.com#f", "has a fragment"},
		{"https://user@example.com", "has userinfo"},
	}
	for _, b := range bad {
		t.Run(b.origin, func(t *testing.T) {
			if _, err := server.NewCORSPolicy([]string{b.origin}); err == nil {
				t.Errorf("NewCORSPolicy(%q) succeeded; %s", b.origin, b.why)
			}
		})
	}
	good := []string{"https://app.example.com", "http://localhost:5173", "https://example.com:8443"}
	for _, g := range good {
		t.Run(g, func(t *testing.T) {
			p, err := server.NewCORSPolicy([]string{g})
			if err != nil {
				t.Errorf("NewCORSPolicy(%q) = %v, want it accepted", g, err)
			}
			if p == nil {
				t.Errorf("NewCORSPolicy(%q) returned a nil policy", g)
			}
		})
	}
}

// containsVary reports whether the response's Vary header names field.
func containsVary(resp *http.Response, field string) bool {
	for _, v := range resp.Header.Values("Vary") {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), field) {
				return true
			}
		}
	}
	return false
}
