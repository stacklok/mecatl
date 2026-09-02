package clientauth

import (
	"crypto/tls"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIssuerHTTPClientPrivateRequiresCA(t *testing.T) {
	if _, err := IssuerHTTPClient(t.Context(), IssuerAddressPolicyPrivate, "https://idp.internal", nil); err == nil {
		t.Fatal("private policy built a client with no CA bundle")
	}
}

// A public issuer must verify against system roots, NOT a pinned pool, and must
// not disable verification. RootCAs nil is what selects the system pool.
func TestPublicIssuerClientUsesSystemRootsAndVerifies(t *testing.T) {
	client, err := IssuerHTTPClient(t.Context(), IssuerAddressPolicyPublic, "https://idp.example", nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := transportTLSConfig(t, client)
	if cfg.RootCAs != nil {
		t.Error("public issuer pinned a root pool; want system roots")
	}
	if cfg.InsecureSkipVerify {
		t.Error("public issuer disabled certificate verification")
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %d, want TLS 1.2", cfg.MinVersion)
	}
}

// An operator-supplied bundle REPLACES system roots rather than augmenting them.
func TestPublicIssuerClientCABundleReplacesSystemRoots(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	bundle := certPEMFromServer(t, srv)
	client, err := IssuerHTTPClient(t.Context(), IssuerAddressPolicyPublic, srv.URL, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if transportTLSConfig(t, client).RootCAs == nil {
		t.Fatal("supplied CA bundle did not replace system roots")
	}
	if _, err := IssuerHTTPClient(t.Context(), IssuerAddressPolicyPublic, srv.URL, []byte("not a certificate")); err == nil {
		t.Fatal("a bundle with no certificates was accepted")
	}
}

func TestPublicIssuerClientRefusesRedirectAndNonHTTPS(t *testing.T) {
	client, err := IssuerHTTPClient(t.Context(), IssuerAddressPolicyPublic, "https://idp.example", nil)
	if err != nil {
		t.Fatal(err)
	}
	if client.CheckRedirect == nil {
		t.Fatal("public issuer client follows redirects")
	}
	if err := client.CheckRedirect(nil, nil); err == nil {
		t.Error("redirect was permitted")
	}
	for _, raw := range []string{"http://idp.example/x", "https://user:pw@idp.example/x"} {
		req, reqErr := http.NewRequest(http.MethodGet, raw, nil)
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		if _, err := client.Transport.RoundTrip(req); err == nil || !strings.Contains(err.Error(), "HTTPS without userinfo") {
			t.Errorf("RoundTrip(%q) error = %v, want scheme/userinfo rejection", raw, err)
		}
	}
}

// The dial-layer screen is defence in depth, re-run per connection rather than
// pinned at construction. It delegates to the engine's shared predicate.
func TestRefusePrivateAddress(t *testing.T) {
	tests := []struct {
		addr    string
		refused bool
	}{
		{addr: "8.8.8.8:443"},
		{addr: "[2606:4700:4700::1111]:443"},
		{addr: "127.0.0.1:443", refused: true},
		{addr: "10.0.0.1:443", refused: true},
		{addr: "169.254.169.254:80", refused: true},
		{addr: "100.64.0.1:443", refused: true},
		{addr: "[64:ff9b::a9fe:a9fe]:443", refused: true},
		{addr: "idp.example:443", refused: true}, // never an unresolved name
	}
	for _, test := range tests {
		t.Run(test.addr, func(t *testing.T) {
			err := refusePrivateAddress("tcp", test.addr, nil)
			if refused := err != nil; refused != test.refused {
				t.Errorf("refusePrivateAddress(%q) error = %v, want refused=%t", test.addr, err, test.refused)
			}
		})
	}
}

func transportTLSConfig(t *testing.T, client *http.Client) *tls.Config {
	t.Helper()
	outer, ok := client.Transport.(publicIssuerTransport)
	if !ok {
		t.Fatalf("transport = %T, want publicIssuerTransport", client.Transport)
	}
	inner, ok := outer.next.(*http.Transport)
	if !ok {
		t.Fatalf("inner transport = %T, want *http.Transport", outer.next)
	}
	if inner.TLSClientConfig == nil {
		t.Fatal("no TLS config on the issuer transport")
	}
	if inner.DialContext == nil {
		t.Fatal("issuer transport has no address-screening dialer")
	}
	return inner.TLSClientConfig
}

func certPEMFromServer(t *testing.T, srv *httptest.Server) []byte {
	t.Helper()
	if srv.Certificate() == nil {
		t.Fatal("test server has no certificate")
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
}

// Both registry write paths must normalise. Enroll previously validated only the
// CA path, so a bad policy enrolled successfully and was then quarantined by
// readRows on the next load -- a "successful" login with no visible entry and an
// orphaned refresh token.
func TestBothRegistryWritePathsNormalisePolicy(t *testing.T) {
	id := identity("remote.example:443")
	bad := Connection{Identity: id, IssuerAddressPolicy: IssuerAddressPolicy("Public")}

	registry, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Upsert(bad); err == nil || !strings.Contains(err.Error(), "invalid issuer connection policy") {
		t.Errorf("Upsert error = %v, want invalid issuer connection policy", err)
	}
	err = Enroll(t.Context(), bad, Token{AccessToken: "t"}, EnrollmentConfig{Registry: registry, Credentials: credentials(t)})
	if err == nil || !strings.Contains(err.Error(), "invalid issuer connection policy") {
		t.Errorf("Enroll error = %v, want invalid issuer connection policy", err)
	}

	// An unset policy is still defaulted, not rejected, on both paths.
	legacy := Connection{Identity: id}
	if _, err := registry.Upsert(legacy); err != nil {
		t.Fatalf("Upsert(legacy) = %v, want the private default", err)
	}
	saved, err := registry.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || saved[0].IssuerAddressPolicy != IssuerAddressPolicyPrivate {
		t.Fatalf("saved policy = %#v, want private", saved)
	}
}

// The public transport must NOT confine requests to the issuer's own authority.
// Confining them is what rejected any provider that publishes its token or JWKS
// endpoint on a different host (Google serves oauth2.googleapis.com and
// www.googleapis.com from an accounts.google.com issuer), and the rejection
// surfaced as an opaque ErrTokenExchange. scopedhttps still confines by design;
// that is its own package's contract, tested there.
func TestPublicIssuerTransportDoesNotScopeToIssuerAuthority(t *testing.T) {
	var seen []string
	stub := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = append(seen, req.URL.Host)
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
	})
	transport := publicIssuerTransport{next: stub}

	// The three origins a single OIDC flow touches when a provider splits them.
	for _, raw := range []string{
		"https://accounts.google.com/.well-known/openid-configuration",
		"https://oauth2.googleapis.com/token",
		"https://www.googleapis.com/oauth2/v3/certs",
	} {
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip(%q) = %v, want the request to be forwarded", raw, err)
		}
		_ = resp.Body.Close()
	}
	want := []string{"accounts.google.com", "oauth2.googleapis.com", "www.googleapis.com"}
	if len(seen) != len(want) {
		t.Fatalf("forwarded hosts = %v, want %v", seen, want)
	}
	for i, host := range want {
		if seen[i] != host {
			t.Errorf("forwarded host %d = %q, want %q", i, seen[i], host)
		}
	}
}
