package port

import (
	"context"
	"errors"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

// ErrLeaseHeld is the sentinel a SessionLease returns (wrapped with %w) when the
// requested session lease is currently held by a DIFFERENT, still-live owner: an
// Acquire that cannot take over, or a Renew/Release whose caller no longer holds
// the lease (it expired and was taken, or was released). It is a TRANSIENT
// condition, NOT sticky — the holder may release or its lease may lapse, after
// which a later Acquire succeeds. A consumer maps it to a "leased elsewhere"
// refusal (the run-entry gate) and, on a Renew, to "we lost the lease" (the
// renewer cancels the run). Distinct from ErrLeaseUnsupported, which says the
// seam will never work here at all.
var ErrLeaseHeld = errors.New("port: session lease held by another owner")

// ErrLeaseUnsupported is the sentinel a SessionLease returns (wrapped with %w)
// when the lease BACKEND cannot lease at all — e.g. a remote driver answering
// UNIMPLEMENTED. It is the "this seam will never work here" signal, distinct
// from a transient infrastructure failure (I/O error, timeout) and from
// ErrLeaseHeld (a live competitor). A consumer that sees it via errors.Is should
// stop consulting the seam: the composition layer's run-entry gate logs one INFO
// and stickily disables leasing for the process, degrading to the byte-identical
// no-lease path. This mirrors ErrPruneUnsupported's sticky-disable contract for
// the retention seam.
var ErrLeaseUnsupported = errors.New("port: session leasing not supported by this backend")

// Lease is an immutable value object describing a single-writer hold on a
// session id. It is the unit a SessionLease grants, refreshes, and relinquishes.
// Implementations never mutate a Lease in place; Acquire/Renew return a fresh
// value.
type Lease struct {
	// SessionID is the session this lease guards.
	SessionID session.SessionID
	// Owner is the holding process's owner-identity string (e.g.
	// "<hostname>-<pid>-<build-nonce>"). Composition builds it once per Build so
	// two Builds in one process get distinct owners.
	Owner string
	// Token is the monotonic fencing epoch for this session id: it advances only
	// on a TAKEOVER Acquire (a free/expired/other-owner lease being granted to a
	// new holder) and is STABLE across a successful Renew. A future CAS-Save can
	// reject a writer holding a stale token; V1 plumbs the token but does not
	// consult it (the enforcement is the lease grant itself).
	Token uint64
	// Expiry is the wall-clock instant the lease lapses if not renewed before it.
	Expiry time.Time
}

// SessionLease is the OPTIONAL cross-process single-writer seam for session
// state (ADR 0027 Phase 4, multi-replica readiness). It is discovered by type
// assertion exactly like PrunableStore: a store/backend that does not implement
// it is simply never leased, and composition wires a lease ONLY when an operator
// selects a backend by flag — the default path is byte-identical with no lease.
//
// The loop is lease-agnostic: engine/agent NEVER imports this port. The hold is
// acquired at the server run-entry seam (after the same-process run-entry lock),
// renewed by a Service-owned goroutine, and released on session close / shutdown
// — the same storage-agnostic discipline as port.EventLog (the loop emits;
// composition persists).
//
// CONCURRENCY: implementations MUST be safe for concurrent calls across DISTINCT
// session ids — one process leases many sessions at once. Calls for the SAME id
// from one process are serialised by the caller (one renewer per held lease).
type SessionLease interface {
	// Acquire grants the lease for id to owner. It SUCCEEDS (returning the
	// granted Lease) when the lease is free, expired, or already held by owner;
	// a takeover (free/expired/other-owner→owner) returns a STRICTLY GREATER
	// Token than any prior grant for that id, while a same-owner re-acquire need
	// not advance the token. It returns ErrLeaseHeld (wrapped) when the lease is
	// held by a DIFFERENT, still-live owner, and ErrLeaseUnsupported (wrapped)
	// when the backend cannot lease at all.
	Acquire(ctx context.Context, id session.SessionID, owner string) (Lease, error)

	// Renew extends a lease the caller still holds, returning a REFRESHED Lease
	// (new Expiry, SAME Token — the immutable-value-object discipline; the caller
	// stores the returned value). It returns ErrLeaseHeld (wrapped) when the
	// caller no longer holds the lease — definitively, the record now names a
	// different owner or a different token, meaning it was taken over by another
	// owner or released and re-acquired. Bare expiry of the caller's own
	// owner/token, with nothing else having taken it over, is NOT by itself one
	// of these definitive-loss conditions: an implementation that can prove no
	// one else could have raced it (e.g. a single-host backend re-checking its
	// own durable record) may reclaim instead of declaring loss — this narrows,
	// never widens, when ErrLeaseHeld may be returned, so existing callers are
	// unaffected. An implementation that cannot prove this may still always
	// treat expiry as loss. Either way, ErrLeaseHeld is the LOSS SIGNAL: the
	// renewer treats it as "cancel the run". ErrLeaseUnsupported is wrapped only
	// by a backend that never supported leasing (an already-acquired lease
	// implies the backend supports it, so a healthy seam never starts returning
	// Unsupported mid-hold).
	Renew(ctx context.Context, l Lease) (Lease, error)

	// Release relinquishes a lease the caller holds; it is IDEMPOTENT (releasing
	// an unheld/unknown lease, or one whose owner/token no longer match, is
	// success and a no-op — Release only ever drops the caller's OWN hold). A
	// non-nil error is an infrastructure failure, never "not held".
	Release(ctx context.Context, l Lease) error
}
