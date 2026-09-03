package mcpbroker

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/vmcp"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

const (
	authNone  = "none"
	authOAuth = "oauth"
)

// ToolHiveConfig is the immutable adapter-owned input to bundled process
// construction. Composition reduces the operator schema to this value before
// invoking the adapter.
type ToolHiveConfig struct {
	CallbackURL string
	Profiles    []ToolHiveProfile
	Occupied    []string
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
	ClientSecretEnv       string
	Scopes                []string
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
	protectedRoutes := 0
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
			protectedRoutes++
			if protectedRoutes > 1 {
				return toolHiveConstruction{}, fmt.Errorf("%w: at most one OAuth upstream is supported by the shared callback", ErrProtectedRouteUnsupported)
			}
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
			if len(profile.Static) == 0 {
				out.protectedBackends = append(out.protectedBackends, profile.Name)
			}
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
	if oauth.ClientID == "" {
		return authserver.UpstreamRunConfig{}, fmt.Errorf("%w: protected upstream %q is missing client identity", ErrInvalidCatalogue, profile.Name)
	}
	redirect := issuer + "/oauth/callback"
	if oauth.AuthorizationEndpoint != "" || oauth.TokenEndpoint != "" {
		if oauth.AuthorizationEndpoint == "" || oauth.TokenEndpoint == "" {
			return authserver.UpstreamRunConfig{}, fmt.Errorf("%w: protected upstream %q has partial OAuth2 endpoints", ErrInvalidCatalogue, profile.Name)
		}
		return authserver.UpstreamRunConfig{Name: provider, Type: authserver.UpstreamProviderTypeOAuth2, OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
			AuthorizationEndpoint: oauth.AuthorizationEndpoint, TokenEndpoint: oauth.TokenEndpoint, ClientID: oauth.ClientID,
			ClientSecretEnvVar: oauth.ClientSecretEnv, RedirectURI: redirect, Scopes: append([]string(nil), oauth.Scopes...),
		}}, nil
	}
	if oauth.Issuer == "" {
		return authserver.UpstreamRunConfig{}, fmt.Errorf("%w: protected upstream %q is missing an issuer", ErrInvalidCatalogue, profile.Name)
	}
	return authserver.UpstreamRunConfig{Name: provider, Type: authserver.UpstreamProviderTypeOIDC, OIDCConfig: &authserver.OIDCUpstreamRunConfig{
		IssuerURL: oauth.Issuer, ClientID: oauth.ClientID, ClientSecretEnvVar: oauth.ClientSecretEnv,
		RedirectURI: redirect, Scopes: append([]string(nil), oauth.Scopes...),
	}}, nil
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

func cloneProviderByBackend(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for backend, provider := range in {
		out[backend] = provider
	}
	return out
}
