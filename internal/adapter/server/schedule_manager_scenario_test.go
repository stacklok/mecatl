package server_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/scheduler"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// TestScheduleSharedCatalog_Scenario1_ManagerIsStoreShaped pins AC1.1
// (ADR 0076): the schedule manager is STORE-SHAPED — constructable from a
// port.SessionStore + a now-func ALONE (no *server.Service value required,
// resolvable before buildEngine). A store that backs no ScheduleStore (the
// in-memory memstore) yields a nil/absent manager — the honest no-scheduling
// path — matching the ServerCapabilities.Scheduling gate.
func TestScheduleSharedCatalog_Scenario1_ManagerIsStoreShaped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	nowFunc := func() time.Time { return now }

	// A store-backed manager is constructable with no *server.Service anywhere
	// (the pre-Service shape ADR 0076 names) — over a real jsonlstore so the
	// ScheduleStore type-assertion is exercised against the real adapter, not
	// a fixture that could drift.
	store, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	mgr := server.NewScheduleManager(server.ScheduleManagerConfig{
		Store: store,
		Now:   nowFunc,
	})
	if mgr == nil {
		t.Fatal("NewScheduleManager over a ScheduleStore-backed store = nil, want non-nil")
	}

	// The manager satisfies the consumer-local port.ScheduleManager (the
	// Schedule tool's seam) — a compile-time assertion would be unexported-type
	// proof; this exercises the assignment.
	var pm port.ScheduleManager = mgr

	// The manager works standalone: a valid cron create saves ENABLED with the
	// cronparse-computed first fire, with no Service in sight.
	spec := port.ScheduleSpec{
		Name:      "standalone",
		Prompt:    "rotate keys",
		Trigger:   port.TriggerSpec{Cron: "@every 1h"},
		Workspace: "/ws",
		Mode:      session.ModePlan,
	}
	sched, err := pm.CreateSchedule(ctx, spec)
	if err != nil {
		t.Fatalf("standalone CreateSchedule: %v", err)
	}
	if !sched.State.Enabled {
		t.Error("standalone create saved Enabled=false, want true")
	}
	if sched.State.NextFireAt.IsZero() {
		t.Error("standalone create saved a zero NextFireAt, want the cronparse-computed first fire")
	}
	loaded, err := pm.GetSchedule(ctx, "standalone")
	if err != nil {
		t.Fatalf("standalone GetSchedule: %v", err)
	}
	if !loaded.State.NextFireAt.Equal(sched.State.NextFireAt) {
		t.Errorf("loaded NextFireAt = %v, want the saved first fire %v", loaded.State.NextFireAt, sched.State.NextFireAt)
	}

	// A store with NO ScheduleStore (memstore) yields the honest absent manager
	// — nil, matching the capabilities Scheduling=false gate.
	if got := server.NewScheduleManager(server.ScheduleManagerConfig{
		Store: memstore.New(),
		Now:   nowFunc,
	}); got != nil {
		t.Errorf("NewScheduleManager over a non-ScheduleStore store = %v, want nil (the honest no-scheduling path)", got)
	}
}

// TestScheduleSharedCatalog_Scenario1_ServiceDelegates pins AC1.3: the
// Service's RPC surface delegates to the pre-Service manager WITHOUT behaviour
// change. Service.ScheduleManager() returns non-nil exactly when the store
// backs a ScheduleStore (nil otherwise — the unchanged gate), and the whole
// port.ScheduleManager verb set behaves identically through the delegating
// wrapper (a wrapper that shadows a second seam would drift from the manager
// the test drives directly over the SAME store).
func TestScheduleSharedCatalog_Scenario1_ServiceDelegates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)

	store, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	// The PRE-SERVICE manager composition builds before buildEngine (ADR 0076):
	// constructed from the store alone and HANDED to the Service.
	mgr := server.NewScheduleManager(server.ScheduleManagerConfig{
		Store: store,
		Now:   func() time.Time { return now },
	})
	if mgr == nil {
		t.Fatal("NewScheduleManager over jsonlstore = nil, want non-nil")
	}

	svc := newDelegatingScheduleService(t, store, now, server.Config{ScheduleManager: mgr})

	// The gate: ScheduleManager() returns the embedded manager (the single
	// truth), NOT the Service itself.
	if got := svc.ScheduleManager(); got == nil {
		t.Fatal("Service.ScheduleManager() = nil over a ScheduleStore-backed store, want the manager")
	} else if _, isService := got.(*server.Service); isService {
		t.Fatal("Service.ScheduleManager() returned the Service itself, want the embedded manager (the single truth)")
	}

	// The delegation is byte-identical: drive every verb through the Service
	// wrapper and observe it on the manager (the SAME seam) over the same store.
	spec := port.ScheduleSpec{
		Name:      "delegate",
		Prompt:    "p",
		Trigger:   port.TriggerSpec{Cron: "@every 1h"},
		Workspace: "/ws",
		Mode:      session.ModePlan,
	}
	if _, err := svc.CreateSchedule(ctx, spec); err != nil {
		t.Fatalf("svc.CreateSchedule: %v", err)
	}
	if _, err := mgr.GetSchedule(ctx, "delegate"); err != nil {
		t.Fatalf("manager does not see the svc-created schedule: %v", err)
	}
	got, err := svc.GetSchedule(ctx, "delegate")
	if err != nil {
		t.Fatalf("svc.GetSchedule: %v", err)
	}
	if got.Spec.Name != "delegate" {
		t.Errorf("svc.GetSchedule name = %q, want delegate", got.Spec.Name)
	}
	list, err := svc.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("svc.ListSchedules: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("svc.ListSchedules = %d schedules, want 1", len(list))
	}

	// Update re-validates and preserves firing state; pause/resume flip Enabled.
	spec.Prompt = "p2"
	if _, err := svc.UpdateSchedule(ctx, spec); err != nil {
		t.Fatalf("svc.UpdateSchedule: %v", err)
	}
	updated, err := svc.GetSchedule(ctx, "delegate")
	if err != nil {
		t.Fatalf("svc.GetSchedule after update: %v", err)
	}
	if updated.Spec.Prompt != "p2" {
		t.Errorf("updated prompt = %q, want p2", updated.Spec.Prompt)
	}
	if err := svc.PauseSchedule(ctx, "delegate"); err != nil {
		t.Fatalf("svc.PauseSchedule: %v", err)
	}
	paused, err := svc.GetSchedule(ctx, "delegate")
	if err != nil {
		t.Fatalf("svc.GetSchedule after pause: %v", err)
	}
	if paused.State.Enabled {
		t.Error("svc.PauseSchedule left Enabled=true, want false")
	}
	if err := svc.ResumeSchedule(ctx, "delegate"); err != nil {
		t.Fatalf("svc.ResumeSchedule: %v", err)
	}
	resumed, err := svc.GetSchedule(ctx, "delegate")
	if err != nil {
		t.Fatalf("svc.GetSchedule after resume: %v", err)
	}
	if !resumed.State.Enabled {
		t.Error("svc.ResumeSchedule left Enabled=false, want true")
	}
	fires, err := svc.ListFires(ctx, "delegate")
	if err != nil {
		t.Fatalf("svc.ListFires: %v", err)
	}
	if len(fires) != 0 {
		t.Errorf("svc.ListFires = %d fires, want 0 (nothing fired)", len(fires))
	}

	// EmitScheduleEvent delegates: nil EventLog ⇒ a no-op that does not panic.
	svc.EmitScheduleEvent(ctx, session.SchedulePayload{Kind: "fired", SessionID: "s1", ScheduleName: "delegate"})

	if err := svc.DeleteSchedule(ctx, "delegate"); err != nil {
		t.Fatalf("svc.DeleteSchedule: %v", err)
	}
	if _, err := svc.GetSchedule(ctx, "delegate"); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Errorf("svc.GetSchedule after delete: err=%v, want ErrScheduleNotFound", err)
	}

	// A store WITHOUT a ScheduleStore: ScheduleManager() is nil (the unchanged
	// gate) and the delegating verbs keep the ErrNoScheduleStore contract.
	noStoreSvc := newDelegatingScheduleService(t, memstore.New(), now, server.Config{})
	if got := noStoreSvc.ScheduleManager(); got != nil {
		t.Errorf("Service.ScheduleManager() over a non-ScheduleStore store = %v, want nil", got)
	}
	if _, err := noStoreSvc.CreateSchedule(ctx, spec); !errors.Is(err, server.ErrNoScheduleStore) {
		t.Errorf("no-store CreateSchedule: err=%v, want ErrNoScheduleStore", err)
	}
}

// TestScheduleSharedCatalog_Scenario1_FireNowStatesPreserved pins AC1.4:
// FireNow still distinguishes its three states after the move, with the
// in-process scheduler late-set onto the MANAGER (not the Service) —
// (a) ErrNoScheduleStore when no store backs a ScheduleStore,
// (b) ErrSchedulerNotRunning when the store is present but no scheduler is
// wired, and (c) the mapped scheduler sentinels once one is late-set. The
// Service's delegating FireNow reports the identical sentinels.
func TestScheduleSharedCatalog_Scenario1_FireNowStatesPreserved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)

	// (a) No ScheduleStore at all → ErrNoScheduleStore.
	noStoreMgr := server.NewScheduleManager(server.ScheduleManagerConfig{
		Store: memstore.New(),
		Now:   func() time.Time { return now },
	})
	if noStoreMgr != nil {
		t.Fatal("NewScheduleManager over memstore = non-nil, want nil")
	}
	noStoreSvc := newDelegatingScheduleService(t, memstore.New(), now, server.Config{})
	if _, err := noStoreSvc.FireNow(ctx, "x"); !errors.Is(err, server.ErrNoScheduleStore) {
		t.Errorf("svc.FireNow with no ScheduleStore: err=%v, want ErrNoScheduleStore", err)
	}

	// (b) Store present, no scheduler wired → ErrSchedulerNotRunning, on BOTH
	// the manager (the seam's new home) and the delegating Service.
	store, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	mgr := server.NewScheduleManager(server.ScheduleManagerConfig{
		Store: store,
		Now:   func() time.Time { return now },
	})
	if mgr == nil {
		t.Fatal("NewScheduleManager over jsonlstore = nil, want non-nil")
	}
	if _, err := mgr.FireNow(ctx, "x"); !errors.Is(err, server.ErrSchedulerNotRunning) {
		t.Errorf("mgr.FireNow with no scheduler wired: err=%v, want ErrSchedulerNotRunning", err)
	}
	svc := newDelegatingScheduleService(t, store, now, server.Config{ScheduleManager: mgr})
	if _, err := svc.FireNow(ctx, "x"); !errors.Is(err, server.ErrSchedulerNotRunning) {
		t.Errorf("svc.FireNow with no scheduler wired: err=%v, want ErrSchedulerNotRunning", err)
	}
	if svc.HasScheduler() {
		t.Error("svc.HasScheduler() = true before SetScheduler, want false")
	}

	// (c) The scheduler is LATE-SET onto the manager (SetScheduler), after
	// which the mapped scheduler sentinels surface through BOTH the manager and
	// the delegating Service: an unknown schedule → the store's not-found
	// sentinel; a paused schedule → ErrScheduleDisabled (the mapped
	// scheduler.ErrFireNowDisabled).
	sched := scheduler.New(scheduler.Config{
		Store:        store.ScheduleStore(),
		Clock:        wallclock.Clock{},
		Diagnostics:  port.NopDiagnostics{},
		TickInterval: time.Hour, // the tick never fires during the test
	})
	svc.SetScheduler(sched) // the delegating setter — late-sets onto the manager
	defer func() { _ = sched.Stop() }()
	if !svc.HasScheduler() {
		t.Error("svc.HasScheduler() = false after SetScheduler, want true")
	}
	if _, err := mgr.FireNow(ctx, "never-created"); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Errorf("mgr.FireNow on an unknown schedule: err=%v, want ErrScheduleNotFound (the mapped scheduler sentinel)", err)
	}
	if _, err := svc.FireNow(ctx, "never-created"); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Errorf("svc.FireNow on an unknown schedule: err=%v, want ErrScheduleNotFound", err)
	}
	if _, err := mgr.CreateSchedule(ctx, port.ScheduleSpec{
		Name:      "paused",
		Prompt:    "p",
		Trigger:   port.TriggerSpec{Cron: "@every 1h"},
		Workspace: "/ws",
		Mode:      session.ModePlan,
	}); err != nil {
		t.Fatalf("mgr.CreateSchedule: %v", err)
	}
	if err := mgr.PauseSchedule(ctx, "paused"); err != nil {
		t.Fatalf("mgr.PauseSchedule: %v", err)
	}
	if _, err := mgr.FireNow(ctx, "paused"); !errors.Is(err, server.ErrScheduleDisabled) {
		t.Errorf("mgr.FireNow on a paused schedule: err=%v, want ErrScheduleDisabled", err)
	}
	if _, err := svc.FireNow(ctx, "paused"); !errors.Is(err, server.ErrScheduleDisabled) {
		t.Errorf("svc.FireNow on a paused schedule: err=%v, want ErrScheduleDisabled (delegation is byte-identical)", err)
	}
}

// newDelegatingScheduleService builds a Service over the given store with the
// given extra Config fields applied (mirroring composition: the manager is
// HANDED to the Service, not self-discovered).
func newDelegatingScheduleService(t *testing.T, store port.SessionStore, now time.Time, extra server.Config) *server.Service {
	t.Helper()
	llm := mockllm.New(mockllm.TextTurn("x"))
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
		Store:   store,
	})
	cfg := server.Config{
		Engine:              engine,
		Store:               store,
		Workspaces:          func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:                 func() time.Time { return now },
		DefaultCapabilities: llm.Capabilities(),
		Diagnostics:         port.NopDiagnostics{},
		ScheduleManager:     extra.ScheduleManager,
	}
	svc, err := server.NewService(cfg)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}
