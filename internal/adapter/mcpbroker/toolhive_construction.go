package mcpbroker

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/authorizationdetails"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/mcp/toolcalldetails"
	"github.com/stacklok/toolhive/pkg/oauthproto"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/factory"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/pkg/vmcp/callbinding"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker/internal/as2trust"
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
	// AS2Trust, with AS-2 set up, makes AS-2 trust an ID-JAG issuer; see
	// AS2Trust. The field is removed when #2533 makes AS-2 trust AS-1 in
	// production from configuration (the shortcut is marked on
	// app.Config.IdentityBrokerAS2Trust).
	AS2Trust AS2Trust
	// GatewayCalls, with AS-2 set up, sends tools/call on GatewayCalls.Servers'
	// broker routes to the embedded vMCP as a gateway, authorized by the
	// identity broker. EmbeddedGatewayClient is then required. Nil keeps
	// every route on the MCP broker's OAuth grant.
	GatewayCalls *GatewayCalls
	// EmbeddedGatewayClient builds the embedded gateway's HTTP client over
	// the vMCP handler for endpoint.
	EmbeddedGatewayClient func(vmcp http.Handler, endpoint string) (*http.Client, error)
	// SigningKeyFile is the absolute path of the PEM private key the embedded
	// authorization server signs its tokens with; empty generates a key at
	// each start. FallbackKeyFiles are absolute paths of earlier keys, PEM
	// private keys too, whose tokens still verify; they require
	// SigningKeyFile.
	SigningKeyFile   string
	FallbackKeyFiles []string
}

// defaultGatewayMaxCallDuration is how long the vMCP keeps a gateway call
// open: it accepts the call's messages, and keeps its token's single-use
// record, for at most this long after the call starts. It does not stop a
// longer call.
const defaultGatewayMaxCallDuration = callbinding.DefaultMaxCallDuration

// gatewayMaxCallDuration is the vMCP's maximum call duration:
// defaultGatewayMaxCallDuration, unless a test's trust shortens it. It is
// also the grace after a gateway token's exp (gatewayCallBinding).
func (t AS2Trust) gatewayMaxCallDuration() time.Duration {
	if t.t != nil && t.t.MaxCallDuration > 0 {
		return t.t.MaxCallDuration
	}
	return defaultGatewayMaxCallDuration
}

// callEntryRegistry is AS-2's authorization_details registry: ToolHive's
// MCP call type alone, which AS-2 processes in the ID-JAG's signed claim and
// copies into the gateway token (credentials-and-lifecycle.md, step (8)).
// ToolHive registers no type by default.
func callEntryRegistry() (*authorizationdetails.Registry, error) {
	types := map[string]authorizationdetails.Type{}
	toolcalldetails.Register(types)
	registry, err := authorizationdetails.New(types)
	if err != nil {
		return nil, fmt.Errorf("mcpbroker: register the MCP call type: %w", err)
	}
	return registry, nil
}

// gatewayCallBinding is the vMCP's call binding with AS-2 set up at issuer
// (credentials-and-lifecycle.md, step (9), check 3): every token of the
// broker client must carry a call entry, a call stays open for at most
// maxCall, and the call a token starts may send its own messages after the
// token's exp for as long as it is open. The token validator in front of the
// vMCP and the vMCP share it, so both answer with incoming's challenge.
//
// The grace after exp equals maxCall because the message Mecatl sends after
// exp is the call's cancellation, which can come at any point in the call. A
// shorter grace would answer a later cancellation 401, and the backend's
// tool would keep running until maxCall. No elicitation reply follows exp:
// the vMCP answers a tools/call as one JSON response and Mecatl's client
// opens no GET stream, so no elicitation reaches it (#2821). The vMCP admits
// an expired token only for its open call's cancellation, reply, GET, or
// DELETE on that call's session, and only for AS-2's tokens of client, so
// the longer grace gives a stolen token little more.
//
// Shortcut: the single-use record is in this process's memory, so a gateway token replayed to another replica within its lifetime is accepted; removed by #2805 (more than one replica), with the vMCP's single-use record shared by all replicas (stacklok/toolhive#7261).
func gatewayCallBinding(issuer string, client ToolHiveBrokerClient, maxCall time.Duration, incoming *vmcpconfig.IncomingAuthConfig) (*callbinding.Binding, error) {
	binding, err := callbinding.New(callbinding.Config{
		Clients:         []string{client.ID},
		MaxCallDuration: maxCall,
		ExpiredTokenAdmission: []callbinding.ExpiredTokenAdmission{{
			Issuer: issuer, ClientID: client.ID, Grace: maxCall,
		}},
		Challenge: factory.IncomingBearerChallenge(incoming),
	})
	if err != nil {
		return nil, fmt.Errorf("mcpbroker: create the vMCP's call binding: %w", err)
	}
	return binding, nil
}

// signingKeyRunConfig is ToolHive's configuration of the embedded
// authorization server's signing keys, nil without a key file. ToolHive
// joins each file name to an empty KeyDir, so the absolute paths stay as
// they are.
func (c ToolHiveConfig) signingKeyRunConfig() (*authserver.SigningKeyRunConfig, error) {
	if c.SigningKeyFile == "" {
		if len(c.FallbackKeyFiles) != 0 {
			return nil, fmt.Errorf("%w: signing fallback keys need a signing key file", ErrInvalidCatalogue)
		}
		return nil, nil
	}
	for _, path := range append([]string{c.SigningKeyFile}, c.FallbackKeyFiles...) {
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("%w: signing key file %q is not an absolute path", ErrInvalidCatalogue, path)
		}
	}
	return &authserver.SigningKeyRunConfig{SigningKeyFile: c.SigningKeyFile, FallbackKeyFiles: append([]string(nil), c.FallbackKeyFiles...)}, nil
}

// SigningPublicKeys reads the signing key at keyFile and its fallbacks as
// the embedded authorization server reads them, and returns their public
// keys, so composition can refuse a key another role uses. It refuses a key
// the vMCP cannot verify tokens from (checkSigningKeyType) and a key listed
// twice, the active key included, which would publish its key ID twice.
// Errors name the file, never key material.
func SigningPublicKeys(keyFile string, fallbacks []string) ([]crypto.PublicKey, error) {
	paths := append([]string{keyFile}, fallbacks...)
	keys := make([]crypto.PublicKey, 0, len(paths))
	for _, path := range paths {
		signer, err := servercrypto.LoadSigningKey(path)
		if err != nil {
			return nil, fmt.Errorf("signing key %s: %w", path, err)
		}
		pub := signer.Public()
		if err := checkSigningKeyType(pub); err != nil {
			return nil, fmt.Errorf("signing key %s: %w", path, err)
		}
		for j, earlier := range keys {
			if eq, ok := earlier.(interface{ Equal(crypto.PublicKey) bool }); ok && eq.Equal(pub) {
				return nil, fmt.Errorf("signing key %s is the same key as %s", path, paths[j])
			}
		}
		keys = append(keys, pub)
	}
	return keys, nil
}

// checkSigningKeyType refuses a signing key whose tokens the vMCP cannot
// verify. ToolHive's embedded authorization server signs with an Ed25519
// key, as EdDSA, but its token validator accepts only RSA and ECDSA
// signatures, so every such token would be refused; and it refuses an ECDSA
// curve other than P-256, P-384, or P-521 only after startup has begun.
func checkSigningKeyType(pub crypto.PublicKey) error {
	switch k := pub.(type) {
	case *rsa.PublicKey:
		return nil
	case *ecdsa.PublicKey:
		switch k.Curve {
		case elliptic.P256(), elliptic.P384(), elliptic.P521():
			return nil
		}
		return fmt.Errorf("an ECDSA key on %s, but it must be on P-256, P-384, or P-521", k.Curve.Params().Name)
	default:
		return fmt.Errorf("a %T key, but it must be an RSA or ECDSA key", pub)
	}
}

// AS2Trust is an ID-JAG issuer AS-2 trusts for the listed subjects only.
// Its zero value trusts none, as production's does until #2533. Building one
// that trusts an issuer needs an as2trust.Trust. Go's internal-package rule
// keeps that type inside this adapter, and a depguard rule lets only this
// file and mcpbrokertest import it outside tests; depguard also keeps
// production code from importing mcpbrokertest.
type AS2Trust struct{ t *as2trust.Trust }

// NewAS2Trust returns an AS2Trust trusting t.
func NewAS2Trust(t as2trust.Trust) AS2Trust { return AS2Trust{t: &t} }

// TrustsAnIssuer reports whether t trusts an ID-JAG issuer; the zero value,
// production's, trusts none.
func (t AS2Trust) TrustsAnIssuer() bool { return t.t != nil }

// ToolHiveBrokerClient is the identity broker's client at AS-2.
type ToolHiveBrokerClient struct {
	// ID is the client ID.
	ID string
	// KeySetFile is the absolute path of the client's public signing keys as
	// a JSON JWK Set (RFC 7517 section 5), the operator's file, which must
	// hold at least one key and which ToolHive reads once, at construction.
	// It may hold an old and a new key while the client rotates.
	KeySetFile string
}

// as2DelegateClient is ToolHive's configuration of the broker client at AS-2
// with the vMCP resource. Its gateway tokens get AS-2's default lifetime,
// capped at the remaining validity of the ID-JAG they are redeemed from.
// Restricted with no source clients, it can exchange only tokens issued to
// itself, never a user's gateway login token. ToolHive refuses a subject
// token that carries authorization_details (stacklok/toolhive#7282), so it
// cannot exchange a call-bound gateway token, which would get around the
// token's single use with a new jti.
//
// Shortcut: ToolHive requires the token-exchange grant on this client, so a gateway token without a call entry, which the broker never obtains since it sends one on every call, could still be exchanged for another up to its expiry; removed by #2533 (AS-2 marks gateway tokens, which ToolHive refuses at token exchange, stacklok/toolhive#7247).
func as2DelegateClient(client ToolHiveBrokerClient, resource string) authserver.DelegateClientRunConfig {
	return authserver.DelegateClientRunConfig{
		ClientID:                client.ID,
		TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodPrivateKeyJWT,
		JWKSFile:                client.KeySetFile,
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

// as2AccessTokenType is the JOSE typ of the access tokens AS-2 issues.
const as2AccessTokenType = "at+jwt"

// as2TokenEndpoint is the token endpoint of AS-2 at issuer.
func as2TokenEndpoint(issuer string) string {
	return issuer + "/oauth/token"
}

// AS2Endpoints returns the issuer, token endpoint, and vMCP resource
// NewToolHiveProcess gives AS-2 when it sets AS-2 up for callbackURL, which
// it does only with a protected profile and a broker client. It fails when
// callbackURL is not the canonical HTTPS URL protected profiles require.
func AS2Endpoints(callbackURL string) (issuer, tokenEndpoint, resource string, err error) {
	issuer, err = toolHiveIssuer(callbackURL, true)
	if err != nil {
		return "", "", "", err
	}
	return issuer, as2TokenEndpoint(issuer), as2Resource(issuer), nil
}

// as2RunConfig returns AS-2's additions to the embedded authorization
// server's RunConfig. trust is zero in production.
func as2RunConfig(client ToolHiveBrokerClient, issuer, resource string, trust AS2Trust) (*authserver.InboundGrantsRunConfig, []tokenexchange.TrustedIssuer) {
	// Leaving JWTBearer unset keeps ToolHive's JWT-bearer grant, and so
	// ID-JAG redemption, disabled.
	grants := &authserver.InboundGrantsRunConfig{TokenExchange: &authserver.TokenExchangeInboundGrantRunConfig{
		DelegateClients: []authserver.DelegateClientRunConfig{as2DelegateClient(client, resource)},
	}}
	var trusted []tokenexchange.TrustedIssuer
	if trust.t != nil {
		trusted, grants.JWTBearer = as2TrustRunConfig(trust.t, issuer, resource)
	}
	return grants, trusted
}

// as2TrustRef names the trusted issuer for its JWT-bearer policy.
const as2TrustRef = "as1"

// as2TrustRunConfig is ToolHive's configuration of t at AS-2: ID-JAGs for
// issuer that may obtain only resource. It accepts ID-JAGs only, never plain
// RFC 7523 assertions, which ToolHive would otherwise redeem from a trusted
// issuer without client authentication.
func as2TrustRunConfig(t *as2trust.Trust, issuer, resource string) ([]tokenexchange.TrustedIssuer, *authserver.JWTBearerInboundGrantRunConfig) {
	bindings := make([]tokenexchange.JWTBearerSubjectBinding, len(t.Subjects))
	for i, subject := range t.Subjects {
		bindings[i] = tokenexchange.JWTBearerSubjectBinding{Subject: subject, AllowedResources: []string{resource}}
	}
	trusted := []tokenexchange.TrustedIssuer{{
		Name: as2TrustRef, IssuerURL: t.Issuer, JWKSURL: t.JWKSURL, CAFilePath: t.CAFile, AllowPrivateIPs: t.AllowPrivateAddresses,
	}}
	return trusted, &authserver.JWTBearerInboundGrantRunConfig{IssuerPolicies: []authserver.JWTBearerIssuerPolicyRunConfig{{
		IssuerRef: as2TrustRef, MaxAssertionAge: t.MaxAssertionAge.String(),
		SubjectBindings: bindings, AcceptedAudiences: []string{issuer},
		AcceptedAssertionTypes: []tokenexchange.JWTBearerAssertionType{tokenexchange.JWTBearerAssertionTypeIDJAG},
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
