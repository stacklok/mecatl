package conformance

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// clock is a test-controlled time source.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: time.Now()} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// runCredentials checks the credentials registration issues with the Core's
// VerifyCredential, the call AS-1 makes in process.
func runCredentials(t *testing.T, factory Factory) {
	ctx := context.Background()

	type fixture struct {
		clock   *clock
		core    Implementation
		svc     identitybroker.Service
		session identitybroker.Session
		reg     identitybroker.Registration
	}
	setup := func(t *testing.T) fixture {
		t.Helper()
		c := newClock()
		core := factory(t, c.Now)
		svc := asMecatl(core)
		s := mustOpen(t, svc, openRequest("hostS42", alice))
		return fixture{clock: c, core: core, svc: svc, session: s, reg: mustRegisterAt(t, svc, registerRequest(s), c.Now)}
	}
	verify := func(core identitybroker.CredentialVerifier, sessionID string, credential identitybroker.Token) (identitybroker.VerifiedAgent, error) {
		return core.VerifyCredential(ctx, identitybroker.VerifyCredentialRequest{SessionID: sessionID, Credential: credential})
	}
	wantRefused := func(t *testing.T, call string, got identitybroker.VerifiedAgent, err error, code identitybroker.Code, reason identitybroker.Reason) {
		t.Helper()
		wantError(t, call, err, code, reason)
		if got != (identitybroker.VerifiedAgent{}) {
			t.Fatalf("%s: a refused credential returned %+v", call, got)
		}
	}

	t.Run("an issued credential verifies in its session", func(t *testing.T) {
		f := setup(t)
		got, err := verify(f.core, f.session.ID, f.reg.Credential)
		if err != nil {
			t.Fatalf("VerifyCredential: %v", err)
		}
		if got.Agent != f.reg.Agent || !got.ExpiresAt.Equal(f.reg.ExpiresAt) {
			t.Fatalf("VerifyCredential = %+v, want agent %+v expiring at %v", got, f.reg.Agent, f.reg.ExpiresAt)
		}
		if !strings.HasPrefix(got.AgentID, "ag_") || strings.Contains(got.AgentID, "/") || strings.Contains(got.AgentID, "main") {
			t.Fatalf("agent ID %q is not an opaque ag_ identifier", got.AgentID)
		}
	})

	t.Run("agents share the ag_ identifier but not their instance", func(t *testing.T) {
		f := setup(t)
		other := mustOpen(t, f.svc, openRequest("hostS43", alice))
		otherReg := mustRegisterAt(t, f.svc, registerRequest(other), f.clock.Now)
		a, errA := verify(f.core, f.session.ID, f.reg.Credential)
		b, errB := verify(f.core, other.ID, otherReg.Credential)
		if errA != nil || errB != nil {
			t.Fatalf("VerifyCredential: %v, %v", errA, errB)
		}
		if a.AgentID != b.AgentID || a.Agent == b.Agent {
			t.Fatalf("agents = %+v and %+v, want one ag_ identifier and two instances", a, b)
		}
	})

	t.Run("a credential for another broker session is refused", func(t *testing.T) {
		f := setup(t)
		other := mustOpen(t, f.svc, openRequest("hostS43", alice))
		got, err := verify(f.core, other.ID, f.reg.Credential)
		wantRefused(t, "VerifyCredential in another session", got, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied)
	})

	t.Run("a credential of a closed session is refused with the closing reason", func(t *testing.T) {
		f := setup(t)
		mustClose(t, f.svc, f.session.ID)
		got, err := verify(f.core, f.session.ID, f.reg.Credential)
		wantRefused(t, "VerifyCredential in a closed session", got, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed)
	})

	t.Run("after the agent moves to a reopened session, its old credential is refused", func(t *testing.T) {
		f := setup(t)
		mustClose(t, f.svc, f.session.ID)
		reopened := mustOpen(t, f.svc, openRequest("hostS42", alice))
		again := mustRegisterAt(t, f.svc, registerRequest(reopened), f.clock.Now)
		if _, err := verify(f.core, reopened.ID, again.Credential); err != nil {
			t.Fatalf("VerifyCredential in the reopened session: %v", err)
		}
		got, err := verify(f.core, f.session.ID, f.reg.Credential)
		wantRefused(t, "VerifyCredential in the old session", got, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied)
	})

	t.Run("a reopened session does not revive credentials issued before it closed", func(t *testing.T) {
		// The clock does not move, so both credentials are issued in the same
		// second and carry the same expiry.
		f := setup(t)
		mustClose(t, f.svc, f.session.ID)
		reopened := mustOpen(t, f.svc, openRequest("hostS42", alice))
		again := mustRegisterAt(t, f.svc, registerRequest(reopened), f.clock.Now)
		got, err := verify(f.core, reopened.ID, f.reg.Credential)
		wantRefused(t, "VerifyCredential in the reopened session of a credential from before", got, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied)
		if _, err := verify(f.core, reopened.ID, again.Credential); err != nil {
			t.Fatalf("VerifyCredential of the credential issued on reopening: %v", err)
		}
	})

	t.Run("a refused takeover leaves the credential bound to its session", func(t *testing.T) {
		f := setup(t)
		// Bob's session reports Alice's agent's incarnation as its own.
		open := openRequest("hostS43", bob)
		open.Incarnation = f.session.Incarnation
		bobs := mustOpen(t, f.svc, open)
		if _, err := f.svc.RegisterAgent(ctx, registerRequest(bobs)); err == nil {
			t.Fatal("another owner's session took over the agent")
		}
		got, err := verify(f.core, bobs.ID, f.reg.Credential)
		wantRefused(t, "VerifyCredential in the other owner's session", got, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied)
		if _, err := verify(f.core, f.session.ID, f.reg.Credential); err != nil {
			t.Fatalf("VerifyCredential in its own session after a refused takeover: %v", err)
		}
	})

	t.Run("an expired credential is refused, and registering again renews it", func(t *testing.T) {
		f := setup(t)
		f.clock.Advance(f.reg.ExpiresAt.Sub(f.clock.Now()) + time.Hour)
		got, err := verify(f.core, f.session.ID, f.reg.Credential)
		wantRefused(t, "VerifyCredential of an expired credential", got, err, identitybroker.CodeUnauthenticated, identitybroker.ReasonSVIDExpired)
		renewed, err := f.svc.RegisterAgent(ctx, registerRequest(f.session))
		if err != nil {
			t.Fatalf("RegisterAgent again: %v", err)
		}
		if _, err := verify(f.core, f.session.ID, renewed.Credential); err != nil {
			t.Fatalf("VerifyCredential of the renewed credential: %v", err)
		}
	})

	t.Run("a credential signed with another key is refused", func(t *testing.T) {
		f := setup(t)
		// Another implementation has its own key; register the same agent there.
		otherSvc := asMecatl(factory(t, f.clock.Now))
		otherSession := mustOpen(t, otherSvc, openRequest("hostS42", alice))
		foreign := mustRegisterAt(t, otherSvc, registerRequest(otherSession), f.clock.Now)
		got, err := verify(f.core, f.session.ID, foreign.Credential)
		wantRefused(t, "VerifyCredential of another key's credential", got, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied)
	})

	t.Run("a malformed credential is refused", func(t *testing.T) {
		f := setup(t)
		got, err := verify(f.core, f.session.ID, identitybroker.NewToken("not-a-credential"))
		wantRefused(t, "VerifyCredential of a malformed credential", got, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied)
	})

	t.Run("invalid requests are refused", func(t *testing.T) {
		f := setup(t)
		for name, req := range map[string]identitybroker.VerifyCredentialRequest{
			"no session":    {Credential: f.reg.Credential},
			"no credential": {SessionID: f.session.ID},
		} {
			_, err := f.core.VerifyCredential(ctx, req)
			wantError(t, "VerifyCredential with "+name, err, identitybroker.CodeInvalidArgument, "")
		}
	})
}
