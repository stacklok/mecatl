package redisstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// This file implements durable cursors over the current per-session Redis
// STREAM event log. Old Redis datatypes are rejected without conversion.
//
// CONNECTION COST — the honest one. A blocking XREAD occupies a pooled
// connection for the duration of its block. Follow uses an isolated, bounded
// client so it cannot exhaust the durability pool used by Save and Append. It
// blocks in BOUNDED slices (followBlock) and re-acquires between them, so a
// watcher parks a connection for at most one slice at a time instead of
// indefinitely, and a cancelled follow gives the connection back within one
// slice rather than on the next append. That bounds the hold, it does not make
// it free: N concurrent watchers still want N connections. Process-local
// admission therefore cannot exceed the configured follow-pool size.

const (
	// eventsGenerationKeyPrefix holds a session log's positional basis: an
	// opaque token minted when the stream is first created.
	//
	// It is a SEPARATE key rather than a stream entry because Redis streams have
	// no header slot, and because deleting the log must be able to drop the
	// basis in the same atomic step (see deleteMetadataScript's KEYS list) —
	// otherwise a deleted-and-recreated log would inherit the old generation and
	// a cursor from the previous log would resolve silently against the new one.
	eventsGenerationKeyPrefix = "mecatl:events-gen:"

	// EventLogGapFormat tags a gap marker: a position where an append is KNOWN
	// to have failed.
	//
	// A sibling tag rather than a new event type, mirroring the jsonlstore
	// envelope: a gap is a fact about DELIVERY, not something that happened in
	// the run, so it never becomes a session.Event (ADR 0250 decision 5). The
	// legacy Read skips it; cursor readers see it via ReadAfter.
	EventLogGapFormat = "redisstore-eventlog-gap/1"

	// recordField is the stream entry field carrying the JSON envelope. One
	// field keeps an entry's encoding identical to the LIST element it replaces,
	// so the migration is a re-homing rather than a re-encoding.
	recordField = "r"

	// followBlock bounds one blocking XREAD. See the connection-cost note above:
	// it trades a little latency at the tail for a bounded connection hold and a
	// prompt response to cancellation.
	followBlock = time.Second

	// readPageSize bounds ONE storage round trip, independent of the caller's
	// ReadOptions.Limit.
	//
	// ReadOptions.Limit == 0 means "yield until the log ends or the context is
	// cancelled" — an unbounded SEQUENCE, not permission to pull an unbounded
	// response out of Redis in one reply. The read loop pages so each XREAD
	// remains bounded.
	readPageSize = 256

	// noBlock omits the BLOCK argument entirely. It is NOT zero: go-redis maps
	// Block==0 to "BLOCK 0", which blocks FOREVER, so a drain that passed zero
	// would hang on an exhausted stream instead of returning.
	noBlock = -1 * time.Nanosecond
)

var _ port.CursorEventLog = (*Store)(nil)

func eventsGenerationKey(id session.SessionID) string {
	return eventsGenerationKeyPrefix + string(id)
}

// newLogGeneration mints an opaque token identifying a log's positional basis.
// Random rather than derived from the key or a counter: it is compared across
// processes and restarts, where anything process-local would collide.
func newLogGeneration() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// appendRecordScript appends only to a current Redis STREAM. Unsupported old
// LIST logs fail before any key is changed.
var appendRecordScript = redis.NewScript(`
local kind = redis.call('TYPE', KEYS[1])['ok']
if kind ~= 'none' and kind ~= 'stream' then
  return redis.error_reply('MECATL_UNSUPPORTED_EVENT_LOG_TYPE_' .. kind)
end
if redis.call('EXISTS', KEYS[2]) == 0 then
  redis.call('SET', KEYS[2], ARGV[2])
end
local id = redis.call('XADD', KEYS[1], '*', ARGV[3], ARGV[1])
return {redis.call('GET', KEYS[2]), id}
`)

// AppendEvent durably records ev and returns the cursor positioned after it. It
// satisfies port.CursorEventLog.
//
// It carries the same durability obligation as Append — a nil error means the
// record is on stable storage — and the same at-most-once, no-retry contract.
func (st *Store) AppendEvent(ctx context.Context, id session.SessionID, ev session.Event) (port.Cursor, error) {
	evJSON, err := json.Marshal(ev)
	if err != nil {
		return "", fmt.Errorf("redisstore: marshal event: %w", err)
	}
	rec, err := json.Marshal(eventLogRecord{V: EventLogFormat, Ev: evJSON})
	if err != nil {
		return "", fmt.Errorf("redisstore: marshal event record: %w", err)
	}
	return st.appendRecord(ctx, id, rec)
}

// AppendGap durably records a gap marker at the next position. It satisfies
// port.CursorEventLog.
//
// This is the best-effort, cross-process tier of ADR 0250's three-tier
// append-gap guarantee: when an append fails, one gap marker is attempted, and
// if it lands then every watcher everywhere learns of the gap deterministically
// rather than silently skipping it. It covers the LIKELY failure — one rejected
// or unencodable record — and not a total backend outage, which by construction
// cannot record its own failure.
func (st *Store) AppendGap(ctx context.Context, id session.SessionID, reason string) (port.Cursor, error) {
	rec, err := json.Marshal(eventLogRecord{V: EventLogGapFormat, R: reason})
	if err != nil {
		return "", fmt.Errorf("redisstore: marshal gap record: %w", err)
	}
	return st.appendRecord(ctx, id, rec)
}

func (st *Store) appendRecord(ctx context.Context, id session.SessionID, rec []byte) (port.Cursor, error) {
	client, release, err := st.clients.acquire()
	if err != nil {
		return "", err
	}
	defer release()
	out, err := appendRecordScript.Run(ctx, client,
		[]string{eventsKey(id), eventsGenerationKey(id)},
		rec, newLogGeneration(), recordField,
	).Slice()
	if err != nil {
		if strings.Contains(err.Error(), "MECATL_UNSUPPORTED_EVENT_LOG_TYPE_") {
			return "", fmt.Errorf("redisstore: unsupported old event log type for %q; data was left unchanged", id)
		}
		return "", fmt.Errorf("redisstore: append event %q: %w", id, err)
	}
	generation, entryID, err := appendResult(out)
	if err != nil {
		return "", fmt.Errorf("redisstore: append event %q: %w", id, err)
	}
	return port.EncodeCursor(id, generation, entryID), nil
}

// appendResult unpacks the {generation, id} pair the append script returns.
func appendResult(out []any) (generation, entryID string, err error) {
	if len(out) != 2 {
		return "", "", fmt.Errorf("append script returned %d values, want 2", len(out))
	}
	generation, ok := out[0].(string)
	if !ok {
		return "", "", fmt.Errorf("append script returned generation of type %T, want string", out[0])
	}
	entryID, ok = out[1].(string)
	if !ok {
		return "", "", fmt.Errorf("append script returned entry id of type %T, want string", out[1])
	}
	return generation, entryID, nil
}

// ReadAfter yields the log's records strictly after the given cursor. It
// satisfies port.CursorEventLog.
//
// The zero cursor starts from the beginning. A cursor from a superseded log
// generation yields ErrCursorExpired; one that cannot be decoded or resolved to
// a stream ID yields ErrCursorMalformed. Neither is ever coerced to a position:
// resuming from approximately the right place is indistinguishable from
// resuming from the right one until data is already lost.
func (st *Store) ReadAfter(ctx context.Context, id session.SessionID, after port.Cursor, opts port.ReadOptions) iter.Seq2[port.LogRecord, error] {
	return func(yield func(port.LogRecord, error) bool) {
		readCtx := ctx
		if opts.Follow {
			followCtx, release, err := st.followers.admit(ctx)
			if err != nil {
				yield(port.LogRecord{}, err)
				return
			}
			defer release()
			readCtx = followCtx
		}

		basis, position, err := st.readAfterBasis(readCtx, id, after, opts.Follow)
		if err != nil {
			if readCtx.Err() != nil {
				return
			}
			yield(port.LogRecord{}, err)
			return
		}

		lastID, err := streamPosition(position)
		if err != nil {
			yield(port.LogRecord{}, err)
			return
		}
		st.readStreamAfter(readCtx, id, basis, lastID, opts, yield)
	}
}

// readAfterBasis resolves the log's basis and the cursor's position, holding a
// client only for the two commands that takes.
//
// It is deliberately a SEPARATE, short-lived acquisition from the read loop's.
// A follower can be parked for hours, and one lease held for the life of the
// iterator pins a retired credential generation open — clientGenerations cannot
// close a client while its refs are non-zero — so a credential rotation never
// completes for as long as anyone is watching. Raised in review on #869.
func (st *Store) readAfterBasis(ctx context.Context, id session.SessionID, after port.Cursor, follow bool) (basis logBasis, position string, err error) {
	client, release, err := st.acquireReadClient(follow)
	if err != nil {
		return logBasis{}, "", err
	}
	defer release()

	generation, present, err := logGeneration(ctx, client, id)
	if err != nil {
		return logBasis{}, "", err
	}
	position, err = port.DecodeCursor(after, id, generation)
	if err != nil {
		return logBasis{}, "", err
	}
	// A non-zero cursor ANCHORS the basis even when the stored generation is
	// empty: DecodeCursor has just verified the two agree, so the caller is
	// resuming a real position and any later basis is a replacement, not a
	// creation.
	return logBasis{generation: generation, known: present || after != ""}, position, nil
}

func (st *Store) acquireReadClient(follow bool) (redis.UniversalClient, func(), error) {
	if follow {
		return st.clients.acquireFollow()
	}
	return st.clients.acquire()
}

// yieldReadError reports err to the consumer, preserving the classification a
// sentinel already carries.
//
// A cursor error is the consumer's cue to restart from the beginning rather than
// retry, so wrapping it in an infrastructure-shaped "read events" message would
// bury the one bit that decides what the caller does next.
func yieldReadError(id session.SessionID, err error, yield func(port.LogRecord, error) bool) {
	if errors.Is(err, port.ErrCursorExpired) || errors.Is(err, port.ErrCursorMalformed) || errors.Is(err, errStoreClosed) {
		yield(port.LogRecord{}, err)
		return
	}
	yield(port.LogRecord{}, fmt.Errorf("redisstore: read events %q: %w", id, err))
}

// logGeneration reports the current STREAM log's positional basis.
func logGeneration(ctx context.Context, client redis.UniversalClient, id session.SessionID) (generation string, present bool, err error) {
	kind, err := client.Type(ctx, eventsKey(id)).Result()
	if err != nil {
		return "", false, fmt.Errorf("redisstore: type of event key %q: %w", id, err)
	}
	if kind != "none" && kind != "stream" {
		return "", false, fmt.Errorf("redisstore: unsupported old event log type %q for %q; data was left unchanged", kind, id)
	}
	generation, err = client.Get(ctx, eventsGenerationKey(id)).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("redisstore: read event generation %q: %w", id, err)
	}
	return generation, true, nil
}

// logBasis is the generation a read is anchored to, plus whether it is anchored
// at all.
//
// The two are distinct because the EMPTY generation is a REAL value, reported by
// both a log that does not exist yet and a legacy log predating generations. A
// follower that attaches to an absent log and then watches the first append mint
// a real generation must ADOPT it — the log was CREATED, not replaced — while one
// anchored to a real generation that later reads a different one has had its
// basis replaced underneath it. Conflating the two turns either the creation case
// into a spurious expiry or the replacement case into a silent pass; the first is
// what a naive version of this guard did to every follower attaching before the
// first append. jsonlstore carries the same distinction for the same reason.
type logBasis struct {
	generation string
	known      bool
}

// observe folds the currently-stored basis into b, reporting ErrCursorExpired if
// it REPLACED an anchored one.
func (b *logBasis) observe(current string) error {
	switch {
	case !b.known:
		if current != "" {
			b.generation, b.known = current, true
		}
		return nil
	case current != b.generation:
		return fmt.Errorf("%w: the log was replaced while following", port.ErrCursorExpired)
	}
	return nil
}

// streamPosition validates a cursor's decoded position as a Redis stream ID.
//
// Validated HERE rather than left to Redis: XREAD rejects a malformed ID with a
// generic error string, and this must be distinguishable as ErrCursorMalformed
// so the consumer can tell a corrupted token from an infrastructure fault.
func streamPosition(position string) (string, error) {
	if position == "" {
		// The beginning. XREAD is exclusive, so "0-0" yields every entry.
		return "0-0", nil
	}
	ms, seq, found := strings.Cut(position, "-")
	if _, err := strconv.ParseUint(ms, 10, 64); err != nil {
		return "", fmt.Errorf("%w: %q is not a stream id", port.ErrCursorMalformed, position)
	}
	if found {
		if _, err := strconv.ParseUint(seq, 10, 64); err != nil {
			return "", fmt.Errorf("%w: %q is not a stream id", port.ErrCursorMalformed, position)
		}
	}
	return position, nil
}

// readStreamAfter drains the entries after lastID, then optionally follows.
//
// One mechanism serves both halves: XREAD is exclusive of the ID it is given, so
// "everything after this cursor" and "everything from the beginning" (ID 0-0)
// are the same call, and following is the same call again with BLOCK set. XRANGE
// would need an exclusive-range prefix and a separate follow path.
func (st *Store) readStreamAfter(
	ctx context.Context,
	id session.SessionID,
	basis logBasis,
	lastID string,
	opts port.ReadOptions,
	yield func(port.LogRecord, error) bool,
) {
	sent := 0
	// live flips once the stream has been drained of everything present when the
	// read started — the replay/live boundary a follower reports to its caller.
	// It is set ONLY on the first empty result: a batch arriving before that is
	// still replay, however recently it was appended.
	live := false
	for {
		// NOTE: go-redis reads Block as "0 means block FOREVER, negative means
		// omit the BLOCK argument". The drain must therefore pass a negative
		// value, not zero.
		block := noBlock
		if live {
			block = followBlock
		}
		msgs, err := st.streamCycle(ctx, id, &basis, lastID, readCount(opts, sent), block, opts.Follow)
		switch {
		case errors.Is(err, redis.Nil):
			// Nothing more available. A plain read is done; a follower marks the
			// boundary and loops so it re-checks ctx between bounded blocks.
			if !opts.Follow || ctx.Err() != nil {
				return
			}
			live = true
			continue
		case err != nil:
			if ctx.Err() != nil {
				// A cancelled follow ends CLEANLY: cancellation is how a watch is
				// meant to stop, and reporting it as a fault would make every
				// clean detach look like a failure.
				return
			}
			yieldReadError(id, err, yield)
			return
		}

		for _, msg := range msgs {
			lastID = msg.ID
			rec, ok, err := decodeStreamMessage(msg)
			if err != nil {
				yield(port.LogRecord{}, err)
				return
			}
			if !ok {
				continue // an entry occupying a position but carrying no record
			}
			rec.Cursor = port.EncodeCursor(id, basis.generation, msg.ID)
			rec.Live = live
			if !yield(rec, nil) {
				return
			}
			sent++
			if opts.Limit > 0 && sent >= opts.Limit {
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// streamCycle performs ONE bounded XREAD: acquire a client, re-verify the log's
// basis, read, release — all before the caller yields anything.
//
// Three properties, each per-cycle by design:
//
// The basis RE-CHECK is what converts "the log was deleted and recreated under a
// live follower" from silently streaming the replacement log's records into
// ErrCursorExpired. The cold-attach check in readAfterBasis cannot cover it: a
// follower has already passed that check and is parked at the tail. memstore and
// jsonlstore re-checked per cycle already; this backend did not. Raised in review
// on #869. It runs AFTER the read for the reason given at the call itself.
//
// The LEASE is acquired and released here rather than around the whole iterator,
// so a follower parked for hours cannot pin a retired credential generation.
//
// And the release happens BEFORE the caller yields: the batch is decoded out of
// the reply first, so a slow transport consumer holds no pooled connection.
//
// COST, stated honestly: this adds a TYPE and a GET per cycle, so an idle
// follower issues three commands per followBlock rather than one. That is the
// price of the guard above, and it is the same trade jsonlstore makes for the
// same reason. It is NOT a candidate for caching — a cached basis is exactly the
// stale basis the check exists to catch.
func (st *Store) streamCycle(
	ctx context.Context,
	id session.SessionID,
	basis *logBasis,
	lastID string,
	count int64,
	block time.Duration,
	follow bool,
) ([]redis.XMessage, error) {
	client, release, err := st.acquireReadClient(follow)
	if err != nil {
		return nil, err
	}
	defer release()

	streams, readErr := client.XRead(ctx, &redis.XReadArgs{
		Streams: []string{eventsKey(id), lastID},
		Count:   count,
		Block:   block,
	}).Result()

	// The basis is verified AFTER the read, and that ORDERING is the guard. A
	// blocking XREAD straddles time: Redis wakes a blocked client on an XADD to
	// the key, and because stream IDs are wall-clock derived, a replacement log's
	// first entry sorts AFTER the follower's position — so a single reply can
	// legitimately carry a record from a log that did not exist when the cycle
	// began. Checking before the read passes in exactly that case, which is how
	// the first version of this guard still handed over the replacement record.
	//
	// Verifying afterwards cannot be raced the same way: if the record came from
	// the replacement log then the new basis was already stored when it was
	// written, so this read observes it and refuses. A reset landing after the
	// check is harmless — the record yielded was genuinely from the log being
	// followed at the time it was read.
	current, _, err := logGeneration(ctx, client, id)
	if err != nil {
		return nil, err
	}
	if err := basis.observe(current); err != nil {
		return nil, err
	}

	if readErr != nil {
		return nil, readErr
	}
	var msgs []redis.XMessage
	for _, stream := range streams {
		msgs = append(msgs, stream.Messages...)
	}
	return msgs, nil
}

// readCount is the size of the next storage round trip: the internal page cap,
// lowered to whatever remains of a positive ReadOptions.Limit so a bounded read
// never over-fetches.
func readCount(opts port.ReadOptions, sent int) int64 {
	count := int64(readPageSize)
	if opts.Limit > 0 {
		if remaining := int64(opts.Limit - sent); remaining < count {
			count = remaining
		}
	}
	return count
}

// decodeStreamMessage turns one stream entry into a LogRecord. ok is false for
// an entry that occupies a position but carries no record.
func decodeStreamMessage(msg redis.XMessage) (rec port.LogRecord, ok bool, err error) {
	raw, present := msg.Values[recordField]
	if !present {
		return port.LogRecord{}, false, nil
	}
	text, isString := raw.(string)
	if !isString {
		return port.LogRecord{}, false, fmt.Errorf("redisstore: event entry %s field %q has type %T, want string", msg.ID, recordField, raw)
	}
	return decodeEventLogRecord([]byte(text))
}

// decodeEventLogRecord decodes one envelope, shared by the stream and legacy-list
// paths so a gap or an unknown tag means the same thing on both.
func decodeEventLogRecord(raw []byte) (rec port.LogRecord, ok bool, err error) {
	var envelope eventLogRecord
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return port.LogRecord{}, false, fmt.Errorf("redisstore: decode event record: %w", err)
	}
	switch envelope.V {
	case EventLogGapFormat:
		return port.LogRecord{Kind: port.LogRecordGap, GapReason: envelope.R}, true, nil
	case EventLogFormat:
		var ev session.Event
		if err := json.Unmarshal(envelope.Ev, &ev); err != nil {
			return port.LogRecord{}, false, fmt.Errorf("redisstore: decode event: %w", err)
		}
		return port.LogRecord{Kind: port.LogRecordEvent, Event: ev}, true, nil
	default:
		return port.LogRecord{}, false, fmt.Errorf("redisstore: unknown event-log format %q (want %q)", envelope.V, EventLogFormat)
	}
}
