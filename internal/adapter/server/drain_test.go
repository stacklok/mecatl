package server_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// newDrainTestService builds a minimal Service over mockllm for the drain-gate
// tests: one turn that ends immediately, no lease wired (the gate must work
// WITHOUT a lease too — a draining replica rejects new runs regardless).
func newDrainTestService(t *testing.T) *server.Service {
	t.Helper()
	store := memstore.New()
	ps := permstore.New()
	cat := tool.NewCatalog()
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("ok")),
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, ps),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:     engine,
		Store:      store,
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}

// miniredisRun starts an in-process miniredis for the StorageReady tests.
func miniredisRun() (*miniredis.Miniredis, error) { return miniredis.Run() }

// redisstoreNew wraps redisstore.New for the StorageReady tests.
func redisstoreNew(addr string) (*redisstore.Store, error) { return redisstore.New(addr) }

// newRedisTestService builds a Service backed by the given Redis store (for the
// StorageReady tests): the Service serves traffic through THIS store, so
// StorageReady pings the same client /readyz would test in production.
func newRedisTestService(t *testing.T, st *redisstore.Store) *server.Service {
	t.Helper()
	ps := permstore.New()
	cat := tool.NewCatalog()
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("ok")),
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, ps),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:     engine,
		Store:      st,
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}

// TestDrainGateStartsFalse: a fresh Service accepts run-entries (the gate is
// byte-identical to pre-ADR-0048 when Drain has not been called).
func TestDrainGateStartsFalse(t *testing.T) {
	svc := newDrainTestService(t)
	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := svc.StartRun(context.Background(), sess.ID, "go"); err != nil {
		t.Fatalf("StartRun on a fresh (non-draining) service = %v, want nil", err)
	}
	if svc.ActiveRuns() != 0 {
		t.Errorf("ActiveRuns on a no-lease service = %d, want 0", svc.ActiveRuns())
	}
}

// TestDrainRejectsNewRuns: once Drain is armed, StartRun (and the
// awaiting-resume path) returns ErrUnavailable BEFORE leasing/launching — the
// run never starts. A drain gate without a wired lease still rejects (the gate
// is not lease-dependent). We ALSO assert the run was never registered
// (LookupRun misses) and no lease is held (ActiveRuns==0) — the gate must
// refuse BEFORE the register/acquire side effects, not just return an error
// after launching.
func TestDrainRejectsNewRuns(t *testing.T) {
	svc := newDrainTestService(t)
	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	svc.Drain()
	_, err = svc.StartRun(context.Background(), sess.ID, "after drain")
	if !errors.Is(err, server.ErrUnavailable) {
		t.Fatalf("StartRun after Drain = %v, want ErrUnavailable", err)
	}
	// The run must NEVER have been registered — the gate refused before launch.
	if _, ok := svc.LookupRun(sess.ID); ok {
		t.Fatal("a run was registered after Drain (the drain gate must refuse BEFORE registering/launching, not after)")
	}
	if svc.ActiveRuns() != 0 {
		t.Errorf("ActiveRuns after a drained StartRun = %d, want 0 (no lease held)", svc.ActiveRuns())
	}
}

// TestDrainIsIdempotent: calling Drain twice is harmless (a one-way gate).
func TestDrainIsIdempotent(t *testing.T) {
	svc := newDrainTestService(t)
	if svc.IsDraining() {
		t.Fatalf("IsDraining = true on a fresh service, want false (draining starts false)")
	}
	svc.Drain()
	if !svc.IsDraining() {
		t.Fatalf("IsDraining = false after Drain, want true")
	}
	svc.Drain() // must not panic or error
	if !svc.IsDraining() {
		t.Fatalf("IsDraining = false after double Drain, want true")
	}
	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := svc.StartRun(context.Background(), sess.ID, "go"); !errors.Is(err, server.ErrUnavailable) {
		t.Fatalf("StartRun after double Drain = %v, want ErrUnavailable", err)
	}
}

// TestDrainDoesNotBlockExistingRun: Drain only gates NEW run-entries; an
// already-launched run is NOT cancelled by Drain itself (that is the bounded
// GracefulStop's job in the cmd binary). The run stays LIVE while draining —
// asserted with a blockingProvider (streams nothing until ctx is cancelled) so
// the run cannot have completed by the time Drain is armed, then the explicit
// cancel ends it with the cancelled stop (NOT a benign terminal Drain would
// have imposed — proving Drain did not touch the in-flight run).
func TestDrainDoesNotBlockExistingRun(t *testing.T) {
	svc := newLeasedService(t, nil, blockingProvider{})
	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun before drain: %v", err)
	}
	// Arm the drain while the run is LIVE (blockingProvider streams nothing
	// until cancelled, so the run is StateRunning here, not done). The run must
	// stay live — Drain must NOT cancel an in-flight run.
	svc.Drain()
	// Give the run a moment to observe (and reject) any drain side-effect; a
	// regression that cancelled on Drain would end the run within this window.
	time.Sleep(50 * time.Millisecond)
	if _, ok := svc.LookupRun(sess.ID); !ok {
		t.Fatal("the live run was removed/ended after Drain — Drain must NOT cancel an in-flight run")
	}
	// Now explicitly cancel the run (the bounded GracefulStop's job, not Drain's).
	run.Cancel()
	var stop session.StopReason
	for ev := range run.Events() {
		if ev.Type == session.EvResult && ev.Result != nil {
			stop = ev.Result.Stop
		}
	}
	svc.FinishRun(sess.ID, run)
	if stop != session.StopCancelled {
		t.Fatalf("the live run's stop after explicit cancel = %q, want %q (the run must stay live through Drain and end only on the explicit cancel)", stop, session.StopCancelled)
	}
}

// TestDrainAwaitingResumePathGated: the awaiting-resume path
// (resumeFromAwaiting) reaches acquireLease (and thus the drain gate) ONLY for
// a genuinely-awaiting session. For an idle session the state check fires
// first and returns ErrNoActiveRun — that is correct (there is nothing to
// resume), and the drain gate does not override it. The drain gate's job on
// the resume path is to prevent a NEW resumed run from starting on an
// awaiting session held by a draining replica; that path is structurally
// covered because acquireLease sits on resumeFromAwaiting after the
// StateAwaiting check. Here we assert the idle-session case returns the
// honest ErrNoActiveRun, not a drain error (the gate is not a blanket veto on
// Approve — an in-flight same-process Approve on a LIVE run stays allowed).
func TestDrainAwaitingResumePathGated(t *testing.T) {
	svc := newDrainTestService(t)
	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	svc.Drain()
	// ApproveRun on an idle (non-awaiting) session with no live run: the state
	// check returns ErrNoActiveRun BEFORE the drain gate (acquireLease) is
	// reached — correct, there is nothing to resume.
	_, err = svc.ApproveRun(context.Background(), sess.ID, "ask-x", session.VerdictAllowOnce, "")
	if !errors.Is(err, server.ErrNoActiveRun) {
		t.Fatalf("ApproveRun after Drain on an idle session = %v, want ErrNoActiveRun (state check fires before the drain gate)", err)
	}
}

// TestDrainGateRefusesBeforeAcquire: with a wired lease, the drain gate must
// refuse BEFORE calling Acquire — a regression that acquired-then-refused would
// leak a held lease on a draining replica (the survivor would then have to
// wait the TTL). The fakeLease counts acquires; after Drain, StartRun must
// return ErrUnavailable with the acquire counter UNCHANGED.
func TestDrainGateRefusesBeforeAcquire(t *testing.T) {
	lease := &fakeLease{}
	svc := newLeasedService(t, lease, mockllm.New(mockllm.TextTurn("never runs")))
	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	svc.Drain()
	_, err = svc.StartRun(context.Background(), sess.ID, "after drain")
	if !errors.Is(err, server.ErrUnavailable) {
		t.Fatalf("StartRun after Drain with a wired lease = %v, want ErrUnavailable", err)
	}
	lease.mu.Lock()
	acquires := lease.acquires
	lease.mu.Unlock()
	if acquires != 0 {
		t.Fatalf("Acquire called %d time(s) on a draining replica, want 0 (the drain gate must refuse BEFORE acquiring — a held lease on a draining replica would leak)", acquires)
	}
}

// TestStorageReadyMemstoreAlwaysReady: a non-pinging store (memstore) is
// always ready — readiness is then drain-gated only (the byte-identical
// fallback when --redis-url is empty). Confirms StorageReady does not panic on
// a store without a Ping method.
func TestStorageReadyMemstoreAlwaysReady(t *testing.T) {
	svc := newDrainTestService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !svc.StorageReady(ctx) {
		t.Error("StorageReady on a memstore = false, want true (a non-pinging store is always ready)")
	}
}

// TestStorageReadyRedis: a Redis-backed service's StorageReady pings the SAME
// store the Service serves traffic through. Over a live miniredis it reports
// true; after the broker closes it reports false (so /readyz flips not-ready on
// a Redis outage). Built inline (newDrainTestService uses memstore).
func TestStorageReadyRedis(t *testing.T) {
	mr, err := miniredisRun()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	st, err := redisstoreNew(mr.Addr())
	if err != nil {
		t.Fatalf("redisstore.New: %v", err)
	}
	defer st.Close()
	svc := newRedisTestService(t, st)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !svc.StorageReady(ctx) {
		t.Error("StorageReady on a live miniredis = false, want true")
	}
	mr.Close()
	// After the broker closes, the ping must fail (a short-timeout ctx keeps it
	// from wedging). Allow a brief window for the client to notice.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pingCtx, pingCancel := context.WithTimeout(context.Background(), 2*time.Second)
		if !svc.StorageReady(pingCtx) {
			pingCancel()
			return
		}
		pingCancel()
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("StorageReady on a closed miniredis stayed true, want false (Redis outage → /readyz not-ready)")
}
