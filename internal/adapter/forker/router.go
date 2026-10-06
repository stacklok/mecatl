package forker

import (
	"context"
	"errors"
	"fmt"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// ErrUnsupportedEnvironmentKind prevents an external/remote Environment from
// silently falling through to the host-local forker.
var ErrUnsupportedEnvironmentKind = errors.New("unsupported environment kind for child fork")

// KindRouter selects the child-environment backend from the parent's durable
// EnvironmentRef kind. The local fallback is deliberately restricted to the
// in-tree local and memory kinds; an unregistered external kind fails closed.
type KindRouter struct {
	local  tool.EnvironmentForker
	byKind map[session.EnvironmentKind]tool.EnvironmentForker
}

// KindMergerRouter selects merge backend from the parent environment kind.
type KindMergerRouter struct {
	local  tool.EnvironmentMerger
	byKind map[session.EnvironmentKind]tool.EnvironmentMerger
}

// NewKindMergerRouter constructs a fail-closed kind-aware merger.
func NewKindMergerRouter(local tool.EnvironmentMerger, byKind map[session.EnvironmentKind]tool.EnvironmentMerger) *KindMergerRouter {
	registered := make(map[session.EnvironmentKind]tool.EnvironmentMerger, len(byKind))
	for kind, merger := range byKind {
		if kind != "" && merger != nil {
			registered[kind] = merger
		}
	}
	return &KindMergerRouter{local: local, byKind: registered}
}

var _ tool.EnvironmentMerger = (*KindMergerRouter)(nil)

// Merge routes by parent kind and requires child and parent to share that kind.
func (r *KindMergerRouter) Merge(ctx context.Context, child, parent tool.Environment) error {
	kind := parent.Ref().Kind
	if child.Ref().Kind != kind {
		return fmt.Errorf("%w: child %q parent %q", ErrUnsupportedEnvironmentKind, child.Ref().Kind, kind)
	}
	if r != nil {
		if selected := r.byKind[kind]; selected != nil {
			return selected.Merge(ctx, child, parent)
		}
		if (kind == session.EnvKindLocal || kind == session.EnvKindMem) && r.local != nil {
			return r.local.Merge(ctx, child, parent)
		}
	}
	return fmt.Errorf("%w: %q", ErrUnsupportedEnvironmentKind, kind)
}

// NewKindRouter constructs a kind-aware delegation forker. The registrations
// are copied so caller mutation cannot change routing while children are live.
func NewKindRouter(local tool.EnvironmentForker, byKind map[session.EnvironmentKind]tool.EnvironmentForker) *KindRouter {
	registered := make(map[session.EnvironmentKind]tool.EnvironmentForker, len(byKind))
	for kind, childForker := range byKind {
		if kind != "" && childForker != nil {
			registered[kind] = childForker
		}
	}
	return &KindRouter{local: local, byKind: registered}
}

var _ tool.EnvironmentForker = (*KindRouter)(nil)

// Fork routes from base.Ref().Kind. Unknown remote kinds never use the host
// filesystem fallback, which would break Workspace/runner namespace affinity.
func (r *KindRouter) Fork(ctx context.Context, base tool.Environment, label string) (tool.Environment, func() error, string, error) {
	kind := base.Ref().Kind
	if r != nil {
		if selected := r.byKind[kind]; selected != nil {
			return selected.Fork(ctx, base, label)
		}
		if (kind == session.EnvKindLocal || kind == session.EnvKindMem) && r.local != nil {
			return r.local.Fork(ctx, base, label)
		}
	}
	return tool.Environment{}, nil, "", fmt.Errorf("%w: %q", ErrUnsupportedEnvironmentKind, kind)
}
