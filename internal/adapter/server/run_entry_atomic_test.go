package server_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type loadBarrierStore struct {
	inner       *memstore.Store
	mu          sync.Mutex
	loads       int
	firstLoad   chan struct{}
	secondLoad  chan struct{}
	releaseLoad chan struct{}
}

func (s *loadBarrierStore) Save(ctx context.Context, sess *session.Session) error {
	return s.inner.Save(ctx, sess)
}

func (s *loadBarrierStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	s.mu.Lock()
	s.loads++
	load := s.loads
	s.mu.Unlock()
	switch load {
	case 1:
		close(s.firstLoad)
		select {
		case <-s.releaseLoad:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	case 2:
		close(s.secondLoad)
	}
	return s.inner.Load(ctx, id)
}

func TestADR_0108_RunEntryLocksLoadAuthorizePurposeAndReopen(t *testing.T) {
	ctx := context.Background()
	base := memstore.New()
	sess := session.New("same-id", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := sess.Reopen(); err == nil {
		t.Fatal("idle fixture unexpectedly reopened")
	}
	if err := base.Save(ctx, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	store := &loadBarrierStore{
		inner: base, firstLoad: make(chan struct{}), secondLoad: make(chan struct{}), releaseLoad: make(chan struct{}),
	}
	providerRelease := make(chan struct{})
	eng := agent.NewEngine(agent.Deps{
		LLM: mockllm.NewWith([]mockllm.Option{
			mockllm.WithRequestObserver(func(port.LLMRequest) { <-providerRelease }),
		}, mockllm.TextTurn("first")),
		Catalog: tool.NewCatalog(), Model: "test",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: eng, Store: store,
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	type result struct {
		run *agent.Run
		err error
	}
	results := make(chan result, 2)
	go func() {
		run, startErr := svc.StartRunContent(ctx, sess.ID, "first", nil)
		results <- result{run: run, err: startErr}
	}()
	<-store.firstLoad
	go func() {
		run, startErr := svc.StartRunContent(ctx, sess.ID, "second", nil)
		results <- result{run: run, err: startErr}
	}()

	select {
	case <-store.secondLoad:
		t.Fatal("second same-id start loaded before the first completed atomic run entry")
	case <-time.After(100 * time.Millisecond):
	}
	close(store.releaseLoad)

	var successfulRun *agent.Run
	var successes int
	got := []result{<-results, <-results}
	for _, res := range got {
		if res.err == nil {
			successes++
			successfulRun = res.run
			continue
		}
		if !errors.Is(res.err, server.ErrFailedPrecondition) {
			close(providerRelease)
			t.Fatalf("duplicate start error = %v, want failed precondition", res.err)
		}
	}
	if successes != 1 {
		close(providerRelease)
		t.Fatalf("successful same-id starts = %d, want exactly 1", successes)
	}
	close(providerRelease)
	for range successfulRun.Events() {
	}
	svc.FinishRun(sess.ID, successfulRun)
}

type ownershipRunEntryStore struct {
	inner *memstore.Store

	mu         sync.Mutex
	aliceLoads int
	saves      int

	aliceAuthoritative    chan struct{}
	releaseAlice          chan struct{}
	releaseOnce           sync.Once
	authoritativeMutation func(context.Context) error
}

func (s *ownershipRunEntryStore) Save(ctx context.Context, sess *session.Session) error {
	s.mu.Lock()
	s.saves++
	s.mu.Unlock()
	return s.inner.Save(ctx, sess)
}

func (s *ownershipRunEntryStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	principal := session.PrincipalFromContext(ctx)
	if principal != nil && principal.Subject == "alice" {
		s.mu.Lock()
		s.aliceLoads++
		load := s.aliceLoads
		s.mu.Unlock()
		if load == 2 {
			if s.authoritativeMutation != nil {
				if err := s.authoritativeMutation(ctx); err != nil {
					return nil, err
				}
			}
			close(s.aliceAuthoritative)
			select {
			case <-s.releaseAlice:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	return s.inner.Load(ctx, id)
}

func (s *ownershipRunEntryStore) release() {
	s.releaseOnce.Do(func() { close(s.releaseAlice) })
}

func (s *ownershipRunEntryStore) saveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves
}

func TestCallerSeparation_ForeignPromptDoesNotContendOnOwnerRunEntry(t *testing.T) {
	base := memstore.New()
	alice := &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser}
	bob := &session.Principal{Issuer: "https://idp.example", Subject: "bob", GrantType: session.GrantTypeUser}
	aliceBaseCtx, cancelAlice := context.WithCancel(context.Background())
	t.Cleanup(cancelAlice)
	aliceCtx := session.WithPrincipal(aliceBaseCtx, alice)
	bobCtx := session.WithPrincipal(context.Background(), bob)
	sess := session.New("alice-session", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := sess.RestoreLabels(alice, session.Authority{}); err != nil {
		t.Fatalf("RestoreLabels: %v", err)
	}
	if err := base.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save fixture: %v", err)
	}
	store := &ownershipRunEntryStore{
		inner:              base,
		aliceAuthoritative: make(chan struct{}),
		releaseAlice:       make(chan struct{}),
	}
	lease := &fakeLease{}
	eng := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("done")),
		Catalog: tool.NewCatalog(),
		Model:   "test",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: eng, Store: store,
		Workspaces:         func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		OwnershipEnforced:  true,
		SessionLease:       lease,
		LeaseOwner:         "test-server",
		LeaseTTL:           time.Hour,
		LeaseRenewInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	t.Cleanup(store.release)

	type result struct {
		run *agent.Run
		err error
	}
	aliceResult := make(chan result, 1)
	go func() {
		run, startErr := svc.StartRunContent(aliceCtx, sess.ID, "owner prompt", nil)
		aliceResult <- result{run: run, err: startErr}
	}()
	select {
	case <-store.aliceAuthoritative:
	case <-time.After(time.Second):
		t.Fatal("owner prompt did not reach the authoritative under-lock load")
	}

	before, err := base.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("Load before foreign prompt: %v", err)
	}
	foreignResult := make(chan error, 1)
	go func() {
		_, startErr := svc.StartRunContent(bobCtx, sess.ID, "foreign prompt", nil)
		foreignResult <- startErr
	}()
	select {
	case err := <-foreignResult:
		if !errors.Is(err, server.ErrNotFound) {
			t.Fatalf("foreign prompt error = %v, want ErrNotFound", err)
		}
	case <-time.After(time.Second):
		t.Fatal("foreign prompt contended on the owner's run-entry lock")
	}
	if _, err := svc.StartRunContent(bobCtx, "missing-session", "missing prompt", nil); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("missing prompt error = %v, want ErrNotFound", err)
	}
	if lease.acquires != 0 {
		t.Fatalf("lease acquires before owner release = %d, want 0", lease.acquires)
	}
	if got := store.saveCount(); got != 0 {
		t.Fatalf("store saves from foreign prompt = %d, want 0", got)
	}
	if _, ok := svc.LookupRun(sess.ID); ok {
		t.Fatal("foreign prompt registered a run")
	}
	after, err := base.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("Load after foreign prompt: %v", err)
	}
	if after.State != before.State || len(after.Conversation.Messages) != len(before.Conversation.Messages) {
		t.Fatalf("foreign prompt mutated snapshot: before=%s/%d after=%s/%d", before.State, len(before.Conversation.Messages), after.State, len(after.Conversation.Messages))
	}

	store.release()
	owner := <-aliceResult
	if owner.err != nil {
		t.Fatalf("owner prompt: %v", owner.err)
	}
	for range owner.run.Events() {
	}
	svc.FinishRun(sess.ID, owner.run)
}

func TestCallerSeparation_RunEntryReloadReauthorizesAfterPreflight(t *testing.T) {
	alice := &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser}
	bob := &session.Principal{Issuer: "https://idp.example", Subject: "bob", GrantType: session.GrantTypeUser}

	tests := []struct {
		name   string
		mutate func(context.Context, *memstore.Store, session.SessionID) error
	}{
		{
			name: "deleted",
			mutate: func(ctx context.Context, store *memstore.Store, id session.SessionID) error {
				return store.Delete(ctx, id)
			},
		},
		{
			name: "owner replaced",
			mutate: func(ctx context.Context, store *memstore.Store, id session.SessionID) error {
				replacement := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(2, 0))
				if err := replacement.RestoreLabels(bob, session.Authority{}); err != nil {
					return err
				}
				return store.Save(ctx, replacement)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := memstore.New()
			sess := session.New("alice-session", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
			if err := sess.RestoreLabels(alice, session.Authority{}); err != nil {
				t.Fatalf("RestoreLabels: %v", err)
			}
			if err := base.Save(context.Background(), sess); err != nil {
				t.Fatalf("Save fixture: %v", err)
			}
			store := &ownershipRunEntryStore{
				inner:              base,
				aliceAuthoritative: make(chan struct{}),
				releaseAlice:       make(chan struct{}),
			}
			store.authoritativeMutation = func(ctx context.Context) error {
				return tt.mutate(ctx, base, sess.ID)
			}
			lease := &fakeLease{}
			svc, err := newPlacementTestService(server.Config{
				Engine:             agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("unexpected")), Catalog: tool.NewCatalog(), Model: "test"}),
				Store:              store,
				Workspaces:         func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
				OwnershipEnforced:  true,
				SessionLease:       lease,
				LeaseOwner:         "test-server",
				LeaseTTL:           time.Hour,
				LeaseRenewInterval: time.Hour,
			})
			if err != nil {
				t.Fatalf("NewService: %v", err)
			}
			t.Cleanup(svc.Close)
			t.Cleanup(store.release)

			ctx, cancel := context.WithCancel(session.WithPrincipal(context.Background(), alice))
			t.Cleanup(cancel)
			result := make(chan error, 1)
			go func() {
				_, startErr := svc.StartRunContent(ctx, sess.ID, "owner prompt", nil)
				result <- startErr
			}()
			select {
			case <-store.aliceAuthoritative:
			case <-time.After(time.Second):
				t.Fatal("owner prompt did not reach the authoritative under-lock load")
			}
			store.release()
			if err := <-result; !errors.Is(err, server.ErrNotFound) {
				t.Fatalf("StartRunContent after %s = %v, want ErrNotFound", tt.name, err)
			}
			if lease.acquires != 0 {
				t.Fatalf("lease acquires after %s = %d, want 0", tt.name, lease.acquires)
			}
			if _, ok := svc.LookupRun(sess.ID); ok {
				t.Fatalf("run registered after %s", tt.name)
			}
		})
	}
}

var _ port.SessionStore = (*loadBarrierStore)(nil)
var _ port.SessionStore = (*ownershipRunEntryStore)(nil)
