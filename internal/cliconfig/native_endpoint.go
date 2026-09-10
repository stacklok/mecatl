package cliconfig

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/toolhive-core/networking"

	"github.com/stacklok/mecatl/authn/oidc/scopedhttps"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/llmendpoint"
	"github.com/stacklok/mecatl/internal/adapter/oidcclient"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

const nativeRedirectURI = "http://127.0.0.1:18473/oauth/callback"

// NativeEndpointPresenter presents one authorization URL through a host-owned callback runtime.
type NativeEndpointPresenter func(context.Context, string) (oauthlogin.Result, error)

// NativeEndpointRuntime owns local credential handles for one configured endpoint.
type NativeEndpointRuntime struct {
	definition permconfig.ProviderDefinition
	identity   llmendpoint.CredentialIdentity
	issuer     *http.Client
	gateway    http.RoundTripper
	present    NativeEndpointPresenter
	locker     llmendpoint.Locker

	mu     sync.Mutex
	stores []credentialstore.Store
}

// OpenNativeEndpointRuntime resolves local trust and identity without opening a
// keyring, credential store, browser, or network connection.
func OpenNativeEndpointRuntime(definition permconfig.ProviderDefinition, present NativeEndpointPresenter) (*NativeEndpointRuntime, error) {
	if definition.Native == nil {
		return nil, llmendpoint.ErrNotEnrolled
	}
	issuerClient, issuerDigest, err := nativeTrustClient(definition.Native.OIDC.Issuer, definition.Native.IssuerTrust)
	if err != nil {
		return nil, llmendpoint.ErrNotEnrolled
	}
	gatewayClient, gatewayDigest, err := nativeTrustClient(definition.BaseURL, definition.Native.GatewayTrust)
	if err != nil {
		return nil, llmendpoint.ErrNotEnrolled
	}
	locker, err := llmendpoint.NewTransactionLocker(definition.Native.CredentialHome)
	if err != nil {
		return nil, llmendpoint.ErrNotEnrolled
	}
	id := llmendpoint.CredentialIdentity{
		SchemaVersion: 1, EndpointID: definition.ID, Gateway: definition.BaseURL,
		Issuer: definition.Native.OIDC.Issuer, ClientID: definition.Native.OIDC.ClientID,
		ResourceAudience: definition.Native.OIDC.ResourceAudience,
		Scopes:           append([]string(nil), definition.Native.OIDC.Scopes...), RedirectURI: nativeRedirectURI,
		IssuerTrust:  llmendpoint.TrustIdentity{Policy: definition.Native.IssuerTrust.Policy, CADigest: issuerDigest},
		GatewayTrust: llmendpoint.TrustIdentity{Policy: definition.Native.GatewayTrust.Policy, CADigest: gatewayDigest},
	}
	return &NativeEndpointRuntime{definition: definition, identity: id, issuer: issuerClient, gateway: gatewayClient.Transport, present: present, locker: locker}, nil
}

func nativeTrustClient(endpoint string, trust permconfig.NativeTrust) (*http.Client, string, error) {
	digest := ""
	var pem []byte
	if trust.Policy == "private-ca" {
		file, err := os.Open(trust.CABundle)
		if err != nil {
			return nil, "", err
		}
		defer func() { _ = file.Close() }()
		pem, err = io.ReadAll(io.LimitReader(file, (1<<20)+1))
		if err != nil || len(pem) > 1<<20 {
			return nil, "", errors.New("native endpoint CA bundle is unavailable")
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, "", errors.New("native endpoint CA bundle is invalid")
		}
		sum := sha256.Sum256(pem)
		digest = hex.EncodeToString(sum[:])
	}

	var transport http.RoundTripper
	if trust.Policy == "private-ca" {
		transport = &lazyScopedTransport{endpoint: endpoint, pem: append([]byte(nil), pem...)}
	} else {
		transport = &http.Transport{
			Proxy:                  nil,
			DialContext:            networking.NewPrivateIPBlockingDialContext(),
			TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:    10 * time.Second,
			ResponseHeaderTimeout:  15 * time.Second,
			MaxResponseHeaderBytes: 32 << 10,
			DisableKeepAlives:      true,
		}
	}
	confined, err := newOriginTransport(endpoint, transport)
	if err != nil {
		return nil, "", err
	}
	return &http.Client{Transport: confined, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, digest, nil
}

type lazyScopedTransport struct {
	mu       sync.Mutex
	endpoint string
	pem      []byte
	next     http.RoundTripper
}

func (t *lazyScopedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	if t.next == nil {
		client, err := scopedhttps.NewSingleIssuerClient(req.Context(), []string{t.endpoint}, t.pem)
		if err != nil {
			t.mu.Unlock()
			return nil, err
		}
		t.next = client.Transport
		clear(t.pem)
		t.pem = nil
	}
	next := t.next
	t.mu.Unlock()
	return next.RoundTrip(req)
}

type originTransport struct {
	scheme, host, port string
	next               http.RoundTripper
}

func newOriginTransport(endpoint string, next http.RoundTripper) (originTransport, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || next == nil {
		return originTransport{}, errors.New("native endpoint origin is invalid")
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return originTransport{scheme: u.Scheme, host: u.Hostname(), port: port, next: next}, nil
}

func (t originTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, errors.New("native endpoint request is invalid")
	}
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	if req.URL.Scheme != t.scheme || !strings.EqualFold(req.URL.Hostname(), t.host) || port != t.port || req.URL.User != nil {
		return nil, errors.New("native endpoint request escaped its configured origin")
	}
	return t.next.RoundTrip(req)
}

func (r *NativeEndpointRuntime) open(ctx context.Context, existingOnly bool) (*llmendpoint.CredentialRepository, credentialstore.Store, error) {
	keyring, err := oidcclient.NewKeyring(r.definition.Native.CredentialHome)
	if err != nil {
		return nil, nil, err
	}
	store, err := llmendpoint.NewProtectedStore(ctx, llmendpoint.ProtectedStoreConfig{Root: r.definition.Native.CredentialHome, Keyring: keyring, ExistingOnly: existingOnly})
	if err != nil {
		return nil, nil, err
	}
	return llmendpoint.NewCredentialRepository(store), store, nil
}

func (r *NativeEndpointRuntime) lifecycle(repo *llmendpoint.CredentialRepository) llmendpoint.Lifecycle {
	cfg := oidcclient.Config{Issuer: r.identity.Issuer, ClientID: r.identity.ClientID, Audience: r.identity.ResourceAudience, RedirectURI: r.identity.RedirectURI, Scopes: append([]string(nil), r.identity.Scopes...), HTTPClient: r.issuer, Present: r.present}
	return llmendpoint.Lifecycle{
		Repository: repo, Locker: r.locker,
		Authorize: func(ctx context.Context) (llmendpoint.Token, error) {
			tok, err := oidcclient.AuthorizationCode(ctx, cfg)
			return llmendpoint.Token{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, TokenType: tok.TokenType, Expiry: tok.Expiry}, err
		},
		Exchange: func(ctx context.Context, refresh string) (llmendpoint.Token, error) {
			tok, err := oidcclient.Refresh(ctx, cfg, refresh)
			return llmendpoint.Token{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, TokenType: tok.TokenType, Expiry: tok.Expiry}, err
		},
		ValidateAccessToken: func(ctx context.Context, access string) error {
			return oidcclient.ValidateAccessToken(ctx, cfg, access)
		},
	}
}

// Login runs host-presented enrollment and persists the resulting record.
func (r *NativeEndpointRuntime) Login(ctx context.Context) error {
	if r.present == nil {
		return llmendpoint.ErrNotEnrolled
	}
	repo, store, err := r.open(ctx, false)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	return r.lifecycle(repo).Enroll(ctx, r.identity)
}

// Status passively inspects existing protected state.
func (r *NativeEndpointRuntime) Status(ctx context.Context) llmendpoint.Status {
	repo, store, err := r.open(ctx, true)
	if err != nil {
		if errors.Is(err, credentialstore.ErrNotFound) {
			return llmendpoint.StatusNotEnrolled
		}
		return llmendpoint.StatusStorageUnavailable
	}
	defer func() { _ = store.Close() }()
	return r.lifecycle(repo).Status(ctx, r.identity, time.Now())
}

// Logout deletes exact local state before bounded best-effort revocation.
func (r *NativeEndpointRuntime) Logout(ctx context.Context) error {
	repo, store, err := r.open(ctx, true)
	if err != nil {
		if errors.Is(err, credentialstore.ErrNotFound) {
			return nil
		}
		return err
	}
	defer func() { _ = store.Close() }()
	cfg := oidcclient.Config{Issuer: r.identity.Issuer, ClientID: r.identity.ClientID, Audience: r.identity.ResourceAudience, Scopes: r.identity.Scopes, HTTPClient: r.issuer}
	return r.lifecycle(repo).Logout(ctx, r.identity, func(ctx context.Context, tok llmendpoint.Token) error {
		_ = oidcclient.Revoke(ctx, cfg, tok.RefreshToken, "refresh_token")
		return oidcclient.Revoke(ctx, cfg, tok.AccessToken, "access_token")
	})
}

// Source opens existing protected state for a headless serving process. It never
// installs a presenter or starts enrollment.
func (r *NativeEndpointRuntime) Source(ctx context.Context) (llmendpoint.BearerSource, error) {
	repo, store, err := r.open(ctx, true)
	if err != nil {
		return nil, err
	}
	source := &nativeBearerSource{LifecycleSource: llmendpoint.LifecycleSource{Identity: r.identity, Repository: repo, Lifecycle: r.lifecycle(repo)}, transport: r.gateway}
	if err := source.Validate(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	r.mu.Lock()
	r.stores = append(r.stores, store)
	r.mu.Unlock()
	return source, nil
}

// Close releases every serving-process store opened by Source.
func (r *NativeEndpointRuntime) Close() error {
	r.mu.Lock()
	stores := r.stores
	r.stores = nil
	r.mu.Unlock()
	var errs []error
	for _, store := range stores {
		errs = append(errs, store.Close())
	}
	return errors.Join(errs...)
}

type nativeBearerSource struct {
	llmendpoint.LifecycleSource
	transport http.RoundTripper
}

func (s *nativeBearerSource) GatewayTransport() http.RoundTripper { return s.transport }

// NativeEndpointLoader is a headless, browser-free app credential loader.
type NativeEndpointLoader struct {
	mu       sync.Mutex
	runtimes []*NativeEndpointRuntime
}

// Load opens an existing endpoint record without installing browser enrollment.
func (l *NativeEndpointLoader) Load(ctx context.Context, definition permconfig.ProviderDefinition) (llmendpoint.BearerSource, error) {
	runtime, err := OpenNativeEndpointRuntime(definition, nil)
	if err != nil {
		return nil, err
	}
	source, err := runtime.Source(ctx)
	if err != nil {
		_ = runtime.Close()
		return nil, err
	}
	l.mu.Lock()
	l.runtimes = append(l.runtimes, runtime)
	l.mu.Unlock()
	return source, nil
}

// Close releases all loader-owned endpoint runtimes.
func (l *NativeEndpointLoader) Close() error {
	l.mu.Lock()
	runtimes := l.runtimes
	l.runtimes = nil
	l.mu.Unlock()
	var errs []error
	for _, runtime := range runtimes {
		errs = append(errs, runtime.Close())
	}
	return errors.Join(errs...)
}
