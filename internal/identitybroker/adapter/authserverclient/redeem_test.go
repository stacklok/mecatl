package authserverclient_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/mcpbroker/mcpbrokertest"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/authserverclient"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/inprocesshttp"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

const grantJWTBearer = "urn:ietf:params:oauth:grant-type:jwt-bearer"

// TestRedeemAtRealAS2 redeems an ID-JAG from the harness's AS-1 at the real
// embedded AS-2, set up to trust that AS-1 through mcpbrokertest. Not
// parallel: StartAS2 sets an environment variable.
func TestRedeemAtRealAS2(t *testing.T) {
	f := newFixture(t)
	keySet, err := json.Marshal(f.signer.JWKS())
	if err != nil {
		t.Fatal(err)
	}
	as2 := mcpbrokertest.StartAS2(t, identitybroker.BrokerClientID, keySet, mcpbrokertest.AS1Trust{
		Issuer: f.h.AS1.Issuer(), JWKSURL: f.h.AS1.JWKSURL(), CAFile: f.h.WriteCACert(t, t.TempDir()),
		Subjects: []string{f.h.AS1.Subject(identityharness.Alice)},
	}).AS2
	f.trustSVID(as2.Issuer, as2.Resource)
	as1 := f.client(t, &recorder{}, nil)
	jag, err := as1.IssueIDJAG(t.Context(), idjagRequest(f.aliceGrant(t, as1), as2.Issuer, as2.Resource))
	if err != nil {
		t.Fatalf("IssueIDJAG: %v", err)
	}

	// Keep AS-2's raw answers, to check what the client did not keep.
	var mu sync.Mutex
	var answers [][]byte
	hc := inprocesshttp.NewClient(as2.Handler(), func(*url.URL) error { return nil })
	base := hc.Transport
	hc.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		resp, err := base.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		raw, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		mu.Lock()
		answers = append(answers, raw)
		mu.Unlock()
		resp.Body = io.NopCloser(bytes.NewReader(raw))
		return resp, err
	})
	c, err := authserverclient.New(authserverclient.Config{
		Issuer: as2.Issuer, TokenEndpoint: as2.TokenEndpoint, Assertions: f.signer, HTTPClient: hc,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Redeem(t.Context(), identitybroker.Redemption{Assertion: jag.IDJAG, Resource: as2.Resource})
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if res.AccessToken.IsZero() || res.ExpiresIn <= 0 {
		t.Fatalf("result = %+v, want a gateway token with a lifetime", res)
	}
	mu.Lock()
	raw := slices.Clone(answers)
	mu.Unlock()
	var answer map[string]any
	if len(raw) != 1 || json.Unmarshal(raw[0], &answer) != nil {
		t.Fatalf("AS-2 answered %d times, want once with JSON", len(raw))
	}
	if _, ok := answer["refresh_token"]; ok {
		t.Fatal("AS-2 issued a refresh token with the gateway token")
	}

	// An ID-JAG redeems once; fosite gives no reason.
	_, err = c.Redeem(t.Context(), identitybroker.Redemption{Assertion: jag.IDJAG, Resource: as2.Resource})
	var refusal *identitybroker.ServerRefusal
	if !errors.As(err, &refusal) || refusal.Code != "invalid_grant" {
		t.Fatalf("second Redeem = %v, want invalid_grant", err)
	}
	assertNoSecrets(t, err, jag.IDJAG.Value(), res.AccessToken.Value())
}

func TestRedeemSendsTheForm(t *testing.T) {
	var form url.Values
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		form = r.PostForm
		_, _ = w.Write([]byte(`{"access_token":"gw-secret-value","token_type":"Bearer","expires_in":300,"refresh_token":"rt-secret-value"}`))
	}))
	t.Cleanup(server.Close)
	res, err := newStubClient(t, server.URL, server.Client()).Redeem(t.Context(),
		identitybroker.Redemption{Assertion: identitybroker.NewToken("jag-secret-value"), Resource: testVMCP})
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	want := map[string]string{
		"grant_type":            grantJWTBearer,
		"assertion":             "jag-secret-value",
		"resource":              testVMCP,
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
	// The refresh token is not part of the result.
	if res.AccessToken.Value() != "gw-secret-value" || res.ExpiresIn != 5*time.Minute ||
		!reflect.DeepEqual(res, identitybroker.GatewayTokenResult{AccessToken: res.AccessToken, ExpiresIn: res.ExpiresIn}) {
		t.Fatalf("result = %+v, want the gateway token and 5m", res)
	}
}

func TestRedeemAcceptsBearerInAnyCase(t *testing.T) {
	for _, tokenType := range []string{"Bearer", "bearer", "BEARER"} {
		res, err := stubAnswering(t, http.StatusOK, `{"access_token":"gw-secret-value","token_type":"`+tokenType+`"}`).Redeem(t.Context(),
			identitybroker.Redemption{Assertion: identitybroker.NewToken("jag"), Resource: testVMCP})
		if err != nil || res.AccessToken.Value() != "gw-secret-value" || res.ExpiresIn != 0 {
			t.Fatalf("token_type %s: %+v, %v; want the token and no lifetime", tokenType, res, err)
		}
	}
}

func TestRedeemMalformedResponses(t *testing.T) {
	for name, body := range map[string]string{
		"not JSON":          `not json`,
		"wrong member type": `{"access_token":5,"token_type":"Bearer"}`,
		"no access_token":   `{"token_type":"Bearer"}`,
		"no token_type":     `{"access_token":"gw-secret-value"}`,
		"not bearer":        `{"access_token":"gw-secret-value","token_type":"N_A"}`,
		"DPoP":              `{"access_token":"gw-secret-value","token_type":"DPoP"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := stubAnswering(t, http.StatusOK, body).Redeem(t.Context(),
				identitybroker.Redemption{Assertion: identitybroker.NewToken("jag-secret-value"), Resource: testVMCP})
			if !errors.Is(err, identitybroker.ErrMalformedResponse) || !errors.Is(err, identitybroker.ErrTransportConfiguration) ||
				!identitybroker.IsConfigurationFault(err) || identitybroker.IsUnavailable(err) {
				t.Fatalf("Redeem = %v, want a malformed response that is a configuration fault", err)
			}
			assertNoSecrets(t, err, "jag-secret-value", "gw-secret-value")
		})
	}
}
