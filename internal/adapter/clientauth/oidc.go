package clientauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	authoidc "github.com/stacklok/mecatl/authn/oidc"
	"github.com/stacklok/mecatl/authn/oidc/scopedhttps"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

var (
	// ErrDiscovery indicates that OIDC discovery was rejected.
	ErrDiscovery = errors.New("clientauth: OIDC discovery rejected")
	// ErrAuthorization indicates that authorization failed.
	ErrAuthorization = errors.New("clientauth: authorization failed")
	// ErrTokenExchange indicates that the token exchange failed.
	ErrTokenExchange = errors.New("clientauth: token exchange failed")
	// ErrLoginRequired indicates that interactive login is required.
	ErrLoginRequired = errors.New("clientauth: login required")
	// ErrCredentialCleanup indicates that a rejected credential could not be removed safely.
	ErrCredentialCleanup = errors.New("clientauth: credential cleanup failed")
)

// LoginRequiredCause explains why a credential can no longer be used. The set is
// deliberately closed: server-side authentication rejection is a transport concern.
type LoginRequiredCause string

const (
	// NotEnrolled means no credential is stored for the target.
	NotEnrolled LoginRequiredCause = "not_enrolled"
	// SessionExpired means the credential cannot be renewed.
	SessionExpired LoginRequiredCause = "session_expired"
	// CredentialUnusable means stored credential data is corrupt or malformed.
	// #nosec G101 -- diagnostic label, not a credential.
	CredentialUnusable LoginRequiredCause = "credential_unusable"
)

// LoginRequiredError retains ErrLoginRequired for compatibility while exposing
// a safe, local diagnosis. It never contains provider responses or credentials.
type LoginRequiredError struct{ Cause LoginRequiredCause }

func (e *LoginRequiredError) Error() string { return fmt.Sprintf("%s: %s", ErrLoginRequired, e.Cause) }
func (*LoginRequiredError) Unwrap() error   { return ErrLoginRequired }

func loginRequired(cause LoginRequiredCause) error { return &LoginRequiredError{Cause: cause} }

// Presenter is implemented by the host-owned oauthlogin callback runtime. This package neither opens a browser nor listens for callbacks.
type Presenter interface {
	Present(context.Context, string) (oauthlogin.Result, error)
}

// PresenterFunc adapts a function to the Presenter interface.
type PresenterFunc func(context.Context, string) (oauthlogin.Result, error)

// Present invokes f with the authorization URL.
func (f PresenterFunc) Present(ctx context.Context, u string) (oauthlogin.Result, error) {
	return f(ctx, u)
}

// LoginConfig defines a public OAuth client. ClientSecret is intentionally absent.
type LoginConfig struct {
	Identity   Identity
	Presenter  Presenter
	HTTPClient *http.Client
	// PrivateHTTPS allows fixture/private issuer HTTPS only with supplied CA bytes.
	PrivateHTTPS  bool
	TrustedCAPEM  []byte
	TrustedCAFile string
	// Registry supplies target-scoped transaction locking to RefreshSource. Login
	// itself does not use it, so browser interaction remains outside the lock.
	Registry *Registry
}
type discovery struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	// IssuerParameterSupported reports RFC 9207 support. When true the server
	// promised an iss on every authorization response, so a missing one is an
	// attack signal rather than an unsupported optional feature.
	IssuerParameterSupported bool `json:"authorization_response_iss_parameter_supported"`
}

// Login runs one Authorization Code + PKCE S256 exchange and validates the initial access token.
func Login(ctx context.Context, cfg LoginConfig) (Token, error) {
	id, err := cfg.Identity.Canonical()
	if err != nil {
		return Token{}, err
	}
	if cfg.Presenter == nil {
		return Token{}, ErrAuthorization
	}
	client, err := oidcClient(ctx, id, cfg)
	if err != nil {
		return Token{}, fmt.Errorf("%w: %w", ErrDiscovery, err)
	}
	if cfg.HTTPClient == nil {
		defer client.CloseIdleConnections()
	}
	doc, err := fetchDiscovery(ctx, client, id)
	if err != nil {
		return Token{}, fmt.Errorf("%w: %w", ErrDiscovery, err)
	}
	verifier, err := randomURLValue(32)
	if err != nil {
		return Token{}, ErrAuthorization
	}
	state, err := randomURLValue(32)
	if err != nil {
		return Token{}, ErrAuthorization
	}
	oc := oauth2.Config{ClientID: id.ClientID, RedirectURL: id.RedirectURI, Endpoint: oauth2.Endpoint{AuthURL: doc.AuthorizationEndpoint, TokenURL: doc.TokenEndpoint}, Scopes: id.Scopes}
	authURL := oc.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.S256ChallengeOption(verifier))
	result, err := cfg.Presenter.Present(ctx, authURL)
	if err != nil {
		// Preserve the callback rejection reason and any context error; the
		// wrapped cause names a validation rule, never a callback value.
		return Token{}, fmt.Errorf("%w: %w", ErrAuthorization, err)
	}
	if result.State != state || result.Code == "" {
		return Token{}, fmt.Errorf("%w: callback did not match the authorization request", ErrAuthorization)
	}
	// RFC 9207 section 2.4. The callback listener rejects a mismatched iss; the
	// required-if-advertised half needs the discovery document and so lives here.
	if result.Iss == "" {
		if doc.IssuerParameterSupported {
			return Token{}, fmt.Errorf("%w: authorization server advertises RFC 9207 but the callback carried no iss", ErrAuthorization)
		}
	} else {
		callbackIssuer, err := canonicalIssuerURL(result.Iss)
		if err != nil || callbackIssuer != id.Issuer {
			return Token{}, fmt.Errorf("%w: callback iss did not match the expected issuer", ErrAuthorization)
		}
	}
	exchangeCtx := context.WithValue(ctx, oauth2.HTTPClient, client)
	tok, err := oc.Exchange(exchangeCtx, result.Code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Token{}, ErrTokenExchange
	}
	validator, err := authoidc.NewValidator(ctx, authoidc.Config{Issuer: id.Issuer, JWKSURI: doc.JWKSURI, Audience: id.Audience, HTTPClient: client})
	if err != nil {
		return Token{}, ErrDiscovery
	}
	defer func() { _ = validator.Close() }()
	if _, err := validator.Validate(ctx, tok.AccessToken); err != nil {
		return Token{}, safeValidationError(err)
	}
	return Token{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, TokenType: tok.TokenType, Expiry: tok.Expiry.UTC().Format("2006-01-02T15:04:05Z")}, nil
}

// RefreshSource validates access tokens and refreshes them only when necessary.
// It is safe for gRPC's concurrent per-RPC credential calls.
type RefreshSource struct {
	identity   Identity
	creds      *Credentials
	registry   *Registry
	client     *http.Client
	endpoint   oauth2.Endpoint
	validator  *authoidc.Validator
	ownsClient bool
	mu         sync.Mutex
	activity   uint64
	refreshed  uint64
	pending    LoginRequiredCause
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	closeOnce  sync.Once
}

const (
	refreshAhead = 30 * time.Second
	refreshPoll  = 5 * time.Second
)

// NewRefreshSource constructs a target-bound source. Call Close when the dial is done.
func NewRefreshSource(ctx context.Context, creds *Credentials, cfg LoginConfig) (*RefreshSource, error) {
	if creds == nil || cfg.Registry == nil {
		return nil, loginRequired(CredentialUnusable)
	}
	id, err := cfg.Identity.Canonical()
	if err != nil {
		return nil, err
	}
	client, err := oidcClient(ctx, id, cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDiscovery, err)
	}
	ownsClient := cfg.HTTPClient == nil
	closeOnFailure := ownsClient
	defer func() {
		if closeOnFailure {
			client.CloseIdleConnections()
		}
	}()
	doc, err := fetchDiscovery(ctx, client, id)
	if err != nil {
		return nil, ErrDiscovery
	}
	validator, err := authoidc.NewValidator(ctx, authoidc.Config{Issuer: id.Issuer, JWKSURI: doc.JWKSURI, Audience: id.Audience, HTTPClient: client})
	if err != nil {
		return nil, ErrDiscovery
	}
	result := newRefreshSource(id, creds, cfg.Registry, client, doc.TokenEndpoint, validator, refreshPoll)
	result.ownsClient = ownsClient
	closeOnFailure = false
	return result, nil
}

func newRefreshSource(id Identity, creds *Credentials, registry *Registry, client *http.Client, tokenURL string, validator *authoidc.Validator, poll time.Duration) *RefreshSource {
	ctx, cancel := context.WithCancel(context.Background())
	s := &RefreshSource{identity: id, creds: creds, registry: registry, client: client, endpoint: oauth2.Endpoint{TokenURL: tokenURL}, validator: validator, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	go s.refreshLoop(poll)
	return s
}

// Close releases resources held by the refresh source and waits for proactive
// refresh to stop before closing owned clients and the validator.
func (s *RefreshSource) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		<-s.done
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.validator != nil {
			_ = s.validator.Close()
		}
		if s.ownsClient && s.client != nil {
			s.client.CloseIdleConnections()
		}
	})
	return nil
}

func (s *RefreshSource) refreshLoop(interval time.Duration) {
	defer close(s.done)
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.maybeProactiveRefresh()
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *RefreshSource) maybeProactiveRefresh() {
	if s.registry == nil {
		return
	}
	unlock, err := s.registry.lockTarget(s.ctx, s.identity.Target)
	if err != nil {
		return
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activity == s.refreshed {
		return
	}
	rec, err := s.creds.Load(s.ctx, s.identity)
	if err != nil {
		return
	}
	expiry, err := time.Parse(time.RFC3339, rec.Token.Expiry)
	if err != nil || time.Until(expiry) > refreshAhead {
		return
	}
	if _, _, err := s.tokenLocked(s.ctx, rec, expiry); err == nil {
		s.refreshed = s.activity
	} else {
		var required *LoginRequiredError
		if errors.As(err, &required) && required.Cause == SessionExpired {
			s.pending = SessionExpired
		}
	}
}

// Token returns a validated access token and persists a rotated token using CAS.
func (s *RefreshSource) Token(ctx context.Context) (string, error) {
	if s.registry == nil {
		return "", loginRequired(CredentialUnusable)
	}
	unlock, err := s.registry.lockTarget(ctx, s.identity.Target)
	if err != nil {
		return "", err
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	token, refreshed, err := s.tokenLocked(ctx, CredentialRecord{}, time.Time{})
	if s.pending != "" {
		var required *LoginRequiredError
		if errors.As(err, &required) && required.Cause == NotEnrolled {
			cause := s.pending
			s.pending = ""
			return "", loginRequired(cause)
		}
		if err == nil {
			s.pending = ""
		}
	}
	if err == nil {
		s.activity++
		if refreshed {
			s.refreshed = s.activity
		}
	}
	return token, err
}

func (s *RefreshSource) tokenLocked(ctx context.Context, loaded CredentialRecord, expiry time.Time) (string, bool, error) {
	rec := loaded
	var err error
	if rec.Token.AccessToken == "" {
		rec, err = s.creds.Load(ctx, s.identity)
		if err != nil {
			if errors.Is(err, credentialstore.ErrNotFound) {
				return "", false, loginRequired(NotEnrolled)
			}
			if errors.Is(err, ErrCorrupt) {
				return "", false, loginRequired(CredentialUnusable)
			}
			return "", false, fmt.Errorf("clientauth: saved credential unavailable: %w", err)
		}
	}
	if expiry.IsZero() {
		expiry, err = time.Parse(time.RFC3339, rec.Token.Expiry)
		if err != nil {
			return "", false, loginRequired(CredentialUnusable)
		}
	}
	if time.Until(expiry) > refreshAhead {
		if _, err := s.validator.Validate(ctx, rec.Token.AccessToken); err == nil {
			return rec.Token.AccessToken, false, nil
		}
	}
	if rec.Token.RefreshToken == "" {
		return "", false, loginRequired(SessionExpired)
	}
	return s.exchangeAndPersist(ctx, rec)
}

// exchangeAndPersist performs the refresh_token exchange for rec and persists
// the result, split out of tokenLocked to keep that function's branching
// within the complexity gate.
func (s *RefreshSource) exchangeAndPersist(ctx context.Context, rec CredentialRecord) (string, bool, error) {
	exchangeCtx := context.WithValue(ctx, oauth2.HTTPClient, s.client)
	// The seed token's Expiry is deliberately already-past, not the real expiry:
	// refreshAhead already decided a refresh is due, so TokenSource must always
	// perform a genuine refresh_token exchange here. Passing the real (not-yet-
	// expired-by-x/oauth2's own smaller reuse threshold) expiry lets TokenSource
	// hand back this locally-constructed token verbatim without ever calling the
	// token endpoint -- and since that placeholder never had TokenType set,
	// validToken() then rejects it as corrupt even though the stored credential
	// was never touched.
	seed := &oauth2.Token{AccessToken: rec.Token.AccessToken, RefreshToken: rec.Token.RefreshToken, Expiry: time.Now().Add(-time.Minute)}
	tok, err := (&oauth2.Config{ClientID: s.identity.ClientID, Endpoint: s.endpoint}).TokenSource(exchangeCtx, seed).Token()
	if err != nil {
		if isInvalidGrant(err) {
			if deleteErr := s.creds.Delete(ctx, s.identity, rec.Version); deleteErr != nil && !errors.Is(deleteErr, credentialstore.ErrNotFound) {
				return "", false, ErrCredentialCleanup
			}
			return "", false, loginRequired(SessionExpired)
		}
		return "", false, ErrTokenExchange
	}
	refresh := tok.RefreshToken
	if refresh == "" {
		refresh = rec.Token.RefreshToken
	}
	replacement := Token{AccessToken: tok.AccessToken, RefreshToken: refresh, TokenType: tok.TokenType, Expiry: tok.Expiry.UTC().Format(time.RFC3339)}
	rotated := tok.RefreshToken != "" && tok.RefreshToken != rec.Token.RefreshToken
	// Persist a rotated refresh token before validating the new access token. A
	// transient JWKS failure must not strand the one-time refresh token: the next
	// attempt must be able to validate the replacement without exchanging again.
	if rotated && validToken(replacement) {
		if _, err := s.creds.Save(ctx, s.identity, replacement, &rec.Version); err != nil {
			return "", false, fmt.Errorf("clientauth: credential update failed: %w", err)
		}
	}
	if _, err := s.validator.Validate(ctx, tok.AccessToken); err != nil {
		return "", false, safeValidationError(err)
	}
	if !rotated {
		if _, err := s.creds.Save(ctx, s.identity, replacement, &rec.Version); err != nil {
			return "", false, fmt.Errorf("clientauth: credential update failed: %w", err)
		}
	}
	return replacement.AccessToken, true, nil
}

func isInvalidGrant(err error) bool {
	var retrieveErr *oauth2.RetrieveError
	return errors.As(err, &retrieveErr) && retrieveErr.ErrorCode == "invalid_grant"
}

func safeValidationError(err error) error {
	if errors.Is(err, authoidc.ErrIdentityUnavailable) {
		return authoidc.ErrIdentityUnavailable
	}
	return authoidc.ErrInvalidToken
}
func oidcClient(ctx context.Context, id Identity, cfg LoginConfig) (*http.Client, error) {
	if cfg.PrivateHTTPS {
		if cfg.HTTPClient != nil {
			return nil, errors.New("custom HTTP client is not allowed with private HTTPS issuer mode")
		}
		if len(cfg.TrustedCAPEM) == 0 {
			return nil, errors.New("private HTTPS issuer requires a CA bundle")
		}
		return scopedhttps.NewSingleIssuerClient(ctx, []string{id.Issuer}, cfg.TrustedCAPEM)
	}
	if cfg.HTTPClient != nil {
		return cfg.HTTPClient, nil
	}
	return &http.Client{Timeout: 15e9, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}, nil
}
func fetchDiscovery(ctx context.Context, client *http.Client, id Identity) (discovery, error) {
	endpoint := strings.TrimSuffix(id.Issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return discovery{}, err
	}
	res, err := client.Do(req)
	if err != nil {
		return discovery{}, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return discovery{}, errors.New("discovery status")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return discovery{}, err
	}
	var doc discovery
	if json.Unmarshal(body, &doc) != nil {
		return discovery{}, errors.New("discovery JSON")
	}
	issuer, err := canonicalIssuerURL(doc.Issuer)
	if err != nil || issuer != id.Issuer {
		return discovery{}, errors.New("issuer mismatch")
	}
	for _, endpoint := range []struct {
		field string
		raw   string
	}{
		{"authorization_endpoint", doc.AuthorizationEndpoint},
		{"token_endpoint", doc.TokenEndpoint},
		{"jwks_uri", doc.JWKSURI},
	} {
		u, err := url.Parse(endpoint.raw)
		if err != nil || u.Scheme != httpsScheme || u.Host != mustHost(id.Issuer) || u.User != nil {
			return discovery{}, fmt.Errorf("discovery %s must be an HTTPS URL on the issuer's host", endpoint.field)
		}
	}
	found := false
	for _, method := range doc.CodeChallengeMethods {
		if method == "S256" {
			found = true
		}
	}
	if !found {
		return discovery{}, errors.New("S256 unsupported")
	}
	return doc, nil
}
func mustHost(raw string) string { u, _ := url.Parse(raw); return u.Host }
func randomURLValue(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
