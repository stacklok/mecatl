package server_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

var brokerAlice = &session.Principal{Issuer: "https://login.example.com", Subject: "alice-204", GrantType: session.GrantTypeUser}

// recordingBroker wraps the real broker, records every call, and can make
// every call fail as unavailable.
type recordingBroker struct {
	inner identitybroker.Service
	// verifier is the broker's in-process credential check, as AS-1 calls it.
	verifier identitybroker.CredentialVerifier

	mu        sync.Mutex
	opens     []identitybroker.OpenSessionRequest
	closes    []identitybroker.CloseSessionRequest
	registers []identitybroker.RegisterAgentRequest
	// registered holds the registrations the broker accepted.
	registered  []identitybroker.RegisterAgentRequest
	unavailable bool
	// registerUnavailable makes only RegisterAgent fail as unavailable.
	registerUnavailable bool
	// beforeRegister, when set, runs before each RegisterAgent reaches the
	// broker.
	beforeRegister func(identitybroker.RegisterAgentRequest)
	// otherAgent makes RegisterAgent return another agent than registered.
	otherAgent bool
	// renamedAgent makes RegisterAgent return another agent whose SPIFFE ID
	// still ends in the registered incarnation.
	renamedAgent bool
	// registerErr, when set, makes RegisterAgent fail with it.
	registerErr error
	// editRegistration, when set, changes each registration RegisterAgent
	// returns.
	editRegistration func(*identitybroker.Registration)
	// openErr, when set, makes OpenSession fail with it.
	openErr error
	// afterOpen, when set, runs after each OpenSession the broker answered.
	afterOpen func(identitybroker.OpenSessionRequest)
}

// newRecordingBroker wraps the real identity broker, so these tests exercise
// the implementation mecak8s runs, not the stand-in. opts configure it, such
// as identitybroker.WithAuthServer for session open to convert tokens.
func newRecordingBroker(opts ...identitybroker.Option) *recordingBroker {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	core, err := identitybroker.New(identitybroker.Config{
		TrustDomain:        "agents.example.com",
		Audience:           "https://broker.example.com",
		CredentialLifetime: 5 * time.Minute,
	}, nil, key, opts...)
	if err != nil {
		panic(err)
	}
	return &recordingBroker{inner: identitybroker.NewInProcess(identitybroker.UnverifiedAuthenticator{Name: "mecak8s-test"}, core), verifier: core}
}

func (b *recordingBroker) down() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.unavailable {
		return identitybroker.NewError(identitybroker.CodeUnavailable, "", "test broker is down")
	}
	return nil
}

func (b *recordingBroker) setUnavailable(v bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.unavailable = v
}

func (b *recordingBroker) OpenSession(ctx context.Context, req identitybroker.OpenSessionRequest) (identitybroker.Session, error) {
	if err := b.down(); err != nil {
		return identitybroker.Session{}, err
	}
	b.mu.Lock()
	b.opens = append(b.opens, req)
	openErr, after := b.openErr, b.afterOpen
	b.mu.Unlock()
	if openErr != nil {
		return identitybroker.Session{}, openErr
	}
	opened, err := b.inner.OpenSession(ctx, req)
	if after != nil {
		after(req)
	}
	return opened, err
}

func (b *recordingBroker) FindSession(ctx context.Context, req identitybroker.FindSessionRequest) (identitybroker.Session, bool, error) {
	if err := b.down(); err != nil {
		return identitybroker.Session{}, false, err
	}
	return b.inner.FindSession(ctx, req)
}

func (b *recordingBroker) CloseSession(ctx context.Context, req identitybroker.CloseSessionRequest) error {
	if err := b.down(); err != nil {
		return err
	}
	b.mu.Lock()
	b.closes = append(b.closes, req)
	b.mu.Unlock()
	return b.inner.CloseSession(ctx, req)
}

func (b *recordingBroker) RegisterAgent(ctx context.Context, req identitybroker.RegisterAgentRequest) (identitybroker.Registration, error) {
	if err := b.down(); err != nil {
		return identitybroker.Registration{}, err
	}
	b.mu.Lock()
	b.registers = append(b.registers, req)
	registerDown, hook, other, renamed := b.registerUnavailable, b.beforeRegister, b.otherAgent, b.renamedAgent
	registerErr, edit := b.registerErr, b.editRegistration
	b.mu.Unlock()
	if registerDown {
		return identitybroker.Registration{}, identitybroker.NewError(identitybroker.CodeUnavailable, "", "test broker cannot register")
	}
	if registerErr != nil {
		return identitybroker.Registration{}, registerErr
	}
	if hook != nil {
		hook(req)
	}
	reg, err := b.inner.RegisterAgent(ctx, req)
	if err == nil {
		b.mu.Lock()
		b.registered = append(b.registered, req)
		b.mu.Unlock()
	}
	if err == nil && other {
		reg.Agent.Subject += "-other"
	}
	if err == nil && renamed {
		reg.Agent.Subject = strings.Replace(reg.Agent.Subject, "/shared/", "/renamed/", 1)
	}
	if err == nil && edit != nil {
		edit(&reg)
	}
	return reg, err
}

func (b *recordingBroker) registerRequests() []identitybroker.RegisterAgentRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]identitybroker.RegisterAgentRequest(nil), b.registers...)
}

func (b *recordingBroker) acceptedRegistrations() []identitybroker.RegisterAgentRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]identitybroker.RegisterAgentRequest(nil), b.registered...)
}

func (b *recordingBroker) set(f func(*recordingBroker)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f(b)
}

func (b *recordingBroker) AuthorizeCall(ctx context.Context, req identitybroker.AuthorizeCallRequest) (identitybroker.Authorization, error) {
	return b.inner.AuthorizeCall(ctx, req)
}

func (b *recordingBroker) openCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.opens)
}

func (b *recordingBroker) openRequests() []identitybroker.OpenSessionRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]identitybroker.OpenSessionRequest(nil), b.opens...)
}

func (b *recordingBroker) closeCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.closes)
}

// find returns the open broker session for host.
func (b *recordingBroker) find(t *testing.T, host session.SessionID) (identitybroker.Session, bool) {
	t.Helper()
	s, ok, err := b.inner.FindSession(context.Background(), identitybroker.FindSessionRequest{RootHostSessionID: string(host)})
	if err != nil {
		t.Fatalf("FindSession(%s): %v", host, err)
	}
	return s, ok
}

func newIdentityBrokerService(t *testing.T, broker identitybroker.Service, llm *mockllm.Provider, configure ...func(*server.Config)) *server.Service {
	t.Helper()
	store := memstore.New()
	eng := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
		Store:   store,
	})
	cfg := server.Config{
		Engine:         eng,
		Store:          store,
		IdentityBroker: broker,
		Now:            func() time.Time { return time.Unix(0, 0) },
	}
	for _, f := range configure {
		f(&cfg)
	}
	svc, err := newPlacementTestService(cfg)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func aliceContext(t *testing.T) context.Context {
	return session.WithPrincipal(t.Context(), brokerAlice)
}

func runToEnd(ctx context.Context, t *testing.T, svc *server.Service, id session.SessionID) error {
	t.Helper()
	run, err := svc.StartRun(ctx, id, "hello")
	if err != nil {
		return err
	}
	for range run.Events() {
	}
	return nil
}

// wantBrokerSession checks host has an open broker session for its saved
// incarnation and owner.
func wantBrokerSession(t *testing.T, broker *recordingBroker, sess *session.Session) identitybroker.Session {
	t.Helper()
	got, ok := broker.find(t, sess.ID)
	if !ok {
		t.Fatalf("session %s has no open broker session", sess.ID)
	}
	want := identitybroker.Owner{Issuer: brokerAlice.Issuer, Subject: brokerAlice.Subject}
	if got.Incarnation != string(sess.Incarnation()) || got.Owner != want {
		t.Fatalf("broker session = %+v, want incarnation %s and owner %+v", got, sess.Incarnation(), want)
	}
	return got
}

func TestIdentityBrokerOpensOnCreate(t *testing.T) {
	broker := newRecordingBroker()
	svc := newIdentityBrokerService(t, broker, mockllm.New())
	sess, err := svc.CreateSession(aliceContext(t), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	wantBrokerSession(t, broker, sess)
}

func TestIdentityBrokerOpensScheduledSessionsForTheScheduleOwner(t *testing.T) {
	broker := newRecordingBroker()
	svc := newIdentityBrokerService(t, broker, mockllm.New())
	// The scheduler creates fires with the schedule's captured owner, not its
	// own system principal.
	sess, err := svc.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault,
		server.WithSessionID("fire-1"), server.WithOwner(brokerAlice), server.WithScheduledRelationship("nightly", ""))
	if err != nil {
		t.Fatal(err)
	}
	if sess.Kind != session.SessionKindScheduled {
		t.Fatalf("kind = %s, want scheduled", sess.Kind)
	}
	wantBrokerSession(t, broker, sess)
}

func TestIdentityBrokerRetriedCreateOpensOnce(t *testing.T) {
	broker := newRecordingBroker()
	// A create is recognized as a retry only with ownership enforced.
	svc := newIdentityBrokerService(t, broker, mockllm.New(), func(cfg *server.Config) { cfg.OwnershipEnforced = true })
	ctx := aliceContext(t)
	first, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID("retried"))
	if err != nil {
		t.Fatal(err)
	}
	opened := wantBrokerSession(t, broker, first)
	again, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID("retried"))
	if err != nil {
		t.Fatal(err)
	}
	if again.Incarnation() != first.Incarnation() {
		t.Fatalf("retried create returned incarnation %s, want %s", again.Incarnation(), first.Incarnation())
	}
	if got := wantBrokerSession(t, broker, again); got != opened {
		t.Fatalf("retried create left broker session %+v, want %+v", got, opened)
	}
	if n := broker.openCount(); n != 1 {
		t.Fatalf("OpenSession calls = %d, want 1", n)
	}
}

func TestIdentityBrokerSkipsSessionsWithoutAnOwner(t *testing.T) {
	broker := newRecordingBroker()
	svc := newIdentityBrokerService(t, broker, mockllm.New(mockllm.TextTurn("done")))
	sess, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := runToEnd(t.Context(), t, svc, sess.ID); err != nil {
		t.Fatalf("an ownerless session's run: %v", err)
	}
	if n := broker.openCount(); n != 0 {
		t.Fatalf("OpenSession calls for an ownerless session = %d, want 0", n)
	}
}

func TestIdentityBrokerOpensForForkAndClearSuccessors(t *testing.T) {
	broker := newRecordingBroker()
	svc := newIdentityBrokerService(t, broker, mockllm.New())
	ctx := aliceContext(t)
	source, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	sourceBroker := wantBrokerSession(t, broker, source)

	forked, err := svc.ForkSessionSuccessor(ctx, server.ForkSuccessorRequest{Source: source.ID})
	if err != nil {
		t.Fatal(err)
	}
	cleared, err := svc.ClearSessionSuccessor(ctx, source.ID, server.SuccessorPlacement{})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []session.SessionID{forked, cleared} {
		successor, err := svc.GetSession(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got := wantBrokerSession(t, broker, successor); got.ID == sourceBroker.ID {
			t.Fatalf("successor %s shares the source's broker session", id)
		}
	}
	// Clear keeps its source session, so the source keeps its broker session.
	if got, ok := broker.find(t, source.ID); !ok || got != sourceBroker {
		t.Fatalf("source broker session after clear = %+v, %v; want %+v", got, ok, sourceBroker)
	}
}

func TestIdentityBrokerReopensAtRunEntry(t *testing.T) {
	broker := newRecordingBroker()
	svc := newIdentityBrokerService(t, broker, mockllm.New(mockllm.TextTurn("done")))
	ctx := aliceContext(t)
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	first := wantBrokerSession(t, broker, sess)
	// Losing the broker session, as a broker restart does, is repaired before
	// the next run.
	if err := broker.inner.CloseSession(t.Context(), identitybroker.CloseSessionRequest{SessionID: first.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatal(err)
	}
	if err := runToEnd(ctx, t, svc, sess.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := wantBrokerSession(t, broker, sess); got.ID == first.ID {
		t.Fatalf("run entry kept closed broker session %s", got.ID)
	}
}

func TestIdentityBrokerReplacesABrokerSessionLeftForAnEarlierIncarnation(t *testing.T) {
	broker := newRecordingBroker()
	svc := newIdentityBrokerService(t, broker, mockllm.New(mockllm.TextTurn("done")))
	ctx := aliceContext(t)
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	stale := wantBrokerSession(t, broker, sess)
	if err := broker.inner.CloseSession(t.Context(), identitybroker.CloseSessionRequest{SessionID: stale.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatal(err)
	}
	// A broker session for an earlier incarnation of the same host session ID,
	// as a delete whose close failed would leave.
	if _, err := broker.inner.OpenSession(t.Context(), identitybroker.OpenSessionRequest{
		HostSessionID: string(sess.ID), Incarnation: string(session.NewIncarnationID()), Owner: stale.Owner,
	}); err != nil {
		t.Fatal(err)
	}
	if err := runToEnd(ctx, t, svc, sess.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	wantBrokerSession(t, broker, sess)
}

func TestIdentityBrokerUnavailableStopsTheRunNotTheCreate(t *testing.T) {
	broker := newRecordingBroker()
	llm := mockllm.New(mockllm.TextTurn("must not run"))
	svc := newIdentityBrokerService(t, broker, llm)
	ctx := aliceContext(t)
	broker.setUnavailable(true)
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("create with the identity broker down: %v", err)
	}
	if err := runToEnd(ctx, t, svc, sess.ID); !errors.Is(err, server.ErrIdentityBrokerUnavailable) {
		t.Fatalf("run with the identity broker down: %v, want ErrIdentityBrokerUnavailable", err)
	}
	if n := llm.Calls(); n != 0 {
		t.Fatalf("the agent ran without a broker session: %d model requests", n)
	}
	// Once the broker is back, the next run opens the broker session.
	broker.setUnavailable(false)
	if err := runToEnd(ctx, t, svc, sess.ID); err != nil {
		t.Fatalf("run after the identity broker recovered: %v", err)
	}
	wantBrokerSession(t, broker, sess)
}

func TestIdentityBrokerClosesOnDeleteOnly(t *testing.T) {
	broker := newRecordingBroker()
	svc := newIdentityBrokerService(t, broker, mockllm.New())
	ctx := aliceContext(t)
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	wantBrokerSession(t, broker, sess)

	// Releasing the session locally keeps its broker session.
	svc.CloseSession(sess.ID)
	if err := svc.EndSession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	wantBrokerSession(t, broker, sess)
	if n := broker.closeCount(); n != 0 {
		t.Fatalf("CloseSession calls after local release = %d, want 0", n)
	}

	if err := svc.DeleteSession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := broker.find(t, sess.ID); ok {
		t.Fatal("deleting the session left its broker session open")
	}
	if n := broker.closeCount(); n != 1 {
		t.Fatalf("CloseSession calls after delete = %d, want 1", n)
	}
}

// hangingFindBroker blocks FindSession until its context ends, as an
// unresponsive remote broker would.
type hangingFindBroker struct {
	*recordingBroker
	hang bool
	// waiting, when set, receives a value each time a hanging FindSession
	// starts waiting, so a test can act while the call is in flight.
	waiting chan struct{}
}

func (b *hangingFindBroker) FindSession(ctx context.Context, req identitybroker.FindSessionRequest) (identitybroker.Session, bool, error) {
	if b.hang {
		if b.waiting != nil {
			b.waiting <- struct{}{}
		}
		<-ctx.Done()
		// A transport, not the broker, reports a deadline as a plain error.
		return identitybroker.Session{}, false, ctx.Err()
	}
	return b.recordingBroker.FindSession(ctx, req)
}

func TestIdentityBrokerCloseOnDeleteIsBounded(t *testing.T) {
	restore := server.SetEngineCloseTimeoutForTest(50 * time.Millisecond)
	defer restore()
	broker := &hangingFindBroker{recordingBroker: newRecordingBroker()}
	svc := newIdentityBrokerService(t, broker, mockllm.New())
	ctx := aliceContext(t)
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	broker.hang = true
	done := make(chan error, 1)
	go func() { done <- svc.DeleteSession(ctx, sess.ID) }()
	select {
	case err := <-done:
		// The record is gone; the failed close is logged, not returned.
		if err != nil {
			t.Fatalf("DeleteSession with a hung identity broker: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("DeleteSession waited on a hung identity broker")
	}
}

func TestIdentityBrokerOffChangesNothing(t *testing.T) {
	svc := newIdentityBrokerService(t, nil, mockllm.New(mockllm.TextTurn("done")))
	ctx := aliceContext(t)
	run, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := runToEnd(ctx, t, svc, run.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	deleted, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteSession(ctx, deleted.ID); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityBrokerDeadlineAtRunEntryIsUnavailable(t *testing.T) {
	broker := &hangingFindBroker{recordingBroker: newRecordingBroker()}
	llm := mockllm.New(mockllm.TextTurn("must not run"))
	svc := newIdentityBrokerService(t, broker, llm)
	sess, err := svc.CreateSession(aliceContext(t), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	broker.hang = true
	ctx, cancel := context.WithTimeout(aliceContext(t), 50*time.Millisecond)
	defer cancel()
	if _, err := svc.StartRun(ctx, sess.ID, "hello"); !errors.Is(err, server.ErrIdentityBrokerUnavailable) {
		t.Fatalf("run with an unresponsive identity broker: %v, want ErrIdentityBrokerUnavailable", err)
	}
	if n := llm.Calls(); n != 0 {
		t.Fatalf("the agent ran without a broker session: %d model requests", n)
	}
}

func TestIdentityBrokerRetentionDeleteCloses(t *testing.T) {
	broker := newRecordingBroker()
	svc := newIdentityBrokerService(t, broker, mockllm.New())
	sess, err := svc.CreateSession(aliceContext(t), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	wantBrokerSession(t, broker, sess)
	if err := svc.DeleteSessionForRetention(t.Context(), sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := broker.find(t, sess.ID); ok {
		t.Fatal("a retention delete left the session's broker session open")
	}
}

func TestIdentityBrokerEndScheduledRunCloses(t *testing.T) {
	broker := newRecordingBroker()
	svc := newIdentityBrokerService(t, broker, mockllm.New())
	sess, err := svc.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault,
		server.WithSessionID("fire-2"), server.WithOwner(brokerAlice), server.WithScheduledRelationship("nightly", ""))
	if err != nil {
		t.Fatal(err)
	}
	wantBrokerSession(t, broker, sess)
	svc.EndScheduledRun(t.Context(), sess)
	if _, ok := broker.find(t, sess.ID); ok {
		t.Fatal("the scheduled run ended but its broker session is open")
	}
	if _, err := svc.GetSession(t.Context(), sess.ID); err != nil {
		t.Fatalf("the scheduled session was not kept for result delivery: %v", err)
	}
}

func TestIdentityBrokerTeamRunOpensTheRootSession(t *testing.T) {
	for _, down := range []bool{false, true} {
		name := "available"
		if down {
			name = "unavailable"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := memstore.New()
			ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
			env := tool.MustEnvironment(ref, memfs.NewWorkspace("/ws"), memledger.New(), nil)
			provider := &teamRootCaptureProvider{}
			memberEngine := func(*team.Team, agent.MemberSpec, string) agent.MemberBuild {
				return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{LLM: provider, Catalog: tool.NewCatalog(), Model: "test"})}
			}
			broker := newRecordingBroker()
			svc, err := server.NewService(server.Config{
				Engine: agent.NewEngine(agent.Deps{Catalog: tool.NewCatalog()}), Store: store,
				PlacementProvider: delegationPlacementProvider{binding: server.PlacementBinding{Environment: env, Ref: ref}},
				PlacementScope:    "test-scope", MemberEngine: memberEngine, IdentityBroker: broker,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(svc.Close)
			// The root session as a restart leaves it: saved, with no broker session.
			source := session.New("team-root", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
			if err := source.RestoreLabels(brokerAlice, session.Authority{}); err != nil {
				t.Fatal(err)
			}
			if err := store.Create(ctx, source); err != nil {
				t.Fatal(err)
			}
			teamID, _, err := svc.CreateTeamForSession(ctx, source.ID, "reviewers", "goal", 0, []agent.MemberSpec{{Name: "lead", Lead: true}})
			if err != nil {
				t.Fatal(err)
			}
			broker.setUnavailable(down)
			_, err = svc.RunTeam(ctx, teamID, nil)
			if down {
				if !errors.Is(err, server.ErrIdentityBrokerUnavailable) {
					t.Fatalf("RunTeam with the identity broker down: %v, want ErrIdentityBrokerUnavailable", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantRegistered(t, broker, wantBrokerSession(t, broker, source), source)
		})
	}
}
