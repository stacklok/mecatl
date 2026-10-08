package app

import (
	"context"

	"github.com/stacklok/mecatl/engine/adapter/memledger"
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

func referenceIntentLifecycle(provider server.PlacementProvider) server.ReferenceIntentLifecycle {
	if profiled, ok := provider.(*profilePlacementProvider); ok {
		lifecycle, _ := profiled.remote.(server.ReferenceIntentLifecycle)
		return lifecycle
	}
	lifecycle, _ := provider.(server.ReferenceIntentLifecycle)
	return lifecycle
}

func executionAccess(provider server.PlacementProvider) server.ExecutionAccess {
	access, _ := provider.(server.ExecutionAccess)
	return access
}

type profilePlacementProvider struct {
	remote server.PlacementProvider
	local  *localPlacementProvider
}

func (p *profilePlacementProvider) ValidatePlacement(ctx context.Context) error {
	if validator, ok := p.remote.(server.PlacementValidator); ok {
		return validator.ValidatePlacement(ctx)
	}
	return nil
}
func (p *profilePlacementProvider) Bind(ctx context.Context, req server.PlacementBindRequest) (server.PlacementBinding, error) {
	if req.Selector.IsNoFS() {
		return p.local.Bind(ctx, req)
	}
	return p.remote.Bind(ctx, req)
}
func (p *profilePlacementProvider) Reattach(ctx context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	if req.Ref.Kind == session.EnvKindNoFS {
		return p.local.Reattach(ctx, req)
	}
	remote, ok := p.remote.(server.PlacementReattacher)
	if !ok {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	return remote.Reattach(ctx, req)
}

func (p *profilePlacementProvider) PrepareReferenceDelete(ctx context.Context, req server.PlacementSuccessorRequest) (server.ReferenceDeleteHandle, error) {
	lifecycle, ok := p.remote.(server.ReferenceLifecycle)
	if !ok || !lifecycle.Applies(req.Ref) {
		return nil, server.ErrPlacementUnavailable
	}
	return lifecycle.PrepareReferenceDelete(ctx, req)
}

func (p *profilePlacementProvider) ListReferenceIntents(ctx context.Context, limit int) ([]server.ReferenceIntent, error) {
	lifecycle, ok := p.remote.(server.ReferenceIntentLifecycle)
	if !ok {
		return nil, server.ErrPlacementUnavailable
	}
	return lifecycle.ListReferenceIntents(ctx, limit)
}
func (p *profilePlacementProvider) CommitReferenceIntent(ctx context.Context, intent server.ReferenceIntent) error {
	lifecycle, ok := p.remote.(server.ReferenceIntentLifecycle)
	if !ok {
		return server.ErrPlacementUnavailable
	}
	return lifecycle.CommitReferenceIntent(ctx, intent)
}
func (p *profilePlacementProvider) ConfirmReferenceIntentDelete(ctx context.Context, intent server.ReferenceIntent) error {
	lifecycle, ok := p.remote.(server.ReferenceIntentLifecycle)
	if !ok {
		return server.ErrPlacementUnavailable
	}
	return lifecycle.ConfirmReferenceIntentDelete(ctx, intent)
}
func (p *profilePlacementProvider) CancelReferenceIntentDelete(ctx context.Context, intent server.ReferenceIntent) error {
	lifecycle, ok := p.remote.(server.ReferenceIntentLifecycle)
	if !ok {
		return server.ErrPlacementUnavailable
	}
	return lifecycle.CancelReferenceIntentDelete(ctx, intent)
}

func (p *profilePlacementProvider) ReserveSuccessor(ctx context.Context, req server.PlacementSuccessorRequest) (server.PlacementBinding, error) {
	reservoir, ok := p.remote.(server.PlacementSuccessorReservoir)
	if !ok {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	return reservoir.ReserveSuccessor(ctx, req)
}

func (p *profilePlacementProvider) Applies(ref session.EnvironmentRef) bool {
	access, ok := p.remote.(server.ExecutionAccess)
	return ok && access.Applies(ref)
}
func (p *profilePlacementProvider) AcquireRun(ctx context.Context, req server.ExecutionRunRequest) (server.ExecutionRunHandle, error) {
	access, ok := p.remote.(server.ExecutionAccess)
	if !ok || !access.Applies(req.Ref) {
		return nil, server.ErrPlacementUnavailable
	}
	return access.AcquireRun(ctx, req)
}

// localPlacementProvider is the trusted composition default. It owns exactly
// one configured local record plus the no-FS attenuation; it has no inventory
// registry or path-derived public identifier.
type localPlacementProvider struct {
	scope         server.PlacementScope
	root          string
	workspace     server.WorkspaceFactory
	runnerForRoot func(string) tool.CommandRunner
	worktrees     server.WorktreeLister
	selectors     *server.WorktreeSelectorIssuer
}

func (p *localPlacementProvider) Bind(ctx context.Context, req server.PlacementBindRequest) (server.PlacementBinding, error) {
	if req.Scope != p.scope {
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}

	switch {
	case req.Selector.IsNoFS():
		return p.bindNoFS()
	case req.Selector.IsDefault():
		if p.root == "" {
			return p.bindNoFS()
		}
		return p.bindLocal()
	case req.Selector.IsWorktree():
		return p.bindSelectedWorktree(ctx, req)
	default:
		return server.PlacementBinding{}, server.ErrInvalidPlacementSelection
	}
}

func (p *localPlacementProvider) Reattach(ctx context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	if req.Scope != p.scope {
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}
	if req.Ref == configuredLocalPlacementRef(p.root) {
		if p.root == "" {
			return server.PlacementBinding{}, server.ErrPlacementUnavailable
		}
		return p.bindLocal()
	}
	if req.Ref == (session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: noFSPlacementID, Revision: noFSPlacementRevision}) {
		return p.bindNoFS()
	}
	if choice, ok := p.currentWorktree(ctx, req.Ref); ok {
		return p.bindWorktree(choice)
	}
	return server.PlacementBinding{}, server.ErrPlacementNotFound
}

func (p *localPlacementProvider) ListWorktrees(ctx context.Context, req server.PlacementDiscoveryRequest) ([]server.ScopedWorktree, error) {
	if req.Scope != p.scope || p.worktrees == nil || p.selectors == nil || !p.authorizedSource(ctx, req.SourceRef) {
		return nil, server.ErrPlacementNotFound
	}
	current, err := p.worktrees.List(ctx, req.SourceRef.ID)
	if err != nil {
		return nil, server.ErrPlacementUnavailable
	}
	out := make([]server.ScopedWorktree, 0, len(current))
	for _, choice := range current {
		label := choice.Branch
		if label == "" {
			label = "Detached worktree"
		}
		out = append(out, server.ScopedWorktree{
			Selector: p.selectors.Issue(req.Principal, req.Source, choice),
			Label:    label, Branch: choice.Branch, Revision: choice.Head, Bare: choice.Bare,
		})
	}
	return out, nil
}

func (p *localPlacementProvider) bindSelectedWorktree(ctx context.Context, req server.PlacementBindRequest) (server.PlacementBinding, error) {
	if p.worktrees == nil || p.selectors == nil || !p.authorizedSource(ctx, req.Selector.SourceRef) {
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}
	current, err := p.worktrees.List(ctx, req.Selector.SourceRef.ID)
	if err != nil {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	choice, err := p.selectors.Match(req.Selector.ID, req.Principal, req.Selector.Source, current)
	if err != nil {
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}
	// Re-enumerate immediately before construction so a replaced worktree choice
	// cannot be opened under an authorization decision for an older revision.
	current, err = p.worktrees.List(ctx, req.Selector.SourceRef.ID)
	if err != nil {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	matched := false
	for _, candidate := range current {
		if candidate.Path == choice.Path && candidate.Head == choice.Head && candidate.Branch == choice.Branch && candidate.Bare == choice.Bare {
			matched = true
			break
		}
	}
	if !matched {
		return server.PlacementBinding{}, server.ErrPlacementChanged
	}
	return p.bindWorktree(choice)
}

func (p *localPlacementProvider) authorizedSource(ctx context.Context, ref session.EnvironmentRef) bool {
	if ref == configuredLocalPlacementRef(p.root) {
		return true
	}
	_, ok := p.currentWorktree(ctx, ref)
	return ok
}

func (p *localPlacementProvider) currentWorktree(ctx context.Context, ref session.EnvironmentRef) (server.Worktree, bool) {
	if p.worktrees == nil || ref.Kind != session.EnvKindLocal {
		return server.Worktree{}, false
	}
	current, err := p.worktrees.List(ctx, p.root)
	if err != nil {
		return server.Worktree{}, false
	}
	for _, choice := range current {
		if choice.Path == ref.ID && choice.Head == ref.Revision {
			return choice, true
		}
	}
	return server.Worktree{}, false
}

func (p *localPlacementProvider) bindWorktree(choice server.Worktree) (server.PlacementBinding, error) {
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: choice.Path, Revision: choice.Head}
	ws := p.workspace(choice.Path)
	if ws == nil {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	var runner tool.CommandRunner
	if p.runnerForRoot != nil {
		runner = p.runnerForRoot(ws.Root())
	}
	env, err := tool.NewEnvironment(ref, ws, memledger.New(), runner)
	if err != nil {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	return server.PlacementBinding{Environment: env, Ref: ref, GovernanceRoot: choice.Path, Metadata: server.PlacementMetadata{
		Label: choice.Branch, Branch: choice.Branch, Revision: choice.Head,
	}}, nil
}

func configuredLocalPlacementRef(root string) session.EnvironmentRef {
	return session.EnvironmentRef{
		Kind: session.EnvKindLocal, ID: root,
		Revision: localDefaultPlacementRevision,
	}
}

func (p *localPlacementProvider) bindLocal() (server.PlacementBinding, error) {
	// Authorization and selector resolution are complete before this first root
	// access. The immutable provider has no generation that can race afterward.
	ws := p.workspace(p.root)
	if ws == nil {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	ref := configuredLocalPlacementRef(p.root)
	var runner tool.CommandRunner
	if p.runnerForRoot != nil {
		runner = p.runnerForRoot(ws.Root())
	}
	env, err := tool.NewEnvironment(ref, ws, memledger.New(), runner)
	if err != nil {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	return server.PlacementBinding{
		Environment:    env,
		Ref:            ref,
		GovernanceRoot: p.root,
		Metadata:       server.PlacementMetadata{Label: "Local workspace", Revision: "configured"},
	}, nil
}

func (*localPlacementProvider) bindNoFS() (server.PlacementBinding, error) {
	ref := session.EnvironmentRef{
		Kind: session.EnvKindNoFS, ID: noFSPlacementID,
		Revision: noFSPlacementRevision,
	}
	env, err := tool.NewEnvironment(ref, nofs.New(), memledger.New(), nil)
	if err != nil {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	return server.PlacementBinding{
		Environment: env,
		Ref:         ref,
		Metadata:    server.PlacementMetadata{Label: "No filesystem"},
	}, nil
}
