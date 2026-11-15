package identityharness_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

const (
	tokenTypeJWT   = "urn:ietf:params:oauth:token-type:jwt"
	testAS2Issuer  = "https://gateway.example/v1/mcp/broker"
	testAS2VMCP    = testAS2Issuer + "/mcp"
	testAgentID    = "ag_7hq2x9"
	svidAlice      = "svid-alice"
	svidExpired    = "svid-expired"
	svidForeign    = "svid-foreign"
	svidValidatorX = "svid-validator-fails"
)

// idjagFixture is a harness whose AS-1 issues ID-JAGs for one AS-2 target,
// with a grant for Alice and a validator that accepts svidAlice for her.
type idjagFixture struct {
	h     *identityharness.Harness
	clock *testClock
	grant string
	// actorExpiry is the expiry the validator reports, relative to the clock.
	actorExpiry time.Duration
}

func newIDJAGFixture(t *testing.T, opts identityharness.Options) *idjagFixture {
	t.Helper()
	f := &idjagFixture{clock: newTestClock(), actorExpiry: time.Hour}
	opts.Now = f.clock.Now
	f.h = identityharness.New(t, opts)
	f.h.AS1.AddIDJAGTarget(testAS2Issuer, testAS2VMCP)
	f.h.AS1.SetActorValidator(f.validate)
	f.grant = f.openGrant(t, identityharness.Alice)
	return f
}

func (f *idjagFixture) validate(_ context.Context, owner identityharness.Owner, actor string) (string, time.Time, error) {
	switch {
	case actor == svidExpired:
		return "", time.Time{}, identityharness.ErrActorExpired
	case actor == svidForeign || owner != f.h.Owner(identityharness.Alice):
		return "", time.Time{}, identityharness.ErrActorDenied
	case actor == svidValidatorX:
		return "", time.Time{}, errors.New("registry unavailable")
	}
	return testAgentID, f.clock.Now().Add(f.actorExpiry), nil
}

// openGrant opens a session for user and returns the grant's refresh token.
func (f *idjagFixture) openGrant(t *testing.T, user string) string {
	t.Helper()
	resp, err := f.h.ScriptedBroker().OpenSession(t.Context(), openReq(f.h.AS1.MintAccessToken(identityharness.AccessTokenSpec{User: user})))
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	return resp.RefreshToken
}

// idjagForm is a valid ID-JAG exchange form, authenticated as the broker.
func idjagForm(h *identityharness.Harness, grant, actor string) url.Values {
	return url.Values{
		"grant_type":            {grantTokenExchange},
		"subject_token":         {grant},
		"subject_token_type":    {tokenTypeRefresh},
		"actor_token":           {actor},
		"actor_token_type":      {tokenTypeJWT},
		"requested_token_type":  {tokenTypeIDJAG},
		"audience":              {testAS2Issuer},
		"resource":              {testAS2VMCP},
		"mecatl_operation":      {"op-1"},
		"client_assertion_type": {jwtBearer},
		"client_assertion":      {h.ScriptedBroker().Assertion(identityharness.ClientAssertionSpec{})},
	}
}

func TestIDJAGExchangeChecks(t *testing.T) {
	set := func(name, value string) func(*testing.T, *idjagFixture, url.Values) {
		return func(_ *testing.T, _ *idjagFixture, form url.Values) { form.Set(name, value) }
	}
	del := func(name string) func(*testing.T, *idjagFixture, url.Values) {
		return func(_ *testing.T, _ *idjagFixture, form url.Values) { form.Del(name) }
	}
	for _, tt := range []struct {
		name   string
		edit   func(*testing.T, *idjagFixture, url.Values)
		status int
		code   string
		reason string
	}{
		// Check 2: the form.
		{"subject token type", set("subject_token_type", tokenTypeAccess), 400, "invalid_request", ""},
		{"actor token type", set("actor_token_type", tokenTypeAccess), 400, "invalid_request", ""},
		{"no actor token", del("actor_token"), 400, "invalid_request", ""},
		{"no subject token", del("subject_token"), 400, "invalid_request", ""},
		{"no audience", del("audience"), 400, "invalid_request", ""},
		{"no resource", del("resource"), 400, "invalid_request", ""},
		{"scope", set("scope", "openid"), 400, "invalid_request", ""},
		{"authorization_details", set("authorization_details", "[]"), 400, "invalid_request", ""},
		{"two audiences", func(_ *testing.T, _ *idjagFixture, form url.Values) { form.Add("audience", testAS2Issuer) }, 400, "invalid_request", ""},
		// Check 3: the target.
		{"unknown audience", set("audience", "https://other.example"), 400, "invalid_target", ""},
		{"unknown resource", set("resource", testAS2Issuer), 400, "invalid_target", ""},
		// Check 4: the grant.
		{"unknown grant", set("subject_token", "as1-grant-rt-unknown"), 400, "invalid_request", "grant_invalid"},
		{"expired grant", func(_ *testing.T, f *idjagFixture, form url.Values) {
			f.clock.Advance(identityharness.DefaultGrantMaxLifetime)
			form.Set("client_assertion", f.h.ScriptedBroker().Assertion(identityharness.ClientAssertionSpec{}))
		}, 400, "invalid_request", "grant_invalid"},
		{"revoked grant", func(t *testing.T, f *idjagFixture, _ url.Values) {
			if err := f.h.ScriptedBroker().Revoke(t.Context(), f.grant); err != nil {
				t.Fatal(err)
			}
		}, 400, "invalid_request", "revoked"},
		// Check 6: the actor, through the validator.
		{"expired actor", set("actor_token", svidExpired), 400, "invalid_request", "svid_expired"},
		{"foreign actor", set("actor_token", svidForeign), 400, "invalid_request", "denied"},
		{"validator failure", set("actor_token", svidValidatorX), 503, "server_error", ""},
		{"no validator", func(_ *testing.T, f *idjagFixture, _ url.Values) { f.h.AS1.SetActorValidator(nil) }, 400, "invalid_request", "denied"},
		{"actor at its expiry", func(_ *testing.T, f *idjagFixture, _ url.Values) { f.actorExpiry = 0 }, 400, "invalid_request", "svid_expired"},
		{"another user's grant", func(t *testing.T, f *idjagFixture, form url.Values) {
			form.Set("subject_token", f.openGrant(t, identityharness.Bob))
		}, 400, "invalid_request", "denied"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newIDJAGFixture(t, identityharness.Options{})
			form := idjagForm(f.h, f.grant, svidAlice)
			tt.edit(t, f, form)
			_, err := postToken(t, f.h, form)
			assertReason(t, err, tt.status, tt.code, tt.reason)
			if got := f.h.AS1.IDJAGsIssued(); len(got) != 0 {
				t.Fatalf("AS-1 issued %d ID-JAGs on a refusal", len(got))
			}
		})
	}
}

func TestIDJAGShape(t *testing.T) {
	f := newIDJAGFixture(t, identityharness.Options{})
	now := f.clock.Now()
	body, err := postToken(t, f.h, idjagForm(f.h, f.grant, svidAlice))
	if err != nil {
		t.Fatalf("ID-JAG exchange: %v", err)
	}
	if body["issued_token_type"] != tokenTypeIDJAG || body["token_type"] != "N_A" || body["expires_in"] != float64(120) {
		t.Fatalf("response = %v, want an id-jag, N_A, and expires_in 120", body)
	}
	if _, ok := body["refresh_token"]; ok {
		t.Fatal("the ID-JAG came with a refresh token")
	}
	token, _ := body["access_token"].(string)
	header, claims := verify(t, f.h, token)
	if want := map[string]any{"typ": identityharness.IDJAGHeaderType, "alg": "ES256", "kid": "as1-key-1"}; !reflect.DeepEqual(header, want) {
		t.Fatalf("header = %v, want %v", header, want)
	}
	jti, _ := claims["jti"].(string)
	want := map[string]any{
		"iss": f.h.AS1.Issuer(), "sub": f.h.AS1.Subject(identityharness.Alice), "aud": testAS2Issuer,
		"resource": testAS2VMCP, "client_id": identityharness.BrokerClientID,
		"act": map[string]any{"iss": f.h.AS1.Issuer(), "sub": testAgentID},
		"jti": jti, "iat": float64(now.Unix()), "exp": float64(now.Add(identityharness.DefaultIDJAGLifetime).Unix()),
	}
	if !reflect.DeepEqual(map[string]any(claims), want) || !strings.HasPrefix(jti, "jag-") {
		t.Fatalf("claims = %v, want %v with a jag- jti", claims, want)
	}
	issued := f.h.AS1.IDJAGsIssued()
	wantIssued := identityharness.IDJAGIssuance{
		JTI: jti, Subject: f.h.AS1.Subject(identityharness.Alice), Owner: f.h.Owner(identityharness.Alice),
		AgentID: testAgentID, Audience: testAS2Issuer, Resource: testAS2VMCP, Operation: "op-1",
		ExpiresAt: now.Add(identityharness.DefaultIDJAGLifetime),
	}
	if len(issued) != 1 || issued[0] != wantIssued {
		t.Fatalf("issued = %+v, want [%+v]", issued, wantIssued)
	}
	// Every exchange issues a new ID-JAG; the grant is not used up.
	again, err := postToken(t, f.h, idjagForm(f.h, f.grant, svidAlice))
	if err != nil || again["access_token"] == token {
		t.Fatalf("second exchange = %v, %v; want a new ID-JAG", again["access_token"] != nil, err)
	}
	if issued := f.h.AS1.IDJAGsIssued(); len(issued) != 2 || issued[1].JTI == jti {
		t.Fatalf("issued = %+v, want two ID-JAGs with distinct jtis", issued)
	}
}

func TestIDJAGExpCappedAtActor(t *testing.T) {
	for _, tt := range []struct {
		name     string
		lifetime time.Duration
		actor    time.Duration
		want     time.Duration
	}{
		{"actor expires first", 0, 30 * time.Second, 30 * time.Second},
		{"configured lifetime first", 10 * time.Second, time.Hour, 10 * time.Second},
		{"default lifetime first", 0, time.Hour, identityharness.DefaultIDJAGLifetime},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newIDJAGFixture(t, identityharness.Options{IDJAGLifetime: tt.lifetime})
			f.actorExpiry = tt.actor
			body, err := postToken(t, f.h, idjagForm(f.h, f.grant, svidAlice))
			if err != nil {
				t.Fatalf("ID-JAG exchange: %v", err)
			}
			_, claims := verify(t, f.h, body["access_token"].(string))
			exp := f.clock.Now().Add(tt.want)
			if claims["exp"] != float64(exp.Unix()) || body["expires_in"] != tt.want.Seconds() {
				t.Fatalf("exp %v, expires_in %v; want %d and %v", claims["exp"], body["expires_in"], exp.Unix(), tt.want.Seconds())
			}
			if issued := f.h.AS1.IDJAGsIssued(); len(issued) != 1 || !issued[0].ExpiresAt.Equal(exp) {
				t.Fatalf("issued = %+v, want one expiring at %v", issued, exp)
			}
		})
	}
}

func TestRevokeGrants(t *testing.T) {
	f := newIDJAGFixture(t, identityharness.Options{})
	second := f.openGrant(t, identityharness.Alice)
	bob := f.openGrant(t, identityharness.Bob)
	if n := f.h.AS1.RevokeGrants(identityharness.Alice); n != 2 {
		t.Fatalf("RevokeGrants = %d, want Alice's 2", n)
	}
	if n := f.h.AS1.RevokeGrants(identityharness.Alice); n != 0 {
		t.Fatalf("RevokeGrants again = %d, want 0", n)
	}
	for _, grant := range []string{f.grant, second} {
		if g, _ := f.h.AS1.Grant(grant); g.Active || g.Reason != "revoked" {
			t.Fatalf("Alice's grant = %+v, want revoked", g)
		}
		_, err := postToken(t, f.h, idjagForm(f.h, grant, svidAlice))
		assertReason(t, err, http.StatusBadRequest, "invalid_request", "revoked")
	}
	if g, _ := f.h.AS1.Grant(bob); !g.Active {
		t.Fatalf("Bob's grant = %+v, want active", g)
	}
}

func TestSessionOpenIgnoresIDJAGParameters(t *testing.T) {
	// A session-open exchange still refuses an actor token: only an ID-JAG
	// request reaches the ID-JAG exchange.
	f := newIDJAGFixture(t, identityharness.Options{})
	form := exchangeForm(f.h, aliceToken(f.h), `[{"type":"`+exchangeGrantType+`","issuable_token_types":["`+tokenTypeIDJAG+`"]}]`)
	form.Set("actor_token", svidAlice)
	form.Set("actor_token_type", tokenTypeJWT)
	_, err := postToken(t, f.h, form)
	assertReason(t, err, http.StatusBadRequest, "invalid_request", "")
}
