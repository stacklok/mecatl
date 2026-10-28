package identityharness_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
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
	tokenTypeIDJAG     = "urn:ietf:params:oauth:token-type:id-jag"
	exchangeGrantType  = "https://toolhive.dev/exchange_grant"
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

func openReq(token string) identityharness.SessionOpenExchange {
	return identityharness.SessionOpenExchange{SubjectToken: token}
}

func aliceToken(h *identityharness.Harness) string {
	return h.AS1.MintAccessToken(identityharness.AccessTokenSpec{User: identityharness.Alice})
}

// grantEntry is the exchange-grant entry listing only id-jag, with
// expiresAt when nonzero.
func grantEntry(expiresAt int64) identityharness.ExchangeGrant {
	return identityharness.ExchangeGrant{Type: exchangeGrantType, IssuableTokenTypes: []string{tokenTypeIDJAG}, ExpiresAt: expiresAt}
}

func TestSessionOpenIssuesAGenericGrant(t *testing.T) {
	clock := newTestClock()
	h := identityharness.New(t, identityharness.Options{Now: clock.Now})
	tokens, err := h.SignIn(t.Context(), identityharness.SignInRequest{User: identityharness.Alice})
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	resp, err := h.ScriptedBroker().OpenSession(t.Context(), openReq(tokens.AccessToken))
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	expires := clock.Now().Add(identityharness.DefaultGrantMaxLifetime)
	want := []identityharness.ExchangeGrant{grantEntry(expires.Unix())}
	if resp.RefreshToken == "" || resp.IssuedTokenType != tokenTypeRefresh || resp.TokenType != "N_A" ||
		resp.ExpiresIn != int(identityharness.DefaultGrantMaxLifetime.Seconds()) ||
		!reflect.DeepEqual(resp.AuthorizationDetails, want) {
		t.Fatalf("response = %+v, want a refresh token in access_token, N_A, AS-1's maximum, and the entry echoed with it", resp)
	}
	_, claims := verify(t, h, tokens.AccessToken)
	grant, ok := h.AS1.Grant(resp.RefreshToken)
	wantGrant := identityharness.Grant{
		ClientID: identityharness.BrokerClientID, Subject: h.AS1.Subject(identityharness.Alice),
		SubID: h.Owner(identityharness.Alice), SourceJTI: claims["jti"].(string),
		Output: tokenTypeIDJAG, ExpiresAt: expires, Active: true,
	}
	if !ok || grant != wantGrant {
		t.Fatalf("grant = %+v, %v; want %+v", grant, ok, wantGrant)
	}
}

// TestSessionOpenDoesNotRotate checks the raw response: the grant's refresh
// token arrives in access_token, with no second refresh token, and the echo
// is exactly the entry with the granted expiry, naming no session or owner.
func TestSessionOpenDoesNotRotate(t *testing.T) {
	clock := newTestClock()
	h := identityharness.New(t, identityharness.Options{Now: clock.Now})
	requested := clock.Now().Add(time.Hour).Unix()
	entry := `{"type":"` + exchangeGrantType + `","issuable_token_types":["` + tokenTypeIDJAG + `"],"expires_at":` + strconv.FormatInt(requested, 10) + `}`
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

func TestSessionOpenCapsExpiresAt(t *testing.T) {
	clock := newTestClock()
	h := identityharness.New(t, identityharness.Options{Now: clock.Now, GrantMaxLifetime: time.Hour})
	b := h.ScriptedBroker()
	maxExpiry := clock.Now().Add(time.Hour).Unix()
	for _, tt := range []struct {
		name               string
		requested, granted int64
	}{
		{"absent: AS-1's maximum", 0, maxExpiry},
		{"below the maximum: kept", maxExpiry - 600, maxExpiry - 600},
		{"at the maximum", maxExpiry, maxExpiry},
		{"above the maximum: capped", maxExpiry + 3600, maxExpiry},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := b.OpenSession(t.Context(), identityharness.SessionOpenExchange{SubjectToken: aliceToken(h), ExpiresAt: tt.requested})
			if err != nil {
				t.Fatalf("OpenSession: %v", err)
			}
			grant, _ := h.AS1.Grant(resp.RefreshToken)
			if !reflect.DeepEqual(resp.AuthorizationDetails, []identityharness.ExchangeGrant{grantEntry(tt.granted)}) ||
				resp.ExpiresIn != int(tt.granted-clock.Now().Unix()) || grant.ExpiresAt.Unix() != tt.granted {
				t.Fatalf("echo %+v, expires_in %d, grant expiry %v; want %d", resp.AuthorizationDetails, resp.ExpiresIn, grant.ExpiresAt, tt.granted)
			}
		})
	}
}

func TestSessionOpenRefusals(t *testing.T) {
	clock := newTestClock()
	h := identityharness.New(t, identityharness.Options{Now: clock.Now})
	b := h.ScriptedBroker()
	details := func(entries ...string) string { return "[" + strings.Join(entries, ",") + "]" }
	member := func(name, value string) string { return `,"` + name + `":` + value }
	entry := func(extra string) string {
		return `{"type":"` + exchangeGrantType + `","issuable_token_types":["` + tokenTypeIDJAG + `"]` + extra + `}`
	}
	good := entry("")

	// Positive control: the unmodified form converts, so each check 3 case
	// below fails only for its own defect.
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

	past := strconv.FormatInt(clock.Now().Unix(), 10)
	for _, c := range []struct {
		name         string
		edit         func(url.Values)
		code, reason string
	}{
		{"subject token type", func(f url.Values) { f.Set("subject_token_type", "urn:ietf:params:oauth:token-type:id_token") }, "invalid_request", ""},
		{"requested token type", func(f url.Values) { f.Set("requested_token_type", tokenTypeAccess) }, "invalid_request", ""},
		{"no subject token", func(f url.Values) { f.Del("subject_token") }, "invalid_request", ""},
		{"actor token", func(f url.Values) { f.Set("actor_token", "svid") }, "invalid_request", ""},
		{"scope", func(f url.Values) { f.Set("scope", "openid") }, "invalid_scope", ""},
		{"resource", func(f url.Values) { f.Set("resource", h.AS1.Audience()) }, "invalid_target", ""},
		{"audience", func(f url.Values) { f.Set("audience", h.AS1.Audience()) }, "invalid_target", ""},
		{"no authorization_details", func(f url.Values) { f.Del("authorization_details") }, "invalid_request", ""},
		{"repeated parameter", func(f url.Values) { f.Add("authorization_details", details(good)) }, "invalid_request", ""},
		{"not JSON", func(f url.Values) { f.Set("authorization_details", "{") }, "invalid_authorization_details", "malformed_entry"},
		{"no entry", func(f url.Values) { f.Set("authorization_details", "[]") }, "invalid_authorization_details", "single_entry_required"},
		{"two entries", func(f url.Values) { f.Set("authorization_details", details(good, good)) }, "invalid_authorization_details", "single_entry_required"},
		{"other type", func(f url.Values) {
			f.Set("authorization_details", details(`{"type":"mecatl_session","issuable_token_types":["`+tokenTypeIDJAG+`"]}`))
		}, "invalid_authorization_details", "malformed_entry"},
		{"session member", func(f url.Values) { f.Set("authorization_details", details(entry(member("session", `"A42"`)))) }, "invalid_authorization_details", "malformed_entry"},
		{"type in another case", func(f url.Values) {
			f.Set("authorization_details", details(`{"TYPE":"`+exchangeGrantType+`","issuable_token_types":["`+tokenTypeIDJAG+`"]}`))
		}, "invalid_authorization_details", "malformed_entry"},
		{"duplicated member", func(f url.Values) {
			f.Set("authorization_details", details(entry(member("issuable_token_types", `["`+tokenTypeIDJAG+`"]`))))
		}, "invalid_authorization_details", "malformed_entry"},
		{"no issuable token types", func(f url.Values) { f.Set("authorization_details", details(`{"type":"`+exchangeGrantType+`"}`)) }, "invalid_authorization_details", "invalid_issuable_token_types"},
		{"two issuable token types", func(f url.Values) {
			f.Set("authorization_details", details(`{"type":"`+exchangeGrantType+`","issuable_token_types":["`+tokenTypeIDJAG+`","`+tokenTypeIDJAG+`"]}`))
		}, "invalid_authorization_details", "invalid_issuable_token_types"},
		{"unknown output", func(f url.Values) {
			f.Set("authorization_details", details(`{"type":"`+exchangeGrantType+`","issuable_token_types":["`+tokenTypeAccess+`"]}`))
		}, "invalid_authorization_details", "invalid_issuable_token_types"},
		{"refresh token output", func(f url.Values) {
			f.Set("authorization_details", details(`{"type":"`+exchangeGrantType+`","issuable_token_types":["`+tokenTypeRefresh+`"]}`))
		}, "invalid_authorization_details", "token_type_not_permitted"},
		{"expires_at now", func(f url.Values) { f.Set("authorization_details", details(entry(member("expires_at", past)))) }, "invalid_authorization_details", "invalid_expires_at"},
		{"expires_at string", func(f url.Values) { f.Set("authorization_details", details(entry(member("expires_at", `"`+past+`"`)))) }, "invalid_authorization_details", "invalid_expires_at"},
		{"expires_at fraction", func(f url.Values) {
			f.Set("authorization_details", details(entry(member("expires_at", "1900000000.5"))))
		}, "invalid_authorization_details", "invalid_expires_at"},
	} {
		t.Run("check 3: "+c.name, func(t *testing.T) {
			form := exchangeForm(h, aliceToken(h), details(good))
			c.edit(form)
			_, err := postToken(t, h, form)
			assertReason(t, err, http.StatusBadRequest, c.code, c.reason)
		})
	}
	if got := h.AS1.ExchangeRefusals(); len(got) != 0 {
		t.Fatalf("malformed requests were recorded: %v", got)
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
			before := len(h.AS1.ExchangeRefusals())
			_, err := b.OpenSession(t.Context(), openReq(token))
			assertReason(t, err, http.StatusBadRequest, "invalid_request", "")
			if got := h.AS1.ExchangeRefusals()[before:]; !slices.Equal(got, []identityharness.Refusal{{Code: "invalid_request"}}) {
				t.Errorf("recorded refusals = %v, want one invalid_request with no reason", got)
			}
		})
	}
	// RFC 9068's media-type form of typ is accepted.
	if _, err := b.OpenSession(t.Context(), openReq(h.AS1.MintAccessToken(identityharness.AccessTokenSpec{Type: "application/at+jwt"}))); err != nil {
		t.Errorf("application/at+jwt subject token: %v", err)
	}

	t.Run("check 5: token used", func(t *testing.T) {
		token := aliceToken(h)
		if _, err := b.OpenSession(t.Context(), openReq(token)); err != nil {
			t.Fatalf("first exchange: %v", err)
		}
		issued, before := h.AS1.GrantsIssued(), len(h.AS1.ExchangeRefusals())
		// A later expires_at makes the request differ; the jti still decides.
		_, err := b.OpenSession(t.Context(), identityharness.SessionOpenExchange{SubjectToken: token, ExpiresAt: clock.Now().Add(time.Hour).Unix()})
		assertReason(t, err, http.StatusBadRequest, "invalid_request", "token_used")
		if h.AS1.GrantsIssued() != issued {
			t.Error("a used token was converted again")
		}
		if got := h.AS1.ExchangeRefusals()[before:]; !slices.Equal(got, []identityharness.Refusal{{Code: "invalid_request", Reason: "token_used"}}) {
			t.Errorf("recorded refusals = %v, want token_used", got)
		}
	})
}

// TestSessionOpenLostResponseIsNotRetried: AS-1 converts the token but the
// broker never sees the response. Resending the same access token is refused
// with token_used, even with a new assertion; the orphan grant stays valid,
// and a fresh access token converts.
func TestSessionOpenLostResponseIsNotRetried(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	b := h.ScriptedBroker()
	token := aliceToken(h)
	h.AS1.LoseNextExchangeResponse()
	_, err := b.OpenSession(t.Context(), openReq(token))
	var oauthErr *identityharness.OAuthError
	if err == nil || errors.As(err, &oauthErr) {
		t.Fatalf("OpenSession = %v, want a lost response", err)
	}
	if h.AS1.GrantsIssued() != 1 {
		t.Fatal("AS-1 did not process the lost exchange")
	}
	_, err = b.OpenSession(t.Context(), openReq(token))
	assertReason(t, err, http.StatusBadRequest, "invalid_request", "token_used")
	if h.AS1.GrantsIssued() != 1 {
		t.Fatal("the retry with the same token was converted")
	}
	resp, err := b.OpenSession(t.Context(), openReq(aliceToken(h)))
	if err != nil {
		t.Fatalf("exchange with a fresh token: %v", err)
	}
	if grant, _ := h.AS1.Grant(resp.RefreshToken); !grant.Active || h.AS1.GrantsIssued() != 2 {
		t.Errorf("after a fresh token: grant %+v, %d issued; want a second active grant beside the orphan", grant, h.AS1.GrantsIssued())
	}
}

// TestLaterExchangeDoesNotTouchEarlierGrants: AS-1 keeps no per-session
// state, so a second exchange for the same user is a second grant, and the
// first stays active until the broker revokes it.
func TestLaterExchangeDoesNotTouchEarlierGrants(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	b := h.ScriptedBroker()
	first, err := b.OpenSession(t.Context(), openReq(aliceToken(h)))
	if err != nil {
		t.Fatalf("first exchange: %v", err)
	}
	second, err := b.OpenSession(t.Context(), openReq(aliceToken(h)))
	if err != nil {
		t.Fatalf("second exchange: %v", err)
	}
	old, _ := h.AS1.Grant(first.RefreshToken)
	current, _ := h.AS1.Grant(second.RefreshToken)
	if !old.Active || !current.Active || first.RefreshToken == second.RefreshToken {
		t.Errorf("old %+v, current %+v; want two distinct active grants", old, current)
	}
}

func TestGrantRefreshTokenRefusedAtRefreshGrant(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	resp, err := h.ScriptedBroker().OpenSession(t.Context(), openReq(aliceToken(h)))
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
	if grant, _ := h.AS1.Grant(resp.RefreshToken); !grant.Active {
		t.Error("presenting the token at the refresh grant revoked it")
	}
}

func TestRevocationRevokesTheGrant(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	b := h.ScriptedBroker()
	first, err := b.OpenSession(t.Context(), openReq(aliceToken(h)))
	if err != nil {
		t.Fatalf("first exchange: %v", err)
	}
	second, err := b.OpenSession(t.Context(), openReq(aliceToken(h)))
	if err != nil {
		t.Fatalf("second exchange: %v", err)
	}
	// An unknown value is answered 200 and changes nothing.
	if err := b.Revoke(t.Context(), "not-a-token"); err != nil {
		t.Fatalf("Revoke(unknown): %v", err)
	}

	resp, err := h.HTTPClient().PostForm(h.AS1.RevocationEndpoint(), url.Values{"token": {first.RefreshToken}, "client_id": {identityharness.DefaultClientID}})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("revocation by a public client: status %d, want 401", resp.StatusCode)
	}
	if grant, _ := h.AS1.Grant(first.RefreshToken); !grant.Active {
		t.Fatalf("grant %+v after refused revocations, want active", grant)
	}

	if err := b.Revoke(t.Context(), first.RefreshToken); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if grant, _ := h.AS1.Grant(first.RefreshToken); grant.Active || grant.Reason != "revoked" {
		t.Fatalf("after revocation: grant %+v, want revoked", grant)
	}
	if grant, _ := h.AS1.Grant(second.RefreshToken); !grant.Active {
		t.Fatalf("revoking one grant changed another: %+v", grant)
	}
	// AS-1 knows no sessions, so it has nothing to refuse a later open for.
	if _, err := b.OpenSession(t.Context(), openReq(aliceToken(h))); err != nil {
		t.Fatalf("exchange after a revocation: %v", err)
	}
}

func TestGrantExpiry(t *testing.T) {
	clock := newTestClock()
	h := identityharness.New(t, identityharness.Options{Now: clock.Now, GrantMaxLifetime: time.Hour})
	b := h.ScriptedBroker()
	byClock, err := b.OpenSession(t.Context(), openReq(aliceToken(h)))
	if err != nil {
		t.Fatalf("first exchange: %v", err)
	}
	forced, err := b.OpenSession(t.Context(), openReq(aliceToken(h)))
	if err != nil {
		t.Fatalf("second exchange: %v", err)
	}
	if byClock.ExpiresIn != 3600 {
		t.Errorf("expires_in = %d, want the configured maximum 3600", byClock.ExpiresIn)
	}

	h.AS1.ExpireGrant(forced.RefreshToken)
	if grant, _ := h.AS1.Grant(forced.RefreshToken); grant.Active || grant.Reason != "" {
		t.Errorf("forced expiry: grant %+v, want inactive with no reason", grant)
	}
	if grant, _ := h.AS1.Grant(byClock.RefreshToken); !grant.Active {
		t.Fatalf("grant %+v before its expiry, want active", grant)
	}
	clock.Advance(time.Hour)
	if grant, _ := h.AS1.Grant(byClock.RefreshToken); grant.Active || grant.Reason != "" {
		t.Errorf("at its expiry: grant %+v, want inactive with no reason", grant)
	}
}

func TestSessionOpenFaults(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	b := h.ScriptedBroker()

	h.AS1.FailNextExchange(identityharness.Fault{Status: http.StatusServiceUnavailable, Error: "temporarily_unavailable"})
	_, err := b.OpenSession(t.Context(), openReq(aliceToken(h)))
	assertReason(t, err, http.StatusServiceUnavailable, "temporarily_unavailable", "")
	if h.AS1.GrantsIssued() != 0 {
		t.Fatal("a failed exchange issued a grant")
	}

	// Each fault fires once.
	if _, err := b.OpenSession(t.Context(), openReq(aliceToken(h))); err != nil {
		t.Fatalf("exchange after the faults: %v", err)
	}

	// A mismatched echo does not change the grant, which the broker then
	// revokes.
	wrong := identityharness.ExchangeGrant{Type: exchangeGrantType, IssuableTokenTypes: []string{tokenTypeRefresh}}
	h.AS1.MismatchNextExchangeEcho(wrong)
	resp, err := b.OpenSession(t.Context(), openReq(aliceToken(h)))
	if err != nil {
		t.Fatalf("exchange with mismatched echo: %v", err)
	}
	if !reflect.DeepEqual(resp.AuthorizationDetails, []identityharness.ExchangeGrant{wrong}) {
		t.Fatalf("echo = %+v, want the forced mismatch", resp.AuthorizationDetails)
	}
	if grant, _ := h.AS1.Grant(resp.RefreshToken); grant.Output != tokenTypeIDJAG || !grant.Active {
		t.Errorf("grant = %+v, want an active id-jag grant", grant)
	}
	if err := b.Revoke(t.Context(), resp.RefreshToken); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if grant, _ := h.AS1.Grant(resp.RefreshToken); grant.Reason != "revoked" {
		t.Errorf("after revocation: grant %+v, want revoked", grant)
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
		!slices.Equal(doc.DetailsTypes, []string{exchangeGrantType}) {
		t.Errorf("discovery = %+v, want revocation, token exchange, none and private_key_jwt, the exchange-grant type", doc)
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
