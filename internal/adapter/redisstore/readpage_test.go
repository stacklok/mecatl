package redisstore

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// cmdSpy records the arguments of every command the store issues.
//
// A go-redis hook is used rather than inspecting miniredis because the assertion
// is about what mecatl SENDS, not about what the server does with it. miniredis
// answers an uncapped XREAD perfectly happily, which is precisely why the
// unbounded fetch was invisible to every existing test.
type cmdSpy struct {
	mu   sync.Mutex
	sent [][]any
}

func (*cmdSpy) DialHook(next redis.DialHook) redis.DialHook { return next }

func (*cmdSpy) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func (s *cmdSpy) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		s.mu.Lock()
		s.sent = append(s.sent, cmd.Args())
		s.mu.Unlock()
		return next(ctx, cmd)
	}
}

// matching returns the recorded argument lists whose command name is name.
func (s *cmdSpy) matching(name string) [][]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out [][]any
	for _, args := range s.sent {
		if len(args) > 0 && strings.EqualFold(fmt.Sprint(args[0]), name) {
			out = append(out, args)
		}
	}
	return out
}

// countArg reports the COUNT value in one command's arguments.
func countArg(args []any) (string, bool) {
	for i, a := range args {
		if strings.EqualFold(fmt.Sprint(a), "count") && i+1 < len(args) {
			return fmt.Sprint(args[i+1]), true
		}
	}
	return "", false
}

func spyStore(t *testing.T) *Store {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	st, err := New(mr.Addr())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestUnboundedLimitStillBoundsTheFetch pins the contract that ReadOptions.Limit
// == 0 bounds the SEQUENCE, not the storage round trip.
//
// The natural implementation passes the caller's zero straight through to XREAD,
// which omits COUNT entirely and has Redis return every entry in one reply — so a
// watcher attaching to a long transcript makes the server materialise the whole
// log and go-redis decode it before a single record is yielded. Nothing about the
// records the consumer receives differs, which is why this cannot be a
// conformance case: internal paging is invisible through iter.Seq2, so it has to
// be asserted at the wire. Raised in review on #868.
func TestUnboundedLimitStillBoundsTheFetch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := spyStore(t)
	const id session.SessionID = "readpage-unbounded"

	if _, err := st.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Text: "one"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	spy := &cmdSpy{}
	st.testClient().AddHook(spy)
	for _, err := range st.ReadAfter(ctx, id, "", port.ReadOptions{}) {
		if err != nil {
			t.Fatalf("ReadAfter: %v", err)
		}
	}

	reads := spy.matching("xread")
	if len(reads) == 0 {
		t.Fatal("no XREAD was issued; the spy is not observing the read path")
	}
	for _, args := range reads {
		count, present := countArg(args)
		if !present {
			t.Errorf("XREAD issued with no COUNT (%v) — an unbounded Limit must still bound the fetch", args)
			continue
		}
		if want := fmt.Sprint(readPageSize); count != want {
			t.Errorf("XREAD COUNT = %s, want %s (readPageSize)", count, want)
		}
	}
}

// TestBoundedLimitNeverOverFetches checks the other direction: a caller asking
// for fewer records than a page must not have a whole page pulled for it.
func TestBoundedLimitNeverOverFetches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := spyStore(t)
	const id session.SessionID = "readpage-bounded"
	for i := range 4 {
		if _, err := st.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: int64(i)}); err != nil {
			t.Fatalf("AppendEvent(%d): %v", i, err)
		}
	}

	spy := &cmdSpy{}
	st.testClient().AddHook(spy)
	var got int
	for _, err := range st.ReadAfter(ctx, id, "", port.ReadOptions{Limit: 2}) {
		if err != nil {
			t.Fatalf("ReadAfter: %v", err)
		}
		got++
	}
	if got != 2 {
		t.Fatalf("Limit 2 yielded %d records, want 2", got)
	}
	for _, args := range spy.matching("xread") {
		if count, present := countArg(args); !present || count != "2" {
			t.Errorf("XREAD COUNT = %q (present=%v), want 2 — a bounded read must not over-fetch", count, present)
		}
	}
}

// TestPagingCrossesPageBoundariesWithoutLoss is the correctness half of the page
// cap: capping each round trip is only safe if the loop keeps going.
//
// It writes more records than one page holds and asserts every one arrives, in
// order, exactly once — the failure a naive cap introduces is silent truncation
// at exactly readPageSize, which no smaller fixture can see.
func TestPagingCrossesPageBoundariesWithoutLoss(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := spyStore(t)
	const id session.SessionID = "readpage-crossing"

	const total = readPageSize + 5
	for i := range total {
		if _, err := st.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: int64(i), Text: fmt.Sprintf("e%d", i)}); err != nil {
			t.Fatalf("AppendEvent(%d): %v", i, err)
		}
	}

	var texts []string
	for rec, err := range st.ReadAfter(ctx, id, "", port.ReadOptions{}) {
		if err != nil {
			t.Fatalf("ReadAfter: %v", err)
		}
		texts = append(texts, rec.Event.Text)
	}
	if len(texts) != total {
		t.Fatalf("read %d records across a page boundary, want %d — the page cap truncated the sequence", len(texts), total)
	}
	for i, text := range texts {
		if want := fmt.Sprintf("e%d", i); text != want {
			t.Fatalf("record %d = %q, want %q — paging reordered or duplicated", i, text, want)
		}
	}
}

// closeDiag records shutdown diagnostics so a test can assert on the WARN
// Store.Close emits when it gives up with operations still outstanding.
type closeDiag struct {
	mu       sync.Mutex
	timedOut bool
}

func (d *closeDiag) With(...any) port.Diagnostics { return d }

func (d *closeDiag) Log(_ context.Context, _ port.Level, msg string, args ...any) {
	if msg != "redis store shutdown" {
		return
	}
	for i := 0; i+1 < len(args); i += 2 {
		if fmt.Sprint(args[i]) == "outcome" && fmt.Sprint(args[i+1]) == "timed_out" {
			d.mu.Lock()
			d.timedOut = true
			d.mu.Unlock()
		}
	}
}

// TestCloseDoesNotWaitOnAParkedFollower pins a property the per-cycle lease buys.
//
// When ReadAfter held one lease for the life of its iterator, a parked follower
// kept a credential generation's ref count non-zero. clientGenerations will not
// force-close a client with live leases, so Close spent its entire grace period
// waiting and then logged "timed_out" with the client still open — for as long as
// anyone was watching, which for a follower is the point. Acquiring per cycle
// bounds that to one cycle: the lease is released between reads, so Close
// completes promptly with nothing outstanding.
//
// The assertion is the SHUTDOWN DIAGNOSTIC rather than a wall-clock timeout,
// because Close returns nil either way and always returns within the grace — a
// generous timeout therefore passes with the lease held and proves nothing (which
// is exactly what the first version of this test did).
//
// Store.Close now also owns and cancels followers. This older regression remains
// useful because the per-cycle lease independently ensures a parked follower
// cannot pin a retired credential generation while it crosses read cycles.
func TestCloseDoesNotWaitOnAParkedFollower(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	st, err := New(mr.Addr())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	diag := &closeDiag{}
	st.diagnostics = diag

	const id session.SessionID = "close-parked-follower"
	if _, err := st.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Text: "one"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	// Park a follower: it delivers the existing record, then blocks at the tail.
	drained := make(chan struct{})
	go func() {
		var once bool
		for _, err := range st.ReadAfter(ctx, id, "", port.ReadOptions{Follow: true}) {
			if !once {
				once = true
				close(drained)
			}
			if err != nil {
				return
			}
		}
	}()
	select {
	case <-drained:
	case <-time.After(30 * time.Second):
		t.Fatal("follower never reached the follow loop")
	}
	// Let it get inside a blocking read rather than between cycles.
	time.Sleep(50 * time.Millisecond)

	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	diag.mu.Lock()
	timedOut := diag.timedOut
	diag.mu.Unlock()
	if timedOut {
		t.Error("Store.Close timed out with operations outstanding while a follower was parked; the follow lease is pinning a client open for the follower's lifetime")
	}
}
