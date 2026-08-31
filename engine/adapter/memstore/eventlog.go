package memstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"iter"
	"strconv"
	"sync"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// EventLog is an in-memory, concurrency-safe port.EventLog — the durable-event
// sibling of the in-memory Store. It is the no-store-dir default the composition
// layer wires when SessionStore is memstore (so the event-log seam is never nil),
// and the mockable seam offline tests assert against: Append records each event
// per session id, Read replays them in append order.
//
// It also satisfies port.CursorEventLog. In-memory is the one backend where
// "durable" is a fiction, so the cursor half is here for CONTRACT coverage
// rather than deployment: it lets the shared conformance suite pin cursor
// semantics without a Redis or a temp dir, and lets an offline test exercise a
// follower. Cross-process follow is by definition out of reach here — a second
// process shares no memory — which is exactly why the cross-process obligation
// is proved against Redis and JSONL instead.
//
// It does NOT round-trip through a serialization (the relay already hands it a
// value Event and the loop never mutates a past event), so the recorded events
// are stored by value directly.
type EventLog struct {
	mu   sync.Mutex
	logs map[session.SessionID]*memLog
}

// memLog is one session's records plus the two things a cursor needs: a
// generation identifying this log's positional basis, and a broadcast channel
// so a follower can wait for an append instead of polling.
type memLog struct {
	generation string
	records    []port.LogRecord

	// changed is closed (and replaced) on every append. A follower captures the
	// current channel while holding the lock, releases it, and selects on the
	// channel and its context — the close-and-replace idiom, which unlike
	// sync.Cond composes with context cancellation.
	changed chan struct{}
}

// compile-time assertions that EventLog satisfies both ports.
var (
	_ port.EventLog       = (*EventLog)(nil)
	_ port.CursorEventLog = (*EventLog)(nil)
)

// NewEventLog constructs an empty in-memory event log.
func NewEventLog() *EventLog {
	return &EventLog{logs: make(map[session.SessionID]*memLog)}
}

// Append records ev under id in append order. It never fails (in-memory).
func (l *EventLog) Append(ctx context.Context, id session.SessionID, ev session.Event) error {
	_, err := l.AppendEvent(ctx, id, ev)
	return err
}

// AppendEvent records ev and returns the cursor positioned after it.
func (l *EventLog) AppendEvent(_ context.Context, id session.SessionID, ev session.Event) (port.Cursor, error) {
	return l.appendRecord(id, port.LogRecord{Kind: port.LogRecordEvent, Event: ev}), nil
}

// AppendGap records a gap marker and returns the cursor positioned after it.
func (l *EventLog) AppendGap(_ context.Context, id session.SessionID, reason string) (port.Cursor, error) {
	return l.appendRecord(id, port.LogRecord{Kind: port.LogRecordGap, GapReason: reason}), nil
}

// appendRecord is the one append path both public appenders share, so an event
// and a gap can never disagree about ordering, generation, or wake-up.
func (l *EventLog) appendRecord(id session.SessionID, rec port.LogRecord) port.Cursor {
	l.mu.Lock()
	defer l.mu.Unlock()
	lg := l.ensureLocked(id)
	lg.records = append(lg.records, rec)
	close(lg.changed)
	lg.changed = make(chan struct{})
	return port.EncodeCursor(id, lg.generation, strconv.Itoa(len(lg.records)))
}

// Read yields the EVENTS recorded under id in append order. A miss (no events)
// yields an empty sequence (absence is data). Gap markers are SKIPPED: this is
// port.EventLog, whose shipped contract is that it returns events, and widening
// it to emit a non-event would break every existing consumer — including the
// event-sourced fold.
//
// The slice is copied under the lock so a concurrent Append cannot race the
// iteration.
func (l *EventLog) Read(_ context.Context, id session.SessionID) iter.Seq2[session.Event, error] {
	l.mu.Lock()
	var snapshot []port.LogRecord
	if lg := l.logs[id]; lg != nil {
		snapshot = make([]port.LogRecord, len(lg.records))
		copy(snapshot, lg.records)
	}
	l.mu.Unlock()
	return func(yield func(session.Event, error) bool) {
		for _, rec := range snapshot {
			if rec.Kind != port.LogRecordEvent {
				continue
			}
			if !yield(rec.Event, nil) {
				return
			}
		}
	}
}

// ReadAfter yields records strictly after the cursor, optionally following the
// tail until ctx is done.
func (l *EventLog) ReadAfter(ctx context.Context, id session.SessionID, after port.Cursor, opts port.ReadOptions) iter.Seq2[port.LogRecord, error] {
	return func(yield func(port.LogRecord, error) bool) {
		start, gen0, err := l.resolve(id, after)
		if err != nil {
			yield(port.LogRecord{}, err)
			return
		}
		next := start
		var yielded int
		// live flips once the reader has drained everything that was present when
		// it first reached the tail; records after that arrived while it watched.
		var live bool
		for {
			batch, gen, wait := l.sliceFrom(id, next)
			if gen != gen0 {
				// The log was reset underneath a follower. Its position now
				// addresses a different record, which is precisely the silent
				// corruption generations exist to convert into a loud failure.
				yield(port.LogRecord{}, fmt.Errorf("%w: the log was reset while following", port.ErrCursorExpired))
				return
			}
			for _, rec := range batch {
				next++
				rec.Cursor = port.EncodeCursor(id, gen, strconv.Itoa(next))
				rec.Live = live
				if !yield(rec, nil) {
					return
				}
				yielded++
				if opts.Limit > 0 && yielded >= opts.Limit {
					return
				}
			}
			if !opts.Follow {
				return
			}
			live = true
			select {
			case <-ctx.Done():
				// A cancelled follow is a clean detach, not a fault.
				return
			case <-wait:
			}
		}
	}
}

// resolve validates after against the log's current generation and returns the
// index of the first record to yield plus that generation.
//
// It MINTS the log when the id has none, so the generation it returns is
// authoritative for the whole read. Reading it without minting would let a
// follower attach at generation "" and then see the first append's real
// generation as a reset.
func (l *EventLog) resolve(id session.SessionID, after port.Cursor) (start int, generation string, err error) {
	l.mu.Lock()
	gen := l.ensureLocked(id).generation
	l.mu.Unlock()

	pos, err := port.DecodeCursor(after, id, gen)
	if err != nil {
		return 0, "", err
	}
	if pos == "" {
		return 0, gen, nil
	}
	idx, err := strconv.Atoi(pos)
	if err != nil || idx < 0 {
		return 0, "", fmt.Errorf("%w: position %q is not an index", port.ErrCursorMalformed, pos)
	}
	return idx, gen, nil
}

// sliceFrom returns a copy of the records at or after from, the log's
// generation, and the channel that closes on the next append.
func (l *EventLog) sliceFrom(id session.SessionID, from int) (batch []port.LogRecord, generation string, wait <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lg := l.ensureLocked(id)
	if from < len(lg.records) {
		batch = make([]port.LogRecord, len(lg.records)-from)
		copy(batch, lg.records[from:])
	}
	return batch, lg.generation, lg.changed
}

// ensureLocked returns the log for id, creating it if absent. Callers hold l.mu.
func (l *EventLog) ensureLocked(id session.SessionID) *memLog {
	lg := l.logs[id]
	if lg == nil {
		lg = &memLog{generation: newGeneration(), changed: make(chan struct{})}
		l.logs[id] = lg
	}
	return lg
}

// Reset discards the log recorded under id and mints a NEW generation, so every
// outstanding cursor for that id expires rather than silently addressing a
// different record. It is the in-memory analogue of deleting a session's log.
//
// It closes the outgoing log's broadcast channel so a parked follower wakes and
// observes the expiry immediately instead of blocking until its context ends.
func (l *EventLog) Reset(id session.SessionID) {
	l.mu.Lock()
	if old := l.logs[id]; old != nil {
		close(old.changed)
	}
	l.logs[id] = &memLog{generation: newGeneration(), changed: make(chan struct{})}
	l.mu.Unlock()
}

// newGeneration mints an opaque token identifying a log's positional basis.
// Random rather than a counter: generations are compared across processes and
// across restarts, so a per-process counter would collide the moment two
// processes each minted their first one.
func newGeneration() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
