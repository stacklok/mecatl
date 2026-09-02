package server

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func placementTestEnvironment(ref session.EnvironmentRef) tool.Environment {
	return tool.MustEnvironment(
		ref,
		memfs.NewWorkspace("/placement"),
		nil,
	)
}

type placementProviderFunc func(context.Context, PlacementBindRequest) (PlacementBinding, error)

func (f placementProviderFunc) Bind(ctx context.Context, req PlacementBindRequest) (PlacementBinding, error) {
	return f(ctx, req)
}

func TestADR_0288_BindRejectsRebindBetweenAuthorizationAndResolution(t *testing.T) {
	t.Parallel()

	authorized := make(chan struct{})
	continueBind := make(chan struct{})
	var mu sync.Mutex
	revision := "rev-1"
	environmentConstructions := 0

	provider := placementProviderFunc(func(_ context.Context, _ PlacementBindRequest) (PlacementBinding, error) {
		mu.Lock()
		authorizedRevision := revision
		mu.Unlock()
		close(authorized)
		<-continueBind

		mu.Lock()
		defer mu.Unlock()
		if revision != authorizedRevision {
			return PlacementBinding{}, ErrPlacementChanged
		}
		environmentConstructions++
		ref := session.EnvironmentRef{Kind: "worktree", ID: "wt-opaque-7", Revision: revision}
		return PlacementBinding{Environment: placementTestEnvironment(ref), Ref: ref}, nil
	})
	binder, err := NewPlacementBinder(provider)
	if err != nil {
		t.Fatalf("NewPlacementBinder: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, bindErr := binder.Bind(context.Background(), PlacementBindRequest{
			Selector:  SelectWorktree("source", session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "source", Revision: "rev-1"}, "opaque-token"),
			Operation: PlacementOperationCreate,
			Scope:     "tenant-a",
		})
		done <- bindErr
	}()
	<-authorized
	mu.Lock()
	revision = "rev-2"
	mu.Unlock()
	close(continueBind)

	if err := <-done; !errors.Is(err, ErrPlacementChanged) {
		t.Fatalf("Bind error = %v, want ErrPlacementChanged", err)
	}
	if environmentConstructions != 0 {
		t.Fatalf("environment constructions = %d, want 0 after revision race", environmentConstructions)
	}
}

func TestInvariant_server_owned_placement_ids_fail_closed(t *testing.T) {
	t.Parallel()

	providerCalls := 0
	binder, err := NewPlacementBinder(placementProviderFunc(func(context.Context, PlacementBindRequest) (PlacementBinding, error) {
		providerCalls++
		return PlacementBinding{}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	legacyID := PlacementSelector{Kind: "id", ID: "/private/legacy/path"}
	if _, err := binder.Bind(context.Background(), PlacementBindRequest{Selector: legacyID, Operation: PlacementOperationCreate, Scope: "tenant-a"}); !errors.Is(err, ErrInvalidPlacementSelection) {
		t.Fatalf("legacy placement ID = %v, want invalid selection", err)
	}
	if providerCalls != 0 {
		t.Fatalf("legacy placement ID reached provider %d time(s)", providerCalls)
	}
}

func TestPlacementBinderRejectsMismatchedOrUnsafeProviderOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		binding PlacementBinding
	}{
		{name: "nil workspace", binding: PlacementBinding{Ref: session.EnvironmentRef{Kind: "remote", ID: "r1", Revision: "v1"}}},
		{name: "missing revision", binding: PlacementBinding{Ref: session.EnvironmentRef{Kind: "remote", ID: "r1"}, Environment: placementTestEnvironment(session.EnvironmentRef{Kind: "remote", ID: "r1"})}},
		{name: "mismatched identity", binding: PlacementBinding{Ref: session.EnvironmentRef{Kind: "remote", ID: "r1", Revision: "v1"}, Environment: placementTestEnvironment(session.EnvironmentRef{Kind: "remote", ID: "r2", Revision: "v1"})}},
		{name: "mismatched revision", binding: PlacementBinding{Ref: session.EnvironmentRef{Kind: "remote", ID: "r1", Revision: "v1"}, Environment: placementTestEnvironment(session.EnvironmentRef{Kind: "remote", ID: "r1", Revision: "v2"})}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			binder, err := NewPlacementBinder(placementProviderFunc(func(context.Context, PlacementBindRequest) (PlacementBinding, error) {
				return tc.binding, nil
			}))
			if err != nil {
				t.Fatalf("NewPlacementBinder: %v", err)
			}
			if _, err := binder.Bind(context.Background(), PlacementBindRequest{Selector: DefaultPlacement(), Operation: PlacementOperationCreate, Scope: "tenant-a"}); !errors.Is(err, ErrInvalidPlacementBinding) {
				t.Fatalf("Bind error = %v, want ErrInvalidPlacementBinding", err)
			}
		})
	}
}
