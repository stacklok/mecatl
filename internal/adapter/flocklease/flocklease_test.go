package flocklease_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/leaseconformance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
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
func TestExpiredSameOwnerMustTakeNewGeneration(t *testing.T) {
	const ttl = time.Minute
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	adapter, err := flocklease.New(t.TempDir(), ttl, clk)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	first, err := adapter.Acquire(ctx, session.SessionID("same-owner-expiry"), "owner")
	if err != nil {
		t.Fatalf("Acquire first: %v", err)
	}
	clk.advance(ttl)
	if _, err := adapter.Renew(ctx, first); !errors.Is(err, port.ErrLeaseHeld) {
		t.Fatalf("Renew expired generation = %v, want ErrLeaseHeld", err)
	}
	successor, err := adapter.Acquire(ctx, first.SessionID, first.Owner)
	if err != nil {
		t.Fatalf("Acquire same owner after expiry: %v", err)
	}
	if successor.Token <= first.Token {
		t.Fatalf("successor token = %d, want > expired token %d", successor.Token, first.Token)
	}
	if err := adapter.Release(ctx, first); err != nil {
		t.Fatalf("stale Release: %v", err)
	}
	refreshed, err := adapter.Renew(ctx, successor)
	if err != nil {
		t.Fatalf("Renew successor after stale Release: %v", err)
	}
	if refreshed.Token != successor.Token {
		t.Fatalf("renewed token = %d, want %d", refreshed.Token, successor.Token)
	}
}

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
