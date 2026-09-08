package app

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/redis/go-redis/v9"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
)

// buildToolHiveAuthRedisClient connects to the operator's configured Redis
// instance for the embedded ToolHive auth server to share (under its own key
// prefix — see mcpbroker.ToolHiveConfig.AuthRedisClient), so ToolHive's inner
// pending OAuth records can survive a pod restart. Mecatl's outer callback and
// enrollment correlation remain process-local; this storage does not recover
// an interrupted outer enrollment. internal/app cannot import the
// vendored toolhive storage package directly (see
// TestToolHiveImportsStayBehindApprovedAdapterLeaves — toolhive imports stay
// behind approved adapter leaves, and composition is not one), so it hands
// mcpbroker a bare redis.UniversalClient and lets the approved leaf
// (toolhive_process.go) build the storage.Storage. When Redis isn't
// configured this returns a nil client, and ToolHiveConfig's own nil fallback
// selects storage.NewMemoryStorage() (dev/non-HA only) — unchanged behavior.
// The returned close func closes the client (a no-op when nil); closing it
// separately is fine even though EmbeddedAuthServer.Close also closes the
// storage wrapping it — go-redis Close is idempotent.
func buildToolHiveAuthRedisClient(cfg Config) (redis.UniversalClient, func(), error) {
	if cfg.RedisURL == "" {
		return nil, func() {}, nil
	}
	client, err := redisstore.NewClient(redisstore.Config{
		Addr:           cfg.RedisURL,
		UsernameFile:   cfg.RedisUsernameFile,
		PasswordFile:   cfg.RedisPasswordFile,
		CAFile:         cfg.RedisTLSCAFile,
		TLS:            cfg.RedisTLS,
		AllowPlaintext: cfg.RedisAllowPlaintext,
		Diagnostics:    cfg.diag(),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("mcp broker auth storage: redis: %w", err)
	}
	return client, func() { _ = client.Close() }, nil
}

// toolHiveBrokerConfig is the construction boundary: operator-schema values are
// copied once into immutable adapter-owned values before ToolHive sees them.
func toolHiveBrokerConfig(routes []permconfig.MCPServerProfile, callbackURL string, occupied []string, authRedisClient redis.UniversalClient, diag port.Diagnostics) mcpbroker.ToolHiveConfig {
	profiles := make([]mcpbroker.ToolHiveProfile, len(routes))
	for i, route := range routes {
		profile := mcpbroker.ToolHiveProfile{Name: route.Name, URL: route.URL, Auth: route.Auth.Mode}
		if oauth := route.Auth.OAuth; oauth != nil {
			converted := &mcpbroker.ToolHiveOAuth{
				Issuer:              oauth.Issuer,
				Scopes:              append([]string(nil), oauth.Scopes...),
				RequestRefreshToken: oauth.RequestRefreshToken,
			}
			if oauth.Upstream != nil && oauth.Upstream.OAuth2 != nil {
				converted.AuthorizationEndpoint = oauth.Upstream.OAuth2.AuthorizationEndpoint
				converted.TokenEndpoint = oauth.Upstream.OAuth2.TokenEndpoint
			}
			if oauth.Client.Preregistered != nil {
				converted.ClientID = oauth.Client.Preregistered.ID
				converted.ClientSecretEnv = oauth.Client.Preregistered.SecretEnv
			} else if oauth.Client.CIMD != nil {
				converted.ClientID = oauth.Client.CIMD.DocumentURL
			} else if oauth.Client.DCR != nil {
				converted.DCRDiscoveryURL = oauth.Client.DCR.DiscoveryURL
			}
			profile.OAuth = converted
			profile.Static = make([]mcpbroker.StaticTool, len(oauth.Tools))
			for j, candidate := range oauth.Tools {
				profile.Static[j] = mcpbroker.StaticTool{Name: candidate.Name, Description: candidate.Description, Schema: append(json.RawMessage(nil), candidate.InputSchema...), ReadOnly: candidate.ReadOnly}
			}
		}
		profiles[i] = profile
	}
	return mcpbroker.ToolHiveConfig{
		Profiles: profiles, CallbackURL: callbackURL, Occupied: append([]string(nil), occupied...),
		AuthRedisClient: authRedisClient, Diagnostics: diag,
	}
}

func mcpBrokerCallbackPath(raw string) (string, error) {
	callback, err := url.Parse(raw)
	if err != nil || callback.EscapedPath() != callback.Path {
		return "", fmt.Errorf("invalid MCP broker callback URL")
	}
	if callback.Path == "" {
		return "/", nil
	}
	return callback.Path, nil
}
