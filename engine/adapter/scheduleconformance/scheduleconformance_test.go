package scheduleconformance_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memschedulestore"
	"github.com/stacklok/mecatl/engine/adapter/scheduleconformance"
	"github.com/stacklok/mecatl/engine/port"
)

// fakeClock is an advanceable port.Clock the conformance suite drives forward
// to express "the schedule is now due" / "the slot was missed" without real
// sleeps (the leaseconformance precedent).
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// TestScheduleConformanceSelfTest proves the suite itself is sound by running
// it against the in-memory reference store (the leaseconformance_test.go
// precedent — a shared suite ships with a self-test against its reference
// adapter so a suite bug is caught in the suite's own package, not only in an
// adapter's).
func TestScheduleConformanceSelfTest(t *testing.T) {
	scheduleconformance.Run(t, func(*testing.T) (port.ScheduleStore, func(time.Duration)) {
		clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
		return memschedulestore.New(clk), clk.advance
	})
}
