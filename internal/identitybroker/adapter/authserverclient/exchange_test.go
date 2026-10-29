package authserverclient_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// sessionGrant is the entry the broker core sends at session open.
func sessionGrant(expiresAt time.Time) identitybroker.ExchangeGrant {
	return identitybroker.ExchangeGrant{
		Type: identitybroker.ExchangeGrantType, IssuableTokenTypes: []string{identitybroker.TokenTypeIDJAG}, ExpiresAt: expiresAt,
	}
}

func TestExchangeConvertsAtAS1(t *testing.T) {
	for name, expiresAt := range map[string]time.Duration{"no expires_at": 0, "expires_at": time.Hour} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, fixtureOptions{})
			c := f.client(t, &recorder{}, nil)
			req := identitybroker.TokenExchange{SubjectToken: identitybroker.NewToken(f.aliceToken()), Grant: sessionGrant(time.Time{})}
			wantExpiry := f.clock.Now().Add(identityharness.DefaultGrantMaxLifetime)
			if expiresAt != 0 {
				req.Grant.ExpiresAt = f.clock.Now().Add(expiresAt)
				wantExpiry = req.Grant.ExpiresAt
			}
			res, err := c.Exchange(t.Context(), req)
			if err != nil {
				t.Fatalf("Exchange: %v", err)
			}
			if res.IssuedTokenType != identitybroker.TokenTypeRefreshToken || len(res.Grants) != 1 {
				t.Fatalf("result type %q with %d grants, want a refresh token and one echoed entry", res.IssuedTokenType, len(res.Grants))
			}
			g := res.Grants[0]
			if g.Type != identitybroker.ExchangeGrantType || !slices.Equal(g.IssuableTokenTypes, []string{identitybroker.TokenTypeIDJAG}) || !g.ExpiresAt.Equal(wantExpiry) {
				t.Fatalf("echo = %+v, want the exchange grant for id-jag expiring at %v", g, wantExpiry)
			}
			grant, ok := f.h.AS1.Grant(res.RefreshToken.Value())
			if !ok || !grant.Active || !grant.ExpiresAt.Equal(wantExpiry) {
				t.Fatalf("AS-1 grant = %+v, %v; want an active grant expiring at %v", grant, ok, wantExpiry)
			}
		})
	}
}

func TestExchangeSendsTheSessionOpenForm(t *testing.T) {
	expires := time.Unix(1_800_003_600, 0)
	for _, tt := range []struct {
		name        string
		grant       identitybroker.ExchangeGrant
		wantDetails string
	}{
		{"no expires_at", sessionGrant(time.Time{}), `[{"type":"https://toolhive.dev/exchange_grant","issuable_token_types":["urn:ietf:params:oauth:token-type:id-jag"]}]`},
		{"expires_at", sessionGrant(expires), `[{"type":"https://toolhive.dev/exchange_grant","issuable_token_types":["urn:ietf:params:oauth:token-type:id-jag"],"expires_at":1800003600}]`},
		// The adapter sends the entry it is given; the core owns its content.
		{"the given entry", identitybroker.ExchangeGrant{Type: "https://example.com/other", IssuableTokenTypes: []string{"a", "b"}},
			`[{"type":"https://example.com/other","issuable_token_types":["a","b"]}]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var form url.Values
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				form = r.PostForm
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"rt-secret-value","issued_token_type":"urn:ietf:params:oauth:token-type:refresh_token","authorization_details":[{"type":"t","issuable_token_types":["x"],"expires_at":1800003600}]}`))
			}))
			t.Cleanup(server.Close)
			c := newStubClient(t, server.URL, server.Client())
			res, err := c.Exchange(t.Context(), identitybroker.TokenExchange{SubjectToken: identitybroker.NewToken("subject"), Grant: tt.grant})
			if err != nil {
				t.Fatalf("Exchange: %v", err)
			}
			want := map[string]string{
				"grant_type":            grantTokenExchange,
				"subject_token":         "subject",
				"subject_token_type":    tokenTypeAccess,
				"requested_token_type":  tokenTypeRefresh,
				"authorization_details": tt.wantDetails,
				"client_assertion_type": "urn:ietf:params:oauth:client-assertion-type:jwt-bearer",
			}
			for name, value := range want {
				if got := form[name]; len(got) != 1 || got[0] != value {
					t.Errorf("form %s = %q, want %q", name, got, value)
				}
			}
			if form.Get("client_assertion") == "" || len(form) != len(want)+1 {
				t.Errorf("form has %d fields, want exactly %d with a client assertion", len(form), len(want)+1)
			}
			// The adapter decodes the echo as sent; the core checks it.
			if res.RefreshToken.Value() != "rt-secret-value" || len(res.Grants) != 1 || res.Grants[0].Type != "t" || !res.Grants[0].ExpiresAt.Equal(expires) {
				t.Fatalf("result = %+v, want the response decoded as sent", res)
			}
		})
	}
}

func TestExchangeEchoWithoutExpiryDecodesAsZero(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"rt-secret-value","issued_token_type":"x","authorization_details":[{"type":"t","issuable_token_types":[]}]}`))
	}))
	t.Cleanup(server.Close)
	res, err := newStubClient(t, server.URL, server.Client()).Exchange(t.Context(), identitybroker.TokenExchange{SubjectToken: identitybroker.NewToken("s"), Grant: sessionGrant(time.Time{})})
	if err != nil || len(res.Grants) != 1 || !res.Grants[0].ExpiresAt.IsZero() {
		t.Fatalf("Exchange = %+v, %v; want one entry with no expiry", res, err)
	}
}

func TestExchangeMalformedSuccessIsAConfigurationFault(t *testing.T) {
	for name, body := range map[string]string{
		"not JSON":              `not json`,
		"wrong member type":     `{"access_token":5,"issued_token_type":"x"}`,
		"wrong expires_at type": `{"access_token":"rt-secret-value","issued_token_type":"x","authorization_details":[{"type":"t","expires_at":"soon"}]}`,
		"no access_token":       `{"issued_token_type":"urn:ietf:params:oauth:token-type:refresh_token"}`,
		"no issued_token_type":  `{"access_token":"rt-secret-value"}`,
		"authorization_details": `{"access_token":"rt-secret-value","issued_token_type":"x","authorization_details":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			t.Cleanup(server.Close)
			c := newStubClient(t, server.URL, server.Client())
			_, err := c.Exchange(t.Context(), identitybroker.TokenExchange{SubjectToken: identitybroker.NewToken("subject-secret"), Grant: sessionGrant(time.Time{})})
			if !errors.Is(err, identitybroker.ErrMalformedResponse) || !errors.Is(err, identitybroker.ErrTransportConfiguration) ||
				!identitybroker.IsConfigurationFault(err) || identitybroker.IsUnavailable(err) {
				t.Fatalf("Exchange = %v, want a malformed response that is a configuration fault", err)
			}
			assertNoSecrets(t, err, "subject-secret", "rt-secret-value")
		})
	}
}

func TestExchangeReturnsAS1Refusals(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	r := &recorder{}
	c := f.client(t, r, nil)
	f.h.AS1.FailNextExchange(identityharness.Fault{Status: http.StatusServiceUnavailable})
	token := f.aliceToken()
	_, err := c.Exchange(t.Context(), identitybroker.TokenExchange{SubjectToken: identitybroker.NewToken(token), Grant: sessionGrant(time.Time{})})
	var refusal *identitybroker.ServerRefusal
	if !errors.As(err, &refusal) || refusal.Status != http.StatusServiceUnavailable || !identitybroker.IsUnavailable(err) {
		t.Fatalf("Exchange = %v, want AS-1's 503", err)
	}
	assertNoSecrets(t, err, append(values(r.all()), token)...)
}
