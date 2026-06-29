package memschedulestore_test

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
// sleeps (the memlease_test.go precedent).
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

// TestMemschedulestoreConformance runs the shared ScheduleStore conformance
// table against the in-memory reference store, driving its injected fake clock
// to cross due/expiry boundaries.
func TestMemschedulestoreConformance(t *testing.T) {
	scheduleconformance.Run(t, func(*testing.T) (port.ScheduleStore, func(time.Duration)) {
		clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
		return memschedulestore.New(clk), clk.advance
	})
}
