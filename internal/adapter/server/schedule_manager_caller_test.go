package server_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memschedulestore"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestScheduleVerbsRejectMissingCallerUnderEnforcement pins review finding 4
// (issue #368): with ownership enforcement ON, every public ScheduleManager
// verb (the nine port.ScheduleManager methods plus GetFire) called against a
// ctx carrying NO verified principal must reject — never silently fall
// through to the shared scheduleOwnerlessNamespace bucket any other
// unauthenticated caller could also reach. Read/mutate verbs on an existing
// name see the SAME absence-shaped error a genuinely-missing schedule would
// produce; Create sees a generic refusal (never a distinguishing message);
// Delete stays idempotent (nil, nothing to delete from this caller's view);
// List returns an empty enumeration.
func TestScheduleVerbsRejectMissingCallerUnderEnforcement(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := memschedulestore.New()
	sessions := memstore.New()
	mgr := server.NewScheduleManager(server.ScheduleManagerConfig{
		Store: sessions, ScheduleStore: store, Now: func() time.Time { return now }, OwnershipEnforced: true,
	})
	if mgr == nil {
		t.Fatal("NewScheduleManager = nil, want a manager")
	}
	ctx := context.Background() // deliberately no principal

	if _, err := mgr.CreateSchedule(ctx, testSchedule("caller-missing")); !errors.Is(err, server.ErrInvalidArgument) {
		t.Errorf("CreateSchedule(no caller) = %v, want ErrInvalidArgument (generic refusal)", err)
	}
	if _, err := mgr.GetSchedule(ctx, "caller-missing"); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Errorf("GetSchedule(no caller) = %v, want ErrScheduleNotFound", err)
	}
	if got, err := mgr.ListSchedules(ctx); err != nil || len(got) != 0 {
		t.Errorf("ListSchedules(no caller) = %+v, %v, want an empty slice and no error", got, err)
	}
	if _, err := mgr.UpdateSchedule(ctx, testSchedule("caller-missing")); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Errorf("UpdateSchedule(no caller) = %v, want ErrScheduleNotFound", err)
	}
	if err := mgr.DeleteSchedule(ctx, "caller-missing"); err != nil {
		t.Errorf("DeleteSchedule(no caller) = %v, want nil (idempotent, nothing visible to delete)", err)
	}
	if err := mgr.PauseSchedule(ctx, "caller-missing"); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Errorf("PauseSchedule(no caller) = %v, want ErrScheduleNotFound", err)
	}
	if err := mgr.ResumeSchedule(ctx, "caller-missing"); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Errorf("ResumeSchedule(no caller) = %v, want ErrScheduleNotFound", err)
	}
	if _, err := mgr.FireNow(ctx, "caller-missing"); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Errorf("FireNow(no caller) = %v, want ErrScheduleNotFound", err)
	}
	if _, err := mgr.GetFire(ctx, "some-fire-id"); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Errorf("GetFire(no caller) = %v, want ErrScheduleNotFound", err)
	}
	if _, err := mgr.ListFires(ctx, "caller-missing"); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Errorf("ListFires(no caller) = %v, want ErrScheduleNotFound", err)
	}

	// None of the above may have created or mutated anything visible: the
	// store itself stays empty (no shared-bucket schedule was created).
	all, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("store contains %d schedules after all-rejected calls, want 0 (no shared-bucket write)", len(all))
	}
}

// TestScheduleVerbsUnaffectedWhenEnforcementDisabled guards the ownerless
// deployment: with ownership enforcement OFF, a ctx carrying no principal is
// the NORMAL case (there is no verifier wired), and every verb must keep
// working exactly as before — byte-identical to the pre-fix behavior.
func TestScheduleVerbsUnaffectedWhenEnforcementDisabled(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := memschedulestore.New()
	sessions := memstore.New()
	mgr := server.NewScheduleManager(server.ScheduleManagerConfig{
		Store: sessions, ScheduleStore: store, Now: func() time.Time { return now },
	})
	if mgr == nil {
		t.Fatal("NewScheduleManager = nil, want a manager")
	}
	ctx := context.Background()

	spec := testSchedule("no-enforcement")
	spec.EnvironmentRef = session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
	spec.PlacementScope = "legacy-local"
	if _, err := mgr.CreateSchedule(ctx, spec); err != nil {
		t.Fatalf("CreateSchedule(unenforced, no caller): %v", err)
	}
	if _, err := mgr.GetSchedule(ctx, "no-enforcement"); err != nil {
		t.Errorf("GetSchedule(unenforced, no caller): %v", err)
	}
	if got, err := mgr.ListSchedules(ctx); err != nil || len(got) != 1 {
		t.Errorf("ListSchedules(unenforced, no caller) = %+v, %v, want exactly the one created schedule", got, err)
	}
	if err := mgr.PauseSchedule(ctx, "no-enforcement"); err != nil {
		t.Errorf("PauseSchedule(unenforced, no caller): %v", err)
	}
	if err := mgr.ResumeSchedule(ctx, "no-enforcement"); err != nil {
		t.Errorf("ResumeSchedule(unenforced, no caller): %v", err)
	}
	if err := mgr.DeleteSchedule(ctx, "no-enforcement"); err != nil {
		t.Errorf("DeleteSchedule(unenforced, no caller): %v", err)
	}
}
