package server

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc/status"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func placementTestEnvironment(ref session.EnvironmentRef) tool.Environment {
	return tool.MustEnvironment(
		ref,
		memfs.NewWorkspace("/placement"),
		memledger.New(),
		nil,
	)
}

type placementProviderFunc func(context.Context, PlacementBindRequest) (PlacementBinding, error)

func (f placementProviderFunc) Bind(ctx context.Context, req PlacementBindRequest) (PlacementBinding, error) {
	return f(ctx, req)
}

func TestPlacementReadinessFailureIsBoundedAcrossPublicTransports(t *testing.T) {
	public := NewPlacementReadinessError(
		"verify",
		"artifact_verification",
		"microVM artifact verification failed, do not use the downloaded artifacts",
	)
	binder, err := NewPlacementBinder(placementProviderFunc(func(context.Context, PlacementBindRequest) (PlacementBinding, error) {
		return PlacementBinding{}, fmt.Errorf("private token SECRET at /private/runtime and exact-ref-123: %w", public)
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, bindErr := binder.Bind(t.Context(), PlacementBindRequest{Selector: DefaultPlacement(), Operation: PlacementOperationCreate, Scope: "tenant-a"})
	if bindErr == nil || !errors.Is(bindErr, ErrPlacementUnavailable) {
		t.Fatalf("Bind error = %v, want placement unavailable", bindErr)
	}

	grpcDetail := status.Convert(toStatus(bindErr)).Message()
	recorder := httptest.NewRecorder()
	writeServiceError(recorder, bindErr)
	httpDetail := recorder.Body.String()
	for surface, detail := range map[string]string{"grpc": grpcDetail, "http": httpDetail} {
		for _, want := range []string{"stage=verify", "category=artifact_verification", "cause=microVM artifact verification failed", "mecated microvm doctor", "diagnostics log"} {
			if !strings.Contains(detail, want) {
				t.Errorf("%s detail %q omitted %q", surface, detail, want)
			}
		}
		for _, secret := range []string{"SECRET", "/private/runtime", "exact-ref-123"} {
			if strings.Contains(detail, secret) {
				t.Errorf("%s detail leaked %q: %q", surface, secret, detail)
			}
		}
		if len(detail) > 1024 {
			t.Errorf("%s detail is unbounded: %d bytes", surface, len(detail))
		}
	}

	for _, cause := range []string{strings.Repeat("x", 300) + "/private", "credential SECRET must not cross"} {
		unsafe := NewPlacementReadinessError("verify", "artifact_verification", cause)
		if unsafe != ErrPlacementUnavailable {
			t.Fatalf("unsafe public cause was retained: %v", unsafe)
		}
	}
}

func TestADR_0291_BindRejectsRebindBetweenAuthorizationAndResolution(t *testing.T) {
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

func TestPlacementBinderTreatsRemoteKindsGenerically(t *testing.T) {
	for _, kind := range []session.EnvironmentKind{"microvm", "another-remote-backend"} {
		for _, root := range []string{"", "/host/source"} {
			name := string(kind) + "/empty"
			if root != "" {
				name = string(kind) + "/explicit"
			}
			t.Run(name, func(t *testing.T) {
				ref := session.EnvironmentRef{Kind: kind, ID: "opaque", Revision: "v1"}
				binder, err := NewPlacementBinder(placementProviderFunc(func(context.Context, PlacementBindRequest) (PlacementBinding, error) {
					return PlacementBinding{Ref: ref, Environment: placementTestEnvironment(ref), GovernanceRoot: root}, nil
				}))
				if err != nil {
					t.Fatal(err)
				}
				binding, err := binder.Bind(t.Context(), PlacementBindRequest{Selector: DefaultPlacement(), Operation: PlacementOperationCreate, Scope: "tenant-a"})
				if err != nil {
					t.Fatal(err)
				}
				got, err := PlacementGovernanceRoot(binding)
				if err != nil {
					t.Fatal(err)
				}
				if got != root {
					t.Fatalf("PlacementGovernanceRoot = %q, want %q", got, root)
				}
			})
		}
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
