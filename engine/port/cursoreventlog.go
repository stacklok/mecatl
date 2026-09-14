package port

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"iter"

	"github.com/stacklok/mecatl/engine/session"
)

// ErrCursorMalformed is returned when a cursor cannot be decoded at all — bad
// base64, bad JSON, an unknown envelope version, or a position the backend
// cannot align to a record boundary.
//
// It is deliberately DISTINCT from ErrCursorExpired. Expired means "your
// position was valid but the log's basis moved underneath you"; malformed means
// "this is not a cursor I issued". A client can retry an expired cursor by
// restarting from the beginning; a malformed one indicates a bug or tampering
// and restarting silently would hide it.
var ErrCursorMalformed = errors.New("port: malformed event-log cursor")

// ErrCursorExpired is returned when a cursor's generation does not match the
// log's current generation — the log was deleted and recreated, or otherwise had
// its positional basis replaced.
//
// The contract this exists to enforce is NEVER SILENTLY WRONG. A positional
// cursor against a rebuilt log does not fail: it happily points at a real
// position holding a DIFFERENT event, and the consumer resumes from the wrong
// place with no error to notice. Generation-scoping converts that silent
// corruption into a loud, recoverable one — the consumer restarts from the
// beginning or reloads the transcript.
var ErrCursorExpired = errors.New("port: event-log cursor expired")

// ErrEventFollowCapacity is yielded by a CursorEventLog follow read when the
// backend cannot immediately admit another follower. Callers may retry from
// their last processed cursor; the rejected read performs no storage work.
//
// It is deliberately distinct from delivery lag. Capacity means the backend
// rejected the iterator before it began, while lag means an admitted consumer
// could not keep up with records already being delivered.
var ErrEventFollowCapacity = errors.New("port: event-log follow capacity exhausted")

// cursorEnvelopeVersion tags the encoded cursor so a future encoding change is
// distinguishable from corruption. A cursor carrying an unrecognised version is
// malformed, never coerced.
const cursorEnvelopeVersion = "cur/1"

// Cursor is an OPAQUE resume token meaning "you have durably received every
// record up to and including this position".
//
// Opaque is a promise, not an implementation detail. The encoding is stateless
// — a server-side cursor registry was rejected in ADR 0250 because it would need
// eviction and a cloud-inventory row to buy nothing — which does make a cursor
// inspectable by a determined client. That is exactly why the generation is
// inside it: a client that decodes one, hand-edits it, and passes it back gets
// ErrCursorExpired or ErrCursorMalformed rather than silently wrong data. Treat
// the value as bytes to hand back, nothing more.
//
// The ZERO cursor means THE BEGINNING OF THE LOG. That is a real value, not a
// missing one: a consumer attaching for the first time has no cursor, and
// "replay everything, then follow" is the common case rather than an edge.
type Cursor string

// cursorEnvelope is the decoded form: the session the cursor was issued for,
// plus a backend-owned generation and position, both opaque to this package.
type cursorEnvelope struct {
	V string `json:"v"`
	S string `json:"s"`
	G string `json:"g"`
	P string `json:"p"`
}

// cursorEncodeFailed is the value EncodeCursor returns if the envelope ever
// fails to marshal.
//
// It is deliberately NOT the zero Cursor. The zero value means "the beginning of
// the log", so returning it from a failed encode would convert an
// unreachable-but-catastrophic bug into a silent full replay that the consumer
// believes is an increment — the exact failure this whole type exists to
// prevent. "!" is outside the base64url alphabet, so every DecodeCursor rejects
// it as malformed: fail-closed, loudly.
const cursorEncodeFailed Cursor = "!"

// EncodeCursor builds an opaque cursor from a backend's generation and position.
//
// It lives in port rather than in each adapter so all four backends agree
// byte-for-byte on the envelope, and so tamper rejection has ONE implementation
// to test rather than four to keep in sync.
//
// base64url (unpadded) keeps a cursor safe in a URL path, a query parameter, a
// JSON string, and an HTTP header without escaping — it is carried by all of
// those before it reaches a backend.
func EncodeCursor(id session.SessionID, generation, position string) Cursor {
	b, err := json.Marshal(cursorEnvelope{V: cursorEnvelopeVersion, S: string(id), G: generation, P: position})
	if err != nil {
		// Unreachable: the struct is four strings, which always marshal.
		return cursorEncodeFailed
	}
	return Cursor(base64.RawURLEncoding.EncodeToString(b))
}

// DecodeCursor recovers the position from cur, verifying BOTH that the cursor
// was issued for this session and that its generation matches the log's current
// one.
//
// The ZERO cursor decodes to the zero position with no error — "the beginning"
// is a legitimate request, and forcing every backend to special-case it before
// calling here would put the same branch in four places.
//
// SESSION SCOPING IS ENFORCED HERE, not left to each backend. A position is only
// meaningful relative to one log, so a cursor issued for session A applied to
// session B must fail. Checking it in the port rather than per backend is what
// makes the guarantee structural: a backend cannot forget it, and a new backend
// inherits it. It also closes the case a generation check CANNOT: a legacy log
// predating generations reports the EMPTY generation, so two legacy logs share a
// basis value and a cross-session cursor would decode cleanly and resolve to a
// real — but wrong — record. Found by review on #868; the mismatch is
// ErrCursorMalformed rather than ErrCursorExpired because nothing moved
// underneath the caller: the cursor was never valid here, which is a bug or
// tampering, not staleness.
//
// A generation mismatch is ErrCursorExpired. Passing an EMPTY currentGeneration
// disables THAT check, which is what a backend with no generation basis (a legacy
// log written before generations existed) needs — such a log's cursors carry an
// empty generation too, so the comparison holds without a special case. The
// session check above is never disabled.
func DecodeCursor(cur Cursor, id session.SessionID, currentGeneration string) (position string, err error) {
	if cur == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(string(cur))
	if err != nil {
		return "", fmt.Errorf("%w: not base64url", ErrCursorMalformed)
	}
	var env cursorEnvelope
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		return "", fmt.Errorf("%w: not a cursor envelope", ErrCursorMalformed)
	}
	if env.V != cursorEnvelopeVersion {
		return "", fmt.Errorf("%w: unknown cursor version %q", ErrCursorMalformed, env.V)
	}
	if env.S != string(id) {
		return "", fmt.Errorf("%w: cursor was issued for session %q, not %q", ErrCursorMalformed, env.S, id)
	}
	if env.G != currentGeneration {
		// The CURRENT generation is deliberately absent from this message: a
		// client that can trigger an expiry at will should not be able to read
		// the live basis value back out of the error. The cursor's own generation
		// is already in the caller's hands.
		return "", fmt.Errorf("%w: cursor was issued against log generation %q, which is no longer current", ErrCursorExpired, env.G)
	}
	return env.P, nil
}

// LogRecordKind distinguishes the two things an append position can hold.
type LogRecordKind string

const (
	// LogRecordEvent is an ordinary recorded session.Event.
	LogRecordEvent LogRecordKind = "event"

	// LogRecordGap marks a position where an append is KNOWN to have failed.
	//
	// It is a log-record envelope variant, NOT a session.Event (ADR 0250): a gap
	// is a fact about DELIVERY, not something that happened in the run, and
	// making it an event would leak it into the event taxonomy, the proto Event
	// message, the kind-parity gate, and every consumer that folds events into a
	// session. It occupies a real append position so cursors advance past it
	// correctly, and the legacy EventLog.Read SKIPS it, preserving that port's
	// existing contract of returning only events.
	LogRecordGap LogRecordKind = "gap"
)

// LogRecord is one durable position in a session's log.
type LogRecord struct {
	// Kind says whether this position holds an event or a gap marker.
	Kind LogRecordKind

	// Event is the recorded event. It is the zero Event when Kind is
	// LogRecordGap.
	Event session.Event

	// GapReason describes why an append failed, when Kind is LogRecordGap. It is
	// operator-facing diagnostic text and may be empty.
	GapReason string

	// Cursor is the resume token positioned AFTER this record. Handing it back to
	// ReadAfter yields the NEXT record, so a consumer that persists it after
	// processing each record gets at-least-once delivery across a reconnect.
	Cursor Cursor

	// Live reports whether this record arrived AFTER the read had drained
	// everything present when it started.
	//
	// It is the replay/live boundary a follower needs in order to tell a caller
	// "you are now caught up". Computing it in the backend is free — each one
	// already knows where its initial snapshot ended — whereas a consumer cannot
	// recover it afterwards without a second round-trip to ask for the tail.
	Live bool
}

// ReadOptions bounds a ReadAfter.
type ReadOptions struct {
	// Limit caps the number of records yielded. Zero means unbounded.
	//
	// This is the bounded paging the legacy Read cannot express: it reads the
	// whole log and stops, so a long transcript has no way to arrive in pieces.
	//
	// Zero bounds the SEQUENCE, not the FETCH. It means "yield records until the
	// log ends or the context is cancelled" — it is NOT permission to pull an
	// unbounded response out of storage in one round trip. A backend may, and for
	// a large log should, page internally at whatever size it likes while
	// continuing to yield; that choice is invisible through the iterator, so it
	// is a backend decision rather than a contract term. Stated because the
	// natural reading of "unbounded" is to pass the caller's zero straight
	// through to the storage call, which turns a long transcript into one large
	// allocation (raised in review on #868).
	//
	// When Limit is POSITIVE it is a TOTAL budget for the call, not a per-wake-up
	// one: reaching it ends the read even with Follow set. A follower wanting an
	// unbounded tail sets Limit to zero — under the per-wake-up reading a total
	// cap would be inexpressible, whereas this way both are.
	Limit int

	// Follow keeps the iterator open at the tail instead of ending there,
	// yielding records as they are appended until ctx is done.
	//
	// A follow that ends because ctx was cancelled returns WITHOUT yielding an
	// error: cancellation is how a watch is meant to end, and reporting it as a
	// fault would make every clean detach look like a failure in the logs.
	//
	// A positive Limit still applies and still ends the read: Follow means "do
	// not stop at the tail", not "ignore the budget".
	Follow bool
}

// CursorEventLog is EventLog plus durable positions: append returns where the
// record landed, and reads resume from a position rather than always from the
// start.
//
// It is ADDITIVE (ADR 0250). EventLog is unchanged and unbroken, and a backend
// opts in by also implementing this interface. A backend that does not is NOT
// silently degraded to "replay the whole log every time" — the watch operation
// reports the feature as unsupported, because a client asking to resume from a
// position and being handed the entire transcript instead is a correctness
// problem dressed as a performance one.
//
// CONCURRENCY: the same contract as EventLog — safe for concurrent use across
// session ids, with per-id append order well-defined for a single session's
// serialised appends. Additionally, a Follow read must tolerate appends
// happening concurrently on the same id; that is its whole purpose.
type CursorEventLog interface {
	EventLog

	// AppendEvent durably records ev and returns the cursor positioned after it.
	//
	// It carries the SAME durability obligation as EventLog.Append — nil means
	// the record is on stable storage — and the same at-most-once, no-retry
	// contract. It exists alongside Append rather than replacing it because
	// EventLog is a shipped port with consumers that do not want a cursor.
	AppendEvent(ctx context.Context, id session.SessionID, ev session.Event) (Cursor, error)

	// AppendGap durably records a gap marker at the next position.
	//
	// This is the best-effort, cross-process tier of ADR 0250's three-tier
	// append-gap guarantee: when an append fails, one gap marker is attempted, and
	// if it lands then every watcher everywhere learns of the gap deterministically
	// rather than silently skipping it. It covers the LIKELY failure — one
	// rejected or unencodable record — and not a total backend outage, which by
	// construction cannot record its own failure.
	AppendGap(ctx context.Context, id session.SessionID, reason string) (Cursor, error)

	// ReadAfter yields the log's records STRICTLY AFTER the given cursor.
	//
	// The zero cursor starts from the beginning. A cursor from a superseded log
	// generation yields ErrCursorExpired; one that cannot be decoded or aligned
	// yields ErrCursorMalformed. Neither is ever coerced to a position — a cursor
	// that cannot be honoured exactly must fail, because resuming from
	// approximately the right place is indistinguishable from resuming from the
	// right place until data is already lost.
	//
	// As with EventLog.Read, an error is yielded on a zero-value record and the
	// iterator then stops, a miss is an empty sequence rather than an error, and
	// resources are released when the consumer breaks out early.
	ReadAfter(ctx context.Context, id session.SessionID, after Cursor, opts ReadOptions) iter.Seq2[LogRecord, error]
}
