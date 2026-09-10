package mcp

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

// OAuthSubject identifies the host profile and principal that own a credential.
type OAuthSubject struct {
	Profile   string
	Principal string
}

// OAuthDCRConfig selects durable Dynamic Client Registration for a direct MCP profile.
type OAuthDCRConfig struct{}

// OAuthDCRLoginAction selects the explicit registration operation performed by login.
type OAuthDCRLoginAction uint8

const (
	// OAuthDCRLoginReuse reuses a ready registration or creates the initial registration.
	OAuthDCRLoginReuse OAuthDCRLoginAction = iota
	// OAuthDCRLoginResetRegistration explicitly replaces a ready registration.
	OAuthDCRLoginResetRegistration
	// OAuthDCRLoginRetryRegistration explicitly retries a pending registration attempt.
	OAuthDCRLoginRetryRegistration
)

// OAuthClientConfig selects one durable client-registration profile.
type OAuthClientConfig struct {
	Preregistered               *oauthex.ClientCredentials
	ClientIDMetadataDocumentURL string
	DCR                         *OAuthDCRConfig
}

// OAuthNetworkPolicy declares endpoint origins. DNS and transport enforcement is
// added by the controller's hardened HTTP client; persistence uses the origins to
// reject records for endpoints outside the configured identity.
type OAuthNetworkPolicy struct {
	AdditionalOrigins []string
	PrivateOrigins    []string
	MaxRedirects      int
}

// OAuthPresenter hands an authorization URL to a host-owned interactive flow.
type OAuthPresenter interface {
	PresentAuthorization(context.Context, string) (*auth.AuthorizationResult, error)
}

// OAuthPresenterFunc adapts a function into an OAuthPresenter.
type OAuthPresenterFunc func(context.Context, string) (*auth.AuthorizationResult, error)

// PresentAuthorization calls f with the authorization URL.
func (f OAuthPresenterFunc) PresentAuthorization(ctx context.Context, authorizationURL string) (*auth.AuthorizationResult, error) {
	return f(ctx, authorizationURL)
}

// OAuthOptions configures the persistence-capable SDK handler core.
type OAuthOptions struct {
	Subject     OAuthSubject
	Issuer      string
	Client      OAuthClientConfig
	RedirectURL string
	Presenter   OAuthPresenter
	// CredentialStore enables mutable restore, authorization, refresh rotation, and reset.
	// It is mutually exclusive with CredentialReader so reads and writes cannot cross CAS domains.
	CredentialStore credentialstore.Store
	// CredentialReader restores an existing opaque credential without mutation.
	// It is mutually exclusive with CredentialStore.
	CredentialReader credentialstore.Reader
	// AllowInMemoryRefresh permits a refreshed token from a read-only source to be
	// used only for this controller lifetime. It does not provide restart durability.
	AllowInMemoryRefresh bool
	Network              OAuthNetworkPolicy
	RequestRefreshToken  bool
	AllowedScopes        []string
	Timeout              time.Duration
	allowLoopbackForTest bool
	testRootCAs          *x509.CertPool
	dcr                  *oauthDCRResolved
	dcrTicket            *oauthDCRTicket
}

// AllowOAuthLoopbackForTest enables loopback only for in-process test servers.
// It is deliberately absent from OAuthNetworkPolicy and every production config
// projection, so operator input can never relax the loopback denial.
func AllowOAuthLoopbackForTest(t interface{ Helper() }, opts *OAuthOptions) {
	t.Helper()
	if opts != nil {
		opts.allowLoopbackForTest = true
	}
}

// TrustOAuthCertificateForTest trusts one test server certificate for OAuth TLS.
func TrustOAuthCertificateForTest(t interface{ Helper() }, opts *OAuthOptions, cert *x509.Certificate) {
	t.Helper()
	if opts == nil || cert == nil {
		return
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	opts.testRootCAs = roots
}

type oauthRegistration struct {
	kind         string
	clientID     string
	clientSecret string
	generation   string
	redirectPath string
	sdk          *oauthex.ClientCredentials
	cimd         string
}

func oauthPersistence(opts OAuthOptions) (credentialstore.Reader, credentialstore.ConditionalWriter, error) {
	if opts.CredentialStore != nil && opts.CredentialReader != nil {
		return nil, nil, errors.New("OAuth credential store conflicts with credential reader")
	}
	if opts.CredentialStore != nil {
		return opts.CredentialStore, opts.CredentialStore, nil
	}
	return opts.CredentialReader, nil, nil
}

func validateOAuthOptions(opts OAuthOptions) (oauthRegistration, map[string]struct{}, error) {
	if err := validateSafeValue("OAuth subject profile", opts.Subject.Profile); err != nil {
		return oauthRegistration{}, nil, err
	}
	if err := validateSafeValue("OAuth subject principal", opts.Subject.Principal); err != nil {
		return oauthRegistration{}, nil, err
	}
	issuer, err := validateHTTPURL("OAuth issuer", opts.Issuer, false)
	if err != nil {
		return oauthRegistration{}, nil, err
	}
	if _, err := validateHTTPURL("OAuth redirect URL", opts.RedirectURL, false); err != nil {
		return oauthRegistration{}, nil, err
	}
	reader, _, err := oauthPersistence(opts)
	if err != nil {
		return oauthRegistration{}, nil, err
	}
	if reader == nil {
		return oauthRegistration{}, nil, errors.New("OAuth credential reader is required")
	}
	if len(opts.AllowedScopes) == 0 {
		return oauthRegistration{}, nil, errors.New("OAuth allowed scopes are required")
	}
	for _, scope := range opts.AllowedScopes {
		if err := validateSafeValue("OAuth allowed scope", scope); err != nil {
			return oauthRegistration{}, nil, err
		}
	}
	if opts.Timeout < 0 {
		return oauthRegistration{}, nil, errors.New("OAuth timeout must not be negative")
	}
	if opts.Network.MaxRedirects < 0 || opts.Network.MaxRedirects > 5 {
		return oauthRegistration{}, nil, errors.New("OAuth max redirects must be between zero and five")
	}

	registration, err := resolvedOAuthRegistration(opts)
	if err != nil {
		return oauthRegistration{}, nil, err
	}
	origins, err := validateOAuthOrigins(issuer, opts.Network)
	if err != nil {
		return oauthRegistration{}, nil, err
	}
	return registration, origins, nil
}

func validateOAuthRegistration(clientOpts OAuthClientConfig, issuer string) (oauthRegistration, error) {
	preregistered := clientOpts.Preregistered != nil
	cimd := clientOpts.ClientIDMetadataDocumentURL != ""
	dcr := clientOpts.DCR != nil
	if boolCount(preregistered, cimd, dcr) != 1 {
		return oauthRegistration{}, errors.New("OAuth client must configure exactly one registration form")
	}
	if dcr {
		return oauthRegistration{}, errors.New("OAuth DCR client registration is unresolved")
	}
	if preregistered {
		client := clientOpts.Preregistered
		if err := client.Validate(); err != nil {
			return oauthRegistration{}, errors.New("OAuth preregistered client is invalid")
		}
		if client.ClientSecretAuth == nil || client.ClientSecretAuth.ClientSecret == "" {
			return oauthRegistration{}, errors.New("OAuth preregistered confidential client secret is required")
		}
		if client.Issuer == "" || client.Issuer != issuer {
			return oauthRegistration{}, errors.New("OAuth preregistered client issuer must exactly match OAuth issuer")
		}
		if err := validateSafeValue("OAuth client ID", client.ClientID); err != nil {
			return oauthRegistration{}, err
		}
		return oauthRegistration{kind: "preregistered", clientID: client.ClientID, clientSecret: client.ClientSecretAuth.ClientSecret, sdk: client}, nil
	}

	u, err := validateHTTPURL("OAuth client ID metadata document URL", clientOpts.ClientIDMetadataDocumentURL, true)
	if err != nil || u.Path == "" || u.Path == "/" {
		return oauthRegistration{}, errors.New("OAuth client ID metadata document URL is invalid")
	}
	return oauthRegistration{kind: "cimd", clientID: clientOpts.ClientIDMetadataDocumentURL, cimd: clientOpts.ClientIDMetadataDocumentURL}, nil
}

func boolCount(values ...bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}

func resolvedOAuthRegistration(opts OAuthOptions) (oauthRegistration, error) {
	if opts.Client.DCR == nil {
		return validateOAuthRegistration(opts.Client, opts.Issuer)
	}
	if boolCount(opts.Client.Preregistered != nil, opts.Client.ClientIDMetadataDocumentURL != "", true) != 1 || opts.dcr == nil {
		return oauthRegistration{}, errors.New("OAuth DCR client registration is unresolved")
	}
	if opts.dcr.issuer != opts.Issuer || opts.dcr.clientID == "" || !validDCRRandom(opts.dcr.generation) {
		return oauthRegistration{}, errors.New("OAuth DCR client registration is invalid")
	}
	client := &oauthex.ClientCredentials{ClientID: opts.dcr.clientID, Issuer: opts.Issuer}
	if err := client.Validate(); err != nil {
		return oauthRegistration{}, errors.New("OAuth DCR client registration is invalid")
	}
	return oauthRegistration{kind: oauthDCRClientKind, clientID: opts.dcr.clientID, generation: opts.dcr.generation, redirectPath: opts.dcr.path, sdk: client}, nil
}

func validateOAuthOrigins(issuer *url.URL, network OAuthNetworkPolicy) (map[string]struct{}, error) {
	origins := map[string]struct{}{urlOrigin(issuer): {}}
	for _, raw := range network.AdditionalOrigins {
		u, err := validateOrigin("OAuth additional origin", raw)
		if err != nil {
			return nil, err
		}
		origins[urlOrigin(u)] = struct{}{}
	}
	for _, raw := range network.PrivateOrigins {
		if _, err := validateOrigin("OAuth private origin", raw); err != nil {
			return nil, err
		}
	}
	return origins, nil
}

func validatePrivateOrigins(origins map[string]struct{}, network OAuthNetworkPolicy) error {
	for _, raw := range network.PrivateOrigins {
		u, _ := validateOrigin("OAuth private origin", raw)
		if _, ok := origins[urlOrigin(u)]; !ok {
			return errors.New("OAuth private origin must also be allowed")
		}
	}
	return nil
}

func validateSafeValue(field, value string) error {
	if value == "" || !utf8.ValidString(value) {
		return errors.New(field + " is required and must be valid UTF-8")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return errors.New(field + " must not contain control characters")
		}
	}
	return nil
}

func validateHTTPURL(field, raw string, httpsOnly bool) (*url.URL, error) {
	if raw == "" {
		return nil, errors.New(field + " is required")
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New(field + " is invalid")
	}
	if (httpsOnly && !strings.EqualFold(u.Scheme, "https")) || (!httpsOnly && !strings.EqualFold(u.Scheme, oauthHTTPURLScheme) && !strings.EqualFold(u.Scheme, "https")) {
		return nil, errors.New(field + " is invalid")
	}
	if u.Hostname() == "" || !validPort(u) {
		return nil, errors.New(field + " is invalid")
	}
	return u, nil
}

func validateOrigin(field, raw string) (*url.URL, error) {
	u, err := validateHTTPURL(field, raw, false)
	if err != nil || u.Path != "" && u.Path != "/" || u.RawQuery != "" {
		return nil, errors.New(field + " is invalid")
	}
	return u, nil
}

func urlOrigin(u *url.URL) string {
	if u == nil {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	hostname := strings.ToLower(u.Hostname())
	if ip := net.ParseIP(hostname); ip != nil {
		hostname = ip.String()
	}
	port := u.Port()
	if port == "" || scheme == "https" && port == "443" || scheme == oauthHTTPURLScheme && port == "80" {
		port = ""
	}
	if strings.Contains(hostname, ":") {
		hostname = "[" + hostname + "]"
	}
	if port != "" {
		hostname = net.JoinHostPort(strings.Trim(hostname, "[]"), port)
	}
	return scheme + "://" + hostname
}

func scopeFilter(allowed []string) func([]string) []string {
	set := make(map[string]struct{}, len(allowed))
	for _, scope := range allowed {
		set[scope] = struct{}{}
	}
	return func(discovered []string) []string {
		filtered := make([]string, 0, len(discovered))
		for _, scope := range discovered {
			if _, ok := set[scope]; ok && !slices.Contains(filtered, scope) {
				filtered = append(filtered, scope)
			}
		}
		return filtered
	}
}

// newOAuthPersistenceCore restores credentials and configures the official SDK
// handler hooks. The caller owns the HTTP client and supplies the presenter.
func newOAuthPersistenceCore(ctx context.Context, resource string, opts OAuthOptions, client *http.Client, fetcher auth.AuthorizationCodeFetcher) (*oauthCredentialState, *auth.AuthorizationCodeHandler, error) {
	if client == nil {
		return nil, nil, errors.New("OAuth HTTP client is required")
	}
	registration, origins, err := validateOAuthOptions(opts)
	if err != nil {
		return nil, nil, err
	}
	canonical, err := canonicalOAuthResource(resource)
	if err != nil {
		return nil, nil, err
	}
	resourceURL, _ := url.Parse(canonical)
	origins[urlOrigin(resourceURL)] = struct{}{}
	if err := validatePrivateOrigins(origins, opts.Network); err != nil {
		return nil, nil, err
	}
	reader, writer, err := oauthPersistence(opts)
	if err != nil {
		return nil, nil, err
	}
	identity := oauthCredentialIdentity{Profile: opts.Subject.Profile, Principal: opts.Subject.Principal, Resource: canonical, Issuer: opts.Issuer, ClientKind: registration.kind, ClientID: registration.clientID}
	state, err := restoreOAuthCredential(ctx, reader, writer, identity, registration, origins, client, opts.RequestRefreshToken, opts.AllowInMemoryRefresh)
	if err != nil {
		return nil, nil, err
	}
	cfg := &auth.AuthorizationCodeHandlerConfig{
		PreregisteredClient:      registration.sdk,
		RedirectURL:              opts.RedirectURL,
		AuthorizationCodeFetcher: fetcher,
		ScopeFilter:              scopeFilter(opts.AllowedScopes),
		RequestRefreshToken:      opts.RequestRefreshToken,
		AcceptUnadvertisedIss:    true,
		Client:                   client,
		InitialTokenSource:       state.initialTokenSource(),
		NewTokenSource:           state.newTokenSource,
	}
	if registration.cimd != "" {
		cfg.ClientIDMetadataDocumentConfig = &auth.ClientIDMetadataDocumentConfig{URL: registration.cimd}
	}
	handler, err := auth.NewAuthorizationCodeHandler(cfg)
	if err != nil {
		return nil, nil, projectOAuthError(err)
	}
	return state, handler, nil
}

func oauthContext(ctx context.Context, client *http.Client) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, client)
}

type authorizationChallengeKey struct {
	authorization [sha256.Size]byte
	challenge     [sha256.Size]byte
	status        int
}

type authorizationFlight struct {
	key       authorizationChallengeKey
	done      chan struct{}
	err       error
	completed bool
}

// OAuthController owns one official SDK authorization handler and the durable
// credential state for one MCP resource.
type OAuthController struct {
	state     *oauthCredentialState
	handler   *auth.AuthorizationCodeHandler
	authorize func(context.Context, *http.Request, *http.Response) error
	presenter OAuthPresenter
	client    *http.Client
	transport *oauthHTTPTransport

	flightMu sync.Mutex
	flight   *authorizationFlight
	closed   bool
	active   sync.WaitGroup

	lifetimeCtx context.Context
	cancel      context.CancelFunc
	closeDone   chan struct{}
}

// NewOAuthController constructs the OAuth handler once. The caller context
// bounds credential restoration; the returned controller has its own lifetime
// and is safe for concurrent use until Close.
func NewOAuthController(ctx context.Context, resource string, opts OAuthOptions) (*OAuthController, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := canonicalOAuthResource(resource); err != nil {
		return nil, err
	}
	client, transport, err := newOAuthHTTPClient(resource, opts)
	if err != nil {
		return nil, err
	}
	opts, err = resolvePreparedDCR(ctx, resource, opts, client)
	if err != nil {
		transport.base.CloseIdleConnections()
		return nil, projectOAuthError(err)
	}
	if opts.dcr != nil {
		transport.dcrPublicClientID = opts.dcr.clientID
	}
	if _, _, err := validateOAuthOptions(opts); err != nil {
		transport.base.CloseIdleConnections()
		return nil, err
	}
	lifetimeCtx, lifetimeCancel := context.WithCancel(context.Background())
	controller := &OAuthController{
		presenter:   opts.Presenter,
		client:      client,
		transport:   transport,
		lifetimeCtx: lifetimeCtx,
		cancel:      lifetimeCancel,
		closeDone:   make(chan struct{}),
	}
	fetcher := controller.presentAuthorization
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = defaultOAuthTimeout
	}
	restoreCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	state, handler, err := newOAuthPersistenceCore(restoreCtx, resource, opts, client, fetcher)
	if err != nil {
		lifetimeCancel()
		transport.base.CloseIdleConnections()
		return nil, projectOAuthError(err)
	}
	state.lifetime = lifetimeCtx
	controller.state = state
	controller.handler = handler
	controller.authorize = handler.Authorize
	return controller, nil
}

func (c *OAuthController) presentAuthorization(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
	if c.state == nil || c.state.writer == nil {
		return nil, projectOAuthError(ErrOAuthUnavailable)
	}
	if c.presenter == nil {
		return nil, projectOAuthError(ErrOAuthLoginRequired)
	}
	if args == nil {
		return nil, projectOAuthError(ErrOAuthUnavailable)
	}
	authorizationURL, err := validateHTTPURL("OAuth authorization URL", args.URL, false)
	if err != nil || urlOrigin(authorizationURL) != c.transport.issuerOrigin {
		return nil, projectOAuthError(ErrOAuthUnavailable)
	}
	result, err := c.presenter.PresentAuthorization(ctx, args.URL)
	return result, projectOAuthError(err)
}

// TokenSource returns the controller's current durable token source. A nil
// source means the resource has not required authorization yet.
func (c *OAuthController) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.flightMu.Lock()
	closed := c.closed
	c.flightMu.Unlock()
	if closed {
		return nil, projectOAuthError(ErrOAuthUnavailable)
	}
	return c.state.tokenSource(ctx), nil
}

func (c *OAuthController) operationContext(ctx context.Context) (context.Context, func()) {
	opCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.lifetimeCtx, cancel)
	if c.lifetimeCtx.Err() != nil {
		cancel()
	}
	return opCtx, func() {
		stop()
		cancel()
	}
}

func oauthAuthorizationChallengeKey(req *http.Request, resp *http.Response) authorizationChallengeKey {
	var key authorizationChallengeKey
	if req != nil {
		key.authorization = sha256.Sum256([]byte(req.Header.Get("Authorization")))
	}
	if resp != nil {
		key.status = resp.StatusCode
		key.challenge = sha256.Sum256([]byte(resp.Header.Get("WWW-Authenticate")))
	}
	return key
}

func (c *OAuthController) authorizationAllowed(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.state == nil || c.state.writer == nil {
		return projectOAuthError(ErrOAuthUnavailable)
	}
	return nil
}

// Authorize coalesces concurrent and late-arriving equivalent challenges for
// this credential identity while leaving cancellation bounded by each caller's
// context. A completed outcome remains attached to its challenge key until a
// different credential/challenge arrives or ResetCredential invalidates it.
func (c *OAuthController) Authorize(ctx context.Context, req *http.Request, resp *http.Response) error { //nolint:gocyclo // single-flight state transitions stay explicit.
	if err := c.authorizationAllowed(ctx); err != nil {
		closeOAuthResponse(resp)
		return err
	}
	c.flightMu.Lock()
	if c.closed {
		c.flightMu.Unlock()
		closeOAuthResponse(resp)
		return projectOAuthError(ErrOAuthUnavailable)
	}
	c.active.Add(1)
	c.flightMu.Unlock()
	defer c.active.Done()

	ctx, cancel := c.operationContext(ctx)
	defer cancel()
	if c.presenter == nil {
		closeOAuthResponse(resp)
		return projectOAuthError(ErrOAuthLoginRequired)
	}
	key := oauthAuthorizationChallengeKey(req, resp)
	for {
		if err := ctx.Err(); err != nil {
			closeOAuthResponse(resp)
			return err
		}
		c.flightMu.Lock()
		if c.closed {
			c.flightMu.Unlock()
			closeOAuthResponse(resp)
			return projectOAuthError(ErrOAuthUnavailable)
		}
		if existing := c.flight; existing != nil {
			sameChallenge := existing.key == key
			if existing.completed {
				if sameChallenge && !errors.Is(existing.err, context.Canceled) && !errors.Is(existing.err, context.DeadlineExceeded) {
					err := existing.err
					c.flightMu.Unlock()
					closeOAuthResponse(resp)
					return err
				}
				// A different completed challenge, or the first caller after a
				// cancelled equivalent challenge, replaces the retained outcome.
			} else {
				c.flightMu.Unlock()
				if sameChallenge {
					closeOAuthResponse(resp)
				}
				select {
				case <-ctx.Done():
					if !sameChallenge {
						closeOAuthResponse(resp)
					}
					return ctx.Err()
				case <-existing.done:
					if sameChallenge {
						if (errors.Is(existing.err, context.Canceled) || errors.Is(existing.err, context.DeadlineExceeded)) && ctx.Err() == nil {
							continue
						}
						return existing.err
					}
					continue
				}
			}
		}
		flight := &authorizationFlight{key: key, done: make(chan struct{})}
		c.flight = flight
		c.flightMu.Unlock()

		if err := c.state.beginAuthorization(ctx); err != nil {
			closeOAuthResponse(resp)
			c.completeAuthorizationFlight(flight, err)
			return err
		}
		err := projectOAuthError(c.authorize(ctx, req, resp))
		c.completeAuthorizationFlight(flight, err)
		return err
	}
}

func (c *OAuthController) completeAuthorizationFlight(flight *authorizationFlight, err error) {
	c.flightMu.Lock()
	flight.err = err
	flight.completed = true
	close(flight.done)
	c.flightMu.Unlock()
}

// ResetCredential conditionally deletes the current record and clears the live
// token source. A concurrent CAS winner is preserved and adopted.
func (c *OAuthController) ResetCredential(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.flightMu.Lock()
	closed := c.closed
	c.flightMu.Unlock()
	if closed {
		return projectOAuthError(ErrOAuthUnavailable)
	}
	if err := c.state.reset(ctx); err != nil {
		return err
	}
	c.flightMu.Lock()
	if c.flight != nil && c.flight.completed {
		c.flight = nil
	}
	c.flightMu.Unlock()
	return nil
}

// Close cancels the controller lifetime, joins authorization leaders and
// waiters, and releases the shared OAuth/resource HTTP idle pool. It is
// idempotent and does not close the borrowed credential store. Presenter
// implementations must honor cancellation: Close does not return while host
// code can still access controller-owned state.
func (c *OAuthController) Close() error {
	if c == nil {
		return nil
	}
	c.flightMu.Lock()
	if c.closed {
		done := c.closeDone
		c.flightMu.Unlock()
		<-done
		return nil
	}
	c.closed = true
	c.cancel()
	c.flightMu.Unlock()

	c.active.Wait()
	// Credential writes and deletes run while holding state.mu and derive their
	// contexts from the controller lifetime. Join that critical section after
	// cancellation so Close cannot return while a store operation can still
	// publish a new record or clear the current one.
	if c.state != nil {
		_ = c.state.waitUntilIdle()
	}
	c.transport.base.CloseIdleConnections()
	close(c.closeDone)
	return nil
}

var _ auth.OAuthHandler = (*OAuthController)(nil)
