package scheduler_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memschedulestore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/scheduler"
)

func TestSchedulerPresentsLiteralNameInLifecycleAndMetrics(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := memschedulestore.New()
	physical := "schedule/deadbeef\x00nightly"
	var events, metrics []session.SchedulePayload
	s := scheduler.New(scheduler.Config{
		Store: store,
		Clock: clk,
		Fire: func(_ context.Context, sched port.Schedule, now time.Time) (port.ScheduleFire, error) {
			return port.ScheduleFire{ID: "fire", ScheduleName: sched.Spec.Name, FiredAt: now, Stop: session.StopEndTurn}, nil
		},
		PresentScheduleName: func(name string) string { return strings.TrimPrefix(name, "schedule/deadbeef\x00") },
		EmitScheduleEvent:   func(_ context.Context, p session.SchedulePayload) { events = append(events, p) },
		ScheduleMetrics:     func(p session.SchedulePayload, _ time.Duration) { metrics = append(metrics, p) },
	})
	if err := store.Save(context.Background(), port.Schedule{Spec: port.ScheduleSpec{Name: physical, Trigger: port.TriggerSpec{Cron: "* * * * *"}}, State: port.ScheduleState{Enabled: true, NextFireAt: clk.Now()}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := s.FireNow(context.Background(), physical, clk.Now()); err != nil {
		t.Fatalf("FireNow: %v", err)
	}
	if len(events) != 1 || events[0].ScheduleName != "nightly" || strings.ContainsRune(events[0].ScheduleName, '\x00') {
		t.Fatalf("event payload = %#v, want literal schedule name", events)
	}
	if len(metrics) != 1 || metrics[0].ScheduleName != "nightly" || strings.ContainsRune(metrics[0].ScheduleName, '\x00') {
		t.Fatalf("metrics payload = %#v, want literal schedule name", metrics)
	}
}
