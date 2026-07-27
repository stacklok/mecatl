package app

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// captureDiag is a minimal port.Diagnostics that records Log calls so a test can
// assert the bounded-backlog WARN fired (and only at the right level). It is the
// non-vacuous oracle for the "drops the OLDEST with a WARN" assertion: a queue
// that silently dropped (no WARN) or dropped at Info would fail here.
type captureDiag struct {
	entries []diagEntry
}

type diagEntry struct {
	level port.Level
	msg   string
	args  []any
}

func (c *captureDiag) Log(_ context.Context, level port.Level, msg string, args ...any) {
	c.entries = append(c.entries, diagEntry{level: level, msg: msg, args: args})
}

func (c *captureDiag) With(_ ...any) port.Diagnostics { return c }

var _ port.Diagnostics = (*captureDiag)(nil)

func (c *captureDiag) warnCount(substr string) int {
	n := 0
	for _, e := range c.entries {
		if e.level == port.LevelWarn && strings.Contains(e.msg, substr) {
			n++
		}
	}
	return n
}

// pendingTexts is a helper that pulls the Text field out of a note slice in
// enqueue order, so a test can assert the drained sequence without re-stating
// the struct shape.
func pendingTexts(notes []port.DeliveryNote) []string {
	out := make([]string, 0, len(notes))
	for _, n := range notes {
		out = append(out, n.Text)
	}
	return out
}

// TestFireDelivery_Scenario4_DrainedExactlyOnceAcrossRuns pins AC4.2's ledger
// half (the durable session-scoped ledger this task owns): a note queued in
// "run N" is still pending — and drains exactly once — in "run N+1" when run N
// ends first. The queue is session-scoped, NOT run-scoped: the per-Run
// noticed/delivered registry dies with its run, but this ledger survives it.
//
// "Run N ends first" is modelled by constructing a FRESH queue over the same
// durable dir between the enqueue and the drain — the run (and its in-process
// registry) is gone; only the durable session-scoped ledger remains. The
// exactly-once property: MarkDelivered removes the note from the pending set,
// and a second drain on the SAME seq re-delivers nothing.
func TestFireDelivery_Scenario4_DrainedExactlyOnceAcrossRuns(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// "run N": a queue over the durable dir enqueues one note for origin s1.
	qN, err := NewFileDeliveryQueue(dir, WithDeliveryDiagnostics(&captureDiag{}))
	if err != nil {
		t.Fatalf("NewFileDeliveryQueue (run N): %v", err)
	}
	origin := session.SessionID("s1")
	note, err := qN.Enqueue(ctx, origin, "fire-1 reported: done")
	if err != nil {
		t.Fatalf("Enqueue (run N): %v", err)
	}
	if note.Seq != 1 {
		t.Fatalf("first note Seq = %d, want 1 (monotonic per-session ledger)", note.Seq)
	}
	// run N ENDS — drop the in-process handle. The durable ledger is all that
	// survives.
	qN.Close()

	// "run N+1": a brand-new queue over the SAME durable dir. The note queued
	// in run N must still be pending (the ledger is session-scoped, not
	// run-scoped).
	qN1, err := NewFileDeliveryQueue(dir, WithDeliveryDiagnostics(&captureDiag{}))
	if err != nil {
		t.Fatalf("NewFileDeliveryQueue (run N+1): %v", err)
	}
	defer qN1.Close()
	pending, err := qN1.Pending(ctx, origin)
	if err != nil {
		t.Fatalf("Pending (run N+1): %v", err)
	}
	if len(pending) != 1 || pending[0].Text != "fire-1 reported: done" {
		t.Fatalf("run N+1 pending = %+v, want the one note queued in run N (ledger survived the run)", pending)
	}
	if pending[0].Seq != note.Seq {
		t.Fatalf("run N+1 pending Seq = %d, want %d (the same ledger seq, not a re-mint)", pending[0].Seq, note.Seq)
	}

	// Drain exactly once: MarkDelivered removes it; a second MarkDelivered is a
	// no-op idempotent re-mark; and a re-Pending shows nothing left.
	if err := qN1.MarkDelivered(ctx, origin, note.Seq); err != nil {
		t.Fatalf("MarkDelivered: %v", err)
	}
	if err := qN1.MarkDelivered(ctx, origin, note.Seq); err != nil {
		t.Fatalf("MarkDelivered (idempotent re-mark): %v", err)
	}
	after, err := qN1.Pending(ctx, origin)
	if err != nil {
		t.Fatalf("Pending after drain: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("after MarkDelivered, pending = %d, want 0 (exactly-once: the note is gone, not re-deliverable)", len(after))
	}
}

// TestFireDelivery_Scenario4_MultiplePendingAllDrained pins AC4.3's backlog
// half (this task owns): multiple pending notes accumulate in enqueue order
// (no coalescing), and a bounded backlog cap drops the OLDEST pending note
// with a WARN rather than growing unboundedly on an overloaded origin.
//
// The drain-side "each is drained, no coalescing" is task 05's AC4.3; this
// test owns the queue half: accumulation in order, and the bounded drop.
func TestFireDelivery_Scenario4_MultiplePendingAllDrained(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	diag := &captureDiag{}
	// A tight cap so the drop is observable without enqueuing thousands.
	q, err := NewFileDeliveryQueue(dir, WithDeliveryBacklogCap(3), WithDeliveryDiagnostics(diag))
	if err != nil {
		t.Fatalf("NewFileDeliveryQueue: %v", err)
	}
	defer q.Close()
	origin := session.SessionID("s2")

	// Enqueue four notes; the cap is 3, so the OLDEST (seq 1) is dropped with a
	// WARN. The remaining three are kept in enqueue order — no coalescing.
	var seqs []uint64
	for i, body := range []string{"n1", "n2", "n3", "n4"} {
		n, err := q.Enqueue(ctx, origin, body)
		if err != nil {
			t.Fatalf("Enqueue #%d: %v", i, err)
		}
		seqs = append(seqs, n.Seq)
	}
	// Monotonic per-session sequence (the exactly-once ledger key), assigned
	// across the dropped note too — seq 1 was assigned to n1 before it was
	// dropped, so n4 carries seq 4.
	if got, want := seqs, []uint64{1, 2, 3, 4}; !equalSeqsDQ(got, want) {
		t.Fatalf("seqs = %v, want %v (monotonic across the dropped note)", got, want)
	}
	pending, err := q.Pending(ctx, origin)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	got := pendingTexts(pending)
	want := []string{"n2", "n3", "n4"} // n1 (the oldest) was dropped by the cap
	if !equalStringsDQ(got, want) {
		t.Fatalf("pending after cap drop = %v, want %v (oldest dropped, rest in order, no coalescing)", got, want)
	}
	// The drop emitted EXACTLY one WARN (not an Info, not silent). A silent
	// drop or an Info-level drop fails this — the overload must be visible.
	if n := diag.warnCount("delivery"); n != 1 {
		t.Fatalf("backlog-drop WARN count = %d, want 1 (the oldest dropped with a WARN, not silent, not a lower level)", n)
	}
}

// TestFireDelivery_Scenario4_PendingQueueSurvivesRestart pins AC4.5: the
// pending-delivery queue is DURABLE (persist-in-snapshot List 2 decision). A
// process restart with notes still pending drains them on the origin's next
// run-entry — the queue does not lose them. This is the two-Build drill shape
// (the approve-after-restart precedent): enqueue under one queue handle, drop
// it (process death), construct a fresh handle over the same durable dir, and
// the pending notes are still there.
func TestFireDelivery_Scenario4_PendingQueueSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	origin := session.SessionID("s3")

	// Build #1: enqueue two notes for the origin. Do NOT drain.
	q1, err := NewFileDeliveryQueue(dir)
	if err != nil {
		t.Fatalf("NewFileDeliveryQueue #1: %v", err)
	}
	for _, body := range []string{"restart-survivor-a", "restart-survivor-b"} {
		if _, err := q1.Enqueue(ctx, origin, body); err != nil {
			t.Fatalf("Enqueue #1: %v", err)
		}
	}
	// Sanity: both are pending in-process before the "restart".
	if pre, _ := q1.Pending(ctx, origin); len(pre) != 2 {
		t.Fatalf("pre-restart pending = %d, want 2", len(pre))
	}
	q1.Close() // process death: the in-process handle is gone.

	// Build #2: a brand-new queue over the SAME durable dir. The notes are
	// still pending — the queue survived the restart (persist-in-snapshot).
	q2, err := NewFileDeliveryQueue(dir)
	if err != nil {
		t.Fatalf("NewFileDeliveryQueue #2: %v", err)
	}
	defer q2.Close()
	pending, err := q2.Pending(ctx, origin)
	if err != nil {
		t.Fatalf("Pending after restart: %v", err)
	}
	if got, want := pendingTexts(pending), []string{"restart-survivor-a", "restart-survivor-b"}; !equalStringsDQ(got, want) {
		t.Fatalf("after restart pending = %v, want %v (durable: notes survived restart)", got, want)
	}
	// And the seqs survived too (the ledger is the same, not re-minted) — so a
	// drain in the restarted process records exactly-once against the original
	// ledger.
	if pending[0].Seq != 1 || pending[1].Seq != 2 {
		t.Fatalf("after restart seqs = %d,%d, want 1,2 (the same ledger, not re-minted)", pending[0].Seq, pending[1].Seq)
	}
	// Draining in the restarted process works and clears the queue.
	if err := q2.MarkDelivered(ctx, origin, pending[0].Seq); err != nil {
		t.Fatalf("MarkDelivered #1 after restart: %v", err)
	}
	if err := q2.MarkDelivered(ctx, origin, pending[1].Seq); err != nil {
		t.Fatalf("MarkDelivered #2 after restart: %v", err)
	}
	if after, _ := q2.Pending(ctx, origin); len(after) != 0 {
		t.Fatalf("after post-restart drain, pending = %d, want 0", len(after))
	}
}

// TestDeliveryQueue_NopIsNoDeliveryDefault pins the "byte-identical to a
// no-delivery path" invariant for the memstore default: a Nop queue drops
// Enqueue and returns empty from Pending, so a deployment with no delivery
// wired sees no notes — the same shape as if the feature did not exist. A
// mutation that made Nop start buffering (silently holding notes) would fail.
func TestDeliveryQueue_NopIsNoDeliveryDefault(t *testing.T) {
	ctx := context.Background()
	q := port.NopDeliveryQueue{}
	origin := session.SessionID("s-nop")
	if _, err := q.Enqueue(ctx, origin, "anything"); err != nil {
		t.Fatalf("Nop Enqueue err = %v, want nil (a no-op drop, not an error)", err)
	}
	pending, err := q.Pending(ctx, origin)
	if err != nil {
		t.Fatalf("Nop Pending err = %v, want nil", err)
	}
	if len(pending) != 0 {
		t.Fatalf("Nop pending = %d, want 0 (byte-identical no-delivery path: nothing is buffered)", len(pending))
	}
	if err := q.MarkDelivered(ctx, origin, 42); err != nil {
		t.Fatalf("Nop MarkDelivered err = %v, want nil (a no-op)", err)
	}
}

// TestDeliveryQueue_InMemoryLostOnRestart pins the honest-degradation invariant:
// the in-memory (memstore-tier) queue works in-process but does NOT survive a
// restart — it degrades honestly to empty, byte-identical to the no-delivery
// path for a restarted process. A mutation that silently persisted in-memory
// state (pretending durability it does not have) would fail.
func TestDeliveryQueue_InMemoryLostOnRestart(t *testing.T) {
	ctx := context.Background()
	q1 := NewInMemoryDeliveryQueue()
	origin := session.SessionID("s-mem")
	if _, err := q1.Enqueue(ctx, origin, "ephemeral"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if pre, _ := q1.Pending(ctx, origin); len(pre) != 1 {
		t.Fatalf("in-memory pre-restart pending = %d, want 1", len(pre))
	}
	// "Restart": a fresh in-memory queue. Nothing carries over — honest
	// degradation, not silent persistence.
	q2 := NewInMemoryDeliveryQueue()
	if after, _ := q2.Pending(ctx, origin); len(after) != 0 {
		t.Fatalf("in-memory post-restart pending = %d, want 0 (honest degradation: in-memory does not survive restart)", len(after))
	}
}

// equalSeqsDQ / equalStringsDQ are small slice equality helpers kept local so
// the test bodies read as the invariant they assert. The DQ suffix avoids a
// name collision with the package's existing equalStrings helper.
func equalSeqsDQ(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStringsDQ(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestDeliveryQueue_FileBacklogCapZero pins the cap's disable semantics: a
// cap of 0 means UNBOUNDED (no drop), not "drop everything". This is the
// honest reading of "bounded backlog cap" — an operator who sets 0 opts out of
// bounding, matching the MaxNoProgressNudges<0-disables idiom.
func TestDeliveryQueue_FileBacklogCapZero(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	q, err := NewFileDeliveryQueue(dir, WithDeliveryBacklogCap(0))
	if err != nil {
		t.Fatalf("NewFileDeliveryQueue: %v", err)
	}
	defer q.Close()
	origin := session.SessionID("s-cap0")
	for i := 0; i < 5; i++ {
		if _, err := q.Enqueue(ctx, origin, "n"); err != nil {
			t.Fatalf("Enqueue #%d: %v", i, err)
		}
	}
	pending, err := q.Pending(ctx, origin)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 5 {
		t.Fatalf("cap=0 pending = %d, want 5 (0 = unbounded, not drop-all)", len(pending))
	}
}

// TestDeliveryQueue_FileSeqsPerSessionIsolated pins that the monotonic seq is
// per-SESSION (the ledger key), so two origins each get their own 1,2,3...
// sequence — a drain on one origin never touches the other's ledger.
func TestDeliveryQueue_FileSeqsPerSessionIsolated(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	q, err := NewFileDeliveryQueue(dir)
	if err != nil {
		t.Fatalf("NewFileDeliveryQueue: %v", err)
	}
	defer q.Close()
	a := session.SessionID("s-iso-a")
	b := session.SessionID("s-iso-b")
	na, _ := q.Enqueue(ctx, a, "a1")
	nb, _ := q.Enqueue(ctx, b, "b1")
	na2, _ := q.Enqueue(ctx, a, "a2")
	if na.Seq != 1 || na2.Seq != 2 {
		t.Fatalf("origin a seqs = %d,%d, want 1,2", na.Seq, na2.Seq)
	}
	if nb.Seq != 1 {
		t.Fatalf("origin b seq = %d, want 1 (per-session isolated ledger)", nb.Seq)
	}
	pa, _ := q.Pending(ctx, a)
	if len(pa) != 2 {
		t.Fatalf("origin a pending = %d, want 2 (isolated from b)", len(pa))
	}
	pb, _ := q.Pending(ctx, b)
	if len(pb) != 1 {
		t.Fatalf("origin b pending = %d, want 1 (isolated from a)", len(pb))
	}
	// Draining a's seq 1 does NOT touch b's pending.
	_ = q.MarkDelivered(ctx, a, na.Seq)
	pb2, _ := q.Pending(ctx, b)
	if len(pb2) != 1 {
		t.Fatalf("after draining a, origin b pending = %d, want 1 (ledgers are isolated)", len(pb2))
	}
}

// (No trailing imports to keep honest — the test uses only context, strings,
// testing, port, and session.)
