package scheduleconformance

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func runExecutionFencedCases(t *testing.T, newStore func(t *testing.T) port.ScheduleStore) {
	t.Helper()
	ctx := context.Background()
	t.Run("claims fence unresolved predecessors and late settlement", func(t *testing.T) {
		for _, manual := range []bool{false, true} {
			for _, outcome := range []string{"pending", "started", "terminal", "skipped"} {
				t.Run(fmt.Sprintf("manual=%t/%s", manual, outcome), func(t *testing.T) {
					s := newStore(t)
					now := time.Unix(1_700_000_000, 0)
					const name = "predecessor"
					if err := s.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{Name: name, Trigger: port.TriggerSpec{Cron: sampleCron}}, State: port.ScheduleState{Enabled: true, NextFireAt: now}}); err != nil {
						t.Fatal(err)
					}
					claim := s.Claim
					if manual {
						claim = s.ClaimNow
					}
					if _, err := claim(ctx, name, now, now.Add(time.Minute)); err != nil {
						t.Fatal(err)
					}
					prior := port.ScheduleFire{ID: "prior", SessionID: "prior", ScheduleName: name, FiredAt: now, StartedAt: now}
					if outcome == "started" || outcome == "terminal" {
						if err := s.RecordFireStart(ctx, name, prior); err != nil {
							t.Fatal(err)
						}
					}
					if outcome == "terminal" || outcome == "skipped" {
						prior.Stop = session.StopEndTurn
						if outcome == "skipped" {
							prior.SessionID, prior.StartedAt, prior.Stop, prior.Err = "", time.Time{}, session.StopCancelled, "misfire skipped by policy"
						}
						if err := s.RecordFire(ctx, prior); err != nil {
							t.Fatal(err)
						}
					}
					later := now.Add(2 * time.Minute)
					_, err := claim(ctx, name, later, later.Add(time.Minute))
					if outcome == "pending" || outcome == "started" {
						if !errors.Is(err, port.ErrScheduleNotFound) {
							t.Fatalf("unresolved predecessor bypassed: %v", err)
						}
						return
					}
					if err != nil {
						t.Fatalf("settled predecessor blocked: %v", err)
					}
					// A distinct late outcome must not settle the newly pending claim.
					prior.ID = "late-prior"
					if err := s.RecordFire(ctx, prior); err != nil {
						t.Fatal(err)
					}
					loaded, err := s.Load(ctx, name)
					if err != nil || loaded.State.LastFireSessionID != port.PendingFireSessionID || !loaded.State.LastFireAt.Equal(later) || loaded.State.FireCount != 2 {
						t.Fatalf("late writer cleared successor: %+v %v", loaded.State, err)
					}
					if _, err := claim(ctx, name, later.Add(time.Hour), later.Add(2*time.Hour)); !errors.Is(err, port.ErrScheduleNotFound) {
						t.Fatalf("late settlement reopened claim: %v", err)
					}
					successor := port.ScheduleFire{ID: "successor", SessionID: "successor", ScheduleName: name, FiredAt: later, StartedAt: later, Deadline: later.Add(time.Hour)}
					if err := s.RecordFireStart(ctx, name, successor); err != nil {
						t.Fatal(err)
					}
					prior.ID = "late-prior-after-start"
					if err := s.RecordFire(ctx, prior); err != nil {
						t.Fatal(err)
					}
					loaded, err = s.Load(ctx, name)
					if err != nil || loaded.State.LastFireSessionID != successor.SessionID || !loaded.State.LastFireStartedAt.Equal(later) || !loaded.State.FireDeadline.Equal(successor.Deadline) {
						t.Fatalf("late writer cleared started successor: %+v %v", loaded.State, err)
					}
					for _, id := range []string{"late-prior", "late-prior-after-start"} {
						fire, err := s.LoadFire(ctx, id)
						if err != nil || fire.ID != id || fire.Stop != prior.Stop || !fire.FiredAt.Equal(now) {
							t.Fatalf("late terminal history lost: %+v %v", fire, err)
						}
					}
				})
			}
		}
	})

	t.Run("rearm validates current eligibility without mutation", func(t *testing.T) {
		for _, outcome := range []string{"enabled", "pending", "started", "success", "cancelled", "missing-history", "wrong-slot", "wrong-session", "cron", "retry-off", "exhausted"} {
			t.Run(outcome, func(t *testing.T) {
				s := newStore(t)
				reArmer, ok := s.(port.ScheduleOneShotReArmer)
				if !ok {
					t.Skip("store does not implement ScheduleOneShotReArmer")
				}
				now := time.Unix(1_700_000_000, 0).UTC()
				const name = "rearm-ineligible"
				sched := port.Schedule{
					Spec:  port.ScheduleSpec{Name: name, Trigger: port.TriggerSpec{OneShot: now}, OneShotRetry: true, OneShotMaxRetries: 2},
					State: port.ScheduleState{LastFireAt: now, LastFireSessionID: "failed", FireCount: 1},
				}
				fire := port.ScheduleFire{ID: "failed", SessionID: "failed", ScheduleName: name, FiredAt: now, Stop: session.StopError}
				switch outcome {
				case "enabled":
					sched.State.Enabled, sched.State.NextFireAt = true, now.Add(time.Minute)
				case "pending":
					sched.State.LastFireSessionID = port.PendingFireSessionID
					fire.FiredAt = now.Add(-time.Minute)
				case "started":
					sched.State.LastFireStartedAt, sched.State.FireDeadline = now, now.Add(time.Hour)
					fire.FiredAt = now.Add(-time.Minute)
				case "success":
					fire.Stop = session.StopEndTurn
				case "cancelled":
					fire.Stop = session.StopCancelled
				case "wrong-slot":
					fire.FiredAt = now.Add(-time.Minute)
				case "wrong-session":
					fire.SessionID, fire.ScheduleName = "older", "other-schedule"
					if err := s.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{Name: fire.ScheduleName}}); err != nil {
						t.Fatal(err)
					}
				case "cron":
					sched.Spec.Trigger = port.TriggerSpec{Cron: sampleCron}
				case "retry-off":
					sched.Spec.OneShotRetry = false
				case "exhausted":
					sched.State.OneShotRetryCount = sched.Spec.OneShotMaxRetries
				}
				if err := s.Save(ctx, sched); err != nil {
					t.Fatal(err)
				}
				if outcome != "missing-history" {
					if err := s.RecordFire(ctx, fire); err != nil {
						t.Fatal(err)
					}
				}
				before, err := s.Load(ctx, name)
				if err != nil || !scheduleStateEqual(before.State, sched.State) {
					t.Fatalf("invalid eligibility fixture: %+v want %+v, %v", before.State, sched.State, err)
				}
				if err := reArmer.ReArmOneShot(ctx, name, now.Add(time.Hour)); !errors.Is(err, port.ErrScheduleNotFound) {
					t.Fatalf("ineligible rearm = %v, want ErrScheduleNotFound", err)
				}
				got, err := s.Load(ctx, name)
				if err != nil || !scheduleStateEqual(got.State, sched.State) {
					t.Fatalf("ineligible successor mutated: %+v want %+v, %v", got.State, sched.State, err)
				}
			})
		}
	})

	t.Run("rearm newer failures obey current budget", func(t *testing.T) {
		s := newStore(t)
		reArmer, ok := s.(port.ScheduleOneShotReArmer)
		if !ok {
			t.Skip("store does not implement ScheduleOneShotReArmer")
		}
		now := time.Unix(1_700_000_000, 0)
		const name = "rearm-budget"
		if err := s.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{Name: name, Trigger: port.TriggerSpec{OneShot: now}, OneShotRetry: true, OneShotMaxRetries: 2}, State: port.ScheduleState{Enabled: true, NextFireAt: now}}); err != nil {
			t.Fatal(err)
		}
		for attempt := 0; attempt <= 2; attempt++ {
			at := now.Add(time.Duration(attempt) * time.Minute)
			if _, err := s.Claim(ctx, name, at, time.Time{}); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordFire(ctx, port.ScheduleFire{ID: fmt.Sprintf("failed-%d", attempt), ScheduleName: name, FiredAt: at, Stop: session.StopError}); err != nil {
				t.Fatal(err)
			}
			err := reArmer.ReArmOneShot(ctx, name, at.Add(time.Minute))
			if attempt < 2 && err != nil {
				t.Fatal(err)
			}
			if attempt == 2 && !errors.Is(err, port.ErrScheduleNotFound) {
				t.Fatalf("budget bypassed: %v", err)
			}
		}
		got, err := s.Load(ctx, name)
		if err != nil || got.State.Enabled || got.State.OneShotRetryCount != 2 || got.State.FireCount != 3 {
			t.Fatalf("retry budget not bounded: %+v %v", got.State, err)
		}
	})
}

func scheduleStateEqual(got, want port.ScheduleState) bool {
	return normalizeScheduleState(got) == normalizeScheduleState(want)
}

func normalizeScheduleState(state port.ScheduleState) port.ScheduleState {
	state.NextFireAt = state.NextFireAt.UTC()
	state.LastFireAt = state.LastFireAt.UTC()
	state.LastFireStartedAt = state.LastFireStartedAt.UTC()
	state.LastFireProgressAt = state.LastFireProgressAt.UTC()
	state.FireDeadline = state.FireDeadline.UTC()
	return state
}
