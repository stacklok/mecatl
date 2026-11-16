package identitybroker_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

const (
	as1Issuer   = "https://as1.example.com"
	as2Issuer   = "https://as2.example.com"
	as2Resource = "https://vmcp.example.com/mcp"
)

// jwtShaped returns an unsigned compact JWT with header and claims.
func jwtShaped(header, claims map[string]any) identitybroker.Token {
	enc := func(v map[string]any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			panic(err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	return identitybroker.NewToken(enc(header) + "." + enc(claims) + ".sig")
}

func idjagExp() time.Time { return fixedNow().Add(2 * time.Minute) }

func idjagToken(header map[string]any, claims map[string]any) identitybroker.Token {
	if header == nil {
		header = map[string]any{"alg": "ES256", "typ": "oauth-id-jag+jwt"}
	}
	if claims == nil {
		claims = map[string]any{"iss": as1Issuer, "sub": "alice", "jti": "jag-" + rand.Text(), "exp": idjagExp().Unix()}
	}
	return jwtShaped(header, claims)
}

// issueIDJAG is AS-1's answer to an ID-JAG exchange.
func issueIDJAG(int, identitybroker.IDJAGRequest) (identitybroker.IDJAGResult, error) {
	return identitybroker.IDJAGResult{IDJAG: idjagToken(nil, nil)}, nil
}

// gatewayToken returns a gateway token with exp, or none when exp is zero.
func gatewayToken(exp time.Time) identitybroker.Token {
	claims := map[string]any{"iss": as2Issuer, "aud": as2Resource, "jti": "gw-" + rand.Text()}
	if !exp.IsZero() {
		claims["exp"] = exp.Unix()
	}
	return jwtShaped(map[string]any{"alg": "ES256", "typ": "at+jwt"}, claims)
}

// redeemGatewayToken is AS-2's answer to a redemption.
func redeemGatewayToken(int, identitybroker.Redemption) (identitybroker.GatewayTokenResult, error) {
	return identitybroker.GatewayTokenResult{AccessToken: gatewayToken(fixedNow().Add(time.Hour)), ExpiresIn: time.Hour}, nil
}

// fakeIssuer is AS-1's ID-JAG exchange, answering call n (from 1) with
// answer, or issueIDJAG when answer is nil.
type fakeIssuer struct {
	answer func(n int, req identitybroker.IDJAGRequest) (identitybroker.IDJAGResult, error)

	mu     sync.Mutex
	reqs   []identitybroker.IDJAGRequest
	issued []identitybroker.Token
}

func (f *fakeIssuer) IssueIDJAG(_ context.Context, req identitybroker.IDJAGRequest) (identitybroker.IDJAGResult, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	n := len(f.reqs)
	f.mu.Unlock()
	answer := f.answer
	if answer == nil {
		answer = issueIDJAG
	}
	res, err := answer(n, req)
	if err == nil {
		f.mu.Lock()
		f.issued = append(f.issued, res.IDJAG)
		f.mu.Unlock()
	}
	return res, err
}

func (f *fakeIssuer) requests() []identitybroker.IDJAGRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]identitybroker.IDJAGRequest(nil), f.reqs...)
}

func (f *fakeIssuer) tokens() []identitybroker.Token {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]identitybroker.Token(nil), f.issued...)
}

// fakeAS2 is AS-2's redemption, answering call n (from 1) with answer, or
// redeemGatewayToken when answer is nil.
type fakeAS2 struct {
	answer func(n int, req identitybroker.Redemption) (identitybroker.GatewayTokenResult, error)

	mu         sync.Mutex
	assertions []string
	issued     []identitybroker.Token
}

func (f *fakeAS2) Redeem(_ context.Context, req identitybroker.Redemption) (identitybroker.GatewayTokenResult, error) {
	f.mu.Lock()
	f.assertions = append(f.assertions, req.Assertion.Value())
	n := len(f.assertions)
	f.mu.Unlock()
	answer := f.answer
	if answer == nil {
		answer = redeemGatewayToken
	}
	res, err := answer(n, req)
	if err == nil {
		f.mu.Lock()
		f.issued = append(f.issued, res.AccessToken)
		f.mu.Unlock()
	}
	return res, err
}

func (f *fakeAS2) redeemed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.assertions...)
}

func (f *fakeAS2) tokens() []identitybroker.Token {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]identitybroker.Token(nil), f.issued...)
}

// withGateway configures g with a recorder and an ID-JAG issuer, which a
// gateway requires; later options may replace them.
func withGateway(g identitybroker.Gateway) identitybroker.Option {
	opts := []identitybroker.Option{
		identitybroker.WithRecorder(identitybroker.NewMemoryRecorder()),
		identitybroker.WithIDJAGIssuer(&fakeIssuer{}),
		identitybroker.WithGateway(g),
	}
	return func(b *identitybroker.Broker) {
		for _, opt := range opts {
			opt(b)
		}
	}
}

// perCallFixture is a granted session on a broker whose gateway routes
// "github" and "calendar", with AS-2 bound.
type perCallFixture struct {
	grantedFixture
	issuer *fakeIssuer
	as2    *fakeAS2
	rec    *identitybroker.MemoryRecorder
	unbind func()
}

func newPerCall(t *testing.T, opts ...identitybroker.Option) perCallFixture {
	t.Helper()
	issuer, as2, rec := &fakeIssuer{}, &fakeAS2{}, identitybroker.NewMemoryRecorder()
	opts = append([]identitybroker.Option{
		identitybroker.WithRecorder(rec),
		identitybroker.WithIDJAGIssuer(issuer),
		identitybroker.WithGateway(identitybroker.Gateway{Servers: []string{"github", "calendar"}}),
	}, opts...)
	f := openGranted(t, opts...)
	unbind, err := f.b.BindGatewayAS2(boundAS2(as2))
	if err != nil {
		t.Fatalf("BindGatewayAS2: %v", err)
	}
	t.Cleanup(unbind)
	return perCallFixture{grantedFixture: f, issuer: issuer, as2: as2, rec: rec, unbind: unbind}
}

func boundAS2(as2 identitybroker.GatewayAuthServer) identitybroker.GatewayAS2 {
	return identitybroker.GatewayAS2{Issuer: as2Issuer, Resource: as2Resource, AuthServer: as2}
}

// wantCounts fails t unless the fixture made exchanges ID-JAG exchanges and
// redemptions redemptions.
func (f perCallFixture) wantCounts(t *testing.T, exchanges, redemptions int) {
	t.Helper()
	if got, gotR := len(f.issuer.requests()), len(f.as2.redeemed()); got != exchanges || gotR != redemptions {
		t.Fatalf("made %d ID-JAG exchanges and %d redemptions, want %d and %d", got, gotR, exchanges, redemptions)
	}
}

func refusal(status int, code, reason string) error {
	return identitybroker.NewServerRefusal(status, code, reason)
}

func TestBindGatewayAS2(t *testing.T) {
	issuer, as2 := &fakeIssuer{}, &fakeAS2{}
	f := openGranted(t,
		identitybroker.WithRecorder(identitybroker.NewMemoryRecorder()),
		identitybroker.WithIDJAGIssuer(issuer),
		identitybroker.WithGateway(identitybroker.Gateway{Servers: []string{"github"}}))

	_, err := f.authorize(callRequest(f.s.ID, f.cred))
	wantError(t, err, identitybroker.CodeUnavailable, "", f.cred)
	if n := len(issuer.requests()); n != 0 {
		t.Fatalf("an unbound gateway made %d ID-JAG exchanges, want none", n)
	}

	for name, bad := range map[string]identitybroker.GatewayAS2{
		"nothing":       {},
		"no issuer":     {Resource: as2Resource, AuthServer: as2},
		"no resource":   {Issuer: as2Issuer, AuthServer: as2},
		"no port":       {Issuer: as2Issuer, Resource: as2Resource},
		"spaced issuer": {Issuer: "as 2", Resource: as2Resource, AuthServer: as2},
	} {
		if _, err := f.b.BindGatewayAS2(bad); !errors.Is(err, identitybroker.ErrInvalidConfig) {
			t.Errorf("BindGatewayAS2 with %s = %v, want ErrInvalidConfig", name, err)
		}
	}

	unbind, err := f.b.BindGatewayAS2(boundAS2(as2))
	if err != nil {
		t.Fatalf("BindGatewayAS2: %v", err)
	}
	if _, err := f.b.BindGatewayAS2(boundAS2(as2)); !errors.Is(err, identitybroker.ErrInvalidConfig) {
		t.Fatalf("a second BindGatewayAS2 = %v, want ErrInvalidConfig", err)
	}
	if _, err := f.authorize(callRequest(f.s.ID, f.cred)); err != nil {
		t.Fatalf("AuthorizeCall with AS-2 bound: %v", err)
	}
	unbind()
	unbind()
	_, err = f.authorize(callRequest(f.s.ID, f.cred))
	wantError(t, err, identitybroker.CodeUnavailable, "", f.cred)
	if n := len(issuer.requests()); n != 1 {
		t.Fatalf("made %d ID-JAG exchanges, want 1: none once unbound", n)
	}

	// An old unbind leaves a later binding alone.
	again, err := f.b.BindGatewayAS2(boundAS2(as2))
	if err != nil {
		t.Fatalf("BindGatewayAS2 after unbind: %v", err)
	}
	unbind()
	if _, err := f.authorize(callRequest(f.s.ID, f.cred)); err != nil {
		t.Fatalf("an old unbind unbound a later binding: %v", err)
	}
	again()

	if _, err := newBroker(t).BindGatewayAS2(boundAS2(as2)); !errors.Is(err, identitybroker.ErrInvalidConfig) {
		t.Fatalf("BindGatewayAS2 without a gateway = %v, want ErrInvalidConfig", err)
	}

	// A gateway configured with its AS-2 is bound by New.
	g := openGranted(t,
		identitybroker.WithRecorder(identitybroker.NewMemoryRecorder()),
		identitybroker.WithIDJAGIssuer(&fakeIssuer{}),
		identitybroker.WithGateway(identitybroker.Gateway{Servers: []string{"github"}, AS2: boundAS2(&fakeAS2{})}))
	if _, err := g.authorize(callRequest(g.s.ID, g.cred)); err != nil {
		t.Fatalf("AuthorizeCall with AS-2 configured at New: %v", err)
	}
	if _, err := g.b.BindGatewayAS2(boundAS2(as2)); !errors.Is(err, identitybroker.ErrInvalidConfig) {
		t.Fatalf("BindGatewayAS2 over a configured AS-2 = %v, want ErrInvalidConfig", err)
	}
}

func TestUnbindGatewayAS2WaitsForCallsInFlight(t *testing.T) {
	f := newPerCall(t)
	entered, proceed := make(chan struct{}), make(chan struct{})
	// A failed assertion still lets the redemption in flight finish.
	release := sync.OnceFunc(func() { close(proceed) })
	t.Cleanup(release)
	var redeemed atomic.Bool
	f.as2.answer = func(n int, req identitybroker.Redemption) (identitybroker.GatewayTokenResult, error) {
		close(entered)
		<-proceed
		res, err := redeemGatewayToken(n, req)
		redeemed.Store(true)
		return res, err
	}
	type result struct {
		auth identitybroker.Authorization
		err  error
	}
	called := make(chan result, 1)
	go func() {
		auth, err := f.authorize(callRequest(f.s.ID, f.cred))
		called <- result{auth, err}
	}()
	receive(t, entered)

	unbound := make(chan struct{})
	go func() {
		f.unbind()
		close(unbound)
	}()
	deadline := time.Now().Add(waitLimit)
	for identitybroker.AS2Bound(f.b) {
		if time.Now().After(deadline) {
			t.Fatal("unbind did not remove the binding")
		}
		runtime.Gosched()
	}
	// A new call finds AS-2 unbound without waiting for the one in flight.
	_, err := f.authorize(callRequest(f.s.ID, f.cred))
	wantError(t, err, identitybroker.CodeUnavailable, "", f.cred)
	select {
	case <-unbound:
		t.Fatal("unbind returned while a redemption was in flight")
	default:
	}

	release()
	receive(t, unbound)
	if !redeemed.Load() {
		t.Fatal("unbind returned before the redemption in flight")
	}
	if got := receive(t, called); got.err != nil || got.auth.GatewayToken.IsZero() {
		t.Fatalf("the call in flight = %v, %v; want a gateway token", got.auth, got.err)
	}
}

func TestAuthorizeCallGetsOneGatewayTokenPerCall(t *testing.T) {
	f := newPerCall(t)
	grant, ok := identitybroker.SessionGrant(f.b, f.s.ID)
	if !ok {
		t.Fatal("the session holds no grant")
	}
	var auths []identitybroker.Authorization
	seen := map[string]bool{}
	for i := range 2 {
		req := callRequest(f.s.ID, f.cred)
		req.OperationID = fmt.Sprintf("op-%d", i)
		got, err := f.authorize(req)
		if err != nil {
			t.Fatalf("AuthorizeCall %d: %v", i, err)
		}
		f.wantCounts(t, i+1, i+1)
		r := f.issuer.requests()[i]
		if r.SubjectToken.Value() != grant.RefreshToken.Value() || r.ActorToken.Value() != f.cred.Value() ||
			r.Audience != as2Issuer || r.Resource != as2Resource || r.OperationID != req.OperationID {
			t.Fatalf("ID-JAG request %d = %+v, want the session's grant, the credential, AS-2, and the operation", i, r)
		}
		assertion := f.as2.redeemed()[i]
		if assertion != f.issuer.tokens()[i].Value() {
			t.Fatalf("call %d redeemed something other than its ID-JAG", i)
		}
		if got.GatewayToken.Value() != f.as2.tokens()[i].Value() || !strings.HasPrefix(got.AttemptID, "att_") {
			t.Fatalf("call %d = %+v, want AS-2's token and an att_ attempt", i, got)
		}
		if !got.ExpiresAt.Equal(idjagExp()) {
			t.Fatalf("call %d expires at %v, want the ID-JAG's exp %v", i, got.ExpiresAt, idjagExp())
		}
		for _, v := range []string{assertion, got.GatewayToken.Value(), got.AttemptID} {
			if seen[v] {
				t.Fatalf("call %d repeats an ID-JAG, gateway token, or attempt", i)
			}
			seen[v] = true
		}
		auths = append(auths, got)
	}

	idjags, decisions := f.rec.IDJAGs(), f.rec.Decisions()
	if len(idjags) != 2 || len(decisions) != 2 {
		t.Fatalf("recorded %d ID-JAGs and %d decisions, want 2 each", len(idjags), len(decisions))
	}
	for i, got := range auths {
		j, d := idjags[i], decisions[i]
		if j.SessionID != f.s.ID || !strings.HasPrefix(j.AgentID, "ag_") || j.Agent.IsZero() || j.RunID != "R42" ||
			j.OperationID != fmt.Sprintf("op-%d", i) || j.AttemptID != got.AttemptID ||
			j.IDJAG.Issuer != as1Issuer || !strings.HasPrefix(j.IDJAG.ID, "jag-") || !j.ExpiresAt.Equal(idjagExp()) {
			t.Errorf("ID-JAG record %d = %+v", i, j)
		}
		if d.Outcome != identitybroker.OutcomeSuccess || d.Code != "" || d.Reason != "" || d.AttemptID != got.AttemptID ||
			d.IDJAG != j.IDJAG || d.GatewayToken.Issuer != as2Issuer || !strings.HasPrefix(d.GatewayToken.ID, "gw-") ||
			d.GatewayTokenUnreferenced || d.AgentID != j.AgentID {
			t.Errorf("decision %d = %+v", i, d)
		}
	}
}

func TestAuthorizeCallListNeedsNoToolOrHash(t *testing.T) {
	f := newPerCall(t)
	req := callRequest(f.s.ID, f.cred)
	req.Method, req.Tool, req.ArgumentsSHA256 = identitybroker.MethodToolsList, "", ""
	if got, err := f.authorize(req); err != nil || got.GatewayToken.IsZero() {
		t.Fatalf("a tools/list = %v, %v; want a gateway token", got, err)
	}
}

func TestAuthorizeCallRedeemsOnlyAnIDJAG(t *testing.T) {
	claims := func(drop string) map[string]any {
		c := map[string]any{"iss": as1Issuer, "jti": "jag-1", "exp": idjagExp().Unix()}
		delete(c, drop)
		return c
	}
	for name, tok := range map[string]identitybroker.Token{
		"a plain JWT": idjagToken(map[string]any{"alg": "ES256", "typ": "JWT"}, nil),
		"no typ":      idjagToken(map[string]any{"alg": "ES256"}, nil),
		"no jti":      idjagToken(nil, claims("jti")),
		"no exp":      idjagToken(nil, claims("exp")),
		"not a JWT":   identitybroker.NewToken("opaque-token"),
	} {
		t.Run(name, func(t *testing.T) {
			f := newPerCall(t)
			f.issuer.answer = func(int, identitybroker.IDJAGRequest) (identitybroker.IDJAGResult, error) {
				return identitybroker.IDJAGResult{IDJAG: tok}, nil
			}
			_, err := f.authorize(callRequest(f.s.ID, f.cred))
			wantError(t, err, identitybroker.CodeInternal, "", tok)
			f.wantCounts(t, 1, 0)
			if d := f.rec.Decisions(); len(d) != 1 || d[0].Outcome != identitybroker.OutcomeError {
				t.Fatalf("decisions = %+v, want one error", d)
			}
		})
	}
}

func TestAuthorizeCallGatewayTokenExpiry(t *testing.T) {
	for _, tc := range []struct {
		name      string
		token     identitybroker.Token
		expiresIn time.Duration
		want      time.Time
	}{
		{"the ID-JAG's exp", gatewayToken(fixedNow().Add(time.Hour)), time.Hour, idjagExp()},
		{"expires_in", gatewayToken(fixedNow().Add(time.Hour)), 30 * time.Second, fixedNow().Add(30 * time.Second)},
		{"the token's exp", gatewayToken(fixedNow().Add(time.Minute)), 0, fixedNow().Add(time.Minute)},
		{"a token without exp", gatewayToken(time.Time{}), 0, idjagExp()},
		{"an opaque token", identitybroker.NewToken("opaque-gateway-token"), 0, idjagExp()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPerCall(t)
			f.as2.answer = func(int, identitybroker.Redemption) (identitybroker.GatewayTokenResult, error) {
				return identitybroker.GatewayTokenResult{AccessToken: tc.token, ExpiresIn: tc.expiresIn}, nil
			}
			got, err := f.authorize(callRequest(f.s.ID, f.cred))
			if err != nil {
				t.Fatalf("AuthorizeCall: %v", err)
			}
			if !got.ExpiresAt.Equal(tc.want) {
				t.Fatalf("ExpiresAt = %v, want %v", got.ExpiresAt, tc.want)
			}
			d := f.rec.Decisions()[0]
			opaque := !strings.Contains(tc.token.Value(), ".")
			if d.GatewayTokenUnreferenced != opaque || (d.GatewayToken == identitybroker.CredentialRef{}) != opaque {
				t.Fatalf("decision names the gateway token as %+v, unavailable %v; want unavailable only for an opaque token", d.GatewayToken, d.GatewayTokenUnreferenced)
			}
		})
	}
}

func TestAuthorizeCallMapsAS2Refusals(t *testing.T) {
	for _, tc := range []struct {
		name        string
		first, then error // then is nil for a success
		code        identitybroker.Code
		reason      identitybroker.Reason
		redemptions int
	}{
		{"prose then success", refusal(400, "invalid_grant", "The JWT was already used"), nil, "", "", 2},
		{"a known reason", refusal(400, "invalid_grant", "denied"), nil, identitybroker.CodeInternal, "", 1},
		{"consent required", refusal(400, "invalid_grant", "consent_required"), nil, identitybroker.CodeFailedPrecondition, identitybroker.ReasonConsentRequired, 1},
		// A transient refusal is never a prompt, whatever its reason.
		{"consent required in a 503", refusal(503, "invalid_grant", "consent_required"), nil, identitybroker.CodeUnavailable, "", 1},
		{"consent required in a server_error", refusal(400, "server_error", "consent_required"), nil, identitybroker.CodeUnavailable, "", 1},
		{"prose twice", refusal(400, "invalid_grant", "The JWT was already used"), refusal(400, "invalid_grant", ""), identitybroker.CodeInternal, "", 2},
		{"unavailable", refusal(503, "temporarily_unavailable", ""), nil, identitybroker.CodeUnavailable, "", 1},
		{"misconfigured", fmt.Errorf("tls: %w", identitybroker.ErrTransportConfiguration), nil, identitybroker.CodeInternal, "", 1},
		{"lost twice", identitybroker.ErrNoResponse, identitybroker.ErrNoResponse, identitybroker.CodeUnavailable, "", 2},
		// Only a lost response gets a new ID-JAG; a 502 or 504 fails the call.
		{"bad gateway", refusal(502, "", ""), nil, identitybroker.CodeUnavailable, "", 1},
		{"gateway timeout", refusal(504, "", ""), nil, identitybroker.CodeUnavailable, "", 1},
		{"an unclassified error", errors.New("boom"), nil, identitybroker.CodeInternal, "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPerCall(t)
			f.as2.answer = func(n int, req identitybroker.Redemption) (identitybroker.GatewayTokenResult, error) {
				switch {
				case n == 1:
					return identitybroker.GatewayTokenResult{}, tc.first
				case tc.then != nil:
					return identitybroker.GatewayTokenResult{}, tc.then
				}
				return redeemGatewayToken(n, req)
			}
			got, err := f.authorize(callRequest(f.s.ID, f.cred))
			if tc.code == "" {
				if err != nil || got.GatewayToken.IsZero() {
					t.Fatalf("AuthorizeCall = %v, %v; want a gateway token", got, err)
				}
			} else {
				wantError(t, err, tc.code, tc.reason, f.cred)
				if strings.Contains(err.Error(), "boom") {
					t.Fatalf("AuthorizeCall = %v, want fixed detail", err)
				}
			}
			f.wantCounts(t, tc.redemptions, tc.redemptions)
			if r := f.as2.redeemed(); len(r) == 2 && r[0] == r[1] {
				t.Fatal("an ID-JAG was redeemed twice")
			}
		})
	}
}

func TestAuthorizeCallMapsAS1Refusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		code   identitybroker.Code
		reason identitybroker.Reason
	}{
		{"not_issuable", refusal(400, "invalid_grant", "not_issuable"), identitybroker.CodeInternal, ""},
		{"invalid_client", refusal(401, "invalid_client", ""), identitybroker.CodeInternal, ""},
		// Only a lost response gets a new ID-JAG; a 502 or 504 fails the call.
		{"bad gateway", refusal(502, "", ""), identitybroker.CodeUnavailable, ""},
		{"gateway timeout", refusal(504, "", ""), identitybroker.CodeUnavailable, ""},
		{"malformed", fmt.Errorf("%w: %w", identitybroker.ErrMalformedResponse, identitybroker.ErrTransportConfiguration), identitybroker.CodeInternal, ""},
		{"cancelled", context.Canceled, identitybroker.CodeUnavailable, ""},
		{"an unclassified error", errors.New("boom"), identitybroker.CodeInternal, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPerCall(t)
			f.issuer.answer = func(int, identitybroker.IDJAGRequest) (identitybroker.IDJAGResult, error) {
				return identitybroker.IDJAGResult{}, tc.err
			}
			_, err := f.authorize(callRequest(f.s.ID, f.cred))
			wantError(t, err, tc.code, tc.reason, f.cred)
			if strings.Contains(err.Error(), "boom") {
				t.Fatalf("AuthorizeCall = %v, want fixed detail", err)
			}
			f.wantCounts(t, 1, 0)
			d := f.rec.Decisions()
			if len(d) != 1 || d[0].Code != tc.code || d[0].Reason != tc.reason {
				t.Fatalf("decisions = %+v, want one with %s %q", d, tc.code, tc.reason)
			}
			// Only a revocation closes the session.
			if _, ok := identitybroker.SessionGrant(f.b, f.s.ID); !ok {
				t.Fatal("the refusal closed the session")
			}
		})
	}
}

func TestAuthorizeCallLostAS1ResponseTwiceIsUnavailable(t *testing.T) {
	f := newPerCall(t)
	f.issuer.answer = func(int, identitybroker.IDJAGRequest) (identitybroker.IDJAGResult, error) {
		return identitybroker.IDJAGResult{}, fmt.Errorf("adapter: %w", identitybroker.ErrNoResponse)
	}
	_, err := f.authorize(callRequest(f.s.ID, f.cred))
	wantError(t, err, identitybroker.CodeUnavailable, "", f.cred)
	f.wantCounts(t, 2, 0)
}

// TestAuthorizeCallRetryAllowances shows each retry rule allows one retry of
// its own: a lost response, a reasonless invalid_grant at AS-2, and a
// revocation while the session holds a newer grant.
func TestAuthorizeCallRetryAllowances(t *testing.T) {
	lost := func(perCallFixture) (identitybroker.IDJAGResult, error) {
		return identitybroker.IDJAGResult{}, identitybroker.ErrNoResponse
	}
	// revokedWithNewer replaces the session's grant, as a reopen does, and
	// answers that the grant the exchange presented is revoked.
	revokedWithNewer := func(f perCallFixture) (identitybroker.IDJAGResult, error) {
		if _, err := f.b.OpenSession(context.Background(), testWorkload, identitybroker.OpenSessionRequest{
			HostSessionID: openHost, Incarnation: openIncarnation, Owner: testOwner, AccessToken: unsignedToken(t, testOwner),
		}); err != nil {
			return identitybroker.IDJAGResult{}, err
		}
		return identitybroker.IDJAGResult{}, refusal(400, "invalid_grant", "revoked")
	}
	issue := func(perCallFixture) (identitybroker.IDJAGResult, error) {
		return issueIDJAG(0, identitybroker.IDJAGRequest{})
	}
	type redemption func() (identitybroker.GatewayTokenResult, error)
	redeemLost := func() (identitybroker.GatewayTokenResult, error) {
		return identitybroker.GatewayTokenResult{}, identitybroker.ErrNoResponse
	}
	reasonless := func() (identitybroker.GatewayTokenResult, error) {
		return identitybroker.GatewayTokenResult{}, refusal(400, "invalid_grant", "")
	}
	redeem := func() (identitybroker.GatewayTokenResult, error) {
		return redeemGatewayToken(0, identitybroker.Redemption{})
	}

	for _, tc := range []struct {
		name        string
		exchanges   []func(perCallFixture) (identitybroker.IDJAGResult, error)
		redemptions []redemption
		code        identitybroker.Code // empty for a success
	}{
		{"lost then revoked with a newer grant", []func(perCallFixture) (identitybroker.IDJAGResult, error){lost, revokedWithNewer, issue}, []redemption{redeem}, ""},
		{"revoked with a newer grant then lost", []func(perCallFixture) (identitybroker.IDJAGResult, error){revokedWithNewer, lost, issue}, []redemption{redeem}, ""},
		{"every rule once", []func(perCallFixture) (identitybroker.IDJAGResult, error){lost, revokedWithNewer, issue, issue}, []redemption{reasonless, redeem}, ""},
		{"lost at AS-2 then a reasonless invalid_grant", []func(perCallFixture) (identitybroker.IDJAGResult, error){issue, issue, issue}, []redemption{redeemLost, reasonless, redeem}, ""},
		{"revoked with a newer grant twice", []func(perCallFixture) (identitybroker.IDJAGResult, error){revokedWithNewer, revokedWithNewer}, nil, identitybroker.CodeUnavailable},
		{"lost at AS-1 then at AS-2", []func(perCallFixture) (identitybroker.IDJAGResult, error){lost, issue}, []redemption{redeemLost}, identitybroker.CodeUnavailable},
		{"a reasonless invalid_grant twice after a lost response", []func(perCallFixture) (identitybroker.IDJAGResult, error){lost, issue, issue}, []redemption{reasonless, reasonless}, identitybroker.CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPerCall(t)
			f.issuer.answer = func(n int, _ identitybroker.IDJAGRequest) (identitybroker.IDJAGResult, error) {
				if n > len(tc.exchanges) {
					t.Errorf("unexpected ID-JAG exchange %d", n)
					return identitybroker.IDJAGResult{}, identitybroker.ErrNoResponse
				}
				return tc.exchanges[n-1](f)
			}
			f.as2.answer = func(n int, _ identitybroker.Redemption) (identitybroker.GatewayTokenResult, error) {
				if n > len(tc.redemptions) {
					t.Errorf("unexpected redemption %d", n)
					return identitybroker.GatewayTokenResult{}, identitybroker.ErrNoResponse
				}
				return tc.redemptions[n-1]()
			}
			got, err := f.authorize(callRequest(f.s.ID, f.cred))
			if tc.code == "" {
				if err != nil || got.GatewayToken.IsZero() {
					t.Fatalf("AuthorizeCall = %+v, %v; want a gateway token", got, err)
				}
			} else {
				wantError(t, err, tc.code, "", f.cred)
			}
			f.wantCounts(t, len(tc.exchanges), len(tc.redemptions))
			seen := map[string]bool{}
			for _, a := range f.as2.redeemed() {
				if seen[a] {
					t.Fatal("an ID-JAG was redeemed twice")
				}
				seen[a] = true
			}
		})
	}
}

// TestAuthorizeCallConcurrentCallsInOneSession shows concurrent calls in one
// session each get their own ID-JAG, gateway token, and attempt.
func TestAuthorizeCallConcurrentCallsInOneSession(t *testing.T) {
	f := newPerCall(t)
	const calls = 8
	auths := make([]identitybroker.Authorization, calls)
	errs := make([]error, calls)
	var wg sync.WaitGroup
	for i := range calls {
		wg.Go(func() {
			req := callRequest(f.s.ID, f.cred)
			req.OperationID = fmt.Sprintf("op-%d", i)
			auths[i], errs[i] = f.authorize(req)
		})
	}
	wg.Wait()
	f.wantCounts(t, calls, calls)
	seen := map[string]bool{}
	for i, auth := range auths {
		if errs[i] != nil || auth.GatewayToken.IsZero() {
			t.Fatalf("call %d = %+v, %v; want a gateway token", i, auth, errs[i])
		}
		for _, v := range []string{auth.GatewayToken.Value(), auth.AttemptID} {
			if seen[v] {
				t.Fatalf("call %d repeats a gateway token or attempt", i)
			}
			seen[v] = true
		}
	}
	for _, a := range f.as2.redeemed() {
		if seen[a] {
			t.Fatal("an ID-JAG was redeemed twice")
		}
		seen[a] = true
	}
	if d := f.rec.Decisions(); len(d) != calls {
		t.Fatalf("recorded %d decisions, want %d", len(d), calls)
	}
}

// TestAuthorizeCallCancelledWhileRetryingALostResponse shows a call cancelled
// during the new exchange that follows a lost response ends promptly as
// UNAVAILABLE, with no further exchange.
func TestAuthorizeCallCancelledWhileRetryingALostResponse(t *testing.T) {
	f := newPerCall(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	retrying := make(chan struct{})
	f.issuer.answer = func(n int, _ identitybroker.IDJAGRequest) (identitybroker.IDJAGResult, error) {
		if n == 1 {
			return identitybroker.IDJAGResult{}, identitybroker.ErrNoResponse
		}
		close(retrying)
		<-ctx.Done()
		// The adapter reports a request cancelled in flight as unanswered.
		return identitybroker.IDJAGResult{}, identitybroker.ErrNoResponse
	}
	type result struct {
		auth identitybroker.Authorization
		err  error
	}
	called := make(chan result, 1)
	go func() {
		auth, err := f.b.AuthorizeCall(ctx, testWorkload, callRequest(f.s.ID, f.cred))
		called <- result{auth, err}
	}()
	receive(t, retrying)
	cancel()
	got := receive(t, called)
	wantError(t, got.err, identitybroker.CodeUnavailable, "", f.cred)
	if !got.auth.GatewayToken.IsZero() {
		t.Fatal("a cancelled call returned a gateway token")
	}
	f.wantCounts(t, 2, 0)
	if d := f.rec.Decisions(); len(d) != 1 || d[0].Code != identitybroker.CodeUnavailable {
		t.Fatalf("decisions = %+v, want one UNAVAILABLE refusal", d)
	}
}

// TestAuthorizeCallCancelledWithNoResponseGetsNoNewIDJAG shows a request
// left unanswered because the call was cancelled, as the adapter reports it,
// gets no new ID-JAG: only a call still running retries a lost response.
func TestAuthorizeCallCancelledWithNoResponseGetsNoNewIDJAG(t *testing.T) {
	t.Run("at AS-1", func(t *testing.T) {
		f := newPerCall(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f.issuer.answer = func(int, identitybroker.IDJAGRequest) (identitybroker.IDJAGResult, error) {
			cancel()
			return identitybroker.IDJAGResult{}, identitybroker.ErrNoResponse
		}
		_, err := f.b.AuthorizeCall(ctx, testWorkload, callRequest(f.s.ID, f.cred))
		wantError(t, err, identitybroker.CodeUnavailable, "", f.cred)
		f.wantCounts(t, 1, 0)
	})
	t.Run("at AS-2", func(t *testing.T) {
		f := newPerCall(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f.as2.answer = func(int, identitybroker.Redemption) (identitybroker.GatewayTokenResult, error) {
			cancel()
			return identitybroker.GatewayTokenResult{}, identitybroker.ErrNoResponse
		}
		_, err := f.b.AuthorizeCall(ctx, testWorkload, callRequest(f.s.ID, f.cred))
		wantError(t, err, identitybroker.CodeUnavailable, "", f.cred)
		f.wantCounts(t, 1, 1)
	})
}

// TestAuthorizeCallHardExpiryBeforeTheRedemption shows a session reaching its
// hard expiry during the ID-JAG exchange redeems nothing.
func TestAuthorizeCallHardExpiryBeforeTheRedemption(t *testing.T) {
	f := newPerCall(t)
	grant, _ := identitybroker.SessionGrant(f.b, f.s.ID)
	f.issuer.answer = func(n int, req identitybroker.IDJAGRequest) (identitybroker.IDJAGResult, error) {
		*f.clock = grant.ExpiresAt.Add(-identitybroker.ClockSkew)
		return issueIDJAG(n, req)
	}
	_, err := f.authorize(callRequest(f.s.ID, f.cred))
	wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed, f.cred)
	f.wantCounts(t, 1, 0)
}

func TestAuthorizeCallAfterTheHardExpiryClosesTheSession(t *testing.T) {
	f := newPerCall(t)
	grant, _ := identitybroker.SessionGrant(f.b, f.s.ID)
	*f.clock = grant.ExpiresAt.Add(-identitybroker.ClockSkew)
	_, err := f.authorize(callRequest(f.s.ID, f.cred))
	wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed, f.cred)
	f.wantCounts(t, 0, 0)
}

// closingRecorder is a MemoryRecorder that calls close, when set, right after
// it records a success.
type closingRecorder struct {
	*identitybroker.MemoryRecorder
	close func()
}

func (r *closingRecorder) RecordDecision(ctx context.Context, rec identitybroker.DecisionRecord) error {
	err := r.MemoryRecorder.RecordDecision(ctx, rec)
	if rec.Outcome == identitybroker.OutcomeSuccess && r.close != nil {
		r.close()
	}
	return err
}

// faultyRecorder is a MemoryRecorder that refuses ID-JAG records, or
// success decisions, when told to.
type faultyRecorder struct {
	*identitybroker.MemoryRecorder
	failIDJAG, failSuccess bool
}

func (r faultyRecorder) RecordIDJAG(ctx context.Context, rec identitybroker.IDJAGRecord) error {
	if r.failIDJAG {
		return errors.New("recorder unavailable")
	}
	return r.MemoryRecorder.RecordIDJAG(ctx, rec)
}

func (r faultyRecorder) RecordDecision(ctx context.Context, rec identitybroker.DecisionRecord) error {
	if r.failSuccess && rec.Outcome == identitybroker.OutcomeSuccess {
		return errors.New("recorder unavailable")
	}
	return r.MemoryRecorder.RecordDecision(ctx, rec)
}

func TestAuthorizeCallWriteOrder(t *testing.T) {
	t.Run("an ID-JAG that cannot be recorded is not redeemed", func(t *testing.T) {
		rec := faultyRecorder{MemoryRecorder: identitybroker.NewMemoryRecorder(), failIDJAG: true}
		f := newPerCall(t, identitybroker.WithRecorder(rec))
		_, err := f.authorize(callRequest(f.s.ID, f.cred))
		wantError(t, err, identitybroker.CodeUnavailable, "", f.cred)
		f.wantCounts(t, 1, 0)
		d := rec.Decisions()
		if len(d) != 1 || d[0].Outcome != identitybroker.OutcomeError || d[0].Code != identitybroker.CodeUnavailable ||
			!strings.HasPrefix(d[0].AttemptID, "att_") || d[0].IDJAG.ID == "" {
			t.Fatalf("decisions = %+v, want one error naming the attempt and ID-JAG", d)
		}
	})

	t.Run("a success that cannot be recorded returns no token", func(t *testing.T) {
		rec := faultyRecorder{MemoryRecorder: identitybroker.NewMemoryRecorder(), failSuccess: true}
		f := newPerCall(t, identitybroker.WithRecorder(rec))
		got, err := f.authorize(callRequest(f.s.ID, f.cred))
		wantError(t, err, identitybroker.CodeUnavailable, "", f.cred, f.as2.tokens()[0])
		if !got.GatewayToken.IsZero() {
			t.Fatal("an unrecorded success returned a gateway token")
		}
		d := rec.Decisions()
		if len(d) != 1 || d[0].Outcome != identitybroker.OutcomeError {
			t.Fatalf("decisions = %+v, want one error", d)
		}
		// The withheld token's attempt was never issued, so it cannot be
		// reported rejected.
		report := callRequest(f.s.ID, f.cred)
		report.RejectedAttemptID = d[0].AttemptID
		_, err = f.authorize(report)
		wantError(t, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied, f.cred)
	})

	t.Run("a call whose session reaches its hard expiry while its success is recorded returns no token", func(t *testing.T) {
		rec := closingRecorder{MemoryRecorder: identitybroker.NewMemoryRecorder()}
		f := newPerCall(t, identitybroker.WithRecorder(&rec))
		grant, _ := identitybroker.SessionGrant(f.b, f.s.ID)
		rec.close = func() { *f.clock = grant.ExpiresAt.Add(-identitybroker.ClockSkew) }
		got, err := f.authorize(callRequest(f.s.ID, f.cred))
		wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed, append(f.as2.tokens(), f.cred)...)
		if !got.GatewayToken.IsZero() {
			t.Fatal("a call whose session expired returned a gateway token")
		}
	})

	for name, retry := range map[string]bool{"a call": false, "a retry": true} {
		t.Run(name+" whose session closes while its success is recorded returns no token", func(t *testing.T) {
			rec := closingRecorder{MemoryRecorder: identitybroker.NewMemoryRecorder()}
			f := newPerCall(t, identitybroker.WithRecorder(&rec))
			req := callRequest(f.s.ID, f.cred)
			if retry {
				first, err := f.authorize(req)
				if err != nil {
					t.Fatalf("AuthorizeCall: %v", err)
				}
				req.RejectedAttemptID = first.AttemptID
			}
			rec.close = func() { identitybroker.CloseSessionWithReason(f.b, f.s.ID, identitybroker.ReasonSignedOut) }
			got, err := f.authorize(req)
			wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSignedOut, append(f.as2.tokens(), f.cred)...)
			if !got.GatewayToken.IsZero() {
				t.Fatal("a call whose session closed returned a gateway token")
			}
			d := rec.Decisions()
			if len(d) < 2 {
				t.Fatalf("decisions = %+v, want a success followed by a refusal", d)
			}
			success, refusal := d[len(d)-2], d[len(d)-1]
			if success.Outcome != identitybroker.OutcomeSuccess || refusal.Outcome == identitybroker.OutcomeSuccess ||
				refusal.Reason != identitybroker.ReasonSignedOut || refusal.AttemptID != success.AttemptID {
				t.Fatalf("decisions = %+v, want a success then a signed_out refusal for the same attempt", d[len(d)-2:])
			}
		})
	}
}

// ctxRecorder is a MemoryRecorder that refuses decisions on a cancelled
// context, as a record sink honouring cancellation does.
type ctxRecorder struct{ *identitybroker.MemoryRecorder }

func (r ctxRecorder) RecordDecision(ctx context.Context, rec identitybroker.DecisionRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.MemoryRecorder.RecordDecision(ctx, rec)
}

func TestAuthorizeCallRecordsARefusalOfACancelledCall(t *testing.T) {
	rec := ctxRecorder{identitybroker.NewMemoryRecorder()}
	f := newPerCall(t, identitybroker.WithRecorder(rec))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.issuer.answer = func(int, identitybroker.IDJAGRequest) (identitybroker.IDJAGResult, error) {
		return identitybroker.IDJAGResult{}, ctx.Err()
	}
	_, err := f.b.AuthorizeCall(ctx, testWorkload, callRequest(f.s.ID, f.cred))
	wantError(t, err, identitybroker.CodeUnavailable, "", f.cred)
	if d := rec.Decisions(); len(d) != 1 || d[0].Code != identitybroker.CodeUnavailable {
		t.Fatalf("decisions = %+v, want the cancelled call's refusal", d)
	}
}

// TestWithGatewayBindsAS2InEveryBroker shows one WithGateway option with an
// AS-2 binds it in each broker built from it.
func TestWithGatewayBindsAS2InEveryBroker(t *testing.T) {
	opt := withGateway(identitybroker.Gateway{Servers: []string{"github"}, AS2: boundAS2(&fakeAS2{})})
	for i := range 2 {
		if b := newBroker(t, opt); !identitybroker.AS2Bound(b) {
			t.Fatalf("broker %d built from the option has AS-2 unbound", i+1)
		}
	}
}

func TestAuthorizeCallRevokedRules(t *testing.T) {
	ctx := context.Background()
	closesSession := func(t *testing.T, f perCallFixture, err error) {
		t.Helper()
		wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed, f.cred)
		if _, err := f.authorize(callRequest(f.s.ID, f.cred)); err == nil {
			t.Fatal("a call after the close was allowed")
		}
		if _, open, _ := f.b.FindSession(ctx, testWorkload, identitybroker.FindSessionRequest{RootHostSessionID: openHost}); open {
			t.Fatal("the session is still open")
		}
	}

	closeDuring := func(f perCallFixture) {
		if err := f.b.CloseSession(ctx, testWorkload, identitybroker.CloseSessionRequest{SessionID: f.s.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
			panic(err)
		}
	}
	t.Run("a close during the exchange stops the redemption", func(t *testing.T) {
		f := newPerCall(t)
		f.issuer.answer = func(n int, req identitybroker.IDJAGRequest) (identitybroker.IDJAGResult, error) {
			closeDuring(f)
			return issueIDJAG(n, req)
		}
		_, err := f.authorize(callRequest(f.s.ID, f.cred))
		f.wantCounts(t, 1, 0)
		closesSession(t, f, err)
	})

	// A session closed with another reason refuses with that reason, not
	// session_closed.
	signOut := func(f perCallFixture) {
		identitybroker.CloseSessionWithReason(f.b, f.s.ID, identitybroker.ReasonSignedOut)
	}
	signedOut := func(t *testing.T, f perCallFixture, got identitybroker.Authorization, err error) {
		t.Helper()
		wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSignedOut, f.cred)
		if !got.GatewayToken.IsZero() {
			t.Fatal("a call in a closed session returned a gateway token")
		}
	}
	t.Run("a sign-out during the exchange refuses with signed_out", func(t *testing.T) {
		f := newPerCall(t)
		f.issuer.answer = func(n int, req identitybroker.IDJAGRequest) (identitybroker.IDJAGResult, error) {
			signOut(f)
			return issueIDJAG(n, req)
		}
		got, err := f.authorize(callRequest(f.s.ID, f.cred))
		f.wantCounts(t, 1, 0)
		signedOut(t, f, got, err)
	})
	t.Run("a sign-out during the redemption refuses with signed_out", func(t *testing.T) {
		f := newPerCall(t)
		f.as2.answer = func(n int, req identitybroker.Redemption) (identitybroker.GatewayTokenResult, error) {
			signOut(f)
			return redeemGatewayToken(n, req)
		}
		got, err := f.authorize(callRequest(f.s.ID, f.cred))
		f.wantCounts(t, 1, 1)
		signedOut(t, f, got, err)
	})
	t.Run("a sign-out during a refused redemption refuses with signed_out", func(t *testing.T) {
		f := newPerCall(t)
		f.as2.answer = func(int, identitybroker.Redemption) (identitybroker.GatewayTokenResult, error) {
			signOut(f)
			return identitybroker.GatewayTokenResult{}, refusal(400, "invalid_grant", "denied")
		}
		got, err := f.authorize(callRequest(f.s.ID, f.cred))
		f.wantCounts(t, 1, 1)
		signedOut(t, f, got, err)
	})
	t.Run("a sign-out during the last lost redemption refuses with signed_out", func(t *testing.T) {
		f := newPerCall(t)
		f.as2.answer = func(n int, _ identitybroker.Redemption) (identitybroker.GatewayTokenResult, error) {
			if n == 2 {
				signOut(f)
			}
			return identitybroker.GatewayTokenResult{}, identitybroker.ErrNoResponse
		}
		got, err := f.authorize(callRequest(f.s.ID, f.cred))
		f.wantCounts(t, 2, 2)
		signedOut(t, f, got, err)
	})
	t.Run("revoked after a sign-out refuses with signed_out", func(t *testing.T) {
		f := newPerCall(t)
		f.issuer.answer = func(int, identitybroker.IDJAGRequest) (identitybroker.IDJAGResult, error) {
			signOut(f)
			return identitybroker.IDJAGResult{}, refusal(400, "invalid_grant", "revoked")
		}
		got, err := f.authorize(callRequest(f.s.ID, f.cred))
		f.wantCounts(t, 1, 0)
		signedOut(t, f, got, err)
	})
}

func TestAuthorizeCallRechecksTheSessionBeforeARetry(t *testing.T) {
	signOut := func(f perCallFixture) {
		identitybroker.CloseSessionWithReason(f.b, f.s.ID, identitybroker.ReasonSignedOut)
	}
	signedOut := func(t *testing.T, f perCallFixture, got identitybroker.Authorization, err error) {
		t.Helper()
		wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSignedOut, f.cred)
		if !got.GatewayToken.IsZero() {
			t.Fatal("a call in a closed session returned a gateway token")
		}
		if d := f.rec.Decisions(); len(d) != 1 || d[0].Reason != identitybroker.ReasonSignedOut {
			t.Fatalf("decisions = %+v, want one signed_out refusal", d)
		}
	}

	t.Run("a close after a lost exchange gets no further ID-JAG", func(t *testing.T) {
		f := newPerCall(t)
		f.issuer.answer = func(int, identitybroker.IDJAGRequest) (identitybroker.IDJAGResult, error) {
			signOut(f)
			return identitybroker.IDJAGResult{}, identitybroker.ErrNoResponse
		}
		got, err := f.authorize(callRequest(f.s.ID, f.cred))
		f.wantCounts(t, 1, 0)
		signedOut(t, f, got, err)
	})

	t.Run("a close after a reasonless invalid_grant gets no further ID-JAG", func(t *testing.T) {
		f := newPerCall(t)
		f.as2.answer = func(int, identitybroker.Redemption) (identitybroker.GatewayTokenResult, error) {
			signOut(f)
			return identitybroker.GatewayTokenResult{}, refusal(400, "invalid_grant", "")
		}
		got, err := f.authorize(callRequest(f.s.ID, f.cred))
		f.wantCounts(t, 1, 1)
		signedOut(t, f, got, err)
	})

	t.Run("a grant replaced between tries is the one presented", func(t *testing.T) {
		f := newPerCall(t)
		f.issuer.answer = func(n int, req identitybroker.IDJAGRequest) (identitybroker.IDJAGResult, error) {
			if n > 1 {
				return issueIDJAG(n, req)
			}
			if _, err := f.b.OpenSession(context.Background(), testWorkload, identitybroker.OpenSessionRequest{
				HostSessionID: openHost, Incarnation: openIncarnation, Owner: testOwner, AccessToken: unsignedToken(t, testOwner),
			}); err != nil {
				return identitybroker.IDJAGResult{}, err
			}
			return identitybroker.IDJAGResult{}, identitybroker.ErrNoResponse
		}
		if _, err := f.authorize(callRequest(f.s.ID, f.cred)); err != nil {
			t.Fatalf("AuthorizeCall: %v", err)
		}
		f.wantCounts(t, 2, 1)
		newer, _ := identitybroker.SessionGrant(f.b, f.s.ID)
		reqs := f.issuer.requests()
		if reqs[0].SubjectToken.Value() == reqs[1].SubjectToken.Value() || reqs[1].SubjectToken.Value() != newer.RefreshToken.Value() {
			t.Fatal("the second try did not present the session's new grant")
		}
	})
}

func TestAuthorizeCallRecordsTheVerifiedOwner(t *testing.T) {
	f := newPerCall(t)
	verified := identitybroker.Owner{Issuer: testOwner.Issuer, Subject: "alice-verified"}
	identitybroker.SetVerifiedOwner(f.b, f.s.ID, verified)
	if _, err := f.authorize(callRequest(f.s.ID, f.cred)); err != nil {
		t.Fatalf("AuthorizeCall: %v", err)
	}
	if d := f.rec.Decisions(); len(d) != 1 || d[0].Owner != verified {
		t.Fatalf("decisions = %+v, want one naming the verified owner %+v", d, verified)
	}
}

func TestAuthorizeCallRejectedTokenRetry(t *testing.T) {
	f := newPerCall(t)
	report := func(id string) identitybroker.AuthorizeCallRequest {
		req := callRequest(f.s.ID, f.cred)
		req.RejectedAttemptID = id
		return req
	}
	// A report for another call consumes the attempt.
	mismatchedCall, err := f.authorize(callRequest(f.s.ID, f.cred))
	if err != nil {
		t.Fatalf("AuthorizeCall: %v", err)
	}
	mismatched := report(mismatchedCall.AttemptID)
	mismatched.Tool = "github.issue_write"
	_, err = f.authorize(mismatched)
	wantError(t, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied, f.cred)
	_, err = f.authorize(report(mismatchedCall.AttemptID))
	wantError(t, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied, f.cred)

	// A report with a credential that fails check 3 does not consume it.
	forgedCall, err := f.authorize(callRequest(f.s.ID, f.cred))
	if err != nil {
		t.Fatalf("AuthorizeCall: %v", err)
	}
	other := openGranted(t)
	forged := report(forgedCall.AttemptID)
	forged.Credential = other.cred
	_, err = f.authorize(forged)
	wantError(t, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied, f.cred, other.cred)
	if _, err := f.authorize(report(forgedCall.AttemptID)); err != nil {
		t.Fatalf("a report after a forged one: %v", err)
	}
}

// TestAuthorizeCallConcurrentRejectionReports shows two concurrent reports
// that one attempt was rejected get one retry: the other is denied.
func TestAuthorizeCallConcurrentRejectionReports(t *testing.T) {
	f := newPerCall(t)
	first, err := f.authorize(callRequest(f.s.ID, f.cred))
	if err != nil {
		t.Fatalf("AuthorizeCall: %v", err)
	}
	req := callRequest(f.s.ID, f.cred)
	req.RejectedAttemptID = first.AttemptID
	var errs [2]error
	var wg sync.WaitGroup
	for i := range errs {
		wg.Go(func() { _, errs[i] = f.authorize(req) })
	}
	wg.Wait()
	allowed := 0
	for _, err := range errs {
		if err == nil {
			allowed++
			continue
		}
		wantError(t, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied, f.cred)
	}
	if allowed != 1 {
		t.Fatalf("%d reports of one rejected attempt were allowed, want 1 (errors %v)", allowed, errs)
	}
}

func TestAuthorizeCallKeepsTokensSecret(t *testing.T) {
	f := newPerCall(t)
	auth, err := f.authorize(callRequest(f.s.ID, f.cred))
	if err != nil {
		t.Fatalf("AuthorizeCall: %v", err)
	}
	var errs []error
	f.as2.answer = func(int, identitybroker.Redemption) (identitybroker.GatewayTokenResult, error) {
		return identitybroker.GatewayTokenResult{}, refusal(400, "invalid_grant", "")
	}
	_, err = f.authorize(callRequest(f.s.ID, f.cred))
	errs = append(errs, err)
	f.issuer.answer = func(int, identitybroker.IDJAGRequest) (identitybroker.IDJAGResult, error) {
		return identitybroker.IDJAGResult{IDJAG: idjagToken(map[string]any{"typ": "JWT"}, nil)}, nil
	}
	_, err = f.authorize(callRequest(f.s.ID, f.cred))
	errs = append(errs, err)

	grant, _ := identitybroker.SessionGrant(f.b, f.s.ID)
	secrets := append([]identitybroker.Token{grant.RefreshToken, f.cred}, f.issuer.tokens()...)
	secrets = append(secrets, f.as2.tokens()...)
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
	leaks := func(text string) bool {
		for _, s := range secrets {
			if strings.Contains(text, s.Value()) {
				return true
			}
		}
		return false
	}
	// Positive control: the scan finds a captured token planted in a copy.
	if !leaks(out.String() + auth.GatewayToken.Value()) {
		t.Fatal("the scan missed a planted gateway token")
	}
	if leaks(out.String()) {
		t.Fatal("an error, record, or formatted authorization carries a token")
	}
}
