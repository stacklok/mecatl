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
	sess := session.New("same-id", session.ModeDefault, "/workspace", session.Limits{}, time.Unix(1, 0))
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
	svc, err := server.NewService(server.Config{
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

var _ port.SessionStore = (*loadBarrierStore)(nil)
