package mcpbroker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/redis/go-redis/v9"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
	"github.com/stacklok/toolhive/pkg/vmcp"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"

	"github.com/stacklok/mecatl/engine/port"
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
	// CallbackURL is the public URL whose path receives OAuth callbacks for this process.
	CallbackURL string
	// Profiles declares the ordered upstream MCP backends and their authentication mode.
	Profiles []ToolHiveProfile
	// ReservedToolNames contains model-visible names supplied by the surrounding
	// core/global catalogue. Broker discovery rejects collisions before it creates
	// an attachment.
	ReservedToolNames []string
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
	// lifecycle — EmbeddedAuthServer.Close calls storage.Close, which closes
	// the client it was given, so no separate cleanup is needed beyond that.
	AuthRedisClient redis.UniversalClient
	// ProtectedStorage is the production encrypted storage seam for OAuth profiles.
	ProtectedStorage *ProtectedStorageConfig
	// Diagnostics receives per-backend authenticated-discovery outcomes during
	// workspace-enrollment catalogue freeze (success + tool count, or failure +
	// backend name) — see stageAuthenticatedRoutes. A nil value defaults to
	// port.NopDiagnostics{}, matching every other nil-safe Diagnostics consumer.
	Diagnostics port.Diagnostics
}

// ToolHiveProfile is one configured Streamable HTTP upstream.
type ToolHiveProfile struct {
	// Name is the stable broker-side backend name used to route discovered tools.
	Name string
	// URL is the upstream Streamable HTTP endpoint contacted by ToolHive.
	URL string
	// Auth selects the upstream authentication mode, currently "none" or "oauth".
	Auth string
	// OAuth supplies the upstream OAuth and client-registration details when Auth is "oauth".
	OAuth *ToolHiveOAuth
	// Static declares trusted protected tools available before authenticated discovery.
	Static []StaticTool
}

// ToolHiveOAuth contains only values needed to construct ToolHive's upstream.
type ToolHiveOAuth struct {
	// Issuer identifies the upstream authorization-server issuer when metadata is used.
	Issuer string
	// AuthorizationEndpoint is the upstream endpoint where the user grants access.
	AuthorizationEndpoint string
	// TokenEndpoint is the upstream endpoint used to exchange codes and refresh tokens.
	TokenEndpoint string
	// ClientID identifies the preregistered OAuth client.
	ClientID string
	// ClientSecretFile names the local file read when confidential-client authentication is needed.
	ClientSecretFile string
	// Scopes are requested for the upstream grant.
	Scopes []string
	// RequestRefreshToken asks the upstream for refresh-token-capable authorization.
	RequestRefreshToken bool
	// DCRDiscoveryURL enables RFC 7591 registration through RFC 8414 metadata.
	DCRDiscoveryURL string
}

// StaticTool is one trusted protected tool declaration. Its schema is copied
// into the frozen model-facing catalogue; backend routing remains private.
type StaticTool struct {
	// Name and Description are the model-visible identity and help text.
	Name, Description string
	// Schema is the JSON input schema copied into the frozen tool specification.
	Schema json.RawMessage
	// ReadOnly marks the tool for dispatch policy; it does not grant upstream authorization.
	ReadOnly bool
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
		if oauth.ClientID != "" || oauth.ClientSecretFile != "" {
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
		return authserver.UpstreamRunConfig{}, fmt.Errorf("%w: protected upstream %q client secret file is invalid", ErrInvalidCatalogue, profile.Name)
	}
	if oauth.AuthorizationEndpoint != "" || oauth.TokenEndpoint != "" {
		return toolHiveOAuth2Upstream(profile, provider, issuer, nil)
	}
	if oauth.Issuer == "" {
		return authserver.UpstreamRunConfig{}, fmt.Errorf("%w: protected upstream %q is missing an issuer", ErrInvalidCatalogue, profile.Name)
	}
	redirect := issuer + "/oauth/callback"
	return authserver.UpstreamRunConfig{Name: provider, Type: authserver.UpstreamProviderTypeOIDC, OIDCConfig: &authserver.OIDCUpstreamRunConfig{
		IssuerURL: oauth.Issuer, ClientID: oauth.ClientID, ClientSecretFile: oauth.ClientSecretFile,
		RedirectURI: redirect, Scopes: append([]string(nil), oauth.Scopes...),
		AdditionalAuthorizationParams: toolHiveAdditionalAuthorizationParams(oauth),
	}}, nil
}

func toolHiveOAuth2Upstream(profile ToolHiveProfile, provider, issuer string, dcr *authserver.DCRUpstreamConfig) (authserver.UpstreamRunConfig, error) {
	oauth := profile.OAuth
	if oauth.AuthorizationEndpoint == "" || oauth.TokenEndpoint == "" {
		return authserver.UpstreamRunConfig{}, fmt.Errorf("%w: protected upstream %q has partial OAuth2 endpoints", ErrInvalidCatalogue, profile.Name)
	}
	config := &authserver.OAuth2UpstreamRunConfig{
		AuthorizationEndpoint: oauth.AuthorizationEndpoint, TokenEndpoint: oauth.TokenEndpoint, ClientID: oauth.ClientID,
		ClientSecretFile: oauth.ClientSecretFile, RedirectURI: issuer + "/oauth/callback", Scopes: append([]string(nil), oauth.Scopes...),
		AdditionalAuthorizationParams: toolHiveAdditionalAuthorizationParams(oauth), DCRConfig: dcr,
	}
	if oauth.ClientSecretFile != "" {
		config.TokenEndpointAuthMethod = oauthproto.TokenEndpointAuthMethodClientSecretBasic
	}
	return authserver.UpstreamRunConfig{Name: provider, Type: authserver.UpstreamProviderTypeOAuth2, OAuth2Config: config}, nil
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

const maxOAuthClientSecretBytes = 64 << 10

// validateClientSecretFile proves that the configured credential is present and
// bounded without retaining it. ToolHive reads the same path only while building
// its in-process upstream client.
func validateClientSecretFile(path string) error {
	if path == "" {
		return nil
	}
	_, err := readClientSecretFile(path)
	if err != nil {
		return err
	}
	return nil
}

func readClientSecretFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	secret, err := io.ReadAll(io.LimitReader(file, maxOAuthClientSecretBytes+1))
	if err != nil || len(secret) > maxOAuthClientSecretBytes {
		return "", errors.New("invalid")
	}
	value := strings.TrimSpace(string(secret))
	clear(secret)
	if value == "" {
		return "", errors.New("invalid")
	}
	return value, nil
}
