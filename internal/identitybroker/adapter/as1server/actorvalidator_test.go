package as1server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/authserverclient"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

const (
	testActIssuer      = "spiffe://agents.test"
	testTargetIssuer   = "https://as2.test"
	testTargetResource = "https://vmcp.test/mcp"
	testAgentID        = "ag_01jz7m3x5q8v2n4k6p9r1t3w5y"
	testAgentSubject   = "spiffe://agents.test/session/s1/agent/main"
	testActorID        = testActIssuer + "/agent/" + testAgentID
	// leakMarker is in the fake's error text and must reach no response.
	leakMarker = "broker-detail-7c1f"
)

// withIDJAG has the delegate issue ID-JAGs for the test target.
func withIDJAG(_ *Config, d *DelegateClient) {
	d.IDJAG = &IDJAGIssuance{
		ActIssuer: testActIssuer,
		Targets:   []IDJAGTarget{{Issuer: testTargetIssuer, ClientID: "mecatl-at-as2", Resources: []string{testTargetResource}}},
	}
}

// fakeVerifier answers VerifyCredentialForOwner with agent or err and records
// the requests.
type fakeVerifier struct {
	mu    sync.Mutex
	agent identitybroker.VerifiedAgent
	err   error
	got   []identitybroker.VerifyCredentialForOwnerRequest
}

func (*fakeVerifier) VerifyCredential(context.Context, identitybroker.VerifyCredentialRequest) (identitybroker.VerifiedAgent, error) {
	return identitybroker.VerifiedAgent{}, errors.New("fakeVerifier: VerifyCredential is not AS-1's check")
}

func (f *fakeVerifier) VerifyCredentialForOwner(_ context.Context, req identitybroker.VerifyCredentialForOwnerRequest) (identitybroker.VerifiedAgent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, req)
	return f.agent, f.err
}

func (f *fakeVerifier) requests() []identitybroker.VerifyCredentialForOwnerRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]identitybroker.VerifyCredentialForOwnerRequest(nil), f.got...)
}

// allowing is a verifier accepting every credential as the test agent until
// expires.
func allowing(expires time.Time) *fakeVerifier {
	return &fakeVerifier{agent: identitybroker.VerifiedAgent{
		Agent: identitybroker.AgentRef{Subject: testAgentSubject}, AgentID: testAgentID, ActorID: testActorID, ExpiresAt: expires,
	}}
}

// idjagFixture is AS-1 issuing ID-JAGs, the broker's client for it, and a
// grant held for the harness's default user.
type idjagFixture struct {
	h      *identityharness.Harness
	f      *fixture
	signer *identitybroker.ClientAssertionSigner
	client *authserverclient.Client
	grant  identitybroker.Token
}

func startIDJAG(t *testing.T) *idjagFixture {
	t.Helper()
	return startIDJAGWith(t, nil)
}

// startIDJAGWith is startIDJAG after edit, when set, changes the issuance.
func startIDJAGWith(t *testing.T, edit func(*IDJAGIssuance)) *idjagFixture {
	t.Helper()
	h := identityharness.New(t, identityharness.Options{})
	f, signer, _ := startDelegateAS1(t, h, func(c *Config, d *DelegateClient) {
		withIDJAG(c, d)
		if edit != nil {
			edit(d.IDJAG)
		}
	})
	c := delegateClient(t, f, signer)
	res, err := c.Exchange(t.Context(), identitybroker.TokenExchange{
		SubjectToken: identitybroker.NewToken(f.signIn(t, testAudience).AccessToken), Grant: idjagGrant(),
	})
	if err != nil {
		t.Fatalf("session-open Exchange: %v", err)
	}
	return &idjagFixture{h: h, f: f, signer: signer, client: c, grant: res.RefreshToken}
}

// actorToken is a JWT standing in for an agent's SVID; the fake verifier
// does not read it.
func actorToken(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"sub": testAgentSubject, "aud": "mecatl-broker", "exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

// issue asks AS-1 for an ID-JAG for the test target with actor.
func (x *idjagFixture) issue(t *testing.T, actor string) (identitybroker.IDJAGResult, error) {
	t.Helper()
	return x.client.IssueIDJAG(t.Context(), identitybroker.IDJAGRequest{
		SubjectToken: x.grant, ActorToken: identitybroker.NewToken(actor),
		Audience: testTargetIssuer, Resource: testTargetResource,
	})
}

// form is the ID-JAG request form for actor, authenticated as the delegate.
func (x *idjagFixture) form(t *testing.T, actor string) url.Values {
	t.Helper()
	assertion, err := x.signer.ClientAssertion(x.f.server.Issuer(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return url.Values{
		"grant_type":            {grantTypeTokenExchange},
		"subject_token":         {x.grant.Value()},
		"subject_token_type":    {identitybroker.TokenTypeRefreshToken},
		"actor_token":           {actor},
		"actor_token_type":      {identitybroker.TokenTypeJWT},
		"requested_token_type":  {identitybroker.TokenTypeIDJAG},
		"audience":              {testTargetIssuer},
		"resource":              {testTargetResource},
		"client_assertion_type": {clientAssertionType},
		"client_assertion":      {assertion.Value()},
	}
}

// postTokenBody posts form to AS-1's token endpoint in process and returns
// the status and the whole response body.
func postTokenBody(t *testing.T, server *Server, form url.Values) (int, string) {
	t.Helper()
	resp, err := server.InProcessClient().PostForm(server.TokenEndpoint(), form)
	if err != nil {
		t.Fatalf("token POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

// recovers has v allow the test agent and fails t unless the grant then
// issues an ID-JAG: the earlier failure revoked nothing.
func (x *idjagFixture) recovers(t *testing.T, v *fakeVerifier) {
	t.Helper()
	v.mu.Lock()
	v.err, v.agent = nil, allowing(time.Now().Add(time.Minute)).agent
	v.mu.Unlock()
	if _, err := x.issue(t, actorToken(t)); err != nil {
		t.Fatalf("IssueIDJAG once the verifier allows: %v", err)
	}
}

// wantIDJAGRefusal fails t unless err is a refusal with status, code, and
// reason.
func wantIDJAGRefusal(t *testing.T, err error, status int, code, reason string) {
	t.Helper()
	var refusal *identitybroker.ServerRefusal
	if !errors.As(err, &refusal) || refusal.Status != status || refusal.Code != code || refusal.Reason != reason {
		t.Fatalf("IssueIDJAG = %v, want a refusal: HTTP %d %s (%s)", err, status, code, reason)
	}
}

func TestIDJAGNamesTheVerifiedAgent(t *testing.T) {
	x := startIDJAG(t)
	expires := time.Now().Add(30 * time.Second).Truncate(time.Second)
	v := allowing(expires)
	if err := x.f.server.UseCredentialVerifier(v); err != nil {
		t.Fatalf("UseCredentialVerifier: %v", err)
	}
	actor := actorToken(t)

	res, err := x.issue(t, actor)
	if err != nil {
		t.Fatalf("IssueIDJAG: %v", err)
	}
	if err := verify(res.IDJAG.Value(), jwks(t, x.f.server)); err != nil {
		t.Fatalf("the ID-JAG does not verify against AS-1's key set: %v", err)
	}
	_, claims := parse(t, res.IDJAG.Value())
	wantAct := map[string]any{"iss": testActIssuer, "sub": v.agent.ActorID}
	if act, _ := claims["act"].(map[string]any); !mapsEqual(act, wantAct) {
		t.Errorf("act = %v, want %v with no nested act", claims["act"], wantAct)
	}
	wantSubID := map[string]any{"format": "iss_sub", "iss": x.h.IdP.Issuer(), "sub": identityharness.Alice}
	if subID, _ := claims["sub_id"].(map[string]any); !mapsEqual(subID, wantSubID) {
		t.Errorf("sub_id = %v, want %v", claims["sub_id"], wantSubID)
	}
	wantUpstream := map[string]any{"iss": x.h.IdP.Issuer(), "claim": "sub", "value": identityharness.Alice}
	if upstream, _ := claims["https://toolhive.dev/upstream_subject"].(map[string]any); !mapsEqual(upstream, wantUpstream) {
		t.Errorf("upstream_subject = %v, want %v", claims["https://toolhive.dev/upstream_subject"], wantUpstream)
	}
	if exp, err := claims.GetExpirationTime(); err != nil || exp == nil || exp.After(expires) {
		t.Errorf("exp = %v, want at most the credential's expiry %v", exp, expires)
	}
	if aud, _ := claims.GetAudience(); len(aud) != 1 || aud[0] != testTargetIssuer {
		t.Errorf("aud = %v, want [%s]", aud, testTargetIssuer)
	}
	got := v.requests()
	want := identitybroker.Owner{Issuer: x.h.IdP.Issuer(), Subject: identityharness.Alice}
	if len(got) != 1 || got[0].Owner != want || got[0].Credential.Value() != actor {
		t.Fatalf("verifier got %d request(s), want one for owner %+v with the actor token", len(got), want)
	}
}

func TestIDJAGRefusalsFollowTheVerifier(t *testing.T) {
	for name, tc := range map[string]struct {
		err          error
		status       int
		code, reason string
	}{
		"an expired credential": {
			err:    identitybroker.ReasonError(identitybroker.ReasonSVIDExpired, "the SVID expired "+leakMarker),
			status: 400, code: "invalid_request", reason: "actor_token_expired",
		},
		// Another user's session, a closed one, and an untrusted credential
		// all reach AS-1 as this one refusal.
		"a denial": {
			err:    identitybroker.ReasonError(identitybroker.ReasonDenied, "another owner "+leakMarker),
			status: 400, code: "invalid_request", reason: "denied",
		},
		// CredentialVerifier refuses only with ReasonDenied; anything else is
		// a check that could not be made, so it is retryable, not final.
		"a denial with another reason": {
			err:    identitybroker.NewError(identitybroker.CodePermissionDenied, identitybroker.ReasonSessionOwned, leakMarker),
			status: 500, code: "server_error", reason: "unknown",
		},
		// A server_error's description is ToolHive's prose, so no known reason.
		"an unauthenticated error that is not expiry": {
			err:    identitybroker.NewError(identitybroker.CodeUnauthenticated, identitybroker.ReasonTokenUsed, leakMarker),
			status: 500, code: "server_error", reason: "unknown",
		},
		"an outage": {
			err:    identitybroker.NewError(identitybroker.CodeUnavailable, "", leakMarker),
			status: 500, code: "server_error", reason: "unknown",
		},
		"a plain error": {
			err:    errors.New(leakMarker),
			status: 500, code: "server_error", reason: "unknown",
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := startIDJAG(t)
			if err := x.f.server.UseCredentialVerifier(&fakeVerifier{err: tc.err}); err != nil {
				t.Fatalf("UseCredentialVerifier: %v", err)
			}
			_, err := x.issue(t, actorToken(t))
			wantIDJAGRefusal(t, err, tc.status, tc.code, tc.reason)
			status, body := postTokenBody(t, x.f.server, x.form(t, actorToken(t)))
			if status != tc.status || strings.Contains(body, leakMarker) {
				t.Fatalf("raw request: HTTP %d %s; want %d without the verifier's error text", status, body, tc.status)
			}
		})
	}
}

func TestIDJAGOutageRevokesNothing(t *testing.T) {
	x := startIDJAG(t)
	v := &fakeVerifier{err: errors.New(leakMarker)}
	if err := x.f.server.UseCredentialVerifier(v); err != nil {
		t.Fatalf("UseCredentialVerifier: %v", err)
	}
	_, err := x.issue(t, actorToken(t))
	wantIDJAGRefusal(t, err, 500, "server_error", "unknown")

	x.recovers(t, v)
}

// TestIDJAGChecksTheAnswersExpiry covers an accepted credential whose expiry
// has passed: within ToolHive's 60 s it is expired, as for the broker's
// clock-skew acceptance; older, or zero, it is a validator fault.
func TestIDJAGChecksTheAnswersExpiry(t *testing.T) {
	for name, tc := range map[string]struct {
		expires      func() time.Time
		status       int
		code, reason string
	}{
		"two minutes ago": {func() time.Time { return time.Now().Add(-2 * time.Minute) }, 500, "server_error", "unknown"},
		"ten seconds ago": {func() time.Time { return time.Now().Add(-10 * time.Second) }, 400, "invalid_request", "actor_token_expired"},
		"zero":            {func() time.Time { return time.Time{} }, 500, "server_error", "unknown"},
	} {
		t.Run(name, func(t *testing.T) {
			x := startIDJAG(t)
			v := allowing(tc.expires())
			if err := x.f.server.UseCredentialVerifier(v); err != nil {
				t.Fatalf("UseCredentialVerifier: %v", err)
			}
			_, err := x.issue(t, actorToken(t))
			wantIDJAGRefusal(t, err, tc.status, tc.code, tc.reason)
			x.recovers(t, v)
		})
	}
}

func TestIDJAGLifetime(t *testing.T) {
	for name, tc := range map[string]struct {
		lifetime time.Duration
		want     int64
	}{
		"configured": {2 * time.Minute, 120},
		"default":    {0, 60},
	} {
		t.Run(name, func(t *testing.T) {
			x := startIDJAGWith(t, func(i *IDJAGIssuance) { i.Targets[0].Lifetime = tc.lifetime })
			if err := x.f.server.UseCredentialVerifier(allowing(time.Now().Add(time.Hour))); err != nil {
				t.Fatalf("UseCredentialVerifier: %v", err)
			}
			res, err := x.issue(t, actorToken(t))
			if err != nil {
				t.Fatalf("IssueIDJAG: %v", err)
			}
			_, claims := parse(t, res.IDJAG.Value())
			iat, errIat := claims.GetIssuedAt()
			exp, errExp := claims.GetExpirationTime()
			if errIat != nil || errExp != nil || iat == nil || exp == nil || exp.Unix()-iat.Unix() != tc.want {
				t.Fatalf("iat %v, exp %v; want exp - iat = %ds", iat, exp, tc.want)
			}
		})
	}
}

func TestIDJAGBeforeTheVerifierIsBound(t *testing.T) {
	x := startIDJAG(t)
	_, err := x.issue(t, actorToken(t))
	wantIDJAGRefusal(t, err, 500, "server_error", "unknown")

	if err := x.f.server.UseCredentialVerifier(allowing(time.Now().Add(time.Minute))); err != nil {
		t.Fatalf("UseCredentialVerifier: %v", err)
	}
	if _, err := x.issue(t, actorToken(t)); err != nil {
		t.Fatalf("IssueIDJAG once bound: %v", err)
	}
}

func TestIDJAGRequiresAJWTActorToken(t *testing.T) {
	x := startIDJAG(t)
	v := allowing(time.Now().Add(time.Minute))
	if err := x.f.server.UseCredentialVerifier(v); err != nil {
		t.Fatalf("UseCredentialVerifier: %v", err)
	}
	for name, tc := range map[string]struct {
		set       func(url.Values)
		parameter string
	}{
		"no actor token":       {func(f url.Values) { f.Del("actor_token"); f.Del("actor_token_type") }, "'actor_token'"},
		"no actor token type":  {func(f url.Values) { f.Del("actor_token_type") }, "'actor_token_type'"},
		"an access token type": {func(f url.Values) { f.Set("actor_token_type", tokenTypeAccessToken) }, "'actor_token_type'"},
	} {
		t.Run(name, func(t *testing.T) {
			form := x.form(t, actorToken(t))
			tc.set(form)
			status, code, description := postTokenDescribed(t, x.f.server, form)
			if status != 400 || code != "invalid_request" || !strings.Contains(description, tc.parameter) {
				t.Fatalf("HTTP %d %s %q, want 400 invalid_request naming %s", status, code, description, tc.parameter)
			}
		})
	}
	if n := len(v.requests()); n != 0 {
		t.Fatalf("the verifier was called %d time(s), want none", n)
	}
}

func TestUseCredentialVerifierRefuses(t *testing.T) {
	t.Run("without ID-JAG issuance", func(t *testing.T) {
		h := identityharness.New(t, identityharness.Options{})
		f, _, _ := startDelegateAS1(t, h, nil)
		if err := f.server.UseCredentialVerifier(allowing(time.Now())); err == nil {
			t.Fatal("UseCredentialVerifier succeeded without ID-JAG issuance")
		}
	})
	t.Run("nil and a second verifier", func(t *testing.T) {
		h := identityharness.New(t, identityharness.Options{})
		f, _, _ := startDelegateAS1(t, h, withIDJAG)
		if err := f.server.UseCredentialVerifier(nil); err == nil {
			t.Fatal("UseCredentialVerifier(nil) succeeded")
		}
		if err := f.server.UseCredentialVerifier(allowing(time.Now())); err != nil {
			t.Fatalf("first UseCredentialVerifier: %v", err)
		}
		if err := f.server.UseCredentialVerifier(allowing(time.Now())); err == nil {
			t.Fatal("second UseCredentialVerifier succeeded")
		}
	})
}

func TestIDJAGIssuanceConfig(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	issuer := "https://127.0.0.1:1" + testIssuerPath
	for name, edit := range map[string]func(*IDJAGIssuance){
		"no targets":            func(i *IDJAGIssuance) { i.Targets = nil },
		"no act issuer":         func(i *IDJAGIssuance) { i.ActIssuer = "" },
		"an https act issuer":   func(i *IDJAGIssuance) { i.ActIssuer = "https://agents.test" },
		"an act issuer path":    func(i *IDJAGIssuance) { i.ActIssuer = testActIssuer + "/agent" },
		"a trailing slash":      func(i *IDJAGIssuance) { i.ActIssuer = testActIssuer + "/" },
		"an uppercase domain":   func(i *IDJAGIssuance) { i.ActIssuer = "spiffe://Agents.test" },
		"a port":                func(i *IDJAGIssuance) { i.ActIssuer = testActIssuer + ":8443" },
		"userinfo":              func(i *IDJAGIssuance) { i.ActIssuer = "spiffe://u@agents.test" },
		"a query":               func(i *IDJAGIssuance) { i.ActIssuer = testActIssuer + "?x=1" },
		"no trust domain":       func(i *IDJAGIssuance) { i.ActIssuer = "spiffe://" },
		"a fractional lifetime": func(i *IDJAGIssuance) { i.Targets[0].Lifetime = 1500 * time.Millisecond },
		"a negative lifetime":   func(i *IDJAGIssuance) { i.Targets[0].Lifetime = -time.Second },
		"a lifetime over 5m":    func(i *IDJAGIssuance) { i.Targets[0].Lifetime = 5*time.Minute + time.Second },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t, h, issuer)
			d, _, _ := newDelegate(t)
			withIDJAG(&cfg, &d)
			edit(d.IDJAG)
			cfg.Delegate = &d
			if _, err := New(t.Context(), cfg); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("New = %v, want ErrInvalidConfig", err)
			}
		})
	}
	t.Run("a target at AS-1's own issuer", func(t *testing.T) {
		cfg := testConfig(t, h, issuer)
		d, _, _ := newDelegate(t)
		withIDJAG(&cfg, &d)
		d.IDJAG.Targets[0].Issuer = issuer
		cfg.Delegate = &d
		server, err := New(t.Context(), cfg)
		if server != nil {
			_ = server.Close()
		}
		if err == nil || errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "create authorization server") ||
			!strings.Contains(err.Error(), "must not be this server's own issuer") {
			t.Fatalf("New = %v, want ToolHive's refusal", err)
		}
	})
}

func TestActorValidatorMapsOutcomes(t *testing.T) {
	const iss, sub = "https://login.example.com", "alice-204"
	subID := `{"format":"iss_sub","iss":"` + iss + `","sub":"` + sub + `"}`
	request := func(subjectIdentifier string) tokenexchange.ActorRequest {
		return tokenexchange.ActorRequest{
			ActorToken: "svid-value", ActorTokenType: identitybroker.TokenTypeJWT, ClientID: identitybroker.BrokerClientID,
			Grant:  tokenexchange.GrantOwner{Subject: "user-1", SubjectIdentifier: subjectIdentifier},
			Target: tokenexchange.ActorTarget{Issuer: testTargetIssuer, Resource: testTargetResource},
		}
	}
	bound := func(v identitybroker.CredentialVerifier) *actorValidator {
		a := &actorValidator{clientID: identitybroker.BrokerClientID, actIssuer: testActIssuer}
		a.verifier.Store(&v)
		return a
	}
	expires := time.Now().Add(time.Minute)

	t.Run("allow", func(t *testing.T) {
		v := allowing(expires)
		got, err := bound(v).ValidateActor(t.Context(), request(subID))
		want := tokenexchange.ActorResult{AgentID: testActorID, ExpiresAt: expires, CredentialSubject: testAgentSubject}
		if err != nil || got != want {
			t.Fatalf("ValidateActor = %+v, %v; want %+v", got, err, want)
		}
		if reqs := v.requests(); len(reqs) != 1 || reqs[0].Owner != (identitybroker.Owner{Issuer: iss, Subject: sub}) || reqs[0].Credential.Value() != "svid-value" {
			t.Fatalf("verifier got %+v, want the grant's owner and the actor token", reqs)
		}
	})
	for name, tc := range map[string]struct {
		a      *actorValidator
		req    tokenexchange.ActorRequest
		denied tokenexchange.ActorDeniedReason
	}{
		"expired":        {a: bound(&fakeVerifier{err: identitybroker.ReasonError(identitybroker.ReasonSVIDExpired, leakMarker)}), req: request(subID), denied: tokenexchange.ActorDeniedReasonActorTokenExpired},
		"denied":         {a: bound(&fakeVerifier{err: identitybroker.ReasonError(identitybroker.ReasonDenied, leakMarker)}), req: request(subID), denied: tokenexchange.ActorDeniedReasonDenied},
		"wrapped expiry": {a: bound(&fakeVerifier{err: fmt.Errorf("x: %w", identitybroker.ReasonError(identitybroker.ReasonSVIDExpired, leakMarker))}), req: request(subID), denied: tokenexchange.ActorDeniedReasonActorTokenExpired},
		"wrapped denial": {a: bound(&fakeVerifier{err: errors.Join(errors.New("x"), identitybroker.ReasonError(identitybroker.ReasonDenied, leakMarker))}), req: request(subID), denied: tokenexchange.ActorDeniedReasonDenied},
		"another client": {a: bound(allowing(expires)), req: func() tokenexchange.ActorRequest {
			r := request(subID)
			r.ClientID = "other"
			return r
		}(), denied: tokenexchange.ActorDeniedReasonDenied},
		"an outage":               {a: bound(&fakeVerifier{err: identitybroker.NewError(identitybroker.CodeUnavailable, "", leakMarker)}), req: request(subID)},
		"a plain error":           {a: bound(&fakeVerifier{err: errors.New(leakMarker)}), req: request(subID)},
		"an agent with no ag_ ID": {a: bound(&fakeVerifier{agent: identitybroker.VerifiedAgent{Agent: identitybroker.AgentRef{Subject: testAgentSubject}, ActorID: testActIssuer + "/agent/", ExpiresAt: expires}}), req: request(subID)},
		"an agent outside act.iss": {a: bound(&fakeVerifier{agent: identitybroker.VerifiedAgent{
			Agent: identitybroker.AgentRef{Subject: testAgentSubject}, AgentID: testAgentID, ActorID: "spiffe://other.test/agent/" + testAgentID, ExpiresAt: expires,
		}}), req: request(subID)},
		"unbound":                   {a: &actorValidator{clientID: identitybroker.BrokerClientID, actIssuer: testActIssuer}, req: request(subID)},
		"no sub_id":                 {a: bound(allowing(expires)), req: request("")},
		"an email sub_id":           {a: bound(allowing(expires)), req: request(`{"format":"email","email":"alice-204@example.com"}`)},
		"a malformed sub_id":        {a: bound(allowing(expires)), req: request(`{"format":"iss_sub","iss":"` + iss + `","sub":"alice 204"}`)},
		"a sub_id that is not JSON": {a: bound(allowing(expires)), req: request(sub)},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := tc.a.ValidateActor(t.Context(), tc.req)
			if got != (tokenexchange.ActorResult{}) || err == nil {
				t.Fatalf("ValidateActor = %+v, %v; want a zero result and an error", got, err)
			}
			var denial *tokenexchange.ActorDeniedError
			switch isDenial := errors.As(err, &denial); {
			case tc.denied != "" && (!isDenial || denial.Reason != tc.denied):
				t.Fatalf("ValidateActor error %v, want a %s denial", err, tc.denied)
			case tc.denied == "" && isDenial:
				t.Fatalf("ValidateActor error %v, want a plain error, not a denial", err)
			}
			for _, secret := range []string{leakMarker, sub, iss, "svid-value", "alice"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error %q contains %q", err, secret)
				}
			}
		})
	}
}
