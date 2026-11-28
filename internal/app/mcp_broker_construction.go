package app

import (
	"context"
	"crypto"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/stacklok/mecatl/adapters/redisstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/agentcredential"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/inprocesshttp"
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

// as1Trust names the MCP broker adapter's AS-2 trust in AS-1 for
// Config.IdentityBrokerAS2Trust, so only this file imports the adapter.
type as1Trust = mcpbroker.AS1Trust

// mcpBrokerTestSeam names the MCP broker adapter's test seam for
// Config.MCPBrokerTestSeam, so only this file imports the adapter.
type mcpBrokerTestSeam = mcpbroker.TestSeam

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
	as2 *identityBrokerAS2
	// gatewayServers are the servers whose calls the broker sends through the
	// identity broker's gateway; nil when it sends none.
	gatewayServers []string
	closer         func() error
}

// mcpBrokerIdentity is what the identity broker gives the MCP broker; the
// zero value is no identity broker, or one without an embedded gateway
// entry, and leaves the MCP broker as it is without the identity broker.
type mcpBrokerIdentity struct {
	// clientKeySetFile is the path of the broker client's public JWK Set, the
	// identity broker's operator file. The bundled process registers the
	// broker client with it at its authorization server, which becomes AS-2.
	// It is set only with entry.
	clientKeySetFile string
	// entry is the identity broker's embedded gateway entry, which the AS-2
	// set up must be; nil sets up no AS-2.
	entry *identitybroker.GatewayEntry
	// brokerKeys are the identity broker's public keys, which AS-2 must not
	// sign with; set with entry.
	brokerKeys []crypto.PublicKey
	// as2RequestTimeout bounds each request the identity broker makes at
	// AS-2; set with entry.
	as2RequestTimeout time.Duration
	// gatewayCalls is nil when the identity broker has no gateway path.
	gatewayCalls *mcpBrokerGatewayCalls
}

// deriveEmbeddedGateway is the embedded gateway the bundled MCP broker will
// run for declaration, derived before the identity broker is built so its
// gateway entry can be checked against it: AS-2's endpoints for the callback
// URL and the OAuth routes. It is nil without the bundled broker, without an
// OAuth route, or for a callback URL AS-2 cannot derive its issuer from,
// which the bundled broker refuses anyway.
func deriveEmbeddedGateway(cfg Config, declaration mcpauthority.BrokerConfig, selected bool) *embeddedGateway {
	if !selected || cfg.selectsConfiguredMCPBroker() {
		return nil
	}
	var routes []string
	for _, route := range declaration.Routes {
		if route.Auth.Mode == "oauth" {
			routes = append(routes, route.Name)
		}
	}
	if len(routes) == 0 {
		return nil
	}
	issuer, tokenEndpoint, resource, err := mcpbroker.AS2Endpoints(declaration.CallbackURL)
	if err != nil {
		return nil
	}
	return &embeddedGateway{Issuer: issuer, TokenEndpoint: tokenEndpoint, Resource: resource, Routes: routes}
}

// as2SigningPublicKeys reads the embedded gateway's signing key and its
// fallbacks as its authorization server does, and returns their public keys.
func as2SigningPublicKeys(key as2SigningKey) ([]crypto.PublicKey, error) {
	return mcpbroker.SigningPublicKeys(key.file, key.fallbacks)
}

// mcpBrokerGatewayCalls is what the MCP broker needs to send gateway calls
// through the identity broker.
type mcpBrokerGatewayCalls struct {
	service identitybroker.Service
	// credentials is the holder the server registers agents into.
	credentials *agentcredential.Holder
	// servers are the servers the identity broker's gateway routes.
	servers []string
}

// gatewayInProcessClient reaches the embedded vMCP at endpoint straight from
// its handler, streaming its responses, and refuses every other URL. It sets
// no timeout: a call's context bounds it.
func gatewayInProcessClient(vmcp http.Handler, endpoint string) (*http.Client, error) {
	target, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("gateway endpoint: %w", err)
	}
	return inprocesshttp.NewStreamingClient(vmcp, func(u *url.URL) error {
		if u.Scheme != target.Scheme || u.Host != target.Host || u.User != nil || u.Path != target.Path {
			return fmt.Errorf("gateway in-process client refuses %s://%s%s: not the gateway endpoint", u.Scheme, u.Host, u.Path)
		}
		return nil
	}), nil
}

// checkGatewayResource refuses a gateway, when there is one, whose resource is
// not the one AS-2 issues the identity broker's gateway tokens for.
func checkGatewayResource(gateway mcpbroker.Gateway, as2 *mcpbroker.AS2) error {
	if gateway != nil && (as2 == nil || as2.Resource != gateway.Resource()) {
		return errGatewayIsNotAS2Resource
	}
	return nil
}

// as2InProcessClient reaches AS-2's token endpoint straight from the embedded
// authorization server, without a network listener, and refuses every other
// URL, each call bounded by timeout.
func as2InProcessClient(as2 *mcpbroker.AS2, timeout time.Duration) (*http.Client, error) {
	token, err := url.Parse(as2.TokenEndpoint)
	if err != nil {
		return nil, fmt.Errorf("AS-2 token endpoint: %w", err)
	}
	client := inprocesshttp.NewClient(as2.Handler(), func(u *url.URL) error {
		if u.Scheme != token.Scheme || u.Host != token.Host || u.User != nil || u.Path != token.Path {
			return fmt.Errorf("AS-2 in-process client refuses %s://%s%s: not the token endpoint", u.Scheme, u.Host, u.Path)
		}
		return nil
	})
	client.Timeout = timeout
	return client, nil
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
	// Startup allows a signing key only with an embedded gateway entry
	// (checkGatewayEntry).
	config.SigningKeyFile, config.FallbackKeyFiles = declaration.SigningKeyFile, append([]string(nil), declaration.FallbackKeyFiles...)
	if identity.entry != nil && identity.clientKeySetFile != "" {
		config.BrokerClient = &mcpbroker.ToolHiveBrokerClient{ID: identitybroker.BrokerClientID, KeySetFile: identity.clientKeySetFile}
	}
	config.AS1Trust = cfg.IdentityBrokerAS2Trust
	config.TestSeam = cfg.MCPBrokerTestSeam
	if calls := identity.gatewayCalls; calls != nil && config.BrokerClient != nil {
		// Shortcut: only servers the identity broker's gateway entry routes use the gateway path, and production's routes none until AS-2 trusts AS-1, so production is unchanged; removed by #2533.
		config.GatewayCalls = &mcpbroker.GatewayCalls{
			Authorizer:  calls.service,
			Credentials: calls.credentials,
			Servers:     append([]string(nil), calls.servers...),
		}
		config.EmbeddedGatewayClient = gatewayInProcessClient
	}
	process, err := mcpbroker.NewToolHiveProcess(ctx, config)
	if err != nil {
		authStorageClose()
		return mcpBrokerParts{}, fmt.Errorf("build bundled MCP broker: %w", err)
	}
	if err := checkGatewayResource(process.Gateway, process.AS2); err != nil {
		_ = process.Close()
		authStorageClose()
		return mcpBrokerParts{}, fmt.Errorf("build bundled MCP broker: %w", err)
	}
	var gatewayServers []string
	if process.Gateway != nil {
		gatewayServers = config.GatewayCalls.Servers
	}
	// Startup checked the entry and the signing key files against the AS-2
	// it derived; this checks the AS-2 the process set up, and the keys it
	// loaded, which a changed file could make differ.
	if entry := identity.entry; entry != nil {
		if err := checkBuiltAS2(process.AS2, *entry, identity.brokerKeys); err != nil {
			_ = process.Close()
			authStorageClose()
			return mcpBrokerParts{}, fmt.Errorf("build bundled MCP broker: %w", err)
		}
	}
	var as2 *identityBrokerAS2
	if process.AS2 != nil {
		httpClient, err := as2InProcessClient(process.AS2, identity.as2RequestTimeout)
		if err != nil {
			_ = process.Close()
			authStorageClose()
			return mcpBrokerParts{}, fmt.Errorf("build bundled MCP broker: %w", err)
		}
		if wrap := cfg.identityBrokerAS2Transport; wrap != nil {
			httpClient.Transport = wrap(httpClient.Transport)
		}
		as2 = &identityBrokerAS2{Issuer: process.AS2.Issuer, TokenEndpoint: process.AS2.TokenEndpoint, Resource: process.AS2.Resource, HTTPClient: httpClient}
	}
	parts := mcpBrokerParts{
		service:             process.Runtime,
		inspector:           process.Runtime,
		workspaceEnrollment: process.WorkspaceEnrollmentRequired(),
		callbackPath:        process.CallbackPath,
		as2:                 as2,
		gatewayServers:      gatewayServers,
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

// checkBuiltAS2 refuses a built AS-2 other than entry's, naming the field
// that differs, and one that loaded a signing key among brokerKeys.
func checkBuiltAS2(as2 *mcpbroker.AS2, entry identitybroker.GatewayEntry, brokerKeys []crypto.PublicKey) error {
	if as2 == nil {
		return fmt.Errorf("%w: the MCP broker set up no AS-2", errAS2IsNotTheEntry)
	}
	for _, f := range []struct{ name, built, entry string }{
		{"issuer", as2.Issuer, entry.AS2Issuer},
		{"token endpoint", as2.TokenEndpoint, entry.TokenEndpoint},
		{"resource", as2.Resource, entry.Resource},
	} {
		if f.built != f.entry {
			return fmt.Errorf("%w: its %s is %q, the entry's is %q", errAS2IsNotTheEntry, f.name, f.built, f.entry)
		}
	}
	return checkAS2KeysAreNotBrokerKeys(as2.SigningPublicKeys(), brokerKeys)
}
