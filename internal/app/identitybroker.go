package app

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/buildinfo"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/authserverclient"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/brokerconfig"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/definitionloader"
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
// are: out-of-root reads are allowed at some permission modes. The command
// flags and the Helm chart state the same rule and alternatives.
//
// Shortcut: the identity broker refuses a local workspace; removed by #2807 (the broker runs as its own service).
var errIdentityBrokerWithLocalWorkspace = errors.New("the identity broker cannot run with a workspace on this process's filesystem (--workspace): its keys are files there, which sessions' file tools could read; run without a workspace for file-less sessions, or use --redis-filesystem or Kubernetes execution (--execution-enabled) instead")

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
	// by the configuration file's AS-2 request timeout.
	HTTPClient *http.Client
}

// embeddedGateway is the embedded gateway the bundled MCP broker runs, as an
// embedded gateway entry must describe it: AS-2's issuer, token endpoint, and
// vMCP resource, derived from mcp.broker.callback_url before either broker is
// built, and the OAuth routes it can route.
type embeddedGateway struct {
	Issuer, TokenEndpoint, Resource string
	// Routes are the names of the MCP broker's OAuth routes.
	Routes []string
}

// as2SigningKey is the embedded gateway's signing key and its fallbacks
// (mcp.broker.signing_key_file and fallback_key_files). The identity broker
// never holds it; startup only checks it is not one of the broker's keys.
type as2SigningKey struct {
	file      string
	fallbacks []string
}

// errEmbeddedEntryWithoutEmbeddedGateway refuses an embedded gateway entry in
// a process that runs no embedded gateway.
var errEmbeddedEntryWithoutEmbeddedGateway = errors.New("the identity broker's configuration file declares an embedded gateway, but this process runs none: the embedded gateway needs the bundled MCP broker (mcp.mode: broker) with an OAuth server and an https mcp.broker.callback_url")

// errEmbeddedEntryDisagrees refuses an embedded gateway entry whose AS-2
// differs from the embedded gateway's.
var errEmbeddedEntryDisagrees = errors.New("the identity broker's embedded gateway entry disagrees with the embedded gateway this process runs")

// errEmbeddedEntryServerIsNotARoute refuses an embedded gateway entry routing
// a server the MCP broker does not route through its embedded gateway.
var errEmbeddedEntryServerIsNotARoute = errors.New("the identity broker's embedded gateway entry routes a server that is not one of the MCP broker's OAuth servers (mcp.servers with auth.mode: oauth; names match exactly)")

// errGatewayServersBeforeAS2TrustsAS1 refuses an entry routing servers while
// AS-2 trusts no ID-JAG issuer, which would refuse every call's ID-JAG.
//
// Shortcut: production's gateway entry routes no servers; removed by #2533 (AS-2 trusts AS-1).
var errGatewayServersBeforeAS2TrustsAS1 = errors.New("the identity broker's gateway entry routes servers, but the embedded gateway's authorization server (AS-2) does not trust AS-1's ID-JAGs yet (#2533); list no servers until it does")

// errGatewayServersWithoutAS1 refuses an entry routing servers without the
// AS-1 that issues their calls' ID-JAGs.
var errGatewayServersWithoutAS1 = errors.New("the identity broker's gateway entry routes servers, which requires AS-1 (--identity-broker-as1-issuer) to issue their calls' ID-JAGs")

// errNoAS2SigningKey refuses an embedded gateway entry without the embedded
// gateway's signing key.
var errNoAS2SigningKey = errors.New("the identity broker's embedded gateway entry requires the embedded gateway's signing key (mcp.broker.signing_key_file)")

// errAS2SigningKeyWithoutEntry refuses the embedded gateway's signing key
// without the identity broker and its embedded gateway entry.
//
// Shortcut: the key is allowed only with the identity broker on; removed by #2869 (the embedded gateway's keys survive a restart with the identity broker off).
var errAS2SigningKeyWithoutEntry = errors.New("mcp.broker.signing_key_file requires the identity broker (--enable-identity-broker) with an embedded gateway entry in its configuration file")

// errAS2SigningKeyIsABrokerKey refuses an embedded gateway signing key that
// is also one of the identity broker's keys.
var errAS2SigningKeyIsABrokerKey = errors.New("the embedded gateway's signing key (mcp.broker.signing_key_file or a fallback) is also one of the identity broker's keys; each role needs its own key")

// errAS2IsNotTheEntry refuses an AS-2 other than the one the identity
// broker's gateway entry names.
var errAS2IsNotTheEntry = errors.New("the MCP gateway's authorization server (AS-2) is not the one the identity broker's gateway entry names")

// errGatewayIsNotAS2Resource refuses gateway calls sent to a gateway whose
// resource is not the one AS-2 issues their tokens for.
var errGatewayIsNotAS2Resource = errors.New("the MCP gateway's resource is not the one its authorization server (AS-2) issues tokens for")

// as1Issuer is the issuer of the AS-1 the identity broker uses, hosted or
// not; empty without one.
func as1Issuer(cfg Config) string {
	switch {
	case cfg.IdentityBrokerHostedAS1 != nil:
		return cfg.IdentityBrokerHostedAS1.Issuer
	case cfg.IdentityBrokerAS1 != nil:
		return cfg.IdentityBrokerAS1.Issuer
	}
	return ""
}

// checkGatewayEntry returns the embedded gateway entry of file, after
// checking it against the embedded gateway this process runs and the
// embedded gateway's signing key against the broker's keys; nil when file
// has none, which then refuses a signing key. It runs before AS-1 is built.
func checkGatewayEntry(cfg Config, file brokerconfig.File, embedded *embeddedGateway, key as2SigningKey) (*identitybroker.GatewayEntry, error) {
	if len(file.Gateways) == 0 {
		if key.file != "" {
			return nil, errAS2SigningKeyWithoutEntry
		}
		return nil, nil
	}
	// The broker accepts one embedded entry (identitybroker.ValidateGatewayEntries).
	entry := file.Gateways[0]
	entry.Servers = slices.Clone(entry.Servers)
	if embedded == nil {
		return nil, errEmbeddedEntryWithoutEmbeddedGateway
	}
	for _, f := range []struct{ name, entry, embedded string }{
		{"as2_issuer", entry.AS2Issuer, embedded.Issuer},
		{"token_endpoint", entry.TokenEndpoint, embedded.TokenEndpoint},
		{"resource", entry.Resource, embedded.Resource},
	} {
		if f.entry != f.embedded {
			return nil, fmt.Errorf("%w: its %s is %q, the embedded gateway's is %q", errEmbeddedEntryDisagrees, f.name, f.entry, f.embedded)
		}
	}
	for _, server := range entry.Servers {
		if !slices.Contains(embedded.Routes, server) {
			return nil, fmt.Errorf("%w: %q", errEmbeddedEntryServerIsNotARoute, server)
		}
	}
	if len(entry.Servers) != 0 {
		if !cfg.IdentityBrokerAS2Trust.TrustsAnIssuer() {
			return nil, errGatewayServersBeforeAS2TrustsAS1
		}
		if cfg.IdentityBrokerHostedAS1 == nil && cfg.IdentityBrokerAS1 == nil {
			return nil, errGatewayServersWithoutAS1
		}
	}
	if key.file == "" {
		return nil, errNoAS2SigningKey
	}
	as2Keys, err := as2SigningPublicKeys(key)
	if err != nil {
		return nil, fmt.Errorf("the embedded gateway's signing key: %w", err)
	}
	if err := checkAS2KeysAreNotBrokerKeys(as2Keys, file.Keys.PublicKeys()); err != nil {
		return nil, err
	}
	return &entry, nil
}

// checkAS2KeysAreNotBrokerKeys refuses an AS-2 signing key that is one of
// brokerKeys, and, failing closed, a key it cannot compare.
func checkAS2KeysAreNotBrokerKeys(as2Keys, brokerKeys []crypto.PublicKey) error {
	for _, pub := range as2Keys {
		eq, ok := pub.(interface{ Equal(crypto.PublicKey) bool })
		if !ok {
			return fmt.Errorf("the embedded gateway's signing key: a %T key cannot be compared with the identity broker's keys", pub)
		}
		if slices.ContainsFunc(brokerKeys, eq.Equal) {
			return errAS2SigningKeyIsABrokerKey
		}
	}
	return nil
}

// identityBrokerParts is the identity broker as startup builds it.
type identityBrokerParts struct {
	// service is the boundary Mecatl calls; Mecatl never gets the core.
	service identitybroker.Service
	// hosted is the hosted AS-1; nil when none is configured.
	hosted *IdentityBrokerAS1Server
	// hostedServer is the hosted AS-1 itself, for tests; nil when none is
	// configured.
	hostedServer *as1server.Server
	// as1HTTPClient is the broker's in-process client to the hosted AS-1, for
	// tests; nil when none is configured.
	as1HTTPClient *http.Client
	// client signs the broker's client assertions; nil for an injected core
	// or without a broker client key.
	client *identitybroker.ClientAssertionSigner
	// clientKeySetFile is the broker client's public key set file, which
	// AS-2 registers; empty without a broker client key.
	clientKeySetFile string
	// core is the real broker; nil for an injected test core.
	core *identitybroker.Broker
	// definitions are the definition records core loaded, or, for an
	// injected core, the records this configuration loads.
	definitions []identitybroker.DefinitionRecord
	// recorder holds the core's decision and ID-JAG records; nil for an
	// injected core.
	recorder *identitybroker.MemoryRecorder
	// gatewayEntry is the configuration file's embedded gateway entry, which
	// startup checked against the embedded gateway; nil without one, and then
	// the MCP broker sets up no AS-2.
	gatewayEntry *identitybroker.GatewayEntry
	// brokerPublicKeys are the public keys of the identity broker's
	// configuration file, which the AS-2 the MCP broker builds must not sign
	// with; set only with gatewayEntry.
	brokerPublicKeys []crypto.PublicKey
	// gateway reports whether the core has a gateway, whose AS-2 bindAS2
	// binds: the entry routes servers.
	gateway bool
	// gatewayServers are the servers the gateway routes; set only with
	// gateway.
	gatewayServers []string
	// as2RequestTimeout bounds each request the broker makes at the AS-2 the
	// MCP broker sets up for gatewayEntry; set with gatewayEntry.
	as2RequestTimeout time.Duration
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
// names; no key is generated. Its embedded gateway entry, if any, must agree
// with embedded, the embedded gateway this process runs, and needs the
// embedded gateway's signing key, as2Key, which is refused without one. With
// a hosted AS-1 and no test override, session open converts users' access
// tokens at that AS-1 in process: buildBrokerWithHostedAS1 registers the
// file's broker client key set there for the confidential delegate, which may
// obtain ID-JAGs for the entry when it routes servers.
func buildIdentityBroker(ctx context.Context, cfg Config, embedded *embeddedGateway, as2Key as2SigningKey) (identityBrokerParts, error) {
	if !cfg.IdentityBroker {
		if err := checkIdentityBrokerOff(cfg, as2Key); err != nil {
			return identityBrokerParts{}, err
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
	var entry *identitybroker.GatewayEntry
	if core == nil {
		if cfg.IdentityBrokerConfigFile == "" {
			return identityBrokerParts{}, errNoIdentityBrokerConfigFile
		}
		var err error
		if file, err = brokerconfig.Load(cfg.IdentityBrokerConfigFile, brokerconfig.Host{AS1: cfg.IdentityBrokerHostedAS1 != nil, AS1Issuer: as1Issuer(cfg)}); err != nil {
			return identityBrokerParts{}, err
		}
		if entry, err = checkGatewayEntry(cfg, file, embedded, as2Key); err != nil {
			return identityBrokerParts{}, err
		}
	} else if as2Key.file != "" {
		// An injected test core has no configuration file, so no entry.
		return identityBrokerParts{}, errAS2SigningKeyWithoutEntry
	}
	// Production wiring: host AS-1 with the broker client as the delegate,
	// then point the broker's session open at that AS-1. A test that replaces
	// the core or sets IdentityBrokerAS1 explicitly keeps the paths below.
	var brokerKeys []crypto.PublicKey
	if entry != nil {
		brokerKeys = file.Keys.PublicKeys()
	}
	if core == nil && cfg.IdentityBrokerAS1 == nil && cfg.IdentityBrokerHostedAS1 != nil {
		parts, err := buildBrokerWithHostedAS1(ctx, cfg, file, entry)
		parts.brokerPublicKeys = brokerKeys
		return parts, err
	}
	var parts identityBrokerParts
	if core == nil {
		broker, err := newIdentityBroker(ctx, cfg, file, entry)
		if err != nil {
			return identityBrokerParts{}, fmt.Errorf("build identity broker: %w", err)
		}
		core = broker.core
		parts = broker.parts()
	} else {
		// An injected core is a test's; the server still sends the references
		// of the definitions this configuration loads, as it does for the
		// real broker. Only the references are used, so no ceiling is
		// resolved.
		definitions, err := loadIdentityBrokerDefinitions(ctx, cfg, nil)
		if err != nil {
			return identityBrokerParts{}, fmt.Errorf("build identity broker: %w", err)
		}
		parts.definitions = definitions
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
	parts.brokerPublicKeys = brokerKeys
	return parts, nil
}

// checkIdentityBrokerOff refuses configuration that needs the identity
// broker when it is off.
func checkIdentityBrokerOff(cfg Config, as2Key as2SigningKey) error {
	switch {
	case cfg.IdentityBrokerHostedAS1 != nil:
		return errIdentityBrokerAS1WithoutBroker
	case cfg.IdentityBrokerConfigFile != "":
		return errIdentityBrokerConfigFileWithoutBroker
	case as2Key.file != "":
		return errAS2SigningKeyWithoutEntry
	}
	return nil
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
// pointed at that AS-1 in process. With an entry that routes servers, the
// delegate also obtains per-call ID-JAGs for its AS-2 there, whose actor
// tokens AS-1 checks with the broker's CredentialVerifier; AS-1 issues none
// for any other audience or resource.
func buildBrokerWithHostedAS1(ctx context.Context, cfg Config, file brokerconfig.File, entry *identitybroker.GatewayEntry) (identityBrokerParts, error) {
	as1 := cfg.IdentityBrokerHostedAS1
	sources := make([]string, len(as1.PublicClients))
	for i, c := range as1.PublicClients {
		sources[i] = c.ID
	}
	delegate := &as1server.DelegateClient{
		ID:                   identitybroker.BrokerClientID,
		KeySetFile:           file.BrokerClientKeySetFile,
		AllowedSourceClients: sources,
		// Every session's hard expiry. Never derived from, nor the source of,
		// a schedule's horizon (#2569).
		IDJAGMaxLifetime: file.Limits.IDJAGMaxLifetime,
	}
	if entry != nil && len(entry.Servers) != 0 {
		// Shortcut: AS-1 issues ID-JAGs only for a gateway entry that routes servers, and production's routes none until AS-2 trusts AS-1, so production configures no target and no actor profile; removed by #2533.
		delegate.IDJAG = &as1server.IDJAGIssuance{
			ActIssuer: identitybroker.ActorIssuer(identityBrokerConfig(cfg, file).TrustDomain),
			Targets: []as1server.IDJAGTarget{{
				Issuer: entry.AS2Issuer, ClientID: identitybroker.BrokerClientID, Resources: []string{entry.Resource},
				Lifetime: file.Limits.IDJAGLifetime,
			}},
		}
	}
	server, err := buildHostedAS1(ctx, as1, file, delegate)
	if err != nil {
		return identityBrokerParts{}, err
	}
	httpClient := server.InProcessClient()
	httpClient.Timeout = file.Limits.AS1RequestTimeout
	// AssertionNow stays nil, and Now unless a test sets identityBrokerNow:
	// production uses time.Now for the broker and its assertions.
	cfg.IdentityBrokerAS1 = &IdentityBrokerAS1{
		Issuer: server.Issuer(), TokenEndpoint: server.TokenEndpoint(), HTTPClient: httpClient,
		Now: cfg.identityBrokerNow,
	}
	if delegate.IDJAG != nil {
		cfg.IdentityBrokerAS1.ActorTokens = server
	}
	broker, err := newIdentityBroker(ctx, cfg, file, entry)
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
	parts.as1HTTPClient = httpClient
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
	core *identitybroker.Broker
	// definitions are the records core loaded.
	definitions []identitybroker.DefinitionRecord
	client      *identitybroker.ClientAssertionSigner
	// clientKeySetFile is the broker client's public key set file.
	clientKeySetFile string
	recorder         *identitybroker.MemoryRecorder
	// gatewayEntry is the configuration file's checked embedded gateway
	// entry; nil without one.
	gatewayEntry *identitybroker.GatewayEntry
	gateway      bool
	// gatewayServers are the servers the gateway routes.
	gatewayServers []string
	// as2RequestTimeout is the configuration file's AS-2 request timeout.
	as2RequestTimeout time.Duration
}

// parts returns the identity broker's parts that b holds; the caller adds
// the service, the hosted AS-1, and release.
func (b identityBroker) parts() identityBrokerParts {
	return identityBrokerParts{
		client: b.client, clientKeySetFile: b.clientKeySetFile, core: b.core, definitions: b.definitions,
		recorder: b.recorder, gatewayEntry: b.gatewayEntry, gateway: b.gateway, gatewayServers: b.gatewayServers,
		as2RequestTimeout: b.as2RequestTimeout,
	}
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
// the built-in definitions and the operator's global definitions from
// cfg.AgentsDirs, the directories the harness reads first; a directory that
// cannot be read fails startup, and each definition left out or adjusted is
// logged. With cfg.IdentityBrokerAS1, session open converts access tokens at
// that AS-1, authenticating with the broker client key, and calls get their
// ID-JAGs there; a gateway is configured only then, and only when entry, the
// checked gateway entry, routes servers.
func newIdentityBroker(ctx context.Context, cfg Config, file brokerconfig.File, entry *identitybroker.GatewayEntry) (identityBroker, error) {
	config := identityBrokerConfig(cfg, file)
	as1 := cfg.IdentityBrokerAS1
	// A gateway needs AS-1 to issue its ID-JAGs, so without an AS-1 there is
	// none; checkGatewayEntry refuses an entry routing servers then.
	// Shortcut: production's gateway entry routes no servers, so gateway calls are refused as not configured; removed by #2533 (AS-2 trusts AS-1).
	var gatewayServers []string
	if as1 != nil && entry != nil && len(entry.Servers) != 0 {
		gatewayServers = slices.Clone(entry.Servers)
	}
	// Each definition's ceiling is resolved against the servers the gateway
	// routes.
	definitions, err := loadIdentityBrokerDefinitions(ctx, cfg, gatewayServers)
	if err != nil {
		return identityBroker{}, err
	}
	var client *identitybroker.ClientAssertionSigner
	if file.Keys.BrokerClient != nil {
		if client, err = identitybroker.NewClientAssertionSigner(file.Keys.BrokerClient, file.Limits.ClientAssertionLifetime); err != nil {
			return identityBroker{}, err
		}
	}
	// Records are kept in memory (the shortcut on identitybroker.MemoryRecorder).
	recorder := identitybroker.NewMemoryRecorder()
	diag := cfg.diag()
	opts := []identitybroker.Option{
		identitybroker.WithRecorder(loggedRecorder{recorder: recorder, diag: diag}),
		identitybroker.WithDeliveryFailures(func(f identitybroker.DeliveryFailure) {
			diag.Log(context.Background(), port.LevelWarn, "identity broker record not accepted",
				"audit_id", f.AuditID, "type", f.Type, "err", f.Err.Error())
		}),
		identitybroker.WithAgentIssuerFallbacks(file.Keys.AgentIssuerFallbacks...),
	}
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
		if gatewayServers != nil {
			opts = append(opts, identitybroker.WithGateway(identitybroker.Gateway{Issuer: entry.AS2Issuer, Resource: entry.Resource, Servers: gatewayServers}))
		}
	}
	core, err := identitybroker.New(config, definitions, file.Keys.AgentIssuer, opts...)
	if err != nil {
		return identityBroker{}, err
	}
	if as1 != nil && as1.ActorTokens != nil {
		if err := as1.ActorTokens.UseCredentialVerifier(core); err != nil {
			return identityBroker{}, fmt.Errorf("bind AS-1's actor-token check: %w", err)
		}
	}
	return identityBroker{
		core: core, definitions: definitions, client: client, clientKeySetFile: file.BrokerClientKeySetFile,
		recorder: recorder, gatewayEntry: entry, gateway: gatewayServers != nil, gatewayServers: gatewayServers,
		as2RequestTimeout: file.Limits.AS2RequestTimeout,
	}, nil
}

// loadIdentityBrokerDefinitions loads the identity broker's definition
// records: the built-ins of this build and the global definitions in
// cfg.AgentsDirs, with ceilings resolved against gatewayServers. A definition
// left out or adjusted is logged, without its contents; a directory that
// cannot be read fails the load.
func loadIdentityBrokerDefinitions(ctx context.Context, cfg Config, gatewayServers []string) ([]identitybroker.DefinitionRecord, error) {
	records, problems, err := definitionloader.Load(ctx, definitionloader.Config{Dirs: cfg.AgentsDirs, BuildID: buildinfo.BuildID, Servers: gatewayServers})
	for _, p := range problems {
		outcome := "definition adjusted and loaded"
		if p.Dropped {
			outcome = "definition left out"
		}
		cfg.diag().Log(ctx, port.LevelWarn, "identity broker definition problem",
			"location", p.Location, "reason", p.Reason, "outcome", outcome)
	}
	if err != nil {
		return nil, fmt.Errorf("load identity broker definitions: %w", err)
	}
	return records, nil
}

// identityDefinitionRefs returns the references the server registers
// sessions' main agents with: those of records, the definitions the broker
// loaded. The server takes a global record of a session's definition name to
// be the definition the session runs, which holds only while the operator's
// agent directories win every name. An operator harness_context policy can
// order agent-definition sources, or exclude one per name, so another source,
// such as a driver, can win a name the broker loaded. Under such a policy only
// the built-in references are passed: a named session then registers by name
// only and is refused, as any definition the broker did not load is, rather
// than registering another source's definition as the operator's.
//
// Shortcut: a session knows its definition by name only; removed by #2548 (sessions record their exact reference, so this guess goes away).
func identityDefinitionRefs(cfg Config, records []identitybroker.DefinitionRecord) []identitybroker.DefinitionRef {
	refs := definitionRefs(records)
	if cfg.harnessResolver == nil {
		return refs
	}
	return slices.DeleteFunc(refs, func(ref identitybroker.DefinitionRef) bool {
		return ref.Source != identitybroker.SourceSystem
	})
}

// definitionRefs returns the references of records.
func definitionRefs(records []identitybroker.DefinitionRecord) []identitybroker.DefinitionRef {
	refs := make([]identitybroker.DefinitionRef, len(records))
	for i, rec := range records {
		refs[i] = rec.Ref
	}
	return refs
}

// loggedRecorder is the identity broker's record sink: recorder keeps each
// record, and diag gets a one-line debug summary of it, with its type,
// AuditID, outcome, and refusal, never its subjects or credential references.
type loggedRecorder struct {
	recorder identitybroker.Recorder
	diag     port.Diagnostics
}

// Record implements identitybroker.Recorder.
func (r loggedRecorder) Record(ctx context.Context, rec identitybroker.Record) error {
	if err := r.recorder.Record(ctx, rec); err != nil {
		return err
	}
	h := rec.Header()
	r.diag.Log(context.WithoutCancel(ctx), port.LevelDebug, "identity broker record",
		"type", h.Type, "audit_id", h.AuditID, "outcome", string(h.Outcome), "code", string(h.Code), "reason", string(h.Reason))
	return nil
}
