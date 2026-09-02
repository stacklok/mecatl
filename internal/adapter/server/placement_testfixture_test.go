package server_test

import (
	"context"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type testPlacementProvider struct{}

func (testPlacementProvider) Bind(_ context.Context, req server.PlacementBindRequest) (server.PlacementBinding, error) {
	if req.Selector.Kind == session.PlacementSelectorNoFS {
		ref := session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "test-nofs", Revision: "v1"}
		return server.PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, nofs.New(), nil)}, nil
	}
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "test-default", Revision: "v1"}
	return server.PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/ws"), nil)}, nil
}

func (testPlacementProvider) Reattach(ctx context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	if req.Ref.Kind == session.EnvKindNoFS {
		return testPlacementProvider{}.Bind(ctx, server.PlacementBindRequest{Selector: session.NoFSPlacement(), Scope: req.Scope})
	}
	return testPlacementProvider{}.Bind(ctx, server.PlacementBindRequest{Selector: session.DefaultPlacement(), Scope: req.Scope})
}
