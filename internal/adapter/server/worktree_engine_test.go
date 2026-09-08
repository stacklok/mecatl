package server_test

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// worktree_engine_test.go pins the issue-#102 per-session-engine routing for a
// worktree session DIRECTLY against the Service (the pure needsRehydration
// boolean + the HasSessionEngine registration check), complementing the
// end-to-end file-landing acceptance test in internal/app.

// worktreeEngineService builds a Service with a SessionEngine factory that
// records whether it was called, and the given DefaultWorkspace. The factory's
// engine is a minimal real engine (no tools) so StartRun is not needed here —
// the assertions are over create-time routing + needsRehydration only.
func worktreeEngineService(t *testing.T, defaultWorkspace string, factoryCalled *bool) *server.Service {
	t.Helper()
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(allowRules(), nil),
		Model:   "test-model",
	})
	cfg := server.Config{
		Engine: engine,
		Store:  memstore.New(),

		Now:              func() time.Time { return time.Unix(0, 0) },
		SharedEngineRoot: defaultWorkspace,
	}
	if factoryCalled != nil {
		cfg.SessionEngine = fakeSessionEngineFactory(factoryCalled)
	}
	svc, err := newPlacementTestService(cfg)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// TestCreateSessionUsesServerDefaultPlacement proves direct Service creation has no
// path input and stays on the shared engine for the configured default placement.
func TestCreateSessionUsesServerDefaultPlacement(t *testing.T) {
	ctx := context.Background()
	const base = "/srv/base"

	var called bool
	svc := worktreeEngineService(t, base, &called)
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if called {
		t.Error("default placement unexpectedly selected a per-session engine")
	}
	if svc.HasSessionEngineForTest(sess.ID) {
		t.Error("default placement unexpectedly registered a per-session engine")
	}
	if sess.EnvironmentRef.ID != base {
		t.Fatalf("placement ID = %q, want server-owned %q", sess.EnvironmentRef.ID, base)
	}
}

func TestInvariant_alternate_placement_rehydrates_root_scoped_engine(t *testing.T) {
	const base = "/srv/base"
	const alternate = "/srv/worktree"
	store := memstore.New()
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: alternate, Revision: "worktree-head"}
	persisted := session.New("alternate", session.ModeDefault, ref, session.Limits{MaxTurns: 1}, time.Unix(0, 0))
	if err := store.Save(context.Background(), persisted); err != nil {
		t.Fatal(err)
	}
	var factoryRoot string
	factory := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, root string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		factoryRoot = root
		return server.SessionEngineResult{Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("reattached")), Catalog: tool.NewCatalog()})}, nil
	}
	svc, err := newPlacementTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("shared-default-must-not-run")), Catalog: tool.NewCatalog()}),
		Store:  store, SharedEngineRoot: base, SessionEngine: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.StartRun(context.Background(), persisted.ID, "continue")
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for ev := range run.Events() {
		if ev.Type == session.EvMessageDelta {
			text += ev.Text
		}
	}
	if factoryRoot != alternate || text != "reattached" {
		t.Fatalf("restart root/reply = %q/%q, want verified alternate root and rehydrated engine", factoryRoot, text)
	}
}

// TestNeedsRehydrationWorktreeAndDefault pins the needsRehydration widening: a
// default-FS session (Workspace == DefaultWorkspace) does NOT rehydrate; a
// worktree session (Workspace != DefaultWorkspace) DOES; and a
// no-DefaultWorkspace server (child/cloud) does NOT spuriously rehydrate on a
// non-empty workspace.
func TestNeedsRehydrationIgnoresObsoleteWorkspaceIdentity(t *testing.T) {
	const base = "/srv/base"
	const wtB = "/srv/wtB"
	cases := []struct {
		name             string
		defaultWorkspace string
		sess             *session.Session
		want             bool
	}{
		{
			name:             "default-FS session does not rehydrate",
			defaultWorkspace: base,
			sess:             session.New("s1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: base, Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0)),
			want:             false,
		},
		{
			name:             "opaque local ref is not guessed to be alternate without provider verification",
			defaultWorkspace: base,
			sess:             session.New("s2", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: wtB, Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0)),
			want:             false,
		},
		{
			name:             "no-DefaultWorkspace (child/cloud): non-empty workspace does NOT spuriously rehydrate",
			defaultWorkspace: "",
			sess:             session.New("s3", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: wtB, Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0)),
			want:             false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := worktreeEngineService(t, tc.defaultWorkspace, nil)
			if got := svc.NeedsRehydrationForTest(tc.sess); got != tc.want {
				t.Fatalf("needsRehydration = %v, want %v", got, tc.want)
			}
		})
	}
}
