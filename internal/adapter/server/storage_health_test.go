package server_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type healthStore struct {
	*memstore.Store
	calls int
}

func (s *healthStore) SessionStorageHealth(context.Context) (port.SessionStorageHealth, error) {
	s.calls++
	return port.SessionStorageHealth{Available: true, CurrentBytesAvailable: true, CurrentBytes: 7, SessionCount: 2}, nil
}

func storageHealthService(t *testing.T, store port.SessionStore, authorize func(context.Context) bool) *server.Service {
	t.Helper()
	llm := mockllm.New()
	svc, err := server.NewService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil)}),
		Store:  store, Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		StorageManagementAuthorized: authorize,
		RetentionPolicy:             server.RetentionPolicy{MainMaxAge: 24 * time.Hour, MainMaxCount: 4, SweepCadence: time.Hour},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func TestStorageHealthManagementAuthorizationAndNoLeak(t *testing.T) {
	store := &healthStore{Store: memstore.New()}
	svc := storageHealthService(t, store, func(context.Context) bool { return false })
	_, err := svc.StorageHealth(context.Background())
	if !errors.Is(err, server.ErrManagementUnauthorized) {
		t.Fatalf("StorageHealth error = %v, want ErrManagementUnauthorized", err)
	}
	if store.calls != 0 {
		t.Fatalf("unauthorized health request reached backend %d times", store.calls)
	}
	for _, forbidden := range []string{"session-id", "/secret", "owner", "transcript"} {
		if strings.Contains(strings.ToLower(err.Error()), forbidden) {
			t.Fatalf("authorization error leaked %q: %v", forbidden, err)
		}
	}
}

func TestStorageHealthUnsupportedBackendIsHonest(t *testing.T) {
	svc := storageHealthService(t, memstore.New(), func(context.Context) bool { return true })
	status, err := svc.StorageHealth(context.Background())
	if err != nil {
		t.Fatalf("StorageHealth: %v", err)
	}
	if status.Available || status.UnavailableReason != "backend_unsupported" || status.CurrentBytesAvailable {
		t.Fatalf("unsupported backend status = %+v", status)
	}
}
