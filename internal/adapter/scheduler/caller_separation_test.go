package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memschedulestore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// TestCallerSeparation_Scenario4_OwnerlessCutoverIsObservableAndSafe pins the
// scheduler half of AC4.6: after ownership enforcement is enabled, a historical
// ownerless schedule remains visible to infrastructure inventory but is never
// claimed, fired, or re-armed by the background scheduler.
func TestCallerSeparation_Scenario4_OwnerlessCutoverIsObservableAndSafe(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	store := memschedulestore.New()
	ownerless := port.Schedule{
		Spec: port.ScheduleSpec{
			Name:    "pre-oidc",
			Prompt:  "must not run",
			Trigger: port.TriggerSpec{OneShot: now.Add(-time.Minute)},
		},
		State: port.ScheduleState{Enabled: true, NextFireAt: now.Add(-time.Minute)},
	}
	if err := store.Save(context.Background(), ownerless); err != nil {
		t.Fatalf("Save: %v", err)
	}
	inventory, err := store.List(context.Background())
	if err != nil || len(inventory) != 1 || inventory[0].Spec.Name != ownerless.Spec.Name || inventory[0].Spec.Owner != nil {
		t.Fatalf("ownerless pre-cutover inventory = %+v, %v; want the unchanged ownerless schedule", inventory, err)
	}

	fires := 0
	s := New(Config{
		Store: store,
		Clock: fixedClock{now: now},
		Fire: func(context.Context, port.Schedule, time.Time) (port.ScheduleFire, error) {
			fires++
			return port.ScheduleFire{Stop: session.StopEndTurn}, nil
		},
		CanProcess: func(s port.Schedule) bool { return s.Spec.Owner != nil },
	})
	s.RunOnceForTest(context.Background())
	s.RunOnceForTest(context.Background())

	if fires != 0 {
		t.Fatalf("ownerless schedule fired %d times after cutover, want 0", fires)
	}
	got, err := store.Load(context.Background(), ownerless.Spec.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.State.FireCount != 0 || !got.State.NextFireAt.Equal(ownerless.State.NextFireAt) || !got.State.Enabled {
		t.Fatalf("ownerless schedule was adopted or mutated: %+v", got.State)
	}

	compatibility := New(Config{
		Store: store,
		Clock: fixedClock{now: now},
		Fire: func(context.Context, port.Schedule, time.Time) (port.ScheduleFire, error) {
			fires++
			return port.ScheduleFire{Stop: session.StopEndTurn}, nil
		},
	})
	compatibility.RunOnceForTest(context.Background())
	if fires != 1 {
		t.Fatalf("ownerless compatibility path fired %d times after verifier disable, want 1", fires)
	}
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }
