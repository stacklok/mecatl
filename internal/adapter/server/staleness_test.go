package server_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memlease"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// fixedClock is an advanceable port.Clock for memlease, independent of the
// server's own cfg.Now (they model two different clocks: memlease's internal
// expiry math vs. the server's age-horizon math).
type fixedClock struct{ t time.Time }

func (c *fixedClock) Now() time.Time { return c.t }

var _ port.Clock = (*fixedClock)(nil)

// staleMeta builds a port.SessionMeta for a StateRunning snapshot last
// modified age before now.
func staleMeta(id session.SessionID, now time.Time, age time.Duration) port.SessionMeta {
	return port.SessionMeta{ID: id, State: session.StateRunning, ModifiedAt: now.Add(-age)}
}

// TestSessionStaleWithinAgeWindowNeverStale pins the hard precondition: a
// running snapshot inside staleSessionWindow is never stale, regardless of
// any liveness/lease signal — even when every other signal would otherwise
// say "stale" (no lease wired, no live run).
func TestSessionStaleWithinAgeWindowNeverStale(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	svc := newLeasedService(t, nil, mockllm.New(mockllm.TextTurn("ok")), func() time.Time { return now })

	meta := staleMeta("orphan-fresh", now, 5*time.Minute) // well inside the 30-minute default window
	if svc.SessionStale(context.Background(), meta) {
		t.Fatal("SessionStale on a fresh (in-window) running snapshot = true, want false")
	}
}

// TestSessionStaleNoLeaseIsLiveNotStale: with no lease wired, a same-process
// live run (IsLive==true) is never stale, even past the age window.
func TestSessionStaleNoLeaseIsLiveNotStale(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	svc := newLeasedService(t, nil, blockingProvider{}, func() time.Time { return now })
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	t.Cleanup(func() {
		run.Cancel()
		for range run.Events() {
		}
		svc.FinishRun(sess.ID, run)
	})

	meta := staleMeta(sess.ID, now, time.Hour) // well past the window
	if svc.SessionStale(context.Background(), meta) {
		t.Fatal("SessionStale on a live (IsLive==true) session = true, want false")
	}
}

// TestSessionStaleNoLeaseNotLivePastWindowIsStale is the single-process/
// file-storage default path: no lease wired, past the age window, not live
// -> stale. This is expected to be the most common real case (issue #475).
func TestSessionStaleNoLeaseNotLivePastWindowIsStale(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	svc := newLeasedService(t, nil, mockllm.New(mockllm.TextTurn("ok")), func() time.Time { return now })

	meta := staleMeta("orphan-crashed", now, time.Hour) // past window, never registered live
	if !svc.SessionStale(context.Background(), meta) {
		t.Fatal("SessionStale, no lease, past window, not live = false, want true")
	}
}

// TestSessionStaleLeasePeerHolds: lease wired, a genuinely different, live
// owner holds the real lease for id (ErrLeaseHeld on the trial, and this
// process's own heldLeases does NOT contain id) -> not stale.
func TestSessionStaleLeasePeerHolds(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clk := &fixedClock{t: time.Unix(1_700_000_000, 0)}
	lease := memlease.New(clk, time.Hour)
	svc := newLeasedService(t, lease, mockllm.New(mockllm.TextTurn("ok")), func() time.Time { return now })

	const id session.SessionID = "subagent-peer-held"
	// A different, genuinely live process takes the real lease for id — this
	// service never went through acquireLease for it, so heldLeases has no entry.
	if _, err := lease.Acquire(context.Background(), id, "peer-owner"); err != nil {
		t.Fatalf("peer Acquire: %v", err)
	}

	meta := staleMeta(id, now, time.Hour) // past window, not live locally
	if svc.SessionStale(context.Background(), meta) {
		t.Fatal("SessionStale with a peer genuinely holding the lease = true, want false")
	}
}

// TestSessionStaleLeaseSelfHeldIsStale is the self-held-lease correction —
// the single most important case in this step. This process holds the REAL
// lease for id (from a run that ended without CloseSession releasing it), but
// IsLive(id) is false (the run deregistered on FinishRun). The trial Acquire
// (a DIFFERENT owner string, "<owner>-stale-trial") collides with our own
// real hold and comes back ErrLeaseHeld — that must be read as staleness
// (our own prior run died without releasing), not as "someone else is live".
func TestSessionStaleLeaseSelfHeldIsStale(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clk := &fixedClock{t: time.Unix(1_700_000_000, 0)}
	lease := memlease.New(clk, time.Hour) // generous TTL: no expiry surprises mid-test
	svc := newLeasedService(t, lease, mockllm.New(mockllm.TextTurn("ok")), func() time.Time { return now })

	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	for range run.Events() {
	}
	// FinishRun deregisters the run (IsLive -> false) but does NOT release the
	// held lease — only CloseSession does that. This is exactly the shape of a
	// process that drove a run to completion in-memory but crashed/exited
	// before ever calling CloseSession, leaving its own lease held.
	svc.FinishRun(sess.ID, run)
	if svc.IsLive(sess.ID) {
		t.Fatal("precondition: IsLive after FinishRun (without CloseSession) = true, want false")
	}

	meta := staleMeta(sess.ID, now, time.Hour) // past window
	if !svc.SessionStale(context.Background(), meta) {
		t.Fatal("SessionStale with a self-held (not-live) real lease = false, want true (self-held-lease correction)")
	}
}

// TestSessionStaleLeaseUnsupportedDisablesSweepNotFallback: ErrLeaseUnsupported
// from the trial Acquire stickily disables the WHOLE sweep for the process
// lifetime, not just this one candidate — a second call, even for a
// different, definitely-stale-looking candidate, also returns not-stale, and
// the lease backend is never even asked again (no per-candidate silent
// fallback to local-only liveness).
func TestSessionStaleLeaseUnsupportedDisablesSweepNotFallback(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	lease := &fakeLease{acquireErr: port.ErrLeaseUnsupported}
	svc := newLeasedService(t, lease, mockllm.New(mockllm.TextTurn("ok")), func() time.Time { return now })

	if svc.LeaseSweepDisabled() {
		t.Fatal("precondition: sweep already disabled")
	}

	meta1 := staleMeta("sub-a", now, time.Hour)
	if svc.SessionStale(context.Background(), meta1) {
		t.Fatal("SessionStale on ErrLeaseUnsupported = true, want false")
	}
	if !svc.LeaseSweepDisabled() {
		t.Fatal("LeaseSweepDisabled() = false after ErrLeaseUnsupported, want true")
	}

	meta2 := staleMeta("sub-b", now, 2*time.Hour) // a different, even-more-stale-looking candidate
	if svc.SessionStale(context.Background(), meta2) {
		t.Fatal("SessionStale on a second candidate after sticky-disable = true, want false")
	}

	lease.mu.Lock()
	acquires := lease.acquires
	lease.mu.Unlock()
	if acquires != 1 {
		t.Fatalf("trial Acquire called %d times, want exactly 1 (sticky-disable must stop re-attempting, not per-candidate fallback)", acquires)
	}
}

// TestSessionStaleLeaseOtherErrorFailSafe: any lease error/timeout other than
// ErrLeaseHeld/ErrLeaseUnsupported is fail-safe — not stale, and NOT sticky
// (a transient infra blip must not disable the whole sweep the way a genuine
// ErrLeaseUnsupported does).
func TestSessionStaleLeaseOtherErrorFailSafe(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	lease := &fakeLease{acquireErr: errors.New("transient backend blip")}
	svc := newLeasedService(t, lease, mockllm.New(mockllm.TextTurn("ok")), func() time.Time { return now })

	meta1 := staleMeta("sub-c", now, time.Hour)
	if svc.SessionStale(context.Background(), meta1) {
		t.Fatal("SessionStale on a transient lease error = true, want false (fail-safe)")
	}
	meta2 := staleMeta("sub-d", now, time.Hour)
	if svc.SessionStale(context.Background(), meta2) {
		t.Fatal("SessionStale on a second transient lease error = true, want false")
	}
	if svc.LeaseSweepDisabled() {
		t.Fatal("LeaseSweepDisabled() = true after a transient error, want false (not sticky, unlike ErrLeaseUnsupported)")
	}
	lease.mu.Lock()
	acquires := lease.acquires
	lease.mu.Unlock()
	if acquires != 2 {
		t.Fatalf("trial Acquire called %d times, want 2 (a transient error is never sticky)", acquires)
	}
}
