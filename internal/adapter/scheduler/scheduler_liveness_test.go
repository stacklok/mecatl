package scheduler

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/stacklok/mecatl/engine/adapter/memlease"
	"github.com/stacklok/mecatl/engine/adapter/memschedulestore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

type livenessDiagnostics struct{ messages chan string }

func (d *livenessDiagnostics) With(...any) port.Diagnostics { return d }
func (d *livenessDiagnostics) Log(_ context.Context, _ port.Level, message string, _ ...any) {
	select {
	case d.messages <- message:
	default:
	}
}

type unknownFireLease struct{ port.SessionLease }

func (unknownFireLease) Acquire(context.Context, session.SessionID, string) (port.Lease, error) {
	return port.Lease{}, errors.New("lease backend unavailable")
}

func TestReplicaUnknownOwnershipFailsClosed(t *testing.T) {
	for _, state := range []string{"pending", "inflight", "probe-error"} {
		t.Run(state, func(t *testing.T) {
			t.Cleanup(func() { goleak.VerifyNone(t) })
			ctx := context.Background()
			clock := &livenessClock{now: time.Unix(1700000000, 0)}
			store := memschedulestore.New()
			prior := session.SessionID("sched--prior")
			started := clock.Now().Add(-time.Hour)
			if state == "pending" {
				prior = port.PendingFireSessionID
				started = time.Time{}
			}
			if state == "probe-error" {
				started = time.Time{}
			}
			if err := store.Save(ctx, port.Schedule{
				Spec:  port.ScheduleSpec{Name: "replica", Trigger: port.TriggerSpec{Cron: "* * * * *"}, Singleton: true},
				State: port.ScheduleState{Enabled: true, NextFireAt: clock.Now(), LastFireAt: clock.Now().Add(-time.Hour), LastFireSessionID: prior, LastFireStartedAt: started},
			}); err != nil {
				t.Fatal(err)
			}
			s := New(Config{Store: store, Clock: clock, Lease: unknownFireLease{},
				Fire: func(context.Context, port.Schedule, time.Time) (port.ScheduleFire, error) {
					t.Error("fired with unknown prior ownership")
					return port.ScheduleFire{}, nil
				},
				ReconcileStaleFire: func(context.Context, port.Schedule) { t.Error("settled unknown prior ownership") },
			})
			s.RunOnceForTest(ctx)
			if _, err := s.FireNow(ctx, "replica", clock.Now()); !errors.Is(err, ErrFireNowOverlap) {
				t.Fatalf("manual ownership guard: %v", err)
			}
			loaded, err := store.Load(ctx, "replica")
			if err != nil || loaded.State.FireCount != 0 || loaded.State.LastFireSessionID != prior {
				t.Fatalf("claim modified quarantine: %+v, %v", loaded, err)
			}
			if err := s.Stop(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type livenessClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *livenessClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *livenessClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func awaitLiveness(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not reach gate")
	}
}

func TestPollingPastStuckFireRetainsCapacityAndName(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "started"}[started], func(t *testing.T) {
			t.Cleanup(func() { goleak.VerifyNone(t) })
			ctx := context.Background()
			clock := &livenessClock{now: time.Unix(1700000000, 0)}
			store := memschedulestore.New()
			entered, release, unrelated := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			s := New(Config{
				Store: store, Clock: clock, MaxConcurrentFires: 2,
				ReconcileStaleFire: func(context.Context, port.Schedule) { t.Error("reconciled active fire") },
				Fire: func(ctx context.Context, sched port.Schedule, now time.Time) (port.ScheduleFire, error) {
					fire := port.ScheduleFire{ID: "fire-" + sched.Spec.Name, ScheduleName: sched.Spec.Name, SessionID: session.SessionID("sched--" + sched.Spec.Name), FiredAt: now, Stop: session.StopTimeout}
					if sched.Spec.Name == "stuck" {
						if started {
							inflight := fire
							inflight.Stop = ""
							inflight.StartedAt = now
							inflight.Deadline = now.Add(time.Second)
							if err := store.RecordFireStart(ctx, sched.Spec.Name, inflight); err != nil {
								t.Error(err)
							}
						}
						close(entered)
						<-release // deliberately ignores cancellation
					} else {
						close(unrelated)
					}
					return fire, nil
				},
			})
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }); _ = s.Stop() })
			if err := store.Save(ctx, port.Schedule{
				Spec:  port.ScheduleSpec{Name: "stuck", Trigger: port.TriggerSpec{OneShot: clock.Now()}, OneShotRetry: true, OneShotMaxRetries: 3},
				State: port.ScheduleState{Enabled: true, NextFireAt: clock.Now()},
			}); err != nil {
				t.Fatal(err)
			}
			polled := make(chan struct{})
			go func() { s.tickOnce(ctx); close(polled) }()
			awaitLiveness(t, entered)
			awaitLiveness(t, polled)
			clock.advance(time.Hour)
			if err := store.Save(ctx, port.Schedule{
				Spec:  port.ScheduleSpec{Name: "new", Trigger: port.TriggerSpec{OneShot: clock.Now()}},
				State: port.ScheduleState{Enabled: true, NextFireAt: clock.Now()},
			}); err != nil {
				t.Fatal(err)
			}
			s.tickOnce(ctx)
			awaitLiveness(t, unrelated)
			if _, err := s.FireNow(ctx, "stuck", clock.Now()); !errors.Is(err, ErrFireNowOverlap) {
				t.Fatalf("manual overlap: %v", err)
			}
			loaded, err := store.Load(ctx, "stuck")
			if err != nil {
				t.Fatal(err)
			}
			if loaded.State.Enabled || loaded.State.OneShotRetryCount != 0 || loaded.State.FireCount != 1 {
				t.Fatalf("live claim changed: %+v", loaded.State)
			}
			if started {
				fire, err := store.LoadFire(ctx, "fire-stuck")
				if err != nil || fire.Stop != "" || !fire.Deadline.Before(clock.Now()) {
					t.Fatalf("degraded fire not retained: %+v, %v", fire, err)
				}
			}
			releaseOnce.Do(func() { close(release) })
			s.firesWG.Wait()
			if err := s.Stop(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLifetimeCapacityIncludesManualAndBlockedDelivery(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t) })
	ctx := context.Background()
	clock := &livenessClock{now: time.Unix(1700000000, 0)}
	store := memschedulestore.New()
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	diag := &livenessDiagnostics{messages: make(chan string, 8)}
	s := New(Config{
		Store: store, Clock: clock, MaxConcurrentFires: 1, Diagnostics: diag,
		Fire: func(_ context.Context, sched port.Schedule, now time.Time) (port.ScheduleFire, error) {
			return port.ScheduleFire{ID: "fire-" + sched.Spec.Name, ScheduleName: sched.Spec.Name, FiredAt: now, Stop: session.StopEndTurn}, nil
		},
		DeliverFireResult: func(context.Context, port.Schedule, port.ScheduleFire) { close(entered); <-release },
	})
	t.Cleanup(func() { once.Do(func() { close(release) }); awaitLiveness(t, finished); _ = s.Stop() })
	for _, name := range []string{"manual", "due"} {
		if err := store.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{Name: name, Trigger: port.TriggerSpec{Cron: "* * * * *"}}, State: port.ScheduleState{Enabled: true, NextFireAt: clock.Now()}}); err != nil {
			t.Fatal(err)
		}
	}
	go func() { defer close(finished); _, _ = s.FireNow(ctx, "manual", clock.Now()) }()
	awaitLiveness(t, entered)
	s.tickOnce(ctx)
	if _, err := s.FireNow(ctx, "due", clock.Now()); err == nil {
		t.Fatal("manual fire bypassed lifetime capacity")
	}
	select {
	case message := <-diag.messages:
		if message != "scheduler: fire capacity exhausted; polling continues" {
			t.Fatalf("capacity diagnostic: %q", message)
		}
	default:
		t.Fatal("exhausted capacity was silent")
	}
	loaded, err := store.Load(ctx, "due")
	if err != nil || loaded.State.FireCount != 0 {
		t.Fatalf("capacity-deferred slot claimed: %+v, %v", loaded, err)
	}
	fire, err := store.LoadFire(ctx, "fire-manual")
	if err != nil || fire.Stop != session.StopEndTurn {
		t.Fatalf("delivery blocked durable terminal: %+v, %v", fire, err)
	}
	once.Do(func() { close(release) })
	awaitLiveness(t, finished)
}

func TestLifetimeCapacityAcrossPollsAndBlockedReconciliation(t *testing.T) {
	for _, reconcile := range []bool{false, true} {
		t.Run(map[bool]string{false: "overdue-fire", true: "reconciliation"}[reconcile], func(t *testing.T) {
			t.Cleanup(func() { goleak.VerifyNone(t) })
			ctx := context.Background()
			clock := &livenessClock{now: time.Unix(1700000000, 0)}
			store := memschedulestore.New()
			first, second, third := make(chan struct{}), make(chan struct{}), make(chan struct{})
			releaseFirst, releaseSecond := make(chan struct{}), make(chan struct{})
			var firstOnce, secondOnce sync.Once
			var active, high atomic.Int32
			enter := func() {
				n := active.Add(1)
				for old := high.Load(); n > old; old = high.Load() {
					if high.CompareAndSwap(old, n) {
						break
					}
				}
			}
			s := New(Config{Store: store, Clock: clock, MaxConcurrentFires: 2,
				Fire: func(_ context.Context, sched port.Schedule, now time.Time) (port.ScheduleFire, error) {
					enter()
					defer active.Add(-1)
					switch sched.Spec.Name {
					case "first":
						close(first)
						<-releaseFirst
					case "second":
						close(second)
						<-releaseSecond
					case "third":
						close(third)
					}
					return port.ScheduleFire{ID: "fire-" + sched.Spec.Name, ScheduleName: sched.Spec.Name, FiredAt: now, Stop: session.StopEndTurn}, nil
				},
				ReconcileStaleFire: func(context.Context, port.Schedule) {
					enter()
					defer active.Add(-1)
					close(first)
					<-releaseFirst
				},
			})
			t.Cleanup(func() {
				firstOnce.Do(func() { close(releaseFirst) })
				secondOnce.Do(func() { close(releaseSecond) })
				_ = s.Stop()
			})
			state := port.ScheduleState{Enabled: true, NextFireAt: clock.Now()}
			if reconcile {
				state = port.ScheduleState{LastFireSessionID: port.PendingFireSessionID, LastFireAt: clock.Now().Add(-time.Hour)}
			}
			if err := store.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{Name: "first", FireTimeout: time.Second, Trigger: port.TriggerSpec{OneShot: clock.Now()}}, State: state}); err != nil {
				t.Fatal(err)
			}
			poll := func() *sync.WaitGroup {
				done := make(chan struct{})
				var batch *sync.WaitGroup
				go func() { batch = s.tickOnce(ctx); close(done) }()
				awaitLiveness(t, done)
				return batch
			}
			firstBatch := poll()
			awaitLiveness(t, first)
			clock.advance(time.Hour)
			for _, name := range []string{"second", "third"} {
				if err := store.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{Name: name, Trigger: port.TriggerSpec{OneShot: clock.Now()}}, State: port.ScheduleState{Enabled: true, NextFireAt: clock.Now()}}); err != nil {
					t.Fatal(err)
				}
				poll()
				if name == "second" {
					awaitLiveness(t, second)
				}
			}
			for range 5 {
				poll()
			}
			loaded, err := store.Load(ctx, "third")
			if err != nil || loaded.State.FireCount != 0 || active.Load() != 2 || high.Load() != 2 {
				t.Fatalf("bounded admission: third=%+v err=%v active=%d high=%d", loaded.State, err, active.Load(), high.Load())
			}
			firstOnce.Do(func() { close(releaseFirst) })
			firstBatch.Wait()
			// The reconciled fixture is now settled, as a real callback would do.
			if reconcile {
				if err := store.RecordFire(ctx, port.ScheduleFire{ID: "settled", ScheduleName: "first", FiredAt: clock.Now(), Stop: session.StopCancelled}); err != nil {
					t.Fatal(err)
				}
			}
			poll()
			awaitLiveness(t, third)
			secondOnce.Do(func() { close(releaseSecond) })
			s.firesWG.Wait()
			if high.Load() != 2 {
				t.Fatalf("worker high-water exceeded capacity: %d", high.Load())
			}
		})
	}
}

func TestReplicaOwnershipAndCapacitySurviveEpochTurnover(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "started"}[started], func(t *testing.T) {
			t.Cleanup(func() { goleak.VerifyNone(t) })
			ctx := context.Background()
			clock := &livenessClock{now: time.Unix(1700000000, 0)}
			store := memschedulestore.New()
			lease := memlease.New(clock, 24*time.Hour)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			var calls atomic.Int32
			fire := func(ctx context.Context, sched port.Schedule, now time.Time) (port.ScheduleFire, error) {
				if calls.Add(1) > 1 {
					return port.ScheduleFire{ID: "recovered", ScheduleName: sched.Spec.Name, FiredAt: now, Stop: session.StopEndTurn}, nil
				}
				f := port.ScheduleFire{ID: "sched--owned", SessionID: "sched--owned", ScheduleName: sched.Spec.Name, FiredAt: now, Stop: session.StopCancelled}
				if started {
					held, err := lease.Acquire(ctx, f.SessionID, "replica-a")
					if err != nil {
						t.Error(err)
					}
					defer func() { _ = lease.Release(context.Background(), held) }()
					inflight := f
					inflight.Stop, inflight.StartedAt, inflight.Deadline = "", now, now.Add(time.Second)
					if err := store.RecordFireStart(ctx, sched.Spec.Name, inflight); err != nil {
						t.Error(err)
					}
				}
				close(entered)
				<-release
				return f, nil
			}
			a := New(Config{Store: store, Clock: clock, Lease: lease, LeaseOwner: "replica-a", Fire: fire, TickInterval: time.Hour, LeaseRenewInterval: time.Hour, MaxConcurrentFires: 1})
			b := New(Config{Store: store, Clock: clock, Lease: lease, LeaseOwner: "replica-b", Fire: fire, TickInterval: time.Hour, LeaseRenewInterval: time.Hour, MaxConcurrentFires: 1,
				ReconcileStaleFire: func(context.Context, port.Schedule) { t.Error("reconciled another replica's live ownership") },
			})
			t.Cleanup(func() { once.Do(func() { close(release) }); _ = a.Stop(); _ = b.Stop() })
			promote := func(s *Scheduler, owner string) {
				held, _, err := s.acquireLeader(ctx)
				if err != nil {
					t.Fatalf("promote %s: %v", owner, err)
				}
				s.started.Store(true)
				s.startEpoch(ctx, held, true)
			}
			if err := store.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{Name: "owned", Singleton: true, Trigger: port.TriggerSpec{Cron: "* * * * *"}}, State: port.ScheduleState{Enabled: true, NextFireAt: clock.Now()}}); err != nil {
				t.Fatal(err)
			}
			promote(a, "a")
			a.tickOnce(a.tickCtx)
			awaitLiveness(t, entered)
			a.releaseLeader()
			a.waitEpoch()
			clock.advance(time.Hour)
			promote(b, "b")
			for range 3 {
				b.tickOnce(b.tickCtx).Wait()
				if _, err := b.FireNow(ctx, "owned", clock.Now()); !errors.Is(err, ErrFireNowOverlap) {
					t.Fatalf("replica claimed live name: %v", err)
				}
			}
			b.releaseLeader()
			b.waitEpoch()
			promote(a, "a-again")
			if err := store.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{Name: "unrelated", Trigger: port.TriggerSpec{OneShot: clock.Now()}}, State: port.ScheduleState{Enabled: true, NextFireAt: clock.Now()}}); err != nil {
				t.Fatal(err)
			}
			a.tickOnce(a.tickCtx).Wait()
			loaded, err := store.Load(ctx, "unrelated")
			if err != nil || loaded.State.FireCount != 0 || calls.Load() != 1 {
				t.Fatalf("epoch reset lifetime capacity: %+v, %v calls=%d", loaded.State, err, calls.Load())
			}
			once.Do(func() { close(release) })
			a.firesWG.Wait()
			// Remove the unrelated slot and show same-name ownership recovers.
			if err := store.Delete(ctx, "unrelated"); err != nil {
				t.Fatal(err)
			}
			if _, err := a.FireNow(ctx, "owned", clock.Now()); err != nil {
				t.Fatalf("ownership did not recover: %v", err)
			}
		})
	}
}
