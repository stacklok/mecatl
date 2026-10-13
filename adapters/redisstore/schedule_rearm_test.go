package redisstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

type rearmEvalGate struct {
	entered, release chan struct{}
	once             sync.Once
}

func (*rearmEvalGate) DialHook(next redis.DialHook) redis.DialHook { return next }
func (*rearmEvalGate) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func (g *rearmEvalGate) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		args := cmd.Args()
		if cmd.Name() == "evalsha" && args[1] == reArmOneShotScript.Hash() {
			g.once.Do(func() { close(g.entered); <-g.release })
		}
		return next(ctx, cmd)
	}
}

type rearmGetFailure struct {
	key string
	err error
}

func (*rearmGetFailure) DialHook(next redis.DialHook) redis.DialHook { return next }
func (*rearmGetFailure) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (g *rearmGetFailure) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		args := cmd.Args()
		if cmd.Name() == "get" && len(args) > 1 && args[1] == g.key {
			return g.err
		}
		return next(ctx, cmd)
	}
}

func TestRearmSnapshotFencedAtRedisMutation(t *testing.T) {
	for _, successor := range []string{"enabled", "pending", "started", "success", "same-slot-success", "policy", "budget"} {
		t.Run(successor, func(t *testing.T) {
			ctx := context.Background()
			mr := miniredis.RunT(t)
			a, err := New(mr.Addr())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = a.Close() })
			b, err := New(mr.Addr())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = b.Close() })
			as, bs := a.ScheduleStore(), b.ScheduleStore()
			now := time.Unix(1700000000, 0)
			const name = "retry"
			if err := bs.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{Name: name, Trigger: port.TriggerSpec{OneShot: now}, OneShotRetry: true, OneShotMaxRetries: 2}, State: port.ScheduleState{Enabled: true, NextFireAt: now}}); err != nil {
				t.Fatal(err)
			}
			if _, err := bs.Claim(ctx, name, now, time.Time{}); err != nil {
				t.Fatal(err)
			}
			if err := bs.RecordFire(ctx, port.ScheduleFire{ID: "failed", ScheduleName: name, FiredAt: now, Stop: session.StopError}); err != nil {
				t.Fatal(err)
			}
			gate := &rearmEvalGate{entered: make(chan struct{}), release: make(chan struct{})}
			a.testClient().AddHook(gate)
			var once sync.Once
			done := make(chan struct{})
			var rearmErr error
			t.Cleanup(func() { once.Do(func() { close(gate.release) }); <-done })
			go func() {
				defer close(done)
				rearmErr = as.(port.ScheduleOneShotReArmer).ReArmOneShot(ctx, name, now.Add(time.Minute))
			}()
			select {
			case <-gate.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("A did not reach EVAL after reading eligibility")
			}
			switch successor {
			case "policy", "budget":
				sched, err := bs.Load(ctx, name)
				if err != nil {
					t.Fatal(err)
				}
				if successor == "policy" {
					sched.Spec.OneShotRetry = false
				} else {
					sched.Spec.OneShotMaxRetries = 0
				}
				if err := bs.Save(ctx, sched); err != nil {
					t.Fatal(err)
				}
			case "same-slot-success":
				if err := bs.RecordFire(ctx, port.ScheduleFire{ID: "late-success", ScheduleName: name, FiredAt: now, Stop: session.StopEndTurn}); err != nil {
					t.Fatal(err)
				}
			default:
				if err := bs.(port.ScheduleOneShotReArmer).ReArmOneShot(ctx, name, now.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
				if successor != "enabled" {
					later := now.Add(time.Minute)
					if _, err := bs.Claim(ctx, name, later, time.Time{}); err != nil {
						t.Fatal(err)
					}
					fire := port.ScheduleFire{ID: "successor", SessionID: "successor", ScheduleName: name, FiredAt: later, StartedAt: later}
					switch successor {
					case "started":
						if err := bs.RecordFireStart(ctx, name, fire); err != nil {
							t.Fatal(err)
						}
					case "success":
						fire.Stop = session.StopEndTurn
						if err := bs.RecordFire(ctx, fire); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			before, err := bs.Load(ctx, name)
			if err != nil {
				t.Fatal(err)
			}
			once.Do(func() { close(gate.release) })
			<-done
			if !errors.Is(rearmErr, port.ErrScheduleNotFound) {
				t.Fatalf("stale EVAL = %v, want ErrScheduleNotFound", rearmErr)
			}
			after, err := bs.Load(ctx, name)
			if err != nil || after.State != before.State || after.Spec.OneShotRetry != before.Spec.OneShotRetry || after.Spec.OneShotMaxRetries != before.Spec.OneShotMaxRetries {
				t.Fatalf("stale EVAL mutated successor: %+v -> %+v, %v", before, after, err)
			}
		})
	}
}

func TestRearmOneShotFailsClosedOnUnreadableCurrentOutcome(t *testing.T) {
	for _, test := range []struct {
		name        string
		current     session.StopReason
		prepare     func(t *testing.T, store *Store)
		wantRearmed bool
	}{
		{name: "failure control", current: session.StopError, wantRearmed: true},
		{
			name:    "get error",
			current: session.StopEndTurn,
			prepare: func(_ *testing.T, store *Store) {
				store.testClient().AddHook(&rearmGetFailure{key: scheduleFireKey("current"), err: errors.New("injected get failure")})
			},
		},
		{
			name:    "corrupt current outcome",
			current: session.StopError,
			prepare: func(t *testing.T, store *Store) {
				if err := store.testClient().Set(t.Context(), scheduleFireKey("current"), "not-json", 0).Err(); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:    "legacy outcome id absent",
			current: session.StopError,
			prepare: func(t *testing.T, store *Store) {
				if err := store.testClient().HDel(t.Context(), scheduleKey("retry"), fieldLastFireOutcomeID).Err(); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			mr := miniredis.RunT(t)
			store, err := New(mr.Addr())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			schedules := store.ScheduleStore()
			now := time.Unix(1_700_000_000, 0)
			const name = "retry"
			if err := schedules.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{Name: name, Trigger: port.TriggerSpec{OneShot: now}, OneShotRetry: true, OneShotMaxRetries: 2}, State: port.ScheduleState{Enabled: true, NextFireAt: now}}); err != nil {
				t.Fatal(err)
			}
			if _, err := schedules.Claim(ctx, name, now, time.Time{}); err != nil {
				t.Fatal(err)
			}
			if err := schedules.RecordFire(ctx, port.ScheduleFire{ID: "failed", SessionID: "session", ScheduleName: name, FiredAt: now, Stop: session.StopError}); err != nil {
				t.Fatal(err)
			}
			if err := schedules.RecordFire(ctx, port.ScheduleFire{ID: "current", SessionID: "session", ScheduleName: name, FiredAt: now, Stop: test.current}); err != nil {
				t.Fatal(err)
			}
			if test.prepare != nil {
				test.prepare(t, store)
			}
			before, err := schedules.Load(ctx, name)
			if err != nil {
				t.Fatal(err)
			}
			err = schedules.(port.ScheduleOneShotReArmer).ReArmOneShot(ctx, name, now.Add(time.Minute))
			if test.wantRearmed {
				if err != nil {
					t.Fatalf("ReArmOneShot: %v", err)
				}
			} else if err == nil {
				t.Fatal("ReArmOneShot unexpectedly succeeded")
			}
			after, loadErr := schedules.Load(ctx, name)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if test.wantRearmed {
				if !after.State.Enabled || after.State.OneShotRetryCount != before.State.OneShotRetryCount+1 {
					t.Fatalf("ReArmOneShot state = %+v, want enabled with retry count %d", after.State, before.State.OneShotRetryCount+1)
				}
			} else if after.State != before.State {
				t.Fatalf("failed ReArmOneShot mutated state: %+v -> %+v", before.State, after.State)
			}
		})
	}
}

func TestRecordFirePreservesCurrentNonErrorOutcome(t *testing.T) {
	for _, test := range []struct {
		name        string
		record      func(t *testing.T, schedules port.ScheduleStore, store *Store, now time.Time)
		fireIDs     []string
		wantRearmed bool
	}{
		{
			name: "success then error in same claim",
			record: func(t *testing.T, schedules port.ScheduleStore, _ *Store, now time.Time) {
				t.Helper()
				for _, fire := range []port.ScheduleFire{
					{ID: "success", SessionID: "session", ScheduleName: "retry", FiredAt: now, Stop: session.StopEndTurn},
					{ID: "error", SessionID: "session", ScheduleName: "retry", FiredAt: now, Stop: session.StopError},
				} {
					if err := schedules.RecordFire(t.Context(), fire); err != nil {
						t.Fatal(err)
					}
				}
			},
			fireIDs: []string{"success", "error"},
		},
		{
			name: "error then success in same claim",
			record: func(t *testing.T, schedules port.ScheduleStore, _ *Store, now time.Time) {
				t.Helper()
				for _, fire := range []port.ScheduleFire{
					{ID: "error", SessionID: "session", ScheduleName: "retry", FiredAt: now, Stop: session.StopError},
					{ID: "success", SessionID: "session", ScheduleName: "retry", FiredAt: now, Stop: session.StopEndTurn},
				} {
					if err := schedules.RecordFire(t.Context(), fire); err != nil {
						t.Fatal(err)
					}
				}
			},
			fireIDs: []string{"error", "success"},
		},
		{
			name: "error in distinct claim",
			record: func(t *testing.T, schedules port.ScheduleStore, store *Store, now time.Time) {
				t.Helper()
				if err := schedules.RecordFire(t.Context(), port.ScheduleFire{ID: "success", SessionID: "session", ScheduleName: "retry", FiredAt: now, Stop: session.StopEndTurn}); err != nil {
					t.Fatal(err)
				}
				later := now.Add(time.Minute)
				if err := store.testClient().HSet(t.Context(), scheduleKey("retry"), fieldEnabled, "1", fieldNextFireAt, nanoStr(later)).Err(); err != nil {
					t.Fatal(err)
				}
				if _, err := schedules.Claim(t.Context(), "retry", later, time.Time{}); err != nil {
					t.Fatal(err)
				}
				if err := schedules.RecordFire(t.Context(), port.ScheduleFire{ID: "error", SessionID: "session", ScheduleName: "retry", FiredAt: later, Stop: session.StopError}); err != nil {
					t.Fatal(err)
				}
			},
			fireIDs:     []string{"success", "error"},
			wantRearmed: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			mr := miniredis.RunT(t)
			store, err := New(mr.Addr())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			schedules := store.ScheduleStore()
			now := time.Unix(1_700_000_000, 0)
			if err := schedules.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{Name: "retry", Trigger: port.TriggerSpec{OneShot: now}, OneShotRetry: true, OneShotMaxRetries: 2}, State: port.ScheduleState{Enabled: true, NextFireAt: now}}); err != nil {
				t.Fatal(err)
			}
			if _, err := schedules.Claim(ctx, "retry", now, time.Time{}); err != nil {
				t.Fatal(err)
			}
			test.record(t, schedules, store, now)
			for _, fireID := range test.fireIDs {
				if _, err := schedules.LoadFire(ctx, fireID); err != nil {
					t.Fatalf("LoadFire(%q): %v", fireID, err)
				}
			}
			err = schedules.(port.ScheduleOneShotReArmer).ReArmOneShot(ctx, "retry", now.Add(2*time.Minute))
			if test.wantRearmed {
				if err != nil {
					t.Fatalf("ReArmOneShot: %v", err)
				}
			} else if err == nil {
				t.Fatal("ReArmOneShot unexpectedly succeeded")
			}
		})
	}
}

func TestRecordFireReportsPointerReadFailureAfterPersistingHistory(t *testing.T) {
	ctx := t.Context()
	mr := miniredis.RunT(t)
	store, err := New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	schedules := store.ScheduleStore()
	now := time.Unix(1_700_000_000, 0)
	if err := schedules.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{Name: "retry", Trigger: port.TriggerSpec{OneShot: now}, OneShotRetry: true, OneShotMaxRetries: 2}, State: port.ScheduleState{Enabled: true, NextFireAt: now}}); err != nil {
		t.Fatal(err)
	}
	if _, err := schedules.Claim(ctx, "retry", now, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := schedules.RecordFire(ctx, port.ScheduleFire{ID: "success", SessionID: "session", ScheduleName: "retry", FiredAt: now, Stop: session.StopEndTurn}); err != nil {
		t.Fatal(err)
	}
	if err := store.testClient().Del(ctx, scheduleFireKey("success")).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.testClient().LPush(ctx, scheduleFireKey("success"), "not-a-fire").Err(); err != nil {
		t.Fatal(err)
	}
	if err := schedules.RecordFire(ctx, port.ScheduleFire{ID: "error", SessionID: "session", ScheduleName: "retry", FiredAt: now, Stop: session.StopError}); err == nil {
		t.Fatal("RecordFire unexpectedly succeeded")
	}
	if _, err := schedules.LoadFire(ctx, "error"); err != nil {
		t.Fatalf("LoadFire(error): %v", err)
	}
	if err := schedules.(port.ScheduleOneShotReArmer).ReArmOneShot(ctx, "retry", now.Add(time.Minute)); err == nil {
		t.Fatal("ReArmOneShot unexpectedly succeeded")
	}
}
