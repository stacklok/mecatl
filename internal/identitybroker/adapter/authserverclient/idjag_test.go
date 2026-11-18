package authserverclient_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server/as1servertest"
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

// testActTrustDomain is the agent trust domain of the real AS-1's ID-JAGs.
const testActTrustDomain = "agents.test"

// idjagAS1 is the real AS-1 issuing ID-JAGs, the broker's client for it
// behind faults, and a grant held for Alice whose agent presents actor.
type idjagAS1 struct {
	a      *as1servertest.AS1
	faults *identityharness.FaultTransport
	client *authserverclient.Client
	// access is Alice's access token, and grant the refresh token it was
	// exchanged for.
	access, grant identitybroker.Token
	actor         string
}

// startIDJAGAS1 starts the real AS-1 issuing ID-JAGs for as2Issuer and
// as2Resource, checking actor tokens with realAS1Verifier, and opens a grant
// for Alice there.
func startIDJAGAS1(t *testing.T, as2Issuer, as2Resource string) *idjagAS1 {
	t.Helper()
	h := identityharness.New(t, identityharness.Options{})
	a := as1servertest.Start(t, h, as1servertest.IssueIDJAGs(testActTrustDomain, as2Issuer, as2Resource))
	x := &idjagAS1{a: a, actor: actorJWT(t, identitybroker.ActorIssuer(testActTrustDomain)+"/session/s1/agent/main")}
	alice := h.Owner(identityharness.Alice)
	if err := a.Server.UseCredentialVerifier(realAS1Verifier{alice: identitybroker.Owner{Issuer: alice.Issuer, Subject: alice.Subject}, actor: x.actor}); err != nil {
		t.Fatal(err)
	}
	hc := a.HTTPClient()
	x.faults = identityharness.NewFaultTransport(hc.Transport, a.Server.TokenEndpoint())
	hc.Transport = x.faults
	var err error
	if x.client, err = authserverclient.New(authserverclient.Config{
		Issuer: a.Issuer(), TokenEndpoint: a.Server.TokenEndpoint(), Assertions: a.Signer, HTTPClient: hc,
	}); err != nil {
		t.Fatal(err)
	}
	x.access = a.SignIn(t, as1servertest.SignIn{User: identityharness.Alice})
	exchanged, err := x.client.Exchange(t.Context(), identitybroker.TokenExchange{SubjectToken: x.access, Grant: sessionGrant(time.Time{})})
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	x.grant = exchanged.RefreshToken
	return x
}

// request is the ID-JAG request for Alice's agent at as2Issuer and
// as2Resource.
func (x *idjagAS1) request(as2Issuer, as2Resource string) identitybroker.IDJAGRequest {
	req := idjagRequest(x.grant, as2Issuer, as2Resource)
	req.ActorToken = identitybroker.NewToken(x.actor)
	return req
}

func idjagRequest(grant identitybroker.Token, audience, resource string) identitybroker.IDJAGRequest {
	return identitybroker.IDJAGRequest{
		SubjectToken: grant, ActorToken: identitybroker.NewToken(testSVID),
		Audience: audience, Resource: resource, OperationID: "op-pr42-001",
	}
}

// realAS1Verifier accepts actor as Alice's agent testAgentID of trust domain
// agents.test and denies everything else, standing in for the broker's
// CredentialVerifier at the real AS-1.
type realAS1Verifier struct {
	alice identitybroker.Owner
	actor string
}

func (realAS1Verifier) VerifyCredential(context.Context, identitybroker.VerifyCredentialRequest) (identitybroker.VerifiedAgent, error) {
	return identitybroker.VerifiedAgent{}, errors.New("realAS1Verifier: VerifyCredential is not AS-1's check")
}

func (v realAS1Verifier) VerifyCredentialForOwner(_ context.Context, req identitybroker.VerifyCredentialForOwnerRequest) (identitybroker.VerifiedAgent, error) {
	if req.Owner != v.alice || req.Credential.Value() != v.actor {
		return identitybroker.VerifiedAgent{}, identitybroker.ReasonError(identitybroker.ReasonDenied, "not the test agent")
	}
	actIssuer := identitybroker.ActorIssuer(testActTrustDomain)
	return identitybroker.VerifiedAgent{
		Agent: identitybroker.AgentRef{Subject: actIssuer + "/session/s1/agent/main"}, AgentID: testAgentID,
		ActorID: actIssuer + "/agent/" + testAgentID, ExpiresAt: time.Now().Add(time.Hour),
	}, nil
}

// actorJWT is a JWT standing in for an agent's SVID; realAS1Verifier compares
// it byte for byte.
func actorJWT(t *testing.T, sub string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"sub": sub, "aud": "mecatl-broker", "exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

// TestIssueIDJAGAgainstRealAS1 runs the exchange at the real AS-1, which
// checks the actor token with the bound verifier and refuses any form other
// than design step (7)'s.
func TestIssueIDJAGAgainstRealAS1(t *testing.T) {
	x := startIDJAGAS1(t, testAS2Issuer, testVMCP)
	c := x.client
	req := x.request(testAS2Issuer, testVMCP)
	res, err := c.IssueIDJAG(t.Context(), req)
	if err != nil {
		t.Fatalf("IssueIDJAG: %v", err)
	}
	if typ := segment(t, res.IDJAG, 0)["typ"]; typ != "oauth-id-jag+jwt" {
		t.Fatalf("ID-JAG typ = %v, want oauth-id-jag+jwt", typ)
	}
	got := claims(t, res.IDJAG)
	actIssuer := identitybroker.ActorIssuer(testActTrustDomain)
	if got["client_id"] != identitybroker.BrokerClientID || got["aud"] != testAS2Issuer || got["resource"] != testVMCP ||
		got["iss"] != x.a.Issuer() || got["sub"] != as1servertest.Subject(t, x.access) ||
		!reflect.DeepEqual(got["act"], map[string]any{"iss": actIssuer, "sub": actIssuer + "/agent/" + testAgentID}) {
		t.Fatalf("ID-JAG claims = %v", got)
	}
	// Exactly one ID-JAG was issued, the one returned, for the operation the
	// request named.
	var issued []identityharness.GrantRequest
	for _, r := range x.faults.Requests() {
		if r.Kind == identityharness.KindIDJAG {
			issued = append(issued, r)
		}
	}
	if len(issued) != 1 || issued[0].IssuedToken != res.IDJAG.Value() || claims(t, identitybroker.NewToken(issued[0].IssuedToken))["jti"] != got["jti"] ||
		issued[0].Operation != "op-pr42-001" {
		t.Fatalf("AS-1 answered %d ID-JAG exchanges, want one issuing the returned ID-JAG for op-pr42-001", len(issued))
	}

	// AS-1's refusals are returned as refusals.
	req.ActorToken = identitybroker.NewToken(actorJWT(t, actIssuer+"/session/s1/agent/other"))
	_, err = c.IssueIDJAG(t.Context(), req)
	var refusal *identitybroker.ServerRefusal
	if !errors.As(err, &refusal) || refusal.Code != "invalid_request" || refusal.Reason != "denied" {
		t.Fatalf("IssueIDJAG with another actor = %v, want invalid_request denied", err)
	}
	req.ActorToken = identitybroker.NewToken(x.actor)
	req.Audience = "https://other.example"
	_, err = c.IssueIDJAG(t.Context(), req)
	if !errors.As(err, &refusal) || refusal.Code != "invalid_target" || !identitybroker.IsConfigurationFault(err) {
		t.Fatalf("IssueIDJAG for another audience = %v, want invalid_target", err)
	}
}

// TestIssueIDJAGAgainstRealAS1RefusesAnUnconfiguredResource checks design
// step (7)'s check 3 at the real AS-1: the audience is the configured AS-2,
// but the resource is not one of its vMCPs, so AS-1 refuses with
// invalid_target and issues no ID-JAG.
func TestIssueIDJAGAgainstRealAS1RefusesAnUnconfiguredResource(t *testing.T) {
	x := startIDJAGAS1(t, testAS2Issuer, testVMCP)
	_, err := x.client.IssueIDJAG(t.Context(), x.request(testAS2Issuer, testAS2Issuer+"/other-mcp"))
	var refusal *identitybroker.ServerRefusal
	if !errors.As(err, &refusal) || refusal.Status != http.StatusBadRequest || refusal.Code != "invalid_target" ||
		!identitybroker.IsConfigurationFault(err) {
		t.Fatalf("IssueIDJAG for an unconfigured resource = %v, want a 400 invalid_target configuration fault", err)
	}
	var exchanges int
	for _, r := range x.faults.Requests() {
		if r.Kind != identityharness.KindIDJAG {
			continue
		}
		exchanges++
		if r.Status != http.StatusBadRequest || r.IssuedToken != "" {
			t.Fatalf("AS-1 answered the ID-JAG exchange with status %d, issuing a token: %v; want 400 and none", r.Status, r.IssuedToken != "")
		}
	}
	if exchanges != 1 {
		t.Fatalf("AS-1 answered %d ID-JAG exchanges, want 1", exchanges)
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
