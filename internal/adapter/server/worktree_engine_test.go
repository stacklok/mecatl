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
		Engine:           engine,
		Store:            memstore.New(),
		Workspaces:       func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:              func() time.Time { return time.Unix(0, 0) },
		DefaultWorkspace: defaultWorkspace,
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

// TestWorktreeSessionRoutesThroughPerSessionFactory asserts the D1 widening: a
// CreateSession whose workspace DIFFERS from DefaultWorkspace routes through the
// per-session engine factory (HasSessionEngine true), while one EQUAL to
// DefaultWorkspace stays on the shared-engine fast path (HasSessionEngine false).
func TestClientWorkspaceDoesNotRoutePerSessionFactory(t *testing.T) {
	ctx := context.Background()
	const base = "/srv/base"
	const wtB = "/srv/wtB"

	// A different-workspace session registers a per-session engine.
	var called bool
	svc := worktreeEngineService(t, base, &called)
	sess, err := svc.CreateSession(ctx, wtB, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession(wtB): %v", err)
	}
	if called {
		t.Error("client workspace unexpectedly selected a per-session engine")
	}
	if svc.HasSessionEngineForTest(sess.ID) {
		t.Error("client workspace unexpectedly registered a per-session engine")
	}
	if sess.EnvironmentRef.ID != base {
		t.Fatalf("placement ID = %q, want server-owned %q", sess.EnvironmentRef.ID, base)
	}

	// A same-as-DefaultWorkspace session stays on the shared engine (no factory call).
	called = false
	if _, err := svc.CreateSession(ctx, base, session.ModeDefault, session.Limits{}); err != nil {
		t.Fatalf("CreateSession(base): %v", err)
	}
	if called {
		t.Error("CreateSession(base) called the SessionEngine factory — a default-FS session must stay on the shared engine (byte-identical to pre-#102)")
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
			name:             "obsolete worktree-shaped ref does not imply engine rehydration",
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
