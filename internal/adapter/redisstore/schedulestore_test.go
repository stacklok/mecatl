package redisstore_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/adapter/cronparse"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
)

// TestRedisScheduleClaimIsAtMostOnce is the MULTI-REPLICA at-most-once proof for
// the Redis-backed ScheduleStore — the property the jsonl mutex gives only
// single-process, the redis backend gives ACROSS replicas. N goroutines each
// call Claim(ctx, sameName, sameNow, sameNextFire) on the SAME miniredis-backed
// store concurrently (under -race). Exactly ONE Claim must succeed (return nil
// error + the advanced Schedule); all others must return an error wrapping
// port.ErrScheduleNotFound (the fail-safe interpretation: the slot is gone, a
// peer already claimed it). The Lua CAS (EVAL) is the atomic fence — a plain
// HGETALL+HSET would race and let multiple Claimers win.
//
// This runs under the race detector (go test -race) to also catch any
// client-side data race in the Claim path.
func TestRedisScheduleClaimIsAtMostOnce(t *testing.T) {
	const replicas = 10
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	st, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatalf("redisstore.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sch := st.ScheduleStore()
	ctx := context.Background()

	now := time.Unix(1_700_000_000, 0)
	const name = "race-sched"
	const cron = "* * * * *"
	next, err := cronparse.NextFire(cron, now, time.UTC)
	if err != nil {
		t.Fatalf("cronparse.NextFire: %v", err)
	}
	if err := sch.Save(ctx, port.Schedule{
		Spec:  port.ScheduleSpec{Name: name, Prompt: "p", Trigger: port.TriggerSpec{Cron: cron}},
		State: port.ScheduleState{NextFireAt: now, Enabled: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var (
		wg          sync.WaitGroup
		successes   atomic.Int64
		notFounds   atomic.Int64
		otherErrors atomic.Int64
		start       = make(chan struct{})
	)
	wg.Add(replicas)
	for i := 0; i < replicas; i++ {
		go func() {
			defer wg.Done()
			<-start // release all replicas at once to maximize contention
			_, err := sch.Claim(ctx, name, now, next)
			switch {
			case err == nil:
				successes.Add(1)
			case errors.Is(err, port.ErrScheduleNotFound):
				notFounds.Add(1)
			default:
				otherErrors.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := successes.Load(); got != 1 {
		t.Fatalf("successes = %d, want exactly 1 (at-most-once across %d concurrent Claims)", got, replicas)
	}
	if got := otherErrors.Load(); got != 0 {
		t.Fatalf("otherErrors = %d, want 0 (the only legal outcomes are success or ErrScheduleNotFound)", got)
	}
	if got := notFounds.Load(); got != replicas-1 {
		t.Fatalf("notFounds = %d, want %d (every non-winner must see the slot gone)", got, replicas-1)
	}

	// The winner's advance must be durable: a re-Load reflects the advanced
	// NextFireAt + FireCount=1, and a subsequent Claim at the same now is also
	// not-found (the slot is gone for ALL callers, not just the concurrent
	// batch).
	got, err := sch.Load(ctx, name)
	if err != nil {
		t.Fatalf("Load after race: %v", err)
	}
	if got.State.FireCount != 1 {
		t.Errorf("post-race FireCount = %d, want 1 (exactly one Claim advanced)", got.State.FireCount)
	}
	if !got.State.NextFireAt.Equal(next) {
		t.Errorf("post-race NextFireAt = %v, want %v (the winner's advance persisted)", got.State.NextFireAt, next)
	}
	if _, err := sch.Claim(ctx, name, now, next); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Fatalf("Claim at the same now after the race = %v, want ErrScheduleNotFound (the slot stays gone)", err)
	}
}

// TestRedisScheduleClaimIsAtMostOnceRepeated repeats the concurrent-claim proof
// many times to catch the rare interleaving a single run might miss under the
// race detector (the property must hold on EVERY run, not just in expectation).
func TestRedisScheduleClaimIsAtMostOnceRepeated(t *testing.T) {
	const runs = 25
	const replicas = 8
	for r := 0; r < runs; r++ {
		mr, err := miniredis.Run()
		if err != nil {
			t.Fatalf("miniredis: %v", err)
		}
		st, err := redisstore.New(mr.Addr())
		if err != nil {
			t.Fatalf("redisstore.New: %v", err)
		}
		sch := st.ScheduleStore()
		ctx := context.Background()
		now := time.Unix(1_700_000_000, 0).Add(time.Duration(r) * time.Second)
		name := "race-sched-rep"
		next, err := cronparse.NextFire("* * * * *", now, time.UTC)
		if err != nil {
			t.Fatalf("cronparse.NextFire: %v", err)
		}
		if err := sch.Save(ctx, port.Schedule{
			Spec:  port.ScheduleSpec{Name: name, Prompt: "p", Trigger: port.TriggerSpec{Cron: "* * * * *"}},
			State: port.ScheduleState{NextFireAt: now, Enabled: true},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}

		var (
			wg        sync.WaitGroup
			successes atomic.Int64
			start     = make(chan struct{})
		)
		wg.Add(replicas)
		for i := 0; i < replicas; i++ {
			go func() {
				defer wg.Done()
				<-start
				if _, err := sch.Claim(ctx, name, now, next); err == nil {
					successes.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		if got := successes.Load(); got != 1 {
			t.Fatalf("run %d: successes = %d, want 1", r, got)
		}
		_ = st.Close()
		mr.Close()
	}
}
