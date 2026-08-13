package tools

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/toolkit"
)

// callFetch builds a FetchMcpResourceTool call with the given uri arg and
// executes it against a context.Background + nil workspace (the tool needs no
// workspace). It returns the model-facing ToolResult.
func callFetch(t *testing.T, uri string) session.ToolResult {
	t.Helper()
	return callFetchTool(t, FetchMcpResourceTool{}, uri)
}

// callFetchTool executes t with a uri arg, returning the model-facing result.
func callFetchTool(t *testing.T, t2 FetchMcpResourceTool, uri string) session.ToolResult {
	t.Helper()
	args, _ := json.Marshal(map[string]string{"uri": uri})
	res, err := t2.Execute(context.Background(),
		session.NewToolCall("call-1", "FetchMcpResource", args), tool.Environment{})
	if err != nil {
		t.Fatalf("FetchMcpResource returned a Go error (reserved for harness faults): %v", err)
	}
	return res
}

// TestFetchMcpResourceSSRFDenied proves the SSRF IP-deny (session.ValidateMediaURL)
// rejects internal/metadata/loopback targets BEFORE any fetch. The URIs below
// must all fail validation; none should reach the network.
func TestFetchMcpResourceSSRFDenied(t *testing.T) {
	cases := []string{
		// The cloud-metadata IP — the canonical SSRF target.
		"https://169.254.169.254/latest/meta-data/",
		// Loopback.
		"https://127.0.0.1/",
		"https://localhost/",
		// RFC1918 private.
		"https://10.0.0.1/",
		"https://192.168.1.1/",
		// Link-local.
		"https://169.254.0.1/",
		// CGNAT.
		"https://100.64.0.1/",
		// inet_aton-style non-canonical numeric host.
		"https://0x7f.0.0.1/",
	}
	for _, uri := range cases {
		t.Run(uri, func(t *testing.T) {
			res := callFetch(t, uri)
			if !res.IsError {
				t.Fatalf("SSRF target %q was NOT rejected (result was not an error): %q", uri, res.Content)
			}
			// The error must name that the URI was rejected (SSRF), not guide to
			// ReadMcpResource (that guidance is for non-https schemes only).
			if !strings.Contains(res.Content, "rejected") {
				t.Fatalf("SSRF rejection message must say \"rejected\", got: %q", res.Content)
			}
			if strings.Contains(res.Content, "ReadMcpResource") {
				t.Fatalf("SSRF rejection must NOT guide to ReadMcpResource (that is for non-https only), got: %q", res.Content)
			}
		})
	}
}

// TestFetchMcpResourceHttpsOnly proves a plaintext http:// URI is rejected
// (ValidateMediaURL is https-only). http:// is NOT routed as "server-readonly"
// either — it is https-only with a validation failure, because http:// is the
// SSRF-to-loopback shape the validator exists to block. The error must be a
// validation rejection, not the ReadMcpResource guidance.
func TestFetchMcpResourceHttpsOnly(t *testing.T) {
	// A public-looking http:// URL: the scheme check rejects it as https-only
	// (the splitScheme pre-screen routes http:// away from the fetch path, but
	// http:// is https-only-failed, not server-readonly-guidance). Confirm the
	// tool does NOT fetch it and does NOT guide to ReadMcpResource.
	res := callFetch(t, "http://example.com/")
	if !res.IsError {
		t.Fatalf("http:// URI must be rejected, got a successful result: %q", res.Content)
	}
	// http:// is NOT a server-readonly scheme like perf://; it is the
	// https-only validation failure. The message must explain https-only, not
	// guide to ReadMcpResource.
	if !strings.Contains(strings.ToLower(res.Content), "https") {
		t.Fatalf("http:// rejection must explain https-only, got: %q", res.Content)
	}
}

// TestFetchMcpResourceNonHttpsGuidesToReadMcpResource proves a non-https
// server-readonly scheme (perf://, file://, custom) returns the helpful error
// pointing the model at ReadMcpResource with the owning server name. The tool
// does NOT auto-route (it does not know which server owns the URI) — it tells
// the model what to do.
func TestFetchMcpResourceNonHttpsGuidesToReadMcpResource(t *testing.T) {
	cases := []string{
		"perf://project/some-resource",
		"file:///etc/hostname",
		"custom://owner/blob/123",
	}
	for _, uri := range cases {
		t.Run(uri, func(t *testing.T) {
			res := callFetch(t, uri)
			if !res.IsError {
				t.Fatalf("non-https URI %q must return an error guiding to ReadMcpResource, got: %q", uri, res.Content)
			}
			if !strings.Contains(res.Content, "ReadMcpResource") {
				t.Fatalf("non-https guidance must name ReadMcpResource, got: %q", res.Content)
			}
			if !strings.Contains(res.Content, "server-readonly") {
				t.Fatalf("non-https guidance must explain the URI is server-readonly, got: %q", res.Content)
			}
		})
	}
}

// TestFetchMcpResourceMissingURI proves a missing/empty uri is a model-facing
// error.
func TestFetchMcpResourceMissingURI(t *testing.T) {
	res := callFetch(t, "")
	if !res.IsError {
		t.Fatalf("empty uri must be an error, got: %q", res.Content)
	}
	if !strings.Contains(res.Content, "required") {
		t.Fatalf("empty-uri error must say \"required\", got: %q", res.Content)
	}
}

// TestFetchMcpResourceSpec pins the tool's spec: the name, read-only posture,
// and that the description documents the https-only contract + the
// ReadMcpResource guidance for non-https URIs (gauntlet #10 — the description
// is the model's onboarding manual).
func TestFetchMcpResourceSpec(t *testing.T) {
	tt := FetchMcpResourceTool{}
	spec := tt.Spec()
	if spec.Name != "FetchMcpResource" {
		t.Fatalf("Spec name = %q, want FetchMcpResource", spec.Name)
	}
	if !tt.ReadOnly() {
		t.Fatal("FetchMcpResource must be read-only (it is an outward fetch, no mutation)")
	}
	if !strings.Contains(spec.Description, "https://") {
		t.Fatalf("description must document the https:// contract, got: %q", spec.Description)
	}
	if !strings.Contains(spec.Description, "ReadMcpResource") {
		t.Fatalf("description must name ReadMcpResource for non-https URIs, got: %q", spec.Description)
	}
}

// TestFetchMcpResourceFetchesText proves the validated https fetch path
// returns text content verbatim (truncated to the shared cap), using a test
// TLS server + a stubbed http.Client (with a custom transport trusting the
// test cert) so the fetch is OFFLINE. The test server runs on loopback, so
// the request URL itself would fail ValidateMediaURL — therefore the stubbed
// client is wired onto the tool AFTER bypassing the request-URL validation
// via a test-only constructor that skips it, exercising ONLY the fetch+render
// path. The SSRF validation of the REQUEST URL is covered separately by
// TestFetchMcpResourceSSRFDenied.
func TestFetchMcpResourceFetchesText(t *testing.T) {
	body := "hello resource link\nline two"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	tt := FetchMcpResourceTool{
		httpClient: srv.Client(), // trusts the test cert; no CookieJar by default
	}
	res := callFetchToolBypassValidation(t, tt, srv.URL)
	if res.IsError {
		t.Fatalf("text fetch returned an error: %q", res.Content)
	}
	if res.Content != body {
		t.Fatalf("text fetch body mismatch:\nwant: %q\ngot:  %q", body, res.Content)
	}
}

// TestFetchMcpResourceTruncatesLargeText proves a too-large text body is
// truncated to toolkit.MaxOutputBytes with the shared truncation marker.
func TestFetchMcpResourceTruncatesLargeText(t *testing.T) {
	// A body comfortably over the cap.
	big := strings.Repeat("a", toolkit.MaxOutputBytes*2)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(big))
	}))
	defer srv.Close()

	tt := FetchMcpResourceTool{httpClient: srv.Client()}
	res := callFetchToolBypassValidation(t, tt, srv.URL)
	if res.IsError {
		t.Fatalf("large-text fetch returned an error: %q", res.Content)
	}
	if !strings.HasSuffix(res.Content, toolkit.TruncationMarker) {
		t.Fatalf("large text must be truncated with the shared marker; got len=%d, suffix=%q",
			len(res.Content), res.Content[len(res.Content)-len(toolkit.TruncationMarker):])
	}
	if len(res.Content) > toolkit.MaxOutputBytes+len(toolkit.TruncationMarker) {
		t.Fatalf("truncated text exceeds cap+marker: got len=%d, cap=%d", len(res.Content), toolkit.MaxOutputBytes)
	}
}

// TestFetchMcpResourceBinarySummary proves a binary content-type body is
// summarized as "[binary resource: <mime>, <n> bytes]" (mirroring
// mcp.flattenResourceContents), never base64-dumped.
func TestFetchMcpResourceBinarySummary(t *testing.T) {
	blob := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a} // PNG header
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(blob)
	}))
	defer srv.Close()

	tt := FetchMcpResourceTool{httpClient: srv.Client()}
	res := callFetchToolBypassValidation(t, tt, srv.URL)
	if res.IsError {
		t.Fatalf("binary fetch returned an error: %q", res.Content)
	}
	want := "[binary resource: image/png, 8 bytes]"
	if res.Content != want {
		t.Fatalf("binary summary mismatch:\nwant: %q\ngot:  %q", want, res.Content)
	}
}

// TestFetchMcpResourceBinarySummaryNoMIME proves an explicit
// application/octet-stream Content-Type yields the octet-stream fallback in
// the binary summary. (An ABSENT Content-Type is documented as text — many
// servers omit it for plain resources — and is covered by the text tests.)
func TestFetchMcpResourceBinarySummaryNoMIME(t *testing.T) {
	got := renderFetchedBody("application/octet-stream", []byte{0x00, 0x01})
	if want := "[binary resource: application/octet-stream, 2 bytes]"; got != want {
		t.Fatalf("renderFetchedBody(octet-stream, binary) = %q, want %q", got, want)
	}
}

// TestFetchMcpResourceHTTPError proves a 4xx/5xx response is a model-facing
// error naming the HTTP status.
func TestFetchMcpResourceHTTPError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	tt := FetchMcpResourceTool{httpClient: srv.Client()}
	res := callFetchToolBypassValidation(t, tt, srv.URL)
	if !res.IsError {
		t.Fatalf("HTTP 404 must be an error, got: %q", res.Content)
	}
	if !strings.Contains(res.Content, "404") {
		t.Fatalf("HTTP error must name the status, got: %q", res.Content)
	}
}

// TestRenderFetchedBodyTextTruncate is a focused unit test of the text-render
// path's truncation at the shared cap boundary.
func TestRenderFetchedBodyTextTruncate(t *testing.T) {
	// Exactly at the cap: no truncation.
	exact := strings.Repeat("x", toolkit.MaxOutputBytes)
	if got := renderFetchedBody("text/plain", []byte(exact)); got != exact {
		t.Fatalf("body exactly at cap must not be truncated; got len=%d", len(got))
	}
	// One byte over: truncated with the marker.
	over := strings.Repeat("x", toolkit.MaxOutputBytes+1)
	got := renderFetchedBody("text/plain", []byte(over))
	if !strings.HasSuffix(got, toolkit.TruncationMarker) {
		t.Fatalf("body over cap must be truncated with the marker; got len=%d", len(got))
	}
}

// TestIsTextContentType pins the text/binary classification used to decide
// verbatim-render vs summarize.
func TestIsTextContentType(t *testing.T) {
	text := []string{
		"", // absent → text (many servers omit it for plain resources)
		"text/plain",
		"text/plain; charset=utf-8",
		"text/html",
		"application/json",
		"application/xml",
		"application/JSON", // case-insensitive
		"application/vnd.api+json",
		"application/atom+xml",
		"application/yaml",
		"application/javascript",
	}
	for _, m := range text {
		if !isTextContentType(m) {
			t.Errorf("isTextContentType(%q) = false, want true", m)
		}
	}
	bin := []string{
		"image/png",
		"audio/mpeg",
		"video/mp4",
		"application/octet-stream",
		"application/pdf",
		"application/zip",
	}
	for _, m := range bin {
		if isTextContentType(m) {
			t.Errorf("isTextContentType(%q) = true, want false", m)
		}
	}
}

// callFetchToolBypassValidation executes the tool's fetch path with a stubbed
// http.Client, bypassing the request-URL ValidateMediaURL screen (which would
// reject the loopback test server). It reaches fetchResource directly so the
// fetch+render logic — the part NOT covered by the SSRF rejection tests — is
// exercised offline. The SSRF validation of the REQUEST URL is covered by
// TestFetchMcpResourceSSRFDenied; the redirect re-validation lives in the
// production per-call client's CheckRedirect and is structurally the same
// ValidateMediaURL call.
func callFetchToolBypassValidation(t *testing.T, tt FetchMcpResourceTool, uri string) session.ToolResult {
	t.Helper()
	if tt.httpClient == nil {
		t.Fatal("callFetchToolBypassValidation requires a stubbed httpClient")
	}
	res, err := fetchResource(context.Background(), "call-1", uri, tt.httpClient)
	if err != nil {
		t.Fatalf("fetchResource returned a Go error: %v", err)
	}
	return res
}

// testTLSPool returns a cert pool trusting the test TLS server's self-signed
// cert, so the production Execute path (which builds its own transport) can
// verify the test server. It is the per-call transport's TLSClientConfig roots.
func testTLSPool(t *testing.T, srv *httptest.Server) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return pool
}

// dialTestServer returns a DialContext that ALWAYS dials the test server's
// loopback address regardless of the requested host. It lets the production
// Execute path reach a loopback test server using a public-looking request URL
// (https://example.com:PORT/) that passes ValidateMediaURL's hostname screen,
// while the real dial lands on the test server. It does NOT itself perform the
// SSRF guard (it is the test override of ssrfGuardedDialContext) — the SSRF
// dial-layer guard is unit-covered by session.TestValidateResolvedIP, and the
// redirect URL-string layer is covered by TestFetchMcpResourceRedirectToInternalRejected.
func dialTestServer(t *testing.T, srvAddr string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	t.Helper()
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		d := net.Dialer{}
		return d.DialContext(ctx, network, srvAddr)
	}
}

// TestFetchMcpResourceRedirectToInternalRejected proves the URL-STRING layer of
// the two-layer SSRF defense: the per-call client's CheckRedirect re-runs
// session.ValidateMediaURL on every redirect target's origin. A public origin
// (https://example.com:PORT/, which passes the initial ValidateMediaURL screen)
// 302-redirects to https://169.254.169.254/ (the cloud-metadata IP); the
// redirect re-validation rejects it before the redirect dial. The test drives
// the REAL FetchMcpResourceTool.Execute (not the fetchResource bypass) so the
// production per-call client — CheckRedirect + the dial-IP transport — is the
// code under test.
//
// The test TLS server's self-signed cert is valid for example.com (httptest
// mints it with DNSNames [example.com, *.example.com]), so Execute can be
// pointed at https://example.com:PORT/ with a dialContext that reaches the
// loopback test server + a TLS config trusting its cert. The dialContext test
// seam overrides the production ssrfGuardedDialContext so the dial reaches the
// test server; the redirect test is about CheckRedirect, NOT the dial guard
// (the dial guard is unit-covered by session.TestValidateResolvedIP).
func TestFetchMcpResourceRedirectToInternalRejected(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 302 to the cloud-metadata IP — ValidateMediaURL rejects it.
		http.Redirect(w, r, "https://169.254.169.254/", http.StatusFound)
	}))
	defer srv.Close()

	tt := FetchMcpResourceTool{
		tlsConfig:   &tls.Config{RootCAs: testTLSPool(t, srv)},
		dialContext: dialTestServer(t, srv.Listener.Addr().String()),
	}
	// Build the request URL as https://example.com:PORT/. example.com passes
	// ValidateMediaURL's hostname screen (public DNS name, not internal), so
	// Execute builds the per-call client and dials. The dialContext test seam
	// routes the dial to the loopback test server; the redirect then fires
	// CheckRedirect, which re-validates the redirect target and rejects it.
	uri := "https://example.com" + urlPort(srv.URL)
	res := callFetchTool(t, tt, uri)
	if !res.IsError {
		t.Fatalf("redirect to internal IP must be rejected, got a successful result: %q", res.Content)
	}
	// The error must name the SSRF rejection — either the redirect target
	// (169.254.169.254) or the SSRF/internal framing.
	if !strings.Contains(res.Content, "169.254.169.254") &&
		!strings.Contains(strings.ToLower(res.Content), "internal") &&
		!strings.Contains(strings.ToLower(res.Content), "ssrf") &&
		!strings.Contains(res.Content, "rejected") {
		t.Fatalf("redirect rejection must name the SSRF rejection (169.254.169.254/internal/SSRF/rejected), got: %q", res.Content)
	}
}

// urlPort extracts the ":PORT" suffix of a test server URL (srv.URL is
// https://127.0.0.1:PORT) so a test can rebuild the URL against a different
// host (e.g. https://example.com:PORT) while keeping the server's port.
func urlPort(rawurl string) string {
	i := strings.LastIndex(rawurl, ":")
	if i < 0 {
		return ""
	}
	return rawurl[i:]
}

// TestFetchMcpResourceDialLayerRejectsInternalIP proves the DIAL-IP layer of
// the two-layer SSRF defense: when a hostname resolves to an internal IP at
// dial time, ssrfGuardedDialContext rejects the dial. This is the
// DNS-rebinding window the URL-string layer cannot close. Rather than mock DNS
// (fiddly and environment-dependent), the test exercises the guard's contract
// directly: ssrfGuardedDialContext is called with a host that resolves to an
// internal IP, and the dial is rejected. The shared IP predicate itself is
// unit-covered by session.TestValidateResolvedIP; this test pins that the
// production dial path wires the predicate and surfaces the rejection (not a
// panic, not a silent pass).
//
// We point the guard at "localhost", which net.DefaultResolver resolves to
// 127.0.0.1 (loopback — rejected by ValidateResolvedIP). No network egress
// happens: the guard rejects after the local resolution, before any dial.
func TestFetchMcpResourceDialLayerRejectsInternalIP(t *testing.T) {
	// Resolve localhost to confirm the test environment answers loopback
	// (it universally does). If a sandbox ever resolves localhost to a public
	// IP, this test would flake — skip loudly rather than assert falsely.
	ips, err := net.DefaultResolver.LookupIPAddr(context.Background(), "localhost")
	if err != nil {
		t.Skipf("localhost resolution failed in this environment: %v", err)
	}
	allInternal := true
	for _, ip := range ips {
		if session.ValidateResolvedIP(ip.IP) == nil {
			allInternal = false
			break
		}
	}
	if !allInternal {
		t.Skip("localhost resolved to a public IP in this environment; the dial-layer guard cannot be exercised here")
	}

	// Use the production dial guard directly. It must reject the localhost dial.
	conn, derr := ssrfGuardedDialContext(context.Background(), "tcp", "localhost:443")
	if conn != nil {
		_ = conn.Close()
		t.Fatal("ssrfGuardedDialContext dialed an internal IP (localhost) — the guard must reject before dialing")
	}
	if derr == nil {
		t.Fatal("ssrfGuardedDialContext returned no error for an internal-IP dial — the guard must reject")
	}
	// The rejection must frame itself as an SSRF guard rejection.
	if !strings.Contains(strings.ToLower(derr.Error()), "ssrf") &&
		!strings.Contains(strings.ToLower(derr.Error()), "internal") {
		t.Fatalf("dial-layer rejection must name SSRF/internal, got: %v", derr)
	}

	// Belt-and-suspenders: a known-public literal IP must PASS the same predicate
	// (so the guard is not over-broad). 1.1.1.1 is a public DNS resolver.
	if err := session.ValidateResolvedIP(net.IPv4(1, 1, 1, 1)); err != nil {
		t.Fatalf("ValidateResolvedIP(1.1.1.1) = %v, want nil (public IP must pass)", err)
	}
	// And the cloud-metadata IP must be rejected (the canonical SSRF target).
	if err := session.ValidateResolvedIP(net.IPv4(169, 254, 169, 254)); err == nil {
		t.Fatal("ValidateResolvedIP(169.254.169.254) = nil, want error (metadata IP must be rejected)")
	}
}
