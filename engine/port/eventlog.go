package port

import (
	"context"
	"iter"

	"github.com/stacklok/mecatl/engine/session"
)

// EventLog is the append-only, per-session DURABLE record of a run's event
// stream — the rich timeline (live reasoning, ask/verdict pairs, delegation
// lifecycle) that the gRPC/HTTP relay otherwise emits and discards. It is a
// SEPARATE port from port.EventSink: a Sink is a synchronous live MIRROR of the
// loop's emits (telemetry, ACP), whereas an EventLog is durable storage that a
// later consumer reads back chronologically. The two never share a code path —
// the loop is storage-agnostic (it only emits), and the persistence happens at
// the server relay, beside the existing awaiting-ask Persist.
//
// The log stores ALREADY-REDACTED events: the event stream is itself the
// redaction boundary (Subagent/Parallel payloads are metadata-only, Team
// previews are capped, surfaced child asks are clamped — gauntlet #7), so the
// log inherits that discipline and adds no new redaction code. The relay may
// coalesce same-turn message/reasoning deltas into one event of each kind before
// calling Append; their text and first-observed kind ordering remain exact, while
// the live client still receives every original delta unchanged.
//
// Both the local JSONL adapter (3a) and the gRPC driver (3c) implement this one
// contract; a remote driver maps Read 1:1 onto a server-streaming RPC.
//
// CONCURRENCY: implementations MUST be safe for concurrent Append and Read across
// session ids — the server shares ONE EventLog across all relay goroutines (every
// in-flight run appends through it at once). Per-id append order need only be
// well-defined for a SINGLE session's appends, which the server serialises (one
// relay loop per run).
type EventLog interface {
	// Append durably records ev under the session id.
	//
	// DURABILITY OBLIGATION: Append must be durable before it returns nil — the
	// record is on stable storage (or committed to the backing service) by the
	// time nil is returned. An implementation that CANNOT guarantee that returns
	// an error rather than buffering silently. A non-nil error may have happened
	// before OR after the record committed (for example, a post-write sync can
	// fail), so the commit outcome is indeterminate to the caller. The caller
	// therefore MUST NOT retry: retrying could duplicate a committed event. (The
	// relay logs a WARN on failure and never aborts the run — a broken log must not
	// break the live stream — but it relies on nil meaning durable, so a silent
	// in-memory buffer that may lose the record on crash is a contract violation,
	// not a valid optimisation.)
	//
	// AT-MOST-ONCE / NO DEDUP: the caller appends each event AT MOST ONCE and
	// never retries, so implementations need NOT deduplicate. The log is keyed by
	// APPEND ORDER: session.Event.Seq is monotonic within a run, but the contract
	// is raw append order — implementations MUST NOT reorder by Seq (or anything
	// else); Read returns events in the exact order Append received them.
	Append(ctx context.Context, id session.SessionID, ev session.Event) error

	// Read returns the session's recorded events in APPEND order as a lazy
	// iterator, matching the LLMProvider.Stream idiom: it is streamable (maps
	// 1:1 to a server-streaming Read RPC and avoids a unary size cap on a long
	// log) and yields each event with a per-item error.
	//
	// IMPLEMENTER OBLIGATION: on an infrastructure fault (an undecodable record,
	// an unknown format tag, an I/O fault) Read yields (session.Event{}, err) and
	// then RETURNS — it yields no further events after an error. A MISS — no log
	// recorded for the id — yields an EMPTY sequence, not an error: absence is
	// data, exactly like an empty conversation. An implementation MUST release any
	// resource it opened (file handle, stream) when the consumer breaks out of the
	// range early, exactly like a well-behaved iter.Seq2.
	Read(ctx context.Context, id session.SessionID) iter.Seq2[session.Event, error]
}
