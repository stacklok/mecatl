package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/authserverclient"
)

// identityBrokerWorkload is the workload name the identity broker records until
// it authenticates the workload's credential, which will be addressed later.
// The name, and Workload.Verified being false, keep it from reading as a
// verified identity.
const identityBrokerWorkload = "mecak8s-unverified"

// interimIdentityBrokerConfig is the identity broker's configuration until
// operator configuration and keys exist. The .invalid names keep its agent
// credentials from being mistaken for a real deployment's.
var interimIdentityBrokerConfig = identitybroker.Config{
	TrustDomain:        "agents.mecatl.invalid",
	Audience:           "https://identity-broker.mecatl.invalid",
	CredentialLifetime: time.Hour,
}

// errIdentityBrokerWithShell refuses the identity broker while sessions can run
// a shell in this process: a model-driven shell could read the agent issuer's
// signing key from the process's memory or environment.
var errIdentityBrokerWithShell = errors.New("the identity broker holds its signing key in this process, so it cannot run while sessions have a shell in this process; use --no-shell, or --shell \"\" with Kubernetes execution to keep the remote Shell")

// IdentityBrokerAS1Config hosts AS-1, the identity broker's sign-in
// authorization server, in this process. It requires Config.IdentityBroker.
// AS-1 keeps pending sign-ins and refresh tokens in memory.
type IdentityBrokerAS1Config struct {
	// Issuer is AS-1's https issuer URL; its routes are served under its path.
	Issuer string
	// Audience is the Mecatl API audience AS-1 issues access tokens for.
	Audience string
	// SigningKeyFile is a PEM key that signs access tokens. Empty generates
	// one, so tokens stop verifying when the process restarts.
	SigningKeyFile string
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

// buildIdentityBroker returns the identity broker Mecatl calls, the hosted
// AS-1 when configured, and a function that releases both. All are nil or
// no-ops when the broker is not enabled. Mecatl gets only the authentication
// boundary, never the core behind it. A construction failure fails startup,
// as does a configuration that gives sessions a shell.
//
// Session open does not convert tokens at the hosted AS-1: the broker is not
// pointed at it here.
func buildIdentityBroker(ctx context.Context, cfg Config) (identitybroker.Service, *IdentityBrokerAS1Server, func(), error) {
	if !cfg.IdentityBroker {
		if cfg.IdentityBrokerHostedAS1 != nil {
			return nil, nil, nil, errIdentityBrokerAS1WithoutBroker
		}
		return nil, nil, func() {}, nil
	}
	if shellAvailable(cfg) {
		return nil, nil, nil, errIdentityBrokerWithShell
	}
	core := cfg.identityBrokerCore
	if core == nil {
		broker, err := newIdentityBroker(cfg)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("build identity broker: %w", err)
		}
		core = broker.core
	}
	release := func() {}
	if closer, ok := core.(io.Closer); ok {
		release = func() { _ = closer.Close() }
	}
	var hosted *IdentityBrokerAS1Server
	if as1 := cfg.IdentityBrokerHostedAS1; as1 != nil {
		clients := make([]as1server.PublicClient, len(as1.PublicClients))
		for i, c := range as1.PublicClients {
			clients[i] = as1server.PublicClient{ID: c.ID, RedirectURIs: c.RedirectURIs}
		}
		server, err := as1server.New(ctx, as1server.Config{
			Issuer: as1.Issuer, Audience: as1.Audience, SigningKeyFile: as1.SigningKeyFile,
			PublicClients: clients,
			Upstream: as1server.UpstreamConfig{
				Issuer: as1.UpstreamIssuer, ClientID: as1.UpstreamClientID,
				ClientSecretFile: as1.UpstreamClientSecretFile, SubjectClaim: as1.SubjectClaim,
				CAFile: as1.UpstreamCAFile, AllowPrivateAddresses: as1.UpstreamAllowPrivateAddresses,
			},
		})
		if err != nil {
			release()
			return nil, nil, nil, fmt.Errorf("build AS-1: %w", err)
		}
		hosted = &IdentityBrokerAS1Server{Issuer: server.Issuer(), Routes: server.Routes(), Handler: server.Handler(), HTTPClient: server.InProcessClient()}
		releaseCore := release
		release = func() {
			_ = server.Close()
			releaseCore()
		}
	}
	return identitybroker.NewInProcess(identitybroker.UnverifiedAuthenticator{Name: identityBrokerWorkload}, core), hosted, release, nil
}

// identityBroker is the real identity broker's startup state: its core and
// the signer for its client authentication at AS-1 and AS-2. With an AS-1
// configured, session open authenticates there with the signer; without one,
// which is every deployment until session open uses the hosted AS-1 (#2607),
// nothing calls AS-1 or AS-2.
type identityBroker struct {
	core   *identitybroker.Broker
	client *identitybroker.ClientAssertionSigner
}

// IdentityBrokerAS1 is the AS-1 the identity broker converts access tokens
// at during session open.
type IdentityBrokerAS1 struct {
	Issuer        string
	TokenEndpoint string
	HTTPClient    *http.Client
	// ClientKey replaces the generated broker client key, so a test can
	// register its public key at AS-1 before the broker is built.
	ClientKey *ecdsa.PrivateKey
}

// newIdentityBroker constructs the real identity broker with the interim
// configuration and, one key per role, an agent issuer key and a broker
// client key generated for this process. It loads no definitions yet. With
// cfg.IdentityBrokerAS1, session open converts access tokens at that AS-1,
// authenticating with the broker client key.
//
// The broker client key lasts only as long as the process until an operator
// key replaces it (#2538): an external server registered with its JWKS inline
// would refuse the broker after a restart, while in-process servers register
// it again at each build.
func newIdentityBroker(cfg Config) (identityBroker, error) {
	config := interimIdentityBrokerConfig
	if cfg.identityBrokerConfig != nil {
		config = *cfg.identityBrokerConfig
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return identityBroker{}, fmt.Errorf("generate signing key: %w", err)
	}
	as1 := cfg.IdentityBrokerAS1
	var clientKey *ecdsa.PrivateKey
	if as1 != nil {
		clientKey = as1.ClientKey
	}
	if clientKey == nil {
		if clientKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
			return identityBroker{}, fmt.Errorf("generate broker client key: %w", err)
		}
	}
	client, err := identitybroker.NewClientAssertionSigner(clientKey, 0)
	if err != nil {
		return identityBroker{}, err
	}
	var opts []identitybroker.Option
	if as1 != nil {
		as1Client, err := authserverclient.New(authserverclient.Config{
			Issuer: as1.Issuer, TokenEndpoint: as1.TokenEndpoint, Assertions: client, HTTPClient: as1.HTTPClient,
		})
		if err != nil {
			return identityBroker{}, fmt.Errorf("AS-1 client: %w", err)
		}
		opts = append(opts, identitybroker.WithAuthServer(as1Client))
	}
	core, err := identitybroker.New(config, nil, key, opts...)
	if err != nil {
		return identityBroker{}, err
	}
	return identityBroker{core: core, client: client}, nil
}
