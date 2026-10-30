// Package as1server hosts AS-1, Mecatl's sign-in authorization server, on
// ToolHive's embedded authorization server. AS-1 signs users in through one
// upstream OIDC IdP and issues RFC 9068 (typ at+jwt) access tokens for the
// Mecatl API audience that carry the company identity as the RFC 9493 sub_id
// claim. Its clients are the configured public clients, which sign in with
// PKCE.
//
// The server keeps its own in-memory storage, so pending sign-ins and refresh
// tokens do not survive a restart, and caps the sign-ins pending there. Its
// signing key survives when Config.SigningKeyFile is set.
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

	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
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
	// SigningKeyFile is a PEM private key (EC P-256 for ES256, or RSA) that
	// signs access tokens. Empty generates a key at startup, so tokens stop
	// verifying when the server restarts.
	SigningKeyFile string
	// PublicClients are the sign-in clients AS-1 registers. At least one is
	// required.
	PublicClients []PublicClient
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
	issuer  *url.URL
	routes  []string
	handler http.Handler
	auth    *runner.EmbeddedAuthServer
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
	runConfig := &authserver.RunConfig{
		SchemaVersion:    "v1",
		Issuer:           cfg.Issuer,
		AllowedAudiences: audiences,
		Upstreams:        []authserver.UpstreamRunConfig{{Type: authserver.UpstreamProviderTypeOIDC, OIDCConfig: cfg.upstreamRunConfig()}},
	}
	if cfg.SigningKeyFile != "" {
		runConfig.SigningKeyConfig = &authserver.SigningKeyRunConfig{
			KeyDir: filepath.Dir(cfg.SigningKeyFile), SigningKeyFile: filepath.Base(cfg.SigningKeyFile),
		}
	}
	// On error the constructor closes store.
	auth, err := runner.NewEmbeddedAuthServerWithStorage(ctx, runConfig, store,
		runner.WithSubjectIdentifierAudiences([]string{cfg.Audience}),
		runner.WithAccessTokenType(accessTokenType))
	if err != nil {
		return nil, fmt.Errorf("as1server: create authorization server: %w", err)
	}
	s := &Server{issuer: issuer, auth: auth}
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
	}
	seen := make(map[string]bool, len(cfg.PublicClients))
	for _, pc := range cfg.PublicClients {
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
	if _, err := httpsURL("upstream issuer", cfg.Upstream.Issuer); err != nil {
		return nil, err
	}
	return issuer, nil
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
		recorded := &bufferedResponse{header: make(http.Header)}
		next.ServeHTTP(recorded, r)
		if recorded.status == 0 {
			recorded.status = http.StatusOK
		}
		body := recorded.body.Bytes()
		var doc map[string]json.RawMessage
		if recorded.status == http.StatusOK && json.Unmarshal(body, &doc) == nil {
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
		maps.Copy(w.Header(), recorded.header)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(recorded.status)
		_, _ = w.Write(body)
	})
}

// Issuer is AS-1's issuer URL.
func (s *Server) Issuer() string { return s.issuer.String() }

// Routes are the exact paths Handler serves, for the caller to mount on its
// mux after checking they collide with nothing already there.
func (s *Server) Routes() []string { return slices.Clone(s.routes) }

// Handler serves AS-1's routes and nothing else.
func (s *Server) Handler() http.Handler { return s.handler }

// Close releases the authorization server and its storage. It is idempotent.
func (s *Server) Close() error { return s.auth.Close() }
