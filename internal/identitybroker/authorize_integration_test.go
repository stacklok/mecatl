package identitybroker_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/mecatl/internal/adapter/mcpbroker/mcpbrokertest"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server/as1servertest"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/authserverclient"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/inprocesshttp"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// The tests in this file authorize calls against the harness's AS-1 and the
// real embedded ToolHive AS-2, through the real authserverclient adapter, with
// a FaultTransport in front of each. None is parallel: mcpbrokertest.StartAS2
// sets an environment variable.

// harnessFixture is a broker whose gateway routes "github", exchanging at the
// harness's AS-1 and redeeming at the real AS-2, with Alice's session open on
// openHost and its main agent registered.
type harnessFixture struct {
	h   *identityharness.Harness
	b   *identitybroker.Broker
	rec *identitybroker.MemoryRecorder
	// as1 and as2 are the fault transports in front of each server's token
	// endpoint.
	as1, as2 *identityharness.FaultTransport
	// as2Client redeems at AS-2 as the broker does, with fresh client
	// assertions.
	as2Client                      *authserverclient.Client
	as2Issuer, as2Resource, as1Iss string
	// vmcp is the real vMCP the MCP broker process serves at as2Resource.
	vmcp http.Handler

	s    identitybroker.Session
	cred identitybroker.Token
}

func newHarnessFixture(t *testing.T) *harnessFixture {
	t.Helper()
	return newHarnessFixtureWith(t, harnessOptions{})
}

// harnessOptions vary a harnessFixture's AS-2.
type harnessOptions struct {
	// distrustAlice leaves Alice out of the AS-1 subjects AS-2 trusts.
	distrustAlice bool
	// unknownAS2Key has the broker sign its AS-2 client assertions with a key
	// AS-2 does not know.
	unknownAS2Key bool
}

func newHarnessFixtureWith(t *testing.T, opts harnessOptions) *harnessFixture {
	t.Helper()
	newSigner := func() (*identitybroker.ClientAssertionSigner, *ecdsa.PrivateKey) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		signer, err := identitybroker.NewClientAssertionSigner(key, 0)
		if err != nil {
			t.Fatal(err)
		}
		return signer, key
	}
	signer, clientKey := newSigner()
	as2Signer := signer
	if opts.unknownAS2Key {
		as2Signer, _ = newSigner()
	}
	h := identityharness.New(t, identityharness.Options{
		BrokerKeys: []identityharness.BrokerKey{{KeyID: signer.JWKS().Keys[0].KeyID, Public: &clientKey.PublicKey}},
	})
	keySet, err := json.Marshal(signer.JWKS())
	if err != nil {
		t.Fatal(err)
	}
	subjects := []string{h.AS1.Subject(identityharness.Alice), h.AS1.Subject(identityharness.Bob)}
	if opts.distrustAlice {
		subjects = subjects[1:]
	}
	process := mcpbrokertest.StartAS2(t, identitybroker.BrokerClientID, keySet, mcpbrokertest.AS1Trust{
		Issuer: h.AS1.Issuer(), JWKSURL: h.AS1.JWKSURL(), CAFile: h.WriteCACert(t, t.TempDir()),
		Subjects: subjects,
	})
	as2 := process.AS2
	h.AS1.AddIDJAGTarget(as2.Issuer, as2.Resource)

	as1HTTP := h.HTTPClient()
	as1Faults := identityharness.NewFaultTransport(as1HTTP.Transport, h.AS1.TokenEndpoint())
	as1HTTP.Transport = as1Faults
	as1, err := authserverclient.New(authserverclient.Config{
		Issuer: h.AS1.Issuer(), TokenEndpoint: h.AS1.TokenEndpoint(), Assertions: signer, HTTPClient: as1HTTP,
	})
	if err != nil {
		t.Fatal(err)
	}
	as2HTTP := inprocesshttp.NewClient(as2.Handler(), func(*url.URL) error { return nil })
	as2Faults := identityharness.NewFaultTransport(as2HTTP.Transport, as2.TokenEndpoint)
	as2HTTP.Transport = as2Faults
	as2Client, err := authserverclient.New(authserverclient.Config{
		Issuer: as2.Issuer, TokenEndpoint: as2.TokenEndpoint, Assertions: as2Signer, HTTPClient: as2HTTP,
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := identitybroker.NewMemoryRecorder()
	b := newBroker(t,
		identitybroker.WithAuthServer(as1),
		identitybroker.WithIDJAGIssuer(as1),
		identitybroker.WithRecorder(rec),
		identitybroker.WithGateway(identitybroker.Gateway{
			Servers: []string{"github"},
			AS2:     identitybroker.GatewayAS2{Issuer: as2.Issuer, Resource: as2.Resource, AuthServer: as2Client},
		}))
	h.AS1.SetActorValidator(as1servertest.HarnessActorValidator(b))
	f := &harnessFixture{
		h: h, b: b, rec: rec, as1: as1Faults, as2: as2Faults, as2Client: as2Client,
		as2Issuer: as2.Issuer, as2Resource: as2.Resource, as1Iss: h.AS1.Issuer(), vmcp: process.Handlers.VMCP,
	}
	f.s, f.cred = f.open(t, openHost, identityharness.Alice)
	return f
}

func (f *harnessFixture) owner(user string) identitybroker.Owner {
	return identitybroker.Owner{Issuer: f.h.IdP.Issuer(), Subject: user}
}

// openToken opens, or reopens, host for user with a fresh AS-1 access token.
// Each host gets its own incarnation, so main agents of two hosts differ.
func (f *harnessFixture) openToken(host, user string) (identitybroker.Session, error) {
	incarnation := openIncarnation
	if host != openHost {
		incarnation = "inc_bbcdefghijklmnopqrstuvwxya"
	}
	return f.b.OpenSession(context.Background(), testWorkload, identitybroker.OpenSessionRequest{
		HostSessionID: host, Incarnation: incarnation, Owner: f.owner(user),
		AccessToken: identitybroker.NewToken(f.h.AS1.MintAccessToken(identityharness.AccessTokenSpec{User: user})),
	})
}

// open opens host for user holding a grant and registers its main agent.
func (f *harnessFixture) open(t *testing.T, host, user string) (identitybroker.Session, identitybroker.Token) {
	t.Helper()
	s, err := f.openToken(host, user)
	if err != nil || !s.HoldsRefreshToken {
		t.Fatalf("OpenSession = %+v, %v; want a session holding a grant", s, err)
	}
	reg, err := f.b.RegisterAgent(context.Background(), testWorkload, mainAgent(s))
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	return s, reg.Credential
}

func (f *harnessFixture) authorize(req identitybroker.AuthorizeCallRequest) (identitybroker.Authorization, error) {
	return f.b.AuthorizeCall(context.Background(), testWorkload, req)
}

func (f *harnessFixture) closeSession(id string) error {
	return f.b.CloseSession(context.Background(), testWorkload, identitybroker.CloseSessionRequest{SessionID: id, Reason: identitybroker.ReasonSessionClosed})
}

// wantCounts fails t unless the broker sent exchanges ID-JAG exchanges to
// AS-1 and redemptions redemptions to AS-2.
func (f *harnessFixture) wantCounts(t *testing.T, exchanges, redemptions int) {
	t.Helper()
	if got, gotR := f.as1.Count(identityharness.KindIDJAG), f.as2.Count(identityharness.KindRedemption); got != exchanges || gotR != redemptions {
		t.Fatalf("sent %d ID-JAG exchanges and %d redemptions, want %d and %d", got, gotR, exchanges, redemptions)
	}
}

// requests returns the requests of kind the broker sent through ft.
func requests(ft *identityharness.FaultTransport, kind identityharness.GrantKind) []identityharness.GrantRequest {
	var out []identityharness.GrantRequest
	for _, r := range ft.Requests() {
		if r.Kind == kind && r.Fault != identityharness.FaultDuplicate {
			out = append(out, r)
		}
	}
	return out
}

// jwtPart returns the JSON members of a compact JWS's header (0) or claims
// (1).
func jwtPart(t *testing.T, token string, i int) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("a token has %d parts, want a compact JWS", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[i])
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func unixClaim(t *testing.T, claims map[string]any, name string) time.Time {
	t.Helper()
	n, ok := claims[name].(float64)
	if !ok {
		t.Fatalf("claim %s = %v, want a number", name, claims[name])
	}
	return time.Unix(int64(n), 0)
}

// TestAuthorizeCallGetsOneGatewayTokenPerCallAtAS2 shows each call gets its
// own ID-JAG and gateway token, nothing cached, and that the real AS-2's
// token names the agent as the actor, the broker as the client, and the vMCP
// as its audience (#2535 done-when 1 and 5).
func TestAuthorizeCallGetsOneGatewayTokenPerCallAtAS2(t *testing.T) {
	f := newHarnessFixture(t)
	seen := map[string]bool{}
	for i := range 2 {
		req := callRequest(f.s.ID, f.cred)
		req.OperationID = fmt.Sprintf("op-%d", i)
		got, err := f.authorize(req)
		if err != nil {
			t.Fatalf("AuthorizeCall %d: %v", i, err)
		}
		f.wantCounts(t, i+1, i+1)
		redeemed := requests(f.as2, identityharness.KindRedemption)[i]
		issued := requests(f.as1, identityharness.KindIDJAG)[i]
		if redeemed.Assertion == "" || redeemed.Assertion != issued.IssuedToken || redeemed.IssuedToken != got.GatewayToken.Value() {
			t.Fatalf("call %d redeemed something other than its ID-JAG, or returned another token", i)
		}
		jag := jwtPart(t, redeemed.Assertion, 1)
		for _, v := range []string{jag["jti"].(string), got.GatewayToken.Value(), got.AttemptID} {
			if seen[v] {
				t.Fatalf("call %d repeats an ID-JAG, gateway token, or attempt", i)
			}
			seen[v] = true
		}
		agentID := f.rec.IDJAGs()[i].AgentID
		v := jwtPart(t, got.GatewayToken.Value(), 1)
		if v["client_id"] != identitybroker.BrokerClientID ||
			!reflect.DeepEqual(v["act"], map[string]any{"iss": f.as1Iss, "sub": agentID}) ||
			!reflect.DeepEqual(v["aud"], []any{f.as2Resource}) ||
			v["sub"] != f.as1Iss+"#"+f.h.AS1.Subject(identityharness.Alice) {
			t.Fatalf("call %d's gateway token claims = %v, want act AS-1 and %s, client_id %s, and aud the vMCP", i, v, agentID, identitybroker.BrokerClientID)
		}
		if !strings.HasPrefix(agentID, "ag_") {
			t.Fatalf("the acting agent is %q, want an ag_ identifier", agentID)
		}
	}
	jags := f.h.AS1.IDJAGsIssued()
	if len(jags) != 2 || jags[0].JTI == jags[1].JTI || jags[0].Operation != "op-0" || jags[1].Operation != "op-1" {
		t.Fatalf("AS-1 issued %+v, want one ID-JAG per operation", jags)
	}
	d := f.rec.Decisions()
	if len(d) != 2 || d[0].Outcome != identitybroker.OutcomeSuccess || d[1].Outcome != identitybroker.OutcomeSuccess ||
		d[0].GatewayToken.Issuer != f.as2Issuer || d[0].GatewayToken.ID == "" || d[0].GatewayToken == d[1].GatewayToken {
		t.Fatalf("decisions = %+v, want two successes naming distinct AS-2 tokens", d)
	}
}

// TestAuthorizeCallGatewayTokenExpiresWithTheIDJAGAtAS2 shows the gateway
// token, and the expiry the broker reports for it, end no later than the
// ID-JAG it was redeemed for (#2535 done-when 2).
func TestAuthorizeCallGatewayTokenExpiresWithTheIDJAGAtAS2(t *testing.T) {
	f := newHarnessFixture(t)
	got, err := f.authorize(callRequest(f.s.ID, f.cred))
	if err != nil {
		t.Fatalf("AuthorizeCall: %v", err)
	}
	recorded := f.rec.IDJAGs()
	if len(recorded) != 1 || recorded[0].ExpiresAt.IsZero() {
		t.Fatalf("ID-JAG records = %+v, want one with an expiry", recorded)
	}
	idjagExp := recorded[0].ExpiresAt
	if issued := f.h.AS1.IDJAGsIssued(); len(issued) != 1 || !issued[0].ExpiresAt.Equal(idjagExp) {
		t.Fatalf("AS-1 issued %+v, want one expiring at the recorded %v", issued, idjagExp)
	}
	if got.ExpiresAt.IsZero() || got.ExpiresAt.After(idjagExp) {
		t.Fatalf("ExpiresAt = %v, want no later than the ID-JAG's exp %v", got.ExpiresAt, idjagExp)
	}
	if vExp := unixClaim(t, jwtPart(t, got.GatewayToken.Value(), 1), "exp"); vExp.After(idjagExp) {
		t.Fatalf("the gateway token expires at %v, after the ID-JAG's exp %v", vExp, idjagExp)
	}
}

// TestAuthorizeCallIDJAGShapeAtAS2 shows what the broker redeems is an
// ID-JAG, and AS-1 recorded the call's operation with it.
func TestAuthorizeCallIDJAGShapeAtAS2(t *testing.T) {
	f := newHarnessFixture(t)
	req := callRequest(f.s.ID, f.cred)
	req.OperationID = "op-pr42-001"
	if _, err := f.authorize(req); err != nil {
		t.Fatalf("AuthorizeCall: %v", err)
	}
	redeemed := requests(f.as2, identityharness.KindRedemption)
	if len(redeemed) != 1 {
		t.Fatalf("%d redemptions, want 1", len(redeemed))
	}
	if typ := jwtPart(t, redeemed[0].Assertion, 0)["typ"]; typ != identityharness.IDJAGHeaderType {
		t.Fatalf("the redeemed assertion's typ = %v, want %s", typ, identityharness.IDJAGHeaderType)
	}
	jag := jwtPart(t, redeemed[0].Assertion, 1)
	issued := f.h.AS1.IDJAGsIssued()
	if len(issued) != 1 {
		t.Fatalf("AS-1 issued %d ID-JAGs, want 1", len(issued))
	}
	want := identityharness.IDJAGIssuance{
		JTI: jag["jti"].(string), Subject: f.h.AS1.Subject(identityharness.Alice), Owner: f.h.Owner(identityharness.Alice),
		AgentID: f.rec.IDJAGs()[0].AgentID, Audience: f.as2Issuer, Resource: f.as2Resource, Operation: "op-pr42-001",
		ExpiresAt: issued[0].ExpiresAt,
	}
	if issued[0] != want {
		t.Fatalf("AS-1 issued %+v, want %+v", issued[0], want)
	}
	if r := f.rec.IDJAGs()[0]; r.IDJAG.Issuer != f.as1Iss || r.IDJAG.ID != want.JTI || r.OperationID != "op-pr42-001" {
		t.Fatalf("the ID-JAG record = %+v, want AS-1's jti for the operation", r)
	}
}

// TestAuthorizeCallWriteOrderAtAS2 shows the ID-JAG is recorded before AS-2
// sees it (#2535 done-when 3).
func TestAuthorizeCallWriteOrderAtAS2(t *testing.T) {
	f := newHarnessFixture(t)
	var atRedemption []identitybroker.IDJAGRecord
	f.as2.OnNext(identityharness.KindRedemption, func() { atRedemption = f.rec.IDJAGs() })
	if _, err := f.authorize(callRequest(f.s.ID, f.cred)); err != nil {
		t.Fatalf("AuthorizeCall: %v", err)
	}
	redeemed := requests(f.as2, identityharness.KindRedemption)
	if len(atRedemption) != 1 || len(redeemed) != 1 || atRedemption[0].IDJAG.ID != jwtPart(t, redeemed[0].Assertion, 1)["jti"] {
		t.Fatalf("at the redemption the recorder held %+v, want the redeemed ID-JAG", atRedemption)
	}
	if d := f.rec.Decisions(); len(d) != 1 || d[0].Outcome != identitybroker.OutcomeSuccess {
		t.Fatalf("decisions = %+v, want one success", d)
	}
}

// callGateway sends a tools/call to the gateway stand-in with token and
// returns the status and WWW-Authenticate.
func callGateway(t *testing.T, f *harnessFixture, g *identityharness.Gateway, token identitybroker.Token) (int, string) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"github.pull_request_read","arguments":{}}}`
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, g.URL(), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token.Value())
	resp, err := f.h.HTTPClient().Do(req)
	if err != nil {
		t.Fatalf("gateway call: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("WWW-Authenticate")
}

// TestAuthorizeCallRejectedTokenRetryAtGateway reports a token the gateway
// rejected and gets one fresh one for it, once (#2535 done-when 1, 4, and 10).
func TestAuthorizeCallRejectedTokenRetryAtGateway(t *testing.T) {
	f := newHarnessFixture(t)
	// The gateway accepts only gateway tokens AS-2 issued.
	g := f.h.StartGateway(t, identityharness.GatewayOptions{Verify: func(token string) error {
		for _, r := range requests(f.as2, identityharness.KindRedemption) {
			if r.IssuedToken != "" && r.IssuedToken == token {
				return nil
			}
		}
		return errors.New("not a gateway token AS-2 issued")
	}})
	report := func(id string) identitybroker.AuthorizeCallRequest {
		req := callRequest(f.s.ID, f.cred)
		req.RejectedAttemptID = id
		return req
	}

	first, err := f.authorize(callRequest(f.s.ID, f.cred))
	if err != nil {
		t.Fatalf("AuthorizeCall: %v", err)
	}
	g.RejectNextToken()
	if status, challenge := callGateway(t, f, g, first.GatewayToken); status != http.StatusUnauthorized || !strings.Contains(challenge, `error="invalid_token"`) {
		t.Fatalf("the gateway answered %d %q, want 401 invalid_token", status, challenge)
	}
	retry, err := f.authorize(report(first.AttemptID))
	if err != nil {
		t.Fatalf("the retry: %v", err)
	}
	if retry.AttemptID == first.AttemptID || retry.GatewayToken.Value() == first.GatewayToken.Value() {
		t.Fatal("the retry repeated the first attempt or its token")
	}
	f.wantCounts(t, 2, 2)
	if status, _ := callGateway(t, f, g, retry.GatewayToken); status != http.StatusOK {
		t.Fatalf("the gateway answered the retry's token with %d, want 200", status)
	}
	calls := g.Calls()
	if len(calls) != 2 || calls[0].TokenJTI == calls[1].TokenJTI || calls[1].TokenJTI != f.rec.Decisions()[1].GatewayToken.ID {
		t.Fatalf("gateway calls = %+v, want the rejected token then the retry's", calls)
	}
	if d := f.rec.Decisions()[1]; d.RejectedAttemptID != first.AttemptID || d.AttemptID != retry.AttemptID || d.Outcome != identitybroker.OutcomeSuccess {
		t.Fatalf("the retry's decision = %+v", d)
	}

	// The retry's attempt, the first attempt again, and an unknown attempt
	// are refused without reaching AS-1 or AS-2.
	for _, id := range []string{retry.AttemptID, first.AttemptID, "att_unknown"} {
		_, err := f.authorize(report(id))
		wantError(t, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied, f.cred)
	}
	f.wantCounts(t, 2, 2)

	// A call that reports nothing is authorized as before.
	if _, err := f.authorize(callRequest(f.s.ID, f.cred)); err != nil {
		t.Fatalf("an unflagged AuthorizeCall after the refusals: %v", err)
	}
	f.wantCounts(t, 3, 3)
}

// TestAuthorizeCallRevokedRulesAtAS1 runs the revocation rules against the
// harness's AS-1 (#2535 done-when 4).
func TestAuthorizeCallRevokedRulesAtAS1(t *testing.T) {
	closesSession := func(t *testing.T, f *harnessFixture, err error) {
		t.Helper()
		wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed, f.cred)
		if _, open := findSession(t, f.b, openHost); open {
			t.Fatal("the session is still open")
		}
	}

	t.Run("AS-1 revoking the user's grants closes the session", func(t *testing.T) {
		f := newHarnessFixture(t)
		if n := f.h.AS1.RevokeGrants(identityharness.Alice); n != 1 {
			t.Fatalf("AS-1 revoked %d grants, want 1", n)
		}
		_, err := f.authorize(callRequest(f.s.ID, f.cred))
		closesSession(t, f, err)
		f.wantCounts(t, 1, 0)
		if r := requests(f.as1, identityharness.KindIDJAG); r[0].Reason != "revoked" || !r[0].Reached {
			t.Fatalf("AS-1 answered %+v, want its own revoked", r[0])
		}
		_, err = f.authorize(callRequest(f.s.ID, f.cred))
		closesSession(t, f, err)
		f.wantCounts(t, 1, 0)
	})

	t.Run("parent_revoked closes the session", func(t *testing.T) {
		f := newHarnessFixture(t)
		f.as1.FailNext(identityharness.KindIDJAG, identityharness.Fault{Error: "invalid_request", Reason: "parent_revoked"})
		_, err := f.authorize(callRequest(f.s.ID, f.cred))
		closesSession(t, f, err)
		f.wantCounts(t, 1, 0)
	})

	t.Run("a newer grant is retried", func(t *testing.T) {
		f := newHarnessFixture(t)
		old, _ := identitybroker.SessionGrant(f.b, f.s.ID)
		var reopenErr, revokeErr error
		// The session is reopened with a fresh token and AS-1 revokes the old
		// grant while the call's exchange is on its way.
		f.as1.OnNext(identityharness.KindIDJAG, func() {
			_, reopenErr = f.openToken(openHost, identityharness.Alice)
			revokeErr = f.h.ScriptedBroker().Revoke(context.Background(), old.RefreshToken.Value())
		})
		got, err := f.authorize(callRequest(f.s.ID, f.cred))
		if reopenErr != nil || revokeErr != nil {
			t.Fatalf("reopen: %v; revoke: %v", reopenErr, revokeErr)
		}
		if err != nil || got.GatewayToken.IsZero() {
			t.Fatalf("AuthorizeCall = %+v, %v; want a gateway token from the newer grant", got, err)
		}
		f.wantCounts(t, 2, 1)
		if r := requests(f.as1, identityharness.KindIDJAG); r[0].Reason != "revoked" || r[1].IssuedToken == "" {
			t.Fatalf("AS-1 answered %+v, want revoked then an ID-JAG", r)
		}
		newer, ok := identitybroker.SessionGrant(f.b, f.s.ID)
		if !ok || newer.RefreshToken.Value() == old.RefreshToken.Value() {
			t.Fatal("the session does not hold a newer grant")
		}
	})

	t.Run("a close during the exchange stops the redemption", func(t *testing.T) {
		f := newHarnessFixture(t)
		var closeErr error
		f.as1.OnNext(identityharness.KindIDJAG, func() { closeErr = f.closeSession(f.s.ID) })
		got, err := f.authorize(callRequest(f.s.ID, f.cred))
		if closeErr != nil {
			t.Fatalf("CloseSession: %v", closeErr)
		}
		if !got.GatewayToken.IsZero() {
			t.Fatal("a call in a closed session returned a gateway token")
		}
		// AS-1's actor check denies the closed session's agent; the call
		// still reports the closing reason.
		if r := requests(f.as1, identityharness.KindIDJAG); r[0].Reason != "denied" {
			t.Fatalf("AS-1 answered %+v, want its own denied", r[0])
		}
		closesSession(t, f, err)
		f.wantCounts(t, 1, 0)
		if d := f.rec.Decisions(); len(d) != 1 || d[0].Reason != identitybroker.ReasonSessionClosed {
			t.Fatalf("decisions = %+v, want one session_closed", d)
		}
	})

	t.Run("a close during the redemption drops the token", func(t *testing.T) {
		f := newHarnessFixture(t)
		var closeErr error
		f.as2.OnNext(identityharness.KindRedemption, func() { closeErr = f.closeSession(f.s.ID) })
		got, err := f.authorize(callRequest(f.s.ID, f.cred))
		if closeErr != nil {
			t.Fatalf("CloseSession: %v", closeErr)
		}
		if !got.GatewayToken.IsZero() {
			t.Fatal("a call in a closed session returned a gateway token")
		}
		closesSession(t, f, err)
		f.wantCounts(t, 1, 1)
		if r := requests(f.as2, identityharness.KindRedemption); r[0].IssuedToken == "" {
			t.Fatal("AS-2 issued no gateway token, so nothing was dropped")
		}
		for _, d := range f.rec.Decisions() {
			if d.Outcome == identitybroker.OutcomeSuccess {
				t.Fatalf("recorded a success for a call in a closed session: %+v", d)
			}
		}
	})
}

// distinctClientAssertions fails t if any two requests through the fault
// transports carried the same client assertion.
func distinctClientAssertions(t *testing.T, transports ...*identityharness.FaultTransport) {
	t.Helper()
	seen := map[string]bool{}
	for _, ft := range transports {
		for _, r := range ft.Requests() {
			if r.Fault == identityharness.FaultDuplicate {
				continue
			}
			if r.ClientAssertion == "" || seen[r.ClientAssertion] {
				t.Fatal("a request carried no client assertion, or one sent before")
			}
			seen[r.ClientAssertion] = true
		}
	}
}

// TestAuthorizeCallLostResponsesAtAS2 shows a lost answer from either server
// gets a new ID-JAG, never a resubmission (#2535 done-when 4).
func TestAuthorizeCallLostResponsesAtAS2(t *testing.T) {
	t.Run("a lost ID-JAG", func(t *testing.T) {
		f := newHarnessFixture(t)
		f.as1.LoseNextResponse(identityharness.KindIDJAG)
		if _, err := f.authorize(callRequest(f.s.ID, f.cred)); err != nil {
			t.Fatalf("AuthorizeCall: %v", err)
		}
		f.wantCounts(t, 2, 1)
		issued := f.h.AS1.IDJAGsIssued()
		redeemed := requests(f.as2, identityharness.KindRedemption)
		if len(issued) != 2 || jwtPart(t, redeemed[0].Assertion, 1)["jti"] != issued[1].JTI {
			t.Fatalf("AS-1 issued %d ID-JAGs; want 2, the second redeemed", len(issued))
		}
		if r := f.rec.IDJAGs(); len(r) != 1 || r[0].IDJAG.ID != issued[1].JTI {
			t.Fatalf("ID-JAG records = %+v, want only the one the broker received", r)
		}
		distinctClientAssertions(t, f.as1, f.as2)
	})

	t.Run("a lost gateway token", func(t *testing.T) {
		f := newHarnessFixture(t)
		f.as2.LoseNextResponse(identityharness.KindRedemption)
		got, err := f.authorize(callRequest(f.s.ID, f.cred))
		if err != nil {
			t.Fatalf("AuthorizeCall: %v", err)
		}
		f.wantCounts(t, 2, 2)
		redeemed := requests(f.as2, identityharness.KindRedemption)
		if redeemed[0].Assertion == redeemed[1].Assertion {
			t.Fatal("an ID-JAG was redeemed twice")
		}
		if !redeemed[0].Reached || redeemed[0].IssuedToken == "" || got.GatewayToken.Value() != redeemed[1].IssuedToken {
			t.Fatal("want AS-2 to issue a lost token and the call to return the second")
		}
		if r := f.rec.IDJAGs(); len(r) != 2 || r[0].IDJAG.ID == r[1].IDJAG.ID {
			t.Fatalf("ID-JAG records = %+v, want both ID-JAGs", r)
		}
		distinctClientAssertions(t, f.as1, f.as2)
	})
}

// TestAuthorizeCallMapsAS1RefusalsAtAS1 maps AS-1's refusals of the ID-JAG
// exchange, injected where the harness does not model them; none reaches
// AS-2 or closes the session (#2535 done-when 7).
func TestAuthorizeCallMapsAS1RefusalsAtAS1(t *testing.T) {
	f := newHarnessFixture(t)
	for _, tc := range []struct {
		fault  identityharness.Fault
		code   identitybroker.Code
		reason identitybroker.Reason
	}{
		{identityharness.Fault{Error: "invalid_request", Reason: "svid_expired"}, identitybroker.CodeUnauthenticated, identitybroker.ReasonSVIDExpired},
		{identityharness.Fault{Error: "invalid_request", Reason: "denied"}, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied},
		{identityharness.Fault{Error: "invalid_request", Reason: "signed_out"}, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSignedOut},
		{identityharness.Fault{Error: "invalid_request", Reason: "upstream_revoked"}, identitybroker.CodePermissionDenied, identitybroker.ReasonUpstreamRevoked},
		{identityharness.Fault{Error: "invalid_request", Reason: "owner_action_required"}, identitybroker.CodeFailedPrecondition, identitybroker.ReasonOwnerActionRequired},
		{identityharness.Fault{Error: "invalid_request", Reason: "grant_invalid"}, identitybroker.CodeInternal, ""},
		{identityharness.Fault{Error: "invalid_target"}, identitybroker.CodeInternal, ""},
		{identityharness.Fault{Status: http.StatusServiceUnavailable, Error: "server_error"}, identitybroker.CodeUnavailable, ""},
	} {
		name := tc.fault.Reason
		if name == "" {
			name = tc.fault.Error
		}
		t.Run(name, func(t *testing.T) {
			exchanges, redemptions, decisions := f.as1.Count(identityharness.KindIDJAG), f.as2.Count(identityharness.KindRedemption), len(f.rec.Decisions())
			f.as1.FailNext(identityharness.KindIDJAG, tc.fault)
			_, err := f.authorize(callRequest(f.s.ID, f.cred))
			wantError(t, err, tc.code, tc.reason, f.cred)
			f.wantCounts(t, exchanges+1, redemptions)
			d := f.rec.Decisions()
			if len(d) != decisions+1 || d[decisions].Code != tc.code || d[decisions].Reason != tc.reason {
				t.Fatalf("decisions = %+v, want one more with %s %q", d[decisions:], tc.code, tc.reason)
			}
			// The session stays open and its grant still works at AS-1.
			if _, err := f.authorize(callRequest(f.s.ID, f.cred)); err != nil {
				t.Fatalf("AuthorizeCall after the refusal: %v", err)
			}
			f.wantCounts(t, exchanges+2, redemptions+1)
		})
	}
}

// TestAS2RefusesTheBrokersReusedIDJAG shows the real AS-2 redeems an ID-JAG
// the broker obtained once only (#2535 done-when 5).
func TestAS2RefusesTheBrokersReusedIDJAG(t *testing.T) {
	f := newHarnessFixture(t)
	f.as2.DuplicateNext(identityharness.KindRedemption)
	got, err := f.authorize(callRequest(f.s.ID, f.cred))
	if err != nil {
		t.Fatalf("AuthorizeCall: %v", err)
	}
	f.wantCounts(t, 1, 1)
	var original, dup identityharness.GrantRequest
	for _, r := range f.as2.Requests() {
		if r.Fault == identityharness.FaultDuplicate {
			dup = r
		} else {
			original = r
		}
	}
	if original.IssuedToken != got.GatewayToken.Value() || !dup.Reached || dup.Status < http.StatusBadRequest || dup.IssuedToken != "" {
		t.Fatalf("the resubmitted redemption was answered %d %q with a token %v; want a refusal", dup.Status, dup.Code, dup.IssuedToken != "")
	}

	// The same ID-JAG with a fresh client assertion is refused too.
	_, err = f.as2Client.Redeem(t.Context(), identitybroker.Redemption{Assertion: identitybroker.NewToken(original.Assertion), Resource: f.as2Resource})
	var refusal *identitybroker.ServerRefusal
	if !errors.As(err, &refusal) || refusal.Code != "invalid_grant" {
		t.Fatalf("redeeming the ID-JAG again = %v, want invalid_grant", err)
	}
	last := f.as2.Requests()[len(f.as2.Requests())-1]
	if last.ClientAssertion == original.ClientAssertion || last.Assertion != original.Assertion || last.IssuedToken != "" {
		t.Fatal("want a fresh client assertion for the same ID-JAG, and no token")
	}
}

// vmcpStatus is the status of an MCP initialize at f's real vMCP with token
// as its Bearer token; a 200 must carry a JSON-RPC result. A session it opens
// is ended, so the vMCP lets its backend go.
func vmcpStatus(t *testing.T, f *harnessFixture, token string) int {
	t.Helper()
	send := func(method, body, sessionID string) *http.Response {
		req := httptest.NewRequestWithContext(t.Context(), method, f.as2Resource, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if sessionID != "" {
			req.Header.Set("Mcp-Session-Id", sessionID)
		}
		rec := httptest.NewRecorder()
		f.vmcp.ServeHTTP(rec, req)
		return rec.Result()
	}
	const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"identitybroker-test","version":"1.0"}}}`
	resp := send(http.MethodPost, initialize, "")
	if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
		send(http.MethodDelete, "", id)
	}
	if resp.StatusCode == http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil || !identityharness.HasJSONRPCResult(body) {
			t.Fatalf("vMCP initialize answered 200 without a JSON-RPC result: %v", err)
		}
	}
	return resp.StatusCode
}

// TestRealVMCPAcceptsTheBrokersGatewayToken shows the real vMCP accepts the
// gateway token the broker obtained for a call, and refuses a token AS-2 did
// not sign, though it names AS-2's key ID.
func TestRealVMCPAcceptsTheBrokersGatewayToken(t *testing.T) {
	f := newHarnessFixture(t)
	if f.vmcp == nil {
		t.Fatal("the MCP broker process serves no vMCP")
	}
	got, err := f.authorize(callRequest(f.s.ID, f.cred))
	if err != nil {
		t.Fatalf("AuthorizeCall: %v", err)
	}
	if status := vmcpStatus(t, f, got.GatewayToken.Value()); status != http.StatusOK {
		t.Fatalf("vMCP initialize with the gateway token: status %d, want 200", status)
	}

	// A token signed here with a fresh key under AS-2's key ID: the vMCP
	// refuses it on its signature, whatever its claims. A key ID AS-2 does not
	// hold, such as AS-1's, would send the vMCP to fetch AS-2's JWKS over the
	// network.
	claims := jwt.MapClaims(jwtPart(t, got.GatewayToken.Value(), 1))
	claims["aud"] = []string{f.as2Issuer}
	other := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	kid, _ := jwtPart(t, got.GatewayToken.Value(), 0)["kid"].(string)
	if kid == "" {
		t.Fatal("the gateway token names no key ID")
	}
	other.Header["kid"] = kid
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := other.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	if status := vmcpStatus(t, f, signed); status != http.StatusUnauthorized {
		t.Fatalf("vMCP initialize with a token AS-2 did not sign: status %d, want 401", status)
	}
}

// TestAuthorizeCallMapsRealAS2Refusals drives refusals from the real AS-2.
func TestAuthorizeCallMapsRealAS2Refusals(t *testing.T) {
	t.Run("a user AS-2 does not trust", func(t *testing.T) {
		f := newHarnessFixtureWith(t, harnessOptions{distrustAlice: true})
		_, err := f.authorize(callRequest(f.s.ID, f.cred))
		wantError(t, err, identitybroker.CodeInternal, "", f.cred)
		// A reasonless invalid_grant is retried once with a new ID-JAG.
		f.wantCounts(t, 2, 2)
		redeemed := requests(f.as2, identityharness.KindRedemption)
		if redeemed[0].Assertion == redeemed[1].Assertion {
			t.Fatal("an ID-JAG was redeemed twice")
		}
		// AS-2's error_description is prose, which the broker reads as no
		// reason code.
		for i, r := range redeemed {
			reason := identitybroker.NewServerRefusal(r.Status, r.Code, r.Reason).Reason
			if !r.Reached || r.Code != "invalid_grant" || (reason != "" && reason != "unknown") || r.IssuedToken != "" {
				t.Fatalf("AS-2 answered redemption %d with %d %q reason %q, want a reasonless invalid_grant", i, r.Status, r.Code, reason)
			}
		}
	})

	t.Run("a client key AS-2 does not know", func(t *testing.T) {
		f := newHarnessFixtureWith(t, harnessOptions{unknownAS2Key: true})
		_, err := f.authorize(callRequest(f.s.ID, f.cred))
		wantError(t, err, identitybroker.CodeInternal, "", f.cred)
		f.wantCounts(t, 1, 1)
		if r := requests(f.as2, identityharness.KindRedemption)[0]; !r.Reached || r.Code != "invalid_client" || r.IssuedToken != "" {
			t.Fatalf("AS-2 answered %d %q, want invalid_client", r.Status, r.Code)
		}
	})
}

// TestAuthorizeCallNeverSharesTokensAtAS2 shows each user's session gets
// gateway tokens for that user only, and a closed session gets none
// (#2535 done-when 9).
func TestAuthorizeCallNeverSharesTokensAtAS2(t *testing.T) {
	f := newHarnessFixture(t)
	bobSession, bobCred := f.open(t, "hostS43", identityharness.Bob)
	alice, err := f.authorize(callRequest(f.s.ID, f.cred))
	if err != nil {
		t.Fatalf("Alice's AuthorizeCall: %v", err)
	}
	bob, err := f.authorize(callRequest(bobSession.ID, bobCred))
	if err != nil {
		t.Fatalf("Bob's AuthorizeCall: %v", err)
	}
	aliceSub, bobSub := jwtPart(t, alice.GatewayToken.Value(), 1)["sub"], jwtPart(t, bob.GatewayToken.Value(), 1)["sub"]
	if aliceSub != f.as1Iss+"#"+f.h.AS1.Subject(identityharness.Alice) || bobSub != f.as1Iss+"#"+f.h.AS1.Subject(identityharness.Bob) || aliceSub == bobSub {
		t.Fatalf("gateway token subjects are %v and %v, want Alice's and Bob's", aliceSub, bobSub)
	}
	if issued := f.h.AS1.IDJAGsIssued(); len(issued) != 2 || issued[0].Owner != f.h.Owner(identityharness.Alice) || issued[1].Owner != f.h.Owner(identityharness.Bob) {
		t.Fatalf("AS-1 issued %+v, want one ID-JAG for Alice and one for Bob", issued)
	}
	// An agent of one session cannot act in the other.
	_, err = f.authorize(callRequest(bobSession.ID, f.cred))
	wantError(t, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied, f.cred)
	f.wantCounts(t, 2, 2)

	if err := f.closeSession(f.s.ID); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	_, err = f.authorize(callRequest(f.s.ID, f.cred))
	wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed, f.cred)
	f.wantCounts(t, 2, 2)
}

// TestAuthorizeCallSessionsOfOneUserGetTheirOwnTokensAtAS2 shows two sessions
// of the same user get distinct gateway tokens and attempts.
func TestAuthorizeCallSessionsOfOneUserGetTheirOwnTokensAtAS2(t *testing.T) {
	f := newHarnessFixture(t)
	otherSession, otherCred := f.open(t, "hostS43", identityharness.Alice)
	first, err := f.authorize(callRequest(f.s.ID, f.cred))
	if err != nil {
		t.Fatalf("AuthorizeCall in the first session: %v", err)
	}
	second, err := f.authorize(callRequest(otherSession.ID, otherCred))
	if err != nil {
		t.Fatalf("AuthorizeCall in the second session: %v", err)
	}
	if first.GatewayToken.Value() == second.GatewayToken.Value() || first.AttemptID == second.AttemptID {
		t.Fatal("two sessions of one user share a gateway token or an attempt")
	}
	if a, b := jwtPart(t, first.GatewayToken.Value(), 1)["sub"], jwtPart(t, second.GatewayToken.Value(), 1)["sub"]; a != b {
		t.Fatalf("gateway token subjects are %v and %v, want the same user", a, b)
	}
	f.wantCounts(t, 2, 2)
}

// TestAuthorizeCallKeepsTokensSecretAtAS2 looks for every refresh token,
// ID-JAG, and gateway token the servers issued wherever the broker reports
// on a call (#2535 done-when 8, the core part).
func TestAuthorizeCallKeepsTokensSecretAtAS2(t *testing.T) {
	f := newHarnessFixture(t)
	auth, err := f.authorize(callRequest(f.s.ID, f.cred))
	if err != nil {
		t.Fatalf("AuthorizeCall: %v", err)
	}
	var errs []error
	// A refusal after an ID-JAG was issued, and one after a gateway token was
	// issued and dropped.
	f.as2.FailNext(identityharness.KindRedemption, identityharness.Fault{Error: "invalid_grant"})
	f.as2.FailNext(identityharness.KindRedemption, identityharness.Fault{Error: "invalid_grant"})
	_, err = f.authorize(callRequest(f.s.ID, f.cred))
	errs = append(errs, err)
	f.as1.FailNext(identityharness.KindIDJAG, identityharness.Fault{Error: "invalid_request", Reason: "denied"})
	_, err = f.authorize(callRequest(f.s.ID, f.cred))
	errs = append(errs, err)
	f.as2.OnNext(identityharness.KindRedemption, func() {
		if err := f.closeSession(f.s.ID); err != nil {
			panic(err)
		}
	})
	_, err = f.authorize(callRequest(f.s.ID, f.cred))
	errs = append(errs, err)

	secrets := []string{f.cred.Value()}
	var assertions []string
	for _, ft := range []*identityharness.FaultTransport{f.as1, f.as2} {
		for _, r := range ft.Requests() {
			secrets = append(secrets, r.IssuedToken, r.Assertion)
			assertions = append(assertions, r.ClientAssertion)
		}
	}
	secrets = slices.Compact(slices.Sorted(slices.Values(slices.DeleteFunc(secrets, func(s string) bool { return s == "" }))))
	// The credential, the session's refresh token, four ID-JAGs, and the
	// gateway tokens of the first and last calls.
	if len(secrets) != 1+1+4+2 {
		t.Fatalf("captured %d distinct tokens, want 8", len(secrets))
	}

	var out strings.Builder
	for _, err := range errs {
		if err == nil {
			t.Fatal("a refusal returned no error")
		}
		fmt.Fprintf(&out, "%v %+v\n", err, err)
	}
	fmt.Fprintf(&out, "%v %+v %#v\n", auth, auth, auth)
	for _, rec := range []any{f.rec.IDJAGs(), f.rec.Decisions()} {
		raw, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&out, "%+v %s\n", rec, raw)
	}
	for _, a := range assertions {
		fmt.Fprintf(&out, "%s %v %v\n", a, jwtPart(t, a, 0), jwtPart(t, a, 1))
	}
	out.WriteString(strings.Join(os.Environ(), "\n"))
	for _, s := range secrets {
		if strings.Contains(out.String(), s) {
			t.Fatal("an error, record, client assertion, the environment, or a formatted authorization carries a token")
		}
	}
}
