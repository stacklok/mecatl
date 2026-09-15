package server_test

import (
	"context"
	"sync/atomic"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func newPlacementTestService(cfg server.Config) (*server.Service, error) {
	if cfg.SharedEngineRoot == "" {
		cfg.SharedEngineRoot = "/ws"
	}
	if cfg.PlacementProvider == nil {
		root := cfg.SharedEngineRoot
		if root == "" {
			root = "/ws"
		}
		cfg.SharedEngineRoot = root
		cfg.PlacementProvider = testPlacementProvider{root: root, firstBind: &atomic.Bool{}}
		cfg.PlacementScope = "test"
	}
	return server.NewService(cfg)
}

func newPlacementTeamTestService(cfg server.Config) (*server.Service, error) {
	svc, err := newPlacementTestService(cfg)
	if err != nil {
		return nil, err
	}
	if err := createPlacementTestSource(svc); err != nil {
		return nil, err
	}
	return svc, nil
}

//nolint:revive // Test helper keeps the service receiver before its context.
func forkSession(svc *server.Service, ctx context.Context, source session.SessionID, title, effort string) (session.SessionID, error) {
	return svc.ForkSessionSuccessor(ctx, server.ForkSuccessorRequest{Source: source, Title: title, ReasoningEffort: effort})
}

func createPlacementTestSource(svc *server.Service) error {
	_, err := svc.CreateSessionWithProfile(context.Background(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID("source"))
	return err
}

type testPlacementProvider struct {
	root       string
	workspaces func(string) tool.Workspace
	firstBind  *atomic.Bool
}

func (p testPlacementProvider) workspace(root string) tool.Workspace {
	if p.workspaces != nil {
		return p.workspaces(root)
	}
	return memfs.NewWorkspace(root)
}

func (p testPlacementProvider) Bind(_ context.Context, req server.PlacementBindRequest) (server.PlacementBinding, error) {
	if req.Selector.Kind == server.PlacementSelectorNoFS {
		ref := session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "in-tree-v1"}
		return server.PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, nofs.New(), memledger.New(), nil)}, nil
	}
	root := p.root
	if root == "" {
		root = "/ws"
	}
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: root, Revision: "in-tree-v1"}
	ws := p.workspace(root)
	if ws == nil {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	return server.PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, ws, memledger.New(), nil)}, nil
}

func (testPlacementProvider) ListWorktrees(context.Context, server.PlacementDiscoveryRequest) ([]server.ScopedWorktree, error) {
	return nil, nil
}

func (p testPlacementProvider) Reattach(_ context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	if req.Ref.Kind == session.EnvKindNoFS {
		return server.PlacementBinding{Ref: req.Ref, Environment: tool.MustEnvironment(req.Ref, nofs.New(), memledger.New(), nil)}, nil
	}
	ws := p.workspace(req.Ref.ID)
	if ws == nil {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	return server.PlacementBinding{Ref: req.Ref, Environment: tool.MustEnvironment(req.Ref, ws, memledger.New(), nil)}, nil
}
