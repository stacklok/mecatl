package server_test

import (
	"context"
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

// default_model_pending_test.go pins the issue #262 review finding 1 routing
// arms (Config.DefaultModelPending) directly against the Service — the same
// factory-called/HasSessionEngine/needsRehydration pattern
// worktree_engine_test.go uses for the sibling issue-#102 arm.

// pendingDefaultService builds a Service exactly like worktreeEngineService
// but with Config.DefaultModelPending set, and a DEFAULT (matching)
// workspace so the worktree arm never fires — isolating the assertion to the
// DefaultModelPending arm alone.
func pendingDefaultService(t *testing.T, pending bool, factoryCalled *bool) (*server.Service, string) {
	t.Helper()
	const workspace = "/srv/base"
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(allowRules(), nil),
		Model:   "test-model",
	})
	cfg := server.Config{
		Engine:              engine,
		Store:               memstore.New(),
		Workspaces:          func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:                 func() time.Time { return time.Unix(0, 0) },
		DefaultWorkspace:    workspace,
		DefaultModelPending: pending,
	}
	if factoryCalled != nil {
		cfg.SessionEngine = fakeSessionEngineFactory(factoryCalled)
	}
	svc, err := server.NewService(cfg)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc, workspace
}

// TestDefaultModelPendingRoutesZeroSelectorThroughPerSessionFactory pins
// sessionNeedsPerFactory's new arm: a zero-selector, default-workspace,
// default-profile CreateSession — which would otherwise stay on the
// shared-engine fast path — routes through the per-session factory when
// Config.DefaultModelPending is true, and stays on the fast path (byte-
// identical) when it is false.
func TestDefaultModelPendingRoutesZeroSelectorThroughPerSessionFactory(t *testing.T) {
	ctx := context.Background()

	t.Run("pending=true routes through the factory", func(t *testing.T) {
		var called bool
		svc, workspace := pendingDefaultService(t, true, &called)
		sess, err := svc.CreateSession(ctx, workspace, session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if !called {
			t.Error("CreateSession did NOT call the SessionEngine factory with DefaultModelPending=true")
		}
		if !svc.HasSessionEngineForTest(sess.ID) {
			t.Error("CreateSession did not register a per-session engine (HasSessionEngine false)")
		}
	})

	t.Run("pending=false stays on the shared engine (byte-identical)", func(t *testing.T) {
		var called bool
		svc, workspace := pendingDefaultService(t, false, &called)
		if _, err := svc.CreateSession(ctx, workspace, session.ModeDefault, session.Limits{}); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if called {
			t.Error("CreateSession called the SessionEngine factory with DefaultModelPending=false — must stay on the shared engine")
		}
	})
}

// TestNeedsRehydration_DefaultModelPending pins needsRehydration's new arm: a
// default-FS, zero-selector session (which would otherwise NOT rehydrate)
// DOES rehydrate when Config.DefaultModelPending is true — so a session
// persisted before a restart into a still-down proxy also rebuilds through
// the factory at run entry and picks up a heal that landed meanwhile.
func TestNeedsRehydration_DefaultModelPending(t *testing.T) {
	const workspace = "/srv/base"
	sess := session.New("s1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: workspace, Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))

	svcPending, _ := pendingDefaultService(t, true, nil)
	if !svcPending.NeedsRehydrationForTest(sess) {
		t.Error("needsRehydration = false, want true (DefaultModelPending must force rehydration for a zero-selector session)")
	}

	svcNotPending, _ := pendingDefaultService(t, false, nil)
	if svcNotPending.NeedsRehydrationForTest(sess) {
		t.Error("needsRehydration = true, want false (a default-FS zero-selector session must not rehydrate without DefaultModelPending)")
	}
}
