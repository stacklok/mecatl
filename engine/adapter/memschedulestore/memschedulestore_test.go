package memschedulestore_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memschedulestore"
	"github.com/stacklok/mecatl/engine/adapter/scheduleconformance"
	"github.com/stacklok/mecatl/engine/port"
)

// TestMemschedulestoreConformance runs the shared ScheduleStore conformance
// table against the in-memory reference store. The suite controls time by
// passing explicit `now` values to Due/Claim, so the store needs no clock.
func TestMemschedulestoreConformance(t *testing.T) {
	scheduleconformance.Run(t, func(*testing.T) port.ScheduleStore {
		return memschedulestore.New()
	})
}
