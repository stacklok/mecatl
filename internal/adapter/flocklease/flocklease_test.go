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

// TestRenewReclaimsExpiredLeaseWithNoCompetitor covers issue #1333: a process
// suspended (e.g. laptop sleep) past the TTL must not lose its lease to a
// competitor that never ran. On a single host, the durable record still
// naming the caller at the caller's own token — read under the same stable
// transition lock Acquire/Release use — proves nobody raced an Acquire in the
// interim, so Renew reclaims with a fresh expiry instead of declaring loss.
func TestRenewReclaimsExpiredLeaseWithNoCompetitor(t *testing.T) {
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
	clk.advance(2 * ttl)
	renewed, err := adapter.Renew(ctx, first)
	if err != nil {
		t.Fatalf("Renew expired-but-uncontested lease: %v", err)
	}
	if renewed.Token != first.Token {
		t.Fatalf("renewed token = %d, want unchanged %d", renewed.Token, first.Token)
	}
	if !renewed.Expiry.After(first.Expiry) {
		t.Fatalf("renewed expiry %v not extended past original %v", renewed.Expiry, first.Expiry)
	}
	wantExpiry := clk.Now().Add(ttl)
	if !renewed.Expiry.Equal(wantExpiry) {
		t.Fatalf("renewed expiry = %v, want %v", renewed.Expiry, wantExpiry)
	}
}

// TestRenewStillFailsAfterGenuineTakeover is the critical safety case: a
// record whose owner/token DID change during the gap — a genuine competitor
// took over — must still return ErrLeaseHeld unconditionally, even though on
// a real single host this specific race can't happen concurrently (it is
// simulated here by an out-of-band Acquire under a different owner).
func TestRenewStillFailsAfterGenuineTakeover(t *testing.T) {
	const ttl = time.Minute
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	adapter, err := flocklease.New(t.TempDir(), ttl, clk)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	id := session.SessionID("contested-expiry")
	first, err := adapter.Acquire(ctx, id, "owner-a")
	if err != nil {
		t.Fatalf("Acquire first: %v", err)
	}
	clk.advance(ttl)
	competitor, err := adapter.Acquire(ctx, id, "owner-b")
	if err != nil {
		t.Fatalf("Acquire competitor: %v", err)
	}
	if competitor.Token <= first.Token {
		t.Fatalf("competitor token = %d, want > original token %d", competitor.Token, first.Token)
	}
	if _, err := adapter.Renew(ctx, first); !errors.Is(err, port.ErrLeaseHeld) {
		t.Fatalf("Renew after genuine takeover = %v, want ErrLeaseHeld", err)
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
