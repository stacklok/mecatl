package scheduler_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memschedulestore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/scheduler"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestSchedulerPresentsAuthoritativeNameInLifecycleAndMetrics(t *testing.T) {
	owner := &session.Principal{Issuer: "https://issuer.example", Subject: "alice", GrantType: session.GrantTypeUser}
	digest := sha256.Sum256([]byte(owner.Issuer + "\x00" + owner.Subject))
	ownedPhysical := fmt.Sprintf("schedule/%x\x00nightly", digest[:])
	ownerlessPhysicalLooking := fmt.Sprintf("schedule/%x\x00literal", digest[:])

	for _, tc := range []struct {
		name string
		spec port.ScheduleSpec
		want string
	}{
		{name: "owned physical key", spec: port.ScheduleSpec{Name: ownedPhysical, Owner: owner}, want: "nightly"},
		{name: "ownerless physical-looking literal", spec: port.ScheduleSpec{Name: ownerlessPhysicalLooking}, want: ownerlessPhysicalLooking},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
			store := memschedulestore.New()
			var events, metrics []session.SchedulePayload
			s := scheduler.New(scheduler.Config{
				Store: store,
				Clock: clk,
				Fire: func(_ context.Context, sched port.Schedule, now time.Time) (port.ScheduleFire, error) {
					return port.ScheduleFire{ID: "fire", ScheduleName: sched.Spec.Name, FiredAt: now, Stop: session.StopEndTurn}, nil
				},
				PresentScheduleName: server.PresentScheduleName,
				EmitScheduleEvent:   func(_ context.Context, p session.SchedulePayload) { events = append(events, p) },
				ScheduleMetrics:     func(p session.SchedulePayload, _ time.Duration) { metrics = append(metrics, p) },
			})
			tc.spec.Trigger = port.TriggerSpec{Cron: "* * * * *"}
			if err := store.Save(context.Background(), port.Schedule{Spec: tc.spec, State: port.ScheduleState{Enabled: true, NextFireAt: clk.Now()}}); err != nil {
				t.Fatalf("Save: %v", err)
			}
			if _, err := s.FireNow(context.Background(), tc.spec.Name, clk.Now()); err != nil {
				t.Fatalf("FireNow: %v", err)
			}
			if len(events) != 1 || events[0].ScheduleName != tc.want {
				t.Fatalf("event payload = %#v, want schedule name %q", events, tc.want)
			}
			if len(metrics) != 1 || metrics[0].ScheduleName != tc.want {
				t.Fatalf("metrics payload = %#v, want schedule name %q", metrics, tc.want)
			}
		})
	}
}
