package server_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memschedulestore"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type healthStore struct {
	*memstore.Store
	calls int
	err   error
}

func (s *healthStore) SessionStorageHealth(context.Context) (port.SessionStorageHealth, error) {
	s.calls++
	if s.err != nil {
		return port.SessionStorageHealth{}, s.err
	}
	return port.SessionStorageHealth{Available: true, CurrentBytesAvailable: true, CurrentBytes: 7, SessionCount: 2}, nil
}

type pagingOverrideStore struct {
	*healthStore
	page func(context.Context, port.SessionMetadataPageRequest) (port.SessionMetadataPage, error)
}

func (s *pagingOverrideStore) PageSessionMetadata(ctx context.Context, req port.SessionMetadataPageRequest) (port.SessionMetadataPage, error) {
	return s.page(ctx, req)
}

func storageHealthService(t *testing.T, store port.SessionStore, authorize func(context.Context) bool) *server.Service {
	return storageHealthServiceWithUpdate(t, store, authorize, nil)
}

func storageHealthServiceWithUpdate(t *testing.T, store port.SessionStore, authorize func(context.Context) bool, update func(server.StorageMaintenanceEvent)) *server.Service {
	t.Helper()
	llm := mockllm.New()
	svc, err := newPlacementTestService(server.Config{
		Engine:                      agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil)}),
		Store:                       store,
		StorageManagementAuthorized: authorize,
		StorageMaintenanceUpdate:    update,
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

func TestStorageHealthIncludesOwnerlessCutoverInventory(t *testing.T) {
	ctx := context.Background()
	sessions := memstore.New()
	schedules := memschedulestore.New()
	alice := &session.Principal{Issuer: "https://issuer.example", Subject: "alice", GrantType: session.GrantTypeUser}

	legacy := session.New("legacy-session", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/legacy", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	owned := session.New("owned-session", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/owned", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	if err := owned.RestoreLabels(alice, session.Authority{}); err != nil {
		t.Fatal(err)
	}
	for _, sess := range []*session.Session{legacy, owned} {
		if err := sessions.Save(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}
	if err := schedules.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{Name: "legacy-schedule"}}); err != nil {
		t.Fatal(err)
	}
	if err := schedules.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{Name: "owned-schedule", Owner: alice}}); err != nil {
		t.Fatal(err)
	}

	svc, err := newPlacementTestService(server.Config{
		Engine:                      agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil)}),
		Store:                       sessions,
		ScheduleManager:             server.NewScheduleManager(server.ScheduleManagerConfig{Store: sessions, ScheduleStore: schedules}),
		StorageManagementAuthorized: func(context.Context) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := svc.StorageHealth(ctx)
	if err != nil {
		t.Fatalf("StorageHealth: %v", err)
	}
	inventory := status.Ownerless
	if !inventory.SessionsAvailable || inventory.SessionCount != 1 || len(inventory.SessionIDs) != 1 || inventory.SessionIDs[0] != "legacy-session" {
		t.Fatalf("ownerless session inventory = %+v", inventory)
	}
	if !inventory.SchedulesAvailable || inventory.ScheduleCount != 1 || len(inventory.ScheduleNames) != 1 || inventory.ScheduleNames[0] != "legacy-schedule" {
		t.Fatalf("ownerless schedule inventory = %+v", inventory)
	}
}

func TestStorageHealthReportsUnsupportedOwnerlessInventoryPerFamily(t *testing.T) {
	store := &pagingOverrideStore{
		healthStore: &healthStore{Store: memstore.New()},
		page: func(context.Context, port.SessionMetadataPageRequest) (port.SessionMetadataPage, error) {
			return port.SessionMetadataPage{}, port.ErrSessionMetadataPagingUnsupported
		},
	}
	svc := storageHealthService(t, store, func(context.Context) bool { return true })
	status, err := svc.StorageHealth(context.Background())
	if err != nil {
		t.Fatalf("StorageHealth: %v", err)
	}
	if !status.Available || status.Ownerless.SessionsAvailable || status.Ownerless.SessionsUnavailableReason != "backend_unsupported" {
		t.Fatalf("storage health with unsupported ownerless session inventory = %+v", status)
	}
}

func TestStorageHealthRejectsNonProgressingOwnerlessPager(t *testing.T) {
	store := &pagingOverrideStore{
		healthStore: &healthStore{Store: memstore.New()},
		page: func(context.Context, port.SessionMetadataPageRequest) (port.SessionMetadataPage, error) {
			return port.SessionMetadataPage{NextCursor: &port.SessionMetadataCursor{Generation: "stuck"}}, nil
		},
	}
	svc := storageHealthService(t, store, func(context.Context) bool { return true })
	if _, err := svc.StorageHealth(context.Background()); !errors.Is(err, server.ErrStorageHealthBackend) {
		t.Fatalf("StorageHealth = %v, want ErrStorageHealthBackend", err)
	}
}

func TestStorageHealthBackendFailureIsSanitized(t *testing.T) {
	raw := errors.New("read /private/store: OPENROUTER_API_KEY=secret")
	store := &healthStore{Store: memstore.New(), err: raw}
	var event server.StorageMaintenanceEvent
	svc := storageHealthServiceWithUpdate(t, store, func(context.Context) bool { return true }, func(got server.StorageMaintenanceEvent) {
		event = got
	})
	_, err := svc.StorageHealth(context.Background())
	if !errors.Is(err, server.ErrStorageHealthBackend) || strings.Contains(err.Error(), "/private/store") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("StorageHealth error = %v", err)
	}
	if event.State != server.StorageMaintenanceFailed || event.Failure != "health: storage backend unavailable" || strings.Contains(event.Failure, "private") {
		t.Fatalf("health failure lifecycle = %+v", event)
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

// TestOwnerlessInventoryToleratesAnEmptyPageThatAdvances pins that the cutover
// inventory treats a NO-PROGRESS CURSOR as the backend fault, not an empty page.
//
// Rows the walk has already passed can be deleted under it — retention and the
// child GC run concurrently with an operator's preflight — so a page can come
// back empty while the cursor legitimately advances. Failing there turned a
// normal race into "storage health unavailable", which is exactly the signal an
// operator consults to decide whether an OIDC cutover is safe.
func TestOwnerlessInventoryToleratesAnEmptyPageThatAdvances(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	call := 0
	store := &pagingOverrideStore{
		healthStore: &healthStore{},
		page: func(_ context.Context, _ port.SessionMetadataPageRequest) (port.SessionMetadataPage, error) {
			call++
			switch call {
			case 1:
				next := port.SessionMetadataCursor{ModifiedAt: base, ID: "a"}
				return port.SessionMetadataPage{
					Sessions:   []port.SessionDiscoveryMeta{{ID: "a", ModifiedAt: base}},
					NextCursor: &next,
				}, nil
			case 2:
				// Emptied by a concurrent sweep, but the cursor MOVED.
				next := port.SessionMetadataCursor{ModifiedAt: base.Add(time.Second), ID: "b"}
				return port.SessionMetadataPage{NextCursor: &next}, nil
			default:
				return port.SessionMetadataPage{}, nil
			}
		},
	}
	svc := storageHealthService(t, store, func(context.Context) bool { return true })

	health, err := svc.StorageHealth(context.Background())
	if err != nil {
		t.Fatalf("an empty page with an advancing cursor was reported as a backend failure: %v", err)
	}
	if !health.Ownerless.SessionsAvailable {
		t.Fatal("inventory did not complete")
	}
	if health.Ownerless.SessionCount != 1 {
		t.Fatalf("ownerless session count = %d, want the one ownerless row seen before the empty page", health.Ownerless.SessionCount)
	}
}

// TestOwnerlessInventoryStillRefusesAStalledCursor is the negative control: a
// pager that returns the SAME cursor is a genuine stall and must still fail,
// so the fix above did not simply remove the bound.
func TestOwnerlessInventoryStillRefusesAStalledCursor(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	stuck := port.SessionMetadataCursor{ModifiedAt: base, ID: "a"}
	store := &pagingOverrideStore{
		healthStore: &healthStore{},
		page: func(_ context.Context, _ port.SessionMetadataPageRequest) (port.SessionMetadataPage, error) {
			next := stuck
			return port.SessionMetadataPage{
				Sessions:   []port.SessionDiscoveryMeta{{ID: "a", ModifiedAt: base}},
				NextCursor: &next,
			}, nil
		},
	}
	svc := storageHealthService(t, store, func(context.Context) bool { return true })

	if _, err := svc.StorageHealth(context.Background()); err == nil {
		t.Fatal("a pager whose cursor never advances was accepted; the walk would loop forever")
	}
}
