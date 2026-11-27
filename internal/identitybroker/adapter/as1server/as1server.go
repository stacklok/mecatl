// Package as1server hosts AS-1, Mecatl's sign-in authorization server, on
// ToolHive's embedded authorization server. AS-1 signs users in through one
// upstream OIDC IdP and issues RFC 9068 (typ at+jwt) access tokens for the
// Mecatl API audience that carry the company identity as the RFC 9493 sub_id
// claim. Its clients are the configured public clients, which sign in with
// PKCE, and optionally one confidential delegate client, the identity
// broker's, which exchanges their access tokens for refresh tokens. When
// configured, the delegate client also exchanges those refresh tokens for
// per-call ID-JAGs, whose actor tokens the identity broker's
// CredentialVerifier checks.
//
// The server keeps its own in-memory storage, so pending sign-ins and refresh
// tokens do not survive a restart, and caps the sign-ins pending there. Its
// signing key, and so its published key set, survives when
// Config.SigningKeyFile is set.
//
// Only the sign-in surface is served: dynamic client registration and the
// device flow are not, and the discovery documents do not advertise them.
package as1server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/authorizationdetails"
	"github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/mcp/toolcalldetails"
	"github.com/stacklok/toolhive/pkg/oauthproto"

	"github.com/stacklok/mecatl/internal/identitybroker/adapter/inprocesshttp"
)

// accessTokenType is the JOSE typ of AS-1 access tokens (RFC 9068).
const accessTokenType = "at+jwt"

// ErrInvalidConfig reports a Config that New refuses before starting anything.
var ErrInvalidConfig = errors.New("as1server: invalid configuration")

// Config configures AS-1.
type Config struct {
	// Issuer is AS-1's issuer URL: https, without query, fragment, or trailing
	// slash. It may have a path, under which every route is served.
	Issuer string
	// Audience is the Mecatl API identifier, the only audience AS-1 issues
	// tokens for and the one whose tokens carry sub_id.
	Audience string
	// SigningKeyFile is the absolute path of a PEM private key (EC P-256 for
	// ES256, or RSA) that signs access tokens. Empty generates a key at
	// startup, so tokens stop verifying when the server restarts; only tests
	// leave it empty, as a deployment always names the operator's key.
	SigningKeyFile string
	// FallbackKeyFiles are the absolute paths of earlier signing keys, in the
	// same form. AS-1 publishes them in its key set beside SigningKeyFile's,
	// so tokens they signed keep verifying during a rotation, and never signs
	// with them. They require SigningKeyFile.
	FallbackKeyFiles []string
	// PublicClients are the sign-in clients AS-1 registers. At least one is
	// required.
	PublicClients []PublicClient
	// Delegate is the one pre-provisioned confidential client, the identity
	// broker's, that exchanges the public clients' access tokens. Nil
	// registers none.
	Delegate *DelegateClient
	// Upstream is the IdP users sign in at.
	Upstream UpstreamConfig
}

// PublicClient is a public sign-in client of AS-1: it has no secret and uses
// PKCE with the authorization-code and refresh-token grants. It may ask for
// the openid, profile, and offline_access scopes, and its access tokens are
// for Config.Audience.
type PublicClient struct {
	// ID is the client ID, unique among Config.PublicClients.
	ID string
	// RedirectURIs are the client's absolute redirect URIs, matched exactly.
	RedirectURIs []string
}

// UpstreamConfig is AS-1's registration at the upstream OIDC IdP.
type UpstreamConfig struct {
	// Issuer is the IdP's https issuer URL, used for OIDC discovery.
	Issuer string
	// ClientID is AS-1's client ID at the IdP.
	ClientID string
	// ClientSecretFile holds AS-1's client secret at the IdP. Empty makes AS-1
	// a public client of the IdP.
	ClientSecretFile string
	// SubjectClaim is the ID-token claim that names the company identity.
	// Empty selects "sub"; Entra ID needs "oid".
	SubjectClaim string
	// CAFile is a PEM bundle trusted for the IdP's TLS certificate in
	// addition to the system roots. Empty trusts the system roots only.
	CAFile string
	// AllowPrivateAddresses lets AS-1 reach the IdP on a private or
	// link-local address, which its SSRF protection otherwise refuses.
	// ToolHive admits a loopback issuer host (localhost, 127.0.0.0/8, ::1)
	// either way.
	AllowPrivateAddresses bool
}

// Server is a running AS-1. Close releases it.
type Server struct {
	// issuerID is the configured issuer, byte for byte as ToolHive uses it.
	issuerID string
	issuer   *url.URL
	routes   []string
	handler  http.Handler
	// actors checks ID-JAG requests' actor tokens; nil when the delegate
	// client issues no ID-JAGs.
	actors *actorValidator
	// store is the storage the authorization server uses, which the
	// test-only RevokeGrant revokes grants in.
	store *cappedStorage
	// release releases the authorization server, then its storage, once.
	release func() error
}

// options are test-only construction settings.
type options struct {
	// extraAudiences are further allowed audiences whose tokens carry no
	// sub_id. Test-only.
	extraAudiences []string
	// maxPendingAuthorizations replaces the pending sign-in cap when
	// positive. Test-only.
	maxPendingAuthorizations int
}

// New validates cfg, discovers the upstream IdP, and builds AS-1.
func New(ctx context.Context, cfg Config) (*Server, error) {
	return newServer(ctx, cfg, options{})
}

func newServer(ctx context.Context, cfg Config, opts options) (*Server, error) {
	issuer, err := cfg.validate()
	if err != nil {
		return nil, err
	}
	audiences := append([]string{cfg.Audience}, opts.extraAudiences...)
	store := &cappedStorage{MemoryStorage: storage.NewMemoryStorage(), limit: maxPendingAuthorizations}
	if opts.maxPendingAuthorizations > 0 {
		store.limit = opts.maxPendingAuthorizations
	}
	for _, pc := range cfg.PublicClients {
		client, err := registration.New(registration.Config{
			ID:                      pc.ID,
			RedirectURIs:            slices.Clone(pc.RedirectURIs),
			TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodNone,
			GrantTypes:              []string{oauthproto.GrantTypeAuthorizationCode, oauthproto.GrantTypeRefreshToken},
			ResponseTypes:           []string{oauthproto.ResponseTypeCode},
			Scopes:                  []string{"openid", "profile", "offline_access"},
			Audience:                audiences,
		})
		if err != nil {
			_ = store.Close()
			return nil, fmt.Errorf("as1server: construct client %q: %w", pc.ID, err)
		}
		if err := store.RegisterClient(ctx, client); err != nil {
			_ = store.Close()
			return nil, fmt.Errorf("as1server: register client %q: %w", pc.ID, err)
		}
	}
	// Shortcut: AS-1 sets no HMAC secret, so ToolHive generates one at each start and the authorization codes and refresh tokens it protects do not survive a restart; removed by #2563 (durable broker state).
	runConfig := &authserver.RunConfig{
		SchemaVersion:    "v1",
		Issuer:           cfg.Issuer,
		AllowedAudiences: audiences,
		Upstreams:        []authserver.UpstreamRunConfig{{Type: authserver.UpstreamProviderTypeOIDC, OIDCConfig: cfg.upstreamRunConfig()}},
	}
	if cfg.Delegate != nil {
		// Setting only TokenExchange leaves ToolHive's JWT-bearer inbound grant
		// disabled.
		runConfig.InboundGrants = &authserver.InboundGrantsRunConfig{
			TokenExchange: &authserver.TokenExchangeInboundGrantRunConfig{
				DelegateClients: []authserver.DelegateClientRunConfig{delegateRunConfig(*cfg.Delegate, cfg.Audience)},
			},
		}
	}
	if cfg.SigningKeyFile != "" {
		// ToolHive joins each file name to KeyDir; an empty KeyDir leaves the
		// absolute paths as they are, so the keys may live in different
		// directories, such as one mounted Secret each.
		runConfig.SigningKeyConfig = &authserver.SigningKeyRunConfig{
			SigningKeyFile: cfg.SigningKeyFile, FallbackKeyFiles: slices.Clone(cfg.FallbackKeyFiles),
		}
	}
	runOpts := []runner.EmbeddedAuthServerOption{
		runner.WithSubjectIdentifierAudiences([]string{cfg.Audience}),
		runner.WithAccessTokenType(accessTokenType),
	}
	var actors *actorValidator
	if cfg.Delegate != nil && cfg.Delegate.IDJAG != nil {
		actors = &actorValidator{clientID: cfg.Delegate.ID, actIssuer: cfg.Delegate.IDJAG.ActIssuer}
		runOpts = append(runOpts, runner.WithActorTokenValidators(map[string]tokenexchange.ActorValidatorRegistration{
			actorValidatorName: {Validator: actors, ActIssuer: actors.actIssuer},
		}))
		registry, err := callEntryRegistry()
		if err != nil {
			_ = store.Close()
			return nil, err
		}
		runOpts = append(runOpts, runner.WithAuthorizationDetailsRegistry(registry))
	}
	// ToolHive borrows store on success and failure; AS-1 closes it.
	auth, err := runner.NewEmbeddedAuthServerWithStorage(ctx, runConfig, store, runOpts...)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("as1server: create authorization server: %w", err)
	}
	s := &Server{issuerID: cfg.Issuer, issuer: issuer, actors: actors, store: store, release: sync.OnceValue(func() error {
		return errors.Join(auth.Close(), store.Close())
	})}
	s.routes, s.handler = signInRoutes(issuer, auth.Handler())
	return s, nil
}

// maxPendingAuthorizations caps the sign-ins AS-1 holds before the user has
// approved them: /oauth/authorize stores one for every anonymous request.
// Sign-ins past that step may use a further tenth so a flood of new ones does
// not fail those already in progress. ToolHive keeps an entry for its
// ten-minute lifetime plus up to one five-minute cleanup interval.
const maxPendingAuthorizations = 10_000

// errTooManyPendingAuthorizations refuses a sign-in at the cap. ToolHive
// answers it with a server_error redirect to the client and does not show
// this text to the browser.
var errTooManyPendingAuthorizations = errors.New("as1server: too many pending sign-ins")

// cappedStorage is ToolHive's memory storage with pending authorizations
// capped. The embedded storage serves every other method, including the
// optional interfaces ToolHive looks for.
type cappedStorage struct {
	*storage.MemoryStorage
	// mu makes the count and the store one step.
	mu    sync.Mutex
	limit int
}

// StorePendingAuthorization refuses a new sign-in once limit are pending, and
// one past consent once a further tenth are.
func (s *cappedStorage) StorePendingAuthorization(ctx context.Context, state string, pending *storage.PendingAuthorization) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := s.limit
	if pending != nil && pending.ConsentStage != storage.ConsentStageAwaiting {
		limit += s.limit / 10
	}
	if s.Stats().PendingAuthorizations >= limit {
		return errTooManyPendingAuthorizations
	}
	return s.MemoryStorage.StorePendingAuthorization(ctx, state, pending)
}

// upstreamRunConfig is ToolHive's configuration of AS-1's upstream IdP. It
// asks for offline access the way OIDC Core requires.
func (cfg Config) upstreamRunConfig() *authserver.OIDCUpstreamRunConfig {
	return &authserver.OIDCUpstreamRunConfig{
		IssuerURL:                     cfg.Upstream.Issuer,
		ClientID:                      cfg.Upstream.ClientID,
		ClientSecretFile:              cfg.Upstream.ClientSecretFile,
		RedirectURI:                   cfg.Issuer + "/oauth/callback",
		Scopes:                        []string{"openid", "offline_access"},
		AdditionalAuthorizationParams: map[string]string{"prompt": "consent"},
		SubjectClaim:                  cfg.Upstream.SubjectClaim,
		CAFilePath:                    cfg.Upstream.CAFile,
		AllowPrivateIPs:               cfg.Upstream.AllowPrivateAddresses,
	}
}

// validate checks cfg and returns the parsed issuer.
func (cfg Config) validate() (*url.URL, error) {
	issuer, err := httpsURL("issuer", cfg.Issuer)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(issuer.Path, "/") || (issuer.Path != "" && path.Clean(issuer.Path) != issuer.Path) ||
		issuer.EscapedPath() != issuer.Path {
		return nil, fmt.Errorf("%w: issuer path must be clean, unescaped, and without a trailing slash", ErrInvalidConfig)
	}
	switch {
	case cfg.Audience == "":
		return nil, fmt.Errorf("%w: audience is required", ErrInvalidConfig)
	case len(cfg.PublicClients) == 0:
		return nil, fmt.Errorf("%w: at least one public client is required", ErrInvalidConfig)
	case cfg.Upstream.ClientID == "":
		return nil, fmt.Errorf("%w: upstream client ID is required", ErrInvalidConfig)
	case cfg.SigningKeyFile == "" && len(cfg.FallbackKeyFiles) != 0:
		return nil, fmt.Errorf("%w: fallback signing keys require a signing key file", ErrInvalidConfig)
	}
	if cfg.SigningKeyFile != "" {
		for _, file := range append([]string{cfg.SigningKeyFile}, cfg.FallbackKeyFiles...) {
			if !filepath.IsAbs(file) {
				return nil, fmt.Errorf("%w: signing key file %q is not an absolute path", ErrInvalidConfig, file)
			}
		}
	}
	seen, err := validatePublicClients(cfg.PublicClients)
	if err != nil {
		return nil, err
	}
	if _, err := httpsURL("upstream issuer", cfg.Upstream.Issuer); err != nil {
		return nil, err
	}
	if cfg.Delegate != nil {
		if err := cfg.Delegate.validate(seen); err != nil {
			return nil, err
		}
	}
	return issuer, nil
}

// validatePublicClients checks clients and returns the set of their IDs.
func validatePublicClients(clients []PublicClient) (map[string]bool, error) {
	seen := make(map[string]bool, len(clients))
	for _, pc := range clients {
		switch {
		case pc.ID == "":
			return nil, fmt.Errorf("%w: public client ID is required", ErrInvalidConfig)
		case seen[pc.ID]:
			return nil, fmt.Errorf("%w: duplicate public client ID %q", ErrInvalidConfig, pc.ID)
		case len(pc.RedirectURIs) == 0:
			return nil, fmt.Errorf("%w: public client %q needs at least one redirect URI", ErrInvalidConfig, pc.ID)
		}
		seen[pc.ID] = true
		for _, raw := range pc.RedirectURIs {
			if u, err := url.Parse(raw); err != nil || !u.IsAbs() || u.Host == "" || u.Fragment != "" {
				return nil, fmt.Errorf("%w: public client %q redirect URI %q must be an absolute URL without a fragment", ErrInvalidConfig, pc.ID, raw)
			}
		}
	}
	return seen, nil
}

// httpsURL parses an issuer-style URL: https, with a host, and without
// userinfo, query, or fragment.
func httpsURL(name, raw string) (*url.URL, error) {
	if raw == "" {
		return nil, fmt.Errorf("%w: %s is required", ErrInvalidConfig, name)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, fmt.Errorf("%w: %s must be an https URL with a host and without userinfo, query, or fragment", ErrInvalidConfig, name)
	}
	return u, nil
}

// signInRoutes lists the routes AS-1 serves under issuer's path and returns a
// handler that serves only those, passing them to embedded with the issuer
// path stripped. Everything else, including registration and the device flow,
// is not found, and the discovery documents leave out the endpoints that
// would be.
func signInRoutes(issuer *url.URL, embedded http.Handler) ([]string, http.Handler) {
	issuerPath := issuer.Path
	routes := []string{
		issuerPath + "/oauth/authorize",
		issuerPath + "/oauth/consent",
		issuerPath + "/oauth/token",
		issuerPath + "/oauth/callback",
		issuerPath + "/.well-known/openid-configuration",
		issuerPath + "/.well-known/oauth-authorization-server",
		issuerPath + "/.well-known/jwks.json",
	}
	routed := make(map[string]bool, len(routes))
	for _, route := range routes {
		routed[issuer.Scheme+"://"+issuer.Host+route] = true
	}
	stripped := http.StripPrefix(issuerPath, embedded)
	discovery := withoutUnroutedEndpoints(stripped, routed)
	mux := http.NewServeMux()
	for _, route := range routes {
		if strings.HasPrefix(route, issuerPath+"/.well-known/") && !strings.HasSuffix(route, "/jwks.json") {
			mux.Handle(route, discovery)
		} else {
			mux.Handle(route, stripped)
		}
	}
	return routes, mux
}

// withoutUnroutedEndpoints serves next's discovery document without the
// *_endpoint fields whose URL is not one of routed, such as ToolHive's
// registration and device-flow endpoints. Any other response passes through.
func withoutUnroutedEndpoints(next http.Handler, routed map[string]bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorded := inprocesshttp.NewRecorder()
		next.ServeHTTP(recorded, r)
		body := recorded.Body()
		var doc map[string]json.RawMessage
		if recorded.Status() == http.StatusOK && json.Unmarshal(body, &doc) == nil {
			for key, raw := range doc {
				var endpoint string
				if strings.HasSuffix(key, "_endpoint") && (json.Unmarshal(raw, &endpoint) != nil || !routed[endpoint]) {
					delete(doc, key)
				}
			}
			if rewritten, err := json.Marshal(doc); err == nil {
				body = rewritten
			}
		}
		maps.Copy(w.Header(), recorded.Header())
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(recorded.Status())
		_, _ = w.Write(body)
	})
}

// Issuer is AS-1's issuer URL, byte for byte as configured: the audience a
// delegate client's assertions name.
func (s *Server) Issuer() string { return s.issuerID }

// TokenEndpoint is AS-1's token endpoint.
func (s *Server) TokenEndpoint() string { return s.issuerID + "/oauth/token" }

// Routes are the exact paths Handler serves, for the caller to mount on its
// mux after checking they collide with nothing already there.
func (s *Server) Routes() []string { return slices.Clone(s.routes) }

// Handler serves AS-1's routes and nothing else.
func (s *Server) Handler() http.Handler { return s.handler }

// Close releases the authorization server and its storage. It is idempotent.
func (s *Server) Close() error { return s.release() }

// ErrUnknownGrant reports a refresh token RevokeGrant cannot revoke: it is
// malformed, or it names no live grant of AS-1.
var ErrUnknownGrant = errors.New("as1server: unknown grant")

// RevokeGrant is test-only. It revokes the grant behind refreshToken, a
// refresh token AS-1 issued the delegate client at session open, directly in
// AS-1's storage, so tests can see AS-1 answer revoked for it. It bypasses
// AS-1's handler, client authentication, and ownership checks, so nothing
// outside tests may call it (a forbidigo rule enforces this); production
// revocation goes through AS-1's RFC 7009 route (stacklok/toolhive#6966).
// Revoking a revoked grant succeeds. A malformed token, a public client's
// refresh token, or an unknown grant is ErrUnknownGrant. Errors never carry
// the token.
func (s *Server) RevokeGrant(ctx context.Context, refreshToken string) error {
	// AS-1 keys a grant by the HMAC signature of its refresh token, the part
	// after the '.' (fosite's HMAC strategy, which ToolHive uses).
	_, signature, ok := strings.Cut(refreshToken, ".")
	if !ok || signature == "" {
		return ErrUnknownGrant
	}
	err := s.store.RevokeExchangeGrant(ctx, signature, time.Now())
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return ErrUnknownGrant
	case err != nil:
		return fmt.Errorf("as1server: revoke grant: %w", err)
	}
	return nil
}

// callEntryRegistry is AS-1's authorization_details registry: ToolHive's MCP
// call type alone, which AS-1 accepts on an ID-JAG exchange and signs into
// the ID-JAG it issues (credentials-and-lifecycle.md, step (7)). ToolHive
// registers no type by default, and the session-open exchange grant has a
// registry of its own.
func callEntryRegistry() (*authorizationdetails.Registry, error) {
	types := map[string]authorizationdetails.Type{}
	toolcalldetails.Register(types)
	registry, err := authorizationdetails.New(types)
	if err != nil {
		return nil, fmt.Errorf("as1server: register the MCP call type: %w", err)
	}
	return registry, nil
}
