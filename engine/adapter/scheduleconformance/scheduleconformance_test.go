package scheduleconformance_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memschedulestore"
	"github.com/stacklok/mecatl/engine/adapter/scheduleconformance"
	"github.com/stacklok/mecatl/engine/port"
)

// TestScheduleConformanceSelfTest proves the suite itself is sound by running
// it against the in-memory reference store (the leaseconformance_test.go
// precedent — a shared suite ships with a self-test against its reference
// adapter so a suite bug is caught in the suite's own package, not only in an
// adapter's).
func TestScheduleConformanceSelfTest(t *testing.T) {
	scheduleconformance.Run(t, func(*testing.T) port.ScheduleStore {
		return memschedulestore.New()
	})
}
