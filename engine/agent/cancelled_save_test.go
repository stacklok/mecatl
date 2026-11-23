package agent_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// saveDeadlineStore records, for every Save after it is armed, how far away
// the context's deadline is (or that it has none).
type saveDeadlineStore struct {
	*memstore.Store
	armed atomic.Bool
	mu    sync.Mutex
	saves []time.Duration // -1 when the context had no deadline
}

func (s *saveDeadlineStore) Save(ctx context.Context, sess *session.Session) error {
	if s.armed.Load() {
		remaining := time.Duration(-1)
		if dl, ok := ctx.Deadline(); ok {
			remaining = time.Until(dl)
		}
		s.mu.Lock()
		s.saves = append(s.saves, remaining)
		s.mu.Unlock()
	}
	return s.Store.Save(ctx, sess)
}

// TestCancelledReadBatchSaveIsBounded pins that the save of a cancelled read
// batch's results, like every other save of a cancelled run, has a context
// bounded by the persistence timeout: a store that hangs must not hold the
// run open.
func TestCancelledReadBatchSaveIsBounded(t *testing.T) {
	store := &saveDeadlineStore{Store: memstore.New()}
	quick := &fakeTool{name: "QuickRead", readOnly: true, exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		return session.NewToolResult(in.ID, "ok"), nil
	}}
	park := newNamedParkTool("WaitRead")
	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c0", "QuickRead", `{}`), toolCall("c1", "WaitRead", `{}`)),
		mockllm.TextTurn("done"),
	)
	engine := agent.NewEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, quick, park), Policy: allowAll(), Model: "test", Store: store})
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
	sess := session.New("cancelled-batch", session.ModeYolo, ref, session.Limits{}, time.Now())
	env := tool.MustEnvironment(ref, memfs.NewWorkspace("/ws"), memledger.New(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := engine.Run(ctx, sess, env, agent.RunRequest{Text: "go"})
	closed := make(chan struct{})
	go func() {
		for range run.Events() {
		}
		close(closed)
	}()
	<-park.started
	store.armed.Store(true)
	cancel()
	run.Cancel()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("run did not end after cancellation")
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	// The cancelled batch's results are saved, then terminate saves again.
	if len(store.saves) < 2 {
		t.Fatalf("saves after the cancel = %d, want the batch save and the terminal save", len(store.saves))
	}
	for i, remaining := range store.saves {
		switch {
		case remaining < 0:
			t.Errorf("save #%d after the cancel has no deadline; want it bounded by the 5s persistence timeout", i+1)
		case remaining > 5*time.Second:
			t.Errorf("save #%d after the cancel: deadline in %v, want at most the 5s persistence timeout", i+1, remaining)
		}
	}
}
