package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

// switchableAS1 converts access tokens, or refuses them with token_used.
type switchableAS1 struct {
	mu      sync.Mutex
	refuse  bool
	minted  int
	refused int
}

func (a *switchableAS1) set(refuse bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refuse = refuse
}

func (a *switchableAS1) refusals() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.refused
}

func (a *switchableAS1) Exchange(context.Context, identitybroker.TokenExchange) (identitybroker.TokenExchangeResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.refuse {
		a.refused++
		return identitybroker.TokenExchangeResult{}, identitybroker.NewServerRefusal(http.StatusBadRequest, "invalid_request", "token_used")
	}
	a.minted++
	return identitybroker.TokenExchangeResult{
		RefreshToken:    identitybroker.NewToken("refresh"),
		IssuedTokenType: identitybroker.TokenTypeRefreshToken,
		Grants: []identitybroker.ExchangeGrant{{
			Type: identitybroker.ExchangeGrantType, IssuableTokenTypes: []string{identitybroker.TokenTypeIDJAG}, ExpiresAt: time.Now().Add(time.Hour),
		}},
	}, nil
}

// presentingBroker wraps a broker and counts the opens that presented an
// access token.
type presentingBroker struct {
	identitybroker.Service
	presented atomic.Int32
}

func (b *presentingBroker) OpenSession(ctx context.Context, req identitybroker.OpenSessionRequest) (identitybroker.Session, error) {
	if !req.AccessToken.IsZero() {
		b.presented.Add(1)
	}
	return b.Service.OpenSession(ctx, req)
}

func convertingBroker(t *testing.T, as1 identitybroker.AuthServer) *presentingBroker {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	core, err := identitybroker.New(identitybroker.Config{
		TrustDomain: "agents.example.com", Audience: "https://broker.example.com", CredentialLifetime: 5 * time.Minute,
	}, nil, key, identitybroker.WithAuthServer(as1))
	if err != nil {
		t.Fatal(err)
	}
	return &presentingBroker{Service: identitybroker.NewInProcess(identitybroker.UnverifiedAuthenticator{Name: "mecak8s-test"}, core)}
}

// requestWithAccessToken is a user request's context carrying an AS-1-shaped
// access token for owner, as the authenticator attaches one.
func requestWithAccessToken(t *testing.T, owner *session.Principal, jti string) context.Context {
	t.Helper()
	enc := base64.RawURLEncoding
	claims, err := json.Marshal(map[string]any{
		"jti": jti, "exp": time.Now().Add(time.Hour).Unix(),
		"sub_id": map[string]string{"format": "iss_sub", "iss": owner.Issuer, "sub": owner.Subject},
	})
	if err != nil {
		t.Fatal(err)
	}
	token := enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString(claims) + ".sig"
	ctx, done := withRequestAccessToken(session.WithPrincipal(t.Context(), owner), identitybroker.NewToken(token))
	t.Cleanup(done)
	return ctx
}

var mcpAuthorizationOwner = &session.Principal{Issuer: "https://login.example.com", Subject: "alice-204", GrantType: session.GrantTypeUser}

// mcpAuthorizationBrokerFixture is the lifecycle fixture with a converting
// identity broker and an owner on its parked session.
type mcpAuthorizationBrokerFixture struct {
	lifecycleFixture
	as1     *switchableAS1
	broker  *presentingBroker
	control MCPAuthorizationControl
}

func newMCPAuthorizationBrokerFixture(t *testing.T, status session.AuthorizationStatus, now func() time.Time) mcpAuthorizationBrokerFixture {
	t.Helper()
	f := newLifecycleFixture(t, status, nil, now, nil)
	as1 := &switchableAS1{refuse: true}
	broker := convertingBroker(t, as1)
	f.svc.cfg.IdentityBroker = broker
	sess, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	sess.Owner = mcpAuthorizationOwner
	if err := f.store.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	return mcpAuthorizationBrokerFixture{
		lifecycleFixture: f, as1: as1, broker: broker,
		control: MCPAuthorizationControl{SessionID: "authorization-session", AuthorizationID: f.pending.Authorization.ID},
	}
}

func (f mcpAuthorizationBrokerFixture) snapshot(t *testing.T) string {
	t.Helper()
	loaded, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	out, err := sessnap.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestMCPAuthorizationRefusedTokenChangesNothing proves the MCP authorization
// controls open the identity broker session before they settle anything: a
// refused token leaves the authorization pending, the session unsaved, and
// the MCP broker uncalled. With an accepted token, each request presents its
// token once, though the control opens the broker session twice.
func TestMCPAuthorizationRefusedTokenChangesNothing(t *testing.T) {
	type control func(*Service, context.Context, session.SessionID, MCPAuthorizationControl) (MCPAuthorizationResult, error)
	for _, tc := range []struct {
		name   string
		status session.AuthorizationStatus
		// expired makes the pending authorization's expiry pass before the
		// control, so the recheck cancels it.
		expired bool
		act     control
	}{
		{name: "recheck granted", status: session.AuthorizationGranted, act: (*Service).RecheckMCPAuthorization},
		{name: "recheck denied", status: session.AuthorizationDenied, act: (*Service).RecheckMCPAuthorization},
		{name: "recheck expired", status: session.AuthorizationPending, expired: true, act: (*Service).RecheckMCPAuthorization},
		{name: "cancel", status: session.AuthorizationPending, act: (*Service).CancelMCPAuthorization},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// An expired authorization is parked two hours ago, so its hour
			// has passed by the time the control runs at the real time, which
			// the broker's credentials are issued against.
			var offset atomic.Int64
			if tc.expired {
				offset.Store(int64(-2 * time.Hour))
			}
			now := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
			f := newMCPAuthorizationBrokerFixture(t, tc.status, now)
			offset.Store(0)
			before := f.snapshot(t)

			_, err := tc.act(f.svc, requestWithAccessToken(t, mcpAuthorizationOwner, "jti-1"), f.control.SessionID, f.control)
			if !errors.Is(err, ErrBearerRejected) {
				t.Fatalf("with a refused token: %v, want ErrBearerRejected", err)
			}
			if n := f.as1.refusals(); n != 1 {
				t.Fatalf("AS-1 refusals = %d, want 1", n)
			}
			if after := f.snapshot(t); after != before {
				t.Fatalf("a refused token changed the session:\nbefore %s\nafter  %s", before, after)
			}
			f.attach.mu.Lock()
			status, refreshes := f.attach.status, f.attach.refreshCalls
			f.attach.mu.Unlock()
			if status != tc.status || refreshes != 0 {
				t.Fatalf("MCP broker after a refused token: status %s, refreshes %d; want %s and none", status, refreshes, tc.status)
			}

			// Control: an accepted token settles the authorization, presented
			// once however many opens the control makes.
			f.as1.set(false)
			presented := f.broker.presented.Load()
			result, err := tc.act(f.svc, requestWithAccessToken(t, mcpAuthorizationOwner, "jti-2"), f.control.SessionID, f.control)
			if err != nil {
				t.Fatalf("with an accepted token: %v", err)
			}
			drainLifecycleRun(t, f.svc, result)
			if n := f.broker.presented.Load() - presented; n != 1 {
				t.Fatalf("the accepted control presented its token %d times, want once", n)
			}
			if f.snapshot(t) == before {
				t.Fatal("an accepted token settled nothing")
			}
		})
	}
}

// TestMCPAuthorizationPendingPollOpensNothing proves a recheck that finds the
// authorization still pending does not open the identity broker session, so
// even a token AS-1 would refuse is neither presented nor answered with a 401.
func TestMCPAuthorizationPendingPollOpensNothing(t *testing.T) {
	f := newMCPAuthorizationBrokerFixture(t, session.AuthorizationPending, time.Now)
	before := f.snapshot(t)
	result, err := f.svc.RecheckMCPAuthorization(requestWithAccessToken(t, mcpAuthorizationOwner, "jti-1"), f.control.SessionID, f.control)
	if err != nil || result.Status != session.AuthorizationPending {
		t.Fatalf("pending poll = %+v, %v; want pending, no error", result, err)
	}
	if n := f.broker.presented.Load(); n != 0 || f.as1.refusals() != 0 {
		t.Fatalf("a pending poll presented its token %d times (%d refusals)", n, f.as1.refusals())
	}
	if after := f.snapshot(t); after != before {
		t.Fatal("a pending poll changed the session")
	}
}

// TestMCPAuthorizationConcurrentBrokerCloseIsNotABearerRejection proves that
// when the broker session closes after a granted recheck's first open, the
// continuation, which runs after the claim is saved, opens a new broker
// session without the spent token and does not answer bearer_rejected.
func TestMCPAuthorizationConcurrentBrokerCloseIsNotABearerRejection(t *testing.T) {
	f := newMCPAuthorizationBrokerFixture(t, session.AuthorizationGranted, time.Now)
	f.as1.set(false)
	var closeErr error
	f.attach.refreshHook = func() {
		// Runs once the claim is saved: the broker session closes, as a
		// concurrent delete or restart would close it.
		open, found, err := f.broker.FindSession(context.Background(), identitybroker.FindSessionRequest{RootHostSessionID: "authorization-session"})
		if err != nil || !found {
			closeErr = errors.Join(err, errors.New("no broker session to close"))
			return
		}
		closeErr = f.broker.CloseSession(context.Background(), identitybroker.CloseSessionRequest{SessionID: open.ID, Reason: identitybroker.ReasonSessionClosed})
	}
	result, err := f.svc.RecheckMCPAuthorization(requestWithAccessToken(t, mcpAuthorizationOwner, "jti-1"), f.control.SessionID, f.control)
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if errors.Is(err, ErrBearerRejected) {
		t.Fatalf("recheck after its writes = %v; the tagged 401 means nothing happened", err)
	}
	if err != nil {
		t.Fatalf("recheck: %v", err)
	}
	drainLifecycleRun(t, f.svc, result)
	if n := f.broker.presented.Load(); n != 1 {
		t.Fatalf("the recheck presented its token %d times, want once", n)
	}
}

// TestIdentityBrokerSessionRefusalChallengeKeysOnTheSentinel proves only the
// identity broker's token refusals carry the invalid_token challenge: other
// 401s keep answering without one.
func TestIdentityBrokerSessionRefusalChallengeKeysOnTheSentinel(t *testing.T) {
	for _, tc := range []struct {
		err       error
		challenge string
	}{
		{ErrBearerRejected, `Bearer error="invalid_token"`},
		{ErrSignInRequired, `Bearer error="invalid_token"`},
		{errExecutionUnauthenticated, ""},
		{errConnectorUnauthenticated, ""},
	} {
		rec := httptest.NewRecorder()
		writeServiceError(rec, tc.err)
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != tc.challenge {
			t.Errorf("%v: %d, challenge %q; want 401, %q", tc.err, rec.Code, rec.Header().Get("WWW-Authenticate"), tc.challenge)
		}
	}
}

// TestMCPAuthorizationFirstOpenTakesAnUnpresentedToken proves a granted
// recheck whose first open finds the session holding a refresh token takes
// the request's token anyway: when the broker session closes after the claim
// is saved, the continuation's open presents nothing, so a token an earlier
// request converted is not presented again after the write.
func TestMCPAuthorizationFirstOpenTakesAnUnpresentedToken(t *testing.T) {
	f := newMCPAuthorizationBrokerFixture(t, session.AuthorizationGranted, time.Now)
	f.as1.set(false)
	sess, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	// An earlier request converted the token with jti-spent for this session.
	earlier := requestWithAccessToken(t, mcpAuthorizationOwner, "jti-spent")
	if _, err := f.broker.OpenSession(t.Context(), identitybroker.OpenSessionRequest{
		HostSessionID: string(sess.ID), Incarnation: string(sess.Incarnation()),
		Owner:       identitybroker.Owner{Issuer: mcpAuthorizationOwner.Issuer, Subject: mcpAuthorizationOwner.Subject},
		AccessToken: accessTokenFromRequest(earlier),
	}); err != nil {
		t.Fatal(err)
	}
	presented := f.broker.presented.Load()
	f.attach.refreshHook = func() {
		open, found, err := f.broker.FindSession(context.Background(), identitybroker.FindSessionRequest{RootHostSessionID: string(sess.ID)})
		if err == nil && found {
			_ = f.broker.CloseSession(context.Background(), identitybroker.CloseSessionRequest{SessionID: open.ID, Reason: identitybroker.ReasonSessionClosed})
		}
	}
	result, err := f.svc.RecheckMCPAuthorization(requestWithAccessToken(t, mcpAuthorizationOwner, "jti-spent"), f.control.SessionID, f.control)
	if errors.Is(err, ErrBearerRejected) {
		t.Fatalf("recheck = %v: a tagged 401 after the claim was saved", err)
	}
	if err != nil {
		t.Fatalf("recheck: %v", err)
	}
	drainLifecycleRun(t, f.svc, result)
	if n := f.broker.presented.Load() - presented; n != 0 {
		t.Fatalf("the recheck presented its token %d times, want none", n)
	}
}
