package server_test

import (
	"context"
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

// selectorRecordingFactory returns a SessionEngineFactory that records the
// provider selector it was called with and serves a fresh per-session engine
// replying with reply. The cloud-native Phase 1 sibling of profileRecordingFactory.
func selectorRecordingFactory(reply string, got *atomic.Value, calls *atomic.Int32) server.SessionEngineFactory {
	return func(_ context.Context, sel server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		calls.Add(1)
		got.Store(sel)
		eng := agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn(reply)),
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "test-model",
		})
		return server.SessionEngineResult{Engine: eng, Close: func() error { return nil }}, nil
	}
}

// selectorServiceOverStore builds a real Service over a caller-supplied store
// (the restart-simulation seam — two Services over the same store are the same
// deployment before and after a process restart) with a REAL osfs-style Workspaces
// factory that records the roots it is consulted with. Unlike the no-fs seam, a
// selector session HAS a real workspace, so the factory MUST be consulted for it;
// the recorder lets a test assert the workspace path round-tripped.
func selectorServiceOverStore(t *testing.T, store *memstore.Store, factory server.SessionEngineFactory, factoryRoots *[]string) *server.Service {
	t.Helper()
	svc, err := newPlacementTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn("SHARED-ENGINE-REPLY")),
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "test-model",
		}),
		Store: store,
		Workspaces: func(root string) tool.Workspace {
			if factoryRoots != nil {
				*factoryRoots = append(*factoryRoots, root)
			}
			// A selector session uses a real workspace; an in-memory stand-in is
			// enough for the seam under test (engine selection, not FS behaviour).
			return memfs.NewWorkspace(root)
		},
		DefaultLimits: session.Limits{MaxTurns: 5},
		Now:           func() time.Time { return time.Unix(0, 0) },
		SessionEngine: factory,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// TestSelectorSessionRehydratesWithPersistedSelector is the selector-survival
// guard (cloud-native Phase 1): a PERSISTED session bound to a NON-default
// provider/model selector, whose per-session engine died with the process, must
// be REHYDRATED at the run-entry seam by a second Service — rebuilt through the
// factory with the SAME persisted selector (NOT the zero/default-provider floor),
// so its run continues on the same model. Mutation-verified: passing
// ProviderSelector{} to the rehydration factory (instead of reading the persisted
// labels back) makes the factory see the empty selector and fails this test.
func TestSelectorSessionRehydratesWithPersistedSelector(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()

	wantSel := server.ProviderSelector{ProviderID: "openrouter", ModelID: "anthropic/claude-3.5-sonnet"}

	// "Before the restart": create the selector session over a real workspace.
	var (
		gotSel atomic.Value
		calls  atomic.Int32
	)
	svc1 := selectorServiceOverStore(t, store, selectorRecordingFactory("PRE-RESTART", &gotSel, &calls), nil)
	sess, err := svc1.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{}, wantSel)
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	// The selector was persisted onto the aggregate as the opaque label pair.
	if sess.ProviderID != wantSel.ProviderID || sess.ModelID != wantSel.ModelID {
		t.Fatalf("persisted selector labels = {%q,%q}, want {%q,%q}",
			sess.ProviderID, sess.ModelID, wantSel.ProviderID, wantSel.ModelID)
	}
	run, err := svc1.StartRun(ctx, sess.ID, "first turn")
	if err != nil {
		t.Fatalf("StartRun (pre-restart): %v", err)
	}
	if got := drainServerRun(run); got != "PRE-RESTART" {
		t.Fatalf("pre-restart reply = %q, want PRE-RESTART", got)
	}

	// "After the restart": a NEW Service over the SAME store, empty in-memory
	// registries — exactly the post-restart state.
	var factoryRoots []string
	var (
		gotSel2 atomic.Value
		calls2  atomic.Int32
	)
	svc2 := selectorServiceOverStore(t, store, selectorRecordingFactory("REHYDRATED", &gotSel2, &calls2), &factoryRoots)

	run2, err := svc2.StartRunContent(ctx, sess.ID, "post-restart turn", nil)
	if err != nil {
		t.Fatalf("StartRunContent (post-restart): %v", err)
	}
	if got := drainServerRun(run2); got != "REHYDRATED" {
		t.Fatalf("post-restart reply = %q, want REHYDRATED (the rehydrated selector engine); "+
			"SHARED-ENGINE-REPLY means the session DEGRADED onto the default-provider shared engine", got)
	}
	if calls2.Load() != 1 {
		t.Fatalf("factory called %d times after restart, want exactly 1 (rehydration)", calls2.Load())
	}
	// THE KEY ASSERTION: the rehydration factory saw the PERSISTED selector, not zero.
	if got := gotSel2.Load(); got != wantSel {
		t.Fatalf("rehydration factory saw selector %v, want the persisted %v (a zero selector is the wrong-model bug)", got, wantSel)
	}
	// The factory receives the private root resolved from the exact placement.
	sawWorkspace := false
	for _, r := range factoryRoots {
		if r == "/ws" {
			sawWorkspace = true
		}
	}
	if !sawWorkspace {
		t.Fatalf("the exact placement root never reached the engine factory (roots seen: %v)", factoryRoots)
	}
}

// TestDefaultFSSessionDoesNotRehydrate pins the bound on the widened trigger: a
// DEFAULT FS session (empty selector, default profile, NON-empty workspace) must
// keep riding the SHARED engine after a restart — the rehydration factory is
// NEVER consulted for it. Without the bound, every session would needlessly
// rebuild a per-session engine on resume.
func TestDefaultFSSessionDoesNotRehydrate(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()

	// "Before the restart": a plain default FS session (shared-engine fast path).
	var (
		gotSel atomic.Value
		calls  atomic.Int32
	)
	svc1 := selectorServiceOverStore(t, store, selectorRecordingFactory("UNUSED", &gotSel, &calls), nil)
	sess, err := svc1.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("factory called %d times for a default-session create, want 0 (shared-engine fast path)", calls.Load())
	}

	// "After the restart": a NEW Service over the same store.
	var factoryRoots []string
	var (
		gotSel2 atomic.Value
		calls2  atomic.Int32
	)
	svc2 := selectorServiceOverStore(t, store, selectorRecordingFactory("UNUSED", &gotSel2, &calls2), &factoryRoots)
	run, err := svc2.StartRunContent(ctx, sess.ID, "post-restart turn", nil)
	if err != nil {
		t.Fatalf("StartRunContent (post-restart): %v", err)
	}
	if got := drainServerRun(run); got != "SHARED-ENGINE-REPLY" {
		t.Fatalf("post-restart reply = %q, want SHARED-ENGINE-REPLY (a default FS session must keep riding the shared engine)", got)
	}
	if calls2.Load() != 0 {
		t.Fatalf("rehydration factory called %d times for a default FS session, want 0 (the widened trigger must not fire for it)", calls2.Load())
	}
}
