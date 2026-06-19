package memstore

import (
	"context"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// Lease is an in-memory, concurrency-safe port.SessionLease — the cross-process
// single-writer sibling of the in-memory Store, the same shape as the EventLog
// sibling. It exists so the type-assert discovery path (composition asserting a
// configured SessionStore for port.SessionLease) is exercised offline: a memstore
// deployment can opt INTO leasing by flag, and this is the lease it gets.
//
// It is NOT auto-wired by Store: composition wires a lease only when an operator
// selects a backend, so the default no-flag path stays byte-identical. The Store
// itself deliberately does NOT implement SessionLease — leasing is a separate,
// flag-selected concern, so this is its own type constructed on demand.
//
// Records expire against an injected clock (default time.Now), so tests advance a
// fake clock past the TTL to exercise expiry/takeover without real sleeps.
type Lease struct {
	mu     sync.Mutex
	leases map[session.SessionID]leaseRecord
	maxTok map[session.SessionID]uint64
	now    func() time.Time
	ttl    time.Duration
}

type leaseRecord struct {
	owner  string
	token  uint64
	expiry time.Time
}

// compile-time assertion that Lease satisfies the port.
var _ port.SessionLease = (*Lease)(nil)

// NewLease constructs an in-memory lease with the given TTL. A nil now defaults
// to time.Now; a non-positive ttl defaults to 30s.
func NewLease(ttl time.Duration, now func() time.Time) *Lease {
	if now == nil {
		now = time.Now
	}
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &Lease{
		leases: make(map[session.SessionID]leaseRecord),
		maxTok: make(map[session.SessionID]uint64),
		now:    now,
		ttl:    ttl,
	}
}

// Acquire grants the lease when free/expired/same-owner; else ErrLeaseHeld.
func (l *Lease) Acquire(_ context.Context, id session.SessionID, owner string) (port.Lease, error) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	cur, held := l.leases[id]
	switch {
	case !held, !now.Before(cur.expiry):
		return l.grant(id, owner, l.maxTok[id]+1, now), nil
	case cur.owner == owner:
		return l.grant(id, owner, cur.token, now), nil
	default:
		return port.Lease{}, port.ErrLeaseHeld
	}
}

// Renew extends a held lease (same token, new expiry); else ErrLeaseHeld.
func (l *Lease) Renew(_ context.Context, in port.Lease) (port.Lease, error) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	cur, held := l.leases[in.SessionID]
	if !held || cur.owner != in.Owner || cur.token != in.Token || !now.Before(cur.expiry) {
		return port.Lease{}, port.ErrLeaseHeld
	}
	return l.grant(in.SessionID, in.Owner, in.Token, now), nil
}

// Release drops the caller's own hold; idempotent.
func (l *Lease) Release(_ context.Context, in port.Lease) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	cur, held := l.leases[in.SessionID]
	if held && cur.owner == in.Owner && cur.token == in.Token {
		delete(l.leases, in.SessionID)
	}
	return nil
}

func (l *Lease) grant(id session.SessionID, owner string, token uint64, now time.Time) port.Lease {
	expiry := now.Add(l.ttl)
	l.leases[id] = leaseRecord{owner: owner, token: token, expiry: expiry}
	if token > l.maxTok[id] {
		l.maxTok[id] = token
	}
	return port.Lease{SessionID: id, Owner: owner, Token: token, Expiry: expiry}
}
