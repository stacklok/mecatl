package cliconfig

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

// MCPAuthorityOptions supplies the already-parsed operator block and command-root policy.
type MCPAuthorityOptions struct {
	Operator        *permconfig.MCPSection
	Legacy          *MCPServerList
	LookupEnv       func(string) (string, bool)
	DefaultMode     mcpauthority.Mode
	BrokerSupported bool
}

// ResolveMCPAuthority applies exactly one mode-specific validation and loading
// path. Broker mode retains neutral declarations and opens no global resources.
func ResolveMCPAuthority(opts MCPAuthorityOptions) (*mcpauthority.Result, error) {
	mode := opts.DefaultMode
	if mode != mcpauthority.Global && mode != mcpauthority.Broker {
		return nil, fmt.Errorf("%w: MCP root default must be global or broker", ErrMCPProfileInvalid)
	}
	if opts.Operator != nil && opts.Operator.Mode != "" {
		mode = mcpauthority.Mode(opts.Operator.Mode)
	}
	if mode != mcpauthority.Global && mode != mcpauthority.Broker {
		return nil, fmt.Errorf("%w: mcp.mode must be global or broker", ErrMCPProfileInvalid)
	}
	if mode == mcpauthority.Broker {
		if !opts.BrokerSupported {
			return nil, fmt.Errorf("%w: broker MCP mode is unsupported by this command root", ErrMCPProfileInvalid)
		}
		if opts.Legacy != nil && len(opts.Legacy.entries) != 0 {
			return nil, fmt.Errorf("%w: --mcp-server is global-only and conflicts with mcp.mode: broker", ErrMCPProfileInvalid)
		}
		return resolveBrokerAuthority(opts.Operator)
	}
	return resolveGlobalAuthority(opts)
}

func resolveGlobalAuthority(opts MCPAuthorityOptions) (*mcpauthority.Result, error) {
	if opts.Operator != nil && opts.Operator.Broker.CallbackURL != "" {
		return nil, fmt.Errorf("%w: mcp.broker.callback_url is inert in global mode", ErrMCPProfileInvalid)
	}
	if opts.Operator != nil {
		for _, route := range opts.Operator.Servers {
			if route.Auth.OAuth == nil {
				continue
			}
			if route.Auth.OAuth.Upstream != nil {
				return nil, fmt.Errorf("%w: MCP server %q: oauth upstream selection is broker-only", ErrMCPProfileInvalid, route.Name)
			}
			if err := validateGlobalOAuth(route); err != nil {
				return nil, err
			}
		}
	}
	profiles, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: opts.Operator, Legacy: opts.Legacy, LookupEnv: opts.LookupEnv})
	if err != nil {
		return nil, err
	}
	return mcpauthority.NewGlobal(profiles.Servers, profiles), nil
}

func validateGlobalOAuth(route permconfig.MCPServerProfile) error {
	oauth := route.Auth.OAuth
	if oauth.Profile == "" || oauth.Principal == "" || oauth.Issuer == "" || oauth.Network == nil || oauth.Credentials.Mode == "" {
		return fmt.Errorf("%w: MCP server %q: global OAuth requires profile, principal, issuer, credentials, and network", ErrMCPProfileInvalid, route.Name)
	}
	if oauth.Upstream != nil {
		return fmt.Errorf("%w: MCP server %q: oauth upstream selection is broker-only", ErrMCPProfileInvalid, route.Name)
	}
	if oauth.Client.Mode != "dcr" {
		if len(oauth.Scopes) == 0 {
			return fmt.Errorf("%w: MCP server %q: global OAuth requires scopes", ErrMCPProfileInvalid, route.Name)
		}
		return nil
	}
	if oauth.Client.DCR == nil || oauth.Client.DCR.DiscoveryURL != "" {
		return fmt.Errorf("%w: MCP server %q: direct DCR requires an empty dcr payload", ErrMCPProfileInvalid, route.Name)
	}
	if oauth.Credentials.Mode != "local" || oauth.Credentials.Local == nil || oauth.Credentials.Environment != nil {
		return fmt.Errorf("%w: MCP server %q: direct DCR requires mutable local credentials", ErrMCPProfileInvalid, route.Name)
	}
	refresh := true
	if oauth.RequestRefreshTokenSet {
		refresh = oauth.RequestRefreshToken
	}
	want := []string{"openid"}
	if refresh {
		want = append(want, "offline_access")
	}
	if len(oauth.Scopes) != 0 && !sameStringSet(oauth.Scopes, want) {
		return fmt.Errorf("%w: MCP server %q: direct DCR scopes do not match refresh selection", ErrMCPProfileInvalid, route.Name)
	}
	return nil
}

func sameStringSet(got, want []string) bool {
	set := make(map[string]struct{}, len(got))
	for _, value := range got {
		set[value] = struct{}{}
	}
	if len(set) != len(want) {
		return false
	}
	for _, value := range want {
		if _, ok := set[value]; !ok {
			return false
		}
	}
	return true
}

func resolveBrokerAuthority(section *permconfig.MCPSection) (*mcpauthority.Result, error) {
	if section == nil {
		return mcpauthority.NewBroker(mcpauthority.BrokerConfig{}), nil
	}
	oauthCount := 0
	for _, route := range section.Servers {
		switch route.Auth.Mode {
		case "none":
		case "static_bearer":
			return nil, fmt.Errorf("%w: MCP server %q: static_bearer is unsupported in broker mode", ErrMCPProfileInvalid, route.Name)
		case "oauth":
			oauthCount++
			if err := validateBrokerOAuth(route); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("%w: MCP server %q: invalid auth mode", ErrMCPProfileInvalid, route.Name)
		}
	}
	callback := section.Broker.CallbackURL
	if oauthCount > 0 {
		var err error
		callback, err = normalizeBrokerCallbackURL(callback)
		if err != nil {
			return nil, err
		}
	} else if callback != "" {
		return nil, fmt.Errorf("%w: mcp.broker.callback_url requires a broker OAuth server", ErrMCPProfileInvalid)
	}
	return mcpauthority.NewBroker(mcpauthority.BrokerConfig{Routes: section.Servers, CallbackURL: callback}), nil
}

func validateBrokerOAuth(route permconfig.MCPServerProfile) error {
	oauth := route.Auth.OAuth
	if oauth == nil || len(oauth.Scopes) == 0 || oauth.Network == nil || oauth.Client.Mode == "" {
		return fmt.Errorf("%w: MCP server %q: broker OAuth requires client, scopes, and network", ErrMCPProfileInvalid, route.Name)
	}
	if err := validateBrokerOAuthUpstream(route.Name, oauth); err != nil {
		return err
	}
	if len(oauth.Network.AdditionalOrigins) != 0 || len(oauth.Network.PrivateOrigins) != 0 || oauth.Network.MaxRedirects != 0 {
		return fmt.Errorf("%w: MCP server %q: broker OAuth accepts only an empty network policy because ToolHive cannot enforce exact-origin network controls", ErrMCPProfileInvalid, route.Name)
	}
	if oauth.Profile != "" || oauth.Principal != "" || oauth.Credentials.Mode != "" || oauth.Credentials.Local != nil || oauth.Credentials.Environment != nil {
		return fmt.Errorf("%w: MCP server %q: broker OAuth forbids profile, principal, and credentials", ErrMCPProfileInvalid, route.Name)
	}
	return nil
}

func validateBrokerOAuthUpstream(routeName string, oauth *permconfig.MCPOAuthProfile) error {
	if oauth.Client.Mode == "dcr" {
		if oauth.Client.DCR == nil || oauth.Client.DCR.DiscoveryURL == "" || oauth.Upstream == nil || oauth.Upstream.Mode != "oauth2" || oauth.Upstream.OAuth2 == nil || oauth.Issuer != "" {
			return fmt.Errorf("%w: MCP server %q: broker DCR requires OAuth2 upstream, discovery_url, and no issuer", ErrMCPProfileInvalid, routeName)
		}
		return nil
	}
	if oauth.Upstream == nil || oauth.Upstream.Mode == "oidc" {
		if oauth.Issuer == "" {
			return fmt.Errorf("%w: MCP server %q: broker OIDC requires issuer", ErrMCPProfileInvalid, routeName)
		}
		return nil
	}
	if oauth.Upstream.Mode != "oauth2" || oauth.Upstream.OAuth2 == nil || oauth.Upstream.OAuth2.AuthorizationEndpoint == "" || oauth.Upstream.OAuth2.TokenEndpoint == "" || oauth.Issuer != "" {
		return fmt.Errorf("%w: MCP server %q: broker OAuth2 requires explicit endpoints and forbids issuer", ErrMCPProfileInvalid, routeName)
	}
	return nil
}

func normalizeBrokerCallbackURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%w: mcp.broker.callback_url must be an absolute HTTPS URL without userinfo, query, or fragment", ErrMCPProfileInvalid)
	}
	if strings.TrimSpace(raw) != raw {
		return "", fmt.Errorf("%w: mcp.broker.callback_url must not contain surrounding whitespace", ErrMCPProfileInvalid)
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}
