package server_test

import (
	"context"
	"errors"
	"iter"
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
)

// fakeLease is a programmable port.SessionLease for the server-layer lease tests.
// Acquire counts and may fail; Renew/Release run scripted hooks. It is concurrency
// safe so the renewer goroutine and the test can poke it together.
type fakeLease struct {
	mu sync.Mutex

	acquireErr    error
	acquires      int
	acquireExpiry time.Time // expiry the next Acquire grants (zero = time.Now()+1h)

	renewHook    func(port.Lease) (port.Lease, error)
	releaseHook  func(port.Lease) error
	releases     int
	lastReleased port.Lease // the lease value handed to the last Release call
}

func (f *fakeLease) Acquire(_ context.Context, id session.SessionID, owner string) (port.Lease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquires++
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

func (f *fakeLease) Release(_ context.Context, l port.Lease) error {
	f.mu.Lock()
	f.releases++
	f.lastReleased = l
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
