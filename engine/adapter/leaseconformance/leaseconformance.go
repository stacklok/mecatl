// Package leaseconformance provides a shared conformance test suite for the
// port.SessionLease interface. Adapters (the in-memory reference, the single-host
// flock lease, the gRPC driver client over bufconn, the k8s lease) call Run with
// a factory that constructs a fresh lease plus an advance callback that drives
// the lease's notion of time past its TTL, and the suite exercises only the
// port.SessionLease interface through the port's value types.
//
// Importing "testing" in a non-_test.go file is intentional here: this is a
// test-helper package whose sole purpose is to be imported by adapter tests, the
// conventional Go pattern for shared conformance suites (cf. the sibling
// eventlogconformance/storeconformance packages).
//
// The suite pins the CONTRACT, not the implementation: how the lease stores its
// records (a map, a record file, a coordination.k8s.io Lease object) is
// adapter-internal and deliberately NOT asserted here. It is the SAME suite the
// reference memlease and the gRPC driver client (over bufconn) both pass — the
// dual-path contract-unification: the Go port is the contract, the wire is one
// adapter.
//
// TIME: the suite never sleeps. It expresses "the lease lapsed" by calling the
// factory-supplied advance(d) callback, which pushes the lease's injected clock
// (or, for a real-clock adapter, the real TTL window) forward by d. A fake-clock
// adapter advances instantly; a real-clock adapter may implement advance as a
// short real sleep over a small TTL.
package leaseconformance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// TTL is the lease lifetime the factory MUST construct its lease with, so the
// suite's advance(TTL+ε) calls reliably cross the expiry boundary.
const TTL = 30 * time.Second

const (
	ownerA = "owner-a"
	ownerB = "owner-b"
)

// Run executes the shared SessionLease conformance table against the lease
// produced by newLease. newLease returns a fresh, isolated lease constructed with
// TTL, plus an advance callback that moves THAT lease's clock forward by the
// given duration. Both must be wired to the same time source.
func Run(t *testing.T, newLease func(t *testing.T) (port.SessionLease, func(time.Duration))) {
	t.Helper()
	ctx := context.Background()

	t.Run("acquire fresh succeeds", func(t *testing.T) {
		l, _ := newLease(t)
		const id session.SessionID = "conf-lease-fresh"
		lease, err := l.Acquire(ctx, id, ownerA)
		if err != nil {
			t.Fatalf("Acquire fresh: %v", err)
		}
		if lease.SessionID != id || lease.Owner != ownerA {
			t.Fatalf("Acquire returned %+v, want id=%q owner=%q", lease, id, ownerA)
		}
		if lease.Expiry.IsZero() {
			t.Fatal("Acquire returned a zero Expiry")
		}
	})

	t.Run("renew extends expiry and keeps token", func(t *testing.T) {
		l, advance := newLease(t)
		const id session.SessionID = "conf-lease-renew"
		first, err := l.Acquire(ctx, id, ownerA)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		advance(TTL / 2) // still within the lease window
		renewed, err := l.Renew(ctx, first)
		if err != nil {
			t.Fatalf("Renew within window: %v", err)
		}
		if renewed.Token != first.Token {
			t.Errorf("Renew token = %d, want %d (Renew keeps the same token)", renewed.Token, first.Token)
		}
		if !renewed.Expiry.After(first.Expiry) {
			t.Errorf("Renew expiry = %v, want strictly after %v", renewed.Expiry, first.Expiry)
		}
	})

	t.Run("release frees the lease for another owner", func(t *testing.T) {
		l, _ := newLease(t)
		const id session.SessionID = "conf-lease-release"
		held, err := l.Acquire(ctx, id, ownerA)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		if err := l.Release(ctx, held); err != nil {
			t.Fatalf("Release: %v", err)
		}
		if _, err := l.Acquire(ctx, id, ownerB); err != nil {
			t.Fatalf("Acquire after Release by other owner: %v", err)
		}
	})

	t.Run("double acquire by another owner fails with ErrLeaseHeld", func(t *testing.T) {
		l, _ := newLease(t)
		const id session.SessionID = "conf-lease-contend"
		if _, err := l.Acquire(ctx, id, ownerA); err != nil {
			t.Fatalf("Acquire by A: %v", err)
		}
		_, err := l.Acquire(ctx, id, ownerB)
		if !errors.Is(err, port.ErrLeaseHeld) {
			t.Fatalf("Acquire by B = %v, want ErrLeaseHeld", err)
		}
	})

	t.Run("same owner re-acquire succeeds without contention", func(t *testing.T) {
		l, _ := newLease(t)
		const id session.SessionID = "conf-lease-reacquire"
		first, err := l.Acquire(ctx, id, ownerA)
		if err != nil {
			t.Fatalf("Acquire #1: %v", err)
		}
		second, err := l.Acquire(ctx, id, ownerA)
		if err != nil {
			t.Fatalf("Acquire #2 by same owner: %v", err)
		}
		if second.Token != first.Token {
			t.Errorf("same-owner re-acquire token = %d, want unchanged %d", second.Token, first.Token)
		}
	})

	t.Run("acquire after release yields a strictly greater token", func(t *testing.T) {
		l, _ := newLease(t)
		const id session.SessionID = "conf-lease-token-release"
		first, err := l.Acquire(ctx, id, ownerA)
		if err != nil {
			t.Fatalf("Acquire #1: %v", err)
		}
		if err := l.Release(ctx, first); err != nil {
			t.Fatalf("Release: %v", err)
		}
		second, err := l.Acquire(ctx, id, ownerB)
		if err != nil {
			t.Fatalf("Acquire after release: %v", err)
		}
		if second.Token <= first.Token {
			t.Errorf("token after takeover = %d, want strictly > %d", second.Token, first.Token)
		}
	})

	t.Run("acquire after ttl expiry succeeds with a strictly greater token", func(t *testing.T) {
		l, advance := newLease(t)
		const id session.SessionID = "conf-lease-token-expiry"
		first, err := l.Acquire(ctx, id, ownerA)
		if err != nil {
			t.Fatalf("Acquire by A: %v", err)
		}
		advance(TTL + time.Second) // lapse A's lease
		second, err := l.Acquire(ctx, id, ownerB)
		if err != nil {
			t.Fatalf("Acquire by B after expiry: %v", err)
		}
		if second.Token <= first.Token {
			t.Errorf("token after expiry-takeover = %d, want strictly > %d", second.Token, first.Token)
		}
	})

	t.Run("renew after expiry-and-takeover fails with ErrLeaseHeld", func(t *testing.T) {
		l, advance := newLease(t)
		const id session.SessionID = "conf-lease-renew-lost"
		first, err := l.Acquire(ctx, id, ownerA)
		if err != nil {
			t.Fatalf("Acquire by A: %v", err)
		}
		advance(TTL + time.Second)
		if _, err := l.Acquire(ctx, id, ownerB); err != nil {
			t.Fatalf("takeover by B: %v", err)
		}
		_, err = l.Renew(ctx, first)
		if !errors.Is(err, port.ErrLeaseHeld) {
			t.Fatalf("Renew of a lost lease = %v, want ErrLeaseHeld", err)
		}
	})

	t.Run("renew after release fails with ErrLeaseHeld", func(t *testing.T) {
		l, _ := newLease(t)
		const id session.SessionID = "conf-lease-renew-released"
		held, err := l.Acquire(ctx, id, ownerA)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		if err := l.Release(ctx, held); err != nil {
			t.Fatalf("Release: %v", err)
		}
		_, err = l.Renew(ctx, held)
		if !errors.Is(err, port.ErrLeaseHeld) {
			t.Fatalf("Renew after Release = %v, want ErrLeaseHeld", err)
		}
	})

	t.Run("release is idempotent", func(t *testing.T) {
		l, _ := newLease(t)
		const id session.SessionID = "conf-lease-release-idem"
		held, err := l.Acquire(ctx, id, ownerA)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		if err := l.Release(ctx, held); err != nil {
			t.Fatalf("Release #1: %v", err)
		}
		if err := l.Release(ctx, held); err != nil {
			t.Fatalf("Release #2 (idempotent): %v", err)
		}
		// Releasing an entirely unknown lease is also success.
		if err := l.Release(ctx, port.Lease{SessionID: "conf-lease-never", Owner: ownerA, Token: 1}); err != nil {
			t.Fatalf("Release of unknown lease: %v", err)
		}
	})

	t.Run("token is monotone across a full takeover chain", func(t *testing.T) {
		l, advance := newLease(t)
		const id session.SessionID = "conf-lease-monotone"
		var last uint64
		owners := []string{ownerA, ownerB, ownerA}
		for i, o := range owners {
			lease, err := l.Acquire(ctx, id, o)
			if err != nil {
				t.Fatalf("takeover #%d by %q: %v", i, o, err)
			}
			if lease.Token <= last {
				t.Fatalf("takeover #%d token = %d, want strictly > %d (monotone)", i, lease.Token, last)
			}
			last = lease.Token
			advance(TTL + time.Second) // lapse so the next owner can take over
		}
	})

	t.Run("distinct sessions lease independently", func(t *testing.T) {
		l, _ := newLease(t)
		if _, err := l.Acquire(ctx, "conf-lease-multi-a", ownerA); err != nil {
			t.Fatalf("Acquire a: %v", err)
		}
		// A different id held by a different owner must not contend.
		if _, err := l.Acquire(ctx, "conf-lease-multi-b", ownerB); err != nil {
			t.Fatalf("Acquire b (distinct id): %v", err)
		}
	})
}
