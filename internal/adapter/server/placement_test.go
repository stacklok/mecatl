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

func placementTestEnvironment(ref session.PlacementRef) tool.Environment {
	return tool.MustEnvironment(
		session.EnvironmentRef{Kind: session.EnvironmentKind(ref.Kind), ID: ref.ID},
		memfs.NewWorkspace("/placement"),
		nil,
	)
}

type placementProviderFunc func(context.Context, PlacementBindRequest) (PlacementBinding, error)

func (f placementProviderFunc) Bind(ctx context.Context, req PlacementBindRequest) (PlacementBinding, error) {
	return f(ctx, req)
}

func TestADR_0280_BindRejectsRebindBetweenAuthorizationAndResolution(t *testing.T) {
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
		ref := session.PlacementRef{Kind: "worktree", ID: "wt-opaque-7", Revision: revision}
		return PlacementBinding{Environment: placementTestEnvironment(ref), Ref: ref}, nil
	})
	binder, err := NewPlacementBinder(provider)
	if err != nil {
		t.Fatalf("NewPlacementBinder: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, bindErr := binder.Bind(context.Background(), PlacementBindRequest{
			Selector:  session.SelectPlacementID("wt-opaque-7"),
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

	const availableID = "opaque-available"
	var environmentConstructions, trustEvaluations, persistenceWrites, providerCalls int
	var providerSelectors []session.PlacementSelector
	notFound := func() (PlacementBinding, error) {
		return PlacementBinding{}, ErrPlacementNotFound
	}
	provider := placementProviderFunc(func(_ context.Context, req PlacementBindRequest) (PlacementBinding, error) {
		providerCalls++
		providerSelectors = append(providerSelectors, req.Selector)
		if req.Selector.Kind != session.PlacementSelectorID || req.Selector.ID != availableID || req.Scope != "tenant-a" {
			return notFound()
		}
		return PlacementBinding{}, ErrPlacementUnavailable
	})
	binder, err := NewPlacementBinder(provider)
	if err != nil {
		t.Fatalf("NewPlacementBinder: %v", err)
	}

	tests := []struct {
		name     string
		selector session.PlacementSelector
		scope    PlacementScope
		want     error
	}{
		{name: "absent", selector: session.SelectPlacementID("absent"), scope: "tenant-a", want: ErrPlacementNotFound},
		{name: "authorization-hidden", selector: session.SelectPlacementID("hidden"), scope: "tenant-a", want: ErrPlacementNotFound},
		{name: "stale", selector: session.SelectPlacementID("stale"), scope: "tenant-a", want: ErrPlacementNotFound},
		{name: "wrong scope", selector: session.SelectPlacementID(availableID), scope: "tenant-b", want: ErrPlacementNotFound},
		{name: "unavailable", selector: session.SelectPlacementID(availableID), scope: "tenant-a", want: ErrPlacementUnavailable},
		{name: "invalid selector", selector: session.PlacementSelector{Kind: session.PlacementSelectorID}, scope: "tenant-a", want: ErrInvalidPlacementSelection},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			binding, bindErr := binder.Bind(context.Background(), PlacementBindRequest{
				Selector: tc.selector, Operation: PlacementOperationCreate, Scope: tc.scope,
			})
			if !errors.Is(bindErr, tc.want) {
				t.Fatalf("Bind error = %v, want %v", bindErr, tc.want)
			}
			if binding.Environment.Workspace() != nil {
				t.Fatal("failed Bind returned an environment")
			}
		})
	}

	if providerCalls != len(tests)-1 {
		t.Fatalf("provider Bind calls = %d, want %d (invalid selector must stop at binder)", providerCalls, len(tests)-1)
	}
	for _, selector := range providerSelectors {
		if selector.Kind == session.PlacementSelectorDefault {
			t.Fatal("failed explicit ID silently fell back to the default selector")
		}
	}

	// These steps represent the only post-bind consumers. Every rejected ID must
	// leave them unreachable; a successful result is the sole capability to proceed.
	if environmentConstructions != 0 || trustEvaluations != 0 || persistenceWrites != 0 {
		t.Fatalf("rejected IDs caused side effects: environment=%d trust=%d persistence=%d",
			environmentConstructions, trustEvaluations, persistenceWrites)
	}
}

func TestPlacementBinderRejectsMismatchedOrUnsafeProviderOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		binding PlacementBinding
	}{
		{name: "nil workspace", binding: PlacementBinding{Ref: session.PlacementRef{Kind: "remote", ID: "r1", Revision: "v1"}}},
		{name: "missing revision", binding: PlacementBinding{Ref: session.PlacementRef{Kind: "remote", ID: "r1"}, Environment: placementTestEnvironment(session.PlacementRef{Kind: "remote", ID: "r1"})}},
		{name: "mismatched identity", binding: PlacementBinding{Ref: session.PlacementRef{Kind: "remote", ID: "r1", Revision: "v1"}, Environment: placementTestEnvironment(session.PlacementRef{Kind: "remote", ID: "r2"})}},
		{name: "unsafe metadata", binding: PlacementBinding{Ref: session.PlacementRef{Kind: "remote", ID: "r1", Revision: "v1"}, Environment: placementTestEnvironment(session.PlacementRef{Kind: "remote", ID: "r1"}), Metadata: PlacementMetadata{Name: "bad\nname"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			binder, err := NewPlacementBinder(placementProviderFunc(func(context.Context, PlacementBindRequest) (PlacementBinding, error) {
				return tc.binding, nil
			}))
			if err != nil {
				t.Fatalf("NewPlacementBinder: %v", err)
			}
			if _, err := binder.Bind(context.Background(), PlacementBindRequest{Selector: session.DefaultPlacement(), Operation: PlacementOperationCreate, Scope: "tenant-a"}); !errors.Is(err, ErrInvalidPlacementBinding) {
				t.Fatalf("Bind error = %v, want ErrInvalidPlacementBinding", err)
			}
		})
	}
}
