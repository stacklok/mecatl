package app

import (
	"context"
	"errors"
	"path/filepath"
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

func TestADR_0280_CompositionConfiguresProviderOwnedPlacements(t *testing.T) {
	t.Parallel()

	t.Run("selector vocabulary is closed", func(t *testing.T) {
		selectors := []session.PlacementSelector{
			session.DefaultPlacement(),
			session.NoFSPlacement(),
			session.SelectPlacementID("opaque-id"),
		}
		for _, selector := range selectors {
			if !selector.Valid() {
				t.Fatalf("canonical selector %+v is invalid", selector)
			}
		}
		if (session.PlacementSelector{Kind: "path", ID: "/server/root"}).Valid() {
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

		binding, err := built.Service.BindPlacement(context.Background(), session.DefaultPlacement(), server.PlacementOperationCreate)
		if err != nil {
			t.Fatalf("BindPlacement(default): %v", err)
		}
		if binding.Ref.Kind != "local" || binding.Ref.ID == "" || binding.Ref.Revision == "" {
			t.Fatalf("default ref = %+v, want complete local provider ref", binding.Ref)
		}
		if binding.Ref.ID == root || filepath.IsAbs(binding.Ref.ID) {
			t.Fatalf("default public ID %q exposes or derives authority from root %q", binding.Ref.ID, root)
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
				Metadata: server.PlacementMetadata{Name: "Provider placement"},
			}}
			built, err := Build(context.Background(), Config{
				Workspace: rootForPlacementTest(t), UseMock: true,
				PlacementProvider: provider, PlacementScope: "deployment-a",
			})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			defer built.Close()
			if len(provider.calls) != 1 || provider.calls[0].Selector.Kind != session.PlacementSelectorDefault {
				t.Fatalf("startup Bind calls = %+v, want exactly one default Bind", provider.calls)
			}

			binding, err := built.Service.BindPlacement(context.Background(), session.SelectPlacementID(ref.ID), server.PlacementOperationCreate)
			if err != nil {
				t.Fatalf("BindPlacement(id): %v", err)
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
