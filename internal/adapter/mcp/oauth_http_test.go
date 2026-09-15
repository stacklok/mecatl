package mcp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/port"
)

func oauthDialContext(origin string) context.Context {
	return context.WithValue(context.Background(), oauthOriginContextKey{}, origin)
}

func TestOAuthHTTPResolvePinRejectsMixedAndRebindingAnswers(t *testing.T) {
	origin := "https://oauth.example"
	var lookups, dials atomic.Int32
	transport := &oauthHTTPTransport{
		origins: map[string]struct{}{origin: {}},
		private: map[string]struct{}{},
		lookup: func(context.Context, string, string) ([]netip.Addr, error) {
			lookups.Add(1)
			return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("169.254.169.254")}, nil
		},
		dial: func(context.Context, string, string) (net.Conn, error) {
			dials.Add(1)
			return nil, errors.New("must not dial")
		},
	}
	if _, err := transport.dialContext(oauthDialContext(origin), "tcp", "oauth.example:443"); !errors.Is(err, ErrOAuthUnavailable) {
		t.Fatalf("mixed DNS error = %v", err)
	}
	if lookups.Load() != 1 || dials.Load() != 0 {
		t.Fatalf("lookups=%d dials=%d, want one validation lookup and no dial", lookups.Load(), dials.Load())
	}

	lookups.Store(0)
	transport.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		lookups.Add(1)
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	transport.dial = func(_ context.Context, _ string, address string) (net.Conn, error) {
		if address != "8.8.8.8:443" {
			t.Fatalf("dial address = %q", address)
		}
		left, right := net.Pipe()
		_ = right.Close()
		return left, nil
	}
	conn, err := transport.dialContext(oauthDialContext(origin), "tcp", "oauth.example:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if lookups.Load() != 1 {
		t.Fatalf("DNS lookups = %d, want exactly one before pinned dial", lookups.Load())
	}
}

func TestOAuthHTTPPrivateOriginOptInIsExact(t *testing.T) {
	privateOrigin := "http://internal.example:8080"
	transport := &oauthHTTPTransport{
		origins: map[string]struct{}{privateOrigin: {}, "http://other.example:8080": {}},
		private: map[string]struct{}{privateOrigin: {}},
		lookup: func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("10.0.0.8")}, nil
		},
		dial: func(_ context.Context, _ string, _ string) (net.Conn, error) {
			left, right := net.Pipe()
			_ = right.Close()
			return left, nil
		},
	}
	conn, err := transport.dialContext(oauthDialContext(privateOrigin), "tcp", "internal.example:8080")
	if err != nil {
		t.Fatalf("explicit private origin: %v", err)
	}
	_ = conn.Close()
	if _, err := transport.dialContext(oauthDialContext("http://other.example:8080"), "tcp", "other.example:8080"); !errors.Is(err, ErrOAuthUnavailable) {
		t.Fatalf("non-opted private origin error = %v", err)
	}
}

func TestOAuthHTTPLoopbackHTTPRequiresLoopbackDNSAnswer(t *testing.T) {
	origin := "http://localhost:8080"
	transport := &oauthHTTPTransport{
		origins: map[string]struct{}{origin: {}}, private: map[string]struct{}{}, allowLoopback: true,
		lookup: func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		},
		dial: func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("non-loopback answer must not be dialed")
			return nil, nil
		},
	}
	if _, err := transport.dialContext(oauthDialContext(origin), "tcp", "localhost:8080"); !errors.Is(err, ErrOAuthUnavailable) {
		t.Fatalf("rebound localhost error = %v", err)
	}
	transport.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	transport.dial = func(context.Context, string, string) (net.Conn, error) {
		left, right := net.Pipe()
		_ = right.Close()
		return left, nil
	}
	conn, err := transport.dialContext(oauthDialContext(origin), "tcp", "localhost:8080")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
}

func TestOAuthHTTPOriginRedirectAndProxyPolicy(t *testing.T) {
	store := newOAuthMemoryStore(t)
	opts := testOAuthOptions(store)
	client, transport, err := newOAuthHTTPClient("https://mcp.example/mcp", opts)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	if transport.base.Proxy != nil {
		t.Fatal("OAuth transport inherited a proxy")
	}

	req, _ := http.NewRequest(http.MethodGet, "https://attacker.example/token", nil)
	if _, err := transport.RoundTrip(req); !errors.Is(err, ErrOAuthUnavailable) {
		t.Fatalf("disallowed origin error = %v", err)
	}

	policy := oauthRedirectPolicy(2)
	first, _ := http.NewRequest(http.MethodGet, "https://issuer.example/start", nil)
	same, _ := http.NewRequest(http.MethodGet, "https://issuer.example/next", nil)
	if err := policy(same, []*http.Request{first}); err != nil {
		t.Fatalf("safe same-origin GET redirect: %v", err)
	}
	cross, _ := http.NewRequest(http.MethodGet, "https://attacker.example/next", nil)
	if err := policy(cross, []*http.Request{first}); !errors.Is(err, ErrOAuthUnavailable) {
		t.Fatalf("cross-origin redirect error = %v", err)
	}
	post, _ := http.NewRequest(http.MethodPost, "https://issuer.example/token", nil)
	if err := policy(same, []*http.Request{post}); !errors.Is(err, ErrOAuthUnavailable) {
		t.Fatalf("POST redirect error = %v", err)
	}
	first.Header.Set("Authorization", "Basic canary")
	if err := policy(same, []*http.Request{first}); !errors.Is(err, ErrOAuthUnavailable) {
		t.Fatalf("credential redirect error = %v", err)
	}
	third, _ := http.NewRequest(http.MethodGet, "https://issuer.example/third", nil)
	if err := policy(third, []*http.Request{first, same, third}); !errors.Is(err, ErrOAuthUnavailable) {
		t.Fatalf("redirect cap error = %v", err)
	}
}

func TestOAuthHTTPEgressSeparatesDiscoveryFromCredentials(t *testing.T) {
	var resourceHits, additionalHits, issuerHits atomic.Int32
	resource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resourceHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer resource.Close()
	additional := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		additionalHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer additional.Close()
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		issuerHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer issuer.Close()

	opts := testOAuthOptions(newOAuthMemoryStore(t))
	opts.Issuer = issuer.URL
	opts.Client.Preregistered.Issuer = issuer.URL
	opts.Network.AdditionalOrigins = []string{additional.URL}
	opts.Network.PrivateOrigins = []string{resource.URL, additional.URL, issuer.URL}
	client, _, err := newOAuthHTTPClient(resource.URL+"/mcp", opts)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()

	resp, err := client.Get(resource.URL + "/.well-known/oauth-protected-resource")
	if err != nil {
		t.Fatalf("resource metadata GET: %v", err)
	}
	_ = resp.Body.Close()
	if resourceHits.Load() != 1 {
		t.Fatalf("resource discovery hits = %d, want 1", resourceHits.Load())
	}

	assertBlocked := func(name, target, body, authorization string) {
		t.Helper()
		req, reqErr := http.NewRequest(http.MethodPost, target, strings.NewReader(body))
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		if _, requestErr := client.Do(req); !errors.Is(requestErr, ErrOAuthUnavailable) {
			t.Fatalf("%s error = %v, want unavailable", name, requestErr)
		}
	}
	assertBlocked("resource code", resource.URL+"/token", "grant_type=authorization_code&code=code-canary", "Basic secret-canary")
	assertBlocked("additional refresh", additional.URL+"/token", "grant_type=refresh_token&refresh_token=refresh-canary", "Basic secret-canary")
	bearerReq, err := http.NewRequest(http.MethodGet, additional.URL+"/metadata", nil)
	if err != nil {
		t.Fatal(err)
	}
	bearerReq.Header.Set("Authorization", "Bearer access-token-canary")
	if _, err := client.Do(bearerReq); !errors.Is(err, ErrOAuthUnavailable) {
		t.Fatalf("additional bearer error = %v, want unavailable", err)
	}
	assertBlocked("client_secret_post", issuer.URL+"/token", "grant_type=authorization_code&code=code-canary&client_secret=secret-canary", "")
	assertBlocked("confidential without Basic", issuer.URL+"/token", "grant_type=authorization_code&code=code-canary", "")
	if resourceHits.Load() != 1 || additionalHits.Load() != 0 || issuerHits.Load() != 0 {
		t.Fatalf("credential canary reached network: resource=%d additional=%d issuer=%d", resourceHits.Load(), additionalHits.Load(), issuerHits.Load())
	}
}

func TestOAuthHTTPExpectedIssuerAcceptsClientSecretBasic(t *testing.T) {
	var hits atomic.Int32
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		user, secret, ok := r.BasicAuth()
		if !ok || user != "client-id" || secret != testClientSecretCanary {
			t.Error("issuer did not receive the expected client_secret_basic credential")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "code-canary" {
			t.Errorf("issuer form = %v, parse error = %v", r.Form, err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer issuer.Close()

	opts := testOAuthOptions(newOAuthMemoryStore(t))
	opts.Issuer = issuer.URL
	opts.Client.Preregistered.Issuer = issuer.URL
	opts.Network.PrivateOrigins = []string{issuer.URL}
	client, _, err := newOAuthHTTPClient("https://mcp.example/mcp", opts)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	req, _ := http.NewRequest(http.MethodPost, issuer.URL+"/token", strings.NewReader("grant_type=authorization_code&code=code-canary"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("client-id", testClientSecretCanary)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("expected-issuer token POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || hits.Load() != 1 {
		t.Fatalf("status=%d issuer hits=%d, want 204/1", resp.StatusCode, hits.Load())
	}
}

func TestOAuthHTTPRestoredBearerRequiresExactPrivateOptInAndBypassesProxy(t *testing.T) {
	var resourceHits, issuerHits, proxyHits atomic.Int32
	resource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resourceHits.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer restored-canary" {
			t.Errorf("resource Authorization = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer resource.Close()
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		issuerHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer issuer.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits.Add(1)
		http.Error(w, "proxy must not be used", http.StatusBadGateway)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")

	resourcePort := strings.TrimPrefix(resource.URL, "http://127.0.0.1:")
	resourceURL := "http://private.example:" + resourcePort + "/mcp"
	store := newOAuthMemoryStore(t)
	opts := testOAuthOptions(store)
	opts.Issuer = issuer.URL
	opts.Client.Preregistered.Issuer = issuer.URL
	seed, err := NewOAuthController(context.Background(), resourceURL, opts)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testOAuthConfig(issuer.URL + "/token")
	if _, err := seed.state.newTokenSource(context.Background(), cfg, validOAuthToken("restored-canary", "refresh-canary")); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	request := func(controller *OAuthController) error {
		controller.transport.lookup = func(_ context.Context, _, host string) ([]netip.Addr, error) {
			if host == "private.example" {
				return []netip.Addr{netip.MustParseAddr("10.0.0.8")}, nil
			}
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		controller.transport.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
			if strings.HasPrefix(address, "10.0.0.8:") {
				address = resource.Listener.Addr().String()
			}
			return (&net.Dialer{}).DialContext(ctx, network, address)
		}
		source, err := controller.TokenSource(context.Background())
		if err != nil {
			return err
		}
		client := oauth2.NewClient(context.Background(), source)
		client.Transport.(*oauth2.Transport).Base = newMCPHTTPClient(ServerConfig{URL: resourceURL, OAuth: &opts}, controller).Transport
		req, _ := http.NewRequest(http.MethodPost, resourceURL, nil)
		resp, err := client.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		return err
	}

	denied, err := NewOAuthController(context.Background(), resourceURL, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := request(denied); !errors.Is(err, ErrOAuthUnavailable) {
		t.Fatalf("non-opted resource error = %v, want unavailable", err)
	}
	_ = denied.Close()
	if resourceHits.Load() != 0 || proxyHits.Load() != 0 {
		t.Fatalf("denied request reached resource/proxy: resource=%d proxy=%d", resourceHits.Load(), proxyHits.Load())
	}

	opts.Network.PrivateOrigins = []string{"http://private.example:" + resourcePort}
	allowed, err := NewOAuthController(context.Background(), resourceURL, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer allowed.Close()
	if err := request(allowed); err != nil {
		t.Fatalf("exact-opted resource request: %v", err)
	}
	tokenReq, _ := http.NewRequest(http.MethodPost, issuer.URL+"/token", strings.NewReader("grant_type=authorization_code&code=x"))
	tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenReq.SetBasicAuth("client-id", testClientSecretCanary)
	resp, err := allowed.client.Do(tokenReq)
	if err != nil {
		t.Fatalf("issuer request: %v", err)
	}
	_ = resp.Body.Close()
	if resourceHits.Load() != 1 || issuerHits.Load() != 1 || proxyHits.Load() != 0 {
		t.Fatalf("hits resource=%d issuer=%d proxy=%d, want 1/1/0", resourceHits.Load(), issuerHits.Load(), proxyHits.Load())
	}
}

func TestOAuthHTTPPrivateOptInAllowsOnlyRFC1918AndULAWithoutUnsafeDial(t *testing.T) {
	origin := "https://oauth.example"
	for _, test := range []struct {
		name      string
		addr      string
		wantDials int32
	}{
		{name: "RFC1918", addr: "10.0.0.8", wantDials: 1},
		{name: "ULA", addr: "fd00::8", wantDials: 1},
		{name: "loopback", addr: "127.0.0.1"},
		{name: "IPv6 loopback", addr: "::1"},
		{name: "link-local", addr: "169.254.1.1"},
		{name: "cloud metadata", addr: "169.254.169.254"},
		{name: "unspecified", addr: "0.0.0.0"},
		{name: "IPv6 unspecified", addr: "::"},
		{name: "multicast", addr: "224.0.0.1"},
		{name: "IPv6 multicast", addr: "ff02::1"},
		{name: "public", addr: "192.0.2.8"},
		{name: "mapped RFC1918", addr: "::ffff:10.0.0.8"},
		{name: "mapped metadata", addr: "::ffff:169.254.169.254"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var dials atomic.Int32
			transport := &oauthHTTPTransport{
				origins: map[string]struct{}{origin: {}}, private: map[string]struct{}{origin: {}},
				lookup: func(context.Context, string, string) ([]netip.Addr, error) {
					return []netip.Addr{netip.MustParseAddr(test.addr)}, nil
				},
				dial: func(context.Context, string, string) (net.Conn, error) {
					dials.Add(1)
					return nil, errors.New("injected dial stop")
				},
			}
			_, err := transport.dialContext(oauthDialContext(origin), "tcp", "oauth.example:443")
			if !errors.Is(err, ErrOAuthUnavailable) {
				t.Fatalf("error = %v, want unavailable", err)
			}
			if got := dials.Load(); got != test.wantDials {
				t.Fatalf("dials = %d, want %d", got, test.wantDials)
			}
		})
	}

	t.Run("mixed private and unsafe answer rejects before dial", func(t *testing.T) {
		var dials atomic.Int32
		transport := &oauthHTTPTransport{
			origins: map[string]struct{}{origin: {}}, private: map[string]struct{}{origin: {}},
			lookup: func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("10.0.0.8"), netip.MustParseAddr("169.254.169.254")}, nil
			},
			dial: func(context.Context, string, string) (net.Conn, error) {
				dials.Add(1)
				return nil, errors.New("must not dial")
			},
		}
		if _, err := transport.dialContext(oauthDialContext(origin), "tcp", "oauth.example:443"); !errors.Is(err, ErrOAuthUnavailable) {
			t.Fatalf("error = %v, want unavailable", err)
		}
		if dials.Load() != 0 {
			t.Fatalf("mixed DNS answer caused %d dials", dials.Load())
		}
	})
}

func TestOAuthHTTPRejectsUnsafeSingleDNSAnswersWithoutDial(t *testing.T) {
	origin := "https://oauth.example"
	for _, test := range []struct {
		name string
		addr string
	}{
		{"private", "10.0.0.8"},
		{"loopback", "127.0.0.1"},
		{"link-local", "169.254.1.1"},
		{"aws metadata", "169.254.169.254"},
		{"azure metadata", "168.63.129.16"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var dials atomic.Int32
			transport := &oauthHTTPTransport{
				origins: map[string]struct{}{origin: {}}, private: map[string]struct{}{},
				lookup: func(context.Context, string, string) ([]netip.Addr, error) {
					return []netip.Addr{netip.MustParseAddr(test.addr)}, nil
				},
				dial: func(context.Context, string, string) (net.Conn, error) {
					dials.Add(1)
					return nil, errors.New("must not dial")
				},
			}
			if _, err := transport.dialContext(oauthDialContext(origin), "tcp", "oauth.example:443"); !errors.Is(err, ErrOAuthUnavailable) {
				t.Fatalf("error = %v, want unavailable", err)
			}
			if dials.Load() != 0 {
				t.Fatalf("unsafe answer caused %d dials", dials.Load())
			}
		})
	}
}

func TestOAuthHTTPDNSCancellationAndClientTimeout(t *testing.T) {
	origin := "https://oauth.example"
	transport := &oauthHTTPTransport{
		origins: map[string]struct{}{origin: {}}, private: map[string]struct{}{},
		lookup: func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(oauthDialContext(origin))
	cancel()
	if _, err := transport.dialContext(ctx, "tcp", "oauth.example:443"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled DNS error = %v", err)
	}

	store := newOAuthMemoryStore(t)
	opts := testOAuthOptions(store)
	opts.Timeout = 17 * time.Millisecond
	client, _, err := newOAuthHTTPClient("https://mcp.example/mcp", opts)
	if err != nil {
		t.Fatal(err)
	}
	if client.Timeout != opts.Timeout {
		t.Fatalf("client timeout = %v, want %v", client.Timeout, opts.Timeout)
	}
}

func TestMCPOAuthResourceTransportRejectsCleartextWithoutExactPrivateOptIn(t *testing.T) {
	store := newOAuthMemoryStore(t)
	opts := testOAuthOptions(store)
	resource := "http://private.example:8080/mcp"
	controller, err := NewOAuthController(context.Background(), resource, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	client := newMCPHTTPClient(ServerConfig{URL: resource, OAuth: &opts}, controller)
	req, _ := http.NewRequest(http.MethodPost, resource, nil)
	if _, err := client.Transport.RoundTrip(req); !errors.Is(err, ErrOAuthUnavailable) {
		t.Fatalf("cleartext private resource error = %v, want unavailable", err)
	}
	if controller.transport.base.Proxy != nil {
		t.Fatal("resource transport inherited an environment proxy")
	}

	opts.Network.PrivateOrigins = []string{"http://private.example:8080"}
	optedIn, err := NewOAuthController(context.Background(), resource, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer optedIn.Close()
	if _, ok := optedIn.transport.private["http://private.example:8080"]; !ok {
		t.Fatal("exact resource private opt-in was not retained")
	}
}

func TestMCPOAuthCrossOriginRedirectNeverReachesDestinationWithBearer(t *testing.T) {
	var hits atomic.Int32
	var authorization string
	attacker := httptest.NewServer(http.HandlerFunc(func(httpWriter http.ResponseWriter, req *http.Request) {
		hits.Add(1)
		authorization = req.Header.Get("Authorization")
		httpWriter.WriteHeader(http.StatusNoContent)
	}))
	defer attacker.Close()
	resource := httptest.NewServer(http.HandlerFunc(func(httpWriter http.ResponseWriter, _ *http.Request) {
		httpWriter.Header().Set("Location", attacker.URL)
		httpWriter.WriteHeader(http.StatusFound)
	}))
	defer resource.Close()

	store := newOAuthMemoryStore(t)
	opts := testOAuthOptions(store)
	opts.Network.PrivateOrigins = []string{resource.URL}
	seed, err := NewOAuthController(context.Background(), resource.URL+"/mcp", opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.state.newTokenSource(context.Background(), testOAuthConfig("https://issuer.example/token"), validOAuthToken("bearer-canary", "refresh")); err != nil {
		t.Fatal(err)
	}
	_ = seed.Close()
	if _, err := Connect(context.Background(), ServerConfig{Name: "redirect", URL: resource.URL + "/mcp", OAuth: &opts}, nil); err == nil {
		t.Fatal("cross-origin redirect unexpectedly connected")
	}
	if hits.Load() != 0 || authorization != "" {
		t.Fatalf("redirect destination hits=%d Authorization=%q, want no request", hits.Load(), authorization)
	}
}

func TestMCPOAuthRedirectGateRejectsCrossOriginBeforeDial(t *testing.T) {
	origin := "https://mcp.example"
	policy := mcpOAuthRedirectPolicy(origin, maxOAuthRedirects)
	first, _ := http.NewRequest(http.MethodPost, origin+"/mcp", nil)
	cross, _ := http.NewRequest(http.MethodGet, "https://attacker.example/mcp", nil)
	cross.Header.Set("Authorization", "Bearer canary")
	if err := policy(cross, []*http.Request{first}); !errors.Is(err, ErrOAuthUnavailable) {
		t.Fatalf("cross-origin MCP redirect error = %v", err)
	}
	same, _ := url.Parse(origin + "/other")
	cross.URL = same
	if err := policy(cross, []*http.Request{first}); err != nil {
		t.Fatalf("same-origin MCP redirect: %v", err)
	}
}

type oauthDiagnosticRecord struct {
	level port.Level
	msg   string
	attrs []any
}

type oauthDiagnosticRecorder struct {
	records []oauthDiagnosticRecord
}

func (r *oauthDiagnosticRecorder) Log(_ context.Context, level port.Level, msg string, attrs ...any) {
	r.records = append(r.records, oauthDiagnosticRecord{level: level, msg: msg, attrs: append([]any(nil), attrs...)})
}

func (r *oauthDiagnosticRecorder) With(...any) port.Diagnostics { return r }

func TestMCPOAuthLoginTimeoutDiagnostics_Scenario1_DirectResourceTimeoutPolicy(t *testing.T) {
	opts := testOAuthOptions(newOAuthMemoryStore(t))
	client, transport, err := newOAuthHTTPClient("https://mcp.example/mcp", opts)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	if got := transport.base.ResponseHeaderTimeout; got != defaultOAuthTimeout {
		t.Fatalf("direct OAuth ResponseHeaderTimeout = %v, want %v", got, defaultOAuthTimeout)
	}
	if got := client.Timeout; got != defaultOAuthTimeout {
		t.Fatalf("direct OAuth whole-request timeout = %v, want %v", got, defaultOAuthTimeout)
	}
	if got := transport.base.TLSHandshakeTimeout; got != 5*time.Second {
		t.Fatalf("direct OAuth TLS handshake timeout = %v, want 5s", got)
	}

	tokenClient, err := NewHardenedOAuthTokenClient(HardenedOAuthTokenClientOptions{TokenEndpoint: "https://issuer.example/token"})
	if err != nil {
		t.Fatal(err)
	}
	tokenTransport, ok := tokenClient.Transport.(*oauthHTTPTransport)
	if !ok {
		t.Fatalf("token transport = %T", tokenClient.Transport)
	}
	if got := tokenTransport.base.ResponseHeaderTimeout; got != 10*time.Second {
		t.Fatalf("token OAuth ResponseHeaderTimeout = %v, want 10s", got)
	}
}

func TestMCPOAuthLoginTimeoutDiagnostics_Scenario1_DelayedResourceHeadersSucceed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(75 * time.Millisecond)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	opts := testOAuthOptions(newOAuthMemoryStore(t))
	opts.Issuer = server.URL
	opts.Client.Preregistered.Issuer = server.URL
	opts.Network.PrivateOrigins = []string{server.URL}
	AllowOAuthLoopbackForTest(t, &opts)
	client, transport, err := newOAuthHTTPClient(server.URL, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	client.Timeout = 200 * time.Millisecond
	transport.base.ResponseHeaderTimeout = 25 * time.Millisecond
	if _, err := client.Get(server.URL); err == nil {
		t.Fatal("old scaled response-header timeout unexpectedly succeeded")
	}
	transport.base.ResponseHeaderTimeout = 150 * time.Millisecond
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("revised scaled response-header timeout: %v", err)
	}
	closeOAuthResponse(resp)
}

func TestMCPOAuthLoginTimeoutDiagnostics_Scenario1_RoundTripFailureDiagnostic(t *testing.T) {
	const secret = "query-token-canary"
	recorder := &oauthDiagnosticRecorder{}
	origin := "http://127.0.0.1:1"
	transport := &oauthHTTPTransport{
		origins:       map[string]struct{}{origin: {}},
		private:       map[string]struct{}{},
		allowLoopback: true,
		diag:          NewOAuthDiagnostics(recorder).Redacted(),
		lookup: func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		},
		dial: (&net.Dialer{Timeout: 50 * time.Millisecond}).DialContext,
	}
	transport.base = &http.Transport{DialContext: transport.dialContext}
	req, err := http.NewRequest(http.MethodGet, origin+"/resource?access_token="+secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(req); err == nil {
		t.Fatal("RoundTrip unexpectedly succeeded")
	}
	if len(recorder.records) != 1 {
		t.Fatalf("diagnostic records = %d, want 1", len(recorder.records))
	}
	record := recorder.records[0]
	if record.level != port.LevelWarn || record.msg != "mcp: OAuth HTTP transport request failed" {
		t.Fatalf("diagnostic = %#v", record)
	}
	attrs := make(map[string]any)
	for i := 0; i+1 < len(record.attrs); i += 2 {
		attrs[record.attrs[i].(string)] = record.attrs[i+1]
	}
	if attrs["method"] != http.MethodGet || attrs["url"] != origin+"/resource" {
		t.Fatalf("diagnostic attributes = %#v", attrs)
	}
	if _, ok := attrs["duration"].(time.Duration); !ok {
		t.Fatalf("duration = %T, want time.Duration", attrs["duration"])
	}
	if got := attrs["err"].(string); got == "" || strings.Contains(got, secret) {
		t.Fatalf("redacted error = %q", got)
	}
}
