package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/authserverclient"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/brokerconfig"
)

// identityBrokerWorkload is the workload name the identity broker records until
// it authenticates the workload's credential, which will be addressed later.
// The name, and Workload.Verified being false, keep it from reading as a
// verified identity.
const identityBrokerWorkload = "mecak8s-unverified"

// errNoIdentityBrokerConfigFile refuses the identity broker without its
// configuration file, which holds its keys.
var errNoIdentityBrokerConfigFile = errors.New("the identity broker requires its configuration file (--identity-broker-config), which names its agent trust domain, audience, and keys")

// errIdentityBrokerConfigFileWithoutBroker refuses a configuration file for an
// identity broker that is not enabled.
var errIdentityBrokerConfigFileWithoutBroker = errors.New("the identity broker's configuration file (--identity-broker-config) requires the identity broker (--enable-identity-broker)")

// errIdentityBrokerWithShell refuses the identity broker while sessions can run
// a shell in this process: a model-driven shell could read the broker's key
// files, or its keys from the process's memory.
var errIdentityBrokerWithShell = errors.New("the identity broker's keys are files in this process's filesystem and keys in its memory, so it cannot run while sessions have a shell in this process; use --no-shell, or --shell \"\" with Kubernetes execution to keep the remote Shell")

// errIdentityBrokerWithLocalWorkspace refuses the identity broker while
// sessions' file tools reach this process's filesystem, where its key files
// are: out-of-root reads are allowed at some permission modes.
//
// Shortcut: the identity broker's keys are files in this process's filesystem, so it refuses a local workspace; removed by #2807 (the broker runs as its own service).
var errIdentityBrokerWithLocalWorkspace = errors.New("the identity broker's keys are files in this process's filesystem, so it cannot run while sessions have a local workspace (--workspace); run without a workspace, with --redis-filesystem, or with Kubernetes execution")

// IdentityBrokerAS1Config hosts AS-1, the identity broker's sign-in
// authorization server, in this process. It requires Config.IdentityBroker.
// AS-1 keeps pending sign-ins and refresh tokens in memory.
type IdentityBrokerAS1Config struct {
	// Issuer is AS-1's https issuer URL; its routes are served under its path.
	Issuer string
	// Audience is the Mecatl API audience AS-1 issues access tokens for.
	Audience string
	// PublicClients are the sign-in clients AS-1 registers: no secret, PKCE,
	// and tokens for Audience. At least one is required.
	PublicClients []IdentityBrokerAS1PublicClient
	// UpstreamIssuer, UpstreamClientID, and UpstreamClientSecretFile are
	// AS-1's registration at the IdP users sign in at. An empty secret file
	// makes AS-1 a public client there.
	UpstreamIssuer           string
	UpstreamClientID         string
	UpstreamClientSecretFile string
	// SubjectClaim is the IdP ID-token claim naming the company identity.
	// Empty selects "sub".
	SubjectClaim string
	// UpstreamCAFile is a PEM bundle trusted for the IdP's TLS certificate in
	// addition to the system roots. Empty trusts the system roots only.
	UpstreamCAFile string
	// UpstreamAllowPrivateAddresses lets AS-1 reach the IdP on a private or
	// link-local address, which its SSRF protection otherwise refuses.
	UpstreamAllowPrivateAddresses bool
}

// IdentityBrokerAS1PublicClient is a public sign-in client of AS-1.
type IdentityBrokerAS1PublicClient struct {
	// ID is the client ID, unique among the configured clients.
	ID string
	// RedirectURIs are the client's absolute redirect URIs, matched exactly.
	RedirectURIs []string
}

// IdentityBrokerAS1Server is a hosted AS-1 as a command root serves it.
type IdentityBrokerAS1Server struct {
	// Issuer is AS-1's issuer URL.
	Issuer string
	// Routes are the exact paths Handler serves, to mount on the listener
	// that serves Issuer after checking they collide with no other route.
	Routes []string
	// Handler serves Routes and nothing else.
	Handler http.Handler
	// HTTPClient reaches AS-1 without a listener, so the process's own token
	// validator can fetch its discovery document and keys before listeners
	// start.
	HTTPClient *http.Client
}

// errIdentityBrokerAS1WithoutBroker refuses a hosted AS-1 the identity broker
// would not be running beside.
var errIdentityBrokerAS1WithoutBroker = errors.New("hosting AS-1 requires the identity broker")

// as1RequestTimeout bounds one session-open token exchange the broker makes at
// a hosted AS-1 in process. Session open holds the host session's open lock
// while it makes at most two requests, the exchange and one resend, so this
// bounds how long that lock is held. AS-1 answers in process, normally within
// milliseconds.
const as1RequestTimeout = 10 * time.Second

// as2RequestTimeout bounds one call the identity broker makes at AS-2, which
// answers in process like a hosted AS-1.
const as2RequestTimeout = as1RequestTimeout

// identityBrokerAS2 is the embedded MCP gateway's authorization server as the
// identity broker reaches it as AS-2. Startup records it when the identity
// broker has a broker client key and the bundled MCP broker has a protected
// route, then binds it as the identity broker's gateway AS-2 when the broker
// has a gateway.
type identityBrokerAS2 struct {
	// Issuer is AS-2's issuer: the client-assertion audience and an ID-JAG's
	// aud.
	Issuer string
	// TokenEndpoint is AS-2's token endpoint.
	TokenEndpoint string
	// Resource is the gateway's vMCP resource, an ID-JAG's resource.
	Resource string
	// HTTPClient serves AS-2's token endpoint in process, each call bounded
	// by as2RequestTimeout.
	HTTPClient *http.Client
}

// gatewayIDJAGTarget is the AS-2 a hosted AS-1 issues the gateway's ID-JAGs
// for: the issuer and vMCP resource the bundled MCP broker gives AS-2, derived
// before either is built.
type gatewayIDJAGTarget struct {
	Issuer, Resource string
}

// errAS2IssuerIsAS1s refuses an AS-2 that would share AS-1's issuer.
var errAS2IssuerIsAS1s = errors.New("the MCP gateway's authorization server (AS-2) would have the same issuer as AS-1")

// errAS2IsNotIDJAGTarget refuses an AS-2 other than the one AS-1 issues the
// gateway's ID-JAGs for.
var errAS2IsNotIDJAGTarget = errors.New("the MCP gateway's authorization server (AS-2) is not the one AS-1 issues ID-JAGs for")

// errGatewayIsNotAS2Resource refuses gateway calls sent to an endpoint other
// than the resource AS-2 issues their tokens for.
var errGatewayIsNotAS2Resource = errors.New("the MCP gateway's endpoint is not the resource its authorization server (AS-2) issues tokens for")

// sameIssuer reports whether two issuer URLs name the same server: scheme and
// host compare case-insensitively, a default port equals none, and a trailing
// slash is ignored. Unparseable URLs compare byte for byte.
func sameIssuer(a, b string) bool {
	if a == b {
		return true
	}
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	if errA != nil || errB != nil {
		return false
	}
	return strings.EqualFold(ua.Scheme, ub.Scheme) &&
		strings.EqualFold(ua.Hostname(), ub.Hostname()) &&
		effectivePort(ua) == effectivePort(ub) &&
		strings.TrimSuffix(ua.EscapedPath(), "/") == strings.TrimSuffix(ub.EscapedPath(), "/")
}

// effectivePort is u's port, or its scheme's default when it names none.
func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return "443"
	case "http":
		return "80"
	}
	return ""
}

// hostedAS1IDJAGMaxLifetime caps the broker refresh tokens the hosted AS-1
// issues at session open, and so a broker session's hard expiry. Twelve hours
// covers a working session while bounding unattended authority; #2813 makes
// it configurable.
const hostedAS1IDJAGMaxLifetime = 12 * time.Hour

// identityBrokerParts is the identity broker as startup builds it.
type identityBrokerParts struct {
	// service is the boundary Mecatl calls; Mecatl never gets the core.
	service identitybroker.Service
	// hosted is the hosted AS-1; nil when none is configured.
	hosted *IdentityBrokerAS1Server
	// hostedServer is the hosted AS-1 itself, for tests; nil when none is
	// configured.
	hostedServer *as1server.Server
	// client signs the broker's client assertions; nil for an injected core
	// or without a broker client key.
	client *identitybroker.ClientAssertionSigner
	// clientKeySetFile is the broker client's public key set file, which
	// AS-2 registers; empty without a broker client key.
	clientKeySetFile string
	// core is the real broker; nil for an injected test core.
	core *identitybroker.Broker
	// recorder holds the core's decision and ID-JAG records; nil for an
	// injected core.
	recorder *identitybroker.MemoryRecorder
	// gateway reports whether the core has a gateway, whose AS-2 bindAS2
	// binds.
	gateway bool
	// gatewayServers are the servers the gateway routes; set only with
	// gateway.
	gatewayServers []string
	// release releases the broker and AS-1.
	release func()
}

// bindAS2 binds as2 as the core's gateway AS-2, authenticating there with
// the broker client's assertions; unbind waits for calls using AS-2 to
// finish. It is a no-op returning a no-op unbind when the core has no
// gateway, whose calls are then denied, or when as2 is nil, so the gateway's
// calls are unavailable.
func (p identityBrokerParts) bindAS2(as2 *identityBrokerAS2) (unbind func(), err error) {
	if p.core == nil || !p.gateway || as2 == nil {
		return func() {}, nil
	}
	as2Client, err := authserverclient.New(authserverclient.Config{
		Issuer: as2.Issuer, TokenEndpoint: as2.TokenEndpoint, Assertions: p.client, HTTPClient: as2.HTTPClient,
	})
	if err != nil {
		return nil, fmt.Errorf("AS-2 client: %w", err)
	}
	unbind, err = p.core.BindGatewayAS2(identitybroker.GatewayAS2{Issuer: as2.Issuer, Resource: as2.Resource, AuthServer: as2Client})
	if err != nil {
		return nil, fmt.Errorf("bind the identity broker's AS-2: %w", err)
	}
	return unbind, nil
}

// buildIdentityBroker returns the identity broker's parts, all nil or no-ops
// when the broker is not enabled. Mecatl gets only the authentication
// boundary, never the core behind it. A construction failure fails startup,
// as does a configuration that gives sessions a shell.
//
// The real broker starts from its configuration file and the operator keys it
// names; no key is generated. With a hosted AS-1 and no test override, session
// open converts users' access tokens at that AS-1 in process:
// buildBrokerWithHostedAS1 registers the file's broker client key set there
// for the confidential delegate, which may obtain ID-JAGs for target when it
// is set.
func buildIdentityBroker(ctx context.Context, cfg Config, target *gatewayIDJAGTarget) (identityBrokerParts, error) {
	if !cfg.IdentityBroker {
		switch {
		case cfg.IdentityBrokerHostedAS1 != nil:
			return identityBrokerParts{}, errIdentityBrokerAS1WithoutBroker
		case cfg.IdentityBrokerConfigFile != "":
			return identityBrokerParts{}, errIdentityBrokerConfigFileWithoutBroker
		}
		return identityBrokerParts{release: func() {}}, nil
	}
	if shellAvailable(cfg) {
		return identityBrokerParts{}, errIdentityBrokerWithShell
	}
	if localWorkspace(cfg) {
		return identityBrokerParts{}, errIdentityBrokerWithLocalWorkspace
	}
	core := cfg.identityBrokerCore
	// An injected test core needs no file; its hosted AS-1 generates its
	// signing key.
	var file brokerconfig.File
	if core == nil {
		if cfg.IdentityBrokerConfigFile == "" {
			return identityBrokerParts{}, errNoIdentityBrokerConfigFile
		}
		var err error
		if file, err = brokerconfig.Load(cfg.IdentityBrokerConfigFile, brokerconfig.Host{AS1: cfg.IdentityBrokerHostedAS1 != nil}); err != nil {
			return identityBrokerParts{}, err
		}
	}
	// Production wiring: host AS-1 with the broker client as the delegate,
	// then point the broker's session open at that AS-1. A test that replaces
	// the core or sets IdentityBrokerAS1 explicitly keeps the paths below.
	if core == nil && cfg.IdentityBrokerAS1 == nil && cfg.IdentityBrokerHostedAS1 != nil {
		return buildBrokerWithHostedAS1(ctx, cfg, file, target)
	}
	var parts identityBrokerParts
	if core == nil {
		broker, err := newIdentityBroker(cfg, file)
		if err != nil {
			return identityBrokerParts{}, fmt.Errorf("build identity broker: %w", err)
		}
		core = broker.core
		parts = broker.parts()
	}
	parts.release = func() {}
	if closer, ok := core.(io.Closer); ok {
		parts.release = func() { _ = closer.Close() }
	}
	if as1 := cfg.IdentityBrokerHostedAS1; as1 != nil {
		server, err := buildHostedAS1(ctx, as1, file, nil)
		if err != nil {
			parts.release()
			return identityBrokerParts{}, err
		}
		parts.hosted = newHostedAS1Server(server)
		parts.release = releaseServerThen(server, parts.release)
	}
	parts.service = identitybroker.NewInProcess(identitybroker.UnverifiedAuthenticator{Name: identityBrokerWorkload}, core)
	return parts, nil
}

// localWorkspace reports whether sessions' file tools can reach this
// process's own filesystem: a workspace served by the local placement, alone
// or as remote execution's local fallback. The Redis filesystem excludes a
// workspace, and another placement, such as a microVM, serves it elsewhere.
func localWorkspace(cfg Config) bool {
	return cfg.Workspace != "" && (cfg.PlacementProvider == nil || cfg.RemoteExecution)
}

// buildBrokerWithHostedAS1 is the production path when the broker and a hosted
// AS-1 are both enabled: it hosts AS-1 with the file's broker client key set
// registered for the confidential delegate client
// identitybroker.BrokerClientID, and builds the broker with its session open
// pointed at that AS-1 in process. With target, the delegate also obtains
// per-call ID-JAGs for it there, whose actor tokens AS-1 checks with the
// broker's CredentialVerifier.
func buildBrokerWithHostedAS1(ctx context.Context, cfg Config, file brokerconfig.File, target *gatewayIDJAGTarget) (identityBrokerParts, error) {
	as1 := cfg.IdentityBrokerHostedAS1
	sources := make([]string, len(as1.PublicClients))
	for i, c := range as1.PublicClients {
		sources[i] = c.ID
	}
	delegate := &as1server.DelegateClient{
		ID:                   identitybroker.BrokerClientID,
		KeySetFile:           file.BrokerClientKeySetFile,
		AllowedSourceClients: sources,
		IDJAGMaxLifetime:     hostedAS1IDJAGMaxLifetime,
	}
	if target != nil {
		// Shortcut: AS-1 issues ID-JAGs only for the AS-2 the test-only gateway servers configure, so production configures no target and no actor profile; removed by #2533 and #2538.
		delegate.IDJAG = &as1server.IDJAGIssuance{
			ActIssuer: identitybroker.ActorIssuer(identityBrokerConfig(cfg, file).TrustDomain),
			// Lifetime stays zero, ToolHive's default of a minute.
			Targets: []as1server.IDJAGTarget{{Issuer: target.Issuer, ClientID: identitybroker.BrokerClientID, Resources: []string{target.Resource}}},
		}
	}
	server, err := buildHostedAS1(ctx, as1, file, delegate)
	if err != nil {
		return identityBrokerParts{}, err
	}
	httpClient := server.InProcessClient()
	httpClient.Timeout = as1RequestTimeout
	// Now and AssertionNow stay nil: production uses time.Now for the broker
	// and its assertions.
	cfg.IdentityBrokerAS1 = &IdentityBrokerAS1{
		Issuer: server.Issuer(), TokenEndpoint: server.TokenEndpoint(), HTTPClient: httpClient,
	}
	if delegate.IDJAG != nil {
		cfg.IdentityBrokerAS1.ActorTokens = server
	}
	broker, err := newIdentityBroker(cfg, file)
	if err != nil {
		_ = server.Close()
		return identityBrokerParts{}, fmt.Errorf("build identity broker: %w", err)
	}
	var core identitybroker.Core = broker.core
	parts := broker.parts()
	parts.release = releaseServerThen(server, func() {})
	if closer, ok := core.(io.Closer); ok {
		parts.release = releaseServerThen(server, func() { _ = closer.Close() })
	}
	parts.service = identitybroker.NewInProcess(identitybroker.UnverifiedAuthenticator{Name: identityBrokerWorkload}, core)
	parts.hosted = newHostedAS1Server(server)
	parts.hostedServer = server
	return parts, nil
}

// buildHostedAS1 constructs the hosted AS-1 from as1, signing with file's
// AS-1 signing key and publishing its fallbacks, and optionally registering
// delegate as its confidential delegate client.
func buildHostedAS1(ctx context.Context, as1 *IdentityBrokerAS1Config, file brokerconfig.File, delegate *as1server.DelegateClient) (*as1server.Server, error) {
	clients := make([]as1server.PublicClient, len(as1.PublicClients))
	for i, c := range as1.PublicClients {
		clients[i] = as1server.PublicClient{ID: c.ID, RedirectURIs: c.RedirectURIs}
	}
	server, err := as1server.New(ctx, as1server.Config{
		Issuer: as1.Issuer, Audience: as1.Audience,
		SigningKeyFile: file.AS1SigningKeyFile, FallbackKeyFiles: file.AS1FallbackKeyFiles,
		PublicClients: clients, Delegate: delegate,
		Upstream: as1server.UpstreamConfig{
			Issuer: as1.UpstreamIssuer, ClientID: as1.UpstreamClientID,
			ClientSecretFile: as1.UpstreamClientSecretFile, SubjectClaim: as1.SubjectClaim,
			CAFile: as1.UpstreamCAFile, AllowPrivateAddresses: as1.UpstreamAllowPrivateAddresses,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("build AS-1: %w", err)
	}
	return server, nil
}

// newHostedAS1Server is the command-root view of server: its issuer, routes,
// handler, and a fresh in-process client for the token validator.
func newHostedAS1Server(server *as1server.Server) *IdentityBrokerAS1Server {
	return &IdentityBrokerAS1Server{Issuer: server.Issuer(), Routes: server.Routes(), Handler: server.Handler(), HTTPClient: server.InProcessClient()}
}

// releaseServerThen closes server before running next.
func releaseServerThen(server *as1server.Server, next func()) func() {
	return func() {
		_ = server.Close()
		next()
	}
}

// identityBroker is the real identity broker's startup state: its core, the
// signer for its client authentication at AS-1 and AS-2, its records, and
// whether it has a gateway. With an AS-1 configured, session open
// authenticates there with the signer; without one, which is a deployment
// that hosts no AS-1, nothing calls AS-1 or AS-2.
type identityBroker struct {
	core   *identitybroker.Broker
	client *identitybroker.ClientAssertionSigner
	// clientKeySetFile is the broker client's public key set file.
	clientKeySetFile string
	recorder         *identitybroker.MemoryRecorder
	gateway          bool
	// gatewayServers are the servers the gateway routes.
	gatewayServers []string
}

// parts returns the identity broker's parts that b holds; the caller adds
// the service, the hosted AS-1, and release.
func (b identityBroker) parts() identityBrokerParts {
	return identityBrokerParts{client: b.client, clientKeySetFile: b.clientKeySetFile, core: b.core, recorder: b.recorder, gateway: b.gateway, gatewayServers: b.gatewayServers}
}

// IdentityBrokerAS1 is the AS-1 the identity broker converts access tokens
// at during session open.
type IdentityBrokerAS1 struct {
	Issuer        string
	TokenEndpoint string
	HTTPClient    *http.Client
	// Now replaces the broker's clock, so a test can move past a broker
	// session's hard expiry. It does not date client assertions. Nil selects
	// time.Now.
	Now func() time.Time
	// AssertionNow replaces the clock that dates the broker's client
	// assertions at AS-1, for a test whose AS-1 checks them against a moved
	// clock. Nil selects time.Now.
	AssertionNow func() time.Time
	// ActorTokens is AS-1's actor-token check, bound to the broker's
	// CredentialVerifier once the broker is built; nil when AS-1 issues no
	// ID-JAGs. It applies only to an AS-1 running in this process, which can
	// call the broker. A failed bind fails startup.
	ActorTokens interface {
		UseCredentialVerifier(identitybroker.CredentialVerifier) error
	}
}

// identityBrokerConfig is the identity broker's configuration: its
// configuration file's, or a test's replacement.
func identityBrokerConfig(cfg Config, file brokerconfig.File) identitybroker.Config {
	if cfg.identityBrokerConfig != nil {
		return *cfg.identityBrokerConfig
	}
	return file.Config
}

// errNoBrokerClientKey refuses an AS-1 the broker has no client key for.
var errNoBrokerClientKey = errors.New("the identity broker's configuration file names no broker client key, which it authenticates with at AS-1")

// newIdentityBroker constructs the real identity broker from its
// configuration file's trust domain, audience, and keys, one per role: the
// agent issuer key, with its fallbacks, and the broker client key. It loads
// no definitions yet. With cfg.IdentityBrokerAS1, session open converts
// access tokens at that AS-1, authenticating with the broker client key, and
// calls get their ID-JAGs there; a gateway is configured only then, and only
// when a test sets cfg.IdentityBrokerGatewayServers.
func newIdentityBroker(cfg Config, file brokerconfig.File) (identityBroker, error) {
	config := identityBrokerConfig(cfg, file)
	as1 := cfg.IdentityBrokerAS1
	var client *identitybroker.ClientAssertionSigner
	if file.Keys.BrokerClient != nil {
		var err error
		if client, err = identitybroker.NewClientAssertionSigner(file.Keys.BrokerClient, 0); err != nil {
			return identityBroker{}, err
		}
	}
	// Records are kept in memory (the shortcut on identitybroker.MemoryRecorder).
	recorder := identitybroker.NewMemoryRecorder()
	opts := []identitybroker.Option{identitybroker.WithRecorder(recorder), identitybroker.WithAgentIssuerFallbacks(file.Keys.AgentIssuerFallbacks...)}
	gateway := false
	var gatewayServers []string
	if as1 != nil {
		if client == nil {
			return identityBroker{}, errNoBrokerClientKey
		}
		as1Client, err := authserverclient.New(authserverclient.Config{
			Issuer: as1.Issuer, TokenEndpoint: as1.TokenEndpoint, Assertions: client, HTTPClient: as1.HTTPClient, Now: as1.AssertionNow,
		})
		if err != nil {
			return identityBroker{}, fmt.Errorf("AS-1 client: %w", err)
		}
		opts = append(opts, identitybroker.WithAuthServer(as1Client), identitybroker.WithIDJAGIssuer(as1Client))
		if as1.Now != nil {
			opts = append(opts, identitybroker.WithClock(as1.Now))
		}
		// A gateway needs AS-1 to issue its ID-JAGs, so without an AS-1 there
		// is none.
		// Shortcut: production configures no gateway entry, so gateway calls are refused as not configured; removed by #2533 (AS-2 trusts AS-1 in production, gateway from configuration).
		if cfg.IdentityBrokerGatewayServers != nil {
			opts = append(opts, identitybroker.WithGateway(identitybroker.Gateway{Servers: cfg.IdentityBrokerGatewayServers}))
			gateway, gatewayServers = true, append([]string(nil), cfg.IdentityBrokerGatewayServers...)
		}
	}
	core, err := identitybroker.New(config, nil, file.Keys.AgentIssuer, opts...)
	if err != nil {
		return identityBroker{}, err
	}
	if as1 != nil && as1.ActorTokens != nil {
		if err := as1.ActorTokens.UseCredentialVerifier(core); err != nil {
			return identityBroker{}, fmt.Errorf("bind AS-1's actor-token check: %w", err)
		}
	}
	return identityBroker{
		core: core, client: client, clientKeySetFile: file.BrokerClientKeySetFile, recorder: recorder,
		gateway: gateway, gatewayServers: gatewayServers,
	}, nil
}
