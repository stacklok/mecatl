package app

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func testEnvironment(ws tool.Workspace, runner tool.CommandRunner) tool.Environment {
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: ws.Root(), Revision: "in-tree-v1"}
	if ws.Root() == "" {
		ref = session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "in-tree-v1"}
	}
	return tool.MustEnvironment(ref, ws, runner)
}

type appTestPlacementProvider struct {
	root         string
	failReattach bool
}

func (p appTestPlacementProvider) Bind(_ context.Context, req server.PlacementBindRequest) (server.PlacementBinding, error) {
	if req.Selector.IsNoFS() {
		ref := session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "test-v1"}
		return server.PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, nofs.New(), nil)}, nil
	}
	root := p.root
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: root, Revision: "test-v1"}
	return server.PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace(root), nil)}, nil
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
		return server.PlacementBinding{Ref: req.Ref, Environment: tool.MustEnvironment(req.Ref, nofs.New(), nil)}, nil
	}
	return server.PlacementBinding{Ref: req.Ref, Environment: tool.MustEnvironment(req.Ref, memfs.NewWorkspace(root), nil)}, nil
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
	return server.NewServiceContext(context.Background(), cfg)
}

func memEnvironment(root string) tool.Environment {
	return testEnvironment(memfs.NewWorkspace(root), nil)
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
	return tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: dir, Revision: "in-tree-v1"}, ws, runner)
}
