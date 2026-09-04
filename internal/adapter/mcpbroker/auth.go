package mcpbroker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

const (
	defaultAuthorizationTTL = 10 * time.Minute
	defaultExchangeTimeout  = 30 * time.Second
	maxCallbackQueryBytes   = 8 << 10
	maxCallbackValueBytes   = 2048
)

// closeDrainTimeout bounds how long Runtime.Close waits for in-flight
// attachment operations to unwind (their context is cancelled by
// markDeletedLocked; this bounds the wait for them to actually return) before
// giving up and letting the caller tear down owned resources anyway. Matches
// the server package's engineCloseTimeout — one shutdown budget, not two.
// A package var (not const) so tests can shrink it to prove the bound fires.
var closeDrainTimeout = 10 * time.Second

type oauthRoute struct {
	authorizationEndpoint string
	tokenEndpoint         string
	callbackURL           string
	clientID              string
	secretEnv             string
	scopes                []string
	requestRefresh        bool
	// resource is the RFC 8707 resource indicator sent with every authorization
	// and token request: the canonical URI of the MCP server this route's
	// grant is scoped to. Required by the MCP Authorization Spec 2025-06-18.
	resource string
}

func compileOAuthRoute(callbackURL string, declaration permconfig.MCPServerProfile) (*oauthRoute, error) {
	profile := declaration.Auth.OAuth
	if profile == nil || profile.Upstream == nil || profile.Upstream.Mode != "oauth2" || profile.Upstream.OAuth2 == nil {
		return nil, fmt.Errorf("%w: route %q requires trusted explicit OAuth2 endpoints", ErrProtectedRouteUnsupported, declaration.Name)
	}
	if profile.Client.Mode != "preregistered" || profile.Client.Preregistered == nil {
		return nil, fmt.Errorf("%w: route %q requires a preregistered OAuth client", ErrProtectedRouteUnsupported, declaration.Name)
	}
	for label, raw := range map[string]string{
		"authorization endpoint": profile.Upstream.OAuth2.AuthorizationEndpoint,
		"token endpoint":         profile.Upstream.OAuth2.TokenEndpoint,
		"callback URL":           callbackURL,
	} {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return nil, fmt.Errorf("%w: route %q has invalid %s", ErrInvalidCatalogue, declaration.Name, label)
		}
		if parsed.Scheme != "https" {
			return nil, fmt.Errorf("%w: route %q requires HTTPS for %s", ErrInvalidCatalogue, declaration.Name, label)
		}
	}
	if profile.Client.Preregistered.ID == "" || profile.Client.Preregistered.SecretEnv == "" || len(profile.Scopes) == 0 {
		return nil, fmt.Errorf("%w: route %q has incomplete OAuth client metadata", ErrInvalidCatalogue, declaration.Name)
	}
	return &oauthRoute{
		authorizationEndpoint: profile.Upstream.OAuth2.AuthorizationEndpoint,
		tokenEndpoint:         profile.Upstream.OAuth2.TokenEndpoint,
		callbackURL:           callbackURL,
		clientID:              profile.Client.Preregistered.ID,
		secretEnv:             profile.Client.Preregistered.SecretEnv,
		scopes:                append([]string(nil), profile.Scopes...),
		requestRefresh:        profile.RequestRefreshToken,
		resource:              declaration.URL,
	}, nil
}

func (c *Catalogue) protected() bool {
	for _, route := range c.routes {
		if route.oauth != nil {
			return true
		}
	}
	return false
}

type oauthRuntimeOptions struct {
	httpClient           *http.Client
	resolveSecret        func(context.Context, string) (string, error)
	now                  func() time.Time
	random               func([]byte) (int, error)
	ttl                  time.Duration
	timeout              time.Duration
	allowLoopback        bool
	testRootCAs          *x509.CertPool
	testHelper           interface{ Helper() }
	testBrokerHTTPClient *http.Client
	// forcedTokenEndpoint forces the hardened OAuth token client to be built
	// for this endpoint even when the compiled catalogue has no static oauth
	// route: a bundled ToolHive Process may have a protected authorization
	// target used only by pre-prompt workspace enrollment (every protected
	// backend requires live discovery, none has a static tool declaration).
	forcedTokenEndpoint string
}

func defaultOAuthRuntimeOptions() oauthRuntimeOptions {
	return oauthRuntimeOptions{
		resolveSecret: func(_ context.Context, name string) (string, error) {
			value, ok := os.LookupEnv(name)
			if !ok || value == "" {
				return "", errors.New("OAuth client secret is unavailable")
			}
			return value, nil
		},
		now: time.Now, random: rand.Read, ttl: defaultAuthorizationTTL, timeout: defaultExchangeTimeout,
	}
}

// Option configures process-owned OAuth custody without widening the broker contract.
type Option func(*Runtime)

// WithAuthorizedCaller installs the protected-route transport seam.
func WithAuthorizedCaller(caller AuthorizedCaller) Option {
	return func(runtime *Runtime) { runtime.authorizedCaller = caller }
}

// WithOAuthLoopbackForTest enables only an in-process TLS test token endpoint.
// No production configuration surface can relax the hardened client's IP policy.
func WithOAuthLoopbackForTest(t interface{ Helper() }, roots *x509.CertPool) Option {
	t.Helper()
	return func(runtime *Runtime) {
		runtime.oauth.allowLoopback = true
		runtime.oauth.testRootCAs = roots
		runtime.oauth.testHelper = t
	}
}

// WithBrokerHTTPClientForTest supplies a client that trusts the in-process TLS
// vMCP handler. It is honored only together with WithOAuthLoopbackForTest.
func WithBrokerHTTPClientForTest(t interface{ Helper() }, client *http.Client) Option {
	t.Helper()
	return func(runtime *Runtime) { runtime.oauth.testBrokerHTTPClient = client }
}

// WithOAuthSecretResolver resolves trusted secret references from P07 declarations.
func WithOAuthSecretResolver(resolver func(context.Context, string) (string, error)) Option {
	return func(runtime *Runtime) {
		if resolver != nil {
			runtime.oauth.resolveSecret = resolver
		}
	}
}

// WithOAuthLimits overrides transaction and network bounds. Non-positive values retain defaults.
func WithOAuthLimits(transactionTTL, exchangeTimeout time.Duration) Option {
	return func(runtime *Runtime) {
		if transactionTTL > 0 {
			runtime.oauth.ttl = transactionTTL
		}
		if exchangeTimeout > 0 {
			runtime.oauth.timeout = exchangeTimeout
		}
	}
}

// withHardenedTokenEndpoint is unexported: only newToolHiveProcess may force
// the hardened OAuth token client to exist for a Process-owned protected
// target that has no static oauth route in the compiled catalogue.
func withHardenedTokenEndpoint(endpoint string) Option {
	return func(runtime *Runtime) { runtime.oauth.forcedTokenEndpoint = endpoint }
}

const (
	// Resolved authorization records are retained for idempotent status/cancel.
	// At the cap, new authorizations fail closed rather than evicting replay state.
	maxAuthorizationRecords = 1024
	// Executed protected calls are never evicted: losing a claim could replay an
	// ambiguously completed mutation. At the cap, further calls fail closed.
	maxExecutedCallsPerGrant = 4096
)

type callbackState struct {
	logical     *logicalSession
	transaction *authorizationTransaction
}

type authorizationIdentity struct {
	id      string
	binding session.AuthorizationBinding
}

type authorizationTransaction struct {
	identity     authorizationIdentity
	route        *oauthRoute
	backend      string
	callHash     [32]byte
	callID       session.ToolCallID
	state        string
	verifier     string
	clientSecret string
	expiresAt    time.Time
	status       session.AuthorizationStatus
	claimed      bool
	cancel       context.CancelFunc
	// bundleBackends is nil for an ordinary tool-call-bound authorization. A
	// non-nil value marks the aggregate pre-prompt ToolHive enrollment. Its
	// resulting credential is broker-scoped and never copied into backend grants.
	bundleBackends []string
}

type oauthGrant struct {
	config       *oauth2.Config
	token        *oauth2.Token
	firstCall    [32]byte
	firstPending bool
	executed     map[session.ToolCallID][32]byte
}

func (r *Runtime) registerCallbackState(state string, logical *logicalSession, transaction *authorizationTransaction) bool {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if _, exists := r.states[state]; exists {
		return false
	}
	r.states[state] = callbackState{logical: logical, transaction: transaction}
	return true
}

func (r *Runtime) claimCallbackState(state string) (callbackState, bool) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	indexed, ok := r.states[state]
	if ok {
		delete(r.states, state)
	}
	return indexed, ok
}

func (r *Runtime) removeCallbackState(state string, transaction *authorizationTransaction) {
	if state == "" {
		return
	}
	r.stateMu.Lock()
	if indexed, ok := r.states[state]; ok && indexed.transaction == transaction {
		delete(r.states, state)
	}
	r.stateMu.Unlock()
}

func opaque(random func([]byte) (int, error)) (string, error) {
	bytes := make([]byte, 32)
	if _, err := random(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func callHash(call session.ToolCall) [32]byte {
	h := sha256.New()
	_, _ = h.Write([]byte(call.ID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(call.Name))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(call.Args)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

type protectedSessionTool struct {
	*sessionTool
}

// existingAuthorizationLocked checks whether backend/hash already has a
// resolved outcome (a granted credential, a pending transaction, or a
// capacity/mismatch error) — the idempotent fast path RequestAuthorization
// takes both BEFORE and AFTER releasing logical.mu for secret resolution, so
// a concurrent caller's result during that window is never overwritten.
// resolved reports whether the caller should return immediately with
// (result, found, err); resolved == false means a new transaction must be
// created.
func existingAuthorizationLocked(logical *logicalSession, backend string, hash [32]byte) (result session.ExternalAuthorization, found, resolved bool, err error) {
	if logical.deleted {
		return session.ExternalAuthorization{}, false, true, contract.ErrStateUnavailable
	}
	if grant := logical.grants[backend]; grant != nil {
		if grant.firstPending && grant.firstCall != hash {
			return session.ExternalAuthorization{}, false, true, errors.New("broker authorization is bound to another effective tool call")
		}
		return session.ExternalAuthorization{}, false, true, nil
	}
	for _, transaction := range logical.authorizations {
		if transaction.backend == backend && transaction.status == session.AuthorizationPending {
			if transaction.callHash != hash {
				return session.ExternalAuthorization{}, false, true, errors.New("broker route already has a different pending authorization")
			}
			return transaction.external(), true, true, nil
		}
	}
	if len(logical.authorizations) >= maxAuthorizationRecords {
		return session.ExternalAuthorization{}, false, true, errors.New("broker authorization record capacity reached")
	}
	return session.ExternalAuthorization{}, false, false, nil
}

func (t *protectedSessionTool) RequestAuthorization(ctx context.Context, call session.ToolCall) (session.ExternalAuthorization, bool, error) {
	opCtx, done, err := t.attachment.beginOperation(ctx)
	if err != nil {
		return session.ExternalAuthorization{}, false, err
	}
	defer done()
	if call.Name != t.route.spec.Name {
		return session.ExternalAuthorization{}, false, errors.New("broker authorization call does not match wrapper")
	}
	logical := t.attachment.logical
	hash := callHash(call)

	logical.mu.Lock()
	if result, found, resolved, err := existingAuthorizationLocked(logical, t.route.backend, hash); resolved {
		logical.mu.Unlock()
		return result, found, err
	}
	logical.mu.Unlock()

	// Secret resolution is potentially slow (a secret-manager round trip) and
	// must NOT run while holding logical.mu: that mutex also gates session
	// deletion and Runtime.Close's ability to even reach the point of
	// cancelling this operation's context, so holding it here would block
	// unrelated shutdown/deletion for the duration of the round trip.
	var secret string
	if t.route.oauth.secretEnv != "" {
		secret, err = t.attachment.runtime.oauth.resolveSecret(opCtx, t.route.oauth.secretEnv)
		if err != nil {
			return session.ExternalAuthorization{}, false, err
		}
	}
	id, err := opaque(t.attachment.runtime.oauth.random)
	if err != nil {
		return session.ExternalAuthorization{}, false, fmt.Errorf("create authorization identity: %w", err)
	}
	binding, err := opaque(t.attachment.runtime.oauth.random)
	if err != nil {
		return session.ExternalAuthorization{}, false, fmt.Errorf("create authorization binding: %w", err)
	}
	state, err := opaque(t.attachment.runtime.oauth.random)
	if err != nil {
		return session.ExternalAuthorization{}, false, fmt.Errorf("create callback state: %w", err)
	}
	verifier, err := opaque(t.attachment.runtime.oauth.random)
	if err != nil {
		return session.ExternalAuthorization{}, false, fmt.Errorf("create PKCE verifier: %w", err)
	}

	logical.mu.Lock()
	defer logical.mu.Unlock()
	// Re-verify: a concurrent call (or deletion) may have already resolved this
	// exact backend/call while the secret was resolving above.
	if result, found, resolved, err := existingAuthorizationLocked(logical, t.route.backend, hash); resolved {
		return result, found, err
	}
	copyRoute := *t.route.oauth
	transaction := &authorizationTransaction{
		identity: authorizationIdentity{id: id, binding: session.AuthorizationBinding(binding)}, route: &copyRoute,
		backend: t.route.backend, callHash: hash, callID: call.ID, state: state, verifier: verifier, clientSecret: secret,
		expiresAt: t.attachment.runtime.oauth.now().Add(t.attachment.runtime.oauth.ttl), status: session.AuthorizationPending,
	}
	logical.authorizations[transaction.identity] = transaction
	if !t.attachment.runtime.registerCallbackState(state, logical, transaction) {
		delete(logical.authorizations, transaction.identity)
		transaction.clientSecret = ""
		transaction.verifier = ""
		transaction.state = ""
		return session.ExternalAuthorization{}, false, errors.New("create unique callback state")
	}
	return transaction.external(), true, nil
}

func (t *authorizationTransaction) external() session.ExternalAuthorization {
	return session.ExternalAuthorization{ID: t.identity.id, DisplayName: t.backend, Binding: t.identity.binding, ExpiresAt: t.expiresAt}
}

func (t *authorizationTransaction) oauthConfig(secret string) *oauth2.Config {
	// The hardened OAuth token client (internal/adapter/mcp.NewHardenedOAuthTokenClient)
	// unconditionally requires an HTTP Basic Authorization header on every token
	// request, including the protectedTarget public client (no secretEnv): Basic
	// with an empty password is a valid encoding of that public client's identity.
	// AuthStyleAutoDetect never satisfies that requirement, so it is never used.
	return &oauth2.Config{ClientID: t.route.clientID, ClientSecret: secret, RedirectURL: t.route.callbackURL,
		Scopes: append([]string(nil), t.route.scopes...), Endpoint: oauth2.Endpoint{
			AuthURL: t.route.authorizationEndpoint, TokenURL: t.route.tokenEndpoint, AuthStyle: oauth2.AuthStyleInHeader,
		}}
}

// authCodeOptions is the single option builder shared by every authorization
// and token request this transaction makes, so the PKCE challenge, refresh
// request, and RFC 8707 resource indicator can never drift between the
// per-tool-call and workspace-enrollment presentation paths.
func (t *authorizationTransaction) authCodeOptions() []oauth2.AuthCodeOption {
	challenge := sha256.Sum256([]byte(t.verifier))
	options := []oauth2.AuthCodeOption{
		oauth2.SetAuthURLParam("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:])),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	}
	if t.route.requestRefresh {
		options = append(options, oauth2.AccessTypeOffline)
	}
	if t.route.resource != "" {
		options = append(options, oauth2.SetAuthURLParam("resource", t.route.resource))
	}
	return options
}

func lookupAuthorization(logical *logicalSession, authorization session.ExternalAuthorization) (*authorizationTransaction, error) {
	transaction := logical.authorizations[authorizationIdentity{id: authorization.ID, binding: authorization.Binding}]
	if transaction == nil {
		return nil, contract.ErrAuthorizationNotFound
	}
	return transaction, nil
}

func (a *Attachment) lookupAuthorizationLocked(authorization session.ExternalAuthorization) (*authorizationTransaction, error) {
	transaction, err := lookupAuthorization(a.logical, authorization)
	if errors.Is(err, contract.ErrAuthorizationNotFound) && a.runtime.catalogue.protected() && len(a.logical.authorizations) == 0 {
		// A protected authorization reference with no process-local transaction is
		// the explicit in-process restart posture: never mint replacement state.
		return nil, contract.ErrStateUnavailable
	}
	return transaction, err
}

// PresentAuthorization returns a live URL for the exact process-local transaction.
func (a *Attachment) PresentAuthorization(ctx context.Context, authorization session.ExternalAuthorization) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	_, done, err := a.beginOperation(ctx)
	if err != nil {
		return "", err
	}
	defer done()
	a.logical.mu.Lock()
	defer a.logical.mu.Unlock()
	transaction, err := a.lookupAuthorizationLocked(authorization)
	if err != nil {
		return "", err
	}
	a.expireLocked(transaction)
	if transaction.status != session.AuthorizationPending || transaction.claimed {
		return "", contract.ErrAuthorizationNotFound
	}
	cfg := transaction.oauthConfig(transaction.clientSecret)
	return cfg.AuthCodeURL(transaction.state, transaction.authCodeOptions()...), nil
}

// AuthorizationStatus reports the exact transaction's current lifecycle status.
func (a *Attachment) AuthorizationStatus(ctx context.Context, authorization session.ExternalAuthorization) (session.AuthorizationStatus, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	_, done, err := a.beginOperation(ctx)
	if err != nil {
		return "", err
	}
	defer done()
	a.logical.mu.Lock()
	defer a.logical.mu.Unlock()
	transaction, err := a.lookupAuthorizationLocked(authorization)
	if err != nil {
		return "", err
	}
	a.expireLocked(transaction)
	return transaction.status, nil
}

func (a *Attachment) expireLocked(transaction *authorizationTransaction) {
	if transaction.status == session.AuthorizationPending && !a.runtime.oauth.now().Before(transaction.expiresAt) {
		transaction.status = session.AuthorizationExpired
		a.runtime.removeCallbackState(transaction.state, transaction)
		if transaction.cancel != nil {
			transaction.cancel()
		}
		transaction.clientSecret = ""
		transaction.verifier = ""
		transaction.state = ""
	}
}

// CancelAuthorization idempotently settles only the exact pending transaction.
func (a *Attachment) CancelAuthorization(ctx context.Context, authorization session.ExternalAuthorization) (contract.CancelOutcome, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	_, done, err := a.beginOperation(ctx)
	if err != nil {
		return "", err
	}
	defer done()
	a.logical.mu.Lock()
	defer a.logical.mu.Unlock()
	transaction, err := a.lookupAuthorizationLocked(authorization)
	if err != nil {
		return "", err
	}
	a.expireLocked(transaction)
	switch transaction.status {
	case session.AuthorizationPending:
		transaction.status = session.AuthorizationCancelled
		a.runtime.removeCallbackState(transaction.state, transaction)
		if transaction.cancel != nil {
			transaction.cancel()
		}
		transaction.clientSecret = ""
		transaction.verifier = ""
		transaction.state = ""
		return contract.CancelCancelled, nil
	case session.AuthorizationCancelled:
		return contract.CancelAlreadyCancelled, nil
	default:
		return contract.CancelAlreadyResolved, nil
	}
}

func (t *protectedSessionTool) AbortAuthorization(ctx context.Context, authorization session.ExternalAuthorization) error {
	_, err := t.attachment.CancelAuthorization(ctx, authorization)
	return err
}

// CallbackHandler returns the process-owned, bounded one-time OAuth callback handler.
func (r *Runtime) CallbackHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.Body != nil && request.ContentLength > 0 || len(request.URL.RawQuery) > maxCallbackQueryBytes {
			http.Error(w, "invalid OAuth callback", http.StatusBadRequest)
			return
		}
		values, err := url.ParseQuery(request.URL.RawQuery)
		if err != nil || !exactCallbackValues(values) {
			http.Error(w, "invalid OAuth callback", http.StatusBadRequest)
			return
		}
		if callbackErr := values.Get("error"); callbackErr != "" {
			if err := r.handleCallbackError(values.Get("state")); err != nil {
				http.Error(w, "invalid OAuth callback", http.StatusBadRequest)
				return
			}
			http.Error(w, "OAuth authorization was not granted", http.StatusBadRequest)
			return
		}
		if err := r.handleCallback(request.Context(), values.Get("code"), values.Get("state")); err != nil {
			http.Error(w, "invalid OAuth callback", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(authorizationCompletePage))
	})
}

// authorizationCompletePage is the terminal page shown in the user's browser on
// a successful callback. It exists so completion is VISIBLE — an HTTP 204 here
// left the tab blank with no navigation the user could distinguish from "stuck",
// which is what actually happened during live debugging: the OAuth round trip
// completed correctly (confirmed via server logs) but the blank page read as a
// frozen browser, leading to a closed window and a confusing retry against an
// already-consumed one-shot authorization.
const authorizationCompletePage = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Authorization complete</title></head>
<body style="font-family:system-ui,sans-serif;text-align:center;padding:4rem">
<h1>Authorization complete</h1>
<p>You can close this window now.</p>
</body></html>`

func exactCallbackValues(values url.Values) bool {
	if len(values) < 2 || len(values) > 3 || len(values["state"]) != 1 {
		return false
	}
	hasCode := len(values["code"]) == 1
	hasError := len(values["error"]) == 1
	if hasCode == hasError {
		return false
	}
	for key, list := range values {
		allowed := key == "state" || hasCode && (key == "code" || key == "scope") || hasError && (key == "error" || key == "error_description")
		if !allowed || len(list) != 1 || list[0] == "" || len(list[0]) > maxCallbackValueBytes {
			return false
		}
	}
	return true
}

func (r *Runtime) handleCallbackError(state string) error {
	indexed, ok := r.claimCallbackState(state)
	if !ok {
		return contract.ErrAuthorizationNotFound
	}
	logical, transaction := indexed.logical, indexed.transaction
	logical.mu.Lock()
	defer logical.mu.Unlock()
	if logical.deleted || transaction.status != session.AuthorizationPending || transaction.claimed {
		return contract.ErrAuthorizationNotFound
	}
	if !r.oauth.now().Before(transaction.expiresAt) {
		transaction.status = session.AuthorizationExpired
	} else {
		transaction.claimed = true
		transaction.status = session.AuthorizationFailed
	}
	if transaction.cancel != nil {
		transaction.cancel()
		transaction.cancel = nil
	}
	transaction.clientSecret = ""
	transaction.verifier = ""
	transaction.state = ""
	return nil
}

func (r *Runtime) handleCallback(ctx context.Context, code, state string) error {
	r.mu.RLock()
	closed := r.closed
	r.mu.RUnlock()
	if closed {
		return contract.ErrStateUnavailable
	}
	indexed, ok := r.claimCallbackState(state)
	if !ok {
		return contract.ErrAuthorizationNotFound
	}
	logical, transaction := indexed.logical, indexed.transaction
	logical.mu.Lock()
	if logical.deleted || transaction.status != session.AuthorizationPending || transaction.claimed {
		logical.mu.Unlock()
		return contract.ErrAuthorizationNotFound
	}
	if !r.oauth.now().Before(transaction.expiresAt) {
		transaction.status = session.AuthorizationExpired
		transaction.clientSecret = ""
		transaction.verifier = ""
		transaction.state = ""
		logical.mu.Unlock()
		return contract.ErrAuthorizationNotFound
	}
	transaction.claimed = true
	exchangeCtx, exchangeCancel := context.WithTimeout(context.WithoutCancel(ctx), r.oauth.timeout)
	transaction.cancel = exchangeCancel
	cfg := transaction.oauthConfig(transaction.clientSecret)
	verifier := transaction.verifier
	transaction.clientSecret = ""
	transaction.verifier = ""
	transaction.state = ""
	logical.mu.Unlock()

	exchangeCtx = context.WithValue(exchangeCtx, oauth2.HTTPClient, r.oauth.httpClient)
	exchangeOptions := []oauth2.AuthCodeOption{oauth2.VerifierOption(verifier)}
	if transaction.route.resource != "" {
		exchangeOptions = append(exchangeOptions, oauth2.SetAuthURLParam("resource", transaction.route.resource))
	}
	token, err := cfg.Exchange(exchangeCtx, code, exchangeOptions...)
	exchangeCancel()
	logical.mu.Lock()
	defer logical.mu.Unlock()
	transaction.cancel = nil
	if logical.deleted || transaction.status != session.AuthorizationPending {
		return contract.ErrStateUnavailable
	}
	if err != nil || !validBearerToken(token) {
		transaction.status = session.AuthorizationFailed
		return errors.New("OAuth token exchange failed")
	}
	grant := &oauthGrant{config: cfg, token: token, firstCall: transaction.callHash, firstPending: true, executed: make(map[session.ToolCallID][32]byte)}
	if transaction.bundleBackends != nil {
		// This is the one opaque credential for the ToolHive broker operation.
		// Its claims and ToolHive-owned upstream credentials are never decoded or
		// copied into mecatl's per-backend grant map.
		grant.firstPending = false
		logical.brokerCredential = grant
	} else {
		logical.grants[transaction.backend] = grant
	}
	transaction.status = session.AuthorizationGranted
	return nil
}

func validBearerToken(token *oauth2.Token) bool {
	return token != nil && token.AccessToken != "" && strings.EqualFold(token.TokenType, "bearer")
}

type scopedTokenSource struct {
	runtime *Runtime
	logical *logicalSession
	backend string
	// ctx is the operation's own context (from beginOperation), so a refresh
	// round trip is cancelled with the operation instead of running until
	// s.runtime.oauth.timeout regardless of the caller giving up.
	ctx context.Context
}

func (s *scopedTokenSource) Token() (*oauth2.Token, error) {
	s.logical.mu.Lock()
	if s.logical.deleted {
		s.logical.mu.Unlock()
		return nil, contract.ErrStateUnavailable
	}
	grant := s.logical.grants[s.backend]
	if grant == nil {
		s.logical.mu.Unlock()
		return nil, contract.ErrAuthorizationNotFound
	}
	if grant.token.Valid() {
		token := cloneToken(grant.token)
		s.logical.mu.Unlock()
		return token, nil
	}
	config, current := grant.config, grant.token
	s.logical.mu.Unlock()

	// The network round trip must NOT run under logical.mu: that mutex also
	// gates session deletion, and context.Background() here would ignore the
	// operation's own cancellation, letting a refresh outlive a caller that
	// gave up (and outlive the session it belongs to).
	ctx, cancel := context.WithTimeout(s.ctx, s.runtime.oauth.timeout)
	defer cancel()
	ctx = context.WithValue(ctx, oauth2.HTTPClient, s.runtime.oauth.httpClient)
	fresh, err := config.TokenSource(ctx, current).Token()

	s.logical.mu.Lock()
	defer s.logical.mu.Unlock()
	if s.logical.deleted || s.logical.grants[s.backend] != grant {
		// The grant was deleted or replaced while the refresh was in flight
		// (session deletion, revocation, or a concurrent refresh): the fresh
		// token belongs to a grant that is no longer current, so discard it.
		return nil, contract.ErrStateUnavailable
	}
	if err != nil {
		var retrieveErr *oauth2.RetrieveError
		if errors.As(err, &retrieveErr) && retrieveErr.ErrorCode == "invalid_grant" {
			s.logical.revokeGrantLocked(s.backend, grant)
		}
		return nil, errors.New("OAuth token refresh failed")
	}
	if !validBearerToken(fresh) {
		s.logical.revokeGrantLocked(s.backend, grant)
		return nil, errors.New("OAuth token refresh failed")
	}
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = grant.token.RefreshToken
	}
	grant.token = fresh
	return cloneToken(fresh), nil
}

func (l *logicalSession) revokeGrantLocked(backend string, expected *oauthGrant) {
	if l.grants[backend] != expected {
		return
	}
	clearGrantToken(expected)
	delete(l.grants, backend)
}

type brokerTokenSource struct {
	runtime *Runtime
	logical *logicalSession
	// ctx is the operation's own context (from beginOperation); see the
	// identical rationale on scopedTokenSource.
	ctx context.Context
}

func (s *brokerTokenSource) Token() (*oauth2.Token, error) {
	s.logical.mu.Lock()
	if s.logical.deleted {
		s.logical.mu.Unlock()
		return nil, contract.ErrStateUnavailable
	}
	grant := s.logical.brokerCredential
	if grant == nil {
		s.logical.mu.Unlock()
		return nil, contract.ErrAuthorizationNotFound
	}
	if grant.token.Valid() {
		token := cloneToken(grant.token)
		s.logical.mu.Unlock()
		return token, nil
	}
	config, current := grant.config, grant.token
	s.logical.mu.Unlock()

	ctx, cancel := context.WithTimeout(s.ctx, s.runtime.oauth.timeout)
	defer cancel()
	ctx = context.WithValue(ctx, oauth2.HTTPClient, s.runtime.oauth.httpClient)
	fresh, err := config.TokenSource(ctx, current).Token()

	s.logical.mu.Lock()
	defer s.logical.mu.Unlock()
	if s.logical.deleted || s.logical.brokerCredential != grant {
		return nil, contract.ErrStateUnavailable
	}
	if err != nil {
		var retrieveErr *oauth2.RetrieveError
		if errors.As(err, &retrieveErr) && retrieveErr.ErrorCode == "invalid_grant" {
			clearGrantToken(grant)
			s.logical.brokerCredential = nil
		}
		return nil, errors.New("OAuth broker credential refresh failed")
	}
	if !validBearerToken(fresh) {
		clearGrantToken(grant)
		s.logical.brokerCredential = nil
		return nil, errors.New("OAuth broker credential refresh failed")
	}
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = grant.token.RefreshToken
	}
	grant.token = fresh
	return cloneToken(fresh), nil
}

func clearGrantToken(grant *oauthGrant) {
	if grant != nil && grant.token != nil {
		grant.token.AccessToken = ""
		grant.token.RefreshToken = ""
	}
}

func cloneToken(token *oauth2.Token) *oauth2.Token {
	if token == nil {
		return nil
	}
	clone := *token
	return &clone
}

func (t *sessionTool) executeProtected(ctx context.Context, call session.ToolCall) (session.ToolResult, error) {
	logical := t.attachment.logical
	hash := callHash(call)
	logical.mu.Lock()
	grant := logical.grants[t.route.backend]
	if grant == nil {
		logical.mu.Unlock()
		return session.ToolResult{}, contract.ErrAuthorizationNotFound
	}
	if grant.firstPending {
		if grant.firstCall != hash {
			logical.mu.Unlock()
			return session.ToolResult{}, errors.New("protected call does not match authorized effective call")
		}
		grant.firstPending = false
	}
	if err := claimGrantCallLocked(grant, call, hash); err != nil {
		logical.mu.Unlock()
		return session.ToolResult{}, err
	}
	logical.mu.Unlock()
	result, err := t.attachment.runtime.authorizedCaller(ctx, logical.ref, t.route.backend, call, &scopedTokenSource{runtime: t.attachment.runtime, logical: logical, backend: t.route.backend, ctx: ctx})
	result.CallID = call.ID
	return result, err
}

func (t *sessionTool) executeBroker(ctx context.Context, call session.ToolCall) (session.ToolResult, error) {
	logical := t.attachment.logical
	hash := callHash(call)
	logical.mu.Lock()
	grant := logical.brokerCredential
	if grant == nil {
		logical.mu.Unlock()
		return session.ToolResult{}, contract.ErrAuthorizationNotFound
	}
	if err := claimGrantCallLocked(grant, call, hash); err != nil {
		logical.mu.Unlock()
		return session.ToolResult{}, err
	}
	logical.mu.Unlock()
	result, err := t.attachment.runtime.authorizedCaller(ctx, logical.ref, t.route.backend, call, &brokerTokenSource{runtime: t.attachment.runtime, logical: logical, ctx: ctx})
	result.CallID = call.ID
	return result, err
}

func claimGrantCallLocked(grant *oauthGrant, call session.ToolCall, hash [32]byte) error {
	if prior, exists := grant.executed[call.ID]; exists {
		if prior == hash {
			return errors.New("protected call outcome is ambiguous; automatic replay refused")
		}
		return errors.New("protected call ID was reused with different arguments")
	}
	if len(grant.executed) >= maxExecutedCallsPerGrant {
		return errors.New("protected-call replay ledger is full")
	}
	grant.executed[call.ID] = hash // claim before transport: an unknown outcome is never replayed.
	return nil
}

func (l *logicalSession) markDeletedLocked(status session.AuthorizationStatus) {
	l.deleted = true
	l.provisional = false
	l.cleanupStatus = status
	l.cancelOps()
}

func (l *logicalSession) maybeCleanupLocked(runtime *Runtime) {
	if !l.deleted || l.activeOps != 0 || l.cleaned {
		return
	}
	status := l.cleanupStatus
	if status == "" {
		status = session.AuthorizationClosed
	}
	l.clearSecretsLocked(runtime, status)
}

// waitOperations blocks until every in-flight operation registered through
// beginOperation has returned, or timeout elapses. It must be called AFTER
// markDeletedLocked (which cancels their context, so they unwind promptly)
// and with the caller holding no lock. Returns false on timeout: the caller
// gives up waiting but the operations are still cancelled and will finish
// asynchronously.
func (l *logicalSession) waitOperations(timeout time.Duration) bool {
	l.mu.Lock()
	done := l.operationsDone
	l.mu.Unlock()
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (l *logicalSession) clearSecretsLocked(runtime *Runtime, status session.AuthorizationStatus) {
	if l.cleaned {
		return
	}
	l.cleaned = true
	for _, transaction := range l.authorizations {
		runtime.removeCallbackState(transaction.state, transaction)
		if transaction.status == session.AuthorizationPending {
			transaction.status = status
		}
		if transaction.cancel != nil {
			transaction.cancel()
		}
		transaction.clientSecret = ""
		transaction.verifier = ""
		transaction.state = ""
	}
	for _, grant := range l.grants {
		clearGrantToken(grant)
	}
	clearGrantToken(l.brokerCredential)
	l.brokerCredential = nil
	l.completedEnrollment = nil
	clear(l.authorizations)
	clear(l.grants)
}

// Close releases all process-owned callbacks, transactions, grants and logical
// sessions. It cancels every in-flight attachment operation but does NOT wait
// for them to actually return — a bare Runtime (constructed via New/Compile,
// with no Process-owned resources beyond its own http.Client) has nothing an
// in-flight operation could race after Close returns. A Runtime bundled into a
// Process, which DOES tear down additional owned resources (vMCP/authserver)
// right after closing its Runtime, must drain first: see closeAndDrain.
func (r *Runtime) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	sessions := r.sessions
	r.sessions = make(map[session.SessionID]*logicalSession)
	r.mu.Unlock()
	for _, logical := range sessions {
		logical.mu.Lock()
		logical.markDeletedLocked(session.AuthorizationClosed)
		logical.maybeCleanupLocked(r)
		logical.mu.Unlock()
	}
	if r.oauth.httpClient != nil {
		r.oauth.httpClient.CloseIdleConnections()
	}
	return nil
}

// closeAndDrain is Close, plus a bounded wait (closeDrainTimeout) for every
// operation cancelled by Close to actually return, before the caller tears
// down any dependency those operations might still be using. Used only by
// Process.Close/rollback, which owns exactly such dependencies (vMCP server,
// embedded authserver) — a bare Runtime has none, so it keeps using the
// non-blocking Close.
func (r *Runtime) closeAndDrain(timeout time.Duration) error {
	sessions := r.snapshotSessions()
	if err := r.Close(); err != nil {
		return err
	}
	for _, logical := range sessions {
		logical.waitOperations(timeout)
	}
	return nil
}

func (r *Runtime) snapshotSessions() []*logicalSession {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sessions := make([]*logicalSession, 0, len(r.sessions))
	for _, logical := range r.sessions {
		sessions = append(sessions, logical)
	}
	return sessions
}
