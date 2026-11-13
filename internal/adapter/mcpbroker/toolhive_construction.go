package mcpbroker

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
	"github.com/stacklok/toolhive/pkg/vmcp"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/mcpsecretfile"
)

const (
	authNone  = "none"
	authOAuth = "oauth"

	// toolHiveAuthStoragePrefix namespaces the embedded auth server's Redis
	// keys away from mecatl's own versioned session-store scheme on the SAME
	// managed Redis instance.
	toolHiveAuthStoragePrefix = "mecatl:authserver:"
)

// ToolHiveConfig is the immutable adapter-owned input to bundled process
// construction. Composition reduces the operator schema to this value before
// invoking the adapter.
type ToolHiveConfig struct {
	CallbackURL string
	Profiles    []ToolHiveProfile
	Occupied    []string
	// AuthStorage backs the embedded auth server's pending-authorization,
	// token, grant, and DCR storage directly. Tests use this to inject a
	// fake/spy storage.Storage; composition (which cannot import the
	// vendored toolhive storage package — see
	// TestToolHiveImportsStayBehindApprovedAdapterLeaves) uses AuthRedisClient
	// instead. Precedence: AuthStorage wins if set, else AuthRedisClient
	// selects a Redis-backed store (under toolHiveAuthStoragePrefix), else
	// storage.NewMemoryStorage() — which does not survive a process restart,
	// so a pod bounce between a user starting and completing an OAuth
	// authorization loses the pending state (a genuine "pending authorization
	// not found" failure).
	AuthStorage storage.Storage
	// AuthRedisClient, when set (and AuthStorage is nil), backs the embedded
	// auth server with a Redis-backed storage.Storage sharing this managed
	// Redis instance with mecatl's own session store, under
	// toolHiveAuthStoragePrefix. The caller (composition) owns the client's
	// lifecycle — Process.Close closes the Redis-backed store, which closes
	// the client it was given, so no separate cleanup is needed beyond that.
	AuthRedisClient redis.UniversalClient
	// Diagnostics receives per-backend authenticated-discovery outcomes during
	// workspace-enrollment catalogue freeze (success + tool count, or failure +
	// backend name) — see stageAuthenticatedRoutes. A nil value defaults to
	// port.NopDiagnostics{}, matching every other nil-safe Diagnostics consumer.
	Diagnostics port.Diagnostics
	// BrokerClient, with at least one protected upstream, sets the embedded
	// authorization server up as the identity broker's AS-2 (Process.AS2):
	// it registers the broker client, and the vMCP's resource becomes its
	// endpoint instead of the issuer. Nil, or no protected upstream, leaves the
	// embedded server as it is.
	BrokerClient *ToolHiveBrokerClient
}

// ToolHiveBrokerClient is the identity broker's client at AS-2.
type ToolHiveBrokerClient struct {
	// ID is the client ID.
	ID string
	// KeySet is the client's public signing keys as a JSON JWK Set (RFC 7517
	// section 5). It is written to a private temporary file ToolHive reads
	// once, at construction, and removed after.
	KeySet []byte
}

// as2DelegateClient is ToolHive's configuration of the broker client, whose
// public key set is in jwksFile, at AS-2 with the vMCP resource. Restricted
// with no source clients, it can exchange only tokens issued to itself, never
// a user's gateway login token.
func as2DelegateClient(client ToolHiveBrokerClient, jwksFile, resource string) authserver.DelegateClientRunConfig {
	return authserver.DelegateClientRunConfig{
		ClientID:                client.ID,
		TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodPrivateKeyJWT,
		JWKSFile:                jwksFile,
		GrantTypes:              []string{oauthproto.GrantTypeTokenExchange, oauthproto.GrantTypeJWTBearer},
		Scopes:                  []string{"openid"},
		Audiences:               []string{resource},
		TokenExchange:           &authserver.DelegateClientTokenExchange{SelfIssuedSubjectTokens: authserver.SelfIssuedSubjectTokensRestricted},
	}
}

// as2Resource is the vMCP resource when the embedded authorization server at
// issuer is set up as AS-2. ToolHive refuses an accepted ID-JAG audience,
// AS-2's issuer, that is also a resource audience.
func as2Resource(issuer string) string {
	return issuer + "/mcp"
}

// as2RunConfig writes the broker client's key set to a file and returns AS-2's
// additions to the embedded authorization server's RunConfig, with a function
// that removes the file once ToolHive has read it, at construction. trust is
// nil in production.
func as2RunConfig(client ToolHiveBrokerClient, issuer, resource string, trust *as2Trust) (*authserver.InboundGrantsRunConfig, []tokenexchange.TrustedIssuer, func(), error) {
	jwksFile, remove, err := writeKeySet("as2-broker-client-", client.KeySet)
	if err != nil {
		return nil, nil, nil, err
	}
	// Leaving JWTBearer unset keeps ToolHive's JWT-bearer grant, and so
	// ID-JAG redemption, disabled.
	grants := &authserver.InboundGrantsRunConfig{TokenExchange: &authserver.TokenExchangeInboundGrantRunConfig{
		DelegateClients: []authserver.DelegateClientRunConfig{as2DelegateClient(client, jwksFile, resource)},
	}}
	var trusted []tokenexchange.TrustedIssuer
	if trust != nil {
		trusted, grants.JWTBearer = trust.runConfig(issuer, resource)
	}
	return grants, trusted, remove, nil
}

// as2Trust is an ID-JAG issuer AS-2 trusts for the listed subjects only, with
// its keys fetched from jwksURL over a private address, trusting caFile.
// Production configures none until ToolHive can trust AS-1 for every subject
// (stacklok/toolhive#6887); only tests set it, through toolHiveProcessOptions.
type as2Trust struct {
	issuer, jwksURL, caFile string
	subjects                []string
	maxAssertionAge         time.Duration
}

// as2TrustRef names the trusted issuer for its JWT-bearer policy.
const as2TrustRef = "as1"

// runConfig is ToolHive's configuration of t at AS-2: ID-JAGs for issuer that
// may obtain only resource.
func (t *as2Trust) runConfig(issuer, resource string) ([]tokenexchange.TrustedIssuer, *authserver.JWTBearerInboundGrantRunConfig) {
	bindings := make([]tokenexchange.JWTBearerSubjectBinding, len(t.subjects))
	for i, subject := range t.subjects {
		bindings[i] = tokenexchange.JWTBearerSubjectBinding{Subject: subject, AllowedResources: []string{resource}}
	}
	trusted := []tokenexchange.TrustedIssuer{{
		Name: as2TrustRef, IssuerURL: t.issuer, JWKSURL: t.jwksURL, CAFilePath: t.caFile, AllowPrivateIPs: true,
	}}
	return trusted, &authserver.JWTBearerInboundGrantRunConfig{IssuerPolicies: []authserver.JWTBearerIssuerPolicyRunConfig{{
		IssuerRef: as2TrustRef, MaxAssertionAge: t.maxAssertionAge.String(),
		SubjectBindings: bindings, AcceptedAudiences: []string{issuer},
	}}}
}

// ToolHiveProfile is one configured Streamable HTTP upstream.
type ToolHiveProfile struct {
	Name   string
	URL    string
	Auth   string
	OAuth  *ToolHiveOAuth
	Static []StaticTool
}

// ToolHiveOAuth contains only values needed to construct ToolHive's upstream.
type ToolHiveOAuth struct {
	Issuer                string
	AuthorizationEndpoint string
	TokenEndpoint         string
	ClientID              string
	// ClientSecretFile names the local file read when confidential-client authentication is needed.
	ClientSecretFile string
	// ClientSecretEnv names the MECATL_* environment variable read by ToolHive;
	// mutually exclusive with ClientSecretFile.
	ClientSecretEnv     string
	Scopes              []string
	RequestRefreshToken bool
	// DCRDiscoveryURL enables RFC 7591 registration through RFC 8414 metadata.
	DCRDiscoveryURL string
}

// StaticTool is one trusted protected tool declaration. Its schema is copied
// into the frozen model-facing catalogue; backend routing remains private.
type StaticTool struct {
	Name, Description string
	Schema            json.RawMessage
	ReadOnly          bool
}

type toolHiveConstruction struct {
	upstreams         []authserver.UpstreamRunConfig
	backends          []vmcp.Backend
	anonymous         []ToolHiveProfile
	protectedBackends []string
	providerByBackend map[string]string
	staticByBackend   map[string][]StaticTool
}

func compileToolHiveConstruction(profiles []ToolHiveProfile, issuer string) (toolHiveConstruction, error) {
	out := toolHiveConstruction{
		upstreams: make([]authserver.UpstreamRunConfig, 0), backends: make([]vmcp.Backend, 0, len(profiles)),
		anonymous: make([]ToolHiveProfile, 0), protectedBackends: make([]string, 0),
		providerByBackend: make(map[string]string), staticByBackend: make(map[string][]StaticTool),
	}
	seenBackends := make(map[string]struct{}, len(profiles))
	seenProviders := make(map[string]string)
	for _, profile := range profiles {
		key := strings.ToLower(profile.Name)
		if key == "" || profile.URL == "" {
			return toolHiveConstruction{}, fmt.Errorf("%w: upstream name and URL are required", ErrInvalidCatalogue)
		}
		if strings.Contains(profile.Name, ".") {
			return toolHiveConstruction{}, fmt.Errorf("%w: upstream %q contains reserved ToolHive routing separator %q", ErrInvalidCatalogue, profile.Name, ".")
		}
		if _, exists := seenBackends[key]; exists {
			return toolHiveConstruction{}, fmt.Errorf("%w: duplicate upstream %q", ErrInvalidCatalogue, profile.Name)
		}
		seenBackends[key] = struct{}{}
		backend := vmcp.Backend{ID: profile.Name, Name: profile.Name, BaseURL: profile.URL, TransportType: "streamable-http"}
		switch profile.Auth {
		case authNone:
			if profile.OAuth != nil || len(profile.Static) != 0 {
				return toolHiveConstruction{}, fmt.Errorf("%w: anonymous upstream %q contains protected configuration", ErrInvalidCatalogue, profile.Name)
			}
			out.anonymous = append(out.anonymous, cloneToolHiveProfile(profile))
		case authOAuth:
			if profile.OAuth == nil {
				return toolHiveConstruction{}, fmt.Errorf("%w: protected upstream %q is missing OAuth configuration", ErrInvalidCatalogue, profile.Name)
			}
			provider, err := toolHiveProviderKey(profile.Name)
			if err != nil {
				return toolHiveConstruction{}, err
			}
			if prior, exists := seenProviders[provider]; exists {
				return toolHiveConstruction{}, fmt.Errorf("%w: upstreams %q and %q map to provider %q", ErrInvalidCatalogue, prior, profile.Name, provider)
			}
			seenProviders[provider] = profile.Name
			upstream, err := toolHiveUpstream(profile, provider, issuer)
			if err != nil {
				return toolHiveConstruction{}, err
			}
			out.upstreams = append(out.upstreams, upstream)
			out.protectedBackends = append(out.protectedBackends, profile.Name)
			out.providerByBackend[profile.Name] = provider
			out.staticByBackend[profile.Name] = cloneStaticTools(profile.Static)
			backend.AuthConfig = &authtypes.BackendAuthStrategy{Type: "upstream_inject", UpstreamInject: &authtypes.UpstreamInjectConfig{ProviderName: provider}}
		default:
			return toolHiveConstruction{}, fmt.Errorf("%w: unsupported auth mode %q for upstream %q", ErrInvalidCatalogue, profile.Auth, profile.Name)
		}
		out.backends = append(out.backends, backend)
	}
	return out, nil
}

func toolHiveProviderKey(name string) (string, error) {
	key := strings.Trim(strings.ReplaceAll(strings.ToLower(name), "_", "-"), "-")
	if key == "" || len(key) > 63 {
		return "", fmt.Errorf("%w: upstream %q cannot map to a ToolHive provider key", ErrInvalidCatalogue, name)
	}
	for _, r := range key {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return "", fmt.Errorf("%w: upstream %q cannot map to a ToolHive provider key", ErrInvalidCatalogue, name)
		}
	}
	return key, nil
}

func toolHiveUpstream(profile ToolHiveProfile, provider, issuer string) (authserver.UpstreamRunConfig, error) {
	oauth := profile.OAuth
	if oauth.DCRDiscoveryURL != "" {
		if oauth.ClientID != "" || oauth.ClientSecretFile != "" || oauth.ClientSecretEnv != "" {
			return authserver.UpstreamRunConfig{}, fmt.Errorf("%w: protected upstream %q combines DCR with a client identity", ErrInvalidCatalogue, profile.Name)
		}
		if oauth.AuthorizationEndpoint == "" || oauth.TokenEndpoint == "" {
			return authserver.UpstreamRunConfig{}, fmt.Errorf("%w: protected upstream %q DCR requires OAuth2 endpoints", ErrInvalidCatalogue, profile.Name)
		}
		return toolHiveOAuth2Upstream(profile, provider, issuer, &authserver.DCRUpstreamConfig{DiscoveryURL: oauth.DCRDiscoveryURL})
	}
	if oauth.ClientID == "" {
		return authserver.UpstreamRunConfig{}, fmt.Errorf("%w: protected upstream %q is missing client identity", ErrInvalidCatalogue, profile.Name)
	}
	if err := validateClientSecretFile(oauth.ClientSecretFile); err != nil {
		return authserver.UpstreamRunConfig{}, fmt.Errorf("%w: protected upstream %q client secret file: %v", ErrInvalidCatalogue, profile.Name, err)
	}
	if oauth.AuthorizationEndpoint != "" || oauth.TokenEndpoint != "" {
		return toolHiveOAuth2Upstream(profile, provider, issuer, nil)
	}
	if oauth.Issuer == "" {
		return authserver.UpstreamRunConfig{}, fmt.Errorf("%w: protected upstream %q is missing an issuer", ErrInvalidCatalogue, profile.Name)
	}
	redirect := issuer + "/oauth/callback"
	return authserver.UpstreamRunConfig{Name: provider, Type: authserver.UpstreamProviderTypeOIDC, OIDCConfig: &authserver.OIDCUpstreamRunConfig{
		IssuerURL: oauth.Issuer, ClientID: oauth.ClientID, ClientSecretFile: oauth.ClientSecretFile, ClientSecretEnvVar: oauth.ClientSecretEnv,
		RedirectURI: redirect, Scopes: append([]string(nil), oauth.Scopes...),
		AdditionalAuthorizationParams: toolHiveAdditionalAuthorizationParams(oauth),
	}}, nil
}

func toolHiveOAuth2Upstream(profile ToolHiveProfile, provider, issuer string, dcr *authserver.DCRUpstreamConfig) (authserver.UpstreamRunConfig, error) {
	oauth := profile.OAuth
	if oauth.AuthorizationEndpoint == "" || oauth.TokenEndpoint == "" {
		return authserver.UpstreamRunConfig{}, fmt.Errorf("%w: protected upstream %q has partial OAuth2 endpoints", ErrInvalidCatalogue, profile.Name)
	}
	return authserver.UpstreamRunConfig{Name: provider, Type: authserver.UpstreamProviderTypeOAuth2, OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
		AuthorizationEndpoint: oauth.AuthorizationEndpoint, TokenEndpoint: oauth.TokenEndpoint, ClientID: oauth.ClientID,
		ClientSecretFile: oauth.ClientSecretFile, ClientSecretEnvVar: oauth.ClientSecretEnv, RedirectURI: issuer + "/oauth/callback", Scopes: append([]string(nil), oauth.Scopes...),
		AdditionalAuthorizationParams: toolHiveAdditionalAuthorizationParams(oauth), DCRConfig: dcr,
	}}, nil
}

func toolHiveAdditionalAuthorizationParams(oauth *ToolHiveOAuth) map[string]string {
	if oauth.RequestRefreshToken {
		return map[string]string{"access_type": "offline"}
	}
	return nil
}

func cloneToolHiveProfile(in ToolHiveProfile) ToolHiveProfile {
	out := in
	if in.OAuth != nil {
		oauth := *in.OAuth
		oauth.Scopes = append([]string(nil), in.OAuth.Scopes...)
		out.OAuth = &oauth
	}
	out.Static = cloneStaticTools(in.Static)
	return out
}

func cloneStaticTools(in []StaticTool) []StaticTool {
	out := append([]StaticTool(nil), in...)
	for i := range out {
		out[i].Schema = append(json.RawMessage(nil), in[i].Schema...)
	}
	return out
}

// validateClientSecretFile proves that a configured credential is present and
// bounded without retaining it. ToolHive reads the same path while building
// its in-process upstream client.
func validateClientSecretFile(path string) error {
	if path == "" {
		return nil
	}
	_, err := mcpsecretfile.Read(path)
	return err
}
