package mcpbroker

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/ory/fosite"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/factory"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	vmcpserver "github.com/stacklok/toolhive/pkg/vmcp/server"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	mcpadapter "github.com/stacklok/mecatl/internal/adapter/mcp"
)

const (
	toolHiveBasePath = "/v1/mcp/broker"
	toolHiveMCPPath  = toolHiveBasePath + "/mcp"
)

type ownedResource struct {
	name  string
	close func() error
}

// Process is the single owner of a valid broker Runtime and all bundled
// ToolHive resources. ToolHive values never cross the neutral broker boundary.
type Process struct {
	Runtime  *Runtime
	Handlers HandlerBundle

	ctx             context.Context
	cancel          context.CancelFunc
	lifecycleMu     sync.Mutex
	closed          bool
	construction    toolHiveConstruction
	discovery       *authenticatedDiscovery
	protectedTarget *oauthRoute
	// occupied is the immutable model-visible name set outside this Process's
	// broker catalogue (core/global tools), captured once at construction so a
	// later workspace-enrollment freeze can reuse it without re-deriving it.
	occupied           []string
	queryAuthenticated func(context.Context, ToolHiveAuthSessionID, string) (AuthenticatedCapabilities, error)
	resources          []ownedResource
	closeOnce          sync.Once
	closeErr           error
}

type toolHiveProcessOptions struct {
	runtimeOptions   []Option
	brokerHTTPClient *http.Client
}

// NewToolHiveProcess discovers anonymous upstreams, constructs one ordered
// ToolHive process, and returns only after the Runtime and every owned resource
// are valid. Any partial construction is rolled back in reverse dependency order.
func NewToolHiveProcess(ctx context.Context, config ToolHiveConfig) (*Process, error) {
	return newToolHiveProcess(ctx, config, toolHiveProcessOptions{})
}

//nolint:gocyclo // Broker construction is one ordered admission transaction with reverse-order rollback.
func newToolHiveProcess(ctx context.Context, config ToolHiveConfig, options toolHiveProcessOptions) (*Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	issuer, err := toolHiveIssuer(config.CallbackURL, hasProtected(config.Profiles))
	if err != nil {
		return nil, err
	}
	construction, err := compileToolHiveConstruction(config.Profiles, issuer)
	if err != nil {
		return nil, err
	}
	routes, err := discoverAnonymous(ctx, construction.anonymous, config.Occupied)
	if err != nil {
		return nil, err
	}
	protectedTarget, err := newToolHiveProtectedTarget(issuer, config.CallbackURL, len(construction.upstreams) != 0)
	if err != nil {
		return nil, err
	}
	staticRoutes, err := compileStaticProtectedRoutes(construction, protectedTarget, routes, config.Occupied)
	if err != nil {
		return nil, err
	}
	routes = append(routes, staticRoutes...)
	sortRoutes(routes)
	catalogue := &Catalogue{routes: routes}
	caller := anonymousCaller(construction.anonymous)
	runtimeOptions := append([]Option(nil), options.runtimeOptions...)
	runtimeOptions = append(runtimeOptions, WithAuthorizedCaller(toolHiveProtectedCaller(issuer+"/mcp", options.brokerHTTPClient)))
	if protectedTarget != nil {
		// Every configured protected upstream may lack a static tool
		// declaration (workspace enrollment only), in which case the compiled
		// catalogue has no oauth route at all: force the hardened token client
		// into existence for the Process-owned target regardless.
		runtimeOptions = append(runtimeOptions, withHardenedTokenEndpoint(protectedTarget.tokenEndpoint))
	}
	runtime, err := New(catalogue, caller, runtimeOptions...)
	if err != nil {
		return nil, err
	}

	processCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	process := &Process{Runtime: runtime, ctx: processCtx, cancel: cancel, construction: construction, protectedTarget: protectedTarget, occupied: append([]string(nil), config.Occupied...)}
	runtime.process = process
	process.resources = append(process.resources, ownedResource{name: "process-context", close: func() error { cancel(); return nil }})
	rollback := func(cause error) (*Process, error) {
		process.rollback()
		return nil, cause
	}

	outgoing := vmcpauth.NewDefaultOutgoingAuthRegistry()
	if err := outgoing.RegisterStrategy(authtypes.StrategyTypeUnauthenticated, strategies.NewUnauthenticatedStrategy()); err != nil {
		return rollback(fmt.Errorf("mcpbroker: register anonymous strategy: %w", err))
	}
	if len(construction.upstreams) != 0 {
		if err := outgoing.RegisterStrategy("upstream_inject", strategies.NewUpstreamInjectStrategy()); err != nil {
			return rollback(fmt.Errorf("mcpbroker: register protected strategy: %w", err))
		}
	}

	var auth *runner.EmbeddedAuthServer
	var tokens upstreamCredentialReader
	var incoming func(http.Handler) http.Handler
	var authInfo http.Handler
	if len(construction.upstreams) != 0 {
		// A restart between a user starting an OAuth authorization and
		// completing it in their browser must not lose the pending-state
		// record. Composition supplies a Redis-backed store whenever the
		// operator already configured Redis for the session store
		// (ToolHiveConfig.AuthStorage); otherwise this falls back to the
		// in-memory default, which does not survive a process restart.
		authStore := config.AuthStorage
		if authStore == nil && config.AuthRedisClient != nil {
			authStore = storage.NewRedisStorageWithClient(config.AuthRedisClient, toolHiveAuthStoragePrefix)
		}
		if authStore == nil {
			authStore = storage.NewMemoryStorage()
		}
		if protectedTarget == nil {
			return rollback(fmt.Errorf("%w: protected ToolHive target is required", ErrInvalidCatalogue))
		}
		if err := authStore.RegisterClient(processCtx, &fosite.DefaultClient{
			ID: protectedTarget.clientID, RedirectURIs: []string{config.CallbackURL},
			GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"},
			Scopes: []string{"openid", "offline_access"}, Audience: []string{issuer}, Public: true,
		}); err != nil {
			return rollback(fmt.Errorf("mcpbroker: register embedded authorization client: %w", err))
		}
		auth, err = runner.NewEmbeddedAuthServerWithStorage(processCtx, &authserver.RunConfig{
			SchemaVersion: "v1", Issuer: issuer, AllowedAudiences: []string{issuer}, Upstreams: construction.upstreams,
		}, authStore)
		if err != nil {
			return rollback(fmt.Errorf("mcpbroker: create embedded auth server: %w", err))
		}
		process.resources = append(process.resources, ownedResource{name: "authserver", close: auth.Close})
		reader := upstreamtoken.NewInProcessService(auth.IDPTokenStorage(), auth.UpstreamTokenRefresher())
		tokens = reader
		incoming, _, authInfo, err = factory.NewIncomingAuthMiddleware(processCtx, &vmcpconfig.IncomingAuthConfig{
			Type: "oidc", OIDC: &vmcpconfig.OIDCConfig{Issuer: issuer, Audience: issuer, Resource: issuer, JWKSURL: issuer + "/.well-known/jwks.json"},
		}, "mecatl-broker", nil, reader, auth.KeyProvider())
		if err != nil {
			return rollback(fmt.Errorf("mcpbroker: create incoming auth: %w", err))
		}
	}

	backendClient, err := vmcpclient.NewHTTPBackendClient(outgoing)
	if err != nil {
		return rollback(fmt.Errorf("mcpbroker: create backend client: %w", err))
	}
	aggregationConfig := toolHiveAggregationConfig()
	resolver, err := aggregator.NewConflictResolver(aggregationConfig)
	if err != nil {
		return rollback(fmt.Errorf("mcpbroker: create conflict resolver: %w", err))
	}
	capabilityAggregator := aggregator.NewDefaultAggregator(backendClient, resolver, aggregationConfig, nil)
	serverConfig := &vmcpserver.Config{Name: "mecatl-broker", Version: "v1", EndpointPath: toolHiveMCPPath,
		AuthMiddleware: incoming, AuthInfoHandler: authInfo, AuthServer: auth,
		Aggregator: capabilityAggregator, SessionFactory: vmcpsession.NewSessionFactory(outgoing),
	}
	backendRegistry := vmcp.NewImmutableRegistry(construction.backends)
	server, err := vmcpserver.New(processCtx, serverConfig, router.NewSessionRouter(&vmcp.RoutingTable{}), backendClient, backendRegistry, nil)
	if err != nil {
		return rollback(fmt.Errorf("mcpbroker: create vMCP server: %w", err))
	}
	if len(construction.protectedBackends) != 0 {
		process.discovery = &authenticatedDiscovery{
			capabilities: capabilityAggregator,
			backends:     backendRegistry,
			tokens:       tokens,
			providers:    cloneProviderByBackend(construction.providerByBackend),
		}
	}
	process.resources = append(process.resources, ownedResource{name: "vmcp", close: func() error { return server.Stop(context.Background()) }})
	vmcpHandler, err := server.Handler(processCtx)
	if err != nil {
		return rollback(fmt.Errorf("mcpbroker: create vMCP handler: %w", err))
	}
	process.Handlers.VMCP = vmcpHandler
	if auth != nil {
		embedded := http.StripPrefix(toolHiveBasePath, auth.Handler())
		process.Handlers.Authorization = embedded
		process.Handlers.Token = embedded
		process.Handlers.UpstreamCallback = embedded
		process.Handlers.Discovery = embedded
		process.Handlers.JWKS = embedded
		process.Handlers.ProtectedResource = authInfo
	}
	if config.CallbackURL != "" {
		callbackHandlers, _, handlerErr := runtime.Handlers(config.CallbackURL)
		if handlerErr != nil {
			return rollback(handlerErr)
		}
		process.Handlers.Callback = callbackHandlers.Callback
	}
	return process, nil
}

func discoverAnonymous(ctx context.Context, profiles []ToolHiveProfile, occupied []string) ([]route, error) {
	configs := make([]mcpadapter.ServerConfig, len(profiles))
	for i, profile := range profiles {
		configs[i] = mcpadapter.ServerConfig{Name: profile.Name, URL: profile.URL}
	}
	definitions := make([]ToolDefinition, 0)
	if len(configs) != 0 {
		manager, err := mcpadapter.NewManager(ctx, configs, nil, nil)
		if err != nil {
			return nil, fmt.Errorf("mcpbroker: discover anonymous upstreams: %w", err)
		}
		defer func() { _ = manager.Close() }()
		for _, wrapped := range manager.Tools() {
			spec := wrapped.Spec()
			for _, profile := range profiles {
				if strings.HasPrefix(spec.Name, "mcp__"+profile.Name+"__") {
					definitions = append(definitions, ToolDefinition{Backend: profile.Name, Name: spec.Name, Description: spec.Description, Schema: spec.Schema, ReadOnly: wrapped.ReadOnly()})
					break
				}
			}
		}
	}
	declarations := make([]ToolHiveProfile, len(profiles))
	copy(declarations, profiles)
	backends := make(map[string]ToolHiveProfile, len(declarations))
	for _, profile := range declarations {
		backends[strings.ToLower(profile.Name)] = profile
	}
	seen := make(map[string]struct{}, len(occupied)+len(definitions))
	for _, name := range occupied {
		if name == "" {
			return nil, fmt.Errorf("%w: occupied tool name is empty", ErrInvalidCatalogue)
		}
		seen[name] = struct{}{}
	}
	routes := make([]route, 0, len(definitions))
	for _, definition := range definitions {
		profile, ok := backends[strings.ToLower(definition.Backend)]
		if !ok {
			return nil, fmt.Errorf("%w: discovery references missing upstream %q", ErrInvalidCatalogue, definition.Backend)
		}
		if _, duplicate := seen[definition.Name]; duplicate {
			return nil, fmt.Errorf("%w: model-visible tool name collision %q", ErrInvalidCatalogue, definition.Name)
		}
		seen[definition.Name] = struct{}{}
		routes = append(routes, route{backend: profile.Name, spec: tool.ToolSpec{Name: definition.Name, Description: definition.Description, Schema: append([]byte(nil), definition.Schema...)}, readOnly: definition.ReadOnly})
	}
	sortRoutes(routes)
	return routes, nil
}

func compileStaticProtectedRoutes(construction toolHiveConstruction, protectedTarget *oauthRoute, base []route, occupied []string) ([]route, error) {
	seen := make(map[string]struct{}, len(occupied)+len(base))
	for _, name := range occupied {
		if name == "" {
			return nil, fmt.Errorf("%w: occupied tool name is empty", ErrInvalidCatalogue)
		}
		seen[name] = struct{}{}
	}
	for _, route := range base {
		seen[route.spec.Name] = struct{}{}
	}

	routes := make([]route, 0)
	for _, backend := range construction.backends {
		declaredTools := construction.staticByBackend[backend.ID]
		if len(declaredTools) == 0 {
			continue
		}
		if protectedTarget == nil {
			return nil, fmt.Errorf("%w: static protected tools require the ToolHive authorization target", ErrInvalidCatalogue)
		}
		for _, declared := range declaredTools {
			name := "mcp__" + backend.ID + "__" + declared.Name
			candidate, err := validateAuthenticatedRoute(backend.ID, ToolDefinition{
				Backend: backend.ID, Name: name, Description: declared.Description,
				Schema: append([]byte(nil), declared.Schema...), ReadOnly: declared.ReadOnly,
			}, seen)
			if err != nil {
				return nil, fmt.Errorf("%w: static tool declaration %q", err, name)
			}
			candidate.oauth = protectedTarget
			seen[name] = struct{}{}
			routes = append(routes, candidate)
		}
	}
	return routes, nil
}

func toolHiveAggregationConfig() *vmcpconfig.AggregationConfig {
	return &vmcpconfig.AggregationConfig{
		ConflictResolution: vmcp.ConflictStrategyPrefix,
		ConflictResolutionConfig: &vmcpconfig.ConflictResolutionConfig{
			PrefixFormat: "{workload}.",
		},
	}
}

func toolHiveAdvertisedToolName(backend, modelVisibleName string) (string, error) {
	toolName, ok := strings.CutPrefix(modelVisibleName, "mcp__"+backend+"__")
	if backend == "" || !ok || toolName == "" {
		return "", fmt.Errorf("%w: tool %q does not belong to backend %q", ErrInvalidCatalogue, modelVisibleName, backend)
	}
	return backend + "." + toolName, nil
}

func newToolHiveProtectedTarget(issuer, callbackURL string, required bool) (*oauthRoute, error) {
	if !required {
		return nil, nil
	}
	clientID, err := opaque(rand.Read)
	if err != nil {
		return nil, fmt.Errorf("%w: create ToolHive authorization client: %v", ErrInvalidCatalogue, err)
	}
	return &oauthRoute{
		authorizationEndpoint: issuer + "/oauth/authorize",
		tokenEndpoint:         issuer + "/oauth/token",
		callbackURL:           callbackURL,
		clientID:              clientID,
		scopes:                []string{"openid", "offline_access"},
		requestRefresh:        true,
	}, nil
}

func toolHiveProtectedCaller(endpoint string, client *http.Client) AuthorizedCaller {
	return func(ctx context.Context, _ SessionRef, backend string, call session.ToolCall, tokens oauth2.TokenSource) (session.ToolResult, error) {
		if endpoint == "" || backend == "" {
			return session.ToolResult{}, fmt.Errorf("%w: protected ToolHive target is not configured", ErrInvalidCatalogue)
		}
		if tokens == nil {
			return session.ToolResult{}, fmt.Errorf("%w: protected upstream token source is required", ErrInvalidCatalogue)
		}
		advertisedName, err := toolHiveAdvertisedToolName(backend, call.Name)
		if err != nil {
			return session.ToolResult{}, err
		}
		wrappedName := "mcp__broker__" + advertisedName
		config := mcpadapter.ServerConfig{Name: "broker", URL: endpoint, TokenSource: tokens, HTTPClient: client}
		upstream, err := mcpadapter.Connect(ctx, config, nil)
		if err != nil {
			return session.ToolResult{}, fmt.Errorf("mcpbroker: connect protected ToolHive target: %w", err)
		}
		defer func() { _ = upstream.Close() }()
		for _, wrapped := range upstream.Tools() {
			if wrapped.Spec().Name != wrappedName {
				continue
			}
			forwarded := call
			forwarded.Name = wrapped.Spec().Name
			return wrapped.Execute(ctx, forwarded, tool.Environment{})
		}
		return session.ToolResult{}, fmt.Errorf("mcpbroker: protected ToolHive target omitted tool %q", call.Name)
	}
}

func anonymousCaller(profiles []ToolHiveProfile) Caller {
	servers := make(map[string]mcpadapter.ServerConfig, len(profiles))
	for _, profile := range profiles {
		servers[profile.Name] = mcpadapter.ServerConfig{Name: profile.Name, URL: profile.URL}
	}
	return func(ctx context.Context, _ SessionRef, backend string, call session.ToolCall) (session.ToolResult, error) {
		config, ok := servers[backend]
		if !ok {
			return session.ToolResult{}, fmt.Errorf("%w: anonymous upstream is not configured", ErrInvalidCatalogue)
		}
		upstream, err := mcpadapter.Connect(ctx, config, nil)
		if err != nil {
			return session.ToolResult{}, fmt.Errorf("mcpbroker: connect anonymous upstream: %w", err)
		}
		defer func() { _ = upstream.Close() }()
		for _, wrapped := range upstream.Tools() {
			if wrapped.Spec().Name == call.Name {
				return wrapped.Execute(ctx, call, tool.Environment{})
			}
		}
		return session.ToolResult{}, fmt.Errorf("mcpbroker: anonymous upstream omitted tool %q", call.Name)
	}
}

func sortRoutes(routes []route) {
	slices.SortFunc(routes, func(a, b route) int { return strings.Compare(a.spec.Name, b.spec.Name) })
}

func toolHiveIssuer(callbackURL string, protected bool) (string, error) {
	if !protected {
		return "http://mecatl.invalid" + toolHiveBasePath, nil
	}
	parsed, err := url.Parse(callbackURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path == "" || parsed.String() != callbackURL {
		return "", fmt.Errorf("%w: protected upstreams require a canonical HTTPS callback URL", ErrInvalidCatalogue)
	}
	return parsed.Scheme + "://" + parsed.Host + toolHiveBasePath, nil
}

func hasProtected(profiles []ToolHiveProfile) bool {
	for _, profile := range profiles {
		if profile.Auth == authOAuth {
			return true
		}
	}
	return false
}

func (p *Process) rollback() {
	if p.Runtime != nil {
		_ = p.Runtime.Close()
	}
	_ = p.closeResources()
}

func (p *Process) closeResources() error {
	var result error
	for i := len(p.resources) - 1; i >= 0; i-- {
		result = errors.Join(result, p.resources[i].close())
	}
	return result
}

// WorkspaceEnrollmentRequired reports whether at least one configured
// protected upstream has no trusted static tool declaration, so its complete
// tool catalogue can only be learned by authenticating first and then running
// live authenticated discovery (workspace enrollment). Composition uses this
// to decide whether to advertise the enrollment capability.
func (p *Process) WorkspaceEnrollmentRequired() bool {
	return p != nil && len(p.construction.protectedBackends) > 0
}

// Close first cancels process-owned work, then drains the neutral Runtime,
// stops vMCP, and closes authserver. It is idempotent.
func (p *Process) Close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		p.lifecycleMu.Lock()
		p.closed = true
		cancel := p.cancel
		p.lifecycleMu.Unlock()
		if cancel != nil {
			cancel()
		}
		if p.Runtime != nil {
			p.closeErr = p.Runtime.Close()
		}
		p.closeErr = errors.Join(p.closeErr, p.closeResources())
	})
	return p.closeErr
}
