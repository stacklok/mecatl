package conformance

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// runOwnerCredentials checks the CredentialVerifier's owner-keyed form, the
// check AS-1's actor-token validator makes before issuing an ID-JAG.
func runOwnerCredentials(t *testing.T, factory Factory) {
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
	verify := func(core identitybroker.CredentialVerifier, owner identitybroker.Owner, credential identitybroker.Token) (identitybroker.VerifiedAgent, error) {
		return core.VerifyCredentialForOwner(ctx, identitybroker.VerifyCredentialForOwnerRequest{Owner: owner, Credential: credential})
	}
	wantRefused := func(t *testing.T, call string, got identitybroker.VerifiedAgent, err error, code identitybroker.Code, reason identitybroker.Reason) {
		t.Helper()
		wantError(t, call, err, code, reason)
		if got != (identitybroker.VerifiedAgent{}) {
			t.Fatalf("%s: a refused credential returned %+v", call, got)
		}
	}
	wantDenied := func(t *testing.T, call string, got identitybroker.VerifiedAgent, err error) {
		t.Helper()
		wantRefused(t, call, got, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied)
	}

	t.Run("an issued credential verifies for its session's owner", func(t *testing.T) {
		f := setup(t)
		got, err := verify(f.core, alice, f.reg.Credential)
		if err != nil {
			t.Fatalf("VerifyCredentialForOwner: %v", err)
		}
		if got.Agent != f.reg.Agent || !got.ExpiresAt.Equal(f.reg.ExpiresAt) {
			t.Fatalf("VerifyCredentialForOwner = %+v, want agent %+v expiring at %v", got, f.reg.Agent, f.reg.ExpiresAt)
		}
		if !strings.HasPrefix(got.AgentID, "ag_") || strings.Contains(got.AgentID, "/") || strings.Contains(got.AgentID, "main") {
			t.Fatalf("agent ID %q is not an opaque ag_ identifier", got.AgentID)
		}
		session, err := f.core.VerifyCredential(ctx, identitybroker.VerifyCredentialRequest{SessionID: f.session.ID, Credential: f.reg.Credential})
		if err != nil || session != got {
			t.Fatalf("the session-keyed form = %+v, %v, want %+v", session, err, got)
		}
	})

	t.Run("an agent in another session of the same owner verifies, unlike in the session-keyed form", func(t *testing.T) {
		// Which of the owner's sessions a call is for is the broker's
		// per-operation check, not AS-1's.
		f := setup(t)
		other := mustOpen(t, f.svc, openRequest("hostS43", alice))
		if _, err := verify(f.core, alice, f.reg.Credential); err != nil {
			t.Fatalf("VerifyCredentialForOwner with another session of the owner open: %v", err)
		}
		got, err := f.core.VerifyCredential(ctx, identitybroker.VerifyCredentialRequest{SessionID: other.ID, Credential: f.reg.Credential})
		wantDenied(t, "VerifyCredential in the owner's other session", got, err)
	})

	t.Run("another owner's grant is refused", func(t *testing.T) {
		f := setup(t)
		mustOpen(t, f.svc, openRequest("hostS43", bob))
		got, err := verify(f.core, bob, f.reg.Credential)
		wantDenied(t, "VerifyCredentialForOwner for another owner", got, err)
	})

	t.Run("a credential of a closed session is denied, with no closing reason", func(t *testing.T) {
		f := setup(t)
		mustClose(t, f.svc, f.session.ID)
		got, err := verify(f.core, alice, f.reg.Credential)
		wantDenied(t, "VerifyCredentialForOwner in a closed session", got, err)
	})

	t.Run("a credential from before a close is denied after the agent registers into the replacement session", func(t *testing.T) {
		// The clock does not move, so both credentials carry the same expiry.
		f := setup(t)
		mustClose(t, f.svc, f.session.ID)
		reopened := mustOpen(t, f.svc, openRequest("hostS42", alice))
		again := mustRegisterAt(t, f.svc, registerRequest(reopened), f.clock.Now)
		got, err := verify(f.core, alice, f.reg.Credential)
		wantDenied(t, "VerifyCredentialForOwner of a credential from before the close", got, err)
		renewed, err := verify(f.core, alice, again.Credential)
		if err != nil {
			t.Fatalf("VerifyCredentialForOwner of the credential issued in the replacement session: %v", err)
		}
		first, err := f.core.VerifyCredential(ctx, identitybroker.VerifyCredentialRequest{SessionID: reopened.ID, Credential: again.Credential})
		if err != nil || renewed != first {
			t.Fatalf("the two forms disagree: %+v, %+v, %v", renewed, first, err)
		}
		if renewed.Agent != f.reg.Agent {
			t.Fatalf("the agent registered again is %+v, want %+v", renewed.Agent, f.reg.Agent)
		}
	})

	t.Run("re-registering keeps the agent and its ag_ identifier", func(t *testing.T) {
		f := setup(t)
		before, err := verify(f.core, alice, f.reg.Credential)
		if err != nil {
			t.Fatalf("VerifyCredentialForOwner: %v", err)
		}
		f.clock.Advance(time.Minute)
		again := mustRegisterAt(t, f.svc, registerRequest(f.session), f.clock.Now)
		after, err := verify(f.core, alice, again.Credential)
		if err != nil {
			t.Fatalf("VerifyCredentialForOwner of the renewed credential: %v", err)
		}
		if after.Agent != before.Agent || after.AgentID != before.AgentID {
			t.Fatalf("after renewal the agent is %+v, want %+v", after, before)
		}
	})

	t.Run("an expired credential is refused with svid_expired", func(t *testing.T) {
		f := setup(t)
		f.clock.Advance(f.reg.ExpiresAt.Sub(f.clock.Now()) + time.Hour)
		got, err := verify(f.core, alice, f.reg.Credential)
		wantRefused(t, "VerifyCredentialForOwner of an expired credential", got, err, identitybroker.CodeUnauthenticated, identitybroker.ReasonSVIDExpired)
		// Expiry is checked before the agent's session, so a renewal is
		// still asked for with another owner or a closed session.
		got, err = verify(f.core, bob, f.reg.Credential)
		wantRefused(t, "VerifyCredentialForOwner of an expired credential for another owner", got, err, identitybroker.CodeUnauthenticated, identitybroker.ReasonSVIDExpired)
		mustClose(t, f.svc, f.session.ID)
		got, err = verify(f.core, alice, f.reg.Credential)
		wantRefused(t, "VerifyCredentialForOwner of an expired credential of a closed session", got, err, identitybroker.CodeUnauthenticated, identitybroker.ReasonSVIDExpired)
	})

	t.Run("a credential signed with another key is denied", func(t *testing.T) {
		f := setup(t)
		otherSvc := asMecatl(factory(t, f.clock.Now))
		otherSession := mustOpen(t, otherSvc, openRequest("hostS42", alice))
		foreign := mustRegisterAt(t, otherSvc, registerRequest(otherSession), f.clock.Now)
		got, err := verify(f.core, alice, foreign.Credential)
		wantDenied(t, "VerifyCredentialForOwner of another key's credential", got, err)
	})

	t.Run("malformed requests are denied", func(t *testing.T) {
		// AS-1 passes on what its client sent, so a malformed request is a
		// refusal, not a failure to answer.
		f := setup(t)
		for name, req := range map[string]identitybroker.VerifyCredentialForOwnerRequest{
			"malformed credential":    {Owner: alice, Credential: identitybroker.NewToken("not-a-credential")},
			"no credential":           {Owner: alice},
			"no owner":                {Credential: f.reg.Credential},
			"owner with no subject":   {Owner: identitybroker.Owner{Issuer: alice.Issuer}, Credential: f.reg.Credential},
			"owner with a space":      {Owner: identitybroker.Owner{Issuer: alice.Issuer, Subject: "alice 204"}, Credential: f.reg.Credential},
			"owner differing in case": {Owner: identitybroker.Owner{Issuer: alice.Issuer, Subject: strings.ToUpper(alice.Subject)}, Credential: f.reg.Credential},
		} {
			got, err := f.core.VerifyCredentialForOwner(ctx, req)
			wantDenied(t, "VerifyCredentialForOwner with "+name, got, err)
		}
	})
}
