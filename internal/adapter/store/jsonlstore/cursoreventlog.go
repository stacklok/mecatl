package jsonlstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// The two non-event record tags on the .events.jsonl log. Both are siblings of
// eventLogFormat inside the SAME versioned envelope, which is the whole reason a
// gap does not need to be a session.Event: the log already had a place to put a
// record that is not an event.
const (
	// eventLogGenerationTag marks the header record carrying the log's
	// generation. It is written once, as the FIRST record of a new log.
	eventLogGenerationTag = "eventlog-gen/1"

	// eventLogGapTag marks a position where an append is known to have failed.
	eventLogGapTag = "eventlog-gap/1"
)

// eventFollowInterval is how often a following ReadAfter re-stats the log.
//
// ADR 0250 chose size-polling over fsnotify deliberately: attachment is not a
// keystroke-latency path, fsnotify adds dependency surface to a store adapter,
// and it silently does not work on network filesystems — where polling degrades
// identically to the local case. 100ms keeps a live tail feeling immediate.
//
// COST, stated honestly (an earlier version of this comment claimed "one Stat per
// watcher per tenth of a second", which understated it — raised in review on
// #868): each tick runs readEventPage, which takes the snapshot-family lock,
// re-reads the generation header (its own open + tail-size probe), and then opens
// the file again for the page scan. So an idle follower costs roughly two opens,
// two tail probes and a header scan per tick, not one Stat.
//
// That is deliberate rather than merely unfixed. The heavy half is the generation
// re-check, which is what converts "the log was deleted and recreated under a
// live follower" from silently-wrong data into ErrCursorExpired, so gating it
// behind a cheap size/mtime comparison trades a correctness guard for constant
// factors. It stays unoptimised because jsonlstore is not the many-follower
// deployment: mecak8s keeps NO local state and follows through redisstore's
// XREAD (ADR 0048), so jsonlstore followers are local and few. If that changes,
// the gate to add is a stat comparing size, mtime AND inode — inode being the
// part that still catches a recreate — never size alone.
const eventFollowInterval = 100 * time.Millisecond

// compile-time assertion that Store satisfies the cursor port.
var _ port.CursorEventLog = (*Store)(nil)

// AppendEvent durably records ev and returns the cursor positioned after it.
func (st *Store) AppendEvent(ctx context.Context, id session.SessionID, ev session.Event) (port.Cursor, error) {
	evJSON, err := json.Marshal(ev)
	if err != nil {
		return "", fmt.Errorf("jsonlstore: marshal event: %w", err)
	}
	return st.appendLogRecord(ctx, id, eventLogRecord{V: eventLogFormat, Ev: evJSON})
}

// AppendGap durably records a gap marker and returns the cursor positioned after
// it.
func (st *Store) AppendGap(ctx context.Context, id session.SessionID, reason string) (port.Cursor, error) {
	return st.appendLogRecord(ctx, id, eventLogRecord{V: eventLogGapTag, R: reason})
}

// appendLogRecord is the ONE append path for every record kind, so an event, a
// gap, and the generation header can never disagree about ordering, locking, or
// durability. Append (the legacy port.EventLog entry point) routes through it too.
func (st *Store) appendLogRecord(ctx context.Context, id session.SessionID, rec eventLogRecord) (port.Cursor, error) {
	if err := validateSessionID(id); err != nil {
		return "", err
	}
	if err := st.requireAppendDurability(appendStrict); err != nil {
		return "", err
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return "", fmt.Errorf("jsonlstore: marshal event record: %w", err)
	}
	if recordSize := len(line) + 1; recordSize > maxEventRecordSize {
		return "", fmt.Errorf("jsonlstore: event record is %d bytes including newline, exceeds %d-byte limit", recordSize, maxEventRecordSize)
	}

	var (
		generation string
		end        int64
	)
	err = st.withSnapshotFamilyLock(ctx, st.resolver.currentSnapshotPath(id), func() error {
		if err := st.prepareWrite(id); err != nil {
			return err
		}
		gen, present, err := st.eventLogGenerationLocked(id)
		if err != nil {
			return err
		}
		lines := [][]byte{line}
		if !present {
			// A brand-new log. Mint a generation and commit the header WITH the
			// first record in one write: a crash between two separate appends
			// would leave a header whose generation is real but for which no
			// cursor was ever issued.
			gen = newLogGeneration()
			header, herr := json.Marshal(eventLogRecord{V: eventLogGenerationTag, G: gen})
			if herr != nil {
				return fmt.Errorf("marshal generation header: %w", herr)
			}
			lines = [][]byte{header, line}
		}
		generation = gen
		end, err = st.appendRecords(st.resolver.canonicalPath(id, kindEvents), lines, appendStrict)
		return err
	})
	if err != nil {
		return "", err
	}
	return port.EncodeCursor(id, generation, strconv.FormatInt(end, 10)), nil
}

// ReadAfter yields the log's records strictly after the cursor, optionally
// following the tail until ctx is done.
func (st *Store) ReadAfter(ctx context.Context, id session.SessionID, after port.Cursor, opts port.ReadOptions) iter.Seq2[port.LogRecord, error] {
	return func(yield func(port.LogRecord, error) bool) {
		if err := validateSessionID(id); err != nil {
			yield(port.LogRecord{}, err)
			return
		}
		generation, present, err := st.eventLogGeneration(ctx, id)
		if err != nil {
			yield(port.LogRecord{}, err)
			return
		}
		pos, err := port.DecodeCursor(after, id, generation)
		if err != nil {
			yield(port.LogRecord{}, err)
			return
		}
		offset := int64(0)
		if pos != "" {
			if offset, err = strconv.ParseInt(pos, 10, 64); err != nil || offset < 0 {
				yield(port.LogRecord{}, fmt.Errorf("%w: position %q is not a byte offset", port.ErrCursorMalformed, pos))
				return
			}
		}

		// A follower may attach BEFORE the session has appended anything. Its
		// basis is then genuinely unknown rather than empty, and it must ADOPT the
		// generation of whatever log appears — otherwise the log's creation, which
		// is the very event it is waiting for, reads as the basis moving and
		// expires it immediately.
		basis := logBasis{generation: generation, known: present || after != ""}

		var yielded int
		var live bool
		for {
			var batch []port.LogRecord
			var next int64
			batch, next, basis, err = st.readEventPage(ctx, id, basis, offset, opts.Limit-yielded)
			if err != nil {
				yield(port.LogRecord{}, err)
				return
			}
			offset = next
			for _, rec := range batch {
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
			case <-time.After(eventFollowInterval):
			}
		}
	}
}

// logBasis is the generation a read is anchored to, plus whether it is anchored
// at all.
//
// The two are distinct: the EMPTY generation is a real value (a legacy log
// written before generations existed), so "no generation" cannot stand in for
// "no anchor yet". Conflating them is what makes a follower attached to a
// not-yet-created log expire on the log's first append.
type logBasis struct {
	generation string
	known      bool
}

// readEventPage decodes every complete record at or after offset, up to limit
// (0 = unbounded), and returns them with the offset to resume from and the basis
// they were read against.
//
// It re-verifies the generation on every call rather than trusting the one read
// when the follow started: a log that is deleted and recreated under a live
// follower would otherwise have its new records read at the old file's offsets —
// a positional cursor resolving into a different file, which is exactly the
// silent-wrong-data case generations exist to convert into a loud failure.
func (st *Store) readEventPage(ctx context.Context, id session.SessionID, basis logBasis, offset int64, limit int) ([]port.LogRecord, int64, logBasis, error) {
	var (
		f            *os.File
		completeSize int64
	)
	err := st.withSnapshotFamilyLock(ctx, st.resolver.currentSnapshotPath(id), func() error {
		current, present, err := st.eventLogGenerationLocked(id)
		if err != nil {
			return err
		}
		switch {
		case !basis.known && present:
			basis = logBasis{generation: current, known: true}
		case basis.known && current != basis.generation:
			return fmt.Errorf("%w: the log was replaced while reading", port.ErrCursorExpired)
		}
		f, completeSize, err = st.openEventFileLocked(id)
		return err
	})
	if err != nil {
		if errors.Is(err, port.ErrCursorExpired) {
			return nil, 0, basis, err
		}
		return nil, 0, basis, fmt.Errorf("jsonlstore: capture event file: %w", err)
	}
	if f == nil {
		// No log yet. A follower must be able to wait for the first append, so
		// this is an empty page rather than an error.
		return nil, offset, basis, nil
	}
	defer func() { _ = f.Close() }()

	if offset > completeSize {
		return nil, 0, basis, fmt.Errorf("%w: cursor offset %d is past the log's %d committed bytes", port.ErrCursorMalformed, offset, completeSize)
	}
	if err := assertRecordBoundary(f, offset); err != nil {
		return nil, 0, basis, err
	}
	if offset == completeSize {
		return nil, offset, basis, nil
	}

	out, next, err := decodeEventPage(id, f, basis, offset, completeSize, limit)
	if err != nil {
		return nil, 0, basis, err
	}
	return out, next, basis, nil
}

// decodeEventPage decodes every complete record in [offset, completeSize) from f,
// up to limit (0 = unbounded), stamping each with a cursor on basis, and returns
// them with the offset to resume from.
//
// Split out of readEventPage so the capture-and-validate half — which holds the
// snapshot-family lock and owns the generation re-check — reads separately from
// the decode half, which holds no lock and only needs the already-captured
// committed size.
func decodeEventPage(id session.SessionID, f *os.File, basis logBasis, offset, completeSize int64, limit int) ([]port.LogRecord, int64, error) {
	var out []port.LogRecord
	next := offset
	sc := newEventScanner(io.NewSectionReader(f, offset, completeSize-offset))
	for sc.Scan() {
		line := sc.Bytes()
		next += int64(len(line)) + 1
		var rec eventLogRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, 0, fmt.Errorf("jsonlstore: decode event record: %w", err)
		}
		if rec.V == eventLogGenerationTag {
			continue // the header occupies a position but carries no record
		}
		// Every surfaced record's cursor points PAST it, so handing it back
		// yields the next record rather than repeating this one.
		cursor := port.EncodeCursor(id, basis.generation, strconv.FormatInt(next, 10))
		switch rec.V {
		case eventLogGapTag:
			out = append(out, port.LogRecord{
				Kind:      port.LogRecordGap,
				GapReason: rec.R,
				Cursor:    cursor,
			})
		case eventLogFormat:
			var ev session.Event
			if err := json.Unmarshal(rec.Ev, &ev); err != nil {
				return nil, 0, fmt.Errorf("jsonlstore: decode event: %w", err)
			}
			out = append(out, port.LogRecord{
				Kind:   port.LogRecordEvent,
				Event:  ev,
				Cursor: cursor,
			})
		default:
			return nil, 0, fmt.Errorf("jsonlstore: unknown event-log format %q (want %q)", rec.V, eventLogFormat)
		}
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	if err := sc.Err(); err != nil {
		return nil, 0, fmt.Errorf("jsonlstore: scan event file: %w", err)
	}
	return out, next, nil
}

// assertRecordBoundary rejects an offset that does not sit at the start of a
// record.
//
// This is the tamper guard a byte offset needs. A hand-edited offset landing
// mid-record would make the scanner resume on a JSON fragment — which surfaces
// as a decode error, i.e. as corruption rather than as a bad cursor — and an
// offset landing inside a DIFFERENT record's whitespace could silently skip one.
// A record boundary is exactly "offset 0, or the byte before it is a newline".
func assertRecordBoundary(f *os.File, offset int64) error {
	if offset == 0 {
		return nil
	}
	var b [1]byte
	if _, err := f.ReadAt(b[:], offset-1); err != nil {
		return fmt.Errorf("%w: cannot verify cursor offset %d: %v", port.ErrCursorMalformed, offset, err)
	}
	if b[0] != '\n' {
		return fmt.Errorf("%w: cursor offset %d is not at a record boundary", port.ErrCursorMalformed, offset)
	}
	return nil
}

// eventLogGeneration reads the log's generation under the family lock.
func (st *Store) eventLogGeneration(ctx context.Context, id session.SessionID) (generation string, present bool, err error) {
	err = st.withSnapshotFamilyLock(ctx, st.resolver.currentSnapshotPath(id), func() error {
		var lerr error
		generation, present, lerr = st.eventLogGenerationLocked(id)
		return lerr
	})
	return generation, present, err
}

// eventLogGenerationLocked reads the mandatory generation header. An existing
// unversioned log is unsupported and remains untouched.
func (st *Store) eventLogGenerationLocked(id session.SessionID) (generation string, present bool, err error) {
	f, completeSize, err := st.openEventFileLocked(id)
	if err != nil {
		return "", false, err
	}
	if f == nil {
		return "", false, nil
	}
	defer func() { _ = f.Close() }()
	if completeSize == 0 {
		return "", false, nil
	}
	sc := newEventScanner(io.NewSectionReader(f, 0, completeSize))
	if !sc.Scan() {
		if scanErr := sc.Err(); scanErr != nil {
			return "", false, fmt.Errorf("read generation header: %w", scanErr)
		}
		return "", false, nil
	}
	var rec eventLogRecord
	if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
		return "", true, fmt.Errorf("decode generation header: %w", err)
	}
	if rec.V != eventLogGenerationTag || rec.G == "" {
		return "", true, fmt.Errorf("jsonlstore: unsupported unversioned event log; data was left unchanged")
	}
	return rec.G, true, nil
}

// openEventFileLocked opens the session's readable event file and returns it
// with the offset of its last committed record. A missing log returns a nil file
// and no error — absence is data. Callers hold the family lock and own closing
// the returned handle.
func (st *Store) openEventFileLocked(id session.SessionID) (*os.File, int64, error) {
	path, present, err := st.resolver.readablePath(id, kindEvents)
	if err != nil || !present {
		return nil, 0, err
	}
	root, err := os.OpenRoot(st.resolver.canonicalDir())
	if err != nil {
		return nil, 0, fmt.Errorf("open event root: %w", err)
	}
	name := filepath.Base(path)
	defer func() { _ = root.Close() }()

	f, err := openRegular(root, name)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("open event file: %w", err)
	}
	completeSize, err := completeRecordSize(f)
	if err != nil {
		_ = f.Close()
		return nil, 0, fmt.Errorf("inspect event file tail: %w", err)
	}
	return f, completeSize, nil
}

// newLogGeneration mints an opaque token identifying a log's positional basis.
// Random rather than derived from the path or a counter: it is compared across
// processes and restarts, where anything process-local would collide.
func newLogGeneration() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
