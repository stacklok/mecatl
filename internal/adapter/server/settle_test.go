package server_test

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/syscaller"
)

// crashOrphanedSession builds a StateRunning session whose trailing assistant
// message carries dangling tool_use calls that never got results — the exact
// shape a crash leaves behind (mirrors
// engine/session.TestAbandonFromRunningClosesOutOrphansAndIdles).
func crashOrphanedSession(t *testing.T, id session.SessionID) *session.Session {
	t.Helper()
	sess := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/tmp/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	if err := sess.RecordUserPrompt("do two things", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := sess.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	calls := []session.ToolCall{
		session.NewToolCall("call-a", "read_file", nil),
		session.NewToolCall("call-b", "list_dir", nil),
	}
	if err := sess.RecordAssistant(session.NewAssistantMessage("", "", calls)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	if sess.State != session.StateRunning {
		t.Fatalf("precondition: state = %q, want running", sess.State)
	}
	return sess
}

// TestSettleIfStaleAbandonsRunningSessionThenNoOps pins SettleIfStale's write
// path directly (issue #475): a crash-orphaned running snapshot on disk with
// an unpaired tool_use gets repaired to idle with valid tool pairing, and a
// second call against the now-idle id is an honest no-op.
func TestSettleIfStaleAbandonsRunningSessionThenNoOps(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	const id session.SessionID = "crashed-orphan"
	if err := store.Save(ctx, crashOrphanedSession(t, id)); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	svc := newSettleService(t, store)

	staleCtx := syscaller.Context(ctx, syscaller.RootStaleSessionReconcile)
	settled, err := svc.SettleIfStale(staleCtx, id)
	if err != nil {
		t.Fatalf("SettleIfStale: %v", err)
	}
	if !settled {
		t.Fatal("SettleIfStale on a crash-orphaned running session = false, want true")
	}

	reloaded, err := store.Load(ctx, id)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.State != session.StateIdle {
		t.Fatalf("reloaded state = %q, want idle", reloaded.State)
	}
	if err := session.ValidateToolPairing(reloaded.Conversation.Messages); err != nil {
		t.Fatalf("reloaded history fails tool pairing: %v", err)
	}

	// Second call: already idle, nothing to settle, no error.
	settled, err = svc.SettleIfStale(staleCtx, id)
	if err != nil {
		t.Fatalf("second SettleIfStale: %v", err)
	}
	if settled {
		t.Fatal("second SettleIfStale on an already-idle session = true, want false")
	}
}

// TestSettleIfStaleSkipsGenuinelyLiveSession pins the A2 guard: even when the
// on-disk snapshot looks stale (StateRunning), a session id that is locally
// live (IsLive==true — a real run registered in the gap between the sweep's
// staleness decision and this call) must not be abandoned.
func TestSettleIfStaleSkipsGenuinelyLiveSession(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	svc := newSettleService(t, store)

	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(ctx, sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	t.Cleanup(func() {
		run.Cancel()
		for range run.Events() {
		}
		svc.FinishRun(sess.ID, run)
	})
	if !svc.IsLive(sess.ID) {
		t.Fatal("precondition: IsLive after StartRun = false, want true")
	}

	// Overwrite the on-disk snapshot with a stale-looking crash-orphan shape
	// for the SAME id, as if a metadata scan saw this before the run started.
	orphan := crashOrphanedSession(t, sess.ID)
	if err := store.Save(ctx, orphan); err != nil {
		t.Fatalf("overwrite Save: %v", err)
	}

	settled, err := svc.SettleIfStale(syscaller.Context(ctx, syscaller.RootStaleSessionReconcile), sess.ID)
	if err != nil {
		t.Fatalf("SettleIfStale: %v", err)
	}
	if settled {
		t.Fatal("SettleIfStale on a genuinely live session = true, want false (must not abandon)")
	}

	reloaded, err := store.Load(ctx, sess.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.State != session.StateRunning {
		t.Fatalf("reloaded state = %q, want running (no write should have happened)", reloaded.State)
	}
}

// newSettleService builds a minimal Service over the given store, with no
// lease wired — SettleIfStale's IsLive guard is exercised via the local run
// registry only.
func newSettleService(t *testing.T, store *memstore.Store) *server.Service {
	t.Helper()
	ps := permstore.New()
	cat := tool.NewCatalog()
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("ok")),
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, ps),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:     engine,
		Store:      store,
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}
