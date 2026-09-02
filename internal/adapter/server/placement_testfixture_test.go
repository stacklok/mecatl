package server_test

import (
	"context"
	"sync/atomic"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func newPlacementTestService(cfg server.Config) (*server.Service, error) {
	if cfg.PlacementProvider == nil {
		root := cfg.DefaultWorkspace
		if root == "" {
			root = "/ws"
		}
		cfg.PlacementProvider = testPlacementProvider{root: root, workspaces: cfg.Workspaces, firstBind: &atomic.Bool{}}
		cfg.PlacementScope = "test"
	}
	svc, err := server.NewService(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.MemberEngine != nil {
		if _, err := svc.CreateSessionWithProfile(context.Background(), "", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID("source")); err != nil {
			return nil, err
		}
	}
	return svc, nil
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
		return server.PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, nofs.New(), nil)}, nil
	}
	root := p.root
	if root == "" {
		root = "/ws"
	}
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: root, Revision: "in-tree-v1"}
	var ws tool.Workspace
	if p.firstBind != nil && !p.firstBind.Swap(true) {
		ws = memfs.NewWorkspace(root)
	} else {
		ws = p.workspace(root)
	}
	if ws == nil {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	return server.PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, ws, nil)}, nil
}

func (p testPlacementProvider) Reattach(_ context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	if req.Ref.Kind == session.EnvKindNoFS {
		return server.PlacementBinding{Ref: req.Ref, Environment: tool.MustEnvironment(req.Ref, nofs.New(), nil)}, nil
	}
	ws := p.workspace(req.Ref.ID)
	if ws == nil {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	return server.PlacementBinding{Ref: req.Ref, Environment: tool.MustEnvironment(req.Ref, ws, nil)}, nil
}
