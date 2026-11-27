// Package mcpbrokertest makes the MCP broker's AS-2 trust an AS-1 in tests,
// so they can redeem ID-JAGs at the real embedded authorization server; see
// mcpbroker.AS2Trust.
package mcpbrokertest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker/internal/as2trust"
)

// CallbackURL is the gateway callback StartAS2 configures; AS-2's issuer is
// derived from it.
const CallbackURL = "https://broker.example/callback"

// clientSecretEnv names the protected upstream's client secret, which
// ToolHive reads at construction.
const clientSecretEnv = "MECATL_TEST_CLIENT_SECRET" // #nosec G101 -- environment variable name, not a credential.

// AS1Trust is an ID-JAG issuer for AS-2 to trust: its issuer, its JWKS URL
// and the CA file that verifies it, whether that URL may be a private or
// loopback address (a test's AS-1 is on loopback, so set
// AllowPrivateAddresses), the AS-1 subjects it may issue for, and the oldest
// ID-JAG AS-2 accepts. Zero MaxAssertionAge selects ten minutes in TrustAS1.
type AS1Trust = as2trust.Trust

// TrustAS1 returns the AS-2 trust in trust's issuer, for
// mcpbroker.ToolHiveConfig.AS2Trust.
func TrustAS1(trust AS1Trust) mcpbroker.AS2Trust {
	return mcpbroker.NewAS2Trust(trust.WithDefaults())
}

// StartAS2 starts an MCP broker process with one static protected upstream
// and AS-2 set up for the broker client brokerClientID, whose public keys
// are the JWK Set brokerKeySet, which it writes to a file as an operator
// would, trusting trust's issuer. It
// closes the process when t ends. It sets an environment variable, so t must
// not be parallel.
func StartAS2(t testing.TB, brokerClientID string, brokerKeySet []byte, trust AS1Trust) *mcpbroker.Process {
	t.Helper()
	return StartAS2With(t, brokerClientID, brokerKeySet, trust, AS2Options{})
}

// AS2Options vary the process StartAS2With starts.
type AS2Options struct {
	// Profiles are upstreams added after the static protected one, such as
	// anonymous backends the vMCP routes.
	Profiles []mcpbroker.ToolHiveProfile
	// GatewayTokenLifetime sets mcpbroker.ToolHiveConfig's field of that
	// name, and ExpiredTokenGrace the vMCP's grace after a gateway token's
	// exp, through the trust; zero keeps their defaults.
	GatewayTokenLifetime, ExpiredTokenGrace time.Duration
}

// StartAS2With is StartAS2 with opts.
func StartAS2With(t testing.TB, brokerClientID string, brokerKeySet []byte, trust AS1Trust, opts AS2Options) *mcpbroker.Process {
	t.Helper()
	t.Setenv(clientSecretEnv, "construction-only-secret")
	keySetFile := filepath.Join(t.TempDir(), "broker-client-jwks.json")
	if err := os.WriteFile(keySetFile, brokerKeySet, 0o600); err != nil {
		t.Fatalf("mcpbrokertest: write the broker key set: %v", err)
	}
	cfg := mcpbroker.ToolHiveConfig{
		CallbackURL: CallbackURL,
		Profiles: append([]mcpbroker.ToolHiveProfile{{
			Name: "private", URL: "https://private.example/mcp", Auth: "oauth",
			OAuth: &mcpbroker.ToolHiveOAuth{ // #nosec G101 -- placeholder endpoints and an environment variable name, not credentials.
				AuthorizationEndpoint: "https://issuer.example/authorize", TokenEndpoint: "https://issuer.example/token",
				ClientID: "private-client", ClientSecretEnv: clientSecretEnv, Scopes: []string{"openid"},
			},
			Static: []mcpbroker.StaticTool{{Name: "echo", Description: "echo", Schema: json.RawMessage(`{"type":"object"}`)}},
		}}, opts.Profiles...),
		BrokerClient:         &mcpbroker.ToolHiveBrokerClient{ID: brokerClientID, KeySetFile: keySetFile},
		AS2Trust:             TrustAS1(withGrace(trust, opts.ExpiredTokenGrace)),
		GatewayTokenLifetime: opts.GatewayTokenLifetime,
	}
	process, err := mcpbroker.NewToolHiveProcess(t.Context(), cfg)
	if err != nil {
		t.Fatalf("mcpbrokertest: start AS-2: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	if process.AS2 == nil {
		t.Fatal("mcpbrokertest: AS-2 is not set up")
	}
	return process
}

// withGrace returns trust with the vMCP's grace after a gateway token's exp
// set to grace, when positive.
func withGrace(trust AS1Trust, grace time.Duration) AS1Trust {
	if grace > 0 {
		trust.ExpiredTokenGrace = grace
	}
	return trust
}
