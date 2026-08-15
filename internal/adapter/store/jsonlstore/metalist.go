package jsonlstore

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// metaSnapshot is the SMALL decode target for a cheap picker listing: it
// carries ONLY the fields a /sessions row needs (id, state, counters.turns,
// model id, title, created_at) and SKIPS the messages array entirely. Go's
// encoding/json ignores unknown fields, so json.Unmarshal(lastLine, &meta) into
// this struct parses the JSON but never materializes the (large) conversation.
// The json tags mirror sessnap.Snapshot's so the wire keys agree exactly —
// pinned by TestMetaSnapshotTagsAreSessnapSubset (a reflection tripwire so the
// mirror cannot silently drift).
type metaSnapshot struct {
	ID              session.SessionID           `json:"id"`
	State           session.State               `json:"state"`
	Counters        session.Counters            `json:"counters"`
	ModelID         string                      `json:"model_id,omitempty"`
	Title           string                      `json:"title,omitempty"`
	TitleProvenance session.TitleProvenance     `json:"title_provenance,omitempty"`
	Kind            session.SessionKind         `json:"kind,omitempty"`
	Relationship    session.SessionRelationship `json:"relationship,omitzero"`
	Workspace       string                      `json:"workspace"`
	CreatedAt       time.Time                   `json:"created_at"`
	// Owner is the session's verified owner (ADR 0100). Decoding it here is
	// what keeps the cheap fast path's row IDENTICAL to the Load-per-row
	// fallback's; a pre-owner snapshot simply has no key and stays nil.
	Owner *session.Principal `json:"owner,omitempty"`
}

// knownStates is the set of valid session.State values. MetaList validates the
// decoded state against it so a snapshot carrying an unknown state (a corrupt
// or forward-incompatible file) zeroes the snapshot-derived fields, matching
// the Load-fails behaviour: the row still surfaces its id/mtime, but State/
// Turns/ModelID/CreatedAt/Title are empty (a corrupt snapshot is visible in the
// picker with its id/mtime even if it can't be opened).
//
// This is a mirror of the session package's own state set (the authority is
// RestoreState's transition validation). It is pinned by
// TestKnownStatesMatchSessionPackage (a test-only import of engine/session) so
// a new session.State landing there cannot silently make the picker zero a
// valid snapshot here.
var knownStates = map[session.State]bool{
	session.StateIdle:      true,
	session.StateRunning:   true,
	session.StateAwaiting:  true,
	session.StateCompleted: true,
	session.StateFailed:    true,
	session.StateCancelled: true,
}

// lastLineSeekWindow is the tail-read window for readLastLine. A snapshot line
// is one JSON record per Save; for a long-running session the file grows, but
// the LATEST line is always at the END. Reading the last window (64 KiB) and
// finding the final newline avoids scanning a multi-megabyte file. A line
// longer than the window (a pathological single Save larger than 64 KiB — a
// huge multimodal message) falls back to the full-scan path so it is never
// silently truncated.
const lastLineSeekWindow = 64 * 1024

// compile-time assertions that Store satisfies the optional metadata seams.
var (
	_ port.MetaLister           = (*Store)(nil)
	_ port.SessionMetadataPager = (*Store)(nil)
)

// MetaList returns every stored session's picker metadata by reading ONLY the
// last snapshot line of each *.session.jsonl file and decoding into a small
// struct that skips the messages array. It satisfies port.MetaLister.
//
// It is the CHEAP-listing path Service.ListSessions prefers (via type
// assertion) over the Load-per-row fallback: listing N sessions is
// O(N × last-line-read) instead of O(N × filesize), because each file is
// tail-read (seek near the end, find the last newline) rather than fully
// scanned, and the unmarshal skips the conversation entirely. A file smaller
// than the seek window is read whole (small file = fast). It reuses the same mu
// as List/Save (one serialized reader per Store).
//
// The last line is the LATEST snapshot (append-only, latest-line-wins), so the
// metadata reflects the session's CURRENT state/turns/model/title, exactly as a
// full Load would.
//
// CORRUPT-ROW CONTRACT (mirrors the Load-per-row path): a row whose last line
// decodes a valid id but CANNOT be fully restored (an unknown state
// RestoreState would reject, a missing id, or undecodable JSON) still surfaces
// with its id + modified_at but ZEROED snapshot-derived fields — the same
// behaviour ListSessions had when Load failed per row. So a corrupt snapshot
// file is visible in the picker with its id/mtime even if it can't be opened,
// exactly as before.
func (st *Store) MetaList(ctx context.Context) ([]port.SessionMeta, error) {
	rows, err := st.discoveryMetaList(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]port.SessionMeta, 0, len(rows))
	for _, row := range rows {
		out = append(out, port.SessionMeta{
			ID: row.ID, ModifiedAt: row.ModifiedAt, State: row.State, Turns: row.Turns,
			ModelID: row.ModelID, CreatedAt: row.CreatedAt, Title: row.Title, Owner: row.Owner,
		})
	}
	return out, nil
}

func (st *Store) discoveryMetaList(_ context.Context) ([]port.SessionDiscoveryMeta, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	files, err := st.resolver.snapshotFiles()
	if err != nil {
		return nil, err
	}
	out := make([]port.SessionDiscoveryMeta, 0, len(files))
	for _, file := range files {
		meta := port.SessionDiscoveryMeta{ID: file.id, ModifiedAt: file.modified}
		var m metaSnapshot
		if err := json.Unmarshal(file.last, &m); err == nil && knownStates[m.State] {
			kind := m.Kind
			if kind == "" {
				kind = session.SessionKindUnknown
			}
			if session.ValidateSessionMetadata(kind, m.Relationship) != nil {
				out = append(out, meta)
				continue
			}
			meta.State = m.State
			meta.Turns = m.Counters.Turns
			meta.ModelID = m.ModelID
			meta.Title = m.Title
			meta.TitleProvenance = m.TitleProvenance
			meta.Workspace = m.Workspace
			meta.Kind = kind
			meta.Relationship = m.Relationship
			meta.Owner = m.Owner
			meta.CreatedAt = m.CreatedAt
		}
		out = append(out, meta)
	}
	return out, nil
}

// PageSessionMetadata scans the latest-line metadata projection, then applies
// the shared owner-filtered keyset contract. The response is bounded even
// though this v1 adapter may scan all snapshot files.
func (st *Store) PageSessionMetadata(ctx context.Context, request port.SessionMetadataPageRequest) (port.SessionMetadataPage, error) {
	rows, err := st.discoveryMetaList(ctx)
	if err != nil {
		return port.SessionMetadataPage{}, err
	}
	return port.PaginateSessionMetadata(rows, request), nil
}

// readLastLine returns the last complete line of the file at path. It seeks
// near the end (last lastLineSeekWindow bytes) and scans backward for the final
// newline, so a multi-megabyte append-only session file is NOT read in full —
// only its tail. A file smaller than the window is read whole. A line longer
// than the window (a single snapshot larger than 64 KiB) falls back to a full
// forward scan so it is never silently truncated.
func readLastLine(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // path is derived from the store dir listing
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()
	if size <= int64(lastLineSeekWindow) {
		// Small file: read whole, then find the last non-blank line.
		return readLastLineFull(f)
	}

	// Seek to (size - window) and read the tail. We back up one extra byte when
	// possible so a newline exactly at the seek boundary doesn't hide the line
	// before it (the line is the one AFTER a newline).
	seek := size - int64(lastLineSeekWindow)
	if seek > 0 {
		seek-- // include a possible boundary newline
	}
	if _, err := f.Seek(seek, 0); err != nil {
		return nil, err
	}
	tail, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	// Find the last complete line: drop a trailing newline, then cut at the last
	// newline. The bytes before the last newline are earlier (complete) lines;
	// the bytes after it (to the trimmed end) are the final line.
	tail = bytes.TrimRight(tail, "\r\n")
	if i := bytes.LastIndexByte(tail, '\n'); i >= 0 {
		return tail[i+1:], nil
	}
	// The window contains a single (very long) line — longer than the seek
	// window. Fall back to a full forward scan so it is read in full, never
	// truncated. The cursor is at EOF after the tail ReadAll, so re-seek to 0
	// (readLastLineFull does the same — scanLastNonBlankLine reads from the
	// current position).
	return readLastLineFull(f)
}

// readLastLineFull scans the whole file forward, keeping the last non-blank
// line. It is the fallback for a small file or a single line longer than the
// seek window. Delegates to the shared scanLastNonBlankLine so the scan
// discipline stays in ONE place (Load/decodeSessionID/MetaList all share it).
func readLastLineFull(f *os.File) ([]byte, error) {
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	return scanLastNonBlankLine(f)
}
