package app

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
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

// buildIdentityBroker returns the identity broker Mecatl calls, and a function
// that releases it. Both are no-ops when the broker is not enabled. Mecatl gets
// only the authentication boundary, never the core behind it. A construction
// failure fails startup, as does a configuration that gives sessions a shell.
func buildIdentityBroker(cfg Config) (identitybroker.Service, func(), error) {
	if !cfg.IdentityBroker {
		return nil, func() {}, nil
	}
	if shellAvailable(cfg) {
		return nil, nil, errIdentityBrokerWithShell
	}
	core := cfg.identityBrokerCore
	if core == nil {
		broker, err := newIdentityBroker(cfg)
		if err != nil {
			return nil, nil, fmt.Errorf("build identity broker: %w", err)
		}
		core = broker.core
	}
	release := func() {}
	if closer, ok := core.(io.Closer); ok {
		release = func() { _ = closer.Close() }
	}
	return identitybroker.NewInProcess(identitybroker.UnverifiedAuthenticator{Name: identityBrokerWorkload}, core), release, nil
}

// identityBroker is the real identity broker's startup state: its core and
// the signer for its client authentication at AS-1 and AS-2. The signer stays
// inside this wiring; nothing calls AS-1 or AS-2 yet, and broker session open
// (#2492) will pass it into the broker's construction.
type identityBroker struct {
	core   *identitybroker.Broker
	client *identitybroker.ClientAssertionSigner
}

// newIdentityBroker constructs the real identity broker with the interim
// configuration and, one key per role, an agent issuer key and a broker
// client key generated for this process. It loads no definitions yet.
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
	core, err := identitybroker.New(config, nil, key)
	if err != nil {
		return identityBroker{}, err
	}
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return identityBroker{}, fmt.Errorf("generate broker client key: %w", err)
	}
	client, err := identitybroker.NewClientAssertionSigner(clientKey, 0)
	if err != nil {
		return identityBroker{}, err
	}
	return identityBroker{core: core, client: client}, nil
}
