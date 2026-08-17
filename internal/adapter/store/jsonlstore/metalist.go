package jsonlstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	// Owner is the session's verified owner (ADR 0204). Decoding it here is
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

// readLastLine returns the last non-blank record without reading older history.
// It grows an EOF window geometrically until it finds the delimiter immediately
// before that record, so bytes read and allocated are bounded by a small constant
// factor of the latest record rather than by the append-only file's total size.
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
	return readLastLineAt(f, st.Size())
}

func readLastLineAt(r io.ReaderAt, size int64) ([]byte, error) {
	if size == 0 {
		return nil, nil
	}
	window := int64(lastLineSeekWindow)
	// Leave room for the delimiter before a maximum-sized record plus ordinary
	// trailing blank lines. The record itself remains capped below.
	maxWindow := int64(maxScannerTokenSize + lastLineSeekWindow)
	if window > size {
		window = size
	}
	for {
		start := size - window
		tail := make([]byte, window)
		if _, err := r.ReadAt(tail, start); err != nil {
			return nil, err
		}
		if line, complete, err := lastNonBlankRecord(tail, start == 0); complete || err != nil {
			return line, err
		}
		if window >= maxWindow || window >= size {
			return nil, fmt.Errorf("jsonlstore: latest record exceeds %d bytes", maxScannerTokenSize)
		}
		window *= 2
		if window > maxWindow {
			window = maxWindow
		}
		if window > size {
			window = size
		}
	}
}

// lastNonBlankRecord searches one EOF window from newest to oldest. The first
// segment is usable only when its leading boundary is known (a newline in this
// window, or the beginning of the file); otherwise the caller must grow the
// window because that segment may be a truncated suffix of a larger record.
func lastNonBlankRecord(tail []byte, startsAtFileBeginning bool) ([]byte, bool, error) {
	end := len(tail)
	for {
		newline := bytes.LastIndexByte(tail[:end], '\n')
		start := newline + 1
		candidate := bytes.TrimSuffix(tail[start:end], []byte{'\r'})
		if len(bytes.TrimSpace(candidate)) > 0 {
			if newline < 0 && !startsAtFileBeginning {
				return nil, false, nil
			}
			if len(candidate) > maxScannerTokenSize {
				return nil, true, fmt.Errorf("jsonlstore: latest record exceeds %d bytes", maxScannerTokenSize)
			}
			return append([]byte(nil), candidate...), true, nil
		}
		if newline < 0 {
			if startsAtFileBeginning {
				return nil, true, nil
			}
			return nil, false, nil
		}
		end = newline
	}
}
