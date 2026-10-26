package identityharness_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

const (
	grantTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
	jwtBearer          = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
	tokenTypeAccess    = "urn:ietf:params:oauth:token-type:access_token"
	tokenTypeRefresh   = "urn:ietf:params:oauth:token-type:refresh_token"
)

// testClock is a settable clock safe to read from server goroutines.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock { return &testClock{now: time.Unix(1_800_000_000, 0)} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func openReq(h *identityharness.Harness, token, session, user string) identityharness.SessionOpenExchange {
	return identityharness.SessionOpenExchange{SubjectToken: token, Session: session, Owner: h.Owner(user)}
}

func aliceToken(h *identityharness.Harness) string {
	return h.AS1.MintAccessToken(identityharness.AccessTokenSpec{User: identityharness.Alice})
}

func TestSessionOpenBindsAndEchoesSessionAndOwner(t *testing.T) {
	clock := newTestClock()
	h := identityharness.New(t, identityharness.Options{Now: clock.Now})
	tokens, err := h.SignIn(t.Context(), identityharness.SignInRequest{User: identityharness.Alice})
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	resp, err := h.ScriptedBroker().OpenSession(t.Context(), openReq(h, tokens.AccessToken, "A42", identityharness.Alice))
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	want := []identityharness.SessionDetails{{Type: "mecatl_session", Session: "A42", Owner: h.Owner(identityharness.Alice)}}
	if resp.RefreshToken == "" || resp.IssuedTokenType != tokenTypeRefresh || resp.TokenType != "N_A" ||
		resp.ExpiresIn != int(identityharness.DefaultSessionRefreshTokenLifetime.Seconds()) ||
		!slices.Equal(resp.AuthorizationDetails, want) {
		t.Fatalf("response = %+v, want a refresh token in access_token, N_A, the hard expiry, and the entry echoed", resp)
	}
	_, claims := verify(t, h, tokens.AccessToken)
	grant, ok := h.AS1.SessionRefreshToken(resp.RefreshToken)
	wantGrant := identityharness.SessionGrant{
		Session: "A42", Owner: h.Owner(identityharness.Alice), ClientID: identityharness.BrokerClientID,
		Subject: h.AS1.Subject(identityharness.Alice), SourceJTI: claims["jti"].(string),
		HardExpiry: clock.Now().Add(identityharness.DefaultSessionRefreshTokenLifetime), Active: true,
	}
	if !ok || grant != wantGrant {
		t.Fatalf("grant = %+v, %v; want %+v", grant, ok, wantGrant)
	}
	if h.AS1.SessionClosed("A42") {
		t.Error("A42 is closed after its open")
	}
}

// TestSessionOpenDoesNotRotate checks the raw response: the session refresh
// token arrives in access_token, with no second refresh token, and the echo is
// the entry sent.
func TestSessionOpenDoesNotRotate(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	entry := `{"type":"mecatl_session","session":"A42","owner":{"iss":"` + h.IdP.Issuer() + `","sub":"alice-204"},"note":"kept"}`
	body, err := postToken(t, h, exchangeForm(h, aliceToken(h), "["+entry+"]"))
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if _, ok := body["refresh_token"]; ok {
		t.Errorf("response carries refresh_token: %v", body)
	}
	var sent any
	if err := json.Unmarshal([]byte("["+entry+"]"), &sent); err != nil {
		t.Fatal(err)
	}
	if got := mustJSON(t, body["authorization_details"]); got != mustJSON(t, sent) {
		t.Errorf("echo = %s, want the entry sent, %s", got, mustJSON(t, sent))
	}
}

func TestSessionOpenRefusals(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	b := h.ScriptedBroker()
	details := func(entries ...string) string { return "[" + strings.Join(entries, ",") + "]" }
	owner := `{"iss":"` + h.IdP.Issuer() + `","sub":"alice-204"}`
	good := `{"type":"mecatl_session","session":"A42","owner":` + owner + `}`

	// Positive control: the unmodified form opens a session, so each check 3
	// case below fails only for its own defect.
	if _, err := postToken(t, h, exchangeForm(h, aliceToken(h), details(good))); err != nil {
		t.Fatalf("unmodified form: %v", err)
	}

	t.Run("check 2: public client", func(t *testing.T) {
		form := exchangeForm(h, aliceToken(h), details(good))
		form.Del("client_assertion")
		form.Del("client_assertion_type")
		form.Set("client_id", identityharness.DefaultClientID)
		_, err := postToken(t, h, form)
		assertOAuthError(t, err, "unauthorized_client")
	})

	for _, c := range []struct {
		name string
		edit func(url.Values)
		want string
	}{
		{"subject token type", func(f url.Values) { f.Set("subject_token_type", "urn:ietf:params:oauth:token-type:id_token") }, "invalid_request"},
		{"requested token type", func(f url.Values) { f.Set("requested_token_type", tokenTypeAccess) }, "invalid_request"},
		{"no subject token", func(f url.Values) { f.Del("subject_token") }, "invalid_request"},
		{"actor token", func(f url.Values) { f.Set("actor_token", "svid") }, "invalid_request"},
		{"scope", func(f url.Values) { f.Set("scope", "openid") }, "invalid_scope"},
		{"no authorization_details", func(f url.Values) { f.Del("authorization_details") }, "invalid_request"},
		{"repeated parameter", func(f url.Values) { f.Add("authorization_details", details(good)) }, "invalid_request"},
		{"not JSON", func(f url.Values) { f.Set("authorization_details", "{") }, "invalid_authorization_details"},
		{"no entry", func(f url.Values) { f.Set("authorization_details", "[]") }, "invalid_authorization_details"},
		{"two entries", func(f url.Values) { f.Set("authorization_details", details(good, good)) }, "invalid_authorization_details"},
		{"other type", func(f url.Values) {
			f.Set("authorization_details", details(`{"type":"other","session":"A42","owner":`+owner+`}`))
		}, "invalid_authorization_details"},
		{"no session", func(f url.Values) {
			f.Set("authorization_details", details(`{"type":"mecatl_session","owner":`+owner+`}`))
		}, "invalid_authorization_details"},
		{"no owner subject", func(f url.Values) {
			f.Set("authorization_details", details(`{"type":"mecatl_session","session":"A42","owner":{"iss":"x"}}`))
		}, "invalid_authorization_details"},
		{"member names in another case", func(f url.Values) {
			f.Set("authorization_details", details(`{"TYPE":"mecatl_session","Session":"A42","OWNER":`+owner+`}`))
		}, "invalid_authorization_details"},
		{"owner member names in another case", func(f url.Values) {
			f.Set("authorization_details", details(`{"type":"mecatl_session","session":"A42","owner":{"ISS":"`+h.IdP.Issuer()+`","Sub":"alice-204"}}`))
		}, "invalid_authorization_details"},
		{"duplicated session", func(f url.Values) {
			f.Set("authorization_details", details(`{"type":"mecatl_session","session":"B43","session":"A42","owner":`+owner+`}`))
		}, "invalid_authorization_details"},
		{"duplicated owner subject", func(f url.Values) {
			f.Set("authorization_details", details(`{"type":"mecatl_session","session":"A42","owner":{"iss":"`+h.IdP.Issuer()+`","sub":"bob-317","sub":"alice-204"}}`))
		}, "invalid_authorization_details"},
	} {
		t.Run("check 3: "+c.name, func(t *testing.T) {
			form := exchangeForm(h, aliceToken(h), details(good))
			c.edit(form)
			_, err := postToken(t, h, form)
			assertReason(t, err, http.StatusBadRequest, c.want, "")
		})
	}

	for name, token := range map[string]string{
		"garbage":         "not-a-jwt",
		"expired":         h.AS1.MintAccessToken(identityharness.AccessTokenSpec{Lifetime: -time.Minute}),
		"foreign key":     h.AS1.MintAccessToken(identityharness.AccessTokenSpec{ForeignKey: true}),
		"other audience":  h.AS1.MintAccessToken(identityharness.AccessTokenSpec{Audience: "https://other.test/api"}),
		"other issuer":    h.AS1.MintAccessToken(identityharness.AccessTokenSpec{Issuer: h.IdP.Issuer()}),
		"typ JWT":         h.AS1.MintAccessToken(identityharness.AccessTokenSpec{Type: "JWT"}),
		"no typ":          h.AS1.MintAccessToken(identityharness.AccessTokenSpec{OmitType: true}),
		"other client":    h.AS1.MintAccessToken(identityharness.AccessTokenSpec{ClientID: "rogue"}),
		"broker's client": h.AS1.MintAccessToken(identityharness.AccessTokenSpec{ClientID: identityharness.BrokerClientID}),
		"no sub_id":       h.AS1.MintAccessToken(identityharness.AccessTokenSpec{OmitSubID: true}),
		"sub_id string":   h.AS1.MintAccessToken(identityharness.AccessTokenSpec{SubID: identityharness.Alice}),
		"sub_id format":   h.AS1.MintAccessToken(identityharness.AccessTokenSpec{SubID: map[string]string{"format": "email", "iss": h.IdP.Issuer(), "sub": identityharness.Alice}}),
	} {
		t.Run("check 4: "+name, func(t *testing.T) {
			_, err := b.OpenSession(t.Context(), openReq(h, token, "S4-"+name, identityharness.Alice))
			assertReason(t, err, http.StatusBadRequest, "invalid_request", "subject_invalid")
			if got := h.AS1.SessionOpenRefusals("S4-" + name); !slices.Equal(got, []string{"subject_invalid"}) {
				t.Errorf("recorded refusals = %v, want subject_invalid", got)
			}
		})
	}
	// RFC 9068's media-type form of typ is accepted.
	if _, err := b.OpenSession(t.Context(), openReq(h, h.AS1.MintAccessToken(identityharness.AccessTokenSpec{Type: "application/at+jwt"}), "S4-typ", identityharness.Alice)); err != nil {
		t.Errorf("application/at+jwt subject token: %v", err)
	}

	t.Run("check 5: owner mismatch", func(t *testing.T) {
		_, err := b.OpenSession(t.Context(), openReq(h, aliceToken(h), "S5", identityharness.Bob))
		assertReason(t, err, http.StatusBadRequest, "invalid_request", "owner_mismatch")
		if got := h.AS1.SessionOpenRefusals("S5"); !slices.Equal(got, []string{"owner_mismatch"}) {
			t.Errorf("recorded refusals = %v, want owner_mismatch", got)
		}
	})

	t.Run("check 6: another owner", func(t *testing.T) {
		if _, err := b.OpenSession(t.Context(), openReq(h, aliceToken(h), "S6", identityharness.Alice)); err != nil {
			t.Fatalf("Alice's open: %v", err)
		}
		bob := h.AS1.MintAccessToken(identityharness.AccessTokenSpec{User: identityharness.Bob})
		_, err := b.OpenSession(t.Context(), openReq(h, bob, "S6", identityharness.Bob))
		assertReason(t, err, http.StatusBadRequest, "invalid_authorization_details", "session_owned")
		if got := h.AS1.SessionOpenRefusals("S6"); !slices.Equal(got, []string{"session_owned"}) {
			t.Errorf("recorded refusals = %v, want session_owned", got)
		}
	})

	t.Run("check 7: token used", func(t *testing.T) {
		token := aliceToken(h)
		if _, err := b.OpenSession(t.Context(), openReq(h, token, "S7a", identityharness.Alice)); err != nil {
			t.Fatalf("first open: %v", err)
		}
		_, err := b.OpenSession(t.Context(), openReq(h, token, "S7b", identityharness.Alice))
		assertReason(t, err, http.StatusBadRequest, "invalid_request", "token_used")
		if h.AS1.SessionRefreshTokensIssued("S7b") != 0 {
			t.Error("a used token opened a second session")
		}
		if got := h.AS1.SessionOpenRefusals("S7b"); !slices.Equal(got, []string{"token_used"}) {
			t.Errorf("recorded refusals = %v, want token_used", got)
		}
	})
}

// TestSessionOpenLostResponseRetry: AS-1 processes the open but the broker
// never sees the response; resending the same subject token and entry with a
// new assertion is accepted and replaces the orphan.
func TestSessionOpenLostResponseRetry(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	b := h.ScriptedBroker()
	token := aliceToken(h)
	h.AS1.LoseNextSessionOpenResponse()
	_, err := b.OpenSession(t.Context(), openReq(h, token, "A42", identityharness.Alice))
	var oauthErr *identityharness.OAuthError
	if err == nil || errors.As(err, &oauthErr) {
		t.Fatalf("OpenSession = %v, want a lost response", err)
	}
	if h.AS1.SessionRefreshTokensIssued("A42") != 1 {
		t.Fatal("AS-1 did not process the lost open")
	}
	resp, err := b.OpenSession(t.Context(), openReq(h, token, "A42", identityharness.Alice))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if grant, _ := h.AS1.SessionRefreshToken(resp.RefreshToken); !grant.Active || h.AS1.SessionRefreshTokensIssued("A42") != 2 {
		t.Errorf("after retry: grant %+v, %d issued; want an active replacement of the orphan", grant, h.AS1.SessionRefreshTokensIssued("A42"))
	}
}

func TestLaterOpenReplacesRefreshTokenWithoutExtendingHardExpiry(t *testing.T) {
	clock := newTestClock()
	h := identityharness.New(t, identityharness.Options{Now: clock.Now})
	b := h.ScriptedBroker()
	first, err := b.OpenSession(t.Context(), openReq(h, aliceToken(h), "A42", identityharness.Alice))
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	clock.Advance(time.Hour)
	second, err := b.OpenSession(t.Context(), openReq(h, aliceToken(h), "A42", identityharness.Alice))
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	old, _ := h.AS1.SessionRefreshToken(first.RefreshToken)
	current, _ := h.AS1.SessionRefreshToken(second.RefreshToken)
	if old.Active || old.Reason != "superseded" || !current.Active {
		t.Errorf("old %+v, current %+v; want the old superseded and the new active", old, current)
	}
	if current.HardExpiry != old.HardExpiry || second.ExpiresIn != first.ExpiresIn-3600 {
		t.Errorf("hard expiry moved: %v -> %v, expires_in %d -> %d", old.HardExpiry, current.HardExpiry, first.ExpiresIn, second.ExpiresIn)
	}
}

func TestSessionRefreshTokenRefusedAtRefreshGrant(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	resp, err := h.ScriptedBroker().OpenSession(t.Context(), openReq(h, aliceToken(h), "A42", identityharness.Alice))
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	_, err = postToken(t, h, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {resp.RefreshToken},
		"client_assertion_type": {jwtBearer},
		"client_assertion":      {h.ScriptedBroker().Assertion(identityharness.ClientAssertionSpec{})},
	})
	assertOAuthError(t, err, "invalid_grant")
	_, err = h.Refresh(t.Context(), identityharness.DefaultClientID, resp.RefreshToken)
	assertOAuthError(t, err, "invalid_grant")
	if grant, _ := h.AS1.SessionRefreshToken(resp.RefreshToken); !grant.Active {
		t.Error("presenting the token at the refresh grant revoked it")
	}
}

func TestRevocationClosesSession(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	b := h.ScriptedBroker()
	first, err := b.OpenSession(t.Context(), openReq(h, aliceToken(h), "A42", identityharness.Alice))
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	second, err := b.OpenSession(t.Context(), openReq(h, aliceToken(h), "A42", identityharness.Alice))
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	// A replaced token or an unknown value is answered 200 and changes nothing.
	for _, token := range []string{first.RefreshToken, "not-a-token"} {
		if err := b.Revoke(t.Context(), token); err != nil {
			t.Fatalf("Revoke(%q): %v", token, err)
		}
	}
	if h.AS1.SessionClosed("A42") {
		t.Fatal("revoking a replaced token closed the session")
	}

	resp, err := h.HTTPClient().PostForm(h.AS1.RevocationEndpoint(), url.Values{"token": {second.RefreshToken}, "client_id": {identityharness.DefaultClientID}})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("revocation by a public client: status %d, want 401", resp.StatusCode)
	}

	if err := b.Revoke(t.Context(), second.RefreshToken); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if grant, _ := h.AS1.SessionRefreshToken(second.RefreshToken); grant.Active || grant.Reason != "session_closed" || !h.AS1.SessionClosed("A42") {
		t.Fatalf("after revocation: grant %+v, closed %v; want session_closed", grant, h.AS1.SessionClosed("A42"))
	}
	_, err = b.OpenSession(t.Context(), openReq(h, aliceToken(h), "A42", identityharness.Alice))
	assertReason(t, err, http.StatusBadRequest, "invalid_authorization_details", "session_owned")
}

func TestHardExpiryClosesSession(t *testing.T) {
	clock := newTestClock()
	h := identityharness.New(t, identityharness.Options{Now: clock.Now, SessionRefreshTokenLifetime: time.Hour})
	b := h.ScriptedBroker()
	byClock, err := b.OpenSession(t.Context(), openReq(h, aliceToken(h), "A42", identityharness.Alice))
	if err != nil {
		t.Fatalf("open A42: %v", err)
	}
	forced, err := b.OpenSession(t.Context(), openReq(h, aliceToken(h), "B43", identityharness.Alice))
	if err != nil {
		t.Fatalf("open B43: %v", err)
	}
	if byClock.ExpiresIn != 3600 {
		t.Errorf("expires_in = %d, want the configured hard expiry 3600", byClock.ExpiresIn)
	}

	h.AS1.ExpireSession("B43")
	if grant, _ := h.AS1.SessionRefreshToken(forced.RefreshToken); grant.Reason != "session_closed" || !h.AS1.SessionClosed("B43") {
		t.Errorf("forced expiry: grant %+v, want session_closed", grant)
	}
	if grant, _ := h.AS1.SessionRefreshToken(byClock.RefreshToken); !grant.Active {
		t.Fatalf("A42 grant %+v before its hard expiry, want active", grant)
	}
	clock.Advance(time.Hour)
	if grant, _ := h.AS1.SessionRefreshToken(byClock.RefreshToken); grant.Reason != "session_closed" || !h.AS1.SessionClosed("A42") {
		t.Errorf("at the hard expiry: grant %+v, want session_closed", grant)
	}
	for _, session := range []string{"A42", "B43"} {
		_, err := b.OpenSession(t.Context(), openReq(h, aliceToken(h), session, identityharness.Alice))
		assertReason(t, err, http.StatusBadRequest, "invalid_authorization_details", "session_owned")
	}
}

func TestSessionOpenFaults(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	b := h.ScriptedBroker()

	h.AS1.FailNextSessionOpen(identityharness.Fault{Status: http.StatusServiceUnavailable, Error: "temporarily_unavailable"})
	_, err := b.OpenSession(t.Context(), openReq(h, aliceToken(h), "A42", identityharness.Alice))
	assertReason(t, err, http.StatusServiceUnavailable, "temporarily_unavailable", "")
	if h.AS1.SessionRefreshTokensIssued("A42") != 0 {
		t.Fatal("a failed open issued a refresh token")
	}

	// Each fault fires once.
	if _, err := b.OpenSession(t.Context(), openReq(h, aliceToken(h), "A42", identityharness.Alice)); err != nil {
		t.Fatalf("open after the faults: %v", err)
	}

	// A mismatched echo still binds the token to the entry sent; the broker
	// revokes it, which closes the session.
	wrong := identityharness.SessionDetails{Type: "mecatl_session", Session: "B43", Owner: h.Owner(identityharness.Bob)}
	h.AS1.MismatchNextSessionOpenEcho(wrong)
	resp, err := b.OpenSession(t.Context(), openReq(h, aliceToken(h), "C44", identityharness.Alice))
	if err != nil {
		t.Fatalf("open with mismatched echo: %v", err)
	}
	if !slices.Equal(resp.AuthorizationDetails, []identityharness.SessionDetails{wrong}) {
		t.Fatalf("echo = %+v, want the forced mismatch", resp.AuthorizationDetails)
	}
	if grant, _ := h.AS1.SessionRefreshToken(resp.RefreshToken); grant.Session != "C44" || grant.Owner != h.Owner(identityharness.Alice) {
		t.Errorf("grant = %+v, want it bound to C44 and Alice", grant)
	}
	if err := b.Revoke(t.Context(), resp.RefreshToken); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if !h.AS1.SessionClosed("C44") {
		t.Error("revoking the mismatched open's token left C44 open")
	}
}

func TestDiscoveryAdvertisesBrokerSupport(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	var doc struct {
		RevocationEndpoint string   `json:"revocation_endpoint"`
		GrantTypes         []string `json:"grant_types_supported"`
		AuthMethods        []string `json:"token_endpoint_auth_methods_supported"`
		DetailsTypes       []string `json:"authorization_details_types_supported"`
	}
	getJSON(t, h, h.AS1.Issuer()+"/.well-known/openid-configuration", &doc)
	if doc.RevocationEndpoint != h.AS1.RevocationEndpoint() ||
		!slices.Contains(doc.GrantTypes, grantTokenExchange) || !slices.Contains(doc.GrantTypes, "authorization_code") ||
		!slices.Contains(doc.AuthMethods, "none") || !slices.Contains(doc.AuthMethods, "private_key_jwt") ||
		!slices.Equal(doc.DetailsTypes, []string{"mecatl_session"}) {
		t.Errorf("discovery = %+v, want revocation, token exchange, none and private_key_jwt, mecatl_session", doc)
	}
}

// exchangeForm is a valid session-open form, authenticated as the broker.
func exchangeForm(h *identityharness.Harness, token, details string) url.Values {
	return url.Values{
		"grant_type":            {grantTokenExchange},
		"subject_token":         {token},
		"subject_token_type":    {tokenTypeAccess},
		"requested_token_type":  {tokenTypeRefresh},
		"authorization_details": {details},
		"client_assertion_type": {jwtBearer},
		"client_assertion":      {h.ScriptedBroker().Assertion(identityharness.ClientAssertionSpec{})},
	}
}

// assertReason checks an OAuth error's status, code, and the reason that
// starts its description.
func assertReason(t *testing.T, err error, status int, code, reason string) {
	t.Helper()
	var oauthErr *identityharness.OAuthError
	if !errors.As(err, &oauthErr) || oauthErr.Status != status || oauthErr.Code != code || oauthErr.Description != reason {
		t.Fatalf("error = %v (%+v), want %d %s with reason %q", err, oauthErr, status, code, reason)
	}
}
