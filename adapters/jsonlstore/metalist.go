package jsonlstore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// metaSnapshot is the SMALL decode target for a cheap picker listing: it
// carries ONLY the fields a /sessions row needs (id, state, counters.turns,
// model id, title, created_at) and SKIPS the messages array entirely. Go's
// encoding/json ignores unknown fields, so json.Unmarshal(lastLine, &meta) into
// metadata wrapper when current snapshots are written. It must remain a strict
// subset of sessnap.Snapshot because legacy snapshot files use it as a cheap
// decode target.
type metaSnapshot struct {
	ID              session.SessionID           `json:"id"`
	State           session.State               `json:"state"`
	Counters        session.Counters            `json:"counters"`
	ModelID         string                      `json:"model_id,omitempty"`
	Title           string                      `json:"title,omitempty"`
	TitleProvenance session.TitleProvenance     `json:"title_provenance,omitempty"`
	Kind            session.SessionKind         `json:"kind,omitempty"`
	Relationship    session.SessionRelationship `json:"relationship,omitzero"`
	EnvironmentRef  session.EnvironmentRef      `json:"environment_ref,omitzero"`
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
// than the seek window is read whole (small file = fast). Catalog rebuilds use
// a dedicated process mutex plus cross-process catalog flock; they never take a
// store-wide session-operation lock.
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

func metaSnapshotFromSession(s *session.Session) metaSnapshot {
	return metaSnapshot{
		ID: s.ID, State: s.State, Counters: s.Counters, ModelID: s.ModelID,
		Title: s.Title, TitleProvenance: s.TitleProvenance, Kind: s.Kind,
		Relationship: s.Relationship, EnvironmentRef: s.EnvironmentRef, CreatedAt: s.CreatedAt,
		Owner: s.Owner,
	}
}

func (st *Store) discoveryMetaList(ctx context.Context) ([]port.SessionDiscoveryMeta, error) {
	st.inventoryMu.Lock()
	defer st.inventoryMu.Unlock()
	var rows []port.SessionDiscoveryMeta
	err := st.withInventoryCatalogLock(ctx, func() error {
		var err error
		rows, err = st.discoveryMetaListLocked(ctx)
		return err
	})
	return rows, err
}

func (st *Store) discoveryMetaListLocked(ctx context.Context) ([]port.SessionDiscoveryMeta, error) {
	// A durable catalog is derivative only. Every read first fingerprints the
	// authoritative snapshot directory entries, so another Store's atomic save,
	// create, remove, or promotion invalidates it without relying on process memory.
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fingerprint, err := st.inventoryFingerprint()
		if err != nil {
			return nil, err
		}
		if rows, ok := st.readInventoryCatalog(fingerprint); ok {
			return rows, nil
		}
		sources, err := st.inventorySources()
		if err != nil {
			return nil, err
		}

		st.observeInventoryWork(inventoryWorkRebuild)
		rows, err := st.rebuildInventoryRows()
		if err != nil {
			return nil, err
		}
		after, err := st.inventoryFingerprint()
		if err != nil {
			return nil, err
		}
		afterSources, err := st.inventorySources()
		if err != nil {
			return nil, err
		}
		if after != fingerprint || !maps.Equal(afterSources, sources) {
			continue // a shared-directory writer changed the source during rebuild
		}
		generation := inventoryGeneration(after, afterSources)
		if err := st.writeInventoryCatalog(after, afterSources, rows); err != nil {
			return nil, err
		}
		if err := st.reconcileInventoryArtifacts(generation); err != nil {
			return nil, err
		}
		return rows, nil
	}
	return nil, fmt.Errorf("jsonlstore: inventory changed repeatedly during catalog rebuild")
}

func (st *Store) rebuildInventoryRows() ([]port.SessionDiscoveryMeta, error) {
	files, err := st.resolver.snapshotFiles()
	if err != nil {
		return nil, err
	}
	out := make([]port.SessionDiscoveryMeta, 0, len(files))
	for _, file := range files {
		st.observeInventoryWork(inventoryWorkSnapshotRead)
		meta := port.SessionDiscoveryMeta{ID: file.id, ModifiedAt: file.modified, EstimatedBytes: file.estimatedBytes}
		var m metaSnapshot
		if file.metadata != nil {
			m = *file.metadata
		} else if err := json.Unmarshal(file.last, &m); err != nil {
			out = append(out, meta)
			continue
		}
		if knownStates[m.State] {
			kind := m.Kind
			if kind == "" {
				kind = session.SessionKindUnknown
			}
			if session.ValidateSessionMetadata(kind, m.Relationship) == nil {
				meta.State = m.State
				meta.Turns = m.Counters.Turns
				meta.ModelID = m.ModelID
				meta.Title = m.Title
				meta.TitleProvenance = m.TitleProvenance
				meta.EnvironmentRef = m.EnvironmentRef
				meta.Kind = kind
				meta.Relationship = m.Relationship
				meta.Activity = session.ValidActivity(file.activity)
				meta.Owner = m.Owner
				meta.CreatedAt = m.CreatedAt
			}
		}
		out = append(out, meta)
	}
	return out, nil
}

// SupportsSessionActivityProjection reports that the JSONL metadata catalog
// atomically reflects the latest snapshot's activity.
func (*Store) SupportsSessionActivityProjection() bool { return true }

// PageSessionMetadata reads at most Limit+1 rows from the owner-specific,
// pre-ordered derivative catalog. The cursor's byte position seeks directly to
// page two; no prior catalog row or snapshot payload is traversed.
func (st *Store) PageSessionMetadata(ctx context.Context, request port.SessionMetadataPageRequest) (port.SessionMetadataPage, error) {
	if request.Limit < 0 {
		return port.SessionMetadataPage{}, fmt.Errorf("jsonlstore: metadata page limit must be non-negative")
	}
	st.inventoryMu.Lock()
	defer st.inventoryMu.Unlock()
	var page port.SessionMetadataPage
	err := st.withInventoryCatalogLock(ctx, func() error {
		var err error
		page, err = st.pageSessionMetadataLocked(ctx, request)
		return err
	})
	return page, err
}

func (st *Store) pageSessionMetadataLocked(ctx context.Context, request port.SessionMetadataPageRequest) (port.SessionMetadataPage, error) {
	scopeKey := inventoryGlobalScope
	if request.OwnershipEnforced {
		scopeKey = inventoryOwnerScope(request.Owner)
	}
	catalog, err := st.readyInventoryCatalog(ctx, request.Cursor)
	if err != nil {
		return port.SessionMetadataPage{}, err
	}
	if !inventoryCursorMatches(request.Cursor, catalog.Generation, scopeKey) {
		return port.SessionMetadataPage{}, port.ErrSessionMetadataCursorRestart
	}
	scope, ok := catalog.Scopes[scopeKey]
	if !ok {
		if request.Cursor != nil {
			return port.SessionMetadataPage{}, port.ErrSessionMetadataCursorRestart
		}
		return port.SessionMetadataPage{TotalCount: 0}, nil
	}
	rows, nextPosition, hasMore, err := st.readInventoryScopePage(ctx, request, scope)
	if err != nil {
		return port.SessionMetadataPage{}, err
	}
	page := port.SessionMetadataPage{Sessions: rows, TotalCount: scope.Count}
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		page.NextCursor = &port.SessionMetadataCursor{
			ModifiedAt: last.ModifiedAt, ID: last.ID, Generation: catalog.Generation,
			Scope: scopeKey, Continuation: encodeInventoryContinuation(nextPosition),
		}
	}
	return page, nil
}

func (st *Store) readyInventoryCatalog(ctx context.Context, cursor *port.SessionMetadataCursor) (inventoryCatalog, error) {
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return inventoryCatalog{}, err
		}
		fingerprint, err := st.inventoryFingerprint()
		if err != nil {
			return inventoryCatalog{}, err
		}
		if catalog, ready := st.readInventoryManifest(fingerprint); ready {
			return catalog, nil
		}
		if cursor != nil {
			return inventoryCatalog{}, port.ErrSessionMetadataCursorRestart
		}
		if _, err := st.discoveryMetaListLocked(ctx); err != nil {
			return inventoryCatalog{}, err
		}
		if st.inventoryCatalogReadyObserver != nil {
			st.inventoryCatalogReadyObserver()
		}
		fingerprint, err = st.inventoryFingerprint()
		if err != nil {
			return inventoryCatalog{}, err
		}
		if catalog, ready := st.readInventoryManifest(fingerprint); ready {
			return catalog, nil
		}
	}
	return inventoryCatalog{}, fmt.Errorf("jsonlstore: inventory changed repeatedly while preparing catalog")
}

const inventoryContinuationPrefix = "jsonl-v1."

func encodeInventoryContinuation(position int64) string {
	return inventoryContinuationPrefix + base64.RawURLEncoding.EncodeToString(strconv.AppendInt(nil, position, 10))
}

func decodeInventoryContinuation(token string) (int64, bool) {
	if len(token) <= len(inventoryContinuationPrefix) || token[:len(inventoryContinuationPrefix)] != inventoryContinuationPrefix {
		return 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(token[len(inventoryContinuationPrefix):])
	if err != nil {
		return 0, false
	}
	position, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || position < 0 || encodeInventoryContinuation(position) != token {
		return 0, false
	}
	return position, true
}

func inventoryCursorMatches(cursor *port.SessionMetadataCursor, generation, scope string) bool {
	if cursor == nil {
		return true
	}
	_, valid := decodeInventoryContinuation(cursor.Continuation)
	return cursor.Generation == generation && cursor.Scope == scope && valid
}

func (st *Store) readInventoryScopePage(ctx context.Context, request port.SessionMetadataPageRequest, scope inventoryCatalogScope) ([]port.SessionDiscoveryMeta, int64, bool, error) {
	position := int64(0)
	if request.Cursor != nil {
		var valid bool
		position, valid = decodeInventoryContinuation(request.Cursor.Continuation)
		if !valid {
			return nil, 0, false, port.ErrSessionMetadataCursorRestart
		}
	}
	f, err := os.Open(filepath.Join(st.inventoryCatalogDir(), scope.File)) //nolint:gosec // manifest-validated adapter-private path
	if err != nil {
		if request.Cursor != nil {
			return nil, 0, false, port.ErrSessionMetadataCursorRestart
		}
		return nil, 0, false, fmt.Errorf("jsonlstore: open inventory scope: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(position, io.SeekStart); err != nil {
		return nil, 0, false, port.ErrSessionMetadataCursorRestart
	}
	reader := bufio.NewReaderSize(f, 64*1024)
	capacity := request.Limit
	if capacity > scope.Count {
		capacity = scope.Count
	}
	rows := make([]port.SessionDiscoveryMeta, 0, capacity)
	nextPosition := position
	for len(rows) <= request.Limit {
		line, readErr := readInventoryLine(ctx, reader)
		if readErr != nil {
			if readErr == io.EOF {
				return rows, nextPosition, false, nil
			}
			return nil, 0, false, readErr
		}
		st.observeInventoryWork(inventoryWorkCatalogRow)
		row, err := decodeInventoryPageRow(line, request, rows)
		if err != nil {
			return nil, 0, false, err
		}
		if len(rows) == request.Limit {
			return rows, nextPosition, true, nil
		}
		rows = append(rows, row)
		nextPosition += int64(len(line))
	}
	return rows, nextPosition, false, nil
}

func readInventoryLine(ctx context.Context, reader *bufio.Reader) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	line, err := reader.ReadBytes('\n')
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("jsonlstore: read inventory row: %w", err)
	}
	if len(line) == 0 && err == io.EOF {
		return nil, io.EOF
	}
	return line, nil
}

func decodeInventoryPageRow(line []byte, request port.SessionMetadataPageRequest, rows []port.SessionDiscoveryMeta) (port.SessionDiscoveryMeta, error) {
	var row port.SessionDiscoveryMeta
	if err := json.Unmarshal(line, &row); err != nil || !validInventoryRows([]port.SessionDiscoveryMeta{row}) {
		if request.Cursor != nil {
			return port.SessionDiscoveryMeta{}, port.ErrSessionMetadataCursorRestart
		}
		return port.SessionDiscoveryMeta{}, fmt.Errorf("jsonlstore: invalid inventory row")
	}
	if request.OwnershipEnforced && (request.Owner == nil || !request.Owner.SameIdentity(row.Owner)) {
		return port.SessionDiscoveryMeta{}, fmt.Errorf("jsonlstore: inventory scope contains a foreign owner")
	}
	if request.Cursor != nil && len(rows) == 0 && !metadataRowAfter(row, request.Cursor) {
		return port.SessionDiscoveryMeta{}, port.ErrSessionMetadataCursorRestart
	}
	if len(rows) > 0 {
		previous := rows[len(rows)-1]
		if !metadataRowAfter(row, &port.SessionMetadataCursor{ModifiedAt: previous.ModifiedAt, ID: previous.ID}) {
			return port.SessionDiscoveryMeta{}, fmt.Errorf("jsonlstore: inventory rows are out of order")
		}
	}
	return row, nil
}

func metadataRowAfter(row port.SessionDiscoveryMeta, cursor *port.SessionMetadataCursor) bool {
	cursorRow := port.SessionDiscoveryMeta{ModifiedAt: cursor.ModifiedAt, ID: cursor.ID}
	return port.CompareSessionMetadataOrder(row, cursorRow) > 0
}

// readLastLineAt returns the last non-blank record without reading older history.
// It grows an EOF window geometrically until it finds the delimiter immediately
// before that record, so bytes read and allocated are bounded by a small constant
// factor of the latest record rather than by the append-only file's total size.
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
