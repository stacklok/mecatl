package app

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type placementContextKey struct{}

type compositionPlacementProvider struct {
	binding       server.PlacementBinding
	err           error
	calls         []server.PlacementBindRequest
	reattachCalls []server.PlacementReattachRequest
	contextSeen   any
}

func (p *compositionPlacementProvider) Bind(ctx context.Context, req server.PlacementBindRequest) (server.PlacementBinding, error) {
	p.calls = append(p.calls, req)
	p.contextSeen = ctx.Value(placementContextKey{})
	return p.binding, p.err
}

func (p *compositionPlacementProvider) Reattach(_ context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	p.reattachCalls = append(p.reattachCalls, req)
	return p.binding, p.err
}

type scriptedWorktreeLister struct {
	mu        sync.Mutex
	responses [][]server.Worktree
}

func (l *scriptedWorktreeLister) List(context.Context, string) ([]server.Worktree, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.responses) == 0 {
		return nil, nil
	}
	out := append([]server.Worktree(nil), l.responses[0]...)
	if len(l.responses) > 1 {
		l.responses = l.responses[1:]
	}
	return out, nil
}

func TestInvariant_local_placement_identity_and_atomic_worktree_binding(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	providerA := &localPlacementProvider{scope: "test", root: rootA, workspace: func(root string) tool.Workspace { return memfs.NewWorkspace(root) }}
	providerB := &localPlacementProvider{scope: "test", root: rootB, workspace: func(root string) tool.Workspace { return memfs.NewWorkspace(root) }}
	binding, err := providerA.Bind(context.Background(), server.PlacementBindRequest{Selector: server.DefaultPlacement(), Scope: "test", Operation: server.PlacementOperationCreate})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := providerB.Reattach(context.Background(), server.PlacementReattachRequest{Ref: binding.Ref, Scope: "test"}); !errors.Is(err, server.ErrPlacementNotFound) {
		t.Fatalf("root-A ref reattached under root B: %v", err)
	}

	key := make([]byte, 32)
	issuer, err := server.NewWorktreeSelectorIssuer(key)
	if err != nil {
		t.Fatal(err)
	}
	oldChoice := server.Worktree{Path: "/private/worktree", Branch: "feature", Head: "old"}
	newChoice := server.Worktree{Path: oldChoice.Path, Branch: oldChoice.Branch, Head: "new"}
	lister := &scriptedWorktreeLister{responses: [][]server.Worktree{{oldChoice}, {oldChoice}, {newChoice}}}
	workspaceCalls := 0
	provider := &localPlacementProvider{scope: "test", root: rootA, selectors: issuer, worktrees: lister, workspace: func(root string) tool.Workspace {
		workspaceCalls++
		return memfs.NewWorkspace(root)
	}}
	sourceRef := configuredLocalPlacementRef(rootA)
	choices, err := provider.ListWorktrees(context.Background(), server.PlacementDiscoveryRequest{Source: "source", SourceRef: sourceRef, Scope: "test"})
	if err != nil || len(choices) != 1 {
		t.Fatalf("ListWorktrees = %+v, %v", choices, err)
	}
	_, err = provider.Bind(context.Background(), server.PlacementBindRequest{Selector: server.SelectWorktree("source", sourceRef, choices[0].Selector), Scope: "test", Operation: server.PlacementOperationSuccessor})
	if !errors.Is(err, server.ErrPlacementChanged) || workspaceCalls != 0 {
		t.Fatalf("replacement bind = %v, workspace constructions = %d; want changed before access", err, workspaceCalls)
	}
}

func TestInvariant_selected_successor_reattaches_after_restart(t *testing.T) {
	root := t.TempDir()
	choice := server.Worktree{Path: "/private/worktree", Branch: "feature", Head: "head-1"}
	lister := &scriptedWorktreeLister{responses: [][]server.Worktree{{choice}}}
	issuer, _ := server.NewWorktreeSelectorIssuer(make([]byte, 32))
	provider := &localPlacementProvider{scope: "test", root: root, selectors: issuer, worktrees: lister, workspace: func(root string) tool.Workspace { return memfs.NewWorkspace(root) }}
	sourceRef := configuredLocalPlacementRef(root)
	listed, err := provider.ListWorktrees(context.Background(), server.PlacementDiscoveryRequest{Source: "source", SourceRef: sourceRef, Scope: "test"})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := provider.Bind(context.Background(), server.PlacementBindRequest{Selector: server.SelectWorktree("source", sourceRef, listed[0].Selector), Scope: "test", Operation: server.PlacementOperationSuccessor})
	if err != nil {
		t.Fatal(err)
	}
	restarted := &localPlacementProvider{scope: "test", root: root, worktrees: &scriptedWorktreeLister{responses: [][]server.Worktree{{choice}}}, workspace: func(root string) tool.Workspace { return memfs.NewWorkspace(root) }}
	rebound, err := restarted.Reattach(context.Background(), server.PlacementReattachRequest{Ref: selected.Ref, Scope: "test"})
	if err != nil || rebound.Ref != selected.Ref || rebound.Environment.Workspace().Root() != choice.Path {
		t.Fatalf("restart reattach = %+v, %v", rebound, err)
	}
}

func TestADR_0290_CompositionConfiguresProviderOwnedPlacements(t *testing.T) {
	t.Parallel()

	t.Run("selector vocabulary is closed", func(t *testing.T) {
		selectors := []server.PlacementSelector{
			server.DefaultPlacement(),
			server.NoFSPlacement(),
		}
		for _, selector := range selectors {
			if !selector.Valid() {
				t.Fatalf("canonical selector %+v is invalid", selector)
			}
		}
		if (server.PlacementSelector{Kind: "path", ID: "/server/root"}).Valid() {
			t.Fatal("path selector widened the closed placement vocabulary")
		}
	})

	t.Run("startup validation uses the build context", func(t *testing.T) {
		ref := session.EnvironmentRef{Kind: "remote", ID: "opaque-context-id", Revision: "r1"}
		provider := &compositionPlacementProvider{binding: server.PlacementBinding{
			Ref: ref,
			Environment: tool.MustEnvironment(
				ref,
				memfs.NewWorkspace("/private-context-root"), nil,
			),
		}}
		ctx := context.WithValue(context.Background(), placementContextKey{}, "build-context")
		built, err := Build(ctx, Config{
			Workspace: rootForPlacementTest(t), UseMock: true,
			PlacementProvider: provider, PlacementScope: "deployment-a",
		})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		defer built.Close()
		if provider.contextSeen != "build-context" {
			t.Fatalf("startup provider context value = %v, want build-context", provider.contextSeen)
		}
	})

	t.Run("trusted local default uses opaque identity", func(t *testing.T) {
		root := t.TempDir()
		built, err := Build(context.Background(), Config{Workspace: root, UseMock: true})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		defer built.Close()

		binding, err := built.Service.BindPlacement(context.Background(), server.DefaultPlacement(), server.PlacementOperationCreate)
		if err != nil {
			t.Fatalf("BindPlacement(default): %v", err)
		}
		if binding.Ref.Kind != "local" || binding.Ref.ID == "" || binding.Ref.Revision == "" {
			t.Fatalf("default ref = %+v, want complete local provider ref", binding.Ref)
		}
		if binding.Ref.ID != root {
			t.Fatalf("private default ID = %q, want exact configured root %q", binding.Ref.ID, root)
		}
		if binding.Environment.Workspace() == nil {
			t.Fatal("default binding has nil workspace")
		}
	})

	for _, kind := range []session.EnvironmentKind{"worktree", "remote"} {
		kind := kind
		t.Run(string(kind)+" provider extension", func(t *testing.T) {
			ref := session.EnvironmentRef{Kind: kind, ID: "opaque-provider-id", Revision: "inventory-r7"}
			provider := &compositionPlacementProvider{binding: server.PlacementBinding{
				Ref: ref,
				Environment: tool.MustEnvironment(
					ref,
					memfs.NewWorkspace("/private-provider-root"), nil,
				),
				Metadata: server.PlacementMetadata{Label: "Provider placement"},
			}}
			built, err := Build(context.Background(), Config{
				Workspace: rootForPlacementTest(t), UseMock: true,
				PlacementProvider: provider, PlacementScope: "deployment-a",
			})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			defer built.Close()
			if len(provider.calls) != 1 || provider.calls[0].Selector.Kind != server.PlacementSelectorDefault {
				t.Fatalf("startup Bind calls = %+v, want exactly one default Bind", provider.calls)
			}

			binding, err := built.Service.BindPlacement(context.Background(), server.DefaultPlacement(), server.PlacementOperationCreate)
			if err != nil {
				t.Fatalf("BindPlacement(default): %v", err)
			}
			if binding.Ref != ref {
				t.Fatalf("provider ref = %+v, want stable exact %+v", binding.Ref, ref)
			}
		})
	}

	t.Run("invalid default fails startup", func(t *testing.T) {
		provider := &compositionPlacementProvider{err: server.ErrPlacementUnavailable}
		built, err := Build(context.Background(), Config{
			Workspace: rootForPlacementTest(t), UseMock: true,
			PlacementProvider: provider, PlacementScope: "deployment-a",
		})
		if built != nil {
			built.Close()
			t.Fatal("Build returned a service for an invalid default placement")
		}
		if !errors.Is(err, server.ErrPlacementUnavailable) {
			t.Fatalf("Build error = %v, want ErrPlacementUnavailable", err)
		}
		if len(provider.calls) != 1 {
			t.Fatalf("startup Bind calls = %d, want 1", len(provider.calls))
		}
	})
}

func rootForPlacementTest(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}
