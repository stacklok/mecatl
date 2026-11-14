// Package mcpbrokertest makes the MCP broker's AS-2 trust an AS-1 in tests,
// so they can redeem ID-JAGs at the real embedded authorization server; see
// mcpbroker.AS2Trust.
package mcpbrokertest

import (
	"encoding/json"
	"slices"
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
// and the CA file that verifies it, the AS-1 subjects it may issue for, and
// the oldest ID-JAG AS-2 accepts. Zero MaxAssertionAge selects ten minutes in
// TrustAS1.
type AS1Trust = as2trust.Trust

// TrustAS1 returns the AS-2 trust in trust's issuer, for
// mcpbroker.ToolHiveConfig.AS2Trust.
func TrustAS1(trust AS1Trust) mcpbroker.AS2Trust {
	if trust.MaxAssertionAge <= 0 {
		trust.MaxAssertionAge = 10 * time.Minute
	}
	trust.Subjects = slices.Clone(trust.Subjects)
	return mcpbroker.NewAS2Trust(trust)
}

// StartAS2 starts an MCP broker process with one static protected upstream
// and AS-2 set up for the broker client brokerClientID, whose public keys
// are the JWK Set brokerKeySet, trusting trust's issuer. It
// closes the process when t ends. It sets an environment variable, so t must
// not be parallel.
func StartAS2(t testing.TB, brokerClientID string, brokerKeySet []byte, trust AS1Trust) *mcpbroker.Process {
	t.Helper()
	t.Setenv(clientSecretEnv, "construction-only-secret")
	cfg := mcpbroker.ToolHiveConfig{
		CallbackURL: CallbackURL,
		Profiles: []mcpbroker.ToolHiveProfile{{
			Name: "private", URL: "https://private.example/mcp", Auth: "oauth",
			OAuth: &mcpbroker.ToolHiveOAuth{ // #nosec G101 -- placeholder endpoints and an environment variable name, not credentials.
				AuthorizationEndpoint: "https://issuer.example/authorize", TokenEndpoint: "https://issuer.example/token",
				ClientID: "private-client", ClientSecretEnv: clientSecretEnv, Scopes: []string{"openid"},
			},
			Static: []mcpbroker.StaticTool{{Name: "echo", Description: "echo", Schema: json.RawMessage(`{"type":"object"}`)}},
		}},
		BrokerClient: &mcpbroker.ToolHiveBrokerClient{ID: brokerClientID, KeySet: brokerKeySet},
		AS2Trust:     TrustAS1(trust),
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
