package server_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

// lostDeleteStore deletes a session but reports it as already gone, as a
// delete whose response was lost and then retried would.
type lostDeleteStore struct{ *memstore.Store }

func (s lostDeleteStore) Delete(ctx context.Context, id session.SessionID) error {
	if err := s.Store.Delete(ctx, id); err != nil {
		return err
	}
	return port.ErrSessionNotFound
}

func withLostDeletes(cfg *server.Config) {
	cfg.Store = lostDeleteStore{Store: cfg.Store.(*memstore.Store)}
}

func TestIdentityBrokerAlreadyDeletedBranchesClose(t *testing.T) {
	deletes := map[string]func(*server.Service, context.Context, session.SessionID) error{
		"DeleteSession": func(svc *server.Service, ctx context.Context, id session.SessionID) error {
			return svc.DeleteSession(ctx, id)
		},
		"DeleteSessionForRetention": func(svc *server.Service, ctx context.Context, id session.SessionID) error {
			return svc.DeleteSessionForRetention(ctx, id)
		},
	}
	for name, del := range deletes {
		t.Run(name, func(t *testing.T) {
			broker := newRecordingBroker()
			svc := newIdentityBrokerService(t, broker, mockllm.New(), withLostDeletes)
			ctx := aliceContext(t)
			sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			wantBrokerSession(t, broker, sess)
			if err := del(svc, ctx, sess.ID); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if _, ok := broker.find(t, sess.ID); ok {
				t.Fatalf("%s took the already-deleted branch and left the broker session open", name)
			}
		})
	}
}

func TestIdentityBrokerRetentionCandidateDeleteCloses(t *testing.T) {
	broker := newRecordingBroker()
	var store *memstore.Store
	svc := newIdentityBrokerService(t, broker, mockllm.New(), func(cfg *server.Config) {
		store = cfg.Store.(*memstore.Store)
		cfg.LocalStorageMaintenanceSingleWriter = true
	})
	sess, err := svc.CreateSession(aliceContext(t), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	wantBrokerSession(t, broker, sess)
	page, err := store.PageSessionMetadata(t.Context(), port.SessionMetadataPageRequest{Limit: 10})
	if err != nil || len(page.Sessions) != 1 {
		t.Fatalf("metadata page = %+v, %v", page, err)
	}
	if err := svc.DeleteSessionForRetentionCandidate(t.Context(), page.Sessions[0]); err != nil {
		t.Fatal(err)
	}
	if _, ok := broker.find(t, sess.ID); ok {
		t.Fatal("a retention-candidate delete left the session's broker session open")
	}
}

// referencePlacement adds a reference lifecycle whose confirmation fails once,
// and execution access that applies to every placement, so deletes prepare and
// confirm a reference.
type referencePlacement struct {
	testPlacementProvider
	failConfirm *atomic.Bool
}

func (referencePlacement) Applies(session.EnvironmentRef) bool { return true }

func (p referencePlacement) PrepareReferenceDelete(context.Context, server.PlacementSuccessorRequest) (server.ReferenceDeleteHandle, error) {
	return referenceHandle(p), nil
}

type referenceHandle referencePlacement

func (h referenceHandle) Confirm(context.Context) error {
	if h.failConfirm.CompareAndSwap(true, false) {
		return errors.New("injected reference confirmation failure")
	}
	return nil
}

func (referenceHandle) Cancel(context.Context) error { return nil }

func (referencePlacement) AcquireRun(context.Context, server.ExecutionRunRequest) (server.ExecutionRunHandle, error) {
	return nil, errors.New("runs are not used in this test")
}

func TestIdentityBrokerClosesEvenWhenReferenceConfirmationFails(t *testing.T) {
	broker := newRecordingBroker()
	failConfirm := &atomic.Bool{}
	svc := newIdentityBrokerService(t, broker, mockllm.New(), func(cfg *server.Config) {
		placement := referencePlacement{
			testPlacementProvider: testPlacementProvider{root: "/ws", firstBind: &atomic.Bool{}},
			failConfirm:           failConfirm,
		}
		cfg.SharedEngineRoot, cfg.PlacementProvider, cfg.PlacementScope = "/ws", placement, "test"
		cfg.ExecutionAccess = placement
	})
	ctx := aliceContext(t)
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	wantBrokerSession(t, broker, sess)
	if _, held := svc.HeldIdentityCredentialForTest(sess.ID); !held {
		t.Fatal("no credential is held after create")
	}
	failConfirm.Store(true)
	if err := svc.DeleteSession(ctx, sess.ID); err == nil {
		t.Fatal("DeleteSession succeeded although reference confirmation failed")
	}
	// The record is gone, so a retry finds nothing to delete and never reaches
	// the cleanup: the broker session must already be closed and the
	// credential dropped.
	if _, ok := broker.find(t, sess.ID); ok {
		t.Fatal("a failed reference confirmation left the broker session open")
	}
	if _, held := svc.HeldIdentityCredentialForTest(sess.ID); held {
		t.Fatal("a failed reference confirmation left the agent's credential held")
	}
	if err := svc.DeleteSession(ctx, sess.ID); err != nil {
		t.Fatalf("retried DeleteSession: %v", err)
	}
}

func TestIdentityBrokerSessionOwnedByAnotherUserStopsTheRun(t *testing.T) {
	broker := newRecordingBroker()
	llm := mockllm.New(mockllm.TextTurn("must not run"))
	svc := newIdentityBrokerService(t, broker, llm)
	ctx := aliceContext(t)
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	alices := wantBrokerSession(t, broker, sess)
	if err := broker.inner.CloseSession(t.Context(), identitybroker.CloseSessionRequest{SessionID: alices.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatal(err)
	}
	// A broker session for the same host session under another owner: the
	// design treats session_owned as a pairing bug that fails the open.
	bobs, err := broker.inner.OpenSession(t.Context(), identitybroker.OpenSessionRequest{
		HostSessionID: string(sess.ID), Incarnation: string(sess.Incarnation()),
		Owner: identitybroker.Owner{Issuer: brokerAlice.Issuer, Subject: "bob-117"},
	})
	if err != nil {
		t.Fatal(err)
	}
	closesBefore := broker.closeCount()
	err = runToEnd(ctx, t, svc, sess.ID)
	if !errors.Is(err, server.ErrIdentityBrokerSessionRefused) || errors.Is(err, server.ErrIdentityBrokerUnavailable) {
		t.Fatalf("run against another owner's broker session: %v, want ErrIdentityBrokerSessionRefused", err)
	}
	if n := llm.Calls(); n != 0 {
		t.Fatalf("the agent ran without its broker session: %d model requests", n)
	}
	if broker.closeCount() != closesBefore {
		t.Fatal("the server closed another owner's broker session")
	}
	if got, ok := broker.find(t, sess.ID); !ok || got != bobs {
		t.Fatalf("other owner's broker session = %+v, %v; want %+v left open", got, ok, bobs)
	}
}

func TestIdentityBrokerConcurrentRetriedCreatesOpenForTheSavedSession(t *testing.T) {
	broker := newRecordingBroker()
	svc := newIdentityBrokerService(t, broker, mockllm.New(), func(cfg *server.Config) { cfg.OwnershipEnforced = true })
	ctx := aliceContext(t)
	const creates = 16
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range creates {
		wg.Go(func() {
			<-start
			// A racing duplicate either returns the saved session or is refused
			// while the first create holds the ID; either way only the saved
			// session's incarnation may be opened.
			_, _ = svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID("raced"))
		})
	}
	close(start)
	wg.Wait()
	saved, err := svc.GetSession(ctx, "raced")
	if err != nil {
		t.Fatal(err)
	}
	wantBrokerSession(t, broker, saved)
	broker.mu.Lock()
	defer broker.mu.Unlock()
	for _, open := range broker.opens {
		if open.Incarnation != string(saved.Incarnation()) {
			t.Fatalf("an open used incarnation %s, want only the saved session's %s", open.Incarnation, saved.Incarnation())
		}
	}
}

func TestIdentityBrokerCancelledRunEntryIsACancellation(t *testing.T) {
	broker := &hangingFindBroker{recordingBroker: newRecordingBroker(), waiting: make(chan struct{})}
	svc := newIdentityBrokerService(t, broker, mockllm.New(mockllm.TextTurn("must not run")))
	sess, err := svc.CreateSession(aliceContext(t), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	broker.hang = true
	ctx, cancel := context.WithCancel(aliceContext(t))
	// Cancel only once run entry is waiting on the broker, so the cancellation
	// reaches the broker call rather than an earlier step.
	go func() {
		<-broker.waiting
		cancel()
	}()
	_, err = svc.StartRun(ctx, sess.ID, "hello")
	if !errors.Is(err, context.Canceled) || errors.Is(err, server.ErrInternal) {
		t.Fatalf("run entry cancelled by the caller: %v, want context.Canceled, not ErrInternal", err)
	}
}

func TestIdentityBrokerEndScheduledRunIgnoresOtherSessions(t *testing.T) {
	broker := newRecordingBroker()
	svc := newIdentityBrokerService(t, broker, mockllm.New())
	sess, err := svc.CreateSession(aliceContext(t), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	opened := wantBrokerSession(t, broker, sess)
	svc.EndScheduledRun(t.Context(), sess)
	if got, ok := broker.find(t, sess.ID); !ok || got != opened {
		t.Fatalf("EndScheduledRun on an interactive session: broker session = %+v, %v; want %+v left open", got, ok, opened)
	}
}
