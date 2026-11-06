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
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server/as1servertest"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/authserverclient"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

const (
	openHost        = "hostS42"
	openIncarnation = "inc_abcdefghijklmnopqrstuvwxya"
	// as1RefreshPrefix starts every refresh token AS-1 issues, so an error
	// can be checked for one the test never saw.
	as1RefreshPrefix = "ory_rt_"
	// waitLimit bounds every wait on another goroutine.
	waitLimit = 10 * time.Second
)

// brokerClock is the broker's clock: wall time, which AS-1 stamps expiries
// with, plus an offset a test sets to move the broker past a session's hard
// expiry. It is safe for concurrent use.
type brokerClock struct{ offset atomic.Int64 }

func (c *brokerClock) Now() time.Time { return time.Now().Add(time.Duration(c.offset.Load())) }

// Set moves the clock so it reads at, and runs on from there.
func (c *brokerClock) Set(at time.Time) { c.offset.Store(int64(time.Until(at))) }

// Reset puts the clock back on wall time, so the broker accepts the expiries
// AS-1 stamps again.
func (c *brokerClock) Reset() { c.offset.Store(0) }

// openFixture is a broker that converts access tokens at the real AS-1
// through the real adapter and the fault transport.
type openFixture struct {
	as1   *as1servertest.AS1
	clock *brokerClock
	b     *identitybroker.Broker
}

func newOpenFixture(t *testing.T) *openFixture {
	t.Helper()
	return newOpenFixtureWith(t, nil)
}

// newOpenFixtureWith is newOpenFixture with AS-1's configuration changed by
// edit.
func newOpenFixtureWith(t *testing.T, edit func(*as1server.Config, *as1server.DelegateClient)) *openFixture {
	t.Helper()
	a := as1servertest.Start(t, identityharness.New(t, identityharness.Options{}), edit)
	clock := &brokerClock{}
	b := newBroker(t, identitybroker.WithAuthServer(a.Client(t)))
	identitybroker.SetClock(b, clock.Now)
	return &openFixture{as1: a, clock: clock, b: b}
}

func (f *openFixture) owner(user string) identitybroker.Owner {
	o := f.as1.Harness.Owner(user)
	return identitybroker.Owner{Issuer: o.Issuer, Subject: o.Subject}
}

// token signs user in at AS-1 and returns their access token.
func (f *openFixture) token(t *testing.T, user string) identitybroker.Token {
	t.Helper()
	return f.as1.SignIn(t, as1servertest.SignIn{User: user})
}

// crafted mints an access token at the harness's AS-1 as spec describes, for
// checks that refuse it before AS-1 is called.
func (f *openFixture) crafted(spec identityharness.AccessTokenSpec) identitybroker.Token {
	return identitybroker.NewToken(f.as1.Harness.AS1.MintAccessToken(spec))
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

// attempts is how many requests the broker sent AS-1's token endpoint,
// whether or not they reached AS-1.
func (f *openFixture) attempts() int { return len(f.as1.Faults.Requests()) }

// asCalled is a snapshot of what AS-1 saw, to show a call did not reach it.
type asCalled struct{ requests, conversions, refusals int }

func (f *openFixture) called() asCalled {
	return asCalled{f.attempts(), f.as1.Faults.Conversions(), len(f.as1.Faults.Refusals())}
}

// expire moves the broker's clock a second past the expiry of the grant
// session id holds.
func (f *openFixture) expire(t *testing.T, id string) identitybroker.HeldGrant {
	t.Helper()
	held, ok := identitybroker.SessionGrant(f.b, id)
	if !ok {
		t.Fatal("the session holds no refresh token")
	}
	f.clock.Set(held.ExpiresAt.Add(time.Second))
	return held
}

// waitPastMaximum waits until a grant AS-1 converts at its maximum lifetime
// expires at least a second after expiresAt, so a test tells the maximum
// apart from a requested expiry. AS-1 stamps expiries with wall time, which
// only waiting moves.
func waitPastMaximum(expiresAt time.Time) {
	time.Sleep(time.Until(expiresAt.Add(time.Second - as1servertest.IDJAGMaxLifetime)))
}

// wantNoVerifiedOwner fails t if session id has a verified owner.
func wantNoVerifiedOwner(t *testing.T, b *identitybroker.Broker, id string) {
	t.Helper()
	if _, ok := identitybroker.VerifiedOwner(b, id); ok {
		t.Fatal("the session has a verified owner, but AS-1 accepted no token for it")
	}
}

// wantAS1Refused fails t unless the last request reached AS-1 and AS-1
// refused it with code and, unless reason is empty, reason. ToolHive
// describes some refusals, such as invalid_client, in prose with no reason
// code.
func (f *openFixture) wantAS1Refused(t *testing.T, code, reason string) {
	t.Helper()
	reqs := f.as1.Faults.Requests()
	if len(reqs) == 0 {
		t.Fatal("no request reached AS-1")
	}
	if last := reqs[len(reqs)-1]; !last.Refused() || last.Fault != as1servertest.FaultNone || last.Code != code || reason != "" && last.Reason != reason {
		t.Fatalf("AS-1's answer: reached %v, status %d, %q (%q), fault %q; want its own refusal %q (%q)",
			last.Reached, last.Status, last.Code, last.Reason, last.Fault, code, reason)
	}
}

// accessClaims are the access-token claims the tests read.
type accessClaims struct {
	JTI string `json:"jti"`
	Exp int64  `json:"exp"`
}

func readClaims(t *testing.T, token identitybroker.Token) accessClaims {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(strings.Split(token.Value(), ".")[1])
	if err != nil {
		t.Fatal(err)
	}
	var c accessClaims
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// jti reads an access token's jti.
func jti(t *testing.T, token identitybroker.Token) string { return readClaims(t, token).JTI }

// wantError fails t unless err is an *identitybroker.Error with code and
// reason, and carries none of secrets and no AS-1 refresh token.
func wantError(t *testing.T, err error, code identitybroker.Code, reason identitybroker.Reason, secrets ...identitybroker.Token) {
	t.Helper()
	var e *identitybroker.Error
	if !errors.As(err, &e) || e.Code != code || e.Reason != reason {
		t.Fatalf("error = %v, want %s %q", err, code, reason)
	}
	if strings.Contains(err.Error(), as1RefreshPrefix) {
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
	token := f.token(t, identityharness.Alice)
	before := time.Now()
	s, err := f.open(openHost, alice, token)
	after := time.Now()
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if !s.HoldsRefreshToken || s.Owner != alice {
		t.Fatalf("session = %+v, want Alice's, holding a refresh token", s)
	}
	if got := f.as1.Faults.Conversions(); got != 1 {
		t.Fatalf("AS-1 converted %d tokens, want 1", got)
	}
	held, ok := identitybroker.SessionGrant(f.b, s.ID)
	if !ok || held.VerifiedOwner != alice || held.SourceJTI != jti(t, token) {
		t.Fatalf("held grant: ok %v, verified owner %+v, source jti matches %v; want Alice's grant from the token", ok, held.VerifiedOwner, held.SourceJTI == jti(t, token))
	}
	if !strings.HasPrefix(held.RefreshToken.Value(), as1RefreshPrefix) {
		t.Fatalf("the held refresh token does not start with %q", as1RefreshPrefix)
	}
	// No stored expiry yet: expires_at is left out and AS-1 applies its
	// maximum, in whole seconds of wall time, which the broker stores.
	earliest, latest := before.Add(as1servertest.IDJAGMaxLifetime).Truncate(time.Second), after.Add(as1servertest.IDJAGMaxLifetime+time.Second)
	if held.ExpiresAt.Before(earliest) || held.ExpiresAt.After(latest) {
		t.Fatalf("held expiry %v, want AS-1's maximum, between %v and %v", held.ExpiresAt, earliest, latest)
	}
	if found, ok := f.find(t, openHost); !ok || found != s {
		t.Fatalf("FindSession = %+v, %v; want %+v", found, ok, s)
	}
	// A later open without a token returns the session, still holding it.
	again, err := f.open(openHost, alice, identitybroker.Token{})
	if err != nil || again != s || f.attempts() != 1 {
		t.Fatalf("tokenless reopen = %+v, %v (%d requests to AS-1); want %+v and no new request", again, err, f.attempts(), s)
	}
}

// TestSessionOpenNeverRepresentsAConvertedToken: an open repeating the token
// the session already converted returns the session without calling AS-1,
// which would refuse the spent token.
func TestSessionOpenNeverRepresentsAConvertedToken(t *testing.T) {
	f := newOpenFixture(t)
	alice := f.owner(identityharness.Alice)
	token := f.token(t, identityharness.Alice)
	s, err := f.open(openHost, alice, token)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := identitybroker.SessionGrant(f.b, s.ID)
	again, err := f.open(openHost, alice, token)
	if err != nil || again != s || !again.HoldsRefreshToken {
		t.Fatalf("repeated open = %+v, %v; want %+v", again, err, s)
	}
	if got := f.called(); got != (asCalled{requests: 1, conversions: 1}) {
		t.Fatalf("AS-1 saw %+v; want the one conversion", got)
	}
	if held, ok := identitybroker.SessionGrant(f.b, s.ID); !ok || held.RefreshToken != first.RefreshToken {
		t.Fatal("the session no longer holds the first grant")
	}
}

func TestConcurrentSessionOpensWithOneTokenExchangeOnce(t *testing.T) {
	f := newOpenFixture(t)
	alice := f.owner(identityharness.Alice)
	token := f.token(t, identityharness.Alice)
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
	if f.attempts() != 1 || f.as1.Faults.Conversions() != 1 {
		t.Fatalf("AS-1 saw %d requests and converted %d tokens, want one exchange", f.attempts(), f.as1.Faults.Conversions())
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
		AccessToken: f.token(t, identityharness.Alice), // even a mismatched one
	})
	if err != nil || s.HoldsRefreshToken || f.attempts() != 0 {
		t.Fatalf("OpenSession = %+v, %v, %d exchanges; want a paired session and no exchange", s, err, f.attempts())
	}
}

// TestSessionOpenBrokerCheck1RefusesWithoutCallingAS1 crafts the malformed
// tokens at the harness's AS-1: the broker refuses them before AS-1 is called.
func TestSessionOpenBrokerCheck1RefusesWithoutCallingAS1(t *testing.T) {
	subID := func(members map[string]string) func(*testing.T, *openFixture) identitybroker.Token {
		return func(_ *testing.T, f *openFixture) identitybroker.Token {
			for k, v := range members {
				if v == "IDP" {
					members[k] = f.as1.Harness.IdP.Issuer()
				}
			}
			return f.crafted(identityharness.AccessTokenSpec{SubID: members})
		}
	}
	tests := []struct {
		name   string
		token  func(*testing.T, *openFixture) identitybroker.Token
		code   identitybroker.Code
		reason identitybroker.Reason
	}{
		{"another user's token", func(t *testing.T, f *openFixture) identitybroker.Token { return f.token(t, identityharness.Bob) },
			identitybroker.CodePermissionDenied, identitybroker.ReasonOwnerMismatch},
		{"no sub_id", func(_ *testing.T, f *openFixture) identitybroker.Token {
			return f.crafted(identityharness.AccessTokenSpec{OmitSubID: true})
		}, identitybroker.CodeUnauthenticated, identitybroker.ReasonSubjectInvalid},
		{"sub_id as a string", func(_ *testing.T, f *openFixture) identitybroker.Token {
			return f.crafted(identityharness.AccessTokenSpec{SubID: identityharness.Alice})
		}, identitybroker.CodeUnauthenticated, identitybroker.ReasonSubjectInvalid},
		{"sub_id of another format", subID(map[string]string{"format": "email", "iss": "IDP", "sub": identityharness.Alice}),
			identitybroker.CodeUnauthenticated, identitybroker.ReasonSubjectInvalid},
		{"sub_id without sub", subID(map[string]string{"format": "iss_sub", "iss": "IDP"}),
			identitybroker.CodeUnauthenticated, identitybroker.ReasonSubjectInvalid},
		{"sub_id without iss", subID(map[string]string{"format": "iss_sub", "sub": identityharness.Alice}),
			identitybroker.CodeUnauthenticated, identitybroker.ReasonSubjectInvalid},
		{"not a JWT", func(*testing.T, *openFixture) identitybroker.Token {
			return identitybroker.NewToken("garbage-secret-value")
		},
			identitybroker.CodeUnauthenticated, identitybroker.ReasonSubjectInvalid},
		{"payload not base64", func(*testing.T, *openFixture) identitybroker.Token { return identitybroker.NewToken("a.!!!.c") },
			identitybroker.CodeUnauthenticated, identitybroker.ReasonSubjectInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newOpenFixture(t)
			token := tt.token(t, f)
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
		if _, err := f.open(openHost, f.owner(identityharness.Alice), f.token(t, identityharness.Alice)); err != nil {
			t.Fatal(err)
		}
		before := f.called()
		bobToken := f.token(t, identityharness.Bob)
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
		token := f.token(t, identityharness.Alice)
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
	s, err := f.open(openHost, alice, f.token(t, identityharness.Alice))
	if err != nil {
		t.Fatal(err)
	}
	first, _ := identitybroker.SessionGrant(f.b, s.ID)
	// AS-1's maximum for the second token now ends after the first grant.
	waitPastMaximum(first.ExpiresAt)
	second := f.token(t, identityharness.Alice)
	if _, err := f.open(openHost, alice, second); err != nil {
		t.Fatalf("second open: %v", err)
	}
	held, ok := identitybroker.SessionGrant(f.b, s.ID)
	if !ok || held.SourceJTI != jti(t, second) || held.RefreshToken == first.RefreshToken {
		t.Fatal("the session does not hold the second token's refresh token")
	}
	// AS-1 grants the requested expires_at, the first grant's expiry, and
	// the broker checks its echo against it.
	if !held.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("second grant expires %v, want the session's hard expiry %v", held.ExpiresAt, first.ExpiresAt)
	}
	if f.as1.Faults.Conversions() != 2 {
		t.Fatalf("AS-1 converted %d tokens, want 2", f.as1.Faults.Conversions())
	}
}

// TestSessionOpenFailedReplaceKeepsTheHeldToken: a replacing open that fails
// leaves the session's refresh token as it was.
func TestSessionOpenFailedReplaceKeepsTheHeldToken(t *testing.T) {
	for name, fault := range map[string]as1servertest.Fault{
		"503":        {Status: 503},
		"token_used": {Error: "invalid_request", Reason: "token_used"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newOpenFixture(t)
			alice := f.owner(identityharness.Alice)
			first := f.token(t, identityharness.Alice)
			s, err := f.open(openHost, alice, first)
			if err != nil {
				t.Fatal(err)
			}
			f.as1.Faults.FailNextExchange(fault)
			if _, err := f.open(openHost, alice, f.token(t, identityharness.Alice)); err == nil {
				t.Fatal("the replacing open succeeded")
			}
			held, ok := identitybroker.SessionGrant(f.b, s.ID)
			if found, _ := f.find(t, openHost); !ok || held.SourceJTI != jti(t, first) || !found.HoldsRefreshToken {
				t.Fatal("a failed replacing open changed the held refresh token")
			}
		})
	}
}

// TestSessionHardExpiryCountsOneSkewEarly: the broker counts the hard expiry
// as reached one clock skew before the held refresh token's expiry, so it
// never presents a grant AS-1 may already have expired.
func TestSessionHardExpiryCountsOneSkewEarly(t *testing.T) {
	f := newOpenFixture(t)
	old, err := f.open(openHost, f.owner(identityharness.Alice), f.token(t, identityharness.Alice))
	if err != nil {
		t.Fatal(err)
	}
	held, ok := identitybroker.SessionGrant(f.b, old.ID)
	if !ok {
		t.Fatal("the session holds no refresh token")
	}

	f.clock.Set(held.ExpiresAt.Add(-identitybroker.ClockSkew - time.Second))
	if s, ok := f.find(t, openHost); !ok || s.ID != old.ID || !s.HoldsRefreshToken {
		t.Fatalf("one second before the skew margin: FindSession = %+v, %v; want the session, holding its refresh token", s, ok)
	}

	f.clock.Set(held.ExpiresAt.Add(-identitybroker.ClockSkew))
	if s, ok := f.find(t, openHost); ok {
		t.Fatalf("at the skew margin, before the token's own expiry: FindSession = %+v, want none", s)
	}
	if _, ok := identitybroker.SessionGrant(f.b, old.ID); ok {
		t.Fatal("the expired session still holds a refresh token")
	}
}

// TestSessionHardExpiryClosesTheSession: once the held refresh token's
// expiry passes, every lookup finds the session closed, and the next open
// pairs a new broker session. AS-1 stamps expiries with wall time, so the
// broker's clock moves past the expiry for the lookups, none of which
// reaches AS-1, and back to wall time for the token-bearing open. A
// token-bearing open as the first call stays on the moved clock instead.
func TestSessionHardExpiryClosesTheSession(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		// converts reports that lookup is itself the token-bearing open, which
		// converts at AS-1, so no reopen follows it.
		converts bool
		// lookup is the first call after the expiry.
		lookup func(t *testing.T, f *openFixture, old identitybroker.Session, cred identitybroker.Token)
	}{
		{"a tokenless open", false, func(t *testing.T, f *openFixture, old identitybroker.Session, _ identitybroker.Token) {
			next, err := f.open(openHost, old.Owner, identitybroker.Token{})
			if err != nil || next.ID == old.ID || next.HoldsRefreshToken {
				t.Fatalf("open = %+v, %v; want a new paired session", next, err)
			}
		}},
		{"a token-bearing open", true, func(t *testing.T, f *openFixture, old identitybroker.Session, _ identitybroker.Token) {
			// AS-1's own expiry is on wall time, which the broker's clock has
			// passed, so the echo expires an hour after the broker's now.
			f.as1.Faults.ReplaceNextExchangeEcho(idjagEcho(f.clock.Now().Add(time.Hour)))
			conversions := f.as1.Faults.Conversions()
			next, err := f.open(openHost, old.Owner, f.token(t, identityharness.Alice))
			if err != nil || next.ID == old.ID || !next.HoldsRefreshToken {
				t.Fatalf("token-bearing open = %+v, %v; want a new session holding a refresh token", next, err)
			}
			if got := f.as1.Faults.Conversions(); got != conversions+1 {
				t.Fatalf("AS-1 converted %d tokens for the open, want 1", got-conversions)
			}
		}},
		{"FindSession", false, func(t *testing.T, f *openFixture, _ identitybroker.Session, _ identitybroker.Token) {
			if s, ok := f.find(t, openHost); ok {
				t.Fatalf("FindSession = %+v, want none", s)
			}
		}},
		{"RegisterAgent", false, func(t *testing.T, f *openFixture, old identitybroker.Session, _ identitybroker.Token) {
			_, err := f.b.RegisterAgent(ctx, testWorkload, mainAgent(old))
			wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed)
		}},
		{"AuthorizeCall", false, func(t *testing.T, f *openFixture, old identitybroker.Session, cred identitybroker.Token) {
			_, err := f.b.AuthorizeCall(ctx, testWorkload, identitybroker.AuthorizeCallRequest{
				SessionID: old.ID, Credential: cred, OperationID: "op-1", Target: "gateway", Tool: "search",
			})
			wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed, cred)
		}},
		{"VerifyCredential", false, func(t *testing.T, f *openFixture, old identitybroker.Session, cred identitybroker.Token) {
			_, err := f.b.VerifyCredential(ctx, identitybroker.VerifyCredentialRequest{SessionID: old.ID, Credential: cred})
			wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed, cred)
		}},
		{"VerifyCredentialForOwner", func(t *testing.T, f *openFixture, old identitybroker.Session, cred identitybroker.Token) {
			_, err := f.b.VerifyCredentialForOwner(ctx, identitybroker.VerifyCredentialForOwnerRequest{Owner: old.Owner, Credential: cred})
			wantError(t, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied, cred)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // each waits up to a second for AS-1's wall clock
			f := newOpenFixture(t)
			old, err := f.open(openHost, f.owner(identityharness.Alice), f.token(t, identityharness.Alice))
			if err != nil {
				t.Fatal(err)
			}
			oldGrant, _ := identitybroker.SessionGrant(f.b, old.ID)
			// Register just before the expiry, so the credential outlives it.
			f.clock.Set(oldGrant.ExpiresAt.Add(-identitybroker.ClockSkew - 2*time.Minute))
			reg, err := f.b.RegisterAgent(ctx, testWorkload, mainAgent(old))
			if err != nil {
				t.Fatalf("RegisterAgent before the expiry: %v", err)
			}
			f.expire(t, old.ID)
			before := f.called()

			tt.lookup(t, f, old, reg.Credential)

			if _, ok := identitybroker.SessionGrant(f.b, old.ID); ok {
				t.Fatal("the expired session still holds a refresh token")
			}
			_, err = f.b.RegisterAgent(ctx, testWorkload, mainAgent(old))
			wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed)
			if tt.converts {
				return
			}
			if got := f.called(); got != before {
				t.Fatalf("AS-1 was called after the expiry: %+v, before %+v", got, before)
			}

			// Reopen on wall time, once AS-1's maximum ends after the old grant.
			f.clock.Reset()
			waitPastMaximum(oldGrant.ExpiresAt)
			next, err := f.open(openHost, old.Owner, f.token(t, identityharness.Alice))
			if err != nil || next.ID == old.ID || !next.HoldsRefreshToken {
				t.Fatalf("token-bearing open = %+v, %v; want a new session holding a refresh token", next, err)
			}
			// The new session has no hard expiry yet: it asks for none, and AS-1
			// applies its maximum, beyond the closed session's.
			if held, _ := identitybroker.SessionGrant(f.b, next.ID); !held.ExpiresAt.After(oldGrant.ExpiresAt) {
				t.Fatalf("new grant expires %v, want after the closed session's %v", held.ExpiresAt, oldGrant.ExpiresAt)
			}
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
	f.clock.Set(time.Now().Add(10 * as1servertest.IDJAGMaxLifetime))
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

// idjagEcho is an echoed exchange grant issuing only ID-JAGs, expiring at
// expiresAt (left out when zero).
func idjagEcho(expiresAt time.Time) as1servertest.ExchangeGrant {
	g := as1servertest.ExchangeGrant{Type: identitybroker.ExchangeGrantType, IssuableTokenTypes: []string{identitybroker.TokenTypeIDJAG}}
	if !expiresAt.IsZero() {
		g.ExpiresAt = expiresAt.Unix()
	}
	return g
}

// TestSessionOpenEchoMismatchClosesTheSession: AS-1 converts the token, and
// the fault transport alters or removes the grant it echoes.
func TestSessionOpenEchoMismatchClosesTheSession(t *testing.T) {
	tests := []struct {
		name string
		// held converts a token first, so the open requests an expiry.
		held bool
		// echo replaces AS-1's echo; none removes it. requested is the
		// session's hard expiry, zero unless held.
		echo func(now, requested time.Time) []as1servertest.ExchangeGrant
	}{
		{name: "refresh-token output", echo: func(now, _ time.Time) []as1servertest.ExchangeGrant {
			g := idjagEcho(now.Add(time.Hour))
			g.IssuableTokenTypes = []string{identitybroker.TokenTypeRefreshToken}
			return []as1servertest.ExchangeGrant{g}
		}},
		{name: "another type", echo: func(now, _ time.Time) []as1servertest.ExchangeGrant {
			g := idjagEcho(now.Add(time.Hour))
			g.Type = "https://example.com/other"
			return []as1servertest.ExchangeGrant{g}
		}},
		{name: "no expires_at", echo: func(time.Time, time.Time) []as1servertest.ExchangeGrant {
			return []as1servertest.ExchangeGrant{idjagEcho(time.Time{})}
		}},
		{name: "expires_at already passed", echo: func(now, _ time.Time) []as1servertest.ExchangeGrant {
			return []as1servertest.ExchangeGrant{idjagEcho(now)}
		}},
		{name: "expires_at later than requested", held: true, echo: func(_, requested time.Time) []as1servertest.ExchangeGrant {
			return []as1servertest.ExchangeGrant{idjagEcho(requested.Add(time.Hour))}
		}},
		{name: "two grants", echo: func(now, _ time.Time) []as1servertest.ExchangeGrant {
			return []as1servertest.ExchangeGrant{idjagEcho(now.Add(time.Hour)), idjagEcho(now.Add(time.Hour))}
		}},
		{name: "no echo", echo: func(time.Time, time.Time) []as1servertest.ExchangeGrant { return nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newOpenFixture(t)
			alice := f.owner(identityharness.Alice)
			if tt.held {
				if _, err := f.open(openHost, alice, f.token(t, identityharness.Alice)); err != nil {
					t.Fatal(err)
				}
			}
			s, err := f.open(openHost, alice, identitybroker.Token{})
			if err != nil {
				t.Fatal(err)
			}
			requested, _ := identitybroker.SessionGrant(f.b, s.ID)
			f.as1.Faults.ReplaceNextExchangeEcho(tt.echo(f.clock.Now(), requested.ExpiresAt)...)
			token := f.token(t, identityharness.Alice)
			conversions := f.as1.Faults.Conversions()
			_, err = f.open(openHost, alice, token)
			wantError(t, err, identitybroker.CodeInternal, "", token)
			if f.as1.Faults.Conversions() != conversions+1 {
				t.Fatal("AS-1 did not convert the token whose echo was altered")
			}
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
		fault  func(*as1servertest.FaultTransport)
		code   identitybroker.Code
		reason identitybroker.Reason
		// attempts is how many requests the broker sends AS-1.
		attempts int
		holds    bool
	}{
		{name: "processed, response lost: token_used", fault: (*as1servertest.FaultTransport).LoseNextExchangeResponse,
			code: identitybroker.CodeUnauthenticated, reason: identitybroker.ReasonTokenUsed, attempts: 2},
		{name: "dropped before processing", fault: (*as1servertest.FaultTransport).DropNextExchange, attempts: 2, holds: true},
		{name: "502", fault: func(a *as1servertest.FaultTransport) { a.FailNextExchange(as1servertest.Fault{Status: 502}) }, attempts: 2, holds: true},
		{name: "504", fault: func(a *as1servertest.FaultTransport) { a.FailNextExchange(as1servertest.Fault{Status: 504}) }, attempts: 2, holds: true},
		{name: "two 502s", fault: func(a *as1servertest.FaultTransport) {
			a.FailNextExchange(as1servertest.Fault{Status: 502})
			a.FailNextExchange(as1servertest.Fault{Status: 502})
		}, code: identitybroker.CodeUnavailable, attempts: 2},
		{name: "two lost responses", fault: func(a *as1servertest.FaultTransport) { a.DropNextExchange(); a.DropNextExchange() },
			code: identitybroker.CodeUnavailable, attempts: 2},
		{name: "503 is not resent", fault: func(a *as1servertest.FaultTransport) {
			a.FailNextExchange(as1servertest.Fault{Status: 503})
			a.FailNextExchange(as1servertest.Fault{Status: 503})
		}, code: identitybroker.CodeUnavailable, attempts: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newOpenFixture(t)
			tt.fault(f.as1.Faults)
			token := f.token(t, identityharness.Alice)
			s, err := f.open(openHost, f.owner(identityharness.Alice), token)
			if tt.code == "" {
				if err != nil {
					t.Fatalf("OpenSession: %v", err)
				}
			} else {
				wantError(t, err, tt.code, tt.reason, token)
			}
			reqs := f.as1.Faults.Requests()
			if len(reqs) != tt.attempts {
				t.Fatalf("the broker sent AS-1 %d requests, want %d", len(reqs), tt.attempts)
			}
			if len(reqs) == 2 && reqs[0].ClientAssertion == reqs[1].ClientAssertion {
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

// TestSessionOpenMapsAS1Refusals maps every answer the fault transport gives
// in AS-1's place, without AS-1 seeing the request.
func TestSessionOpenMapsAS1Refusals(t *testing.T) {
	tests := []struct {
		name   string
		fault  as1servertest.Fault
		code   identitybroker.Code
		reason identitybroker.Reason
	}{
		{"token_used", as1servertest.Fault{Error: "invalid_request", Reason: "token_used"}, identitybroker.CodeUnauthenticated, identitybroker.ReasonTokenUsed},
		{"signed_out", as1servertest.Fault{Error: "invalid_request", Reason: "signed_out"}, identitybroker.ReasonSignedOut.Code(), identitybroker.ReasonSignedOut},
		{"upstream_revoked", as1servertest.Fault{Error: "invalid_request", Reason: "upstream_revoked"}, identitybroker.ReasonUpstreamRevoked.Code(), identitybroker.ReasonUpstreamRevoked},
		{"owner_action_required", as1servertest.Fault{Error: "invalid_request", Reason: "owner_action_required"}, identitybroker.ReasonOwnerActionRequired.Code(), identitybroker.ReasonOwnerActionRequired},
		{"subject_token_invalid", as1servertest.Fault{Error: "invalid_request", Reason: "subject_token_invalid"}, identitybroker.CodeUnauthenticated, identitybroker.ReasonSubjectInvalid},
		{"subject_token_audience", as1servertest.Fault{Error: "invalid_request", Reason: "subject_token_audience"}, identitybroker.CodeInternal, ""},
		{"subject_token_source_client", as1servertest.Fault{Error: "invalid_request", Reason: "subject_token_source_client"}, identitybroker.CodeInternal, ""},
		{"subject_token_offline_access", as1servertest.Fault{Error: "invalid_request", Reason: "subject_token_offline_access"}, identitybroker.CodeInternal, ""},
		{"grant_invalid", as1servertest.Fault{Error: "invalid_request", Reason: "grant_invalid"}, identitybroker.CodeInternal, ""},
		{"invalid_request with no reason", as1servertest.Fault{Error: "invalid_request"}, identitybroker.CodeInternal, ""},
		{"subject_invalid from AS-1", as1servertest.Fault{Error: "invalid_request", Reason: "subject_invalid"}, identitybroker.CodeInternal, ""},
		{"503", as1servertest.Fault{Status: 503}, identitybroker.CodeUnavailable, ""},
		{"429", as1servertest.Fault{Status: 429}, identitybroker.CodeUnavailable, ""},
		{"server_error", as1servertest.Fault{Status: 500, Error: "server_error"}, identitybroker.CodeUnavailable, ""},
		{"temporarily_unavailable", as1servertest.Fault{Error: "temporarily_unavailable"}, identitybroker.CodeUnavailable, ""},
		{"invalid_client", as1servertest.Fault{Status: 401, Error: "invalid_client"}, identitybroker.CodeInternal, ""},
		{"unauthorized_client", as1servertest.Fault{Error: "unauthorized_client"}, identitybroker.CodeInternal, ""},
		{"invalid_scope", as1servertest.Fault{Error: "invalid_scope"}, identitybroker.CodeInternal, ""},
		{"invalid_target", as1servertest.Fault{Error: "invalid_target"}, identitybroker.CodeInternal, ""},
		{"invalid_authorization_details", as1servertest.Fault{Error: "invalid_authorization_details", Reason: "malformed_entry"}, identitybroker.CodeInternal, ""},
		{"unknown error", as1servertest.Fault{Error: "made_up"}, identitybroker.CodeInternal, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newOpenFixture(t)
			f.as1.Faults.FailNextExchange(tt.fault)
			token := f.token(t, identityharness.Alice)
			_, err := f.open(openHost, f.owner(identityharness.Alice), token)
			wantError(t, err, tt.code, tt.reason, token)
			if reqs := f.as1.Faults.Requests(); len(reqs) != 1 || reqs[0].Fault != as1servertest.FaultAnswered || reqs[0].Reached {
				t.Fatalf("requests %+v, want one, answered in AS-1's place", reqs)
			}
			s, ok := f.find(t, openHost)
			if !ok || s.HoldsRefreshToken {
				t.Fatalf("FindSession = %+v, %v; want the session paired without a refresh token", s, ok)
			}
			wantNoVerifiedOwner(t, f.b, s.ID)
		})
	}
}

// TestSessionOpenReportsAS1sOwnRefusals runs AS-1's real checks of the
// subject token and the client assertion. A token for another audience
// carries no sub_id, so the broker refuses it before AS-1; AS-1's
// subject_token_audience is driven in package as1server, which can have AS-1
// issue for another audience.
func TestSessionOpenReportsAS1sOwnRefusals(t *testing.T) {
	studio := as1server.PublicClient{ID: "studio", RedirectURIs: []string{"http://127.0.0.1:18474/callback"}}
	otherKeys := otherKeySet(t)
	tests := []struct {
		name string
		edit func(*as1server.Config, *as1server.DelegateClient)
		// token is the access token Alice opens with.
		token func(*testing.T, *openFixture) identitybroker.Token
		// as1Code and as1Reason are AS-1's refusal; an empty as1Reason is
		// not checked.
		as1Code, as1Reason string
		code               identitybroker.Code
		reason             identitybroker.Reason
	}{
		{
			name: "a token AS-1 did not sign: subject_token_invalid is subject_invalid",
			token: func(_ *testing.T, f *openFixture) identitybroker.Token {
				return f.crafted(identityharness.AccessTokenSpec{ForeignKey: true, Issuer: f.as1.Issuer()})
			},
			as1Code: "invalid_request", as1Reason: "subject_token_invalid",
			code: identitybroker.CodeUnauthenticated, reason: identitybroker.ReasonSubjectInvalid,
		},
		{
			name: "an expired token: subject_token_invalid is subject_invalid",
			token: func(t *testing.T, f *openFixture) identitybroker.Token {
				return f.as1.ExpiredAccessToken(t, f.token(t, identityharness.Alice))
			},
			as1Code: "invalid_request", as1Reason: "subject_token_invalid",
			code: identitybroker.CodeUnauthenticated, reason: identitybroker.ReasonSubjectInvalid,
		},
		{
			name: "a token from an unlisted client: subject_token_source_client is a configuration fault",
			edit: func(c *as1server.Config, _ *as1server.DelegateClient) {
				c.PublicClients = append(c.PublicClients, studio)
			},
			token: func(t *testing.T, f *openFixture) identitybroker.Token {
				return f.as1.SignIn(t, as1servertest.SignIn{Client: studio})
			},
			as1Code: "invalid_request", as1Reason: "subject_token_source_client",
			code: identitybroker.CodeInternal,
		},
		{
			name: "a token without offline_access: subject_token_offline_access is a configuration fault",
			token: func(t *testing.T, f *openFixture) identitybroker.Token {
				return f.as1.SignIn(t, as1servertest.SignIn{Scope: "openid"})
			},
			as1Code: "invalid_request", as1Reason: "subject_token_offline_access",
			code: identitybroker.CodeInternal,
		},
		{
			name: "a failed client assertion: invalid_client is a configuration fault",
			edit: func(_ *as1server.Config, d *as1server.DelegateClient) {
				// AS-1 knows the delegate by another key than the broker's.
				d.KeySet = otherKeys
			},
			token:   func(t *testing.T, f *openFixture) identitybroker.Token { return f.token(t, identityharness.Alice) },
			as1Code: "invalid_client",
			code:    identitybroker.CodeInternal,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newOpenFixtureWith(t, tt.edit)
			token := tt.token(t, f)
			_, err := f.open(openHost, f.owner(identityharness.Alice), token)
			wantError(t, err, tt.code, tt.reason, token)
			f.wantAS1Refused(t, tt.as1Code, tt.as1Reason)
			if f.attempts() != 1 {
				t.Fatalf("the broker sent AS-1 %d requests, want 1", f.attempts())
			}
			if n := identitybroker.ConvertedTokens(f.b); n != 0 {
				t.Fatalf("%d converted tokens remembered, want 0", n)
			}
			s, ok := f.find(t, openHost)
			if !ok || s.HoldsRefreshToken {
				t.Fatalf("FindSession = %+v, %v; want the session paired without a refresh token", s, ok)
			}
			wantNoVerifiedOwner(t, f.b, s.ID)
		})
	}
	t.Run("a token converted for another session: token_used", func(t *testing.T) {
		f := newOpenFixture(t)
		token := f.token(t, identityharness.Alice)
		if _, err := f.open(openHost, f.owner(identityharness.Alice), token); err != nil {
			t.Fatal(err)
		}
		_, err := f.open("hostS43", f.owner(identityharness.Alice), token)
		wantError(t, err, identitybroker.CodeUnauthenticated, identitybroker.ReasonTokenUsed, token)
		if f.as1.Faults.Conversions() != 1 {
			t.Fatalf("AS-1 converted %d tokens, want 1", f.as1.Faults.Conversions())
		}
	})
}

// otherKeySet is the JWK Set of a fresh key that is not the broker's.
func otherKeySet(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := identitybroker.NewClientAssertionSigner(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	set, err := json.Marshal(signer.JWKS())
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestSessionCloseDiscardsTheRefreshToken(t *testing.T) {
	f := newOpenFixture(t)
	alice := f.owner(identityharness.Alice)
	s, err := f.open(openHost, alice, f.token(t, identityharness.Alice))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.b.CloseSession(context.Background(), testWorkload, identitybroker.CloseSessionRequest{SessionID: s.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatal(err)
	}
	if _, ok := identitybroker.SessionGrant(f.b, s.ID); ok {
		t.Fatal("the closed session still holds a refresh token")
	}
	next, err := f.open(openHost, alice, f.token(t, identityharness.Alice))
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

// TestSessionOpenClosesOnAnAnswerItCannotUse covers unusable answers from a
// fake AuthServer, including ones AS-1 and the fault transport cannot be made
// to give: the session closes, and the refresh token, if any, is neither kept
// nor disclosed.
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
		a, b := f.token(t, identityharness.Alice), f.token(t, identityharness.Alice)
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
		a := f.token(t, identityharness.Alice)
		first, err := f.open(openHost, alice, a)
		if err != nil {
			t.Fatal(err)
		}
		held := f.expire(t, first.ID)
		// A outlives the first session's grant.
		if aExp := time.Unix(readClaims(t, a).Exp, 0); !f.clock.Now().Before(aExp.Add(5 * time.Minute)) {
			t.Fatalf("A expires at %v, which the broker forgets before the grant expiring at %v", aExp, held.ExpiresAt)
		}
		if s, ok := f.find(t, openHost); ok {
			t.Fatalf("FindSession after the expiry = %+v, want none", s)
		}
		f.clock.Reset()
		second, err := f.open(openHost, alice, f.token(t, identityharness.Alice))
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
		other := idjagEcho(time.Time{})
		other.Type = "https://example.com/other"
		f.as1.Faults.ReplaceNextExchangeEcho(other)
		a := f.token(t, identityharness.Alice)
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
		a := f.token(t, identityharness.Alice)
		if _, err := f.open(openHost, alice, a); err != nil {
			t.Fatal(err)
		}
		if n := identitybroker.ConvertedTokens(f.b); n != 1 {
			t.Fatalf("%d converted tokens remembered, want 1", n)
		}
		f.clock.Set(time.Unix(readClaims(t, a).Exp, 0).Add(5 * time.Minute))
		if n := identitybroker.ConvertedTokens(f.b); n != 0 {
			t.Fatalf("%d converted tokens remembered past exp plus the skew, want 0", n)
		}
		// AS-1 is asked again. AS-1's clock is wall time, on which A has not
		// expired, so AS-1 refuses it as spent rather than as expired.
		_, err := f.open(openHost, alice, a)
		wantError(t, err, identitybroker.CodeUnauthenticated, identitybroker.ReasonTokenUsed, a)
		f.wantAS1Refused(t, "invalid_request", "token_used")
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
		fault func(*as1servertest.FaultTransport)
		want  int
	}{
		{"converted", func(*as1servertest.FaultTransport) {}, 1},
		{"token_used", func(a *as1servertest.FaultTransport) {
			a.FailNextExchange(as1servertest.Fault{Error: "invalid_request", Reason: "token_used"})
		}, 1},
		{"lost response", (*as1servertest.FaultTransport).LoseNextExchangeResponse, 1},
		{"two lost responses", func(a *as1servertest.FaultTransport) { a.DropNextExchange(); a.DropNextExchange() }, 1},
		{"503", func(a *as1servertest.FaultTransport) { a.FailNextExchange(as1servertest.Fault{Status: 503}) }, 0},
		{"invalid_client", func(a *as1servertest.FaultTransport) {
			a.FailNextExchange(as1servertest.Fault{Status: 401, Error: "invalid_client"})
		}, 0},
		{"the token refused", func(a *as1servertest.FaultTransport) {
			a.FailNextExchange(as1servertest.Fault{Error: "invalid_request", Reason: "subject_token_invalid"})
		}, 0},
		{"invalid_request with no reason", func(a *as1servertest.FaultTransport) {
			a.FailNextExchange(as1servertest.Fault{Error: "invalid_request"})
		}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newOpenFixture(t)
			tt.fault(f.as1.Faults)
			_, _ = f.open(openHost, f.owner(identityharness.Alice), f.token(t, identityharness.Alice))
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
