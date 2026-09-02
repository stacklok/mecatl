package main

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestADR_0290_ResourceInputGrammar(t *testing.T) {
	t.Parallel()
	bare, err := parseProtectedResource("api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if bare.Resource != "https://api.example.com" || bare.GRPCTarget != "api.example.com:443" {
		t.Fatalf("bare identity = %#v", bare)
	}
	explicit, err := parseProtectedResource("https://api.example.com/rpc/v1")
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Resource != "https://api.example.com/rpc/v1" || explicit.GRPCTarget != "api.example.com:443" {
		t.Fatalf("explicit identity = %#v", explicit)
	}
	for _, raw := range []string{"http://api.example.com", "https://user@api.example.com", "https://api.example.com?a=b", "https://api.example.com#x", "https://api.example.com/%zz", "https://api.example.com/\u202e", "api.example.com:443", "https://a@b@api.example.com"} {
		if _, err := parseProtectedResource(raw); err == nil {
			t.Errorf("parseProtectedResource(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestADR_0290_ExactResourceBinding(t *testing.T) {
	t.Parallel()
	resource, err := parseProtectedResource("https://api.example.com/service/v1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resource.MetadataURL, "https://api.example.com/.well-known/oauth-protected-resource/service/v1"; got != want {
		t.Fatalf("metadata URL = %q, want %q", got, want)
	}
	root, err := parseProtectedResource("https://api.example.com/")
	if err != nil || root.MetadataURL != "https://api.example.com/.well-known/oauth-protected-resource" {
		t.Fatalf("root metadata URL = %#v, %v", root, err)
	}
	for _, got := range []string{"https://api.example.com", "https://api.example.com/service", "https://api.example.com/service/v1/", "https://api.example.com:443/service/v1", "https://api.example.com/service%2Fv1"} {
		if resourceMatches(resource, got) {
			t.Errorf("near-match %q accepted", got)
		}
	}
	if !resourceMatches(resource, "https://api.example.com/service/v1") {
		t.Fatal("exact resource was rejected")
	}
}

func TestADR_0290_MetadataFetchSecurity(t *testing.T) {
	t.Parallel()
	client := newPublicBootstrapClient()
	defer client.CloseIdleConnections()
	if client.Timeout <= 0 || client.Jar != nil {
		t.Fatalf("bootstrap client is not bounded and anonymous: timeout=%s jar=%v", client.Timeout, client.Jar)
	}
	transport := client.Transport.(anonymousTransport).next.(*http.Transport)
	if transport.Proxy != nil || transport.TLSClientConfig.InsecureSkipVerify || transport.TLSClientConfig.RootCAs != nil || len(transport.TLSClientConfig.Certificates) != 0 {
		t.Fatal("bootstrap transport permits proxying, custom roots, insecure TLS, or client certificates")
	}
	if err := client.CheckRedirect(&http.Request{}, nil); err == nil {
		t.Fatal("bootstrap client accepted redirect")
	}
	request := &http.Request{URL: mustURL(t, "https://api.example.com"), Header: http.Header{"Authorization": {"Bearer secret"}, "Cookie": {"x=y"}}}
	if err := validateAnonymousRequest(request); err == nil {
		t.Fatal("authenticated bootstrap request accepted")
	}
	for _, host := range []string{"127.0.0.1", "::1", "10.0.0.1", "169.254.1.1", "224.0.0.1"} {
		if err := validatePublicAddress(host); err == nil {
			t.Errorf("non-public address %s accepted", host)
		}
	}
}

func TestADR_0290_MetadataBodyBounds(t *testing.T) {
	t.Parallel()
	if err := validateJSONMediaType("application/json; charset=utf-8"); err != nil {
		t.Fatal(err)
	}
	if err := validateJSONMediaType("application/problem+json"); err != nil {
		t.Fatal(err)
	}
	if err := validateJSONMediaType("text/plain"); err == nil {
		t.Fatal("non-JSON content type accepted")
	}
	if _, err := readJSONBody(io.NopCloser(strings.NewReader(`{"resource":"x"} trailing`))); err == nil {
		t.Fatal("trailing JSON data accepted")
	}
	if _, err := readJSONBody(io.NopCloser(strings.NewReader(strings.Repeat("x", maxDiscoveryBodyBytes+1)))); err == nil {
		t.Fatal("over-limit body accepted")
	}
}

func TestADR_0290_ProfileDocumentValidation(t *testing.T) {
	t.Parallel()
	resource, err := parseProtectedResource("https://api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	valid := `{"resource":"https://api.example.com","authorization_servers":["https://issuer.example.com"],"com.stacklok.mecatl.audience":"api","com.stacklok.mecatl.client_id":"client","scopes_supported":["openid"]}`
	if _, err := parseProfileDocument(resource, []byte(valid)); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"resource":"https://api.example.com","authorization_servers":[]}`,
		`{"resource":"https://api.example.com","authorization_servers":["https://a.example","https://b.example"]}`,
		`{"resource":"https://api.example.com","authorization_servers":["http://issuer.example"],"com.stacklok.mecatl.audience":"api","com.stacklok.mecatl.client_id":"client"}`,
		`{"resource":"https://api.example.com","authorization_servers":["https://issuer.example"],"com.stacklok.mecatl.audience":"","com.stacklok.mecatl.client_id":"client"}`,
		`{"resource":"https://api.example.com","authorization_servers":["https://issuer.example"],"com.stacklok.mecatl.audience":"api\u202e","com.stacklok.mecatl.client_id":"client"}`,
		`{"resource":"https://api.example.com","authorization_servers":["https://issuer.example"],"com.stacklok.mecatl.audience":"api","com.stacklok.mecatl.client_id":"client\nforged"}`,
		`{"resource":"https://api.example.com","authorization_servers":["https://issuer.example"],"com.stacklok.mecatl.audience":"api","com.stacklok.mecatl.client_id":"client","scopes_supported":["openid","openid"]}`,
	} {
		if _, err := parseProfileDocument(resource, []byte(body)); err == nil {
			t.Errorf("unsafe profile accepted: %s", body)
		}
	}
}

func TestADR_0290_IssuerBinding(t *testing.T) {
	t.Parallel()
	issuer, err := parseIssuer("https://issuer.example.com/tenant")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateIssuerDocument(issuer, []byte(`{"issuer":"https://issuer.example.com/tenant"}`)); err != nil {
		t.Fatal(err)
	}
	if err := validateIssuerDocument(issuer, []byte(`{"issuer":"https://issuer.example.com/other"}`)); err == nil {
		t.Fatal("issuer mismatch accepted")
	}
}

func TestADR_0290_DiscoveryFailureLeavesNoState(t *testing.T) {
	t.Parallel()
	resource, err := parseProtectedResource("https://api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err = discoverProtectedResource(t.Context(), resource, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("network failure containing bearer secret")
	}))
	if err == nil || calls != 1 {
		t.Fatalf("discovery error = %v, calls = %d", err, calls)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe discovery error = %v", err)
	}
}

func TestADR_0290_MetadataDuplicateFields(t *testing.T) {
	t.Parallel()
	resource, err := parseProtectedResource("https://api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	duplicate := `{"resource":"https://api.example.com","resource":"https://evil.example","authorization_servers":["https://issuer.example"],"com.stacklok.mecatl.audience":"api","com.stacklok.mecatl.client_id":"client"}`
	if _, err := parseProfileDocument(resource, []byte(duplicate)); err == nil {
		t.Fatal("duplicate security field accepted")
	}
	unknown := `{"resource":"https://api.example.com","authorization_servers":["https://issuer.example"],"com.stacklok.mecatl.audience":"api","com.stacklok.mecatl.client_id":"client","future":{"any":"value","note":"resource resource"}}`
	if _, err := parseProfileDocument(resource, []byte(unknown)); err != nil {
		t.Fatalf("unknown non-profile field rejected: %v", err)
	}
}

func TestADR_0290_DiscoveryUsesClientTimeout(t *testing.T) {
	resource, err := parseProtectedResource("https://api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	var sawDeadline bool
	_, err = discoverProtectedResource(t.Context(), resource, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok || time.Until(deadline) > bootstrapTimeout || time.Until(deadline) <= 0 {
			t.Fatalf("discovery request did not inherit client timeout: deadline=%v ok=%v", deadline, ok)
		}
		sawDeadline = true
		if strings.Contains(req.URL.Path, "openid-configuration") {
			return jsonResponse(`{"issuer":"https://issuer.example.com"}`), nil
		}
		return jsonResponse(`{"resource":"https://api.example.com","authorization_servers":["https://issuer.example.com"],"com.stacklok.mecatl.audience":"api","com.stacklok.mecatl.client_id":"client"}`), nil
	}))
	if err != nil || !sawDeadline {
		t.Fatalf("discovery = %v, timeout deadline observed=%v", err, sawDeadline)
	}
}
func TestInvariant_resource_discovery_is_anonymous(t *testing.T) {
	t.Parallel()
	resource, err := parseProtectedResource("https://api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	var requests []*http.Request
	discovered, err := discoverProtectedResource(t.Context(), resource, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.Clone(req.Context()))
		if strings.Contains(req.URL.Path, "openid-configuration") {
			return jsonResponse(`{"issuer":"https://issuer.example.com"}`), nil
		}
		return jsonResponse(`{"resource":"https://api.example.com","authorization_servers":["https://issuer.example.com"],"com.stacklok.mecatl.audience":"api","com.stacklok.mecatl.client_id":"client"}`), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if discovered.Issuer != "https://issuer.example.com" || discovered.Audience != "api" || discovered.ClientID != "client" {
		t.Fatalf("discovered profile = %#v", discovered)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	for _, req := range requests {
		if req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" || req.Header.Get("User-Agent") != "" {
			t.Fatalf("non-anonymous headers sent: %#v", req.Header)
		}
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func jsonResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}
