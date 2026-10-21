package server_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/standin"
)

var brokerAlice = &session.Principal{Issuer: "https://login.example.com", Subject: "alice-204", GrantType: session.GrantTypeUser}

// recordingBroker wraps the stand-in broker, records every call, and can make
// every call fail as unavailable.
type recordingBroker struct {
	inner identitybroker.Service

	mu          sync.Mutex
	opens       []identitybroker.OpenSessionRequest
	closes      []identitybroker.CloseSessionRequest
	unavailable bool
}

func newRecordingBroker() *recordingBroker {
	return &recordingBroker{inner: identitybroker.NewInProcess(identitybroker.UnverifiedAuthenticator{Name: "mecak8s-test"}, standin.New())}
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
	b.mu.Unlock()
	return b.inner.OpenSession(ctx, req)
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
	return b.inner.RegisterAgent(ctx, req)
}

func (b *recordingBroker) AuthorizeCall(ctx context.Context, req identitybroker.AuthorizeCallRequest) (identitybroker.Authorization, error) {
	return b.inner.AuthorizeCall(ctx, req)
}

func (b *recordingBroker) openCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.opens)
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
		HostSessionID: string(sess.ID), Incarnation: "inc_earlier", Owner: stale.Owner,
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
}

func (b *hangingFindBroker) FindSession(ctx context.Context, req identitybroker.FindSessionRequest) (identitybroker.Session, bool, error) {
	if b.hang {
		<-ctx.Done()
		return identitybroker.Session{}, false, identitybroker.NewError(identitybroker.CodeUnavailable, "", ctx.Err().Error())
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
