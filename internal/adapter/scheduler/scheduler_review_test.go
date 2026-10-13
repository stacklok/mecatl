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

type preclaimGate struct {
	port.ScheduleStore
	entered, release chan struct{}
}

func (g *preclaimGate) Claim(ctx context.Context, name string, now, next time.Time) (port.Schedule, error) {
	close(g.entered)
	<-g.release
	return g.ScheduleStore.Claim(context.WithoutCancel(ctx), name, now, next)
}

func (g *preclaimGate) ClaimNow(ctx context.Context, name string, now, next time.Time) (port.Schedule, error) {
	close(g.entered)
	<-g.release
	return g.ScheduleStore.ClaimNow(context.WithoutCancel(ctx), name, now, next)
}

func TestReplicaPreclaimCannotBypassSuccessor(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "manual"}[manual], func(t *testing.T) {
			t.Cleanup(func() { goleak.VerifyNone(t) })
			ctx := context.Background()
			clock := &livenessClock{now: time.Unix(1700000000, 0)}
			store := memschedulestore.New()
			gate := &preclaimGate{ScheduleStore: store, entered: make(chan struct{}), release: make(chan struct{})}
			leases := memlease.New(clock, 24*time.Hour)
			entered, release, aDone, bDone := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
			var gateOnce, fireOnce sync.Once
			var calls atomic.Int32
			fire := func(ctx context.Context, sched port.Schedule, now time.Time) (port.ScheduleFire, error) {
				if calls.Add(1) != 1 {
					t.Error("stale replica admitted a second execution")
					return port.ScheduleFire{}, nil
				}
				f := port.ScheduleFire{ID: "sched--b", SessionID: "sched--b", ScheduleName: sched.Spec.Name, FiredAt: now, StartedAt: now}
				if err := store.RecordFireStart(ctx, sched.Spec.Name, f); err != nil {
					t.Error(err)
				}
				close(entered)
				<-release
				f.Stop = session.StopEndTurn
				return f, nil
			}
			a := New(Config{Store: gate, Clock: clock, Lease: leases, LeaseOwner: "a", Fire: fire, TickInterval: time.Hour})
			b := New(Config{Store: store, Clock: clock, Lease: leases, LeaseOwner: "b", Fire: fire, TickInterval: time.Hour})
			t.Cleanup(func() {
				gateOnce.Do(func() { close(gate.release) })
				fireOnce.Do(func() { close(release) })
				_ = a.Stop()
				_ = b.Stop()
				awaitLiveness(t, aDone)
				awaitLiveness(t, bDone)
			})
			if err := store.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{Name: "shared", Trigger: port.TriggerSpec{Cron: "* * * * *"}}, State: port.ScheduleState{Enabled: true, NextFireAt: clock.Now()}}); err != nil {
				t.Fatal(err)
			}
			promote := func(s *Scheduler) {
				held, _, err := s.acquireLeader(ctx)
				if err != nil {
					t.Fatalf("leadership: %v", err)
				}
				s.started.Store(true)
				s.startEpoch(ctx, held, true)
			}
			promote(a)
			// The automatic stale replica carries a later due-slot snapshot.
			aNow := clock.Now().Add(2 * time.Minute)
			var aErr error
			go func() {
				defer close(aDone)
				if manual {
					_, aErr = a.FireNow(ctx, "shared", aNow)
				} else {
					clock.advance(2 * time.Minute)
					a.tickOnce(a.tickCtx).Wait()
				}
			}()
			awaitLiveness(t, gate.entered)
			a.releaseLeader()
			a.waitEpoch()
			promote(b)
			// B starts at an earlier slot; A's blocked claim is still due after B's advance.
			go func() { defer close(bDone); _, _ = b.FireNow(ctx, "shared", aNow.Add(-time.Minute)) }()
			awaitLiveness(t, entered)
			gateOnce.Do(func() { close(gate.release) })
			awaitLiveness(t, aDone)
			if manual && !errors.Is(aErr, port.ErrScheduleNotFound) {
				t.Fatalf("stale manual claim was not rejected atomically: %v", aErr)
			}
			loaded, err := store.Load(ctx, "shared")
			if err != nil || loaded.State.FireCount != 1 || loaded.State.LastFireSessionID != "sched--b" || calls.Load() != 1 {
				t.Fatalf("successor ownership changed: %+v %v calls=%d", loaded.State, err, calls.Load())
			}
			fireOnce.Do(func() { close(release) })
			awaitLiveness(t, bDone)
		})
	}
}

func TestMisfireSkipLeaseBackendSettlesBeforeFreshSlot(t *testing.T) {
	ctx := context.Background()
	clock := &livenessClock{now: time.Unix(1700000000, 0)}
	store := memschedulestore.New()
	var calls atomic.Int32
	s := New(Config{Store: store, Clock: clock, Lease: memlease.New(clock, time.Hour), Fire: func(_ context.Context, sched port.Schedule, now time.Time) (port.ScheduleFire, error) {
		calls.Add(1)
		return port.ScheduleFire{ID: "fresh", ScheduleName: sched.Spec.Name, FiredAt: now, Stop: session.StopEndTurn}, nil
	}})
	t.Cleanup(func() { _ = s.Stop() })
	if err := store.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{Name: "skip", Trigger: port.TriggerSpec{Cron: "* * * * *"}, Misfire: port.MisfireSkip}, State: port.ScheduleState{Enabled: true, NextFireAt: clock.Now().Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	s.RunOnceForTest(ctx)
	loaded, err := store.Load(ctx, "skip")
	if err != nil || loaded.State.LastFireSessionID != "" || !loaded.State.LastFireStartedAt.IsZero() || calls.Load() != 0 {
		t.Fatalf("skipped claim is not settled: %+v %v", loaded.State, err)
	}
	fires, err := store.ListFires(ctx, "skip")
	if err != nil || len(fires) != 1 || fires[0].Stop != session.StopCancelled || fires[0].SessionID != "" || fires[0].Err != "misfire skipped by policy" {
		t.Fatalf("skip outcome invented execution: %+v %v", fires, err)
	}
	clock.advance(loaded.State.NextFireAt.Sub(clock.Now()))
	s.RunOnceForTest(ctx)
	if calls.Load() != 1 {
		t.Fatal("fresh slot did not fire after skipped claim")
	}
}

type prerearmGate struct {
	*memschedulestore.Store
	entered, release chan struct{}
	result           chan error
}

func (g *prerearmGate) ReArmOneShot(ctx context.Context, name string, next time.Time) error {
	close(g.entered)
	<-g.release
	err := g.Store.ReArmOneShot(context.WithoutCancel(ctx), name, next)
	g.result <- err
	return err
}

func TestReplicaDelayedRearmValidatesSuccessor(t *testing.T) {
	for _, successor := range []string{"enabled", "pending", "started", "success", "new-failure", "budget-exhausted"} {
		t.Run(successor, func(t *testing.T) {
			t.Cleanup(func() { goleak.VerifyNone(t) })
			ctx := context.Background()
			clock := &livenessClock{now: time.Unix(1700000000, 0)}
			store := memschedulestore.New()
			gate := &prerearmGate{Store: store, entered: make(chan struct{}), release: make(chan struct{}), result: make(chan error, 1)}
			leases := memlease.New(clock, 24*time.Hour)
			entered, release, bDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var gateOnce, fireOnce sync.Once
			var calls atomic.Int32
			fire := func(ctx context.Context, sched port.Schedule, now time.Time) (port.ScheduleFire, error) {
				if calls.Add(1) != 1 {
					t.Error("duplicate active execution")
				}
				f := port.ScheduleFire{ID: "sched--b", SessionID: "sched--b", ScheduleName: sched.Spec.Name, FiredAt: now, Stop: session.StopEndTurn}
				if successor == "started" {
					started := f
					started.Stop, started.StartedAt = "", now
					if err := store.RecordFireStart(ctx, sched.Spec.Name, started); err != nil {
						t.Error(err)
					}
				}
				close(entered)
				<-release
				if successor == "new-failure" || successor == "budget-exhausted" {
					f.Stop = session.StopError
				}
				return f, nil
			}
			a := New(Config{Store: gate, Clock: clock, Lease: leases, LeaseOwner: "a", Fire: fire, TickInterval: time.Hour})
			b := New(Config{Store: store, Clock: clock, Lease: leases, LeaseOwner: "b", Fire: fire, TickInterval: time.Hour})
			t.Cleanup(func() {
				gateOnce.Do(func() { close(gate.release) })
				fireOnce.Do(func() { close(release) })
				_ = a.Stop()
				_ = b.Stop()
			})
			budget := 2
			if successor == "budget-exhausted" {
				budget = 1
			}
			if err := store.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{Name: "shared", Trigger: port.TriggerSpec{OneShot: clock.Now()}, OneShotRetry: true, OneShotMaxRetries: budget}, State: port.ScheduleState{Enabled: true, NextFireAt: clock.Now()}}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Claim(ctx, "shared", clock.Now(), time.Time{}); err != nil {
				t.Fatal(err)
			}
			if err := store.RecordFire(ctx, port.ScheduleFire{ID: "original-failure", ScheduleName: "shared", FiredAt: clock.Now(), Stop: session.StopError}); err != nil {
				t.Fatal(err)
			}
			promote := func(s *Scheduler) {
				held, _, err := s.acquireLeader(ctx)
				if err != nil {
					t.Fatal(err)
				}
				s.started.Store(true)
				s.startEpoch(ctx, held, true)
			}
			promote(a)
			aBatch := a.tickOnce(a.tickCtx)
			awaitLiveness(t, gate.entered)
			a.releaseLeader()
			a.waitEpoch()
			promote(b)
			b.tickOnce(b.tickCtx).Wait()
			if successor != "enabled" {
				clock.advance(oneShotReArmBackoff)
				bBatch := b.tickOnce(b.tickCtx)
				go func() { defer close(bDone); bBatch.Wait() }()
				awaitLiveness(t, entered)
				if successor != "pending" && successor != "started" {
					fireOnce.Do(func() { close(release) })
					awaitLiveness(t, bDone)
				}
			}
			before, err := store.Load(ctx, "shared")
			if err != nil || before.State.OneShotRetryCount != 1 {
				t.Fatalf("B did not rearm once: %+v %v", before.State, err)
			}
			gateOnce.Do(func() { close(gate.release) })
			aBatch.Wait()
			err = <-gate.result
			after, loadErr := store.Load(ctx, "shared")
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if successor == "new-failure" {
				if err != nil || !after.State.Enabled || after.State.OneShotRetryCount != 2 || after.State.FireCount != before.State.FireCount {
					t.Fatalf("current eligible failure not retried safely: %+v %v", after.State, err)
				}
			} else if !errors.Is(err, port.ErrScheduleNotFound) || after.State != before.State {
				t.Fatalf("delayed A mutated successor: before=%+v after=%+v err=%v", before.State, after.State, err)
			}
			if successor != "enabled" && calls.Load() != 1 {
				t.Fatalf("executions = %d, want 1", calls.Load())
			}
			fireOnce.Do(func() { close(release) })
			if successor != "enabled" {
				awaitLiveness(t, bDone)
			}
		})
	}
}

type failingRearmStore struct {
	*memschedulestore.Store
	attempts atomic.Int32
}

func (s *failingRearmStore) ReArmOneShot(ctx context.Context, name string, next time.Time) error {
	if s.attempts.Add(1) == 1 {
		return errors.New("transient re-arm failure")
	}
	return s.Store.ReArmOneShot(ctx, name, next)
}

func TestPendingRetrySettlementSurvivesRearmFailure(t *testing.T) {
	ctx := context.Background()
	clock := &livenessClock{now: time.Unix(1700000000, 0)}
	store := &failingRearmStore{Store: memschedulestore.New()}
	if err := store.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{Name: "retry", Trigger: port.TriggerSpec{OneShot: clock.Now()}, OneShotRetry: true, OneShotMaxRetries: 1}, State: port.ScheduleState{Enabled: false, FireCount: 1, LastFireSessionID: port.PendingFireSessionID, LastFireAt: clock.Now()}}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	s := New(Config{Store: store, Clock: clock, Fire: func(_ context.Context, sched port.Schedule, now time.Time) (port.ScheduleFire, error) {
		calls.Add(1)
		return port.ScheduleFire{ID: "retry-complete", ScheduleName: sched.Spec.Name, FiredAt: now, Stop: session.StopEndTurn}, nil
	}})
	t.Cleanup(func() { _ = s.Stop() })
	s.RunOnceForTest(ctx)
	s.RunOnceForTest(ctx)
	loaded, err := store.Load(ctx, "retry")
	if err != nil || !loaded.State.Enabled || loaded.State.OneShotRetryCount != 1 || loaded.State.LastFireSessionID != "" {
		t.Fatalf("settled admission failure lost retry: %+v %v", loaded.State, err)
	}
	clock.advance(loaded.State.NextFireAt.Sub(clock.Now()))
	s.RunOnceForTest(ctx)
	if calls.Load() != 1 {
		t.Fatal("settled retry did not fire")
	}
}

type blockedRearmStore struct {
	*memschedulestore.Store
	entered, release chan struct{}
	once             sync.Once
}

func (s *blockedRearmStore) Load(ctx context.Context, name string) (port.Schedule, error) {
	if name == "retry" {
		s.once.Do(func() { close(s.entered) })
		<-s.release
	}
	return s.Store.Load(ctx, name)
}

func TestBlockedRearmLoadDoesNotBlockDueAdmission(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t) })
	ctx := context.Background()
	clock := &livenessClock{now: time.Unix(1700000000, 0)}
	store := &blockedRearmStore{Store: memschedulestore.New(), entered: make(chan struct{}), release: make(chan struct{})}
	entered, release := make(chan struct{}), make(chan struct{})
	var retryOnce, fireOnce sync.Once
	s := New(Config{Store: store, Clock: clock, MaxConcurrentFires: 2, Fire: func(_ context.Context, sched port.Schedule, now time.Time) (port.ScheduleFire, error) {
		close(entered)
		<-release
		return port.ScheduleFire{ID: "due", ScheduleName: sched.Spec.Name, FiredAt: now, Stop: session.StopEndTurn}, nil
	}, ReconcileStaleFire: func(context.Context, port.Schedule) { t.Error("retry raced reconciliation") }})
	t.Cleanup(func() {
		retryOnce.Do(func() { close(store.release) })
		fireOnce.Do(func() { close(release) })
		_ = s.Stop()
	})
	if err := store.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{Name: "retry", Trigger: port.TriggerSpec{OneShot: clock.Now()}, OneShotRetry: true, OneShotMaxRetries: 1}, State: port.ScheduleState{Enabled: false, FireCount: 1, LastFireSessionID: port.PendingFireSessionID, LastFireAt: clock.Now()}}); err != nil {
		t.Fatal(err)
	}
	batch := s.tickOnce(ctx)
	awaitLiveness(t, store.entered)
	for _, name := range []string{"due", "deferred"} {
		if err := store.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{Name: name, Trigger: port.TriggerSpec{OneShot: clock.Now().Add(time.Minute)}}, State: port.ScheduleState{Enabled: true, NextFireAt: clock.Now().Add(time.Minute)}}); err != nil {
			t.Fatal(err)
		}
		clock.advance(time.Minute)
		s.tickOnce(ctx)
		if name == "due" {
			awaitLiveness(t, entered)
		}
	}
	deferred, err := store.Load(ctx, "deferred")
	if err != nil || deferred.State.FireCount != 0 {
		t.Fatalf("blocked rearm escaped lifetime bound: %+v %v", deferred.State, err)
	}
	retryOnce.Do(func() { close(store.release) })
	batch.Wait()
	fireOnce.Do(func() { close(release) })
	s.firesWG.Wait()
}
