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
