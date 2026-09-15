package mcp

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

const (
	defaultOAuthTimeout = 30 * time.Second
	maxOAuthRedirects   = 5
	maxOAuthRequestBody = 1 << 20
)

type oauthLookupFunc func(context.Context, string, string) ([]netip.Addr, error)
type oauthDialFunc func(context.Context, string, string) (net.Conn, error)

// HardenedOAuthTokenClientOptions configures an exact-origin OAuth token client.
// The resulting client refuses redirects and proxies, validates every resolved IP,
// pins dialing to the validated addresses, requires TLS 1.2+, and applies bounded
// dial, handshake, response-header, and whole-request timeouts.
type HardenedOAuthTokenClientOptions struct {
	TokenEndpoint string
	Timeout       time.Duration
	allowLoopback bool
	rootCAs       *x509.CertPool
}

// AllowHardenedOAuthTokenLoopbackForTest enables a loopback TLS endpoint and its
// test CA. The relaxation is deliberately unavailable as production data/config.
func AllowHardenedOAuthTokenLoopbackForTest(t interface{ Helper() }, opts *HardenedOAuthTokenClientOptions, roots *x509.CertPool) {
	t.Helper()
	if opts != nil {
		opts.allowLoopback = true
		opts.rootCAs = roots
	}
}

// NewHardenedOAuthTokenClient constructs a client restricted to the trusted token
// endpoint's exact origin. Request path/query remain controlled by oauth2.Config.
func NewHardenedOAuthTokenClient(opts HardenedOAuthTokenClientOptions) (*http.Client, error) {
	endpoint, err := validateHTTPURL("OAuth token endpoint", opts.TokenEndpoint, false)
	if err != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("OAuth token endpoint must be a canonical HTTPS URL")
	}
	origin := urlOrigin(endpoint)
	resolver := &net.Resolver{}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &oauthHTTPTransport{
		origins:            map[string]struct{}{origin: {}},
		private:            make(map[string]struct{}),
		issuerOrigin:       origin,
		resourceOrigin:     origin,
		requireClientBasic: true,
		lookup:             resolver.LookupNetIP,
		dial:               dialer.DialContext,
		allowLoopback:      opts.allowLoopback,
	}
	transport.base = &http.Transport{
		Proxy:                  nil,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: opts.rootCAs},
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  10 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
		MaxIdleConns:           2,
		MaxIdleConnsPerHost:    2,
		IdleConnTimeout:        30 * time.Second,
		DialContext:            transport.dialContext,
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultOAuthTimeout
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return projectOAuthError(ErrOAuthUnavailable)
		},
	}, nil
}

type oauthHTTPTransport struct {
	origins            map[string]struct{}
	private            map[string]struct{}
	issuerOrigin       string
	resourceOrigin     string
	requireClientBasic bool
	dcrPublicClientID  string
	dcrResource        string
	dcrIssuer          string
	lookup             oauthLookupFunc
	dial               oauthDialFunc
	allowLoopback      bool
	base               *http.Transport
	diag               OAuthDiagnostics
}

func newOAuthHTTPClient(resource string, opts OAuthOptions) (*http.Client, *oauthHTTPTransport, error) {
	canonical, err := canonicalOAuthResource(resource)
	if err != nil {
		return nil, nil, err
	}
	resourceURL, _ := url.Parse(canonical)
	issuer, err := validateHTTPURL("OAuth issuer", opts.Issuer, false)
	if err != nil {
		return nil, nil, err
	}
	origins, err := validateOAuthOrigins(issuer, opts.Network)
	if err != nil {
		return nil, nil, err
	}
	origins[urlOrigin(resourceURL)] = struct{}{}
	if err := validatePrivateOrigins(origins, opts.Network); err != nil {
		return nil, nil, err
	}
	private, err := oauthPrivateOrigins(opts.Network)
	if err != nil {
		return nil, nil, err
	}
	resolver := &net.Resolver{}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &oauthHTTPTransport{
		origins:            origins,
		private:            private,
		issuerOrigin:       urlOrigin(issuer),
		resourceOrigin:     urlOrigin(resourceURL),
		requireClientBasic: opts.Client.Preregistered != nil,
		dcrResource:        canonical,
		lookup:             resolver.LookupNetIP,
		dial:               dialer.DialContext,
		allowLoopback:      opts.allowLoopbackForTest,
		diag:               opts.Diagnostics.Redacted(),
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: opts.testRootCAs}
	transport.base = &http.Transport{
		Proxy:                  nil,
		TLSClientConfig:        tlsConfig,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  defaultOAuthTimeout,
		MaxResponseHeaderBytes: 64 << 10,
		MaxIdleConns:           8,
		MaxIdleConnsPerHost:    2,
		IdleConnTimeout:        30 * time.Second,
		DialContext:            transport.dialContext,
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = defaultOAuthTimeout
	}
	client := &http.Client{
		Transport:     transport,
		Timeout:       timeout,
		CheckRedirect: oauthRedirectPolicy(opts.Network.MaxRedirects),
	}
	return client, transport, nil
}

func oauthPrivateOrigins(network OAuthNetworkPolicy) (map[string]struct{}, error) {
	private := make(map[string]struct{}, len(network.PrivateOrigins))
	for _, raw := range network.PrivateOrigins {
		u, err := validateOrigin("OAuth private origin", raw)
		if err != nil {
			return nil, err
		}
		private[urlOrigin(u)] = struct{}{}
	}
	return private, nil
}

type oauthOriginContextKey struct{}
type oauthResourceRequestContextKey struct{}

func oauthRequestForm(req *http.Request) (url.Values, error) {
	if req.Body == nil {
		return nil, nil
	}
	mediaType, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, maxOAuthRequestBody+1))
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(strings.NewReader(string(body)))
	if len(body) > maxOAuthRequestBody {
		return nil, errors.New("OAuth request body is too large")
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}
	return values, nil
}

func oauthRequestHasCredential(req *http.Request, form url.Values) bool {
	if req.Header.Get("Authorization") != "" {
		return true
	}
	for _, values := range []url.Values{req.URL.Query(), form} {
		for _, key := range []string{"code", "refresh_token", "client_secret", "client_assertion", "subject_token", "actor_token"} {
			if values.Get(key) != "" {
				return true
			}
		}
		if values.Get("grant_type") != "" {
			return true
		}
	}
	return false
}

func (t *oauthHTTPTransport) validateEgress(req *http.Request, origin string) error {
	form, err := oauthRequestForm(req)
	if err != nil {
		return err
	}
	if form.Get("client_secret") != "" || req.URL.Query().Get("client_secret") != "" {
		return errors.New("OAuth client_secret_post is not permitted")
	}
	resourceRequest, _ := req.Context().Value(oauthResourceRequestContextKey{}).(bool)
	if resourceRequest {
		if origin != t.resourceOrigin {
			return errors.New("OAuth resource request origin is invalid")
		}
		return nil
	}
	credentialBearing := oauthRequestHasCredential(req, form)
	if origin != t.issuerOrigin && (credentialBearing || req.Body != nil) {
		return errors.New("OAuth credential request origin is invalid")
	}
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		if origin != t.issuerOrigin {
			return errors.New("OAuth protocol POST origin is invalid")
		}
		if t.requireClientBasic && !strings.HasPrefix(req.Header.Get("Authorization"), "Basic ") {
			return errors.New("OAuth confidential client must use client_secret_basic")
		}
		if err := t.validateDCRPublicExchange(req, form); err != nil {
			return err
		}
	}
	return nil
}

func (t *oauthHTTPTransport) validateDCRPublicExchange(req *http.Request, form url.Values) error {
	if t.dcrPublicClientID == "" || form.Get("grant_type") != "authorization_code" {
		return nil
	}
	if strings.HasPrefix(req.Header.Get("Authorization"), "Basic ") {
		return errors.New("OAuth public client Basic probe rejected")
	}
	if req.Header.Get("Authorization") != "" || len(form["client_id"]) != 1 || form.Get("client_id") != t.dcrPublicClientID || len(form["resource"]) != 1 || form.Get("resource") != t.dcrResource || form.Get("client_secret") != "" || form.Get("client_assertion") != "" || form.Get("client_assertion_type") != "" {
		return errors.New("OAuth public client token request is invalid")
	}
	return nil
}

func (t *oauthHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil || req.URL.User != nil || req.URL.Fragment != "" {
		return nil, projectOAuthError(ErrOAuthUnavailable)
	}
	origin := urlOrigin(req.URL)
	if _, ok := t.origins[origin]; !ok {
		return nil, projectOAuthError(ErrOAuthUnavailable)
	}
	if err := t.validateEgress(req, origin); err != nil {
		return nil, projectOAuthError(err)
	}
	if !strings.EqualFold(req.URL.Scheme, "https") {
		_, private := t.private[origin]
		loopbackTest := t.allowLoopback && isLoopbackHost(req.URL.Hostname())
		if !private && !loopbackTest {
			return nil, projectOAuthError(ErrOAuthUnavailable)
		}
	}
	req = req.Clone(context.WithValue(req.Context(), oauthOriginContextKey{}, origin))
	started := time.Now()
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		t.diag.Warn(req.Context(), "mcp: OAuth HTTP transport request failed",
			"method", req.Method, "duration", time.Since(started), "url", req.URL.String(), "err", err)
		if ctxErr := req.Context().Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, projectOAuthError(err)
	}
	if t.dcrIssuer != "" && (strings.Contains(req.URL.Path, "/.well-known/oauth-authorization-server") || strings.Contains(req.URL.Path, "/.well-known/openid-configuration")) {
		if err := validateDCRRuntimeMetadataIssuer(resp, t.dcrIssuer); err != nil {
			return nil, projectOAuthError(err)
		}
	}
	return resp, nil
}

func validateDCRRuntimeMetadataIssuer(resp *http.Response, expected string) error {
	if resp == nil || resp.Body == nil {
		return ErrOAuthDCRRecoveryRequired
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxOAuthRequestBody+1))
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil || len(body) > maxOAuthRequestBody {
		return ErrOAuthDCRRecoveryRequired
	}
	var metadata struct {
		Issuer string `json:"issuer"`
	}
	if err := json.Unmarshal(body, &metadata); err != nil || metadata.Issuer != expected {
		return ErrOAuthDCRRecoveryRequired
	}
	return nil
}

func (t *oauthHTTPTransport) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, servicePort, err := net.SplitHostPort(address)
	if err != nil {
		return nil, projectOAuthError(ErrOAuthUnavailable)
	}
	origin, ok := ctx.Value(oauthOriginContextKey{}).(string)
	if !ok {
		return nil, projectOAuthError(ErrOAuthUnavailable)
	}
	u, err := url.Parse(origin)
	if err != nil || !strings.EqualFold(u.Hostname(), host) {
		return nil, projectOAuthError(ErrOAuthUnavailable)
	}
	wantPort := u.Port()
	if wantPort == "" {
		if strings.EqualFold(u.Scheme, "https") {
			wantPort = "443"
		} else {
			wantPort = "80"
		}
	}
	if wantPort != servicePort {
		return nil, projectOAuthError(ErrOAuthUnavailable)
	}
	_, private := t.private[origin]
	loopbackTest := t.allowLoopback && isLoopbackHost(host)
	addrs, err := t.lookup(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, projectOAuthError(ErrOAuthUnavailable)
	}
	for _, addr := range addrs {
		if !allowedOAuthAddress(addr, private, loopbackTest) {
			return nil, projectOAuthError(ErrOAuthUnavailable)
		}
	}
	var lastErr error
	for _, addr := range addrs {
		conn, dialErr := t.dial(ctx, network, net.JoinHostPort(addr.String(), servicePort))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	return nil, projectOAuthError(lastErr)
}

func allowedOAuthAddress(addr netip.Addr, private, loopbackTest bool) bool {
	switch {
	case loopbackTest:
		return addr.IsValid() && addr.Zone() == "" && addr.Unmap().IsLoopback()
	case private:
		return privateOAuthAddress(addr)
	default:
		return !unsafeOAuthAddress(addr)
	}
}

func privateOAuthAddress(addr netip.Addr) bool {
	return addr.IsValid() && addr.Zone() == "" && !addr.Is4In6() && addr.IsPrivate()
}

func unsafeOAuthAddress(addr netip.Addr) bool {
	if !addr.IsValid() || addr.Zone() != "" {
		return true
	}
	addr = addr.Unmap()
	if addr == netip.MustParseAddr("168.63.129.16") {
		return true
	}
	return session.ValidateResolvedIP(net.IP(addr.AsSlice())) != nil
}

func oauthRedirectPolicy(limit int) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if limit == 0 || limit > maxOAuthRedirects || len(via) > limit || len(via) == 0 {
			return projectOAuthError(ErrOAuthUnavailable)
		}
		previous := via[len(via)-1]
		if previous.Method != http.MethodGet && previous.Method != http.MethodHead {
			return projectOAuthError(ErrOAuthUnavailable)
		}
		if previous.Header.Get("Authorization") != "" {
			return projectOAuthError(ErrOAuthUnavailable)
		}
		if urlOrigin(req.URL) != urlOrigin(previous.URL) {
			return projectOAuthError(ErrOAuthUnavailable)
		}
		return nil
	}
}

type oauthResourceRoundTripper struct {
	base *oauthHTTPTransport
}

func (t oauthResourceRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, projectOAuthError(ErrOAuthUnavailable)
	}
	ctx := context.WithValue(req.Context(), oauthResourceRequestContextKey{}, true)
	return t.base.RoundTrip(req.Clone(ctx))
}

func mcpOAuthRedirectPolicy(origin string, limit int) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if limit < 1 || limit > maxOAuthRedirects || len(via) == 0 || len(via) > limit || requestOrigin(req.URL) != origin {
			return projectOAuthError(ErrOAuthUnavailable)
		}
		return nil
	}
}

func validPort(u *url.URL) bool {
	if strings.HasSuffix(u.Host, ":") && u.Port() == "" {
		return false
	}
	if portNumber := u.Port(); portNumber != "" {
		n, err := strconv.ParseUint(portNumber, 10, 16)
		return err == nil && n != 0
	}
	return true
}

func closeOAuthResponse(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}

var _ http.RoundTripper = (*oauthHTTPTransport)(nil)
