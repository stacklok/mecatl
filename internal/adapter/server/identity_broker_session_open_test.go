package server_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

// fakeAS1 is AS-1's token endpoint as the identity broker's session open uses
// it: it converts each access token into a refresh token, or refuses with the
// configured error.
type fakeAS1 struct {
	mu     sync.Mutex
	refuse error
	calls  int
}

func (a *fakeAS1) Exchange(_ context.Context, req identitybroker.TokenExchange) (identitybroker.TokenExchangeResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.refuse != nil {
		return identitybroker.TokenExchangeResult{}, a.refuse
	}
	expires := time.Now().Add(time.Hour)
	if !req.Grant.ExpiresAt.IsZero() && req.Grant.ExpiresAt.Before(expires) {
		expires = req.Grant.ExpiresAt
	}
	return identitybroker.TokenExchangeResult{
		RefreshToken:    identitybroker.NewToken(fmt.Sprintf("refresh-%d", a.calls)),
		IssuedTokenType: identitybroker.TokenTypeRefreshToken,
		Grants:          []identitybroker.ExchangeGrant{{Type: identitybroker.ExchangeGrantType, IssuableTokenTypes: []string{identitybroker.TokenTypeIDJAG}, ExpiresAt: expires}},
	}, nil
}

// refuseWith makes every later exchange fail with AS-1's refusal carrying
// reason; an empty reason accepts again.
func (a *fakeAS1) refuseWith(reason string) {
	a.setRefusal(nil)
	if reason != "" {
		a.setRefusal(identitybroker.NewServerRefusal(http.StatusBadRequest, "invalid_request", reason))
	}
}

func (a *fakeAS1) setRefusal(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refuse = err
}

func (a *fakeAS1) exchanges() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// accessTokenFor is an AS-1-shaped access token for owner, distinct per jti.
// The broker reads its sub_id, jti, and exp without verifying it; the test
// validator vouches for it.
func accessTokenFor(owner *session.Principal, jti string) string {
	enc := base64.RawURLEncoding
	claims, err := json.Marshal(map[string]any{
		"jti":    jti,
		"exp":    time.Now().Add(time.Hour).Unix(),
		"sub_id": map[string]string{"format": "iss_sub", "iss": owner.Issuer, "sub": owner.Subject},
	})
	if err != nil {
		panic(err)
	}
	return enc.EncodeToString([]byte(`{"alg":"none","typ":"at+jwt"}`)) + "." + enc.EncodeToString(claims) + ".sig"
}

// sessionOpenFixture is a service whose identity broker converts tokens at a
// fake AS-1, with an authenticator that admits the tokens it mints.
type sessionOpenFixture struct {
	svc    *server.Service
	store  *memstore.Store
	events *memstore.EventLog
	as1    *fakeAS1
	broker *recordingBroker
	auth   *server.Authenticator
	tokens map[string]session.Principal
	next   int
}

func newSessionOpenFixture(t *testing.T, llm *mockllm.Provider, configure ...func(*server.Config)) *sessionOpenFixture {
	t.Helper()
	f := &sessionOpenFixture{as1: &fakeAS1{}, events: memstore.NewEventLog(), tokens: map[string]session.Principal{}}
	f.broker = newRecordingBroker(identitybroker.WithAuthServer(f.as1))
	f.svc = newIdentityBrokerService(t, f.broker, llm, append([]func(*server.Config){func(cfg *server.Config) {
		f.store = cfg.Store.(*memstore.Store)
		cfg.EventLog = f.events
	}}, configure...)...)
	f.auth = server.NewAuthenticator(server.SecurityConfig{Validator: fakeValidator{ok: f.tokens}})
	return f
}

// token mints a fresh access token for Alice, admitted by f.auth.
func (f *sessionOpenFixture) token() string {
	f.next++
	tok := accessTokenFor(brokerAlice, fmt.Sprintf("jti-%d", f.next))
	f.tokens[tok] = *brokerAlice
	return tok
}

// within runs fn inside a unary request authenticated with a fresh token, and
// returns how many broker opens in it presented a token.
func (f *sessionOpenFixture) within(t *testing.T, fn func(context.Context)) int {
	t.Helper()
	before := f.broker.openCount()
	withinRequest(t, "unary", f.auth, f.token(), fn)
	presented := 0
	for _, open := range f.broker.openRequests()[before:] {
		if !open.AccessToken.IsZero() {
			presented++
		}
	}
	return presented
}

// save stores sess as Alice's, as a restart or an earlier request left it.
func (f *sessionOpenFixture) save(t *testing.T, sess *session.Session) {
	t.Helper()
	sess.Owner = brokerAlice
	if err := f.store.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
}

// state is everything a request could have changed: every stored session and
// its event log.
func (f *sessionOpenFixture) state(t *testing.T) string {
	t.Helper()
	stored, err := f.store.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, s := range stored {
		loaded, err := f.store.Load(t.Context(), s.ID)
		if err != nil {
			t.Fatal(err)
		}
		snap, err := sessnap.Marshal(loaded)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&out, "%s %s\n", s.ID, snap)
		for ev, err := range f.events.Read(t.Context(), s.ID) {
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(ev)
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&out, "  %s\n", body)
		}
	}
	return out.String()
}

func drain(run *agent.Run) {
	if run == nil {
		return
	}
	for range run.Events() {
	}
}

// TestIdentityBrokerOpenRefusalMapping proves each broker refusal at session
// open reaches the caller as the design's outcome, that a refused open
// changes nothing, and that a broker session the refused open paired is not
// left open.
func TestIdentityBrokerOpenRefusalMapping(t *testing.T) {
	for _, tc := range []struct {
		name   string
		refuse func(*fakeAS1)
		// otherSubID makes the token's sub_id another user than the caller
		// the authenticator verified: a pairing bug the broker refuses.
		otherSubID bool
		// ownedByBob runs a prompt on a session whose broker session belongs
		// to another user, instead of a create.
		ownedByBob bool
		want       error
		// as1Called is whether AS-1 sees the token; the broker's own checks
		// refuse without calling it.
		as1Called bool
	}{
		{name: "token_used", refuse: func(a *fakeAS1) { a.refuseWith("token_used") }, want: server.ErrBearerRejected, as1Called: true},
		{name: "subject_invalid", refuse: func(a *fakeAS1) {
			a.setRefusal(identitybroker.NewServerRefusal(http.StatusBadRequest, "invalid_request", ""))
		}, want: server.ErrBearerRejected, as1Called: true},
		{name: "signed_out", refuse: func(a *fakeAS1) { a.refuseWith("signed_out") }, want: server.ErrSignInRequired, as1Called: true},
		{name: "upstream_revoked", refuse: func(a *fakeAS1) { a.refuseWith("upstream_revoked") }, want: server.ErrSignInRequired, as1Called: true},
		{name: "owner_action_required", refuse: func(a *fakeAS1) { a.refuseWith("owner_action_required") }, want: server.ErrSignInRequired, as1Called: true},
		{name: "owner_mismatch", refuse: func(*fakeAS1) {}, otherSubID: true, want: server.ErrIdentityBrokerSessionRefused},
		{name: "session_owned", refuse: func(*fakeAS1) {}, ownedByBob: true, want: server.ErrIdentityBrokerSessionRefused},
		{name: "unavailable", refuse: func(a *fakeAS1) {
			a.setRefusal(identitybroker.NewServerRefusal(http.StatusServiceUnavailable, "temporarily_unavailable", ""))
		}, want: server.ErrIdentityBrokerUnavailable, as1Called: true},
		{name: "invalid_client", refuse: func(a *fakeAS1) {
			a.setRefusal(identitybroker.NewServerRefusal(http.StatusUnauthorized, "invalid_client", ""))
		}, want: server.ErrInternal, as1Called: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			llm := mockllm.New(mockllm.TextTurn("must not run"))
			f := newSessionOpenFixture(t, llm)
			var run func(context.Context) error
			if tc.ownedByBob {
				sess, err := f.svc.CreateSession(aliceContext(t), session.ModeDefault, session.Limits{})
				if err != nil {
					t.Fatal(err)
				}
				alices := wantBrokerSession(t, f.broker, sess)
				if err := f.broker.inner.CloseSession(t.Context(), identitybroker.CloseSessionRequest{SessionID: alices.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
					t.Fatal(err)
				}
				if _, err := f.broker.inner.OpenSession(t.Context(), identitybroker.OpenSessionRequest{
					HostSessionID: string(sess.ID), Incarnation: string(sess.Incarnation()),
					Owner: identitybroker.Owner{Issuer: brokerAlice.Issuer, Subject: "bob-117"},
				}); err != nil {
					t.Fatal(err)
				}
				run = func(ctx context.Context) error { return runToEnd(ctx, t, f.svc, sess.ID) }
			} else {
				run = func(ctx context.Context) error {
					_, err := f.svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
					return err
				}
			}
			tc.refuse(f.as1)
			bearer := f.token()
			if tc.otherSubID {
				bearer = accessTokenFor(&session.Principal{Issuer: brokerAlice.Issuer, Subject: "bob-117"}, "jti-mismatch")
				f.tokens[bearer] = *brokerAlice
			}
			before, opensBefore := f.state(t), f.broker.openCount()
			var err error
			withinRequest(t, "unary", f.auth, bearer, func(ctx context.Context) { err = run(ctx) })
			if !errors.Is(err, tc.want) {
				t.Fatalf("open = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), bearer) {
				t.Fatal("the error contains the access token")
			}
			if got := f.as1.exchanges() > 0; got != tc.as1Called {
				t.Fatalf("AS-1 called %t, want %t", got, tc.as1Called)
			}
			if after := f.state(t); after != before {
				t.Fatalf("a refused open changed state:\nbefore:\n%s\nafter:\n%s", before, after)
			}
			if llm.Calls() != 0 {
				t.Fatal("the agent ran after a refused open")
			}
			if !tc.ownedByBob {
				// The refused create paired a broker session before the
				// conversion; it must not outlive the unsaved session.
				host := session.SessionID(f.broker.openRequests()[opensBefore].HostSessionID)
				if open, ok := f.broker.find(t, host); ok {
					t.Fatalf("the refused create left broker session %+v open", open)
				}
			}
		})
	}
}

// TestIdentityBrokerRefusedTokenChangesNothing proves that on every path that
// opens a broker session with the request's token, a refused token is
// answered with the tagged 401 before the request changed anything: no
// session, history, event, or cancelled run, though each path saves after its
// open. Each path then runs with an accepted token, and no request presents
// its token twice.
func TestIdentityBrokerRefusedTokenChangesNothing(t *testing.T) {
	type pathFixture struct {
		f   *sessionOpenFixture
		run func(context.Context) error
		// extra reports changes outside the store and event log.
		extra func() string
	}
	// settledRun runs a prompt to its end and retires the run, as a client's
	// finished request leaves the session.
	settledRun := func(t *testing.T, f *sessionOpenFixture, id session.SessionID) {
		t.Helper()
		ctx := aliceContext(t)
		run, err := f.svc.StartRun(ctx, id, "hello")
		if err != nil {
			t.Fatal(err)
		}
		drain(run)
		f.svc.Persist(ctx, id)
		f.svc.FinishRun(id, run)
	}
	// failedSession is a session whose run failed retryably, so a prompt would
	// recover it and a retry would rerun the step, both saving.
	failedSession := func(t *testing.T, f *sessionOpenFixture) session.SessionID {
		t.Helper()
		sess, err := f.svc.CreateSession(aliceContext(t), session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		settledRun(t, f, sess.ID)
		if loaded, err := f.store.Load(t.Context(), sess.ID); err != nil || loaded.State != session.StateFailed {
			t.Fatalf("setup: session %v, %v; want it failed", loaded, err)
		}
		return sess.ID
	}
	retryable := mockllm.ErrorTurn(&retryFailure{session.RetryDispositionRetryable, session.StreamProgressPrecommit})
	paths := map[string]func(t *testing.T) pathFixture{
		"create": func(t *testing.T) pathFixture {
			f := newSessionOpenFixture(t, mockllm.New())
			return pathFixture{f: f, run: func(ctx context.Context) error {
				_, err := f.svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
				return err
			}}
		},
		"fork": func(t *testing.T) pathFixture {
			f := newSessionOpenFixture(t, mockllm.New())
			source, err := f.svc.CreateSession(aliceContext(t), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			return pathFixture{f: f, run: func(ctx context.Context) error {
				_, err := f.svc.ForkSessionSuccessor(ctx, server.ForkSuccessorRequest{Source: source.ID})
				return err
			}}
		},
		"clear": func(t *testing.T) pathFixture {
			var mu sync.Mutex
			var cleared []session.SessionID
			f := newSessionOpenFixture(t, mockllm.New(), func(cfg *server.Config) {
				cfg.SessionCleared = func(id session.SessionID) {
					mu.Lock()
					cleared = append(cleared, id)
					mu.Unlock()
				}
			})
			source, err := f.svc.CreateSession(aliceContext(t), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			return pathFixture{f: f, run: func(ctx context.Context) error {
				_, err := f.svc.ClearSessionSuccessor(ctx, source.ID, server.SuccessorPlacement{})
				return err
			}, extra: func() string {
				mu.Lock()
				defer mu.Unlock()
				return fmt.Sprint(cleared)
			}}
		},
		"prompt": func(t *testing.T) pathFixture {
			// The first run fails, so the next run entry would recover the
			// session and save it: the refusal must come before that.
			llm := mockllm.New(retryable, mockllm.TextTurn("done"))
			f := newSessionOpenFixture(t, llm)
			id := failedSession(t, f)
			return pathFixture{f: f, run: func(ctx context.Context) error {
				return runToEnd(ctx, t, f.svc, id)
			}, extra: func() string { return fmt.Sprint(llm.Calls()) }}
		},
		"retry": func(t *testing.T) pathFixture {
			// Retrying the failed run saves the retry and runs the step again.
			llm := mockllm.New(retryable, mockllm.TextTurn("done"))
			f := newSessionOpenFixture(t, llm)
			id := failedSession(t, f)
			return pathFixture{f: f, run: func(ctx context.Context) error {
				run, err := f.svc.RetryFailedRun(ctx, id)
				drain(run)
				return err
			}, extra: func() string { return fmt.Sprint(llm.Calls()) }}
		},
		"approval": func(t *testing.T) pathFixture {
			// A persisted ask: resuming it records the verdict and saves.
			llm := mockllm.New(mockllm.TextTurn("done"))
			f := newSessionOpenFixture(t, llm)
			parked, ask := makePersistedControlSession(t, "approval-session")
			f.save(t, parked)
			return pathFixture{f: f, run: func(ctx context.Context) error {
				run, err := f.svc.ApproveRun(ctx, parked.ID, ask.AskID, session.VerdictAllowOnce, parked.RunID())
				drain(run)
				return err
			}, extra: func() string { return fmt.Sprint(llm.Calls()) }}
		},
		"run ask": func(t *testing.T) pathFixture {
			llm := mockllm.New(mockllm.TextTurn("done"))
			f := newSessionOpenFixture(t, llm)
			parked, ask := makePersistedControlSession(t, "run-ask-session")
			f.save(t, parked)
			return pathFixture{f: f, run: func(ctx context.Context) error {
				_, err := f.svc.ResolveRunAsk(ctx, parked.ID, parked.RunID(), ask.AskID, session.VerdictAllowOnce)
				return err
			}, extra: func() string { return fmt.Sprint(llm.Calls()) }}
		},
		"plan ask": func(t *testing.T) pathFixture {
			llm := mockllm.New(mockllm.TextTurn("executed"))
			f := newSessionOpenFixture(t, llm)
			parked := makePlanControlSession(t, "plan-ask-session")
			f.save(t, parked)
			return pathFixture{f: f, run: func(ctx context.Context) error {
				_, err := f.svc.ResolvePlanAsk(ctx, parked.ID, parked.RunID(), "plan-ask", session.VerdictAllowOnce)
				return err
			}, extra: func() string { return fmt.Sprint(llm.Calls()) }}
		},
		"compact": func(t *testing.T) pathFixture {
			// Compaction rewrites the history it summarises and saves it.
			llm := mockllm.New(mockllm.TextTurn("one"), mockllm.TextTurn("two"), mockllm.TextTurn("summary"))
			f := newSessionOpenFixture(t, llm)
			sess, err := f.svc.CreateSession(aliceContext(t), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				settledRun(t, f, sess.ID)
			}
			return pathFixture{f: f, run: func(ctx context.Context) error {
				_, err := f.svc.CompactSession(ctx, sess.ID, brokerAlice)
				return err
			}, extra: func() string { return fmt.Sprint(llm.Calls()) }}
		},
		"team": func(t *testing.T) pathFixture {
			ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
			env := tool.MustEnvironment(ref, memfs.NewWorkspace("/ws"), memledger.New(), nil)
			provider := &teamRootCaptureProvider{}
			f := newSessionOpenFixture(t, mockllm.New(), func(cfg *server.Config) {
				cfg.PlacementProvider = delegationPlacementProvider{binding: server.PlacementBinding{Environment: env, Ref: ref}}
				cfg.PlacementScope = "test-scope"
				cfg.MemberEngine = func(*team.Team, agent.MemberSpec, string) agent.MemberBuild {
					return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{LLM: provider, Catalog: tool.NewCatalog(), Model: "test"})}
				}
			})
			root := session.New("team-root", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
			if err := root.RestoreLabels(brokerAlice, session.Authority{}); err != nil {
				t.Fatal(err)
			}
			if err := f.store.Create(t.Context(), root); err != nil {
				t.Fatal(err)
			}
			teamID, _, err := f.svc.CreateTeamForSession(t.Context(), root.ID, "reviewers", "goal", 0, []agent.MemberSpec{{Name: "lead", Lead: true}})
			if err != nil {
				t.Fatal(err)
			}
			return pathFixture{f: f, run: func(ctx context.Context) error {
				_, err := f.svc.RunTeam(ctx, teamID, func(agent.TeamEvent) {})
				return err
			}, extra: func() string {
				provider.mu.Lock()
				defer provider.mu.Unlock()
				return fmt.Sprint(len(provider.pairs))
			}}
		},
	}
	for name, setup := range paths {
		t.Run(name, func(t *testing.T) {
			p := setup(t)
			before, extraBefore := p.f.state(t), ""
			if p.extra != nil {
				extraBefore = p.extra()
			}
			p.f.as1.refuseWith("token_used")
			var err error
			if n := p.f.within(t, func(ctx context.Context) { err = p.run(ctx) }); n != 1 {
				t.Fatalf("the refused request presented its token %d times, want once (it returned %v)", n, err)
			}
			if !errors.Is(err, server.ErrBearerRejected) {
				t.Fatalf("with a refused token: %v, want ErrBearerRejected", err)
			}
			if after := p.f.state(t); after != before {
				t.Fatalf("a refused token changed state:\nbefore:\n%s\nafter:\n%s", before, after)
			}
			if p.extra != nil {
				if got := p.extra(); got != extraBefore {
					t.Fatalf("a refused token changed %s to %s", extraBefore, got)
				}
			}
			// Control: the same request with an accepted token, presenting it
			// at most once however many opens the path makes.
			p.f.as1.refuseWith("")
			if n := p.f.within(t, func(ctx context.Context) { err = p.run(ctx) }); n > 1 {
				t.Fatalf("the accepted request presented its token %d times, want at most once", n)
			}
			if err != nil {
				t.Fatalf("with an accepted token: %v", err)
			}
		})
	}
}

// TestIdentityBrokerClearKeepsARunningSourceOnARefusedToken proves a clear
// whose token is refused neither cancels the source's run in flight nor
// changes its history.
func TestIdentityBrokerClearKeepsARunningSourceOnARefusedToken(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(port.LLMRequest) {
		once.Do(func() { close(entered) })
		<-release
	})}, mockllm.TextTurn("done"))
	f := newSessionOpenFixture(t, llm)
	ctx := aliceContext(t)
	source, err := f.svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.svc.StartRun(ctx, source.ID, "hello")
	if err != nil {
		t.Fatal(err)
	}
	defer once.Do(func() {})
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the source's run never reached the model")
	}
	before := f.state(t)
	f.as1.refuseWith("token_used")
	f.within(t, func(ctx context.Context) {
		_, err = f.svc.ClearSessionSuccessor(ctx, source.ID, server.SuccessorPlacement{})
	})
	if !errors.Is(err, server.ErrBearerRejected) {
		t.Fatalf("clear with a refused token: %v, want ErrBearerRejected", err)
	}
	if after := f.state(t); after != before {
		t.Fatalf("the refused clear changed state:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	close(release)
	var evs []session.Event
	for ev := range run.Events() {
		evs = append(evs, ev)
	}
	f.svc.FinishRun(source.ID, run)
	if hasResultWithStop(evs, session.StopCancelled) || !hasResultWithStop(evs, session.StopEndTurn) {
		t.Fatalf("the source's run after the refused clear stopped with %v, want it to finish its turn", stopReasonsOf(evs))
	}
	if loaded, err := f.store.Load(t.Context(), source.ID); err != nil || loaded.State != session.StateCompleted {
		t.Fatalf("source after its run: %v, %v; want it completed", loaded, err)
	}
}

// TestIdentityBrokerClearClosesItsBrokerSessionWhenTheSuccessorIsNotSaved
// proves the broker session Clear opens before cancelling the source is
// closed when no successor is saved, here because the source vanished.
func TestIdentityBrokerClearClosesItsBrokerSessionWhenTheSuccessorIsNotSaved(t *testing.T) {
	f := newSessionOpenFixture(t, mockllm.New())
	source, err := f.svc.CreateSession(aliceContext(t), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var successorHost session.SessionID
	f.broker.set(func(b *recordingBroker) {
		b.afterOpen = func(req identitybroker.OpenSessionRequest) {
			if req.HostSessionID != string(source.ID) && successorHost == "" {
				successorHost = session.SessionID(req.HostSessionID)
				// The source goes away between the successor's broker open and
				// its creation, as a concurrent delete would leave it.
				if err := f.store.Delete(context.Background(), source.ID); err != nil {
					t.Error(err)
				}
			}
		}
	})
	closes := f.broker.closeCount()
	var successor session.SessionID
	f.within(t, func(ctx context.Context) {
		successor, err = f.svc.ClearSessionSuccessor(ctx, source.ID, server.SuccessorPlacement{})
	})
	if successor != "" {
		t.Fatalf("clear of a vanished source = %q, %v; want no successor", successor, err)
	}
	if successorHost == "" {
		t.Fatal("clear opened no broker session for its successor")
	}
	if open, ok := f.broker.find(t, successorHost); ok {
		t.Fatalf("the unsaved successor's broker session %+v is still open", open)
	}
	if f.broker.closeCount() == closes {
		t.Fatal("clear closed no broker session")
	}
}

// TestIdentityBrokerLostCreateRaceClosesOnlyTheLosersBrokerSession runs two
// replicas, each with its own in-process broker, creating the same session ID
// against one store. Both find no broker session and open one before saving;
// the loser's save fails, so it closes its own broker session by ID while the
// winner's stays open.
func TestIdentityBrokerLostCreateRaceClosesOnlyTheLosersBrokerSession(t *testing.T) {
	winner := newSessionOpenFixture(t, mockllm.New())
	loser := newSessionOpenFixture(t, mockllm.New(), func(cfg *server.Config) { cfg.Store = winner.store })
	loser.store = winner.store
	create := func(f *sessionOpenFixture) (*session.Session, error) {
		var sess *session.Session
		var err error
		f.within(t, func(ctx context.Context) {
			sess, err = f.svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID("dup"))
		})
		return sess, err
	}
	// The winner's whole create runs while the loser is between its broker
	// open and its save: both found no session and no broker session.
	var won *session.Session
	var wonErr error
	var once sync.Once
	loser.broker.set(func(b *recordingBroker) {
		b.afterOpen = func(identitybroker.OpenSessionRequest) {
			once.Do(func() { won, wonErr = create(winner) })
		}
	})
	lost, lostErr := create(loser)
	if wonErr != nil || won == nil {
		t.Fatalf("the winning create = %v, %v", won, wonErr)
	}
	winning, ok := winner.broker.find(t, "dup")
	if !ok || !winning.HoldsRefreshToken {
		t.Fatalf("winner's broker session = %+v (%v), want one holding a refresh token", winning, ok)
	}
	// The store's refusal of the duplicate reaches the client as an invalid
	// argument naming the existing ID.
	if !errors.Is(lostErr, server.ErrInvalidArgument) || !strings.Contains(lostErr.Error(), "already exists") || lost != nil {
		t.Fatalf("the losing create = %v, %v; want ErrInvalidArgument for an existing session ID", lost, lostErr)
	}
	if stored, err := winner.store.Load(t.Context(), "dup"); err != nil || stored.Incarnation() != won.Incarnation() {
		t.Fatalf("stored dup = %v, %v; want the winner's", stored, err)
	}
	if open, ok := loser.broker.find(t, "dup"); ok {
		t.Fatalf("the loser's broker session %+v outlived its failed save", open)
	}
	if loser.as1.exchanges() != 1 || loser.broker.openCount() != 1 {
		t.Fatalf("loser: %d exchanges, %d opens; want its one pre-open", loser.as1.exchanges(), loser.broker.openCount())
	}
	if loser.broker.closeCount() != 1 {
		t.Fatalf("loser closed %d broker sessions, want its own one", loser.broker.closeCount())
	}
	if got, ok := winner.broker.find(t, "dup"); !ok || got.ID != winning.ID {
		t.Fatalf("winner's broker session after the race = %+v (%v), want %s", got, ok, winning.ID)
	}
}

// TestIdentityBrokerSendsATokenOnlyWithoutALiveRefreshToken proves a session
// whose broker session holds a refresh token does not spend later requests'
// tokens.
func TestIdentityBrokerSendsATokenOnlyWithoutALiveRefreshToken(t *testing.T) {
	f := newSessionOpenFixture(t, mockllm.New(mockllm.TextTurn("one"), mockllm.TextTurn("two")))
	var sess *session.Session
	var err error
	f.within(t, func(ctx context.Context) { sess, err = f.svc.CreateSession(ctx, session.ModeDefault, session.Limits{}) })
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := f.broker.find(t, sess.ID); !ok || !got.HoldsRefreshToken || f.as1.exchanges() != 1 {
		t.Fatalf("after create: broker session %+v (%v), %d exchanges; want one holding a refresh token", got, ok, f.as1.exchanges())
	}
	for range 2 {
		if n := f.within(t, func(ctx context.Context) { err = runToEnd(ctx, t, f.svc, sess.ID) }); n != 0 {
			t.Fatalf("a run presented its token %d times to a session holding a refresh token", n)
		}
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	}
	if n := f.as1.exchanges(); n != 1 {
		t.Fatalf("exchanges after two runs = %d, want the create's one", n)
	}
}

// TestIdentityBrokerConvertsATokenForASessionHoldingNone proves a session
// paired without a token, such as one created without a user request, takes
// the next request's token.
func TestIdentityBrokerConvertsATokenForASessionHoldingNone(t *testing.T) {
	f := newSessionOpenFixture(t, mockllm.New(mockllm.TextTurn("done")))
	sess, err := f.svc.CreateSession(aliceContext(t), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := f.broker.find(t, sess.ID); !ok || got.HoldsRefreshToken {
		t.Fatalf("after a create without a token: broker session %+v (%v), want one holding no refresh token", got, ok)
	}
	if n := f.within(t, func(ctx context.Context) { err = runToEnd(ctx, t, f.svc, sess.ID) }); n != 1 {
		t.Fatalf("the run presented its token %d times, want once", n)
	}
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, ok := f.broker.find(t, sess.ID); !ok || !got.HoldsRefreshToken || f.as1.exchanges() != 1 {
		t.Fatalf("after a run with a token: broker session %+v (%v), %d exchanges; want it holding the run's", got, ok, f.as1.exchanges())
	}
}

// TestIdentityBrokerReopenNeverRepresentsAConvertedToken proves that when the
// broker session closes after the request's token was converted for it, the
// request fails with the tagged 401, having changed nothing, instead of
// opening a new broker session with the spent token.
func TestIdentityBrokerReopenNeverRepresentsAConvertedToken(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("must not run"))
	f := newSessionOpenFixture(t, llm)
	// Registration fails at create, so the run below registers the agent, and
	// the hook can close the broker session in between.
	f.broker.set(func(b *recordingBroker) { b.registerUnavailable = true })
	sess, err := f.svc.CreateSession(aliceContext(t), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	f.broker.set(func(b *recordingBroker) { b.registerUnavailable = false })
	var once sync.Once
	f.broker.set(func(b *recordingBroker) {
		b.beforeRegister = func(req identitybroker.RegisterAgentRequest) {
			once.Do(func() {
				_ = b.inner.CloseSession(context.Background(), identitybroker.CloseSessionRequest{SessionID: req.SessionID, Reason: identitybroker.ReasonSessionClosed})
			})
		}
	})
	before, opensBefore := f.state(t), f.broker.openCount()
	if n := f.within(t, func(ctx context.Context) { err = runToEnd(ctx, t, f.svc, sess.ID) }); n != 1 {
		t.Fatalf("the run presented its token %d times, want once", n)
	}
	if !errors.Is(err, server.ErrBearerRejected) {
		t.Fatalf("run whose broker session closed after converting its token: %v, want ErrBearerRejected", err)
	}
	if n := f.as1.exchanges(); n != 1 {
		t.Fatalf("exchanges = %d, want 1: the spent token was presented again", n)
	}
	if n := f.broker.openCount() - opensBefore; n != 1 {
		t.Fatalf("opens = %d, want 1: no second open in the same request", n)
	}
	if after := f.state(t); after != before {
		t.Fatalf("the refused run changed state:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if llm.Calls() != 0 {
		t.Fatal("the agent ran")
	}
}

// TestIdentityBrokerRefusalAfterAResponseIsNotTagged proves a token reason
// from the broker on an open made after the stream answered is never
// reported with the bearer_rejected tag: the client could not replay it.
func TestIdentityBrokerRefusalAfterAResponseIsNotTagged(t *testing.T) {
	broker := newRecordingBroker()
	svc := newIdentityBrokerService(t, broker, mockllm.New(mockllm.TextTurn("must not run")))
	auth := server.NewAuthenticator(server.SecurityConfig{Validator: brokerTokenValidator()})
	sess, err := svc.CreateSession(aliceContext(t), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	lost := wantBrokerSession(t, broker, sess)
	if err := broker.inner.CloseSession(t.Context(), identitybroker.CloseSessionRequest{SessionID: lost.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatal(err)
	}
	broker.set(func(b *recordingBroker) {
		b.openErr = identitybroker.ReasonError(identitybroker.ReasonTokenUsed, "test broker refuses the token")
	})
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+userBearer))
	var runErr error
	err = auth.StreamInterceptor()(nil, fakeServerStream{ctx: ctx}, &grpc.StreamServerInfo{FullMethod: "/mecatl.v1.HarnessService/Converse"},
		func(_ any, ss grpc.ServerStream) error {
			if err := ss.SendMsg(&mecatlv1.ConverseResponse{}); err != nil {
				return err
			}
			runErr = runToEnd(ss.Context(), t, svc, sess.ID)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if runErr == nil || errors.Is(runErr, server.ErrBearerRejected) {
		t.Fatalf("open refused for a token after the stream answered: %v; want an error without the bearer_rejected tag", runErr)
	}
}

// TestIdentityBrokerOpensAfterAResponseCarryNoToken proves a stream or HTTP
// request stops handing its token to the broker once it has answered, since a
// refusal then could not be retried as if nothing happened.
func TestIdentityBrokerOpensAfterAResponseCarryNoToken(t *testing.T) {
	answers := map[string]func(t *testing.T, w http.ResponseWriter){
		"http WriteHeader": func(_ *testing.T, w http.ResponseWriter) { w.WriteHeader(http.StatusOK) },
		"http Write": func(t *testing.T, w http.ResponseWriter) {
			if _, err := w.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
		},
		"http Flush": func(t *testing.T, w http.ResponseWriter) {
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatal("the response writer lost http.Flusher")
			}
			flusher.Flush()
		},
	}
	for _, surface := range []string{"stream", "http WriteHeader", "http Write", "http Flush"} {
		t.Run(surface, func(t *testing.T) {
			broker := newRecordingBroker()
			svc := newIdentityBrokerService(t, broker, mockllm.New())
			auth := server.NewAuthenticator(server.SecurityConfig{Validator: brokerTokenValidator()})
			create := func(ctx context.Context) {
				if _, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{}); err != nil {
					t.Error(err)
				}
			}
			// Each request opens once, answered first or not: unanswered, the
			// open presents the token (the control); answered, it presents
			// none. No open precedes the answer, so the token is still the
			// request's until the answer drops it.
			for _, answer := range []bool{false, true} {
				before := broker.openCount()
				want := userBearer
				if answer {
					want = ""
				}
				if surface == "stream" {
					ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+userBearer))
					err := auth.StreamInterceptor()(nil, fakeServerStream{ctx: ctx}, &grpc.StreamServerInfo{FullMethod: "/mecatl.v1.HarnessService/Converse"},
						func(_ any, ss grpc.ServerStream) error {
							if answer {
								if err := ss.SendMsg(&mecatlv1.ConverseResponse{}); err != nil {
									return err
								}
							}
							create(ss.Context())
							return nil
						})
					if err != nil {
						t.Fatal(err)
					}
					wantNewOpenTokens(t, broker, before, want)
					continue
				}
				req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/sessions", nil)
				req.Header.Set("Authorization", "Bearer "+userBearer)
				rec := httptest.NewRecorder()
				ran := false
				auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					ran = true
					if u, ok := w.(interface{ Unwrap() http.ResponseWriter }); !ok || u.Unwrap() != http.ResponseWriter(rec) {
						t.Error("http.ResponseController cannot reach the underlying writer through Unwrap")
					}
					if answer {
						answers[surface](t, w)
					}
					create(r.Context())
				})).ServeHTTP(rec, req)
				if !ran {
					t.Fatal("handler did not run")
				}
				wantNewOpenTokens(t, broker, before, want)
			}
		})
	}
}

// TestIdentityBrokerRefusalTransportParity proves a refused token reaches the
// client exactly as the authenticator's own rejection does, on both wire
// surfaces, and a revoked authority as sign_in_required.
func TestIdentityBrokerRefusalTransportParity(t *testing.T) {
	codeOf := func(t *testing.T, err error) (codes.Code, string) {
		t.Helper()
		st, ok := status.FromError(err)
		if !ok {
			t.Fatalf("not a status: %v", err)
		}
		return st.Code(), errorCodeOf(t, st)
	}
	restCreate := func(t *testing.T, f *sessionOpenFixture, bearer string) (int, string, string) {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/sessions", strings.NewReader(`{}`))
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		f.auth.Middleware(server.NewHTTPHandler(f.svc)).ServeHTTP(rec, req)
		var body struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body.Code, rec.Header().Get("WWW-Authenticate")
	}
	for _, tc := range []struct{ refusal, wantCode string }{
		{"token_used", "bearer_rejected"},
		{"signed_out", "sign_in_required"},
	} {
		t.Run(tc.refusal, func(t *testing.T) {
			f := newSessionOpenFixture(t, mockllm.New())
			f.as1.refuseWith(tc.refusal)
			client, cleanup := dialGRPCSecure(t, f.svc, f.auth)
			t.Cleanup(cleanup)

			_, err := client.CreateSession(bearerCtx(t.Context(), f.token()), &mecatlv1.CreateSessionRequest{})
			if code, reason := codeOf(t, err); code != codes.Unauthenticated || reason != tc.wantCode {
				t.Fatalf("gRPC create = %v, %q; want Unauthenticated, %q", code, reason, tc.wantCode)
			}
			if tc.wantCode == "bearer_rejected" {
				// The authenticator's own rejection carries the same code and tag.
				_, authErr := client.CreateSession(bearerCtx(t.Context(), "unknown-bearer"), &mecatlv1.CreateSessionRequest{})
				authCode, authReason := codeOf(t, authErr)
				if code, reason := codeOf(t, err); code != authCode || reason != authReason {
					t.Fatalf("broker refusal %v/%q differs from the authenticator's %v/%q", code, reason, authCode, authReason)
				}
			}
			if httpStatus, code, challenge := restCreate(t, f, f.token()); httpStatus != http.StatusUnauthorized || code != tc.wantCode || challenge != `Bearer error="invalid_token"` {
				t.Fatalf("HTTP create = %d %q, challenge %q; want 401 %q with an invalid_token challenge", httpStatus, code, challenge, tc.wantCode)
			}
			if state := f.state(t); state != "" {
				t.Fatalf("refused creates saved:\n%s", state)
			}
		})
	}
	// The authenticator's REST rejection carries the same code as a broker
	// refusal, with the challenge the presented bearer calls for.
	f := newSessionOpenFixture(t, mockllm.New())
	for _, tc := range []struct{ name, bearer, challenge string }{
		{"missing bearer", "", "Bearer"},
		{"invalid bearer", "unknown-bearer", `Bearer error="invalid_token"`},
	} {
		if httpStatus, code, challenge := restCreate(t, f, tc.bearer); httpStatus != http.StatusUnauthorized || code != "bearer_rejected" || challenge != tc.challenge {
			t.Errorf("%s: HTTP create = %d %q, challenge %q; want 401 \"bearer_rejected\", %q", tc.name, httpStatus, code, challenge, tc.challenge)
		}
	}
}
