package cliconfig

import (
	"fmt"
	"net/url"

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
	issuer, err := url.Parse(oauth.Issuer)
	if err != nil || issuer.Scheme != "https" {
		return fmt.Errorf("%w: MCP server %q: direct DCR requires an HTTPS issuer", ErrMCPProfileInvalid, route.Name)
	}
	if oauth.Credentials.Mode != "local" || oauth.Credentials.Local == nil || oauth.Credentials.Environment != nil {
		return fmt.Errorf("%w: MCP server %q: direct DCR requires mutable local credentials", ErrMCPProfileInvalid, route.Name)
	}
	if oauth.RequestRefreshToken {
		return fmt.Errorf("%w: MCP server %q: direct DCR does not support refresh tokens", ErrMCPProfileInvalid, route.Name)
	}
	if len(oauth.Scopes) != 0 && !sameStringSet(oauth.Scopes, []string{"openid"}) {
		return fmt.Errorf("%w: MCP server %q: direct DCR scopes must contain only openid", ErrMCPProfileInvalid, route.Name)
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
	if section != nil && (len(section.Servers) != 0 || section.Broker.CallbackURL != "") {
		return nil, fmt.Errorf("%w: broker profiles and callbacks are broker-owned; move mcp.servers to mecabroker profiles and mcp.broker.callback_url to mecabroker callback_url", ErrMCPProfileInvalid)
	}
	return mcpauthority.NewBroker(mcpauthority.BrokerConfig{}), nil
}
