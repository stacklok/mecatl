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
	svc, err := server.NewService(server.Config{
		Engine:     engine,
		Store:      store,
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:        func() time.Time { return now },
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
		Name:      "cron-1",
		Prompt:    "rotate keys",
		Trigger:   port.TriggerSpec{Cron: "* * * * *"},
		Mutating:  true,
		Workspace: "/tmp",
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

// TestCreateScheduleOneShotFuture: a one-shot schedule's NextFireAt is its
// OneShot instant; a past one-shot is rejected fail-closed.
func TestCreateScheduleOneShotFuture(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	svc, _ := newScheduleService(t, now)
	ctx := context.Background()

	future := now.Add(time.Hour)
	sched, err := svc.CreateSchedule(ctx, port.ScheduleSpec{
		Name:      "once-future",
		Prompt:    "x",
		Trigger:   port.TriggerSpec{OneShot: future},
		Mutating:  true,
		Workspace: "/tmp",
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
		Name:      "once-past",
		Prompt:    "x",
		Trigger:   port.TriggerSpec{OneShot: now.Add(-time.Hour)},
		Mutating:  true,
		Workspace: "/tmp",
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
		Name:      "bad",
		Prompt:    "x",
		Trigger:   port.TriggerSpec{Cron: "* * * * *"},
		Mutating:  false,
		Mode:      session.ModeDefault,
		Workspace: "/tmp",
	}); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("CreateSchedule Mutating=false Mode=default = %v, want ErrInvalidArgument", err)
	}
	// Mutating=false + ModePlan (read-only) → accepted.
	if _, err := svc.CreateSchedule(ctx, port.ScheduleSpec{
		Name:      "good",
		Prompt:    "x",
		Trigger:   port.TriggerSpec{Cron: "* * * * *"},
		Mutating:  false,
		Mode:      session.ModePlan,
		Workspace: "/tmp",
	}); err != nil {
		t.Fatalf("CreateSchedule Mutating=false Mode=plan: %v", err)
	}
}

// TestCreateScheduleWorkspaceProfileInvariant: the workspace/profile check is
// PROFILE-AWARE, mirroring the session create-seam. A default-profile schedule
// with no workspace is rejected (a fire would mint a filesystem session with
// nothing to root — an unfireable schedule); a no-fs-profile schedule WITH a
// workspace is also rejected (a no-FS fire has no filesystem to root).
func TestCreateScheduleWorkspaceProfileInvariant(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	svc, _ := newScheduleService(t, now)
	ctx := context.Background()

	// Default profile, empty workspace → rejected.
	if _, err := svc.CreateSchedule(ctx, port.ScheduleSpec{
		Name:     "no-workspace",
		Prompt:   "x",
		Trigger:  port.TriggerSpec{Cron: "* * * * *"},
		Mutating: true,
	}); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("CreateSchedule default-profile no workspace = %v, want ErrInvalidArgument", err)
	}

	// no-fs profile, non-empty workspace → rejected.
	if _, err := svc.CreateSchedule(ctx, port.ScheduleSpec{
		Name:      "no-fs-with-workspace",
		Prompt:    "x",
		Trigger:   port.TriggerSpec{Cron: "* * * * *"},
		Mutating:  true,
		Profile:   string(server.ProfileNoFS),
		Workspace: "/tmp",
	}); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("CreateSchedule no-fs profile with workspace = %v, want ErrInvalidArgument", err)
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
	svc, err := server.NewService(server.Config{
		Engine:     engine,
		Store:      memstore.New(),
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:        func() time.Time { return time.Unix(0, 0) },
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
		Name:      "pause-resume",
		Prompt:    "x",
		Trigger:   port.TriggerSpec{Cron: "* * * * *"},
		Mutating:  true,
		Workspace: "/tmp",
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
		Name:      "updatable",
		Prompt:    "v1",
		Trigger:   port.TriggerSpec{Cron: "0 9 * * *"},
		Mutating:  true,
		Workspace: "/tmp",
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
