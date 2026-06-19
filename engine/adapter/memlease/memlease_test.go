package memlease_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/leaseconformance"
	"github.com/stacklok/mecatl/engine/adapter/memlease"
	"github.com/stacklok/mecatl/engine/port"
)

// fakeClock is an advanceable port.Clock the conformance suite drives forward to
// cross the lease TTL without real sleeps.
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

// TestMemleaseConformance runs the shared SessionLease conformance table against
// the in-memory reference lease, driving its injected fake clock past the TTL.
func TestMemleaseConformance(t *testing.T) {
	leaseconformance.Run(t, func(*testing.T) (port.SessionLease, func(time.Duration)) {
		clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
		return memlease.New(clk, leaseconformance.TTL), clk.advance
	})
}
