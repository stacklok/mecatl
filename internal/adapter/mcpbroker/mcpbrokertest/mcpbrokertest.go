// Package mcpbrokertest starts the MCP broker's AS-2 for tests, trusting a
// test's AS-1 as production does (mcpbroker.AS1Trust), and signs users in at
// it, so tests can redeem ID-JAGs at the real embedded authorization server.
//
// The package is test code; production packages never import it.
package mcpbrokertest

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker/internal/testseam"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker/mcpbrokertest/as2signin"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// CallbackURL is the gateway callback StartAS2 configures; AS-2's issuer is
// derived from it (mcpbroker.AS2Endpoints).
const CallbackURL = "https://broker.example/callback"

// clientSecretEnv names the protected upstream's client secret, which
// ToolHive reads at construction.
const clientSecretEnv = "MECATL_TEST_CLIENT_SECRET" // #nosec G101 -- environment variable name, not a credential.

// AS1 is the AS-1 AS-2 trusts, and the identity harness whose IdP is the
// company IdP both sign users in through. A test's AS-1 from as1servertest
// fills it from as1servertest.AS1's Issuer, JWKSURL, and Harness.
type AS1 struct {
	// Issuer and JWKSURL are AS-1's issuer and key set URL, served on
	// loopback TLS that Harness's certificate verifies.
	Issuer, JWKSURL string
	// ActorIssuer is the agent trust domain as AS-1's ID-JAGs name it in
	// act.iss, such as identitybroker.ActorIssuer(trustDomain).
	ActorIssuer string
	// Harness runs the company IdP AS-1 signs users in at.
	Harness *identityharness.Harness
}

// Trust registers AS-2 at as2Issuer at the company IdP and returns AS-2's
// trust in as1, as production configures it: AS-1's keys and the company IdP
// on loopback, so private addresses are allowed, with the harness's
// certificate trusted. A test that builds AS-2 itself sets it on
// mcpbroker.ToolHiveConfig.AS1Trust; StartAS2 calls it.
func Trust(t testing.TB, as1 AS1, as2Issuer string) *mcpbroker.AS1Trust {
	t.Helper()
	clientID, secretFile, caFile := as2signin.RegisterAtIdP(t, as1.Harness, as2Issuer)
	return &mcpbroker.AS1Trust{
		Issuer: as1.Issuer, JWKSURL: as1.JWKSURL, CAFile: caFile, AllowPrivateAddresses: true,
		ActorIssuer: as1.ActorIssuer,
		CompanyIdP: mcpbroker.AS2CompanyIdP{
			Issuer: as1.Harness.IdP.Issuer(), ClientID: clientID, ClientSecretFile: secretFile,
			CAFile: caFile, AllowPrivateAddresses: true,
		},
	}
}

// SignInSeam is the test seam that has AS-2 register the client SignIn signs
// users in through, for an AS-2 a test cannot give a store, such as one
// composition builds (app.Config.MCPBrokerTestSeam).
func SignInSeam() mcpbroker.TestSeam {
	return mcpbroker.NewTestSeam(signInSeam(0))
}

func signInSeam(maxCallDuration time.Duration) testseam.Seam {
	return testseam.Seam{
		GatewayMaxCallDuration: maxCallDuration,
		SignInClient:           &testseam.Client{ID: as2signin.ClientID, RedirectURI: as2signin.RedirectURI},
	}
}

// SignIn signs user, a user of the company IdP at h, in at the AS-2 at
// issuer for resource, its vMCP resource, as a browser would, through the
// company IdP and each provider upstream, and returns their AS-2 access
// token. AS-2 must have been built with SignInSeam, and its provider
// upstreams' providers must sign users in at once, as Provider's do. as2
// serves AS-2's routes, such as a mux the MCP broker's handlers are mounted
// on. From then on AS-2 resolves ID-JAGs whose upstream_subject names user.
// It selects the user at the IdP, so it must not run concurrently with
// another sign-in at h.
func SignIn(t testing.TB, h *identityharness.Harness, as2 http.Handler, issuer, resource, user string) string {
	t.Helper()
	return as2signin.SignIn(t, h, as2, issuer, resource, user)
}

// SignInAtCompanyIdP is SignIn through the company IdP only: it stops where
// AS-2 sends the browser on to the first provider upstream. AS-2 then
// resolves ID-JAGs naming user but holds no provider credential for them, and
// the providers, which need not be reachable, are never contacted.
func SignInAtCompanyIdP(t testing.TB, h *identityharness.Harness, as2 http.Handler, issuer, resource, user string) {
	t.Helper()
	as2signin.SignInAtCompanyIdP(t, h, as2, issuer, resource, user)
}

// Provider starts a fake OAuth provider for a protected upstream, which
// signs every user in at once, so SignIn can complete the upstream's leg.
func Provider(t testing.TB) (authorizationEndpoint, tokenEndpoint string) {
	t.Helper()
	return as2signin.Provider(t)
}

// Process is an MCP broker process StartAS2 started, with AS-2 set up.
type Process struct {
	*mcpbroker.Process
	harness *identityharness.Harness
	mux     http.Handler
	store   as2signin.Store
}

// SignIn signs user, a user of the company IdP, in at AS-2 as a browser
// would, through the company IdP and the protected upstream, and returns
// their AS-2 access token for the vMCP resource. From then on AS-2 resolves
// ID-JAGs whose upstream_subject names user. It selects the user at the IdP,
// so it must not run concurrently with another sign-in at the same harness.
func (p *Process) SignIn(t testing.TB, user string) string {
	t.Helper()
	return SignIn(t, p.harness, p.mux, p.AS2.Issuer, p.AS2.Resource, user)
}

// Users is how many users AS-2 has recorded: one per user signed in at AS-2,
// however often, and none for an ID-JAG redeemed or refused.
func (p *Process) Users() int { return p.store.Users() }

// StartAS2 starts an MCP broker process with one static protected upstream
// and AS-2 set up for the broker client brokerClientID, whose public keys
// are the JWK Set brokerKeySet, which it writes to a file as an operator
// would, trusting as1 (Trust). AS-2 redeems an ID-JAG only for a user
// signed in at it (Process.SignIn). It closes the process when t ends. It
// sets an environment variable, so t must not be parallel.
func StartAS2(t testing.TB, brokerClientID string, brokerKeySet []byte, as1 AS1) *Process {
	t.Helper()
	return StartAS2With(t, brokerClientID, brokerKeySet, as1, AS2Options{})
}

// AS2Options vary the process StartAS2With starts.
type AS2Options struct {
	// Profiles are upstreams added after the static protected one, such as
	// anonymous backends the vMCP routes. A protected one's provider must
	// sign users in at once, as Provider's does, for SignIn to pass it.
	Profiles []mcpbroker.ToolHiveProfile
	// MaxCallDuration sets the vMCP's maximum call duration, which is also
	// its grace after a gateway token's exp; zero keeps the default.
	MaxCallDuration time.Duration
}

// StartAS2With is StartAS2 with opts.
func StartAS2With(t testing.TB, brokerClientID string, brokerKeySet []byte, as1 AS1, opts AS2Options) *Process {
	t.Helper()
	issuer, _, _, err := mcpbroker.AS2Endpoints(CallbackURL)
	if err != nil {
		t.Fatalf("mcpbrokertest: AS-2 endpoints: %v", err)
	}
	t.Setenv(clientSecretEnv, "construction-only-secret")
	keySetFile := filepath.Join(t.TempDir(), "broker-client-jwks.json")
	if err := os.WriteFile(keySetFile, brokerKeySet, 0o600); err != nil {
		t.Fatalf("mcpbrokertest: write the broker key set: %v", err)
	}
	store := as2signin.NewStore()
	authorize, token := Provider(t)
	cfg := mcpbroker.ToolHiveConfig{
		CallbackURL: CallbackURL,
		Profiles: append([]mcpbroker.ToolHiveProfile{{
			Name: "private", URL: "https://private.example/mcp", Auth: "oauth",
			OAuth: &mcpbroker.ToolHiveOAuth{ // #nosec G101 -- a test provider and an environment variable name, not credentials.
				AuthorizationEndpoint: authorize, TokenEndpoint: token,
				ClientID: "private-client", ClientSecretEnv: clientSecretEnv, Scopes: []string{"openid"},
			},
			Static: []mcpbroker.StaticTool{{Name: "echo", Description: "echo", Schema: json.RawMessage(`{"type":"object"}`)}},
		}}, opts.Profiles...),
		BrokerClient: &mcpbroker.ToolHiveBrokerClient{ID: brokerClientID, KeySetFile: keySetFile},
		AS1Trust:     Trust(t, as1, issuer),
		TestSeam:     mcpbroker.NewTestSeam(signInSeam(opts.MaxCallDuration)),
		AuthStorage:  store.MemoryStorage,
	}
	process, err := mcpbroker.NewToolHiveProcess(t.Context(), cfg)
	if err != nil {
		t.Fatalf("mcpbrokertest: start AS-2: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	if process.AS2 == nil {
		t.Fatal("mcpbrokertest: AS-2 is not set up")
	}
	mux := http.NewServeMux()
	if err := process.Handlers.Mount(mux, "/callback"); err != nil {
		t.Fatalf("mcpbrokertest: mount the process's handlers: %v", err)
	}
	return &Process{Process: process, harness: as1.Harness, mux: mux, store: store}
}
