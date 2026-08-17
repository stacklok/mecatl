// Package jsonlstore implements an append-only, JSONL-backed port.SessionStore,
// port.ToolCallRecorder (the tool-call audit seam), and port.EventLog (the
// durable run-event timeline). It is the observability/replay seam: every Save
// appends a session snapshot as one JSON line to a per-session file, every
// ToolCall appends a structured tool-call record to a per-session log, and every
// Append records one relayed event to a per-session event log. Nothing is ever
// overwritten, so the files form a replayable audit trail; Load reads the most
// recent snapshot line, while EventLog.Read scans ALL event lines cumulatively.
//
// SESSION-FAMILY NAMING. Each session's three files share a family stem under
// the owner-only `sid-v1` subdirectory. The reversible token is `sid-v1-` plus
// Raw URL-base64 of the complete opaque valid-UTF-8 session id:
//
//	<dir>/sid-v1/sid-v1-<token>.session.jsonl
//	<dir>/sid-v1/sid-v1-<token>.tools.jsonl
//	<dir>/sid-v1/sid-v1-<token>.events.jsonl
//
// A pre-rewrite family may still use the lossy legacySafeName stem. The
// sessionResolver in resolve.go is the single authority for canonical/legacy
// paths, ownership checks, and write-time migration. Reads never migrate.
//
// The .events.jsonl log is PARALLEL to (not a superset of) .tools.jsonl: the
// tool log is the structured per-tool AUDIT seam (args, queue/exec timing), the
// event log is the relayed STREAM (reasoning, ask/verdict pairs, delegation
// lifecycle) the server otherwise discards. Neither subsumes the other.
//
// Physical names are confined to filename-safe tokens under the store dir.
package jsonlstore

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// ErrNotFound is returned by Load when no snapshot file exists for the id. It wraps
// port.ErrSessionNotFound so a consumer that may not import this adapter can
// distinguish not-found from an infra failure via errors.Is.
var ErrNotFound = fmt.Errorf("jsonlstore: session not found: %w", port.ErrSessionNotFound)

// Store is an append-only JSONL SessionStore and ToolCallRecorder rooted at a directory.
type Store struct {
	// resolver is the single authority for canonical and legacy family paths
	// (including the plain root dir, resolver.dir — Store has no separate
	// copy of it).
	resolver sessionResolver
	mu       sync.Mutex // serializes appends across files
}

// compile-time assertions that Store satisfies both ports plus the optional
// retention seam and the durable event log.
var (
	_ port.SessionStore     = (*Store)(nil)
	_ port.ToolCallRecorder = (*Store)(nil)
	_ port.PrunableStore    = (*Store)(nil)
	_ port.EventLog         = (*Store)(nil)
)

// New constructs a Store writing under dir, creating dir if needed. The dir is
// created at mode 0700: the store holds raw conversation transcripts (session
// snapshots, tool-call args/results, and the relayed event stream) in plaintext,
// so it is owner-only by construction.
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("jsonlstore: create dir: %w", err)
	}
	resolver := sessionResolver{dir: dir}
	if err := os.MkdirAll(resolver.canonicalDir(), 0o700); err != nil {
		return nil, fmt.Errorf("jsonlstore: create canonical dir: %w", err)
	}
	return &Store{resolver: resolver}, nil
}

// Save appends a snapshot of s as a single JSON line to the session file.
//
// New writes target the canonical versioned-token path. Before writing, the
// resolver lazily migrates a MATCHING legacy family (one whose legacy snapshot
// carries an id exactly equal to s.ID) forward — sidecars first, snapshot
// last — so the new snapshot line appends to the canonical file carrying the
// migrated history in order and the legacy files are removed. A legacy family
// whose embedded id does NOT match s.ID is left untouched (it belongs to a
// different session that the lossy stem happened to collide with).
func (st *Store) Save(_ context.Context, s *session.Session) error {
	if s == nil {
		return sessnap.ErrNilSession
	}
	if err := validateSessionID(s.ID); err != nil {
		return err
	}
	line, err := sessnap.Marshal(s)
	if err != nil {
		return err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := st.resolver.prepareWrite(s.ID); err != nil {
		return err
	}
	return appendLine(st.resolver.canonicalPath(s.ID, kindSnapshot), line)
}

// Load reads the authoritative snapshot without modifying storage. Canonical
// presence prevents fallback; legacy is accepted only when its latest embedded
// id exactly matches the requested id.
func (st *Store) Load(_ context.Context, id session.SessionID) (*session.Session, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	line, err := st.resolver.loadSnapshot(id)
	if err != nil {
		return nil, err
	}
	return sessnap.Unmarshal(line)
}

// maxScannerTokenSize is also the reverse reader's latest-record ceiling.
const maxScannerTokenSize = 16 * 1024 * 1024

// newScanner builds a bufio.Scanner over f with the generous buffer a snapshot
// or event-log line can need.
func newScanner(f *os.File) *bufio.Scanner {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxScannerTokenSize)
	return sc
}

// sessionFileSuffix / toolsFileSuffix are the per-session file suffixes under
// dir (see the package doc layout).
const (
	sessionFileSuffix = ".session.jsonl"
	toolsFileSuffix   = ".tools.jsonl"
	eventsFileSuffix  = ".events.jsonl"
)

// EventLogFormat is the per-record format tag written on every event-log line.
// It versions the on-disk encoding so the language-neutral driver wire (3c) and
// any future format change can be distinguished; Read rejects an unknown tag as
// an infra error (a forward-incompatible log must fail loud, not silently skip).
//
// It is EXPORTED so the gRPC driver wire (`internal/adapter/grpcdriver.EventLogFormat`)
// can be pinned EQUAL to it by a test: the wire payload is exactly this record's
// "ev" bytes (json.Marshal of a session.Event), so the two tags MUST agree or a
// log written by one path is unreadable by the other (the one-codec claim). The
// unexported alias keeps the in-file call sites terse.
const EventLogFormat = "eventlog-json/1"

// eventLogFormat is the in-file alias of EventLogFormat (keeps the existing call
// sites terse; the two are the one constant).
const eventLogFormat = EventLogFormat

// eventLogRecord is one .events.jsonl line: a format tag plus the verbatim
// session.Event JSON. The event is stored as already-redacted JSON (the relay is
// the redaction boundary); the tag lets Read validate the encoding version.
type eventLogRecord struct {
	V  string          `json:"v"`
	Ev json.RawMessage `json:"ev"`
}

// List returns one row per logical session id. IDs come from latest snapshots,
// never filenames; canonical files win when canonical and legacy coexist.
//
// COST: ids are decoded from each session file's latest snapshot line rather
// than from filenames (a filename is not invertible back to the id, and now
// there are two directories — canonical and legacy — to reconcile), so List
// costs one directory read per dir plus one reverse TAIL read per snapshot
// file. The reader grows its EOF window only to the latest record, never scanning
// older snapshot history. Fine for a retention sweep on a startup/hourly cadence;
// indexed inventory is a separate concern.
//
// List enumerates ONLY *.session.jsonl files, which is what makes the
// sidecars-before-snapshot removal order load-bearing (see familyOrder in
// resolve.go): a .tools.jsonl or .events.jsonl sidecar without its session
// file is invisible here and can never be swept. A pre-existing orphan is
// accepted as unreachable; Delete's ordering prevents this store from
// creating new ones.
func (st *Store) List(_ context.Context) ([]port.StoredSession, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	files, err := st.resolver.snapshotFiles()
	if err != nil {
		return nil, err
	}
	out := make([]port.StoredSession, 0, len(files))
	for _, file := range files {
		out = append(out, port.StoredSession{ID: file.id, ModifiedAt: file.modified})
	}
	return out, nil
}

// Delete removes canonical sidecars before the canonical snapshot, and a
// legacy family in the same order — REMOVAL ORDER is load-bearing, see
// familyOrder's doc comment (resolve.go): the snapshot file is what List
// enumerates, so removing it last means a partial failure leaves the family
// still VISIBLE (the next retention sweep retries it), where the reverse
// order would leave an invisible orphaned sidecar no sweep could ever find.
// With two families (canonical + legacy) this now has to hold TWICE per
// call: canonical sidecars before the canonical snapshot, AND — only when
// the legacy snapshot's embedded id proves it belongs to this session —
// legacy sidecars before the legacy snapshot. A legacy family that fails
// ownership (mismatch or absent) is left untouched; that mismatch and
// absence are both idempotent success.
//
// The canonical family is removed on PRESENCE alone, never on the snapshot
// parsing: the token is injective, so the file is ours whatever it contains,
// and gating removal on validity made a torn snapshot line permanently
// unprunable — every retention sweep re-failed on it while List, which skips
// undecodable files, never surfaced it. port.PrunableStore requires that a
// Delete either remove or be idempotent success, so an unreadable snapshot
// must not be a third outcome.
func (st *Store) Delete(_ context.Context, id session.SessionID) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	canonicalOwned, err := st.resolver.canonicalOwnership(id)
	if err != nil {
		return err
	}
	legacyOwned, err := st.resolver.legacyOwned(id)
	if err != nil {
		return err
	}
	for _, kind := range sidecarKinds {
		if err := removeSessionFile(st.resolver.canonicalPath(id, kind)); err != nil {
			return fmt.Errorf("jsonlstore: delete %q: %w", id, err)
		}
		if legacyOwned {
			if err := removeSessionFile(st.resolver.legacyPath(id, kind)); err != nil {
				return fmt.Errorf("jsonlstore: delete legacy %q: %w", id, err)
			}
		}
	}
	if canonicalOwned {
		if err := removeSessionFile(st.resolver.canonicalPath(id, kindSnapshot)); err != nil {
			return fmt.Errorf("jsonlstore: delete %q: %w", id, err)
		}
	}
	if legacyOwned {
		if err := removeSessionFile(st.resolver.legacyPath(id, kindSnapshot)); err != nil {
			return fmt.Errorf("jsonlstore: delete legacy %q: %w", id, err)
		}
	}
	return nil
}

func removeSessionFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// toolCallRecord is the structured line written by ToolCall. It is a flat,
// self-describing record for offline replay/analysis.
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

// ToolCall appends a structured tool-call record to the per-session tool log,
// including both the dispatch queue time (queued) and the execution wall time
// (took) in microseconds. It satisfies port.ToolCallRecorder. Errors are intentionally
// swallowed (the port has no error return) but the record is best-effort durable.
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
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := st.resolver.prepareWrite(id); err != nil {
		return
	}
	_ = appendLine(st.resolver.canonicalPath(id, kindTools), line)
}

// Append records one relayed event under id as a format-tagged JSON line on the
// per-session event log. It satisfies port.EventLog. The event is marshalled to
// its session.Event JSON verbatim (already redacted at the relay) and wrapped in
// the {"v":"eventlog-json/1","ev":...} envelope so Read can validate the format.
// It reuses the same mu/appendLine as Save/ToolCall (one serialized writer per
// Store), and is best-effort durable: the relay logs a WARN on a returned error
// and never aborts the run.
func (st *Store) Append(_ context.Context, id session.SessionID, ev session.Event) error {
	if err := validateSessionID(id); err != nil {
		return err
	}
	evJSON, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("jsonlstore: marshal event: %w", err)
	}
	line, err := json.Marshal(eventLogRecord{V: eventLogFormat, Ev: evJSON})
	if err != nil {
		return fmt.Errorf("jsonlstore: marshal event record: %w", err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := st.resolver.prepareWrite(id); err != nil {
		return err
	}
	return appendLine(st.resolver.canonicalPath(id, kindEvents), line)
}

// Read scans the per-session event log and yields every recorded event in append
// order (cumulative — NOT latest-line-wins like the snapshot read). It satisfies
// port.EventLog. A MISS (no event file) yields an EMPTY sequence: absence is data.
// A genuine fault — an undecodable record, an unknown format tag, or an I/O error
// — is yielded as the error on a zero-value event and the consumer stops (the
// iterator returns after the consumer's range body returns false on the error
// item, the standard iter.Seq2 error idiom).
func (st *Store) Read(_ context.Context, id session.SessionID) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		st.mu.Lock()
		path, present, err := st.resolver.readablePath(id, kindEvents)
		st.mu.Unlock()
		if err != nil {
			yield(session.Event{}, fmt.Errorf("jsonlstore: resolve event file: %w", err))
			return
		}
		if !present {
			return
		}
		f, err := os.Open(path) //nolint:gosec // resolver-derived path
		if err != nil {
			if os.IsNotExist(err) {
				return
			}
			yield(session.Event{}, fmt.Errorf("jsonlstore: open event file: %w", err))
			return
		}
		defer func() { _ = f.Close() }()

		sc := newScanner(f)
		for sc.Scan() {
			b := sc.Bytes()
			if len(strings.TrimSpace(string(b))) == 0 {
				continue
			}
			var rec eventLogRecord
			if err := json.Unmarshal(b, &rec); err != nil {
				yield(session.Event{}, fmt.Errorf("jsonlstore: decode event record: %w", err))
				return
			}
			if rec.V != eventLogFormat {
				yield(session.Event{}, fmt.Errorf("jsonlstore: unknown event-log format %q (want %q)", rec.V, eventLogFormat))
				return
			}
			var ev session.Event
			if err := json.Unmarshal(rec.Ev, &ev); err != nil {
				yield(session.Event{}, fmt.Errorf("jsonlstore: decode event: %w", err))
				return
			}
			if !yield(ev, nil) {
				return
			}
		}
		if err := sc.Err(); err != nil {
			yield(session.Event{}, fmt.Errorf("jsonlstore: scan event file: %w", err))
		}
	}
}

// ScheduleStore returns a port.ScheduleStore backed by the SAME directory as
// the session store (a sibling struct sharing the dir + the single-process
// mutex). Composition discovers it via type-assertion on this accessor — NOT
// by asserting the *Store itself implements port.ScheduleStore (the schedule
// store is a separate concern; the accessor keeps session-store and
// schedule-store methods from bloating one struct, the way PrunableStore is
// discovered on the store itself but here the schedule store is a sibling
// struct, not the session store). A caller that does not need schedules never
// calls this; the byte-identical default is no schedules.
func (st *Store) ScheduleStore() port.ScheduleStore {
	return &scheduleStore{dir: st.resolver.dir, mu: &st.mu}
}

// appendLine appends b followed by a newline to the file at path, opening it
// for append (creating it if needed). Each line is a complete JSON record.
//
// TORN-TAIL REPAIR. A write is one unsynced Write of record+'\n', so a crash,
// SIGKILL or ENOSPC partway through leaves a truncated final line with NO
// terminating newline. Appending straight onto that would GLUE the next record
// to the fragment, so one interrupted write would corrupt the following record
// too — turning a damaged tail into a permanently unreadable file, and (for the
// snapshot) destroying the very append that would otherwise have repaired it,
// since Load is last-line-wins. So: if the file is non-empty and does not end
// in '\n', emit a leading newline first. The fragment then stands as its own
// line, where scanLastNonBlankLine and readLastLine both look PAST it to the
// record just written. Damage stays bounded to the one interrupted record.
func appendLine(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o644) //nolint:gosec // path is sanitized
	if err != nil {
		return fmt.Errorf("jsonlstore: open for append: %w", err)
	}
	rec := make([]byte, 0, len(b)+2)
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("jsonlstore: stat for append: %w", err)
	}
	if size := info.Size(); size > 0 {
		var tail [1]byte
		if _, err := f.ReadAt(tail[:], size-1); err != nil {
			_ = f.Close()
			return fmt.Errorf("jsonlstore: read tail for append: %w", err)
		}
		if tail[0] != '\n' {
			rec = append(rec, '\n')
		}
	}
	rec = append(rec, b...)
	rec = append(rec, '\n')
	if _, err := f.Write(rec); err != nil {
		_ = f.Close()
		return fmt.Errorf("jsonlstore: append: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("jsonlstore: close after append: %w", err)
	}
	return nil
}

// legacySafeName maps a SessionID to the pre-v1 filename-safe token. It is
// intentionally LOSSY and retained only for legacy session-family discovery and
// schedule-store compatibility. Any rune that is not alphanumeric, '-', '_' or
// '.' becomes '_'. A leading '.' is also neutralized.
func legacySafeName(id session.SessionID) string {
	s := string(id)
	if s == "" {
		return "_empty_"
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if strings.HasPrefix(out, ".") {
		out = "_" + out[1:]
	}
	return out
}
