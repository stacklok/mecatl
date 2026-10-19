package scheduleconformance

import (
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
)

func TestScheduleStateEqualComparesTimestampInstants(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	want := port.ScheduleState{
		NextFireAt:         at,
		LastFireAt:         at.Add(time.Minute),
		FireCount:          1,
		Enabled:            true,
		LastFireSessionID:  "owner-a",
		LastFireStartedAt:  at.Add(2 * time.Minute),
		LastFireProgressAt: at.Add(3 * time.Minute),
		FireDeadline:       at.Add(4 * time.Minute),
	}
	got := want
	location := time.FixedZone("EET", 2*60*60)
	got.NextFireAt = got.NextFireAt.In(location)
	got.LastFireAt = got.LastFireAt.In(location)
	got.LastFireStartedAt = got.LastFireStartedAt.In(location)
	got.LastFireProgressAt = got.LastFireProgressAt.In(location)
	got.FireDeadline = got.FireDeadline.In(location)
	if !scheduleStateEqual(got, want) {
		t.Fatal("equivalent timestamp instants with different locations are not equal")
	}

	for _, test := range []struct {
		name   string
		mutate func(*port.ScheduleState)
	}{
		{
			name: "instant",
			mutate: func(state *port.ScheduleState) {
				state.NextFireAt = state.NextFireAt.Add(time.Nanosecond)
			},
		},
		{
			name: "counter",
			mutate: func(state *port.ScheduleState) {
				state.FireCount++
			},
		},
		{
			name: "owner",
			mutate: func(state *port.ScheduleState) {
				state.LastFireSessionID = "owner-b"
			},
		},
		{
			name: "deadline",
			mutate: func(state *port.ScheduleState) {
				state.FireDeadline = state.FireDeadline.Add(time.Nanosecond)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := got
			test.mutate(&changed)
			if scheduleStateEqual(changed, want) {
				t.Fatal("changed state compared equal")
			}
		})
	}
}
