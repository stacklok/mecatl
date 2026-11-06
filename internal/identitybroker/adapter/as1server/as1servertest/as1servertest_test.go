package as1servertest_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server/as1servertest"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

func start(t *testing.T) *as1servertest.AS1 {
	t.Helper()
	return as1servertest.Start(t, identityharness.New(t, identityharness.Options{}), nil)
}

// open is the broker's session-open exchange of subject.
func open(subject identitybroker.Token) identitybroker.TokenExchange {
	return identitybroker.TokenExchange{
		SubjectToken: subject,
		Grant:        identitybroker.ExchangeGrant{Type: identitybroker.ExchangeGrantType, IssuableTokenTypes: []string{identitybroker.TokenTypeIDJAG}},
	}
}

// wantRefusal fails t unless err is a refusal with status, code, and reason.
func wantRefusal(t *testing.T, err error, status int, code, reason string) {
	t.Helper()
	var refusal *identitybroker.ServerRefusal
	if !errors.As(err, &refusal) || refusal.Status != status || refusal.Code != code || refusal.Reason != reason {
		t.Fatalf("error = %v, want a refusal: HTTP %d %q (%q)", err, status, code, reason)
	}
}

func TestStartConvertsASignInThroughTheBrokerClient(t *testing.T) {
	a := start(t)
	c := a.Client(t)

	now := time.Now()
	res, err := c.Exchange(t.Context(), open(a.SignIn(t, as1servertest.SignIn{})))
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if res.RefreshToken.IsZero() || res.IssuedTokenType != identitybroker.TokenTypeRefreshToken || len(res.Grants) != 1 {
		t.Fatalf("result has a refresh token %v, type %q, %d grants; want a refresh token with one grant",
			!res.RefreshToken.IsZero(), res.IssuedTokenType, len(res.Grants))
	}
	if exp := res.Grants[0].ExpiresAt; exp.Before(now.Add(as1servertest.IDJAGMaxLifetime-5*time.Second)) || exp.After(now.Add(as1servertest.IDJAGMaxLifetime+5*time.Second)) {
		t.Errorf("grant expiry %v, want now + %v within 5s", exp, as1servertest.IDJAGMaxLifetime)
	}
	reqs := a.Faults.Requests()
	if len(reqs) != 1 || !reqs[0].Exchange() || !reqs[0].Converted() || reqs[0].Fault != as1servertest.FaultNone || reqs[0].ClientAssertion.IsZero() {
		t.Fatalf("recorded %+v, want one converted exchange carrying an assertion", reqs)
	}
	if a.Faults.Conversions() != 1 || len(a.Faults.Refusals()) != 0 {
		t.Fatalf("%d conversions, %d refusals; want 1 and 0", a.Faults.Conversions(), len(a.Faults.Refusals()))
	}
}

func TestSignInSelectsUserScopeAndClient(t *testing.T) {
	studio := as1server.PublicClient{ID: "studio", RedirectURIs: []string{"http://127.0.0.1:18474/callback"}}
	h := identityharness.New(t, identityharness.Options{})
	a := as1servertest.Start(t, h, func(c *as1server.Config, _ *as1server.DelegateClient) {
		// studio signs in but stays out of the delegate's allowed source clients.
		c.PublicClients = append(c.PublicClients, studio)
	})
	c := a.Client(t)

	t.Run("user", func(t *testing.T) {
		bob := a.SignIn(t, as1servertest.SignIn{User: identityharness.Bob})
		if got := as1servertest.SignedInUser(t, bob); got != identityharness.Bob {
			t.Fatalf("signed in %q, want %q", got, identityharness.Bob)
		}
		alice := a.SignIn(t, as1servertest.SignIn{})
		if got := as1servertest.SignedInUser(t, alice); got != identityharness.Alice {
			t.Fatalf("the next sign-in signed in %q, want the default user %q", got, identityharness.Alice)
		}
		for _, token := range []identitybroker.Token{bob, alice} {
			if _, err := c.Exchange(t.Context(), open(token)); err != nil {
				t.Fatalf("Exchange: %v", err)
			}
		}
	})
	t.Run("no offline_access", func(t *testing.T) {
		_, err := c.Exchange(t.Context(), open(a.SignIn(t, as1servertest.SignIn{Scope: "openid"})))
		wantRefusal(t, err, http.StatusBadRequest, "invalid_request", "subject_token_offline_access")
	})
	t.Run("unlisted client", func(t *testing.T) {
		_, err := c.Exchange(t.Context(), open(a.SignIn(t, as1servertest.SignIn{Client: studio})))
		wantRefusal(t, err, http.StatusBadRequest, "invalid_request", "subject_token_source_client")
	})
}

func TestExpiredAccessTokenIsRefusedAsInvalid(t *testing.T) {
	a := start(t)
	expired := a.ExpiredAccessToken(t, a.SignIn(t, as1servertest.SignIn{}))
	// Control: AS-1's published key verifies the token, so the refusal is for
	// its expiry alone.
	wantVerifiesExceptExpiry(t, a, expired)

	_, err := a.Client(t).Exchange(t.Context(), open(expired))
	wantRefusal(t, err, http.StatusBadRequest, "invalid_request", "subject_token_invalid")
	refusals := a.Faults.Refusals()
	if len(refusals) != 1 || refusals[0].Code != "invalid_request" || refusals[0].Reason != "subject_token_invalid" {
		t.Fatalf("recorded refusals %+v, want AS-1's one subject_token_invalid", refusals)
	}
}

// wantVerifiesExceptExpiry fails t unless a key in AS-1's JWKS verifies
// token's signature and the token is expired.
func wantVerifiesExceptExpiry(t *testing.T, a *as1servertest.AS1, token identitybroker.Token) {
	t.Helper()
	resp, err := a.Server.InProcessClient().Get(a.Issuer() + "/.well-known/jwks.json")
	if err != nil {
		t.Fatalf("JWKS: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var set struct {
		Keys []struct{ Kid, X, Y string } `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		t.Fatalf("JWKS: %v", err)
	}
	_, err = jwt.Parse(token.Value(), func(tok *jwt.Token) (any, error) {
		for _, k := range set.Keys {
			if k.Kid != tok.Header["kid"] {
				continue
			}
			x, xErr := base64.RawURLEncoding.DecodeString(k.X)
			y, yErr := base64.RawURLEncoding.DecodeString(k.Y)
			if xErr != nil || yErr != nil {
				return nil, errors.New("malformed key")
			}
			return ecdsa.ParseUncompressedPublicKey(elliptic.P256(), slices.Concat([]byte{4}, x, y))
		}
		return nil, errors.New("unknown kid")
	}, jwt.WithValidMethods([]string{"ES256"}))
	if !errors.Is(err, jwt.ErrTokenExpired) || errors.Is(err, jwt.ErrTokenSignatureInvalid) || errors.Is(err, jwt.ErrTokenUnverifiable) {
		t.Fatalf("verify = %v, want only an expired token", err)
	}
}

// TestFailNextExchangeAnswersWithoutAS1: an answered exchange never reaches
// AS-1, so the same subject token converts afterwards.
func TestFailNextExchangeAnswersWithoutAS1(t *testing.T) {
	tests := []struct {
		name   string
		fault  as1servertest.Fault
		status int
	}{
		{"429", as1servertest.Fault{Status: 429}, 429},
		{"502", as1servertest.Fault{Status: 502}, 502},
		{"503 temporarily_unavailable", as1servertest.Fault{Status: 503, Error: "temporarily_unavailable"}, 503},
		{"server_error", as1servertest.Fault{Status: 500, Error: "server_error"}, 500},
		{"invalid_client", as1servertest.Fault{Status: 401, Error: "invalid_client"}, 401},
		{"signed_out", as1servertest.Fault{Error: "invalid_request", Reason: "signed_out"}, 400},
		{"upstream_revoked", as1servertest.Fault{Error: "invalid_request", Reason: "upstream_revoked: the IdP revoked it"}, 400},
		{"owner_action_required", as1servertest.Fault{Error: "invalid_request", Reason: "owner_action_required"}, 400},
		{"grant_invalid", as1servertest.Fault{Error: "invalid_request", Reason: "grant_invalid"}, 400},
	}
	a := start(t)
	c := a.Client(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := len(a.Faults.Requests())
			token := a.SignIn(t, as1servertest.SignIn{})
			a.Faults.FailNextExchange(tt.fault)
			_, err := c.Exchange(t.Context(), open(token))
			reason := identitybroker.NewServerRefusal(tt.status, tt.fault.Error, tt.fault.Reason).Reason
			wantRefusal(t, err, tt.status, tt.fault.Error, reason)
			if _, err := c.Exchange(t.Context(), open(token)); err != nil {
				t.Fatalf("exchange after the fault: %v", err)
			}
			reqs := a.Faults.Requests()[before:]
			if len(reqs) != 2 || reqs[0].Fault != as1servertest.FaultAnswered || reqs[0].Reached || !reqs[1].Converted() {
				t.Fatalf("recorded %+v, want an answered exchange, then a conversion", reqs)
			}
		})
	}
}

func TestDropNextExchangeNeverReachesAS1(t *testing.T) {
	a := start(t)
	c := a.Client(t)
	token := a.SignIn(t, as1servertest.SignIn{})

	a.Faults.DropNextExchange()
	if _, err := c.Exchange(t.Context(), open(token)); !errors.Is(err, identitybroker.ErrNoResponse) {
		t.Fatalf("dropped exchange = %v, want ErrNoResponse", err)
	}
	if _, err := c.Exchange(t.Context(), open(token)); err != nil {
		t.Fatalf("resend: %v", err)
	}
	reqs := a.Faults.Requests()
	if len(reqs) != 2 || reqs[0].Fault != as1servertest.FaultDropped || reqs[0].Reached || !reqs[1].Converted() {
		t.Fatalf("recorded %+v, want a dropped exchange, then a conversion", reqs)
	}
	if reqs[0].ClientAssertion == reqs[1].ClientAssertion {
		t.Fatal("the resend carried the dropped request's client assertion")
	}
}

func TestLoseNextExchangeResponseAfterAS1Converted(t *testing.T) {
	a := start(t)
	c := a.Client(t)
	token := a.SignIn(t, as1servertest.SignIn{})

	a.Faults.LoseNextExchangeResponse()
	if _, err := c.Exchange(t.Context(), open(token)); !errors.Is(err, identitybroker.ErrNoResponse) {
		t.Fatalf("lost exchange = %v, want ErrNoResponse", err)
	}
	_, err := c.Exchange(t.Context(), open(token))
	wantRefusal(t, err, http.StatusBadRequest, "invalid_request", "token_used")
	reqs := a.Faults.Requests()
	if len(reqs) != 2 || reqs[0].Fault != as1servertest.FaultLost || !reqs[0].Converted() || !reqs[1].Refused() {
		t.Fatalf("recorded %+v, want a converted exchange whose response was lost, then a refusal", reqs)
	}
	if a.Faults.Conversions() != 1 {
		t.Fatalf("%d conversions, want 1", a.Faults.Conversions())
	}
}

func TestReplaceNextExchangeEcho(t *testing.T) {
	a := start(t)
	c := a.Client(t)
	other := as1servertest.ExchangeGrant{Type: "https://example.com/other", IssuableTokenTypes: []string{identitybroker.TokenTypeIDJAG}, ExpiresAt: 1_900_000_000}

	t.Run("replaced", func(t *testing.T) {
		a.Faults.ReplaceNextExchangeEcho(other)
		res, err := c.Exchange(t.Context(), open(a.SignIn(t, as1servertest.SignIn{})))
		if err != nil {
			t.Fatalf("Exchange: %v", err)
		}
		if len(res.Grants) != 1 || res.Grants[0].Type != other.Type || !slices.Equal(res.Grants[0].IssuableTokenTypes, other.IssuableTokenTypes) ||
			res.Grants[0].ExpiresAt.Unix() != other.ExpiresAt || res.RefreshToken.IsZero() {
			t.Fatalf("echoed %+v, want only %+v beside the refresh token", res.Grants, other)
		}
		if last := lastRequest(t, a); last.Fault != as1servertest.FaultEchoReplaced || !last.Converted() {
			t.Fatalf("recorded %+v, want a converted exchange whose echo was replaced", last)
		}
	})
	t.Run("no expires_at", func(t *testing.T) {
		a.Faults.ReplaceNextExchangeEcho(as1servertest.ExchangeGrant{Type: identitybroker.ExchangeGrantType, IssuableTokenTypes: []string{identitybroker.TokenTypeIDJAG}})
		res, err := c.Exchange(t.Context(), open(a.SignIn(t, as1servertest.SignIn{})))
		if err != nil || len(res.Grants) != 1 || !res.Grants[0].ExpiresAt.IsZero() {
			t.Fatalf("Exchange = %+v, %v; want one grant without an expiry", res.Grants, err)
		}
	})
	t.Run("removed", func(t *testing.T) {
		a.Faults.ReplaceNextExchangeEcho()
		res, err := c.Exchange(t.Context(), open(a.SignIn(t, as1servertest.SignIn{})))
		if err != nil || len(res.Grants) != 0 || res.RefreshToken.IsZero() {
			t.Fatalf("Exchange = %+v, %v; want a refresh token without an echo", res.Grants, err)
		}
	})
	t.Run("refused exchange", func(t *testing.T) {
		// An exchange AS-1 refuses keeps AS-1's answer and consumes the fault.
		token := a.SignIn(t, as1servertest.SignIn{})
		if _, err := c.Exchange(t.Context(), open(token)); err != nil {
			t.Fatal(err)
		}
		a.Faults.ReplaceNextExchangeEcho(other)
		_, err := c.Exchange(t.Context(), open(token))
		wantRefusal(t, err, http.StatusBadRequest, "invalid_request", "token_used")
		// Nothing was replaced, so the refusal is recorded without a fault.
		if last := lastRequest(t, a); last.Fault != as1servertest.FaultNone || !last.Refused() || last.Reason != "token_used" {
			t.Fatalf("recorded %+v, want AS-1's token_used refusal without a fault", last)
		}
		res, err := c.Exchange(t.Context(), open(a.SignIn(t, as1servertest.SignIn{})))
		if err != nil || len(res.Grants) != 1 || res.Grants[0].Type != identitybroker.ExchangeGrantType {
			t.Fatalf("Exchange = %+v, %v; want AS-1's own echo", res.Grants, err)
		}
	})
}

// lastRequest returns the latest request FaultTransport recorded.
func lastRequest(t *testing.T, a *as1servertest.AS1) as1servertest.Request {
	t.Helper()
	reqs := a.Faults.Requests()
	if len(reqs) == 0 {
		t.Fatal("no request recorded")
	}
	return reqs[len(reqs)-1]
}

// TestFaultsApplyInOrder: queued faults apply one per exchange, in the order
// queued, and then exchanges pass through.
func TestFaultsApplyInOrder(t *testing.T) {
	a := start(t)
	c := a.Client(t)
	token := a.SignIn(t, as1servertest.SignIn{})

	a.Faults.DropNextExchange()
	a.Faults.FailNextExchange(as1servertest.Fault{Status: 502})
	if _, err := c.Exchange(t.Context(), open(token)); !errors.Is(err, identitybroker.ErrNoResponse) {
		t.Fatalf("first exchange = %v, want ErrNoResponse", err)
	}
	_, err := c.Exchange(t.Context(), open(token))
	wantRefusal(t, err, 502, "", "")
	if _, err := c.Exchange(t.Context(), open(token)); err != nil {
		t.Fatalf("third exchange: %v", err)
	}
	var kinds []as1servertest.FaultKind
	for _, r := range a.Faults.Requests() {
		kinds = append(kinds, r.Fault)
	}
	if want := []as1servertest.FaultKind{as1servertest.FaultDropped, as1servertest.FaultAnswered, as1servertest.FaultNone}; !slices.Equal(kinds, want) {
		t.Fatalf("faults %q, want %q", kinds, want)
	}
}
