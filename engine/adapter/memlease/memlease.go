// Package memlease is the in-memory reference port.SessionLease: the offline,
// clock-injected single-writer lease the conformance suite validates and that
// composition can wire as the default-store opt-in. It keeps per-session lease
// records in a mutex-guarded map and derives expiry from an injected port.Clock,
// so tests advance a fake clock past the TTL to exercise expiry/takeover without
// real sleeps.
//
// It is the reference for the SAME port.SessionLease contract the flock,
// gRPC-driver, and k8s adapters implement; the contract is the port, the
// in-memory map is one adapter.
package memlease

import (
	"context"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// record is a held lease's stored state: the owner, the current fencing token,
// and the wall-clock expiry. The zero record means "no lease".
type record struct {
	owner  string
	token  uint64
	expiry time.Time
}

// Lease is a concurrency-safe in-memory SessionLease. The token counter is
// per-id monotone (advanced on every takeover), so a successful takeover always
// returns a strictly greater token than any prior grant for that id.
type Lease struct {
	mu     sync.Mutex
	leases map[session.SessionID]record
	clock  port.Clock
	ttl    time.Duration
	maxTok map[session.SessionID]uint64 // highest token ever granted per id
}

// compile-time assertion that Lease satisfies the port.
var _ port.SessionLease = (*Lease)(nil)

// New constructs an in-memory lease with the given TTL and clock. A nil clock or
// a non-positive ttl is a programming error at the only construction sites
// (composition, tests), so they are not defended here beyond a sane default ttl.
func New(clock port.Clock, ttl time.Duration) *Lease {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &Lease{
		leases: make(map[session.SessionID]record),
		clock:  clock,
		ttl:    ttl,
		maxTok: make(map[session.SessionID]uint64),
	}
}

// Acquire grants the lease for id to owner when it is free, expired, or already
// held by owner; otherwise it returns ErrLeaseHeld. A takeover bumps the
// per-id token strictly past any prior grant; a same-owner re-acquire keeps the
// existing token (the hold is unchanged, only the expiry refreshes).
func (l *Lease) Acquire(_ context.Context, id session.SessionID, owner string) (port.Lease, error) {
	now := l.clock.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	cur, held := l.leases[id]
	switch {
	case !held, !now.Before(cur.expiry):
		// free or expired → takeover, strictly-greater token.
		return l.grant(id, owner, l.maxTok[id]+1, now), nil
	case cur.owner == owner:
		// same owner re-acquiring its own live lease → keep token, refresh expiry.
		return l.grant(id, owner, cur.token, now), nil
	default:
		// held by a different, still-live owner.
		return port.Lease{}, port.ErrLeaseHeld
	}
}

// Renew extends a lease the caller still holds (owner + token match, unexpired),
// returning the refreshed lease with the SAME token and a new expiry. Otherwise
// it returns ErrLeaseHeld (the loss signal).
func (l *Lease) Renew(_ context.Context, in port.Lease) (port.Lease, error) {
	now := l.clock.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	cur, held := l.leases[in.SessionID]
	if !held || cur.owner != in.Owner || cur.token != in.Token || !now.Before(cur.expiry) {
		return port.Lease{}, port.ErrLeaseHeld
	}
	return l.grant(in.SessionID, in.Owner, in.Token, now), nil
}

// Release relinquishes a lease the caller holds (owner + token match); it is
// idempotent (an unknown id or a mismatched owner/token is a no-op success).
func (l *Lease) Release(_ context.Context, in port.Lease) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	cur, held := l.leases[in.SessionID]
	if held && cur.owner == in.Owner && cur.token == in.Token {
		delete(l.leases, in.SessionID)
	}
	return nil
}

// grant writes the record and returns the port.Lease value; the caller holds the
// mutex. It records the high-water token so a later takeover stays monotone even
// after a release.
func (l *Lease) grant(id session.SessionID, owner string, token uint64, now time.Time) port.Lease {
	expiry := now.Add(l.ttl)
	l.leases[id] = record{owner: owner, token: token, expiry: expiry}
	if token > l.maxTok[id] {
		l.maxTok[id] = token
	}
	return port.Lease{SessionID: id, Owner: owner, Token: token, Expiry: expiry}
}
