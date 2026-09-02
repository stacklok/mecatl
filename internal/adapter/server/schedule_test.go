package server_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// newScheduleService builds a Service over a jsonlstore (which exposes a
// ScheduleStore) for the schedule create-seam tests. It uses a fixed clock so
// the one-shot-future and cron-next-fire assertions are deterministic.
func newScheduleService(t *testing.T, now time.Time) (*server.Service, port.ScheduleStore) {
	t.Helper()
	dir := t.TempDir()
	store, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatalf("jsonlstore: %v", err)
	}
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
		Store:   store,
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:           engine,
		Store:            store,
		SharedEngineRoot: "/tmp",

		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc, store.ScheduleStore()
}

// TestCreateScheduleCron: a valid cron schedule is created with the first
// NextFireAt computed via cronparse, Singleton defaulted true, and Enabled true.
func TestCreateScheduleCron(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	svc, schedStore := newScheduleService(t, now)
	ctx := context.Background()

	sched, err := svc.CreateSchedule(ctx, port.ScheduleSpec{
		Name:     "cron-1",
		Prompt:   "rotate keys",
		Trigger:  port.TriggerSpec{Cron: "* * * * *"},
		Mutating: true,
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if !sched.State.Enabled {
		t.Errorf("Enabled = false, want true (new schedule active)")
	}
	if !sched.Spec.Singleton {
		t.Errorf("Singleton = false, want true (intended default)")
	}
	// NextFireAt is strictly after now (the next minute boundary).
	if !sched.State.NextFireAt.After(now) {
		t.Errorf("NextFireAt = %v, want after now %v", sched.State.NextFireAt, now)
	}
	// The schedule is persisted (the create-seam Saves).
	loaded, err := schedStore.Load(ctx, "cron-1")
	if err != nil {
		t.Fatalf("Load after create: %v", err)
	}
	if loaded.Spec.Prompt != "rotate keys" {
		t.Errorf("persisted Prompt = %q, want %q", loaded.Spec.Prompt, "rotate keys")
	}
}

// TestCreateScheduleRejectsDuplicateName: ScheduleStore.Save is an
// UPSERT-by-name, so a second Create with an existing name would SILENTLY
// CLOBBER the original spec. The create-seam rejects the duplicate
// (ErrInvalidArgument, naming the schedule) and leaves the ORIGINAL spec intact
// — the edit path is UpdateSchedule, a distinct method.
func TestCreateScheduleRejectsDuplicateName(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	svc, schedStore := newScheduleService(t, now)
	ctx := context.Background()

	if _, err := svc.CreateSchedule(ctx, port.ScheduleSpec{
		Name:     "dup",
		Prompt:   "original prompt",
		Trigger:  port.TriggerSpec{Cron: "* * * * *"},
		Mutating: true,
	}); err != nil {
		t.Fatalf("first CreateSchedule: %v", err)
	}

	// A second create with the SAME name but a DIFFERENT spec must be rejected.
	_, err := svc.CreateSchedule(ctx, port.ScheduleSpec{
		Name:     "dup",
		Prompt:   "CLOBBERING prompt",
		Trigger:  port.TriggerSpec{Cron: "0 0 * * *"},
		Mutating: true,
	})
	if err == nil {
		t.Fatal("second CreateSchedule with a duplicate name succeeded, want ErrInvalidArgument")
	}
	if !errors.Is(err, server.ErrInvalidArgument) {
		t.Errorf("duplicate-name err = %v, want ErrInvalidArgument", err)
	}
	if !strings.Contains(err.Error(), "dup") {
		t.Errorf("duplicate-name err = %v, want it to name the schedule %q", err, "dup")
	}
	// The ORIGINAL spec is unchanged (not clobbered by the rejected create).
	loaded, lerr := schedStore.Load(ctx, "dup")
	if lerr != nil {
		t.Fatalf("Load after rejected create: %v", lerr)
	}
	if loaded.Spec.Prompt != "original prompt" {
		t.Errorf("persisted Prompt = %q, want %q (original NOT clobbered)", loaded.Spec.Prompt, "original prompt")
	}
	wantRef := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/tmp", Revision: "in-tree-v1"}
	if loaded.Spec.EnvironmentRef != wantRef || loaded.Spec.PlacementScope != "test" {
		t.Errorf("persisted placement = (%+v, %q), want exact original (%+v, %q)", loaded.Spec.EnvironmentRef, loaded.Spec.PlacementScope, wantRef, "test")
	}
}

// TestCreateScheduleOneShotFuture: a one-shot schedule's NextFireAt is its
// OneShot instant; a past one-shot is rejected fail-closed.
func TestCreateScheduleOneShotFuture(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	svc, _ := newScheduleService(t, now)
	ctx := context.Background()

	future := now.Add(time.Hour)
	sched, err := svc.CreateSchedule(ctx, port.ScheduleSpec{
		Name:     "once-future",
		Prompt:   "x",
		Trigger:  port.TriggerSpec{OneShot: future},
		Mutating: true,
	})
	if err != nil {
		t.Fatalf("CreateSchedule future: %v", err)
	}
	if !sched.State.NextFireAt.Equal(future) {
		t.Errorf("NextFireAt = %v, want %v (the OneShot instant)", sched.State.NextFireAt, future)
	}

	// Past one-shot rejected (Mutating=true so the mode check passes; the
	// future-invariant is what rejects).
	if _, err := svc.CreateSchedule(ctx, port.ScheduleSpec{
		Name:     "once-past",
		Prompt:   "x",
		Trigger:  port.TriggerSpec{OneShot: now.Add(-time.Hour)},
		Mutating: true,
	}); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("CreateSchedule past one-shot = %v, want ErrInvalidArgument", err)
	}
}

// TestCreateScheduleRejectsReadleaningWithWriteMode: a Mutating=false schedule
// with a write-capable Mode (ModeDefault) is rejected; ModePlan is accepted.
func TestCreateScheduleRejectsReadleaningWithWriteMode(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	svc, _ := newScheduleService(t, now)
	ctx := context.Background()

	// Mutating=false + ModeDefault (write-capable) → rejected. A workspace is
	// set so the spec clears the profile-invariant check first and the
	// rejection asserted below is genuinely the mode check, not a masked
	// workspace error.
	if _, err := svc.CreateSchedule(ctx, port.ScheduleSpec{
		Name:     "bad",
		Prompt:   "x",
		Trigger:  port.TriggerSpec{Cron: "* * * * *"},
		Mutating: false,
		Mode:     session.ModeDefault,
	}); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("CreateSchedule Mutating=false Mode=default = %v, want ErrInvalidArgument", err)
	}
	// Mutating=false + ModePlan (read-only) → accepted.
	if _, err := svc.CreateSchedule(ctx, port.ScheduleSpec{
		Name:     "good",
		Prompt:   "x",
		Trigger:  port.TriggerSpec{Cron: "* * * * *"},
		Mutating: false,
		Mode:     session.ModePlan,
	}); err != nil {
		t.Fatalf("CreateSchedule Mutating=false Mode=plan: %v", err)
	}
}

// TestADR_0280_ScheduleResolvesSelectorBeforePersistingExactEnvironmentRef
// proves a public create carries no private path/ref while the server resolves
// its default selector and persists the exact durable identity and scope.
func TestADR_0280_ScheduleResolvesSelectorBeforePersistingExactEnvironmentRef(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	svc, schedStore := newScheduleService(t, now)
	ctx := context.Background()

	created, err := svc.CreateSchedule(ctx, port.ScheduleSpec{
		Name: "resolved-placement", Prompt: "x", Trigger: port.TriggerSpec{Cron: "* * * * *"}, Mutating: true,
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	stored, err := schedStore.Load(ctx, created.Spec.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	wantRef := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/tmp", Revision: "in-tree-v1"}
	if stored.Spec.EnvironmentRef != wantRef || stored.Spec.PlacementScope != "test" {
		t.Fatalf("stored placement = (%+v, %q), want (%+v, %q)", stored.Spec.EnvironmentRef, stored.Spec.PlacementScope, wantRef, "test")
	}
	if created.Spec.EnvironmentRef != wantRef || created.Spec.PlacementScope != "test" {
		t.Fatalf("returned placement = (%+v, %q), want exact persisted placement", created.Spec.EnvironmentRef, created.Spec.PlacementScope)
	}
}

// TestCreateScheduleRejectsBadCron: an invalid cron expression is rejected
// fail-closed (never saved as a never-fires schedule).
func TestCreateScheduleRejectsBadCron(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	svc, _ := newScheduleService(t, now)
	ctx := context.Background()

	if _, err := svc.CreateSchedule(ctx, port.ScheduleSpec{
		Name:     "bad-cron",
		Prompt:   "x",
		Trigger:  port.TriggerSpec{Cron: "not a cron"},
		Mutating: true,
	}); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("CreateSchedule bad cron = %v, want ErrInvalidArgument", err)
	}
}

// TestScheduleAccessorsNoStore: with a store that does NOT expose a
// ScheduleStore (memstore), the schedule methods return ErrNoScheduleStore.
func TestScheduleAccessorsNoStore(t *testing.T) {
	// Build a Service over memstore (no ScheduleStore accessor).
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
		Store:   memstore.New(),
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: engine,
		Store:  memstore.New(),

		Now: func() time.Time { return time.Unix(0, 0) },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	ctx := context.Background()
	if _, err := svc.CreateSchedule(ctx, port.ScheduleSpec{Name: "x", Prompt: "p", Trigger: port.TriggerSpec{Cron: "* * * * *"}}); !errors.Is(err, server.ErrNoScheduleStore) {
		t.Fatalf("CreateSchedule without ScheduleStore = %v, want ErrNoScheduleStore", err)
	}
	if _, err := svc.GetSchedule(ctx, "x"); !errors.Is(err, server.ErrNoScheduleStore) {
		t.Fatalf("GetSchedule without ScheduleStore = %v, want ErrNoScheduleStore", err)
	}
	if _, err := svc.FireNow(ctx, "x"); !errors.Is(err, server.ErrNoScheduleStore) {
		t.Fatalf("FireNow without scheduler = %v, want ErrNoScheduleStore", err)
	}
}

// TestPauseResumeSchedule: PauseSchedule/ResumeSchedule flip Enabled via the
// dedicated SetEnabled store method (Save cannot mutate Enabled because it
// preserves the State half on overwrite). The flip persists: Load after Pause
// → Enabled false; Load after Resume → Enabled true.
func TestPauseResumeSchedule(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	svc, schedStore := newScheduleService(t, now)
	ctx := context.Background()

	spec := port.ScheduleSpec{
		Name:     "pause-resume",
		Prompt:   "x",
		Trigger:  port.TriggerSpec{Cron: "* * * * *"},
		Mutating: true,
	}
	if _, err := svc.CreateSchedule(ctx, spec); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}

	if err := svc.PauseSchedule(ctx, "pause-resume"); err != nil {
		t.Fatalf("PauseSchedule: %v", err)
	}
	got, err := schedStore.Load(ctx, "pause-resume")
	if err != nil {
		t.Fatalf("Load after Pause: %v", err)
	}
	if got.State.Enabled {
		t.Errorf("after Pause: Enabled = true, want false (the flip must persist)")
	}

	if err := svc.ResumeSchedule(ctx, "pause-resume"); err != nil {
		t.Fatalf("ResumeSchedule: %v", err)
	}
	got, err = schedStore.Load(ctx, "pause-resume")
	if err != nil {
		t.Fatalf("Load after Resume: %v", err)
	}
	if !got.State.Enabled {
		t.Errorf("after Resume: Enabled = false, want true (the flip must persist)")
	}

	// Pause on an unknown name wraps ErrScheduleNotFound (the store's
	// SetEnabled not-found case), surfaced by the Service unchanged.
	if err := svc.PauseSchedule(ctx, "no-such-schedule"); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Fatalf("PauseSchedule(unknown) = %v, want ErrScheduleNotFound", err)
	}
}

// TestUpdateSchedulePreservesCreatedAt: UpdateSchedule overwrites the Spec half
// while preserving the State half (firing progress) AND the creation timestamp.
// CreatedAt is a store-side timestamp, never operator-authored, so an Update must
// not clobber it to the zero value — the reconcile update path (reconcileSchedules)
// would otherwise destroy the audit trail on every restart.
func TestUpdateSchedulePreservesCreatedAt(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	svc, schedStore := newScheduleService(t, now)
	ctx := context.Background()

	spec := port.ScheduleSpec{
		Name:     "updatable",
		Prompt:   "v1",
		Trigger:  port.TriggerSpec{Cron: "0 9 * * *"},
		Mutating: true,
	}
	created, err := svc.CreateSchedule(ctx, spec)
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if created.Spec.CreatedAt.IsZero() {
		t.Fatalf("CreatedAt is zero after create")
	}

	// Update with a changed prompt — everything else identical.
	spec.Prompt = "v2"
	updated, err := svc.UpdateSchedule(ctx, spec)
	if err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}
	if updated.Spec.Prompt != "v2" {
		t.Fatalf("Update did not apply new Prompt; got %q", updated.Spec.Prompt)
	}
	if !updated.Spec.CreatedAt.Equal(created.Spec.CreatedAt) {
		t.Fatalf("CreatedAt not preserved: create=%v update=%v",
			created.Spec.CreatedAt, updated.Spec.CreatedAt)
	}

	// Persisted form must also keep the original CreatedAt (the bug was in the
	// Save path, not just the returned value).
	loaded, err := schedStore.Load(ctx, "updatable")
	if err != nil {
		t.Fatalf("Load after Update: %v", err)
	}
	if !loaded.Spec.CreatedAt.Equal(created.Spec.CreatedAt) {
		t.Fatalf("persisted CreatedAt not preserved: create=%v loaded=%v",
			created.Spec.CreatedAt, loaded.Spec.CreatedAt)
	}
}

// TestFireDelivery_Scenario1_OutOfBandCreateHasEmptyOrigin pins AC1.2: a
// schedule created out-of-band (REST/gRPC, no conversation) persists an empty
// OriginSessionID and behaves exactly as before — the field is metadata-only,
// never rendered into a prompt.
func TestFireDelivery_Scenario1_OutOfBandCreateHasEmptyOrigin(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	svc, schedStore := newScheduleService(t, now)
	ctx := context.Background()

	// Create without setting OriginSessionID — the field is left at its zero value.
	sched, err := svc.CreateSchedule(ctx, port.ScheduleSpec{
		Name:     "empty-origin",
		Prompt:   "p",
		Trigger:  port.TriggerSpec{Cron: "* * * * *"},
		Mutating: true,
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if sched.Spec.OriginSessionID != "" {
		t.Errorf("OriginSessionID = %q, want empty (out-of-band create carries no origin)", sched.Spec.OriginSessionID)
	}

	// The schedule persists and Loads back with the empty OriginSessionID intact.
	loaded, err := schedStore.Load(ctx, "empty-origin")
	if err != nil {
		t.Fatalf("Load after create: %v", err)
	}
	if loaded.Spec.OriginSessionID != "" {
		t.Errorf("persisted OriginSessionID = %q, want empty", loaded.Spec.OriginSessionID)
	}
}

// TestFireDelivery_Scenario1_UnknownOriginRejected pins AC1.3: a create whose
// OriginSessionID names a non-existent session is rejected fail-closed by the
// create-seam (the same ErrInvalidArgument class as the other spec rejections).
func TestFireDelivery_Scenario1_UnknownOriginRejected(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	svc, _ := newScheduleService(t, now)
	ctx := context.Background()

	// A non-empty OriginSessionID that names a session not in the store is
	// rejected with ErrInvalidArgument.
	_, err := svc.CreateSchedule(ctx, port.ScheduleSpec{
		Name:            "unknown-origin",
		Prompt:          "p",
		Trigger:         port.TriggerSpec{Cron: "* * * * *"},
		Mutating:        true,
		OriginSessionID: "no-such-session",
	})
	if !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("CreateSchedule with unknown OriginSessionID = %v, want ErrInvalidArgument", err)
	}

	// The rejected schedule is NOT saved (fail-closed means no residual state).
	if _, lerr := svc.GetSchedule(ctx, "unknown-origin"); !errors.Is(lerr, port.ErrScheduleNotFound) {
		t.Fatalf("the rejected schedule was SAVED — fail-closed means not saved; err=%v", lerr)
	}
}
