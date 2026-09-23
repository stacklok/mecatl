package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/syscaller"
)

// fakeLease is a programmable port.SessionLease for the server-layer lease tests.
// Acquire counts and may fail; Renew/Release run scripted hooks. It is concurrency
// safe so the renewer goroutine and the test can poke it together.
type fakeLease struct {
	mu sync.Mutex

	acquireErr    error
	acquires      int
	acquireExpiry time.Time // expiry the next Acquire grants (zero = time.Now()+1h)
	// acquireHook, when set, runs synchronously inside Acquire (after the count
	// bump, before the grant is returned), passed the requested owner string so
	// a test can distinguish a trial Acquire (owner suffixed with
	// staleTrialLeaseSuffix) from an ordinary run-entry Acquire — used by
	// concurrency tests that need to observe or assert on exactly when an
	// Acquire call happened relative to some other in-flight call (e.g. a
	// same-id Release).
	acquireHook func(id session.SessionID, owner string)

	renewHook         func(port.Lease) (port.Lease, error)
	releaseHook       func(port.Lease) error
	releases          int
	lastReleased      port.Lease // the lease value handed to the last Release call
	lastReleaseCtxErr error
}

func (f *fakeLease) Acquire(_ context.Context, id session.SessionID, owner string) (port.Lease, error) {
	f.mu.Lock()
	f.acquires++
	hook := f.acquireHook
	f.mu.Unlock()
	if hook != nil {
		hook(id, owner)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.acquireErr != nil {
		return port.Lease{}, f.acquireErr
	}
	expiry := f.acquireExpiry
	if expiry.IsZero() {
		expiry = time.Now().Add(time.Hour)
	}
	return port.Lease{SessionID: id, Owner: owner, Token: 1, Expiry: expiry}, nil
}

func (f *fakeLease) Renew(_ context.Context, l port.Lease) (port.Lease, error) {
	f.mu.Lock()
	hook := f.renewHook
	f.mu.Unlock()
	if hook != nil {
		return hook(l)
	}
	return l, nil
}

func (f *fakeLease) Release(ctx context.Context, l port.Lease) error {
	f.mu.Lock()
	f.releases++
	f.lastReleased = l
	f.lastReleaseCtxErr = ctx.Err()
	hook := f.releaseHook
	f.mu.Unlock()
	if hook != nil {
		return hook(l)
	}
	return nil
}

func (f *fakeLease) releaseCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.releases
}

func (f *fakeLease) lastReleasedLease() port.Lease {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastReleased
}

func (f *fakeLease) lastReleaseContextError() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastReleaseCtxErr
}

var _ port.SessionLease = (*fakeLease)(nil)

// blockingProvider streams nothing until ctx is cancelled, then ends the stream —
// so a run on it stays live (StateRunning) until something cancels it. It lets the
// renewer-loss test have a live run for the renewer's Cancel to hit.
type blockingProvider struct{}

func (blockingProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	return func(yield func(port.Chunk, error) bool) {
		<-ctx.Done() // block until the run is cancelled.
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopCancelled}, nil)
	}, nil
}

func (blockingProvider) Capabilities() port.ProviderCapabilities { return port.ProviderCapabilities{} }

var _ port.LLMProvider = blockingProvider{}

// newLeasedService builds a Service whose engine drives one mockllm turn, wired
// with the given SessionLease, a short TTL, and an immediate renew interval so the
// renewer fires quickly under test. An optional now func injects a clock for the
// renewer's grace logic (nil = real time.Now).
func newLeasedService(t *testing.T, lease port.SessionLease, llm port.LLMProvider, now ...func() time.Time) *server.Service {
	t.Helper()
	store := memstore.New()
	ps := permstore.New()
	cat := tool.NewCatalog()
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, ps),
		Model:   "test-model",
	})
	cfg := server.Config{
		Engine: engine,
		Store:  store,

		SessionLease:       lease,
		LeaseOwner:         "owner-test",
		LeaseTTL:           90 * time.Millisecond,
		LeaseRenewInterval: 15 * time.Millisecond,
	}
	if len(now) > 0 && now[0] != nil {
		cfg.Now = now[0]
	}
	svc, err := newPlacementTestService(cfg)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}

// TestLeaseRenewLossCancelsRun: when Renew returns ErrLeaseHeld (we lost the
// lease), the renewer cancels the session's live run via LookupRun→Cancel.
func TestLeaseRenewLossCancelsRun(t *testing.T) {
	lease := &fakeLease{}
	var lost atomic.Bool
	lease.renewHook = func(port.Lease) (port.Lease, error) {
		lost.Store(true)
		return port.Lease{}, port.ErrLeaseHeld // we lost it.
	}
	// A run that BLOCKS until cancelled, so the renewer's Cancel has a live run to
	// hit (blockingProvider streams nothing until ctx is cancelled).
	svc := newLeasedService(t, lease, blockingProvider{})
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	// Drain the run's events; the renewer's lease loss must Cancel it so the
	// channel closes (the blocking turn ends on ctx cancel).
	var sawCancel bool
	for ev := range run.Events() {
		if ev.Type == session.EvResult && ev.Result != nil && ev.Result.Stop == session.StopCancelled {
			sawCancel = true
		}
	}
	svc.FinishRun(sess.ID, run)
	if !lost.Load() {
		t.Fatal("the renewer never attempted a Renew")
	}
	if !sawCancel {
		t.Fatal("the run was not cancelled after the lease loss (renewer→LookupRun→Cancel did not fire)")
	}
	// run.Cancel() only signals cancellation; it does not block until the
	// renewer's own goroutine reaches its subsequent Release call, and the run's
	// events channel can close (ending the drain above) before that happens. So
	// wait for the Release rather than asserting on it immediately.
	if !eventually(time.Second, func() bool { return lease.releaseCount() == 1 }) {
		t.Fatalf("lease Release calls after definitive renewal loss = %d, want 1", lease.releaseCount())
	}
	if err := lease.lastReleaseContextError(); err != nil {
		t.Fatalf("lease-loss Release context = %v, want cancel-detached live context", err)
	}
}

// TestLeaseUnsupportedStickyDisable: an Acquire returning ErrLeaseUnsupported
// stickily disables leasing — the run proceeds, and a SECOND run does not even
// re-attempt Acquire.
func TestLeaseUnsupportedStickyDisable(t *testing.T) {
	lease := &fakeLease{acquireErr: port.ErrLeaseUnsupported}
	svc := newLeasedService(t, lease, mockllm.New(mockllm.TextTurn("ok")))
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "first")
	if err != nil {
		t.Fatalf("StartRun #1 with an unsupported lease = %v, want success (sticky-disable)", err)
	}
	for range run.Events() {
	}
	svc.FinishRun(sess.ID, run)

	// loadAndReopen the completed session for a follow-up run.
	run2, err := svc.StartRun(context.Background(), sess.ID, "second")
	if err != nil {
		t.Fatalf("StartRun #2 after sticky-disable = %v, want success", err)
	}
	for range run2.Events() {
	}
	svc.FinishRun(sess.ID, run2)

	lease.mu.Lock()
	acquires := lease.acquires
	lease.mu.Unlock()
	if acquires != 1 {
		t.Fatalf("Acquire called %d times, want exactly 1 (sticky-disable must stop re-attempting)", acquires)
	}
}

// TestLeaseReleasedOnCloseSession: CloseSession stops the renewer and Releases the
// held lease.
func TestLeaseReleasedOnCloseSession(t *testing.T) {
	lease := &fakeLease{}
	svc := newLeasedService(t, lease, mockllm.New(mockllm.TextTurn("ok")))
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
	svc.FinishRun(sess.ID, run)

	if lease.releaseCount() != 0 {
		t.Fatalf("lease released %d times before CloseSession, want 0", lease.releaseCount())
	}
	svc.CloseSession(sess.ID)
	if lease.releaseCount() != 1 {
		t.Fatalf("lease released %d times after CloseSession, want 1", lease.releaseCount())
	}
}

// TestLeaseHeldElsewhereRefusesRun: Acquire returning ErrLeaseHeld surfaces as
// ErrSessionLeasedElsewhere at the run-entry funnel.
func TestLeaseHeldElsewhereRefusesRun(t *testing.T) {
	lease := &fakeLease{acquireErr: port.ErrLeaseHeld}
	svc := newLeasedService(t, lease, mockllm.New(mockllm.TextTurn("never runs")))
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_, err = svc.StartRun(context.Background(), sess.ID, "go")
	if !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("StartRun with a held lease = %v, want ErrSessionLeasedElsewhere", err)
	}
}

// TestCloseSessionClearsLostOwnershipAndReacquires: once this process's renewer
// definitively loses a lease (ErrLeaseHeld on Renew), CloseSession is the
// documented recovery path (docs/adr/0027-cloud-native.md List 1 row 27) — it
// must actually clear the lostOwnership tombstone instead of deferring forever
// (the pre-fix bug: closeSessionAuthorized tried to reaffirm the already-lost
// lease first, which failed the same way every time, so closeSessionLocal —
// the only place that deletes lostOwnership — was never reached). A run started
// on the SAME Service afterwards must succeed via a fresh Acquire.
func TestCloseSessionClearsLostOwnershipAndReacquires(t *testing.T) {
	lease := &fakeLease{}
	svc := newLeasedService(t, lease, mockllm.New(mockllm.TextTurn("first"), mockllm.TextTurn("second")))
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
	svc.FinishRun(sess.ID, run)

	// The run has already completed, so failing the NEXT background renew tick
	// loses the lease with no live run for onLeaseLost to cancel — it still must
	// tombstone the id via lostOwnership.
	var lost atomic.Bool
	lease.mu.Lock()
	lease.renewHook = func(port.Lease) (port.Lease, error) {
		lost.Store(true)
		return port.Lease{}, port.ErrLeaseHeld // we lost it.
	}
	lease.mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for !lost.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !lost.Load() {
		t.Fatal("the renewer never attempted a Renew after arming renewHook")
	}

	// The lease is now genuinely free (fakeLease has no acquireErr), but before the
	// fix a re-entry on this same Service would keep failing with
	// ErrSessionLeasedElsewhere forever because closeSessionAuthorized could never
	// reach closeSessionLocal to clear lostOwnership.
	acquiresBeforeClose := func() int {
		lease.mu.Lock()
		defer lease.mu.Unlock()
		return lease.acquires
	}()
	svc.CloseSession(sess.ID)

	lease.mu.Lock()
	lease.renewHook = nil // stop failing renews for the reacquired run.
	lease.mu.Unlock()
	run2, err := svc.StartRun(context.Background(), sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun after CloseSession following a lost lease = %v, want success (tombstone should be cleared)", err)
	}
	for range run2.Events() {
	}
	svc.FinishRun(sess.ID, run2)

	lease.mu.Lock()
	acquiresAfter := lease.acquires
	lease.mu.Unlock()
	if acquiresAfter <= acquiresBeforeClose {
		t.Fatalf("Acquire count after CloseSession = %d, want > %d (a fresh Acquire proves the tombstone was cleared, not bypassed)", acquiresAfter, acquiresBeforeClose)
	}
}

// TestCloseSessionStillRefusedWhileLeaseHeldElsewhere is the regression guard for
// the safety property CloseSessionClearsLostOwnershipAndReacquires must not
// widen: a session this process never held (lostOwnership never set, because it
// never successfully acquired in the first place) must still defer CloseSession
// when the backend reports the lease held elsewhere — it must NOT fall into the
// "already lost, skip reaffirm" branch and tear down local state regardless.
func TestCloseSessionStillRefusedWhileLeaseHeldElsewhere(t *testing.T) {
	lease := &fakeLease{acquireErr: port.ErrLeaseHeld}
	svc := newLeasedService(t, lease, mockllm.New(mockllm.TextTurn("never runs")))
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_, err = svc.StartRun(context.Background(), sess.ID, "go")
	if !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("StartRun with a held lease = %v, want ErrSessionLeasedElsewhere", err)
	}

	svc.CloseSession(sess.ID) // must defer, not tear down local state, and not panic.

	if got := lease.releaseCount(); got != 0 {
		t.Fatalf("lease released %d times for a lease this process never held, want 0", got)
	}
	_, err = svc.StartRun(context.Background(), sess.ID, "go")
	if !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("StartRun after a deferred CloseSession = %v, want still ErrSessionLeasedElsewhere", err)
	}
}

// TestStaleSessionSweepRecoversAfterSelfInflictedLeaseLoss reproduces the
// production deadlock: this process's lease renewer misses its window
// (flocklease's Renew returns ErrLeaseHeld identically for "a real competitor
// took it" and "this record simply expired because a renew landed late" —
// there is no live run for onLeaseLost to cancel here, isolating the tombstone
// effect from any run-completion save race), onLeaseLost sets the
// lostOwnership tombstone, and the durable snapshot is (as in production, via a
// crash or an interrupted persist) left at StateRunning.
//
// The session was never closed, so its only recovery path is the periodic
// stale-session reconcile sweep (internal/app/session_reconcile.go), which
// reaches this exact session via SettleIfStale -> acquireMutationLease ->
// acquireLease -> reaffirmLease. Before the fix, reaffirmLease fail-fasts on
// the tombstone without ever attempting a real Acquire, so this is a
// permanent deadlock recoverable only by a process restart or CloseSession
// (docs/adr/0027-cloud-native.md List 1 row 27) — neither of which the sweep
// can perform. The fix: once the tombstone is set, reaffirmLease attempts a
// real Acquire before giving up, and clears the tombstone on success.
func TestStaleSessionSweepRecoversAfterSelfInflictedLeaseLoss(t *testing.T) {
	lease := &fakeLease{}
	store := memstore.New()
	ps := permstore.New()
	cat := tool.NewCatalog()
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("first")),
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, ps),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:             engine,
		Store:              store,
		SessionLease:       lease,
		LeaseOwner:         "owner-test",
		LeaseTTL:           90 * time.Millisecond,
		LeaseRenewInterval: 15 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(svc.Close)

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
	svc.FinishRun(sess.ID, run)

	// The run has already completed (and persisted) cleanly. The lease is still
	// held for the session's life (HOLD-FOR-SESSION-LIFE), so its renewer is
	// still ticking. Fail the NEXT renew with no live run for onLeaseLost to
	// cancel — deterministic, no completion-save race.
	lease.mu.Lock()
	lease.renewHook = func(port.Lease) (port.Lease, error) {
		return port.Lease{}, port.ErrLeaseHeld // self-inflicted or real — flocklease can't tell.
	}
	lease.mu.Unlock()
	staleCtx := syscaller.Context(context.Background(), syscaller.RootStaleSessionReconcile)
	deadline := time.Now().Add(2 * time.Second)
	lostOwnershipObserved := false
	for !lostOwnershipObserved && time.Now().Before(deadline) {
		candidates, err := svc.LostOwnershipCandidates(staleCtx)
		if err != nil {
			t.Fatalf("LostOwnershipCandidates after renewal loss: %v", err)
		}
		for _, candidate := range candidates {
			if candidate == sess.ID {
				lostOwnershipObserved = true
				break
			}
		}
		if !lostOwnershipObserved {
			time.Sleep(time.Millisecond)
		}
	}
	if !lostOwnershipObserved {
		t.Fatal("lost ownership was not recorded after the renewer reported lease loss")
	}

	if svc.IsLive(sess.ID) {
		t.Fatal("precondition: IsLive after loss with no live run = true, want false")
	}

	// Now overwrite the durable snapshot to the StateRunning shape a crash (or an
	// interrupted final persist) leaves behind — the sweep's actual candidate
	// filter (staleMaintenanceSessionCandidate requires State==Running).
	if err := store.Save(context.Background(), crashOrphanedSession(t, sess.ID)); err != nil {
		t.Fatalf("overwrite Save: %v", err)
	}

	settled, err := svc.SettleIfStale(staleCtx, sess.ID)
	if err != nil {
		t.Fatalf("SettleIfStale after self-inflicted lease loss = (%v, %v), want (true, nil) — the sweep must be able to recover a never-closed session", settled, err)
	}
	if !settled {
		t.Fatal("SettleIfStale after self-inflicted lease loss = false, want true")
	}

	acquiresAfter := func() int {
		lease.mu.Lock()
		defer lease.mu.Unlock()
		return lease.acquires
	}()
	if acquiresAfter < 2 {
		t.Fatalf("Acquire count after SettleIfStale = %d, want >= 2 (a fresh Acquire proves the tombstone was cleared, not bypassed)", acquiresAfter)
	}

	// The tombstone must be gone, not merely bypassed once: a normal re-entry now
	// succeeds too.
	lease.mu.Lock()
	lease.renewHook = nil
	lease.mu.Unlock()
	run2, err := svc.StartRun(context.Background(), sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun after SettleIfStale recovery = %v, want success (tombstone should be cleared)", err)
	}
	for range run2.Events() {
	}
	svc.FinishRun(sess.ID, run2)
}

// TestStaleSessionSweepRefusesWhenGenuinelyHeldElsewhere is
// TestStaleSessionSweepRecoversAfterSelfInflictedLeaseLoss's negative
// counterpart: the sweep's bypass of the lostOwnership tombstone must still
// correctly refuse when a REAL competitor (not this same process) currently
// holds the lease, and must leave the durable snapshot untouched.
func TestStaleSessionSweepRefusesWhenGenuinelyHeldElsewhere(t *testing.T) {
	lease := &fakeLease{}
	store := memstore.New()
	ps := permstore.New()
	cat := tool.NewCatalog()
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("first")),
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, ps),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:             engine,
		Store:              store,
		SessionLease:       lease,
		LeaseOwner:         "owner-test",
		LeaseTTL:           90 * time.Millisecond,
		LeaseRenewInterval: 15 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(svc.Close)

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
	svc.FinishRun(sess.ID, run)

	var lost atomic.Bool
	lease.mu.Lock()
	lease.renewHook = func(port.Lease) (port.Lease, error) {
		lost.Store(true)
		return port.Lease{}, port.ErrLeaseHeld
	}
	lease.mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for !lost.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !lost.Load() {
		t.Fatal("the renewer never attempted a Renew after arming renewHook")
	}

	if err := store.Save(context.Background(), crashOrphanedSession(t, sess.ID)); err != nil {
		t.Fatalf("overwrite Save: %v", err)
	}

	// Unlike the recovery test, a REAL competitor now genuinely holds the lease
	// — the sweep's own re-Acquire attempt must see that, not merely "expired".
	lease.mu.Lock()
	lease.acquireErr = port.ErrLeaseHeld
	lease.mu.Unlock()

	staleCtx := syscaller.Context(context.Background(), syscaller.RootStaleSessionReconcile)
	settled, err := svc.SettleIfStale(staleCtx, sess.ID)
	if !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("SettleIfStale while genuinely held elsewhere = (%v, %v), want (false, ErrSessionLeasedElsewhere)", settled, err)
	}
	if settled {
		t.Fatal("SettleIfStale while genuinely held elsewhere = true, want false")
	}

	reloaded, err := store.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.State != session.StateRunning {
		t.Fatalf("reloaded state = %q, want running (no write should have happened while genuinely held elsewhere)", reloaded.State)
	}
}

// TestLeaseRenewedWhileRunLive proves the renewer keeps the hold alive during a
// long run AND that the refreshed lease (not the original) is the one handed to
// Release — i.e. the h.lease refresh under s.mu is load-bearing, not dead code.
// renewHook returns a token-STABLE lease with a strictly-later Expiry each tick,
// bumping a counter; after ≥2 renews we CloseSession and assert the released lease
// carries the refreshed Expiry.
func TestLeaseRenewedWhileRunLive(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	// Acquire grants `base` as the expiry; each renew returns a STRICTLY-LATER
	// expiry (base+n*hour). So a release that carries `base` means the refresh
	// never reached Release (the renewer's h.lease write is dead), while any
	// expiry strictly after `base` means it did.
	lease := &fakeLease{acquireExpiry: base}
	var renews atomic.Int64
	lease.renewHook = func(l port.Lease) (port.Lease, error) {
		n := renews.Add(1)
		// Same token (a renew never bumps it), strictly-later expiry each tick.
		return port.Lease{
			SessionID: l.SessionID,
			Owner:     l.Owner,
			Token:     l.Token,
			Expiry:    base.Add(time.Duration(n) * time.Hour),
		}, nil
	}
	svc := newLeasedService(t, lease, blockingProvider{})
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	// Wait for at least 2 renews (interval is 15ms), then end the session.
	deadline := time.After(3 * time.Second)
	for renews.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("renewer fired only %d times, want >=2 (the renewer must keep the hold alive)", renews.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}
	got := renews.Load()
	svc.CloseSession(sess.ID) // stops the renewer + Releases the (refreshed) lease.
	run.Cancel()
	for range run.Events() {
	}
	svc.FinishRun(sess.ID, run)

	// The released lease must carry a REFRESHED expiry (>= the base+1h the first
	// renew set), proving Release saw heldLeases[id].lease after the renewer wrote
	// it — not the original lease the acquire produced. (Reverting the h.lease
	// assignment in renewLoop makes lastReleased keep the original expiry → fail.)
	released := lease.lastReleasedLease()
	if !released.Expiry.After(base) {
		t.Fatalf("released lease Expiry = %v, want a refreshed expiry after %v (renews=%d) — the h.lease refresh is not reaching Release",
			released.Expiry, base, got)
	}
}

// TestRenewerRaceWithClose is the executable -race guard for the C1 fix: a renewer
// ticking on a short interval (always succeeding) while CloseSession fires
// concurrently must not race on heldLeases[id].lease or panic. Run under the repo
// -race suite, a missing guard around the h.lease read in releaseLease would trip
// the race detector here.
func TestRenewerRaceWithClose(t *testing.T) {
	lease := &fakeLease{}
	lease.renewHook = func(l port.Lease) (port.Lease, error) {
		// Always succeed with a fresh, later-expiry lease so the renewer keeps
		// writing heldLeases[id].lease right up until Close.
		return port.Lease{SessionID: l.SessionID, Owner: l.Owner, Token: l.Token, Expiry: time.Now().Add(time.Hour)}, nil
	}
	svc := newLeasedService(t, lease, blockingProvider{})
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	// Let a few ticks land (interval 15ms), then close while ticks are in flight.
	time.Sleep(40 * time.Millisecond)
	svc.CloseSession(sess.ID)
	run.Cancel()
	for range run.Events() {
	}
	svc.FinishRun(sess.ID, run)
}

func TestStaleRenewCompletionCannotAffectReacquiredLease(t *testing.T) {
	lease := &fakeLease{}
	renewStarted := make(chan struct{})
	finishRenew := make(chan struct{})
	var renewCalls atomic.Int64
	lease.renewHook = func(l port.Lease) (port.Lease, error) {
		if renewCalls.Add(1) == 1 {
			close(renewStarted)
			<-finishRenew // deliberately ignore the cancelled renew context.
			return port.Lease{}, port.ErrLeaseHeld
		}
		return l, nil
	}
	svc := newLeasedService(t, lease, blockingProvider{})
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	first, err := svc.StartRun(context.Background(), sess.ID, "first")
	if err != nil {
		t.Fatalf("StartRun first: %v", err)
	}
	select {
	case <-renewStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("renew did not start")
	}

	// Remove the captured hold while its backend Renew is blocked, then finish the
	// old run and reacquire a successor generation for the same session id.
	svc.CloseSession(sess.ID)
	first.Cancel()
	for range first.Events() {
	}
	svc.FinishRun(sess.ID, first)
	second, err := svc.StartRun(context.Background(), sess.ID, "second")
	if err != nil {
		t.Fatalf("StartRun successor: %v", err)
	}
	before := lease.releaseCount()
	close(finishRenew)
	time.Sleep(50 * time.Millisecond)
	if got := lease.releaseCount(); got != before {
		t.Fatalf("stale renew completion released successor: releases %d -> %d", before, got)
	}
	if _, ok := svc.LookupRun(sess.ID); !ok {
		t.Fatal("stale renew completion cancelled the current run")
	}

	second.Cancel()
	for range second.Events() {
	}
	svc.FinishRun(sess.ID, second)
	svc.CloseSession(sess.ID)
}

// TestLeaseTransientRenewBlipKeepsRun: a SINGLE transient Renew failure (NOT
// ErrLeaseHeld) with ample headroom to expiry must NOT cancel the run — the
// renewer's grace retries next tick. Uses an injected clock pinned well before
// the lease expiry so the grace branch (now+interval < expiry) holds.
func TestLeaseTransientRenewBlipKeepsRun(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	// Pin the clock; the acquire lease expiry is real-time (fakeLease.Acquire uses
	// time.Now()+1h), so a fixed past `now` is always far inside the window — the
	// grace branch always holds, so a transient blip never cancels.
	clk := func() time.Time { return now }

	lease := &fakeLease{}
	var calls atomic.Int64
	lease.renewHook = func(l port.Lease) (port.Lease, error) {
		if calls.Add(1) == 1 {
			return port.Lease{}, errors.New("transient backend blip") // one-off infra error.
		}
		return l, nil // recover on the next tick.
	}
	svc := newLeasedService(t, lease, blockingProvider{}, clk)
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	// Watch for a (wrong) cancellation while letting several ticks pass. The run
	// must stay live through the blip and the recovery tick.
	cancelled := make(chan struct{})
	go func() {
		for ev := range run.Events() {
			if ev.Type == session.EvResult && ev.Result != nil && ev.Result.Stop == session.StopCancelled {
				close(cancelled)
				return
			}
		}
	}()
	// Wait past at least 3 ticks (the blip + recovery), then assert still live.
	select {
	case <-cancelled:
		t.Fatal("the run was cancelled on a single transient renew blip — the grace did not hold")
	case <-time.After(120 * time.Millisecond):
	}
	// Confirm the renewer recovered (≥2 calls: the blip + at least one success).
	if calls.Load() < 2 {
		t.Fatalf("renewer fired %d times, want >=2 (blip + recovery)", calls.Load())
	}
	run.Cancel()
	svc.CloseSession(sess.ID)
	<-cancelled // the explicit cancel now ends it cleanly.
}

// TestReconcileLeaseLossTombstoneRecoversCancelledSession is issue #1334's core
// repro: onLeaseLost drives a session with no parked ask OUT of StateRunning
// via run.Cancel() (to StateCancelled), so it can never again match the
// StateRunning-only stale-session sweep (SessionStale/SettleIfStale) and the
// lostOwnership tombstone would otherwise never clear short of CloseSession or
// a process restart. ReconcileLeaseLossTombstone must clear it once a genuine
// trial-Acquire proves the lease free, unblocking the next ordinary run-entry
// (which itself, unchanged, repairs the session state via loadAndReopen's
// existing Interrupt seam).
func TestReconcileLeaseLossTombstoneRecoversCancelledSession(t *testing.T) {
	lease := &fakeLease{}
	store := memstore.New()
	ps := permstore.New()
	cat := tool.NewCatalog()
	engine := agent.NewEngine(agent.Deps{
		LLM:     blockingProvider{},
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, ps),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:             engine,
		Store:              store,
		SessionLease:       lease,
		LeaseOwner:         "owner-test",
		LeaseTTL:           90 * time.Millisecond,
		LeaseRenewInterval: 15 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(svc.Close)

	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	// Fail the next renew: no parked ask, so onLeaseLost's preserveAwaiting
	// branch does not apply — it cancels the live run, which blockingProvider
	// ends with StopCancelled once ctx is done.
	var lost atomic.Bool
	lease.mu.Lock()
	lease.renewHook = func(port.Lease) (port.Lease, error) {
		lost.Store(true)
		return port.Lease{}, port.ErrLeaseHeld
	}
	lease.mu.Unlock()

	var sawCancel bool
	for ev := range run.Events() {
		if ev.Type == session.EvResult && ev.Result != nil && ev.Result.Stop == session.StopCancelled {
			sawCancel = true
		}
	}
	svc.FinishRun(sess.ID, run)
	if !lost.Load() || !sawCancel {
		t.Fatal("precondition: the renewer never lost the lease and cancelled the run")
	}

	// The engine here has no Store wired (mirrors newAskingService's
	// engineSaves=false), so the durable snapshot is whatever this test writes
	// directly — the crash-orphan-test idiom. Persist the StateCancelled shape
	// onLeaseLost's real cancel path would leave behind.
	cancelled := session.New(sess.ID, session.ModeDefault, sess.EnvironmentRef, session.Limits{}, time.Unix(0, 0))
	if err := cancelled.RecordUserPrompt("go", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := cancelled.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := cancelled.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := store.Save(context.Background(), cancelled); err != nil {
		t.Fatalf("overwrite Save: %v", err)
	}

	// Precondition: the tombstone still fails every ordinary caller fast, even
	// though the session itself is an ordinary Interrupt-recoverable cancelled
	// snapshot.
	if _, err := svc.StartRun(context.Background(), sess.ID, "again"); !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("StartRun before reconcile = %v, want ErrSessionLeasedElsewhere", err)
	}

	staleCtx := syscaller.Context(context.Background(), syscaller.RootStaleSessionReconcile)
	ids, err := svc.LostOwnershipCandidates(staleCtx)
	if err != nil {
		t.Fatalf("LostOwnershipCandidates: %v", err)
	}
	if len(ids) != 1 || ids[0] != sess.ID {
		t.Fatalf("LostOwnershipCandidates = %v, want [%q]", ids, sess.ID)
	}
	cleared, err := svc.ReconcileLeaseLossTombstone(staleCtx, sess.ID)
	if err != nil {
		t.Fatalf("ReconcileLeaseLossTombstone: %v", err)
	}
	if !cleared {
		t.Fatal("ReconcileLeaseLossTombstone = false, want true (the lease is genuinely free)")
	}

	// The tombstone is gone, not merely bypassed once: a normal re-entry now
	// succeeds, and loadAndReopen's existing Interrupt seam repairs the state.
	run2, err := svc.StartRun(context.Background(), sess.ID, "again")
	if err != nil {
		t.Fatalf("StartRun after ReconcileLeaseLossTombstone = %v, want success (tombstone should be cleared)", err)
	}
	run2.Cancel()
	for range run2.Events() {
	}
	svc.FinishRun(sess.ID, run2)
}

// TestOnLeaseLostSerializesAgainstReconcileTrial is the panel finding's
// regression test (issue #1334 follow-up): onLeaseLost's real Release for a
// lost session id must never overlap ReconcileLeaseLossTombstone's trial
// Acquire for the SAME id (engine/port/lease.go's "callers serialize same-id
// calls" contract). It blocks the Release call mid-flight, asserts a
// concurrently-invoked ReconcileLeaseLossTombstone has NOT yet started its
// trial Acquire, then unblocks Release and confirms the Acquire only happens
// once Release has returned — proving leaseLossMu, not luck, orders them.
func TestOnLeaseLostSerializesAgainstReconcileTrial(t *testing.T) {
	lease := &fakeLease{}
	store := memstore.New()
	ps := permstore.New()
	cat := tool.NewCatalog()
	engine := agent.NewEngine(agent.Deps{
		LLM:     blockingProvider{},
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, ps),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:             engine,
		Store:              store,
		SessionLease:       lease,
		LeaseOwner:         "owner-test",
		LeaseTTL:           90 * time.Millisecond,
		LeaseRenewInterval: 15 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(svc.Close)

	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	releaseStarted := make(chan struct{})
	releaseProceed := make(chan struct{})
	var unblockOnce sync.Once
	unblockRelease := func() { unblockOnce.Do(func() { close(releaseProceed) }) }
	// Register the unblock as cleanup FIRST, before anything can Fatal: an
	// earlier failure (e.g. the very overlap this test guards against) must
	// still release the blocked Release/reconcile goroutines rather than
	// leaking them (JAORMX's non-blocking test-robustness follow-up).
	t.Cleanup(unblockRelease)
	var released atomic.Bool
	var blockedOnce sync.Once
	lease.mu.Lock()
	lease.releaseHook = func(port.Lease) error {
		// Only onLeaseLost's OWN Release (the first one) is under test; a
		// later trial's self-Release (leaseTrial's immediate Acquire+Release)
		// must run normally or it would deadlock on releaseProceed too.
		blockedOnce.Do(func() {
			close(releaseStarted)
			<-releaseProceed
			released.Store(true)
		})
		return nil
	}
	var lost atomic.Bool
	lease.renewHook = func(port.Lease) (port.Lease, error) {
		lost.Store(true)
		return port.Lease{}, port.ErrLeaseHeld
	}
	lease.mu.Unlock()

	var overlap atomic.Bool
	var trialAcquires atomic.Int32
	lease.mu.Lock()
	lease.acquireHook = func(_ session.SessionID, owner string) {
		if !strings.HasSuffix(owner, "-stale-trial") {
			return // the initial real run-entry Acquire, not the reconcile's trial.
		}
		trialAcquires.Add(1)
		if !released.Load() {
			overlap.Store(true)
		}
	}
	lease.mu.Unlock()

	// Drain to StopCancelled: onLeaseLost's run.Cancel() runs BEFORE its Release
	// call, so the run can finish while our Release hook is still blocked.
	for ev := range run.Events() {
		if ev.Type == session.EvResult && ev.Result != nil && ev.Result.Stop == session.StopCancelled {
			break
		}
	}
	svc.FinishRun(sess.ID, run)
	if !lost.Load() {
		t.Fatal("precondition: the renewer never lost the lease")
	}

	select {
	case <-releaseStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("onLeaseLost never called Release")
	}

	staleCtx := syscaller.Context(context.Background(), syscaller.RootStaleSessionReconcile)
	reconcileDone := make(chan struct{})
	go func() {
		defer close(reconcileDone)
		if _, err := svc.ReconcileLeaseLossTombstone(staleCtx, sess.ID); err != nil {
			t.Errorf("ReconcileLeaseLossTombstone: %v", err)
		}
	}()

	// Give the reconcile goroutine ample time to run ahead if leaseLossMu did
	// NOT serialize it: it must still be blocked on the per-id lock, so no
	// trial Acquire can have happened yet.
	time.Sleep(100 * time.Millisecond)
	if n := trialAcquires.Load(); n != 0 {
		t.Fatalf("trial Acquire count = %d before Release returned, want 0 (Acquire started while Release was still in flight)", n)
	}
	select {
	case <-reconcileDone:
		t.Fatal("ReconcileLeaseLossTombstone returned before Release completed; leaseLossMu did not serialize it")
	default:
	}

	unblockRelease()

	select {
	case <-reconcileDone:
	case <-time.After(2 * time.Second):
		t.Fatal("ReconcileLeaseLossTombstone never completed after Release was unblocked")
	}
	if overlap.Load() {
		t.Fatal("trial Acquire ran while Release was still in flight — same-id overlap")
	}
	if trialAcquires.Load() != 1 {
		t.Fatalf("trial Acquire count = %d, want 1", trialAcquires.Load())
	}
}

// TestCloseSessionSerializesAgainstReconcileTrial is JAORMX's second-round
// panel follow-up: closeSessionLocal's unconditional tombstone clear is a
// SECOND writer that could race ReconcileLeaseLossTombstone's trial the same
// way onLeaseLost's Release could — CloseSession could observe the tombstone,
// clear it (without ever calling the real backend itself), and let a brand
// new StartRun perform a REAL Acquire while the trial's own self-Release
// (leaseTrial's immediate Acquire-then-Release) was still in flight.
//
// A third-round follow-up review found this version's assertion vacuous: it
// only called the reopening StartRun AFTER both CloseSession and Reconcile
// had already been confirmed done, by which point the trial Release had
// necessarily already completed — so overlap could never be observed either
// way. Fixed by starting a POLLING reopen goroutine concurrently with
// CloseSession, immediately once the trial Release is confirmed blocked
// (before it is ever unblocked): while the tombstone is genuinely still set
// (the fixed behaviour), each poll is refused locally with
// ErrSessionLeasedElsewhere and never reaches the backend at all, so the
// reopening real Acquire can only happen once the tombstone is actually
// cleared — exactly the moment this test needs to observe. The same review
// also flagged that draining to StopCancelled only proves onLeaseLost called
// run.Cancel(), which happens BEFORE its own Release in the function body —
// not that the Release itself had returned — so swapping in the
// trial-blocking releaseHook right after was not provably safe from
// intercepting that first Release instead of the trial's. Fixed by an
// explicit "first Release completed" signal, installed before triggering the
// loss and waited on before installing the trial-blocking hook.
func TestCloseSessionSerializesAgainstReconcileTrial(t *testing.T) {
	lease := &fakeLease{}
	store := memstore.New()
	ps := permstore.New()
	cat := tool.NewCatalog()
	engine := agent.NewEngine(agent.Deps{
		LLM:     blockingProvider{},
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, ps),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:             engine,
		Store:              store,
		SessionLease:       lease,
		LeaseOwner:         "owner-test",
		LeaseTTL:           90 * time.Millisecond,
		LeaseRenewInterval: 15 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(svc.Close)

	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	// Installed BEFORE the loss so onLeaseLost's own Release (the FIRST
	// Release call, made from inside its function body AFTER run.Cancel())
	// is provably observed complete before this test ever installs the
	// trial-blocking hook below.
	firstReleaseDone := make(chan struct{})
	var firstReleaseOnce sync.Once
	lease.mu.Lock()
	lease.releaseHook = func(port.Lease) error {
		firstReleaseOnce.Do(func() { close(firstReleaseDone) })
		return nil
	}
	var lost atomic.Bool
	lease.renewHook = func(port.Lease) (port.Lease, error) {
		lost.Store(true)
		return port.Lease{}, port.ErrLeaseHeld
	}
	lease.mu.Unlock()

	// Drive the loss: draining to StopCancelled only proves onLeaseLost
	// called run.Cancel(), which precedes its own Release call in the
	// function body — the explicit wait below is what actually proves that
	// Release returned.
	for ev := range run.Events() {
		if ev.Type == session.EvResult && ev.Result != nil && ev.Result.Stop == session.StopCancelled {
			break
		}
	}
	svc.FinishRun(sess.ID, run)
	if !lost.Load() {
		t.Fatal("precondition: the renewer never lost the lease")
	}
	select {
	case <-firstReleaseDone:
	case <-time.After(2 * time.Second):
		t.Fatal("onLeaseLost never completed its own Release")
	}

	// Overwrite with the StateCancelled shape onLeaseLost's real cancel path
	// leaves behind (this Config has no Store wired into the engine, so the
	// durable snapshot is whatever this test writes directly).
	cancelled := session.New(sess.ID, session.ModeDefault, sess.EnvironmentRef, session.Limits{}, time.Unix(0, 0))
	if err := cancelled.RecordUserPrompt("go", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := cancelled.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := cancelled.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := store.Save(context.Background(), cancelled); err != nil {
		t.Fatalf("overwrite Save: %v", err)
	}

	// Now block the TRIAL's own self-Release (leaseTrial's immediate
	// Acquire-then-Release). onLeaseLost's own Release is already provably
	// complete (firstReleaseDone above), so this hook's first invocation IS
	// the trial's self-Release.
	trialReleaseStarted := make(chan struct{})
	trialReleaseProceed := make(chan struct{})
	var unblockOnce sync.Once
	unblockTrialRelease := func() { unblockOnce.Do(func() { close(trialReleaseProceed) }) }
	t.Cleanup(unblockTrialRelease)
	var trialReleaseDone atomic.Bool
	var blockedOnce sync.Once
	lease.mu.Lock()
	lease.releaseHook = func(port.Lease) error {
		blockedOnce.Do(func() {
			close(trialReleaseStarted)
			<-trialReleaseProceed
			trialReleaseDone.Store(true)
		})
		return nil
	}
	lease.mu.Unlock()

	var overlap atomic.Bool
	lease.mu.Lock()
	lease.acquireHook = func(_ session.SessionID, owner string) {
		if strings.HasSuffix(owner, "-stale-trial") {
			return // the reconcile's own trial Acquire, not the reopening real one.
		}
		if !trialReleaseDone.Load() {
			overlap.Store(true)
		}
	}
	lease.mu.Unlock()

	staleCtx := syscaller.Context(context.Background(), syscaller.RootStaleSessionReconcile)
	reconcileDone := make(chan struct{})
	go func() {
		defer close(reconcileDone)
		if _, err := svc.ReconcileLeaseLossTombstone(staleCtx, sess.ID); err != nil {
			t.Errorf("ReconcileLeaseLossTombstone: %v", err)
		}
	}()

	select {
	case <-trialReleaseStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("ReconcileLeaseLossTombstone never reached its trial Release")
	}

	closeDone := make(chan struct{})
	go func() {
		defer close(closeDone)
		svc.CloseSession(sess.ID)
	}()

	// CloseSession must still be blocked on leaseLossMu: it must not have
	// cleared the tombstone (or returned at all) while the trial Release is
	// still in flight.
	time.Sleep(100 * time.Millisecond)
	select {
	case <-closeDone:
		t.Fatal("CloseSession returned before the trial Release completed; leaseLossMu did not serialize closeSessionLocal")
	default:
	}

	// Start the REOPENING StartRun NOW, concurrently with the still-blocked
	// trial Release — not after everything settles. While the tombstone is
	// genuinely still set, each attempt is refused LOCALLY with
	// ErrSessionLeasedElsewhere and never reaches the backend, so this loop
	// only performs its real Acquire once the tombstone is actually cleared:
	// the exact moment that must not precede the trial Release completing.
	var run2 *agent.Run
	reopenDone := make(chan struct{})
	go func() {
		defer close(reopenDone)
		for {
			r, err := svc.StartRun(context.Background(), sess.ID, "again")
			if err == nil {
				run2 = r
				return
			}
			if errors.Is(err, server.ErrSessionLeasedElsewhere) {
				time.Sleep(2 * time.Millisecond)
				continue
			}
			t.Errorf("StartRun reopen: %v", err)
			return
		}
	}()

	// The reopen must still be spinning on the tombstone: it must not have
	// succeeded (a real Acquire) while the trial Release is still blocked.
	time.Sleep(100 * time.Millisecond)
	select {
	case <-reopenDone:
		t.Fatal("reopening StartRun succeeded before the trial Release completed; the tombstone was cleared too early")
	default:
	}

	unblockTrialRelease()

	select {
	case <-reconcileDone:
	case <-time.After(2 * time.Second):
		t.Fatal("ReconcileLeaseLossTombstone never completed after its trial Release was unblocked")
	}
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("CloseSession never completed after the trial Release was unblocked")
	}
	select {
	case <-reopenDone:
	case <-time.After(2 * time.Second):
		t.Fatal("reopening StartRun never completed after the trial Release was unblocked")
	}
	if run2 == nil {
		t.Fatal("reopening StartRun failed for a reason other than ErrSessionLeasedElsewhere")
	}
	run2.Cancel()
	for range run2.Events() {
	}
	svc.FinishRun(sess.ID, run2)

	if overlap.Load() {
		t.Fatal("a real Acquire ran while the trial Release was still in flight — same-id overlap")
	}
}

// TestReconcileLeaseLossTombstoneRecoversAwaitingSession is the awaiting
// counterpart: onLeaseLost's preserveAwaiting branch drives the session to
// StateAwaiting (parked on a permission ask) rather than cancelling it, and
// deliberately leaves heldLeases[id] in place (marked invalid) instead of
// deleting it. ReconcileLeaseLossTombstone must clear BOTH the tombstone and
// that stale invalid heldLeases entry, or the very next real Acquire attempt
// would still hard-refuse via acquireLeaseCore's separate held-but-invalid
// check even with the tombstone gone.
func TestReconcileLeaseLossTombstoneRecoversAwaitingSession(t *testing.T) {
	lease := &fakeLease{}
	store := memstore.New()
	ps := permstore.New()
	var ran atomic.Int64
	cat := tool.NewCatalog()
	cat.MustRegister(&writeAskTool{ran: &ran})
	// Engine has no Store wired (mirrors newAskingService's engineSaves=false):
	// the run's internal cancel-on-loss must not overwrite the durable awaiting
	// snapshot svc.Persist writes below. Two scripted turns: the initial ask,
	// then the post-resume completion.
	engine := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("w1", "Write", json.RawMessage(`{"path":"a.go"}`))),
			mockllm.TextTurn("done after approval"),
		),
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, ps),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:             engine,
		Store:              store,
		SessionLease:       lease,
		LeaseOwner:         "owner-test",
		LeaseTTL:           90 * time.Millisecond,
		LeaseRenewInterval: 15 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(svc.Close)

	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var askID string
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			askID = ev.Ask.AskID
			svc.Persist(context.Background(), sess.ID) // durable awaiting snapshot; marks runState.awaiting.
			break
		}
	}
	if askID == "" {
		t.Fatal("the run never raised a permission ask")
	}

	// The run stays genuinely parked (nothing resolves the ask); fail the next
	// renew so onLeaseLost's preserveAwaiting branch fires.
	var lost atomic.Bool
	lease.mu.Lock()
	lease.renewHook = func(port.Lease) (port.Lease, error) {
		lost.Store(true)
		return port.Lease{}, port.ErrLeaseHeld
	}
	lease.mu.Unlock()
	for range run.Events() { // drain the retract + cancel-driven terminal.
	}
	svc.FinishRun(sess.ID, run)
	if !lost.Load() {
		t.Fatal("precondition: the renewer never attempted a Renew")
	}
	if ran.Load() != 0 {
		t.Fatalf("Write executed %d time(s) pre-approval, want 0", ran.Load())
	}

	// Precondition: the durable snapshot is still the awaiting one svc.Persist
	// wrote (the parked run's cancel-driven terminal must not have overwritten
	// it — the engine has no Store), yet every ordinary caller fails fast on
	// the tombstone.
	reloaded, err := store.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.State != session.StateAwaiting {
		t.Fatalf("precondition: durable state = %q, want awaiting", reloaded.State)
	}
	if err := svc.Approve(context.Background(), sess.ID, askID, session.VerdictAllowOnce); !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("Approve before reconcile = %v, want ErrSessionLeasedElsewhere", err)
	}

	staleCtx := syscaller.Context(context.Background(), syscaller.RootStaleSessionReconcile)
	cleared, err := svc.ReconcileLeaseLossTombstone(staleCtx, sess.ID)
	if err != nil {
		t.Fatalf("ReconcileLeaseLossTombstone: %v", err)
	}
	if !cleared {
		t.Fatal("ReconcileLeaseLossTombstone = false, want true (the lease is genuinely free)")
	}

	// The tombstone (and the stale invalid heldLeases bookkeeping) is gone: the
	// pending ask resumes normally through the existing awaiting-resume seam.
	resumed, err := svc.ApproveRun(context.Background(), sess.ID, askID, session.VerdictAllowOnce, "")
	if err != nil {
		t.Fatalf("ApproveRun after ReconcileLeaseLossTombstone = %v, want success", err)
	}
	var stop session.StopReason
	for ev := range resumed.Events() {
		if ev.Type == session.EvResult && ev.Result != nil {
			stop = ev.Result.Stop
		}
	}
	svc.FinishRun(sess.ID, resumed)
	if ran.Load() != 1 {
		t.Fatalf("pending Write executed %d time(s) on resume, want exactly 1", ran.Load())
	}
	if stop != session.StopEndTurn {
		t.Fatalf("resumed run stop = %q, want %q", stop, session.StopEndTurn)
	}
}

// TestReconcileLeaseLossTombstoneRefusesWhenGenuinelyHeldElsewhere is the
// safety-critical negative counterpart: a genuine trial-Acquire failure (a
// real competitor, or a peer's not-yet-expired hold) must leave the tombstone
// in place — ReconcileLeaseLossTombstone must never clear it unconditionally.
func TestReconcileLeaseLossTombstoneRefusesWhenGenuinelyHeldElsewhere(t *testing.T) {
	lease := &fakeLease{}
	store := memstore.New()
	ps := permstore.New()
	cat := tool.NewCatalog()
	engine := agent.NewEngine(agent.Deps{
		LLM:     blockingProvider{},
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, ps),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:             engine,
		Store:              store,
		SessionLease:       lease,
		LeaseOwner:         "owner-test",
		LeaseTTL:           90 * time.Millisecond,
		LeaseRenewInterval: 15 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(svc.Close)

	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	var lost atomic.Bool
	lease.mu.Lock()
	lease.renewHook = func(port.Lease) (port.Lease, error) {
		lost.Store(true)
		return port.Lease{}, port.ErrLeaseHeld
	}
	lease.mu.Unlock()
	for range run.Events() {
	}
	svc.FinishRun(sess.ID, run)
	if !lost.Load() {
		t.Fatal("precondition: the renewer never lost the lease")
	}

	cancelled := session.New(sess.ID, session.ModeDefault, sess.EnvironmentRef, session.Limits{}, time.Unix(0, 0))
	if err := cancelled.RecordUserPrompt("go", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := cancelled.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := cancelled.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := store.Save(context.Background(), cancelled); err != nil {
		t.Fatalf("overwrite Save: %v", err)
	}

	// Unlike the recovery test, a REAL competitor now genuinely holds the
	// lease — the trial-Acquire must see that, not merely "expired".
	lease.mu.Lock()
	lease.acquireErr = port.ErrLeaseHeld
	lease.mu.Unlock()

	staleCtx := syscaller.Context(context.Background(), syscaller.RootStaleSessionReconcile)
	cleared, err := svc.ReconcileLeaseLossTombstone(staleCtx, sess.ID)
	if err != nil {
		t.Fatalf("ReconcileLeaseLossTombstone while genuinely held elsewhere: %v", err)
	}
	if cleared {
		t.Fatal("ReconcileLeaseLossTombstone while genuinely held elsewhere = true, want false")
	}

	if _, err := svc.StartRun(context.Background(), sess.ID, "again"); !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("StartRun after a refused reconcile = %v, want still ErrSessionLeasedElsewhere", err)
	}
}

// TestReconcileLeaseLossTombstoneFailSafeOnGenericError is the fail-safe
// negative case for a bare, non-sentinel trial-Acquire error — anything other
// than port.ErrLeaseHeld/port.ErrLeaseUnsupported. leaseTrial must treat
// ambiguity as "not free," never as license to clear the tombstone.
func TestReconcileLeaseLossTombstoneFailSafeOnGenericError(t *testing.T) {
	lease := &fakeLease{}
	store := memstore.New()
	ps := permstore.New()
	cat := tool.NewCatalog()
	engine := agent.NewEngine(agent.Deps{
		LLM:     blockingProvider{},
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, ps),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:             engine,
		Store:              store,
		SessionLease:       lease,
		LeaseOwner:         "owner-test",
		LeaseTTL:           90 * time.Millisecond,
		LeaseRenewInterval: 15 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(svc.Close)

	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	var lost atomic.Bool
	lease.mu.Lock()
	lease.renewHook = func(port.Lease) (port.Lease, error) {
		lost.Store(true)
		return port.Lease{}, port.ErrLeaseHeld
	}
	lease.mu.Unlock()
	for range run.Events() {
	}
	svc.FinishRun(sess.ID, run)
	if !lost.Load() {
		t.Fatal("precondition: the renewer never lost the lease")
	}

	cancelled := session.New(sess.ID, session.ModeDefault, sess.EnvironmentRef, session.Limits{}, time.Unix(0, 0))
	if err := cancelled.RecordUserPrompt("go", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := cancelled.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := cancelled.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := store.Save(context.Background(), cancelled); err != nil {
		t.Fatalf("overwrite Save: %v", err)
	}

	// A bare backend error — not one of the two lease sentinels — must be
	// treated as ambiguous, never as proof the lease is free.
	lease.mu.Lock()
	lease.acquireErr = errors.New("boom")
	lease.mu.Unlock()

	staleCtx := syscaller.Context(context.Background(), syscaller.RootStaleSessionReconcile)
	cleared, err := svc.ReconcileLeaseLossTombstone(staleCtx, sess.ID)
	if err != nil {
		t.Fatalf("ReconcileLeaseLossTombstone on a generic trial error: %v", err)
	}
	if cleared {
		t.Fatal("ReconcileLeaseLossTombstone on a generic trial error = true, want false (fail-safe)")
	}

	if _, err := svc.StartRun(context.Background(), sess.ID, "again"); !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("StartRun after a fail-safe-refused reconcile = %v, want still ErrSessionLeasedElsewhere", err)
	}
}
