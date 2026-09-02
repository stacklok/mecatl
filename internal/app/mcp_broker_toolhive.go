package app

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

// toolHiveBrokerConfig is the construction boundary: operator-schema values are
// copied once into immutable adapter-owned values before ToolHive sees them.
func toolHiveBrokerConfig(routes []permconfig.MCPServerProfile, callbackURL string, occupied []string) mcpbroker.ToolHiveConfig {
	profiles := make([]mcpbroker.ToolHiveProfile, len(routes))
	for i, route := range routes {
		profile := mcpbroker.ToolHiveProfile{Name: route.Name, URL: route.URL, Auth: route.Auth.Mode}
		if oauth := route.Auth.OAuth; oauth != nil {
			converted := &mcpbroker.ToolHiveOAuth{Issuer: oauth.Issuer, Scopes: append([]string(nil), oauth.Scopes...)}
			if oauth.Upstream != nil && oauth.Upstream.OAuth2 != nil {
				converted.AuthorizationEndpoint = oauth.Upstream.OAuth2.AuthorizationEndpoint
				converted.TokenEndpoint = oauth.Upstream.OAuth2.TokenEndpoint
			}
			if oauth.Client.Preregistered != nil {
				converted.ClientID = oauth.Client.Preregistered.ID
				converted.ClientSecretEnv = oauth.Client.Preregistered.SecretEnv
			} else if oauth.Client.CIMD != nil {
				converted.ClientID = oauth.Client.CIMD.DocumentURL
			}
			profile.OAuth = converted
			profile.Static = make([]mcpbroker.StaticTool, len(oauth.Tools))
			for j, candidate := range oauth.Tools {
				profile.Static[j] = mcpbroker.StaticTool{Name: candidate.Name, Description: candidate.Description, Schema: append(json.RawMessage(nil), candidate.InputSchema...), ReadOnly: candidate.ReadOnly}
			}
		}
		profiles[i] = profile
	}
	return mcpbroker.ToolHiveConfig{Profiles: profiles, CallbackURL: callbackURL, Occupied: append([]string(nil), occupied...)}
}

func mcpBrokerCallbackPath(raw string) (string, error) {
	callback, err := url.Parse(raw)
	if err != nil || callback.Path == "" || callback.EscapedPath() != callback.Path {
		return "", fmt.Errorf("invalid MCP broker callback URL")
	}
	return callback.Path, nil
}
