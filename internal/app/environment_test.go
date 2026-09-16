package app

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func testReadLedger() tool.ReadLedger { return memledger.New() }

func newTestSubagentTool(engine *agent.Engine, opts ...agent.SubagentOption) tool.Tool {
	opts = append(opts, agent.WithSubagentReadLedgerFactory(testReadLedger))
	return agent.NewSubagentTool(engine, opts...)
}

func newTestSupervisor(tm *team.Team, base tool.Environment, factory agent.MemberEngine, opts ...agent.SupervisorOption) *agent.Supervisor {
	opts = append(opts, agent.WithTeamReadLedgerFactory(testReadLedger))
	return agent.NewSupervisor(tm, base, factory, opts...)
}

func testEnvironment(ws tool.Workspace, runner tool.CommandRunner) tool.Environment {
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: ws.Root(), Revision: "in-tree-v1"}
	if ws.Root() == "" {
		ref = session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "in-tree-v1"}
	}
	return tool.MustEnvironment(ref, ws, memledger.New(), runner)
}

type appTestPlacementProvider struct {
	root         string
	failReattach bool
	runner       tool.CommandRunner
}

func (p appTestPlacementProvider) Bind(_ context.Context, req server.PlacementBindRequest) (server.PlacementBinding, error) {
	if req.Selector.IsNoFS() {
		ref := session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "test-v1"}
		return server.PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, nofs.New(), memledger.New(), nil)}, nil
	}
	root := p.root
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: root, Revision: "test-v1"}
	return server.PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace(root), memledger.New(), p.runner), CompositionRoot: root}, nil
}
func (appTestPlacementProvider) ListWorktrees(context.Context, server.PlacementDiscoveryRequest) ([]server.ScopedWorktree, error) {
	return nil, nil
}

func (p appTestPlacementProvider) Reattach(_ context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	if p.failReattach {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	root := req.Ref.ID
	if req.Ref.Kind == session.EnvKindNoFS {
		return server.PlacementBinding{Ref: req.Ref, Environment: tool.MustEnvironment(req.Ref, nofs.New(), memledger.New(), nil)}, nil
	}
	return server.PlacementBinding{Ref: req.Ref, Environment: tool.MustEnvironment(req.Ref, memfs.NewWorkspace(root), memledger.New(), p.runner), CompositionRoot: root}, nil
}

func newTestServerService(cfg server.Config) (*server.Service, error) {
	if cfg.SessionEngine == nil {
		cfg.SessionEngine = func(context.Context, server.ProviderSelector, []mcp.ServerConfig, server.SessionProfile, string, session.PermissionMode) (server.SessionEngineResult, error) {
			return server.SessionEngineResult{Engine: cfg.Engine}, nil
		}
	}
	if cfg.PlacementProvider == nil {
		root := cfg.SharedEngineRoot
		if root == "" {
			root = "/ws"
		}
		cfg.SharedEngineRoot = root
		cfg.PlacementProvider = appTestPlacementProvider{root: root}
		cfg.PlacementScope = "legacy-local"
	}
	return server.NewService(cfg)
}

func memEnvironment(root string) tool.Environment {
	return testEnvironment(memfs.NewWorkspace(root), nil)
}

func TestChildWorkspaceViewPreservesContentBackend(t *testing.T) {
	base := memfs.NewWorkspace("/workspace")
	if got := childWorkspaceView(base); got != base {
		t.Fatal("a non-relaxed custom Workspace must pass through by identity")
	}

	relaxed := newEscapeWorkspace(base, &escapeClassifier{})
	got, ok := childWorkspaceView(relaxed).(*escapeWorkspace)
	if !ok {
		t.Fatalf("child view type = %T, want *escapeWorkspace", childWorkspaceView(relaxed))
	}
	if got.Workspace != base {
		t.Fatal("child view replaced the underlying content backend")
	}
	if !got.confined {
		t.Fatal("child view did not remove relaxed path authority")
	}
}

// osfsEnvironment builds an osfs Environment rooted at dir with an OPTIONAL
// bound command runner, failing the test on error. It is the Environment-seam
// analogue of osfsWSForTest for the app tests that drive an engine or
// delegation tool against a real on-disk workspace (issue #462).
func osfsEnvironment(t *testing.T, dir string, runner tool.CommandRunner) tool.Environment {
	t.Helper()
	ws, err := osfs.NewWorkspace(dir)
	if err != nil {
		t.Fatalf("osfs workspace %q: %v", dir, err)
	}
	return tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: dir, Revision: "in-tree-v1"}, ws, memledger.New(), runner)
}
