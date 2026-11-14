package authserverclient_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/authserverclient"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

const (
	testAS2Issuer = "https://gateway.example/v1/mcp/broker"
	testVMCP      = testAS2Issuer + "/mcp"
	testAgentID   = "ag_7hq2x9"
	testSVID      = "svid-alice"
	tokenTypeJWT  = "urn:ietf:params:oauth:token-type:jwt"
	tokenTypeJAG  = "urn:ietf:params:oauth:token-type:id-jag"
)

// trustSVID has the harness's AS-1 accept testSVID for Alice's grants and
// issue ID-JAGs for audience and resource.
func (f *fixture) trustSVID(audience, resource string) {
	f.h.AS1.AddIDJAGTarget(audience, resource)
	f.h.AS1.SetActorValidator(func(_ context.Context, owner identityharness.Owner, actor string) (string, time.Time, error) {
		if actor != testSVID || owner != f.h.Owner(identityharness.Alice) {
			return "", time.Time{}, identityharness.ErrActorDenied
		}
		return testAgentID, time.Now().Add(time.Hour), nil
	})
}

// aliceGrant opens a session for Alice through c and returns the grant's
// refresh token.
func (f *fixture) aliceGrant(t *testing.T, c *authserverclient.Client) identitybroker.Token {
	t.Helper()
	res, err := c.Exchange(t.Context(), identitybroker.TokenExchange{SubjectToken: identitybroker.NewToken(f.aliceToken()), Grant: sessionGrant(time.Time{})})
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	return res.RefreshToken
}

func idjagRequest(grant identitybroker.Token, audience, resource string) identitybroker.IDJAGRequest {
	return identitybroker.IDJAGRequest{
		SubjectToken: grant, ActorToken: identitybroker.NewToken(testSVID),
		Audience: audience, Resource: resource, OperationID: "op-pr42-001",
	}
}

// TestIssueIDJAGAgainstHarness runs the exchange at the harness's AS-1, which
// refuses any form other than design step (7)'s.
func TestIssueIDJAGAgainstHarness(t *testing.T) {
	f := newFixture(t)
	f.trustSVID(testAS2Issuer, testVMCP)
	c := f.client(t, &recorder{}, nil)
	grant := f.aliceGrant(t, c)
	res, err := c.IssueIDJAG(t.Context(), idjagRequest(grant, testAS2Issuer, testVMCP))
	if err != nil {
		t.Fatalf("IssueIDJAG: %v", err)
	}
	if typ := segment(t, res.IDJAG, 0)["typ"]; typ != "oauth-id-jag+jwt" {
		t.Fatalf("ID-JAG typ = %v, want oauth-id-jag+jwt", typ)
	}
	got := claims(t, res.IDJAG)
	if got["client_id"] != identitybroker.BrokerClientID || got["aud"] != testAS2Issuer || got["resource"] != testVMCP ||
		got["sub"] != f.h.AS1.Subject(identityharness.Alice) ||
		!reflect.DeepEqual(got["act"], map[string]any{"iss": f.h.AS1.Issuer(), "sub": testAgentID}) {
		t.Fatalf("ID-JAG claims = %v", got)
	}
	issued := f.h.AS1.IDJAGsIssued()
	if len(issued) != 1 || issued[0].Operation != "op-pr42-001" || issued[0].JTI != got["jti"] {
		t.Fatalf("AS-1 issued %+v, want one ID-JAG for the operation", issued)
	}

	// AS-1's refusals are returned as refusals.
	req := idjagRequest(grant, testAS2Issuer, testVMCP)
	req.ActorToken = identitybroker.NewToken("svid-other")
	_, err = c.IssueIDJAG(t.Context(), req)
	var refusal *identitybroker.ServerRefusal
	if !errors.As(err, &refusal) || refusal.Code != "invalid_request" || refusal.Reason != "denied" {
		t.Fatalf("IssueIDJAG with another actor = %v, want invalid_request denied", err)
	}
	_, err = c.IssueIDJAG(t.Context(), idjagRequest(grant, "https://other.example", testVMCP))
	if !errors.As(err, &refusal) || refusal.Code != "invalid_target" || !identitybroker.IsConfigurationFault(err) {
		t.Fatalf("IssueIDJAG for another audience = %v, want invalid_target", err)
	}
}

func TestIssueIDJAGSendsTheForm(t *testing.T) {
	var form url.Values
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		form = r.PostForm
		_, _ = w.Write([]byte(`{"access_token":"jag-secret-value","issued_token_type":"` + tokenTypeJAG + `","token_type":"N_A","expires_in":90}`))
	}))
	t.Cleanup(server.Close)
	res, err := newStubClient(t, server.URL, server.Client()).IssueIDJAG(t.Context(),
		idjagRequest(identitybroker.NewToken("rt-secret"), testAS2Issuer, testVMCP))
	if err != nil {
		t.Fatalf("IssueIDJAG: %v", err)
	}
	want := map[string]string{
		"grant_type":            grantTokenExchange,
		"subject_token":         "rt-secret",
		"subject_token_type":    tokenTypeRefresh,
		"actor_token":           testSVID,
		"actor_token_type":      tokenTypeJWT,
		"requested_token_type":  tokenTypeJAG,
		"audience":              testAS2Issuer,
		"resource":              testVMCP,
		"mecatl_operation":      "op-pr42-001",
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
	if res.IDJAG.Value() != "jag-secret-value" {
		t.Fatalf("result = %+v, want the ID-JAG", res)
	}
}

// stubAnswering is a stand-in token endpoint answering every request with
// status and body.
func stubAnswering(t *testing.T, status int, body string) *authserverclient.Client {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return newStubClient(t, server.URL, server.Client())
}

func TestIssueIDJAGMalformedResponses(t *testing.T) {
	for name, body := range map[string]string{
		"not JSON":             `not json`,
		"wrong member type":    `{"access_token":5,"issued_token_type":"` + tokenTypeJAG + `"}`,
		"no access_token":      `{"issued_token_type":"` + tokenTypeJAG + `"}`,
		"no issued_token_type": `{"access_token":"jag-secret-value"}`,
		"a refresh token":      `{"access_token":"jag-secret-value","issued_token_type":"` + tokenTypeRefresh + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := stubAnswering(t, http.StatusOK, body).IssueIDJAG(t.Context(),
				idjagRequest(identitybroker.NewToken("rt-secret"), testAS2Issuer, testVMCP))
			if !errors.Is(err, identitybroker.ErrMalformedResponse) || !errors.Is(err, identitybroker.ErrTransportConfiguration) ||
				!identitybroker.IsConfigurationFault(err) || identitybroker.IsUnavailable(err) {
				t.Fatalf("IssueIDJAG = %v, want a malformed response that is a configuration fault", err)
			}
			assertNoSecrets(t, err, "rt-secret", testSVID, "jag-secret-value")
		})
	}
}

func TestIssueIDJAGNeverPutsTokensInErrors(t *testing.T) {
	secrets := []string{"rt-secret", testSVID, "jag-secret-value"}
	for name, answer := range map[string]struct {
		status int
		body   string
	}{
		"refusal echoing the tokens":  {http.StatusBadRequest, `{"error":"rt-secret","error_description":"svid-alice: rt-secret jag-secret-value"}`},
		"refusal with a known reason": {http.StatusBadRequest, `{"error":"invalid_request","error_description":"denied: svid-alice rt-secret"}`},
		"server error":                {http.StatusServiceUnavailable, `{"error":"server_error","error_description":"rt-secret"}`},
		"malformed with a token":      {http.StatusOK, `{"access_token":"jag-secret-value","issued_token_type":"svid-alice"}`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := stubAnswering(t, answer.status, answer.body).IssueIDJAG(t.Context(),
				idjagRequest(identitybroker.NewToken("rt-secret"), testAS2Issuer, testVMCP))
			if err == nil {
				t.Fatal("IssueIDJAG succeeded, want an error")
			}
			assertNoSecrets(t, err, secrets...)
		})
	}
}
