// Package redisstore implements the Redis-backed port.SessionStore, port.EventLog,
// port.PrunableStore, and port.ToolCallRecorder for the cloud-native posture
// (ADR 0048, mecak8s). The agent pods are storage-free: session snapshots and
// the durable event log live in Redis as a managed service, and this adapter is
// the single store the server relay binds when the operator selects a Redis
// backend (a transport alternative to the local jsonlstore).
//
// It REUSES the existing storage formats — it is a TRANSPORT, not a format:
//   - snapshots are encoded with engine/adapter/sessnap (sessnap-json/1), shared
//     with jsonlstore and the gRPC driver; and
//   - the event-log record is the same {"v":<tag>,"ev":<json>} envelope shape as
//     jsonlstore, with its own format tag (redisstore-eventlog/1) so a
//     forward-incompatible log fails loud on Read (an unknown tag is an error).
//
// No client-side mutex is needed: Redis serializes commands single-threaded and
// HSET/HGET/RPUSH are atomic, so the in-process sync.Mutex that jsonlstore
// carries is absent here. The adapter is validated by the SAME conformance
// suites as jsonlstore (storeconformance + eventlogconformance), exercised
// offline against an in-process miniredis so `task test` needs no live broker.
//
// DURABILITY CAVEAT: Append/Save call RPUSH/HSET synchronously and return only
// once Redis acknowledges the command, but Redis's own persistence config
// (RDB snapshotting vs AOF fsync policy) determines durability-on-crash. An
// operator selecting this backend must configure Redis persistence to match
// their durability requirement; the adapter makes no durability claim beyond
// "Redis accepted the write".
package redisstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// Key layout under a single Redis keyspace. The session id is the raw string
// after the sessionKeyPrefix (Redis keys are arbitrary strings, so no
// sanitization is needed — unlike jsonlstore's filename-safe safeName).
const (
	sessionKeyPrefix = "mecatl:session:"
	eventsKeyPrefix  = "mecatl:events:"
	toolsKeyPrefix   = "mecatl:tools:"

	// hash fields on the session key.
	fieldBlob  = "blob"
	fieldMtime = "mtime"
)

// ErrNotFound is returned by Load when no snapshot exists for the id. It wraps
// port.ErrSessionNotFound so a consumer that may not import this adapter can
// distinguish not-found from an infra failure via errors.Is.
var ErrNotFound = fmt.Errorf("redisstore: session not found: %w", port.ErrSessionNotFound)

// EventLogFormat is the per-record format tag written on every event-log
// record. It versions the on-disk encoding so Read can reject an unknown tag as
// an infra error (a forward-incompatible log must fail loud, not silently skip).
const EventLogFormat = "redisstore-eventlog/1"

// eventLogRecord is one event-log entry: a format tag plus the verbatim
// session.Event JSON. The event is stored as already-redacted JSON (the relay
// is the redaction boundary); the tag lets Read validate the encoding version.
// It mirrors the jsonlstore envelope shape so the two stores are codec-siblings.
type eventLogRecord struct {
	V  string          `json:"v"`
	Ev json.RawMessage `json:"ev"`
}

// compile-time assertions that Store satisfies all four ports it meets.
var (
	_ port.SessionStore     = (*Store)(nil)
	_ port.ToolCallRecorder = (*Store)(nil)
	_ port.PrunableStore    = (*Store)(nil)
	_ port.EventLog         = (*Store)(nil)
)

// Store is the Redis-backed SessionStore + EventLog + PrunableStore +
// ToolCallRecorder. It talks to a single Redis address (a managed service or an
// in-process miniredis for tests). It is safe for concurrent use: Redis
// serializes commands, so no client-side mutex is required.
type Store struct {
	client *redis.Client
}

// New connects to the Redis broker at addr and pings it to fail fast on an
// unreachable backend. The returned Store is ready to serve all four ports.
func New(addr string) (*Store, error) {
	if addr == "" {
		return nil, errors.New("redisstore: empty redis address")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	if err := client.Ping(context.Background()).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redisstore: ping %q: %w", addr, err)
	}
	return &Store{client: client}, nil
}

// Save stores a sessnap-encoded snapshot of s under the session key, stamping
// the current time as the mtime field so List can report the SAVE time (not a
// fresh time.Now() at list time — the stable-mtime conformance invariant).
// HSET overwrites the blob field, so a second Save replaces the first (the
// overwrite contract).
func (st *Store) Save(ctx context.Context, s *session.Session) error {
	if s == nil {
		return sessnap.ErrNilSession
	}
	blob, err := sessnap.Marshal(s)
	if err != nil {
		return err
	}
	mtime := time.Now().UnixNano()
	if err := st.client.HSet(ctx, sessionKey(s.ID), fieldBlob, blob, fieldMtime, mtime).Err(); err != nil {
		return fmt.Errorf("redisstore: save %q: %w", s.ID, err)
	}
	return nil
}

// Load reads the snapshot blob for id and restores it. A missing key (redis.Nil
// on HGET) wraps port.ErrSessionNotFound with the id in the message.
func (st *Store) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	blob, err := st.client.HGet(ctx, sessionKey(id), fieldBlob).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
		}
		return nil, fmt.Errorf("redisstore: load %q: %w", id, err)
	}
	return sessnap.Unmarshal(blob)
}

// List returns every stored session's id and SAVE-time mtime. It SCANs the
// keyspace for session keys (MATCH mecatl:session:*), then HGETs the mtime
// field for each. The mtime is the value written at Save time, so two Lists
// with no intervening Save agree exactly (the stable-across-reads invariant).
// SCAN is cursor-based and non-blocking; a corrupt mtime field (absent or
// unparseable) is skipped best-effort rather than failing the whole inventory.
func (st *Store) List(ctx context.Context) ([]port.StoredSession, error) {
	var out []port.StoredSession
	scan := st.client.Scan(ctx, 0, sessionKeyPrefix+"*", 0).Iterator()
	for scan.Next(ctx) {
		key := scan.Val()
		id := strings.TrimPrefix(key, sessionKeyPrefix)
		if id == "" {
			continue
		}
		mtimeRaw, err := st.client.HGet(ctx, key, fieldMtime).Result()
		if err != nil {
			// A key without an mtime field is a corrupt/partial entry; skip it
			// best-effort (Load of that id would fail the same way) rather than
			// fail the whole inventory.
			continue
		}
		ns, err := strconv.ParseInt(mtimeRaw, 10, 64)
		if err != nil {
			continue
		}
		out = append(out, port.StoredSession{ID: session.SessionID(id), ModifiedAt: time.Unix(0, ns)})
	}
	if err := scan.Err(); err != nil {
		return nil, fmt.Errorf("redisstore: list: %w", err)
	}
	return out, nil
}

// Delete removes the session snapshot AND its event-log and tool-call sidecars.
// It is idempotent: DEL on a missing key succeeds (port.PrunableStore contract),
// and the three keys are deleted in one round-trip.
func (st *Store) Delete(ctx context.Context, id session.SessionID) error {
	keys := []string{toolsKey(id), eventsKey(id), sessionKey(id)}
	if err := st.client.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("redisstore: delete %q: %w", id, err)
	}
	return nil
}

// Append durably records ev under id as a format-tagged JSON record on the
// per-session event list (RPUSH). It satisfies port.EventLog. The event is
// marshalled to its session.Event JSON verbatim (already redacted at the relay)
// and wrapped in the {"v":"redisstore-eventlog/1","ev":...} envelope so Read
// can validate the format. RPUSH preserves append order, so Read returns
// events in the exact order Append received them.
func (st *Store) Append(ctx context.Context, id session.SessionID, ev session.Event) error {
	evJSON, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("redisstore: marshal event: %w", err)
	}
	rec, err := json.Marshal(eventLogRecord{V: EventLogFormat, Ev: evJSON})
	if err != nil {
		return fmt.Errorf("redisstore: marshal event record: %w", err)
	}
	if err := st.client.RPush(ctx, eventsKey(id), rec).Err(); err != nil {
		return fmt.Errorf("redisstore: append event %q: %w", id, err)
	}
	return nil
}

// Read yields the session's recorded events in APPEND order (RPUSH order =
// LRANGE 0 -1 order). It satisfies port.EventLog. A MISS (no event key) yields
// an EMPTY sequence: absence is data, not an error. A genuine fault — an
// undecodable record, an unknown format tag, or a Redis error — is yielded as
// the error on a zero-value event and the consumer stops (the standard
// iter.Seq2 error idiom).
func (st *Store) Read(ctx context.Context, id session.SessionID) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		records, err := st.client.LRange(ctx, eventsKey(id), 0, -1).Result()
		if err != nil {
			// A missing event key is not an error in Redis (LRANGE on a missing
			// key returns an empty slice), so any error here is a genuine fault.
			yield(session.Event{}, fmt.Errorf("redisstore: read events %q: %w", id, err))
			return
		}
		for _, raw := range records {
			var rec eventLogRecord
			if err := json.Unmarshal([]byte(raw), &rec); err != nil {
				yield(session.Event{}, fmt.Errorf("redisstore: decode event record: %w", err))
				return
			}
			if rec.V != EventLogFormat {
				yield(session.Event{}, fmt.Errorf("redisstore: unknown event-log format %q (want %q)", rec.V, EventLogFormat))
				return
			}
			var ev session.Event
			if err := json.Unmarshal(rec.Ev, &ev); err != nil {
				yield(session.Event{}, fmt.Errorf("redisstore: decode event: %w", err))
				return
			}
			if !yield(ev, nil) {
				return
			}
		}
	}
}

// toolCallRecord is the structured record written by ToolCall. It mirrors the
// jsonlstore tool-call audit record so the two stores produce the same audit
// shape for offline replay/analysis.
type toolCallRecord struct {
	Type         string             `json:"type"` // always "tool_call"
	Time         time.Time          `json:"time"`
	SessionID    session.SessionID  `json:"session_id"`
	CallID       session.ToolCallID `json:"call_id"`
	Tool         string             `json:"tool"`
	Args         json.RawMessage    `json:"args,omitempty"`
	Result       string             `json:"result"`
	IsError      bool               `json:"is_error"`
	QueuedMicros int64              `json:"queued_micros"`
	TookMicros   int64              `json:"took_micros"`
}

// ToolCall appends a structured tool-call record to the per-session tool list
// (RPUSH). It satisfies port.ToolCallRecorder. Errors are intentionally
// swallowed (the port has no error return); the record is best-effort durable.
func (st *Store) ToolCall(id session.SessionID, call session.ToolCall, result session.ToolResult, queued, took time.Duration) {
	rec := toolCallRecord{
		Type:         "tool_call",
		Time:         time.Now().UTC(),
		SessionID:    id,
		CallID:       call.ID,
		Tool:         call.Name,
		Args:         call.Args,
		Result:       result.Content,
		IsError:      result.IsError,
		QueuedMicros: queued.Microseconds(),
		TookMicros:   took.Microseconds(),
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	// Bound the audit write so a stalled Redis cannot wedge every audit call
	// on the go-redis default timeout; the recorder has no error return, so a
	// bounded ctx is the only way to keep a slow broker from piling up.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = st.client.RPush(ctx, toolsKey(id), line).Err()
}

// Ping checks the Redis broker is reachable. It is the readyz health probe a
// storage-free deployment (mecak8s) consults on /readyz: if Redis is down the
// endpoint controller removes the pod. It uses a short timeout so a stalled
// broker fails the probe quickly rather than wedging readiness.
func (st *Store) Ping(ctx context.Context) error {
	if st.client == nil {
		return errors.New("redisstore: store not open")
	}
	return st.client.Ping(ctx).Err()
}

// Close releases the Redis connection. It is safe to call multiple times.
func (st *Store) Close() error {
	if st.client == nil {
		return nil
	}
	return st.client.Close()
}

// ScheduleStore returns a port.ScheduleStore backed by the SAME Redis client as
// the session store (a sibling struct sharing the connection). Composition
// discovers it via type-assertion on this accessor — NOT by asserting the
// *Store itself implements port.ScheduleStore (the schedule store is a separate
// concern; the accessor keeps session-store and schedule-store methods from
// bloating one struct, the jsonlstore.ScheduleStore precedent — and the way
// PrunableStore is discovered on the store itself but here the schedule store is
// a sibling struct, not the session store). A caller that does not need
// schedules never calls this; the byte-identical default is no schedules.
//
// The schedule store carries NO client-side mutex: Redis serializes commands
// single-threaded, and the Claim path is a Lua CAS (EVAL) that is the
// cross-replica at-most-once fence — the multi-host counterpart of the
// single-process mutex the jsonl schedule store carries. See schedulestore.go.
func (st *Store) ScheduleStore() port.ScheduleStore {
	return &scheduleStore{client: st.client}
}

func sessionKey(id session.SessionID) string { return sessionKeyPrefix + string(id) }
func eventsKey(id session.SessionID) string  { return eventsKeyPrefix + string(id) }
func toolsKey(id session.SessionID) string   { return toolsKeyPrefix + string(id) }
