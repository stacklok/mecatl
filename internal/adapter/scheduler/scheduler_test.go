package scheduler_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	mu           sync.Mutex
	fires        []recordedFire
	err          error              // returned for every fire if non-nil
	stopOverride session.StopReason // if non-empty, overrides the default StopEndTurn return
	blockCh      chan struct{}      // if non-nil, a fire blocks until this is closed
	entered      chan struct{}      // if non-nil, closed the first time a fire starts
	inflight     atomic.Int32       // current in-flight count (for the fan-out test)
	maxSeen      atomic.Int32       // high-water in-flight count
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
	stop := session.StopEndTurn
	if f.stopOverride != "" {
		stop = f.stopOverride
	}
	return port.ScheduleFire{
		ID:           fmt.Sprintf("fire-%s-%d", sched.Spec.Name, sched.State.FireCount),
		ScheduleName: sched.Spec.Name,
		SessionID:    session.SessionID("sched--" + sched.Spec.Name),
		FiredAt:      now,
		Stop:         stop,
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

// TestStandbyOnLeaseHeld: a lease-backed scheduler whose Acquire loses to a
// peer enters STANDBY — Start is INFALLIBLE (returns nil), the replica does NOT
// tick/fire, LeaderOwner reports leader=false, and Stop joins the
// leadership-loop goroutine. This is the multi-replica availability contract:
// a non-leader replica must SERVE (Start succeeds — no CrashLoop) and stand by
// to take over when the leader's lease lapses.
func TestStandbyOnLeaseHeld(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}

	// A leader lease backend already held by "owner-A".
	leaseBE := memleaseHeldBy(clk, "owner-A")
	// This scheduler tries to acquire as "owner-B" → ErrLeaseHeld → standby.
	// The short TickInterval makes the no-fire assertion meaningful: had the
	// replica (wrongly) started ticking, a due schedule would fire quickly.
	s := scheduler.New(scheduler.Config{
		Store:        store,
		Lease:        leaseBE,
		LeaseOwner:   "owner-B",
		Fire:         fire.fire,
		Clock:        clk,
		TickInterval: 25 * time.Millisecond,
	})

	// A schedule due now: a standby replica must NOT fire it.
	due := clk.Now()
	if err := store.Save(context.Background(), port.Schedule{
		Spec:  port.ScheduleSpec{Name: "leased", Prompt: "x", Trigger: port.TriggerSpec{Cron: "* * * * *"}},
		State: port.ScheduleState{NextFireAt: due, Enabled: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Start is infallible: a peer holding the leader lease is NOT an error.
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start err = %v, want nil (standby is not a start failure)", err)
	}

	// Several would-be tick intervals: a ticking replica would have fired.
	time.Sleep(150 * time.Millisecond)
	if got := fire.count(); got != 0 {
		t.Fatalf("fires = %d, want 0 (standby: a peer holds the leader lease)", got)
	}
	loaded, _ := store.Load(context.Background(), "leased")
	if loaded.State.FireCount != 0 {
		t.Fatalf("FireCount = %d, want 0 (standby)", loaded.State.FireCount)
	}
	if _, leader := s.LeaderOwner(); leader {
		t.Fatal("LeaderOwner leader = true, want false (started, lease-backed, in standby)")
	}

	// Stop joins the standby leadership-loop goroutine (goleak).
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
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

// TestFireNow: the manual fire path Claims + fires a due schedule and records the
// fire. It mirrors fireOne's tail via the shared fireClaimed helper.
func TestFireNow(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}
	s := newTestScheduler(t, store, clk, fire)

	due := clk.Now()
	if err := store.Save(context.Background(), port.Schedule{
		Spec: port.ScheduleSpec{
			Name:    "manual",
			Prompt:  "x",
			Trigger: port.TriggerSpec{Cron: "* * * * *"},
		},
		State: port.ScheduleState{NextFireAt: due, Enabled: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	fr, err := s.FireNow(context.Background(), "manual", clk.Now())
	if err != nil {
		t.Fatalf("FireNow: %v", err)
	}
	if fr.ScheduleName != "manual" {
		t.Errorf("ScheduleName = %q, want manual", fr.ScheduleName)
	}
	if fr.Stop != session.StopEndTurn {
		t.Errorf("Stop = %q, want %q", fr.Stop, session.StopEndTurn)
	}
	if got := fire.count(); got != 1 {
		t.Fatalf("fires = %d, want 1", got)
	}
	// Claim advanced + the fire record was stored.
	loaded, _ := store.Load(context.Background(), "manual")
	if loaded.State.FireCount != 1 {
		t.Fatalf("FireCount = %d, want 1", loaded.State.FireCount)
	}
	got, err := store.LoadFire(context.Background(), fr.ID)
	if err != nil {
		t.Fatalf("LoadFire: %v", err)
	}
	if got.Stop != session.StopEndTurn {
		t.Fatalf("stored fire Stop = %q, want %q", got.Stop, session.StopEndTurn)
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestFireNowDisabled: a paused/done schedule is rejected with ErrFireNowDisabled
// and is NOT claimed.
func TestFireNowDisabled(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}
	s := newTestScheduler(t, store, clk, fire)

	if err := store.Save(context.Background(), port.Schedule{
		Spec:  port.ScheduleSpec{Name: "paused", Prompt: "x", Trigger: port.TriggerSpec{Cron: "* * * * *"}},
		State: port.ScheduleState{NextFireAt: clk.Now(), Enabled: false},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := s.FireNow(context.Background(), "paused", clk.Now()); !errors.Is(err, scheduler.ErrFireNowDisabled) {
		t.Fatalf("FireNow on disabled = %v, want ErrFireNowDisabled", err)
	}
	if got := fire.count(); got != 0 {
		t.Fatalf("fires = %d, want 0 (disabled schedule not fired)", got)
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestFireNowOneShotExhausted: a one-shot that has already fired is rejected
// with ErrFireNowExhausted.
func TestFireNowOneShotExhausted(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}
	s := newTestScheduler(t, store, clk, fire)

	if err := store.Save(context.Background(), port.Schedule{
		Spec:  port.ScheduleSpec{Name: "once", Prompt: "x", Trigger: port.TriggerSpec{OneShot: clk.Now()}},
		State: port.ScheduleState{NextFireAt: clk.Now(), Enabled: true, FireCount: 1},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := s.FireNow(context.Background(), "once", clk.Now()); !errors.Is(err, scheduler.ErrFireNowExhausted) {
		t.Fatalf("FireNow on exhausted one-shot = %v, want ErrFireNowExhausted", err)
	}
	if got := fire.count(); got != 0 {
		t.Fatalf("fires = %d, want 0 (exhausted one-shot not fired)", got)
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestFireNowSingletonOverlap: a singleton schedule whose prior fire is still
// running is rejected with ErrFireNowOverlap and is NOT claimed (the live
// pointer is preserved, mirroring the tick loop's singleton skip).
func TestFireNowSingletonOverlap(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}

	leaseBE := &heldSessionLease{held: map[session.SessionID]string{"sched--prior": "owner-prior"}, clk: clk}
	s := scheduler.New(scheduler.Config{
		Store:      store,
		Lease:      leaseBE,
		LeaseOwner: "owner-this",
		Fire:       fire.fire,
		Clock:      clk,
	})

	if err := store.Save(context.Background(), port.Schedule{
		Spec: port.ScheduleSpec{
			Name:      "singleton",
			Prompt:    "x",
			Trigger:   port.TriggerSpec{Cron: "* * * * *"},
			Singleton: true,
		},
		State: port.ScheduleState{
			NextFireAt:        clk.Now(),
			Enabled:           true,
			LastFireSessionID: "sched--prior",
		},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := s.FireNow(context.Background(), "singleton", clk.Now()); !errors.Is(err, scheduler.ErrFireNowOverlap) {
		t.Fatalf("FireNow on overlapping singleton = %v, want ErrFireNowOverlap", err)
	}
	if got := fire.count(); got != 0 {
		t.Fatalf("fires = %d, want 0 (overlapping singleton not fired)", got)
	}
	// The live pointer is preserved (no Claim clobbered it).
	loaded, _ := store.Load(context.Background(), "singleton")
	if loaded.State.LastFireSessionID != "sched--prior" {
		t.Fatalf("LastFireSessionID = %q, want %q (skip must not clobber the live pointer)",
			loaded.State.LastFireSessionID, "sched--prior")
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestFireNowEmitCallback: the EmitScheduleEvent callback fires for a successful
// FireNow (kind="fired") and for the failed path (kind="failed").
func TestFireNowEmitCallback(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}

	var gotMu sync.Mutex
	var got []session.SchedulePayload
	emit := func(p session.SchedulePayload) {
		gotMu.Lock()
		got = append(got, p)
		gotMu.Unlock()
	}
	s := scheduler.New(scheduler.Config{
		Store:              store,
		Fire:               fire.fire,
		Clock:              clk,
		TickInterval:       1 * time.Hour,
		MaxConcurrentFires: 4,
		EmitScheduleEvent:  emit,
	})

	if err := store.Save(context.Background(), port.Schedule{
		Spec:  port.ScheduleSpec{Name: "emit-ok", Prompt: "x", Trigger: port.TriggerSpec{Cron: "* * * * *"}},
		State: port.ScheduleState{NextFireAt: clk.Now(), Enabled: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := s.FireNow(context.Background(), "emit-ok", clk.Now()); err != nil {
		t.Fatalf("FireNow: %v", err)
	}
	gotMu.Lock()
	if len(got) != 1 || got[0].Kind != "fired" || got[0].ScheduleName != "emit-ok" {
		t.Fatalf("emit on success = %+v, want one fired payload for emit-ok", got)
	}
	gotMu.Unlock()

	// A failed fire emits kind="failed".
	fire.err = errors.New("boom")
	if err := store.Save(context.Background(), port.Schedule{
		Spec:  port.ScheduleSpec{Name: "emit-fail", Prompt: "x", Trigger: port.TriggerSpec{Cron: "* * * * *"}},
		State: port.ScheduleState{NextFireAt: clk.Now().Add(time.Second), Enabled: true},
	}); err != nil {
		t.Fatalf("Save fail: %v", err)
	}
	clk.advance(time.Minute) // make the new schedule due
	if _, err := s.FireNow(context.Background(), "emit-fail", clk.Now()); err == nil {
		t.Fatal("FireNow on failing fire = nil, want the fire error")
	}
	gotMu.Lock()
	if len(got) != 2 || got[1].Kind != "failed" || got[1].ScheduleName != "emit-fail" {
		t.Fatalf("emit on failure = %+v, want a failed payload for emit-fail as the 2nd", got)
	}
	gotMu.Unlock()

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestFireNowStopErrorEmitsFailed: a FireFunc that returns (fire, nil) with
// fire.Stop == StopError (a run that drained to a terminal EvResult carrying
// StopError but NO Go error — the makeFireFunc shape) is emitted as
// EvScheduleFailed (kind="failed"), NOT EvScheduleFired. This is the S4 fix:
// keying the failed emit off fireErr alone would misclassify this as "fired".
func TestFireNowStopErrorEmitsFailed(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	// A fire stub that returns a nil error but a StopError stop (the
	// makeFireFunc shape for a run that failed without a Go error).
	fire := &fireStub{stopOverride: session.StopError}

	var gotMu sync.Mutex
	var got []session.SchedulePayload
	emit := func(p session.SchedulePayload) {
		gotMu.Lock()
		got = append(got, p)
		gotMu.Unlock()
	}
	s := scheduler.New(scheduler.Config{
		Store:              store,
		Fire:               fire.fire,
		Clock:              clk,
		TickInterval:       1 * time.Hour,
		MaxConcurrentFires: 4,
		EmitScheduleEvent:  emit,
	})

	if err := store.Save(context.Background(), port.Schedule{
		Spec:  port.ScheduleSpec{Name: "stop-err", Prompt: "x", Trigger: port.TriggerSpec{Cron: "* * * * *"}},
		State: port.ScheduleState{NextFireAt: clk.Now(), Enabled: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fr, err := s.FireNow(context.Background(), "stop-err", clk.Now())
	if err != nil {
		t.Fatalf("FireNow: %v (a nil fireErr with StopError is NOT a scheduler error)", err)
	}
	if fr.Stop != session.StopError {
		t.Fatalf("Stop = %q, want %q", fr.Stop, session.StopError)
	}
	gotMu.Lock()
	if len(got) != 1 || got[0].Kind != "failed" || got[0].ScheduleName != "stop-err" {
		t.Fatalf("emit = %+v, want one failed payload for stop-err (StopError with nil fireErr)", got)
	}
	gotMu.Unlock()

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestRenewLeaderDefinitiveLossDemotesThenRecovers is the renewLeader/demote
// coverage test for the DEFINITIVE-loss path under the standby leadership
// model: once Renew returns port.ErrLeaseHeld (a competitor took over the
// __scheduler__ leader lease), demote() must tear down the CURRENT epoch (the
// tick loop stops, so we do not double-fire against the new leader) and the
// leadership loop returns to standby. Because the backend allows this replica
// to re-acquire, it then FAILS OVER: re-promotes and resumes ticking.
//
// It exercises the real Start() ticker (not RunOnceForTest, which drives
// tickOnce directly regardless of whether the tick ctx was cancelled and so
// cannot observe "the epoch stopped"): a schedule due at Start proves the
// first epoch is alive; a captured diagnostics WARN proves demote ran; a
// second schedule made due strictly AFTER that WARN firing proves the replica
// re-acquired leadership and resumed ticking (the failover contract — a
// demoted replica that never recovers would leave the schedule un-fired).
func TestRenewLeaderDefinitiveLossDemotesThenRecovers(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}
	diag := &capturingDiag{}

	// Acquire always succeeds (for "owner-this"); every Renew is a DEFINITIVE
	// loss (ErrLeaseHeld) — mirrors a peer having taken over. Each epoch the
	// renewer demotes the replica; the leadership loop then re-acquires and
	// starts a fresh epoch (the failover cycle this test pins).
	lease := &renewFailLease{}
	s := scheduler.New(scheduler.Config{
		Store:        store,
		Lease:        lease,
		LeaseOwner:   "owner-this",
		Fire:         fire.fire,
		Clock:        clk,
		Diagnostics:  diag,
		TickInterval: 25 * time.Millisecond,
		// LeaseRenewInterval is deliberately an order of magnitude larger than
		// TickInterval: the test relies on the first tick firing the due
		// "before-loss" schedule BEFORE the renewer's first (definitively-lost)
		// Renew cancels the epoch's tick ctx. With equal intervals the two
		// tickers race and under CI load the renewer can win, cancelling
		// ticking before the initial schedule ever fires.
		LeaseRenewInterval: 250 * time.Millisecond,
		MaxConcurrentFires: 4,
	})

	// A schedule already due at Start: proves the ticker is alive before the
	// leader-lease loss.
	due := clk.Now()
	if err := store.Save(context.Background(), port.Schedule{
		Spec:  port.ScheduleSpec{Name: "before-loss", Prompt: "x", Trigger: port.TriggerSpec{Cron: "* * * * *"}},
		State: port.ScheduleState{NextFireAt: due, Enabled: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if !pollUntil(10*time.Second, func() bool { return fire.count() >= 1 }) {
		t.Fatal("scheduler never fired the initial due schedule (ticker not running)")
	}
	if !pollUntil(10*time.Second, diag.sawLostLease) {
		t.Fatal("renewLeader never declared the leader lease lost on a definitive ErrLeaseHeld")
	}

	// The definitive loss demoted the replica (the first epoch's tick loop
	// stopped). The standby leadership loop retries the acquire (2s backoff),
	// renewFailLease grants it, and a fresh epoch resumes ticking — a schedule
	// made due AFTER the declared loss eventually fires on the new epoch. (We
	// deliberately do NOT advance the clock: the already-fired "before-loss"
	// cron has advanced its own NextFireAt to the next minute, so it stays
	// quiescent on the fixed fake clock — advancing would re-arm it and
	// pollute the count.)
	countAtLoss := fire.count()
	if err := store.Save(context.Background(), port.Schedule{
		Spec:  port.ScheduleSpec{Name: "after-loss", Prompt: "x", Trigger: port.TriggerSpec{Cron: "* * * * *"}},
		State: port.ScheduleState{NextFireAt: clk.Now(), Enabled: true},
	}); err != nil {
		t.Fatalf("Save after-loss: %v", err)
	}
	// The failover window is generous: the standby backoff is 2s, so poll up
	// to 10s for the re-promoted epoch to fire the after-loss schedule.
	if !pollUntil(10*time.Second, func() bool { return fire.count() > countAtLoss }) {
		t.Fatalf("scheduler never fired the after-loss schedule (no failover: demote → standby → re-acquire → resume broken); fires = %d, want > %d",
			fire.count(), countAtLoss)
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestRenewLeaderTransientFaultKeepsTicking is the renewLeader coverage test
// for the TRANSIENT-fault path: a Renew error that is NOT port.ErrLeaseHeld,
// with plenty of headroom before the held lease's expiry, must NOT stop the
// tick loop (renewLeader's "continue" branch) — only a definitive loss or an
// imminent expiry should ever call declareLeaderLost.
func TestRenewLeaderTransientFaultKeepsTicking(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}

	// A generous TTL (1h) means every transient Renew failure still has ample
	// headroom (LeaseRenewInterval, 15ms) before expiry, so renewLeader must
	// "continue" (retry) rather than declare loss.
	lease := &transientFailLease{clk: clk, ttl: time.Hour}
	s := scheduler.New(scheduler.Config{
		Store:              store,
		Lease:              lease,
		LeaseOwner:         "owner-this",
		Fire:               fire.fire,
		Clock:              clk,
		TickInterval:       25 * time.Millisecond,
		LeaseRenewInterval: 25 * time.Millisecond,
		MaxConcurrentFires: 4,
	})

	if err := store.Save(context.Background(), port.Schedule{
		Spec:  port.ScheduleSpec{Name: "cron", Prompt: "x", Trigger: port.TriggerSpec{Cron: "* * * * *"}},
		State: port.ScheduleState{NextFireAt: clk.Now(), Enabled: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// First fire — the ticker is alive.
	if !pollUntil(10*time.Second, func() bool { return fire.count() >= 1 }) {
		t.Fatal("scheduler never fired the initial due schedule")
	}
	// Let several transient-failure renew cycles accumulate — the renewer must
	// keep retrying (not stop) across all of them.
	if !pollUntil(10*time.Second, func() bool { return lease.renewCalls() >= 3 }) {
		t.Fatal("renewer never retried after a transient renew fault")
	}

	// Advance the clock so the cron is due again. If the transient fault had
	// (incorrectly) stopped the tick loop, this would never fire.
	loaded, err := store.Load(context.Background(), "cron")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	clk.advance(loaded.State.NextFireAt.Sub(clk.Now()) + time.Second)
	if !pollUntil(10*time.Second, func() bool { return fire.count() >= 2 }) {
		t.Fatal("tick loop stopped ticking after a transient renew fault (want: keep ticking, per renewLeader's headroom-retry branch)")
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestOneShotReArmOnPendingCrash: a one-shot with OneShotRetry=true whose prior
// fire crashed (LastFireSessionID == PendingFireSessionID — Claim happened but
// RecordFire never did) is re-armed up to OneShotMaxRetries by the tick loop's
// post-fire scan (maybeReArmOneShots). After a tick, the schedule is re-enabled
// (Enabled=true), NextFireAt advanced to now+backoff, and OneShotRetryCount=1.
//
// The re-arm scan runs AFTER the due-fire batch (it is a post-fire pass, not a
// separate tick), so the tick must process at least one due schedule to reach
// it. A harmless due cron ("sparker") drives the tick past the no-due-work
// early return; the sparker's own fire is incidental to the re-arm assertion.
func TestOneShotReArmOnPendingCrash(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}
	s := newTestScheduler(t, store, clk, fire)

	// A due cron that drives the tick past the no-due-work early return so the
	// post-fire re-arm scan runs. It fires (recorded) but is not under test.
	if err := store.Save(context.Background(), port.Schedule{
		Spec:  port.ScheduleSpec{Name: "sparker", Prompt: "x", Trigger: port.TriggerSpec{Cron: "* * * * *"}},
		State: port.ScheduleState{NextFireAt: clk.Now(), Enabled: true},
	}); err != nil {
		t.Fatalf("Save sparker: %v", err)
	}

	// A one-shot that was Claim'd (Enabled=false, FireCount=1) but never
	// RecordFire'd — LastFireSessionID is still the pending sentinel. This is
	// the crash-mid-fire shape (sub-case 1).
	if err := store.Save(context.Background(), port.Schedule{
		Spec: port.ScheduleSpec{
			Name:              "retry-pending",
			Prompt:            "x",
			Trigger:           port.TriggerSpec{OneShot: clk.Now()},
			OneShotRetry:      true,
			OneShotMaxRetries: 3,
		},
		State: port.ScheduleState{
			NextFireAt:        time.Time{}, // disabled (Claim zeroed it)
			Enabled:           false,
			FireCount:         1,
			LastFireSessionID: port.PendingFireSessionID,
		},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s.RunOnceForTest(context.Background())

	// The re-arm scan re-enabled the one-shot for a retry.
	loaded, err := store.Load(context.Background(), "retry-pending")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded.State.Enabled {
		t.Fatalf("Enabled = false, want true (re-armed)")
	}
	if loaded.State.OneShotRetryCount != 1 {
		t.Fatalf("OneShotRetryCount = %d, want 1 (incremented on re-arm)", loaded.State.OneShotRetryCount)
	}
	// NextFireAt is now + backoff (the re-arm set it).
	if !loaded.State.NextFireAt.After(clk.Now()) {
		t.Fatalf("NextFireAt = %v, want after now (re-arm advanced it)", loaded.State.NextFireAt)
	}
	// Only the sparker fired on this tick — the re-arm only re-enabled the
	// one-shot; the actual retry fire happens on a LATER tick once the backoff
	// elapses (Due returns it once NextFireAt <= now).
	if got := fire.count(); got != 1 {
		t.Fatalf("fires = %d, want 1 (only the sparker; the retry fire is a later tick)", got)
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestOneShotReArmNoDueWork: a quiet one-shot-only deployment (NO sparker cron,
// nothing due on the tick) must still re-arm a crashed one-shot. This is the
// gap the reviewer flagged: the re-arm scan runs BEFORE the len(due)==0 early
// return, so a tick with zero due schedules still re-arms. Without the correct
// ordering the crashed one-shot would stall indefinitely (there is no due work
// to "spark" the tick past the early return).
func TestOneShotReArmNoDueWork(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}
	s := newTestScheduler(t, store, clk, fire)

	// The ONLY schedule is a crashed one-shot — nothing is Due on this tick.
	if err := store.Save(context.Background(), port.Schedule{
		Spec: port.ScheduleSpec{
			Name:              "retry-pending",
			Prompt:            "x",
			Trigger:           port.TriggerSpec{OneShot: clk.Now()},
			OneShotRetry:      true,
			OneShotMaxRetries: 3,
		},
		State: port.ScheduleState{
			NextFireAt:        time.Time{}, // disabled (Claim zeroed it)
			Enabled:           false,
			FireCount:         1,
			LastFireSessionID: port.PendingFireSessionID,
		},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s.RunOnceForTest(context.Background())

	// The re-arm scan re-enabled the one-shot even though nothing was due.
	loaded, err := store.Load(context.Background(), "retry-pending")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded.State.Enabled {
		t.Fatalf("Enabled = false, want true (re-armed on a no-due-work tick)")
	}
	if loaded.State.OneShotRetryCount != 1 {
		t.Fatalf("OneShotRetryCount = %d, want 1 (incremented on re-arm)", loaded.State.OneShotRetryCount)
	}
	if !loaded.State.NextFireAt.After(clk.Now()) {
		t.Fatalf("NextFireAt = %v, want after now (re-arm advanced it)", loaded.State.NextFireAt)
	}
	// Nothing fired — the re-arm only re-enabled; the retry fire is a later tick.
	if got := fire.count(); got != 0 {
		t.Fatalf("fires = %d, want 0 (nothing due; re-arm only re-enables)", got)
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestOneShotReArmOnStopError: a one-shot with OneShotRetry=true whose prior
// fire ended in StopError (RecordFire recorded Stop==StopError) is re-armed.
// This is crash sub-case 2: the fire ran but ended in error.
func TestOneShotReArmOnStopError(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}
	s := newTestScheduler(t, store, clk, fire)

	const fireID = "sched--retry-err-1"
	// A due cron that drives the tick past the no-due-work early return so the
	// post-fire re-arm scan runs.
	if err := store.Save(context.Background(), port.Schedule{
		Spec:  port.ScheduleSpec{Name: "sparker", Prompt: "x", Trigger: port.TriggerSpec{Cron: "* * * * *"}},
		State: port.ScheduleState{NextFireAt: clk.Now(), Enabled: true},
	}); err != nil {
		t.Fatalf("Save sparker: %v", err)
	}
	// Save the schedule first (RecordFire looks it up by ScheduleName).
	if err := store.Save(context.Background(), port.Schedule{
		Spec: port.ScheduleSpec{
			Name:              "retry-err",
			Prompt:            "x",
			Trigger:           port.TriggerSpec{OneShot: clk.Now()},
			OneShotRetry:      true,
			OneShotMaxRetries: 3,
		},
		State: port.ScheduleState{
			NextFireAt:        time.Time{},
			Enabled:           false,
			FireCount:         1,
			LastFireSessionID: session.SessionID(fireID),
		},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Record a prior fire that ended in StopError under the LastFireSessionID
	// the state points at, so LoadFire returns Stop==StopError.
	if err := store.RecordFire(context.Background(), port.ScheduleFire{
		ID:           fireID,
		ScheduleName: "retry-err",
		SessionID:    session.SessionID(fireID),
		FiredAt:      clk.Now(),
		Stop:         session.StopError,
		Err:          "boom",
	}); err != nil {
		t.Fatalf("RecordFire prior: %v", err)
	}

	s.RunOnceForTest(context.Background())

	loaded, err := store.Load(context.Background(), "retry-err")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded.State.Enabled {
		t.Fatalf("Enabled = false, want true (re-armed after StopError)")
	}
	if loaded.State.OneShotRetryCount != 1 {
		t.Fatalf("OneShotRetryCount = %d, want 1", loaded.State.OneShotRetryCount)
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestOneShotNoRetryNotReArmed: a one-shot with OneShotRetry=false (the default,
// at-most-once) is NOT re-armed, even if its prior fire crashed (pending). This
// pins the pre-Phase-2 behavior is unchanged for a non-opted-in one-shot.
func TestOneShotNoRetryNotReArmed(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}
	s := newTestScheduler(t, store, clk, fire)

	// A due cron that drives the tick past the no-due-work early return so the
	// post-fire re-arm scan runs (and proves it does NOT re-arm this one-shot).
	if err := store.Save(context.Background(), port.Schedule{
		Spec:  port.ScheduleSpec{Name: "sparker", Prompt: "x", Trigger: port.TriggerSpec{Cron: "* * * * *"}},
		State: port.ScheduleState{NextFireAt: clk.Now(), Enabled: true},
	}); err != nil {
		t.Fatalf("Save sparker: %v", err)
	}

	if err := store.Save(context.Background(), port.Schedule{
		Spec: port.ScheduleSpec{
			Name:         "noretry",
			Prompt:       "x",
			Trigger:      port.TriggerSpec{OneShot: clk.Now()},
			OneShotRetry: false, // at-most-once (default)
		},
		State: port.ScheduleState{
			NextFireAt:        time.Time{},
			Enabled:           false,
			FireCount:         1,
			LastFireSessionID: port.PendingFireSessionID, // crashed mid-fire
		},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s.RunOnceForTest(context.Background())

	loaded, err := store.Load(context.Background(), "noretry")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.State.Enabled {
		t.Fatalf("Enabled = true, want false (OneShotRetry=false: NOT re-armed)")
	}
	if loaded.State.OneShotRetryCount != 0 {
		t.Fatalf("OneShotRetryCount = %d, want 0 (not re-armed)", loaded.State.OneShotRetryCount)
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestOneShotReArmBudgetExhausted: beyond OneShotMaxRetries, the one-shot stays
// disabled (NOT re-armed). The retry-budget gate (OneShotRetryCount >=
// OneShotMaxRetries) prevents a permanent crash-loop.
func TestOneShotReArmBudgetExhausted(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}
	s := newTestScheduler(t, store, clk, fire)

	// A due cron that drives the tick past the no-due-work early return so the
	// post-fire re-arm scan runs (and proves the budget gate fires).
	if err := store.Save(context.Background(), port.Schedule{
		Spec:  port.ScheduleSpec{Name: "sparker", Prompt: "x", Trigger: port.TriggerSpec{Cron: "* * * * *"}},
		State: port.ScheduleState{NextFireAt: clk.Now(), Enabled: true},
	}); err != nil {
		t.Fatalf("Save sparker: %v", err)
	}

	// A crashed one-shot already at its retry budget (3 >= 3).
	if err := store.Save(context.Background(), port.Schedule{
		Spec: port.ScheduleSpec{
			Name:              "exhausted",
			Prompt:            "x",
			Trigger:           port.TriggerSpec{OneShot: clk.Now()},
			OneShotRetry:      true,
			OneShotMaxRetries: 3,
		},
		State: port.ScheduleState{
			NextFireAt:        time.Time{},
			Enabled:           false,
			FireCount:         1,
			LastFireSessionID: port.PendingFireSessionID,
			OneShotRetryCount: 3, // budget exhausted
		},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s.RunOnceForTest(context.Background())

	loaded, err := store.Load(context.Background(), "exhausted")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.State.Enabled {
		t.Fatalf("Enabled = true, want false (retry budget exhausted — permanently done)")
	}
	if loaded.State.OneShotRetryCount != 3 {
		t.Fatalf("OneShotRetryCount = %d, want 3 (budget NOT incremented past the limit)", loaded.State.OneShotRetryCount)
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestOneShotReArmStoreWithoutInterface: a store that does NOT implement
// ScheduleOneShotReArmer degrades gracefully — the re-arm scan is a nil-safe
// type-assertion no-op (no panic, no re-arm). This pins the byte-identical
// pre-Phase-2 fallback for a store that opted out of the re-arm seam.
func TestOneShotReArmStoreWithoutInterface(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	// Wrap memschedulestore so the optional ScheduleOneShotReArmer method is
	// NOT forwarded — the type assertion in maybeReArmOneShots yields ok=false.
	store := &noReArmStore{ScheduleStore: memschedulestore.New()}
	fire := &fireStub{}
	s := newTestScheduler(t, store, clk, fire)

	// A due cron that drives the tick past the no-due-work early return so the
	// post-fire re-arm scan runs (and proves it degrades to a no-op).
	if err := store.Save(context.Background(), port.Schedule{
		Spec:  port.ScheduleSpec{Name: "sparker", Prompt: "x", Trigger: port.TriggerSpec{Cron: "* * * * *"}},
		State: port.ScheduleState{NextFireAt: clk.Now(), Enabled: true},
	}); err != nil {
		t.Fatalf("Save sparker: %v", err)
	}

	// Sanity: the wrapped store really does NOT satisfy the re-arm interface.
	if _, ok := any(store).(port.ScheduleOneShotReArmer); ok {
		t.Fatalf("noReArmStore unexpectedly satisfies ScheduleOneShotReArmer — test fixture is broken")
	}

	if err := store.Save(context.Background(), port.Schedule{
		Spec: port.ScheduleSpec{
			Name:              "norearmer",
			Prompt:            "x",
			Trigger:           port.TriggerSpec{OneShot: clk.Now()},
			OneShotRetry:      true,
			OneShotMaxRetries: 3,
		},
		State: port.ScheduleState{
			NextFireAt:        time.Time{},
			Enabled:           false,
			FireCount:         1,
			LastFireSessionID: port.PendingFireSessionID,
		},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// RunOnceForTest must NOT panic and must NOT re-arm (the scan is skipped).
	s.RunOnceForTest(context.Background())

	loaded, err := store.Load(context.Background(), "norearmer")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.State.Enabled {
		t.Fatalf("Enabled = true, want false (no re-arm — store lacks the seam)")
	}
	if loaded.State.OneShotRetryCount != 0 {
		t.Fatalf("OneShotRetryCount = %d, want 0 (no re-arm)", loaded.State.OneShotRetryCount)
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

// pollUntil polls f every 5ms until it returns true or the deadline elapses
// (then evaluates f once more), for driving a REAL ticker-backed goroutine
// (Start, not RunOnceForTest) to a deterministic checkpoint without a fixed
// sleep. Mirrors the `eventually` helper used elsewhere in this repo (e.g.
// internal/app/scheduler_fire_test.go).
func pollUntil(deadline time.Duration, f func() bool) bool {
	deadlineAt := time.Now().Add(deadline)
	for time.Now().Before(deadlineAt) {
		if f() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return f()
}

// capturingDiag is a port.Diagnostics that records every log message, so a
// test can assert an internal event (like declareLeaderLost's WARN) fired
// without a exported hook into the scheduler's private state.
type capturingDiag struct {
	mu   sync.Mutex
	msgs []string
}

func (d *capturingDiag) Log(_ context.Context, _ port.Level, msg string, _ ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.msgs = append(d.msgs, msg)
}

func (d *capturingDiag) With(...any) port.Diagnostics { return d }

// sawLostLease reports whether declareLeaderLost's WARN has been recorded.
func (d *capturingDiag) sawLostLease() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, m := range d.msgs {
		if strings.Contains(m, "lost scheduler leader lease") {
			return true
		}
	}
	return false
}

// renewFailLease is a port.SessionLease that grants Acquire once per id (to
// whichever owner asks first) and then returns port.ErrLeaseHeld from EVERY
// Renew call — a DEFINITIVE leader-lease loss on the first renew, modelling a
// peer having taken over.
type renewFailLease struct {
	mu     sync.Mutex
	holder string
}

func (l *renewFailLease) Acquire(_ context.Context, id session.SessionID, owner string) (port.Lease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holder != "" && l.holder != owner {
		return port.Lease{}, port.ErrLeaseHeld
	}
	l.holder = owner
	return port.Lease{SessionID: id, Owner: owner, Token: 1, Expiry: time.Now().Add(time.Hour)}, nil
}

func (*renewFailLease) Renew(_ context.Context, _ port.Lease) (port.Lease, error) {
	return port.Lease{}, port.ErrLeaseHeld
}

func (*renewFailLease) Release(_ context.Context, _ port.Lease) error { return nil }

// transientFailLease is a port.SessionLease that grants Acquire once (with an
// expiry ttl past the fake clock's current time) and then returns a NON-
// ErrLeaseHeld ("transient infra fault") error from every Renew call — the
// renewLeader "keep the lease unless within one renew interval of expiry"
// retry branch, as long as the caller keeps ttl generous relative to how far
// the test advances the fake clock.
type transientFailLease struct {
	mu         sync.Mutex
	holder     string
	expiry     time.Time
	clk        port.Clock
	ttl        time.Duration
	renewCount int
}

func (l *transientFailLease) Acquire(_ context.Context, id session.SessionID, owner string) (port.Lease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holder != "" && l.holder != owner {
		return port.Lease{}, port.ErrLeaseHeld
	}
	l.holder = owner
	l.expiry = l.clk.Now().Add(l.ttl)
	return port.Lease{SessionID: id, Owner: owner, Token: 1, Expiry: l.expiry}, nil
}

func (l *transientFailLease) Renew(_ context.Context, _ port.Lease) (port.Lease, error) {
	l.mu.Lock()
	l.renewCount++
	l.mu.Unlock()
	return port.Lease{}, errors.New("transient infra fault")
}

func (*transientFailLease) Release(_ context.Context, _ port.Lease) error { return nil }

func (l *transientFailLease) renewCalls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.renewCount
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

// metricCall records one ScheduleMetrics callback invocation.
type metricCall struct {
	payload  session.SchedulePayload
	duration time.Duration
}

// TestScheduleMetricsFiredFailedSkipped asserts the ScheduleMetrics callback is
// invoked with the correct payload (Kind) and duration on the fired, failed,
// and skipped (misfire) paths:
//   - fired/failed: duration > 0 (Claim→terminal), recorded from fireClaimed;
//   - skipped (misfire): duration == 0 (no run), recorded from the fireOne
//     skip path.
//
// It uses the fakeClock: a custom FireFunc advances the clock during the fire so
// the recorded duration is deterministically non-zero.
func TestScheduleMetricsFiredFailedSkipped(t *testing.T) {
	defer goleak.VerifyNone(t)

	// --- fired path ---
	t.Run("fired", func(t *testing.T) {
		defer goleak.VerifyNone(t)
		clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
		store := memschedulestore.New()
		fire := &fireStub{}

		var mu sync.Mutex
		var calls []metricCall
		metrics := func(p session.SchedulePayload, d time.Duration) {
			mu.Lock()
			calls = append(calls, metricCall{payload: p, duration: d})
			mu.Unlock()
		}
		s := scheduler.New(scheduler.Config{
			Store:              store,
			Fire:               fire.fire,
			Clock:              clk,
			TickInterval:       1 * time.Hour,
			MaxConcurrentFires: 4,
			ScheduleMetrics:    metrics,
		})
		// A custom fire that advances the clock mid-fire so the recorded
		// Claim→terminal duration is deterministically non-zero.
		wrappedFire := func(ctx context.Context, sched port.Schedule, now time.Time) (port.ScheduleFire, error) {
			clk.advance(2 * time.Second)
			return fire.fire(ctx, sched, now)
		}
		s.SetFire(wrappedFire)

		if err := store.Save(context.Background(), port.Schedule{
			Spec:  port.ScheduleSpec{Name: "m-fired", Prompt: "x", Trigger: port.TriggerSpec{Cron: "* * * * *"}},
			State: port.ScheduleState{NextFireAt: clk.Now(), Enabled: true},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		s.RunOnceForTest(context.Background())

		mu.Lock()
		defer mu.Unlock()
		if len(calls) != 1 {
			t.Fatalf("metrics calls = %d, want 1: %+v", len(calls), calls)
		}
		if calls[0].payload.Kind != "fired" {
			t.Errorf("kind = %q, want fired", calls[0].payload.Kind)
		}
		if calls[0].payload.ScheduleName != "m-fired" {
			t.Errorf("schedule = %q, want m-fired", calls[0].payload.ScheduleName)
		}
		if calls[0].duration <= 0 {
			t.Errorf("fired duration = %v, want > 0", calls[0].duration)
		}
		if calls[0].duration != 2*time.Second {
			t.Errorf("fired duration = %v, want 2s (clock advanced 2s during fire)", calls[0].duration)
		}
		if err := s.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	})

	// --- failed path ---
	t.Run("failed", func(t *testing.T) {
		defer goleak.VerifyNone(t)
		clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
		store := memschedulestore.New()
		fire := &fireStub{err: errors.New("boom")}

		var mu sync.Mutex
		var calls []metricCall
		metrics := func(p session.SchedulePayload, d time.Duration) {
			mu.Lock()
			calls = append(calls, metricCall{payload: p, duration: d})
			mu.Unlock()
		}
		s := scheduler.New(scheduler.Config{
			Store:              store,
			Fire:               fire.fire,
			Clock:              clk,
			TickInterval:       1 * time.Hour,
			MaxConcurrentFires: 4,
			ScheduleMetrics:    metrics,
		})
		wrappedFire := func(ctx context.Context, sched port.Schedule, now time.Time) (port.ScheduleFire, error) {
			clk.advance(3 * time.Second)
			return fire.fire(ctx, sched, now)
		}
		s.SetFire(wrappedFire)

		if err := store.Save(context.Background(), port.Schedule{
			Spec:  port.ScheduleSpec{Name: "m-failed", Prompt: "x", Trigger: port.TriggerSpec{Cron: "* * * * *"}},
			State: port.ScheduleState{NextFireAt: clk.Now(), Enabled: true},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		s.RunOnceForTest(context.Background())

		mu.Lock()
		defer mu.Unlock()
		if len(calls) != 1 {
			t.Fatalf("metrics calls = %d, want 1: %+v", len(calls), calls)
		}
		if calls[0].payload.Kind != "failed" {
			t.Errorf("kind = %q, want failed", calls[0].payload.Kind)
		}
		if calls[0].duration != 3*time.Second {
			t.Errorf("failed duration = %v, want 3s (clock advanced 3s during fire)", calls[0].duration)
		}
		if err := s.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	})

	// --- skipped (misfire) path ---
	t.Run("skipped", func(t *testing.T) {
		defer goleak.VerifyNone(t)
		clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
		store := memschedulestore.New()
		fire := &fireStub{}

		var mu sync.Mutex
		var calls []metricCall
		metrics := func(p session.SchedulePayload, d time.Duration) {
			mu.Lock()
			calls = append(calls, metricCall{payload: p, duration: d})
			mu.Unlock()
		}
		s := scheduler.New(scheduler.Config{
			Store:              store,
			Fire:               fire.fire,
			Clock:              clk,
			TickInterval:       1 * time.Hour,
			MaxConcurrentFires: 4,
			ScheduleMetrics:    metrics,
		})
		// MisfireSkip + a slot missed beyond the grace window (1h tick) → skipped.
		past := clk.Now().Add(-2 * time.Hour)
		if err := store.Save(context.Background(), port.Schedule{
			Spec:  port.ScheduleSpec{Name: "m-skip", Prompt: "x", Trigger: port.TriggerSpec{Cron: "* * * * *"}, Misfire: port.MisfireSkip},
			State: port.ScheduleState{NextFireAt: past, Enabled: true},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		s.RunOnceForTest(context.Background())

		mu.Lock()
		defer mu.Unlock()
		if len(calls) != 1 {
			t.Fatalf("metrics calls = %d, want 1: %+v", len(calls), calls)
		}
		if calls[0].payload.Kind != "skipped" {
			t.Errorf("kind = %q, want skipped", calls[0].payload.Kind)
		}
		if calls[0].duration != 0 {
			t.Errorf("skipped duration = %v, want 0 (no run)", calls[0].duration)
		}
		if calls[0].payload.ScheduleName != "m-skip" {
			t.Errorf("schedule = %q, want m-skip", calls[0].payload.ScheduleName)
		}
		if err := s.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	})

	// --- skipped (singleton overlap) path ---
	// L3: the only other skipped-path metric emit (the overlap branch in fireOne,
	// distinct from the misfire-skip branch above) was untested. Mirrors
	// TestSingletonOverlapPreservesLivePointer's held-lease setup.
	t.Run("skipped-singleton-overlap", func(t *testing.T) {
		defer goleak.VerifyNone(t)
		clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
		store := memschedulestore.New()
		fire := &fireStub{}

		leaseBE := &heldSessionLease{held: map[session.SessionID]string{"sched--prior": "owner-prior"}, clk: clk}

		var mu sync.Mutex
		var calls []metricCall
		metrics := func(p session.SchedulePayload, d time.Duration) {
			mu.Lock()
			calls = append(calls, metricCall{payload: p, duration: d})
			mu.Unlock()
		}
		s := scheduler.New(scheduler.Config{
			Store:              store,
			Lease:              leaseBE,
			LeaseOwner:         "owner-this",
			Fire:               fire.fire,
			Clock:              clk,
			TickInterval:       1 * time.Hour,
			MaxConcurrentFires: 4,
			ScheduleMetrics:    metrics,
		})

		if err := store.Save(context.Background(), port.Schedule{
			Spec: port.ScheduleSpec{
				Name:      "m-overlap",
				Prompt:    "x",
				Trigger:   port.TriggerSpec{Cron: "* * * * *"},
				Singleton: true,
			},
			State: port.ScheduleState{
				NextFireAt:        clk.Now(),
				Enabled:           true,
				LastFireSessionID: "sched--prior", // a prior fire is still running
			},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		s.RunOnceForTest(context.Background())

		mu.Lock()
		defer mu.Unlock()
		if len(calls) != 1 {
			t.Fatalf("metrics calls = %d, want 1: %+v", len(calls), calls)
		}
		if calls[0].payload.Kind != "skipped" {
			t.Errorf("kind = %q, want skipped", calls[0].payload.Kind)
		}
		if calls[0].duration != 0 {
			t.Errorf("overlap-skip duration = %v, want 0 (no run)", calls[0].duration)
		}
		if calls[0].payload.ScheduleName != "m-overlap" {
			t.Errorf("schedule = %q, want m-overlap", calls[0].payload.ScheduleName)
		}
		if err := s.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	})
}

// --- standby-hardening fakes (the architect-review fixes) --------------------

// unsupportedDueStore is a memschedulestore whose Due always returns
// port.ErrScheduleUnsupported — the "this backend can never store schedules"
// case the tick loop must stickily disable on.
type unsupportedDueStore struct {
	*memschedulestore.Store
}

func (unsupportedDueStore) Due(context.Context, time.Time) ([]port.Schedule, error) {
	return nil, port.ErrScheduleUnsupported
}

// recordingLease is a port.SessionLease that records the ORDER of Acquire and
// Release calls relative to a shared probe, so a test can assert the demote
// (epoch cancel) precedes the lease release. Acquire always succeeds.
type recordingLease struct {
	mu      sync.Mutex
	events  []string
	onFirst func() // invoked synchronously inside the first Release call
}

func (l *recordingLease) Acquire(_ context.Context, id session.SessionID, owner string) (port.Lease, error) {
	l.mu.Lock()
	l.events = append(l.events, "acquire")
	l.mu.Unlock()
	return port.Lease{SessionID: id, Owner: owner, Token: 1, Expiry: time.Now().Add(time.Hour)}, nil
}

func (*recordingLease) Renew(_ context.Context, l port.Lease) (port.Lease, error) { return l, nil }

func (l *recordingLease) Release(_ context.Context, _ port.Lease) error {
	l.mu.Lock()
	l.events = append(l.events, "release")
	first := len(l.events) == 1 || !l.sawAcquireBeforeReleaseLocked()
	cb := l.onFirst
	l.mu.Unlock()
	if first && cb != nil {
		cb()
	}
	return nil
}

func (l *recordingLease) sawAcquireBeforeReleaseLocked() bool {
	for _, e := range l.events {
		if e == "release" {
			return false
		}
	}
	return true
}

// --- Fix 2: ErrScheduleUnsupported is sticky across the epoch lifecycle -----

// TestStickyUnsupportedStopsLeadershipLoop: when the store's Due reports
// ErrScheduleUnsupported, the scheduler must not churn a fresh leadership epoch
// every TickInterval. Before the fix, the tick loop cancelled only the current
// epoch ctx; the leadership loop looped back to acquireLeader and started a new
// epoch that failed Due again — an infinite acquire/tick/release cycle. The
// stickyUnsupported flag must stop the loop.
func TestStickyUnsupportedStopsLeadershipLoop(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := unsupportedDueStore{memschedulestore.New()}
	fire := &fireStub{}
	diag := &capturingDiag{}
	lease := &recordingLease{}
	s := scheduler.New(scheduler.Config{
		Store:        store,
		Lease:        lease,
		LeaseOwner:   "owner-this",
		Fire:         fire.fire,
		Clock:        clk,
		Diagnostics:  diag,
		TickInterval: 10 * time.Millisecond,
	})

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// The first epoch's first Due returns Unsupported → the tick loop sets the
	// sticky flag and cancels; the leadership loop must then EXIT (no re-acquire
	// churn). Wait for the sticky stop to take effect, then confirm the acquire
	// count stops growing (a churning loop would keep acquiring every epoch).
	if !pollUntil(5*time.Second, func() bool {
		diag.mu.Lock()
		defer diag.mu.Unlock()
		for _, m := range diag.msgs {
			if strings.Contains(m, "schedule store unsupported") {
				return true
			}
		}
		return false
	}) {
		t.Fatal("tick loop never logged the store-unsupported stop")
	}
	lease.mu.Lock()
	acquiresAtSticky := 0
	for _, e := range lease.events {
		if e == "acquire" {
			acquiresAtSticky++
		}
	}
	lease.mu.Unlock()
	// Give a churning loop ample time to re-acquire several times.
	time.Sleep(200 * time.Millisecond)
	lease.mu.Lock()
	acquiresAfter := 0
	for _, e := range lease.events {
		if e == "acquire" {
			acquiresAfter++
		}
	}
	lease.mu.Unlock()
	if acquiresAfter != acquiresAtSticky {
		t.Fatalf("leadership loop kept acquiring after sticky-unsupported (acquires %d → %d); the sticky disable did not stop the epoch churn",
			acquiresAtSticky, acquiresAfter)
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// --- Fix 1: releaseLeader demotes BEFORE releasing the lease -----------------

// TestReleaseLeaderDemotesBeforeRelease: on a clean Stop, the scheduler must
// cancel the leadership epoch (so no new fires start) BEFORE it releases the
// leader lease — otherwise a standby could promote and tick while the outgoing
// leader's epoch was still firing. The recordingLease fires a probe inside the
// Release call; at that instant the scheduler must already report not-leader.
func TestReleaseLeaderDemotesBeforeRelease(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}
	var s2 *scheduler.Scheduler
	lease := &recordingLease{
		onFirst: func() {
			// Inside the Release call: the scheduler must ALREADY be demoted.
			if _, leader := s2.LeaderOwner(); leader {
				t.Errorf("releaseLeader released the lease while still leader (demote-before-release violated)")
			}
		},
	}
	s2 = scheduler.New(scheduler.Config{
		Store:        store,
		Lease:        lease,
		LeaseOwner:   "owner-this",
		Fire:         fire.fire,
		Clock:        clk,
		TickInterval: 10 * time.Millisecond,
	})
	if err := s2.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Wait for leadership to be acquired (the probe only makes sense once leader).
	if !pollUntil(5*time.Second, func() bool {
		_, leader := s2.LeaderOwner()
		return leader
	}) {
		t.Fatal("scheduler never became leader")
	}
	if err := s2.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// The onFirst probe asserted the ordering synchronously; reaching here with
	// no t.Errorf means demote preceded release.
}

// --- Fix 1 belt-and-braces: a demoted (standby) replica's tickOnce no-fires --

// TestTickOnceNoFireWhenNotLeader: a lease-backed scheduler that is NOT the
// leader must not poll Due / fire from tickOnce, even if a tick is in flight
// past the epoch-ctx cancel (the belt-and-braces leader gate).
func TestTickOnceNoFireWhenNotLeader(t *testing.T) {
	defer goleak.VerifyNone(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	fire := &fireStub{}
	leaseBE := memleaseHeldBy(clk, "owner-A") // a peer holds the leader lease
	s := scheduler.New(scheduler.Config{
		Store:        store,
		Lease:        leaseBE,
		LeaseOwner:   "owner-B",
		Fire:         fire.fire,
		Clock:        clk,
		TickInterval: time.Hour,
	})
	if err := store.Save(context.Background(), port.Schedule{
		Spec:  port.ScheduleSpec{Name: "due", Prompt: "x", Trigger: port.TriggerSpec{Cron: "* * * * *"}},
		State: port.ScheduleState{NextFireAt: clk.Now(), Enabled: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Start → standby (peer leads). A direct RunOnceForTest drives tickOnce; the
	// leader gate must no-op it (a standby never polls Due).
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	s.RunOnceForTest(context.Background())
	if got := fire.count(); got != 0 {
		t.Fatalf("fires = %d, want 0 (standby tickOnce must not fire)", got)
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
