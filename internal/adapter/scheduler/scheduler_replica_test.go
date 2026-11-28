package scheduler_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memlease"
	"github.com/stacklok/mecatl/engine/adapter/memschedulestore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/scheduler"
)

// twoReplicas builds a leader (A) and a standby (B) that share one schedule
// store and one lease backend, like two mecak8s pods sharing Redis. It saves a
// due singleton cron schedule named "job".
func twoReplicas(t *testing.T, store port.ScheduleStore, fireA, fireB *fireStub) (*fakeClock, *scheduler.Scheduler, *scheduler.Scheduler) {
	t.Helper()
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	leases := memlease.New(clk, time.Hour)
	mk := func(owner string, f *fireStub) *scheduler.Scheduler {
		return scheduler.New(scheduler.Config{Store: store, Lease: leases, LeaseOwner: owner, Fire: f.fire, Clock: clk, TickInterval: time.Hour})
	}
	a, b := mk("owner-A", fireA), mk("owner-B", fireB)
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start A: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, leader := a.LeaderOwner(); leader {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("A never became the leader")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start B: %v", err)
	}
	if _, leader := b.LeaderOwner(); leader {
		t.Fatal("B is the leader, want standby")
	}
	t.Cleanup(func() {
		_ = b.Stop()
		_ = a.Stop()
	})
	if err := store.Save(context.Background(), port.Schedule{
		Spec:  port.ScheduleSpec{Name: "job", Prompt: "x", Singleton: true, Trigger: port.TriggerSpec{Cron: "* * * * *"}},
		State: port.ScheduleState{NextFireAt: clk.Now(), Enabled: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return clk, a, b
}

// TestFireNowOnStandbyReplica: FireNow succeeds on a replica that does not
// hold the scheduler lease (issue #2423).
func TestFireNowOnStandbyReplica(t *testing.T) {
	fireA, fireB := &fireStub{}, &fireStub{}
	clk, _, b := twoReplicas(t, memschedulestore.New(), fireA, fireB)
	if _, err := b.FireNow(context.Background(), "job", clk.Now()); err != nil {
		t.Fatalf("standby FireNow: %v", err)
	}
	if got := fireB.count(); got != 1 {
		t.Fatalf("standby fires = %d, want 1", got)
	}
}

// TestStandbyFireNowBlocksLeaderTick: while a standby's manual fire is in
// flight, the leader's tick does not start a second fire.
func TestStandbyFireNowBlocksLeaderTick(t *testing.T) {
	fireA := &fireStub{}
	fireB := &fireStub{blockCh: make(chan struct{}), entered: make(chan struct{})}
	clk, a, b := twoReplicas(t, memschedulestore.New(), fireA, fireB)
	done := make(chan error, 1)
	go func() {
		_, err := b.FireNow(context.Background(), "job", clk.Now())
		done <- err
	}()
	<-fireB.entered
	clk.advance(2 * time.Minute) // the next cron slot is due on the leader
	a.RunOnceForTest(context.Background())
	if got := fireA.count(); got != 0 {
		t.Fatalf("leader fires = %d while standby fire in flight, want 0", got)
	}
	close(fireB.blockCh)
	if err := <-done; err != nil {
		t.Fatalf("standby FireNow: %v", err)
	}
}

// TestLeaderFireBlocksStandbyFireNow: while the leader's tick fire is in
// flight, a standby's FireNow is rejected as an overlap.
func TestLeaderFireBlocksStandbyFireNow(t *testing.T) {
	fireA := &fireStub{blockCh: make(chan struct{}), entered: make(chan struct{})}
	fireB := &fireStub{}
	clk, a, b := twoReplicas(t, memschedulestore.New(), fireA, fireB)
	go a.RunOnceForTest(context.Background())
	<-fireA.entered
	_, err := b.FireNow(context.Background(), "job", clk.Now().Add(time.Second))
	if !errors.Is(err, scheduler.ErrFireNowOverlap) {
		t.Fatalf("standby FireNow err = %v, want ErrFireNowOverlap", err)
	}
	if got := fireB.count(); got != 0 {
		t.Fatalf("standby fires = %d, want 0", got)
	}
	close(fireA.blockCh)
}
