package agenthook

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// blockingRunner is a dispatch seam that parks every delivery until released.
// It makes the worker's boundary properties observable: while it is parked the
// worker cannot drain, so producers are exercised against a genuinely full
// queue rather than a lucky race.
type blockingRunner struct {
	release chan struct{} // closed to let every parked delivery finish

	mu       sync.Mutex
	started  int
	payloads []string
	ctxs     []context.Context
}

func newBlockingRunner() *blockingRunner {
	return &blockingRunner{release: make(chan struct{})}
}

func (b *blockingRunner) run(ctx context.Context, _ string, _ []string, payload string) {
	b.mu.Lock()
	b.started++
	b.payloads = append(b.payloads, payload)
	b.ctxs = append(b.ctxs, ctx)
	b.mu.Unlock()
	select {
	case <-b.release:
	case <-ctx.Done():
	}
}

func (b *blockingRunner) delivered() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.payloads...)
}

func (b *blockingRunner) firstCtx(t *testing.T) context.Context {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		if len(b.ctxs) > 0 {
			c := b.ctxs[0]
			b.mu.Unlock()
			return c
		}
		b.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no delivery was ever attempted")
	return nil
}

func notifierWith(run runnerFunc, timeout time.Duration) *Notifier {
	return &Notifier{script: "/nonexistent", run: run, timeout: timeout}
}

// TestProducersNeverBlockOnAStalledWorker is the load-bearing property: these
// events are emitted from the UI reducer, so a wedged hook command must never
// back-pressure the agent loop. Far more events than the queue holds are
// enqueued while the single worker is parked mid-delivery.
func TestProducersNeverBlockOnAStalledWorker(t *testing.T) {
	b := newBlockingRunner()
	defer close(b.release)
	n := notifierWith(b.run, time.Minute)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range queueDepth * 3 {
			n.PermissionRequest(context.Background(), "s", strconv.Itoa(i))
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("enqueue blocked behind a stalled worker; it must always drop instead")
	}
}

// TestFullQueueDropsOldest pins the documented saturation policy. With the
// worker parked, the buffer fills and the OLDEST pending events are discarded,
// so what eventually reaches the host is the most RECENT lifecycle state — the
// opposite choice (dropping the newest) would strand the host on stale status.
func TestFullQueueDropsOldest(t *testing.T) {
	b := newBlockingRunner()
	n := notifierWith(b.run, time.Minute)

	// One event enters the worker immediately; the rest contend for the buffer.
	total := queueDepth * 2
	for i := range total {
		n.PermissionRequest(context.Background(), "s", strconv.Itoa(i))
	}
	close(b.release)

	// Drain deterministically rather than sleeping on a guess.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	n.Close(ctx)

	got := b.delivered()
	if len(got) > queueDepth+1 {
		t.Fatalf("queue is unbounded: delivered %d of %d with depth %d", len(got), total, queueDepth)
	}
	if len(got) < 2 {
		t.Fatalf("expected the worker to deliver the surviving events, got %d", len(got))
	}
	// The final event must survive: it is the freshest state.
	last := decode(t, got[len(got)-1])
	if last.Message != strconv.Itoa(total-1) {
		t.Errorf("newest event was dropped: last delivered %q, want %q", last.Message, strconv.Itoa(total-1))
	}
	// And an early one must have been dropped, proving oldest-first eviction.
	first := decode(t, got[1])
	if first.Message == "1" {
		t.Errorf("oldest events were kept; expected them evicted: %q", got[1])
	}
}

// TestDeliveryContextIsDetachedFromCaller proves the cancel-detached contract.
// The terminal event fires exactly as the run's context is torn down, so a
// delivery honouring the caller's context would abort the completion
// notification it exists to send. It must instead be bounded by its own timeout.
func TestDeliveryContextIsDetachedFromCaller(t *testing.T) {
	b := newBlockingRunner()
	defer close(b.release)
	n := notifierWith(b.run, time.Minute)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel() // the run is already torn down

	n.Start(cancelled, "sess-1")

	ctx := b.firstCtx(t)
	if ctx.Err() != nil {
		t.Fatalf("delivery inherited the caller's cancellation: %v", ctx.Err())
	}
	if _, ok := ctx.Deadline(); !ok {
		t.Error("a detached delivery must still be bounded by its own deadline")
	}
}

// TestDeliveryIsIndependentlyBounded proves each invocation carries its own
// timeout, so one wedged hook command cannot wedge the worker forever.
func TestDeliveryIsIndependentlyBounded(t *testing.T) {
	b := newBlockingRunner()
	defer close(b.release)
	n := notifierWith(b.run, 60*time.Millisecond)

	n.Start(context.Background(), "s")
	n.Stop(context.Background(), "s", false, "done")

	// If the per-delivery bound did not fire, the second event never starts.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(b.delivered()) >= 2 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("a wedged delivery blocked the worker; each one must be separately bounded")
}

// TestCloseDrainsQueuedDeliveries proves Close is an owned shutdown, not just a
// cancel: work already accepted is delivered before it returns.
func TestCloseDrainsQueuedDeliveries(t *testing.T) {
	r := newRecorder(2)
	n := newTestNotifier(r)
	n.Start(context.Background(), "s")
	n.Stop(context.Background(), "s", false, "bye")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	n.Close(ctx)

	if got := len(r.snapshot()); got != 2 {
		t.Fatalf("Close must drain accepted work before returning, delivered %d/2", got)
	}
}

// TestCloseIsBoundedByAWedgedDelivery proves the drain cannot hold up process
// exit or a /connect restart: a hook command that never returns costs the bound,
// not forever.
func TestCloseIsBoundedByAWedgedDelivery(t *testing.T) {
	b := newBlockingRunner()
	defer close(b.release)
	n := notifierWith(b.run, time.Minute) // per-delivery bound far exceeds Close's

	n.Start(context.Background(), "s")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	n.Close(ctx)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Close waited %v on a wedged delivery; it must abandon at its bound", elapsed)
	}
}

// TestNoDeliveryCrossesTheCloseBoundary is the generation-boundary guarantee.
// mecatui can restart in-process (/connect), building a SUCCESSOR notifier. A
// retired generation must be inert afterwards: otherwise its late terminal could
// land after the successor's busy signal and mark the host idle mid-run.
func TestNoDeliveryCrossesTheCloseBoundary(t *testing.T) {
	r := newRecorder(4)
	n := newTestNotifier(r)
	n.Start(context.Background(), "gen-1")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	n.Close(ctx)
	before := len(r.snapshot())

	// Everything the retired generation could still try to say.
	n.Start(context.Background(), "gen-1")
	n.Stop(context.Background(), "gen-1", true, "late terminal")
	n.PermissionRequest(context.Background(), "gen-1", "late ask")

	// Give a (wrongly) resurrected worker time to deliver.
	time.Sleep(100 * time.Millisecond)
	if got := len(r.snapshot()); got != before {
		t.Fatalf("a closed notifier delivered %d extra event(s); it must be inert past the boundary", got-before)
	}
}

// TestCloseIsIdempotentAndSafeWhenUnused covers the shapes shutdown actually
// hits: a notifier that never emitted, a repeated Close, and the nil receiver.
func TestCloseIsIdempotentAndSafeWhenUnused(_ *testing.T) {
	ctx := context.Background()

	var nilNotifier *Notifier
	nilNotifier.Close(ctx) // must not panic

	r := newRecorder(1)
	unused := newTestNotifier(r)
	unused.Close(ctx) // no worker was ever started
	unused.Close(ctx) // idempotent

	used := newTestNotifier(newRecorder(1))
	used.Start(ctx, "s")
	used.Close(ctx)
	used.Close(ctx)
}

// TestRunnerKillsDescendantsOnTimeout proves the containment claim the package
// makes. exec kills only the script; a hook that spawns a helper (a backgrounded
// curl, a sleep) would otherwise keep running past the advertised bound, and
// repeated deliveries would accumulate orphans.
//
// The kill is deliberately withheld until the descendant provably exists. An
// earlier version of this test fired a fixed 200ms timeout instead and passed
// with containment REMOVED — the shell simply had not forked the descendant
// yet, so it asserted nothing. Waiting on the started marker removes that race
// and makes the test fail without procgroup.Configure.
func TestRunnerKillsDescendantsOnTimeout(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "descendant-started")
	survived := filepath.Join(dir, "descendant-survived")
	script := filepath.Join(dir, "notify.sh")
	// Background a helper that outlives its parent on purpose: it announces
	// itself, waits past the kill, then records that it was never stopped.
	body := "#!/bin/sh\n" +
		"( : > " + started + "; sleep 3; : > " + survived + " ) &\n" +
		"sleep 30\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defaultRunner(ctx, script, nil, buildPayload(EventStop, "s", ""))
	}()

	waitForFile(t, started, "descendant never started; the test would assert nothing")
	cancel() // now the timeout equivalent fires, with the descendant definitely alive

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runner did not return after cancellation")
	}

	// Well past the descendant's own sleep: if it was not contained, it wrote.
	time.Sleep(4 * time.Second)
	if _, err := os.Stat(survived); err == nil {
		t.Fatal("hook descendant outlived the bound; the process group must be killed")
	}
}

func waitForFile(t *testing.T, path, msg string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}

// TestConcurrentProducersAndCloseAreRaceFree exercises the lock discipline
// between producers and the shutdown boundary under -race.
func TestConcurrentProducersAndCloseAreRaceFree(_ *testing.T) {
	var delivered atomic.Int64
	n := notifierWith(func(context.Context, string, []string, string) { delivered.Add(1) }, time.Second)

	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n.PermissionRequest(context.Background(), "s", strconv.Itoa(i))
		}()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n.Close(ctx)
	wg.Wait()
	n.Close(ctx)
}
