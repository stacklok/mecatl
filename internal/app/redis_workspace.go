package app

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

const redisEnvironmentKind session.EnvironmentKind = "redis"

const (
	redisEnvironmentRevision = "redis-workspace-v1"
	anonymousWorkspaceScope  = "anonymous"
)

type redisPlacementProvider struct{ store *redisstore.Store }

func (p *redisPlacementProvider) Bind(ctx context.Context, req server.PlacementBindRequest) (server.PlacementBinding, error) {
	if !req.Selector.Valid() || req.Scope == "" {
		return server.PlacementBinding{}, server.ErrInvalidPlacementSelection
	}
	if req.Selector.IsNoFS() {
		ref := session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: redisEnvironmentRevision}
		return server.PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, nofs.New(), memledger.New(), nil), Metadata: server.PlacementMetadata{Kind: "nofs", Label: "No filesystem"}}, nil
	}
	if !req.Selector.IsDefault() {
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}
	scope := redisPrincipalScope(req.Principal)
	ws, err := p.store.CreateWorkspace(ctx, scope)
	if err != nil {
		return server.PlacementBinding{}, errors.Join(server.ErrPlacementUnavailable, err)
	}
	return redisPlacementBinding(scope, ws), nil
}

func (p *redisPlacementProvider) Reattach(ctx context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	if req.Scope == "" || !req.Ref.Valid() {
		return server.PlacementBinding{}, server.ErrInvalidPlacementSelection
	}
	if req.Ref.Kind == session.EnvKindNoFS {
		if req.Ref.ID != "none" || req.Ref.Revision != redisEnvironmentRevision {
			return server.PlacementBinding{}, server.ErrPlacementNotFound
		}
		return server.PlacementBinding{Ref: req.Ref, Environment: tool.MustEnvironment(req.Ref, nofs.New(), memledger.New(), nil), Metadata: server.PlacementMetadata{Kind: "nofs", Label: "No filesystem"}}, nil
	}
	scope := redisPrincipalScope(req.Principal)
	if req.Ref.Kind != redisEnvironmentKind || req.Ref.ID != scope || req.Ref.Revision != redisEnvironmentRevision {
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}
	ws, err := p.store.OpenWorkspace(ctx, scope)
	if err != nil {
		return server.PlacementBinding{}, errors.Join(server.ErrPlacementUnavailable, err)
	}
	return redisPlacementBinding(scope, ws), nil
}

func redisPlacementBinding(scope string, ws tool.Workspace) server.PlacementBinding {
	ref := session.EnvironmentRef{Kind: redisEnvironmentKind, ID: scope, Revision: redisEnvironmentRevision}
	return server.PlacementBinding{
		Ref:         ref,
		Environment: tool.MustEnvironment(ref, ws, memledger.New(), nil),
		Metadata:    server.PlacementMetadata{Kind: "redis", Label: "Redis workspace", Revision: redisEnvironmentRevision},
	}
}

func redisPrincipalScope(principal *session.Principal) string {
	if principal == nil {
		return anonymousWorkspaceScope
	}
	sum := session.PrincipalScopeHash(principal)
	return hex.EncodeToString(sum[:])
}
