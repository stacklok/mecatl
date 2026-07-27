package port

import (
	"context"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

// DeliveryQueue is the DURABLE per-session pending-delivery queue for
// scheduled-task fire results (ADR 0075, fire-result-delivery Scenario 4). It is
// keyed on the ORIGIN session id — the session that created the schedule — NOT
// on a per-Run registry (which dies with its run). A note queued during one run
// is drained on the origin's NEXT run-entry if the current run ends first: the
// exactly-once ledger is SESSION-scoped.
//
// The queue holds the opaque RENDERED note text (produced by the delivery
// renderer, task 03) plus a monotonic per-session sequence that is the
// exactly-once ledger key. It does NOT call the renderer and does NOT know how
// the text was produced; it is a data structure, not a rendering surface.
//
// DURABILITY: a durable backing (the same durability the session snapshot has,
// e.g. a JSONL sidecar under the store dir) survives a process restart so a
// note queued before a restart drains after it. A backing with no persistence
// (the in-memory default) degrades honestly — in-process it works, across a
// restart it is empty (byte-identical to a no-delivery path for the restarted
// process). NopDeliveryQueue is the byte-identical no-delivery default: a
// deployment with delivery unwired sees nothing.
//
// The loop is storage-agnostic and stays so: the fire path (composition) ENQUEUES
// to it, and the loop's turn-boundary drain + the run-entry funnel DEQUEUE from
// it via this port. Implementations live in composition/adapters, never in
// engine/agent.
//
// CONCURRENCY: implementations MUST be safe for concurrent Enqueue/Pending/
// MarkDelivered across session ids — the fire path and the origin's drain may
// run on different goroutines. Per-id order need only be well-defined for a
// SINGLE session's enqueues, which the implementation serialises.
type DeliveryQueue interface {
	// Enqueue appends a rendered note for the origin session, assigning the
	// next monotonic per-session seq. It returns the assigned note (the caller
	// may surface its seq to the fire path). When the pending backlog for the
	// origin exceeds the configured cap, the OLDEST pending note is DROPPED
	// (with a WARN via the injected Diagnostics) rather than growing
	// unboundedly on an overloaded origin; the cap 0 means UNBOUNDED (no drop).
	// Enqueue is NOT idempotent: each call mints a fresh seq and a fresh note.
	Enqueue(ctx context.Context, origin session.SessionID, text string) (DeliveryNote, error)

	// Pending returns the origin's not-yet-delivered notes in ENQUEUE order
	// (oldest first). The drain reads this at the origin's next turn boundary
	// / run-entry and records each via MarkDelivered. A miss (no notes for the
	// origin) returns an empty slice, not an error: absence is data.
	Pending(ctx context.Context, origin session.SessionID) ([]DeliveryNote, error)

	// MarkDelivered records that seq was delivered to the origin, removing it
	// from the pending set. It is IDEMPOTENT: a re-mark of an already-delivered
	// (or unknown) seq is a no-op success, never an error. This is the
	// exactly-once ledger: a seq marked delivered is never re-delivered.
	MarkDelivered(ctx context.Context, origin session.SessionID, seq uint64) error
}

// DeliveryNote is one pending-delivery entry: the opaque rendered note text plus
// the monotonic per-session sequence that is the exactly-once ledger key. The
// text is ALREADY rendered (fenced-untrusted, clamped) by the time it arrives
// here; the queue stores it verbatim and does not re-render.
type DeliveryNote struct {
	// Seq is the monotonic per-session sequence assigned at Enqueue time. It is
	// the exactly-once ledger key: the drain records MarkDelivered(origin, seq)
	// so a note is delivered exactly once, and the seq survives the run it was
	// queued during (the ledger is session-scoped).
	Seq uint64
	// SessionID is the ORIGIN session the note is destined for (the queue's key).
	SessionID session.SessionID
	// Text is the opaque rendered note (the renderer's output, stored verbatim).
	Text string
	// EnqueuedAt is when the note was enqueued (for diagnostics/backlog
	// observability; NOT a ledger key).
	EnqueuedAt time.Time
}

// NopDeliveryQueue is the byte-identical no-delivery default: Enqueue drops,
// Pending returns empty, MarkDelivered is a no-op. It is the queue a deployment
// with delivery unwired sees — the same shape as if the feature did not exist.
// Its zero value is usable, so it is the safe default when nothing is injected.
type NopDeliveryQueue struct{}

// Enqueue drops the note and returns a zero-seq note. It never errors: the
// fire path treats a nil-error Enqueue as "recorded" and proceeds; a Nop queue
// simply records nothing (the delivery path is opt-in).
func (NopDeliveryQueue) Enqueue(_ context.Context, origin session.SessionID, _ string) (DeliveryNote, error) {
	return DeliveryNote{SessionID: origin}, nil
}

// Pending returns an empty slice for every origin (absence is data).
func (NopDeliveryQueue) Pending(_ context.Context, _ session.SessionID) ([]DeliveryNote, error) {
	return nil, nil
}

// MarkDelivered is a no-op success (idempotent by construction).
func (NopDeliveryQueue) MarkDelivered(_ context.Context, _ session.SessionID, _ uint64) error {
	return nil
}
