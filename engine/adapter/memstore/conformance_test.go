package memstore_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/eventlogconformance"
	"github.com/stacklok/mecatl/engine/adapter/leaseconformance"
	"github.com/stacklok/mecatl/engine/adapter/lineageconformance"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/storeconformance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// TestMemstoreConformance runs the shared SessionStore conformance table
// against the in-memory reference store. This run IS the suite's in-engine
// validation (there is deliberately no separate self-test fake).
func TestMemstoreConformance(t *testing.T) {
	storeconformance.Run(t, func(*testing.T) port.SessionStore {
		return memstore.New()
	})
}

// TestResumableSessionStatusMetrics_Scenario1_StoreRoundTripAndFailure names the
// shared conformance proof required by the acceptance plan. The reference in-memory
// store exercises populated and absent occupancy round trips; every durable adapter
// runs the same shared suite through its own conformance entry point.
func TestResumableSessionStatusMetrics_Scenario1_StoreRoundTripAndFailure(t *testing.T) {
	storeconformance.Run(t, func(*testing.T) port.SessionStore {
		return memstore.New()
	})
}

func TestMemstoreLineageConformance(t *testing.T) {
	lineageconformance.Run(t, memstore.New())
}

func TestMemstoreSessionCreatorConformance(t *testing.T) {
	storeconformance.RunSessionCreator(t, func(*testing.T) (port.SessionStore, port.SessionStore) {
		st := memstore.New()
		return st, st
	})
}

// TestMemstorePrunableConformance runs the shared PrunableStore (retention
// seam) table against the in-memory reference store.
func TestMemstorePrunableConformance(t *testing.T) {
	storeconformance.RunPrunable(t, func(*testing.T) port.SessionStore {
		return memstore.New()
	})
}

func TestSessionContinuityUX_Scenario3_PagerConformance(t *testing.T) {
	storeconformance.RunMetadataPager(t, func(*testing.T) port.SessionStore {
		return memstore.New()
	})
}

func TestMemstoreConditionalPrunableConformance(t *testing.T) {
	storeconformance.RunConditionalPrunable(t, func(*testing.T) port.SessionStore {
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

// TestMemstoreCursorEventLogConformance runs the shared CursorEventLog table
// against the in-memory reference log.
//
// This run is the suite's OWN validation: memstore is the only backend with no
// I/O, no encoding, and no migration, so a failure here is a failure of the
// contract or of the suite, never of a storage detail. Cross-process delivery is
// skipped because a second process shares no memory with this log — the
// obligation is meaningless here rather than unmet, and it is proved against
// Redis and JSONL instead.
func TestMemstoreCursorEventLogConformance(t *testing.T) {
	eventlogconformance.RunCursor(t, eventlogconformance.CursorSuite{
		New: func(*testing.T) port.CursorEventLog {
			return memstore.NewEventLog()
		},
		Reset: func(t *testing.T, log port.CursorEventLog, id session.SessionID) {
			t.Helper()
			log.(*memstore.EventLog).Reset(id)
		},
		SkipCrossProcess: true,
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
