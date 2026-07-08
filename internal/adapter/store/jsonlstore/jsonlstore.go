// Package jsonlstore implements an append-only, JSONL-backed port.SessionStore,
// port.ToolCallRecorder (the tool-call audit seam), and port.EventLog (the
// durable run-event timeline). It is the observability/replay seam: every Save
// appends a session snapshot as one JSON line to a per-session file, every
// ToolCall appends a structured tool-call record to a per-session log, and every
// Append records one relayed event to a per-session event log. Nothing is ever
// overwritten, so the files form a replayable audit trail; Load reads the most
// recent snapshot line, while EventLog.Read scans ALL event lines cumulatively.
//
// Layout under the configured dir:
//
//	<dir>/<id>.session.jsonl   — one snapshot per Save (latest line wins)
//	<dir>/<id>.tools.jsonl     — one record per ToolCallRecorder.ToolCall
//	<dir>/<id>.events.jsonl    — one record per EventLog.Append (cumulative)
//
// The .events.jsonl log is PARALLEL to (not a superset of) .tools.jsonl: the
// tool log is the structured per-tool AUDIT seam (args, queue/exec timing), the
// event log is the relayed STREAM (reasoning, ask/verdict pairs, delegation
// lifecycle) the server otherwise discards. Neither subsumes the other.
//
// Session ids are sanitized for use as filenames so an id can never escape dir.
package jsonlstore

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
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
	dir string
	mu  sync.Mutex // serializes appends across files
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
	return &Store{dir: dir}, nil
}

// Save appends a snapshot of s as a single JSON line to the session file.
func (st *Store) Save(_ context.Context, s *session.Session) error {
	if s == nil {
		return sessnap.ErrNilSession
	}
	line, err := sessnap.Marshal(s)
	if err != nil {
		return err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return appendLine(st.sessionPath(s.ID), line)
}

// Load reads the session file and reconstructs the latest snapshot line.
func (st *Store) Load(_ context.Context, id session.SessionID) (*session.Session, error) {
	path := st.sessionPath(id)
	f, err := os.Open(path) //nolint:gosec // path is sanitized via sessionPath
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
		}
		return nil, fmt.Errorf("jsonlstore: open session file: %w", err)
	}
	defer func() { _ = f.Close() }()

	var last []byte
	last, err = scanLastNonBlankLine(f)
	if err != nil {
		return nil, fmt.Errorf("jsonlstore: scan session file: %w", err)
	}
	if last == nil {
		return nil, fmt.Errorf("%w: %q (empty file)", ErrNotFound, id)
	}
	return sessnap.Unmarshal(last)
}

// scanLastNonBlankLine returns the last non-blank line of f (seeking to 0 first).
// It is the shared latest-line-wins snapshot read that Load, decodeSessionID, and
// readLastLineFull (the metalist fallback) all call — ONE scan discipline across
// the three "read the latest snapshot line" sites, so a format/scan change fixes
// once and the picker's fast path cannot drift from Load's truth. The caller owns
// the returned slice (a fresh copy; the scanner's buffer is reused internally).
func scanLastNonBlankLine(f *os.File) ([]byte, error) {
	var last []byte
	sc := newScanner(f)
	for sc.Scan() {
		b := sc.Bytes()
		if len(strings.TrimSpace(string(b))) == 0 {
			continue
		}
		last = append(last[:0], b...) // copy: scanner reuses its buffer
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return last, nil
}

// newScanner builds a bufio.Scanner over f with the generous buffer a snapshot
// line can need (a snapshot carrying a large conversation can be multi-MB).
func newScanner(f *os.File) *bufio.Scanner {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
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

// List returns every stored session's id and last-modified time (the session
// file's mtime). It satisfies the optional port.PrunableStore retention seam.
//
// COST: safeName is NOT invertible (distinct ids can collide onto one
// filename, and a sanitized rune cannot be restored), so the REAL id is
// decoded from each session file's last snapshot line (the same latest-line
// the Load path trusts) rather than derived from the filename. That makes
// List O(total store bytes) in the worst case — acceptable for a
// retention sweep that runs on a startup/hourly cadence, not a hot path.
//
// List enumerates ONLY *.session.jsonl files: a .tools.jsonl sidecar without
// its session file (an orphan from a pre-fix partial Delete, or hand-pruning)
// is invisible here and is never swept — accepted as unreachable. Delete's
// tools-first removal order prevents this store from creating new ones.
func (st *Store) List(_ context.Context) ([]port.StoredSession, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	entries, err := os.ReadDir(st.dir)
	if err != nil {
		return nil, fmt.Errorf("jsonlstore: list store dir: %w", err)
	}
	var out []port.StoredSession
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), sessionFileSuffix) {
			continue
		}
		path := filepath.Join(st.dir, e.Name())
		id, err := decodeSessionID(path)
		if err != nil {
			// A truncated/empty/corrupt session file has no decodable id; skip
			// it rather than fail the whole inventory (Load of that id would
			// fail the same way). Best-effort listing, like the sweep itself.
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // raced with a concurrent delete; tolerate it
		}
		out = append(out, port.StoredSession{ID: id, ModifiedAt: info.ModTime()})
	}
	return out, nil
}

// Delete removes the session's snapshot file AND its tool-call log. It is
// idempotent: a missing file is success (port.PrunableStore contract), so
// concurrent List/Delete races are tolerated by construction.
//
// REMOVAL ORDER is load-bearing: the sidecars (tools, then events) go FIRST and
// the session file LAST, because the session file is what List enumerates. A
// partial failure then leaves the set still VISIBLE (the session file survives,
// so the next retention sweep retries the whole Delete); the reverse order would
// leave an INVISIBLE orphaned sidecar that no future sweep can ever find (List
// ignores sidecars without a session file — a pre-existing orphan is accepted as
// unreachable; this ordering prevents us from ever creating one).
func (st *Store) Delete(_ context.Context, id session.SessionID) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, path := range []string{st.toolsPath(id), st.eventsPath(id), st.sessionPath(id)} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("jsonlstore: delete %q: %w", id, err)
		}
	}
	return nil
}

// decodeSessionID reads the REAL session id out of a session file's latest
// snapshot line (only the "id" field is decoded; the rest of the snapshot is
// skipped). It mirrors Load's latest-line-wins read.
func decodeSessionID(path string) (session.SessionID, error) {
	f, err := os.Open(path) //nolint:gosec // path is derived from the store dir listing
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	last, err := scanLastNonBlankLine(f)
	if err != nil {
		return "", err
	}
	if last == nil {
		return "", fmt.Errorf("jsonlstore: %s: empty session file", path)
	}
	var head struct {
		ID session.SessionID `json:"id"`
	}
	if err := json.Unmarshal(last, &head); err != nil {
		return "", fmt.Errorf("jsonlstore: %s: decode snapshot id: %w", path, err)
	}
	if head.ID == "" {
		return "", fmt.Errorf("jsonlstore: %s: snapshot carries no id", path)
	}
	return head.ID, nil
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
	_ = appendLine(st.toolsPath(id), line)
}

// Append records one relayed event under id as a format-tagged JSON line on the
// per-session event log. It satisfies port.EventLog. The event is marshalled to
// its session.Event JSON verbatim (already redacted at the relay) and wrapped in
// the {"v":"eventlog-json/1","ev":...} envelope so Read can validate the format.
// It reuses the same mu/appendLine as Save/ToolCall (one serialized writer per
// Store), and is best-effort durable: the relay logs a WARN on a returned error
// and never aborts the run.
func (st *Store) Append(_ context.Context, id session.SessionID, ev session.Event) error {
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
	return appendLine(st.eventsPath(id), line)
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
		f, err := os.Open(st.eventsPath(id)) //nolint:gosec // path is sanitized via eventsPath
		if err != nil {
			if os.IsNotExist(err) {
				return // miss → empty sequence (absence is data, not an error)
			}
			yield(session.Event{}, fmt.Errorf("jsonlstore: open event file: %w", err))
			return
		}
		defer func() { _ = f.Close() }()

		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
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

func (st *Store) sessionPath(id session.SessionID) string {
	return filepath.Join(st.dir, safeName(id)+sessionFileSuffix)
}

func (st *Store) toolsPath(id session.SessionID) string {
	return filepath.Join(st.dir, safeName(id)+toolsFileSuffix)
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
	return &scheduleStore{dir: st.dir, mu: &st.mu}
}

func (st *Store) eventsPath(id session.SessionID) string {
	return filepath.Join(st.dir, safeName(id)+eventsFileSuffix)
}

// appendLine appends b followed by a newline to the file at path, opening it
// for append (creating it if needed). Each line is a complete JSON record.
func appendLine(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // path is sanitized
	if err != nil {
		return fmt.Errorf("jsonlstore: open for append: %w", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("jsonlstore: append: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("jsonlstore: close after append: %w", err)
	}
	return nil
}

// safeName maps a SessionID to a filename-safe token so it cannot traverse out
// of the store dir. Any rune that is not alphanumeric, '-', '_' or '.' becomes
// '_'. A leading '.' is also neutralized.
func safeName(id session.SessionID) string {
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
