package server_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// newMCPService builds a server.Service with a configurable SessionEngine factory,
// a shared engine that streams sharedReply, and the in-memory store/workspace.
func newMCPService(t *testing.T, sharedReply string, factory server.SessionEngineFactory) *server.Service {
	t.Helper()
	svc, _ := newMCPServiceStore(t, sharedReply, factory)
	return svc
}

// newMCPServiceStore is newMCPService that also returns the backing store, so a test
// can pre-persist a session (e.g. a completed one) for the LoadSessionWithMCP path.
func newMCPServiceStore(t *testing.T, sharedReply string, factory server.SessionEngineFactory) (*server.Service, *memstore.Store) {
	t.Helper()
	shared := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn(sharedReply)),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	store := memstore.New()
	svc, err := newPlacementTestService(server.Config{
		Engine:        shared,
		Store:         store,
		Workspaces:    func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		DefaultLimits: session.Limits{MaxTurns: 10, MaxToolCalls: 20},
		Now:           func() time.Time { return time.Unix(0, 0) },
		SessionEngine: factory,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc, store
}

// persistCompleted creates a session via the store, drives it to a completed
// terminal state, and re-persists it — so LoadSession*/LoadSessionWithMCP exercise
// the reopen-if-completed path. It returns the session id.
func persistCompleted(t *testing.T, store *memstore.Store) session.SessionID {
	t.Helper()
	sess := session.New("sess-1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 3}, time.Unix(0, 0))
	if err := sess.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := sess.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return sess.ID
}

// finalText drains a run and returns the terminal result text.
func finalText(t *testing.T, run *agent.Run) string {
	t.Helper()
	var text string
	for ev := range run.Events() {
		if ev.Type == session.EvResult && ev.Result != nil {
			text = ev.Result.Text
		}
	}
	return text
}

// TestCreateSessionWithMCPEmptySpecsSharedEngine asserts that with no MCP specs the
// session uses the SHARED engine and the per-session factory is NOT called.
func TestCreateSessionWithMCPEmptySpecsSharedEngine(t *testing.T) {
	var called atomic.Int32
	factory := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		called.Add(1)
		return server.SessionEngineResult{Close: func() error { return nil }}, nil
	}
	svc := newMCPService(t, "shared reply", factory)

	sess, err := svc.CreateSessionWithMCP(context.Background(), "/ws", session.ModeDefault, session.Limits{}, nil)
	if err != nil {
		t.Fatalf("CreateSessionWithMCP: %v", err)
	}
	if called.Load() != 0 {
		t.Fatalf("factory called %d times for empty specs, want 0", called.Load())
	}

	run, err := svc.StartRun(context.Background(), sess.ID, "hi")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := finalText(t, run); !strings.Contains(got, "shared reply") {
		t.Fatalf("final text = %q, want the shared engine's reply", got)
	}
	svc.FinishRun(sess.ID, run)
}

// TestCreateSessionWithMCPNilFactory asserts that supplying specs with no
// SessionEngine factory configured is an invalid-argument error.
func TestCreateSessionWithMCPNilFactory(t *testing.T) {
	svc := newMCPService(t, "shared", nil) // no SessionEngine
	_, err := svc.CreateSessionWithMCP(context.Background(), "/ws", session.ModeDefault, session.Limits{},
		[]mcp.ServerConfig{{Name: "docs", URL: "https://example.test/mcp"}})
	if err == nil || !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("want ErrInvalidArgument for specs without a factory, got %v", err)
	}
}

// TestStartRunRoutesToPerSessionEngine asserts a session created with specs runs on
// the PER-SESSION engine (a distinguishable reply), not the shared one.
func TestStartRunRoutesToPerSessionEngine(t *testing.T) {
	var closed atomic.Int32
	perSession := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("per-session reply")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	factory := func(_ context.Context, _ server.ProviderSelector, specs []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		if len(specs) != 1 || specs[0].URL != "https://example.test/mcp" {
			t.Errorf("factory specs = %+v", specs)
		}
		return server.SessionEngineResult{Engine: perSession, Close: func() error { closed.Add(1); return nil }}, nil
	}
	svc := newMCPService(t, "shared reply", factory)

	sess, err := svc.CreateSessionWithMCP(context.Background(), "/ws", session.ModeDefault, session.Limits{},
		[]mcp.ServerConfig{{Name: "docs", URL: "https://example.test/mcp"}})
	if err != nil {
		t.Fatalf("CreateSessionWithMCP: %v", err)
	}

	run, err := svc.StartRun(context.Background(), sess.ID, "hi")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := finalText(t, run); !strings.Contains(got, "per-session reply") {
		t.Fatalf("final text = %q, want the per-session engine's reply", got)
	}
	svc.FinishRun(sess.ID, run)

	// CloseSession tears the per-session engine's MCP manager down exactly once.
	svc.CloseSession(sess.ID)
	if closed.Load() != 1 {
		t.Fatalf("close called %d times, want 1", closed.Load())
	}
	// Idempotent: a second close is a no-op.
	svc.CloseSession(sess.ID)
	if closed.Load() != 1 {
		t.Fatalf("close called %d times after second CloseSession, want 1", closed.Load())
	}
}

// TestEndSessionUnknownReturnsNotFound asserts EndSession on a never-created id
// surfaces ErrNotFound (so the gRPC/HTTP surfaces return NotFound / 404), rather
// than the void CloseSession's silent success.
func TestEndSessionUnknownReturnsNotFound(t *testing.T) {
	svc := newMCPService(t, "shared", nil)
	if err := svc.EndSession(context.Background(), "never-created"); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("EndSession unknown id err = %v, want ErrNotFound", err)
	}
}

// TestEndSessionEvictsLearnedAndTearsDown asserts EndSession fires OnCloseSession
// with the closing id AND tears the per-session engine down exactly once; a second
// EndSession on the (now released but still persisted) session returns nil and does
// NOT double-close — the surface-facing session-end is idempotent past first close.
func TestEndSessionEvictsLearnedAndTearsDown(t *testing.T) {
	var closed atomic.Int32
	var forgot []session.SessionID
	perSession := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("per-session reply")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	factory := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		return server.SessionEngineResult{Engine: perSession, Close: func() error { closed.Add(1); return nil }}, nil
	}
	shared := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("shared")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:        shared,
		Store:         memstore.New(),
		Workspaces:    func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		DefaultLimits: session.Limits{MaxTurns: 10, MaxToolCalls: 20},
		Now:           func() time.Time { return time.Unix(0, 0) },
		SessionEngine: factory,
		OnCloseSession: func(id session.SessionID) {
			forgot = append(forgot, id)
		},
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	sess, err := svc.CreateSessionWithMCP(context.Background(), "/ws", session.ModeDefault, session.Limits{},
		[]mcp.ServerConfig{{Name: "docs", URL: "https://example.test/mcp"}})
	if err != nil {
		t.Fatalf("CreateSessionWithMCP: %v", err)
	}

	if err := svc.EndSession(context.Background(), sess.ID); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	if len(forgot) != 1 || forgot[0] != sess.ID {
		t.Fatalf("OnCloseSession fired with %+v, want exactly [%q]", forgot, sess.ID)
	}
	if closed.Load() != 1 {
		t.Fatalf("per-session engine close called %d times, want 1", closed.Load())
	}

	// Second EndSession: the snapshot is still persisted (close != delete), so it
	// returns nil; teardown is idempotent, so the engine is not double-closed.
	if err := svc.EndSession(context.Background(), sess.ID); err != nil {
		t.Fatalf("second EndSession: %v", err)
	}
	if closed.Load() != 1 {
		t.Fatalf("per-session engine close called %d times after second EndSession, want 1", closed.Load())
	}
}

// TestServiceCloseTearsDownSessionEngines asserts Service.Close closes every
// registered per-session engine.
func TestServiceCloseTearsDownSessionEngines(t *testing.T) {
	var closed atomic.Int32
	factory := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		eng := agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn("x")),
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "test-model",
		})
		return server.SessionEngineResult{Engine: eng, Close: func() error { closed.Add(1); return nil }}, nil
	}
	svc := newMCPService(t, "shared", factory)
	if _, err := svc.CreateSessionWithMCP(context.Background(), "/ws", session.ModeDefault, session.Limits{},
		[]mcp.ServerConfig{{Name: "a", URL: "https://a.test/mcp"}}); err != nil {
		t.Fatalf("create a: %v", err)
	}
	if _, err := svc.CreateSessionWithMCP(context.Background(), "/ws", session.ModeDefault, session.Limits{},
		[]mcp.ServerConfig{{Name: "b", URL: "https://b.test/mcp"}}); err != nil {
		t.Fatalf("create b: %v", err)
	}
	svc.Close()
	if closed.Load() != 2 {
		t.Fatalf("close called %d times, want 2", closed.Load())
	}
}

// --- LoadSessionWithMCP (session/load re-mount, Slice B) ---------------------

// TestLoadSessionWithMCPEmptySpecsSharedEngine asserts that with no MCP specs the
// load path delegates to LoadSession: a completed session reopens to idle, NO
// per-session engine is registered (the factory is never called), and a subsequent
// StartRun uses the SHARED engine.
func TestLoadSessionWithMCPEmptySpecsSharedEngine(t *testing.T) {
	var called atomic.Int32
	factory := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		called.Add(1)
		return server.SessionEngineResult{Close: func() error { return nil }}, nil
	}
	svc, store := newMCPServiceStore(t, "shared reply", factory)
	id := persistCompleted(t, store)

	sess, err := svc.LoadSessionWithMCP(context.Background(), id, nil)
	if err != nil {
		t.Fatalf("LoadSessionWithMCP: %v", err)
	}
	if sess.State != session.StateIdle {
		t.Fatalf("loaded state = %q, want idle (reopened)", sess.State)
	}
	if called.Load() != 0 {
		t.Fatalf("factory called %d times for empty specs, want 0", called.Load())
	}

	run, err := svc.StartRun(context.Background(), id, "hi")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := finalText(t, run); !strings.Contains(got, "shared reply") {
		t.Fatalf("final text = %q, want the shared engine's reply", got)
	}
	svc.FinishRun(id, run)
}

// TestLoadSessionWithMCPNilFactory asserts that supplying specs with no
// SessionEngine factory configured is an invalid-argument error.
func TestLoadSessionWithMCPNilFactory(t *testing.T) {
	svc, store := newMCPServiceStore(t, "shared", nil) // no SessionEngine
	id := persistCompleted(t, store)
	_, err := svc.LoadSessionWithMCP(context.Background(), id,
		[]mcp.ServerConfig{{Name: "docs", URL: "https://example.test/mcp"}})
	if err == nil || !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("want ErrInvalidArgument for specs without a factory, got %v", err)
	}
}

// TestLoadSessionWithMCPRoutesToPerSessionEngine asserts a session loaded with specs
// runs on the PER-SESSION engine (a distinguishable reply), reopens to idle, and that
// CloseSession invokes the per-session close func exactly once.
func TestLoadSessionWithMCPRoutesToPerSessionEngine(t *testing.T) {
	var closed atomic.Int32
	perSession := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("per-session reply")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	factory := func(_ context.Context, _ server.ProviderSelector, specs []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		if len(specs) != 1 || specs[0].URL != "https://example.test/mcp" {
			t.Errorf("factory specs = %+v", specs)
		}
		return server.SessionEngineResult{Engine: perSession, Close: func() error { closed.Add(1); return nil }}, nil
	}
	svc, store := newMCPServiceStore(t, "shared reply", factory)
	id := persistCompleted(t, store)

	sess, err := svc.LoadSessionWithMCP(context.Background(), id,
		[]mcp.ServerConfig{{Name: "docs", URL: "https://example.test/mcp"}})
	if err != nil {
		t.Fatalf("LoadSessionWithMCP: %v", err)
	}
	if sess.State != session.StateIdle {
		t.Fatalf("loaded state = %q, want idle (reopened)", sess.State)
	}

	run, err := svc.StartRun(context.Background(), id, "hi")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := finalText(t, run); !strings.Contains(got, "per-session reply") {
		t.Fatalf("final text = %q, want the per-session engine's reply", got)
	}
	svc.FinishRun(id, run)

	svc.CloseSession(id)
	if closed.Load() != 1 {
		t.Fatalf("close called %d times, want 1", closed.Load())
	}
}

// TestLoadSessionWithMCPUnknownIDNotFound asserts an unknown id yields ErrNotFound
// AND the factory is never called (fail-fast: load/reopen happens before any MCP
// connect, so an unknown id never wastes a connect).
func TestLoadSessionWithMCPUnknownIDNotFound(t *testing.T) {
	var called atomic.Int32
	factory := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		called.Add(1)
		return server.SessionEngineResult{Close: func() error { return nil }}, nil
	}
	svc := newMCPService(t, "shared", factory)
	_, err := svc.LoadSessionWithMCP(context.Background(), "never-created",
		[]mcp.ServerConfig{{Name: "docs", URL: "https://example.test/mcp"}})
	if !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("LoadSessionWithMCP unknown id = %v, want ErrNotFound", err)
	}
	if called.Load() != 0 {
		t.Fatalf("factory called %d times for unknown id, want 0 (fail-fast before connect)", called.Load())
	}
}

// TestLoadSessionWithMCPReloadReplacesAndClosesPrior asserts loading the same id
// twice with specs closes the FIRST per-session engine before registering the second
// (the re-load leak guard), so a re-load on the same connection never orphans a
// manager.
func TestLoadSessionWithMCPReloadReplacesAndClosesPrior(t *testing.T) {
	var firstClosed, secondClosed atomic.Int32
	var n atomic.Int32
	factory := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		eng := agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn("x")),
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "test-model",
		})
		switch n.Add(1) {
		case 1:
			return server.SessionEngineResult{Engine: eng, Close: func() error { firstClosed.Add(1); return nil }}, nil
		default:
			return server.SessionEngineResult{Engine: eng, Close: func() error { secondClosed.Add(1); return nil }}, nil
		}
	}
	svc, store := newMCPServiceStore(t, "shared", factory)
	id := persistCompleted(t, store)

	specs := []mcp.ServerConfig{{Name: "docs", URL: "https://example.test/mcp"}}
	if _, err := svc.LoadSessionWithMCP(context.Background(), id, specs); err != nil {
		t.Fatalf("first LoadSessionWithMCP: %v", err)
	}
	if _, err := svc.LoadSessionWithMCP(context.Background(), id, specs); err != nil {
		t.Fatalf("second LoadSessionWithMCP: %v", err)
	}
	// The re-load must have closed the FIRST engine's manager when replacing it.
	if firstClosed.Load() != 1 {
		t.Fatalf("first engine close called %d times on re-load, want 1 (leak guard)", firstClosed.Load())
	}
	if secondClosed.Load() != 0 {
		t.Fatalf("second engine closed %d times before teardown, want 0", secondClosed.Load())
	}
	// CloseSession tears the SECOND (current) engine down.
	svc.CloseSession(id)
	if secondClosed.Load() != 1 {
		t.Fatalf("second engine close called %d times after CloseSession, want 1", secondClosed.Load())
	}
}
