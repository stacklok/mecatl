package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server/as1servertest"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// TestIdentityBrokerAgainstRealAS1 starts mecak8s with the identity broker on
// and the real AS-1, built by the test with the harness IdP as its upstream,
// as both the issuer of API access tokens and the authorization server
// session open converts them at. Once a broker session's hard expiry passes,
// the session is closed, and a new sign-in opens a new one. AS-1 stamps a grant's expiry with wall time, so
// the test moves only the broker's clock past it, observes the close through
// calls that do not reach AS-1, and moves the clock back before a new sign-in
// reopens the session.
func TestIdentityBrokerAgainstRealAS1(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	a := as1servertest.Start(t, h, nil)
	// The broker's clock is wall time plus an offset. Its client assertions
	// stay on wall time, which AS-1 checks them against.
	var clockOffset atomic.Int64
	now := func() time.Time { return time.Now().Add(time.Duration(clockOffset.Load())) }
	api, built := serveIdentityBroker(t, identityBrokerAS1Tokens{
		issuer: a.Issuer(), audience: as1servertest.Audience, caFile: h.WriteCACert(t, t.TempDir()),
		clientIDs: as1servertest.PublicClientID,
	}, &app.IdentityBrokerAS1{
		Issuer: a.Issuer(), TokenEndpoint: a.Server.TokenEndpoint(), HTTPClient: a.HTTPClient(), ClientKey: a.Key,
		Now: now,
	})

	findBrokerSession := func(id string) (identitybroker.Session, bool) {
		t.Helper()
		s, ok, err := built.IdentityBroker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: id})
		if err != nil {
			t.Fatalf("FindSession(%s): %v", id, err)
		}
		return s, ok
	}
	createSession := func(who, token string) string {
		t.Helper()
		status, body := apiCall(t, http.MethodPost, api+"/v1/sessions", token, "{}")
		var created struct {
			ID string `json:"session_id"`
		}
		if status != http.StatusCreated && status != http.StatusOK || json.Unmarshal([]byte(body), &created) != nil || created.ID == "" {
			t.Fatalf("%s creates a session: status %d: %s", who, status, body)
		}
		return created.ID
	}

	// Creating each session converts the user's sign-in at AS-1, so its broker
	// session, owned by their company identity, holds a refresh token.
	aliceCompany := identitybroker.Owner{Issuer: h.IdP.Issuer(), Subject: identityharness.Alice}
	bobCompany := identitybroker.Owner{Issuer: h.IdP.Issuer(), Subject: identityharness.Bob}
	aliceID := createSession("Alice", a.SignIn(t, as1servertest.SignIn{User: identityharness.Alice}).Value())
	expiring, ok := findBrokerSession(aliceID)
	if !ok || expiring.Owner != aliceCompany || !expiring.HoldsRefreshToken || a.Faults.Conversions() != 1 {
		t.Fatalf("Alice's broker session = %+v, %v after %d conversions; want hers holding the refresh token of AS-1's one conversion", expiring, ok, a.Faults.Conversions())
	}
	bobID := createSession("Bob", a.SignIn(t, as1servertest.SignIn{User: identityharness.Bob}).Value())
	bobsExpiring, ok := findBrokerSession(bobID)
	if !ok || bobsExpiring.Owner != bobCompany || !bobsExpiring.HoldsRefreshToken || a.Faults.Conversions() != 2 {
		t.Fatalf("Bob's broker session = %+v, %v after %d conversions; want his holding a refresh token", bobsExpiring, ok, a.Faults.Conversions())
	}
	registerMain := func(brokerSession string) (identitybroker.Registration, error) {
		return built.IdentityBroker.RegisterAgent(t.Context(), identitybroker.RegisterAgentRequest{
			SessionID: brokerSession, HostSessionID: aliceID, Owner: aliceCompany, Incarnation: expiring.Incarnation,
			Definition: identitybroker.DefinitionRef{Source: "system", Name: "main"},
		})
	}
	before, err := registerMain(expiring.ID)
	if err != nil {
		t.Fatalf("register Alice's main agent: %v", err)
	}

	// Hard expiry: AS-1 granted at most its configured maximum, so moving the
	// broker past it closes both sessions. (The broker's SessionGrant is not
	// reachable outside its package.)
	requests := len(a.Faults.Requests())
	clockOffset.Store(int64(as1servertest.IDJAGMaxLifetime + time.Minute))
	closed := identitybroker.ReasonError(identitybroker.ReasonSessionClosed, "")
	if _, err := registerMain(expiring.ID); !errors.Is(err, closed) {
		t.Errorf("register in the expired broker session = %v, want %v", err, closed)
	}
	if _, err := built.IdentityBroker.AuthorizeCall(t.Context(), identitybroker.AuthorizeCallRequest{
		SessionID: expiring.ID, Credential: before.Credential, OperationID: "op-1", Target: "gateway", Tool: "search",
	}); !errors.Is(err, closed) {
		t.Errorf("authorize in the expired broker session = %v, want %v", err, closed)
	}
	if s, ok := findBrokerSession(aliceID); ok {
		t.Errorf("Alice's expired broker session is still found: %+v", s)
	}
	if s, ok := findBrokerSession(bobID); ok {
		t.Errorf("Bob's expired broker session is still found: %+v", s)
	}
	if n := len(a.Faults.Requests()); n != requests {
		t.Fatalf("observing the close sent AS-1 %d requests, want none", n-requests)
	}
	// Back on wall time, which AS-1's expiries use, a new sign-in reopens.
	clockOffset.Store(0)

	fresh := a.SignIn(t, as1servertest.SignIn{User: identityharness.Alice}).Value()
	if status, body := apiCall(t, http.MethodPost, api+"/v1/sessions/"+aliceID+"/prompt", fresh, `{"text":"after expiry"}`); status != http.StatusOK {
		t.Fatalf("Alice prompts her session after its broker session expired: status %d: %s", status, body)
	}
	reopened, ok := findBrokerSession(aliceID)
	if !ok || reopened.ID == expiring.ID || reopened.Owner != aliceCompany || !reopened.HoldsRefreshToken || a.Faults.Conversions() != 3 {
		t.Fatalf("Alice's broker session after expiry = %+v, %v after %d conversions; want a new one holding the new sign-in's refresh token", reopened, ok, a.Faults.Conversions())
	}
	// The 200 means the prompt's run entry registered the main agent in the
	// new broker session; registering again here returns its agent.
	if after, err := registerMain(reopened.ID); err != nil || after.Agent != before.Agent {
		t.Errorf("register in the new broker session = %+v, %v; want agent %+v", after.Agent, err, before.Agent)
	}
	// A tokenless open after the expiry pairs a new session that holds no
	// refresh token.
	bobsReopened, err := built.IdentityBroker.OpenSession(t.Context(), identitybroker.OpenSessionRequest{
		HostSessionID: bobID, Incarnation: bobsExpiring.Incarnation, Owner: bobCompany,
	})
	if err != nil || bobsReopened.ID == bobsExpiring.ID || bobsReopened.HoldsRefreshToken {
		t.Errorf("Bob's tokenless open after expiry = %+v, %v; want a new broker session holding no refresh token", bobsReopened, err)
	}
}
