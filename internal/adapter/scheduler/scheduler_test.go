package scheduler_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/stacklok/mecatl/engine/adapter/memschedulestore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/scheduler"
)

// fakeClock is an advanceable port.Clock the tests drive forward to express
// "the schedule is now due" / "the slot was missed" without real sleeps (the
// memschedulestore_test.go precedent).
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// recordedFire is what the stub FireFunc records per fire.
type recordedFire struct {
	sched port.Schedule
	now   time.Time
}

// fireStub is a FireFunc that records fires and optionally blocks (for the
// MaxConcurrentFires fan-out test) or returns an error (for the failed-fire
// test).
type fireStub struct {
	mu       sync.Mutex
	fires    []recordedFire
	err      error         // returned for every fire if non-nil
	blockCh  chan struct{} // if non-nil, a fire blocks until this is closed
	entered  chan struct{} // if non-nil, closed the first time a fire starts
	inflight atomic.Int32  // current in-flight count (for the fan-out test)
	maxSeen  atomic.Int32  // high-water in-flight count
}

func (f *fireStub) fire(ctx context.Context, sched port.Schedule, now time.Time) (port.ScheduleFire, error) {
	cur := f.inflight.Add(1)
	// record high-water for the concurrency test
	for {
		old := f.maxSeen.Load()
		if cur <= old || f.maxSeen.CompareAndSwap(old, cur) {
			break
		}
	}
	// Signal entry once (for the Stop-join test).
	if f.entered != nil {
		select {
		case <-f.entered:
		default:
			close(f.entered)
		}
	}
	defer f.inflight.Add(-1)
	f.mu.Lock()
	f.fires = append(f.fires, recordedFire{sched: sched, now: now})
	f.mu.Unlock()
	if f.blockCh != nil {
		select {
		case <-f.blockCh:
		case <-ctx.Done():
			return port.ScheduleFire{}, ctx.Err()
		}
	}
	if f.err != nil {
		return port.ScheduleFire{}, f.err
	}
	return port.ScheduleFire{
		ID:           fmt.Sprintf("fire-%s-%d", sched.Spec.Name, sched.State.FireCount),
		ScheduleName: sched.Spec.Name,
		SessionID:    session.SessionID("sched--" + sched.Spec.Name),
		FiredAt:      now,
		Stop:         session.StopEndTurn,
	}, nil
}

func (f *fireStub) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.fires)
}

// newTestScheduler builds a Scheduler with a fake clock, a mem schedule store,
// and a fire stub. The caller owns the returned clock/store/stub for driving.
func newTestScheduler(t *testing.T, store port.ScheduleStore, clk port.Clock, fire *fireStub) *scheduler.Scheduler {
	t.Helper()
	return scheduler.New(scheduler.Config{
		Store:              store,
		Lease:              nil, // standalone (no leader gate) for the at-most-once-via-Claim tests
		Fire:               fire.fire,
		Clock:              clk,
		TickInterval:       1 * time.Hour, // tests drive runOnce directly, not the ticker
		MaxConcurrentFires: 4,
	})
}

// TestFireOnceAndAdvances: a due schedule fires once when ticked; RecordFire
// recorded with the stop reason; NextFireAt advanced.
func TestFireOnceAndAdvances(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}
	s := newTestScheduler(t, store, clk, fire)

	// A cron schedule due now (NextFireAt == now), fires every minute.
	due := clk.Now()
	if err := store.Save(context.Background(), port.Schedule{
		Spec: port.ScheduleSpec{
			Name:    "heartbeat",
			Prompt:  "check status",
			Trigger: port.TriggerSpec{Cron: "* * * * *"},
		},
		State: port.ScheduleState{NextFireAt: due, Enabled: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s.RunOnceForTest(context.Background())

	if got := fire.count(); got != 1 {
		t.Fatalf("fires = %d, want 1", got)
	}
	// NextFireAt advanced past `now` to the next cron fire (strictly after now).
	loaded, err := store.Load(context.Background(), "heartbeat")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded.State.NextFireAt.After(due) {
		t.Fatalf("NextFireAt = %v, want after due %v", loaded.State.NextFireAt, due)
	}
	if loaded.State.FireCount != 1 {
		t.Fatalf("FireCount = %d, want 1", loaded.State.FireCount)
	}
	// RecordFire stored the fire outcome with the stop reason.
	fr, err := store.LoadFire(context.Background(), "fire-heartbeat-1")
	if err != nil {
		t.Fatalf("LoadFire: %v", err)
	}
	if fr.Stop != session.StopEndTurn {
		t.Fatalf("Stop = %q, want %q", fr.Stop, session.StopEndTurn)
	}
	if fr.SessionID != "sched--heartbeat" {
		t.Fatalf("SessionID = %q, want sched--heartbeat", fr.SessionID)
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestAtMostOnceStandalone: with Lease == nil (standalone), two schedulers
// sharing a mem store both tick, but the Claim fence ensures exactly one fires
// per slot.
func TestAtMostOnceStandalone(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fireA := &fireStub{}
	fireB := &fireStub{}
	sA := newTestScheduler(t, store, clk, fireA)
	sB := newTestScheduler(t, store, clk, fireB)

	due := clk.Now()
	if err := store.Save(context.Background(), port.Schedule{
		Spec: port.ScheduleSpec{
			Name:    "dual",
			Prompt:  "x",
			Trigger: port.TriggerSpec{Cron: "* * * * *"},
		},
		State: port.ScheduleState{NextFireAt: due, Enabled: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Both schedulers tick at the same `now`.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); sA.RunOnceForTest(context.Background()) }()
	go func() { defer wg.Done(); sB.RunOnceForTest(context.Background()) }()
	wg.Wait()

	total := fireA.count() + fireB.count()
	if total != 1 {
		t.Fatalf("total fires = %d, want 1 (at-most-once via Claim)", total)
	}
	// Exactly one of the two won the Claim.
	if fireA.count() == 1 && fireB.count() == 1 {
		t.Fatal("both schedulers fired (Claim fence failed)")
	}
	// NextFireAt advanced exactly once.
	loaded, err := store.Load(context.Background(), "dual")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.State.FireCount != 1 {
		t.Fatalf("FireCount = %d, want 1", loaded.State.FireCount)
	}

	if err := sA.Stop(); err != nil {
		t.Fatalf("Stop A: %v", err)
	}
	if err := sB.Stop(); err != nil {
		t.Fatalf("Stop B: %v", err)
	}
}

// TestStandDownOnLeaseHeld: a second scheduler with Lease != nil receiving
// ErrLeaseHeld on Start returns the error without ticking.
func TestStandDownOnLeaseHeld(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}

	// A leader lease backend already held by "owner-A".
	leaseBE := memleaseHeldBy(clk, "owner-A")
	// This scheduler tries to acquire as "owner-B" → ErrLeaseHeld.
	s := scheduler.New(scheduler.Config{
		Store:      store,
		Lease:      leaseBE,
		LeaseOwner: "owner-B",
		Fire:       fire.fire,
		Clock:      clk,
	})

	if err := s.Start(context.Background()); !errors.Is(err, port.ErrLeaseHeld) {
		t.Fatalf("Start err = %v, want ErrLeaseHeld", err)
	}
	// It stood down: no fire even though a schedule is due.
	due := clk.Now()
	if err := store.Save(context.Background(), port.Schedule{
		Spec:  port.ScheduleSpec{Name: "leased", Prompt: "x", Trigger: port.TriggerSpec{Cron: "* * * * *"}},
		State: port.ScheduleState{NextFireAt: due, Enabled: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// The stood-down scheduler never started its tick goroutine, so runOnce is
	// the only way it would fire — and Start returned an error so the caller
	// should not call it. Assert no fire by checking the store.
	loaded, _ := store.Load(context.Background(), "leased")
	if loaded.State.FireCount != 0 {
		t.Fatalf("FireCount = %d, want 0 (stood down)", loaded.State.FireCount)
	}
	// Stop is a no-op on a never-started scheduler (started flag is false after
	// the ErrLeaseHeld path reset it).
	_ = s.Stop()
}

// TestMisfireFireOnceNow: MisfireFireOnceNow (default) + past NextFireAt fires
// once + advances to a future next fire.
func TestMisfireFireOnceNow(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}
	s := newTestScheduler(t, store, clk, fire)

	// NextFireAt is an hour in the past (a missed slot).
	past := clk.Now().Add(-time.Hour)
	if err := store.Save(context.Background(), port.Schedule{
		Spec: port.ScheduleSpec{
			Name:    "catchup",
			Prompt:  "x",
			Trigger: port.TriggerSpec{Cron: "* * * * *"},
			Misfire: port.MisfireFireOnceNow, // default, explicit for clarity
		},
		State: port.ScheduleState{NextFireAt: past, Enabled: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s.RunOnceForTest(context.Background())

	if got := fire.count(); got != 1 {
		t.Fatalf("fires = %d, want 1 (catch-up fire)", got)
	}
	loaded, err := store.Load(context.Background(), "catchup")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// NextFireAt advanced to the next fire AFTER now (not after the past slot).
	if !loaded.State.NextFireAt.After(clk.Now()) {
		t.Fatalf("NextFireAt = %v, want after now %v", loaded.State.NextFireAt, clk.Now())
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestMisfireSkip: MisfireSkip + a slot missed beyond the grace window (the
// process was down) → Claim advances, NO fire.
func TestMisfireSkip(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}
	s := newTestScheduler(t, store, clk, fire)

	// The test scheduler's TickInterval is 1h, so the misfire grace window is 1h.
	// A slot missed by 2h is clearly beyond it — a genuine missed window, skipped.
	past := clk.Now().Add(-2 * time.Hour)
	if err := store.Save(context.Background(), port.Schedule{
		Spec: port.ScheduleSpec{
			Name:    "skip",
			Prompt:  "x",
			Trigger: port.TriggerSpec{Cron: "* * * * *"},
			Misfire: port.MisfireSkip,
		},
		State: port.ScheduleState{NextFireAt: past, Enabled: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s.RunOnceForTest(context.Background())

	if got := fire.count(); got != 0 {
		t.Fatalf("fires = %d, want 0 (MisfireSkip beyond grace)", got)
	}
	loaded, err := store.Load(context.Background(), "skip")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Claim still advanced NextFireAt (the slot is not re-returned).
	if loaded.State.FireCount != 1 {
		t.Fatalf("FireCount = %d, want 1 (Claim advanced without firing)", loaded.State.FireCount)
	}
	if !loaded.State.NextFireAt.After(clk.Now()) {
		t.Fatalf("NextFireAt = %v, want after now", loaded.State.NextFireAt)
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestMisfireSkipFreshSlotStillFires: a MisfireSkip schedule whose slot just
// became due (missed by LESS than the grace window — ordinary poll jitter) still
// FIRES. This is the regression guard for the bug where a bare
// `NextFireAt.Before(now)` skipped every fire, so a MisfireSkip schedule never
// fired at all (review #189).
func TestMisfireSkipFreshSlotStillFires(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}
	s := newTestScheduler(t, store, clk, fire)

	// Missed by one minute — far inside the 1h grace window: a freshly-due slot,
	// which MisfireSkip must still fire (not treat as a missed window).
	fresh := clk.Now().Add(-time.Minute)
	if err := store.Save(context.Background(), port.Schedule{
		Spec: port.ScheduleSpec{
			Name:    "skip-fresh",
			Prompt:  "x",
			Trigger: port.TriggerSpec{Cron: "* * * * *"},
			Misfire: port.MisfireSkip,
		},
		State: port.ScheduleState{NextFireAt: fresh, Enabled: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s.RunOnceForTest(context.Background())

	if got := fire.count(); got != 1 {
		t.Fatalf("fires = %d, want 1 (fresh MisfireSkip slot must still fire)", got)
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestMaxFiresExhaustion: after N fires, the schedule is disabled (Claim
// returns not-found / Due no longer returns it).
func TestMaxFiresExhaustion(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}
	s := newTestScheduler(t, store, clk, fire)

	// A cron that fires at most 2 times.
	if err := store.Save(context.Background(), port.Schedule{
		Spec: port.ScheduleSpec{
			Name:     "bounded",
			Prompt:   "x",
			Trigger:  port.TriggerSpec{Cron: "* * * * *"},
			MaxFires: 2,
		},
		State: port.ScheduleState{NextFireAt: clk.Now(), Enabled: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Fire 1.
	s.RunOnceForTest(context.Background())
	if got := fire.count(); got != 1 {
		t.Fatalf("after tick 1: fires = %d, want 1", got)
	}
	// Advance to the next fire.
	loaded, _ := store.Load(context.Background(), "bounded")
	clk.advance(loaded.State.NextFireAt.Sub(clk.Now()))

	// Fire 2 (exhausts MaxFires).
	s.RunOnceForTest(context.Background())
	if got := fire.count(); got != 2 {
		t.Fatalf("after tick 2: fires = %d, want 2", got)
	}
	loaded, _ = store.Load(context.Background(), "bounded")
	if loaded.State.Enabled {
		t.Fatalf("after exhaustion: Enabled = true, want false")
	}
	if !loaded.State.NextFireAt.IsZero() {
		t.Fatalf("after exhaustion: NextFireAt = %v, want zero", loaded.State.NextFireAt)
	}

	// A third tick fires nothing (Due no longer returns it; Claim would
	// not-found).
	clk.advance(time.Minute)
	s.RunOnceForTest(context.Background())
	if got := fire.count(); got != 2 {
		t.Fatalf("after tick 3: fires = %d, want 2 (exhausted)", got)
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestOneShotFiresOnceThenDisabled: a one-shot fires once then is disabled.
func TestOneShotFiresOnceThenDisabled(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}
	s := newTestScheduler(t, store, clk, fire)

	if err := store.Save(context.Background(), port.Schedule{
		Spec: port.ScheduleSpec{
			Name:    "once",
			Prompt:  "x",
			Trigger: port.TriggerSpec{OneShot: clk.Now()},
		},
		State: port.ScheduleState{NextFireAt: clk.Now(), Enabled: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s.RunOnceForTest(context.Background())
	if got := fire.count(); got != 1 {
		t.Fatalf("fires = %d, want 1", got)
	}
	loaded, _ := store.Load(context.Background(), "once")
	if loaded.State.Enabled {
		t.Fatalf("Enabled = true, want false (one-shot done)")
	}
	if !loaded.State.NextFireAt.IsZero() {
		t.Fatalf("NextFireAt = %v, want zero", loaded.State.NextFireAt)
	}

	// A second tick fires nothing.
	s.RunOnceForTest(context.Background())
	if got := fire.count(); got != 1 {
		t.Fatalf("after tick 2: fires = %d, want 1", got)
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestMaxConcurrentFiresBoundsFanout: fire N due schedules; the stub blocks
// until a channel is closed; assert at most MaxConcurrentFires are in-flight.
func TestMaxConcurrentFiresBoundsFanout(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	block := make(chan struct{})
	fire := &fireStub{blockCh: block}
	s := scheduler.New(scheduler.Config{
		Store:              store,
		Fire:               fire.fire,
		Clock:              clk,
		MaxConcurrentFires: 3,
	})

	// 8 due schedules.
	for i := 0; i < 8; i++ {
		name := fmt.Sprintf("fan-%d", i)
		if err := store.Save(context.Background(), port.Schedule{
			Spec: port.ScheduleSpec{
				Name:    name,
				Prompt:  "x",
				Trigger: port.TriggerSpec{Cron: "* * * * *"},
			},
			State: port.ScheduleState{NextFireAt: clk.Now(), Enabled: true},
		}); err != nil {
			t.Fatalf("Save %s: %v", name, err)
		}
	}

	// Run one tick in a goroutine (it will block on the fire stub).
	tickDone := make(chan struct{})
	go func() { s.RunOnceForTest(context.Background()); close(tickDone) }()

	// Wait for the fan-out to reach steady state (3 in-flight), then unblock.
	// Poll for the high-water mark with a short deadline.
	deadline := time.After(2 * time.Second)
	for fire.maxSeen.Load() < 3 {

		select {
		case <-deadline:
			t.Fatalf("max in-flight = %d, want >= 3 (fan-out never reached the bound)", fire.maxSeen.Load())
		default:
		}
		runtimeYield()
	}
	// The bound is 3, so the high-water should never exceed 3.
	if got := fire.maxSeen.Load(); got > 3 {
		t.Fatalf("max in-flight = %d, want <= 3 (MaxConcurrentFires bound violated)", got)
	}
	// Unblock all fires and let the tick complete (the errgroup runs the
	// remaining queued fires after the 3 in-flight ones unwind).
	close(block)
	select {
	case <-tickDone:
	case <-time.After(5 * time.Second):
		t.Fatal("RunOnceForTest did not complete after unblock")
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := fire.count(); got != 8 {
		t.Fatalf("total fires = %d, want 8", got)
	}
}

// TestStopJoinsInflightFires: a fire mid-run when Stop is called completes or
// is cancelled cleanly; done is closed.
func TestStopJoinsInflightFires(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	release := make(chan struct{})
	entered := make(chan struct{})
	fire := &fireStub{blockCh: release, entered: entered}
	s := scheduler.New(scheduler.Config{
		Store:              store,
		Fire:               fire.fire,
		Clock:              clk,
		TickInterval:       1 * time.Hour, // the real ticker won't fire; we drive via RunOnceForTest
		MaxConcurrentFires: 4,
		StopFireGrace:      200 * time.Millisecond,
	})

	if err := store.Save(context.Background(), port.Schedule{
		Spec:  port.ScheduleSpec{Name: "slow", Prompt: "x", Trigger: port.TriggerSpec{Cron: "* * * * *"}},
		State: port.ScheduleState{NextFireAt: clk.Now(), Enabled: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Start the scheduler so RunOnceForTest runs under the scheduler's tick ctx
	// (which Stop cancels, exactly as the real ticker would). The 1h
	// TickInterval means the real ticker does not fire during the test.
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Run one tick in a goroutine; the fire blocks on `release` (and signals
	// `entered` once it has started).
	go s.RunOnceForTest(context.Background())
	<-entered

	// Stop cancels the tick ctx (the fire's ctx derives from it), so the stub's
	// ctx.Done() fires and the in-flight fire unwinds. Stop then joins and
	// closes done.
	stopDone := make(chan struct{})
	go func() {
		_ = s.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
		// Stop completed (the fire unwound on ctx cancel within the grace).
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not complete within 5s (in-flight fire not joined)")
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("Done not closed after Stop")
	}
	// Close release to unblock any lingering path (no-op if the fire already
	// unwound via ctx).
	close(release)
}

// TestDrainStopsNewFires: Drain() stops new fires mid-tick.
func TestDrainStopsNewFires(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}
	s := newTestScheduler(t, store, clk, fire)

	if err := store.Save(context.Background(), port.Schedule{
		Spec:  port.ScheduleSpec{Name: "d", Prompt: "x", Trigger: port.TriggerSpec{Cron: "* * * * *"}},
		State: port.ScheduleState{NextFireAt: clk.Now(), Enabled: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Drain before the tick → no fire.
	s.Drain()
	if !s.IsDraining() {
		t.Fatal("IsDraining = false, want true after Drain")
	}
	s.RunOnceForTest(context.Background())
	if got := fire.count(); got != 0 {
		t.Fatalf("fires = %d, want 0 (draining)", got)
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestFailedFireRecordedAsStopError: a FireFunc error is recorded as a failed
// fire (StopError) and NOT retried (the slot is gone).
func TestFailedFireRecordedAsStopError(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{err: errors.New("boom")}
	s := newTestScheduler(t, store, clk, fire)

	if err := store.Save(context.Background(), port.Schedule{
		Spec:  port.ScheduleSpec{Name: "fail", Prompt: "x", Trigger: port.TriggerSpec{Cron: "* * * * *"}},
		State: port.ScheduleState{NextFireAt: clk.Now(), Enabled: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s.RunOnceForTest(context.Background())

	// The fire was attempted (count=1) and recorded as StopError.
	if got := fire.count(); got != 1 {
		t.Fatalf("fires = %d, want 1", got)
	}
	loaded, _ := store.Load(context.Background(), "fail")
	if loaded.State.FireCount != 1 {
		t.Fatalf("FireCount = %d, want 1 (Claim advanced)", loaded.State.FireCount)
	}
	// The fire record exists with StopError + the error string.
	// The stub returns a zero ScheduleFire on error, so the scheduler fills it;
	// the ID is the zero-value fire.ID ("").
	fr, err := store.LoadFire(context.Background(), "")
	if err != nil {
		t.Fatalf("LoadFire: %v", err)
	}
	if fr.Stop != session.StopError {
		t.Fatalf("Stop = %q, want %q", fr.Stop, session.StopError)
	}
	if fr.Err != "boom" {
		t.Fatalf("Err = %q, want boom", fr.Err)
	}
	// NextFireAt still advanced (no retry).
	if !loaded.State.NextFireAt.After(clk.Now()) {
		t.Fatalf("NextFireAt = %v, want after now (no retry)", loaded.State.NextFireAt)
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestSingletonOverlapPreservesLivePointer is the regression test for review
// finding #1: when a singleton schedule's prior fire is still running (its session
// lease is held by any replica), the skip path MUST NOT Claim — a Claim would
// overwrite LastFireSessionID with port.PendingFireSessionID, destroying the
// pointer to the still-running prior fire. On the next tick the singleton check
// (gated on LastFireSessionID != pending) would then be SKIPPED, and a second
// fire would launch concurrently with the still-running prior fire, defeating the
// singleton guarantee.
//
// The fix: the skip path leaves the slot due; the next tick re-checks the
// singleton via the trial-lease and skips again while the prior fire holds it.
// This test pins that the pointer survives a skip and a second tick still skips.
func TestSingletonOverlapPreservesLivePointer(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}

	// A lease backend that holds a specific session id ("sched--prior") — any
	// Acquire on that id from a different owner returns ErrLeaseHeld. Releasing
	// it (via Release) clears the hold so a later Acquire succeeds.
	leaseBE := &heldSessionLease{held: map[session.SessionID]string{"sched--prior": "owner-prior"}, clk: clk}
	s := scheduler.New(scheduler.Config{
		Store:              store,
		Lease:              leaseBE,
		LeaseOwner:         "owner-this",
		Fire:               fire.fire,
		Clock:              clk,
		TickInterval:       1 * time.Hour,
		MaxConcurrentFires: 4,
	})

	due := clk.Now()
	if err := store.Save(context.Background(), port.Schedule{
		Spec: port.ScheduleSpec{
			Name:      "singleton",
			Prompt:    "x",
			Trigger:   port.TriggerSpec{Cron: "* * * * *"},
			Singleton: true,
		},
		State: port.ScheduleState{
			NextFireAt:        due,
			Enabled:           true,
			LastFireSessionID: "sched--prior", // a prior fire ran and is still running
		},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Tick 1: the prior fire's lease is held → singleton skip. The skip MUST NOT
	// Claim, so LastFireSessionID stays "sched--prior" (not port.PendingFireSessionID)
	// and FireCount stays 0.
	s.RunOnceForTest(context.Background())
	if got := fire.count(); got != 0 {
		t.Fatalf("tick 1: fires = %d, want 0 (singleton overlap skip)", got)
	}
	loaded, _ := store.Load(context.Background(), "singleton")
	if loaded.State.LastFireSessionID != "sched--prior" {
		t.Fatalf("tick 1: LastFireSessionID = %q, want %q (skip must not clobber the live pointer)",
			loaded.State.LastFireSessionID, "sched--prior")
	}
	if loaded.State.FireCount != 0 {
		t.Fatalf("tick 1: FireCount = %d, want 0 (skip must not Claim)", loaded.State.FireCount)
	}

	// Tick 2: the prior fire is STILL running. This is the crux of finding #1 —
	// if the skip-path had Claimed, LastFireSessionID would now be "pending", the
	// singleton check would be skipped, and a second fire would launch. It MUST
	// skip again.
	s.RunOnceForTest(context.Background())
	if got := fire.count(); got != 0 {
		t.Fatalf("tick 2: fires = %d, want 0 (singleton still overlapping)", got)
	}
	loaded, _ = store.Load(context.Background(), "singleton")
	if loaded.State.LastFireSessionID != "sched--prior" {
		t.Fatalf("tick 2: LastFireSessionID = %q, want %q (pointer clobbered by skip-Claim bug)",
			loaded.State.LastFireSessionID, "sched--prior")
	}

	// Now the prior fire finishes: release its lease. The next tick's singleton
	// check acquires freely → Claims + fires.
	leaseBE.release("sched--prior")
	s.RunOnceForTest(context.Background())
	if got := fire.count(); got != 1 {
		t.Fatalf("tick 3: fires = %d, want 1 (prior fire finished; singleton fires)", got)
	}
	loaded, _ = store.Load(context.Background(), "singleton")
	if loaded.State.FireCount != 1 {
		t.Fatalf("tick 3: FireCount = %d, want 1", loaded.State.FireCount)
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// --- helpers -----------------------------------------------------------------

// runtimeYield yields the goroutine to let the fan-out reach steady state.
func runtimeYield() {
	time.Sleep(time.Millisecond)
}

// memleaseHeldBy returns a memlease.Lease already held by `owner` so a second
// owner's Acquire returns ErrLeaseHeld. It uses the engine/adapter/memlease
// reference (the scheduler test may import engine/adapter/* — it is a test
// file, and memlease is an engine/adapter reference adapter).
func memleaseHeldBy(clk port.Clock, owner string) port.SessionLease {
	// Constructed inline via the memlease package would be cleaner, but to
	// avoid an extra import indirection we use a tiny stand-in that always
	// returns ErrLeaseHeld for any owner != owner. This is sufficient for the
	// StandDownOnLeaseHeld test, which only needs Start to see ErrLeaseHeld.
	return &heldLease{clk: clk, owner: owner}
}

// heldLease is a minimal port.SessionLease that is pre-held by `owner`: any
// Acquire from a different owner returns ErrLeaseHeld, and Renew/Release are
// no-ops (the scheduler under test stands down before ever renewing).
type heldLease struct {
	clk   port.Clock
	owner string
}

func (h *heldLease) Acquire(_ context.Context, id session.SessionID, owner string) (port.Lease, error) {
	if owner == h.owner {
		// same owner re-acquire: grant.
		return port.Lease{SessionID: id, Owner: owner, Token: 1, Expiry: h.clk.Now().Add(30 * time.Second)}, nil
	}
	return port.Lease{}, port.ErrLeaseHeld
}
func (*heldLease) Renew(_ context.Context, _ port.Lease) (port.Lease, error) {
	return port.Lease{}, port.ErrLeaseHeld
}
func (*heldLease) Release(_ context.Context, _ port.Lease) error { return nil }

// heldSessionLease is a port.SessionLease that tracks per-session-id holds. A
// held id returns ErrLeaseHeld to any Acquire from a different owner; release(id)
// clears the hold so a later Acquire succeeds. It is the singleton-overlap
// test's liveness oracle: a held "sched--prior" simulates a still-running prior
// fire; release simulates its completion.
type heldSessionLease struct {
	mu   sync.Mutex
	held map[session.SessionID]string // id -> owner
	clk  port.Clock
}

func (h *heldSessionLease) Acquire(_ context.Context, id session.SessionID, owner string) (port.Lease, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cur, ok := h.held[id]; ok && cur != owner {
		return port.Lease{}, port.ErrLeaseHeld
	}
	h.held[id] = owner
	return port.Lease{SessionID: id, Owner: owner, Token: 1, Expiry: h.clk.Now().Add(30 * time.Second)}, nil
}
func (*heldSessionLease) Renew(_ context.Context, l port.Lease) (port.Lease, error) { return l, nil }
func (h *heldSessionLease) Release(_ context.Context, l port.Lease) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.held, l.SessionID)
	return nil
}
func (h *heldSessionLease) release(id session.SessionID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.held, id)
}
