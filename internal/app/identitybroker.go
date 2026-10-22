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
		core = broker
	}
	release := func() {}
	if closer, ok := core.(io.Closer); ok {
		release = func() { _ = closer.Close() }
	}
	return identitybroker.NewInProcess(identitybroker.UnverifiedAuthenticator{Name: identityBrokerWorkload}, core), release, nil
}

// newIdentityBroker constructs the real identity broker with the interim
// configuration and a key generated for this process. It loads no
// definitions yet.
func newIdentityBroker(cfg Config) (*identitybroker.Broker, error) {
	config := interimIdentityBrokerConfig
	if cfg.identityBrokerConfig != nil {
		config = *cfg.identityBrokerConfig
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate signing key: %w", err)
	}
	return identitybroker.New(config, nil, key)
}
