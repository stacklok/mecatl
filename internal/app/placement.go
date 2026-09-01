package app

import (
	"context"

	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

const (
	localDefaultPlacementID       = "local-default"
	localDefaultPlacementRevision = "configured-v1"
	noFSPlacementID               = "no-fs"
	noFSPlacementRevision         = "nofs-v1"
	defaultPlacementScope         = server.PlacementScope("deployment")
)

// localPlacementProvider is the trusted composition default. It owns exactly
// one configured local record plus the no-FS attenuation; it has no inventory
// registry or path-derived public identifier.
type localPlacementProvider struct {
	scope         server.PlacementScope
	root          string
	workspace     server.WorkspaceFactory
	runnerForRoot func(string) tool.CommandRunner
}

func (p *localPlacementProvider) Bind(_ context.Context, req server.PlacementBindRequest) (server.PlacementBinding, error) {
	if req.Scope != p.scope {
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}

	switch req.Selector.Kind {
	case session.PlacementSelectorNoFS:
		return p.bindNoFS()
	case session.PlacementSelectorDefault:
		if p.root == "" {
			return p.bindNoFS()
		}
		return p.bindLocal()
	case session.PlacementSelectorID:
		switch req.Selector.ID {
		case noFSPlacementID:
			return p.bindNoFS()
		case localDefaultPlacementID:
			if p.root == "" {
				return server.PlacementBinding{}, server.ErrPlacementUnavailable
			}
			return p.bindLocal()
		default:
			return server.PlacementBinding{}, server.ErrPlacementNotFound
		}
	default:
		return server.PlacementBinding{}, server.ErrInvalidPlacementSelection
	}
}

func (p *localPlacementProvider) bindLocal() (server.PlacementBinding, error) {
	// Authorization and selector resolution are complete before this first root
	// access. The immutable provider has no generation that can race afterward.
	ws := p.workspace(p.root)
	if ws == nil {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	ref := session.EnvironmentRef{
		Kind: session.EnvKindLocal, ID: localDefaultPlacementID,
		Revision: localDefaultPlacementRevision,
	}
	env, err := tool.NewEnvironment(ref, ws, p.runnerForRoot(p.root))
	if err != nil {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	return server.PlacementBinding{
		Environment: env,
		Ref:         ref,
		Metadata:    server.PlacementMetadata{Name: "Local workspace"},
	}, nil
}

func (*localPlacementProvider) bindNoFS() (server.PlacementBinding, error) {
	ref := session.EnvironmentRef{
		Kind: session.EnvKindNoFS, ID: noFSPlacementID,
		Revision: noFSPlacementRevision,
	}
	env, err := tool.NewEnvironment(ref, nofs.New(), nil)
	if err != nil {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	return server.PlacementBinding{
		Environment: env,
		Ref:         ref,
		Metadata:    server.PlacementMetadata{Name: "No filesystem"},
	}, nil
}
