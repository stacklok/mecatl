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

// TestReconcileStaleFireAfterClaim detects crash sub-case 1 (issue #386 Phase
// 4b): a schedule whose Claim stamped the pending sentinel but whose
// RecordFireStart never ran (LastFireSessionID == "pending", LastFireStartedAt
// zero, LastFireAt older than the stale window) is flagged for reconciliation
// and handed to the composition callback, which records a terminal StopError
// fire and clears the in-flight state. A freshly-claimed pending fire (within
// the window) is NOT flagged.
func TestReconcileStaleFireAfterClaim(t *testing.T) {
	defer goleak.VerifyNone(t)
	restore := scheduler.SetStaleFireWindowForTest(1 * time.Minute)
	defer restore()

	for _, tc := range []struct {
		name      string
		claimAge  time.Duration // LastFireAt age relative to now
		wantStale bool
	}{
		{"stale", 2 * time.Minute, true},
		{"fresh", 10 * time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer goleak.VerifyNone(t)
			clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
			store := memschedulestore.New()
			fire := &fireStub{}

			now := clk.Now()
			claimAt := now.Add(-tc.claimAge)
			if err := store.Save(context.Background(), port.Schedule{
				Spec: port.ScheduleSpec{
					Name:    "crash-claim",
					Prompt:  "x",
					Trigger: port.TriggerSpec{Cron: "* * * * *"},
				},
				State: port.ScheduleState{
					// Already claimed: NextFireAt advanced into the future,
					// LastFireSessionID = pending (RecordFireStart never ran).
					NextFireAt:        now.Add(time.Hour),
					Enabled:           true,
					LastFireAt:        claimAt,
					LastFireSessionID: port.PendingFireSessionID,
					// LastFireStartedAt zero — RecordFireStart never ran.
				},
			}); err != nil {
				t.Fatalf("Save: %v", err)
			}

			var reconciled []port.Schedule
			s := scheduler.New(scheduler.Config{
				Store:              store,
				Lease:              nil, // no lease backend: window is the only oracle
				Fire:               fire.fire,
				Clock:              clk,
				TickInterval:       1 * time.Hour,
				MaxConcurrentFires: 4,
				ReconcileStaleFire: func(_ context.Context, sched port.Schedule) {
					reconciled = append(reconciled, sched)
				},
			})
			s.RunOnceForTest(context.Background())

			if tc.wantStale {
				if len(reconciled) != 1 {
					t.Fatalf("reconciled = %d, want 1 (stale pending fire should be reconciled)", len(reconciled))
				}
				if reconciled[0].State.LastFireSessionID != port.PendingFireSessionID {
					t.Errorf("reconciled schedule LastFireSessionID = %q, want pending sentinel", reconciled[0].State.LastFireSessionID)
				}
				if fire.count() != 0 {
					t.Errorf("fire ran %d times, want 0 (reconcile must NOT fire)", fire.count())
				}
			} else {
				if len(reconciled) != 0 {
					t.Fatalf("reconciled = %d, want 0 (fresh pending fire within window must NOT be reconciled)", len(reconciled))
				}
			}
		})
	}
}

// TestReconcileStaleFireAfterSession detects crash sub-case 2 (issue #386
// Phase 4b): a schedule whose RecordFireStart ran (LastFireSessionID is a real
// "sched--" id, LastFireStartedAt set) but whose RecordFire never ran, the
// session's lease is NOT live (the trial-lease acquired freely), and
// LastFireStartedAt is older than the stale window → flagged for
// reconciliation. A genuinely-live fire (lease held) is NOT flagged. A
// freshly-started fire (within the window) is NOT flagged.
func TestReconcileStaleFireAfterSession(t *testing.T) {
	defer goleak.VerifyNone(t)
	restore := scheduler.SetStaleFireWindowForTest(1 * time.Minute)
	defer restore()

	// A lease backend that holds "sched--live" (a genuinely-running fire) but
	// is free for "sched--crashed" (the crashed process's session lease lapsed).
	leaseBE := &heldSessionLease{
		held: map[session.SessionID]string{"sched--live": "owner-live"},
		clk:  &fakeClock{t: time.Unix(1_700_000_000, 0)},
	}

	for _, tc := range []struct {
		name      string
		sessID    session.SessionID
		startAge  time.Duration // LastFireStartedAt age relative to now
		wantStale bool
	}{
		{"crashed-stale", "sched--crashed", 2 * time.Minute, true},
		{"live-held", "sched--live", 2 * time.Minute, false},
		{"crashed-fresh", "sched--crashed", 10 * time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer goleak.VerifyNone(t)
			clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
			// Re-point the lease backend's clock so release/expiry uses the
			// per-subtest clock.
			leaseBE.clk = clk
			store := memschedulestore.New()
			fire := &fireStub{}

			now := clk.Now()
			startAt := now.Add(-tc.startAge)
			claimAt := startAt // Claim happened at/before the run start.
			if err := store.Save(context.Background(), port.Schedule{
				Spec: port.ScheduleSpec{
					Name:      "crash-run",
					Prompt:    "x",
					Trigger:   port.TriggerSpec{Cron: "* * * * *"},
					Singleton: true, // the lease check applies to singleton schedules
				},
				State: port.ScheduleState{
					NextFireAt:         now.Add(time.Hour),
					Enabled:            true,
					LastFireAt:         claimAt,
					LastFireSessionID:  tc.sessID,
					LastFireStartedAt:  startAt, // in-flight (RecordFireStart ran)
					LastFireProgressAt: startAt,
					// No FireDeadline: the window fallback applies.
				},
			}); err != nil {
				t.Fatalf("Save: %v", err)
			}
			// Persist an in-flight fire record (RecordFireStart's write) so the
			// store reflects a crashed-but-recorded-start fire.
			if err := store.RecordFireStart(context.Background(), "crash-run", port.ScheduleFire{
				ID:           string(tc.sessID),
				ScheduleName: "crash-run",
				SessionID:    tc.sessID,
				FiredAt:      claimAt,
				StartedAt:    startAt,
			}); err != nil {
				t.Fatalf("RecordFireStart: %v", err)
			}

			var reconciled []port.Schedule
			s := scheduler.New(scheduler.Config{
				Store:              store,
				Lease:              leaseBE,
				LeaseOwner:         "owner-this",
				Fire:               fire.fire,
				Clock:              clk,
				TickInterval:       1 * time.Hour,
				MaxConcurrentFires: 4,
				ReconcileStaleFire: func(_ context.Context, sched port.Schedule) {
					reconciled = append(reconciled, sched)
				},
			})
			s.RunOnceForTest(context.Background())

			if tc.wantStale {
				if len(reconciled) != 1 {
					t.Fatalf("reconciled = %d, want 1 (stale in-flight fire with lapsed lease should be reconciled)", len(reconciled))
				}
				if reconciled[0].State.LastFireSessionID != tc.sessID {
					t.Errorf("reconciled schedule LastFireSessionID = %q, want %q", reconciled[0].State.LastFireSessionID, tc.sessID)
				}
				if fire.count() != 0 {
					t.Errorf("fire ran %d times, want 0 (reconcile must NOT fire)", fire.count())
				}
			} else {
				if len(reconciled) != 0 {
					t.Fatalf("reconciled = %d, want 0 (%s must NOT be reconciled)", len(reconciled), tc.name)
				}
			}
		})
	}
}

// TestReconcileStaleFireNilCallbackNoop asserts the reconcile scan is a
// nil-safe no-op when the ReconcileStaleFire callback is unwired (the
// byte-identical pre-Phase-4b posture): a stale fire is detected but NOT
// settled, and the tick proceeds without panic.
func TestReconcileStaleFireNilCallbackNoop(t *testing.T) {
	defer goleak.VerifyNone(t)
	restore := scheduler.SetStaleFireWindowForTest(1 * time.Minute)
	defer restore()

	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}

	now := clk.Now()
	if err := store.Save(context.Background(), port.Schedule{
		Spec: port.ScheduleSpec{
			Name:    "stale-nil",
			Prompt:  "x",
			Trigger: port.TriggerSpec{Cron: "* * * * *"},
		},
		State: port.ScheduleState{
			NextFireAt:        now.Add(time.Hour),
			Enabled:           true,
			LastFireAt:        now.Add(-2 * time.Minute),
			LastFireSessionID: port.PendingFireSessionID,
		},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// No ReconcileStaleFire wired — the byte-identical no-reconcile path.
	s := scheduler.New(scheduler.Config{
		Store:              store,
		Lease:              nil,
		Fire:               fire.fire,
		Clock:              clk,
		TickInterval:       1 * time.Hour,
		MaxConcurrentFires: 4,
	})
	// Must not panic and must not settle (the stale fire stays pending).
	s.RunOnceForTest(context.Background())

	loaded, err := store.Load(context.Background(), "stale-nil")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The fire stays pending — the nil callback did not reconcile it.
	if loaded.State.LastFireSessionID != port.PendingFireSessionID {
		t.Errorf("LastFireSessionID = %q, want pending (nil callback must not settle)", loaded.State.LastFireSessionID)
	}
}
