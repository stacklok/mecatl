package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/stacklok/mecatl/adapters/redisstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/identitybroker"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// This file is the single construction site of the MCP broker adapter: it is
// the only non-test file in composition or the command roots that names
// internal/adapter/mcpbroker (depguard rule internal-adapter-mcpbroker-importers).
// Everything else receives the contract values in mcpBrokerParts.

// ConfiguredMCPBroker holds the inputs of the configured (non-bundled) MCP
// broker path, embedded in Config. Any non-empty field selects that path
// instead of the bundled ToolHive process. Inputs passed to buildMCPBroker
// outside ConfiguredMCPBroker never change the selected path.
//
// It is a seam for tests and test fixtures, which inject callers and
// discovered tools; its fields name adapter types, so command roots (kept off
// the adapter by depguard) never set it and always get the bundled process.
type ConfiguredMCPBroker struct {
	MCPBrokerDiscovered       []mcpbroker.ToolDefinition
	MCPBrokerCaller           mcpbroker.Caller
	MCPBrokerAuthorizedCaller mcpbroker.AuthorizedCaller
	MCPBrokerQueryCaller      mcpbroker.QueryCaller
	MCPBrokerOptions          []mcpbroker.Option
}

// selectsConfiguredMCPBroker reports whether any field is set.
func (c ConfiguredMCPBroker) selectsConfiguredMCPBroker() bool {
	return c.MCPBrokerCaller != nil || c.MCPBrokerAuthorizedCaller != nil || c.MCPBrokerQueryCaller != nil ||
		len(c.MCPBrokerDiscovered) != 0 || len(c.MCPBrokerOptions) != 0
}

// mcpBrokerParts is a constructed MCP broker seen only through its contract.
// The zero value is no broker; close is then a no-op.
type mcpBrokerParts struct {
	service brokercontract.Service
	// inspector is set only for the bundled ToolHive process.
	inspector brokercontract.ConnectorInspector
	// workspaceEnrollment is true only when the bundled process needs
	// authenticate-then-discover enrollment.
	workspaceEnrollment bool
	// handlers is nil when the broker exposes no HTTP surface.
	handlers     brokercontract.HTTPHandlers
	callbackPath string
	// as2 is set only when the bundled process set up AS-2.
	as2    *identityBrokerAS2
	closer func() error
}

// mcpBrokerIdentity is what the identity broker gives the MCP broker; the
// zero value is no identity broker.
type mcpBrokerIdentity struct {
	// clientKeySet is the broker client's public JWK Set. With a protected
	// route, the bundled process registers the broker client with it at its
	// authorization server, which becomes AS-2. Empty sets up no AS-2.
	clientKeySet []byte
	// as1Issuer is AS-1's issuer, which AS-2's must differ from; empty when
	// the identity broker has no AS-1.
	as1Issuer string
}

func (p mcpBrokerParts) close() {
	if p.closer != nil {
		_ = p.closer()
	}
}

// buildMCPBroker constructs the bundled ToolHive process or, when
// cfg.ConfiguredMCPBroker is selected, a broker Runtime over the configured
// callers. On error it has already released everything it built.
func buildMCPBroker(ctx context.Context, cfg Config, declaration mcpauthority.BrokerConfig, occupied []string, identity mcpBrokerIdentity) (mcpBrokerParts, error) {
	if !cfg.selectsConfiguredMCPBroker() {
		return buildBundledMCPBroker(ctx, cfg, declaration, occupied, identity)
	}
	return buildConfiguredMCPBroker(cfg.ConfiguredMCPBroker, declaration, occupied)
}

func buildBundledMCPBroker(ctx context.Context, cfg Config, declaration mcpauthority.BrokerConfig, occupied []string, identity mcpBrokerIdentity) (mcpBrokerParts, error) {
	authRedisClient, authStorageClose, err := buildToolHiveAuthRedisClient(cfg)
	if err != nil {
		return mcpBrokerParts{}, fmt.Errorf("build bundled MCP broker: %w", err)
	}
	config := toolHiveBrokerConfig(declaration.Routes, declaration.CallbackURL, occupied, authRedisClient, cfg.diag())
	if len(identity.clientKeySet) != 0 {
		config.BrokerClient = &mcpbroker.ToolHiveBrokerClient{ID: identitybroker.BrokerClientID, KeySet: identity.clientKeySet}
	}
	process, err := mcpbroker.NewToolHiveProcess(ctx, config)
	if err != nil {
		authStorageClose()
		return mcpBrokerParts{}, fmt.Errorf("build bundled MCP broker: %w", err)
	}
	var as2 *identityBrokerAS2
	if process.AS2 != nil {
		if identity.as1Issuer != "" && sameIssuer(process.AS2.Issuer, identity.as1Issuer) {
			_ = process.Close()
			authStorageClose()
			return mcpBrokerParts{}, fmt.Errorf("build bundled MCP broker: %w: %s", errAS2IssuerIsAS1s, identity.as1Issuer)
		}
		httpClient := process.AS2.InProcessClient()
		httpClient.Timeout = as2RequestTimeout
		as2 = &identityBrokerAS2{Issuer: process.AS2.Issuer, TokenEndpoint: process.AS2.TokenEndpoint, Resource: process.AS2.Resource, HTTPClient: httpClient}
	}
	parts := mcpBrokerParts{
		service:             process.Runtime,
		inspector:           process.Runtime,
		workspaceEnrollment: process.WorkspaceEnrollmentRequired(),
		callbackPath:        process.CallbackPath,
		as2:                 as2,
		// The process closes the Redis client only through its embedded auth
		// server, which exists only for protected upstreams. Close it here too;
		// go-redis Close is idempotent.
		closer: func() error {
			err := process.Close()
			authStorageClose()
			return err
		},
	}
	if !process.Handlers.Empty() {
		parts.handlers = process.Handlers
	}
	return parts, nil
}

func buildConfiguredMCPBroker(configured ConfiguredMCPBroker, declaration mcpauthority.BrokerConfig, occupied []string) (mcpBrokerParts, error) {
	catalogue, err := mcpbroker.Compile(declaration, configured.MCPBrokerDiscovered, occupied)
	if err != nil {
		return mcpBrokerParts{}, fmt.Errorf("build MCP broker catalogue: %w", err)
	}
	options := append([]mcpbroker.Option(nil), configured.MCPBrokerOptions...)
	if configured.MCPBrokerAuthorizedCaller != nil {
		options = append(options, mcpbroker.WithAuthorizedCaller(configured.MCPBrokerAuthorizedCaller))
	}
	if configured.MCPBrokerQueryCaller != nil {
		options = append(options, mcpbroker.WithQueryCaller(configured.MCPBrokerQueryCaller))
	}
	runtime, err := mcpbroker.New(catalogue, configured.MCPBrokerCaller, options...)
	if err != nil {
		return mcpBrokerParts{}, fmt.Errorf("build MCP broker: %w", err)
	}
	parts := mcpBrokerParts{service: runtime, closer: runtime.Close}
	if declaration.CallbackURL != "" {
		handlers, callbackPath, err := runtime.Handlers(declaration.CallbackURL)
		if err != nil {
			parts.close()
			return mcpBrokerParts{}, fmt.Errorf("build MCP broker handlers: %w", err)
		}
		parts.handlers, parts.callbackPath = handlers, callbackPath
	}
	return parts, nil
}

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
				converted.ClientSecretFile = oauth.Client.Preregistered.SecretFile
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
