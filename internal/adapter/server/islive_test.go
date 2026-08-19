package server_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestServiceIsLiveTracksRunRegistry pins the IsLive contract the composition
// layer's child-session GC relies on: false for an unknown id, false for a
// created-but-runless session, true from StartRunContent (registration is
// synchronous) until FinishRun, false after.
func TestServiceIsLiveTracksRunRegistry(t *testing.T) {
	svc := newService(t, mockllm.New(mockllm.TextTurn("ok")), allowRules())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if svc.IsLive("never-seen") {
		t.Error("IsLive(unknown id) = true, want false")
	}
	sess, err := svc.CreateSession(ctx, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if svc.IsLive(sess.ID) {
		t.Error("IsLive(created, runless session) = true, want false")
	}

	run, err := svc.StartRunContent(ctx, sess.ID, "go", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	if !svc.IsLive(sess.ID) {
		t.Error("IsLive(mid-run session) = false, want true")
	}
	for range run.Events() {
		// drain to the terminal; the run stays registered until FinishRun
	}
	if !svc.IsLive(sess.ID) {
		t.Error("IsLive(drained, not yet FinishRun) = false, want true (the registry, not the run state, is the source)")
	}
	svc.FinishRun(sess.ID, run)
	if svc.IsLive(sess.ID) {
		t.Error("IsLive(after FinishRun) = true, want false")
	}
}

type testLiveness struct {
	mu     sync.Mutex
	active map[session.SessionID]int
}

func (r *testLiveness) Register(_ context.Context, id session.SessionID, _ context.CancelFunc) (func(), error) {
	r.mu.Lock()
	if r.active == nil {
		r.active = make(map[session.SessionID]int)
	}
	r.active[id]++
	r.mu.Unlock()
	return sync.OnceFunc(func() {
		r.mu.Lock()
		if r.active[id] <= 1 {
			delete(r.active, id)
		} else {
			r.active[id]--
		}
		r.mu.Unlock()
	}), nil
}

func (r *testLiveness) IsLive(id session.SessionID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active[id] > 0
}

func TestServiceIsLiveIncludesEngineChildren(t *testing.T) {
	tracker := &testLiveness{}
	llm := mockllm.New(mockllm.TextTurn("ok"))
	engine := agent.NewEngine(agent.Deps{
		LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(allowRules(), nil), Model: "test",
	})
	svc, err := server.NewService(server.Config{
		Engine: engine, Store: memstore.New(), SessionLiveness: tracker,
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	for _, child := range []session.SessionID{"subagent-child", "parallel-child-0", "team-child-lead"} {
		release, err := tracker.Register(context.Background(), child, func() {})
		if err != nil {
			t.Fatalf("Register(%q): %v", child, err)
		}
		if !svc.IsLive(child) {
			t.Fatalf("IsLive(%q) = false for registered engine child", child)
		}
		release()
		release()
		if svc.IsLive(child) {
			t.Fatalf("IsLive(%q) remained true after terminal release", child)
		}
	}
}
