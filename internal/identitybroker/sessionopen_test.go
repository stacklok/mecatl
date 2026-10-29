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
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/authserverclient"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

const (
	openHost        = "hostS42"
	openIncarnation = "inc_abcdefghijklmnopqrstuvwxya"
	// harnessRefreshPrefix starts every refresh token the harness AS-1
	// issues, so an error can be checked for one the test never saw.
	harnessRefreshPrefix = "as1-grant-rt-"
	// waitLimit bounds every wait on another goroutine.
	waitLimit = 10 * time.Second
)

// openClock is a settable clock safe to read from the harness's goroutines.
type openClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *openClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *openClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// openFixture is a broker that converts access tokens at the harness AS-1
// through the real adapter, on one clock.
type openFixture struct {
	h     *identityharness.Harness
	clock *openClock
	b     *identitybroker.Broker
}

func newOpenFixture(t *testing.T) *openFixture {
	t.Helper()
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := identitybroker.NewClientAssertionSigner(clientKey, 0)
	if err != nil {
		t.Fatal(err)
	}
	clock := &openClock{now: time.Unix(1_800_000_000, 0)}
	h := identityharness.New(t, identityharness.Options{
		Now:        clock.Now,
		BrokerKeys: []identityharness.BrokerKey{{KeyID: signer.JWKS().Keys[0].KeyID, Public: &clientKey.PublicKey}},
	})
	as1, err := authserverclient.New(authserverclient.Config{
		Issuer: h.AS1.Issuer(), TokenEndpoint: h.AS1.TokenEndpoint(), Assertions: signer,
		HTTPClient: h.HTTPClient(), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	b := newBroker(t, identitybroker.WithAuthServer(as1))
	identitybroker.SetClock(b, clock.Now)
	return &openFixture{h: h, clock: clock, b: b}
}

func (f *openFixture) owner(user string) identitybroker.Owner {
	o := f.h.Owner(user)
	return identitybroker.Owner{Issuer: o.Issuer, Subject: o.Subject}
}

func (f *openFixture) token(user string) identitybroker.Token {
	return identitybroker.NewToken(f.h.AS1.MintAccessToken(identityharness.AccessTokenSpec{User: user}))
}

func (f *openFixture) open(host string, owner identitybroker.Owner, token identitybroker.Token) (identitybroker.Session, error) {
	return f.b.OpenSession(context.Background(), testWorkload, identitybroker.OpenSessionRequest{
		HostSessionID: host, Incarnation: openIncarnation, Owner: owner, AccessToken: token,
	})
}

func (f *openFixture) find(t *testing.T, host string) (identitybroker.Session, bool) {
	t.Helper()
	return findSession(t, f.b, host)
}

func findSession(t *testing.T, b *identitybroker.Broker, host string) (identitybroker.Session, bool) {
	t.Helper()
	s, ok, err := b.FindSession(context.Background(), testWorkload, identitybroker.FindSessionRequest{RootHostSessionID: host})
	if err != nil {
		t.Fatalf("FindSession: %v", err)
	}
	return s, ok
}

// attempts is how many requests reached AS-1's token endpoint.
func (f *openFixture) attempts() int { return len(f.h.AS1.ReceivedClientAssertions()) }

// asCalled is a snapshot of what AS-1 saw, to show a call did not reach it.
type asCalled struct{ requests, grants, refusals int }

func (f *openFixture) called() asCalled {
	return asCalled{f.attempts(), f.h.AS1.GrantsIssued(), len(f.h.AS1.ExchangeRefusals())}
}

// wantNoVerifiedOwner fails t if session id has a verified owner.
func wantNoVerifiedOwner(t *testing.T, b *identitybroker.Broker, id string) {
	t.Helper()
	if _, ok := identitybroker.VerifiedOwner(b, id); ok {
		t.Fatal("the session has a verified owner, but AS-1 accepted no token for it")
	}
}

// jti reads an access token's jti.
func jti(t *testing.T, token identitybroker.Token) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(strings.Split(token.Value(), ".")[1])
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		JTI string `json:"jti"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c.JTI
}

// wantError fails t unless err is an *identitybroker.Error with code and
// reason, and carries none of secrets and no harness refresh token.
func wantError(t *testing.T, err error, code identitybroker.Code, reason identitybroker.Reason, secrets ...identitybroker.Token) {
	t.Helper()
	var e *identitybroker.Error
	if !errors.As(err, &e) || e.Code != code || e.Reason != reason {
		t.Fatalf("error = %v, want %s %q", err, code, reason)
	}
	if strings.Contains(err.Error(), harnessRefreshPrefix) {
		t.Fatalf("error %q discloses a refresh token", err)
	}
	for _, s := range secrets {
		if !s.IsZero() && strings.Contains(err.Error(), s.Value()) {
			t.Fatalf("error %q discloses a token", err)
		}
	}
}

// receive returns the next value from ch, failing t after waitLimit.
func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(waitLimit):
		t.Fatalf("no result within %v", waitLimit)
		panic("unreachable")
	}
}

// send sends v on ch, failing t after waitLimit.
func send[T any](t *testing.T, ch chan<- T, v T) {
	t.Helper()
	select {
	case ch <- v:
	case <-time.After(waitLimit):
		t.Fatalf("not received within %v", waitLimit)
	}
}

func TestSessionOpenConvertsTheAccessToken(t *testing.T) {
	f := newOpenFixture(t)
	alice := f.owner(identityharness.Alice)
	token := f.token(identityharness.Alice)
	s, err := f.open(openHost, alice, token)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if !s.HoldsRefreshToken || s.Owner != alice {
		t.Fatalf("session = %+v, want Alice's, holding a refresh token", s)
	}
	if got := f.h.AS1.GrantsIssued(); got != 1 {
		t.Fatalf("AS-1 issued %d grants, want 1", got)
	}
	held, ok := identitybroker.SessionGrant(f.b, s.ID)
	if !ok || held.VerifiedOwner != alice || held.SourceJTI != jti(t, token) {
		t.Fatalf("held grant: ok %v, verified owner %+v, source jti matches %v; want Alice's grant from the token", ok, held.VerifiedOwner, held.SourceJTI == jti(t, token))
	}
	grant, ok := f.h.AS1.Grant(held.RefreshToken.Value())
	// No stored expiry yet: expires_at is left out and AS-1 applies its
	// maximum, which the broker stores.
	wantExpiry := f.clock.Now().Add(identityharness.DefaultGrantMaxLifetime)
	if !ok || !grant.Active || grant.SourceJTI != jti(t, token) || !grant.ExpiresAt.Equal(wantExpiry) || !held.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("AS-1 grant %+v (%v), held expiry %v; want an active grant for the token expiring at %v", grant, ok, held.ExpiresAt, wantExpiry)
	}
	if found, ok := f.find(t, openHost); !ok || found != s {
		t.Fatalf("FindSession = %+v, %v; want %+v", found, ok, s)
	}
	// A later open without a token returns the session, still holding it.
	again, err := f.open(openHost, alice, identitybroker.Token{})
	if err != nil || again != s || f.h.AS1.GrantsIssued() != 1 {
		t.Fatalf("tokenless reopen = %+v, %v (%d grants); want %+v and no new grant", again, err, f.h.AS1.GrantsIssued(), s)
	}
}

// TestSessionOpenNeverRepresentsAConvertedToken: an open repeating the token
// the session already converted returns the session without calling AS-1,
// which would refuse the spent token.
func TestSessionOpenNeverRepresentsAConvertedToken(t *testing.T) {
	f := newOpenFixture(t)
	alice := f.owner(identityharness.Alice)
	token := f.token(identityharness.Alice)
	s, err := f.open(openHost, alice, token)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.open(openHost, alice, token)
	if err != nil || again != s || !again.HoldsRefreshToken {
		t.Fatalf("repeated open = %+v, %v; want %+v", again, err, s)
	}
	if f.attempts() != 1 || f.h.AS1.GrantsIssued() != 1 || len(f.h.AS1.ExchangeRefusals()) != 0 {
		t.Fatalf("AS-1 saw %d requests, issued %d grants, refused %d; want the one conversion", f.attempts(), f.h.AS1.GrantsIssued(), len(f.h.AS1.ExchangeRefusals()))
	}
	held, _ := identitybroker.SessionGrant(f.b, s.ID)
	if grant, ok := f.h.AS1.Grant(held.RefreshToken.Value()); !ok || !grant.Active {
		t.Fatalf("first grant = %+v, %v; want it still active and held", grant, ok)
	}
}

func TestConcurrentSessionOpensWithOneTokenExchangeOnce(t *testing.T) {
	f := newOpenFixture(t)
	alice := f.owner(identityharness.Alice)
	token := f.token(identityharness.Alice)
	results := make(chan openResult, 2)
	for range 2 {
		go func() {
			s, err := f.open(openHost, alice, token)
			results <- openResult{s, err}
		}()
	}
	r1, r2 := receive(t, results), receive(t, results)
	if r1.err != nil || r2.err != nil || r1.s != r2.s || !r1.s.HoldsRefreshToken {
		t.Fatalf("concurrent opens = %+v, %v and %+v, %v; want one session holding a refresh token", r1.s, r1.err, r2.s, r2.err)
	}
	if f.attempts() != 1 || f.h.AS1.GrantsIssued() != 1 {
		t.Fatalf("AS-1 saw %d requests and issued %d grants, want one exchange", f.attempts(), f.h.AS1.GrantsIssued())
	}
}

func TestSessionOpenWithoutATokenOnlyPairs(t *testing.T) {
	f := newOpenFixture(t)
	s, err := f.open(openHost, f.owner(identityharness.Alice), identitybroker.Token{})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if s.HoldsRefreshToken || f.attempts() != 0 {
		t.Fatalf("session %+v, %d exchanges; want a paired session and no exchange", s, f.attempts())
	}
	wantNoVerifiedOwner(t, f.b, s.ID)
}

func TestSessionOpenIgnoresTokensWithoutAnAuthServer(t *testing.T) {
	f := newOpenFixture(t)
	b := newBroker(t) // no WithAuthServer
	s, err := b.OpenSession(context.Background(), testWorkload, identitybroker.OpenSessionRequest{
		HostSessionID: openHost, Incarnation: openIncarnation, Owner: f.owner(identityharness.Bob),
		AccessToken: f.token(identityharness.Alice), // even a mismatched one
	})
	if err != nil || s.HoldsRefreshToken || f.attempts() != 0 {
		t.Fatalf("OpenSession = %+v, %v, %d exchanges; want a paired session and no exchange", s, err, f.attempts())
	}
}

func TestSessionOpenBrokerCheck1RefusesWithoutCallingAS1(t *testing.T) {
	subID := func(members map[string]string) func(*openFixture) identitybroker.Token {
		return func(f *openFixture) identitybroker.Token {
			for k, v := range members {
				if v == "IDP" {
					members[k] = f.h.IdP.Issuer()
				}
			}
			return identitybroker.NewToken(f.h.AS1.MintAccessToken(identityharness.AccessTokenSpec{SubID: members}))
		}
	}
	tests := []struct {
		name   string
		token  func(*openFixture) identitybroker.Token
		code   identitybroker.Code
		reason identitybroker.Reason
	}{
		{"another user's token", func(f *openFixture) identitybroker.Token { return f.token(identityharness.Bob) },
			identitybroker.CodePermissionDenied, identitybroker.ReasonOwnerMismatch},
		{"no sub_id", func(f *openFixture) identitybroker.Token {
			return identitybroker.NewToken(f.h.AS1.MintAccessToken(identityharness.AccessTokenSpec{OmitSubID: true}))
		}, identitybroker.CodeUnauthenticated, identitybroker.ReasonSubjectInvalid},
		{"sub_id as a string", func(f *openFixture) identitybroker.Token {
			return identitybroker.NewToken(f.h.AS1.MintAccessToken(identityharness.AccessTokenSpec{SubID: identityharness.Alice}))
		}, identitybroker.CodeUnauthenticated, identitybroker.ReasonSubjectInvalid},
		{"sub_id of another format", subID(map[string]string{"format": "email", "iss": "IDP", "sub": identityharness.Alice}),
			identitybroker.CodeUnauthenticated, identitybroker.ReasonSubjectInvalid},
		{"sub_id without sub", subID(map[string]string{"format": "iss_sub", "iss": "IDP"}),
			identitybroker.CodeUnauthenticated, identitybroker.ReasonSubjectInvalid},
		{"sub_id without iss", subID(map[string]string{"format": "iss_sub", "sub": identityharness.Alice}),
			identitybroker.CodeUnauthenticated, identitybroker.ReasonSubjectInvalid},
		{"not a JWT", func(*openFixture) identitybroker.Token { return identitybroker.NewToken("garbage-secret-value") },
			identitybroker.CodeUnauthenticated, identitybroker.ReasonSubjectInvalid},
		{"payload not base64", func(*openFixture) identitybroker.Token { return identitybroker.NewToken("a.!!!.c") },
			identitybroker.CodeUnauthenticated, identitybroker.ReasonSubjectInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newOpenFixture(t)
			token := tt.token(f)
			_, err := f.open(openHost, f.owner(identityharness.Alice), token)
			wantError(t, err, tt.code, tt.reason, token)
			if got := f.called(); got != (asCalled{}) {
				t.Fatalf("AS-1 was called: %+v", got)
			}
			// The new session is paired with the reported owner, with no
			// refresh token and no verified owner.
			s, ok := f.find(t, openHost)
			if !ok || s.HoldsRefreshToken || s.Owner != f.owner(identityharness.Alice) {
				t.Fatalf("FindSession = %+v, %v; want Alice's paired session", s, ok)
			}
			wantNoVerifiedOwner(t, f.b, s.ID)
		})
	}
}

func TestSessionOpenBrokerCheck2RefusesAnotherVerifiedOwner(t *testing.T) {
	t.Run("another reported owner", func(t *testing.T) {
		f := newOpenFixture(t)
		if _, err := f.open(openHost, f.owner(identityharness.Alice), f.token(identityharness.Alice)); err != nil {
			t.Fatal(err)
		}
		before := f.called()
		bobToken := f.token(identityharness.Bob)
		_, err := f.open(openHost, f.owner(identityharness.Bob), bobToken)
		wantError(t, err, identitybroker.CodePermissionDenied, identitybroker.ReasonSessionOwned, bobToken)
		if got := f.called(); got != before {
			t.Fatalf("AS-1 was called: %+v, before %+v", got, before)
		}
	})
	t.Run("another verified owner", func(t *testing.T) {
		f := newOpenFixture(t)
		s, err := f.open(openHost, f.owner(identityharness.Alice), identitybroker.Token{})
		if err != nil {
			t.Fatal(err)
		}
		// Unreachable through the public calls: the verified owner is only
		// recorded from a token matching the reported owner.
		identitybroker.SetVerifiedOwner(f.b, s.ID, f.owner(identityharness.Bob))
		token := f.token(identityharness.Alice)
		_, err = f.open(openHost, f.owner(identityharness.Alice), token)
		wantError(t, err, identitybroker.CodePermissionDenied, identitybroker.ReasonSessionOwned, token)
		if got := f.called(); got != (asCalled{}) {
			t.Fatalf("AS-1 was called: %+v", got)
		}
	})
}

// TestSessionOpenReplacesAndNeverExtends: an open converting another token
// while the session holds one stores the new token, discarding the old, and
// asks for no later expiry than the session's.
func TestSessionOpenReplacesAndNeverExtends(t *testing.T) {
	f := newOpenFixture(t)
	alice := f.owner(identityharness.Alice)
	s, err := f.open(openHost, alice, f.token(identityharness.Alice))
	if err != nil {
		t.Fatal(err)
	}
	first, _ := identitybroker.SessionGrant(f.b, s.ID)
	f.clock.Advance(time.Hour)
	second := f.token(identityharness.Alice)
	if _, err := f.open(openHost, alice, second); err != nil {
		t.Fatalf("second open: %v", err)
	}
	held, ok := identitybroker.SessionGrant(f.b, s.ID)
	if !ok || held.SourceJTI != jti(t, second) || held.RefreshToken.Value() == first.RefreshToken.Value() {
		t.Fatal("the session does not hold the second token's refresh token")
	}
	grant, _ := f.h.AS1.Grant(held.RefreshToken.Value())
	// AS-1 grants the requested expires_at: the first grant's expiry.
	if !grant.ExpiresAt.Equal(first.ExpiresAt) || !held.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("second grant expires %v (held %v), want the session's hard expiry %v", grant.ExpiresAt, held.ExpiresAt, first.ExpiresAt)
	}
	if f.h.AS1.GrantsIssued() != 2 {
		t.Fatalf("AS-1 issued %d grants, want 2", f.h.AS1.GrantsIssued())
	}
}

// TestSessionOpenFailedReplaceKeepsTheHeldToken: a replacing open that fails
// leaves the session's refresh token as it was.
func TestSessionOpenFailedReplaceKeepsTheHeldToken(t *testing.T) {
	for name, fault := range map[string]identityharness.Fault{
		"503":        {Status: 503},
		"token_used": {Error: "invalid_request", Reason: "token_used"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newOpenFixture(t)
			alice := f.owner(identityharness.Alice)
			first := f.token(identityharness.Alice)
			s, err := f.open(openHost, alice, first)
			if err != nil {
				t.Fatal(err)
			}
			f.h.AS1.FailNextExchange(fault)
			if _, err := f.open(openHost, alice, f.token(identityharness.Alice)); err == nil {
				t.Fatal("the replacing open succeeded")
			}
			held, ok := identitybroker.SessionGrant(f.b, s.ID)
			if found, _ := f.find(t, openHost); !ok || held.SourceJTI != jti(t, first) || !found.HoldsRefreshToken {
				t.Fatal("a failed replacing open changed the held refresh token")
			}
		})
	}
}

// TestSessionHardExpiryClosesTheSession: once the held refresh token's
// expiry passes, every lookup finds the session closed, and the next open
// pairs a new broker session.
func TestSessionHardExpiryClosesTheSession(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		// lookup is the first call after the expiry.
		lookup func(t *testing.T, f *openFixture, old identitybroker.Session, cred identitybroker.Token)
	}{
		{"a token-bearing open", func(t *testing.T, f *openFixture, old identitybroker.Session, _ identitybroker.Token) {
			next, err := f.open(openHost, old.Owner, f.token(identityharness.Alice))
			if err != nil || next.ID == old.ID || !next.HoldsRefreshToken {
				t.Fatalf("open = %+v, %v; want a new session holding a refresh token", next, err)
			}
			// The new session has no hard expiry yet: AS-1 applies its maximum.
			held, _ := identitybroker.SessionGrant(f.b, next.ID)
			if want := f.clock.Now().Add(identityharness.DefaultGrantMaxLifetime); !held.ExpiresAt.Equal(want) {
				t.Fatalf("new grant expires %v, want %v", held.ExpiresAt, want)
			}
		}},
		{"a tokenless open", func(t *testing.T, f *openFixture, old identitybroker.Session, _ identitybroker.Token) {
			next, err := f.open(openHost, old.Owner, identitybroker.Token{})
			if err != nil || next.ID == old.ID || next.HoldsRefreshToken {
				t.Fatalf("open = %+v, %v; want a new paired session", next, err)
			}
		}},
		{"FindSession", func(t *testing.T, f *openFixture, _ identitybroker.Session, _ identitybroker.Token) {
			if s, ok := f.find(t, openHost); ok {
				t.Fatalf("FindSession = %+v, want none", s)
			}
		}},
		{"RegisterAgent", func(t *testing.T, f *openFixture, old identitybroker.Session, _ identitybroker.Token) {
			_, err := f.b.RegisterAgent(ctx, testWorkload, mainAgent(old))
			wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed)
		}},
		{"AuthorizeCall", func(t *testing.T, f *openFixture, old identitybroker.Session, cred identitybroker.Token) {
			_, err := f.b.AuthorizeCall(ctx, testWorkload, identitybroker.AuthorizeCallRequest{
				SessionID: old.ID, Credential: cred, OperationID: "op-1", Target: "gateway", Tool: "search",
			})
			wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed, cred)
		}},
		{"VerifyCredential", func(t *testing.T, f *openFixture, old identitybroker.Session, cred identitybroker.Token) {
			_, err := f.b.VerifyCredential(ctx, identitybroker.VerifyCredentialRequest{SessionID: old.ID, Credential: cred})
			wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed, cred)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newOpenFixture(t)
			old, err := f.open(openHost, f.owner(identityharness.Alice), f.token(identityharness.Alice))
			if err != nil {
				t.Fatal(err)
			}
			// Register just before the expiry, so the credential outlives it.
			f.clock.Advance(identityharness.DefaultGrantMaxLifetime - 2*time.Minute)
			reg, err := f.b.RegisterAgent(ctx, testWorkload, mainAgent(old))
			if err != nil {
				t.Fatalf("RegisterAgent before the expiry: %v", err)
			}
			f.clock.Advance(2 * time.Minute)

			tt.lookup(t, f, old, reg.Credential)

			if _, ok := identitybroker.SessionGrant(f.b, old.ID); ok {
				t.Fatal("the expired session still holds a refresh token")
			}
			_, err = f.b.RegisterAgent(ctx, testWorkload, mainAgent(old))
			wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed)
		})
	}
}

// TestSessionWithoutARefreshTokenDoesNotExpire: a session that holds no
// refresh token has no hard expiry.
func TestSessionWithoutARefreshTokenDoesNotExpire(t *testing.T) {
	f := newOpenFixture(t)
	s, err := f.open(openHost, f.owner(identityharness.Alice), identitybroker.Token{})
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(10 * identityharness.DefaultGrantMaxLifetime)
	if found, ok := f.find(t, openHost); !ok || found.ID != s.ID {
		t.Fatalf("FindSession = %+v, %v; want %+v", found, ok, s)
	}
	if _, err := f.b.RegisterAgent(context.Background(), testWorkload, mainAgent(s)); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
}

// mainAgent is the registration of s's main agent.
func mainAgent(s identitybroker.Session) identitybroker.RegisterAgentRequest {
	return identitybroker.RegisterAgentRequest{
		SessionID: s.ID, HostSessionID: s.HostSessionID, Owner: s.Owner, Incarnation: s.Incarnation,
		Definition: identitybroker.DefinitionRef{Name: "main"},
	}
}

func TestSessionOpenEchoMismatchClosesTheSession(t *testing.T) {
	tests := []struct {
		name string
		// held converts a token first, so the open requests an expiry.
		held bool
		echo func(f *openFixture) identityharness.ExchangeGrant
	}{
		{name: "refresh-token output", echo: func(f *openFixture) identityharness.ExchangeGrant {
			return identityharness.ExchangeGrant{Type: identityharness.ExchangeGrantType, IssuableTokenTypes: []string{identitybroker.TokenTypeRefreshToken}, ExpiresAt: f.clock.Now().Add(time.Hour).Unix()}
		}},
		{name: "another type", echo: func(f *openFixture) identityharness.ExchangeGrant {
			return identityharness.ExchangeGrant{Type: "https://example.com/other", IssuableTokenTypes: []string{identityharness.TokenTypeIDJAG}, ExpiresAt: f.clock.Now().Add(time.Hour).Unix()}
		}},
		{name: "no expires_at", echo: func(*openFixture) identityharness.ExchangeGrant {
			return identityharness.ExchangeGrant{Type: identityharness.ExchangeGrantType, IssuableTokenTypes: []string{identityharness.TokenTypeIDJAG}}
		}},
		{name: "expires_at already passed", echo: func(f *openFixture) identityharness.ExchangeGrant {
			return identityharness.ExchangeGrant{Type: identityharness.ExchangeGrantType, IssuableTokenTypes: []string{identityharness.TokenTypeIDJAG}, ExpiresAt: f.clock.Now().Unix()}
		}},
		{name: "expires_at later than requested", held: true, echo: func(f *openFixture) identityharness.ExchangeGrant {
			return identityharness.ExchangeGrant{Type: identityharness.ExchangeGrantType, IssuableTokenTypes: []string{identityharness.TokenTypeIDJAG}, ExpiresAt: f.clock.Now().Add(identityharness.DefaultGrantMaxLifetime).Unix()}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newOpenFixture(t)
			alice := f.owner(identityharness.Alice)
			if tt.held {
				if _, err := f.open(openHost, alice, f.token(identityharness.Alice)); err != nil {
					t.Fatal(err)
				}
				f.clock.Advance(time.Hour)
			}
			s, err := f.open(openHost, alice, identitybroker.Token{})
			if err != nil {
				t.Fatal(err)
			}
			f.h.AS1.MismatchNextExchangeEcho(tt.echo(f))
			token := f.token(identityharness.Alice)
			_, err = f.open(openHost, alice, token)
			wantError(t, err, identitybroker.CodeInternal, "", token)
			if _, ok := f.find(t, openHost); ok {
				t.Fatal("the session is still open after a mismatched echo")
			}
			if _, ok := identitybroker.SessionGrant(f.b, s.ID); ok {
				t.Fatal("the session kept a refresh token after a mismatched echo")
			}
			_, err = f.b.RegisterAgent(context.Background(), testWorkload, identitybroker.RegisterAgentRequest{
				SessionID: s.ID, HostSessionID: openHost, Owner: alice, Incarnation: openIncarnation,
				Definition: identitybroker.DefinitionRef{Name: "main"},
			})
			wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed)
		})
	}
}

func TestSessionOpenResendsOnceAfterALostResponse(t *testing.T) {
	tests := []struct {
		name   string
		fault  func(*identityharness.AS1)
		code   identitybroker.Code
		reason identitybroker.Reason
		// attempts is how many requests reach AS-1.
		attempts int
		holds    bool
	}{
		{name: "processed, response lost: token_used", fault: (*identityharness.AS1).LoseNextExchangeResponse,
			code: identitybroker.CodeUnauthenticated, reason: identitybroker.ReasonTokenUsed, attempts: 2},
		{name: "dropped before processing", fault: (*identityharness.AS1).DropNextExchange, attempts: 2, holds: true},
		{name: "502", fault: func(a *identityharness.AS1) { a.FailNextExchange(identityharness.Fault{Status: 502}) }, attempts: 2, holds: true},
		{name: "504", fault: func(a *identityharness.AS1) { a.FailNextExchange(identityharness.Fault{Status: 504}) }, attempts: 2, holds: true},
		{name: "two 502s", fault: func(a *identityharness.AS1) {
			a.FailNextExchange(identityharness.Fault{Status: 502})
			a.FailNextExchange(identityharness.Fault{Status: 502})
		}, code: identitybroker.CodeUnavailable, attempts: 2},
		{name: "two lost responses", fault: func(a *identityharness.AS1) { a.DropNextExchange(); a.DropNextExchange() },
			code: identitybroker.CodeUnavailable, attempts: 2},
		{name: "503 is not resent", fault: func(a *identityharness.AS1) {
			a.FailNextExchange(identityharness.Fault{Status: 503})
			a.FailNextExchange(identityharness.Fault{Status: 503})
		}, code: identitybroker.CodeUnavailable, attempts: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newOpenFixture(t)
			tt.fault(f.h.AS1)
			token := f.token(identityharness.Alice)
			s, err := f.open(openHost, f.owner(identityharness.Alice), token)
			if tt.code == "" {
				if err != nil {
					t.Fatalf("OpenSession: %v", err)
				}
			} else {
				wantError(t, err, tt.code, tt.reason, token)
			}
			assertions := f.h.AS1.ReceivedClientAssertions()
			if len(assertions) != tt.attempts {
				t.Fatalf("%d requests reached AS-1, want %d", len(assertions), tt.attempts)
			}
			if len(assertions) == 2 && assertions[0] == assertions[1] {
				t.Fatal("the resend reused the first client assertion")
			}
			found, ok := f.find(t, openHost)
			if !ok || found.HoldsRefreshToken != tt.holds {
				t.Fatalf("FindSession = %+v, %v; want the session paired, holding a refresh token %v", found, ok, tt.holds)
			}
			if tt.code == "" && s != found {
				t.Fatalf("OpenSession = %+v, FindSession = %+v", s, found)
			}
			if !tt.holds {
				wantNoVerifiedOwner(t, f.b, found.ID)
			}
		})
	}
}

func TestSessionOpenMapsAS1Refusals(t *testing.T) {
	tests := []struct {
		name   string
		fault  identityharness.Fault
		code   identitybroker.Code
		reason identitybroker.Reason
	}{
		{"token_used", identityharness.Fault{Error: "invalid_request", Reason: "token_used"}, identitybroker.CodeUnauthenticated, identitybroker.ReasonTokenUsed},
		{"signed_out", identityharness.Fault{Error: "invalid_request", Reason: "signed_out"}, identitybroker.ReasonSignedOut.Code(), identitybroker.ReasonSignedOut},
		{"upstream_revoked", identityharness.Fault{Error: "invalid_request", Reason: "upstream_revoked"}, identitybroker.ReasonUpstreamRevoked.Code(), identitybroker.ReasonUpstreamRevoked},
		{"owner_action_required", identityharness.Fault{Error: "invalid_request", Reason: "owner_action_required"}, identitybroker.ReasonOwnerActionRequired.Code(), identitybroker.ReasonOwnerActionRequired},
		{"invalid_request with no reason", identityharness.Fault{Error: "invalid_request"}, identitybroker.CodeUnauthenticated, identitybroker.ReasonSubjectInvalid},
		{"subject_invalid", identityharness.Fault{Error: "invalid_request", Reason: "subject_invalid"}, identitybroker.CodeUnauthenticated, identitybroker.ReasonSubjectInvalid},
		{"503", identityharness.Fault{Status: 503}, identitybroker.CodeUnavailable, ""},
		{"429", identityharness.Fault{Status: 429}, identitybroker.CodeUnavailable, ""},
		{"server_error", identityharness.Fault{Status: 500, Error: "server_error"}, identitybroker.CodeUnavailable, ""},
		{"temporarily_unavailable", identityharness.Fault{Error: "temporarily_unavailable"}, identitybroker.CodeUnavailable, ""},
		{"invalid_client", identityharness.Fault{Status: 401, Error: "invalid_client"}, identitybroker.CodeInternal, ""},
		{"unauthorized_client", identityharness.Fault{Error: "unauthorized_client"}, identitybroker.CodeInternal, ""},
		{"invalid_scope", identityharness.Fault{Error: "invalid_scope"}, identitybroker.CodeInternal, ""},
		{"invalid_target", identityharness.Fault{Error: "invalid_target"}, identitybroker.CodeInternal, ""},
		{"invalid_authorization_details", identityharness.Fault{Error: "invalid_authorization_details", Reason: "malformed_entry"}, identitybroker.CodeInternal, ""},
		{"unknown error", identityharness.Fault{Error: "made_up"}, identitybroker.CodeInternal, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newOpenFixture(t)
			f.h.AS1.FailNextExchange(tt.fault)
			token := f.token(identityharness.Alice)
			_, err := f.open(openHost, f.owner(identityharness.Alice), token)
			wantError(t, err, tt.code, tt.reason, token)
			if f.attempts() != 1 {
				t.Fatalf("%d requests reached AS-1, want 1", f.attempts())
			}
			s, ok := f.find(t, openHost)
			if !ok || s.HoldsRefreshToken {
				t.Fatalf("FindSession = %+v, %v; want the session paired without a refresh token", s, ok)
			}
			wantNoVerifiedOwner(t, f.b, s.ID)
		})
	}
}

// TestSessionOpenReportsAS1sOwnRefusals runs AS-1's real checks 4 and 5.
func TestSessionOpenReportsAS1sOwnRefusals(t *testing.T) {
	t.Run("a token AS-1 did not sign: subject_invalid", func(t *testing.T) {
		f := newOpenFixture(t)
		token := identitybroker.NewToken(f.h.AS1.MintAccessToken(identityharness.AccessTokenSpec{ForeignKey: true}))
		_, err := f.open(openHost, f.owner(identityharness.Alice), token)
		wantError(t, err, identitybroker.CodeUnauthenticated, identitybroker.ReasonSubjectInvalid, token)
	})
	t.Run("a token converted for another session: token_used", func(t *testing.T) {
		f := newOpenFixture(t)
		token := f.token(identityharness.Alice)
		if _, err := f.open(openHost, f.owner(identityharness.Alice), token); err != nil {
			t.Fatal(err)
		}
		_, err := f.open("hostS43", f.owner(identityharness.Alice), token)
		wantError(t, err, identitybroker.CodeUnauthenticated, identitybroker.ReasonTokenUsed, token)
		if f.h.AS1.GrantsIssued() != 1 {
			t.Fatalf("AS-1 issued %d grants, want 1", f.h.AS1.GrantsIssued())
		}
	})
}

func TestSessionCloseDiscardsTheRefreshToken(t *testing.T) {
	f := newOpenFixture(t)
	alice := f.owner(identityharness.Alice)
	s, err := f.open(openHost, alice, f.token(identityharness.Alice))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.b.CloseSession(context.Background(), testWorkload, identitybroker.CloseSessionRequest{SessionID: s.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatal(err)
	}
	if _, ok := identitybroker.SessionGrant(f.b, s.ID); ok {
		t.Fatal("the closed session still holds a refresh token")
	}
	next, err := f.open(openHost, alice, f.token(identityharness.Alice))
	if err != nil || next.ID == s.ID || !next.HoldsRefreshToken {
		t.Fatalf("open after close = %+v, %v; want a new session holding a refresh token", next, err)
	}
}

func fixedNow() time.Time { return time.Unix(1_800_000_000, 0) }

// fakeAS1 is an AuthServer answering each call with answer.
type fakeAS1 struct {
	answer func(ctx context.Context, call int) (identitybroker.TokenExchangeResult, error)

	mu    sync.Mutex
	calls int
}

func (a *fakeAS1) Exchange(ctx context.Context, _ identitybroker.TokenExchange) (identitybroker.TokenExchangeResult, error) {
	a.mu.Lock()
	a.calls++
	n := a.calls
	a.mu.Unlock()
	return a.answer(ctx, n)
}

func (a *fakeAS1) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// grantResult is the answer AS-1 gives a matching exchange.
func grantResult(refresh string) identitybroker.TokenExchangeResult {
	return identitybroker.TokenExchangeResult{
		RefreshToken:    identitybroker.NewToken(refresh),
		IssuedTokenType: identitybroker.TokenTypeRefreshToken,
		Grants:          []identitybroker.ExchangeGrant{matchingGrant()},
	}
}

func matchingGrant() identitybroker.ExchangeGrant {
	return identitybroker.ExchangeGrant{
		Type: identitybroker.ExchangeGrantType, IssuableTokenTypes: []string{identitybroker.TokenTypeIDJAG},
		ExpiresAt: fixedNow().Add(time.Hour),
	}
}

// openWithFake opens openHost with a token on a broker converting at as1.
func openWithFake(t *testing.T, as1 identitybroker.AuthServer) (*identitybroker.Broker, identitybroker.Token, error) {
	t.Helper()
	b := newBroker(t, identitybroker.WithAuthServer(as1))
	identitybroker.SetClock(b, fixedNow)
	token := unsignedToken(t, testOwner)
	_, err := b.OpenSession(context.Background(), testWorkload, identitybroker.OpenSessionRequest{
		HostSessionID: openHost, Incarnation: openIncarnation, Owner: testOwner, AccessToken: token,
	})
	return b, token, err
}

// TestSessionOpenClosesOnAnAnswerItCannotUse covers answers the harness AS-1
// cannot be made to give: the session closes, and the refresh token, if
// any, is neither kept nor disclosed.
func TestSessionOpenClosesOnAnAnswerItCannotUse(t *testing.T) {
	const refresh = "rt-fake-secret"
	tests := map[string]func() (identitybroker.TokenExchangeResult, error){
		"another issued token type": func() (identitybroker.TokenExchangeResult, error) {
			r := grantResult(refresh)
			r.IssuedTokenType = "urn:ietf:params:oauth:token-type:access_token"
			return r, nil
		},
		"no grants": func() (identitybroker.TokenExchangeResult, error) {
			r := grantResult(refresh)
			r.Grants = nil
			return r, nil
		},
		"two grants": func() (identitybroker.TokenExchangeResult, error) {
			r := grantResult(refresh)
			r.Grants = append(r.Grants, matchingGrant())
			return r, nil
		},
		"no refresh token": func() (identitybroker.TokenExchangeResult, error) {
			return grantResult(""), nil
		},
		"undecodable response": func() (identitybroker.TokenExchangeResult, error) {
			return identitybroker.TokenExchangeResult{}, fmt.Errorf("adapter: %w (%w)", identitybroker.ErrMalformedResponse, identitybroker.ErrTransportConfiguration)
		},
	}
	for name, answer := range tests {
		t.Run(name, func(t *testing.T) {
			as1 := &fakeAS1{answer: func(context.Context, int) (identitybroker.TokenExchangeResult, error) { return answer() }}
			b, token, err := openWithFake(t, as1)
			wantError(t, err, identitybroker.CodeInternal, "", token, identitybroker.NewToken(refresh))
			if _, ok := findSession(t, b, openHost); ok {
				t.Fatal("the session is still open")
			}
			if as1.count() != 1 {
				t.Fatalf("%d exchanges, want 1", as1.count())
			}
		})
	}
	t.Run("a matching answer is kept", func(t *testing.T) {
		as1 := &fakeAS1{answer: func(context.Context, int) (identitybroker.TokenExchangeResult, error) {
			return grantResult(refresh), nil
		}}
		b, _, err := openWithFake(t, as1)
		if s, _ := findSession(t, b, openHost); err != nil || !s.HoldsRefreshToken {
			t.Fatalf("OpenSession: %v, holds %v; want the grant held", err, s.HoldsRefreshToken)
		}
	})
}

func TestSessionOpenTransportFailuresLeaveTheSessionOpen(t *testing.T) {
	tests := []struct {
		name   string
		answer func(ctx context.Context, cancel context.CancelFunc) error
		code   identitybroker.Code
	}{
		{"misconfigured connection", func(context.Context, context.CancelFunc) error {
			return fmt.Errorf("adapter: %w", identitybroker.ErrTransportConfiguration)
		}, identitybroker.CodeInternal},
		{"cancelled during the exchange", func(ctx context.Context, cancel context.CancelFunc) error {
			cancel()
			return ctx.Err()
		}, identitybroker.CodeUnavailable},
		// A lost response is resent only while the open's context lasts.
		{"lost response after cancellation", func(_ context.Context, cancel context.CancelFunc) error {
			cancel()
			return fmt.Errorf("adapter: %w", identitybroker.ErrNoResponse)
		}, identitybroker.CodeUnavailable},
		{"deadline exceeded", func(context.Context, context.CancelFunc) error {
			return fmt.Errorf("adapter: %w", context.DeadlineExceeded)
		}, identitybroker.CodeUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			as1 := &fakeAS1{answer: func(callCtx context.Context, _ int) (identitybroker.TokenExchangeResult, error) {
				return identitybroker.TokenExchangeResult{}, tt.answer(callCtx, cancel)
			}}
			b := newBroker(t, identitybroker.WithAuthServer(as1))
			identitybroker.SetClock(b, fixedNow)
			token := unsignedToken(t, testOwner)
			_, err := b.OpenSession(ctx, testWorkload, identitybroker.OpenSessionRequest{
				HostSessionID: openHost, Incarnation: openIncarnation, Owner: testOwner, AccessToken: token,
			})
			wantError(t, err, tt.code, "", token)
			if as1.count() != 1 {
				t.Fatalf("%d exchanges, want 1 and no resend", as1.count())
			}
			s, ok := findSession(t, b, openHost)
			if !ok || s.HoldsRefreshToken {
				t.Fatalf("FindSession = %+v, %v; want the session open without a refresh token", s, ok)
			}
			wantNoVerifiedOwner(t, b, s.ID)
		})
	}
}

// blockingAS1 is an AuthServer whose exchanges wait for release, so a test
// can act while one is in flight.
type blockingAS1 struct {
	entered chan struct{}
	release chan struct{}

	mu                     sync.Mutex
	calls, inFlight, maxIn int
}

func newBlockingAS1() *blockingAS1 {
	return &blockingAS1{entered: make(chan struct{}, 4), release: make(chan struct{})}
}

func (a *blockingAS1) Exchange(ctx context.Context, _ identitybroker.TokenExchange) (identitybroker.TokenExchangeResult, error) {
	a.mu.Lock()
	a.calls++
	a.inFlight++
	a.maxIn = max(a.maxIn, a.inFlight)
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.inFlight--
		a.mu.Unlock()
	}()
	a.entered <- struct{}{}
	select {
	case <-a.release:
	case <-ctx.Done():
		return identitybroker.TokenExchangeResult{}, ctx.Err()
	}
	return grantResult("rt-blocking-" + rand.Text()), nil
}

// unsignedToken is an access token whose payload check 1 reads, with a
// fresh jti; the fake AuthServers accept anything.
func unsignedToken(t *testing.T, owner identitybroker.Owner) identitybroker.Token {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"jti": rand.Text(), "sub_id": map[string]string{"format": "iss_sub", "iss": owner.Issuer, "sub": owner.Subject},
	})
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	return identitybroker.NewToken(enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString(payload) + ".sig")
}

type openResult struct {
	s   identitybroker.Session
	err error
}

func TestSessionOpenExchangeDoesNotBlockOtherCalls(t *testing.T) {
	as1 := newBlockingAS1()
	b := newBroker(t, identitybroker.WithAuthServer(as1))
	identitybroker.SetClock(b, fixedNow)
	ctx := context.Background()
	req := identitybroker.OpenSessionRequest{HostSessionID: openHost, Incarnation: openIncarnation, Owner: testOwner}

	withToken := req
	withToken.AccessToken = unsignedToken(t, testOwner)
	done := make(chan openResult, 1)
	go func() {
		s, err := b.OpenSession(ctx, testWorkload, withToken)
		done <- openResult{s, err}
	}()
	receive(t, as1.entered)

	// The same host session: found, registered into, and not yet holding.
	s, ok, err := b.FindSession(ctx, testWorkload, identitybroker.FindSessionRequest{RootHostSessionID: openHost})
	if err != nil || !ok || s.HoldsRefreshToken {
		t.Fatalf("FindSession during the exchange = %+v, %v, %v", s, ok, err)
	}
	reg, err := b.RegisterAgent(ctx, testWorkload, identitybroker.RegisterAgentRequest{
		SessionID: s.ID, HostSessionID: openHost, Owner: testOwner, Incarnation: openIncarnation,
		Definition: identitybroker.DefinitionRef{Name: "main"},
	})
	if err != nil {
		t.Fatalf("RegisterAgent during the exchange: %v", err)
	}
	if _, err := b.VerifyCredential(ctx, identitybroker.VerifyCredentialRequest{SessionID: s.ID, Credential: reg.Credential}); err != nil {
		t.Fatalf("VerifyCredential during the exchange: %v", err)
	}
	// Another host session: opened and closed.
	other := req
	other.HostSessionID = "hostS43"
	o, err := b.OpenSession(ctx, testWorkload, other)
	if err != nil {
		t.Fatalf("OpenSession of another host session during the exchange: %v", err)
	}
	if err := b.CloseSession(ctx, testWorkload, identitybroker.CloseSessionRequest{SessionID: o.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatalf("CloseSession of another session during the exchange: %v", err)
	}
	// Closing the converting session itself: the open then discards the
	// refresh token it gets.
	if err := b.CloseSession(ctx, testWorkload, identitybroker.CloseSessionRequest{SessionID: s.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatalf("CloseSession during the exchange: %v", err)
	}
	close(as1.release)
	res := receive(t, done)
	wantError(t, res.err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed, withToken.AccessToken)
	if strings.Contains(res.err.Error(), "rt-blocking-") {
		t.Fatalf("error %q discloses the refresh token", res.err)
	}
	if _, ok := identitybroker.SessionGrant(b, s.ID); ok {
		t.Fatal("the session closed during the exchange kept its refresh token")
	}
}

// waitForHostLockRefs waits until want opens hold or wait for host's open
// lock, yielding instead of sleeping, and fails t after waitLimit.
func waitForHostLockRefs(t *testing.T, b *identitybroker.Broker, host string, want int) {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for {
		if got, _ := identitybroker.HostLockRefs(b, host); got == want {
			return
		}
		if time.Now().After(deadline) {
			got, _ := identitybroker.HostLockRefs(b, host)
			t.Fatalf("%d opens hold or wait for the host lock, want %d", got, want)
		}
		runtime.Gosched()
	}
}

func TestSessionOpensOfOneHostSessionAreSerialized(t *testing.T) {
	as1 := newBlockingAS1()
	b := newBroker(t, identitybroker.WithAuthServer(as1))
	identitybroker.SetClock(b, fixedNow)
	ctx := context.Background()
	req := identitybroker.OpenSessionRequest{HostSessionID: openHost, Incarnation: openIncarnation, Owner: testOwner}

	first := req
	first.AccessToken = unsignedToken(t, testOwner)
	firstDone := make(chan openResult, 1)
	go func() {
		s, err := b.OpenSession(ctx, testWorkload, first)
		firstDone <- openResult{s, err}
	}()
	receive(t, as1.entered)
	waitForHostLockRefs(t, b, openHost, 1)

	// A waiting open gives up when its context ends, without an exchange.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err := b.OpenSession(cancelled, testWorkload, req)
	wantError(t, err, identitybroker.CodeUnavailable, "")

	// Opens of the same host session wait for the one in flight: a tokenless
	// one then sees its refresh token, and one with another token exchanges
	// after it, never alongside it.
	tokenless := make(chan openResult, 1)
	go func() {
		s, err := b.OpenSession(ctx, testWorkload, req)
		tokenless <- openResult{s, err}
	}()
	second := req
	second.AccessToken = unsignedToken(t, testOwner)
	secondDone := make(chan openResult, 1)
	go func() {
		s, err := b.OpenSession(ctx, testWorkload, second)
		secondDone <- openResult{s, err}
	}()
	// Both are counted on the lock the first open holds, so neither can pass
	// it before the first finishes.
	waitForHostLockRefs(t, b, openHost, 3)
	select {
	case r := <-tokenless:
		t.Fatalf("a tokenless open returned %+v, %v while an exchange was in flight", r.s, r.err)
	default:
	}

	send(t, as1.release, struct{}{})
	r1 := receive(t, firstDone)
	if r1.err != nil || !r1.s.HoldsRefreshToken {
		t.Fatalf("first open = %+v, %v", r1.s, r1.err)
	}
	// The second exchange starts once the first open releases the lock; the
	// tokenless open may run before or after it.
	receive(t, as1.entered)
	send(t, as1.release, struct{}{})
	r0, r2 := receive(t, tokenless), receive(t, secondDone)
	if r0.err != nil || !r0.s.HoldsRefreshToken || r0.s.ID != r1.s.ID {
		t.Fatalf("tokenless open = %+v, %v; want the first open's session, holding a refresh token", r0.s, r0.err)
	}
	if r2.err != nil || r2.s.ID != r1.s.ID {
		t.Fatalf("second open = %+v, %v", r2.s, r2.err)
	}
	as1.mu.Lock()
	calls, maxIn := as1.calls, as1.maxIn
	as1.mu.Unlock()
	if calls != 2 || maxIn != 1 {
		t.Fatalf("%d exchanges, at most %d at once; want 2, one at a time", calls, maxIn)
	}
	if refs, ok := identitybroker.HostLockRefs(b, openHost); ok {
		t.Fatalf("the host lock is still held by %d opens after every open finished", refs)
	}
}

// TestSessionOpenSurvivesAPanickingAuthServer: a panic in the AuthServer
// reaches the caller as an ordinary panic, with the broker's locks released.
func TestSessionOpenSurvivesAPanickingAuthServer(t *testing.T) {
	as1 := &fakeAS1{answer: func(context.Context, int) (identitybroker.TokenExchangeResult, error) {
		panic("auth server bug")
	}}
	b := newBroker(t, identitybroker.WithAuthServer(as1))
	identitybroker.SetClock(b, fixedNow)
	ctx := context.Background()
	req := identitybroker.OpenSessionRequest{HostSessionID: openHost, Incarnation: openIncarnation, Owner: testOwner}

	withToken := req
	withToken.AccessToken = unsignedToken(t, testOwner)
	recovered := make(chan any, 1)
	go func() {
		defer func() { recovered <- recover() }()
		_, _ = b.OpenSession(ctx, testWorkload, withToken)
	}()
	if got := receive(t, recovered); got != "auth server bug" {
		t.Fatalf("recovered %v, want the AuthServer's panic", got)
	}
	if refs, ok := identitybroker.HostLockRefs(b, openHost); ok {
		t.Fatalf("the host lock is still held by %d opens after the panic", refs)
	}

	done := make(chan error, 1)
	go func() {
		done <- func() error {
			s, err := b.OpenSession(ctx, testWorkload, req)
			if err != nil {
				return fmt.Errorf("OpenSession of the same host session: %w", err)
			}
			found, ok, err := b.FindSession(ctx, testWorkload, identitybroker.FindSessionRequest{RootHostSessionID: openHost})
			if err != nil || !ok || found.ID != s.ID {
				return fmt.Errorf("FindSession = %+v, %v, %v; want %+v", found, ok, err, s)
			}
			if err := b.CloseSession(ctx, testWorkload, identitybroker.CloseSessionRequest{SessionID: s.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
				return fmt.Errorf("CloseSession: %w", err)
			}
			other := req
			other.HostSessionID = "hostS43"
			if _, err := b.OpenSession(ctx, testWorkload, other); err != nil {
				return fmt.Errorf("OpenSession of another host session: %w", err)
			}
			return nil
		}()
	}()
	if err := receive(t, done); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{openHost, "hostS43"} {
		if refs, ok := identitybroker.HostLockRefs(b, host); ok {
			t.Fatalf("the host lock of %s is still held by %d opens", host, refs)
		}
	}
}

// TestSessionOpenNeverRepresentsATokenConvertedBefore: the broker remembers
// every token AS-1 may have converted, for any session, not only the one
// behind the held refresh token.
func TestSessionOpenNeverRepresentsATokenConvertedBefore(t *testing.T) {
	t.Run("A, B, then A in one session", func(t *testing.T) {
		f := newOpenFixture(t)
		alice := f.owner(identityharness.Alice)
		a, b := f.token(identityharness.Alice), f.token(identityharness.Alice)
		s, err := f.open(openHost, alice, a)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.open(openHost, alice, b); err != nil {
			t.Fatal(err)
		}
		again, err := f.open(openHost, alice, a)
		held, _ := identitybroker.SessionGrant(f.b, s.ID)
		if err != nil || again.ID != s.ID || !again.HoldsRefreshToken || held.SourceJTI != jti(t, b) {
			t.Fatalf("open with A again = %+v, %v; want the session holding B's grant", again, err)
		}
		if f.attempts() != 2 {
			t.Fatalf("%d requests reached AS-1, want 2", f.attempts())
		}
	})
	t.Run("A, expiry, B in a new session, then A", func(t *testing.T) {
		f := newOpenFixture(t)
		alice := f.owner(identityharness.Alice)
		// A outlives the first session's grant.
		a := identitybroker.NewToken(f.h.AS1.MintAccessToken(identityharness.AccessTokenSpec{User: identityharness.Alice, Lifetime: 24 * time.Hour}))
		first, err := f.open(openHost, alice, a)
		if err != nil {
			t.Fatal(err)
		}
		f.clock.Advance(identityharness.DefaultGrantMaxLifetime)
		second, err := f.open(openHost, alice, f.token(identityharness.Alice))
		if err != nil || second.ID == first.ID {
			t.Fatalf("open with B = %+v, %v; want a new session", second, err)
		}
		again, err := f.open(openHost, alice, a)
		if err != nil || again != second || f.attempts() != 2 {
			t.Fatalf("open with A again = %+v, %v after %d requests; want %+v and no new request", again, err, f.attempts(), second)
		}
	})
	t.Run("A with a mismatched echo, then A in a new session", func(t *testing.T) {
		f := newOpenFixture(t)
		alice := f.owner(identityharness.Alice)
		f.h.AS1.MismatchNextExchangeEcho(identityharness.ExchangeGrant{Type: "https://example.com/other", IssuableTokenTypes: []string{identityharness.TokenTypeIDJAG}})
		a := f.token(identityharness.Alice)
		if _, err := f.open(openHost, alice, a); err == nil {
			t.Fatal("the mismatched open succeeded")
		}
		_, err := f.open(openHost, alice, a)
		wantError(t, err, identitybroker.CodeUnauthenticated, identitybroker.ReasonTokenUsed, a)
		if f.attempts() != 1 {
			t.Fatalf("%d requests reached AS-1, want 1", f.attempts())
		}
		if s, ok := f.find(t, openHost); !ok || s.HoldsRefreshToken {
			t.Fatalf("FindSession = %+v, %v; want the new session paired without a refresh token", s, ok)
		}
	})
	t.Run("A is forgotten after its exp plus the skew", func(t *testing.T) {
		f := newOpenFixture(t)
		alice := f.owner(identityharness.Alice)
		a := f.token(identityharness.Alice)
		if _, err := f.open(openHost, alice, a); err != nil {
			t.Fatal(err)
		}
		if n := identitybroker.ConvertedTokens(f.b); n != 1 {
			t.Fatalf("%d converted tokens remembered, want 1", n)
		}
		f.clock.Advance(identityharness.DefaultAccessTokenLifetime + 5*time.Minute)
		if n := identitybroker.ConvertedTokens(f.b); n != 0 {
			t.Fatalf("%d converted tokens remembered past exp plus the skew, want 0", n)
		}
		// AS-1 is asked again, and refuses the expired token itself.
		_, err := f.open(openHost, alice, a)
		wantError(t, err, identitybroker.CodeUnauthenticated, identitybroker.ReasonSubjectInvalid, a)
		if f.attempts() != 2 {
			t.Fatalf("%d requests reached AS-1, want 2", f.attempts())
		}
	})
}

// TestSessionOpenRemembersTokensAS1MayHaveConverted: an exchange AS-1 may
// have processed remembers the token; one AS-1 surely refused or never
// processed does not.
func TestSessionOpenRemembersTokensAS1MayHaveConverted(t *testing.T) {
	tests := []struct {
		name  string
		fault func(*identityharness.AS1)
		want  int
	}{
		{"converted", func(*identityharness.AS1) {}, 1},
		{"token_used", func(a *identityharness.AS1) {
			a.FailNextExchange(identityharness.Fault{Error: "invalid_request", Reason: "token_used"})
		}, 1},
		{"lost response", (*identityharness.AS1).LoseNextExchangeResponse, 1},
		{"two lost responses", func(a *identityharness.AS1) { a.DropNextExchange(); a.DropNextExchange() }, 1},
		{"503", func(a *identityharness.AS1) { a.FailNextExchange(identityharness.Fault{Status: 503}) }, 0},
		{"invalid_client", func(a *identityharness.AS1) {
			a.FailNextExchange(identityharness.Fault{Status: 401, Error: "invalid_client"})
		}, 0},
		{"the token refused", func(a *identityharness.AS1) { a.FailNextExchange(identityharness.Fault{Error: "invalid_request"}) }, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newOpenFixture(t)
			tt.fault(f.h.AS1)
			_, _ = f.open(openHost, f.owner(identityharness.Alice), f.token(identityharness.Alice))
			if n := identitybroker.ConvertedTokens(f.b); n != tt.want {
				t.Fatalf("%d converted tokens remembered, want %d", n, tt.want)
			}
		})
	}
}

// oversizedStub is an AS-1 stand-in answering each request with the next of
// answers, as status and body.
func oversizedStub(t *testing.T, answers ...func(http.ResponseWriter)) (*identitybroker.Broker, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := int(requests.Add(1))
		if n > len(answers) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		answers[n-1](w)
	}))
	t.Cleanup(server.Close)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := identitybroker.NewClientAssertionSigner(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	as1, err := authserverclient.New(authserverclient.Config{
		Issuer: server.URL, TokenEndpoint: server.URL + "/token", Assertions: signer, HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	b := newBroker(t, identitybroker.WithAuthServer(as1))
	identitybroker.SetClock(b, fixedNow)
	return b, &requests
}

func answer(status int, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// TestSessionOpenOversizedAnswers runs oversized AS-1 answers through the
// real adapter: a 502 is still resent once, and a success still closes the
// session as an undecodable answer.
func TestSessionOpenOversizedAnswers(t *testing.T) {
	huge := strings.Repeat("x", 1<<20+1)
	ctx := context.Background()
	open := func(b *identitybroker.Broker) (identitybroker.Token, error) {
		token := unsignedToken(t, testOwner)
		_, err := b.OpenSession(ctx, testWorkload, identitybroker.OpenSessionRequest{
			HostSessionID: openHost, Incarnation: openIncarnation, Owner: testOwner, AccessToken: token,
		})
		return token, err
	}
	t.Run("an oversized 502 is resent once", func(t *testing.T) {
		grant := fmt.Sprintf(`{"access_token":"rt-stub-secret","issued_token_type":%q,"authorization_details":[{"type":%q,"issuable_token_types":[%q],"expires_at":%d}]}`,
			identitybroker.TokenTypeRefreshToken, identitybroker.ExchangeGrantType, identitybroker.TokenTypeIDJAG, fixedNow().Add(time.Hour).Unix())
		b, requests := oversizedStub(t, answer(http.StatusBadGateway, huge), answer(http.StatusOK, grant))
		if _, err := open(b); err != nil {
			t.Fatalf("OpenSession: %v", err)
		}
		if n := requests.Load(); n != 2 {
			t.Fatalf("%d requests, want 2", n)
		}
		if s, ok := findSession(t, b, openHost); !ok || !s.HoldsRefreshToken {
			t.Fatalf("FindSession = %+v, %v; want the session holding the resend's grant", s, ok)
		}
	})
	t.Run("an oversized success closes the session", func(t *testing.T) {
		b, requests := oversizedStub(t, answer(http.StatusOK, `{"access_token":"`+huge+`"}`))
		token, err := open(b)
		wantError(t, err, identitybroker.CodeInternal, "", token)
		if strings.Contains(err.Error(), "xxxx") {
			t.Fatalf("error %q discloses the response", err)
		}
		if n := requests.Load(); n != 1 {
			t.Fatalf("%d requests, want 1", n)
		}
		if _, ok := findSession(t, b, openHost); ok {
			t.Fatal("the session is still open after an oversized success")
		}
	})
}
