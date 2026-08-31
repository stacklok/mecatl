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

// cursorEnvelope is the decoded form: a backend-owned generation plus a
// backend-owned position, both opaque to this package.
type cursorEnvelope struct {
	V string `json:"v"`
	G string `json:"g"`
	P string `json:"p"`
}

// EncodeCursor builds an opaque cursor from a backend's generation and position.
//
// It lives in port rather than in each adapter so all four backends agree
// byte-for-byte on the envelope, and so tamper rejection has ONE implementation
// to test rather than four to keep in sync.
//
// base64url (unpadded) keeps a cursor safe in a URL path, a query parameter, a
// JSON string, and an HTTP header without escaping — it is carried by all of
// those before it reaches a backend.
func EncodeCursor(generation, position string) Cursor {
	b, err := json.Marshal(cursorEnvelope{V: cursorEnvelopeVersion, G: generation, P: position})
	if err != nil {
		// Unreachable: the struct is three strings, which always marshal.
		return ""
	}
	return Cursor(base64.RawURLEncoding.EncodeToString(b))
}

// DecodeCursor recovers the generation and position from cur, and verifies the
// generation against the log's current one.
//
// The ZERO cursor decodes to the zero position with no error — "the beginning"
// is a legitimate request, and forcing every backend to special-case it before
// calling here would put the same branch in four places.
//
// A generation mismatch is ErrCursorExpired. Passing an EMPTY currentGeneration
// disables the check, which is what a backend with no generation basis (a legacy
// log written before generations existed) needs — such a log's cursors carry an
// empty generation too, so the comparison holds without a special case.
func DecodeCursor(cur Cursor, currentGeneration string) (position string, err error) {
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
	if env.G != currentGeneration {
		return "", fmt.Errorf("%w: cursor is from log generation %q, current is %q", ErrCursorExpired, env.G, currentGeneration)
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
	Limit int

	// Follow keeps the iterator open at the tail instead of ending there,
	// yielding records as they are appended until ctx is done.
	//
	// A follow that ends because ctx was cancelled returns WITHOUT yielding an
	// error: cancellation is how a watch is meant to end, and reporting it as a
	// fault would make every clean detach look like a failure in the logs.
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
