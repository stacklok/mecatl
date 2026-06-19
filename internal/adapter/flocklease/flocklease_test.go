package flocklease_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/leaseconformance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/flocklease"
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

// TestFlockleaseConformance runs the shared SessionLease conformance table
// against the single-host flock lease over a temp dir, driving its injected fake
// clock past the TTL.
func TestFlockleaseConformance(t *testing.T) {
	leaseconformance.Run(t, func(t *testing.T) (port.SessionLease, func(time.Duration)) {
		clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
		l, err := flocklease.New(t.TempDir(), leaseconformance.TTL, clk)
		if err != nil {
			t.Fatalf("flocklease.New: %v", err)
		}
		return l, clk.advance
	})
}
