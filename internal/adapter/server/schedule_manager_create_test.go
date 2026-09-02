package server_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
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

// noCreatorStore wraps a *memschedulestore.Store via a NAMED field (never an
// embedded one — Go only promotes methods through embedding), so it satisfies
// port.ScheduleStore WITHOUT satisfying the OPTIONAL port.ScheduleCreator seam.
// It exists solely to exercise CreateSchedule's fallback check-then-Save path
// (review finding 5, issue #368): every real backend this repo ships
// (memschedulestore/jsonlstore/redisstore) now implements ScheduleCreator, so
// without this fake the fallback branch would go untested.
type noCreatorStore struct {
	inner *memschedulestore.Store
}

func newNoCreatorStore() *noCreatorStore { return &noCreatorStore{inner: memschedulestore.New()} }

func (n *noCreatorStore) Save(ctx context.Context, s port.Schedule) error {
	return n.inner.Save(ctx, s)
}
func (n *noCreatorStore) Load(ctx context.Context, name string) (port.Schedule, error) {
	return n.inner.Load(ctx, name)
}
func (n *noCreatorStore) Delete(ctx context.Context, name string) error {
	return n.inner.Delete(ctx, name)
}
func (n *noCreatorStore) List(ctx context.Context) ([]port.Schedule, error) { return n.inner.List(ctx) }
func (n *noCreatorStore) Due(ctx context.Context, now time.Time) ([]port.Schedule, error) {
	return n.inner.Due(ctx, now)
}
func (n *noCreatorStore) Claim(ctx context.Context, name string, now, nextFire time.Time) (port.Schedule, error) {
	return n.inner.Claim(ctx, name, now, nextFire)
}
func (n *noCreatorStore) ClaimNow(ctx context.Context, name string, now, nextFire time.Time) (port.Schedule, error) {
	return n.inner.ClaimNow(ctx, name, now, nextFire)
}
func (n *noCreatorStore) SetEnabled(ctx context.Context, name string, enabled bool) error {
	return n.inner.SetEnabled(ctx, name, enabled)
}
func (n *noCreatorStore) RecordFire(ctx context.Context, f port.ScheduleFire) error {
	return n.inner.RecordFire(ctx, f)
}
func (n *noCreatorStore) RecordFireStart(ctx context.Context, name string, fire port.ScheduleFire) error {
	return n.inner.RecordFireStart(ctx, name, fire)
}
func (n *noCreatorStore) RecordFireProgress(ctx context.Context, name, fireID string, at time.Time) error {
	return n.inner.RecordFireProgress(ctx, name, fireID, at)
}
func (n *noCreatorStore) LoadFire(ctx context.Context, fireID string) (port.ScheduleFire, error) {
	return n.inner.LoadFire(ctx, fireID)
}
func (n *noCreatorStore) ListFires(ctx context.Context, scheduleName string) ([]port.ScheduleFire, error) {
	return n.inner.ListFires(ctx, scheduleName)
}

// Deliberately NOT implementing port.ScheduleCreator — that is the entire
// point of this fake.
var _ port.ScheduleStore = (*noCreatorStore)(nil)

func newManagerBackedBy(t *testing.T, store port.ScheduleStore, now time.Time) *server.Service {
	t.Helper()
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
	})
	sessions := memstore.New()
	svc, err := newPlacementTestService(server.Config{
		Engine:           engine,
		Store:            sessions,
		ScheduleManager:  server.NewScheduleManager(server.ScheduleManagerConfig{Store: sessions, ScheduleStore: store, Now: func() time.Time { return now }}),
		SharedEngineRoot: "/tmp",

		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

func testSchedule(name string) port.ScheduleSpec {
	return port.ScheduleSpec{
		Name:     name,
		Prompt:   "do work",
		Trigger:  port.TriggerSpec{Cron: "* * * * *"},
		Mutating: true,
	}
}

func TestOwnerlessPhysicalLookingScheduleNameRoundTrips(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	svc := newManagerBackedBy(t, memschedulestore.New(), now)
	digest := sha256.Sum256([]byte("https://issuer.example\x00alice"))
	name := fmt.Sprintf("schedule/%x\x00nightly", digest[:])
	ctx := context.Background()

	created, err := svc.CreateSchedule(ctx, testSchedule(name))
	if err != nil || created.Spec.Name != name {
		t.Fatalf("CreateSchedule = (%q, %v), want byte-exact name %q", created.Spec.Name, err, name)
	}
	loaded, err := svc.GetSchedule(ctx, name)
	if err != nil || loaded.Spec.Name != name {
		t.Fatalf("GetSchedule = (%q, %v), want byte-exact name %q", loaded.Spec.Name, err, name)
	}
	listed, err := svc.ListSchedules(ctx)
	if err != nil || len(listed) != 1 || listed[0].Spec.Name != name {
		t.Fatalf("ListSchedules = (%+v, %v), want byte-exact name %q", listed, err, name)
	}
	updatedSpec := testSchedule(name)
	updatedSpec.Prompt = "updated"
	updated, err := svc.UpdateSchedule(ctx, updatedSpec)
	if err != nil || updated.Spec.Name != name || updated.Spec.Prompt != "updated" {
		t.Fatalf("UpdateSchedule = (%+v, %v), want same literal name and updated prompt", updated, err)
	}
	if err := svc.PauseSchedule(ctx, name); err != nil {
		t.Fatalf("PauseSchedule: %v", err)
	}
	if err := svc.ResumeSchedule(ctx, name); err != nil {
		t.Fatalf("ResumeSchedule: %v", err)
	}
	if err := svc.DeleteSchedule(ctx, name); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}
	if _, err := svc.GetSchedule(ctx, name); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Fatalf("GetSchedule after delete = %v, want schedule not found", err)
	}
}

// TestCreateScheduleUsesAtomicCreateWhenAvailable pins the manager's happy
// path (review finding 5, issue #368): when the backing store implements the
// OPTIONAL port.ScheduleCreator seam (memschedulestore does), concurrent
// same-name creates through the manager yield exactly one winner and N-1
// ErrInvalidArgument duplicate-name errors — never two silent successes. This
// is the manager-level proof that CreateSchedule routes through Create rather
// than the racy check-then-Save fallback when the seam is available.
func TestCreateScheduleUsesAtomicCreateWhenAvailable(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := memschedulestore.New()
	if _, ok := port.ScheduleStore(store).(port.ScheduleCreator); !ok {
		t.Fatal("memschedulestore.Store no longer implements port.ScheduleCreator (test precondition broken)")
	}
	svc := newManagerBackedBy(t, store, now)
	ctx := context.Background()

	const n = 8
	var wg sync.WaitGroup
	results := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.CreateSchedule(ctx, testSchedule("concurrent-atomic"))
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	var wins, dupErrors int
	for err := range results {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, server.ErrInvalidArgument):
			dupErrors++
		default:
			t.Fatalf("CreateSchedule (concurrent) = %v, want nil or ErrInvalidArgument", err)
		}
	}
	if wins != 1 {
		t.Fatalf("wins = %d, want exactly 1 (atomic create — no silent overwrite)", wins)
	}
	if dupErrors != n-1 {
		t.Fatalf("dupErrors = %d, want %d", dupErrors, n-1)
	}
}

func TestCreateScheduleRejectsNonAtomicStoreWhenOwnershipEnforced(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newNoCreatorStore()
	mgr := server.NewScheduleManager(server.ScheduleManagerConfig{
		Store:             memstore.New(),
		ScheduleStore:     store,
		OwnershipEnforced: true,
		Now:               func() time.Time { return now },
	})
	ctx := session.WithPrincipal(context.Background(), &session.Principal{
		Issuer:    "https://issuer.example",
		Subject:   "alice",
		GrantType: session.GrantTypeUser,
	})

	_, err := mgr.CreateSchedule(ctx, testSchedule("must-be-atomic"))
	if !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("CreateSchedule = %v, want ErrFailedPrecondition", err)
	}
	if _, err := store.Load(context.Background(), "must-be-atomic"); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Fatalf("store.Load after rejected create = %v, want ErrScheduleNotFound", err)
	}
}

// TestCreateScheduleFallsBackToCheckThenSaveWithoutCreator pins the
// degradation path: a store that does NOT implement port.ScheduleCreator still
// gets ordinary (non-concurrent) duplicate-name rejection via the pre-existing
// check-then-Save fallback — byte-identical caller-visible behavior to the
// atomic path for the sequential case the fallback is scoped to.
func TestCreateScheduleFallsBackToCheckThenSaveWithoutCreator(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newNoCreatorStore()
	if _, ok := port.ScheduleStore(store).(port.ScheduleCreator); ok {
		t.Fatal("noCreatorStore unexpectedly implements port.ScheduleCreator (test precondition broken)")
	}
	svc := newManagerBackedBy(t, store, now)
	ctx := context.Background()

	if _, err := svc.CreateSchedule(ctx, testSchedule("fallback-dup")); err != nil {
		t.Fatalf("first CreateSchedule: %v", err)
	}
	if _, err := svc.CreateSchedule(ctx, testSchedule("fallback-dup")); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("second CreateSchedule (duplicate) = %v, want ErrInvalidArgument", err)
	}
}
