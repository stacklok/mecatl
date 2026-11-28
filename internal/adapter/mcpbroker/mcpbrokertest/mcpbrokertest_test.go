package mcpbrokertest_test

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/mecatl/internal/adapter/mcpbroker/mcpbrokertest"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server/as1servertest"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/inprocesshttp"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// TestStartAS2RedeemsOnlyForUsersSignedInAtAS2 starts AS-2 trusting a real
// AS-1 and redeems an ID-JAG AS-1's key signs for Alice: refused until Alice
// signs in at AS-2, then redeemed for the user AS-2 recorded. Not parallel:
// StartAS2 sets an environment variable.
func TestStartAS2RedeemsOnlyForUsersSignedInAtAS2(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	a := as1servertest.Start(t, h, nil)
	alice := a.SignIn(t, as1servertest.SignIn{User: identityharness.Alice})
	signer := a.Signer
	keySet, err := json.Marshal(signer.JWKS())
	if err != nil {
		t.Fatal(err)
	}
	actorIssuer := identitybroker.ActorIssuer("agents.test")
	process := mcpbrokertest.StartAS2(t, identitybroker.BrokerClientID, keySet, mcpbrokertest.AS1{
		Issuer: a.Issuer(), JWKSURL: a.JWKSURL(), ActorIssuer: actorIssuer, Harness: h,
	})
	as2 := process.AS2
	// idJAG is Alice's access token turned into an ID-JAG for the broker
	// client at AS-2, signed with AS-1's key; it keeps the token's
	// upstream_subject, which names Alice at the company IdP.
	idJAG := a.Forge(t, alice, as1servertest.Forgery{
		Claims: func(c jwt.MapClaims) {
			now := time.Now()
			c["aud"], c["client_id"], c["jti"] = as2.Issuer, identitybroker.BrokerClientID, rand.Text()
			c["iat"], c["exp"] = now.Unix(), now.Add(5*time.Minute).Unix()
			c["resource"], c["act"] = as2.Resource, map[string]any{"iss": actorIssuer, "sub": "ag_7hq2x9"}
		},
		Header: func(h map[string]any) { h["typ"] = "oauth-id-jag+jwt" },
	}).Value()
	redeem := func() (int, map[string]any) {
		t.Helper()
		assertion, err := signer.ClientAssertion(as2.Issuer, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		resp, err := inprocesshttp.NewClient(as2.Handler(), func(*url.URL) error { return nil }).PostForm(as2.TokenEndpoint, url.Values{
			"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {idJAG},
			"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"}, "client_assertion": {assertion.Value()},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}

	if status, body := redeem(); status != http.StatusBadRequest || body["error"] != "access_denied" || body["error_description"] != "subject_unknown" {
		t.Fatalf("redemption before Alice signs in at AS-2: status %d, body %v; want 400 access_denied subject_unknown", status, body)
	}
	if n := process.Users(); n != 0 {
		t.Fatalf("AS-2 recorded %d users before any sign-in, want 0", n)
	}
	if login := process.SignIn(t, identityharness.Alice); login == "" {
		t.Fatal("SignIn returned no access token")
	}
	if n := process.Users(); n != 1 {
		t.Fatalf("AS-2 recorded %d users after Alice signed in, want 1", n)
	}
	status, body := redeem()
	if status != http.StatusOK {
		t.Fatalf("redemption after Alice signed in at AS-2: status %d, body %v", status, body)
	}
	token, _ := body["access_token"].(string)
	claims := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token, claims); err != nil {
		t.Fatal(err)
	}
	if sub, _ := claims.GetSubject(); sub == "" || sub == as1servertest.Subject(t, alice) {
		t.Fatalf("gateway token sub = %q, want AS-2's user for Alice", sub)
	}
}
