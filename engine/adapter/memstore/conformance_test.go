package memstore_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/eventlogconformance"
	"github.com/stacklok/mecatl/engine/adapter/leaseconformance"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/storeconformance"
	"github.com/stacklok/mecatl/engine/port"
)

// TestMemstoreConformance runs the shared SessionStore conformance table
// against the in-memory reference store. This run IS the suite's in-engine
// validation (there is deliberately no separate self-test fake).
func TestMemstoreConformance(t *testing.T) {
	storeconformance.Run(t, func(*testing.T) port.SessionStore {
		return memstore.New()
	})
}

// TestMemstorePrunableConformance runs the shared PrunableStore (retention
// seam) table against the in-memory reference store.
func TestMemstorePrunableConformance(t *testing.T) {
	storeconformance.RunPrunable(t, func(*testing.T) port.SessionStore {
		return memstore.New()
	})
}

// TestMemstoreEventLogConformance runs the shared EventLog conformance table
// against the in-memory reference event log (the no-store-dir offline default).
func TestMemstoreEventLogConformance(t *testing.T) {
	eventlogconformance.Run(t, func(*testing.T) port.EventLog {
		return memstore.NewEventLog()
	})
}

// leaseClock is an advanceable now-func source the lease conformance suite drives
// past the TTL without real sleeps.
type leaseClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *leaseClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *leaseClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// TestMemstoreLeaseConformance runs the shared SessionLease conformance table
// against the in-memory lease sibling — the type-assert-discovered seam a
// memstore deployment opts into by flag.
func TestMemstoreLeaseConformance(t *testing.T) {
	leaseconformance.Run(t, func(*testing.T) (port.SessionLease, func(time.Duration)) {
		clk := &leaseClock{t: time.Unix(1_700_000_000, 0)}
		return memstore.NewLease(leaseconformance.TTL, clk.now), clk.advance
	})
}
