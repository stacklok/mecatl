package scheduler_test

import (
	"context"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/stacklok/mecatl/engine/adapter/memschedulestore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/scheduler"
)

// TestIssue2588_PastFireDeadlineHeldLeaseReconciles is the regression repro for
// https://github.com/stacklok/mecatl/issues/2588 — a singleton schedule whose
// in-flight fire is past its explicit FireDeadline must be reaped (reconcile or
// terminal RecordFire) even when the prior-fire session lease is still held.
// Otherwise the singleton trial-lease sees ErrLeaseHeld forever and later ticks
// never fire.
//
// Regression guard: shouldReconcileStaleFire must reconcile when FireDeadline
// has lapsed even if isPriorFireLive still sees a held lease.
func TestIssue2588_PastFireDeadlineHeldLeaseReconciles(t *testing.T) {
	defer goleak.VerifyNone(t)
	restore := scheduler.SetStaleFireWindowForTest(1 * time.Hour)
	defer restore()

	const (
		schedName = "singleton-hung-deadline"
		fireTO    = 50 * time.Millisecond
	)
	sessID := session.SessionID("sched--hung-past-deadline")

	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	leaseBE := &heldSessionLease{
		held: map[session.SessionID]string{sessID: "owner-hung"},
		clk:  clk,
	}
	store := memschedulestore.New()
	fire := &fireStub{}

	now := clk.Now()
	startAt := now.Add(-2 * time.Minute)
	deadline := startAt.Add(fireTO) // lapsed long before now

	if err := store.Save(context.Background(), port.Schedule{
		Spec: port.ScheduleSpec{
			Name:        schedName,
			Prompt:      "x",
			Trigger:     port.TriggerSpec{Cron: "* * * * *"},
			Singleton:   true,
			FireTimeout: fireTO,
		},
		State: port.ScheduleState{
			NextFireAt:         now, // due again — must not singleton-skip forever
			Enabled:            true,
			LastFireAt:         startAt,
			LastFireSessionID:  sessID,
			LastFireStartedAt:  startAt,
			LastFireProgressAt: startAt,
			FireDeadline:       deadline,
		},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.RecordFireStart(context.Background(), schedName, port.ScheduleFire{
		ID:           string(sessID),
		ScheduleName: schedName,
		SessionID:    sessID,
		FiredAt:      startAt,
		StartedAt:    startAt,
		Deadline:     deadline,
	}); err != nil {
		t.Fatalf("RecordFireStart: %v", err)
	}

	var reconciled int
	s := scheduler.New(scheduler.Config{
		Store:              store,
		Lease:              leaseBE,
		LeaseOwner:         "owner-tick",
		Fire:               fire.fire,
		Clock:              clk,
		TickInterval:       time.Hour,
		MaxConcurrentFires: 4,
		ReconcileStaleFire: func(_ context.Context, _ port.Schedule) {
			reconciled++
		},
	})
	s.RunOnceForTest(context.Background())

	if reconciled != 1 {
		t.Fatalf("reconciled = %d, want 1 (past FireDeadline must reconcile despite held lease; issue #2588)", reconciled)
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
