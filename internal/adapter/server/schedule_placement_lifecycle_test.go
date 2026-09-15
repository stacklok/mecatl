package server_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memschedulestore"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

type schedulePlacementProvider struct {
	binds, reattaches, closes, deletes int
	refs                               []session.EnvironmentRef
	retained                           bool
	deleteErr, closeErr                error
}

func schedulePlacementBinding(ref session.EnvironmentRef, closeFn func() error) server.PlacementBinding {
	env := tool.MustEnvironment(ref, memfs.NewWorkspace("/workspace"), memledger.New(), nil)
	return server.PlacementBinding{Ref: ref, Environment: env, CompositionRoot: "/host", Close: closeFn}
}

func (p *schedulePlacementProvider) Bind(_ context.Context, req server.PlacementBindRequest) (server.PlacementBinding, error) {
	if req.Selector.IsNoFS() {
		ref := session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "no-fs", Revision: "nofs-v1"}
		env := tool.MustEnvironment(ref, nofs.New(), memledger.New(), nil)
		return server.PlacementBinding{Ref: ref, Environment: env}, nil
	}
	p.binds++
	ref := session.EnvironmentRef{Kind: "microvm", ID: fmt.Sprintf("schedule-%d.logical", p.binds), Revision: "1"}
	p.refs = append(p.refs, ref)
	return schedulePlacementBinding(ref, func() error { p.closes++; return p.closeErr }), nil
}

func (p *schedulePlacementProvider) Reattach(_ context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	p.reattaches++
	return schedulePlacementBinding(req.Ref, nil), nil
}

func (p *schedulePlacementProvider) DeletePlacement(_ context.Context, req server.PlacementDeleteRequest) (server.PlacementDeleteResult, error) {
	p.deletes++
	if req.Scope != "test" || !req.Ref.Valid() {
		return server.PlacementDeleteResult{}, server.ErrPlacementNotFound
	}
	return server.PlacementDeleteResult{Retained: p.retained}, p.deleteErr
}

func newSchedulePlacementService(t *testing.T, provider server.PlacementProvider) (*server.Service, port.ScheduleStore) {
	t.Helper()
	store, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Model: "test", Store: store})
	svc, err := server.NewService(server.Config{Engine: eng, Store: store, PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: "/host", Now: func() time.Time { return time.Unix(1_700_000_000, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	return svc, store.ScheduleStore()
}

func schedulePlacementSpec(name string) port.ScheduleSpec {
	return port.ScheduleSpec{Name: name, Prompt: "write marker", Trigger: port.TriggerSpec{Cron: "* * * * *"}, Mutating: true}
}

func TestScheduleClaimHandsPlacementToFireSession(t *testing.T) {
	for _, claim := range []struct {
		name string
		run  func(context.Context, port.ScheduleStore, string, time.Time) (port.Schedule, error)
	}{
		{name: "claim", run: func(ctx context.Context, store port.ScheduleStore, name string, now time.Time) (port.Schedule, error) {
			return store.Claim(ctx, name, now.Add(time.Minute), now.Add(2*time.Minute))
		}},
		{name: "claim-now", run: func(ctx context.Context, store port.ScheduleStore, name string, now time.Time) (port.Schedule, error) {
			return store.ClaimNow(ctx, name, now.Add(time.Second), now.Add(2*time.Minute))
		}},
	} {
		t.Run(claim.name, func(t *testing.T) {
			provider := &schedulePlacementProvider{}
			svc, store := newSchedulePlacementService(t, provider)
			ctx := t.Context()
			created, err := svc.CreateSchedule(ctx, schedulePlacementSpec(claim.name))
			if err != nil {
				t.Fatal(err)
			}
			now := time.Unix(1_700_000_000, 0)
			claimed, err := claim.run(ctx, store, claim.name, now)
			if err != nil {
				t.Fatal(err)
			}
			if claimed.State.FireCount != 1 {
				t.Fatalf("FireCount=%d, want 1", claimed.State.FireCount)
			}
			if err := store.RecordFire(ctx, port.ScheduleFire{ID: claim.name + "-fire", ScheduleName: claim.name, SessionID: "sched--historic", FiredAt: now, Stop: session.StopEndTurn}); err != nil {
				t.Fatal(err)
			}
			if err := svc.DeleteSchedule(ctx, claim.name); err != nil {
				t.Fatal(err)
			}
			if provider.deletes != 0 {
				t.Fatalf("historical fire placement was deleted %d times", provider.deletes)
			}
			if _, err := store.Load(ctx, claim.name); !errors.Is(err, port.ErrScheduleNotFound) {
				t.Fatalf("schedule record survived: %v", err)
			}
			if created.Spec.EnvironmentRef != claimed.Spec.EnvironmentRef {
				t.Fatal("claim changed exact placement")
			}
		})
	}
}

func TestScheduleIndependentPlacementAllocatedOnceAndCleaned(t *testing.T) {
	provider := &schedulePlacementProvider{}
	svc, store := newSchedulePlacementService(t, provider)
	ctx := t.Context()

	created, err := svc.CreateSchedule(ctx, schedulePlacementSpec("owned"))
	if err != nil {
		t.Fatal(err)
	}
	if !created.Spec.PlacementOwned || provider.binds != 1 || provider.closes != 1 {
		t.Fatalf("created ownership/binds/closes = %v/%d/%d", created.Spec.PlacementOwned, provider.binds, provider.closes)
	}
	if _, err := svc.CreateSchedule(ctx, schedulePlacementSpec("owned")); err == nil {
		t.Fatal("duplicate create succeeded")
	}
	if provider.binds != 1 {
		t.Fatalf("duplicate allocated a placement: binds=%d", provider.binds)
	}
	updatedSpec := schedulePlacementSpec("owned")
	updatedSpec.Prompt = "updated"
	forged := updatedSpec
	forged.EnvironmentRef = session.EnvironmentRef{Kind: "microvm", ID: "other.logical", Revision: "1"}
	if _, err := svc.UpdateSchedule(ctx, forged); err == nil {
		t.Fatal("update accepted a replacement exact placement")
	}
	updated, err := svc.UpdateSchedule(ctx, updatedSpec)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Spec.PlacementOwned || updated.Spec.EnvironmentRef != created.Spec.EnvironmentRef {
		t.Fatalf("update changed placement ownership: %+v", updated.Spec)
	}
	if err := svc.DeleteSchedule(ctx, "owned"); err != nil {
		t.Fatal(err)
	}
	if provider.deletes != 1 {
		t.Fatalf("placement deletes=%d, want 1", provider.deletes)
	}
	if _, err := store.Load(ctx, "owned"); err == nil {
		t.Fatal("schedule survived successful placement cleanup")
	}
}

func TestScheduleCleanupFailureRetainsDisabledExactPlacement(t *testing.T) {
	provider := &schedulePlacementProvider{deleteErr: fmt.Errorf("cleanup unavailable")}
	svc, store := newSchedulePlacementService(t, provider)
	created, err := svc.CreateSchedule(t.Context(), schedulePlacementSpec("retry-cleanup"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteSchedule(t.Context(), "retry-cleanup"); err == nil {
		t.Fatal("cleanup failure was hidden")
	}
	retained, err := store.Load(t.Context(), "retry-cleanup")
	if err != nil {
		t.Fatal(err)
	}
	if retained.State.Enabled || retained.State.DeletionID == "" || retained.Spec.EnvironmentRef != created.Spec.EnvironmentRef || !retained.Spec.PlacementOwned {
		t.Fatalf("retained schedule = %+v", retained)
	}
	binds := provider.binds
	if _, err := svc.CreateSchedule(t.Context(), schedulePlacementSpec("retry-cleanup")); err == nil {
		t.Fatal("same-name create succeeded while deletion was pending")
	}
	if provider.binds != binds {
		t.Fatal("same-name create allocated a replacement placement")
	}
	if _, err := svc.UpdateSchedule(t.Context(), schedulePlacementSpec("retry-cleanup")); err == nil {
		t.Fatal("update succeeded while deletion was pending")
	}
	if err := svc.ResumeSchedule(t.Context(), "retry-cleanup"); err == nil {
		t.Fatal("resume succeeded while deletion was pending")
	}
	if err := svc.PauseSchedule(t.Context(), "retry-cleanup"); err == nil {
		t.Fatal("pause succeeded while deletion was pending")
	}
	if _, err := store.ClaimNow(t.Context(), "retry-cleanup", time.Unix(1_700_000_100, 0), time.Time{}); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Fatalf("claim during deletion = %v", err)
	}
	provider.deleteErr = nil
	if err := svc.DeleteSchedule(t.Context(), "retry-cleanup"); err != nil {
		t.Fatal(err)
	}
	if provider.deletes != 2 {
		t.Fatalf("cleanup attempts=%d, want 2", provider.deletes)
	}
}

func TestScheduleLegacyAndNoFSPlacementsAreNeverDeleted(t *testing.T) {
	provider := &schedulePlacementProvider{}
	svc, store := newSchedulePlacementService(t, provider)
	ctx := t.Context()

	noFS := schedulePlacementSpec("no-fs")
	noFS.Profile = string(server.ProfileNoFS)
	created, err := svc.CreateSchedule(ctx, noFS)
	if err != nil {
		t.Fatal(err)
	}
	if created.Spec.PlacementOwned {
		t.Fatal("no-fs schedule marked placement-owned")
	}
	if err := svc.DeleteSchedule(ctx, "no-fs"); err != nil {
		t.Fatal(err)
	}

	legacy := port.Schedule{Spec: schedulePlacementSpec("legacy"), State: port.ScheduleState{Enabled: true, LastFireSessionID: port.PendingFireSessionID}}
	legacy.Spec.EnvironmentRef = session.EnvironmentRef{Kind: "microvm", ID: "legacy.logical", Revision: "1"}
	legacy.Spec.PlacementScope = "test"
	if err := store.Save(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteSchedule(ctx, "legacy"); err != nil {
		t.Fatal(err)
	}
	if provider.deletes != 0 {
		t.Fatalf("destructive cleanup called for no-fs/legacy placement %d times", provider.deletes)
	}
}

func TestScheduleDeleteDisablesBeforeActiveFireCleanup(t *testing.T) {
	provider := &schedulePlacementProvider{}
	svc, store := newSchedulePlacementService(t, provider)
	ctx := t.Context()
	created, err := svc.CreateSchedule(ctx, schedulePlacementSpec("active"))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Load(ctx, "active")
	if err != nil {
		t.Fatal(err)
	}
	stored.State.LastFireSessionID = port.PendingFireSessionID
	if err := store.Delete(ctx, "active"); err != nil {
		t.Fatal(err)
	}
	if creator, ok := store.(port.ScheduleCreator); ok {
		if err := creator.Create(ctx, stored); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Fatal("test store lacks atomic creator")
	}
	if err := svc.DeleteSchedule(ctx, "active"); err == nil {
		t.Fatal("active schedule deletion succeeded")
	}
	remaining, err := store.Load(ctx, "active")
	if err != nil {
		t.Fatal(err)
	}
	if !remaining.State.Enabled || provider.deletes != 0 || remaining.Spec.EnvironmentRef != created.Spec.EnvironmentRef {
		t.Fatalf("active delete state enabled/deletes/ref = %v/%d/%+v", remaining.State.Enabled, provider.deletes, remaining.Spec.EnvironmentRef)
	}
}

type claimBeforeBeginScheduleStore struct {
	*memschedulestore.Store
	now time.Time
}

func (s *claimBeforeBeginScheduleStore) BeginDelete(ctx context.Context, name, deletionID string) (port.Schedule, error) {
	claimed, err := s.ClaimNow(ctx, name, s.now, s.now.Add(time.Minute))
	if err != nil {
		return port.Schedule{}, err
	}
	if err := s.RecordFire(ctx, port.ScheduleFire{ID: "racing-fire", ScheduleName: name, SessionID: "sched--racing-fire", FiredAt: s.now, Stop: session.StopError, Err: "failed before run publication"}); err != nil {
		return port.Schedule{}, err
	}
	if claimed.State.FireCount != 1 {
		return port.Schedule{}, fmt.Errorf("claim did not advance fire count")
	}
	return s.Store.BeginDelete(ctx, name, deletionID)
}

func TestScheduleDeleteUsesAtomicBeginRecordForClaimHandoff(t *testing.T) {
	provider := &schedulePlacementProvider{}
	sessions := memstore.New()
	schedules := &claimBeforeBeginScheduleStore{Store: memschedulestore.New(), now: time.Unix(1_700_000_100, 0)}
	manager := server.NewScheduleManager(server.ScheduleManagerConfig{Store: sessions, ScheduleStore: schedules})
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Model: "test", Store: sessions})
	svc, err := server.NewService(server.Config{Engine: eng, Store: sessions, ScheduleManager: manager, PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: "/host", Now: func() time.Time { return time.Unix(1_700_000_000, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateSchedule(t.Context(), schedulePlacementSpec("claim-race")); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteSchedule(t.Context(), "claim-race"); err != nil {
		t.Fatal(err)
	}
	if provider.deletes != 0 {
		t.Fatalf("cleanup used stale pre-BeginDelete FireCount: deletes=%d", provider.deletes)
	}
}

type failingCompleteScheduleStore struct {
	*memschedulestore.Store
	fail bool
}

func (s *failingCompleteScheduleStore) CompleteDelete(ctx context.Context, name, deletionID string) error {
	if s.fail {
		s.fail = false
		return fmt.Errorf("completion unavailable")
	}
	return s.Store.CompleteDelete(ctx, name, deletionID)
}

func TestScheduleDeleteCompletionFailureLeavesRetryableTombstone(t *testing.T) {
	provider := &schedulePlacementProvider{}
	sessions := memstore.New()
	schedules := &failingCompleteScheduleStore{Store: memschedulestore.New(), fail: true}
	manager := server.NewScheduleManager(server.ScheduleManagerConfig{Store: sessions, ScheduleStore: schedules})
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Model: "test", Store: sessions})
	svc, err := server.NewService(server.Config{Engine: eng, Store: sessions, ScheduleManager: manager, PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: "/host", Now: func() time.Time { return time.Unix(1_700_000_000, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	created, err := svc.CreateSchedule(t.Context(), schedulePlacementSpec("finish-retry"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteSchedule(t.Context(), "finish-retry"); err == nil || !strings.Contains(err.Error(), "completion is pending") {
		t.Fatalf("first delete = %v", err)
	}
	pending, err := schedules.Load(t.Context(), "finish-retry")
	if err != nil || pending.State.DeletionID == "" || pending.Spec.EnvironmentRef != created.Spec.EnvironmentRef {
		t.Fatalf("pending tombstone = %+v, %v", pending, err)
	}
	if err := svc.DeleteSchedule(t.Context(), "finish-retry"); err != nil {
		t.Fatal(err)
	}
	if _, err := schedules.Load(t.Context(), "finish-retry"); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Fatalf("completed load = %v", err)
	}
	if provider.deletes != 2 {
		t.Fatalf("cleanup attempts = %d, want 2", provider.deletes)
	}
}

type failingScheduleCreator struct {
	port.ScheduleStore
	err error
}

func (s failingScheduleCreator) Create(context.Context, port.Schedule) error { return s.err }

func TestScheduleCreatePersistenceFailureRollsBackOwnedPlacement(t *testing.T) {
	provider := &schedulePlacementProvider{deleteErr: fmt.Errorf("rollback failed"), closeErr: fmt.Errorf("release failed")}
	sessions := memstore.New()
	failing := failingScheduleCreator{ScheduleStore: memschedulestore.New(), err: fmt.Errorf("save failed")}
	manager := server.NewScheduleManager(server.ScheduleManagerConfig{Store: sessions, ScheduleStore: failing})
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Model: "test", Store: sessions})
	svc, err := server.NewService(server.Config{Engine: eng, Store: sessions, ScheduleManager: manager, PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: "/host", Now: func() time.Time { return time.Unix(1_700_000_000, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	_, createErr := svc.CreateSchedule(t.Context(), schedulePlacementSpec("rollback"))
	if createErr == nil {
		t.Fatal("create unexpectedly succeeded")
	}
	if !strings.Contains(createErr.Error(), "save failed") || !strings.Contains(createErr.Error(), "rollback schedule placement") || !strings.Contains(createErr.Error(), "release schedule placement") || strings.Contains(createErr.Error(), "rollback failed") || strings.Contains(createErr.Error(), "release failed") {
		t.Fatalf("create error did not safely report persistence, release, and rollback failures: %v", createErr)
	}
	if provider.binds != 1 || provider.closes != 1 || provider.deletes != 1 {
		t.Fatalf("bind/close/delete = %d/%d/%d, want 1/1/1", provider.binds, provider.closes, provider.deletes)
	}
}
